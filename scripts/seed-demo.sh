#!/usr/bin/env bash
# scripts/seed-demo.sh — Seed a demo account and populate the shell for screenshots.
#
# Creates a throwaway Vulos data directory under /tmp, boots the backend, registers
# a demo account, seeds realistic content, runs the Playwright screenshotter, then
# cleans up.
#
# Usage:
#   ./scripts/seed-demo.sh               # seed + capture + clean up
#   KEEP_DATA=1 ./scripts/seed-demo.sh   # leave data dir for manual inspection
#   SKIP_CAPTURE=1 ./scripts/seed-demo.sh  # seed only, no screenshots
#   BASE_URL=https://custom:8080 ./scripts/seed-demo.sh
#
# Output: docs/screenshots/ is updated in place.
#
# Requirements:
#   go (1.21+), node 18+, npx playwright install chromium (run once)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEMO_EMAIL="${SCREENSHOT_EMAIL:-demo@localhost}"
DEMO_USERNAME="${SCREENSHOT_USERNAME:-demo}"
DEMO_PASSWORD="${SCREENSHOT_PASSWORD:-Vul0sD3moPass!}"
DEMO_NAME="${SCREENSHOT_NAME:-Demo User}"
BACKEND_PORT="${BACKEND_PORT:-18081}"
BASE_URL="${BASE_URL:-http://localhost:${BACKEND_PORT}}"
KEEP_DATA="${KEEP_DATA:-0}"
SKIP_CAPTURE="${SKIP_CAPTURE:-0}"

DEMO_HOME="$(mktemp -d /tmp/vulos-demo-XXXXXX)"
DEMO_DATA="${DEMO_HOME}/.vulos/data"
DEMO_DB="${DEMO_HOME}/.vulos/db"

GREEN='\033[0;32m'; BLUE='\033[0;34m'; RED='\033[0;31m'; YELLOW='\033[0;33m'; NC='\033[0m'
log()  { echo -e "${BLUE}[seed]${NC} $*"; }
ok()   { echo -e "${GREEN}[seed] ok${NC} $*"; }
warn() { echo -e "${YELLOW}[seed] warn${NC} $*"; }
fail() { echo -e "${RED}[seed] FAIL${NC} $*" >&2; exit 1; }

cleanup() {
  log "Stopping backend (PID $BACKEND_PID)..."
  kill "$BACKEND_PID" 2>/dev/null || true
  wait "$BACKEND_PID" 2>/dev/null || true
  if [ "$KEEP_DATA" = "1" ]; then
    warn "Leaving demo data at $DEMO_HOME (KEEP_DATA=1)"
  else
    rm -rf "$DEMO_HOME"
    log "Demo data cleaned up"
  fi
}
trap cleanup EXIT

# ── 1. Ensure data dirs exist ─────────────────────────────────────────────────
mkdir -p "$DEMO_DATA" "$DEMO_DB"
log "Demo data dir: $DEMO_HOME"

# ── 2. Build the backend ──────────────────────────────────────────────────────
log "Building backend..."
cd "$REPO_ROOT/backend"
go build -o "$DEMO_HOME/vulos-server" ./cmd/server 2>&1 || fail "go build failed"
ok "Backend built"

# ── 3. Start the backend with a temp HOME (redirects all ~/.vulos/* paths) ───
log "Starting backend on port $BACKEND_PORT..."
env HOME="$DEMO_HOME" \
  PORT="$BACKEND_PORT" \
  "$DEMO_HOME/vulos-server" \
  --env=local \
  2>"$DEMO_HOME/server.log" &
BACKEND_PID=$!

# Wait for the backend to be ready (up to 30s)
READY=0
for i in $(seq 1 30); do
  if curl -sf "${BASE_URL}/api/auth/status" >/dev/null 2>&1; then
    READY=1
    break
  fi
  sleep 1
done
[ "$READY" = "1" ] || fail "Backend did not come up within 30s — check $DEMO_HOME/server.log"
ok "Backend ready at $BASE_URL"

# ── 4. Register demo account ──────────────────────────────────────────────────
log "Registering demo account (${DEMO_USERNAME})..."
REG_RESP=$(curl -sf -X POST "${BASE_URL}/api/auth/register" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"${DEMO_USERNAME}\",\"password\":\"${DEMO_PASSWORD}\",\"display_name\":\"${DEMO_NAME}\"}" 2>&1) \
  || fail "Registration request failed: $REG_RESP"

# Extract session token from Set-Cookie (vulos_session=<token>) or response body
SESSION_TOKEN=$(echo "$REG_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('session',{}).get('token',''))" 2>/dev/null || true)

