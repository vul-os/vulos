// no-broker-dep:allow-file: comments state this rendezvous role is wire-identical to Ephor's, so a
// Vulos relay is a drop-in replacement for an Ephor one and vice versa
// -- wire-compatibility for operator choice, not a dependency.

// Package rendezvous is the relay's DISCOVERY role: the small, content-blind
// service that lets two boxes find each other when they are not on the same
// LAN.
//
// # Why this exists separately from the tunnel
//
// The tunnel makes ONE box reachable from the public internet. Rendezvous
// answers a different question: "where is the box holding key K right now?"
// Two of your own boxes in two different houses can each be perfectly
// reachable and still never find one another, because mDNS only sees the
// local link. This closes that, and it is the reason a relay is worth running
// even for someone whose boxes all have public IPs.
//
// # It is a lookup table, not an authority
//
// A rendezvous node stores opaque, self-signed claims keyed by public key and
// hands them back. It learns which keys are online and what endpoints they
// claim — that is the unavoidable price of NAT traversal — and nothing else.
// It never sees application data, and it NEVER DIALS an announced endpoint:
// endpoints are stored and echoed verbatim, so this role has no outbound
// request surface and therefore no SSRF surface of its own.
//
// A node that lies can withhold a peer or point a caller at an address the
// caller cannot authenticate to. It cannot forge anything, because every
// announcement is signed by the key it is filed under, and because the
// changeset exchange that follows discovery runs directly to the peer over
// TLS and checks signatures of its own. This is why running several
// rendezvous nodes under different operators is the recommended posture: a
// single node's silence is survivable, and its lies are detectable by
// comparing answers.
//
// # Wire compatibility
//
// The protocol here is byte-identical to the one backend/internal/fabric's
// RendezvousDiscoverer already speaks, which is in turn identical to Ephor's
// rendezvous role. A Vulos relay is therefore a drop-in for an Ephor one and
// vice versa: an operator can move between them by changing a URL, and a box
// does not know or care which it is talking to. That interchangeability is
// the point — it is what makes the coordinator swappable rather than a
// dependency.
package rendezvous

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DomainAnnounce is the domain-separation tag for an announcement's signing
// preimage. It MUST match the client's constant of the same value
// (backend/internal/fabric/rendezvous.go) — a mismatch is a signature
// failure, not a subtle bug. A test pins them together.
const DomainAnnounce = "vulos-rdv/announce/1"

// DefaultPathPrefix is where the role mounts.
const DefaultPathPrefix = "/rendezvous"

// Bounds. Every one of these exists because this endpoint is unauthenticated:
// anyone who can reach the relay can announce under a key they generated, so
// the only thing standing between the service and a memory-exhaustion attack
// is that each of these is finite.
const (
	// MaxEntries caps distinct keys held. On overflow the oldest-expiring
	// entries are evicted, so an attacker generating keys degrades discovery
	// for others rather than taking the process down.
	MaxEntries = 10_000
	// MaxEndpointsPerKey caps announced endpoints per key.
	MaxEndpointsPerKey = 8
	// MaxEndpointLen caps one endpoint URL.
	MaxEndpointLen = 512
	// MaxMetaLen caps the opaque meta string.
	MaxMetaLen = 256
	// MaxBodyBytes caps an announce body read.
	MaxBodyBytes = 16 << 10
	// MaxTTL caps how long an announcement stays live regardless of what it
	// asks for, so a single request cannot pin memory indefinitely.
	MaxTTL = 30 * time.Minute
	// DefaultTTL applies when an announcement asks for none.
	DefaultTTL = 5 * time.Minute
	// MaxClockSkew is how far an announcement's timestamp may be from the
	// node's clock. It bounds replay: an announcement captured off the wire
	// is useless once it falls outside this window.
	MaxClockSkew = 5 * time.Minute
)

// Config configures the role.
type Config struct {
	// PathPrefix is the mount point. Empty uses DefaultPathPrefix.
	PathPrefix string
	// DisablePublicResolve requires resolves to come from a caller that has
	// itself announced. Off by default: resolve is a lookup of a value the
	// holder chose to publish, and gating it would break first contact.
	DisablePublicResolve bool
	// AllowedOrigins, when non-empty, restricts the browser CORS allowlist.
	// This is NOT access control — writes are signed and reads are public —
	// it only limits which pages may call the role from a browser.
	AllowedOrigins []string
	// Logf receives operator-facing log lines.
	Logf func(format string, args ...any)
	// nowFn is a test seam.
	nowFn func() time.Time
}

// entry is one live announcement.
type entry struct {
	key       string
	endpoints []string
	meta      string
	ts        int64
	expiresAt time.Time
}

// Service implements the role. Zero value is not usable; use New.
type Service struct {
	cfg    Config
	prefix string
	nowFn  func() time.Time

	mu      sync.RWMutex
	entries map[string]*entry
}

