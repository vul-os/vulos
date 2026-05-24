package sync

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	// FIX-CGO-TEST-RESIDUE-01: use pure-Go modernc.org/sqlite (never CGo
	// mattn/go-sqlite3) so `CGO_ENABLED=0 go test ./...` passes the
	// pure-Go invariant. Driver name is "sqlite" (not "sqlite3").
	_ "modernc.org/sqlite"
)

// ── in-memory mock implementations ───────────────────────────────────────────

// mockPeerClient records Post calls and can inject a fixed error.
type mockPeerClient struct {
	mu      sync.Mutex
	calls   []mockPeerCall
	postErr error
}

type mockPeerCall struct {
	baseURL      string
	envelopeType string
	env          HotPathEnvelope
}

func (m *mockPeerClient) Post(_ context.Context, baseURL, envelopeType string, env HotPathEnvelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.postErr != nil {
		return m.postErr
	}
	m.calls = append(m.calls, mockPeerCall{baseURL: baseURL, envelopeType: envelopeType, env: env})
	return nil
}

func (m *mockPeerClient) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// mockRelayClient implements HotPathRelayClient with in-memory storage.
type mockRelayClient struct {
	mu         sync.Mutex
	blobs      map[string][][]byte
	depositErr error
	pickupErr  error
}

func newMockRelay() *mockRelayClient {
	return &mockRelayClient{blobs: make(map[string][][]byte)}
}

func (r *mockRelayClient) Deposit(_ context.Context, recipientVulaID, _ string, blob []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.depositErr != nil {
		return r.depositErr
	}
	cp := make([]byte, len(blob))
	copy(cp, blob)
	r.blobs[recipientVulaID] = append(r.blobs[recipientVulaID], cp)
	return nil
}

func (r *mockRelayClient) Pickup(_ context.Context, localVulaID string) ([][]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pickupErr != nil {
		return nil, r.pickupErr
	}
	blobs := r.blobs[localVulaID]
	delete(r.blobs, localVulaID)
	return blobs, nil
}

func (r *mockRelayClient) depositCount(vulaID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.blobs[vulaID])
}

// ── SQLite helpers ────────────────────────────────────────────────────────────

// openMemDB opens an in-memory SQLite database and creates a plain
// crsql_changes table with the same schema as the cr-sqlite virtual table.
// Because the cr-sqlite native extension is not available in CI, a plain table
// is used — the transport tests only need the INSERT/SELECT contract.
func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS crsql_changes (
		"table"      TEXT    NOT NULL,
		pk           TEXT    NOT NULL,
		cid          TEXT    NOT NULL,
		val          TEXT,
		col_version  INTEGER NOT NULL DEFAULT 0,
		db_version   INTEGER NOT NULL DEFAULT 0,
		site_id      BLOB,
		cl           INTEGER NOT NULL DEFAULT 0,
		seq          INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		t.Fatalf("create crsql_changes: %v", err)
	}

	t.Cleanup(func() { db.Close() })
	return db
}

// insertTestChange inserts a fake cr-sqlite change row.
func insertTestChange(t *testing.T, db *sql.DB, table, pk string, dbVersion int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO crsql_changes ("table", pk, cid, val, col_version, db_version, site_id, cl, seq)
		VALUES (?, ?, 'col', 'val', 1, ?, NULL, 1, 0)
	`, table, pk, dbVersion)
	if err != nil {
		t.Fatalf("insertTestChange: %v", err)
	}
}

// countChanges returns the number of rows in crsql_changes.
func countChanges(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crsql_changes`).Scan(&n); err != nil {
		t.Fatalf("countChanges: %v", err)
	}
	return n
}

// ── key helpers ───────────────────────────────────────────────────────────────

func genKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return priv
}

// ── envelope helpers ──────────────────────────────────────────────────────────

// applyEnvelope simulates instance hp receiving a hot-path envelope by calling
// HandleInboundChangeset with env serialised as the HTTP body.
func applyEnvelope(t *testing.T, hp *HotPath, env HotPathEnvelope) {
	t.Helper()
	bodyJSON, err := json.Marshal(env)
	if err != nil {
		t.Errorf("marshal envelope: %v", err)
		return
	}
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/changeset",
		bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	hp.HandleInboundChangeset(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("HandleInboundChangeset returned %d: %s", rr.Code, rr.Body.String())
	}
}

