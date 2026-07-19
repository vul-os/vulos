// routes_developer_keys.go — CP unified API-key system (APIKEYS-01).
//
// Endpoints registered:
//
//	GET    /api/developer/keys          session-auth  list keys (metadata, never raw secret)
//	POST   /api/developer/keys          session-auth  issue new key (returns raw secret once)
//	DELETE /api/developer/keys/{id}     session-auth  revoke / delete a key
//
//	POST   /api/keys/introspect         service-auth  resolve any vk_ key → account/scopes/products
//	                                    (optional X-Relay-Auth: <CP_SHARED_SECRET>)
//
// Security properties:
//   - Keys are vk_ prefixed, 43-char base64url suffix (32 random bytes = 256 bits).
//   - Only the SHA-256 hex digest is stored; the raw key is returned exactly once
//     at POST /api/developer/keys and is never logged.
//   - Issue endpoint is rate-limited: 10 keys per account per 24 h.
//   - Introspect is rotation-aware: accepts the current OR previous CP_SHARED_SECRET
//     (same as the relay secret pattern used across the CP). When no secret is
//     configured the endpoint is open (network-trust mode / dev).
//   - Revoked and expired keys always return {valid:false} from introspect.
//
// Wire-in (main.go):
//
//	developerKeysCloser := WireDeveloperKeys(mux, dbDir, authStore)
//	defer developerKeysCloser()
package cproutes

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/vul-os/vulos-management/pkg/apikeys"
	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/billingport"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// ─── Valid scopes + products ──────────────────────────────────────────────────

// knownScopes is the allow-list of scopes that may be granted to a key.
// Expand as new product APIs are added.
var knownScopes = map[string]bool{
	"documents.read":  true,
	"documents.write": true,
	"drive.read":      true,
	"drive.write":     true,
	"relay.send":      true,
}

// knownProducts is the allow-list of product identifiers.
// (mail.*, talk.* and meet.* were withdrawn: mail is box-level via lilmail and
// Talk/Meet are now third-party — all archived as first-party CP products,
// 2026-07-15.)
var knownProducts = map[string]bool{
	"office": true,
	"relay":  true,
	"drive":  true,
	"files":  true, // SUBDOMAIN-ROUTING-01
}

// billingEntitlementFilter implements apikeys.EntitlementFilter over the
// provider-agnostic billingport.EntitlementResolver seam (ENTITLEMENT-SEAM-01).
//
// In the box / KILL-FLAT-TIERS model the operational control plane enforces no
// per-app tier gate: every Class-P app runs on the account's own box and is
// entitled for every account regardless of tier. So the operational filter is
// structurally a pass-through — it consults the injected resolver's effective
// tier (so a commercial distributor can observe standing) but never narrows the
// claimed product set. A distributor that DOES want tier-based narrowing injects
// its own apikeys.EntitlementFilter via Store.SetEntitlementFilter; it does not
// reuse this operational one. Fails open (products unchanged) on any resolver
// error, mirroring every other best-effort billing seam.
type billingEntitlementFilter struct {
	ent billingport.EntitlementResolver
}

func (f billingEntitlementFilter) FilterProducts(ctx context.Context, accountID string, products []string) ([]string, error) {
	if f.ent == nil || len(products) == 0 {
		return products, nil
	}
	// Consult effective standing (dunning-aware) but do not gate: box-model apps
	// are entitled for every account. A resolver error is non-fatal (fail open).
	_, _ = f.ent.EffectiveTierFor(ctx, accountID)
	return products, nil
}

// ─── Wire helper ─────────────────────────────────────────────────────────────

