package main

// routes_webhooks.go — Settings -> Developer -> Webhooks: outbound webhook
// subscriptions for the box owner.
//
// Endpoints (all admin-only; the box has exactly one owner-facing set of
// subscriptions — see backend/services/webhooks for the single-owner-scope
// rationale):
//
//	GET    /api/webhooks                    — list subscriptions (secret redacted)
//	POST   /api/webhooks                    — create a subscription (step-up gated)
//	GET    /api/webhooks/{id}               — get one subscription (secret redacted)
//	PATCH  /api/webhooks/{id}                — edit url/topics/active (step-up gated)
//	DELETE /api/webhooks/{id}               — remove a subscription
//	POST   /api/webhooks/{id}/rotate-secret — issue a fresh HMAC secret (returned once)
//	POST   /api/webhooks/{id}/test          — fire an immediate synthetic "test.ping"
//	GET    /api/webhooks/{id}/deliveries    — delivery log (most recent first)
//	GET    /api/webhooks/topics             — the fixed list of subscribable topics
//
// Create and edit are gated with stepup.Require: a webhook subscription is an
// exfiltration vector (every event payload the owner later emits gets POSTed,
// signed, to whatever URL is configured here), so pointing it at a new or
// different URL requires a freshly-elevated session — the same bar as other
// destructive/sensitive owner actions. Delete/rotate/test/list are not gated:
// they narrow or exercise an already-approved URL rather than widen the
// exfil surface.
//
// The SSRF guard (backend/services/webhooks/ssrf.go) is enforced INSIDE the
// webhooks package on every create/edit and again at every delivery dial —
// this file never bypasses it.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strconv"

	"vulos/backend/services/auth"
	"vulos/backend/services/stepup"
	"vulos/backend/services/webhooks"
)

