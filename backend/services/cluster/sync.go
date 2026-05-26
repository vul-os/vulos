// Package cluster — CLUSTER-05: cr-sqlite changeset push/pull sync loop.
//
// SyncLoop periodically:
//  1. Pushes: SELECT crsql_changes WHERE db_version > last_pushed → serialize →
//     upload to nodes/{node_id}/changes/{db_version}.bin (SSE-C encrypted).
//  2. Pulls: list nodes/{peer_id}/changes/ for each known peer, download
//     changesets newer than the per-peer cursor, apply via INSERT INTO
//     crsql_changes.
//
// Cursors (last_pushed db_version, per-peer last_pulled db_version) are
// persisted in a local _sync_cursors table so sync can resume after restart.
//
// Usage:
//
//	sl, err := cluster.NewSyncLoop(ctx, cluster, db)
//	if err != nil { ... }
//	go sl.Run(ctx)
package cluster

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
)

// ── Constants ────────────────────────────────────────────────────────────────

const (
	// changesPrefix is the S3 prefix for changeset objects.
	// Full key: nodes/{node_id}/changes/{db_version}.bin
	changesPrefix = "nodes/"

	// defaultSyncInterval is the fallback when VULOS_SYNC_INTERVAL is unset.
	defaultSyncInterval = 5 * time.Second

	// cursorSelf is the _sync_cursors key for this node's push cursor.
	cursorSelf = "__self__"

	// changesetMagic is a 4-byte magic header identifying a vulos changeset.
	// Allows future format versioning.
	changesetMagic = "VCST" // Vulos ChangeSeT
)

// ── crsql_changes row ────────────────────────────────────────────────────────

// changeRow mirrors one row from the crsql_changes virtual table.
// Columns: table, pk, cid, val, col_version, db_version, site_id, cl, seq
type changeRow struct {
	Table      string  `json:"t"`
	PK         string  `json:"pk"`
	CID        string  `json:"cid"`
	Val        *string `json:"v"` // NULL-able
	ColVersion int64   `json:"cv"`
	DBVersion  int64   `json:"dv"`
	SiteID     []byte  `json:"sid"`
	CL         int64   `json:"cl"`
	Seq        int64   `json:"seq"`
}

// ── wire format ──────────────────────────────────────────────────────────────

// changeset is the JSON-serialised bundle uploaded to S3.
type changeset struct {
	NodeID    string      `json:"node_id"`
	DBVersion int64       `json:"db_version"`
	Changes   []changeRow `json:"changes"`
}

// serialiseChangeset encodes a changeset as:
//
//	[4 bytes magic][8 bytes length][JSON payload]
func serialiseChangeset(cs *changeset) ([]byte, error) {
	payload, err := json.Marshal(cs)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 4+8+len(payload))
	copy(buf[:4], changesetMagic)
	binary.BigEndian.PutUint64(buf[4:12], uint64(len(payload)))
	copy(buf[12:], payload)
	return buf, nil
}

// deserialiseChangeset parses a wire-format changeset.
func deserialiseChangeset(data []byte) (*changeset, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("sync: changeset too short (%d bytes)", len(data))
	}
	if string(data[:4]) != changesetMagic {
		return nil, fmt.Errorf("sync: invalid changeset magic %q", data[:4])
	}
	payloadLen := binary.BigEndian.Uint64(data[4:12])
	if int(payloadLen) != len(data)-12 {
		return nil, fmt.Errorf("sync: length mismatch: header=%d actual=%d", payloadLen, len(data)-12)
	}
	var cs changeset
	if err := json.Unmarshal(data[12:], &cs); err != nil {
		return nil, fmt.Errorf("sync: unmarshal: %w", err)
	}
	return &cs, nil
}

// ── cursor persistence ────────────────────────────────────────────────────────

