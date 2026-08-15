#!/usr/bin/env bash
# smoke-multiscreen.sh — SCREENS-01. Does the SHIPPING kiosk put one browser on
# each output, and does the right browser land on the right one?
#
# ── Why this file was rewritten on 2026-08-15, and what it used to be ────────
#
# The previous version of this gate ran labwc on two headless outputs, launched
# two `foot` terminals with `--title "Vulos — HEADLESS-N"`, and asserted one
# window per output. It had a real control — without the rules both windows
# piled onto one output — and it passed continuously for weeks while the
# multi-output kiosk had NEVER worked on any boot.
#
# It was green because of one sentence in its own header: "Uses foot rather
# than cog because it is tiny and takes --title; the mechanism is the same."
# The mechanism was not the same in the one respect that decided everything.
# foot sets its toplevel title; cog never does — cog-platform-wl.c contains a
# single xdg_toplevel_set_title() call, at window creation, with the literal
# "Cog". The gate was therefore SUPPLYING the very attribute whose absence was
# the bug. It examined the wrong thing, convincingly, with a real control.
#
# The rule that came out of that, and which this file exists to obey: a test
# double must be verified to SHARE THE PROPERTY UNDER TEST, not merely to be
# convenient. So there is no double here. This drives cog — the client that
# ships — through scripts/vulos-kiosk-genconfig.sh — the generator that ships,
# copied in rather than reimplemented — with the argv scripts/vulos-kiosk.sh
# execs, including the `/bin/sh` interpreter that works around /run being
# noexec on a real box.
#
# ── The two claims, asserted separately ──────────────────────────────────────
#
# Placement rests on two independent contracts, and the old gate tested only
# the second while assuming the first:
#
#   A  THE CLIENT'S. cog, launched with --gapplication-app-id=X, puts X on its
#      xdg_toplevel as the app_id — and puts nothing else there that a rule
#      might match by accident.
#   B  THE COMPOSITOR'S. labwc, given <windowRule identifier="X"> with a
#      MoveToOutput action, moves that window to that output.
#
# The `wire` arm asserts A by reading the WAYLAND PROTOCOL ITSELF
# (WAYLAND_DEBUG=1), not by inference. It requires every app id the generator
# wrote into rc.xml to appear in a set_app_id() on the wire, and it requires
# that the ONLY title cog ever sends is "Cog". That second assertion is the
# regression test for the original defect: if a future cog started forwarding
# the document title, the reasoning in vulos-kiosk-genconfig.sh would be stale
# and this gate says so instead of quietly still passing.
#
# The `normal` arm asserts B, and more: each screen's page is a DIFFERENT SOLID
# COLOUR, derived by the stub server from the ?screen= parameter that the
# generated URL carries. So the gate does not merely count "one window per
# output" — it checks WHICH window landed WHERE. A swapped mapping fails here.
# roadmap/SCREENS-QEMU.md records that SCREENS-02 explicitly cannot see a swap;
# this is the gate that can.
#
# ── THE CONTROLS ARE THE POINT ───────────────────────────────────────────────
#
# Two windows landing on two screens proves nothing on its own — a compositor
# might distribute them by default, in which case the rules are decoration. So
# the same scenario runs three more times and the test is the DIFFERENCE:
#
#   normal        distinct app ids, real rules   → S1 on HEADLESS-1, S2 on HEADLESS-2
#   same-app-id   VULOS_KIOSK_FORCE_APP_ID       → one output blank, both browsers on the other
#   no-rules      <windowRule> elements stripped → one output blank, both browsers on the other
#
# `same-app-id` drives the SHIPPING seam — the one scripts/smoke-multiscreen-qemu.sh
# uses for its control on a real boot — so the two gates fail the same way for
# the same reason. `no-rules` is the older control, kept because it answers a
# different question: it is what proves labwc does not distribute windows on
# its own. If it ever stops going red, the `normal` arm's pass can no longer be
# attributed to the rules, and this script fails loudly rather than reporting a
# pass that rests on a compositor default.
#
# A control that fails for the WRONG reason is not a control, so each control
# arm must still have loaded both pages (the stub server counts the fetches) —
# a browser that never started would satisfy "one output is blank" while
# meaning nothing.
#
# Measured 2026-08-15, labwc 0.8.3 / cog 0.18.4 / WPE WebKit on debian trixie:
#
#   arm           HEADLESS-1        HEADLESS-2
#   normal        #ff0000  (S1)     #0000ff  (S2)
#   same-app-id   #000000  blank    #ff0000  (S1)
#   no-rules      #000000  blank    #0000ff  (S2)
#
# ── Showing this gate red on demand ──────────────────────────────────────────
#
#   --break same-app-id   collapse the normal arm's app ids  → placement fails
#   --break rules         strip the normal arm's windowRules → placement fails
#   --break title         match on title="Vulos — HEADLESS-N" instead of on the
#                         app id — i.e. REBUILD THE ORIGINAL DEFECT. cog's only
#                         title is "Cog", so nothing matches and both browsers
#                         pile onto one output. This is the arm that says, in
#                         one command, that the gate would have caught the bug
#                         that shipped.
#
# ── What this does NOT cover ─────────────────────────────────────────────────
#
# Headless wlroots outputs, not DRM connectors. Software rendering (pixman), no
# GPU, no physical monitor. A stub page, not the Vulos shell. Nothing here
# boots an OS image — scripts/smoke-multiscreen-qemu.sh (SCREENS-02) is the
# gate that does, and neither of them has met two real displays.
set -euo pipefail

