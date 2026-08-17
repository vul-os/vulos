package docsref

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scripts/initramfs/vulos-live is a shell script, so it can be RUN rather than
// only read — and running it is the only thing that has ever found a bug in it.
//
// WHAT THIS EXISTS TO STOP RECURRING.
//
// Every Vulos boot ever produced opened with four errors, before anything else,
// deterministically:
//
//	mount: No such file or directory
//	mount: invalid option --
//	mkdir: /root/run: Read-only file system
//	mount: No such file or directory
//
// The first two are Debian's: /scripts/init-bottom/udev runs
// `mount -n -o move /dev "${rootmnt}/dev" || mount -n --move /dev "${rootmnt}/dev"`,
// klibc's mount cannot find the target and then cannot parse the long option in
// the fallback. The last two were ours: this hook did
//
//	mkdir -p "${rootmnt}/run/vulos"
//	mount -o bind /run/vulos "${rootmnt}/run/vulos"
//
// BEFORE it rebound $rootmnt to the overlay. Everything in the init-bottom stage
// runs with $rootmnt still pointing at the DATA PARTITION — the read-only ext4
// that root=LABEL=VULOS-LIVE-DATA named — and not at the OS root, because this
// hook is the thing that makes $rootmnt the OS root, halfway through that stage.
// A write into $rootmnt before that moment lands on a filesystem that is
// read-only, is not the root, and is about to be shadowed. It cannot work, and
// for the whole life of this project it did not.
//
// WHY NOTHING CAUGHT IT. The installed boot entry carries `quiet splash`, and
// initramfs-tools' log_begin_msg/log_warning_msg return immediately when
// quiet=y — so every deliberate line this hook prints is invisible on a real
// boot, and the only lines anyone ever saw from it were the raw klibc stderr of
// commands that failed. Four errors at the top of every log is how a log stops
// being read. scripts/test-vulos-live-cmdline.sh covers the cmdline and
// slot-marker logic but stops at END-TESTABLE, well above the mount sequence.
//
// So this test drives the WHOLE file, unmodified but for the one absolute
// `. /scripts/functions` source it cannot satisfy off a real initramfs, against
// a fabricated read-only $rootmnt with klibc-shaped mount and mkdir stubs, and
// asserts on what it actually did.

// klibcMount is a deliberately faithful stand-in for the initramfs `mount`,
// which is klibc's — not util-linux's and not busybox's. The two properties that
// matter, both of which have cost this project a real bug:
//
//   - it has no long options at all, so `--move`/`--bind` are usage errors
//   - it requires BOTH a device and a directory, always, so the util-linux
//     `mount -o remount,rw /root` spelling silently does nothing
//
// %[1]s is the harness tmpdir; /run is remapped into it because a test may not
// write to the host's real /run.
const klibcMount = `#!/bin/sh
T="%[1]s"
printf '%%s\n' "mount $*" >> "$T/mount.log"
opts=""; typ=""; pos=""
while [ $# -gt 0 ]; do
  case "$1" in
    --*) echo "mount: invalid option --" >&2; exit 1 ;;
    -o)  opts="$2"; shift 2 ;;
    -t)  typ="$2"; shift 2 ;;
    -n|-r|-w|-f|-i) shift ;;
    -*)  echo "mount: invalid option -- ${1#-}" >&2; exit 1 ;;
    *)   pos="$pos $1"; shift ;;
  esac
done
set -- $pos
if [ $# -ne 2 ]; then
  echo "Usage: mount [-r] [-w] [-o options] [-t type] [-f] [-i] [-n] device directory" >&2
  exit 1
fi
dir="$2"
case "$dir" in /run|/run/*) dir="$T${dir}" ;; esac
if [ ! -d "$dir" ]; then echo "mount: No such file or directory" >&2; exit 1; fi
printf '%%s\n' "$typ|$opts|$1|$2" >> "$T/mount.ok"
exit 0
`

