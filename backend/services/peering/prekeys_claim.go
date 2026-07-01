// prekeys_claim.go — Contract A: the one-time-prekey CLAIM endpoint.
//
// Static well-known publication of the prekey bundle is NOT enough for per-sender
// forward secrecy: every sender that fetched the bundle would pick the SAME
// one-time prekey (OneTimePreKeys[0]), so the OPK leg of X3DH would be shared and
// FS would degrade to the signed-prekey-only level for everyone after the first.
//
// The claim endpoint is the depletion mechanism: each caller atomically receives a
// DIFFERENT OPK which is deleted from the pool on hand-out (PreKeyStore.
// ClaimOneTimePreKey). A sender claims the recipient's bundle, runs X3DHInitiate
// with the returned signed prekey + OPK, and the recipient consumes-and-deletes
// that same OPK in X3DHRespond — so a captured ciphertext cannot be re-derived
// once the OPK is gone.
//
// Wire shape (identical on the box host AND the cell, per Contract A):
//
//	POST /api/peering/prekeys/claim
//	  request : { "identity_vula_id": "<base58 VulaID>" }
//	  200     : { "identity_vula_id": "...",
//	              "signed_prekey":   { "id": "...", "pub": "<b64>", "sig": "<b64>" },
//	              "one_time_prekey": { "id": "...", "pub": "<b64>" } | null }
//
// `pub`/`sig` are Go []byte → JSON standard-base64 (encoding/json default), the
// same encoding used by the published PreKeyBundlePublic. `one_time_prekey` is
// null when the pool is exhausted (sender falls back to signed-prekey-only,
// never reuses).
//
// Auth model: same as the host's other public /api/peering/* routes (e.g. the
// well-known bundle and profile fetch) — unauthenticated. Claiming is by design
// open to any sender (including remote boxes) so they can establish a forward-
// secret session; OPKs are public, single-use, and pool depletion is covered by
// background replenishment (StartPreKeyReplenish). The request's identity_vula_id
// must match the identity this host actually holds prekeys for, else 404.
package peering

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// RegisterPreKeyHandlers mounts the prekey directory on mux:
//
//   - POST /api/peering/prekeys/claim   — hand out + delete one OPK (Contract A),
//   - POST /api/peering/prekeys/publish — register a REMOTE peer's PUBLIC bundle.
//
// store holds THIS host's OWN identity prekeys (may be nil). published is the
// PUBLIC-ONLY directory for REMOTE (browser) peers (may be nil). At least one must
// be non-nil. Wire both onto the same peeringMux as the other /api/peering routes;
// they share that public auth model. CLOUD-BLIND: published stores only PUBLIC
// prekey material — never a private key.
func RegisterPreKeyHandlers(mux *http.ServeMux, store *PreKeyStore, published *PublishedBundleStore) {
	if store == nil && published == nil {
		return
	}

	// PUBLISH: a browser peer registers its client-generated PUBLIC bundle so
	// senders can claim its one-time prekeys. The signed-prekey signature is
	// verified against the claimed identity inside Publish (fail closed) — an
	// unsigned/forged bundle is rejected, so no authenticated session is required
	// to safely accept a publish (the signature IS the authorization).
	if published != nil {
		mux.HandleFunc("POST /api/peering/prekeys/publish", func(w http.ResponseWriter, r *http.Request) {
			var bundle PreKeyBundlePublic
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&bundle); err != nil {
				wkWriteErr(w, http.StatusBadRequest, "invalid prekey bundle")
				return
			}
			if bundle.IdentityVulaID == "" {
				wkWriteErr(w, http.StatusBadRequest, "identity_vula_id required")
				return
			}
			// Never accept a bundle for a revoked identity (fail closed).
			if isVulaIDRevoked(bundle.IdentityVulaID) {
				wkWriteErr(w, http.StatusForbidden, "identity is revoked")
				return
			}
			// Publish verifies the signed-prekey signature against IdentityVulaID and
			// rejects malformed/forged bundles without storing anything.
			if err := published.Publish(&bundle); err != nil {
				wkWriteErr(w, http.StatusBadRequest, "bundle rejected: "+err.Error())
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(map[string]any{
				"stored":           true,
				"one_time_prekeys": published.OneTimePreKeyCount(bundle.IdentityVulaID),
			})
		})
	}

	mux.HandleFunc("POST /api/peering/prekeys/claim", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IdentityVulaID string `json:"identity_vula_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IdentityVulaID == "" {
			wkWriteErr(w, http.StatusBadRequest, "identity_vula_id required")
			return
		}
		// Reject claims against a revoked identity (fail closed): a sender must not
		// open a forward-secret session to a retired key.
		if isVulaIDRevoked(req.IdentityVulaID) {
			wkWriteErr(w, http.StatusForbidden, "identity is revoked")
			return
		}

		var claimed *ClaimedBundle

		// 1) This host's OWN identity: serve from the private PreKeyStore.
		if store != nil && req.IdentityVulaID == store.IdentityVulaID() {
			c, err := store.ClaimOneTimePreKey()
			if err != nil {
				wkWriteErr(w, http.StatusInternalServerError, "claim failed")
				return
			}
			claimed = c
		} else if published != nil {
			// 2) A REMOTE (browser) peer that PUBLISHED its public bundle here.
			c, ok := published.Claim(req.IdentityVulaID)
			if !ok {
				wkWriteErr(w, http.StatusNotFound, "no prekeys for that identity on this host")
				return
			}
			claimed = c
		} else {
			wkWriteErr(w, http.StatusNotFound, "no prekeys for that identity on this host")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(claimed)
	})
}

// StartPreKeyReplenish launches a background goroutine that periodically tops the
// one-time prekey pool back up to target so claims keep depleting fresh OPKs
// (sustaining per-sender forward secrecy) rather than draining to the signed-
// prekey-only fallback. It exits when ctx is cancelled. Replenishment is
// deliberately SEPARATE from claim so that claim can return null on genuine
// exhaustion (single-use guarantee) and so depletion is observable.
func StartPreKeyReplenish(ctx context.Context, store *PreKeyStore, target int, interval time.Duration) {
	if store == nil || target <= 0 {
		return
	}
	if interval <= 0 {
		interval = time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		// Top up once promptly at startup, then on each tick.
		if store.OneTimePreKeyCount() < target {
			if err := store.Replenish(target); err != nil {
				os.Stderr.WriteString("[peering/prekeys] replenish: " + err.Error() + "\n")
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if store.OneTimePreKeyCount() < target {
					if err := store.Replenish(target); err != nil {
						os.Stderr.WriteString("[peering/prekeys] replenish: " + err.Error() + "\n")
					}
				}
			}
		}
	}()
}
