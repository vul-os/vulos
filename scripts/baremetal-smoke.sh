#!/usr/bin/env bash
# Vulos OS — bare-metal boot smoke harness
#
# ⚠ THIS HARNESS CANNOT CURRENTLY PASS. Read this before spending time on it.
#
# It builds `build.sh --disk`, whose loader entry sets init=/sbin/vulos-init.
# vulos-init therefore runs as PID 1 and reaches the VERITY-02 gate, which
# reads /etc/vulos/stable.json — and NO build step writes that file. The gate
# log.Fatalf()s, init exits 1, and the kernel panics with "Attempted to kill
# init!". Confirmed by mounting the built image: /etc/vulos/ contains only
# os-bucket-url, release-cert.json and trust-anchor.pub.
#
# What we actually SHIP is `build.sh --live`, whose loader entry has no init=,
# so systemd is PID 1, vulos-init never runs, and VERITY-02 is never reached.
# That path boots and is covered by scripts/smoke-liveusb.sh (locally) and by
# the BOOT-01 gate in .github/workflows/release.yml (which boots the exact
# artifact before publishing it).
#
# So this script tests an image variant that is neither shipped nor bootable.
# It is referenced by no workflow, no Makefile target and no doc, which is why
# nobody noticed. Fixing it means either making --disk builds produce a signed
# stable.json, or giving vulos-init a documented dev mode — both real work, and
# neither should be done by weakening VERITY-02, which is a fail-closed
# security gate. Left in place rather than deleted because the disk/installed
# path is a genuine target; it just is not finished.
#
# Builds a genuinely UEFI-bootable image (build.sh --disk) inside a
# reproducible Docker builder, boots it in QEMU, and asserts the OS reached
# a serving desktop — exercising the real boot chain:
#
#   UEFI (edk2) → systemd-boot → kernel → initramfs (branded Plymouth splash)
#     → vulos-init (mounts, hwdetect, net, sshd) → vulos-server
#       → labwc/cage compositor + browser shell → HTTP 200 on /api/setup/status
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

# ── 0. Throwaway smoke-test signing keys ────────────────────────────────────
# --disk now signs a boot-time /etc/vulos/stable.json (VERITY-02) with a
# release key whose public half the embedded release cert must certify. The
# repo's checked-in keys/release-cert.json is PRODUCTION ceremony output
# (docs/KEY-CEREMONY.md) — its private half is deliberately not on this
# machine, and any keys/release.priv.json lying around locally is not
# guaranteed to match it (build.sh now checks this and refuses to sign with a
# non-matching key rather than produce an image that fails at boot). Rather
# than depend on either, this harness generates its own throwaway root +
# release keypair and cert with backend/cmd/sign — the same tool a real fork
# build uses (see build.sh's "Fork procedure" header) — and points build.sh
# at it via the existing VULOS_TRUST_ANCHOR_PUBKEY / VULOS_RELEASE_CERT /
# VULOS_RELEASE_PRIV_KEY overrides. None of this is secret or committed;
# cached under output/ across runs like the rootfs, regenerated on --rebuild.
SMOKE_KEYS="$OUTDIR/_smoke-keys"
if [ ! -f "$SMOKE_KEYS/release-cert.json" ] || [ "$REBUILD" = "1" ]; then
  say "Generating throwaway smoke-test signing keys…"
  rm -rf "$SMOKE_KEYS"
  mkdir -p "$SMOKE_KEYS"
  (
    cd "$REPO/backend" &&
    go run ./cmd/sign gen-key \
      -out-priv "$SMOKE_KEYS/root.priv.json" -out-pub "$SMOKE_KEYS/root.pub.json" >/dev/null &&
    go run ./cmd/sign gen-key \
      -out-priv "$SMOKE_KEYS/release.priv.json" -out-pub "$SMOKE_KEYS/release.pub.json" >/dev/null &&
    go run ./cmd/sign export-anchor \
      -pub "$SMOKE_KEYS/root.pub.json" -out "$SMOKE_KEYS/trust-anchor.pub" >/dev/null &&
    go run ./cmd/sign issue-release-cert \
      -root-priv "$SMOKE_KEYS/root.priv.json" \
      -release-pub "$SMOKE_KEYS/release.pub.json" \
      -key-id "baremetal-smoke" \
      -not-after "2099-01-01T00:00:00Z" \
      -min-epoch 0 \
      -out "$SMOKE_KEYS/release-cert.json" >/dev/null
  ) || die "failed to generate smoke-test signing keys"
  ok "smoke-test signing keys: $SMOKE_KEYS"
fi

# ── 1. Build ────────────────────────────────────────────────────────────────
if [ "$NO_BUILD" = "0" ]; then
  say "Building builder image (cached after first run)…"
  docker build -q -t "$BUILDER_IMG" -f "$BUILDER_DF" "$REPO/scripts" >/dev/null

  REUSE="--reuse-rootfs"
  [ "$REBUILD" = "1" ] && REUSE=""
  say "Building Vulos OS bootable image (build.sh --arm64 --disk ${REUSE:-(full rebuild)})…"
  # The rootfs/image MUST be built on a container-native filesystem: debootstrap
  # tar-extracts device nodes / ownership / xattrs that OrbStack's virtiofs
  # bind mount can't represent ("tar failed"). So build into the vulos-bm-work
  # Docker volume (OUTDIR=/work/output, also persists rootfs for --reuse-rootfs)
  # and copy only the finished disk image back to the bind-mounted /src/output.
  # /src is the bind-mounted repo, so the smoke keys generated above at
  # $OUTDIR/_smoke-keys (= $REPO/output/_smoke-keys) are visible in-container
  # at /src/output/_smoke-keys — independent of build.sh's own /work/output.
  docker run --rm --privileged \
    -v "$REPO":/src -w /src \
    -v /src/frontend/node_modules \
    -v vulos-bm-work:/work \
    -v vulos-bm-gocache:/root/.cache/go-build \
    -v vulos-bm-gomod:/go/pkg/mod \
    -v vulos-bm-npm:/root/.npm \
    -e VULOS_TRUST_ANCHOR_PUBKEY=/src/output/_smoke-keys/trust-anchor.pub \
    -e VULOS_RELEASE_CERT=/src/output/_smoke-keys/release-cert.json \
    -e VULOS_RELEASE_PRIV_KEY=/src/output/_smoke-keys/release.priv.json \
    -e VULOS_OS_BUCKET_URL=https://os.vulos.org \
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
  -machine virt,gic-version=3 -accel hvf -cpu host -smp 4 -m "${VM_MEM_MB:-12288}" \
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
# Readiness = /api/setup/status. /api/health is no longer auth-gated (it now
# serves an anonymous caller the verdict — {"status","timestamp"} + 200/503 —
# and withholds the per-check detail), but it is still the wrong readiness
# signal here: it answers 503 on a HEALTHY-but-degraded box, e.g. low disk in a
# scratch VM, which is not "the server is up and routing". Keep setup-status.
say "Waiting up to ${TIMEOUT}s for http://127.0.0.1:${HOSTPORT}/api/setup/status …"
URL="http://127.0.0.1:${HOSTPORT}/api/setup/status"
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
  ok "PASS — Vulos OS booted and is serving the desktop"
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
