#!/usr/bin/env bash
# test-all.sh — run all Vulos OSS tests: unit (with -race), seeded e2e, and
# frontend build.
#
# Usage:
#   ./scripts/test-all.sh           # full suite
#   SKIP_NPM=1 ./scripts/test-all.sh  # skip npm build (CI without Node)
#
# Exit codes:
#   0 — all steps passed
#   1 — one or more steps failed (summary printed at end)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND="$ROOT/backend"

PASS=()
FAIL=()

run_step() {
  local name="$1"
  shift
  echo ""
  echo "──────────────────────────────────────────────────────────"
  echo "  STEP: $name"
  echo "──────────────────────────────────────────────────────────"
  if "$@"; then
    PASS+=("$name")
    echo "  PASS: $name"
  else
    FAIL+=("$name")
    echo "  FAIL: $name"
  fi
}

# ── Step 1: unit tests with race detector ─────────────────────────────────────
run_step "go test -race ./..." \
  bash -c "cd '$BACKEND' && go test -race -timeout 5m ./..."

# ── Step 2: seeded e2e tests ──────────────────────────────────────────────────
run_step "go test -tags=e2e (firstboot/e2e)" \
  bash -c "cd '$BACKEND' && go test -tags=e2e -timeout 2m -v ./firstboot/e2e/..."

# ── Step 3: frontend build ────────────────────────────────────────────────────
if [[ "${SKIP_NPM:-0}" == "1" ]]; then
  echo ""
  echo "  SKIP: npm run build (SKIP_NPM=1)"
else
  run_step "npm run build" \
    bash -c "cd '$ROOT' && npm run build"
fi

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo "══════════════════════════════════════════════════════════════"
echo "  TEST SUMMARY"
echo "══════════════════════════════════════════════════════════════"
for s in "${PASS[@]+"${PASS[@]}"}"; do
  echo "  PASS  $s"
done
for s in "${FAIL[@]+"${FAIL[@]}"}"; do
  echo "  FAIL  $s"
done

if [[ ${#FAIL[@]} -gt 0 ]]; then
  echo ""
  echo "  ${#FAIL[@]} step(s) failed."
  exit 1
fi

echo ""
echo "  All steps passed."
exit 0
