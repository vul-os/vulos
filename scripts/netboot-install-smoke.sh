#!/usr/bin/env bash
# Vula OS — netboot-install QEMU smoke harness (NETB-05)
#
# Netboot (NETB-01..04) had never actually been run before this: it had unit
# tests but nothing had ever performed a real netboot-to-install-to-reboot
# cycle. This harness closes that gap as far as is achievable on a single
# local Mac with no cloud/remote infrastructure:
#
#   Phase 1 — build a real --live squashfs (build.sh --live), with the fixed
#             scripts/initramfs/vulos-live hook baked in via update-initramfs
#             (same tool real releases use).
#   Phase 2 — run the REAL netboot-install Go pipeline
#             (backend/services/installer, NOT mocked — real parted, mkfs,
#             mount, bootctl, cp) against a real loop-backed disk image, via
#             TestNetbootInstall_RealPipeline_E2E.
#   Phase 3 — boot the resulting disk image in QEMU (OVMF/UEFI, arm64/HVF)
#             and assert it reaches a fully running Vulos OS, exactly the
#             same gold-standard HTTP check scripts/smoke-liveusb.sh and
#             scripts/baremetal-smoke.sh use.
#
# What this does NOT exercise (see the task report for the full breakdown):
#   - The iPXE/UEFI-HTTP-Boot chainload itself (scripts/netboot/boot.ipxe,
#     imgverify, TLS pinning). Phase 1's build IS the artifact that chain
#     would fetch; this harness starts from "already fetched into RAM",
#     which is where the dfff94f5 bug and this task's other findings live.
#   - The OTA/A/B slot-flip → next-boot bootloader-entry rewrite (that
#     mechanism does not exist yet anywhere in the repo — a separate,
#     larger gap, out of scope here).
#
# Usage:
#   scripts/netboot-install-smoke.sh                 # build, install, boot, assert
#   scripts/netboot-install-smoke.sh --show           # open a QEMU window
#   scripts/netboot-install-smoke.sh --no-build       # reuse a previous build's squashfs
#   scripts/netboot-install-smoke.sh --rebuild        # force a full rootfs rebuild (slow)
#   scripts/netboot-install-smoke.sh --timeout 480
#   scripts/netboot-install-smoke.sh --skip-if-unavailable   # CI: loud skip, not a fake pass
#
# Tool requirements (tool-guarded before anything runs):
#   - docker (OrbStack/Docker Desktop) — the install pipeline needs real
#     Linux parted/mkfs/mount/bootctl, none of which exist on macOS.
#   - qemu-system-aarch64 with HVF acceleration + OVMF/edk2 aarch64 firmware.
#
# There is no `timeout` command on this Mac — every wait below is a bounded
# polling loop, never a bare blocking command.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
OUTDIR="$REPO/output"
BUILDER_IMG="vulos-baremetal-builder"
BUILDER_DF="$REPO/scripts/baremetal-builder.Dockerfile"

EDK2_CODE="${EDK2_CODE_AA64:-/opt/homebrew/share/qemu/edk2-aarch64-code.fd}"
EDK2_VARS_SRC="${EDK2_VARS_AA64:-/opt/homebrew/share/qemu/edk2-arm-vars.fd}"

HOSTPORT="${HOSTPORT:-8091}"
TIMEOUT=360
SHOW=0
REBUILD=0
NO_BUILD=0
SKIP_IF_UNAVAILABLE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --show)      SHOW=1; shift ;;
    --rebuild)   REBUILD=1; shift ;;
    --no-build)  NO_BUILD=1; shift ;;
    --timeout)   TIMEOUT="$2"; shift 2 ;;
    --skip-if-unavailable) SKIP_IF_UNAVAILABLE=1; shift ;;
    -h|--help)   grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

c_b='\033[0;34m'; c_g='\033[0;32m'; c_r='\033[0;31m'; c_d='\033[2m'; c_n='\033[0m'
say()  { printf "${c_b}▸ %s${c_n}\n" "$*"; }
ok()   { printf "${c_g}✓ %s${c_n}\n" "$*"; }
die()  { printf "${c_r}✗ %s${c_n}\n" "$*" >&2; exit 1; }

# unavailable — same contract as scripts/smoke-liveusb.sh: without
# --skip-if-unavailable this is a hard failure. With it, a LOUD, itemised
# skip that names every claim this run therefore did NOT verify, then exits
# 0 — never a silent green.
unavailable() {
  if [ "$SKIP_IF_UNAVAILABLE" != "1" ]; then
    die "$*"
  fi
  printf "\n${c_r}%s${c_n}\n" "════════════════════════════════════════════════════════════════"
  printf "${c_r}SKIPPED — NETB-05 (netboot-install QEMU boot) DID NOT RUN${c_n}\n"
  printf "${c_r}%s${c_n}\n" "════════════════════════════════════════════════════════════════"
  printf "Reason: %s\n\n" "$*"
  printf "The following were NOT verified by this run:\n"
  printf "  - the real netboot-install Go pipeline succeeds against a real disk\n"
  printf "  - bootctl's --path/--root invocation actually works (NETB-05 found it did not)\n"
  printf "  - the vulos-live hook finds the slot-a squashfs via vulos.squashfs= and boots it\n"
  printf "  - the installed machine reaches a fully running Vulos OS on second boot\n\n"
  printf "Run on a host with docker + qemu-system-aarch64 + OVMF for real coverage:\n"
  printf "  bash scripts/netboot-install-smoke.sh\n\n"
  exit 0
}

