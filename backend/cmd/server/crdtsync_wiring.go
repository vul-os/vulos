package main

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/crdtsync"
	"vulos/backend/internal/fabric"
	"vulos/backend/internal/multiinstance"
	"vulos/backend/internal/sqlcrdt"
)

// fabricPeerRoster answers "may this key exchange CRDT state with us" from the
// multi-instance roster — the SAME roster that already decides whose signed
// uninstall observations count toward quorum (CRDT-QUORUM-01) and whose key the
// rendezvous relay is asked to resolve.
//
// This is the entire authorisation boundary for WAN sync, so it is worth being
// explicit about what it means:
//
//   - Who authorises a new WAN peer: the OPERATOR, by enrolling that instance
//     in this box's roster. That is the same act that already makes a box a
//     fleet member. There is no trust-on-first-use, no "seen at the relay
//     therefore trusted", and no vouch chain: a peer cannot introduce a peer.
//
//   - Why the roster cannot be captured by the thing it authorises: no
//     peer-facing handler writes it. The roster is written by local
//     provisioning (multiinstance/provision.go), by local key rotation
//     (rotation.go), by this box publishing its OWN key (appsync SetIdentity),
//     and by control-plane sync (cloudsync.go, the operator's own account). The
//     fabric changeset path applies app_registry rows only — it never inserts
//     an instance or a public key — and the instance roster is not a
//     crdtsync-replicated domain (crdtsync/policy.go refuses app_registry
//     outright). So a peer that gains sync access cannot use it to grant sync
//     access to anyone else.
//
//   - Revocation and rotation come free, because they are already modelled
//     here: a revoked instance is refused outright, and a key inside its
//     rotation-overlap window is still accepted until PrevKeyExpiresAt, exactly
//     as multiinstance's own verifyChangesetSignature treats them.
//
// It re-reads the roster on every call rather than caching, so revoking a peer
// takes effect on the next request rather than the next restart.
//
// # Deny dominates allow
//
// TrustedPeer sweeps the WHOLE roster for a reason to refuse before it looks for
// a reason to admit. That ordering is the point of this type: it used to skip
// revoked rows with a `continue`, which is correct only while exactly one row
// can carry a given key and only while the roster is the only allow arm. Neither
// is guaranteed — an operator pin list is a second allow arm, and two rows can
// name one key across a rotation. Structuring it as deny-sweep-then-allow means
// a new allow arm cannot be added without the deny arm already having run.
type fabricPeerRoster struct {
	reg *multiinstance.Registry
	// selfULID is this box's own instance ULID, set by startCRDTSync. It is
	// excluded from AttributesPeers: this box always publishes its OWN key into
	// the roster (appsync SetIdentity), so counting it would make every box
	// claim it can attribute peers the moment it starts.
	selfULID string
}

// TrustedPeer implements crdtsync.PeerRoster. It fails CLOSED on every error
// path: an unreadable roster trusts nobody.
func (r fabricPeerRoster) TrustedPeer(pub ed25519.PublicKey) bool {
	if r.reg == nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	pol := operatorPeerPolicy()
	// The operator's deny list first, before the roster is even read: it is the
	// one eviction lever that works when the roster cannot be written (no
	// control plane, a read-only or corrupt registry) and it must not depend on
	// anything that could fail open.
	if pol.deniesKey(pub) {
		return false
	}
	insts, err := r.reg.List()
	if err != nil {
		return false
	}

	// PASS 1 — revocation. Every row, both key slots, before any allow arm.
	// The rotation-overlap expiry is deliberately IGNORED here: a revoked
	// instance is refused under its previous key whether that window is open or
	// closed. FABRIC-KEY-01.
	for _, in := range insts {
		if !in.Revoked && !pol.deniesInstance(in.ULID) {
			continue
		}
		if keyMatches(in.Ed25519PublicKey, pub) || keyMatches(in.PrevEd25519PublicKey, pub) {
			return false
		}
	}

	// PASS 2 — the roster's allow arm.
	now := time.Now().UTC()
	for _, in := range insts {
		if keyMatches(in.Ed25519PublicKey, pub) {
			return true
		}
		// Rotation overlap: the previous key stays valid only until its
		// window closes. A zero expiry means no overlap is active, not an
		// unbounded one.
		if in.PrevEd25519PublicKey != "" && !in.PrevKeyExpiresAt.IsZero() &&
			now.Before(in.PrevKeyExpiresAt.UTC()) && keyMatches(in.PrevEd25519PublicKey, pub) {
			return true
		}
	}

	// PASS 3 — the operator's pin list. It exists because nothing in this
	// codebase writes a PEER's public key into a local roster: appsync publishes
	// SELF's key, and CloudSyncer publishes peers' keys from the control plane.
	// A box with no control plane can therefore attribute nobody, and a peer
	// nobody can be named cannot be evicted. VULOS_FABRIC_PEER_KEYS is how an
	// operator names them by hand. It is an ALLOW arm only — pass 1 already ran,
	// so a pinned key that is also revoked is refused.
	return pol.allowsKey(pub)
}

