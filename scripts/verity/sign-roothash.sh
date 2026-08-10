#!/bin/sh
# scripts/verity/sign-roothash.sh — bind a dm-verity root hash to the RELEASE KEY.
#
# Usage:
#   scripts/verity/sign-roothash.sh <squashfs> <roothash_file> <release_priv> \
#                                   <release_cert> <bundle_out> [key_id] [artifact_path]
#
# This is the OFFLINE CEREMONY.  It is the only step in the verity chain that
# touches a private key, and by design it does not run on a build machine or a
# CI runner — see "Where this runs" below.
#
# ── What problem this closes ─────────────────────────────────────────────────
#
# gen-verity.sh produces os-core.hashtree + os-core.roothash, and dm-verity then
# binds every block of the image to that root hash at runtime.  What binds the
# ROOT HASH to anyone?  Nothing — until this script runs.  An attacker who can
# substitute the squashfs AND the roothash together defeats dm-verity entirely:
# the kernel verifies the attacker's image against the attacker's tree and
# reports success.  The root hash is a security statement only once it is signed.
#
# ── What it produces ─────────────────────────────────────────────────────────
#
# One file, <bundle_out>, conventionally named "<roothash_file>.sig" because
# that is the name the installer stages into a slot (roothashSigDestName,
# backend/services/installer/netboot_verity.go) and the name
# scripts/initramfs/vulos-live derives from the squashfs it is booting:
#
#   vulos-sig-v1
#   algorithm: ed25519
#   key-id: <key_id>
#   sig: <base64 Ed25519 signature over canonical(ImagePayload)>
#   payload: <base64 of the ImagePayload JSON document>
#
# The first four lines are BYTE-FOR-BYTE the output of `cmd/sign sign-image` —
# this script does not implement any cryptography, it runs that command.  The
# signed bytes are canonical(ImagePayload) and nothing else: the same surface the
# netboot installer (c1fba1b0), the OTA updater (7d1101af) and cmd/init verify.
#
# The fifth line is TRANSPORT, not a new signing surface.  An ImagePayload
# signature covers a name, a size and a root hash; it cannot be checked without
# the payload it was made over, which is exactly why c1fba1b0 made stable.json
# mandatory beside stable.json.sig.  The initramfs has nowhere to put a second
# file — the installer stages exactly three names into a slot and that code is
# not ours to change — so the manifest rides in the signature file.
# signing.ParseSig ignores keyed lines it does not recognise, so every existing
# reader parses this file unchanged and a bare sign-image .sig is a valid prefix.
#
# ── Where this runs ──────────────────────────────────────────────────────────
#
# On the machine that holds the release private key, which is NOT a build
# machine.  .github/workflows/release.yml publishes os-core.roothash as a
# release asset precisely so a human can sign it offline afterwards ("The
# release private key is never on a CI runner (see docs/KEY-CEREMONY.md), so
# signing is a deliberate offline step").  This script is that step, made into
# one command instead of a sign-image invocation plus a hand-assembled file.
#
# build.sh calls it automatically ONLY when a release private key is already
# present on the machine — a developer or smoke-harness build using
# keys/release.priv.json.  A CI build has no key, emits an unsigned roothash,
# and says so.  It cannot and must not sign.
#
# ── Fail-closed ──────────────────────────────────────────────────────────────
#
# Every failure is fatal.  There is no "emit an unsigned bundle" path: a bundle
# that exists is a bundle the initramfs will hold to the full standard, and half
# of one is worse than none.

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

die() {
    printf '%b\n' "${RED}✗ [sign-roothash] $*${NC}" >&2
    exit 1
}
info() { printf '%b\n' "${BLUE}  ▸ [sign-roothash] $*${NC}" >&2; }
ok()   { printf '%b\n' "  ${GREEN}✓${NC} [sign-roothash] $*" >&2; }

SQUASHFS="${1:-}"
ROOTHASH_FILE="${2:-}"
RELEASE_PRIV="${3:-}"
RELEASE_CERT="${4:-}"
BUNDLE_OUT="${5:-}"
KEY_ID="${6:-}"
ARTIFACT_PATH="${7:-}"

if [ -z "$SQUASHFS" ] || [ -z "$ROOTHASH_FILE" ] || [ -z "$RELEASE_PRIV" ] \
        || [ -z "$RELEASE_CERT" ] || [ -z "$BUNDLE_OUT" ]; then
    die "Usage: $0 <squashfs> <roothash_file> <release_priv> <release_cert> <bundle_out> [key_id] [artifact_path]"
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

[ -f "$SQUASHFS" ]      || die "squashfs not found: $SQUASHFS"
[ -f "$ROOTHASH_FILE" ] || die "roothash file not found: $ROOTHASH_FILE"
[ -f "$RELEASE_PRIV" ]  || die "release private key not found: $RELEASE_PRIV"
[ -f "$RELEASE_CERT" ]  || die "release cert not found: $RELEASE_CERT"

