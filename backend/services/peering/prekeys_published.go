// no-broker-dep:allow-file: comment names 'Ephor/fabric.js' only as an example of a browser peer
// that may query this endpoint for prekey claims -- illustrative, no
// dependency.

// prekeys_published.go — the PUBLIC prekey directory for REMOTE (browser) peers.
//
// # Why this exists
//
// PreKeyStore (prekeys.go) holds THIS node's own PRIVATE prekey material and can
// only answer CLAIMs for its own identity. Browser peers (Ephor/fabric.js)
// generate their X3DH prekeys entirely client-side and hold ALL private scalars
// themselves; they merely need somewhere to PUBLISH the PUBLIC halves so a sender
// can claim a one-time prekey and open a forward-secret session.
//
// PublishedBundleStore is that directory. It is CLOUD-BLIND / CONTENT-BLIND: it
// stores ONLY public prekey material (a signed-prekey public key + signature and a
// pool of one-time-prekey public keys). It never sees, holds, or serves any
// private key — the browser peer keeps every private scalar. The host cannot
// decrypt anything with what it stores here; it can only help two clients agree on
// keys the host itself never learns.
//
// In-memory only (mirrors the ephemeral relay model): a lost/restarted host simply
// drops published bundles and peers re-publish. Bounded on both the number of
// tracked identities and the pool size per identity to cap memory.
package peering

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/curve25519"
)

// Bounds on the published directory (public material only; cheap, but capped so a
// flood of publishes cannot exhaust host memory).
const (
	defaultMaxPublishedIdentities = 8192
	defaultMaxPublishedOPKs       = 256
)

// publishedEntry is one identity's PUBLIC bundle. It holds NO private key.
type publishedEntry struct {
	signedPreKey SignedPreKeyPublic             // public key + Ed25519 signature
	opks         map[string]OneTimePreKeyPublic // id → PUBLIC one-time prekey
	opkOrder     []string                       // insertion order (bounded FIFO trim)
	updatedAt    string
}

// PublishedBundleStore is a bounded, in-memory, PUBLIC-ONLY prekey directory keyed
// by peer identity VulaID. Goroutine-safe.
type PublishedBundleStore struct {
	mu            sync.Mutex
	byIdentity    map[string]*publishedEntry
	maxIdentities int
	maxOPKs       int
}

// NewPublishedBundleStore creates a bounded directory. Non-positive bounds fall
// back to defaults.
func NewPublishedBundleStore(maxIdentities, maxOPKsPerIdentity int) *PublishedBundleStore {
	if maxIdentities <= 0 {
		maxIdentities = defaultMaxPublishedIdentities
	}
	if maxOPKsPerIdentity <= 0 {
		maxOPKsPerIdentity = defaultMaxPublishedOPKs
	}
	return &PublishedBundleStore{
		byIdentity:    make(map[string]*publishedEntry),
		maxIdentities: maxIdentities,
		maxOPKs:       maxOPKsPerIdentity,
	}
}

