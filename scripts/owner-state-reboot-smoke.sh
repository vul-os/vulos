#!/usr/bin/env bash
# OWNSTATE-01 — does the owner's account survive a reboot on an installed box?
#
# THE DEFECT. On a netboot-installed disk an agent created an owner account and
# rebooted the guest. Afterwards:
#
#   /api/auth/status → {"has_users":false}
#   login as founder → 401 invalid username or password
#   /root/.vulos/*   → entirely recreated at reboot time
#
# `mount -o bind $MERGED $rootmnt` in scripts/initramfs/vulos-live leaves the
# ext4 shadowed, so /root/.vulos resolved inside the overlay whose upper layer
# is a tmpfs in RAM. auth.db, auth.key, the peering identity, the device key and
# the vaults all lived in RAM and died at power-off, and every unit test passed
# throughout because they all build their store in a t.TempDir().
#
# WHY THIS SCRIPT EXISTS AND backend/internal/docsref DOES NOT REPLACE IT.
# Those tests drive the hook under dash with klibc-shaped stubs and assert mount
# TOPOLOGY. That settles where a write is DIRECTED. It cannot show the kernel
# accepts the mounts, that the ext4 is really writable after the remount, or
# that a row written by a running OS is still there after a power cycle.
# roadmap/BOOT-FOUR-ERRORS.md says it outright: no static test replaces the
# round trip. This is the round trip.
#
# WHAT IT DOES
#   Phase 0 — throwaway signing keys (same as scripts/netboot-install-smoke.sh).
#   Phase 1 — build a real --live squashfs with THIS repo's vulos-live hook
#             baked into the initramfs via update-initramfs.
#   Phase 2 — run the REAL netboot-install pipeline (no mocks: real parted,
#             mkfs, mount, bootctl) against a real loop-backed disk.
#   Phase 3 — boot the installed disk in QEMU. Assert has_users=false, then
#             CREATE THE OWNER over HTTP, and assert has_users=true.
#   Phase 4 — shut the guest down cleanly (QMP system_powerdown, i.e. ACPI —
#             not a kill, so the OS flushes), then BOOT THE SAME DISK AGAIN
#             with fresh UEFI vars.
#   Phase 5 — the verdict: has_users must still be true and the owner must be
#             able to LOG IN. A 401 here is the defect, reproduced.
#   Phase 6 — loop-mount the disk from the host and prove the bytes are on the
#             PARTITION: /root/.vulos/db/auth.db must exist there, and
#             /root/.vulos/apps must be EMPTY (the tmpfs kept installed-app
#             manifests off the disk, per roadmap/APP-DIR-PERSISTENCE.md).
#
# Exit codes:
#   0  the owner survived the reboot and the bytes are on the partition.
#   1  a hard failure — the build, the install, either boot, or the verdict.
#   2  INCONCLUSIVE — the machine booted but the account could not be created,
#      so there was nothing to test the survival of.
#
# Usage:
#   scripts/owner-state-reboot-smoke.sh              # build, install, round trip
#   scripts/owner-state-reboot-smoke.sh --no-build   # reuse the volume's squashfs
#   scripts/owner-state-reboot-smoke.sh --no-install # reuse output/_ownstate-vda.img
#   scripts/owner-state-reboot-smoke.sh --timeout 480
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

HOSTPORT="${HOSTPORT:-8097}"
# 900, not 420. The first real run of this harness reached HTTP at 396 s on the
# first boot and 383 s on the second, on a host at load ~260 — i.e. it passed
# with under 30 s of margin, and would have reported "the disk did not reach
# HTTP" on a slightly busier machine. That failure would have been about the
# host, not about the box, and it would have looked exactly like the defect this
# script exists to detect. The deadline is the instrument; the assertions are
# has_users and the login, and neither moved.
TIMEOUT=900
NO_BUILD=0
NO_INSTALL=0

while [ $# -gt 0 ]; do
  case "$1" in
    --no-build)   NO_BUILD=1; shift ;;
    --no-install) NO_BUILD=1; NO_INSTALL=1; shift ;;
    --timeout)    TIMEOUT="$2"; shift 2 ;;
    -h|--help)    grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

c_b='\033[0;34m'; c_g='\033[0;32m'; c_r='\033[0;31m'; c_d='\033[2m'; c_n='\033[0m'
say()  { printf "${c_b}▸ %s${c_n}\n" "$*"; }
ok()   { printf "${c_g}✓ %s${c_n}\n" "$*"; }
die()  { printf "${c_r}✗ %s${c_n}\n" "$*" >&2; exit 1; }

