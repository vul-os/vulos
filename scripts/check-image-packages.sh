#!/usr/bin/env bash
# check-image-packages.sh — the OS image's apt package set is a deliberate list.
#
# The image installs two display stacks and two audio servers. That is currently
# intentional (see the long comment in Dockerfile), but it got there partly by
# accretion: `labwc cage` and `cage labwc` sat two lines apart in the same
# apt-get invocation, which is what a block nobody reads as a whole looks like.
#
# This pins the top-level set so a new dependency has to be a deliberate diff
# rather than a side effect of someone adding one line to an install list.
#
# WHY IT ASSERTS THE COUNT, NOT JUST MEMBERSHIP: a check that only verifies the
# expected entries are present passes happily when twenty extra ones show up,
# which is exactly the direction this drifts. Both directions fail here.
#
# Usage:
#   scripts/check-image-packages.sh            # verify against the manifest
#   scripts/check-image-packages.sh --update   # rewrite the manifest from the
#                                              # Dockerfile, for a reviewed diff
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DOCKERFILE="$ROOT/Dockerfile"
MANIFEST="$ROOT/scripts/image-packages.txt"

c_r=$'\033[0;31m'; c_g=$'\033[0;32m'; c_y=$'\033[0;33m'; c_n=$'\033[0m'

# Extract the top-level packages from every `apt-get install` block: take the
# lines of each continued command and drop flags, the command words, and any
# shell operators. This reads the Dockerfile rather than a built image on
# purpose — it runs in a second, needs no Docker, and catches the mistake at the
# point it is made.
extract_packages() {
  awk '
    # Start collecting at `apt-get install`, and STOP at the first `&&`.
    # Without that stop this swept up arguments to whatever was chained after
    # the install — `dpkg --add-architecture amd64` contributed "amd64", and
    # `flatpak remote-add ... flathub` contributed "remote-add" and "flathub".
    # A manifest full of non-packages is worse than none: it makes the diff
    # noisy enough that nobody reads it, which is the failure this gate exists
    # to prevent.
    /apt-get[[:space:]]+install/ { inblock = 1; seen_install = 0 }
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
      if ($0 !~ /\\$/) inblock = 0
    }
  ' "$DOCKERFILE" | sort -u
}

ACTUAL="$(extract_packages)"

if [ "${1:-}" = "--update" ]; then
  printf '%s\n' "$ACTUAL" > "$MANIFEST"
  echo "${c_g}manifest rewritten:${c_n} $MANIFEST ($(printf '%s\n' "$ACTUAL" | wc -l | tr -d ' ') packages)"
  echo "Review the diff before committing — that review IS the gate."
  exit 0
fi

if [ ! -f "$MANIFEST" ]; then
  echo "${c_r}FATAL:${c_n} no manifest at $MANIFEST — run: $0 --update" >&2
  exit 1
fi

EXPECTED="$(sort -u "$MANIFEST")"

ADDED="$(comm -13 <(printf '%s\n' "$EXPECTED") <(printf '%s\n' "$ACTUAL") || true)"
REMOVED="$(comm -23 <(printf '%s\n' "$EXPECTED") <(printf '%s\n' "$ACTUAL") || true)"

n_expected=$(printf '%s\n' "$EXPECTED" | grep -c . || true)
n_actual=$(printf '%s\n' "$ACTUAL" | grep -c . || true)

# The count is asserted separately and FIRST, so a manifest that somehow agrees
# on membership while disagreeing on size still fails loudly rather than
# reporting a clean bill of health.
if [ "$n_expected" -ne "$n_actual" ]; then
  printf '%sPackage COUNT changed:%s manifest has %d, Dockerfile has %d\n' "$c_y" "$c_n" "$n_expected" "$n_actual" >&2
fi

if [ -n "$ADDED" ] || [ -n "$REMOVED" ]; then
  printf '%s✗ the image package set does not match scripts/image-packages.txt%s\n' "$c_r" "$c_n" >&2
  [ -n "$ADDED" ]   && { echo "" >&2; echo "  ADDED (in Dockerfile, not in the manifest):" >&2; printf '%s\n' "$ADDED"   | sed 's/^/    + /' >&2; }
  [ -n "$REMOVED" ] && { echo "" >&2; echo "  REMOVED (in the manifest, not in the Dockerfile):" >&2; printf '%s\n' "$REMOVED" | sed 's/^/    - /' >&2; }
  cat >&2 <<'EOF'

Every package in this image is transitive closure, CVE surface and boot time on
someone's hardware. If the change is intended, run:

    scripts/check-image-packages.sh --update

and commit the manifest diff alongside the Dockerfile change, so the addition is
visible in review instead of arriving silently.
EOF
  exit 1
fi

printf '%s✓ image package set matches the manifest (%d packages)%s\n' "$c_g" "$n_actual" "$c_n"
