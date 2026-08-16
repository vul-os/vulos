#!/usr/bin/env bash
#
# verify-apt-persistence.sh — prove that the guards behind the answer in
# roadmap/APT-INSTALL-PERSISTENCE.md are capable of failing, and (on a real box)
# check that answer end to end.
#
# THE ANSWER THIS DEFENDS
#
#   An app installed with `apt-get` survives a reboot on the plain --disk
#   install and on NOTHING else. The live-USB, live-ESP and netboot-installed
#   boots all hand / to an overlay whose upper layer is a tmpfs in RAM, and
#   apt writes to /usr, /var/lib/dpkg, /var/lib/apt, /etc and /opt — all of
#   which are in that tmpfs.
#
# WHY THIS SCRIPT EXISTS
#
# backend/internal/docsref/aptpersist_test.go asserts that answer. Six guards
# that all pass tell you nothing on their own — this project's dominant defect
# class is a gate that prints PASS while checking nothing, and several of these
# assertions are of the shape "the hook did NOT do X", which a harness that
# failed to start the hook satisfies trivially. So each guard is mutated here,
# one at a time, and must FAIL with the message it was written to print.
#
# IT NEVER TOUCHES THE WORKING TREE. The git index in this repository is shared
# with other agents, and an in-place mutate/restore cycle is exactly how one
# agent's half-applied edit ends up in another's commit. Instead a MIRROR of the
# handful of files the guards read is built in a temp directory, the mutation is
# applied there, and the tests run against the mirror. Nothing under the repo is
# written, at any point, in any mode.
#
# USAGE
#
#   scripts/verify-apt-persistence.sh            # mutation-verify the guards
#   scripts/verify-apt-persistence.sh --on-box   # probe a booted Vulos box
#
# The --on-box mode is the one thing no static test can replace. Run it on the
# machine itself; it is read-only unless you pass --on-box --install-probe.

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-mutate}"

RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; DIM=$'\033[2m'; NC=$'\033[0m'

# ─────────────────────────────────────────────────────────────────────────────
# --on-box: the end-to-end check, on the machine itself
# ─────────────────────────────────────────────────────────────────────────────

on_box() {
    echo "== Vulos app-install persistence probe =="
    echo

    echo "--- kernel command line (the token that decides everything) ---"
    cat /proc/cmdline || true
    echo
    if grep -Eq '(^| )vulos\.live($|[= ])' /proc/cmdline; then
        echo "${YELLOW}vulos.live present -> this boot runs the initramfs overlay."
        echo "Expect / to be an overlay and apt installs to be VOLATILE.${NC}"
    else
        echo "${GREEN}no vulos.live token -> the hook exits at its gate."
        echo "Expect / to be a real writable filesystem and apt installs to PERSIST.${NC}"
    fi
    echo

    echo "--- what backs each directory apt writes to ---"
    # Every one of these is a path dpkg unpacks into. cmd.Dir in registry.go is
    # irrelevant to all of them.
    for d in / /usr /var/lib/dpkg /var/lib/apt /etc /opt /var/cache/vulos "${HOME:-/root}/.vulos"; do
        if [ -e "$d" ]; then
            findmnt -n -o TARGET,SOURCE,FSTYPE --target "$d" 2>/dev/null \
                | sed "s|^|  $d -> |" || echo "  $d -> (findmnt unavailable)"
        else
            echo "  $d -> (absent)"
        fi
    done
    echo
    echo "${DIM}  FSTYPE 'overlay' means volatile: the write lands in the tmpfs upper layer."
    echo "  A real device (ext4 on /dev/...) means it persists.${NC}"
    echo

    echo "--- is the writable layer RAM? ---"
    findmnt -n -o TARGET,SOURCE,FSTYPE,OPTIONS /run/vulos/rw 2>/dev/null \
        || echo "  /run/vulos/rw not mounted (not an overlay boot)"
    echo "${DIM}  No size= option means the tmpfs defaults to half of RAM — which is why"
    echo "  'apt-get install libreoffice' on an overlay boot is an out-of-memory risk"
    echo "  and not merely a write that vanishes.${NC}"
    echo

    echo "--- what the App Hub currently lists ---"
    curl -s --max-time 5 localhost:8080/api/apps 2>/dev/null | grep -c '"id"' \
        | sed 's/^/  apps listed: /' || echo "  (server not answering on :8080)"
    echo

    if [ "${2:-}" = "--install-probe" ]; then
        echo "--- installing a marker package (WRITES to this box) ---"
        apt-get update -qq || true
        apt-get install -y --no-install-recommends sl
        echo "  installed at: $(command -v sl || echo '<not on PATH>')"
        echo
        echo "${YELLOW}Now reboot, run this script again WITHOUT --install-probe, and check:"
        echo "    command -v sl"
        echo "  nothing  -> the install was volatile; the note's answer holds for this path."
        echo "  a path   -> the install persisted; the note is WRONG for this path, say so.${NC}"
    else
        echo "--- marker package from a previous run ---"
        if command -v sl >/dev/null 2>&1; then
            echo "  ${GREEN}sl is present at $(command -v sl) — an apt install SURVIVED on this path.${NC}"
        else
            echo "  sl is not installed."
            echo "  ${DIM}Either it was never installed, or it was and did not survive."
            echo "  Run with --on-box --install-probe, reboot, then run this again.${NC}"
        fi
    fi
}

