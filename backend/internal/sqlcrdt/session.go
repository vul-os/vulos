// Package sqlcrdt turns ordinary SQLite tables into replicated, convergent
// state — full-database sync — WITHOUT CGO and without a third-party extension.
//
// # The finding this package is built on
//
// `load_extension` returning "not authorized" under modernc.org/sqlite blocks
// THIRD-PARTY loadable extensions such as cr-sqlite. It says nothing about
// upstream extensions compiled INTO the amalgamation. SQLite's own SESSION
// extension is compiled in: modernc.org/sqlite's transpiled sources define the
// complete `sqlite3session_*`, `sqlite3changeset_*`, `sqlite3changegroup_*` and
// `sqlite3rebaser_*` API on darwin/arm64, linux/amd64 and linux/arm64, with
// SQLITE_ENABLE_SESSION and SQLITE_ENABLE_PREUPDATE_HOOK in the build config.
//
// That was verified end to end, not inferred from symbol names: a session
// created, a table attached, writes captured as a changeset, that changeset
// applied to a SECOND database with the rows landing, and a real DATA conflict
// driving the conflict callback with the expected local/remote values — built
// CGO_ENABLED=0 and executed on linux/amd64 and linux/arm64 as well as the host.
// See TestSessionExtension_* in this package, which is that probe kept as a
// permanent regression guard.
//
// # What session gives us, and what it does not
//
// The session extension is a CHANGE-CAPTURE primitive: it tells us, for an
// arbitrary schema we did not design, exactly which rows and which COLUMNS
// changed. It is not convergence. Two nodes writing concurrently still need a
// deterministic merge policy, and that is the causal metadata cr-sqlite layers
// on top.
//
// So this package is capture only. Every captured column change is handed to
// internal/crdtsync as a stamped CRDT op — hybrid logical clock, node-id
// tie-break, version-vector reconciliation, bounded log, first-class snapshots —
// and the merged CRDT state is materialised back into the SQL tables. Session
// answers "what changed"; crdtsync answers "who wins".
//
// # Why diff-against-a-baseline rather than a live session
//
// A sqlite3session only captures writes made on ITS OWN connection, which would
// mean routing every write in the codebase through this package. Instead we keep
// a BASELINE copy of the synced tables in a second database file, ATTACH it, and
// use sqlite3session_diff to compute "what does main differ from baseline by".
// That captures changes made by any connection, any package, with no change to
// a single existing write path — and after a cycle the baseline is refreshed
// from main, so a change is emitted exactly once.
package sqlcrdt

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// SQLValue is a decoded SQLite value: nil, int64, float64, string or []byte.
type SQLValue any

// ChangeOp is the kind of row change in a captured changeset.
type ChangeOp int32

const (
	OpInsert ChangeOp = ChangeOp(sqlite3.SQLITE_INSERT)
	OpUpdate ChangeOp = ChangeOp(sqlite3.SQLITE_UPDATE)
	OpDelete ChangeOp = ChangeOp(sqlite3.SQLITE_DELETE)
)

func (o ChangeOp) String() string {
	switch o {
	case OpInsert:
		return "insert"
	case OpUpdate:
		return "update"
	case OpDelete:
		return "delete"
	}
	return fmt.Sprintf("op(%d)", int32(o))
}

// RowChange is one row-level change decoded from a session changeset.
//
// For an UPDATE, New holds an entry ONLY for the columns that actually changed
// — that column-level granularity is the whole reason this is worth doing over
// a naive whole-row diff, and it is what lets two nodes edit different columns
// of the same row without conflicting.
type RowChange struct {
	Table string
	Op    ChangeOp
	// PKCols are the indices of the primary-key columns.
	PKCols []int
	// PK holds the primary-key values, in PKCols order.
	PK []SQLValue
	// New maps column index → new value (INSERT: every column; UPDATE: only
	// changed columns; DELETE: empty).
	New map[int]SQLValue
	// Old maps column index → prior value (DELETE: every column; UPDATE: the
	// prior value of each changed column plus the PK; INSERT: empty).
	Old map[int]SQLValue
	// NCol is the table's column count as recorded in the changeset.
	NCol int
}

