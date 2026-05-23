// relay.go — Relay integration layer for vumail (VUMAIL-05).
//
// RelayClient implements KeyResolver and provides:
//   - PublishKey  — PUT /keys/<address> on the relay key directory.
//   - LookupKey   — GET /keys/<address>; results cached locally for 1 h.
//   - Subscribe   — WebSocket subscription to inbound mail for this address.
//
// The relay base URL defaults to DefaultRelayURL and is overridden by the
// VULOS_RELAY_URL environment variable (resolved by New / NewRelayClient).
package vumail

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ─── RelayClient ─────────────────────────────────────────────────────────────

// RelayClient is the real relay integration. It implements KeyResolver so it
// can be passed directly to Service.Send and RegisterHandlers.
type RelayClient struct {
	baseURL string
	client  *http.Client

	// key cache: address → (pubKey, expiresAt)
	cacheMu sync.RWMutex
	cache   map[string]keyCacheEntry
}

type keyCacheEntry struct {
	key       ed25519.PublicKey
	expiresAt time.Time
}

const keyCacheTTL = time.Hour

// relayKeyPayload is the JSON body for PublishKey and the JSON response for
// LookupKey from the relay key directory.
type relayKeyPayload struct {
	PublicKeyB64 string `json:"public_key_b64"`
}

// NewRelayClient constructs a RelayClient. baseURL is the relay root
// (e.g. "https://relay.vulos.org"); if empty the VULOS_RELAY_URL env var is
// consulted, then DefaultRelayURL.
func NewRelayClient(baseURL string) *RelayClient {
	if baseURL == "" {
		baseURL = os.Getenv("VULOS_RELAY_URL")
	}
	if baseURL == "" {
		baseURL = DefaultRelayURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &RelayClient{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
		cache:   make(map[string]keyCacheEntry),
	}
}

// withClient replaces the HTTP client (used in tests).
func (rc *RelayClient) withClient(c *http.Client) *RelayClient {
	rc.client = c
	return rc
}

// ─── PublishKey ───────────────────────────────────────────────────────────────

// PublishKey registers or updates the Ed25519 public key for address in the
// relay key directory via PUT /keys/<address>.
func (rc *RelayClient) PublishKey(ctx context.Context, address, pubKeyB64 string) error {
	payload, err := json.Marshal(relayKeyPayload{PublicKeyB64: pubKeyB64})
	if err != nil {
		return fmt.Errorf("relay: marshal publish payload: %w", err)
	}

	url := rc.baseURL + "/keys/" + address
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("relay: build publish request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := rc.client.Do(req)
	if err != nil {
		return fmt.Errorf("relay: publish key PUT: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("relay: publish key returned HTTP %d", resp.StatusCode)
	}

	// Warm the local cache with our own key.
	pub, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err == nil && len(pub) == ed25519.PublicKeySize {
		rc.cacheMu.Lock()
		rc.cache[address] = keyCacheEntry{key: ed25519.PublicKey(pub), expiresAt: time.Now().Add(keyCacheTTL)}
		rc.cacheMu.Unlock()
	}

	return nil
}

// ─── LookupKey ────────────────────────────────────────────────────────────────

// LookupKey resolves a recipient's Ed25519 public key from the relay key
// directory (GET /keys/<address>). Results are cached locally for 1 h.
// LookupKey implements KeyResolver.
func (rc *RelayClient) LookupKey(ctx context.Context, address string) (ed25519.PublicKey, error) {
	// 1. Check cache.
	rc.cacheMu.RLock()
	entry, ok := rc.cache[address]
	rc.cacheMu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.key, nil
	}

	// 2. Fetch from relay.
	url := rc.baseURL + "/keys/" + address
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("relay: build lookup request: %w", err)
	}

	resp, err := rc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("relay: lookup key GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("relay: key not found for %q", address)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("relay: lookup key returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, fmt.Errorf("relay: read lookup response: %w", err)
	}

	var kp relayKeyPayload
	if err := json.Unmarshal(body, &kp); err != nil {
		return nil, fmt.Errorf("relay: parse lookup response: %w", err)
	}

	pub, err := base64.StdEncoding.DecodeString(kp.PublicKeyB64)
	if err != nil {
		return nil, fmt.Errorf("relay: decode public key: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("relay: public key length %d, want %d", len(pub), ed25519.PublicKeySize)
	}

	key := ed25519.PublicKey(pub)

	// 3. Store in cache.
	rc.cacheMu.Lock()
	rc.cache[address] = keyCacheEntry{key: key, expiresAt: time.Now().Add(keyCacheTTL)}
	rc.cacheMu.Unlock()

	return key, nil
}

// ─── Subscribe ────────────────────────────────────────────────────────────────

// wsInboundMessage is the JSON frame pushed by the relay over the WebSocket.
type wsInboundMessage struct {
	// Type identifies the frame kind; only "mail" frames are acted upon.
	Type string `json:"type"`

	// Envelope is the raw JSON of the mailEnvelope for "mail" frames.
	Envelope json.RawMessage `json:"envelope"`

	// SenderPublicKeyB64 is provided by the relay for signature verification.
	// Empty means the relay is not providing it; callers skip verification.
	SenderPublicKeyB64 string `json:"sender_public_key_b64,omitempty"`
}

// Subscribe opens a WebSocket connection to
// wss://<relay>/ws/mail/<address> and dispatches inbound mail frames to
// svc.Receive. It reconnects automatically on disconnection until ctx is
// cancelled. Subscribe blocks the calling goroutine; run it in a goroutine.
func (rc *RelayClient) Subscribe(ctx context.Context, svc *Service) {
	address := ""
	if svc.identity != nil {
		address = svc.identity.Address
	}
	if address == "" {
		log.Printf("[vumail/relay] Subscribe: no identity address — aborting")
		return
	}

	wsURL := relayWSURL(rc.baseURL, address)

	backoff := time.Second
	const maxBackoff = 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			log.Printf("[vumail/relay] Subscribe: context done — stopping")
			return
		default:
		}

		log.Printf("[vumail/relay] connecting to %s", wsURL)
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
		if err != nil {
			log.Printf("[vumail/relay] dial error: %v (retry in %s)", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff = min(backoff*2, maxBackoff)
			}
			continue
		}
		backoff = time.Second // reset on successful connect

		log.Printf("[vumail/relay] subscribed for %s", address)
		rc.readLoop(ctx, conn, svc)
		conn.Close()

		log.Printf("[vumail/relay] connection closed — reconnecting")
	}
}

