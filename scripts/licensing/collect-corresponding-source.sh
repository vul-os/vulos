#!/bin/bash
# collect-corresponding-source.sh — make the Corresponding Source of the Vulos
# image actually PRODUCIBLE.
#
# WHY THIS EXISTS
# ---------------
# The Vulos image is a Debian derivative: build.sh debootstraps trixie and
# apt-installs bash, sudo, systemd, coreutils, iptables, flatpak, GStreamer,
# PulseAudio and friends into the rootfs, which is then packed into
# image.squashfs and shipped (and netbooted, and SOLD).
#
# Those binaries are under the GPL-2.0, GPL-3.0, LGPL-2.1 and LGPL-3.0. When you
# convey them in binary form you must also deliver the Corresponding Source, and
# the "just point at the upstream mirror" shortcut (GPL-2 §3(c) / GPL-3 §6(c))
# is only available to NONCOMMERCIAL redistributors. Vulos is sold, so that
# shortcut is not ours. Our route is:
#
#   - GPL-2 §3(b): a written offer, valid for at least three years, to give any
#     third party the complete machine-readable Corresponding Source; or
#   - GPL-3 §6(b): the equivalent written offer, valid three years (and, for as
#     long as we offer spare parts or support, for that period too).
#
# See WRITTEN-OFFER.md (DRAFT — the founder and a lawyer must finalise it).
#
# A written offer is only honest if the source can still be produced when it is
# called in — years later, after deb.debian.org has long since dropped those
# exact versions. That is what this script is for:
#
#   1. It records the EXACT binary and source package versions installed into a
#      specific image (SOURCES.manifest).
#   2. It pins a snapshot.debian.org timestamp, which is an archive that keeps
#      every version forever, so `apt-get source <pkg>=<exact-version>` still
#      resolves in three years' time (sources.list.snapshot).
#   3. With --fetch it downloads that source right now, so we hold it ourselves
#      and do not depend on snapshot.debian.org still being up when someone asks
#      (SHA256SUMS over the downloaded .dsc/.orig/.debian files).
#
# It fails closed: if any source package cannot be fetched, the script exits
# non-zero rather than let a release ship with an offer it cannot honour.
#
# USAGE
#   collect-corresponding-source.sh --rootfs DIR [--out DIR] [--fetch] [--snapshot STAMP]
#
#   --rootfs DIR    the built rootfs to inventory (build.sh: $OUTDIR/rootfs)
#   --out DIR       where to write the manifest/snapshot pin (default: alongside rootfs)
#   --fetch         actually download the corresponding source (needs root + network;
#                   this is the step that makes the written offer honourable)
#   --snapshot S    pin to this snapshot.debian.org timestamp (default: now, UTC).
#                   Reuse the SAME stamp when re-fetching source for an image you
#                   already shipped.
#
# Requires root (it chroots into the rootfs), as build.sh already does.
set -euo pipefail

ROOTFS=""
OUTDIR=""
DO_FETCH=0
SNAPSHOT=""

while [ $# -gt 0 ]; do
  case "$1" in
    --rootfs)   ROOTFS="$2"; shift 2 ;;
    --out)      OUTDIR="$2"; shift 2 ;;
    --fetch)    DO_FETCH=1; shift ;;
    --snapshot) SNAPSHOT="$2"; shift 2 ;;
    -h|--help)  sed -n '2,50p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[ -n "$ROOTFS" ] || { echo "error: --rootfs is required" >&2; exit 2; }
[ -d "$ROOTFS" ] || { echo "error: rootfs not found: $ROOTFS" >&2; exit 2; }
[ -x "$ROOTFS/usr/bin/dpkg-query" ] || [ -x "$ROOTFS/bin/dpkg-query" ] || {
  echo "error: $ROOTFS does not look like a Debian rootfs (no dpkg-query)" >&2; exit 2; }

[ -n "$OUTDIR" ] || OUTDIR="$(dirname "$ROOTFS")"
mkdir -p "$OUTDIR"

MANIFEST="$OUTDIR/SOURCES.manifest"
SNAPFILE="$OUTDIR/sources.list.snapshot"
SRCDIR="$OUTDIR/corresponding-source"

# snapshot.debian.org addresses an archive by an exact instant. Pin one now and
# write it down: the offer must resolve to the SAME archive state in three years.
[ -n "$SNAPSHOT" ] || SNAPSHOT="$(date -u +%Y%m%dT%H%M%SZ)"
SNAP_URL="https://snapshot.debian.org/archive/debian/$SNAPSHOT/"

echo "▸ Inventorying installed packages in $ROOTFS"

