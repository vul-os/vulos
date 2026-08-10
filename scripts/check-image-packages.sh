#!/usr/bin/env bash
# check-image-packages.sh — the apt package sets are deliberate lists.
#
# There are THREE of them, not one, and they belong to three different targets:
#
#   Dockerfile        → the container image (streamed apps over WebRTC)
#   build.sh deploy   → `--deploy` first-time setup on a remote Debian host
#   build.sh rootfs   → the debootstrap rootfs for the bare-metal OS image
#
# They overlap heavily but are NOT the same set, and pinning their union would
# be a hollow gate: a package deleted from one list is still present in another,
# so a union check reports a clean bill of health while a target loses it. Each
# list is therefore pinned separately, and manifest entries carry their list.
#
# WHY THIS COVERS build.sh AT ALL: it did not, and both build.sh lists drifted
# unnoticed for exactly that reason. The deploy list had its backslash
# continuation broken so that `avahi-daemon avahi-utils dhcpcd5 wpasupplicant`
# and `openssh-server` were passed as ARGUMENTS TO FLATPAK instead of installed
# — a remote box with no SSH server, no DHCP client and no mDNS — and nothing
# noticed because the only package gate in the repo read the Dockerfile.
#
# WHY IT ASSERTS THE COUNT, NOT JUST MEMBERSHIP: a check that only verifies the
# expected entries are present passes happily when twenty extra ones show up,
# which is exactly the direction this drifts. Both directions fail here.
#
# WHY IT FAILS ON AN EMPTY LIST: the build.sh lists are located by a
# `# PACKAGE-SET: <name>` marker comment. Delete the marker and the extractor
# finds nothing — which would otherwise look like "no packages changed" for a
# list that vanished. An empty extraction is a hard error.
#
# Usage:
#   scripts/check-image-packages.sh            # verify against the manifests
#   scripts/check-image-packages.sh --update   # rewrite them from the sources,
#                                              # for a reviewed diff
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DOCKERFILE="$ROOT/Dockerfile"
BUILD_SH="$ROOT/build.sh"
MANIFEST="$ROOT/scripts/image-packages.txt"
BUILD_MANIFEST="$ROOT/scripts/build-sh-packages.txt"

c_r=$'\033[0;31m'; c_g=$'\033[0;32m'; c_y=$'\033[0;33m'; c_n=$'\033[0m'

