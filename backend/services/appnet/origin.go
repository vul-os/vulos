package appnet

// origin.go — ORIGIN-01: per-app browser origins.
//
// Background. Until now every app was served from the OS shell's own origin at
// /app/{id}/. That made `allow-same-origin` on the shell's app iframes a full
// shell compromise: an app granted it could reach window.top, the shell's
// localStorage/cookies, and the gateway-injected auth headers. The shell worked
// around this by handing `allow-same-origin` to a hand-picked allowlist of
// first-party apps (SANDBOX-02) — tolerable while every app shipped with the
// OS, fatal the moment a stranger can install one.
//
// The fix is to give each app its OWN origin, so that "same-origin" for an app
// means the app itself and never the shell. The scheme reuses the subdomain
// routing that already exists (NET-01/NET-02, see subdomain.go):
//
//	{app}--{profile}.{baseDomain}       e.g. clock--default.box.example.com
//
// The shell keeps the base domain itself (box.example.com). Because the browser
// treats those as different origins, localStorage / DOM / IndexedDB access from
// an app frame is blocked by the browser, not by our sandbox bookkeeping.
//
// AVAILABILITY. Per-app origins need (a) a base domain and (b) DNS + TLS that
// cover its wildcard. A default self-hosted box has neither: VULOS_DOMAIN is
// empty and the server falls back to plain HTTP on :8080 (see cmd/server/main.go),
// so there is no second name to serve an app from. Enabled() therefore reports
// whether the deployment can actually do this, and every caller must FAIL SAFE
// when it cannot: the app stays on the path-prefix URL and the shell must NOT
// grant it `allow-same-origin` (it runs in an opaque origin instead). We never
// grant a capability on the strength of configuration we cannot verify.
//
// The shell does not trust this package's answer on its own — it derives the
// sandbox from the frame URL's actual origin (see src/core/AppOrigins.js), so a
// misconfiguration degrades to "no allow-same-origin" rather than to "app runs
// on the shell's origin with allow-same-origin".

import (
	"net"
	"os"
	"strings"
)

// DefaultProfile is the profile used when an app URL carries no {profile}
// segment. It matches ParseSubdomain's default (see subdomain.go).
const DefaultProfile = "default"

// BaseDomain returns the configured per-instance base domain (VULOS_DOMAIN),
// normalised to lowercase with any leading dot and surrounding space removed.
// Empty when unset.
func BaseDomain() string {
	return normaliseDomain(os.Getenv("VULOS_DOMAIN"))
}

func normaliseDomain(d string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), ".")
}

// Enabled reports whether per-app origins can actually be served for baseDomain.
//
// False when:
//   - baseDomain is empty (the default self-host box — no name to serve from);
//   - baseDomain is "localhost" or a bare IP address, neither of which has
//     usable subdomains for our purposes. ("localhost" is excluded to match the
//     host dispatcher in cmd/server/main.go, which refuses to route *.localhost
//     to the gateway; an IP has no subdomains at all.)
//
// When this returns false the caller MUST fall back to the path-prefix URL and
// the shell MUST withhold `allow-same-origin`. See the file header.
func Enabled(baseDomain string) bool {
	b := normaliseDomain(baseDomain)
	if b == "" || b == "localhost" {
		return false
	}
	if net.ParseIP(b) != nil {
		return false
	}
	// A base domain must have at least one dot — a single-label name has no
	// cookie scope and no wildcard cert story.
	return strings.Contains(b, ".")
}

