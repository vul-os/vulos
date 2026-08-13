#!/usr/bin/env bash
# smoke-multiscreen-qemu.sh — SCREENS-02: boot a REAL image in QEMU with TWO
# virtual displays and photograph BOTH of them.
#
# This is the last mile of roadmap/SCREENS.md. Everything before it was proved
# somewhere that is not a booted OS:
#
#   scripts/smoke-multiscreen.sh   labwc places windows by windowRule +
#                                  MoveToOutput — but with a config the test
#                                  wrote, `foot` terminals, and HEADLESS-N
#                                  outputs from wlroots' headless backend.
#   scripts/smoke-kiosk-multiscreen.sh
#                                  the real launcher enumerates outputs and
#                                  takes the multi-output branch — but in a
#                                  container, against a faked /sys/class/drm,
#                                  and the compositor never came up.
#
# What was left: the real launcher, on a real boot, driving real browsers onto
# real DRM connectors. That is what this file does, and it is the only harness
# here whose outputs are DRM connectors rather than a software backend.
#
# ── The QEMU shape, and why it is not the obvious one ────────────────────────
#
# roadmap/SCREENS.md predicted "a second -device virtio-gpu-pci". This harness
# uses ONE device carrying two scanouts instead. The first draft of this
# comment justified that with a NAME COLLISION — two cards, both connectors
# called Virtual-1, so the launcher would derive one name for two outputs.
#
# That was reasoning, and it was WRONG. Booted and read off the guest's own
# log:
#
#     [drm] pci: virtio-gpu-pci detected at 0000:00:02.0
#     [drm] number of scanouts: 1
#     [drm] forcing Virtual-1 connector on
#     [drm] Initialized virtio_gpu 0.1.0 for 0000:00:02.0 on minor 0
#     [drm] pci: virtio-gpu-pci detected at 0000:00:03.0
#     [drm] number of scanouts: 1
#     [drm] forcing Virtual-2 connector on
#     [drm] Initialized virtio_gpu 0.1.0 for 0000:00:03.0 on minor 1
#
# DRM numbers connectors per TYPE across the whole system, not per card, so
# the second device's connector is Virtual-2 and the names do not collide.
# The launcher would enumerate both correctly.
#
# The real reason to prefer max_outputs=2 is narrower and worth stating
# accurately: two devices are two GPUs (minor 0 and minor 1, card0 and card1),
# which puts the run on wlroots' multi-GPU path — a different and much rarer
# hardware shape than the one this feature is for. One device with two
# scanouts is one card with two connectors, which is what a desk with two
# monitors on one graphics card actually looks like, and what the placement
# rules were written against. Two devices is a legitimate second scenario;
# it is not the one being verified here.
#
# ── The connector force, measured rather than assumed ────────────────────────
#
# MEASURED 2026-08-13: with `-display none`, QEMU only ever advertises scanout
# 0 as enabled. Booted with max_outputs=2 and nothing else, the guest's second
# connector stays DISCONNECTED and head 1's framebuffer sits at QEMU's
# unconfigured 640x480 default forever — the guest never scans out to it. The
# kiosk then counts ONE screen, takes the single-output branch, and this
# harness would have been checking the single-screen path while claiming two.
#
# `video=Virtual-2:1024x768e` forces the connector on from the kernel side
# (DRM_FORCE_ON), which is what the boot log then says:
#
#     [drm] forcing Virtual-1 connector on
#     [drm] forcing Virtual-2 connector on
#
# and head 1 immediately becomes a real 1024x768 scanout. This is a QEMU-side
# workaround for QEMU not sending display-info for head 1 without a UI — the
# second output really is there and really is rendered to. It is NOT a product
# change and NOT a fake connector: the connector exists either way, the force
# only makes the guest believe the cable is plugged in.
#
# The mode matters. `video=Virtual-2:1280x800e` is REJECTED by the virtio-gpu
# driver ("User-defined mode not supported"), and a bare `video=Virtual-2:e`
# leaves head 1 at 5120x2160 — the largest mode in the generated EDID, an
# absurd viewport to render under pixman. 1024x768 is accepted by both
# connectors and gives two heads of equal geometry, which also makes
# "the two heads differ" a statement about CONTENT rather than about size.
#
# ── What is asserted ─────────────────────────────────────────────────────────
#
#   S1  serial: the launcher itself reports TWO connected outputs.
#   S2  serial: the launcher takes the labwc multi-output branch.
#   P0  head 0 shows a rendered desktop (Phase 7's colour/fill metric, from
#       scripts/netboot-install-smoke.sh, unchanged).
#   P1  head 1 shows a rendered desktop — the assertion that does not exist
#       anywhere else in this repository.
#   D   the two heads are not the same picture, and differ in the TOP BAND
#       where frontend/src/shell/TopBar.tsx renders the screen name.
#
# What D can and cannot tell apart, stated exactly because a discriminator
# that is oversold is worse than none:
#
#   CAN distinguish  one browser per output  from  both browsers on ONE output
#                    and the other output empty — the failure roadmap/SCREENS.md
#                    exists to prevent, and the one the control below produces.
#   CANNOT distinguish  correct mapping  from  SWAPPED mapping. Nothing here
#                    reads the text on the screen, so a config that put
#                    Virtual-1's browser on Virtual-2 and vice versa passes
#                    every assertion in this file. Verifying the mapping needs
#                    OCR or a per-screen colour, neither of which exists yet.
#
# ── The control, which is what makes any of it evidence ─────────────────────
#
#   --control break-title   keeps EVERYTHING else identical — two heads, two
#       connectors, two browsers, labwc, the real launcher — and breaks only
#       the one contract the placement rests on: the shell's window title.
#       VULOS_KIOSK_URL is set (via systemd.setenv on the kernel cmdline, so
#       no product file and no image content changes) to a URL that already
#       carries a query string. vulos-kiosk-genconfig appends its own
#       `?screen=…`, the result has two `?`, and `screen` is no longer a
#       parameter the shell can read. readScreenIdentity() then returns null,
#       the title stays the default instead of "Vulos — Virtual-2", no
#       windowRule matches, and both browsers land wherever labwc puts them.
#       P1 MUST go red. If it does not, this harness cannot see the failure it
#       claims to check and its pass means nothing.
#
#   --control single-head   the cheap control: one head, so there is genuinely
#       no second output. P1 must go red for the other reason.
#
# Usage:
#   scripts/smoke-multiscreen-qemu.sh --image out/vulos-v0.1.1-arm64.img.gz
#   scripts/smoke-multiscreen-qemu.sh --image X --control break-title --expect fail
#   scripts/smoke-multiscreen-qemu.sh --image X --control single-head --expect fail
#   scripts/smoke-multiscreen-qemu.sh --image X --show      # open QEMU windows
#
# Requirements (tool-guarded before anything runs):
#   qemu-system-aarch64 + edk2 aarch64 firmware, mtools (mcopy/mtype — the
#   kernel cmdline lives in a FAT32 ESP inside the image), python3.
#
# There is no `timeout` command on this Mac. Every wait below is a bounded
# polling loop.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
OUTDIR="$REPO/output"