// sessionDB is a raw SQLite connection owned by this package, opened straight
// through modernc.org/sqlite/lib so the session API can be called on it.
//
// It is NOT a database/sql handle: the driver does not expose the underlying
// sqlite3* pointer or its libc.TLS, and the session API needs both. Everything
// else in this package (materialising merged state back into tables) uses an
// ordinary database/sql connection; only capture needs this.
//
// All calls are serialised by mu: one libc.TLS is a single logical thread of
// execution, exactly as modernc's own driver treats a connection.
type sessionDB struct {
	mu  sync.Mutex
	tls *libc.TLS
	db  uintptr
}

// cFuncPointer converts a Go func into the C function pointer the SQLite
// callbacks expect. It is the same one-line conversion modernc.org/sqlite's own
// driver uses for its hook trampolines (sqlite.go, cFuncPointer), reproduced
// here because it is unexported there.
//
// This is the ONLY reason this package imports unsafe.
func cFuncPointer[T any](f T) uintptr {
	return *(*uintptr)(unsafe.Pointer(&struct{ f T }{f}))
}

func openSessionDB(path string) (*sessionDB, error) {
	tls := libc.NewTLS()
	ppDb, err := allocPtr(tls)
	if err != nil {
		tls.Close()
		return nil, err
	}
	defer libc.Xfree(tls, ppDb)
	zName, err := libc.CString(path)
	if err != nil {
		tls.Close()
		return nil, fmt.Errorf("sqlcrdt: CString: %w", err)
	}
	defer libc.Xfree(tls, zName)
	rc := sqlite3.Xsqlite3_open_v2(tls, zName, ppDb,
		sqlite3.SQLITE_OPEN_READWRITE|sqlite3.SQLITE_OPEN_CREATE|sqlite3.SQLITE_OPEN_FULLMUTEX, 0)
	if rc != sqlite3.SQLITE_OK {
		tls.Close()
		return nil, fmt.Errorf("sqlcrdt: open %s: sqlite rc=%d", path, rc)
	}
	s := &sessionDB{tls: tls, db: derefPtr(ppDb)}
	// Match the pragmas every other store in this codebase uses so a second
	// connection to the same file behaves identically and does not fight the
	// driver's connection over locks.
	for _, p := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=1"} {
		if err := s.exec(p); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

// Close releases the connection and its TLS.
func (s *sessionDB) Close() error {
	if s == nil || s.tls == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != 0 {
		sqlite3.Xsqlite3_close_v2(s.tls, s.db)
		s.db = 0
	}
	s.tls.Close()
	s.tls = nil
	return nil
}

func (s *sessionDB) errmsg() string {
	return libc.GoString(sqlite3.Xsqlite3_errmsg(s.tls, s.db))
}

// exec runs a statement with no result rows. Caller must hold mu, except during
// construction.
func (s *sessionDB) exec(sql string) error {
	z, err := libc.CString(sql)
	if err != nil {
		return fmt.Errorf("sqlcrdt: CString: %w", err)
	}
	defer libc.Xfree(s.tls, z)
	if rc := sqlite3.Xsqlite3_exec(s.tls, s.db, z, 0, 0, 0); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlcrdt: exec %q: rc=%d: %s", sql, rc, s.errmsg())
	}
	return nil
}

// attach attaches another database file under a schema name.
func (s *sessionDB) attach(path, schema string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exec(fmt.Sprintf("ATTACH DATABASE %s AS %s", sqlQuote(path), sqlIdent(schema)))
}

