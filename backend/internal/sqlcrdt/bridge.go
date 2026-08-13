package sqlcrdt

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"vulos/backend/internal/crdtsync"
)

// RowField is the reserved field name carrying a row's EXISTENCE as a
// LWW-register. A live row has it set; a deleted row has it tombstoned.
//
// Row existence has to be its own register rather than an implicit consequence
// of "no columns": without it, a DELETE on one node and a concurrent UPDATE on
// another could not be ordered against each other, and the row would resurrect
// or vanish depending purely on arrival order. With it, delete-vs-update is an
// ordinary LWW decision on a single register, and it converges.
const RowField = "__row"

// TableSpec declares one table as replicated, and — crucially — WHICH COLUMNS.
//
// The column policy is an ALLOW-LIST by default-deny for anything named in
// Exclude. It is the enforcement point for the "which data syncs" decision
// recorded in roadmap/CRDTSYNC.md: an excluded column is never captured, never
// leaves the box, and is never overwritten by a peer.
type TableSpec struct {
	// Name is the table name. It must have an explicit PRIMARY KEY — the
	// session extension cannot capture changes to a table without one, and a
	// row with no stable identity has nothing to converge on.
	Name string
	// Columns, when non-empty, is an explicit allow-list; every other column is
	// ignored. When empty, all columns except Exclude are replicated.
	Columns []string
	// Exclude names columns that must never replicate, whatever Columns says.
	Exclude []string
}

// Config configures a Bridge.
type Config struct {
	// LivePath is the SQLite database file to replicate.
	LivePath string
	// BaselinePath is where the capture baseline is kept. Defaults to
	// LivePath + ".syncbase". It holds a copy of the replicated tables as of
	// the last completed cycle and is derived state — deleting it costs a
	// full re-capture, never data.
	BaselinePath string
	// Tables declares what replicates. An empty list means nothing replicates
	// (fail-closed: sync is opt-in per table, never "everything by default").
	Tables []TableSpec
	// CRDT is the convergence engine. Required.
	CRDT *crdtsync.Store
}

// Bridge connects a live SQLite database to a crdtsync replica.
//
// Capture turns local SQL writes into stamped CRDT ops; Materialise writes the
// merged CRDT state back into the SQL tables. Neither direction requires any
// existing code to change how it talks to the database.
type Bridge struct {
	mu  sync.Mutex
	cfg Config
	// closed is set by Close and makes every subsequent cycle operation fail
	// with ErrClosed. Without it a cycle racing a Close dereferences the freed
	// session handle — see ErrClosed for the shutdown crash that caused.
	closed   bool
	sess     *sessionDB
	live     *sql.DB
	tables   []TableSpec
	colCache map[string]*tableInfo
	// onApplied is notified after rows land in the live DB on a peer's behalf.
	// See SetOnApplied for why a service holding its own cache needs this.
	onApplied func(applied int)
}

type tableInfo struct {
	name    string
	cols    []string // ordinal → column name
	pkCols  []int    // ordinals of PK columns, in PK order
	allowed map[string]bool
}

// New opens the bridge: it validates every declared table, creates the baseline
// database with matching schemas, and attaches it to the capture connection.
func New(cfg Config) (*Bridge, error) {
	if cfg.LivePath == "" {
		return nil, errors.New("sqlcrdt: Config.LivePath is required")
	}
	if cfg.CRDT == nil {
		return nil, errors.New("sqlcrdt: Config.CRDT is required")
	}
	if len(cfg.Tables) == 0 {
		return nil, errors.New("sqlcrdt: Config.Tables is empty — replication is opt-in per table, never implicit")
	}
	if cfg.BaselinePath == "" {
		cfg.BaselinePath = cfg.LivePath + ".syncbase"
	}
	live, err := sql.Open("sqlite", dsn(cfg.LivePath))
	if err != nil {
		return nil, fmt.Errorf("sqlcrdt: open live db: %w", err)
	}
	live.SetMaxOpenConns(1)
	if err := live.Ping(); err != nil {
		live.Close()
		return nil, fmt.Errorf("sqlcrdt: ping live db: %w", err)
	}

	b := &Bridge{cfg: cfg, live: live, colCache: map[string]*tableInfo{}}
	for _, ts := range cfg.Tables {
		ti, err := b.loadTableInfo(ts)
		if err != nil {
			live.Close()
			return nil, err
		}
		b.colCache[ts.Name] = ti
		b.tables = append(b.tables, ts)
	}

	if err := b.ensureBaselineSchema(); err != nil {
		live.Close()
		return nil, err
	}
	sess, err := openSessionDB(cfg.LivePath)
	if err != nil {
		live.Close()
		return nil, err
	}
	if err := sess.attach(cfg.BaselinePath, "baseline"); err != nil {
		sess.Close()
		live.Close()
		return nil, err
	}
	b.sess = sess
	return b, nil
}

