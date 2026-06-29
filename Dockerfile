# Vula OS — Debian Container (layered for fast rebuilds)
#
# ── Default build (pre-built frontend + backend binary) ───────────────────────
#
#   Step 1 — build frontend (requires vulos-relay + vulos-office siblings):
#     cd ../vulos-relay/client && npm install && npm run build:lib
#     npm ci && npm run build
#
#   Step 2 — build the Go binary for the target platform (requires vulos-apps):
#     mkdir -p bin
#     cd backend
#     CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
#       -ldflags "-s -w -X main.Version=dev" -o ../bin/vulos-server ./cmd/server
#     CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
#       -ldflags "-s -w" -o ../bin/vulos-init ./cmd/init
#
#   Step 3 — build the image:
#     docker build -t vulos .
#
# ── Standalone / multi-arch build (Go compiled inside Docker) ─────────────────
#
#   Resolves the go.mod `replace github.com/vul-os/vulos-apps => ../../vulos-apps`
#   by supplying vulos-apps as a named BuildKit build-context.  WORKDIR mirrors
#   the local checkout layout so ../../vulos-apps resolves inside the container.
#   FROM --platform=$BUILDPLATFORM compiles Go natively; GOOS/GOARCH env vars
#   handle cross-compilation for the target platform.
#
#     docker build \
#       --build-arg BINARY_SOURCE=built \
#       --build-context vulos-apps=../vulos-apps \
#       --build-arg VERSION=vX.Y.Z \
#       -t vulos .
#
# Run:   docker run -p 8080:8080 --shm-size=1g vulos
# Open:  http://localhost:8080
#
# Layer order (bottom = changes least, top = changes most):
#   1. System packages (apt) — rarely changes
#   2. Frontend build (npm) — changes with UI work
#   3. Go binary + config — changes most often

# ── Binary source selection ────────────────────────────────────────────────────
# 'prebuilt' (default, CI):  binaries are pre-built on the host before
#   docker build and placed at bin/vulos-server + bin/vulos-init.
#   Each CI matrix job builds for exactly one platform, so a flat bin/ dir
#   is sufficient (no per-arch subdirectory needed).
# 'built'   (standalone / release multi-arch):  builds Go from source inside
#   Docker.  Requires --build-context vulos-apps=../vulos-apps.
ARG BINARY_SOURCE=prebuilt

# ── Stage 1: Frontend (pre-built) ─────────────────────────────────────────────
# The frontend is built on the CI runner (or locally via `npm run build`)
# before `docker build` is invoked.  dist/ is COPY'd from the build context
# rather than rebuilt inside Docker, avoiding the file: dep path issue where
# ../vulos-relay/client is outside the Docker build context (context: .).
# To build locally: cd ../vulos-relay/client && npm install && npm run build:lib
#                   npm ci && npm run build && docker build .
FROM scratch AS frontend
COPY dist/ /dist/

# ── Stage 2a: Pre-built Go backend (CI / default path) ────────────────────────
# go.mod has: replace github.com/vul-os/vulos-apps => ../../vulos-apps
# This path resolves on the runner (sibling cloned) but not inside the Docker
# build context.  Binaries are compiled on the runner (CGO_ENABLED=0, so
# cross-compilation is trivial) and placed in bin/ before docker build.
# Each CI matrix job builds for one platform and owns bin/ exclusively.
FROM scratch AS backend-prebuilt
COPY bin/ /

# ── Stage 2b: Go source build (standalone / release multi-arch path) ──────────
# Used when --build-arg BINARY_SOURCE=built is passed.
# vulos-apps must be provided as a named BuildKit build-context:
#   --build-context vulos-apps=../vulos-apps
#
# The WORKDIR mirrors the local checkout layout so the replace directive
#   replace github.com/vul-os/vulos-apps => ../../vulos-apps
# resolves correctly inside Docker:
#   /workspace/vulos/backend + ../../vulos-apps = /workspace/vulos-apps ✓
#
# FROM --platform=$BUILDPLATFORM runs the Go compiler on the native builder
# platform (fast); GOOS/GOARCH from TARGETOS/TARGETARCH handle cross-compilation
# so this path works correctly for multi-arch buildx builds (linux/amd64 +
# linux/arm64 in a single invocation).
FROM --platform=$BUILDPLATFORM golang:trixie AS backend-built
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ENV GOTOOLCHAIN=auto
WORKDIR /workspace/vulos/backend
COPY backend/go.mod backend/go.sum ./
COPY --from=vulos-apps . /workspace/vulos-apps/
RUN go mod download
COPY backend/ .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /vulos-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" \
    -o /vulos-init ./cmd/init

# ── Stage 2: Resolved backend binary source ────────────────────────────────────
# Bridge stage: selects the binary source (prebuilt vs built-from-source).
# BINARY_SOURCE is declared globally above (before first FROM) so it is
# available for substitution in FROM statements throughout this Dockerfile.
FROM backend-${BINARY_SOURCE} AS backend

