// cdn.go — BYO-CDN HTTP routes for the vulos.cloud control-plane.
//
// Implements WEBAPP-05: customers connect their own CDN (Cloudflare/Fastly/Bunny)
// and the control-plane manages:
//   - per-account CDN config (provider, origin host, mTLS, host-header)
//   - cached CDN egress IP ranges (auto-refreshed every 4 h)
//   - firewall rule set returned to the OS agent for origin-firewall enforcement
//
// Routes registered:
//
//	GET  /api/cdn/config                — get caller's BYO-CDN config (Pro+)
//	PUT  /api/cdn/config                — create/update BYO-CDN config (Pro+)
//	DELETE /api/cdn/config              — remove BYO-CDN config (Pro+)
//	GET  /api/cdn/firewall-rules        — get effective firewall rule set for caller
//	POST /api/cdn/admin/refresh-ranges  — admin: trigger immediate IP-range refresh
//
// Authz: all customer endpoints require a valid session + Pro-or-higher tier.
// The firewall-rules endpoint is also callable by the OS agent via
// X-Device-Auth: <CP_SHARED_SECRET> with ?account_id= query param.
// The admin refresh endpoint requires X-Admin-Auth: <CP_SHARED_SECRET>.
//
// Tier gate: CDN_ALLOW_FREE=1 bypasses the Pro gate in dev/test.
package cproutes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/billingport"
	"github.com/vul-os/vulos-management/pkg/cdn"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// RegisterCDN wires BYO-CDN endpoints into mux.
//
// entitlements is the billing seam used for the Pro-or-higher tier gate
// (billingport.EntitlementResolver.EffectiveTierFor). validSharedSecret verifies
// the CP shared secret (current OR previous) presented by the OS agent / admin
// caller; supply a resolver backed by the SecretsProvider (validSharedSecretEither).
func RegisterCDN(
	mux *http.ServeMux,
	st cdn.Store,
	fetcher *cdn.RangeFetcher,
	authStore *auth.Store,
	entitlements billingport.EntitlementResolver,
	ownerResolver fleet.OwnershipResolver,
	validSharedSecret func(ctx context.Context, presented string) bool,
) {
	h := &cdnHandlers{
		st:                st,
		fetcher:           fetcher,
		auth:              authStore,
		entitlements:      entitlements,
		owner:             ownerResolver,
		validSharedSecret: validSharedSecret,
	}
	mux.HandleFunc("GET /api/cdn/config", h.getConfig)
	mux.HandleFunc("PUT /api/cdn/config", h.setConfig)
	mux.HandleFunc("DELETE /api/cdn/config", h.deleteConfig)
	mux.HandleFunc("GET /api/cdn/firewall-rules", h.getFirewallRules)
	mux.HandleFunc("POST /api/cdn/admin/refresh-ranges", h.adminRefreshRanges)
}

// cdnHandlers holds dependencies for BYO-CDN HTTP handlers.
type cdnHandlers struct {
	st                cdn.Store
	fetcher           *cdn.RangeFetcher
	auth              *auth.Store
	entitlements      billingport.EntitlementResolver
	owner             fleet.OwnershipResolver
	validSharedSecret func(ctx context.Context, presented string) bool
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/cdn/config
// ─────────────────────────────────────────────────────────────────────────────

func (h *cdnHandlers) getConfig(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	if !h.requirePro(w, r, u.ID) {
		return
	}

	cfg, err := h.st.GetConfig(r.Context(), u.ID)
	if errors.Is(err, cdn.ErrNotFound) {
		httpx.Err(w, http.StatusNotFound, "no BYO-CDN config found")
		return
	}
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, cfg)
}

// ─────────────────────────────────────────────────────────────────────────────
// PUT /api/cdn/config
// ─────────────────────────────────────────────────────────────────────────────

type setCDNConfigRequest struct {
	Provider    string `json:"provider"`     // cloudflare|fastly|bunny
	OriginHost  string `json:"origin_host"`  // e.g. origin-<cust>.vulos.org
	MTLSEnabled *bool  `json:"mtls_enabled"` // defaults to true
	HostHeader  string `json:"host_header"`  // Host to enforce; defaults to origin_host
	Enabled     *bool  `json:"enabled"`      // defaults to true
}