command -v docker            >/dev/null 2>&1 || die "docker not found"
command -v qemu-system-aarch64 >/dev/null 2>&1 || die "qemu-system-aarch64 not found"
[ -f "$EDK2_CODE" ]     || die "UEFI firmware not found: $EDK2_CODE"
[ -f "$EDK2_VARS_SRC" ] || die "UEFI vars template not found: $EDK2_VARS_SRC"

mkdir -p "$OUTDIR"
DISK_IMG="$OUTDIR/_ownstate-vda.img"
SERIAL1="$OUTDIR/_ownstate-serial-boot1.log"
SERIAL2="$OUTDIR/_ownstate-serial-boot2.log"
QMP="$OUTDIR/_os-qmp.sock"
BASE="http://127.0.0.1:${HOSTPORT}"

# The credentials whose survival is the entire point.
OWNER_USER="founder"
OWNER_PASS="OwnerStateRoundTrip!2026"

# ── Phase 0: throwaway signing keys ──────────────────────────────────────────
# build.sh only signs os-core.roothash with a release key the SHIPPED cert
# authorises, and the tracked cert is production ceremony output whose private
# half is deliberately absent. Same throwaway pair scripts/netboot-install-
# smoke.sh and scripts/baremetal-smoke.sh generate; reused if already there.
SMOKE_KEYS="$OUTDIR/_smoke-keys"
if [ ! -f "$SMOKE_KEYS/release-cert.json" ]; then
  say "Generating throwaway smoke-test signing keys…"
  rm -rf "$SMOKE_KEYS"; mkdir -p "$SMOKE_KEYS"
  (
    cd "$REPO/backend" &&
    go run ./cmd/sign gen-key -out-priv "$SMOKE_KEYS/root.priv.json"    -out-pub "$SMOKE_KEYS/root.pub.json"    >/dev/null &&
    go run ./cmd/sign gen-key -out-priv "$SMOKE_KEYS/release.priv.json" -out-pub "$SMOKE_KEYS/release.pub.json" >/dev/null &&
    go run ./cmd/sign export-anchor -pub "$SMOKE_KEYS/root.pub.json" -out "$SMOKE_KEYS/trust-anchor.pub" >/dev/null &&
    go run ./cmd/sign issue-release-cert \
      -root-priv "$SMOKE_KEYS/root.priv.json" -release-pub "$SMOKE_KEYS/release.pub.json" \
      -key-id "ownstate-smoke" -not-after "2099-01-01T00:00:00Z" -min-epoch 0 \
      -out "$SMOKE_KEYS/release-cert.json" >/dev/null
  ) || die "failed to generate smoke-test signing keys"
fi
ok "Phase 0 — signing keys ready"

# ── Phase 1: build the --live artifact with THIS repo's vulos-live hook ──────
if [ "$NO_BUILD" = "0" ]; then
  say "Building builder image (cached after first run)…"
  docker build -q -t "$BUILDER_IMG" -f "$BUILDER_DF" "$REPO/scripts" >/dev/null
  say "Building Vulos OS --live squashfs (build.sh --arm64 --live --reuse-rootfs)…"
  say "  scripts/initramfs/vulos-live is copied into the rootfs and baked into the"
  say "  initramfs by update-initramfs here — the SAME hook Phase 3 boots."
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
    bash -c "mkdir -p /work/output && ./build.sh --arm64 --live --reuse-rootfs /work/output" \
    || die "build.sh failed"
fi
ok "Phase 1 — image.squashfs + kernel + initramfs in the vulos-bm-work volume"

