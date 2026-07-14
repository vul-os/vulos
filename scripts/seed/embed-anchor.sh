#!/bin/sh
# scripts/seed/embed-anchor.sh — Install the Vulos trust-anchor public key into
# a seed rootfs + initramfs so the early-boot verify path (VERITY-02) can
# validate the fetched OS chain without any network call.
#
# Usage (called from build.sh during seed assembly):
#   scripts/seed/embed-anchor.sh <rootfs_dir>
#
# The trust-anchor public key is resolved in order:
#   1. $VULOS_TRUST_ANCHOR_PUBKEY — explicit env-var path (production builds)
#   2. keys/trust-anchor.pub in the repo root  — developer convenience key
#      (build proceeds but emits a prominent WARNING; must not be the key that
#       authorises production images)
#
# If neither source exists the script prints a clear error and exits non-zero,
# which causes the parent build.sh (running under set -e) to abort — you
# cannot produce an image that silently lacks a trust anchor.
#
# Key format:
#   A raw Ed25519 public key encoded as standard Base64 (RFC 4648) on a single
#   line, as produced by the vulos keygen tool (SIGN-03).  The file should be
#   32 decoded bytes.  The format matches what backend/services/signing uses for
#   ed25519.Verify; see backend/services/signing/FORMAT.md for the full
#   serialisation spec.
#
# In-image paths (contract with VERITY-02):
#   /etc/vulos/trust-anchor.pub       — primary path (real root, runtime use)
#   /etc/vulos/trust-anchor.pub       — same file packed into every initramfs
#                                       image via the initramfs-tools hook below
#                                       (available at early-boot, before pivot)
#
# Initramfs embedding:
#   An initramfs-tools "hook" is dropped into the rootfs at
#   /etc/initramfs-tools/hooks/vulos-trust-anchor.  When update-initramfs runs
#   (in build.sh, after this script), the hook copies the key into every
#   generated initrd so that the early verify binary can read it from
#   /etc/vulos/trust-anchor.pub before pivot_root.
#
# Immutability / re-flash semantics:
#   Once the image is produced the key is frozen inside both the rootfs and the
#   initramfs.  Rotating the trust anchor requires re-flashing the seed.  There
#   is no runtime key-update mechanism; that is by design (see SEED-TRUST.md).

set -e

# ── Helpers ──────────────────────────────────────────────────────────────────

RED='\033[0;31m'
YELLOW='\033[1;33m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
DIM='\033[2m'
NC='\033[0m'

die() {
    printf '%b\n' "${RED}✗ [embed-anchor] $*${NC}" >&2
    exit 1
}

warn() {
    printf '%b\n' "${YELLOW}⚠  [embed-anchor] $*${NC}" >&2
}

info() {
    printf '%b\n' "${BLUE}  ▸ [embed-anchor] $*${NC}"
}

ok() {
    printf '%b\n' "  ${GREEN}✓${NC} [embed-anchor] $*"
}

# ── Args ──────────────────────────────────────────────────────────────────────

ROOTFS="${1:-}"
if [ -z "$ROOTFS" ]; then
    die "Usage: $0 <rootfs_dir>"
fi
if [ ! -d "$ROOTFS" ]; then
    die "rootfs directory does not exist: $ROOTFS"
fi

# ── Resolve script / repo root ────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# ── Resolve key source ────────────────────────────────────────────────────────

DEV_KEY_PATH="$REPO_ROOT/keys/trust-anchor.pub"

if [ -n "${VULOS_TRUST_ANCHOR_PUBKEY:-}" ]; then
    # Env var set — production / explicit path
    ANCHOR_SRC="$VULOS_TRUST_ANCHOR_PUBKEY"
    if [ ! -f "$ANCHOR_SRC" ]; then
        die "VULOS_TRUST_ANCHOR_PUBKEY is set to '$ANCHOR_SRC' but the file does not exist."
    fi
    info "Using trust anchor from VULOS_TRUST_ANCHOR_PUBKEY: $ANCHOR_SRC"
    IS_DEV_KEY=0
