#!/bin/sh
# Launch the OS full-screen in a browser on the local display.
#
# Browser search order matches findKioskBrowser in backend/cmd/init/main.go and
# is asserted to stay in sync by TestKioskBrowsersMatchInit. cog is preferred: it
# is a WebKit kiosk shell with no chrome to hide, and the rootfs installs it.
set -eu
URL="${VULOS_KIOSK_URL:-http://localhost:8080}"

# VULOS_DRM_ROOT overrides where connectors are read from. Default is the real
# sysfs; scripts/smoke-kiosk-multiscreen.sh points it at a fake tree so this
# script — the one the image runs — can be executed and its output checked,
# rather than only grepped.
#
# Screen identity (see roadmap/SCREENS.md, frontend/src/providers/screenIdentity.ts).
#
# One browser per output is the target shape. This is the SINGLE-OUTPUT step of
# it: name the output we are actually on and say so in the URL. The shell reads
# these three parameters and, because screens=1, renders exactly what it renders
# today — no chip, no peer explanations. Nothing a user sees changes.
#
# It is worth doing ahead of the multi-output launcher because it makes the seam
# LIVE. Until something sets these, readScreenIdentity() returns null on every
# real boot and the parser is reachable from no surface — tested, rendered in
# code, and never executed.
#
# The name comes from the DRM connector directory, which is card0-HDMI-A-1 and
# friends; strip the card prefix to get the name the compositor uses. If nothing
# is readable the parameters are omitted ENTIRELY rather than guessed: the parser
# refuses partial or inconsistent input, so a half-set identity would be
# discarded anyway, and omitting it keeps the single-screen path byte-identical
# to what it was before this existed.
screen_name=""
screen_list=""
screen_count=0
for st in "${VULOS_DRM_ROOT:-/sys/class/drm}"/*/status; do
  [ -r "$st" ] || continue
  [ "$(cat "$st" 2>/dev/null)" = "connected" ] || continue
  conn=$(basename "$(dirname "$st")")
  nm=$(echo "$conn" | sed 's/^card[0-9]*-//')
  screen_list="$screen_list $nm"
  screen_count=$((screen_count + 1))
  [ -n "$screen_name" ] || screen_name="$nm"
done
BASE_URL="$URL"
if [ -n "$screen_name" ]; then
  case "$URL" in
    *\?*) URL="$URL&screen=$screen_name&screens=1&screenIndex=1" ;;
    *)    URL="$URL?screen=$screen_name&screens=1&screenIndex=1" ;;
  esac
  # Report the REAL count, not "1 of 1". This line runs before the multi-output
  # branch below, so on a two-monitor box it used to claim one screen and then
  # start two browsers — a journal that contradicts what the machine does is
  # worse than no journal, and this is the only window into a box that is
  # showing you nothing.
  echo "vulos-kiosk: screen identity $screen_name ($screen_count connected:$screen_list)"
fi

# Say what we decided and why, ALWAYS.
#
# The first version of this gated the whole unit on
# ConditionPathExistsGlob=/dev/dri/card*, which meant a box that did not start
# the kiosk said NOTHING about it: systemd skips a unit whose condition fails
# without a message anyone sees on the console. Booting a real image, the screen
# looked identical whether the unit was missing, skipped, or crashed — and there
# was no way to tell which from outside the machine.
#
# So the detection lives here now, where it can be logged, and the unit runs
# unconditionally. A headless box exits 0 with a reason on the journal instead of
# being silently absent.
#
# VULOS_DRI_ROOT overrides where DRM render nodes are looked for, exactly as
# VULOS_DRM_ROOT does for connectors and for the same reason: without it the
# headless decision could only be GREPPED, never executed, on any host that has
# a /dev/dri of its own. TestKioskHeadlessExitsZero points both at empty
# directories and runs this file. A real boot sets neither and reads the real
# paths.
have_display=no
for node in "${VULOS_DRI_ROOT:-/dev/dri}"/card*; do
  [ -e "$node" ] && have_display=yes && break
done
if [ "$have_display" = no ]; then
  # A DRM node can be absent while a display is still attached on some kernels;
  # check the sysfs connector state before giving up.
  for st in "${VULOS_DRM_ROOT:-/sys/class/drm}"/*/status; do
    [ -e "$st" ] || continue
    if grep -qx connected "$st" 2>/dev/null; then have_display=yes; break; fi
  done
fi

if [ "$have_display" = no ]; then
  echo "vulos-kiosk: no display found (no /dev/dri/card* and no connected DRM connector) — not starting a browser" >&2
  exit 0
fi
echo "vulos-kiosk: display present; starting the OS in a browser at $URL" >&2

