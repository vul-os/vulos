#!/usr/bin/env bash
# prove-launch.sh — the whole app-launch path, run for real.
#
# `ip netns` is Linux-only and needs CAP_SYS_ADMIN, so the launch path cannot
# execute on a developer Mac at all. This runs the Linux-only end-to-end tests
# (backend/cmd/server/activate_linux_test.go) inside a privileged Linux
# container against the checkout you are sitting in. Nothing is mocked: real
# namespaces, real iptables rules, real python processes running as nobody,
# real HTTP through the real gateway.
#
# Usage:  bash scripts/prove-launch.sh [-run TestName]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${LAUNCH_IMAGE:-golang:1.25}"
RUN_FILTER="${1:-}"
RUN_ARG="-run TestLaunchEndToEnd"
if [ -n "$RUN_FILTER" ]; then
  RUN_ARG="$RUN_FILTER ${2:-}"
fi

echo "== LAUNCH-01/PORTBIND-01 end to end =="
echo "image: ${IMAGE}"
echo "repo:  ${REPO_ROOT}"

# Pin the platform to the host's own architecture. Without this Docker pulls
# linux/amd64 on an arm64 Mac and runs the whole Go build under emulation, which
# turns a two-minute proof into something that never finishes.
HOST_ARCH="$(uname -m)"
case "$HOST_ARCH" in
  arm64|aarch64) PLATFORM=linux/arm64 ;;
  x86_64|amd64)  PLATFORM=linux/amd64 ;;
  *) echo "unknown host arch ${HOST_ARCH}"; exit 1 ;;
esac
echo "platform: ${PLATFORM}"

docker run --rm --privileged \
  --platform "${PLATFORM}" \
  -v "${REPO_ROOT}:/src" \
  -w /src/backend \
  -e VULOS_E2E_LAUNCH=1 \
  -e GOFLAGS=-mod=mod \
  "${IMAGE}" \
  bash -eu -c '
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq >/dev/null
    apt-get install -y -qq iproute2 iptables util-linux procps python3 >/dev/null
    echo "python3: $(python3 -V)"
    echo "-- go test ./cmd/server '"$RUN_ARG"' -count=1 -v --"
    go test ./cmd/server '"$RUN_ARG"' -count=1 -v -timeout 20m 2>&1 | grep -vE "^\[auth\]|^20[0-9][0-9]/" | tail -120
    exit ${PIPESTATUS[0]}
  '
