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

// ─── Does an app installed with apt-get survive a reboot? ────────────────────
//
// THE QUESTION, and why it cannot be answered for "Vulos" as one thing.
//
// backend/services/appnet/registry.go runs a registry entry's `install` string
// with cmd.Dir = <appDir>. 29 of the 56 registry entries run `apt-get install`
// there, and apt-get ignores the working directory entirely: it writes to /usr,
// /var/lib/dpkg, /var/lib/apt, /etc and /opt, i.e. the SYSTEM tree. So the
// question "does it survive a reboot" is precisely the question "is the system
// tree on this box persistent", and THAT differs per boot path.
//
// This project ships five kernel command lines, from four writers. Each one is
// read out of its own source below rather than restated, so this test cannot
// drift from what the installers actually write:
//
//	build.sh --live         root=LABEL=VULOS-LIVE-DATA ro  vulos.live=1
//	esp.go (live re-flash)  root=LABEL=vulos-root      ro  vulos.live=1 toram
//	netboot_install.go      root=LABEL=vulos-root      ro  vulos.live=0 …slot-a…
//	build.sh --disk         root=LABEL=vulos-root      rw  init=/sbin/vulos-init
//	disk.go                 root=LABEL=vulos-root      rw  init=/sbin/vulos-init
//
// cmdline_has in scripts/initramfs/vulos-live matches BOTH the bare token and
// the KEY= form, so the first three activate the live hook and the last two do
// not. That single fact splits the answer in two:
//
//   - hook active  → / is an overlay: squashfs lower (read-only, dm-verity
//     sealed) + tmpfs upper IN RAM. Every byte apt writes lands in the upper
//     layer and is destroyed at power-off.
//   - hook inactive → the hook exits at its gate, / is the real ext4 mounted
//     rw, and apt writes to disk exactly as on any Debian box.
//
// WHY THIS IS NOT AN apt QUESTION. Nothing above mentions apt. flatpak
// (/var/lib/flatpak), the static-download path (which writes into the app dir
// under $HOME/.vulos), and apt all resolve to the same two answers on the same
// two sets of paths. Removing apt entries from registry.json would not change a
// single line of this file's output.
//
// The tests below establish the three things the answer rests on, by DERIVING
// each from the repository rather than asserting it:
//
//  1. which command lines activate the hook (TestBootPathCmdlinesStillSplitTheSameWay)
//  2. what the hook leaves reachable from persistent storage
//     (TestOnlyVarCacheVulosIsRescuedFromTheOverlay, and the --disk gate in
//     TestDiskInstallCmdlineLeavesTheHookInert)
//  3. that the one mechanism which could persist the app data directory runs
//     only on the path that already persists, and looks for a filesystem label
//     nothing in this repository creates
//     (TestVulosInitRunsOnlyWhereTheRootIsAlreadyPersistent,
//     TestNothingCreatesTheDataPartitionLabel)
//
// Findings and the per-path verdict: roadmap/APT-INSTALL-PERSISTENCE.md.
// A runnable end-to-end probe for a real booted box: scripts/probe-install-persistence.sh.

// aptWriteTargets are the directories `apt-get install` actually writes into.
// None of them is the app directory registry.go points cmd.Dir at.
var aptWriteTargets = []string{
	"/usr",
	"/var/lib/dpkg",
	"/var/lib/apt",
	"/etc",
	"/opt",
}

// loaderEntry is one kernel command line this repository writes, together with
// the file that writes it.
type loaderEntry struct {
	source  string
	cmdline string
}

