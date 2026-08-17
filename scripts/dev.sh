#!/bin/sh
# Vulos OS — Development Script
#
# Usage:
#   ./scripts/dev.sh                Local dev (Go backend + Vite HMR, no Docker)
#   ./scripts/dev.sh deploy         Full Docker build + deploy
#   ./scripts/dev.sh deploy quick   Quick rebuild (copy backend + frontend into running container)
#   ./scripts/dev.sh deploy layer   Layered rebuild (Docker build, reuses cached apt layer — fast)
#
# Local dev:  http://localhost:5173
# Docker:     http://localhost:8080

set -e

# Every path below (backend/, Dockerfile, dist/, registry.json, landing/) is
# relative to the repo root, not to this script's own location. Anchor CWD to
# the repo root (parent of scripts/) so this still works regardless of where
# it's invoked from, now that it no longer lives at the root itself.
cd "$(dirname "$0")/.."
# Absolute repo root, so paths stay correct even if a later step cds away.
REPO_ROOT="$PWD"

GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
DIM='\033[2m'
NC='\033[0m'

NAME="vulos"
PORT="${PORT:-8080}"
SHM="1g"
VOLUME="vulos-data"

start_container() {
  echo "Starting container..."
  TLS_MOUNT=""
  DOMAIN="${VULOS_DOMAIN:-lvh.me}"
  if [ -f "$HOME/.vulos/localhost.pem" ] && [ -f "$HOME/.vulos/localhost-key.pem" ]; then
    TLS_MOUNT="-v $HOME/.vulos/localhost.pem:/root/.vulos/localhost.pem:ro -v $HOME/.vulos/localhost-key.pem:/root/.vulos/localhost-key.pem:ro"
    echo "TLS certs mounted (HTTPS enabled)"
  fi
  docker run -d \
    --name "$NAME" \
    -p "$PORT:8080" \
    --shm-size="$SHM" \
    --privileged \
    -v "$VOLUME:/root/.vulos" \
    $TLS_MOUNT \
    -e VULOS_DOMAIN="$DOMAIN" \
    "$NAME"

  echo "Domain: $DOMAIN (web apps at https://{app}.$DOMAIN:$PORT)"

  echo "Waiting for startup..."
  sleep 3

  if docker ps --filter "name=$NAME" --format '{{.Status}}' | grep -q "Up"; then
    # Skip the fifteen-step wizard on a dev container.
    #
    # This USED to be a `RUN touch` layer in the Dockerfile, which meant every
    # image built from it — including the published one — booted claiming a
    # person had already set it up, and no first boot anywhere ever ran the
    # wizard. The convenience is legitimate; shipping it to users is not. So it
    # lives here, in the dev entry point, applied to the running container.
    #
    # Set VULOS_DEV_RUN_SETUP=1 to leave the marker off and actually walk the
    # wizard — which is the only way to see, on a dev box, what a real first
    # boot looks like.
    if [ "${VULOS_DEV_RUN_SETUP:-0}" != "1" ]; then
      docker exec "$NAME" sh -c 'mkdir -p /var/lib/vulos && touch /var/lib/vulos/.setup-complete' \
        || echo "${RED}Could not write the dev setup marker — the wizard will run.${NC}"
    fi
    echo "${GREEN}OS running at http://localhost:$PORT${NC}"
  else
    echo "${RED}Failed to start. Logs:${NC}"
    docker logs "$NAME" --tail 20
    exit 1
  fi
}

# ── Deploy: full Docker build ──────────────────────────────
deploy_full() {
  echo "${BLUE}Full Docker build + deploy${NC}"

  echo "Stopping existing container..."
  docker rm -f "$NAME" 2>/dev/null || true

  echo "Building image (all layers)..."
  docker build -t "$NAME" .

  start_container
}

# ── Deploy: layered rebuild (reuses apt cache) ────────────
deploy_layer() {
  echo "${BLUE}Layered rebuild — reuses cached system layers${NC}"

  echo "Stopping existing container..."
  docker rm -f "$NAME" 2>/dev/null || true

  echo "Building image (cached apt, rebuilds Go + frontend)..."
  START=$(date +%s)
  docker build -t "$NAME" .
  END=$(date +%s)
  echo "${DIM}Build took $((END - START))s${NC}"

  start_container
}

# ── Deploy: quick rebuild ─────────────────────────────────
deploy_quick() {
  echo "${BLUE}Quick rebuild — backend + frontend only${NC}"

  if ! docker ps --filter "name=$NAME" --format '{{.Status}}' | grep -q "Up"; then
    echo "${RED}Container not running. Use './scripts/dev.sh deploy' first.${NC}"
    exit 1
  fi

  echo "Building Go backend..."
  cd backend
  CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o ../vulos-server ./cmd/server
  cd ..

  echo "Building frontend..."
  (cd "$REPO_ROOT/frontend" && npm run build)

  echo "Copying into container..."
  docker cp vulos-server "$NAME":/usr/local/bin/vulos-server
  docker cp "$REPO_ROOT/frontend/dist/." "$NAME":/opt/vulos/webroot/
  docker cp registry.json "$NAME":/opt/vulos/registry.json
  [ -d landing ] && docker exec "$NAME" mkdir -p /opt/vulos/landing && docker cp landing/. "$NAME":/opt/vulos/landing/

  echo "Restarting container..."
  docker restart "$NAME"

  rm -f vulos-server

  sleep 3
  if docker ps --filter "name=$NAME" --format '{{.Status}}' | grep -q "Up"; then
    echo "${GREEN}Running at http://localhost:$PORT${NC}"
  else
    echo "${RED}Failed. Logs:${NC}"
    docker logs "$NAME" --tail 20
    exit 1
  fi
}

# ── Local dev (no Docker) ─────────────────────────────────
dev_local() {
  echo "${BLUE}╔══════════════════════════════╗${NC}"
  echo "${BLUE}║   Vulos OS — Dev Mode         ║${NC}"
  echo "${BLUE}╚══════════════════════════════╝${NC}"

  # Ensure setup marker exists (skip OOBE)
  mkdir -p /var/lib/vulos 2>/dev/null || mkdir -p /tmp/vulos-dev
  touch /var/lib/vulos/.setup-complete 2>/dev/null || touch /tmp/vulos-dev/.setup-complete

  # Kill background processes on exit
  cleanup() {
    echo "\n${GREEN}Shutting down...${NC}"
    kill $BACKEND_PID $FRONTEND_PID 2>/dev/null
    exit 0
  }
  trap cleanup INT TERM

  # Start backend
  echo "${GREEN}▸ Starting Go backend on :8080${NC}"
  cd backend
  go run ./cmd/server -env=local &
  BACKEND_PID=$!
  cd ..

  sleep 2

  # Start frontend
  echo "${GREEN}▸ Starting Vite dev server on :5173${NC}"
  (cd "$REPO_ROOT/frontend" && npm run dev) &
  FRONTEND_PID=$!

  echo ""
  echo "${GREEN}═══════════════════════════════${NC}"
  echo "${GREEN}  Backend:  http://localhost:8080${NC}"
  echo "${GREEN}  Frontend: http://localhost:5173${NC}"
  echo "${GREEN}  Vite proxies /api → :8080${NC}"
  echo "${GREEN}  Press Ctrl+C to stop${NC}"
  echo "${GREEN}═══════════════════════════════${NC}"

  wait
}

# ── Main ──────────────────────────────────────────────────
case "$1" in
  deploy)
    case "$2" in
      quick) deploy_quick ;;
      layer) deploy_layer ;;
      *)     deploy_full ;;
    esac
    ;;
  *)
    dev_local
    ;;
esac
