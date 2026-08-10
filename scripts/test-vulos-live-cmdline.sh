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

EXPECTED_ASSERTIONS=33
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

# ── OSDIST-FLIP-01: boot-state.json overrides the install-time cmdline slot ───
#
# The cmdline is written ONCE at install, pinned to slot-a. An OTA stages into
# the other slot and flips "active"; the boot-counter rollback flips it back.
# Neither did anything until apply_active_slot existed, so a staged update never
# booted and — worse — a rollback that had decided the running slot was bad
# still booted it.
#
# Every case builds a REAL temp root, so the "is the image actually there" check
# runs against the filesystem rather than a stub.
echo ""
echo "-- OSDIST-FLIP-01: boot-state.json slot selection"

SLOTROOT="$(mktemp -d)"
trap 'rm -rf "${SLOTROOT}"' EXIT
mkdir -p "${SLOTROOT}/var/cache/vulos/slot-a" "${SLOTROOT}/var/cache/vulos/slot-b"
: > "${SLOTROOT}/var/cache/vulos/slot-a/os-core.squashfs"
: > "${SLOTROOT}/var/cache/vulos/slot-b/os-core.squashfs"
STATE="${SLOTROOT}/var/cache/vulos/boot-state.json"

# run_slot <state-file> → "<squashfs>|<hashtree>|<roothash>|<selected>"
run_slot() {
  ( set -e
    # shellcheck disable=SC1090
    . "${EXTRACT}"
    SQUASHFS_PATH="/var/cache/vulos/slot-a/os-core.squashfs"
    HASHTREE_PATH="/var/cache/vulos/slot-a/os-core.hashtree"
    ROOTHASH_PATH="/var/cache/vulos/slot-a/os-core.roothash"
    # Called PLAINLY, never as $(...): the function mutates SQUASHFS_PATH in the
    # caller's shell, and command substitution would run it in a subshell and
    # discard that. The production hook has the same requirement — the first
    # version of this change got it wrong and these tests are what caught it.
    apply_active_slot "$1" "${SLOTROOT}" || true
    printf '%s|%s|%s|%s' "${SQUASHFS_PATH}" "${HASHTREE_PATH}" "${ROOTHASH_PATH}" "${SELECTED_SLOT}" )
}

printf '{"active":"b","pending":"","boot_counter":0,"last_known_good":"a"}' > "${STATE}"
OUT="$(run_slot "${STATE}")"
assert_eq "${OUT%%|*}" "/var/cache/vulos/slot-b/os-core.squashfs" "active=b boots slot-b, overriding the slot-a cmdline"
REST="${OUT#*|}"
assert_eq "${REST%%|*}" "/var/cache/vulos/slot-b/os-core.hashtree" "verity hashtree follows the selected slot"
REST2="${REST#*|}"
assert_eq "${REST2%%|*}" "/var/cache/vulos/slot-b/os-core.roothash" "verity roothash follows the selected slot"
assert_eq "${OUT##*|}" "b" "the selected slot is reported to the caller"

printf '{"active":"a","pending":"","boot_counter":0,"last_known_good":"a"}' > "${STATE}"
OUT="$(run_slot "${STATE}")"
assert_eq "${OUT%%|*}" "/var/cache/vulos/slot-a/os-core.squashfs" "active=a boots slot-a (the rollback destination)"

# ── fail-closed paths: each KEEPS the cmdline image, never boots nothing ─────

OUT="$(run_slot "${SLOTROOT}/var/cache/vulos/does-not-exist.json")"
assert_eq "${OUT%%|*}" "/var/cache/vulos/slot-a/os-core.squashfs" "missing boot-state keeps the cmdline image"