// AttributesPeers implements crdtsync.PeerAttribution — "can this box tell one
// peer from another yet?", which is what decides whether the shared secret is
// still accepted (see crdtsync.SignedOrBootstrapSecretAuthorizer).
//
// It answers YES if any peer identity is known, INCLUDING a revoked one. That
// case is the whole reason the question is asked: a fleet whose only peer has
// just been evicted must not answer "I know nobody" and reopen the bearer-secret
// path the evicted box still holds a valid credential for.
//
// Every failure answers YES as well — an unreadable roster means a signature is
// required, never that the secret is waved through.
func (r fabricPeerRoster) AttributesPeers() bool {
	if operatorPeerPolicy().hasPins() {
		return true
	}
	if r.reg == nil {
		return true
	}
	insts, err := r.reg.List()
	if err != nil {
		return true
	}
	for _, in := range insts {
		if in.ULID == r.selfULID {
			continue
		}
		if in.Ed25519PublicKey != "" || in.PrevEd25519PublicKey != "" {
			return true
		}
	}
	return false
}

// PeerKeyForInstance returns the current public key rostered for instanceID, or
// "" when the instance is unknown, has published no key, or is REVOKED.
//
// It is used two ways by the wiring, and refusing a revoked instance serves
// both: it is the audience this box signs a LAN request to, and its absence is
// what drops that peer from the dial list. Eviction has to cover the PULL
// direction too — a revoked box that we keep dialling serves us ops we then
// merge, and refusing its requests would not have stopped that.
func (r fabricPeerRoster) PeerKeyForInstance(instanceID string) string {
	if r.reg == nil || instanceID == "" || instanceID == r.selfULID {
		return ""
	}
	in, ok := r.reg.Get(instanceID)
	if !ok || in.Revoked || in.Ed25519PublicKey == "" {
		return ""
	}
	if operatorPeerPolicy().deniesInstance(instanceID) {
		return ""
	}
	if key, err := crdtsync.DecodePeerKey(in.Ed25519PublicKey); err == nil && operatorPeerPolicy().deniesKey(key) {
		return ""
	}
	return in.Ed25519PublicKey
}

// ── the operator's peer policy ───────────────────────────────────────────────
//
// Two environment variables, read once, comma/whitespace separated:
//
//	VULOS_FABRIC_PEER_KEYS     — peer public keys this operator vouches for.
//	                             An ALLOW arm, subordinate to every deny arm.
//	VULOS_FABRIC_REVOKED_PEERS — instance ULIDs and/or peer public keys that are
//	                             evicted. A DENY arm, dominant over everything.
//
// They exist because the machinery below them was unreachable. `Instance.Revoked`
// was enforced in three places and set in NONE: AppSync.RevokePeer had no
// production caller, and the control plane's own instance wire type (CloudInstance)
// carries no revoked field at all, so a cloud sync could not set it either. An
// enforcement path with no trigger is not a security control. This is the trigger,
// it works with no control plane and no network, and it is the operator — the same
// party that enrols a box — who pulls it.
//
// A denied peer is ALSO written into the roster as revoked at startup
// (applyOperatorRevocations), so the eviction persists after the variable is
// removed and reaches the OTHER enforcement point, appsync's quorum verification.
// The in-memory list is what the door checks, so a failed write cannot fail open.
const (
	envFabricPeerKeys     = "VULOS_FABRIC_PEER_KEYS"
	envFabricRevokedPeers = "VULOS_FABRIC_REVOKED_PEERS"
)

type peerPolicy struct {
	pinned     [][]byte
	deniedKeys [][]byte
	deniedIDs  map[string]bool
}

