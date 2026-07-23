package cproutes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cloudhome"
)

// stagerSpy records SaveReceivedToDrive calls.
type stagerSpy struct {
	got []cloudhome.ReceivedDoc
	acc []string
}

func (s *stagerSpy) SaveReceivedToDrive(_ context.Context, accountID string, doc cloudhome.ReceivedDoc) error {
	s.acc = append(s.acc, accountID)
	s.got = append(s.got, doc)
	return nil
}

// fakePuller stands in for a sharer's /api/files/peer/serve: it records the
// forwarded PeerFetchRequest and returns fixed bytes. Proof verification is
// covered byte-for-byte by the cloudhome package interop test.
type fakePuller struct {
	doc []byte
	got cloudhome.PeerFetchRequest
}

func (f *fakePuller) Serve(_ context.Context, _ string, req cloudhome.PeerFetchRequest) (io.ReadCloser, error) {
	f.got = req
	return io.NopCloser(bytes.NewReader(f.doc)), nil
}

// buildCapDelivery builds a {recipient, link, capability} body matching the
// vulos files.CapabilityDelivery wire shape.
func buildCapDelivery(recipient, capID string) (capRaw []byte, body []byte) {
	cap := map[string]any{
		"id":            capID,
		"name":          "report.pdf",
		"is_dir":        false,
		"content_type":  "application/pdf",
		"owner_vula_id": "vula:ed25519:ALICE",
		"owner_addr":    "https://alice.example",
		"recipient":     recipient,
	}
	capRaw, _ = json.Marshal(cap)
	link := base64.RawURLEncoding.EncodeToString(capRaw)
	body, _ = json.Marshal(map[string]any{
		"recipient":  recipient,
		"link":       link,
		"capability": json.RawMessage(capRaw),
	})
	return capRaw, body
}

func newCloudHomeTestServer(t *testing.T, stager cloudhome.DriveStager, puller cloudhome.PeerServeClient) (*http.ServeMux, *cloudhome.Service) {
	t.Helper()
	authStore, err := openAuthStoreForTest(":memory:", []byte("test-secret-test-secret-123456789"))
	if err != nil {
		t.Fatalf("openAuthStoreForTest: %v", err)
	}
	t.Cleanup(func() { _ = authStore.Close() })

	if _, _, err := authStore.Signup(context.Background(), "bob@vulos.org", "correct-horse-battery", "1.2.3.4", "ua"); err != nil {
		t.Fatalf("signup: %v", err)
	}

	svc, err := cloudhome.NewService(cloudhome.Config{
		Store:   cloudhome.NewMemStore(),
		KEK:     make([]byte, 32),
		Server:  "https://cell.vulos.test",
		Account: authAccountLookup{st: authStore},
		Box:     nil,
		Stager:  stager,
		Puller:  puller,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	mux := http.NewServeMux()
	RegisterCloudHome(mux, svc, authStore)
	return mux, svc
}

func TestVerifyLookupResolvesAccountOnly(t *testing.T) {
	mux, _ := newCloudHomeTestServer(t, &stagerSpy{}, &fakePuller{})

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/verify/lookup?email=bob@vulos.org", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var res cloudhome.DiscoveryResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(res.VulaID, cloudhome.VulaIDPrefix) {
		t.Fatalf("bad VulaID: %q", res.VulaID)
	}
	if res.Server != "https://cell.vulos.test" {
		t.Fatalf("server: %q", res.Server)
	}
	if res.DisplayName != "bob@vulos.org" {
		t.Fatalf("display: %q", res.DisplayName)
	}
}

// TestDirectoryAdvertisesNoContentPreKey pins the CONTENT-BLIND directory shape:
// a resolvable lookup returns ONLY vula_id/server/display_name and NO content
// prekey bundle or prekey-claim path, and there is no prekey-claim route to POST.
func TestDirectoryAdvertisesNoContentPreKey(t *testing.T) {
	mux, _ := newCloudHomeTestServer(t, &stagerSpy{}, &fakePuller{})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/verify/lookup?email=bob@vulos.org", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	// The directory JSON must not carry any prekey/claim keys.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, forbidden := range []string{"prekey_claim_path", "signed_prekey", "one_time_prekey"} {
		if _, ok := raw[forbidden]; ok {
			t.Fatalf("directory leaked content-prekey key %q: %s", forbidden, rr.Body.String())
		}
	}
	// The prekey-claim route must not exist (no handler registered) → 404.
	rr2 := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"identity_vula_id": "vula:ed25519:anything"})
	mux.ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/api/peering/prekeys/claim", bytes.NewReader(body)))
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("prekey-claim route must be gone (404), got %d", rr2.Code)
	}
}

