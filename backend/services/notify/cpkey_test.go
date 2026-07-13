package notify

// cpkey_test.go — the CP public-key relay the browser depends on to create its
// CP-KEYED push subscription (webPush.js enableCPPush → GET
// /api/mail/push/cp-key → this fetch → the CP's cp-key endpoint).
//
// Without it the whole send-on-behalf path was dead: the client's first fetch
// never reached a route, so it always bailed and the box's cp-subscribe handler
// was never called by anything.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fakeCPKeyServer stands in for the CP's UNAUTHENTICATED GET
// /api/mail/push/cp-key, counting hits so the cache can be observed.
func fakeCPKeyServer(t *testing.T, hits *int32, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if r.URL.Path != cpKeyPath {
			t.Errorf("CP got path %q, want %q", r.URL.Path, cpKeyPath)
		}
		if r.Method != http.MethodGet {
			t.Errorf("CP got method %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// TestCPKey_SelfHostReportsDisabled — a nil (self-host) registrar answers
// enabled:false without contacting anything. The client reads that as "no CP"
// and keeps to the cell's own direct send path.
func TestCPKey_SelfHostReportsDisabled(t *testing.T) {
	var r *CPRegistrar // self-host: NewCPRegistrar returns nil
	key, err := r.CPKey(context.Background())
	if err != nil {
		t.Fatalf("CPKey on nil registrar: %v", err)
	}
	if key.Enabled || key.Public != "" {
		t.Fatalf("self-host must report disabled with no key, got %+v", key)
	}
}

// TestCPKey_RelaysPublicKey pins the wire contract the browser codes against:
// {enabled, vapid_public} straight from the CP.
func TestCPKey_RelaysPublicKey(t *testing.T) {
	var hits int32
	srv := fakeCPKeyServer(t, &hits,
		`{"enabled":true,"vapid_public":"BPUBLIC_CP_KEY","subject":"mailto:cp@vulos.org"}`, http.StatusOK)
	defer srv.Close()

	r := liveRegistrar(t, srv)
	key, err := r.CPKey(context.Background())
	if err != nil {
		t.Fatalf("CPKey: %v", err)
	}
	if !key.Enabled || key.Public != "BPUBLIC_CP_KEY" || key.Subject != "mailto:cp@vulos.org" {
		t.Fatalf("relayed key = %+v", key)
	}

	// Cached: a second device subscribing must not re-ask the CP.
	if _, err := r.CPKey(context.Background()); err != nil {
		t.Fatalf("CPKey (cached): %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("CP was hit %d times, want 1 (key must be cached)", got)
	}
}

// TestCPKey_CPErrorIsAnError — a configured-but-broken CP must surface as an
// error, never as enabled:false. enabled:false is the settled "this box has no
// CP" answer; reporting it on a transport failure would make the box lie about
// its own configuration and permanently stop the client from retrying.
func TestCPKey_CPErrorIsAnError(t *testing.T) {
	var hits int32
	srv := fakeCPKeyServer(t, &hits, `{"error":"boom"}`, http.StatusInternalServerError)
	defer srv.Close()

	r := liveRegistrar(t, srv)
	if _, err := r.CPKey(context.Background()); err == nil {
		t.Fatal("expected an error when the CP returns 500")
	}
}

// TestCPKey_EnabledWithoutKeyIsRejected — a CP that claims enabled but sends no
// key is unusable: the browser would call subscribe() with an empty
// applicationServerKey. Fail rather than pass the half-answer on.
func TestCPKey_EnabledWithoutKeyIsRejected(t *testing.T) {
	var hits int32
	srv := fakeCPKeyServer(t, &hits, `{"enabled":true}`, http.StatusOK)
	defer srv.Close()

	r := liveRegistrar(t, srv)
	if _, err := r.CPKey(context.Background()); err == nil {
		t.Fatal("expected an error when the CP reports enabled with no public key")
	}
}

// TestCPKey_CPDisabledIsNotAnError — a CP with no VAPID key of its own reports
// enabled:false; that is a valid, settled answer and must relay as-is.
func TestCPKey_CPDisabledIsNotAnError(t *testing.T) {
	var hits int32
	srv := fakeCPKeyServer(t, &hits, `{"enabled":false}`, http.StatusOK)
	defer srv.Close()

	r := liveRegistrar(t, srv)
	key, err := r.CPKey(context.Background())
	if err != nil {
		t.Fatalf("CPKey: %v", err)
	}
	if key.Enabled {
		t.Fatalf("expected enabled=false relayed from the CP, got %+v", key)
	}
}
