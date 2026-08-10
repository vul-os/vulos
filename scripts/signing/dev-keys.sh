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
# VULOS_DEV_KEYS_DIR is a TEST SEAM only (backend/services/signing/
# devkeys_script_test.go points it at a temp dir so the refusal path can be
# exercised without a real keys/).  Production callers never set it.
KEYS_DIR="${VULOS_DEV_KEYS_DIR:-$REPO_ROOT/keys}"
DEVANCHOR_GO="$REPO_ROOT/backend/services/signing/devanchor.go"

# Published seeds — these MUST match signing.DevRootSeed / signing.DevReleaseSeed
# in backend/services/signing/devanchor.go, which pins the resulting pubkeys.
DEV_ROOT_SEED="vulos-dev-signing-root-v1"
DEV_RELEASE_SEED="vulos-dev-signing-release-v1"

# A far-future expiry keeps the dev cert valid without a recurring chore. A real
# release cert gets a 12-month NotAfter — see docs/KEY-CEREMONY.md.
DEV_CERT_NOT_AFTER="2099-01-01T00:00:00Z"
DEV_CERT_KEY_ID="dev-release-DO-NOT-TRUST"

sign() { (cd "$REPO_ROOT/backend" && go run ./cmd/sign "$@"); }

# ── Refuse to destroy PRODUCTION trust material ──────────────────────────────
#
# This script overwrites keys/trust-anchor.pub, keys/release-cert.json and both
# *.pub.json — the SHIPPED public halves that the ceremony (scripts/signing/
# ceremony.sh) also writes.  `make sign-registry` runs it automatically whenever
# the dev release private key is absent, which is ALWAYS true on a fresh clone
# because *.priv.json is gitignored.  So, before this guard, routine work on a
# tree carrying real ceremony output silently replaced the production anchor
# with the published-seed dev anchor and re-signed every registry entry with the
# dev key — a downgrade of the repo's root of trust, performed by a command
# nobody would think twice about.
#
# The anchor decides it: if keys/trust-anchor.pub is anything other than the dev
# anchor pinned in devanchor.go, this tree holds material this script did not
# produce and must not destroy.  Overriding is possible but must be typed out.
DEV_KEYS_OVERWRITE="${VULOS_DEV_KEYS_OVERWRITE:-}"

refuse() {
    # STDERR: `make sign-registry` invokes this script with >/dev/null.
    {
        echo
        echo "✗ REFUSING to regenerate the dev keys: $1"
        echo
        echo "  $KEYS_DIR holds signing material this script did not produce —"
        echo "  almost certainly the output of the production key ceremony"
        echo "  (docs/KEY-CEREMONY.md).  Regenerating would overwrite the trust"
        echo "  anchor and release cert with the DEV keypair, whose private half"
        echo "  is derived from a published seed, and any following"
        echo "  'make sign-registry' would re-sign every registry entry with it."
        echo
        echo "  If you meant to sign with a real key:"
        echo "      make sign-registry RELEASE_PRIV=/path/to/release.priv.json"
        echo "  If you are re-running the ceremony:"
        echo "      make ceremony"
        echo "  If you really do want the dev keys back in this tree, say so:"
        echo "      VULOS_DEV_KEYS_OVERWRITE=1 $0"
        echo "  (then 'git checkout -- keys/' to restore the committed material)"
        echo
    } >&2
    exit 1
}

guard_existing_material() {
    [ -e "$KEYS_DIR/trust-anchor.pub" ] || [ -e "$KEYS_DIR/release-cert.json" ] || return 0

    if [ "$DEV_KEYS_OVERWRITE" = "1" ]; then
        echo "⚠ VULOS_DEV_KEYS_OVERWRITE=1 — overwriting existing trust material in $KEYS_DIR" >&2
        return 0
    fi

    # Single source of truth for what "the dev anchor" is: the constant that
    # production code refuses (signing.DevAnchorPubB64).  Read it rather than
    # duplicating it, so the two can never drift.
    local dev_anchor
    dev_anchor="$(sed -n 's/^[[:space:]]*DevAnchorPubB64[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$DEVANCHOR_GO" | head -n1)"
    [ -n "$dev_anchor" ] || refuse "could not read DevAnchorPubB64 from $DEVANCHOR_GO (refusing rather than guessing)"

    if [ -e "$KEYS_DIR/trust-anchor.pub" ]; then
        local have
        have="$(tr -d '[:space:]' < "$KEYS_DIR/trust-anchor.pub")"
        if [ "$have" != "$dev_anchor" ]; then
            refuse "keys/trust-anchor.pub is NOT the dev anchor (found ${have:0:16}…, dev is ${dev_anchor:0:16}…)"
        fi
    fi

    if [ -e "$KEYS_DIR/release-cert.json" ]; then
        local key_id
        key_id="$(sed -n 's/.*"key_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$KEYS_DIR/release-cert.json" | head -n1)"
        if [ "$key_id" != "$DEV_CERT_KEY_ID" ]; then
            refuse "keys/release-cert.json is issued to key_id \"$key_id\", not the dev cert \"$DEV_CERT_KEY_ID\""
        fi
    fi
}

guard_existing_material

# TEST SEAM: stop after the guard so the refusal/pass decision can be asserted
# without spending a minute deriving keys.  Never set in normal use.
if [ "${VULOS_DEV_KEYS_CHECK_ONLY:-}" = "1" ]; then
    echo "✓ dev-keys guard: existing material is the dev keypair (or absent) — safe to regenerate"
    exit 0
fi

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