# If token not in body, log in explicitly to get it
if [ -z "$SESSION_TOKEN" ]; then
  log "Logging in to get session token..."
  LOGIN_RESP=$(curl -sf -c "$DEMO_HOME/cookies.txt" -X POST "${BASE_URL}/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${DEMO_USERNAME}\",\"password\":\"${DEMO_PASSWORD}\"}" 2>&1) \
    || fail "Login failed: $LOGIN_RESP"
  SESSION_TOKEN=$(echo "$LOGIN_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('session',{}).get('token',''))" 2>/dev/null || true)
fi

ok "Demo account registered: ${DEMO_USERNAME}"

# Helper: authenticated API call
api() {
  local method="$1"; shift
  local path="$1"; shift
  local body="${1:-}"
  local extra_args=()
  if [ -n "$body" ]; then
    extra_args+=(-H "Content-Type: application/json" -d "$body")
  fi
  if [ -n "$SESSION_TOKEN" ]; then
    curl -sf -b "vulos_session=${SESSION_TOKEN}" \
      -X "$method" "${BASE_URL}${path}" \
      "${extra_args[@]}" 2>/dev/null || true
  else
    curl -sf -b "$DEMO_HOME/cookies.txt" \
      -X "$method" "${BASE_URL}${path}" \
      "${extra_args[@]}" 2>/dev/null || true
  fi
}

# ── 5. Seed realistic content ─────────────────────────────────────────────────

# 5a. Pinned apps in the launchpad (via preferences)
log "Seeding pinned apps..."
api PUT "/api/profiles/me" '{"preferences":{"pinned_apps":"terminal,files,messages,apphub,activity,persona"}}' || \
  warn "Could not seed pinned apps (profile endpoint may differ)"

# 5b. Upload a few sample files
log "Seeding sample files..."
if api GET "/api/files" >/dev/null 2>&1; then
  # Create a sample text file
  echo "Welcome to Vulos — a web-native OS." | \
    curl -sf -b "vulos_session=${SESSION_TOKEN}" \
      -X POST "${BASE_URL}/api/files/upload" \
      -F "file=@-;filename=welcome.txt" 2>/dev/null || \
    curl -sf -b "vulos_session=${SESSION_TOKEN}" \
      -X POST "${BASE_URL}/api/files" \
      -H "Content-Type: application/json" \
      -d '{"name":"welcome.txt","content":"Welcome to Vulos — a web-native OS.","path":"/home/demo/welcome.txt"}' 2>/dev/null || \
    warn "File upload not available in dev mode (OK)"

  # Create a sample notes file
  printf "# Notes\n\n- Install apps from App Hub\n- Share files via Drop\n- Open Terminal with Ctrl+Alt+T\n" | \
    curl -sf -b "vulos_session=${SESSION_TOKEN}" \
      -X POST "${BASE_URL}/api/files/upload" \
      -F "file=@-;filename=notes.md" 2>/dev/null || \
    warn "Notes file upload skipped"
fi

# 5c. Seed a sample AI chat history so the AI panel shows content
log "Seeding AI chat history..."
api POST "/api/ai/chat" \
  '{"message":"Hello! Show me what Vulos can do.","history":[]}' || \
  warn "AI chat seed skipped (no AI backend in dev)"

# 5d. Update profile with display preferences
log "Seeding profile settings..."
api PUT "/api/profiles/me" \
  '{"name":"Demo User","preferences":{"theme":"dark","accent":"blue","wallpaper":"default"}}' || \
  warn "Profile update skipped"

# 5e. Notify the backend that first-time setup is done so shell shows desktop
if [ -d "/var/lib/vulos" ] 2>/dev/null; then
  touch /var/lib/vulos/.setup-complete 2>/dev/null || true
fi
# In dev mode we skip /var/lib — the local-mode backend does first-boot detection
# differently. The shell frontend auto-skips setup when env=local is detected.

ok "Content seeded"

# ── 6. Export env vars for the screenshotter ──────────────────────────────────
export SCREENSHOT_EMAIL="$DEMO_USERNAME"
export SCREENSHOT_PASSWORD="$DEMO_PASSWORD"
export BASE_URL

# ── 7. Run the Playwright screenshotter ──────────────────────────────────────
if [ "$SKIP_CAPTURE" = "1" ]; then
  log "SKIP_CAPTURE=1 — skipping screenshot capture"
  log "Backend still running at $BASE_URL with demo data"
  log "Run manually: SCREENSHOT_EMAIL=${DEMO_USERNAME} SCREENSHOT_PASSWORD=${DEMO_PASSWORD} BASE_URL=${BASE_URL} npm run screenshots"
  # Wait for manual inspection
  read -r -p "Press Enter to shut down the demo server and clean up..."
else
  log "Running Playwright screenshotter..."
  cd "$REPO_ROOT"
  SCREENSHOT_EMAIL="$DEMO_USERNAME" \
  SCREENSHOT_PASSWORD="$DEMO_PASSWORD" \
  BASE_URL="$BASE_URL" \
  node scripts/screenshots.mjs
  ok "Screenshots updated in docs/screenshots/"
fi
