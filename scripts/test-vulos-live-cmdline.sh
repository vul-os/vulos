#!/usr/bin/env bash
# test-vulos-live-cmdline.sh — gate for the vulos.squashfs= cmdline parsing +
# path-derivation logic inside scripts/initramfs/vulos-live.
#
# Why this exists: the initramfs hook that mounts the OS squashfs
# (scripts/initramfs/vulos-live) had never been exercised — not even by a
# real boot — until NETB-05. That absence let two installer paths
# (backend/internal/installer/esp.go, backend/services/installer/
# netboot_install.go) ship a `vulos.squashfs=<path>` kernel-cmdline token that
# NOTHING ever read: the hook hardcoded /image.squashfs and ignored the
# token, so a netboot-installed disk that correctly set vulos.live=0 (the
# dfff94f5 fix) still failed to find its own OS at boot. This gate pins the
# parsing/derivation logic that closes that gap so a future edit cannot
# silently reopen it without booting a kernel to notice.
#
# What it exercises: the EXACT script text shipped in the initramfs image,
# not a reimplementation. It extracts the block between the
# "BEGIN-TESTABLE"/"END-TESTABLE" markers in vulos-live (read_cmdline_value +
# the SQUASHFS_PATH/HASHTREE_PATH/ROOTHASH_PATH derivation) and sources that
# extract in a real POSIX shell (dash, matching the initramfs's /bin/sh),
# fed a fake /proc/cmdline via $VULOS_CMDLINE_FILE. The mount/overlay logic
# below those markers is NOT exercised here — that requires a real kernel and
# is covered by scripts/netboot-install-smoke.sh (QEMU boot).
#
# FAIL-CLOSED CONTRACT:
#   * Every case ends in an assert_* call; each records itself in ASSERTIONS_RUN.
#   * The run FAILS unless ASSERTIONS_RUN == EXPECTED_ASSERTIONS.
#   * There is no skip path. dash missing, markers missing, or the extract
#     failing to source are all hard failures, not skips.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOOK="${ROOT}/scripts/initramfs/vulos-live"
SH_BIN="$(command -v dash || command -v sh)"

EXPECTED_ASSERTIONS=15
ASSERTIONS_RUN=0
FAILURES=0

fail() { printf '  FAIL: %s\n' "$*" >&2; FAILURES=$((FAILURES + 1)); }
pass() { printf '  ok: %s\n' "$*"; }

assert_eq() {
  local got="$1" want="$2" what="$3"
  ASSERTIONS_RUN=$((ASSERTIONS_RUN + 1))
  if [ "${got}" = "${want}" ]; then
    pass "${what}"
  else
    fail "${what}"
    printf '       got:  %s\n       want: %s\n' "${got}" "${want}" >&2
  fi
}

[ -f "${HOOK}" ] || { echo "FATAL: hook not found at ${HOOK}" >&2; exit 1; }

TMPROOT="$(mktemp -d)"
trap 'rm -rf "${TMPROOT}"' EXIT

# Extract the testable block. A missing/mismatched marker pair is a hard
# failure (not a skip) — it means the hook was refactored and this gate no
# longer knows what it's testing.
EXTRACT="${TMPROOT}/extract.sh"
awk '/# ── BEGIN-TESTABLE/{f=1} f{print} /# ── END-TESTABLE/{exit}' "${HOOK}" > "${EXTRACT}"
if [ ! -s "${EXTRACT}" ]; then
  echo "FATAL: BEGIN-TESTABLE/END-TESTABLE markers not found in ${HOOK} — did the hook get refactored?" >&2
  exit 1
fi
if ! grep -q 'END-TESTABLE' "${EXTRACT}"; then
  echo "FATAL: extract has no END-TESTABLE marker — BEGIN without a matching END in ${HOOK}" >&2
  exit 1
fi

# run_case <cmdline-content> → prints "SQUASHFS_PATH|HASHTREE_PATH|ROOTHASH_PATH"
# by sourcing the real extracted hook text with a fake /proc/cmdline. The
# fixture is written with NO trailing newline — deliberately: that is how a
# real /proc/cmdline is presented, and is exactly the shape that exposed the
# "read ... || _cl=''" bug (see the comment on cmdline_has in vulos-live).
run_case() {
  local cmdline="$1"
  local cmdfile="${TMPROOT}/cmdline"
  printf '%s' "${cmdline}" > "${cmdfile}"
  VULOS_CMDLINE_FILE="${cmdfile}" "${SH_BIN}" -c \
    ". '${EXTRACT}'; printf '%s|%s|%s' \"\$SQUASHFS_PATH\" \"\$HASHTREE_PATH\" \"\$ROOTHASH_PATH\""
}

# run_cmdline_has <cmdline-content> <key> → prints "0" (matched) or "1" (not
# matched), i.e. cmdline_has's own exit status. Same no-trailing-newline
# fixture as run_case.
run_cmdline_has() {
  local cmdline="$1" key="$2"
  local cmdfile="${TMPROOT}/cmdline_has_input"
  printf '%s' "${cmdline}" > "${cmdfile}"
  VULOS_CMDLINE_FILE="${cmdfile}" "${SH_BIN}" -c \
    ". '${EXTRACT}'; cmdline_has '${key}'; printf '%s' \$?"
}

echo "== vulos-live cmdline / squashfs-path-derivation gate =="

