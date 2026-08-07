//go:build linux

package installer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"vulos/backend/cmd/verify"
	"vulos/backend/services/signing"
)

// ─── test helpers: build a throwaway trust chain, mirroring VERITY-02 ────────

// writeSignedManifest generates a throwaway root+release Ed25519 keypair,
// issues a release cert, signs an ImagePayload with roothash, and writes
// stable.json + stable.json.sig at manifestPath (+".sig"). It also writes the
// matching trust anchor + release cert under dir and returns a
// diskManifestVerifyPaths pointing at them, so loadVerifiedStableManifest can
// be exercised against a real (but throwaway) signature chain without ever
// touching /etc/vulos.
func writeSignedManifest(t *testing.T, dir, manifestPath, roothash string) diskManifestVerifyPaths {
	t.Helper()

	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate root key: %v", err)
	}
	releasePub, releasePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate release key: %v", err)
	}

	cert, err := signing.IssueReleaseCert(rootPriv, releasePub, "test-release-key", time.Now().Add(24*time.Hour), 0)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}

	anchorPath := filepath.Join(dir, "trust-anchor.pub")
	if err := os.WriteFile(anchorPath, []byte(base64.StdEncoding.EncodeToString(rootPub)+"\n"), 0o644); err != nil {
		t.Fatalf("write anchor: %v", err)
	}
	certPath := filepath.Join(dir, "release-cert.json")
	certJSON, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("marshal cert: %v", err)
	}
	if err := os.WriteFile(certPath, certJSON, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	payload := verify.ImagePayload{
		Path:       "vulos-amd64.img",
		RootHash:   roothash,
		Size:       123456,
		MinEpoch:   0,
		ReleasedAt: time.Now().UTC().Format(time.RFC3339),
	}
	canonical, err := signing.Canonical(payload)
	if err != nil {
		t.Fatalf("canonical payload: %v", err)
	}
	sigBytes := signing.Sign(releasePriv, canonical)
	sigFile, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "test-release-key",
		SigBytes:  sigBytes,
	})
	if err != nil {
		t.Fatalf("marshal sig: %v", err)
	}

	manifestJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, manifestJSON, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath+".sig", sigFile, 0o644); err != nil {
		t.Fatalf("write sig: %v", err)
	}

	return diskManifestVerifyPaths{
		AnchorPath: anchorPath,
		CertPath:   certPath,
		EpochPath:  filepath.Join(dir, "epoch-floor.json"), // absent -> floor 0
	}
}

// ─── loadVerifiedStableManifest ───────────────────────────────────────────────

func TestLoadVerifiedStableManifest_ValidChainVerifies(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "stable.json")
	roothash := strings.Repeat("a", 64)
	vp := writeSignedManifest(t, dir, manifestPath, roothash)

	payload, manifestBytes, sigBytes, err := loadVerifiedStableManifest(manifestPath, vp)
	if err != nil {
		t.Fatalf("expected a validly-signed manifest to verify, got: %v", err)
	}
	if payload.RootHash != roothash {
		t.Errorf("payload.RootHash = %q, want %q", payload.RootHash, roothash)
	}
	if len(manifestBytes) == 0 {
		t.Error("expected non-empty manifest bytes")
	}
	if len(sigBytes) == 0 {
		t.Error("expected non-empty sig bytes")
	}
}

func TestLoadVerifiedStableManifest_MissingManifest_Aborts(t *testing.T) {
	dir := t.TempDir()
	_, _, _, err := loadVerifiedStableManifest(filepath.Join(dir, "stable.json"), diskManifestVerifyPaths{})
	if err == nil {
		t.Fatal("expected an error when stable.json is absent, got nil — this is exactly the e5248e84 failure mode (missing manifest -> kernel panic at boot) if it does not abort here")
	}
	if !strings.Contains(err.Error(), "stable.json") {
		t.Errorf("error should mention stable.json, got: %v", err)
	}
}

func TestLoadVerifiedStableManifest_MissingSig_Aborts(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "stable.json")
	payload := verify.ImagePayload{Path: "vulos-amd64.img", RootHash: strings.Repeat("b", 64)}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Deliberately no manifestPath+".sig" written.

	_, _, _, err = loadVerifiedStableManifest(manifestPath, diskManifestVerifyPaths{})
	if err == nil {
		t.Fatal("expected an error when stable.json.sig is absent, got nil")
	}
}

