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
>
> **Two exceptions, which are real and implemented:** §4 (the dm-verity hash
> tree and root hash, produced by `build.sh`) and §5 (release signing, done by
> `backend/cmd/sign` with Ed25519 keys). Both sections were rewritten on
> 2026-08-10 because they had described tooling — `cosign`, an OpenSSL P-256
> `signer.crt`, a published GPG fingerprint — that this project has never used.

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

> **Status:** unlike §2, §3 and §6, this step **is implemented** — by `build.sh`,
> not by the hand-run commands this section used to show. The names below are
> the real ones; earlier revisions of this document invented
> `vulos-rootfs.sqfs` / `vulos-rootfs.verity`, which no build produces.

`build.sh`'s VERITY-01 block (`build.sh:1170`) runs `veritysetup format` over the
image and emits three files beside it in the output directory:

| File | What it is |
|------|------------|
| `os-core.squashfs` | the image |
| `os-core.hashtree` | the dm-verity Merkle tree |
| `os-core.roothash` | one line of hex — the verity root hash |

A detached `os-core.roothash.sig` is what would bind that root hash to the trust
anchor. It is produced by the offline signing step (§5), never by the build — the
release private key does not touch a build machine.

**No shipped build carries one today.** `build.sh` emits the three files above and
no `.sig`, and it compiles only `vulos-server` and `vulos-init`, so
`vulos-verify-sig` is source in this repository rather than a binary on a box.
The initramfs hook contains the check and takes its documented
"roothash signature not verified" branch instead. So dm-verity binds the image to
a root hash, and nothing yet binds that root hash to the release key: an attacker
who can substitute the squashfs *and* the roothash together is not stopped by
this layer. Stated here because the table above otherwise reads as a complete
chain. See THREAT-MODEL.md, which records the same gap as residual risk.

**The root hash is not embedded in the bootloader.** The installer's boot entry
(`writeSlotABootEntry`, `backend/services/installer/netboot_install.go:520`)
carries a kernel path and `vulos.live=0` — no `roothash=` and no verity
parameter. The initramfs instead reads `os-core.roothash` as a *file* found
beside the image (`scripts/initramfs/vulos-live:127-142`), verifies its signature
against the pinned anchor when a `.sig` and verifier are present, and then runs
`veritysetup open`.

For which boot paths actually reach that `veritysetup open` today — it is not all
of them — see
[ARCHITECTURE.md → OS distribution (bare metal)](ARCHITECTURE.md#os-distribution-bare-metal).

---

## 5. Signing

> **This section was wrong in every particular and has been replaced.** It
> described an ECDSA `prime256v1` key made with `openssl`, signatures made with
> `cosign`, a `signer.crt`, and out-of-band verification "against the GPG
> fingerprint published in `SECURITY.md`". None of that exists: `cosign` appears
> nowhere in this repository, no OpenSSL key is used for release signing, and
> `SECURITY.md:73` states plainly that no PGP key has been published. It also
> claimed "Vulos OS supports an optional third-party (partner) signing key for
> enterprise deployments" — there is no partner-key concept in the code.

The real signer is `backend/cmd/sign`, and the keys are **Ed25519**. Its
subcommands:

| Subcommand | What it does |
|------------|--------------|
| `gen-key` | Generate an Ed25519 keypair (used for both the offline root key and the online release key) |
| `issue-release-cert` | Offline root operation: the root key signs a release cert |
| `sign-image` | Sign an OS image payload with the release key |
| `sign-manifest` | Sign a `stable.json` manifest with the release key |
| `export-anchor` | Write a **root** public key out as `trust-anchor.pub` |
| `sign-registry` / `verify-registry` | Sign / verify `registry.json` against the anchor + release cert (CI runs the verify) |
| `publish-feed` / `verify-feed` | Append to and verify `registry-feed.json`'s hash chain |

The trust model is root-signs-intermediate: an offline root key issues a release
cert, the release key signs images and manifests, and revocation is a monotonic
`min_epoch` floor rather than a CRL. The full custody, rotation and revocation
procedure is [KEY-CEREMONY.md](KEY-CEREMONY.md) — that is the authoritative
document, not this one.

Note what is *signed*: `stable.json` and the image payload, not the
`manifest.json` of §3, which nothing emits.

The forks-and-derivatives path is `VULOS_REGISTRY_PUBKEY` (a direct Ed25519
verification key that bypasses the cert chain), documented in
[APPS.md](APPS.md#environment-quick-reference). That is the closest thing to a
"third-party signer" that exists, and it is for forks running their own bucket,
not for co-signing a Vulos release.

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
