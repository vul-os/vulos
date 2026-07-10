package main

// Integration tests for the vulos-verify-sig CLI: the initramfs roothash gate.
// The binary is built once, then invoked with real files so exit codes reflect
// production behaviour (fail-closed on any doubt).

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"vulos/backend/services/signing"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "vulos-verify-sig")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// harness writes an anchor, a payload file, and (optionally) a valid .sig.
type harness struct {
	dir        string
	anchorPath string
	filePath   string
	sigPath    string
	priv       ed25519.PrivateKey
	payload    []byte
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	anchorPath := filepath.Join(dir, "trust-anchor.pub")
	if err := os.WriteFile(anchorPath, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); err != nil {
		t.Fatalf("write anchor: %v", err)
	}
	payload := []byte("deadbeefcafef00d roothash contents\n")
	filePath := filepath.Join(dir, "os-core.roothash")
	if err := os.WriteFile(filePath, payload, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return &harness{
		dir: dir, anchorPath: anchorPath, filePath: filePath,
		sigPath: filePath + ".sig", priv: priv, payload: payload,
	}
}

func (h *harness) writeValidSig(t *testing.T) {
	t.Helper()
	sig := signing.Sign(h.priv, h.payload)
	data, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID, KeyID: "test", SigBytes: sig,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(h.sigPath, data, 0o644); err != nil {
		t.Fatalf("write sig: %v", err)
	}
}

func run(t *testing.T, bin string, args ...string) int {
	t.Helper()
	cmd := exec.Command(bin, args...)
	err := cmd.Run()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	t.Fatalf("run: %v", err)
	return -1
}

func TestVerifySig_ValidExitsZero(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	h.writeValidSig(t)
	if code := run(t, bin, h.anchorPath, h.filePath, h.sigPath); code != 0 {
		t.Fatalf("valid sig should exit 0, got %d", code)
	}
}

func TestVerifySig_TamperedFileExitsOne(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	h.writeValidSig(t)
	// Tamper the file after signing.
	if err := os.WriteFile(h.filePath, append([]byte("X"), h.payload...), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if code := run(t, bin, h.anchorPath, h.filePath, h.sigPath); code != 1 {
		t.Fatalf("tampered file must exit 1 (fail closed), got %d", code)
	}
}

func TestVerifySig_MissingSigExitsOne(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	// No sig written.
	if code := run(t, bin, h.anchorPath, h.filePath, h.sigPath); code != 1 {
		t.Fatalf("missing sig must exit 1, got %d", code)
	}
}

func TestVerifySig_MissingAnchorExitsOne(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	h.writeValidSig(t)
	if code := run(t, bin, filepath.Join(h.dir, "nope.pub"), h.filePath, h.sigPath); code != 1 {
		t.Fatalf("missing anchor must exit 1, got %d", code)
	}
}

func TestVerifySig_WrongKeyExitsOne(t *testing.T) {
	bin := buildBinary(t)
	h := newHarness(t)
	// Sign with an unrelated key — must not verify against the pinned anchor.
	_, attackerPriv, _ := ed25519.GenerateKey(rand.Reader)
	sig := signing.Sign(attackerPriv, h.payload)
	data, _ := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID, KeyID: "attacker", SigBytes: sig,
	})
	if err := os.WriteFile(h.sigPath, data, 0o644); err != nil {
		t.Fatalf("write sig: %v", err)
	}
	if code := run(t, bin, h.anchorPath, h.filePath, h.sigPath); code != 1 {
		t.Fatalf("wrong-key sig must exit 1, got %d", code)
	}
}

func TestVerifySig_BadArgsExitsTwo(t *testing.T) {
	bin := buildBinary(t)
	if code := run(t, bin, "only-one-arg"); code != 2 {
		t.Fatalf("bad args must exit 2, got %d", code)
	}
}
