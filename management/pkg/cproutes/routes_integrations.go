// routes_integrations.go — INTEG-01: third-party OAuth integration broker.
//
// The control plane is the fleet's OAuth broker. It holds the single registered
// Google client_id/secret + redirect_uri, runs the authorization-code exchange,
// and custodies the refresh_token encrypted at rest. Boxes mint short-lived
// access tokens over the device-HMAC channel (see the /token handler, Wave 2).
//
// Providers (EXT-PROVIDERS): {provider} is google or dropbox for the connect
// flow; the token mint also accepts the gcs alias (GCS objects are brokered via
// the Google grant — there is no separate gcs connection).
//
// Session-gated routes (the logged-in CP user connects/manages their account):
//
//	GET    /api/integrations/{provider}/start     — 302 → provider consent (offline)
//	GET    /api/integrations/{provider}/callback  — exchange code, store refresh token
//	GET    /api/integrations                       — list caller's connections
//	GET    /api/integrations/{provider}/status     — connection status for caller
//	DELETE /api/integrations/{provider}            — disconnect (revokes at provider)
//
// Device-authenticated route (a box mints a short-lived access token):
//
//	GET    /api/integrations/{provider}/token      — X-Integration-Sig (HMAC) + X-Device-ULID
//
// This is a connected-account DATA integration, NOT an identity provider —
// AUTH-02's "no Google login" decision stands. See CLOUD_SYSTEM_CONTRACT.md.
package cproutes

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/integrations"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// integrationsHandlers holds dependencies for integration HTTP handlers.
type integrationsHandlers struct {
	svc           *integrations.Service
	auth          *auth.Store
	stateSecret   []byte
	ownerResolver fleet.OwnershipResolver     // device ULID → owning account (Wave 2)
	deviceKeys    integrations.DeviceKeyStore // INTEG-SEC-01: pinned per-device pubkeys (TOFU)
	caCertPubKey  []byte                      // INTEG-SEC-01: ed25519 mgmt-CA pubkey (cert auth)
	now           func() time.Time
	mintLimiter   *mintRateLimiter // per-ULID rate limit on the token mint
}

// RegisterIntegrationsRoutes wires integration endpoints into mux.
//   - svc: the broker service (store + crypto + provider exchanger)
//   - authStore: session validation for the user-facing routes
//   - stateSecret: HMAC key for CSRF state (fleet-wide; e.g. CP_SHARED_SECRET)
//   - ownerResolver: resolves device ULID → account for the device-auth /token route
//   - deviceKeys: pinned per-device pubkey registry (INTEG-SEC-01 TOFU)
//   - caCertPubKey: ed25519 management-CA pubkey for owner-attested cert auth
func RegisterIntegrationsRoutes(mux *http.ServeMux, svc *integrations.Service, authStore *auth.Store, stateSecret []byte, ownerResolver fleet.OwnershipResolver, deviceKeys integrations.DeviceKeyStore, caCertPubKey []byte) {
	h := &integrationsHandlers{
		svc:           svc,
		auth:          authStore,
		stateSecret:   stateSecret,
		ownerResolver: ownerResolver,
		deviceKeys:    deviceKeys,
		caCertPubKey:  caCertPubKey,
		now:           func() time.Time { return time.Now().UTC() },
		// 30 mints/minute per device ULID: legit boxes cache and refresh ~1/hour,
		// so this is generous for them while throttling brute-force/scraping of
		// the fleet-wide-secret-authenticated endpoint.
		mintLimiter: newMintRateLimiter(30, time.Minute),
	}

	// Session-gated user routes. {provider} is google or dropbox (GCS has no
	// connect flow — it is brokered via the Google grant; see mintToken).
	mux.HandleFunc("GET /api/integrations/{provider}/start", h.start)
	mux.HandleFunc("GET /api/integrations/{provider}/callback", h.callback)
	mux.HandleFunc("GET /api/integrations", h.list)
	mux.HandleFunc("GET /api/integrations/{provider}/status", h.status)
	mux.HandleFunc("DELETE /api/integrations/{provider}", h.disconnect)

	// Device-authenticated token mint (Wave 2). HMAC/cert-gated, no session.
	// {provider} is google, dropbox, or the mint-only gcs alias.
	mux.HandleFunc("GET /api/integrations/{provider}/token", h.mintToken)

	// Device key registration (INTEG-SEC-01). Fleet-HMAC bootstrap + self-sig.
	mux.HandleFunc("POST /api/integrations/device/register", h.registerDeviceKey)

	// Owner-authed device-key status + rotation/revocation (INTEG-SEC-01). The
	// caller must own the ULID. DELETE un-pins so a re-keyed box can re-register
	// and so an owner can remediate a bad/attacker pin.
	mux.HandleFunc("GET /api/integrations/device/{ulid}/key", h.deviceKeyStatus)
	mux.HandleFunc("DELETE /api/integrations/device/{ulid}/key", h.revokeDeviceKey)
}

