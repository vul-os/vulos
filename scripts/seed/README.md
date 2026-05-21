# scripts/seed/ — Seed Assembly Helpers

Scripts that operate on the local seed image during `build.sh` assembly.
The seed is the small, irreducible thing that is physically flashed to a
device.  Everything else (the full OS, updates) is fetched and verified from a
public bucket; the seed is what makes that fetching trustworthy.

See `roadmap/SEED-TRUST.md` for the full design rationale.

---

## embed-anchor.sh

Installs the **trust-anchor public key** (signing public key) into the seed
rootfs and its initramfs so the early-boot verify path (VERITY-02) can
validate the fetched OS chain without any network dependency.

### Usage

```sh
# Called automatically by build.sh during rootfs assembly.
# Can also be called manually for testing:
scripts/seed/embed-anchor.sh <rootfs_dir>
```

### Key resolution order

| Priority | Source | Notes |
|----------|--------|-------|
| 1 | `$VULOS_TRUST_ANCHOR_PUBKEY` env var | Path to the production key file |
| 2 | `keys/trust-anchor.pub` in repo root | Dev/local key — build warns loudly |
| — | *(nothing found)* | **Build fails with a clear error** |

### In-image paths (VERITY-02 contract)

| Path | Location | When available |
|------|----------|----------------|
| `/etc/vulos/trust-anchor.pub` | rootfs | After `pivot_root` (runtime) |
| `/etc/vulos/trust-anchor.pub` | inside initramfs cpio | **Before** `pivot_root` (early boot) |

Both paths are identical by design.  The initramfs copy is what VERITY-02 uses
to verify the fetched OS chain before the real root is even mounted.

The key is installed **mode 0444** (read-only for all users).  It is a public
key; there is no secret to protect, but immutability matters — nothing in the
running system should modify it.

### Key format

Raw Ed25519 public key, Base64-encoded (RFC 4648), single line, 32 decoded
bytes.  This matches the format consumed by `ed25519.Verify` in
`backend/services/signing/` (see `backend/services/signing/FORMAT.md`).

### Immutability / re-flash semantics

The trust anchor is **hard-baked into both the rootfs and the initramfs** at
build time.  Once the image is flashed the key is frozen.

- **Rotating the trust anchor requires re-flashing the seed.**
- There is no runtime key-update path; that is by design (SEED-TRUST.md).
- A device flashed with key A will **never** trust images signed with key B,
  no matter what the bucket URL says.

This is what makes trust **independent of location**: a poisoned mirror,
redirected DNS, or MITM cannot serve a forged OS because the signature check
is anchored to the baked key, not the fetch URL.

### Dev key (local builds)

For local/CI builds without a production key, place a generated Ed25519
public key at `keys/trust-anchor.pub` in the repo root.  The build will
proceed but will emit a prominent `DEV KEY IN USE` warning — such images must
never be flashed to production devices.

To generate a dev key:

```sh
mkdir -p keys
openssl genpkey -algorithm ed25519 -out keys/trust-anchor.key
openssl pkey -in keys/trust-anchor.key -pubout -outform DER \
  | tail -c 32 | base64 > keys/trust-anchor.pub
```

`keys/trust-anchor.key` (the private key) must **never** be committed.
`keys/trust-anchor.pub` may be committed for CI convenience but should be
clearly labelled as a dev-only artifact.

### Forkability

Because `build.sh` requires an explicit key, a forker can:

1. Generate their own offline root key pair (SIGN-03).
2. Set `VULOS_TRUST_ANCHOR_PUBKEY` to their public key path.
3. Run `build.sh` — the resulting image trusts **only** their key.

Devices flashed with the fork's seed reject images signed by any other key.
The project is forkable at the key level with no changes to the codebase.

---

## Files in this directory

| File | Purpose |
|------|---------|
| `embed-anchor.sh` | Install trust-anchor key into rootfs + initramfs hook |
| `README.md` | This document |