# Truncated mid-write: the realistic corruption, since this file is rewritten on
# every boot-counter increment. Cut INSIDE the value — an earlier version of this
# case stopped after `"active":"b",` which still contains a complete, parseable
# pair, so it proved nothing.
printf '{"active":"b' > "${STATE}"
OUT="$(run_slot "${STATE}")"
assert_eq "${OUT%%|*}" "/var/cache/vulos/slot-a/os-core.squashfs" "truncated boot-state keeps the cmdline image"

# The [ab] restriction, tested DIRECTLY. Going through apply_active_slot only
# proves the downstream "does the image exist" check caught it — slot-z has no
# directory, so the letter restriction could be removed entirely and that path
# would still pass. Assert the parser itself refuses.
printf '{"active":"z"}' > "${STATE}"
if ( . "${EXTRACT}" 2>/dev/null; read_active_slot "${STATE}" >/dev/null ); then
  assert_eq "accepted" "rejected" "read_active_slot refuses a slot letter outside a|b"
else
  assert_eq "rejected" "rejected" "read_active_slot refuses a slot letter outside a|b"
fi

OUT="$(run_slot "${STATE}")"
assert_eq "${OUT%%|*}" "/var/cache/vulos/slot-a/os-core.squashfs" "an unknown slot letter keeps the cmdline image"

# A flip recorded but never staged: booting it would be booting nothing.
printf '{"active":"b"}' > "${STATE}"
rm -f "${SLOTROOT}/var/cache/vulos/slot-b/os-core.squashfs"
OUT="$(run_slot "${STATE}")"
assert_eq "${OUT%%|*}" "/var/cache/vulos/slot-a/os-core.squashfs" "a slot with no image on disk keeps the cmdline image"
assert_eq "${OUT##*|}" "" "and reports no selection, so the caller logs the fallback"
: > "${SLOTROOT}/var/cache/vulos/slot-b/os-core.squashfs"

# ── The booted-slot marker, against a KLIBC-FAITHFUL fake `mount` ────────────
#
# The initramfs `mount` is klibc's — not util-linux's, not busybox's — and it
# requires BOTH a device and a directory:
#
#   Usage: mount [-r] [-w] [-o options] [-t type] [-f] [-i] [-n] device directory
#
# `mount -o remount,rw /root`, the util-linux spelling where libmount resolves
# the device itself, is a USAGE ERROR there. It exits 1 having remounted
# nothing, and the marker write that followed then failed against a still
# read-only root. Because every step was best-effort with stderr discarded, the
# ONLY symptom was a file that never appeared — which is what made this cost a
# QEMU boot with an instrumented initramfs to find.
#
# So the fake `mount` below enforces klibc's arity rather than accepting
# anything: exactly two positional arguments after the option flags, or usage
# error + exit 1. Run the device-less spelling against it and these cases fail,
# which is the property that makes them worth having. `sync` is faked too — the
# initramfs has one, but it is not this test's subject and a real one would
# flush the whole host.
echo ""
echo "-- booted-slot marker (klibc mount arity)"

FAKEBIN="${TMPROOT}/fakebin"
mkdir -p "${FAKEBIN}"
MOUNTLOG="${TMPROOT}/mount.log"
cat > "${FAKEBIN}/mount" <<'FAKE'
#!/bin/sh
# klibc mount: options are flags; exactly two positionals (device directory).
printf '%s\n' "$*" >> "${MOUNTLOG}"
_pos=0
while [ $# -gt 0 ]; do
  case "$1" in
    -o|-t) shift 2 ;;
    -r|-w|-f|-i|-n) shift ;;
    *) _pos=$((_pos + 1)); shift ;;
  esac
done
if [ "$_pos" -ne 2 ]; then
  echo "Usage: mount [-r] [-w] [-o options] [-t type] [-f] [-i] [-n] device directory" >&2
  exit 1
fi
exit 0
FAKE
printf '#!/bin/sh\nexit 0\n' > "${FAKEBIN}/sync"
chmod +x "${FAKEBIN}/mount" "${FAKEBIN}/sync"

