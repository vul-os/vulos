#!/usr/bin/env bash
# test-storage-mode.sh — gate for the storage-mode selection path of
# install-vulos.sh (D-STORE-LOCAL-DEFAULT).
#
# What it guarantees:
#   1. A NEW install defaults to local, self-contained storage — never to a
#      hosted third-party service.
#   2. An EXISTING install (one that already has storage.yaml) is never
#      repointed, not even by an explicit --storage flag that disagrees.
#   3. Every hosted selection is announced as hosted, with a reason.
#
# How it runs: the installer's dry-run plan is executed for real, once per
# case, against a scratch CONFIG_DIR (VULOS_INSTALL_CONFIG_DIR). Nothing is
# installed and nothing under /etc is read or written.
#
# FAIL-CLOSED CONTRACT — read before editing:
#   * Every case ends in assert_contains/assert_absent; each records itself in
#     ASSERTIONS_RUN.
#   * The run FAILS unless ASSERTIONS_RUN == EXPECTED_ASSERTIONS. A case that
#     silently stops executing (an early return, a renamed flag that makes the
#     installer exit 1, a `set -e` abort) therefore fails the gate instead of
#     passing by doing nothing.
#   * The installer's exit status is checked on every invocation; a dry run
#     that dies is a failure, never a skip.
#   * There is no skip path at all. If bash cannot run the installer, this
#     script exits non-zero.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALLER="${ROOT}/scripts/install-vulos.sh"

# Bump this deliberately when adding/removing assertions. It is the coverage
# assertion: it is what makes "the gate ran but checked nothing" impossible.
EXPECTED_ASSERTIONS=28

ASSERTIONS_RUN=0
FAILURES=0

fail() { printf '  FAIL: %s\n' "$*" >&2; FAILURES=$((FAILURES + 1)); }
pass() { printf '  ok: %s\n' "$*"; }

assert_contains() {
  local haystack="$1" needle="$2" what="$3"
  ASSERTIONS_RUN=$((ASSERTIONS_RUN + 1))
  if [[ "${haystack}" == *"${needle}"* ]]; then
    pass "${what}"
  else
    fail "${what} — expected to find: ${needle}"
    printf '  ---- actual output ----\n%s\n  -----------------------\n' "${haystack}" >&2
  fi
}

assert_absent() {
  local haystack="$1" needle="$2" what="$3"
  ASSERTIONS_RUN=$((ASSERTIONS_RUN + 1))
  if [[ "${haystack}" != *"${needle}"* ]]; then
    pass "${what}"
  else
    fail "${what} — did NOT expect to find: ${needle}"
    printf '  ---- actual output ----\n%s\n  -----------------------\n' "${haystack}" >&2
  fi
}

# run_plan <scratch-config-dir> [installer args...] → prints the dry-run plan.
# A non-zero exit from the installer aborts the whole gate (fail closed).
run_plan() {
  local cfgdir="$1"; shift
  local out status
  set +e
  out="$(VULOS_INSTALL_CONFIG_DIR="${cfgdir}" bash "${INSTALLER}" --dry-run "$@" 2>&1)"
  status=$?
  set -e
  if [ "${status}" -ne 0 ]; then
    printf 'FATAL: installer dry-run exited %d for args: %s\n%s\n' "${status}" "$*" "${out}" >&2
    exit 1
  fi
  if [ -z "${out}" ]; then
    printf 'FATAL: installer dry-run produced no output for args: %s\n' "$*" >&2
    exit 1
  fi
  printf '%s' "${out}"
}

# seed_config <dir> <backend> — write a pre-existing storage.yaml.
seed_config() {
  mkdir -p "$1"
  printf '# pre-existing install\nbackend: "%s"\n' "$2" > "$1/storage.yaml"
}

[ -f "${INSTALLER}" ] || { echo "FATAL: installer not found at ${INSTALLER}" >&2; exit 1; }
[ -s "${INSTALLER}" ] || { echo "FATAL: installer is empty: ${INSTALLER}" >&2; exit 1; }

TMPROOT="$(mktemp -d)"
trap 'rm -rf "${TMPROOT}"' EXIT

echo "== install-vulos.sh storage-mode gate =="

# ── Case 1: new install, no flags → local, self-contained ─────────────────────
echo "-- new install (no flags)"
OUT="$(run_plan "${TMPROOT}/fresh")"
assert_contains "${OUT}" "Storage mode:   local-fs  [default]" "new install selects local-fs by default"
assert_contains "${OUT}" "self-hosted"                          "new install is announced as self-hosted"
assert_absent   "${OUT}" "backend: tigris"                      "new install does not select tigris"
assert_absent   "${OUT}" "Storage mode:   tigris"               "new install does not select a hosted backend"
assert_contains "${OUT}" "Why: new install"                     "new install states WHY it chose this mode"

# ── Case 2: explicit hosted opt-in ────────────────────────────────────────────
echo "-- --storage=tigris (hosted opt-in)"
OUT="$(run_plan "${TMPROOT}/tigris" --storage=tigris)"
assert_contains "${OUT}" "Storage mode:   tigris  [flag]"   "explicit --storage=tigris is honoured"
assert_contains "${OUT}" "HOSTED"                           "hosted selection is labelled HOSTED"
assert_contains "${OUT}" "tigrisdata.com"                   "hosted selection names the third party"
assert_contains "${OUT}" "OPT-IN"                           "hosted selection is described as an opt-in"

