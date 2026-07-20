# Vulos OS – Reproducible Builds

> **Status: this describes an intended design, not an enforced guarantee.**
>
> Reproducibility is a security claim, and a reader is entitled to assume a
> document like this describes something machine-checked. It does not. Concretely:
>
> - `scripts/gen-manifest.sh`, `scripts/build-squashfs.sh`, `scripts/ci-reproducible.sh`,
>   `make rootfs` and `make reproducible-build` **do not exist**.
> - **No CI job verifies release-tag digests against a published manifest**, and no
>   `manifest.json` is ever emitted — nothing in `.github/workflows/` writes one.
> - The determinism flags in §1 are real and worth using. Everything downstream of
>   them is a procedure a human would have to run by hand, and nobody currently does.
>
> Treat the sections below as the specification to build against, not as a
> description of what happens today. Each section carries its own status note.

This document describes how a verity-signed Vulos OS rootfs *would* be built so
that it is reproducible and verifiable against the published source.

---

## 1. Build Flags for Determinism

All Go binaries must be built with:

```sh
cd backend   # the Go module root is backend/ — there is no root go.mod
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

> **Status:** the rootfs tree is assembled by `build.sh`, the real image
> builder. There is no `make rootfs` target and no `scripts/build-squashfs.sh`
> — the deterministic-SquashFS wrapper described below is a **specification of
> the intended canonical invocation, not a script that exists yet**. Run
> `build.sh --live` for the live-USB/SquashFS path that is implemented today.

```sh
# 1. Build the Go binary (see §1 above)
# 2. Assemble the rootfs tree — build.sh does the debootstrap and lays out
#    the tree under ./output/rootfs (use --live for the SquashFS image path)
sudo ./build.sh --live

# 3. Build a deterministic SquashFS
SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) \
  mksquashfs build/rootfs/ build/vulos-rootfs.sqfs \
    -comp zstd \
    -noappend \
    -mkfs-time "$(git log -1 --format=%ct)"
```

**Determinism note**: `mksquashfs` is not reproducible by default. The `-mkfs-time` flag sets the image timestamp. File ordering must be controlled via a filelist (`-sort-file`). Factoring this into a canonical `scripts/build-squashfs.sh` is still **outstanding work** — the invocation above is the specification for it; today the SquashFS is produced inline by `build.sh --live`.

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

> **Status:** `scripts/gen-manifest.sh` does not exist and no CI job emits this
> file — nothing in `.github/workflows/` writes a `manifest.json`. The schema
> above is the intended format; generating it (and publishing it alongside the
> release artefacts) is outstanding work, tracked with the rest of the gaps
> called out in §2 and §6.

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
SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) sudo ./build.sh --live
sha256sum output/vulos-rootfs.sqfs
# Compare against the published manifest.json sqfs_sha256
```

> **Status:** there is no `make reproducible-build` target and no
> `scripts/ci-reproducible.sh`; **no CI job currently re-builds a release tag
> and compares the digest against the published manifest.** Closing that gap —
> a one-command reproducible build plus the CI check that enforces it — is
> outstanding work, and until it lands the reproducibility claim in this
> document is a design intent that is verified by hand, not automatically.
