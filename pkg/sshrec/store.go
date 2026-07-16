package sshrec

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SQLStore is the production Store backed by SQLite or Postgres via cpdb.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialises writes
}

// Open applies migrations to db and returns a ready *SQLStore.
//
// db should be obtained from cpdb.Open("sshrec") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("sshrec: migrations fs: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("sshrec: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion.
var _ Store = (*SQLStore)(nil)

// --------------------------------------------------------------------------
// Arming
// --------------------------------------------------------------------------

// Arm sets or replaces the arming window for req.ULID.
func (s *SQLStore) Arm(ctx context.Context, req ArmRequest) error {
	armedUntil := time.Now().UTC().Add(req.Window).Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO sshrec_arming (ulid, armed_until, armed_by, reason)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(ulid) DO UPDATE SET
		     armed_until = excluded.armed_until,
		     armed_by    = excluded.armed_by,
		     reason      = excluded.reason`),
		req.ULID, armedUntil, req.AccountID, req.Reason,
	)
	if err != nil {
		return fmt.Errorf("sshrec: arm: %w", err)
	}
	return nil
}

// GetArming returns whether ulid is currently armed.
func (s *SQLStore) GetArming(ctx context.Context, ulid string) (time.Time, bool) {
	var armedUntilStr string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT armed_until FROM sshrec_arming WHERE ulid = ?`), ulid,
	).Scan(&armedUntilStr)
	if err != nil {
		return time.Time{}, false
	}
	t := parseRFC3339(armedUntilStr)
	if time.Now().UTC().After(t) {
		return time.Time{}, false
	}
	return t, true
}

// --------------------------------------------------------------------------
// Requests
// --------------------------------------------------------------------------

// InsertRequest stores a new recovery request.
func (s *SQLStore) InsertRequest(ctx context.Context, r Request) error {
	appJSON, err := json.Marshal(r.Approvals)
	if err != nil {
		return fmt.Errorf("sshrec: marshal approvals: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO sshrec_requests
		     (id, ulid, account_id, public_key_ssh, state, approvals_json,
		      required_approvals, source_ip, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		r.ID, r.ULID, r.AccountID, r.PublicKeySSH, r.State,
		string(appJSON), r.RequiredApprovals, r.SourceIP,
		r.CreatedAt.UTC().Format(time.RFC3339),
		r.ExpiresAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("sshrec: insert request: %w", err)
	}
	return nil
}

// GetRequest returns a request by ID.
func (s *SQLStore) GetRequest(ctx context.Context, id string) (Request, error) {
	return s.scanRequest(ctx, id)
}

func (s *SQLStore) scanRequest(ctx context.Context, id string) (Request, error) {
	var r Request
	var appJSON, createdStr, expiresStr string
	var sourceIP sql.NullString
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, ulid, account_id, public_key_ssh, state, approvals_json,
		        required_approvals, source_ip, created_at, expires_at
		   FROM sshrec_requests WHERE id = ?`), id,
	).Scan(&r.ID, &r.ULID, &r.AccountID, &r.PublicKeySSH, &r.State, &appJSON,
		&r.RequiredApprovals, &sourceIP, &createdStr, &expiresStr)
	if err == sql.ErrNoRows {
		return Request{}, ErrUnknownRequest
	}
	if err != nil {
		return Request{}, fmt.Errorf("sshrec: get request: %w", err)
	}
	r.SourceIP = sourceIP.String
	r.CreatedAt = parseRFC3339(createdStr)
	r.ExpiresAt = parseRFC3339(expiresStr)
	if err := json.Unmarshal([]byte(appJSON), &r.Approvals); err != nil {
		r.Approvals = []Approval{}
	}
	return r, nil
}

// Approve appends an approval and returns the updated request.
func (s *SQLStore) Approve(ctx context.Context, id, byAccount string) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.scanRequest(ctx, id)
	if err != nil {
		return Request{}, err
	}
	// Dual-control: the requester may not approve their own request.
	if byAccount == r.AccountID {
		return r, ErrSelfApproval
	}
	// Dedup: an account may approve a request at most once (defeats reaching
	// quorum by the same approver approving twice).
	for _, a := range r.Approvals {
		if a.By == byAccount {
			return r, ErrAlreadyApproved
		}
	}
	r.Approvals = append(r.Approvals, Approval{By: byAccount, At: time.Now().UTC()})
	if r.RequiredApprovals > 0 && len(r.Approvals) >= r.RequiredApprovals {
		r.State = "approved"
	}
	return r, s.updateRequest(ctx, r)
}

// Reject sets state=rejected.
func (s *SQLStore) Reject(ctx context.Context, id, byAccount string) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.scanRequest(ctx, id)
	if err != nil {
		return Request{}, err
	}
	_ = byAccount
	r.State = "rejected"
	return r, s.updateRequest(ctx, r)
}

func (s *SQLStore) updateRequest(ctx context.Context, r Request) error {
	appJSON, err := json.Marshal(r.Approvals)
	if err != nil {
		return fmt.Errorf("sshrec: marshal approvals: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE sshrec_requests SET state = ?, approvals_json = ? WHERE id = ?`),
		r.State, string(appJSON), r.ID,
	)
	if err != nil {
		return fmt.Errorf("sshrec: update request: %w", err)
	}
	return nil
}

