// MINST-04: App registry sync — pure-Go CRDT changeset replication.
//
// AppSync replicates the app_registry table across instances using a
// pure-Go CRDT changeset layer (no CGO / cr-sqlite extension required).
//
// Conflict resolution strategy
//
//   - app_version field:  Last-Write-Wins (LWW) — the row with the higher
//     updated_at timestamp wins.  On a tie, the deterministic tie-break is the
//     lexicographically larger writer node id (installed_by).  Comparing the
//     writer node — not the row's instance_ulid, which is identical on both
//     sides of a merge for the same (instance_ulid, app_id) key — is what makes
//     two peers that observe the same pair of concurrent writes converge to the
//     SAME winner regardless of which side applies first.  (Audit P1-3.)
//   - installed flag:     OR-set semantics — install wins over uninstall
//     when the timestamps are equal.  A true uninstall (status=0) only
//     propagates when its updated_at is strictly newer than the local row.
//     Exception: when more than 2 instances exist in the registry, an
//     uninstall is only accepted when distinct-origin quorum is met.
//
// Quorum hardening (audit P1-3 + CRDT-QUORUM-01)
//
//	The uninstall quorum is met by N DISTINCT ORIGINATING INSTANCES having
//	independently reported the uninstall — never by any self-reported
//	AppChangeset.PeerCount.  Each uninstall changeset carries its reporting
//	instance's identity (AppChangeset.OriginULID); applying it records ONE
//	observation in app_uninstall_observations keyed by (instance_ulid, app_id,
//	observer_ulid).  The uninstall applies only once the number of DISTINCT
//	observers meets the quorum threshold derived from the LOCALLY-observed peer
//	count (a majority of the registry roster).  Consequences:
//	  (a) A single peer that inflates PeerCount to 99 still contributes exactly
//	      ONE observation and cannot force a removal — the field is ignored for
//	      the security decision and kept only as a non-authoritative telemetry
//	      hint.
//	  (b) A peer cannot unilaterally BLOCK a legitimate removal: it can only
//	      withhold its own observation; other instances' observations still
//	      accumulate toward quorum.
//	  (c) When the local registry shows ≤ 2 instances quorum is not required at
//	      all (a 2-node system cannot form a majority): a strictly-newer
//	      uninstall is accepted on LWW alone.
//	The observation set is a monotonic OR-set persisted in SQLite so it survives
//	merges/restarts and converges deterministically (union semantics).
//
// Wire format
//
//	Changesets are JSON-encoded AppChangeset values.  They are transport-
//	agnostic — the caller serialises and delivers them via whatever channel
//	it chooses (direct HTTP, relay deposit, etc.).  ApplyChangeset handles
//	the merge atomically under a single SQLite write transaction.
//
// HTTP endpoints (wired via RegisterAppSyncHandlers — never from main.go)
//
//	GET /api/instances/:ulid/apps   — per-instance app inventory
//
// Usage
//
//	as, err := multiinstance.OpenAppSync(reg)
//	// emit on local install:
//	cs, err := as.EmitChangeset("my-ulid", []AppRegistryEntry{...})
//	// apply received changeset from a peer:
//	err = as.ApplyChangeset(cs)
//	// HTTP handler wiring:
//	multiinstance.RegisterAppSyncHandlers(mux, as)
package multiinstance

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ── Domain types ──────────────────────────────────────────────────────────────

// AppRegistryEntry is one row in the app_registry table.
type AppRegistryEntry struct {
	// InstanceULID identifies which instance hosts this app.
	InstanceULID string `json:"instance_ulid"`
	// AppID is the canonical app slug (e.g. "browser", "notes").
	AppID string `json:"app_id"`
	// AppVersion is the installed version string (e.g. "1.2.3").
	AppVersion string `json:"app_version"`
	// Installed is the OR-set flag: true = installed, false = uninstalled.
	Installed bool `json:"installed"`
	// InstalledBy is the node ID that wrote this row.
	InstalledBy string `json:"installed_by"`
	// UpdatedAt is the wall-clock timestamp used for LWW ordering.
	UpdatedAt time.Time `json:"updated_at"`
}