EDK2_CODE="${EDK2_CODE_AA64:-/opt/homebrew/share/qemu/edk2-aarch64-code.fd}"
EDK2_VARS_SRC="${EDK2_VARS_AA64:-/opt/homebrew/share/qemu/edk2-arm-vars.fd}"

HOSTPORT="${HOSTPORT:-8099}"
TIMEOUT="${TIMEOUT:-600}"
SCREEN_TRIES="${SCREEN_TRIES:-60}"
IMAGE=""
HEADS=2
CONTROL=none
EXPECT=pass
SHOW=0
SKIP_IF_UNAVAILABLE=0
MODE_W=1024
MODE_H=768

while [ $# -gt 0 ]; do
  case "$1" in
    --image)    IMAGE="$2"; shift 2 ;;
    --control)  CONTROL="$2"; shift 2 ;;
    --expect)   EXPECT="$2"; shift 2 ;;
    --heads)    HEADS="$2"; shift 2 ;;
    --timeout)  TIMEOUT="$2"; shift 2 ;;
    --show)     SHOW=1; shift ;;
    --skip-if-unavailable) SKIP_IF_UNAVAILABLE=1; shift ;;
    -h|--help)  grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

case "$CONTROL" in
  none|break-title|single-head) ;;
  *) echo "unknown --control: $CONTROL (none|break-title|single-head)" >&2; exit 2 ;;
