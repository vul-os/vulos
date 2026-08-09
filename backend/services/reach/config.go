package reach

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// config.go — loading and validating the relay endpoint set.
//
// # Where the set comes from (highest precedence wins)
//
//  1. VULOS_RELAY_ENDPOINTS_FILE — path to a JSON array. PREFERRED: the file
//     holds bearer tokens, so it lives on disk at 0600 where the process
//     environment (which leaks into `ps`, crash dumps, and child processes)
//     never sees them.
//  2. VULOS_RELAY_ENDPOINTS — the same JSON array inline. For Fly/Docker/
//     Kubernetes secret injection, where a file is awkward and the platform
//     already treats the environment as the secret channel.
//  3. VULOS_RELAY_BASE_URL + VULOS_RELAY_NAME + VULOS_RELAY_TOKEN — the
//     single-endpoint form every earlier release used. Kept working verbatim,
//     so upgrading a box changes nothing until its operator opts into a set.
//
// The forms do not merge. A box configured with a file ignores the inline and
// legacy variables entirely, so there is exactly one answer to "where did this
// endpoint come from" and no way to end up with a set that is the accidental
// union of two half-finished configurations.
//
// # Why the whole set is refused on one bad entry
//
// Reachability config that half-applies is worse than config that does not
// apply: an operator who typo'd relay B's URL would get a box that silently
// runs on relay A alone, believing it had redundancy it does not have. Load
// therefore validates every entry and returns an error naming the offending
// index, and the caller starts with NO relays rather than with a subset. That
// is a loud, correctable failure instead of a quiet, false one.

// Environment variable names. Exported so docs, tests, and status surfaces
// name them from one place rather than restating string literals.
const (
	EnvEndpointsFile = "VULOS_RELAY_ENDPOINTS_FILE"
	EnvEndpoints     = "VULOS_RELAY_ENDPOINTS"

	// Legacy single-endpoint form (still supported).
	EnvLegacyBaseURL = "VULOS_RELAY_BASE_URL"
	EnvLegacyName    = "VULOS_RELAY_NAME"
	EnvLegacyToken   = "VULOS_RELAY_TOKEN"

	// EnvAllowInsecure permits http:// (rather than https://) endpoints, and
	// only for loopback hosts. Development affordance: it is read from the
	// box's OWN process environment and is unreachable from any HTTP surface.
	EnvAllowInsecure = "VULOS_RELAY_ALLOW_INSECURE"
)

// Source records which of the three forms produced the set, for status
// display and for the startup log line.
type Source string

const (
	SourceNone   Source = "none"
	SourceFile   Source = "file"
	SourceInline Source = "inline"
	SourceLegacy Source = "legacy"
)

// FromEnv loads and validates the endpoint set from the process environment.
//
// A box with none of the variables set gets an EMPTY set and a nil error —
// that is the expected posture for a box with a public IP or a LAN-only box,
// not a misconfiguration. Callers must handle Len()==0 by degrading.
func FromEnv() (*Set, Source, error) {
	if path := strings.TrimSpace(os.Getenv(EnvEndpointsFile)); path != "" {
		eps, err := loadFile(path)
		if err != nil {
			return NewSet(nil), SourceFile, err
		}
		return NewSet(eps), SourceFile, nil
	}

	if inline := strings.TrimSpace(os.Getenv(EnvEndpoints)); inline != "" {
		eps, err := parseJSON([]byte(inline))
		if err != nil {
			return NewSet(nil), SourceInline, fmt.Errorf("%s: %w", EnvEndpoints, err)
		}
		return NewSet(eps), SourceInline, nil
	}

	base := strings.TrimSpace(os.Getenv(EnvLegacyBaseURL))
	if base == "" {
		return NewSet(nil), SourceNone, nil
	}
	ep := Endpoint{
		BaseURL: base,
		Name:    strings.TrimSpace(os.Getenv(EnvLegacyName)),
		Token:   strings.TrimSpace(os.Getenv(EnvLegacyToken)),
	}
	eps, err := normalise([]Endpoint{ep})
	if err != nil {
		return NewSet(nil), SourceLegacy, fmt.Errorf("%s/%s/%s: %w",
			EnvLegacyBaseURL, EnvLegacyName, EnvLegacyToken, err)
	}
	return NewSet(eps), SourceLegacy, nil
}

// loadFile reads and validates a JSON endpoints file.
//
// SECURITY: the file holds bearer tokens, so a world-readable or
// world-writable file is REFUSED rather than loaded with a warning. A warning
// would be ignored, and the failure mode (every relay grant this box holds,
// readable by any local user) is severe enough to be worth a startup error
// the operator must fix with a one-line chmod.
func loadFile(path string) ([]Endpoint, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvEndpointsFile, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s: %s is a directory", EnvEndpointsFile, path)
	}
	if mode := fi.Mode().Perm(); mode&0o007 != 0 {
		return nil, fmt.Errorf(
			"%s: %s is world-accessible (mode %#o) and holds relay bearer tokens — run: chmod 600 %s",
			EnvEndpointsFile, path, mode, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvEndpointsFile, err)
	}
	eps, err := parseJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%s (%s): %w", EnvEndpointsFile, path, err)
	}
	return eps, nil
}

// parseJSON decodes a JSON array of endpoints and validates it. Unknown
// fields are REJECTED: a typo'd key ("uri" for "url", "secret" for "token")
// would otherwise be silently dropped and produce an endpoint that fails to
// authenticate for reasons the operator cannot see in their own config.
func parseJSON(raw []byte) ([]Endpoint, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var eps []Endpoint
	if err := dec.Decode(&eps); err != nil {
		return nil, fmt.Errorf("parse: %w (expected a JSON array of {\"url\",\"name\",\"token\"} objects)", err)
	}
	return normalise(eps)
}

