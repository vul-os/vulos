# Vula OS — bare-metal image builder
#
# build.sh needs a Linux host (debootstrap/chroot) and cannot run on macOS.
# This is the reproducible builder the smoke harness runs the build inside:
# Go toolchain + Node + debootstrap + the loop-free image tools
# (mke2fs -d, mtools, parted) so --disk works without privileged mounts, plus
# cryptsetup-bin (veritysetup) so VERITY-01 can produce a real dm-verity root
# hash for the stable.json that --disk now signs (VERITY-02) — without it the
# build refuses to fabricate a bogus root hash and fails loudly instead.
#
# Built/cached automatically by scripts/baremetal-smoke.sh.
FROM golang:trixie

ENV DEBIAN_FRONTEND=noninteractive
# go.mod requires Go 1.25.x; auto-fetch the pinned toolchain if the base lags.
ENV GOTOOLCHAIN=auto

RUN apt-get update && apt-get install -y --no-install-recommends \
      debootstrap \
      e2fsprogs dosfstools mtools parted squashfs-tools \
      cryptsetup-bin \
      xz-utils zstd ca-certificates curl git \
 && rm -rf /var/lib/apt/lists/*

# Node from the official static tarball — deterministic, no NodeSource/Debian
# version skew (the frontend needs Node 22+; Debian trixie ships older).
ARG NODE_VERSION=22.12.0
RUN set -eux; \
    case "$(uname -m)" in \
      aarch64) na=arm64 ;; \
      x86_64)  na=x64   ;; \
      *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${na}.tar.xz" \
      | tar -xJ -C /usr/local --strip-components=1; \
    node --version; npm --version

WORKDIR /src