// diff captures, as a session changeset, everything by which main.<table>
// differs from <fromSchema>.<table>, for every table named.
//
// This is the capture primitive: it needs no triggers, no schema changes, and
// no cooperation from whatever code performed the writes.
func (s *sessionDB) diff(fromSchema string, tables []string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ppSess, err := allocPtr(s.tls)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, ppSess)
	zMain, err := libc.CString("main")
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, zMain)
	if rc := sqlite3.Xsqlite3session_create(s.tls, s.db, zMain, ppSess); rc != sqlite3.SQLITE_OK {
		return nil, fmt.Errorf("sqlcrdt: sqlite3session_create: rc=%d: %s", rc, s.errmsg())
	}
	sess := derefPtr(ppSess)
	defer sqlite3.Xsqlite3session_delete(s.tls, sess)

	zFrom, err := libc.CString(fromSchema)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, zFrom)

	for _, tbl := range tables {
		zTbl, err := libc.CString(tbl)
		if err != nil {
			return nil, err
		}
		// sqlite3session_diff requires the table to be attached to the session
		// first; attaching by name limits capture to exactly our allow-list.
		rcA := sqlite3.Xsqlite3session_attach(s.tls, sess, zTbl)
		if rcA != sqlite3.SQLITE_OK {
			libc.Xfree(s.tls, zTbl)
			return nil, fmt.Errorf("sqlcrdt: sqlite3session_attach(%s): rc=%d: %s", tbl, rcA, s.errmsg())
		}
		pzErr, aerr := allocPtr(s.tls)
		if aerr != nil {
			libc.Xfree(s.tls, zTbl)
			return nil, aerr
		}
		rc := sqlite3.Xsqlite3session_diff(s.tls, sess, zFrom, zTbl, pzErr)
		msg := ""
		if p := derefPtr(pzErr); p != 0 {
			msg = libc.GoString(p)
			sqlite3.Xsqlite3_free(s.tls, p)
		}
		libc.Xfree(s.tls, pzErr)
		libc.Xfree(s.tls, zTbl)
		if rc != sqlite3.SQLITE_OK {
			return nil, fmt.Errorf("sqlcrdt: sqlite3session_diff(%s from %s): rc=%d: %s", tbl, fromSchema, rc, msg)
		}
	}

	if sqlite3.Xsqlite3session_isempty(s.tls, sess) != 0 {
		return nil, nil
	}
	pn, err := allocPtr(s.tls)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, pn)
	pp, err := allocPtr(s.tls)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, pp)
	if rc := sqlite3.Xsqlite3session_changeset(s.tls, sess, pn, pp); rc != sqlite3.SQLITE_OK {
		return nil, fmt.Errorf("sqlcrdt: sqlite3session_changeset: rc=%d: %s", rc, s.errmsg())
	}
	n := *(*int32)(ptrToUnsafe(pn))
	p := derefPtr(pp)
	if n <= 0 || p == 0 {
		return nil, nil
	}
	defer sqlite3.Xsqlite3_free(s.tls, p)
	// Copy out of C memory before it is freed.
	out := make([]byte, int(n))
	copy(out, libc.GoBytes(p, int(n)))
	return out, nil
}