// ErrClosed is returned by Capture, Materialise and Cycle once Close has run.
//
// A closed bridge must REFUSE work, not crash on it. Close frees the session
// handle, so a cycle that slipped past it called sessionDB.diff on a nil
// receiver and panicked — and because the loop in Run holds no recover, that
// panic took the whole process down. It was reachable in production, not just
// under test: the server started Run and Close as two independent goroutines
// keyed on the same context, with nothing ordering them, so a box could panic
// on the way down instead of exiting cleanly.
//
// Guarding the state rather than ordering the goroutines is deliberate: it
// makes the crash impossible for EVERY caller and every interleaving, rather
// than for the one shutdown path that happened to be audited.
var ErrClosed = errors.New("sqlcrdt: bridge is closed")

// Close releases both connections. Safe to call more than once, and safe to
// call while Run is still ticking — subsequent cycles return ErrClosed.
func (b *Bridge) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	var first error
	if b.sess != nil {
		first = b.sess.Close()
		b.sess = nil
	}
	if b.live != nil {
		if err := b.live.Close(); err != nil && first == nil {
			first = err
		}
		b.live = nil
	}
	return first
}

// Domain returns the crdtsync domain name for a table.
func Domain(table string) string { return "sql:" + table }

// SetOnApplied registers a callback fired after a cycle writes rows into the
// LIVE database on behalf of a peer. It is not called when a cycle merely
// captures local writes, and not called when nothing was applied.
//
// The bridge writes SQL underneath a running process. Any service that holds
// its own in-memory view of a bridged table will not see those rows until it is
// told to look again — and "until it is told" was, in practice, "until the box
// restarts". The auth store is the case that made this necessary: a replicated
// account existed in auth.db and could not be logged into, because Login
// iterates an in-memory map that was populated once at startup.
//
// Safe to call before Run. A nil callback disables notification.
func (b *Bridge) SetOnApplied(fn func(applied int)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onApplied = fn
}

// notifyApplied invokes the callback outside the bridge's lock: the callback
// reloads another service's state and must never be able to deadlock against a
// cycle already in progress.
func (b *Bridge) notifyApplied(applied int) {
	b.mu.Lock()
	fn := b.onApplied
	b.mu.Unlock()
	if fn != nil {
		fn(applied)
	}
}

// Cycle runs one full local round: capture local SQL writes into the CRDT, then
// materialise the merged CRDT state back into SQL.
//
// The baseline is refreshed after BOTH halves. After capture, so a change is
// emitted exactly once; after materialise, so a value this box just wrote on
// behalf of a peer is not then re-captured as if this box had authored it (which
// would restamp it with a newer local stamp and could beat a genuinely newer
// remote write).
func (b *Bridge) Cycle() (captured, applied int, err error) {
	captured, err = b.Capture()
	if err != nil {
		return 0, 0, err
	}
	applied, err = b.Materialise()
	if err != nil {
		return captured, 0, err
	}
	return captured, applied, nil
}

// Capture diffs the live tables against the baseline and records every allowed
// column change as a stamped CRDT op. Returns the number of ops recorded.
func (b *Bridge) Capture() (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, ErrClosed
	}
	return b.captureLocked()
}

func (b *Bridge) captureLocked() (int, error) {
	names := make([]string, 0, len(b.tables))
	for _, ts := range b.tables {
		names = append(names, ts.Name)
	}
	cs, err := b.sess.diff("baseline", names)
	if err != nil {
		return 0, err
	}
	if len(cs) == 0 {
		return 0, nil
	}
	changes, err := b.sess.decodeChangeset(cs)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ch := range changes {
		ti := b.colCache[ch.Table]
		if ti == nil {
			continue // not a replicated table
		}
		key, err := encodeKey(ch.PK)
		if err != nil {
			return n, err
		}
		dom := Domain(ch.Table)
		switch ch.Op {
		case OpDelete:
			if err := b.cfg.CRDT.Delete(dom, key, RowField); err != nil {
				return n, err
			}
			n++
		case OpInsert, OpUpdate:
			if ch.Op == OpInsert {
				if err := b.cfg.CRDT.Set(dom, key, RowField, []byte{'1'}); err != nil {
					return n, err
				}
				n++
			}
			idx := make([]int, 0, len(ch.New))
			for i := range ch.New {
				idx = append(idx, i)
			}
			sort.Ints(idx)
			for _, i := range idx {
				if i < 0 || i >= len(ti.cols) {
					continue
				}
				col := ti.cols[i]
				if !ti.allowed[col] {
					continue // excluded column: never captured, never leaves the box
				}
				if err := b.cfg.CRDT.Set(dom, key, col, encodeValue(ch.New[i])); err != nil {
					return n, err
				}
				n++
			}
		}
	}
	if err := b.refreshBaselineLocked(); err != nil {
		return n, err
	}
	return n, nil
}