esac
case "$EXPECT" in pass|fail) ;; *) echo "--expect must be pass or fail" >&2; exit 2 ;; esac
[ "$CONTROL" = "single-head" ] && HEADS=1

c_b='\033[0;34m'; c_g='\033[0;32m'; c_r='\033[0;31m'; c_d='\033[2m'; c_n='\033[0m'
say()  { printf "${c_b}▸ %s${c_n}\n" "$*"; }
ok()   { printf "${c_g}✓ %s${c_n}\n" "$*"; }
die()  { printf "${c_r}✗ %s${c_n}\n" "$*" >&2; exit 1; }

# Same contract as the other smokes: without --skip-if-unavailable a missing
# tool is a hard failure. With it, a loud itemised skip that names what was
# therefore NOT verified — never a silent green.
unavailable() {
  if [ "$SKIP_IF_UNAVAILABLE" != "1" ]; then die "$*"; fi
  printf "\n${c_r}%s${c_n}\n" "════════════════════════════════════════════════════════════════"
  printf "${c_r}SKIPPED — SCREENS-02 (two-display QEMU boot) DID NOT RUN${c_n}\n"
  printf "${c_r}%s${c_n}\n" "════════════════════════════════════════════════════════════════"
  printf "Reason: %s\n\n" "$*"
  printf "NOT verified by this run:\n"
  printf "  - the real kiosk launcher sees two DRM connectors on a real boot\n"
  printf "  - it starts labwc and one browser per output\n"
  printf "  - BOTH outputs end up showing a rendered desktop\n\n"
  exit 0
}

command -v qemu-system-aarch64 >/dev/null 2>&1 \
  || unavailable "qemu-system-aarch64 not found (brew install qemu)"
command -v mcopy >/dev/null 2>&1 \
  || unavailable "mtools not found (brew install mtools) — the kernel cmdline is inside the image's FAT32 ESP"
command -v python3 >/dev/null 2>&1 || unavailable "python3 not found"
[ -f "$EDK2_CODE" ]     || unavailable "UEFI firmware not found: $EDK2_CODE"
[ -f "$EDK2_VARS_SRC" ] || unavailable "UEFI vars template not found: $EDK2_VARS_SRC"

# ── The image ────────────────────────────────────────────────────────────────
#
# No default and no search of output/. A stale image is exactly how this
# verification produces a confident wrong answer: output/vulos-live-arm64.img
# on this machine predates the multi-output launcher entirely, and booting it
# would show one screen and look like a product failure. The image must be
# named, so the run is always ABOUT a known build.
[ -n "$IMAGE" ] || unavailable "no --image given. Build one in CI (.github/workflows/release.yml, workflow_dispatch) and pass the downloaded vulos-*-arm64.img.gz — this harness deliberately does not guess."
[ -f "$IMAGE" ] || die "image not found: $IMAGE"

mkdir -p "$OUTDIR"
WORK="$OUTDIR/_ms-qemu-vda.img"
QMP="$OUTDIR/_ms-qmp.sock"
SERIAL="$OUTDIR/_ms-serial.log"
SHOT0="$OUTDIR/_ms-head0.ppm"
SHOT1="$OUTDIR/_ms-head1.ppm"

case "$IMAGE" in
  *.gz) say "Decompressing $IMAGE → $WORK"; gunzip -c "$IMAGE" > "$WORK" ;;
  *)    say "Copying $IMAGE → $WORK"; cp "$IMAGE" "$WORK" ;;
esac

