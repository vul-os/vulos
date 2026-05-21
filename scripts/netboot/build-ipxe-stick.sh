#!/bin/sh
# scripts/netboot/build-ipxe-stick.sh — Build a ~1 MB iPXE USB stick image
# that chainloads boot.vulos.org to fetch kernel + initramfs + squashfs.
#
# Usage:
#   scripts/netboot/build-ipxe-stick.sh [--outdir <dir>]
#
#   --outdir <dir>   Write output to <dir> (default: output/)
#
# Output:
#   <outdir>/vulos-netboot-stick.img   — raw disk image (~1 MB), dd to USB
#
# The script embeds scripts/netboot/boot.ipxe into the iPXE binary at build
# time so the stick boots without any external server.
#
# Tool guard:
#   Requires the iPXE build toolchain (make, gcc, binutils, liblzma-dev, etc.).
#   When the toolchain is absent the script prints the equivalent manual make
#   command and exits 0 (non-fatal), consistent with other tool guards in this
#   repo (gen-verity.sh, debootstrap guard in build.sh).
#   Install on Debian/Ubuntu:
#     apt-get install make gcc binutils liblzma-dev perl mtools xz-utils
#
# iPXE TLS:
#   HTTPS support requires the iPXE build to include TLS (CONFIG_TLS=y,
#   which is the upstream default for the `ipxe.usb` / `ipxe.iso` targets).
#   The embedded boot.ipxe tries HTTPS first and falls back to plain HTTP when
#   TLS negotiation fails (e.g. the server has no cert for boot.vulos.org).
#
# Security model — plain HTTP is safe because:
#   Every fetched artifact (kernel, initramfs, squashfs) is signature-verified
#   against the trust anchor baked into the image (SEED-01/SIGNING.md) before
#   execution.  TLS is opportunistic.  NETB-02 adds imgverify + Secure Boot.
#
# Equivalent manual commands (run inside an iPXE source tree):
#   make bin/ipxe.usb    EMBED=<abs_path_to_boot.ipxe>
#   # For UEFI USB (x86_64):
#   make bin-x86_64-efi/ipxe.usb   EMBED=<abs_path_to_boot.ipxe>

set -e

# ── Colours ───────────────────────────────────────────────────────────────────

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
DIM='\033[2m'
NC='\033[0m'

die()  { printf '%b\n' "${RED}✗ [build-ipxe-stick] $*${NC}" >&2; exit 1; }
info() { printf '%b\n' "${BLUE}  ▸ [build-ipxe-stick] $*${NC}"; }
ok()   { printf '%b\n' "  ${GREEN}✓${NC} [build-ipxe-stick] $*"; }
warn() { printf '%b\n' "${YELLOW}⚠  [build-ipxe-stick] $*${NC}" >&2; }
dim()  { printf '%b\n' "  ${DIM}[build-ipxe-stick] $*${NC}"; }

# ── Args ──────────────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

OUTDIR="$REPO_ROOT/output"
while [ $# -gt 0 ]; do
  case "$1" in
    --outdir) OUTDIR="$2"; shift 2 ;;
    *) die "Unknown argument: $1" ;;
  esac
done

mkdir -p "$OUTDIR"
OUTDIR="$(cd "$OUTDIR" && pwd)"

IPXE_SCRIPT="$SCRIPT_DIR/boot.ipxe"
STICK_IMG="$OUTDIR/vulos-netboot-stick.img"

if [ ! -f "$IPXE_SCRIPT" ]; then
  die "boot.ipxe not found at $IPXE_SCRIPT"
fi

# ── Tool guard ────────────────────────────────────────────────────────────────
# The iPXE build toolchain is not universally available.  When absent, print
# the manual make commands and exit 0 (non-fatal) — the same pattern used by
# gen-verity.sh and the debootstrap guard in build.sh.

TOOLCHAIN_MISSING=""
for _t in make gcc ld; do
  command -v "$_t" >/dev/null 2>&1 || TOOLCHAIN_MISSING="$TOOLCHAIN_MISSING $_t"
done

if [ -n "$TOOLCHAIN_MISSING" ]; then
  printf '%b\n' "  ${DIM}[build-ipxe-stick] iPXE build toolchain not found (missing:$TOOLCHAIN_MISSING)${NC}"
  printf '%b\n' "  ${DIM}  Install on Debian/Ubuntu:${NC}"
  printf '%b\n' "  ${DIM}    apt-get install make gcc binutils liblzma-dev perl mtools xz-utils${NC}"
  printf '%b\n' "  ${DIM}  Then clone iPXE and build manually:${NC}"
  printf '%b\n' "  ${DIM}    git clone https://github.com/ipxe/ipxe.git /tmp/ipxe${NC}"
  printf '%b\n' "  ${DIM}    cd /tmp/ipxe/src${NC}"
  printf '%b\n' "  ${DIM}    # Legacy BIOS USB stick (~1 MB):${NC}"
  printf '%b\n' "  ${DIM}    make bin/ipxe.usb EMBED=$IPXE_SCRIPT${NC}"
  printf '%b\n' "  ${DIM}    # UEFI USB stick (x86_64):${NC}"
  printf '%b\n' "  ${DIM}    make bin-x86_64-efi/ipxe.usb EMBED=$IPXE_SCRIPT${NC}"
  printf '%b\n' "  ${DIM}    cp bin/ipxe.usb $STICK_IMG${NC}"
  printf '%b\n' "  ${DIM}  Flash: dd if=$STICK_IMG of=/dev/sdX bs=512 status=progress${NC}"
  exit 0
