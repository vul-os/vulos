// mobilepush.go — JMAP push → APNs/FCM bridge (MOBILE-01).
//
// Routes:
//
//	POST /api/mobilepush/register        — register a device token (account-scoped, auth required)
//	POST /api/mobilepush/dispatch        — internal: receive a JMAP push event and fan out
//	                                        to registered tokens (secret-gated, called by the box mail engine)
package cproutes

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/env"
	"github.com/vul-os/vulos-management/pkg/mobilepush"
)

// WireMobilePush opens (or creates) the mobilepush database via cpdb and returns
// the Subscriber + Dispatcher. Called after env is initialised.
func WireMobilePush(dbDir string) (sub *mobilepush.Subscriber, disp *mobilepush.Dispatcher, closer func()) {
	// COORDINATOR: needs setDBDirIfUnset (composition-root helper, routes_developer_keys.go)
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Fatalf("[mobilepush] failed to set DB dir: %v", err)
	}
	db, err := cpdb.Open("mobilepush")
	if err != nil {
		log.Fatalf("[mobilepush] failed to open cpdb: %v", err)
	}
	sub, err = mobilepush.OpenSubscriber(db)
	if err != nil {
		log.Fatalf("[mobilepush] failed to open subscriber store: %v", err)
	}

	cfg, err := mobilepush.NewConfig(env.IsProd())
	if err != nil {
		if env.IsProd() {
			log.Fatalf("[mobilepush] prod config error (fail-closed): %v", err)
		}
		log.Printf("[mobilepush] WARN: %v — running in no-op mode", err)
		cfg = &mobilepush.Config{Prod: false}
	}

	disp = mobilepush.NewDispatcher(sub, cfg)
	return sub, disp, func() { sub.Close() }
}

// RegisterMobilePush wires the mobilepush HTTP endpoints into mux.
//
// dispatchSecret resolves the shared secret (MP_DISPATCH_SECRET) that gates the
// internal /api/mobilepush/dispatch endpoint called by the box mail engine. It
// is resolved per-request so a rotated value takes effect without a restart; an
// empty result disables the endpoint (fail-closed). Supply a resolver that reads
// the value through the SecretsProvider (secretOrEnv).
func RegisterMobilePush(mux *http.ServeMux, sub *mobilepush.Subscriber, disp *mobilepush.Dispatcher, authStore *auth.Store, dispatchSecret func(ctx context.Context) string) {

	// POST /api/mobilepush/register — register a device token.
	// Body: {"token":"<device-token>","platform":"apns"|"fcm"}
	mux.HandleFunc("POST /api/mobilepush/register", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		var req struct {
			Token    string              `json:"token"`
			Platform mobilepush.Platform `json:"platform"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		dt, err := sub.Register(r.Context(), u.ID, req.Token, req.Platform)
		if err != nil {
			log.Printf("[mobilepush] register: %v", err)
			http.Error(w, `{"error":"invalid token or platform"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(dt)
	})

	// POST /api/mobilepush/dispatch — receive a JMAP push event and fan out.
	// This endpoint is called by the box mail engine's JMAP push emitter (internal).
	// Gated by MP_DISPATCH_SECRET (X-Dispatch-Secret header); if the secret is
	// unset the endpoint is disabled entirely.
	mux.HandleFunc("POST /api/mobilepush/dispatch", func(w http.ResponseWriter, r *http.Request) {
		// Resolve the dispatch secret per-request so a rotated value takes effect
		// without a restart. If unset, the endpoint is disabled (fail-closed).
		secret := ""
		if dispatchSecret != nil {
			secret = dispatchSecret(r.Context())
		}
		if secret == "" {
			http.Error(w, `{"error":"mobilepush dispatch is not configured"}`, http.StatusServiceUnavailable)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Dispatch-Secret")), []byte(secret)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		var event mobilepush.JMAPPushEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		if event.AccountID == "" {
			http.Error(w, `{"error":"accountId required"}`, http.StatusBadRequest)
			return
		}

		result := disp.Dispatch(r.Context(), event)
		log.Printf("[mobilepush] dispatch account=%s sent=%d failed=%d",
			result.AccountID, result.TokensSent, result.TokensFailed)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"account_id":    result.AccountID,
			"tokens_sent":   result.TokensSent,
			"tokens_failed": result.TokensFailed,
		})
	})
}