// klibcMkdir refuses any path under $rootmnt, which is how the real one behaves:
// the kernel cmdline says `ro`, so the data partition is mounted read-only and
// EROFS is what a write to it returns. %[1]s is the tmpdir, %[2]s the real
// mkdir binary, %[3]s the fabricated $rootmnt.
const klibcMkdir = `#!/bin/sh
T="%[1]s"; REAL="%[2]s"; RO="%[3]s"
printf '%%s\n' "mkdir $*" >> "$T/mkdir.log"
p=""
for a in "$@"; do case "$a" in -*) ;; *) p="$p $a" ;; esac; done
rc=0
for d in $p; do
  case "$d" in
    "$RO"|"$RO"/*)
      printf '%%s\n' "$d" >> "$T/mkdir.rejected"
      echo "mkdir: $d: Read-only file system" >&2; rc=1; continue ;;
  esac
  t="$d"; case "$d" in /run|/run/*) t="$T${d}" ;; esac
  "$REAL" -p "$t" || rc=1
done
exit $rc
`

const fakeInitramfsFunctions = `log_begin_msg()   { echo "Begin: $*"; }
log_end_msg()     { echo "done."; }
log_warning_msg() { echo "Warning: $*"; }
log_failure_msg() { echo "Failure: $*"; }
panic()           { echo "PANIC: $*"; exit 99; }
`

// liveHookRun is the outcome of one driven boot of the hook.
type liveHookRun struct {
	rootmnt  string
	output   string
	exitCode int
	mounts   []string // raw argv lines, in order
	mkdirs   []string // raw argv lines, in order
	rejected []string // mkdir targets the read-only $rootmnt refused
}

// driveLiveHook runs scripts/initramfs/vulos-live to completion.
//
// mode "live" fabricates a live-USB data partition (image.squashfs and nothing
// else); mode "installed" fabricates a netboot-installed disk with an A/B slot
// layout and a boot-state.json naming slot b, which is the path that also
// exercises record_booted_slot's klibc remount.
//
// mode "netboot" is "installed" pinned to slot a AND driven with the EXACT
// kernel command line backend/services/installer/netboot_install.go writes onto
// the ESP — read out of that file rather than restated here, so the two cannot
// drift. It is the only mode that reproduces a real netboot-installed boot.
func driveLiveHook(t *testing.T, mode string) liveHookRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell harness")
	}
	sh, err := exec.LookPath("dash")
	if err != nil {
		sh = "/bin/sh" // dash on Debian, and POSIX enough elsewhere
	}
	realMkdir, err := exec.LookPath("mkdir")
	if err != nil {
		t.Fatalf("no mkdir on PATH: %v", err)
	}

	dir := t.TempDir()
	rootmnt := filepath.Join(dir, "rootmnt")
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{rootmnt, bin, filepath.Join(dir, "run")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	write(filepath.Join(dir, "functions"), fakeInitramfsFunctions, 0o644)
	write(filepath.Join(bin, "mount"), fmt.Sprintf(klibcMount, dir), 0o755)
	write(filepath.Join(bin, "mkdir"), fmt.Sprintf(klibcMkdir, dir, realMkdir, rootmnt), 0o755)
	write(filepath.Join(bin, "losetup"), "#!/bin/sh\ncase \"$1\" in -f) echo /dev/loop0;; esac\nexit 0\n", 0o755)
	for _, s := range []string{"modprobe", "sync", "udevadm"} {
		write(filepath.Join(bin, s), "#!/bin/sh\nexit 0\n", 0o755)
	}
	// veritysetup and vulos-verify-sig are deliberately NOT stubbed: their
	// absence is what a CI/live-USB image looks like, and it puts the hook on
	// the documented unverified-loop-mount fallback rather than a path this
	// harness would have to fake a Merkle tree for.

	write(filepath.Join(rootmnt, "image.squashfs"), "", 0o644)
	mountsFile := filepath.Join(dir, "mounts")
	write(mountsFile, fmt.Sprintf("/dev/vda2 %s ext4 ro 0 0\n", rootmnt), 0o644)
	// No trailing newline on the cmdline file: that is how the kernel presents
	// /proc/cmdline, and this hook has already been bitten by it once.
	cmdlineText := "root=LABEL=VULOS-LIVE-DATA ro vulos.live=1 quiet splash"
	switch mode {
	case "installed":
		slot := filepath.Join(rootmnt, "var/cache/vulos/slot-b")
		if err := os.MkdirAll(slot, 0o755); err != nil {
			t.Fatalf("mkdir slot: %v", err)
		}
		write(filepath.Join(slot, "os-core.squashfs"), "", 0o644)
		write(filepath.Join(rootmnt, "var/cache/vulos/boot-state.json"), `{"active":"b"}`, 0o644)
	case "netboot":
		slot := filepath.Join(rootmnt, "var/cache/vulos/slot-a")
		if err := os.MkdirAll(slot, 0o755); err != nil {
			t.Fatalf("mkdir slot: %v", err)
		}
		write(filepath.Join(slot, "os-core.squashfs"), "", 0o644)
		write(filepath.Join(rootmnt, "var/cache/vulos/boot-state.json"), `{"active":"a"}`, 0o644)
		// OWNSTATE-01: the owner-state directories the netboot installer's
		// `state-dirs` step lays down. They are fabricated from the installer's
		// own list rather than restated, so a disk layout change cannot leave
		// this harness driving a shape the installer no longer produces.
		for _, rel := range netbootInstallerStateDirs(t) {
			if err := os.MkdirAll(filepath.Join(rootmnt, rel), 0o755); err != nil {
				t.Fatalf("mkdir owner-state dir %s: %v", rel, err)
			}
		}
		cmdlineText = installedDiskKernelCmdline(t)
	}
	cmdline := filepath.Join(dir, "cmdline")
	write(cmdline, cmdlineText, 0o644)

	src := readRepoFile(t, initramfsH)
	const sourceLine = ". /scripts/functions"
	if !strings.Contains(src, sourceLine) {
		t.Fatalf("%s no longer sources %s; this harness is driving something it does not understand", initramfsH, sourceLine)
	}
	hook := filepath.Join(dir, "hook")
	write(hook, strings.Replace(src, sourceLine, ". "+filepath.Join(dir, "functions"), 1), 0o755)

	cmd := exec.Command(sh, hook)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"rootmnt="+rootmnt,
		"VULOS_CMDLINE_FILE="+cmdline,
		"VULOS_MOUNTS_FILE="+mountsFile,
	)
	out, runErr := cmd.CombinedOutput()
	code := 0
	if ee, ok := runErr.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if runErr != nil {
		t.Fatalf("running %s: %v", initramfsH, runErr)
	}

	readLines := func(name string) []string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil
		}
		var out []string
		for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			if l != "" {
				out = append(out, l)
			}
		}
		return out
	}

	return liveHookRun{
		rootmnt:  rootmnt,
		output:   string(out),
		exitCode: code,
		mounts:   readLines("mount.log"),
		mkdirs:   readLines("mkdir.log"),
		rejected: readLines("mkdir.rejected"),
	}
}