// AppLabel returns the leading DNS label an app is served from:
// "{app}--{profile}", or just "{app}" for the default profile (which keeps the
// URLs the shell already mints — see src/shell/launchApp.js — parseable by
// ParseSubdomain either way).
//
// Returns ("", false) when appID or profile is not a valid DNS label component,
// which is the same validation ParseSubdomain applies on the way back in. This
// is what stops an attacker-chosen app id from smuggling extra labels (a "." in
// the id) and hijacking another origin.
func AppLabel(appID, profile string) (string, bool) {
	// Deliberately NOT lowercased. An app id is an identity, not a DNS input: if we
	// normalised case here, "Clock" and "clock" would map to the SAME origin, and
	// two different apps could end up sharing one origin's storage. net01ValidIdent
	// rejects anything that is not already lowercase, so a non-canonical id is
	// refused outright rather than quietly folded onto someone else's origin.
	appID = strings.TrimSpace(appID)
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = DefaultProfile
	}
	if !net01ValidIdent(appID) || !net01ValidIdent(profile) {
		return "", false
	}
	// An app id containing "--" would make the label ambiguous to ParseSubdomain
	// (it splits on the first "--"), so an id like "a--b" could be addressed as
	// app "a" profile "b". Refuse it outright rather than serve an ambiguous origin.
	if strings.Contains(appID, "--") || strings.Contains(profile, "--") {
		return "", false
	}
	return appID + "--" + profile, true
}

// AppHost returns the full hostname an app is served from, e.g.
// "clock--default.box.example.com". Returns ("", false) when per-app origins
// are not available or the ids are invalid.
func AppHost(appID, profile, baseDomain string) (string, bool) {
	if !Enabled(baseDomain) {
		return "", false
	}
	label, ok := AppLabel(appID, profile)
	if !ok {
		return "", false
	}
	return label + "." + normaliseDomain(baseDomain), true
}

// AppOrigin returns the browser origin for an app, e.g.
// "https://clock--default.box.example.com" (port included only when non-default
// for the scheme, matching how a browser serialises an origin).
func AppOrigin(scheme, appID, profile, baseDomain, port string) (string, bool) {
	host, ok := AppHost(appID, profile, baseDomain)
	if !ok {
		return "", false
	}
	return scheme + "://" + joinHostPort(host, scheme, port), true
}

// ShellOrigin returns the origin the OS shell itself is served from — the base
// domain, with no app label. This is the value app frames must use as their
// postMessage targetOrigin, and the only origin allowed to frame an app.
func ShellOrigin(scheme, baseDomain, port string) (string, bool) {
	if !Enabled(baseDomain) {
		return "", false
	}
	return scheme + "://" + joinHostPort(normaliseDomain(baseDomain), scheme, port), true
}

// joinHostPort appends :port unless it is the default port for the scheme (a
// browser omits the default port when serialising an origin, and an origin
// string that disagrees with the browser's would fail every comparison).
func joinHostPort(host, scheme, port string) string {
	port = strings.TrimPrefix(strings.TrimSpace(port), ":")
	if port == "" ||
		(scheme == "https" && port == "443") ||
		(scheme == "http" && port == "80") {
		return host
	}
	return host + ":" + port
}

// SplitHostPort splits a request Host header into host and port ("" when the
// Host carries no port). IPv6 literals are returned with their brackets intact.
func SplitHostPort(hostHeader string) (host, port string) {
	hostHeader = strings.TrimSpace(hostHeader)
	if h, p, err := net.SplitHostPort(hostHeader); err == nil {
		return h, p
	}
	return hostHeader, ""
}

// IsAppHost reports whether a request Host is an app origin (i.e. a subdomain of
// baseDomain that parses as an app label) and returns the app id and profile it
// addresses. It is the authoritative answer to "which app is this origin for?",
// and the gateway uses it to refuse serving app A's content on app B's origin.
func IsAppHost(hostHeader, baseDomain string) (appID, profile string, ok bool) {
	if !Enabled(baseDomain) {
		return "", "", false
	}
	host, _ := SplitHostPort(hostHeader)
	host = strings.ToLower(host)
	base := normaliseDomain(baseDomain)
	if !strings.HasSuffix(host, "."+base) {
		return "", "", false
	}
	return ParseSubdomain(host, base)
}
