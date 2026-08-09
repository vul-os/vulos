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
    go test ./services/installer/... -run TestNetbootInstall_RealPipeline_E2E -v -timeout 10m
    TESTRC=$?

    wait "$LINKER_PID" 2>/dev/null || true
    cleanup
    trap - EXIT

    if [ "$TESTRC" -ne 0 ]; then
      exit "$TESTRC"
    fi

    cp /work/output/_netboot-disk.img /src/output/_netboot-installed-vda.img
    echo "installed disk image copied to output/_netboot-installed-vda.img"
  '

[ -f "$DISK_IMG" ] || die "install pipeline did not produce $DISK_IMG"
ok "Phase 2 done — real install pipeline succeeded; disk image: $DISK_IMG ($(du -h "$DISK_IMG" | cut -f1))"

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
  grep -E "$OVERLAY_PATTERNS" "$SERIAL" 2>/dev/null | tail -5 | while IFS= read -r ln; do
    printf "  ${c_d}serial: %s${c_n}\n" "$ln"
  done
  if [ "$SHOW" = "1" ]; then
    echo ""
    say "QEMU window is open — press Ctrl-C to stop."
    wait "$QEMU_PID"
  else
    qmp '{"execute":"quit"}' >/dev/null 2>&1 || true
  fi
  exit 0
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