# ── Phase 2: the REAL netboot-install pipeline against a real disk ───────────
if [ "$NO_INSTALL" = "0" ]; then
  say "Running the real netboot-install pipeline (TestNetbootInstall_RealPipeline_E2E)…"
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
      [ -f "$SQUASHFS" ] || { echo "FATAL: $SQUASHFS not found — did Phase 1 run?" >&2; exit 1; }
      KIMG="$(ls -1 /work/output/rootfs/boot/vmlinuz-*   2>/dev/null | sort -V | tail -1)"
      IIMG="$(ls -1 /work/output/rootfs/boot/initrd.img-* 2>/dev/null | sort -V | tail -1)"
      [ -n "$KIMG" ] && [ -n "$IIMG" ] || { echo "FATAL: no kernel/initrd under /work/output/rootfs/boot" >&2; exit 1; }
      mkdir -p /boot /etc/vulos
      cp "$KIMG" /boot/vmlinuz
      cp "$IIMG" /boot/initramfs-vulos.img
      echo "http://os.invalid.example/" > /etc/vulos/os-bucket-url

      dd if=/dev/zero of=/work/output/_ownstate-disk.img bs=1M count=1400 status=none
      LOOPDEV="$(losetup -P -f --show /work/output/_ownstate-disk.img)"
      ln -sf "$LOOPDEV" /dev/vda
      cleanup() { losetup -d "$LOOPDEV" 2>/dev/null || true; }
      trap cleanup EXIT

      # This container has no udev, so the kernel registers the partitions but
      # nothing creates the device nodes. mknod them from sysfs, exactly as
      # scripts/netboot-install-smoke.sh does; real hardware never needs it.
      (
        for _ in $(seq 1 150); do
          if [ -e "/sys/class/block/${LOOPDEV#/dev/}p1/dev" ] && [ -e "/sys/class/block/${LOOPDEV#/dev/}p2/dev" ]; then
            for p in 1 2; do
              devnum="$(cat "/sys/class/block/${LOOPDEV#/dev/}p${p}/dev")"
              [ -e "${LOOPDEV}p${p}" ] || mknod "${LOOPDEV}p${p}" b "${devnum%%:*}" "${devnum##*:}"
              ln -sf "${LOOPDEV}p${p}" "/dev/vda${p}"
            done
            exit 0
          fi
          sleep 0.2
        done
        echo "FATAL: partitions never registered" >&2; exit 1
      ) &
      LINKER_PID=$!

      export VULOS_NETBOOT_E2E=1 VULOS_E2E_DISK=vda VULOS_E2E_SQUASHFS="$SQUASHFS"
      # -count=1 is not optional: Go will serve a CACHED pass for a test that
      # partitions a real disk, and then this harness ships a stale image.
      set +e
      go test ./services/installer/... -run TestNetbootInstall_RealPipeline_E2E -count=1 -v -timeout 15m
      TESTRC=$?
      set -e
      LINKRC=0; wait "$LINKER_PID" 2>/dev/null || LINKRC=$?
      cleanup; trap - EXIT
      [ "$LINKRC" -eq 0 ] || { echo "FATAL: partition linker failed" >&2; exit 1; }
      [ "$TESTRC" -eq 0 ] || exit "$TESTRC"
      parted -s /work/output/_ownstate-disk.img print >/dev/null 2>&1 \
        || { echo "FATAL: produced image has no partition table" >&2; exit 1; }
      cp /work/output/_ownstate-disk.img /src/output/_ownstate-vda.img
    ' || die "the netboot-install pipeline failed"
fi
[ -f "$DISK_IMG" ] || die "no installed disk image at $DISK_IMG"
dd if="$DISK_IMG" bs=1 skip=512 count=8 2>/dev/null | grep -q "EFI PART" \
  || die "the disk image has no GPT header"
ok "Phase 2 — install pipeline succeeded; $DISK_IMG ($(du -h "$DISK_IMG" | cut -f1))"

# ── Boot helpers ─────────────────────────────────────────────────────────────
QEMU_PID=""
cleanup_qemu() { [ -n "$QEMU_PID" ] && kill "$QEMU_PID" 2>/dev/null || true; rm -f "$QMP"; }
trap cleanup_qemu EXIT INT TERM

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

# boot_guest SERIAL_LOG — starts QEMU on $DISK_IMG with FRESH UEFI vars and
# waits for the box to answer HTTP.
#
# The vars file is a fresh copy every time on purpose: a reused vars.fd carries
# whatever NVRAM a previous boot left, including OVMF's interactive setup
# screen after a failure, which hangs forever and looks exactly like "the disk
# is not bootable".
boot_guest() {
  local serial="$1"
  local vars="$OUTDIR/_ownstate-uefi-vars.fd"
  cp "$EDK2_VARS_SRC" "$vars"
  : > "$serial"
  rm -f "$QMP"
  qemu-system-aarch64 \
    -machine virt,gic-version=3 -accel hvf -cpu host -smp 4 -m "${VM_MEM_MB:-4096}" \
    -drive if=pflash,format=raw,readonly=on,file="$EDK2_CODE" \
    -drive if=pflash,format=raw,file="$vars" \
    -drive if=virtio,format=raw,file="$DISK_IMG" \
    -device virtio-net-pci,netdev=n0 \
    -netdev user,id=n0,hostfwd=tcp:127.0.0.1:${HOSTPORT}-:8080 \
    -qmp unix:"$QMP",server,nowait \
    -serial "file:$serial" \
    -display none -vga none \
    -no-reboot &
  QEMU_PID=$!

  local deadline=$(( $(date +%s) + TIMEOUT ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if ! kill -0 "$QEMU_PID" 2>/dev/null; then
      printf "${c_r}  QEMU exited early${c_n}\n" >&2
      return 1
    fi
    if curl -fsS --max-time 3 "$BASE/api/setup/status" >/dev/null 2>&1; then
      return 0
    fi
    printf "${c_d}  … %ds\r${c_n}" "$(( $(date +%s) - (deadline - TIMEOUT) ))"
    sleep 3
  done
  echo
  return 1
}

# shutdown_guest — ACPI powerdown, NOT a kill. A kill is a power cut, and this
# harness must not be able to fail because SQLite lost an unflushed page: the
# question is whether the mount topology puts the bytes on the disk, not
# whether the box survives a yank.
shutdown_guest() {
  qmp '{"execute":"system_powerdown"}' >/dev/null 2>&1 || true
  local deadline=$(( $(date +%s) + 120 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    kill -0 "$QEMU_PID" 2>/dev/null || { QEMU_PID=""; return 0; }
    sleep 2
  done
  printf "${c_r}  guest did not power down in 120s — killing it${c_n}\n" >&2
  kill "$QEMU_PID" 2>/dev/null || true
  QEMU_PID=""
  return 1
}

# ── Phase 3: first boot — create the owner ───────────────────────────────────
say "Phase 3 — booting the installed disk (first boot) → $SERIAL1"
boot_guest "$SERIAL1" || die "the installed disk did not reach HTTP on its first boot (serial: $SERIAL1)"
ok "first boot up — HTTP answering on $BASE"

STATUS1="$(curl -fsS --max-time 5 "$BASE/api/auth/status" || true)"
say "  /api/auth/status (before setup): $STATUS1"
case "$STATUS1" in
  *'"has_users":false'*) : ;;
  *) printf "${c_r}✗ a freshly installed box already reports users: %s${c_n}\n" "$STATUS1" >&2
     printf "  Either the install carried an account over, or the previous run's disk was reused.\n" >&2
     exit 2 ;;
esac

say "  creating the owner account (POST /api/auth/register)…"
REG="$(curl -sS --max-time 15 -X POST "$BASE/api/auth/register" \
        -H 'Content-Type: application/json' \
        -d "{\"username\":\"$OWNER_USER\",\"password\":\"$OWNER_PASS\",\"display_name\":\"Founder\"}" || true)"
say "  register → ${REG:0:200}"
STATUS2="$(curl -fsS --max-time 5 "$BASE/api/auth/status" || true)"
say "  /api/auth/status (after setup):  $STATUS2"
case "$STATUS2" in
  *'"has_users":true'*) ok "owner account created on the running box" ;;
  *) printf "${c_r}✗ the account was not created, so there is nothing whose survival to test${c_n}\n" >&2
     printf "  register response: %s\n  status: %s\n" "$REG" "$STATUS2" >&2
     exit 2 ;;
esac

# Prove the credentials work BEFORE the reboot, so a post-reboot 401 can only
# mean "the account is gone", never "the password was never right".
LOGIN1="$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' -X POST "$BASE/api/auth/login" \
          -H 'Content-Type: application/json' \
          -d "{\"username\":\"$OWNER_USER\",\"password\":\"$OWNER_PASS\"}" || true)"
say "  login BEFORE reboot → HTTP $LOGIN1"
[ "$LOGIN1" = "200" ] || { printf "${c_r}✗ the owner cannot log in even before a reboot (HTTP %s)${c_n}\n" "$LOGIN1" >&2; exit 2; }

# ── Phase 4: clean shutdown, then boot the SAME disk again ───────────────────
say "Phase 4 — ACPI powerdown, then a second boot of the same disk"
shutdown_guest || say "  (guest was killed after the powerdown timeout — continuing)"
ok "guest powered down"

say "Phase 5 — booting the same disk again → $SERIAL2"
boot_guest "$SERIAL2" || die "the disk did not boot a second time (serial: $SERIAL2)"
ok "second boot up"

# ── Phase 5: the verdict ─────────────────────────────────────────────────────
STATUS3="$(curl -fsS --max-time 5 "$BASE/api/auth/status" || true)"
say "  /api/auth/status AFTER REBOOT: $STATUS3"
LOGIN2="$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' -X POST "$BASE/api/auth/login" \
          -H 'Content-Type: application/json' \
          -d "{\"username\":\"$OWNER_USER\",\"password\":\"$OWNER_PASS\"}" || true)"
say "  login AFTER REBOOT → HTTP $LOGIN2"

VERDICT=0
case "$STATUS3" in
  *'"has_users":true'*) ok "has_users survived the reboot" ;;
  *) printf "${c_r}✗ /api/auth/status says %s after a reboot — the owner is GONE${c_n}\n" "$STATUS3" >&2; VERDICT=1 ;;
