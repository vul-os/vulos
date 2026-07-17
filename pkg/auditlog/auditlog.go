// Package auditlog provides a simple tamper-evident audit log API backed by
// SQLite for the vulos.cloud control plane.
//
// API:
//
//	Logger.Record(ctx, actor, action, target, metadata) — append a hashed entry
//	Logger.Verify(ctx, from, to) — walk the chain, return first broken row
//
// Each row carries: prev_hash (hex-SHA256 of the previous row's payload), and
// entry_hash (hex-SHA256 of this row's canonical fields).  Verify re-computes
// every hash and returns the first *VerifyError on divergence.
package auditlog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ---------------------------------------------------------------------------
// VerifyError
// ---------------------------------------------------------------------------

// VerifyError describes the first tampered row detected by Verify.
type VerifyError struct {
	Seq          int64  // row sequence number
	EntryID      string // opaque entry identifier
	StoredHash   string // hash as recorded in the DB
	ComputedHash string // hash computed at verify-time
}

func (e *VerifyError) Error() string {
	return fmt.Sprintf("auditlog: chain broken at seq=%d entry=%s (stored=%s computed=%s)",
		e.Seq, e.EntryID, e.StoredHash, e.ComputedHash)
}

// ---------------------------------------------------------------------------
// Logger
// ---------------------------------------------------------------------------

// Logger is a tamper-evident audit log backed by SQLite.
// Obtain one via Open.  All methods are safe for concurrent use.
type Logger struct {
	db   *cpdb.DB
	mu   sync.Mutex // serialises writes + prevHash tracking
	prev string     // hex-SHA256 of the last committed entry; "" = chain start
}

// Open applies migrations to db and returns a ready Logger.
//
// db should be obtained from cpdb.Open("auditlog") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.  Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*Logger, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("auditlog: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("auditlog: migrate: %w", err)
	}
	l := &Logger{db: db}
	if err := l.restoreChainTip(); err != nil {
		return nil, err
	}
	return l, nil
}

// Close closes the underlying database connection.
func (l *Logger) Close() error { return l.db.Close() }

// ---------------------------------------------------------------------------
// Record
// ---------------------------------------------------------------------------

// Record appends a tamper-evident entry.
//   - actor:    authenticated principal (e.g. user email or "system")
//   - action:   short action label  (e.g. "pop.drain", "ota.release")
//   - target:   object acted upon   (e.g. pop ID, release version)
//   - metadata: optional key-value context pairs
//
// The entry carries an EMPTY tenant_id: it is a platform-level record and is
// never returned by the org-scoped QueryOrg path. Org-admin actions must use
// RecordOrg so they are bound to (and only visible to) their own org.
func (l *Logger) Record(ctx context.Context, actor, action, target string, metadata map[string]string) error {
	return l.record(ctx, "", actor, action, target, metadata)
}

// RecordOrg appends a tamper-evident entry SCOPED to tenantID (the acting org).
// tenantID is bound into the hash chain so the row cannot later be re-pointed at
// another tenant without breaking Verify. Only rows written via RecordOrg (with
// a non-empty tenant) are ever returned by QueryOrg, and only for their own org.
//
// tenantID must be non-empty; an empty tenantID is rejected (a blank-tenant org
// record would be indistinguishable from a platform record and could leak into
// the wrong org's view). actor is the acting principal (account id / email).
func (l *Logger) RecordOrg(ctx context.Context, tenantID, actor, action, target string, metadata map[string]string) error {
	if tenantID == "" {
		return fmt.Errorf("auditlog: RecordOrg requires a non-empty tenant id")
	}
	return l.record(ctx, tenantID, actor, action, target, metadata)
}