BREAK=""
while [ $# -gt 0 ]; do
    case "$1" in
        --break) BREAK="${2:-}"; shift 2 ;;
        *) echo "usage: $0 [--break same-app-id|rules|title]" >&2; exit 2 ;;
    esac
done
case "$BREAK" in
    ''|same-app-id|rules|title) ;;
    *) echo "$0: unknown --break '$BREAK'" >&2; exit 2 ;;
esac

REPO=$(cd "$(dirname "$0")/.." && pwd)
IMAGE=${VULOS_SCREENS01_IMAGE:-vulos-screens01:trixie}

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin" "$WORK/out"

# THE REAL GENERATOR, copied — not a reimplementation of it. If the shipping
# script stops producing a config that places windows, this gate goes red.
cp "$REPO/scripts/vulos-kiosk-genconfig.sh" "$WORK/bin/vulos-kiosk-genconfig"
chmod +x "$WORK/bin/vulos-kiosk-genconfig"

# ── The stub server ──────────────────────────────────────────────────────────
# Stands in for vulos-server, and does exactly one thing the real one does not:
# it makes each screen visually distinguishable, so a swapped mapping is
# visible in a screendump. The colour comes from the ?screen= connector name
# that vulos-kiosk-genconfig puts in the URL — the same string the windowRule
# and MoveToOutput are built from.
cat > "$WORK/stub.py" <<'PY'
import re, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs

COLOURS = {"1": "#ff0000", "2": "#0000ff", "3": "#00ff00"}


class H(BaseHTTPRequestHandler):
    def do_GET(self):
        q = parse_qs(urlparse(self.path).query)
        screen = (q.get("screen") or [""])[0]
        m = re.search(r"(\d+)$", screen)
        colour = COLOURS.get(m.group(1) if m else "", "#808080")
        body = (
            "<!doctype html><meta charset=utf-8>"
            "<title>Vulos stub {}</title>"
            "<style>html,body{{margin:0;padding:0;width:100%;height:100%;"
            "background:{};}}</style><body></body>"
        ).format(screen or "none", colour).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
        sys.stderr.write("SERVED %s -> %s\n" % (self.path, colour))
        sys.stderr.flush()

    def log_message(self, *a):
        pass


HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY

# ── The instrument ───────────────────────────────────────────────────────────
# Reads grim's binary PPM and reports the DOMINANT colour and its share, then
# labels the output S1 / S2 / blank.
#
# Deliberately not "the two heads differ" and not a file size. A file size is
# what the old gate used, and it cannot tell a screen holding the wrong window
# from one holding the right one. The share floor matters too: a stray majority
# of a few pixels must not be allowed to name a screen, so anything under 40%
# of sampled pixels is reported as `none` whatever its hue.
cat > "$WORK/metric.py" <<'PY'
import sys
from collections import Counter

