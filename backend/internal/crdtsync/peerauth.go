package crdtsync

// peerauth.go — WAN peer identity for the CRDT exchange endpoints.
//
// # Why the LAN scheme does not extend to the WAN
//
// The LAN path authenticates with X-Fabric-Auth: ONE shared secret held by
// every box in the fleet. Inside a link-local tunnel that is defensible — the
// listener is pinned to the LAN IP and the secret only ever travels to
// addresses multicast resolved. It has three properties that make it unusable
// over the internet:
//
//  1. It is symmetric. Every holder can impersonate every other holder, so it
//     identifies "a member of this fleet", never "which member".
//  2. It is a bearer token. It is transmitted on every request, so it is
//     disclosed to whatever address the caller dials. A WAN address comes from
//     a rendezvous relay whose /resolve answer is UNSIGNED (see
//     fabric/rendezvous.go's documented trust gap), so dialling it with the
//     secret hands a fleet-wide credential to whoever that relay names.
//  3. It authenticates the REQUEST only. A pull RESPONSE is merged into this
//     box's database. TLS proves the responder holds a certificate for the name
//     the relay chose, which an attacker who controls the relay trivially has
//     for their own domain. Without a response-side proof, a lying relay writes
//     to your database.
//
// # The identity
//
// A peer IS its Ed25519 public key. Specifically the per-instance FABRIC
// signing key (internal/multiinstance.LoadOrCreateSealedInstanceKey, sealed at
// rest under the OS-keyring root key), which this codebase already uses for:
//
//   - signing uninstall observations that peers verify against the ROSTERED
//     public key (CRDT-QUORUM-01, multiinstance/appsync.go),
//   - addressing a box at a rendezvous relay — RendezvousDiscoverer.SelfKey()
//     is that key, and a sibling's PeerKeys entry is that same string,
//   - rotation with an overlap window and revocation (FABRIC-KEY-01,
//     multiinstance/rotation.go).
//
// No new PKI, no new key, no new roster. The key is encoded here exactly as the
// rendezvous layer encodes it (base64url, unpadded), because that string is
// LITERALLY the relay address: "the peer whose key I resolved" and "the peer
// who signed this response" are then the same bytes with no conversion step to
// get wrong. services/peering's Vulos ID is the same idea in a different
// encoding over a DIFFERENT key (peering's own identity key, not the fabric
// one); mixing them here would introduce a second identity for the same box.
//
// # What a signature proves, and what it does not
//
// This mirrors services/fleetid's VerifyVouchRequest, deliberately, down to the
// canonicaliser (services/signing.Canonical over the envelope with Sig
// cleared) and the freshness window:
//
//	It PROVES the message was produced by the holder of the named key,
//	recently, for exactly this method/path/body, addressed to exactly this box.
//	It PROVES NOTHING about whether that peer may write here. Authorisation is a
//	separate, deny-by-default roster check (PeerRoster) — because a fully
//	compromised box holds its own key and signs perfectly.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"vulos/backend/services/signing"
)

// PeerAuthHeader carries the signed request envelope: base64url(raw) of the
// canonical JSON envelope, signature included.
const PeerAuthHeader = "X-Crdt-Peer-Auth"

// PeerAuthResponseHeader carries the signed response envelope. A WAN client
// REQUIRES it on any 200 it is going to merge.
const PeerAuthResponseHeader = "X-Crdt-Peer-Auth-Response"

// Envelope type tags. They are domain separators: an envelope of one type can
// never be replayed as the other, because Type is inside the signed bytes.
const (
	peerAuthRequestType  = "vulos-crdt/peer-auth/1"
	peerAuthResponseType = "vulos-crdt/peer-auth-response/1"
)

const (
	// PeerAuthMaxAge is how long a signed request stays acceptable. It matches
	// the order of fleetid's VouchMaxAge: long enough for a slow WAN round trip
	// and a box whose clock is a little off, short enough that a captured
	// request is not a durable credential.
	PeerAuthMaxAge = 5 * time.Minute
	// PeerAuthClockSkew is how far into the future an issue time may sit before
	// it is refused. Two boxes in two houses do not share a clock.
	PeerAuthClockSkew = 60 * time.Second
	// maxNonceCache bounds the replay cache. Reaching it is not a reason to
	// start accepting replays: the verifier purges expired entries and, if the
	// cache is still full, FAILS CLOSED.
	maxNonceCache = 16384
)