# ── The root hash, shape-asserted ────────────────────────────────────────────
# A dm-verity root hash is 64 lowercase hex characters.  Signing anything else
# produces a bundle that can never match the file veritysetup is handed, which
# surfaces as a boot halt that reads like tampering.  This project has already
# shipped a roothash field containing an ANSI-coloured build transcript once.
ROOTHASH="$(tr -d ' \t\r\n' < "$ROOTHASH_FILE")"
case "$ROOTHASH" in
    *[!0-9a-f]* | "") die "roothash in $ROOTHASH_FILE is not lowercase hex: '$ROOTHASH'" ;;
esac
[ "${#ROOTHASH}" -eq 64 ] || die "roothash in $ROOTHASH_FILE is ${#ROOTHASH} chars, want 64"

# ── The release key must be the one the cert certifies ───────────────────────
# Signing with a key the shipped cert does not authorise produces a bundle that
# fails at BOOT rather than at the ceremony — the silent-unbootable-artifact
# failure mode.  build.sh's --disk path already refuses this; so does this.
CERT_PUBKEY="$(grep -o '"release_pubkey"[[:space:]]*:[[:space:]]*"[0-9a-f]*"' "$RELEASE_CERT" 2>/dev/null | grep -o '[0-9a-f]\{64\}' || true)"
PRIV_PUBKEY="$(grep -o '"public_key"[[:space:]]*:[[:space:]]*"[0-9a-f]*"' "$RELEASE_PRIV" 2>/dev/null | grep -o '[0-9a-f]\{64\}' || true)"
if [ -z "$CERT_PUBKEY" ] || [ -z "$PRIV_PUBKEY" ] || [ "$CERT_PUBKEY" != "$PRIV_PUBKEY" ]; then
    die "the release private key does not match the release cert.
  cert ($RELEASE_CERT) authorises: ${CERT_PUBKEY:-<unreadable>}
  key  ($RELEASE_PRIV) public half: ${PRIV_PUBKEY:-<unreadable>}
  A bundle signed with this key would be rejected at boot, not here."
fi

# min_epoch comes from the cert, exactly as build.sh --disk derives it: the
# payload's floor must be one the cert itself can satisfy.
MIN_EPOCH="$(grep -o '"min_epoch"[[:space:]]*:[[:space:]]*[0-9]*' "$RELEASE_CERT" 2>/dev/null | grep -o '[0-9]*$' || true)"
[ -n "$MIN_EPOCH" ] || MIN_EPOCH=0

SIZE="$(wc -c < "$SQUASHFS" | tr -d ' ')"
[ "$SIZE" -gt 0 ] || die "squashfs $SQUASHFS is empty"

[ -n "$KEY_ID" ] || KEY_ID="vulos-roothash-$(date -u +%Y%m%d)"
[ -n "$ARTIFACT_PATH" ] || ARTIFACT_PATH="$(basename "$SQUASHFS")"
RELEASED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# ── Sign: canonical(ImagePayload), release key. No new cryptography here. ────
info "signing root hash $ROOTHASH (size=$SIZE path=$ARTIFACT_PATH min_epoch=$MIN_EPOCH)"
SIG_TMP="${BUNDLE_OUT}.sigpart"
rm -f "$SIG_TMP"
( cd "$REPO_ROOT/backend" && go run ./cmd/sign sign-image \
    -release-priv "$RELEASE_PRIV" \
    -key-id "$KEY_ID" \
    -path "$ARTIFACT_PATH" \
    -roothash "$ROOTHASH" \
    -size "$SIZE" \
    -min-epoch "$MIN_EPOCH" \
    -released-at "$RELEASED_AT" \
    -out "$SIG_TMP" ) >&2 || die "cmd/sign sign-image failed"
[ -s "$SIG_TMP" ] || die "sign-image produced no signature at $SIG_TMP"

# ── Attach the manifest the signature was made over ──────────────────────────
# The JSON is written in the SAME field order build.sh --disk writes
# /etc/vulos/stable.json.  Order does not matter — every verifier parses this
# document and RE-CANONICALISES it before checking the signature — but keeping
# the two spellings identical means a reader comparing them sees one shape.
PAYLOAD_JSON="$(printf '{"path":"%s","roothash":"%s","size":%s,"min_epoch":%s,"released_at":"%s"}' \
    "$ARTIFACT_PATH" "$ROOTHASH" "$SIZE" "$MIN_EPOCH" "$RELEASED_AT")"

# base64 with NO line wrapping: the bundle is a line-oriented format and a
# wrapped blob would be silently truncated at the first newline by every reader.
# `base64 -w0` is GNU-only (absent on macOS/BusyBox), so wrapping is stripped
# instead, which works everywhere.
PAYLOAD_B64="$(printf '%s' "$PAYLOAD_JSON" | base64 | tr -d '\n\r ')"
[ -n "$PAYLOAD_B64" ] || die "failed to base64-encode the payload manifest"

rm -f "$BUNDLE_OUT"
cat "$SIG_TMP" > "$BUNDLE_OUT"
printf 'payload: %s\n' "$PAYLOAD_B64" >> "$BUNDLE_OUT"
rm -f "$SIG_TMP"
chmod 0444 "$BUNDLE_OUT"

ok "signed roothash bundle → $BUNDLE_OUT"
ok "  key-id:  $KEY_ID"
ok "  payload: $PAYLOAD_JSON"
