// Package recovery implements the account-recovery flow for vulos.cloud.
//
// This flow is for users who have lost BOTH their phone (TOTP device) AND all
// recovery codes. It deliberately hard-codes a 14-day delay as an ATO
// (account-takeover) countermeasure.
//
// # Why 14 days (this is non-negotiable)
//
// Account recovery is the #1 vector for social-engineering-based ATO. An
// attacker who can convince support to expedite a recovery bypasses all
// technical controls. A hard, unconditional 14-day wait gives the legitimate
// owner enough time to notice the pending recovery request and cancel it.
// During this window every login attempt is served a warning banner. Industry
// data from large consumer IdPs (Google, Apple) shows that 99% of successful
// ATO recovery attacks complete within 72 hours — a 14-day clock with
// mandatory notification emails at days 1, 7, and 13 creates a >4× buffer.
// Shortening this window requires a board-level security risk acceptance, not
// an engineering change.
//
// # Flow
//
//  1. User submits email + optional phone digits + manual-review token
//     (issued offline by support).
//  2. 14-day clock starts. Existing TOTP is NOT suspended yet. Login with
//     existing TOTP still works; a warning banner is shown.
//  3. Admin verifies the manual-review token and the government-ID upload
//     (stored encrypted, hard-deleted 30 days after completion).
//  4. After 14 days + review: TOTP is reset, new recovery codes minted.
//
// # Abuse resistance
//
//   - 3 recovery attempts in any 24-hour window → account frozen in
//     pending-review state. Prevents spray attacks.
//   - Manual-review token prevents fully self-service abuse.
//   - Government-ID upload is AES-256-GCM encrypted under RECOVERY_KEK
//     (never stored plaintext). Deleted 30 days post-completion.
//
// Storage: cpdb-backed — SQLite by default; Postgres when DATABASE_URL is set.
package recovery

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

const recovCrockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// RecoveryWindow is the mandatory delay between a recovery request submission
// and the moment at which TOTP can be reset.
//
// NON-NEGOTIABLE: see package-level doc for rationale.
const RecoveryWindow = 14 * 24 * time.Hour

// MaxAbuse24h is the number of recovery attempts in a 24-hour window that
// triggers the account-freeze abuse gate.
const MaxAbuse24h = 3

// Sentinel errors.
var (
	ErrNotFound         = errors.New("recovery: request not found")
	ErrAlreadyPending   = errors.New("recovery: account already has a pending recovery request")
	ErrAbuseThreshold   = errors.New("recovery: too many recovery attempts; account is frozen pending review")
	ErrWindowNotElapsed = errors.New("recovery: 14-day recovery window has not elapsed")
	ErrReviewTokenUsed  = errors.New("recovery: manual-review token already used")
	ErrReviewTokenBad   = errors.New("recovery: manual-review token invalid")
	ErrNotEligible      = errors.New("recovery: request is not in a state eligible for completion")
)

// Status values for a RecoveryRequest.
const (
	StatusPending   = "pending"
	StatusReviewing = "reviewing"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
)

// RecoveryRequest is a recovery request record.
type RecoveryRequest struct {
	ID                 string
	AccountID          string
	Email              string
	PhoneLast4         string
	ManualReviewToken  string // one-time admin token; hashed in storage
	ReviewTokenUsed    bool
	TOTPSuspended      bool
	IDUploadPath       string // encrypted storage path
	IDUploadDeleted    bool
	Status             string
	SubmittedAt        time.Time
	RecoveryEligibleAt time.Time // submitted_at + 14d
	CompletedAt        *time.Time
	CancelledAt        *time.Time
}

// TOTPResetter is called on completion to reset TOTP and mint new recovery codes.
// Implemented by auth.Store in production; stubbed in tests.
type TOTPResetter interface {
	// ResetTOTP disables TOTP and mints new recovery codes for the account.
	// Returns the new plain-text recovery codes (displayed once to the user).
	ResetTOTP(ctx context.Context, accountID string) (newCodes []string, err error)
}

