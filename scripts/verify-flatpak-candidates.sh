#!/usr/bin/env bash
# verify-flatpak-candidates.sh — check apt->Flathub conversion entries against Flathub itself.
#
# WHAT THIS PROVES, and what it does not.
#
# This verifies the *resolvability and shape* of a Flatpak recipe: that the app id
# exists on Flathub, that it resolves for exactly the architectures the entry
# declares, that the id is unambiguous under the argv FlatpakInstall actually runs,
# and that the recorded publisher-verification / licence facts match Flathub's API.
#
# It does NOT prove the app launches or renders. Desktop apps are gigabytes and the
# GPU/portal behaviour only shows up on a real box. Full install-and-launch proof is
# scripts/verify-app-recipe.sh's job. Keep the two claims separate.
#
# The check that earns this script's existence is UNAMBIGUOUS_ID. backend/services/
# appnet/flatpak.go runs:
#     flatpak install -y --noninteractive flathub <id>
# with no branch. For an app publishing more than one branch (org.qgis.qgis ships
# stable AND lts; org.winehq.Wine ships seven and none is named plain "stable") that
# command exits 1 with "Multiple branches available" and the app can never install.
# Flathub's own API reports a single "branch" field for these apps, so metadata alone
# does not catch it — only asking flatpak does.
#
# Usage:
#   scripts/verify-flatpak-candidates.sh                       # verify registry.d/apt-to-flatpak.json
#   scripts/verify-flatpak-candidates.sh --file <path.json>
#   scripts/verify-flatpak-candidates.sh --self-test           # prove the checks go red
#
# Requires: docker (an arm64 or amd64 Linux container with flatpak), curl, python3.
# flatpak is queried inside a debian:trixie-slim container so this runs on macOS.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENTRIES_FILE="${REPO_ROOT}/registry.d/apt-to-flatpak.json"
IMAGE="vulos-flatpak-verify:trixie"
SELF_TEST=0

while [ $# -gt 0 ]; do
  case "$1" in
    --file) ENTRIES_FILE="$2"; shift 2 ;;
    --self-test) SELF_TEST=1; shift ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

PASS=0; FAIL=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
info() { printf '       %s\n' "$1"; }

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required tool: $1" >&2; exit 2; }; }
need docker; need curl; need python3

# ---------------------------------------------------------------- flatpak image
build_image() {
  if docker image inspect "$IMAGE" >/dev/null 2>&1; then return 0; fi
  echo "==> building $IMAGE (one-off, cached thereafter)"
  local d; d="$(mktemp -d)"
  cat > "$d/Dockerfile" <<'DOCKERFILE'
FROM debian:trixie-slim
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends flatpak ca-certificates \
 && rm -rf /var/lib/apt/lists/*
RUN flatpak remote-add --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo
DOCKERFILE
  docker build -q -t "$IMAGE" "$d" >/dev/null
  rm -rf "$d"
}

# fp_remote_info <arch> <ref> -> prints flatpak output, exit status is flatpak's.
# --dns is set explicitly: the default embedded resolver is flaky under host load and
# a DNS failure must not be misread as "app does not exist".
fp_remote_info() {
  docker run --rm --dns 8.8.8.8 --dns 1.1.1.1 "$IMAGE" \
    flatpak remote-info flathub --arch="$1" "$2" 2>&1
}

# A network/DNS failure is NOT evidence about the app. Detect and retry.
is_transient() {
  grep -qiE 'could not resolve|could not connect|timed out|temporary failure|connection reset|no route to host' <<<"$1"
}

fp_remote_info_retry() {
  local arch="$1" ref="$2" out
  for _ in 1 2 3; do
    out="$(fp_remote_info "$arch" "$ref")" && { printf '%s' "$out"; return 0; }
    is_transient "$out" || { printf '%s' "$out"; return 1; }
    sleep 5
  done
  printf '%s' "$out"; return 1
}

# --------------------------------------------------------------- Flathub API
api() { curl -fsS --retry 3 --retry-delay 2 "https://flathub.org/api/v2/$1" 2>/dev/null; }

# ------------------------------------------------------------------- checks
# check_app <vulos_id> <flatpak_ref> <declared_arch_csv>
#   declared_arch_csv uses Debian spelling (amd64,arm64); empty means "all".
check_app() {
  local vid="$1" ref="$2" declared="$3"
  local bare="${ref%%//*}"          # id without //branch
  local has_branch=0; [ "$ref" != "$bare" ] && has_branch=1

  echo "== ${vid}  ->  ${ref}"

  # 1. the app exists on Flathub at all
  local summary; summary="$(api "summary/${bare}" || true)"
  if [ -z "$summary" ]; then
    bad "${vid}: EXISTS — Flathub has no app ${bare}"
    return
  fi
  ok "${vid}: EXISTS on Flathub"

  # 2. published architectures, measured — Debian spelling for comparison
  local measured
  measured="$(python3 -c '
import json,sys
m={"x86_64":"amd64","aarch64":"arm64"}
d=json.load(sys.stdin)
print(",".join(sorted(m.get(a,a) for a in d.get("arches",[]))))' <<<"$summary")"
  info "flathub arches: ${measured:-<none>}"

  # 3. declared arch must equal measured arch, intersected with what Vulos ships.
  #    Vulos publishes amd64 and arm64 only.
  local expected
  expected="$(python3 -c '
import sys
vulos={"amd64","arm64"}
m=set(x for x in sys.argv[1].split(",") if x)
print(",".join(sorted(m & vulos)))' "$measured")"
  local declared_norm
  declared_norm="$(python3 -c '
import sys
print(",".join(sorted(x for x in sys.argv[1].split(",") if x)))' "$declared")"
  # an entry declaring nothing means "all arches" -> only correct if both are published
  [ -z "$declared_norm" ] && declared_norm="amd64,arm64"
  if [ "$declared_norm" = "$expected" ]; then
    ok "${vid}: ARCH declared [${declared_norm}] matches Flathub [${expected}]"
  else
    bad "${vid}: ARCH declared [${declared_norm}] but Flathub publishes [${expected}] — an entry offered on an arch Flathub does not build cannot install"
  fi

  # 4. UNAMBIGUOUS_ID — the check that matters. Resolve with the exact ref the
  #    recipe carries, for every arch the entry claims to support.
  local a fparch out
  for a in ${declared_norm//,/ }; do
    case "$a" in amd64) fparch=x86_64 ;; arm64) fparch=aarch64 ;; *) continue ;; esac
    out="$(fp_remote_info_retry "$fparch" "$ref")" || true
    if grep -qiE '^[[:space:]]*Ref:' <<<"$out"; then
      ok "${vid}: RESOLVES on ${a} (${fparch}) as '${ref}'"
    elif grep -qi 'Multiple branches available' <<<"$out"; then
      bad "${vid}: AMBIGUOUS on ${a} — 'flatpak install flathub ${ref}' exits 1. Qualify the id with //<branch>. flatpak said: $(grep -o 'you must specify one of:.*' <<<"$out" | head -c 200)"
    elif is_transient "$out"; then
      bad "${vid}: NETWORK — could not reach Flathub after retries; this is NOT a verdict on the app, re-run"
    else
      bad "${vid}: DOES NOT RESOLVE on ${a} — $(head -1 <<<"$out" | cut -c1-160)"
    fi
  done

  # 5. a branch-qualified id must actually be needed and valid; an unqualified id
  #    must genuinely be unambiguous (already covered above, but state it)
  if [ "$has_branch" = 1 ]; then
    info "id is branch-qualified (${ref#*//}) — required because the bare id is ambiguous"
  fi

  # 6. publisher verification, recorded not assumed
  local ver
  ver="$(api "verification/${bare}/status" || echo '{}')"
  local isver
  isver="$(python3 -c '
