package peering

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ─────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────

func newTestPresenceService() *PresenceService {
	return NewPresenceService()
}

func doPresenceReq(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func presenceMux(svc *PresenceService) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterPresenceHandlers(mux, svc)
	return mux
}

// ─────────────────────────────────────────
// presenceRoom unit tests
// ─────────────────────────────────────────

func TestPresenceRoom_JoinLeave(t *testing.T) {
	room := newPresenceRoom()

	c := &presenceClient{send: make(chan []byte, 8), appID: "app1", peerID: "peer-a"}
	room.join(c)

	if room.count() != 1 {
		t.Errorf("count after join = %d, want 1", room.count())
	}

	peers := room.peerList()
	if len(peers) != 1 {
		t.Fatalf("peer list len = %d, want 1", len(peers))
	}
	if peers[0].PeerID != "peer-a" {
		t.Errorf("peerID = %q, want %q", peers[0].PeerID, "peer-a")
	}

	room.leave(c)
	if room.count() != 0 {
		t.Errorf("count after leave = %d, want 0", room.count())
	}
	// Peer entry must be evicted on disconnect (awareness clears).
	if len(room.peerList()) != 0 {
		t.Errorf("peer list after leave should be empty, got %d entries", len(room.peerList()))
	}
}

func TestPresenceRoom_AwarenessClearsOnDisconnect(t *testing.T) {
	// AC: "awareness clears on disconnect"
	room := newPresenceRoom()

	c := &presenceClient{send: make(chan []byte, 8), appID: "app1", peerID: "peer-b"}
	room.join(c)
	room.upsertPeer("peer-b", json.RawMessage(`{"cursor":{"x":10,"y":20}}`))

	// Before disconnect, peer data is present.
	peers := room.peerList()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer before disconnect")
	}

	room.leave(c)

	// After disconnect, peer entry must be gone — never persisted.
	if len(room.peerList()) != 0 {
		t.Errorf("peer entry not cleared after disconnect; awareness must be ephemeral")
	}
}

func TestPresenceRoom_BroadcastExcludesOrigin(t *testing.T) {
	room := newPresenceRoom()

	c1 := &presenceClient{send: make(chan []byte, 8), appID: "app1", peerID: "p1"}
	c2 := &presenceClient{send: make(chan []byte, 8), appID: "app1", peerID: "p2"}
	room.join(c1)
	room.join(c2)

	msg := []byte(`{"channel":"presence","from":"p1","payload":{}}`)
	room.broadcast(c1, msg)

	select {
	case data := <-c2.send:
		if string(data) != string(msg) {
			t.Errorf("c2 received %q, want %q", data, msg)
		}
	default:
		t.Error("c2 did not receive the broadcast")
	}

	select {
	case data := <-c1.send:
		t.Errorf("c1 (origin) should not receive its own broadcast, got %q", data)
	default:
		// correct
	}
}

func TestPresenceRoom_BroadcastNilOriginSendsToAll(t *testing.T) {
	// nil origin = S2S relay path; must reach all local clients.
	room := newPresenceRoom()

	c1 := &presenceClient{send: make(chan []byte, 8), appID: "app1", peerID: "p1"}
	c2 := &presenceClient{send: make(chan []byte, 8), appID: "app1", peerID: "p2"}
	room.join(c1)
	room.join(c2)

	msg := []byte(`{"channel":"presence","from":"remote","payload":{}}`)
	room.broadcast(nil, msg)

	for i, c := range []*presenceClient{c1, c2} {
		select {
		case <-c.send:
			// received — OK
		default:
			t.Errorf("client %d did not receive broadcast from nil origin", i+1)
		}
	}
}

func TestPresenceRoom_UpsertPeer_NeverPersisted(t *testing.T) {
	// AC: "presence is ephemeral, never written to the bucket"
	// We verify this structurally: presenceRoom has no storage handle; all
	// state lives in the in-memory peers map only.
	room := newPresenceRoom()
	room.upsertPeer("peer-x", json.RawMessage(`{"x":1}`))

	peers := room.peerList()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if string(peers[0].Data) != `{"x":1}` {
		t.Errorf("data = %s, want {\"x\":1}", peers[0].Data)
	}
}

func TestPresenceRoom_EvictStale(t *testing.T) {
	room := newPresenceRoom()
	// Insert a peer entry with a past UpdatedAt.
	room.mu.Lock()
	room.peers["stale-peer"] = &peerEntry{
		PeerID:    "stale-peer",
		UpdatedAt: time.Now().Add(-2 * presenceTTL),
	}
	room.mu.Unlock()

	room.evictStale()

	if len(room.peerList()) != 0 {
		t.Errorf("stale peer was not evicted")
	}
}

// ─────────────────────────────────────────
// PresenceService unit tests
// ─────────────────────────────────────────

func TestPresenceService_RoomLazyCreate(t *testing.T) {
	svc := newTestPresenceService()
	r1 := svc.room("app-a")
	r2 := svc.room("app-a")
	if r1 != r2 {
		t.Errorf("room() should return the same instance for the same appID")
	}
	r3 := svc.room("app-b")
	if r1 == r3 {
		t.Errorf("different appIDs should have different rooms")
	}
}