// requireOwnedULID resolves the path ULID and verifies the session account owns
// it. Returns the canonical ULID, or "" after writing the error response.
func (h *integrationsHandlers) requireOwnedULID(w http.ResponseWriter, r *http.Request) (string, *auth.User) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return "", nil
	}
	if h.deviceKeys == nil || h.ownerResolver == nil {
		httpx.Err(w, http.StatusServiceUnavailable, "device key registry unavailable")
		return "", nil
	}
	canon, err := routing.CanonULID(r.PathValue("ulid"))
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid ULID")
		return "", nil
	}
	owner, err := h.ownerResolver.OwnerOf(r.Context(), canon)
	if err != nil || owner == "" || owner != u.ID {
		// Don't distinguish "not yours" from "not enrolled" — both are 403.
		httpx.Err(w, http.StatusForbidden, "device not owned by this account")
		return "", nil
	}
	return canon, u
}

// deviceKeyStatus reports whether a device key is pinned for the owned ULID.
func (h *integrationsHandlers) deviceKeyStatus(w http.ResponseWriter, r *http.Request) {
	canon, u := h.requireOwnedULID(w, r)
	if u == nil {
		return
	}
	dk, err := h.deviceKeys.Get(r.Context(), canon)
	if errors.Is(err, integrations.ErrNoDeviceKey) {
		httpx.JSON(w, map[string]any{"ulid": canon, "pinned": false})
		return
	}
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not read device key")
		return
	}
	fp := sha256.Sum256(dk.PubKeyDER)
	httpx.JSON(w, map[string]any{
		"ulid":        canon,
		"pinned":      true,
		"algo":        dk.Algo,
		"fingerprint": hex.EncodeToString(fp[:8]),
		"pinned_at":   dk.CreatedAt.Format(time.RFC3339),
	})
}

// revokeDeviceKey un-pins the owned ULID's device key (rotation/revocation).
func (h *integrationsHandlers) revokeDeviceKey(w http.ResponseWriter, r *http.Request) {
	canon, u := h.requireOwnedULID(w, r)
	if u == nil {
		return
	}
	if err := h.deviceKeys.Delete(r.Context(), canon); err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not revoke device key")
		return
	}
	log.Printf("[integrations] account=%s revoked device key for ulid=%s", u.ID, canon)
	w.WriteHeader(http.StatusNoContent)
}

// connectableProvider reports whether provider has a session-gated OAuth connect
// flow (start/callback/status/disconnect). GCS is intentionally excluded: it has
// no connection of its own and is brokered via the Google grant (see mintToken).
func connectableProvider(provider string) bool {
	return provider == integrations.ProviderGoogle ||
		provider == integrations.ProviderDropbox ||
		provider == integrations.ProviderMicrosoft
}

// providerConfigured reports whether the OAuth env for provider is present.
func providerConfigured(provider string) bool {
	switch provider {
	case integrations.ProviderGoogle:
		return integrations.Configured()
	case integrations.ProviderDropbox:
		return integrations.DropboxConfigured()
	case integrations.ProviderMicrosoft:
		return integrations.MicrosoftConfigured()
	default:
		return false
	}
}