elif [ -f "$DEV_KEY_PATH" ]; then
    # Developer convenience key — build allowed but must warn loudly
    ANCHOR_SRC="$DEV_KEY_PATH"
    warn "═══════════════════════════════════════════════════════════"
    warn "  DEV KEY IN USE — trust anchor is the repo-local dev key"
    warn "  ($DEV_KEY_PATH)"
    warn "  Do NOT ship/flash this image for production use."
    warn "  Set VULOS_TRUST_ANCHOR_PUBKEY to a production key."
    warn "═══════════════════════════════════════════════════════════"
    IS_DEV_KEY=1
else
    # No key anywhere — fail loudly, do not build an unverifiable image
    die "$(cat <<'EOF'
No trust-anchor public key found.  Cannot build a verifiable seed image.

Provide a key via one of:
  export VULOS_TRUST_ANCHOR_PUBKEY=/path/to/trust-anchor.pub   # production
  keys/trust-anchor.pub in the repo root                       # dev only

The key must be a raw Ed25519 public key, Base64-encoded (RFC 4648), single
line, 32 decoded bytes (see backend/services/signing/FORMAT.md).

To generate a dev key for local builds:
  mkdir -p keys
  # Using openssl (if available):
  openssl genpkey -algorithm ed25519 -out keys/trust-anchor.key 2>/dev/null && \
    openssl pkey -in keys/trust-anchor.key -pubout -outform DER 2>/dev/null | \
    tail -c 32 | base64 > keys/trust-anchor.pub
  # Or use the vulos keygen tool once SIGN-03 lands.
EOF
)"
fi

# ── Basic sanity: key must be non-empty ───────────────────────────────────────

if [ ! -s "$ANCHOR_SRC" ]; then
    die "Trust-anchor key file is empty: $ANCHOR_SRC"
fi

# ── Install key into rootfs ───────────────────────────────────────────────────

DEST_DIR="$ROOTFS/etc/vulos"
DEST_KEY="$DEST_DIR/trust-anchor.pub"

mkdir -p "$DEST_DIR"
cp "$ANCHOR_SRC" "$DEST_KEY"
chmod 0444 "$DEST_KEY"   # read-only for all users — it's a public key
ok "Installed trust anchor → $DEST_KEY (mode 0444)"

# ── Install the root-signed release cert ──────────────────────────────────────
#
# The anchor alone is not enough: it is the offline ROOT key, and the root never
# signs artifacts directly.  The release cert is what binds the online RELEASE
# key to that root, and it is what the App Hub (REGISTRY-SIGN-01) and the
# manifest verifier chain through.  Without it a box with a valid anchor still
# refuses every signed artifact, which looks like a mysterious outage rather
# than a missing file — so resolve it here and fail loudly if it is absent.
#
# Resolved in order:
#   1. $VULOS_RELEASE_CERT      — explicit path (production builds)
#   2. keys/release-cert.json   — repo dev cert (warns, like the dev anchor)
DEV_CERT_PATH="$REPO_ROOT/keys/release-cert.json"
DEST_CERT="$DEST_DIR/release-cert.json"

if [ -n "${VULOS_RELEASE_CERT:-}" ]; then
    CERT_SRC="$VULOS_RELEASE_CERT"
    if [ ! -f "$CERT_SRC" ]; then
        die "VULOS_RELEASE_CERT is set to '$CERT_SRC' but the file does not exist."
    fi
    info "Using release cert from VULOS_RELEASE_CERT: $CERT_SRC"
elif [ -f "$DEV_CERT_PATH" ]; then
    CERT_SRC="$DEV_CERT_PATH"
    if [ "$IS_DEV_KEY" = "1" ]; then
        warn "Release cert is the repo DEV cert ($DEV_CERT_PATH) — DO NOT SHIP."
    else
        # Production anchor + dev cert cannot chain: the cert would not validate.
        die "A production trust anchor was supplied but the release cert is the repo DEV cert.
