// MINST-04: App registry sync — pure-Go CRDT changeset replication.
//
// AppSync replicates the app_registry table across instances using a
// pure-Go CRDT changeset layer (no CGO / cr-sqlite extension required).
//
// # SYNC-APPS-01: desire and realisation
//
// The standing directive is that a user's instances are ONE OS — "each instance
// is almost a direct clone of the next". app_registry alone cannot express that.
// It is keyed (instance_ulid, app_id), which makes it a PER-INSTANCE INVENTORY:
// it records what each box happens to have. That is a DESCRIPTION. "I installed
// Steam, put it everywhere" is an INTENT, and an inventory has no way to hold
// one — there is no row that means "the fleet should have this", only rows that
// mean "this box does".
//
// So there are two sets, deliberately in two tables with two different keys:
//
//	app_desired    one row per app_id      — FLEET-LEVEL DESIRE.  What the user asked for.
//	app_registry   one row per (inst,app)  — PER-BOX REALISATION. What actually happened.
//
// Three properties are load-bearing, and each is enforced structurally rather
// than by convention:
//
//  1. REMOVAL IS A STATE, NOT AN ABSENCE. DesireRemove writes desired=0 with a
//     fresh timestamp — a tombstone row that is never vacuumed. A desired set in
//     which removal is spelled "the row is gone" resurrects apps the user
//     deleted on every sync, forever: the peer that has not heard about the
//     removal re-sends its copy and the removing box merges it back as news.
//     Against a tombstone that re-send is simply an older write, and loses.
//
//  2. REALISATION IS NEVER AUTHORITATIVE OVER DESIRE. Nothing in the realisation
//     path writes app_desired. mergeEntry routes a wire row to mergeDesired or to
//     the realisation merge on the reserved key and the two never meet, so a box
//     that FAILED to install an app cannot report that back as "not wanted". One
//     broken instance therefore cannot uninstall the fleet. The reconciler runs
//     strictly one way: desire → this box's disk → a report of what happened.
//
//  3. AN ARCH MISMATCH IS A REALISATION FAILURE, NOT A SPECIAL CASE. A box that
//     cannot install a desired app records realise_state='failed' with the reason
//     the installer produced (services/appnet/arch.go's ArchUnavailableReason
//     yields "requires amd64; this box is arm64"), and that report replicates.
//     There is no arch-shaped branch in this file, on purpose: the moment arch
//     gets its own path, every other reason an install can fail goes silent
//     again — which is the failure mode this whole split exists to remove.
//
// # Why the desired set rides app_registry's wire type
//
// A desire row travels as an AppRegistryEntry whose InstanceULID is the reserved
// sentinel DesiredSetULID ("@fleet"). '@' is not in the Crockford base32 ULID
// alphabet, so no real instance can collide with it.
//
// This is a discriminator, not a hack, and the alternative was worse. The fabric
// transport's interface (fabric.AppSyncMerger) is fixed over []AppRegistryEntry
// and internal/fabric is a separate concern; a second slice on AppChangeset would
// have meant changing that interface, its handlers and its cursor logic, and — in
// the failure case that matters — a desire row that silently did not ride the
// transport at all while every test still passed. Reusing the one wire row means
// the desired set inherits, unchanged and already proven: the cursor, the
// signature, the roster verification and the revocation check at the door.
//
// # Why desire is NOT quorum-gated, and what replaces the quorum
//
// The uninstall quorum below defends app_registry against a holder of the shared
// fabric secret unilaterally removing apps — necessary precisely BECAUSE that
// secret is identical on every box and cannot distinguish peers.
//
// Applying it to DESIRE would be a product defect, not a hardening: a user would
// have to uninstall an app on a majority of their own boxes before it went away
// anywhere, which is the opposite of what the directive asks for. Intent is
// expressed once, by a person, on one machine.
//
// What defends the desired set instead is admission: mergeDesired runs ONLY for a
// changeset whose origin verified — a rostered, non-revoked peer whose Ed25519
// signature checks out against its ROSTERED key (verifyChangesetSignature, which
// the eviction work made revocation-aware). An unverified changeset's desire rows
// are DROPPED, not merely uncounted; there is no quorum to withhold from, so
// "does not count" would have meant "applies". This adds no trust path — it is
// the same door, consulted for a different set.
//
// The residual, stated rather than hidden: a rostered box that is compromised but
// not yet revoked can remove apps fleet-wide, because it can say anything the
// user could say. That was already true of every other intent the fabric carries.
// It is bounded by revocation (which now works and is enforced before merge), it
// is recorded (actor_ulid names who removed what and when), and it is one action
// to undo. Requiring quorum would not have bounded it either — a compromised box
// can wait for the user to remove something and corroborate it.
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
//	observations whose observed_at is NOT before the generation's install timestamp.
//	This dual guard (epoch + temporal watermark) prevents a verified peer from
//	bypassing the GC by replaying a pre-reinstall signed changeset: the replayed
//	observation lands at the current epoch (recordUninstallObservation uses
//	currentGeneration at apply-time), but distinctUninstallOrigins additionally
//	requires observed_at >= generation_at so the stale-timestamp replay is excluded
//	(CRDT-GC-01 temporal watermark).
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
	"context"
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

	// RealiseState is what this instance managed to do about the app, and it is
	// the half of the split that makes a failure visible instead of silent:
	// RealiseRealised / RealiseRemoved / RealiseFailed, or "" for a row written
	// before SYNC-APPS-01 (or by a peer that predates it).
	//
	// It travels WITH the row on the wire, because a box that reports "I cannot
	// install this, and here is why" only into its own database has reported
	// nothing — the user is standing at a different box.
	RealiseState string `json:"realise_state,omitempty"`
	// RealiseDetail is the human-readable reason for RealiseFailed, verbatim
	// from whatever refused the install. An architecture mismatch arrives here
	// as "requires amd64; this box is arm64" by the same route a failed download
	// or a bad signature does — see the package doc on why arch has no branch.
	RealiseDetail string `json:"realise_detail,omitempty"`

	// ── SYNC-APPS-02: re-realisation ─────────────────────────────────────────
	//
	// ReRealiseCount is how many times this instance has installed this app
	// AGAIN after having already realised it and then lost the bits. It is a
	// GROW-ONLY counter, merged as a max rather than by LWW (see upsertEntry),
	// so a peer holding an older copy cannot talk it back down and the several
	// merge paths that construct partial rows pass 0 harmlessly.
	//
	// It replicates because it has to. On the box this exists for, the local
	// database sits in the same tmpfs as the app directory and dies with it on
	// every reboot, so a count kept locally would read zero on exactly the boots
	// it is meant to count. The fleet is a volatile box's only memory.
	ReRealiseCount int `json:"rerealise_count,omitempty"`
	// ReRealisedAt is when the last re-realisation happened. Deliberately not
	// UpdatedAt: that moves for every report about the row, and the backoff has
	// to measure from the last RE-REALISATION specifically.
	ReRealisedAt time.Time `json:"rerealised_at,omitempty"`
	// ReRealiseReason is the storage-durability fact this box measured about
	// ITSELF at that moment (Realiser's optional StorageVolatility detail), or
	// "" when it could not measure one. It is the answer to "why did this box
	// have to install it again", and it is empty rather than guessed because a
	// user reads it standing at a different box.
	ReRealiseReason string `json:"rerealise_reason,omitempty"`
}

// Realisation states for AppRegistryEntry.RealiseState.
const (
	// RealiseUnknown is a row that predates SYNC-APPS-01 or was never attempted.
	// It is the zero value on purpose: every existing row reads back as this, so
	// an upgrade changes no behaviour.
	RealiseUnknown = ""
	// RealiseRealised means the app is installed and working on that instance.
	RealiseRealised = "realised"
	// RealiseRemoved means the app was uninstalled from that instance.
	RealiseRemoved = "removed"
	// RealiseFailed means the instance TRIED and could not. RealiseDetail says
	// why. This is the state that distinguishes "this box cannot run it" from
	// "this box has not got round to it", which absence alone cannot.
	RealiseFailed = "failed"
)