// assertHarnessActuallyRanTheHook is the anti-hollow-gate check. Every assertion
// below is of the form "the hook did NOT do X", and a harness that failed to
// start the hook at all satisfies every one of them. So pin what a real run
// must have done, before believing anything it did not do.
func (r liveHookRun) assertHarnessActuallyRanTheHook(t *testing.T) {
	t.Helper()
	if r.exitCode != 0 {
		t.Fatalf("hook exited %d, so nothing below is evidence of anything.\n%s", r.exitCode, r.output)
	}
	want := []string{"-t squashfs", "-t tmpfs", "-t overlay"}
	joined := strings.Join(r.mounts, "\n")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Fatalf("the hook never issued a %q mount, so it did not reach the mount "+
				"sequence this test is about.\nmounts:\n%s\noutput:\n%s", w, joined, r.output)
		}
	}
	if len(r.mkdirs) < 3 {
		t.Fatalf("only %d mkdir calls; the hook did not build the overlay layout.\n%s",
			len(r.mkdirs), r.output)
	}
}

// TestLiveHookNeverWritesIntoTheReadOnlyDataPartition is the guard for the two
// errors this file's history is about. Any mkdir into $rootmnt during
// init-bottom is a write to the read-only data partition, and it fails.
func TestLiveHookNeverWritesIntoTheReadOnlyDataPartition(t *testing.T) {
	for _, mode := range []string{"live", "installed"} {
		t.Run(mode, func(t *testing.T) {
			r := driveLiveHook(t, mode)
			r.assertHarnessActuallyRanTheHook(t)

			if len(r.rejected) > 0 {
				t.Errorf("%s tried to mkdir inside $rootmnt while it is still the "+
					"READ-ONLY DATA PARTITION: %v\nEvery one of these prints "+
					"'mkdir: <path>: Read-only file system' on every real boot and "+
					"achieves nothing — $rootmnt only becomes the OS root at the "+
					"'mount -o bind $MERGED $rootmnt' at the end of this hook.\noutput:\n%s",
					initramfsH, r.rejected, r.output)
			}
			if strings.Contains(r.output, "Read-only file system") {
				t.Errorf("%s emitted a read-only-filesystem error on a boot that "+
					"is supposed to be clean:\n%s", initramfsH, r.output)
			}
			if strings.Contains(r.output, "mount: No such file or directory") {
				t.Errorf("%s mounted onto a target that does not exist. On a real "+
					"boot this is a SILENT failed mount — stderr is all anyone sees, "+
					"because `quiet splash` suppresses every log_*_msg in this "+
					"file:\n%s", initramfsH, r.output)
			}
			if strings.Contains(r.output, "invalid option --") {
				t.Errorf("%s passed an option the initramfs `mount` (klibc's) does "+
					"not have. It has no long options at all:\n%s", initramfsH, r.output)
			}
		})
	}
}

