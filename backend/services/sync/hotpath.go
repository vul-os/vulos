// Package sync — SYNC-01: instance↔instance changeset hot path over peering mesh.
//
// HotPath streams crsql_changes directly between live peer instances using the
// existing peering mesh transport (PeerClient / InboundMiddleware). When a
// direct connection is blocked by NAT or cross-location routing the hot path
// falls back to the relay — the same transport peering already uses for
// messages.
//
// Architecture
//
//   - Outbound: HotPath.Push sends a signed Envelope of type TypeChangeset to
//     each known peer. The envelope payload is a wire-format changeset
//     (serialiseChangeset), the same binary encoding used by CLUSTER-05 (S3
//     cold path). Delivery uses PeerClient (direct HTTP) with a fall-through to
//     relay deposit on SSRF / network errors.
//   - Inbound:  HandleInboundChangeset receives a TypeChangeset envelope,
//     verifies the signature (InboundMiddleware already did this, but this
//     handler validates the payload type too), deserialises the changeset, and
//     inserts it via the cluster's insertChanges path — the same CRDT merge that
//     the cold path uses.
//   - Cold path: completely unaffected. SyncLoop (cluster/sync.go) continues
//     running its periodic S3 push/pull cycle. Hot path is additive.
//
// Per-peer send cursors
//
//	HotPath maintains its own in-memory per-peer "last sent" db_version map so
//	that each Push only streams rows the peer has not yet seen. These cursors
//	are intentionally NOT persisted — the hot path is transient. On restart the
//	cursor resets to 0 and the cold path (or relay pickup) fills any gap before
//	the mesh catches up.
//
// Relay fallback
//
//	When PeerClient.Post returns any error (SSRF guard, network error, non-2xx)
//	the outbound changeset is deposited on the relay as an encrypted blob. The
//	receiving peer's periodic HotPath.Pickup call collects pending blobs and
//	applies them. This mirrors exactly how message delivery handles NAT/offline
//	peers via OutboxQueue, but is self-contained here so the sync package does
//	not depend on the message pipeline.
//
// Usage
//
//	hp, err := sync.NewHotPath(sync.HotPathConfig{...})
//	if err != nil { ... }
//	go hp.Run(ctx)                            // periodic push loop
//	mux.HandleFunc("POST /api/peering/inbound/changeset", hp.HandleInboundChangeset)
package sync

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ── envelope type constant ─────────────────────────────────────────────────────

// TypeChangeset is the peering Envelope.Type value for hot-path changesets.
// Registered alongside TypeMessage / TypeSignaling etc.
const TypeChangeset = "changeset"

// ── crsql_changes types (mirrored from cluster/sync.go) ─────────────────────
//
// These types and helpers are intentionally re-declared here rather than
// exported from the cluster package, to avoid an import cycle
// (sync → cluster → … → sync is possible as the cluster package references
// the sync package's SnapshotS3 interface).  The definitions are kept
// byte-for-byte compatible with cluster.changeRow and cluster.changeset so
// the same binary wire format (changesetMagic + length + JSON) is shared.

// hpChangeRow mirrors one row from the crsql_changes virtual table.
type hpChangeRow struct {
	Table      string  `json:"t"`
	PK         string  `json:"pk"`
	CID        string  `json:"cid"`
	Val        *string `json:"v"`
	ColVersion int64   `json:"cv"`
	DBVersion  int64   `json:"dv"`
	SiteID     []byte  `json:"sid"`
	CL         int64   `json:"cl"`
	Seq        int64   `json:"seq"`
}

// hpChangeset is the JSON-serialised changeset bundle sent over the mesh.
type hpChangeset struct {
	NodeID    string        `json:"node_id"`
	DBVersion int64         `json:"db_version"`
	Changes   []hpChangeRow `json:"changes"`
}

// hpMagic is the 4-byte magic prefix for the changeset wire format.
// Must equal cluster.changesetMagic ("VCST") so the two packages can exchange
// changeset blobs without re-encoding.
const hpMagic = "VCST"

