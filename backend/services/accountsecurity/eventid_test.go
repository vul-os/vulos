package accountsecurity

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// The audit log replicates now (crdtsync policy, grow-only), and replication
// keys on the table's PRIMARY KEY. It used to be `id INTEGER AUTOINCREMENT`,
// allocated independently on every box — so two machines gave the SAME key to
// two DIFFERENT events, and a merge saw one key with two conflicting values and
// dropped one box's real entry. Migrations 0002/0003 make a random event_id the
// primary key.
//
// Two properties have to hold, and both are load-bearing rather than tidy:
//
//  1. event_id is UNIQUE ACROSS BOXES, not merely within one. A per-box counter
//     satisfies "unique" locally and still collides on merge, which is exactly
//     the bug. So the test opens two independent stores and checks their ids are
//     disjoint.
//  2. event_id is UNPREDICTABLE. Grow-only keeps the FIRST writer, so a hostile
//     peer can suppress an entry whose key it can guess by writing that key
//     first. Sequential ids make that trivial.

func newStore(t *testing.T) *store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "accountsecurity.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func idsOf(t *testing.T, s *store, userID string) []string {
	t.Helper()
	recs, err := s.recentSensitiveActions(context.Background(), userID, 100)
	if err != nil {
		t.Fatalf("recentSensitiveActions: %v", err)
	}
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.EventID)
	}
	return out
}

func TestEventID_IsDisjointAcrossBoxes(t *testing.T) {
	// Two boxes, each recording its own events from a clean database. Under the
	// old AUTOINCREMENT key both would have produced 1, 2, 3 — and a merge would
	// have treated box B's event 1 as a conflicting version of box A's event 1.
	a, b := newStore(t), newStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := a.recordSensitiveAction(ctx, "u1", "login", "10.0.0.1", "ua"); err != nil {
			t.Fatal(err)
		}
		if err := b.recordSensitiveAction(ctx, "u1", "login", "10.0.0.2", "ua"); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	for _, id := range idsOf(t, a, "u1") {
		if id == "" {
			t.Fatal("an entry has an empty event_id — it would collide with every other empty one")
		}
		seen[id] = true
	}
	for _, id := range idsOf(t, b, "u1") {
		if seen[id] {
			t.Fatalf("box B minted event_id %q that box A also used — a merge would drop one of the two events", id)
		}
	}
}

func TestEventID_IsNotSequential(t *testing.T) {
	// Grow-only keeps the FIRST writer, so a hostile peer can suppress an entry
	// whose key it can guess by writing that key first. A counter makes that
	// trivial.
	//
	// This test originally checked only that ids were >=16 chars and distinct.
	// A mutation replacing crypto/rand with a zero-padded counter SURVIVED it:
	// "%032d" is 32 characters and never repeats, and the cross-box test could
	// not see it either, because two stores in ONE process share a
	// package-level counter and so still produce disjoint ids. Both of those
	// are the same mistake — measuring a property the counter also has.
	//
	// The property a counter does NOT have is that consecutive values differ
	// almost everywhere. Successive counter values differ in one or two
	// characters; successive random hex differs in roughly fifteen of every
	// sixteen.
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := s.recordSensitiveAction(ctx, "u1", "login", "10.0.0.1", "ua"); err != nil {
			t.Fatal(err)
		}
	}
	ids := idsOf(t, s, "u1")
	if len(ids) != 3 {
		t.Fatalf("got %d entries, want 3", len(ids))
	}
	for _, id := range ids {
		if len(id) < 16 {
			t.Errorf("event_id %q is short enough to enumerate; grow-only pre-emption becomes practical", id)
		}
	}

	// Compare every pair, not just neighbours: a counter that jumped by a large
	// stride would still differ in few positions overall.
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a, b := ids[i], ids[j]
			if a == b {
				t.Fatalf("event_ids repeat within one box: %q", a)
			}
			n := len(a)
			if len(b) < n {
				n = len(b)
			}
			same := 0
			for k := 0; k < n; k++ {
				if a[k] == b[k] {
					same++
				}
			}
			// Random hex agrees in about 1/16 of positions. A counter agrees in
			// nearly all of them. Half is far above the former and far below
			// the latter, so this discriminates without being flaky.
			if same*2 > n {
				t.Errorf("event_ids %q and %q agree in %d of %d positions — this looks like a counter, not an unpredictable id", a, b, same, n)
			}
		}
	}
}

func TestRecentSensitiveActions_StillNewestFirst(t *testing.T) {
	// Ordering moved from `id DESC` to `ts DESC, event_id DESC` when the integer
	// id was dropped. The read path's contract — newest first — must survive
	// that, or an audit surface silently starts showing the oldest events.
	s := newStore(t)
	ctx := context.Background()
	for _, act := range []string{"first", "second", "third"} {
		if err := s.recordSensitiveAction(ctx, "u1", Action(act), "10.0.0.1", "ua"); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := s.recentSensitiveActions(ctx, "u1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	for i := 1; i < len(recs); i++ {
		if recs[i-1].Ts.Before(recs[i].Ts) {
			t.Fatalf("records are not newest-first: %v came before %v", recs[i-1].Ts, recs[i].Ts)
		}
	}
}

// THE MIGRATION. A box upgrading has rows in the OLD shape: an integer primary
// key and no event_id. Dropping them would silently delete a user's security
// history — the one record whose absence nobody notices until it matters.
//
// This builds the pre-0002 table by hand and runs the real migration runner
// over it, rather than trusting that the SQL is right by inspection.
func TestMigration_PreservesExistingAuditRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accountsecurity.db")

	// The 0001 shape, created directly so the migration runner has genuinely
	// old data to move rather than something already in the new form.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE acctsec_sensitive_actions (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		ts         TEXT NOT NULL,
		user_id    TEXT NOT NULL,
		action     TEXT NOT NULL,
		client_ip  TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatal(err)
	}
	for _, act := range []string{"old-login", "old-pin-change"} {
		if _, err := raw.Exec(
			`INSERT INTO acctsec_sensitive_actions(ts,user_id,action,client_ip,user_agent) VALUES(?,?,?,?,?)`,
			"2026-08-01T00:00:00Z", "u1", act, "10.0.0.9", "old-ua"); err != nil {
			t.Fatal(err)
		}
	}
	// No schema_migrations rows are planted: 0001 is CREATE TABLE IF NOT EXISTS
	// throughout, so the real runner applies it harmlessly over the table above
	// and then does the 0002/0003 work this test is actually about. Faking the
	// bookkeeping row would mean guessing the runner's own schema (it records a
	// content checksum too) and would test that guess rather than the migration.
	_ = raw.Close()

	s, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore over an old-shape database: %v", err)
	}
	defer s.Close()

	recs, err := s.recentSensitiveActions(context.Background(), "u1", 10)
	if err != nil {
		t.Fatalf("read after migration: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d rows after migration, want 2 — the upgrade deleted a user's security history", len(recs))
	}
	seen := map[string]bool{}
	for _, r := range recs {
		if r.EventID == "" {
			t.Errorf("migrated row %q has no event_id; it would collide with every other empty one on merge", r.Action)
		}
		if seen[r.EventID] {
			t.Errorf("migrated rows share event_id %q — the backfill is not unique", r.EventID)
		}
		seen[r.EventID] = true
	}
}
