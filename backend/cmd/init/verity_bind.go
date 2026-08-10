//go:build linux

package main

// verity_bind.go — VERITY-02: bind the SIGNED root hash to the OS this machine
// is actually running.
//
// # The gate that checked nothing
//
// verifyOSBeforeBoot filled BOTH halves of cmd/verify's root-hash comparison out
// of the same file: ExpectedRootHash came from /etc/vulos/stable.json, and so
// did ImagePayloadForSig.  The check therefore compared a value with itself.  It
// could not fail, it never opened an image, and it logged "OS image verified OK
// (roothash=…)" every time.  The signature it DID verify covers a path, a size
// and a root hash — a description of an artifact, not the artifact.
//
// # What can honestly be bound here, and what cannot
//
// This code runs after pivot_root, and it cannot re-hash the image itself: on
// the squashfs layout scripts/initramfs/vulos-live mounts the root partition at
// $rootmnt and then binds the merged overlay over it, so os-core.squashfs is no
// longer reachable by path from the booted system.  Re-deriving a dm-verity root
// hash from the bytes is in any case not something a userspace pass can do
// cheaply or without the hash tree's salt.
//
// What the kernel already did is better than either.  `veritysetup open` builds
// a dm-verity target pinned to a root hash, and the kernel re-verifies EVERY
// 4096-byte block against that Merkle tree as it is read.  So asking device
// mapper "which root hash is the device backing this root being verified
// against?" and comparing it with the signed one binds the signature to the
// bytes the machine is executing — via the same primitive the initramfs used,
// not a stand-in for it.
//
// # The layout that cannot be bound at all
//
// `build.sh --disk` ships an EXT4 root, not a squashfs+dm-verity device (its own
// comment says so, and so does backend/internal/installer/disk.go).  There is no
// verity device on such a machine, so there is nothing to ask.  The honest
// outcome is not to invent a pass: the binder returns errNoVerityDevice, and
// verifyOSBeforeBoot reports plainly that the manifest's SIGNATURE verified and
// its root hash was NOT bound to anything.  Where a verity device does exist,
// any failure to bind — mismatch, unreadable status, absent tool — HALTS.
//
// The residual hole is stated rather than hidden: an attacker with write access
// to a verity-installed disk could convert it to the ext4 layout, keep the
// genuine signed manifest, and land in the unbindable branch.  They would need
// arbitrary write access to the root filesystem, which on the ext4 layout defeats
// everything anyway — but closing it needs the installer to record which layout
// it laid down, and that is a change to the installers, not to this gate.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"vulos/backend/cmd/verify"
)

// verityDeviceName is the dm-verity mapping scripts/initramfs/vulos-live opens
// (VERITY_NAME in that script).  The two must agree.
const verityDeviceName = "vulos-root"

// verityDevicePath is the device-mapper node that exists if, and only if, the
// initramfs opened a dm-verity target for the root image.  Its presence is what
// distinguishes "this machine has a binding to check" from "this machine is the
// ext4 layout, which has none".  A var for the test seam only.
var verityDevicePath = "/dev/mapper/" + verityDeviceName

// veritysetupStatus asks device mapper about a mapping.  A var so tests can
// drive the parse and the comparison without cryptsetup installed; production
// never assigns it.
var veritysetupStatus = func(name string) ([]byte, error) {
	return exec.Command("veritysetup", "status", name).CombinedOutput()
}

// errNoVerityDevice reports that this machine is not running a dm-verity-backed
// root image, so the signed root hash cannot be bound to anything here.
var errNoVerityDevice = errors.New("no dm-verity device backs this root")

// activeVerityRootHash returns the root hash the kernel is enforcing for the
// running root image, in lowercase hex.
func activeVerityRootHash() (string, error) {
	if _, err := os.Stat(verityDevicePath); err != nil {
		return "", fmt.Errorf("%w (%s: %v)", errNoVerityDevice, verityDevicePath, err)
	}
	out, err := veritysetupStatus(verityDeviceName)
	if err != nil {
		return "", fmt.Errorf("veritysetup status %s: %v: %s",
			verityDeviceName, err, strings.TrimSpace(string(out)))
	}
	return parseVeritysetupRootHash(string(out))
}

// parseVeritysetupRootHash pulls the "root hash:" field out of `veritysetup
// status` output.  Anything that is not a plausible hex hash is rejected rather
// than compared: a garbled value that happened to equal a garbled signed value
// would be a pass for the wrong reason.
func parseVeritysetupRootHash(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.TrimSpace(line)
		if !strings.HasPrefix(f, "root hash:") {
			continue
		}
		h := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(f, "root hash:")))
		if h == "" {
			return "", fmt.Errorf("veritysetup reported an empty root hash")
		}
		if strings.TrimLeft(h, "0123456789abcdef") != "" {
			return "", fmt.Errorf("veritysetup reported a non-hex root hash %q", h)
		}
		return h, nil
	}
	return "", fmt.Errorf("veritysetup status has no \"root hash:\" line")
}

// activeVerityBinder is the verify.RootHashBinder used at boot: it compares the
// signed root hash against the one the kernel is enforcing.
func activeVerityBinder() verify.RootHashBinder {
	return func(signedRootHash string) error {
		active, err := activeVerityRootHash()
		if err != nil {
			return err
		}
		want := strings.ToLower(strings.TrimSpace(signedRootHash))
		if want != active {
			return fmt.Errorf("the release key signed root hash %s, but this root is verified against %s",
				want, active)
		}
		return nil
	}
}
