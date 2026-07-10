package main

// routes_identity_claim_test.go — IDENTITY-01 coverage for the @vulos.to claim
// proxies (routes_identity_claim.go):
//
//   GET  /api/identity/check  — forwards handle/domain + Cookie to the CP, relays
//                               CP status/body, and returns {"offline":true} when
//                               the CP is unreachable.
//   POST /api/identity/claim  — forwards ONLY {handle,domain} + Cookie (never an
//                               account id) to the CP, relays CP status/body
//                               verbatim, and is OS-session gated by the auth
//                               middleware (401 without a session).
//
// OFFLINE FIRST-BOOT (device-cert forwarding): when the box has completed
// owner-attested cloud enrollment, the claim proxy ALSO forwards the box's device
// certificate (INTEG-SEC-01: X-Device-ULID/Pubkey/Cert/Sig) so the CP can
// authenticate the enrolled device when the wizard holds no CP session cookie yet.
// The signature is purpose-bound ("identity:claim:<ulid>") and made with the device
// key — proving the box (not the browser) holds it. When no enrolled identity is
// installed, NO device headers are sent (session-only path unchanged).

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/auth"
)

// fakeCP stands up a minimal control-plane that records the last request it saw
// and replies with a canned status + body.
type fakeCP struct {
	srv         *httptest.Server
	lastPath    string
	lastQuery   string
	lastCookie  string
	lastBody    string
	lastHeaders http.Header
	status      int
	body        string
}

func newFakeCP(t *testing.T, status int, body string) *fakeCP {
	t.Helper()
	cp := &fakeCP{status: status, body: body}
	cp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cp.lastPath = r.URL.Path
		cp.lastQuery = r.URL.RawQuery
		cp.lastCookie = r.Header.Get("Cookie")
		cp.lastHeaders = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		cp.lastBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cp.status)
		w.Write([]byte(cp.body))
	}))
	t.Cleanup(cp.srv.Close)
	return cp
}

// ── GET /api/identity/check ─────────────────────────────────────────────────

func TestIdentityCheck_ForwardsAndRelays(t *testing.T) {
	cp := newFakeCP(t, http.StatusOK, `{"address":"alice@vulos.to","available":true}`)
	t.Setenv("VULOS_CLOUD_API_URL", cp.srv.URL)

	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	r := httptest.NewRequest(http.MethodGet, "/api/identity/check?handle=alice&domain=vulos.to", nil)
	r.Header.Set("Cookie", "cp_session=abc123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("check status = %d, want 200", w.Code)
	}
	if cp.lastPath != "/api/identity/check" {
		t.Fatalf("CP path = %q, want /api/identity/check", cp.lastPath)
	}
	if !strings.Contains(cp.lastQuery, "handle=alice") || !strings.Contains(cp.lastQuery, "domain=vulos.to") {
		t.Fatalf("CP query = %q, want handle+domain forwarded", cp.lastQuery)
	}
	if cp.lastCookie != "cp_session=abc123" {
		t.Fatalf("CP cookie = %q, want the incoming Cookie forwarded", cp.lastCookie)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("relayed body not JSON: %v", err)
	}
	if got["available"] != true || got["address"] != "alice@vulos.to" {
		t.Fatalf("relayed body = %v, want CP body verbatim", got)
	}
}

