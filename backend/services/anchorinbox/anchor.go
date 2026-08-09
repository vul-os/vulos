// Package anchorinbox implements ANCHOR-01: the per-account anchor inbox
// allowance record.
//
// WHAT THIS PACKAGE ACTUALLY DOES — and what it does not:
//
// It records, in local SQLite, that an account is entitled to an anchor inbox
// of DefaultCapacityBytes (~1 GiB): a reliable landing zone for
// email/notifications that stays available even while the account's primary
// storage is in flux. That record is ALL this package creates. It does not
// create a bucket, does not hold Tigris (or any object-store) credentials, and
// moves no bytes anywhere. Creating the actual hosted bucket is a cloud-side
// action performed by the Vulos cloud control plane if and when the account
// connects to it; on a box that never connects to a Vulos cloud account, this
// local row is the only thing that exists.
//
// RELATIONSHIP TO THE STORAGE-MODE SELECTOR (internal/storagemode): none, and
// deliberately none in BOTH directions. The selector governs where THIS BOX
// puts the bytes it stores (default: local-fs, its own disk — see
// storagemode.DefaultMode). The anchor inbox is not local storage, so:
//
//   - Selecting local-fs or local-minio-sync does NOT relocate the anchor
//     inbox, because there is nothing local to relocate; and
//   - it equally does NOT cause this box to hand anything to Tigris. Nothing
//     here uploads, and no code in this repository reads BucketPrefix to route
//     data. Do not read the old "ALWAYS provisioned on Tigris regardless of the
//     storage backend selection" wording into it: that described an intent, not
//     this code, and it contradicted the local-by-default posture.
//
// So the anchor inbox is not an exception carved out of the user's storage
// choice — it is a hosted-account entitlement that only materialises for
// accounts that opt into the cloud at all. If that ever changes (i.e. something
// starts routing real bytes using BucketPrefix), this comment, the storage
// settings panel copy (src/core/settings/StoragePanel.jsx), and the
// storage-mode interaction all have to be revisited together.
//
// HTTP endpoints (register via RegisterAnchorHandlers):
//
//	POST /api/anchor-inbox/provision  — idempotently provisions the anchor inbox
//	GET  /api/anchor-inbox/status     — returns current AnchorInboxStatus
//
// Storage is pure-Go modernc.org/sqlite (CGO_ENABLED=0).
package anchorinbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
	"vulos/backend/internal/datadir"

	_ "modernc.org/sqlite" // pure-Go SQLite driver — never CGo
)

// DefaultCapacityBytes is the anchor inbox quota recorded for an account
// (1 GiB). It is the entitlement written into the local record — not space
// reserved or allocated anywhere by this box (see the package doc).
const DefaultCapacityBytes int64 = 1 << 30 // 1 GiB

// AnchorInboxStatus describes the current state of an account's anchor inbox
// RECORD. Every field below describes the local record; none of them is
// evidence that hosted storage exists (see the package doc).
type AnchorInboxStatus struct {
	AccountID string `json:"account_id"`
	// BucketPrefix is the deterministic name a hosted bucket WOULD carry
	// (derived from AccountID). Nothing in this repository creates a bucket
	// under it or routes data to it.
	BucketPrefix string `json:"bucket_prefix"`
	// CapacityBytes is the recorded entitlement, not reserved space.
	CapacityBytes int64 `json:"capacity_bytes"`
	// UsedBytes is only ever what a caller has reported; this package does not
	// measure any store.
	UsedBytes int64 `json:"used_bytes"`
	// Status is the state of the LOCAL RECORD: "pending" | "provisioned" |
	// "error". "provisioned" means the entitlement row exists — it does NOT
	// mean a hosted bucket has been created, which is a cloud-side step this
	// box neither performs nor observes.
	Status        string    `json:"status"`
	ProvisionedAt time.Time `json:"provisioned_at,omitempty"`
}

// AnchorStore is the SQLite-backed anchor-inbox state store.
// It is safe for concurrent use.
type AnchorStore struct {
	mu sync.RWMutex
	db *sql.DB
}

// DefaultDBPath returns $HOME/.vulos/db/anchorinbox.db.
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("anchorinbox: cannot resolve home dir: %w", err)
	}
	return datadir.Join("db", "anchorinbox.db"), nil
}

// Open opens (or creates) the anchor inbox database at dbPath and applies the
// schema. When dbPath is empty, DefaultDBPath is used.
func Open(dbPath string) (*AnchorStore, error) {
	if dbPath == "" {
		var err error
		dbPath, err = DefaultDBPath()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, fmt.Errorf("anchorinbox: mkdir: %w", err)
	}

	dsn := "file:" + dbPath +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("anchorinbox: open: %w", err)
	}
	// modernc/sqlite is not safe for unbounded concurrent writers on one file.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("anchorinbox: ping: %w", err)
	}

	const schema = `
CREATE TABLE IF NOT EXISTS anchor_inboxes (
    account_id      TEXT PRIMARY KEY,
    bucket_prefix   TEXT NOT NULL DEFAULT '',
    capacity_bytes  INTEGER NOT NULL DEFAULT 0,
    used_bytes      INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'pending',
    provisioned_at  TEXT NOT NULL DEFAULT ''
);
`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("anchorinbox: schema: %w", err)
	}
	return &AnchorStore{db: db}, nil
}

