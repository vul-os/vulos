#!/usr/bin/env bash
# install.sh — one-command self-host of the Vulos control plane, with an
# OPTIONAL relay stood up alongside it.
#
# Tiers this covers (see docs/SELF-HOST.md):
#   individual        : a box + (optionally) a relay — you don't need THIS at all.
#   fleet/enterprise  : + this control plane (accounts, enrollment, OS routing,
#                       relay fleet, admin console). That's what this installs.
#
# The relay is OPTIONAL. You only need one if a box lacks public reachability
# (behind NAT/CGNAT) or you want a stable public hostname you control. A box that
# already has a public IP + domain is reachable directly — skip the relay.
#
# What this script does:
#   1. Builds the control-plane binary (make build → ./bin/cp).
#   2. Writes a self-host env file (control-plane config; free no-op billing).
#   3. (--with-relay) Delegates to the vulos-relay repo's own installer to stand
#      up a relay next to the control plane.
#   4. (unless --no-run) Starts the control plane and health-checks it.
#
# Usage:
#   scripts/selfhost/install.sh --domain cp.example.com
#   scripts/selfhost/install.sh --domain cp.example.com --database-url postgres://…
#   scripts/selfhost/install.sh --domain cp.example.com \
#       --with-relay --relay-domain relay.example.com
#   scripts/selfhost/install.sh --domain cp.example.com --no-run   # build+config only
#
# The relay half needs Docker + Docker Compose and a checkout of the vulos-relay
# repo (default: a sibling ../vulos-relay; override with --relay-repo <path>).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"   # scripts/selfhost/ → repo root
cd "$REPO_ROOT"

# ── Defaults / args ───────────────────────────────────────────────────────────
DOMAIN=""
DATABASE_URL=""
CP_ADDR=":8080"
# Data/secrets live OUTSIDE the repo tree by default so nothing sensitive is ever
# committed (override with --data-dir or VULOS_DATA_DIR).
DATA_DIR="${VULOS_DATA_DIR:-$HOME/.vulos/selfhost}"
WITH_RELAY=0
RELAY_DOMAIN=""
RELAY_REPO=""
DO_RUN=1
FORCE=0

usage() { sed -n '2,38p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --domain)        DOMAIN="${2:-}"; shift 2 ;;
    --domain=*)      DOMAIN="${1#*=}"; shift ;;
    --database-url)  DATABASE_URL="${2:-}"; shift 2 ;;
    --database-url=*) DATABASE_URL="${1#*=}"; shift ;;
    --addr)          CP_ADDR="${2:-}"; shift 2 ;;
    --addr=*)        CP_ADDR="${1#*=}"; shift ;;
    --data-dir)      DATA_DIR="${2:-}"; shift 2 ;;
    --data-dir=*)    DATA_DIR="${1#*=}"; shift ;;
    --with-relay)    WITH_RELAY=1; shift ;;
    --relay-domain)  RELAY_DOMAIN="${2:-}"; WITH_RELAY=1; shift 2 ;;
    --relay-domain=*) RELAY_DOMAIN="${1#*=}"; WITH_RELAY=1; shift ;;
    --relay-repo)    RELAY_REPO="${2:-}"; shift 2 ;;
    --relay-repo=*)  RELAY_REPO="${1#*=}"; shift ;;
    --no-run)        DO_RUN=0; shift ;;
    --force)         FORCE=1; shift ;;
    -h|--help)       usage 0 ;;
    *) echo "install.sh: unknown option: $1" >&2; usage 1 ;;
  esac
done

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarn:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# ── Domain (prompt if unset) ──────────────────────────────────────────────────
if [ -z "$DOMAIN" ]; then
  printf 'Control-plane domain (e.g. cp.example.com) [localhost]: '
  read -r DOMAIN || true
  DOMAIN="${DOMAIN:-localhost}"
fi

# ── Preflight: Go toolchain ───────────────────────────────────────────────────
command -v go >/dev/null 2>&1 || die "go not found — install Go 1.26+ (https://go.dev/dl/)"

# ── 1. Build the control-plane binary ─────────────────────────────────────────
log "building control plane (make build) ..."
if command -v make >/dev/null 2>&1; then
  make build
else
  mkdir -p bin && go build -o bin/cp ./cmd/server && echo "built ./bin/cp"
fi
[ -x "$REPO_ROOT/bin/cp" ] || die "build did not produce ./bin/cp"

# ── 2. Write the self-host env file ───────────────────────────────────────────
mkdir -p "$DATA_DIR"
ENV_FILE="$DATA_DIR/cp.env"
# Durable on-disk SQLite by default; Postgres if a DSN was supplied.
SQLITE_PATH="$DATA_DIR/cp.db"

# Strong secret generator (openssl, else /dev/urandom).
gen_secret() {
  if command -v openssl >/dev/null 2>&1; then openssl rand -hex 32
  else head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'; fi
}

if [ -f "$ENV_FILE" ] && [ "$FORCE" -ne 1 ]; then
  warn "$ENV_FILE exists — keeping it (pass --force to regenerate)."
