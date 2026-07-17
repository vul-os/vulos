// wire_operational_extra_test.go — proves the network/routing + storage
// operational route groups wired by registerNetworkOperational are (a) actually
// MOUNTED in the zero-config self-host binary (never 404) and (b) AUTHZ-GATED
// (an unauthenticated caller is refused — 401/403, or an honest 503 for a group
// that fail-closes on a missing secret — never a fail-open 200 and never a 404
// implying the route was silently dropped).
//
// This is the regression guard for "management is functionally complete for
// self-hosting": if any of enrollment, the OS routing plane (DNS/relay/edge/CDN/
// multi-location), integrations, the mail key directory, cloud-home, or the
// storage/files/selection/resolver plane is un-wired or fails open, a case here
// fails.
package cproutes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/billingport"
)

// buildNetworkOperationalMux assembles the shared stores exactly as
// RegisterOperational does, then mounts registerNetworkOperational onto a fresh
// mux. Returns the mux and the auth store (so a test can mint a real session).
func buildNetworkOperationalMux(t *testing.T) (*http.ServeMux, *auth.Store) {
	t.Helper()
	// Deterministic, isolated per-subsystem SQLite dir for this test process.
	t.Setenv("VULOS_DB_DIR", t.TempDir())

	as := openTestAuthStore(t)
	deps := OperationalDeps{
		AuthStore:      as,
		Entitlements:   billingport.NewNoopResolver(),
		DBDir:          t.TempDir(),
		AdminAccountID: "", // no admin ⇒ admin-gated legs deny (fail-closed)
	}

	fleetStore, fleetCloser := openFleetStore(deps.DBDir)
	t.Cleanup(fleetCloser)
	routingStore, routingCloser := openRoutingStore(deps.DBDir)
	t.Cleanup(routingCloser)

	mux := http.NewServeMux()
	closers := registerNetworkOperational(mux, deps, fleetStore, routingStore)
	t.Cleanup(func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	})
	return mux, as
}

// gatedCase is one representative route of a newly-wired group.
type gatedCase struct {
	group  string
	method string
	path   string
}

// TestNetworkOperationalGroupsWiredAndGated hits a representative session-gated
// route of every newly-wired operational group with NO credentials and asserts
// the route is mounted (not 404) and refuses the caller (not a 2xx fail-open).
func TestNetworkOperationalGroupsWiredAndGated(t *testing.T) {
	mux, _ := buildNetworkOperationalMux(t)

	cases := []gatedCase{
		// Device enrollment (RFC-8628 + web wizard).
		{"enroll/web", http.MethodPost, "/api/enroll"},
		{"enroll/connmode", http.MethodPost, "/api/connmode"},
		// OS routing plane.
		{"dnsplane/zone", http.MethodGet, "/api/dnsplane/zone/01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"relay/status", http.MethodGet, "/api/relay/status"},
		{"edge/config", http.MethodGet, "/api/edge/config"},
		{"cdn/config", http.MethodGet, "/api/cdn/config"},
		// org_id supplied so the request reaches the session/org-admin gate (the
		// handler validates the required query param before authenticating).
		{"multiloc/locations", http.MethodGet, "/api/multiloc/locations?org_id=org_test"},
		// Third-party OAuth data broker.
		{"integrations/list", http.MethodGet, "/api/integrations"},
		// Mail key directory.
		{"keydir/put", http.MethodPut, "/api/mail/keydir"},
		// Cloud-home peering intake (fail-closed to 503 without CLOUDHOME_KEK).
		{"cloudhome/revoke", http.MethodPost, "/api/cloudhome/revoke"},
		// Storage / files / selection / resolver plane.
		{"storage/config", http.MethodGet, "/api/storage/config"},
		{"storage/backend", http.MethodGet, "/api/storage/backend"},
		{"resolve/backend", http.MethodGet, "/api/resolve/backend"},
	}

	for _, c := range cases {
		t.Run(c.group, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code == http.StatusNotFound {
				t.Fatalf("%s %s = 404 — group %q is NOT wired into the self-host binary",
					c.method, c.path, c.group)
			}
			// Fail-closed: an unauthenticated caller must be refused, never served.
			// 401/403 = auth gate; 503 = a group that fail-closes on a missing
			// secret (cloud-home without CLOUDHOME_KEK). Anything 2xx is a fail-open.
			switch rr.Code {
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable:
				// gated as required
			default:
				if rr.Code >= 200 && rr.Code < 300 {
					t.Fatalf("%s %s = %d (2xx) — group %q FAILS OPEN to an unauthenticated caller",
						c.method, c.path, rr.Code, c.group)
				}
				// A non-2xx, non-404 (e.g. 400 bad body) still proves mounted+gated
				// upstream of any body handling only if auth ran first; to be strict
				// we require the auth codes above for these session-gated routes.
				t.Fatalf("%s %s = %d — group %q not gated with 401/403/503; got unexpected status",
					c.method, c.path, rr.Code, c.group)
			}
		})
	}
}

// TestBootEnrollPublicLegsMounted proves the RFC-8628 device-authorization legs a
// box hits BEFORE it has a session (POST /enroll/start, /enroll/poll) are mounted
// (not 404). These are intentionally public + per-IP rate-limited, so an empty
// body yields a 4xx from the handler — the point is they are NOT missing.
func TestBootEnrollPublicLegsMounted(t *testing.T) {
	mux, _ := buildNetworkOperationalMux(t)
	for _, path := range []string{"/enroll/start", "/enroll/poll"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code == http.StatusNotFound {
			t.Fatalf("POST %s = 404 — boot-enrollment (RFC-8628) is NOT wired", path)
		}
	}
}

// TestRelayStatusAuthenticates proves the auth gate is a REAL gate, not a
// dead deny-all: a valid session gets a 200 (not another 401). This closes the
// L1-style "dead-gated" failure mode where a group is mounted but rejects even
// legitimate callers.
func TestRelayStatusAuthenticates(t *testing.T) {
	mux, as := buildNetworkOperationalMux(t)
	_, cookie := signupAndSession(t, as, "wire-op@example.test", "correct-horse-battery-staple-9")

	req := httptest.NewRequest(http.MethodGet, "/api/relay/status", nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/relay/status with a valid session = %d, want 200 (gate must authenticate, not deny-all)", rr.Code)
	}
}
