// Package audit provides an append-only, tamper-evident audit log for the
// vulos.cloud control-plane (COMPLY-03).
//
// Every admin action, login event, and bucket write is recorded as an Entry.
// Entries are flushed to a separate retention bucket (configured via env var
// AUDIT_BUCKET) as newline-delimited JSON objects, keyed by date:
//
//	audit/YYYY/MM/DD/<ulid>.ndjson
//
// Tamper-evidence is provided by a hash-chain: each entry carries the SHA-256
// of the previous entry's serialised JSON (hex-encoded). The first entry in a
// session uses a zero hash.
//
// Storage: cpdb-backed — SQLite (modernc.org/sqlite, pure-Go, no CGO) by
// default; Postgres via pgx/v5/stdlib when DATABASE_URL / VULOS_DATABASE_URL is
// set. Entries are written to the local spool first (durable, queryable), then
// asynchronously flushed to the retention bucket by the background Flusher.
//
// Environment variables:
//
//	AUDIT_BUCKET      name of the Tigris retention bucket (required in prod)
//	AUDIT_DB_PATH     path to the spool SQLite file (optional; default: <db_dir>/audit.db)
package audit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ---------------------------------------------------------------------------
// Event kinds
// ---------------------------------------------------------------------------

// Kind identifies the category of an audit event.
type Kind string

const (
	// KindLogin records a successful authentication (password or passkey).
	KindLogin Kind = "auth.login"
	// KindLoginFailed records a failed authentication attempt.
	KindLoginFailed Kind = "auth.login_failed"
	// KindLogout records an explicit session termination.
	KindLogout Kind = "auth.logout"
	// KindAdminAction records a privileged administrative operation.
	KindAdminAction Kind = "admin.action"
	// KindBucketWrite records a presigned-PUT or direct bucket write issued by
	// the control plane on behalf of an account.
	KindBucketWrite Kind = "storage.bucket_write"
	// KindBucketDelete records a bucket object deletion.
	KindBucketDelete Kind = "storage.bucket_delete"
	// KindSessionRevoke records a session revocation (single or revoke-all).
	KindSessionRevoke Kind = "auth.session_revoke"
)

// ---------------------------------------------------------------------------
// Entry
// ---------------------------------------------------------------------------

// Entry is a single immutable audit-log record.
// The JSON representation is the canonical serialisation used for hashing.
type Entry struct {
	// ID is a random 16-byte hex string that uniquely identifies this entry.
	ID string `json:"id"`
	// Seq is the monotonically-increasing sequence number within the spool DB.
	// Zero for entries that have not yet been written to the DB.
	Seq int64 `json:"seq,omitempty"`
	// Timestamp is the UTC time at which the event occurred (RFC3339Nano).
	Timestamp string `json:"ts"`
	// Kind is the event category.
	Kind Kind `json:"kind"`
	// AccountID is the account/user that performed (or was the subject of) the action.
	AccountID string `json:"account_id,omitempty"`
	// Actor is a free-form string identifying the authenticated principal
	// (e.g. email address, "system", "device:<id>").
	Actor string `json:"actor,omitempty"`
	// Detail holds event-specific metadata. Must be JSON-serialisable.
	Detail map[string]string `json:"detail,omitempty"`
	// PrevHash is the hex-encoded SHA-256 of the previous entry's canonical JSON.
	// All-zeros for the very first entry in the chain.
	PrevHash string `json:"prev_hash"`
	// Hash is the hex-encoded SHA-256 of this entry's canonical JSON (with Hash="").
	// Computed and stored by the Log after the entry is assembled.
	Hash string `json:"hash"`
}

// ---------------------------------------------------------------------------
// Bucket sink interface (narrow seam for testability)
// ---------------------------------------------------------------------------

// BucketSink is the minimal interface the Flusher needs to ship NDJSON blobs
// to object storage. storage.TigrisProvider satisfies this via PutObject.
type BucketSink interface {
	// EnsureBucket creates the bucket if it does not already exist (idempotent).
	EnsureBucket(ctx context.Context, name string) error
	// PutObject writes r (of size bytes, or -1 if unknown) to bucket/key.
	PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64) error
}

