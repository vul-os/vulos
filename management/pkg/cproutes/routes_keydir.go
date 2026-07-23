// routes_keydir.go — MAIL-07 vulos.org key-directory HTTP routes.
//
// Routes (session-gated unless noted):
//
//	PUT  /api/mail/keydir                      — publish / rotate own key entry
//	GET  /api/mail/keydir                      — get own key entry
//	DELETE /api/mail/keydir                    — revoke own key entry
//	PATCH /api/mail/keydir/discoverable        — toggle discoverability
//
//	GET  /api/mail/keydir/lookup               — unauthenticated: resolve address → key
//	                                             (queried by vulos-relay peering transport)
//
// The relay-side peering transport (OSS vulos-relay) calls /lookup to resolve
// a Vulos account address to its Ed25519 public key before opening a peering session.
// No auth required on /lookup so relay nodes can query without a session cookie.
package cproutes

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/keydir"
)

// RegisterKeyDirRoutes wires all MAIL-07 endpoints into mux.
func RegisterKeyDirRoutes(mux *http.ServeMux, st keydir.Store, authStore *auth.Store) {
	// ── PUT /api/mail/keydir ─────────────────────────────────────────────────
	// Body: { "public_key_b64": "...", "peer_endpoint": "...", "discoverable": true }
	mux.HandleFunc("PUT /api/mail/keydir", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		var req struct {
			VumailAddress string `json:"vumail_address"`
			PublicKeyB64  string `json:"public_key_b64"`
			PeerEndpoint  string `json:"peer_endpoint"`
			Discoverable  bool   `json:"discoverable"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeKeyDirError(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.PublicKeyB64 == "" {
			writeKeyDirError(w, "public_key_b64 required", http.StatusBadRequest)
			return
		}
		if req.VumailAddress == "" {
			// Default: derive from the account's registered email if it is @vulos.org.
			req.VumailAddress = u.Email
		}
		// SECURITY (audit H4): the session account may only publish a key for an
		// address it actually owns — its own registered identity email. Otherwise
		// an attacker could squat an unclaimed or another user's address and
		// publish their own public key for it, enabling mail MITM. Compare
		// case-insensitively after trimming.
		if !strings.EqualFold(strings.TrimSpace(req.VumailAddress), strings.TrimSpace(u.Email)) {
			writeKeyDirError(w, "you may only publish a key for your own Vulos address", http.StatusForbidden)
			return
		}
		e, err := st.Upsert(r.Context(), keydir.Entry{
			AccountID:     u.ID,
			VumailAddress: req.VumailAddress,
			PublicKeyB64:  req.PublicKeyB64,
			PeerEndpoint:  req.PeerEndpoint,
			Discoverable:  req.Discoverable,
		})
		if err != nil {
			if errors.Is(err, keydir.ErrNotVumail) {
				writeKeyDirError(w, "vulos_address must be an @vulos.org address", http.StatusBadRequest)
				return
			}
			if errors.Is(err, keydir.ErrDuplicate) {
				writeKeyDirError(w, "Vulos address is already registered by another account", http.StatusConflict)
				return
			}
			if errors.Is(err, keydir.ErrInvalidInput) {
				writeKeyDirError(w, "missing required fields", http.StatusBadRequest)
				return
			}
			log.Printf("[keydir] upsert: %v", err)
			writeKeyDirError(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "entry": e})
	})

	// ── GET /api/mail/keydir ─────────────────────────────────────────────────
	// Returns the calling account's own key entry.
	mux.HandleFunc("GET /api/mail/keydir", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		e, err := st.GetByAccount(r.Context(), u.ID)
		if err != nil {
			if errors.Is(err, keydir.ErrNotFound) {
				writeKeyDirError(w, "no key directory entry found", http.StatusNotFound)
				return
			}
			log.Printf("[keydir] get by account: %v", err)
			writeKeyDirError(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(e)
	})

	// ── DELETE /api/mail/keydir ──────────────────────────────────────────────
	// Revokes the calling account's key entry.  The entry remains in the DB
	// with state="revoked" but is invisible to /lookup.
	mux.HandleFunc("DELETE /api/mail/keydir", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		if err := st.Revoke(r.Context(), u.ID); err != nil {
			log.Printf("[keydir] revoke: %v", err)
			writeKeyDirError(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	// ── PATCH /api/mail/keydir/discoverable ──────────────────────────────────
	// Body: { "discoverable": true|false }
	mux.HandleFunc("PATCH /api/mail/keydir/discoverable", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		var req struct {
			Discoverable bool `json:"discoverable"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeKeyDirError(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := st.SetDiscoverable(r.Context(), u.ID, req.Discoverable); err != nil {
			if errors.Is(err, keydir.ErrNotFound) {
				writeKeyDirError(w, "no key directory entry found", http.StatusNotFound)
				return
			}
			log.Printf("[keydir] set discoverable: %v", err)
			writeKeyDirError(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	// ── GET /api/mail/keydir/lookup ──────────────────────────────────────────
	// Public endpoint — no session required.
	// Query param: ?address=alice@vulos.org
	// Returns: { "vumail_address" (vulos_address): "...", "public_key_b64": "...", "peer_endpoint": "..." }
	// Returns 404 if not found or not discoverable.
	//
	// This is the endpoint queried by the OSS vulos-relay peering transport to
	// resolve a Vulos account address → Ed25519 public key before establishing a
	// Vulos-to-Vulos peering session.
	mux.HandleFunc("GET /api/mail/keydir/lookup", func(w http.ResponseWriter, r *http.Request) {
		address := r.URL.Query().Get("address")
		if address == "" {
			writeKeyDirError(w, "address query parameter required", http.StatusBadRequest)
			return
		}
		if err := keydir.ValidateVumailAddress(address); err != nil {
			writeKeyDirError(w, "address must be a valid @vulos.org address", http.StatusBadRequest)
			return
		}
		e, err := st.GetByAddress(r.Context(), address)
		if err != nil {
			if errors.Is(err, keydir.ErrNotFound) {
				writeKeyDirError(w, "address not found", http.StatusNotFound)
				return
			}
			log.Printf("[keydir] lookup: %v", err)
			writeKeyDirError(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Non-discoverable entries still exist but are hidden from the public
		// directory lookup (the relay needs to know the key exists, but we
		// respect the user's preference not to be listed).
		if !e.Discoverable {
			writeKeyDirError(w, "address not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"vumail_address": e.VumailAddress,
			"public_key_b64": e.PublicKeyB64,
			"peer_endpoint":  e.PeerEndpoint,
		})
	})
}

func writeKeyDirError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
