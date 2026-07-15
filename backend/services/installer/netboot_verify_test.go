package installer

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/osdist"
	"vulos/backend/services/signing"
)

// ---------------------------------------------------------------------------
// Test harness: a temp "seed" tree with anchor + squashfs + detached .sig
// ---------------------------------------------------------------------------

type verifyFixture struct {
	dir          string
	anchorPath   string
	epochPath    string
	squashfsPath string
	pub          ed25519.PublicKey
	priv         ed25519.PrivateKey
	image        []byte
}

// newVerifyFixture creates a temp dir with a keypair and a written anchor file.
func newVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	dir := t.TempDir()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	anchorPath := filepath.Join(dir, "trust-anchor.pub")
	// Anchor file format: single line, base64(raw 32-byte pubkey).
	if err := os.WriteFile(anchorPath, []byte(base64.StdEncoding.EncodeToString(pub)), 0o644); err != nil {
		t.Fatalf("write anchor: %v", err)
	}

	image := []byte("this-is-a-fake-squashfs-payload-for-testing-only")
	squashfsPath := filepath.Join(dir, "os-core.squashfs")
	if err := os.WriteFile(squashfsPath, image, 0o644); err != nil {
		t.Fatalf("write squashfs: %v", err)
	}

	return &verifyFixture{
		dir:          dir,
		anchorPath:   anchorPath,
		epochPath:    filepath.Join(dir, "epoch-floor.json"),
		squashfsPath: squashfsPath,
		pub:          pub,
		priv:         priv,
		image:        image,
	}
}

// writeSig signs the given bytes with the fixture key and writes a detached
// .sig file at path in the Vulos detached-signature format.
func (f *verifyFixture) writeSig(t *testing.T, path string, over []byte) {
	t.Helper()
	sig := signing.Sign(f.priv, over)
	data, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "test-release",
		SigBytes:  sig,
	})
	if err != nil {
		t.Fatalf("marshal sig: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write sig %s: %v", path, err)
	}
}

func (f *verifyFixture) cfg() netbootVerifyConfig {
	return netbootVerifyConfig{anchorPath: f.anchorPath, epochPath: f.epochPath}
}

func (f *verifyFixture) svc() *Service {
	c := f.cfg()
	s := newWithCommander(newMockCmd())
	s.verifyCfg = &c
	return s
}

func hexHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// Happy path: valid detached signature over the image passes.
// ---------------------------------------------------------------------------