# ── Patch the kernel cmdline in the ESP ──────────────────────────────────────
#
# Three changes, all of them ADDITIVE or diagnostic, none of them touching a
# single byte of the OS's own files:
#
#   drop `quiet`        so the DRM connector decisions are readable on serial.
#                       Without it the single most important line in the whole
#                       run — whether the second connector came up — is not
#                       printed anywhere.
#   add  video=…e       forces both connectors on (see the header).
#   add  systemd.setenv only in --control break-title.
#
# `splash`/plymouth are left exactly as shipped: plymouth owns the framebuffer
# early in boot and removing it would change what the compositor inherits,
# which is precisely the kind of "harness-shaped boot" this file exists to
# avoid.
ESP_OFF="$(python3 - "$WORK" <<'PY'
import struct, sys
f = open(sys.argv[1], 'rb'); f.seek(512); hdr = f.read(92)
if hdr[:8] != b'EFI PART': sys.exit("no GPT header in image")
lba, n, sz = struct.unpack('<Q', hdr[72:80])[0], struct.unpack('<I', hdr[80:84])[0], struct.unpack('<I', hdr[84:88])[0]
f.seek(lba*512)
for _ in range(n):
    e = f.read(sz)
    if e[:16] == b'\0'*16: continue
    name = e[56:128].decode('utf-16-le').rstrip('\0')
    if name.upper() == 'ESP':
        print(struct.unpack('<Q', e[32:40])[0] * 512); break
else: sys.exit("no partition named ESP")
PY
)" || die "could not locate the ESP partition in $WORK"
say "ESP at byte offset $ESP_OFF"

export MTOOLS_SKIP_CHECK=1
ENTRY="$OUTDIR/_ms-entry.conf"
mtype -i "$WORK@@$ESP_OFF" ::/loader/entries/vulos.conf > "$ENTRY" 2>/dev/null \
  || die "no ::/loader/entries/vulos.conf in the image's ESP — is this a Vulos image?"

CONTROL_SETENV=""
if [ "$CONTROL" = "break-title" ]; then
  # A URL that ALREADY carries a query string. vulos-kiosk-genconfig appends
  # `?screen=…` unconditionally, so the shell sees `c=1?screen=Virtual-1` as
  # one parameter named `c` and no `screen` at all. Nothing else changes: two
  # browsers still start, still load the shell, still fill their windows.
  CONTROL_SETENV=" systemd.setenv=VULOS_KIOSK_URL=http://localhost:8080/?vulos-control=1"
fi

VIDEO=""
i=1
while [ "$i" -le "$HEADS" ]; do
  VIDEO="$VIDEO video=Virtual-$i:${MODE_W}x${MODE_H}e"
  i=$((i + 1))
done

python3 - "$ENTRY" "$VIDEO$CONTROL_SETENV" <<'PY'
import sys
path, extra = sys.argv[1], sys.argv[2]
out = []
for line in open(path).read().splitlines():
    if line.startswith('options'):
        line = ' '.join(w for w in line.split() if w != 'quiet') + extra
    out.append(line)
open(path, 'w').write('\n'.join(out) + '\n')
PY
mcopy -o -i "$WORK@@$ESP_OFF" "$ENTRY" ::/loader/entries/vulos.conf \
  || die "failed to write the patched loader entry back into the ESP"
say "Kernel cmdline now:"
sed -n 's/^options //p' "$ENTRY" | sed "s/^/    /"

# The patch is worthless if it did not land. Read it BACK out of the image —
# mcopy can succeed against a full or read-only filesystem in ways that leave
# the old bytes in place, and a run against the unpatched cmdline would show
# one connector and look like a product failure.
mtype -i "$WORK@@$ESP_OFF" ::/loader/entries/vulos.conf 2>/dev/null \
  | grep -q "video=Virtual-1:${MODE_W}x${MODE_H}e" \
  || die "the loader entry read back out of the ESP does not contain the video= force — the patch did not land"
if [ "$CONTROL" = "break-title" ]; then
  mtype -i "$WORK@@$ESP_OFF" ::/loader/entries/vulos.conf 2>/dev/null \
    | grep -q "systemd.setenv=VULOS_KIOSK_URL=" \
    || die "control requested but systemd.setenv did not land in the ESP — the control would have run as an ordinary pass"
fi
ok "loader entry verified in the image"

# ── Boot ─────────────────────────────────────────────────────────────────────
VARS="$OUTDIR/_ms-uefi-vars.fd"
cp "$EDK2_VARS_SRC" "$VARS"     # ALWAYS fresh — a carried-over vars.fd can drop
                                # OVMF into its interactive setup screen, which
                                # looks exactly like an unbootable disk.