func TestVerifyLookupUnknownEmail404(t *testing.T) {
	mux, _ := newCloudHomeTestServer(t, &stagerSpy{}, &fakePuller{})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/verify/lookup?email=nobody@vulos.org", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCapabilityIntakeRoundTrip(t *testing.T) {
	spy := &stagerSpy{}
	// Wave 3: the sharer serves a content-blind VSEAL1 envelope, not plaintext.
	sealed := sealedTestEnvelope("recipient-key-b64", "opaque-ciphertext")
	puller := &fakePuller{doc: sealed}
	mux, svc := newCloudHomeTestServer(t, spy, puller)

	// Resolving Bob through the directory provisions his cloud-home VulaID, the
	// same identity a remote sharer would have bound the capability to.
	res, err := svc.Resolve(context.Background(), "bob@vulos.org")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	capRaw, body := buildCapDelivery(res.VulaID, "cap-42")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/files/peer/inbound", strings.NewReader(string(body))))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	// The cell PULLed via the sharer's serve endpoint, forwarding the opaque cap
	// and binding the request to Bob's cloud-home VulaID.
	if puller.got.RequesterID != res.VulaID {
		t.Fatalf("pull requester %q, want %q", puller.got.RequesterID, res.VulaID)
	}
	if !bytes.Equal(puller.got.Capability, capRaw) {
		t.Fatalf("forwarded capability mutated:\n got %s\nwant %s", puller.got.Capability, capRaw)
	}
	if puller.got.Proof == "" || puller.got.Timestamp == 0 {
		t.Fatalf("missing fetch proof/timestamp: %+v", puller.got)
	}
	// The pulled bytes were staged into Bob's Drive with capability metadata.
	if len(spy.got) != 1 || spy.got[0].DocID != "cap-42" || spy.acc[0] == "" {
		t.Fatalf("stager not called correctly: acc=%v doc=%+v", spy.acc, spy.got)
	}
	if !bytes.Equal(spy.got[0].Bytes, sealed) || !spy.got[0].Sealed || spy.got[0].Name != "report.pdf" {
		t.Fatalf("staged doc mismatch (must be the verbatim sealed envelope): %+v", spy.got[0])
	}
	if !bytes.HasPrefix(spy.got[0].Bytes, []byte("VSEAL1\n")) {
		t.Fatal("cell staged non-sealed bytes")
	}
}

// sealedTestEnvelope builds a minimal valid VSEAL1 envelope for route tests. The
// cell only parses it (no content key), so an arbitrary opaque body is fine.
func sealedTestEnvelope(kidB64, body string) []byte {
	hdr := map[string]any{
		"v": 1, "aead": "AES-256-GCM", "kdf": "HKDF-SHA256", "kem": "X25519",
		"cnonce": base64.StdEncoding.EncodeToString(make([]byte, 12)),
		"wraps": []map[string]string{{
			"kid":    kidB64,
			"epk":    base64.StdEncoding.EncodeToString(make([]byte, 32)),
			"wnonce": base64.StdEncoding.EncodeToString(make([]byte, 12)),
			"wct":    base64.StdEncoding.EncodeToString(make([]byte, 48)),
		}},
	}
	hj, _ := json.Marshal(hdr)
	out := []byte("VSEAL1\n")
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(hj)))
	out = append(out, l[:]...)
	out = append(out, hj...)
	out = append(out, []byte(body)...)
	return out
}

func TestCapabilityIntakeUnknownRecipient404(t *testing.T) {
	mux, _ := newCloudHomeTestServer(t, &stagerSpy{}, &fakePuller{})
	_, body := buildCapDelivery("vula:ed25519:NOTOURS", "cap-1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/files/peer/inbound", strings.NewReader(string(body))))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404 (fail closed), got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCloudHomeRoutesDegrade503(t *testing.T) {
	mux := http.NewServeMux()
	RegisterCloudHome(mux, nil, nil) // nil service => fail closed
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/verify/lookup?email=bob@vulos.org", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DIR-ENUM-01 — directory-enumeration hardening
// ─────────────────────────────────────────────────────────────────────────────

func TestVerifyLookupUniformNegativeBody(t *testing.T) {
	mux, _ := newCloudHomeTestServer(t, &stagerSpy{}, &fakePuller{})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/verify/lookup?email=nobody@vulos.org", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Negative body must be the uniform shape — no "no account", no email echo —
	// so absent vs private accounts are indistinguishable.
	if body["error"] != "not discoverable" {
		t.Fatalf("non-uniform negative body: %+v", body)
	}
	if s := rr.Body.String(); strings.Contains(s, "no Vulos account") || strings.Contains(s, "nobody") {
		t.Fatalf("negative body leaked existence info: %s", s)
	}
}

func TestVerifyLookupAnonRateLimited(t *testing.T) {
	mux, _ := newCloudHomeTestServer(t, &stagerSpy{}, &fakePuller{})
	send := func() int {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/verify/lookup?email=nobody@vulos.org", nil)
		r.RemoteAddr = "203.0.113.9:4444"
		mux.ServeHTTP(rr, r)
		return rr.Code
	}
	for i := 0; i < dirAnonPerMin; i++ {
		if code := send(); code == http.StatusTooManyRequests {
			t.Fatalf("anon request %d throttled too early", i)
		}
	}
	if code := send(); code != http.StatusTooManyRequests {
		t.Fatalf("over-limit anon lookup: want 429, got %d", code)
	}
}

func TestVerifyLookupRelayCallerNotIPThrottled(t *testing.T) {
	t.Setenv("CP_SHARED_SECRET", "relay-secret-xyz")
	mux, _ := newCloudHomeTestServer(t, &stagerSpy{}, &fakePuller{})
	// A relay/service caller presenting the shared secret gets the generous
	// per-caller budget, NOT the hard anonymous per-IP one — so it sails past
	// dirAnonPerMin requests.
	for i := 0; i < dirAnonPerMin+5; i++ {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/verify/lookup?email=bob@vulos.org", nil)
		r.RemoteAddr = "203.0.113.10:4444"
		r.Header.Set("X-Relay-Auth", "relay-secret-xyz")
		mux.ServeHTTP(rr, r)
		if rr.Code == http.StatusTooManyRequests {
			t.Fatalf("trusted relay caller throttled at request %d (anon limit leaked)", i)
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("relay lookup %d: want 200, got %d: %s", i, rr.Code, rr.Body.String())
		}
	}
}
