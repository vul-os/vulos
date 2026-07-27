package reach

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- helpers ---------------------------------------------------------------

// freezeClock pins nowFn and removes backoff jitter so ordering assertions are
// deterministic. Full jitter is correct in production and useless in a test.
func freezeClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	cur := at
	oldNow, oldJitter := nowFn, jitterFn
	nowFn = func() time.Time { return cur }
	jitterFn = func() float64 { return 1.0 } // no jitter: full backoff duration
	t.Cleanup(func() { nowFn, jitterFn = oldNow, oldJitter })
	return &cur
}

func mustSet(t *testing.T, eps ...Endpoint) *Set {
	t.Helper()
	norm, err := normalise(eps)
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	return NewSet(norm)
}

func names(eps []Endpoint) string {
	out := make([]string, 0, len(eps))
	for _, e := range eps {
		out = append(out, e.Name)
	}
	return strings.Join(out, ",")
}

// clearEnv unsets every variable FromEnv consults, so a developer's own
// environment cannot make these tests pass or fail spuriously.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		EnvEndpointsFile, EnvEndpoints,
		EnvLegacyBaseURL, EnvLegacyName, EnvLegacyToken, EnvAllowInsecure,
	} {
		t.Setenv(k, "")
	}
}

// --- loading ---------------------------------------------------------------

// TestFromEnvUnconfiguredIsEmptyNotError: a box with no relay env vars is the
// EXPECTED posture (public IP, or LAN-only), not a misconfiguration.
func TestFromEnvUnconfiguredIsEmptyNotError(t *testing.T) {
	clearEnv(t)
	set, src, err := FromEnv()
	if err != nil {
		t.Fatalf("unconfigured box must not error: %v", err)
	}
	if src != SourceNone {
		t.Errorf("source = %q, want %q", src, SourceNone)
	}
	if set.Len() != 0 {
		t.Errorf("Len = %d, want 0", set.Len())
	}
	// The zero-ish set must be safe to drive without nil checks.
	if _, ok := set.Primary(); ok {
		t.Error("Primary on an empty set reported ok")
	}
	if got := set.Describe(); !strings.Contains(got, "no relay endpoints") {
		t.Errorf("Describe = %q", got)
	}
}

// TestFromEnvLegacySingleEndpoint: the pre-existing three-variable form still
// produces a working one-endpoint set, so upgrading a box changes nothing.
func TestFromEnvLegacySingleEndpoint(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvLegacyBaseURL, "https://relay.example.com/")
	t.Setenv(EnvLegacyName, "box1")
	t.Setenv(EnvLegacyToken, "s3cret")

	set, src, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if src != SourceLegacy {
		t.Errorf("source = %q, want %q", src, SourceLegacy)
	}
	ep, ok := set.Primary()
	if !ok {
		t.Fatal("Primary reported not-ok")
	}
	if ep.BaseURL != "https://relay.example.com" {
		t.Errorf("trailing slash not normalised: %q", ep.BaseURL)
	}
	if ep.WSURL() != "wss://relay.example.com" {
		t.Errorf("WSURL = %q", ep.WSURL())
	}
	if ep.PublicURL() != "https://box1.relay.example.com" {
		t.Errorf("PublicURL = %q", ep.PublicURL())
	}
	if ep.PathModeURL() != "https://relay.example.com/t/box1/" {
		t.Errorf("PathModeURL = %q", ep.PathModeURL())
	}
}

// TestFromEnvInlineJSONMultiEndpoint is the multi-relay case this package
// exists for.
func TestFromEnvInlineJSONMultiEndpoint(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvEndpoints, `[
	  {"url":"https://relay-a.example.com","name":"box1","token":"ta","region":"eu"},
	  {"url":"https://relay-b.example.com","name":"box1","token":"tb","region":"us"}
	]`)

	set, src, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if src != SourceInline {
		t.Errorf("source = %q, want %q", src, SourceInline)
	}
	if set.Len() != 2 {
		t.Fatalf("Len = %d, want 2", set.Len())
	}
	all := set.All()
	if all[0].BaseURL != "https://relay-a.example.com" || all[1].BaseURL != "https://relay-b.example.com" {
		t.Errorf("configuration order not preserved: %v", all)
	}
	// The same box legitimately holds a DIFFERENT grant on each relay.
	if all[0].Token == all[1].Token {
		t.Error("per-endpoint tokens collapsed into one")
	}
}

