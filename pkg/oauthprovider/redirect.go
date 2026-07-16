package oauthprovider

import (
	"crypto/subtle"
	"fmt"
	"net/url"
	"strings"
)

// exactMatch reports whether candidate equals one of the registered URIs
// byte-for-byte. Exact matching (no prefix / wildcard / normalisation) is the
// single most important defence against open-redirect token theft. The compare
// is constant-time per-entry to avoid leaking which prefix matched.
func exactMatch(candidate string, registered []string) bool {
	for _, r := range registered {
		if len(r) == len(candidate) && subtle.ConstantTimeCompare([]byte(r), []byte(candidate)) == 1 {
			return true
		}
	}
	return false
}

// sanitizeRedirectURIs validates + de-duplicates redirect URIs at registration
// time. Each must be an absolute URL with an https scheme, OR an http URL whose
// host is a loopback address (localhost / 127.0.0.1 / ::1) for local dev. A
// fragment is never allowed in a redirect URI (RFC 6749 §3.1.2).
func sanitizeRedirectURIs(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if seen[raw] {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("oauthprovider: invalid redirect_uri %q: %w", raw, err)
		}
		if u.Fragment != "" || strings.Contains(raw, "#") {
			return nil, fmt.Errorf("oauthprovider: redirect_uri %q must not contain a fragment", raw)
		}
		if !u.IsAbs() || u.Host == "" {
			return nil, fmt.Errorf("oauthprovider: redirect_uri %q must be an absolute URL", raw)
		}
		switch u.Scheme {
		case "https":
			// always allowed
		case "http":
			if !isLoopbackHost(u.Hostname()) {
				return nil, fmt.Errorf("oauthprovider: redirect_uri %q must use https (http allowed only for loopback)", raw)
			}
		default:
			return nil, fmt.Errorf("oauthprovider: redirect_uri %q has unsupported scheme %q", raw, u.Scheme)
		}
		seen[raw] = true
		out = append(out, raw)
	}
	return out, nil
}

func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// BuildRedirect appends query parameters to a (trusted, exact-matched) redirect
// URI, preserving any existing query string. Used to send the code/state back
// to the client and to relay redirectable errors.
func BuildRedirect(redirectURI string, params map[string]string) (string, error) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