func TestLoadVerifiedStableManifest_MissingRequiredFields_Aborts(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "stable.json")
	// Valid JSON, but no roothash/path — structurally incomplete.
	if err := os.WriteFile(manifestPath, []byte(`{"size":1,"min_epoch":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath+".sig", []byte("vulos-sig-v1\nalgorithm: ed25519\nkey-id: x\nsig: AAAA\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := loadVerifiedStableManifest(manifestPath, diskManifestVerifyPaths{})
	if err == nil {
		t.Fatal("expected an error for a manifest missing roothash/path, got nil")
	}
}

func TestLoadVerifiedStableManifest_TamperedManifest_Aborts(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "stable.json")
	original := strings.Repeat("c", 64)
	vp := writeSignedManifest(t, dir, manifestPath, original)

	// Tamper the manifest AFTER signing: an attacker (or a bad copy) changing
	// the root hash must invalidate the signature, since the release key
	// signed the ORIGINAL bytes.
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(data, []byte(original), []byte(strings.Repeat("d", 64)), 1)
	if bytes.Equal(data, tampered) {
		t.Fatal("test bug: tamper had no effect on manifest bytes")
	}
	if err := os.WriteFile(manifestPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, _, err = loadVerifiedStableManifest(manifestPath, vp)
	if err == nil {
		t.Fatal("expected an error for a tampered manifest whose signature no longer matches, got nil")
	}
}

func TestLoadVerifiedStableManifest_WrongAnchor_Aborts(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "stable.json")
	vp := writeSignedManifest(t, dir, manifestPath, strings.Repeat("e", 64))

	// Point at a DIFFERENT (unrelated) trust anchor — simulates a release
	// cert signed by a key the device does not actually trust.
	otherRootPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongAnchor := filepath.Join(dir, "wrong-anchor.pub")
	if err := os.WriteFile(wrongAnchor, []byte(base64.StdEncoding.EncodeToString(otherRootPub)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vp.AnchorPath = wrongAnchor

	_, _, _, err = loadVerifiedStableManifest(manifestPath, vp)
	if err == nil {
		t.Fatal("expected an error when the release cert does not validate against the trust anchor, got nil")
	}
}

// ─── writeManifestToRoot ───────────────────────────────────────────────────────

func TestWriteManifestToRoot_PlacesFilesAtVerityPaths(t *testing.T) {
	root := t.TempDir()
	manifestBytes := []byte(`{"path":"vulos-amd64.img","roothash":"abc123"}`)
	sigBytes := []byte("vulos-sig-v1\nalgorithm: ed25519\nkey-id: x\nsig: AAAA\n")

	if err := writeManifestToRoot(root, manifestBytes, sigBytes); err != nil {
		t.Fatalf("writeManifestToRoot: %v", err)
	}

	// These are exactly the relative paths verityManifestPath /
	// verityManifestSigPath (backend/cmd/init/main.go) read once root becomes "/".
	gotManifest, err := os.ReadFile(filepath.Join(root, "etc", "vulos", "stable.json"))
	if err != nil {
		t.Fatalf("stable.json not written where VERITY-02 reads it: %v", err)
	}
	if string(gotManifest) != string(manifestBytes) {
		t.Errorf("stable.json content mismatch: got %q, want %q", gotManifest, manifestBytes)
	}

	gotSig, err := os.ReadFile(filepath.Join(root, "etc", "vulos", "stable.json.sig"))
	if err != nil {
		t.Fatalf("stable.json.sig not written where VERITY-02 reads it: %v", err)
	}
	if string(gotSig) != string(sigBytes) {
		t.Errorf("stable.json.sig content mismatch: got %q, want %q", gotSig, sigBytes)
	}
}

// ─── WriteBootableDisk — validation / ordering ────────────────────────────────

func TestWriteBootableDisk_EmptyTargetDisk(t *testing.T) {
	err := WriteBootableDisk(context.Background(), DiskConfig{})
	if err == nil {
		t.Error("expected error for empty TargetDisk, got nil")
	}
	if !strings.Contains(err.Error(), "TargetDisk") {
		t.Errorf("error should mention TargetDisk, got: %v", err)
	}
}

// TestWriteBootableDisk_AbortsWhenManifestMissing proves the manifest check
// happens BEFORE any destructive disk operation: TargetDisk here does not
// exist, so if partitioning were attempted first the error would come back
// from parted/mkfs, not from the manifest check. This is the install-time
// version of the VERITY-02 gate — it must fire, and it must fire first.
func TestWriteBootableDisk_AbortsWhenManifestMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := DiskConfig{
		TargetDisk:         "/dev/vulos-installer-test-nonexistent-device",
		SquashfsPath:       filepath.Join(dir, "os-core.squashfs"), // also absent
		StableManifestPath: filepath.Join(dir, "stable.json"),      // absent
	}
	err := WriteBootableDisk(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected WriteBootableDisk to abort when the manifest is missing, got nil")
	}
	if !strings.Contains(err.Error(), "stable.json") {
		t.Errorf("expected the abort to be manifest-related (checked first, before any disk op), got: %v", err)
	}
}

// ─── writeDiskBootEntry ────────────────────────────────────────────────────────

func TestWriteDiskBootEntry_CreatesEntry(t *testing.T) {
	tmpESP := t.TempDir()

	if err := writeDiskBootEntry(context.Background(), tmpESP); err != nil {
		t.Fatalf("writeDiskBootEntry: %v", err)
	}

	entryPath := filepath.Join(tmpESP, "loader", "entries", "vulos.conf")
	data, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatalf("entry file not created: %v", err)
	}
	content := string(data)

	checks := []string{
		"root=LABEL=vulos-root",
		"init=/sbin/vulos-init",
		"/vmlinuz",
		"/initrd.img",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("entry missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "vulos.live") {
		t.Errorf("disk entry must not carry vulos.live — that is the --live entry's marker:\n%s", content)
	}

	loaderConf := filepath.Join(tmpESP, "loader", "loader.conf")
	if _, err := os.Stat(loaderConf); err != nil {
		t.Errorf("loader.conf not created: %v", err)
	}
}

// TestWriteDiskBootEntry_MatchesBuildShDiskEntry reads build.sh directly and
// asserts the installer's loader entry is byte-for-byte the same string
// build.sh's --disk section writes. This is deliberately NOT a hardcoded
// copy of the string on both sides: comparing against build.sh's own printf
// argument means a future edit to either side that breaks the match fails
// this test, instead of quietly producing the build.sh-vs-installer drift
// that caused the e5248e84 kernel panic.
func TestWriteDiskBootEntry_MatchesBuildShDiskEntry(t *testing.T) {
	buildSh := readBuildSh(t)

	re := regexp.MustCompile(`printf '([^']*)' > "\$OUTDIR/_entry\.conf"`)
	m := re.FindSubmatch(buildSh)
	if m == nil {
		t.Fatal("could not locate the --disk loader-entry printf in build.sh (search string \"_entry.conf\") — has build.sh's --disk section moved or changed shape?")
	}
	want := unescapePrintf(string(m[1]))

	tmpESP := t.TempDir()
	if err := writeDiskBootEntry(context.Background(), tmpESP); err != nil {
		t.Fatalf("writeDiskBootEntry: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(tmpESP, "loader", "entries", "vulos.conf"))
	if err != nil {
		t.Fatalf("entry file not created: %v", err)
	}

	if string(got) != want {
		t.Errorf("installer's disk loader entry does not match build.sh's --disk entry:\n--- build.sh wants ---\n%q\n--- installer wrote ---\n%q", want, string(got))
	}
}

// TestDiskLoaderConf_MatchesBuildSh is the same drift check for loader.conf.
func TestDiskLoaderConf_MatchesBuildSh(t *testing.T) {
	buildSh := readBuildSh(t)

	re := regexp.MustCompile(`printf '([^']*)' > "\$OUTDIR/_loader\.conf"`)
	m := re.FindSubmatch(buildSh)
	if m == nil {
		t.Fatal("could not locate the --disk loader.conf printf in build.sh (search string \"_loader.conf\")")
	}
	want := unescapePrintf(string(m[1]))

	if diskLoaderConfContent != want {
		t.Errorf("diskLoaderConfContent does not match build.sh's --disk loader.conf:\nbuild.sh:  %q\ninstaller: %q", want, diskLoaderConfContent)
	}
}

// readBuildSh locates and reads the repo's build.sh relative to this test
// file's own location, so the test works regardless of the caller's cwd.
func readBuildSh(t *testing.T) []byte {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	// backend/internal/installer/disk_test.go -> repo root is three levels up.
	buildShPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "build.sh")
	data, err := os.ReadFile(buildShPath)
	if err != nil {
		t.Fatalf("could not read build.sh at %s (repo layout assumption broken?): %v", buildShPath, err)
	}
	return data
}

// unescapePrintf turns a shell single-quoted printf argument's literal `\n`
// sequences into real newlines, matching what `printf` itself does at build
// time.
func unescapePrintf(s string) string {
	return strings.ReplaceAll(s, `\n`, "\n")
}