: > "$SERIAL"
rm -f "$QMP" "$SHOT0" "$SHOT1"

QEMU_PID=""
cleanup() { [ -n "$QEMU_PID" ] && kill "$QEMU_PID" 2>/dev/null || true; rm -f "$QMP"; }
trap cleanup EXIT INT TERM

DISPLAY_ARGS="-display none"
[ "$SHOW" = "1" ] && DISPLAY_ARGS="-display cocoa"

say "Booting with $HEADS head(s) — control=$CONTROL, expect=$EXPECT"
set -x
qemu-system-aarch64 \
  -machine virt,gic-version=3 -accel hvf -cpu host -smp 4 -m "${VM_MEM_MB:-4096}" \
  -drive if=pflash,format=raw,readonly=on,file="$EDK2_CODE" \
  -drive if=pflash,format=raw,file="$VARS" \
  -drive if=virtio,format=raw,file="$WORK" -snapshot \
  -device "virtio-gpu-pci,max_outputs=$HEADS,id=gpu0,xres=$MODE_W,yres=$MODE_H" \
  -device qemu-xhci -device usb-kbd -device usb-tablet \
  -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:${HOSTPORT}-:8080" \
  -device virtio-net-pci,netdev=n0 \
  -qmp unix:"$QMP",server,nowait \
  -serial "file:$SERIAL" \
  $DISPLAY_ARGS \
  -no-reboot &
QEMU_PID=$!
set +x

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
r = call(json.loads(cmd))
print(r, end="")
sys.exit(0 if '"error"' not in r else 1)
PY
}

# ── Wait for the OS ──────────────────────────────────────────────────────────
HTTP_URL="http://127.0.0.1:${HOSTPORT}/api/setup/status"
say "Waiting up to ${TIMEOUT}s for HTTP ($HTTP_URL)…"
deadline=$(( $(date +%s) + TIMEOUT ))
BOOTED=0
while [ "$(date +%s)" -lt "$deadline" ]; do
  if ! kill -0 "$QEMU_PID" 2>/dev/null; then
    printf "${c_r}  QEMU exited early${c_n}\n" >&2; break
  fi
  if curl -fsS --max-time 3 "$HTTP_URL" >/dev/null 2>&1; then BOOTED=1; break; fi
  printf "${c_d}  … %ds\r${c_n}" "$(( $(date +%s) - (deadline - TIMEOUT) ))"
  sleep 3
done
echo
[ "$BOOTED" = "1" ] && ok "the OS is up and serving" \
                    || printf "${c_r}✗ the OS never served HTTP${c_n}\n" >&2

# ── Pixels ───────────────────────────────────────────────────────────────────
#
# The colour/fill metric is scripts/netboot-install-smoke.sh's Phase 7,
# unchanged and deliberately not re-tuned: a rendered browser UI has hundreds
# of colours and fills the frame, a text console on black has two or three.
# Its history is the reason it is reused rather than reinvented — that phase
# used to assert only that the screendump FILE EXISTED, so it photographed a
# blank console and reported PASS for weeks.
metrics() {
  python3 - "$1" <<'PY'
import sys, hashlib
try: d = open(sys.argv[1], 'rb').read()
except OSError: print("0 0 0x0 -"); raise SystemExit
if not d.startswith(b'P6'): print("0 0 0x0 -"); raise SystemExit
vals, idx = [], 2
while len(vals) < 3:
    while idx < len(d) and d[idx:idx+1].isspace(): idx += 1
    if d[idx:idx+1] == b'#':
        while d[idx:idx+1] not in (b'\n', b''): idx += 1
        continue
    s = idx
    while idx < len(d) and not d[idx:idx+1].isspace(): idx += 1
    vals.append(int(d[s:idx]))
idx += 1
w, h, _ = vals
px = d[idx:idx + w*h*3]
step, colours, nonblack, n = 3*17, set(), 0, 0
for i in range(0, max(len(px)-2, 0), step):
    c = px[i:i+3]; colours.add(bytes(c)); n += 1
    if sum(c) > 30: nonblack += 1
print(f"{len(colours)} {100*nonblack//max(n,1)} {w}x{h} {hashlib.sha256(px).hexdigest()[:12]}")
PY
}

