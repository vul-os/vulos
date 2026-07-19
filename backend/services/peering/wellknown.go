// Package peering implements Vula OS peer-to-peer identity, profile, and discovery.
//
// PEER-12: well-known identity endpoint + peer profile fetch/cache.
//
// Exported surface:
//   - RegisterWellKnownHandlers(mux *http.ServeMux) — mounts GET /.well-known/vula-id
//     and GET /api/peering/profile/{vula_id} on the supplied mux.
//   - FetchPeerProfile(ctx, vulaID, serverAddr) (*WKPeerProfile, error) — fetches
//     and caches a peer's public profile from their Vula instance.
//
// All identifiers introduced here are prefixed wk / WellKnown to avoid conflicts
// with future files in the same package.
package peering

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"vulos/backend/internal/datadir"

	"vulos/backend/internal/safedial"
	"vulos/backend/services/signing"
)

// peeringSSRFBypass is a test-only hook for the entire peering package. When
// true, all SSRF validation (ValidateHost calls and safedial transports) is
// skipped so httptest.Server on 127.0.0.1 and fake hostnames ("home.alice")
// are accepted. Must NEVER be set in production — only via TestMain in tests
// inside package peering.
var peeringSSRFBypass bool

// --------------------------------------------------------------------------
// Identity storage
// --------------------------------------------------------------------------

// wkIdentityFile is the path (relative to the peering data dir) where the
// node's own keypair + Vula ID are persisted.
const wkIdentityFile = "identity/identity.json"

// wkProfileFile is the path (relative to the peering data dir) where the
// node's own profile is stored.
const wkProfileFile = "profile/profile.json"

// wkPeeringDataDir returns the canonical peering data directory.
// Default: ~/.vulos/peering.  Override via VULOS_PEERING_DIR env var.
func wkPeeringDataDir() string {
	if d := os.Getenv("VULOS_PEERING_DIR"); d != "" {
		return d
	}
	return datadir.Join("peering")
}

// --------------------------------------------------------------------------
// Vula ID format
// --------------------------------------------------------------------------

// wkVulaIDPrefix is the URI scheme prefix for all Vula IDs.
const wkVulaIDPrefix = "vula:ed25519:"

// wkFormatVulaID encodes a raw Ed25519 public key as a Vula ID string.
func wkFormatVulaID(pub ed25519.PublicKey) string {
	return wkVulaIDPrefix + base64.RawURLEncoding.EncodeToString(pub)
}

