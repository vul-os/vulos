package appnet

import "testing"

// ORIGIN-01. These tests pin the rules that decide whether an app gets its own
// browser origin — and, critically, that we say NO whenever we cannot actually
// serve one. Every "false" below is a case where the shell must fall back to an
// opaque-origin frame rather than hand the app the shell's origin.

func TestEnabled_FailsClosedWithoutAServableDomain(t *testing.T) {
	cases := []struct {
		base string
		want bool
		why  string
	}{
		{"", false, "the default self-host box: no VULOS_DOMAIN, so there is no second name to serve an app from"},
		{"localhost", false, "single label; the host dispatcher refuses to route *.localhost to the gateway"},
		{"192.168.1.10", false, "an IP address has no subdomains"},
		{"::1", false, "an IPv6 literal has no subdomains"},
		{"box", false, "single-label name: no cookie scope, no wildcard cert story"},
		{"box.example.com", true, "a real base domain"},
		{"BOX.Example.COM", true, "case-insensitive"},
		{".box.example.com", true, "a leading dot is tolerated and normalised"},
		{"apps.example.com", true, "a multi-label base is fine — ParseSubdomain only reads the first label"},
	}
	for _, c := range cases {
		if got := Enabled(c.base); got != c.want {
			t.Errorf("Enabled(%q) = %v, want %v — %s", c.base, got, c.want, c.why)
		}
	}
}

func TestAppLabel_RejectsAmbiguousAndHostileIDs(t *testing.T) {
	// An app id is attacker-influenced the moment third-party apps exist. A label
	// we would not mint ourselves must never be accepted, or an app could claim
	// another app's origin.
	bad := []struct{ id, profile, why string }{
		{"", "default", "empty id"},
		{"a.b", "default", "a dot would smuggle an extra DNS label and change the origin"},
		{"-lead", "default", "leading hyphen is not a legal DNS label"},
		{"trail-", "default", "trailing hyphen is not a legal DNS label"},
		{"UPPER", "default", "ParseSubdomain lowercases hosts; an uppercase id would not round-trip"},
		{"a--b", "default", "\"--\" is the app/profile separator: id \"a--b\" is indistinguishable from app \"a\" profile \"b\""},
		{"ok", "a--b", "same ambiguity on the profile side"},
		{"under_score", "default", "not a legal DNS label"},
	}
	for _, c := range bad {
		if got, ok := AppLabel(c.id, c.profile); ok {
			t.Errorf("AppLabel(%q, %q) = %q, true — want rejected: %s", c.id, c.profile, got, c.why)
		}
	}

	if got, ok := AppLabel("clock", ""); !ok || got != "clock--default" {
		t.Errorf("AppLabel(clock, \"\") = %q, %v — want clock--default, true", got, ok)
	}
	if got, ok := AppLabel("clock", "work"); !ok || got != "clock--work" {
		t.Errorf("AppLabel(clock, work) = %q, %v — want clock--work, true", got, ok)
	}
}

func TestAppLabelRoundTripsThroughParseSubdomain(t *testing.T) {
	// The origin we MINT must be the origin we PARSE back to the same app. If these
	// two ever disagree, an app could be served on a host that maps to a different
	// app id — origin confusion.
	const base = "box.example.com"
	for _, id := range []string{"clock", "pdf-viewer", "text-editor", "a", "a1-b2"} {
		for _, profile := range []string{"default", "work"} {
			host, ok := AppHost(id, profile, base)
			if !ok {
				t.Fatalf("AppHost(%q, %q) refused", id, profile)
			}
			gotID, gotProfile, ok := ParseSubdomain(host, base)
			if !ok || gotID != id || gotProfile != profile {
				t.Errorf("round-trip broke: minted %q → parsed (%q, %q, %v), want (%q, %q, true)",
					host, gotID, gotProfile, ok, id, profile)
			}
		}
	}
}

func TestAppOrigin_SerialisesLikeABrowser(t *testing.T) {
	// The origin string is compared against event.origin in the browser, so it must
	// match the browser's serialisation exactly — default ports omitted, or every
	// postMessage origin check fails and the bridge silently dies.
	cases := []struct {
		scheme, port, want string
	}{
		{"https", "443", "https://clock--default.box.example.com"},
		{"https", "", "https://clock--default.box.example.com"},
		{"http", "80", "http://clock--default.box.example.com"},
		{"https", "8443", "https://clock--default.box.example.com:8443"},
		{"http", "8080", "http://clock--default.box.example.com:8080"},
	}
	for _, c := range cases {
		got, ok := AppOrigin(c.scheme, "clock", "default", "box.example.com", c.port)
		if !ok || got != c.want {
			t.Errorf("AppOrigin(%s, port=%q) = %q, %v — want %q", c.scheme, c.port, got, ok, c.want)
		}
	}

	if _, ok := AppOrigin("https", "clock", "default", "", "443"); ok {
		t.Error("AppOrigin must refuse when per-app origins are unavailable (empty base domain)")
	}
}

func TestShellOrigin(t *testing.T) {
	got, ok := ShellOrigin("https", "box.example.com", "")
	if !ok || got != "https://box.example.com" {
		t.Errorf("ShellOrigin = %q, %v — want https://box.example.com", got, ok)
	}
	if _, ok := ShellOrigin("https", "localhost", ""); ok {
		t.Error("ShellOrigin must refuse when per-app origins are unavailable")
	}
}

func TestIsAppHost(t *testing.T) {
	const base = "box.example.com"
	cases := []struct {
		host            string
		wantApp, wantPr string
		wantOK          bool
	}{
		{"clock--default.box.example.com", "clock", "default", true},
		{"clock--default.box.example.com:8080", "clock", "default", true},
		{"clock.box.example.com", "clock", "default", true},
		{"clock--work.box.example.com", "clock", "work", true},
		{"box.example.com", "", "", false},          // the SHELL's own origin is not an app host
		{"evil.com", "", "", false},                 // unrelated host
		{"box.example.com.evil.com", "", "", false}, // suffix-confusion attempt
		{"a--b--c.box.example.com", "", "", false},  // ambiguous label
	}
	for _, c := range cases {
		app, pr, ok := IsAppHost(c.host, base)
		if ok != c.wantOK || app != c.wantApp || pr != c.wantPr {
			t.Errorf("IsAppHost(%q) = (%q, %q, %v) — want (%q, %q, %v)",
				c.host, app, pr, ok, c.wantApp, c.wantPr, c.wantOK)
		}
	}

	// With origins off, nothing is an app host.
	if _, _, ok := IsAppHost("clock--default.box.example.com", ""); ok {
		t.Error("IsAppHost must return false when per-app origins are disabled")
	}
}