// decodeEnvelopeChangeset extracts and deserialises the changeset from an env.
func decodeEnvelopeChangeset(t *testing.T, env HotPathEnvelope) *hpChangeset {
	t.Helper()
	var payload struct {
		Encoding string `json:"encoding"`
		Data     string `json:"data"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	cs, err := deserialiseHPChangeset(raw)
	if err != nil {
		t.Fatalf("deserialise changeset: %v", err)
	}
	return cs
}

// ── funcPeerClient ────────────────────────────────────────────────────────────

// funcPeerClient backs HotPathPeerClient with an arbitrary function.
type funcPeerClient struct {
	fn func(context.Context, string, string, HotPathEnvelope) error
}

func (f *funcPeerClient) Post(ctx context.Context, baseURL, typ string, env HotPathEnvelope) error {
	return f.fn(ctx, baseURL, typ, env)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestHotPath_DirectMeshConvergence verifies AC: two live instances converge
// via the direct mesh.
//
//  1. Instance A seeds two rows in its DB.
//  2. A pushes to B via mockPeerClient (direct HTTP succeeds).
//  3. The captured envelope is replayed into B via HandleInboundChangeset.
//  4. B's DB has both rows.
func TestHotPath_DirectMeshConvergence(t *testing.T) {
	ctx := context.Background()
	dbA := openMemDB(t)
	dbB := openMemDB(t)

	privA, privB := genKey(t), genKey(t)

	insertTestChange(t, dbA, "todos", "pk1", 1)
	insertTestChange(t, dbA, "todos", "pk2", 2)

	clientA := &mockPeerClient{}
	hpA, err := NewHotPath(HotPathConfig{
		NodeID:  "node-a",
		VulaID:  "vula:ed25519:AAAA",
		PrivKey: privA,
		DB:      dbA,
		Peers: func() []PeerInfo {
			return []PeerInfo{{NodeID: "node-b", VulaID: "vula:ed25519:BBBB", ServerURL: "https://b.example.com"}}
		},
		Client: clientA,
	})
	if err != nil {
		t.Fatalf("NewHotPath A: %v", err)
	}

	hpB, err := NewHotPath(HotPathConfig{
		NodeID:  "node-b",
		VulaID:  "vula:ed25519:BBBB",
		PrivKey: privB,
		DB:      dbB,
		Peers:   func() []PeerInfo { return nil },
		Client:  &mockPeerClient{},
	})
	if err != nil {
		t.Fatalf("NewHotPath B: %v", err)
	}

	// Push from A.
	if err := hpA.pushToPeer(ctx, PeerInfo{
		NodeID:    "node-b",
		VulaID:    "vula:ed25519:BBBB",
		ServerURL: "https://b.example.com",
	}); err != nil {
		t.Fatalf("pushToPeer: %v", err)
	}

	if clientA.callCount() != 1 {
		t.Fatalf("expected 1 Post call, got %d", clientA.callCount())
	}

	// B receives via HandleInboundChangeset.
	applyEnvelope(t, hpB, clientA.calls[0].env)

	if n := countChanges(t, dbB); n != 2 {
		t.Errorf("B: expected 2 changes, got %d", n)
	}
}

// TestHotPath_RelayFallback verifies AC: relay fallback when direct is blocked.
//
// When PeerClient.Post fails (NAT blocked), the changeset is deposited on the
// relay. When B calls Pickup, the changes are applied to B's DB.
func TestHotPath_RelayFallback(t *testing.T) {
	ctx := context.Background()
	dbA := openMemDB(t)
	dbB := openMemDB(t)

	privA, privB := genKey(t), genKey(t)

	insertTestChange(t, dbA, "notes", "n1", 10)
	insertTestChange(t, dbA, "notes", "n2", 11)

	relay := newMockRelay()
	clientA := &mockPeerClient{postErr: fmt.Errorf("connection refused")}

	hpA, err := NewHotPath(HotPathConfig{
		NodeID:  "node-c",
		VulaID:  "vula:ed25519:CCCC",
		PrivKey: privA,
		DB:      dbA,
		Peers: func() []PeerInfo {
			return []PeerInfo{{NodeID: "node-d", VulaID: "vula:ed25519:DDDD", ServerURL: "https://d.example.com"}}
		},
		Client: clientA,
		Relay:  relay,
	})
	if err != nil {
		t.Fatalf("NewHotPath A: %v", err)
	}

	hpB, err := NewHotPath(HotPathConfig{
		NodeID:  "node-d",
		VulaID:  "vula:ed25519:DDDD",
		PrivKey: privB,
		DB:      dbB,
		Peers:   func() []PeerInfo { return nil },
		Client:  &mockPeerClient{},
		Relay:   relay,
	})
	if err != nil {
		t.Fatalf("NewHotPath B: %v", err)
	}

	// Push falls back to relay.
	if err := hpA.pushToPeer(ctx, PeerInfo{
		NodeID:    "node-d",
		VulaID:    "vula:ed25519:DDDD",
		ServerURL: "https://d.example.com",
	}); err != nil {
		t.Fatalf("pushToPeer (relay): %v", err)
	}

	if relay.depositCount("vula:ed25519:DDDD") != 1 {
		t.Fatalf("expected 1 relay deposit, got %d", relay.depositCount("vula:ed25519:DDDD"))
	}

	// B picks up from relay.
	hpB.Pickup(ctx)

	if n := countChanges(t, dbB); n != 2 {
		t.Errorf("B: expected 2 changes after relay pickup, got %d", n)
	}
}

// TestHotPath_ColdPathUnaffected verifies AC: cold-path bucket sync still runs.
//
// HotPath must not touch the _sync_cursors table that cluster.SyncLoop owns.
func TestHotPath_ColdPathUnaffected(t *testing.T) {
	ctx := context.Background()
	db := openMemDB(t)

	// Create and seed the cold-path cursor table as SyncLoop would.
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS _sync_cursors (
		node_id    TEXT PRIMARY KEY,
		db_version INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		t.Fatalf("create _sync_cursors: %v", err)
	}
	_, err = db.Exec(`INSERT INTO _sync_cursors (node_id, db_version) VALUES ('__self__', 42)`)
	if err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	priv := genKey(t)
	hp, err := NewHotPath(HotPathConfig{
		NodeID:  "node-e",
		VulaID:  "vula:ed25519:EEEE",
		PrivKey: priv,
		DB:      db,
		Peers:   func() []PeerInfo { return nil },
		Client:  &mockPeerClient{},
	})
	if err != nil {
		t.Fatalf("NewHotPath: %v", err)
	}

	hp.pushAll(ctx)

	// Cold-path cursor must be untouched.
	var cursor int64
	if err := db.QueryRow(`SELECT db_version FROM _sync_cursors WHERE node_id = '__self__'`).Scan(&cursor); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != 42 {
		t.Errorf("cold-path cursor modified by hotpath: got %d, want 42", cursor)
	}
}

// TestHotPath_CRDTMerge_NoBrainSplit verifies AC: CRDT merge (no split-brain).
//
// Both A and B write distinct rows and exchange changesets. After exchange both
// DBs must contain all rows — CRDT merge converges without data loss.
func TestHotPath_CRDTMerge_NoBrainSplit(t *testing.T) {
	ctx := context.Background()
	dbA := openMemDB(t)
	dbB := openMemDB(t)

	privA, privB := genKey(t), genKey(t)

	insertTestChange(t, dbA, "items", "pk1", 1)
	insertTestChange(t, dbA, "items", "pk2", 2)
	insertTestChange(t, dbB, "items", "pk3", 3)
	insertTestChange(t, dbB, "items", "pk4", 4)

	clientA := &mockPeerClient{}
	clientB := &mockPeerClient{}

	hpA, _ := NewHotPath(HotPathConfig{
		NodeID:  "node-f",
		VulaID:  "vula:ed25519:FFFF",
		PrivKey: privA,
		DB:      dbA,
		Peers: func() []PeerInfo {
			return []PeerInfo{{NodeID: "node-g", VulaID: "vula:ed25519:GGGG", ServerURL: "https://g.example.com"}}
		},
		Client: clientA,
	})

	hpB, _ := NewHotPath(HotPathConfig{
		NodeID:  "node-g",
		VulaID:  "vula:ed25519:GGGG",
		PrivKey: privB,
		DB:      dbB,
		Peers: func() []PeerInfo {
			return []PeerInfo{{NodeID: "node-f", VulaID: "vula:ed25519:FFFF", ServerURL: "https://f.example.com"}}
		},
		Client: clientB,
	})

	// A → B
	if err := hpA.pushToPeer(ctx, PeerInfo{NodeID: "node-g", VulaID: "vula:ed25519:GGGG", ServerURL: "https://g.example.com"}); err != nil {
		t.Fatalf("A→B push: %v", err)
	}
	// B → A
	if err := hpB.pushToPeer(ctx, PeerInfo{NodeID: "node-f", VulaID: "vula:ed25519:FFFF", ServerURL: "https://f.example.com"}); err != nil {
		t.Fatalf("B→A push: %v", err)
	}

	// Apply cross-envelopes.
	applyEnvelope(t, hpB, clientA.calls[0].env) // A's changes → B
	applyEnvelope(t, hpA, clientB.calls[0].env) // B's changes → A

	if n := countChanges(t, dbA); n != 4 {
		t.Errorf("A: expected 4 changes, got %d", n)
	}
	if n := countChanges(t, dbB); n != 4 {
		t.Errorf("B: expected 4 changes, got %d", n)
	}
}

// TestHotPath_CursorAdvances verifies that the per-peer cursor advances so
// subsequent pushes only send new rows.
func TestHotPath_CursorAdvances(t *testing.T) {
	ctx := context.Background()
	db := openMemDB(t)
	priv := genKey(t)

	insertTestChange(t, db, "docs", "d1", 1)
	insertTestChange(t, db, "docs", "d2", 2)

	client := &mockPeerClient{}
	hp, err := NewHotPath(HotPathConfig{
		NodeID:  "node-h",
		VulaID:  "vula:ed25519:HHHH",
		PrivKey: priv,
		DB:      db,
		Peers: func() []PeerInfo {
			return []PeerInfo{{NodeID: "node-i", VulaID: "vula:ed25519:IIII", ServerURL: "https://i.example.com"}}
		},
		Client: client,
	})
	if err != nil {
		t.Fatalf("NewHotPath: %v", err)
	}

	peer := PeerInfo{NodeID: "node-i", VulaID: "vula:ed25519:IIII", ServerURL: "https://i.example.com"}

	// First push — sends d1 and d2.
	if err := hp.pushToPeer(ctx, peer); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if client.callCount() != 1 {
		t.Fatalf("expected 1 Post call, got %d", client.callCount())
	}

	// Add a third row.
	insertTestChange(t, db, "docs", "d3", 3)

	// Second push — cursor is 2; only d3 should be sent.
	if err := hp.pushToPeer(ctx, peer); err != nil {
		t.Fatalf("second push: %v", err)
	}
	if client.callCount() != 2 {
		t.Fatalf("expected 2 Post calls, got %d", client.callCount())
	}

	cs := decodeEnvelopeChangeset(t, client.calls[1].env)
	if len(cs.Changes) != 1 {
		t.Errorf("expected 1 change in second push, got %d", len(cs.Changes))
	}
	if cs.Changes[0].PK != "d3" {
		t.Errorf("expected pk=d3, got %q", cs.Changes[0].PK)
	}
}

// TestHotPath_ConvergesWithinInterval checks that the push loop running at
// 50 ms delivers changes well within a typical 5-second sync interval.
func TestHotPath_ConvergesWithinInterval(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped in short mode")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbA := openMemDB(t)
	dbB := openMemDB(t)

	privA, privB := genKey(t), genKey(t)
	received := make(chan struct{}, 1)

	var hpBPtr *HotPath // set before loop starts so the closure sees the real value

	clientA := &funcPeerClient{fn: func(_ context.Context, _ string, _ string, env HotPathEnvelope) error {
		if hpBPtr == nil {
			return nil
		}
		go func() {
			applyEnvelope(t, hpBPtr, env)
			select {
			case received <- struct{}{}:
			default:
			}
		}()
		return nil
	}}

	hpA, err := NewHotPath(HotPathConfig{
		NodeID:       "node-j",
		VulaID:       "vula:ed25519:JJJJ",
		PrivKey:      privA,
		DB:           dbA,
		PushInterval: 50 * time.Millisecond,
		Peers: func() []PeerInfo {
			return []PeerInfo{{NodeID: "node-k", VulaID: "vula:ed25519:KKKK", ServerURL: "https://k.example.com"}}
		},
		Client: clientA,
	})
	if err != nil {
		t.Fatalf("NewHotPath A: %v", err)
	}

	hpB, err := NewHotPath(HotPathConfig{
		NodeID:  "node-k",
		VulaID:  "vula:ed25519:KKKK",
		PrivKey: privB,
		DB:      dbB,
		Peers:   func() []PeerInfo { return nil },
		Client:  &mockPeerClient{},
	})
	if err != nil {
		t.Fatalf("NewHotPath B: %v", err)
	}
	hpBPtr = hpB

	go hpA.Run(ctx)

	// Seed a change after the loop starts.
	time.Sleep(20 * time.Millisecond)
	insertTestChange(t, dbA, "live", "row1", 1)

	select {
	case <-received:
		// Delivered well under the 5-second sync interval.
	case <-time.After(2 * time.Second):
		t.Fatal("convergence timed out (2 s << 5 s sync interval)")
	}
}

// TestHotPath_NewHotPath_Validation verifies that NewHotPath rejects invalid configs.
func TestHotPath_NewHotPath_Validation(t *testing.T) {
	priv := genKey(t)
	db := openMemDB(t)
	noopClient := &mockPeerClient{}

	tests := []struct {
		name    string
		cfg     HotPathConfig
		wantErr bool
	}{
		{
			name:    "missing NodeID",
			cfg:     HotPathConfig{VulaID: "vula:ed25519:X", PrivKey: priv, DB: db, Peers: func() []PeerInfo { return nil }, Client: noopClient},
			wantErr: true,
		},
		{
			name:    "missing VulaID",
			cfg:     HotPathConfig{NodeID: "n1", PrivKey: priv, DB: db, Peers: func() []PeerInfo { return nil }, Client: noopClient},
			wantErr: true,
		},
		{
			name:    "missing PrivKey",
			cfg:     HotPathConfig{NodeID: "n1", VulaID: "vula:ed25519:X", DB: db, Peers: func() []PeerInfo { return nil }, Client: noopClient},
			wantErr: true,
		},
		{
			name:    "nil DB",
			cfg:     HotPathConfig{NodeID: "n1", VulaID: "vula:ed25519:X", PrivKey: priv, Peers: func() []PeerInfo { return nil }, Client: noopClient},
			wantErr: true,
		},
		{
			name:    "nil Peers func",
			cfg:     HotPathConfig{NodeID: "n1", VulaID: "vula:ed25519:X", PrivKey: priv, DB: db, Client: noopClient},
			wantErr: true,
		},
		{
			name:    "nil Client",
			cfg:     HotPathConfig{NodeID: "n1", VulaID: "vula:ed25519:X", PrivKey: priv, DB: db, Peers: func() []PeerInfo { return nil }},
			wantErr: true,
		},
		{
			name:    "valid config",
			cfg:     HotPathConfig{NodeID: "n1", VulaID: "vula:ed25519:X", PrivKey: priv, DB: db, Peers: func() []PeerInfo { return nil }, Client: noopClient},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewHotPath(tc.cfg)
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestHotPath_NoRelayConfigured ensures an error is returned when direct fails
// and no relay is configured.
func TestHotPath_NoRelayConfigured(t *testing.T) {
	ctx := context.Background()
	db := openMemDB(t)
	priv := genKey(t)

	insertTestChange(t, db, "t", "p", 1)

	hp, err := NewHotPath(HotPathConfig{
		NodeID:  "node-x",
		VulaID:  "vula:ed25519:XXXX",
		PrivKey: priv,
		DB:      db,
		Peers:   func() []PeerInfo { return nil },
		Client:  &mockPeerClient{postErr: fmt.Errorf("dial: connection refused")},
		// Relay deliberately omitted.
	})
	if err != nil {
		t.Fatalf("NewHotPath: %v", err)
	}

	err = hp.pushToPeer(ctx, PeerInfo{
		NodeID:    "node-y",
		VulaID:    "vula:ed25519:YYYY",
		ServerURL: "https://y.example.com",
	})
	if err == nil {
		t.Error("expected error when direct fails and no relay is configured, got nil")
	}
}

// TestHotPath_InboundChangeset_WrongType verifies that HandleInboundChangeset
// rejects envelopes with the wrong type.
func TestHotPath_InboundChangeset_WrongType(t *testing.T) {
	db := openMemDB(t)
	priv := genKey(t)
	hp, _ := NewHotPath(HotPathConfig{
		NodeID:  "node-z",
		VulaID:  "vula:ed25519:ZZZZ",
		PrivKey: priv,
		DB:      db,
		Peers:   func() []PeerInfo { return nil },
		Client:  &mockPeerClient{},
	})

	env := HotPathEnvelope{
		ID:        "bad-type-1",
		From:      "vula:ed25519:AAAA",
		Type:      "message", // wrong type
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   json.RawMessage(`{}`),
	}
	bodyJSON, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/changeset",
		bytes.NewReader(bodyJSON))
	rr := httptest.NewRecorder()
	hp.HandleInboundChangeset(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for wrong envelope type, got %d", rr.Code)
	}
}