Set VULOS_RELEASE_CERT to the cert issued by your root key (docs/KEY-CEREMONY.md)."
    fi
else
    die "$(cat <<'EOF'
No release cert found.  A trust anchor without a release cert cannot verify
anything — the box would refuse every app install and every OS update.

Provide one via:
  export VULOS_RELEASE_CERT=/path/to/release-cert.json   # production
  keys/release-cert.json in the repo root                # dev (make dev-keys)

Issue a production cert with the offline root key:
  vulos-sign issue-release-cert -root-priv <root.priv.json> \
      -release-pub <release.pub.json> -key-id release-YYYY-MM \
      -not-after <RFC3339> -min-epoch <n> -out release-cert.json

See docs/KEY-CEREMONY.md.
EOF
)"
fi

cp "$CERT_SRC" "$DEST_CERT"
chmod 0444 "$DEST_CERT"
ok "Installed release cert → $DEST_CERT (mode 0444)"

# ── Install initramfs hook ────────────────────────────────────────────────────
#
# The hook is a standard initramfs-tools "hooks/" script.  When update-initramfs
# runs it copies /etc/vulos/trust-anchor.pub into the cpio archive at the same
# path, so the early-boot verify binary (VERITY-02) can read the key before
# pivot_root, entirely from RAM.

HOOK_DIR="$ROOTFS/etc/initramfs-tools/hooks"
mkdir -p "$HOOK_DIR"

cat > "$HOOK_DIR/vulos-trust-anchor" << 'HOOK_BODY'
#!/bin/sh
# initramfs-tools hook — embed Vulos trust-anchor key into initramfs
# Installed by scripts/seed/embed-anchor.sh (SEED-01).
# The key at /etc/vulos/trust-anchor.pub is copied verbatim into the cpio
# archive so that the early verify path (VERITY-02) can read it before
# pivot_root.
PREREQ=""
prereqs() { echo "$PREREQ"; }
case "$1" in
    prereqs) prereqs; exit 0 ;;
esac

. /usr/share/initramfs-tools/hook-functions

KEY_SRC="/etc/vulos/trust-anchor.pub"
CERT_SRC="/etc/vulos/release-cert.json"
for f in "$KEY_SRC" "$CERT_SRC"; do
    if [ ! -f "$f" ]; then
        echo "ERROR: vulos-trust-anchor hook: $f not found — aborting initramfs build" >&2
        exit 1
    fi
done

# Ensure /etc/vulos exists inside the initramfs working tree
mkdir -p "${DESTDIR}/etc/vulos"

# Copy the anchor AND the release cert into the initramfs. The pre-pivot verifier
# needs both: the anchor to trust, the cert to know which release key that trust
# extends to.
cp "$KEY_SRC" "${DESTDIR}/etc/vulos/trust-anchor.pub"
chmod 0444 "${DESTDIR}/etc/vulos/trust-anchor.pub"
cp "$CERT_SRC" "${DESTDIR}/etc/vulos/release-cert.json"
chmod 0444 "${DESTDIR}/etc/vulos/release-cert.json"
HOOK_BODY

chmod 0755 "$HOOK_DIR/vulos-trust-anchor"
ok "Installed initramfs hook → $HOOK_DIR/vulos-trust-anchor"

# ── Summary ───────────────────────────────────────────────────────────────────

if [ "$IS_DEV_KEY" = "1" ]; then
    warn "Trust anchor embedded using DEV key — re-run with VULOS_TRUST_ANCHOR_PUBKEY for production."
else
    ok "Trust anchor embedded (production key)."
fi

ok "Key will be available at /etc/vulos/trust-anchor.pub in both rootfs and initramfs."
