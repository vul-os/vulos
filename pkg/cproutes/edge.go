// edge.go — WEBAPP-04 Vulos edge cache control-plane API.
//
// Routes registered:
//
//	Admin (ADMIN_ACCOUNT_ID-gated):
//	  PUT  /api/edge/nodes/{id}           — upsert edge PoP node
//	  GET  /api/edge/nodes                — list all edge nodes
//	  POST /api/edge/nodes/{id}/drain     — drain a node
//	  POST /api/edge/nodes/{id}/undrain   — undrain a node
//
//	Per-tenant (session-gated):
//	  GET  /api/edge/config?ulid=         — list domain configs for a ULID
//	  PUT  /api/edge/config               — set (create/update) a domain config
//	  DELETE /api/edge/config?ulid=&domain= — remove a domain config
//
//	Edge nodes (shared secret — EDGE_INGEST_SECRET):
//	  POST /api/edge/bandwidth            — ingest bandwidth metering event
//
//	Per-tenant bandwidth view (session-gated):
//	  GET  /api/edge/bandwidth?ulid=      — list bandwidth events for a ULID
//	  GET  /api/edge/bandwidth/total?ulid=&since= — totals for a ULID since a date
package cproutes

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/edge"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// RegisterEdge wires all WEBAPP-04 edge control-plane HTTP routes.
//
// Parameters:
//
//	st             — edge store (MemStore or SQLStore)
//	authStore      — session-based caller authentication
//	fleetStore     — device ownership checks
//	adminAccountID — gates admin-only node management endpoints
func RegisterEdge(
	mux *http.ServeMux,
	st edge.Store,
	authStore *auth.Store,
	fleetStore fleet.Store,
	adminAccountID string,
) {
	h := &edgeHandlers{
		store:          st,
		auth:           authStore,
		fleet:          fleetStore,
		adminAccountID: adminAccountID,
	}

	// Admin: PoP node management
	mux.HandleFunc("PUT /api/edge/nodes/{id}", h.upsertNode)
	mux.HandleFunc("GET /api/edge/nodes", h.listNodes)
	mux.HandleFunc("POST /api/edge/nodes/{id}/drain", h.drainNode)
	mux.HandleFunc("POST /api/edge/nodes/{id}/undrain", h.undrainNode)

	// Per-tenant: domain config
	mux.HandleFunc("GET /api/edge/config", h.listDomainConfigs)
	mux.HandleFunc("PUT /api/edge/config", h.setDomainConfig)
	mux.HandleFunc("DELETE /api/edge/config", h.deleteDomainConfig)

	// Edge-node ingest: bandwidth metering (shared secret)
	mux.HandleFunc("POST /api/edge/bandwidth", h.ingestBandwidth)

	// Per-tenant: bandwidth views
	mux.HandleFunc("GET /api/edge/bandwidth", h.listBandwidthEvents)
	mux.HandleFunc("GET /api/edge/bandwidth/total", h.bandwidthTotal)
}

