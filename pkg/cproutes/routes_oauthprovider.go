// routes_oauthprovider.go — OAUTHP-01: Vulos Cloud as an OAuth2 / OIDC provider.
//
// Vulos controls identity for the suite, so it issues "Sign in with Vulos" plus
// scoped API access. The Vulos Mail product and approved third parties run the
// Authorization Code flow WITH PKCE (S256) + refresh tokens against Vulos
// accounts (internal/auth — the single identity source of truth).
//
// OIDC / OAuth2 endpoints (public, browser/back-channel):
//
//	GET  /.well-known/openid-configuration   — discovery document
//	GET  /.well-known/jwks.json              — RS256 public signing keys (JWKS; alias)
//	GET  /oauth/jwks                          — RS256 public signing keys (JWKS)
//	GET  /oauth/authorize                     — validate + consent gate → auth code
//	GET  /oauth2/authorize                    — alias (same handler)
//	POST /oauth/token                         — code→tokens (PKCE) + refresh grant
//	POST /oauth2/token                        — alias (same handler)
//	GET  /oauth/userinfo                      — Bearer access token → profile/email
//	POST /oauth/userinfo                      — (same, per OIDC spec)
//	GET  /oauth2/userinfo                     — alias
//	POST /oauth2/userinfo                     — alias
//	POST /oauth/revoke                        — RFC 7009 token revocation
//	POST /oauth2/revoke                       — alias
//
// Consent (session-gated, used by the React consent page src/pages/Consent.jsx):
//
//	GET  /api/oauth/authorize/info           — client/scope display for consent
//	POST /api/oauth/authorize/decision       — approve→code redirect | deny
//
// Consent management (session-gated — user's own consent records):
//
//	GET    /api/oauth/consents               — list user's stored consent grants
//	DELETE /api/oauth/consents/{client_id}   — revoke a stored consent
//
// Developer console (session-gated, ownership-checked — src/pages/app/Developer.jsx):
//
//	GET    /api/oauth/apps                    — list caller's apps
//	POST   /api/oauth/apps                    — register app (secret shown once)
//	GET    /api/oauth/apps/{client_id}        — app detail
//	DELETE /api/oauth/apps/{client_id}        — delete app
//	POST   /api/oauth/apps/{client_id}/rotate-secret — rotate secret (shown once)
//
// Rate limits:
//   - POST /oauth/token + /oauth2/token: 60 requests/minute per IP (token endpoint)
//   - GET /oauth/authorize + /oauth2/authorize: 30 requests/minute per IP
package cproutes

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/openid"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/ddos"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/oauthfosite"
	"github.com/vul-os/vulos-management/pkg/oauthprovider"
)

// oauthIPLimiter is a tiny fixed-window per-IP rate limiter used to cap
// requests to the token and authorize endpoints, which are the most
// security-sensitive (brute-force of codes / tokens).
//
// LIMITER-EVICT-01: the hits map is internet-facing (keyed by client IP), so
// without eviction it grows unbounded under IP churn/spoofing. allow() runs an
// amortised sweep (at most once per window) that drops entries whose fixed
// window has already expired, bounding memory to the count of IPs seen within
// the current window.
type oauthIPLimiter struct {
	mu        sync.Mutex
	hits      map[string]*oauthIPWindow
	limit     int
	window    time.Duration
	lastSweep time.Time
}

type oauthIPWindow struct {
	count int
	reset time.Time
}

func newOAuthIPLimiter(limit int, window time.Duration) *oauthIPLimiter {
	return &oauthIPLimiter{hits: make(map[string]*oauthIPWindow), limit: limit, window: window, lastSweep: time.Now()}
}

func (l *oauthIPLimiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
	w, ok := l.hits[ip]
	if !ok || now.After(w.reset) {
		l.hits[ip] = &oauthIPWindow{count: 1, reset: now.Add(l.window)}
		return true
	}
	w.count++
	return w.count <= l.limit
}

// sweepLocked drops entries whose fixed window has expired. Runs at most once
// per window. Must be called with l.mu held.
func (l *oauthIPLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	for ip, w := range l.hits {
		if now.After(w.reset) {
			delete(l.hits, ip)
		}
	}
}

