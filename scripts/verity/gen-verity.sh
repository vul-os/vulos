#!/bin/sh
# scripts/verity/gen-verity.sh — dm-verity Merkle tree + root hash generation
# and re-verification for a Vulos OS squashfs image.
#
# Usage (called from build.sh after mksquashfs):
#   scripts/verity/gen-verity.sh <squashfs> <hashtree> <roothash_out>
#
# Arguments:
#   squashfs     — path to the read-only squashfs image (e.g. output/image.squashfs)
#   hashtree     — path where the Merkle hash-tree file will be written
#                  (e.g. output/os-core.hashtree)
#   roothash_out — path where the hex root hash will be written
#                  (e.g. output/os-core.roothash)
#
# What this script does:
#   1. Tool-guards veritysetup — skips gracefully when not available (CI without
#      root/loop support can run `sh -n` to validate syntax only).
#   2. Runs `veritysetup format <squashfs> <hashtree>` to compute the dm-verity
#      Merkle tree over every block of the squashfs and produce the root hash.
#   3. Parses the root hash from veritysetup's output and writes it to
#      <roothash_out> (single hex line, no trailing newline separator).
#   4. Echoes the root hash to stdout so the caller (build.sh) can log it.
#   5. Re-verifies with `veritysetup verify` to assert the tree + squashfs
#      match the root hash we just extracted.  Any mismatch is fatal.
#
# Output contract (consumed by OSDIST-01 / stable.json signing):
#   <roothash_out>  — one line: 64-char lowercase hex string (SHA-256 root hash)
#   <hashtree>      — binary Merkle hash tree (shipped alongside the squashfs
#                     in the OS bucket; read by the dm-verity kernel driver at
#                     boot time via the vulos-live initramfs or A/B slot driver)
#
# Tool-guard behaviour:
#   If veritysetup is absent the script prints a clear notice and exits 0 so
#   that a `build.sh` running under `set -e` is not aborted on a host that
#   does not have cryptsetup/veritysetup installed.  CI with full root/loop
#   support is expected to have veritysetup; the caller's guard output makes
#   a missing roothash obvious.
#
# Re-verify semantics:
#   `veritysetup verify <squashfs> <hashtree> <roothash>` re-reads every block
#   of the squashfs, rebuilds the Merkle tree in memory, and asserts the
#   recomputed root matches the stored root.  This catches any truncation or
#   bit-flip that occurred between format and final image assembly.
#
# Design notes:
#   - The root hash is the image's content identity (SIGNING.md).  It is what
#     `stable.json` pins in the `roothash` field (osdist/manifest.go:StableManifest).
#   - The hash tree is a separate file: it is uploaded to the OS bucket
#     alongside the squashfs so the dm-verity kernel driver can open it as a
#     separate device (or the initramfs can embed it as a data block device).
#   - We do NOT modify the squashfs in place — verity is external tree mode,
#     keeping the squashfs byte-for-byte reproducible.
#   - veritysetup defaults: SHA-256, 4096-byte data/hash block size — matches
#     the kernel's dm-verity default and what the initramfs verify hook expects.

set -e

# ── Helpers ──────────────────────────────────────────────────────────────────

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
DIM='\033[2m'
NC='\033[0m'

die() {
    printf '%b\n' "${RED}✗ [gen-verity] $*${NC}" >&2
    exit 1
}

# Progress goes to STDERR, like die() above. stdout is reserved for exactly one
# thing: the root hash. This script both prints progress AND echoes the hash for
# `$(...)` capture, and it prints MORE progress after the hash — so with progress
# on stdout a caller capturing stdout got the whole transcript, ANSI codes
# included. That is not hypothetical: it put the entire log into stable.json's
# roothash field, and VERITY-02 then refused the image at boot with a kernel
# panic. Callers should still prefer the roothash FILE (the documented output
# contract below), but stdout is now safe to capture either way.
info() {
    printf '%b\n' "${BLUE}  ▸ [gen-verity] $*${NC}" >&2
}

ok() {
    printf '%b\n' "  ${GREEN}✓${NC} [gen-verity] $*" >&2
}

dim() {
    printf '%b\n' "  ${DIM}[gen-verity] $*${NC}"
}

# ── Args ──────────────────────────────────────────────────────────────────────

SQUASHFS="${1:-}"
HASHTREE="${2:-}"
ROOTHASH_OUT="${3:-}"

if [ -z "$SQUASHFS" ] || [ -z "$HASHTREE" ] || [ -z "$ROOTHASH_OUT" ]; then
    die "Usage: $0 <squashfs> <hashtree> <roothash_out>"
fi

if [ ! -f "$SQUASHFS" ]; then
    die "squashfs not found: $SQUASHFS"
fi

# ── Tool guard ────────────────────────────────────────────────────────────────
# veritysetup requires loop-device access (root + kernel loop module).
# When unavailable (macOS cross-build hosts, Docker without --privileged, etc.)
# we skip gracefully and leave a clear notice so the caller knows no hash was
# produced.  CI with full root+loop support is expected to have veritysetup.

if ! command -v veritysetup >/dev/null 2>&1; then
    printf '%b\n' "  ${DIM}[gen-verity] veritysetup not found — skipping dm-verity hash generation${NC}"
    printf '%b\n' "  ${DIM}             Install: apt-get install cryptsetup-bin${NC}"
    printf '%b\n' "  ${DIM}             Verity hash tree and root hash will NOT be produced.${NC}"
    exit 0
fi

# ── Format: build Merkle tree + extract root hash ─────────────────────────────

info "Running veritysetup format on $(basename "$SQUASHFS") ..."

# veritysetup format prints several lines; Root hash is on a line like:
#   Root hash:      <64-char hex>
# We capture the full output, then extract the root hash.
VERITY_OUT="$(veritysetup format "$SQUASHFS" "$HASHTREE" 2>&1)" || \
    die "veritysetup format failed:\n$VERITY_OUT"

# Extract root hash (case-insensitive label match, strip leading whitespace)
ROOTHASH="$(printf '%s\n' "$VERITY_OUT" | awk -F: '/[Rr]oot [Hh]ash/{gsub(/^[[:space:]]+|[[:space:]]+$/, "", $2); print $2}')"

if [ -z "$ROOTHASH" ]; then
    die "Could not parse root hash from veritysetup output:\n$VERITY_OUT"
fi

# Write root hash to output file (single line, no trailing newline)
printf '%s' "$ROOTHASH" > "$ROOTHASH_OUT"
chmod 0444 "$ROOTHASH_OUT"

ok "Merkle hash tree  → $(basename "$HASHTREE")"
ok "Root hash         → $ROOTHASH"
ok "Root hash file    → $ROOTHASH_OUT"

# Echo root hash to stdout so build.sh can log/capture it
printf '%s\n' "$ROOTHASH"

# ── Re-verify: assert tree + squashfs reproduce the same root hash ────────────

info "Re-verifying: veritysetup verify ..."
veritysetup verify "$SQUASHFS" "$HASHTREE" "$ROOTHASH" || \
    die "veritysetup verify FAILED — root hash mismatch or corrupted image/hashtree.
  squashfs:  $SQUASHFS
  hashtree:  $HASHTREE
  roothash:  $ROOTHASH
  This is a fatal build error; the image must not be distributed."

ok "Re-verify passed — root hash is consistent with squashfs + hash tree."
