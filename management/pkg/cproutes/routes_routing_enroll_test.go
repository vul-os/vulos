package cproutes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// enrollTestEnv bundles a fresh mux, store, and two authenticated sessions so
// the auth/ownership tests (audit C1/C2/H2) can exercise owner vs non-owner.
type enrollTestEnv struct {
	mux       *http.ServeMux
	st        routing.Store
	authStore *auth.Store
	token1    string // session for acct1
	acct1     string // account id for token1
	token2    string // session for acct2
	acct2     string // account id for token2
}

// testMux builds a fresh ServeMux backed by an in-memory store and a real
// auth.Store with two signed-up users. RegisterEnrollRoutes is gated behind the
// session, so every request must carry a session cookie.
func testMux(t *testing.T) enrollTestEnv {
	t.Helper()
	st := routing.NewMemStore()

	authSt, err := openAuthStoreForTest(":memory:", []byte("testsecret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { authSt.Close() }) //nolint:errcheck

	u1, tok1, err := authSt.Signup(context.Background(), "one@example.com", "supersecretpassword1", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Signup u1: %v", err)
	}
	u2, tok2, err := authSt.Signup(context.Background(), "two@example.com", "supersecretpassword2", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Signup u2: %v", err)
	}

	mux := http.NewServeMux()
	RegisterEnrollRoutes(mux, st, authSt)
	return enrollTestEnv{
		mux: mux, st: st, authStore: authSt,
		token1: tok1, acct1: u1.ID,
		token2: tok2, acct2: u2.ID,
	}
}

// post POSTs JSON to the mux with the given session token and returns the recorder.
func post(t *testing.T, mux *http.ServeMux, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func decodeResp(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// validULID is a canonical 26-char Crockford ULID used across tests.
const validULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// ─── auth enforcement (audit C1/C2/H2) ───────────────────────────────────────

func TestEnroll_RequiresSession(t *testing.T) {
	env := testMux(t)
	for _, path := range []string{"/api/enroll", "/api/enroll/direct", "/api/connmode"} {
		rr := post(t, env.mux, path, map[string]any{"ulid": validULID, "mode": "fabric", "direct_ip": "1.2.3.4"}, "")
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s without session: want 401, got %d", path, rr.Code)
		}
	}
}

func TestEnroll_AccountFromSession_NotBody(t *testing.T) {
	env := testMux(t)
	// Attacker (acct1) tries to forge account_id in the body — it must be ignored
	// and the binding owned by the session account (acct1).
	rr := post(t, env.mux, "/api/enroll", map[string]any{
		"ulid":       validULID,
		"account_id": "attacker-supplied-acct",
		"mode":       "fabric",
	}, env.token1)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}
	var resp enrollResponse
	decodeResp(t, rr, &resp)
	if resp.AccountID != env.acct1 {
		t.Errorf("account_id must come from session (%s), got %q", env.acct1, resp.AccountID)
	}
}

func TestConnMode_NonOwner_403(t *testing.T) {
	env := testMux(t)
	// acct1 enrolls the ULID.
	if rr := post(t, env.mux, "/api/enroll", map[string]any{"ulid": validULID, "mode": "fabric"}, env.token1); rr.Code != http.StatusOK {
		t.Fatalf("enroll: want 200, got %d: %s", rr.Code, rr.Body)
	}
	// acct2 (not the owner) tries to flip the conn mode → 403.
	rr := post(t, env.mux, "/api/connmode", map[string]any{"ulid": validULID, "mode": "own-domain"}, env.token2)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-owner connmode: want 403, got %d: %s", rr.Code, rr.Body)
	}
}

func TestEnrollDirect_NonOwner_409(t *testing.T) {
	env := testMux(t)
	// acct1 enrolls the ULID in fabric mode.
	if rr := post(t, env.mux, "/api/enroll", map[string]any{"ulid": validULID, "mode": "fabric"}, env.token1); rr.Code != http.StatusOK {
		t.Fatalf("enroll: want 200, got %d: %s", rr.Code, rr.Body)
	}
	// acct2 tries to claim it via direct-enroll → 409 (bound to another account).
	rr := post(t, env.mux, "/api/enroll/direct", map[string]any{"ulid": validULID, "direct_ip": "203.0.113.9"}, env.token2)
	if rr.Code != http.StatusConflict {
		t.Fatalf("non-owner direct-enroll: want 409, got %d: %s", rr.Code, rr.Body)
	}
}

// ─── POST /api/enroll ────────────────────────────────────────────────────────

func TestEnroll_HappyPath(t *testing.T) {
	env := testMux(t)
	rr := post(t, env.mux, "/api/enroll", map[string]any{"ulid": validULID, "mode": "fabric"}, env.token1)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}
	var resp enrollResponse
	decodeResp(t, rr, &resp)

	if resp.ULID != validULID {
		t.Errorf("ulid mismatch: want %q, got %q", validULID, resp.ULID)
	}
	if resp.AccountID != env.acct1 {
		t.Errorf("account_id mismatch")
	}
	if resp.Mode != routing.ModeFabric {
		t.Errorf("mode mismatch: want fabric, got %q", resp.Mode)
	}
	if resp.CreatedAt == "" {
		t.Error("created_at should not be empty")
	}
	if resp.AcmeDNS.Username == "" {
		t.Error("acme_dns.username must not be empty")
	}
	if resp.AcmeDNS.Password == "" {
		t.Error("acme_dns.password must not be empty")
	}
	if resp.AcmeDNS.Subdomain == "" {
		t.Error("acme_dns.subdomain must not be empty")
	}
	if resp.AcmeDNS.FullDomain == "" {
		t.Error("acme_dns.fulldomain must not be empty")
	}
	wantSub := strings.ToLower(validULID)
	if resp.AcmeDNS.Subdomain != wantSub {
		t.Errorf("acme_dns.subdomain: want %q, got %q", wantSub, resp.AcmeDNS.Subdomain)
	}
}

