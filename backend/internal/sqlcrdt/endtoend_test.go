package sqlcrdt

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/internal/crdtsync"
)

// The reminders schema exactly as services/assistant/reminders_store.go creates
// it. These tests drive the REAL table through the REAL engine over a REAL HTTP
// server: nothing here is a stand-in for the thing being claimed.
const remindersSchema = `CREATE TABLE IF NOT EXISTS reminders (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    text       TEXT NOT NULL,
    remind_at  INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    done       INTEGER NOT NULL DEFAULT 0
);`

const e2eSecret = "e2e-fabric-secret"

type box struct {
	name   string
	live   *sql.DB
	crdt   *crdtsync.Store
	bridge *Bridge
	srv    *httptest.Server
	syncer *crdtsync.Syncer
}

func newBox(t *testing.T, name string) *box {
	t.Helper()
	dir := t.TempDir()
	livePath := filepath.Join(dir, "reminders.db")

	live, err := sql.Open("sqlite", dsn(livePath))
	if err != nil {
		t.Fatal(err)
	}
	live.SetMaxOpenConns(1)
	if _, err := live.Exec(remindersSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { live.Close() })

	st, err := crdtsync.Open(filepath.Join(dir, "crdtsync.db"), name, crdtsync.SyncableDomains())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	var rt ReplicatedTable
	for _, c := range ReplicatedTables() {
		if c.Spec.Name == "reminders" {
			rt = c
		}
	}
	br, err := New(Config{LivePath: livePath, Tables: []TableSpec{rt.Spec}, CRDT: st})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { br.Close() })

	mux := http.NewServeMux()
	st.RegisterHandlers(mux, crdtsync.SecretAuthorizer(e2eSecret))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &box{name: name, live: live, crdt: st, bridge: br, srv: srv}
}

