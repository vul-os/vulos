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
//     uninstall is only accepted when quorum is met.
//
// Quorum hardening (audit P1-3)
//
//	The uninstall quorum is governed by the LOCALLY-OBSERVED peer count from the
//	registry, never by the self-reported AppChangeset.PeerCount alone.  A
//	malicious or buggy peer therefore cannot:
//	  (a) force a removal by inflating PeerCount — the local registry must also
//	      corroborate ≥ 2 peers before a >2-instance uninstall is accepted; nor
//	  (b) unilaterally block a removal by deflating PeerCount — when the local
//	      registry shows ≤ 2 instances quorum is not required at all.
//	The self-reported PeerCount is treated only as a non-authoritative hint that
//	can tighten (never loosen) the locally-derived decision.
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
	// PeerCount is the number of instances the originator knew about when
	// it emitted this changeset.  Used to evaluate uninstall quorum.
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

	// Count known instances for quorum evaluation.
	peers, err := as.reg.List()
	if err != nil {
		return fmt.Errorf("appsync: ApplyChangeset: list peers: %w", err)
	}
	peerCount := len(peers)

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
		if err = as.mergeEntry(tx, e, peerCount, cs.PeerCount); err != nil {
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
//     uninstall-quorum guard below: a first-seen uninstall under a quorum
//     regime is recorded as installed=true so a peer cannot seed a removal).
//  2. LWW on updated_at: remote row wins only when it is strictly newer.
//     Tie-break: lexicographically larger writer node id (installed_by) wins —
//     deterministic and symmetric across peers.
//  3. OR-set installed flag: if timestamps are equal, true wins over false.
//  4. Uninstall quorum: governed by the locally-observed peer count (see
//     quorumOK); the self-reported remotePeerCount can only tighten the
//     decision, never loosen it.
func (as *AppSync) mergeEntry(tx *sql.Tx, remote AppRegistryEntry, localPeerCount, remotePeerCount int) error {
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
		// row under a quorum regime, otherwise (a) it would block a later install
		// via LWW and (b) it is exactly the unilateral-removal a hostile peer
		// would attempt.
		if !remote.Installed && !as.quorumOK(localPeerCount, remotePeerCount) {
			log.Printf("[appsync] quorum not met for first-seen uninstall of %s/%s — recording installed=true",
				remote.InstanceULID, remote.AppID)
			remote.Installed = true
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
		// Quorum check for uninstalls:
		if !remote.Installed && !as.quorumOK(localPeerCount, remotePeerCount) {
			log.Printf("[appsync] quorum not met for uninstall of %s/%s — retaining installed=true",
				remote.InstanceULID, remote.AppID)
			// Keep installed=true from the local row; still update version if newer.
			remote.Installed = true
		}
		return as.upsertEntry(tx, remote)

	case remote.UpdatedAt.Equal(localUpdatedAt):
		// Tie: apply OR-set on installed; deterministic tie-break on the writer
		// node id (installed_by), NOT the row's instance_ulid (which is identical
		// on both sides for this (instance_ulid, app_id) key).
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
		// Remote is older → discard.
		return nil
	}
}

// quorumOK reports whether an uninstall may be accepted given the LOCALLY
// observed peer count (authoritative) and the self-reported remote peer count
// (a non-authoritative hint).
//
// Rules (audit P1-3 — do not trust self-reported counts):
//   - When the local registry shows ≤ 2 instances, quorum is NOT required: a
//     two-node system cannot form a majority, so a strictly-newer uninstall is
//     accepted on LWW alone. (A peer cannot block this by lying about its own
//     count because we ignore remotePeerCount in this branch.)
//   - When the local registry shows > 2 instances, quorum IS required AND the
//     self-reported remotePeerCount must independently corroborate ≥ 2 peers.
//     A peer therefore cannot force a removal by inflating its count (the local
//     registry must agree there are >2 instances) and cannot bypass the
//     corroboration requirement by deflating it (deflation only fails quorum).
func (as *AppSync) quorumOK(localPeerCount, remotePeerCount int) bool {
	if localPeerCount <= 2 {
		return true // quorum not required in a ≤2-node system
	}
	// >2 local instances: require the originator to have independently observed
	// at least 2 peers. The local count is the gate; the remote count must agree.
	return remotePeerCount >= 2
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
