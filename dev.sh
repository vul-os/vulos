#!/bin/sh
# Vula OS — Development Script
#
# Usage:
#   ./dev.sh              Local dev (Go backend + Vite HMR, no Docker)
#   ./dev.sh deploy       Full Docker build + deploy
#   ./dev.sh deploy quick Quick rebuild (backend + frontend only, restart container)
#
# Local dev:  http://localhost:5173
# Docker:     http://localhost:8080

set -e

GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

NAME="vulos"
PORT="${PORT:-8080}"
SHM="1g"
VOLUME="vulos-data"

# ── Deploy: full Docker build ──────────────────────────────
deploy_full() {
  echo "${BLUE}Full Docker build + deploy${NC}"

  echo "Stopping existing container..."
  docker rm -f "$NAME" 2>/dev/null || true

  echo "Building image..."
  docker build -t "$NAME" .

  LANDING="${LANDING_PORT:-3000}"

  echo "Starting container..."
  docker run -d \
    --name "$NAME" \
    -p "$PORT:8080" \
    -p "$LANDING:3000" \
    --shm-size="$SHM" \
    -v "$VOLUME:/root/.vulos" \
    -e LANDING_PORT=3000 \
    "$NAME"

  echo "Waiting for startup..."
  sleep 3

  if docker ps --filter "name=$NAME" --format '{{.Status}}' | grep -q "Up"; then
    echo "${GREEN}OS running at http://localhost:$PORT${NC}"
    echo "${GREEN}Landing at http://localhost:$LANDING${NC}"
  else
    echo "${RED}Failed to start. Logs:${NC}"
    docker logs "$NAME" --tail 20
    exit 1
  fi
}

# ── Deploy: quick rebuild ─────────────────────────────────
deploy_quick() {
  echo "${BLUE}Quick rebuild — backend + frontend only${NC}"

  echo "Building Go backend..."
  cd backend
  CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o ../vulos-server ./cmd/server
  cd ..

  echo "Building frontend..."
  npm run build

  echo "Copying into container..."
  docker cp vulos-server "$NAME":/usr/local/bin/vulos-server
  docker cp dist/. "$NAME":/opt/vulos/webroot/
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
  echo "${BLUE}║   Vula OS — Dev Mode         ║${NC}"
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
  npm run dev &
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
      *)     deploy_full ;;
    esac
    ;;
  *)
    dev_local
    ;;
esac
