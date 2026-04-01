# Vula OS — Debian Container (layered for fast rebuilds)
#
# Build: docker build -t vulos .
# Run:   docker run -p 8080:8080 --shm-size=1g vulos
# Open:  http://localhost:8080
#
# Layer order (bottom = changes least, top = changes most):
#   1. System packages (apt) — rarely changes
#   2. Frontend build (npm) — changes with UI work
#   3. Go binary + config — changes most often

# ── Stage 1: Frontend build ──────────────────────────────
FROM node:22-slim AS frontend
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY index.html vite.config.js eslint.config.js ./
COPY src/ src/
COPY public/ public/
RUN npm run build

# ── Stage 2: Go backend build ────────────────────────────
FROM golang:trixie AS backend
WORKDIR /app
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /vulos-server ./cmd/server
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /vulos-init ./cmd/init

# ── Stage 3: Runtime image ───────────────────────────────
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
    flatpak \
    && ( dpkg --print-architecture | grep -q amd64 && apt-get install -y --no-install-recommends intel-media-va-driver-non-free || true ) \
    && rm -rf /var/lib/apt/lists/* \
    && flatpak remote-add --if-not-exists flathub https://flathub.org/repo/flathub.flatpakrepo \

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

# Layer 3: Static assets (changes with content updates)
COPY apps/ /opt/vulos/apps/
# Layer 4: Frontend build output (changes with UI work)
COPY --from=frontend /app/dist /opt/vulos/webroot

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
ENV VULOS_REGISTRY=/opt/vulos/registry.json
ENV SHELL=/bin/bash
ENV DISPLAY=:99
ENV HOSTNAME=vula

EXPOSE 8080
ENTRYPOINT ["tini", "--"]
CMD ["/usr/local/bin/vulos-server", "-env", "local"]
