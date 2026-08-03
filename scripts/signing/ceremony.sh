#!/usr/bin/env bash
# scripts/signing/ceremony.sh — run the Vulos PRODUCTION signing ceremony in one
# command and collect EVERYTHING you must keep into a single vault folder.
#
# This is the scripted form of docs/KEY-CEREMONY.md §4. It:
#   1. generates the ROOT and RELEASE keypairs,
#   2. has the root sign the release cert,
#   3. installs the four PUBLIC files into keys/ (these get committed),
#   4. signs registry.json with the release key,
#   5. verifies the production release gate (verify-registry-prod) now passes,
#   6. drops every private key + a complete record + a README into --vault.
#
# PRIVATE KEYS ARE WRITTEN ONLY INTO --vault, NEVER INTO THE REPO. Point --vault
# at encrypted removable media and keep TWO copies in TWO places.
#
# ┌───────────────────────────────────────────────────────────────────────────┐
# │ SECURITY NOTE: the strongest form generates the ROOT key on an air-gapped  │
# │ machine that never goes online (docs/KEY-CEREMONY.md §4). This script      │
# │ collapses that to one machine for convenience — a real trade-off: whoever  │
# │ can read this machine during the run can read the root key. For the        │
# │ crown-jewel root key, prefer a clean offline box.                          │
# └───────────────────────────────────────────────────────────────────────────┘
#
# Usage:
#   scripts/signing/ceremony.sh --vault DIR [options]
#
#   --vault DIR        REQUIRED. Folder to hold all private keys + everything to
#                      keep. Point it at encrypted removable media, and it MUST be
#                      outside the repo (e.g. /Volumes/VULOS-VAULT).
#   --key-id ID        Release-cert label (default: release-YYYY-MM).
#   --not-after DATE   Cert expiry, RFC 3339 (default: +12 months).
#   --min-epoch N      Rollback floor (default: 1; bump on compromise rotation).
#   --rotate           Reuse the existing root key in the vault; mint a NEW
#                      release key + cert only (routine 12-month rotation). The
#                      anchor does not change, so nothing is reflashed.
#   --commit           Also git-commit the public trust material + signed registry.
#   --yes, -y          Don't prompt for confirmation.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
KEYS_DIR="$REPO_ROOT/keys"

VAULT="" ; KEY_ID="" ; NOT_AFTER="" ; MIN_EPOCH="1"
ROTATE=0 ; DO_COMMIT=0 ; ASSUME_YES=0

die() { echo "ERROR: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --vault)     VAULT="${2:-}"; shift 2 ;;
    --key-id)    KEY_ID="${2:-}"; shift 2 ;;
    --not-after) NOT_AFTER="${2:-}"; shift 2 ;;
    --min-epoch) MIN_EPOCH="${2:-}"; shift 2 ;;
    --rotate)    ROTATE=1; shift ;;
    --commit)    DO_COMMIT=1; shift ;;
    --yes|-y)    ASSUME_YES=1; shift ;;
    -h|--help)   sed -n '2,44p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

[ -f "$REPO_ROOT/registry.json" ] || die "registry.json not found — run from inside the vulos repo"
command -v go >/dev/null 2>&1 || die "go toolchain not found on PATH"

# Default the vault to a gitignored folder in the sibling vulos-cloud repo
# (proprietary), so the keys you must keep land in one place with no extra flags.
# Override with --vault (e.g. encrypted removable media) any time.
if [ -z "$VAULT" ]; then
  if [ -d "$REPO_ROOT/../vulos-cloud/.git" ]; then
    VAULT="$(cd "$REPO_ROOT/.." && pwd)/vulos-cloud/signing-vault"
    echo "▸ No --vault given — using the gitignored vault in vulos-cloud:"
    echo "    $VAULT"
  else
    die "--vault DIR is required (no sibling vulos-cloud found). Point it at encrypted media, e.g. --vault /Volumes/VULOS-VAULT"
  fi
fi

mkdir -p "$VAULT" && chmod 700 "$VAULT"
VAULT="$(cd "$VAULT" && pwd)"

