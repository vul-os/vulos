// Package relayconfig is the SINGLE source of truth for the box's
// relay/reachability + TURN "provider" — the concrete answer to "you use
// Ephor OR bring your own relay".
//
// # Why this exists
//
// Before this package, ICE/relay behaviour was hardcoded (and split-brained)
// across several places: backend/services/peering/ice.go served public
// Google STUN plus whatever the TURN_SECRET/TURN_HOST env vars said, while
// Settings' "TURN / WebRTC" panel persisted an entirely separate, DIFFERENT
// config (network.TURNStore's turn.json) that nothing at serve time ever
// read — an admin could "save" TURN settings that silently had zero effect.
// Meanwhile backend/services/peering/sfu/room.go, backend/services/stream/
// stream.go, and src/builtin/stream/StreamViewer.jsx each hardcoded their own
// copy of the public Google STUN server, deaf to either config. A self-
// hoster who wanted their own TURN cluster, a libp2p Circuit Relay v2 peer,
// or a Tailscale/Headscale/Nebula mesh instead of Vulos's own relay had no
// supported way to do it, and no single place controlled all of it.
//
// This package fixes the split-brain (see effectiveTURNConfig — the
// admin-configured network.TURNStore is now authoritative when set, env
// remains the fallback) and gives every app-facing surface ONE resolver:
// relayconfig.ICEServers(ctx, userID) for app-media ICE. Settings gets ONE
// store to read/write.
//
// # Reachability is THREE orthogonal facets, not one mode
//
// A single "provider" doesn't mean "handles everything" — it means "the
// thing the owner currently has selected", and that thing declares which of
// three independent facets it actually covers (ReachabilityProvider.
// Capabilities). A facet a provider doesn't cover keeps using its EXISTING
// independent gate (Ephor's own default) instead of going dark:
//
//	Facet A — app-media ICE (STUN/TURN for browser WebRTC)
//	Facet B — box HTTP ingress (reaching the box's HTTP surface from outside NAT)
//	Facet C — box<->box rendezvous/discovery
//
// CRITICAL: libp2p and wireguard are TRANSPORT providers — they cover facets
// B/C, not A. Selecting either one must NOT silently zero out ICE (that
// would break Meet/Talk media while looking, from Settings, like a
// perfectly reasonable choice). ICEServers() enforces this: it only ever
// consults the active provider for ICE if that provider actually claims
// FacetICE (ephor, turn); everything else still resolves through ephor's
// ICE default. Callers must never branch on provider name — call
// ICEServers()/IngressInfo()/ResolvePeer() and use the result.
//
// # Persistence + fail-safe
//
// Config is persisted as a single JSON file (<dbDir>/relayconfig.json),
// mirroring gwurl's pattern: Init loads it once at startup, Set
// validates-then-optionally-probes-then-persists-then-swaps atomically, and
// any load/parse/validation failure fails SAFE to the ephor default rather
// than bricking reachability or serving a malformed config. Set itself
// validates (and, for turn/wireguard, health-probes) BEFORE touching any
// persisted or in-memory state, so a rejected or unreachable change never
// affects the currently-active, previously-working provider — the owner is
// never locked out. Settings/loopback/LAN access to this API itself is
// untouched by any provider selection here (this package only ever changes
// what ICEServers/IngressInfo/ResolvePeer report — never the box's own HTTP
// listener), so there is always a local path to revert a bad choice.
//
// # Security
//
//   - Validate enforces well-formed ICE URLs (stun:/stuns:/turn:/turns: with
//     a real host[:port], no embedded userinfo/credentials in the URL
//     itself), well-formed multiaddrs (must carry a /p2p/<id> component —
//     Circuit Relay v2 peers are pinned by peer ID, never bare host), and
//     well-formed WireGuard endpoints (host:port or http(s) URL). Malformed
//     input is rejected outright, never persisted.
//   - This package NEVER stores raw WireGuard/mesh private key material —
//     only the coordinator endpoint (Tailscale/Headscale/Nebula manage their
//     own keys; this seam just points at the coordinator).
//   - Get() (the safe public view) NEVER returns a TURN credential — only
//     whether one is set (HasCredential), matching network.TURNStore's
//     existing write-only secret handling. Credentials generated for ephor
//     are short-lived HMAC (<=1h, see network.TURNConfig.GenerateCredentials).
//   - Changing the provider is a reachability + security-relevant action
//     (see backend/cmd/server/routes_relayconfig.go): gated owner/admin +
//     step-up at the HTTP layer, not in this package.
package relayconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"vulos/backend/services/network"
)