// readLoop reads frames from conn until it is closed or ctx is cancelled.
func (rc *RelayClient) readLoop(ctx context.Context, conn *websocket.Conn, svc *Service) {
	// close conn when ctx is cancelled so ReadMessage unblocks.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var frame wsInboundMessage
		if err := json.Unmarshal(raw, &frame); err != nil {
			log.Printf("[vumail/relay] malformed frame: %v", err)
			continue
		}
		if frame.Type != "mail" {
			// ignore presence / ping / other frames
			continue
		}

		// Resolve sender public key for signature verification (best-effort).
		var senderPub ed25519.PublicKey
		if frame.SenderPublicKeyB64 != "" {
			if b, err := base64.StdEncoding.DecodeString(frame.SenderPublicKeyB64); err == nil && len(b) == ed25519.PublicKeySize {
				senderPub = ed25519.PublicKey(b)
			}
		}

		if err := svc.Receive(ctx, []byte(frame.Envelope), senderPub); err != nil {
			log.Printf("[vumail/relay] Receive error: %v", err)
		}
	}
}

// relayWSURL converts an HTTP/HTTPS base URL to the WebSocket subscription URL.
func relayWSURL(baseURL, address string) string {
	u := baseURL
	u = strings.TrimRight(u, "/")
	// https → wss, http → ws
	if strings.HasPrefix(u, "https://") {
		u = "wss://" + u[len("https://"):]
	} else if strings.HasPrefix(u, "http://") {
		u = "ws://" + u[len("http://"):]
	}
	return u + "/ws/mail/" + address
}

// min returns the smaller of two durations.
func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