else
  # The prod resolver (VULOS_ENV, fail-safe to prod) drives the fail-CLOSED
  # gates. Full "prod" additionally requires push (APNS/FCM) + KEK secrets a
  # self-hoster typically has no reason to hold — so the working self-host
  # posture is VULOS_ENV=local, which relaxes those provider-specific gates.
  # We still generate a REAL SESSION_SECRET (never the dev fallback). To run the
  # full production hardening, set VULOS_ENV=prod and supply the push/KEK vars
  # documented in docs/SELF-HOST.md.
  SESSION_SECRET="$(gen_secret)"
  {
    echo "# Generated by scripts/selfhost/install.sh — control-plane self-host config."
    echo "# Free no-op billing seam + bring-your-own-bucket storage (never charges)."
    echo "VULOS_DOMAIN=$DOMAIN"
    echo "CP_ADDR=$CP_ADDR"
    echo "CP_ENV=selfhost           # free-form label surfaced on /version"
    echo "VULOS_ENV=local           # working self-host posture (see note above); prod adds push/KEK requirements"
    echo "# Signs session cookies — keep secret; rotating it logs everyone out."
    echo "SESSION_SECRET=$SESSION_SECRET"
    if [ -n "$DATABASE_URL" ]; then
      echo "DATABASE_URL=$DATABASE_URL          # Postgres (production)"
    else
      echo "# DATABASE_URL unset ⇒ local SQLite at the path below (durable):"
      echo "CP_SQLITE_PATH=$SQLITE_PATH"
    fi
    echo "# Optional: set RECOVERY_KEK to mount account-recovery routes (fail-closed if unset)."
  } > "$ENV_FILE"
  chmod 600 "$ENV_FILE"
  log "wrote $ENV_FILE (domain=$DOMAIN, db=$([ -n "$DATABASE_URL" ] && echo postgres || echo sqlite))"
fi

# ── 3. Optional relay alongside the control plane ─────────────────────────────
if [ "$WITH_RELAY" -eq 1 ]; then
  [ -n "$RELAY_DOMAIN" ] || { printf 'Relay base domain (e.g. relay.example.com): '; read -r RELAY_DOMAIN || true; }
  [ -n "$RELAY_DOMAIN" ] || die "--with-relay needs --relay-domain relay.example.com"

  # Locate the vulos-relay checkout (sibling by default).
  if [ -z "$RELAY_REPO" ]; then
    for cand in "$REPO_ROOT/../vulos-relay" "$HOME/code/vulos/vulos-relay"; do
      [ -x "$cand/scripts/install.sh" ] && { RELAY_REPO="$cand"; break; }
    done
  fi

  if [ -n "$RELAY_REPO" ] && [ -x "$RELAY_REPO/scripts/install.sh" ]; then
    log "standing up a relay via $RELAY_REPO/scripts/install.sh ..."
    RELAY_UP_FLAG="--no-up"; [ "$DO_RUN" -eq 1 ] && RELAY_UP_FLAG=""
    ( cd "$RELAY_REPO" && ./scripts/install.sh --domain "$RELAY_DOMAIN" $RELAY_UP_FLAG )
  else
    warn "vulos-relay checkout not found — skipping relay bring-up."
    cat <<EOF

  To add a relay, clone the relay repo and run its one-command installer:
    git clone https://github.com/vul-os/vulos-relay
    cd vulos-relay && ./scripts/install.sh --domain $RELAY_DOMAIN

  Then re-run this with --relay-repo /path/to/vulos-relay to link it.
EOF
  fi
else
  log "relay NOT requested — the control plane runs standalone."
  echo "    (Add one later with --with-relay --relay-domain relay.example.com if a box needs public reachability.)"
fi

# ── 4. Run the control plane ──────────────────────────────────────────────────
if [ "$DO_RUN" -ne 1 ]; then
  cat <<EOF

Config written. Start the control plane with:
  set -a; . "$ENV_FILE"; set +a
  ./bin/cp

Put it behind your own TLS-terminating reverse proxy (Caddy/nginx/Traefik).
EOF
  exit 0
fi

log "starting control plane on $CP_ADDR ..."
set -a; . "$ENV_FILE"; set +a
"$REPO_ROOT/bin/cp" >"$DATA_DIR/cp.log" 2>&1 &
CP_PID=$!
echo "$CP_PID" > "$DATA_DIR/cp.pid"

# Health check /healthz on the loopback bind.
HC_HOST="127.0.0.1"; HC_PORT="${CP_ADDR##*:}"; [ -n "$HC_PORT" ] || HC_PORT=8080
ok=0
for _ in $(seq 1 20); do
  if curl -fsS "http://$HC_HOST:$HC_PORT/healthz" >/dev/null 2>&1; then ok=1; break; fi
  # Bail early if the process died.
  kill -0 "$CP_PID" 2>/dev/null || break
  sleep 0.5
done

if [ "$ok" -eq 1 ]; then
  log "control plane is up (pid $CP_PID). /version confirms the billing rail:"
  curl -fsS "http://$HC_HOST:$HC_PORT/version" 2>/dev/null || true; echo
  cat <<EOF

Running. Logs: $DATA_DIR/cp.log   Stop: kill \$(cat $DATA_DIR/cp.pid)

Next: put ./bin/cp behind a TLS-terminating reverse proxy for internet access.
Full guide + tiers + optional relay: docs/SELF-HOST.md
EOF
else
  warn "control plane did not report healthy — see $DATA_DIR/cp.log"
  kill "$CP_PID" 2>/dev/null || true
  exit 1
fi