// WireDeveloperKeys opens the API-keys store via cpdb (SQLite by default;
// Postgres when DATABASE_URL / VULOS_DATABASE_URL is set) and registers routes.
// Returns a closer that shuts the store down on process exit.
//
// Backend selection is transparent: set DATABASE_URL to a Postgres DSN to use
// Postgres; leave it unset to use SQLite at <dbDir>/apikeys.db (self-host default).
func WireDeveloperKeys(mux *http.ServeMux, dbDir string, authStore *auth.Store, ent billingport.EntitlementResolver) func() {
	// cpdb.Open reads DATABASE_URL / VULOS_DATABASE_URL; falls back to SQLite at
	// <VULOS_DB_DIR>/apikeys.db. We set VULOS_DB_DIR from dbDir so the file lands
	// in the same directory that all other SQLite stores use today.
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[apikeys] WARNING: could not set DB dir (%v) — developer key routes not registered", err)
		return func() {}
	}

	db, err := cpdb.Open("apikeys")
	if err != nil {
		log.Printf("[apikeys] WARNING: could not open cpdb (%v) — developer key routes not registered", err)
		return func() {}
	}

	st, err := apikeys.Open(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[apikeys] WARNING: could not open store (%v) — developer key routes not registered", err)
		return func() {}
	}

	log.Printf("[apikeys] store opened (backend=%s)", db.Backend())

	// ENTITLEMENT-SEAM-01: wire the entitlement filter so a vk_ key's
	// introspected products reflect the account's CURRENT standing. In the box
	// model this is a pass-through (every app is entitled for every account); a
	// commercial distributor that wants tier-based narrowing injects its own
	// filter. A nil resolver leaves introspect returning products verbatim.
	if ent != nil {
		st.SetEntitlementFilter(billingEntitlementFilter{ent: ent})
	}

	// 10 keys per account per 24 h on the issue endpoint.
	limiter := apikeys.NewIssueLimiter(24*60*60*1e9 /* 24h */, 10)

	RegisterDeveloperKeyRoutes(mux, st, authStore, limiter)
	log.Println("[apikeys] routes registered (GET/POST/DELETE /api/developer/keys, POST /api/keys/introspect)")

	return func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[apikeys] store close: %v", cerr)
		}
	}
}

// setDBDirIfUnset sets VULOS_DB_DIR to dir when the env var is not already
// set.  cpdb.Open uses VULOS_DB_DIR as the base directory for SQLite files,
// which must match the directory used by all other stores in wire_stores.go.
func setDBDirIfUnset(dir string) error {
	if dir == "" {
		return nil
	}
	if os.Getenv("VULOS_DB_DIR") != "" {
		return nil // already set — don't override
	}
	return os.Setenv("VULOS_DB_DIR", dir)
}

// SetDBDirIfUnset is the exported wrapper so package-main callers (main.go and
// any wire_*.go not yet moved into cproutes) can set the SQLite base dir.
// Files already inside cproutes call the unexported setDBDirIfUnset directly.
func SetDBDirIfUnset(dir string) error { return setDBDirIfUnset(dir) }

