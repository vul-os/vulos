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

## Relationship to Secure Boot

When a machine has no one-time install stick and boots cold, the **Secure Boot shim** in the seed is the firmware-level anchor (see NETBOOT.md's two-layer safety). The shim chains to our signed bootloader, which chains to the signed kernel/initramfs, which verifies the squashfs — an unbroken signature chain rooted in the baked key. The forker's shim is signed with the forker's chain.
