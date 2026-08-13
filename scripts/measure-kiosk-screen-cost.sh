#!/usr/bin/env bash
# measure-kiosk-screen-cost.sh — marginal memory cost of one kiosk browser per screen.
#
# roadmap/SCREENS.md item 4 ("Cost") asks whether N monitors is supportable on
# the 2GB floor this project advertises. That is a question about MARGINAL
# memory per additional screen, which is not the same number as one instance's
# own footprint — cog/WPE WebKit processes share a great deal (the library
# text pages, fonts, the WebKit .so itself) across instances of the same
# binary. A naive sum of each process's RSS overstates the marginal cost,
# sometimes severely, because it double-counts that shared memory once per
# process that maps it.
#
# This script launches 1, then 2, then 3 independent cog instances (each under
# its own headless cage compositor — see "What this measures" below) against a
# server serving the REAL frontend build (frontend/dist), and after each
# addition takes two whole-system snapshots:
#
#   RSS  — sum of Rss over every process's /proc/<pid>/smaps_rollup. This is
#          the "naive" number and is expected to overstate shared cost.
#   PSS  — sum of Pss over the same processes. PSS divides each shared page's
#          cost by the number of processes mapping it, so summing PSS across
#          processes does NOT double-count shared memory the way RSS does.
#          This is the number the marginal-cost decision should use.
#
# It also counts every process cog spawns (WPEWebProcess, WPENetworkProcess,
# bwrap, dbus-run-session, xdg-dbus-proxy, cage, cog itself) rather than just
# the one process this script started — a per-PID reading of the parent alone
# undercounts badly, per roadmap/SCREENS.md's own instruction.
#
# What this measures, and what it does NOT:
#
#  - Runs inside a privileged Debian trixie container on Docker/OrbStack,
#    linux/arm64, on a Mac. That is virtualized Linux on Apple Silicon, not the
#    bare-metal x86_64/arm64 2GB box this project ships images for. Container
#    memory accounting (smaps_rollup) is the real kernel's, so the NUMBERS are
#    real — but the underlying CPU/memory subsystem is not bare metal, and nothing
#    here was run on amd64.
#  - Uses WLR_RENDERER=pixman (software rendering), matching the software path
#    vulos-kiosk.sh takes on any GPU-less or virtio_gpu box (see its "sw=yes"
#    branch). A hardware GL path has a different memory profile — VRAM-backed
#    buffers rather than a system-RAM framebuffer per screen — and is NOT
#    measured here. This script's numbers are therefore representative of the
#    software-rendering fleet (every box without a working GPU driver, which
#    per vulos-kiosk.sh's own logic is the common case this project targets),
#    not of a hardware-accelerated box.
#  - Simulates N screens as N independent single-window `cage` compositors,
#    each running one `cog` instance, rather than one `labwc` compositor
#    hosting N cog windows the way the real multi-output branch of
#    vulos-kiosk.sh does for screen_count > 1. scripts/smoke-kiosk-multiscreen.sh
#    already spent seven rounds establishing that labwc does not reliably come
#    up in this exact kind of container (its own "DECISION 2026-08-13: stop
#    extending this harness" applies here too), and this script does not
#    reopen that. The substitution is judged small: cage and labwc are
#    themselves tiny (76KB / 713KB installed, per roadmap/BAREMETAL-INIT.md)
#    next to a WebKit process tree, so which compositor supervises each cog
#    instance should not materially change cog's own memory. What it does NOT
#    establish is the compositor's own marginal cost under labwc specifically,
#    or whether labwc itself scales differently with output count.
#
# Usage: scripts/measure-kiosk-screen-cost.sh <image-with-cog-labwc-installed>
#
# The image must already have cog, cage, python3, dbus, procps installed
# (build one with `docker commit` first — installs are slow and OrbStack's
# btrfs store is fragile under repeated installs, so this script does not
# apt-get anything). `cage` is easy to forget: it is a separate package from
# `labwc` and cog does not depend on it, but the single-screen path this
# script simulates needs it. This script's own first run against a
# cog-only image failed with "invalid option -- '-'" (a `--platform=wl` flag
# meant for cog, misplaced on cage's own command line) and then with
# "No such file or directory" until `cage` was installed explicitly.
set -euo pipefail

IMAGE="${1:?usage: $0 <docker-image-with-cog-installed>}"
FRONTEND_DIST="$(cd "$(dirname "$0")/.." && pwd)/frontend/dist"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

if [ ! -f "$FRONTEND_DIST/index.html" ]; then
  echo "measure-kiosk-screen-cost: $FRONTEND_DIST/index.html not found — run (cd frontend && npm run build) first" >&2
  exit 2
