#!/bin/sh
# vulos-lan-ifaces.sh — decide which interfaces may carry the box's IDENTITY.
#
# ── THE DEFECT THIS EXISTS TO FIX (measured 2026-08-17, booted arm64 box) ────
#
#     avahi-resolve -n vulos.local  ->  169.254.23.36
#     avahi-resolve -a 10.0.2.15    ->  vulos.local
#
# 10.0.2.15 is the box. 169.254.23.36 is an IPv4LL address that dhcpcd put on
# `vh_bae456`, an APP's veth (backend/services/appnet/namespace.go:140 —
# `VethHost: fmt.Sprintf("vh_%s", shortID)`). So the box's own name resolved to
# an address that belongs to one application's private point-to-point link and
# is in no certificate's SAN list.
#
# Reproduced end to end in a Debian trixie container (arm64, dhcpcd 10.1.0,
# avahi 0.8-16) — see scripts/smoke-lan-name.sh, which is the gate. Two
# separate mechanisms combine, and only one of them OWNS the defect:
#
#  1. dhcpcd MANAGES THE APP VETHS. Debian's dhcpcd.service is literally
#     "DHCP Client Daemon on all interfaces" (ExecStart=/usr/sbin/dhcpcd -q -b,
#     manager mode) and the stock /etc/dhcpcd.conf carries no `denyinterfaces`.
#     Measured, with an appnet-shaped veth present:
#
#       vh_bae456: soliciting a DHCP lease
#       vh_bae456: probing for an IPv4LL address
#       vh_bae456: using IPv4LL address 169.254.69.120
#       vh_bae456: adding IP address 169.254.69.120/16 ...
#       vh_bae456: adding default route          <-- READ THAT AGAIN
#
#     That last line is the reason this half is not cosmetic. dhcpcd broadcasts
#     DHCP DISCOVER *into every app's network namespace* and will accept a
#     lease from whatever answers. An untrusted app that runs a DHCP server on
#     its own side of the veth can therefore hand the HOST a default route and
#     a set of resolvers. dhcpcd has no business on an app's link at all:
#     appnet already addresses both ends statically.
#
#  2. avahi PUBLISHES THE BOX'S NAME ON THOSE LINKS. This is the half that owns
#     the reported defect, and fixing (1) alone does NOT fix it — measured,
#     with no link-local anywhere and only appnet's own static address present:
#
#       vulos.local  10.200.23.1     (6 lookups, 6 identical answers)
#
#     Still not the box's LAN address; still in no SAN list. An app-network
#     interface acquiring an address is arguably fine. Publishing it as the
#     box's identity is not. So avahi is the layer that owns this.
#
# ── WHY AN ALLOW-LIST AND NOT A DENY-LIST ───────────────────────────────────
#
# avahi's deny-interfaces takes EXACT NAMES ONLY. Measured, same container:
#
#   deny-interfaces=vh_*         ->  vulos.local  10.200.23.1   (no effect)
#   deny-interfaces=vh_bae456    ->  vulos.local  192.168.215.13 (works)
#
# App veth names are derived from a hash of the app id and appear and vanish as
# apps start and stop, so an exact-name deny-list cannot be written in advance.
# The set of LAN interfaces, by contrast, is knowable at avahi start. Hence:
# compute the allow-list here and hand it to avahi.
#
# ── FAIL-OPEN, DELIBERATELY ─────────────────────────────────────────────────
#
# If this script cannot identify a single LAN interface it REMOVES the
# restriction rather than writing an empty one. An empty allow-list would leave
# a box with no mDNS name at all, which is a worse failure than the one being
# fixed, and it is exactly the failure a novel NIC naming scheme would cause.
#
# ── WHAT IS NOT COVERED ─────────────────────────────────────────────────────
#
# The allow-list is a snapshot taken when avahi starts (it is wired as an
# ExecStartPre on avahi-daemon.service, so it is recomputed on every avahi
# start and restart, not once at install). A NIC that appears LATER — a USB
# ethernet adapter hotplugged into a running box — is not in the list until
# avahi is restarted. That is a real gap and it is not addressed here.

set -eu

# APP_IFACE_GLOBS — the interfaces that carry APP traffic and must never carry
# the box's identity. `vh_*` and `vn_*` are backend/services/appnet's veth pair
# (namespace.go:140-141); `vulos-br0` is the bridge name that appnet's Manager
# is constructed with (namespace.go:82) and would use if the point-to-point
# model ever went back to a bridge.
#
# TestAppIfaceGlobsCoverAppnet pins this list against appnet's own source, so
# renaming the veths there fails the build here rather than silently
# re-opening the defect.
APP_IFACE_GLOBS='vh_* vn_* vulos-br0'

# VIRT_IFACE_GLOBS — links that exist on a box but are never the way a LAN
# client reaches it. Kept separate from APP_IFACE_GLOBS because only the first
# list is pinned against appnet.
VIRT_IFACE_GLOBS='lo docker* br-* veth* virbr* tun* tap* wg* zt* dummy* sit* tunl* ip6tnl* bond* gre*'

