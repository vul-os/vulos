package main

// routes_cdn.go — Settings -> Network -> CDN: box-owner BYO-CDN config
// (which vendor fronts this box) + an opt-in origin-firewall PREVIEW.
//
// Endpoints (all admin-only — see cdnRequireAdmin):
//
//	GET    /api/cdn/config              — current BYO-CDN config
//	PUT    /api/cdn/config               — set provider/origin/host-header/mtls/ssh-port/extra-ports
//	DELETE /api/cdn/config               — remove config (also turns the firewall off)
//	GET    /api/cdn/ranges?provider=...  — cached CDN egress CIDRs for one provider (or all, if omitted)
//	POST   /api/cdn/ranges/refresh       — trigger an out-of-band range refresh now
//	GET    /api/cdn/firewall             — computed status + freshly generated ruleset PREVIEW
//	POST   /api/cdn/firewall/enable      — turn the firewall on (step-up gated — see below)
//	POST   /api/cdn/firewall/disable     — turn the firewall off
//
// ── Live enforcement is NOT wired here ──────────────────────────────────────
// "Enabling" the firewall computes a real, valid nftables ruleset
// (services/cdn/firewall.go, BuildNFTRuleset) and RECORDS it via
// cdn.DryRunApplier — it does not shell out to `nft` or touch this box's
// actual packet filter. This box had no pre-existing firewall integration at
// all, and getting a first one's lockout-safety right deserves its own
// reviewed, hardware-tested change rather than riding along with everything
// else in this port. See services/cdn/firewall.go's package doc for the full
// reasoning and the Applier seam a real implementation would plug into.
//
// Enable is still step-up gated, matching the bar for other
// destructive/sensitive owner actions (webhooks create/edit, KMS rotate) —
// once live enforcement lands, that gate is exactly where it needs to be;
// gating it now costs nothing and means the UX doesn't change out from under
// the owner later.
import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"

	"vulos/backend/services/auth"
	"vulos/backend/services/cdn"
	"vulos/backend/services/stepup"
)