// TestLiveHookBindsTheOverlayOverRootmntLast pins the one write to $rootmnt that
// does belong here — and its position — ON A LIVE BOOT. Deleting the two dead
// lines above it must not be allowed to take this with them, and on this path
// nothing may follow it: once $rootmnt is the overlay, initramfs-tools' init
// immediately moves /run onto it and shadows anything bound under ${rootmnt}/run.
//
// The netboot-INSTALLED path deliberately adds exactly one mount after the
// rebind — the on-disk /var/cache/vulos, which has to go ON TOP of the overlay
// and therefore cannot precede it. That is a different boot with a different
// invariant and it is pinned separately by
// TestNetbootInstalledDiskKeepsVarCacheVulosOnDisk below. Keeping the live
// assertion strict here is the point: the exception is one named path, not a
// licence to append mounts.
func TestLiveHookBindsTheOverlayOverRootmntLast(t *testing.T) {
	r := driveLiveHook(t, "live")
	r.assertHarnessActuallyRanTheHook(t)

	var touching []string
	for _, m := range r.mounts {
		if strings.Contains(m, r.rootmnt) {
			touching = append(touching, m)
		}
	}
	// The installed path also remounts $rootmnt rw/ro to write the booted-slot
	// marker; the live path here has no /var/cache/vulos, so the overlay bind is
	// the only mount that may name $rootmnt at all.
	if len(touching) != 1 {
		t.Fatalf("expected exactly one mount naming $rootmnt on a live boot, got %d: %v\n"+
			"Anything else here is either a write to the read-only data partition or a "+
			"mount that initramfs-tools' `mount -n -o move /run ${rootmnt}/run` will "+
			"shadow moments later.\noutput:\n%s", len(touching), touching, r.output)
	}
	if !strings.Contains(touching[0], "-o bind") || !strings.HasSuffix(touching[0], r.rootmnt) {
		t.Errorf("the single $rootmnt mount is not the overlay bind: %q", touching[0])
	}
	if last := r.mounts[len(r.mounts)-1]; last != touching[0] {
		t.Errorf("the overlay bind onto $rootmnt is not the LAST mount the hook "+
			"performs; %q came after it. Anything mounted after the rebind is "+
			"operating on the OS root and belongs in the OS, not the initramfs.",
			last)
	}
}

