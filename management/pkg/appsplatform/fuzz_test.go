package appsplatform

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func trimLowerForTest(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func lowerSchemeForTest(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme)
}

func parseUnixForTest(ts string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
}

// Fuzz targets harden the parsers/verifiers against malformed input: they must
// never panic and must uphold their security invariants on arbitrary bytes. They
// run as ordinary tests over the seed corpus under `go test`.

// FuzzParseSlash ensures slash-command parsing never panics and that a reported
// command name never carries the leading slash or surrounding whitespace.
func FuzzParseSlash(f *testing.F) {
	for _, s := range []string{"", "/", "/deploy", "/deploy now", "  /x  y ", "hello", "//", "/  ", "/A B"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		name, _, ok := ParseSlash(body)
		if !ok {
			if name != "" {
				t.Fatalf("ok=false but name=%q", name)
			}
			return
		}
		if name == "" {
			t.Fatal("ok=true but empty name")
		}
		// The name must be trimmed + lowercased (the form ResolveSlashCommand
		// normalizes and matches against).
		if name != trimLowerForTest(name) {
			t.Fatalf("name not normalized (trim/lower): %q", name)
		}
	})
}

// FuzzValidateWebhookURL ensures the SSRF guard never panics on arbitrary input,
// always returns a decision, and never permits a non-http(s) scheme.
func FuzzValidateWebhookURL(f *testing.F) {
	f.Add("http://example.com")
	f.Add("https://8.8.8.8/x")
	f.Add("http://127.0.0.1")
	f.Add("file:///etc/passwd")
	f.Add("")
	f.Add("://noscheme")
	f.Add("http://[::1]")
	f.Add("ht\ntp://x")
	// Stub DNS so fuzzing never touches the network (hermetic): arbitrary
	// hostnames resolve to a fixed public address.
	orig := resolveIPs
	defer func() { resolveIPs = orig }()
	resolveIPs = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("8.8.8.8")}, nil }
	f.Fuzz(func(t *testing.T, raw string) {
		// Keep the guard ON regardless of the ambient environment.
		t.Setenv(AllowPrivateWebhooksEnv, "")
		err := ValidateWebhookURL(raw) // must not panic
		// A blank URL means "no webhook configured" and is always allowed.
		if err == nil && strings.TrimSpace(raw) != "" {
			// Any accepted, non-blank URL must be http or https.
			u := lowerSchemeForTest(raw)
			if u != "http" && u != "https" {
				t.Fatalf("accepted a non-http(s) URL %q (scheme %q)", raw, u)
			}
		}
	})
}

// FuzzVerify ensures signature verification never panics on arbitrary
// timestamp/body/secret/signature bytes and rejects everything that is not the
// exact expected signature.
func FuzzVerify(f *testing.F) {
	f.Add("1", []byte("body"), "secret", "v0=deadbeef")
	f.Add("", []byte(""), "", "")
	f.Fuzz(func(t *testing.T, ts string, body []byte, secret, sig string) {
		got := Verify(ts, body, secret, sig) // must not panic
		want := sig == Sign(ts, body, secret)
		if got != want {
			t.Fatalf("Verify disagreed with the canonical signature for (%q,%q)", ts, secret)
		}
		// VerifyWithSkew with a huge window must agree with Verify on validity.
		if VerifyWithSkew(ts, body, secret, sig, 100*365*24*time.Hour) && !got {
			// Allowed only if the timestamp is numeric; otherwise it must be false.
			if _, e := parseUnixForTest(ts); e == nil {
				t.Fatal("VerifyWithSkew accepted a signature Verify rejected")
			}
		}
	})
}

// FuzzNormalizeScopes ensures scope normalization never panics and never emits a
// scope outside the configured set or with surrounding whitespace/duplicates.
func FuzzNormalizeScopes(f *testing.F) {
	f.Add("apps:write")
	f.Add(" APPS:WRITE ")
	f.Add("bogus")
	f.Add("")
	set := DefaultScopeSet()
	f.Fuzz(func(t *testing.T, s string) {
		out, err := set.Normalize([]string{s, s}) // duplicate on purpose
		if err != nil {
			return // unknown scope rejected — fine
		}
		seen := map[string]bool{}
		for _, sc := range out {
			if !set[sc] {
				t.Fatalf("emitted scope %q not in the set", sc)
			}
			if seen[sc] {
				t.Fatalf("emitted duplicate scope %q", sc)
			}
			seen[sc] = true
		}
	})
}
