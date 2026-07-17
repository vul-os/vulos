// oauth_openredirect_test.go — M4 regression: the OAuth ?return= validator
// rejects backslash-smuggled and protocol-relative open redirects.
package cproutes

import "testing"

func TestSafeNextPath_RejectsOpenRedirects(t *testing.T) {
	bad := []string{
		`/\evil.com`,       // browsers normalise \ → / ⇒ //evil.com
		`/\/evil.com`,      // \/ ⇒ //
		`/\\evil.com`,      // double backslash
		`//evil.com`,       // protocol-relative
		`https://evil.com`, // absolute cross-origin
		`javascript:alert(1)`,
		`/path\then`, // backslash anywhere
		``,
	}
	for _, in := range bad {
		if got := safeNextPath(in); got != "" {
			t.Errorf("safeNextPath(%q) = %q, want \"\" (rejected)", in, got)
		}
	}

	good := []string{"/", "/console", "/onboarding/link-account", "/products/office?tab=1"}
	for _, in := range good {
		if got := safeNextPath(in); got != in {
			t.Errorf("safeNextPath(%q) = %q, want it preserved", in, got)
		}
	}
}