// edgeHandlers holds dependencies for edge HTTP handlers.
type edgeHandlers struct {
	store          edge.Store
	auth           *auth.Store
	fleet          fleet.Store
	adminAccountID string
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin: PoP node management
// ─────────────────────────────────────────────────────────────────────────────

// PUT /api/edge/nodes/{id}
// Body: { "region": "us-east", "origin_url": "https://…", "drained": false }
func (h *edgeHandlers) upsertNode(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	if u.ID != h.adminAccountID {
		httpx.Err(w, http.StatusForbidden, "admin only")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpx.Err(w, http.StatusBadRequest, "node id required")
		return
	}
	var req struct {
		Region    string `json:"region"`
		OriginURL string `json:"origin_url"`
		Drained   bool   `json:"drained"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	n := edge.Node{
		ID:        id,
		Region:    req.Region,
		OriginURL: req.OriginURL,
		Drained:   req.Drained,
	}
	got, err := h.store.UpsertNode(r.Context(), n)
	if err != nil {
		edgeErr(w, err)
		return
	}
	httpx.JSON(w, got)
}

// GET /api/edge/nodes
func (h *edgeHandlers) listNodes(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	if u.ID != h.adminAccountID {
		httpx.Err(w, http.StatusForbidden, "admin only")
		return
	}
	nodes, err := h.store.ListNodes(r.Context())
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	if nodes == nil {
		nodes = []edge.Node{}
	}
	httpx.JSON(w, map[string]any{"nodes": nodes})
}

// POST /api/edge/nodes/{id}/drain
func (h *edgeHandlers) drainNode(w http.ResponseWriter, r *http.Request) {
	h.setNodeDrain(w, r, true)
}

// POST /api/edge/nodes/{id}/undrain
func (h *edgeHandlers) undrainNode(w http.ResponseWriter, r *http.Request) {
	h.setNodeDrain(w, r, false)
}

func (h *edgeHandlers) setNodeDrain(w http.ResponseWriter, r *http.Request, drain bool) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	if u.ID != h.adminAccountID {
		httpx.Err(w, http.StatusForbidden, "admin only")
		return
	}
	id := r.PathValue("id")
	var err error
	if drain {
		err = h.store.DrainNode(r.Context(), id)
	} else {
		err = h.store.UndrainNode(r.Context(), id)
	}
	if err != nil {
		edgeErr(w, err)
		return
	}
	n, _ := h.store.GetNode(r.Context(), id)
	httpx.JSON(w, n)
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-tenant: domain config
// ─────────────────────────────────────────────────────────────────────────────

// GET /api/edge/config?ulid=
func (h *edgeHandlers) listDomainConfigs(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	ulidParam := r.URL.Query().Get("ulid")
	if ulidParam == "" {
		httpx.Err(w, http.StatusBadRequest, "ulid is required")
		return
	}
	canon, err := routing.CanonULID(ulidParam)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidULID.Error())
		return
	}
	if !h.isOwnerOrAdmin(w, r, u.ID, canon) {
		return
	}
	configs, err := h.store.ListDomainConfigs(r.Context(), canon)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	if configs == nil {
		configs = []edge.DomainConfig{}
	}
	httpx.JSON(w, map[string]any{"configs": configs})
}

// PUT /api/edge/config
// Body: { "ulid": "…", "domain": "…", "cache_ttl_sec": 300, "rate_limit_rps": 100, "enabled": true }
func (h *edgeHandlers) setDomainConfig(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	var req struct {
		ULID         string `json:"ulid"`
		Domain       string `json:"domain"`
		CacheTTLSec  int    `json:"cache_ttl_sec"`
		RateLimitRPS int    `json:"rate_limit_rps"`
		Enabled      bool   `json:"enabled"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.ULID == "" || req.Domain == "" {
		httpx.Err(w, http.StatusBadRequest, "ulid and domain are required")
		return
	}
	canon, err := routing.CanonULID(req.ULID)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidULID.Error())
		return
	}
	if !h.isOwnerOrAdmin(w, r, u.ID, canon) {
		return
	}
	// Apply defaults when caller omits TTL/RPS
	if req.CacheTTLSec == 0 {
		req.CacheTTLSec = edge.DefaultCacheTTLSec
	}
	if req.RateLimitRPS == 0 {
		req.RateLimitRPS = edge.DefaultRateLimitRPS
	}
	cfg := edge.DomainConfig{
		ULID:         canon,
		Domain:       req.Domain,
		CacheTTLSec:  req.CacheTTLSec,
		RateLimitRPS: req.RateLimitRPS,
		Enabled:      req.Enabled,
	}
	got, err := h.store.SetDomainConfig(r.Context(), cfg)
	if err != nil {
		edgeErr(w, err)
		return
	}
	httpx.JSON(w, got)
}

// DELETE /api/edge/config?ulid=&domain=
func (h *edgeHandlers) deleteDomainConfig(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	ulidParam := r.URL.Query().Get("ulid")
	domain := r.URL.Query().Get("domain")
	if ulidParam == "" || domain == "" {
		httpx.Err(w, http.StatusBadRequest, "ulid and domain are required")
		return
	}
	canon, err := routing.CanonULID(ulidParam)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidULID.Error())
		return
	}
	if !h.isOwnerOrAdmin(w, r, u.ID, canon) {
		return
	}
	if err := h.store.DeleteDomainConfig(r.Context(), canon, domain); err != nil {
		edgeErr(w, err)
		return
	}
	httpx.JSON(w, map[string]any{"ok": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// Edge-node ingest: bandwidth metering
// ─────────────────────────────────────────────────────────────────────────────

// POST /api/edge/bandwidth
// Auth: X-Edge-Secret header must match EDGE_INGEST_SECRET env var.
// Body: { "account_id": "…", "ulid": "…", "domain": "…",
//
//	"bytes_in": 1234, "bytes_out": 5678,
//	"period_start": "…", "period_end": "…" }
func (h *edgeHandlers) ingestBandwidth(w http.ResponseWriter, r *http.Request) {
	// RISK-SEC-01: source via the SecretsProvider (env default; KMS-ready).
	// M1: constant-time comparison to avoid leaking the secret via timing.
	secret := secretOrEnv(r.Context(), "EDGE_INGEST_SECRET")
	provided := r.Header.Get("X-Edge-Secret")
	if secret == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
		httpx.Err(w, http.StatusUnauthorized, "invalid edge ingest secret")
		return
	}
	var req struct {
		AccountID   string `json:"account_id"`
		ULID        string `json:"ulid"`
		Domain      string `json:"domain"`
		BytesIn     int64  `json:"bytes_in"`
		BytesOut    int64  `json:"bytes_out"`
		PeriodStart string `json:"period_start"` // RFC3339
		PeriodEnd   string `json:"period_end"`   // RFC3339
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	// M1: bind the body's ulid → account_id so a (compromised or buggy) edge
	// node cannot attribute another tenant's bandwidth to a victim. The device
	// row is the source of truth for ULID ownership.
	if req.ULID != "" && req.AccountID != "" {
		canon, cerr := routing.CanonULID(req.ULID)
		if cerr != nil {
			httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidULID.Error())
			return
		}
		dev, derr := h.fleet.GetDevice(r.Context(), canon)
		if derr != nil || dev.AccountID != req.AccountID {
			httpx.Err(w, http.StatusForbidden, "ulid does not belong to account_id")
			return
		}
		req.ULID = canon
	}
	ps, err := time.Parse(time.RFC3339, req.PeriodStart)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid period_start: "+err.Error())
		return
	}
	pe, err := time.Parse(time.RFC3339, req.PeriodEnd)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid period_end: "+err.Error())
		return
	}
	ev := edge.BandwidthEvent{
		AccountID:   req.AccountID,
		ULID:        req.ULID,
		Domain:      req.Domain,
		BytesIn:     req.BytesIn,
		BytesOut:    req.BytesOut,
		PeriodStart: ps,
		PeriodEnd:   pe,
	}
	got, err := h.store.EmitBandwidthEvent(r.Context(), ev)
	if err != nil {
		edgeErr(w, err)
		return
	}
	httpx.JSON(w, got)
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-tenant: bandwidth views
// ─────────────────────────────────────────────────────────────────────────────