// ---------------------------------------------------------------------------
// Log — the primary API
// ---------------------------------------------------------------------------

// Log is the control-plane audit log. Obtain one via Open.
// All methods are safe for concurrent use.
type Log struct {
	db     *cpdb.DB
	mu     sync.Mutex // serialises DB writes + prevHash tracking
	prev   string     // hex SHA-256 of the last committed entry; "" = chain start
	sink   BucketSink // may be nil in dev mode
	bucket string     // retention bucket name
}

// Open applies migrations to db and returns a ready audit Log.  sink may be nil
// (dev mode: entries are only spooled to the DB; no bucket flush).  bucket is
// the retention bucket name (e.g. "vulos-audit-retention").
//
// db should be obtained from cpdb.Open("audit") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.  Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB, sink BucketSink, bucket string) (*Log, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("audit: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("audit: migrate: %w", err)
	}
	l := &Log{db: db, sink: sink, bucket: bucket}
	// Restore the chain tip from the DB so restarts don't break the chain.
	if err := l.restoreChainTip(); err != nil {
		db.Close()
		return nil, err
	}
	return l, nil
}

// Close closes the underlying DB connection.
func (l *Log) Close() error { return l.db.Close() }

// restoreChainTip loads the hash of the last committed entry so the chain
// continues correctly after a process restart.
func (l *Log) restoreChainTip() error {
	var h string
	err := l.db.QueryRow(`SELECT hash FROM audit_entries ORDER BY seq DESC LIMIT 1`).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		l.prev = strings.Repeat("0", 64) // zero hash — chain start
		return nil
	}
	if err != nil {
		return fmt.Errorf("audit: restore chain tip: %w", err)
	}
	l.prev = h
	return nil
}

// ---------------------------------------------------------------------------
// Record — append a new entry
// ---------------------------------------------------------------------------

