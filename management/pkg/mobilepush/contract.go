// Package mobilepush translates JMAP push events (state-change notifications
// from vulos-mail) into APNs (Apple Push Notification service) and FCM
// (Firebase Cloud Messaging) HTTP v1 push notifications.
//
// # Architecture
//
// Two primary types:
//
//   - Subscriber manages the device-token registry in a SQLite table.
//   - Dispatcher accepts a JMAPPushEvent and fans out to all registered tokens
//     for the account via APNs HTTP/2 (JWT-authenticated) and FCM HTTP v1.
//
// # Environment variables (required in VULOS_ENV=prod)
//
//   - APNS_KEY_ID         — APNs signing key ID (10-character string)
//   - APNS_TEAM_ID        — Apple Team ID (10-character string)
//   - APNS_KEY_P8_PATH    — filesystem path to the .p8 private-key file
//   - FCM_PROJECT_ID      — Firebase project ID
//   - FCM_SERVICE_ACCOUNT_PATH — filesystem path to the service-account JSON
//
// In non-prod environments, missing env vars are logged and the dispatcher
// becomes a no-op for that platform (fail-open in dev/local; fail-closed in prod).
//
// # Pure-Go constraint
//
// Uses modernc.org/sqlite (no CGO). Uses golang.org/x/crypto/ssh/knownhosts
// only for the ES256 JWT signer (no CGO path).
package mobilepush

import (
	"errors"
	"time"
)

// Platform identifies the push notification platform for a device token.
type Platform string

const (
	PlatformAPNs Platform = "apns"
	PlatformFCM  Platform = "fcm"
)

// DeviceToken is one registered device for an account.
type DeviceToken struct {
	ID        int64     `json:"id"`
	AccountID string    `json:"account_id"`
	Token     string    `json:"token"`
	Platform  Platform  `json:"platform"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// JMAPPushEvent is the subset of a JMAP state-change push payload that we
// translate into a mobile notification. Only the fields we actually use are
// included; the full RFC 8620 push message may contain more.
type JMAPPushEvent struct {
	// AccountID is the JMAP account that experienced the state change.
	AccountID string `json:"accountId"`

	// TypeStates maps data-type name to the new state string, e.g.:
	//   { "Email": "abc123", "Mailbox": "def456" }
	TypeStates map[string]string `json:"typeStates"`
}

// DispatchResult summarises the outcome of one Dispatch call.
type DispatchResult struct {
	AccountID    string
	TokensSent   int
	TokensFailed int
	Errors       []error
}

// Sentinel errors.
var (
	// ErrMissingConfig is returned when a required env var is absent in prod.
	ErrMissingConfig = errors.New("mobilepush: required env var missing (fail-closed in prod)")

	// ErrInvalidPlatform is returned when an unrecognised platform string is provided.
	ErrInvalidPlatform = errors.New("mobilepush: invalid platform (must be apns or fcm)")

	// ErrInvalidToken is returned when a token or account ID is empty.
	ErrInvalidToken = errors.New("mobilepush: token and account_id must not be empty")
)