func TestVerifyNetbootSquashfs_ValidSignature(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeSig(t, f.squashfsPath+".sig", f.image)

	if err := f.svc().verifyNetbootSquashfs(f.squashfsPath, f.cfg()); err != nil {
		t.Fatalf("valid image should verify, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// #3 MITM substitution / #1 tamper: any change to the image fails closed.
// ---------------------------------------------------------------------------

func TestVerifyNetbootSquashfs_TamperedImage_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	// Sign the ORIGINAL image, then tamper the bytes on disk.
	f.writeSig(t, f.squashfsPath+".sig", f.image)
	if err := os.WriteFile(f.squashfsPath, append([]byte("EVIL"), f.image...), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	err := f.svc().verifyNetbootSquashfs(f.squashfsPath, f.cfg())
	if !errors.Is(err, ErrSquashfsBadSignature) {
		t.Fatalf("tampered image must fail with ErrSquashfsBadSignature, got: %v", err)
	}
}

// A signature produced by a DIFFERENT key (attacker key) must not verify
// against the pinned anchor — TLS/CA success is irrelevant; only the pinned key.
func TestVerifyNetbootSquashfs_WrongSigningKey_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	_, attackerPriv, _ := ed25519.GenerateKey(rand.Reader)
	attackerSig := signing.Sign(attackerPriv, f.image)
	data, _ := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID, KeyID: "attacker", SigBytes: attackerSig,
	})
	if err := os.WriteFile(f.squashfsPath+".sig", data, 0o644); err != nil {
		t.Fatalf("write attacker sig: %v", err)
	}

	err := f.svc().verifyNetbootSquashfs(f.squashfsPath, f.cfg())
	if !errors.Is(err, ErrSquashfsBadSignature) {
		t.Fatalf("attacker-key signature must fail, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Missing signature ⇒ refuse (no unsigned install).
// ---------------------------------------------------------------------------

func TestVerifyNetbootSquashfs_MissingSig_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	// Deliberately do NOT write the .sig.
	err := f.svc().verifyNetbootSquashfs(f.squashfsPath, f.cfg())
	if !errors.Is(err, ErrSquashfsSigMissing) {
		t.Fatalf("missing sig must fail with ErrSquashfsSigMissing, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// #2 cert-pinning note: absent/malformed pinned anchor ⇒ refuse (never fall
// back to CA-trust).
// ---------------------------------------------------------------------------

func TestVerifyNetbootSquashfs_MissingAnchor_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeSig(t, f.squashfsPath+".sig", f.image)
	c := f.cfg()
	c.anchorPath = filepath.Join(f.dir, "does-not-exist.pub")

	err := f.svc().verifyNetbootSquashfs(f.squashfsPath, c)
	if !errors.Is(err, ErrAnchorMissing) {
		t.Fatalf("missing anchor must fail with ErrAnchorMissing, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Signed manifest present: roothash binding + epoch-floor downgrade defence.
// ---------------------------------------------------------------------------

// writeManifest writes stable.json + stable.json.sig beside the squashfs.
func (f *verifyFixture) writeManifest(t *testing.T, minEpoch int64, rootHash string) {
	t.Helper()
	m := &osdist.StableManifest{
		Channel:    "stable",
		Latest:     "v08",
		MinEpoch:   minEpoch,
		RootHash:   rootHash,
		Size:       int64(len(f.image)),
		ReleasedAt: time.Unix(0, 0).UTC(),
		Path:       "os/v08/os-core.squashfs",
	}
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatalf("manifest canonical: %v", err)
	}
	manifestPath := filepath.Join(f.dir, "stable.json")
	// Write the exact canonical bytes so verification re-derives the same input.
	if err := os.WriteFile(manifestPath, canonical, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	f.writeSig(t, manifestPath+".sig", canonical)
}

func TestVerifyNetbootSquashfs_ManifestRoothashMatch_Passes(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeSig(t, f.squashfsPath+".sig", f.image)
	f.writeManifest(t, 1, hexHash(f.image))

	if err := f.svc().verifyNetbootSquashfs(f.squashfsPath, f.cfg()); err != nil {
		t.Fatalf("matching roothash should pass, got: %v", err)
	}
}

func TestVerifyNetbootSquashfs_ManifestRoothashMismatch_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeSig(t, f.squashfsPath+".sig", f.image)
	// Manifest names a different image hash than the one on disk.
	f.writeManifest(t, 1, hexHash([]byte("some-other-image")))

	err := f.svc().verifyNetbootSquashfs(f.squashfsPath, f.cfg())
	if !errors.Is(err, ErrSquashfsHashMismatch) {
		t.Fatalf("roothash mismatch must fail, got: %v", err)
	}
}

// #5 Downgrade/rollback: a signed manifest whose min_epoch is below the device
// floor is refused even though the signature is valid.
func TestVerifyNetbootSquashfs_DowngradeBelowEpochFloor_FailsClosed(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeSig(t, f.squashfsPath+".sig", f.image)
	// Manifest is validly signed but for an OLD epoch (2).
	f.writeManifest(t, 2, hexHash(f.image))

	// Raise the device epoch floor to 5 — higher than the manifest epoch.
	es, err := signing.NewEpochStore(f.epochPath)
	if err != nil {
		t.Fatalf("epoch store: %v", err)
	}
	if err := es.RaiseTo(5); err != nil {
		t.Fatalf("raise floor: %v", err)
	}

	err = f.svc().verifyNetbootSquashfs(f.squashfsPath, f.cfg())
	if err == nil || !errors.Is(err, osdist.ErrEpochTooLow) {
		t.Fatalf("downgrade below floor must fail with ErrEpochTooLow, got: %v", err)
	}
}

// A validly-signed manifest at or above the floor is accepted.
func TestVerifyNetbootSquashfs_EpochAtFloor_Passes(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeSig(t, f.squashfsPath+".sig", f.image)
	f.writeManifest(t, 5, hexHash(f.image))

	es, _ := signing.NewEpochStore(f.epochPath)
	_ = es.RaiseTo(5)

	if err := f.svc().verifyNetbootSquashfs(f.squashfsPath, f.cfg()); err != nil {
		t.Fatalf("epoch at floor should pass, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Pipeline integration: the verify step aborts the install before staging.
// ---------------------------------------------------------------------------

func TestRunNetbootInstall_UnverifiedSquashfs_AbortsBeforeStage(t *testing.T) {
	f := newVerifyFixture(t)
	// No .sig written → verification must fail closed and the pipeline must stop
	// at the verify-squashfs step, never reaching stage-squashfs.
	c := f.cfg()

	mc := newMockCmd()
	mc.set("aaaa-1111", nil, "blkid", "-s", "UUID", "-o", "value", "/dev/sda1")
	mc.set("bbbb-2222", nil, "blkid", "-s", "UUID", "-o", "value", "/dev/sda2")
	svc := newWithCommander(mc)
	svc.verifyCfg = &c

	hub := newProgressHub()
	req := NetbootInstallRequest{
		Disk:         "sda",
		Confirm:      true,
		SquashfsPath: f.squashfsPath,
	}
	svc.runNetbootInstall(req, hub)

	done, err := hub.isDone()
	if !done {
		t.Fatal("pipeline should have completed (with error)")
	}
	if err == nil {
		t.Fatal("unverified squashfs must abort the install")
	}
	if !contains(err.Error(), "verify-squashfs") {
		t.Fatalf("install must fail at verify-squashfs step, got: %v", err)
	}
	// The staged squashfs must NOT exist on the (mock) target.
	staged := filepath.Join(netbootInstallMount, vulosCacheRelPath, "slot-a", "os-core.squashfs")
	if _, statErr := os.Stat(staged); statErr == nil {
		t.Fatalf("squashfs was staged despite failed verification: %s", staged)
	}
}

// TestRunNetbootInstall_UnverifiedSquashfs_DoesNotWipeDisk pins the ordering
// guarantee: because verify-squashfs runs FIRST (before partition/format), an
// image that fails verification must abort with the target disk NEVER touched —
// no parted, no mkfs, no mount. A bad or unsigned image can never cost the
// operator their existing disk.
func TestRunNetbootInstall_UnverifiedSquashfs_DoesNotWipeDisk(t *testing.T) {
	f := newVerifyFixture(t)
	c := f.cfg() // no .sig written → verification fails closed

	mc := newMockCmd()
	svc := newWithCommander(mc)
	svc.verifyCfg = &c

	hub := newProgressHub()
	req := NetbootInstallRequest{Disk: "sda", Confirm: true, SquashfsPath: f.squashfsPath}
	svc.runNetbootInstall(req, hub)

	done, err := hub.isDone()
	if !done || err == nil {
		t.Fatalf("expected failed completion, done=%v err=%v", done, err)
	}
	if !contains(err.Error(), "verify-squashfs") {
		t.Fatalf("must fail at verify-squashfs, got: %v", err)
	}
	// No external command may have run — the disk is pristine.
	for _, dangerous := range []string{"parted", "mkfs", "mkfs.fat", "mkfs.vfat", "mkfs.ext4", "sgdisk", "wipefs", "dd", "mount"} {
		for _, call := range mc.calls {
			if strings.HasPrefix(call, dangerous) {
				t.Fatalf("destructive command %q ran despite failed verification (call=%q) — verify must gate before any disk write", dangerous, call)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Admin gate: the destructive netboot-install endpoint must reject non-admins.
// ---------------------------------------------------------------------------

func TestHandleNetbootInstall_AdminGate_DeniesNonAdmin(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	svc.isAdmin = func(r *http.Request) bool { return false } // deny everyone

	mux := http.NewServeMux()
	RegisterNetbootHandlers(mux, svc)

	body := `{"disk":"sda","confirm":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/installer/netboot-install",
		strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin must get 403, got %d", rr.Code)
	}
}

func TestHandleNetbootInstall_AdminGate_AllowsAdmin(t *testing.T) {
	mc := newMockCmd()
	svc := newWithCommander(mc)
	svc.isAdmin = func(r *http.Request) bool { return true } // allow

	mux := http.NewServeMux()
	RegisterNetbootHandlers(mux, svc)

	// Point at a nonexistent squashfs so the async pipeline fails harmlessly.
	body := `{"disk":"sda","confirm":true,"squashfs_path":"/nonexistent/os-core.squashfs"}`
	req := httptest.NewRequest(http.MethodPost, "/api/installer/netboot-install",
		strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("admin must be accepted (202), got %d", rr.Code)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
