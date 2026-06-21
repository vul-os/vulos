// relay.go — relay peers: deposit / pickup / ack protocol + config/store (PEER-38).
//
// # Overview
//
// A relay peer is any Vula instance that both parties trust, willing to hold
// encrypted blobs in transit until the recipient comes online.  The relay
// never decrypts — it stores opaque ciphertext indexed by the recipient's
// Vula ID.
//
// # Protocol
//
//	POST /api/peering/relay/deposit
//	  Body: { to, blob_b64, ttl_hours, nonce, signature }
//	  → relay stores blob, indexed by recipient Vula ID
//
//	GET /api/peering/relay/pickup
//	  Header: Authorization: Vula-Relay <vula_id>.<timestamp_unix>.<base64url_sig>
//	  → returns all pending blobs for the authenticated Vula ID
//
//	POST /api/peering/relay/ack
//	  Body: { blob_ids: [...] }
//	  → recipient confirms receipt; relay deletes stored blobs
//
// # Relay role config
//
// The relay peer opts in via config.json:
//
//	~/.vulos/peering/relay/config.json
//	{
//	  "enabled":      true,
//	  "capacity_mb":  500,   (total storage cap for all recipients)
//	  "ttl_hours":    72,    (default TTL; capped at 168 h / 7 days)
//	  "allowed":      ["vula:ed25519:..."]  (empty = mutual-trust check only)
//	}
//
// # Storage
//
//	~/.vulos/peering/relay/
//	  ├── config.json
//	  └── store/
//	      └── <sanitised-recipient-vula-id>/
//	          └── <blob-id>.json    (RelayBlob)
//
// # Limits (per relay peer)
//
//   - Max stored per recipient: 100 MB (configurable up to capacity_mb)
//   - Max TTL: 72 h default, 168 h (7 days) maximum
//   - Max single blob: 25 MB
//   - Rate limit: 100 deposits/hour per sender
//
// # Trust
//
// Deposits are accepted only from senders whose Ed25519 signature over the
// canonical deposit request is valid AND who are approved in the local
// contacts store (mutual trust).  Pickup is authenticated by an Ed25519
// signature over "<vula_id>.<timestamp_unix>".
//
// The relay NEVER calls DecryptFromPeer or touches the crypto layer — all
// blobs remain opaque.
//
// # Exported API
//
//	store, err := peering.NewRelayStore(home, contacts)
//	store.Start(ctx)                         // background TTL reaper
//
//	// RegisterRelayHandlers is the only exported HTTP wiring function.
//	peering.RegisterRelayHandlers(mux, store)
package peering

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/cpbilling"
)

// ─── Relay limits (package-level constants) ───────────────────────────────────

const (
	// relayMaxBlobBytes is the maximum size of a single deposited blob (25 MB).
	relayMaxBlobBytes int64 = 25 * 1024 * 1024

	// relayMaxPerRecipientBytes is the maximum total stored bytes per recipient
	// (100 MB).
	relayMaxPerRecipientBytes int64 = 100 * 1024 * 1024

	// relayDefaultTTLHours is the default blob TTL (72 hours).
	relayDefaultTTLHours = 72

	// relayMaxTTLHours is the hard cap on blob TTL (7 days = 168 hours).
	relayMaxTTLHours = 168

	// relayMaxDepositsPerHour is the maximum deposits per sender per hour.
	relayMaxDepositsPerHour = 100

	// relayPickupTimestampTolerance is the maximum skew accepted on a pickup
	// timestamp.  Requests outside ±5 minutes are rejected as replays.
	relayPickupTimestampTolerance = 5 * time.Minute

	// relayReaperInterval is how often the background reaper removes expired
	// blobs.
	relayReaperInterval = 15 * time.Minute

	// relayRequestBodyLimit is the maximum HTTP body the relay handlers will
	// read (26 MB — slightly above the 25 MB blob limit to accommodate JSON
	// overhead).
	relayRequestBodyLimit int64 = 27 * 1024 * 1024

	// relayPickupBodyLimit is the maximum body for pickup ACK requests (1 MiB).
	relayPickupBodyLimit int64 = 1 << 20
)

// ─── Config ───────────────────────────────────────────────────────────────────

