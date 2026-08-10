//go:build linux

package main

// verity_bind_test.go — the VERITY-02 binding: does the signed root hash
// describe the OS this machine is actually running?
//
// The gate this replaces compared the manifest's root hash with the manifest's
// root hash. So the doubles here MEASURE something: the fake `veritysetup
// status` derives its root hash from the BYTES of an image file on disk, the way
// dm-verity's Merkle tree does. A double that simply agreed would reproduce the
// exact defect under test — it would pass against a substituted image.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeVerityDevice points the binder at a temp "device" node and makes
// `veritysetup status` report the root hash of imagePath's current contents.
// Returns the image path so a test can substitute the image under it.
func fakeVerityDevice(t *testing.T, content []byte) string {
	t.Helper()
	dir := t.TempDir()

	imagePath := filepath.Join(dir, "os-core.squashfs")
	if err := os.WriteFile(imagePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	devPath := filepath.Join(dir, "vulos-root")
	if err := os.WriteFile(devPath, []byte("dm node"), 0o644); err != nil {
		t.Fatal(err)
	}

	prevDev, prevStatus := verityDevicePath, veritysetupStatus
	verityDevicePath = devPath
	veritysetupStatus = func(name string) ([]byte, error) {
		// Measure the bytes the "device" is backed by, exactly as the kernel's
		// dm-verity target does when it walks the tree.
		data, err := os.ReadFile(imagePath)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		return []byte(fmt.Sprintf(`/dev/mapper/%s is active and is in use.
  type:        VERITY
  status:      verified
  hash type:   1
  data block:  4096
  hash block:  4096
  hash name:   sha256
  root hash:   %s
`, name, hex.EncodeToString(sum[:]))), nil
	}
	t.Cleanup(func() { verityDevicePath, veritysetupStatus = prevDev, prevStatus })

	return imagePath
}

// signedHashOf is what the release key would have signed for this content.
func signedHashOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// The image the kernel is enforcing IS the one that was signed.
func TestActiveVerityBinder_BindsTheRunningImage(t *testing.T) {
	content := []byte("the-image-that-was-signed")
	fakeVerityDevice(t, content)

	if err := activeVerityBinder()(signedHashOf(content)); err != nil {
		t.Fatalf("the signed image should bind: %v", err)
	}
}

// THE test: the machine is running different bytes from the ones that were
// signed. The signature over the payload is untouched and still valid — only the
// image differs — so this is exactly the case the old self-comparison could
// never catch.
func TestActiveVerityBinder_SubstitutedImage_Refused(t *testing.T) {
	content := []byte("the-image-that-was-signed")
	imagePath := fakeVerityDevice(t, content)
	signed := signedHashOf(content)

	if err := activeVerityBinder()(signed); err != nil {
		t.Fatalf("precondition: the signed image must bind first: %v", err)
	}

	if err := os.WriteFile(imagePath, []byte("a-different-image-entirely"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := activeVerityBinder()(signed)
	if err == nil {
		t.Fatal("a root verified against a DIFFERENT image must be refused")
	}
	if !strings.Contains(err.Error(), "verified against") {
		t.Fatalf("the refusal should name both hashes, got: %v", err)
	}
}

// Hex case must not decide it either way.
func TestActiveVerityBinder_CaseInsensitive(t *testing.T) {
	content := []byte("the-image-that-was-signed")
	fakeVerityDevice(t, content)

	if err := activeVerityBinder()(strings.ToUpper(signedHashOf(content))); err != nil {
		t.Fatalf("hex case alone must not refuse a matching image: %v", err)
	}
}

// No dm-verity device: the ext4 --disk layout. This is the ONE outcome
// verifyOSBeforeBoot does not halt on, so it must be distinguishable — a bare
// error would be indistinguishable from a mismatch and would either brick that
// layout or wave a real mismatch through.
func TestActiveVerityRootHash_NoDevice_IsDistinguishable(t *testing.T) {
	prev := verityDevicePath
	verityDevicePath = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { verityDevicePath = prev })

	_, err := activeVerityRootHash()
	if !errors.Is(err, errNoVerityDevice) {
		t.Fatalf("an absent verity device must report errNoVerityDevice, got: %v", err)
	}
}

// A device that EXISTS but cannot be interrogated is a refusal, not a shrug:
// it must NOT masquerade as the ext4 layout, or an attacker could reach the
// non-halting branch by breaking veritysetup.
func TestActiveVerityRootHash_StatusFails_IsNotMistakenForNoDevice(t *testing.T) {
	devPath := filepath.Join(t.TempDir(), "vulos-root")
	if err := os.WriteFile(devPath, []byte("dm node"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevDev, prevStatus := verityDevicePath, veritysetupStatus
	verityDevicePath = devPath
	veritysetupStatus = func(string) ([]byte, error) {
		return []byte("veritysetup: command not found"), errors.New("exit status 127")
	}
	t.Cleanup(func() { verityDevicePath, veritysetupStatus = prevDev, prevStatus })

	_, err := activeVerityRootHash()
	if err == nil {
		t.Fatal("an uninterrogable verity device must be an error")
	}
	if errors.Is(err, errNoVerityDevice) {
		t.Fatalf("it must NOT be reported as an absent device — that branch does not halt the boot: %v", err)
	}
}

// The parser rejects anything it cannot read as a hash, rather than comparing
// garbage: two identically garbled values must never count as a match.
func TestParseVeritysetupRootHash(t *testing.T) {
	good := "  type: VERITY\n  root hash:   ABCdef0123\n"
	got, err := parseVeritysetupRootHash(good)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "abcdef0123" {
		t.Fatalf("root hash should be lowercased, got %q", got)
	}

	for name, out := range map[string]string{
		"no root hash line": "/dev/mapper/vulos-root is active.\n  type: VERITY\n",
		"empty value":       "  root hash:   \n",
		"not hex":           "  root hash:   \x1b[0mzzzz\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseVeritysetupRootHash(out); err == nil {
				t.Fatalf("expected an error for %q", out)
			}
		})
	}
}
