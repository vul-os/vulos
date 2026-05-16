package appnet

import (
	"strings"
	"unicode"
)

// ParseSubdomain parses a request host against baseDomain and returns the app
// identifier and profile encoded in the first DNS label.
//
// Expected host format:
//
//	{app}--{profile}.{ulid}.{baseDomain}   → app, profile, true
//	{app}.{ulid}.{baseDomain}              → app, "default", true
//	anything not ending in "."+baseDomain  → "", "", false
//
// The separator "--" is chosen because it cannot appear in a single DNS label
// by convention, so it unambiguously splits app from profile even when both
// names contain single hyphens.
//
// Rules:
//   - Port is stripped from host before matching.
//   - Host matching is case-insensitive (lowercased before comparison).
//   - Only the first DNS label (before the first ".") is parsed; the rest
//     (ulid, baseDomain) are not inspected.
//   - Exactly one "--" occurrence is required when a profile is specified;
//     more than one occurrence returns (_,_,false).
//   - Both app and profile are validated: lowercase alphanumeric and hyphens
//     only, no leading/trailing hyphens.
func ParseSubdomain(host, baseDomain string) (app, profile string, ok bool) {
	// Strip port
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		// Only strip if it looks like a port (no colon before it that isn't brackets)
		// For IPv6 the host would be [::1]:port — we don't handle that edge case here
		// because app subdomains are always simple hostnames.
		host = host[:idx]
	}

	// Normalise to lowercase
	host = strings.ToLower(host)
	baseDomain = strings.ToLower(baseDomain)

	suffix := "." + baseDomain
	if !strings.HasSuffix(host, suffix) {
		return "", "", false
	}

	// Everything before the baseDomain suffix
	remainder := strings.TrimSuffix(host, suffix)
	if remainder == "" {
		return "", "", false
	}

	// The first label is the part before the first "."
	// e.g. remainder = "myapp--dev.01JABCDEF" → label = "myapp--dev"
	label := remainder
	if dotIdx := strings.Index(remainder, "."); dotIdx >= 0 {
		label = remainder[:dotIdx]
	}

	if label == "" {
		return "", "", false
	}

	// Count "--" occurrences in the label
	count := strings.Count(label, "--")
	switch {
	case count == 0:
		// No profile segment → default profile
		app = label
		profile = "default"
	case count == 1:
		parts := strings.SplitN(label, "--", 2)
		app = parts[0]
		profile = parts[1]
	default:
		// More than one "--" → ambiguous; reject
		return "", "", false
	}

	if !net01ValidIdent(app) || !net01ValidIdent(profile) {
		return "", "", false
	}

	return app, profile, true
}

// net01ValidIdent returns true when s is a non-empty string consisting only of
// lowercase ASCII letters, digits, and hyphens, with no leading or trailing
// hyphens. This mirrors common DNS label / Kubernetes name restrictions.
//
// A naming.ValidateIdent helper does not exist in this codebase at the time of
// NET-01, so the check is inlined here with the net01_ prefix to avoid future
// collisions.
func net01ValidIdent(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, r := range s {
		if !unicode.IsLower(r) && !unicode.IsDigit(r) && r != '-' {
			return false
		}
	}
	return true
}