// RelayConfig is the relay operator's configuration stored at
// ~/.vulos/peering/relay/config.json.
type RelayConfig struct {
	// Enabled controls whether this node accepts relay deposits. Default false.
	Enabled bool `json:"enabled"`

	// CapacityMB is the total storage cap (in MiB) for all relay blobs.
	// 0 means the relay uses the package default (500 MiB).
	CapacityMB int64 `json:"capacity_mb"`

	// TTLHours is the default TTL (in hours) applied when the depositor does
	// not request a shorter one. Capped to relayMaxTTLHours.
	TTLHours int `json:"ttl_hours"`

	// Allowed is a list of Vula IDs explicitly allowed to use this relay as
	// sender. Empty means any mutually-approved contact may deposit.
	Allowed []string `json:"allowed,omitempty"`
}

// relayDefaultCapacityMB is the total capacity when RelayConfig.CapacityMB is
// zero.
const relayDefaultCapacityMB int64 = 500

// effectiveCapacityBytes returns the configured capacity in bytes.
func (c *RelayConfig) effectiveCapacityBytes() int64 {
	mb := c.CapacityMB
	if mb <= 0 {
		mb = relayDefaultCapacityMB
	}
	return mb * 1024 * 1024
}

// effectiveTTL returns a TTL duration for a blob.
//
// Resolution order:
//  1. Use requestedHours if > 0 (sender-specified TTL).
//  2. Fall back to c.TTLHours if requestedHours is 0.
//  3. Fall back to relayDefaultTTLHours if c.TTLHours is also 0.
//  4. Clamp the result to relayMaxTTLHours (hard cap, 7 days).
//
// This means a depositor may request any TTL up to relayMaxTTLHours;
// requests that exceed the cap are silently clamped rather than rejected.
func (c *RelayConfig) effectiveTTL(requestedHours int) time.Duration {
	hours := requestedHours
	if hours <= 0 {
		hours = c.TTLHours
	}
	if hours <= 0 {
		hours = relayDefaultTTLHours
	}
	if hours > relayMaxTTLHours {
		hours = relayMaxTTLHours
	}
	return time.Duration(hours) * time.Hour
}

// ─── RelayBlob — on-disk record ───────────────────────────────────────────────

// RelayBlob is the JSON record written to disk for each deposited blob.
// The relay stores the ciphertext verbatim — it never inspects BlobB64.
type RelayBlob struct {
	// ID is the unique blob identifier (opaque string chosen by the sender,
	// validated to be non-empty and filesystem-safe by the relay).
	ID string `json:"id"`

	// RecipientVulaID is the intended recipient's Vula ID.
	RecipientVulaID string `json:"recipient_vula_id"`

	// SenderVulaID is the depositor's Vula ID (verified by signature).
	SenderVulaID string `json:"sender_vula_id"`

	// BlobB64 is the base64-standard-encoded ciphertext.  The relay treats
	// this as an opaque byte string and never decrypts it.
	BlobB64 string `json:"blob_b64"`

	// BlobSize is len(decoded BlobB64) in bytes, cached to avoid re-decoding
	// for size accounting.
	BlobSize int64 `json:"blob_size"`

	// DepositedAt is when the blob was stored.
	DepositedAt time.Time `json:"deposited_at"`

	// ExpiresAt is when the relay will discard the blob if not ACKed.
	ExpiresAt time.Time `json:"expires_at"`
}

// ─── RelayStore ───────────────────────────────────────────────────────────────

// RelayStore manages relay configuration, blob storage, and rate limiting.
// Obtain one via NewRelayStore. The zero value is not usable.
type RelayStore struct {
	root     string        // ~/.vulos/peering/relay/
	storeDir string        // ~/.vulos/peering/relay/store/
	contacts *ContactStore // for mutual-trust check

	mu     sync.RWMutex
	config RelayConfig

	// rateMu guards senderDeposits.
	rateMu         sync.Mutex
	senderDeposits map[string][]time.Time // sender → timestamps of deposits in last hour

	// billing GATES relay deposits on suspension and METERS relayed bytes
	// against cp. Nil or disabled (CP_URL unset) = standalone OS: the relay is
	// ungated/unmetered. The account key is the sender's VulaID (cp maps it).
	// cp does not yet return relay-specific per-tier caps, so this enforces
	// suspension only (see WithBilling doc).
	billing *cpbilling.Client
}

// WithBilling wires a cp billing client so relay deposits are gated on the
// sender's suspension state and the relayed byte count is metered. A
// nil/disabled client is a no-op (standalone OS). Returns rs for chaining.
//
// CP CONTRACT NOTE: cp does not yet return relay-specific per-tier caps
// (e.g. relay_enabled, relay_bytes_budget). This layer therefore enforces only
// `suspended` and emits per-deposit usage (kind=relay_bytes, bytes=blob size)
// so cp can bill on volume. For full per-tier enforcement cp should add relay
// caps to /api/entitlements.
func (rs *RelayStore) WithBilling(b *cpbilling.Client) *RelayStore {
	rs.billing = b
	return rs
}

