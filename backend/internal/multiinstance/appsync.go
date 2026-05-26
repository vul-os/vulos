// MINST-04: App registry sync — pure-Go CRDT changeset replication.
//
// AppSync replicates the app_registry table across instances using a
// pure-Go CRDT changeset layer (no CGO / cr-sqlite extension required).
//
// Conflict resolution strategy
//
//   - app_version field:  Last-Write-Wins (LWW) — the row with the higher
//     updated_at timestamp wins.  On a tie, the deterministic tie-break is the
//     lexicographically larger writer node id (installed_by).  Comparing the
//     writer node — not the row's instance_ulid, which is identical on both
//     sides of a merge for the same (instance_ulid, app_id) key — is what makes
//     two peers that observe the same pair of concurrent writes converge to the
//     SAME winner regardless of which side applies first.  (Audit P1-3.)
//   - installed flag:     OR-set semantics — install wins over uninstall
//     when the timestamps are equal.  A true uninstall (status=0) only
//     propagates when its updated_at is strictly newer than the local row.
//     Exception: when more than 2 instances exist in the registry, an
//     uninstall is only accepted when distinct-origin quorum is met.
//
// Quorum hardening (audit P1-3 + CRDT-QUORUM-01)
//
//	The uninstall quorum is met by N DISTINCT, VERIFIED, ROSTERED ORIGINATING
//	INSTANCES having independently reported the uninstall — never by any
//	self-reported AppChangeset.PeerCount, and never by a self-asserted origin
//	string.  This is the REAL CRDT-QUORUM-01 fix: because VULOS_FABRIC_SECRET is a
//	SINGLE shared bearer secret identical on every box, a self-asserted OriginULID
//	cannot distinguish peers — one secret-holder could otherwise mint N fake
//	origins ("fake-1".."fake-N") and fabricate a quorum.  So each box has its OWN
//	Ed25519 keypair (SetIdentity / LoadOrCreateInstanceKey); its PUBLIC key is
//	published into the registry ROSTER (instances.ed25519_public_key).
//	EmitChangeset SIGNS the changeset; ApplyChangeset records an observation in
//	app_uninstall_observations (keyed by instance_ulid, app_id, observer_ulid)
//	ONLY when verifyChangesetSignature succeeds — i.e. the origin is a KNOWN
//	rostered peer AND the signature verifies against THAT peer's rostered key (a
//	box's own origin is trusted implicitly).  Consequences:
//	  (a) A single peer that inflates PeerCount to 99, OR forges many distinct
//	      OriginULIDs, OR sends an unknown/unsigned/badly-signed origin, still
//	      contributes AT MOST ONE verified observation (its own signed identity)
//	      and cannot force a removal.
//	  (b) A peer cannot unilaterally BLOCK a legitimate removal: it can only
//	      withhold its own observation; other instances' observations still
//	      accumulate toward quorum.
//	  (c) When the local registry shows ≤ 2 instances quorum is not required at
//	      all (a 2-node system cannot form a majority): a strictly-newer
//	      uninstall is accepted on LWW alone.
//	The observation set is a monotonic OR-set persisted in SQLite so it survives
//	merges/restarts and converges deterministically (union semantics).  Each
//	observation is tagged with the app's install GENERATION (app_install_generation);
//	a (re)install bumps the generation and quorum counts ONLY current-generation
//	observations, so a re-install GCs stale rows and a later uninstall must gather
//	FRESH observations (observation-set GC).
//
// Wire format
//
//	Changesets are JSON-encoded AppChangeset values.  They are transport-
//	agnostic — the caller serialises and delivers them via whatever channel
//	it chooses (direct HTTP, relay deposit, etc.).  ApplyChangeset handles
//	the merge atomically under a single SQLite write transaction.
//
// HTTP endpoints (wired via RegisterAppSyncHandlers — never from main.go)
//
//	GET /api/instances/:ulid/apps   — per-instance app inventory
//
// Usage
//
//	as, err := multiinstance.OpenAppSync(reg)
//	// emit on local install:
//	cs, err := as.EmitChangeset("my-ulid", []AppRegistryEntry{...})
//	// apply received changeset from a peer:
//	err = as.ApplyChangeset(cs)
//	// HTTP handler wiring:
//	multiinstance.RegisterAppSyncHandlers(mux, as)
package multiinstance

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── Domain types ──────────────────────────────────────────────────────────────

// AppRegistryEntry is one row in the app_registry table.
type AppRegistryEntry struct {
	// InstanceULID identifies which instance hosts this app.
	InstanceULID string `json:"instance_ulid"`
	// AppID is the canonical app slug (e.g. "browser", "notes").
	AppID string `json:"app_id"`
	// AppVersion is the installed version string (e.g. "1.2.3").
	AppVersion string `json:"app_version"`
	// Installed is the OR-set flag: true = installed, false = uninstalled.
	Installed bool `json:"installed"`
	// InstalledBy is the node ID that wrote this row.
	InstalledBy string `json:"installed_by"`
	// UpdatedAt is the wall-clock timestamp used for LWW ordering.
	UpdatedAt time.Time `json:"updated_at"`
}