# ── Case 3: co-located MinIO ──────────────────────────────────────────────────
echo "-- --storage=minio"
OUT="$(run_plan "${TMPROOT}/minio" --storage=minio)"
assert_contains "${OUT}" "Storage mode:   minio  [flag]" "explicit --storage=minio is honoured"
assert_contains "${OUT}" "self-hosted"                   "MinIO is announced as self-hosted"

# ── Case 4: the legacy --storage=local alias still means MinIO ────────────────
echo "-- --storage=local (legacy alias)"
OUT="$(run_plan "${TMPROOT}/localalias" --storage=local)"
assert_contains "${OUT}" "Storage mode:   minio  [flag]" "legacy --storage=local still means MinIO"

# ── Case 5: explicit local-fs ─────────────────────────────────────────────────
echo "-- --storage=local-fs"
OUT="$(run_plan "${TMPROOT}/localfs" --storage=local-fs)"
assert_contains "${OUT}" "Storage mode:   local-fs  [flag]" "explicit --storage=local-fs is honoured"

# ── Case 6: EXISTING tigris install, no flag → untouched ──────────────────────
echo "-- existing install on tigris, re-run with no flags"
seed_config "${TMPROOT}/existing-tigris" tigris
OUT="$(run_plan "${TMPROOT}/existing-tigris")"
assert_contains "${OUT}" "Storage mode:   tigris  [existing config]" "existing tigris install is NOT moved to local"
assert_contains "${OUT}" "kept exactly as it is"                     "existing install says it is kept as-is"

# ── Case 7: EXISTING tigris install + a conflicting flag → still untouched ────
echo "-- existing install on tigris, re-run with --storage=local-fs"
OUT="$(run_plan "${TMPROOT}/existing-tigris" --storage=local-fs)"
assert_contains "${OUT}" "Storage mode:   tigris  [existing config]" "conflicting flag does not repoint an existing install"
assert_contains "${OUT}" "never repointed"                           "the conflict is reported loudly"
assert_contains "${OUT}" "data is never migrated"                    "the conflict warning states data is not migrated"

# ── Case 8: EXISTING minio install ────────────────────────────────────────────
echo "-- existing install on minio"
seed_config "${TMPROOT}/existing-minio" minio
OUT="$(run_plan "${TMPROOT}/existing-minio")"
assert_contains "${OUT}" "Storage mode:   minio  [existing config]" "existing minio install is preserved"
assert_contains "${OUT}" "minio          — local S3-compatible"     "existing minio install keeps its MinIO service"

# ── Case 9: EXISTING local install ────────────────────────────────────────────
echo "-- existing install on local"
seed_config "${TMPROOT}/existing-local" local
OUT="$(run_plan "${TMPROOT}/existing-local")"
assert_contains "${OUT}" "Storage mode:   local-fs  [existing config]" "existing local install is preserved"

# ── Case 10: EXISTING install on a backend this installer doesn't know ────────
echo "-- existing install on an unrecognised backend"
seed_config "${TMPROOT}/existing-weird" wasabi
OUT="$(run_plan "${TMPROOT}/existing-weird")"
assert_contains "${OUT}" "Storage mode:   wasabi  [existing config]" "unknown existing backend is preserved verbatim"
assert_contains "${OUT}" "unrecognised backend"                      "unknown existing backend is reported"

# ── Case 11: the shipped source itself ────────────────────────────────────────
# Guards the decision at rest: someone flipping the literal default back to a
# hosted service fails here even if every dry run above still passes.
echo "-- shipped defaults in the installer source"
SRC="$(cat "${INSTALLER}")"
assert_contains "${SRC}" 'STORAGE_MODE="local-fs"' "shipped default STORAGE_MODE is local-fs"
assert_absent   "${SRC}" 'STORAGE_MODE="tigris"
'                                                  "no hosted backend is assigned as a shipped default"
assert_contains "${SRC}" 'STORAGE_MODE_SOURCE="default"' "the shipped default records its provenance"

# ── Case 12: the Go side agrees with the installer ────────────────────────────
# The OS-side selector and the installer must not disagree about what the
# default is; a split would mean the dashboard says one thing and the box does
# another.
echo "-- backend selector default"
GOSRC="$(cat "${ROOT}/backend/internal/storagemode/storagemode.go")"
assert_contains "${GOSRC}" "const DefaultMode = ModeLocalFS"           "backend DefaultMode is the local mode"
assert_contains "${GOSRC}" "const LegacyDefaultMode = ModeCentralTigris" "backend keeps a legacy pin for existing installs"

# ── Coverage assertion + verdict ──────────────────────────────────────────────
echo ""
echo "assertions run: ${ASSERTIONS_RUN} (expected ${EXPECTED_ASSERTIONS})"
if [ "${ASSERTIONS_RUN}" -ne "${EXPECTED_ASSERTIONS}" ]; then
  printf 'FAIL: coverage assertion — %d assertions ran, expected %d.\n' \
    "${ASSERTIONS_RUN}" "${EXPECTED_ASSERTIONS}" >&2
  printf '      Cases were skipped, or EXPECTED_ASSERTIONS was not updated with intent.\n' >&2
  exit 1
fi
if [ "${FAILURES}" -ne 0 ]; then
  printf 'FAIL: %d assertion(s) failed.\n' "${FAILURES}" >&2
  exit 1
fi
echo "PASS: storage-mode selection gate (${ASSERTIONS_RUN} assertions)"