// --------------------------------------------------------------------------
// Certs
// --------------------------------------------------------------------------

// InsertCert stores a minted cert record.
func (s *SQLStore) InsertCert(ctx context.Context, c Cert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO sshrec_certs
		     (key_id, request_id, ulid, serial, not_before, not_after,
		      principal, source_restrict, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`),
		c.KeyID, c.RequestID, c.ULID, c.Serial,
		c.NotBefore.UTC().Format(time.RFC3339),
		c.NotAfter.UTC().Format(time.RFC3339),
		c.Principal, c.SourceRestrict,
	)
	if err != nil {
		return fmt.Errorf("sshrec: insert cert: %w", err)
	}
	return nil
}

// GetCert returns the cert record by key_id.
func (s *SQLStore) GetCert(ctx context.Context, keyID string) (Cert, error) {
	var c Cert
	var nbStr, naStr string
	var revokedInt int
	var revokedAt sql.NullString
	var revokedReason sql.NullString
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT key_id, request_id, ulid, serial, not_before, not_after,
		        principal, source_restrict, revoked, revoked_at, revoked_reason
		   FROM sshrec_certs WHERE key_id = ?`), keyID,
	).Scan(&c.KeyID, &c.RequestID, &c.ULID, &c.Serial, &nbStr, &naStr,
		&c.Principal, &c.SourceRestrict, &revokedInt, &revokedAt, &revokedReason)
	if err == sql.ErrNoRows {
		return Cert{}, ErrNotFound
	}
	if err != nil {
		return Cert{}, fmt.Errorf("sshrec: get cert: %w", err)
	}
	c.NotBefore = parseRFC3339(nbStr)
	c.NotAfter = parseRFC3339(naStr)
	c.Revoked = revokedInt != 0
	if revokedAt.Valid && revokedAt.String != "" {
		t := parseRFC3339(revokedAt.String)
		c.RevokedAt = &t
	}
	c.RevokedReason = revokedReason.String
	return c, nil
}

