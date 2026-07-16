package oauthfosite

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/openid"
	"github.com/ory/fosite/token/jwt"
)

// AuthzGrant is a fully pre-validated, consent-approved authorization request
// that is ready to be turned into an authorization code. The Vulos control-plane
// authorize/consent handlers have ALREADY enforced, before constructing this:
//   - the user is authenticated (a live Vulos session),
//   - redirect_uri exactly matches a registered URI,
//   - PKCE is present and uses S256,
//   - the requested scopes are a subset of the client's registered scopes,
//   - consent is granted (stored grant or an explicit "Allow").
//
// IssueCode therefore mints the code through fosite's authorize pipeline WITHOUT
// re-running fosite's own request parser (NewAuthorizeRequest), whose mandatory
// `state` minimum-entropy rule Vulos deliberately does not impose (the hand-
// rolled provider left `state` optional and unchecked — RPs own their CSRF
// state). Every SECURITY-relevant step still runs inside NewAuthorizeResponse:
// the authorize-code handler re-checks the requested scopes against the client's
// allow-list and the redirect scheme, the PKCE handler persists the S256
// challenge for verification at the token endpoint, and the OpenID handler
// stores the id_token session.
type AuthzGrant struct {
	ClientID      string
	RedirectURI   string
	Scopes        []string
	State         string
	Nonce         string
	CodeChallenge string // S256 code_challenge (method is fixed to S256)
	// Subject is the id_token/access-token `sub` (pairwise-or-raw; computed by
	// the caller's oauthprovider policy).
	Subject string
	// EmailClaims, when non-nil, is merged into the id_token / userinfo claims
	// (email + email_verified) — set only when the email scope was granted.
	EmailClaims map[string]any
}

// IssueCode mints an authorization code for g through fosite and returns the
// redirect parameters (code + state) to deliver to the client. The caller adds
// the RFC 9207 `iss` parameter. The code, its PKCE challenge and the OIDC
// session are persisted in the fosite session tables keyed by the code
// signature, exactly as a normal fosite authorize flow would store them, so the
// token endpoint validates and consumes the code identically.
func IssueCode(ctx context.Context, provider fosite.OAuth2Provider, store *Store, g AuthzGrant) (url.Values, error) {
	if provider == nil || store == nil {
		return nil, fmt.Errorf("oauthfosite: nil provider/store")
	}
	client, err := store.GetClient(ctx, g.ClientID)
	if err != nil {
		return nil, err
	}
	redir, err := url.Parse(g.RedirectURI)
	if err != nil {
		return nil, fmt.Errorf("oauthfosite: parse redirect_uri: %w", err)
	}

	// The request form is what the PKCE + OpenID handlers read (code_challenge,
	// code_challenge_method, nonce, redirect_uri). It must mirror a real
	// authorize query.
	form := url.Values{}
	form.Set("response_type", "code")
	form.Set("client_id", g.ClientID)
	form.Set("redirect_uri", g.RedirectURI)
	form.Set("scope", strings.Join(g.Scopes, " "))
	if g.State != "" {
		form.Set("state", g.State)
	}
	form.Set("code_challenge", g.CodeChallenge)
	form.Set("code_challenge_method", "S256")
	if g.Nonce != "" {
		form.Set("nonce", g.Nonce)
	}

	ar := fosite.NewAuthorizeRequest()
	ar.Client = client
	ar.Form = form
	ar.RedirectURI = redir
	ar.ResponseTypes = fosite.Arguments{"code"}
	ar.State = g.State
	ar.RequestedAt = time.Now().UTC()
	ar.SetRequestedScopes(fosite.Arguments(g.Scopes))
	// Consent already granted upstream: grant every requested scope so the
	// OpenID handler issues an id_token (openid) and the response `scope` is
	// exactly the requested set.
	for _, s := range g.Scopes {
		ar.GrantScope(s)
	}

	now := time.Now().UTC()
	sess := &openid.DefaultSession{
		Claims: &jwt.IDTokenClaims{
			Subject:     g.Subject,
			IssuedAt:    now,
			RequestedAt: now,
			AuthTime:    now,
			Extra:       map[string]any{},
		},
		Headers: &jwt.Headers{},
		Subject: g.Subject,
	}
	for k, v := range g.EmailClaims {
		sess.Claims.Extra[k] = v
	}

	resp, err := provider.NewAuthorizeResponse(ctx, ar, sess)
	if err != nil {
		return nil, err
	}
	return resp.GetParameters(), nil
}
