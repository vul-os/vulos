#!/bin/sh
# Vulos — System Image Builder & Deployer
#
# Builds a bare-metal Debian 13 (trixie) system image.
# Optionally deploys to a remote machine via SSH with Caddy + wildcard TLS.
#
# Usage:
#   sudo ./build.sh                                    # build to ./output/ (amd64)
#   sudo ./build.sh --arm64                            # ARM64 generic rootfs
#   sudo ./build.sh --arm64 --device rpi4              # RPi 4 image
#   sudo ./build.sh --arm64 --device pinephone         # PinePhone image
#   sudo ./build.sh --arm64 --device generic-arm64     # generic ARM64 (default with --arm64)
#   ./build.sh --deploy 192.168.1.50                   # build + deploy via SSH
#   ./build.sh --deploy-only 192.168.1.50              # skip packages, just push code
#   ./build.sh --deploy 192.168.1.50 \
#     --domain os.vulos.org \
#     --dns-namecheap myuser APIKEY123                  # with Caddy wildcard TLS
#   sudo ./build.sh --live                             # produce bootable live-USB image
#   ./build.sh --netboot-stick                        # build ~1 MB iPXE netboot USB stick image
#
# Image filenames:
#   vulos-amd64.tar.gz                        (default, no flags)
#   vulos-arm64-generic-arm64.tar.gz          (--arm64 or --arm64 --device generic-arm64)
#   vulos-arm64-rpi4.tar.gz                   (--arm64 --device rpi4)
#   vulos-arm64-pinephone.tar.gz              (--arm64 --device pinephone)
#
# Env vars (alternative to flags):
#   NAMECHEAP_USER, NAMECHEAP_KEY, VULOS_DOMAIN
#
#   SEED-01 / SEED-03 trust + bucket (apply to rootfs / seed builds only):
#   VULOS_TRUST_ANCHOR_PUBKEY — path to the Ed25519 public key that becomes the
#                               immutable trust anchor baked into the seed.
#                               Required for fork builds (fails loud when absent).
#                               Falls back to keys/trust-anchor.pub (dev, warns).
#   VULOS_OS_BUCKET_URL       — HTTPS URL of the OS bucket that the seed will
#                               fetch images from (e.g. https://os.example.com).
#                               Embedded into /etc/vulos/os-bucket-url inside the
#                               seed.  When omitted the Vulos upstream default is
#                               used.  Key + bucket travel together: a seed built
#                               with a fork's key+bucket trusts only that fork.
#
# Fork procedure (SEED-03):
#   # 1. Generate your own root keypair (requires backend/cmd/sign — SIGN-03)
#   backend/cmd/sign gen-key --out keys/my-fork.key --pub keys/my-fork.pub
#   # 2. Stand up your own OS bucket (any HTTPS host serving the update manifest)
#   # 3. Build the seed with your key + bucket
#   VULOS_TRUST_ANCHOR_PUBKEY=keys/my-fork.pub \
#   VULOS_OS_BUCKET_URL=https://os.my-fork.example.com \
#     sudo ./build.sh
#   # 4. Flash — the resulting seed trusts only your key and fetches from your
#   #    bucket.  Upstream-signed images are rejected.  Re-flashing re-establishes
#   #    trust end-to-end.  See roadmap/SEED-TRUST.md for the full procedure.
#
# Prerequisites:
#   - Go 1.21+, Node 18+, npm
#   - SSH key access to target (for --deploy)
#   - Namecheap API access + server IP whitelisted (for --dns-namecheap)
#   - qemu-user-static (for --arm64 cross-debootstrap on amd64 hosts)

set -e

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
ARCH="${ARCH:-amd64}"
GOARCH="$ARCH"
[ "$ARCH" = "x86_64" ] && GOARCH="amd64" && ARCH="amd64"
[ "$ARCH" = "aarch64" ] && GOARCH="arm64" && ARCH="arm64"
SUITE="trixie"

GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
DIM='\033[2m'
NC='\033[0m'

# ═══════════════════════════════════
# Parse args
# ═══════════════════════════════════
DEPLOY_HOST=""
DEPLOY_ONLY=false
DOMAIN="${VULOS_DOMAIN:-}"
NC_USER="${NAMECHEAP_USER:-}"
NC_KEY="${NAMECHEAP_KEY:-}"
OUTDIR_ARG=""
LIVE_MODE=0
DISK_MODE=0
REUSE_ROOTFS=0
DEVICE=""
NETBOOT_STICK=0

while [ $# -gt 0 ]; do
  case "$1" in
    --arm64)       ARCH="arm64"; shift ;;
    --device)      DEVICE="$2"; shift 2 ;;
    --deploy)      DEPLOY_HOST="$2"; shift 2 ;;
    --deploy-only) DEPLOY_HOST="$2"; DEPLOY_ONLY=true; shift 2 ;;
    --domain)      DOMAIN="$2"; shift 2 ;;
    --dns-namecheap) NC_USER="$2"; NC_KEY="$3"; shift 3 ;;
    --live)        LIVE_MODE=1; shift ;;
    --disk)        DISK_MODE=1; shift ;;
    --reuse-rootfs) REUSE_ROOTFS=1; shift ;;
    --netboot-stick) NETBOOT_STICK=1; shift ;;
    *) OUTDIR_ARG="$1"; shift ;;
  esac
done

# Re-derive GOARCH after flags (--arm64 may have changed ARCH)
GOARCH="$ARCH"
[ "$ARCH" = "x86_64" ] && GOARCH="amd64" && ARCH="amd64"
[ "$ARCH" = "aarch64" ] && GOARCH="arm64" && ARCH="arm64"

# Default DEVICE
if [ "$ARCH" = "arm64" ] && [ -z "$DEVICE" ]; then
  DEVICE="generic-arm64"
fi

# Validate --device value when set
if [ -n "$DEVICE" ]; then
  case "$DEVICE" in
    rpi4|pinephone|generic-arm64) ;;
    *)
      echo "${RED}Unknown --device '$DEVICE'. Valid values: rpi4, pinephone, generic-arm64${NC}"
      exit 1
      ;;
  esac
  if [ "$ARCH" != "arm64" ]; then
    echo "${RED}--device requires --arm64${NC}"
    exit 1
  fi
fi

# Validate qemu-user-static for cross-debootstrap (arm64 on non-arm64 host)
if [ "$ARCH" = "arm64" ]; then
  HOST_ARCH="$(uname -m 2>/dev/null || true)"
  if [ "$HOST_ARCH" != "aarch64" ] && [ "$HOST_ARCH" != "arm64" ]; then
    if ! command -v qemu-aarch64-static >/dev/null 2>&1; then
      echo "${RED}Cross-building arm64 rootfs requires qemu-user-static.${NC}"
      echo "${RED}Install with: apt-get install qemu-user-static${NC}"
      # Only fatal if we're actually going to run debootstrap (checked later)
      QEMU_MISSING=1
    fi
  fi
fi

# Validate
if [ -n "$DOMAIN" ] && [ -z "$NC_USER" ]; then
  echo "${RED}--domain requires --dns-namecheap <user> <key> (or NAMECHEAP_USER + NAMECHEAP_KEY env vars)${NC}"
  exit 1
fi

# Default root@ if no user specified
case "$DEPLOY_HOST" in
  "") ;;
  *@*) ;;
  *) DEPLOY_HOST="root@$DEPLOY_HOST" ;;
esac

OUTDIR="${OUTDIR_ARG:-$ROOT_DIR/output}"
mkdir -p "$OUTDIR"
OUTDIR="$(cd "$OUTDIR" && pwd)"

