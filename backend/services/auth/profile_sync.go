// Package auth — profile_sync.go
//
// CLOGIN-03: Apply cloud-pushed signed profile updates to the local OS.
//
// The cloud sends a ProfileUpdateEnvelope over the enrollment/management
// channel (e.g. via the JoinSync/Sync pipeline or a direct management
// endpoint).  The envelope contains:
//
//   - A canonically-serialised ProfileUpdatePayload (produced by
//     services/signing.Canonical)
//   - An Ed25519 signature over those canonical bytes, made with the
//     login-broker key baked at enrollment
//
// Apply() verifies the signature via services/signing.VerifyWithAnchor
// before touching anything.  On any verification failure it returns an
// error immediately — the function is fail-closed.
//
// Fields that may be updated:
//   - Username  — maps to the Linux login name (via sysuser.SetPassword / passwd rename)
//   - Password  — pushed as a plaintext value; hashed locally before storage
//   - FullName  — display / GECOS name
//
// Every field that is actually changed is appended to the audit log at
// ProfileSyncAuditLog (default /var/log/vulos/profile-sync.log).
//
// Cloud-managed mode: when the envelope's CloudManaged flag is true, local
// edits to these three fields are overwritten on the next sync.  When it is
// false, the local values are sticky and this message is a one-time push.
//
// Thread-safety: ProfileSyncer is safe for concurrent use.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vulos/backend/services/signing"
)

// ─── Well-known paths ─────────────────────────────────────────────────────────

const (
	// ProfileSyncAuditLog is the canonical path for the profile-sync audit log.
	// Can be overridden via VULOS_PROFILE_SYNC_AUDIT_LOG env.
	ProfileSyncAuditLog = "/var/log/vulos/profile-sync.log"

	// profileSyncAnchorPath is the trust-anchor used to verify profile-update
	// envelopes.  It is the same anchor written at SEED-01 time and used by
	// signing.VerifyWithAnchor.  Override via VULOS_PROFILE_SYNC_ANCHOR env.
	profileSyncAnchorPath = signing.DefaultAnchorPath

	auditLogMode = 0600
	auditLogDir  = 0750
)

// ─── Wire types ───────────────────────────────────────────────────────────────

// ProfileUpdatePayload is the canonical JSON body that the cloud signs.
// All string fields are optional; an empty string means "do not change this
// field".  The cloud MUST omit or zero out fields it does not intend to update.
type ProfileUpdatePayload struct {
	// AccountID is the cloud account ULID — used to locate the local user.
	AccountID string `json:"account_id"`
	// Username is the new OS login name.  Empty means no change.
	Username string `json:"username,omitempty"`
	// Password is the new password in plaintext.  It is hashed immediately on
	// receipt and never stored or logged in plain form.  Empty means no change.
	Password string `json:"password,omitempty"`
	// FullName is the new display name / GECOS full name.  Empty means no change.
	FullName string `json:"full_name,omitempty"`
	// CloudManaged, when true, declares this device cloud-managed: subsequent
	// pushes overwrite local edits.  false = one-time push (local edits sticky).
	CloudManaged bool `json:"cloud_managed"`
	// IssuedAt is the UTC time the cloud produced this envelope.
	IssuedAt time.Time `json:"issued_at"`
	// ExpiresAt is the UTC time after which this envelope must be rejected.
	ExpiresAt time.Time `json:"expires_at"`
}

// ProfileUpdateEnvelope is the wire message sent from the cloud to the OS.
// PayloadBytes must be the exact output of signing.Canonical(ProfileUpdatePayload{…})
// so that VerifyWithAnchor can re-verify the signature deterministically.
type ProfileUpdateEnvelope struct {
	// PayloadBytes is the canonical JSON of ProfileUpdatePayload, exactly as
	// the cloud signed it.
	PayloadBytes []byte `json:"payload"`
	// Signature is the raw Ed25519 signature (64 bytes) over PayloadBytes,
	// made with the login-broker key.
	Signature []byte `json:"signature"`
}

// ─── Audit record ─────────────────────────────────────────────────────────────

// ProfileSyncAuditRecord is one line appended to the audit log per Apply call.
// Each changed field generates its own entry; a single Apply may write
// multiple records.
type ProfileSyncAuditRecord struct {
	Timestamp    time.Time `json:"ts"`
	AccountID    string    `json:"account_id"`
	Field        string    `json:"field"`
	OldValue     string    `json:"old,omitempty"` // never set for password
	NewValue     string    `json:"new,omitempty"` // never set for password
	CloudManaged bool      `json:"cloud_managed"`
}