// ── key encoding ─────────────────────────────────────────────────────────────

// EncodePeerKey renders an Ed25519 public key the way the rendezvous layer
// does: base64url, unpadded. This is the peer's identity string.
func EncodePeerKey(pub ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(pub)
}

// DecodePeerKey parses a peer identity string back to a public key.
//
// It accepts base64url-unpadded (the rendezvous/relay encoding) AND
// base64-standard (the encoding multiinstance's roster column uses) so a key
// copied from either place resolves to the same 32 bytes. The DECODED BYTES are
// what is ever compared — never the strings — so the two encodings of one key
// cannot become two identities.
func DecodePeerKey(s string) (ed25519.PublicKey, error) {
	if s == "" {
		return nil, errors.New("crdtsync: empty peer key")
	}
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding,
	} {
		if raw, err := enc.DecodeString(s); err == nil && len(raw) == ed25519.PublicKeySize {
			return ed25519.PublicKey(raw), nil
		}
	}
	return nil, fmt.Errorf("crdtsync: peer key %q is not a base64 Ed25519 public key", redactKey(s))
}

func redactKey(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8] + "…"
}

// samePeerKey compares two identity strings by their DECODED bytes.
func samePeerKey(a, b string) bool {
	ka, err := DecodePeerKey(a)
	if err != nil {
		return false
	}
	kb, err := DecodePeerKey(b)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(ka, kb) == 1
}

// ── the trust boundary ───────────────────────────────────────────────────────

// PeerRoster decides WHICH peers may exchange CRDT state with this box. It is
// the entire security boundary of WAN sync — a peer that passes it can write to
// this box's replicated tables — so it is deliberately the smallest possible
// interface, deny-by-default, and separate from the signature check.
//
// The production implementation (cmd/server) is backed by the existing
// multi-instance roster: an instance the OPERATOR enrolled, that has published
// a key, and that is not revoked. There is no trust-on-first-use, no
// auto-enrolment, and no transitive trust: a trusted peer cannot introduce
// another peer, because roster membership is not a replicated domain (see
// policy.go — app_registry and the instance roster are both refused) and no
// peer-facing handler writes it.
type PeerRoster interface {
	// TrustedPeer reports whether pub may exchange CRDT state right now. It
	// must fail CLOSED on any doubt, and must re-check on every call so a
	// revocation takes effect without a restart.
	TrustedPeer(pub ed25519.PublicKey) bool
}

// PeerAttribution is the optional half of a PeerRoster: "can this box name its
// peers at all yet?"
//
// It exists because eviction is impossible without attribution. The shared
// fabric secret identifies a fleet, not a member, so a request that carries only
// the secret cannot be told apart from a request carrying the SAME secret from a
// box that was revoked an hour ago. Where a box can attribute its peers, the
// secret must therefore stop being sufficient. Where it cannot attribute
// anybody, refusing the secret would not evict a compromised box — there is
// nothing to evict it BY — it would only stop sync working at all.
//
// So the secret survives exactly one window: until this box first learns a peer
// identity. AttributesPeers is what closes that window, and it is deliberately
// answered from state a peer cannot write (see the production implementation's
// doc in cmd/server/crdtsync_wiring.go).
//
// A roster that does not implement it is treated as attributing — window
// CLOSED, signature required. Fail closed, including for a roster written later
// by someone who never read this comment.
type PeerAttribution interface {
	// AttributesPeers reports whether this box has learned at least one PEER
	// identity. A REVOKED peer still counts: a fleet whose only peer has just
	// been evicted must not fall back to accepting the bearer secret, which is
	// precisely the credential that peer still holds.
	AttributesPeers() bool
}

// CanAttributePeers answers PeerAttribution for any roster, fail-closed: a nil
// roster, or one that does not implement the interface, counts as attributing,
// so the caller requires a signature rather than opening the secret path.
func CanAttributePeers(r PeerRoster) bool {
	if a, ok := r.(PeerAttribution); ok {
		return a.AttributesPeers()
	}
	return true
}

