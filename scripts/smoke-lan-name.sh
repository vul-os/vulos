#!/usr/bin/env bash
# smoke-lan-name.sh — LANNAME-01. Does the box's own name resolve to the box's
# LAN address, on a box that is also running applications?
#
# ── THE DEFECT ───────────────────────────────────────────────────────────────
#
# Measured 2026-08-17 on a booted arm64 box with one app running:
#
#     avahi-resolve -n vulos.local  ->  169.254.23.36
#     avahi-resolve -a 10.0.2.15    ->  vulos.local
#
# 10.0.2.15 was the box. 169.254.23.36 was an IPv4LL address dhcpcd had put on
# `vh_bae456` — one APPLICATION's veth (backend/services/appnet/namespace.go).
# The box published, as its own identity, an address on an app's private link:
# not its LAN address, and in no certificate's SAN list. Every certificate this
# box mints carries the LAN IP as a SAN (cmd/server/lan_pairing.go certIPs), so
# a client that took that record got a routing failure AND a TLS name mismatch.
#
# Nothing in the tree could see it. internal/lan's tests cover OUR advertiser
# (pion/mdns), which answers with lan.DetectLANIP(); avahi-daemon is a SECOND
# responder that the image installs and systemd starts, derives its answer
# independently, and no test had ever looked at it.
#
# ── WHAT THIS GATE DOES ──────────────────────────────────────────────────────
#
# It builds the box's network shape in a container — a LAN interface, plus an
# appnet-shaped veth pair carrying appnet's own static 10.200.x addressing —
# then runs the REAL daemons the image ships (dhcpcd 10.x in the manager mode
# Debian's unit uses, avahi 0.8) and asks the box, through avahi, what its own
# name resolves to.
#
# Two arms, and the control is the point:
#
#   stock  no Vulos network config at all — i.e. what the image shipped before
#          2026-08-17. This arm MUST resolve the box's name to something that
#          is NOT the LAN address. If it resolves correctly the scenario has
#          stopped reproducing the defect and this gate is no longer testing
#          anything, so that is a FAILURE, not a pass.
#   fixed  scripts/vulos-lan-ifaces.sh applied — the same file the image
#          installs, copied in rather than reimplemented. This arm MUST
#          resolve the box's name to the LAN address, every time.
#
# ── WHY BOTH HALVES OF THE FIX ARE HERE ──────────────────────────────────────
#
# `--break dhcpcd` and `--break avahi` each disable one half. Both must still
# go red, because each half alone is insufficient and that was measured:
#
#   dhcpcd fixed, avahi stock  ->  vulos.local  10.200.23.1
#
# i.e. removing the link-local only changes WHICH wrong address you get. avahi
# is the layer that owns publishing, and dhcpcd is the layer that owns having
# no business on an app's link at all (it solicits DHCP inside every app
# namespace and will install a DEFAULT ROUTE from whatever answers).
#
# ── WHAT THIS CANNOT PROVE ───────────────────────────────────────────────────
#
#  * It runs in Docker. There is no udevd, so dhcpcd is given `nodev`; without
#    it dhcpcd prints "waiting for interface to initialise" for EVERY
#    interface, finds none, and exits — which would make this gate green for
#    the wrong reason. `nodev` is a container workaround, not image config, and
#    it means this gate cannot see anything that depends on real udev ordering,
#    real NIC drivers, wifi association, or systemd unit ordering on a real
#    boot. Those remain unproven here.
#  * It does not boot a Vulos image. It asserts the behaviour of the config the
#    image installs, not that the image installs it — TestLANIfacesInstalled
#    (backend/internal/docsref) is the half that reads build.sh.
#  * It uses ONE app veth. Nothing here says anything about many.
#
# Measured on this host, arm64, before any fix (the exact founder symptom):
#     vulos.local   169.254.23.36
# and after:
#     vulos.local   192.168.215.13   (6 lookups, 6 identical answers)
set -euo pipefail

BREAK=""
while [ $# -gt 0 ]; do
    case "$1" in
        --break) BREAK="${2:-}"; shift 2 ;;
        *) echo "usage: $0 [--break avahi|dhcpcd]" >&2; exit 2 ;;
    esac
done
case "$BREAK" in
    ''|avahi|dhcpcd) ;;
    *) echo "$0: unknown --break '$BREAK'" >&2; exit 2 ;;
esac

REPO=$(cd "$(dirname "$0")/.." && pwd)
IMAGE=${VULOS_LANNAME_IMAGE:-vulos-lanname:trixie}

