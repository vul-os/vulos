package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
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
