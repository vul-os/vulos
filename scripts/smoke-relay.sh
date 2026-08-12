#!/usr/bin/env bash
# scripts/smoke-relay.sh — repeatable local harness for Vulos's built-in relay
#
# The relay (`vulos relay serve`, backend/services/reach/) was previously
# exercised BY HAND: two boxes through one relay, two boxes through two
# relays with four simultaneous tunnels, failover when a relay dies, a bad
# token rejected, a world-readable config refused, and one bad entry in a set
# refusing the whole set. None of that was repeatable — nothing in the test
# suite or scripts reproduced it, so a regression would be silent. This
# script reproduces all six, against REAL, separate OS processes:
#
#   - TWO real `vulos relay serve` relays (the actual CLI: real flag parsing,
#     real grants-file loading, real file-permission checks) — built from
#     backend/cmd/vulos, on two loopback ports.
#   - "Box" processes are backend/services/reach/cmd/reachbox, a ~100-line
#     stand-in that does exactly what backend/cmd/server/reachwire.go does
#     (reach.FromEnv() -> tunnel.NewAgent) and nothing else. The real box
#     binary (cmd/server) is the whole OS and is out of scope for this
#     change; reachbox exercises the identical box-side config/wiring surface
#     without it. See that file's doc comment for the full rationale.
#
# LOCAL ONLY. Everything binds 127.0.0.1. Nothing here touches Fly, deploys
# anything, or reaches a non-loopback address — the cloud relay is explicitly
# a separate, human-run exercise.
#
# What this does NOT cover (see the summary printed at the end too):
#   - TLS termination (-cert/-key): both relays run plain HTTP on loopback,
#     which is the documented, supported mode for exactly this kind of local
#     testing (VULOS_RELAY_ALLOW_INSECURE=1, loopback only).
#   - The direct-endpoint ownership probe (-direct-probe): unrelated to the
#     six hand-tested scenarios this script targets.
#   - The rendezvous discovery role: same.
#   - Anything involving a non-loopback network path, a real domain, or a
#     real TLS certificate — those require the cloud relay the owner wants to
#     test by hand.
#
# Usage:
#   scripts/smoke-relay.sh
#
# Requirements: go, curl, python3 (free port selection only). No docker, no
# QEMU, no network access beyond 127.0.0.1.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
BACKEND="$REPO/backend"

# ── colour helpers (matches scripts/smoke-liveusb.sh, baremetal-smoke.sh) ──
c_b='\033[0;34m'; c_g='\033[0;32m'; c_r='\033[0;31m'; c_y='\033[0;33m'; c_d='\033[2m'; c_n='\033[0m'
say()  { printf "${c_b}▸ %s${c_n}\n" "$*"; }
ok()   { printf "${c_g}✓ %s${c_n}\n" "$*"; }
warn() { printf "${c_y}! %s${c_n}\n" "$*"; }
die()  { printf "${c_r}✗ %s${c_n}\n" "$*" >&2; exit 1; }

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  grep '^#' "$0" | sed 's/^# \{0,1\}//'
  exit 0
fi

# ── tool guards ──────────────────────────────────────────────────────────────
command -v go     >/dev/null 2>&1 || die "go not found — install Go to run this harness"
command -v curl   >/dev/null 2>&1 || die "curl not found"
command -v python3 >/dev/null 2>&1 || die "python3 not found — used only to pick free loopback ports"

# ── scoreboard ───────────────────────────────────────────────────────────────
# check <description> — records one assertion. Every scenario section calls
# this at least once; PASS/FAIL is decided per-call, not by "the section threw
# no error", so a scenario that silently did nothing shows up as zero checks
# rather than as a pass.
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

# ── workdir + process bookkeeping ───────────────────────────────────────────
WORK="$(mktemp -d "${TMPDIR:-/tmp}/vulos-smoke-relay.XXXXXX")"
PIDS=()
spawn() { # spawn <logfile> <cmd...>
  local log="$1"; shift
  "$@" >"$log" 2>&1 &
  PIDS+=("$!")
}
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  for pid in "${PIDS[@]:-}"; do
    wait "$pid" 2>/dev/null || true
  done
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