// Materialise writes the merged CRDT state back into the SQL tables and then
// refreshes the baseline. Returns the number of rows written or removed.
func (b *Bridge) Materialise() (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, ErrClosed
	}
	n, err := b.materialiseLocked()
	if err != nil {
		return n, err
	}
	if err := b.refreshBaselineLocked(); err != nil {
		return n, err
	}
	return n, nil
}

func (b *Bridge) materialiseLocked() (int, error) {
	written := 0
	for _, ts := range b.tables {
		ti := b.colCache[ts.Name]
		dom := Domain(ts.Name)
		keys, err := b.cfg.CRDT.Keys(dom)
		if err != nil {
			return written, err
		}
		// Tombstoned rows do not appear in Keys (it lists live registers only),
		// so ask the engine for every key it has ever seen in this domain and
		// diff — a deleted row must be REMOVED from SQL, not merely not-written.
		allKeys, err := b.cfg.CRDT.AllKeys(dom)
		if err != nil {
			return written, err
		}
		live := map[string]bool{}
		for _, k := range keys {
			live[k] = true
		}
		for _, key := range allKeys {
			pk, err := decodeKey(key)
			if err != nil {
				return written, err
			}
			if len(pk) != len(ti.pkCols) {
				continue // key shape does not match this table's PK; skip rather than guess
			}
			_, rowLive, err := b.cfg.CRDT.Get(dom, key, RowField)
			if err != nil {
				return written, err
			}
			if !rowLive {
				n, err := b.deleteRow(ti, pk)
				if err != nil {
					return written, err
				}
				written += n
				continue
			}
			fields, err := b.cfg.CRDT.Fields(dom, key)
			if err != nil {
				return written, err
			}
			n, err := b.upsertRow(ti, pk, fields)
			if err != nil {
				return written, err
			}
			written += n
		}
	}
	return written, nil
}