if [ "$MODE" = "--on-box" ]; then
    on_box "$@"
    exit 0
fi

# ─────────────────────────────────────────────────────────────────────────────
# Mutation verification
# ─────────────────────────────────────────────────────────────────────────────

command -v go >/dev/null 2>&1 || { echo "${RED}go is required${NC}" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "${RED}python3 is required${NC}" >&2; exit 1; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/aptpersist-verify.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
MIRROR="$WORK/mirror"

# The files the guards read through repoRoot, plus the docsref test sources.
# Listed explicitly: a guard that starts reading a file not mirrored here fails
# loudly in the baseline run rather than silently reading nothing.
MIRRORED_FILES=(
    "build.sh"
    "scripts/initramfs/vulos-live"
    "backend/internal/installer/disk.go"
    "backend/internal/installer/esp.go"
    "backend/services/installer/netboot_install.go"
    "backend/cmd/init/main.go"
    "backend/cmd/server/main.go"
    "backend/internal/datadir/datadir.go"
    "backend/internal/docsref/aptpersist_test.go"
    "backend/internal/docsref/livehook_test.go"
)

build_mirror() {
    rm -rf "$MIRROR"
    for f in "${MIRRORED_FILES[@]}"; do
        [ -f "$REPO/$f" ] || { echo "${RED}missing from the repo: $f${NC}" >&2; exit 1; }
        mkdir -p "$MIRROR/$(dirname "$f")"
        cp "$REPO/$f" "$MIRROR/$f"
    done

    # A module of its own so `go test` never reaches the real tree. The `go`
    # directive is reduced to major.minor deliberately: the real go.mod pins a
    # patch release, and asking for one the local toolchain does not have makes
    # `go test` try to DOWNLOAD a toolchain, which hangs offline and looks
    # exactly like a slow test run.
    printf 'module vulos/backend\n\ngo 1.25\n' > "$MIRROR/backend/go.mod"

    # repoRoot / readRepoFile / initramfsH live in test files this mirror does
    # not carry (they bring in unrelated fixtures). Re-declare the three of them
    # with the SAME semantics as backend/internal/docsref/{docsref,bootsig}_test.go.
    cat > "$MIRROR/backend/internal/docsref/mirror_helpers_test.go" <<'GOEOF'
package docsref

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	repoRoot   = "../../.."
	initramfsH = "scripts/initramfs/vulos-live"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if len(b) < 500 {
		t.Fatalf("%s is %d bytes; too short to be the file this check describes", rel, len(b))
	}
	return string(b)
}
GOEOF
}