# Compositor environment. cage refuses to start without XDG_RUNTIME_DIR ("[ERROR]
# [../cage.c:299] XDG_RUNTIME_DIR is not set in the environment") and restarts
# forever, which is exactly what a first run of this unit did.
#
# The wlroots software-path variables mirror backend/cmd/init/main.go's startKiosk,
# which documents why each is required under QEMU virtio-gpu without working GL:
# legacy KMS modeset, no GBM modifier negotiation, and the pixman renderer.
# Without them cage starts, opens /dev/dri/card0 and silently paints nothing —
# no log, no exit. TestKioskEnvMatchesInit keeps the two lists in step.
mkdir -p /run/user/0 && chmod 700 /run/user/0
# These are DEFAULTS, not mandates: every one is ${VAR:-default}, so a real
# boot (which sets none of them) gets exactly the values it always did, while a
# harness can supply an environment suited to a headless compositor. Without
# that, scripts/smoke-kiosk-multiscreen.sh cannot start labwc at all, because
# LIBSEAT_BACKEND=builtin wants real DRM and there is none in a container.
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/0}"
# Somewhere writable for fontconfig. The image ships a pre-built cache (fc-cache
# runs at build time), but the root is a read-only dm-verity squashfs, so any
# cache write fontconfig still attempts has nowhere to go and it says so on
# every start. /run/user/0 is tmpfs.
mkdir -p /run/user/0/.cache
export XDG_CACHE_HOME=/run/user/0/.cache
export LIBSEAT_BACKEND="${LIBSEAT_BACKEND:-builtin}"
# ALWAYS, per backend/cmd/init/main.go's own note: this means "do not abort the
# compositor if libinput sees zero devices right now", NOT "disable input".
# Unset, cage's libinput backend aborts with "Unable to start the wlroots
# backend" whenever udev coldplug has not finished — including on machines whose
# keyboard is about to appear. Hotplugged devices still arrive normally.
export WLR_LIBINPUT_NO_DEVICES="${WLR_LIBINPUT_NO_DEVICES:-1}"

# Software rendering only when there is no real GPU. virtio_gpu is the QEMU case;
# a machine with a hardware driver keeps the GL renderer.
sw=yes
for d in /sys/class/drm/card*/device/driver; do
  [ -e "$d" ] || continue
  case "$(basename "$(readlink -f "$d")")" in
    virtio_gpu|"") ;;
    *) sw=no ;;
  esac
done
if [ "$sw" = yes ]; then
  export WLR_RENDERER="${WLR_RENDERER:-pixman}"
  export WLR_RENDERER_ALLOW_SOFTWARE="${WLR_RENDERER_ALLOW_SOFTWARE:-1}"
  export WLR_DRM_NO_ATOMIC="${WLR_DRM_NO_ATOMIC:-1}"
  export WLR_DRM_NO_MODIFIERS="${WLR_DRM_NO_MODIFIERS:-1}"
  export DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-unix:path=/dev/null}"
  echo "vulos-kiosk: software rendering path (pixman, legacy KMS, no modifiers)" >&2
fi

# Wait for the server to actually serve. Starting the browser first shows an
# error page that never retries, which looks exactly like a broken box.
i=0
while [ "$i" -lt 60 ]; do
  # BASE_URL, not URL. $URL carries the screen identity query string by this
  # point, so "$URL/api/setup/status" puts the path AFTER the query and probes
  # a nonsense address that can never answer. The effect was that every box
  # with a display waited the full sixty seconds and then launched anyway —
  # defeating exactly the protection this loop exists to provide, and adding a
  # minute to every boot. Found by tracing the real script under sh -x.
  if curl -fsS --max-time 2 "$BASE_URL/api/setup/status" >/dev/null 2>&1; then break; fi
  i=$((i + 1))
  sleep 1
done

# ── Multi-output: one browser per screen, placed by labwc ────────────────────
#
# Only when MORE THAN ONE output is connected. A single-screen box takes the
# path below, unchanged, because that is what every real boot uses today and a
# regression there is a box that shows nothing.
#
# labwc rather than cage: cage is deliberately one-window-one-output. labwc
# places windows with a windowRule carrying a MoveToOutput action, and a rule
# can only name a window it can tell apart — so each instance is identified by
# its TITLE, which the shell sets from the screen= parameter it was given
# (frontend/src/providers/screenIdentity.ts, screenWindowTitle). The connector
# name is the same string on both sides: read from /sys/class/drm here, passed
# in the URL, echoed back in the title, matched by the rule.
#
# -S runs the session and terminates the compositor when it exits, so the
# systemd unit sees the kiosk end rather than a compositor lingering with no
# browsers.
if [ "$screen_count" -gt 1 ] && command -v labwc >/dev/null 2>&1 && command -v cog >/dev/null 2>&1; then
  cfg=/run/vulos-kiosk
  # Generated by the installed script, which is scripts/vulos-kiosk-genconfig.sh
  # in the repo — one source, exercised directly by TestKioskGenConfig rather
  # than re-implemented here where a test could only grep at it.
  vulos-kiosk-genconfig "$cfg" "$BASE_URL" $screen_list || exit 1

  echo "vulos-kiosk: $screen_count screens ($screen_list ) — labwc, one browser per output"
  exec labwc -C "$cfg" -S "$cfg/session.sh"
fi

for cand in cog chromium chromium-browser; do
  if command -v "$cand" >/dev/null 2>&1; then
    case "$cand" in
      cog) exec cage -- "$cand" "$URL" ;;
      *)   exec cage -- "$cand" --kiosk --ozone-platform=wayland --no-sandbox \
             --no-first-run --no-default-browser-check --noerrdialogs \
             --disable-translate --disable-features=Translate "$URL" ;;
    esac
  fi
done

# No browser: say so ON THE SCREEN rather than exiting quietly into a blank
# display, which is indistinguishable from a hung boot.
echo "vulos-kiosk: no kiosk browser found (looked for: cog chromium chromium-browser)" >&2
exit 1