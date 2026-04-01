#!/bin/sh
# Vula OS — System Image Builder & Deployer
#
# Builds a bare-metal Debian 13 (trixie) system image.
# Optionally deploys to a remote machine via SSH with Caddy + wildcard TLS.
#
# Usage:
#   sudo ./build.sh                                    # build to ./output/
#   sudo ARCH=arm64 ./build.sh                         # ARM64
#   ./build.sh --deploy 192.168.1.50                   # build + deploy via SSH
#   ./build.sh --deploy-only 192.168.1.50              # skip packages, just push code
#   ./build.sh --deploy 192.168.1.50 \
#     --domain os.vulos.org \
#     --dns-namecheap myuser APIKEY123                  # with Caddy wildcard TLS
#
# Env vars (alternative to flags):
#   NAMECHEAP_USER, NAMECHEAP_KEY, VULOS_DOMAIN
#
# Prerequisites:
#   - Go 1.21+, Node 18+, npm
#   - SSH key access to target (for --deploy)
#   - Namecheap API access + server IP whitelisted (for --dns-namecheap)

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

while [ $# -gt 0 ]; do
  case "$1" in
    --deploy)      DEPLOY_HOST="$2"; shift 2 ;;
    --deploy-only) DEPLOY_HOST="$2"; DEPLOY_ONLY=true; shift 2 ;;
    --domain)      DOMAIN="$2"; shift 2 ;;
    --dns-namecheap) NC_USER="$2"; NC_KEY="$3"; shift 3 ;;
    *) OUTDIR_ARG="$1"; shift ;;
  esac
done

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
echo "${BLUE}║      Vula OS — Image Builder     ║${NC}"
echo "${BLUE}╠══════════════════════════════════╣${NC}"
echo "${BLUE}║${NC} Arch:   $ARCH"
echo "${BLUE}║${NC} Suite:  $SUITE"
echo "${BLUE}║${NC} Output: $OUTDIR"
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
    flatpak rsync systemd systemd-sysv

# Intel VA-API (amd64 only)
dpkg --print-architecture | grep -q amd64 && \
    apt-get install -y --no-install-recommends intel-media-va-driver-non-free || true

apt-get clean
rm -rf /var/lib/apt/lists/*

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
printf '{"CommandLineFlagSecurityWarningsEnabled": false}\n' > /etc/chromium/policies/managed/vulos.json

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
Description=Vula OS Server
After=network.target

[Service]
Type=simple
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

echo "${BLUE}▸ Building Debian rootfs with debootstrap...${NC}"
ROOTFS="$OUTDIR/rootfs"
rm -rf "$ROOTFS"

debootstrap --arch="$ARCH" --variant=minbase "$SUITE" "$ROOTFS" http://deb.debian.org/debian

chroot "$ROOTFS" sh -c 'sed -i "s/Components: main/Components: main contrib non-free non-free-firmware/" /etc/apt/sources.list.d/debian.sources 2>/dev/null || true'

chroot "$ROOTFS" apt-get update
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
    flatpak rsync systemd systemd-sysv

[ "$ARCH" = "amd64" ] && chroot "$ROOTFS" apt-get install -y --no-install-recommends intel-media-va-driver-non-free || true

chroot "$ROOTFS" apt-get clean
rm -rf "$ROOTFS/var/lib/apt/lists/"*
chroot "$ROOTFS" flatpak remote-add --if-not-exists flathub https://flathub.org/repo/flathub.flatpakrepo

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
cp -r "$OUTDIR/webroot" "$ROOTFS/opt/vulos/webroot"
cp -r "$OUTDIR/apps" "$ROOTFS/opt/vulos/apps"
cp "$OUTDIR/registry.json" "$ROOTFS/opt/vulos/registry.json"

mkdir -p "$ROOTFS/root/.vulos/data" "$ROOTFS/root/.vulos/db" \
    "$ROOTFS/root/.vulos/sandbox" "$ROOTFS/root/.vulos/browser/extensions" \
    "$ROOTFS/tmp/xdg-runtime"

mkdir -p "$ROOTFS/etc/chromium/policies/managed"
printf '{"CommandLineFlagSecurityWarningsEnabled": false}\n' > "$ROOTFS/etc/chromium/policies/managed/vulos.json"

echo "vula" > "$ROOTFS/etc/hostname"
echo "%sudo ALL=(ALL) ALL" > "$ROOTFS/etc/sudoers.d/sudo-group"
chmod 440 "$ROOTFS/etc/sudoers.d/sudo-group"

cat > "$ROOTFS/etc/systemd/system/vulos.service" << 'EOF'
[Unit]
Description=Vula OS Server
After=network.target

[Service]
Type=simple
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

echo "${BLUE}▸ Creating rootfs tarball...${NC}"
tar czf "$OUTDIR/vulos-$ARCH.tar.gz" -C "$ROOTFS" .
echo "  ${GREEN}✓${NC} vulos-$ARCH.tar.gz ($(du -h "$OUTDIR/vulos-$ARCH.tar.gz" | cut -f1))"

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
echo "  tar xzf $OUTDIR/vulos-$ARCH.tar.gz -C /mnt/target"
echo "${GREEN}═══════════════════════════════════${NC}"