// ─── The netboot-installed disk's /var/cache/vulos ───────────────────────────
//
// THE FAILURE THIS EXISTS TO STOP, stated plainly because it is invisible from
// every other angle in this repository:
//
// A netboot-installed disk boots `root=LABEL=vulos-root ro ... vulos.live=0
// vulos.squashfs=/var/cache/vulos/slot-a/os-core.squashfs`. `vulos.live=0`
// ACTIVATES this hook — cmdline_has matches the KEY= form, deliberately and
// necessarily, because the OS on that machine lives INSIDE the squashfs the
// same partition carries, and nothing but this hook mounts it. So that disk
// boots the overlay, and the ext4 that holds /var/cache/vulos/slot-* is left
// mounted at $rootmnt *underneath* the `mount -o bind $MERGED $rootmnt` at the
// end of this hook — shadowed, and unreachable by path for the rest of the
// machine's life.
//
// Nothing else remounts it. There is no fstab on this image, no .mount unit
// anywhere in the tree, and backend/cmd/init/main.go's mountAll() mounts only
// the pseudo-filesystems plus LABEL=vulos-data at ~/.vulos. So after switch_root
// the OS's /var/cache/vulos resolves inside the overlay: squashfs lower, tmpfs
// upper. Every write the running OS makes there — the OSDIST-03 boot counter,
// MarkHealthy, OTA staging into the inactive slot — lands in RAM and is gone on
// the next boot, while the initramfs keeps reading the untouched on-disk
// boot-state.json. A box would look healthy, roll back nothing, and lose its
// update state on every restart.
//
// WHY NOTHING CAUGHT IT. Every unit test of the slot manager runs against a real
// directory in a tmpdir, so persistence is assumed rather than exercised.
// scripts/netboot-install-smoke.sh Phase 4 flips boot-state.json and reads
// booted-slot by LOOP-MOUNTING THE IMAGE FROM THE HOST — it proves the initramfs
// can read the file and that the initramfs (pre-rebind, so on the real ext4) can
// write the marker. It never asks the RUNNING OS to write anything. And the
// plain `--disk` install is genuinely unaffected — `rw`, no vulos.live, the hook
// exits at its gate — so the one install path that is easy to test is the one
// that was never broken.
//
// So the assertion has to be about MOUNT TOPOLOGY, and it has to be made by
// running the hook.

// installedDiskKernelCmdline returns the kernel command line the NETB-03
// installer actually writes, read out of the installer source rather than
// restated here. If that options line changes, this harness changes with it —
// which is the whole point, since the defect this guards was created by what
// that line says.
func installedDiskKernelCmdline(t *testing.T) string {
	t.Helper()
	const installerGo = "backend/services/installer/netboot_install.go"
	src := readRepoFile(t, installerGo)
	const marker = `"options root=`
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("%s no longer contains an %s… loader-entry options line; this test is "+
			"driving a command line the installer does not write", installerGo, marker)
	}
	rest := src[i+1:]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("%s: unterminated options string literal", installerGo)
	}
	line := strings.TrimPrefix(rest[:j], "options ")
	for _, want := range []string{"vulos.live=", "vulos.squashfs=/var/cache/vulos/slot-"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the installer's kernel command line %q no longer contains %q, so this "+
				"test would be driving a boot that is not the one it is about", line, want)
		}
	}
	return line
}

// mountArgs splits a logged `mount …` argv line into its two positional
// arguments (klibc's mount requires exactly two, always: device and directory).
func mountArgs(line string) (source, target string) {
	f := strings.Fields(line)
	if len(f) < 2 {
		return "", ""
	}
	return f[len(f)-2], f[len(f)-1]
}