// loaderEntryCmdlines extracts every systemd-boot `options root=…` line a file
// writes. It is deliberately syntax-agnostic (it reads Go string literals and
// shell printf format strings alike) because the five command lines live in
// three languages and the only thing they share is that exact prefix.
//
// The terminator is whichever comes first of: a literal `\n` (both a Go escape
// and a shell printf escape), a double quote, or a real newline.
func loaderEntryCmdlines(t *testing.T, rel string) []loaderEntry {
	t.Helper()
	src := readRepoFile(t, rel)
	const marker = "options root="
	var out []loaderEntry
	for i := 0; ; {
		j := strings.Index(src[i:], marker)
		if j < 0 {
			break
		}
		start := i + j + len("options ")
		rest := src[start:]
		end := len(rest)
		for _, term := range []string{`\n`, `"`, "\n"} {
			if k := strings.Index(rest, term); k >= 0 && k < end {
				end = k
			}
		}
		out = append(out, loaderEntry{source: rel, cmdline: strings.TrimSpace(rest[:end])})
		i = start + end
	}
	if len(out) == 0 {
		t.Fatalf("%s no longer writes any `%s…` loader entry; this test is reasoning "+
			"about a boot path that has moved somewhere else", rel, marker)
	}
	return out
}

// allLoaderEntries reads every command line this repository writes, from every
// writer. If a sixth boot path appears and is not listed here, the count check
// below fails rather than this test silently ignoring it — a new boot path is
// exactly the event that would make the answer in the roadmap note wrong.
func allLoaderEntries(t *testing.T) []loaderEntry {
	t.Helper()
	var all []loaderEntry
	for _, f := range []string{
		"build.sh",
		"backend/internal/installer/disk.go",
		"backend/internal/installer/esp.go",
		"backend/services/installer/netboot_install.go",
	} {
		all = append(all, loaderEntryCmdlines(t, f)...)
	}
	return all
}

// cmdlineHas mirrors cmdline_has in scripts/initramfs/vulos-live: a token
// matches either bare or as KEY=<anything>. Getting this wrong in either
// direction is what produced the /var/cache/vulos defect, so it is spelled out
// rather than approximated with strings.Contains.
func cmdlineHas(cmdline, key string) bool {
	for _, tok := range strings.Fields(cmdline) {
		if tok == key || strings.HasPrefix(tok, key+"=") {
			return true
		}
	}
	return false
}

// TestBootPathCmdlinesStillSplitTheSameWay pins the fact the whole answer rests
// on: exactly which boot paths hand / to the initramfs overlay.
//
// It also pins the COUNT. A new installer that writes a sixth command line is
// a new boot path with its own persistence answer, and it must not be able to
// appear without someone revisiting roadmap/APT-INSTALL-PERSISTENCE.md.
func TestBootPathCmdlinesStillSplitTheSameWay(t *testing.T) {
	all := allLoaderEntries(t)
	if len(all) != 5 {
		var got []string
		for _, e := range all {
			got = append(got, e.source+": "+e.cmdline)
		}
		t.Fatalf(`this repository now writes %d kernel command lines, not the 5 that
roadmap/APT-INSTALL-PERSISTENCE.md answers for. Every command line is a boot
path, and each one persists an apt-get install or does not, on its own terms.

Read the new one, decide which side it falls on, and update the note.

%s`, len(all), strings.Join(got, "\n"))
	}

	var overlay, real []string
	for _, e := range all {
		if cmdlineHas(e.cmdline, "vulos.live") {
			overlay = append(overlay, e.source)
		} else {
			real = append(real, e.source)
		}
	}
	if len(overlay) != 3 {
		t.Errorf("expected 3 command lines to activate the live overlay (build.sh --live, "+
			"esp.go, netboot_install.go), got %d: %v.\nEach one is a boot whose / is a "+
			"tmpfs-backed overlay, so every apt-get install on it is destroyed at "+
			"power-off.", len(overlay), overlay)
	}
	if len(real) != 2 {
		t.Errorf("expected 2 command lines to leave the hook inert (build.sh --disk and "+
			"disk.go, the plain --disk install), got %d: %v.\nThose are the only boots "+
			"whose / is a real writable ext4 — the only ones where an apt-get install "+
			"survives a reboot.", len(real), real)
	}

	// The netboot-installed path is the one that LOOKS like a permanent
	// installation and is not. Name it explicitly so it cannot quietly move.
	for _, e := range all {
		if e.source != "backend/services/installer/netboot_install.go" {
			continue
		}
		if !cmdlineHas(e.cmdline, "vulos.live") {
			t.Errorf("the netboot installer no longer writes vulos.live, so the hook would "+
				"not run and it could not mount its own slot squashfs at all: %q", e.cmdline)
		}
		if !strings.Contains(e.cmdline, "root=LABEL=vulos-root ro") {
			t.Errorf("the netboot-installed disk no longer boots its root READ-ONLY: %q.\n"+
				"The `ro` is half of why the running OS cannot persist anything outside "+
				"the one subtree the hook re-exposes.", e.cmdline)
		}
	}
}