# ── Tool-guard ────────────────────────────────────────────────────────────────
command -v docker >/dev/null 2>&1 \
  || unavailable "docker not found — needed to build the live image and run the real install pipeline (OrbStack or Docker Desktop)"
command -v qemu-system-aarch64 >/dev/null 2>&1 \
  || unavailable "qemu-system-aarch64 not found — install QEMU (e.g. brew install qemu)"
[ -f "$EDK2_CODE" ] \
  || unavailable "UEFI firmware not found: $EDK2_CODE"
[ -f "$EDK2_VARS_SRC" ] \
  || unavailable "UEFI vars template not found: $EDK2_VARS_SRC"

mkdir -p "$OUTDIR"

# Keep this SHORT (< 104 bytes) — a long unix-socket path makes QEMU fail to
# start in a way that looks exactly like a boot failure.
QMP="$OUTDIR/_nb-qmp.sock"
SERIAL="$OUTDIR/_netboot-serial.log"
DISK_IMG="$OUTDIR/_netboot-installed-vda.img"

# ── Phase 1: build the --live artifact with the fixed vulos-live hook ────────
if [ "$NO_BUILD" = "0" ]; then
  say "Building builder image (cached after first run; now also carries systemd-boot for Phase 2)…"
  docker build -q -t "$BUILDER_IMG" -f "$BUILDER_DF" "$REPO/scripts" >/dev/null

  REUSE="--reuse-rootfs"
  [ "$REBUILD" = "1" ] && REUSE=""
  say "Building Vulos OS --live squashfs (build.sh --arm64 --live ${REUSE:-(full rebuild)})…"
  say "  scripts/initramfs/vulos-live is copied into the rootfs and baked into the"
  say "  initramfs via update-initramfs during this step — the SAME fixed hook that"
  say "  Phase 3 boots, not a stand-in."
  docker run --rm --privileged \
    -v "$REPO":/src -w /src \
    -v vulos-bm-work:/work \
    -v vulos-bm-gocache:/root/.cache/go-build \
    -v vulos-bm-gomod:/go/pkg/mod \
    -v vulos-bm-npm:/root/.npm \
    "$BUILDER_IMG" \
    bash -c "mkdir -p /work/output && ./build.sh --arm64 --live $REUSE /work/output"
fi
ok "Phase 1 done — image.squashfs + kernel + initramfs are in the vulos-bm-work volume"

# ── Phase 2: run the REAL netboot-install pipeline against a real disk ───────
say "Running the real netboot-install pipeline (TestNetbootInstall_RealPipeline_E2E) —"
say "  real parted/mkfs.vfat/mkfs.ext4/mount/bootctl/cp, no mocks, inside a privileged container…"

rm -f "$DISK_IMG"