# The public OS repo must NEVER hold a private key.
case "$VAULT/" in
  "$REPO_ROOT"/*) die "The vault must NOT live inside the vulos OS repo (it is public). Use vulos-cloud or removable media." ;;
esac

# If the vault sits inside ANY git work tree, that repo MUST ignore it — so a
# private key can never be committed, not even by accident.
if vault_repo="$(git -C "$VAULT" rev-parse --show-toplevel 2>/dev/null)"; then
  if ! git -C "$vault_repo" check-ignore -q "$VAULT"; then
    die "Vault $VAULT is inside git repo $vault_repo but is NOT gitignored there. Add it (e.g. a line 'signing-vault/') to that repo's .gitignore first — a private key must never be committable."
  fi
  echo "▸ Vault is gitignored in $vault_repo — private keys cannot be committed."
fi

# Portable date defaults: BSD/macOS first, then GNU/Linux.
if [ -z "$NOT_AFTER" ]; then
  NOT_AFTER="$(date -u -v+1y +%Y-%m-%dT00:00:00Z 2>/dev/null || date -u -d '+12 months' +%Y-%m-%dT00:00:00Z)"
fi
[ -n "$KEY_ID" ] || KEY_ID="release-$(date -u +%Y-%m)"

sign() { (cd "$REPO_ROOT/backend" && go run ./cmd/sign "$@"); }

ROOT_PRIV="$VAULT/root.priv.json"  ; ROOT_PUB="$VAULT/root.pub.json"
REL_PRIV="$VAULT/release.priv.json" ; REL_PUB="$VAULT/release.pub.json"
ANCHOR="$VAULT/trust-anchor.pub"    ; CERT="$VAULT/release-cert.json"

# ── Guard rails ──────────────────────────────────────────────────────────────
if [ "$ROTATE" -eq 1 ]; then
  [ -f "$ROOT_PRIV" ] || die "--rotate needs the existing root key at $ROOT_PRIV"
  [ -f "$ROOT_PUB" ]  || die "--rotate needs the existing root pubkey at $ROOT_PUB"
else
  [ -f "$ROOT_PRIV" ] && die "$ROOT_PRIV already exists. Minting a NEW root key would orphan every fielded box (they would need reflashing). Use --rotate to keep this root and rotate only the release key, or point --vault at a fresh folder."
fi

echo
echo "  Vault (keep this safe):     $VAULT"
echo "  Cert key-id:                $KEY_ID"
echo "  Cert expires:               $NOT_AFTER"
echo "  Min epoch (rollback floor): $MIN_EPOCH"
echo "  Mode:                       $([ $ROTATE -eq 1 ] && echo 'rotate release key (root unchanged)' || echo 'full first-time ceremony')"
echo
if [ "$ASSUME_YES" -ne 1 ]; then
  printf "Proceed? [y/N] "; read -r ans; [ "$ans" = "y" ] || [ "$ans" = "Y" ] || die "aborted"
fi

# ── 1. ROOT key (generated only on a full ceremony) ──────────────────────────
if [ "$ROTATE" -eq 0 ]; then
  echo "▸ Generating ROOT keypair → $ROOT_PRIV"
  sign gen-key -out-priv "$ROOT_PRIV" -out-pub "$ROOT_PUB"
fi
echo "▸ Exporting trust anchor → $ANCHOR"
sign export-anchor -pub "$ROOT_PUB" -out "$ANCHOR"

# ── 2. RELEASE key ───────────────────────────────────────────────────────────
echo "▸ Generating RELEASE keypair → $REL_PRIV"
sign gen-key -out-priv "$REL_PRIV" -out-pub "$REL_PUB"

# ── 3. Root signs the release cert ───────────────────────────────────────────
echo "▸ Root signing the release cert → $CERT"
sign issue-release-cert \
  -root-priv   "$ROOT_PRIV" \
  -release-pub "$REL_PUB" \
  -key-id      "$KEY_ID" \
  -not-after   "$NOT_AFTER" \
  -min-epoch   "$MIN_EPOCH" \
  -out         "$CERT"

# ── 4. Install PUBLIC trust material into the repo (these get committed) ──────
echo "▸ Installing public trust material into keys/"
cp "$ANCHOR"   "$KEYS_DIR/trust-anchor.pub"
cp "$CERT"     "$KEYS_DIR/release-cert.json"
cp "$ROOT_PUB" "$KEYS_DIR/root.pub.json"
cp "$REL_PUB"  "$KEYS_DIR/release.pub.json"

# ── 5. Sign the registry with the release key ────────────────────────────────
echo "▸ Signing registry.json with the release key"
sign sign-registry -release-priv "$REL_PRIV" -registry "$REPO_ROOT/registry.json"

# ── 6. Verify the production release gate now passes ─────────────────────────
echo "▸ Verifying the production release gate (verify-registry-prod)"
sign verify-registry -require-prod-keys \
  -anchor     "$KEYS_DIR/trust-anchor.pub" \
  -cert       "$KEYS_DIR/release-cert.json" \
  -registry   "$REPO_ROOT/registry.json" \
  -unverified "$REPO_ROOT/registry-unverified.json"

# ── 7. Lock down + record the vault ──────────────────────────────────────────
chmod 600 "$VAULT"/*.priv.json 2>/dev/null || true
# Keep a complete record: also stash a copy of the committed publics in the vault.
cp "$KEYS_DIR/trust-anchor.pub" "$KEYS_DIR/release-cert.json" \
   "$KEYS_DIR/root.pub.json" "$KEYS_DIR/release.pub.json" "$VAULT/"

cat > "$VAULT/README.txt" <<EOF
VULOS SIGNING VAULT — KEEP THIS SAFE, KEEP IT OFFLINE
key-id: $KEY_ID   cert expires: $NOT_AFTER   min-epoch: $MIN_EPOCH

WHAT'S HERE
  root.priv.json      ROOT private key — the crown jewel. Offline, air-gapped.
                      Leak  => every box forgeable; reflash the whole fleet.
                      Loss  => cannot rotate the release key; reflash the fleet.
                      Used ~once/year, only to sign a new release cert.
  release.priv.json   RELEASE private key — signs registry + OS images each
                      release. Point RELEASE_PRIV at this file. Rotatable
                      without reflashing anything.
  trust-anchor.pub / release-cert.json / *.pub.json
                      PUBLIC — already committed to the repo. Kept here too so
                      the vault is a complete, self-contained record.
  MANIFEST.sha256     checksums of everything above.

CUSTODY
  - This folder is gitignored inside vulos-cloud, so it is never committed. But
    it is NOT encrypted at rest — keep at least ONE encrypted backup copy
    elsewhere (removable media in a second location).
  - Strongest posture: move root.priv.json out to OFFLINE encrypted media once
    the ceremony is done; you only need it again to rotate the release cert
    (~once a year). release.priv.json is the one you use each release.
  - NEVER copy the *.priv.json files into a git repo that would commit them, or
    into a secrets manager CI can read. (.gitignore + CI refuse committed
    private keys, and this script refuses a non-ignored vault inside a repo.)
  - Sign a routine release later (root stays in its drawer):
      make sign-registry RELEASE_PRIV=$VAULT/release.priv.json
  - Rotate the release key in ~12 months (root unchanged, nothing reflashed):
      scripts/signing/ceremony.sh --vault $VAULT --rotate
EOF

( cd "$VAULT" && { command -v shasum >/dev/null 2>&1 && shasum -a 256 -- *.json *.pub 2>/dev/null || sha256sum -- *.json *.pub 2>/dev/null; } > MANIFEST.sha256 || true )

echo
echo "✓ Ceremony complete."
echo "  Vault (keep safe, offline, TWO copies): $VAULT"
echo "  Public trust material installed in:     keys/"
echo
echo "NEXT:"
echo "  1) Make a SECOND encrypted copy of the vault now."
echo "  2) Commit the public trust material + signed registry:"
echo "       git add keys/trust-anchor.pub keys/release-cert.json keys/root.pub.json keys/release.pub.json registry.json"
echo "       git commit -m 'signing: install production trust anchor, re-sign registry'"
echo "       git push"

if [ "$DO_COMMIT" -eq 1 ]; then
  echo "▸ --commit: committing public trust material + registry"
  ( cd "$REPO_ROOT" \
    && git add keys/trust-anchor.pub keys/release-cert.json keys/root.pub.json keys/release.pub.json registry.json \
    && git commit -m "signing: install production trust anchor, re-sign registry" )
fi
