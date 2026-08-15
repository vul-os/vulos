#!/usr/bin/env bash
# prove-portbind.sh — PORTBIND-01, proven against a real kernel.
#
# The unit test (backend/services/appnet/portbind_test.go) pins that namespace
# setup EMITS the sysctl step. That is a check on the shape of the code. It
# cannot tell you whether the step changes anything, because `ip netns` is
# Linux-only and the development machine is a Mac.
#
# This script runs the actual sequence the launcher runs — `ip netns add`, then
# `setpriv --reuid=65534 --regid=65534 --clear-groups --no-new-privs` inside it,
# then bind(0.0.0.0, 80) — in a privileged Linux container, twice: once without
# the floor lowered (must FAIL) and once with it (must SUCCEED). If either
# outcome flips, the script exits non-zero.
#
# Requires Docker. Run:  bash scripts/prove-portbind.sh
set -euo pipefail

IMAGE="${PORTBIND_IMAGE:-python:3.13-slim}"
APP_PORT=80

echo "== PORTBIND-01: does an app running as nobody in a fresh netns bind port ${APP_PORT}? =="
echo "image: ${IMAGE}"

out=$(docker run --rm --privileged "${IMAGE}" sh -eu -c '
apt-get update -qq >/dev/null 2>&1
apt-get install -y -qq iproute2 util-linux procps >/dev/null 2>&1

APP_PORT='"${APP_PORT}"'
NS=vulos_probe

echo "HOST_FLOOR=$(cat /proc/sys/net/ipv4/ip_unprivileged_port_start)"

ip netns add "$NS"
ip netns exec "$NS" ip link set lo up
echo "FRESH_NS_FLOOR=$(ip netns exec "$NS" cat /proc/sys/net/ipv4/ip_unprivileged_port_start)"

# Exactly the launcher command shape (services/appnet/launcher.go).
bind_probe() {
  ip netns exec "$NS" setpriv --reuid=65534 --regid=65534 --clear-groups --no-new-privs \
    python3 -c "
import socket, sys
s = socket.socket()
try:
    s.bind((\"0.0.0.0\", ${APP_PORT}))
    print(\"OK\")
except Exception as e:
    print(\"FAIL:%s\" % type(e).__name__)
" 2>/dev/null || echo "FAIL:exec"
}

echo "BEFORE=$(bind_probe)"

# The step namespaceSteps() adds.
ip netns exec "$NS" sysctl -q -w "net.ipv4.ip_unprivileged_port_start=${APP_PORT}"
echo "AFTER_FLOOR=$(ip netns exec "$NS" cat /proc/sys/net/ipv4/ip_unprivileged_port_start)"
echo "AFTER=$(bind_probe)"

ip netns del "$NS"
')

echo "$out"

get() { echo "$out" | grep "^$1=" | cut -d= -f2-; }

fresh=$(get FRESH_NS_FLOOR)
before=$(get BEFORE)
after_floor=$(get AFTER_FLOOR)
after=$(get AFTER)

fail=0
if [ "$fresh" != "1024" ]; then
  echo "UNEXPECTED: a fresh netns reported floor '$fresh', not the kernel default 1024."
  echo "  The premise of this fix is that 'ip netns add' resets the floor. Investigate"
  echo "  before trusting either result below."
  fail=1
fi
if [ "$before" != "FAIL:PermissionError" ]; then
  echo "GATE FAILED: binding port ${APP_PORT} as nobody in a fresh netns gave '$before',"
  echo "  expected FAIL:PermissionError. Either the kernel changed or this probe no"
  echo "  longer reproduces the condition — the fix would then be unnecessary, and"
  echo "  saying so is the point of running this."
  fail=1
fi
if [ "$after_floor" != "$APP_PORT" ]; then
  echo "GATE FAILED: floor is '$after_floor' after the sysctl, expected ${APP_PORT}."
  fail=1
fi
if [ "$after" != "OK" ]; then
  echo "GATE FAILED: binding port ${APP_PORT} STILL fails ('$after') after lowering the"
  echo "  floor. The fix does not work; every bundled app remains unable to start."
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo
  echo "PORTBIND-01: NOT PROVEN"
  exit 1
fi

echo
echo "PORTBIND-01 PROVEN:"
echo "  fresh netns floor        = ${fresh}  (host was $(get HOST_FLOOR))"
echo "  bind ${APP_PORT} as nobody before = ${before}"
echo "  bind ${APP_PORT} as nobody after  = ${after}  (floor lowered to ${after_floor})"
