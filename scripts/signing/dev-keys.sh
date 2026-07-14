#!/usr/bin/env bash
# scripts/signing/dev-keys.sh — regenerate the repo's DEVELOPMENT signing keys.
#
# ┌──────────────────────────────────────────────────────────────────────────┐
# │  THESE KEYS ARE NOT SECRET.                                              │
# │  They are derived deterministically from the published seed strings      │
# │  below, so anyone can reproduce the private halves in one command.       │
# │  They exist so a fresh clone can verify the committed registry.json      │
# │  signatures offline, with no flags and no key material to fetch.         │
# │                                                                          │
# │  backend/services/signing/devanchor.go pins their public keys and        │
# │  REFUSES them whenever VULOS_ENV=prod — which is also the default when   │
# │  VULOS_ENV is unset.  A production box therefore cannot be tricked into  │
# │  trusting them, no matter what lands in /etc/vulos.                      │
# │                                                                          │
# │  For a real release, run the ceremony in docs/KEY-CEREMONY.md instead.   │
# └──────────────────────────────────────────────────────────────────────────┘
#
# Outputs (repo root):
#   keys/root.pub.json      dev ROOT public key            [committed]
#   keys/release.pub.json   dev RELEASE public key         [committed]
#   keys/trust-anchor.pub   dev ROOT pubkey, base64        [committed, SHIPPED]
#   keys/release-cert.json  root-signed release cert       [committed, SHIPPED]
#   keys/root.priv.json     dev ROOT private key           [gitignored]
#   keys/release.priv.json  dev RELEASE private key        [gitignored]
#
# The private-key files are regenerated on demand and never committed — see
# .gitignore.  `make sign-registry` calls this script first so that a clone with
# no keys/ private material can still re-sign the registry.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
KEYS_DIR="$REPO_ROOT/keys"

# Published seeds — these MUST match signing.DevRootSeed / signing.DevReleaseSeed
# in backend/services/signing/devanchor.go, which pins the resulting pubkeys.
DEV_ROOT_SEED="vulos-dev-signing-root-v1"
DEV_RELEASE_SEED="vulos-dev-signing-release-v1"

# A far-future expiry keeps the dev cert valid without a recurring chore. A real
# release cert gets a 12-month NotAfter — see docs/KEY-CEREMONY.md.
DEV_CERT_NOT_AFTER="2099-01-01T00:00:00Z"
DEV_CERT_KEY_ID="dev-release-DO-NOT-TRUST"

sign() { (cd "$REPO_ROOT/backend" && go run ./cmd/sign "$@"); }

mkdir -p "$KEYS_DIR"

echo "▸ Deriving DEV root key (seed: $DEV_ROOT_SEED)"
sign gen-key -dev-seed "$DEV_ROOT_SEED" \
    -out-priv "$KEYS_DIR/root.priv.json" \
    -out-pub  "$KEYS_DIR/root.pub.json"

echo "▸ Deriving DEV release key (seed: $DEV_RELEASE_SEED)"
sign gen-key -dev-seed "$DEV_RELEASE_SEED" \
    -out-priv "$KEYS_DIR/release.priv.json" \
    -out-pub  "$KEYS_DIR/release.pub.json"

echo "▸ Exporting DEV trust anchor (keys/trust-anchor.pub)"
sign export-anchor -pub "$KEYS_DIR/root.pub.json" -out "$KEYS_DIR/trust-anchor.pub"

echo "▸ Issuing DEV release cert (root signs release key)"
sign issue-release-cert \
    -root-priv   "$KEYS_DIR/root.priv.json" \
    -release-pub "$KEYS_DIR/release.pub.json" \
    -key-id      "$DEV_CERT_KEY_ID" \
    -not-after   "$DEV_CERT_NOT_AFTER" \
    -min-epoch   0 \
    -out         "$KEYS_DIR/release-cert.json"

echo
echo "✓ Dev keys ready in keys/ — private halves are gitignored."
echo "  These keys are refused whenever VULOS_ENV=prod (signing.RefuseDevKeyInProd)."
