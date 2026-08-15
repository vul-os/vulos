#!/usr/bin/env bash
# arch-emulation-bench.sh — measure the real cost of running an x86_64 binary
# on an arm64 Vulos box. Owned by roadmap/ARCH-PLACEMENT.md.
#
# ── Why this shape, and what the first two attempts got wrong ────────────────
#
# ATTEMPT 1 compared `docker run --platform linux/arm64` against
# `docker run --platform linux/amd64` on this Apple-Silicon Mac. The emulated
# case came out FASTER than native, which is the signature of a broken
# measurement. Two reasons, both disqualifying:
#
#   1. On macOS, Docker/OrbStack translates x86_64 with **Rosetta 2**, not
#      qemu-user. Rosetta is an Apple-Silicon-only AOT translator that reaches
#      a large fraction of native speed. A Vulos arm64 box is a Raspberry Pi,
#      an Ampere server or an arm64 VM running Debian — **Rosetta does not
#      exist there.** Measuring Rosetta and reporting it as "emulation cost on
#      an ARM box" would be a confident wrong answer.
#   2. The host is under heavy load (other agents running containers), so
#      absolute wall times drifted ~80% run to run.
#
# THIS version fixes both:
#   - Everything runs in ONE arm64 container and each emulator is invoked
#     **explicitly by name** (`qemu-x86_64 …`, `box64 …`). binfmt_misc is never
#     consulted, so Rosetta cannot silently service the call. What is measured
#     is the software that would actually be installed on a Vulos ARM box.
#   - The three cases are **interleaved** (native, qemu, box64, native, qemu,
#     box64, …) rather than run in blocks, so host-load drift hits all three
#     roughly equally instead of biasing whichever ran during a quiet patch.
#   - The reported figure is the **median**, and the raw samples are printed so
#     a reader can see the spread and judge the number for themselves.
#
# ── The control ─────────────────────────────────────────────────────────────
#
# The workload is `busybox gzip`, taken from Debian's own `busybox-static`
# package downloaded for BOTH arm64 and amd64. Same upstream source, same
# Debian version, same build system, statically linked — so the only variable
# is the instruction set and who executes it. A statically linked binary also
# removes the "did the emulator find an x86_64 libc" failure mode from the
# measurement entirely.
#
# Deliberately NOT used: openssl speed, or anything crypto/SIMD heavy. aarch64
# has crypto extensions the emulators do not model, so such a benchmark would
# measure the extension gap rather than the emulator.
#
# Usage: scripts/arch-emulation-bench.sh [reps]     (default 5)
set -uo pipefail

REPS="${1:-5}"

docker run --rm --platform linux/arm64 debian:trixie-slim sh -s -- "$REPS" <<'INNER'
set -u
REPS="$1"

log() { echo "$*" >&2; }

log "=== host container arch: $(uname -m) ==="

dpkg --add-architecture amd64
apt-get update -qq >/dev/null 2>&1
# qemu-user  -> /usr/bin/qemu-x86_64   (universal, slow, full-system-call TCG)
# box64      -> /usr/bin/box64         (aarch64-only, faster, partial coverage)
# time       -> /usr/bin/time -v       (max RSS, for the memory number)
apt-get install -y -qq qemu-user box64 time >/dev/null 2>&1

echo "--- emulator versions (Debian trixie arm64 packages) ---"
qemu-x86_64 --version 2>&1 | head -1
box64 --version 2>&1 | head -2
echo

mkdir -p /opt/n /opt/x /w && cd /w
apt-get download busybox-static busybox-static:amd64 >/dev/null 2>&1
for d in busybox-static_*_arm64.deb; do dpkg -x "$d" /opt/n; done
for d in busybox-static_*_amd64.deb; do dpkg -x "$d" /opt/x; done

NB=$(find /opt/n -name busybox -type f | head -1)
XB=$(find /opt/x -name busybox -type f | head -1)
echo "--- the two binaries under test ---"
file_out() { head -c 20 "$1" | od -An -tx1 | head -1; }
echo "native arm64 : $NB"
echo "amd64        : $XB"
echo

echo "--- PROOF the x86_64 binary actually executes under each emulator ---"
echo -n "qemu-x86_64: "; qemu-x86_64 "$XB" echo "OK-x86_64-ran-under-qemu" 2>&1 | head -1
echo -n "box64      : "; box64 "$XB" echo "OK-x86_64-ran-under-box64" 2>&1 | head -1
echo

# Deterministic payload, identical bytes in every case.
seq 1 3000000 > /tmp/a.bin
"$NB" yes "vulos arch bench payload 0123456789 abcdefghijklmnop" 2>/dev/null | head -c 8388608 > /tmp/b.bin
cat /tmp/a.bin /tmp/b.bin /tmp/a.bin > /tmp/work.bin
echo "payload: $(stat -c %s /tmp/work.bin) bytes"
echo

ms() {  # ms <cmd...>  -> elapsed milliseconds on stdout
  S=$(date +%s%N)
  "$@" gzip -9 < /tmp/work.bin > /dev/null 2>/dev/null
  E=$(date +%s%N)
  echo $(( (E-S)/1000000 ))
}

N_S=""; Q_S=""; B_S=""
echo "--- interleaved timing, $REPS reps (ms per gzip -9 pass) ---"
r=1
while [ "$r" -le "$REPS" ]; do
  n=$(ms "$NB")
  q=$(ms qemu-x86_64 "$XB")
  b=$(ms box64 "$XB")
  echo "rep $r: native=${n}  qemu-x86_64=${q}  box64=${b}"
  N_S="$N_S $n"; Q_S="$Q_S $q"; B_S="$B_S $b"
  r=$((r+1))
done

median() { echo "$1" | tr ' ' '\n' | grep -v '^$' | sort -n | awk '{a[NR]=$1} END{print (NR%2)?a[(NR+1)/2]:int((a[NR/2]+a[NR/2+1])/2)}'; }
NM=$(median "$N_S"); QM=$(median "$Q_S"); BM=$(median "$B_S")

echo
echo "=== MEDIANS ==="
echo "native arm64 : ${NM} ms   (1.00x)"
[ "$NM" -gt 0 ] && echo "qemu-x86_64  : ${QM} ms   ($(awk -v a=$QM -v b=$NM 'BEGIN{printf "%.2f", a/b}')x native)"
[ "$NM" -gt 0 ] && echo "box64        : ${BM} ms   ($(awk -v a=$BM -v b=$NM 'BEGIN{printf "%.2f", a/b}')x native)"

echo
echo "=== PROCESS START-UP COST (20x 'busybox true', total ms) ==="
echo "  GUI apps spawn many short-lived helper processes; per-exec translation"
echo "  overhead is felt as sluggishness that a throughput number hides."
st() { S=$(date +%s%N); i=0; while [ $i -lt 20 ]; do "$@" true >/dev/null 2>&1; i=$((i+1)); done; E=$(date +%s%N); echo $(( (E-S)/1000000 )); }
echo "native arm64 : $(st "$NB") ms"
echo "qemu-x86_64  : $(st qemu-x86_64 "$XB") ms"
echo "box64        : $(st box64 "$XB") ms"

echo
echo "=== PEAK RESIDENT MEMORY for one gzip -9 pass (KB) ==="
mem() { /usr/bin/time -f "%M" "$@" gzip -9 < /tmp/work.bin > /dev/null 2>/tmp/m; cat /tmp/m; }
echo "native arm64 : $(mem "$NB") KB"
echo "qemu-x86_64  : $(mem qemu-x86_64 "$XB") KB"
echo "box64        : $(mem box64 "$XB") KB"
INNER
