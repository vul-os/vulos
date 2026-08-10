package crdtsync

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testIdentity makes a fresh peer identity.
func testIdentity(t *testing.T) *PeerIdentity {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewPeerIdentity(priv)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// signedRequest builds an http.Request as the syncer would: body buffered,
// envelope signed over it.
func signedRequest(t *testing.T, from *PeerIdentity, audience, method, path string, body []byte) (*http.Request, string) {
	t.Helper()
	header, nonce, err := from.SignRequest(method, path, body, audience)
	if err != nil {
		t.Fatalf("SignRequest: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set(PeerAuthHeader, header)
	return req, nonce
}

func TestPeerAuthRoundTrip(t *testing.T) {
	a, b := testIdentity(t), testIdentity(t)
	roster, err := NewStaticPeerRoster(a.ID())
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewPeerVerifier(b.PublicKey(), roster)
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"domain":"test"}`)
	req, _ := signedRequest(t, a, b.ID(), http.MethodPost, "/api/crdt/pull", body)
	peer, err := v.VerifyRequest(req)
	if err != nil {
		t.Fatalf("a rostered peer's signed request was refused: %v", err)
	}
	if peer != a.ID() {
		t.Errorf("attributed to %q, want %q", peer, a.ID())
	}
	// The body must still be readable by the handler downstream — the verifier
	// consumes it to hash it and is required to put it back.
	got := make([]byte, len(body))
	if _, err := req.Body.Read(got); err != nil || !bytes.Equal(got, body) {
		t.Errorf("body not restored for the handler: %q err=%v", got, err)
	}
}

// TestPeerAuthRefusesEveryWayItCanBeWrong is the fail-closed matrix. Each case
// is a single deviation from a request that WOULD be accepted, so a case that
// stops failing means that specific check stopped running.
func TestPeerAuthRefusesEveryWayItCanBeWrong(t *testing.T) {
	a, b, stranger := testIdentity(t), testIdentity(t), testIdentity(t)
	roster, err := NewStaticPeerRoster(a.ID())
	if err != nil {
		t.Fatal(err)
	}
	newVerifier := func() *PeerVerifier {
		v, err := NewPeerVerifier(b.PublicKey(), roster)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	body := []byte(`{"domain":"test"}`)

	cases := []struct {
		name string
		// build returns a request that must be REFUSED.
		build func(t *testing.T) *http.Request
	}{
		{"no envelope at all", func(t *testing.T) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/api/crdt/pull", bytes.NewReader(body))
		}},
		{"envelope is not base64", func(t *testing.T) *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/crdt/pull", bytes.NewReader(body))
			r.Header.Set(PeerAuthHeader, "!!!not base64!!!")
			return r
		}},
		{"signer is not in the roster", func(t *testing.T) *http.Request {
			r, _ := signedRequest(t, stranger, b.ID(), http.MethodPost, "/api/crdt/pull", body)
			return r
		}},
		{"addressed to a different box", func(t *testing.T) *http.Request {
			// Signed correctly by a rostered peer — but for someone else. This
			// is the property the shared secret cannot have: a request captured
			// at one box is useless at another.
			r, _ := signedRequest(t, a, stranger.ID(), http.MethodPost, "/api/crdt/pull", body)
			return r
		}},
		{"body swapped after signing", func(t *testing.T) *http.Request {
			r, _ := signedRequest(t, a, b.ID(), http.MethodPost, "/api/crdt/pull", body)
			r.Body = http.NoBody
			r.Body = httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte(`{"domain":"evil"}`))).Body
			return r
		}},
		{"replayed on a different path", func(t *testing.T) *http.Request {
			r, _ := signedRequest(t, a, b.ID(), http.MethodPost, "/api/crdt/pull", body)
			r.URL.Path = "/api/crdt/push"
			return r
		}},
		{"replayed with a different method", func(t *testing.T) *http.Request {
			r, _ := signedRequest(t, a, b.ID(), http.MethodPost, "/api/crdt/pull", body)
			r.Method = http.MethodGet
			return r
		}},
		{"signature is garbage", func(t *testing.T) *http.Request {
			r, _ := signedRequest(t, a, b.ID(), http.MethodPost, "/api/crdt/pull", body)
			r.Header.Set(PeerAuthHeader, mutateEnvelope(t, r.Header.Get(PeerAuthHeader), func(e *peerAuthEnvelope) {
				e.Sig = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
			}))
			return r
		}},
		{"a field was edited after signing", func(t *testing.T) *http.Request {
			// The nonce is inside the signed bytes; changing it must invalidate
			// the signature rather than simply dodging the replay cache.
			r, _ := signedRequest(t, a, b.ID(), http.MethodPost, "/api/crdt/pull", body)
			r.Header.Set(PeerAuthHeader, mutateEnvelope(t, r.Header.Get(PeerAuthHeader), func(e *peerAuthEnvelope) {
				e.Nonce = "a-different-nonce"
			}))
			return r
		}},
		{"peer_key swapped for a rostered one", func(t *testing.T) *http.Request {
			// A stranger claiming to be a rostered peer. The roster check would
			// pass on the CLAIM; only the signature check catches this.
			r, _ := signedRequest(t, stranger, b.ID(), http.MethodPost, "/api/crdt/pull", body)
			r.Header.Set(PeerAuthHeader, mutateEnvelope(t, r.Header.Get(PeerAuthHeader), func(e *peerAuthEnvelope) {
				e.PeerKey = a.ID()
			}))
			return r
		}},
		{"wrong envelope type", func(t *testing.T) *http.Request {
			r, _ := signedRequest(t, a, b.ID(), http.MethodPost, "/api/crdt/pull", body)
			r.Header.Set(PeerAuthHeader, mutateEnvelope(t, r.Header.Get(PeerAuthHeader), func(e *peerAuthEnvelope) {
				e.Type = peerAuthResponseType
			}))
			return r
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newVerifier()
			if peer, err := v.VerifyRequest(tc.build(t)); err == nil {
				t.Fatalf("ACCEPTED %q (attributed to %s) — this must fail closed", tc.name, peer)
			}
		})
	}
}

// mutateEnvelope decodes a request envelope, applies fn, and re-encodes it
// WITHOUT re-signing — which is the point: it simulates an attacker who can
// edit the header but does not hold the key.
func mutateEnvelope(t *testing.T, header string, fn func(*peerAuthEnvelope)) string {
	t.Helper()
	var env peerAuthEnvelope
	if err := decodeEnvelope(header, &env); err != nil {
		t.Fatal(err)
	}
	fn(&env)
	out, err := encodeEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPeerAuthFreshnessWindow(t *testing.T) {
	a, b := testIdentity(t), testIdentity(t)
	roster, _ := NewStaticPeerRoster(a.ID())

	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return base }
	body := []byte(`{}`)

	for _, tc := range []struct {
		name    string
		verifAt time.Time
		wantOK  bool
	}{
		{"same instant", base, true},
		{"just inside the window", base.Add(PeerAuthMaxAge - time.Second), true},
		{"just outside the window", base.Add(PeerAuthMaxAge + time.Second), false},
		{"issued a little in the future is tolerated", base.Add(-PeerAuthClockSkew + time.Second), true},
		{"issued far in the future is not", base.Add(-PeerAuthClockSkew - 2*time.Second), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := NewPeerVerifier(b.PublicKey(), roster)
			if err != nil {
				t.Fatal(err)
			}
			v.now = func() time.Time { return tc.verifAt }
			req, _ := signedRequest(t, a, b.ID(), http.MethodPost, "/api/crdt/pull", body)
			_, err = v.VerifyRequest(req)
			if tc.wantOK && err != nil {
				t.Fatalf("refused a fresh request: %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("accepted a request outside the freshness window")
			}
		})
	}
}

func TestPeerAuthRejectsReplay(t *testing.T) {
	// A captured request, re-sent verbatim inside the freshness window, is the
	// attack the nonce cache exists for: every other check still passes on a
	// byte-identical copy.
	a, b := testIdentity(t), testIdentity(t)
	roster, _ := NewStaticPeerRoster(a.ID())
	v, err := NewPeerVerifier(b.PublicKey(), roster)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"domain":"test"}`)
	header, _, err := a.SignRequest(http.MethodPost, "/api/crdt/push", body, b.ID())
	if err != nil {
		t.Fatal(err)
	}
	build := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/crdt/push", bytes.NewReader(body))
		r.Header.Set(PeerAuthHeader, header)
		return r
	}
	if _, err := v.VerifyRequest(build()); err != nil {
		t.Fatalf("first delivery refused: %v", err)
	}
	if _, err := v.VerifyRequest(build()); err == nil {
		t.Fatal("a byte-identical replay was ACCEPTED")
	}
	// A fresh request from the same peer still works — the cache rejects the
	// repeat, not the peer.
	req2, _ := signedRequest(t, a, b.ID(), http.MethodPost, "/api/crdt/push", body)
	if _, err := v.VerifyRequest(req2); err != nil {
		t.Fatalf("a fresh request from the same peer was refused: %v", err)
	}
}

