package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// echoIPServer returns a test server that echoes a fixed IP string.
func echoIPServer(ip string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(ip)) //nolint:errcheck
	}))
}

// controlServer creates a fake Vulos control API that handles:
//   - POST /api/enroll/direct → returns acme-dns creds
//   - PUT  /api/dns/update    → records the last received IP
func controlServer(t *testing.T, uuid, key string, lastUpdatedIP *atomic.Value) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/enroll/direct":
			var req DirectEnrollRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if req.ULID == "" || req.IP == "" {
				http.Error(w, "missing fields", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(DirectEnrollResponse{ //nolint:errcheck
				AcmeDNSUUID: uuid,
				AcmeDNSKey:  key,
			})

		case r.Method == http.MethodPut && r.URL.Path == "/api/dns/update":
			var req DNSUpdateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if lastUpdatedIP != nil {
				lastUpdatedIP.Store(req.IP)
			}
			w.WriteHeader(http.StatusOK)

		default:
			http.NotFound(w, r)
		}
	}))
}

// ---------------------------------------------------------------------------
// DetectPublicIP
// ---------------------------------------------------------------------------

func TestDetectPublicIP_Success(t *testing.T) {
	want := "1.2.3.4"
	echo := echoIPServer(want)
	defer echo.Close()

	e := NewEnroller(
		WithIPEchoURL(echo.URL),
		WithHTTPClient(echo.Client()),
	)

	got, err := e.DetectPublicIP(context.Background())
	if err != nil {
		t.Fatalf("DetectPublicIP: unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("DetectPublicIP = %q; want %q", got, want)
	}
}

func TestDetectPublicIP_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := NewEnroller(
		WithIPEchoURL(srv.URL),
		WithHTTPClient(srv.Client()),
	)

	_, err := e.DetectPublicIP(context.Background())
	if err == nil {
		t.Fatal("DetectPublicIP: expected error on non-200; got nil")
	}
}

func TestDetectPublicIP_EmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// write nothing
	}))
	defer srv.Close()

	e := NewEnroller(
		WithIPEchoURL(srv.URL),
		WithHTTPClient(srv.Client()),
	)

	_, err := e.DetectPublicIP(context.Background())
	if err == nil {
		t.Fatal("DetectPublicIP: expected error on empty body; got nil")
	}
}

// ---------------------------------------------------------------------------
// EnrollDirect
// ---------------------------------------------------------------------------

func TestEnrollDirect_Success(t *testing.T) {
	const wantUUID = "test-uuid-1234"
	const wantKey = "test-key-abcd"

	ctrl := controlServer(t, wantUUID, wantKey, nil)
	defer ctrl.Close()

	e := NewEnroller(
		WithControlURL(ctrl.URL),
		WithHTTPClient(ctrl.Client()),
	)

	creds, err := e.EnrollDirect(context.Background(), "01testulid00000000000", "1.2.3.4", "user@example.com")
	if err != nil {
		t.Fatalf("EnrollDirect: unexpected error: %v", err)
	}
	if creds.UUID != wantUUID {
		t.Errorf("creds.UUID = %q; want %q", creds.UUID, wantUUID)
	}
	if creds.Key != wantKey {
		t.Errorf("creds.Key = %q; want %q", creds.Key, wantKey)
	}
	// Env vars should be persisted.
	t.Setenv("VULOS_ACME_DNS_UUID", "") // ensure clean read
	t.Setenv("VULOS_ACME_DNS_KEY", "")
	// Re-enroll to verify setenv path (already set by EnrollDirect above).
	_, _ = e.EnrollDirect(context.Background(), "01testulid00000000000", "1.2.3.4", "user@example.com")
	loaded := LoadAcmeDNSCreds()
	if loaded == nil {
		t.Fatal("LoadAcmeDNSCreds: got nil after enrollment")
	}
	if loaded.UUID != wantUUID || loaded.Key != wantKey {
		t.Errorf("LoadAcmeDNSCreds = {%q,%q}; want {%q,%q}", loaded.UUID, loaded.Key, wantUUID, wantKey)
	}
}

func TestEnrollDirect_RecordsLastIP(t *testing.T) {
	ctrl := controlServer(t, "u", "k", nil)
	defer ctrl.Close()

	e := NewEnroller(
		WithControlURL(ctrl.URL),
		WithHTTPClient(ctrl.Client()),
	)

	if _, err := e.EnrollDirect(context.Background(), "testulid", "5.5.5.5", "x@y.com"); err != nil {
		t.Fatalf("EnrollDirect: %v", err)
	}
	if got := e.LastPublicIP(); got != "5.5.5.5" {
		t.Errorf("LastPublicIP = %q; want \"5.5.5.5\"", got)
	}
}

func TestEnrollDirect_ControlAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := NewEnroller(
		WithControlURL(srv.URL),
		WithHTTPClient(srv.Client()),
	)

	_, err := e.EnrollDirect(context.Background(), "ulid", "1.1.1.1", "e@e.com")
	if err == nil {
		t.Fatal("EnrollDirect: expected error on 500; got nil")
	}
}

// ---------------------------------------------------------------------------
// UpdateDNS
// ---------------------------------------------------------------------------