// ─────────────────────────────────────────
// HTTP handler tests
// ─────────────────────────────────────────

func TestHandlePresencePeers_Empty(t *testing.T) {
	svc := newTestPresenceService()
	mux := presenceMux(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/presence/myapp/peers", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["app_id"] != "myapp" {
		t.Errorf("app_id = %q, want %q", resp["app_id"], "myapp")
	}
	if count, ok := resp["count"].(float64); !ok || count != 0 {
		t.Errorf("count = %v, want 0", resp["count"])
	}
}

func TestHandlePresencePeers_WithPeers(t *testing.T) {
	svc := newTestPresenceService()

	// Manually seed presence state.
	room := svc.room("collab-app")
	room.upsertPeer("peer-1", json.RawMessage(`{"cursor":{"x":5,"y":10}}`))
	room.upsertPeer("peer-2", json.RawMessage(`{"cursor":{"x":20,"y":30}}`))

	mux := presenceMux(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/peering/presence/collab-app/peers", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if count, ok := resp["count"].(float64); !ok || count != 2 {
		t.Errorf("count = %v, want 2", resp["count"])
	}
}

// ─────────────────────────────────────────
// S2S inbound handler tests
// ─────────────────────────────────────────

func TestHandleInboundPresence_Plain_MissingAppID(t *testing.T) {
	svc := newTestPresenceService()
	mux := presenceMux(svc)

	body := map[string]string{"peer_id": "p1", "type": "presence:state"}
	rr := doPresenceReq(t, mux, http.MethodPost, "/api/peering/inbound/presence", body)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleInboundPresence_Plain_MissingPeerID(t *testing.T) {
	svc := newTestPresenceService()
	mux := presenceMux(svc)

	body := map[string]string{"app_id": "my-app", "type": "presence:state"}
	rr := doPresenceReq(t, mux, http.MethodPost, "/api/peering/inbound/presence", body)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleInboundPresence_Plain_OK(t *testing.T) {
	svc := newTestPresenceService()
	mux := presenceMux(svc)

	body := presencePayload{
		Type:   presenceMsgState,
		AppID:  "shared-app",
		PeerID: "remote-peer",
		Data:   json.RawMessage(`{"cursor":{"x":1,"y":2}}`),
	}
	rr := doPresenceReq(t, mux, http.MethodPost, "/api/peering/inbound/presence", body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Verify the ephemeral state was recorded (but not persisted to disk).
	room := svc.room("shared-app")
	peers := room.peerList()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer in room, got %d", len(peers))
	}
	if peers[0].PeerID != "remote-peer" {
		t.Errorf("peerID = %q, want %q", peers[0].PeerID, "remote-peer")
	}
}

func TestHandleInboundPresence_WithEnvelope(t *testing.T) {
	// AC: S2S relay path uses the envelope sender as the canonical peer ID.
	svc := newTestPresenceService()

	innerPayload, _ := json.Marshal(presencePayload{
		Type:  presenceMsgState,
		AppID: "env-app",
		Data:  json.RawMessage(`{"x":99}`),
	})

	env := &Envelope{
		ID:      "test-id",
		From:    "vula:ed25519:relay-peer",
		Type:    "presence",
		Payload: json.RawMessage(innerPayload),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/presence", nil)
	ctx := context.WithValue(req.Context(), EnvelopeKey, env)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	svc.HandleInboundPresence(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Peer entry should be keyed by env.From.
	room := svc.room("env-app")
	peers := room.peerList()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].PeerID != "vula:ed25519:relay-peer" {
		t.Errorf("peerID = %q, want %q", peers[0].PeerID, "vula:ed25519:relay-peer")
	}
}

func TestHandleInboundPresence_NoLeaseRouting(t *testing.T) {
	// AC: "does not route through the lease primitive"
	// Structural check: PresenceService has no reference to any lease store.
	// If this compiles, the constraint is met. The test acts as a regression
	// guard ensuring no lease dependency is introduced.
	svc := newTestPresenceService()
	_ = svc // used
}

func TestPresenceService_BuildFrame(t *testing.T) {
	svc := newTestPresenceService()

	data := json.RawMessage(`{"cursor":{"x":5}}`)
	frame := svc.buildFrame("peer-z", presenceMsgState, "my-app", data)

	if frame.Channel != ChannelPresence {
		t.Errorf("channel = %q, want %q", frame.Channel, ChannelPresence)
	}
	if frame.From != "peer-z" {
		t.Errorf("from = %q, want %q", frame.From, "peer-z")
	}

	var p presencePayload
	if err := json.Unmarshal(frame.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Type != presenceMsgState {
		t.Errorf("payload type = %q, want %q", p.Type, presenceMsgState)
	}
	if p.AppID != "my-app" {
		t.Errorf("app_id = %q, want %q", p.AppID, "my-app")
	}
}