// normalise validates every entry, applies defaults, and de-duplicates by
// BaseURL. It returns an error naming the offending index so an operator with
// a six-relay file can find the bad one without bisecting.
func normalise(in []Endpoint) ([]Endpoint, error) {
	if len(in) == 0 {
		return nil, errors.New("no relay endpoints configured (expected at least one)")
	}
	if len(in) > MaxEndpoints {
		return nil, fmt.Errorf("%d relay endpoints configured, maximum is %d", len(in), MaxEndpoints)
	}

	allowInsecure := allowInsecure()
	seen := make(map[string]int, len(in))
	out := make([]Endpoint, 0, len(in))

	for i, e := range in {
		e.BaseURL = strings.TrimRight(strings.TrimSpace(e.BaseURL), "/")
		e.Name = strings.TrimSpace(e.Name)
		e.Token = strings.TrimSpace(e.Token)
		e.Region = strings.TrimSpace(e.Region)

		if err := validateURL(e.BaseURL, allowInsecure); err != nil {
			return nil, fmt.Errorf("relay endpoint %d: %w", i, err)
		}
		if err := ValidName(e.Name); err != nil {
			return nil, fmt.Errorf("relay endpoint %d (%s): %w", i, e.BaseURL, err)
		}
		if err := validateToken(e.Token); err != nil {
			return nil, fmt.Errorf("relay endpoint %d (%s): %w", i, e.BaseURL, err)
		}
		if len(e.Region) > MaxRegionLen {
			return nil, fmt.Errorf("relay endpoint %d (%s): region longer than %d bytes", i, e.BaseURL, MaxRegionLen)
		}
		if prev, dup := seen[e.BaseURL]; dup {
			return nil, fmt.Errorf("relay endpoint %d duplicates endpoint %d (%s) — one grant per relay",
				i, prev, e.BaseURL)
		}
		seen[e.BaseURL] = i

		// Unset priority means "keep configuration order".
		if e.Priority == 0 {
			e.Priority = i
		}
		out = append(out, e)
	}
	return out, nil
}

// allowInsecure reports the development opt-out for the https requirement.
func allowInsecure() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvAllowInsecure))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// validateURL enforces the transport invariants: a parseable absolute URL,
// https (or loopback http under the dev opt-out), a real host, and NO
// embedded userinfo — credentials belong in Token, where redaction applies.
func validateURL(raw string, allowInsecure bool) error {
	if raw == "" {
		return errors.New("url is required")
	}
	if len(raw) > MaxURLLen {
		return fmt.Errorf("url longer than %d bytes", MaxURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url %q is not parseable: %w", raw, err)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q has no host", raw)
	}
	if u.User != nil {
		// Do NOT interpolate raw/u here: it embeds the operator's credentials
		// (e.g. "https://user:pass@host/..."), and this error propagates up
		// through Load/FromEnv into a plain log.Printf in cmd/server — logging
		// raw would put the secret in the box's own log file. u.Redacted()
		// keeps the host/path for diagnosis while replacing the password.
		return fmt.Errorf("url %s embeds credentials — put the bearer grant in \"token\" instead", u.Redacted())
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("url %q must not carry a query or fragment", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if !allowInsecure {
			return fmt.Errorf("url %q is plaintext http — the relay grant rides these requests; use https (or set %s=1 for a loopback dev relay)",
				raw, EnvAllowInsecure)
		}
		if !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("url %q is plaintext http to a non-loopback host — refused even with %s=1",
				raw, EnvAllowInsecure)
		}
		return nil
	default:
		return fmt.Errorf("url %q has unsupported scheme %q (want https)", raw, u.Scheme)
	}
}

// isLoopbackHost reports whether host is a loopback literal or "localhost".
// Kept deliberately narrow: this gates the plaintext-http dev affordance, so
// anything it is unsure about must be treated as NOT loopback.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

// ValidName enforces a single DNS label: the name becomes a public hostname
// component (<name>.relay.example.com) and a path segment (/t/<name>/), so
// anything outside the label alphabet is either unrepresentable or an
// injection risk in one of those two positions.
//
// The tunnel package deliberately carries its own copy of this rule (the
// relay role must not link the box-side endpoint machinery). Its test imports
// this function and asserts the two agree, so the duplication cannot drift.
func ValidName(name string) error {
	if name == "" {
		return errors.New("name is required (it becomes this box's public hostname on that relay)")
	}
	if len(name) > MaxNameLen {
		return fmt.Errorf("name %q is longer than %d bytes", name, MaxNameLen)
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return fmt.Errorf("name %q must not start or end with a hyphen", name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return fmt.Errorf("name %q contains %q — names are lowercase a-z, 0-9 and hyphen (one DNS label)", name, string(c))
		}
	}
	return nil
}

// validateToken bounds the bearer grant and rejects control characters, which
// cannot appear in an HTTP header value and would otherwise fail far from
// here with an opaque error.
func validateToken(tok string) error {
	if tok == "" {
		return errors.New("token is required (the bearer grant the relay authorised this name with)")
	}
	if len(tok) > MaxTokenLen {
		return fmt.Errorf("token is longer than %d bytes", MaxTokenLen)
	}
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; c < 0x20 || c == 0x7f {
			return errors.New("token contains a control character")
		}
	}
	return nil
}

// SplitList parses a comma-separated configuration value into a list,
// trimming whitespace and dropping empty entries.
//
// It exists so that every "this setting accepts several values" surface in the
// OS — relay endpoints, rendezvous nodes, browser origins — parses them the
// same way. A setting that accepted "a,b" in one place and "a, b" in another
// would be a papercut an operator hits exactly once, at 2am, on the box they
// cannot reach.
func SplitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
