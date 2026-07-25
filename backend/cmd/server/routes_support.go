// routes_support.go — Settings → Help & Support: the box owner can submit a
// support request and see its tier/channel/SLA status.
//
// Fold note: native port of the former vulos-management SUPPORT-03 three-tier
// support model into vulos proper (see backend/services/support). Owner-gated
// (same instRequireAdmin pattern as the other admin-only surfaces) — a single
// box has one owner, and this is their support inbox, not a multi-tenant
// ticket queue.
//
//	POST /api/support/requests          — submit a request (subject, body, priority)
//	GET  /api/support/requests          — list this owner's prior requests
//	POST /api/support/requests/{id}/close — close a request
//	GET  /api/support/eligibility       — { tier, channel, available } — lets the
//	                                       panel render the Free-tier wall without
//	                                       a failed submit round-trip
//
// Tier resolution: when a gateway with entitlements is configured
// (cpbilling.Client.Enabled()), the owner's plan tier is read from that
// authoritative lookup. Otherwise (standalone/self-hosted box,
// the common case) the tier defaults to "free" — which is the honest state:
// there is no live support desk behind a box that isn't enrolled anywhere.
package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"vulos/backend/internal/cpbilling"
	"vulos/backend/services/auth"
	"vulos/backend/services/notify"
	"vulos/backend/services/support"
)

// supportRequestBodyLimit caps the JSON body accepted by POST /api/support/requests.
const supportRequestBodyLimit = 16 << 10 // 16 KiB — plenty for a support message

// registerSupportRoutes wires the Help & Support endpoints into mux. It opens
// the support-requests store under dbDir; on failure it logs and registers no
// routes (fail-soft, matching services/integrations/selfhost's posture) rather
// than crashing the server over a non-essential surface.
func registerSupportRoutes(mux *http.ServeMux, dbDir string, authStore *auth.Store, notifySvc *notify.Service, billingClient *cpbilling.Client) {
	store, err := support.OpenStore(dbDir)
	if err != nil {
		log.Printf("[support] DISABLED: could not open store: %v", err)
		return
	}

	// resolveTier returns the authoritative plan tier for userID when a
	// control plane is configured, else "free". A cold cp outage also
	// resolves to "free" — fail closed on the wall rather than granting a
	// paid channel on unverified evidence (mirrors cpbilling.Gate's posture).
	resolveTier := func(r *http.Request, userID string) string {
		if billingClient == nil || !billingClient.Enabled() {
			return "free"
		}
		ent, err := billingClient.Entitlement(r.Context(), userID, "support")
		if err != nil || ent.Tier == "" {
			return "free"
		}
		return ent.Tier
	}

	// GET /api/support/eligibility — lets the panel render the Free wall
	// up-front instead of discovering it via a failed submit.
	mux.HandleFunc("GET /api/support/eligibility", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		userID := r.Header.Get("X-User-ID")
		tier := resolveTier(r, userID)
		channel, chErr := support.TicketChannelFor(tier)
		writeJSON(w, map[string]any{
			"tier":      tier,
			"channel":   channel,
			"available": chErr == nil,
		})
	})

	// POST /api/support/requests — submit a support request.
	mux.HandleFunc("POST /api/support/requests", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		var req struct {
			Subject  string `json:"subject"`
			Body     string `json:"body"`
			Priority string `json:"priority"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, supportRequestBodyLimit)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		subject := strings.TrimSpace(req.Subject)
		if subject == "" {
			writeErr(w, http.StatusBadRequest, "subject is required")
			return
		}

		userID := r.Header.Get("X-User-ID")
		tier := resolveTier(r, userID)

		created, err := store.Submit(r.Context(), userID, tier, req.Priority, subject, req.Body)
		if err != nil {
			if errors.Is(err, support.ErrNoTicketChannel) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				json.NewEncoder(w).Encode(map[string]any{
					"error":         err.Error(),
					"tier":          tier,
					"available":     false,
					"docs_url":      "https://vulos.org/docs",
					"community_url": "https://vulos.org/community",
				})
				return
			}
			writeErr(w, http.StatusInternalServerError, "submit: "+err.Error())
			return
		}
		execAuditLog(r, "POST /api/support/requests", "id="+strconv.FormatInt(created.ID, 10)+" tier="+tier+" channel="+created.Channel)
		if notifySvc != nil {
			notifySvc.Send(
				"Support request submitted",
				"\""+subject+"\" is tracked via the "+created.Channel+" channel.",
				notify.LevelInfo,
				"support",
			)
		}
		writeJSON(w, created)
	})

	// GET /api/support/requests — list this owner's prior requests, newest first.
	mux.HandleFunc("GET /api/support/requests", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		userID := r.Header.Get("X-User-ID")
		list, err := store.List(r.Context(), userID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "list: "+err.Error())
			return
		}
		writeJSON(w, map[string]any{"requests": list})
	})

	// POST /api/support/requests/{id}/close — mark a request resolved.
	mux.HandleFunc("POST /api/support/requests/{id}/close", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, http.StatusBadRequest, "invalid request id")
			return
		}
		userID := r.Header.Get("X-User-ID")
		if err := store.CloseRequest(r.Context(), id, userID); err != nil {
			switch {
			case errors.Is(err, support.ErrNotFound):
				writeErr(w, http.StatusNotFound, "request not found")
			case errors.Is(err, support.ErrForbidden):
				writeErr(w, http.StatusForbidden, "not the request owner")
			case errors.Is(err, support.ErrAlreadyClosed):
				writeErr(w, http.StatusConflict, "request already closed")
			default:
				writeErr(w, http.StatusInternalServerError, "close: "+err.Error())
			}
			return
		}
		execAuditLog(r, "POST /api/support/requests/{id}/close", "id="+strconv.FormatInt(id, 10))
		writeJSON(w, map[string]string{"status": "closed"})
	})

	log.Printf("[support] registered /api/support/{requests,eligibility} (owner-gated)")
}