// whSubscriptionView is the API-safe subscription shape: Secret is NEVER
// included except immediately after CreateSubscription / RotateSecret, where
// it is carried in a separate "secret" field on that one response only.
type whSubscriptionView struct {
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	Topics    []string `json:"topics"`
	Active    bool     `json:"active"`
	CreatedBy string   `json:"created_by"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

func whView(s webhooks.Subscription) whSubscriptionView {
	return whSubscriptionView{
		ID: s.ID, URL: s.URL, Topics: s.Topics, Active: s.Active, CreatedBy: s.CreatedBy,
		CreatedAt: s.CreatedAt.Format(rfc3339), UpdatedAt: s.UpdatedAt.Format(rfc3339),
	}
}

// rfc3339 avoids importing "time" just for the layout constant name in this file.
const rfc3339 = "2006-01-02T15:04:05Z07:00"

type whDeliveryView struct {
	ID             string  `json:"id"`
	Topic          string  `json:"topic"`
	Status         string  `json:"status"`
	AttemptCount   int     `json:"attempt_count"`
	NextAttemptAt  string  `json:"next_attempt_at"`
	LastAttemptAt  *string `json:"last_attempt_at,omitempty"`
	LastStatusCode *int    `json:"last_status_code,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

func whDeliveryViewOf(d webhooks.Delivery) whDeliveryView {
	v := whDeliveryView{
		ID: d.ID, Topic: d.Topic, Status: d.Status, AttemptCount: d.AttemptCount,
		NextAttemptAt: d.NextAttemptAt.Format(rfc3339), CreatedAt: d.CreatedAt.Format(rfc3339),
		LastStatusCode: d.LastStatusCode,
	}
	if d.LastAttemptAt != nil {
		s := d.LastAttemptAt.Format(rfc3339)
		v.LastAttemptAt = &s
	}
	return v
}

// registerWebhooksRoutes wires the box-owner webhooks surface onto mux.
//
// Opens (or creates) the webhooks SQLite store under <home>/db. On open
// failure it logs and registers nothing — the feature is simply unavailable
// rather than crashing the box on a corrupt/unwritable store, matching the
// fail-closed pattern used by the other package-owned stores wired here
// (see registerSelfHostIntegrations).
func registerWebhooksRoutes(mux *http.ServeMux, authStore *auth.Store, home string) {
	dbDir := filepath.Join(home, "db")
	store, err := webhooks.OpenStore(dbDir)
	if err != nil {
		log.Printf("[webhooks] DISABLED: could not open store: %v", err)
		return
	}
	dispatcher := webhooks.NewDispatcher(store)

	// GET /api/webhooks/topics — static list; useful for populating the
	// panel's topic picker without hard-coding it twice.
	mux.HandleFunc("GET /api/webhooks/topics", func(w http.ResponseWriter, r *http.Request) {
		if !whRequireAdmin(w, r, authStore) {
			return
		}
		topics := make([]string, 0, len(webhooks.ValidTopics))
		for t := range webhooks.ValidTopics {
			topics = append(topics, t)
		}
		writeJSON(w, map[string]any{"topics": topics})
	})

	mux.HandleFunc("GET /api/webhooks", func(w http.ResponseWriter, r *http.Request) {
		if !whRequireAdmin(w, r, authStore) {
			return
		}
		subs, err := store.ListSubscriptions(r.Context())
		if err != nil {
			log.Printf("[webhooks] list: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not list webhooks")
			return
		}
		out := make([]whSubscriptionView, 0, len(subs))
		for _, s := range subs {
			out = append(out, whView(s))
		}
		writeJSON(w, map[string]any{"subscriptions": out})
	})

	mux.HandleFunc("POST /api/webhooks", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := whRequireAdminUser(w, r, authStore)
		if !ok {
			return
		}
		// Step-up: creating a subscription points a signed feed of box events
		// at a new URL — an exfiltration vector — so require a freshly
		// elevated session.
		if err := stepup.Require(r); err != nil {
			writeErr(w, http.StatusForbidden, "step-up verification required: "+err.Error())
			return
		}
		var body struct {
			URL    string   `json:"url"`
			Topics []string `json:"topics"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		sub, err := store.CreateSubscription(r.Context(), userID, body.URL, body.Topics)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("[webhooks] CREATED subscription=%s topics=%v user=%s", sub.ID, sub.Topics, userID)
		resp := whView(sub)
		writeJSON(w, map[string]any{
			"subscription": resp,
			// Secret is returned exactly once, here, and never again.
			"secret": base64.RawURLEncoding.EncodeToString(sub.Secret),
		})
	})

	mux.HandleFunc("GET /api/webhooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !whRequireAdmin(w, r, authStore) {
			return
		}
		sub, err := store.GetSubscription(r.Context(), r.PathValue("id"))
		if errors.Is(err, webhooks.ErrSubNotFound) {
			writeErr(w, http.StatusNotFound, "webhook not found")
			return
		}
		if err != nil {
			log.Printf("[webhooks] get: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not load webhook")
			return
		}
		writeJSON(w, whView(sub))
	})

	mux.HandleFunc("PATCH /api/webhooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := whRequireAdminUser(w, r, authStore)
		if !ok {
			return
		}
		// Step-up: editing can repoint the URL an already-signed feed is sent
		// to — same exfil-vector reasoning as create.
		if err := stepup.Require(r); err != nil {
			writeErr(w, http.StatusForbidden, "step-up verification required: "+err.Error())
			return
		}
		var body struct {
			URL    *string  `json:"url"`
			Topics []string `json:"topics"`
			Active *bool    `json:"active"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		sub, err := store.UpdateSubscription(r.Context(), r.PathValue("id"), body.URL, body.Topics, body.Active)
		if errors.Is(err, webhooks.ErrSubNotFound) {
			writeErr(w, http.StatusNotFound, "webhook not found")
			return
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("[webhooks] UPDATED subscription=%s user=%s", sub.ID, userID)
		writeJSON(w, whView(sub))
	})

	mux.HandleFunc("DELETE /api/webhooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := whRequireAdminUser(w, r, authStore)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if err := store.DeleteSubscription(r.Context(), id); errors.Is(err, webhooks.ErrSubNotFound) {
			writeErr(w, http.StatusNotFound, "webhook not found")
			return
		} else if err != nil {
			log.Printf("[webhooks] delete: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not delete webhook")
			return
		}
		log.Printf("[webhooks] DELETED subscription=%s user=%s", id, userID)
		writeJSON(w, map[string]string{"status": "deleted", "id": id})
	})

	mux.HandleFunc("POST /api/webhooks/{id}/rotate-secret", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := whRequireAdminUser(w, r, authStore)
		if !ok {
			return
		}
		id := r.PathValue("id")
		secret, err := store.RotateSecret(r.Context(), id)
		if errors.Is(err, webhooks.ErrSubNotFound) {
			writeErr(w, http.StatusNotFound, "webhook not found")
			return
		}
		if err != nil {
			log.Printf("[webhooks] rotate: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not rotate secret")
			return
		}
		log.Printf("[webhooks] ROTATED secret subscription=%s user=%s", id, userID)
		writeJSON(w, map[string]string{"secret": base64.RawURLEncoding.EncodeToString(secret)})
	})

	mux.HandleFunc("POST /api/webhooks/{id}/test", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := whRequireAdminUser(w, r, authStore)
		if !ok {
			return
		}
		id := r.PathValue("id")
		sub, err := store.GetSubscription(r.Context(), id)
		if errors.Is(err, webhooks.ErrSubNotFound) {
			writeErr(w, http.StatusNotFound, "webhook not found")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not load webhook")
			return
		}
		code, sendErr := dispatcher.SendTest(r.Context(), sub)
		if sendErr != nil {
			log.Printf("[webhooks] test send subscription=%s user=%s: %v", id, userID, sendErr)
			writeJSON(w, map[string]any{"delivered": false, "error": sendErr.Error()})
			return
		}
		writeJSON(w, map[string]any{"delivered": code >= 200 && code < 300, "status_code": code})
	})

	mux.HandleFunc("GET /api/webhooks/{id}/deliveries", func(w http.ResponseWriter, r *http.Request) {
		if !whRequireAdmin(w, r, authStore) {
			return
		}
		id := r.PathValue("id")
		limit := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		deliveries, err := store.ListDeliveries(r.Context(), id, limit)
		if err != nil {
			log.Printf("[webhooks] list deliveries: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not load delivery log")
			return
		}
		out := make([]whDeliveryView, 0, len(deliveries))
		for _, d := range deliveries {
			out = append(out, whDeliveryViewOf(d))
		}
		writeJSON(w, map[string]any{"deliveries": out})
	})

	log.Printf("[webhooks] registered /api/webhooks (owner-only; SSRF guard enforced on create/edit + every delivery dial)")
}

// whRequireAdmin authorises an owner-only webhooks request. A nil auth store
// (degraded boot) is treated as "no admin exists" and refuses — never as
// "everyone is admin". Mirrors instRequireAdmin (routes_instances_manage.go).
func whRequireAdmin(w http.ResponseWriter, r *http.Request, authStore *auth.Store) bool {
	_, ok := whRequireAdminUser(w, r, authStore)
	return ok
}

// whRequireAdminUser is whRequireAdmin plus the verified caller's user id, for
// handlers that want it for audit logging.
func whRequireAdminUser(w http.ResponseWriter, r *http.Request, authStore *auth.Store) (string, bool) {
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