fi

# ── Locate or clone iPXE source tree ─────────────────────────────────────────

# Prefer a source tree already in the repo cache; fall back to a fresh clone.
IPXE_SRC="${VULOS_IPXE_SRC:-}"
if [ -z "$IPXE_SRC" ]; then
  # Check repo-local cache
  if [ -d "$REPO_ROOT/.ipxe-src/src" ]; then
    IPXE_SRC="$REPO_ROOT/.ipxe-src/src"
    info "Using cached iPXE source: $IPXE_SRC"
  else
    IPXE_CLONE_DIR="$REPO_ROOT/.ipxe-src"
    info "Cloning iPXE (shallow)..."
    if ! command -v git >/dev/null 2>&1; then
      die "git not found — cannot clone iPXE.  Set VULOS_IPXE_SRC to a local iPXE src/ tree."
    fi
    git clone --depth=1 https://github.com/ipxe/ipxe.git "$IPXE_CLONE_DIR"
    IPXE_SRC="$IPXE_CLONE_DIR/src"
    ok "iPXE source cloned to $IPXE_CLONE_DIR"
  fi
fi

if [ ! -f "$IPXE_SRC/Makefile" ]; then
  die "iPXE Makefile not found at $IPXE_SRC/Makefile — check VULOS_IPXE_SRC"
fi

# ── Build: embed boot.ipxe into ipxe.usb (Legacy BIOS target, ~1 MB) ─────────

info "Building iPXE USB stick image (BIOS target)..."
info "  Script : $IPXE_SCRIPT"
info "  Output : $STICK_IMG"

# Targets:
#   bin/ipxe.usb          — legacy BIOS, ~1 MB
#   bin-x86_64-efi/ipxe.usb — UEFI (larger; built additionally if toolchain allows)
(
  cd "$IPXE_SRC"
  make -j"$(nproc 2>/dev/null || echo 1)" \
    bin/ipxe.usb \
    EMBED="$IPXE_SCRIPT" \
    NO_WERROR=1 \
    2>&1
)

if [ ! -f "$IPXE_SRC/bin/ipxe.usb" ]; then
  die "iPXE build succeeded but bin/ipxe.usb not found"
fi

cp "$IPXE_SRC/bin/ipxe.usb" "$STICK_IMG"

IMG_SIZE="$(wc -c < "$STICK_IMG" 2>/dev/null || stat -f%z "$STICK_IMG" 2>/dev/null || echo '?')"
IMG_SIZE_KB=$(( ${IMG_SIZE:-0} / 1024 ))
ok "iPXE stick image: $STICK_IMG (${IMG_SIZE_KB} KB)"

# ── Optionally also build the UEFI variant ────────────────────────────────────

UEFI_STICK_IMG="$OUTDIR/vulos-netboot-stick-uefi.img"
info "Attempting UEFI target (bin-x86_64-efi/ipxe.usb)..."
if (cd "$IPXE_SRC" && make -j"$(nproc 2>/dev/null || echo 1)" \
      bin-x86_64-efi/ipxe.usb EMBED="$IPXE_SCRIPT" NO_WERROR=1 2>&1); then
  if [ -f "$IPXE_SRC/bin-x86_64-efi/ipxe.usb" ]; then
    cp "$IPXE_SRC/bin-x86_64-efi/ipxe.usb" "$UEFI_STICK_IMG"
    UEFI_SIZE="$(wc -c < "$UEFI_STICK_IMG" 2>/dev/null || stat -f%z "$UEFI_STICK_IMG" 2>/dev/null || echo '?')"
    UEFI_SIZE_KB=$(( ${UEFI_SIZE:-0} / 1024 ))
    ok "UEFI stick image: $UEFI_STICK_IMG (${UEFI_SIZE_KB} KB)"
  fi
else
  dim "UEFI target build skipped (optional; BIOS stick is sufficient)"
fi

# ── Summary ───────────────────────────────────────────────────────────────────

echo ""
printf '%b\n' "${GREEN}Netboot stick image ready:${NC} $STICK_IMG"
echo ""
echo "Flash the stick:"
echo "  dd if=$STICK_IMG of=/dev/sdX bs=512 status=progress"
echo ""
echo "Boot behaviour:"
echo "  1. DHCP"
echo "  2. Chainload https://boot.vulos.org/boot.ipxe  (HTTPS preferred)"
echo "  3. Fallback:  http://boot.vulos.org/boot.ipxe  (plain HTTP, security via signatures)"
echo "  4. Fetch kernel + initramfs + squashfs → live-RAM session"
echo ""
echo "UEFI HTTP Boot (no stick required, firmware must support RFC 7230):"
echo "  http://boot.vulos.org/boot.ipxe  (plain HTTP; security via signatures — see NETB-02)"
