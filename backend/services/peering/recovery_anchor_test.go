package peering

import (
	"crypto/rand"
	"testing"
)

// TestRecoveryAnchor_LiveSeam proves recovery is ACTIVE end-to-end: the box
// derives + persists a real anchor public id from a recovery seed, installs it on
// the lifecycle store, and a peer that pinned that anchor follows a recovery cert
// onto the new identity key after the old key is "lost".
func TestRecoveryAnchor_LiveSeam(t *testing.T) {
	dir := t.TempDir()

	// Account recovery seed (64 bytes in production; ≥16 accepted here).
	seed := make([]byte, 64)
	if _, err := rand.Read(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The box's own identity (root) and lifecycle store, seeded WITHOUT an anchor.
	_, rootID := pkIdentity(t)
	store, err := NewLifecycleStore(dir, rootID, "")
	if err != nil {
		t.Fatalf("NewLifecycleStore: %v", err)
	}
	if store.AnchorVulosID() != "" {
		t.Fatal("anchor should start empty (recovery not yet enabled)")
	}

	// Seam: derive + persist the PUBLIC anchor and install it live.
	anchorID, err := AnchorFromRecoverySeed(store, dir, seed)
	if err != nil {
		t.Fatalf("AnchorFromRecoverySeed: %v", err)
	}
	if anchorID == "" {
		t.Fatal("anchor id must be non-empty after install (recovery ACTIVE)")
	}
	if store.AnchorVulosID() != anchorID {
		t.Fatalf("store anchor = %q, want %q", store.AnchorVulosID(), anchorID)
	}

	// Persisted to disk and reloadable on next boot.
	if got := LoadRecoveryAnchorID(dir); got != anchorID {
		t.Fatalf("LoadRecoveryAnchorID = %q, want %q", got, anchorID)
	}

	// A fresh store opened on the same dir must pick the anchor back up via the
	// reloaded id (mirrors the boot path: LoadRecoveryAnchorID → NewLifecycleStore).
	reloaded, err := NewLifecycleStore(dir, rootID, LoadRecoveryAnchorID(dir))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if reloaded.AnchorVulosID() != anchorID {
		t.Fatalf("reloaded anchor = %q, want %q", reloaded.AnchorVulosID(), anchorID)
	}

	// ── Peer side: follow recovery after key loss ───────────────────────────
	// Re-derive the anchor PRIVATE key from the seed (this is what the user does
	// off-box from their recovery kit) and mint a recovery cert onto a new key.
	anchorPriv, anchorVulosID, err := DeriveRecoveryAnchor(seed)
	if err != nil {
		t.Fatalf("DeriveRecoveryAnchor: %v", err)
	}
	if anchorVulosID != anchorID {
		t.Fatalf("anchor id mismatch: derived %q vs persisted %q", anchorVulosID, anchorID)
	}

	_, newID := pkIdentity(t) // the recovered identity's brand-new key
	rec, err := NewRecoveryCert(anchorPriv, rootID, newID, anchorVulosID)
	if err != nil {
		t.Fatalf("NewRecoveryCert: %v", err)
	}

	// A peer who pinned the anchor (TOFU) follows the recovery to newID.
	peer, err := NewLifecycleStore(t.TempDir(), rootID, "")
	if err != nil {
		t.Fatalf("peer store: %v", err)
	}
	if err := peer.PinAnchor(rootID, anchorVulosID); err != nil {
		t.Fatalf("PinAnchor: %v", err)
	}
	chain := []LifecycleLink{{Recovery: rec}}
	head, err := ResolveCurrentKey(rootID, peer.TrustedAnchorFor(rootID), chain)
	if err != nil {
		t.Fatalf("ResolveCurrentKey (pinned peer): %v", err)
	}
	if head != newID {
		t.Fatalf("recovered head = %q, want %q", head, newID)
	}

	// A peer that did NOT pin the anchor must NOT be able to follow the recovery
	// (fail closed — the whole point of the anchor being account-bound).
	if _, err := ResolveCurrentKey(rootID, "", chain); err == nil {
		t.Fatal("un-pinned peer must reject a recovery it cannot verify")
	}
}

// TestRecoveryAnchor_TakeoverRejected ensures a persisted/installed anchor cannot
// be silently swapped (a box-compromise takeover vector).
func TestRecoveryAnchor_TakeoverRejected(t *testing.T) {
	dir := t.TempDir()
	_, rootID := pkIdentity(t)
	store, err := NewLifecycleStore(dir, rootID, "")
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	seed1 := make([]byte, 64)
	rand.Read(seed1)
	if _, err := AnchorFromRecoverySeed(store, dir, seed1); err != nil {
		t.Fatalf("first install: %v", err)
	}

	// A different seed → different anchor → must be refused at both layers.
	seed2 := make([]byte, 64)
	rand.Read(seed2)
	if _, err := PersistRecoveryAnchor(dir, seed2); err == nil {
		t.Fatal("persisting a different anchor must fail closed")
	}
	_, anchor2, _ := DeriveRecoveryAnchor(seed2)
	if err := store.SetAnchor(anchor2); err == nil {
		t.Fatal("SetAnchor to a different key must fail closed")
	}

	// Re-installing the SAME anchor is idempotent (no error).
	if _, err := AnchorFromRecoverySeed(store, dir, seed1); err != nil {
		t.Fatalf("idempotent re-install: %v", err)
	}
}