// Provider names the currently-selected relay/reachability backend. Exactly
// one is active at a time (the Settings UX stays a single, simple choice);
// which facets it actually affects is determined by that provider's
// Capabilities(), not by this name — see the package doc's facet model.
type Provider string

const (
	// ProviderEphor is the DEFAULT: Vulos's own relay/TURN/rendezvous path.
	// It has NO special privilege in the resolver — it is registered and
	// dispatched exactly like every other provider — it simply happens to be
	// the one every facet falls back to when nothing else claims them.
	//
	// NOTE ON THE STRING VALUE: changing this persisted string is safe by
	// construction — see providerFor's default case and Init's fail-safe
	// fallback below: ANY unrecognised persisted provider name resolves to
	// this same DefaultConfig() / ephorProvider at both load time and
	// dispatch time, so a box carrying a stale on-disk value reads as "using
	// the default provider" exactly as it did before — it just logs a
	// one-line "unknown relay provider" warning on Init.
	ProviderEphor Provider = "ephor"
	// ProviderNone opts the box's HTTP ingress OUT of the relay tunnel
	// (static IP / port-forward setups). Facets A/C are unaffected (they
	// keep resolving via ephor's fallback).
	ProviderNone Provider = "none"
	// ProviderTURN is a bring-your-own STUN/TURN ICE server list (facet A
	// only). Facets B/C are unaffected.
	ProviderTURN Provider = "turn"
	// ProviderLibp2p is bring-your-own Circuit Relay v2 peer multiaddrs
	// (facets B+C). Facet A (ICE) is unaffected — browsers still need a
	// STUN/TURN answer, which keeps coming from ephor's fallback.
	ProviderLibp2p Provider = "libp2p"
	// ProviderWireGuard points the box's ingress at a Tailscale/Headscale/
	// Nebula coordinator (facet B only). Facets A/C are unaffected.
	ProviderWireGuard Provider = "wireguard"
)

// Valid reports whether p is one of the known providers.
func (p Provider) Valid() bool {
	switch p {
	case ProviderEphor, ProviderNone, ProviderTURN, ProviderLibp2p, ProviderWireGuard:
		return true
	}
	return false
}

// ICEServer mirrors the W3C RTCIceServer dictionary (plus a non-standard,
// browser-ignored "ttl" bookkeeping field the OS already served before this
// package existed) — the SAME shape peering/ice.go and
// network.TURNCredentials already serve to browsers, so a BYO TURN entry
// slots into the exact wire format apps already consume.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
	// TTL is seconds until Credential expires, when known (ephor's
	// time-limited HMAC credential sets this; a static BYO TURN credential
	// leaves it 0/omitted).
	TTL int `json:"ttl,omitempty"`
}

// TURNProviderConfig is the BYO STUN/TURN config (ProviderTURN, facet A).
type TURNProviderConfig struct {
	ICEServers []ICEServer `json:"ice_servers,omitempty"`
}

// Libp2pProviderConfig is the BYO Circuit Relay v2 config (ProviderLibp2p,
// facets B+C).
type Libp2pProviderConfig struct {
	// RelayPeers are full multiaddrs including a /p2p/<peer-id> component,
	// e.g. "/dns4/relay.example.org/tcp/4001/p2p/12D3KooW...".
	RelayPeers []string `json:"relay_peers,omitempty"`
}

// WireGuardProviderConfig points at a WireGuard-mesh coordinator
// (Tailscale/Headscale/Nebula) (ProviderWireGuard, facet B). Deliberately
// carries NO key material — the coordinator manages its own keys; this seam
// only records where it is.
type WireGuardProviderConfig struct {
	// Endpoint is the coordinator's host:port or http(s) URL.
	Endpoint string `json:"endpoint,omitempty"`
	// Network is an optional human label (tailnet name, Nebula network, …).
	Network string `json:"network,omitempty"`
}