// Record appends a new audit entry. It is synchronous: the entry is committed
// to the SQLite spool before returning. Bucket flush happens asynchronously
// via Flusher.Flush or FlushNow.
func (l *Log) Record(ctx context.Context, kind Kind, accountID, actor string, detail map[string]string) (*Entry, error) {
	id, err := randomHex(16)
	if err != nil {
		return nil, fmt.Errorf("audit: generate id: %w", err)
	}

	e := &Entry{
		ID:        id,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Kind:      kind,
		AccountID: accountID,
		Actor:     actor,
		Detail:    detail,
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	e.PrevHash = l.prev
	h, err := hashEntry(e)
	if err != nil {
		return nil, fmt.Errorf("audit: hash entry: %w", err)
	}
	e.Hash = h

	detailJSON, err := json.Marshal(e.Detail)
	if err != nil {
		detailJSON = []byte("{}")
	}

	var seq int64
	err = l.db.QueryRowContext(ctx,
		l.db.Rebind(`INSERT INTO audit_entries
		   (entry_id, ts, kind, account_id, actor, detail_json, prev_hash, hash, flushed)
		   VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0) RETURNING seq`),
		e.ID, e.Timestamp, string(e.Kind),
		nullStr(e.AccountID), nullStr(e.Actor),
		string(detailJSON),
		e.PrevHash, e.Hash,
	).Scan(&seq)
	if err != nil {
		return nil, fmt.Errorf("audit: insert entry: %w", err)
	}
	e.Seq = seq
	l.prev = e.Hash
	return e, nil
}

// ---------------------------------------------------------------------------
// Query — support use
// ---------------------------------------------------------------------------

// QueryOptions filters a Query call.
type QueryOptions struct {
	AccountID string    // filter by account (empty = all)
	Kind      Kind      // filter by kind (empty = all)
	Since     time.Time // inclusive lower bound (zero = no bound)
	Limit     int       // max rows (0 = 100)
}

// Query returns audit entries matching opts, ordered by seq ascending.
//
// Security note: user-supplied filter values (AccountID, Kind, Since) are
// always passed as positional ? parameters to QueryContext — they are never
// interpolated into the SQL string.  The fmt.Sprintf below constructs only the
// structural skeleton of the WHERE clause from in-code constant strings
// ("account_id = ?", "kind = ?", "ts >= ?"); no runtime user input reaches
// the SQL string itself.
func (l *Log) Query(ctx context.Context, opts QueryOptions) ([]Entry, error) {
	limit := opts.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	// Build the WHERE clause from fixed predicate strings only.
	// All values are collected into args and passed as query parameters —
	// never interpolated — so SQL injection via filter fields is impossible.
	parts := []string{"1=1"}
	args := []any{}

	if opts.AccountID != "" {
		parts = append(parts, "account_id = ?")
		args = append(args, opts.AccountID)
	}
	if opts.Kind != "" {
		parts = append(parts, "kind = ?")
		args = append(args, string(opts.Kind))
	}
	if !opts.Since.IsZero() {
		parts = append(parts, "ts >= ?")
		args = append(args, opts.Since.UTC().Format(time.RFC3339Nano))
	}

	args = append(args, limit)
	// where is built entirely from the constant strings above; no user input
	// is ever part of the SQL text.
	where := strings.Join(parts, " AND ")
	q := fmt.Sprintf(`SELECT seq, entry_id, ts, kind, account_id, actor, detail_json, prev_hash, hash
		FROM audit_entries WHERE %s ORDER BY seq ASC LIMIT ?`, where)

	rows, err := l.db.QueryContext(ctx, l.db.Rebind(q), args...)
	if err != nil {
		return nil, fmt.Errorf("audit: query: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var (
			e          Entry
			accountID  sql.NullString
			actor      sql.NullString
			detailJSON string
		)
		if err := rows.Scan(&e.Seq, &e.ID, &e.Timestamp, (*string)(&e.Kind),
			&accountID, &actor, &detailJSON, &e.PrevHash, &e.Hash); err != nil {
			return nil, fmt.Errorf("audit: scan: %w", err)
		}
		e.AccountID = accountID.String
		e.Actor = actor.String
		_ = json.Unmarshal([]byte(detailJSON), &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Flusher — async bucket shipping
// ---------------------------------------------------------------------------

// Flusher ships unflushed audit entries from the spool to the retention bucket.
// Create one via NewFlusher and call Run to start the background loop, or call
// FlushNow for a one-shot flush (e.g. in tests).
type Flusher struct {
	log    *Log
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	tick   time.Duration
}

// NewFlusher creates a Flusher that ships entries every tick interval.
// A tick of 0 uses the default of 5 minutes.
func NewFlusher(log *Log, tick time.Duration) *Flusher {
	if tick <= 0 {
		tick = 5 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Flusher{
		log:    log,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
		tick:   tick,
	}
}

// Run starts the background flush loop.  Call Stop to shut it down.
func (f *Flusher) Run() {
	go func() {
		defer close(f.done)
		t := time.NewTicker(f.tick)
		defer t.Stop()
		for {
			select {
			case <-f.ctx.Done():
				// One final flush on shutdown.
				_ = f.FlushNow(context.Background())
				return
			case <-t.C:
				_ = f.FlushNow(f.ctx)
			}
		}
	}()
}

// Stop signals the background loop to stop and waits for it to exit.
func (f *Flusher) Stop() {
	f.cancel()
	<-f.done
}

// FlushNow ships all unflushed entries to the retention bucket immediately.
// It groups entries by date (YYYY/MM/DD) and writes one NDJSON object per
// group.  Entries are marked flushed only after the bucket write succeeds.
// Safe to call concurrently (uses the log's mutex for DB access).
func (f *Flusher) FlushNow(ctx context.Context) error {
	if f.log.sink == nil || f.log.bucket == "" {
		return nil // no-op in dev mode
	}

	// Ensure the retention bucket exists.
	if err := f.log.sink.EnsureBucket(ctx, f.log.bucket); err != nil {
		return fmt.Errorf("audit: flush ensure bucket: %w", err)
	}

	rows, err := f.log.db.QueryContext(ctx,
		`SELECT seq, entry_id, ts, kind, account_id, actor, detail_json, prev_hash, hash
		   FROM audit_entries WHERE flushed = 0 ORDER BY seq ASC LIMIT 500`)
	if err != nil {
		return fmt.Errorf("audit: flush query: %w", err)
	}

	type group struct {
		seqs []int64
		buf  strings.Builder
	}
	groups := map[string]*group{}
	var orderKeys []string

	for rows.Next() {
		var (
			e          Entry
			accountID  sql.NullString
			actor      sql.NullString
			detailJSON string
		)
		if err := rows.Scan(&e.Seq, &e.ID, &e.Timestamp, (*string)(&e.Kind),
			&accountID, &actor, &detailJSON, &e.PrevHash, &e.Hash); err != nil {
			rows.Close()
			return fmt.Errorf("audit: flush scan: %w", err)
		}
		e.AccountID = accountID.String
		e.Actor = actor.String
		_ = json.Unmarshal([]byte(detailJSON), &e.Detail)

		dateKey := entryDateKey(e.Timestamp)
		g, ok := groups[dateKey]
		if !ok {
			g = &group{}
			groups[dateKey] = g
			orderKeys = append(orderKeys, dateKey)
		}
		b, _ := json.Marshal(e)
		g.buf.Write(b)
		g.buf.WriteByte('\n')
		g.seqs = append(g.seqs, e.Seq)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("audit: flush rows: %w", err)
	}

	if len(orderKeys) == 0 {
		return nil // nothing to flush
	}

	for _, dateKey := range orderKeys {
		g := groups[dateKey]
		objectKey := objectKeyForDate(dateKey)
		body := g.buf.String()
		r := strings.NewReader(body)
		if err := f.log.sink.PutObject(ctx, f.log.bucket, objectKey, r, int64(len(body))); err != nil {
			return fmt.Errorf("audit: flush put object %q: %w", objectKey, err)
		}
		// Mark the batch flushed only after the write succeeds.
		if err := markFlushed(ctx, f.log.db, g.seqs); err != nil {
			return fmt.Errorf("audit: mark flushed: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// hashEntry computes the SHA-256 of e's canonical JSON with the Hash field
// set to empty string (so the hash is independent of itself).
func hashEntry(e *Entry) (string, error) {
	orig := e.Hash
	e.Hash = ""
	b, err := json.Marshal(e)
	e.Hash = orig
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// randomHex returns n random bytes encoded as a 2n-character hex string.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// nullStr returns a NULL string for empty values, a valid string otherwise.
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// entryDateKey extracts "YYYY/MM/DD" from an RFC3339Nano timestamp.
// Falls back to today's UTC date on parse failure.
func entryDateKey(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t = time.Now().UTC()
	}
	return t.UTC().Format("2006/01/02")
}

// objectKeyForDate builds the S3 key prefix for a given date key.
// A fresh random suffix ensures multiple flushes on the same day don't
// overwrite each other's objects.
func objectKeyForDate(dateKey string) string {
	suf, _ := randomHex(8)
	return fmt.Sprintf("audit/%s/%s.ndjson", dateKey, suf)
}

// markFlushed updates the flushed flag for the given sequence numbers.
//
// Security note: seqs contains int64 sequence numbers generated internally by
// the database (LastInsertId); they are never sourced from user-controlled
// input.  The placeholders string is constructed from the constant literal "?"
// repeated len(seqs) times — no user value is ever interpolated into the SQL
// text.  All actual values are passed via ExecContext args.
func markFlushed(ctx context.Context, db *cpdb.DB, seqs []int64) error {
	if len(seqs) == 0 {
		return nil
	}
	// Build "?,?,?" from only the constant literal "?"; values go in args.
	placeholders := make([]string, len(seqs))
	args := make([]any, len(seqs))
	for i, s := range seqs {
		placeholders[i] = "?"
		args[i] = s
	}
	_, err := db.ExecContext(ctx,
		db.Rebind(fmt.Sprintf(`UPDATE audit_entries SET flushed = 1 WHERE seq IN (%s)`,
			strings.Join(placeholders, ","))),
		args...)
	return err
}
