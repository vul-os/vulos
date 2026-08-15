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
	if mode == "installed" {
		slot := filepath.Join(rootmnt, "var/cache/vulos/slot-b")
		if err := os.MkdirAll(slot, 0o755); err != nil {
			t.Fatalf("mkdir slot: %v", err)
		}
		write(filepath.Join(slot, "os-core.squashfs"), "", 0o644)
		write(filepath.Join(rootmnt, "var/cache/vulos/boot-state.json"), `{"active":"b"}`, 0o644)
	}
	cmdline := filepath.Join(dir, "cmdline")
	// No trailing newline: that is how the kernel presents /proc/cmdline, and
	// this hook has already been bitten by it once.
	write(cmdline, "root=LABEL=VULOS-LIVE-DATA ro vulos.live=1 quiet splash", 0o644)

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
// does belong here — and its position. Deleting the two dead lines above it must
// not be allowed to take this with them, and nothing may be added after it: once
// $rootmnt is the overlay, initramfs-tools' init immediately moves /run onto it
// and shadows anything bound underneath.
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