// TestNetbootInstalledDiskKeepsVarCacheVulosOnDisk is the guard. It drives the
// whole hook with the installer's own command line against a fabricated
// netboot-installed disk and asserts the resulting mount topology, because
// topology is the only thing that decides where a write to /var/cache/vulos
// lands.
func TestNetbootInstalledDiskKeepsVarCacheVulosOnDisk(t *testing.T) {
	r := driveLiveHook(t, "netboot")
	r.assertHarnessActuallyRanTheHook(t)

	// The hook must actually be on the slot path, or nothing below is about the
	// boot this test names.
	if !strings.Contains(r.output, "/var/cache/vulos/slot-a/os-core.squashfs") {
		t.Fatalf("the hook did not select the on-disk slot image, so this run is not a "+
			"netboot-installed boot at all:\n%s", r.output)
	}

	cacheDir := r.rootmnt + "/var/cache/vulos"

	bind, expose := -1, -1
	for i, m := range r.mounts {
		src, tgt := mountArgs(m)
		switch {
		case tgt == r.rootmnt && strings.Contains(m, "-o bind"):
			bind = i
		case tgt == cacheDir && strings.Contains(m, "-o bind"):
			expose = i
			if strings.HasPrefix(src, r.rootmnt) {
				t.Errorf("the mount that re-exposes %s takes its source from %q, which is "+
					"UNDER the overlay bind and therefore resolves to the overlay's own "+
					"empty directory, not the disk. The on-disk subtree has to be captured "+
					"at a path outside $rootmnt BEFORE the rebind shadows it.", cacheDir, src)
			}
		}
	}

	if bind < 0 {
		t.Fatalf("the hook never bound the overlay onto $rootmnt:\n%v", r.mounts)
	}
	if expose < 0 {
		t.Fatalf(`%s leaves /var/cache/vulos INSIDE THE OVERLAY on a netboot-installed disk.

The ext4 that holds /var/cache/vulos/slot-* is mounted at $rootmnt and is then
shadowed by "mount -o bind $MERGED $rootmnt". No mount targets %s
afterwards, and nothing outside the initramfs remounts that partition — there is
no fstab and no .mount unit on this image. So the running OS's boot counter,
MarkHealthy and OTA staging all write into the overlay's tmpfs upper layer and
vanish on reboot, while the initramfs goes on reading the stale on-disk
boot-state.json.

mounts the hook performed, in order:
%s
output:
%s`, initramfsH, cacheDir, strings.Join(r.mounts, "\n"), r.output)
	}
	if expose < bind {
		t.Errorf("the on-disk /var/cache/vulos is mounted BEFORE the overlay bind onto "+
			"$rootmnt (index %d vs %d), so the rebind buries it immediately. klibc's mount "+
			"has no --rbind, so a plain bind of $MERGED does not carry submounts: this one "+
			"has to come after.", expose, bind)
	}

	// The partition must be left WRITABLE, or the bind above is a read-only
	// window onto the disk and the OS still cannot persist a thing.
	// record_booted_slot deliberately flips rw → write → ro, so the assertion is
	// on the LAST remount, not on any remount.
	lastRemount := ""
	for _, m := range r.mounts {
		if strings.Contains(m, "remount,") {
			lastRemount = m
		}
	}
	if lastRemount == "" {
		t.Errorf("no remount at all on a netboot-installed boot; the cmdline says `ro`, so " +
			"the disk is read-only and every write the OS makes to /var/cache/vulos fails.")
	} else if !strings.Contains(lastRemount, "remount,rw") {
		t.Errorf("the last remount is %q — the disk is left READ-ONLY. record_booted_slot "+
			"restores `ro` after writing its marker, so something has to remount rw again "+
			"for the OS's own writes to /var/cache/vulos to land.", lastRemount)
	} else if _, tgt := mountArgs(lastRemount); tgt != r.rootmnt {
		t.Errorf("the final remount targets %q, not $rootmnt; it cannot be remounting the "+
			"data partition.", tgt)
	}
}

// ─── The netboot-installed disk's OWNER ACCOUNT ──────────────────────────────
//
// WHAT WAS OBSERVED, on a real arm64 QEMU boot of a netboot-installed disk with
// dm-verity active — not reasoned from mount topology, seen:
//
//	/api/auth/status  →  {"has_users":false}
//	login as founder  →  401 invalid username or password
//	/root/.vulos/*    →  entirely recreated at reboot time
//
// The owner's account did not survive a reboot. The cause is the one NETB-03
// above is about, applied to a second subtree: `mount -o bind $MERGED $rootmnt`
// shadows the ext4, /root/.vulos resolves inside the overlay, and its tmpfs
// upper layer is RAM. auth.db, auth.key, db/instance.json, the peering Ed25519
// identity, the device key, the passkey/TOTP/credential vaults and the user's
// files all went with it, and so did /var/lib/vulos — the .setup-complete
// marker, the LAN TLS material and the signing epoch floor, which cmd/server
// hardcodes outside the data dir.
//
// WHY NOTHING CAUGHT IT. Every auth test constructs a store in a t.TempDir(), so
// persistence is assumed rather than exercised, and the two boot paths that are
// easy to test (--disk, and the live-USB where ephemerality is the design) are
// the two that were never broken.

