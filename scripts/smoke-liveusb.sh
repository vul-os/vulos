#!/usr/bin/env bash
# Vulos OS — live-USB UEFI QEMU smoke harness
#
# Boots the --live image produced by build.sh --live inside QEMU with OVMF
# (x86_64 UEFI) and asserts the system reaches a sane post-boot state:
#
#   OVMF (UEFI) → systemd-boot → kernel → initramfs vulos-live hook
#     (pivot to squashfs+overlayfs) → login prompt or first-boot wizard
#
# The test watches the serial console for the pivot (progress/diagnostic only)
# but PASS is decided solely by a reachable HTTP endpoint — the gold-standard
# "fully running" check — and exits 0 on success, 1 on failure.
#
# Usage:
#   scripts/smoke-liveusb.sh                  # CI: boot headless, assert, exit 0/1
#   scripts/smoke-liveusb.sh --show           # open QEMU window (requires a display)
#   scripts/smoke-liveusb.sh --no-build       # boot an existing image; skip build
#   scripts/smoke-liveusb.sh --rebuild        # force a full rootfs rebuild (slow)
#   scripts/smoke-liveusb.sh --timeout 480
#   scripts/smoke-liveusb.sh --arch arm64     # override target arch (default: amd64)
#
# Tool requirements (tool-guarded; tested before anything runs):
#   - qemu-system-x86_64  (or qemu-system-aarch64 for --arch arm64)
#   - OVMF / edk2 firmware (.fd files bundled with QEMU; see EDK2_* vars below)
#
# Default reuses output/ across runs so the fix-test loop only re-runs QEMU.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
OUTDIR="$REPO/output"
ARCH="${ARCH:-amd64}"
BUILDER_IMG="vulos-baremetal-builder"
BUILDER_DF="$REPO/scripts/baremetal-builder.Dockerfile"

HOSTPORT="${HOSTPORT:-8089}"
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
    --arch)      ARCH="$2"; shift 2 ;;
    # For CI on runners that cannot host a VM. Turns a MISSING-TOOL condition
    # (and only that) into a loud, itemised skip that exits 0. Every other
    # failure — a build error, a boot hang, a failed assertion — still exits 1.
    # This exists so the CI step does not need `|| true`, which would swallow
    # real regressions along with the unavailability.
    --skip-if-unavailable) SKIP_IF_UNAVAILABLE=1; shift ;;
    -h|--help)   grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

# ── Colours ──────────────────────────────────────────────────────────────────
c_b='\033[0;34m'; c_g='\033[0;32m'; c_r='\033[0;31m'; c_d='\033[2m'; c_n='\033[0m'
say()  { printf "${c_b}▸ %s${c_n}\n" "$*"; }
ok()   { printf "${c_g}✓ %s${c_n}\n" "$*"; }
die()  { printf "${c_r}✗ %s${c_n}\n" "$*" >&2; exit 1; }

# unavailable <what-is-missing> — the environment cannot host this test.
#
# Without --skip-if-unavailable this is a hard failure, exactly like die().
# With it, the test is skipped — but LOUDLY: it names the missing tool and
# spells out every assertion that was therefore NOT checked, so a green CI run
# can never be mistaken for "the live USB image boots".
unavailable() {
  if [ "$SKIP_IF_UNAVAILABLE" != "1" ]; then
    die "$*"
  fi
  printf "\n${c_r}%s${c_n}\n" "════════════════════════════════════════════════════════════════"
  printf "${c_r}SKIPPED — SMOKE-02 (live-USB QEMU boot) DID NOT RUN${c_n}\n"
  printf "${c_r}%s${c_n}\n" "════════════════════════════════════════════════════════════════"
  printf "Reason: %s\n\n" "$*"
  printf "The following were NOT verified by this run:\n"
  printf "  - the live image boots under UEFI to a usable state\n"
  printf "  - vulos-init runs and the backend reaches /health\n"
  printf "  - the seeded first-boot flow completes\n"
  printf "  - no kernel panic or boot hang on the target arch (%s)\n\n" "$ARCH"
  printf "Run on a host with QEMU + OVMF for real coverage:\n"
  printf "  bash scripts/smoke-liveusb.sh\n\n"
  exit 0
}

# ── Per-arch knobs ────────────────────────────────────────────────────────────
case "$ARCH" in
  amd64)
    QEMU_BIN="qemu-system-x86_64"
    EDK2_CODE="${EDK2_CODE_X64:-/opt/homebrew/share/qemu/edk2-x86_64-code.fd}"
    EDK2_VARS_SRC="${EDK2_VARS_X64:-/opt/homebrew/share/qemu/edk2-i386-vars.fd}"
    QEMU_MACHINE="-machine q35,accel=hvf -cpu host -smp 4 -m ${VM_MEM_MB:-4096}"
    QEMU_SERIAL="-serial file:${OUTDIR}/_live-serial.log"
    ;;
  arm64)
    QEMU_BIN="qemu-system-aarch64"
    EDK2_CODE="${EDK2_CODE_AA64:-/opt/homebrew/share/qemu/edk2-aarch64-code.fd}"
    EDK2_VARS_SRC="${EDK2_VARS_AA64:-/opt/homebrew/share/qemu/edk2-arm-vars.fd}"
    QEMU_MACHINE="-machine virt,gic-version=3 -accel hvf -cpu host -smp 4 -m ${VM_MEM_MB:-4096}"
    QEMU_SERIAL="-serial file:${OUTDIR}/_live-serial.log"
    ;;
  *)
    die "--arch must be amd64 or arm64 (got: $ARCH)"
    ;;