// Publish stores (or refreshes) a peer's PUBLIC bundle. It FAILS CLOSED:
//   - the signed prekey signature MUST verify against the identity embedded in
//     IdentityVulaID (VerifySignedPreKey) — an unsigned/forged bundle is rejected
//     and NOTHING is stored,
//   - every one-time prekey must be a well-formed 32-byte X25519 public key.
//
// Re-publishing REPLACES the signed prekey (rotation/refresh) and APPENDS new
// one-time prekeys (deduped by id), trimming oldest first to the per-identity cap.
// Only PUBLIC bytes are ever copied in; no private key can be represented by the
// input types, so none is stored.
func (s *PublishedBundleStore) Publish(bundle *PreKeyBundlePublic) error {
	if s == nil {
		return errors.New("prekeys: nil published store")
	}
	if bundle == nil {
		return errors.New("prekeys: nil bundle")
	}
	if bundle.IdentityVulaID == "" {
		return errors.New("prekeys: bundle missing identity_vula_id")
	}
	// Signature check binds the signed prekey to the claimed identity: only the
	// holder of that identity's Ed25519 private key can produce it. This is what
	// stops a third party from publishing a forged bundle for someone else.
	if err := bundle.VerifySignedPreKey(); err != nil {
		return err
	}
	// Validate every OPK is a proper X25519 point before mutating any state.
	for i := range bundle.OneTimePreKeys {
		if len(bundle.OneTimePreKeys[i].Pub) != curve25519.PointSize {
			return errors.New("prekeys: one-time prekey wrong size")
		}
		if bundle.OneTimePreKeys[i].ID == "" {
			return errors.New("prekeys: one-time prekey missing id")
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.byIdentity[bundle.IdentityVulaID]
	if !ok {
		if len(s.byIdentity) >= s.maxIdentities {
			return errors.New("prekeys: published directory full")
		}
		entry = &publishedEntry{opks: make(map[string]OneTimePreKeyPublic)}
		s.byIdentity[bundle.IdentityVulaID] = entry
	}

	// Refresh the signed prekey (copy public bytes; no private material exists).
	entry.signedPreKey = SignedPreKeyPublic{
		ID:  bundle.SignedPreKey.ID,
		Pub: append([]byte(nil), bundle.SignedPreKey.Pub...),
		Sig: append([]byte(nil), bundle.SignedPreKey.Sig...),
	}
	entry.updatedAt = time.Now().UTC().Format(time.RFC3339)

	// Append new OPKs (dedupe by id), then trim oldest to the cap.
	for i := range bundle.OneTimePreKeys {
		id := bundle.OneTimePreKeys[i].ID
		if _, exists := entry.opks[id]; exists {
			continue
		}
		entry.opks[id] = OneTimePreKeyPublic{
			ID:  id,
			Pub: append([]byte(nil), bundle.OneTimePreKeys[i].Pub...),
		}
		entry.opkOrder = append(entry.opkOrder, id)
	}
	entry.trimLocked(s.maxOPKs)
	return nil
}

// trimLocked drops the oldest one-time prekeys until the pool is within cap. Caller
// holds s.mu.
func (e *publishedEntry) trimLocked(max int) {
	for len(e.opks) > max && len(e.opkOrder) > 0 {
		oldest := e.opkOrder[0]
		e.opkOrder = e.opkOrder[1:]
		delete(e.opks, oldest)
	}
}

// Claim serves a PUBLISHED bundle for vulaID: the signed prekey plus AT MOST ONE
// one-time prekey, atomically DELETED from the pool (single-use → per-sender
// forward secrecy). The bool is false when the identity has not published (caller
// → 404). OneTimePreKey is nil when the pool is exhausted (signed-prekey-only
// fallback). Never returns any private key (the store holds none).
func (s *PublishedBundleStore) Claim(vulaID string) (*ClaimedBundle, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.byIdentity[vulaID]
	if !ok {
		return nil, false
	}
	out := &ClaimedBundle{
		IdentityVulaID: vulaID,
		SignedPreKey: SignedPreKeyPublic{
			ID:  entry.signedPreKey.ID,
			Pub: append([]byte(nil), entry.signedPreKey.Pub...),
			Sig: append([]byte(nil), entry.signedPreKey.Sig...),
		},
	}
	// Hand out + delete the oldest OPK (single-use). Skip any stale order entries.
	for len(entry.opkOrder) > 0 {
		id := entry.opkOrder[0]
		entry.opkOrder = entry.opkOrder[1:]
		opk, exists := entry.opks[id]
		if !exists {
			continue
		}
		out.OneTimePreKey = &OneTimePreKeyPublic{ID: opk.ID, Pub: append([]byte(nil), opk.Pub...)}
		delete(entry.opks, id)
		break
	}
	return out, true
}

// Has reports whether an identity has a published bundle (test/introspection).
func (s *PublishedBundleStore) Has(vulaID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byIdentity[vulaID]
	return ok
}

// OneTimePreKeyCount reports remaining published OPKs for an identity (test/metrics).
func (s *PublishedBundleStore) OneTimePreKeyCount(vulaID string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.byIdentity[vulaID]; ok {
		return len(e.opks)
	}
	return 0
}