// RegisterDeveloperKeyRoutes wires the key management + introspection endpoints.
// Separated from WireDeveloperKeys so tests can inject an in-memory store.
func RegisterDeveloperKeyRoutes(mux *http.ServeMux, st *apikeys.Store, authStore *auth.Store, limiter *apikeys.IssueLimiter) {
	// ── GET /api/developer/keys ───────────────────────────────────────────────
	// Returns the list of keys for the session account.
	// Metadata only — the raw secret is NEVER re-exposed.
	mux.HandleFunc("GET /api/developer/keys", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		keys, err := st.ListKeys(r.Context(), u.ID)
		if err != nil {
			log.Printf("[apikeys] list keys account=%s: %v", u.ID, err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, keys)
	})

	// ── POST /api/developer/keys ──────────────────────────────────────────────
	// Issues a new key. Returns the raw secret exactly once in {"key":"vk_…"}.
	// Body: {"name":"…","scopes":[…],"products":[…]}
	mux.HandleFunc("POST /api/developer/keys", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}

		// Rate limit.
		if !limiter.Allow(u.ID) {
			w.Header().Set("Retry-After", "3600")
			httpx.Err(w, http.StatusTooManyRequests, "rate limit: max 10 keys issued per 24 hours")
			return
		}

		var body struct {
			Name     string   `json:"name"`
			Scopes   []string `json:"scopes"`
			Products []string `json:"products"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		if err := dec.Decode(&body); err != nil {
			httpx.Err(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			httpx.Err(w, http.StatusBadRequest, "name is required")
			return
		}

		// Validate and sanitize scopes.
		scopes := filterSlice(body.Scopes, knownScopes)
		if len(body.Scopes) > 0 && len(scopes) == 0 {
			httpx.Err(w, http.StatusBadRequest, "no valid scopes; known: "+joinKeys(knownScopes))
			return
		}

		// Validate and sanitize products.
		products := filterSlice(body.Products, knownProducts)
		if len(body.Products) > 0 && len(products) == 0 {
			httpx.Err(w, http.StatusBadRequest, "no valid products; known: "+joinKeys(knownProducts))
			return
		}

		rawKey, meta, err := st.IssueKey(r.Context(), u.ID, strings.TrimSpace(body.Name), scopes, products)
		if err != nil {
			log.Printf("[apikeys] issue key account=%s: %v", u.ID, err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Return meta + the raw key (ONCE). Never log rawKey.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         meta.ID,
			"name":       meta.Name,
			"scopes":     meta.Scopes,
			"products":   meta.Products,
			"created_at": meta.CreatedAt,
			"key":        rawKey, // shown once; caller must copy it now
		})
	})

	// ── DELETE /api/developer/keys/{id} ──────────────────────────────────────
	// Revokes the key. Uses account-scoped revoke so cross-account attempts fail.
	mux.HandleFunc("DELETE /api/developer/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		keyID := r.PathValue("id")
		if keyID == "" {
			httpx.Err(w, http.StatusBadRequest, "key id required")
			return
		}
		if err := st.RevokeKey(r.Context(), u.ID, keyID); err != nil {
			if err == apikeys.ErrKeyNotFound {
				httpx.Err(w, http.StatusNotFound, "key not found")
				return
			}
			log.Printf("[apikeys] revoke key %s account=%s: %v", keyID, u.ID, err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// ── POST /api/keys/introspect ─────────────────────────────────────────────
	// Service endpoint: resolves any vk_ key → {valid, account, scopes, products}.
	// Matches the contract consumed by vulos-office/backend/apikey and any other
	// Vulos product that needs to validate a user-supplied API key.
	//
	// Auth: X-Relay-Auth: <CP_SHARED_SECRET> (required).
	//   SEC-H3 (audit): previously failed OPEN when CP_SHARED_SECRET was unset.
	//   Fixed: fail-closed — 503 if no secret configured (dev must set it or
	//   use RELAY_REQUIRE_POP_SIG=0 to bypass). 401 if secret is wrong.
	mux.HandleFunc("POST /api/keys/introspect", func(w http.ResponseWriter, r *http.Request) {
		// Service-auth gate: fail-closed.
		requireSig := os.Getenv("RELAY_REQUIRE_POP_SIG") != "0"
		if requireSig {
			if !hasSharedSecret(r.Context()) {
				httpx.Err(w, http.StatusServiceUnavailable, "no shared secret configured (set CP_SHARED_SECRET, or RELAY_REQUIRE_POP_SIG=0 in dev)")
				return
			}
			presented := r.Header.Get("X-Relay-Auth")
			if !validSharedSecretEither(r.Context(), presented) {
				httpx.Err(w, http.StatusUnauthorized, "invalid or missing X-Relay-Auth")
				return
			}
		}

		var body struct {
			Key string `json:"key"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		if err := dec.Decode(&body); err != nil {
			httpx.Err(w, http.StatusBadRequest, "invalid JSON")
			return
		}

		// Non-vk_ credentials: return valid=false immediately (wrong key type).
		if !strings.HasPrefix(body.Key, apikeys.KeyPrefix) {
			httpx.JSON(w, apikeys.IntrospectResult{Valid: false})
			return
		}

		res, err := st.Introspect(r.Context(), body.Key)
		if err != nil {
			log.Printf("[apikeys] introspect: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, res)
	})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// filterSlice returns only the elements of items that appear in allowed.
// Deduplicates the output.
func filterSlice(items []string, allowed map[string]bool) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if allowed[item] && !seen[item] {
			out = append(out, item)
			seen[item] = true
		}
	}
	return out
}

// joinKeys returns the sorted keys of m as a comma-separated string.
func joinKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

// _ is a compile-time check that context is imported (used by
// hasSharedSecret / validSharedSecretEither via wire_secrets.go).
var _ = context.Background