WANT = {"S1": (0xff, 0x00, 0x00), "S2": (0x00, 0x00, 0xff)}
TOL = 32
FLOOR = 40

try:
    d = open(sys.argv[1], "rb").read()
except OSError:
    print("none - 0 0 0x0"); raise SystemExit
if not d.startswith(b"P6"):
    print("none - 0 0 0x0"); raise SystemExit
vals, idx = [], 2
while len(vals) < 3:
    while idx < len(d) and d[idx:idx + 1].isspace():
        idx += 1
    if d[idx:idx + 1] == b"#":
        while d[idx:idx + 1] not in (b"\n", b""):
            idx += 1
        continue
    s = idx
    while idx < len(d) and not d[idx:idx + 1].isspace():
        idx += 1
    vals.append(int(d[s:idx]))
idx += 1
w, h, _ = vals
px = d[idx:idx + w * h * 3]
c, n = Counter(), 0
for i in range(0, max(len(px) - 2, 0), 3 * 7):
    c[px[i:i + 3]] += 1
    n += 1
if not n:
    print("none - 0 0 %dx%d" % (w, h)); raise SystemExit
top, cnt = c.most_common(1)[0]
share = 100 * cnt // n
label = "none"
if share >= FLOOR:
    for name, want in WANT.items():
        if all(abs(top[i] - want[i]) <= TOL for i in range(3)):
            label = name
print("%s #%02x%02x%02x %d %d %dx%d" % (label, top[0], top[1], top[2], share, len(c), w, h))
PY

# ── The container side ───────────────────────────────────────────────────────
cat > "$WORK/run.sh" <<'RUN'
#!/bin/sh
set -eu
PORT=8099
export XDG_RUNTIME_DIR=/tmp/xdg
mkdir -p "$XDG_RUNTIME_DIR"; chmod 700 "$XDG_RUNTIME_DIR"
# The environment scripts/vulos-kiosk.sh exports on the software-rendering
# path, which is the path every GPU-less box takes. The dead session bus is
# part of that path and is kept rather than papered over: it costs cog one
# "Connection refused" line and nothing else (measured — roadmap/SCREENS.md).
export WLR_BACKENDS=headless WLR_HEADLESS_OUTPUTS=2 WLR_RENDERER=pixman
export WLR_RENDERER_ALLOW_SOFTWARE=1
export DBUS_SESSION_BUS_ADDRESS=unix:path=/dev/null

mkdir -p /work/out
# APPEND, not truncate, and the difference is not cosmetic. Each arm empties
# this file to count its own page loads; with a plain `>` the server keeps
# writing at its old offset, so the file comes back as a hole of NUL bytes
# followed by the new lines — and the first SERVED line of every arm after the
# first is glued to that hole and stops matching `^SERVED`. The readiness wait
# then times out, or worse, the "did both browsers load" assertion reads one
# instead of two. Caught by looking at the bytes; nothing in the output said so.
: > /work/out/stub.log
python3 /work/stub.py "$PORT" >/dev/null 2>>/work/out/stub.log &
i=0
while ! python3 -c "import socket,sys; sys.exit(0 if socket.socket().connect_ex(('127.0.0.1',$PORT))==0 else 1)"; do
    i=$((i + 1)); [ "$i" -lt 30 ] || { echo "stub server never came up"; exit 1; }; sleep 1
done

