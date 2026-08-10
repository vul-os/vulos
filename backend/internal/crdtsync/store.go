package crdtsync

import (
	"bytes"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	dbmigrate "vulos/backend/internal/migrate"
)

//go:embed migrations
var migrationsFS embed.FS

// ErrSnapshotSchema is returned when a peer sends a snapshot this build cannot
// interpret. Refusing beats half-applying: a partially applied snapshot would
// advance the version vector past state this box does not hold.
var ErrSnapshotSchema = errors.New("crdtsync: unsupported snapshot schema version")

// DefaultDeltaLimit caps how many ops one Delta response carries. A requester
// that is far behind simply takes several rounds; merge is idempotent and
// order-independent so a truncated delta is never a correctness problem.
const DefaultDeltaLimit = 2000

// Store is the CRDT engine: a SQLite-backed, pure-Go, leaderless replica.
//
// Safe for concurrent use. All mutating paths run inside a single SQLite write
// transaction, matching the discipline in internal/multiinstance.
type Store struct {
	db    *sql.DB
	actor string
	clock *clock

	// allowed is the replication allow-list (see policy.go). It is fixed at
	// Open and never mutated, so no lock guards it.
	allowed map[string]bool
	// growOnly marks domains whose registers are add-only and immutable. See
	// DomainDecision.GrowOnly.
	growOnly map[string]bool

	// mu serialises writers. modernc.org/sqlite is opened with
	// SetMaxOpenConns(1); the mutex additionally makes read-modify-write
	// sequences (local seq allocation, counter accumulation) atomic.
	mu sync.Mutex

	hookMu        sync.RWMutex
	onLocalChange func()
}

