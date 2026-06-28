# OS Distribution & Image Updates

How a Vulos machine *gets* and *keeps* its operating system. The OS is no longer flashed once and patched in place over SSH — it ships as **signed, immutable, versioned squashfs artifacts** in a **public** object-storage bucket, cached locally, verified, and run from local disk with **A/B slots** and **boot-counter auto-rollback**.

For the trust anchor + flashed seed see SEED-TRUST.md. For first-boot netboot/install see NETBOOT.md. For signing, dm-verity and key rotation see SIGNING.md. For the squashfs/overlay build see BAREMETAL-INIT.md. For multi-instance *data* sync (a separate bucket) see CLUSTER.md.

> **Goal.** Make OS updates atomic, verifiable, and reversible. Any machine pulls the OS it's supposed to run from a public bucket, proves it's authentic with a baked-in signing key (not access control), runs it off a local squashfs, and can always boot the last-known-good slot if a new release fails.
> **Non-goals.** Running the OS *live* off S3 (we source from S3, cache locally, run local). In-place package patching of a mutable rootfs. A bespoke update protocol — we borrow the well-trodden A/B + rollback model from RAUC / Mender / ostree / Android A/B / Flatcar rather than reinvent it.
> **Status.** ✅ SHIPPED. The squashfs+overlayfs build output (`build.sh --live`) is the artifact. A/B slot management, the public-bucket layout, the signed `stable.json` manifest, boot-counter auto-rollback, and the `osdist` service (which polls `stable.json`, downloads to the inactive slot, verifies the detached signature and dm-verity root hash, then flips the slot) are all implemented and wired. A self-hosted machine reads `os/stable.json` directly and is correctness-complete without a release-advisor service.

---

## The Artifact

The OS image is the existing `build.sh --live` output: a **squashfs** root with an **overlayfs** writable upper. It is immutable and content-addressed by its **dm-verity root hash** (see SIGNING.md). The same artifact boots a VM, a USB stick, and a bare-metal install — there is no per-machine image.

- `os-core.squashfs` — the read-only OS image.
- `os-core.squashfs.sig` — detached signature over the artifact (release key; see SIGNING.md).
- The dm-verity root hash + the signature + the **minimum-trusted-epoch** travel in the signed manifest, not in the image.

We reuse `build.sh --live` directly — the live-RAM session ("Try Vulos", see NETBOOT.md) and the installed-disk image are the *same* squashfs, mounted the same way.

---

## Public Bucket Layout

The OS bucket is **public read** for everyone. There is no per-device credential and no access control on reads — **security comes from signing, not from secrecy** (anyone may download the OS; only the holder of the release key can produce one that verifies). This is the same posture as a Linux distro mirror.

```
os/
├── stable.json              # signed manifest: latest version + roothash + sig + min-epoch
├── stable.json.sig          # signature over stable.json (release key)
├── v07/
│   ├── os-core.squashfs
│   └── os-core.squashfs.sig
├── v08/
│   ├── os-core.squashfs
│   └── os-core.squashfs.sig
└── ...
```

`stable.json` is the single entry point a device reads to learn the latest version:

```json
{
  "channel": "stable",
  "latest": "v08",
  "min_epoch": 3,
  "roothash": "<dm-verity root hash of v08/os-core.squashfs>",
  "size": 734003200,
  "released_at": "2026-05-20T09:00:00Z",
  "path": "os/v08/os-core.squashfs"
}
```

A device:
1. Fetches `os/stable.json` (+ `.sig`), verifies the signature against the baked trust anchor and that `min_epoch ≥` the highest epoch it has seen (see SIGNING.md — rollback/downgrade protection).
2. If `latest` differs from the running slot, downloads `os/v08/os-core.squashfs` to the inactive slot.
3. Verifies the dm-verity root hash matches the manifest and the detached `.sig` verifies.
4. Flips the active slot atomically and reboots (see A/B below).

The bucket **URL is soft / runtime config** — it can fail over to a mirror or a cloud-accelerated endpoint, because the **key**, not the URL, enforces trust. The **key stays hard-baked** in the seed (see SEED-TRUST.md). A poisoned mirror cannot serve a malicious image; it would fail signature + verity verification.

---

## A/B Slots, Local Cache, Atomic Flip

OS images live on a **local cache partition** in two slots. The OS is sourced from S3 but **run from the local squashfs** — never streamed live off the bucket.

```
cache partition
├── slot-a/  os-core.squashfs   (active: v07, last-known-good)
├── slot-b/  os-core.squashfs   (staged: v08, freshly downloaded + verified)
└── boot-state.json             # active slot, boot counter, last-good slot
```

**Update flow:**
1. Download the new image into the **inactive** slot (`slot-b`).
2. Verify dm-verity root hash + detached signature against `stable.json`.
3. Set `slot-b` as the **pending-active** slot and reset the **boot counter** to 0.
4. Reboot.

**Boot-counter auto-rollback:**
- The bootloader increments a persistent boot counter before handing off.
- vulos-init marks the boot **healthy** (resets the counter, promotes the slot to last-known-good) only after the desktop/services come up cleanly.
- If the counter exceeds the threshold (e.g. 3 failed boots) without a healthy mark, the bootloader **falls back to the last-known-good slot** automatically.

This is the RAUC/Mender/ostree/Android-A/B/Flatcar model — we **borrow it, not reinvent it**. The writable overlay (user data, `~/.vulos`) lives on its own partition and is **not** part of the A/B flip, so an OS rollback never touches user state.

---

## Control-Plane Boundary

The OSS side is correctness-complete on its own: a self-hosted machine just reads `os/stable.json` directly, and an offline machine on a self-hosted bucket updates fine. Any optional release-advisor service (telling a deployment which release it *should* be on, staged rollouts, canary channels) is developed in a separate (non-public) repository and is out of scope for this roadmap — it would be an accelerator/advisor only.

---

## Why Not …

- **Run live off S3.** Latency, offline, and integrity all suffer; a verified local squashfs is fast, offline-capable, and tamper-evident.
- **In-place apt upgrades.** Non-atomic, non-reversible, drift between machines. Immutable A/B images are atomic and identical fleet-wide.
- **Private bucket / per-device tokens.** Token management is a liability and buys nothing — a signed public artifact is already unforgeable. Public-read + signing is simpler and forkable (SEED-TRUST.md).
- **A homegrown delta protocol.** A/B + full-image swap + verity is simpler than block-delta schemes and already battle-tested upstream.