// TestVulosInitRunsOnlyWhereTheRootIsAlreadyPersistent is the finding that
// closes the "but surely something mounts a data partition" question.
//
// backend/cmd/init/main.go's mountDataPartition() is the ONLY code in the image
// that could put the box's data directory ($HOME/.vulos — databases, keys, and
// crucially the installed-app manifests) on persistent storage independently of
// the root filesystem. It lives in vulos-init, which is PID 1 only when the
// kernel command line says init=/sbin/vulos-init.
//
// And that token appears on exactly the two command lines that do NOT activate
// the overlay — the two boots whose root is already a writable ext4 and which
// therefore need it least. On all three overlay boots systemd is PID 1,
// vulos-init never executes, and nothing whatsoever mounts a data partition.
//
// The invariant asserted here is the biconditional, because it is what makes
// the statement checkable rather than anecdotal: a boot runs vulos-init if and
// only if its root is already persistent.
func TestVulosInitRunsOnlyWhereTheRootIsAlreadyPersistent(t *testing.T) {
	initToken := vulosInitToken(t)

	for _, e := range allLoaderEntries(t) {
		overlay := cmdlineHas(e.cmdline, "vulos.live")
		runsInit := strings.Contains(e.cmdline, initToken)
		if overlay && runsInit {
			t.Errorf(`%s now boots the OVERLAY *and* hands PID 1 to vulos-init:

  %s

That is a genuine change to this answer: mountDataPartition() would run on a
boot whose / is a tmpfs, and the box's data directory could become persistent
while /usr stays volatile. That combination is the "installed app that cannot
start" shape — the manifest under $HOME/.vulos survives, the apt-installed
binary under /usr does not. Re-read roadmap/APT-INSTALL-PERSISTENCE.md before
shipping it.`, e.source, e.cmdline)
		}
		if !overlay && !runsInit {
			t.Errorf("%s boots a real writable root but no longer runs vulos-init: %q.\n"+
				"mountAll(), the data partition mount and the whole BMINIT phase list "+
				"would not execute on that boot.", e.source, e.cmdline)
		}
	}
}

// vulosInitToken reads the init= token out of the installer that writes it,
// rather than restating "/sbin/vulos-init" here.
func vulosInitToken(t *testing.T) string {
	t.Helper()
	const diskGo = "backend/internal/installer/disk.go"
	for _, e := range loaderEntryCmdlines(t, diskGo) {
		for _, tok := range strings.Fields(e.cmdline) {
			if strings.HasPrefix(tok, "init=") {
				return tok
			}
		}
	}
	t.Fatalf("%s no longer writes an init= token, so there is no boot on which "+
		"backend/cmd/init/main.go runs at all", diskGo)
	return ""
}

