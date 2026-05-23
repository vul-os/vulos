# Vulos OS – Reproducible Builds

This document explains how to build a verity-signed Vulos OS rootfs that is reproducible and verifiable against the published source.

---

## 1. Build Flags for Determinism

All Go binaries must be built with:

```sh
CGO_ENABLED=0 \
  GOFLAGS="-trimpath -buildvcs=false" \
  SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) \
  go build -ldflags="-s -w -extldflags=-static" ./cmd/server/
```

| Flag | Purpose |
|------|---------|
| `-trimpath` | Strips local file paths from the binary; makes the binary identical regardless of where the repo is checked out |
| `CGO_ENABLED=0` | Fully static binary; no system-library dependency |
| `-buildvcs=false` | Excludes VCS metadata (commit hash) from the binary; use explicit `-ldflags=-X main.version=$(git describe)` instead |
| `SOURCE_DATE_EPOCH` | Sets the embedded build timestamp to the last commit time, enabling reproducibility across build machines |
| `-s -w` | Strip debug info and DWARF; reduces size, does not affect reproducibility |

To verify two binaries are identical:
```sh
sha256sum build1/vulos build2/vulos
# Both lines must match
```

---

## 2. Building the SquashFS Rootfs

The Vulos rootfs is packaged as a read-only SquashFS image.

```sh
# 1. Build the Go binary (see §1 above)
# 2. Assemble the rootfs tree
make rootfs   # produces build/rootfs/

# 3. Build a deterministic SquashFS
SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) \
  mksquashfs build/rootfs/ build/vulos-rootfs.sqfs \
    -comp zstd \
    -noappend \
    -mkfs-time "$(git log -1 --format=%ct)"
```

**Determinism note**: `mksquashfs` is not reproducible by default. The `-mkfs-time` flag sets the image timestamp. File ordering must be controlled via a filelist (`-sort-file`). Refer to `scripts/build-squashfs.sh` for the canonical invocation.

---

## 3. Capturing the SquashFS Digest

After building:

```sh
sha256sum build/vulos-rootfs.sqfs > build/vulos-rootfs.sqfs.sha256
```

The manifest file `build/manifest.json` records:
```json
{
  "version": "v0.3.1",
  "git_commit": "abc1234",
  "source_date_epoch": 1748000000,
  "sqfs_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "build_date": "2026-05-23T00:00:00Z"
}
```

`scripts/gen-manifest.sh` generates this file as part of the CI build.

---

## 4. dm-verity Signing

The SquashFS image is integrity-protected using `dm-verity`:

```sh
# Create a verity hash tree alongside the image
veritysetup format build/vulos-rootfs.sqfs build/vulos-rootfs.verity \
  > build/vulos-rootfs.verity-params.txt

# Extract the root hash (embed in the bootloader / kernel cmdline)
ROOT_HASH=$(grep "Root hash:" build/vulos-rootfs.verity-params.txt | awk '{print $3}')
echo "${ROOT_HASH}" > build/vulos-rootfs.roothash
```

The root hash is embedded in the signed bootloader. At boot, the initramfs uses `veritysetup open` to map the image; any corruption causes an I/O error and aborts boot.

---

## 5. Third-Party Signer Key Handling

Vulos OS supports an optional third-party (partner) signing key for enterprise deployments.

**Key generation** (offline, by the signing authority):
```sh
openssl ecparam -name prime256v1 -genkey -noout -out signer.key
openssl req -new -x509 -key signer.key -out signer.crt -days 3650 \
  -subj "/CN=Vulos OS Release Signing Key/O=Vulos"
```

**Signing the manifest**:
```sh
cosign sign-blob --key signer.key build/manifest.json \
  > build/manifest.json.sig
```

**Verification** (by end users):
```sh
# 1. Download the release bundle: vulos-rootfs.sqfs, manifest.json, manifest.json.sig, signer.crt
# 2. Verify the manifest signature
cosign verify-blob --key signer.crt --signature manifest.json.sig manifest.json

# 3. Verify the SquashFS digest matches the manifest
EXPECTED=$(jq -r .sqfs_sha256 manifest.json)
ACTUAL=$(sha256sum vulos-rootfs.sqfs | awk '{print $1}')
[ "$EXPECTED" = "$ACTUAL" ] && echo "OK" || echo "MISMATCH"

# 4. Verify dm-verity root hash
ROOT_HASH=$(cat vulos-rootfs.roothash)
veritysetup verify vulos-rootfs.sqfs vulos-rootfs.verity "${ROOT_HASH}" \
  && echo "verity OK" || echo "verity FAILED"
```

**Key rotation**: documented in `docs/KMS.md` (vulos-cloud). For OSS builds, the signing key is held by the Vulos maintainers; see the GPG fingerprint in `SECURITY.md`.

---

## 6. Rebuilding to Verify

Anyone with the source at the tagged commit can reproduce the build:

```sh
git checkout v0.3.1
SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) make reproducible-build
sha256sum build/vulos-rootfs.sqfs
# Compare against the published manifest.json sqfs_sha256
```

The CI pipeline (`scripts/ci-reproducible.sh`) performs this check on every release tag and fails if the digest does not match the published manifest.