func TestPeerAuthReplayCacheFailsClosedWhenFull(t *testing.T) {
	// Forgetting a nonce to make room is exactly the replay the cache exists to
	// stop, so a full cache must refuse rather than evict live entries.
	a, b := testIdentity(t), testIdentity(t)
	roster, _ := NewStaticPeerRoster(a.ID())
	v, _ := NewPeerVerifier(b.PublicKey(), roster)
	now := time.Now().UTC()
	for i := 0; i < maxNonceCache; i++ {
		v.seen[a.ID()+"\x00fill"+string(rune(i))] = now
	}
	if err := v.rememberNonce(a.ID(), "fresh", now); err == nil {
		t.Fatal("a full replay cache accepted a new nonce instead of failing closed")
	}
	// Once the entries age out, it recovers rather than staying wedged.
	if err := v.rememberNonce(a.ID(), "fresh", now.Add(2*(PeerAuthMaxAge+PeerAuthClockSkew))); err != nil {
		t.Fatalf("cache did not recover after entries expired: %v", err)
	}
}

func TestPeerKeyEncodingIsOneIdentity(t *testing.T) {
	// The roster column is base64-standard; the rendezvous relay address is
	// base64url-raw. If those became two identities, a peer would be rostered
	// under one string and refused under the other.
	id := testIdentity(t)
	raw := []byte(id.PublicKey())
	forms := []string{
		base64.RawURLEncoding.EncodeToString(raw),
		base64.StdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
	}
	for _, f := range forms {
		got, err := DecodePeerKey(f)
		if err != nil {
			t.Fatalf("DecodePeerKey(%q): %v", f, err)
		}
		if !bytes.Equal(got, raw) {
			t.Errorf("%q decoded to different bytes", f)
		}
		if !samePeerKey(f, id.ID()) {
			t.Errorf("%q is not recognised as the same identity as %q", f, id.ID())
		}
	}
	// A roster built from the standard encoding still trusts the url-encoded
	// form, which is the exact mismatch that would silently deny every peer.
	roster, err := NewStaticPeerRoster(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !roster.TrustedPeer(id.PublicKey()) {
		t.Error("a roster entry in base64-standard did not match the same key")
	}
	if _, err := DecodePeerKey("short"); err == nil {
		t.Error("a non-key string decoded as a key")
	}
}

func TestEmptyRosterTrustsNobody(t *testing.T) {
	r, err := NewStaticPeerRoster()
	if err != nil {
		t.Fatal(err)
	}
	if r.TrustedPeer(testIdentity(t).PublicKey()) {
		t.Fatal("an empty roster trusted a peer")
	}
	if _, err := NewPeerVerifier(testIdentity(t).PublicKey(), nil); err == nil {
		t.Fatal("a verifier was built with no roster — a signature is not an authorisation")
	}
}

func TestSignRequestRefusesEmptyAudience(t *testing.T) {
	if _, _, err := testIdentity(t).SignRequest(http.MethodPost, "/api/crdt/pull", nil, ""); err == nil {
		t.Fatal("signed an envelope with no audience — it would be replayable at every box in the fleet")
	}
}

// ── response side ────────────────────────────────────────────────────────────

func TestResponseAuthRefusesEveryWayItCanBeWrong(t *testing.T) {
	// This is the half that a request-only scheme would miss. A pull RESPONSE
	// is merged into the caller's database, and the address it came from was
	// supplied by a relay whose answer carries no signature.
	server, client, imposter := testIdentity(t), testIdentity(t), testIdentity(t)
	body := []byte(`{"domain":"test","ops":[]}`)
	nonce := "the-request-nonce"

	good, err := server.SignResponse(client.ID(), nonce, http.StatusOK, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyResponse(good, server.ID(), client.ID(), nonce, http.StatusOK, body); err != nil {
		t.Fatalf("a correctly signed response was refused: %v", err)
	}

	bad, err := imposter.SignResponse(client.ID(), nonce, http.StatusOK, body)
	if err != nil {
		t.Fatal(err)
	}
	otherNonce, err := server.SignResponse(client.ID(), "some-other-nonce", http.StatusOK, body)
	if err != nil {
		t.Fatal(err)
	}
	otherAudience, err := server.SignResponse(imposter.ID(), nonce, http.StatusOK, body)
	if err != nil {
		t.Fatal(err)
	}
	otherBody, err := server.SignResponse(client.ID(), nonce, http.StatusOK, []byte(`{"domain":"test","ops":[{}]}`))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		header string
	}{
		// The single most important row: an endpoint that simply does not sign.
		// A relay that points this box at an attacker produces exactly this.
		{"no signature at all", ""},
		{"signed by a different key than the one we resolved", bad},
		{"answers a different request's nonce", otherNonce},
		{"addressed to a different box", otherAudience},
		{"signed over a different body", otherBody},
		{"envelope is not base64", "!!!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := VerifyResponse(tc.header, server.ID(), client.ID(), nonce, http.StatusOK, body); err == nil {
				t.Fatalf("ACCEPTED a response that %s", tc.name)
			}
		})
	}

	// And the body itself must not be swappable after signing.
	if err := VerifyResponse(good, server.ID(), client.ID(), nonce, http.StatusOK, []byte(`{"domain":"test","ops":[{"evil":1}]}`)); err == nil {
		t.Fatal("ACCEPTED a response whose body was swapped after signing")
	}
	// Nor the status.
	if err := VerifyResponse(good, server.ID(), client.ID(), nonce, http.StatusForbidden, body); err == nil {
		t.Fatal("ACCEPTED a response whose status did not match the signed one")
	}
}

