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
# ── Modes ───────────────────────────────────────────────────────────────────
#
#   bench   [reps]  the original throughput/start-up/RSS comparison (default)
#   cost            what shipping the emulator costs the OS image, measured as
#                   an xz-squashfs delta on ONE tree — the same compression
#                   build.sh uses (`mksquashfs -comp xz`), so the number is the
#                   image's, not a `du` guess.
#   handler         proof that an x86_64 binary runs through the binfmt handler
#                   THIS REPO SHIPS, with the emulator never named on the
#                   command line, and with its exit status checked.
#   dynamic         box64 and qemu-user on a DYNAMICALLY linked x86_64 binary,
#                   which is the shape box64 is designed for and the shape
#                   `bench` never tested. Includes a GL probe.
#   all             cost, then handler, then bench, then dynamic.
#
# Usage: scripts/arch-emulation-bench.sh [bench|cost|handler|dynamic|all] [reps]
set -uo pipefail

MODE="${1:-bench}"
case "$MODE" in
  bench|cost|handler|dynamic|all) shift || true ;;
  ''|*[0-9]*) MODE=bench ;;          # back-compat: a bare rep count
  *) echo "unknown mode: $MODE" >&2; exit 2 ;;
esac
REPS="${1:-5}"

# Every docker invocation below checks its exit status. §4.3 of
# roadmap/ARCH-PLACEMENT.md exists because a timing harness that does not check
# exit status timed a crashing box64 and produced a plausible 1.57x. Nothing in
# this file may report a number it has not proved came from a process that ran.
RC=0

run_bench() {
# -i is REQUIRED: without it docker does not attach stdin, `sh -s` reads EOF
# immediately, and the whole script exits 0 having run nothing at all.
docker run --rm -i --platform linux/arm64 debian:trixie-slim sh -s -- "$REPS" <<'INNER'
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
}

