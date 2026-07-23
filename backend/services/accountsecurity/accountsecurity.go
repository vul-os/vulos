// Package accountsecurity implements account-takeover (ATO) monitoring for a
// single Vulos box owner.
//
// It hooks on sensitive account actions (password change, recovery/master-key
// reset, passkey changes, bulk data export, mass file download), keeps a
// short rolling log of them, and evaluates two anomaly rules against that
// log. When a rule fires, it records an alert and sends a cross-device
// notification (via backend/services/notify — this package never talks to a
// browser or mail server directly) so the owner can review and, if it wasn't
// them, lock the account from any device.
//
// Ported from management/pkg/security's ato.go/store.go (a former
// multi-tenant control-plane package) and trimmed to single-owner semantics:
// no CP fleet, no cross-account admin dashboard, no WAF/bot/CT/honeypot/
// egress sub-features — those are out of scope for a personal box and are not
// ported. Session/credential handling is NOT duplicated here: this package
// never touches passwords or session tokens directly. HTTP handlers
// (routes_accountsecurity.go) that need to revoke sessions call
// backend/services/auth directly and then tell this package the alert was
// resolved.
//
// # Anomaly rules
//
//   - Rule 1 ("multiple_sensitive_actions"): >=3 sensitive actions by the same
//     user within a 30-minute window.
//   - Rule 2 ("new_ip_sensitive_action"): a sensitive action performed within
//     1 hour of the first-ever sighting of the client IP it came from.
//
// # Hooking a sensitive action
//
// Call Service.RecordAndCheck after the action has already succeeded (never
// gate the action itself on this — ATO monitoring observes, it does not
// authorise; that job belongs to services/stepup). Example:
//
//	if alert, err := acctSecSvc.RecordAndCheck(ctx, userID, accountsecurity.ActionPasswordChange, clientIP, userAgent); err != nil {
//		log.Printf("accountsecurity: %v", err)
//	} else if alert != nil {
//		log.Printf("accountsecurity: alert %d raised (%s)", alert.ID, alert.Reason)
//	}
//
// NOTE ON INTEGRATION SCOPE: wiring this call into vulos's actual sensitive
// endpoints (services/auth's ChangePassword/ResetPasswordWithPhrase/
// RecoverAccountWithPhrase/RewrapMasterKeyOnPasswordChange, services/passkeys'
// add/remove, and the profile/data export handler in
// cmd/server/routes_export.go) touches existing files outside this fold's
// new-files-only scope and is left as a small follow-up (one import + one
// call per call site) — see the fold's integration manifest NOTES.
package accountsecurity

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"vulos/backend/services/notify"
)

// Action identifies a sensitive account action being monitored.
type Action string

const (
	ActionPasswordChange Action = "password_change" // auth.Store.ChangePassword / ResetPasswordWithSessionKey
	ActionRecoveryUsed   Action = "recovery_used"   // auth.Store.RecoverAccountWithPhrase / ResetPasswordWithPhrase
	ActionMasterKeyReset Action = "masterkey_reset" // auth.Store.ProvisionMasterKey / RewrapMasterKeyOnPasswordChange
	ActionPasskeyChange  Action = "passkey_change"  // passkeys add/remove
	ActionRoleChange     Action = "role_change"     // auth.Store.SetRole
	ActionBulkExport     Action = "bulk_export"     // profile/data export
	ActionMassDownload   Action = "mass_download"   // large multi-file download
)

// SensitiveActions lists every action this package monitors. Exported so
// call sites and the frontend can validate/label against a single source of
// truth.
var SensitiveActions = []Action{
	ActionPasswordChange,
	ActionRecoveryUsed,
	ActionMasterKeyReset,
	ActionPasskeyChange,
	ActionRoleChange,
	ActionBulkExport,
	ActionMassDownload,
}

// anomalyWindowMins is Rule 1's window: >=3 sensitive actions inside this
// many minutes trips an alert.
const anomalyWindowMins = 30

// anomalyThreshold is Rule 1's count.
const anomalyThreshold = 3

// newIPWindow is Rule 2's window: a sensitive action within this long of the
// first-ever sighting of its client IP trips an alert.
const newIPWindow = 1 * time.Hour

