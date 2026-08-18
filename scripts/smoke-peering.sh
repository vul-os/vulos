#!/bin/sh
# SMOKE-01 — Peering route regression guard
#
# Builds the OSS backend binary, starts it on localhost, waits for /health,
# then curls every registered peering HTTP route and asserts the response is
# NOT 501 (Not Implemented).  A 501 means the handler exists in the code but
# was never wired into the router — exactly the class of regression this script
# guards against.
#
# Any other status (200, 400, 401, 403, 404, 503 …) is treated as "wired" and
# counts as a pass for that route.  Only 501 counts as a failure.
#
# Port:
#   The server reads PORT from .env.local > .env > env var > default 8080.
#   This script writes a temporary .env.local to inject the desired port so
#   the server binds on a port we control.  The file is removed on exit.
#   Use --port to choose a specific port (default: auto-select a free one).
#
# TLS:
#   If ~/.vulos/localhost.pem (mkcert) or /etc/vulos/tls/cert.pem exists the
#   server auto-enables TLS.  The script detects this from the startup log and
#   probes via https:// + -k (insecure, for self-signed dev certs).
#
# Usage:
#   scripts/smoke-peering.sh             # normal run; exits 0 on pass / 1 on fail
#   scripts/smoke-peering.sh --port 9090 # explicit port
#
# Requirements: go, curl
# Runs headless; no external services required.

set -eu

# ── tool guards ────────────────────────────────────────────────────────────────
command -v go   >/dev/null 2>&1 || { echo "SKIP: go not found — install Go to run this smoke test" >&2; exit 0; }
command -v curl >/dev/null 2>&1 || { echo "ERROR: curl not found" >&2; exit 1; }

# ── colour helpers ─────────────────────────────────────────────────────────────
c_b='\033[0;34m'; c_g='\033[0;32m'; c_r='\033[0;31m'; c_d='\033[2m'; c_n='\033[0m'
say() { printf "${c_b}▸ %s${c_n}\n" "$*"; }
ok()  { printf "${c_g}✓ %s${c_n}\n" "$*"; }
err() { printf "${c_r}✗ %s${c_n}\n" "$*" >&2; }

# ── locate repo root ───────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO="$SCRIPT_DIR/.."

# ── parse args ─────────────────────────────────────────────────────────────────
PORT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --port) PORT="$2"; shift 2 ;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

# Auto-select a free port when not specified.
if [ -z "$PORT" ]; then
  # Prefer python3 for reliable free-port selection; fall back to a high fixed port.
  PORT=$(python3 -c \
    "import socket; s=socket.socket(); s.bind(('',0)); p=s.getsockname()[1]; s.close(); print(p)" \
    2>/dev/null) || PORT=19877
fi

TIMEOUT=30   # seconds to wait for server ready

# ── temporary files ────────────────────────────────────────────────────────────
SRV_LOG=/tmp/vulos-smoke-srv-$$.log
SRV_BIN=/tmp/vulos-smoke-server-$$
BUILD_LOG=/tmp/vulos-smoke-build-$$.log
ENV_LOCAL_BAK=/tmp/vulos-smoke-envbak-$$   # backup of existing .env.local
ENV_LOCAL_ORIG=0     # 1 = .env.local existed before we touched it
ENV_LOCAL_WRITTEN=0  # 1 = we wrote .env.local
SRV_PID=""
# ISOLATED STATE. The server is pointed at a throwaway data dir (datadir.Root
# honours VULOS_DATA_DIR, defaulting to $HOME/.vulos) for two reasons: this
# script registers a user below, which must not touch the developer's own box,
# and registration is only unauthenticated while NO user exists — so the dir
# has to be empty on every run or the probe silently loses its session.
DATA_DIR=/tmp/vulos-smoke-datadir-$$
COOKIES=/tmp/vulos-smoke-cookies-$$

# ── cleanup ────────────────────────────────────────────────────────────────────
cleanup() {
  # Kill server
  if [ -n "$SRV_PID" ]; then
    kill "$SRV_PID" 2>/dev/null || true
    wait "$SRV_PID" 2>/dev/null || true
  fi
  # Remove temp binary + logs
  rm -f "$SRV_BIN" "$SRV_LOG" "$BUILD_LOG" "$COOKIES"
  rm -rf "$DATA_DIR"
  # Restore .env.local
  if [ "$ENV_LOCAL_WRITTEN" = "1" ]; then
    if [ "$ENV_LOCAL_ORIG" = "1" ] && [ -f "$ENV_LOCAL_BAK" ]; then
      mv "$ENV_LOCAL_BAK" "$REPO/.env.local"
    else
      rm -f "$REPO/.env.local"
    fi
    rm -f "$ENV_LOCAL_BAK"
  fi
}
trap cleanup EXIT INT TERM