// DesiredSetULID is the reserved InstanceULID that marks a wire row as a
// FLEET-LEVEL DESIRE rather than a per-instance realisation.
//
// '@' is outside the Crockford base32 alphabet ULIDs are drawn from, so no real
// instance identifier can ever collide with it. TestDesiredSetULIDCannotBeAULID
// pins that rather than trusting this sentence, and localMutate refuses to write
// a realisation row that claims the key.
const DesiredSetULID = "@fleet"

// DesiredEntry is one row of the FLEET-LEVEL desired set: one entry per app,
// not per instance. It is the answer to "what has the user asked to be
// installed", which is the thing the directive makes the default.
type DesiredEntry struct {
	// AppID is the canonical app slug. It is the whole primary key — there is
	// deliberately no instance in it.
	AppID string `json:"app_id"`
	// Version is the desired version, or "" for "whatever the registry calls
	// latest".
	Version string `json:"version"`
	// Desired is the two-state flag. TRUE: the user wants this app on every
	// instance. FALSE: the user REMOVED it — an explicit tombstone, which is
	// what stops the removal being resurrected by any peer that still holds a
	// pre-removal copy. A removed entry keeps its row forever.
	Desired bool `json:"desired"`
	// ActorULID is the instance on which the intent was expressed. It is the
	// deterministic LWW tie-break and the record of who removed what. It is NOT
	// an authorisation input: that is the changeset signature.
	ActorULID string `json:"actor_ulid"`
	// UpdatedAt is when the intent was expressed; LWW ordering.
	UpdatedAt time.Time `json:"updated_at"`
}

// wire renders a desire as the single AppRegistryEntry wire row, tagged with the
// reserved sentinel so a receiver routes it to the desired set. See the package
// doc for why there is one wire type rather than two.
func (d DesiredEntry) wire() AppRegistryEntry {
	return AppRegistryEntry{
		InstanceULID: DesiredSetULID,
		AppID:        d.AppID,
		AppVersion:   d.Version,
		Installed:    d.Desired,
		InstalledBy:  d.ActorULID,
		UpdatedAt:    d.UpdatedAt,
	}
}

// desiredFromWire is wire's inverse. It does not check the sentinel; callers
// route on isDesiredRow first.
func desiredFromWire(e AppRegistryEntry) DesiredEntry {
	return DesiredEntry{
		AppID:     e.AppID,
		Version:   e.AppVersion,
		Desired:   e.Installed,
		ActorULID: e.InstalledBy,
		UpdatedAt: e.UpdatedAt,
	}
}

// isDesiredRow reports whether a wire row belongs to the fleet desired set
// rather than to some instance's realisation inventory. It is the single
// routing predicate; every path that consumes a wire row asks it.
func isDesiredRow(e AppRegistryEntry) bool { return e.InstanceULID == DesiredSetULID }

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
				// NODE-CAP-01: seed the store-only flag from VULOS_STORE_ONLY on
				// FIRST registration only. Later SetIdentity calls take the
				// existing-instance branch above (Get→Upsert), which preserves a
				// value the operator has since changed via the Settings toggle.
				//
				// Ordering assumption: this self-registration is expected to run
				// before any cloud sync first touches the owner ULID. If a future
				// wiring ever ran Sync() first, the owner row would be created as
				// serving (the CP has no VULOS_STORE_ONLY) and this env seed would
				// then be skipped by the existing-row branch — a headless
				// store-only intent could be lost. Cloud sync is currently unwired,
				// so this is a latent ordering constraint, noted so it isn't a
				// surprise when Sync/presence polling is turned on.
				StoreOnly: storeOnlyEnv(),
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
// variant, wrap with the OS keyring like internal/airouter does. The fabric is
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

// LocalInstall records that appID@version IS REALISED on THIS instance — the
// box has it, on disk, working — and fires the local-change hook (fabric Nudge)
// so peers learn promptly.
//
// It writes a single app_registry row stamped with UpdatedAt = now and
// InstalledBy = instanceULID (this box is the writer node), which is exactly the
// LWW input ChangesetSince/EmitChangeset will later replicate. instanceULID must
// be this box's stable ULID.
//
// This is a REPORT, not an instruction. It says what happened here; it does not
// and must not touch the fleet desired set (see the package doc, property 2).
func (as *AppSync) LocalInstall(instanceULID, appID, version string) error {
	return as.localMutate(instanceULID, appID, version, true, RealiseRealised, "", nil)
}

// LocalReRealise records that appID@version is realised here AGAIN — the box had
// already realised it, the bits were gone when the reconciler looked, and it has
// just put them back. count is the new cumulative total (the previous count plus
// one) and reason is the storage-durability fact this box measured about itself,
// or "" when it could not measure one.
//
// The row it writes is a normal realised row: installed=true,
// realise_state='realised'. That is the whole point. Nothing failed — the box
// installed the app, the app worked, and then its storage evaporated — so
// recording a failure would make the fleet show a working box as broken, and
// clearing the desire would delete what the user asked for. What changes is only
// that the row now carries how many times this has happened and why.
func (as *AppSync) LocalReRealise(instanceULID, appID, version string, count int, reason string) error {
	if count <= 0 {
		return fmt.Errorf("appsync: LocalReRealise: count must be >= 1 (a re-realisation that counts zero of them is an ordinary install: use LocalInstall)")
	}
	return as.localMutate(instanceULID, appID, version, true, RealiseRealised, "", &reRealisation{count: count, reason: reason})
}

// reRealisation carries the three SYNC-APPS-02 fields through localMutate. The
// timestamp is taken from the same clock as the row's UpdatedAt at write time
// rather than passed in, so a re-realisation cannot be back- or post-dated by a
// caller.
type reRealisation struct {
	count  int
	reason string
}

// LocalUninstall records that appID is no longer realised on THIS instance
// (OR-set flag flipped to false, stamped now) and fires the local-change hook.
// The actual propagation still obeys the uninstall-quorum rules on the receiving
// peers.
//
// Also a report about this box only. Removing an app FROM THE FLEET is
// DesireRemove, which is a different set with a different key.
func (as *AppSync) LocalUninstall(instanceULID, appID string) error {
	return as.localMutate(instanceULID, appID, "", false, RealiseRemoved, "", nil)
}

// ReportRealiseFailure records that THIS instance tried to realise a desired app
// and could not, with the reason verbatim.
//
// This is the entry the whole desire/realisation split exists for. Without it a
// box that cannot run an app is indistinguishable from a box that has not tried
// yet — both are simply "no row" — and the user, standing at a different box,
// sees an app that is missing for no stated reason. With it, the fleet can say
// "your arm64 box cannot install this: requires amd64; this box is arm64".
//
// The row is written installed=false (the box genuinely does not have the app)
// with realise_state='failed'. It is a statement about THIS instance's row only:
// the uninstall observation it produces is keyed to this instance as the target
// and this instance as the observer, so it can never contribute toward removing
// the app from a peer, and it never touches the desired set. The desire stands,
// which is what makes the failure a report the user can act on rather than a
// silent capitulation.
func (as *AppSync) ReportRealiseFailure(instanceULID, appID, version, reason string) error {
	if reason == "" {
		// A failure with no reason is the silence this method exists to remove.
		return fmt.Errorf("appsync: ReportRealiseFailure: reason must not be empty (a failure with no reason is the silence this reports against)")
	}
	return as.localMutate(instanceULID, appID, version, false, RealiseFailed, reason, nil)
}