func TestEnroll_NoTLSKeyField(t *testing.T) {
	env := testMux(t)
	rr := post(t, env.mux, "/api/enroll", map[string]any{"ulid": validULID, "mode": "fabric"}, env.token1)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(strings.NewReader(rr.Body.String())).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	forbidden := []string{"tls_key", "private_key", "key", "tls_private_key"}
	for _, f := range forbidden {
		if _, ok := raw[f]; ok {
			t.Errorf("response must not contain field %q", f)
		}
	}
	if acmeDNSRaw, ok := raw["acme_dns"]; ok {
		var acmeMap map[string]json.RawMessage
		if err := json.Unmarshal(acmeDNSRaw, &acmeMap); err == nil {
			for _, f := range forbidden {
				if _, ok := acmeMap[f]; ok {
					t.Errorf("acme_dns must not contain field %q", f)
				}
			}
		}
	}
}

func TestEnroll_IdempotentSameAccount(t *testing.T) {
	env := testMux(t)
	body := map[string]any{"ulid": validULID, "mode": "fabric"}
	rr1 := post(t, env.mux, "/api/enroll", body, env.token1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first enroll: want 200, got %d", rr1.Code)
	}
	rr2 := post(t, env.mux, "/api/enroll", body, env.token1)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second enroll (idempotent): want 200, got %d: %s", rr2.Code, rr2.Body)
	}
	var r1, r2 enrollResponse
	decodeResp(t, rr1, &r1)
	decodeResp(t, rr2, &r2)
	if r1.ULID != r2.ULID {
		t.Errorf("ULID changed between idempotent calls")
	}
	if r1.AccountID != r2.AccountID {
		t.Errorf("AccountID changed between idempotent calls")
	}
}

func TestEnroll_CrossAccount_409(t *testing.T) {
	env := testMux(t)
	post(t, env.mux, "/api/enroll", map[string]any{"ulid": validULID, "mode": "fabric"}, env.token1)
	rr := post(t, env.mux, "/api/enroll", map[string]any{"ulid": validULID, "mode": "fabric"}, env.token2)
	if rr.Code != http.StatusConflict {
		t.Fatalf("cross-account re-enroll: want 409, got %d: %s", rr.Code, rr.Body)
	}
}

func TestEnroll_InvalidULID_400(t *testing.T) {
	env := testMux(t)
	rr := post(t, env.mux, "/api/enroll", map[string]any{"ulid": "not-a-valid-ulid", "mode": "fabric"}, env.token1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid ULID: want 400, got %d", rr.Code)
	}
}

func TestEnroll_InvalidMode_400(t *testing.T) {
	env := testMux(t)
	rr := post(t, env.mux, "/api/enroll", map[string]any{"ulid": validULID, "mode": "local"}, env.token1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode: want 400, got %d", rr.Code)
	}
}

func TestEnroll_ModeD_Rejected(t *testing.T) {
	env := testMux(t)
	for _, m := range []string{"local", "d", "D", "mode-d", ""} {
		rr := post(t, env.mux, "/api/enroll", map[string]any{"ulid": validULID, "mode": m}, env.token1)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("mode %q: want 400, got %d", m, rr.Code)
		}
	}
}

// ─── POST /api/enroll/direct ─────────────────────────────────────────────────

func TestEnrollDirect_SetsIP(t *testing.T) {
	env := testMux(t)
	rr := post(t, env.mux, "/api/enroll/direct", map[string]any{"ulid": validULID, "direct_ip": "203.0.113.42"}, env.token1)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}
	var resp enrollResponse
	decodeResp(t, rr, &resp)
	if resp.Mode != routing.ModeDirect {
		t.Errorf("mode: want direct, got %q", resp.Mode)
	}
	b, err := env.st.GetBinding(context.Background(), validULID)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if b.DirectIP != "203.0.113.42" {
		t.Errorf("DirectIP: want 203.0.113.42, got %q", b.DirectIP)
	}
}