# ── 1. Build ───────────────────────────────────────────────────────────────────
say "Building backend binary…"
if ! ( cd "$REPO/backend" && go build -o "$SRV_BIN" ./cmd/server ) >"$BUILD_LOG" 2>&1; then
  err "go build failed:"
  cat "$BUILD_LOG" >&2
  exit 1
fi
ok "binary built"

# ── 2. Inject PORT via .env.local ──────────────────────────────────────────────
# The server's config loader reads .env.local before .env, so writing PORT
# there is the portable way to override the port regardless of .env contents.
if [ -f "$REPO/.env.local" ]; then
  ENV_LOCAL_ORIG=1
  cp "$REPO/.env.local" "$ENV_LOCAL_BAK"
fi
# Write a minimal .env.local that only sets PORT.
printf 'PORT=%s\n' "$PORT" >"$REPO/.env.local"
ENV_LOCAL_WRITTEN=1

# ── 3. Start server ────────────────────────────────────────────────────────────
say "Starting server on port ${PORT}…"
rm -rf "$DATA_DIR"; mkdir -p "$DATA_DIR"
VULOS_DATA_DIR="$DATA_DIR" VULOS_PREWARM_BROWSER=0 "$SRV_BIN" -env local >"$SRV_LOG" 2>&1 &
SRV_PID=$!

# ── 4. Wait for readiness + detect TLS ────────────────────────────────────────
say "Waiting up to ${TIMEOUT}s for http(s)://127.0.0.1:${PORT}/health …"
deadline=$(( $(date +%s) + TIMEOUT ))
READY=0
SCHEME=http
while [ "$(date +%s)" -lt "$deadline" ]; do
  if ! kill -0 "$SRV_PID" 2>/dev/null; then
    err "server process exited early"
    tail -20 "$SRV_LOG" >&2 || true
    exit 1
  fi
  # Auto-detect TLS from startup log
  if grep -q "with TLS" "$SRV_LOG" 2>/dev/null; then
    SCHEME=https
  fi
  BASE="${SCHEME}://127.0.0.1:${PORT}"
  STATUS=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 2 "${BASE}/health" 2>/dev/null || true)
  if [ "$STATUS" = "200" ]; then
    READY=1
    break
  fi
  sleep 1
done

if [ "$READY" != "1" ]; then
  err "server did not become ready within ${TIMEOUT}s"
  tail -30 "$SRV_LOG" >&2 || true
  exit 1
fi
BASE="${SCHEME}://127.0.0.1:${PORT}"
ok "server is up at ${BASE}"

# ── 4a. A SESSION, so the probe reaches the ROUTER ────────────────────────────
#
# Without this every probe below is answered 401 by auth.Handler.Middleware
# (backend/services/auth/handlers.go), which wraps the whole mux in
# backend/cmd/server/main.go, and the ServeMux is never consulted at all. See
# the negative control immediately below for how that was measured.
#
# POST /api/auth/register is in auth's publicPaths and, when the store holds no
# users, creates the first user AND logs it in, setting the session cookie. The
# data dir above is fresh on every run, so that precondition always holds.
#
# SIDE EFFECT, deliberately noted rather than hidden: registration fires
# auth.Handler.OnUserCreated -> sysuser.EnsureUser, which shells out to
# `adduser`. That needs root and this process is not root (nor is a GitHub
# runner), so it fails and is logged — no system user is created. If this
# script is ever run AS ROOT it would create a real Linux user called
# "vulos-smoke". Do not run it as root.
say "Registering a throwaway first user to obtain a session…"
REG_STATUS=$(curl -sk -c "$COOKIES" -o /dev/null -w '%{http_code}' --max-time 10 \
  -X POST -H 'Content-Type: application/json' \
  --data-raw '{"username":"vulos-smoke","password":"Vulos-Smoke-Passw0rd!","display_name":"vulos smoke"}' \
  "${BASE}/api/auth/register" 2>/dev/null || echo "000")
if [ "$REG_STATUS" != "200" ]; then
  err "could not register the throwaway user (HTTP ${REG_STATUS})."
  err "Registration is public only while the store has NO users; \$DATA_DIR is"
  err "created fresh each run, so a non-200 here means the auth surface changed."
  tail -20 "$SRV_LOG" >&2 || true
  exit 1
