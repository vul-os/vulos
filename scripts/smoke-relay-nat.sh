#!/usr/bin/env bash
# scripts/smoke-relay-nat.sh — the box behind NAT, for real.
#
# scripts/smoke-relay.sh proves multi-relay, failover and config hygiene, but
# everything in it binds 127.0.0.1, where "the box is reachable from outside"
# is not actually being tested: on loopback nothing was ever unreachable. The
# central promise of the reverse tunnel (docs/REACH.md: "Most boxes sit behind
# NAT, CGNAT, or a router nobody can configure") had NO automated evidence.
#
# This script supplies it. A box runs with a stateful firewall that DROPS every
# new inbound connection and permits only replies to connections it opened
# itself — which is precisely what NAT/CGNAT does to reachability — and must
# still be publicly reachable through the relay.
#
# # Why a firewall and not two docker networks
#
# MEASURED on this host, not assumed: two separate docker bridge networks do
# NOT isolate. A container on network A reached a container on network B by IP
# on the first try (OrbStack routes between bridges). A harness built on
# network separation would have reported a NAT property it never established —
# the exact failure mode this script exists to avoid. A stateful INPUT policy
# is enforced inside the box's own netns and is verifiable from outside it.
#
# # The control probe is the point
#
# The load-bearing assertion here is a NEGATIVE one: the relay MUST NOT be able
# to dial the box directly. For that to mean anything, the box has to be
# running something that WOULD answer if it were reachable — otherwise the
# probe fails because nothing was listening, and the NAT claim is unfounded.
# So the box serves the SAME handler on a direct listener (REACHBOX_LISTEN),
# and the script asserts:
#
#   1. relay -> box direct  = BLOCKED   (there is genuinely no inbound path)
#   2. box   -> relay       = works     (egress works, so 1 is not "no network")
#   3. box's direct listener answers from INSIDE the box's own netns
#                                       (it really would answer if reachable)
#   4. host  -> relay -> box = works    (public reachability with no inbound path)
#
# Drop any one and the conclusion collapses. 1 without 2 and 3 is just a broken
# container.
#
# Usage:
#   scripts/smoke-relay-nat.sh
#
# Requirements: docker (running), go, curl. Pulls alpine:3 and installs
# iptables inside the box container, so it needs image-registry network access
# the first time. Wired into CI as the REACH-NAT job (.github/workflows/ci.yml);
# that job carries no `needs:`, so an unrelated backend failure cannot take this
# gate's coverage down with it.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
BACKEND="$REPO/backend"

c_b='\033[0;34m'; c_g='\033[0;32m'; c_r='\033[0;31m'; c_y='\033[0;33m'; c_n='\033[0m'
say()  { printf "${c_b}▸ %s${c_n}\n" "$*"; }
ok()   { printf "${c_g}✓ %s${c_n}\n" "$*"; }
warn() { printf "${c_y}! %s${c_n}\n" "$*"; }
die()  { printf "${c_r}✗ %s${c_n}\n" "$*" >&2; exit 1; }

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  grep '^#' "$0" | sed 's/^# \{0,1\}//'
  exit 0
fi

ASSERTIONS=0
FAILURES=0
check() {
  local desc="$1" cond="$2"
  ASSERTIONS=$((ASSERTIONS + 1))
  if [ "$cond" = "0" ]; then
    ok "$desc"
  else
    printf "${c_r}✗ FAIL: %s${c_n}\n" "$desc" >&2
    FAILURES=$((FAILURES + 1))
  fi
}

command -v go   >/dev/null 2>&1 || die "go not found"
command -v curl >/dev/null 2>&1 || die "curl not found"

# A missing docker is a SKIP, and it is printed so it cannot be mistaken for a
# pass — a skip that reads as green is how a gate stops checking anything.
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  printf "${c_y}! SKIPPED — docker is not available/running. THIS IS NOT A PASS:${c_n}\n" >&2
  printf "${c_y}!   the box-behind-NAT property was NOT verified by this run.${c_n}\n" >&2
  exit 0
