// relay_test.go — unit tests for VUMAIL-05 RelayClient.
//
// Tests:
//   - PublishKey round-trip against a local stub relay.
//   - LookupKey returns cached key on second call (no second HTTP hit).
//   - Subscribe receives and dispatches a test mail frame.
package vumail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ─── Stub relay helpers ───────────────────────────────────────────────────────

// stubRelay is an in-process HTTP + WebSocket server that mimics the vulos-relay
// key-directory endpoints for testing.
type stubRelay struct {
	mu   sync.Mutex
	keys map[string]string // address → public_key_b64
}

var wsUpgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// newStubRelayServer starts an httptest server with stub relay behaviour and
// returns the server and the relay state. The caller must call srv.Close().
func newStubRelayServer(t *testing.T) (*httptest.Server, *stubRelay) {
	t.Helper()
	sr := &stubRelay{keys: make(map[string]string)}

	mux := http.NewServeMux()

	// PUT /keys/<address>
	mux.HandleFunc("PUT /keys/", func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Path[len("/keys/"):]
		var payload relayKeyPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		sr.mu.Lock()
		sr.keys[addr] = payload.PublicKeyB64
		sr.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	// GET /keys/<address>
	mux.HandleFunc("GET /keys/", func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Path[len("/keys/"):]
		sr.mu.Lock()
		k, ok := sr.keys[addr]
		sr.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(relayKeyPayload{PublicKeyB64: k})
	})

	// GET /ws/mail/<address> — WebSocket endpoint
	mux.HandleFunc("GET /ws/mail/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Keep the connection open until the client closes it.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	srv := httptest.NewServer(mux)
	return srv, sr
}

// ─── PublishKey ───────────────────────────────────────────────────────────────

func TestRelayClientPublishKey(t *testing.T) {
	srv, sr := newStubRelayServer(t)
	defer srv.Close()

	id, err := GenerateIdentity("publish-test@vumail.org", "pw")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	rc := NewRelayClient(srv.URL)
	if err := rc.PublishKey(context.Background(), id.Address, id.PublicKeyB64); err != nil {
		t.Fatalf("PublishKey: %v", err)
	}

	sr.mu.Lock()
	got, ok := sr.keys[id.Address]
	sr.mu.Unlock()

	if !ok {
		t.Fatal("key not stored in stub relay")
	}
	if got != id.PublicKeyB64 {
		t.Errorf("stored key = %q, want %q", got, id.PublicKeyB64)
	}
}

func TestRelayClientPublishKeyWarmsCache(t *testing.T) {
	srv, _ := newStubRelayServer(t)
	defer srv.Close()

	id, _ := GenerateIdentity("cache-warm@vumail.org", "pw")
	rc := NewRelayClient(srv.URL)

	if err := rc.PublishKey(context.Background(), id.Address, id.PublicKeyB64); err != nil {
		t.Fatalf("PublishKey: %v", err)
	}

	// The cache should be warm — close the server to prove no HTTP call is made.
	srv.Close()

	key, err := rc.LookupKey(context.Background(), id.Address)
	if err != nil {
		t.Fatalf("LookupKey after PublishKey (cache hit expected): %v", err)
	}
	if len(key) == 0 {
		t.Error("returned key is empty")
	}
}

// ─── LookupKey ────────────────────────────────────────────────────────────────

func TestRelayClientLookupKey(t *testing.T) {
	srv, sr := newStubRelayServer(t)
	defer srv.Close()

	id, _ := GenerateIdentity("lookup-test@vumail.org", "pw")

	// Seed the relay directly.
	sr.mu.Lock()
	sr.keys[id.Address] = id.PublicKeyB64
	sr.mu.Unlock()

	rc := NewRelayClient(srv.URL)

	key, err := rc.LookupKey(context.Background(), id.Address)
	if err != nil {
		t.Fatalf("LookupKey: %v", err)
	}

	expected, _ := id.PublicKey()
	if string(key) != string(expected) {
		t.Error("returned key does not match expected")
	}
}

func TestRelayClientLookupKeyCache(t *testing.T) {
	srv, sr := newStubRelayServer(t)
	defer srv.Close()

	id, _ := GenerateIdentity("cache-test@vumail.org", "pw")
	sr.mu.Lock()
	sr.keys[id.Address] = id.PublicKeyB64
	sr.mu.Unlock()

	callCount := 0
	// Wrap the server's handler to count hits.
	countingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && len(r.URL.Path) > len("/keys/") {
			callCount++
		}
		srv.Config.Handler.ServeHTTP(w, r)
	}))
	defer countingSrv.Close()

	// Seed counting server's relay with the same key.
	// We re-use the stub relay's backing store but the counting server proxies to srv.
	// Simpler: use a fresh stub relay.
	srv2, sr2 := newStubRelayServer(t)
	defer srv2.Close()
	sr2.mu.Lock()
	sr2.keys[id.Address] = id.PublicKeyB64
	sr2.mu.Unlock()

	rc := NewRelayClient(srv2.URL)

	// First call — should hit the network.
	k1, err := rc.LookupKey(context.Background(), id.Address)
	if err != nil {
		t.Fatalf("LookupKey first: %v", err)
	}

	// Close the backing server to confirm the second call uses cache.
	srv2.Close()

	k2, err := rc.LookupKey(context.Background(), id.Address)
	if err != nil {
		t.Fatalf("LookupKey second (cache): %v", err)
	}

	if string(k1) != string(k2) {
		t.Error("cached key does not match first key")
	}
}