// NewRelayStore creates a RelayStore backed by
// filepath.Join(home, ".vulos", "peering", "relay").
//
// contacts must be non-nil and is used for mutual-trust verification.
// The relay directory tree is created idempotently.
func NewRelayStore(home string, contacts *ContactStore) (*RelayStore, error) {
	if contacts == nil {
		return nil, errors.New("peering/relay: contacts must not be nil")
	}
	root := filepath.Join(home, ".vulos", "peering", "relay")
	storeDir := filepath.Join(root, "store")
	for _, dir := range []string{root, storeDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("peering/relay: mkdir %s: %w", dir, err)
		}
	}

	rs := &RelayStore{
		root:           root,
		storeDir:       storeDir,
		contacts:       contacts,
		senderDeposits: make(map[string][]time.Time),
	}

	if err := rs.loadConfig(); err != nil {
		return nil, fmt.Errorf("peering/relay: load config: %w", err)
	}

	return rs, nil
}

// Start launches the background blob-reaper goroutine. It runs until ctx is
// cancelled. Call Start exactly once after construction.
func (rs *RelayStore) Start(ctx context.Context) {
	go rs.reapLoop(ctx)
}

// ─── Config persistence ───────────────────────────────────────────────────────

// loadConfig reads config.json. If the file does not exist a default
// (disabled) config is written and used.
func (rs *RelayStore) loadConfig() error {
	path := filepath.Join(rs.root, "config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		rs.config = RelayConfig{Enabled: false}
		return rs.persistConfig()
	}
	if err != nil {
		return fmt.Errorf("read config.json: %w", err)
	}
	var cfg RelayConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config.json: %w", err)
	}
	rs.config = cfg
	return nil
}

// persistConfig writes config.json atomically.
func (rs *RelayStore) persistConfig() error {
	path := filepath.Join(rs.root, "config.json")
	data, err := json.MarshalIndent(rs.config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return relayAtomicWrite(rs.root, path, data)
}

// GetConfig returns a snapshot of the relay configuration.
func (rs *RelayStore) GetConfig() RelayConfig {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.config
}

// SetConfig replaces the relay configuration and persists it.
func (rs *RelayStore) SetConfig(cfg RelayConfig) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.config = cfg
	return rs.persistConfig()
}

// ─── Deposit ──────────────────────────────────────────────────────────────────

// relayDepositRequest is the JSON body of POST /api/peering/relay/deposit.
type relayDepositRequest struct {
	// To is the intended recipient's Vula ID.
	To string `json:"to"`

	// BlobID is a sender-chosen unique identifier for this blob.
	// Must be non-empty and contain only filesystem-safe characters.
	BlobID string `json:"blob_id"`

	// BlobB64 is the base64-standard-encoded ciphertext (max 25 MB decoded).
	BlobB64 string `json:"blob_b64"`

	// TTLHours is the requested TTL in hours. 0 uses the relay's default.
	// Capped to relayMaxTTLHours by the relay.
	TTLHours int `json:"ttl_hours,omitempty"`

	// SenderVulaID is the depositor's Vula ID (must match the envelope From).
	SenderVulaID string `json:"sender_vula_id"`

	// Nonce is a hex-encoded random nonce (16 bytes minimum) included in the
	// signed payload to prevent replay attacks.
	Nonce string `json:"nonce"`

	// Signature is the base64url (no-padding) Ed25519 signature over the
	// canonical bytes of this object with the "signature" field absent, signed
	// with the sender's private key.  Verification uses the public key derived
	// from SenderVulaID.
	Signature string `json:"signature"`
}

