// Package multiinstance — FABRIC-KEY-01: fabric signing-key rotation +
// revocation (the roster-facing half; key-at-rest sealing lives in
// instancekey.go).
//
// Rotation model (overlap window)
//
//	A box rotates its per-instance Ed25519 signing key by:
//	  1. Generating a fresh keypair.
//	  2. Moving its CURRENT public key into the roster's prev_ed25519_public_key
//	     slot with an expiry = now + overlap, then setting the NEW public key as
//	     the current ed25519_public_key.
//	  3. Switching its in-process signer to the new private key.
//	During the overlap, peers ACCEPT signatures from EITHER the new key OR the old
//	key (see verifyChangesetSignature), so an observation signed just before the
//	rotation — still propagating across the fabric — keeps counting toward quorum
//	until peers have re-learned the new key. After the overlap the old key is
//	rejected.
//
// Revocation
//
//	A compromised peer is marked Revoked in the roster. A revoked instance's
//	signed observations NEVER count toward quorum (verifyChangesetSignature fails
//	closed for a revoked origin), and it is refused at the CRDT sync door
//	(cmd/server/crdtsync_wiring.go's fabricPeerRoster), so a revoked box stops
//	being able to write to its peers immediately.
//
//	# Revocation is MONOTONIC. There is no un-revoke.
//
//	This used not to be true, and the un-revoke paths were the reason eviction
//	did not work:
//
//	  - RestoreFromRevocation cleared the flag outright. It existed "for
//	    completeness / tests" and had no production caller. It is DELETED, not
//	    gated: a function whose only effect is to undo a security decision is a
//	    liability in a codebase where the roster is written by a background cloud
//	    poll, and "for tests" is not a reason to keep a live un-revoke.
//	  - RotateIdentity cleared Revoked on SELF on every rotation. A compromised
//	    box runs this code: rotating its own key was a one-call self-pardon. It
//	    now REFUSES to rotate while self is revoked.
//	  - Registry.Upsert wrote `revoked = excluded.revoked` unconditionally, so
//	    ANY writer that did not carry the flag cleared it — including
//	    CloudSyncer, whose wire type has no revoked field at all, on every poll.
//	    Upsert now latches the bit in SQL (see registry.go), the same way it
//	    already protects an owner row's store_only.
//
//	What monotonicity costs: a box that was revoked in error cannot be readmitted
//	under the same ULID. That is the correct trade — readmission is enrolment,
//	and enrolment is an operator act with a fresh identity, not a flag flip that
//	a concurrent writer can also perform.
package multiinstance

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"
)

// DefaultRotationOverlap is the grace window during which a rotated-away
// (previous) signing key is still accepted, giving the fabric time to converge
// on the new key before the old one is rejected.
const DefaultRotationOverlap = 24 * time.Hour

