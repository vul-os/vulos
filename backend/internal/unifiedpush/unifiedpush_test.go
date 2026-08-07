package unifiedpush

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- store isolation, cap, validation ---------------------------------------

func testStores(t *testing.T) map[string]Store {
	t.Helper()
	sq, err := NewSQLiteStore(filepath.Join(t.TempDir(), "endpoints.sqlite"))
	if err != nil {
		t.Fatalf("sqlite store: %v", err)
	}
	t.Cleanup(func() { sq.Close() })
	return map[string]Store{"mem": NewMemStore(), "sqlite": sq}
}

func sampleEndpoint(url string) Endpoint {
	return Endpoint{URL: url}
}

func TestUnifiedPushStore_PerOwnerIsolation(t *testing.T) {
	for name, st := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := st.Save("alice", sampleEndpoint("https://up.example/a")); err != nil {
				t.Fatal(err)
			}
			if err := st.Save("bob", sampleEndpoint("https://up.example/b")); err != nil {
				t.Fatal(err)
			}
			a, _ := st.List("alice")
			if len(a) != 1 || a[0].URL != "https://up.example/a" {
				t.Fatalf("alice sees wrong endpoints: %+v", a)
			}
			if a[0].OwnerID != "alice" {
				t.Fatalf("owner not stamped: %q", a[0].OwnerID)
			}
			// bob deleting alice's URL string must NOT touch alice's row.
			if err := st.Delete("bob", "https://up.example/a"); err != nil {
				t.Fatal(err)
			}
			if a2, _ := st.List("alice"); len(a2) != 1 {
				t.Fatalf("cross-owner delete leaked: alice now has %d", len(a2))
			}
		})
	}
}

func TestUnifiedPushStore_CapEvictsOldest(t *testing.T) {
	for name, st := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			base := time.Now().Add(-time.Hour)
			for i := 0; i < MaxEndpointsPerUser; i++ {
				e := sampleEndpoint("https://up.example/" + string(rune('a'+i)))
				e.CreatedAt = base.Add(time.Duration(i) * time.Minute)
				if err := st.Save("u", e); err != nil {
					t.Fatal(err)
				}
			}
			// One more (newer) → the oldest ("a") is evicted, count stays capped.
			extra := sampleEndpoint("https://up.example/NEW")
			extra.CreatedAt = time.Now()
			if err := st.Save("u", extra); err != nil {
				t.Fatal(err)
			}
			eps, _ := st.List("u")
			if len(eps) != MaxEndpointsPerUser {
				t.Fatalf("cap not enforced: %d endpoints", len(eps))
			}
			for _, e := range eps {
				if e.URL == "https://up.example/a" {
					t.Fatalf("oldest endpoint was not evicted")
				}
			}
		})
	}
}

func TestValidate(t *testing.T) {
	ok := Endpoint{URL: "https://up.example/x"}
	if err := Validate(ok); err != nil {
		t.Fatalf("valid endpoint rejected: %v", err)
	}
	cases := []Endpoint{
		{URL: ""},
		{URL: "http://up.example/x"}, // not https
		{URL: "https://" + strings.Repeat("x", maxEndpointLen)},
	}
	for i, c := range cases {
		if err := Validate(c); err == nil {
			t.Fatalf("case %d: invalid endpoint accepted", i)
		}
	}
}

// TestValidate_SSRF is the registration-time SSRF gate: a user-supplied
// UnifiedPush endpoint pointing at loopback, the box's own LAN, link-local,
// or a cloud metadata address must be REJECTED, exactly as
// backend/internal/webpush's subscription validation rejects the same shapes
// (both reuse backend/internal/safedial).
func TestValidate_SSRF(t *testing.T) {
	blocked := []string{
		"https://127.0.0.1/push",             // loopback
		"https://localhost/push",             // loopback name
		"https://[::1]/push",                 // IPv6 loopback
		"https://169.254.169.254/latest/api", // cloud metadata (link-local)
		"https://10.0.0.5/push",              // RFC1918 (box's own LAN)
		"https://192.168.1.1/push",           // RFC1918
		"https://172.16.0.1/push",            // RFC1918
		"https://2130706433/push",            // decimal-encoded 127.0.0.1
		"https://0x7f000001/push",            // hex-encoded 127.0.0.1
		"https://box.internal/push",          // internal hostname
		"https://svc.local/push",             // mDNS/.local
		"https://[::ffff:127.0.0.1]/push",    // IPv4-mapped loopback
	}
	for _, u := range blocked {
		ep := Endpoint{URL: u}
		if err := Validate(ep); err == nil {
			t.Fatalf("SSRF endpoint accepted: %s", u)
		}
	}
	// A real distributor endpoint (public host) must still validate.
	ok := Endpoint{URL: "https://ntfy.sh/up/abc123"}
	if err := Validate(ok); err != nil {
		t.Fatalf("public distributor endpoint rejected: %v", err)
	}
}

// TestLiveSender_RefusesInternalEndpoint asserts the SECOND SSRF layer: even
// if an endpoint somehow reached the sender unvalidated (e.g. a legacy row
// from before this guard existed), LiveSender.Send refuses to POST to it.
func TestLiveSender_RefusesInternalEndpoint(t *testing.T) {
	sender := LiveSender{}
	_, err := sender.Send(Endpoint{URL: "https://127.0.0.1:1/push"}, []byte(`{}`), Config{Enabled: true})
	if err == nil {
		t.Fatal("LiveSender accepted a loopback endpoint")
	}
}

// TestLoadConfig_FailSafeOff confirms the feature is OFF unless explicitly
// opted in — no env var set means Enabled is false.
func TestLoadConfig_FailSafeOff(t *testing.T) {
	cfg := LoadConfig()
	if cfg.Enabled {
		t.Fatal("UnifiedPush enabled with no VULOS_PUSH_UNIFIEDPUSH_ENABLE set")
	}
}

func TestLoadConfig_EnabledViaEnv(t *testing.T) {
	t.Setenv("VULOS_PUSH_UNIFIEDPUSH_ENABLE", "1")
	cfg := LoadConfig()
	if !cfg.Enabled {
		t.Fatal("UnifiedPush not enabled with VULOS_PUSH_UNIFIEDPUSH_ENABLE=1")
	}
}

// TestLiveSender_BlocksHTTPTestServer confirms the SSRF guard fires even
// against a real httptest.Server (127.0.0.1) standing in for what a
// malicious "distributor" endpoint would be — the guard does not special-case
// test infra. End-to-end delivery to a real endpoint (payload content,
// prune-on-404/410) is exercised at the notify.Service layer via a fake
// Sender, since a live target the guard permits cannot be stood up in a
// unit test by construction (see services/notify/unifiedpush_service_test.go).
func TestLiveSender_BlocksHTTPTestServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	sender := LiveSender{}
	_, err := sender.Send(Endpoint{URL: srv.URL}, []byte(`{}`), Config{Enabled: true})
	if err == nil {
		t.Fatal("LiveSender delivered to a loopback httptest server — SSRF guard did not fire")
	}
}
