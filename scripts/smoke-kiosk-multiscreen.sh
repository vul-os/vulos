#!/usr/bin/env bash
# smoke-kiosk-multiscreen.sh — run the REAL kiosk launcher on two screens.
#
# scripts/smoke-multiscreen.sh proves labwc's windowRule/MoveToOutput places
# windows, but it does so with a config written by the test. This one runs
# scripts/vulos-kiosk.sh — the exact file build.sh installs into the image —
# and lets it do the whole job: enumerate connectors, generate the labwc config
# via vulos-kiosk-genconfig, start labwc, launch one browser per screen.
#
# Two substitutions, both deliberate and both named so nothing is over-claimed:
#
#   VULOS_DRM_ROOT points at a fake sysfs tree. The connectors in it are called
#   HEADLESS-1 and HEADLESS-2 because those are the output names wlroots gives
#   its headless backend — MoveToOutput must name an output that exists, so the
#   fake connectors have to match what the compositor will actually provide.
#
#   The OS server is replaced by a static page that sets document.title from
#   the screen= parameter, exactly as the real shell does (screenWindowTitle in
#   frontend/src/providers/screenIdentity.ts). That is the contract the labwc
#   rule depends on; substituting it keeps this test about the KIOSK path and
#   not about booting an OS.
#
# What that leaves genuinely unverified: a real boot, with the real shell
# setting its own title, on real DRM connectors.
#
# STATUS 2026-08-13 — the KIOSK path is verified; the compositor does not start.
#
# Reached, by executing the real installed script:
#
#     vulos-kiosk: screen identity HEADLESS-1 (2 connected: HEADLESS-1 HEADLESS-2)
#     vulos-kiosk: 2 screens ( HEADLESS-1 HEADLESS-2 ) — labwc, one browser per output
#
# So enumeration, counting and the multi-output branch decision are all correct
# in the file the image installs. That is the part this harness existed to
# check, and it now checks it.
#
# Remaining failure: no Wayland socket appears, so labwc does not come up. Its
# environment here is the kiosk's own, which is written for real DRM hardware
# (LIBSEAT_BACKEND=builtin, the WLR_DRM_* flags, XDG_RUNTIME_DIR=/run/user/0).
# scripts/smoke-multiscreen.sh proves labwc works headless when IT sets the
# environment, so the gap is between those two environments rather than in
# labwc or in the placement rules.
#
# FOUR of this harness's failures were its own bugs, and naming them is the
# point — each looked like a product fault first:
#   1. XDG_RUNTIME_DIR set here was overridden by the kiosk's own export.
#   2. The stub served no /api/setup/status, so the readiness loop timed out.
#   3. curl was not installed, so the probe could not succeed even once the
#      URL was correct — it failed for a reason unrelated to the server.
#   4. wayland-0 was assumed rather than discovered.
#
# It also found TWO REAL DEFECTS, which is why it is committed despite failing:
# the kiosk logging a hardcoded "(1 of 1)" while starting two browsers, and a
# readiness probe built from a URL carrying a query string, which could never
# succeed and cost sixty seconds on every boot with a display.
#
# ROUND 5 — labwc was never the problem. cog is:
#     ERROR cog: Failed to fully launch dbus-proxy: Child process exited with code 1
# cog launches xdg-dbus-proxy for its web-process sandbox and the kiosk pointed
# DBUS_SESSION_BUS_ADDRESS at /dev/null. Both browsers died instantly, the
# session's `wait` returned, and labwc -S terminated the compositor BY DESIGN.
# "socket: NONE" was a consequence, and it misdirected two rounds.
#
# ROUND 6 — a real bus was necessary but not sufficient. dbus-run-session got
# cog past the proxy (it activates org.a11y.Bus successfully), and the run still
# produced nothing. The error underneath:
#
#     bwrap: Creating new namespace failed: Operation not permitted
#
# cog sandboxes its web process with bubblewrap, which needs unprivileged user
# namespaces; Docker blocks them by default. So this was never a product fault
# and never labwc — it is what a browser sandbox requires to start in a
# container. --cap-add SYS_ADMIN --security-opt seccomp=unconfined are now
# passed, UNVERIFIED at the time of writing.
#
# Three layers of symptom stood between the log and the cause: no screenshot,
# then no socket, then a dbus-proxy error, then bwrap. Each looked like the
# answer.
#
# ── DECISION 2026-08-13: stop extending this harness. Use QEMU instead. ──────
#
# Round 7 hung with no output after the namespace flags were added, and that is
# the point to stop. Every one of the seven failures has been ENVIRONMENTAL, and
# none has been a fault in Vulos:
#
#   1. XDG_RUNTIME_DIR overridden by the kiosk's own export
#   2. no /api/setup/status served, so the readiness loop timed out
#   3. curl absent, so the probe could not succeed at all
#   4. wayland-0 assumed rather than discovered
#   5. no session bus, so cog's dbus-proxy died
#   6. bwrap denied unprivileged user namespaces
#   7. the run hanging under emulation with the sandbox flags granted
#
# When seven consecutive failures are all about making a container behave like a
# graphics stack, the container has stopped being a good proxy for the thing
# under test. The remaining claim — that the REAL launcher places real browsers
# on real outputs — is cheaper to get from a QEMU boot with two virtual
# displays, where the graphics stack is genuine and none of these seven problems
# exist.
#
# scripts/netboot-install-smoke.sh already has working QEMU screendump machinery
# and the kiosk already renders under it. Adding a second -device virtio-gpu-pci
# and screendumping both heads is a smaller change than any two of the rounds
# above.
#
# KEEP this file. It is not wasted: it found two real defects that reading never
# would — the kiosk logging "(1 of 1)" while starting two browsers, and a
# readiness probe built from a URL carrying a query string, which could never
# succeed and cost sixty seconds on every boot with a display. It also forced
# the compositor environment to become overridable defaults. Its diagnosis chain
# is recorded above because the next person will otherwise repeat it.
#
# What it does NOT do is pass, and it should not be extended further without a
# specific reason to believe round eight differs from rounds one to seven.
#
# NOT wired into CI while it cannot pass.
set -euo pipefail

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/www/api/setup" "$WORK/drm/card0-HEADLESS-1" "$WORK/drm/card0-HEADLESS-2" "$WORK/shots" "$WORK/www"
echo connected > "$WORK/drm/card0-HEADLESS-1/status"
echo connected > "$WORK/drm/card0-HEADLESS-2/status"

