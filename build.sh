#!/bin/sh
# Vula OS — System Image Builder
#
# Builds a bare-metal Debian system image for flashing to real hardware.
#
# Usage:
#   sudo ./build.sh                    # build to ./output/
#   sudo ./build.sh /tmp/vulos         # custom output dir
#   sudo ARCH=arm64 ./build.sh         # ARM64
#
# Prerequisites:
#   - Go 1.21+, Node 18+, npm
#   - debootstrap (apt-get install debootstrap)
#   - Root access (for debootstrap + chroot)

set -e

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
ARCH="${ARCH:-amd64}"
GOARCH="$ARCH"
[ "$ARCH" = "x86_64" ] && GOARCH="amd64" && ARCH="amd64"
[ "$ARCH" = "aarch64" ] && GOARCH="arm64" && ARCH="arm64"
OUTDIR="$(cd "${1:-$ROOT_DIR/output}" 2>/dev/null && pwd || echo "${1:-$ROOT_DIR/output}")"
SUITE="bookworm"

echo "╔══════════════════════════════════╗"
echo "║      Vula OS — Image Builder     ║"
echo "╠══════════════════════════════════╣"
echo "║ Arch:   $ARCH"
echo "║ Suite:  $SUITE"
echo "║ Output: $OUTDIR"
echo "╚══════════════════════════════════╝"
echo ""

mkdir -p "$OUTDIR"

# ═══════════════════════════════════
# 1. Build Go binaries
# ═══════════════════════════════════
echo "▸ Building Go binaries ($GOARCH)..."
cd "$ROOT_DIR/backend"
GOOS=linux GOARCH=$GOARCH CGO_ENABLED=0 go build -ldflags="-s -w" -o "$OUTDIR/vulos-server" ./cmd/server
GOOS=linux GOARCH=$GOARCH CGO_ENABLED=0 go build -ldflags="-s -w" -o "$OUTDIR/vulos-init" ./cmd/init
echo "  ✓ vulos-server, vulos-init"

# ═══════════════════════════════════
# 2. Build frontend
# ═══════════════════════════════════
echo "▸ Building frontend..."
cd "$ROOT_DIR"
npm ci --silent 2>/dev/null || npm install --silent
npm run build
cp -r dist "$OUTDIR/webroot"
echo "  ✓ webroot/"

# ═══════════════════════════════════
# 3. Copy app services
# ═══════════════════════════════════
echo "▸ Copying app services..."
mkdir -p "$OUTDIR/apps"
for app in browser notes gallery; do
    if [ -d "$ROOT_DIR/apps/$app" ]; then
        cp -r "$ROOT_DIR/apps/$app" "$OUTDIR/apps/"
        echo "  ✓ $app"
    fi
done

# ═══════════════════════════════════
# 4. Build Debian rootfs
# ═══════════════════════════════════
if ! command -v debootstrap >/dev/null 2>&1; then
    echo "  ⚠ debootstrap not found — skipping rootfs build"
    echo "  Install: apt-get install debootstrap"
    echo ""
    echo "Binaries and frontend built to $OUTDIR"
    exit 0
fi

echo "▸ Building Debian rootfs with debootstrap..."
ROOTFS="$OUTDIR/rootfs"
rm -rf "$ROOTFS"

debootstrap --arch="$ARCH" --variant=minbase "$SUITE" "$ROOTFS" http://deb.debian.org/debian

# Install packages inside chroot
chroot "$ROOTFS" apt-get update
chroot "$ROOTFS" apt-get install -y --no-install-recommends \
    tini bash sudo python3 curl jq ca-certificates \
    iproute2 iptables \
    xvfb chromium xdotool \
    gstreamer1.0-tools gstreamer1.0-plugins-base gstreamer1.0-plugins-good \
    pulseaudio pulseaudio-utils \
    fonts-noto socat \
    systemd systemd-sysv

chroot "$ROOTFS" apt-get clean
rm -rf "$ROOTFS/var/lib/apt/lists/"*

# Install vulos binaries
cp "$OUTDIR/vulos-server" "$ROOTFS/usr/local/bin/"
cp "$OUTDIR/vulos-init" "$ROOTFS/sbin/vulos-init"
chmod +x "$ROOTFS/usr/local/bin/vulos-server" "$ROOTFS/sbin/vulos-init"

# Web root + apps
mkdir -p "$ROOTFS/opt/vulos"
cp -r "$OUTDIR/webroot" "$ROOTFS/opt/vulos/webroot"
cp -r "$OUTDIR/apps" "$ROOTFS/opt/vulos/apps"

# Hostname
echo "vulos" > "$ROOTFS/etc/hostname"

# Sudo group
echo "%sudo ALL=(ALL) ALL" > "$ROOTFS/etc/sudoers.d/sudo-group"
chmod 440 "$ROOTFS/etc/sudoers.d/sudo-group"

# ── systemd: vulos-server ──
cat > "$ROOTFS/etc/systemd/system/vulos.service" << 'EOF'
[Unit]
Description=Vula OS Server
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/vulos-server -env main
Restart=on-failure
RestartSec=5
Environment=PORT=8080

[Install]
WantedBy=multi-user.target
EOF

# ── systemd: kiosk ──
cat > "$ROOTFS/etc/systemd/system/vulos-kiosk.service" << 'EOF'
[Unit]
Description=Vula OS Kiosk (Cage + WPE WebKit)
After=vulos.service
Requires=vulos.service

[Service]
Type=simple
ExecStart=/usr/bin/cage -- /usr/bin/cog http://localhost:8080
Restart=on-failure
Environment=WLR_LIBINPUT_NO_DEVICES=1
Environment=WLR_NO_HARDWARE_CURSORS=1
Environment=XDG_RUNTIME_DIR=/run/user/0

[Install]
WantedBy=graphical.target
EOF

# Enable services
chroot "$ROOTFS" systemctl enable vulos.service
chroot "$ROOTFS" systemctl enable vulos-kiosk.service 2>/dev/null || true

# Setup marker
mkdir -p "$ROOTFS/var/lib/vulos"
touch "$ROOTFS/var/lib/vulos/.setup-complete"

echo "  ✓ rootfs built"

# Tarball
echo "▸ Creating rootfs tarball..."
tar czf "$OUTDIR/vulos-$ARCH.tar.gz" -C "$ROOTFS" .
echo "  ✓ vulos-$ARCH.tar.gz"

echo ""
echo "═══════════════════════════════════"
echo "Build complete!"
echo ""
ls -lh "$OUTDIR/vulos-server" "$OUTDIR/vulos-init" 2>/dev/null
echo ""
echo "To test with Docker:"
echo "  docker build -t vulos ."
echo "  docker run -p 8080:8080 --shm-size=1g vulos"
echo ""
echo "To deploy rootfs to a device:"
echo "  tar xzf $OUTDIR/vulos-$ARCH.tar.gz -C /mnt/target"
echo "═══════════════════════════════════"