fi
ok "session established"

# Every probe from here on carries the session cookie.
AUTH_ARGS="-b $COOKIES"

# ── 4b. NEGATIVE CONTROL — can this probe see the router at all? ───────────────
#
# MEASURED 2026-08-18, and it is why this section exists: 61 of the 65 routes
# below answered 401, and so did
#
#     GET /api/totally/bogus/xyz        ->  401
#
# a path that is registered nowhere in the tree. The box wraps its whole mux in
# auth.Handler.Middleware (backend/cmd/server/main.go, `handler :=
# secHeadersMiddleware(appsgate.Middleware(routeVerifier.Middleware(
# authHandler.Middleware(mainHandler))))`), so an unauthenticated request is
# answered by the MIDDLEWARE and never reaches the ServeMux. A route that had
# been deleted from the router, or one whose handler returns 501, is therefore
# indistinguishable from a working one: both are 401.
#
# That was verified by mutation — handleICEConfig was replaced with a handler
# that returns 501 and this script still printed
#
#     PASS — all 65 peering routes are wired (none returned 501)
#
# So the probe must first establish that it can tell a real route from a
# missing one. Every route whose status EQUALS the control's is counted as NOT
# OBSERVED, and the verdict below refuses to call those verified.
CONTROL_PATH_A="/api/peering/__vulos_smoke_no_such_route__"
CONTROL_PATH_B="/api/__vulos_smoke_no_such_route__"
CONTROL_STATUS=$(curl -sk $AUTH_ARGS -o /dev/null -w '%{http_code}' --max-time 5 "${BASE}${CONTROL_PATH_A}" 2>/dev/null || echo "000")
CONTROL_STATUS_B=$(curl -sk $AUTH_ARGS -o /dev/null -w '%{http_code}' --max-time 5 "${BASE}${CONTROL_PATH_B}" 2>/dev/null || echo "000")
say "negative control: two paths that exist nowhere answered ${CONTROL_STATUS} and ${CONTROL_STATUS_B}"
if [ "$CONTROL_STATUS" != "$CONTROL_STATUS_B" ]; then
  err "the two nonexistent-path controls disagree (${CONTROL_STATUS} vs ${CONTROL_STATUS_B})."
  err "This script cannot decide what 'not routed' looks like, so it cannot"
  err "classify anything below. Investigate before trusting this gate."
  exit 1
fi
if [ "$CONTROL_STATUS" != "404" ]; then
  err "a path registered nowhere answered ${CONTROL_STATUS}, not 404."
  err "Something in front of the ServeMux is answering for it, so this probe is"
  err "NOT observing the router and cannot tell a wired route from a missing one."
  err "That is precisely the state this gate spent weeks in: see 4b above."
  exit 1
fi
ok "control returns 404 — this probe is observing the ROUTER, not a middleware"

# ── 5. Route table ─────────────────────────────────────────────────────────────
#
# Tab-separated: METHOD<TAB>PATH[<TAB>BODY]
# PLACEHOLDER_ID / PLACEHOLDER_HASH are substituted with sentinel values.
# Routes that need auth will 401; routes to non-existent resources will 404.
# Both are fine — only 501 (Not Implemented) indicates an unwired handler.

PLACEHOLDER_ID="00000000-0000-0000-0000-000000000000"
PLACEHOLDER_HASH="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

