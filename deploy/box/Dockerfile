# Vulos OS — Headless box image (self-provisioned)
#
# The image you run on your own server for a headless box: terminal + web-apps
# only. NO GPU, NO desktop streaming, NO Xvfb, NO GStreamer, NO Wine, NO DXVK.
# (For a full desktop box with app streaming, use the top-level Dockerfile.)
#
# Runs the Vulos OS backend (./vulos server) and serves the static frontend.
# Owned apps like Office (vulos-office) are separate deployments reached through
# the App Hub; PIM (lilmail) is likewise configured via env. Their URLs are
# injected at runtime via environment variables.
#
# Build (from repo root):
#   docker build -f deploy/box/Dockerfile -t vulos-box .
#
# Run (requires real secrets — will refuse to start with dev defaults):
#   docker run -p 8080:8080 \
#     -e VULOS_ENV=prod \
#     -e VULOS_RPID=vulos.org \
#     -e VULOS_ORIGIN=https://vulos.org \
#     -e VULOS_RESTIC_PASSWORD=<secret> \
#     -e S3_ACCESS_KEY=<key> \
#     -e S3_SECRET_KEY=<secret> \
#     -e S3_BUCKET=<bucket> \
#     -e BURST_HEARTBEAT_SECRET=<secret> \
#     vulos-box

# ── Stage 1: Frontend build ───────────────────────────────────────────────────
FROM node:22-slim AS frontend
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY index.html vite.config.js eslint.config.js ./
COPY src/ src/
COPY public/ public/
# Build with VITE_MANAGED=true to strip GPU/streaming UI components.
ENV VITE_MANAGED=true
RUN npm run build

# ── Stage 2: Go backend build ─────────────────────────────────────────────────
FROM golang:1.23-bookworm AS gobuild
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
# CGO disabled — no C deps needed for headless mode.
# -tags managed disables GPU/stream/X11 build tags if present.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.buildMode=managed" \
    -o /vulos-server \
    ./cmd/server

# ── Stage 3: Runtime image ────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

# tini for proper signal forwarding + zombie reaping in containers.
RUN apt-get update && apt-get install -y --no-install-recommends \
    tini \
    ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Non-root user for the process.
RUN groupadd -r vulos && useradd -r -g vulos -m -d /home/vulos vulos

# Data directory (mounted as a volume in production for persistence).
RUN mkdir -p /data && chown vulos:vulos /data

WORKDIR /app
COPY --from=gobuild /vulos-server /app/vulos
COPY --from=frontend /app/dist /app/dist
COPY deploy/box/entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

# Trust anchor + release cert (REGISTRY-SIGN-01). PUBLIC keys only — the root
# private key never leaves offline storage. signing.DefaultAnchorPath resolves
# to /etc/vulos/trust-anchor.pub, which is what the App Hub chains registry
# entry signatures to.
#
# This image runs VULOS_ENV=prod, where the repo's DEV anchor is REFUSED
# outright (signing.RefuseDevKeyInProd) — app installs will fail closed until
# these files are replaced with real ceremony output and registry.json is
# re-signed. See docs/KEY-CEREMONY.md.
RUN mkdir -p /etc/vulos
COPY keys/trust-anchor.pub /etc/vulos/trust-anchor.pub
COPY keys/release-cert.json /etc/vulos/release-cert.json
RUN chmod 0444 /etc/vulos/trust-anchor.pub /etc/vulos/release-cert.json
COPY registry.json /app/registry.json
RUN chmod 0444 /app/registry.json
ENV VULOS_REGISTRY=/app/registry.json

# Static frontend served from /app/dist; the Go server reads VULOS_STATIC_DIR.
ENV VULOS_STATIC_DIR=/app/dist

# Headless mode: disable GPU, desktop streaming, X11, and related subsystems.
ENV VULOS_NO_GPU=1
ENV VULOS_NO_STREAM=1
ENV VULOS_NO_DESKTOP=1

# Data lives in /data (mount a persistent volume here).
ENV VULOS_DATA_DIR=/data

# Default to prod safety. Operators MUST override the following env vars
# or the server will refuse to start:
#   VULOS_RPID, VULOS_ORIGIN          — WebAuthn relying party
#   VULOS_RESTIC_PASSWORD             — vault encryption key
#   BURST_HEARTBEAT_SECRET            — /init-passphrase gate
ENV VULOS_ENV=prod

EXPOSE 8080

USER vulos

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/app/entrypoint.sh"]