// operatorPeerPolicy parses the policy once per process. Once, not per request:
// an operator editing the environment of a running process is not a thing that
// happens, and re-reading it per request would put a syscall on the auth path.
// A change takes effect on restart — which is also when the roster write below
// makes it permanent.
var operatorPeerPolicy = sync.OnceValue(loadOperatorPeerPolicy)

func loadOperatorPeerPolicy() *peerPolicy {
	p := &peerPolicy{deniedIDs: map[string]bool{}}
	for _, raw := range splitPolicyList(os.Getenv(envFabricPeerKeys)) {
		key, err := crdtsync.DecodePeerKey(raw)
		if err != nil {
			// Refuse the entry, never the whole list: silently widening trust is
			// the failure mode that matters, and an unparseable ALLOW entry that
			// is dropped can only narrow it.
			log.Printf("[crdtsync] %s: ignoring unparseable peer key %q: %v", envFabricPeerKeys, redactPolicyEntry(raw), err)
			continue
		}
		p.pinned = append(p.pinned, key)
	}
	for _, raw := range splitPolicyList(os.Getenv(envFabricRevokedPeers)) {
		if key, err := crdtsync.DecodePeerKey(raw); err == nil {
			p.deniedKeys = append(p.deniedKeys, key)
			continue
		}
		// Not a key, so it is an instance ULID. An entry that parses as neither
		// is still kept as a ULID: a deny list must never silently drop an entry
		// the operator meant as an eviction. A ULID that matches no instance
		// denies nothing, which is the safe direction for a typo.
		p.deniedIDs[raw] = true
	}
	if len(p.pinned) > 0 || len(p.deniedKeys) > 0 || len(p.deniedIDs) > 0 {
		log.Printf("[crdtsync] operator peer policy: %d pinned key(s), %d evicted key(s), %d evicted instance ULID(s)",
			len(p.pinned), len(p.deniedKeys), len(p.deniedIDs))
	}
	return p
}

func splitPolicyList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func redactPolicyEntry(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8] + "…"
}

func (p *peerPolicy) hasPins() bool { return p != nil && len(p.pinned) > 0 }

func (p *peerPolicy) allowsKey(pub ed25519.PublicKey) bool {
	if p == nil {
		return false
	}
	for _, k := range p.pinned {
		if subtle.ConstantTimeCompare(k, pub) == 1 {
			return true
		}
	}
	return false
}

func (p *peerPolicy) deniesKey(pub ed25519.PublicKey) bool {
	if p == nil {
		return false
	}
	for _, k := range p.deniedKeys {
		if subtle.ConstantTimeCompare(k, pub) == 1 {
			return true
		}
	}
	return false
}

func (p *peerPolicy) deniesInstance(ulid string) bool {
	return p != nil && ulid != "" && p.deniedIDs[ulid]
}

// applyOperatorRevocations persists VULOS_FABRIC_REVOKED_PEERS into the roster's
// revoked column, so the eviction outlives the environment variable and reaches
// appsync's quorum check as well as this package's door.
//
// Best effort by design: the door does not depend on it. It is also one-way —
// Registry.Upsert latches the revoked bit — so this can only ever add.
func applyOperatorRevocations(reg *multiinstance.Registry) {
	if reg == nil {
		return
	}
	pol := operatorPeerPolicy()
	if len(pol.deniedIDs) == 0 && len(pol.deniedKeys) == 0 {
		return
	}
	insts, err := reg.List()
	if err != nil {
		log.Printf("[crdtsync] could not read the roster to persist operator revocations: %v", err)
		return
	}
	for _, in := range insts {
		if in.Revoked {
			continue
		}
		denied := pol.deniesInstance(in.ULID)
		if !denied {
			if key, kerr := crdtsync.DecodePeerKey(in.Ed25519PublicKey); kerr == nil && pol.deniesKey(key) {
				denied = true
			}
		}
		if !denied {
			continue
		}
		in.Revoked = true
		if err := reg.Upsert(in); err != nil {
			log.Printf("[crdtsync] could not persist the revocation of %s (it is still refused at the door): %v", in.ULID, err)
			continue
		}
		log.Printf("[crdtsync] instance %s marked REVOKED in the roster from %s — this is permanent", in.ULID, envFabricRevokedPeers)
	}
}

