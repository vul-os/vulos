package crdtsync

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── test harness ─────────────────────────────────────────────────────────────
//
// Every helper here exists to make ONE claim checkable: that two replicas which
// have seen the same set of ops hold byte-identical state, no matter what order
// they saw them in. digest() is that claim's ground truth — it hashes the
// materialised state INCLUDING stamps, so a test cannot pass merely because two
// boxes happen to show the same value while disagreeing about who wrote it.

// fakeTime is an injectable wall clock. Concurrency in these tests is expressed
// by controlling stamps, not by racing goroutines: a "concurrent" write is one
// whose stamp neither dominates nor is dominated in the way the test intends.
type fakeTime struct {
	mu sync.Mutex
	ms int64
}

func newFakeTime(startMS int64) *fakeTime { return &fakeTime{ms: startMS} }

func (f *fakeTime) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.UnixMilli(f.ms).UTC()
}

func (f *fakeTime) set(ms int64) {
	f.mu.Lock()
	f.ms = ms
	f.mu.Unlock()
}

func (f *fakeTime) advance(ms int64) {
	f.mu.Lock()
	f.ms += ms
	f.mu.Unlock()
}

// testDomains is the allow-list every test replica is opened with. Production
// wiring passes SyncableDomains() instead; see policy.go.
var testDomains = []string{dom, "alpha", "beta"}

