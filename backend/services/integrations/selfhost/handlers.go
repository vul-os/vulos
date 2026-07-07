package selfhost

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"
)

// RegisterHandlers wires the self-host integration endpoints onto mux. They are
// session-gated by the OS auth middleware (which injects X-User-ID after
// validating the session). The routes mirror the CP surface but run entirely on
// the box against the owner's own OAuth apps.
//
//	GET /api/integrations/{provider}/connect  — 302 → provider consent (auth-code + PKCE)
//	GET /api/integrations/{provider}/callback — exchange code, store encrypted refresh token
//	GET /api/integrations/{provider}/status   — { configured, connected, email }
//	GET /api/integrations                      — list caller's connections
//	GET /api/integrations/{provider}/token    — { access_token, expires_at, scopes }
//	DELETE /api/integrations/{provider}        — disconnect
//
// stateSecret is the HMAC key for CSRF/PKCE state (a stable per-box secret).
// secureCookie should be true when the box serves HTTPS (prod).
func RegisterHandlers(mux *http.ServeMux, svc *Service, stateSecret []byte, secureCookie bool) {
	h := &handlers{svc: svc, stateSecret: stateSecret, secureCookie: secureCookie, now: func() time.Time { return time.Now().UTC() }}

	mux.HandleFunc("GET /api/integrations/{provider}/connect", h.connect)
	mux.HandleFunc("GET /api/integrations/{provider}/callback", h.callback)
	mux.HandleFunc("GET /api/integrations/{provider}/status", h.status)
	mux.HandleFunc("GET /api/integrations/{provider}/token", h.token)
	mux.HandleFunc("DELETE /api/integrations/{provider}", h.disconnect)
	mux.HandleFunc("GET /api/integrations", h.list)
}

type handlers struct {
	svc          *Service
	stateSecret  []byte
	secureCookie bool
	now          func() time.Time
}

// requireUser returns the session user id (injected by the auth middleware) or ""
// after writing 401.
func (h *handlers) requireUser(w http.ResponseWriter, r *http.Request) string {
	uid := r.Header.Get("X-User-ID")
	if uid == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
	}
	return uid
}

// connect: 302 → provider consent. Session-gated; the account is re-derived from
// the session at callback time (never trusted from the redirect).
func (h *handlers) connect(w http.ResponseWriter, r *http.Request) {
	uid := h.requireUser(w, r)
	if uid == "" {
		return
	}
	provider := r.PathValue("provider")
	if !KnownProvider(provider) {
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	if !ProviderConfigured(provider) {
		// The owner has not set this provider's client credentials — unavailable,
		// not a crash.
		writeErr(w, http.StatusServiceUnavailable, provider+" is not configured on this instance")
		return
	}
	state, challenge, err := GenerateState(w, h.stateSecret, uid, provider, h.now(), h.secureCookie)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not start OAuth")
		return
	}
	url, err := h.svc.AuthCodeURL(provider, state, challenge)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, provider+" is not configured on this instance")
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// callback: verify CSRF/PKCE state, exchange the code, store the encrypted
// refresh token under the SESSION's user id, then 302 back to the account UI.
func (h *handlers) callback(w http.ResponseWriter, r *http.Request) {
	uid := h.requireUser(w, r)
	if uid == "" {
		return
	}
	provider := r.PathValue("provider")
	if !KnownProvider(provider) {
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	defer ClearStateCookie(w, h.secureCookie)

	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		http.Redirect(w, r, "/app/account?integration="+escape(provider)+"&status=denied", http.StatusFound)
		return
	}
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		writeErr(w, http.StatusBadRequest, "missing code or state")
		return
	}
	verifier, err := VerifyState(r, state, string(h.stateSecret), uid, provider, h.now())
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid OAuth state")
		return
	}
	if _, err := h.svc.Connect(r.Context(), uid, provider, code, verifier); err != nil {
		if errors.Is(err, ErrNoRefreshToken) {
			writeErr(w, http.StatusBadGateway, provider+" did not return a refresh token — retry and grant offline access")
			return
		}
		log.Printf("[selfhost/integrations] connect failed user=%s provider=%s: %v", uid, provider, err)
		writeErr(w, http.StatusBadGateway, "could not complete "+provider+" connection")
		return
	}
	log.Printf("[selfhost/integrations] user=%s connected provider=%s", uid, provider)
	http.Redirect(w, r, "/app/account?integration="+escape(provider)+"&status=connected", http.StatusFound)
}

// status: connection status + display metadata for the caller.
func (h *handlers) status(w http.ResponseWriter, r *http.Request) {
	uid := h.requireUser(w, r)
	if uid == "" {
		return
	}
	provider := r.PathValue("provider")
	if !KnownProvider(provider) {
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	connected, c, err := h.svc.Status(r.Context(), uid, provider)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "status lookup failed")
		return
	}
	resp := map[string]any{
		"provider":   provider,
		"configured": ProviderConfigured(provider),
		"connected":  connected,
		"self_host":  true,
	}
	if connected {
		resp["email"] = c.AccountEmail
		resp["scopes"] = c.Scopes
		resp["connected_at"] = c.CreatedAt.Format(time.RFC3339)
	}
	writeJSON(w, resp)
}

// token: mint a short-lived access token for the caller. Refreshes locally from
// the encrypted refresh token. The refresh token is NEVER returned.
func (h *handlers) token(w http.ResponseWriter, r *http.Request) {
	uid := h.requireUser(w, r)
	if uid == "" {
		return
	}
	provider := r.PathValue("provider")
	if !KnownProvider(provider) {
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	minted, err := h.svc.MintAccessToken(r.Context(), uid, provider)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotConnected):
			writeErr(w, http.StatusNotFound, provider+" is not connected for this account")
		case errors.Is(err, ErrProviderNotConfigured):
			writeErr(w, http.StatusServiceUnavailable, provider+" is not configured on this instance")
		default:
			log.Printf("[selfhost/integrations] mint failed user=%s provider=%s: %v", uid, provider, err)
			writeErr(w, http.StatusBadGateway, "could not obtain access token")
		}
		return
	}
	expiresIn := int(time.Until(minted.Expiry).Seconds())
	if expiresIn < 0 {
		expiresIn = 0
	}
	writeJSON(w, map[string]any{
		"access_token": minted.AccessToken,
		"expires_at":   minted.Expiry.UTC().Format(time.RFC3339),
		"expires_in":   expiresIn,
		"scopes":       minted.Scopes,
		"provider":     provider,
	})
}

// list: all of the caller's connections. Never includes any token material.
func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	uid := h.requireUser(w, r)
	if uid == "" {
		return
	}
	conns, err := h.svc.List(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]map[string]any, 0, len(conns))
	for _, c := range conns {
		out = append(out, map[string]any{
			"provider":     c.Provider,
			"email":        c.AccountEmail,
			"scopes":       c.Scopes,
			"connected_at": c.CreatedAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, out)
}

// disconnect: remove the caller's connection for a provider.
func (h *handlers) disconnect(w http.ResponseWriter, r *http.Request) {
	uid := h.requireUser(w, r)
	if uid == "" {
		return
	}
	provider := r.PathValue("provider")
	if !KnownProvider(provider) {
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	if err := h.svc.Disconnect(r.Context(), uid, provider); err != nil {
		log.Printf("[selfhost/integrations] disconnect failed user=%s provider=%s: %v", uid, provider, err)
		writeErr(w, http.StatusInternalServerError, "disconnect failed")
		return
	}
	log.Printf("[selfhost/integrations] user=%s disconnected provider=%s", uid, provider)
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