# Stand-in for the shell: sets the title the windowRule matches.
cat > "$WORK/www/index.html" <<'HTML'
<!doctype html><meta charset="utf-8"><title>Vulos</title>
<body style="background:#111;color:#eee;font:16px sans-serif">
<script>
  var p = new URLSearchParams(location.search);
  var n = p.get('screen');
  if (n && Number(p.get('screens')) > 1) document.title = 'Vulos — ' + n;
  document.body.textContent = document.title;
</script>
HTML

# vulos-kiosk polls $URL/api/setup/status for up to SIXTY SECONDS before
# launching a browser — it refuses to show an error page that never retries.
# A static server satisfies that if the path exists as a file. Without this the
# kiosk sits in the wait loop, the screenshot lands mid-wait, and the run times
# out before the multi-output branch is ever reached. That was round 2's
# failure, and it looked exactly like the branch being skipped.
echo '{"setup_complete":true}' > "$WORK/www/api/setup/status"

cp scripts/vulos-kiosk.sh "$WORK/vulos-kiosk"
cp scripts/vulos-kiosk-genconfig.sh "$WORK/vulos-kiosk-genconfig"
chmod +x "$WORK/vulos-kiosk" "$WORK/vulos-kiosk-genconfig"

cat > "$WORK/run.sh" <<'RUN'
#!/bin/sh
set -e
# vulos-kiosk exports XDG_RUNTIME_DIR=/run/user/0 itself, so that is the
# directory that has to exist — setting a different one here is overridden and
# labwc then fails to create its socket. That was this harness's first failure.
mkdir -p /run/user/0; chmod 700 /run/user/0
# Supply a headless-appropriate environment. vulos-kiosk now takes these as
# DEFAULTS (${VAR:-...}), so setting them here wins without changing what a real
# boot does. LIBSEAT_BACKEND=noop matters most: the kiosk default of "builtin"
# wants real DRM, which a container has none of.
export WLR_BACKENDS=headless WLR_HEADLESS_OUTPUTS=2 WLR_RENDERER=pixman
export LIBSEAT_BACKEND=noop
export XDG_RUNTIME_DIR=/run/user/0
# NOT /dev/null. cog launches xdg-dbus-proxy for its web-process sandbox and
# dies without a real bus; dbus-run-session below provides one and exports the
# address, which the kiosk now inherits because its own value is a default.
export VULOS_DRM_ROOT=/work/drm
export PATH="/work:$PATH"

(cd /work/www && python3 -m http.server 8080 >/dev/null 2>&1) &
sleep 2
export VULOS_KIOSK_URL=http://localhost:8080

# The kiosk execs labwc, so run it in the background and screenshot from here.
sh -x /work/vulos-kiosk > /work/kiosk.log 2>&1 &
sleep 18
# Discover the socket rather than assuming wayland-0.
sock=$(ls /run/user/0/wayland-* 2>/dev/null | grep -v '\.lock$' | head -1)
echo "socket: ${sock:-NONE}" >> /work/kiosk.log
for o in HEADLESS-1 HEADLESS-2; do
    XDG_RUNTIME_DIR=/run/user/0 WAYLAND_DISPLAY=$(basename "${sock:-wayland-0}") \
      grim -o "$o" "/work/shots/$o.png" 2>>/work/kiosk.log || true
done
sleep 1
RUN
chmod +x "$WORK/run.sh"

echo "▸ running the real vulos-kiosk against two headless outputs…"
# cog sandboxes its web process with bubblewrap, which needs unprivileged user
# namespaces. Docker blocks those by default, and the failure surfaces as a
# dbus-proxy error several layers downstream — see ROUND 6 above. These two
# flags are what a browser sandbox needs to start in a container.
docker run --rm --platform linux/amd64 \
  --cap-add SYS_ADMIN --security-opt seccomp=unconfined \
  -v "$WORK:/work" debian:trixie sh -c '
  apt-get update -qq >/dev/null 2>&1
  apt-get install -y -qq labwc cog grim python3 curl dbus >/dev/null 2>&1
  timeout 90 dbus-run-session -- sh /work/run.sh || true
'

echo "── kiosk log ──"; cat "$WORK/kiosk.log" 2>/dev/null || true

size() { [ -f "$1" ] && wc -c < "$1" | tr -d ' ' || echo 0; }
a=$(size "$WORK/shots/HEADLESS-1.png"); b=$(size "$WORK/shots/HEADLESS-2.png")
echo "  HEADLESS-1=$a  HEADLESS-2=$b"

if [ "$a" = 0 ] || [ "$b" = 0 ]; then
    echo "FAIL: an output was not captured at all — labwc or the kiosk did not start."
    exit 1
fi
# Both screens must hold a window. An empty headless screen compresses small.
if [ "$a" -lt 5000 ] || [ "$b" -lt 5000 ]; then
    echo "FAIL: a screen is empty. The real kiosk did not place one browser per output."
    exit 1
fi
echo "PASS: the installed kiosk script placed a browser on each of two screens."