echo ""
echo "${BLUE}╔══════════════════════════════════╗${NC}"
echo "${BLUE}║      Vulos — Image Builder     ║${NC}"
echo "${BLUE}╠══════════════════════════════════╣${NC}"
echo "${BLUE}║${NC} Arch:   $ARCH"
[ -n "$DEVICE" ] && echo "${BLUE}║${NC} Device: $DEVICE"
echo "${BLUE}║${NC} Suite:  $SUITE"
echo "${BLUE}║${NC} Output: $OUTDIR"
[ "$LIVE_MODE" = "1" ] && echo "${BLUE}║${NC} Mode:   live-USB (squashfs + overlayfs)"
[ "$NETBOOT_STICK" = "1" ] && echo "${BLUE}║${NC} Mode:   netboot-stick (~1 MB iPXE USB)"
[ -n "$DEPLOY_HOST" ] && echo "${BLUE}║${NC} Deploy: $DEPLOY_HOST"
[ -n "$DOMAIN" ] && echo "${BLUE}║${NC} Domain: $DOMAIN (+ *.$DOMAIN)"
[ -n "$NC_USER" ] && echo "${BLUE}║${NC} DNS:    Namecheap ($NC_USER)"
echo "${BLUE}╚══════════════════════════════════╝${NC}"
echo ""

# ═══════════════════════════════════
# 1. Build Go binaries
# ═══════════════════════════════════
echo "${BLUE}▸ Building Go binaries ($GOARCH)...${NC}"
cd "$ROOT_DIR/backend"
GOOS=linux GOARCH="$GOARCH" CGO_ENABLED=0 go build -ldflags="-s -w" -o "$OUTDIR/vulos-server" ./cmd/server
GOOS=linux GOARCH="$GOARCH" CGO_ENABLED=0 go build -ldflags="-s -w" -o "$OUTDIR/vulos-init" ./cmd/init
echo "  ${GREEN}✓${NC} vulos-server, vulos-init"

# ═══════════════════════════════════
# 2. Build frontend
# ═══════════════════════════════════
echo "${BLUE}▸ Building frontend...${NC}"
cd "$ROOT_DIR"
npm ci --silent 2>/dev/null || npm install --silent
npm run build
rm -rf "$OUTDIR/webroot"
cp -r dist "$OUTDIR/webroot"
echo "  ${GREEN}✓${NC} webroot/"

# ═══════════════════════════════════
# 3. Copy assets
# ═══════════════════════════════════
echo "${BLUE}▸ Copying assets...${NC}"
rm -rf "$OUTDIR/apps"
mkdir -p "$OUTDIR/apps"
for app in "$ROOT_DIR/apps/"*/; do
  [ -d "$app" ] && cp -r "$app" "$OUTDIR/apps/" && echo "  ${GREEN}✓${NC} $(basename "$app")"
done
cp "$ROOT_DIR/registry.json" "$OUTDIR/registry.json"
[ -f "$ROOT_DIR/scripts/xdg-open" ] && cp "$ROOT_DIR/scripts/xdg-open" "$OUTDIR/xdg-open"
echo "  ${GREEN}✓${NC} registry.json"

# ═══════════════════════════════════
# Deploy mode — SSH to remote machine
# ═══════════════════════════════════
if [ -z "$DEPLOY_HOST" ]; then
  # No deploy — skip to rootfs build
  :
else
  echo ""
  echo "${BLUE}▸ Deploying to $DEPLOY_HOST...${NC}"

  # Test SSH connection
  if ! ssh -o ConnectTimeout=5 -o BatchMode=yes "$DEPLOY_HOST" "echo ok" >/dev/null 2>&1; then
    echo "${RED}✗ Cannot SSH to $DEPLOY_HOST — check keys and connectivity${NC}"
    exit 1
  fi
  echo "  ${GREEN}✓${NC} SSH connection verified"

  # ── System packages (first time only) ──
  if $DEPLOY_ONLY; then
    echo "  ${DIM}--deploy-only: skipping package install${NC}"
  elif ssh "$DEPLOY_HOST" "test -f /var/lib/vulos/.setup-complete" 2>/dev/null; then
    echo "  ${GREEN}✓${NC} System packages already installed (skipping)"
  else
    echo "${BLUE}▸ First-time setup — installing system packages...${NC}"
    ssh "$DEPLOY_HOST" sh -s << 'SETUP_EOF'
set -e
export DEBIAN_FRONTEND=noninteractive

# Enable non-free repos
sed -i 's/Components: main/Components: main contrib non-free non-free-firmware/' /etc/apt/sources.list.d/debian.sources 2>/dev/null || true

apt-get update
apt-get install -y --no-install-recommends \
    tini bash sudo python3 curl jq ca-certificates wget \
    iproute2 iptables \
    xvfb chromium xdotool \
    gstreamer1.0-tools gstreamer1.0-plugins-base gstreamer1.0-plugins-good gstreamer1.0-plugins-bad \
    gstreamer1.0-vaapi \
    pulseaudio pulseaudio-utils \
    fonts-noto socat \
    mesa-va-drivers mesa-vulkan-drivers libva2 vainfo \
    bluez bluez-tools pulseaudio-module-bluetooth \
    joystick evtest libevdev2 \
    matchbox-window-manager x11-xserver-utils \
    labwc cage \
    flatpak rsync systemd systemd-sysv \
    plymouth plymouth-themes
    flatpak rsync systemd systemd-sysv \
    avahi-daemon avahi-utils dhcpcd5 wpasupplicant
    flatpak rsync systemd systemd-sysv \
    openssh-server

# Intel VA-API (amd64 only)
dpkg --print-architecture | grep -q amd64 && \
    apt-get install -y --no-install-recommends intel-media-va-driver-non-free || true

