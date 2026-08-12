# Vulos OS — bare-metal image builder
#
# build.sh needs a Linux host (debootstrap/chroot) and cannot run on macOS.
# This is the reproducible builder the smoke harness runs the build inside:
# Go toolchain + Node + debootstrap + the loop-free image tools
# (mke2fs -d, mtools, parted) so --disk works without privileged mounts, plus
# cryptsetup-bin (veritysetup) so VERITY-01 can produce a real dm-verity root
# hash for the stable.json that --disk now signs (VERITY-02) — without it the
# build refuses to fabricate a bogus root hash and fails loudly instead.
# initramfs-tools-core is here for that harness's Phase 5, which has to answer
# "can the initramfs on the ESP actually activate dm-verity?" — i.e. does it
# carry veritysetup and the dm-verity module. A real initramfs is TWO
# concatenated archives (an uncompressed early one, then a gzip one), so a plain
# `cpio -t` reads only the first and a plain `gzip -dc` reads neither; both
# report "absent" for everything in the main archive, which would be a
# diagnostic that always says the same thing. unmkinitramfs understands the
# concatenation, so the answer is measured rather than assumed.
# systemd-boot (bootctl) is here for scripts/netboot-install-smoke.sh, which —
# unlike build.sh's own loop-free/mtools ESP assembly — runs the REAL netboot
# installer (backend/services/installer) against a real loop-mounted disk, and
# that installer shells out to the real bootctl.
#
# Built/cached automatically by scripts/baremetal-smoke.sh and
# scripts/netboot-install-smoke.sh.
FROM golang:trixie

ENV DEBIAN_FRONTEND=noninteractive
# go.mod requires Go 1.25.x; auto-fetch the pinned toolchain if the base lags.
ENV GOTOOLCHAIN=auto

RUN apt-get update && apt-get install -y --no-install-recommends \
      debootstrap \
      e2fsprogs dosfstools mtools parted squashfs-tools \
      cryptsetup-bin systemd-boot util-linux \
      initramfs-tools-core cpio \
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