// wkParseVulaID extracts the raw Ed25519 public key embedded in a Vula ID.
// A Vula ID *is* a public key, so this is the key the peer's profile signature
// must verify against. Returns an error for malformed IDs.
func wkParseVulaID(vulaID string) (ed25519.PublicKey, error) {
	if !strings.HasPrefix(vulaID, wkVulaIDPrefix) {
		return nil, fmt.Errorf("peering: vula_id %q missing %q prefix", vulaID, wkVulaIDPrefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(vulaID, wkVulaIDPrefix))
	if err != nil {
		return nil, fmt.Errorf("peering: decode vula_id key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("peering: vula_id key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// wkStoredIdentity.privateKey reconstructs the Ed25519 private key from the
// stored base64url seed.
func (id *wkStoredIdentity) privateKey() (ed25519.PrivateKey, error) {
	seed, err := base64.RawURLEncoding.DecodeString(id.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("peering: decode identity seed: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("peering: identity seed is %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// wkCanonicalSigningBytes returns the deterministic bytes that a profile
// signature covers: the canonical JSON of the response with the signature
// field cleared (it is omitempty, so it is absent from the signed payload).
func wkCanonicalSigningBytes(resp WKIdentityResponse) ([]byte, error) {
	resp.Signature = ""
	return signing.Canonical(resp)
}

// wkSignResponse signs resp in place with priv, populating resp.Signature.
func wkSignResponse(resp *WKIdentityResponse, priv ed25519.PrivateKey) error {
	payload, err := wkCanonicalSigningBytes(*resp)
	if err != nil {
		return err
	}
	resp.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, payload))
	return nil
}

// wkVerifyResponse reports whether resp carries a valid Ed25519 signature over
// its canonical form by the key embedded in expectedVulaID. Fail-closed:
// missing, malformed, or mismatched signatures all return false.
func wkVerifyResponse(resp WKIdentityResponse, expectedVulaID string) bool {
	pub, err := wkParseVulaID(expectedVulaID)
	if err != nil {
		return false
	}
	if resp.Signature == "" {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(resp.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	payload, err := wkCanonicalSigningBytes(resp)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, payload, sig)
}

// --------------------------------------------------------------------------
// On-disk identity (own node)
// --------------------------------------------------------------------------

// wkStoredIdentity is the JSON-serialisable form saved in identity.json.
type wkStoredIdentity struct {
	VulaID     string `json:"vula_id"`
	PublicKey  string `json:"public_key"`  // base64url
	PrivateKey string `json:"private_key"` // base64url (seed)
	CreatedAt  string `json:"created_at"`
}

// wkLoadOrCreateIdentity returns the node's identity, generating a fresh
// keypair on first boot and persisting it to disk.
func wkLoadOrCreateIdentity(dataDir string) (*wkStoredIdentity, error) {
	path := filepath.Join(dataDir, wkIdentityFile)

	// Try to load existing identity.
	if raw, err := os.ReadFile(path); err == nil {
		var id wkStoredIdentity
		if err := json.Unmarshal(raw, &id); err == nil && id.VulaID != "" {
			return &id, nil
		}
	}

	// Generate new Ed25519 keypair.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("peering: generate keypair: %w", err)
	}

	id := &wkStoredIdentity{
		VulaID:     wkFormatVulaID(pub),
		PublicKey:  base64.RawURLEncoding.EncodeToString(pub),
		PrivateKey: base64.RawURLEncoding.EncodeToString(priv.Seed()),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("peering: mkdir identity: %w", err)
	}
	raw, _ := json.MarshalIndent(id, "", "  ")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return nil, fmt.Errorf("peering: write identity: %w", err)
	}

	return id, nil
}

// --------------------------------------------------------------------------
// Own profile (what this node advertises)
// --------------------------------------------------------------------------

// WKVisibility controls who can see a profile field.
type WKVisibility string

const (
	WKVisibilityPublic WKVisibility = "public"
	WKVisibilityPeers  WKVisibility = "peers"
	WKVisibilityNobody WKVisibility = "nobody"
)

// WKProfileVisibility holds per-field visibility settings.
type WKProfileVisibility struct {
	Image WKVisibility `json:"image"`
	Bio   WKVisibility `json:"bio"`
	Email WKVisibility `json:"email"`
}

// WKOwnProfile is the full locally-stored profile (all fields + visibility).
type WKOwnProfile struct {
	VulaID        string              `json:"vula_id"`
	DisplayName   string              `json:"display_name"`
	Bio           string              `json:"bio,omitempty"`
	Email         string              `json:"email,omitempty"`
	VerifiedEmail bool                `json:"verified_email"`
	Slug          string              `json:"slug,omitempty"`
	Visibility    WKProfileVisibility `json:"visibility"`
	UpdatedAt     string              `json:"updated_at"`
}

// wkLoadOwnProfile reads the stored own profile; returns a minimal default if absent.
func wkLoadOwnProfile(dataDir string, vulaID string) WKOwnProfile {
	path := filepath.Join(dataDir, wkProfileFile)
	if raw, err := os.ReadFile(path); err == nil {
		var p WKOwnProfile
		if json.Unmarshal(raw, &p) == nil && p.VulaID != "" {
			return p
		}
	}
	return WKOwnProfile{
		VulaID:      vulaID,
		DisplayName: "Vula User",
		Visibility: WKProfileVisibility{
			Image: WKVisibilityPublic,
			Bio:   WKVisibilityPeers,
			Email: WKVisibilityNobody,
		},
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// --------------------------------------------------------------------------
// Well-known response (public fields only)
// --------------------------------------------------------------------------

// WKIdentityResponse is what GET /.well-known/vula-id returns to the world.
// Only public-visibility fields appear here; everything else is omitted.
type WKIdentityResponse struct {
	VulaID        string `json:"vula_id"`
	DisplayName   string `json:"display_name"`
	VerifiedEmail bool   `json:"verified_email"`
	Slug          string `json:"slug,omitempty"`
	// Image is included only when visibility == "public" (URL to avatar endpoint).
	Image string `json:"image,omitempty"`
	// Bio is included only when visibility == "public".
	Bio string `json:"bio,omitempty"`
	// Lifecycle, when present, publishes this identity's key-lifecycle data
	// (rotation/recovery chain + observed revocations) so peers can follow key
	// rotations/recoveries and honor revocations (identity_lifecycle.go). The
	// certificates inside carry their own independent base58 signatures, so they
	// are verifiable regardless of this response's signature; the outer signature
	// only attests that this server published them.
	Lifecycle *WKLifecycle `json:"lifecycle,omitempty"`

	// PreKeys, when present, publishes this identity's X3DH prekey bundle (signed
	// prekey + one-time prekey pool) so senders can establish FORWARD-SECRET
	// content sessions (prekeys.go). The signed prekey carries its own identity-key
	// signature; one-time prekeys are consumed at most once by the recipient.
	PreKeys *PreKeyBundlePublic `json:"prekeys,omitempty"`

	// Signature is the base64url Ed25519 signature over the canonical form of
	// this response (with this field empty), produced by the node's identity
	// key. Because a Vula ID IS an Ed25519 public key, fetchers verify this
	// against the expected Vula ID to authenticate the profile (PEER-12
	// hardening). Empty only when the node could not load its private key.
	Signature string `json:"sig,omitempty"`
}

// WKLifecycle is the published key-lifecycle bundle (see identity_lifecycle.go).
type WKLifecycle struct {
	// RootVulaID is the original identity this chain descends from.
	RootVulaID string `json:"root_vula_id"`
	// AnchorVulaID is the account recovery anchor (peers TOFU-pin this to follow
	// recovery certs).
	AnchorVulaID string `json:"anchor_vula_id,omitempty"`
	// Chain is the ordered rotation/recovery transition history.
	Chain []LifecycleLink `json:"chain,omitempty"`
	// Revocations are observed, self/anchor-signed revocation certificates.
	Revocations []*RevocationCert `json:"revocations,omitempty"`
}

// wkLifecyclePublisher, when set, supplies the lifecycle bundle embedded in the
// well-known response. The LIVE server wires it from a *LifecycleStore. Kept as a
// package hook so the wellknown handler need not import the store directly and so
// tests can install a stub.
var (
	wkLifecyclePublisherMu sync.RWMutex
	wkLifecyclePublisher   func() *WKLifecycle
)

// SetLifecyclePublisher installs the lifecycle bundle source for the well-known
// endpoint. The live server passes a closure over its *LifecycleStore.OwnChain /
// RevocationList. Passing nil disables lifecycle publication.
func SetLifecyclePublisher(f func() *WKLifecycle) {
	wkLifecyclePublisherMu.Lock()
	wkLifecyclePublisher = f
	wkLifecyclePublisherMu.Unlock()
}

func wkCurrentLifecycle() *WKLifecycle {
	wkLifecyclePublisherMu.RLock()
	f := wkLifecyclePublisher
	wkLifecyclePublisherMu.RUnlock()
	if f == nil {
		return nil
	}
	return f()
}

// wkLifecycleIngestor, when set, consumes a peer's published lifecycle bundle on
// every authenticated profile fetch/refresh: it verifies + records the peer's
// revocations and follows its rotation/recovery chain (identity_lifecycle.go). The
// LIVE server wires it from a *LifecycleStore.IngestPeerLifecycle. Without it,
// FetchPeerProfile never reads wk.Lifecycle and revocations never propagate.
var (
	wkLifecycleIngestorMu sync.RWMutex
	wkLifecycleIngestor   func(lc *WKLifecycle)
)

// SetLifecycleIngestor installs the sink for peer lifecycle bundles seen during
// profile fetch/refresh. The live server passes a closure over its
// *LifecycleStore.IngestPeerLifecycle so a peer that publishes a revocation (or a
// rotation/recovery) is honored without a separate gossip channel. Passing nil
// disables ingestion.
func SetLifecycleIngestor(f func(lc *WKLifecycle)) {
	wkLifecycleIngestorMu.Lock()
	wkLifecycleIngestor = f
	wkLifecycleIngestorMu.Unlock()
}

func wkIngestLifecycle(lc *WKLifecycle) {
	if lc == nil {
		return
	}
	wkLifecycleIngestorMu.RLock()
	f := wkLifecycleIngestor
	wkLifecycleIngestorMu.RUnlock()
	if f != nil {
		f(lc)
	}
}

// wkBuildPublicResponse filters the own profile down to public-only fields.
//
// CONSOLIDATION B-3: this no longer advertises a manual endpoint list. The old
// PEER-40 "endpoints" array was populated from an in-memory registry that
// nothing in production consumed for delivery (delivery uses the single
// contact.Server address, resolved via resolvePeerBaseURL). A peer's real
// reachability is its relay-tunnel name + verified-direct endpoint, discovered
// via the relay (/_vulos-direct/resolve) — not a self-claimed host:port list.
func wkBuildPublicResponse(p WKOwnProfile) WKIdentityResponse {
	resp := WKIdentityResponse{
		VulaID:        p.VulaID,
		DisplayName:   p.DisplayName,
		VerifiedEmail: p.VerifiedEmail,
		Slug:          p.Slug,
	}
	if p.Visibility.Image == WKVisibilityPublic {
		resp.Image = "/api/peering/profile/image"
	}
	if p.Visibility.Bio == WKVisibilityPublic {
		resp.Bio = p.Bio
	}
	return resp
}

// --------------------------------------------------------------------------
// Peer profile cache
// --------------------------------------------------------------------------

// WKPeerProfile is a cached copy of a remote peer's profile as seen from
// this node (may include "peers"-level fields if the peer is an approved contact).
type WKPeerProfile struct {
	VulaID        string `json:"vula_id"`
	DisplayName   string `json:"display_name"`
	Bio           string `json:"bio,omitempty"`
	VerifiedEmail bool   `json:"verified_email"`
	Slug          string `json:"slug,omitempty"`
	Image         string `json:"image,omitempty"`
	// PreKeys is the peer's published X3DH prekey bundle, when present, enabling
	// forward-secret content sessions to this peer (prekeys.go). The signed prekey
	// is verified against the peer's identity before the profile is cached.
	PreKeys *PreKeyBundlePublic `json:"prekeys,omitempty"`
	// Signature is the verified Ed25519 signature from the peer's well-known
	// response (base64url). Retained for auditing; a cached profile is only
	// stored after this signature has been verified against the Vula ID.
	Signature string `json:"sig,omitempty"`
	// CachedAt records when we last fetched this profile.
	CachedAt time.Time `json:"cached_at"`
	// ServerAddr is the address we fetched from.
	ServerAddr string `json:"server_addr"`
}

// wkCacheTTL is how long a cached peer profile is considered fresh.
const wkCacheTTL = 5 * time.Minute

// wkProfileCache is the in-process LRU-style cache (capped at wkCacheMax entries).
var (
	wkCacheMu  sync.RWMutex
	wkCache    = map[string]*WKPeerProfile{} // vula_id -> profile
	wkCacheMax = 1000
)

// wkCacheGet returns a cached peer profile if it is still fresh.
func wkCacheGet(vulaID string) (*WKPeerProfile, bool) {
	wkCacheMu.RLock()
	defer wkCacheMu.RUnlock()
	p, ok := wkCache[vulaID]
	if !ok {
		return nil, false
	}
	if time.Since(p.CachedAt) > wkCacheTTL {
		return nil, false // stale
	}
	return p, true
}

// wkCachePut stores a peer profile in the cache.
func wkCachePut(vulaID string, p *WKPeerProfile) {
	wkCacheMu.Lock()
	defer wkCacheMu.Unlock()
	// Evict oldest entries when at capacity.
	if len(wkCache) >= wkCacheMax {
		var oldest string
		var oldestTime time.Time
		for k, v := range wkCache {
			if oldest == "" || v.CachedAt.Before(oldestTime) {
				oldest = k
				oldestTime = v.CachedAt
			}
		}
		delete(wkCache, oldest)
	}
	wkCache[vulaID] = p
}

// --------------------------------------------------------------------------
// FetchPeerProfile — exported helper
// --------------------------------------------------------------------------

// FetchPeerProfile fetches a peer's public profile from their Vula instance at
// serverAddr (e.g. "alice.vulos.org:8080" or "https://alice.vulos.org").
// It checks the in-process cache first and only hits the network on a cache miss
// or when the cached entry has expired.
//
// serverAddr may be:
//   - a bare host:port (HTTP assumed for local dev, HTTPS for anything else)
//   - a full https:// URL base
func FetchPeerProfile(ctx context.Context, vulaID, serverAddr string) (*WKPeerProfile, error) {
	if vulaID == "" {
		return nil, fmt.Errorf("peering: vulaID required")
	}

	// Cache hit?
	if p, ok := wkCacheGet(vulaID); ok {
		return p, nil
	}

	wk, err := wkFetchAndVerify(ctx, vulaID, serverAddr)
	if err != nil {
		return nil, err
	}

	// Ingest the peer's published key-lifecycle (revocations + rotation/recovery
	// chain) BEFORE building/caching the profile. This is what makes a revocation
	// take effect through the real fetch path: a peer that has self-revoked (or
	// been anchor-revoked) is recorded here, so the admission gate and
	// VerifyVulaSignatureChecked subsequently reject it.
	wkIngestLifecycle(wk.Lifecycle)

	// Fail closed if the fetched identity is now revoked: do not hand back (or
	// cache) a usable profile for a key that has just been retired.
	if isVulaIDRevoked(vulaID) {
		return nil, fmt.Errorf("peering: identity %s is revoked — refusing profile", vulaID)
	}

	// Forward-secrecy prekeys: only surface a bundle whose signed prekey verifies
	// against the (already-authenticated) identity. A bundle with a bad signature
	// is dropped so a sender never establishes a session to an unauthenticated key.
	var preKeys *PreKeyBundlePublic
	if wk.PreKeys != nil {
		if wk.PreKeys.IdentityVulaID == "" {
			wk.PreKeys.IdentityVulaID = vulaID
		}
		if wk.PreKeys.IdentityVulaID == vulaID && wk.PreKeys.VerifySignedPreKey() == nil {
			preKeys = wk.PreKeys
		}
	}

	p := &WKPeerProfile{
		VulaID:        vulaID, // authenticated identity (== wk.VulaID when present)
		DisplayName:   wk.DisplayName,
		Bio:           wk.Bio,
		VerifiedEmail: wk.VerifiedEmail,
		Slug:          wk.Slug,
		Image:         wk.Image,
		PreKeys:       preKeys,
		Signature:     wk.Signature,
		CachedAt:      time.Now(),
		ServerAddr:    serverAddr,
	}

	wkCachePut(vulaID, p)
	return p, nil
}

// wkFetchAndVerify performs the SSRF-guarded GET of a peer's /.well-known/vula-id,
// decodes it, checks the returned Vula ID matches what we expected, and verifies
// the response's Ed25519 signature against that key. It returns the authenticated
// WKIdentityResponse. It is shared by FetchPeerProfile (which then builds/caches a
// profile) and RefreshPeerLifecycle (which only ingests the lifecycle bundle).
func wkFetchAndVerify(ctx context.Context, vulaID, serverAddr string) (WKIdentityResponse, error) {
	var zero WKIdentityResponse

	// Build the well-known URL.
	base := wkNormaliseServerAddr(serverAddr)
	wkURL := base + "/.well-known/vula-id"

	// H1 fix: validate host against the SSRF deny-list before dialling.
	// Peers are external by definition, so allowLAN=false (strict mode).
	// peeringSSRFBypass is a test-only override (package-level var, default false).
	// We also wire safedial.New into the Transport so DNS-rebinding attacks
	// that resolve to a private IP at connect time are caught too.
	if !peeringSSRFBypass {
		host := serverAddr
		if strings.HasPrefix(serverAddr, "http://") || strings.HasPrefix(serverAddr, "https://") {
			if u, parseErr := url.Parse(serverAddr); parseErr == nil {
				host = u.Hostname()
			}
		} else if h, _, splitErr := net.SplitHostPort(serverAddr); splitErr == nil {
			host = h
		}
		if _, ssrfErr := safedial.ValidateHost(host, false); ssrfErr != nil {
			return zero, fmt.Errorf("peering: FetchPeerProfile SSRF guard rejected server %q: %w", serverAddr, ssrfErr)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wkURL, nil)
	if err != nil {
		return zero, fmt.Errorf("peering: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "VulaOS/1.0 (+peering)")

	dialFn := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if peeringSSRFBypass {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
		return safedial.New(false).DialContext(ctx, network, addr)
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext:         dialFn,
			TLSHandshakeTimeout: 5 * time.Second,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return zero, fmt.Errorf("peering: fetch %s: %w", wkURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("peering: well-known returned %d from %s", resp.StatusCode, wkURL)
	}

	var wk WKIdentityResponse
	if err := json.NewDecoder(resp.Body).Decode(&wk); err != nil {
		return zero, fmt.Errorf("peering: decode well-known: %w", err)
	}

	// Validate that the returned Vula ID matches what we expected.
	if wk.VulaID != "" && wk.VulaID != vulaID {
		return zero, fmt.Errorf("peering: vula_id mismatch: expected %s got %s", vulaID, wk.VulaID)
	}

	// Authenticate the profile. The peer profile (VerifiedEmail, DisplayName,
	// …) is otherwise attacker-mutable in transit / by a malicious
	// server, enabling identity spoofing. A Vula ID IS
	// an Ed25519 public key, so require a valid signature over the canonical
	// response by that key. Fail closed on any missing/invalid signature.
	if !wkVerifyResponse(wk, vulaID) {
		return zero, fmt.Errorf("peering: profile for %s has a missing or invalid signature — rejecting (possible spoof/MITM)", vulaID)
	}
	return wk, nil
}

// RefreshPeerLifecycle fetches a peer's authenticated well-known response and
// ingests ONLY its lifecycle bundle (revocations + rotation/recovery chain),
// bypassing the profile cache. It is the propagation path that lets a revocation
// published by a contact take effect on this node WITHOUT waiting for a fresh
// profile fetch (the profile cache could otherwise serve a stale, pre-revocation
// copy for its whole TTL). Errors are returned for logging.
func RefreshPeerLifecycle(ctx context.Context, vulaID, serverAddr string) error {
	wk, err := wkFetchAndVerify(ctx, vulaID, serverAddr)
	if err != nil {
		return err
	}
	wkIngestLifecycle(wk.Lifecycle)
	return nil
}

// wkLifecycleRefreshInterval is how often the background loop re-ingests known
// contacts' lifecycle bundles so a revocation/rotation propagates promptly.
const wkLifecycleRefreshInterval = 10 * time.Minute

// StartLifecycleRefresh launches a background goroutine that periodically
// re-ingests the lifecycle bundles of known contacts (bypassing the profile
// cache), so a revocation or rotation a contact publishes is honored within at most
// wkLifecycleRefreshInterval even if a fresh profile has not been fetched. The
// goroutine exits when ctx is cancelled.
func StartLifecycleRefresh(ctx context.Context, listContacts func() []WKApprovedPeer) {
	go func() {
		ticker := time.NewTicker(wkLifecycleRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, p := range listContacts() {
					if p.VulaID == "" || p.ServerAddr == "" {
						continue
					}
					tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
					if err := RefreshPeerLifecycle(tctx, p.VulaID, p.ServerAddr); err != nil {
						fmt.Fprintf(os.Stderr, "[peering/wellknown] lifecycle refresh %s: %v\n", p.VulaID, err)
					}
					cancel()
				}
			}
		}
	}()
}

// wkNormaliseServerAddr converts a bare host:port or full URL base into a
// consistent https:// base string (http:// for loopback addresses in dev).
func wkNormaliseServerAddr(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	// Bare host or host:port.
	host := addr
	if strings.Contains(host, ":") {
		// host:port — check for loopback for local dev.
		h := host
		if idx := strings.LastIndex(h, ":"); idx > 0 {
			h = h[:idx]
		}
		if h == "localhost" || h == "127.0.0.1" || h == "::1" {
			return "http://" + addr
		}
	}
	return "https://" + addr
}

// --------------------------------------------------------------------------
// RegisterWellKnownHandlers — exported entry point
// --------------------------------------------------------------------------

// RegisterWellKnownHandlers mounts the following routes on mux:
//
//	GET /.well-known/vula-id           — unauthenticated, public fields only
//	GET /api/peering/profile/{vula_id} — fetch + cache a peer's profile
//
// It reads this node's identity and profile from wkPeeringDataDir().
// Both handlers are safe to call concurrently.
func RegisterWellKnownHandlers(mux *http.ServeMux) {
	dataDir := wkPeeringDataDir()

	// Best-effort: ensure the peering data dirs exist.
	for _, sub := range []string{"identity", "profile"} {
		os.MkdirAll(filepath.Join(dataDir, sub), 0700)
	}

	// Load (or generate) our own identity once at startup.
	identity, err := wkLoadOrCreateIdentity(dataDir)
	if err != nil {
		// Non-fatal — log and serve a placeholder.
		fmt.Fprintf(os.Stderr, "[peering] identity init warning: %v\n", err)
		identity = &wkStoredIdentity{VulaID: "vula:ed25519:unconfigured"}
	}
	ownVulaID := identity.VulaID

	// ── GET /.well-known/vula-id ─────────────────────────────────────────
	// Public, unauthenticated. Returns only public-visibility fields.
	// Load this node's private key once so every well-known response can be
	// signed (peers authenticate the profile against the Vula ID public key).
	ownPriv, privErr := identity.privateKey()
	if privErr != nil {
		fmt.Fprintf(os.Stderr, "[peering] cannot load identity private key; well-known responses will be UNSIGNED and rejected by peers: %v\n", privErr)
	}

	mux.HandleFunc("GET /.well-known/vula-id", func(w http.ResponseWriter, r *http.Request) {
		profile := wkLoadOwnProfile(dataDir, ownVulaID)
		resp := wkBuildPublicResponse(profile)
		// Publish key-lifecycle data (rotation/recovery chain + revocations) so
		// peers can follow rotations and honor revocations (identity_lifecycle.go).
		if lc := wkCurrentLifecycle(); lc != nil {
			resp.Lifecycle = lc
		}
		// Publish the X3DH prekey bundle so senders get forward secrecy (prekeys.go).
		if pk := wkCurrentPreKeyBundle(); pk != nil {
			resp.PreKeys = pk
		}
		// Sign so fetchers can verify authenticity against our Vula ID.
		if privErr == nil {
			if err := wkSignResponse(&resp, ownPriv); err != nil {
				fmt.Fprintf(os.Stderr, "[peering] sign well-known response: %v\n", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=60")
		json.NewEncoder(w).Encode(resp)
	})

	// ── GET /api/peering/profile/{vula_id} ───────────────────────────────
	// Fetch a peer's profile by Vula ID.  The vula_id path segment must be
	// URL-encoded.  The caller supplies the peer's server address as the
	// ?server= query parameter (required for a live fetch; optional if the
	// profile is already cached).
	//
	// Visibility enforcement (server-side): the remote server controls what
	// fields it exposes.  This endpoint additionally gates on whether the
	// requested vula_id is an approved local contact (future: reads
	// contacts.json).  For now it exposes whatever the remote server returned
	// (which itself respects visibility = "public" only for unapproved
	// callers).
	mux.HandleFunc("GET /api/peering/profile/{vula_id}", func(w http.ResponseWriter, r *http.Request) {
		vulaID := r.PathValue("vula_id")
		if vulaID == "" {
			wkWriteErr(w, http.StatusBadRequest, "vula_id required")
			return
		}

		// Check in-process cache first (no server addr needed).
		if p, ok := wkCacheGet(vulaID); ok {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			json.NewEncoder(w).Encode(p)
			return
		}

		serverAddr := r.URL.Query().Get("server")
		if serverAddr == "" {
			wkWriteErr(w, http.StatusBadRequest, "server query parameter required for uncached profile")
			return
		}

		p, err := FetchPeerProfile(r.Context(), vulaID, serverAddr)
		if err != nil {
			wkWriteErr(w, http.StatusBadGateway, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "MISS")
		json.NewEncoder(w).Encode(p)
	})

	// ── POST /api/peering/profile/notify-change ──────────────────────────
	// Inbound push from an approved peer: their profile changed and we should
	// evict our cached copy so the next fetch gets fresh data.
	mux.HandleFunc("POST /api/peering/profile/notify-change", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			VulaID string `json:"vula_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.VulaID == "" {
			wkWriteErr(w, http.StatusBadRequest, "vula_id required")
			return
		}

		wkCacheMu.Lock()
		delete(wkCache, req.VulaID)
		wkCacheMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "cache_evicted", "vula_id": req.VulaID})
	})
}

// --------------------------------------------------------------------------
// Periodic profile sync
// --------------------------------------------------------------------------

// WKApprovedPeer is a minimal record of an approved contact used by the
// profile-sync loop.  Only VulaID and ServerAddr are required; everything
// else is informational.
type WKApprovedPeer struct {
	VulaID     string
	ServerAddr string
}

// wkSyncInterval is how often the background sync loop refreshes stale
// approved-peer profiles.
const wkSyncInterval = 15 * time.Minute

// StartPeerProfileSync launches a background goroutine that periodically
// refreshes the cached profiles of approved peers.
//
// listApproved must return the current set of approved contacts; it is
// called on each tick so that newly-approved contacts are included
// automatically.  The goroutine exits when ctx is cancelled.
//
// Typical wiring in cmd/server/main.go (inside the PEER-42 block):
//
//	peering.StartPeerProfileSync(ctx, func() []peering.WKApprovedPeer {
//	    contacts := contactStore.ListByState(peering.StateApproved)
//	    peers := make([]peering.WKApprovedPeer, 0, len(contacts))
//	    for _, c := range contacts {
//	        peers = append(peers, peering.WKApprovedPeer{c.VulaID, c.Server})
//	    }
//	    return peers
//	})
func StartPeerProfileSync(ctx context.Context, listApproved func() []WKApprovedPeer) {
	go func() {
		ticker := time.NewTicker(wkSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				wkRefreshApprovedProfiles(ctx, listApproved())
			}
		}
	}()
}

// FetchPeerProfileAsync triggers a non-blocking background fetch of a single
// peer's profile.  It is a fire-and-forget convenience used by the approve
// handler so that the contact's profile is cached immediately after approval
// without blocking the HTTP response.
func FetchPeerProfileAsync(vulaID, serverAddr string) {
	if vulaID == "" || serverAddr == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := FetchPeerProfile(ctx, vulaID, serverAddr); err != nil {
			fmt.Fprintf(os.Stderr, "[peering/wellknown] async profile fetch %s: %v\n", vulaID, err)
		}
	}()
}

// wkRefreshApprovedProfiles re-fetches profiles that are absent or stale in
// the cache for the given set of approved peers.  Network errors are logged
// and skipped so a single unreachable peer does not abort the refresh.
func wkRefreshApprovedProfiles(ctx context.Context, peers []WKApprovedPeer) {
	for _, p := range peers {
		if p.VulaID == "" || p.ServerAddr == "" {
			continue
		}
		// Skip if there is a still-fresh cache entry.
		if _, ok := wkCacheGet(p.VulaID); ok {
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := FetchPeerProfile(tctx, p.VulaID, p.ServerAddr)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[peering/wellknown] refresh %s: %v\n", p.VulaID, err)
		}
	}
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

func wkWriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