fi

NET=vulos-natsmoke-net
BOX=vulos-natsmoke-box
RELAY=vulos-natsmoke-relay
WORK="$(mktemp -d "${TMPDIR:-/tmp}/vulos-smoke-nat.XXXXXX")"
BOX_DIRECT_PORT=9090
RELAY_PORT=8443
HOST_PORT=""

cleanup() {
  docker rm -f "$BOX" "$RELAY" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup >/dev/null 2>&1 || true

# ── 1. Build static linux binaries for the container arch ───────────────────
ARCH="$(docker version --format '{{.Server.Arch}}')"
say "Building static linux/$ARCH binaries (real cmd/vulos + reachbox)…"
( cd "$BACKEND" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -o "$WORK/vulos" ./cmd/vulos ) \
  || die "go build ./cmd/vulos failed"
( cd "$BACKEND" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -o "$WORK/reachbox" ./services/reach/cmd/reachbox ) \
  || die "go build reachbox failed"
chmod +x "$WORK/vulos" "$WORK/reachbox"
ok "built vulos and reachbox for linux/$ARCH"

# ── 2. A real TLS certificate on a real domain ──────────────────────────────
#
# Not decoration, and not optional. The box-side config REFUSES plaintext http
# to a non-loopback host even with VULOS_RELAY_ALLOW_INSECURE=1 (that opt-out
# is loopback-only, by design), so a NAT'd box CANNOT be wired up over http at
# all — discovered by this script failing to start. The tunnel dialer also
# uses ordinary public TLS with no custom roots, so the CA has to be trusted by
# the box the way a real one would be: through the system trust store, via
# SSL_CERT_FILE.
#
# The relay serves the tunnel endpoint ONLY on its apex host (Server.isApex
# compares Host to -domain), so the box must dial the relay AS "nat.test", not
# as an IP or a container name. --add-host supplies that name.
say "Issuing a CA and a *.nat.test server certificate…"
mkdir -p "$WORK/tls"
openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -keyout "$WORK/tls/ca.key" -out "$WORK/tls/ca.crt" \
  -subj "/CN=vulos-nat-smoke-ca" >/dev/null 2>&1 || die "could not create the CA"
openssl req -newkey rsa:2048 -nodes \
  -keyout "$WORK/tls/server.key" -out "$WORK/tls/server.csr" \
  -subj "/CN=nat.test" >/dev/null 2>&1 || die "could not create the server key/CSR"
cat >"$WORK/tls/ext.cnf" <<'EOF'
subjectAltName = DNS:nat.test, DNS:*.nat.test
extendedKeyUsage = serverAuth
EOF
openssl x509 -req -in "$WORK/tls/server.csr" -CA "$WORK/tls/ca.crt" -CAkey "$WORK/tls/ca.key" \
  -CAcreateserial -days 2 -extfile "$WORK/tls/ext.cnf" -out "$WORK/tls/server.crt" >/dev/null 2>&1 \
  || die "could not sign the server certificate"
chmod 644 "$WORK/tls/ca.crt" "$WORK/tls/server.crt"
chmod 600 "$WORK/tls/server.key"
ok "issued a CA and a cert for nat.test / *.nat.test"

# ── 3. Relay container ───────────────────────────────────────────────────────
cat >"$WORK/grants.json" <<'EOF'
[{"token":"nat-box1","names":["box1"]}]
EOF
chmod 600 "$WORK/grants.json"

docker network create "$NET" >/dev/null
say "Starting the relay (real \`vulos relay serve\`, real TLS) in a container…"
docker run -d --name "$RELAY" --network "$NET" \
  -v "$WORK:/cfg:ro" -p "127.0.0.1:0:$RELAY_PORT" alpine:3 \
  /cfg/vulos relay serve -addr "0.0.0.0:$RELAY_PORT" -domain nat.test \
  -cert /cfg/tls/server.crt -key /cfg/tls/server.key \
  -grants-file /cfg/grants.json >/dev/null || die "could not start the relay container"

RELAY_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$RELAY")
HOST_PORT=$(docker port "$RELAY" "$RELAY_PORT/tcp" | head -n1 | sed 's/.*://')
[ -n "$RELAY_IP" ] || die "could not determine the relay's container IP"
[ -n "$HOST_PORT" ] || die "could not determine the relay's published host port"

CURL_CA=("--cacert" "$WORK/tls/ca.crt" "--resolve" "nat.test:$HOST_PORT:127.0.0.1" "--resolve" "box1.nat.test:$HOST_PORT:127.0.0.1" "--resolve" "nosuchbox.nat.test:$HOST_PORT:127.0.0.1")
deadline=$(( $(date +%s) + 30 ))
until curl -fsS --max-time 2 "${CURL_CA[@]}" "https://nat.test:$HOST_PORT/_vulos-reach/v1/health" >/dev/null 2>&1; do
  [ "$(date +%s)" -lt "$deadline" ] || { docker logs "$RELAY" >&2 2>&1 || true; die "relay never became healthy"; }
  sleep 0.3
done
ok "relay healthy over TLS at nat.test:$RELAY_PORT (container $RELAY_IP, host 127.0.0.1:$HOST_PORT)"

# ── 3. Box container, behind a stateful firewall ────────────────────────────
cat >"$WORK/endpoints.json" <<EOF
[{"url":"https://nat.test:$RELAY_PORT","name":"box1","token":"nat-box1"}]
EOF
chmod 600 "$WORK/endpoints.json"

say "Starting the box behind a DROP-inbound stateful firewall…"
# The firewall is installed BEFORE reachbox starts, so there is never a window
# in which the box is inbound-reachable.
docker run -d --name "$BOX" --network "$NET" --cap-add NET_ADMIN \
  --add-host "nat.test:$RELAY_IP" \
  -v "$WORK:/cfg:ro" \
  -e REACHBOX_LABEL=box1 \
  -e VULOS_RELAY_ENDPOINTS_FILE=/cfg/endpoints.json \
  -e REACHBOX_LISTEN="0.0.0.0:$BOX_DIRECT_PORT" \
  alpine:3 sh -c "
    set -e
    # ca-certificates FIRST, with alpine's own default trust intact. Setting
    # SSL_CERT_FILE container-wide instead REPLACES the trust store, which
    # breaks apk's own TLS before it can install anything — measured, that is
    # exactly how this failed the first time. Installing the CA into the
    # system store is also what a real deployment does.
    apk add -q iptables ca-certificates
    cp /cfg/tls/ca.crt /usr/local/share/ca-certificates/vulos-nat-smoke.crt
    update-ca-certificates 2>/dev/null
    iptables -P INPUT DROP
    iptables -A INPUT -i lo -j ACCEPT
    iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    echo FIREWALL-READY
    exec /cfg/reachbox
  " >/dev/null || die "could not start the box container"

BOX_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$BOX")
[ -n "$BOX_IP" ] || die "could not determine the box's container IP"

wait_log() { # wait_log <container> <pattern> <timeout_s>
  local deadline=$(( $(date +%s) + $3 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    docker logs "$1" 2>&1 | grep -qE "$2" && return 0
    sleep 0.3
  done
  return 1
}

wait_log "$BOX" 'FIREWALL-READY' 90 || { docker logs "$BOX" >&2 2>&1 || true; die "the box's firewall never came up"; }
ok "box firewall installed (INPUT DROP + ESTABLISHED/RELATED)"

# The firewall must be verifiably in place, not merely "the setup command ran".
fw=$(docker exec "$BOX" iptables -S INPUT 2>/dev/null || true)
check "box's INPUT policy is DROP" \
  "$(printf '%s' "$fw" | grep -q -- '-P INPUT DROP' && echo 0 || echo 1)"
# iptables -S NORMALISES the state list, printing it back as
# "RELATED,ESTABLISHED" regardless of the order it was given in. Matching the
# literal string that was typed into the rule would fail against a firewall
# that is in fact correct, so match the components.
check "box permits ESTABLISHED replies (so egress can still work)" \
  "$(printf '%s' "$fw" | grep -q -- '-m conntrack' && printf '%s' "$fw" | grep -q 'ESTABLISHED' && printf '%s' "$fw" | grep -q -- '-j ACCEPT' && echo 0 || echo 1)"

wait_log "$BOX" 'link .* -> up' 60 || { docker logs "$BOX" >&2 2>&1 || true; die "the box never brought its tunnel up"; }
ok "box tunnel is up"

echo ""
say "── Controls: is this box GENUINELY unreachable from outside? ─────────"

# CONTROL 3 first: the direct listener really would answer, from inside the
# box's own netns. Without this, "the relay could not reach it" proves only
# that nothing was listening.
inside=$(docker exec "$BOX" wget -q -T 5 -O- "http://127.0.0.1:$BOX_DIRECT_PORT/" 2>/dev/null || echo "")
check "the box's direct listener DOES answer from inside the box (${inside:-<nothing>})" \
  "$([ "$inside" = "reachbox:box1 path=/" ] && echo 0 || echo 1)"

# CONTROL 2: egress works, so a blocked inbound probe is not just "no network".
# busybox wget does NOT accept --ca-certificate (it errors "unrecognized
# option" and exits non-zero), which read as "egress is blocked" and would
# have discredited a working path. It uses the system trust store, which is
# where the CA was installed above.
egress=$(docker exec "$BOX" wget -q -T 5 -O- "https://nat.test:$RELAY_PORT/_vulos-reach/v1/health" 2>/dev/null || echo "")
check "the box CAN dial the relay outbound (egress works)" \
  "$([ -n "$egress" ] && echo 0 || echo 1)"

# CONTROL 1, the load-bearing negative: the relay cannot dial the box.
say "Probing relay -> box:$BOX_DIRECT_PORT directly (must be refused/timeout)…"
if docker exec "$RELAY" wget -q -T 6 -O- "http://$BOX_IP:$BOX_DIRECT_PORT/" >/dev/null 2>&1; then
  inbound_blocked=1
else
  inbound_blocked=0
fi
check "the relay CANNOT reach the box directly — the box is genuinely behind NAT" "$inbound_blocked"

# Same probe from the HOST, which is a third party to both containers.
if curl -s --max-time 6 "http://$BOX_IP:$BOX_DIRECT_PORT/" >/dev/null 2>&1; then
  host_blocked=1
else
  host_blocked=0
fi
check "the HOST cannot reach the box directly either" "$host_blocked"

echo ""
say "── The claim: reachable from outside anyway, through the relay ───────"

got=$(curl -s --max-time 8 "${CURL_CA[@]}" "https://box1.nat.test:$HOST_PORT/" || echo "")
check "host -> relay -> box serves the box (got: ${got:-<nothing>})" \
  "$([ "$got" = "reachbox:box1 path=/" ] && echo 0 || echo 1)"

got_path=$(curl -s --max-time 8 "${CURL_CA[@]}" "https://box1.nat.test:$HOST_PORT/deep/path" || echo "")
check "the request reaches the box's handler with its real path" \
  "$([ "$got_path" = "reachbox:box1 path=/deep/path" ] && echo 0 || echo 1)"

# An unknown name must NOT be served — otherwise "it answered" would not prove
# the BOX answered.
unknown=$(curl -s -o /dev/null -w '%{http_code}' --max-time 6 "${CURL_CA[@]}" "https://nosuchbox.nat.test:$HOST_PORT/" || echo 000)
check "an unregistered name is not served (got $unknown)" \
  "$([ "$unknown" = "404" ] && echo 0 || echo 1)"

echo ""
say "── Failover: the tunnel recovers after the relay restarts ────────────"
docker restart "$RELAY" >/dev/null 2>&1 || die "could not restart the relay"
# docker reassigns the ephemeral published port on restart, and the container
# IP can move too. Re-read both — polling the STALE port would have reported a
# failed recovery while the box had in fact already reconnected.
HOST_PORT=$(docker port "$RELAY" "$RELAY_PORT/tcp" | head -n1 | sed 's/.*://')
[ -n "$HOST_PORT" ] || die "could not re-read the relay's published port after restart"
CURL_CA=("--cacert" "$WORK/tls/ca.crt" "--resolve" "nat.test:$HOST_PORT:127.0.0.1" "--resolve" "box1.nat.test:$HOST_PORT:127.0.0.1" "--resolve" "nosuchbox.nat.test:$HOST_PORT:127.0.0.1")
NEW_RELAY_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$RELAY")
check "the relay kept its address across the restart (so --add-host stays valid)" \
  "$([ "$NEW_RELAY_IP" = "$RELAY_IP" ] && echo 0 || echo 1)"
recovered=1
deadline=$(( $(date +%s) + 90 ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  again=$(curl -s --max-time 5 "${CURL_CA[@]}" "https://box1.nat.test:$HOST_PORT/" 2>/dev/null || echo "")
  if [ "$again" = "reachbox:box1 path=/" ]; then recovered=0; break; fi
  sleep 1
done
check "the box re-establishes its tunnel after the relay restarts" "$recovered"
# And it must still be behind NAT afterwards — a recovery that worked because
# the firewall fell over would prove nothing.
if docker exec "$RELAY" wget -q -T 6 -O- "http://$BOX_IP:$BOX_DIRECT_PORT/" >/dev/null 2>&1; then
  still_blocked=1
else
  still_blocked=0
fi
check "the box is STILL unreachable directly after recovery" "$still_blocked"

echo ""
say "Checked ${ASSERTIONS} assertions."
MIN_ASSERTIONS=12
if [ "$ASSERTIONS" -lt "$MIN_ASSERTIONS" ]; then
  die "only ${ASSERTIONS} assertions ran, expected at least ${MIN_ASSERTIONS}"
fi

if [ "$FAILURES" -gt 0 ]; then
  printf "${c_r}✗ FAIL — %d/%d assertions failed${c_n}\n" "$FAILURES" "$ASSERTIONS" >&2
  echo "---- relay ----" >&2; docker logs --tail 40 "$RELAY" >&2 2>&1 || true
  echo "---- box ----"   >&2; docker logs --tail 40 "$BOX"   >&2 2>&1 || true
  exit 1
fi

cat <<'SUMMARY'

Verified here, against real processes in real network namespaces:
  - the box DROPS every new inbound connection (policy asserted, and probed
    from two independent vantage points: the relay and the host)
  - the box's direct listener would answer if it were reachable — so the
    blocked probes are blocked by the firewall, not by nothing listening
  - the box is nonetheless publicly reachable through the relay, with the
    correct path reaching its handler
  - an unregistered name is not served
  - the tunnel re-establishes after the relay restarts, and the box is still
    unreachable directly afterwards

TLS is REAL here: the relay terminates TLS in-process with a certificate the
box verifies through its system trust store, so the handshake, the SNI/apex
routing and the certificate check are all exercised. What is NOT real is the
CA — it is issued by this script, not a public authority.

Still NOT covered:
  - a PUBLICLY-trusted certificate, real public DNS, or a real internet path
    (nat.test is resolved by --add-host and curl --resolve)
  - CGNAT specifically (this models the reachability consequence — no inbound
    path — not a carrier's address translation)
  - an operated cloud relay, which remains a human exercise
  - multi-relay under NAT: this runs ONE relay. scripts/smoke-relay.sh is
    where the multi-relay properties are covered, on loopback.
SUMMARY
ok "PASS — ${ASSERTIONS}/${ASSERTIONS} assertions"
exit 0