# D — are the two heads the same picture? Compared over EVERY pixel of the top
# band (where TopBar renders the screen name), not a sample: the discriminating
# content is a few words of text and a 1-in-17 sample can miss it entirely.
#
# "The frames differ" on its own is NOT a safe test, and the dry run of this
# harness proved it: two blank text consoles alternated between two hashes
# because of the blinking cursor. Head 0 and head 1 are photographed a few
# milliseconds apart, so ANY animation — a cursor, a clock, a spinner — makes
# two screens differ while showing the same thing.
#
# So the run measures its own noise floor. Three shots, in this order:
# head0, head1, head0 again. diff(head0a, head0b) is what one screen does to
# itself across the same interval; diff(head0a, head1) is what the two screens
# do to each other. The second must EXCEED the first, or the difference is
# time passing rather than two different desktops.
compare() {
  python3 - "$1" "$2" <<'PY'
import sys
def read(p):
    d = open(p, 'rb').read()
    vals, idx = [], 2
    while len(vals) < 3:
        while idx < len(d) and d[idx:idx+1].isspace(): idx += 1
        s = idx
        while idx < len(d) and not d[idx:idx+1].isspace(): idx += 1
        vals.append(int(d[s:idx]))
    idx += 1
    w, h, _ = vals
    return w, h, d[idx:idx + w*h*3]
try:
    w0, h0, a = read(sys.argv[1]); w1, h1, b = read(sys.argv[2])
except Exception:
    print("unreadable 0 0"); raise SystemExit
if (w0, h0) != (w1, h1):
    print(f"different-geometry {w0}x{h0} {w1}x{h1}"); raise SystemExit
band = int(h0 * 0.08) or 1
row = w0 * 3
diff = sum(1 for i in range(0, band*row, 3) if a[i:i+3] != b[i:i+3])
tot = band * w0
print(f"{'identical' if a == b else 'differ'} {100*diff//max(tot,1)} {band}")
PY
}

say "Photographing head 0${HEADS:+ and head 1} until both render (or $((SCREEN_TRIES*5))s)…"
M0="0 0 0x0 -"; M1="0 0 0x0 -"; CMP="(not compared)"
P0=0; P1=0
i=0
while [ "$i" -lt "$SCREEN_TRIES" ]; do
  kill -0 "$QEMU_PID" 2>/dev/null || break
  qmp "{\"execute\":\"screendump\",\"arguments\":{\"filename\":\"$SHOT0\",\"device\":\"gpu0\",\"head\":0}}" >/dev/null 2>&1 || true
  if [ "$HEADS" -gt 1 ]; then
    qmp "{\"execute\":\"screendump\",\"arguments\":{\"filename\":\"$SHOT1\",\"device\":\"gpu0\",\"head\":1}}" >/dev/null 2>&1 || true
  fi
  M0="$(metrics "$SHOT0")"; M1="$(metrics "$SHOT1")"
  c0=$(echo "$M0" | cut -d' ' -f1); f0=$(echo "$M0" | cut -d' ' -f2)
  c1=$(echo "$M1" | cut -d' ' -f1); f1=$(echo "$M1" | cut -d' ' -f2)
  P0=0; P1=0
  [ "${c0:-0}" -ge 16 ] && [ "${f0:-0}" -ge 5 ] && P0=1
  [ "${c1:-0}" -ge 16 ] && [ "${f1:-0}" -ge 5 ] && P1=1
  printf "${c_d}  … %2d  head0[%s] %s   head1[%s] %s\r${c_n}" "$i" "$P0" "$M0" "$P1" "$M1"
  [ "$P0" = "1" ] && [ "$P1" = "1" ] && break
  i=$((i + 1)); sleep 5
done
echo

# The noise-floor shot: head 0 again, immediately after head 1, so the interval
# it covers BRACKETS the interval between the two heads' captures.
SHOT0B="$OUTDIR/_ms-head0-again.ppm"
NOISE="n/a 0 0"
if [ "$HEADS" -gt 1 ]; then
  qmp "{\"execute\":\"screendump\",\"arguments\":{\"filename\":\"$SHOT0B\",\"device\":\"gpu0\",\"head\":0}}" >/dev/null 2>&1 || true
  CMP="$(compare "$SHOT0" "$SHOT1")"
  NOISE="$(compare "$SHOT0" "$SHOT0B")"