# mutate FILE OLD NEW — applies exactly one textual mutation to the MIRROR and
# refuses to continue if the anchor is not found. A mutation that did not apply
# produces a test that passes, which reads identically to a guard that does not
# kill; that false negative is the single most expensive mistake this kind of
# harness can make, so it is checked rather than assumed.
mutate() {
    python3 - "$MIRROR/$1" "$2" "$3" <<'PYEOF'
import sys
path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
src = open(path, encoding="utf-8").read()
n = src.count(old)
if n == 0:
    sys.exit(f"MUTATION ANCHOR NOT FOUND in {path}:\n  {old!r}\n"
             "The file has changed and this mutation is no longer applied — so a PASS "
             "below would prove nothing. Fix the anchor.")
open(path, "w", encoding="utf-8").write(src.replace(old, new))
print(f"    applied to {path.split('/mirror/')[-1]} ({n} site{'s' if n != 1 else ''})")
PYEOF
}

run_tests() {
    ( cd "$MIRROR/backend" \
      && GOTOOLCHAIN=local GOPROXY=off GOFLAGS=-mod=mod \
         go test -count=1 ./internal/docsref/ -run "$1" 2>&1 )
}

PASS=0
FAIL=0

# ── baseline ────────────────────────────────────────────────────────────────
echo "== baseline: every guard must PASS on the unmutated tree =="
build_mirror
if out="$(run_tests 'TestBootPathCmdlinesStillSplitTheSameWay|TestVulosInitRunsOnlyWhereTheRootIsAlreadyPersistent|TestNothingCreatesTheDataPartitionLabel|TestOnlyVarCacheVulosIsRescuedFromTheOverlay|TestDiskInstallCmdlineLeavesTheHookInert|TestInstalledAppManifestsShareTheRootFilesystemsFate')"; then
    echo "  ${GREEN}baseline green${NC}"
else
    echo "  ${RED}BASELINE IS RED — nothing below means anything.${NC}"
    echo "$out"
    exit 1
fi
echo

# ── mutations ───────────────────────────────────────────────────────────────
#
# Each entry: description | file | anchor | replacement | test regexp | expected
# substring of the failure message. The expected substring matters as much as
# the failure itself: a mutation that trips some OTHER assertion has not shown
# the guard it was aimed at to be alive.

run_mutation() {
    local desc="$1" file="$2" old="$3" new="$4" tests="$5" want="$6"
    echo "-- ${desc}"
    build_mirror
    mutate "$file" "$old" "$new"
    if out="$(run_tests "$tests")"; then
        echo "  ${RED}SURVIVED — the guard did not kill this mutation.${NC}"
        echo "$out" | sed 's/^/    /'
        FAIL=$((FAIL + 1))
        return
    fi
    if ! printf '%s' "$out" | grep -qF "$want"; then
        echo "  ${RED}FAILED FOR THE WRONG REASON — expected to see: ${want}${NC}"
        echo "$out" | sed 's/^/    /'
        FAIL=$((FAIL + 1))
        return
    fi
    echo "  ${GREEN}killed${NC} ${DIM}(\"${want}\")${NC}"
    PASS=$((PASS + 1))
}

echo "== mutations: each must be KILLED, and by the right assertion =="

# 1 — the NETB-03 re-exposure is what makes /var/cache/vulos persistent. Remove
#     it and the rescued set on a netboot boot becomes empty.
run_mutation \
    "delete the post-rebind /var/cache/vulos re-exposure" \
    "scripts/initramfs/vulos-live" \
    'if mount -o bind "$CACHE_CAPTURE" "${rootmnt}/var/cache/vulos"; then' \
    'if false; then' \
    'TestOnlyVarCacheVulosIsRescuedFromTheOverlay' \
    'the set of subtrees rescued from the overlay changed'

# 2 — the inverse: rescue /usr as well, i.e. make apt installs persist on an
#     overlay boot. That is the change that would INVALIDATE the answer, and it
#     must be impossible to make quietly.
run_mutation \
    "rescue /usr from the overlay too (apt would start persisting)" \
    "scripts/initramfs/vulos-live" \
    'mount -o bind "$MERGED" "$rootmnt"
' \
    'mount -o bind "$MERGED" "$rootmnt"
mkdir -p /run/vulos/usr
mount -o bind /run/vulos/usr "${rootmnt}/usr"
' \
    'TestOnlyVarCacheVulosIsRescuedFromTheOverlay' \
    '/usr is now backed by persistent storage'

