package rendezvous

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/fabric"
)

// --- harness ---------------------------------------------------------------

type fixture struct {
	svc  *Service
	http *httptest.Server
	now  *time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	now := time.Now()
	svc := New(Config{
		Logf:  func(f string, a ...any) { t.Logf("[rdv] "+f, a...) },
		nowFn: func() time.Time { return now },
	})
	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)
	return &fixture{svc: svc, http: ts, now: &now}
}

func (f *fixture) baseURL() string { return f.http.URL + DefaultPathPrefix }

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// mustGet fails the test on a transport error rather than letting a nil
// response panic three lines later with an unrelated message.
func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// signedAnnounce builds a well-formed announcement, so tests can perturb one
// field at a time and see exactly that field rejected.
func signedAnnounce(t *testing.T, priv ed25519.PrivateKey, ts int64, endpoints []string) announceRequest {
	t.Helper()
	key := b64u(priv.Public().(ed25519.PublicKey))
	const ttl = int64(300)
	nonce := b64u([]byte("0123456789abcdef"))
	const meta = "caps=vulos-fabric"

	fields := []string{key, strconv.FormatInt(ts, 10), strconv.FormatInt(ttl, 10), nonce, meta}
	fields = append(fields, endpoints...)
	sig := ed25519.Sign(priv, CanonicalMessage(DomainAnnounce, fields...))

	return announceRequest{
		Key: key, Endpoints: endpoints, Meta: meta,
		TTL: ttl, Nonce: nonce, TS: ts, Sig: b64u(sig),
	}
}