apt-get clean
rm -rf /var/lib/apt/lists/*

# Hardened sshd configuration
mkdir -p /etc/ssh/sshd_config.d
cat > /etc/ssh/sshd_config.d/vulos.conf << 'SSHD_CONF'
# Vulos — hardened sshd config
# Key-only auth — no passwords
PasswordAuthentication no
ChallengeResponseAuthentication no
UsePAM no

# Root login only via key
PermitRootLogin prohibit-password

# Hardening
X11Forwarding no
MaxAuthTries 3
LoginGraceTime 30

# Keep alive (detect dead connections)
ClientAliveInterval 60
ClientAliveCountMax 3
SSHD_CONF

# Flatpak
flatpak remote-add --if-not-exists flathub https://flathub.org/repo/flathub.flatpakrepo

# System config
groupadd -f sudo 2>/dev/null || true
echo "%sudo ALL=(ALL) ALL" > /etc/sudoers.d/sudo-group
chmod 440 /etc/sudoers.d/sudo-group

# Directories
mkdir -p /opt/vulos/webroot /opt/vulos/apps \
    /var/lib/vulos /root/.vulos/data /root/.vulos/db /root/.vulos/sandbox \
    /root/.vulos/browser/extensions \
    /tmp/xdg-runtime

# Chromium policy — suppress sandbox warning
mkdir -p /etc/chromium/policies/managed
cat > /etc/chromium/policies/managed/vulos.json << 'POL'
{
  "CommandLineFlagSecurityWarningsEnabled": false,
  "PasswordManagerEnabled": false,
  "AutofillAddressEnabled": false,
  "AutofillCreditCardEnabled": false,
  "TranslateEnabled": false,
  "BookmarkBarEnabled": false,
  "BrowserSignin": 0,
  "SyncDisabled": true,
  "SearchSuggestEnabled": false,
  "SafeBrowsingEnabled": false,
  "MetricsReportingEnabled": false,
  "DefaultBrowserSettingEnabled": false,
  "PromotionalTabsEnabled": false,
  "HardwareAccelerationModeEnabled": false,
  "BackgroundModeEnabled": false,
  "ImportBookmarks": false,
  "ImportSavedPasswords": false,
  "ImportSearchEngine": false,
  "ImportHistory": false,
  "PasswordLeakDetectionEnabled": false
}
POL

# Hostname
echo "vula" > /etc/hostname

# Mark setup complete
touch /var/lib/vulos/.setup-complete
echo "System setup complete"
SETUP_EOF
    echo "  ${GREEN}✓${NC} System packages installed"
  fi

  # ── Caddy (if --domain provided) ──
  if [ -n "$DOMAIN" ]; then
    # Build Caddy with Namecheap plugin if not present
    if ssh "$DEPLOY_HOST" "test -x /usr/local/bin/caddy" 2>/dev/null; then
      echo "  ${GREEN}✓${NC} Caddy binary exists (skipping build)"
    else
      echo "${BLUE}▸ Building Caddy with Namecheap DNS plugin...${NC}"
      ssh "$DEPLOY_HOST" sh -s << 'CADDY_BUILD_EOF'
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends golang-go git
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
~/go/bin/xcaddy build --with github.com/caddy-dns/namecheap --output /usr/local/bin/caddy
apt-get remove -y golang-go && apt-get autoremove -y
rm -rf ~/go /root/.cache/go-build
setcap cap_net_bind_service=+ep /usr/local/bin/caddy
echo "Caddy built"
CADDY_BUILD_EOF
      echo "  ${GREEN}✓${NC} Caddy built with Namecheap DNS"
    fi

    # Configure Caddy — always update (domain/creds may change)
    echo "${BLUE}▸ Configuring Caddy for $DOMAIN...${NC}"

    # Create user + dirs
    ssh "$DEPLOY_HOST" "id caddy >/dev/null 2>&1 || useradd --system --home /var/lib/caddy --shell /usr/sbin/nologin caddy; mkdir -p /var/lib/caddy/.local/share/caddy /var/lib/caddy/.config/caddy /etc/caddy; chown -R caddy:caddy /var/lib/caddy"

    # Write Caddyfile (use printf to expand $DOMAIN correctly)
    ssh "$DEPLOY_HOST" "printf '%s\n' \
'{' \
'    acme_dns namecheap {' \
'        api_key {env.NAMECHEAP_API_KEY}' \
'        user {env.NAMECHEAP_API_USER}' \
'    }' \
'}' \
'' \
'$DOMAIN {' \
'    reverse_proxy localhost:8080' \
'}' \
'' \
'*.$DOMAIN {' \
'    reverse_proxy localhost:8080' \
'}' > /etc/caddy/Caddyfile"

    # Write env file with credentials
    ssh "$DEPLOY_HOST" "printf 'NAMECHEAP_API_USER=%s\nNAMECHEAP_API_KEY=%s\n' '$NC_USER' '$NC_KEY' > /etc/caddy/env; chmod 600 /etc/caddy/env"

    # Write systemd service for Caddy
    ssh "$DEPLOY_HOST" sh -s << 'CADDY_SVC_EOF'
cat > /etc/systemd/system/caddy.service << 'SVC'
[Unit]
Description=Caddy Web Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=caddy
Group=caddy
ExecStart=/usr/local/bin/caddy run --config /etc/caddy/Caddyfile --adapter caddyfile
ExecReload=/usr/local/bin/caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576
AmbientCapabilities=CAP_NET_BIND_SERVICE
EnvironmentFile=/etc/caddy/env

[Install]
WantedBy=multi-user.target
SVC

systemctl daemon-reload
systemctl enable caddy.service
CADDY_SVC_EOF
    echo "  ${GREEN}✓${NC} Caddy configured for $DOMAIN + *.$DOMAIN"
  fi

  # ── Vulos systemd service (always update) ──
  VULOS_ENV_DOMAIN=""
  [ -n "$DOMAIN" ] && VULOS_ENV_DOMAIN="Environment=VULOS_DOMAIN=$DOMAIN"
  ssh "$DEPLOY_HOST" "cat > /etc/systemd/system/vulos.service << SVC
[Unit]
Description=Vulos Server
After=network.target

[Service]
Type=simple
ExecStartPre=-/usr/bin/plymouth quit --retain-splash
ExecStart=/usr/local/bin/vulos-server -env main
Restart=on-failure
RestartSec=3
Environment=PORT=8080
Environment=VULOS_REGISTRY=/opt/vulos/registry.json
Environment=SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
Environment=XDG_RUNTIME_DIR=/tmp/xdg-runtime
Environment=SHELL=/bin/bash
Environment=HOSTNAME=vula
Environment=HOME=/root
$VULOS_ENV_DOMAIN

[Install]
WantedBy=multi-user.target
SVC
systemctl daemon-reload"

  # ── Stop service, copy files, restart ──
  ssh "$DEPLOY_HOST" "systemctl stop vulos.service 2>/dev/null || true; pkill -9 -f vulos-server 2>/dev/null || true; sleep 1"

  echo "${BLUE}▸ Copying files...${NC}"
  scp -q "$OUTDIR/vulos-server" "$DEPLOY_HOST:/usr/local/bin/vulos-server"
  scp -q "$OUTDIR/vulos-init" "$DEPLOY_HOST:/usr/local/bin/vulos-init"
  echo "  ${GREEN}✓${NC} binaries"

  scp -q "$OUTDIR/registry.json" "$DEPLOY_HOST:/opt/vulos/registry.json"
  [ -f "$OUTDIR/xdg-open" ] && scp -q "$OUTDIR/xdg-open" "$DEPLOY_HOST:/usr/local/bin/xdg-open"
  echo "  ${GREEN}✓${NC} registry + scripts"

  # Sync webroot + apps
  if command -v rsync >/dev/null 2>&1 && ssh "$DEPLOY_HOST" "command -v rsync >/dev/null 2>&1"; then
    rsync -az --delete "$OUTDIR/webroot/" "$DEPLOY_HOST:/opt/vulos/webroot/"
    rsync -az --delete "$OUTDIR/apps/" "$DEPLOY_HOST:/opt/vulos/apps/"
  else
    ssh "$DEPLOY_HOST" "rm -rf /opt/vulos/webroot /opt/vulos/apps"
    scp -rq "$OUTDIR/webroot" "$DEPLOY_HOST:/opt/vulos/webroot"
    scp -rq "$OUTDIR/apps" "$DEPLOY_HOST:/opt/vulos/apps"
  fi
  echo "  ${GREEN}✓${NC} webroot + apps"

  # Set permissions + restart
  ssh "$DEPLOY_HOST" sh -s << 'RESTART_EOF'
chmod +x /usr/local/bin/vulos-server /usr/local/bin/vulos-init
[ -f /usr/local/bin/xdg-open ] && chmod +x /usr/local/bin/xdg-open && \
    rm -f /usr/bin/xdg-open && ln -s /usr/local/bin/xdg-open /usr/bin/xdg-open
systemctl start vulos.service
[ -f /etc/caddy/Caddyfile ] && systemctl restart caddy.service
echo "Services started"
RESTART_EOF
  echo "  ${GREEN}✓${NC} Services started"

  echo ""
  echo "${GREEN}═══════════════════════════════════${NC}"
  echo "${GREEN}Deployed to $DEPLOY_HOST${NC}"
  if [ -n "$DOMAIN" ]; then
    echo "${GREEN}OS:    https://$DOMAIN${NC}"
    echo "${GREEN}Apps:  https://{app}.$DOMAIN${NC}"
    SERVER_IP=$(echo "$DEPLOY_HOST" | sed 's/.*@//')
    echo ""
    echo "${BLUE}NOTE:${NC} Ensure $SERVER_IP is whitelisted in Namecheap API Access:"
    echo "  Namecheap → Profile → Tools → API Access → Whitelisted IPs"
  else
    echo "${GREEN}OS running on port 8080${NC}"
  fi
  echo "${GREEN}═══════════════════════════════════${NC}"
  exit 0
fi

# ═══════════════════════════════════
# 4. Build Debian rootfs (local image)
# ═══════════════════════════════════
if ! command -v debootstrap >/dev/null 2>&1; then
    echo ""
    echo "  ${DIM}debootstrap not found — skipping rootfs build${NC}"
    echo "  ${DIM}Install: apt-get install debootstrap${NC}"
    echo ""
    echo "Binaries and frontend built to $OUTDIR"
    exit 0
fi

# For arm64 cross-debootstrap on a non-arm64 host, qemu-user-static is required
if [ "$ARCH" = "arm64" ] && [ "${QEMU_MISSING:-0}" = "1" ]; then
    echo ""
    echo "${RED}Cannot build arm64 rootfs: qemu-user-static is missing.${NC}"
    echo "${RED}Install with: apt-get install qemu-user-static${NC}"
    echo ""
    exit 1
fi

ROOTFS="$OUTDIR/rootfs"
if [ "$REUSE_ROOTFS" = "1" ] && [ -x "$ROOTFS/bin/bash" ] && ls "$ROOTFS"/boot/vmlinuz-* >/dev/null 2>&1; then
  echo "${BLUE}▸ Reusing cached rootfs at $ROOTFS (--reuse-rootfs)${NC}"
else
echo "${BLUE}▸ Building Debian rootfs with debootstrap (arch=$ARCH)...${NC}"
rm -rf "$ROOTFS"

debootstrap --arch="$ARCH" --variant=minbase "$SUITE" "$ROOTFS" http://deb.debian.org/debian

chroot "$ROOTFS" sh -c 'sed -i "s/Components: main/Components: main contrib non-free non-free-firmware/" /etc/apt/sources.list.d/debian.sources 2>/dev/null || true'

chroot "$ROOTFS" apt-get update
# Single install list. Trailing-backslash continuation must be unbroken — a
# stray newline here previously split this into bare `flatpak …` commands that
# aborted the build under `set -e` (the rootfs never finished building).
# linux-image-${GOARCH} + initramfs-tools: bootable kernel + initrd.
# systemd-boot-efi: UEFI bootloader stub copied into the ESP by --disk/--live.
chroot "$ROOTFS" apt-get install -y --no-install-recommends \
    tini bash sudo python3 curl jq ca-certificates wget \
    iproute2 iptables \
    xvfb chromium xdotool \
    gstreamer1.0-tools gstreamer1.0-plugins-base gstreamer1.0-plugins-good gstreamer1.0-plugins-bad \
    gstreamer1.0-vaapi \
    pulseaudio pulseaudio-utils \
    fonts-noto socat \
    mesa-va-drivers mesa-vulkan-drivers libva2 vainfo \
    bluez bluez-tools pulseaudio-module-bluetooth \
    joystick evtest libevdev2 \
    matchbox-window-manager x11-xserver-utils \
    labwc cage \
    flatpak rsync systemd systemd-sysv \
    plymouth plymouth-themes \
    avahi-daemon avahi-utils dhcpcd5 wpasupplicant \
    openssh-server \
    initramfs-tools "linux-image-${GOARCH}" systemd-boot-efi

[ "$ARCH" = "amd64" ] && chroot "$ROOTFS" apt-get install -y --no-install-recommends intel-media-va-driver-non-free || true

# ── ARM device-specific packages and firmware ──
if [ "$ARCH" = "arm64" ]; then
  case "$DEVICE" in
    rpi4)
      echo "${BLUE}  ▸ Installing RPi 4 firmware and kernel...${NC}"
      chroot "$ROOTFS" apt-get install -y --no-install-recommends \
          raspi-firmware linux-image-arm64 || true

      # ESP config: minimal RPi 4 config.txt and cmdline.txt
      ESP="$OUTDIR/esp-rpi4"
      mkdir -p "$ESP"
      cat > "$ESP/config.txt" << 'RPI4_CONFIG'
# Vulos — Raspberry Pi 4 boot configuration
arm_64bit=1
enable_uart=1
dtoverlay=disable-bt
gpu_mem=64
RPI4_CONFIG
      cat > "$ESP/cmdline.txt" << 'RPI4_CMDLINE'
console=serial0,115200 console=tty1 root=/dev/mmcblk0p2 rootfstype=ext4 elevator=deadline fsck.repair=yes rootwait quiet
RPI4_CMDLINE
      echo "  ${GREEN}✓${NC} RPi 4 config.txt + cmdline.txt written to $ESP"
      ;;
    pinephone)
      echo "${BLUE}  ▸ Installing PinePhone kernel...${NC}"
      # Install generic arm64 kernel; pinephone-specific firmware if available
      chroot "$ROOTFS" apt-get install -y --no-install-recommends \
          linux-image-arm64 || true
      # Attempt pinephone firmware package; degrade gracefully if unavailable
      if chroot "$ROOTFS" apt-cache show firmware-pine64-pinephone >/dev/null 2>&1; then
        chroot "$ROOTFS" apt-get install -y --no-install-recommends \
            firmware-pine64-pinephone || true
      else
        echo "  ${DIM}firmware-pine64-pinephone not available in $SUITE — skipping${NC}"
      fi
      ;;
    generic-arm64)
      echo "${BLUE}  ▸ Installing generic ARM64 kernel...${NC}"
      chroot "$ROOTFS" apt-get install -y --no-install-recommends \
          linux-image-arm64 || true
      ;;
  esac
fi

chroot "$ROOTFS" apt-get clean
rm -rf "$ROOTFS/var/lib/apt/lists/"*
# Pre-register Flathub. Non-fatal: flatpak's ostree repo init can fail in a
# chroot on some build filesystems ("linkat: No such file or directory");
# the remote is a convenience and is re-addable at runtime — it must not
# abort the image build under `set -e`.
chroot "$ROOTFS" flatpak remote-add --if-not-exists flathub https://flathub.org/repo/flathub.flatpakrepo \
  || echo "  ${DIM}warn: flatpak remote-add skipped (non-fatal, re-addable at runtime)${NC}"
fi  # end rootfs build / reuse guard

# Hardened sshd configuration (rootfs)
mkdir -p "$ROOTFS/etc/ssh/sshd_config.d"
cat > "$ROOTFS/etc/ssh/sshd_config.d/vulos.conf" << 'SSHD_CONF'
# Vulos — hardened sshd config
# Key-only auth — no passwords
PasswordAuthentication no
ChallengeResponseAuthentication no
UsePAM no

# Root login only via key
PermitRootLogin prohibit-password

# Hardening
X11Forwarding no
MaxAuthTries 3
LoginGraceTime 30

# Keep alive (detect dead connections)
ClientAliveInterval 60
ClientAliveCountMax 3
SSHD_CONF

cp "$OUTDIR/vulos-server" "$ROOTFS/usr/local/bin/"
cp "$OUTDIR/vulos-init" "$ROOTFS/sbin/vulos-init"
chmod +x "$ROOTFS/usr/local/bin/vulos-server" "$ROOTFS/sbin/vulos-init"

if [ -f "$OUTDIR/xdg-open" ]; then
    cp "$OUTDIR/xdg-open" "$ROOTFS/usr/local/bin/xdg-open"
    chmod +x "$ROOTFS/usr/local/bin/xdg-open"
    rm -f "$ROOTFS/usr/bin/xdg-open"
    ln -s /usr/local/bin/xdg-open "$ROOTFS/usr/bin/xdg-open"
fi

mkdir -p "$ROOTFS/opt/vulos"
# Under --reuse-rootfs the destination dirs already exist from the previous
# build. `cp -r src dst` semantics: when dst is an existing dir, cp creates
# `dst/$(basename src)` instead of replacing — so new builds landed at
# /opt/vulos/webroot/webroot/, while index.html at the top level stayed the
# OLD one and the kiosk kept serving the previous bundle (a real bug we
# hit: src fixes never reached the booted image). Clear first, then copy.
rm -rf "$ROOTFS/opt/vulos/webroot" "$ROOTFS/opt/vulos/apps"
cp -r "$OUTDIR/webroot" "$ROOTFS/opt/vulos/webroot"
cp -r "$OUTDIR/apps" "$ROOTFS/opt/vulos/apps"
cp "$OUTDIR/registry.json" "$ROOTFS/opt/vulos/registry.json"

# BMINIT-07: Plymouth boot splash — vulos theme
# Kernel cmdline: quiet splash plymouth.theme=vulos
mkdir -p "$ROOTFS/usr/share/plymouth/themes/vulos"
cp -r "$ROOT_DIR/assets/plymouth/themes/vulos/." "$ROOTFS/usr/share/plymouth/themes/vulos/"
chroot "$ROOTFS" plymouth-set-default-theme vulos 2>/dev/null || \
    ln -sf /usr/share/plymouth/themes/vulos/vulos.plymouth \
        "$ROOTFS/etc/alternatives/default.plymouth" 2>/dev/null || true

# Force the framebuffer/splash plugin into the initramfs so the branded
# splash is shown from very early boot (before root pivot), not just late.
mkdir -p "$ROOTFS/etc/initramfs-tools/conf.d"
echo "FRAMEBUFFER=y" > "$ROOTFS/etc/initramfs-tools/conf.d/vulos-splash.conf"

# ═══════════════════════════════════
# SEED-01: Embed trust-anchor public key
#
# The signing public key (trust anchor) is baked into the seed at a well-known
# path (/etc/vulos/trust-anchor.pub) so the early-boot verify step (VERITY-02)
# can validate the fetched OS chain before pivot_root.  The key also lands
# inside the initramfs cpio via the vulos-trust-anchor hook installed here.
#
# Key is resolved by scripts/seed/embed-anchor.sh in order:
#   1. $VULOS_TRUST_ANCHOR_PUBKEY  — explicit path (production builds)
#   2. keys/trust-anchor.pub       — dev fallback (build warns loudly)
#   missing                        → build fails loudly; cannot produce an
#                                    unverifiable image.
#
# Changing the trust anchor requires re-flashing the seed — it is immutable
# once the image is produced.  See roadmap/SEED-TRUST.md and
# scripts/seed/README.md for the full contract.
# ═══════════════════════════════════
echo "${BLUE}▸ Embedding trust-anchor public key (SEED-01)...${NC}"
"$ROOT_DIR/scripts/seed/embed-anchor.sh" "$ROOTFS"

# ═══════════════════════════════════
# SEED-03: Embed OS bucket URL
#
# The OS bucket URL tells the seed *where* to fetch the OS image manifest
# (os/stable.json) and squashfs images.  It is baked into the seed alongside
# the trust anchor so that location + trust always travel together.
#
# Baked path: /etc/vulos/os-bucket-url
#   - Read by the early-boot fetch stage (and later by the updater daemon).
#   - Writable at runtime (it is "soft" config — mirrors/failover are safe).
#     The key alone enforces trust; a different URL cannot serve a forged OS
#     because the signature check is against the baked key, not the location.
#
# Resolution order:
#   1. $VULOS_OS_BUCKET_URL  — explicit env var (fork builds + production)
#   2. Upstream default      — https://os.vulos.org  (upstream / dev builds)
#
# Fork constraint: when VULOS_TRUST_ANCHOR_PUBKEY is set (i.e. this is
# explicitly a non-upstream build) VULOS_OS_BUCKET_URL MUST also be set.
# Allowing a fork key with the upstream bucket would produce a seed that rejects
# upstream-signed images AND points at the wrong bucket — a broken build that
# would silently never fetch anything.  We fail loud instead.
# ═══════════════════════════════════
echo "${BLUE}▸ Embedding OS bucket URL (SEED-03)...${NC}"

# Detect fork build: explicit key env var means the caller is NOT the upstream.
_IS_FORK_BUILD=0
if [ -n "${VULOS_TRUST_ANCHOR_PUBKEY:-}" ]; then
    _IS_FORK_BUILD=1
fi

# Resolve bucket URL
_BUCKET_URL="${VULOS_OS_BUCKET_URL:-}"
if [ -z "$_BUCKET_URL" ]; then
    if [ "$_IS_FORK_BUILD" = "1" ]; then
        echo "${RED}✗ SEED-03: VULOS_OS_BUCKET_URL must be set when building with a custom${NC}"
        echo "${RED}   VULOS_TRUST_ANCHOR_PUBKEY.  A fork seed needs its own bucket URL so${NC}"
        echo "${RED}   location + trust travel together.  Set VULOS_OS_BUCKET_URL to the${NC}"
        echo "${RED}   HTTPS root of your OS bucket and re-run.${NC}"
        echo "${RED}   See roadmap/SEED-TRUST.md — Fork Procedure.${NC}"
        exit 1
    fi
    # Upstream / dev build: use the canonical upstream bucket
    _BUCKET_URL="https://os.vulos.org"
    echo "  ${DIM}VULOS_OS_BUCKET_URL not set — using upstream default: $_BUCKET_URL${NC}"
fi

# Write bucket URL into the seed rootfs
BUCKET_URL_DEST="$ROOTFS/etc/vulos/os-bucket-url"
# /etc/vulos already exists (created by embed-anchor.sh above)
mkdir -p "$ROOTFS/etc/vulos"
printf '%s\n' "$_BUCKET_URL" > "$BUCKET_URL_DEST"
chmod 0644 "$BUCKET_URL_DEST"   # readable by all; writable by root (soft config)
echo "  ${GREEN}✓${NC} OS bucket URL embedded → /etc/vulos/os-bucket-url ($_BUCKET_URL)"

# Install initramfs hook so the bucket URL is also available before pivot_root
_BUCKET_HOOK="$ROOTFS/etc/initramfs-tools/hooks/vulos-os-bucket-url"
cat > "$_BUCKET_HOOK" << 'BUCKET_HOOK_BODY'
#!/bin/sh
# initramfs-tools hook — embed Vulos OS bucket URL into initramfs (SEED-03).
# The URL at /etc/vulos/os-bucket-url is copied verbatim into the cpio archive
# so the early-boot fetch stage can read it before pivot_root.
PREREQ=""
prereqs() { echo "$PREREQ"; }
case "$1" in
    prereqs) prereqs; exit 0 ;;
esac

. /usr/share/initramfs-tools/hook-functions

URL_SRC="/etc/vulos/os-bucket-url"
if [ ! -f "$URL_SRC" ]; then
    echo "ERROR: vulos-os-bucket-url hook: $URL_SRC not found — aborting initramfs build" >&2
    exit 1
fi

mkdir -p "${DESTDIR}/etc/vulos"
cp "$URL_SRC" "${DESTDIR}/etc/vulos/os-bucket-url"
chmod 0644 "${DESTDIR}/etc/vulos/os-bucket-url"
BUCKET_HOOK_BODY

chmod 0755 "$_BUCKET_HOOK"
echo "  ${GREEN}✓${NC} OS bucket URL initramfs hook installed"
unset _IS_FORK_BUILD _BUCKET_URL _BUCKET_HOOK

# Regenerate every installed initrd with the vulos theme + plymouth hook +
# trust-anchor hook baked in.  Runs on both fresh and --reuse-rootfs builds
# (kernel already present).  Must run AFTER embed-anchor.sh so the
# vulos-trust-anchor initramfs hook is in place before update-initramfs.
chroot "$ROOTFS" sh -c 'command -v update-initramfs >/dev/null 2>&1 && update-initramfs -u -k all' \
    || echo "  ${DIM}update-initramfs unavailable — splash/trust-anchor will be late-stage only${NC}"

mkdir -p "$ROOTFS/root/.vulos/data" "$ROOTFS/root/.vulos/db" \
    "$ROOTFS/root/.vulos/sandbox" "$ROOTFS/root/.vulos/browser/extensions" \
    "$ROOTFS/tmp/xdg-runtime"

mkdir -p "$ROOTFS/etc/chromium/policies/managed"
# Real bare-metal Chromium policy: kiosk-correct defaults (no password-manager
# prompts, no autofill, no translate, no signin/sync, no bookmark bar, no
# safebrowsing/metrics/promotion popups). Stable across versions — these
# enterprise policies are more durable than CLI flags. Must mirror the
# --deploy path policy block above.
cat > "$ROOTFS/etc/chromium/policies/managed/vulos.json" << 'POL'
{
  "CommandLineFlagSecurityWarningsEnabled": false,
  "PasswordManagerEnabled": false,
  "AutofillAddressEnabled": false,
  "AutofillCreditCardEnabled": false,
  "TranslateEnabled": false,
  "BookmarkBarEnabled": false,
  "BrowserSignin": 0,
  "SyncDisabled": true,
  "SearchSuggestEnabled": false,
  "SafeBrowsingEnabled": false,
  "MetricsReportingEnabled": false,
  "DefaultBrowserSettingEnabled": false,
  "PromotionalTabsEnabled": false,
  "HardwareAccelerationModeEnabled": false,
  "BackgroundModeEnabled": false,
  "ImportBookmarks": false,
  "ImportSavedPasswords": false,
  "ImportSearchEngine": false,
  "ImportHistory": false,
  "PasswordLeakDetectionEnabled": false
}
POL

# labwc compositor config (browser pinned to background, floating focus)
mkdir -p "$ROOTFS/root/.config/labwc"
cp -r assets/labwc/. "$ROOTFS/root/.config/labwc/"

# Vulos traffic-light openbox theme for labwc SSD
mkdir -p "$ROOTFS/usr/share/themes/vulos/openbox-3"
cp -r assets/themes/vulos/. "$ROOTFS/usr/share/themes/vulos/"

echo "vula" > "$ROOTFS/etc/hostname"
echo "%sudo ALL=(ALL) ALL" > "$ROOTFS/etc/sudoers.d/sudo-group"
chmod 440 "$ROOTFS/etc/sudoers.d/sudo-group"

cat > "$ROOTFS/etc/systemd/system/vulos.service" << 'EOF'
[Unit]
Description=Vulos Server
After=network.target

[Service]
Type=simple
ExecStartPre=-/usr/bin/plymouth quit --retain-splash
ExecStart=/usr/local/bin/vulos-server -env main
Restart=on-failure
RestartSec=3
Environment=PORT=8080
Environment=VULOS_REGISTRY=/opt/vulos/registry.json
Environment=SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
Environment=XDG_RUNTIME_DIR=/tmp/xdg-runtime
Environment=SHELL=/bin/bash
Environment=HOSTNAME=vula
Environment=HOME=/root

[Install]
WantedBy=multi-user.target
EOF

chroot "$ROOTFS" systemctl enable vulos.service

mkdir -p "$ROOTFS/var/lib/vulos"
touch "$ROOTFS/var/lib/vulos/.setup-complete"

echo "  ${GREEN}✓${NC} rootfs built"

# ═══════════════════════════════════
# 5. Live-USB image (--live only)
#
# Produces a genuinely UEFI-bootable GPT image:
#   Partition 1: ESP  — FAT32, 220 MiB  (systemd-boot + kernel + initrd + loader entry)
#   Partition 2: data — ext4             (contains image.squashfs as a plain file)
#
# The initramfs init-bottom hook (scripts/initramfs/vulos-live) is installed into
# the rootfs before packing the squashfs; the hook mounts the data partition (set as
# root= in the cmdline), finds /image.squashfs there, and overlays it with a tmpfs
# upper layer so the live system is fully writable.
#
# Built without loop devices or privileged mounts (mke2fs -d + mtools), works in
# Docker/CI.
# ═══════════════════════════════════
if [ "$LIVE_MODE" = "1" ]; then
  echo "${BLUE}▸ Building live-USB image...${NC}"

  # --- Guard: require host tools ---
  LIVE_MISSING=""
  for _tool in mksquashfs parted mkfs.vfat mkfs.ext4 mmd mcopy dd; do
    if ! command -v "$_tool" >/dev/null 2>&1; then
      LIVE_MISSING="$LIVE_MISSING $_tool"
    fi
  done
  if [ -n "$LIVE_MISSING" ]; then
    echo "${RED}✗ Missing host tools required for --live:$LIVE_MISSING${NC}"
    echo "  Install squashfs-tools parted dosfstools e2fsprogs mtools and retry."
    exit 1
  fi

  # Check systemd-boot EFI stub (same logic as --disk)
  case "$GOARCH" in
    arm64) LIVE_EFI_NAME="BOOTAA64.EFI"; LIVE_SDBOOT="usr/lib/systemd/boot/efi/systemd-bootaa64.efi" ;;
    amd64) LIVE_EFI_NAME="BOOTX64.EFI";  LIVE_SDBOOT="usr/lib/systemd/boot/efi/systemd-bootx64.efi" ;;
    *)     echo "${RED}✗ --live: unsupported arch $GOARCH${NC}"; exit 1 ;;
  esac
  if [ ! -f "$ROOTFS/$LIVE_SDBOOT" ]; then
    echo "${RED}✗ systemd-boot stub missing ($LIVE_SDBOOT) — is systemd-boot-efi installed?${NC}"
    exit 1
  fi

  # --- Install squashfs-tools in the chroot ---
  chroot "$ROOTFS" apt-get install -y --no-install-recommends squashfs-tools
  chroot "$ROOTFS" apt-get clean
  rm -rf "$ROOTFS/var/lib/apt/lists/"*

  # --- Install initramfs hook into rootfs (before packing squashfs) ---
  # The hook activates when 'vulos.live' is present in /proc/cmdline.
  # It finds image.squashfs at ${rootmnt}/image.squashfs (ext4 data partition),
  # mounts it read-only as the lower squashfs layer, adds a tmpfs upper layer,
  # and presents the overlay as the real root via overlayfs.
  INITRAMFS_HOOK_DIR="$ROOTFS/etc/initramfs-tools/scripts/init-bottom"
  mkdir -p "$INITRAMFS_HOOK_DIR"
  cp "$ROOT_DIR/scripts/initramfs/vulos-live" "$INITRAMFS_HOOK_DIR/vulos-live"
  chmod 0755 "$INITRAMFS_HOOK_DIR/vulos-live"
  echo "  ${GREEN}✓${NC} initramfs hook installed in rootfs"

  # Regenerate initramfs so the hook is baked in
  chroot "$ROOTFS" sh -c 'command -v update-initramfs >/dev/null 2>&1 && update-initramfs -u -k all' \
      || echo "  ${DIM}update-initramfs unavailable — hook will be absent${NC}"

  # Locate kernel and initrd (needed for ESP population)
  LIVE_KIMG="$(ls -1 "$ROOTFS"/boot/vmlinuz-* 2>/dev/null | sort -V | tail -1)"
  LIVE_IIMG="$(ls -1 "$ROOTFS"/boot/initrd.img-* 2>/dev/null | sort -V | tail -1)"
  if [ -z "$LIVE_KIMG" ] || [ -z "$LIVE_IIMG" ]; then
    echo "${RED}✗ kernel/initrd missing in $ROOTFS/boot — is linux-image-$GOARCH installed?${NC}"
    exit 1
  fi
  echo "  ${DIM}kernel: $(basename "$LIVE_KIMG")  initrd: $(basename "$LIVE_IIMG")${NC}"

  # --- Pack squashfs from the rootfs ---
  SQUASHFS="$OUTDIR/image.squashfs"
  echo "${BLUE}▸ Packing squashfs (xz, this may take a while)...${NC}"
  mksquashfs "$ROOTFS" "$SQUASHFS" -comp xz -noappend -quiet
  echo "  ${GREEN}✓${NC} image.squashfs ($(du -h "$SQUASHFS" | cut -f1))"

  # ═══════════════════════════════════
  # VERITY-01: dm-verity Merkle tree + root hash (os-core.squashfs)
  #
  # Generate a dm-verity Merkle hash tree + root hash over the squashfs
  # so every block is verifiable at runtime.  The root hash is the image's
  # content identity (SIGNING.md) and is what stable.json pins in its
  # `roothash` field (OSDIST-01).
  #
  # Output files:
  #   os-core.hashtree  — binary Merkle hash tree (uploaded to OS bucket
  #                       alongside the squashfs; read by dm-verity at boot)
  #   os-core.roothash  — single hex line: the verity root hash, consumed
  #                       by the stable.json signing step (OSDIST-01)
  # ═══════════════════════════════════
  VERITY_HASHTREE="$OUTDIR/os-core.hashtree"
  VERITY_ROOTHASH_FILE="$OUTDIR/os-core.roothash"
  echo "${BLUE}▸ Generating dm-verity hash tree + root hash (VERITY-01)...${NC}"
  VERITY_ROOT_HASH="$("$ROOT_DIR/scripts/verity/gen-verity.sh" \
      "$SQUASHFS" "$VERITY_HASHTREE" "$VERITY_ROOTHASH_FILE")" || {
    echo "${RED}✗ gen-verity.sh failed — aborting live build${NC}"
    exit 1
  }
  if [ -n "$VERITY_ROOT_HASH" ]; then
    echo "  ${GREEN}✓${NC} dm-verity root hash: $VERITY_ROOT_HASH"
    echo "  ${GREEN}✓${NC} hash tree  → os-core.hashtree"
    echo "  ${GREEN}✓${NC} root hash  → os-core.roothash  (surfaced for stable.json signing)"
  else
    echo "  ${DIM}veritysetup unavailable — root hash not produced (install cryptsetup-bin for production builds)${NC}"
  fi

  # --- Build bootable GPT image ---
  LIVE_IMG="$OUTDIR/vulos-live-$ARCH.img"
  LIVE_ESP_MB=220
  SQUASHFS_MB=$(( $(du -m "$SQUASHFS" | cut -f1) + 32 ))   # squashfs + 32 MiB headroom
  DATA_MB=$(( SQUASHFS_MB + 16 ))                           # ext4 overhead
  IMG_MB=$(( 1 + LIVE_ESP_MB + DATA_MB + 5 ))               # 1 MiB GPT gap + 5 MiB spare

  echo "${BLUE}▸ Creating GPT image (${IMG_MB} MiB)...${NC}"
  rm -f "$LIVE_IMG"
  dd if=/dev/zero of="$LIVE_IMG" bs=1M count="$IMG_MB" status=none

  # Partition table: GPT, ESP at 1 MiB, data partition after ESP
  parted -s "$LIVE_IMG" \
    mklabel gpt \
    mkpart ESP  fat32 1MiB $(( 1 + LIVE_ESP_MB ))MiB \
    set 1 esp on \
    mkpart data ext4  $(( 1 + LIVE_ESP_MB ))MiB 100%

  # ── FAT32 ESP: systemd-boot + kernel + initrd + loader entry (mtools, no loop) ──
  LIVE_ESP_IMG="$OUTDIR/_live_esp.fat"
  rm -f "$LIVE_ESP_IMG"
  dd if=/dev/zero of="$LIVE_ESP_IMG" bs=1M count="$LIVE_ESP_MB" status=none
  mkfs.vfat -F 32 -n "EFI" "$LIVE_ESP_IMG" >/dev/null
  mmd  -i "$LIVE_ESP_IMG" ::/EFI ::/EFI/BOOT ::/loader ::/loader/entries
  mcopy -i "$LIVE_ESP_IMG" "$ROOTFS/$LIVE_SDBOOT"  "::/EFI/BOOT/$LIVE_EFI_NAME"
  mcopy -i "$LIVE_ESP_IMG" "$LIVE_KIMG"             "::/vmlinuz"
  mcopy -i "$LIVE_ESP_IMG" "$LIVE_IIMG"             "::/initrd.img"
  printf 'default vulos\ntimeout 5\nconsole-mode max\n' > "$OUTDIR/_live_loader.conf"
  mcopy -i "$LIVE_ESP_IMG" "$OUTDIR/_live_loader.conf" "::/loader/loader.conf"
  # root=LABEL=VULOS-LIVE-DATA: initramfs mounts the ext4 data partition, finds
  # /image.squashfs there, and overlays it with a tmpfs upper layer (vulos-live hook).
  # vulos.live: activates the hook. quiet splash plymouth.theme=vulos: branded splash.
  printf 'title  Vulos Live\nlinux  /vmlinuz\ninitrd /initrd.img\noptions root=LABEL=VULOS-LIVE-DATA ro vulos.live quiet splash plymouth.theme=vulos console=tty1 console=ttyAMA0,115200\n' \
    > "$OUTDIR/_live_entry.conf"
  mcopy -i "$LIVE_ESP_IMG" "$OUTDIR/_live_entry.conf" "::/loader/entries/vulos.conf"
  rm -f "$OUTDIR/_live_loader.conf" "$OUTDIR/_live_entry.conf"
  echo "  ${GREEN}✓${NC} ESP (systemd-boot, ${LIVE_ESP_MB} MiB)"

  # ── ext4 data partition: contains image.squashfs as a plain file (mke2fs -d) ──
  # The vulos-live initramfs hook expects to find /image.squashfs as a file on a
  # mounted filesystem (set as root= in the cmdline). Using ext4 with a -d staging
  # directory keeps this loop-device-free, consistent with --disk.
  LIVE_DATA_STAGING="$OUTDIR/_live_data_dir"
  LIVE_DATA_IMG="$OUTDIR/_live_data.ext4"
  rm -rf "$LIVE_DATA_STAGING"
  mkdir -p "$LIVE_DATA_STAGING"
  cp "$SQUASHFS" "$LIVE_DATA_STAGING/image.squashfs"
  rm -f "$LIVE_DATA_IMG"
  mkfs.ext4 -q -F -L "VULOS-LIVE-DATA" -d "$LIVE_DATA_STAGING" "$LIVE_DATA_IMG" "${DATA_MB}M"
  rm -rf "$LIVE_DATA_STAGING"
  echo "  ${GREEN}✓${NC} data ext4 (${DATA_MB} MiB, label VULOS-LIVE-DATA)"

  # ── Assemble GPT: ESP + data (offset dd, no loop device) ──
  LIVE_ESP_OFF=$(parted -s "$LIVE_IMG" unit B print | awk '/^ 1 /{print $2}' | tr -d 'B')
  LIVE_DATA_OFF=$(parted -s "$LIVE_IMG" unit B print | awk '/^ 2 /{print $2}' | tr -d 'B')
  dd if="$LIVE_ESP_IMG"  of="$LIVE_IMG" bs=1M seek=$(( LIVE_ESP_OFF  / 1048576 )) conv=notrunc status=none
  dd if="$LIVE_DATA_IMG" of="$LIVE_IMG" bs=1M seek=$(( LIVE_DATA_OFF / 1048576 )) conv=notrunc status=none
  rm -f "$LIVE_ESP_IMG" "$LIVE_DATA_IMG"

  echo "  ${GREEN}✓${NC} GPT image: vulos-live-$ARCH.img ($(du -h "$LIVE_IMG" | cut -f1))"
  echo "  ${DIM}  Partition 1: ESP (FAT32, ${LIVE_ESP_MB} MiB) — systemd-boot + kernel + initrd${NC}"
  echo "  ${DIM}  Partition 2: data (ext4, LABEL=VULOS-LIVE-DATA) — image.squashfs${NC}"
  echo ""
  echo "${GREEN}Live-USB image ready:${NC} $LIVE_IMG"
  echo "${GREEN}Flash with:${NC} dd if=$LIVE_IMG of=/dev/sdX bs=4M status=progress"
  echo ""
fi

# ═══════════════════════════════════
# 5a. Bootable UEFI disk image (--disk)
#
# Produces a genuinely UEFI-bootable GPT image: ESP (systemd-boot + kernel +
# initrd + loader entry) and an ext4 root. Built without loop devices or
# privileged mounts (mke2fs -d + mtools), so it works in Docker/CI/OrbStack.
# This is what the QEMU smoke harness boots. Replaces the old --live path,
# whose ESP was formatted but left empty (no bootloader → unbootable).
# ═══════════════════════════════════
if [ "$DISK_MODE" = "1" ]; then
  echo "${BLUE}▸ Building bootable UEFI disk image...${NC}"

  for _t in mkfs.ext4 mkfs.vfat mcopy mmd parted dd; do
    command -v "$_t" >/dev/null 2>&1 || {
      echo "${RED}✗ --disk needs '$_t' (install: e2fsprogs dosfstools mtools parted)${NC}"
      exit 1
    }
  done

  case "$GOARCH" in
    arm64) EFI_NAME="BOOTAA64.EFI"; SDBOOT="usr/lib/systemd/boot/efi/systemd-bootaa64.efi" ;;
    amd64) EFI_NAME="BOOTX64.EFI";  SDBOOT="usr/lib/systemd/boot/efi/systemd-bootx64.efi" ;;
    *)     echo "${RED}✗ --disk: unsupported arch $GOARCH${NC}"; exit 1 ;;
  esac
  if [ ! -f "$ROOTFS/$SDBOOT" ]; then
    echo "${RED}✗ systemd-boot stub missing ($SDBOOT) — is systemd-boot-efi installed?${NC}"
    exit 1
  fi

  KIMG="$(ls -1 "$ROOTFS"/boot/vmlinuz-* 2>/dev/null | sort -V | tail -1)"
  IIMG="$(ls -1 "$ROOTFS"/boot/initrd.img-* 2>/dev/null | sort -V | tail -1)"
  if [ -z "$KIMG" ] || [ -z "$IIMG" ]; then
    echo "${RED}✗ kernel/initrd missing in $ROOTFS/boot — is linux-image-$GOARCH installed?${NC}"
    exit 1
  fi
  echo "  ${DIM}kernel: $(basename "$KIMG")  initrd: $(basename "$IIMG")${NC}"

  # ── ext4 root, populated from $ROOTFS with no mount/loop (mke2fs -d) ──
  ROOT_MB=$(( $(du -sm "$ROOTFS" | cut -f1) + 1024 ))
  ROOT_IMG="$OUTDIR/_root.ext4"
  rm -f "$ROOT_IMG"
  mkfs.ext4 -q -F -L vulos-root -d "$ROOTFS" "$ROOT_IMG" "${ROOT_MB}M"
  echo "  ${GREEN}✓${NC} ext4 root (${ROOT_MB} MiB, label vulos-root)"

  # ── FAT32 ESP: systemd-boot + kernel + initrd + loader entry (mtools) ──
  ESP_MB=220
  ESP_IMG="$OUTDIR/_esp.fat"
  rm -f "$ESP_IMG"
  dd if=/dev/zero of="$ESP_IMG" bs=1M count="$ESP_MB" status=none
  mkfs.vfat -F 32 -n ESP "$ESP_IMG" >/dev/null
  mmd  -i "$ESP_IMG" ::/EFI ::/EFI/BOOT ::/loader ::/loader/entries
  mcopy -i "$ESP_IMG" "$ROOTFS/$SDBOOT" "::/EFI/BOOT/$EFI_NAME"
  mcopy -i "$ESP_IMG" "$KIMG"           "::/vmlinuz"
  mcopy -i "$ESP_IMG" "$IIMG"           "::/initrd.img"
  printf 'default vulos\ntimeout 0\nconsole-mode max\n' > "$OUTDIR/_loader.conf"
  mcopy -i "$ESP_IMG" "$OUTDIR/_loader.conf" "::/loader/loader.conf"
  # root=LABEL avoids dependence on disk enumeration order. vulos.kiosk=force
  # makes vulos-init start the compositor even when DRM reports no connected
  # output (QEMU virtio-gpu). splash + plymouth.theme=vulos → branded splash.
  printf 'title  Vulos\nlinux  /vmlinuz\ninitrd /initrd.img\noptions root=LABEL=vulos-root rw init=/sbin/vulos-init quiet splash plymouth.theme=vulos vulos.kiosk=force console=tty1 console=ttyAMA0,115200\n' > "$OUTDIR/_entry.conf"
  mcopy -i "$ESP_IMG" "$OUTDIR/_entry.conf" "::/loader/entries/vulos.conf"
  rm -f "$OUTDIR/_loader.conf" "$OUTDIR/_entry.conf"
  echo "  ${GREEN}✓${NC} ESP (systemd-boot, ${ESP_MB} MiB)"

  # ── Assemble GPT: p1=ESP, p2=root (offset dd, no loop device) ──
  DISK_IMG="$OUTDIR/vulos-${ARCH}.img"
  # The ext4 image is sparse: `du` reports ALLOCATED blocks, far less than the
  # filesystem's logical size (ROOT_MB, what we passed to mkfs.ext4). Sizing
  # the disk/partition from `du` makes parted's 100% root partition SMALLER
  # than the ext4 it contains → the kernel sees "fs larger than device" and
  # fails with `mount: Invalid argument`. Use the known logical size.
  ROOT_SZ_MB="$ROOT_MB"
  IMG_MB=$(( 1 + ESP_MB + ROOT_SZ_MB + 5 ))
  rm -f "$DISK_IMG"
  dd if=/dev/zero of="$DISK_IMG" bs=1M count="$IMG_MB" status=none
  parted -s "$DISK_IMG" \
    mklabel gpt \
    mkpart ESP  fat32 1MiB $(( 1 + ESP_MB ))MiB \
    set 1 esp on \
    mkpart root ext4  $(( 1 + ESP_MB ))MiB 100%
  ESP_OFF=$(parted -s "$DISK_IMG" unit B print | awk '/^ 1 /{print $2}' | tr -d 'B')
  ROOT_OFF=$(parted -s "$DISK_IMG" unit B print | awk '/^ 2 /{print $2}' | tr -d 'B')
  dd if="$ESP_IMG"  of="$DISK_IMG" bs=1M seek=$(( ESP_OFF  / 1048576 )) conv=notrunc status=none
  dd if="$ROOT_IMG" of="$DISK_IMG" bs=1M seek=$(( ROOT_OFF / 1048576 )) conv=notrunc status=none
  rm -f "$ESP_IMG" "$ROOT_IMG"
  echo "  ${GREEN}✓${NC} bootable image: vulos-${ARCH}.img ($(du -h "$DISK_IMG" | cut -f1))"
  echo "${GREEN}Boot it:${NC} scripts/baremetal-smoke.sh --show"
  echo ""
fi

# ═══════════════════════════════════
# 5c. Netboot iPXE stick (--netboot-stick)
#
# Produces a ~1 MB iPXE USB stick image that chainloads boot.vulos.org to
# fetch kernel + initramfs + squashfs for a live-RAM session.  The stick is
# a one-time bootstrap tool; the installed machine never needs it again.
#
# Does NOT require the rootfs build above: it is purely an iPXE binary with
# an embedded script.  Delegated entirely to scripts/netboot/build-ipxe-stick.sh
# which tool-guards the iPXE toolchain and prints manual instructions when
# the toolchain is absent.
# ═══════════════════════════════════
if [ "$NETBOOT_STICK" = "1" ]; then
  echo "${BLUE}▸ Building netboot iPXE stick image (NETB-01)...${NC}"
  "$ROOT_DIR/scripts/netboot/build-ipxe-stick.sh" --outdir "$OUTDIR"
  echo ""
fi

# ═══════════════════════════════════
# 5b. Rootfs tarball (always — non-live path unchanged)
# ═══════════════════════════════════
echo "${BLUE}▸ Creating rootfs tarball...${NC}"
# Compute image filename suffix: amd64 uses bare arch; arm64 appends device variant
if [ -n "$DEVICE" ]; then
  IMAGE_NAME="vulos-${ARCH}-${DEVICE}.tar.gz"
else
  IMAGE_NAME="vulos-${ARCH}.tar.gz"
fi
tar czf "$OUTDIR/$IMAGE_NAME" -C "$ROOTFS" .
echo "  ${GREEN}✓${NC} $IMAGE_NAME ($(du -h "$OUTDIR/$IMAGE_NAME" | cut -f1))"

echo ""
echo "${GREEN}═══════════════════════════════════${NC}"
echo "${GREEN}Build complete!${NC}"
echo ""
ls -lh "$OUTDIR/vulos-server" "$OUTDIR/vulos-init" 2>/dev/null
echo ""
echo "Deploy to a machine:"
echo "  ./build.sh --deploy 192.168.1.50"
echo "  ./build.sh --deploy 192.168.1.50 --domain os.vulos.org --dns-namecheap user key"
echo ""
echo "Or flash rootfs:"
echo "  tar xzf $OUTDIR/$IMAGE_NAME -C /mnt/target"
if [ "$ARCH" = "arm64" ] && [ "$DEVICE" = "rpi4" ]; then
  echo ""
  echo "RPi 4 ESP files (copy to boot partition):"
  echo "  $OUTDIR/esp-rpi4/config.txt"
  echo "  $OUTDIR/esp-rpi4/cmdline.txt"
fi
echo "${GREEN}═══════════════════════════════════${NC}"