# ── Stage 3: Runtime image ────────────────────────────────────────────────────
FROM debian:trixie-slim

ENV DEBIAN_FRONTEND=noninteractive

# Layer 1: System packages (heaviest, changes least)
# Enable non-free repos for Intel VA-API driver
RUN sed -i 's/Components: main/Components: main contrib non-free non-free-firmware/' /etc/apt/sources.list.d/debian.sources

# Core + remote browser stack (Xvfb + Chromium + GStreamer)
RUN apt-get update && apt-get install -y --no-install-recommends \
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
    flatpak \
    cage labwc \
    pipewire pipewire-pulse wireplumber \
    gstreamer1.0-pipewire \
    xdg-desktop-portal-wlr \
    libgbm1 libegl1 \
    plymouth plymouth-themes \
    avahi-daemon avahi-utils dhcpcd5 wpasupplicant \
    openssh-server \
    && ( dpkg --print-architecture | grep -q amd64 && apt-get install -y --no-install-recommends intel-media-va-driver-non-free || true ) \
    && rm -rf /var/lib/apt/lists/* \
    && flatpak remote-add --if-not-exists flathub https://flathub.org/repo/flathub.flatpakrepo

# Layer 2: System config (rarely changes)
RUN groupadd -f sudo 2>/dev/null || true \
    && echo "%sudo ALL=(ALL) ALL" > /etc/sudoers.d/sudo-group \
    && chmod 440 /etc/sudoers.d/sudo-group

RUN mkdir -p /opt/vulos/webroot /opt/vulos/apps \
    /var/lib/vulos /root/.vulos/data /root/.vulos/db /root/.vulos/sandbox \
    /root/.vulos/browser/extensions \
    /tmp/xdg-runtime \
    /etc/chromium/policies/managed \
    && printf '{"CommandLineFlagSecurityWarningsEnabled": false}\n' > /etc/chromium/policies/managed/vulos.json

# Hardened sshd configuration
RUN mkdir -p /etc/ssh/sshd_config.d \
    && printf '# Vula OS — hardened sshd config\n\
# Key-only auth — no passwords\n\
PasswordAuthentication no\n\
ChallengeResponseAuthentication no\n\
UsePAM no\n\
\n\
# Root login only via key\n\
PermitRootLogin prohibit-password\n\
\n\
# Hardening\n\
X11Forwarding no\n\
MaxAuthTries 3\n\
LoginGraceTime 30\n\
\n\
# Keep alive (detect dead connections)\n\
ClientAliveInterval 60\n\
ClientAliveCountMax 3\n' > /etc/ssh/sshd_config.d/vulos.conf

# Layer 3: Static assets (changes with content updates)
COPY apps/ /opt/vulos/apps/

# labwc compositor config (browser pinned to background, floating focus)
COPY assets/labwc/ /root/.config/labwc/
# Vula OS traffic-light openbox theme for labwc SSD
COPY assets/themes/vulos/ /usr/share/themes/vulos/
# BMINIT-07: Plymouth boot splash — vulos theme
# Kernel cmdline: quiet splash plymouth.theme=vulos
COPY assets/plymouth/themes/vulos/ /usr/share/plymouth/themes/vulos/
RUN plymouth-set-default-theme vulos 2>/dev/null || \
    ln -sf /usr/share/plymouth/themes/vulos/vulos.plymouth \
        /etc/alternatives/default.plymouth 2>/dev/null || true

# Layer 4: Frontend build output (changes with UI work)
COPY --from=frontend /dist /opt/vulos/webroot

# Layer 5: Registry (changes when apps are added/removed)
COPY registry.json /opt/vulos/registry.json

# Layer 6: Go binary (changes most often — last for fast rebuilds)
COPY --from=backend /vulos-server /usr/local/bin/vulos-server
COPY --from=backend /vulos-init /usr/local/bin/vulos-init
COPY scripts/xdg-open /usr/local/bin/xdg-open
RUN rm -f /usr/bin/xdg-open && ln -s /usr/local/bin/xdg-open /usr/bin/xdg-open

RUN touch /var/lib/vulos/.setup-complete

ENV PORT=8080
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
ENV XDG_RUNTIME_DIR=/tmp/xdg-runtime
ENV WLR_BACKENDS=headless
ENV WLR_RENDERER=pixman
ENV XDG_SESSION_TYPE=wayland
ENV MOZ_ENABLE_WAYLAND=1
ENV VULOS_REGISTRY=/opt/vulos/registry.json
ENV SHELL=/bin/bash
ENV DISPLAY=:99
ENV HOSTNAME=vula

EXPOSE 8080 22
ENTRYPOINT ["tini", "--"]
CMD ["/usr/local/bin/vulos-server", "-env", "local"]
