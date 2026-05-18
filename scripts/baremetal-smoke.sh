#!/usr/bin/env bash
# Vula OS — bare-metal boot smoke harness
#
# Builds a genuinely UEFI-bootable image (build.sh --disk) inside a
# reproducible Docker builder, boots it in QEMU, and asserts the OS reached
# a serving desktop — exercising the real boot chain:
#
#   UEFI (edk2) → systemd-boot → kernel → initramfs (branded Plymouth splash)
#     → vulos-init (mounts, hwdetect, net, sshd) → vulos-server
#       → labwc/cage compositor + browser shell → HTTP 200 on /api/health
#
# Usage:
#   scripts/baremetal-smoke.sh              # CI: build, boot headless, assert, exit 0/1
#   scripts/baremetal-smoke.sh --show       # open a QEMU window and watch it boot
#   scripts/baremetal-smoke.sh --rebuild    # force a full rootfs rebuild (slow)
#   scripts/baremetal-smoke.sh --no-build   # boot the existing image only
#   scripts/baremetal-smoke.sh --timeout 480
#
# Default reuses output/rootfs across runs so the fix-test loop only re-runs
# the fast image assembly + QEMU (a full rootfs build is ~15 min, once).
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
OUTDIR="$REPO/output"
ARCH="arm64"
IMG="$OUTDIR/vulos-${ARCH}.img"
BUILDER_IMG="vulos-baremetal-builder"
BUILDER_DF="$REPO/scripts/baremetal-builder.Dockerfile"

HOSTPORT="${HOSTPORT:-8088}"
TIMEOUT=360
SHOW=0
REBUILD=0
NO_BUILD=0

EDK2_CODE="/opt/homebrew/share/qemu/edk2-aarch64-code.fd"
EDK2_VARS_SRC="/opt/homebrew/share/qemu/edk2-arm-vars.fd"

while [ $# -gt 0 ]; do
  case "$1" in
    --show)     SHOW=1; shift ;;
    --rebuild)  REBUILD=1; shift ;;
    --no-build) NO_BUILD=1; shift ;;
    --timeout)  TIMEOUT="$2"; shift 2 ;;
    -h|--help)  grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

