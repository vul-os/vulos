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

# ── Step 3: installer storage-mode gate ───────────────────────────────────────
# Runs install-vulos.sh's dry-run mode-selection path for real (scratch config
# dir, nothing installed). Fails closed with a coverage assertion.
run_step "storage-mode selection gate" \
  bash "$ROOT/scripts/test-storage-mode.sh"

# ── Step 3b: vulos-live initramfs hook cmdline-parsing gate ───────────────────
# Sources the real vulos.squashfs=/vulos.live cmdline-parsing logic out of
# scripts/initramfs/vulos-live (no kernel needed) and pins it against the
# exact strings the two installer paths write. See scripts/netboot-install-
# smoke.sh for the real-kernel/QEMU exercise of the rest of that hook.
run_step "vulos-live cmdline gate" \
  bash "$ROOT/scripts/test-vulos-live-cmdline.sh"

# ── Step 4: frontend build ────────────────────────────────────────────────────
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