fi

CNAME="screencost-run-$$"
docker rm -f "$CNAME" >/dev/null 2>&1 || true
docker run -d --name "$CNAME" --privileged \
  -v "$FRONTEND_DIST:/work/www:ro" \
  "$IMAGE" sleep infinity >/dev/null

cleanup() { docker rm -f "$CNAME" >/dev/null 2>&1 || true; }
trap 'cleanup; rm -rf "$WORK"' EXIT

# Serve the REAL built frontend (frontend/dist) read-only. This script drives
# cog directly rather than through vulos-kiosk.sh, so its 60s
# /api/setup/status readiness poll never runs and does not need a stub here.
docker exec "$CNAME" sh -c '
  cd /work/www && nohup python3 -m http.server 8080 >/tmp/http.log 2>&1 &
  sleep 1
'

BASE_URL="http://127.0.0.1:8080/"

pss_and_rss_kb() {
  # Sum Pss and Rss (KiB) across every PID whose /proc/<pid>/cmdline or
  # comm matches a cog-related process name. Matching by NAME (not by parent
  # PID tree) deliberately catches everything cog spawns even if bwrap
  # reparents children to PID 1, which it does.
  docker exec "$CNAME" sh -c '
    rss_total=0
    pss_total=0
    n=0
    for pid in /proc/[0-9]*; do
      p=${pid#/proc/}
      comm=$(cat "$pid/comm" 2>/dev/null) || continue
      # /proc/*/comm is truncated to 15 chars + NUL (TASK_COMM_LEN=16), so
      # "WPENetworkProcess" (17 chars) reads back as "WPENetworkProce". An
      # exact-match case here silently dropped every WPENetworkProcess and
      # undercounted each instance by its whole network-process PSS (~33MB in
      # the first manual check that caught this) — verified by comparing
      # against `ps` output before trusting the totals below.
      case "$comm" in
        cog|cage|WPEWebProcess|WPENetworkProce*|bwrap|dbus-run-session|xdg-dbus-proxy|dbus-daemon)
          ;;
        *) continue ;;
      esac
      roll="$pid/smaps_rollup"
      [ -r "$roll" ] || continue
      rss=$(awk "/^Rss:/{print \$2}" "$roll" 2>/dev/null)
      pss=$(awk "/^Pss:/{print \$2}" "$roll" 2>/dev/null)
      rss=${rss:-0}; pss=${pss:-0}
      rss_total=$((rss_total + rss))
      pss_total=$((pss_total + pss))
      n=$((n + 1))
    done
    echo "procs=$n rss_kb=$rss_total pss_kb=$pss_total"
  '
}

start_instance() {
  n=$1
  docker exec -d "$CNAME" sh -c "
    mkdir -p /run/user/0/s$n && chmod 700 /run/user/0/s$n
    export XDG_RUNTIME_DIR=/run/user/0/s$n
    export WLR_BACKENDS=headless WLR_RENDERER=pixman WLR_RENDERER_ALLOW_SOFTWARE=1
    export LIBSEAT_BACKEND=noop
    export WLR_LIBINPUT_NO_DEVICES=1
    dbus-run-session -- cage -- cog --platform=wl '$BASE_URL' > /tmp/cog-$n.log 2>&1
  "
}

echo "screens,procs,rss_kb,pss_kb,rss_mb,pss_mb"
for n in 1 2 3; do
  start_instance "$n"
  # Give WPEWebProcess/WPENetworkProcess time to fork and the page to load.
  sleep 12
  read -r line < <(pss_and_rss_kb)
  # shellcheck disable=SC2086
  eval "$line"
  rss_mb=$((rss_kb / 1024))
  pss_mb=$((pss_kb / 1024))
  echo "$n,$procs,$rss_kb,$pss_kb,$rss_mb,$pss_mb"
done

# Settle check: WPEWebProcess is still visibly hot in `ps` (30-60% CPU) at the
# 12s mark in manual testing, which means a 12s-only reading risks catching
# JIT warmup / first-paint layout rather than a steady state. Re-measure after
# a further wait so the printed numbers can be compared against themselves —
# if they moved a lot, the 12s figures above understate the real cost.
sleep 15
read -r line < <(pss_and_rss_kb)
eval "$line"
echo "settled(+15s),$procs,$rss_kb,$pss_kb,$((rss_kb / 1024)),$((pss_kb / 1024))"

echo "── process list at n=3 (for the process-count claim) ──"
docker exec "$CNAME" sh -c "ps -eo pid,ppid,comm | grep -E 'cog|cage|WPE|bwrap|dbus'"