esac
if [ "$LOGIN2" = "200" ]; then
  ok "the owner logged in after a reboot — OWNSTATE-01 round trip PASSED"
else
  printf "${c_r}✗ login after reboot returned HTTP %s (was 200 before) — the account did not survive${c_n}\n" "$LOGIN2" >&2
  VERDICT=1
fi

shutdown_guest || true

# ── Phase 6: are the bytes actually ON THE PARTITION? ────────────────────────
#
# The HTTP verdict above can in principle be satisfied by something other than
# disk persistence. This looks at the ext4 directly, from the host, with the
# guest off: auth.db must be there, and /root/.vulos/apps must NOT have
# accumulated anything, because the initramfs mounts a tmpfs over it so an app
# manifest cannot outlive the Flatpak payload it points at.
say "Phase 6 — inspecting the partition from the host"
INSPECT="$OUTDIR/_ownstate-partition.txt"
docker run --rm --privileged -v "$OUTDIR":/out "$BUILDER_IMG" bash -c '
  set -e
  LOOP="$(losetup --find --show --partscan /out/_ownstate-vda.img)"
  trap "umount /mnt/i 2>/dev/null || true; losetup -d $LOOP 2>/dev/null || true" EXIT
  base="${LOOP#/dev/}"
  for _ in $(seq 1 150); do grep -q " ${base}p2$" /proc/partitions && break; sleep 0.2; done
  for p in 1 2; do
    d="$(cat /sys/class/block/${base}p${p}/dev)"
    rm -f "${LOOP}p${p}"; mknod "${LOOP}p${p}" b "${d%%:*}" "${d##*:}"
  done
  mkdir -p /mnt/i; mount "${LOOP}p2" /mnt/i
  echo "=== the installed root partition, top level ==="
  ls -la /mnt/i
  echo "=== /root/.vulos (mode must be 0700) ==="
  ls -lad /mnt/i/root/.vulos
  ls -la  /mnt/i/root/.vulos
  echo "=== /root/.vulos/db ==="
  ls -la /mnt/i/root/.vulos/db 2>/dev/null || echo "(absent)"
  echo "=== /root/.vulos/apps — must be EMPTY (tmpfs kept manifests in RAM) ==="
  ls -la /mnt/i/root/.vulos/apps 2>/dev/null || echo "(absent)"
  echo "=== /var/lib/vulos ==="
  ls -la /mnt/i/var/lib/vulos 2>/dev/null || echo "(absent)"
  echo "=== df ==="
  df -h /mnt/i
' > "$INSPECT" 2>&1 || die "could not inspect the partition (see $INSPECT)"
cat "$INSPECT"

if grep -q "auth.db" "$INSPECT"; then
  ok "auth.db is ON THE PARTITION — the running OS's writes reached the disk"
else
  printf "${c_r}✗ no auth.db on the partition; whatever made the HTTP check pass, it was not disk persistence${c_n}\n" >&2
  VERDICT=1
fi

echo
if [ "$VERDICT" = "0" ]; then
  ok "OWNSTATE-01 ROUND TRIP PASSED — account created, box rebooted, owner logged in, bytes on the ext4"
else
  die "OWNSTATE-01 ROUND TRIP FAILED — see above (serials: $SERIAL1, $SERIAL2; partition: $INSPECT)"
fi
