# Vulos — Debian Container (layered for fast rebuilds)
#
# ── Default build (pre-built frontend + backend binary) ───────────────────────
#
#   Step 1 — build frontend:
#     cd frontend && npm ci && npm run build
#
#   Step 2 — build the Go binary for the target platform:
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
#   FROM --platform=$BUILDPLATFORM compiles Go natively; GOOS/GOARCH env vars
#   handle cross-compilation for the target platform.
#
#     docker build \
#       --build-arg BINARY_SOURCE=built \
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
#   Docker.
ARG BINARY_SOURCE=prebuilt

# ── Stage 1: Frontend (pre-built) ─────────────────────────────────────────────
# The frontend is built on the CI runner (or locally via `npm run build`)
# before `docker build` is invoked.  frontend/dist/ is COPY'd from the context
# rather than rebuilt inside Docker.
# To build locally: (cd frontend && npm ci && npm run build) && docker build .
FROM scratch AS frontend
COPY frontend/dist/ /dist/

# ── Stage 2a: Pre-built Go backend (CI / default path) ────────────────────────
# Binaries are compiled on the runner (CGO_ENABLED=0, so cross-compilation is
# trivial) and placed in bin/ before docker build.
# Each CI matrix job builds for one platform and owns bin/ exclusively.
FROM scratch AS backend-prebuilt
COPY bin/ /

# ── Stage 2b: Go source build (standalone / release multi-arch path) ──────────
# Used when --build-arg BINARY_SOURCE=built is passed.
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
#
# THIS IMAGE SHIPS ONE DISPLAY STACK: X11. That is deliberate, and it is not the
# same list as build.sh's bare-metal rootfs — see roadmap/DISPLAY-STACK.md for
# the measurements. The short version:
#
#   This image runs `stream.Pool` — apps streamed over WebRTC. pool.go's
#   `useCage` requires BOTH /dev/dri and a `cage` binary, and a container
#   without --device /dev/dri lands on gpu.TierSoftware, so every session here
#   took the Xvfb branch already. The Wayland half was inert: no input injector
#   is constructed on the cage branch, capture still pointed at an X display
#   cage never starts, and there is no xdg-desktop-portal ScreenCast client
#   anywhere in the repo for pipewiresrc to bind to.
#
#   Removing `labwc cage xdg-desktop-portal-wlr` took 24 packages and 10.2 MiB
#   out of the image, and 26 of the 32 CVEs the whole display-stack question was
#   worth — because cage and labwc both hard-`Depends: xwayland`, and xwayland
#   carries 25 open CVEs on its own. Dropping Wayland removes far MORE X server
#   than dropping X11 does; the older comment here had that backwards.
#
#   The bare-metal rootfs in build.sh keeps labwc and cage. It has a real seat,
#   real DRM and real libinput; this container has none of those. Do not
#   "reunify" the two lists.
#
# Before anyone cleans anything else up here, the traps:
#
#   pulseaudio is NOT redundant with pipewire-pulse. pipewire-pulse provides
#   the client socket, not the daemon binary; services/webbrowser/chrome.go
#   does findBin("pulseaudio") and launches it directly to build the virtual
#   sink/source the streamed browser records from. Removing the package breaks
#   browser audio, and the failure is a runtime "pulseaudio not found", not a
#   build error.
#
#   wireplumber has ZERO references in Go, and that is expected: it is
#   PipeWire's session manager, started by systemd, not by us. A grep-based
#   "unused package" sweep will flag it and be wrong.
#
#   x11-xserver-utils is here for exactly one binary, xrandr (stream resize),
#   and it drags in 31 MiB of `cpp`. It stays only because this image DOES run
#   an X server; build.sh's rootfs does not, which is why it is gone from there.
#
#   liburing2 and git are APP RUNTIME dependencies, not OS ones, and they are
#   here because `deps` in a registry recipe is now VERIFIED at install rather
#   than apt-installed (DEPS-02, roadmap/INSTALL-METHODOLOGY.md §4.6). Measured
#   2026-08-17 in debian:trixie-slim with the apt lists cleared, which is the
#   state build.sh leaves the image in: asking apt for liburing2 exits 100 with
#   "Unable to locate package", so the old install-time call could never
#   have worked on a shipped box. conduwuit 0.5.9 carries liburing.so.2 in
#   DT_NEEDED on BOTH architectures and dies in the loader without it; wede
#   shells out to the git binary. liburing2 costs 1 package, git costs 33.
#
# The package set is pinned by scripts/check-image-packages.sh: adding anything
# here must be a deliberate diff, not a side effect.
RUN apt-get update && apt-get install -y --no-install-recommends \
    tini bash sudo python3 curl jq ca-certificates wget \
    git liburing2 \
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
    pipewire pipewire-pulse wireplumber \
    gstreamer1.0-pipewire \
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
    && printf '# Vulos — hardened sshd config\n\
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
COPY frontend/apps/ /opt/vulos/apps/

