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

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// dropNoopMDNS is a fake mDNS connection that never does network I/O.
type dropNoopMDNS struct{ closed bool }

func (n *dropNoopMDNS) Close() error {
	n.closed = true
	return nil
}

// dropNoopMDNSFactory returns a fake mDNS conn — avoids real multicast sockets
// in CI/unit-test environments where mDNS is unavailable.
func dropNoopMDNSFactory(_ string) (dropMDNSConn, error) {
	return &dropNoopMDNS{}, nil
}

// dropFakeContacts implements DropContactChecker.
type dropFakeContacts struct {
	approved map[string]bool
}

func (f *dropFakeContacts) IsApproved(vulosID string) bool {
	return f.approved[vulosID]
}

// dropFakeMedia implements DropMediaSender.
type dropFakeMedia struct {
	calls []dropFakeMediaCall
}

type dropFakeMediaCall struct {
	targetAddr string
	mediaPath  string
	mimeType   string
}

func (f *dropFakeMedia) SendFile(_ context.Context, targetAddr, mediaPath, mimeType string) (string, error) {
	f.calls = append(f.calls, dropFakeMediaCall{targetAddr, mediaPath, mimeType})
	return "txn-001", nil
}

func (f *dropFakeMedia) ReceiveFile(_ context.Context, downloadURL, fileName, mimeType string) error {
	f.calls = append(f.calls, dropFakeMediaCall{downloadURL, fileName, mimeType})
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func dropNewTestService(t *testing.T, contacts DropContactChecker, media DropMediaSender) *DropService {
	t.Helper()
	svc := NewDropService("vulos:ed25519:testkey", "Tester", contacts, media)
	svc.mdnsNew = dropNoopMDNSFactory
	return svc
}

func dropPost(t *testing.T, mux *http.ServeMux, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func dropGet(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func dropPut(t *testing.T, mux *http.ServeMux, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// Tests: mDNS advertisement
// ---------------------------------------------------------------------------

// TestDropAdvertisingStartStop verifies that Start spins up the mDNS server
// and Stop shuts it down, using the no-op factory so no real socket is opened.
func TestDropAdvertisingStartStop(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc.Start(ctx)
	// Default discoverability is "peers" → should be advertising.
	if !svc.DropIsAdvertising() {
		t.Fatal("expected mDNS to be advertising after Start()")
	}

	svc.Stop()
	if svc.DropIsAdvertising() {
		t.Fatal("expected mDNS to stop after Stop()")
	}
}

// TestDropAdvertisingNobody verifies that Start does NOT open an mDNS server
// when discoverability is set to "nobody".
func TestDropAdvertisingNobody(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	svc.settings.Discoverability = dropDiscoverNobody

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)

	if svc.DropIsAdvertising() {
		t.Fatal("expected no mDNS advertising when discoverability=nobody")
	}
}

// ---------------------------------------------------------------------------
// Tests: discoverability filter
// ---------------------------------------------------------------------------

// TestDropNearbyEveryoneSeesAll verifies that "everyone" mode returns all
// discovered peers regardless of contact status.
func TestDropNearbyEveryoneSeesAll(t *testing.T) {
	contacts := &dropFakeContacts{approved: map[string]bool{"peer-a": true}}
	svc := dropNewTestService(t, contacts, nil)
	svc.settings.Discoverability = dropDiscoverEveryone

	svc.DropRegisterPeer("peer-a", "Alice", "192.168.1.2:8080")
	svc.DropRegisterPeer("peer-b", "Bob", "192.168.1.3:8080")

	nearby := svc.DropNearby()
	if len(nearby) != 2 {
		t.Fatalf("everyone mode: want 2 peers, got %d", len(nearby))
	}
}

// TestDropNearbyPeersOnlyShowsContacts verifies that "peers" mode filters out
// non-contacts.
func TestDropNearbyPeersOnlyShowsContacts(t *testing.T) {
	contacts := &dropFakeContacts{approved: map[string]bool{"peer-a": true}}
	svc := dropNewTestService(t, contacts, nil)
	svc.settings.Discoverability = dropDiscoverPeers

	svc.DropRegisterPeer("peer-a", "Alice", "192.168.1.2:8080")
	svc.DropRegisterPeer("peer-b", "Bob", "192.168.1.3:8080")

	nearby := svc.DropNearby()
	if len(nearby) != 1 || nearby[0].VulosID != "peer-a" {
		t.Fatalf("peers mode: want only peer-a, got %v", nearby)
	}
}

// TestDropNearbyNobodyHidesAll verifies that "nobody" mode returns no peers.
func TestDropNearbyNobodyHidesAll(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	svc.settings.Discoverability = dropDiscoverNobody
	svc.DropRegisterPeer("peer-a", "Alice", "192.168.1.2:8080")

	nearby := svc.DropNearby()
	if len(nearby) != 0 {
		t.Fatalf("nobody mode: want 0 peers, got %d", len(nearby))
	}
}

// ---------------------------------------------------------------------------
// Tests: HTTP handler — /api/peering/drop/nearby
// ---------------------------------------------------------------------------

func TestDropHandleNearbyHTTP(t *testing.T) {
	contacts := &dropFakeContacts{approved: map[string]bool{"peer-a": true}}
	svc := dropNewTestService(t, contacts, nil)
	svc.settings.Discoverability = dropDiscoverEveryone
	svc.DropRegisterPeer("peer-a", "Alice", "192.168.1.2:8080")

	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	rr := dropGet(t, mux, "/api/peering/drop/nearby")
	if rr.Code != http.StatusOK {
		t.Fatalf("nearby: want 200, got %d", rr.Code)
	}
	var peers []DropPeer
	if err := json.NewDecoder(rr.Body).Decode(&peers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("want 1 peer, got %d", len(peers))
	}
	if peers[0].VulosID != "peer-a" {
		t.Fatalf("want peer-a, got %s", peers[0].VulosID)
	}
}

// TestDropHandleNearbyMethodNotAllowed verifies that non-GET requests are rejected.
func TestDropHandleNearbyMethodNotAllowed(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/drop/nearby", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: HTTP handler — /api/peering/drop/settings
// ---------------------------------------------------------------------------

func TestDropHandleSettingsGetPut(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	// GET initial settings
	rr := dropGet(t, mux, "/api/peering/drop/settings")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET settings: want 200, got %d", rr.Code)
	}

	// PUT — change to "everyone"
	rr = dropPut(t, mux, "/api/peering/drop/settings",
		dropSettingsUpdate{Discoverability: dropDiscoverEveryone})
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT settings: want 200, got %d", rr.Code)
	}

	if svc.DropSettings().Discoverability != dropDiscoverEveryone {
		t.Fatal("settings not updated")
	}
}

func TestDropHandleSettingsInvalidDiscoverability(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	rr := dropPut(t, mux, "/api/peering/drop/settings",
		map[string]string{"discoverability": "kinda-visible"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

// TestDropSettingsRestartsMDNS verifies that updating discoverability from
// "nobody" to "everyone" restarts the mDNS server.
func TestDropSettingsRestartsMDNS(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	svc.settings.Discoverability = dropDiscoverNobody

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)

	if svc.DropIsAdvertising() {
		t.Fatal("should not be advertising with nobody")
	}

	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	dropPut(t, mux, "/api/peering/drop/settings",
		dropSettingsUpdate{Discoverability: dropDiscoverEveryone})

	if !svc.DropIsAdvertising() {
		t.Fatal("should be advertising after switching to everyone")
	}
}

// ---------------------------------------------------------------------------
// Tests: HTTP handler — inbound drop + decide
// ---------------------------------------------------------------------------

// TestDropInboundAndDeclineHTTP verifies the inbound → pending → decline flow.
func TestDropInboundAndDeclineHTTP(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	req := dropInboundRequest{
		TransferID:  "tx-decline-01",
		FromVulosID: "stranger",
		DisplayName: "Stranger",
		FileName:    "malware.exe",
		FileSize:    1024,
		MimeType:    "application/octet-stream",
		DownloadURL: "http://evil.example.com/malware.exe",
	}
	rr := dropPost(t, mux, "/api/peering/inbound/drop", req)
	if rr.Code != http.StatusOK {
		t.Fatalf("inbound: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if resp["status"] != "pending" {
		t.Fatalf("want pending, got %q", resp["status"])
	}

	// Decline
	rr = dropPost(t, mux, "/api/peering/drop/decide", dropDecision{
		TransferID: "tx-decline-01",
		Accept:     false,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("decide: want 200, got %d", rr.Code)
	}
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if resp["status"] != "declined" {
		t.Fatalf("want declined, got %q", resp["status"])
	}
}

// TestDropInboundAutoAcceptContact verifies that transfers from approved
// contacts are accepted automatically when auto-accept is enabled.
func TestDropInboundAutoAcceptContact(t *testing.T) {
	contacts := &dropFakeContacts{approved: map[string]bool{"friend-vulos-id": true}}
	media := &dropFakeMedia{}
	svc := dropNewTestService(t, contacts, media)
	svc.DropSetAutoAcceptContacts(true)

	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	req := dropInboundRequest{
		TransferID:  "tx-auto-01",
		FromVulosID: "friend-vulos-id",
		DisplayName: "Friend",
		FileName:    "photo.jpg",
		FileSize:    512000,
		MimeType:    "image/jpeg",
		DownloadURL: "http://friend.vulos.org/api/peering/media/photo.jpg",
	}
	rr := dropPost(t, mux, "/api/peering/inbound/drop", req)
	if rr.Code != http.StatusOK {
		t.Fatalf("inbound: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if resp["status"] != "accepted" {
		t.Fatalf("want auto-accepted, got %q", resp["status"])
	}
}

// TestDropInboundDecideUnknownTransfer verifies that deciding on a non-existent
// transfer returns 404.
func TestDropInboundDecideUnknownTransfer(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	rr := dropPost(t, mux, "/api/peering/drop/decide", dropDecision{
		TransferID: "does-not-exist",
		Accept:     true,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

// TestDropInboundMissingFields verifies that missing required fields return 400.
func TestDropInboundMissingFields(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	rr := dropPost(t, mux, "/api/peering/inbound/drop", dropInboundRequest{
		// TransferID and FromVulosID deliberately omitted
		FileName: "test.txt",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: HTTP handler — send
// ---------------------------------------------------------------------------

// TestDropHandleSendPeerNotFound verifies 404 when the peer is not in the
// nearby list and no TargetAddr is given.
func TestDropHandleSendPeerNotFound(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	rr := dropPost(t, mux, "/api/peering/drop/send", dropSendRequest{
		TargetVulosID: "ghost-peer",
		MediaPath:     "/tmp/file.txt",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

// TestDropHandleSendMediaPathMissing verifies 400 when media_path not given.
func TestDropHandleSendMediaPathMissing(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	rr := dropPost(t, mux, "/api/peering/drop/send", dropSendRequest{
		TargetVulosID: "peer-a",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDropHandleSendWithExplicitAddr verifies that supplying TargetAddr
// bypasses the nearby list lookup (fails because the media_path doesn't exist
// on disk, yielding 400 rather than 404 "peer not found").
func TestDropHandleSendWithExplicitAddrNoMedia(t *testing.T) {
	media := &dropFakeMedia{}
	svc := dropNewTestService(t, nil, media)
	mux := http.NewServeMux()
	RegisterDropHandlers(mux, svc)

	rr := dropPost(t, mux, "/api/peering/drop/send", dropSendRequest{
		TargetVulosID: "peer-x",
		TargetAddr:    "http://10.0.0.2:8080",
		MediaPath:     "/nonexistent/path/file.txt",
	})
	// Expect 400 (os.Stat fails on missing file) not 404 (peer not found)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 (file not found), got %d: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Tests: peer registration and IsContact flag
// ---------------------------------------------------------------------------

func TestDropRegisterPeerSetsIsContact(t *testing.T) {
	contacts := &dropFakeContacts{approved: map[string]bool{"known-peer": true}}
	svc := dropNewTestService(t, contacts, nil)
	svc.settings.Discoverability = dropDiscoverEveryone

	svc.DropRegisterPeer("known-peer", "Alice", "10.0.0.1:8080")
	svc.DropRegisterPeer("unknown-peer", "Eve", "10.0.0.2:8080")

	nearby := svc.DropNearby()
	m := make(map[string]bool)
	for _, p := range nearby {
		m[p.VulosID] = p.IsContact
	}
	if !m["known-peer"] {
		t.Error("known-peer should be marked IsContact=true")
	}
	if m["unknown-peer"] {
		t.Error("unknown-peer should be marked IsContact=false")
	}
}

// ---------------------------------------------------------------------------
// Tests: stale peer pruning
// ---------------------------------------------------------------------------

func TestDropStalePeerPruning(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	svc.settings.Discoverability = dropDiscoverEveryone

	// Inject a peer with a far-past discovery time.
	svc.mu.Lock()
	svc.peers["stale-peer"] = &DropPeer{
		VulosID:      "stale-peer",
		DisplayName:  "Stale",
		Addr:         "10.0.0.99:8080",
		DiscoveredAt: time.Now().Add(-10 * time.Minute),
	}
	svc.mu.Unlock()

	// Run prune manually (simulating the ticker firing).
	threshold := time.Now().Add(-5 * time.Minute)
	svc.mu.Lock()
	for id, p := range svc.peers {
		if p.DiscoveredAt.Before(threshold) {
			delete(svc.peers, id)
		}
	}
	svc.mu.Unlock()

	if len(svc.DropNearby()) != 0 {
		t.Fatal("stale peer should have been pruned")
	}
}

// ---------------------------------------------------------------------------
// Tests: auto-accept toggle
// ---------------------------------------------------------------------------

func TestDropAutoAcceptToggle(t *testing.T) {
	svc := dropNewTestService(t, nil, nil)
	if svc.DropAutoAcceptContacts() {
		t.Fatal("default auto-accept should be false")
	}
	svc.DropSetAutoAcceptContacts(true)
	if !svc.DropAutoAcceptContacts() {
		t.Fatal("auto-accept should be true after enable")
	}
}

// ---------------------------------------------------------------------------
// Tests: hostname derivation
// ---------------------------------------------------------------------------

func TestDropHostname(t *testing.T) {
	cases := []struct {
		vulosID string
		want    string
	}{
		{"short", "vulos-short._vulos-drop._tcp.local"},
		{"vulos:ed25519:abcdefghijklmnopqrstuvwxyz", "vulos-klmnopqrstuvwxyz._vulos-drop._tcp.local"},
	}
	for _, tc := range cases {
		got := dropHostname(tc.vulosID)
		if got != tc.want {
			t.Errorf("dropHostname(%q) = %q, want %q", tc.vulosID, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: mDNS — skip if unavailable (integration guard)
// ---------------------------------------------------------------------------

// TestDropMDNSSkipIfUnavailable attempts a real mDNS server setup and skips
// gracefully if multicast sockets are not available (e.g., CI environment).
func TestDropMDNSSkipIfUnavailable(t *testing.T) {
	conn, err := dropDefaultMDNSFactory("vulos-test._vulos-drop._tcp.local")
	if err != nil {
		t.Skipf("mDNS unavailable (expected in CI): %v", err)
	}
	defer conn.Close()
	// If we got here, the real mDNS server started successfully.
	t.Log("real mDNS server started successfully")
}
