#!/usr/bin/env bash
# smoke-multiscreen.sh — does MoveToOutput actually put one window per screen?
#
# The multi-output kiosk stakes everything on a labwc windowRule matching a
# window's TITLE and moving it to a named output. Every part of that can be
# correct-looking and still fail silently: a rule that matches nothing places
# nothing, and the symptom is every browser stacked on one monitor with no log
# line saying why.
#
# This runs a real labwc against two headless wlroots outputs, launches two
# windows with the titles the shell actually sets, and photographs each output.
#
# THE CONTROL IS THE POINT. Two windows landing on two screens proves nothing on
# its own — a compositor might distribute them by default, in which case the
# rules are decoration. So the same scenario runs TWICE, once with the rules and
# once with an empty <windowRules>, and the test is the DIFFERENCE:
#
#   with rules:    both outputs hold one window each
#   without rules: both windows pile onto one output, the other is empty
#
# Measured 2026-08-13 on labwc 0.8.3: with rules 9967 and 10454 bytes; without,
# 2759 and 14685 — the small one being an empty screen. That is the signal.
#
# Uses foot rather than cog because it is tiny and takes --title; the mechanism
# under test is labwc's, not the browser's. Outputs are HEADLESS-1/2 rather than
# DRM connector names for the same reason.
set -euo pipefail

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/with" "$WORK/without" "$WORK/shots-with" "$WORK/shots-without"

rules() {
    cat <<'XML'
    <windowRule title="Vulos — HEADLESS-1" matchOnce="yes">
      <action name="MoveToOutput" output="HEADLESS-1" />
    </windowRule>
    <windowRule title="Vulos — HEADLESS-2" matchOnce="yes">
      <action name="MoveToOutput" output="HEADLESS-2" />
    </windowRule>
XML
}

write_cfg() { # $1=dir $2=with|without
    {
        echo '<?xml version="1.0"?>'
        echo '<labwc_config>'
        echo '  <windowRules>'
        [ "$2" = "with" ] && rules
        echo '  </windowRules>'
        echo '</labwc_config>'
    } > "$1/rc.xml"
}

write_cfg "$WORK/with" with
write_cfg "$WORK/without" without

cat > "$WORK/session.sh" <<'SESS'
#!/bin/sh
foot --title "Vulos — HEADLESS-1" -- sh -c 'while :; do sleep 1; done' &
foot --title "Vulos — HEADLESS-2" -- sh -c 'while :; do sleep 1; done' &
sleep 6
for o in HEADLESS-1 HEADLESS-2; do grim -o "$o" "$SHOTS/$o.png" || true; done
sleep 1
SESS
chmod +x "$WORK/session.sh"

cat > "$WORK/run.sh" <<'RUN'
#!/bin/sh
set -e
export XDG_RUNTIME_DIR=/tmp/xdg; mkdir -p "$XDG_RUNTIME_DIR"; chmod 700 "$XDG_RUNTIME_DIR"
export WLR_BACKENDS=headless WLR_HEADLESS_OUTPUTS=2 WLR_RENDERER=pixman
for variant in with without; do
    export SHOTS="/work/shots-$variant"
    timeout 40 labwc -C "/work/$variant" -S /work/session.sh >/dev/null 2>&1 || true
done
RUN
chmod +x "$WORK/run.sh"

echo "▸ running labwc twice (with rules, and without) on two headless outputs…"
docker run --rm --platform linux/amd64 -v "$WORK:/work" debian:trixie sh -c '
  apt-get update -qq >/dev/null 2>&1
  apt-get install -y -qq labwc foot grim >/dev/null 2>&1
  sh /work/run.sh
'

size() { wc -c < "$1" | tr -d ' '; }
fail=0
for v in with without; do
    for o in HEADLESS-1 HEADLESS-2; do
        [ -f "$WORK/shots-$v/$o.png" ] || { echo "FAIL: no capture for $v/$o"; fail=1; }
    done
done
[ "$fail" = 0 ] || exit 1

w1=$(size "$WORK/shots-with/HEADLESS-1.png");    w2=$(size "$WORK/shots-with/HEADLESS-2.png")
n1=$(size "$WORK/shots-without/HEADLESS-1.png"); n2=$(size "$WORK/shots-without/HEADLESS-2.png")
echo "  with rules:    HEADLESS-1=$w1  HEADLESS-2=$w2"
echo "  without rules: HEADLESS-1=$n1  HEADLESS-2=$n2"

# With rules: neither screen is empty. An empty headless screen compresses to a
# few KB; a screen holding a window does not.
small() { [ "$1" -lt 5000 ]; }
if small "$w1" || small "$w2"; then
    echo "FAIL: with rules, a screen is empty — both windows landed on one output."
    echo "      The windowRule/MoveToOutput placement is not working."
    exit 1
fi

# Without rules: one screen MUST be empty. If both are populated here, labwc is
# distributing windows on its own and this whole test proves nothing about the
# rules — which is worth failing on, loudly, rather than reporting a pass that
# rests on the compositor's default behaviour.
if ! small "$n1" && ! small "$n2"; then
    echo "FAIL: without rules both screens hold a window, so labwc distributes them"
    echo "      by default and this test cannot attribute placement to the rules."
    exit 1
fi

echo "PASS: rules place one window per output; without them both pile onto one."