# ${source:Package} / ${source:Version} give the SOURCE package a binary was
# built from — which is what the GPL obliges us to be able to hand over. They
# differ from the binary name and version more often than people expect
# (e.g. binary libcurl4 comes from source curl; binary bash 5.2.37-1 from
# source bash 5.2.37-1; libpulse0 from pulseaudio).
chroot "$ROOTFS" dpkg-query -W \
  -f='${binary:Package}\t${Version}\t${source:Package}\t${source:Version}\t${Architecture}\n' \
  > "$MANIFEST.tmp"

# dpkg leaves ${source:Version} empty when it equals the binary version.
awk -F'\t' 'BEGIN{OFS="\t"} {if($3=="")$3=$1; if($4=="")$4=$2; print}' "$MANIFEST.tmp" | sort > "$MANIFEST"
rm -f "$MANIFEST.tmp"

NBIN=$(wc -l < "$MANIFEST" | tr -d ' ')
NSRC=$(awk -F'\t' '{print $3"="$4}' "$MANIFEST" | sort -u | wc -l | tr -d ' ')
echo "  ✓ $MANIFEST — $NBIN binary packages from $NSRC source packages"

cat > "$SNAPFILE" << EOF
# Corresponding Source for the Vulos image, pinned to a snapshot.debian.org
# instant so the exact versions in SOURCES.manifest stay fetchable after
# deb.debian.org has moved on.
#
# Snapshot pinned at: $SNAPSHOT
#
# To fetch the source for a package in SOURCES.manifest:
#   apt-get -o Acquire::Check-Valid-Until=false update
#   apt-get source <source-package>=<source-version>
Types: deb-src
URIs: $SNAP_URL
Suites: trixie
Components: main contrib non-free non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
EOF
echo "  ✓ $SNAPFILE — pinned to $SNAPSHOT"

if [ "$DO_FETCH" != "1" ]; then
  echo ""
  echo "  Manifest only. To make the written offer honourable, fetch the source:"
  echo "    sudo $0 --rootfs $ROOTFS --fetch --snapshot $SNAPSHOT"
  echo ""
  exit 0
fi

# ── fetch the actual corresponding source ────────────────────────────────────
echo "▸ Fetching corresponding source from $SNAP_URL"
mkdir -p "$SRCDIR"

# Work inside the rootfs so apt resolves the same suite/components the image was
# built from. A deb-src list pointing at the pinned snapshot is added; the
# snapshot's Release files are old on purpose, so Check-Valid-Until is disabled.
CHROOT_SRC="$ROOTFS/etc/apt/sources.list.d/vulos-corresponding-source.sources"
cp "$SNAPFILE" "$CHROOT_SRC"
mkdir -p "$ROOTFS/src-out"

chroot "$ROOTFS" apt-get -o Acquire::Check-Valid-Until=false update

FAILED="$OUTDIR/MISSING-SOURCE.txt"
: > "$FAILED"
n=0
awk -F'\t' '{print $3"\t"$4}' "$MANIFEST" | sort -u | while IFS=$'\t' read -r src srcver; do
  [ -n "$src" ] || continue
  n=$((n + 1))
  echo "  [$n/$NSRC] $src=$srcver"
  if ! chroot "$ROOTFS" sh -c "cd /src-out && apt-get source --download-only -o Acquire::Check-Valid-Until=false -y '$src=$srcver'" > /dev/null 2>&1; then
    echo "$src=$srcver" >> "$FAILED"
    echo "      ✗ could not fetch"
  fi
done

# Move the downloads out of the rootfs (they must not end up inside the image).
find "$ROOTFS/src-out" -maxdepth 1 -type f -exec mv {} "$SRCDIR/" \; 2>/dev/null || true
rm -rf "$ROOTFS/src-out" "$CHROOT_SRC"

( cd "$SRCDIR" && find . -maxdepth 1 -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS ) || true
NFILES=$(find "$SRCDIR" -maxdepth 1 -type f ! -name SHA256SUMS | wc -l | tr -d ' ')
SIZE=$(du -sh "$SRCDIR" 2>/dev/null | cut -f1)
echo "  ✓ $SRCDIR — $NFILES files ($SIZE), hashed in SHA256SUMS"

if [ -s "$FAILED" ]; then
  echo ""
  echo "✗ FAILED to fetch corresponding source for $(wc -l < "$FAILED" | tr -d ' ') source package(s):"
  cat "$FAILED"
  echo ""
  echo "  A written offer we cannot honour is worse than no offer. Resolve these"
  echo "  (usually: pick a snapshot timestamp at or after the image's build date)"
  echo "  before this image is distributed."
  exit 1
fi

rm -f "$FAILED"
echo ""
echo "✓ Corresponding source for every installed package is in hand."
echo "  Archive $SRCDIR with the release. It is what the written offer promises."