# LOOKUPS is asserted, not just used: see the coverage count below.
LOOKUPS=6
APP_VETH=vh_bae456
APP_IP=10.200.23.1

WORK=$(mktemp -d)
cleanup() {
    _rc=$?
    # Same lesson as SCREENS-01: the arms run as root through a bind mount, so
    # a plain rm can fail and REPLACE the verdict with its own exit status.
    docker run --rm -v "$WORK:/work" "$IMAGE" rm -rf /work/out >/dev/null 2>&1 || true
    rm -rf "$WORK" >/dev/null 2>&1 || true
    exit "$_rc"
}
trap cleanup EXIT
mkdir -p "$WORK/out" "$WORK/bin"

# THE REAL SCRIPT, copied — not a reimplementation. If the shipping file stops
# producing a config that keeps the box's name on the LAN, this gate goes red.
cp "$REPO/scripts/vulos-lan-ifaces.sh" "$WORK/bin/vulos-lan-ifaces"
chmod +x "$WORK/bin/vulos-lan-ifaces"

cat > "$WORK/run.sh" <<'RUN'
set -u
arm=$1
BREAK=${BREAK:-}
APP_VETH=${APP_VETH:-vh_bae456}
APP_IP=${APP_IP:-10.200.23.1}
LOOKUPS=${LOOKUPS:-6}
out=/work/out

# The box's real LAN address, before anything else touches the network. This is
# the answer every lookup below must produce.
lanip=$(ip -4 -o addr show dev eth0 | awk '{print $4}' | cut -d/ -f1 | head -1)
printf '%s\n' "$lanip" > "$out/lanip-$arm.txt"

# An appnet-shaped app link: veth pair, host side addressed statically exactly
# as backend/services/appnet/namespace.go does it (HostIP + "/24").
ip link add "$APP_VETH" type veth peer name vn_bae456
ip addr add "$APP_IP/24" dev "$APP_VETH"
ip link set "$APP_VETH" up
ip link set vn_bae456 up

# `nodev`: container workaround, documented in this script's header.
printf '\nnodev\n' >> /etc/dhcpcd.conf

if [ "$arm" = fixed ]; then
    [ "$BREAK" = dhcpcd ] || /work/bin/vulos-lan-ifaces --dhcpcd
    [ "$BREAK" = avahi  ] || /work/bin/vulos-lan-ifaces --avahi
fi
cp /etc/dhcpcd.conf "$out/dhcpcd-$arm.conf"
grep -E '^allow-interfaces=' /etc/avahi/avahi-daemon.conf > "$out/avahi-$arm.txt" 2>/dev/null || : > "$out/avahi-$arm.txt"

# dhcpcd exactly as Debian's dhcpcd.service runs it: manager mode, all
# interfaces. -B keeps it in the foreground of this subshell so the log is ours.
/usr/sbin/dhcpcd -b -B -d > "$out/dhcpcd-$arm.log" 2>&1 &
sleep 40

ip -4 -o addr show > "$out/addr-$arm.txt"

mkdir -p /run/dbus
dbus-daemon --system --fork
sleep 1
avahi-daemon --no-drop-root --no-rlimits -D > "$out/avahi-$arm.log" 2>&1
sleep 6

: > "$out/resolve-$arm.txt"
i=0
while [ "$i" -lt "$LOOKUPS" ]; do
    avahi-resolve -n vulos.local >> "$out/resolve-$arm.txt" 2>&1 || echo "RESOLVE-FAILED" >> "$out/resolve-$arm.txt"
    i=$((i + 1))
done
echo "arm $arm done"
RUN