// serialiseHPChangeset encodes cs as: [4 bytes magic][8 bytes length][JSON payload].
func serialiseHPChangeset(cs *hpChangeset) ([]byte, error) {
	payload, err := json.Marshal(cs)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 4+8+len(payload))
	copy(buf[:4], hpMagic)
	binary.BigEndian.PutUint64(buf[4:12], uint64(len(payload)))
	copy(buf[12:], payload)
	return buf, nil
}

// deserialiseHPChangeset parses a wire-format changeset.
func deserialiseHPChangeset(data []byte) (*hpChangeset, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("hotpath: changeset too short (%d bytes)", len(data))
	}
	if string(data[:4]) != hpMagic {
		return nil, fmt.Errorf("hotpath: invalid changeset magic %q", data[:4])
	}
	payloadLen := binary.BigEndian.Uint64(data[4:12])
	if int(payloadLen) != len(data)-12 {
		return nil, fmt.Errorf("hotpath: length mismatch: header=%d actual=%d", payloadLen, len(data)-12)
	}
	var cs hpChangeset
	if err := json.Unmarshal(data[12:], &cs); err != nil {
		return nil, fmt.Errorf("hotpath: unmarshal: %w", err)
	}
	return &cs, nil
}

// hpIsMissingTable mirrors cluster.isMissingTable.
func hpIsMissingTable(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "no such table") || strings.Contains(s, "no such module")
}

// hpIsIgnorableInsertError mirrors cluster.isIgnorableInsertError.
func hpIsIgnorableInsertError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint") || strings.Contains(s, "PRIMARY KEY constraint")
}

// ── wire interfaces (keeps sync package free of concrete peering import) ──────

// HotPathPeerClient is the subset of *peering.PeerClient used by HotPath.
// The peering package's PeerClient satisfies this interface out of the box.
type HotPathPeerClient interface {
	// Post signs and sends env to baseURL/api/peering/inbound/<envelopeType>.
	Post(ctx context.Context, baseURL, envelopeType string, env HotPathEnvelope) error
}

// HotPathRelayClient is the minimal relay interface used for fallback.
// Implementations typically wrap peering.RelayStore or call the relay HTTP API.
type HotPathRelayClient interface {
	// Deposit stores blob for recipient (identified by recipientVulaID) on the
	// relay. blobID must be unique per deposit (use changesetBlobID).
	Deposit(ctx context.Context, recipientVulaID, blobID string, blob []byte) error
	// Pickup fetches and removes all pending blobs deposited for localVulaID.
	Pickup(ctx context.Context, localVulaID string) ([][]byte, error)
}

// HotPathEnvelope is the signed envelope wrapper sent over the mesh.
// It is a subset of peering.Envelope without introducing an import cycle.
type HotPathEnvelope struct {
	ID        string          `json:"id"`
	From      string          `json:"from"`
	To        string          `json:"to,omitempty"`
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature,omitempty"`
}

// ── PeerInfo ──────────────────────────────────────────────────────────────────

// PeerInfo describes a live peer that the hot path can push to.
type PeerInfo struct {
	// NodeID is the peer's cluster node ID (used for per-peer cursor tracking).
	NodeID string
	// VulaID is the peer's Vula ID ("vula:ed25519:<base58>").  Used as the
	// envelope "to" field and as the relay recipient identifier.
	VulaID string
	// ServerURL is the peer's base HTTP URL, e.g. "https://peer.vulos.org:8080".
	// Empty means relay-only delivery.
	ServerURL string
}

// ── HotPathConfig ─────────────────────────────────────────────────────────────

