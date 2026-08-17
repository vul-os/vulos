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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/cmd/verify"
	"vulos/backend/services/osdist"
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

	// ── 1. Produce the artifact set a real signed release produces ───────────
	//
	// verifyNetbootSquashfs verifies the chain `cmd/sign` emits:
	//
	//	offline ROOT key ──issue-release-cert──▶ cert ──▶ RELEASE key
	//	   │ baked as the seed's trust-anchor.pub        │ signs canonical(ManifestPayload)
	//	   ▼                                             └ signs canonical(ImagePayload)
	//	pinned anchor
	//
	// This block used to sign the RAW IMAGE BYTES with the anchor key, because
	// that is what the verifier used to demand — a shape `cmd/sign` cannot
	// produce, so the E2E "proof" exercised a chain that could never exist in
	// production. It now builds the real one, with throwaway keys standing in
	// for the offline root and the release key; every signature below is made
	// with the same `signing` primitives cmd/sign calls, not a stand-in for
	// them.
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate throwaway root key: %v", err)
	}
	releasePub, releasePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate throwaway release key: %v", err)
	}
	if err := os.MkdirAll("/etc/vulos", 0o755); err != nil {
		t.Fatalf("mkdir /etc/vulos: %v", err)
	}
	if err := os.WriteFile(signing.DefaultAnchorPath, []byte(signing.EncodeAnchor(rootPub)), 0o644); err != nil {
		t.Fatalf("write trust anchor: %v", err)
	}

	dir := filepath.Dir(squashfsPath)

	// The release certificate, signed by the root key, beside the image.
	cert, err := signing.IssueReleaseCert(rootPriv, releasePub, "netboot-e2e-test", time.Now().Add(24*time.Hour), 0)
	if err != nil {
		t.Fatalf("issue release cert: %v", err)
	}
	certData, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("marshal release cert: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release-cert.json"), certData, 0o644); err != nil {
		t.Fatalf("write release cert: %v", err)
	}

	// The manifest must name THIS image: the real dm-verity root hash the build
	// produced, and the real byte length. Anything else and the binding check
	// fails, which is the point of it.
	rootHashRaw, err := os.ReadFile(filepath.Join(dir, "os-core.roothash"))
	if err != nil {
		t.Fatalf("read os-core.roothash (VERITY-01 must have produced it): %v", err)
	}
	rootHash := strings.ToLower(strings.TrimSpace(string(rootHashRaw)))
	st, err := os.Stat(squashfsPath)
	if err != nil {
		t.Fatalf("stat squashfs: %v", err)
	}
	manifest := &osdist.StableManifest{
		Channel:    "stable",
		Latest:     "e2e",
		MinEpoch:   0,
		RootHash:   rootHash,
		Size:       st.Size(),
		ReleasedAt: time.Unix(0, 0).UTC(),
		Path:       "os/e2e/os-core.squashfs",
	}
	manifestBytes, err := manifest.Canonical()
	if err != nil {
		t.Fatalf("canonical manifest: %v", err)
	}
	writeSig := func(path string, over []byte) {
		data, err := signing.MarshalSig(signing.Signature{
			Algorithm: signing.AlgorithmID,
			KeyID:     "netboot-e2e-test",
			SigBytes:  signing.Sign(releasePriv, over),
		})
		if err != nil {
			t.Fatalf("marshal .sig for %s: %v", path, err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	manifestPath := filepath.Join(dir, "stable.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		t.Fatalf("write stable.json: %v", err)
	}
	writeSig(manifestPath+".sig", manifestBytes)

	// The image signature, over the ImagePayload the manifest describes —
	// unmarshalled from the manifest bytes exactly as the verifier will do.
	var payload verify.ImagePayload
	if err := json.Unmarshal(manifestBytes, &payload); err != nil {
		t.Fatalf("unmarshal image payload: %v", err)
	}
	canonicalPayload, err := signing.Canonical(payload)
	if err != nil {
		t.Fatalf("canonical image payload: %v", err)
	}
	writeSig(squashfsPath+".sig", canonicalPayload)

	// MUTATION-PROVEN, not assumed: with one byte of this .sig flipped (an 'A'
	// to a 'B', so the file still parses and the package still compiles — a
	// mutation that only breaks the build proves nothing), a full harness run
	// stops here:
	//
	//	── step: verify-verity
	//	── step: verify-squashfs
	//	step "verify-squashfs" failed: netboot-verify: squashfs signature
	//	verification failed against the certified release key
	//	--- FAIL: TestNetbootInstall_RealPipeline_E2E (1.84s)
	//
	// i.e. the pipeline aborts before partition, with the target disk untouched.
	// Worth pinning in a comment because the check this replaced could not fail
	// for a REAL artifact and could not pass for one either — a green E2E run is
	// only evidence of a signature check if a broken signature turns it red.
	t.Logf("signed %s (%d bytes, roothash %s) with a throwaway two-tier PKI: "+
		"root→release cert→release key over canonical(ImagePayload) and "+
		"canonical(ManifestPayload), i.e. the shape cmd/sign emits",
		squashfsPath, st.Size(), rootHash)

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
	// The signature chain is checked AFTER verity, because the root hash verity
	// just validated is what binds the signed ImagePayload to these bytes.
	step("verify-squashfs", func() error {
		var rh string
		if verity != nil {
			rh = verity.rootHashHex
		}
		return svc.verifyNetbootSquashfs(squashfsPath, svc.netbootVerifyConfig(), rh)
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
	// OWNSTATE-01. The initramfs can only bind a directory that ALREADY exists
	// on the partition — until its final rebind that partition is $rootmnt
	// mounted read-only, and a mkdir into it is the failure
	// roadmap/BOOT-FOUR-ERRORS.md is about. So the installer is the only thing
	// that can create them, and this is the step that does it.
	step("state-dirs", func() error {
		return svc.createOwnerStateDirs(ctx, netbootInstallMount)
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

	// OWNSTATE-01, verified on the REAL filesystem before it is unmounted. The
	// mocked test asserts the argv; this asserts the result — the directories
	// exist, with the modes claimed, on the ext4 the initramfs will bind out of.
	// Without them the hook's `[ -d ]` gate fails on every boot and the owner's
	// account goes back to living in RAM, silently.
	for _, d := range ownerStateDirs {
		p := filepath.Join(netbootInstallMount, d.rel)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("OWNSTATE-01: %s does not exist on the installed disk: %v\n"+
				"scripts/initramfs/vulos-live binds this directory out of the overlay so "+
				"the owner's account survives a reboot; it cannot create it, because that "+
				"partition is read-only until the rebind. Without it the box loses its "+
				"owner on every restart.", p, err)
		}
		if !fi.IsDir() {
			t.Fatalf("OWNSTATE-01: %s is not a directory", p)
		}
		var want os.FileMode
		if _, err := fmt.Sscanf(d.mode, "%o", &want); err != nil {
			t.Fatalf("ownerStateDirs mode %q is not octal: %v", d.mode, err)
		}
		if got := fi.Mode().Perm(); got != want.Perm() {
			t.Errorf("OWNSTATE-01: %s has mode %04o, want %04o. This tree holds auth.db "+
				"(the owner's password hash and live sessions), auth.key (the secret every "+
				"session cookie is signed with), the box's Ed25519 peering private key and "+
				"the credential vaults. None of it is encrypted at rest — this project "+
				"ships no LUKS on any boot path — so the directory mode is the only access "+
				"control there is.", p, got, want.Perm())
		}
	}
	t.Logf("OWNSTATE-01: owner-state dirs present on the installed partition with the "+
		"declared modes: %v", ownerStateDirs)

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