// Deposit stores an encrypted blob on behalf of a sender for a recipient.
//
// Validations:
//  1. Relay must be enabled.
//  2. Signature over the canonical request is valid.
//  3. Sender is mutually approved in contacts (IsApproved).
//  4. Sender is in the Allowed list (if Allowed is non-empty).
//  5. Rate limit: ≤ relayMaxDepositsPerHour per sender.
//  6. BlobB64 decoded size ≤ relayMaxBlobBytes.
//  7. Total stored per recipient ≤ relayMaxPerRecipientBytes.
//  8. Total relay store ≤ effectiveCapacityBytes.
func (rs *RelayStore) Deposit(req relayDepositRequest) error {
	rs.mu.RLock()
	cfg := rs.config
	rs.mu.RUnlock()

	if !cfg.Enabled {
		return errors.New("peering/relay: relay is not enabled on this node")
	}

	// 1. Validate fields.
	if req.To == "" {
		return errors.New("peering/relay: deposit: 'to' must not be empty")
	}
	if req.BlobID == "" {
		return errors.New("peering/relay: deposit: 'blob_id' must not be empty")
	}
	if !relayIsSafeID(req.BlobID) {
		return errors.New("peering/relay: deposit: 'blob_id' contains unsafe characters")
	}
	if req.SenderVulaID == "" {
		return errors.New("peering/relay: deposit: 'sender_vula_id' must not be empty")
	}
	if req.Nonce == "" {
		return errors.New("peering/relay: deposit: 'nonce' must not be empty")
	}
	if req.BlobB64 == "" {
		return errors.New("peering/relay: deposit: 'blob_b64' must not be empty")
	}

	// 2. Signature verification.
	if err := relayVerifyDepositSig(req); err != nil {
		return fmt.Errorf("peering/relay: deposit: %w", err)
	}

	// 3. Mutual-trust check.
	if !rs.contacts.IsApproved(req.SenderVulaID) {
		return fmt.Errorf("peering/relay: deposit: sender %q is not approved", req.SenderVulaID)
	}

	// 4. Allowed-list check (if configured).
	if len(cfg.Allowed) > 0 && !relayInAllowedList(req.SenderVulaID, cfg.Allowed) {
		return fmt.Errorf("peering/relay: deposit: sender %q is not in the allowed list", req.SenderVulaID)
	}

	// 5. Rate limit.
	if err := rs.checkRateLimit(req.SenderVulaID); err != nil {
		return err
	}

	// 5b. BILLING GATE (surface 4: relay). Refuse a deposit from a suspended
	// account (authoritative); fail-open/degraded on a cold cp outage. No-op
	// when billing is disabled (standalone OS). Account key = sender VulaID.
	if rs.billing.Enabled() {
		if d := rs.billing.Gate(context.Background(), req.SenderVulaID, cpbilling.ProductRelay); !d.Allowed {
			return fmt.Errorf("peering/relay: deposit: refused: %s", d.Reason)
		}
	}

	// 6. Blob size check.
	blobBytes, err := base64.StdEncoding.DecodeString(req.BlobB64)
	if err != nil {
		return fmt.Errorf("peering/relay: deposit: blob_b64 is not valid base64: %w", err)
	}
	blobSize := int64(len(blobBytes))
	if blobSize > relayMaxBlobBytes {
		return fmt.Errorf("peering/relay: deposit: blob size %d exceeds maximum %d bytes", blobSize, relayMaxBlobBytes)
	}

	// 7. Per-recipient cap.
	recipientDir := rs.recipientDir(req.To)
	recipientStored, err := rs.dirStoredBytes(recipientDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("peering/relay: deposit: compute recipient storage: %w", err)
	}
	if recipientStored+blobSize > relayMaxPerRecipientBytes {
		return fmt.Errorf("peering/relay: deposit: recipient storage cap reached (%d/%d bytes)", recipientStored, relayMaxPerRecipientBytes)
	}

	// 8. Total relay capacity.
	totalStored, err := rs.dirStoredBytes(rs.storeDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("peering/relay: deposit: compute total storage: %w", err)
	}
	if totalStored+blobSize > cfg.effectiveCapacityBytes() {
		return fmt.Errorf("peering/relay: deposit: relay storage capacity reached")
	}

	// All checks passed — persist.
	if err := os.MkdirAll(recipientDir, 0700); err != nil {
		return fmt.Errorf("peering/relay: deposit: mkdir recipient dir: %w", err)
	}

	ttl := cfg.effectiveTTL(req.TTLHours)
	now := time.Now().UTC()
	blob := RelayBlob{
		ID:              req.BlobID,
		RecipientVulaID: req.To,
		SenderVulaID:    req.SenderVulaID,
		BlobB64:         req.BlobB64,
		BlobSize:        blobSize,
		DepositedAt:     now,
		ExpiresAt:       now.Add(ttl),
	}

	blobData, err := json.Marshal(blob)
	if err != nil {
		return fmt.Errorf("peering/relay: deposit: marshal blob: %w", err)
	}

	dst := filepath.Join(recipientDir, sanitizePath(req.BlobID)+".json")
	// Idempotent: if the blob already exists (re-deposit), overwrite.
	if err := relayAtomicWrite(recipientDir, dst, blobData); err != nil {
		return fmt.Errorf("peering/relay: deposit: write blob: %w", err)
	}

	// METER (surface 4: relay). One deposit of blobSize relayed bytes. No-op
	// when billing is disabled.
	rs.billing.MeterAsync(cpbilling.UsageEvent{
		Product:   cpbilling.ProductRelay,
		AccountID: req.SenderVulaID,
		Kind:      cpbilling.KindRelayBytes,
		Count:     1,
		Bytes:     blobSize,
	})

	// Record for rate limiting.
	rs.recordDeposit(req.SenderVulaID, now)
	log.Printf("[peering/relay] deposit: blob %q from %s for %s (%d bytes, expires %s)",
		req.BlobID, req.SenderVulaID, req.To, blobSize, blob.ExpiresAt.Format(time.RFC3339))
	return nil
}