// ── the mounted endpoints ────────────────────────────────────────────────────

func TestHandlersAcceptSignedPeerAndSignTheirResponse(t *testing.T) {
	// The whole scheme, through the real handlers on a real mux.
	server := testIdentity(t)
	peer := testIdentity(t)
	stranger := testIdentity(t)
	roster, _ := NewStaticPeerRoster(peer.ID())
	v, err := NewPeerVerifier(server.PublicKey(), roster)
	if err != nil {
		t.Fatal(err)
	}

	st := newTestStore(t, "SERVER")
	mux := http.NewServeMux()
	st.RegisterHandlers(mux,
		AnyOfAuthorizer(SecretAuthorizer(testSecret), PeerKeyAuthorizer(v)),
		WithResponseSigning(server))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(PullRequest{Domain: dom})

	// A rostered peer: 200, and the response carries a signature this client
	// can verify against the key it expected.
	header, nonce, err := peer.SignRequest(http.MethodPost, "/api/crdt/pull", body, server.ID())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/crdt/pull", bytes.NewReader(body))
	req.Header.Set(PeerAuthHeader, header)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	respBody := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a rostered signed peer got %d: %s", resp.StatusCode, respBody)
	}
	if err := VerifyResponse(resp.Header.Get(PeerAuthResponseHeader), server.ID(), peer.ID(), nonce, http.StatusOK, respBody); err != nil {
		t.Fatalf("the endpoint's response did not verify: %v", err)
	}

	// A stranger holding a perfectly valid key: 401. The signature is fine; the
	// roster is what refuses.
	sHeader, _, err := stranger.SignRequest(http.MethodPost, "/api/crdt/pull", body, server.ID())
	if err != nil {
		t.Fatal(err)
	}
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/crdt/pull", bytes.NewReader(body))
	req2.Header.Set(PeerAuthHeader, sHeader)
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp2)
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unrostered peer got %d, want 401", resp2.StatusCode)
	}

	// The LAN shared-secret path is untouched, and gets an UNSIGNED response
	// exactly as before.
	req3, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/crdt/pull", bytes.NewReader(body))
	req3.Header.Set(AuthHeader, testSecret)
	resp3, err := srv.Client().Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp3)
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("the LAN secret path broke: %d", resp3.StatusCode)
	}
	if resp3.Header.Get(PeerAuthResponseHeader) != "" {
		t.Error("a LAN request got a response signature it never asked for")
	}

	// And a request with NEITHER credential is still refused.
	req4, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/crdt/pull", bytes.NewReader(body))
	resp4, err := srv.Client().Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp4)
	if resp4.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated request got %d, want 401", resp4.StatusCode)
	}
}