// netbootInstallerStateDirs returns the partition-relative directories the
// netboot installer's `state-dirs` step creates, read out of the installer
// source rather than restated. The initramfs hook can only bind a directory
// that already exists on the disk, so if that list ever changes without this
// harness changing with it, the fabricated disk stops being the disk the
// installer produces and every assertion below becomes meaningless.
func netbootInstallerStateDirs(t *testing.T) []string {
	t.Helper()
	const installerGo = "backend/services/installer/netboot_install.go"
	src := readRepoFile(t, installerGo)
	const marker = "var ownerStateDirs = []struct {"
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("%s no longer declares %s; the initramfs hook binds directories this "+
			"installer is supposed to create, and nothing here knows which they are "+
			"any more", installerGo, marker)
	}
	// The declaration is `var ownerStateDirs = []struct { … }{ … }`: the field
	// list comes FIRST and is itself brace-terminated, so the literal's entries
	// only start after the `}{`. Scanning for the first `\n}` without skipping
	// that lands on the type's closing brace and parses an empty list — which is
	// how this helper failed the first time it ran.
	rest := src[i+len(marker):]
	open := strings.Index(rest, "}{")
	if open < 0 {
		t.Fatalf("%s: ownerStateDirs is no longer an anonymous-struct slice literal", installerGo)
	}
	rest = rest[open+2:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatalf("%s: unterminated ownerStateDirs literal", installerGo)
	}
	var out []string
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `{"`) {
			continue
		}
		q := strings.Index(line[1:], `"`)
		r := strings.Index(line[q+2:], `"`)
		if q < 0 || r < 0 {
			continue
		}
		out = append(out, line[q+2:q+2+r])
	}
	if len(out) == 0 {
		t.Fatalf("%s declares ownerStateDirs but this harness parsed no entries out of it", installerGo)
	}
	return out
}