// PeerRosterFunc adapts a function to PeerRoster.
type PeerRosterFunc func(pub ed25519.PublicKey) bool

// TrustedPeer implements PeerRoster.
func (f PeerRosterFunc) TrustedPeer(pub ed25519.PublicKey) bool { return f(pub) }

// StaticPeerRoster is a fixed allow-list of peer keys. It is the test seam and
// the fallback for an operator who wants to pin peers explicitly.
type StaticPeerRoster struct {
	keys [][]byte
}

// NewStaticPeerRoster builds an allow-list. An empty list is allowed and
// trusts NOTHING — a roster that trusts nobody is the correct fail-closed
// state, not a configuration error to be papered over with a permissive
// default.
func NewStaticPeerRoster(keys ...string) (*StaticPeerRoster, error) {
	r := &StaticPeerRoster{}
	for _, k := range keys {
		if k == "" {
			continue
		}
		pub, err := DecodePeerKey(k)
		if err != nil {
			return nil, err
		}
		r.keys = append(r.keys, pub)
	}
	return r, nil
}

// AttributesPeers implements PeerAttribution: an explicit pin list with an
// entry in it is exactly "this box can name a peer".
func (r *StaticPeerRoster) AttributesPeers() bool { return len(r.keys) > 0 }

// TrustedPeer implements PeerRoster.
func (r *StaticPeerRoster) TrustedPeer(pub ed25519.PublicKey) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	for _, k := range r.keys {
		if subtle.ConstantTimeCompare(k, pub) == 1 {
			return true
		}
	}
	return false
}

// ── envelopes ────────────────────────────────────────────────────────────────

