package oauthfosite

import (
	"github.com/ory/fosite"

	"github.com/vul-os/vulos-management/pkg/oauthprovider"
)

// clientAdapter presents an audited oauthprovider.Client to fosite as a
// fosite.Client. It is a read-only view: the oauth_clients table (with its
// exact-match redirect allow-list and hashed secret) remains the single source
// of truth. Redirect matching itself is performed by fosite, which compares the
// requested redirect_uri EXACTLY against GetRedirectURIs — preserving the
// hand-rolled provider's exact-redirect invariant.
type clientAdapter struct {
	c oauthprovider.Client
}

// newClientAdapter wraps a stored client for fosite.
func newClientAdapter(c oauthprovider.Client) *clientAdapter { return &clientAdapter{c: c} }

func (a *clientAdapter) GetID() string { return a.c.ClientID }

// GetHashedSecret returns the stored secret hash. NOTE: the hand-rolled provider
// stores hex(sha256(secret)); fosite's default hasher is BCrypt. Confidential-
// client secret verification therefore needs a compatible hasher wired at
// cutover time (out of STEP-1 scope). Public clients (the "Sign in with Vulos"
// case) carry no secret and authenticate via PKCE, so this is unused for them.
func (a *clientAdapter) GetHashedSecret() []byte { return []byte(a.c.SecretHash) }

// GetRedirectURIs returns the exact-match allow-list. fosite matches the
// requested redirect_uri exactly against this list.
func (a *clientAdapter) GetRedirectURIs() []string { return a.c.RedirectURIs }

// GetGrantTypes returns the grants this foundation supports for the sign-in
// flow: authorization_code + refresh_token.
func (a *clientAdapter) GetGrantTypes() fosite.Arguments {
	return fosite.Arguments{"authorization_code", "refresh_token"}
}

// GetResponseTypes returns "code" — this foundation implements the
// authorization-code flow only (no implicit/hybrid).
func (a *clientAdapter) GetResponseTypes() fosite.Arguments {
	return fosite.Arguments{"code"}
}

// GetScopes returns the scopes the client may request. "openid" and "offline"
// are always permitted so id_tokens and refresh tokens can be issued; the rest
// come from the client's stored allow-list.
func (a *clientAdapter) GetScopes() fosite.Arguments {
	scopes := append([]string{"openid", "offline"}, a.c.Scopes...)
	return fosite.Arguments(scopes)
}

// IsPublic mirrors the stored public flag. When true, fosite (configured with
// EnforcePKCEForPublicClients) REQUIRES a PKCE S256 challenge.
func (a *clientAdapter) IsPublic() bool { return a.c.IsPublic }

// GetAudience returns no fixed audience allow-list at this layer.
func (a *clientAdapter) GetAudience() fosite.Arguments { return fosite.Arguments{} }