// ─── Pickup ───────────────────────────────────────────────────────────────────

// Pickup returns all pending blobs for recipientVulaID.
//
// authTimestampUnix is the Unix timestamp string from the Authorization header.
// sigB64URL is the base64url (no-padding) Ed25519 signature over the string
// "<recipientVulaID>.<authTimestampUnix>" signed by the recipient's private key.
//
// The relay verifies the signature and enforces the timestamp tolerance window.
// It does NOT delete the blobs — deletion is deferred until Ack is called.
func (rs *RelayStore) Pickup(recipientVulaID, authTimestampUnix, sigB64URL string) ([]RelayBlob, error) {
	// Authenticate the pickup request.
	if err := relayVerifyPickupAuth(recipientVulaID, authTimestampUnix, sigB64URL); err != nil {
		return nil, fmt.Errorf("peering/relay: pickup: %w", err)
	}

	recipientDir := rs.recipientDir(recipientVulaID)
	entries, err := os.ReadDir(recipientDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // no blobs — return empty (not an error)
	}
	if err != nil {
		return nil, fmt.Errorf("peering/relay: pickup: read recipient dir: %w", err)
	}

	now := time.Now().UTC()
	var blobs []RelayBlob
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		blob, err := rs.readBlob(filepath.Join(recipientDir, entry.Name()))
		if err != nil {
			log.Printf("[peering/relay] pickup: skip corrupt blob %s: %v", entry.Name(), err)
			continue
		}
		if now.After(blob.ExpiresAt) {
			// Expired — skip (reaper will delete it).
			continue
		}
		blobs = append(blobs, blob)
	}
	return blobs, nil
}

// ─── Ack ──────────────────────────────────────────────────────────────────────

// Ack removes the blobs identified by blobIDs from the relay store for
// recipientVulaID.
//
// The same authentication scheme as Pickup is used (timestamp + signature).
// Blob IDs that are not found are silently skipped (idempotent).
func (rs *RelayStore) Ack(recipientVulaID, authTimestampUnix, sigB64URL string, blobIDs []string) error {
	if err := relayVerifyPickupAuth(recipientVulaID, authTimestampUnix, sigB64URL); err != nil {
		return fmt.Errorf("peering/relay: ack: %w", err)
	}

	recipientDir := rs.recipientDir(recipientVulaID)
	for _, id := range blobIDs {
		if id == "" {
			continue
		}
		path := filepath.Join(recipientDir, sanitizePath(id)+".json")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[peering/relay] ack: remove blob %s: %v", path, err)
		}
	}
	log.Printf("[peering/relay] ack: %d blob(s) deleted for %s", len(blobIDs), recipientVulaID)
	return nil
}

// ─── Background reaper ────────────────────────────────────────────────────────

// reapLoop periodically scans the store and removes blobs whose ExpiresAt is
// in the past.
func (rs *RelayStore) reapLoop(ctx context.Context) {
	ticker := time.NewTicker(relayReaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rs.reapExpired()
		}
	}
}

// reapExpired removes all expired blobs from the store.
func (rs *RelayStore) reapExpired() {
	recipDirs, err := os.ReadDir(rs.storeDir)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	removed := 0
	for _, rd := range recipDirs {
		if !rd.IsDir() {
			continue
		}
		dir := filepath.Join(rs.storeDir, rd.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			blob, err := rs.readBlob(path)
			if err != nil {
				continue
			}
			if now.After(blob.ExpiresAt) {
				if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
					log.Printf("[peering/relay] reaper: remove %s: %v", path, rerr)
				} else {
					removed++
				}
			}
		}
	}
	if removed > 0 {
		log.Printf("[peering/relay] reaper: removed %d expired blob(s)", removed)
	}
}

// ─── Rate limiting ────────────────────────────────────────────────────────────

