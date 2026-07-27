package main

// routes_pairing_test.go — HTTP-level gating + routing tests for the pairing
// and compromised-device-removal endpoints. The cryptographic guarantees
// themselves are proven in services/pairing and services/devicekey; these tests
// pin the owner + step-up gate (fail-closed on nil authStore / no session /
// non-owner / missing step-up) and the request routing.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/auth"
	"vulos/backend/services/devicekey"
	"vulos/backend/services/stepup"
)

type pairingTestEnv struct {
	mux     *http.ServeMux
	adminID string
	userID  string
}

func newPairingTestEnv(t *testing.T, withAuth bool) pairingTestEnv {
	t.Helper()
	// Point stepup + any datadir consumer at an isolated dir so minted step-up
	// tokens verify within the test.
	t.Setenv("VULOS_DATA_DIR", t.TempDir())

	var st *auth.Store
	var adminID, userID string
	if withAuth {
		var err error
		st, err = auth.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("auth.NewStore: %v", err)
		}
		admin, err := st.Register("owner", "ownerpw123-secure!", "Owner")
		if err != nil {
			t.Fatalf("register owner: %v", err)
		}
		user, err := st.Register("bob", "bobpw123-secure!", "Bob")
		if err != nil {
			t.Fatalf("register user: %v", err)
		}
		adminID, userID = admin.ID, user.ID
		if p, ok := st.GetProfile(adminID); !ok || p.Role != auth.RoleAdmin {
			t.Fatalf("first user is not admin/owner: %+v", p)
		}
	}

	ks, err := devicekey.Open(t.TempDir())
	if err != nil {
		t.Fatalf("devicekey.Open: %v", err)
	}
	t.Cleanup(func() { _ = ks.Close() })
	revStore, err := devicekey.NewRevocationStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewRevocationStore: %v", err)
	}

	mux := http.NewServeMux()
	registerPairingRoutes(mux, pairingDeps{
		DBDir:           filepath.Join(t.TempDir(), "db"),
		KeyStore:        ks,
		RevocationStore: revStore,
		AuthStore:       st, // nil when withAuth == false
		Registry:        nil,
	})
	return pairingTestEnv{mux: mux, adminID: adminID, userID: userID}
}

