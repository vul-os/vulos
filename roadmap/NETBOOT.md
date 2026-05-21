# Netboot & First Boot

How an arbitrary PC becomes a Vulos machine **without per-machine disk flashing**. Instead of `dd`-ing an image to every drive, a machine boots over the network (or from a tiny one-time stick), runs a live-RAM "Try Vulos" session, and **installs to local disk as an explicit action** — never a surprise wipe.

For the flashed seed + trust anchor see SEED-TRUST.md. For the public OS bucket + A/B updates see OS-DISTRIBUTION.md. For signing/TLS/verity see SIGNING.md. For the squashfs/`--live` build, installer backend/UI, and the window model see BAREMETAL-INIT.md. For the post-install setup wizard (identity, storage, join) see INIT.md.

> **Goal.** "Any PC, anywhere" reaches a running Vulos desktop with no pre-imaged disk: UEFI HTTP Boot from a URL, or a ~1 MB one-time iPXE stick — both chainload a **configurable boot URL** (default `boot.vulos.org`, a forkable project default) over HTTPS and pull kernel/initramfs/squashfs. Offer a live-RAM session first; **Install** writes the seed + first squashfs to disk only when the user chooses it.
> **Non-goals.** A diskless/PXE-forever deployment (we netboot **to install**, then steady-state boots local). Surprise-wiping disks. Depending on any external server for correctness — a self-hosted boot URL works identically.
> **Status.** Design. The `--live` squashfs path and the installer (BMINIT-12/13) exist; NETB-* add HTTP-Boot/iPXE chainloading, the netboot-to-install hook, and TLS/cert-pinning of the boot pipe. The boot URL is configurable; any server that serves the signed artifacts works, self-hosted or otherwise.

---

## Two Entry Paths

Both paths converge on the same chainload: fetch a signed kernel + initramfs + squashfs over HTTPS and boot it.

1. **UEFI HTTP Boot URL.** Modern firmware can boot directly from an HTTP(S) URL. Point it at `boot.vulos.org` (or a self-hosted equivalent) — no media at all.
2. **~1 MB one-time iPXE stick.** For firmware without HTTP Boot, a tiny iPXE image on a USB stick chainloads the same URL. The stick is used **once** to bootstrap; the installed machine never needs it again.

```
UEFI HTTP Boot ──┐
                 ├──► HTTPS chainload boot.vulos.org ──► kernel + initramfs + squashfs ──► live-RAM session
~1 MB iPXE stick ─┘
```

---

## Netboot-to-Install, Not Diskless

We do **not** run diskless forever. The netboot is a **bootstrap**:

- **First boot:** runs entirely from the network/RAM. If the user installs, vulos-init writes the **seed** (SEED-TRUST.md) + the **first OS squashfs** to local disk.
- **Steady state:** the machine boots **locally** from its cache partition and pulls OS **updates** from the public bucket (OS-DISTRIBUTION.md). The network is needed for updates, not for booting.

This collapses the old "flash every disk by hand" step into "boot once from a URL, click Install."

---

## "Try Vulos" — the Live-RAM Session (Ubuntu-style)

The live session reuses the **`--live`** squashfs path (BAREMETAL-INIT.md): boot the same OS image into RAM with a writable overlay, run it for real, and treat **Install as an explicit, separate action**.

- The user can browse, test hardware, and use apps before committing.
- **Install never happens implicitly** and never wipes a disk without confirmation — the installer (BMINIT-12/13) presents disks and requires an explicit choice. This mirrors the Ubuntu live-USB experience.
- After install, the post-install setup wizard runs (INIT.md): identity, storage, optional cluster join. See "Install-Time UX" below for the local-only vs control-plane account choice.

---

## Two-Layer Safety

Booting code fetched over the network needs two independent protections — **the pipe** and **the payload**:

| Layer | Protects | Mechanism |
|---|---|---|
| **TLS / cert-pin** | the transport (MITM, downgrade, impostor server) | iPXE TLS with a pinned cert / CA for `boot.vulos.org`; UEFI HTTPS Boot validates the server cert |
| **Code signing** | the payload (tampered or substituted images) | every fetched artifact is signature-verified against the baked trust anchor (SIGNING.md) before execution |

When no stick is present and the machine boots cold from firmware, the **Secure Boot shim** (in the seed / chainloaded) is the firmware-level anchor that starts the signed chain (SEED-TRUST.md). TLS stops a network attacker from impersonating the boot server; signing stops *anyone* — including a compromised CDN — from getting unsigned code executed. Neither layer alone is sufficient; together they cover transport and content.

---

## Install-Time UX

After the live session, the user makes **one** post-live choice (full step list lives in INIT.md):

- **Local-only.** Create a **local OS account**: username + full name + password; hostname is autofilled. No external relationship. Fully self-hosted.
- **Connect a control plane.** *Optionally* enroll with a control plane (email + password + 2FA) at a configurable URL, then *optionally* join an existing **data cluster**.

These are **two distinct credentials** and must stay separate:

| Credential | What it's for | Held where |
|---|---|---|
| **Local OS account** | logging into the machine | the machine |
| **Control-plane account** (email + pw + 2FA) | optional enrollment with a control plane | the control plane |

The "bucket you sync to" at install/join time is the **data bucket** (CLUSTER.md) — distinct from the public, read-only **OS bucket** (OS-DISTRIBUTION.md). Joining a data cluster requires the **cluster passphrase, which is held only locally** (the encryption-at-rest invariant in CLUSTER.md). A control plane can route you to a cluster but cannot decrypt it.

---

## Control-Plane Boundary

The OSS side defines the *client* behavior only: HTTP-Boot/iPXE chainload of a **configurable boot URL**, TLS pin, signature verify, and netboot-to-install. It works against any server that serves the signed artifacts, self-hosted or the project default. Cloud/control-plane features are developed in a separate (non-public) repository and are out of scope for this roadmap.


## Plain-HTTP safety model — signatures, not TLS

UEFI HTTP Boot on most consumer/laptop hardware is **plain HTTP only** (UEFI 2.7 HTTPS Boot is server-class). The netboot safety model is **signature verification at every step**, not TLS.

**Trust anchors (one of):**
- **UEFI Secure Boot** key in firmware DB (no-media path).
- **iPXE binary on the one-time stick** the user flashed (USB path).

**Signature chain (every step):**
1. Local trust anchor (firmware Secure Boot OR iPXE-on-stick).
2. iPXE fetches the `.ipxe` script over plain HTTP, runs `imgverify` against its embedded pubkey, fails closed on mismatch.
3. iPXE fetches kernel + initramfs over plain HTTP, `imgverify` each before exec.
4. initramfs verifies the squashfs root hash + detached `.sig` against the release cert (root-signed offline, SIGN-03).
5. dm-verity enforces every block on read at runtime.
6. Min-epoch counter (SIGN-04) blocks replay of old, validly-signed but vulnerable images.

**What an attacker without TLS can/can't do:**
- *Can*: observe download URLs (artifacts are public anyway), inject bytes (rejected by signature verification).
- *Cannot*: cause arbitrary code execution.

**TLS is opportunistic** — used by iPXE-on-stick where available, and by server-class UEFI HTTPS Boot — but is never required for safety.