SYSNET=${VULOS_SYSNET_ROOT:-/sys/class/net}
AVAHI_CONF=${VULOS_AVAHI_CONF:-/etc/avahi/avahi-daemon.conf}
DHCPCD_CONF=${VULOS_DHCPCD_CONF:-/etc/dhcpcd.conf}

MARKER='# --- vulos: app veths are not DHCP links (vulos-lan-ifaces.sh) ---'

# matches_any reports whether $1 matches any whitespace-separated glob in $2.
#
# `set -f` is load-bearing and was a real bug here: splitting the glob list
# without it makes the SHELL expand the globs against the CURRENT DIRECTORY
# first. Run from the repo root, `docker*` became `docker-compose.yml` and
# docker0 was never excluded. Pathname expansion is off for the split; `case`
# patterns are unaffected by it, so the intended matching still happens.
matches_any() {
    _name=$1
    _globs=$2
    set -f
    for _g in $_globs; do
        # shellcheck disable=SC2254 # $_g is a glob on purpose
        case "$_name" in
            $_g) set +f; return 0 ;;
        esac
    done
    set +f
    return 1
}

# lan_ifaces prints, one per line, the interfaces that MAY carry the box's
# identity: everything present that is neither an app link nor virtual
# plumbing.
lan_ifaces() {
    [ -d "$SYSNET" ] || return 0
    for _p in "$SYSNET"/*; do
        [ -e "$_p" ] || continue
        _n=$(basename "$_p")
        matches_any "$_n" "$APP_IFACE_GLOBS" && continue
        matches_any "$_n" "$VIRT_IFACE_GLOBS" && continue
        printf '%s\n' "$_n"
    done
}

# dhcpcd_stanza prints the block appended to /etc/dhcpcd.conf. It is generated
# from APP_IFACE_GLOBS rather than written out a second time in build.sh, so
# the deny-list and the allow-list cannot disagree about what an app link is.
dhcpcd_stanza() {
    printf '%s\n' "$MARKER"
    printf '%s\n' '# See scripts/vulos-lan-ifaces.sh for the measurement. dhcpcd in manager'
    printf '%s\n' '# mode claims appnet veths, IPv4LL-addresses them, and will take a DHCP'
    printf '%s\n' '# lease -- including a DEFAULT ROUTE -- from whatever answers inside an'
    printf '%s\n' '# untrusted application namespace. These are not DHCP links.'
    printf 'denyinterfaces %s\n' "$APP_IFACE_GLOBS"
}

# write_avahi rewrites the allow-interfaces line in avahi-daemon.conf.
write_avahi() {
    if [ ! -f "$AVAHI_CONF" ]; then
        echo "vulos-lan-ifaces: $AVAHI_CONF not found — leaving avahi unconfigured" >&2
        return 0
    fi
    _list=$(lan_ifaces | sort | tr '\n' ',' | sed 's/,$//')

    _tmp="$AVAHI_CONF.vulos.$$"
    # Drop any previous allow-interfaces (ours or the packaged commented one)
    # and any line we wrote before, then re-add under [server].
    grep -v '^allow-interfaces=' "$AVAHI_CONF" > "$_tmp"

    if [ -z "$_list" ]; then
        echo "vulos-lan-ifaces: no LAN interface identified — NOT restricting avahi" >&2
        mv "$_tmp" "$AVAHI_CONF"
        return 0
    fi

    awk -v list="$_list" '
        { print }
        /^\[server\]/ && !done { print "allow-interfaces=" list; done = 1 }
    ' "$_tmp" > "$_tmp.2"
    mv "$_tmp.2" "$AVAHI_CONF"
    rm -f "$_tmp"
    echo "vulos-lan-ifaces: avahi allow-interfaces=$_list" >&2
}

# write_dhcpcd appends the deny stanza once, idempotently.
write_dhcpcd() {
    if [ ! -f "$DHCPCD_CONF" ]; then
        echo "vulos-lan-ifaces: $DHCPCD_CONF not found — dhcpcd not configured" >&2
        return 0
    fi
    if grep -qF "$MARKER" "$DHCPCD_CONF"; then
        return 0
    fi
    printf '\n' >> "$DHCPCD_CONF"
    dhcpcd_stanza >> "$DHCPCD_CONF"
}

case "${1:---avahi}" in
    --avahi)          write_avahi ;;
    --dhcpcd)         write_dhcpcd ;;
    --dhcpcd-stanza)  dhcpcd_stanza ;;
    --list-lan)       lan_ifaces ;;
    --list-app-globs) printf '%s\n' "$APP_IFACE_GLOBS" ;;
    *) echo "usage: $0 [--avahi|--dhcpcd|--dhcpcd-stanza|--list-lan|--list-app-globs]" >&2; exit 2 ;;
esac
