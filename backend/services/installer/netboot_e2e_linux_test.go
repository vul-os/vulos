//go:build linux

// netboot_e2e_linux_test.go — NETB-05: the real netboot-install pipeline
// against a real disk device.
//
// Every other test in this package uses the mock Commander — it proves the
// Go code calls the SHAPE of command it intends to, never that the shape is
// one the real tools accept. TestInstallNetbootBootctl_PathIsRelativeToRoot
// and its siblings exist precisely because a mocked test had, for as long as
// this pipeline has existed, been green while the real bootctl invocation
// was broken (see the comment on installNetbootBootctl). This file closes
// that gap for the whole pipeline at once: it runs runNetbootInstall's exact
// step sequence (minus the final reboot) with New()'s REAL, unmocked
// Commander — real parted, mkfs.vfat, mkfs.ext4, mount, bootctl, cp — against
// a real loop-backed disk device.
//
// This is deliberately NOT part of `go test ./...`. It partitions and
// formats a real block device and must run as root inside a privileged
// container — see scripts/netboot-install-smoke.sh, which builds a real
// --live squashfs (with the scripts/initramfs/vulos-live fixes baked in via
// update-initramfs), sets up the loop device + seed files this test expects,
// runs this test, and then boots the disk image this test produced in QEMU
// to prove the result is actually bootable — the part no Go test can verify.
//
// Skips (not fails) when VULOS_NETBOOT_E2E is unset, so a bare `go test
// ./...` on Linux CI is unaffected. Every other failure mode here is a hard
// t.Fatal, never a skip.
package installer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/signing"
)

