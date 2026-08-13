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
# STATUS 2026-08-13 — narrowing, does NOT yet pass. Three rounds:
#
# R1  labwc had no socket directory. MY bug: vulos-kiosk exports
#     XDG_RUNTIME_DIR=/run/user/0 itself, overriding what this set. Fixed.
#     R1 also found a REAL defect — the kiosk logged a hardcoded "(1 of 1)" and
#     then started two browsers. Fixed, and confirmed here in R2.
#
# R2  Enumeration and counting correct:
#       vulos-kiosk: screen identity HEADLESS-1 (2 connected: HEADLESS-1 HEADLESS-2)
#     but the run still takes the SINGLE-output path and no socket appears.
#
# R3  Two candidate causes ELIMINATED by measurement, not argument:
#       - the readiness wait. vulos-kiosk polls $URL/api/setup/status for up to
#         SIXTY seconds before launching, and this harness served no such path,
#         so the run was timing out mid-wait. Now served as a static file. The
#         behaviour did not change, so that was not it.
#       - a restricted PATH inside the kiosk. There is none; it never touches
#         PATH, and labwc/cog/grim/foot are all confirmed present in the image.
#
# So screen_count is 2, labwc and cog are both on PATH, and the branch guarded
# by exactly those three conditions is still not taken. That is contradictory
# on its face, which is the point at which guessing gets expensive.
#
# NEXT, and deliberately a measurement rather than a theory: run the kiosk under
# `sh -x` and read the trace. It will show the branch evaluation directly —
# which condition was false, and what the variables held at that moment —
# instead of inferring it from which messages did and did not print. This
# investigation has already cost a day once by reasoning ahead of the evidence.
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
export WLR_BACKENDS=headless WLR_HEADLESS_OUTPUTS=2 WLR_RENDERER=pixman
export VULOS_DRM_ROOT=/work/drm
export PATH="/work:$PATH"

(cd /work/www && python3 -m http.server 8080 >/dev/null 2>&1) &
sleep 2
export VULOS_KIOSK_URL=http://localhost:8080

# The kiosk execs labwc, so run it in the background and screenshot from here.
/work/vulos-kiosk > /work/kiosk.log 2>&1 &
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
docker run --rm --platform linux/amd64 -v "$WORK:/work" debian:trixie sh -c '
  apt-get update -qq >/dev/null 2>&1
  apt-get install -y -qq labwc cog grim python3 >/dev/null 2>&1
  timeout 60 sh /work/run.sh || true
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