// record is the shared append path. tenantID may be empty (platform record).
func (l *Logger) record(ctx context.Context, tenantID, actor, action, target string, metadata map[string]string) error {
	id, err := randomHex(16)
	if err != nil {
		return fmt.Errorf("auditlog: gen id: %w", err)
	}

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	metaJSON, _ := json.Marshal(metadata)

	l.mu.Lock()
	defer l.mu.Unlock()

	prevHash := l.prev
	if prevHash == "" {
		prevHash = zeroHash
	}

	h := computeHash(id, ts, tenantID, actor, action, target, string(metaJSON), prevHash)

	_, err = l.db.ExecContext(ctx,
		l.db.Rebind(`INSERT INTO auditlog_entries
		   (entry_id, ts, tenant_id, actor, action, target, metadata_json, prev_hash, entry_hash)
		   VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, ts, tenantID, actor, action, target, string(metaJSON), prevHash, h,
	)
	if err != nil {
		return fmt.Errorf("auditlog: insert: %w", err)
	}
	l.prev = h
	return nil
}

// ---------------------------------------------------------------------------
// OrgEntry + QueryOrg — the org-admin audit review path (ORGADMIN-AUDIT-01)
// ---------------------------------------------------------------------------

// OrgEntry is one org-scoped audit record as returned to an org admin. The hash
// fields are intentionally OMITTED from the JSON: an admin reviews *what
// happened*, not the internal chain plumbing, and exposing the chain hashes
// serves no purpose while widening the surface.
type OrgEntry struct {
	Seq      int64             `json:"seq"`
	ID       string            `json:"id"`
	TS       string            `json:"ts"`
	Actor    string            `json:"actor,omitempty"`
	Action   string            `json:"action"`
	Target   string            `json:"target,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// OrgQueryOptions bounds and paginates a QueryOrg call. All fields are optional.
type OrgQueryOptions struct {
	// Action, when non-empty, filters to a single action label (exact match).
	Action string
	// AfterSeq returns only rows with seq < AfterSeq (keyset pagination —
	// descending, so "older than this cursor"). Zero = newest page.
	AfterSeq int64
	// Limit caps the returned rows. <=0 or over the hard cap uses the default.
	Limit int
}

// orgQueryDefaultLimit / orgQueryMaxLimit bound a single QueryOrg page so a
// caller can never pull an unbounded slice of the (potentially large) log.
const (
	orgQueryDefaultLimit = 50
	orgQueryMaxLimit     = 200
)

// QueryOrg returns audit entries for tenantID ONLY, newest-first, bounded and
// keyset-paginated. It is the read side of the org-admin audit review.
//
// Isolation (IDOR-safe): the tenant_id = ? predicate is ALWAYS present and its
// value is the caller's own org id (resolved server-side from the session, never
// from the request body/query). A caller can therefore only ever read rows their
// own org wrote. Platform rows (those with an empty tenant_id) are excluded both
// by that predicate and by the belt-and-braces non-empty-tenant_id guard.
//
// SQL-injection note: the WHERE clause is assembled from IN-CODE CONSTANT
// predicate strings only ("tenant_id = ?", "action = ?", "seq < ?"); every
// runtime value is passed as a positional ? parameter — no user input reaches
// the SQL text.
func (l *Logger) QueryOrg(ctx context.Context, tenantID string, opts OrgQueryOptions) ([]OrgEntry, error) {
	if tenantID == "" {
		// Fail closed: never run an un-scoped query. An empty tenant would match the
		// blank-tenant platform rows and leak them.
		return []OrgEntry{}, nil
	}
	limit := opts.Limit
	if limit <= 0 || limit > orgQueryMaxLimit {
		limit = orgQueryDefaultLimit
	}

	// tenant_id != '' is redundant with tenant_id = ? (a non-empty value) but is
	// kept as an explicit, self-documenting guard against a future refactor that
	// might pass an empty tenant through.
	parts := []string{"tenant_id = ?", "tenant_id != ''"}
	args := []any{tenantID}
	if opts.Action != "" {
		parts = append(parts, "action = ?")
		args = append(args, opts.Action)
	}
	if opts.AfterSeq > 0 {
		parts = append(parts, "seq < ?")
		args = append(args, opts.AfterSeq)
	}
	args = append(args, limit)

	where := strings.Join(parts, " AND ")
	q := fmt.Sprintf(`SELECT seq, entry_id, ts, actor, action, target, metadata_json
	   FROM auditlog_entries WHERE %s ORDER BY seq DESC LIMIT ?`, where)

	rows, err := l.db.QueryContext(ctx, l.db.Rebind(q), args...)
	if err != nil {
		return nil, fmt.Errorf("auditlog: query org: %w", err)
	}
	defer rows.Close()

	out := make([]OrgEntry, 0, limit)
	for rows.Next() {
		var (
			e        OrgEntry
			actor    string
			target   string
			metaJSON string
		)
		if err := rows.Scan(&e.Seq, &e.ID, &e.TS, &actor, &e.Action, &target, &metaJSON); err != nil {
			return nil, fmt.Errorf("auditlog: query org scan: %w", err)
		}
		e.Actor = actor
		e.Target = target
		if metaJSON != "" && metaJSON != "null" {
			_ = json.Unmarshal([]byte(metaJSON), &e.Metadata)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Query (platform-wide)
// ---------------------------------------------------------------------------

// QueryOptions bounds and filters a platform-wide Query. All fields are optional.
type QueryOptions struct {
	// Actor, when non-empty, filters to rows whose actor CONTAINS this substring
	// (case-sensitive LIKE) — the operator audit page's free-text actor filter.
	Actor string
	// Action, when non-empty, filters to rows whose action CONTAINS this substring.
	Action string
	// AfterSeq returns only rows with seq < AfterSeq (keyset pagination —
	// descending). Zero = newest page.
	AfterSeq int64
	// Limit caps the returned rows. <=0 or over the hard cap uses the default.
	Limit int
}

// Query returns audit entries across the WHOLE platform (every tenant + the
// platform rows with an empty tenant_id), newest-first, bounded and filtered.
// This is the OPERATOR (super-admin) read side — it is NOT tenant-scoped and
// must therefore only ever be mounted behind the super-admin gate. The org-admin
// read side is QueryOrg, which hard-filters on the caller's own tenant.
//
// SQL-injection note: the WHERE clause is assembled from IN-CODE CONSTANT
// predicate strings only; every runtime value is a positional ? parameter.
func (l *Logger) Query(ctx context.Context, opts QueryOptions) ([]OrgEntry, error) {
	limit := opts.Limit
	if limit <= 0 || limit > orgQueryMaxLimit {
		limit = orgQueryDefaultLimit
	}

	var parts []string
	var args []any
	if opts.Actor != "" {
		parts = append(parts, "actor LIKE ?")
		args = append(args, "%"+opts.Actor+"%")
	}
	if opts.Action != "" {
		parts = append(parts, "action LIKE ?")
		args = append(args, "%"+opts.Action+"%")
	}
	if opts.AfterSeq > 0 {
		parts = append(parts, "seq < ?")
		args = append(args, opts.AfterSeq)
	}
	where := ""
	if len(parts) > 0 {
		where = "WHERE " + strings.Join(parts, " AND ")
	}
	args = append(args, limit)

	q := fmt.Sprintf(`SELECT seq, entry_id, ts, actor, action, target, metadata_json
	   FROM auditlog_entries %s ORDER BY seq DESC LIMIT ?`, where)

	rows, err := l.db.QueryContext(ctx, l.db.Rebind(q), args...)
	if err != nil {
		return nil, fmt.Errorf("auditlog: query: %w", err)
	}
	defer rows.Close()

	out := make([]OrgEntry, 0, limit)
	for rows.Next() {
		var (
			e        OrgEntry
			actor    string
			target   string
			metaJSON string
		)
		if err := rows.Scan(&e.Seq, &e.ID, &e.TS, &actor, &e.Action, &target, &metaJSON); err != nil {
			return nil, fmt.Errorf("auditlog: query scan: %w", err)
		}
		e.Actor = actor
		e.Target = target
		if metaJSON != "" && metaJSON != "null" {
			_ = json.Unmarshal([]byte(metaJSON), &e.Metadata)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Verify
// ---------------------------------------------------------------------------

// Verify walks the chain from row seq>=from to seq<=to, re-computing hashes.
// Returns the first *VerifyError where the chain diverges, or nil for a clean
// chain.  Pass from=0 and to=^int64(0)>>1 to verify the entire log.
func (l *Logger) Verify(ctx context.Context, from, to int64) error {
	rows, err := l.db.QueryContext(ctx,
		l.db.Rebind(`SELECT seq, entry_id, ts, tenant_id, actor, action, target, metadata_json, prev_hash, entry_hash
		   FROM auditlog_entries
		   WHERE seq >= ? AND seq <= ?
		   ORDER BY seq ASC`), from, to)
	if err != nil {
		return fmt.Errorf("auditlog: verify query: %w", err)
	}
	defer rows.Close()

	var lastHash string // empty = start of verified window

	for rows.Next() {
		var (
			seq      int64
			entryID  string
			ts       string
			tenantID string
			actor    string
			action   string
			target   string
			metaJSON string
			prevHash string
			stored   string
		)
		if err := rows.Scan(&seq, &entryID, &ts, &tenantID, &actor, &action, &target,
			&metaJSON, &prevHash, &stored); err != nil {
			return fmt.Errorf("auditlog: verify scan: %w", err)
		}

		// The prev_hash of this row must equal either the zero hash (first row)
		// or the entry_hash of the previous row in our scan.
		expectedPrev := lastHash
		if expectedPrev == "" {
			expectedPrev = zeroHash
		}
		if prevHash != expectedPrev {
			return &VerifyError{
				Seq:          seq,
				EntryID:      entryID,
				StoredHash:   prevHash,
				ComputedHash: expectedPrev,
			}
		}

		// Re-compute entry_hash from the row fields.
		computed := computeHash(entryID, ts, tenantID, actor, action, target, metaJSON, prevHash)
		if stored != computed {
			return &VerifyError{
				Seq:          seq,
				EntryID:      entryID,
				StoredHash:   stored,
				ComputedHash: computed,
			}
		}

		lastHash = stored
	}
	return rows.Err()
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

const zeroHash = "0000000000000000000000000000000000000000000000000000000000000000"

// computeHash returns the hex-SHA256 of the concatenated NUL-separated fields.
// The NUL separators prevent field-boundary collisions.
//
// Backward-compat with pre-tenant rows: tenantID is mixed into the hash ONLY
// when it is non-empty. Platform rows (tenantID == "") therefore hash byte-for-
// byte identically to the pre-0002-migration scheme, so Verify stays clean over
// the schema upgrade. Org rows (tenantID != "") bind the tenant into the hash,
// so an org record cannot be silently re-attributed to another tenant without
// breaking the chain — the tamper-evidence the audit review depends on.
func computeHash(id, ts, tenantID, actor, action, target, metaJSON, prevHash string) string {
	h := sha256.New()
	if tenantID != "" {
		// A domain-separated tenant prefix — absent for legacy platform rows so
		// their historical hashes still verify.
		h.Write([]byte("tenant\x00"))
		h.Write([]byte(tenantID))
		h.Write([]byte{0})
	}
	for _, s := range []string{id, ts, actor, action, target, metaJSON, prevHash} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// randomHex returns n random bytes as a 2n-character hex string.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (l *Logger) restoreChainTip() error {
	var h string
	err := l.db.QueryRow(`SELECT entry_hash FROM auditlog_entries ORDER BY seq DESC LIMIT 1`).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		l.prev = ""
		return nil
	}
	if err != nil {
		return fmt.Errorf("auditlog: restore chain tip: %w", err)
	}
	l.prev = h
	return nil
}