func (b *box) connect(t *testing.T, peers ...*box) {
	t.Helper()
	var sp []crdtsync.SyncPeer
	for _, p := range peers {
		sp = append(sp, crdtsync.SyncPeer{InstanceID: p.name, BaseURL: p.srv.URL})
	}
	sy, err := crdtsync.NewSyncer(crdtsync.SyncerConfig{
		Store: b.crdt,
		Peers: crdtsync.PeerSourceFunc(func(context.Context) ([]crdtsync.SyncPeer, error) { return sp, nil }),
		// The wiring passes exactly the approved domains.
		Domains:    crdtsync.SyncableDomains(),
		Secret:     e2eSecret,
		HTTPClient: b.srv.Client(),
		Interval:   50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	b.syncer = sy
}

func (b *box) cycle(t *testing.T) {
	t.Helper()
	if _, _, err := b.bridge.Cycle(); err != nil {
		t.Fatalf("%s: cycle: %v", b.name, err)
	}
}

type reminder struct {
	id, userID, text  string
	remindAt, created int64
	done              int64
}

func (b *box) read(t *testing.T, id string) (reminder, bool) {
	t.Helper()
	var r reminder
	err := b.live.QueryRow(`SELECT id, user_id, text, remind_at, created_at, done FROM reminders WHERE id=?`, id).
		Scan(&r.id, &r.userID, &r.text, &r.remindAt, &r.created, &r.done)
	if err == sql.ErrNoRows {
		return reminder{}, false
	}
	if err != nil {
		t.Fatalf("%s: read %s: %v", b.name, id, err)
	}
	return r, true
}

func (b *box) insert(t *testing.T, r reminder) {
	t.Helper()
	if _, err := b.live.Exec(`INSERT INTO reminders (id, user_id, text, remind_at, created_at, done) VALUES (?,?,?,?,?,?)`,
		r.id, r.userID, r.text, r.remindAt, r.created, r.done); err != nil {
		t.Fatalf("%s: insert: %v", b.name, err)
	}
}

// settle runs enough capture/sync/materialise passes for the pair to quiesce.
func settle(t *testing.T, boxes ...*box) {
	t.Helper()
	ctx := context.Background()
	for round := 0; round < 4; round++ {
		for _, b := range boxes {
			b.cycle(t)
		}
		for _, b := range boxes {
			if b.syncer != nil {
				b.syncer.SyncOnce(ctx)
			}
		}
		for _, b := range boxes {
			b.cycle(t)
		}
	}
}

func TestRemindersReplicateEndToEnd(t *testing.T) {
	// A row INSERTed with ordinary SQL on one box must appear, with its types
	// intact, in the other box's real table — captured by the session
	// extension, merged by the CRDT, shipped over HTTP, materialised back.
	a := newBox(t, "AAA")
	b := newBox(t, "BBB")
	a.connect(t, b)
	b.connect(t, a)

	a.insert(t, reminder{id: "rem-1", userID: "u1", text: "buy milk", remindAt: 1760000000, created: 1750000000})
	settle(t, a, b)

	got, ok := b.read(t, "rem-1")
	if !ok {
		t.Fatal("the reminder never reached the other box's table")
	}
	want := reminder{id: "rem-1", userID: "u1", text: "buy milk", remindAt: 1760000000, created: 1750000000, done: 0}
	if got != want {
		t.Fatalf("materialised row = %+v, want %+v", got, want)
	}
}

func TestConcurrentColumnEditsBothSurviveEndToEnd(t *testing.T) {
	// THE case column granularity exists for, driven entirely through SQL:
	// one box marks a reminder done while the other rewrites its text. A
	// row-level LWW would lose one of them.
	a := newBox(t, "AAA")
	b := newBox(t, "BBB")
	a.connect(t, b)
	b.connect(t, a)

	a.insert(t, reminder{id: "rem-2", userID: "u1", text: "call dentist", remindAt: 100, created: 50})
	settle(t, a, b)
	if _, ok := b.read(t, "rem-2"); !ok {
		t.Fatal("setup: row did not replicate")
	}

	// Concurrent, in different columns, with no sync in between.
	if _, err := a.live.Exec(`UPDATE reminders SET done=1 WHERE id=?`, "rem-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.live.Exec(`UPDATE reminders SET text=? WHERE id=?`, "call dentist at 3pm", "rem-2"); err != nil {
		t.Fatal(err)
	}
	settle(t, a, b)

	for _, box := range []*box{a, b} {
		got, ok := box.read(t, "rem-2")
		if !ok {
			t.Fatalf("%s: row vanished", box.name)
		}
		if got.done != 1 {
			t.Errorf("%s: lost the concurrent done=1 edit (done=%d)", box.name, got.done)
		}
		if got.text != "call dentist at 3pm" {
			t.Errorf("%s: lost the concurrent text edit (text=%q)", box.name, got.text)
		}
	}
}

func TestDeleteReplicatesEndToEnd(t *testing.T) {
	// A DELETE must remove the row on the peer, not merely stop updating it.
	a := newBox(t, "AAA")
	b := newBox(t, "BBB")
	a.connect(t, b)
	b.connect(t, a)

	a.insert(t, reminder{id: "rem-3", userID: "u1", text: "gone soon", remindAt: 1, created: 1})
	settle(t, a, b)
	if _, ok := b.read(t, "rem-3"); !ok {
		t.Fatal("setup: row did not replicate")
	}

	if _, err := a.live.Exec(`DELETE FROM reminders WHERE id=?`, "rem-3"); err != nil {
		t.Fatal(err)
	}
	settle(t, a, b)

	if _, ok := b.read(t, "rem-3"); ok {
		t.Fatal("the delete did not replicate — the row is still present on the peer")
	}
	if _, ok := a.read(t, "rem-3"); ok {
		t.Fatal("the row came back on the originating box")
	}
}

func TestOfflineBoxCatchesUpEndToEnd(t *testing.T) {
	// A box that was disconnected while the other worked must converge on
	// reconnect, and its own offline writes must survive.
	a := newBox(t, "AAA")
	b := newBox(t, "BBB")

	// Both write while disconnected (no syncers wired yet).
	a.insert(t, reminder{id: "rem-a", userID: "u1", text: "from a", remindAt: 1, created: 1})
	b.insert(t, reminder{id: "rem-b", userID: "u1", text: "from b", remindAt: 2, created: 2})
	a.cycle(t)
	b.cycle(t)

	// Reconnect.
	a.connect(t, b)
	b.connect(t, a)
	settle(t, a, b)

	for _, box := range []*box{a, b} {
		if r, ok := box.read(t, "rem-a"); !ok || r.text != "from a" {
			t.Errorf("%s: missing rem-a (%+v ok=%v)", box.name, r, ok)
		}
		if r, ok := box.read(t, "rem-b"); !ok || r.text != "from b" {
			t.Errorf("%s: missing rem-b (%+v ok=%v)", box.name, r, ok)
		}
	}
}

func TestThreeBoxesConvergeEndToEnd(t *testing.T) {
	a := newBox(t, "AAA")
	b := newBox(t, "BBB")
	c := newBox(t, "CCC")
	// A and C never talk directly; everything relays through B.
	a.connect(t, b)
	b.connect(t, a, c)
	c.connect(t, b)

	a.insert(t, reminder{id: "rem-a", userID: "u1", text: "from a", remindAt: 1, created: 1})
	c.insert(t, reminder{id: "rem-c", userID: "u1", text: "from c", remindAt: 3, created: 3})
	settle(t, a, b, c)

	for _, box := range []*box{a, b, c} {
		if _, ok := box.read(t, "rem-a"); !ok {
			t.Errorf("%s: missing rem-a", box.name)
		}
		if _, ok := box.read(t, "rem-c"); !ok {
			t.Errorf("%s: missing rem-c", box.name)
		}
	}
}

func TestBridgeRunLoopCapturesAndNudges(t *testing.T) {
	// The background loop the wiring actually starts: it must capture a write
	// made by ordinary SQL and fire the nudge so the network syncer runs
	// promptly rather than at its next tick.
	a := newBox(t, "AAA")
	b := newBox(t, "BBB")
	a.connect(t, b)

	nudged := make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.bridge.Run(ctx, 20*time.Millisecond, func() {
		a.syncer.Nudge()
		select {
		case nudged <- struct{}{}:
		default:
		}
	})
	go a.syncer.Run(ctx)
	go b.bridge.Run(ctx, 20*time.Millisecond, nil)

	a.insert(t, reminder{id: "rem-loop", userID: "u1", text: "via the loop", remindAt: 9, created: 9})

	select {
	case <-nudged:
	case <-time.After(10 * time.Second):
		t.Fatal("the bridge loop never captured the local write")
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := b.read(t, "rem-loop"); ok && r.text == "via the loop" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the write never reached the peer's table through the running loops")
}

func TestExcludedColumnNeverLeavesTheBox(t *testing.T) {
	// The column allow-list is the last line of defence for a table that gains
	// a sensitive column. An excluded column must be neither captured nor
	// overwritten by a peer.
	dirA, dirB := t.TempDir(), t.TempDir()
	const schema = `CREATE TABLE IF NOT EXISTS widgets (
		id TEXT PRIMARY KEY, label TEXT, secret TEXT);`

	mk := func(dir, name string) (*sql.DB, *crdtsync.Store, *Bridge) {
		live, err := sql.Open("sqlite", dsn(filepath.Join(dir, "w.db")))
		if err != nil {
			t.Fatal(err)
		}
		live.SetMaxOpenConns(1)
		if _, err := live.Exec(schema); err != nil {
			t.Fatal(err)
		}
		st, err := crdtsync.Open(filepath.Join(dir, "c.db"), name, []string{Domain("widgets")})
		if err != nil {
			t.Fatal(err)
		}
		br, err := New(Config{
			LivePath: filepath.Join(dir, "w.db"),
			Tables:   []TableSpec{{Name: "widgets", Columns: []string{"id", "label"}, Exclude: []string{"secret"}}},
			CRDT:     st,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { br.Close(); st.Close(); live.Close() })
		return live, st, br
	}
	liveA, crdtA, brA := mk(dirA, "AAA")
	liveB, crdtB, brB := mk(dirB, "BBB")

	if _, err := liveA.Exec(`INSERT INTO widgets (id,label,secret) VALUES (?,?,?)`, "w1", "hello", "TOP-SECRET"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := brA.Cycle(); err != nil {
		t.Fatal(err)
	}

	// The secret must not be anywhere in A's replicated state.
	fields, err := crdtA.Fields(Domain("widgets"), mustKey(t, "w1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, present := fields["secret"]; present {
		t.Error("the excluded column was captured into the CRDT")
	}

	// Ship A's state to B and materialise.
	d, err := crdtA.Delta(Domain("widgets"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crdtB.Merge(d); err != nil {
		t.Fatal(err)
	}
	if _, _, err := brB.Cycle(); err != nil {
		t.Fatal(err)
	}

	var label string
	var secret sql.NullString
	if err := liveB.QueryRow(`SELECT label, secret FROM widgets WHERE id=?`, "w1").Scan(&label, &secret); err != nil {
		t.Fatal(err)
	}
	if label != "hello" {
		t.Errorf("allowed column did not replicate: %q", label)
	}
	if secret.Valid && secret.String != "" {
		t.Errorf("the excluded column reached the peer: %q", secret.String)
	}
}

func mustKey(t *testing.T, id string) string {
	t.Helper()
	k, err := encodeKey([]SQLValue{id})
	if err != nil {
		t.Fatal(err)
	}
	return k
}