// AppChangeset is the wire format exchanged between instances.
// Each changeset carries all changed rows from one instance since the
// last sync.  Receivers call ApplyChangeset to merge it into local state.
type AppChangeset struct {
	// OriginULID is the instance that produced this changeset.
	OriginULID string `json:"origin_ulid"`
	// Entries are the changed rows.
	Entries []AppRegistryEntry `json:"entries"`
	// PeerCount is the number of instances the originator knew about when it
	// emitted this changeset.  TELEMETRY ONLY — it is NOT trusted for the
	// uninstall-quorum decision (see CRDT-QUORUM-01). Quorum is decided from the
	// count of DISTINCT OriginULIDs that have reported an uninstall, gated by the
	// locally-observed peer count. A peer cannot force a removal by inflating
	// this field.
	PeerCount int `json:"peer_count"`
}

// ── AppSync ───────────────────────────────────────────────────────────────────

// AppSync manages the app_registry table and provides CRDT merge logic.
// It is safe for concurrent use.
type AppSync struct {
	reg *Registry // borrows the same *sql.DB (via the exported DB() accessor)
	db  *sql.DB

	// onLocalChange, when set, is invoked (without holding any lock) after a
	// successful LOCAL app-registry mutation (LocalInstall / LocalUninstall).
	// The fabric P2P sync service registers its Nudge here so a local install/
	// uninstall triggers an immediate out-of-band sync push rather than waiting
	// the background tick. Guarded by hookMu so it can be set after construction.
	hookMu        sync.RWMutex
	onLocalChange func()
}

// OpenAppSync creates an AppSync that shares the Registry's database.
// The app_registry migration (0004_app_registry.sql) must already be applied
// by the registry's migrate() function (it is embedded in the migrations dir).
func OpenAppSync(reg *Registry) (*AppSync, error) {
	db := reg.DB()
	if db == nil {
		return nil, fmt.Errorf("multiinstance/appsync: registry DB is nil")
	}
	return &AppSync{reg: reg, db: db}, nil
}

// SetOnLocalChange registers a callback fired (without holding any AppSync lock)
// after a successful LOCAL app-registry mutation — LocalInstall or
// LocalUninstall. The fabric P2P sync service passes its Nudge here so a local
// change converges immediately instead of waiting the background sync tick.
// Passing nil clears the hook. Safe to call at any time.
func (as *AppSync) SetOnLocalChange(fn func()) {
	as.hookMu.Lock()
	as.onLocalChange = fn
	as.hookMu.Unlock()
}

// fireLocalChange invokes the registered local-change hook, if any. Called after
// a local mutation commits. The hook (typically fabric Nudge) must be cheap and
// non-blocking; it is invoked synchronously but outside any lock.
func (as *AppSync) fireLocalChange() {
	as.hookMu.RLock()
	fn := as.onLocalChange
	as.hookMu.RUnlock()
	if fn != nil {
		fn()
	}
}

// ── Local mutations ────────────────────────────────────────────────────────────

// LocalInstall records that appID@version is installed on THIS instance and
// fires the local-change hook (fabric Nudge) so peers learn promptly.
//
// It writes a single app_registry row stamped with UpdatedAt = now and
// InstalledBy = instanceULID (this box is the writer node), which is exactly the
// LWW input ChangesetSince/EmitChangeset will later replicate. instanceULID must
// be this box's stable ULID.
func (as *AppSync) LocalInstall(instanceULID, appID, version string) error {
	return as.localMutate(instanceULID, appID, version, true)
}

// LocalUninstall records that appID is uninstalled on THIS instance (OR-set flag
// flipped to false, stamped now) and fires the local-change hook. The actual
// propagation still obeys the uninstall-quorum rules on the receiving peers.
func (as *AppSync) LocalUninstall(instanceULID, appID string) error {
	return as.localMutate(instanceULID, appID, "", false)
}