// Config is the full persisted + effective shape. Only the section(s)
// matching the active Provider's claimed facets are actually consumed by the
// resolver, but every section is kept around (and validated whenever
// non-empty — see Validate) so switching providers back and forth never
// loses previously-entered settings or lets a stale, malformed section slip
// into the persisted file.
type Config struct {
	Provider  Provider                `json:"provider"`
	TURN      TURNProviderConfig      `json:"turn,omitempty"`
	Libp2p    Libp2pProviderConfig    `json:"libp2p,omitempty"`
	WireGuard WireGuardProviderConfig `json:"wireguard,omitempty"`
	UpdatedAt string                  `json:"updated_at,omitempty"`
}

// DefaultConfig is ephor with no BYO sections configured — the box's
// out-of-the-box posture.
func DefaultConfig() Config {
	return Config{Provider: ProviderEphor}
}

var (
	mu          sync.RWMutex
	state       = DefaultConfig()
	persistPath string

	turnStoreMu  sync.RWMutex
	turnStoreRef *network.TURNStore

	// Test seams.
	nowFn = time.Now
)

// Init points the resolver at dbDir for persistence and loads any previously
// saved provider config. Call once at startup (mirrors gwurl.Init). Idempotent.
//
// Fail-safe: a missing file is not an error (ephor default). A corrupt file
// or a persisted config that no longer validates leaves the resolver on the
// ephor default IN MEMORY (never bricking reachability) but does NOT delete
// the on-disk file, so the owner can see/fix it from Settings rather than
// silently losing their configuration. Either way the error is returned for
// the caller to log.
func Init(dbDir string) error {
	mu.Lock()
	defer mu.Unlock()
	persistPath = filepath.Join(dbDir, "relayconfig.json")
	raw, err := os.ReadFile(persistPath)
	if errors.Is(err, os.ErrNotExist) {
		state = DefaultConfig()
		return nil
	}
	if err != nil {
		state = DefaultConfig()
		return fmt.Errorf("relayconfig: read %s: %w (fell back to ephor default)", persistPath, err)
	}
	var loaded Config
	if err := json.Unmarshal(raw, &loaded); err != nil {
		state = DefaultConfig()
		return fmt.Errorf("relayconfig: parse %s: %w (fell back to ephor default)", persistPath, err)
	}
	if err := Validate(loaded); err != nil {
		state = DefaultConfig()
		return fmt.Errorf("relayconfig: persisted config is no longer valid: %w (fell back to ephor default)", err)
	}
	state = loaded
	return nil
}

// SetTURNStore injects the box's persisted TURN admin config (Settings' "TURN
// / WebRTC" panel — network.TURNStore) as an AUTHORITATIVE source for
// ephor's self-hosted TURN entry, fixing the historical split-brain where
// Settings wrote turn.json but only the TURN_SECRET/TURN_HOST env vars were
// ever consulted at serve time. When the store has no configured host, the
// env-based network.LoadTURNConfig() is used, unchanged. Call once at
// startup, right after network.NewTURNStore. A nil store (or never calling
// this) simply keeps the env-only behaviour.
func SetTURNStore(store *network.TURNStore) {
	turnStoreMu.Lock()
	defer turnStoreMu.Unlock()
	turnStoreRef = store
}

// effectiveTURNConfig resolves ephor's self-hosted TURN entry: the
// admin-configured store (if present and configured) wins, else the env.
func effectiveTURNConfig() network.TURNConfig {
	turnStoreMu.RLock()
	store := turnStoreRef
	turnStoreMu.RUnlock()
	if store != nil {
		rec := store.Get()
		if rec.Configured && rec.Host != "" {
			port := rec.Port
			if port == 0 {
				port = 3478
			}
			realm := rec.Realm
			if realm == "" {
				realm = "vulos"
			}
			secret := store.Secret()
			return network.TURNConfig{
				Host:    rec.Host,
				Port:    port,
				Realm:   realm,
				Secret:  secret,
				Enabled: secret != "",
			}
		}
	}
	return network.LoadTURNConfig()
}

// EffectiveTURNConfig is the exported form of effectiveTURNConfig: the ONE
// TURN config any caller (app-media ICE, /api/turn/credentials, status
// surfaces) should mint credentials or report status from, so there is no
// second env-only producer split-brained against the admin-configured
// network.TURNStore.
func EffectiveTURNConfig() network.TURNConfig {
	return effectiveTURNConfig()
}