type oauthProviderHandlers struct {
	svc  *oauthprovider.Service
	auth *auth.Store
	// fosite is the ory/fosite protocol engine that now serves the token,
	// userinfo, introspection and revocation endpoints and mints authorization
	// codes. It is backed by the SAME audited oauthprovider store (clients +
	// RS256 signing key) via internal/oauthfosite; nil only if the engine failed
	// to build, in which case the protocol endpoints fail closed (503).
	fosite       fosite.OAuth2Provider
	fstore       *oauthfosite.Store
	tokenLimiter *oauthIPLimiter
	authzLimiter *oauthIPLimiter
}

// RegisterOAuthProviderRoutes wires the provider endpoints into mux.
func RegisterOAuthProviderRoutes(mux *http.ServeMux, svc *oauthprovider.Service, authStore *auth.Store) {
	h := &oauthProviderHandlers{
		svc:          svc,
		auth:         authStore,
		tokenLimiter: newOAuthIPLimiter(60, time.Minute), // 60 token requests/min/IP
		authzLimiter: newOAuthIPLimiter(30, time.Minute), // 30 authorize requests/min/IP
	}

	// Build the fosite engine on the SAME cpdb connection the oauthprovider store
	// uses, backed by that store's clients + signing key (single source of truth).
	// If svc built successfully its signing key + pairwise salt already loaded, so
	// New() cannot realistically fail here; on the off chance it does we log and
	// leave h.fosite nil (protocol endpoints then return 503 rather than panic).
	if fstore, ferr := oauthfosite.Open(svc.Store().CPDB(), svc.Store()); ferr != nil {
		log.Printf("[oauthprovider] ERROR: could not open fosite store: %v — protocol endpoints disabled", ferr)
	} else if provider, perr := oauthfosite.New(context.Background(), fstore, svc.Issuer()); perr != nil {
		log.Printf("[oauthprovider] ERROR: could not build fosite provider: %v — protocol endpoints disabled", perr)
	} else {
		h.fosite = provider
		h.fstore = fstore
	}

	// OIDC / OAuth2 protocol endpoints (canonical /oauth/ paths).
	mux.HandleFunc("GET /.well-known/openid-configuration", h.discovery)
	mux.HandleFunc("GET /.well-known/jwks.json", h.jwks) // RFC 8414 well-known JWKS alias
	mux.HandleFunc("GET /oauth/jwks", h.jwks)
	mux.HandleFunc("GET /oauth/authorize", h.authorize)
	mux.HandleFunc("POST /oauth/token", h.token)
	mux.HandleFunc("GET /oauth/userinfo", h.userinfo)
	mux.HandleFunc("POST /oauth/userinfo", h.userinfo)
	mux.HandleFunc("POST /oauth/revoke", h.revoke)         // RFC 7009
	mux.HandleFunc("POST /oauth/introspect", h.introspect) // RFC 7662

	// /oauth2/ aliases — advertised in the task spec; same handlers.
	mux.HandleFunc("GET /oauth2/authorize", h.authorize)
	mux.HandleFunc("POST /oauth2/token", h.token)
	mux.HandleFunc("GET /oauth2/userinfo", h.userinfo)
	mux.HandleFunc("POST /oauth2/userinfo", h.userinfo)
	mux.HandleFunc("POST /oauth2/revoke", h.revoke)
	mux.HandleFunc("POST /oauth2/introspect", h.introspect)

	// Consent (session-gated; drives the React consent page).
	mux.HandleFunc("GET /api/oauth/authorize/info", h.consentInfo)
	mux.HandleFunc("POST /api/oauth/authorize/decision", h.consentDecision)

	// Consent management (session-gated — the user's own stored grants).
	mux.HandleFunc("GET /api/oauth/consents", h.listConsents)
	mux.HandleFunc("DELETE /api/oauth/consents/{client_id}", h.revokeConsent)

	// Developer console CRUD (session-gated, ownership-checked).
	mux.HandleFunc("GET /api/oauth/apps", h.listApps)
	mux.HandleFunc("POST /api/oauth/apps", h.createApp)
	mux.HandleFunc("GET /api/oauth/apps/{client_id}", h.getApp)
	mux.HandleFunc("DELETE /api/oauth/apps/{client_id}", h.deleteApp)
	mux.HandleFunc("POST /api/oauth/apps/{client_id}/rotate-secret", h.rotateSecret)
}

// ─────────────────────────────────────────────────────────────────────────────
// Discovery + JWKS
// ─────────────────────────────────────────────────────────────────────────────