func TestUpdateDNS_Success(t *testing.T) {
	var lastIP atomic.Value
	ctrl := controlServer(t, "u", "k", &lastIP)
	defer ctrl.Close()

	e := NewEnroller(
		WithControlURL(ctrl.URL),
		WithHTTPClient(ctrl.Client()),
	)

	if err := e.UpdateDNS(context.Background(), "myulid", "9.9.9.9"); err != nil {
		t.Fatalf("UpdateDNS: %v", err)
	}
	if got, _ := lastIP.Load().(string); got != "9.9.9.9" {
		t.Errorf("server recorded IP %q; want \"9.9.9.9\"", got)
	}
	if got := e.LastPublicIP(); got != "9.9.9.9" {
		t.Errorf("LastPublicIP = %q; want \"9.9.9.9\"", got)
	}
}

func TestUpdateDNS_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	e := NewEnroller(
		WithControlURL(srv.URL),
		WithHTTPClient(srv.Client()),
	)

	if err := e.UpdateDNS(context.Background(), "ulid", "1.1.1.1"); err == nil {
		t.Fatal("UpdateDNS: expected error on 502; got nil")
	}
}

// ---------------------------------------------------------------------------
// LoadAcmeDNSCreds
// ---------------------------------------------------------------------------

func TestLoadAcmeDNSCreds_Missing(t *testing.T) {
	t.Setenv("VULOS_ACME_DNS_UUID", "")
	t.Setenv("VULOS_ACME_DNS_KEY", "")
	if LoadAcmeDNSCreds() != nil {
		t.Error("LoadAcmeDNSCreds: expected nil when env vars absent")
	}
}

func TestLoadAcmeDNSCreds_Present(t *testing.T) {
	t.Setenv("VULOS_ACME_DNS_UUID", "my-uuid")
	t.Setenv("VULOS_ACME_DNS_KEY", "my-key")
	got := LoadAcmeDNSCreds()
	if got == nil {
		t.Fatal("LoadAcmeDNSCreds: expected non-nil")
	}
	if got.UUID != "my-uuid" || got.Key != "my-key" {
		t.Errorf("unexpected creds: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// WatchIPChanges
// ---------------------------------------------------------------------------

func TestWatchIPChanges_TriggersUpdateOnChange(t *testing.T) {
	var lastIP atomic.Value
	ctrl := controlServer(t, "u", "k", &lastIP)
	defer ctrl.Close()

	// IP starts as "1.1.1.1", changes to "2.2.2.2" on the second call.
	var callCount int64
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&callCount, 1)
		if n <= 1 {
			w.Write([]byte("1.1.1.1")) //nolint:errcheck
		} else {
			w.Write([]byte("2.2.2.2")) //nolint:errcheck
		}
	}))
	defer echo.Close()

	e := NewEnroller(
		WithIPEchoURL(echo.URL),
		WithControlURL(ctrl.URL),
		WithHTTPClient(ctrl.Client()),
		WithPollInterval(20*time.Millisecond),
	)
	// Seed the initial IP so the first poll looks like a change from empty → 1.1.1.1.
	// That first tick won't call UpdateDNS (lastPublicIP == "").
	// The test monitors the second change: 1.1.1.1 → 2.2.2.2.
	e.mu.Lock()
	e.lastPublicIP = "1.1.1.1"
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go e.WatchIPChanges(ctx, "watchulid")

	// Wait for lastIP to be updated to 2.2.2.2.
	deadline := time.Now().Add(450 * time.Millisecond)
	for time.Now().Before(deadline) {
		if ip, _ := lastIP.Load().(string); ip == "2.2.2.2" {
			return // pass
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("WatchIPChanges: dns update to 2.2.2.2 not observed within timeout")
}

func TestWatchIPChanges_NoUpdateWhenIPStable(t *testing.T) {
	var updateCount int64
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/api/dns/update" {
			atomic.AddInt64(&updateCount, 1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ctrl.Close()

	echo := echoIPServer("3.3.3.3")
	defer echo.Close()

	e := NewEnroller(
		WithIPEchoURL(echo.URL),
		WithControlURL(ctrl.URL),
		WithHTTPClient(ctrl.Client()),
		WithPollInterval(20*time.Millisecond),
	)
	// Seed with the same IP the echo server returns.
	e.mu.Lock()
	e.lastPublicIP = "3.3.3.3"
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go e.WatchIPChanges(ctx, "stableulid")
	<-ctx.Done()

	if n := atomic.LoadInt64(&updateCount); n != 0 {
		t.Errorf("WatchIPChanges: expected 0 dns updates when IP stable; got %d", n)
	}
}

// ---------------------------------------------------------------------------
// EnrollerOption / env-configurable control URL
// ---------------------------------------------------------------------------

func TestEnrollerControlURLFromEnv(t *testing.T) {
	const want = "https://custom.control.example.com"
	t.Setenv("VULOS_CONTROL_URL", want)
	e := NewEnroller()
	if e.controlURL != want {
		t.Errorf("controlURL = %q; want %q", e.controlURL, want)
	}
}

func TestEnrollerIPEchoURLFromEnv(t *testing.T) {
	const want = "https://myecho.example.com"
	t.Setenv("VULOS_IP_ECHO_URL", want)
	e := NewEnroller()
	if e.ipEchoURL != want {
		t.Errorf("ipEchoURL = %q; want %q", e.ipEchoURL, want)
	}
}