// AppChangeset is the wire format exchanged between instances.
// Each changeset carries all changed rows from one instance since the
// last sync.  Receivers call ApplyChangeset to merge it into local state.
type AppChangeset struct {
	// OriginULID is the instance that produced this changeset.
	OriginULID string `json:"origin_ulid"`
	// Entries are the changed rows.
	Entries []AppRegistryEntry `json:"entries"`
	// PeerCount is the number of instances the originator knew about when it
	// emitted this changeset.  TELEMETRY ONLY — it is NOT trusted for the
	// uninstall-quorum decision (see CRDT-QUORUM-01). Quorum is decided from the
	// count of DISTINCT VERIFIED rostered OriginULIDs that have reported an
	// uninstall, gated by the locally-observed peer count. A peer cannot force a
	// removal by inflating this field.
	PeerCount int `json:"peer_count"`

	// ── Signed-origin authentication (CRDT-QUORUM-01 real fix) ───────────────
	//
	// The fabric secret is a SINGLE shared bearer secret identical on every box,
	// so it cannot distinguish one authenticated peer from another. A peer could
	// therefore submit N changesets each claiming a different fake OriginULID and
	// fabricate a quorum. To make an uninstall observation UNFORGEABLE, the
	// originating instance signs the changeset with its OWN per-instance Ed25519
	// private key; the receiver verifies the signature against the origin's
	// public key as published in the registry ROSTER. A single secret-holder can
	// only validly sign as ITSELF, so it contributes exactly ONE distinct
	// verified origin no matter how many fake OriginULIDs it tries.

	// SignerPubKey is the base64-standard Ed25519 public key of OriginULID. It is
	// advisory only: the receiver verifies against the ROSTERED key for
	// OriginULID, not this field, so a forged SignerPubKey cannot help an
	// attacker (it must still match the rostered key AND verify the signature).
	SignerPubKey string `json:"signer_pubkey,omitempty"`

	// Signature is the base64-standard Ed25519 signature over the canonical
	// observation message (changesetSigningMessage) covering OriginULID and every
	// uninstall entry. Install-only changesets may omit it (installs are not
	// quorum-gated); an uninstall whose signature is absent / unverifiable simply
	// does not COUNT toward quorum (the merge still runs, the row stays installed
	// until verified quorum is reached).
	Signature string `json:"signature,omitempty"`
}

// ── AppSync ───────────────────────────────────────────────────────────────────

// AppSync manages the app_registry table and provides CRDT merge logic.
// It is safe for concurrent use.
type AppSync struct {
	reg *Registry // borrows the same *sql.DB (via the exported DB() accessor)
	db  *sql.DB

	// onLocalChange, when set, is invoked (without holding any lock) after a
	// successful LOCAL app-registry mutation (LocalInstall / LocalUninstall).
	// The fabric P2P sync service registers its Nudge here so a local install/
	// uninstall triggers an immediate out-of-band sync push rather than waiting
	// the background tick. Guarded by hookMu so it can be set after construction.
	hookMu        sync.RWMutex
	onLocalChange func()

	// ── Per-instance signing identity (CRDT-QUORUM-01) ───────────────────────
	//
	// selfULID + signPriv are this box's stable identity and Ed25519 private key.
	// EmitChangeset signs every emitted changeset with signPriv; a peer verifies
	// the signature against the ROSTERED public key for selfULID. The public key
	// (signPubB64) is published into the registry roster so peers learn it. When
	// no identity is set (signPriv == nil) emitted changesets are unsigned and an
	// uninstall they carry will not count toward a remote peer's quorum — the
	// system fails CLOSED for quorum but install/LWW replication is unaffected.
	idMu       sync.RWMutex
	selfULID   string
	signPriv   ed25519.PrivateKey
	signPubB64 string
}

// OpenAppSync creates an AppSync that shares the Registry's database.
// The app_registry migration (0004_app_registry.sql) must already be applied
// by the registry's migrate() function (it is embedded in the migrations dir).
func OpenAppSync(reg *Registry) (*AppSync, error) {
	db := reg.DB()
	if db == nil {
		return nil, fmt.Errorf("multiinstance/appsync: registry DB is nil")
	}
	return &AppSync{reg: reg, db: db}, nil
}

// ── Per-instance signing identity (CRDT-QUORUM-01) ─────────────────────────────

// SetIdentity installs this box's stable ULID and Ed25519 private key so that
// changesets it emits are SIGNED and its public key can be advertised in the
// roster. It also self-upserts the box's own (ULID → public key) into the local
// registry so the box can verify changesets it loops back to itself and so a
// peer pulling the roster learns this box's key. Passing a nil key clears the
// identity (emitted changesets become unsigned). Safe to call after construction.
//
// The private key never leaves this process; only the base64 public key is
// published. selfULID's observations are ALWAYS trusted locally (a box trusts
// its own LocalUninstall) without a roster lookup.
func (as *AppSync) SetIdentity(selfULID string, priv ed25519.PrivateKey) error {
	if selfULID == "" {
		return fmt.Errorf("appsync: SetIdentity: selfULID must not be empty")
	}
	var pubB64 string
	if priv != nil {
		if len(priv) != ed25519.PrivateKeySize {
			return fmt.Errorf("appsync: SetIdentity: private key length %d, want %d", len(priv), ed25519.PrivateKeySize)
		}
		pub, ok := priv.Public().(ed25519.PublicKey)
		if !ok {
			return fmt.Errorf("appsync: SetIdentity: private key has no Ed25519 public key")
		}
		pubB64 = base64.StdEncoding.EncodeToString(pub)
	}

	as.idMu.Lock()
	as.selfULID = selfULID
	as.signPriv = priv
	as.signPubB64 = pubB64
	as.idMu.Unlock()

	// Publish our public key into the roster so peers verifying OUR observations
	// can resolve it. Upsert is a merge (existing fields preserved by the
	// registry's coalescing of empty last_seen_at), so this is safe to repeat.
	if pubB64 != "" && as.reg != nil {
		if existing, ok := as.reg.Get(selfULID); ok {
			existing.Ed25519PublicKey = pubB64
			if err := as.reg.Upsert(existing); err != nil {
				return fmt.Errorf("appsync: SetIdentity: publish self pubkey: %w", err)
			}
		} else {
			if err := as.reg.Upsert(Instance{
				ULID:             selfULID,
				Kind:             KindDevice,
				Role:             RoleOwner,
				Status:           StatusOnline,
				Ed25519PublicKey: pubB64,
			}); err != nil {
				return fmt.Errorf("appsync: SetIdentity: register self: %w", err)
			}
		}
	}
	return nil
}

// GenerateAndSetIdentity creates a fresh Ed25519 keypair for selfULID, installs
// it via SetIdentity, and returns the private key so the caller can PERSIST it
// (e.g. encrypted at rest). A box must persist and reuse the SAME key across
// restarts, otherwise peers would have to re-learn its public key (and any
// in-flight signed observations made under the old key would no longer verify).
// Prefer LoadOrCreateInstanceKey on disk for production; this helper is for the
// in-memory / test path.
func (as *AppSync) GenerateAndSetIdentity(selfULID string) (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("appsync: GenerateAndSetIdentity: %w", err)
	}
	if err := as.SetIdentity(selfULID, priv); err != nil {
		return nil, err
	}
	return priv, nil
}