// GET /api/edge/bandwidth?ulid=&limit=
func (h *edgeHandlers) listBandwidthEvents(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	ulidParam := r.URL.Query().Get("ulid")
	if ulidParam == "" {
		httpx.Err(w, http.StatusBadRequest, "ulid is required")
		return
	}
	canon, err := routing.CanonULID(ulidParam)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidULID.Error())
		return
	}
	if !h.isOwnerOrAdmin(w, r, u.ID, canon) {
		return
	}
	limit := 50
	if ls := r.URL.Query().Get("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 {
			limit = n
		}
	}
	events, err := h.store.ListBandwidthEvents(r.Context(), u.ID, limit)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []edge.BandwidthEvent{}
	}
	httpx.JSON(w, map[string]any{"events": events})
}

// GET /api/edge/bandwidth/total?ulid=&since=
// since is an optional RFC3339 timestamp; defaults to 30 days ago.
func (h *edgeHandlers) bandwidthTotal(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	ulidParam := r.URL.Query().Get("ulid")
	if ulidParam == "" {
		httpx.Err(w, http.StatusBadRequest, "ulid is required")
		return
	}
	canon, err := routing.CanonULID(ulidParam)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidULID.Error())
		return
	}
	if !h.isOwnerOrAdmin(w, r, u.ID, canon) {
		return
	}
	since := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if ss := r.URL.Query().Get("since"); ss != "" {
		if t, err := time.Parse(time.RFC3339, ss); err == nil {
			since = t
		}
	}
	tot, err := h.store.BandwidthTotal(r.Context(), canon, since)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, map[string]any{
		"ulid":      canon,
		"since":     since.Format(time.RFC3339),
		"bytes_in":  tot.BytesIn,
		"bytes_out": tot.BytesOut,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Authz helper
// ─────────────────────────────────────────────────────────────────────────────

func (h *edgeHandlers) isOwnerOrAdmin(w http.ResponseWriter, r *http.Request, callerID, ulid string) bool {
	dev, err := h.fleet.GetDevice(r.Context(), ulid)
	if err != nil {
		httpx.Err(w, http.StatusForbidden, "not owner or device not found")
		return false
	}
	if dev.AccountID == callerID {
		return true
	}
	if dev.OrgID != "" {
		role, roleErr := h.fleet.MemberRole(r.Context(), dev.OrgID, callerID)
		if roleErr == nil && (role == "owner" || role == "admin") {
			return true
		}
	}
	httpx.Err(w, http.StatusForbidden, "not owner or org admin")
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Error mapper
// ─────────────────────────────────────────────────────────────────────────────

func edgeErr(w http.ResponseWriter, err error) {
	switch err {
	case edge.ErrNodeNotFound, edge.ErrDomainNotFound:
		httpx.Err(w, http.StatusNotFound, err.Error())
	case edge.ErrInvalidConfig:
		httpx.Err(w, http.StatusBadRequest, err.Error())
	default:
		httpx.Err(w, http.StatusInternalServerError, err.Error())
	}
}