// feedLimit bounds how much history the feed endpoint returns.
const feedLimit = 100

// SensitiveActionRecord is one row from the raw sensitive-action log.
type SensitiveActionRecord struct {
	ID        int64     `json:"id"`
	Ts        time.Time `json:"ts"`
	Action    string    `json:"action"`
	ClientIP  string    `json:"client_ip"`
	UserAgent string    `json:"user_agent,omitempty"`
}

// Alert is a sensitive action that tripped an anomaly rule.
type Alert struct {
	ID         int64      `json:"id"`
	Ts         time.Time  `json:"ts"`
	Action     string     `json:"action"`
	ClientIP   string     `json:"client_ip"`
	Reason     string     `json:"reason"` // "multiple_sensitive_actions" | "new_ip_sensitive_action"
	Status     string     `json:"status"` // "pending" | "dismissed" | "locked"
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// Feed is the view model returned to the owner's Settings -> Security panel.
type Feed struct {
	Actions []SensitiveActionRecord `json:"actions"`
	Alerts  []Alert                 `json:"alerts"`
}

// Service is the accountsecurity subsystem: the sensitive-action log, the
// anomaly engine, and cross-device alerting. Safe for concurrent use (the
// underlying *sql.DB serialises writes; notify.Service is itself
// concurrency-safe).
type Service struct {
	store  *store
	notify *notify.Service // may be nil in tests / degraded boot: alerts are still recorded, just not broadcast
}

// Open opens (or creates) the accountsecurity SQLite database under dbDir
// (typically datadir.Join("db"), the same directory auth/files/etc. use) and
// applies embedded migrations. notifySvc is used to broadcast cross-device
// alerts (backend/services/notify, NOT a new notifier); it may be nil, in
// which case anomalies are still recorded and visible in the feed, just not
// pushed.
func Open(dbDir string, notifySvc *notify.Service) (*Service, error) {
	st, err := openStore(filepath.Join(dbDir, "accountsecurity.db"))
	if err != nil {
		return nil, fmt.Errorf("accountsecurity: open: %w", err)
	}
	return &Service{store: st, notify: notifySvc}, nil
}

// Close closes the underlying database.
func (s *Service) Close() error { return s.store.Close() }

// RecordAndCheck logs a sensitive action for userID and evaluates the
// anomaly rules against the recent log. It returns the Alert it raised, or
// nil if nothing looked anomalous. Call this AFTER the action has already
// succeeded — this is monitoring, not an authorisation gate (use
// services/stepup for that).
func (s *Service) RecordAndCheck(ctx context.Context, userID string, action Action, clientIP, userAgent string) (*Alert, error) {
	if userID == "" {
		return nil, fmt.Errorf("accountsecurity: empty user id")
	}
	if err := s.store.recordSensitiveAction(ctx, userID, action, clientIP, userAgent); err != nil {
		return nil, fmt.Errorf("accountsecurity: record: %w", err)
	}
	return s.checkAnomaly(ctx, userID, action, clientIP)
}

// checkAnomaly evaluates both rules in order and raises at most one alert
// per call (the first rule to fire wins — a burst that is ALSO from a new IP
// is still just one thing worth telling the owner about).
func (s *Service) checkAnomaly(ctx context.Context, userID string, action Action, clientIP string) (*Alert, error) {
	// Rule 1: >=3 sensitive actions in <30 min.
	count, err := s.store.countInWindow(ctx, userID, anomalyWindowMins)
	if err != nil {
		return nil, fmt.Errorf("accountsecurity: window count: %w", err)
	}
	if count >= anomalyThreshold {
		return s.raiseAlert(ctx, userID, action, clientIP, "multiple_sensitive_actions",
			fmt.Sprintf("%d sensitive account changes in the last %d minutes", count, anomalyWindowMins))
	}

	// Rule 2: sensitive change <1h after the first-ever sighting of this IP.
	// "First-ever" is approximated as "no sighting of this IP before the
	// start of the 1h window" — i.e. every sighting of it (including the one
	// just recorded) falls inside the window.
	cutoff := time.Now().UTC().Add(-newIPWindow).Format(rfc)
	seenInWindow, err := s.store.countFromIPSince(ctx, userID, clientIP, cutoff)
	if err != nil {
		return nil, fmt.Errorf("accountsecurity: ip count: %w", err)
	}
	if seenInWindow <= 1 && clientIP != "" {
		return s.raiseAlert(ctx, userID, action, clientIP, "new_ip_sensitive_action",
			"a sensitive account change from a device/network we haven't seen recently")
	}

	return nil, nil
}

// raiseAlert persists the alert and, if a notify.Service is attached,
// broadcasts a cross-device alert (in-app via WebSocket to every connected
// session + Web Push to every registered device — see notify.Service's
// maybeWebPush, which fans out for any user-scoped notification). The alert
// text points the owner at Settings -> Security rather than embedding a
// bespoke link, since that is where the actual dismiss/lock controls live.
func (s *Service) raiseAlert(ctx context.Context, userID string, action Action, clientIP, reason, humanReason string) (*Alert, error) {
	var notifID string
	if s.notify != nil {
		n := s.notify.SendNotification(notify.Notification{
			Title:    "Security alert: review a recent account change",
			Body:     fmt.Sprintf("%s (action: %s, from %s). Open Settings -> Security to review, or lock your account if this wasn't you.", humanReason, action, clientIPOrUnknown(clientIP)),
			Level:    notify.LevelUrgent,
			Source:   "accountsecurity",
			Action:   "settings:security",
			Type:     notify.TypeAlert,
			Subtype:  "account_security",
			Priority: notify.PriorityHigh,
			UserID:   userID,
		})
		if n != nil {
			notifID = n.ID
		}
	}

	id, err := s.store.insertAlert(ctx, userID, action, clientIP, reason, notifID)
	if err != nil {
		return nil, fmt.Errorf("accountsecurity: insert alert: %w", err)
	}
	return &Alert{
		ID:       id,
		Ts:       time.Now().UTC(),
		Action:   string(action),
		ClientIP: clientIP,
		Reason:   reason,
		Status:   "pending",
	}, nil
}

func clientIPOrUnknown(ip string) string {
	if ip == "" {
		return "an unknown location"
	}
	return ip
}

// Feed returns userID's recent sensitive-action log and alerts, newest
// first, for the Settings -> Security panel.
func (s *Service) Feed(ctx context.Context, userID string) (*Feed, error) {
	actions, err := s.store.recentSensitiveActions(ctx, userID, feedLimit)
	if err != nil {
		return nil, fmt.Errorf("accountsecurity: recent actions: %w", err)
	}
	alerts, err := s.store.recentAlerts(ctx, userID, feedLimit)
	if err != nil {
		return nil, fmt.Errorf("accountsecurity: recent alerts: %w", err)
	}
	return &Feed{Actions: actions, Alerts: alerts}, nil
}

// ErrNotOwner is returned by Dismiss/ResolveLocked when the calling user does
// not own the target alert (or it does not exist) — fail-closed, never leak
// or mutate another profile's alert.
var ErrNotOwner = fmt.Errorf("accountsecurity: not the alert owner")

// Dismiss marks alertID reviewed ("this was me, no action needed"). Only the
// alert's own owner may dismiss it.
func (s *Service) Dismiss(ctx context.Context, userID string, alertID int64) error {
	owner, err := s.store.alertOwner(ctx, alertID)
	if err != nil {
		return fmt.Errorf("accountsecurity: lookup: %w", err)
	}
	if owner == "" || owner != userID {
		return ErrNotOwner
	}
	return s.store.setAlertStatus(ctx, alertID, "dismissed")
}

// ResolveLocked marks alertID resolved as "locked" — the caller (routes
// layer) is expected to have ALREADY revoked the owner's sessions via
// services/auth before calling this; this just records that the owner chose
// the lock response. Only the alert's own owner may resolve it this way.
func (s *Service) ResolveLocked(ctx context.Context, userID string, alertID int64) error {
	owner, err := s.store.alertOwner(ctx, alertID)
	if err != nil {
		return fmt.Errorf("accountsecurity: lookup: %w", err)
	}
	if owner == "" || owner != userID {
		return ErrNotOwner
	}
	return s.store.setAlertStatus(ctx, alertID, "locked")
}