// localMutate is the shared write path for LocalInstall/LocalUninstall. It
// upserts the row in a single transaction, then — only on commit success —
// fires the local-change hook so the fabric sync loop pushes the change without
// waiting the tick.
func (as *AppSync) localMutate(instanceULID, appID, version string, installed bool) error {
	if instanceULID == "" {
		return fmt.Errorf("appsync: localMutate: instanceULID must not be empty")
	}
	if appID == "" {
		return fmt.Errorf("appsync: localMutate: appID must not be empty")
	}
	tx, err := as.db.Begin()
	if err != nil {
		return fmt.Errorf("appsync: localMutate: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	entry := AppRegistryEntry{
		InstanceULID: instanceULID,
		AppID:        appID,
		AppVersion:   version,
		Installed:    installed,
		InstalledBy:  instanceULID,
		UpdatedAt:    time.Now().UTC(),
	}
	if err := as.upsertEntry(tx, entry); err != nil {
		return fmt.Errorf("appsync: localMutate: upsert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("appsync: localMutate: commit: %w", err)
	}
	committed = true
	// Local change is durable — nudge the fabric so it converges immediately.
	as.fireLocalChange()
	return nil
}

// ── Emit ─────────────────────────────────────────────────────────────────────

// EmitChangeset builds a changeset from the provided entries and returns it
// ready for delivery to peers.  The caller is responsible for transport.
//
// Typical usage: call after a local install/uninstall to produce the
// changeset that should be pushed to all known peers.
func (as *AppSync) EmitChangeset(originULID string, entries []AppRegistryEntry) (*AppChangeset, error) {
	if originULID == "" {
		return nil, fmt.Errorf("appsync: EmitChangeset: originULID must not be empty")
	}
	peers, err := as.reg.List()
	if err != nil {
		return nil, fmt.Errorf("appsync: EmitChangeset: list peers: %w", err)
	}
	return &AppChangeset{
		OriginULID: originULID,
		Entries:    entries,
		PeerCount:  len(peers),
	}, nil
}

// ── Apply / Merge ─────────────────────────────────────────────────────────────

// ApplyChangeset merges a received AppChangeset into the local app_registry
// using LWW (version / updated_at) and OR-set (installed flag) rules.
//
// All rows are upserted atomically inside a single SQLite transaction.
func (as *AppSync) ApplyChangeset(cs *AppChangeset) error {
	if cs == nil {
		return fmt.Errorf("appsync: ApplyChangeset: nil changeset")
	}
	if len(cs.Entries) == 0 {
		return nil
	}

	// Count locally-known instances for quorum evaluation. This is the only
	// peer count we trust — the self-reported cs.PeerCount is ignored for the
	// security decision (CRDT-QUORUM-01).
	peers, err := as.reg.List()
	if err != nil {
		return fmt.Errorf("appsync: ApplyChangeset: list peers: %w", err)
	}
	peerCount := len(peers)

	// The reporting instance for this changeset. Each uninstall contributes at
	// most ONE distinct observation, keyed by this origin. A changeset with no
	// origin id (legacy / direct apply) falls back to the per-entry writer node
	// (InstalledBy) so a forged changeset cannot dodge attribution.
	originULID := cs.OriginULID

	tx, err := as.db.Begin()
	if err != nil {
		return fmt.Errorf("appsync: ApplyChangeset: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, e := range cs.Entries {
		if err = as.mergeEntry(tx, e, peerCount, originULID); err != nil {
			return fmt.Errorf("appsync: ApplyChangeset: merge %s/%s: %w",
				e.InstanceULID, e.AppID, err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("appsync: ApplyChangeset: commit: %w", err)
	}
	log.Printf("[appsync] applied changeset from %s (%d entries)", cs.OriginULID, len(cs.Entries))
	return nil
}

// mergeEntry merges one AppRegistryEntry into the local table using the rules:
//
//  1. If no local row exists → insert unconditionally (but still subject to the
//     uninstall-quorum guard below: a first-seen uninstall that has not reached
//     distinct-origin quorum is recorded as installed=true so a peer cannot seed
//     a removal).
//  2. LWW on updated_at: remote row wins only when it is strictly newer.
//     Tie-break: lexicographically larger writer node id (installed_by) wins —
//     deterministic and symmetric across peers.
//  3. OR-set installed flag: if timestamps are equal, true wins over false.
//  4. Uninstall quorum: an uninstall applies only when the number of DISTINCT
//     originating instances that have reported it meets the threshold derived
//     from the locally-observed peer count (see uninstallQuorumMet). The
//     self-reported PeerCount plays NO part in this decision (CRDT-QUORUM-01).
//
// originULID is the reporting instance (cs.OriginULID); each uninstall it
// carries contributes exactly one observation toward quorum.
func (as *AppSync) mergeEntry(tx *sql.Tx, remote AppRegistryEntry, localPeerCount int, originULID string) error {
	// Record the distinct-origin observation BEFORE deciding quorum so the
	// reporting instance counts toward its own threshold. Recording is a no-op
	// if this origin already reported this uninstall (idempotent OR-set union).
	if !remote.Installed {
		observer := originULID
		if observer == "" {
			// No changeset origin (legacy/direct apply): attribute to the writer
			// node so a forged uninstall cannot dodge attribution entirely.
			observer = remote.InstalledBy
		}
		if err := as.recordUninstallObservation(tx, remote.InstanceULID, remote.AppID, observer, remote.UpdatedAt); err != nil {
			return fmt.Errorf("record uninstall observation: %w", err)
		}
	}

	row := tx.QueryRow(`
		SELECT app_version, installed, installed_by, updated_at
		FROM app_registry
		WHERE instance_ulid = ? AND app_id = ?
	`, remote.InstanceULID, remote.AppID)

	var (
		localVersion     string
		localInstalled   int
		localInstalledBy string
		localUpdatedRaw  string
	)

	err := row.Scan(&localVersion, &localInstalled, &localInstalledBy, &localUpdatedRaw)
	if err == sql.ErrNoRows {
		// No local row. Insert remote — but never let a peer seed an *uninstall*
		// row before distinct-origin quorum is met, otherwise (a) it would block a
		// later install via LWW and (b) it is exactly the unilateral-removal a
		// hostile peer would attempt.
		if !remote.Installed {
			met, qerr := as.uninstallQuorumMet(tx, remote.InstanceULID, remote.AppID, localPeerCount)
			if qerr != nil {
				return qerr
			}
			if !met {
				log.Printf("[appsync] distinct-origin quorum not met for first-seen uninstall of %s/%s — recording installed=true",
					remote.InstanceULID, remote.AppID)
				remote.Installed = true
			}
		}
		return as.upsertEntry(tx, remote)
	}
	if err != nil {
		return fmt.Errorf("scan local row: %w", err)
	}

	// Parse local updated_at.
	var localUpdatedAt time.Time
	if localUpdatedRaw != "" {
		localUpdatedAt, _ = time.Parse(time.RFC3339Nano, localUpdatedRaw)
	}

	// Determine winning row.
	switch {
	case remote.UpdatedAt.After(localUpdatedAt):
		// Remote is strictly newer → accept.
		// Distinct-origin quorum check for uninstalls:
		if !remote.Installed {
			met, qerr := as.uninstallQuorumMet(tx, remote.InstanceULID, remote.AppID, localPeerCount)
			if qerr != nil {
				return qerr
			}
			if !met {
				log.Printf("[appsync] distinct-origin quorum not met for uninstall of %s/%s — retaining installed=true",
					remote.InstanceULID, remote.AppID)
				// Keep installed=true from the local row; still update version if newer.
				remote.Installed = true
			}
		}
		return as.upsertEntry(tx, remote)

	case remote.UpdatedAt.Equal(localUpdatedAt):
		// Tie. An uninstall that has now reached distinct-origin quorum is the
		// corroborated decision and overrides the OR-set "install wins" default:
		// this is how a legitimate multi-origin uninstall converges even when each
		// originating instance stamps the same removal timestamp, and how a
		// previously-suppressed (downgraded) uninstall flips to removed once the
		// Nth distinct origin reports it. A single forged origin can never reach
		// quorum, so it cannot exploit this branch.
		if !remote.Installed {
			met, qerr := as.uninstallQuorumMet(tx, remote.InstanceULID, remote.AppID, localPeerCount)
			if qerr != nil {
				return qerr
			}
			if met {
				e := remote
				e.Installed = false
				// Deterministic writer for the removed row: the larger node id of
				// the two, so all peers converge to identical installed_by.
				if localInstalledBy > e.InstalledBy {
					e.InstalledBy = localInstalledBy
				}
				if localVersion != "" {
					e.AppVersion = localVersion
				}
				return as.upsertEntry(tx, e)
			}
		}
		// OR-set default: install wins over a non-quorum uninstall; deterministic
		// tie-break on the writer node id (installed_by), NOT the row's
		// instance_ulid (which is identical on both sides for this key).
		mergedInstalled := localInstalled == 1 || remote.Installed // OR-set
		if remote.InstalledBy > localInstalledBy {
			// Remote wins tie-break — use remote's data but OR the installed flag.
			e := remote
			e.Installed = mergedInstalled
			return as.upsertEntry(tx, e)
		}
		// Local wins tie-break — update installed flag only if OR changes it.
		if mergedInstalled && localInstalled == 0 {
			return as.upsertEntry(tx, AppRegistryEntry{
				InstanceULID: remote.InstanceULID,
				AppID:        remote.AppID,
				AppVersion:   localVersion,
				Installed:    true,
				InstalledBy:  localInstalledBy,
				UpdatedAt:    localUpdatedAt,
			})
		}
		// Local already wins, nothing to do.
		return nil

	default:
		// Remote is strictly older. Normally discarded — but a strictly-older
		// uninstall observation that pushes the DISTINCT-origin set over quorum
		// must still flip a currently-installed row to removed (the observation
		// set, not the timestamp, is the quorum trigger). Without this, ordering
		// the corroborating observations newest-first would silently lose the
		// removal. A single forged origin can never reach quorum here either.
		if !remote.Installed && localInstalled == 1 {
			met, qerr := as.uninstallQuorumMet(tx, remote.InstanceULID, remote.AppID, localPeerCount)
			if qerr != nil {
				return qerr
			}
			if met {
				return as.upsertEntry(tx, AppRegistryEntry{
					InstanceULID: remote.InstanceULID,
					AppID:        remote.AppID,
					AppVersion:   localVersion,
					Installed:    false,
					InstalledBy:  localInstalledBy,
					UpdatedAt:    localUpdatedAt,
				})
			}
		}
		return nil
	}
}

// recordUninstallObservation records that observerULID has reported the
// uninstall of (instanceULID, appID). It is an idempotent OR-set union: the same
// observer reporting twice is a no-op (PRIMARY KEY conflict), and the observed_at
// is advanced to the most-recent observation for telemetry. This set is the sole
// authority for uninstall quorum — never AppChangeset.PeerCount (CRDT-QUORUM-01).
func (as *AppSync) recordUninstallObservation(tx *sql.Tx, instanceULID, appID, observerULID string, observedAt time.Time) error {
	if observerULID == "" {
		// Cannot attribute the observation to a distinct origin. Refuse to count
		// it (it contributes nothing toward quorum) rather than crediting a blank.
		return nil
	}
	ts := ""
	if !observedAt.IsZero() {
		ts = observedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.Exec(`
		INSERT INTO app_uninstall_observations
			(instance_ulid, app_id, observer_ulid, observed_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(instance_ulid, app_id, observer_ulid) DO UPDATE SET
			observed_at = MAX(observed_at, excluded.observed_at)
	`, instanceULID, appID, observerULID, ts)
	return err
}

// distinctUninstallOrigins counts the DISTINCT originating instances that have
// reported an uninstall of (instanceULID, appID).
func (as *AppSync) distinctUninstallOrigins(tx *sql.Tx, instanceULID, appID string) (int, error) {
	var n int
	err := tx.QueryRow(`
		SELECT COUNT(*) FROM app_uninstall_observations
		WHERE instance_ulid = ? AND app_id = ?
	`, instanceULID, appID).Scan(&n)
	return n, err
}

// uninstallQuorumThreshold returns the number of DISTINCT originating instances
// that must report an uninstall before it is accepted, derived ONLY from the
// locally-observed peer count.
//
//   - ≤ 2 local instances: 1 — a two-node system cannot form a majority, so a
//     single (strictly-newer) uninstall observation suffices (LWW alone).
//   - > 2 local instances: a strict majority of the roster, floored at 2, so a
//     lone origin can never satisfy it. (N=3→2, N=4→3, N=5→3, …)
//
// Because the threshold is computed from the LOCAL registry and the count is the
// number of DISTINCT origins, a single peer cannot reach quorum by inflating any
// self-reported value — it is always exactly one origin.
func uninstallQuorumThreshold(localPeerCount int) int {
	if localPeerCount <= 2 {
		return 1
	}
	majority := localPeerCount/2 + 1
	if majority < 2 {
		majority = 2
	}
	return majority
}

// uninstallQuorumMet reports whether enough DISTINCT origins have reported the
// uninstall of (instanceULID, appID) to meet the locally-derived threshold.
func (as *AppSync) uninstallQuorumMet(tx *sql.Tx, instanceULID, appID string, localPeerCount int) (bool, error) {
	n, err := as.distinctUninstallOrigins(tx, instanceULID, appID)
	if err != nil {
		return false, fmt.Errorf("count distinct uninstall origins: %w", err)
	}
	return n >= uninstallQuorumThreshold(localPeerCount), nil
}

// upsertEntry writes an AppRegistryEntry into the database (insert or replace).
func (as *AppSync) upsertEntry(tx *sql.Tx, e AppRegistryEntry) error {
	installed := 0
	if e.Installed {
		installed = 1
	}
	updatedAt := ""
	if !e.UpdatedAt.IsZero() {
		updatedAt = e.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}

	_, err := tx.Exec(`
		INSERT INTO app_registry
			(instance_ulid, app_id, app_version, installed, installed_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_ulid, app_id) DO UPDATE SET
			app_version  = excluded.app_version,
			installed    = excluded.installed,
			installed_by = excluded.installed_by,
			updated_at   = excluded.updated_at
	`, e.InstanceULID, e.AppID, e.AppVersion, installed, e.InstalledBy, updatedAt)
	return err
}

// PeerInstanceIDs returns the ULIDs of every instance in the registry roster,
// optionally excluding self. It is the roster source the fabric mDNS discoverer
// uses to build per-instance qualified query names (InstanceFabricName) so a
// >2-box LAN resolves every known peer individually rather than only the single
// responder the shared mDNS name yields. Returns nil on a read error (the
// discoverer then falls back to the shared name only).
func (as *AppSync) PeerInstanceIDs(excludeSelfULID string) []string {
	insts, err := as.reg.List()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(insts))
	for _, in := range insts {
		if in.ULID == "" || in.ULID == excludeSelfULID {
			continue
		}
		out = append(out, in.ULID)
	}
	return out
}

// ── Read helpers ──────────────────────────────────────────────────────────────

// ListAppsForInstance returns all app_registry rows for the given instance ULID.
// Only rows with installed=1 are returned by default; pass includeRemoved=true
// to also include uninstalled entries.
func (as *AppSync) ListAppsForInstance(instanceULID string, includeRemoved bool) ([]AppRegistryEntry, error) {
	query := `
		SELECT instance_ulid, app_id, app_version, installed, installed_by, updated_at
		FROM app_registry
		WHERE instance_ulid = ?`
	if !includeRemoved {
		query += " AND installed = 1"
	}
	query += " ORDER BY app_id"

	rows, err := as.db.Query(query, instanceULID)
	if err != nil {
		return nil, fmt.Errorf("appsync: ListAppsForInstance: %w", err)
	}
	defer rows.Close()

	return scanEntries(rows)
}

// ListAllApps returns every row in app_registry across all instances.
func (as *AppSync) ListAllApps() ([]AppRegistryEntry, error) {
	rows, err := as.db.Query(`
		SELECT instance_ulid, app_id, app_version, installed, installed_by, updated_at
		FROM app_registry
		ORDER BY instance_ulid, app_id
	`)
	if err != nil {
		return nil, fmt.Errorf("appsync: ListAllApps: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ChangesetSince returns every app_registry row whose updated_at is strictly
// greater than the provided cursor, ordered by (updated_at, instance_ulid,
// app_id) so a caller can advance its cursor to the max returned UpdatedAt.
//
// This is the read-side primitive used by the fabric P2P sync transport
// (GET /api/fabric/changeset?since=<cursor>): a peer asks "what changed since I
// last saw you?" and gets back exactly the rows that postdate its cursor,
// including uninstalled (installed=0) rows so OR-set/LWW removals propagate.
//
// A zero `since` returns the full table (a cold-start full sync). The returned
// rows carry the writer node id (installed_by) and updated_at so the receiver's
// ApplyChangeset can run the deterministic LWW merge unchanged.
func (as *AppSync) ChangesetSince(since time.Time) ([]AppRegistryEntry, error) {
	// We compare on the RFC3339Nano text form because that is how updated_at is
	// stored; lexicographic ordering of RFC3339Nano UTC strings matches temporal
	// ordering. Using the text comparison (rather than a parsed-time WHERE) keeps
	// the query index-friendly and avoids per-row parse cost in SQLite.
	var rows *sql.Rows
	var err error
	if since.IsZero() {
		rows, err = as.db.Query(`
			SELECT instance_ulid, app_id, app_version, installed, installed_by, updated_at
			FROM app_registry
			ORDER BY updated_at, instance_ulid, app_id`)
	} else {
		cursor := since.UTC().Format(time.RFC3339Nano)
		rows, err = as.db.Query(`
			SELECT instance_ulid, app_id, app_version, installed, installed_by, updated_at
			FROM app_registry
			WHERE updated_at > ?
			ORDER BY updated_at, instance_ulid, app_id`, cursor)
	}
	if err != nil {
		return nil, fmt.Errorf("appsync: ChangesetSince: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// scanEntries scans *sql.Rows into a slice of AppRegistryEntry.
func scanEntries(rows *sql.Rows) ([]AppRegistryEntry, error) {
	var out []AppRegistryEntry
	for rows.Next() {
		var (
			e          AppRegistryEntry
			installed  int
			updatedRaw string
		)
		if err := rows.Scan(
			&e.InstanceULID, &e.AppID, &e.AppVersion,
			&installed, &e.InstalledBy, &updatedRaw,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		e.Installed = installed == 1
		if updatedRaw != "" {
			e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── Wire format helpers ───────────────────────────────────────────────────────

// MarshalChangeset serialises an AppChangeset to JSON bytes.
func MarshalChangeset(cs *AppChangeset) ([]byte, error) {
	return json.Marshal(cs)
}

// UnmarshalChangeset parses JSON bytes into an AppChangeset.
func UnmarshalChangeset(data []byte) (*AppChangeset, error) {
	var cs AppChangeset
	if err := json.Unmarshal(data, &cs); err != nil {
		return nil, fmt.Errorf("appsync: unmarshal: %w", err)
	}
	return &cs, nil
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

// RegisterAppSyncHandlers wires the MINST-04 HTTP endpoints into mux.
//
//	GET /api/instances/{ulid}/apps   — per-instance app inventory (installed only)
//
// The {ulid} path segment is extracted from the URL; an empty or missing
// segment returns 400.
//
// Usage (from a routes_*.go in cmd/server — never from main.go):
//
//	multiinstance.RegisterAppSyncHandlers(mux, appSync)
func RegisterAppSyncHandlers(mux *http.ServeMux, as *AppSync) {
	// Go 1.22+ pattern: "GET /api/instances/{ulid}/apps"
	mux.HandleFunc("GET /api/instances/{ulid}/apps", func(w http.ResponseWriter, r *http.Request) {
		ulid := r.PathValue("ulid")
		if ulid == "" {
			// Fallback: parse from path manually for older Go versions.
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/instances/"), "/")
			if len(parts) >= 2 {
				ulid = parts[0]
			}
		}
		if ulid == "" {
			http.Error(w, `{"error":"missing instance ulid"}`, http.StatusBadRequest)
			return
		}

		apps, err := as.ListAppsForInstance(ulid, false)
		if err != nil {
			log.Printf("[appsync] ListAppsForInstance %s: %v", ulid, err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if apps == nil {
			apps = []AppRegistryEntry{}
		}

		w.Header().Set("Content-Type", "application/json")
		if encErr := json.NewEncoder(w).Encode(apps); encErr != nil {
			return
		}
	})
}