// localMutate is the shared write path for the REALISATION reports
// (LocalInstall / LocalUninstall / ReportRealiseFailure). It upserts the row in a
// single transaction, then — only on commit success — fires the local-change
// hook so the fabric sync loop pushes the change without waiting the tick.
func (as *AppSync) localMutate(instanceULID, appID, version string, installed bool, realiseState, realiseDetail string, rr *reRealisation) error {
	if instanceULID == "" {
		return fmt.Errorf("appsync: localMutate: instanceULID must not be empty")
	}
	if appID == "" {
		return fmt.Errorf("appsync: localMutate: appID must not be empty")
	}
	// The realisation set and the desired set share a wire type and are told
	// apart by exactly one field. A realisation row that claimed the sentinel
	// would be merged into the fleet desired set by every peer — a box's local
	// report silently rewriting what the user wants, which is precisely the
	// direction property 2 forbids. Refuse at the only door that can produce it.
	if instanceULID == DesiredSetULID {
		return fmt.Errorf("appsync: localMutate: instanceULID %q is the reserved fleet-desire key; a realisation report may not claim it (use DesireInstall/DesireRemove to express intent)", DesiredSetULID)
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
		InstanceULID:  instanceULID,
		AppID:         appID,
		AppVersion:    version,
		Installed:     installed,
		InstalledBy:   instanceULID,
		UpdatedAt:     time.Now().UTC(),
		RealiseState:  realiseState,
		RealiseDetail: realiseDetail,
	}
	if rr != nil {
		entry.ReRealiseCount = rr.count
		entry.ReRealisedAt = entry.UpdatedAt
		entry.ReRealiseReason = rr.reason
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

// ── The fleet desired set (SYNC-APPS-01) ─────────────────────────────────────

// DesireInstall records that the user wants appID installed ON EVERY INSTANCE,
// expressed on the box identified by actorULID. Version "" means "whatever the
// registry calls latest".
//
// One row per app, no instance in the key. This is the write that was missing
// and that made the whole replicator carry nothing: an intent no table could
// hold, so nothing was ever written, so every box converged an empty inventory
// and reported it as healthy.
func (as *AppSync) DesireInstall(actorULID, appID, version string) error {
	return as.desireMutate(actorULID, appID, version, true)
}

// DesireRemove records that the user no longer wants appID anywhere.
//
// It writes desired=0 — an explicit TOMBSTONE with a fresh timestamp — and never
// deletes the row. That is the whole answer to resurrection: a peer that has not
// yet heard about the removal still holds desired=1 at an OLDER timestamp, so
// when the two meet the removal is the newer write and wins, in both directions,
// however many times the stale copy is re-sent. Delete the row instead and the
// same exchange re-installs the app, on every sync, forever.
func (as *AppSync) DesireRemove(actorULID, appID string) error {
	return as.desireMutate(actorULID, appID, "", false)
}

// desireMutate is the shared local write path for the desired set.
//
// The timestamp is max(now, latest-seen + 1ns), not now. A locally expressed
// intent MUST win over everything this box has already seen, and physical clocks
// between a user's own boxes routinely disagree by more than the seconds between
// two of their actions. With a bare now(), a box whose clock lags would have its
// user's "remove this" silently lose the LWW comparison against a remote row it
// had just merged — the removal would appear to work, the UI would show it gone,
// and the next sync would bring the app back with no error anywhere. Bumping past
// the observed maximum keeps LWW's total order while making "the user just said
// so" the newest fact in the system, which it is.
func (as *AppSync) desireMutate(actorULID, appID, version string, desired bool) error {
	if actorULID == "" {
		return fmt.Errorf("appsync: desireMutate: actorULID must not be empty")
	}
	if actorULID == DesiredSetULID {
		return fmt.Errorf("appsync: desireMutate: actorULID must be a real instance, not the reserved key %q", DesiredSetULID)
	}
	if appID == "" {
		return fmt.Errorf("appsync: desireMutate: appID must not be empty")
	}
	tx, err := as.db.Begin()
	if err != nil {
		return fmt.Errorf("appsync: desireMutate: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stamp := time.Now().UTC()
	var maxSeen string
	if err := tx.QueryRow(`SELECT COALESCE(MAX(updated_at), '') FROM app_desired`).Scan(&maxSeen); err != nil {
		return fmt.Errorf("appsync: desireMutate: read desire watermark: %w", err)
	}
	if maxSeen != "" {
		if prev, perr := time.Parse(time.RFC3339Nano, maxSeen); perr == nil && !stamp.After(prev) {
			stamp = prev.Add(time.Nanosecond)
		}
	}

	if err := as.upsertDesired(tx, DesiredEntry{
		AppID:     appID,
		Version:   version,
		Desired:   desired,
		ActorULID: actorULID,
		UpdatedAt: stamp,
	}); err != nil {
		return fmt.Errorf("appsync: desireMutate: upsert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("appsync: desireMutate: commit: %w", err)
	}
	committed = true
	as.fireLocalChange()
	return nil
}

// upsertDesired writes a desired-set row unconditionally. Callers that merge a
// REMOTE row must run the LWW comparison in mergeDesired first; this is the raw
// write.
func (as *AppSync) upsertDesired(tx *sql.Tx, d DesiredEntry) error {
	desired := 0
	if d.Desired {
		desired = 1
	}
	ts := ""
	if !d.UpdatedAt.IsZero() {
		ts = d.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.Exec(`
		INSERT INTO app_desired (app_id, desired_version, desired, actor_ulid, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(app_id) DO UPDATE SET
			desired_version = excluded.desired_version,
			desired         = excluded.desired,
			actor_ulid      = excluded.actor_ulid,
			updated_at      = excluded.updated_at
	`, d.AppID, d.Version, desired, d.ActorULID, ts)
	return err
}

// mergeDesired merges one REMOTE desired-set row.
//
// The algebra is last-writer-wins on UpdatedAt, tie-broken on the larger
// ActorULID. Two things about it are deliberate and neither is the default the
// realisation set uses:
//
//   - THERE IS NO QUORUM. Intent is expressed once, by a person, on one box.
//     Requiring N boxes to agree before an uninstall took effect would mean the
//     user uninstalling an app on a majority of their own machines. What guards
//     this set instead is admission: ApplyChangeset only calls this for a
//     changeset whose origin VERIFIED — rostered, not revoked, signature good.
//     An unverified peer's desire rows are dropped entirely.
//
//   - A TIE IS NOT RESOLVED "INSTALL WINS". The realisation set's OR-set default
//     is right there — an install and an uninstall of the same thing at the same
//     instant is more likely a straggler than a decision. For DESIRE it would be
//     a resurrection bug: two boxes acting on the same user action stamp the same
//     removal, and install-wins on an exact tie means the removal never lands.
//     The tie-break is the actor id alone, which is deterministic, symmetric, and
//     has no opinion about which polarity is safer.
//
// The comparison is total and order-independent (max over (UpdatedAt, ActorULID)),
// so peers applying the same rows in any order converge.
func (as *AppSync) mergeDesired(tx *sql.Tx, remote DesiredEntry) error {
	if remote.AppID == "" {
		return fmt.Errorf("desired row has empty app_id")
	}
	var (
		localVersion string
		localDesired int
		localActor   string
		localRaw     string
	)
	err := tx.QueryRow(`
		SELECT desired_version, desired, actor_ulid, updated_at
		FROM app_desired WHERE app_id = ?
	`, remote.AppID).Scan(&localVersion, &localDesired, &localActor, &localRaw)
	if err == sql.ErrNoRows {
		return as.upsertDesired(tx, remote)
	}
	if err != nil {
		return fmt.Errorf("scan local desired row: %w", err)
	}
	var localAt time.Time
	if localRaw != "" {
		localAt, _ = time.Parse(time.RFC3339Nano, localRaw)
	}
	switch {
	case remote.UpdatedAt.After(localAt):
		return as.upsertDesired(tx, remote)
	case remote.UpdatedAt.Equal(localAt):
		if remote.ActorULID > localActor {
			return as.upsertDesired(tx, remote)
		}
		return nil
	default:
		// Strictly older. This is the stale re-send of a pre-removal copy, and
		// discarding it is exactly what the tombstone bought.
		return nil
	}
}

// DesiredSet returns every row of the fleet desired set INCLUDING tombstones
// (Desired=false), ordered by app id. Tombstones are part of the state, not
// noise: the reconciler needs them to know an app must be REMOVED here rather
// than merely never installed.
func (as *AppSync) DesiredSet() ([]DesiredEntry, error) {
	rows, err := as.db.Query(`
		SELECT app_id, desired_version, desired, actor_ulid, updated_at
		FROM app_desired ORDER BY app_id`)
	if err != nil {
		return nil, fmt.Errorf("appsync: DesiredSet: %w", err)
	}
	defer rows.Close()
	return scanDesired(rows)
}

// DesiredApps returns only the apps the user currently wants (tombstones
// excluded) — the read a UI showing "your apps" wants.
func (as *AppSync) DesiredApps() ([]DesiredEntry, error) {
	all, err := as.DesiredSet()
	if err != nil {
		return nil, err
	}
	out := make([]DesiredEntry, 0, len(all))
	for _, d := range all {
		if d.Desired {
			out = append(out, d)
		}
	}
	return out, nil
}

// desiredChangesSince returns the desired-set rows changed after the cursor,
// rendered as wire rows so they ride the existing changeset transport unchanged.
func (as *AppSync) desiredChangesSince(since time.Time) ([]AppRegistryEntry, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if since.IsZero() {
		rows, err = as.db.Query(`
			SELECT app_id, desired_version, desired, actor_ulid, updated_at
			FROM app_desired ORDER BY updated_at, app_id`)
	} else {
		rows, err = as.db.Query(`
			SELECT app_id, desired_version, desired, actor_ulid, updated_at
			FROM app_desired WHERE updated_at > ? ORDER BY updated_at, app_id`,
			since.UTC().Format(time.RFC3339Nano))
	}
	if err != nil {
		return nil, fmt.Errorf("appsync: desiredChangesSince: %w", err)
	}
	defer rows.Close()
	ds, err := scanDesired(rows)
	if err != nil {
		return nil, err
	}
	out := make([]AppRegistryEntry, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.wire())
	}
	return out, nil
}

// scanDesired scans *sql.Rows into DesiredEntry values.
func scanDesired(rows *sql.Rows) ([]DesiredEntry, error) {
	var out []DesiredEntry
	for rows.Next() {
		var (
			d          DesiredEntry
			desired    int
			updatedRaw string
		)
		if err := rows.Scan(&d.AppID, &d.Version, &desired, &d.ActorULID, &updatedRaw); err != nil {
			return nil, fmt.Errorf("appsync: scan desired: %w", err)
		}
		d.Desired = desired == 1
		if updatedRaw != "" {
			d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
		}
		out = append(out, d)
	}
	return out, rows.Err()
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
// changeset's origin signs and a receiver verifies. The entry fields are joined
// with a NUL separator (which cannot appear in any of the string fields) and
// entries are sorted so the message is independent of slice order. The leading
// domain tag prevents cross-protocol signature reuse.
//
// It covers two sections:
//
//  1. UNINSTALL realisation entries — install entries are excluded because they
//     are not quorum-gated, which is what lets an install-only changeset be left
//     unsigned.
//  2. EVERY fleet-desire row, BOTH polarities (SYNC-APPS-01). Desire is not
//     quorum-gated, so the signature is the only thing standing between a peer
//     and the fleet's app set — leaving desired=1 rows out would have left an
//     unauthenticated REMOTE-INSTALL primitive open, which is a strictly worse
//     hole than the unauthenticated remote-uninstall the quorum was built for.
//     Desire rows are excluded from section 1 (they are not observations about an
//     instance) so no row is covered twice.
//
// Section 2 is APPENDED and is emitted only when there are desire rows, so for
// any changeset a pre-SYNC-APPS-01 box could have produced the message is
// byte-identical to what that box signed. A mixed-version fleet keeps verifying.
func changesetSigningMessage(originULID string, entries []AppRegistryEntry) []byte {
	const domain = "vulos:appsync:uninstall-observation:v1"
	const desireSection = "vulos:appsync:fleet-desire:v1"
	type rec struct {
		instanceULID string
		appID        string
		updatedAt    string
	}
	type drec struct {
		appID     string
		updatedAt string
		desired   string
	}
	recs := make([]rec, 0, len(entries))
	drecs := make([]drec, 0, len(entries))
	for _, e := range entries {
		ts := ""
		if !e.UpdatedAt.IsZero() {
			ts = e.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		if isDesiredRow(e) {
			d := "0"
			if e.Installed {
				d = "1"
			}
			drecs = append(drecs, drec{appID: e.AppID, updatedAt: ts, desired: d})
			continue
		}
		if e.Installed {
			continue // only uninstall observations are signed/quorum-gated
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
	sort.Slice(drecs, func(i, j int) bool {
		if drecs[i].appID != drecs[j].appID {
			return drecs[i].appID < drecs[j].appID
		}
		if drecs[i].updatedAt != drecs[j].updatedAt {
			return drecs[i].updatedAt < drecs[j].updatedAt
		}
		return drecs[i].desired < drecs[j].desired
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
	if len(drecs) > 0 {
		b.WriteByte(0)
		b.WriteString(desireSection)
		for _, d := range drecs {
			b.WriteByte(0)
			b.WriteString(d.appID)
			b.WriteByte(0)
			b.WriteString(d.updatedAt)
			b.WriteByte(0)
			b.WriteString(d.desired)
		}
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
	if !originVerified && hasDesire(cs.Entries) {
		log.Printf("[appsync] changeset from %q carries fleet-desire row(s) but is unverified "+
			"(unknown/unsigned/bad-signature/revoked origin) — they will be DROPPED, not merged", originULID)
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
	// SYNC-APPS-01 routing. A wire row is EITHER a fleet desire or a per-instance
	// realisation, and this is the only place the two are told apart. Everything
	// below this branch — generation epochs, uninstall observations, the OR-set,
	// the quorum — belongs to the realisation set alone and must not run for a
	// desire row. Routing FIRST is what makes property 2 structural: there is no
	// path from here into the realisation tables for a desire row, and none from
	// the realisation merge into app_desired for anything.
	if isDesiredRow(remote) {
		// Admission, not quorum (see mergeDesired). An unverified origin's desire
		// rows are DROPPED. "Recorded but not counted" is the realisation set's
		// answer and has no meaning here: with no quorum to withhold from, not
		// counting an unverified write would be the same as applying it.
		if !originVerified {
			log.Printf("[appsync] DROPPING desired-set row for %q from unverified origin %q "+
				"(unknown/unsigned/bad-signature/revoked) — the fleet desired set is writable only by a verified rostered peer", remote.AppID, originULID)
			return nil
		}
		return as.mergeDesired(tx, desiredFromWire(remote))
	}

	// SYNC-APPS-02: merge the grow-only re-realisation counter FIRST, on its own,
	// because most of what follows can decide not to write the row at all. The
	// LWW switch below discards a strictly-older remote outright and has a tie
	// branch whose "local already wins" case returns without an upsert; a counter
	// carried only inside upsertEntry would be silently dropped on both. The
	// counter is not LWW state — it is monotonic — so it converges independently
	// of who wins the row, which is the only reason it is safe to merge it before
	// the quorum rules have had their say. It cannot flip an installed flag, so
	// it cannot be used to remove anything.
	if err := as.bumpReRealiseCounter(tx, remote); err != nil {
		return fmt.Errorf("merge re-realisation counter: %w", err)
	}

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
// generation AND whose observation timestamp is not older than the generation's
// install timestamp.
//
// Two guards are enforced:
//
//  1. epoch = currentGeneration — the epoch filter GCs observations accumulated
//     before a re-install without a destructive bulk delete (OR-set monotonicity).
//
//  2. observed_at >= generation_at — the timestamp guard ensures that a verified
//     peer cannot "revive" a pre-reinstall observation by replaying a
//     stale-timestamped changeset after the reinstall.  Without this check,
//     recordUninstallObservation stamps the new observation with the current
//     epoch (because currentGeneration is called at apply-time) even though the
//     changeset's observed_at predates the reinstall — bypassing the GC
//     (CRDT-GC-01 temporal watermark).
//
// Both checks are necessary: epoch alone is insufficient because replayed
// stale-timestamp changesets from verified peers are stamped with the current
// epoch at record-time.
func (as *AppSync) distinctUninstallOrigins(tx *sql.Tx, instanceULID, appID string) (int, error) {
	var gen int64
	var generationAt string
	err := tx.QueryRow(`
		SELECT generation, generation_at FROM app_install_generation
		WHERE instance_ulid = ? AND app_id = ?
	`, instanceULID, appID).Scan(&gen, &generationAt)
	if err == sql.ErrNoRows {
		// No generation record: genesis state; epoch 0 with no install timestamp.
		// Count observations with epoch 0 and no timestamp guard (no reinstall
		// has occurred, so no GC watermark to enforce).
		var n int
		err = tx.QueryRow(`
			SELECT COUNT(*) FROM app_uninstall_observations
			WHERE instance_ulid = ? AND app_id = ? AND epoch = 0
		`, instanceULID, appID).Scan(&n)
		return n, err
	}
	if err != nil {
		return 0, err
	}
	var n int
	if generationAt == "" {
		// generation_at is empty (legacy / corrupt row) — fall back to epoch-only filter.
		err = tx.QueryRow(`
			SELECT COUNT(*) FROM app_uninstall_observations
			WHERE instance_ulid = ? AND app_id = ? AND epoch = ?
		`, instanceULID, appID, gen).Scan(&n)
	} else {
		// Dual guard: epoch matches AND the observation was recorded at or after the
		// install that set this generation (temporal watermark — CRDT-GC-01).
		err = tx.QueryRow(`
			SELECT COUNT(*) FROM app_uninstall_observations
			WHERE instance_ulid = ? AND app_id = ? AND epoch = ? AND observed_at >= ?
		`, instanceULID, appID, gen, generationAt).Scan(&n)
	}
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

// hasUninstall reports whether any entry in the slice is a REALISATION uninstall
// (the quorum-gated kind). Desire rows are excluded: they carry the same
// Installed=false spelling for a removal but are governed by admission, not
// quorum, and folding them in here would make the "will not count toward quorum"
// warning say something false about them.
func hasUninstall(entries []AppRegistryEntry) bool {
	for _, e := range entries {
		if !isDesiredRow(e) && !e.Installed {
			return true
		}
	}
	return false
}

// hasDesire reports whether any entry is a fleet-desire row. Used to log the
// distinct thing that happens to those under an unverified origin: they are
// dropped outright.
func hasDesire(entries []AppRegistryEntry) bool {
	for _, e := range entries {
		if isDesiredRow(e) {
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

	reRealisedAt := ""
	if !e.ReRealisedAt.IsZero() {
		reRealisedAt = e.ReRealisedAt.UTC().Format(time.RFC3339Nano)
	}

	// SYNC-APPS-02: the three re-realisation columns do NOT follow the LWW rule
	// the rest of the row follows, and that is what makes them safe to add here.
	//
	// rerealise_count is a GROW-ONLY counter merged as a max. Two consequences,
	// both load-bearing. A peer that still holds an older copy of this row cannot
	// talk the count back down when its write wins LWW on the other fields — a
	// volatile box's whole memory of how often it has re-realised lives in the
	// fleet, and an LWW counter would lose it to any stale peer. And every
	// existing caller — LocalInstall, ReportRealiseFailure, and the three
	// mergeEntry branches that construct a partial AppRegistryEntry for the
	// tie-break — passes the zero value and is therefore a no-op against the
	// stored count, rather than silently erasing it.
	//
	// The other two ride WITH the count: they describe the event the count last
	// counted, so overwriting them from a row carrying an equal or lower count
	// would attach the wrong time and the wrong reason to it.
	_, err := tx.Exec(`
		INSERT INTO app_registry
			(instance_ulid, app_id, app_version, installed, installed_by, updated_at, realise_state, realise_detail,
			 rerealise_count, rerealise_at, rerealise_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_ulid, app_id) DO UPDATE SET
			app_version    = excluded.app_version,
			installed      = excluded.installed,
			installed_by   = excluded.installed_by,
			updated_at     = excluded.updated_at,
			realise_state  = excluded.realise_state,
			realise_detail = excluded.realise_detail,
			rerealise_at     = CASE WHEN excluded.rerealise_count > app_registry.rerealise_count
			                        THEN excluded.rerealise_at ELSE app_registry.rerealise_at END,
			rerealise_reason = CASE WHEN excluded.rerealise_count > app_registry.rerealise_count
			                        THEN excluded.rerealise_reason ELSE app_registry.rerealise_reason END,
			rerealise_count  = MAX(excluded.rerealise_count, app_registry.rerealise_count)
	`, e.InstanceULID, e.AppID, e.AppVersion, installed, e.InstalledBy, updatedAt, e.RealiseState, e.RealiseDetail,
		e.ReRealiseCount, reRealisedAt, e.ReRealiseReason)
	return err
}

// bumpReRealiseCounter raises an existing row's grow-only re-realisation counter
// (and the time and reason describing the event it counts) to the remote's, and
// never lowers it. It updates nothing when there is no local row yet — that case
// is the plain insert upsertEntry performs a few lines later, which carries the
// same three fields.
//
// Separate from upsertEntry on purpose: it has to run on the merge paths that
// deliberately do NOT write the row.
func (as *AppSync) bumpReRealiseCounter(tx *sql.Tx, e AppRegistryEntry) error {
	if e.ReRealiseCount <= 0 {
		return nil
	}
	reRealisedAt := ""
	if !e.ReRealisedAt.IsZero() {
		reRealisedAt = e.ReRealisedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.Exec(`
		UPDATE app_registry
		SET rerealise_count = ?, rerealise_at = ?, rerealise_reason = ?
		WHERE instance_ulid = ? AND app_id = ? AND rerealise_count < ?
	`, e.ReRealiseCount, reRealisedAt, e.ReRealiseReason, e.InstanceULID, e.AppID, e.ReRealiseCount)
	return err
}

// PeerPublicKeys returns the base64url Ed25519 public keys of every rostered
// instance except self, skipping any that has not yet published one.
//
// It is the roster source for rendezvous discovery: mDNS addresses a peer by a
// multicast name, which only works on one LAN, whereas the rendezvous role
// addresses a peer by its key and therefore works anywhere. These are the same
// keys the CRDT already uses to verify signed uninstall observations, so a box
// reachable over the WAN is exactly the box whose observations already count —
// no second identity is introduced.
func (as *AppSync) PeerPublicKeys(excludeSelfULID string) []string {
	ids := as.PeerInstanceIDs(excludeSelfULID)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if k := as.rosteredPubKey(id); k != "" {
			out = append(out, k)
		}
	}
	return out
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
		SELECT ` + appRegistryCols + `
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
		SELECT ` + appRegistryCols + `
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
			SELECT ` + appRegistryCols + `
			FROM app_registry
			ORDER BY updated_at, instance_ulid, app_id`)
	} else {
		cursor := since.UTC().Format(time.RFC3339Nano)
		rows, err = as.db.Query(`
			SELECT `+appRegistryCols+`
			FROM app_registry
			WHERE updated_at > ?
			ORDER BY updated_at, instance_ulid, app_id`, cursor)
	}
	if err != nil {
		return nil, fmt.Errorf("appsync: ChangesetSince: %w", err)
	}
	defer rows.Close()
	realised, err := scanEntries(rows)
	if err != nil {
		return nil, err
	}

	// SYNC-APPS-01: the fleet desired set rides the SAME cursor and the SAME
	// wire rows, tagged with DesiredSetULID. This union is the single point at
	// which desire enters the transport, and it is why the desired set needed no
	// change to internal/fabric: from the transport's side it is more rows of the
	// type it already carries.
	//
	// Both halves are stamped from the same clock and compared as RFC3339Nano
	// text, so merging them and re-sorting keeps the cursor semantics the caller
	// relies on (advance to the max UpdatedAt returned).
	desired, err := as.desiredChangesSince(since)
	if err != nil {
		return nil, err
	}
	if len(desired) == 0 {
		return realised, nil
	}
	out := append(realised, desired...)
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.Before(out[j].UpdatedAt)
		}
		if out[i].InstanceULID != out[j].InstanceULID {
			return out[i].InstanceULID < out[j].InstanceULID
		}
		return out[i].AppID < out[j].AppID
	})
	return out, nil
}

// appRegistryCols is the column list every app_registry read selects, in the
// order scanEntries scans them. It is one constant rather than four copies
// because the failure it prevents is silent in the worst way: a SELECT that
// still lists eight columns while scanEntries expects eleven does not return a
// wrong row, it returns an error on a path most callers log and move past — and
// a SELECT that lists them in a different order returns a row whose reason
// string is in the version field.
const appRegistryCols = `instance_ulid, app_id, app_version, installed, installed_by, updated_at, ` +
	`realise_state, realise_detail, rerealise_count, rerealise_at, rerealise_reason`

// scanEntries scans *sql.Rows into a slice of AppRegistryEntry. The query must
// have selected appRegistryCols.
func scanEntries(rows *sql.Rows) ([]AppRegistryEntry, error) {
	var out []AppRegistryEntry
	for rows.Next() {
		var (
			e               AppRegistryEntry
			installed       int
			updatedRaw      string
			reRealisedAtRaw string
		)
		if err := rows.Scan(
			&e.InstanceULID, &e.AppID, &e.AppVersion,
			&installed, &e.InstalledBy, &updatedRaw,
			&e.RealiseState, &e.RealiseDetail,
			&e.ReRealiseCount, &reRealisedAtRaw, &e.ReRealiseReason,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		e.Installed = installed == 1
		if updatedRaw != "" {
			e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
		}
		if reRealisedAtRaw != "" {
			e.ReRealisedAt, _ = time.Parse(time.RFC3339Nano, reRealisedAtRaw)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── Reconciliation: desire → this box's disk → a report (SYNC-APPS-01) ───────

// Realiser is the box's ability to actually install and remove apps. It is
// deliberately expressed in primitive types: services/appnet owns the installer
// and neither package imports the other, so *appnet.AppStore satisfies this
// structurally and no adapter, and no import edge, exists to go stale.
//
// The direction of every method is one-way. Nothing on this interface can tell
// AppSync what the fleet WANTS — it can only be asked what this box HAS and told
// to change it. That is property 2 expressed as a type: a Realiser cannot write
// the desired set because it has no way to name it.
type Realiser interface {
	// RealisedVersions reports what is actually installed on this box, appID →
	// version. This is ground truth from disk, not from the registry table: the
	// table is a report ABOUT disk and can be stale or wrong, and reconciling
	// against a report of itself would make the loop believe its own bookkeeping.
	RealisedVersions() (map[string]string, error)
	// Realise installs appID at version ("" = latest). A returned error is the
	// reason the user is shown, verbatim — including an architecture mismatch,
	// which arrives here as an ordinary error and gets no special handling.
	Realise(ctx context.Context, appID, version string) error
	// Unrealise removes appID from this box.
	Unrealise(ctx context.Context, appID string) error
}

// DurabilityReporter is the OPTIONAL half of Realiser: a box that can measure
// whether the storage it installs apps into survives a reboot.
//
// It is a second interface rather than a fourth method on Realiser for one
// reason — every Realiser must be able to install, and not every Realiser can
// honestly answer this. *appnet.AppStore satisfies it structurally (it reads the
// kernel's mount table); a Realiser that does not is treated exactly as one that
// answers "I don't know", which is the same code path, so nothing has to be
// updated to keep working.
//
// The contract is deliberately narrow, and it matches what AppStore measures.
// The bool is TRUE only when the box has positively established that its app
// directory is RAM-backed. Durable and UNMEASURABLE both return false — there is
// no mount table on darwin or in a stripped container, and a box that cannot
// measure its own storage must not claim to have.
type DurabilityReporter interface {
	// StorageVolatility reports whether the app directory is on storage that
	// does not survive a reboot, plus a human-readable detail naming the mount
	// it decided on. The detail is "" when the answer is false.
	StorageVolatility() (bool, string)
}

// Causes for an install action in a ReconcilePlan.
const (
	// CauseNeverRealised: the fleet wants the app, it is not on this box's disk,
	// and this box has no realisation row saying it ever was. An ordinary first
	// install.
	CauseNeverRealised = "never-realised"
	// CauseReRealise: the fleet wants the app, it is not on this box's disk, and
	// this box's OWN replicated realisation row says it installed it and it
	// worked. The bits are gone; nothing failed. This is the case the reconciler
	// could not previously see, and seeing it is the whole of SYNC-APPS-02: on
	// an overlay boot every desired app lands here on every boot, and calling it
	// a first install is what turns a reboot into a full re-download.
	CauseReRealise = "re-realise"
	// CauseUndesired: the user removed the app fleet-wide and this box still has
	// it. The removal half of the plan.
	CauseUndesired = "undesired"
)

// ReconcileAction is one thing this box must do to match the fleet desired set.
type ReconcileAction struct {
	// AppID is the app to act on.
	AppID string
	// Version is the desired version for an install ("" = latest); unused for a
	// removal.
	Version string
	// Install is true to install, false to remove.
	Install bool

	// ── SYNC-APPS-02 ─────────────────────────────────────────────────────────

	// Cause is why this action is in the plan: one of CauseNeverRealised,
	// CauseReRealise or CauseUndesired. An install with an empty Cause is a plan
	// built by a caller that did not name the instance, and is treated as a
	// first install.
	Cause string
	// ReRealiseCount, for CauseReRealise, is how many times this box has ALREADY
	// re-realised this app before this pass. Zero on the first one.
	ReRealiseCount int
	// Reason, for CauseReRealise, is the storage-durability fact this box
	// measured about itself — e.g. "overlay at / whose upper layer
	// /run/vulos/rw/upper is tmpfs at /run/vulos/rw (RAM-backed)". It is ""
	// when the box could not measure one, which is a real and common answer and
	// is left empty rather than guessed.
	Reason string
	// Deferred is true when this action is in the plan but must NOT be performed
	// on this pass: the same app was re-realised too recently for another
	// download to be anything but waste. Only a re-realisation is ever deferred.
	// It stays in the plan, with its cause and its reason, precisely so that a
	// deferral is a visible decision rather than an install that quietly did not
	// happen.
	Deferred bool
	// NotBefore, when Deferred, is the time at which this action becomes due.
	NotBefore time.Time
}

// ReconcilePlan is the full set of differences between what the fleet wants and
// what this box has. Computing it as a value, separately from performing it, is
// what makes the interesting half testable without installing anything: every
// convergence property below is a property of PlanReconcile, and the container
// proof only has to show that Reconcile performs the plan it is given.
type ReconcilePlan struct {
	// Actions are the installs and removals, ordered by app id for determinism.
	Actions []ReconcileAction
}

// ReconcileResult records what happened, per app.
type ReconcileResult struct {
	// Installed are apps newly realised on this box. A re-realisation appears
	// here TOO — the app really was installed — and additionally in ReRealised.
	Installed []string
	// ReRealised are the apps in Installed that this box had already realised
	// before and had lost, i.e. the ones that cost a download to get back
	// something the box already had. On a box with volatile storage this is the
	// number that matters: it is the per-pass size of the waste.
	ReRealised []string
	// ReRealiseReason is the storage-durability fact behind ReRealised, measured
	// by the Realiser, or "" when it could not be measured. One string rather
	// than one per app because it is a property of the BOX.
	ReRealiseReason string
	// Deferred maps appID → why a re-realisation was not performed on this pass.
	// These are not failures and not skips: the action is still in the plan with
	// a NotBefore, and the app is still desired.
	Deferred map[string]string
	// Removed are apps unrealised from this box.
	Removed []string
	// Failed maps appID → the reason this box could not realise it. These are
	// the entries that become realise_state='failed' rows and replicate, so the
	// user sees "your arm64 box cannot install this" at whichever box they are
	// standing at rather than an app that is simply absent.
	Failed map[string]string
}

// PlanReconcile computes what this box must do to match the fleet desired set.
//
// The comparison is between the DESIRED set (including its tombstones) and
// GROUND TRUTH from the Realiser — not the realisation table. Three cases:
//
//	desired=1, not on disk   → install
//	desired=0, on disk       → remove   ← this is the case tombstones exist for
//	desired=1, on disk       → nothing
//
// The second case is the one a "desired set" without tombstones cannot express
// at all: a removed app would simply be absent from the desired set, which is
// indistinguishable from an app that was never wanted, so a box that already had
// it would keep it forever while every other box showed it gone.
//
// An app on disk that the desired set has never heard of is left ALONE. It is
// not "undesired" — it is un-adopted, most likely a pre-existing install on a
// box that predates the desired set, and deleting a user's apps because a table
// is new would be the worst possible reading of the directive. Adoption is a
// separate, explicit act (DesireInstall), not an inference.
//
// A version difference is likewise NOT an action here. Upgrades need their own
// ordering and rollback story and pretending a reconcile loop can do them by
// reinstalling would make every version skew a download storm.
// SYNC-APPS-02 addendum — "not on disk" is two different facts.
//
// The three cases above are complete only if the box's storage survives a
// reboot. On the three overlay boot paths it does not: the app directory is in
// a tmpfs upper layer, so after a reboot the disk scan reads empty for apps the
// box really did install. The desired set is intact (it replicates back from a
// peer), so case one fires for every app, on every boot, forever — gigabytes
// re-downloaded per boot, presenting as a slow boot rather than as a defect.
//
// The information needed to tell the two apart was already present and unread.
// The same sync that brings the desire back brings back this box's OWN
// app_registry rows, which say installed=1 / realise_state='realised'. So case
// one splits:
//
//	no row, or a row that never said realised   → CauseNeverRealised, install
//	this box's row says it realised it          → CauseReRealise
//
// A re-realisation is not a failure and not a new install. Nothing went wrong:
// the install worked and the storage evaporated. So the desire is untouched, the
// row stays 'realised', and what changes is that the plan says WHICH of the two
// it is, carries the count of how often it has happened, and carries the box's
// own measurement of WHY (Realiser's optional StorageVolatility — a fact read
// out of the kernel's mount table, never inferred from the boot mode).
//
// A re-realisation may also be DEFERRED — see reRealiseBackoff for exactly when,
// and for why a deferral can never be the reason a user does not get an app.
func (as *AppSync) PlanReconcile(r Realiser) (ReconcilePlan, error) {
	selfULID, _, _ := as.identity()
	return as.PlanReconcileFor(selfULID, r)
}

// PlanReconcileFor is PlanReconcile for a named instance: instanceULID is the
// box whose realisation rows are consulted to tell a first install from a
// re-realisation. PlanReconcile passes the signing identity, which is this box's
// stable ULID; the parameter exists because the reconciler's caller
// (cmd/server) already holds cfg.InstanceID and because a test must be able to
// plan for a box other than the one it is running as.
//
// An empty instanceULID means "no realisation rows to consult". That degrades to
// exactly the pre-SYNC-APPS-02 behaviour — every absence is a first install —
// rather than to an error, because a box that has not yet established its
// identity must still be able to install what the user asked for.
func (as *AppSync) PlanReconcileFor(instanceULID string, r Realiser) (ReconcilePlan, error) {
	if r == nil {
		return ReconcilePlan{}, fmt.Errorf("appsync: PlanReconcile: nil Realiser")
	}
	onDisk, err := r.RealisedVersions()
	if err != nil {
		return ReconcilePlan{}, fmt.Errorf("appsync: PlanReconcile: read realised versions: %w", err)
	}
	desired, err := as.DesiredSet()
	if err != nil {
		return ReconcilePlan{}, fmt.Errorf("appsync: PlanReconcile: read desired set: %w", err)
	}

	// This box's own realisation rows — the fleet's memory of what this box
	// managed, which on a volatile box outlives the box's own copy of it.
	realised := map[string]AppRegistryEntry{}
	if instanceULID != "" {
		rows, rerr := as.ListAppsForInstance(instanceULID, true)
		if rerr != nil {
			return ReconcilePlan{}, fmt.Errorf("appsync: PlanReconcile: read this box's realisation rows: %w", rerr)
		}
		for _, row := range rows {
			realised[row.AppID] = row
		}
	}

	// Measured, not inferred, and only when the Realiser can measure it.
	var volatileReason string
	if dr, ok := r.(DurabilityReporter); ok {
		if volatile, detail := dr.StorageVolatility(); volatile {
			volatileReason = detail
		}
	}

	now := time.Now().UTC()
	var plan ReconcilePlan
	for _, d := range desired {
		_, have := onDisk[d.AppID]
		switch {
		case d.Desired && !have:
			act := ReconcileAction{AppID: d.AppID, Version: d.Version, Install: true, Cause: CauseNeverRealised}
			// "This box realised it" is the row's own claim about itself:
			// installed, and the realisation state it reached was 'realised'. A
			// row left by a FAILED attempt says installed=0/'failed' and is not a
			// prior realisation — that app has never been on this disk, and
			// treating a failure as a lost install would hide a box that cannot
			// run something behind a reason about its storage.
			if prior, ok := realised[d.AppID]; ok && prior.Installed && prior.RealiseState == RealiseRealised {
				act.Cause = CauseReRealise
				act.ReRealiseCount = prior.ReRealiseCount
				act.Reason = volatileReason
				if until, wait := reRealiseDeferredUntil(prior, now); wait {
					act.Deferred = true
					act.NotBefore = until
				}
			}
			plan.Actions = append(plan.Actions, act)
		case !d.Desired && have:
			plan.Actions = append(plan.Actions, ReconcileAction{AppID: d.AppID, Install: false, Cause: CauseUndesired})
		}
	}
	sort.Slice(plan.Actions, func(i, j int) bool { return plan.Actions[i].AppID < plan.Actions[j].AppID })
	return plan, nil
}

// Re-realisation backoff bounds. Independent of any test's expectations: a test
// that derived its boundary from these would prove only that the code agrees
// with itself.
const (
	// reRealiseBackoffBase is the window after the FIRST re-realisation.
	reRealiseBackoffBase = time.Minute
	// reRealiseBackoffCap bounds the window. It is deliberately short — see
	// reRealiseDeferredUntil for why a long one would be a product defect.
	reRealiseBackoffCap = 30 * time.Minute
)

// reRealiseDeferredUntil decides whether a re-realisation must wait, and until
// when. It returns false for an app this box has never re-realised.
//
// The rule is a window since the LAST re-realisation, doubling with the count
// and capped, and NOT a limit on the count itself. That distinction is the whole
// design, so it is worth stating what each choice does to a real box.
//
// A count limit ("stop re-realising after N") would mean a live-USB user who
// reboots for the fifth time does not get their browser back. There is no
// durable storage on that path and nothing to fix; re-downloading IS the correct
// behaviour there, and the brief for this work says so plainly — an app the user
// asked for must still end up runnable. A window since the last event does not
// do that: any two boots more than half an hour apart are BOTH served in full,
// however high the count has climbed, because the window has always elapsed.
//
// What the window does catch is repetition fast enough to be pathological rather
// than merely unlucky: a box in a reboot loop, or the 2-minute reconcile ticker
// in cmd/server re-downloading gigabytes because an install lands somewhere that
// reads back as empty. That is the "forever" in "silently repeated forever", and
// it is the only case where refusing the download costs the user nothing.
//
// The count is never the reason to defer, only the size of the delay, and the
// delay is capped at half an hour so it can never grow into a count limit by
// another name.
func reRealiseDeferredUntil(prior AppRegistryEntry, now time.Time) (time.Time, bool) {
	if prior.ReRealiseCount <= 0 || prior.ReRealisedAt.IsZero() {
		// Never re-realised (or a row from a peer that predates the counter):
		// the first one is always immediate.
		return time.Time{}, false
	}
	window := reRealiseBackoffBase
	for i := 1; i < prior.ReRealiseCount && window < reRealiseBackoffCap; i++ {
		window *= 2
	}
	if window > reRealiseBackoffCap {
		window = reRealiseBackoffCap
	}
	until := prior.ReRealisedAt.Add(window)
	if now.Before(until) {
		return until, true
	}
	return time.Time{}, false
}

// ReRealisation is one box's re-realisation history, as any box in the fleet can
// read it.
type ReRealisation struct {
	// InstanceULID is the box that had to install apps again.
	InstanceULID string `json:"instance_ulid"`
	// Apps is how many distinct apps that box has re-realised at least once.
	Apps int `json:"apps"`
	// Total is the sum of the per-app counts: how many downloads of something
	// the box already had.
	Total int `json:"total"`
	// Reason is the storage-durability fact that box measured about itself, or
	// "" if it could not measure one. Taken from the most recent re-realisation.
	Reason string `json:"reason,omitempty"`
	// LastAt is when that box last re-realised anything.
	LastAt time.Time `json:"last_at"`
}

// ReRealisations reports, per instance, how much of the fleet's downloading is
// re-downloading — apps a box already had and lost.
//
// It exists because a re-realisation is otherwise invisible in exactly the way
// the original defect was: the box installs the app, the app works, the user
// sees a slow boot and nothing else. It reads the replicated rows, so the answer
// for a volatile box is available AT ANOTHER BOX — which matters, because the
// volatile box is the one whose local database does not survive to be asked.
//
// ONE statement, including the reason. The reason belongs to the most recent
// event rather than to the group, which wants a second lookup per instance — and
// a second lookup issued WHILE the outer rows are open deadlocks this process
// outright. The registry opens SQLite with SetMaxOpenConns(1); the open cursor
// holds that connection, the inner query waits for a connection that only the
// cursor can release, and the pool waits forever with no error and no timeout.
// It is a correlated subquery for that reason, not for elegance. (Found by the
// test below hanging for 240s, not by review.)
func (as *AppSync) ReRealisations() ([]ReRealisation, error) {
	rows, err := as.db.Query(`
		SELECT r.instance_ulid, COUNT(*), SUM(r.rerealise_count), MAX(r.rerealise_at),
		       (SELECT x.rerealise_reason FROM app_registry x
		         WHERE x.instance_ulid = r.instance_ulid AND x.rerealise_count > 0
		         ORDER BY x.rerealise_at DESC LIMIT 1)
		FROM app_registry r
		WHERE r.rerealise_count > 0
		GROUP BY r.instance_ulid
		ORDER BY r.instance_ulid`)
	if err != nil {
		return nil, fmt.Errorf("appsync: ReRealisations: %w", err)
	}
	defer rows.Close()
	var out []ReRealisation
	for rows.Next() {
		var (
			r         ReRealisation
			lastAtRaw sql.NullString
			reason    sql.NullString
		)
		if err := rows.Scan(&r.InstanceULID, &r.Apps, &r.Total, &lastAtRaw, &reason); err != nil {
			return nil, fmt.Errorf("appsync: ReRealisations: scan: %w", err)
		}
		if lastAtRaw.Valid && lastAtRaw.String != "" {
			r.LastAt, _ = time.Parse(time.RFC3339Nano, lastAtRaw.String)
		}
		r.Reason = reason.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// deferralReason renders why a re-realisation is waiting, in the words a user
// standing at another box needs: what it is, how often it has happened, when it
// will be tried again, and — when the box could measure it — the storage fact
// behind it.
func deferralReason(a ReconcileAction) string {
	s := fmt.Sprintf("re-realised %d time(s) already; next attempt not before %s",
		a.ReRealiseCount, a.NotBefore.UTC().Format(time.RFC3339))
	if a.Reason != "" {
		s += "; this box's app storage is volatile: " + a.Reason
	}
	return s
}

// reportFailureIfChanged records a realisation failure ONLY when it differs from
// what this instance's row already says.
//
// Without this, a permanent failure churns forever. Reconcile runs on a timer;
// an arm64 box that cannot install an amd64-only app fails on every pass, and
// ReportRealiseFailure stamps UpdatedAt = now unconditionally — so the row
// crosses the LWW cursor every couple of minutes and is pushed to every peer,
// for as long as the app stays desired. Nothing would be WRONG, which is why it
// would not have been noticed: the state converges correctly, the reason is
// right, and the fleet quietly gossips an unchanging fact forever.
//
// A changed reason still writes. That matters more than it looks: "requires
// amd64; this box is arm64" becoming "download timed out" is the difference
// between a box that can never have the app and one that might on the next try,
// and suppressing it to save a write would hide the only signal that says which.
func (as *AppSync) reportFailureIfChanged(instanceULID, appID, version, reason string) error {
	var (
		state  string
		detail string
	)
	err := as.db.QueryRow(`
		SELECT realise_state, realise_detail FROM app_registry
		WHERE instance_ulid = ? AND app_id = ?
	`, instanceULID, appID).Scan(&state, &detail)
	if err == nil && state == RealiseFailed && detail == reason {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("appsync: reportFailureIfChanged: read current row: %w", err)
	}
	return as.ReportRealiseFailure(instanceULID, appID, version, reason)
}

// Reconcile computes the plan and performs it, reporting each outcome into this
// instance's realisation rows so peers learn what this box managed.
//
// Every outcome is reported, including the failures — that is the point. A box
// that cannot install a desired app writes realise_state='failed' with the
// installer's own reason and the row replicates, so the fleet can say WHY an app
// is missing from one instance instead of the user discovering it is.
//
// The desired set is not touched, on any path, including the failure path. A box
// that cannot install something has learned nothing about what the user wants.
//
// Reconcile does not loop or back off; the caller decides cadence. A failure is
// retried on the next call, which is correct for a transient one (a download that
// timed out) and harmless for a permanent one (an arch mismatch is refused before
// anything is downloaded — services/appnet/registry.go checks ArchSupported
// before it touches the filesystem).
func (as *AppSync) Reconcile(ctx context.Context, instanceULID string, r Realiser) (ReconcileResult, error) {
	res := ReconcileResult{Failed: map[string]string{}, Deferred: map[string]string{}}
	if instanceULID == "" {
		return res, fmt.Errorf("appsync: Reconcile: instanceULID must not be empty")
	}
	plan, err := as.PlanReconcileFor(instanceULID, r)
	if err != nil {
		return res, err
	}
	for _, a := range plan.Actions {
		if a.Install {
			// SYNC-APPS-02. A deferred re-realisation is the one action that is
			// in the plan and not performed. Nothing is written to the row: the
			// row already carries the count, the time and the reason from the
			// last one, and re-stamping it every pass would push an unchanging
			// fact across the LWW cursor to every peer every two minutes — the
			// same churn reportFailureIfChanged exists to prevent.
			if a.Deferred {
				res.Deferred[a.AppID] = deferralReason(a)
				continue
			}
			if rerr := r.Realise(ctx, a.AppID, a.Version); rerr != nil {
				res.Failed[a.AppID] = rerr.Error()
				if perr := as.reportFailureIfChanged(instanceULID, a.AppID, a.Version, rerr.Error()); perr != nil {
					return res, fmt.Errorf("appsync: Reconcile: report failure for %s: %w", a.AppID, perr)
				}
				continue
			}
			if a.Cause == CauseReRealise {
				// Recorded as a re-realisation, not as an install: the count is
				// the only thing that distinguishes a box quietly re-downloading
				// its whole app set every boot from a box installing software.
				if perr := as.LocalReRealise(instanceULID, a.AppID, a.Version, a.ReRealiseCount+1, a.Reason); perr != nil {
					return res, fmt.Errorf("appsync: Reconcile: report re-realisation of %s: %w", a.AppID, perr)
				}
				res.ReRealised = append(res.ReRealised, a.AppID)
				if a.Reason != "" {
					res.ReRealiseReason = a.Reason
				}
				res.Installed = append(res.Installed, a.AppID)
				continue
			}
			if perr := as.LocalInstall(instanceULID, a.AppID, a.Version); perr != nil {
				return res, fmt.Errorf("appsync: Reconcile: report install of %s: %w", a.AppID, perr)
			}
			res.Installed = append(res.Installed, a.AppID)
			continue
		}
		if rerr := r.Unrealise(ctx, a.AppID); rerr != nil {
			res.Failed[a.AppID] = rerr.Error()
			if perr := as.reportFailureIfChanged(instanceULID, a.AppID, "", rerr.Error()); perr != nil {
				return res, fmt.Errorf("appsync: Reconcile: report removal failure for %s: %w", a.AppID, perr)
			}
			continue
		}
		if perr := as.LocalUninstall(instanceULID, a.AppID); perr != nil {
			return res, fmt.Errorf("appsync: Reconcile: report removal of %s: %w", a.AppID, perr)
		}
		res.Removed = append(res.Removed, a.AppID)
	}
	return res, nil
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

	// SYNC-APPS-02: the operator's view of re-downloading.
	//
	// It is served from the REPLICATED rows, so asking any box gives the answer
	// for every box — which is the only way to get it for the box that needs it
	// most. A box on volatile storage loses its own database on the reboot that
	// causes the re-realisation; it can be asked about itself only while it is
	// up, and what it says then is whatever it recovered from its peers anyway.
	mux.HandleFunc("GET /api/apps/rerealisations", func(w http.ResponseWriter, r *http.Request) {
		report, err := as.ReRealisations()
		if err != nil {
			log.Printf("[appsync] ReRealisations: %v", err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if report == nil {
			report = []ReRealisation{}
		}
		w.Header().Set("Content-Type", "application/json")
		if encErr := json.NewEncoder(w).Encode(report); encErr != nil {
			return
		}
	})
}