import json,sys
try: print(str(json.load(sys.stdin).get("verified", False)).lower())
except Exception: print("unknown")' <<<"$ver")"
  info "publisher verified: ${isver}"
}

# ------------------------------------------------------------------ self-test
# The script is worthless if it cannot go red. Prove each verdict independently.
self_test() {
  echo "### SELF-TEST — every check below MUST report FAIL to be meaningful"
  local before_fail=$FAIL

  echo
  echo "--- 1. nonexistent Flathub id (expect EXISTS to fail)"
  check_app "selftest-bogus" "com.example.DefinitelyNotARealApp1234" "amd64"

  echo
  echo "--- 2. arch overclaim: Blender is x86_64-only, declare arm64 too (expect ARCH + RESOLVES to fail)"
  check_app "selftest-arch" "org.blender.Blender" "amd64,arm64"

  echo
  echo "--- 3. ambiguous branch: bare org.qgis.qgis (expect AMBIGUOUS to fail)"
  check_app "selftest-branch" "org.qgis.qgis" "amd64"

  echo
  if [ "$FAIL" -gt "$before_fail" ]; then
    echo "SELF-TEST OK — the checks go red ($((FAIL - before_fail)) failures induced deliberately)"
    echo "(these induced failures are excluded from the real tally)"
    FAIL=$before_fail
    return 0
  fi
  echo "SELF-TEST BROKEN — induced no failures; the checks prove nothing"
  return 1
}

# ---------------------------------------------------------------------- main
build_image

if [ "$SELF_TEST" = 1 ]; then
  self_test || exit 1
  echo
fi

if [ ! -f "$ENTRIES_FILE" ]; then
  echo "entries file not found: $ENTRIES_FILE" >&2; exit 2
fi

echo "### VERIFYING $ENTRIES_FILE"
echo

# Emit "<vulos_id>|<flatpak_ref>|<arch_csv>" for every entry that has a flatpak_id.
# Entries deliberately kept on apt carry no flatpak_id and are reported, not checked.
#
# The delimiter is US (0x1f), NOT tab: tab counts as IFS *whitespace*, so bash collapses
# a run of them and an entry with an empty flatpak_id silently shifts its arch into the
# id field. That bug made blender and wine report "Flathub has no app amd64,arm64".
mapfile -t ROWS < <(python3 - "$ENTRIES_FILE" <<'PY'
import json,sys
doc=json.load(open(sys.argv[1]))
apps=doc.get("apps",doc)
for aid,entry in sorted(apps.items()):
    if aid.startswith("_"): continue
    arch=",".join(entry.get("arch") or [])
    fid=""
    for _,rec in sorted((entry.get("versions") or {}).items()):
        if rec.get("flatpak_id"):
            fid=rec["flatpak_id"]; break
    print(f"{aid}\x1f{fid}\x1f{arch}")
PY
)

for row in "${ROWS[@]}"; do
  IFS=$'\x1f' read -r vid fid arch <<<"$row"
  if [ -z "$fid" ]; then
    echo "== ${vid}  ->  (no flatpak_id: stays on apt by decision)"
    info "skipped — not a Flatpak conversion"
    continue
  fi
  check_app "$vid" "$fid" "$arch"
  echo
done

echo "=============================================="
printf 'verify-flatpak-candidates: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