// InstanceRevoked reports whether the roster (or the operator's deny list) has
// evicted instanceID. Unknown instances are NOT revoked: this answer decides
// whether to stop dialling a peer, and a box we have never heard of is a box we
// were never told to evict.
func (r fabricPeerRoster) InstanceRevoked(instanceID string) bool {
	if instanceID == "" || instanceID == r.selfULID {
		return false
	}
	if operatorPeerPolicy().deniesInstance(instanceID) {
		return true
	}
	if r.reg == nil {
		return false
	}
	in, ok := r.reg.Get(instanceID)
	if !ok {
		return false
	}
	if in.Revoked {
		return true
	}
	key, err := crdtsync.DecodePeerKey(in.Ed25519PublicKey)
	return err == nil && operatorPeerPolicy().deniesKey(key)
}

// peerDirectory is what the sync loop needs from the roster: name a peer by its
// instance ULID, and say whether it has been evicted. It is an interface so a
// roster that cannot answer (the static test roster) degrades to "cannot name
// anyone, evicts no one" instead of the wiring type-asserting on the hot path.
type peerDirectory interface {
	PeerKeyForInstance(instanceID string) string
	InstanceRevoked(instanceID string) bool
}

// ── LAN request signing ──────────────────────────────────────────────────────

// peerKeyIndex maps a peer's base URL host to its identity key, refreshed by the
// discovery adapter on every round.
//
// It exists because the signing decision is made in a Doer, which sees a URL and
// not a SyncPeer. Keying on host rather than the whole base URL is deliberate:
// the syncer appends different paths to the same base.
type peerKeyIndex struct {
	mu     sync.RWMutex
	byHost map[string]string
}

func newPeerKeyIndex() *peerKeyIndex { return &peerKeyIndex{byHost: map[string]string{}} }

func (i *peerKeyIndex) remember(baseURL, key string) {
	host := hostOfURL(baseURL)
	if host == "" || key == "" {
		return
	}
	i.mu.Lock()
	i.byHost[host] = key
	i.mu.Unlock()
}

func (i *peerKeyIndex) keyFor(host string) string {
	if host == "" {
		return ""
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.byHost[host]
}

func hostOfURL(raw string) string {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return ""
	}
	return u.Host
}

// lanSigningClient wraps the LAN HTTP client so a LAN request carries a peer
// signature in ADDITION to the shared secret, whenever this box can name the
// peer it is dialling.
//
// Why it is needed: crdtsync/syncer.go signs WAN requests only — a LAN peer is
// dialled with X-Fabric-Auth and nothing else. Once any box in a fleet learns a
// peer identity it stops accepting the bare secret (see the authorizer above),
// so without this the fix would evict the compromised box and every healthy one
// with it.
//
// Why it is additive rather than a replacement: a peer still inside its
// bootstrap window accepts only the secret. Sending both means one fleet can
// finish an upgrade one box at a time.
//
// It does NOT invent trust. If the peer cannot be named, nothing is signed and
// the request goes out exactly as it did before — and a peer that has closed its
// bootstrap window will refuse it, which is the correct outcome: an unnameable
// peer is an unevictable one.
func lanSigningClient(inner crdtsync.Doer, id *crdtsync.PeerIdentity, keys *peerKeyIndex) crdtsync.Doer {
	if inner == nil || id == nil || keys == nil {
		return inner
	}
	return &lanPeerSigner{inner: inner, id: id, keys: keys}
}

type lanPeerSigner struct {
	inner crdtsync.Doer
	id    *crdtsync.PeerIdentity
	keys  *peerKeyIndex
}

func (s *lanPeerSigner) Do(req *http.Request) (*http.Response, error) {
	// Never touch a request the syncer already signed (the WAN path), and never
	// sign a request whose body cannot be reproduced exactly — the body hash is
	// inside the signature, so a guess would produce a signature the peer
	// rejects, which is worse than not signing at all.
	if req == nil || req.Header.Get(crdtsync.PeerAuthHeader) != "" {
		return s.inner.Do(req)
	}
	audience := s.keys.keyFor(req.URL.Host)
	if audience == "" {
		return s.inner.Do(req)
	}
	body, err := requestBodyCopy(req)
	if err != nil {
		return s.inner.Do(req)
	}
	header, _, err := s.id.SignRequest(req.Method, req.URL.Path, body, audience)
	if err != nil {
		log.Printf("[crdtsync] could not sign a LAN request to %s: %v", req.URL.Host, err)
		return s.inner.Do(req)
	}
	req.Header.Set(crdtsync.PeerAuthHeader, header)
	return s.inner.Do(req)
}