# The labwc rc.xml and its openbox SSD theme are NOT copied into this image:
# labwc does not ship here (see the package list above), so both would be config
# for a binary that cannot be started. build.sh installs them into the
# bare-metal rootfs, which is where labwc actually runs.
# BMINIT-07: Plymouth boot splash — vulos theme
# Kernel cmdline: quiet splash plymouth.theme=vulos
COPY assets/plymouth/themes/vulos/ /usr/share/plymouth/themes/vulos/
RUN plymouth-set-default-theme vulos 2>/dev/null || \
    ln -sf /usr/share/plymouth/themes/vulos/vulos.plymouth \
        /etc/alternatives/default.plymouth 2>/dev/null || true

# Layer 4: Frontend build output (changes with UI work)
COPY --from=frontend /dist /opt/vulos/webroot

# Layer 5: Trust anchor + release cert (SEED-01 / REGISTRY-SIGN-01)
#
# These are PUBLIC keys. The root private key never leaves offline storage and
# the release private key never enters CI — see docs/KEY-CEREMONY.md.
#
#   trust-anchor.pub  — the offline ROOT public key. signing.DefaultAnchorPath
#                       resolves here, so the App Hub, the A/B slot updater and
#                       the netboot verifier all have something to chain to.
#   release-cert.json — the root-signed cert authorising the RELEASE key that
#                       actually signed registry.json's entries.
#
# The repo ships the DEV anchor by default. A production box refuses it
# (signing.RefuseDevKeyInProd) — replace both files with ceremony output and
# re-run `make sign-registry` before shipping with VULOS_ENV=prod.
RUN mkdir -p /etc/vulos
COPY keys/trust-anchor.pub /etc/vulos/trust-anchor.pub
COPY keys/release-cert.json /etc/vulos/release-cert.json
RUN chmod 0444 /etc/vulos/trust-anchor.pub /etc/vulos/release-cert.json

# Layer 6: Registry (changes when apps are added/removed)
# Every entry is Ed25519-signed by the release key above; installs are refused
# for any entry that is unsigned or fails verification.
COPY registry.json /opt/vulos/registry.json

# Layer 7: Go binary (changes most often — last for fast rebuilds)
COPY --from=backend /vulos-server /usr/local/bin/vulos-server
COPY --from=backend /vulos-init /usr/local/bin/vulos-init
COPY scripts/xdg-open /usr/local/bin/xdg-open
RUN rm -f /usr/bin/xdg-open && ln -s /usr/local/bin/xdg-open /usr/bin/xdg-open

# DO NOT create /var/lib/vulos/.setup-complete here.
#
# This layer used to, and it is the same defect build.sh shipped for four and a
# half months: GET /api/setup/status is os.Stat on exactly that path, and
# AuthGate runs the fifteen-step first-boot wizard only when it reports false.
# Every container built from this file therefore booted claiming a person had
# already set it up — welcome, chooser, device, language, timezone, network,
# account, pin, apps, appearance, identity, storage, ssh, recoverykit, ready,
# none of them. What a first boot offered instead was the create-account form.
#
# The convenience was real — a dev starting a fresh container does not want to
# walk fifteen steps — but it belongs in the dev entry point, not in the image
# that ships to users. scripts/dev.sh writes the marker into the container after
# it starts, and scripts/seed-demo.sh does the same for a seeded demo box.
#
# The marker is runtime state. Whatever COMPLETES setup writes it, and on a real
# box that is POST /api/setup/complete, called by the wizard's last step.

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
ENV HOSTNAME=vulos

EXPOSE 8080 22
ENTRYPOINT ["tini", "--"]
CMD ["/usr/local/bin/vulos-server", "-env", "local"]