esac

LIVE_IMG="$OUTDIR/vulos-live-${ARCH}.img"
SERIAL="$OUTDIR/_live-serial.log"
QMP="$OUTDIR/_live-qmp.sock"

# ── Tool-guard ────────────────────────────────────────────────────────────────
# QEMU binary
command -v "$QEMU_BIN" >/dev/null 2>&1 \
  || unavailable "$QEMU_BIN not found — install QEMU (e.g. brew install qemu)"

# UEFI/OVMF firmware
[ -f "$EDK2_CODE" ] \
  || unavailable "UEFI firmware not found: $EDK2_CODE — install QEMU with OVMF firmware (e.g. brew install qemu)"

[ -f "$EDK2_VARS_SRC" ] \
  || unavailable "UEFI vars template not found: $EDK2_VARS_SRC"

# docker (only needed for the build step)
if [ "$NO_BUILD" = "0" ]; then
  command -v docker >/dev/null 2>&1 \
    || unavailable "docker not found — needed to build the live image (OrbStack or Docker Desktop)"
fi

# ── 1. Build ──────────────────────────────────────────────────────────────────
if [ "$NO_BUILD" = "0" ]; then
  say "Building builder image (cached after first run)…"
  docker build -q -t "$BUILDER_IMG" -f "$BUILDER_DF" "$REPO/scripts" >/dev/null

  REUSE="--reuse-rootfs"
  [ "$REBUILD" = "1" ] && REUSE=""
  say "Building Vulos OS live-USB image (build.sh --${ARCH} --live ${REUSE:-(full rebuild)})…"
  docker run --rm --privileged \
    -v "$REPO":/src -w /src \
    -v /src/frontend/node_modules \
    -v vulos-bm-work:/work \
    -v vulos-bm-gocache:/root/.cache/go-build \
    -v vulos-bm-gomod:/go/pkg/mod \
    -v vulos-bm-npm:/root/.npm \
    "$BUILDER_IMG" \
    bash -c "mkdir -p /work/output && ./build.sh --${ARCH} --live $REUSE /work/output && mkdir -p /src/output && cp -f /work/output/vulos-live-${ARCH}.img /src/output/vulos-live-${ARCH}.img"
fi

[ -f "$LIVE_IMG" ] || die "live image not found: $LIVE_IMG — run without --no-build first"
ok "image: $LIVE_IMG ($(du -h "$LIVE_IMG" | cut -f1))"

# ── 2. Prepare UEFI vars + serial log ────────────────────────────────────────
mkdir -p "$OUTDIR"
VARS="$OUTDIR/_live-uefi-vars.fd"
if [ ! -f "$VARS" ] || [ "$REBUILD" = "1" ]; then cp "$EDK2_VARS_SRC" "$VARS"; fi
: > "$SERIAL"
rm -f "$QMP"

# ── 3. Boot in QEMU ──────────────────────────────────────────────────────────
QEMU_PID=""
cleanup() {
  [ -n "$QEMU_PID" ] && kill "$QEMU_PID" 2>/dev/null || true
  rm -f "$QMP"
}
trap cleanup EXIT INT TERM

DISPLAY_ARGS="-display none -vga none"
[ "$SHOW" = "1" ] && DISPLAY_ARGS="-display cocoa"

say "Booting $QEMU_BIN (UEFI/OVMF, ${ARCH})…  serial → $SERIAL"

# shellcheck disable=SC2086
$QEMU_BIN \
  $QEMU_MACHINE \
  -drive if=pflash,format=raw,readonly=on,file="$EDK2_CODE" \
  -drive if=pflash,format=raw,file="$VARS" \
  -drive if=virtio,format=raw,file="$LIVE_IMG",readonly=on \
  -device virtio-net-pci,netdev=n0 \
  -netdev user,id=n0,hostfwd=tcp:127.0.0.1:${HOSTPORT}-:8080 \
  -device qemu-xhci -device usb-kbd -device usb-tablet \
  -qmp unix:"$QMP",server,nowait \
  -serial "file:$SERIAL" \
  $DISPLAY_ARGS \
  -no-reboot &
QEMU_PID=$!

# ── QMP helper (capabilities handshake + single command) ─────────────────────
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