// newTestStore opens a Store on a temp file with a fixed actor id.
func newTestStore(t *testing.T, actor string) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "crdt.db"), actor, testDomains)
	if err != nil {
		t.Fatalf("Open(%s): %v", actor, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newTestStoreWithDomains opens a Store with an explicit allow-list.
func newTestStoreWithDomains(t *testing.T, actor string, domains []string) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "crdt.db"), actor, domains)
	if err != nil {
		t.Fatalf("Open(%s): %v", actor, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newTestStoreAt opens a Store at an explicit path (for reopen tests).
func newTestStoreAt(t *testing.T, path, actor string) *Store {
	t.Helper()
	s, err := Open(path, actor, testDomains)
	if err != nil {
		t.Fatalf("Open(%s, %s): %v", path, actor, err)
	}
	return s
}

// withFakeClock pins a store's wall clock so stamps are deterministic.
func withFakeClock(s *Store, ft *fakeTime) {
	s.clock.mu.Lock()
	s.clock.now = ft.now
	s.clock.mu.Unlock()
}

// digest renders the ENTIRE materialised state of a domain — registers with
// their values, tombstone flags and full stamps, plus every per-actor counter
// contribution — as a stable string.
//
// Comparing digests is how convergence is asserted. It deliberately includes
// the stamp: two replicas that show the same value but disagree on the winning
// stamp have NOT converged, because the next concurrent write would resolve
// differently on each of them.
func digest(t *testing.T, s *Store, domain string) string {
	t.Helper()
	var b strings.Builder
	rows, err := s.db.Query(`SELECT key, field, value, deleted, wall, logical, actor FROM crdt_reg WHERE domain=? ORDER BY key, field`, domain)
	if err != nil {
		t.Fatalf("digest registers: %v", err)
	}
	for rows.Next() {
		var key, field, actor string
		var value []byte
		var deleted int
		var wall, logical int64
		if err := rows.Scan(&key, &field, &value, &deleted, &wall, &logical, &actor); err != nil {
			rows.Close()
			t.Fatalf("digest scan: %v", err)
		}
		fmt.Fprintf(&b, "R %s/%s val=%q del=%d stamp=%d.%d@%s\n", key, field, string(value), deleted, wall, logical, actor)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("digest registers: %v", err)
	}

	crows, err := s.db.Query(`SELECT key, field, actor, pos, neg FROM crdt_ctr WHERE domain=? ORDER BY key, field, actor`, domain)
	if err != nil {
		t.Fatalf("digest counters: %v", err)
	}
	defer crows.Close()
	for crows.Next() {
		var key, field, actor string
		var pos, neg int64
		if err := crows.Scan(&key, &field, &actor, &pos, &neg); err != nil {
			t.Fatalf("digest scan counter: %v", err)
		}
		fmt.Fprintf(&b, "C %s/%s@%s pos=%d neg=%d\n", key, field, actor, pos, neg)
	}
	if err := crows.Err(); err != nil {
		t.Fatalf("digest counters: %v", err)
	}
	return b.String()
}

// wire round-trips a Delta through JSON, exactly as the HTTP transport does.
//
// Every delivery in these tests goes through it, so the convergence claims also
// cover the on-wire encoding: a field that failed to serialise (or a []byte that
// lost its bytes through base64) shows up as a convergence failure rather than
// passing silently because the test handed the same in-memory pointer around.
func wire(t *testing.T, d *Delta) *Delta {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal delta: %v", err)
	}
	var out Delta
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	return &out
}

// deltaFor computes what `from` would send `to` for a domain, JSON round-tripped.
func deltaFor(t *testing.T, from, to *Store, domain string) *Delta {
	t.Helper()
	vv, err := to.VersionVector(domain)
	if err != nil {
		t.Fatalf("VersionVector: %v", err)
	}
	d, err := from.Delta(domain, vv, 0)
	if err != nil {
		t.Fatalf("Delta: %v", err)
	}
	return wire(t, d)
}

// pull makes `to` pull once from `from`, returning how many ops were new.
func pull(t *testing.T, to, from *Store, domain string) int {
	t.Helper()
	n, err := to.Merge(deltaFor(t, from, to, domain))
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return n
}

// syncAll drives every ordered pair of nodes until a full pass moves nothing.
// Convergence must not depend on how many rounds it takes, only that it settles.
func syncAll(t *testing.T, domain string, nodes ...*Store) {
	t.Helper()
	for round := 0; round < 64; round++ {
		moved := 0
		for _, to := range nodes {
			for _, from := range nodes {
				if to == from {
					continue
				}
				moved += pull(t, to, from, domain)
			}
		}
		if moved == 0 {
			return
		}
	}
	t.Fatalf("syncAll did not settle after 64 rounds")
}

// assertConverged fails unless every node holds byte-identical domain state.
func assertConverged(t *testing.T, domain string, nodes ...*Store) string {
	t.Helper()
	if len(nodes) == 0 {
		t.Fatal("assertConverged: no nodes")
	}
	want := digest(t, nodes[0], domain)
	for i, n := range nodes[1:] {
		got := digest(t, n, domain)
		if got != want {
			t.Errorf("node[0] (%s) and node[%d] (%s) DIVERGED\n--- node[0] ---\n%s\n--- node[%d] ---\n%s",
				nodes[0].actor, i+1, n.actor, want, i+1, got)
		}
	}
	return want
}

// mustGet reads a register and fails on error.
func mustGet(t *testing.T, s *Store, domain, key, field string) (string, bool) {
	t.Helper()
	v, ok, err := s.Get(domain, key, field)
	if err != nil {
		t.Fatalf("Get(%s/%s/%s): %v", domain, key, field, err)
	}
	return string(v), ok
}

// allOps returns every op a store holds for a domain, sorted deterministically.
// Used to build arbitrary delivery orders in the permutation tests.
func allOps(t *testing.T, s *Store, domain string) []Op {
	t.Helper()
	vv, err := s.VersionVector(domain)
	if err != nil {
		t.Fatalf("VersionVector: %v", err)
	}
	var actors []string
	for a := range vv {
		actors = append(actors, a)
	}
	sort.Strings(actors)
	var out []Op
	for _, a := range actors {
		ops, err := s.opsAfter(domain, a, 0, 1<<30)
		if err != nil {
			t.Fatalf("opsAfter: %v", err)
		}
		out = append(out, ops...)
	}
	return out
}

// deliver merges an explicit list of ops as one delta (JSON round-tripped).
func deliver(t *testing.T, to *Store, domain string, ops []Op) int {
	t.Helper()
	d := &Delta{Domain: domain, Ops: ops}
	n, err := to.Merge(wire(t, d))
	if err != nil {
		t.Fatalf("Merge(%d ops): %v", len(ops), err)
	}
	return n
}
