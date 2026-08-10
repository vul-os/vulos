package sqlcrdt

import (
	"database/sql"
	"path/filepath"
	"testing"

	"modernc.org/libc"
	_ "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// TestSessionExtension_IsCompiledInAndUsable is the permanent regression guard
// for the finding this whole package rests on: SQLite's SESSION extension is
// compiled into modernc.org/sqlite and callable from PURE GO.
//
// This is deliberately the low-level probe rather than a test of our own
// abstractions. The failure mode this codebase keeps hitting is a green test
// over a mechanism that was never actually live — the deleted cr-sqlite hot
// path streamed a `crsql_changes` virtual table that never existed. So this
// test exercises the raw C-level API directly: create a session, capture a
// changeset, apply it to a SECOND database and check the rows LANDED, then
// drive a real conflict and check the callback FIRED with the expected values.
//
// If a future modernc.org/sqlite drops SQLITE_ENABLE_SESSION, this test goes
// red immediately instead of the sync engine silently capturing nothing.
func TestSessionExtension_IsCompiledInAndUsable(t *testing.T) {
	tls := libc.NewTLS()
	defer tls.Close()
	dir := t.TempDir()

	const schema = `CREATE TABLE settings (k TEXT PRIMARY KEY NOT NULL, v TEXT NOT NULL)`

	open := func(name string) uintptr {
		t.Helper()
		ppDb, err := allocPtr(tls)
		if err != nil {
			t.Fatalf("alloc: %v", err)
		}
		defer libc.Xfree(tls, ppDb)
		z, err := libc.CString(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("CString: %v", err)
		}
		defer libc.Xfree(tls, z)
		rc := sqlite3.Xsqlite3_open_v2(tls, z, ppDb,
			sqlite3.SQLITE_OPEN_READWRITE|sqlite3.SQLITE_OPEN_CREATE|sqlite3.SQLITE_OPEN_FULLMUTEX, 0)
		if rc != sqlite3.SQLITE_OK {
			t.Fatalf("open %s: rc=%d", name, rc)
		}
		return derefPtr(ppDb)
	}
	exec := func(db uintptr, q string) {
		t.Helper()
		z, err := libc.CString(q)
		if err != nil {
			t.Fatalf("CString: %v", err)
		}
		defer libc.Xfree(tls, z)
		if rc := sqlite3.Xsqlite3_exec(tls, db, z, 0, 0, 0); rc != sqlite3.SQLITE_OK {
			t.Fatalf("exec %q: rc=%d: %s", q, rc, libc.GoString(sqlite3.Xsqlite3_errmsg(tls, db)))
		}
	}

	db1 := open("a.db")
	exec(db1, schema)

	zMain, _ := libc.CString("main")
	defer libc.Xfree(tls, zMain)
	ppSess, _ := allocPtr(tls)
	defer libc.Xfree(tls, ppSess)
	if rc := sqlite3.Xsqlite3session_create(tls, db1, zMain, ppSess); rc != sqlite3.SQLITE_OK {
		t.Fatalf("sqlite3session_create: rc=%d — the session extension is NOT available", rc)
	}
	sess := derefPtr(ppSess)
	if rc := sqlite3.Xsqlite3session_attach(tls, sess, 0); rc != sqlite3.SQLITE_OK {
		t.Fatalf("sqlite3session_attach: rc=%d", rc)
	}

	exec(db1, `INSERT INTO settings (k,v) VALUES ('theme','dark')`)
	exec(db1, `INSERT INTO settings (k,v) VALUES ('locale','en-ZA')`)

	if sqlite3.Xsqlite3session_isempty(tls, sess) != 0 {
		t.Fatal("session captured NOTHING after two inserts — capture is not live")
	}
	pn, _ := allocPtr(tls)
	defer libc.Xfree(tls, pn)
	pp, _ := allocPtr(tls)
	defer libc.Xfree(tls, pp)
	if rc := sqlite3.Xsqlite3session_changeset(tls, sess, pn, pp); rc != sqlite3.SQLITE_OK {
		t.Fatalf("sqlite3session_changeset: rc=%d", rc)
	}
	nCS := *(*int32)(ptrToUnsafe(pn))
	pCS := derefPtr(pp)
	if nCS <= 0 {
		t.Fatalf("empty changeset (%d bytes)", nCS)
	}

	// ── apply to a SECOND database and check the rows actually land ──────────
	db2 := open("b.db")
	exec(db2, schema)
	if rc := sqlite3.Xsqlite3changeset_apply(tls, db2, nCS, pCS, 0, 0, 0); rc != sqlite3.SQLITE_OK {
		t.Fatalf("sqlite3changeset_apply: rc=%d: %s", rc, libc.GoString(sqlite3.Xsqlite3_errmsg(tls, db2)))
	}
	sqlDB, err := sql.Open("sqlite", filepath.Join(dir, "b.db"))
	if err != nil {
		t.Fatalf("open b.db via driver: %v", err)
	}
	defer sqlDB.Close()
	var theme string
	var count int
	if err := sqlDB.QueryRow(`SELECT v FROM settings WHERE k='theme'`).Scan(&theme); err != nil {
		t.Fatalf("read applied row: %v", err)
	}
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if theme != "dark" || count != 2 {
		t.Fatalf("applied state = theme %q / %d rows, want dark / 2", theme, count)
	}

	// ── drive a REAL conflict through the conflict callback ─────────────────
	db3 := open("c.db")
	exec(db3, schema)
	exec(db3, `INSERT INTO settings (k,v) VALUES ('theme','light')`) // divergent

	ppSess2, _ := allocPtr(tls)
	defer libc.Xfree(tls, ppSess2)
	if rc := sqlite3.Xsqlite3session_create(tls, db1, zMain, ppSess2); rc != sqlite3.SQLITE_OK {
		t.Fatalf("session_create #2: rc=%d", rc)
	}
	sess2 := derefPtr(ppSess2)
	sqlite3.Xsqlite3session_attach(tls, sess2, 0)
	exec(db1, `UPDATE settings SET v='solarized' WHERE k='theme'`)
	pn2, _ := allocPtr(tls)
	defer libc.Xfree(tls, pn2)
	pp2, _ := allocPtr(tls)
	defer libc.Xfree(tls, pp2)
	if rc := sqlite3.Xsqlite3session_changeset(tls, sess2, pn2, pp2); rc != sqlite3.SQLITE_OK {
		t.Fatalf("session_changeset #2: rc=%d", rc)
	}

	fired := 0
	var gotConflict int32 = -1
	var gotLocal, gotRemote string
	onConflict := func(t2 *libc.TLS, _ uintptr, eConflict int32, pIter uintptr) int32 {
		fired++
		gotConflict = eConflict
		ppVal, _ := allocPtr(t2)
		defer libc.Xfree(t2, ppVal)
		if sqlite3.Xsqlite3changeset_conflict(t2, pIter, 1, ppVal) == sqlite3.SQLITE_OK {
			gotLocal = libc.GoString(sqlite3.Xsqlite3_value_text(t2, derefPtr(ppVal)))
		}
		if sqlite3.Xsqlite3changeset_new(t2, pIter, 1, ppVal) == sqlite3.SQLITE_OK {
			if pv := derefPtr(ppVal); pv != 0 {
				gotRemote = libc.GoString(sqlite3.Xsqlite3_value_text(t2, pv))
			}
		}
		return sqlite3.SQLITE_CHANGESET_REPLACE
	}
	rc := sqlite3.Xsqlite3changeset_apply(tls, db3, *(*int32)(ptrToUnsafe(pn2)), derefPtr(pp2),
		0, cFuncPointer(onConflict), 0)
	if rc != sqlite3.SQLITE_OK {
		t.Fatalf("apply with conflict handler: rc=%d: %s", rc, libc.GoString(sqlite3.Xsqlite3_errmsg(tls, db3)))
	}
	if fired == 0 {
		t.Fatal("conflict callback NEVER FIRED — a merge policy built on it would silently do nothing")
	}
	if gotConflict != sqlite3.SQLITE_CHANGESET_DATA {
		t.Errorf("eConflict = %d, want SQLITE_CHANGESET_DATA (%d)", gotConflict, sqlite3.SQLITE_CHANGESET_DATA)
	}
	if gotLocal != "light" || gotRemote != "solarized" {
		t.Errorf("conflict values local=%q remote=%q, want light/solarized", gotLocal, gotRemote)
	}
}

// TestSessionDiff_CapturesChangesFromAnyConnection is the property the whole
// capture design depends on: sqlite3session_diff sees writes made by a
// COMPLETELY DIFFERENT connection (here, an ordinary database/sql handle), so
// no existing write path in the codebase has to be re-routed through us.
func TestSessionDiff_CapturesChangesFromAnyConnection(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	base := filepath.Join(dir, "base.db")

	const schema = `CREATE TABLE profiles (user_id TEXT PRIMARY KEY NOT NULL, theme TEXT, locale TEXT)`

	// Live DB written ONLY through database/sql — never through our session conn.
	appDB, err := sql.Open("sqlite", live)
	if err != nil {
		t.Fatalf("open live: %v", err)
	}
	defer appDB.Close()
	if _, err := appDB.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	baseDB, err := sql.Open("sqlite", base)
	if err != nil {
		t.Fatalf("open base: %v", err)
	}
	if _, err := baseDB.Exec(schema); err != nil {
		t.Fatalf("base schema: %v", err)
	}
	baseDB.Close()

	if _, err := appDB.Exec(`INSERT INTO profiles (user_id, theme, locale) VALUES ('u1','dark','en')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	sdb, err := openSessionDB(live)
	if err != nil {
		t.Fatalf("openSessionDB: %v", err)
	}
	defer sdb.Close()
	if err := sdb.attach(base, "baseline"); err != nil {
		t.Fatalf("attach: %v", err)
	}

	cs, err := sdb.diff("baseline", []string{"profiles"})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(cs) == 0 {
		t.Fatal("diff captured NOTHING — a write from another connection was invisible")
	}
	changes, err := sdb.decodeChangeset(cs)
	if err != nil {
		t.Fatalf("decodeChangeset: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %+v", len(changes), changes)
	}
	c := changes[0]
	if c.Table != "profiles" || c.Op != OpInsert {
		t.Errorf("change = %s/%s, want profiles/insert", c.Table, c.Op)
	}
	if len(c.PK) != 1 || c.PK[0] != "u1" {
		t.Errorf("PK = %v, want [u1]", c.PK)
	}
	if c.New[1] != "dark" || c.New[2] != "en" {
		t.Errorf("new values = %v, want theme=dark locale=en", c.New)
	}

	// An UPDATE must report ONLY the column that changed — the column-level
	// granularity that lets two nodes edit different columns without conflict.
	if _, err := appDB.Exec(`UPDATE profiles SET theme='light' WHERE user_id='u1'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Refresh the baseline to the post-insert state so the diff isolates the update.
	if err := copyTables(live, base, []string{"profiles"}); err != nil {
		t.Fatalf("baseline refresh: %v", err)
	}
	if _, err := appDB.Exec(`UPDATE profiles SET locale='fr' WHERE user_id='u1'`); err != nil {
		t.Fatalf("update2: %v", err)
	}
	cs2, err := sdb.diff("baseline", []string{"profiles"})
	if err != nil {
		t.Fatalf("diff2: %v", err)
	}
	changes2, err := sdb.decodeChangeset(cs2)
	if err != nil {
		t.Fatalf("decode2: %v", err)
	}
	if len(changes2) != 1 || changes2[0].Op != OpUpdate {
		t.Fatalf("got %+v, want one update", changes2)
	}
	if _, ok := changes2[0].New[1]; ok {
		t.Errorf("update reported column 1 (theme) as changed, but only locale changed: %v", changes2[0].New)
	}
	if changes2[0].New[2] != "fr" {
		t.Errorf("update new locale = %v, want fr", changes2[0].New[2])
	}
}