// start: 302 → provider consent. Session-gated so we know which account is
// connecting; the account is re-derived from the session at callback time.
func (h *integrationsHandlers) start(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	provider := r.PathValue("provider")
	if !connectableProvider(provider) {
		httpx.Err(w, http.StatusNotFound, "unknown integration provider")
		return
	}
	if !providerConfigured(provider) {
		httpx.Err(w, http.StatusServiceUnavailable, provider+" OAuth not configured")
		return
	}
	state, err := integrations.GenerateState(w, h.stateSecret, h.now())
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not start OAuth")
		return
	}
	url, err := h.svc.AuthCodeURLFor(provider, state)
	if err != nil {
		httpx.Err(w, http.StatusServiceUnavailable, provider+" OAuth not configured")
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// callback: verify CSRF state, exchange the code, store the encrypted refresh
// token under the session's account, then 302 back to the connected-accounts UI.
func (h *integrationsHandlers) callback(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	provider := r.PathValue("provider")
	if !connectableProvider(provider) {
		httpx.Err(w, http.StatusNotFound, "unknown integration provider")
		return
	}
	defer integrations.ClearStateCookie(w)

	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		// User denied consent or the provider returned an error.
		http.Redirect(w, r, "/app/account?integration="+provider+"&status=denied", http.StatusFound)
		return
	}
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		httpx.Err(w, http.StatusBadRequest, "missing code or state")
		return
	}
	if err := integrations.VerifyState(r, state, h.stateSecret, h.now()); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid OAuth state")
		return
	}

	if _, err := h.svc.Connect(r.Context(), u.ID, provider, code); err != nil {
		if errors.Is(err, integrations.ErrNoRefreshToken) {
			httpx.Err(w, http.StatusBadGateway, provider+" did not return a refresh token — please retry and grant offline access")
			return
		}
		httpx.Err(w, http.StatusBadGateway, "could not complete "+provider+" connection")
		return
	}
	log.Printf("[integrations] account=%s connected provider=%s", u.ID, provider)
	http.Redirect(w, r, "/app/account?integration="+provider+"&status=connected", http.StatusFound)
}

// status: connection status + display metadata for the caller.
func (h *integrationsHandlers) status(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	provider := r.PathValue("provider")
	if !connectableProvider(provider) {
		httpx.Err(w, http.StatusNotFound, "unknown integration provider")
		return
	}
	connected, c, err := h.svc.Status(r.Context(), u.ID, provider)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]any{
		"provider":   provider,
		"configured": providerConfigured(provider),
		"connected":  connected,
	}
	if connected {
		resp["email"] = c.AccountEmail
		resp["scopes"] = c.Scopes
		resp["connected_at"] = c.CreatedAt.Format(time.RFC3339)
	}
	httpx.JSON(w, resp)
}

// list: all of the caller's connections (connected-accounts UI).
func (h *integrationsHandlers) list(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	conns, err := h.svc.List(r.Context(), u.ID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
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
	httpx.JSON(w, out)
}

// disconnect: remove the caller's Google connection.
func (h *integrationsHandlers) disconnect(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	provider := r.PathValue("provider")
	if !connectableProvider(provider) {
		httpx.Err(w, http.StatusNotFound, "unknown integration provider")
		return
	}
	// Disconnect always removes the local credential; a non-nil error here means
	// provider-side revocation failed (transient) — log it but still return 204,
	// since the connection is gone from our store regardless.
	if err := h.svc.Disconnect(r.Context(), u.ID, provider); err != nil {
		log.Printf("[integrations] disconnect: provider revoke failed for account=%s provider=%s: %v", u.ID, provider, err)
	}
	log.Printf("[integrations] account=%s disconnected provider=%s", u.ID, provider)
	w.WriteHeader(http.StatusNoContent)
}