// checkRateLimit returns an error if senderVulaID has exceeded
// relayMaxDepositsPerHour deposits in the past hour.
func (rs *RelayStore) checkRateLimit(senderVulaID string) error {
	rs.rateMu.Lock()
	defer rs.rateMu.Unlock()

	cutoff := time.Now().UTC().Add(-time.Hour)
	prev := rs.senderDeposits[senderVulaID]
	var recent []time.Time
	for _, t := range prev {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	rs.senderDeposits[senderVulaID] = recent

	if len(recent) >= relayMaxDepositsPerHour {
		return fmt.Errorf("peering/relay: deposit: rate limit exceeded (%d/h)", relayMaxDepositsPerHour)
	}
	return nil
}

// recordDeposit records a successful deposit for rate-limiting purposes.
func (rs *RelayStore) recordDeposit(senderVulaID string, at time.Time) {
	rs.rateMu.Lock()
	defer rs.rateMu.Unlock()
	rs.senderDeposits[senderVulaID] = append(rs.senderDeposits[senderVulaID], at)
}

// ─── Storage helpers ──────────────────────────────────────────────────────────

// recipientDir returns the directory path for a recipient's blobs.
func (rs *RelayStore) recipientDir(recipientVulaID string) string {
	return filepath.Join(rs.storeDir, sanitizePath(recipientVulaID))
}

// readBlob reads and unmarshals a RelayBlob from path.
func (rs *RelayStore) readBlob(path string) (RelayBlob, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RelayBlob{}, fmt.Errorf("peering/relay: read blob %s: %w", path, err)
	}
	var blob RelayBlob
	if err := json.Unmarshal(data, &blob); err != nil {
		return RelayBlob{}, fmt.Errorf("peering/relay: unmarshal blob %s: %w", path, err)
	}
	return blob, nil
}

// dirStoredBytes sums the BlobSize of every RelayBlob JSON file under dir.
// Returns 0 and os.ErrNotExist if the directory does not exist.
func (rs *RelayStore) dirStoredBytes(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, os.ErrNotExist
	}
	if err != nil {
		return 0, err
	}

	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			// Recurse into recipient sub-directories (when called with storeDir).
			subTotal, err := rs.dirStoredBytes(filepath.Join(dir, entry.Name()))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return 0, err
			}
			total += subTotal
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		blob, err := rs.readBlob(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue // skip corrupt files
		}
		total += blob.BlobSize
	}
	return total, nil
}

// ─── Signature helpers ────────────────────────────────────────────────────────