docker run --rm --privileged \
  -v "$REPO":/src -w /src/backend \
  -v vulos-bm-work:/work \
  -v vulos-bm-gocache:/root/.cache/go-build \
  -v vulos-bm-gomod:/go/pkg/mod \
  "$BUILDER_IMG" \
  bash -c '
    set -euo pipefail

    SQUASHFS="/work/output/image.squashfs"
    [ -f "$SQUASHFS" ] || { echo "FATAL: $SQUASHFS not found — did Phase 1 run? (try without --no-build)" >&2; exit 1; }

    KIMG="$(ls -1 /work/output/rootfs/boot/vmlinuz-* 2>/dev/null | sort -V | tail -1)"
    IIMG="$(ls -1 /work/output/rootfs/boot/initrd.img-* 2>/dev/null | sort -V | tail -1)"
    [ -n "$KIMG" ] || { echo "FATAL: no kernel found under /work/output/rootfs/boot" >&2; exit 1; }
    [ -n "$IIMG" ] || { echo "FATAL: no initrd found under /work/output/rootfs/boot" >&2; exit 1; }
    echo "kernel:   $KIMG"
    echo "initramfs: $IIMG"

    # Seed source files the install pipeline reads from well-known absolute
    # paths on "the running live system" (backend/services/installer/
    # netboot_install.go doc comment). This container stands in for that
    # live/RAM session: these are the exact paths, populated for real.
    mkdir -p /boot /etc/vulos
    cp "$KIMG" /boot/vmlinuz
    cp "$IIMG" /boot/initramfs-vulos.img
    echo "http://os.invalid.example/" > /etc/vulos/os-bucket-url

    # ── Real loop-backed disk, standing in for a real /dev/vda ────────────────
    dd if=/dev/zero of=/work/output/_netboot-disk.img bs=1M count=1400 status=none
    LOOPDEV="$(losetup -P -f --show /work/output/_netboot-disk.img)"
    echo "loop device: $LOOPDEV"
    ln -sf "$LOOPDEV" /dev/vda
    cleanup() { losetup -d "$LOOPDEV" 2>/dev/null || true; }
    trap cleanup EXIT

    # partSuffix() in backend/services/installer only special-cases nvme/mmcblk
    # names for the kernel-partition "pN" naming; "vda" (matching real virtio
    # disks) resolves to plain "1"/"2", so the real code expects /dev/vda1 and
    # /dev/vda2 to exist directly. parted (run BY the pipeline itself, inside
    # the Go test) makes the kernel register the partitions — confirmed via
    # /sys/class/block/loop0/loop0p{1,2} — but this container has no udev
    # ("udevadm: not found" earlier in this same build), and udev is what
    # normally turns that kernel-side registration into a /dev device node.
    # Without it /dev/loop0p1 never appears no matter how long you poll for
    # it — the FIRST run of this harness did exactly that and timed out.
    # mknod the nodes directly from the sysfs-reported major:minor instead,
    # then symlink them to the plain vda1/vda2 names the pipeline expects.
    # Real hardware never needs any of this — it is purely this loop-backed
    # stand-in for a real /dev/vda.
    (
      for _ in $(seq 1 150); do
        if [ -e "/sys/class/block/${LOOPDEV#/dev/}p1/dev" ] && [ -e "/sys/class/block/${LOOPDEV#/dev/}p2/dev" ]; then
          for p in 1 2; do
            devnum="$(cat "/sys/class/block/${LOOPDEV#/dev/}p${p}/dev")"
            maj="${devnum%%:*}"; min="${devnum##*:}"
            [ -e "${LOOPDEV}p${p}" ] || mknod "${LOOPDEV}p${p}" b "$maj" "$min"
            ln -sf "${LOOPDEV}p${p}" "/dev/vda${p}"
          done
          exit 0
        fi
        sleep 0.2
      done
      echo "FATAL: /sys/class/block/${LOOPDEV#/dev/}p1/dev never appeared — parted did not register partitions" >&2
      exit 1
    ) &
    LINKER_PID=$!

    export VULOS_NETBOOT_E2E=1
    export VULOS_E2E_DISK=vda
    export VULOS_E2E_SQUASHFS="$SQUASHFS"
    # -count=1 is NOT optional. This test partitions and formats a real
    # loop-backed disk; Go happily served a CACHED pass for it, so Phase 2
    # reported "real install pipeline succeeded" without executing a single
    # disk operation, and then shipped whatever stale image was lying around.
    go test ./services/installer/... -run TestNetbootInstall_RealPipeline_E2E -count=1 -v -timeout 10m
    TESTRC=$?

    # A linker failure is FATAL, not advisory. It was `|| true`, which
    # swallowed its exit status — so when parted failed to register partitions
    # it printed "FATAL: ... never appeared", the run carried on regardless, and
    # an image with NO PARTITION TABLE was copied out and declared a success.
    LINKRC=0
    wait "$LINKER_PID" 2>/dev/null || LINKRC=$?
    cleanup
    trap - EXIT

    if [ "$LINKRC" -ne 0 ]; then
      echo "FATAL: the partition linker failed (rc=$LINKRC) — the disk was never partitioned, so nothing below is meaningful" >&2
      exit 1
    fi
    if [ "$TESTRC" -ne 0 ]; then
      exit "$TESTRC"
    fi

    # Post-condition: what we are about to hand to QEMU must actually BE a
    # partitioned disk. Everything upstream can report success and still leave a
    # blank 1.4G file here — that is exactly what happened, and the only symptom
    # from the firmware was dropping to an EFI shell, which reads like a
    # bootloader problem rather than a missing GPT.
    if ! parted -s /work/output/_netboot-disk.img print >/dev/null 2>&1; then
      echo "FATAL: the produced image has no readable partition table — refusing to call this an install" >&2
      parted -s /work/output/_netboot-disk.img unit s print 2>&1 | head -6 >&2 || true
      exit 1
    fi

    cp /work/output/_netboot-disk.img /src/output/_netboot-installed-vda.img
    echo "installed disk image copied to output/_netboot-installed-vda.img"
  '

[ -f "$DISK_IMG" ] || die "install pipeline did not produce $DISK_IMG"
# Cheap GPT sniff on the host: byte 512.. of a GPT disk starts "EFI PART".
if ! dd if="$DISK_IMG" bs=1 skip=512 count=8 2>/dev/null | grep -q "EFI PART"; then
  die "the copied disk image has no GPT header — Phase 2 produced something QEMU cannot boot"
fi
ok "Phase 2 done — real install pipeline succeeded; disk image: $DISK_IMG ($(du -h "$DISK_IMG" | cut -f1)), GPT present"

# ── Phase 3: boot the installed disk in QEMU and assert real success ────────
#
# The UEFI vars file is ALWAYS a fresh copy, never reused across runs. A
# reused vars.fd persists whatever NVRAM state a PREVIOUS boot attempt left —
# including a failed one — and this harness hit that directly: after a
# failed boot, OVMF fell back to its interactive "UiApp" firmware setup
# screen on every subsequent run against the carried-over vars.fd, hanging
# forever with no serial output past the firmware banner, which looks
# identical to "the disk isn't bootable" but has nothing to do with this
# disk's actual contents. A byte-for-byte fresh copy every run is the only
# way this harness's verdict is about the disk under test, not about
# leftover firmware state from the previous one.
VARS="$OUTDIR/_netboot-uefi-vars.fd"
cp "$EDK2_VARS_SRC" "$VARS"
: > "$SERIAL"
rm -f "$QMP"

QEMU_PID=""
cleanup_qemu() { [ -n "$QEMU_PID" ] && kill "$QEMU_PID" 2>/dev/null || true; rm -f "$QMP"; }
trap cleanup_qemu EXIT INT TERM

DISPLAY_ARGS="-display none -vga none"
[ "$SHOW" = "1" ] && DISPLAY_ARGS="-display cocoa"

say "Booting the netboot-installed disk (QEMU aarch64/HVF)…  serial → $SERIAL"
say "  This is the SECOND boot — from the disk the install pipeline just wrote,"
say "  not the --live medium. It is the one dfff94f5 and this task's other"
say "  fixes exist to make work."

qemu-system-aarch64 \
  -machine virt,gic-version=3 -accel hvf -cpu host -smp 4 -m "${VM_MEM_MB:-4096}" \
  -drive if=pflash,format=raw,readonly=on,file="$EDK2_CODE" \
  -drive if=pflash,format=raw,file="$VARS" \
  -drive if=virtio,format=raw,file="$DISK_IMG" \
  -device virtio-net-pci,netdev=n0 \
  -netdev user,id=n0,hostfwd=tcp:127.0.0.1:${HOSTPORT}-:8080 \
  -qmp unix:"$QMP",server,nowait \
  -serial "file:$SERIAL" \
  $DISPLAY_ARGS \
  -no-reboot &
QEMU_PID=$!

qmp() {
  python3 - "$QMP" "$1" <<'PY'
import json, socket, sys, time
sock, cmd = sys.argv[1], sys.argv[2]
s = socket.socket(socket.AF_UNIX)
for _ in range(60):
    try: s.connect(sock); break
    except OSError: time.sleep(0.2)
else: sys.exit(1)
f = s.makefile('rw')
f.readline()
def call(o): f.write(json.dumps(o) + "\r\n"); f.flush(); return f.readline()
call({"execute": "qmp_capabilities"})
print(call(json.loads(cmd)), end="")
PY
}

# PASS is decided SOLELY by the same gold-standard HTTP check
# scripts/smoke-liveusb.sh and scripts/baremetal-smoke.sh use: the OS is
# fully running and vulos-server is actually routing, not just that the
# console printed something that looked like success. Serial evidence of the
# vulos-live overlay activating is logged for diagnosis only and never flips
# PASS by itself.
OVERLAY_PATTERNS='vulos-live: live overlayfs active\|vulos-live: opening dm-verity\|vulos-live: failed\|vulos-live: veritysetup\|Attempted to kill init'
HTTP_URL="http://127.0.0.1:${HOSTPORT}/api/setup/status"

say "Waiting up to ${TIMEOUT}s for HTTP (serial overlay activation logged for diagnostics only)…"
deadline=$(( $(date +%s) + TIMEOUT ))
PASS=0
PASS_MSG=""
OVERLAY_SEEN=0
QEMU_DIED=0
while [ "$(date +%s)" -lt "$deadline" ]; do
  if ! kill -0 "$QEMU_PID" 2>/dev/null; then
    printf "${c_r}  QEMU process exited early${c_n}\n" >&2
    QEMU_DIED=1
    break
  fi

  if [ "$OVERLAY_SEEN" = "0" ] && grep -qsE "$OVERLAY_PATTERNS" "$SERIAL" 2>/dev/null; then
    OVERLAY_SEEN=1
    printf "${c_d}  (serial) vulos-live overlay activity seen — still waiting on HTTP to confirm the backend is actually up…${c_n}\n"
  fi

  if curl -fsS --max-time 3 "$HTTP_URL" >/dev/null 2>&1; then
    PASS=1
    PASS_MSG="HTTP /api/setup/status reachable — netboot-installed OS fully booted on its second (local-disk) boot"
    break
  fi

  elapsed=$(( $(date +%s) - (deadline - TIMEOUT) ))
  printf "${c_d}  … %ds elapsed\r${c_n}" "$elapsed"
  sleep 3
done
echo

SHOT="$OUTDIR/_netboot-screen.ppm"
qmp "{\"execute\":\"screendump\",\"arguments\":{\"filename\":\"$SHOT\"}}" >/dev/null 2>&1 \
  && [ -f "$SHOT" ] && ok "screenshot: $SHOT" || true

if [ "$PASS" = "1" ]; then
  ok "PASS — $PASS_MSG"
  # `|| true`: under `set -euo pipefail` this pipeline returns non-zero whenever
  # grep matches nothing, which killed the script immediately after it had
  # printed PASS — the run looked like a pass and exited 1. These lines are
  # diagnostic only and must never decide the verdict.
  { grep -E "$OVERLAY_PATTERNS" "$SERIAL" 2>/dev/null | tail -5 || true; } | while IFS= read -r ln; do
    printf "  ${c_d}serial: %s${c_n}\n" "$ln"
  done
  if [ "$SHOW" = "1" ]; then
    echo ""
    say "QEMU window is open — press Ctrl-C to stop."
    wait "$QEMU_PID"
    exit 0
  fi
  qmp '{"execute":"quit"}' >/dev/null 2>&1 || true
  sleep 2
  PHASE3_OK=1
else
  printf "${c_r}✗ FAIL — netboot-installed OS did not reach a sane state within ${TIMEOUT}s${c_n}\n" >&2
  echo "" >&2
  if [ "$QEMU_DIED" = "1" ]; then
    echo "Failed signal: QEMU exited before the timeout — the VM process died." >&2
  elif [ "$OVERLAY_SEEN" = "1" ]; then
    echo "Failed signal: the vulos-live hook was reached and logged overlay activity, but" >&2
    echo "HTTP /api/setup/status never became reachable — the boot chain got past the" >&2
    echo "squashfs mount but vulos-server never came up. Check the serial log below." >&2
  else
    echo "Failed signal: no vulos-live activity seen on serial at all — the boot chain" >&2
    echo "never reached the hook (systemd-boot entry, kernel/initrd, or root= mount" >&2
    echo "problem), or the hook exited silently before logging anything." >&2
  fi
  echo "" >&2
  echo "---- last 80 lines of serial ($SERIAL) ----" >&2
  tail -n 80 "$SERIAL" >&2 || true
  echo "" >&2
  qmp '{"execute":"quit"}' >/dev/null 2>&1 || true
  exit 1
fi

# ── Phase 4: OSDIST-FLIP-01 — flip the active slot and prove the flip BOOTS ──
#
# Phase 3 proves the machine boots slot-a, which is what the install pinned on
# the kernel command line. That says nothing about whether an OTA can ever take
# effect, and for a long time it could not: the cmdline is written once at
# install and boot-state.json was read by nothing at boot, so a staged update —
# and, worse, a boot-counter ROLLBACK — recorded intent that no boot honoured.
#
# The initramfs now reads boot-state.json and picks the slot itself. That is
# unit-tested, but a unit test cannot show that real firmware, a real
# systemd-boot entry and a real initramfs agree. Only a reboot can. So:
#
#   1. stage a REAL second image into slot-b (a copy of the slot-a squashfs and
#      its verity siblings — the point is which slot boots, not what differs
#      inside it),
#   2. flip "active" to b in boot-state.json,
#   3. boot the SAME disk again, unchanged otherwise, with a fresh vars.fd,
#   4. require BOTH that the initramfs says it selected slot b AND that the OS
#      comes all the way up to a serving HTTP endpoint from it.
#
# Requiring both matters. The serial line alone is our own log and would pass if
# the machine then failed to boot; HTTP alone would pass if it quietly booted
# slot-a. Together they say the flip was honoured and the result is a working
# system.
[ "${PHASE3_OK:-0}" = "1" ] || exit 0

echo ""
say "Phase 4 — staging slot-b and flipping boot-state.json to it…"

docker run --rm --privileged \
  -v "$OUTDIR":/out \
  "$BUILDER_IMG" \
  bash -c '
    set -euo pipefail
    IMG=/out/'"$(basename "$DISK_IMG")"'
    LOOP="$(losetup --find --show --partscan "$IMG")"
    trap "losetup -d $LOOP 2>/dev/null || true" EXIT

    # --partscan alone does not reliably materialise ${LOOP}p1/p2 in this
    # container: the kernel registers the partitions under /sys but the device
    # nodes are never created, so mount fails with "Cant lookup blockdev".
    # Phase 2 hits the same thing and solves it the same way — wait for /sys,
    # then mknod the nodes by their real major:minor.
    for _ in $(seq 1 150); do
      [ -e "/sys/class/block/${LOOP#/dev/}p2/dev" ] && break
      sleep 0.2
    done
    [ -e "/sys/class/block/${LOOP#/dev/}p2/dev" ] || { echo "FATAL: partitions never registered for $LOOP" >&2; exit 1; }
    for pnum in 1 2; do
      devnum="$(cat "/sys/class/block/${LOOP#/dev/}p${pnum}/dev")"
      [ -e "${LOOP}p${pnum}" ] || mknod "${LOOP}p${pnum}" b "${devnum%%:*}" "${devnum##*:}"
    done

    MNT=/mnt/vulos-flip
    mkdir -p "$MNT"
    # p2 is the ext4 root the installer created (p1 is the ESP).
    mount "${LOOP}p2" "$MNT"
    trap "umount $MNT 2>/dev/null || true; losetup -d $LOOP 2>/dev/null || true" EXIT

    SA="$MNT/var/cache/vulos/slot-a"
    SB="$MNT/var/cache/vulos/slot-b"
    [ -f "$SA/os-core.squashfs" ] || { echo "FATAL: slot-a has no squashfs — Phase 2 did not lay out the slots" >&2; exit 1; }
    mkdir -p "$SB"
    # HARDLINK, not copy. The root partition the installer creates has no room
    # for a second 593MB squashfs — a real copy fails with ENOSPC — and that is
    # itself worth knowing: on a disk this size an A/B update has nowhere to
    # stage. It does not weaken what this phase tests. The question here is
    # WHICH SLOT THE FIRMWARE AND INITRAMFS CHOOSE, and a hardlink is a real
    # file at the slot-b path: -f succeeds, the mount succeeds, and the boot
    # either goes through slot-b or it does not. What the bytes contain is not
    # under test.
    for f in os-core.squashfs os-core.hashtree os-core.roothash os-core.roothash.sig; do
      [ -f "$SA/$f" ] && ln -f "$SA/$f" "$SB/$f"
    done
    echo "slot-b staged: $(ls -1 "$SB" | tr "\n" " ")"

    BS="$MNT/var/cache/vulos/boot-state.json"
    [ -f "$BS" ] || { echo "FATAL: no boot-state.json on the installed disk" >&2; exit 1; }
    echo "  before: $(cat "$BS")"
    printf "{\"active\":\"b\",\"pending\":\"\",\"boot_counter\":0,\"last_known_good\":\"a\"}" > "$BS"
    echo "  after:  $(cat "$BS")"
    sync
  ' || { printf "${c_r}✗ Phase 4 setup failed${c_n}\n" >&2; exit 1; }

ok "slot-b staged and boot-state.json flipped to active=b"

VARS2="$OUTDIR/_netboot-uefi-vars-slotb.fd"
cp "$EDK2_VARS_SRC" "$VARS2"
SERIAL2="$OUTDIR/_netboot-serial-slotb.log"
: > "$SERIAL2"
QMP2="$OUTDIR/_netboot-qmp-slotb.sock"
rm -f "$QMP2"
HOSTPORT2=$(( HOSTPORT + 1 ))

say "Booting the SAME disk again — nothing changed but boot-state.json…"
qemu-system-aarch64 \
  -machine virt,gic-version=3 -accel hvf -cpu host -smp 4 -m "${VM_MEM_MB:-4096}" \
  -drive if=pflash,format=raw,readonly=on,file="$EDK2_CODE" \
  -drive if=pflash,format=raw,file="$VARS2" \
  -drive if=virtio,format=raw,file="$DISK_IMG" \
  -device virtio-net-pci,netdev=n1 \
  -netdev user,id=n1,hostfwd=tcp:127.0.0.1:${HOSTPORT2}-:8080 \
  -qmp unix:"$QMP2",server,nowait \
  -serial "file:$SERIAL2" \
  -display none -vga none \
  -no-reboot &
QEMU2_PID=$!
cleanup_qemu2() { [ -n "${QEMU2_PID:-}" ] && kill "$QEMU2_PID" 2>/dev/null || true; rm -f "$QMP2"; }
trap 'cleanup_qemu; cleanup_qemu2' EXIT INT TERM

HTTP2="http://127.0.0.1:${HOSTPORT2}/api/setup/status"
deadline=$(( $(date +%s) + TIMEOUT ))
HTTP2_OK=0
while [ "$(date +%s)" -lt "$deadline" ]; do
  kill -0 "$QEMU2_PID" 2>/dev/null || { printf "${c_r}  QEMU (slot-b boot) exited early${c_n}\n" >&2; break; }
  if curl -fsS --max-time 3 "$HTTP2" >/dev/null 2>&1; then HTTP2_OK=1; break; fi
  sleep 3
done
echo

# Stop the VM and read the marker the initramfs wrote. The serial cannot answer
# this: the installed entry carries `quiet splash`, so plymouth swallows every
# initramfs line — BOTH boots log zero of them, and Phase 3 passes on HTTP
# alone. The on-disk record is written before switch-root and survives the
# shutdown, so it says which slot this boot actually used rather than which one
# we hoped it used.
kill "$QEMU2_PID" 2>/dev/null || true
wait "$QEMU2_PID" 2>/dev/null || true
sleep 2

BOOTED="$(docker run --rm --privileged -v "$OUTDIR":/out "$BUILDER_IMG" bash -c '
  set -euo pipefail
  IMG=/out/'"$(basename "$DISK_IMG")"'
  LOOP="$(losetup --find --show --partscan "$IMG")"
  trap "losetup -d $LOOP 2>/dev/null || true" EXIT
  for _ in $(seq 1 150); do [ -e "/sys/class/block/${LOOP#/dev/}p2/dev" ] && break; sleep 0.2; done
  devnum="$(cat "/sys/class/block/${LOOP#/dev/}p2/dev")"
  [ -e "${LOOP}p2" ] || mknod "${LOOP}p2" b "${devnum%%:*}" "${devnum##*:}"
  MNT=/mnt/vulos-read; mkdir -p "$MNT"; mount -o ro "${LOOP}p2" "$MNT"
  cat "$MNT/var/cache/vulos/booted-slot" 2>/dev/null || echo "MISSING"
  umount "$MNT" 2>/dev/null || true
' 2>/dev/null)"

echo "$BOOTED" | sed 's/^/      marker: /'
SLOT_SEEN=0
case "$BOOTED" in *"slot=b"*) SLOT_SEEN=1 ;; esac

if [ "$SLOT_SEEN" = "1" ] && [ "$HTTP2_OK" = "1" ]; then
  ok "PASS — the flip is REAL: boot-state.json said slot b, the initramfs honoured it,"
  ok "       the machine came up serving HTTP, and the disk records slot=b."
  PHASE4_OK=1
fi

if [ "${PHASE4_OK:-0}" != "1" ]; then

# "I could not tell" is not "it is broken". If the marker is absent the boot
# left no evidence either way — asserting failure there would be as dishonest as
# asserting success. Only a marker that positively names the WRONG slot proves
# the flip did not happen.
case "$BOOTED" in
  *MISSING*|"")
    printf "${c_y:-}⚠ INCONCLUSIVE — the machine booted and served HTTP with active=b, but the${c_n}\n" >&2
    printf "  boot left no record of which slot it used, so this proves nothing either way.${c_n}\n" >&2
    echo "" >&2
    echo "  The initramfs writes /var/cache/vulos/booted-slot before switch-root; it is" >&2
    echo "  absent here. Known dead ends already ruled out: the built hook DOES contain" >&2
    echo "  the marker code, and the write is wrapped in a remount,rw (the cmdline says" >&2
    echo "  ro, and a plain write silently failed). Still unexplained — most likely the" >&2
    echo "  hook never reaches that line on this boot, or \$rootmnt is not the partition" >&2
    echo "  being read back afterwards." >&2
    echo "" >&2
    echo "  The serial cannot substitute: the entry carries 'quiet splash', so plymouth" >&2
    echo "  swallows every initramfs line on BOTH boots (phase 3 passes on HTTP alone)." >&2
    echo "" >&2
    echo "---- last 40 lines of slot-b serial ($SERIAL2) ----" >&2
    tail -n 40 "$SERIAL2" >&2 || true
    exit 2
    ;;
esac

printf "${c_r}✗ FAIL — the A/B slot flip did not take effect${c_n}\n" >&2
echo "" >&2
if [ "$SLOT_SEEN" = "0" ]; then
  echo "The disk records that this boot did NOT use slot b. boot-state.json said active=b," >&2
  echo "so either apply_active_slot did not run, or it fell through one of its" >&2
  echo "fail-closed paths (see scripts/initramfs/vulos-live) and kept the cmdline" >&2
  echo "slot — which is precisely the bug this phase exists to catch." >&2
elif [ "$HTTP2_OK" = "0" ]; then
  echo "The initramfs DID select slot b, but the OS never reached a serving state" >&2
  echo "from it — so the flip is honoured and the slot-b image is not bootable." >&2
fi
echo "" >&2
echo "---- last 60 lines of slot-b serial ($SERIAL2) ----" >&2
tail -n 60 "$SERIAL2" >&2 || true
kill "$QEMU2_PID" 2>/dev/null || true
exit 1

fi   # end: Phase 4 did not pass

# ── Phase 5: VERITY-03 — is dm-verity actually ACTIVE on the installed disk? ──
#
# ARCHITECTURE.md says dm-verity enforces block-level integrity at runtime via
# the initramfs. On a netboot-installed disk that was simply untrue, and nothing
# noticed for months, because every layer reported success: the install passed,
# the machine booted, HTTP answered. The protection was not running.
#
# It failed for two independent reasons, and fixing either alone changes
# nothing, so this phase checks the whole chain rather than any one link:
#
#   1. the installer never staged os-core.hashtree/os-core.roothash beside the
#      squashfs it copied into the slot (fixed — backend/services/installer/
#      netboot_verity.go),
#   2. veritysetup and the dm-verity kernel driver are not IN the initramfs
#      (a rootfs/build change; NOT fixed here).
#
# Assertions, in order of what they can prove:
#
#   A. the verity siblings are in slot-a at the names the hook derives from
#      vulos.squashfs= — a rename here silently disables verity,
#   B. `veritysetup verify` passes against the STAGED trio, on the disk, after
#      the copy — the installer verified the SOURCE, this verifies what actually
#      landed,
#   C. the boot recorded verity=active in /var/cache/vulos/booted-slot.
#
# C is read from the disk, not the serial, for the same reason Phase 4 is: the
# installed entry carries `quiet splash`, so plymouth swallows every initramfs
# line and BOTH boots log none of them.
echo ""
say "Phase 5 — dm-verity on the installed disk (staged artifacts, then whether the boot used them)…"

VERITY_REPORT="$OUTDIR/_netboot-verity-report.txt"
docker run --rm --privileged -v "$OUTDIR":/out "$BUILDER_IMG" bash -c '
    set -euo pipefail
    IMG=/out/'"$(basename "$DISK_IMG")"'
    LOOP="$(losetup --find --show --partscan "$IMG")"
    trap "losetup -d $LOOP 2>/dev/null || true" EXIT
    for _ in $(seq 1 150); do [ -e "/sys/class/block/${LOOP#/dev/}p2/dev" ] && break; sleep 0.2; done
    for pnum in 1 2; do
      devnum="$(cat "/sys/class/block/${LOOP#/dev/}p${pnum}/dev")"
      [ -e "${LOOP}p${pnum}" ] || mknod "${LOOP}p${pnum}" b "${devnum%%:*}" "${devnum##*:}"
    done
    MNT=/mnt/vulos-verity; mkdir -p "$MNT"
    mount -o ro "${LOOP}p2" "$MNT"
    ESP=/mnt/vulos-esp; mkdir -p "$ESP"
    mount -o ro "${LOOP}p1" "$ESP"
    trap "umount $ESP 2>/dev/null || true; umount $MNT 2>/dev/null || true; losetup -d $LOOP 2>/dev/null || true" EXIT

    SA="$MNT/var/cache/vulos/slot-a"

    # A — the artifacts, at the names the initramfs derives from the boot entry.
    for f in os-core.squashfs os-core.hashtree os-core.roothash; do
      if [ ! -f "$SA/$f" ]; then
        echo "STAGED=no MISSING=$f"
        exit 0
      fi
    done

    # B — the staged tree really describes the staged image. The installer
    # verified the source files; this verifies the bytes that survived the copy.
    # Failing here is not a warning: those three files together are what the
    # initramfs will hand to the kernel, and a mismatch is a panic at boot.
    RH="$(tr -d " \t\r\n" < "$SA/os-core.roothash")"
    if veritysetup verify "$SA/os-core.squashfs" "$SA/os-core.hashtree" "$RH" >/tmp/vs.log 2>&1; then
      echo "STAGED=yes VERIFIES=yes ROOTHASH=$RH"
    else
      echo "STAGED=yes VERIFIES=no ROOTHASH=$RH"
      sed "s/^/    veritysetup: /" /tmp/vs.log
    fi

    # Capability of the initramfs that will actually run at boot — reason #2.
    #
    # unmkinitramfs, not cpio: a real initramfs here is an uncompressed early
    # archive with a gzip one concatenated after it, so `cpio -t` sees only the
    # first and `gzip -dc` sees neither. Either would report "absent" for
    # everything in the main archive on every run — a diagnostic that cannot
    # change is worse than none, because it reads as evidence.
    # (this unmkinitramfs takes no -l, so extract and list; ~50 MiB, a few
    # seconds. Checked against a deliberately verity-capable initramfs, where it
    # reports yes/yes — a probe that can only ever say "no" proves nothing.)
    IR="$ESP/EFI/vulos/initramfs.img"
    NAMES=/tmp/ir-names
    mkdir -p /tmp/ir
    if unmkinitramfs "$IR" /tmp/ir >/dev/null 2>&1 && find /tmp/ir > "$NAMES" && [ "$(wc -l < "$NAMES")" -gt 100 ]; then
      grep -q "sbin/veritysetup" "$NAMES" && echo "IR_VERITYSETUP=yes" || echo "IR_VERITYSETUP=no"
      grep -q "dm-verity"       "$NAMES" && echo "IR_DMVERITY=yes"    || echo "IR_DMVERITY=no"
    else
      echo "IR_VERITYSETUP=unknown IR_DMVERITY=unknown (could not unpack $IR)"
    fi

    # C — what the boot itself recorded.
    grep "^verity=" "$MNT/var/cache/vulos/booted-slot" 2>/dev/null || echo "verity=UNRECORDED"
  ' > "$VERITY_REPORT" 2>&1 || { cat "$VERITY_REPORT" >&2; die "Phase 5 inspection failed"; }

sed 's/^/      /' "$VERITY_REPORT"

grep -q "STAGED=yes" "$VERITY_REPORT" || {
  printf "${c_r}✗ FAIL — the installer did not stage the dm-verity artifacts into slot-a${c_n}\n" >&2
  echo "" >&2
  echo "Without os-core.hashtree + os-core.roothash beside the staged squashfs, the" >&2
  echo "initramfs hook has nothing to open and every installed machine mounts its OS" >&2
  echo "through a plain loop device — which is the exact defect VERITY-03 closed." >&2
  echo "See stageVerityArtifacts in backend/services/installer/netboot_verity.go." >&2
  exit 1
}
grep -q "VERIFIES=yes" "$VERITY_REPORT" || {
  printf "${c_r}✗ FAIL — the STAGED hash tree does not verify against the STAGED squashfs${c_n}\n" >&2
  echo "" >&2
  echo "This is the dangerous state: the hook panics when veritysetup open fails, so a" >&2
  echo "disk in this condition does not boot at all. The installer verified the SOURCE" >&2
  echo "artifacts before copying, so a failure here means the copy did not survive." >&2
  exit 1
}
ok "slot-a carries os-core.hashtree + os-core.roothash, and they verify against the staged image"

if grep -q "^verity=active" "$VERITY_REPORT"; then
  ok "PASS — dm-verity is ACTIVE on the installed disk: the boot opened the verity"
  ok "       device and recorded it, and the machine came up serving HTTP."
  exit 0
fi

if grep -q "verity=UNRECORDED" "$VERITY_REPORT"; then
  printf "${c_r}⚠ INCONCLUSIVE — this boot left no verity= record${c_n}\n" >&2
  echo "The hook writes it beside slot=/via=/image= in /var/cache/vulos/booted-slot." >&2
  echo "An initramfs built before that field existed would look exactly like this." >&2
  exit 2
fi

# verity=inactive with correct artifacts staged: the remaining cause. Say
# precisely which prerequisite the initramfs is missing rather than "verity is
# off", because those are different bugs with different owners.
printf "${c_r}✗ dm-verity is NOT ACTIVE — the artifacts are staged and correct, the initramfs cannot use them${c_n}\n" >&2
echo "" >&2
grep -E "^IR_" "$VERITY_REPORT" | sed 's/^/  /' >&2
echo "" >&2
echo "The installer side is done: slot-a holds a hash tree and root hash that verify" >&2
echo "against the staged image. What is missing is in the OS IMAGE BUILD:" >&2
echo "" >&2
echo "  1. cryptsetup-bin installed into the rootfs (provides /sbin/veritysetup)" >&2
echo "  2. an initramfs-tools hook that runs copy_exec /sbin/veritysetup" >&2
echo "  3. dm-mod, dm-bufio, dm-verity and reed_solomon in the initramfs modules" >&2
echo "     list — MODULES=most does NOT include drivers/md, and dm-verity.ko" >&2
echo "     without dm-bufio/reed_solomon loads with 'Unknown symbol' errors" >&2
echo "" >&2
echo "All three live in build.sh. Until they land, scripts/initramfs/vulos-live takes" >&2
echo "its documented unverified loop-mount fallback and the machine boots normally —" >&2
echo "verified by this very run — but block-level integrity is not enforced, and" >&2
echo "docs/ARCHITECTURE.md's claim that it is remains false for installed disks." >&2
exit 3

