# Local Seed & Trust Anchor

The small, irreducible thing that is actually flashed to a machine — and the reason the whole image-distribution model is **forkable**. Everything else (the OS image, its updates) is fetched and verified; the seed is what makes that fetching trustworthy.

For the public OS bucket + A/B updates see OS-DISTRIBUTION.md. For dm-verity, the PKI, and key rotation see SIGNING.md. For netboot/first-boot see NETBOOT.md.

> **Goal.** Reduce the flashed-to-disk surface to the bare minimum that establishes *where* the OS comes from and *whether to trust it* — then let the OS itself stream in, verified, from a public bucket. Make the trust model **forkable**: anyone can rebuild the seed with their own key + bucket and run a fully independent Vulos.
> **Non-goals.** Baking the whole OS into the seed (that's what the bucket is for). Baking the bucket URL as immutable (it's soft config — the key is the anchor). A vendor lock-in where only we can sign.
> **Status.** Design. Seeds today are produced by `build.sh`; SEED-* tasks add the trust-anchor embedding, the forker rebuild path, and the soft-URL/hard-key split.

---

## What's in the Seed

The flashed local seed is **irreducible**:

| Component | Role | Mutability |
|---|---|---|
| Bootloader (systemd-boot + Secure Boot shim) | first code the firmware runs | flashed |
| Verify-capable initramfs | fetches + verifies + pivots into the OS squashfs | flashed |
| **Signing public key (trust anchor)** | the root key that authorizes everything | **hard-baked, immutable** |
| OS bucket URL | where to fetch `os/stable.json` + images | **soft / runtime config** |

The seed is just enough to answer two questions: **where does the OS live** (bucket URL) and **may I trust what I find there** (the baked public key). It then fetches, verifies, caches, and runs the OS per OS-DISTRIBUTION.md.

---

## Soft URL, Hard Key

The split is deliberate:

- **The key is hard-baked and immutable.** It is the root of trust. Changing it is changing *who can sign your OS* — that must require re-flashing the seed.
- **The bucket URL is soft / runtime config.** It can be a mirror, a failover endpoint, or a cloud accelerator. Pointing a device at a different bucket is harmless: a malicious or stale bucket cannot serve an image that verifies against the baked key. **Trust is enforced by the key, not the location**, so the location is allowed to move.

This is why a poisoned mirror is a non-event (OS-DISTRIBUTION.md) and why failover/mirroring is free.

---

## Forkability (the OSS payoff)

Because **location + trust travel together in the seed**, forking Vulos is a rebuild, not a patch:

1. A forker generates **their own** offline root key (SIGNING.md).
2. They stand up **their own** public OS bucket and sign their own images.
3. They rebuild the seed with their key as the trust anchor and their bucket as the default URL.
4. **Re-flashing that seed re-establishes trust** end to end — devices flashed with the fork's seed trust the fork's bucket and reject ours, and vice versa.

No central authority, no allow-list we control, no shared signing infrastructure. The fork is fully independent the moment its seed is built. This is the operating-system analogue of "compile it yourself with your own keys" and is what keeps the project genuinely open.

---

## Fork Procedure (SEED-03)

> **TL;DR.** Generate your own root keypair, stand up your own OS bucket, build the seed with `VULOS_TRUST_ANCHOR_PUBKEY=... VULOS_OS_BUCKET_URL=... ./build.sh`, flash. Done — trust is fully independent of the upstream.

Everything the seed needs to establish trust is baked at build time.  Re-flashing the seed re-establishes trust end-to-end.  No central authority, no allow-list, no shared signing infrastructure.

### Step 1 — Generate your root keypair

The root key is the only secret in the system.  Generate it **offline** and keep the private half air-gapped.

```sh
# Using the vulos keygen tool (requires SIGN-03):
backend/cmd/sign gen-key --out keys/my-fork.key --pub keys/my-fork.pub

# Alternative: raw openssl (for bootstrapping before SIGN-03 lands):
mkdir -p keys
openssl genpkey -algorithm ed25519 -out keys/my-fork.key
openssl pkey -in keys/my-fork.key -pubout -outform DER \
  | tail -c 32 | base64 > keys/my-fork.pub
```

The public key (`my-fork.pub`) is a single-line Base64-encoded Ed25519 key (32 decoded bytes).  Keep the private key (`my-fork.key`) secret and offline — it is the root of trust for every OS image you ever sign.

### Step 2 — Stand up your OS bucket

The bucket must serve an update manifest at `os/stable.json` (see OS-DISTRIBUTION.md).  Any HTTPS host works: an S3 bucket, a Cloudflare R2 bucket, a static file server, etc.

```sh
# Example bucket root (accessible at https://os.my-fork.example.com):
#   os/stable.json       — update manifest (version + squashfs URL + signature)
#   os/vulos-amd64.squashfs  — OS image signed with your root key
```

Sign your images with the private key from Step 1.

### Step 3 — Build the seed

Pass both your public key and your bucket URL to `build.sh`.  They are baked together — the seed will only trust images signed by your key AND fetched from your bucket.

```sh
VULOS_TRUST_ANCHOR_PUBKEY=keys/my-fork.pub \
VULOS_OS_BUCKET_URL=https://os.my-fork.example.com \
  sudo ./build.sh
```

`build.sh` will fail loud if you provide a custom key without a bucket URL (or vice versa is unsafe — see the Invariant below).

What gets baked into the seed:

| File in seed | Content | Mutability |
|---|---|---|
| `/etc/vulos/trust-anchor.pub` | Your Ed25519 public key | Hard-baked, immutable |
| `/etc/vulos/os-bucket-url` | `https://os.my-fork.example.com` | Soft / runtime config |

Both files are also embedded into the initramfs so they are available before `pivot_root`.

### Step 4 — Flash + verify

```sh
# Flash the seed to the target device (adjust /dev/sdX):
dd if=output/vulos-amd64.tar.gz of=/dev/sdX bs=4M status=progress

# Or use the disk image:
dd if=output/vulos-amd64.img of=/dev/sdX bs=4M status=progress
```

After first boot the seed fetches `os/stable.json` from your bucket, verifies the squashfs signature against `/etc/vulos/trust-anchor.pub`, and pivots into your OS.  **Upstream-signed images are rejected** (wrong key).  **Upstream-bucket images are not fetched** (wrong URL).  The fork is fully independent.

### Re-flashing re-establishes trust

If a device is ever in doubt (key compromised, bucket moved, device handed to a new owner), re-flashing with a freshly built seed re-establishes trust end-to-end:

1. Regenerate the keypair (or reuse if the key is still clean).
2. Run `build.sh` with the new key + bucket.
3. Flash the new seed.

The old trust relationship is gone the moment the new seed is flashed.

### Invariant: key + bucket must match

A seed built with a fork key but pointing at the upstream bucket will boot, attempt to fetch the upstream manifest, fail to verify (wrong key), and never run.  `build.sh` prevents this configuration by requiring `VULOS_OS_BUCKET_URL` whenever `VULOS_TRUST_ANCHOR_PUBKEY` is set.  The two values are always written atomically in the same build — they cannot drift.

---

## Relationship to Secure Boot

When a machine has no one-time install stick and boots cold, the **Secure Boot shim** in the seed is the firmware-level anchor (see NETBOOT.md's two-layer safety). The shim chains to our signed bootloader, which chains to the signed kernel/initramfs, which verifies the squashfs — an unbroken signature chain rooted in the baked key. The forker's shim is signed with the forker's chain.
