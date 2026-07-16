package oauthprovider

import (
	"os"
	"strings"
)

// Scope identifiers issued by the Vulos OAuth2/OIDC provider.
//
// openid + email are the standard OIDC scopes we honor; the mail.* scopes are
// Vulos-product scopes that the Vulos Mail product (and approved third parties)
// request to read/send mail on a user's behalf.
//
// NOTE: the OIDC "profile" scope is intentionally NOT offered. The provider
// never stores users itself (auth.Store exposes only email + email_verified via
// ProfileByID), so there are no profile claims (name/preferred_username/...) to
// honor. Advertising "profile" while issuing nothing for it is misleading, so it
// is omitted from the discovery document, dev console, and consent UI.
const (
	ScopeOpenID   = "openid"    // OIDC sign-in; required to receive an id_token
	ScopeEmail    = "email"     // email + email_verified claims
	ScopeMailRead = "mail.read" // Vulos Mail: read messages
	ScopeMailSend = "mail.send" // Vulos Mail: send messages
)

// knownScopes is the closed set of scopes this provider understands. A request
// for any scope outside this set is rejected (invalid_scope).
var knownScopes = map[string]bool{
	ScopeOpenID:   true,
	ScopeEmail:    true,
	ScopeMailRead: true,
	ScopeMailSend: true,
}

// AllScopes returns every scope this provider KNOWS (for internal validation and
// back-compat), in a stable order. Some of these may not currently be GRANTABLE —
// see GrantableScopes for the set an app may actually request today.
func AllScopes() []string {
	return []string{ScopeOpenID, ScopeEmail, ScopeMailRead, ScopeMailSend}
}

// IsKnownScope reports whether s is a scope this provider issues.
func IsKnownScope(s string) bool { return knownScopes[s] }

// ── Grantability gate (LAUNCH: hosted mail dormant) ──────────────────────────
//
// The mail.read / mail.send scopes address the VULOS-HOSTED mailbox. That
// product is DORMANT at launch, so those scopes resolve to nothing: an app that
// obtained them could read/send no mail. Advertising or granting a scope that
// resolves to nothing is misleading (and a latent authz footgun), so while
// hosted mail is dormant the mail.* scopes stay REGISTERED (known — no schema or
// client-config change) but are UN-GRANTABLE:
//
//   - the dev console will not register a client for them,
//   - the /oauth/authorize endpoint rejects a request that includes them,
//   - the discovery document advertises only the grantable set.
//
// Re-enabling hosted mail is then a single flag flip — VULOS_HOSTED_MAIL=1 — with
// no migration and no client re-registration: any client whose stored scope list
// already contains mail.* immediately becomes able to request it.
//
// openid + email are ALWAYS grantable (core identity).

// mailHostedEnabled reports whether the Vulos-hosted mailbox product is live.
// Read at call time (never cached) so the gate flips the instant the operator
// sets the env — and so tests can toggle it with t.Setenv. Accepts 1/true/on
// (case-insensitive) as truthy; everything else (including unset) is dormant.
func mailHostedEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VULOS_HOSTED_MAIL"))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// IsGrantableScope reports whether s is a scope that may currently be granted
// (registered for, requested at authorize, consented to). A scope is grantable
// iff it is KNOWN and not gated off: the mail.* scopes are gated off while hosted
// mail is dormant.
func IsGrantableScope(s string) bool {
	if !knownScopes[s] {
		return false
	}
	if (s == ScopeMailRead || s == ScopeMailSend) && !mailHostedEnabled() {
		return false
	}
	return true
}

// GrantableScopes returns the currently grantable scopes in stable order (for the
// discovery document and the dev-console/consent UI, so a dormant mail product
// never offers mail.*).
func GrantableScopes() []string {
	out := make([]string, 0, len(knownScopes))
	for _, s := range AllScopes() {
		if IsGrantableScope(s) {
			out = append(out, s)
		}
	}
	return out
}

// FilterGrantable drops any scope that is not currently grantable, returning the
// survivors and whether any KNOWN-but-ungrantable scope was dropped (i.e. a
// gated-off scope such as mail.* while hosted mail is dormant). Unknown scopes are
// dropped WITHOUT setting hadUngrantable — use FilterKnown to detect those — so a
// caller can distinguish "unknown scope" (invalid_scope, client error) from
// "temporarily unavailable scope" (invalid_scope, product dormant).
func FilterGrantable(scopes []string) (kept []string, hadUngrantable bool) {
	for _, s := range scopes {
		switch {
		case IsGrantableScope(s):
			kept = append(kept, s)
		case knownScopes[s]:
			// known but gated off right now
			hadUngrantable = true
		}
	}
	return kept, hadUngrantable
}

// ParseScopes splits a space-separated scope string into a de-duplicated,
// order-preserving slice. Empty/whitespace entries are dropped.
func ParseScopes(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.Fields(s) {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// JoinScopes renders scopes back into a canonical space-separated string.
func JoinScopes(scopes []string) string { return strings.Join(scopes, " ") }

// FilterKnown drops any scope not in knownScopes, returning the surviving
// scopes and whether any were dropped (an unknown scope was requested).
func FilterKnown(scopes []string) (kept []string, hadUnknown bool) {
	for _, s := range scopes {
		if knownScopes[s] {
			kept = append(kept, s)
		} else {
			hadUnknown = true
		}
	}
	return kept, hadUnknown
}

// SubsetOf reports whether every scope in req is present in allow. Used to
// confirm a client only requests scopes it was registered for.
func SubsetOf(req, allow []string) bool {
	allowed := map[string]bool{}
	for _, a := range allow {
		allowed[a] = true
	}
	for _, s := range req {
		if !allowed[s] {
			return false
		}
	}
	return true
}

// HasScope reports whether the space-separated grant string contains scope s.
func HasScope(grant, s string) bool {
	for _, f := range strings.Fields(grant) {
		if f == s {
			return true
		}
	}
	return false
}