// TestNothingCreatesTheDataPartitionLabel — mountDataPartition() looks up a
// filesystem by LABEL. Nothing in this repository ever creates a filesystem
// with that label.
//
// This is a CHARACTERISATION of the current state, deliberately. It passes
// today because the label has no producer; it fails the day someone gives it
// one — and that day the answer in roadmap/APT-INSTALL-PERSISTENCE.md changes,
// because a persistent $HOME/.vulos alongside a volatile /usr is exactly the
// "shows as installed, cannot start" failure the note warns about. Failing is
// the correct response to that change, not a nuisance.
//
// Both halves are read out of the sources: the label from cmd/init/main.go, the
// set of labels this project creates from every mkfs invocation in the build
// and the two installers.
func TestNothingCreatesTheDataPartitionLabel(t *testing.T) {
	label := dataPartitionLabel(t)

	producers := map[string][]string{}
	for _, rel := range []string{
		"build.sh",
		"backend/internal/installer/disk.go",
		"backend/internal/installer/esp.go",
		"backend/services/installer/netboot_install.go",
	} {
		for _, l := range mkfsLabels(t, rel) {
			producers[l] = append(producers[l], rel)
		}
	}
	if len(producers) == 0 {
		t.Fatal("found no mkfs label at all across build.sh and the installers; the scan " +
			"below is checking nothing")
	}

	if src, ok := producers[label]; ok {
		t.Errorf(`%s is now created by %v.

mountDataPartition() in backend/cmd/init/main.go will therefore find it and
mount it at the box data directory. That makes $HOME/.vulos — which holds every
installed app's app.json — persistent on whichever boots run vulos-init.

Check what that does to the answer in roadmap/APT-INSTALL-PERSISTENCE.md. If it
is now mounted on an OVERLAY boot, an apt-installed app keeps its manifest and
loses its binary, and the App Hub will list an app that cannot start.`, label, src)
	}

	// Positive control: the labels this project DOES create must still be
	// there, or the scan above proves nothing by finding nothing.
	for _, want := range []string{"vulos-root"} {
		if _, ok := producers[want]; !ok {
			var have []string
			for l := range producers {
				have = append(have, l)
			}
			t.Fatalf("the mkfs scan did not find the %q label, so it is not reading the "+
				"formatting commands it thinks it is. Labels found: %v", want, have)
		}
	}
}

// dataPartitionLabel reads the filesystem label mountDataPartition() looks up.
func dataPartitionLabel(t *testing.T) string {
	t.Helper()
	const initGo = "backend/cmd/init/main.go"
	src := readRepoFile(t, initGo)
	i := strings.Index(src, "func mountDataPartition()")
	if i < 0 {
		t.Fatalf("%s no longer has mountDataPartition(); the only code that could put the "+
			"box data directory on its own partition is gone, which is itself an answer "+
			"this test should not swallow", initGo)
	}
	body := src[i:]
	const marker = `const label = "`
	j := strings.Index(body, marker)
	if j < 0 {
		t.Fatalf("%s: mountDataPartition() no longer declares `%s…`", initGo, marker)
	}
	rest := body[j+len(marker):]
	k := strings.Index(rest, `"`)
	if k < 0 {
		t.Fatalf("%s: unterminated label literal", initGo)
	}
	return rest[:k]
}

// mkfsLabels returns every filesystem label a file creates, by scanning for the
// `-L` flag of mkfs.ext4 / mkfs.fat / mkfs.vfat. It handles the shell form
// (`mkfs.ext4 -q -F -L vulos-root "$IMG"`) and the Go exec form
// (`runCmd(ctx, "mkfs.ext4", "-F", "-L", "vulos-root", part)`) alike.
func mkfsLabels(t *testing.T, rel string) []string {
	t.Helper()
	src := readRepoFile(t, rel)
	var labels []string
	for _, line := range strings.Split(src, "\n") {
		if !strings.Contains(line, "mkfs.") {
			continue
		}
		if s := strings.TrimSpace(line); strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
			continue
		}
		fields := strings.FieldsFunc(line, func(r rune) bool {
			return r == ' ' || r == '\t' || r == ',' || r == '"' || r == '(' || r == ')'
		})
		for i, f := range fields {
			if (f == "-L" || f == "-n") && i+1 < len(fields) {
				v := fields[i+1]
				if strings.HasPrefix(v, "$") || strings.HasPrefix(v, "-") {
					continue
				}
				labels = append(labels, v)
			}
		}
	}
	return labels
}

// ─── What the hook actually leaves reachable ─────────────────────────────────