# 3 — the other half of "it does not survive": the upper layer is RAM. Move it
#     off the tmpfs and the answer inverts.
run_mutation \
    "move the overlay upperdir off the tmpfs" \
    "scripts/initramfs/vulos-live" \
    'UPPER="/run/vulos/rw/upper"' \
    'UPPER="/run/vulos/upper"' \
    'TestOnlyVarCacheVulosIsRescuedFromTheOverlay' \
    "upperdir is no longer inside the tmpfs"

# 4 — ANTI-HOLLOW-GATE. Most assertions here are "the hook did not do X", which
#     a hook that never ran satisfies for free. Make the hook exit at its gate
#     on every boot and the run must be rejected as evidence, not pass.
run_mutation \
    "make the hook exit at its gate on EVERY boot (hollow-gate control)" \
    "scripts/initramfs/vulos-live" \
    'if ! cmdline_has vulos.live; then' \
    'if true; then' \
    'TestOnlyVarCacheVulosIsRescuedFromTheOverlay' \
    'did not reach the mount sequence'

# 5 — give the --disk boot a vulos.live token. cmdline_has matches the KEY=
#     form, so `vulos.live=0` ACTIVATES the hook; this is the exact subtlety
#     that produced the /var/cache/vulos defect. It would silently make every
#     --disk box's apt installs volatile.
run_mutation \
    "add vulos.live=0 to the --disk command line" \
    "backend/internal/installer/disk.go" \
    'options root=LABEL=vulos-root rw init=/sbin/vulos-init' \
    'options root=LABEL=vulos-root rw vulos.live=0 init=/sbin/vulos-init' \
    'TestVulosInitRunsOnlyWhereTheRootIsAlreadyPersistent' \
    'hands PID 1 to vulos-init'

# 6 — the same mutation seen by the other guard: the hook now actually runs and
#     mounts an overlay on a boot that is supposed to have none.
run_mutation \
    "add vulos.live=0 to the --disk command line (seen by the gate guard)" \
    "backend/internal/installer/disk.go" \
    'options root=LABEL=vulos-root rw init=/sbin/vulos-init' \
    'options root=LABEL=vulos-root rw vulos.live=0 init=/sbin/vulos-init' \
    'TestDiskInstallCmdlineLeavesTheHookInert' \
    'mount(s) on the plain --disk command line'

# 7 — give mountDataPartition a label that IS created. $HOME/.vulos would then
#     be mounted from a partition, and on an overlay boot the manifest would
#     outlive the binary: the "installed but cannot start" shape.
run_mutation \
    "point mountDataPartition at a label something actually creates" \
    "backend/cmd/init/main.go" \
    'const label = "vulos-data"' \
    'const label = "vulos-root"' \
    'TestNothingCreatesTheDataPartitionLabel' \
    'is now created by'

# 8 — the operator-facing route to the same shape: pin the data dir somewhere
#     that outlives /.
run_mutation \
    "set VULOS_DATA_DIR in the vulos-server unit" \
    "build.sh" \
    'Environment=HOME=/root' \
    'Environment=VULOS_DATA_DIR=/mnt/persist
Environment=HOME=/root' \
    'TestInstalledAppManifestsShareTheRootFilesystemsFate' \
    'now sets VULOS_DATA_DIR in the vulos-server unit'

# 9 — a boot path changing sides must not be silent.
run_mutation \
    "drop vulos.live from the live-ESP entry" \
    "backend/internal/installer/esp.go" \
    'vulos.live=1 toram' \
    'toram' \
    'TestBootPathCmdlinesStillSplitTheSameWay' \
    'expected 3 command lines to activate the live overlay'

echo
if [ "$FAIL" -ne 0 ]; then
    echo "${RED}${FAIL} mutation(s) not killed, ${PASS} killed.${NC}"
    echo "${RED}The answer in roadmap/APT-INSTALL-PERSISTENCE.md is not guarded as claimed.${NC}"
    exit 1
fi
echo "${GREEN}all ${PASS} mutations killed by the assertion each was aimed at.${NC}"
echo "${DIM}The working tree was never modified: every mutation was applied to ${MIRROR}.${NC}"
