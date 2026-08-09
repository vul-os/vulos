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

# ── Step 2b: the real server binary, over real HTTP ───────────────────────────
# Compiles cmd/server, runs it on a temp data dir and an ephemeral port, and
# drives first-boot → login → restart → logout against it. Unlike Step 2 (which
# is in-process against mocks) nothing here is stubbed: real middleware order,
# real on-disk stores, real session cookies.
run_step "go test -tags=e2e (real server binary)" \
  bash -c "cd '$BACKEND' && go test -tags=e2e -timeout 5m ./e2e/..."

# ── Step 2c: the relay, as real separate processes ────────────────────────────
# Two real `vulos relay serve` processes and real box agents on loopback: two
# boxes through one relay, four simultaneous tunnels through two, failover when
# a relay is killed mid-flight, a bad token refused, and a world-readable
# config refused. LOCAL ONLY — nothing here touches the cloud relay, which is
# deliberately a separate, human-run exercise.
run_step "relay smoke (real processes, loopback)" \
  bash "$ROOT/scripts/smoke-relay.sh"

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

# ── Step 3c: netboot install → real disk → real QEMU boot ─────────────────────
# Runs the REAL netboot-install Go pipeline against a loop-backed disk inside a
# privileged Linux container, then boots the result in QEMU and asserts a real
# HTTP response from the installed OS. This is the only thing that has ever
# caught the five bugs that each independently made an installed disk
# unbootable — none of them were reachable from a unit test.
#
# NOT in .github/workflows: it needs docker AND qemu-system-aarch64 AND OVMF,
# and hardware acceleration that a hosted runner does not have, so there it
# would only ever take the skip path slowly. --skip-if-unavailable makes that
# skip LOUD and itemised (it names every claim the run did not verify) rather
# than a silent green, which is what makes it safe to have in a suite at all.
run_step "netboot install + QEMU boot" \
  bash "$ROOT/scripts/netboot-install-smoke.sh" --skip-if-unavailable

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
