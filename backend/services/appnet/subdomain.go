package appnet

import "strings"

// ParseSubdomain parses a Host header value against a known base domain and
// returns the resolved appID, profile, and whether the host matched.
//
// Subdomain conventions (NET-01 / NET-02):
//
//	{profile}--{appId}.{baseDomain}  →  app=appId,  profile=profile
//	{appId}.{baseDomain}             →  app=appId,  profile="default"
//
// The double-dash separator (--) was chosen because app IDs are restricted to
// lowercase alphanumeric + single hyphens, so "--" can never appear in a bare
// appID or profile name.
//
// Returns ok=false when host does not end with "."+baseDomain.
func ParseSubdomain(host, baseDomain string) (app, profile string, ok bool) {
	// Strip port if present
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}

	suffix := "." + baseDomain
	if !strings.HasSuffix(host, suffix) {
		return "", "", false
	}

	sub := strings.TrimSuffix(host, suffix)
	if sub == "" {
		return "", "", false
	}

	// Check for profile prefix: profile--appId
	if idx := strings.Index(sub, "--"); idx > 0 {
		profile = sub[:idx]
		app = sub[idx+2:]
		if profile == "" || app == "" {
			// Malformed — treat whole thing as appID with default profile
			return sub, "default", true
		}
		return app, profile, true
	}

	return sub, "default", true
}