func TestNetbootInstall_RealPipeline_E2E(t *testing.T) {
	if os.Getenv("VULOS_NETBOOT_E2E") != "1" {
		t.Skip("VULOS_NETBOOT_E2E not set — this test performs REAL parted/mkfs/mount/bootctl " +
			"against a real disk device and must run inside scripts/netboot-install-smoke.sh's " +
			"privileged container (or an equivalent root/Linux/loop-device setup). " +
			"Run: bash scripts/netboot-install-smoke.sh")
	}
	if os.Getuid() != 0 {
		t.Fatal("must run as root — this test performs real parted/mkfs/mount/bootctl")
	}

	disk := os.Getenv("VULOS_E2E_DISK")
	squashfsPath := os.Getenv("VULOS_E2E_SQUASHFS")
	if disk == "" || squashfsPath == "" {
		t.Fatal("VULOS_E2E_DISK and VULOS_E2E_SQUASHFS must both be set (see scripts/netboot-install-smoke.sh)")
	}
	if _, err := os.Stat(squashfsPath); err != nil {
		t.Fatalf("VULOS_E2E_SQUASHFS does not exist: %v", err)
	}

	// ── 1. Sign the REAL squashfs with a throwaway release key ────────────────
	//
	// verifyNetbootSquashfs (netboot_verify.go) requires a detached signature
	// over the RAW image bytes: signing.Verify(anchor, imageBytes, sig).
	// `cmd/sign sign-image` does NOT produce this — it signs a canonical JSON
	// ImagePayload (path/roothash/size/min_epoch), a different byte string
	// entirely, so its output would never verify here (see the report this
	// test's task produced: no tool in this repo currently emits a raw-bytes
	// squashfs .sig). Sign directly with the `signing` package instead, which
	// is what a raw-bytes signer would eventually need to do too — this is
	// not a mock, it is exactly the primitive verifyNetbootSquashfs calls.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate throwaway release key: %v", err)
	}
	if err := os.MkdirAll("/etc/vulos", 0o755); err != nil {
		t.Fatalf("mkdir /etc/vulos: %v", err)
	}
	if err := os.WriteFile(signing.DefaultAnchorPath, []byte(signing.EncodeAnchor(pub)), 0o644); err != nil {
		t.Fatalf("write trust anchor: %v", err)
	}
	imageBytes, err := os.ReadFile(squashfsPath)
	if err != nil {
		t.Fatalf("read squashfs: %v", err)
	}
	sigBytes := signing.Sign(priv, imageBytes)
	sigData, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "netboot-e2e-test",
		SigBytes:  sigBytes,
	})
	if err != nil {
		t.Fatalf("marshal .sig: %v", err)
	}
	if err := os.WriteFile(squashfsPath+".sig", sigData, 0o644); err != nil {
		t.Fatalf("write .sig: %v", err)
	}
	t.Logf("signed %s (%d bytes) with a throwaway release key; no signed stable.json manifest "+
		"is provided, so roothash/epoch-floor enforcement is not exercised here — the base "+
		"signature check is", squashfsPath, len(imageBytes))

	// ── 2. Confirm the seed source files the pipeline reads are in place ──────
	// These are placed by scripts/netboot-install-smoke.sh at their real,
	// hardcoded absolute paths (netbootInitramfsSrc, etc.) — this test does
	// not fabricate them, matching how a real live/RAM session would already
	// have them from SEED-01/SEED-02 and the boot itself.
	initramfsSrc := netbootInitramfsSrc
	if _, err := os.Stat(initramfsSrc); err != nil {
		initramfsSrc = initramfsAltSrc
		if _, err := os.Stat(initramfsSrc); err != nil {
			t.Fatalf("neither %s nor %s exists — seed sources must be placed before running this test", netbootInitramfsSrc, initramfsAltSrc)
		}
	}

	svc := New() // real commander — no mocks anywhere below.
	ctx := context.Background()
	dev := "/dev/" + disk
	espPart := dev + "1"
	rootPart := dev + "2"

	step := func(name string, fn func() error) {
		t.Logf("── step: %s", name)
		if err := fn(); err != nil {
			t.Fatalf("step %q failed: %v", name, err)
		}
	}

	step("verify-squashfs", func() error {
		return svc.verifyNetbootSquashfs(squashfsPath, svc.netbootVerifyConfig())
	})

	// VERITY-03: resolve + validate the dm-verity siblings before the disk is
	// touched, exactly as runNetbootInstall does.  This is not a formality here:
	// the smoke harness's builder container HAS veritysetup (cryptsetup-bin), so
	// this really runs `veritysetup verify` over the ~600 MiB image the build
	// just produced and its os-core.hashtree, and the result decides whether the
	// disk this test hands to QEMU carries verity inputs at all.
	var verity *verityArtifacts
	step("verify-verity", func() error {
		v, err := svc.resolveVerityArtifacts(ctx, squashfsPath)
		if err != nil {
			return err
		}
		verity = v
		if v == nil {
			t.Logf("no validated dm-verity artifacts beside %s — the installed disk will "+
				"mount its squashfs WITHOUT dm-verity (see netboot_verity.go for the "+
				"two ways this happens)", squashfsPath)
		} else {
			t.Logf("dm-verity artifacts verified: hashtree=%s roothash=%s (%s) sig=%v",
				v.hashtree, v.roothash, v.rootHashHex, v.sig != "")
		}
		return nil
	})
	step("partition", func() error {
		return svc.partition(ctx, dev)
	})
	step("wait-for-kernel-partition-nodes", func() error {
		// parted just wrote a fresh GPT to dev. The kernel creates the
		// partition device nodes (e.g. /dev/loop0p1) asynchronously; poll for
		// espPart/rootPart to exist rather than assuming they already do —
		// real hardware (sda1/sda2, nvme0n1p1/p2) doesn't need this, only the
		// loop-backed stand-in this test runs against (see scripts/netboot-
		// install-smoke.sh, which symlinks e.g. /dev/vda -> /dev/loopN so the
		// pipeline's partSuffix-derived naming resolves).
		return waitForPath(espPart, 10*time.Second)
	})
	step("format-esp", func() error {
		return svc.formatESP(ctx, espPart)
	})
	step("format-root", func() error {
		return svc.formatRoot(ctx, rootPart)
	})
	step("mount", func() error {
		return svc.mountNetboot(ctx, espPart, rootPart)
	})
	step("write-seed", func() error {
		return svc.writeSeedFiles(ctx)
	})
	step("stage-squashfs", func() error {
		return svc.stageFirstSquashfs(ctx, squashfsPath, verity, newProgressHub())
	})
	step("write-boot-state", func() error {
		return svc.writeInitialBootState(ctx)
	})
	step("fstab", func() error {
		return svc.writeFstabNetboot(ctx, espPart, rootPart)
	})
	step("bootloader", func() error {
		return svc.installNetbootLoader(ctx)
	})
	step("unmount", func() error {
		return svc.unmountNetboot(ctx)
	})

	t.Logf("netboot install pipeline completed for real against %s — "+
		"the reboot step is intentionally NOT run here (would reboot the "+
		"container/host); scripts/netboot-install-smoke.sh boots the "+
		"resulting disk image in QEMU instead, which is the real proof.", dev)
}

// waitForPath polls for path to exist, up to timeout. Real hardware doesn't
// need this (partition nodes exist synchronously); it exists for this test's
// loop-device stand-in only.
func waitForPath(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			out, _ := exec.Command("ls", "-la", "/dev").CombinedOutput()
			return fmt.Errorf("timed out waiting for %s to appear; /dev listing:\n%s", path, strings.TrimSpace(string(out)))
		}
		time.Sleep(200 * time.Millisecond)
	}
}
