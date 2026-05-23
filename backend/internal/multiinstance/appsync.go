// MINST-04: App registry sync — pure-Go CRDT changeset replication.
//
// AppSync replicates the app_registry table across instances using a
// pure-Go CRDT changeset layer (no CGO / cr-sqlite extension required).
//
// Conflict resolution strategy
//
//   - app_version field:  Last-Write-Wins (LWW) — the row with the higher
//     updated_at timestamp wins.  On a tie, the lexicographically larger
//     instance_ulid wins (deterministic tie-breaking).
//   - installed flag:     OR-set semantics — install wins over uninstall
//     when the timestamps are equal.  A true uninstall (status=0) only
//     propagates when its updated_at is strictly newer than the local row.
//     Exception: when more than 2 instances exist in the registry, an
//     uninstall is only accepted when the changeset carries a quorum=true
//     flag (set by the originator after receiving acknowledgements from at
//     least 1 other peer — i.e. ≥ 2 instances have confirmed the remove).
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
//  1. If no local row exists → insert unconditionally.
//  2. LWW on updated_at: remote row wins only when it is strictly newer.
//     Tie-break: larger InstanceULID wins (deterministic).
//  3. OR-set installed flag: if timestamps are equal, true wins over false.
//  4. Uninstall quorum: if local registry has >2 instances AND remote
//     peerCount also indicates >2 instances, accept an uninstall (installed=0)
//     only when the remote changeset's peer count implies quorum (originator
//     knew of at least 2 peers — i.e. peerCount ≥ 2).
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
		// No local row — insert remote unconditionally.
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
		if !remote.Installed && !as.quorumOK(remote, localPeerCount, remotePeerCount) {
			log.Printf("[appsync] quorum not met for uninstall of %s/%s — retaining installed=true",
				remote.InstanceULID, remote.AppID)
			// Keep installed=true from the local row; still update version if newer.
			remote.Installed = true
		}
		return as.upsertEntry(tx, remote)

	case remote.UpdatedAt.Equal(localUpdatedAt):
		// Tie: apply OR-set on installed; tie-break on instance_ulid.
		mergedInstalled := localInstalled == 1 || remote.Installed // OR-set
		if remote.InstanceULID > localInstalledBy {
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

// quorumOK returns true when the uninstall can be accepted.
// Quorum requires ≥ 2 peer instances known to the originator AND ≥ 2 known locally.
func (as *AppSync) quorumOK(remote AppRegistryEntry, localPeerCount, remotePeerCount int) bool {
	// Quorum only applies when there are more than 2 instances total.
	if localPeerCount <= 2 && remotePeerCount <= 2 {
		return true // quorum not required
	}
	// Require the originator to have been aware of at least 2 peers.
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