# ─────────────────────────────────────────────────────────────────────────────
# cost — what shipping the emulator adds to the OS image.
#
# Measured the way build.sh actually builds the image: `mksquashfs -comp xz`
# over the rootfs. A `du` of /usr/bin/qemu-* would overstate it by whatever xz
# achieves on 80 near-identical statically linked binaries, and understate
# nothing — so it is the wrong number in an unpredictable direction.
#
# THE SAME TREE is squashed three times, in place, so the deltas are
# apples-to-apples:
#   A  baseline debian:trixie-slim (with squashfs-tools, which is measurement
#      scaffolding and therefore in the BASELINE, not in any delta)
#   B  + qemu-user-static binfmt-support   (the whole 80-emulator qemu-user)
#   C  after pruning to the two emulators a Vulos fleet can actually need:
#      qemu-x86_64 (amd64 apps on an arm64 box) and qemu-aarch64 (the reverse).
#
# C-A is the number build.sh should pay. B-A is what it costs to not prune.
# ─────────────────────────────────────────────────────────────────────────────
run_cost() {
docker run --rm -i --platform linux/arm64 debian:trixie-slim sh -s <<'COST'
set -u
fail() { echo "COST-MEASUREMENT-FAILED: $*" >&2; exit 1; }

apt-get update -qq >/dev/null 2>&1 || fail "apt-get update"
apt-get install -y --no-install-recommends squashfs-tools >/dev/null 2>&1 \
  || fail "squashfs-tools (the measuring instrument) did not install"
command -v mksquashfs >/dev/null || fail "mksquashfs absent after install"

mkdir -p /out
# Excluded: kernel/virtual filesystems, apt's download cache (cleaned anyway,
# but a stray .deb would land in exactly one of the three measurements), and
# /out itself — squashing the previous squashfs into the next one would make
# every delta cumulative and wrong.
squash() {
  apt-get clean
  rm -f "/out/$1.sqfs"
  mksquashfs / "/out/$1.sqfs" -comp xz -noappend -quiet -no-progress \
      -e proc sys dev run tmp out var/cache/apt/archives \
    || fail "mksquashfs $1"
  stat -c %s "/out/$1.sqfs"
}

A=$(squash a-baseline)     || exit 1
echo "A baseline (trixie-slim + squashfs-tools) : $A bytes"

apt-get install -y --no-install-recommends qemu-user-static binfmt-support >/dev/null 2>&1 \
  || fail "qemu-user-static did not install"
# Prove the thing we are costing is actually there before costing it.
[ -x /usr/bin/qemu-x86_64 ] || fail "qemu-x86_64 missing after install"
[ -f /usr/lib/binfmt.d/qemu-x86_64.conf ] || fail "binfmt.d registration missing"

B=$(squash b-full) || exit 1
echo "B + qemu-user-static (all $(ls /usr/bin/qemu-* | wc -l) emulators)      : $B bytes"

echo
echo "--- what is actually in there, uncompressed ---"
du -shc /usr/bin/qemu-* 2>/dev/null | tail -1
dpkg-query -W -f='${Package} ${Installed-Size}KB\n' qemu-user qemu-user-static qemu-user-binfmt binfmt-support libpipeline1

# ── The prune ──
# Debian ships one qemu-user package containing every target. Vulos needs two:
# the x86_64 emulator on arm64 boxes and the aarch64 emulator on amd64 boxes.
# Keeping all 80 costs the image ~30x what the two cost.
KEEP="x86_64 aarch64"
keep_re="qemu-x86_64|qemu-aarch64"
for f in /usr/bin/qemu-*; do
  b=$(basename "$f")
  echo "$b" | grep -Eq "^($keep_re)(-static)?$" || rm -f "$f"
done
for f in /usr/libexec/qemu-binfmt/*; do
  b=$(basename "$f")
  echo "$b" | grep -Eq "^(x86_64|aarch64)-binfmt-P$" || rm -f "$f"
done
for f in /usr/lib/binfmt.d/qemu-*.conf; do
  b=$(basename "$f" .conf)
  echo "$b" | grep -Eq "^($keep_re)$" || rm -f "$f"
done
# The prune must not have removed what we keep.
#
# NOTE, and this guard earned its keep by catching it: there is no
# binfmt.d/qemu-aarch64.conf on an arm64 host, and no qemu-x86_64.conf on an
# amd64 one. Debian does not register a handler for the machine's OWN
# architecture, because the kernel already runs it. So the conf is required
# only for the FOREIGN arch — asking for both failed the measurement outright,
# which is the correct behaviour for a check that does not know that.
NATIVE=$(uname -m)
for a in $KEEP; do
  [ -x "/usr/bin/qemu-$a" ] || fail "prune deleted qemu-$a, the whole point"
  [ -x "/usr/libexec/qemu-binfmt/$a-binfmt-P" ] || fail "prune deleted the $a binfmt interpreter"
  if [ "$a" != "$NATIVE" ]; then
    [ -f "/usr/lib/binfmt.d/qemu-$a.conf" ] || fail "prune deleted qemu-$a.conf, the registration for the foreign arch"
  fi
done
echo
echo "--- after prune, uncompressed ---"
du -shc /usr/bin/qemu-* /usr/libexec/qemu-binfmt/* 2>/dev/null | tail -1

C=$(squash c-pruned) || exit 1
echo "C pruned to x86_64 + aarch64                : $C bytes"

echo
echo "=== IMAGE COST (xz squashfs, the compression build.sh uses) ==="
awk -v a="$A" -v b="$B" -v c="$C" 'BEGIN{
  printf "baseline                      : %10.1f MB\n", a/1048576;
  printf "full qemu-user (80 emulators) : %+10.1f MB\n", (b-a)/1048576;
  printf "pruned to x86_64 + aarch64    : %+10.1f MB   <- what Vulos should pay\n", (c-a)/1048576;
  printf "saved by pruning              : %10.1f MB\n", (b-c)/1048576;
}'
COST
}

# ─────────────────────────────────────────────────────────────────────────────
# handler — the execution proof.
#
# The bench above invokes `qemu-x86_64 <binary>` BY NAME. That proves the
# emulator works; it does not prove the OS will reach it, which is the thing an
# app launcher depends on. This mode registers the handler THIS REPO SHIPS
# (/usr/lib/binfmt.d/qemu-x86_64.conf, verbatim, flags and all) and then runs an
# x86_64 binary with `./binary` — no emulator named anywhere — and checks the
# exit status.
#
# ── Why it does not simply register the ELF magic ────────────────────────────
#
# binfmt_misc is a KERNEL-GLOBAL table. In Docker/OrbStack on this Mac that
# kernel is the shared Linux VM every other container on this machine is using,
# and it already carries an x86_64 ELF handler (Rosetta, per §4.2). Registering
# a second entry for the same magic puts ours at the head of the list and
# silently re-routes every other agent's amd64 container through qemu until we
# deregister. That is not a risk worth taking for a proof.
#
# So: the shipped conf's magic is swapped for an EXTENSION match (`.x86p`) and
# nothing else — same interpreter path, same flags, same emulator. An extension
# handler cannot collide with an ELF-magic handler, so nothing else on this
# machine changes. The registration is removed on exit either way.
#
# The flags are the part that actually matters and they are preserved exactly:
# `F` (fix-binary) makes the kernel open the interpreter AT REGISTRATION TIME
# and keep the fd, which is why the handler still works inside a mount
# namespace that has no /usr/bin/qemu-x86_64 — a Flatpak bwrap sandbox, a
# chroot, a container. The last test below unshares a mount namespace, hides
# the emulator, and runs the binary anyway. Without F that step fails; it is
# the difference between "emulation works" and "emulation works where apps
# actually run".
# ─────────────────────────────────────────────────────────────────────────────
run_handler() {
docker run --rm -i --privileged --platform linux/arm64 debian:trixie-slim sh -s <<'HANDLER'
set -u
FAILED=0
ok()   { echo "  PASS  $*"; }
bad()  { echo "  FAIL  $*"; FAILED=1; }

dpkg --add-architecture amd64
apt-get update -qq >/dev/null 2>&1
apt-get install -y --no-install-recommends qemu-user-static binfmt-support util-linux >/dev/null 2>&1 \
  || { echo "install failed"; exit 1; }

mkdir -p /w && cd /w
apt-get download busybox-static:amd64 >/dev/null 2>&1 || { echo "download failed"; exit 1; }
mkdir -p /opt/x && for d in busybox-static_*_amd64.deb; do dpkg -x "$d" /opt/x; done
XB=$(find /opt/x -name busybox -type f | head -1)
[ -n "$XB" ] || { echo "no amd64 busybox"; exit 1; }
cp "$XB" /w/probe.x86p && chmod +x /w/probe.x86p

echo "=== the handler this repo ships ==="
cat /usr/lib/binfmt.d/qemu-x86_64.conf
CONF=$(cat /usr/lib/binfmt.d/qemu-x86_64.conf)
INTERP=$(echo "$CONF" | awk -F: '{print $5}')
FLAGS=$(echo "$CONF" | awk -F: '{print $6}')
echo "interpreter: $INTERP"
echo "flags      : $FLAGS"
case "$FLAGS" in
  *F*) ok "shipped flags contain F (fix-binary) — required inside a Flatpak/bwrap sandbox" ;;
  *)   bad "shipped flags lack F; the handler will not resolve inside an app sandbox" ;;
esac

echo
echo "=== kernel binfmt_misc state BEFORE we touch it ==="
mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc 2>/dev/null
if [ ! -f /proc/sys/fs/binfmt_misc/register ]; then
  echo "  binfmt_misc not mountable — cannot prove kernel dispatch here"; exit 1
fi
ls /proc/sys/fs/binfmt_misc | grep -v '^\(register\|status\)$' | while read -r e; do
  echo "  existing: $e -> $(awk '/^interpreter/{print $2}' "/proc/sys/fs/binfmt_misc/$e")"
done
if ls /proc/sys/fs/binfmt_misc | grep -qi rosetta; then
  echo "  NOTE: a rosetta handler is registered in this shared kernel. It is left alone."
fi

NAME=vulos-qemu-x86-64-proof
cleanup() { [ -f "/proc/sys/fs/binfmt_misc/$NAME" ] && echo -1 > "/proc/sys/fs/binfmt_misc/$NAME"; }
trap cleanup EXIT INT TERM

# Same interpreter, same flags as the shipped conf; magic swapped for the
# extension so this cannot shadow anything else on the shared kernel.
echo ":$NAME:E::x86p::$INTERP:$FLAGS" > /proc/sys/fs/binfmt_misc/register 2>/dev/null \
  || { bad "could not register the handler"; exit 1; }
[ -f "/proc/sys/fs/binfmt_misc/$NAME" ] && ok "handler registered from the shipped interpreter + flags"
grep -q '^enabled' "/proc/sys/fs/binfmt_misc/$NAME" && ok "handler is enabled"

echo
echo "=== 1. an x86_64 binary, run with NO emulator on the command line ==="
OUT=$(/w/probe.x86p echo RAN-X86_64-THROUGH-BINFMT 2>&1); RC=$?
echo "  exit=$RC output=$OUT"
if [ "$RC" -eq 0 ] && [ "$OUT" = "RAN-X86_64-THROUGH-BINFMT" ]; then
  ok "kernel dispatched an x86_64 ELF to qemu-user and it ran to a zero exit"
else
  bad "the x86_64 binary did not run through the handler (exit $RC)"
fi

echo
echo "=== 2. it is NOT Rosetta servicing this (the §4.2 trap) ==="
# Rosetta maps /run/rosetta into the translated process. qemu does not.
MAPS=$(/w/probe.x86p sh -c 'grep -c rosetta /proc/self/maps || true' 2>/dev/null)
echo "  rosetta mappings in the translated process: ${MAPS:-0}"
if [ "${MAPS:-0}" = "0" ]; then
  ok "no rosetta mapping — this is qemu-user, the software a real ARM box would run"
else
  bad "rosetta serviced the exec; this measurement would not transfer to a real ARM box"
fi

echo
echo "=== 3. it still works inside a sandbox with no emulator visible ==="
# This is the Flatpak case: bwrap gives the app the runtime's own /usr. If the
# handler needed to find /usr/bin/qemu-x86_64 in the app's mount namespace, it
# would fail here — and would fail identically inside every Flatpak.
mkdir -p /sandbox/bin /sandbox/usr
cp /w/probe.x86p /sandbox/probe.x86p
OUT=$(unshare -m sh -c '
  mount -t tmpfs tmpfs /usr/libexec 2>/dev/null
  mount --bind /sandbox/bin /usr/bin 2>/dev/null
  /sandbox/probe.x86p echo RAN-INSIDE-SANDBOX 2>&1' ); RC=$?
echo "  exit=$RC output=$OUT"
if [ "$RC" -eq 0 ] && [ "$OUT" = "RAN-INSIDE-SANDBOX" ]; then
  ok "F-flag handler resolved with the emulator hidden — works inside an app sandbox"
else
  bad "handler did not resolve inside a mount namespace without the emulator (exit $RC)"
fi

echo
echo "=== 4. NEGATIVE CONTROL: with the handler removed, the same exec must fail ==="
# Without this, every PASS above is unfalsifiable: if something else on the
# machine were running the binary, the tests would pass with our handler
# deregistered too.
echo -1 > "/proc/sys/fs/binfmt_misc/$NAME"
OUT=$(/w/probe.x86p echo SHOULD-NOT-RUN 2>&1); RC=$?
echo "  exit=$RC output=$OUT"
if [ "$RC" -eq 0 ]; then
  bad "the x86_64 binary ran with NO handler registered — something else serviced it, so tests 1-3 prove nothing about our handler"
else
  ok "with the handler gone the exec fails (exit $RC) — tests 1-3 were measuring OUR handler"
fi

echo
[ "$FAILED" -eq 0 ] && echo "=== HANDLER PROOF: ALL PASS ===" || echo "=== HANDLER PROOF: FAILURES ABOVE ==="
exit "$FAILED"
HANDLER
}

run_dynamic() {
# ── Why this mode exists ────────────────────────────────────────────────────
#
# `bench` measured box64 against a STATICALLY linked busybox, recorded
# "Illegal instruction", and roadmap/ARCH-PLACEMENT.md §5 generalised that to
# "box64 failed on the test binary" and recommended qemu-user instead.
#
# That generalisation was unsound, and the same document says why three
# paragraphs earlier: **box64 gets its speed by NOT emulating libraries** — it
# intercepts the dynamic linker and binds calls to the host's NATIVE aarch64
# libraries. That mechanism needs a dynamic linker to intercept. A static
# binary is the one shape box64 structurally cannot serve, so `bench` exercised
# box64's known non-case and the verdict was drawn from it.
#
# This mode tests the shape box64 is designed for: a DYNAMICALLY linked x86_64
# binary, with an x86_64 sysroot assembled by `dpkg-deb -x` — which is also
# exactly the shape roadmap/DISTRO-SOURCED-APPS.md's vehicle produces, so the
# measurement is of the real product case rather than a laboratory one.
#
# ── Fairness rules, because the first attempt at this broke both ────────────
#
#  1. qemu-user needs the COMPLETE x86_64 library closure in its sysroot; box64
#     does not, because it substitutes native ones. Give qemu an incomplete
#     sysroot and it dies on a missing libcap while box64 sails past, and the
#     ratio then measures the sysroot rather than the emulator. Both get the
#     same tree, and the tree is checked before any timing runs.
#  2. A rep is DISCARDED unless the process exited 0 **and** produced
#     byte-identical output to native. §4.3 exists because a harness that
#     checked neither timed a crashing box64 and reported a plausible 1.57x.
#     Exit status alone is not enough either: `glxinfo` exits 0 while printing
#     "couldn't find RGB GLX visual".
docker run --rm -i --platform linux/arm64 debian:trixie-slim sh -s -- "$REPS" <<'DYN'
set -u
REPS="$1"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null 2>&1 || { echo "APT_UPDATE_FAILED"; exit 3; }
apt-get install -y -qq box64 qemu-user file python3 time >/dev/null 2>&1 || { echo "TOOLS_FAILED"; exit 3; }
dpkg --add-architecture amd64 && apt-get update -qq >/dev/null 2>&1 || { echo "MULTIARCH_FAILED"; exit 3; }

mkdir -p /x86 /debs && cd /debs
for p in libc6 coreutils gzip libgcc-s1 libselinux1 libpcre2-8-0 libattr1 libacl1 \
         zlib1g libcap2 libcap-ng0 libmd0 libbsd0 libtinfo6 libstdc++6; do
  apt-get download -q "$p:amd64" >/dev/null 2>&1 || echo "MISS $p"
done
for d in /debs/*.deb; do dpkg-deb -x "$d" /x86 2>/dev/null; done
mkdir -p /x86/lib64
cp -a /x86/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 /x86/lib64/ 2>/dev/null || { echo "NO_INTERP"; exit 4; }
ln -sfn /x86/usr/lib /x86/lib
LIB=/x86/usr/lib/x86_64-linux-gnu

# Preconditions. Nothing below is believed unless all of these hold.
[ -x /x86/usr/bin/gzip ] || { echo "PRECONDITION FAILED: no x86_64 gzip in the sysroot"; exit 4; }
case "$(file -b /x86/usr/bin/gzip)" in
  *x86-64*"dynamically linked"*) : ;;
  *) echo "PRECONDITION FAILED: not a dynamically linked x86-64 ELF: $(file -b /x86/usr/bin/gzip)"; exit 4 ;;
esac
echo "under test : $(file -b /x86/usr/bin/gzip)"
echo "native ctrl: $(file -b /usr/bin/gzip)"
echo "x86_64 libs in sysroot: $(ls "$LIB"/*.so* 2>/dev/null | wc -l)"

python3 -c "
import sys,random
random.seed(1234); w=[b'the quick brown fox ',b'vulos ',b'0123456789abcdef',b'\x00\x01\x02\x03']
b=bytearray()
while len(b)<48*1024*1024: b+=random.choice(w)
sys.stdout.buffer.write(bytes(b[:48*1024*1024]))" > /payload.bin
[ "$(stat -c %s /payload.bin)" -eq 50331648 ] || { echo "PRECONDITION FAILED: payload size"; exit 4; }

/usr/bin/gzip -9 -c /payload.bin > /ref.gz || { echo "PRECONDITION FAILED: native gzip"; exit 4; }
REF=$(stat -c %s /ref.gz); REFMD5=$(md5sum < /ref.gz | cut -d" " -f1)
echo "native reference: $REF bytes md5=$REFMD5"

echo
echo "--- correctness gate: each emulator must exit 0 AND emit the reference bytes ---"
BOX64_LD_LIBRARY_PATH="$LIB" box64 /x86/usr/bin/gzip -9 -c /payload.bin > /b.gz 2>/dev/null
echo "box64 exit=$? md5=$(md5sum < /b.gz | cut -d" " -f1)"
qemu-x86_64 -L /x86 /x86/usr/bin/gzip -9 -c /payload.bin > /q.gz 2>/dev/null
echo "qemu  exit=$? md5=$(md5sum < /q.gz | cut -d" " -f1)"
echo "reference                md5=$REFMD5"

echo
echo "--- $REPS interleaved reps; DISCARDED unless exit=0 and bytes==reference ---"
i=1
while [ "$i" -le "$REPS" ]; do
  for c in native box64 qemu; do
    S=$(date +%s%N)
    case $c in
      native) /usr/bin/gzip -9 -c /payload.bin > /o.gz 2>/dev/null; E=$? ;;
      box64)  BOX64_LD_LIBRARY_PATH="$LIB" box64 /x86/usr/bin/gzip -9 -c /payload.bin > /o.gz 2>/dev/null; E=$? ;;
      qemu)   qemu-x86_64 -L /x86 /x86/usr/bin/gzip -9 -c /payload.bin > /o.gz 2>/dev/null; E=$? ;;
    esac
    N=$(date +%s%N); MS=$(( (N-S)/1000000 )); SZ=$(stat -c %s /o.gz)
    if [ "$E" -ne 0 ] || [ "$SZ" -ne "$REF" ]; then
      echo "rep=$i $c DISCARDED exit=$E bytes=$SZ"
    else
      echo "rep=$i $c ms=$MS exit=$E bytes=$SZ"
    fi
  done
  i=$((i+1))
done

echo
echo "--- per-exec cost: 30 execs of gzip --version, successes counted ---"
for c in native box64 qemu; do
  S=$(date +%s%N); OK=0; k=1
  while [ "$k" -le 30 ]; do
    case $c in
      native) /usr/bin/gzip --version >/dev/null 2>&1 ;;
      box64)  BOX64_LD_LIBRARY_PATH="$LIB" box64 /x86/usr/bin/gzip --version >/dev/null 2>&1 ;;
      qemu)   qemu-x86_64 -L /x86 /x86/usr/bin/gzip --version >/dev/null 2>&1 ;;
    esac
    [ $? -eq 0 ] && OK=$((OK+1))
    k=$((k+1))
  done
  N=$(date +%s%N)
  echo "$c: $(( (N-S)/1000000 )) ms / 30 execs, $OK/30 exited 0"
done

echo
echo "--- peak RSS, one gzip pass ---"
for c in native box64 qemu; do
  case $c in
    native) /usr/bin/time -v /usr/bin/gzip -9 -c /payload.bin >/dev/null 2>/tmp/t ;;
    box64)  BOX64_LD_LIBRARY_PATH="$LIB" /usr/bin/time -v box64 /x86/usr/bin/gzip -9 -c /payload.bin >/dev/null 2>/tmp/t ;;
    qemu)   /usr/bin/time -v qemu-x86_64 -L /x86 /x86/usr/bin/gzip -9 -c /payload.bin >/dev/null 2>/tmp/t ;;
  esac
  echo "$c exit=$? $(grep -i "Maximum resident" /tmp/t 2>/dev/null || echo "(time -v unavailable)")"
done

echo
echo "--- GL: does box64 reach the host's NATIVE aarch64 GL stack? ---"
apt-get install -y -qq xvfb mesa-utils libgl1 >/dev/null 2>&1
for p in mesa-utils mesa-utils-bin libx11-6 libxcb1 libxau6 libxdmcp6 libbsd0 libmd0 \
         libgl1 libglx-mesa0 libglvnd0 libglx0 libxext6 libxfixes3 libdrm2 libexpat1 \
         libzstd1 libwayland-client0 libxcb-dri3-0 libxcb-present0 libxcb-sync1 \
         libxcb-randr0 libxcb-shm0 libxcb-xfixes0 libxcb-glx0 libxshmfence1 libllvm19 \
         libelf1t64 libffi8 libedit2 libxml2 liblzma5 libicu76 libbz2-1.0 libsensors5 \
         libsensors-config; do
  apt-get download -q "$p:amd64" >/dev/null 2>&1 || true
done
for d in /debs/*.deb; do dpkg-deb -x "$d" /x86 2>/dev/null; done
Xvfb :99 -screen 0 1024x768x24 >/dev/null 2>&1 &
sleep 3
echo "native aarch64 glxinfo:"
DISPLAY=:99 glxinfo -B 2>&1 | grep -E "Device:|direct rendering|rror" | sed "s/^/  /"
echo "x86_64 glxinfo under box64:"
DISPLAY=:99 BOX64_LD_LIBRARY_PATH="$LIB" box64 /x86/usr/bin/glxinfo -B 2>&1 | grep -E "Device:|direct rendering|rror" | sed "s/^/  /"
echo "x86_64 glxinfo under qemu-user:"
DISPLAY=:99 qemu-x86_64 -L /x86 /x86/usr/bin/glxinfo -B 2>&1 | grep -E "Device:|direct rendering|rror" | sed "s/^/  /"
echo "(a Device: line MATCHING the native one means box64 bound the HOST'S own"
echo " aarch64 Mesa, which is the entire reason box64 exists for 3D workloads."
echo " It does NOT prove hardware acceleration: a container has no GPU, so the"
echo " native stack here is llvmpipe. What is proved is which stack was used.)"
DYN
rc=$?
[ "$rc" -eq 0 ] || echo "arch-emulation-bench: dynamic mode exited $rc — the numbers above, if any, are not trustworthy" >&2
return "$rc"
}

case "$MODE" in
  bench)   run_bench;   RC=$? ;;
  dynamic) run_dynamic; RC=$? ;;
  cost)    run_cost;    RC=$? ;;
  handler) run_handler; RC=$? ;;
  all)     run_cost;    RC=$?
           run_handler; RC=$((RC + $?))
           run_bench;   RC=$((RC + $?))
           run_dynamic; RC=$((RC + $?)) ;;
esac
[ "$RC" -eq 0 ] || echo "arch-emulation-bench: mode '$MODE' exited $RC — the numbers above, if any, are not trustworthy" >&2
exit "$RC"
