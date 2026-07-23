// Package publicapi implements the box's public REST API v1 — a small,
// versioned, bearer-token-authed surface at /api/v1/… that third-party
// tools and scripts can call against a single Vulos box, distinct from the
// browser session cookie the SPA uses.
//
// Ported from vulos-management's pkg/publicapi (the vulos.cloud CP's public
// REST API), adapted from a multi-tenant control-plane surface (account
// billing wallet, hosted mailboxes, enrolled devices, webhooks — all
// CP-owned concepts with no box-level equivalent) down to what a single
// self-hosted box can genuinely answer for itself: the owner's account
// profile, box-level usage, and (optionally) paired devices/instances.
// Billing/wallet and webhooks were NOT ported — see the fold's integration
// manifest NOTES for why.
//
// Auth: `Authorization: Bearer vkl_…` — a LOCALLY issued key from
// backend/internal/apikey's LocalStore (APIKEY-LOCAL-01), introspected
// in-process (never over the network). This is deliberately independent of
// the OS's session cookie AND of the CP-delegated "vk_…" entitlement keys
// (backend/internal/apikey's Introspector/cpIntrospector) — a public API
// caller is neither a browser session nor a cross-product entitlement
// claim, it is "this box's owner handed out a credential for this box's own
// API". See internal/apikey/store.go's package doc for the full rationale.
//
// Rate limit: 60 req/min per key (token-bucket, burst 60, lazy-evicted),
// ported near-verbatim from management/pkg/publicapi/store.go.
package publicapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/apikey"
	"vulos/backend/services/auth"
	"vulos/backend/services/disks"
)

// ─── Rate limiter (per key, 60 req/min) ────────────────────────────────────
// Ported near-verbatim from management/pkg/publicapi/store.go's tokenBucket
// / RateLimiter: pure stdlib, no new dependency.

type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	lastSeen time.Time
}

func (b *tokenBucket) allow(rps float64, burst int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.lastSeen = now
	b.tokens += elapsed * rps
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// RateLimiter enforces 60 req/min per API key with lazy eviction.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	lastGC  time.Time
}

// NewRateLimiter creates a RateLimiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{buckets: make(map[string]*tokenBucket)}
}

// Allow returns true if tokenID is within its rate limit (60 req/min) and
// consumes one unit of budget.
func (rl *RateLimiter) Allow(tokenID string) bool {
	const rps = 1.0  // 60/min sustained
	const burst = 60 // one minute's worth of burst

	rl.mu.Lock()
	now := time.Now()
	if now.Sub(rl.lastGC) > 5*time.Minute {
		for k, bkt := range rl.buckets {
			bkt.mu.Lock()
			idle := now.Sub(bkt.lastSeen)
			bkt.mu.Unlock()
			if idle > 5*time.Minute {
				delete(rl.buckets, k)
			}
		}
		rl.lastGC = now
	}
	bkt, ok := rl.buckets[tokenID]
	if !ok {
		bkt = &tokenBucket{tokens: float64(burst), lastSeen: now}
		rl.buckets[tokenID] = bkt
	}
	rl.mu.Unlock()
	return bkt.allow(rps, burst)
}

// ─── Response types ─────────────────────────────────────────────────────────

