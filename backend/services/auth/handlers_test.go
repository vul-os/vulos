package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// newReq is a helper that builds a GET request with the given Host header.
func newReq(host string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "https://"+host+"/", nil)
	r.Host = host
	return r
}

func TestCookieDomain(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		vulosDomain string // non-empty → set VULOS_DOMAIN env var
		want        string
	}{
		// ── VULOS_DOMAIN env var set ──────────────────────────────────────────
		{
			name:        "env set without leading dot",
			vulosDomain: "01h5t3e8k2qj7r9xmvn4p.vulos.org",
			host:        "browser--work.01h5t3e8k2qj7r9xmvn4p.vulos.org",
			want:        ".01h5t3e8k2qj7r9xmvn4p.vulos.org",
		},
		{
			name:        "env set already has leading dot",
			vulosDomain: ".01h5t3e8k2qj7r9xmvn4p.vulos.org",
			host:        "terminal--default.01h5t3e8k2qj7r9xmvn4p.vulos.org",
			want:        ".01h5t3e8k2qj7r9xmvn4p.vulos.org",
		},
		{
			name:        "env set to lvh.me dev value",
			vulosDomain: "lvh.me",
			host:        "cockpit.lvh.me",
			want:        ".lvh.me",
		},

		// ── fabric/direct-mode bare instance root, NO VULOS_DOMAIN set ────────
		// (CROSS-TENANT-COOKIE regression: this is the real Host a fabric/direct
		// box's OS API sees, since VULOS_DOMAIN is never populated for that mode
		// — see cookieDomain's comment. It must NOT collapse to ".vulos.org".)
		{
			name: "bare fabric instance root — must not collapse to shared apex",
			host: "01h5t3e8k2qj7r9xmvn4p.vulos.org",
			want: ".01h5t3e8k2qj7r9xmvn4p.vulos.org",
		},
		{
			name: "bare fabric instance root with port",
			host: "01h5t3e8k2qj7r9xmvn4p.vulos.org:443",
			want: ".01h5t3e8k2qj7r9xmvn4p.vulos.org",
		},
		{
			name: "a DIFFERENT instance's bare root must not match either",
			host: "zzzzzzzzzzzzzzzzzzzzzzzzzz.vulos.org",
			want: ".zzzzzzzzzzzzzzzzzzzzzzzzzz.vulos.org",
		},

		// A two-label host has no label to spare: stripping one leaves a bare
		// public suffix, which browsers reject outright (so the session cookie
		// is dropped and login never persists). Scope to the exact host.
		{
			name: "bare apex host must not strip to a public suffix",
			host: "mybox.com",
			want: ".mybox.com",
		},
		{
			name: "dev base domain reached directly",
			host: "lvh.me:3000",
			want: ".lvh.me",
		},

		// ── {app}--{profile}.{ulid}.{tld} subdomains (the NET-05 case) ────────
		{
			name: "browser--work under ulid subdomain",
			host: "browser--work.01h5t3e8k2qj7r9xmvn4p.vulos.org",
			want: ".01h5t3e8k2qj7r9xmvn4p.vulos.org",
		},
		{
			name: "terminal--default under ulid subdomain",
			host: "terminal--default.01h5t3e8k2qj7r9xmvn4p.vulos.org",
			want: ".01h5t3e8k2qj7r9xmvn4p.vulos.org",
		},
		{
			name: "browser--personal under ulid subdomain",
			host: "browser--personal.01h5t3e8k2qj7r9xmvn4p.vulos.org",
			want: ".01h5t3e8k2qj7r9xmvn4p.vulos.org",
		},

		// ── Host with port ────────────────────────────────────────────────────
		{
			name: "app--profile subdomain with port",
			host: "browser--work.01h5t3e8k2qj7r9xmvn4p.vulos.org:8443",
			want: ".01h5t3e8k2qj7r9xmvn4p.vulos.org",
		},

		// ── Plain subdomains (no "--") ────────────────────────────────────────
		{
			name: "plain subdomain scoped to parent domain",
			host: "cockpit.lvh.me",
			want: ".lvh.me",
		},
		{
			name: "plain subdomain with port scoped to parent domain",
			host: "cockpit.lvh.me:3000",
			want: ".lvh.me",
		},

		// ── IP addresses — no domain scoping ─────────────────────────────────
		{
			name: "IPv4 address",
			host: "192.168.1.10",
			want: "",
		},
		{
			name: "IPv4 with port",
			host: "192.168.1.10:8080",
			want: "",
		},
		{
			name: "IPv6 loopback",
			host: "[::1]",
			want: "",
		},

		// ── localhost / single-label ──────────────────────────────────────────
		{
			name: "localhost",
			host: "localhost",
			want: "",
		},
		{
			name: "localhost with port",
			host: "localhost:8080",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.vulosDomain != "" {
				t.Setenv("VULOS_DOMAIN", tc.vulosDomain)
			} else {
				// Ensure a stale env var from a previous test (or CI environment)
				// does not bleed into this case.
				os.Unsetenv("VULOS_DOMAIN")
			}

			got := cookieDomain(newReq(tc.host))
			if got != tc.want {
				t.Errorf("cookieDomain(%q) = %q; want %q", tc.host, got, tc.want)
			}
		})
	}
}