// identity returns a snapshot of the signing identity under the lock.
func (as *AppSync) identity() (selfULID string, priv ed25519.PrivateKey, pubB64 string) {
	as.idMu.RLock()
	defer as.idMu.RUnlock()
	return as.selfULID, as.signPriv, as.signPubB64
}

// LoadOrCreateInstanceKey loads this box's persistent fabric signing key from
// path, generating and persisting a fresh Ed25519 key on first use. The key is
// the box's stable identity for signed uninstall observations (CRDT-QUORUM-01)
// and MUST persist across restarts: rotating it would force peers to re-learn the
// public key and invalidate any in-flight signed observations.
//
// The key file holds the base64-standard seed (ed25519.PrivateKey.Seed()) and is
// written 0600. This is the same secrecy class as the box's other private keys.
// SECURITY NOTE: this stores the key UNENCRYPTED at rest; for an at-rest-encrypted
// variant, wrap with the OS keyring like internal/identity does. The fabric is
// LAN-only and the key is access-controlled by file perms + the data dir.
func LoadOrCreateInstanceKey(path string) (ed25519.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		seed, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if derr == nil && len(seed) == ed25519.SeedSize {
			return ed25519.NewKeyFromSeed(seed), nil
		}
		// Corrupt/empty key file — fall through and regenerate (logged by caller).
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("appsync: LoadOrCreateInstanceKey: generate: %w", err)
	}
	seedB64 := base64.StdEncoding.EncodeToString(priv.Seed())
	if err := os.WriteFile(path, []byte(seedB64), 0o600); err != nil {
		return nil, fmt.Errorf("appsync: LoadOrCreateInstanceKey: persist: %w", err)
	}
	return priv, nil
}

// SetOnLocalChange registers a callback fired (without holding any AppSync lock)
// after a successful LOCAL app-registry mutation — LocalInstall or
// LocalUninstall. The fabric P2P sync service passes its Nudge here so a local
// change converges immediately instead of waiting the background sync tick.
// Passing nil clears the hook. Safe to call at any time.
func (as *AppSync) SetOnLocalChange(fn func()) {
	as.hookMu.Lock()
	as.onLocalChange = fn
	as.hookMu.Unlock()
}

// fireLocalChange invokes the registered local-change hook, if any. Called after
// a local mutation commits. The hook (typically fabric Nudge) must be cheap and
// non-blocking; it is invoked synchronously but outside any lock.
func (as *AppSync) fireLocalChange() {
	as.hookMu.RLock()
	fn := as.onLocalChange
	as.hookMu.RUnlock()
	if fn != nil {
		fn()
	}
}

// ── Local mutations ────────────────────────────────────────────────────────────

// LocalInstall records that appID@version is installed on THIS instance and
// fires the local-change hook (fabric Nudge) so peers learn promptly.
//
// It writes a single app_registry row stamped with UpdatedAt = now and
// InstalledBy = instanceULID (this box is the writer node), which is exactly the
// LWW input ChangesetSince/EmitChangeset will later replicate. instanceULID must
// be this box's stable ULID.
func (as *AppSync) LocalInstall(instanceULID, appID, version string) error {
	return as.localMutate(instanceULID, appID, version, true)
}

// LocalUninstall records that appID is uninstalled on THIS instance (OR-set flag
// flipped to false, stamped now) and fires the local-change hook. The actual
// propagation still obeys the uninstall-quorum rules on the receiving peers.
func (as *AppSync) LocalUninstall(instanceULID, appID string) error {
	return as.localMutate(instanceULID, appID, "", false)
}