// Account is the /api/v1/account/me response shape.
type Account struct {
	AccountID   string `json:"account_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	// Products lists this key's CP-resolved product entitlements, when the
	// box is CP-enrolled (X-Vulos-Entitlements-Products; empty otherwise —
	// a locally issued key has no cross-product entitlement, it is simply
	// valid for this box's own API).
	Products  []string  `json:"products,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AccountUsage is the /api/v1/account/usage response shape. Ported from the
// CP's mailbox/device counters down to what a single box can report:
// storage usage (this box's mounted filesystems) and paired device count.
type AccountUsage struct {
	AccountID      string `json:"account_id"`
	DeviceCount    int    `json:"device_count"`
	StorageUsedMB  int64  `json:"storage_used_mb"`
	StorageTotalMB int64  `json:"storage_total_mb"`
}

// Device is a row in the /api/v1/devices list — a paired instance/box
// belonging to this account (see DeviceLister).
type Device struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

// DeviceLister lists the paired devices/instances for the account. Optional:
// when nil, GET /api/v1/devices returns an empty list rather than erroring
// (mirrors the CP handler's graceful-degradation pattern for unconfigured
// dependencies). Left uninjected in the initial wiring — see the fold's
// integration manifest NOTES for why (avoiding a second writer on the
// shared multiinstance registry's single-writer SQLite handle).
type DeviceLister interface {
	ListDevices(ctx context.Context, accountID string) ([]Device, error)
}

// ─── Handler ────────────────────────────────────────────────────────────────

// Handler is the public API v1 HTTP handler.
type Handler struct {
	authStore *auth.Store
	keys      *apikey.LocalStore
	devices   DeviceLister
	rl        *RateLimiter
}

// NewHandler creates a Handler. devices may be nil (see DeviceLister doc).
func NewHandler(authStore *auth.Store, keys *apikey.LocalStore, devices DeviceLister) *Handler {
	return &Handler{authStore: authStore, keys: keys, devices: devices, rl: NewRateLimiter()}
}

// ─── Auth + rate limit guard ────────────────────────────────────────────────

// authenticate extracts and validates the `Authorization: Bearer vkl_…`
// header against the local key store. Returns (ownerID, tokenID, true) on
// success; tokenID is the SHA-256 hash of the raw key (used as the
// rate-limit bucket key so raw secrets are never retained in memory).
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (ownerID, tokenID string, ok bool) {
	authHdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHdr, "Bearer "+apikey.LocalKeyPrefix) {
		writeAPIError(w, http.StatusUnauthorized, "missing or invalid Authorization header: expected 'Bearer vkl_…' — mint a key in Settings → Developer")
		return "", "", false
	}
	if h.keys == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "public API key store not configured")
		return "", "", false
	}
	raw := strings.TrimPrefix(authHdr, "Bearer ")
	res, err := h.keys.Introspect(r.Context(), raw)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "key validation failed")
		return "", "", false
	}
	if !res.Valid {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return "", "", false
	}
	sum := sha256.Sum256([]byte(raw))
	return res.Account, hex.EncodeToString(sum[:]), true
}

// guard runs authenticate then the 60 req/min rate limit. Returns the
// authenticated owner (local user) id, or ("", false) after already having
// written the error response.
func (h *Handler) guard(w http.ResponseWriter, r *http.Request) (ownerID string, ok bool) {
	ownerID, tokenID, ok := h.authenticate(w, r)
	if !ok {
		return "", false
	}
	if !h.rl.Allow(tokenID) {
		w.Header().Set("Retry-After", "60")
		writeAPIError(w, http.StatusTooManyRequests, "rate limit exceeded: 60 req/min")
		return "", false
	}
	return ownerID, true
}

// ─── Handlers ───────────────────────────────────────────────────────────────

// GetAccountMe handles GET /api/v1/account/me.
func (h *Handler) GetAccountMe(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := h.guard(w, r)
	if !ok {
		return
	}
	acct := Account{AccountID: ownerID}
	if h.authStore != nil {
		if u, found := h.authStore.GetUser(ownerID); found && u != nil {
			acct.Email = u.Email
			acct.CreatedAt = u.CreatedAt
		}
		if p, found := h.authStore.GetProfile(ownerID); found && p != nil {
			acct.DisplayName = p.DisplayName
			acct.Role = string(p.Role)
		}
	}
	if products := r.Header.Get("X-Vulos-Entitlements-Products"); products != "" {
		acct.Products = strings.Split(products, ",")
	}
	writeJSON(w, http.StatusOK, acct)
}

// GetAccountUsage handles GET /api/v1/account/usage.
func (h *Handler) GetAccountUsage(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := h.guard(w, r)
	if !ok {
		return
	}
	usage := AccountUsage{AccountID: ownerID}
	status := disks.GetStatus()
	for _, m := range status.Mounts {
		usage.StorageUsedMB += m.UsedMB
		usage.StorageTotalMB += m.TotalMB
	}
	if h.devices != nil {
		if devs, err := h.devices.ListDevices(r.Context(), ownerID); err == nil {
			usage.DeviceCount = len(devs)
		}
	}
	writeJSON(w, http.StatusOK, usage)
}

// ListDevices handles GET /api/v1/devices.
func (h *Handler) ListDevices(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := h.guard(w, r)
	if !ok {
		return
	}
	if h.devices == nil {
		writeJSON(w, http.StatusOK, []Device{})
		return
	}
	devs, err := h.devices.ListDevices(r.Context(), ownerID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if devs == nil {
		devs = []Device{}
	}
	writeJSON(w, http.StatusOK, devs)
}

// ─── Wire helpers ───────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