// RevokeCert marks a cert revoked.
func (s *SQLStore) RevokeCert(ctx context.Context, keyID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE sshrec_certs SET revoked = 1, revoked_at = ?, revoked_reason = ?
		 WHERE key_id = ?`),
		now, reason, keyID,
	)
	if err != nil {
		return fmt.Errorf("sshrec: revoke cert: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListRevoked returns key_ids of all revoked certs.
func (s *SQLStore) ListRevoked(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key_id FROM sshrec_certs WHERE revoked = 1 ORDER BY key_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("sshrec: list revoked: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("sshrec: list revoked scan: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// --------------------------------------------------------------------------
// Pending requests
// --------------------------------------------------------------------------

// ListPendingByULIDs returns pending requests whose ULID is in the provided set.
// An empty ulids slice returns an empty slice.
func (s *SQLStore) ListPendingByULIDs(ctx context.Context, ulids []string) ([]Request, error) {
	if len(ulids) == 0 {
		return []Request{}, nil
	}
	// Build a parameterised IN clause.
	args := make([]any, 0, len(ulids)+1)
	args = append(args, "pending")
	placeholders := make([]byte, 0, len(ulids)*2)
	for i, u := range ulids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, u)
	}
	query := `SELECT id, ulid, account_id, public_key_ssh, state, approvals_json,
	               required_approvals, source_ip, created_at, expires_at
	          FROM sshrec_requests
	         WHERE state = ? AND ulid IN (` + string(placeholders) + `)
	         ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("sshrec: list pending: %w", err)
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var r Request
		var appJSON, createdStr, expiresStr string
		var sourceIP sql.NullString
		if err := rows.Scan(&r.ID, &r.ULID, &r.AccountID, &r.PublicKeySSH, &r.State,
			&appJSON, &r.RequiredApprovals, &sourceIP, &createdStr, &expiresStr); err != nil {
			return nil, fmt.Errorf("sshrec: list pending scan: %w", err)
		}
		r.SourceIP = sourceIP.String
		r.CreatedAt = parseRFC3339(createdStr)
		r.ExpiresAt = parseRFC3339(expiresStr)
		if err := json.Unmarshal([]byte(appJSON), &r.Approvals); err != nil {
			r.Approvals = []Approval{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --------------------------------------------------------------------------
// Audit
// --------------------------------------------------------------------------

// Audit appends an immutable audit event.
func (s *SQLStore) Audit(ctx context.Context, e AuditEvent) error {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("sshrec: marshal audit detail: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO sshrec_audit (at, ulid, account_id, action, key_id, detail_json)
		 VALUES (?, ?, ?, ?, ?, ?)`),
		e.At.UTC().Format(time.RFC3339),
		e.ULID, e.AccountID, e.Action, e.KeyID, string(detailJSON),
	)
	if err != nil {
		return fmt.Errorf("sshrec: audit insert: %w", err)
	}
	return nil
}

// ListAudit returns audit events for ulid, up to limit, oldest first.
func (s *SQLStore) ListAudit(ctx context.Context, ulid string, limit int) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, at, ulid, account_id, action, key_id, detail_json
		   FROM sshrec_audit WHERE ulid = ?
		   ORDER BY at ASC, id ASC
		   LIMIT ?`), ulid, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("sshrec: list audit: %w", err)
	}
	defer rows.Close()

	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var atStr, detailJSON string
		var accountID, keyID sql.NullString
		if err := rows.Scan(&e.ID, &atStr, &e.ULID, &accountID, &e.Action, &keyID, &detailJSON); err != nil {
			return nil, fmt.Errorf("sshrec: list audit scan: %w", err)
		}
		e.At = parseRFC3339(atStr)
		e.AccountID = accountID.String
		e.KeyID = keyID.String
		if err := json.Unmarshal([]byte(detailJSON), &e.Detail); err != nil {
			e.Detail = map[string]any{}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --------------------------------------------------------------------------
// NextSerial — monotonic cert serial counter (SQLite-backed)
// --------------------------------------------------------------------------

// NextSerial increments and returns the next cert serial number.
func (s *SQLStore) NextSerial(ctx context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE sshrec_serial SET serial = serial + 1 WHERE id = 1`)
	if err != nil {
		return 0, fmt.Errorf("sshrec: increment serial: %w", err)
	}
	var serial uint64
	err = s.db.QueryRowContext(ctx, `SELECT serial FROM sshrec_serial WHERE id = 1`).Scan(&serial)
	if err != nil {
		return 0, fmt.Errorf("sshrec: read serial: %w", err)
	}
	return serial, nil
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

func parseRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339Nano, s)
	}
	return t
}