// TestNetbootInstalledDiskKeepsTheOwnerAccountOnDisk is the guard for the
// observed defect. Like the /var/cache/vulos one it asserts MOUNT TOPOLOGY,
// because topology is the only thing that decides whether a write to
// /root/.vulos/db/auth.db reaches the disk or the RAM.
func TestNetbootInstalledDiskKeepsTheOwnerAccountOnDisk(t *testing.T) {
	r := driveLiveHook(t, "netboot")
	r.assertHarnessActuallyRanTheHook(t)

	if !strings.Contains(r.output, "/var/cache/vulos/slot-a/os-core.squashfs") {
		t.Fatalf("the hook did not select the on-disk slot image, so this run is not a "+
			"netboot-installed boot at all:\n%s", r.output)
	}

	bind := -1
	for i, m := range r.mounts {
		if _, tgt := mountArgs(m); tgt == r.rootmnt && strings.Contains(m, "-o bind") {
			bind = i
		}
	}
	if bind < 0 {
		t.Fatalf("the hook never bound the overlay onto $rootmnt:\n%v", r.mounts)
	}

	// Each subtree that has to come back out of the overlay, and what is lost
	// when it does not. The message is the diagnosis, because the only person
	// who will ever read it is looking at a box whose owner cannot log in.
	for _, tc := range []struct {
		rel  string
		lost string
	}{
		{"/root/.vulos", "the owner's account (auth.db), the session-signing secret " +
			"(auth.key), db/instance.json, the box's Ed25519 peering identity, the " +
			"device key, the passkey/TOTP/credential vaults and every file the user " +
			"stored"},
		{"/var/lib/vulos", "the .setup-complete marker (so the setup wizard runs " +
			"again on a box that already has an owner), the LAN TLS certificate and " +
			"key, and the signing epoch floor that stops an OTA downgrade"},
	} {
		target := r.rootmnt + tc.rel
		idx := -1
		for i, m := range r.mounts {
			src, tgt := mountArgs(m)
			if tgt != target || !strings.Contains(m, "-o bind") {
				continue
			}
			idx = i
			if strings.HasPrefix(src, r.rootmnt) {
				t.Errorf("the mount that re-exposes %s takes its source from %q, which is "+
					"UNDER the overlay bind and therefore resolves to the overlay's own "+
					"empty directory, not the disk. The on-disk subtree has to be captured "+
					"at a path outside $rootmnt BEFORE the rebind shadows it.", tc.rel, src)
			}
		}
		if idx < 0 {
			t.Errorf(`%s leaves %s INSIDE THE OVERLAY on a netboot-installed disk.

Nothing mounts %s after "mount -o bind $MERGED $rootmnt", and nothing outside
the initramfs remounts that partition — there is no fstab and no .mount unit on
this image. So every write the running OS makes there lands in the overlay's
tmpfs upper layer and is gone on the next boot. What that costs the user:
%s.

This was OBSERVED, not inferred: an owner account created on a real
netboot-installed arm64 boot was gone after a reboot, /api/auth/status answered
{"has_users":false}, and the login 401'd.

mounts the hook performed, in order:
%s
output:
%s`, initramfsH, tc.rel, target, tc.lost, strings.Join(r.mounts, "\n"), r.output)
			continue
		}
		if idx < bind {
			t.Errorf("the on-disk %s is mounted BEFORE the overlay bind onto $rootmnt "+
				"(index %d vs %d), so the rebind buries it immediately. klibc's mount has "+
				"no --rbind, so a plain bind of $MERGED does not carry submounts: this one "+
				"has to come after.", tc.rel, idx, bind)
		}
	}

	// The app directory must NOT come with it. roadmap/APP-DIR-PERSISTENCE.md
	// measured why: 13 of the 16 installable registry entries are Flatpak SYSTEM
	// installs whose payload lands in /var/lib/flatpak, outside ~/.vulos and
	// still in the tmpfs. A surviving manifest above a dead payload is strictly
	// worse than no manifest — AppStore.RealisedVersions() has no flatpak
	// liveness check, so the reconciler reads it as realised, plans nothing, and
	// the app stays broken forever instead of being re-installed.
	appsDir := r.rootmnt + "/root/.vulos/apps"
	appsIdx := -1
	for i, m := range r.mounts {
		if _, tgt := mountArgs(m); tgt == appsDir {
			appsIdx = i
			if !strings.Contains(m, "-t tmpfs") {
				t.Errorf("the mount over %s is %q, not a tmpfs. Anything else leaves the "+
					"installed-app manifests on the disk while their Flatpak/apt payload "+
					"stays in RAM.", appsDir, m)
			}
		}
	}
	if appsIdx < 0 {
		t.Errorf(`%s now persists /root/.vulos/apps along with the owner's account.

roadmap/APP-DIR-PERSISTENCE.md stopped exactly this on measurement: for 13 of
the 16 installable registry entries the payload is a Flatpak SYSTEM install in
/var/lib/flatpak, which is NOT persisted and dies with the tmpfs. A manifest
that outlives its payload makes the App Hub list an app that cannot launch, and
AppStore.RealisedVersions() reports it as realised so nothing ever re-installs
it. Today the manifest dies with the payload and the box recovers. Keep it that
way: mount a tmpfs back over %s after the state bind.

mounts, in order:
%s`, initramfsH, appsDir, strings.Join(r.mounts, "\n"))
	} else {
		homeIdx := -1
		for i, m := range r.mounts {
			if _, tgt := mountArgs(m); tgt == r.rootmnt+"/root/.vulos" && strings.Contains(m, "-o bind") {
				homeIdx = i
			}
		}
		if homeIdx >= 0 && appsIdx < homeIdx {
			t.Errorf("the tmpfs over %s is mounted BEFORE the /root/.vulos bind (index %d "+
				"vs %d), so the bind covers it and the app manifests land on the disk "+
				"after all.", appsDir, appsIdx, homeIdx)
		}
	}

	// And the partition has to be left WRITABLE, or all of the above is a
	// read-only window onto the disk and the account still cannot be saved.
	lastRemount := ""
	for _, m := range r.mounts {
		if strings.Contains(m, "remount,") {
			lastRemount = m
		}
	}
	if lastRemount == "" {
		t.Errorf("no remount at all on a netboot-installed boot; the cmdline says `ro`, so " +
			"the disk is read-only and creating the owner account fails at the first write.")
	} else if !strings.Contains(lastRemount, "remount,rw") {
		t.Errorf("the last remount is %q — the disk is left READ-ONLY, so /root/.vulos is a "+
			"read-only window and the owner account cannot be written at all.", lastRemount)
	}
}

// TestLiveBootGetsNoOwnerStateMounts pins the other half: a live-USB and a live
// ESP have nowhere to persist anything, and the honest behaviour there is to add
// NOT ONE mount rather than to pretend. The gate is the /var/cache/vulos/slot-*
// layout only.
func TestLiveBootGetsNoOwnerStateMounts(t *testing.T) {
	r := driveLiveHook(t, "live")
	r.assertHarnessActuallyRanTheHook(t)

	for _, rel := range []string{"/root/.vulos", "/root/.vulos/apps", "/var/lib/vulos"} {
		for _, m := range r.mounts {
			if _, tgt := mountArgs(m); tgt == r.rootmnt+rel {
				t.Errorf("a live boot mounted %s (%q). There is no durable storage on that "+
					"path — the whole medium is a squashfs plus a tmpfs — so a mount here "+
					"either fails or persists onto the USB stick the user is about to pull "+
					"out. The gate is the netboot-installed slot layout, and it must stay "+
					"that way.", rel, m)
			}
		}
	}
}
