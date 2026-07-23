# Android-app compatibility strategy

**Goal:** be honest with users about which Android apps Vulos can and cannot run,
and pick the lightest, most box-shaped way to run the ones it can. This document
does **not** re-open [`MOB-01`](DECISIONS.md#mob-01--the-phone-is-a-thin-client-not-an-instance)
(the phone is a thin client, not an instance) or the "ruled out permanently" list
in [`HANDOVER.md`](HANDOVER.md) (phone-as-instance, custom ROM, Waydroid-in-the-product).
Those stand. This is specifically about **streaming a full Android environment
from the box**, the same way Vulos already streams a Linux desktop.

## 1. Container choice: redroid, not Waydroid

Both are "run Android on a Linux kernel without a phone," and both share the
**same underlying trick** — a container process using the *host's own Linux
kernel* (via the Android Container Kernel Module / `binder`+`ashmem` misc
devices) rather than a full guest kernel. That makes them the **lightest class
of Android compatibility that exists** for running arbitrary APKs — genuinely
lighter than a QEMU/KVM Android-x86 VM (separate kernel, slower graphics passthrough)
and *far* lighter than the emulator shipped with Android Studio (built for dev
loops, not runtime density). Nothing lighter runs arbitrary unmodified APKs;
anything lighter (Play back-compat shims, app-specific reverse-engineered
clients) is a per-app hack, not a general solution.

| | **redroid** | **Waydroid** |
|---|---|---|
| Shape | A container (Docker/LXC) running a headless Android that renders to a **virtual display** and expects a remote client (VNC/RTSP/WebRTC-ish) to attach | A container that renders straight to the **local Wayland compositor** as if it were a native window |
| Fits | **Vulos's model**: the box already renders everything (Linux desktop, games, DAW sessions — see [`STREAMING-OPTIMIZATIONS.md`](../roadmap/STREAMING-OPTIMIZATIONS.md)) and streams pixels to whatever client is watching. redroid is *headless by design* — it has no opinion about the display, which is exactly the seam Vulos's streaming stack already fills | A **local desktop** environment for someone sitting at the machine's own Wayland session — assumes there's a human at the box's own screen, which contradicts "box is headless, client is remote" |
| Verdict | **Recommended** — matches the box/relay/streaming architecture with zero new plumbing (same GPU-encode path, same input-bound WebRTC/VNC transport already built for desktop streaming) | Not recommended for Vulos: it's built for "Android apps on my Linux laptop," a different product than "Android apps on my sovereign server, reached from anywhere" |

**Recommendation: redroid, OPT-IN, not default.** Concretely:

- Ships as an **optional package/container**, off by default on every box image.
  It is not part of the base install described in [`INIT.md`](../roadmap/INIT.md).
- The user explicitly enables it (a settings toggle or `apt`/container install),
  the same posture as other heavyweight optional workloads (gaming, DAW —
  see [`GAMING.md`](../roadmap/GAMING.md), [`REALTIME-AUDIO-DAW.md`](../roadmap/REALTIME-AUDIO-DAW.md)).
- Reasons it must stay opt-in, not default:
  - It needs kernel support (`binder`/`ashmem`, ideally the ACK/redroid kernel
    or the right modules on a mainline kernel) — not guaranteed on every board
    Vulos targets (see [`DEVICE-PROFILES.md`](../roadmap/DEVICE-PROFILES.md), ARM variants).
  - It's a full Android userspace: meaningful disk (multi-GB image), RAM, and
    GPU-encode budget competing with the box's other streamed sessions.
  - Google Play Services / Play Store are **not bundled** (GApps is a separate,
    heavier, legally murkier add-on some redroid distributions offer) — sideloaded
    APKs or a microG-style shim are the realistic default, which most users
    won't want without opting in first.
  - It inherits every limitation in §2 below — turning it on does not mean
    "now everything runs," and defaulting it on would imply that promise.
- **Hardware-gated like telephony**: mirror [`backend/services/telephony`](../backend/services/telephony)'s
  pattern — an `IsAvailable()` check (container runtime present? kernel module
  loaded? redroid image pulled?) that degrades cleanly to "unavailable" in the
  UI rather than erroring, so a box without the redroid package installed just
  doesn't show the surface.

## 2. What CANNOT be done on Linux + web — the honest bucketed list

Besides the two everyone already expects (Snapchat's anti-screenshot/AR
pipeline, and banking apps), here is the full picture, bucketed by *why* it
fails — because the "why" determines whether redroid (or any container) helps
at all.

### ① Hardware-bound to the phone itself — no container helps, ever

These need a physical radio, secure element, or sensor that lives in the
handset. A redroid container on a box has none of them, and never will — this
is a hardware ceiling, not a software one.

- **Tap-to-pay / NFC payments** (Google Pay, Samsung Pay) — needs the phone's
  NFC controller + Secure Element.
- **Government ID / digital ID wallets** (mobile driver's licenses, EU Digital
  Identity Wallet apps) — hardware-backed key attestation tied to the specific
  device.
- **Car keys, hotel keys, digital keys** (UWB/NFC/BLE key sharing) — needs the
  phone's own radios physically near the lock.
- **Transit NFC** (tap-to-ride cards emulated in-phone) — same NFC/SE dependency.
- **RCS messaging** — carrier-provisioned to a specific device/SIM.
- **eSIM** — the profile is provisioned to a physical eUICC chip in the handset.
- **Full Apple ecosystem** — iMessage, FaceTime, Continuity/Handoff, Apple Pay:
  Apple's own silicon and closed protocol, not reachable from Android-on-Linux
  at all (this is an Android/iOS gap, not a container gap).
- **Android Auto** — pairs to a car's head unit over USB/Bluetooth from a real
  handset.
- **BLE wearables** (most fitness bands/watches) — paired to one physical
  Bluetooth radio's MAC/bonding state.
- **Real-camera AR** (Pokémon GO's AR mode, Snapchat lenses, ARCore apps needing
  depth/motion sensors) — needs a live camera + accelerometer/gyro feed a
  container can't fabricate meaningfully.

### ② Attestation-locked — a container usually *fails* the check, so it doesn't help

These run fine *technically* inside redroid, but the app (or a service it
depends on) checks **Play Integrity** (formerly SafetyNet) or an equivalent
attestation, and a containerized/rooted-looking environment typically fails
that check. Getting these to pass is an arms race against Google's own
anti-emulation detection, not a capability gap redroid closes.

- **HD/L1 DRM streaming** (Netflix/Disney+ etc. in HD — SD often still works
  without hardware DRM) — Widevine L1 requires a hardware-backed keybox tied
  to a certified device.
- **MDM / enterprise-managed apps** — explicitly check for a managed, attested
  device profile.
- **Some games with anti-cheat** — many mobile anti-cheat SDKs bundle
  integrity/emulator detection.
- **Pokémon GO** — combines ① (real GPS/camera) and ② (aggressive emulator +
  mock-location detection) — double-blocked.
- **Ride-hailing — Bolt, Uber, and similar** — Play-Integrity-gated *and*
  actively fingerprint the runtime for anything emulator-shaped. Critically,
  **they also detect mock/injected GPS as a first-class fraud signal**
  (drivers/riders spoofing location to game fares, zones, or safety checks is
  a known abuse pattern these platforms actively police). A location feed fed
  into a redroid container — even a well-built virtual-GNSS one like the one
  in [`roadmap/LOCATION.md`](../roadmap/LOCATION.md) — reads to their fraud
  models as exactly that signal. **Do not present redroid as a way to run
  ride-hailing apps.** Best case it's flagged and degraded; worst case it's an
  account ban. This is a deliberate, explicit callout, not a footnote.

**General rule for ②: redroid does not rescue attestation-locked apps.** If an
app's threat model already assumes "assume the user's device might be an
emulator, and refuse/degrade if so," a headless container built for that exact
purpose fails the same way an emulator does — not because Vulos did something
wrong, but because that's the check working as designed.

### ③ No web/Linux client, but not locked — the slice redroid actually unlocks

This is the honest target: apps with no argument against running headless,
just no one has built a Linux or web client for them, and they don't check
device attestation.

- **Regional/niche apps** with an Android client but no PWA/web equivalent
  (many South African, Southeast Asian, and Latin American retail/loyalty/
  government-services apps fall here).
- **IoT companion apps** (smart-home device setup/control apps that only ship
  Android/iOS clients, with no local API or web dashboard).

This is the bucket redroid is actually *for*. Everything else in this document
is scoping what it is **not** for, so that turning it on doesn't create a
false expectation.

### ④ Already fine on Linux/web — no Android needed at all

No container required; these already have a first-class Linux or web path,
several of which the OS ships or links to today (see
[`DEFAULT-WEB-APPS.md`](../roadmap/DEFAULT-WEB-APPS.md)):

- **WhatsApp** — Desktop (Electron) or web.
- **Telegram, Signal, Discord, Slack** — native Linux clients or full-featured web.
- **Spotify** — native Linux client.
- **Instagram, TikTok** — usable web surfaces (reduced feature set vs. native,
  but functional: browsing, DMs, posting on some).
- **Most streaming services at reduced DRM** (SD/Widevine L3) — browser
  playback works; only the HD/L1 tier is blocked (see ②).

## Summary table

| Bucket | Example | Container helps? | Why |
|---|---|---|---|
| ① Hardware-bound | NFC pay, gov ID, car keys, eSIM, iMessage, Android Auto, BLE wearables, real AR | **No** | Needs physical radio/sensor/SE in the handset |
| ② Attestation-locked | HD DRM, MDM, some games, Pokémon GO, Bolt/Uber | **No** (usually fails the check) | Play Integrity / anti-emulator detection; ride-hailing also flags mock GPS as fraud |
| ③ Unlocked, just no client | regional/niche apps, IoT companion apps | **Yes** | This is what redroid is for |
| ④ Already fine | WhatsApp, Telegram/Signal/Discord/Slack, Spotify, IG/TikTok web, SD streaming | N/A | No Android needed |

## Related

- [`mobile/DECISIONS.md`](DECISIONS.md) — MOB-01…07, the settled thin-client model this doc does not reopen
- [`mobile/HANDOVER.md`](HANDOVER.md) — "ruled out permanently" list (phone-as-instance, custom ROM, Waydroid-in-the-product)
- [`roadmap/LOCATION.md`](../roadmap/LOCATION.md) — the box-side virtual-GNSS feed a redroid session would consume, and its explicit ride-hailing fraud-signal warning
- [`roadmap/STREAMING-OPTIMIZATIONS.md`](../roadmap/STREAMING-OPTIMIZATIONS.md) — the GPU-encode/streaming pipeline redroid would reuse
- [`backend/services/telephony`](../backend/services/telephony) — the hardware-gating style (`IsAvailable()`, clean "unavailable" degrade) any redroid integration should mirror