run_arm() { # $1 = arm name
    arm=$1
    cfg=/work/out/cfg-$arm
    rm -rf "$cfg"

    # Written as `if` rather than `[ … ] && x`, deliberately: under `set -e` an
    # AND-OR list whose test fails is a well-known way to end a script on the
    # line that was supposed to do nothing. scripts/vulos-kiosk-genconfig.sh
    # carries the same note for the same reason.
    force=""
    if [ "$arm" = same-app-id ]; then
        force=org.vulos.kiosk.control
    fi
    if [ "$arm" = normal ] && [ "${BREAK:-}" = same-app-id ]; then
        force=org.vulos.kiosk.broken
    fi
    if [ -n "$force" ]; then
        VULOS_KIOSK_FORCE_APP_ID="$force" \
            /work/bin/vulos-kiosk-genconfig "$cfg" "http://127.0.0.1:$PORT" HEADLESS-1 HEADLESS-2
    else
        /work/bin/vulos-kiosk-genconfig "$cfg" "http://127.0.0.1:$PORT" HEADLESS-1 HEADLESS-2
    fi

    strip=no
    if [ "$arm" = no-rules ]; then
        strip=yes
    fi
    if [ "$arm" = normal ] && [ "${BREAK:-}" = rules ]; then
        strip=yes
    fi
    if [ "$strip" = yes ]; then
        sed -i '/<windowRule /,/<\/windowRule>/d' "$cfg/rc.xml"
    fi

    # The original defect, rebuilt on demand: match on the title the SHELL sets
    # (screenWindowTitle in frontend/src/providers/screenIdentity.ts) instead of
    # the app id COG sets. The connector name is lifted straight out of the app
    # id, so the rule still names the right screen — it just names it through an
    # attribute cog never sends, which is the whole of the original bug.
    if [ "$arm" = normal ] && [ "${BREAK:-}" = title ]; then
        sed -i 's/identifier="org\.vulos\.kiosk\.out-\([^"]*\)"/title="Vulos — \1"/' "$cfg/rc.xml"
    fi

    cp "$cfg/rc.xml" "/work/out/rc-$arm.xml"
    cp "$cfg/session.sh" "/work/out/session-$arm.sh"

    rm -f "$XDG_RUNTIME_DIR"/wayland-*
    : > /work/out/stub.log
    # EXACTLY the argv scripts/vulos-kiosk.sh execs — labwc -C <cfg> -S
    # "/bin/sh <cfg>/session.sh" — and the session.sh is the generated one,
    # run verbatim. Screenshots are taken from outside it so that nothing in
    # this harness edits the file the box would run.
    if [ "$arm" = wire ]; then
        WAYLAND_DEBUG=1 labwc -C "$cfg" -S "/bin/sh $cfg/session.sh" \
            > "/work/out/log-$arm.txt" 2>&1 &
    else
        labwc -C "$cfg" -S "/bin/sh $cfg/session.sh" > "/work/out/log-$arm.txt" 2>&1 &
    fi
    lab=$!

    sock=""
    i=0
    while [ "$i" -lt 40 ]; do
        sock=$(ls "$XDG_RUNTIME_DIR"/wayland-[0-9] 2>/dev/null | head -1) || true
        [ -n "$sock" ] && break
        kill -0 $lab 2>/dev/null || break
        i=$((i + 1)); sleep 1
    done
    if [ -z "$sock" ]; then
        echo "$arm: labwc never opened a wayland socket"
        return 1
    fi
    WAYLAND_DISPLAY=$(basename "$sock"); export WAYLAND_DISPLAY

    # Wait for the browsers to FETCH THE PAGE rather than sleeping a guess.
    # A live process is not a pass — this project has been fooled by a browser
    # that started and painted nothing — so the readiness signal is the stub
    # server's own request log, which is on the far side of the wire from cog.
    i=0
    while [ "$i" -lt 120 ]; do
        n=$(awk '/^SERVED/ { n++ } END { print n + 0 }' /work/out/stub.log)
        if [ "$n" -ge 2 ]; then break; fi
        i=$((i + 1)); sleep 1
    done
    cp /work/out/stub.log "/work/out/served-$arm.log"
    # The wire arm needs only the toplevel's first commit; the placement arms
    # need the page painted.
    if [ "$arm" = wire ]; then sleep 3; else sleep 10; fi

    if [ "$arm" = wire ]; then
        grep -aE 'set_app_id|set_title' "/work/out/log-$arm.txt" > /work/out/wire.log || true
    else
        for o in HEADLESS-1 HEADLESS-2; do
            grim -t ppm -o "$o" "/work/out/$arm-$o.ppm" 2>>"/work/out/grim.log" || true
            printf '%s %s\n' "$o" "$(python3 /work/metric.py "/work/out/$arm-$o.ppm")" \
                >> "/work/out/metrics-$arm.txt"
        done
    fi

    kill $lab 2>/dev/null || true
    i=0
    while [ "$i" -lt 20 ] && kill -0 $lab 2>/dev/null; do i=$((i + 1)); sleep 1; done
    kill -9 $lab 2>/dev/null || true
    wait $lab 2>/dev/null || true
    sleep 3
}