// do drives a route the way the auth middleware would: X-User-ID is the VERIFIED
// caller. When stepUp is true a fresh elevated token is minted for userID.
func (e pairingTestEnv) do(t *testing.T, method, path, userID string, stepUp bool, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	if stepUp {
		tok, _, err := stepup.Mint(userID)
		if err != nil {
			t.Fatalf("stepup.Mint: %v", err)
		}
		req.Header.Set("X-Stepup-Token", tok)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// ─── issue gating ────────────────────────────────────────────────────────────────

func TestPairingIssue_Gating(t *testing.T) {
	e := newPairingTestEnv(t, true)

	if rec := e.do(t, "POST", "/api/pairing/issue", "", false, "{}"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: code = %d, want 401", rec.Code)
	}
	if rec := e.do(t, "POST", "/api/pairing/issue", e.userID, true, "{}"); rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner: code = %d, want 403", rec.Code)
	}
	if rec := e.do(t, "POST", "/api/pairing/issue", e.adminID, false, "{}"); rec.Code != http.StatusForbidden {
		t.Fatalf("owner without step-up: code = %d, want 403", rec.Code)
	}
	rec := e.do(t, "POST", "/api/pairing/issue", e.adminID, true, `{"device_name":"Laptop"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner + step-up: code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		ShortCode string `json:"short_code"`
		Token     string `json:"token"`
		QR        string `json:"qr_payload"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	if resp.ShortCode == "" || resp.Token == "" || !strings.HasPrefix(resp.QR, "vulos://pair/v1?") {
		t.Fatalf("issue response missing fields: %+v", resp)
	}
}

func TestPairingIssue_FailsClosedOnNilAuthStore(t *testing.T) {
	e := newPairingTestEnv(t, false) // AuthStore == nil
	// Even with a plausible X-User-ID + step-up, a nil auth store cannot resolve
	// an owner, so the gate must refuse.
	if rec := e.do(t, "POST", "/api/pairing/issue", "someone", true, "{}"); rec.Code != http.StatusForbidden {
		t.Fatalf("nil authStore: code = %d, want 403 (fail closed)", rec.Code)
	}
}

// ─── claim: unauthenticated, single-use ─────────────────────────────────────────

func TestPairingClaim_EndToEnd(t *testing.T) {
	e := newPairingTestEnv(t, true)

	// Owner issues a ticket.
	issue := e.do(t, "POST", "/api/pairing/issue", e.adminID, true, `{"device_name":"New Phone"}`)
	if issue.Code != http.StatusOK {
		t.Fatalf("issue: %d %s", issue.Code, issue.Body.String())
	}
	var iresp struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(issue.Body.Bytes(), &iresp)

	// A brand-new device (no session) claims it with its OWN public key.
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	keyB64 := base64.StdEncoding.EncodeToString(der)
	claimBody, _ := json.Marshal(map[string]string{"token": iresp.Token, "device_public_key": keyB64})

	rec := e.do(t, "POST", "/api/pairing/claim", "", false, string(claimBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var cres struct {
		Status   string `json:"status"`
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cres); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if cres.Status != "enrolled" || cres.DeviceID == "" {
		t.Fatalf("unexpected claim result: %+v", cres)
	}
	// The raw response must not carry storage account secrets.
	if strings.Contains(rec.Body.String(), "secret_key") || strings.Contains(rec.Body.String(), "access_key") {
		t.Fatal("claim response leaked storage secret field names")
	}

	// Single use: replaying the same token now 404s.
	if rec2 := e.do(t, "POST", "/api/pairing/claim", "", false, string(claimBody)); rec2.Code != http.StatusNotFound {
		t.Fatalf("replayed claim: code = %d, want 404", rec2.Code)
	}
}

func TestPairingClaim_Validation(t *testing.T) {
	e := newPairingTestEnv(t, true)
	// Missing required fields.
	if rec := e.do(t, "POST", "/api/pairing/claim", "", false, `{"token":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing device key: code = %d, want 400", rec.Code)
	}
	// Unknown token.
	body, _ := json.Marshal(map[string]string{"token": "made-up", "device_public_key": base64.StdEncoding.EncodeToString(make([]byte, 64))})
	if rec := e.do(t, "POST", "/api/pairing/claim", "", false, string(body)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token: code = %d, want 404", rec.Code)
	}
}

// ─── revoke gating + validation ─────────────────────────────────────────────────

func TestPairingRevoke_Gating(t *testing.T) {
	e := newPairingTestEnv(t, true)

	if rec := e.do(t, "POST", "/api/pairing/revoke", "", false, "{}"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: code = %d, want 401", rec.Code)
	}
	if rec := e.do(t, "POST", "/api/pairing/revoke", e.userID, true, "{}"); rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner: code = %d, want 403", rec.Code)
	}
	if rec := e.do(t, "POST", "/api/pairing/revoke", e.adminID, false, "{}"); rec.Code != http.StatusForbidden {
		t.Fatalf("owner without step-up: code = %d, want 403", rec.Code)
	}
	// Owner + step-up but missing device_id → 400.
	if rec := e.do(t, "POST", "/api/pairing/revoke", e.adminID, true, "{}"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing device_id: code = %d, want 400", rec.Code)
	}
	// device_id present but no quorum bundle → 400.
	if rec := e.do(t, "POST", "/api/pairing/revoke", e.adminID, true, `{"device_id":"abc"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing quorum: code = %d, want 400", rec.Code)
	}
	// Full validation passes (request_id + a cert) but the device is unknown → 404.
	body := `{"device_id":"no-such-device","request_id":"r1","quorum_certs":[{"type":"fleet-vouch"}]}`
	if rec := e.do(t, "POST", "/api/pairing/revoke", e.adminID, true, body); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown device: code = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

// ─── devices list gating ─────────────────────────────────────────────────────────

func TestPairingDevices_Gating(t *testing.T) {
	e := newPairingTestEnv(t, true)
	if rec := e.do(t, "GET", "/api/pairing/devices", "", false, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: code = %d, want 401", rec.Code)
	}
	if rec := e.do(t, "GET", "/api/pairing/devices", e.userID, false, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner: code = %d, want 403", rec.Code)
	}
	rec := e.do(t, "GET", "/api/pairing/devices", e.adminID, false, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner: code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "devices") {
		t.Fatalf("unexpected devices body: %s", rec.Body.String())
	}
}