// cdnConfigView is the API-safe config shape.
type cdnConfigView struct {
	Provider        string   `json:"provider"`
	OriginHost      string   `json:"origin_host"`
	HostHeader      string   `json:"host_header"`
	MTLSEnabled     bool     `json:"mtls_enabled"`
	FirewallEnabled bool     `json:"firewall_enabled"`
	SSHPort         int      `json:"ssh_port"`
	ExtraAllowPorts []int    `json:"extra_allow_ports"`
	LastRulesetAt   string   `json:"last_ruleset_at,omitempty"`
	CreatedAt       string   `json:"created_at,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

func cdnConfigViewOf(c cdn.Config) cdnConfigView {
	v := cdnConfigView{
		Provider:        string(c.Provider),
		OriginHost:      c.OriginHost,
		HostHeader:      c.HostHeader,
		MTLSEnabled:     c.MTLSEnabled,
		FirewallEnabled: c.FirewallEnabled,
		SSHPort:         c.SSHPort,
		ExtraAllowPorts: c.ExtraAllowPorts,
	}
	if !c.LastRulesetAt.IsZero() {
		v.LastRulesetAt = c.LastRulesetAt.Format(rfc3339)
	}
	if !c.CreatedAt.IsZero() {
		v.CreatedAt = c.CreatedAt.Format(rfc3339)
	}
	if !c.UpdatedAt.IsZero() {
		v.UpdatedAt = c.UpdatedAt.Format(rfc3339)
	}
	return v
}

type cdnIPRangeView struct {
	CIDR      string `json:"cidr"`
	FetchedAt string `json:"fetched_at"`
}

// registerCDNRoutes wires the box-owner BYO-CDN + origin-firewall-preview
// surface onto mux.
//
// Opens (or creates) the cdn SQLite store under <home>/db and starts the
// background IP-range refresher. On open failure it logs and registers
// nothing — the feature is simply unavailable rather than crashing the box
// on a corrupt/unwritable store (matches registerWebhooksRoutes).
func registerCDNRoutes(mux *http.ServeMux, authStore *auth.Store, home string) {
	dbDir := filepath.Join(home, "db")
	store, err := cdn.OpenStore(dbDir)
	if err != nil {
		log.Printf("[cdn] DISABLED: could not open store: %v", err)
		return
	}

	fetcher := cdn.NewRangeFetcher(store)
	// Background range refresh: seeds on startup, then every
	// cdn.DefaultRefreshInterval. Runs for the lifetime of the process; there
	// is no explicit shutdown hook in this codebase's register* pattern
	// (matches webhooks' dispatcher, which starts its own drain goroutine off
	// context.Background() the same way — see webhooks.NewDispatcher).
	go fetcher.RunRefresher(context.Background(), cdn.DefaultRefreshInterval)

	applier := &cdn.DryRunApplier{Store: store}

	mux.HandleFunc("GET /api/cdn/config", func(w http.ResponseWriter, r *http.Request) {
		if !cdnRequireAdmin(w, r, authStore) {
			return
		}
		cfg, err := store.GetConfig(r.Context())
		if errors.Is(err, cdn.ErrNotFound) {
			writeJSON(w, cdnConfigViewOf(cdn.Config{}))
			return
		}
		if err != nil {
			log.Printf("[cdn] get config: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not load CDN config")
			return
		}
		writeJSON(w, cdnConfigViewOf(cfg))
	})

	mux.HandleFunc("PUT /api/cdn/config", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := cdnRequireAdminUser(w, r, authStore)
		if !ok {
			return
		}
		var body struct {
			Provider        string `json:"provider"`
			OriginHost      string `json:"origin_host"`
			HostHeader      string `json:"host_header"`
			MTLSEnabled     bool   `json:"mtls_enabled"`
			SSHPort         int    `json:"ssh_port"`
			ExtraAllowPorts []int  `json:"extra_allow_ports"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		provider := cdn.Provider(body.Provider)
		if !cdn.ValidProvider(provider) {
			writeErr(w, http.StatusBadRequest, cdn.ErrInvalidProvider.Error())
			return
		}
		if body.OriginHost == "" {
			writeErr(w, http.StatusBadRequest, cdn.ErrOriginRequired.Error())
			return
		}
		if len(body.ExtraAllowPorts) > 32 {
			writeErr(w, http.StatusBadRequest, "too many extra_allow_ports (max 32)")
			return
		}

		// Preserve the existing firewall-enabled/last-ruleset state — this
		// endpoint edits the CDN vendor selection, not the firewall opt-in
		// (that's POST /api/cdn/firewall/enable|disable, deliberately
		// separate and step-up gated).
		existing, err := store.GetConfig(r.Context())
		if err != nil && !errors.Is(err, cdn.ErrNotFound) {
			log.Printf("[cdn] put config: load existing: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not load CDN config")
			return
		}

		cfg := cdn.Config{
			Provider:        provider,
			OriginHost:      body.OriginHost,
			HostHeader:      body.HostHeader,
			MTLSEnabled:     body.MTLSEnabled,
			SSHPort:         body.SSHPort,
			ExtraAllowPorts: body.ExtraAllowPorts,
			FirewallEnabled: existing.FirewallEnabled,
			LastRuleset:     existing.LastRuleset,
			LastRulesetAt:   existing.LastRulesetAt,
		}
		if err := store.SetConfig(r.Context(), cfg); err != nil {
			log.Printf("[cdn] put config: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not save CDN config")
			return
		}
		log.Printf("[cdn] CONFIG SET provider=%s origin=%s user=%s", provider, body.OriginHost, userID)
		saved, err := store.GetConfig(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not reload CDN config")
			return
		}
		writeJSON(w, cdnConfigViewOf(saved))
	})

	mux.HandleFunc("DELETE /api/cdn/config", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := cdnRequireAdminUser(w, r, authStore)
		if !ok {
			return
		}
		if err := applier.Disable(r.Context()); err != nil {
			log.Printf("[cdn] delete config: disable firewall: %v", err)
		}
		if err := store.DeleteConfig(r.Context()); errors.Is(err, cdn.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no CDN config to delete")
			return
		} else if err != nil {
			log.Printf("[cdn] delete config: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not delete CDN config")
			return
		}
		log.Printf("[cdn] CONFIG DELETED user=%s", userID)
		writeJSON(w, map[string]string{"status": "deleted"})
	})

	mux.HandleFunc("GET /api/cdn/ranges", func(w http.ResponseWriter, r *http.Request) {
		if !cdnRequireAdmin(w, r, authStore) {
			return
		}
		providers := cdn.AllProviders
		if q := r.URL.Query().Get("provider"); q != "" {
			p := cdn.Provider(q)
			if !cdn.ValidProvider(p) {
				writeErr(w, http.StatusBadRequest, cdn.ErrInvalidProvider.Error())
				return
			}
			providers = []cdn.Provider{p}
		}
		out := map[string][]cdnIPRangeView{}
		for _, p := range providers {
			ranges, err := store.GetIPRanges(r.Context(), p)
			if err != nil {
				log.Printf("[cdn] get ranges %s: %v", p, err)
				writeErr(w, http.StatusInternalServerError, "could not load CDN IP ranges")
				return
			}
			views := make([]cdnIPRangeView, 0, len(ranges))
			for _, rg := range ranges {
				views = append(views, cdnIPRangeView{CIDR: rg.CIDR, FetchedAt: rg.FetchedAt.Format(rfc3339)})
			}
			out[string(p)] = views
		}
		writeJSON(w, map[string]any{"ranges": out})
	})

	mux.HandleFunc("POST /api/cdn/ranges/refresh", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := cdnRequireAdminUser(w, r, authStore)
		if !ok {
			return
		}
		// Synchronous on-demand refresh — small, bounded (three HTTP GETs,
		// 15s client timeout each, see cdn.NewRangeFetcher) — acceptable to
		// run inline rather than fire-and-forget so the caller can show the
		// updated counts immediately.
		fetcher.RefreshAll(r.Context())
		log.Printf("[cdn] RANGES REFRESHED (manual) user=%s", userID)
		writeJSON(w, map[string]string{"status": "refreshed"})
	})

	mux.HandleFunc("GET /api/cdn/firewall", func(w http.ResponseWriter, r *http.Request) {
		if !cdnRequireAdmin(w, r, authStore) {
			return
		}
		cfg, err := store.GetConfig(r.Context())
		if err != nil && !errors.Is(err, cdn.ErrNotFound) {
			log.Printf("[cdn] firewall status: load config: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not load CDN config")
			return
		}
		var ranges []cdn.IPRange
		if cdn.ValidProvider(cfg.Provider) {
			ranges, err = store.GetIPRanges(r.Context(), cfg.Provider)
			if err != nil {
				log.Printf("[cdn] firewall status: load ranges: %v", err)
				writeErr(w, http.StatusInternalServerError, "could not load CDN IP ranges")
				return
			}
		}
		writeJSON(w, cdn.BuildStatus(cfg, ranges))
	})

	mux.HandleFunc("POST /api/cdn/firewall/enable", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := cdnRequireAdminUser(w, r, authStore)
		if !ok {
			return
		}
		// Step-up: this is the one genuinely lockout-capable action in this
		// package (even in dry-run form it flips the flag the owner will
		// later rely on to mean "this is what's protecting me"), so it gets
		// the same freshly-elevated-session bar as other destructive/
		// sensitive owner actions (see backend/services/stepup).
		if err := stepup.Require(r); err != nil {
			writeErr(w, http.StatusForbidden, "step-up verification required: "+err.Error())
			return
		}
		cfg, err := store.GetConfig(r.Context())
		if errors.Is(err, cdn.ErrNotFound) {
			writeErr(w, http.StatusBadRequest, "configure a CDN provider first (PUT /api/cdn/config)")
			return
		}
		if err != nil {
			log.Printf("[cdn] firewall enable: load config: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not load CDN config")
			return
		}
		if !cfg.IsConfigured() {
			writeErr(w, http.StatusBadRequest, "configure a CDN provider first (PUT /api/cdn/config)")
			return
		}
		ranges, err := store.GetIPRanges(r.Context(), cfg.Provider)
		if err != nil {
			log.Printf("[cdn] firewall enable: load ranges: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not load CDN IP ranges")
			return
		}
		spec := cdn.ResolveRulesetSpec(cfg, ranges)
		ruleset, err := cdn.BuildNFTRuleset(spec)
		if err != nil {
			// Includes cdn.ErrEmptyCIDRs — the fail-open case: refuse to
			// "enable" a firewall that would have nothing to allow through
			// on 80/443. The owner should retry after a successful range
			// refresh.
			writeErr(w, http.StatusConflict, "cannot enable: "+err.Error())
			return
		}
		if err := applier.Apply(r.Context(), cfg.Provider, ruleset); err != nil {
			log.Printf("[cdn] firewall enable: apply: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not enable CDN firewall")
			return
		}
		log.Printf("[cdn] FIREWALL ENABLED (dry-run only — see routes_cdn.go doc) provider=%s cidrs=%d user=%s",
			cfg.Provider, len(spec.CIDRs), userID)
		updated, err := store.GetConfig(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not reload CDN config")
			return
		}
		var updatedRanges []cdn.IPRange
		updatedRanges, _ = store.GetIPRanges(r.Context(), updated.Provider)
		writeJSON(w, map[string]any{
			"status": cdn.BuildStatus(updated, updatedRanges),
			"note":   "dry run only: this box does not yet apply live nftables rules — the ruleset above was generated and recorded, not installed",
		})
	})

	mux.HandleFunc("POST /api/cdn/firewall/disable", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := cdnRequireAdminUser(w, r, authStore)
		if !ok {
			return
		}
		// Disabling narrows nothing and cannot itself cause a lockout, so no
		// step-up requirement here — matches webhooks' delete/rotate.
		if err := applier.Disable(r.Context()); err != nil {
			log.Printf("[cdn] firewall disable: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not disable CDN firewall")
			return
		}
		log.Printf("[cdn] FIREWALL DISABLED user=%s", userID)
		writeJSON(w, map[string]string{"status": "disabled"})
	})

	log.Printf("[cdn] registered /api/cdn (owner-only; live firewall enforcement is DEFERRED — dry-run/preview only)")
}

// cdnRequireAdmin authorises an owner-only CDN request. A nil auth store
// (degraded boot) is treated as "no admin exists" and refuses — never as
// "everyone is admin". Mirrors whRequireAdmin (routes_webhooks.go).
func cdnRequireAdmin(w http.ResponseWriter, r *http.Request, authStore *auth.Store) bool {
	_, ok := cdnRequireAdminUser(w, r, authStore)
	return ok
}

// cdnRequireAdminUser is cdnRequireAdmin plus the verified caller's user id,
// for handlers that want it for audit logging.
func cdnRequireAdminUser(w http.ResponseWriter, r *http.Request, authStore *auth.Store) (string, bool) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	if authStore == nil {
		writeErr(w, http.StatusForbidden, "admin only")
		return "", false
	}
	p, ok := authStore.GetProfile(userID)
	if !ok || p == nil || p.Role != auth.RoleAdmin {
		writeErr(w, http.StatusForbidden, "admin only")
		return "", false
	}
	return userID, true
}