// HotPathConfig holds the constructor parameters for HotPath.
type HotPathConfig struct {
	// NodeID is this instance's cluster node ID (used as the changeset sender ID
	// and as the push cursor "self" key).
	NodeID string

	// VulaID is this node's canonical Vula ID ("vula:ed25519:<base58>").
	// Used to sign outbound envelopes.
	VulaID string

	// PrivKey is the Ed25519 private key used to sign outbound envelopes.
	PrivKey ed25519.PrivateKey

	// DB is the *sql.DB from store.Open (must have cr-sqlite loaded for
	// crsql_changes queries; if not loaded, pushes are silently skipped).
	DB *sql.DB

	// Peers is a function that returns the current set of live peers at push
	// time. The slice may change between calls (new peers join, peers leave).
	// Must be non-nil.
	Peers func() []PeerInfo

	// Client is the outbound HTTP client for direct peer delivery.
	// Must be non-nil.
	Client HotPathPeerClient

	// Relay is the relay client for NAT/offline fallback.
	// May be nil — relay fallback is disabled when nil.
	Relay HotPathRelayClient

	// PushInterval is how often the hot-path push loop runs.
	// Defaults to 500 ms.
	PushInterval time.Duration
}

// ── HotPath ───────────────────────────────────────────────────────────────────

// HotPath drives the near-real-time changeset streaming loop.
//
// Construct with NewHotPath; start the push loop with go hp.Run(ctx).
// Register hp.HandleInboundChangeset on the inbound peering mux to receive
// changesets from remote peers.
type HotPath struct {
	cfg HotPathConfig

	// mu guards sentCursors and applyFn.
	mu sync.Mutex
	// sentCursors maps peerNodeID → last db_version successfully sent.
	// Intentionally in-memory only (reset on restart).
	sentCursors map[string]int64

	// insertFn inserts cr-sqlite change rows.  Swappable for testing.
	insertFn func(ctx context.Context, changes []hpChangeRow) error
}

// NewHotPath creates a HotPath.
func NewHotPath(cfg HotPathConfig) (*HotPath, error) {
	if cfg.NodeID == "" {
		return nil, fmt.Errorf("hotpath: NodeID must not be empty")
	}
	if cfg.VulaID == "" {
		return nil, fmt.Errorf("hotpath: VulaID must not be empty")
	}
	if len(cfg.PrivKey) == 0 {
		return nil, fmt.Errorf("hotpath: PrivKey must not be empty")
	}
	if cfg.DB == nil {
		return nil, fmt.Errorf("hotpath: DB must not be nil")
	}
	if cfg.Peers == nil {
		return nil, fmt.Errorf("hotpath: Peers func must not be nil")
	}
	if cfg.Client == nil {
		return nil, fmt.Errorf("hotpath: Client must not be nil")
	}
	if cfg.PushInterval <= 0 {
		cfg.PushInterval = 500 * time.Millisecond
	}

	hp := &HotPath{
		cfg:         cfg,
		sentCursors: make(map[string]int64),
	}

	// Default insert uses the cluster's transaction-based CRDT insert.
	hp.insertFn = hp.insertChanges
	return hp, nil
}

// Run starts the hot-path push loop. It blocks until ctx is cancelled.
//
//	go hp.Run(ctx)
func (hp *HotPath) Run(ctx context.Context) {
	log.Printf("[hotpath] starting (interval=%s, node=%s)", hp.cfg.PushInterval, hp.cfg.NodeID)

	// Push once immediately, then on every tick.
	hp.pushAll(ctx)

	ticker := time.NewTicker(hp.cfg.PushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[hotpath] stopping")
			return
		case <-ticker.C:
			hp.pushAll(ctx)
		}
	}
}

// pushAll iterates over all live peers and sends them any unseen changesets.
func (hp *HotPath) pushAll(ctx context.Context) {
	peers := hp.cfg.Peers()
	if len(peers) == 0 {
		return
	}

	for _, peer := range peers {
		if peer.NodeID == hp.cfg.NodeID {
			continue // never push to self
		}
		if err := hp.pushToPeer(ctx, peer); err != nil {
			log.Printf("[hotpath] push to %s: %v", peer.NodeID, err)
		}
	}
}