// upsertRow writes one row's merged state. Only allowed columns are written, so
// an excluded column (a secret, a device-local field) keeps whatever this box
// already had — a peer can never overwrite it.
func (b *Bridge) upsertRow(ti *tableInfo, pk []SQLValue, fields map[string][]byte) (int, error) {
	cols := make([]string, 0, len(fields))
	for f := range fields {
		if f == RowField {
			continue
		}
		if !ti.allowed[f] {
			continue
		}
		cols = append(cols, f)
	}
	sort.Strings(cols)

	insertCols := make([]string, 0, len(ti.pkCols)+len(cols))
	args := make([]any, 0, len(ti.pkCols)+len(cols))
	for i, ord := range ti.pkCols {
		insertCols = append(insertCols, ti.cols[ord])
		args = append(args, pk[i])
	}
	pkSet := map[string]bool{}
	for _, ord := range ti.pkCols {
		pkSet[ti.cols[ord]] = true
	}
	updateCols := make([]string, 0, len(cols))
	for _, c := range cols {
		if pkSet[c] {
			continue // the PK identifies the row; it is never an updatable field
		}
		v, err := decodeValue2(fields[c])
		if err != nil {
			return 0, fmt.Errorf("sqlcrdt: %s.%s: %w", ti.name, c, err)
		}
		insertCols = append(insertCols, c)
		args = append(args, v)
		updateCols = append(updateCols, c)
	}

	quoted := make([]string, len(insertCols))
	placeholders := make([]string, len(insertCols))
	for i, c := range insertCols {
		quoted[i] = sqlIdent(c)
		placeholders[i] = "?"
	}
	conflictCols := make([]string, len(ti.pkCols))
	for i, ord := range ti.pkCols {
		conflictCols[i] = sqlIdent(ti.cols[ord])
	}

	var q string
	if len(updateCols) == 0 {
		q = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT(%s) DO NOTHING",
			sqlIdent(ti.name), strings.Join(quoted, ","), strings.Join(placeholders, ","),
			strings.Join(conflictCols, ","))
	} else {
		sets := make([]string, len(updateCols))
		// A guard so an unchanged row is a genuine no-op. Without it, DO UPDATE
		// rewrites identical values on every cycle and reports a row affected
		// every time — which makes the applied count, and anything hung off it,
		// fire continuously against a table nobody is touching. `IS NOT` rather
		// than `<>` because `NULL <> NULL` is NULL, and a column that is NULL on
		// both sides must count as unchanged rather than as unknown.
		diffs := make([]string, len(updateCols))
		for i, c := range updateCols {
			sets[i] = sqlIdent(c) + "=excluded." + sqlIdent(c)
			diffs[i] = sqlIdent(ti.name) + "." + sqlIdent(c) + " IS NOT excluded." + sqlIdent(c)
		}
		q = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT(%s) DO UPDATE SET %s WHERE %s",
			sqlIdent(ti.name), strings.Join(quoted, ","), strings.Join(placeholders, ","),
			strings.Join(conflictCols, ","), strings.Join(sets, ","), strings.Join(diffs, " OR "))
	}
	res, err := b.live.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("sqlcrdt: upsert %s: %w", ti.name, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (b *Bridge) deleteRow(ti *tableInfo, pk []SQLValue) (int, error) {
	where := make([]string, len(ti.pkCols))
	args := make([]any, len(ti.pkCols))
	for i, ord := range ti.pkCols {
		where[i] = sqlIdent(ti.cols[ord]) + "=?"
		args[i] = pk[i]
	}
	res, err := b.live.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s", sqlIdent(ti.name), strings.Join(where, " AND ")), args...)
	if err != nil {
		return 0, fmt.Errorf("sqlcrdt: delete %s: %w", ti.name, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// refreshBaselineLocked makes the baseline an exact copy of the live tables, so
// the next diff reports only changes made after this point.
func (b *Bridge) refreshBaselineLocked() error {
	b.sess.mu.Lock()
	defer b.sess.mu.Unlock()
	if err := b.sess.exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	for _, ts := range b.tables {
		t := sqlIdent(ts.Name)
		if err := b.sess.exec("DELETE FROM baseline." + t); err != nil {
			_ = b.sess.exec("ROLLBACK")
			return err
		}
		if err := b.sess.exec("INSERT INTO baseline." + t + " SELECT * FROM main." + t); err != nil {
			_ = b.sess.exec("ROLLBACK")
			return err
		}
	}
	return b.sess.exec("COMMIT")
}

// ── schema helpers ───────────────────────────────────────────────────────────

func dsn(path string) string {
	return path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
}

func (b *Bridge) loadTableInfo(ts TableSpec) (*tableInfo, error) {
	rows, err := b.live.Query(fmt.Sprintf("PRAGMA table_info(%s)", sqlIdent(ts.Name)))
	if err != nil {
		return nil, fmt.Errorf("sqlcrdt: table_info(%s): %w", ts.Name, err)
	}
	defer rows.Close()
	ti := &tableInfo{name: ts.Name, allowed: map[string]bool{}}
	type pkEntry struct {
		ord, pos int
	}
	var pks []pkEntry
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		ti.cols = append(ti.cols, name)
		if pk > 0 {
			pks = append(pks, pkEntry{ord: cid, pos: pk})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ti.cols) == 0 {
		return nil, fmt.Errorf("sqlcrdt: table %q does not exist", ts.Name)
	}
	if len(pks) == 0 {
		return nil, fmt.Errorf("sqlcrdt: table %q has no PRIMARY KEY — the session extension cannot capture it and a row with no stable identity has nothing to converge on", ts.Name)
	}
	sort.Slice(pks, func(i, j int) bool { return pks[i].pos < pks[j].pos })
	for _, p := range pks {
		ti.pkCols = append(ti.pkCols, p.ord)
	}

	excluded := map[string]bool{}
	for _, c := range ts.Exclude {
		excluded[c] = true
	}
	if len(ts.Columns) > 0 {
		for _, c := range ts.Columns {
			if !excluded[c] {
				ti.allowed[c] = true
			}
		}
	} else {
		for _, c := range ti.cols {
			if !excluded[c] {
				ti.allowed[c] = true
			}
		}
	}
	// PK columns are always "allowed" in the sense that they identify the row,
	// but they are never written as updatable fields (see upsertRow).
	for _, ord := range ti.pkCols {
		ti.allowed[ti.cols[ord]] = true
	}
	for _, c := range ts.Columns {
		found := false
		for _, have := range ti.cols {
			if have == c {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("sqlcrdt: table %q has no column %q declared in Columns", ts.Name, c)
		}
	}
	return ti, nil
}

// ensureBaselineSchema creates the baseline database with the SAME schema as
// live for every replicated table. sqlite3session_diff requires compatible
// schemas, so the CREATE statement is copied verbatim from sqlite_master.
func (b *Bridge) ensureBaselineSchema() error {
	if err := ensureDir(b.cfg.BaselinePath); err != nil {
		return err
	}
	base, err := sql.Open("sqlite", dsn(b.cfg.BaselinePath))
	if err != nil {
		return fmt.Errorf("sqlcrdt: open baseline: %w", err)
	}
	defer base.Close()
	base.SetMaxOpenConns(1)
	if err := base.Ping(); err != nil {
		return fmt.Errorf("sqlcrdt: ping baseline: %w", err)
	}
	for _, ts := range b.tables {
		var ddl string
		err := b.live.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, ts.Name).Scan(&ddl)
		if err != nil {
			return fmt.Errorf("sqlcrdt: read schema for %s: %w", ts.Name, err)
		}
		var exists int
		if err := base.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, ts.Name).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			if _, err := base.Exec(ddl); err != nil {
				return fmt.Errorf("sqlcrdt: create baseline table %s: %w", ts.Name, err)
			}
		}
	}
	return nil
}

func ensureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}

// copyTables replaces the named tables in dst with the contents of the same
// tables in src. It is the baseline-refresh primitive, exposed separately so a
// test can drive it directly.
func copyTables(srcPath, dstPath string, tables []string) error {
	s, err := openSessionDB(srcPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.attach(dstPath, "dst"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	for _, t := range tables {
		q := sqlIdent(t)
		if err := s.exec("DELETE FROM dst." + q); err != nil {
			_ = s.exec("ROLLBACK")
			return err
		}
		if err := s.exec("INSERT INTO dst." + q + " SELECT * FROM main." + q); err != nil {
			_ = s.exec("ROLLBACK")
			return err
		}
	}
	return s.exec("COMMIT")
}

// ── value / key encoding ─────────────────────────────────────────────────────
//
// A CRDT register holds opaque bytes, but a SQL column has a TYPE. Values are
// therefore stored tagged, so a round trip through the engine returns an
// integer as an integer rather than as the text "42" — which would silently
// change a column's affinity on the receiving node.

func encodeValue(v SQLValue) []byte {
	switch t := v.(type) {
	case nil:
		return []byte{'n'}
	case int64:
		return append([]byte{'i'}, []byte(strconv.FormatInt(t, 10))...)
	case float64:
		return append([]byte{'r'}, []byte(strconv.FormatFloat(t, 'g', -1, 64))...)
	case string:
		return append([]byte{'t'}, []byte(t)...)
	case []byte:
		return append([]byte{'b'}, t...)
	default:
		return append([]byte{'t'}, []byte(fmt.Sprint(t))...)
	}
}

func decodeValue2(b []byte) (SQLValue, error) {
	if len(b) == 0 {
		return nil, errors.New("empty encoded value")
	}
	body := b[1:]
	switch b[0] {
	case 'n':
		return nil, nil
	case 'i':
		return strconv.ParseInt(string(body), 10, 64)
	case 'r':
		return strconv.ParseFloat(string(body), 64)
	case 't':
		return string(body), nil
	case 'b':
		out := make([]byte, len(body))
		copy(out, body)
		return out, nil
	default:
		return nil, fmt.Errorf("unknown value tag %q", b[0])
	}
}

// encodeKey renders a primary key as an ASCII-safe, deterministic string: each
// component is its type tag plus base64url of its payload, joined by ':'. Two
// nodes encoding the same PK always produce the same key, which is what lets
// them address the same CRDT row.
func encodeKey(pk []SQLValue) (string, error) {
	if len(pk) == 0 {
		return "", errors.New("sqlcrdt: empty primary key")
	}
	parts := make([]string, len(pk))
	for i, v := range pk {
		enc := encodeValue(v)
		parts[i] = string(enc[0]) + base64.RawURLEncoding.EncodeToString(enc[1:])
	}
	return strings.Join(parts, ":"), nil
}

func decodeKey(k string) ([]SQLValue, error) {
	parts := strings.Split(k, ":")
	out := make([]SQLValue, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("sqlcrdt: malformed key %q", k)
		}
		raw, err := base64.RawURLEncoding.DecodeString(p[1:])
		if err != nil {
			return nil, fmt.Errorf("sqlcrdt: malformed key %q: %w", k, err)
		}
		v, err := decodeValue2(append([]byte{p[0]}, raw...))
		if err != nil {
			return nil, fmt.Errorf("sqlcrdt: malformed key %q: %w", k, err)
		}
		out = append(out, v)
	}
	return out, nil
}