// requestBodyCopy returns the request's body without consuming it. It uses
// GetBody, which net/http populates for the *bytes.Reader bodies the syncer
// builds; anything else is refused rather than drained, because draining a body
// the transport still has to send is how a signing wrapper turns a working
// request into an empty one.
func requestBodyCopy(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	if req.GetBody == nil {
		return nil, fmt.Errorf("request body cannot be re-read")
	}
	rc, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, crdtsync.MaxBodyBytes+1))
}

// keyMatches compares a rostered key string against raw key bytes. The roster
// stores base64-STANDARD while the rendezvous layer uses base64url-raw;
// crdtsync.DecodePeerKey accepts both and the comparison is on the decoded
// bytes, so one key in two encodings is one identity, not two.
func keyMatches(rosterKey string, pub ed25519.PublicKey) bool {
	k, err := crdtsync.DecodePeerKey(rosterKey)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(k, pub) == 1
}

// startCRDTSync wires the general CRDT sync engine onto the LAN fabric.
//
// It is deliberately one function so the whole path is readable in one place:
// open the replica with the approved domains, mount the exchange endpoints on
// the LAN-only mux behind the shared secret fabric uses AND (when a signing key
// and a roster are available) per-peer Ed25519 authentication for WAN peers,
// bridge each approved SQL table, and start both loops.
//
// Everything fails CLOSED. No secret, no discoverer, or a bridge that cannot
// open its table means that part of sync does not run — it never means running
// it unauthenticated or replicating something that was not approved.
//
// Returns the store so the caller can close it; a nil store means sync is off.
func startCRDTSync(
	ctx context.Context,
	lanMux *http.ServeMux,
	dbDir string,
	instanceID string,
	fabricSecret string,
	disc fabric.Discoverer,
	wanClient *fabric.WANClient,
	selfBaseURLs []string,
	signer ed25519.PrivateKey,
	roster crdtsync.PeerRoster,
	// onApplied is called with a replicated table's name after rows are written
	// into the live database on a peer's behalf, so a service holding its own
	// in-memory view of that table can reload it. May be nil.
	onApplied func(table string),
) (*crdtsync.Store, error) {
	if fabricSecret == "" {
		return nil, fmt.Errorf("no fabric secret: an unauthenticated CRDT exchange endpoint is never acceptable")
	}
	if disc == nil {
		return nil, fmt.Errorf("no peer discoverer")
	}
	domains := crdtsync.SyncableDomains()
	if len(domains) == 0 {
		return nil, fmt.Errorf("no domain is approved for replication")
	}

	store, err := crdtsync.Open(filepath.Join(dbDir, "crdtsync.db"), instanceID, domains)
	if err != nil {
		return nil, fmt.Errorf("open replica: %w", err)
	}

	// The production roster arrives through an interface but is a VALUE, so this
	// box's own ULID is filled in here rather than plumbed through main.go's
	// call site (which is pinned by TestCRDTSyncCallSitePassesRealIdentityAndRoster).
	// AttributesPeers has to exclude self, or every box would claim it can name
	// a peer the moment appsync publishes its OWN key.
	if fr, ok := roster.(fabricPeerRoster); ok {
		fr.selfULID = instanceID
		roster = fr
		// One-way, best effort, and the door does not depend on it succeeding.
		applyOperatorRevocations(fr.reg)
	}
	// The same roster, asked the two questions the sync LOOP needs answered.
	// A roster that cannot answer them leaves dir nil, and every use below is
	// nil-guarded: LAN requests then go out unsigned, exactly as before.
	dir, _ := roster.(peerDirectory)
	lanKeys := newPeerKeyIndex()

	// ── peer identity ────────────────────────────────────────────────────────
	//
	// Both halves must be present or WAN sync does not run. Each is separately
	// fail-closed, and neither degrades to the shared secret:
	//
	//	signer — this box's Ed25519 fabric key, so it can sign requests peers
	//	         will accept and sign the responses it serves.
	//	roster — who is allowed. Without it a signature would prove who a peer
	//	         is and nothing about whether it may write here.
	//
	// With neither, this is exactly the LAN-only wiring that shipped before:
	// the secret authorizer, no WAN identity, WAN peers skipped.
	var (
		identity *crdtsync.PeerIdentity
		verifier *crdtsync.PeerVerifier
	)
	if len(signer) == ed25519.PrivateKeySize && roster != nil {
		id, ierr := crdtsync.NewPeerIdentity(signer)
		if ierr != nil {
			store.Close()
			return nil, fmt.Errorf("peer identity: %w", ierr)
		}
		v, verr := crdtsync.NewPeerVerifier(id.PublicKey(), roster)
		if verr != nil {
			store.Close()
			return nil, fmt.Errorf("peer verifier: %w", verr)
		}
		identity, verifier = id, v
		log.Printf("[crdtsync] WAN peer identity active (key=%s; peers authorised from the instance roster, deny-by-default)", id.ID())
	} else {
		log.Printf("[crdtsync] WAN peer identity NOT configured — WAN peers will be skipped, never dialled with the shared LAN secret. " +
			"NOTE: with no signing identity the exchange is authenticated by the shared secret alone, which names the fleet and not a member, " +
			"so a compromised box CANNOT be evicted from sync while this box is in that state")
	}

	// The exchange endpoints ride the LAN-only mux, behind a door that a REVOKED
	// instance cannot open.
	//
	// This used to be AnyOf(secret, signature), argued as "not a weakening,
	// because the signature scheme is strictly stronger than the secret". That
	// argument is about GRANTING access, and it is true. It is silent on
	// REVOKING it, which is the case that matters: AnyOf returns on the first
	// arm that passes, VULOS_FABRIC_SECRET is one bearer credential identical on
	// every box in the fleet, so a revoked instance passed on the secret arm and
	// the roster check was never reached. Marking a compromised box revoked
	// changed nothing about what it could read or write. The reasoning was
	// written down, looked careful, and did not cover the case it needed to.
	//
	// What replaces it (crdtsync.SignedOrBootstrapSecretAuthorizer):
	//
	//	a rostered peer's signature — always accepted; the ONLY thing accepted
	//	  once this box can name a peer, because it is the only credential that
	//	  says WHICH box is calling and therefore the only one that can be taken
	//	  away from one box without taking it away from all of them.
	//	the shared secret — accepted only while this box has learned no peer
	//	  identity at all. That window exists because deleting the secret arm
	//	  outright is an outage, not a fix: a LAN peer is dialled with the secret
	//	  and nothing else (crdtsync/syncer.go's post), and a box that has never
	//	  been told a peer's public key cannot authenticate one, so it would
	//	  refuse every legitimate peer while evicting nobody. The window closes
	//	  permanently at the first enrolment, and a REVOKED peer still counts as
	//	  an enrolment — otherwise evicting your only peer would reopen the exact
	//	  credential it still holds.
	//
	// Scope, stated plainly so "eviction" is not read as more than it is: this
	// stops a revoked instance WRITING to this box and being served state by it.
	// It does not un-read what that instance already pulled, and there is no
	// group re-key here, so it does not make already-shared material unreadable
	// to it. Eviction is future-tense.
	// FABRIC-SECRET-ROT-01. The secret arm is a rotation RING, not a single
	// string: during an overlap it admits the current secret and the overlap
	// value, and it stops admitting the overlap value the instant the window
	// closes — re-evaluated per request, so the close needs no restart.
	//
	// This is a second ring instance, parsed from the same environment as the one
	// internal/fabric builds for its own handlers. They agree on policy because
	// the input and the clock are the same; what does not merge is the ADMISSION
	// COUNTERS, so /api/fabric/status reports the fabric door's traffic and not
	// this one's. That is the right way round for an operator watching a roll:
	// the fabric changeset endpoints are gated on the secret ALONE, whereas this
	// door only consults the secret during the bootstrap window (see below), so
	// fabric's counters are the ones that answer "is anybody still on the old
	// value".
	secretRing := fabric.LoadSecretRingFromEnv(fabricSecret)
	secretRing.LogSummary("[crdtsync]")
	secretAuthz := crdtsync.RingSecretAuthorizer(secretRing)
	authz := secretAuthz
	if verifier != nil {
		authz = crdtsync.SignedOrBootstrapSecretAuthorizer(
			crdtsync.PeerKeyAuthorizer(verifier), secretAuthz, roster)
	}
	store.RegisterHandlers(lanMux, authz, crdtsync.WithResponseSigning(identity))

	// The discoverer is adapted rather than imported into the engine: a WAN
	// rendezvous discoverer satisfies the same fabric.Discoverer interface and
	// arrives here with no change to crdtsync at all.
	peers := crdtsync.PeerSourceFunc(func(ctx context.Context) ([]crdtsync.SyncPeer, error) {
		fp, err := disc.Peers(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]crdtsync.SyncPeer, 0, len(fp))
		for _, p := range fp {
			// Eviction has a second direction. Refusing a revoked peer's
			// requests stops it WRITING to us; it does nothing about the ops we
			// PULL from it and merge. So a peer the roster has revoked is
			// dropped from the dial list here, before any request is built.
			if dir != nil && dir.InstanceRevoked(p.InstanceID) {
				continue
			}
			key := p.PublicKey
			if key == "" && dir != nil {
				// A LAN peer arrives with no key (mDNS resolves a name, not an
				// identity), so it is looked up by ULID. Without this the peer
				// cannot be named, this box cannot sign to it, and an upgraded
				// peer would refuse us.
				key = dir.PeerKeyForInstance(p.InstanceID)
			}
			if key != "" {
				lanKeys.remember(p.BaseURL, key)
			}
			out = append(out, crdtsync.SyncPeer{
				InstanceID: p.InstanceID,
				BaseURL:    p.BaseURL,
				WAN:        p.WAN,
				// Carried through from rendezvous: the key we asked the relay
				// to resolve. Empty for mDNS peers, which is why a LAN peer
				// never accidentally takes the WAN path.
				PublicKey: p.PublicKey,
			})
		}
		return out, nil
	})

	syncerCfg := crdtsync.SyncerConfig{
		Store:   store,
		Peers:   peers,
		Domains: domains,
		Secret:  fabricSecret,
		// The LAN client SIGNS as well as presenting the secret, whenever it can
		// name the peer it is dialling. See lanSigningClient: without it, the
		// first box to learn a peer identity would start refusing every LAN peer
		// that only knows how to send the secret.
		HTTPClient:   lanSigningClient(fabric.NewLANClient(10*time.Second), identity, lanKeys),
		SelfBaseURLs: selfBaseURLs,
		Identity:     identity,
	}
	// A nil *fabric.WANClient must not become a non-nil Doer interface holding a
	// nil pointer, or the fail-closed WAN check would be bypassed by a value
	// that is "not nil" but unusable.
	if wanClient != nil {
		syncerCfg.WANHTTPClient = wanClient
	}

	syncer, err := crdtsync.NewSyncer(syncerCfg)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("syncer: %w", err)
	}

	// The loop's own health, on the same mux and behind the same authorizer as
	// the engine endpoints. Without this an operator can see WHAT this box
	// holds and never whether it is talking to anyone.
	crdtsync.RegisterSyncStatusHandler(lanMux, authz, syncer)

	// A local write nudges the network round rather than waiting for the tick.
	store.SetOnLocalChange(syncer.Nudge)

	bridged := 0
	for _, rt := range sqlcrdt.ReplicatedTables() {
		livePath := rt.LiveDBPath(dbDir)
		br, berr := sqlcrdt.New(sqlcrdt.Config{
			LivePath: livePath,
			Tables:   []sqlcrdt.TableSpec{rt.Spec},
			CRDT:     store,
		})
		if berr != nil {
			// One table failing to bridge (not created yet, schema drift) must
			// not take the whole engine down with it.
			log.Printf("[crdtsync] %s not bridged: %v", rt.Domain, berr)
			continue
		}
		if onApplied != nil {
			table := rt.Spec.Name
			br.SetOnApplied(func(int) { onApplied(table) })
		}
		// Run and Close in ONE goroutine, in that order. They used to be two
		// goroutines both waiting on ctx.Done(), which left their order to the
		// scheduler: when Close won, it freed the session out from under a
		// loop that was still ticking and the next cycle panicked the process
		// during shutdown. Run returns as soon as ctx is done, so sequencing
		// them costs nothing and makes teardown deterministic.
		go func(b *sqlcrdt.Bridge) {
			b.Run(ctx, sqlcrdt.DefaultCycleInterval, syncer.Nudge)
			_ = b.Close()
		}(br)
		bridged++
		log.Printf("[crdtsync] bridging %s (%s)", rt.Domain, livePath)
	}
	if bridged == 0 {
		store.Close()
		return nil, fmt.Errorf("no table could be bridged")
	}

	go syncer.Run(ctx)
	wanState := "WAN peers skipped (no peer identity)"
	if identity != nil {
		wanState = "WAN peers require a rostered peer signature"
	}
	log.Printf("[crdtsync] active (instance=%s, domains=%v, /api/crdt/{pull,push,status} on the LAN listener; %s)", instanceID, domains, wanState)
	return store, nil
}