// pushToPeer reads crsql_changes > last sent cursor for peer, serialises them,
// and delivers via direct mesh or relay fallback.
func (hp *HotPath) pushToPeer(ctx context.Context, peer PeerInfo) error {
	hp.mu.Lock()
	cursor := hp.sentCursors[peer.NodeID]
	hp.mu.Unlock()

	// Query new changes beyond the cursor for this peer.
	rows, err := hp.cfg.DB.QueryContext(ctx, `
		SELECT "table", pk, cid, val, col_version, db_version, site_id, cl, seq
		FROM crsql_changes
		WHERE db_version > ?
		ORDER BY db_version ASC, seq ASC
	`, cursor)
	if err != nil {
		if hpIsMissingTable(err) {
			return nil // cr-sqlite not loaded; silently skip
		}
		return fmt.Errorf("query crsql_changes: %w", err)
	}
	defer rows.Close()

	var changes []hpChangeRow
	var maxVersion int64 = cursor

	for rows.Next() {
		var r hpChangeRow
		if err := rows.Scan(
			&r.Table, &r.PK, &r.CID, &r.Val,
			&r.ColVersion, &r.DBVersion, &r.SiteID, &r.CL, &r.Seq,
		); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		changes = append(changes, r)
		if r.DBVersion > maxVersion {
			maxVersion = r.DBVersion
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows: %w", err)
	}

	if len(changes) == 0 {
		return nil // nothing to send
	}

	cs := &hpChangeset{
		NodeID:    hp.cfg.NodeID,
		DBVersion: maxVersion,
		Changes:   changes,
	}

	data, err := serialiseHPChangeset(cs)
	if err != nil {
		return fmt.Errorf("serialise: %w", err)
	}

	// Build and sign the peering envelope.
	msgID := hotpathUUID()
	payloadB64 := base64.StdEncoding.EncodeToString(data)
	payloadJSON, err := json.Marshal(map[string]string{
		"encoding": "base64",
		"data":     payloadB64,
	})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	env := HotPathEnvelope{
		ID:        msgID,
		From:      hp.cfg.VulaID,
		To:        peer.VulaID,
		Type:      TypeChangeset,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   json.RawMessage(payloadJSON),
	}

	if err := hp.sign(&env); err != nil {
		return fmt.Errorf("sign: %w", err)
	}

	// ── Direct delivery ──────────────────────────────────────────────────────
	var deliveryErr error
	if peer.ServerURL != "" {
		deliveryErr = hp.cfg.Client.Post(ctx, peer.ServerURL, TypeChangeset, env)
	} else {
		deliveryErr = fmt.Errorf("no server URL; relay only")
	}

	if deliveryErr != nil {
		// ── Relay fallback ─────────────────────────────────────────────────
		if hp.cfg.Relay != nil {
			blobID := changesetBlobID(hp.cfg.NodeID, maxVersion)
			if relayErr := hp.cfg.Relay.Deposit(ctx, peer.VulaID, blobID, data); relayErr != nil {
				return fmt.Errorf("direct failed (%v); relay deposit also failed: %w", deliveryErr, relayErr)
			}
			log.Printf("[hotpath] relay fallback: deposited %d changes (db_version=%d) for peer %s",
				len(changes), maxVersion, peer.NodeID)
		} else {
			return fmt.Errorf("direct delivery: %w (no relay configured)", deliveryErr)
		}
	} else {
		log.Printf("[hotpath] pushed %d changes (db_version=%d) → %s (direct)",
			len(changes), maxVersion, peer.NodeID)
	}

	// Advance cursor only after successful delivery (direct or relay).
	hp.mu.Lock()
	if maxVersion > hp.sentCursors[peer.NodeID] {
		hp.sentCursors[peer.NodeID] = maxVersion
	}
	hp.mu.Unlock()

	return nil
}

// Pickup collects blobs deposited for this node on the relay and applies them.
// Call periodically (e.g. alongside the push loop) to drain the relay inbox.
func (hp *HotPath) Pickup(ctx context.Context) {
	if hp.cfg.Relay == nil {
		return
	}

	blobs, err := hp.cfg.Relay.Pickup(ctx, hp.cfg.VulaID)
	if err != nil {
		log.Printf("[hotpath] relay pickup error: %v", err)
		return
	}

	for _, blob := range blobs {
		cs, err := deserialiseHPChangeset(blob)
		if err != nil {
			log.Printf("[hotpath] relay pickup: deserialise: %v", err)
			continue
		}
		if err := hp.insertFn(ctx, cs.Changes); err != nil {
			log.Printf("[hotpath] relay pickup: insert (from=%s db_version=%d): %v",
				cs.NodeID, cs.DBVersion, err)
			continue
		}
		log.Printf("[hotpath] relay pickup: applied %d changes from %s (db_version=%d)",
			len(cs.Changes), cs.NodeID, cs.DBVersion)
	}
}

// HandleInboundChangeset handles POST /api/peering/inbound/changeset.
//
// This handler is called AFTER InboundMiddleware has:
//   - Verified the Ed25519 envelope signature (401 on failure).
//   - Confirmed the sender is in the approved contacts list (403 on failure).
//
// The handler:
//  1. Reads the pre-verified *HotPathEnvelope from the request context (set by
//     the inbound middleware shim registered alongside this handler).
//  2. Validates that the envelope type is TypeChangeset.
//  3. Base64-decodes the payload's "data" field.
//  4. Deserialises the changeset wire format.
//  5. Inserts changes via the cluster CRDT merge path (insertChanges).
//
// Because the hot path uses InboundMiddleware (the same middleware as
// messages/signals), registration looks like:
//
//	mux.HandleFunc("POST /api/peering/inbound/changeset", hp.HandleInboundChangeset)
//
// The handler is exported so it can be wired by the orchestrator in
// cmd/server/main.go onto the inbound sub-mux (wrapped in InboundMiddleware).
func (hp *HotPath) HandleInboundChangeset(w http.ResponseWriter, r *http.Request) {
	// The envelope was decoded and verified by InboundMiddleware and stored
	// in context. We retrieve the raw body ourselves here since the hot path
	// does not import the peering package (to avoid cycles); the orchestrator
	// that wires routes passes us the already-parsed JSON via the context key
	// defined in hotPathEnvelopeKey below.
	env, ok := r.Context().Value(hotPathEnvelopeKey).(*HotPathEnvelope)
	if !ok || env == nil {
		// Fallback: parse the body directly (when used without the context shim).
		var e HotPathEnvelope
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			writeJSONHP(w, http.StatusBadRequest, map[string]string{
				"error": "invalid envelope: " + err.Error(),
			})
			return
		}
		env = &e
	}

	if env.Type != TypeChangeset {
		writeJSONHP(w, http.StatusBadRequest, map[string]string{
			"error": "unexpected envelope type: " + env.Type,
		})
		return
	}

	// Decode payload: {"encoding":"base64","data":"<b64>"}
	var payload struct {
		Encoding string `json:"encoding"`
		Data     string `json:"data"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		writeJSONHP(w, http.StatusBadRequest, map[string]string{
			"error": "invalid changeset payload: " + err.Error(),
		})
		return
	}
	if payload.Encoding != "base64" || payload.Data == "" {
		writeJSONHP(w, http.StatusBadRequest, map[string]string{
			"error": "changeset payload must have encoding=base64 and non-empty data",
		})
		return
	}

	raw, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		writeJSONHP(w, http.StatusBadRequest, map[string]string{
			"error": "base64 decode: " + err.Error(),
		})
		return
	}

	cs, err := deserialiseHPChangeset(raw)
	if err != nil {
		writeJSONHP(w, http.StatusBadRequest, map[string]string{
			"error": "deserialise changeset: " + err.Error(),
		})
		return
	}

	if err := hp.insertFn(r.Context(), cs.Changes); err != nil {
		log.Printf("[hotpath] inbound insert (from=%s db_version=%d): %v",
			cs.NodeID, cs.DBVersion, err)
		writeJSONHP(w, http.StatusInternalServerError, map[string]string{
			"error": "insert changes: " + err.Error(),
		})
		return
	}

	log.Printf("[hotpath] applied %d inbound changes from %s (db_version=%d)",
		len(cs.Changes), cs.NodeID, cs.DBVersion)
	writeJSONHP(w, http.StatusOK, map[string]any{"status": "applied", "count": len(cs.Changes)})
}

// ── context key for envelope shim ─────────────────────────────────────────────

// hotPathContextKey is the unexported context key type for hotpath.
type hotPathContextKey int

// hotPathEnvelopeKey is the context key under which the orchestrator stores a
// *HotPathEnvelope after InboundMiddleware verifies it.
const hotPathEnvelopeKey hotPathContextKey = iota

// WithHotPathEnvelope returns a new context with env stored under hotPathEnvelopeKey.
// Call this in the InboundMiddleware shim that bridges peering.Envelope →
// HotPathEnvelope before dispatching to HandleInboundChangeset.
func WithHotPathEnvelope(ctx context.Context, env *HotPathEnvelope) context.Context {
	return context.WithValue(ctx, hotPathEnvelopeKey, env)
}

// ── insertChanges (CRDT merge) ────────────────────────────────────────────────

// insertChanges inserts change rows into crsql_changes inside a transaction.
// This is the same logic as cluster.SyncLoop.insertChanges — kept here to
// avoid import cycles (sync → cluster would be circular since cluster already
// uses the sync package's SnapshotS3 interface).
func (hp *HotPath) insertChanges(ctx context.Context, changes []hpChangeRow) error {
	if len(changes) == 0 {
		return nil
	}

	tx, err := hp.cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var txErr error
	defer func() {
		if txErr != nil {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO crsql_changes
			("table", pk, cid, val, col_version, db_version, site_id, cl, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		txErr = err
		return err
	}
	defer stmt.Close()

	for _, r := range changes {
		if _, txErr = stmt.ExecContext(ctx,
			r.Table, r.PK, r.CID, r.Val,
			r.ColVersion, r.DBVersion, r.SiteID, r.CL, r.Seq,
		); txErr != nil {
			if hpIsIgnorableInsertError(txErr) {
				txErr = nil
				continue
			}
			return fmt.Errorf("insert change (table=%s pk=%s): %w", r.Table, r.PK, txErr)
		}
	}

	txErr = tx.Commit()
	return txErr
}

// ── signing ───────────────────────────────────────────────────────────────────

// sign computes an Ed25519 signature over the canonical bytes of env (excluding
// the Signature field) and stores it as base64url (no-padding) in env.Signature.
//
// This mirrors peering.Envelope.Sign without depending on the peering package.
func (hp *HotPath) sign(env *HotPathEnvelope) error {
	priv := hp.cfg.PrivKey
	if len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("hotpath: invalid private key length %d", len(priv))
	}

	// Serialise without the signature field for canonical bytes.
	tmp := *env
	tmp.Signature = ""
	canonical, err := json.Marshal(tmp)
	if err != nil {
		return fmt.Errorf("hotpath sign: marshal: %w", err)
	}

	sig := ed25519.Sign(priv, canonical)
	env.Signature = base64.RawURLEncoding.EncodeToString(sig)
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// changesetBlobID returns a filesystem-safe relay blob ID for the given node
// and db_version.  Format: "cs-<nodeID>-<dbVersion>"
func changesetBlobID(nodeID string, dbVersion int64) string {
	return fmt.Sprintf("cs-%s-%d", safeID(nodeID), dbVersion)
}

// safeID strips non-alphanumeric characters so the result is relay-blob-ID safe.
func safeID(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' {
			b = append(b, c)
		} else {
			b = append(b, '_')
		}
	}
	return string(b)
}

// hotpathUUID returns a simple time-based unique ID string suitable for
// envelope IDs.  We avoid importing github.com/google/uuid here to keep
// the sync package dependency-light; the format is compatible with the
// envelope ID field (any non-empty string).
func hotpathUUID() string {
	return fmt.Sprintf("hp-%d", time.Now().UnixNano())
}

// writeJSONHP writes a JSON response — local alias so hotpath.go is self-contained
// and does not depend on the peering package's writeJSON helper.
func writeJSONHP(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body) //nolint:errcheck
}
