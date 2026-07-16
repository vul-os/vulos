#!/bin/sh
# entrypoint.sh — Vulos headless box startup script.
# Runs inside the debian:bookworm-slim runtime image under tini.
# All configuration is via environment variables.
set -eu

# Validate required secrets — refuse to start with missing/dev-default values.
fatal() {
    echo "[entrypoint] FATAL: $1" >&2
    exit 1
}

[ -z "${VULOS_RPID:-}" ]                  && fatal "VULOS_RPID is not set. Set it to your production domain (e.g. vulos.org)."
[ -z "${VULOS_ORIGIN:-}" ]                && fatal "VULOS_ORIGIN is not set. Set it to your production origin (e.g. https://vulos.org)."
[ -z "${VULOS_RESTIC_PASSWORD:-}" ]       && fatal "VULOS_RESTIC_PASSWORD is not set. Set it to a strong random passphrase."
[ -z "${BURST_HEARTBEAT_SECRET:-}" ]      && fatal "BURST_HEARTBEAT_SECRET is not set. Set it to a random secret for the /init-passphrase gate."

# Ensure data directory exists and is writable.
mkdir -p "${VULOS_DATA_DIR:-/data}"

echo "[entrypoint] starting Vulos OS (headless box) — env=${VULOS_ENV:-prod}"
exec /app/vulos -env "${VULOS_ENV:-prod}"