func TestEnrollDirect_AcmeDNSPresent_NoTLSKey(t *testing.T) {
	env := testMux(t)
	rr := post(t, env.mux, "/api/enroll/direct", map[string]any{"ulid": validULID, "direct_ip": "203.0.113.42"}, env.token1)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var resp enrollResponse
	decodeResp(t, rr, &resp)
	if resp.AcmeDNS.Username == "" || resp.AcmeDNS.Password == "" {
		t.Error("acme_dns creds must be present in direct-enroll response")
	}
	raw := rr.Body.String()
	for _, kw := range []string{"tls_key", "private_key"} {
		if strings.Contains(raw, kw) {
			t.Errorf("response body must not contain %q", kw)
		}
	}
}

func TestEnrollDirect_InvalidULID_400(t *testing.T) {
	env := testMux(t)
	rr := post(t, env.mux, "/api/enroll/direct", map[string]any{"ulid": "BADULIDXXX", "direct_ip": "1.2.3.4"}, env.token1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

// ─── POST /api/connmode ──────────────────────────────────────────────────────

func TestConnMode_HappyPath(t *testing.T) {
	env := testMux(t)
	post(t, env.mux, "/api/enroll", map[string]any{"ulid": validULID, "mode": "fabric"}, env.token1)
	rr := post(t, env.mux, "/api/connmode", map[string]any{"ulid": validULID, "mode": "own-domain"}, env.token1)
	if rr.Code != http.StatusOK {
		t.Fatalf("connmode: want 200, got %d: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	decodeResp(t, rr, &resp)
	if resp["mode"] != "own-domain" {
		t.Errorf("mode: want own-domain, got %v", resp["mode"])
	}
}

func TestConnMode_InvalidULID_400(t *testing.T) {
	env := testMux(t)
	rr := post(t, env.mux, "/api/connmode", map[string]any{"ulid": "INVALID", "mode": "fabric"}, env.token1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestConnMode_InvalidMode_400(t *testing.T) {
	env := testMux(t)
	post(t, env.mux, "/api/enroll", map[string]any{"ulid": validULID, "mode": "fabric"}, env.token1)
	rr := post(t, env.mux, "/api/connmode", map[string]any{"ulid": validULID, "mode": "local"}, env.token1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("mode D: want 400, got %d", rr.Code)
	}
}

func TestConnMode_ModeD_NeverSynthesised(t *testing.T) {
	env := testMux(t)
	post(t, env.mux, "/api/enroll", map[string]any{"ulid": validULID, "mode": "fabric"}, env.token1)
	for _, m := range []string{"local", "d", "D", "mode-d"} {
		rr := post(t, env.mux, "/api/connmode", map[string]any{"ulid": validULID, "mode": m}, env.token1)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("mode %q must be rejected with 400, got %d", m, rr.Code)
		}
	}
}

func TestConnMode_NotEnrolled_404(t *testing.T) {
	env := testMux(t)
	rr := post(t, env.mux, "/api/connmode", map[string]any{"ulid": validULID, "mode": "fabric"}, env.token1)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

// ─── acme-dns creds struct: no TLS key fields ────────────────────────────────

func TestAcmeDNSCreds_StructHasNoTLSKeyField(t *testing.T) {
	rt := reflect.TypeOf(routing.AcmeDNSCreds{})
	forbidden := []string{"TLSKey", "PrivateKey", "Key", "TLSPrivateKey"}
	for _, name := range forbidden {
		if _, ok := rt.FieldByName(name); ok {
			t.Errorf("AcmeDNSCreds must not have field %q", name)
		}
	}
	creds, err := routing.GenerateAcmeDNSCreds(validULID)
	if err != nil {
		t.Fatalf("GenerateAcmeDNSCreds: %v", err)
	}
	b, _ := json.Marshal(creds)
	raw := string(b)
	for _, kw := range []string{"tls_key", "private_key", "key\""} {
		if strings.Contains(raw, kw) {
			t.Errorf("AcmeDNSCreds JSON must not contain %q: %s", kw, raw)
		}
	}
}

func TestAcmeDNSCreds_DeterministicSubdomain(t *testing.T) {
	c1, _ := routing.GenerateAcmeDNSCreds(validULID)
	c2, _ := routing.GenerateAcmeDNSCreds(validULID)
	if c1.Subdomain != c2.Subdomain {
		t.Errorf("subdomain must be deterministic: %q != %q", c1.Subdomain, c2.Subdomain)
	}
	if c1.FullDomain != c2.FullDomain {
		t.Errorf("fulldomain must be deterministic")
	}
	if c1.Username == c2.Username {
		t.Error("username should be random per call (coincidence is astronomically unlikely)")
	}
	if c1.Password == c2.Password {
		t.Error("password should be random per call")
	}
}