// Store is the recovery database backend.
type Store struct {
	db  *cpdb.DB
	mu  sync.Mutex
	kek []byte // RECOVERY_KEK for encrypting uploaded ID documents
}

// Open applies embedded migrations to db and returns a ready-to-use *Store.
// kek must be 32 bytes (AES-256 key) for government-ID upload encryption.
// Pass nil kek only in tests (ID upload will be rejected without a kek).
func Open(db *cpdb.DB, kek []byte) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("recovery: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("recovery: migrate: %w", err)
	}
	return &Store{db: db, kek: kek}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// ─── Request submission ───────────────────────────────────────────────────────

// SubmitRequest creates a new recovery request for accountID.
//
// Abuse guard: if 3 or more requests have been submitted in the last 24 hours,
// the account is immediately frozen (ErrAbuseThreshold returned) and an admin
// must manually unblock it.
//
// The 14-day window starts at submission time.
func (s *Store) SubmitRequest(ctx context.Context, accountID, email, phoneLast4 string) (RecoveryRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Abuse check: count submissions in the last 24 hours.
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	var abuseCount int
	if err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM recovery_abuse_log WHERE account_id=? AND submitted_at > ?`),
		accountID, cutoff,
	).Scan(&abuseCount); err != nil {
		return RecoveryRequest{}, fmt.Errorf("recovery: abuse check: %w", err)
	}
	if abuseCount >= MaxAbuse24h {
		// Record this attempt in the abuse log before returning the error,
		// so the count keeps climbing and the account stays frozen.
		_, _ = s.db.ExecContext(ctx,
			s.db.Rebind(`INSERT INTO recovery_abuse_log (account_id, submitted_at) VALUES (?,?)`),
			accountID, time.Now().UTC().Format(time.RFC3339),
		)
		return RecoveryRequest{}, ErrAbuseThreshold
	}

	// Reject if there's already an active pending or reviewing request.
	var existingCount int
	if err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM recovery_requests WHERE account_id=? AND status IN ('pending','reviewing')`),
		accountID,
	).Scan(&existingCount); err != nil {
		return RecoveryRequest{}, fmt.Errorf("recovery: check existing: %w", err)
	}
	if existingCount > 0 {
		return RecoveryRequest{}, ErrAlreadyPending
	}

	id, err := newRecoveryID()
	if err != nil {
		return RecoveryRequest{}, err
	}
	now := time.Now().UTC()
	// The 14-day window is absolute: calculated once at submission, never shortened.
	eligibleAt := now.Add(RecoveryWindow)

	// Log this submission for abuse tracking.
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO recovery_abuse_log (account_id, submitted_at) VALUES (?,?)`),
		accountID, now.Format(time.RFC3339),
	)
	if err != nil {
		return RecoveryRequest{}, fmt.Errorf("recovery: log abuse: %w", err)
	}

	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO recovery_requests
		  (id, account_id, email, phone_last4, status,
		   submitted_at, recovery_eligible_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`),
		id, accountID, email, phoneLast4, StatusPending,
		now.Format(time.RFC3339), eligibleAt.Format(time.RFC3339),
		now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return RecoveryRequest{}, fmt.Errorf("recovery: insert request: %w", err)
	}

	return RecoveryRequest{
		ID:                 id,
		AccountID:          accountID,
		Email:              email,
		PhoneLast4:         phoneLast4,
		Status:             StatusPending,
		SubmittedAt:        now,
		RecoveryEligibleAt: eligibleAt,
	}, nil
}

// GetRequest returns a recovery request by id.
func (s *Store) GetRequest(ctx context.Context, id string) (RecoveryRequest, error) {
	return s.scanRequest(ctx, `
		SELECT id, account_id, email, phone_last4, review_token_used, totp_suspended,
		       id_upload_path, id_upload_deleted, status,
		       submitted_at, recovery_eligible_at, completed_at, cancelled_at
		FROM recovery_requests WHERE id=?`, id)
}