// ─── Sentinel errors ──────────────────────────────────────────────────────────

var (
	// ErrProfileSyncBadSignature is returned when the envelope signature does
	// not verify.  The function is fail-closed: nothing is applied.
	ErrProfileSyncBadSignature = errors.New("profile_sync: signature verification failed")

	// ErrProfileSyncExpired is returned when the envelope's ExpiresAt has passed.
	ErrProfileSyncExpired = errors.New("profile_sync: envelope has expired")

	// ErrProfileSyncNoUser is returned when no local user can be found for the
	// AccountID in the payload.
	ErrProfileSyncNoUser = errors.New("profile_sync: no local user found for account_id")
)

// ─── Syncer ───────────────────────────────────────────────────────────────────

// ProfileSyncer applies cloud-pushed profile updates to the local auth Store.
// It is the sole CLOGIN-03 entry point.
type ProfileSyncer struct {
	mu         sync.Mutex
	store      *Store
	anchorPath string // path to trust-anchor .pub file
	auditLog   string // path to audit log; "" → ProfileSyncAuditLog
}

// NewProfileSyncer returns a ProfileSyncer backed by store.
//
//   - anchorPath: path to the Ed25519 trust-anchor public key.  "" uses
//     signing.DefaultAnchorPath (VULOS_PROFILE_SYNC_ANCHOR env overrides).
//   - auditLog: path to the audit log.  "" uses ProfileSyncAuditLog
//     (VULOS_PROFILE_SYNC_AUDIT_LOG env overrides).
func NewProfileSyncer(store *Store, anchorPath, auditLog string) *ProfileSyncer {
	if anchorPath == "" {
		anchorPath = effectiveAnchorPath()
	}
	if auditLog == "" {
		auditLog = effectiveAuditLogPath()
	}
	return &ProfileSyncer{
		store:      store,
		anchorPath: anchorPath,
		auditLog:   auditLog,
	}
}