for arm in "$@"; do
    run_arm "$arm"
done
echo "arms done"
RUN

# ── The image ────────────────────────────────────────────────────────────────
# Built rather than apt-installed inline so a local re-run costs seconds. NOT
# pinned to linux/amd64: cog is a full WebKit and running it under qemu-user
# emulation is not a test, it is a different experiment. The gate therefore
# runs on the host's own architecture — arm64 on a developer Mac, amd64 in CI.
echo "▸ building the container image (cached after the first run)…"
docker build -t "$IMAGE" - >/dev/null <<'DOCKERFILE'
FROM debian:trixie
RUN apt-get update -qq \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
      labwc cog grim python3 \
 && rm -rf /var/lib/apt/lists/*
DOCKERFILE

# --privileged, and it is not laziness. cog runs its WebProcess inside
# bubblewrap; with only --cap-add SYS_ADMIN the sandbox gets far enough to fail
# late and unhelpfully ("bwrap: loopback: Failed RTM_NEWADDR", then
# "readPIDFromPeer: Unexpected short read from PID socket") and every browser
# dies seconds after start. Disabling WebKit's sandbox instead would be the
# other way to get a green run, and it would be testing a cog the box does not
# ship.
echo "▸ running labwc + cog on two headless outputs: wire, normal, and two controls…"
docker run --rm --privileged -v "$WORK:/work" -e "BREAK=$BREAK" "$IMAGE" \
    sh /work/run.sh wire normal same-app-id no-rules

# ── Assertions ───────────────────────────────────────────────────────────────
fail=0
say() { printf '  %s\n' "$*"; }
bad() { printf 'FAIL: %s\n' "$*" >&2; fail=1; }

echo
echo "A — the client's contract: what cog put on the wire"

WIRE="$WORK/out/wire.log"
[ -s "$WIRE" ] || bad "no set_app_id/set_title traffic was captured at all — cog never mapped a toplevel"

# Every app id the GENERATOR wrote into the rule must appear on the wire. Both
# halves are read from this run: the rule from the rc.xml it produced, the
# app_id from the protocol. Nothing here is a literal typed into this file, so
# a change to kiosk_app_id() cannot leave the gate asserting a stale string.
ids=$(sed -n 's/.*identifier="\([^"]*\)".*/\1/p' "$WORK/out/rc-wire.xml" | sort -u)
[ -n "$ids" ] || bad "the generated rc.xml carried no identifier= rule to check against"
for id in $ids; do
    if grep -qF "set_app_id(\"$id\")" "$WIRE"; then
        say "cog set app_id $id  ✓"
    else
        bad "the rule matches identifier=\"$id\" but no cog window ever sent that app_id."
        bad "      The two halves of the mechanism disagree, which is the failure this gate exists for."
    fi
done

# The title assertion, which is the anti-regression for the original defect.
titles=$(sed -n 's/.*set_title("\(.*\)").*/\1/p' "$WIRE" | sort -u)
say "titles cog sent: $(echo "$titles" | tr '\n' ' ')"
if [ -z "$titles" ]; then
    bad "no set_title() was captured at all, so the title assertion below checked nothing."
    bad "      Either the wire capture is broken or cog stopped setting a title; both need"
    bad "      a human before this gate can be believed again."
elif [ "$(echo "$titles" | grep -vc '^Cog$' || true)" != "0" ]; then
    bad "cog sent a toplevel title other than \"Cog\"."
    bad "      scripts/vulos-kiosk-genconfig.sh's reasoning — that the title is never"
    bad "      the shell's and so can never be matched on — no longer holds. Re-read it"
    bad "      before changing anything: this gate's predecessor was green for weeks"
    bad "      precisely because it used a client that DID set its title."
fi