func TestIdentityCheck_OfflineWhenCPUnreachable(t *testing.T) {
	// Point at a closed port so the request fails at the transport layer.
	t.Setenv("VULOS_CLOUD_API_URL", "http://127.0.0.1:1")

	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	r := httptest.NewRequest(http.MethodGet, "/api/identity/check?handle=alice", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("offline status = %d, want 200 (soft state)", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("offline body not JSON: %v", err)
	}
	if got["offline"] != true {
		t.Fatalf("offline body = %v, want {\"offline\":true}", got)
	}
}

func TestIdentityCheck_MissingHandle400(t *testing.T) {
	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	r := httptest.NewRequest(http.MethodGet, "/api/identity/check", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing-handle status = %d, want 400", w.Code)
	}
}

// ── POST /api/identity/claim ────────────────────────────────────────────────

func TestIdentityClaim_ForwardsCookieAndRelaysStatus(t *testing.T) {
	cp := newFakeCP(t, http.StatusConflict, `{"error":"taken"}`)
	t.Setenv("VULOS_CLOUD_API_URL", cp.srv.URL)

	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	// Include a client-supplied account_id to prove the OS handler drops it.
	body := `{"handle":"alice","domain":"vulos.to","account_id":"attacker-supplied"}`
	r := httptest.NewRequest(http.MethodPost, "/api/identity/claim", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Cookie", "cp_session=sess-xyz")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	// CP 409 must be relayed verbatim.
	if w.Code != http.StatusConflict {
		t.Fatalf("claim status = %d, want 409 relayed", w.Code)
	}
	if cp.lastPath != "/api/identity/claim" {
		t.Fatalf("CP path = %q, want /api/identity/claim", cp.lastPath)
	}
	if cp.lastCookie != "cp_session=sess-xyz" {
		t.Fatalf("CP cookie = %q, want the incoming Cookie forwarded", cp.lastCookie)
	}
	// SECURITY: the OS handler must NOT forward a client-supplied account id.
	var forwarded map[string]any
	if err := json.Unmarshal([]byte(cp.lastBody), &forwarded); err != nil {
		t.Fatalf("forwarded body not JSON: %v", err)
	}
	if _, ok := forwarded["account_id"]; ok {
		t.Fatalf("forwarded body leaked account_id: %v — OS must not trust client-supplied account", forwarded)
	}
	if forwarded["handle"] != "alice" || forwarded["domain"] != "vulos.to" {
		t.Fatalf("forwarded body = %v, want only handle+domain", forwarded)
	}
}

func TestIdentityClaim_MissingHandle400(t *testing.T) {
	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	r := httptest.NewRequest(http.MethodPost, "/api/identity/claim", strings.NewReader(`{"domain":"vulos.to"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing-handle status = %d, want 400", w.Code)
	}
}

// TestIdentityClaim_RequiresLocalSession asserts the auth middleware gates the
// claim proxy: without an OS session it is 401 (it is NOT in publicPaths), while
// /api/identity/check is public (setup-time) and passes through.
func TestIdentityClaim_RequiresLocalSession(t *testing.T) {
	cp := newFakeCP(t, http.StatusOK, `{"address":"alice@vulos.to","claimed":true}`)
	t.Setenv("VULOS_CLOUD_API_URL", cp.srv.URL)

	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Register a first user so the instance is "provisioned" — otherwise the
	// middleware may allow unauthenticated first-setup registration paths.
	if _, err := store.Register("owner", "password-owner-123", "Owner"); err != nil {
		t.Fatalf("register owner: %v", err)
	}
	h := auth.NewHandler(store)

	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)
	srv := httptest.NewServer(h.Middleware(mux))
	t.Cleanup(srv.Close)

	// claim WITHOUT a session cookie → middleware 401 (never reaches the proxy).
	res, err := http.Post(srv.URL+"/api/identity/claim", "application/json",
		strings.NewReader(`{"handle":"alice","domain":"vulos.to"}`))
	if err != nil {
		t.Fatalf("post claim: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated claim status = %d, want 401 (auth-gated)", res.StatusCode)
	}

	// check is public (setup-time) → passes the middleware and reaches the proxy.
	res2, err := http.Get(srv.URL + "/api/identity/check?handle=alice")
	if err != nil {
		t.Fatalf("get check: %v", err)
	}
	defer res2.Body.Close()
	if res2.StatusCode == http.StatusUnauthorized {
		t.Fatalf("check returned 401 — it must be public (setup-time), not auth-gated")
	}
}

// ── OFFLINE FIRST-BOOT: device-cert forwarding ──────────────────────────────

// fakeDeviceAuther is a test identityDeviceAuther holding a real ed25519 device key
// and a canned cert blob, so the forwarded X-Device-Sig is a genuine signature the
// test can verify (proving the box, not the browser, holds the key).
type fakeDeviceAuther struct {
	priv    ed25519.PrivateKey
	pub     ed25519.PublicKey
	cert    []byte
	certOK  bool
	signErr error // when set, SignMint fails (simulates no key)
}

func newFakeDeviceAuther(t *testing.T) *fakeDeviceAuther {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return &fakeDeviceAuther{priv: priv, pub: pub, cert: []byte("ca-cert-blob"), certOK: true}
}

func (f *fakeDeviceAuther) DeviceCert() (cert, pubKey []byte, ok bool) {
	if !f.certOK {
		return nil, nil, false
	}
	return f.cert, f.pub, true
}

func (f *fakeDeviceAuther) SignMint(message string) ([]byte, error) {
	if f.signErr != nil {
		return nil, f.signErr
	}
	return ed25519.Sign(f.priv, []byte(message)), nil
}

// withIdentityDeviceAuth installs auther/ulid into the package globals for the
// duration of the test and restores them afterwards (test isolation).
func withIdentityDeviceAuth(t *testing.T, auther identityDeviceAuther, ulid string) {
	t.Helper()
	prevA, prevU := identityDeviceAuth, identityDeviceAuthULID
	identityDeviceAuth, identityDeviceAuthULID = auther, ulid
	t.Cleanup(func() { identityDeviceAuth, identityDeviceAuthULID = prevA, prevU })
}

// TestIdentityClaim_ForwardsDeviceCertOnFirstBoot: with an enrolled device identity
// installed and NO session cookie, the claim proxy forwards the box's device-cert
// headers so the CP can authenticate the enrolled device. The forwarded X-Device-Sig
// is a genuine device signature over the purpose-bound "identity:claim:<ulid>"
// message — verifiable against the forwarded pubkey.
func TestIdentityClaim_ForwardsDeviceCertOnFirstBoot(t *testing.T) {
	cp := newFakeCP(t, http.StatusOK, `{"address":"alice@vulos.to","claimed":true}`)
	t.Setenv("VULOS_CLOUD_API_URL", cp.srv.URL)

	const ulid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	auther := newFakeDeviceAuther(t)
	withIdentityDeviceAuth(t, auther, ulid)

	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	// NO Cookie header — this is the pure first-boot shape (no CP session yet).
	r := httptest.NewRequest(http.MethodPost, "/api/identity/claim",
		strings.NewReader(`{"handle":"alice","domain":"vulos.to"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("claim status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	h := cp.lastHeaders
	if got := h.Get("X-Device-ULID"); got != ulid {
		t.Fatalf("X-Device-ULID = %q, want %q", got, ulid)
	}
	// Pubkey + cert must be base64(StdEncoding) of the real values.
	gotPub, err := base64.StdEncoding.DecodeString(h.Get("X-Device-Pubkey"))
	if err != nil || string(gotPub) != string(auther.pub) {
		t.Fatalf("X-Device-Pubkey mismatch: err=%v", err)
	}
	gotCert, err := base64.StdEncoding.DecodeString(h.Get("X-Device-Cert"))
	if err != nil || string(gotCert) != string(auther.cert) {
		t.Fatalf("X-Device-Cert mismatch: err=%v", err)
	}
	// SECURITY: the forwarded signature must verify against the forwarded pubkey over
	// the purpose-bound claim message — proving the box holds the device key and that
	// the signature is claim-scoped (not replayable from the mint path).
	gotSig, err := base64.StdEncoding.DecodeString(h.Get("X-Device-Sig"))
	if err != nil {
		t.Fatalf("X-Device-Sig not base64: %v", err)
	}
	claimMsg := identityClaimSigMessage(ulid)
	if claimMsg != "identity:claim:"+ulid {
		t.Fatalf("claim message = %q, want purpose-bound identity:claim:<ulid>", claimMsg)
	}
	if !ed25519.Verify(auther.pub, []byte(claimMsg), gotSig) {
		t.Fatal("forwarded X-Device-Sig does not verify over identity:claim:<ulid> — box key not proven")
	}
	// A mint-scoped signature must NOT verify as the claim signature (purpose-binding).
	if ed25519.Verify(auther.pub, []byte("integrations:token:google:"+ulid), gotSig) {
		t.Fatal("claim signature also verifies as a mint signature — purpose-binding broken")
	}
	// The body still carries ONLY handle+domain (no account id, even over device path).
	var forwarded map[string]any
	if err := json.Unmarshal([]byte(cp.lastBody), &forwarded); err != nil {
		t.Fatalf("forwarded body not JSON: %v", err)
	}
	if _, ok := forwarded["account_id"]; ok {
		t.Fatalf("device-path body leaked account_id: %v", forwarded)
	}
}

// TestIdentityClaim_NoDeviceHeadersWhenNotEnrolled: with NO enrolled identity
// installed, the claim proxy sends NO device-cert headers — the session-only path is
// unchanged. (This is the pre-enrollment / cookie-only shape.)
func TestIdentityClaim_NoDeviceHeadersWhenNotEnrolled(t *testing.T) {
	cp := newFakeCP(t, http.StatusOK, `{"address":"bob@vulos.to","claimed":true}`)
	t.Setenv("VULOS_CLOUD_API_URL", cp.srv.URL)

	// Explicitly clear the globals for this test (no enrolled identity).
	withIdentityDeviceAuth(t, nil, "")

	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	r := httptest.NewRequest(http.MethodPost, "/api/identity/claim",
		strings.NewReader(`{"handle":"bob","domain":"vulos.to"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Cookie", "cp_session=sess-1")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("claim status = %d, want 200", w.Code)
	}
	h := cp.lastHeaders
	for _, k := range []string{"X-Device-ULID", "X-Device-Pubkey", "X-Device-Cert", "X-Device-Sig"} {
		if v := h.Get(k); v != "" {
			t.Fatalf("unexpected %s = %q — no device headers must be sent when unenrolled", k, v)
		}
	}
	// The session cookie still flows (session-only path intact).
	if cp.lastCookie != "cp_session=sess-1" {
		t.Fatalf("CP cookie = %q, want the session forwarded", cp.lastCookie)
	}
}

// TestIdentityClaim_NoDeviceHeadersWhenCertUnavailable: an identity is installed but
// its cert/key is unusable (DeviceCert ok=false, or SignMint errors) → NO device
// headers are sent (no partial/forged headers), and the proxy still forwards the
// session cookie. setIdentityDeviceAuthHeaders must be all-or-nothing.
func TestIdentityClaim_NoDeviceHeadersWhenCertUnavailable(t *testing.T) {
	cp := newFakeCP(t, http.StatusOK, `{"claimed":true}`)
	t.Setenv("VULOS_CLOUD_API_URL", cp.srv.URL)

	const ulid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

	// (a) No usable cert.
	noCert := newFakeDeviceAuther(t)
	noCert.certOK = false
	withIdentityDeviceAuth(t, noCert, ulid)

	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	post := func() http.Header {
		r := httptest.NewRequest(http.MethodPost, "/api/identity/claim",
			strings.NewReader(`{"handle":"alice","domain":"vulos.to"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cookie", "cp_session=s")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("claim status = %d, want 200", w.Code)
		}
		return cp.lastHeaders
	}
	if v := post().Get("X-Device-Cert"); v != "" {
		t.Fatalf("X-Device-Cert = %q, want empty when DeviceCert ok=false", v)
	}

	// (b) Cert present but signing fails.
	signFail := newFakeDeviceAuther(t)
	signFail.signErr = io.EOF
	withIdentityDeviceAuth(t, signFail, ulid)
	if v := post().Get("X-Device-Sig"); v != "" {
		t.Fatalf("X-Device-Sig = %q, want empty when SignMint errors", v)
	}
	// And no orphan ULID/cert header when signing failed (all-or-nothing).
	if v := cp.lastHeaders.Get("X-Device-ULID"); v != "" {
		t.Fatalf("X-Device-ULID = %q, want empty when signing failed (all-or-nothing)", v)
	}
}

// TestSetIdentityDeviceAuth_NoOpOnEmpty asserts SetIdentityDeviceAuth ignores empty
// inputs (nil auther or empty ulid) so a partial enrollment never half-arms the
// device path.
func TestSetIdentityDeviceAuth_NoOpOnEmpty(t *testing.T) {
	withIdentityDeviceAuth(t, nil, "") // baseline: cleared
	SetIdentityDeviceAuth(nil, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if identityDeviceAuth != nil || identityDeviceAuthULID != "" {
		t.Fatal("SetIdentityDeviceAuth armed the path with a nil auther")
	}
	SetIdentityDeviceAuth(newFakeDeviceAuther(t), "")
	if identityDeviceAuth != nil || identityDeviceAuthULID != "" {
		t.Fatal("SetIdentityDeviceAuth armed the path with an empty ulid")
	}
}