// localMutate is the shared write path for LocalInstall/LocalUninstall. It
// upserts the row in a single transaction, then — only on commit success —
// fires the local-change hook so the fabric sync loop pushes the change without
// waiting the tick.
func (as *AppSync) localMutate(instanceULID, appID, version string, installed bool) error {
	if instanceULID == "" {
		return fmt.Errorf("appsync: localMutate: instanceULID must not be empty")
	}
	if appID == "" {
		return fmt.Errorf("appsync: localMutate: appID must not be empty")
	}
	tx, err := as.db.Begin()
	if err != nil {
		return fmt.Errorf("appsync: localMutate: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	entry := AppRegistryEntry{
		InstanceULID: instanceULID,
		AppID:        appID,
		AppVersion:   version,
		Installed:    installed,
		InstalledBy:  instanceULID,
		UpdatedAt:    time.Now().UTC(),
	}
	if err := as.upsertEntry(tx, entry); err != nil {
		return fmt.Errorf("appsync: localMutate: upsert: %w", err)
	}
	// A local (re)install bumps the install generation so any uninstall
	// observations gathered against an earlier generation are GC'd — a later
	// uninstall must gather FRESH observations (CRDT-QUORUM-01 GC). The local
	// uninstall path leaves the generation untouched (it is the event being
	// corroborated, not a new install).
	if installed {
		if err := as.bumpGeneration(tx, instanceULID, appID, entry.UpdatedAt); err != nil {
			return fmt.Errorf("appsync: localMutate: bump generation: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("appsync: localMutate: commit: %w", err)
	}
	committed = true
	// Local change is durable — nudge the fabric so it converges immediately.
	as.fireLocalChange()
	return nil
}

// ── Emit ─────────────────────────────────────────────────────────────────────

// EmitChangeset builds a changeset from the provided entries and returns it
// ready for delivery to peers.  The caller is responsible for transport.
//
// Typical usage: call after a local install/uninstall to produce the
// changeset that should be pushed to all known peers.
func (as *AppSync) EmitChangeset(originULID string, entries []AppRegistryEntry) (*AppChangeset, error) {
	if originULID == "" {
		return nil, fmt.Errorf("appsync: EmitChangeset: originULID must not be empty")
	}
	peers, err := as.reg.List()
	if err != nil {
		return nil, fmt.Errorf("appsync: EmitChangeset: list peers: %w", err)
	}
	cs := &AppChangeset{
		OriginULID: originULID,
		Entries:    entries,
		PeerCount:  len(peers),
	}
	// Sign the changeset with this box's per-instance key so its uninstall
	// observations are unforgeable. We sign only when the emitting origin IS our
	// own identity — a box must never sign as a different origin (that is exactly
	// the forge we are preventing). An unsigned uninstall simply will not count
	// toward a remote peer's quorum.
	selfULID, priv, pubB64 := as.identity()
	if priv != nil && originULID == selfULID {
		msg := changesetSigningMessage(originULID, entries)
		sig := ed25519.Sign(priv, msg)
		cs.SignerPubKey = pubB64
		cs.Signature = base64.StdEncoding.EncodeToString(sig)
	}
	return cs, nil
}

// SignChangeset signs an arbitrary changeset with this box's identity, provided
// cs.OriginULID matches our own selfULID. It is the explicit-signing entry point
// for callers that build a changeset directly rather than via EmitChangeset
// (e.g. tests, or a transport that constructs the changeset itself). A box
// REFUSES to sign a changeset whose origin is not its own identity.
func (as *AppSync) SignChangeset(cs *AppChangeset) error {
	if cs == nil {
		return fmt.Errorf("appsync: SignChangeset: nil changeset")
	}
	selfULID, priv, pubB64 := as.identity()
	if priv == nil {
		return fmt.Errorf("appsync: SignChangeset: no signing identity set")
	}
	if cs.OriginULID != selfULID {
		return fmt.Errorf("appsync: SignChangeset: refusing to sign changeset for origin %q with identity %q", cs.OriginULID, selfULID)
	}
	msg := changesetSigningMessage(cs.OriginULID, cs.Entries)
	cs.SignerPubKey = pubB64
	cs.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
	return nil
}

// changesetSigningMessage builds the canonical, deterministic byte string that a
// changeset's origin signs and a receiver verifies. It binds the OriginULID to
// the set of UNINSTALL entries it carries — install entries are not quorum-gated
// and are excluded so an install-only changeset can be left unsigned. The entry
// fields are joined with a NUL separator (which cannot appear in any of the
// string fields) and entries are sorted so the message is independent of slice
// order. The leading domain tag prevents cross-protocol signature reuse.
func changesetSigningMessage(originULID string, entries []AppRegistryEntry) []byte {
	const domain = "vulos:appsync:uninstall-observation:v1"
	type rec struct {
		instanceULID string
		appID        string
		updatedAt    string
	}
	recs := make([]rec, 0, len(entries))
	for _, e := range entries {
		if e.Installed {
			continue // only uninstall observations are signed/quorum-gated
		}
		ts := ""
		if !e.UpdatedAt.IsZero() {
			ts = e.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		recs = append(recs, rec{instanceULID: e.InstanceULID, appID: e.AppID, updatedAt: ts})
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].instanceULID != recs[j].instanceULID {
			return recs[i].instanceULID < recs[j].instanceULID
		}
		if recs[i].appID != recs[j].appID {
			return recs[i].appID < recs[j].appID
		}
		return recs[i].updatedAt < recs[j].updatedAt
	})
	var b strings.Builder
	b.WriteString(domain)
	b.WriteByte(0)
	b.WriteString(originULID)
	for _, r := range recs {
		b.WriteByte(0)
		b.WriteString(r.instanceULID)
		b.WriteByte(0)
		b.WriteString(r.appID)
		b.WriteByte(0)
		b.WriteString(r.updatedAt)
	}
	return []byte(b.String())
}

// verifyChangesetSignature reports whether cs carries a valid signature by a
// CURRENTLY-VALID rostered public key for cs.OriginULID. It returns true only
// when:
//   - the origin is a KNOWN instance in the local registry roster, AND
//   - that instance is NOT revoked (FABRIC-KEY-01: a revoked key never counts), AND
//   - that instance has a published Ed25519 public key, AND
//   - the signature decodes and verifies over the canonical message against
//     EITHER the current rostered key OR, within an active rotation overlap
//     window, the previous rostered key (FABRIC-KEY-01 grace period).
//
// The self-asserted cs.SignerPubKey is IGNORED for the decision — the rostered
// key is authoritative — so a forged pubkey cannot help. A box's OWN origin
// (selfULID) is trusted implicitly: it does not need a signature to count its
// own LocalUninstall, because that observation was produced inside this process.
func (as *AppSync) verifyChangesetSignature(cs *AppChangeset) bool {
	if cs == nil || cs.OriginULID == "" {
		return false
	}
	// Our own origin is implicitly trusted (local-origin observation).
	if selfULID, _, _ := as.identity(); selfULID != "" && cs.OriginULID == selfULID {
		return true
	}
	if cs.Signature == "" {
		return false
	}
	if as.reg == nil {
		return false
	}
	inst, ok := as.reg.Get(cs.OriginULID)
	if !ok {
		// Unknown / unrostered origin → cannot verify.
		return false
	}
	// FABRIC-KEY-01 revocation: a compromised peer key NEVER counts toward
	// quorum, regardless of which (current or previous) key signed. Fail closed.
	if inst.Revoked {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(cs.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg := changesetSigningMessage(cs.OriginULID, cs.Entries)

	// Accept the CURRENT key.
	if verifyWithB64Key(inst.Ed25519PublicKey, msg, sig) {
		return true
	}
	// FABRIC-KEY-01 rotation overlap: accept the PREVIOUS key while its grace
	// window is still open, so an observation signed just before a rotation (and
	// still in flight) keeps verifying until peers have re-learned the new key.
	if inst.PrevEd25519PublicKey != "" &&
		!inst.PrevKeyExpiresAt.IsZero() &&
		time.Now().UTC().Before(inst.PrevKeyExpiresAt) {
		if verifyWithB64Key(inst.PrevEd25519PublicKey, msg, sig) {
			return true
		}
	}
	return false
}

// verifyWithB64Key decodes a base64-standard Ed25519 public key and reports
// whether sig is a valid signature by it over msg. Returns false on any decode
// or length error (fail closed).
func verifyWithB64Key(pubB64 string, msg, sig []byte) bool {
	if pubB64 == "" {
		return false
	}
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}

// ── Apply / Merge ─────────────────────────────────────────────────────────────

// ApplyChangeset merges a received AppChangeset into the local app_registry
// using LWW (version / updated_at) and OR-set (installed flag) rules.
//
// All rows are upserted atomically inside a single SQLite transaction.
func (as *AppSync) ApplyChangeset(cs *AppChangeset) error {
	if cs == nil {
		return fmt.Errorf("appsync: ApplyChangeset: nil changeset")
	}
	if len(cs.Entries) == 0 {
		return nil
	}

	// Count locally-known instances for quorum evaluation. This is the only
	// peer count we trust — the self-reported cs.PeerCount is ignored for the
	// security decision (CRDT-QUORUM-01).
	peers, err := as.reg.List()
	if err != nil {
		return fmt.Errorf("appsync: ApplyChangeset: list peers: %w", err)
	}
	peerCount := len(peers)

	// The reporting instance for this changeset. Each uninstall contributes at
	// most ONE distinct observation, keyed by this origin — AND only when the
	// changeset is cryptographically VERIFIED to actually come from that rostered
	// origin (CRDT-QUORUM-01). A self-asserted OriginULID is no longer enough:
	// the shared fabric secret cannot distinguish peers, so a single secret-holder
	// could otherwise mint N fake origins. Verification binds the origin to a
	// rostered Ed25519 public key and a signature, so a secret-holder can only
	// ever validly sign as ITSELF → exactly one distinct verified origin.
	originULID := cs.OriginULID
	originVerified := as.verifyChangesetSignature(cs)
	if !originVerified && hasUninstall(cs.Entries) {
		log.Printf("[appsync] changeset from %q carries uninstall(s) but is unverified "+
			"(unknown/unsigned/bad-signature origin) — observations will NOT count toward quorum", originULID)
	}
	// Resolve the origin's rostered public key NOW, BEFORE opening the write
	// transaction. The registry shares this single-connection *sql.DB, so a
	// roster read while a tx is open would deadlock waiting for the only
	// connection (held by the tx). The pubkey is stored on the observation row
	// for audit/provenance only.
	originPubKey := ""
	if originVerified {
		originPubKey = as.rosteredPubKey(originULID)
	}

	tx, err := as.db.Begin()
	if err != nil {
		return fmt.Errorf("appsync: ApplyChangeset: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, e := range cs.Entries {
		if err = as.mergeEntry(tx, e, peerCount, originULID, originVerified, originPubKey); err != nil {
			return fmt.Errorf("appsync: ApplyChangeset: merge %s/%s: %w",
				e.InstanceULID, e.AppID, err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("appsync: ApplyChangeset: commit: %w", err)
	}
	log.Printf("[appsync] applied changeset from %s (%d entries)", cs.OriginULID, len(cs.Entries))
	return nil
}

// mergeEntry merges one AppRegistryEntry into the local table using the rules:
//
//  1. If no local row exists → insert unconditionally (but still subject to the
//     uninstall-quorum guard below: a first-seen uninstall that has not reached
//     distinct-origin quorum is recorded as installed=true so a peer cannot seed
//     a removal).
//  2. LWW on updated_at: remote row wins only when it is strictly newer.
//     Tie-break: lexicographically larger writer node id (installed_by) wins —
//     deterministic and symmetric across peers.
//  3. OR-set installed flag: if timestamps are equal, true wins over false.
//  4. Uninstall quorum: an uninstall applies only when the number of DISTINCT
//     VERIFIED rostered originating instances that have reported it meets the
//     threshold derived from the locally-observed peer count (see
//     uninstallQuorumMet). The self-reported PeerCount plays NO part
//     (CRDT-QUORUM-01); neither does an unverified self-asserted origin.
//
// originULID is the reporting instance (cs.OriginULID); originVerified is true
// only when the changeset's signature verified against originULID's ROSTERED
// public key (or originULID is our own selfULID). An uninstall it carries
// contributes exactly one observation toward quorum — and ONLY when verified.
func (as *AppSync) mergeEntry(tx *sql.Tx, remote AppRegistryEntry, localPeerCount int, originULID string, originVerified bool, originPubKey string) error {
	// An install (re-install) bumps the install GENERATION for this app, which
	// invalidates any uninstall observations gathered against an earlier
	// generation (observation-set GC). This is what stops a re-install + later
	// uninstall from reaching quorum off STALE rows.
	if remote.Installed {
		if err := as.bumpGeneration(tx, remote.InstanceULID, remote.AppID, remote.UpdatedAt); err != nil {
			return fmt.Errorf("bump install generation: %w", err)
		}
	}

	// Record the distinct-origin observation BEFORE deciding quorum so the
	// reporting instance counts toward its own threshold. Recording is a no-op
	// if this origin already reported this uninstall at the current generation
	// (idempotent OR-set union). CRDT-QUORUM-01: ONLY a VERIFIED rostered origin
	// is recorded — an unknown/unsigned/badly-signed origin contributes NOTHING,
	// so a single shared-secret holder cannot fabricate distinct origins.
	if !remote.Installed && originVerified && originULID != "" {
		gen, gerr := as.currentGeneration(tx, remote.InstanceULID, remote.AppID)
		if gerr != nil {
			return fmt.Errorf("read install generation: %w", gerr)
		}
		if err := as.recordUninstallObservation(tx, remote.InstanceULID, remote.AppID, originULID, remote.UpdatedAt, gen, originPubKey); err != nil {
			return fmt.Errorf("record uninstall observation: %w", err)
		}
	}

	row := tx.QueryRow(`
		SELECT app_version, installed, installed_by, updated_at
		FROM app_registry
		WHERE instance_ulid = ? AND app_id = ?
	`, remote.InstanceULID, remote.AppID)

	var (
		localVersion     string
		localInstalled   int
		localInstalledBy string
		localUpdatedRaw  string
	)

	err := row.Scan(&localVersion, &localInstalled, &localInstalledBy, &localUpdatedRaw)
	if err == sql.ErrNoRows {
		// No local row. Insert remote — but never let a peer seed an *uninstall*
		// row before distinct-origin quorum is met, otherwise (a) it would block a
		// later install via LWW and (b) it is exactly the unilateral-removal a
		// hostile peer would attempt.
		if !remote.Installed {
			met, qerr := as.uninstallQuorumMet(tx, remote.InstanceULID, remote.AppID, localPeerCount)
			if qerr != nil {
				return qerr
			}
			if !met {
				log.Printf("[appsync] distinct-origin quorum not met for first-seen uninstall of %s/%s — recording installed=true",
					remote.InstanceULID, remote.AppID)
				remote.Installed = true
			}
		}
		return as.upsertEntry(tx, remote)
	}
	if err != nil {
		return fmt.Errorf("scan local row: %w", err)
	}

	// Parse local updated_at.
	var localUpdatedAt time.Time
	if localUpdatedRaw != "" {
		localUpdatedAt, _ = time.Parse(time.RFC3339Nano, localUpdatedRaw)
	}

	// Determine winning row.
	switch {
	case remote.UpdatedAt.After(localUpdatedAt):
		// Remote is strictly newer → accept.
		// Distinct-origin quorum check for uninstalls:
		if !remote.Installed {
			met, qerr := as.uninstallQuorumMet(tx, remote.InstanceULID, remote.AppID, localPeerCount)
			if qerr != nil {
				return qerr
			}
			if !met {
				log.Printf("[appsync] distinct-origin quorum not met for uninstall of %s/%s — retaining installed=true",
					remote.InstanceULID, remote.AppID)
				// Keep installed=true from the local row; still update version if newer.
				remote.Installed = true
			}
		}
		return as.upsertEntry(tx, remote)

	case remote.UpdatedAt.Equal(localUpdatedAt):
		// Tie. An uninstall that has now reached distinct-origin quorum is the
		// corroborated decision and overrides the OR-set "install wins" default:
		// this is how a legitimate multi-origin uninstall converges even when each
		// originating instance stamps the same removal timestamp, and how a
		// previously-suppressed (downgraded) uninstall flips to removed once the
		// Nth distinct origin reports it. A single forged origin can never reach
		// quorum, so it cannot exploit this branch.
		if !remote.Installed {
			met, qerr := as.uninstallQuorumMet(tx, remote.InstanceULID, remote.AppID, localPeerCount)
			if qerr != nil {
				return qerr
			}
			if met {
				e := remote
				e.Installed = false
				// Deterministic writer for the removed row: the larger node id of
				// the two, so all peers converge to identical installed_by.
				if localInstalledBy > e.InstalledBy {
					e.InstalledBy = localInstalledBy
				}
				if localVersion != "" {
					e.AppVersion = localVersion
				}
				return as.upsertEntry(tx, e)
			}
		}
		// OR-set default: install wins over a non-quorum uninstall; deterministic
		// tie-break on the writer node id (installed_by), NOT the row's
		// instance_ulid (which is identical on both sides for this key).
		mergedInstalled := localInstalled == 1 || remote.Installed // OR-set
		if remote.InstalledBy > localInstalledBy {
			// Remote wins tie-break — use remote's data but OR the installed flag.
			e := remote
			e.Installed = mergedInstalled
			return as.upsertEntry(tx, e)
		}
		// Local wins tie-break — update installed flag only if OR changes it.
		if mergedInstalled && localInstalled == 0 {
			return as.upsertEntry(tx, AppRegistryEntry{
				InstanceULID: remote.InstanceULID,
				AppID:        remote.AppID,
				AppVersion:   localVersion,
				Installed:    true,
				InstalledBy:  localInstalledBy,
				UpdatedAt:    localUpdatedAt,
			})
		}
		// Local already wins, nothing to do.
		return nil

	default:
		// Remote is strictly older. Normally discarded — but a strictly-older
		// uninstall observation that pushes the DISTINCT-origin set over quorum
		// must still flip a currently-installed row to removed (the observation
		// set, not the timestamp, is the quorum trigger). Without this, ordering
		// the corroborating observations newest-first would silently lose the
		// removal. A single forged origin can never reach quorum here either.
		if !remote.Installed && localInstalled == 1 {
			met, qerr := as.uninstallQuorumMet(tx, remote.InstanceULID, remote.AppID, localPeerCount)
			if qerr != nil {
				return qerr
			}
			if met {
				return as.upsertEntry(tx, AppRegistryEntry{
					InstanceULID: remote.InstanceULID,
					AppID:        remote.AppID,
					AppVersion:   localVersion,
					Installed:    false,
					InstalledBy:  localInstalledBy,
					UpdatedAt:    localUpdatedAt,
				})
			}
		}
		return nil
	}
}

// recordUninstallObservation records that observerULID (a VERIFIED rostered
// origin) reported the uninstall of (instanceULID, appID) at install generation
// `epoch`. It is an idempotent OR-set union: the same observer reporting twice is
// a no-op (PRIMARY KEY conflict); observed_at advances to the newest and the
// epoch is refreshed to the CURRENT generation so a re-observation after a
// re-install (which bumped the generation) re-validates the row rather than
// leaving a stale epoch. observer_pubkey records the rostered key the observation
// was verified against (audit/provenance). This set is the sole authority for
// uninstall quorum — never AppChangeset.PeerCount (CRDT-QUORUM-01).
func (as *AppSync) recordUninstallObservation(tx *sql.Tx, instanceULID, appID, observerULID string, observedAt time.Time, epoch int64, observerPubKey string) error {
	if observerULID == "" {
		// Cannot attribute the observation to a distinct origin. Refuse to count
		// it (it contributes nothing toward quorum) rather than crediting a blank.
		return nil
	}
	ts := ""
	if !observedAt.IsZero() {
		ts = observedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.Exec(`
		INSERT INTO app_uninstall_observations
			(instance_ulid, app_id, observer_ulid, observed_at, epoch, observer_pubkey)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_ulid, app_id, observer_ulid) DO UPDATE SET
			observed_at     = MAX(observed_at, excluded.observed_at),
			epoch           = excluded.epoch,
			observer_pubkey = excluded.observer_pubkey
	`, instanceULID, appID, observerULID, ts, epoch, observerPubKey)
	return err
}

// distinctUninstallOrigins counts the DISTINCT VERIFIED originating instances
// that have reported an uninstall of (instanceULID, appID) AT THE CURRENT install
// generation. Observations carrying a stale epoch (made before a later
// re-install) are excluded — that is the observation-set GC: a re-install bumps
// the generation, so a subsequent uninstall must gather FRESH observations.
func (as *AppSync) distinctUninstallOrigins(tx *sql.Tx, instanceULID, appID string) (int, error) {
	gen, err := as.currentGeneration(tx, instanceULID, appID)
	if err != nil {
		return 0, err
	}
	var n int
	err = tx.QueryRow(`
		SELECT COUNT(*) FROM app_uninstall_observations
		WHERE instance_ulid = ? AND app_id = ? AND epoch = ?
	`, instanceULID, appID, gen).Scan(&n)
	return n, err
}

// currentGeneration returns the current install generation/epoch for
// (instanceULID, appID), or 0 when none is recorded (the genesis generation).
func (as *AppSync) currentGeneration(tx *sql.Tx, instanceULID, appID string) (int64, error) {
	var gen int64
	err := tx.QueryRow(`
		SELECT generation FROM app_install_generation
		WHERE instance_ulid = ? AND app_id = ?
	`, instanceULID, appID).Scan(&gen)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return gen, nil
}

// bumpGeneration advances the install generation for (instanceULID, appID) when
// an install with timestamp installedAt is applied that is strictly newer than
// the timestamp that last set the generation. Convergent across peers: the
// generation is monotonic and keyed on the LWW timestamp, so two peers applying
// the same installs in any order converge to the same generation. A re-install
// therefore deterministically supersedes the prior generation's uninstall
// observations (GC). The genesis install moves generation 0 → 1.
func (as *AppSync) bumpGeneration(tx *sql.Tx, instanceULID, appID string, installedAt time.Time) error {
	ts := ""
	if !installedAt.IsZero() {
		ts = installedAt.UTC().Format(time.RFC3339Nano)
	}
	var (
		curGen int64
		curAt  string
	)
	err := tx.QueryRow(`
		SELECT generation, generation_at FROM app_install_generation
		WHERE instance_ulid = ? AND app_id = ?
	`, instanceULID, appID).Scan(&curGen, &curAt)
	switch {
	case err == sql.ErrNoRows:
		// First install → generation 1.
		_, e := tx.Exec(`
			INSERT INTO app_install_generation (instance_ulid, app_id, generation, generation_at)
			VALUES (?, ?, 1, ?)
		`, instanceULID, appID, ts)
		return e
	case err != nil:
		return err
	}
	// Only a STRICTLY-NEWER install bumps the generation; a replay of the same or
	// older install must not (it would needlessly invalidate live observations and
	// break convergence). String comparison of RFC3339Nano UTC matches temporal
	// order.
	if ts > curAt {
		_, e := tx.Exec(`
			UPDATE app_install_generation
			SET generation = generation + 1, generation_at = ?
			WHERE instance_ulid = ? AND app_id = ?
		`, ts, instanceULID, appID)
		return e
	}
	return nil
}

// rosteredPubKey returns the base64 Ed25519 public key published for originULID
// in the registry roster, or "" if the origin is not rostered / has no key. It is
// our own pubkey for our own origin (we may not be in the roster as a row yet).
func (as *AppSync) rosteredPubKey(originULID string) string {
	if selfULID, _, pubB64 := as.identity(); selfULID != "" && originULID == selfULID {
		return pubB64
	}
	if as.reg == nil {
		return ""
	}
	if inst, ok := as.reg.Get(originULID); ok {
		return inst.Ed25519PublicKey
	}
	return ""
}

// hasUninstall reports whether any entry in the slice is an uninstall (the only
// quorum-gated, signature-relevant kind).
func hasUninstall(entries []AppRegistryEntry) bool {
	for _, e := range entries {
		if !e.Installed {
			return true
		}
	}
	return false
}

// uninstallQuorumThreshold returns the number of DISTINCT originating instances
// that must report an uninstall before it is accepted, derived ONLY from the
// locally-observed peer count.
//
//   - ≤ 2 local instances: 1 — a two-node system cannot form a majority, so a
//     single (strictly-newer) uninstall observation suffices (LWW alone).
//   - > 2 local instances: a strict majority of the roster, floored at 2, so a
//     lone origin can never satisfy it. (N=3→2, N=4→3, N=5→3, …)
//
// Because the threshold is computed from the LOCAL registry and the count is the
// number of DISTINCT origins, a single peer cannot reach quorum by inflating any
// self-reported value — it is always exactly one origin.
func uninstallQuorumThreshold(localPeerCount int) int {
	if localPeerCount <= 2 {
		return 1
	}
	majority := localPeerCount/2 + 1
	if majority < 2 {
		majority = 2
	}
	return majority
}

// uninstallQuorumMet reports whether enough DISTINCT origins have reported the
// uninstall of (instanceULID, appID) to meet the locally-derived threshold.
func (as *AppSync) uninstallQuorumMet(tx *sql.Tx, instanceULID, appID string, localPeerCount int) (bool, error) {
	n, err := as.distinctUninstallOrigins(tx, instanceULID, appID)
	if err != nil {
		return false, fmt.Errorf("count distinct uninstall origins: %w", err)
	}
	return n >= uninstallQuorumThreshold(localPeerCount), nil
}

// upsertEntry writes an AppRegistryEntry into the database (insert or replace).
func (as *AppSync) upsertEntry(tx *sql.Tx, e AppRegistryEntry) error {
	installed := 0
	if e.Installed {
		installed = 1
	}
	updatedAt := ""
	if !e.UpdatedAt.IsZero() {
		updatedAt = e.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}

	_, err := tx.Exec(`
		INSERT INTO app_registry
			(instance_ulid, app_id, app_version, installed, installed_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_ulid, app_id) DO UPDATE SET
			app_version  = excluded.app_version,
			installed    = excluded.installed,
			installed_by = excluded.installed_by,
			updated_at   = excluded.updated_at
	`, e.InstanceULID, e.AppID, e.AppVersion, installed, e.InstalledBy, updatedAt)
	return err
}

// PeerInstanceIDs returns the ULIDs of every instance in the registry roster,
// optionally excluding self. It is the roster source the fabric mDNS discoverer
// uses to build per-instance qualified query names (InstanceFabricName) so a
// >2-box LAN resolves every known peer individually rather than only the single
// responder the shared mDNS name yields. Returns nil on a read error (the
// discoverer then falls back to the shared name only).
func (as *AppSync) PeerInstanceIDs(excludeSelfULID string) []string {
	insts, err := as.reg.List()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(insts))
	for _, in := range insts {
		if in.ULID == "" || in.ULID == excludeSelfULID {
			continue
		}
		out = append(out, in.ULID)
	}
	return out
}

// ── Read helpers ──────────────────────────────────────────────────────────────

// ListAppsForInstance returns all app_registry rows for the given instance ULID.
// Only rows with installed=1 are returned by default; pass includeRemoved=true
// to also include uninstalled entries.
func (as *AppSync) ListAppsForInstance(instanceULID string, includeRemoved bool) ([]AppRegistryEntry, error) {
	query := `
		SELECT instance_ulid, app_id, app_version, installed, installed_by, updated_at
		FROM app_registry
		WHERE instance_ulid = ?`
	if !includeRemoved {
		query += " AND installed = 1"
	}
	query += " ORDER BY app_id"

	rows, err := as.db.Query(query, instanceULID)
	if err != nil {
		return nil, fmt.Errorf("appsync: ListAppsForInstance: %w", err)
	}
	defer rows.Close()

	return scanEntries(rows)
}

// ListAllApps returns every row in app_registry across all instances.
func (as *AppSync) ListAllApps() ([]AppRegistryEntry, error) {
	rows, err := as.db.Query(`
		SELECT instance_ulid, app_id, app_version, installed, installed_by, updated_at
		FROM app_registry
		ORDER BY instance_ulid, app_id
	`)
	if err != nil {
		return nil, fmt.Errorf("appsync: ListAllApps: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ChangesetSince returns every app_registry row whose updated_at is strictly
// greater than the provided cursor, ordered by (updated_at, instance_ulid,
// app_id) so a caller can advance its cursor to the max returned UpdatedAt.
//
// This is the read-side primitive used by the fabric P2P sync transport
// (GET /api/fabric/changeset?since=<cursor>): a peer asks "what changed since I
// last saw you?" and gets back exactly the rows that postdate its cursor,
// including uninstalled (installed=0) rows so OR-set/LWW removals propagate.
//
// A zero `since` returns the full table (a cold-start full sync). The returned
// rows carry the writer node id (installed_by) and updated_at so the receiver's
// ApplyChangeset can run the deterministic LWW merge unchanged.
func (as *AppSync) ChangesetSince(since time.Time) ([]AppRegistryEntry, error) {
	// We compare on the RFC3339Nano text form because that is how updated_at is
	// stored; lexicographic ordering of RFC3339Nano UTC strings matches temporal
	// ordering. Using the text comparison (rather than a parsed-time WHERE) keeps
	// the query index-friendly and avoids per-row parse cost in SQLite.
	var rows *sql.Rows
	var err error
	if since.IsZero() {
		rows, err = as.db.Query(`
			SELECT instance_ulid, app_id, app_version, installed, installed_by, updated_at
			FROM app_registry
			ORDER BY updated_at, instance_ulid, app_id`)
	} else {
		cursor := since.UTC().Format(time.RFC3339Nano)
		rows, err = as.db.Query(`
			SELECT instance_ulid, app_id, app_version, installed, installed_by, updated_at
			FROM app_registry
			WHERE updated_at > ?
			ORDER BY updated_at, instance_ulid, app_id`, cursor)
	}
	if err != nil {
		return nil, fmt.Errorf("appsync: ChangesetSince: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// scanEntries scans *sql.Rows into a slice of AppRegistryEntry.
func scanEntries(rows *sql.Rows) ([]AppRegistryEntry, error) {
	var out []AppRegistryEntry
	for rows.Next() {
		var (
			e          AppRegistryEntry
			installed  int
			updatedRaw string
		)
		if err := rows.Scan(
			&e.InstanceULID, &e.AppID, &e.AppVersion,
			&installed, &e.InstalledBy, &updatedRaw,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		e.Installed = installed == 1
		if updatedRaw != "" {
			e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── Wire format helpers ───────────────────────────────────────────────────────

// MarshalChangeset serialises an AppChangeset to JSON bytes.
func MarshalChangeset(cs *AppChangeset) ([]byte, error) {
	return json.Marshal(cs)
}

// UnmarshalChangeset parses JSON bytes into an AppChangeset.
func UnmarshalChangeset(data []byte) (*AppChangeset, error) {
	var cs AppChangeset
	if err := json.Unmarshal(data, &cs); err != nil {
		return nil, fmt.Errorf("appsync: unmarshal: %w", err)
	}
	return &cs, nil
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

// RegisterAppSyncHandlers wires the MINST-04 HTTP endpoints into mux.
//
//	GET /api/instances/{ulid}/apps   — per-instance app inventory (installed only)
//
// The {ulid} path segment is extracted from the URL; an empty or missing
// segment returns 400.
//
// Usage (from a routes_*.go in cmd/server — never from main.go):
//
//	multiinstance.RegisterAppSyncHandlers(mux, appSync)
func RegisterAppSyncHandlers(mux *http.ServeMux, as *AppSync) {
	// Go 1.22+ pattern: "GET /api/instances/{ulid}/apps"
	mux.HandleFunc("GET /api/instances/{ulid}/apps", func(w http.ResponseWriter, r *http.Request) {
		ulid := r.PathValue("ulid")
		if ulid == "" {
			// Fallback: parse from path manually for older Go versions.
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/instances/"), "/")
			if len(parts) >= 2 {
				ulid = parts[0]
			}
		}
		if ulid == "" {
			http.Error(w, `{"error":"missing instance ulid"}`, http.StatusBadRequest)
			return
		}

		apps, err := as.ListAppsForInstance(ulid, false)
		if err != nil {
			log.Printf("[appsync] ListAppsForInstance %s: %v", ulid, err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if apps == nil {
			apps = []AppRegistryEntry{}
		}

		w.Header().Set("Content-Type", "application/json")
		if encErr := json.NewEncoder(w).Encode(apps); encErr != nil {
			return
		}
	})
}