func TestRelayClientLookupKeyNotFound(t *testing.T) {
	srv, _ := newStubRelayServer(t)
	defer srv.Close()

	rc := NewRelayClient(srv.URL)
	_, err := rc.LookupKey(context.Background(), "nobody@vumail.org")
	if err == nil {
		t.Fatal("expected error for unknown address, got nil")
	}
}

// ─── relayWSURL ───────────────────────────────────────────────────────────────

func TestRelayWSURL(t *testing.T) {
	cases := []struct {
		base, addr, want string
	}{
		{"https://relay.vulos.org", "alice@vumail.org", "wss://relay.vulos.org/ws/mail/alice@vumail.org"},
		{"http://localhost:9000", "bob@vumail.org", "ws://localhost:9000/ws/mail/bob@vumail.org"},
		{"http://localhost:9000/", "bob@vumail.org", "ws://localhost:9000/ws/mail/bob@vumail.org"},
	}
	for _, c := range cases {
		got := relayWSURL(c.base, c.addr)
		if got != c.want {
			t.Errorf("relayWSURL(%q, %q) = %q, want %q", c.base, c.addr, got, c.want)
		}
	}
}

// ─── Subscribe ────────────────────────────────────────────────────────────────

func TestRelayClientSubscribeDispatch(t *testing.T) {
	// Build a stub relay that immediately pushes one mail frame then stays open.
	sender, _ := GenerateIdentity("ws-sender@vumail.org", "pw")
	recipient, _ := GenerateIdentity("ws-recipient@vumail.org", "pw2")
	recipientPub, _ := recipient.PublicKey()

	// Encrypt a body for the recipient.
	nonce, ct, _ := encryptBody([]byte("hello via websocket"), recipientPub)
	env := mailEnvelope{
		Version:        1,
		From:           sender.Address,
		To:             recipient.Address,
		Subject:        "WS test",
		BodyCiphertext: base64.StdEncoding.EncodeToString(ct),
		SenderNonce:    base64.StdEncoding.EncodeToString(nonce),
		Timestamp:      "2026-01-01T00:00:00Z",
	}
	_ = signEnvelope(&env, sender.priv)
	envJSON, _ := json.Marshal(env)
	senderPub, _ := sender.PublicKey()

	frame := wsInboundMessage{
		Type:               "mail",
		Envelope:           json.RawMessage(envJSON),
		SenderPublicKeyB64: base64.StdEncoding.EncodeToString(senderPub),
	}
	frameJSON, _ := json.Marshal(frame)

	// frameSent is closed by the WS handler after writing the frame.
	// We wait for the server to have sent it, then give Subscribe a moment.
	frameSent := make(chan struct{})

	// Custom WS handler: push one frame, signal, then hold open.
	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/ws/mail/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, frameJSON)
		select {
		case <-frameSent:
		default:
			close(frameSent)
		}
		// Hold open until client disconnects.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	srv := httptest.NewServer(wsMux)
	defer srv.Close()

	// Use an in-memory store backed by a temp SQLite DB to verify persistence.
	dir := t.TempDir()
	store, err := NewStore(dir + "/ws_test.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	svc := New(recipient, store, "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rc := NewRelayClient(srv.URL)

	go rc.Subscribe(ctx, svc)

	// Wait for the server to have sent the frame and a short grace period.
	select {
	case <-frameSent:
	case <-ctx.Done():
		t.Fatal("timeout waiting for WS frame to be sent")
	}
	// Allow readLoop goroutine to process the frame.
	time.Sleep(200 * time.Millisecond)

	msgs, err := store.listMailbox()
	if err != nil {
		t.Fatalf("listMailbox: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("Subscribe did not dispatch the inbound message to the mailbox")
	}
	if msgs[0].FromAddress != sender.Address {
		t.Errorf("from_address = %q, want %q", msgs[0].FromAddress, sender.Address)
	}
}