# ── 4. Wait for a healthy-boot signal ────────────────────────────────────────
#
# The HTTP check is MANDATORY and is the only thing that sets PASS: it is the
# sole proof that vulos-server is actually up and routing, not crash-looping
# (Restart=on-failure/RestartSec=3 means a dead-on-arg-parse server still
# leaves systemd "active" and the console still prints a login prompt — a
# serial-only match was previously enough to pass and hid exactly that bug).
#
#   (a) Serial log evidence that initramfs pivoted to the live rootfs
#       ("vulos-live" hook banner, "VULOS-LIVE-DATA", "login:", etc.) is kept
#       as progress/diagnostic logging only, and to help explain an eventual
#       failure — it never flips PASS on its own.
#
#   (b) HTTP /api/setup/status reachable on the forwarded port — the server is
#       up and routing (same check as baremetal-smoke.sh). This is the
#       gold-standard "fully running" check and is required for PASS.

SERIAL_PATTERNS='vulos-live\|VULOS-LIVE-DATA\|Started.*Login Service\|login:.*\|Welcome to Vulos\|vulos-init'
HTTP_URL="http://127.0.0.1:${HOSTPORT}/api/setup/status"

say "Waiting up to ${TIMEOUT}s for HTTP (serial pivot/login logged for diagnostics only)…"
deadline=$(( $(date +%s) + TIMEOUT ))
PASS=0
PASS_MSG=""
SERIAL_SEEN=0
QEMU_DIED=0
while [ "$(date +%s)" -lt "$deadline" ]; do
  # Check QEMU is still running
  if ! kill -0 "$QEMU_PID" 2>/dev/null; then
    printf "${c_r}  QEMU process exited early${c_n}\n" >&2
    QEMU_DIED=1
    break
  fi

  # (a) Serial pivot/login check — diagnostic only, does NOT set PASS.
  if [ "$SERIAL_SEEN" = "0" ] && grep -qsE "$SERIAL_PATTERNS" "$SERIAL" 2>/dev/null; then
    SERIAL_SEEN=1
    printf "${c_d}  (serial) pivot/login prompt seen — still waiting on HTTP to confirm the backend is actually up…${c_n}\n"
  fi

  # (b) HTTP check — the only signal that sets PASS.
  if curl -fsS --max-time 3 "$HTTP_URL" >/dev/null 2>&1; then
    PASS=1
    PASS_MSG="HTTP /api/setup/status reachable — live OS fully booted"
    break
  fi

  elapsed=$(( $(date +%s) - (deadline - TIMEOUT) ))
  printf "${c_d}  … %ds elapsed\r${c_n}" "$elapsed"
  sleep 3
done
echo

# ── 5. Verdict + screenshot ───────────────────────────────────────────────────
SHOT="$OUTDIR/_live-screen.ppm"
qmp "{\"execute\":\"screendump\",\"arguments\":{\"filename\":\"$SHOT\"}}" >/dev/null 2>&1 \
  && [ -f "$SHOT" ] && ok "screenshot: $SHOT" || true

if [ "$PASS" = "1" ]; then
  ok "PASS — $PASS_MSG"
  # Print last matched serial line for traceability
  grep -E "$SERIAL_PATTERNS" "$SERIAL" 2>/dev/null | tail -3 | while IFS= read -r ln; do
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
  printf "${c_r}✗ FAIL — live OS did not reach a sane state within ${TIMEOUT}s${c_n}\n" >&2
  echo "" >&2
  if [ "$QEMU_DIED" = "1" ]; then
    echo "Failed signal: QEMU exited before the timeout — the VM process died." >&2
  elif [ "$SERIAL_SEEN" = "1" ]; then
    echo "Failed signal: HTTP /api/setup/status never became reachable, even though" >&2
    echo "the serial console DID show an initramfs pivot / login prompt. The boot" >&2
    echo "chain reached the OS but vulos-server is not answering — most likely it is" >&2
    echo "crash-looping (systemd Restart=on-failure/RestartSec=3 hides this on serial" >&2
    echo "since the console still looks fine). Check journalctl -u vulos.service and" >&2
    echo "the -env value it was started with." >&2
  else
    echo "Failed signal: neither the serial pivot/login prompt nor HTTP" >&2
    echo "/api/setup/status was seen — the live boot chain itself likely never" >&2
    echo "completed." >&2
  fi
  echo "" >&2
  echo "---- last 60 lines of serial ($SERIAL) ----" >&2
  tail -n 60 "$SERIAL" >&2 || true
  echo "" >&2
  echo "Possible causes:" >&2
  echo "  • ESP empty or missing loader entry (build.sh --live failed)" >&2
  echo "  • vulos-live initramfs hook not installed (scripts/initramfs/vulos-live)" >&2
  echo "  • VULOS-LIVE-DATA ext4 partition missing image.squashfs" >&2
  echo "  • QEMU firmware/arch mismatch (check --arch flag)" >&2
  echo "  • backend up but crash-looping (bad -env value / config) — HTTP check" >&2
  echo "    is the only thing that can catch this; see the failed-signal note above" >&2
  qmp '{"execute":"quit"}' >/dev/null 2>&1 || true
  exit 1
fi