// rescuedSubtrees returns the paths under $rootmnt that a driven run of the
// hook mounts AFTER the overlay bind FROM A SOURCE OUTSIDE $rootmnt — i.e. the
// subtrees that are pulled back out of the overlay and onto persistent storage.
// Everything not in this list resolves inside the overlay, whose upper layer is
// the tmpfs.
//
// Two mounts are deliberately NOT counted, and the distinction is the whole
// point of classifying rather than just listing:
//
//   - a bind whose SOURCE is under $rootmnt. It reads from the overlay's own
//     empty directory, not the disk, so it persists nothing — that is mutation
//     3 of the NETB-03 work, and it must not be able to masquerade as a rescue.
//   - a tmpfs. That is the opposite operation: it puts a subtree back INTO RAM
//     on purpose (OWNSTATE-01 does it to /root/.vulos/apps). See
//     revolatilisedSubtrees.
func rescuedSubtrees(t *testing.T, r liveHookRun) []string {
	t.Helper()
	bind := -1
	for i, m := range r.mounts {
		if _, tgt := mountArgs(m); tgt == r.rootmnt && strings.Contains(m, "-o bind") {
			bind = i
		}
	}
	if bind < 0 {
		t.Fatalf("the hook never bound the overlay onto $rootmnt, so this run is not a "+
			"live-overlay boot at all:\n%v", r.mounts)
	}
	var out []string
	for _, m := range r.mounts[bind+1:] {
		src, tgt := mountArgs(m)
		if !strings.HasPrefix(tgt, r.rootmnt+"/") {
			continue
		}
		if !strings.Contains(m, "-o bind") || strings.HasPrefix(src, r.rootmnt) {
			continue
		}
		out = append(out, strings.TrimPrefix(tgt, r.rootmnt))
	}
	return out
}

// revolatilisedSubtrees is the counterpart: the paths under $rootmnt that the
// hook mounts a TMPFS over after the overlay bind, deliberately putting them
// back in RAM.
//
// There is exactly one today — /root/.vulos/apps — and it is there because
// roadmap/APP-DIR-PERSISTENCE.md measured that persisting an app manifest above
// a Flatpak payload that stays in RAM is strictly worse than persisting
// neither. Pinning this set exactly is what stops a future edit from quietly
// dropping that tmpfs and re-creating the ghost-app shape while every other
// assertion here stays green.
func revolatilisedSubtrees(t *testing.T, r liveHookRun) []string {
	t.Helper()
	bind := -1
	for i, m := range r.mounts {
		if _, tgt := mountArgs(m); tgt == r.rootmnt && strings.Contains(m, "-o bind") {
			bind = i
		}
	}
	if bind < 0 {
		t.Fatalf("the hook never bound the overlay onto $rootmnt:\n%v", r.mounts)
	}
	var out []string
	for _, m := range r.mounts[bind+1:] {
		_, tgt := mountArgs(m)
		if strings.HasPrefix(tgt, r.rootmnt+"/") && strings.Contains(m, "-t tmpfs") {
			out = append(out, strings.TrimPrefix(tgt, r.rootmnt))
		}
	}
	return out
}