func (f *fixture) post(t *testing.T, req announceRequest) int {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(f.baseURL()+"/announce", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// --- the compatibility test that matters -----------------------------------

// TestRealFabricClientFindsItsPeer drives the ACTUAL box-side discoverer
// (backend/internal/fabric) against this server. It is the test that makes the
// wire-compatibility claim real: if the signing preimage, field order, JSON
// shape, or paths drift by one byte, this fails.
func TestRealFabricClientFindsItsPeer(t *testing.T) {
	f := newFixture(t)

	keyA, keyB := newKey(t), newKey(t)
	pubA := b64u(keyA.Public().(ed25519.PublicKey))
	pubB := b64u(keyB.Public().(ed25519.PublicKey))

	mk := func(k ed25519.PrivateKey, self string, peers []string) *fabric.RendezvousDiscoverer {
		return &fabric.RendezvousDiscoverer{
			BaseURL:       f.baseURL(),
			Key:           k,
			PeerKeys:      peers,
			SelfEndpoints: []string{self},
			HTTPClient:    f.http.Client(),
		}
	}
	boxA := mk(keyA, "https://box-a.example.net", []string{pubB})
	boxB := mk(keyB, "https://box-b.example.net", []string{pubA})

	ctx := context.Background()

	// Neither has announced yet, so neither finds the other.
	if peers, _ := boxA.Peers(ctx); len(peers) != 0 {
		t.Fatalf("found %d peers before anyone announced", len(peers))
	}

	// A's Peers() announces A and resolves B; B is still absent.
	if peers, _ := boxA.Peers(ctx); len(peers) != 0 {
		t.Fatalf("A found %d peers while B was still offline", len(peers))
	}
	// Now B announces (via its own Peers call) and finds A.
	peers, err := boxB.Peers(ctx)
	if err != nil {
		t.Fatalf("B.Peers: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("B found %d peers, want 1 (A announced already)", len(peers))
	}
	if peers[0].BaseURL != "https://box-a.example.net" {
		t.Errorf("B resolved A to %q", peers[0].BaseURL)
	}

	// And A now finds B — the two-way case, which is what fabric sync needs.
	// A already announced, so force a fresh resolve.
	peers, err = boxA.Peers(ctx)
	if err != nil {
		t.Fatalf("A.Peers: %v", err)
	}
	if len(peers) != 1 || peers[0].BaseURL != "https://box-b.example.net" {
		t.Fatalf("A resolved %+v, want box-b", peers)
	}

	if live := f.svc.Stats().Live; live != 2 {
		t.Errorf("service holds %d live announcements, want 2", live)
	}
}

// TestCanonicalMessagePinned locks the signing preimage to a fixed vector.
// This is a WIRE CONTRACT shared with the box client and with Ephor: changing
// it silently would make every existing deployment fail to verify, with a
// symptom ("signature does not verify") that gives no hint of the cause.
func TestCanonicalMessagePinned(t *testing.T) {
	got := CanonicalMessage("d", "ab", "c")
	want := []byte{
		0, 0, 0, 1, 'd',
		0, 0, 0, 2, 'a', 'b',
		0, 0, 0, 1, 'c',
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("CanonicalMessage = % x, want % x", got, want)
	}

	// Length prefixing must make field boundaries unambiguous: ("ab","c") and
	// ("a","bc") carry the same bytes but must NOT produce the same preimage.
	if bytes.Equal(CanonicalMessage("d", "ab", "c"), CanonicalMessage("d", "a", "bc")) {
		t.Fatal("field boundaries collide — a signature over one field set would validate for another")
	}
	// The domain tag must separate too.
	if bytes.Equal(CanonicalMessage("d1", "x"), CanonicalMessage("d2", "x")) {
		t.Fatal("domain separation is not effective")
	}
}

// TestDomainTagMatchesClient pins the tag the box client signs with.
func TestDomainTagMatchesClient(t *testing.T) {
	// Value from backend/internal/fabric/rendezvous.go's domainRdvAnnounce.
	if DomainAnnounce != "vulos-rdv/announce/1" {
		t.Fatalf("DomainAnnounce = %q — this is a wire contract with the box client and Ephor", DomainAnnounce)
	}
}

// --- rejection rules -------------------------------------------------------

func TestAnnounceRejections(t *testing.T) {
	f := newFixture(t)
	priv := newKey(t)
	now := f.now.Unix()
	good := signedAnnounce(t, priv, now, []string{"https://box.example.net"})

	if code := f.post(t, good); code != http.StatusOK {
		t.Fatalf("a well-formed announcement was rejected: HTTP %d", code)
	}

	cases := []struct {
		name   string
		break_ func(*announceRequest)
	}{
		{"tampered endpoint", func(a *announceRequest) { a.Endpoints = []string{"https://attacker.example"} }},
		{"tampered ttl", func(a *announceRequest) { a.TTL = 9999 }},
		{"tampered meta", func(a *announceRequest) { a.Meta = "caps=something-else" }},
		{"tampered nonce", func(a *announceRequest) { a.Nonce = "different" }},
		{"tampered timestamp", func(a *announceRequest) { a.TS = a.TS - 1 }},
		{"garbage signature", func(a *announceRequest) { a.Sig = b64u(make([]byte, 64)) }},
		{"empty endpoints", func(a *announceRequest) { a.Endpoints = nil }},
		{"key not base64", func(a *announceRequest) { a.Key = "!!!!" }},
		{"key wrong length", func(a *announceRequest) { a.Key = b64u([]byte("short")) }},
		{"endpoint with credentials", func(a *announceRequest) {
			a.Endpoints = []string{"https://u:p@box.example.net"}
		}},
		{"endpoint bad scheme", func(a *announceRequest) { a.Endpoints = []string{"ftp://box.example.net"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := good
			bad.Endpoints = append([]string(nil), good.Endpoints...)
			tc.break_(&bad)
			if code := f.post(t, bad); code == http.StatusOK {
				t.Errorf("accepted a %s", tc.name)
			}
		})
	}
}

// TestSignedByADifferentKeyIsRejected: the most important forgery case — a
// valid signature, but not by the key the announcement is filed under.
func TestSignedByADifferentKeyIsRejected(t *testing.T) {
	f := newFixture(t)
	victim, attacker := newKey(t), newKey(t)

	// Attacker signs correctly, but claims the victim's key.
	a := signedAnnounce(t, attacker, f.now.Unix(), []string{"https://attacker.example"})
	a.Key = b64u(victim.Public().(ed25519.PublicKey))

	if code := f.post(t, a); code == http.StatusOK {
		t.Fatal("an announcement signed by a different key was accepted — anyone could redirect any peer")
	}
}

// TestClockSkewBoundsReplay.
func TestClockSkewBoundsReplay(t *testing.T) {
	f := newFixture(t)
	priv := newKey(t)
	for _, off := range []time.Duration{-MaxClockSkew - time.Minute, MaxClockSkew + time.Minute} {
		a := signedAnnounce(t, priv, f.now.Add(off).Unix(), []string{"https://box.example.net"})
		if code := f.post(t, a); code == http.StatusOK {
			t.Errorf("accepted an announcement %s from the node's clock", off)
		}
	}
}

// TestRollbackReplayIsRejected: within the skew window, a captured older
// announcement must not be able to roll a key back to a stale address.
func TestRollbackReplayIsRejected(t *testing.T) {
	f := newFixture(t)
	priv := newKey(t)
	key := b64u(priv.Public().(ed25519.PublicKey))

	old := signedAnnounce(t, priv, f.now.Add(-2*time.Minute).Unix(), []string{"https://old.example.net"})
	fresh := signedAnnounce(t, priv, f.now.Unix(), []string{"https://new.example.net"})

	if code := f.post(t, old); code != http.StatusOK {
		t.Fatalf("setup: old announcement rejected (%d)", code)
	}
	if code := f.post(t, fresh); code != http.StatusOK {
		t.Fatalf("setup: fresh announcement rejected (%d)", code)
	}
	// Replay the old one — it is correctly signed and inside the skew window.
	if code := f.post(t, old); code == http.StatusOK {
		t.Fatal("a replayed older announcement was accepted")
	}

	resp, err := http.Get(f.baseURL() + "/resolve/" + key)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out resolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Endpoints) == 0 || out.Endpoints[0] != "https://new.example.net" {
		t.Errorf("endpoints rolled back to %v", out.Endpoints)
	}
}

// TestExpiryStopsResolving.
func TestExpiryStopsResolving(t *testing.T) {
	f := newFixture(t)
	priv := newKey(t)
	key := b64u(priv.Public().(ed25519.PublicKey))
	if code := f.post(t, signedAnnounce(t, priv, f.now.Unix(), []string{"https://box.example.net"})); code != http.StatusOK {
		t.Fatal("setup")
	}

	resp := mustGet(t, f.baseURL()+"/resolve/"+key)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("live key resolved %d", resp.StatusCode)
	}
	resp.Body.Close()

	*f.now = f.now.Add(MaxTTL + time.Minute)

	resp = mustGet(t, f.baseURL()+"/resolve/"+key)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expired key resolved %d, want 404", resp.StatusCode)
	}
	if live := f.svc.Stats().Live; live != 0 {
		t.Errorf("Stats reports %d live after expiry", live)
	}
}

// TestTTLIsCapped: an announcement cannot pin memory for longer than MaxTTL,
// whatever it asks for.
func TestTTLIsCapped(t *testing.T) {
	f := newFixture(t)
	priv := newKey(t)
	key := b64u(priv.Public().(ed25519.PublicKey))

	// Sign with an enormous TTL (the signature covers it, so this is a
	// legitimate request, not a forgery — the cap is a policy limit).
	const huge = int64(365 * 24 * 3600)
	nonce := b64u([]byte("0123456789abcdef"))
	const meta = ""
	eps := []string{"https://box.example.net"}
	fields := append([]string{key, strconv.FormatInt(f.now.Unix(), 10), strconv.FormatInt(huge, 10), nonce, meta}, eps...)
	sig := ed25519.Sign(priv, CanonicalMessage(DomainAnnounce, fields...))
	a := announceRequest{Key: key, Endpoints: eps, Meta: meta, TTL: huge, Nonce: nonce, TS: f.now.Unix(), Sig: b64u(sig)}

	if code := f.post(t, a); code != http.StatusOK {
		t.Fatalf("rejected (%d)", code)
	}
	*f.now = f.now.Add(MaxTTL + time.Minute)
	resp := mustGet(t, f.baseURL()+"/resolve/"+key)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a year-long TTL survived the %s cap", MaxTTL)
	}
}