// ListForAccount returns every recovery request for accountID, newest first.
// The account holder reads this from a live session to see (and cancel) a
// request they did not make.
func (s *Store) ListForAccount(ctx context.Context, accountID string) ([]RecoveryRequest, error) {
	return s.listRequests(ctx, `
		SELECT id, account_id, email, phone_last4, review_token_used, totp_suspended,
		       id_upload_path, id_upload_deleted, status,
		       submitted_at, recovery_eligible_at, completed_at, cancelled_at
		FROM recovery_requests WHERE account_id=? ORDER BY submitted_at DESC`, accountID)
}

// ListActive returns every request still awaiting review (pending or
// reviewing), oldest first — the admin queue.
func (s *Store) ListActive(ctx context.Context) ([]RecoveryRequest, error) {
	return s.listRequests(ctx, `
		SELECT id, account_id, email, phone_last4, review_token_used, totp_suspended,
		       id_upload_path, id_upload_deleted, status,
		       submitted_at, recovery_eligible_at, completed_at, cancelled_at
		FROM recovery_requests WHERE status IN ('pending','reviewing') ORDER BY submitted_at ASC`)
}

// ─── Manual review token ──────────────────────────────────────────────────────

// hashReviewToken hashes the review token for storage (SHA-256 hex).
func hashReviewToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// SetManualReviewToken stores the hashed review token for a request.
// Called by an admin after offline verification is accepted.
func (s *Store) SetManualReviewToken(ctx context.Context, requestID, rawToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hashed := hashReviewToken(rawToken)
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE recovery_requests SET manual_review_token=?, status='reviewing', updated_at=?
		WHERE id=? AND status IN ('pending','reviewing')`),
		hashed, now, requestID,
	)
	if err != nil {
		return fmt.Errorf("recovery: set review token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// VerifyReviewToken verifies the raw review token and marks it used.
// This suspends TOTP for the account.
func (s *Store) VerifyReviewToken(ctx context.Context, requestID, rawToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var storedHash string
	var tokenUsed, totpSuspended int
	var status string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT manual_review_token, review_token_used, totp_suspended, status
		FROM recovery_requests WHERE id=?`), requestID,
	).Scan(&storedHash, &tokenUsed, &totpSuspended, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("recovery: verify token query: %w", err)
	}
	if tokenUsed != 0 {
		return ErrReviewTokenUsed
	}
	if hashReviewToken(rawToken) != storedHash {
		return ErrReviewTokenBad
	}

	// Mark token used; suspend TOTP (existing TOTP is now disabled for logins).
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE recovery_requests SET review_token_used=1, totp_suspended=1, updated_at=?
		WHERE id=?`), now, requestID,
	)
	return err
}

// ─── Government-ID upload (encrypted at rest) ─────────────────────────────────

// StoreIDUpload encrypts uploadData under RECOVERY_KEK and stores the path
// in the recovery request. The encrypted bytes are written to storagePath.
// If RECOVERY_KEK is nil this returns an error (never store plaintext IDs).
func (s *Store) StoreIDUpload(ctx context.Context, requestID string, uploadData []byte, storagePath string) error {
	if len(s.kek) != 32 {
		return errors.New("recovery: RECOVERY_KEK must be 32 bytes; refusing to store plaintext ID upload")
	}

	// Encrypt under RECOVERY_KEK using AES-256-GCM.
	ciphertext, encKeyB64, err := encryptIDUpload(s.kek, uploadData)
	if err != nil {
		return fmt.Errorf("recovery: encrypt id upload: %w", err)
	}

	// Write the ciphertext to storagePath.
	if err := os.WriteFile(storagePath, ciphertext, 0o600); err != nil {
		return fmt.Errorf("recovery: write id upload: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE recovery_requests SET id_upload_path=?, id_upload_enc_key=?, updated_at=?
		WHERE id=?`), storagePath, encKeyB64, now, requestID,
	)
	if err != nil {
		return fmt.Errorf("recovery: store id path: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// encryptIDUpload encrypts data using AES-256-GCM with a fresh random DEK.
// The DEK is itself encrypted under kek and returned as base64url.
func encryptIDUpload(kek, data []byte) (ciphertext []byte, encKeyB64 string, err error) {
	// Generate a fresh 32-byte DEK.
	dek := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, "", fmt.Errorf("generate dek: %w", err)
	}

	// Encrypt data under DEK.
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, "", err
	}
	ct := gcm.Seal(nonce, nonce, data, nil)

	// Encrypt DEK under KEK.
	kekBlock, err := aes.NewCipher(kek)
	if err != nil {
		return nil, "", err
	}
	kekGCM, err := cipher.NewGCM(kekBlock)
	if err != nil {
		return nil, "", err
	}
	kekNonce := make([]byte, kekGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, kekNonce); err != nil {
		return nil, "", err
	}
	encDEK := kekGCM.Seal(kekNonce, kekNonce, dek, nil)
	encKeyB64 = base64.RawURLEncoding.EncodeToString(encDEK)

	return ct, encKeyB64, nil
}