routes() {
cat <<'ROUTES'
# peering identity (PEER-02 / PEER-03)
GET	/api/peering/identity
POST	/api/peering/identity/export	{}
POST	/api/peering/identity/import	{"key":""}
# peering WebSocket multiplex stream (PEER-07)
GET	/api/peering/stream
# bandwidth meter (PEER-20b)
GET	/api/peering/bandwidth
GET	/api/peering/bandwidth/peer
# contacts (PEER-08)
GET	/api/peering/contacts
GET	/api/peering/contacts/requests
POST	/api/peering/contacts/request	{"vulos_id":"test","server":"localhost"}
POST	/api/peering/contacts/approve/PLACEHOLDER_ID	{}
POST	/api/peering/contacts/block/PLACEHOLDER_ID	{}
DELETE	/api/peering/contacts/PLACEHOLDER_ID
# messaging / conversations (PEER-09)
GET	/api/peering/conversations
GET	/api/peering/conversations/PLACEHOLDER_ID/messages
POST	/api/peering/conversations/PLACEHOLDER_ID/send	{"text":"hi"}
# groups (PEER-11)
GET	/api/peering/groups
POST	/api/peering/groups	{"name":"test"}
GET	/api/peering/groups/PLACEHOLDER_ID
POST	/api/peering/groups/PLACEHOLDER_ID/members	{"vulos_id":"x"}
POST	/api/peering/groups/PLACEHOLDER_ID/send	{"text":"hi"}
# calls (PEER-13 / PEER-14)
POST	/api/peering/call/initiate	{"peer_id":"x","sdp":""}
POST	/api/peering/call/answer	{"call_id":"x","sdp":""}
POST	/api/peering/call/reject	{"call_id":"x"}
POST	/api/peering/call/signal	{"call_id":"x","candidate":""}
POST	/api/peering/call/hangup	{"call_id":"x"}
# call history (PEER-16)
GET	/api/peering/call/history
POST	/api/peering/call/history/record	{"peer_id":"x","direction":"outbound","duration_s":0}
# profile (PEER-12)
GET	/api/peering/profile
PUT	/api/peering/profile	{"display_name":"test"}
POST	/api/peering/profile/image	{}
GET	/api/peering/profile/image
GET	/api/peering/profile/PLACEHOLDER_ID
POST	/api/peering/profile/notify-change	{}
# media (PEER-17)
POST	/api/peering/media/upload	{}
GET	/api/peering/media/fetch/PLACEHOLDER_HASH
GET	/api/peering/media/thumb/PLACEHOLDER_HASH
# relay store-and-forward (PEER-18)
POST	/api/peering/relay/deposit	{"envelope":""}
GET	/api/peering/relay/pickup
POST	/api/peering/relay/ack	{"ids":[]}
GET	/api/peering/relay/attest
# email verification (PEER-19)
POST	/verify/email/start	{"email":"smoke@example.com"}
GET	/verify/email/confirm
GET	/verify/email/status
# directory discovery (PEER-21)
GET	/api/peering/discover
# ICE / TURN config (PEER-22)
GET	/api/peering/ice
# endpoints registry (PEER-23)
GET	/api/peering/endpoints
POST	/api/peering/endpoints/register	{"address":"localhost:1234","transport":"tcp"}
DELETE	/api/peering/endpoints/PLACEHOLDER_ID
PUT	/api/peering/endpoints/PLACEHOLDER_ID/priority	{"priority":1}
# drop / LAN file transfer (PEER-24 / PEER-25)
GET	/api/peering/drop/nearby
POST	/api/peering/drop/send	{"peer_id":"x"}
GET	/api/peering/drop/settings
POST	/api/peering/drop/decide	{"transfer_id":"x","accept":false}
# proximity drop codes (PEER-26)
POST	/api/peering/drop/code/generate	{}
POST	/api/peering/drop/code/redeem	{"code":"XXXXXX"}
# feeds (PEER-27)
GET	/api/feeds
POST	/api/feeds	{"title":"test"}
GET	/api/feeds/PLACEHOLDER_ID
POST	/api/feeds/PLACEHOLDER_ID/publish	{"content":"hello"}
GET	/api/feeds/PLACEHOLDER_ID/entries
# collab CRDT (PEER-28)
GET	/api/peering/collab/documents
POST	/api/peering/collab/share	{"title":"test"}
GET	/api/peering/collab/PLACEHOLDER_ID
DELETE	/api/peering/collab/PLACEHOLDER_ID
# well-known identity (PEER-12)
GET	/.well-known/vulos-id
ROUTES
}

# ── 6. Probe each route ────────────────────────────────────────────────────────
FAILURES=0
TOTAL=0
OBSERVED=0
UNOBSERVED=0

say "Probing peering routes (only 501 = failure)…"
printf "${c_d}  %-8s %-4s  %s${c_n}\n" "METHOD" "STS" "PATH"

