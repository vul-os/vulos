// ato.go — account takeover monitoring.
//
// Hooks on sensitive actions (email change, 2FA reset, recovery codes, password
// change, bulk export, mass-download). Each hook checks recent activity for
// anomalies and fires cross-device alerts with a cancel link on suspicion.
//
// Hook pattern:
//
//	ATO middleware wraps an existing handler:
//	  SensitiveActionMiddleware(store, "email_change", sender, next)
//
// Anomaly rules:
//   - Multiple (>=3) sensitive changes in <30 min → high suspicion
//   - Sensitive change <1 h after the first activity from a new IP → high suspicion
package security

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/env"
)

// SensitiveActions is the list of action strings that trigger ATO monitoring.
var SensitiveActions = []string{
	"email_change",
	"2fa_reset",
	"recovery_codes_regen",
	"password_change",
	"bulk_export",
	"mass_download",
}

// ATOConfig configures the ATO middleware.
type ATOConfig struct {
	Store  *Store
	Sender InboxSender // for cross-device notifications
}

// SensitiveActionMiddleware wraps a handler and records the action to the ATO log.
// After recording, it checks for anomalies and triggers alerts.
func SensitiveActionMiddleware(cfg ATOConfig, action string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract userID from context (set by auth middleware upstream).
		userID := userIDFromCtx(r.Context())
		clientIP := remoteIPStepUp(r)

		// Execute the real handler first, then analyse (non-blocking).
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		next.ServeHTTP(rec, r)

		// Only analyse on success responses.
		if rec.code >= 200 && rec.code < 300 && cfg.Store != nil && userID != "" {
			go func() {
				ctx := context.Background()
				_ = cfg.Store.RecordSensitiveAction(ctx, userID, action, clientIP)
				checkATOAnomaly(ctx, cfg, userID, action, clientIP)
			}()
		}
	})
}

// checkATOAnomaly evaluates anomaly rules and fires alerts when triggered.
func checkATOAnomaly(ctx context.Context, cfg ATOConfig, userID, action, clientIP string) {
	if cfg.Store == nil {
		return
	}

	// Rule 1: >=3 sensitive changes in <30 min.
	count, err := cfg.Store.SensitiveActionsInWindow(ctx, userID, 30)
	if err == nil && count >= 3 {
		alert := fmt.Sprintf(
			"ATO alert: %d sensitive actions in 30 min for user %s (latest: %s from %s)",
			count, userID, action, clientIP,
		)
		log.Printf("[security/ato] %s", alert)
		_ = cfg.Store.RecordATOEvent(ctx, userID, action, clientIP)
		sendATOAlert(ctx, cfg.Sender, userID, action, clientIP, "multiple_sensitive_actions")
		return
	}

	// Rule 2: sensitive change <1 h after first activity from a new IP.
	oneHourAgo := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	var firstFromIP int
	_ = cfg.Store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM security_sensitive_actions
		 WHERE user_id=? AND client_ip=? AND ts >= ?`,
		userID, clientIP, oneHourAgo,
	).Scan(&firstFromIP)

	if firstFromIP <= 1 {
		// This IP is new (only the current action recorded).
		alert := fmt.Sprintf(
			"ATO alert: sensitive action %q within 1h of first appearance of IP %s for user %s",
			action, clientIP, userID,
		)
		log.Printf("[security/ato] %s", alert)
		_ = cfg.Store.RecordATOEvent(ctx, userID, action, clientIP)
		sendATOAlert(ctx, cfg.Sender, userID, action, clientIP, "new_ip_sensitive_action")
	}
}

// sendATOAlert sends a cross-device notification.
func sendATOAlert(ctx context.Context, sender InboxSender, userID, action, clientIP, reason string) {
	if sender == nil {
		return
	}
	// In production, userID → email lookup via authStore. Here we use userID
	// as the address (the wire layer provides a real authStore lookup).
	toEmail := atoUserIDToEmail(userID)
	if toEmail == "" {
		return
	}

	cancelURL := fmt.Sprintf("%s/account/security/cancel-action?uid=%s&action=%s",
		env.AppURL(), userID, action)

	body := fmt.Sprintf(
		"Security alert: a sensitive action (%s) was performed on your Vulos account.\n\n"+
			"IP address: %s\n"+
			"Reason flagged: %s\n"+
			"Time: %s UTC\n\n"+
			"If this was not you, cancel this action within 24 hours:\n%s\n\n"+
			"If it was you, no action is needed.",
		action, clientIP, reason,
		time.Now().UTC().Format("2006-01-02 15:04:05"),
		cancelURL,
	)
	if err := sender.Send(ctx, toEmail, "Vulos security alert: sensitive action detected", body); err != nil {
		log.Printf("[security/ato] alert send: %v", err)
	}
}

// atoUserIDToEmail converts a userID to an email for ATO notifications.
// In production this is wired to a real authStore lookup; this is a
// fallback for the self-contained package.
func atoUserIDToEmail(userID string) string {
	if strings.Contains(userID, "@") {
		return userID // userID was stored as email (stepup.go pattern)
	}
	return "" // requires wire-layer injection
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// userIDFromCtx extracts the authenticated user ID from the request context.
// The auth middleware sets this via a context key. We use a string type key
// to avoid import cycles.
type ctxUserIDKey struct{}

// SetUserIDCtx returns a context with the userID embedded.
func SetUserIDCtx(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxUserIDKey{}, userID)
}

func userIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserIDKey{}).(string)
	return v
}

// statusRecorder captures the HTTP status code written by the downstream handler.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}