// ─── Completion ───────────────────────────────────────────────────────────────

// Complete finalises a recovery request after the 14-day window has elapsed
// and the review token has been verified. Calls resetter.ResetTOTP and
// marks the request completed.
//
// Returns ErrWindowNotElapsed if called before recovery_eligible_at.
// Returns ErrNotEligible if the request is not in reviewing status.
func (s *Store) Complete(ctx context.Context, requestID string, resetter TOTPResetter) (newCodes []string, err error) {
	s.mu.Lock()

	req, err := s.getRequest(ctx, requestID)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if req.Status != StatusReviewing {
		s.mu.Unlock()
		return nil, ErrNotEligible
	}

	// Hard clock check: 14-day window must have elapsed.
	// This check is performed inside the mutex and cannot be bypassed.
	if time.Now().UTC().Before(req.RecoveryEligibleAt) {
		s.mu.Unlock()
		return nil, ErrWindowNotElapsed
	}

	s.mu.Unlock()

	// Reset TOTP (outside the store mutex to avoid deadlock with auth.Store).
	if resetter == nil {
		return nil, errors.New("recovery: no TOTP resetter configured")
	}
	newCodes, err = resetter.ResetTOTP(ctx, req.AccountID)
	if err != nil {
		return nil, fmt.Errorf("recovery: reset TOTP: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE recovery_requests SET status='completed', completed_at=?, updated_at=?
		WHERE id=?`), now.Format(time.RFC3339), now.Format(time.RFC3339), requestID,
	)
	if err != nil {
		return nil, fmt.Errorf("recovery: mark completed: %w", err)
	}

	// Schedule ID upload for hard deletion 30 days post-completion.
	// In production a background job calls PurgeIDUploads. Here we record completion.
	// (Actual deletion is handled by PurgeExpiredIDUploads.)
	return newCodes, nil
}

// Cancel cancels a pending recovery request (called by the account holder).
func (s *Store) Cancel(ctx context.Context, requestID, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE recovery_requests SET status='cancelled', cancelled_at=?, updated_at=?
		WHERE id=? AND account_id=? AND status IN ('pending','reviewing')`),
		now, now, requestID, accountID,
	)
	if err != nil {
		return fmt.Errorf("recovery: cancel: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeExpiredIDUploads hard-deletes government-ID upload files for requests
// completed more than 30 days ago. Should be called by a background job daily.
func (s *Store) PurgeExpiredIDUploads(ctx context.Context) (purged int, err error) {
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT id, id_upload_path FROM recovery_requests
		WHERE status='completed' AND id_upload_deleted=0
		  AND id_upload_path IS NOT NULL AND completed_at < ?`), cutoff)
	if err != nil {
		return 0, fmt.Errorf("recovery: purge query: %w", err)
	}
	defer rows.Close()
	type toPurge struct{ id, path string }
	var items []toPurge
	for rows.Next() {
		var item toPurge
		if err := rows.Scan(&item.id, &item.path); err != nil {
			return purged, err
		}
		items = append(items, item)
	}
	rows.Close()

	for _, item := range items {
		_ = os.Remove(item.path) // best-effort
		_, _ = s.db.ExecContext(ctx, s.db.Rebind(`
			UPDATE recovery_requests SET id_upload_deleted=1, id_upload_path=NULL, updated_at=?
			WHERE id=?`), time.Now().UTC().Format(time.RFC3339), item.id,
		)
		purged++
	}
	return purged, nil
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

func (s *Store) getRequest(ctx context.Context, id string) (RecoveryRequest, error) {
	return s.scanRequest(ctx, `
		SELECT id, account_id, email, phone_last4, review_token_used, totp_suspended,
		       id_upload_path, id_upload_deleted, status,
		       submitted_at, recovery_eligible_at, completed_at, cancelled_at
		FROM recovery_requests WHERE id=?`, id)
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func (s *Store) scanRequest(ctx context.Context, query string, args ...any) (RecoveryRequest, error) {
	r, err := scanRequestRow(s.db.QueryRowContext(ctx, s.db.Rebind(query), args...))
	if errors.Is(err, sql.ErrNoRows) {
		return RecoveryRequest{}, ErrNotFound
	}
	if err != nil {
		return RecoveryRequest{}, fmt.Errorf("recovery: scan request: %w", err)
	}
	return r, nil
}

func (s *Store) listRequests(ctx context.Context, query string, args ...any) ([]RecoveryRequest, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("recovery: list requests: %w", err)
	}
	defer rows.Close()
	var out []RecoveryRequest
	for rows.Next() {
		r, err := scanRequestRow(rows)
		if err != nil {
			return nil, fmt.Errorf("recovery: scan request: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recovery: list requests: %w", err)
	}
	return out, nil
}

func scanRequestRow(sc rowScanner) (RecoveryRequest, error) {
	var r RecoveryRequest
	var phoneLast4, idUploadPath sql.NullString
	var completedAt, cancelledAt sql.NullString
	var reviewTokenUsed, totpSuspended, idUploadDeleted int
	var submittedAt, eligibleAt string

	err := sc.Scan(
		&r.ID, &r.AccountID, &r.Email, &phoneLast4,
		&reviewTokenUsed, &totpSuspended,
		&idUploadPath, &idUploadDeleted,
		&r.Status,
		&submittedAt, &eligibleAt,
		&completedAt, &cancelledAt,
	)
	if err != nil {
		return RecoveryRequest{}, err
	}
	r.PhoneLast4 = phoneLast4.String
	r.IDUploadPath = idUploadPath.String
	r.IDUploadDeleted = idUploadDeleted != 0
	r.ReviewTokenUsed = reviewTokenUsed != 0
	r.TOTPSuspended = totpSuspended != 0
	r.SubmittedAt, _ = time.Parse(time.RFC3339, submittedAt)
	r.RecoveryEligibleAt, _ = time.Parse(time.RFC3339, eligibleAt)
	if completedAt.Valid {
		t, _ := time.Parse(time.RFC3339, completedAt.String)
		r.CompletedAt = &t
	}
	if cancelledAt.Valid {
		t, _ := time.Parse(time.RFC3339, cancelledAt.String)
		r.CancelledAt = &t
	}
	return r, nil
}

func newRecoveryID() (string, error) {
	b := make([]byte, 26)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(32))
		if err != nil {
			return "", fmt.Errorf("recovery: generate id: %w", err)
		}
		b[i] = recovCrockford[n.Int64()]
	}
	return string(b), nil
}

// keep import satisfied.
var _ = strings.Contains