// makeCSRFTestHandler returns a Middleware-wrapped handler seeded with one
// user+session. The cookie token is returned so tests can set it.
func makeCSRFTestHandler(t *testing.T) (cookieToken string, mw http.Handler) {
	t.Helper()
	store := &Store{
		users:    make(map[string]*User),
		sessions: make(map[string]*Session),
		profiles: make(map[string]*Profile),
		secret:   []byte("test-secret-m6"),
	}
	u := &User{ID: "uid-csrf", Username: "csrfuser", Email: "csrf@test.local"}
	store.users["uid-csrf"] = u
	// Create a live session token for the cookie.
	sess := store.CreateSession(u, "csrf-test-device")
	cookieToken = sess.Token
	h := NewHandler(store)
	echo := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return cookieToken, h.Middleware(echo)
}

// TestCSRF_CookieAuthMutationWithoutJSONRejected verifies M6: a POST
// authenticated via session cookie without Content-Type: application/json
// and without a matching Origin header is rejected with 403.
func TestCSRF_CookieAuthMutationWithoutJSONRejected(t *testing.T) {
	tok, mw := makeCSRFTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader("data=x"))
	req.Host = "vulos.local"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded") // browser form default
	req.Header.Set("Origin", "https://evil.example.com")                // cross-site origin
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: tok})
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for CSRF-unprotected POST, got %d", w.Code)
	}
}

// TestCSRF_CookieAuthMutationWithJSONAllowed verifies that cookie-authed POST
// with Content-Type: application/json is accepted (JSON CT is CSRF-safe).
func TestCSRF_CookieAuthMutationWithJSONAllowed(t *testing.T) {
	tok, mw := makeCSRFTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{}`))
	req.Host = "vulos.local"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example.com") // cross-site — but JSON CT exempts
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: tok})
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatal("expected non-403 for application/json cookie-authed POST, got 403")
	}
}

// TestCSRF_CookieAuthMutationSameOriginAllowed verifies that a same-origin
// POST (matching Origin and Host) is accepted even without JSON content-type.
func TestCSRF_CookieAuthMutationSameOriginAllowed(t *testing.T) {
	tok, mw := makeCSRFTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader("data=x"))
	req.Host = "vulos.local"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://vulos.local") // same origin as Host
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: tok})
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatal("expected non-403 for same-origin POST, got 403")
	}
}

// TestCSRF_BearerAuthMutationSkipsCheck verifies that a Bearer-authenticated
// POST is NOT subject to the CSRF check (Bearer tokens are not cookies).
func TestCSRF_BearerAuthMutationSkipsCheck(t *testing.T) {
	tok, mw := makeCSRFTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader("data=x"))
	req.Host = "vulos.local"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Authorization", "Bearer "+tok) // Bearer, not cookie
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	// Bearer auth means authedViaCookie=false → CSRF check skipped.
	if w.Code == http.StatusForbidden {
		t.Fatal("expected non-403 for Bearer-authed POST (CSRF check should be skipped)")
	}
}