// New builds the service.
func New(cfg Config) *Service {
	if cfg.PathPrefix == "" {
		cfg.PathPrefix = DefaultPathPrefix
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.nowFn == nil {
		cfg.nowFn = time.Now
	}
	return &Service{
		cfg:     cfg,
		prefix:  strings.TrimRight(cfg.PathPrefix, "/"),
		nowFn:   cfg.nowFn,
		entries: make(map[string]*entry),
	}
}

// Prefix reports the mount point.
func (s *Service) Prefix() string { return s.prefix }

// Handler returns the role's HTTP handler, already scoped to its prefix.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+s.prefix+"/announce", s.handleAnnounce)
	mux.HandleFunc("GET "+s.prefix+"/resolve/{key}", s.handleResolve)
	mux.HandleFunc("OPTIONS "+s.prefix+"/", s.handlePreflight)
	return s.withCORS(mux)
}

// --- announce --------------------------------------------------------------

type announceRequest struct {
	Key       string   `json:"key"`
	Endpoints []string `json:"endpoints"`
	Meta      string   `json:"meta"`
	TTL       int64    `json:"ttl"`
	Nonce     string   `json:"nonce"`
	TS        int64    `json:"ts"`
	Sig       string   `json:"sig"`
}

func (s *Service) handleAnnounce(w http.ResponseWriter, r *http.Request) {
	var req announceRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "malformed announcement", http.StatusBadRequest)
		return
	}
	if err := s.store(req); err != nil {
		// One response for every rejection reason. Distinguishing "bad
		// signature" from "stale timestamp" would tell someone probing the
		// service exactly which part of a forged announcement to fix.
		http.Error(w, "announcement rejected", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, `{"ok":true}`)
}

// store validates and files an announcement.
func (s *Service) store(req announceRequest) error {
	pub, err := decodeKey(req.Key)
	if err != nil {
		return err
	}
	if len(req.Endpoints) == 0 {
		return errors.New("no endpoints")
	}
	if len(req.Endpoints) > MaxEndpointsPerKey {
		return fmt.Errorf("more than %d endpoints", MaxEndpointsPerKey)
	}
	for _, e := range req.Endpoints {
		if err := validateEndpoint(e); err != nil {
			return err
		}
	}
	if len(req.Meta) > MaxMetaLen {
		return errors.New("meta too long")
	}
	if req.Nonce == "" || len(req.Nonce) > 128 {
		return errors.New("bad nonce")
	}

	now := s.nowFn()
	skew := time.Duration(now.Unix()-req.TS) * time.Second
	if skew < 0 {
		skew = -skew
	}
	if skew > MaxClockSkew {
		return fmt.Errorf("timestamp is %s from this node's clock", skew)
	}

	// VERIFY THE SIGNATURE. The key IS the identity: an announcement is a
	// self-signed claim, and verifying it against the key it will be filed
	// under is exactly the right check — it proves the holder of that private
	// key made this claim, which is the only thing a lookup table needs to
	// know. Field order is fixed by the wire contract and must not be
	// reordered: key, ts, ttl, nonce, meta, endpoints...
	sig, err := base64.RawURLEncoding.DecodeString(req.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("bad signature encoding")
	}
	fields := []string{
		req.Key,
		strconv.FormatInt(req.TS, 10),
		strconv.FormatInt(req.TTL, 10),
		req.Nonce,
		req.Meta,
	}
	fields = append(fields, req.Endpoints...)
	if !ed25519.Verify(pub, CanonicalMessage(DomainAnnounce, fields...), sig) {
		return errors.New("signature does not verify")
	}

	ttl := time.Duration(req.TTL) * time.Second
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// REPLAY / ROLLBACK GUARD. An announcement captured off the wire is
	// already bounded by the skew window above, but within that window it
	// could still be replayed to roll a key BACK to an older set of
	// endpoints — pointing that peer's correspondents at an address it has
	// moved away from. Requiring a strictly newer timestamp closes that.
	if prev, ok := s.entries[req.Key]; ok && req.TS < prev.ts {
		return errors.New("stale announcement")
	}

	s.evictIfFullLocked()
	s.entries[req.Key] = &entry{
		key:       req.Key,
		endpoints: append([]string(nil), req.Endpoints...),
		meta:      req.Meta,
		ts:        req.TS,
		expiresAt: now.Add(ttl),
	}
	return nil
}