// RotateIdentity rotates THIS box's signing key to newPriv with an overlap
// window. The box's current public key is preserved in the roster as the
// previous key (accepted until now+overlap), the new public key becomes current,
// and the in-process signer switches to newPriv so newly-emitted changesets are
// signed with the new key.
//
//   - newPriv must be a valid Ed25519 private key. Pass GenerateRotationKey to
//     mint one, or supply your own (e.g. derived from recovery).
//   - overlap <= 0 uses DefaultRotationOverlap.
//
// The caller is responsible for PERSISTING newPriv (sealed, via
// LoadOrCreateSealedInstanceKey's writeSealedSeed path) so the rotation survives
// a restart. A box must currently have an identity set (selfULID known) to
// rotate; rotation without a prior identity is just SetIdentity.
func (as *AppSync) RotateIdentity(newPriv ed25519.PrivateKey, overlap time.Duration) error {
	if len(newPriv) != ed25519.PrivateKeySize {
		return fmt.Errorf("appsync: RotateIdentity: new private key length %d, want %d", len(newPriv), ed25519.PrivateKeySize)
	}
	if overlap <= 0 {
		overlap = DefaultRotationOverlap
	}

	selfULID, _, oldPubB64 := as.identity()
	if selfULID == "" {
		return fmt.Errorf("appsync: RotateIdentity: no current identity — call SetIdentity first")
	}
	if as.reg == nil {
		return fmt.Errorf("appsync: RotateIdentity: no registry to publish rotation into")
	}

	newPub, ok := newPriv.Public().(ed25519.PublicKey)
	if !ok {
		return fmt.Errorf("appsync: RotateIdentity: new private key has no Ed25519 public key")
	}
	newPubB64 := base64.StdEncoding.EncodeToString(newPub)
	if newPubB64 == oldPubB64 {
		return fmt.Errorf("appsync: RotateIdentity: new key equals current key (not a rotation)")
	}

	// Publish the rotation into the roster: current → prev (with expiry), new →
	// current. Preserve all other instance fields.
	inst, found := as.reg.Get(selfULID)
	if !found {
		inst = Instance{ULID: selfULID, Kind: KindDevice, Role: RoleOwner, Status: StatusOnline}
	}
	// A revoked box does not get to re-admit itself. This line used to be
	// `inst.Revoked = false`, justified as clearing a "stale" flag — but the
	// code that runs it is the code on the box that was revoked, and the only
	// party with a motive to run it is the box that should not be trusted. A
	// rotation is not a pardon. Readmission is enrolment under a fresh identity,
	// performed by the operator, not a side effect of a key roll.
	if inst.Revoked {
		return fmt.Errorf("appsync: RotateIdentity: instance %s is REVOKED — rotating a key does not lift a revocation, and a revoked instance may not re-admit itself; enrol a fresh identity instead", selfULID)
	}
	if oldPubB64 != "" {
		inst.PrevEd25519PublicKey = oldPubB64
		inst.PrevKeyExpiresAt = time.Now().UTC().Add(overlap)
	}
	inst.Ed25519PublicKey = newPubB64
	if err := as.reg.Upsert(inst); err != nil {
		return fmt.Errorf("appsync: RotateIdentity: publish rotated key: %w", err)
	}

	// Switch the in-process signer to the new key. We set the fields directly
	// (rather than SetIdentity) to avoid SetIdentity overwriting the prev-key
	// overlap we just published.
	as.idMu.Lock()
	as.signPriv = newPriv
	as.signPubB64 = newPubB64
	as.idMu.Unlock()
	return nil
}

// GenerateRotationKey mints a fresh Ed25519 private key suitable for
// RotateIdentity. The caller must persist it (sealed) and pass it to
// RotateIdentity.
func GenerateRotationKey() (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("appsync: GenerateRotationKey: %w", err)
	}
	return priv, nil
}

// RevokePeer marks the rostered peer ulid as compromised. It is ONE-WAY: there
// is no paired un-revoke (see the package doc), and Registry.Upsert latches the
// bit so a later writer that does not carry it cannot clear it either.
//
// Its effect is immediate and covers both of the peer's keys:
//
//   - signed observations from that origin stop counting toward quorum
//     (verifyChangesetSignature fails closed for a revoked origin), and
//   - the peer is refused at the CRDT sync door, before any op is merged
//     (cmd/server/crdtsync_wiring.go's fabricPeerRoster checks revocation
//     before any allow arm, re-reading the roster on every request).
//
// What it does NOT do — and this is the honest boundary of "eviction": it does
// not un-read anything the peer already pulled, and it does not re-key the
// fleet, so it makes future WRITES impossible, not past reads unhappen.
//
// Returns an error if the peer is not in the roster.
func (as *AppSync) RevokePeer(ulid string) error {
	if ulid == "" {
		return fmt.Errorf("appsync: RevokePeer: ulid must not be empty")
	}
	if as.reg == nil {
		return fmt.Errorf("appsync: RevokePeer: no registry")
	}
	inst, ok := as.reg.Get(ulid)
	if !ok {
		return fmt.Errorf("appsync: RevokePeer: unknown instance %q", ulid)
	}
	if inst.Revoked {
		return nil // already revoked — idempotent
	}
	inst.Revoked = true
	if err := as.reg.Upsert(inst); err != nil {
		return fmt.Errorf("appsync: RevokePeer: persist revocation: %w", err)
	}
	return nil
}

// There is deliberately no RestoreFromRevocation. It existed here, cleared the
// flag, and had no production caller — an un-revoke API kept "for completeness"
// in a package whose roster is also written by a background cloud poll. See the
// package doc: revocation is monotonic, and readmission is enrolment.