// TestOnlyVarCacheVulosIsRescuedFromTheOverlay is the measurement, not the
// inference.
//
// It drives scripts/initramfs/vulos-live to completion — the whole file, under
// dash, with klibc-shaped mount/mkdir stubs — and reads the mount log. Mount
// topology is the only thing that decides where a write lands, so this settles
// where an apt-get install goes on an overlay boot without booting a kernel.
//
// The result on both overlay paths: the ONLY subtree the hook pulls back onto
// the disk is /var/cache/vulos, and only on the netboot-installed layout (the
// NETB-03 fix). /usr, /var/lib/dpkg, /var/lib/apt, /etc and /opt — every
// directory apt-get writes to — are inside the overlay, whose upper layer is a
// tmpfs in RAM.
func TestOnlyVarCacheVulosIsRescuedFromTheOverlay(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		want    []string
		wantRAM []string
		why     string
	}{
		{"live", nil, nil, "a live-USB has no on-disk state to keep, so the hook rescues nothing"},
		{"netboot",
			[]string{"/var/cache/vulos", "/root/.vulos", "/var/lib/vulos"},
			[]string{"/root/.vulos/apps"},
			"NETB-03 re-exposes the A/B slot cache; OWNSTATE-01 adds the owner's data " +
				"directory and the hardcoded /var/lib/vulos, and puts the app directory " +
				"straight back in RAM so a manifest cannot outlive its Flatpak payload"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			r := driveLiveHook(t, tc.mode)
			r.assertHarnessActuallyRanTheHook(t)

			got := rescuedSubtrees(t, r)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf(`the set of subtrees rescued from the overlay changed on the %s boot.

  want: %v   (%s)
  got:  %v

mounts, in order:
%s`, tc.mode, tc.want, tc.why, got, strings.Join(r.mounts, "\n"))
			}

			gotRAM := revolatilisedSubtrees(t, r)
			if strings.Join(gotRAM, "\x00") != strings.Join(tc.wantRAM, "\x00") {
				t.Errorf(`the set of subtrees deliberately put BACK IN RAM changed on the %s boot.

  want: %v
  got:  %v

/root/.vulos/apps is in that set on purpose: 13 of the 16 installable registry
entries are Flatpak SYSTEM installs whose payload lands in /var/lib/flatpak,
which is not persisted. Dropping this tmpfs makes the manifest outlive the
payload — the App Hub lists an app that cannot launch, and
AppStore.RealisedVersions() reports it realised so nothing re-installs it.
roadmap/APP-DIR-PERSISTENCE.md is the measurement.

mounts, in order:
%s`, tc.mode, tc.wantRAM, gotRAM, strings.Join(r.mounts, "\n"))
			}

			// The point of the whole exercise: apt's write targets are not
			// among them, so apt-get installs into RAM on this boot.
			for _, target := range aptWriteTargets {
				for _, g := range got {
					if g == target || strings.HasPrefix(target, g+"/") {
						t.Errorf(`%s is now backed by persistent storage on the %s boot (rescued as %q).

An apt-get install writes there, so an app installed with apt would now SURVIVE
a reboot on this path. That is a change to the answer in
roadmap/APT-INSTALL-PERSISTENCE.md and to the founder's decision about the 29
apt registry entries. Update the note.`, target, tc.mode, g)
					}
				}
			}

			// And the writable layer must still be RAM, or none of the above
			// means what it says.
			assertWritableLayerIsRAM(t, r)
		})
	}
}

// assertWritableLayerIsRAM pins the other half of "it does not survive": the
// overlay's upperdir sits on a tmpfs. If that ever becomes a real block device
// the answer inverts, and it must not be able to do so silently.
func assertWritableLayerIsRAM(t *testing.T, r liveHookRun) {
	t.Helper()
	// There is more than one tmpfs on a netboot-installed boot now — OWNSTATE-01
	// mounts a second one over /root/.vulos/apps to keep installed-app manifests
	// in RAM. So this cannot simply take the last tmpfs it sees (it did, and the
	// assertion then compared the overlay against the wrong mount). The tmpfs
	// under test is specifically the one the overlay names as its upperdir.
	var ovl string
	var tmpfsDirs []string
	for _, m := range r.mounts {
		switch {
		case strings.Contains(m, "-t tmpfs"):
			_, d := mountArgs(m)
			tmpfsDirs = append(tmpfsDirs, d)
		case strings.Contains(m, "-t overlay"):
			ovl = m
		}
	}
	if len(tmpfsDirs) == 0 {
		t.Fatalf("the hook mounted no tmpfs; the writable layer is not what this answer "+
			"assumes:\n%s", strings.Join(r.mounts, "\n"))
	}
	if ovl == "" {
		t.Fatalf("the hook mounted no overlay at all:\n%s", strings.Join(r.mounts, "\n"))
	}
	rwDir := ""
	for _, d := range tmpfsDirs {
		if strings.Contains(ovl, "upperdir="+d+"/") {
			rwDir = d
		}
	}
	if rwDir == "" {
		t.Errorf(`no tmpfs backs the overlay's upperdir.

  tmpfs mounted at: %v
  overlay:          %s

If the upper layer has moved to a block device then writes to / — including
every apt-get install — now persist, and the answer in
roadmap/APT-INSTALL-PERSISTENCE.md is obsolete.`, tmpfsDirs, ovl)
		return
	}
	rw := ""
	for _, m := range r.mounts {
		if _, d := mountArgs(m); strings.Contains(m, "-t tmpfs") && d == rwDir {
			rw = m
		}
	}
	if !strings.Contains(ovl, "upperdir="+rwDir+"/") {
		t.Errorf(`the overlay's upperdir is no longer inside the tmpfs.

  tmpfs at: %s
  overlay:  %s

If the upper layer has moved to a block device then writes to / — including
every apt-get install — now persist, and the answer in
roadmap/APT-INSTALL-PERSISTENCE.md is obsolete.`, rwDir, ovl)
	}
	if strings.Contains(rw, "size=") {
		t.Logf("note: the writable tmpfs now declares a size limit (%s). It previously had "+
			"none, i.e. the kernel default of half of RAM — which is what makes "+
			"`apt-get install libreoffice` on an overlay boot an out-of-memory risk "+
			"rather than a disk-space one.", rw)
	}
}