// evictIfFullLocked makes room when the table is full, dropping expired
// entries first and then the soonest-to-expire. Caller holds mu.
func (s *Service) evictIfFullLocked() {
	if len(s.entries) < MaxEntries {
		return
	}
	now := s.nowFn()
	for k, e := range s.entries {
		if now.After(e.expiresAt) {
			delete(s.entries, k)
		}
	}
	if len(s.entries) < MaxEntries {
		return
	}
	// Still full: drop the tenth soonest-to-expire, so eviction is amortised
	// rather than happening on every subsequent announcement.
	type kv struct {
		k string
		t time.Time
	}
	all := make([]kv, 0, len(s.entries))
	for k, e := range s.entries {
		all = append(all, kv{k, e.expiresAt})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	drop := len(all) / 10
	if drop < 1 {
		drop = 1
	}
	for i := 0; i < drop; i++ {
		delete(s.entries, all[i].k)
	}
	s.cfg.Logf("[rendezvous] table full: evicted %d entries", drop)
}

// --- resolve ---------------------------------------------------------------

type resolveResponse struct {
	Key       string   `json:"key"`
	Online    bool     `json:"online"`
	Endpoints []string `json:"endpoints,omitempty"`
	Meta      string   `json:"meta,omitempty"`
}

func (s *Service) handleResolve(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if _, err := decodeKey(key); err != nil {
		http.Error(w, "bad key", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	e := s.entries[key]
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	if e == nil || s.nowFn().After(e.expiresAt) {
		// 404 for "not announced" and for "expired" alike. An offline peer is
		// the normal case, not an error, and the client treats it as such.
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(resolveResponse{Key: key, Online: false})
		return
	}
	_ = json.NewEncoder(w).Encode(resolveResponse{
		Key:       key,
		Online:    true,
		Endpoints: e.endpoints,
		Meta:      e.meta,
	})
}

// --- CORS ------------------------------------------------------------------

func (s *Service) handlePreflight(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// withCORS allows browser callers. This is deliberately permissive by default
// and is NOT access control: announces are signature-gated and resolves return
// only what a key holder chose to publish, so an origin restriction would add
// no security — it exists solely for operators who want to limit which pages
// may use their node.
func (s *Service) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) originAllowed(origin string) bool {
	if len(s.cfg.AllowedOrigins) == 0 {
		return true
	}
	for _, o := range s.cfg.AllowedOrigins {
		if strings.EqualFold(strings.TrimSpace(o), origin) {
			return true
		}
	}
	return false
}

// --- helpers ---------------------------------------------------------------

// CanonicalMessage builds the domain-separated, length-prefixed signing
// preimage: each segment prefixed by its big-endian uint32 length, domain
// first.
//
// The length prefixing is what makes this safe: without it, the fields
// "ab"+"c" and "a"+"bc" would produce the same bytes, so a signature over one
// set of values would validate for a different set. With it, no rearrangement
// of field boundaries can collide.
//
// Byte-identical to backend/internal/fabric's canonicalMessage and to Ephor's
// keyauth.CanonicalMessage — a wire contract pinned by a shared test vector.
func CanonicalMessage(domain string, fields ...string) []byte {
	total := 4 + len(domain)
	for _, f := range fields {
		total += 4 + len(f)
	}
	buf := make([]byte, 0, total)
	var lp [4]byte
	appendSeg := func(s string) {
		binary.BigEndian.PutUint32(lp[:], uint32(len(s)))
		buf = append(buf, lp[:]...)
		buf = append(buf, s...)
	}
	appendSeg(domain)
	for _, f := range fields {
		appendSeg(f)
	}
	return buf
}

// decodeKey parses a base64url (unpadded) Ed25519 public key.
func decodeKey(key string) (ed25519.PublicKey, error) {
	if key == "" {
		return nil, errors.New("empty key")
	}
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		return nil, fmt.Errorf("key is not unpadded base64url: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// validateEndpoint bounds and shape-checks an announced endpoint.
//
// Note what is NOT checked: whether the address is reachable, private, or
// sensible. The node never dials these — it stores and echoes them — so
// screening them would provide no protection to the node and would let it
// silently drop endpoints that are perfectly valid on the caller's network
// (LAN addresses between two boxes in one house are the common case). The
// party that dials is the one that must screen, and it does.
func validateEndpoint(e string) error {
	if e == "" || len(e) > MaxEndpointLen {
		return fmt.Errorf("endpoint length out of range")
	}
	u, err := url.Parse(e)
	if err != nil {
		return fmt.Errorf("endpoint %q is not a URL", e)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("endpoint %q must be http or https", e)
	}
	if u.Host == "" {
		return fmt.Errorf("endpoint %q has no host", e)
	}
	if u.User != nil {
		return fmt.Errorf("endpoint %q must not embed credentials", e)
	}
	return nil
}

// Stats is the operator-facing view of the table.
type Stats struct {
	Live int `json:"live"`
}

// Stats reports how many announcements are currently live.
func (s *Service) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.nowFn()
	live := 0
	for _, e := range s.entries {
		if now.Before(e.expiresAt) {
			live++
		}
	}
	return Stats{Live: live}
}