pick_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

# ── bounded polling (no `timeout`/`gtimeout` on this platform) ─────────────
wait_http_ok() { # wait_http_ok <url> <timeout_s>
  local deadline=$(( $(date +%s) + $2 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    curl -fsS --max-time 2 "$1" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  return 1
}
wait_log() { # wait_log <logfile> <ERE-pattern> <timeout_s>
  local deadline=$(( $(date +%s) + $3 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    grep -qE "$2" "$1" 2>/dev/null && return 0
    sleep 0.2
  done
  return 1
}
wait_port_closed() { # wait_port_closed <url> <timeout_s>
  local deadline=$(( $(date +%s) + $2 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    curl -s --max-time 1 "$1" >/dev/null 2>&1 || return 0
    sleep 0.2
  done
  return 1
}
wait_count_ge() { # wait_count_ge <logfile> <ERE-pattern> <count> <timeout_s>
  local deadline=$(( $(date +%s) + $4 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    n=$(grep -cE "$2" "$1" 2>/dev/null || echo 0)
    [ "$n" -ge "$3" ] 2>/dev/null && return 0
    sleep 0.2
  done
  return 1
}

# distinct_links_up <logfile> — the set of DISTINCT relay labels this box has
# reported "up" for, one per line.
#
# This exists because counting matching log LINES is not the same claim.
# reachbox logs "link <relay-url> -> up" on every transition, so ONE relay that
# merely flapped (up -> backoff -> up) emits two "-> up" lines and satisfies a
# line-count of 2 — while the second relay never connected at all. That is the
# central multi-relay assertion of this whole script, and a line count would
# have let exactly the regression it targets through. Measured, not theorised:
# a synthetic flap log scores 2 by line count and 1 by distinct label.
distinct_links_up() {
  grep -oE 'link [^ ]+ -> up' "$1" 2>/dev/null | awk '{print $2}' | sort -u
}
wait_distinct_up() { # wait_distinct_up <logfile> <count> <timeout_s>
  local deadline=$(( $(date +%s) + $3 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    n=$(distinct_links_up "$1" | wc -l | tr -d ' ')
    [ "${n:-0}" -ge "$2" ] 2>/dev/null && return 0
    sleep 0.2
  done
  return 1
}
http_status() { curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$@" 2>/dev/null || echo 000; }

# ── 1. Build the real binaries ──────────────────────────────────────────────
say "Building vulos (relay CLI) and reachbox (box harness)…"
BIN_VULOS="$WORK/vulos"
BIN_BOX="$WORK/reachbox"
( cd "$BACKEND" && go build -o "$BIN_VULOS" ./cmd/vulos ) || die "go build ./cmd/vulos failed"
( cd "$BACKEND" && go build -o "$BIN_BOX" ./services/reach/cmd/reachbox ) || die "go build reachbox failed"
ok "built $BIN_VULOS and $BIN_BOX"

# ── 2. Ports + grants ────────────────────────────────────────────────────────
R1_PORT=$(pick_port); R1_ADMIN=$(pick_port)
R2_PORT=$(pick_port); R2_ADMIN=$(pick_port)

cat >"$WORK/grants1.json" <<EOF
[
  {"token":"r1-box1","names":["box1"]},
  {"token":"r1-box2","names":["box2"]},
  {"token":"r1-box4","names":["box4"]},
  {"token":"r1-box5","names":["box5"]}
]
EOF
cat >"$WORK/grants2.json" <<EOF
[
  {"token":"r2-box1","names":["box1"]},
  {"token":"r2-box2","names":["box2"]}
]
EOF
chmod 600 "$WORK/grants1.json" "$WORK/grants2.json"

say "Starting two real relay processes on loopback (r1:$R1_PORT, r2:$R2_PORT)…"
spawn "$WORK/relay1.log" "$BIN_VULOS" relay serve \
  -addr "127.0.0.1:$R1_PORT" -domain r1.test -grants-file "$WORK/grants1.json" \
  -admin-addr "127.0.0.1:$R1_ADMIN"
spawn "$WORK/relay2.log" "$BIN_VULOS" relay serve \
  -addr "127.0.0.1:$R2_PORT" -domain r2.test -grants-file "$WORK/grants2.json" \
  -admin-addr "127.0.0.1:$R2_ADMIN"

wait_http_ok "http://127.0.0.1:$R1_PORT/_vulos-reach/v1/health" 15 || { cat "$WORK/relay1.log" >&2; die "relay1 never became healthy"; }
wait_http_ok "http://127.0.0.1:$R2_PORT/_vulos-reach/v1/health" 15 || { cat "$WORK/relay2.log" >&2; die "relay2 never became healthy"; }
ok "both relays healthy"

# ── 3. Two boxes, each holding a tunnel to BOTH relays (4 tunnels total) ────
cat >"$WORK/endpoints-box1.json" <<EOF
[
  {"url":"http://127.0.0.1:$R1_PORT","name":"box1","token":"r1-box1"},
  {"url":"http://127.0.0.1:$R2_PORT","name":"box1","token":"r2-box1"}
]
EOF
cat >"$WORK/endpoints-box2.json" <<EOF
[
  {"url":"http://127.0.0.1:$R1_PORT","name":"box2","token":"r1-box2"},
  {"url":"http://127.0.0.1:$R2_PORT","name":"box2","token":"r2-box2"}
]
EOF
chmod 600 "$WORK/endpoints-box1.json" "$WORK/endpoints-box2.json"

say "Starting box1 and box2, each tunnelled to both relays…"
spawn "$WORK/box1.log" env REACHBOX_LABEL=box1 VULOS_RELAY_ALLOW_INSECURE=1 \
  VULOS_RELAY_ENDPOINTS_FILE="$WORK/endpoints-box1.json" "$BIN_BOX"
spawn "$WORK/box2.log" env REACHBOX_LABEL=box2 VULOS_RELAY_ALLOW_INSECURE=1 \
  VULOS_RELAY_ENDPOINTS_FILE="$WORK/endpoints-box2.json" "$BIN_BOX"

# Each box holds TWO links (one per relay); the two "-> up" lines arrive
# independently and in no guaranteed order, so count DISTINCT relay labels
# rather than pattern-matching both in one grep (and rather than counting
# lines — see distinct_links_up for why that difference matters).
for box in box1 box2; do
  wait_distinct_up "$WORK/$box.log" 2 20 \
    || { cat "$WORK/$box.log" >&2; die "$box never brought both links up"; }
done
ok "box1 and box2 each hold two live links"

# And they must be links to the TWO DIFFERENT relays, named explicitly. The
# wait above proves "two distinct labels"; this proves they are the two relays
# actually configured, so a box that somehow held two links to the SAME relay
# — or to a relay this script never started — cannot satisfy it.
for box in box1 box2; do
  got_links=$(distinct_links_up "$WORK/$box.log" | tr '\n' ' ')
  for want in "http://127.0.0.1:$R1_PORT" "http://127.0.0.1:$R2_PORT"; do
    check "$box holds a live link to $want" \
      "$(printf '%s' " $got_links" | grep -qF " $want " && echo 0 || echo 1)"
  done
done

curl_named() { # curl_named <port> <domain> <name>
  curl -s --max-time 3 -H "Host: $3.$2" "http://127.0.0.1:$1/"
}

echo ""
say "── Scenario: two boxes / one relay ──────────────────────────────────"
b1=$(curl_named "$R1_PORT" r1.test box1)
b2=$(curl_named "$R1_PORT" r1.test box2)
check "box1 reachable through relay1" "$([ "$b1" = "reachbox:box1 path=/" ] && echo 0 || echo 1)"
check "box2 reachable through relay1" "$([ "$b2" = "reachbox:box2 path=/" ] && echo 0 || echo 1)"
check "box1 and box2 are NOT the same response" "$([ "$b1" != "$b2" ] && echo 0 || echo 1)"

echo ""
say "── Scenario: two boxes / two relays, four simultaneous tunnels ──────"
r1b1=$(curl_named "$R1_PORT" r1.test box1)
r1b2=$(curl_named "$R1_PORT" r1.test box2)
r2b1=$(curl_named "$R2_PORT" r2.test box1)
r2b2=$(curl_named "$R2_PORT" r2.test box2)
check "relay1 -> box1" "$([ "$r1b1" = "reachbox:box1 path=/" ] && echo 0 || echo 1)"
check "relay1 -> box2" "$([ "$r1b2" = "reachbox:box2 path=/" ] && echo 0 || echo 1)"
check "relay2 -> box1" "$([ "$r2b1" = "reachbox:box1 path=/" ] && echo 0 || echo 1)"
check "relay2 -> box2" "$([ "$r2b2" = "reachbox:box2 path=/" ] && echo 0 || echo 1)"
n1=$(curl -s --max-time 3 "http://127.0.0.1:$R1_ADMIN/tunnels" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo -1)
n2=$(curl -s --max-time 3 "http://127.0.0.1:$R2_ADMIN/tunnels" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo -1)
check "relay1 admin roster shows exactly 2 live tunnels" "$([ "$n1" = "2" ] && echo 0 || echo 1)"
check "relay2 admin roster shows exactly 2 live tunnels" "$([ "$n2" = "2" ] && echo 0 || echo 1)"

echo ""
say "── Scenario: a bad token is rejected ─────────────────────────────────"
before=$(curl -s --max-time 3 "http://127.0.0.1:$R1_ADMIN/tunnels" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo -1)
status=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 \
  -H "Authorization: Bearer this-token-does-not-exist" \
  "http://127.0.0.1:$R1_PORT/_vulos-reach/v1/tunnel")
after=$(curl -s --max-time 3 "http://127.0.0.1:$R1_ADMIN/tunnels" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo -1)
check "unauthenticated tunnel upgrade returns 401 (got $status)" "$([ "$status" = "401" ] && echo 0 || echo 1)"
# VACUITY GUARD, and it is not hypothetical: both readings fall back to -1 when
# the admin endpoint is unreachable or the JSON will not parse, and "-1 = -1"
# satisfies the before/after comparison below without a relay having been asked
# anything at all. Pin the BEFORE reading to the real, known roster size (the
# two tunnels box1 and box2 hold on relay1) so the comparison can only be
# reached with genuine counts on both sides.
check "setup: relay1's roster read a real count before the bad token ($before)" \
  "$([ "$before" = "2" ] && echo 0 || echo 1)"
check "the rejected attempt allocated no session ($before -> $after)" "$([ "$before" = "$after" ] && echo 0 || echo 1)"

echo ""
say "── Scenario: a world-readable config is refused ──────────────────────"
# (a) relay side: the grants file.
cat >"$WORK/grants-bad-perm.json" <<'EOF'
[{"token":"whatever","names":["boxbadperm"]}]
EOF
chmod 644 "$WORK/grants-bad-perm.json"
set +e
BAD_PORT=$(pick_port)
"$BIN_VULOS" relay serve -addr "127.0.0.1:$BAD_PORT" -domain bad.test \
  -grants-file "$WORK/grants-bad-perm.json" >"$WORK/relay-badperm.log" 2>&1
rc=$?
set -e
check "relay refuses to start with a 0644 grants file (exit $rc)" "$([ "$rc" != "0" ] && echo 0 || echo 1)"
check "the refusal names the fix (chmod 600)" "$(grep -q 'chmod 600' "$WORK/relay-badperm.log" && echo 0 || echo 1)"
chmod 600 "$WORK/grants-bad-perm.json"
GOOD_PORT=$(pick_port)
spawn "$WORK/relay-goodperm.log" "$BIN_VULOS" relay serve \
  -addr "127.0.0.1:$GOOD_PORT" -domain bad.test -grants-file "$WORK/grants-bad-perm.json"
check "the SAME file at 0600 starts a working relay" \
  "$(wait_http_ok "http://127.0.0.1:$GOOD_PORT/_vulos-reach/v1/health" 10 && echo 0 || echo 1)"

# (b) box side: the endpoints file.
cat >"$WORK/endpoints-box5.json" <<EOF
[{"url":"http://127.0.0.1:$R1_PORT","name":"box5","token":"r1-box5"}]
EOF
chmod 644 "$WORK/endpoints-box5.json"
spawn "$WORK/box5-badperm.log" env REACHBOX_LABEL=box5 VULOS_RELAY_ALLOW_INSECURE=1 \
  VULOS_RELAY_ENDPOINTS_FILE="$WORK/endpoints-box5.json" "$BIN_BOX"
check "box process logs CONFIG REFUSED for a 0644 endpoints file" \
  "$(wait_log "$WORK/box5-badperm.log" 'CONFIG REFUSED.*chmod 600' 10 && echo 0 || echo 1)"
status=$(http_status -H "Host: box5.r1.test" "http://127.0.0.1:$R1_PORT/")
check "box5 has NO working tunnel while its config is refused (got $status)" "$([ "$status" = "404" ] && echo 0 || echo 1)"
# Same file, fixed permissions, fresh process — the box-side twin of the
# relay-side recovery check above.
chmod 600 "$WORK/endpoints-box5.json"
spawn "$WORK/box5-goodperm.log" env REACHBOX_LABEL=box5 VULOS_RELAY_ALLOW_INSECURE=1 \
  VULOS_RELAY_ENDPOINTS_FILE="$WORK/endpoints-box5.json" "$BIN_BOX"
check "the SAME file at 0600 lets box5 establish a tunnel" \
  "$(wait_log "$WORK/box5-goodperm.log" 'link .* -> up' 10 && echo 0 || echo 1)"

echo ""
say "── Scenario: one bad entry refuses the WHOLE set ─────────────────────"
# box4 is individually well-formed AND granted on relay1 — if a regression
# ever made a bad sibling entry get silently dropped instead of refusing the
# whole set, box4 would still come up here. It must NOT.
cat >"$WORK/endpoints-box4.json" <<EOF
[
  {"url":"http://127.0.0.1:$R1_PORT","name":"box4","token":"r1-box4"},
  {"url":"not-a-url","name":"box6","token":"not-a-real-token"}
]
EOF
chmod 600 "$WORK/endpoints-box4.json"
spawn "$WORK/box4.log" env REACHBOX_LABEL=box4 VULOS_RELAY_ALLOW_INSECURE=1 \
  VULOS_RELAY_ENDPOINTS_FILE="$WORK/endpoints-box4.json" "$BIN_BOX"
check "box process logs CONFIG REFUSED naming the offending entry" \
  "$(wait_log "$WORK/box4.log" 'CONFIG REFUSED.*endpoint 1' 10 && echo 0 || echo 1)"
check "box process reports 0 endpoints, not a partial 1" \
  "$(wait_log "$WORK/box4.log" 'endpoints: 0' 10 && echo 0 || echo 1)"
status=$(http_status -H "Host: box4.r1.test" "http://127.0.0.1:$R1_PORT/")
check "box4 (individually valid, but sibling to a bad entry) has NO tunnel (got $status)" "$([ "$status" = "404" ] && echo 0 || echo 1)"

echo ""
say "── Scenario: failover (one relay dies, the other keeps serving) ──────"
r1b1_before=$(curl_named "$R1_PORT" r1.test box1)
r2b1_before=$(curl_named "$R2_PORT" r2.test box1)
check "setup: both relays serving box1 before the kill" \
  "$([ "$r1b1_before" = "reachbox:box1 path=/" ] && [ "$r2b1_before" = "reachbox:box1 path=/" ] && echo 0 || echo 1)"

say "Killing relay1 (pid for :$R1_PORT) — simulating the process dying, not a graceful drain…"
relay1_pid=$(lsof -ti tcp:"$R1_PORT" -sTCP:LISTEN 2>/dev/null || true)
[ -n "$relay1_pid" ] || die "could not find relay1's pid to kill"
kill -9 $relay1_pid 2>/dev/null || true

check "relay1's port stops accepting connections" \
  "$(wait_port_closed "http://127.0.0.1:$R1_PORT/_vulos-reach/v1/health" 10 && echo 0 || echo 1)"

# relay2 must keep serving BOTH boxes throughout, uninterrupted by relay1's
# death — this is the whole point of "hold a tunnel to every relay at once".
still_up=0
loop_deadline=$(( $(date +%s) + 8 ))
while [ "$(date +%s)" -lt "$loop_deadline" ]; do
  r2b1_after=$(curl_named "$R2_PORT" r2.test box1)
  r2b2_after=$(curl_named "$R2_PORT" r2.test box2)
  if [ "$r2b1_after" != "reachbox:box1 path=/" ] || [ "$r2b2_after" != "reachbox:box2 path=/" ]; then
    still_up=1
    break
  fi
  sleep 0.3
done
check "relay2 keeps serving box1 and box2 through relay1's death" "$still_up"

# reachbox labels each link by the relay's base URL (see reachbox/main.go),
# so box1's link to the now-dead relay1 is "http://127.0.0.1:$R1_PORT" —
# it must leave "up" (backoff/connecting), while the relay2 link is untouched.
check "box1's log shows its relay1 link leaving 'up' (backoff/reconnect)" \
  "$(wait_log "$WORK/box1.log" "link http://127.0.0.1:$R1_PORT -> (backoff|connecting)" 10 && echo 0 || echo 1)"

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
say "Checked ${ASSERTIONS} assertions."

# COVERAGE ASSERTION: a wholesale parse/orchestration failure (e.g. every
# `spawn` silently no-op'd) must not read as "PASS — 0 checks, 0 failures".
#
# COUNTED, not guessed: a full green run scores exactly 29. Set to that exact
# number rather than a comfortable margin below it, so that DELETING a check
# fails the run instead of quietly shrinking coverage. Raise it deliberately
# when you add one.
MIN_ASSERTIONS=29
if [ "$ASSERTIONS" -lt "$MIN_ASSERTIONS" ]; then
  die "only ${ASSERTIONS} assertions ran, expected at least ${MIN_ASSERTIONS} — the harness itself likely broke before reaching most scenarios"
fi

cat <<'SUMMARY'

Covered automatically by this script:
  1. two boxes / one relay
  2. two boxes / two relays, four simultaneous tunnels
  3. failover (one relay killed mid-flight, the other keeps serving)
  4. a bad token is rejected before any session is allocated
  5. a world-readable config is refused (relay grants file AND box endpoints file)
  6. one bad entry in a set refuses the whole set, not a partial one

NOT covered here (need the cloud relay, by design — see the file header):
  - a real TLS certificate / real public DNS
  - the direct-endpoint ownership probe
  - the rendezvous discovery role
  - anything crossing a non-loopback network path
SUMMARY

if [ "$FAILURES" -gt 0 ]; then
  printf "${c_r}✗ FAIL — %d/%d assertions failed${c_n}\n" "$FAILURES" "$ASSERTIONS" >&2
  echo "" >&2
  echo "---- relay1.log ----" >&2; tail -n 30 "$WORK/relay1.log" >&2 2>/dev/null || true
  echo "---- relay2.log ----" >&2; tail -n 30 "$WORK/relay2.log" >&2 2>/dev/null || true
  echo "---- box1.log ----"   >&2; tail -n 30 "$WORK/box1.log"   >&2 2>/dev/null || true
  echo "---- box2.log ----"   >&2; tail -n 30 "$WORK/box2.log"   >&2 2>/dev/null || true
  exit 1
fi
ok "PASS — ${ASSERTIONS}/${ASSERTIONS} assertions"
exit 0