// TestFileFormWinsOverInlineAndLegacy: the forms must not merge, or an
// operator can end up with the accidental union of two configurations.
func TestFileFormWinsOverInlineAndLegacy(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "relays.json")
	if err := os.WriteFile(path, []byte(`[{"url":"https://from-file.example.com","name":"box1","token":"tf"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvEndpointsFile, path)
	t.Setenv(EnvEndpoints, `[{"url":"https://from-inline.example.com","name":"box1","token":"ti"}]`)
	t.Setenv(EnvLegacyBaseURL, "https://from-legacy.example.com")
	t.Setenv(EnvLegacyName, "box1")
	t.Setenv(EnvLegacyToken, "tl")

	set, src, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if src != SourceFile {
		t.Errorf("source = %q, want %q", src, SourceFile)
	}
	if set.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (forms must not merge)", set.Len())
	}
	if ep, _ := set.Primary(); ep.BaseURL != "https://from-file.example.com" {
		t.Errorf("BaseURL = %q, want the file's", ep.BaseURL)
	}
}

// TestEndpointsFileMustNotBeWorldAccessible: the file holds bearer tokens, so
// a permissive mode is a startup error rather than a warning.
func TestEndpointsFileMustNotBeWorldAccessible(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "relays.json")
	body := []byte(`[{"url":"https://relay.example.com","name":"box1","token":"t"}]`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvEndpointsFile, path)

	set, _, err := FromEnv()
	if err == nil {
		t.Fatal("world-readable endpoints file was accepted")
	}
	if !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("error should tell the operator how to fix it, got: %v", err)
	}
	if set.Len() != 0 {
		t.Error("a refused file must yield an empty set, not a partial one")
	}

	// 0600 is accepted.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FromEnv(); err != nil {
		t.Fatalf("0600 file rejected: %v", err)
	}
}

// TestUnknownFieldRejected: a typo'd key must fail loudly rather than be
// dropped, leaving an endpoint that mysteriously cannot authenticate.
func TestUnknownFieldRejected(t *testing.T) {
	if _, err := parseJSON([]byte(`[{"url":"https://r.example.com","name":"box1","secret":"oops"}]`)); err == nil {
		t.Fatal("unknown field was silently accepted")
	}
}

// --- validation ------------------------------------------------------------

func TestValidationRejects(t *testing.T) {
	cases := []struct {
		name string
		ep   Endpoint
		want string
	}{
		{"plaintext http", Endpoint{BaseURL: "http://relay.example.com", Name: "box1", Token: "t"}, "plaintext http"},
		{"userinfo in url", Endpoint{BaseURL: "https://u:p@relay.example.com", Name: "box1", Token: "t"}, "embeds credentials"},
		{"no host", Endpoint{BaseURL: "https://", Name: "box1", Token: "t"}, "no host"},
		{"bad scheme", Endpoint{BaseURL: "ftp://relay.example.com", Name: "box1", Token: "t"}, "unsupported scheme"},
		{"query string", Endpoint{BaseURL: "https://relay.example.com?a=b", Name: "box1", Token: "t"}, "query or fragment"},
		{"missing name", Endpoint{BaseURL: "https://relay.example.com", Token: "t"}, "name is required"},
		{"uppercase name", Endpoint{BaseURL: "https://relay.example.com", Name: "Box1", Token: "t"}, "lowercase"},
		{"dotted name", Endpoint{BaseURL: "https://relay.example.com", Name: "box.1", Token: "t"}, "lowercase"},
		{"leading hyphen", Endpoint{BaseURL: "https://relay.example.com", Name: "-box", Token: "t"}, "hyphen"},
		{"missing token", Endpoint{BaseURL: "https://relay.example.com", Name: "box1"}, "token is required"},
		{"control char in token", Endpoint{BaseURL: "https://relay.example.com", Name: "box1", Token: "a\nb"}, "control character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalise([]Endpoint{tc.ep})
			if err == nil {
				t.Fatalf("accepted invalid endpoint %+v", tc.ep.Redact())
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestOneBadEntryRefusesTheWholeSet is the anti-false-redundancy rule: an
// operator who typo'd relay B must not silently get a box running on relay A
// alone while believing it has two.
func TestOneBadEntryRefusesTheWholeSet(t *testing.T) {
	_, err := normalise([]Endpoint{
		{BaseURL: "https://relay-a.example.com", Name: "box1", Token: "ta"},
		{BaseURL: "not-a-url", Name: "box1", Token: "tb"},
	})
	if err == nil {
		t.Fatal("a set with one bad entry was accepted")
	}
	if !strings.Contains(err.Error(), "endpoint 1") {
		t.Errorf("error should name the offending index, got: %v", err)
	}
}

func TestDuplicateEndpointRejected(t *testing.T) {
	_, err := normalise([]Endpoint{
		{BaseURL: "https://relay.example.com", Name: "box1", Token: "ta"},
		{BaseURL: "https://relay.example.com/", Name: "box2", Token: "tb"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("duplicate BaseURL not rejected: %v", err)
	}
}

func TestTooManyEndpointsRejected(t *testing.T) {
	var eps []Endpoint
	for i := 0; i <= MaxEndpoints; i++ {
		eps = append(eps, Endpoint{
			BaseURL: "https://relay" + string(rune('a'+i)) + ".example.com",
			Name:    "box1", Token: "t",
		})
	}
	if _, err := normalise(eps); err == nil {
		t.Fatalf("accepted %d endpoints, max is %d", len(eps), MaxEndpoints)
	}
}

// TestLoopbackHTTPOnlyUnderDevOptOut: the plaintext escape hatch is narrow —
// loopback only, and only with the env opt-in.
func TestLoopbackHTTPOnlyUnderDevOptOut(t *testing.T) {
	clearEnv(t)
	lo := Endpoint{BaseURL: "http://127.0.0.1:8443", Name: "box1", Token: "t"}
	if _, err := normalise([]Endpoint{lo}); err == nil {
		t.Fatal("loopback http accepted without the opt-in")
	}

	t.Setenv(EnvAllowInsecure, "1")
	if _, err := normalise([]Endpoint{lo}); err != nil {
		t.Fatalf("loopback http refused with the opt-in: %v", err)
	}
	// Even with the opt-in, a NON-loopback plaintext target stays refused.
	pub := Endpoint{BaseURL: "http://relay.example.com", Name: "box1", Token: "t"}
	if _, err := normalise([]Endpoint{pub}); err == nil {
		t.Fatal("public http accepted under the loopback-only opt-in")
	}
}

// --- ordering, health, failover -------------------------------------------

// TestOrderedPrefersHealthyButNeverDrops is the core failover contract.
func TestOrderedPrefersHealthyButNeverDrops(t *testing.T) {
	clock := freezeClock(t, time.Unix(1_700_000_000, 0))
	set := mustSet(t,
		Endpoint{BaseURL: "https://a.example.com", Name: "a", Token: "t"},
		Endpoint{BaseURL: "https://b.example.com", Name: "b", Token: "t"},
		Endpoint{BaseURL: "https://c.example.com", Name: "c", Token: "t"},
	)

	if got := names(set.Ordered()); got != "a,b,c" {
		t.Fatalf("initial order = %q, want a,b,c", got)
	}

	set.MarkDown("https://a.example.com", errors.New("dial timeout"))
	if got := names(set.Ordered()); got != "b,c,a" {
		t.Errorf("after a fails, order = %q, want b,c,a", got)
	}
	// Crucially: still THREE. A failing endpoint is deprioritised, not dropped.
	if len(set.Ordered()) != 3 {
		t.Error("a failing endpoint was dropped from the set")
	}

	// Everything down -> still three, ordered by who recovers soonest.
	set.MarkDown("https://b.example.com", errors.New("500"))
	set.MarkDown("https://c.example.com", errors.New("500"))
	if len(set.Ordered()) != 3 {
		t.Fatal("an all-failing set went dark instead of continuing to retry")
	}

	// Backoff expiry restores natural order.
	*clock = clock.Add(2 * maxBackoff)
	if got := names(set.Ordered()); got != "a,b,c" {
		t.Errorf("after backoff expiry, order = %q, want a,b,c", got)
	}
}

// TestMarkUpClearsBackoffImmediately: a relay that answers is healthy again
// at once — it should not serve out the rest of a backoff it has disproven.
func TestMarkUpClearsBackoffImmediately(t *testing.T) {
	freezeClock(t, time.Unix(1_700_000_000, 0))
	set := mustSet(t,
		Endpoint{BaseURL: "https://a.example.com", Name: "a", Token: "t"},
		Endpoint{BaseURL: "https://b.example.com", Name: "b", Token: "t"},
	)
	set.MarkDown("https://a.example.com", errors.New("blip"))
	if names(set.Ordered()) != "b,a" {
		t.Fatal("setup: a should be deprioritised")
	}
	set.MarkUp("https://a.example.com")
	if got := names(set.Ordered()); got != "a,b" {
		t.Errorf("after MarkUp, order = %q, want a,b", got)
	}
	for _, st := range set.Statuses() {
		if st.Endpoint.Name == "a" && st.Failures != 0 {
			t.Errorf("MarkUp did not reset the failure count: %d", st.Failures)
		}
	}
}

// TestBackoffGrowsAndCaps guards the retry schedule against both a hot loop
// and an endpoint that backs off into effective permanence.
func TestBackoffGrowsAndCaps(t *testing.T) {
	oldJitter := jitterFn
	jitterFn = func() float64 { return 1.0 }
	t.Cleanup(func() { jitterFn = oldJitter })

	prev := time.Duration(0)
	for f := 1; f <= 20; f++ {
		d := backoffFor(f)
		if d < time.Second {
			t.Fatalf("backoff(%d) = %v, below the 1s floor (hot-loop risk)", f, d)
		}
		if d > maxBackoff {
			t.Fatalf("backoff(%d) = %v, above the %v cap", f, d, maxBackoff)
		}
		if f > 1 && d < prev {
			t.Fatalf("backoff decreased: backoff(%d)=%v < backoff(%d)=%v", f, d, f-1, prev)
		}
		prev = d
	}
	if prev != maxBackoff {
		t.Errorf("backoff never reached the cap: %v != %v", prev, maxBackoff)
	}
}

// TestJitterKeepsBackoffInBounds: full jitter must never produce a value
// outside (0, cap] — a negative or oversized sleep is a real outage.
func TestJitterKeepsBackoffInBounds(t *testing.T) {
	oldJitter := jitterFn
	t.Cleanup(func() { jitterFn = oldJitter })

	for _, j := range []float64{0.0, 0.0001, 0.5, 0.999999} {
		jitterFn = func() float64 { return j }
		for f := 1; f <= 12; f++ {
			d := backoffFor(f)
			if d < time.Second || d > maxBackoff {
				t.Fatalf("jitter %v backoff(%d) = %v, outside [1s, %v]", j, f, d, maxBackoff)
			}
		}
	}
}

// TestPriorityOverridesConfigurationOrder.
func TestPriorityOverridesConfigurationOrder(t *testing.T) {
	freezeClock(t, time.Unix(1_700_000_000, 0))
	set := mustSet(t,
		Endpoint{BaseURL: "https://a.example.com", Name: "a", Token: "t", Priority: 10},
		Endpoint{BaseURL: "https://b.example.com", Name: "b", Token: "t", Priority: 1},
	)
	if got := names(set.Ordered()); got != "b,a" {
		t.Errorf("order = %q, want b,a (priority wins)", got)
	}
}

// TestAllIgnoresHealth: the fan-out view must include endpoints in backoff,
// because equivocation detection needs every source and because a relay that
// cannot hold a tunnel may still answer a read.
func TestAllIgnoresHealth(t *testing.T) {
	freezeClock(t, time.Unix(1_700_000_000, 0))
	set := mustSet(t,
		Endpoint{BaseURL: "https://a.example.com", Name: "a", Token: "t"},
		Endpoint{BaseURL: "https://b.example.com", Name: "b", Token: "t"},
	)
	set.MarkDown("https://a.example.com", errors.New("down"))
	if got := names(set.All()); got != "a,b" {
		t.Errorf("All = %q, want a,b regardless of health", got)
	}
}

// --- secret hygiene --------------------------------------------------------

// TestTokensNeverEscapeThroughDisplaySurfaces is the redaction contract. A
// token reaching a log line or a status route is a credential disclosure, so
// every user-facing accessor is checked in one place.
func TestTokensNeverEscapeThroughDisplaySurfaces(t *testing.T) {
	const secret = "super-secret-grant-value"
	freezeClock(t, time.Unix(1_700_000_000, 0))
	set := mustSet(t,
		Endpoint{BaseURL: "https://a.example.com", Name: "a", Token: secret, Region: "eu"},
	)
	set.MarkDown("https://a.example.com", errors.New("nope"))

	ep, _ := set.Primary()
	surfaces := map[string]string{
		"Endpoint.String":    ep.String(),
		"Endpoint.Redact":    ep.Redact().Token,
		"Endpoint.WSURL":     ep.WSURL(),
		"Endpoint.PublicURL": ep.PublicURL(),
		"Set.Describe":       set.Describe(),
	}
	for name, got := range surfaces {
		if strings.Contains(got, secret) {
			t.Errorf("%s leaked the token: %q", name, got)
		}
	}
	for _, st := range set.Statuses() {
		if st.Endpoint.Token != "" {
			t.Error("Statuses leaked the token")
		}
	}
}

// TestConcurrentHealthUpdates exercises the mutex under -race: health is
// written by every consumer goroutine (tunnel agent, resolver, fan-out).
func TestConcurrentHealthUpdates(t *testing.T) {
	set := mustSet(t,
		Endpoint{BaseURL: "https://a.example.com", Name: "a", Token: "t"},
		Endpoint{BaseURL: "https://b.example.com", Name: "b", Token: "t"},
	)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				if (i+j)%2 == 0 {
					set.MarkDown("https://a.example.com", errors.New("x"))
				} else {
					set.MarkUp("https://a.example.com")
				}
				_ = set.Ordered()
				_ = set.Statuses()
				_ = set.Describe()
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