# ── Case 1: no vulos.squashfs= at all → live-USB defaults (build.sh --live) ──
echo "-- vulos.live=1 only (plain live-USB, no override)"
OUT="$(run_case 'root=LABEL=VULOS-LIVE-DATA ro vulos.live=1 quiet splash')"
assert_eq "${OUT%%|*}" "/image.squashfs" "default squashfs path is /image.squashfs"
OUT2="${OUT#*|}"
assert_eq "${OUT2%%|*}" "/os-core.hashtree" "default hashtree path is /os-core.hashtree"
assert_eq "${OUT2#*|}"  "/os-core.roothash" "default roothash path is /os-core.roothash"

# ── Case 2: NETB-03 slot-a entry (the exact string writeSlotABootEntry writes) ─
echo "-- vulos.live=0 vulos.slot=a vulos.squashfs=<slot path> (NETB-03 netboot install)"
OUT="$(run_case 'root=LABEL=vulos-root ro quiet splash vulos.live=0 vulos.slot=a vulos.squashfs=/var/cache/vulos/slot-a/os-core.squashfs')"
assert_eq "${OUT%%|*}" "/var/cache/vulos/slot-a/os-core.squashfs" "NETB-03 override is honoured for the squashfs path"
OUT2="${OUT#*|}"
assert_eq "${OUT2%%|*}" "/var/cache/vulos/slot-a/os-core.hashtree" "sibling hashtree path derived from the override"
assert_eq "${OUT2#*|}"  "/var/cache/vulos/slot-a/os-core.roothash" "sibling roothash path derived from the override"

# ── Case 3: BMINIT-14 re-flashed live-USB entry (esp.go writeLiveBootEntry) ───
echo "-- vulos.squashfs=/EFI/vulos/os-core.squashfs (BMINIT-14 USB re-flash)"
OUT="$(run_case 'root=LABEL=vulos-root ro quiet splash vulos.live=1 toram vulos.squashfs=/EFI/vulos/os-core.squashfs')"
assert_eq "${OUT%%|*}" "/EFI/vulos/os-core.squashfs" "BMINIT-14 override is honoured for the squashfs path"
OUT2="${OUT#*|}"
assert_eq "${OUT2%%|*}" "/EFI/vulos/os-core.hashtree" "BMINIT-14 sibling hashtree path derived from the override"
assert_eq "${OUT2#*|}"  "/EFI/vulos/os-core.roothash" "BMINIT-14 sibling roothash path derived from the override"

# ── Case 4: override present but NOT ending in .squashfs → keep safe defaults ──
# A malformed/unexpected override must not silently guess a wrong sibling
# path; falling back to the (now-nonexistent) defaults correctly routes
# through the "missing companion files" branch rather than fabricating a
# false hashtree/roothash location.
echo "-- vulos.squashfs=<no .squashfs suffix> → hashtree/roothash keep defaults"
OUT="$(run_case 'vulos.live=1 vulos.squashfs=/mnt/os-image')"
assert_eq "${OUT%%|*}" "/mnt/os-image" "non-.squashfs override is still honoured for the squashfs path itself"
OUT2="${OUT#*|}"
assert_eq "${OUT2%%|*}" "/os-core.hashtree" "hashtree path stays at the safe default when the override has no .squashfs suffix"
assert_eq "${OUT2#*|}"  "/os-core.roothash" "roothash path stays at the safe default when the override has no .squashfs suffix"

# ── Case 5: empty override value is treated as absent ─────────────────────────
echo "-- vulos.squashfs= (empty value) → falls back to defaults, not an empty path"
OUT="$(run_case 'vulos.live=1 vulos.squashfs=')"
assert_eq "${OUT%%|*}" "/image.squashfs" "empty vulos.squashfs= value does not override the default"

# ── Case 6: cmdline_has on a no-trailing-newline cmdline (the actual bug) ─────
# Regression for the "read ... || _cl=''" bug: read returns non-zero on a
# line with no trailing newline (which is how /proc/cmdline is normally
# presented) even though it populated the variable correctly; the old code
# treated that non-zero status as "read failed" and discarded the value,
# so cmdline_has could never match ANYTHING against a real /proc/cmdline —
# meaning vulos.live would never be detected and the live overlay would never
# activate on any real boot. run_case's fixture already has no trailing
# newline (see its comment); this case pins cmdline_has directly instead of
# only observing it indirectly through the vulos.live-gated derivation block.
echo "-- cmdline_has matches vulos.live=0 against a no-trailing-newline cmdline"
GOT="$(run_cmdline_has 'root=LABEL=vulos-root ro vulos.live=0 vulos.slot=a' 'vulos.live')"
assert_eq "${GOT}" "0" "cmdline_has vulos.live matches vulos.live=0 (key=value form)"

echo "-- cmdline_has does not match an absent token"
GOT="$(run_cmdline_has 'root=LABEL=vulos-root ro quiet splash' 'vulos.live')"
assert_eq "${GOT}" "1" "cmdline_has vulos.live does not match when the token is absent"

# ── Coverage assertion + verdict ──────────────────────────────────────────────
echo ""
echo "assertions run: ${ASSERTIONS_RUN} (expected ${EXPECTED_ASSERTIONS})"
if [ "${ASSERTIONS_RUN}" -ne "${EXPECTED_ASSERTIONS}" ]; then
  printf 'FAIL: coverage assertion — %d assertions ran, expected %d.\n' \
    "${ASSERTIONS_RUN}" "${EXPECTED_ASSERTIONS}" >&2
  exit 1
fi
if [ "${FAILURES}" -ne 0 ]; then
  printf 'FAIL: %d assertion(s) failed.\n' "${FAILURES}" >&2
  exit 1
fi
echo "PASS: vulos-live cmdline gate (${ASSERTIONS_RUN} assertions)"