func TestAnyOfAuthorizerWithNothingAcceptsNothing(t *testing.T) {
	a := AnyOfAuthorizer()
	if a(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("AnyOfAuthorizer() with no authorizers accepted a request")
	}
	if PeerKeyAuthorizer(nil)(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("PeerKeyAuthorizer(nil) accepted a request")
	}
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func TestPeerAuthCanonicalBytesArePinned(t *testing.T) {
	// A wire contract: two boxes on different builds must produce the same
	// signing preimage. Pinning it here means a field rename or reordering that
	// would silently stop peers verifying each other fails HERE instead.
	env := peerAuthEnvelope{
		Type:     peerAuthRequestType,
		PeerKey:  "PK",
		Audience: "AUD",
		Method:   "POST",
		Path:     "/api/crdt/pull",
		BodyHash: "BH",
		Nonce:    "N",
		IssuedAt: "2026-08-10T12:00:00Z",
	}
	got, err := canonicalPeerBytes(env)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"audience":"AUD","body_hash":"BH","issued_at":"2026-08-10T12:00:00Z","method":"POST","nonce":"N","path":"/api/crdt/pull","peer_key":"PK","sig":"","type":"vulos-crdt/peer-auth/1"}`
	if string(got) != want {
		t.Errorf("request preimage changed\n got: %s\nwant: %s", got, want)
	}

	renv := peerRespEnvelope{
		Type:     peerAuthResponseType,
		PeerKey:  "PK",
		Audience: "AUD",
		ReqNonce: "N",
		Status:   200,
		BodyHash: "BH",
	}
	rgot, err := canonicalPeerBytes(renv)
	if err != nil {
		t.Fatal(err)
	}
	rwant := `{"audience":"AUD","body_hash":"BH","peer_key":"PK","req_nonce":"N","sig":"","status":200,"type":"vulos-crdt/peer-auth-response/1"}`
	if string(rgot) != rwant {
		t.Errorf("response preimage changed\n got: %s\nwant: %s", rgot, rwant)
	}
	if !strings.Contains(string(got), `"type":"vulos-crdt/peer-auth/1"`) {
		t.Error("the domain separator is not inside the signed bytes")
	}
}