// ─── The --disk path, where the hook does not run at all ─────────────────────

// TestDiskInstallCmdlineLeavesTheHookInert drives the hook with the command
// line the plain --disk installer writes and asserts it exits 0 having mounted
// NOTHING.
//
// That is the whole reason an apt-get install survives a reboot on a --disk
// box: there is no overlay, / is the ext4 the installer formatted, mounted rw
// by the kernel, and apt behaves exactly as it does on any Debian machine.
//
// Driven rather than reasoned, because the gate is `cmdline_has vulos.live` and
// that function matches the KEY= form — the exact subtlety that made
// `vulos.live=0` activate the hook on the netboot path and cost this project
// the /var/cache/vulos defect. "There is no vulos.live token so it does not
// run" is a claim about a shell function, and shell functions are cheap to run.
func TestDiskInstallCmdlineLeavesTheHookInert(t *testing.T) {
	entries := loaderEntryCmdlines(t, "backend/internal/installer/disk.go")
	if len(entries) != 1 {
		t.Fatalf("expected exactly one loader entry in disk.go, got %d", len(entries))
	}
	cmdline := entries[0].cmdline
	if !strings.Contains(cmdline, " rw ") {
		t.Errorf("the --disk installer no longer boots its root `rw`: %q. Without that the "+
			"root is read-only and an apt-get install fails outright rather than "+
			"succeeding and vanishing.", cmdline)
	}

	r := driveHookWithCmdline(t, cmdline)
	if r.exitCode != 0 {
		t.Fatalf("the hook exited %d on the --disk command line; it is supposed to fall "+
			"straight through its gate:\n%s", r.exitCode, r.output)
	}
	if len(r.mounts) != 0 {
		t.Errorf(`the hook performed %d mount(s) on the plain --disk command line:

  %s

mounts:
%s

That command line carries no vulos.live token, so the hook must exit at its
gate and leave / as the real writable ext4. Anything it mounts here puts an
overlay under a boot that is supposed to have none — and every apt-get install
on every --disk box becomes volatile.`, len(r.mounts), cmdline, strings.Join(r.mounts, "\n"))
	}
	if len(r.mkdirs) != 0 {
		t.Errorf("the hook created %d director(ies) on the --disk command line: %v",
			len(r.mkdirs), r.mkdirs)
	}
}