echo
echo "B — placement: which browser landed on which output"
printf '  %-13s %-24s %-24s %s\n' arm HEADLESS-1 HEADLESS-2 loads

# `|| true` on every reader: a missing metrics file must produce an empty label
# that the assertions below then report, not a bare `set -e` exit with nothing
# printed.
label() { # $1=arm $2=output
    awk -v o="$2" '$1 == o { print $2 }' "$WORK/out/metrics-$1.txt" 2>/dev/null || true
}
detail() { # $1=arm $2=output
    awk -v o="$2" '$1 == o { print $2 " " $3 " " $4 "%" }' "$WORK/out/metrics-$1.txt" 2>/dev/null || true
}
served() { awk '/^SERVED/ { n++ } END { print n + 0 }' "$WORK/out/served-$1.log" 2>/dev/null || echo 0; }

for arm in normal same-app-id no-rules; do
    printf '  %-13s %-24s %-24s %s\n' \
        "$arm" "$(detail "$arm" HEADLESS-1)" "$(detail "$arm" HEADLESS-2)" "$(served "$arm")"
done
echo

for arm in normal same-app-id no-rules; do
    [ "$(served "$arm")" -ge 2 ] || bad "$arm: only $(served "$arm") page load(s) reached the server — a browser never started, so this arm says nothing either way."
done

n1=$(label normal HEADLESS-1); n2=$(label normal HEADLESS-2)
if [ "$n1" = S1 ] && [ "$n2" = S2 ]; then
    say "normal: screen 1's browser is on HEADLESS-1 and screen 2's is on HEADLESS-2  ✓"
else
    bad "normal: expected S1 on HEADLESS-1 and S2 on HEADLESS-2, got '$n1' and '$n2'."
    if [ "$n1" = none ] || [ "$n2" = none ]; then
        bad "      One output is blank: both browsers landed on the same screen. That is"
        bad "      EXACTLY the failure two QEMU boots found while this gate was green."
    else
        bad "      Both outputs hold a browser but the wrong one. The mapping is swapped —"
        bad "      a failure SCREENS-02 cannot see and this gate exists to catch."
    fi
fi

# The controls. Each must land both browsers on ONE output and leave the other
# blank — the pile-up. An arm that produced one-per-output here would mean the
# rules are decoration, and that is a failure of this whole gate, not a pass.
for arm in same-app-id no-rules; do
    c1=$(label "$arm" HEADLESS-1); c2=$(label "$arm" HEADLESS-2)
    blank=0
    if [ "$c1" = none ]; then blank=$((blank + 1)); fi
    if [ "$c2" = none ]; then blank=$((blank + 1)); fi
    if [ "$blank" = 1 ]; then
        say "control $arm: went red as it must — one output blank, both browsers on the other  ✓"
    elif [ "$blank" = 0 ]; then
        bad "control $arm: both outputs hold a browser."
        bad "      With no distinguishing rule, labwc is placing windows on separate outputs"
        bad "      BY ITSELF — so the 'normal' arm's result cannot be attributed to the rules"
        bad "      and this gate proves nothing. Failing rather than reporting a pass."
    else
        bad "control $arm: both outputs are blank — no browser rendered anywhere, so this"
        bad "      control failed for the wrong reason and controls nothing."
    fi
done

echo
if [ "$fail" != 0 ]; then
    # Keep the evidence: the trap is about to delete $WORK, and a failure whose
    # configs and screendumps have been thrown away is a failure nobody can act
    # on.
    keep=$(mktemp -d /tmp/screens01-evidence-XXXXXX)
    cp -R "$WORK/out/." "$keep/" 2>/dev/null || true
    echo "SCREENS-01 FAILED — evidence above." >&2
    echo "Configs, logs and screendumps kept in $keep" >&2
    exit 1
fi
if [ -n "$BREAK" ]; then
    echo "SCREENS-01 PASSED, but --break $BREAK was set and it was SUPPOSED to fail." >&2
    echo "The injected defect did not move this gate, which means the gate cannot see it." >&2
    exit 1
fi
echo "PASS: cog sets the app id, labwc places each browser on its own output, and"
echo "      both controls pile them onto one."