// ensureCursorTable creates _sync_cursors if absent.
// Column `node_id` is "self" for the push cursor, or a peer node ID for pull.
func ensureCursorTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS _sync_cursors (
		node_id    TEXT PRIMARY KEY,
		db_version INTEGER NOT NULL DEFAULT 0
	)`)
	return err
}

// readCursor returns the persisted db_version cursor for nodeID (0 if none).
func readCursor(db *sql.DB, nodeID string) (int64, error) {
	var v int64
	err := db.QueryRow(
		`SELECT db_version FROM _sync_cursors WHERE node_id = ?`, nodeID,
	).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return v, err
}

// writeCursor upserts the db_version cursor for nodeID.
func writeCursor(db *sql.DB, nodeID string, v int64) error {
	_, err := db.Exec(
		`INSERT INTO _sync_cursors (node_id, db_version) VALUES (?, ?)
		 ON CONFLICT(node_id) DO UPDATE SET db_version = excluded.db_version`,
		nodeID, v,
	)
	return err
}

// ── SyncLoop ─────────────────────────────────────────────────────────────────

// SyncLoop drives the cr-sqlite changeset push/pull cycle.
// Construct with NewSyncLoop; start with go sl.Run(ctx).
type SyncLoop struct {
	cluster  *Cluster
	db       *sql.DB
	interval time.Duration

	mu sync.Mutex // guards lastPushed and peerCursors during non-loop access
}

// NewSyncLoop creates a SyncLoop.
//   - cluster must be non-nil (use cluster.New or cluster.newDisabled).
//   - db is the *sql.DB from store.Open (may or may not have crsql loaded).
//   - If VULOS_SYNC_INTERVAL is set (integer seconds) it overrides the default.
//
// NewSyncLoop ensures the _sync_cursors table exists.
func NewSyncLoop(ctx context.Context, c *Cluster, db *sql.DB) (*SyncLoop, error) {
	if err := ensureCursorTable(db); err != nil {
		return nil, fmt.Errorf("sync: create cursor table: %w", err)
	}

	interval := defaultSyncInterval
	if s := os.Getenv("VULOS_SYNC_INTERVAL"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			interval = time.Duration(secs) * time.Second
		}
	}

	return &SyncLoop{
		cluster:  c,
		db:       db,
		interval: interval,
	}, nil
}

// Run starts the sync loop.  It blocks until ctx is cancelled.
// If the cluster is disabled (no S3), Run returns immediately.
//
//	go sl.Run(ctx)
func (sl *SyncLoop) Run(ctx context.Context) {
	if !sl.cluster.Enabled() {
		return
	}

	log.Printf("sync: starting (interval=%s, node=%s)", sl.interval, sl.cluster.meta.ID)

	// Run once immediately, then on every tick.
	sl.runOnce(ctx)

	ticker := time.NewTicker(sl.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("sync: stopping (ctx cancelled)")
			return
		case <-ticker.C:
			sl.runOnce(ctx)
		}
	}
}

// runOnce executes one push+pull cycle.
func (sl *SyncLoop) runOnce(ctx context.Context) {
	if err := sl.push(ctx); err != nil {
		log.Printf("sync: push error: %v", err)
	}
	if err := sl.pull(ctx); err != nil {
		log.Printf("sync: pull error: %v", err)
	}
}

// ── push ─────────────────────────────────────────────────────────────────────

// push reads new crsql_changes beyond last_pushed, serialises them, and
// uploads to nodes/{node_id}/changes/{db_version}.bin.
func (sl *SyncLoop) push(ctx context.Context) error {
	lastPushed, err := readCursor(sl.db, cursorSelf)
	if err != nil {
		return fmt.Errorf("push: read cursor: %w", err)
	}

	rows, err := sl.db.QueryContext(ctx, `
		SELECT "table", pk, cid, val, col_version, db_version, site_id, cl, seq
		FROM crsql_changes
		WHERE db_version > ?
		ORDER BY db_version ASC, seq ASC
	`, lastPushed)
	if err != nil {
		// crsql_changes doesn't exist when cr-sqlite isn't loaded — silently skip.
		if isMissingTable(err) {
			return nil
		}
		return fmt.Errorf("push: query crsql_changes: %w", err)
	}
	defer rows.Close()

	var changes []changeRow
	var maxVersion int64 = lastPushed

	for rows.Next() {
		var r changeRow
		if err := rows.Scan(
			&r.Table, &r.PK, &r.CID, &r.Val,
			&r.ColVersion, &r.DBVersion, &r.SiteID, &r.CL, &r.Seq,
		); err != nil {
			return fmt.Errorf("push: scan: %w", err)
		}
		changes = append(changes, r)
		if r.DBVersion > maxVersion {
			maxVersion = r.DBVersion
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("push: rows: %w", err)
	}

	if len(changes) == 0 {
		return nil // nothing to push
	}

	cs := &changeset{
		NodeID:    sl.cluster.meta.ID,
		DBVersion: maxVersion,
		Changes:   changes,
	}

	data, err := serialiseChangeset(cs)
	if err != nil {
		return fmt.Errorf("push: serialise: %w", err)
	}

	key := fmt.Sprintf("nodes/%s/changes/%d.bin", sl.cluster.meta.ID, maxVersion)
	if err := sl.cluster.client.PutEncrypted(ctx, key, data); err != nil {
		return fmt.Errorf("push: upload: %w", err)
	}

	if err := writeCursor(sl.db, cursorSelf, maxVersion); err != nil {
		return fmt.Errorf("push: write cursor: %w", err)
	}

	log.Printf("sync: pushed %d changes (db_version=%d) → %s", len(changes), maxVersion, key)
	return nil
}

// ── pull ─────────────────────────────────────────────────────────────────────

// pull discovers peer change objects from S3 and applies any that are newer
// than the per-peer cursor stored in _sync_cursors.
func (sl *SyncLoop) pull(ctx context.Context) error {
	// List all objects under nodes/ to find peer change directories.
	opts := minio.ListObjectsOptions{
		Prefix:    changesPrefix,
		Recursive: true,
	}
	objectCh := sl.cluster.client.mc.ListObjects(ctx, sl.cluster.client.bucket, opts)

	// Collect keys of change objects (*.bin), grouped by peer node.
	// Skip our own changes.
	type peerChange struct {
		key       string
		dbVersion int64
		peerID    string
	}

	var all []peerChange
	for obj := range objectCh {
		if obj.Err != nil {
			return fmt.Errorf("pull: list: %w", obj.Err)
		}
		key := obj.Key
		// Expected pattern: nodes/{peer_id}/changes/{version}.bin
		if !strings.HasSuffix(key, ".bin") {
			continue
		}
		parts := strings.SplitN(key, "/", 4) // ["nodes", peer_id, "changes", "ver.bin"]
		if len(parts) != 4 || parts[0] != "nodes" || parts[2] != "changes" {
			continue
		}
		peerID := parts[1]
		if peerID == sl.cluster.meta.ID {
			continue // skip self
		}
		versionStr := strings.TrimSuffix(parts[3], ".bin")
		version, err := strconv.ParseInt(versionStr, 10, 64)
		if err != nil {
			continue // ignore malformed keys
		}
		all = append(all, peerChange{key: key, dbVersion: version, peerID: peerID})
	}

	if len(all) == 0 {
		return nil
	}

	// Group by peer, sort by db_version ascending.
	byPeer := make(map[string][]peerChange)
	for _, pc := range all {
		byPeer[pc.peerID] = append(byPeer[pc.peerID], pc)
	}
	for pid := range byPeer {
		sort.Slice(byPeer[pid], func(i, j int) bool {
			return byPeer[pid][i].dbVersion < byPeer[pid][j].dbVersion
		})
	}

	for peerID, pcs := range byPeer {
		cursor, err := readCursor(sl.db, peerID)
		if err != nil {
			log.Printf("sync: pull: read cursor for %s: %v", peerID, err)
			continue
		}

		for _, pc := range pcs {
			if pc.dbVersion <= cursor {
				continue // already applied
			}
			if err := sl.applyPeerChangeset(ctx, pc.key, peerID, pc.dbVersion); err != nil {
				log.Printf("sync: pull: apply %s: %v", pc.key, err)
				// Continue attempting later changesets; cursor only advances on success.
				break
			}
			cursor = pc.dbVersion
		}
	}

	return nil
}

// applyPeerChangeset downloads, deserialises, and inserts one peer changeset.
func (sl *SyncLoop) applyPeerChangeset(ctx context.Context, key, peerID string, dbVersion int64) error {
	data, err := sl.cluster.client.GetEncrypted(ctx, key)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}

	cs, err := deserialiseChangeset(data)
	if err != nil {
		return fmt.Errorf("deserialise: %w", err)
	}

	if err := sl.insertChanges(ctx, cs.Changes); err != nil {
		return fmt.Errorf("insert: %w", err)
	}

	if err := writeCursor(sl.db, peerID, dbVersion); err != nil {
		return fmt.Errorf("write cursor: %w", err)
	}

	log.Printf("sync: applied %d changes from %s (db_version=%d)", len(cs.Changes), peerID, dbVersion)
	return nil
}

// insertChanges inserts change rows via INSERT INTO crsql_changes.
// cr-sqlite merges automatically using CRDT semantics.
func (sl *SyncLoop) insertChanges(ctx context.Context, changes []changeRow) error {
	if len(changes) == 0 {
		return nil
	}

	tx, err := sl.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO crsql_changes
			("table", pk, cid, val, col_version, db_version, site_id, cl, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range changes {
		if _, err = stmt.ExecContext(ctx,
			r.Table, r.PK, r.CID, r.Val,
			r.ColVersion, r.DBVersion, r.SiteID, r.CL, r.Seq,
		); err != nil {
			// Duplicate / already-applied changes are normal — ignore them.
			if isIgnorableInsertError(err) {
				continue
			}
			return fmt.Errorf("insert change (table=%s pk=%s): %w", r.Table, r.PK, err)
		}
	}

	err = tx.Commit()
	return err
}

// ── helpers ───────────────────────────────────────────────────────────────────

// isMissingTable returns true when SQLite says the table doesn't exist.
// This happens when cr-sqlite is not loaded and crsql_changes is unavailable.
func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "no such table") ||
		strings.Contains(s, "no such module")
}

// isIgnorableInsertError returns true for errors that should not abort the
// current batch (e.g. UNIQUE constraint violations for already-applied rows).
func isIgnorableInsertError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint") ||
		strings.Contains(s, "PRIMARY KEY constraint")
}

// ── S3 listing helper exposed on Client ──────────────────────────────────────

// ListPrefix returns S3 object keys that begin with prefix.
// It is a thin wrapper over minio.Client.ListObjects kept in the same package
// so external callers (e.g. future packages) can list without needing the
// raw minio client.
func (c *Client) ListPrefix(ctx context.Context, prefix string) ([]string, error) {
	opts := minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}
	objectCh := c.mc.ListObjects(ctx, c.bucket, opts)

	var keys []string
	for obj := range objectCh {
		if obj.Err != nil {
			return nil, obj.Err
		}
		keys = append(keys, obj.Key)
	}
	return keys, nil
}

// DeleteObject removes a single object from the cluster bucket. It mirrors the
// SnapshotS3 abstraction in services/sync so *Client can drive the Compactor's
// changeset pruning. Deleting an absent key is not treated as an error by the
// minio client.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	return c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{})
}

// ── readAll helper (unexported, avoids import cycle with io package) ──────────

// readAll is a local alias so sync.go is self-contained.
var readAll = io.ReadAll

// ── ensure unused import doesn't break build ──────────────────────────────────
var _ = bytes.Compare