fi

# ── Serial evidence ──────────────────────────────────────────────────────────
S1=0; S2=0
KIOSK_LINES="$(grep -a "vulos-kiosk:" "$SERIAL" 2>/dev/null | sed 's/\x1b\[[0-9;]*m//g' | sort -u || true)"
grep -aq "vulos-kiosk: screen identity .*(2 connected:" "$SERIAL" 2>/dev/null && S1=1
grep -aq "vulos-kiosk: 2 screens .* labwc, one browser per output" "$SERIAL" 2>/dev/null && S2=1

echo ""
printf "${c_b}══ SCREENS-02 evidence ══${c_n}\n"
printf "  image             %s\n" "$IMAGE"
printf "  heads             %s   control=%s\n" "$HEADS" "$CONTROL"
printf "  DRM force         %s\n" "$(grep -ao "forcing Virtual-[0-9] connector on" "$SERIAL" 2>/dev/null | sort -u | tr '\n' ' ')"
printf "  OS serving HTTP   %s\n" "$([ "$BOOTED" = 1 ] && echo yes || echo NO)"
printf "  S1 two connectors seen by the launcher   %s\n" "$([ "$S1" = 1 ] && echo yes || echo NO)"
printf "  S2 labwc multi-output branch taken       %s\n" "$([ "$S2" = 1 ] && echo yes || echo NO)"
printf "  P0 head 0 renders a desktop              %s   (colours fill%% size sha) %s\n" "$([ "$P0" = 1 ] && echo yes || echo NO)" "$M0"
printf "  P1 head 1 renders a desktop              %s   (colours fill%% size sha) %s\n" "$([ "$P1" = 1 ] && echo yes || echo NO)" "$M1"
printf "  D  head0 vs head1   (state topband-diff%% band)  %s\n" "$CMP"
printf "  D  head0 vs itself  (noise floor, same interval) %s\n" "$NOISE"
if [ -n "$KIOSK_LINES" ]; then
  echo "  launcher said:"
  echo "$KIOSK_LINES" | sed 's/^/    /'
else
  echo "  launcher said: (nothing — no vulos-kiosk: lines on serial)"
fi
printf "  screendumps       %s  %s\n" "$SHOT0" "$SHOT1"
printf "  serial            %s\n" "$SERIAL"
echo ""

VERDICT=pass
DIFFSTATE="$(echo "$CMP" | cut -d' ' -f1)"
CROSS="$(echo "$CMP" | cut -d' ' -f2)"
SELF="$(echo "$NOISE" | cut -d' ' -f2)"
for cond in "$BOOTED" "$S1" "$S2" "$P0" "$P1"; do
  [ "$cond" = "1" ] || VERDICT=fail
done
[ "$DIFFSTATE" = "differ" ] || VERDICT=fail
# Strictly greater. Equal means the two heads differ by no more than one head
# differs from itself over the same interval — i.e. by time, not by content.
[ "${CROSS:-0}" -gt "${SELF:-0}" ] 2>/dev/null || VERDICT=fail

if [ "$SHOW" = "1" ]; then say "QEMU window open — Ctrl-C to stop."; wait "$QEMU_PID"; fi
qmp '{"execute":"quit"}' >/dev/null 2>&1 || true

if [ "$VERDICT" = "$EXPECT" ]; then
  if [ "$EXPECT" = "pass" ]; then
    ok "SCREENS-02 PASS — the real launcher put a rendered desktop on BOTH DRM outputs"
  else
    ok "SCREENS-02 CONTROL BEHAVED — expected a failing run under --control $CONTROL and got one"
  fi
  exit 0
fi

if [ "$EXPECT" = "fail" ]; then
  die "CONTROL DID NOT FAIL. --control $CONTROL was supposed to break placement and the run still passed every assertion — this harness cannot see the failure it claims to check, so its green runs prove nothing."
fi
die "SCREENS-02 FAIL — see the evidence block above; the first NO is the one to chase"