c_b='\033[0;34m'; c_g='\033[0;32m'; c_r='\033[0;31m'; c_d='\033[2m'; c_n='\033[0m'
say()  { printf "${c_b}▸ %s${c_n}\n" "$*"; }
ok()   { printf "${c_g}✓ %s${c_n}\n" "$*"; }
die()  { printf "${c_r}✗ %s${c_n}\n" "$*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "docker not found (OrbStack)"
command -v qemu-system-aarch64 >/dev/null 2>&1 || die "qemu-system-aarch64 not found (brew install qemu)"
[ -f "$EDK2_CODE" ] || die "UEFI firmware missing: $EDK2_CODE"

# ── 1. Build ────────────────────────────────────────────────────────────────
if [ "$NO_BUILD" = "0" ]; then
  say "Building builder image (cached after first run)…"
  docker build -q -t "$BUILDER_IMG" -f "$BUILDER_DF" "$REPO/scripts" >/dev/null

  REUSE="--reuse-rootfs"
  [ "$REBUILD" = "1" ] && REUSE=""
  say "Building Vula OS bootable image (build.sh --arm64 --disk ${REUSE:-(full rebuild)})…"
  # The rootfs/image MUST be built on a container-native filesystem: debootstrap
  # tar-extracts device nodes / ownership / xattrs that OrbStack's virtiofs
  # bind mount can't represent ("tar failed"). So build into the vulos-bm-work
  # Docker volume (OUTDIR=/work/output, also persists rootfs for --reuse-rootfs)
  # and copy only the finished disk image back to the bind-mounted /src/output.
  docker run --rm --privileged \
    -v "$REPO":/src -w /src \
    -v vulos-bm-work:/work \
    -v vulos-bm-gocache:/root/.cache/go-build \
    -v vulos-bm-gomod:/go/pkg/mod \
    -v vulos-bm-npm:/root/.npm \
    "$BUILDER_IMG" \
    bash -c "mkdir -p /work/output && ./build.sh --arm64 --device generic-arm64 --disk $REUSE /work/output && mkdir -p /src/output && cp -f /work/output/vulos-${ARCH}.img /src/output/vulos-${ARCH}.img"
fi
[ -f "$IMG" ] || die "image not produced: $IMG"
ok "image: $IMG ($(du -h "$IMG" | cut -f1))"

# ── 2. Boot in QEMU ─────────────────────────────────────────────────────────
VARS="$OUTDIR/_uefi-vars.fd"
if [ ! -f "$VARS" ] || [ "$REBUILD" = "1" ]; then cp "$EDK2_VARS_SRC" "$VARS"; fi
SERIAL="$OUTDIR/_serial.log"
QMP="$OUTDIR/_qmp.sock"
: > "$SERIAL"
rm -f "$QMP"

QEMU_PID=""
cleanup() { [ -n "$QEMU_PID" ] && kill "$QEMU_PID" 2>/dev/null || true; rm -f "$QMP"; }
trap cleanup EXIT INT TERM

DISPLAY_ARGS="-vnc 127.0.0.1:0"           # headless but screenshot-able
[ "$SHOW" = "1" ] && DISPLAY_ARGS="-display cocoa"

say "Booting QEMU (arm64, HVF)…  serial → $SERIAL"
qemu-system-aarch64 \
  -machine virt,gic-version=3 -accel hvf -cpu host -smp 4 -m 4096 \
  -drive if=pflash,format=raw,readonly=on,file="$EDK2_CODE" \
  -drive if=pflash,format=raw,file="$VARS" \
  -drive if=virtio,format=raw,file="$IMG" \
  -device virtio-gpu-pci \
  -device qemu-xhci -device usb-kbd -device usb-tablet \
  -netdev user,id=n0,hostfwd=tcp:127.0.0.1:${HOSTPORT}-:8080 \
  -device virtio-net-pci,netdev=n0 \
  -qmp unix:"$QMP",server,nowait \
  -serial "file:$SERIAL" \
  $DISPLAY_ARGS &
QEMU_PID=$!

# QMP one-shot helper (capabilities handshake + a single command).
qmp() {
  python3 - "$QMP" "$1" <<'PY'
import json,socket,sys,time
sock,cmd=sys.argv[1],sys.argv[2]
s=socket.socket(socket.AF_UNIX);
for _ in range(50):
    try: s.connect(sock); break
    except OSError: time.sleep(0.2)
else: sys.exit(1)
f=s.makefile('rw')
f.readline()
def call(o): f.write(json.dumps(o)+"\r\n"); f.flush(); return f.readline()
call({"execute":"qmp_capabilities"})
print(call(json.loads(cmd)), end="")
PY
}

# ── 3. Poll readiness ───────────────────────────────────────────────────────
say "Waiting up to ${TIMEOUT}s for http://127.0.0.1:${HOSTPORT}/api/health …"
URL="http://127.0.0.1:${HOSTPORT}/api/health"
deadline=$(( $(date +%s) + TIMEOUT ))
PASS=0
while [ "$(date +%s)" -lt "$deadline" ]; do
  if ! kill -0 "$QEMU_PID" 2>/dev/null; then
    printf "${c_r}QEMU exited early${c_n}\n"; break
  fi
  if curl -fsS --max-time 3 "$URL" >/dev/null 2>&1; then PASS=1; break; fi
  sleep 3
  printf "${c_d}  … %ss elapsed%s\r" "$(( $(date +%s) - (deadline - TIMEOUT) ))" ""
done
echo

# ── 4. Verdict + screenshot ─────────────────────────────────────────────────
SHOT="$OUTDIR/smoke-screen.ppm"
qmp "{\"execute\":\"screendump\",\"arguments\":{\"filename\":\"$SHOT\"}}" >/dev/null 2>&1 \
  && [ -f "$SHOT" ] && ok "screenshot: $SHOT" || true

if [ "$PASS" = "1" ]; then
  ok "PASS — Vula OS booted and is serving the desktop"
  echo "  health:  $(curl -fsS "$URL" 2>/dev/null)"
  if [ "$SHOW" = "1" ]; then
    echo ""
    say "QEMU window is open — watch the boot (Plymouth → desktop). Ctrl-C to stop."
    wait "$QEMU_PID"
  else
    qmp '{"execute":"quit"}' >/dev/null 2>&1 || true
  fi
  exit 0
else
  printf "${c_r}✗ FAIL — desktop did not come up within ${TIMEOUT}s${c_n}\n" >&2
  echo "---- last 60 lines of serial ($SERIAL) ----" >&2
  tail -n 60 "$SERIAL" >&2 || true
  qmp '{"execute":"quit"}' >/dev/null 2>&1 || true
  exit 1
fi