# Extract the top-level packages from every `apt-get install` block: take the
# lines of each continued command and drop flags, the command words, and any
# shell operators. This reads the source rather than a built image on purpose —
# it runs in a second, needs no Docker, and catches the mistake at the point it
# is made.
#
# It is also why the gate sees the truncation bug at all: the extractor stops at
# the first line WITHOUT a trailing backslash, exactly like the shell does. A
# list whose continuation is broken therefore extracts short, and the packages
# below the break show up as REMOVED.
extract_packages() {
  # $1 = file, $2 = marker name to require before the block ("" = any block)
  awk -v want="${2:-}" '
    # With a marker: arm only when the marker comment is seen, so build.sh’s
    # small device-firmware installs are not swept into the pinned set.
    want != "" && $0 ~ ("# ?PACKAGE-SET: ?" want "([^a-z0-9-]|$)") { armed = 1; next }
    # Start collecting at `apt-get install`, and STOP at the first `&&`.
    # Without that stop this swept up arguments to whatever was chained after
    # the install — `dpkg --add-architecture amd64` contributed "amd64", and
    # `flatpak remote-add ... flathub` contributed "remote-add" and "flathub".
    # A manifest full of non-packages is worse than none: it makes the diff
    # noisy enough that nobody reads it, which is the failure this gate exists
    # to prevent.
    /apt-get[[:space:]]+install/ {
      if (want == "" || armed) { inblock = 1; seen_install = 0; armed = 0 }
    }
    inblock {
      line = $0
      sub(/#.*/, "", line)
      gsub(/\\$/, "", line)
      n = split(line, tok, /[[:space:]]+/)
      for (i = 1; i <= n; i++) {
        t = tok[i]
        # Only AFTER `install` does `&&` mean "the package list ended". The
        # opening line is `RUN apt-get update && apt-get install ...`, so
        # checking first made every block terminate on its own first line and
        # the manifest collapsed to nothing.
        if (seen_install && (t == "&&" || t == "||" || t == ";")) { inblock = 0; break }
        if (t == "install") { seen_install = 1; continue }
        if (!seen_install) continue
        if (t == "" || t ~ /^-/) continue
        if (t ~ /^[a-z0-9][a-z0-9.+-]*$/) print t
      }
      # The shell ends the command here, and so does this.
      if ($0 !~ /\\$/) inblock = 0
    }
  ' "$1" | sort -u
}

# build.sh’s lists, one package per line, prefixed with the list they belong to.
extract_build_sh() {
  local name
  for name in deploy rootfs; do
    local pkgs
    pkgs="$(extract_packages "$BUILD_SH" "$name")"
    if [ -z "$pkgs" ]; then
      echo "${c_r}FATAL:${c_n} no packages extracted for build.sh list '$name'." >&2
      echo "       The '# PACKAGE-SET: $name' marker comment above its apt-get" >&2
      echo "       install block is missing, or the block moved away from it." >&2
      exit 1
    fi
    printf '%s\n' "$pkgs" | sed "s/^/$name /"
  done
}

ACTUAL="$(extract_packages "$DOCKERFILE" "")"
ACTUAL_BUILD="$(extract_build_sh | sort -u)"

if [ "${1:-}" = "--update" ]; then
  printf '%s\n' "$ACTUAL" > "$MANIFEST"
  {
    echo "# build.sh package sets, pinned per list — see scripts/check-image-packages.sh."
    echo "# Format: <list> <package>. 'deploy' is the remote --deploy first-time"
    echo "# setup; 'rootfs' is the debootstrap bare-metal image. They differ on"
    echo "# purpose; the difference is the point of pinning them separately."
    echo "# Tokens carrying a shell variable — \"linux-image-\${GOARCH}\" — are not"
    echo "# pinnable by name and are deliberately absent."
    printf '%s\n' "$ACTUAL_BUILD"
  } > "$BUILD_MANIFEST"
  echo "${c_g}manifests rewritten:${c_n}"
  echo "  $MANIFEST ($(printf '%s\n' "$ACTUAL" | grep -c . || true) packages)"
  echo "  $BUILD_MANIFEST ($(printf '%s\n' "$ACTUAL_BUILD" | grep -c . || true) entries)"
  echo "Review the diff before committing — that review IS the gate."
  exit 0
fi

fail=0

compare_set() {
  local label="$1" manifest="$2" actual="$3" source_desc="$4"

  if [ ! -f "$manifest" ]; then
    echo "${c_r}FATAL:${c_n} no manifest at $manifest — run: $0 --update" >&2
    exit 1
  fi

  local expected added removed n_expected n_actual bad=0
  expected="$(grep -v '^#' "$manifest" | grep . | sort -u)"

  added="$(comm -13 <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") || true)"
  removed="$(comm -23 <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") || true)"

  n_expected=$(printf '%s\n' "$expected" | grep -c . || true)
  n_actual=$(printf '%s\n' "$actual" | grep -c . || true)

  # The count is asserted separately and FIRST, so a manifest that somehow
  # agrees on membership while disagreeing on size still fails loudly rather
  # than reporting a clean bill of health.
  if [ "$n_expected" -ne "$n_actual" ]; then
    printf '%s[%s] package COUNT changed:%s manifest has %d, %s has %d\n' \
      "$c_y" "$label" "$c_n" "$n_expected" "$source_desc" "$n_actual" >&2
    fail=1; bad=1
  fi

  if [ -n "$added" ] || [ -n "$removed" ]; then
    printf '%s✗ [%s] the package set does not match %s%s\n' \
      "$c_r" "$label" "${manifest#"$ROOT/"}" "$c_n" >&2
    [ -n "$added" ]   && { echo "" >&2; echo "  ADDED (in $source_desc, not in the manifest):" >&2;   printf '%s\n' "$added"   | sed 's/^/    + /' >&2; }
    [ -n "$removed" ] && { echo "" >&2; echo "  REMOVED (in the manifest, not in $source_desc):" >&2; printf '%s\n' "$removed" | sed 's/^/    - /' >&2; }
    echo "" >&2
    fail=1
    return
  fi

  if [ "$bad" -eq 0 ]; then
    printf '%s✓ [%s] matches the manifest (%d packages)%s\n' "$c_g" "$label" "$n_actual" "$c_n"
  fi
}

compare_set "Dockerfile" "$MANIFEST"       "$ACTUAL"       "the Dockerfile"
compare_set "build.sh"   "$BUILD_MANIFEST" "$ACTUAL_BUILD" "build.sh"

if [ "$fail" -ne 0 ]; then
  cat >&2 <<'EOF'
Every package in these lists is transitive closure, CVE surface and boot time on
someone's hardware — and a package that silently LEAVES a list is a feature that
silently stops existing on that target. If the change is intended, run:

    scripts/check-image-packages.sh --update

and commit the manifest diff alongside the source change, so it is visible in
review instead of arriving silently.
EOF
  exit 1
fi