func (h *oauthProviderHandlers) discovery(w http.ResponseWriter, r *http.Request) {
	iss := h.svc.Issuer()
	doc := map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                iss + "/oauth/authorize",
		"token_endpoint":                        iss + "/oauth/token",
		"userinfo_endpoint":                     iss + "/oauth/userinfo",
		"introspection_endpoint":                iss + "/oauth/introspect",
		"revocation_endpoint":                   iss + "/oauth/revoke",
		"jwks_uri":                              iss + "/oauth/jwks",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"subject_types_supported":               []string{"public", "pairwise"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		// RFC 9207: we return the `iss` parameter in every authorization response
		// (IdP mix-up defence). Advertise it so RPs can require + verify it.
		"authorization_response_iss_parameter_supported": true,
		// Advertise only the GRANTABLE scopes: while hosted mail is dormant the
		// mail.* scopes are omitted here (they stay known internally). Advertising a
		// scope the authorize endpoint would reject is misleading — this keeps
		// discovery honest and flips automatically when VULOS_HOSTED_MAIL=1.
		"scopes_supported":                      oauthprovider.GrantableScopes(),
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"code_challenge_methods_supported":      []string{"S256"},
		"claims_supported":                      []string{"sub", "iss", "aud", "exp", "iat", "auth_time", "nonce", "email", "email_verified"},
	}
	httpx.JSON(w, doc)
}

func (h *oauthProviderHandlers) jwks(w http.ResponseWriter, r *http.Request) {
	keys, err := h.svc.Store().AllPublicJWKs(r.Context())
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not load signing keys")
		return
	}
	httpx.JSON(w, map[string]any{"keys": keys})
}

// ─────────────────────────────────────────────────────────────────────────────
// Authorize (validate + consent gate)
// ─────────────────────────────────────────────────────────────────────────────