// relayVerifyDepositSig verifies the Ed25519 signature on a deposit request.
//
// The signed message is the canonical JSON of the request object with the
// "signature" field removed.  The public key is derived from req.SenderVulaID.
func relayVerifyDepositSig(req relayDepositRequest) error {
	pub, err := decodeVulaID(req.SenderVulaID)
	if err != nil {
		return fmt.Errorf("deposit sig: decode sender vula id: %w", err)
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(req.Signature)
	if err != nil {
		return fmt.Errorf("deposit sig: decode signature: %w", err)
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return fmt.Errorf("deposit sig: signature length %d, want %d", len(sigBytes), ed25519.SignatureSize)
	}

	// Build canonical signed bytes: all fields except "signature".
	canonical, err := relayDepositCanonical(req)
	if err != nil {
		return fmt.Errorf("deposit sig: canonical bytes: %w", err)
	}

	if !ed25519.Verify(pub, canonical, sigBytes) {
		return errors.New("deposit sig: signature mismatch")
	}
	return nil
}

// relayDepositCanonical returns the canonical JSON bytes of req with the
// "signature" field removed.
func relayDepositCanonical(req relayDepositRequest) ([]byte, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	delete(m, "signature")
	return canonicaliseObject(m)
}

// relayVerifyPickupAuth authenticates a pickup or ack request.
//
// The Authorization header value must be:
//
//	Vula-Relay <vula_id>.<timestamp_unix>.<base64url_sig>
//
// where sig is the Ed25519 signature over "<vula_id>.<timestamp_unix>"
// (UTF-8 bytes, no trailing newline).
//
// The timestamp must be within ±relayPickupTimestampTolerance of now.
func relayVerifyPickupAuth(recipientVulaID, tsUnixStr, sigB64URL string) error {
	if recipientVulaID == "" {
		return errors.New("pickup auth: recipient vula id must not be empty")
	}
	if tsUnixStr == "" {
		return errors.New("pickup auth: timestamp must not be empty")
	}
	if sigB64URL == "" {
		return errors.New("pickup auth: signature must not be empty")
	}

	// Parse and validate timestamp.
	var tsUnix int64
	if _, err := fmt.Sscanf(tsUnixStr, "%d", &tsUnix); err != nil {
		return fmt.Errorf("pickup auth: invalid timestamp %q: %w", tsUnixStr, err)
	}
	ts := time.Unix(tsUnix, 0).UTC()
	now := time.Now().UTC()
	diff := now.Sub(ts)
	if diff < 0 {
		diff = -diff
	}
	if diff > relayPickupTimestampTolerance {
		return fmt.Errorf("pickup auth: timestamp skew %v exceeds tolerance %v", diff, relayPickupTimestampTolerance)
	}

	// Decode public key from Vula ID.
	pub, err := decodeVulaID(recipientVulaID)
	if err != nil {
		return fmt.Errorf("pickup auth: decode vula id: %w", err)
	}

	// Decode signature.
	sigBytes, err := base64.RawURLEncoding.DecodeString(sigB64URL)
	if err != nil {
		return fmt.Errorf("pickup auth: decode signature: %w", err)
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return fmt.Errorf("pickup auth: signature length %d, want %d", len(sigBytes), ed25519.SignatureSize)
	}

	// Verify over "<vula_id>.<timestamp_unix>".
	msg := []byte(recipientVulaID + "." + tsUnixStr)
	if !ed25519.Verify(pub, msg, sigBytes) {
		return errors.New("pickup auth: signature mismatch")
	}
	return nil
}

// ─── Misc helpers ─────────────────────────────────────────────────────────────

// relayIsSafeID reports whether s contains only characters safe to use as a
// blob ID (and thus as part of a filename).
func relayIsSafeID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// relayInAllowedList reports whether vulaID is in the list.
func relayInAllowedList(vulaID string, list []string) bool {
	for _, id := range list {
		if id == vulaID {
			return true
		}
	}
	return false
}

// relayAtomicWrite writes data to dst using a temp file + rename within dir.
func relayAtomicWrite(dir, dst string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".relay-*.json.tmp")
	if err != nil {
		return fmt.Errorf("peering/relay: create temp: %w", err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		if werr != nil {
			return fmt.Errorf("peering/relay: write temp: %w", werr)
		}
		return fmt.Errorf("peering/relay: close temp: %w", cerr)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("peering/relay: rename to %s: %w", dst, err)
	}
	return nil
}

// parseRelayAuthHeader parses the Authorization header for relay pickup/ack.
//
// Expected format:
//
//	Vula-Relay <vula_id>.<timestamp_unix>.<base64url_sig>
//
// Returns (vulaID, timestampUnix, sigB64URL, nil) on success.
func parseRelayAuthHeader(header string) (string, string, string, error) {
	const prefix = "Vula-Relay "
	if !strings.HasPrefix(header, prefix) {
		return "", "", "", fmt.Errorf("peering/relay: authorization header must start with %q", prefix)
	}
	rest := strings.TrimPrefix(header, prefix)

	// Format: <vula_id>.<timestamp_unix>.<sig>
	// vula_id contains ':' and base58 chars; sig is base64url; timestamp is numeric.
	// We split from the right: last two '.' delimit timestamp and sig.
	lastDot := strings.LastIndex(rest, ".")
	if lastDot < 0 {
		return "", "", "", errors.New("peering/relay: authorization header: missing signature field")
	}
	sigPart := rest[lastDot+1:]
	remaining := rest[:lastDot]

	secondLastDot := strings.LastIndex(remaining, ".")
	if secondLastDot < 0 {
		return "", "", "", errors.New("peering/relay: authorization header: missing timestamp field")
	}
	tsPart := remaining[secondLastDot+1:]
	vulaIDPart := remaining[:secondLastDot]

	if vulaIDPart == "" || tsPart == "" || sigPart == "" {
		return "", "", "", errors.New("peering/relay: authorization header: malformed")
	}
	return vulaIDPart, tsPart, sigPart, nil
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

// RegisterRelayHandlers wires the relay HTTP endpoints onto mux.
//
// Routes registered:
//
//	POST /api/peering/relay/deposit   → handleRelayDeposit
//	GET  /api/peering/relay/pickup    → handleRelayPickup
//	POST /api/peering/relay/ack       → handleRelayAck
//
// store must be non-nil.
func RegisterRelayHandlers(mux *http.ServeMux, store *RelayStore) {
	if mux == nil {
		panic("peering/relay: RegisterRelayHandlers: mux must not be nil")
	}
	if store == nil {
		panic("peering/relay: RegisterRelayHandlers: store must not be nil")
	}
	mux.HandleFunc("POST /api/peering/relay/deposit", store.handleRelayDeposit)
	mux.HandleFunc("GET /api/peering/relay/pickup", store.handleRelayPickup)
	mux.HandleFunc("POST /api/peering/relay/ack", store.handleRelayAck)
}

// handleRelayDeposit handles POST /api/peering/relay/deposit.
//
// Request body (JSON):
//
//	{
//	  "to":             "<recipient_vula_id>",
//	  "blob_id":        "<unique_blob_id>",
//	  "blob_b64":       "<base64std_ciphertext>",
//	  "ttl_hours":      72,
//	  "sender_vula_id": "<sender_vula_id>",
//	  "nonce":          "<hex_random_nonce>",
//	  "signature":      "<base64url_ed25519_sig>"
//	}
//
// Success: 201 Created, body: {"blob_id": "<id>"}
// Errors:  400 Bad Request, 401 Unauthorized, 403 Forbidden, 429 Too Many
//
//	Requests, 507 Insufficient Storage, 503 Service Unavailable
func (rs *RelayStore) handleRelayDeposit(w http.ResponseWriter, r *http.Request) {
	limited := io.LimitReader(r.Body, relayRequestBodyLimit+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		relayWriteError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if int64(len(body)) > relayRequestBodyLimit {
		relayWriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	var req relayDepositRequest
	if err := json.Unmarshal(body, &req); err != nil {
		relayWriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if err := rs.Deposit(req); err != nil {
		statusCode := relayDepositErrStatus(err)
		relayWriteError(w, statusCode, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"blob_id": req.BlobID}) //nolint:errcheck
}

// handleRelayPickup handles GET /api/peering/relay/pickup.
//
// Authorization header:
//
//	Authorization: Vula-Relay <vula_id>.<timestamp_unix>.<base64url_sig>
//
// Success: 200 OK, body: {"blobs": [...RelayBlob...]}
// Errors:  401 Unauthorized
func (rs *RelayStore) handleRelayPickup(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	vulaID, tsStr, sigStr, err := parseRelayAuthHeader(authHeader)
	if err != nil {
		relayWriteError(w, http.StatusUnauthorized, err.Error())
		return
	}

	blobs, err := rs.Pickup(vulaID, tsStr, sigStr)
	if err != nil {
		relayWriteError(w, http.StatusUnauthorized, err.Error())
		return
	}

	if blobs == nil {
		blobs = []RelayBlob{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{"blobs": blobs}) //nolint:errcheck
}

// handleRelayAck handles POST /api/peering/relay/ack.
//
// Authorization header: same as handleRelayPickup.
//
// Request body (JSON):
//
//	{"blob_ids": ["<id1>", "<id2>", ...]}
//
// Success: 200 OK, body: {"deleted": <count>}
// Errors:  400 Bad Request, 401 Unauthorized
func (rs *RelayStore) handleRelayAck(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	vulaID, tsStr, sigStr, err := parseRelayAuthHeader(authHeader)
	if err != nil {
		relayWriteError(w, http.StatusUnauthorized, err.Error())
		return
	}

	limited := io.LimitReader(r.Body, relayPickupBodyLimit+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		relayWriteError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if int64(len(body)) > relayPickupBodyLimit {
		relayWriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	var req struct {
		BlobIDs []string `json:"blob_ids"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		relayWriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if err := rs.Ack(vulaID, tsStr, sigStr, req.BlobIDs); err != nil {
		relayWriteError(w, http.StatusUnauthorized, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]int{"deleted": len(req.BlobIDs)}) //nolint:errcheck
}

// relayWriteError sends a JSON error response.
func relayWriteError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}

// relayDepositErrStatus maps Deposit errors to HTTP status codes.
func relayDepositErrStatus(err error) int {
	s := err.Error()
	switch {
	case strings.Contains(s, "not enabled"):
		return http.StatusServiceUnavailable
	case strings.Contains(s, "not approved"),
		strings.Contains(s, "not in the allowed list"),
		strings.Contains(s, "signature mismatch"),
		strings.Contains(s, "decode sender vula id"):
		return http.StatusForbidden
	case strings.Contains(s, "invalid or missing signature"),
		strings.Contains(s, "deposit sig"):
		return http.StatusUnauthorized
	case strings.Contains(s, "rate limit"):
		return http.StatusTooManyRequests
	case strings.Contains(s, "exceeds maximum"),
		strings.Contains(s, "cap reached"),
		strings.Contains(s, "capacity reached"):
		return http.StatusInsufficientStorage
	default:
		return http.StatusBadRequest
	}
}
