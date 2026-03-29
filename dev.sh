#!/bin/sh
# Vula OS — Quick Dev Start (macOS)
# Runs backend + frontend in parallel. No Docker needed.
#
# Usage: ./dev.sh
# Open:  http://localhost:5173
#
# Prerequisites:
#   - Go 1.25+
#   - Node 22+
#   - Optional: Ollama running on :11434 for AI features

set -e

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

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

# Wait for backend
sleep 2

# Start frontend
echo "${GREEN}▸ Starting Vite dev server on :5173${NC}"
npm run dev &
FRONTEND_PID=$!

echo ""
echo "${GREEN}═══════════════════════════════${NC}"
echo "${GREEN}  Backend:  http://localhost:8080${NC}"
echo "${GREEN}  Frontend: http://localhost:5173${NC}"
echo ""
echo "${GREEN}  Vite proxies /api → :8080${NC}"
echo "${GREEN}  Press Ctrl+C to stop${NC}"
echo "${GREEN}═══════════════════════════════${NC}"

# Wait for either to exit
wait