func parseAuthorizeRequest(r *http.Request) oauthprovider.AuthorizeRequest {
	q := r.URL.Query()
	return oauthprovider.AuthorizeRequest{
		ResponseType:        q.Get("response_type"),
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		Scope:               q.Get("scope"),
		State:               q.Get("state"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
		Nonce:               q.Get("nonce"),
	}
}

// clientIP returns the request's originating IP for rate-limit keying.
//
// It delegates to ddos.RealClientIP, which only honours X-Forwarded-For when the
// immediate upstream is within the configured trusted-proxy CIDR
// (VULOS_EDGE_TRUST_*). The previous implementation trusted the leftmost XFF value
// unconditionally, so an attacker could rotate the header to get an unbounded
// number of buckets and bypass the per-IP authorize/token rate limiters entirely.
func clientIP(r *http.Request) string {
	return ddos.RealClientIP(r)
}

// authorize validates the request, then either renders an error page (when the
// error is not safe to redirect), redirects the error back to the client,
// bounces to login (no session), auto-approves via stored consent, or forwards
// the browser to the consent page.
func (h *oauthProviderHandlers) authorize(w http.ResponseWriter, r *http.Request) {
	// Rate-limit the authorize endpoint per IP.
	if !h.authzLimiter.allow(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	req := parseAuthorizeRequest(r)
	v, aerr := h.svc.ValidateAuthorize(r.Context(), req)
	if aerr != nil {
		if aerr.RedirectOK {
			loc, err := oauthprovider.BuildRedirect(req.RedirectURI, map[string]string{
				"error":             aerr.Code,
				"error_description": aerr.Description,
				"state":             req.State,
				"iss":               h.svc.Issuer(), // RFC 9207
			})
			if err == nil {
				http.Redirect(w, r, loc, http.StatusFound)
				return
			}
		}
		renderAuthorizeError(w, aerr)
		return
	}

	// Require a logged-in Vulos user. If absent, bounce to login, preserving the
	// full authorize URL so we land back here after sign-in.
	u := h.sessionUser(r)
	if u == nil {
		ret := r.URL.RequestURI()
		http.Redirect(w, r, "/login?return="+url.QueryEscape(ret), http.StatusFound)
		return
	}

	// Auto-approve: if the user has already consented to all requested scopes
	// for this client, skip the consent screen and issue a code immediately.
	if h.svc.HasConsent(r.Context(), u.ID, v.Client.ClientID, v.Scopes) {
		loc, err := h.issueFositeCode(r, v, u)
		if err != nil {
			renderAuthorizeError(w, &oauthprovider.AuthzError{Code: "server_error", Description: "could not issue authorization code"})
			return
		}
		log.Printf("[oauthprovider] auto-approved (stored consent) account=%s client=%s scopes=%q", u.ID, v.Client.ClientID, oauthprovider.JoinScopes(v.Scopes))
		http.Redirect(w, r, loc, http.StatusFound)
		return
	}

	// No stored consent: forward to the consent screen (React page). The page
	// re-fetches validated display info via /api/oauth/authorize/info and posts
	// the user's decision to /api/oauth/authorize/decision.
	http.Redirect(w, r, "/oauth/consent?"+r.URL.RawQuery, http.StatusFound)
}

// sessionUser returns the logged-in user without writing an error (so callers
// can choose to redirect to login instead of returning 401).
func (h *oauthProviderHandlers) sessionUser(r *http.Request) *auth.User {
	token := auth.SessionFromRequest(r)
	if token == "" {
		return nil
	}
	u, err := h.auth.LookupSession(r.Context(), token)
	if err != nil {
		return nil
	}
	return u
}

func renderAuthorizeError(w http.ResponseWriter, aerr *oauthprovider.AuthzError) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	// Minimal, dependency-free error page. The consent UI itself is the React
	// page; this only fires for non-redirectable validation failures (bad
	// client_id / redirect_uri) where we must NOT bounce to an untrusted URL.
	_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8">` +
		`<title>Vulos — authorization error</title>` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<style>body{font-family:system-ui,sans-serif;background:#0b0d10;color:#e6e8eb;` +
		`display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}` +
		`.box{max-width:32rem;padding:2rem;text-align:center}` +
		`code{background:#1a1d21;padding:.15rem .4rem;border-radius:.25rem}` +
		`</style></head><body><div class="box"><h1>Authorization error</h1>` +
		`<p>This sign-in request could not be processed: <code>` +
		htmlEscape(aerr.Code) + `</code></p><p>` + htmlEscape(aerr.Description) +
		`</p><p>Please return to the application and try again.</p></div></body></html>`))
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// ─────────────────────────────────────────────────────────────────────────────
// Consent (session-gated)
// ─────────────────────────────────────────────────────────────────────────────

func (h *oauthProviderHandlers) consentInfo(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	req := parseAuthorizeRequest(r)
	v, aerr := h.svc.ValidateAuthorize(r.Context(), req)
	if aerr != nil {
		httpx.Err(w, http.StatusBadRequest, aerr.Code+": "+aerr.Description)
		return
	}
	httpx.JSON(w, map[string]any{
		"client_id":    v.Client.ClientID,
		"client_name":  v.Client.Name,
		"redirect_uri": v.RedirectURI,
		"scopes":       v.Scopes,
		"user_email":   u.Email,
		"state":        v.State,
	})
}

func (h *oauthProviderHandlers) consentDecision(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	var body struct {
		Approve             bool   `json:"approve"`
		ResponseType        string `json:"response_type"`
		ClientID            string `json:"client_id"`
		RedirectURI         string `json:"redirect_uri"`
		Scope               string `json:"scope"`
		State               string `json:"state"`
		CodeChallenge       string `json:"code_challenge"`
		CodeChallengeMethod string `json:"code_challenge_method"`
		Nonce               string `json:"nonce"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	req := oauthprovider.AuthorizeRequest{
		ResponseType:        body.ResponseType,
		ClientID:            body.ClientID,
		RedirectURI:         body.RedirectURI,
		Scope:               body.Scope,
		State:               body.State,
		CodeChallenge:       body.CodeChallenge,
		CodeChallengeMethod: body.CodeChallengeMethod,
		Nonce:               body.Nonce,
	}
	v, aerr := h.svc.ValidateAuthorize(r.Context(), req)
	if aerr != nil {
		httpx.Err(w, http.StatusBadRequest, aerr.Code+": "+aerr.Description)
		return
	}

	if !body.Approve {
		loc, _ := oauthprovider.BuildRedirect(v.RedirectURI, map[string]string{
			"error": "access_denied",
			"state": v.State,
			"iss":   h.svc.Issuer(), // RFC 9207
		})
		httpx.JSON(w, map[string]any{"redirect": loc})
		return
	}

	// Persist the consent grant so future requests for the same client + scopes
	// skip the consent screen.
	if err := h.svc.GrantConsent(r.Context(), u.ID, v); err != nil {
		// Non-fatal: consent storage failure must not block the authorization flow.
		log.Printf("[oauthprovider] WARNING: could not persist consent grant account=%s client=%s: %v", u.ID, v.Client.ClientID, err)
	}

	loc, err := h.issueFositeCode(r, v, u)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not issue authorization code")
		return
	}
	log.Printf("[oauthprovider] consent approved account=%s client=%s scopes=%q", u.ID, v.Client.ClientID, oauthprovider.JoinScopes(v.Scopes))
	httpx.JSON(w, map[string]any{"redirect": loc})
}

// ─────────────────────────────────────────────────────────────────────────────
// Token endpoint
// ─────────────────────────────────────────────────────────────────────────────

// writeOAuthError emits the RFC 6749 §5.2 error JSON with the right status.
func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="oauth"`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

func (h *oauthProviderHandlers) token(w http.ResponseWriter, r *http.Request) {
	// Rate-limit the token endpoint per IP. This is the most targeted endpoint
	// for code/token brute-force; we're generous (60/min) but not unlimited.
	if !h.tokenLimiter.allow(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeOAuthError(w, http.StatusTooManyRequests, "slow_down", "rate limit exceeded; retry after 60 seconds")
		return
	}
	if h.fosite == nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "authorization server is not ready")
		return
	}
	ctx := r.Context()
	// A fresh OIDC session; the auth-code / refresh grants hydrate it from the
	// stored authorize session (carrying the subject + email/verified claims).
	sess := openid.NewDefaultSession()
	ar, err := h.fosite.NewAccessRequest(ctx, r, sess)
	if err != nil {
		h.fosite.WriteAccessError(ctx, w, ar, err)
		return
	}
	resp, err := h.fosite.NewAccessResponse(ctx, ar)
	if err != nil {
		h.fosite.WriteAccessError(ctx, w, ar, err)
		return
	}
	h.writeTokenResponse(w, resp)
}

// writeTokenResponse serialises a fosite access response, re-casing token_type
// to the canonical "Bearer" (fosite emits lowercase "bearer"; RFC 6749 §7.1 is
// case-insensitive but Vulos has always returned "Bearer" and some relying
// parties compare exactly). Cache headers match RFC 6749 §5.1.
func (h *oauthProviderHandlers) writeTokenResponse(w http.ResponseWriter, resp fosite.AccessResponder) {
	m := resp.ToMap()
	if tt, _ := m["token_type"].(string); strings.EqualFold(tt, "bearer") {
		m["token_type"] = "Bearer"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	httpx.JSON(w, m)
}

// issueFositeCode mints an authorization code for an already session-
// authenticated, consent-approved request and returns the client redirect URL
// carrying the code + state + RFC 9207 `iss`.
//
// The request was already validated by the hand-rolled ValidateAuthorize (which
// enforces redirect exact-match, S256 PKCE, and client-scope subset) and consent
// was already granted, so it is handed to oauthfosite.IssueCode, which mints +
// stores the code (and its PKCE + OIDC session) through fosite's authorize
// pipeline. The OIDC session is seeded with the pairwise-or-raw subject and the
// email claims (only when the email scope is granted) so the id_token and
// userinfo assert exactly what the hand-rolled provider did.
func (h *oauthProviderHandlers) issueFositeCode(r *http.Request, v oauthprovider.ValidatedAuthorize, u *auth.User) (string, error) {
	if h.fosite == nil || h.fstore == nil {
		return "", errors.New("oauthprovider: authorization server not ready")
	}
	g := oauthfosite.AuthzGrant{
		ClientID:      v.Client.ClientID,
		RedirectURI:   v.RedirectURI,
		Scopes:        v.Scopes,
		State:         v.State,
		Nonce:         v.Nonce,
		CodeChallenge: v.CodeChallenge,
		Subject:       h.svc.SubjectFor(v.Client, u.ID),
	}
	if oauthprovider.HasScope(oauthprovider.JoinScopes(v.Scopes), oauthprovider.ScopeEmail) {
		g.EmailClaims = map[string]any{"email": u.Email, "email_verified": u.EmailVerified}
	}
	params, err := oauthfosite.IssueCode(r.Context(), h.fosite, h.fstore, g)
	if err != nil {
		return "", err
	}
	return buildAuthorizeRedirect(v.RedirectURI, params, h.svc.Issuer())
}

// buildAuthorizeRedirect assembles the client redirect URL from fosite's
// authorize response parameters (code + state), then appends the RFC 9207 `iss`
// parameter (IdP mix-up defence) that Vulos advertises in its discovery document.
func buildAuthorizeRedirect(redirectURI string, params url.Values, issuer string) (string, error) {
	redir, err := url.Parse(redirectURI)
	if err != nil {
		return "", err
	}
	q := redir.Query()
	for k, vs := range params {
		for _, val := range vs {
			q.Set(k, val)
		}
	}
	q.Set("iss", issuer) // RFC 9207
	redir.RawQuery = q.Encode()
	return redir.String(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// userinfo
// ─────────────────────────────────────────────────────────────────────────────

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

func (h *oauthProviderHandlers) userinfo(w http.ResponseWriter, r *http.Request) {
	tok := bearerToken(r)
	if tok == "" {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_request"`)
		httpx.Err(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	if h.fosite == nil {
		httpx.Err(w, http.StatusServiceUnavailable, "authorization server is not ready")
		return
	}
	ctx := r.Context()
	// Validate the access token through fosite and recover its granted scope +
	// stored OIDC session (subject + email claims seeded at authorize time).
	sess := openid.NewDefaultSession()
	_, ar, err := h.fosite.IntrospectToken(ctx, tok, fosite.AccessToken, sess)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		httpx.Err(w, http.StatusUnauthorized, "invalid access token")
		return
	}
	ds, _ := ar.GetSession().(*openid.DefaultSession)
	if ds == nil {
		httpx.Err(w, http.StatusInternalServerError, "invalid token session")
		return
	}
	out := map[string]any{"sub": ds.GetSubject()}
	if ar.GetGrantedScopes().Has(oauthprovider.ScopeEmail) {
		claims := ds.IDTokenClaims()
		out["email"] = claims.Extra["email"]
		out["email_verified"] = claims.Extra["email_verified"]
	}
	httpx.JSON(w, out)
}

// introspect implements RFC 7662 token introspection. The caller MUST
// authenticate (client_secret_basic / client_secret_post, or a Bearer access
// token). fosite returns {"active": false} for any unknown, expired, or revoked
// token without leaking why.
func (h *oauthProviderHandlers) introspect(w http.ResponseWriter, r *http.Request) {
	if !h.tokenLimiter.allow(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeOAuthError(w, http.StatusTooManyRequests, "slow_down", "rate limit exceeded; retry after 60 seconds")
		return
	}
	if h.fosite == nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "authorization server is not ready")
		return
	}
	ctx := r.Context()
	resp, err := h.fosite.NewIntrospectionRequest(ctx, r, openid.NewDefaultSession())
	if err != nil {
		h.fosite.WriteIntrospectionError(ctx, w, err)
		return
	}
	h.fosite.WriteIntrospectionResponse(ctx, w, resp)
}

// ─────────────────────────────────────────────────────────────────────────────
// Developer console CRUD
// ─────────────────────────────────────────────────────────────────────────────

func clientJSON(c oauthprovider.Client) map[string]any {
	return map[string]any{
		"client_id":     c.ClientID,
		"name":          c.Name,
		"redirect_uris": c.RedirectURIs,
		"scopes":        c.Scopes,
		"public":        c.IsPublic,
		"subject_type":  c.SubjectType,
		"created_at":    c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		"updated_at":    c.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (h *oauthProviderHandlers) listApps(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	clients, err := h.svc.Store().ListClientsByOwner(r.Context(), u.ID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not list apps")
		return
	}
	out := make([]map[string]any, 0, len(clients))
	for _, c := range clients {
		out = append(out, clientJSON(c))
	}
	httpx.JSON(w, out)
}

func (h *oauthProviderHandlers) createApp(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	var body struct {
		Name         string   `json:"name"`
		RedirectURIs []string `json:"redirect_uris"`
		Scopes       []string `json:"scopes"`
		Public       bool     `json:"public"`
		// Optional. Omitted/"" ⇒ "public" (raw-id sub) — unchanged behaviour.
		// "pairwise" ⇒ per-client non-reversible sub.
		SubjectType      string `json:"subject_type"`
		SectorIdentifier string `json:"sector_identifier"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	c, secret, err := h.svc.RegisterClientTyped(r.Context(), u.ID, body.Name, body.RedirectURIs, body.Scopes, body.Public, body.SubjectType, body.SectorIdentifier)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("[oauthprovider] account=%s registered client=%s public=%v", u.ID, c.ClientID, c.IsPublic)
	resp := clientJSON(c)
	// The plaintext secret is returned EXACTLY ONCE here (never persisted, never
	// retrievable again — only rotatable).
	resp["client_secret"] = secret
	w.WriteHeader(http.StatusCreated)
	httpx.JSON(w, resp)
}

// requireOwnedClient resolves {client_id} and verifies caller ownership. A
// non-owned or missing client is reported as 404 (no existence disclosure).
func (h *oauthProviderHandlers) requireOwnedClient(w http.ResponseWriter, r *http.Request) (oauthprovider.Client, *auth.User, bool) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return oauthprovider.Client{}, nil, false
	}
	clientID := r.PathValue("client_id")
	c, err := h.svc.Store().GetClient(r.Context(), clientID)
	if err != nil || c.OwnerUserID != u.ID {
		httpx.Err(w, http.StatusNotFound, "app not found")
		return oauthprovider.Client{}, nil, false
	}
	return c, u, true
}

func (h *oauthProviderHandlers) getApp(w http.ResponseWriter, r *http.Request) {
	c, _, ok := h.requireOwnedClient(w, r)
	if !ok {
		return
	}
	httpx.JSON(w, clientJSON(c))
}

func (h *oauthProviderHandlers) deleteApp(w http.ResponseWriter, r *http.Request) {
	c, u, ok := h.requireOwnedClient(w, r)
	if !ok {
		return
	}
	if err := h.svc.Store().DeleteClient(r.Context(), c.ClientID); err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not delete app")
		return
	}
	log.Printf("[oauthprovider] account=%s deleted client=%s", u.ID, c.ClientID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *oauthProviderHandlers) rotateSecret(w http.ResponseWriter, r *http.Request) {
	c, u, ok := h.requireOwnedClient(w, r)
	if !ok {
		return
	}
	secret, err := h.svc.RotateSecret(r.Context(), c.ClientID)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("[oauthprovider] account=%s rotated secret for client=%s", u.ID, c.ClientID)
	httpx.JSON(w, map[string]any{"client_id": c.ClientID, "client_secret": secret})
}

// ─────────────────────────────────────────────────────────────────────────────
// RFC 7009 token revocation
// ─────────────────────────────────────────────────────────────────────────────

// revoke implements POST /oauth/revoke and POST /oauth2/revoke (RFC 7009) via
// fosite. fosite authenticates the client (Basic or client_secret_post; public
// clients present only client_id), then revokes the presented access or refresh
// token AND its family. Per RFC 7009 §2.2 an unknown/invalid token is a no-op
// that still returns 200 (no existence oracle); a failed client authentication
// returns 401.
func (h *oauthProviderHandlers) revoke(w http.ResponseWriter, r *http.Request) {
	if h.fosite == nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "authorization server is not ready")
		return
	}
	ctx := r.Context()
	err := h.fosite.NewRevocationRequest(ctx, r)
	h.fosite.WriteRevocationResponse(ctx, w, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// Consent management (user's stored grants)
// ─────────────────────────────────────────────────────────────────────────────

// listConsents returns the user's live consent grants (session-gated).
func (h *oauthProviderHandlers) listConsents(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	consents, err := h.svc.ListUserConsents(r.Context(), u.ID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not list consents")
		return
	}
	out := make([]map[string]any, 0, len(consents))
	for _, ci := range consents {
		out = append(out, map[string]any{
			"client_id":   ci.ClientID,
			"client_name": ci.ClientName,
			"scope":       ci.Scope,
			"granted_at":  ci.GrantedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	httpx.JSON(w, out)
}

// revokeConsent revokes the user's stored consent for a specific client.
// Also revokes all live tokens issued to that client for this user so the
// revocation takes immediate effect.
func (h *oauthProviderHandlers) revokeConsent(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	clientID := r.PathValue("client_id")
	if clientID == "" {
		httpx.Err(w, http.StatusBadRequest, "client_id required")
		return
	}
	if err := h.svc.RevokeConsent(r.Context(), u.ID, clientID); err != nil {
		// Not found → 404; any other error → 500.
		if errors.Is(err, oauthprovider.ErrConsentNotFound) {
			httpx.Err(w, http.StatusNotFound, "no active consent for this client")
			return
		}
		httpx.Err(w, http.StatusInternalServerError, "could not revoke consent")
		return
	}
	log.Printf("[oauthprovider] account=%s revoked consent for client=%s", u.ID, clientID)
	w.WriteHeader(http.StatusNoContent)
}