echo "▸ building the container image (cached after the first run)…"
docker build -t "$IMAGE" - >/dev/null <<'DOCKERFILE'
FROM debian:trixie
RUN apt-get update -qq \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
      avahi-daemon avahi-utils dhcpcd5 iproute2 dbus \
 && rm -rf /var/lib/apt/lists/*
DOCKERFILE

# Each arm gets its OWN container: dhcpcd, avahi and the interface set are all
# process- and kernel-state, and an arm that inherited the previous arm's
# addresses would be measuring the wrong box. --privileged is needed to create
# the veth pair and for dhcpcd's raw sockets.
ARMS="stock fixed"
ran=0
for arm in $ARMS; do
    echo "▸ arm '$arm': dhcpcd + avahi on a box with an app veth…"
    docker run --rm --privileged -h vulos -v "$WORK:/work" \
        -e "BREAK=$BREAK" -e "APP_VETH=$APP_VETH" -e "APP_IP=$APP_IP" -e "LOOKUPS=$LOOKUPS" \
        "$IMAGE" sh /work/run.sh "$arm" >/dev/null
    ran=$((ran + 1))
done

# ── Assertions ───────────────────────────────────────────────────────────────
fail=0
say() { printf '  %s\n' "$*"; }
bad() { printf 'FAIL: %s\n' "$*"; fail=1; }

# COVERAGE COUNT. Every guard in this suite that carried one survived mutation
# and every one that lacked one did not. These numbers are the claim that the
# assertions below actually ran over the thing they name: two arms, and
# LOOKUPS answers recorded in each. Deleting an arm, or an avahi that answers
# once and then dies, fails HERE rather than passing quietly with less
# evidence than the report implies.
if [ "$ran" -ne 2 ]; then
    bad "coverage: $ran arms ran, expected exactly 2 (stock, fixed)"
fi
for arm in $ARMS; do
    n=$(grep -c . "$WORK/out/resolve-$arm.txt" 2>/dev/null || echo 0)
    if [ "$n" -ne "$LOOKUPS" ]; then
        bad "coverage: arm '$arm' recorded $n lookup answers, expected exactly $LOOKUPS"
    fi
done

answers() { awk '{print $2}' "$WORK/out/resolve-$1.txt" | sort -u | tr '\n' ' '; }

echo "A — did the scenario reproduce the defect? (control)"
lan_stock=$(cat "$WORK/out/lanip-stock.txt")
a_stock=$(answers stock)
say "arm stock: LAN address is $lan_stock, vulos.local answered: $a_stock"
if [ "$a_stock" = "$lan_stock " ]; then
    bad "control 'stock' resolved the box's name CORRECTLY without any Vulos config."
    bad "      The defect is not being reproduced, so arm 'fixed' passing proves nothing."
    bad "      Do not weaken this — find out why the app veth stopped being published."
else
    say "control stock: went red as it must — the box's own name answered $a_stock, not $lan_stock  ✓"
fi

echo "B — does the shipping config put the box's name back on the LAN?"
lan_fixed=$(cat "$WORK/out/lanip-fixed.txt")
a_fixed=$(answers fixed)
say "arm fixed:  LAN address is $lan_fixed, vulos.local answered: $a_fixed"
say "arm fixed:  avahi allow-interfaces -> $(cat "$WORK/out/avahi-fixed.txt")"
if [ "$a_fixed" = "$lan_fixed " ]; then
    say "every one of the $LOOKUPS lookups answered with the LAN address  ✓"
else
    bad "vulos.local answered '$a_fixed'; every answer must be the LAN address $lan_fixed."
    bad "      A LAN client taking that record reaches nothing, and the address is in"
    bad "      no certificate SAN, so it is a TLS name mismatch as well."
fi

echo "C — is dhcpcd still off the app veth?"
ll_fixed=$(grep -c "$APP_VETH.*169\.254" "$WORK/out/addr-fixed.txt" || true)
ll_stock=$(grep -c "$APP_VETH.*169\.254" "$WORK/out/addr-stock.txt" || true)
say "IPv4LL addresses on $APP_VETH — stock: $ll_stock, fixed: $ll_fixed"
if [ "$ll_stock" -eq 0 ]; then
    bad "control: dhcpcd did NOT put an IPv4LL address on $APP_VETH even in the stock arm."
    bad "      That half of the scenario is not reproducing either; assertion C is inert."
fi
if [ "$ll_fixed" -ne 0 ]; then
    bad "dhcpcd still IPv4LL-addressed $APP_VETH with the shipping config applied."
fi
# dhcpcd must still be doing its actual job on the LAN interface.
if ! grep -q "^eth0" "$WORK/out/dhcpcd-fixed.log" && ! grep -q "eth0:" "$WORK/out/dhcpcd-fixed.log"; then
    bad "dhcpcd stopped managing eth0 too — the deny-list is too broad."
fi

if [ "$fail" != 0 ]; then
    ev=$(mktemp -d /tmp/lanname01-evidence-XXXXXX)
    cp -R "$WORK/out/." "$ev/" 2>/dev/null || true
    echo "LANNAME-01 FAILED — evidence above." >&2
    echo "Logs and configs kept in $ev" >&2
    exit 1
fi
if [ -n "$BREAK" ]; then
    echo "LANNAME-01 PASSED, but --break $BREAK was set and it was SUPPOSED to fail." >&2
    exit 1
fi
echo "PASS: with an app veth present, dhcpcd stays off it and the box's own name"
echo "      resolves to the box's LAN address, $LOOKUPS times out of $LOOKUPS."