func (h *cdnHandlers) setConfig(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	if !h.requirePro(w, r, u.ID) {
		return
	}

	var req setCDNConfigRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}

	provider := cdn.Provider(req.Provider)
	if !cdn.ValidProvider(provider) {
		httpx.Err(w, http.StatusBadRequest, cdn.ErrInvalidProvider.Error())
		return
	}
	if req.OriginHost == "" {
		httpx.Err(w, http.StatusBadRequest, cdn.ErrOriginRequired.Error())
		return
	}

	// Defaults.
	mtls := true
	if req.MTLSEnabled != nil {
		mtls = *req.MTLSEnabled
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	hostHeader := req.HostHeader
	if hostHeader == "" {
		hostHeader = req.OriginHost
	}

	cfg := cdn.Config{
		AccountID:   u.ID,
		Provider:    provider,
		OriginHost:  req.OriginHost,
		MTLSEnabled: mtls,
		HostHeader:  hostHeader,
		Enabled:     enabled,
	}
	if err := h.st.SetConfig(r.Context(), cfg); err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return the stored config.
	stored, err := h.st.GetConfig(r.Context(), u.ID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, stored)
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /api/cdn/config
// ─────────────────────────────────────────────────────────────────────────────

func (h *cdnHandlers) deleteConfig(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	if !h.requirePro(w, r, u.ID) {
		return
	}

	if err := h.st.DeleteConfig(r.Context(), u.ID); errors.Is(err, cdn.ErrNotFound) {
		httpx.Err(w, http.StatusNotFound, "no BYO-CDN config found")
		return
	} else if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, map[string]any{"ok": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/cdn/firewall-rules
// ─────────────────────────────────────────────────────────────────────────────
//
// Two auth modes:
//  1. Session-based: caller's own account (no query param needed).
//  2. OS agent: X-Device-Auth: <CP_SHARED_SECRET> with ?account_id=<id>.

func (h *cdnHandlers) getFirewallRules(w http.ResponseWriter, r *http.Request) {
	accountID, ok := h.resolveFirewallAccount(w, r)
	if !ok {
		return
	}

	rules, err := cdn.BuildFirewallRules(r.Context(), h.st, accountID)
	if errors.Is(err, cdn.ErrNotFound) {
		httpx.Err(w, http.StatusNotFound, "BYO CDN not configured or disabled for this account")
		return
	}
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, rules)
}

// resolveFirewallAccount returns the account ID to use for the firewall-rules
// endpoint, honouring both session-based and device-agent auth.
func (h *cdnHandlers) resolveFirewallAccount(w http.ResponseWriter, r *http.Request) (string, bool) {
	// OS agent path: X-Device-Auth header + account_id query param.
	// RISK-SEC-01: source via the SecretsProvider (env default; KMS-ready).
	// SECRET-ROTATE-01: accept the current OR previous CP_SHARED_SECRET.
	if h.validSharedSecret != nil && h.validSharedSecret(r.Context(), r.Header.Get("X-Device-Auth")) {
		// CROSS-ACCOUNT FIX (item 8): X-Device-Auth is the fleet-global secret, so a
		// client-supplied account_id let any box pull ANY account's firewall rules.
		// Bind to the device's OWN account by resolving its ULID. FAIL CLOSED.
		if h.owner == nil {
			httpx.Err(w, http.StatusServiceUnavailable, "ownership resolver unavailable")
			return "", false
		}
		ulid := r.Header.Get("X-Device-ULID")
		if ulid == "" {
			httpx.Err(w, http.StatusBadRequest, "X-Device-ULID is required")
			return "", false
		}
		accountID, err := h.owner.OwnerOf(r.Context(), ulid)
		if err != nil || accountID == "" {
			httpx.Err(w, http.StatusForbidden, "device not enrolled")
			return "", false
		}
		if q := r.URL.Query().Get("account_id"); q != "" && q != accountID {
			httpx.Err(w, http.StatusForbidden, "account_id does not match authenticated device")
			return "", false
		}
		return accountID, true
	}

	// Session-based path.
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return "", false
	}
	return u.ID, true
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/cdn/admin/refresh-ranges
// ─────────────────────────────────────────────────────────────────────────────

func (h *cdnHandlers) adminRefreshRanges(w http.ResponseWriter, r *http.Request) {
	// RISK-SEC-01: source via the SecretsProvider (env default; KMS-ready).
	// SECRET-ROTATE-01: accept the current OR previous CP_SHARED_SECRET.
	if h.validSharedSecret == nil || !h.validSharedSecret(r.Context(), r.Header.Get("X-Admin-Auth")) {
		httpx.Err(w, http.StatusUnauthorized, "invalid admin auth")
		return
	}

	// Run the refresh synchronously so the response reflects the outcome.
	h.fetcher.RefreshAll(r.Context())
	auditRecord(r.Context(), "system", "cdn.refresh_ranges", "all", nil)

	// Summarise what is now cached.
	type providerCount struct {
		Provider string `json:"provider"`
		Count    int    `json:"count"`
	}
	var summary []providerCount
	for _, p := range []cdn.Provider{cdn.ProviderCloudflare, cdn.ProviderFastly, cdn.ProviderBunny} {
		ranges, err := h.st.GetIPRanges(r.Context(), p)
		count := 0
		if err == nil {
			count = len(ranges)
		}
		summary = append(summary, providerCount{Provider: string(p), Count: count})
	}
	httpx.JSON(w, map[string]any{"ok": true, "providers": summary})
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier gate helper
// ─────────────────────────────────────────────────────────────────────────────

// requirePro checks that the account is on Pro or higher tier.
// Returns false (and writes 403) if the gate fails.
// CDN_ALLOW_FREE=1 bypasses the gate in dev/test.
func (h *cdnHandlers) requirePro(w http.ResponseWriter, r *http.Request, accountID string) bool {
	if os.Getenv("CDN_ALLOW_FREE") == "1" {
		return true
	}
	// Effective (dunning-aware) tier: a payment-suspended account drops to free
	// and loses the CDN paid-feature entitlement until it recovers.
	tier, err := h.entitlements.EffectiveTierFor(r.Context(), accountID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError,
			fmt.Sprintf("billing tier lookup failed: %v", err))
		return false
	}
	if tier == "free" {
		httpx.Err(w, http.StatusForbidden, cdn.ErrNotPro.Error())
		return false
	}
	return true
}