// Open opens (creating if needed) the CRDT database at dbPath and applies
// migrations.
//
// actor is this box's stable instance ULID — the identity every op this box
// originates is stamped with, and the tie-break that makes two peers pick the
// same LWW winner. It must be stable across restarts.
//
// allowedDomains is the replication allow-list and must not be empty. Passing
// it is deliberately unavoidable: this engine replicates whatever it is given,
// so "which data syncs" is a decision the caller has to make out loud rather
// than inherit from a permissive default. Production wiring passes
// SyncableDomains(); see policy.go for the per-domain reasoning.
func Open(dbPath, actor string, allowedDomains []string) (*Store, error) {
	if actor == "" {
		return nil, errors.New("crdtsync: Open: actor (instance ULID) must not be empty")
	}
	allowed := allowSet(allowedDomains)
	if len(allowed) == 0 {
		return nil, errors.New("crdtsync: Open: allowedDomains is empty — replication is opt-in per domain, never implicit (see policy.go)")
	}
	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("crdtsync: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("crdtsync: ping db: %w", err)
	}
	if err := dbmigrate.Apply(db, migrationsFS, "migrations"); err != nil {
		db.Close()
		return nil, fmt.Errorf("crdtsync: migrate: %w", err)
	}
	// Grow-only is derived from the SAME policy table that decides what
	// replicates, so a domain cannot be approved in one place and given the
	// wrong algebra in another.
	growOnly := map[string]bool{}
	for d := range allowed {
		if IsGrowOnly(d) {
			growOnly[d] = true
		}
	}
	s := &Store{db: db, actor: actor, clock: newClock(), allowed: allowed, growOnly: growOnly}
	// Re-seed the hybrid logical clock from persisted state so a restart cannot
	// emit a stamp that loses to something this box already wrote before the
	// restart (a wall clock that jumped backwards would otherwise do exactly
	// that).
	if st, err := s.maxStamp(); err == nil {
		s.clock.observe(st)
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Actor returns this replica's identity.
func (s *Store) Actor() string { return s.actor }

// SetOnLocalChange registers a callback fired (outside any lock) after a
// successful LOCAL mutation, so a transport can push promptly rather than
// waiting its tick. Mirrors multiinstance.AppSync.SetOnLocalChange.
func (s *Store) SetOnLocalChange(fn func()) {
	s.hookMu.Lock()
	s.onLocalChange = fn
	s.hookMu.Unlock()
}

func (s *Store) fireLocalChange() {
	s.hookMu.RLock()
	fn := s.onLocalChange
	s.hookMu.RUnlock()
	if fn != nil {
		fn()
	}
}

// ── Local mutations ──────────────────────────────────────────────────────────

// Set writes a LWW-register locally and records the op for replication.
func (s *Store) Set(domain, key, field string, value []byte) error {
	return s.local(domain, key, field, OpSet, value)
}

// Delete tombstones a LWW-register locally. The tombstone carries a stamp and
// competes with concurrent writes by stamp, so a delete that loses the race
// does not resurrect and a delete that wins is not undone by a late-arriving
// older write.
func (s *Store) Delete(domain, key, field string) error {
	return s.local(domain, key, field, OpDel, nil)
}

// Add applies a signed delta to a PN-counter.
//
// The emitted op carries this actor's CUMULATIVE (pos, neg) totals rather than
// the delta, so replaying or duplicating the op is a no-op instead of a double
// count — merge takes the per-actor maximum of each total.
func (s *Store) Add(domain, key, field string, delta int64) error {
	if domain == "" || key == "" || field == "" {
		return errors.New("crdtsync: Add: domain, key and field are required")
	}
	if err := s.checkDomain(domain); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inTx(func(tx *sql.Tx) error {
		var pos, neg int64
		err := tx.QueryRow(`SELECT pos, neg FROM crdt_ctr WHERE domain=? AND key=? AND field=? AND actor=?`,
			domain, key, field, s.actor).Scan(&pos, &neg)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if delta >= 0 {
			pos += delta
		} else {
			neg += -delta
		}
		op := Op{
			Domain: domain, Actor: s.actor, Key: key, Field: field,
			Kind: OpInc, Value: encodeCounter(pos, neg),
			Stamp: s.clock.tick(s.actor),
		}
		return s.appendLocal(tx, &op)
	}, true)
}

// local is the shared write path for Set/Delete.
func (s *Store) local(domain, key, field string, kind OpKind, value []byte) error {
	if domain == "" || key == "" || field == "" {
		return errors.New("crdtsync: domain, key and field are required")
	}
	if err := s.checkDomain(domain); err != nil {
		return err
	}
	// A grow-only domain has no delete in its algebra — refused HERE as well
	// as at the merge boundary, so a local caller cannot produce an op that
	// every peer would then have to refuse.
	if kind == OpDel && s.growOnly[domain] {
		return &ErrGrowOnlyViolation{Domain: domain, Op: "delete"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inTx(func(tx *sql.Tx) error {
		op := Op{
			Domain: domain, Actor: s.actor, Key: key, Field: field,
			Kind: kind, Value: value, Stamp: s.clock.tick(s.actor),
		}
		return s.appendLocal(tx, &op)
	}, true)
}

// appendLocal allocates the next sequence number for this actor, writes the op
// to the log, merges it into the materialised state, and advances the version
// vector.
func (s *Store) appendLocal(tx *sql.Tx, op *Op) error {
	cur, _, err := s.metaFor(tx, op.Domain, s.actor)
	if err != nil {
		return err
	}
	op.Seq = cur + 1
	if err := s.insertOp(tx, *op); err != nil {
		return err
	}
	if err := s.applyToState(tx, *op); err != nil {
		return err
	}
	return s.setSeq(tx, op.Domain, s.actor, op.Seq)
}

// ── Reads ────────────────────────────────────────────────────────────────────

// Get returns the current value of a LWW-register. ok is false when the field
// was never written or is tombstoned.
func (s *Store) Get(domain, key, field string) (value []byte, ok bool, err error) {
	var v []byte
	var deleted int
	err = s.db.QueryRow(`SELECT value, deleted FROM crdt_reg WHERE domain=? AND key=? AND field=?`,
		domain, key, field).Scan(&v, &deleted)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("crdtsync: Get: %w", err)
	}
	if deleted == 1 {
		return nil, false, nil
	}
	return v, true, nil
}

// Fields returns every live field of one key.
func (s *Store) Fields(domain, key string) (map[string][]byte, error) {
	rows, err := s.db.Query(`SELECT field, value FROM crdt_reg WHERE domain=? AND key=? AND deleted=0 ORDER BY field`,
		domain, key)
	if err != nil {
		return nil, fmt.Errorf("crdtsync: Fields: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]byte)
	for rows.Next() {
		var f string
		var v []byte
		if err := rows.Scan(&f, &v); err != nil {
			return nil, err
		}
		out[f] = v
	}
	return out, rows.Err()
}

// Keys returns every key in a domain that has at least one live field.
func (s *Store) Keys(domain string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT key FROM crdt_reg WHERE domain=? AND deleted=0 ORDER BY key`, domain)
	if err != nil {
		return nil, fmt.Errorf("crdtsync: Keys: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// AllKeys returns every key in a domain, INCLUDING keys whose fields are all
// tombstoned. Keys omits those; a materialiser needs them, because a deleted
// row must be actively removed from the target store rather than merely not
// written.
func (s *Store) AllKeys(domain string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT key FROM crdt_reg WHERE domain=? ORDER BY key`, domain)
	if err != nil {
		return nil, fmt.Errorf("crdtsync: AllKeys: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Counter returns the merged PN-counter value: Σpos − Σneg across all actors.
func (s *Store) Counter(domain, key, field string) (int64, error) {
	rows, err := s.db.Query(`SELECT pos, neg FROM crdt_ctr WHERE domain=? AND key=? AND field=?`, domain, key, field)
	if err != nil {
		return 0, fmt.Errorf("crdtsync: Counter: %w", err)
	}
	defer rows.Close()
	var total int64
	for rows.Next() {
		var pos, neg int64
		if err := rows.Scan(&pos, &neg); err != nil {
			return 0, err
		}
		total += pos - neg
	}
	return total, rows.Err()
}

// Domains lists every domain this replica holds state or log for.
func (s *Store) Domains() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT domain FROM crdt_meta
		UNION SELECT domain FROM crdt_reg
		UNION SELECT domain FROM crdt_ctr
		ORDER BY domain`)
	if err != nil {
		return nil, fmt.Errorf("crdtsync: Domains: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// VersionVector returns this replica's version vector for a domain.
func (s *Store) VersionVector(domain string) (VersionVector, error) {
	rows, err := s.db.Query(`SELECT actor, seq FROM crdt_meta WHERE domain=? AND seq > 0`, domain)
	if err != nil {
		return nil, fmt.Errorf("crdtsync: VersionVector: %w", err)
	}
	defer rows.Close()
	vv := VersionVector{}
	for rows.Next() {
		var a string
		var q int64
		if err := rows.Scan(&a, &q); err != nil {
			return nil, err
		}
		vv[a] = uint64(q)
	}
	return vv, rows.Err()
}

// ── Reconciliation ───────────────────────────────────────────────────────────

// Delta returns the ops peerVV has not seen, together with this replica's own
// version vector so the caller can immediately compute the reverse direction.
//
// When peerVV is behind this replica's compaction floor for any actor, the
// difference can no longer be expressed as ops. Delta then returns a full
// Snapshot (SnapshotRequired=true) — the bounded bootstrap path that keeps a
// joining node from replaying unbounded history.
func (s *Store) Delta(domain string, peerVV VersionVector, limit int) (*Delta, error) {
	if err := s.checkDomain(domain); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultDeltaLimit
	}
	if peerVV == nil {
		peerVV = VersionVector{}
	}
	ourVV, err := s.VersionVector(domain)
	if err != nil {
		return nil, err
	}
	floors, err := s.floors(domain)
	if err != nil {
		return nil, err
	}
	for actor, seq := range ourVV {
		if peerVV[actor] < floors[actor] && seq > peerVV[actor] {
			snap, serr := s.Snapshot(domain)
			if serr != nil {
				return nil, serr
			}
			return &Delta{Domain: domain, SenderVV: ourVV, SnapshotRequired: true, Snapshot: snap}, nil
		}
	}

	d := &Delta{Domain: domain, SenderVV: ourVV}
	actors := make([]string, 0, len(ourVV))
	for a := range ourVV {
		actors = append(actors, a)
	}
	sort.Strings(actors)
	for _, a := range actors {
		if ourVV[a] <= peerVV[a] {
			continue
		}
		remaining := limit - len(d.Ops)
		if remaining <= 0 {
			d.Truncated = true
			break
		}
		// Ordered by seq ascending and limited, so the requester always gets a
		// CONTIGUOUS PREFIX of this actor's missing ops. That is what lets the
		// receiver advance its version vector without ever skipping a gap.
		ops, err := s.opsAfter(domain, a, peerVV[a], remaining+1)
		if err != nil {
			return nil, err
		}
		if len(ops) > remaining {
			ops = ops[:remaining]
			d.Truncated = true
		}
		d.Ops = append(d.Ops, ops...)
		if d.Truncated {
			break
		}
	}
	return d, nil
}

// Merge applies a peer's delta. It is idempotent (a duplicate op is absorbed),
// order-independent (state merge is a max), and safe against gaps (the version
// vector only advances across a contiguous run actually present in the log).
//
// Returns the number of ops newly recorded.
func (s *Store) Merge(d *Delta) (int, error) {
	if d == nil {
		return 0, errors.New("crdtsync: Merge: nil delta")
	}
	// Enforced BEFORE anything is written: a peer must not be able to introduce
	// a domain this box never opted into merely by naming it in a push.
	if err := s.checkDomain(d.Domain); err != nil {
		return 0, err
	}
	if d.SnapshotRequired {
		if d.Snapshot == nil {
			return 0, errors.New("crdtsync: Merge: snapshot required but none supplied")
		}
		if err := s.ApplySnapshot(d.Snapshot); err != nil {
			return 0, err
		}
		return len(d.Snapshot.Registers) + len(d.Snapshot.Counters), nil
	}
	if len(d.Ops) == 0 {
		return 0, nil
	}
	for _, op := range d.Ops {
		if !op.Kind.Valid() {
			return 0, fmt.Errorf("crdtsync: Merge: unknown op kind %q from actor %s seq %d", op.Kind, op.Actor, op.Seq)
		}
		if op.Actor == "" || op.Seq == 0 || op.Key == "" || op.Field == "" {
			return 0, fmt.Errorf("crdtsync: Merge: malformed op from actor %q seq %d", op.Actor, op.Seq)
		}
		// The stamp's actor MUST be the op's origin actor. crdt_op does not store
		// the stamp actor separately — opsAfter reconstructs it from the origin —
		// so an op with a mismatched stamp actor would be merged HERE with one
		// tie-break and RELAYED to a third box with a different one. That is a
		// genuine divergence, reachable by any buggy or hostile peer, and it is
		// cheap to close: an honest actor never produces such an op, because it
		// stamps every op it originates with its own id.
		if op.Stamp.Actor != op.Actor {
			return 0, fmt.Errorf("crdtsync: Merge: op from actor %s seq %d carries stamp actor %q: stamp actor must equal the origin actor",
				op.Actor, op.Seq, op.Stamp.Actor)
		}
		// A grow-only domain accepts no tombstones FROM A PEER either. This is
		// the half that matters: the local refusal above is a correctness
		// guard, this one is the security boundary — it is what stops a
		// compromised-but-rostered box editing the audit trail on every other
		// box in the fleet.
		if op.Kind == OpDel && s.growOnly[d.Domain] {
			return 0, &ErrGrowOnlyViolation{Domain: d.Domain, Op: fmt.Sprintf("delete from actor %s seq %d", op.Actor, op.Seq)}
		}
		// An op may name its own domain; it must be allowed too, or a delta for
		// an approved domain could smuggle ops into a refused one.
		if op.Domain != "" && op.Domain != d.Domain {
			return 0, fmt.Errorf("crdtsync: Merge: op from actor %s seq %d claims domain %q inside a delta for %q",
				op.Actor, op.Seq, op.Domain, d.Domain)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	applied := 0
	touched := map[string]struct{}{}
	err := s.inTx(func(tx *sql.Tx) error {
		for _, op := range d.Ops {
			if op.Domain == "" {
				op.Domain = d.Domain
			}
			n, err := s.insertOpIfNew(tx, op)
			if err != nil {
				return err
			}
			// The STATE merge runs unconditionally, not only for newly-inserted
			// ops. Re-applying is free (it is a max), and it keeps this loop
			// independent of whether the log row happened to be new — which
			// matters after compaction, when an op can be absent from the log
			// while its effect is already in state.
			//
			// Note this is belt-and-braces, not crash recovery: the log insert
			// and the state merge share one transaction, so there is no window
			// in which one lands without the other. Mutation testing confirms
			// no test distinguishes the two forms.
			if err := s.applyToState(tx, op); err != nil {
				return err
			}
			applied += n
			touched[op.Actor] = struct{}{}
		}
		for a := range touched {
			if err := s.advanceContiguous(tx, d.Domain, a); err != nil {
				return err
			}
		}
		return nil
	}, false)
	if err != nil {
		return 0, err
	}
	// Advance the hybrid logical clock past everything we just merged so any op
	// this box emits next sorts after it.
	for _, op := range d.Ops {
		s.clock.observe(op.Stamp)
	}
	return applied, nil
}

// ── Snapshots ────────────────────────────────────────────────────────────────

// Snapshot serialises the whole materialised state of a domain plus the version
// vector it covers. Size is bounded by live key/field count, never by history.
func (s *Store) Snapshot(domain string) (*Snapshot, error) {
	vv, err := s.VersionVector(domain)
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Domain: domain, VV: vv, TakenAtMS: time.Now().UTC().UnixMilli(), SchemaVers: SnapshotSchemaVersion}

	rows, err := s.db.Query(`SELECT key, field, value, deleted, wall, logical, actor FROM crdt_reg WHERE domain=? ORDER BY key, field`, domain)
	if err != nil {
		return nil, fmt.Errorf("crdtsync: Snapshot: registers: %w", err)
	}
	for rows.Next() {
		var r RegisterState
		var deleted int
		if err := rows.Scan(&r.Key, &r.Field, &r.Value, &deleted, &r.Stamp.Wall, &r.Stamp.Logical, &r.Stamp.Actor); err != nil {
			rows.Close()
			return nil, err
		}
		r.Deleted = deleted == 1
		snap.Registers = append(snap.Registers, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	crows, err := s.db.Query(`SELECT key, field, actor, pos, neg FROM crdt_ctr WHERE domain=? ORDER BY key, field, actor`, domain)
	if err != nil {
		return nil, fmt.Errorf("crdtsync: Snapshot: counters: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var c CounterState
		if err := crows.Scan(&c.Key, &c.Field, &c.Actor, &c.Pos, &c.Neg); err != nil {
			return nil, err
		}
		snap.Counters = append(snap.Counters, c)
	}
	return snap, crows.Err()
}

// ApplySnapshot MERGES a snapshot into this replica. Every register goes
// through the same max-by-stamp rule and every counter through the same
// per-actor max, so a node that holds writes the snapshot's author never saw
// keeps them. The version vector and the compaction floor both rise to the
// snapshot's coverage: this replica now holds the STATE for those ops but not
// their log rows, so it must tell a third peer "snapshot required" rather than
// promise ops it cannot produce.
func (s *Store) ApplySnapshot(snap *Snapshot) error {
	if snap == nil {
		return errors.New("crdtsync: ApplySnapshot: nil snapshot")
	}
	if err := s.checkDomain(snap.Domain); err != nil {
		return err
	}
	if snap.SchemaVers > SnapshotSchemaVersion {
		return fmt.Errorf("%w: got %d, this build understands %d", ErrSnapshotSchema, snap.SchemaVers, SnapshotSchemaVersion)
	}
	// A snapshot is the third way state can enter this replica, so the
	// grow-only refusal has to be here too. Without it, a peer that cannot
	// tombstone an audit entry with an op could simply hand over a snapshot
	// carrying the same tombstone and get the identical effect.
	if s.growOnly[snap.Domain] {
		for _, r := range snap.Registers {
			if r.Deleted {
				return &ErrGrowOnlyViolation{Domain: snap.Domain,
					Op: fmt.Sprintf("tombstone for %s/%s inside a snapshot", r.Key, r.Field)}
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.inTx(func(tx *sql.Tx) error {
		for _, r := range snap.Registers {
			if err := s.mergeRegister(tx, snap.Domain, r.Key, r.Field, r.Value, r.Deleted, r.Stamp); err != nil {
				return err
			}
		}
		for _, c := range snap.Counters {
			if err := s.mergeCounter(tx, snap.Domain, c.Key, c.Field, c.Actor, c.Pos, c.Neg); err != nil {
				return err
			}
		}
		for actor, seq := range snap.VV {
			cur, floor, err := s.metaFor(tx, snap.Domain, actor)
			if err != nil {
				return err
			}
			newSeq, newFloor := cur, floor
			if seq > newSeq {
				newSeq = seq
			}
			if seq > newFloor {
				newFloor = seq
			}
			if err := s.putMeta(tx, snap.Domain, actor, newSeq, newFloor); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM crdt_op WHERE domain=? AND actor=? AND seq<=?`, snap.Domain, actor, int64(newFloor)); err != nil {
				return err
			}
			// Ops we already held ABOVE the snapshot's coverage stay valid, so
			// pick the contiguous run back up from the new floor.
			if err := s.advanceContiguous(tx, snap.Domain, actor); err != nil {
				return err
			}
		}
		return nil
	}, false)
	if err != nil {
		return err
	}
	for _, r := range snap.Registers {
		s.clock.observe(r.Stamp)
	}
	return nil
}

// Compact prunes the op log to the newest keepPerActor ops per actor and raises
// the compaction floor accordingly. Peers that fall behind the floor are served
// a snapshot instead, so pruning is always safe — it costs bandwidth for a
// straggler, never correctness. Returns the number of log rows removed.
func (s *Store) Compact(domain string, keepPerActor int) (int, error) {
	if keepPerActor < 0 {
		return 0, errors.New("crdtsync: Compact: keepPerActor must be >= 0")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pruned := 0
	err := s.inTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT actor, seq, floor FROM crdt_meta WHERE domain=?`, domain)
		if err != nil {
			return err
		}
		type meta struct {
			actor      string
			seq, floor int64
		}
		var metas []meta
		for rows.Next() {
			var m meta
			if err := rows.Scan(&m.actor, &m.seq, &m.floor); err != nil {
				rows.Close()
				return err
			}
			metas = append(metas, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, m := range metas {
			newFloor := m.seq - int64(keepPerActor)
			if newFloor <= m.floor {
				continue
			}
			res, err := tx.Exec(`DELETE FROM crdt_op WHERE domain=? AND actor=? AND seq<=?`, domain, m.actor, newFloor)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			pruned += int(n)
			if err := s.putMeta(tx, domain, m.actor, uint64(m.seq), uint64(newFloor)); err != nil {
				return err
			}
		}
		return nil
	}, false)
	return pruned, err
}

// LogSize reports how many op rows a domain currently retains (observability
// and a direct assertion target for the compaction tests).
func (s *Store) LogSize(domain string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM crdt_op WHERE domain=?`, domain).Scan(&n)
	return n, err
}

// ── Internals ────────────────────────────────────────────────────────────────

func (s *Store) inTx(fn func(*sql.Tx) error, fireHook bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("crdtsync: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("crdtsync: commit: %w", err)
	}
	committed = true
	if fireHook {
		s.fireLocalChange()
	}
	return nil
}

func (s *Store) metaFor(tx *sql.Tx, domain, actor string) (seq, floor uint64, err error) {
	var sq, fl int64
	e := tx.QueryRow(`SELECT seq, floor FROM crdt_meta WHERE domain=? AND actor=?`, domain, actor).Scan(&sq, &fl)
	if e == sql.ErrNoRows {
		return 0, 0, nil
	}
	if e != nil {
		return 0, 0, e
	}
	return uint64(sq), uint64(fl), nil
}

func (s *Store) putMeta(tx *sql.Tx, domain, actor string, seq, floor uint64) error {
	_, err := tx.Exec(`
		INSERT INTO crdt_meta (domain, actor, seq, floor) VALUES (?, ?, ?, ?)
		ON CONFLICT(domain, actor) DO UPDATE SET seq=excluded.seq, floor=excluded.floor`,
		domain, actor, int64(seq), int64(floor))
	return err
}

func (s *Store) setSeq(tx *sql.Tx, domain, actor string, seq uint64) error {
	cur, floor, err := s.metaFor(tx, domain, actor)
	if err != nil {
		return err
	}
	if seq <= cur {
		return nil
	}
	return s.putMeta(tx, domain, actor, seq, floor)
}

func (s *Store) floors(domain string) (map[string]uint64, error) {
	rows, err := s.db.Query(`SELECT actor, floor FROM crdt_meta WHERE domain=?`, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]uint64{}
	for rows.Next() {
		var a string
		var f int64
		if err := rows.Scan(&a, &f); err != nil {
			return nil, err
		}
		out[a] = uint64(f)
	}
	return out, rows.Err()
}

func (s *Store) insertOp(tx *sql.Tx, op Op) error {
	_, err := tx.Exec(`
		INSERT INTO crdt_op (domain, actor, seq, key, field, kind, value, wall, logical)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.Domain, op.Actor, int64(op.Seq), op.Key, op.Field, string(op.Kind), op.Value,
		op.Stamp.Wall, int64(op.Stamp.Logical))
	return err
}

// insertOpIfNew records a received op, ignoring an exact duplicate (same
// origin actor + seq). Returns 1 when the row was new.
func (s *Store) insertOpIfNew(tx *sql.Tx, op Op) (int, error) {
	res, err := tx.Exec(`
		INSERT OR IGNORE INTO crdt_op (domain, actor, seq, key, field, kind, value, wall, logical)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.Domain, op.Actor, int64(op.Seq), op.Key, op.Field, string(op.Kind), op.Value,
		op.Stamp.Wall, int64(op.Stamp.Logical))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) opsAfter(domain, actor string, after uint64, limit int) ([]Op, error) {
	rows, err := s.db.Query(`
		SELECT key, field, kind, value, wall, logical, seq
		FROM crdt_op WHERE domain=? AND actor=? AND seq>?
		ORDER BY seq LIMIT ?`, domain, actor, int64(after), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Op
	for rows.Next() {
		op := Op{Domain: domain, Actor: actor}
		var kind string
		var logical int64
		var seq int64
		if err := rows.Scan(&op.Key, &op.Field, &kind, &op.Value, &op.Stamp.Wall, &logical, &seq); err != nil {
			return nil, err
		}
		op.Kind = OpKind(kind)
		op.Stamp.Logical = uint32(logical)
		op.Stamp.Actor = actor
		op.Seq = uint64(seq)
		out = append(out, op)
	}
	return out, rows.Err()
}

// advanceContiguous raises the version-vector entry for actor to the end of the
// contiguous run of log rows starting immediately after the current entry.
//
// This is the guard that makes a gap harmless: if a peer (buggy or hostile)
// hands us seq 7 while we are still missing seq 6, the state merge still runs
// (state is stamp-ordered, so it is correct regardless), but the version vector
// stays at 5 and we keep asking for 6 until we get it.
func (s *Store) advanceContiguous(tx *sql.Tx, domain, actor string) error {
	cur, floor, err := s.metaFor(tx, domain, actor)
	if err != nil {
		return err
	}
	next := cur
	for {
		var one int
		e := tx.QueryRow(`SELECT 1 FROM crdt_op WHERE domain=? AND actor=? AND seq=?`, domain, actor, int64(next+1)).Scan(&one)
		if e == sql.ErrNoRows {
			break
		}
		if e != nil {
			return e
		}
		next++
	}
	if next == cur {
		return nil
	}
	return s.putMeta(tx, domain, actor, next, floor)
}

// applyToState folds one op into the materialised state.
func (s *Store) applyToState(tx *sql.Tx, op Op) error {
	switch op.Kind {
	case OpSet:
		return s.mergeRegister(tx, op.Domain, op.Key, op.Field, op.Value, false, op.Stamp)
	case OpDel:
		return s.mergeRegister(tx, op.Domain, op.Key, op.Field, nil, true, op.Stamp)
	case OpInc:
		pos, neg, err := decodeCounter(op.Value)
		if err != nil {
			return fmt.Errorf("crdtsync: op %s/%d: %w", op.Actor, op.Seq, err)
		}
		return s.mergeCounter(tx, op.Domain, op.Key, op.Field, op.Actor, pos, neg)
	default:
		return fmt.Errorf("crdtsync: applyToState: unknown kind %q", op.Kind)
	}
}

// mergeRegister is THE convergence rule for LWW-registers: keep whichever of
// (local, incoming) is greater under the total order
//
//	(Wall, Logical, Actor, liveness, value)
//
// Because that is a total order and the merge is "take the max", it is
// commutative, associative and idempotent — which is precisely what makes
// out-of-order and duplicate delivery converge to the same state.
func (s *Store) mergeRegister(tx *sql.Tx, domain, key, field string, value []byte, deleted bool, stamp Stamp) error {
	var (
		curVal     []byte
		curDeleted int
		curStamp   Stamp
	)
	err := tx.QueryRow(`SELECT value, deleted, wall, logical, actor FROM crdt_reg WHERE domain=? AND key=? AND field=?`,
		domain, key, field).Scan(&curVal, &curDeleted, &curStamp.Wall, &curStamp.Logical, &curStamp.Actor)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && !s.registerWinsIn(domain, stamp, deleted, value, curStamp, curDeleted == 1, curVal) {
		return nil
	}
	d := 0
	if deleted {
		d = 1
		value = nil
	}
	_, e := tx.Exec(`
		INSERT INTO crdt_reg (domain, key, field, value, deleted, wall, logical, actor)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(domain, key, field) DO UPDATE SET
			value=excluded.value, deleted=excluded.deleted,
			wall=excluded.wall, logical=excluded.logical, actor=excluded.actor`,
		domain, key, field, value, d, stamp.Wall, int64(stamp.Logical), stamp.Actor)
	return e
}

// registerWinsIn applies the domain's algebra: last-writer-wins everywhere
// except a grow-only domain, where a register is immutable once written and the
// FIRST writer keeps it.
//
// Inverting the comparison is all it takes, and that is the point: min over a
// total order is exactly as commutative, associative and idempotent as max, so
// grow-only costs the convergence guarantee nothing.
func (s *Store) registerWinsIn(domain string, inStamp Stamp, inDeleted bool, inVal []byte, curStamp Stamp, curDeleted bool, curVal []byte) bool {
	if s.growOnly[domain] {
		if c := inStamp.Compare(curStamp); c != 0 {
			return c < 0
		}
		// An exact stamp collision still needs a deterministic answer, and it
		// must be the same one on every box. Smaller value bytes win, mirroring
		// the inverted stamp rule.
		return bytes.Compare(inVal, curVal) < 0
	}
	return registerWins(inStamp, inDeleted, inVal, curStamp, curDeleted, curVal)
}

// registerWins reports whether the incoming register state strictly beats the
// current one under the total order described on mergeRegister.
//
// The trailing two components only decide an EXACT stamp collision, which a
// well-behaved actor cannot produce (its stamps are monotonic and carry its own
// id) but a byzantine one can. Live beats deleted — the same bias appsync gives
// install over uninstall — and larger value bytes break the final tie, so two
// boxes handed the same forged pair still agree.
func registerWins(inStamp Stamp, inDeleted bool, inVal []byte, curStamp Stamp, curDeleted bool, curVal []byte) bool {
	if c := inStamp.Compare(curStamp); c != 0 {
		return c > 0
	}
	if inDeleted != curDeleted {
		return !inDeleted // live wins over deleted
	}
	return bytes.Compare(inVal, curVal) > 0
}

// mergeCounter takes the per-actor MAX of the cumulative pos/neg totals. Max is
// commutative, associative and idempotent, so a duplicated or replayed
// increment is absorbed instead of double-counted.
func (s *Store) mergeCounter(tx *sql.Tx, domain, key, field, actor string, pos, neg int64) error {
	_, err := tx.Exec(`
		INSERT INTO crdt_ctr (domain, key, field, actor, pos, neg)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(domain, key, field, actor) DO UPDATE SET
			pos = MAX(pos, excluded.pos),
			neg = MAX(neg, excluded.neg)`,
		domain, key, field, actor, pos, neg)
	return err
}

// maxStamp returns the highest stamp anywhere in this replica's state, used to
// re-seed the hybrid logical clock at Open.
func (s *Store) maxStamp() (Stamp, error) {
	var st Stamp
	var logical int64
	err := s.db.QueryRow(`SELECT wall, logical, actor FROM crdt_reg ORDER BY wall DESC, logical DESC, actor DESC LIMIT 1`).
		Scan(&st.Wall, &logical, &st.Actor)
	if err != nil {
		return Stamp{}, err
	}
	st.Logical = uint32(logical)
	return st, nil
}

// encodeCounter/decodeCounter carry a PN-counter's cumulative per-actor totals
// in an op's Value as "pos:neg" decimal text.
func encodeCounter(pos, neg int64) []byte {
	return []byte(strconv.FormatInt(pos, 10) + ":" + strconv.FormatInt(neg, 10))
}

func decodeCounter(v []byte) (pos, neg int64, err error) {
	parts := strings.SplitN(string(v), ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("malformed counter payload %q", string(v))
	}
	pos, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("malformed counter pos %q", parts[0])
	}
	neg, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("malformed counter neg %q", parts[1])
	}
	if pos < 0 || neg < 0 {
		return 0, 0, fmt.Errorf("negative counter totals %d/%d", pos, neg)
	}
	return pos, neg, nil
}