// currentConfig returns a copy of the effective in-memory config.
func currentConfig() Config {
	mu.RLock()
	defer mu.RUnlock()
	return state
}

// CurrentProvider returns the active provider (defaults to ephor before
// Init or when nothing has ever been configured).
func CurrentProvider() Provider {
	return currentConfig().Provider
}

// Set validates newCfg and, only if valid (and, for facet-B-risk providers,
// only if a best-effort health probe succeeds — see probeCandidate),
// persists it and swaps it in as the effective config. On any validation,
// probe, or persistence failure the CURRENT config is left completely
// untouched — Set never partially applies a change, so a bad request can
// never brick reachability and the owner is never locked out of the
// previously-working provider.
//
// Pass force=true to skip the health probe (e.g. a TURN/WireGuard endpoint
// that is only reachable from clients, not from the box itself, or one whose
// DNS has not propagated yet) — structural validation still always applies.
func Set(newCfg Config, force bool) (PublicView, error) {
	if err := Validate(newCfg); err != nil {
		return PublicView{}, err
	}
	if !force {
		if err := probeCandidate(newCfg); err != nil {
			return PublicView{}, fmt.Errorf("%w (pass force=true to save anyway)", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if persistPath == "" {
		return PublicView{}, errors.New("relayconfig: not initialised")
	}
	toPersist := newCfg
	toPersist.UpdatedAt = nowFn().UTC().Format(time.RFC3339)
	blob, err := json.MarshalIndent(toPersist, "", "  ")
	if err != nil {
		return PublicView{}, fmt.Errorf("relayconfig: encode: %w", err)
	}
	if err := writeFileAtomic(persistPath, blob, 0o600); err != nil {
		return PublicView{}, fmt.Errorf("relayconfig: persist: %w", err)
	}
	state = toPersist
	return buildPublicView(toPersist), nil
}

// ResetToEphor clears any BYO configuration and reverts to the ephor
// default. Equivalent to Set(DefaultConfig(), true) but named for the common
// "go back to the default relay" action in Settings. Never probed — ephor
// is always assumed safe to return to.
func ResetToEphor() (PublicView, error) {
	return Set(DefaultConfig(), true)
}

// PublicICEServer is the credential-redacted view of an ICEServer returned by
// Get() — the shared secret / long-term credential is write-only, matching
// network.TURNStore's existing posture (never returned once saved).
type PublicICEServer struct {
	URLs          []string `json:"urls"`
	Username      string   `json:"username,omitempty"`
	HasCredential bool     `json:"has_credential"`
}

// PublicTURNView is the redacted TURN section of PublicView.
type PublicTURNView struct {
	ICEServers []PublicICEServer `json:"ice_servers,omitempty"`
}

// PublicView is the safe, secret-free shape returned by Get() and Set() for
// display in Settings and API responses. It never carries a TURN credential.
type PublicView struct {
	Provider  Provider                `json:"provider"`
	TURN      PublicTURNView          `json:"turn"`
	Libp2p    Libp2pProviderConfig    `json:"libp2p"`
	WireGuard WireGuardProviderConfig `json:"wireguard"`
	UpdatedAt string                  `json:"updated_at,omitempty"`
}

func buildPublicView(cfg Config) PublicView {
	pv := PublicView{
		Provider:  cfg.Provider,
		Libp2p:    cfg.Libp2p,
		WireGuard: cfg.WireGuard,
		UpdatedAt: cfg.UpdatedAt,
	}
	for _, s := range cfg.TURN.ICEServers {
		pv.TURN.ICEServers = append(pv.TURN.ICEServers, PublicICEServer{
			URLs:          s.URLs,
			Username:      s.Username,
			HasCredential: s.Credential != "",
		})
	}
	return pv
}

// Get returns the current provider config with any secrets redacted — safe
// to serve to any authenticated user (mirrors GET /api/turn/config).
func Get() PublicView {
	return buildPublicView(currentConfig())
}

// writeFileAtomic writes data to a temp file in the same directory and
// renames it over path, so a crash mid-write never leaves a truncated
// relayconfig.json (same pattern as gwurl.writeFileAtomic).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".relayconfig-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