// driveHookWithCmdline runs scripts/initramfs/vulos-live against an arbitrary
// kernel command line and an EMPTY $rootmnt.
//
// It is deliberately minimal and separate from driveLiveHook: the case it
// exists for is the one where the hook must do nothing at all, so fabricating a
// squashfs, a slot layout or a /proc/mounts entry would only give the hook
// material it must not touch. The klibc-shaped stubs are shared, so a mount or
// mkdir this run performs is logged the same way.
func driveHookWithCmdline(t *testing.T, cmdline string) liveHookRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell harness")
	}
	sh, err := exec.LookPath("dash")
	if err != nil {
		sh = "/bin/sh"
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
	for _, s := range []string{"modprobe", "sync", "udevadm", "losetup"} {
		write(filepath.Join(bin, s), "#!/bin/sh\nexit 0\n", 0o755)
	}
	// No trailing newline: that is how the kernel presents /proc/cmdline, and
	// cmdline_has has already been bitten by it once.
	write(filepath.Join(dir, "cmdline"), cmdline, 0o644)
	write(filepath.Join(dir, "mounts"), fmt.Sprintf("/dev/vda2 %s ext4 rw 0 0\n", rootmnt), 0o644)

	src := readRepoFile(t, initramfsH)
	const sourceLine = ". /scripts/functions"
	if !strings.Contains(src, sourceLine) {
		t.Fatalf("%s no longer sources %s; this harness is driving something it does not "+
			"understand", initramfsH, sourceLine)
	}
	hook := filepath.Join(dir, "hook")
	write(hook, strings.Replace(src, sourceLine, ". "+filepath.Join(dir, "functions"), 1), 0o755)

	cmd := exec.Command(sh, hook)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"rootmnt="+rootmnt,
		"VULOS_CMDLINE_FILE="+filepath.Join(dir, "cmdline"),
		"VULOS_MOUNTS_FILE="+filepath.Join(dir, "mounts"),
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
		var lines []string
		for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			if l != "" {
				lines = append(lines, l)
			}
		}
		return lines
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

// ─── The app directory, and what the user sees ───────────────────────────────

// TestInstalledAppManifestsShareTheRootFilesystemsFate is the answer to "what
// does the user actually experience".
//
// AppStore.Installed() is a directory scan of appsDir, and appsDir is
// datadir.Join("apps") — $VULOS_DATA_DIR, or $HOME/.vulos when unset. The
// systemd unit build.sh writes sets HOME=/root and does NOT set
// VULOS_DATA_DIR, so on a real box the installed-app manifests live at
// /root/.vulos/apps — inside /, on the same filesystem as everything apt
// writes.
//
// That is what makes the failure honest rather than deceptive on today's
// images: on an overlay boot the app.json vanishes together with the binary, so
// the App Hub simply stops listing the app. The alternative — a persistent
// manifest above a volatile binary — is the "installed but cannot start" shape,
// and it becomes reachable the moment either VULOS_DATA_DIR or a vulos-data
// partition points the data directory somewhere that outlives the root.
//
// This test pins the two inputs that decide which of those two worlds the box
// is in.
func TestInstalledAppManifestsShareTheRootFilesystemsFate(t *testing.T) {
	unit := readRepoFile(t, "build.sh")

	if strings.Contains(unit, "Environment=VULOS_DATA_DIR=") {
		t.Errorf(`build.sh now sets VULOS_DATA_DIR in the vulos-server unit.

The box data directory — which holds every installed app's app.json — no longer
follows the root filesystem. If it now points at persistent storage while / is
an overlay, an apt-installed app keeps its manifest and loses its binary: the
App Hub lists it, the launch fails, and that is strictly worse than the app
being gone. See roadmap/APT-INSTALL-PERSISTENCE.md.`)
	}
	if !strings.Contains(unit, "Environment=HOME=/root") {
		t.Errorf("the vulos-server unit in build.sh no longer sets HOME=/root, so " +
			"datadir.Root() resolves somewhere this answer has not looked. The " +
			"installed-app manifests move with it.")
	}

	// And datadir must still derive the root from HOME, or the line above is
	// pinning something that no longer matters.
	dd := readRepoFile(t, "backend/internal/datadir/datadir.go")
	if !strings.Contains(dd, `filepath.Join(home, ".vulos")`) {
		t.Errorf("backend/internal/datadir/datadir.go no longer resolves the data root to " +
			"$HOME/.vulos; re-derive where the installed-app manifests live before " +
			"trusting roadmap/APT-INSTALL-PERSISTENCE.md.")
	}

	// Finally, the appsDir really is under the data root and not somewhere
	// persistent of its own.
	server := readRepoFile(t, "backend/cmd/server/main.go")
	if !strings.Contains(server, `datadir.Join("apps")`) {
		t.Errorf("backend/cmd/server/main.go no longer builds the app store on " +
			`datadir.Join("apps"). Where installed manifests live is the difference ` +
			"between an app that disappears and an app that lies about being installed.")
	}
}