// Close releases the underlying database connection.
func (s *AnchorStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Provision ensures the account has an anchor inbox record. It is idempotent:
// if a record already exists for accountID it is returned unchanged.
//
// "Provision" here means only "write the entitlement row"; the returned
// Status is "provisioned" on that basis alone. No bucket is created and no
// object store is contacted — that step belongs to the Vulos cloud control
// plane if the account connects to it. Callers must not treat a successful
// Provision as proof that a hosted landing zone exists.
func (s *AnchorStore) Provision(ctx context.Context, accountID string) (AnchorInboxStatus, error) {
	if accountID == "" {
		return AnchorInboxStatus{}, errors.New("anchorinbox: Provision: accountID must not be empty")
	}

	// Check for existing record first (idempotent path).
	existing, err := s.Status(ctx, accountID)
	if err == nil {
		// Already provisioned — return existing.
		return existing, nil
	}
	// Only proceed if the error is "not found".
	if !errors.Is(err, ErrNotFound) {
		return AnchorInboxStatus{}, fmt.Errorf("anchorinbox: Provision: check existing: %w", err)
	}

	// New record: derive a deterministic bucket prefix from the account ID.
	bucketPrefix := "vulos-anchor-" + sanitiseAccountID(accountID)
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.db.ExecContext(ctx, `
INSERT INTO anchor_inboxes (account_id, bucket_prefix, capacity_bytes, used_bytes, status, provisioned_at)
VALUES (?, ?, ?, 0, 'provisioned', ?)
ON CONFLICT(account_id) DO NOTHING`,
		accountID, bucketPrefix, DefaultCapacityBytes, nowStr,
	)
	if err != nil {
		return AnchorInboxStatus{}, fmt.Errorf("anchorinbox: Provision: insert: %w", err)
	}

	return AnchorInboxStatus{
		AccountID:     accountID,
		BucketPrefix:  bucketPrefix,
		CapacityBytes: DefaultCapacityBytes,
		UsedBytes:     0,
		Status:        "provisioned",
		ProvisionedAt: now,
	}, nil
}

// ErrNotFound is returned by Status when no anchor inbox exists for the account.
var ErrNotFound = errors.New("anchorinbox: not found")

// Status returns the current anchor inbox state for accountID.
// Returns ErrNotFound if the account has not been provisioned yet.
func (s *AnchorStore) Status(ctx context.Context, accountID string) (AnchorInboxStatus, error) {
	if accountID == "" {
		return AnchorInboxStatus{}, errors.New("anchorinbox: Status: accountID must not be empty")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var (
		st            AnchorInboxStatus
		provisionedAt string
	)
	err := s.db.QueryRowContext(ctx, `
SELECT account_id, bucket_prefix, capacity_bytes, used_bytes, status, provisioned_at
FROM anchor_inboxes WHERE account_id = ?`, accountID).Scan(
		&st.AccountID,
		&st.BucketPrefix,
		&st.CapacityBytes,
		&st.UsedBytes,
		&st.Status,
		&provisionedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AnchorInboxStatus{}, ErrNotFound
	}
	if err != nil {
		return AnchorInboxStatus{}, fmt.Errorf("anchorinbox: Status: query: %w", err)
	}
	if provisionedAt != "" {
		if t, parseErr := time.Parse(time.RFC3339Nano, provisionedAt); parseErr == nil {
			st.ProvisionedAt = t
		}
	}
	return st, nil
}

// sanitiseAccountID removes characters that are unsafe in S3 bucket names.
// It keeps lowercase letters, digits, and hyphens; all else becomes a hyphen.
func sanitiseAccountID(id string) string {
	b := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b = append(b, c)
		} else if c >= 'A' && c <= 'Z' {
			b = append(b, c+32) // to lowercase
		} else {
			b = append(b, '-')
		}
	}
	// Limit to 40 chars to stay within S3 bucket name limits.
	if len(b) > 40 {
		b = b[:40]
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// RegisterAnchorHandlers registers the anchor-inbox HTTP endpoints on mux.
func RegisterAnchorHandlers(mux *http.ServeMux, s *AnchorStore) {
	mux.HandleFunc("/api/anchor-inbox/provision", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			anchorWriteError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleAnchorProvision(w, r, s)
	})
	mux.HandleFunc("/api/anchor-inbox/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			anchorWriteError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleAnchorStatus(w, r, s)
	})
}

// handleAnchorProvision handles POST /api/anchor-inbox/provision.
//
// SECURITY (IDOR-ANCHORINBOX-01): the account_id used to be read straight out
// of the request body, so any authenticated profile could provision or
// re-provision the anchor-inbox entitlement record for an arbitrary
// account_id it guessed or enumerated. The only identity this handler can
// trust is the caller's own session, so account_id is now always the
// caller's X-User-ID — a client can never act on another account's record. A
// client-supplied account_id in the body, if present, is ignored.
func handleAnchorProvision(w http.ResponseWriter, r *http.Request, s *AnchorStore) {
	accountID := r.Header.Get("X-User-ID")
	if accountID == "" {
		anchorWriteError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	st, err := s.Provision(r.Context(), accountID)
	if err != nil {
		anchorWriteError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	anchorWriteJSON(w, st, http.StatusOK)
}

// handleAnchorStatus handles GET /api/anchor-inbox/status.
//
// SECURITY (IDOR-ANCHORINBOX-01): see handleAnchorProvision — account_id is
// always the caller's own X-User-ID, never a client-supplied query param, so
// one account can never read another account's entitlement/usage record.
func handleAnchorStatus(w http.ResponseWriter, r *http.Request, s *AnchorStore) {
	accountID := r.Header.Get("X-User-ID")
	if accountID == "" {
		anchorWriteError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	st, err := s.Status(r.Context(), accountID)
	if errors.Is(err, ErrNotFound) {
		anchorWriteError(w, "anchor inbox not found for account", http.StatusNotFound)
		return
	}
	if err != nil {
		anchorWriteError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	anchorWriteJSON(w, st, http.StatusOK)
}

func anchorWriteJSON(w http.ResponseWriter, v any, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func anchorWriteError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