// peerAuthEnvelope is the signed request envelope.
//
// Every field is inside the signature (Sig is cleared before canonicalisation,
// exactly as fleetid.VouchRequest does it), and each one closes a specific
// attack:
//
//	Type      — an envelope cannot be replayed as a response envelope.
//	PeerKey   — who signed. Self-certifying: the verifier needs no prior state
//	            beyond deciding whether it trusts that key.
//	Audience  — WHO IT IS FOR, and the property the shared secret most
//	            conspicuously lacks. A request captured at box B cannot be
//	            replayed at box C, because C's audience check fails.
//	Method,
//	Path      — a pull envelope cannot be reused on push.
//	BodyHash  — the body cannot be altered in flight or swapped for another.
//	Nonce     — replayed within the freshness window, the verifier rejects it.
//	IssuedAt  — outside the window it is refused regardless of the nonce cache,
//	            which is what bounds how long that cache must remember.
type peerAuthEnvelope struct {
	Type     string `json:"type"`
	PeerKey  string `json:"peer_key"`
	Audience string `json:"audience"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	BodyHash string `json:"body_hash"`
	Nonce    string `json:"nonce"`
	IssuedAt string `json:"issued_at"`
	Sig      string `json:"sig"`
}

// peerRespEnvelope is the signed response envelope.
//
// It carries no timestamp on purpose: it is bound to ReqNonce, a value the
// requester generated moments ago and checks on receipt, so a response is only
// ever valid for the one request that asked for it. That is a tighter binding
// than a time window.
type peerRespEnvelope struct {
	Type     string `json:"type"`
	PeerKey  string `json:"peer_key"`
	Audience string `json:"audience"`
	ReqNonce string `json:"req_nonce"`
	Status   int    `json:"status"`
	BodyHash string `json:"body_hash"`
	Sig      string `json:"sig"`
}

// canonicalPeerBytes is the ONE place an envelope becomes signing bytes.
//
// It is services/signing.Canonical — the same canonicaliser fleetid's
// VouchRequest/VouchCert use — so this codebase has one deterministic-JSON
// discipline rather than a second one invented here. The Sig field is present
// and empty in the preimage (never removed), matching that pattern: an envelope
// is signed over itself with the signature slot blank.
func canonicalPeerBytes(v any) ([]byte, error) { return signing.Canonical(v) }

func bodyHash(b []byte) string {
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func encodeEnvelope(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeEnvelope(header string, into any) error {
	raw, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		return fmt.Errorf("crdtsync: peer auth header is not base64url: %w", err)
	}
	if len(raw) > 8<<10 {
		return errors.New("crdtsync: peer auth header is implausibly large")
	}
	return json.Unmarshal(raw, into)
}

func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ── this box's signing identity ──────────────────────────────────────────────

// PeerIdentity is this box's Ed25519 signing identity for the CRDT exchange.
// Construct it from the SAME private key the fabric uses
// (multiinstance.LoadOrCreateSealedInstanceKey), never a fresh one: a second
// key would be a second identity that no peer's roster contains.
type PeerIdentity struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	id   string
	now  func() time.Time
}

// NewPeerIdentity wraps a private key. It fails rather than degrading: a signer
// that cannot sign would present as an unauthenticated peer.
func NewPeerIdentity(priv ed25519.PrivateKey) (*PeerIdentity, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("crdtsync: NewPeerIdentity: private key is %d bytes, want %d", len(priv), ed25519.PrivateKeySize)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("crdtsync: NewPeerIdentity: key has no Ed25519 public half")
	}
	return &PeerIdentity{priv: priv, pub: pub, id: EncodePeerKey(pub), now: time.Now}, nil
}

// PublicKey returns this box's peer public key.
func (i *PeerIdentity) PublicKey() ed25519.PublicKey { return i.pub }

// ID returns this box's peer identity string (base64url public key).
func (i *PeerIdentity) ID() string { return i.id }

// SignRequest returns the value for PeerAuthHeader and the nonce it used. The
// caller keeps the nonce to check the response against.
//
// audience MUST be the recipient's peer key. Signing with an empty audience is
// refused: an envelope any box would accept is a replayable credential, and the
// only reason to omit it would be not knowing who you are talking to — which is
// itself a reason not to send.
func (i *PeerIdentity) SignRequest(method, path string, body []byte, audience string) (header, nonce string, err error) {
	if audience == "" {
		return "", "", errors.New("crdtsync: SignRequest: refusing to sign a request with no audience — an envelope with no recipient is replayable at every box in the fleet")
	}
	audKey, err := DecodePeerKey(audience)
	if err != nil {
		return "", "", fmt.Errorf("crdtsync: SignRequest: audience: %w", err)
	}
	nonce, err = newNonce()
	if err != nil {
		return "", "", err
	}
	env := peerAuthEnvelope{
		Type:     peerAuthRequestType,
		PeerKey:  i.id,
		Audience: EncodePeerKey(audKey),
		Method:   method,
		Path:     path,
		BodyHash: bodyHash(body),
		Nonce:    nonce,
		IssuedAt: i.now().UTC().Format(time.RFC3339),
	}
	payload, err := canonicalPeerBytes(env)
	if err != nil {
		return "", "", err
	}
	env.Sig = base64.RawURLEncoding.EncodeToString(ed25519.Sign(i.priv, payload))
	header, err = encodeEnvelope(env)
	if err != nil {
		return "", "", err
	}
	return header, nonce, nil
}

// SignResponse returns the value for PeerAuthResponseHeader, binding the
// response body to the request's nonce and to the requester's key.
func (i *PeerIdentity) SignResponse(audience, reqNonce string, status int, body []byte) (string, error) {
	if audience == "" || reqNonce == "" {
		return "", errors.New("crdtsync: SignResponse: audience and request nonce are both required")
	}
	audKey, err := DecodePeerKey(audience)
	if err != nil {
		return "", fmt.Errorf("crdtsync: SignResponse: audience: %w", err)
	}
	env := peerRespEnvelope{
		Type:     peerAuthResponseType,
		PeerKey:  i.id,
		Audience: EncodePeerKey(audKey),
		ReqNonce: reqNonce,
		Status:   status,
		BodyHash: bodyHash(body),
	}
	payload, err := canonicalPeerBytes(env)
	if err != nil {
		return "", err
	}
	env.Sig = base64.RawURLEncoding.EncodeToString(ed25519.Sign(i.priv, payload))
	return encodeEnvelope(env)
}

// ── inbound verification ─────────────────────────────────────────────────────

// PeerVerifier authenticates inbound signed requests and enforces the roster.
// It is safe for concurrent use.
type PeerVerifier struct {
	self   ed25519.PublicKey
	selfID string
	roster PeerRoster
	now    func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time
}

// NewPeerVerifier builds a verifier for this box's public key and roster.
//
// A nil roster is refused, not defaulted. "Verify the signature and skip the
// roster" would authenticate every box on the internet that holds a key —
// which is all of them.
func NewPeerVerifier(self ed25519.PublicKey, roster PeerRoster) (*PeerVerifier, error) {
	if len(self) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("crdtsync: NewPeerVerifier: self public key is %d bytes, want %d", len(self), ed25519.PublicKeySize)
	}
	if roster == nil {
		return nil, errors.New("crdtsync: NewPeerVerifier: a PeerRoster is required — a signature identifies a peer, it does not authorise one")
	}
	return &PeerVerifier{
		self:   self,
		selfID: EncodePeerKey(self),
		roster: roster,
		now:    time.Now,
		seen:   map[string]time.Time{},
	}, nil
}

// VerifyRequest authenticates r and returns the caller's peer identity.
//
// It CONSUMES and RESTORES r.Body: the body hash is inside the signature, so it
// has to be read here, and the handler downstream must still see it. The body
// is capped at MaxBodyBytes, the same cap the handlers apply.
func (v *PeerVerifier) VerifyRequest(r *http.Request) (peerID string, err error) {
	header := r.Header.Get(PeerAuthHeader)
	if header == "" {
		return "", errors.New("crdtsync: no peer auth header")
	}
	var env peerAuthEnvelope
	if err := decodeEnvelope(header, &env); err != nil {
		return "", err
	}
	if env.Type != peerAuthRequestType {
		return "", fmt.Errorf("crdtsync: peer auth: wrong envelope type %q", env.Type)
	}

	// Who signed. Decoding is the only thing the presented key buys — whether
	// it is ALLOWED is the roster's call, below.
	pub, err := DecodePeerKey(env.PeerKey)
	if err != nil {
		return "", fmt.Errorf("crdtsync: peer auth: peer_key: %w", err)
	}

	// Addressed to US. This is what stops a request captured at one box being
	// replayed at another — the whole fleet shares the LAN secret, but no two
	// boxes share a key.
	if !samePeerKey(env.Audience, v.selfID) {
		return "", errors.New("crdtsync: peer auth: envelope is addressed to a different box")
	}

	// Bound to this exact call.
	if env.Method != r.Method {
		return "", fmt.Errorf("crdtsync: peer auth: envelope method %q does not match %q", env.Method, r.Method)
	}
	if env.Path != r.URL.Path {
		return "", fmt.Errorf("crdtsync: peer auth: envelope path %q does not match %q", env.Path, r.URL.Path)
	}

	// Freshness, before the nonce cache — it is what bounds how long the cache
	// has to remember anything.
	issued, err := time.Parse(time.RFC3339, env.IssuedAt)
	if err != nil {
		return "", fmt.Errorf("crdtsync: peer auth: bad issued_at %q", env.IssuedAt)
	}
	now := v.now().UTC()
	if issued.Before(now.Add(-PeerAuthMaxAge)) {
		return "", errors.New("crdtsync: peer auth: stale (outside the freshness window)")
	}
	if issued.After(now.Add(PeerAuthClockSkew)) {
		return "", errors.New("crdtsync: peer auth: issued in the future")
	}

	// The body, hashed and put back.
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("crdtsync: peer auth: read body: %w", err)
	}
	if len(body) > MaxBodyBytes {
		return "", errors.New("crdtsync: peer auth: body too large")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if env.BodyHash != bodyHash(body) {
		return "", errors.New("crdtsync: peer auth: body hash does not match the signed envelope")
	}

	// The signature itself.
	sig, err := base64.RawURLEncoding.DecodeString(env.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", errors.New("crdtsync: peer auth: malformed signature")
	}
	unsigned := env
	unsigned.Sig = ""
	payload, err := canonicalPeerBytes(unsigned)
	if err != nil {
		return "", err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return "", errors.New("crdtsync: peer auth: signature does not verify against peer_key")
	}

	// AUTHORISATION. Everything above is attribution: it proves who sent this.
	// This is the line that decides whether they may. Deliberately last, and
	// deliberately re-read on every request so a revocation lands immediately.
	if !v.roster.TrustedPeer(pub) {
		return "", fmt.Errorf("crdtsync: peer auth: peer %s is not in the roster", redactKey(env.PeerKey))
	}

	// Replay, last: only a request that would otherwise have been accepted is
	// worth spending cache on.
	if err := v.rememberNonce(env.PeerKey, env.Nonce, now); err != nil {
		return "", err
	}
	return env.PeerKey, nil
}

// rememberNonce records a nonce and rejects a repeat inside the freshness
// window.
func (v *PeerVerifier) rememberNonce(peerKey, nonce string, now time.Time) error {
	if nonce == "" {
		return errors.New("crdtsync: peer auth: empty nonce")
	}
	key := peerKey + "\x00" + nonce
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, dup := v.seen[key]; dup {
		return errors.New("crdtsync: peer auth: nonce replay")
	}
	if len(v.seen) >= maxNonceCache {
		cutoff := now.Add(-(PeerAuthMaxAge + PeerAuthClockSkew))
		for k, t := range v.seen {
			if t.Before(cutoff) {
				delete(v.seen, k)
			}
		}
		// Still full: refuse. Forgetting a nonce to make room is exactly the
		// replay this cache exists to stop, so the pressure valve is a rejected
		// request, not a weakened check.
		if len(v.seen) >= maxNonceCache {
			return errors.New("crdtsync: peer auth: replay cache is full — refusing rather than forgetting a nonce")
		}
	}
	v.seen[key] = now
	return nil
}

// ── outbound response verification ───────────────────────────────────────────

// VerifyResponse checks a response envelope against the key the client
// EXPECTED to be talking to.
//
// expectPeer is pinned from discovery, not read from the response: a response
// that names its own signer and is then checked against that name proves only
// that somebody holds some key. This is the check that makes an unsigned
// rendezvous /resolve answer safe to act on — a relay may send this box to any
// address it likes, but the ops it merges still have to come from the key it
// was looking for.
func VerifyResponse(header, expectPeer, selfID, reqNonce string, status int, body []byte) error {
	if header == "" {
		return errors.New("crdtsync: response carries no peer signature")
	}
	var env peerRespEnvelope
	if err := decodeEnvelope(header, &env); err != nil {
		return err
	}
	if env.Type != peerAuthResponseType {
		return fmt.Errorf("crdtsync: response auth: wrong envelope type %q", env.Type)
	}
	if !samePeerKey(env.PeerKey, expectPeer) {
		return errors.New("crdtsync: response auth: signed by a different key than the peer we resolved")
	}
	if !samePeerKey(env.Audience, selfID) {
		return errors.New("crdtsync: response auth: addressed to a different box")
	}
	if env.ReqNonce == "" || subtle.ConstantTimeCompare([]byte(env.ReqNonce), []byte(reqNonce)) != 1 {
		return errors.New("crdtsync: response auth: does not answer this request's nonce")
	}
	if env.Status != status {
		return fmt.Errorf("crdtsync: response auth: signed status %d does not match %d", env.Status, status)
	}
	if env.BodyHash != bodyHash(body) {
		return errors.New("crdtsync: response auth: body hash does not match the signed envelope")
	}
	pub, err := DecodePeerKey(env.PeerKey)
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.DecodeString(env.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("crdtsync: response auth: malformed signature")
	}
	unsigned := env
	unsigned.Sig = ""
	payload, err := canonicalPeerBytes(unsigned)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return errors.New("crdtsync: response auth: signature does not verify")
	}
	return nil
}

// ── authorizers ──────────────────────────────────────────────────────────────

// PeerKeyAuthorizer returns an Authorizer that accepts a request only when it
// carries a valid signature from a rostered peer addressed to this box.
func PeerKeyAuthorizer(v *PeerVerifier) Authorizer {
	if v == nil {
		// A nil verifier authorises NOTHING. RegisterHandlers refuses to mount
		// anything on a nil Authorizer, and this keeps the same posture for a
		// caller that composes authorizers.
		return func(*http.Request) bool { return false }
	}
	return func(r *http.Request) bool {
		_, err := v.VerifyRequest(r)
		return err == nil
	}
}

// AnyOfAuthorizer accepts a request that satisfies ANY of the supplied
// authorizers, in order. With no authorizers it accepts NOTHING.
//
// # Do not use it to combine an ATTRIBUTABLE scheme with an UNATTRIBUTABLE one
//
// It used to compose SecretAuthorizer with PeerKeyAuthorizer at the production
// wiring site, under the argument that this is not a weakening because a
// signature is strictly stronger than the secret. That argument is sound for
// GRANTING access and silent on REVOKING it, which is the case that matters:
// AnyOf returns on the FIRST arm that passes, so a revoked instance still
// holding the fleet-wide secret was accepted on the secret arm and never
// reached the roster check. Eviction was impossible, and the reasoning that
// made it so was written down and looked careful.
//
// The rule the composition has to satisfy is that no arm may admit a caller the
// other arms could not have denied. Use SignedOrBootstrapSecretAuthorizer, which
// states that ordering explicitly, rather than AnyOf with a bearer credential in
// it.
func AnyOfAuthorizer(authz ...Authorizer) Authorizer {
	return func(r *http.Request) bool {
		for _, a := range authz {
			if a != nil && a(r) {
				return true
			}
		}
		return false
	}
}

// SecretAccepter is the rotation ring, seen from the door.
//
// It is an interface rather than a concrete type because internal/fabric owns
// the ring and importing fabric here would invert the dependency (fabric is the
// transport, crdtsync is the engine). *fabric.SecretRing satisfies it
// structurally, and so does any test double.
//
// The contract is narrow on purpose: given a presented X-Fabric-Auth value, say
// whether this box admits it RIGHT NOW. "Right now" is load-bearing — an
// implementation must re-evaluate any expiry on every call, because that is what
// closes a rotation window on a box nobody restarts.
type SecretAccepter interface {
	Accepts(presented string) bool
}

// RingSecretAuthorizer is SecretAuthorizer against a rotation ring instead of a
// single string: during a rotation overlap it admits the current secret AND the
// overlap value, and it stops admitting the overlap value the instant that
// window closes.
//
// It is the same door and the same header. The only thing that changed is that
// the set of acceptable values is now a set, and that set can shrink without a
// restart. A nil ring authorises NOTHING, matching SecretAuthorizer's posture
// for an empty secret: a misconfigured box serves no endpoints rather than open
// ones.
func RingSecretAuthorizer(ring SecretAccepter) Authorizer {
	return func(r *http.Request) bool {
		if ring == nil {
			return false
		}
		return ring.Accepts(r.Header.Get(AuthHeader))
	}
}

// SignedOrBootstrapSecretAuthorizer is the CRDT exchange door.
//
// It admits a caller that presents a valid signature from a rostered peer. It
// admits a caller that presents only the shared fabric secret ONLY while this
// box has learned no peer identity at all — the bootstrap window, closed for
// good by the first enrolment (see PeerAttribution).
//
// Why the secret cannot simply be dropped: on the LAN a peer is dialled with the
// secret and nothing else, and a box that has never been told a peer's public
// key has no way to authenticate one. Refusing the secret there evicts nobody
// and stops sync outright, trading a security hole for an outage.
//
// Why it cannot simply be kept: it is one bearer credential, identical on every
// box in the fleet, so a revoked instance still holds a valid one. While it is
// accepted, the roster check is unreachable for anyone who has it, and no
// eviction of any kind is possible.
//
// Hence: the moment this box can tell one peer from another, it requires that
// it be told. Every arm here fails CLOSED —
//
//	signed  nil → the signature path admits nobody.
//	secret  nil → the bootstrap path admits nobody.
//	roster  nil, or not a PeerAttribution → treated as ATTRIBUTING, i.e. the
//	              window is closed and a signature is required.
//
// Ordering matters and is not an optimisation: the signature is tried FIRST, so
// a rostered peer is admitted (and a revoked one refused) on its own identity
// even during the bootstrap window, rather than being waved through anonymously.
func SignedOrBootstrapSecretAuthorizer(signed, secret Authorizer, roster PeerRoster) Authorizer {
	return func(r *http.Request) bool {
		if signed != nil && signed(r) {
			return true
		}
		if CanAttributePeers(roster) {
			// This box can name its peers, so an anonymous caller is one it
			// could never evict. Refuse.
			return false
		}
		return secret != nil && secret(r)
	}
}
