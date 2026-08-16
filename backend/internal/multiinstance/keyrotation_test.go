package multiinstance_test

// FABRIC-KEY-01 tests: fabric signing-key
//   (a) encryption at rest (KeyringSealer / LoadOrCreateSealedInstanceKey),
//   (b) rotation with an overlap window (old key accepted during grace), and
//   (c) revocation (a revoked key's observations stop counting toward quorum).
//
// The quorum assertions use a 2-instance roster: threshold is 1 distinct
// VERIFIED origin (uninstallQuorumThreshold(<=2) == 1), so a single signed
// uninstall that verifies will apply, and one that does NOT verify will be
// suppressed (the row stays installed). That makes "counts vs does-not-count"
// a clean, deterministic assertion.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/multiinstance"
)

// ── (a) Key-at-rest encryption ────────────────────────────────────────────────

func TestKeyringSealerRoundTrip(t *testing.T) {
	rootKey := bytes.Repeat([]byte{0x42}, 32)
	sealer, err := multiinstance.NewKeyringSealer(rootKey)
	if err != nil {
		t.Fatalf("NewKeyringSealer: %v", err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	seed := priv.Seed()

	blob, err := sealer.Seal(seed)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// The sealed blob must NOT contain the plaintext seed.
	if bytes.Contains(blob, seed) {
		t.Fatal("sealed blob contains the plaintext seed — not encrypted at rest")
	}
	got, err := sealer.Open(blob)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, seed) {
		t.Fatal("round-tripped seed differs from original")
	}

	// A different root key must NOT be able to open the envelope (AEAD auth fail).
	other, _ := multiinstance.NewKeyringSealer(bytes.Repeat([]byte{0x99}, 32))
	if _, err := other.Open(blob); err == nil {
		t.Fatal("Open with a wrong root key must fail")
	}
}

func TestLoadOrCreateSealedInstanceKey_EncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fabric_instance_key")
	sealer, _ := multiinstance.NewKeyringSealer(bytes.Repeat([]byte{0x07}, 32))

	// First call generates + persists a sealed key.
	priv1, err := multiinstance.LoadOrCreateSealedInstanceKey(path, sealer)
	if err != nil {
		t.Fatalf("first LoadOrCreateSealedInstanceKey: %v", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	// The on-disk file must be the sealed envelope (encrypted at rest), NOT the
	// bare base64 seed the legacy path wrote.
	if !strings.Contains(string(onDisk), "vulos-fabric-sealed-v1") {
		t.Fatalf("on-disk key is not a sealed envelope: %q", string(onDisk))
	}
	if bytes.Contains(onDisk, priv1.Seed()) {
		t.Fatal("on-disk key file contains plaintext seed")
	}
	if bytes.Contains(onDisk, []byte(base64.StdEncoding.EncodeToString(priv1.Seed()))) {
		t.Fatal("on-disk key file contains base64 plaintext seed")
	}

	// Second call must return the SAME key (stable identity across restarts).
	priv2, err := multiinstance.LoadOrCreateSealedInstanceKey(path, sealer)
	if err != nil {
		t.Fatalf("second LoadOrCreateSealedInstanceKey: %v", err)
	}
	if !priv1.Equal(priv2) {
		t.Fatal("reloaded sealed key differs — identity not stable")
	}
}

func TestLoadOrCreateSealedInstanceKey_MigratesLegacyPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fabric_instance_key")

	// Write a LEGACY plaintext (bare base64 seed) file, as the old
	// LoadOrCreateInstanceKey would have.
	legacy, err := multiinstance.LoadOrCreateInstanceKey(path)
	if err != nil {
		t.Fatalf("legacy LoadOrCreateInstanceKey: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "vulos-fabric-sealed-v1") {
		t.Fatal("precondition: legacy file should be plaintext, not sealed")
	}

	// Sealed loader migrates it in place to an encrypted envelope, preserving the
	// identity.
	sealer, _ := multiinstance.NewKeyringSealer(bytes.Repeat([]byte{0x11}, 32))
	migrated, err := multiinstance.LoadOrCreateSealedInstanceKey(path, sealer)
	if err != nil {
		t.Fatalf("sealed load (migration): %v", err)
	}
	if !legacy.Equal(migrated) {
		t.Fatal("migration changed the signing identity")
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "vulos-fabric-sealed-v1") {
		t.Fatal("legacy file was not migrated to a sealed envelope")
	}
}

func TestSealedKeyFromEnv_FailClosedInProd(t *testing.T) {
	t.Setenv("VULOS_ENV", "prod")
	t.Setenv("VULOS_FABRIC_KEY_HEX", "")
	if _, err := multiinstance.SealedKeyFromEnv(); err == nil {
		t.Fatal("SealedKeyFromEnv must fail closed in prod when the key env is unset")
	}

	// A valid 64-hex key is accepted.
	t.Setenv("VULOS_FABRIC_KEY_HEX", strings.Repeat("ab", 32))
	if _, err := multiinstance.SealedKeyFromEnv(); err != nil {
		t.Fatalf("SealedKeyFromEnv with a valid key: %v", err)
	}
}

// ── helpers: a 2-instance roster where one signed origin meets quorum ─────────

// install records an installed app for victimULID into as so a later uninstall
// has something to remove.
func install(t *testing.T, as *multiinstance.AppSync, victimULID, appID string, at time.Time) {
	t.Helper()
	cs, err := as.EmitChangeset(victimULID, []multiinstance.AppRegistryEntry{
		{InstanceULID: victimULID, AppID: appID, AppVersion: "1.0.0", Installed: true, InstalledBy: "nodeA", UpdatedAt: at},
	})
	if err != nil {
		t.Fatalf("emit install: %v", err)
	}
	if err := as.ApplyChangeset(cs); err != nil {
		t.Fatalf("apply install: %v", err)
	}
}

func stillInstalled(t *testing.T, as *multiinstance.AppSync, victimULID string) bool {
	t.Helper()
	apps, err := as.ListAppsForInstance(victimULID, false)
	if err != nil {
		t.Fatalf("ListAppsForInstance: %v", err)
	}
	return len(apps) == 1 && apps[0].Installed
}

// ── (b) Rotation overlap: old key still accepted during the grace window ──────

func TestRotation_OldKeyAcceptedDuringOverlap(t *testing.T) {
	const (
		victim = "01HWZMINST000000000VICTIM01"
		origin = "01HWZMINST000000000ORIGIN01"
		appID  = "browser"
	)
	reg, as := openTempAppSync(t)
	if err := reg.Upsert(multiinstance.Instance{ULID: victim, Kind: multiinstance.KindDevice, Status: multiinstance.StatusOnline}); err != nil {
		t.Fatalf("upsert victim: %v", err)
	}

	// The reporting origin has its own identity; publish its CURRENT key into the
	// verifier roster. Threshold for a 2-instance roster is 1 → one verified
	// signed uninstall applies.
	o := newSignedOrigin(t, origin)
	o.publishInto(t, reg)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	install(t, as, victim, appID, base)

	// The origin signs an uninstall under its CURRENT (old) key — capture it.
	oldKeyUninstall := o.emitUninstall(t, victim, appID, "1.0.0", base.Add(time.Minute))

	// Now the origin ROTATES to a new key, with the OLD key published into the
	// verifier's roster as prev with an OPEN overlap window.
	rotateOriginInVerifierRoster(t, reg, o, 24*time.Hour)

	// The previously-signed (old-key) uninstall must STILL verify and apply,
	// because we are inside the overlap window — in-flight observations don't break.
	if err := as.ApplyChangeset(oldKeyUninstall); err != nil {
		t.Fatalf("apply old-key uninstall during overlap: %v", err)
	}
	if stillInstalled(t, as, victim) {
		t.Fatal("old-key signed uninstall during overlap must still count toward quorum (and apply)")
	}
}

func TestRotation_OldKeyRejectedAfterOverlapExpires(t *testing.T) {
	const (
		victim = "01HWZMINST000000000VICTIM02"
		origin = "01HWZMINST000000000ORIGIN02"
		appID  = "notes"
	)
	reg, as := openTempAppSync(t)
	if err := reg.Upsert(multiinstance.Instance{ULID: victim, Kind: multiinstance.KindDevice, Status: multiinstance.StatusOnline}); err != nil {
		t.Fatalf("upsert victim: %v", err)
	}
	o := newSignedOrigin(t, origin)
	o.publishInto(t, reg)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	install(t, as, victim, appID, base)
	oldKeyUninstall := o.emitUninstall(t, victim, appID, "1.0.0", base.Add(time.Minute))

	// Rotate with a NEGATIVE overlap → the prev-key window is already expired.
	rotateOriginInVerifierRoster(t, reg, o, -time.Hour)

	// The old-key signature must NOT verify (overlap expired) → does not count →
	// the uninstall is suppressed (row stays installed).
	if err := as.ApplyChangeset(oldKeyUninstall); err != nil {
		t.Fatalf("apply old-key uninstall after overlap: %v", err)
	}
	if !stillInstalled(t, as, victim) {
		t.Fatal("old-key signed uninstall AFTER overlap must NOT count toward quorum")
	}
}

func TestRotation_NewKeyAcceptedAfterRotation(t *testing.T) {
	const (
		victim = "01HWZMINST000000000VICTIM03"
		origin = "01HWZMINST000000000ORIGIN03"
		appID  = "files"
	)
	reg, as := openTempAppSync(t)
	if err := reg.Upsert(multiinstance.Instance{ULID: victim, Kind: multiinstance.KindDevice, Status: multiinstance.StatusOnline}); err != nil {
		t.Fatalf("upsert victim: %v", err)
	}
	o := newSignedOrigin(t, origin)
	o.publishInto(t, reg)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	install(t, as, victim, appID, base)

	// Rotate the origin to a fresh key, publish the new key into the verifier
	// roster (prev kept with an open window).
	rotateOriginInVerifierRoster(t, reg, o, 24*time.Hour)

	// An uninstall signed under the NEW (post-rotation) key must verify + apply.
	newKeyUninstall := o.emitUninstall(t, victim, appID, "1.0.0", base.Add(2*time.Minute))
	if err := as.ApplyChangeset(newKeyUninstall); err != nil {
		t.Fatalf("apply new-key uninstall: %v", err)
	}
	if stillInstalled(t, as, victim) {
		t.Fatal("new-key signed uninstall must count toward quorum (and apply)")
	}
}

// ── (c) Revocation: a revoked key's observations stop counting ────────────────

func TestRevocation_RevokedKeyRejected(t *testing.T) {
	const (
		victim = "01HWZMINST000000000VICTIM04"
		origin = "01HWZMINST000000000ORIGIN04"
		appID  = "vault"
	)
	reg, as := openTempAppSync(t)
	if err := reg.Upsert(multiinstance.Instance{ULID: victim, Kind: multiinstance.KindDevice, Status: multiinstance.StatusOnline}); err != nil {
		t.Fatalf("upsert victim: %v", err)
	}
	o := newSignedOrigin(t, origin)
	o.publishInto(t, reg)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	install(t, as, victim, appID, base)

	// Mark the origin's key revoked.
	if err := as.RevokePeer(origin); err != nil {
		t.Fatalf("RevokePeer: %v", err)
	}

	// A correctly-signed uninstall from the revoked origin must NOT count →
	// the row stays installed (revocation fails closed).
	if err := as.ApplyChangeset(o.emitUninstall(t, victim, appID, "1.0.0", base.Add(time.Minute))); err != nil {
		t.Fatalf("apply revoked-origin uninstall: %v", err)
	}
	if !stillInstalled(t, as, victim) {
		t.Fatal("a REVOKED origin's signed uninstall must NOT count toward quorum")
	}

	// Revocation is MONOTONIC. This block used to call RestoreFromRevocation and
	// assert the origin counted again; that API is gone, and the property under
	// test is now the opposite one — nothing a later writer does clears the bit.
	//
	// The writer used here is the one that actually did it in production:
	// a plain Upsert carrying the instance WITHOUT the flag, which is exactly
	// the shape of CloudSyncer's per-poll write (CloudInstance has no revoked
	// field, so cloudInstanceToLocal always produces Revoked=false).
	unrevoke, ok := reg.Get(origin)
	if !ok {
		t.Fatal("origin vanished from the roster")
	}
	unrevoke.Revoked = false
	if err := reg.Upsert(unrevoke); err != nil {
		t.Fatalf("upsert without the revoked flag: %v", err)
	}
	if again, _ := reg.Get(origin); !again.Revoked {
		t.Fatal("a concurrent writer cleared a revocation: Upsert must LATCH revoked, " +
			"or a cloud-sync poll un-revokes every box the operator evicted")
	}
	if err := as.ApplyChangeset(o.emitUninstall(t, victim, appID, "1.0.0", base.Add(2*time.Minute))); err != nil {
		t.Fatalf("apply post-unrevoke-attempt uninstall: %v", err)
	}
	if !stillInstalled(t, as, victim) {
		t.Fatal("a revoked origin's signed uninstall counted again after an un-revoking write")
	}
}

// TestRotateIdentityRefusesWhileRevoked pins the self-pardon path shut.
//
// RotateIdentity used to set `inst.Revoked = false` on every rotation, with the
// comment "a fresh self-rotation clears any stale revoked flag on self". The
// code runs on the box being revoked, so the only party with a motive to run it
// is the one that must not be trusted: rotating was a one-call self-readmission.
func TestRotateIdentityRefusesWhileRevoked(t *testing.T) {
	const self = "01HWZMINST00000000000SELF04"
	reg, as := openTempAppSync(t)

	if _, err := as.GenerateAndSetIdentity(self); err != nil {
		t.Fatalf("set identity: %v", err)
	}
	if err := as.RevokePeer(self); err != nil {
		t.Fatalf("RevokePeer(self): %v", err)
	}

	newKey, err := multiinstance.GenerateRotationKey()
	if err != nil {
		t.Fatalf("GenerateRotationKey: %v", err)
	}
	if err := as.RotateIdentity(newKey, time.Hour); err == nil {
		t.Fatal("a REVOKED instance rotated its own key — that is a self-pardon: " +
			"the new key would be published into the roster with the revocation lifted")
	}
	inst, ok := reg.Get(self)
	if !ok {
		t.Fatal("self vanished from the roster")
	}
	if !inst.Revoked {
		t.Fatal("the refused rotation still cleared the revoked flag")
	}
}

// originPubKey returns the origin's CURRENT signing public key (base64) by
// emitting an empty changeset and reading SignerPubKey.
func originPubKey(t *testing.T, o *signedOrigin) string {
	t.Helper()
	cs, err := o.as.EmitChangeset(o.ulid, nil)
	if err != nil || cs == nil || cs.SignerPubKey == "" {
		t.Fatalf("origin %s has no current pubkey", o.ulid)
	}
	return cs.SignerPubKey
}

// rotateOriginInVerifierRoster simulates the origin rotating its signing key:
// it rotates the origin's OWN AppSync identity (so subsequent emitUninstall uses
// the new key), then publishes the rotated roster row (new current key + old key
// as prev with the given overlap) into the VERIFIER's registry reg.
func rotateOriginInVerifierRoster(t *testing.T, reg *multiinstance.Registry, o *signedOrigin, overlap time.Duration) {
	t.Helper()
	oldPub := originPubKey(t, o)

	newKey, err := multiinstance.GenerateRotationKey()
	if err != nil {
		t.Fatalf("GenerateRotationKey: %v", err)
	}
	if err := o.as.RotateIdentity(newKey, overlap); err != nil {
		t.Fatalf("RotateIdentity: %v", err)
	}
	newPub := originPubKey(t, o)

	// Publish the rotated row into the VERIFIER's roster: new current key + old
	// key retained as prev with the given overlap window expiry.
	if err := reg.Upsert(multiinstance.Instance{
		ULID:                 o.ulid,
		Kind:                 multiinstance.KindDevice,
		Role:                 multiinstance.RolePeer,
		Status:               multiinstance.StatusOnline,
		Ed25519PublicKey:     newPub,
		PrevEd25519PublicKey: oldPub,
		PrevKeyExpiresAt:     time.Now().UTC().Add(overlap),
	}); err != nil {
		t.Fatalf("publish rotated row: %v", err)
	}
}