// TestUnknownKeyIsNotFoundNotError: an offline peer is the normal case.
func TestUnknownKeyIsNotFoundNotError(t *testing.T) {
	f := newFixture(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	resp, err := http.Get(f.baseURL() + "/resolve/" + b64u(pub))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	var out resolveResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Online {
		t.Error("an unknown key reported online")
	}
}

// TestRejectionsAreIndistinguishable: every bad announcement gets the same
// answer, so probing cannot reveal which part of a forgery to fix.
func TestRejectionsAreIndistinguishable(t *testing.T) {
	f := newFixture(t)
	priv := newKey(t)
	good := signedAnnounce(t, priv, f.now.Unix(), []string{"https://box.example.net"})

	variants := []announceRequest{}
	badSig := good
	badSig.Sig = b64u(make([]byte, 64))
	variants = append(variants, badSig)
	badTS := signedAnnounce(t, priv, f.now.Add(-time.Hour).Unix(), []string{"https://box.example.net"})
	variants = append(variants, badTS)
	badKey := good
	badKey.Key = b64u(make([]byte, 32))
	variants = append(variants, badKey)

	var codes []int
	for _, v := range variants {
		codes = append(codes, f.post(t, v))
	}
	for i, c := range codes {
		if c != codes[0] {
			t.Errorf("variant %d answered %d, variant 0 answered %d — rejection reasons are distinguishable", i, c, codes[0])
		}
	}
}

// TestOversizedBodyRejected.
func TestOversizedBodyRejected(t *testing.T) {
	f := newFixture(t)
	huge := strings.Repeat("a", MaxBodyBytes+1024)
	resp, err := http.Post(f.baseURL()+"/announce", "application/json",
		strings.NewReader(`{"key":"`+huge+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("an oversized announce body was accepted")
	}
}

// TestEndpointsAreNeverDialled documents (and enforces) that this role makes
// no outbound request: an endpoint pointing at a private address is stored
// verbatim, because two boxes in one house legitimately announce LAN
// addresses to each other, and the node dialling anything would be an SSRF
// surface it has no reason to have.
func TestEndpointsAreNeverDialled(t *testing.T) {
	f := newFixture(t)
	priv := newKey(t)
	key := b64u(priv.Public().(ed25519.PublicKey))
	lan := []string{"http://192.168.1.5:8443", "http://127.0.0.1:9999"}

	if code := f.post(t, signedAnnounce(t, priv, f.now.Unix(), lan)); code != http.StatusOK {
		t.Fatalf("LAN endpoints rejected (%d) — two boxes in one house could not find each other", code)
	}
	resp := mustGet(t, f.baseURL()+"/resolve/"+key)
	defer resp.Body.Close()
	var out resolveResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Endpoints) != 2 || out.Endpoints[0] != lan[0] {
		t.Errorf("endpoints = %v, want them echoed verbatim", out.Endpoints)
	}
}