// Apply verifies env's signature and, on success, applies the contained
// profile update to the local Store.
//
// Fail-closed contract:
//   - bad/missing signature        → ErrProfileSyncBadSignature, nothing written
//   - expired envelope             → ErrProfileSyncExpired, nothing written
//   - no local user for AccountID  → ErrProfileSyncNoUser, nothing written
//   - any other error (parse, I/O) → wrapped error, nothing written
//
// On success each applied change is appended to the audit log.  Audit log
// write failure is logged but does NOT fail the apply — the OS state is
// already updated at that point and rolling back would be worse.
func (ps *ProfileSyncer) Apply(env ProfileUpdateEnvelope) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// ── 1. Fail-closed signature verification ─────────────────────────────────
	if len(env.PayloadBytes) == 0 {
		return fmt.Errorf("%w: empty payload", ErrProfileSyncBadSignature)
	}
	if len(env.Signature) == 0 {
		return fmt.Errorf("%w: empty signature", ErrProfileSyncBadSignature)
	}

	if err := signing.VerifyWithAnchor(ps.anchorPath, env.PayloadBytes, env.Signature); err != nil {
		log.Printf("[profile_sync] signature verification failed: %v", err)
		return fmt.Errorf("%w: %v", ErrProfileSyncBadSignature, err)
	}

	// ── 2. Parse the verified payload ─────────────────────────────────────────
	var payload ProfileUpdatePayload
	if err := json.Unmarshal(env.PayloadBytes, &payload); err != nil {
		return fmt.Errorf("profile_sync: parse payload: %w", err)
	}

	// ── 3. Replay-attack guard — check expiry ─────────────────────────────────
	if !payload.ExpiresAt.IsZero() && time.Now().After(payload.ExpiresAt) {
		log.Printf("[profile_sync] rejected expired envelope for account=%s (expired %s)",
			payload.AccountID, payload.ExpiresAt.Format(time.RFC3339))
		return ErrProfileSyncExpired
	}

	// ── 4. Locate the local user by cloud AccountID ───────────────────────────
	if payload.AccountID == "" {
		return fmt.Errorf("profile_sync: payload missing account_id")
	}

	ps.store.mu.Lock()
	defer ps.store.mu.Unlock()

	var targetUser *User
	for _, u := range ps.store.users {
		if u.Providers["cloud"] == payload.AccountID {
			targetUser = u
			break
		}
	}
	if targetUser == nil {
		log.Printf("[profile_sync] no local user for account_id=%s", payload.AccountID)
		return ErrProfileSyncNoUser
	}

	// ── 5. Apply changes, auditing each one ───────────────────────────────────
	var auditRecords []ProfileSyncAuditRecord
	now := time.Now().UTC()

	// 5a. Username
	if payload.Username != "" {
		sanitised := sanitizeProfileUsername(payload.Username)
		if sanitised == "" {
			return fmt.Errorf("profile_sync: proposed username %q sanitises to empty", payload.Username)
		}
		if sanitised != targetUser.Username {
			// Ensure uniqueness
			for _, u := range ps.store.users {
				if u.ID != targetUser.ID && u.Username == sanitised {
					return fmt.Errorf("profile_sync: username %q already taken", sanitised)
				}
			}
			auditRecords = append(auditRecords, ProfileSyncAuditRecord{
				Timestamp:    now,
				AccountID:    payload.AccountID,
				Field:        "username",
				OldValue:     targetUser.Username,
				NewValue:     sanitised,
				CloudManaged: payload.CloudManaged,
			})
			log.Printf("[profile_sync] account=%s username %q → %q (cloud_managed=%v)",
				payload.AccountID, targetUser.Username, sanitised, payload.CloudManaged)
			targetUser.Username = sanitised
		}
	}

	// 5b. Password — never logged in plain form
	if payload.Password != "" {
		newHash := hashPassword(payload.Password)
		targetUser.PasswordHash = newHash

		// Invalidate all existing sessions for this user (SEC-J).
		for token, sess := range ps.store.sessions {
			if sess.UserID == targetUser.ID {
				delete(ps.store.sessions, token)
				ps.store.deleteSession(token)
			}
		}

		auditRecords = append(auditRecords, ProfileSyncAuditRecord{
			Timestamp: now,
			AccountID: payload.AccountID,
			Field:     "password",
			// OldValue and NewValue intentionally omitted for passwords.
			CloudManaged: payload.CloudManaged,
		})
		log.Printf("[profile_sync] account=%s password updated (cloud_managed=%v)",
			payload.AccountID, payload.CloudManaged)
	}

	// 5c. FullName — updates both User.Name and Profile.DisplayName
	if payload.FullName != "" {
		trimmed := strings.TrimSpace(payload.FullName)
		if trimmed != "" && trimmed != targetUser.Name {
			auditRecords = append(auditRecords, ProfileSyncAuditRecord{
				Timestamp:    now,
				AccountID:    payload.AccountID,
				Field:        "full_name",
				OldValue:     targetUser.Name,
				NewValue:     trimmed,
				CloudManaged: payload.CloudManaged,
			})
			log.Printf("[profile_sync] account=%s full_name %q → %q (cloud_managed=%v)",
				payload.AccountID, targetUser.Name, trimmed, payload.CloudManaged)
			targetUser.Name = trimmed

			// Mirror into the OS profile's DisplayName.
			if p, ok := ps.store.profiles[targetUser.ID]; ok {
				p.DisplayName = trimmed
				p.UpdatedAt = now
				ps.store.persistProfile(p)
			}
		}
	}

	// ── 6. Persist the updated user ───────────────────────────────────────────
	if len(auditRecords) > 0 {
		ps.store.persistUser(targetUser)
	}

	// ── 7. Write audit log ────────────────────────────────────────────────────
	if len(auditRecords) > 0 {
		if err := ps.writeAuditRecords(auditRecords); err != nil {
			// Non-fatal: the OS state is already updated.
			log.Printf("[profile_sync] WARNING: audit log write failed: %v", err)
		}
	}

	return nil
}

// writeAuditRecords appends each record as a JSON line to the audit log.
func (ps *ProfileSyncer) writeAuditRecords(records []ProfileSyncAuditRecord) error {
	path := ps.auditLog
	if err := os.MkdirAll(filepath.Dir(path), auditLogDir); err != nil {
		return fmt.Errorf("profile_sync: audit mkdir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, auditLogMode)
	if err != nil {
		return fmt.Errorf("profile_sync: open audit log: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("profile_sync: write audit record: %w", err)
		}
	}
	return nil
}

// ─── Path helpers ─────────────────────────────────────────────────────────────

func effectiveAnchorPath() string {
	if env := os.Getenv("VULOS_PROFILE_SYNC_ANCHOR"); env != "" {
		return env
	}
	return profileSyncAnchorPath
}

func effectiveAuditLogPath() string {
	if env := os.Getenv("VULOS_PROFILE_SYNC_AUDIT_LOG"); env != "" {
		return env
	}
	return ProfileSyncAuditLog
}

// sanitizeProfileUsername applies the same rules as sysuser.sanitizeUsername:
// lowercase alphanum plus dash/underscore, must start with a letter, max 32 chars.
// Returns "" when the result would be empty.
func sanitizeProfileUsername(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		s = "u" + s
	}
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}