while IFS='	' read -r method path body; do
  # Skip comment / blank lines
  case "$method" in
    ''|\#*) continue ;;
  esac

  # Substitute placeholders in the path
  path_real="$(echo "$path" \
    | sed "s/PLACEHOLDER_ID/$PLACEHOLDER_ID/g; s/PLACEHOLDER_HASH/$PLACEHOLDER_HASH/g")"
  TOTAL=$(( TOTAL + 1 ))

  # Build curl arguments: -sk = silent + insecure (handles self-signed dev certs)
  set -- -sk $AUTH_ARGS -o /dev/null -w '%{http_code}' --max-time 5

  case "$method" in
    GET)    set -- "$@" -X GET ;;
    POST)   set -- "$@" -X POST \
              -H 'Content-Type: application/json' \
              --data-raw "${body:-{}}" ;;
    PUT)    set -- "$@" -X PUT \
              -H 'Content-Type: application/json' \
              --data-raw "${body:-{}}" ;;
    DELETE) set -- "$@" -X DELETE ;;
    *)      set -- "$@" -X "$method" ;;
  esac

  STATUS=$(curl "$@" "${BASE}${path_real}" 2>/dev/null || echo "000")

  # A status identical to the nonexistent-path control tells us nothing about
  # this route: the same answer is returned for paths that are registered
  # nowhere. Count it, but do not call it verified.
  if [ "$STATUS" = "$CONTROL_STATUS" ]; then
    UNOBSERVED=$(( UNOBSERVED + 1 ))
  else
    OBSERVED=$(( OBSERVED + 1 ))
  fi

  if [ "$STATUS" = "501" ]; then
    err "501  $method $path_real  ← NOT WIRED"
    FAILURES=$(( FAILURES + 1 ))
  else
    printf "  ${c_d}%-8s ${c_g}%-4s${c_d}  %s${c_n}\n" "$method" "$STATUS" "$path_real"
  fi
done <<EOF
$(routes | grep -v '^#' | grep -v '^$')
EOF

# ── 7. Verdict ─────────────────────────────────────────────────────────────────
echo ""
say "Checked ${TOTAL} peering routes."

# COVERAGE ASSERTION.
#
# Without this, a routes() table that failed to parse — a mangled heredoc, a
# tab converted to spaces by an editor, a bad `grep` filter — yields TOTAL=0,
# FAILURES=0, and this script prints "PASS — all 0 peering routes are wired"
# and exits 0. That is a gate that passes by doing nothing.
#
# The floor is deliberately well below the current route count so that removing
# a route does not trip it, while a wholesale parse failure does. Raise it if
# the table grows substantially.
MIN_ROUTES=40
if [ "$TOTAL" -lt "$MIN_ROUTES" ]; then
  err "FAIL — only ${TOTAL} routes were probed, expected at least ${MIN_ROUTES}."
  err "The route table did not parse correctly, so this run verified almost"
  err "nothing. Check the routes() heredoc in $0 (it is TAB-separated)."
  exit 1
fi

if [ "$FAILURES" -gt 0 ]; then
  err "FAIL — ${FAILURES} route(s) returned 501 (not implemented / not wired)"
  exit 1
fi

# OBSERVATION ASSERTION — the one that stops this gate reporting a conclusion
# it has not earned.
#
# A route only counts as checked if its response DIFFERED from the response
# given to a path that is registered nowhere. Otherwise the answer came from a
# middleware in front of the router and says nothing about whether the route
# exists. Without this, the line below read "all 65 peering routes are wired"
# on a run in which 61 of them were never dispatched to a handler at all.
#
# The floor is most of the table: this gate's whole purpose is the routing
# layer, and a run that cannot see the routing layer has not performed it.
MIN_OBSERVED=40
say "${OBSERVED} of ${TOTAL} routes were actually observed past the front-door middleware (${UNOBSERVED} were not)."
if [ "$OBSERVED" -lt "$MIN_OBSERVED" ]; then
  err "FAIL — only ${OBSERVED} of ${TOTAL} routes were OBSERVED; ${UNOBSERVED} returned ${CONTROL_STATUS},"
  err "the same status a path registered nowhere returns. For those routes this run"
  err "did not distinguish 'wired' from 'deleted' or from 'handler returns 501'."
  err ""
  err "This is NOT a regression in the peering routes — it is this gate reporting"
  err "that it cannot see them. The box answers unauthenticated /api/* requests"
  err "from auth.Handler.Middleware before the ServeMux runs, so the probe needs a"
  err "session to reach any handler."
  err ""
  err "To fix, in order of preference:"
  err "  1. Give this script a session and probe as an authenticated caller."
  err "     NOTE: POST /api/auth/register is public only for the FIRST user and"
  err "     fires auth.Handler.OnUserCreated -> sysuser.EnsureUser, which shells"
  err "     out to adduser. Decide that side effect deliberately before adding it"
  err "     to CI, and point the server at a throwaway data dir."
  err "  2. Assert route registration statically instead (grep the mux registrations)."
  err "  3. Narrow this gate's claim to the routes it can genuinely reach."
  exit 1
fi

ok "PASS — ${OBSERVED} of ${TOTAL} peering routes observed past the middleware, none returned 501"
exit 0