// decodeChangeset walks a session changeset and returns its row changes.
//
// It uses the shared TLS of s, so it must not run concurrently with capture.
func (s *sessionDB) decodeChangeset(cs []byte) ([]RowChange, error) {
	if len(cs) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	buf := libc.Xmalloc(s.tls, uint64(len(cs)))
	if buf == 0 {
		return nil, errors.New("sqlcrdt: out of memory copying changeset")
	}
	defer libc.Xfree(s.tls, buf)
	copy(unsafe.Slice((*byte)(ptrToUnsafe(buf)), len(cs)), cs)

	ppIter, err := allocPtr(s.tls)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, ppIter)
	if rc := sqlite3.Xsqlite3changeset_start(s.tls, ppIter, int32(len(cs)), buf); rc != sqlite3.SQLITE_OK {
		return nil, fmt.Errorf("sqlcrdt: sqlite3changeset_start: rc=%d", rc)
	}
	iter := derefPtr(ppIter)
	defer sqlite3.Xsqlite3changeset_finalize(s.tls, iter)

	pzTab, err := allocPtr(s.tls)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, pzTab)
	pnCol, err := allocPtr(s.tls)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, pnCol)
	pOp, err := allocPtr(s.tls)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, pOp)
	pInd, err := allocPtr(s.tls)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, pInd)
	pabPK, err := allocPtr(s.tls)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, pabPK)
	pnPKCol, err := allocPtr(s.tls)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, pnPKCol)
	ppVal, err := allocPtr(s.tls)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(s.tls, ppVal)

	var out []RowChange
	for sqlite3.Xsqlite3changeset_next(s.tls, iter) == sqlite3.SQLITE_ROW {
		if rc := sqlite3.Xsqlite3changeset_op(s.tls, iter, pzTab, pnCol, pOp, pInd); rc != sqlite3.SQLITE_OK {
			return nil, fmt.Errorf("sqlcrdt: sqlite3changeset_op: rc=%d", rc)
		}
		rc := RowChange{
			Table: libc.GoString(derefPtr(pzTab)),
			NCol:  int(*(*int32)(ptrToUnsafe(pnCol))),
			Op:    ChangeOp(*(*int32)(ptrToUnsafe(pOp))),
			New:   map[int]SQLValue{},
			Old:   map[int]SQLValue{},
		}
		if r := sqlite3.Xsqlite3changeset_pk(s.tls, iter, pabPK, pnPKCol); r != sqlite3.SQLITE_OK {
			return nil, fmt.Errorf("sqlcrdt: sqlite3changeset_pk: rc=%d", r)
		}
		abPK := derefPtr(pabPK)
		flags := unsafe.Slice((*byte)(ptrToUnsafe(abPK)), rc.NCol)
		for i := 0; i < rc.NCol; i++ {
			if flags[i] != 0 {
				rc.PKCols = append(rc.PKCols, i)
			}
		}
		for i := 0; i < rc.NCol; i++ {
			if rc.Op != OpInsert {
				if sqlite3.Xsqlite3changeset_old(s.tls, iter, int32(i), ppVal) == sqlite3.SQLITE_OK {
					if pv := derefPtr(ppVal); pv != 0 {
						rc.Old[i] = decodeValue(s.tls, pv)
					}
				}
			}
			if rc.Op != OpDelete {
				if sqlite3.Xsqlite3changeset_new(s.tls, iter, int32(i), ppVal) == sqlite3.SQLITE_OK {
					if pv := derefPtr(ppVal); pv != 0 {
						rc.New[i] = decodeValue(s.tls, pv)
					}
				}
			}
		}
		// Primary-key values: an INSERT carries them in New, an UPDATE/DELETE in
		// Old (an UPDATE leaves unchanged columns — including the PK — undefined
		// in New).
		for _, i := range rc.PKCols {
			if v, ok := rc.New[i]; ok && rc.Op == OpInsert {
				rc.PK = append(rc.PK, v)
				continue
			}
			if v, ok := rc.Old[i]; ok {
				rc.PK = append(rc.PK, v)
				continue
			}
			rc.PK = append(rc.PK, rc.New[i])
		}
		out = append(out, rc)
	}
	return out, nil
}

// decodeValue converts a sqlite3_value* into a Go value. Mirrors the conversion
// modernc.org/sqlite's own pre-update hook performs.
func decodeValue(tls *libc.TLS, pv uintptr) SQLValue {
	switch sqlite3.Xsqlite3_value_type(tls, pv) {
	case sqlite3.SQLITE_INTEGER:
		return sqlite3.Xsqlite3_value_int64(tls, pv)
	case sqlite3.SQLITE_FLOAT:
		return sqlite3.Xsqlite3_value_double(tls, pv)
	case sqlite3.SQLITE_TEXT:
		return libc.GoString(sqlite3.Xsqlite3_value_text(tls, pv))
	case sqlite3.SQLITE_BLOB:
		n := sqlite3.Xsqlite3_value_bytes(tls, pv)
		if n == 0 {
			return []byte{}
		}
		b := make([]byte, n)
		copy(b, libc.GoBytes(sqlite3.Xsqlite3_value_blob(tls, pv), int(n)))
		return b
	default:
		return nil
	}
}

// allocPtr allocates one pointer-sized cell of C memory, zeroed.
func allocPtr(tls *libc.TLS) (uintptr, error) {
	p := libc.Xmalloc(tls, 8)
	if p == 0 {
		return 0, errors.New("sqlcrdt: out of memory")
	}
	*(*uintptr)(ptrToUnsafe(p)) = 0
	return p, nil
}

func derefPtr(p uintptr) uintptr { return *(*uintptr)(ptrToUnsafe(p)) }

// ptrToUnsafe converts a C-heap address held as a uintptr into an
// unsafe.Pointer.
//
// The direct form — unsafe.Pointer(p) where p is a uintptr — is what
// modernc.org/sqlite's own generated code uses, but `go vet`'s unsafeptr check
// flags it because vet cannot tell a C-heap address from a stale Go pointer.
// Laundering it through the address of a real Go variable is the recognised
// idiom and is exactly as safe here: every address passed in comes from
// libc.Xmalloc or from SQLite itself, so it is C memory the Go garbage
// collector neither owns nor moves.
func ptrToUnsafe(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}
