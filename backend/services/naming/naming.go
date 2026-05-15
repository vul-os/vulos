// Package naming provides the shared identifier validation rules for Vulos.
//
// The "--" double-hyphen is reserved as the subdomain separator in the Vulos
// networking layer ({app}--{profile}.{instance}.vulos), so it must be
// forbidden in every identifier that feeds into DNS labels: app IDs,
// usernames, and browser-profile names.
//
// Allowed pattern: ^[a-z0-9][a-z0-9-]*[a-z0-9]$ with no consecutive hyphens.
//   - Starts and ends with lowercase alphanumeric.
//   - May contain single hyphens in the middle.
//   - No consecutive hyphens (-- forbidden).
//   - Minimum length 2 (one char start + one char end).
//   - Maximum length is caller-enforced.
package naming

import (
	"fmt"
	"regexp"
	"strings"
)

// identPattern enforces the character set and anchors (no leading/trailing
// hyphen). Consecutive hyphens are checked separately via strings.Contains.
var identPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// ValidateIdent checks that name satisfies the Vulos naming rules and fits
// within [minLen, maxLen] inclusive.
//
// Rules:
//   - Only lowercase ASCII letters, digits, and hyphens.
//   - Must start and end with a letter or digit (no leading/trailing hyphens).
//   - No consecutive hyphens ("--" is forbidden — it is the subdomain separator).
//   - Length must be within [minLen, maxLen].
func ValidateIdent(name, kind string, minLen, maxLen int) error {
	n := len(name)
	if n < minLen || n > maxLen {
		return fmt.Errorf("%s %q: length must be between %d and %d characters", kind, name, minLen, maxLen)
	}
	// Single-character names: identPattern requires ≥2 chars; handle separately.
	if n == 1 {
		c := name[0]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			return nil
		}
		return fmt.Errorf("%s %q: must be lowercase alphanumeric with optional single hyphens, no leading/trailing or consecutive hyphens", kind, name)
	}
	if !identPattern.MatchString(name) {
		return fmt.Errorf("%s %q: must be lowercase alphanumeric with optional single hyphens, no leading/trailing or consecutive hyphens", kind, name)
	}
	// Explicitly forbid consecutive hyphens (-- is the Vulos subdomain separator).
	if strings.Contains(name, "--") {
		return fmt.Errorf("%s %q: consecutive hyphens (\"--\") are not allowed", kind, name)
	}
	return nil
}