MOUNTSFILE="${TMPROOT}/mounts"
cat > "${MOUNTSFILE}" <<EOF
proc /proc proc rw,relatime 0 0
/dev/vda2 ${SLOTROOT} ext4 ro,relatime 0 0
EOF

: > "${MOUNTLOG}"
rm -f "${SLOTROOT}/var/cache/vulos/booted-slot"
MOUNTLOG="${MOUNTLOG}" VULOS_MOUNTS_FILE="${MOUNTSFILE}" PATH="${FAKEBIN}:${PATH}" \
  "${SH_BIN}" -c ". '${EXTRACT}'; record_booted_slot '${SLOTROOT}' b boot-state /var/cache/vulos/slot-b/os-core.squashfs"

GOT="$(cat "${SLOTROOT}/var/cache/vulos/booted-slot" 2>/dev/null || echo NOFILE)"
assert_eq "${GOT}" "$(printf 'slot=b\nvia=boot-state\nimage=/var/cache/vulos/slot-b/os-core.squashfs')" \
  "record_booted_slot writes the slot, how it was chosen, and the image"

# The device argument is the whole point: assert it is THERE, and that it is the
# device /proc/mounts names — not a guess and not the mountpoint twice.
assert_eq "$(grep -c -- '-o remount,rw /dev/vda2 '"${SLOTROOT}" "${MOUNTLOG}")" "1" \
  "the rw remount names the device klibc requires, resolved from /proc/mounts"
assert_eq "$(grep -c -- '-o remount,ro /dev/vda2 '"${SLOTROOT}" "${MOUNTLOG}")" "1" \
  "and the root is put back read-only afterwards, also with the device"

# rootmnt_device itself: last match wins, because a later mount at the same
# point shadows an earlier one.
cat > "${MOUNTSFILE}" <<EOF
/dev/vda1 ${SLOTROOT} ext4 ro,relatime 0 0
/dev/vda2 ${SLOTROOT} ext4 ro,relatime 0 0
EOF
GOT="$(VULOS_MOUNTS_FILE="${MOUNTSFILE}" "${SH_BIN}" -c ". '${EXTRACT}'; rootmnt_device '${SLOTROOT}'")"
assert_eq "${GOT}" "/dev/vda2" "rootmnt_device takes the LAST mount at the path, which is the one in effect"

# Not mounted at all → non-zero, so the caller skips the remount rather than
# invoking mount with an empty device (which klibc would read as one positional
# and reject anyway).
cat > "${MOUNTSFILE}" <<EOF
proc /proc proc rw,relatime 0 0
EOF
if VULOS_MOUNTS_FILE="${MOUNTSFILE}" "${SH_BIN}" -c ". '${EXTRACT}'; rootmnt_device '${SLOTROOT}'" >/dev/null; then
  assert_eq "found" "not-found" "rootmnt_device reports failure when the path is not mounted"
else
  assert_eq "not-found" "not-found" "rootmnt_device reports failure when the path is not mounted"
fi

# A root with no /var/cache/vulos (a live-USB or netboot medium) is not an
# error: there is no slot layout to record against, and the boot must carry on.
NOCACHE="${TMPROOT}/nocache"
mkdir -p "${NOCACHE}"
: > "${MOUNTLOG}"
if MOUNTLOG="${MOUNTLOG}" VULOS_MOUNTS_FILE="${MOUNTSFILE}" PATH="${FAKEBIN}:${PATH}" \
     "${SH_BIN}" -c ". '${EXTRACT}'; record_booted_slot '${NOCACHE}' '?' cmdline /image.squashfs"; then
  assert_eq "wrote" "skipped" "record_booted_slot skips a root with no /var/cache/vulos"
else
  assert_eq "skipped" "skipped" "record_booted_slot skips a root with no /var/cache/vulos"
fi
assert_eq "$(wc -l < "${MOUNTLOG}" | tr -d ' ')" "0" "and remounts nothing when there is nothing to record"

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
