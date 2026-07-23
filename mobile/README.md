# Vulos Mobile

The Android client for Vulos. **The phone is a thin client — your box stays the authority.**

> **Status.** 📐 Design settled, not yet built. Tier 1 (PWA) is the shipping path and needs no code in this
> directory. Tier 2 (APK) is scaffolded here and deliberately deferred — see [DECISIONS.md](DECISIONS.md)
> for why, and [BUILD.md](BUILD.md) for how.

---

## The model

Vulos runs on your box. The phone renders it, reached over Relay (direct-first, relay-fallback). One shell,
two delivery tiers — **not a choice between them**, the same code with different packaging:

| Tier | What it is | Buys you | Costs you |
|------|-----------|----------|-----------|
| **1 — PWA** | Installable web app in Chrome/Firefox Android | Offline via service worker + IndexedDB; **Web Push already works** (VAPID, `backend/internal/webpush/`); own icon; standalone window | Nothing. It is the code we already ship. |
| **2 — APK** | Same shell, assets bundled locally | No server-delivered JS; app-private storage that survives "clear browsing data"; later `ROLE_SMS` / call log | **Breaks push** — WebView has no Push API. Implies 3 push paths. Store review, signing, device fragmentation, forever. |

Tier 1 ships first. Tier 2 gets built when a specific wall is *observed*, not predicted.

## What the phone is not

Not a Vulos instance. Not a box. Not a custom ROM. Those were considered and ruled out — see
[DECISIONS.md](DECISIONS.md#ruled-out). A phone is a bad server (doze, process death, no inbound ports)
and shipping a ROM means becoming a device-support organisation forever.

## The irreducible native floor

Some apps will never run on your box, and designing around "the phone holds nothing" is chasing a purity
goal that is not reachable:

- **Banking apps** — Play Integrity + StrongBox hardware attestation; they refuse emulated environments
- **Gov ID / transit / tap-to-pay** — need the NFC secure element, physically not relocatable
- **WhatsApp** — vendor owns the client; registration requires a phone number and the mobile app

Design principle: **everything that can move, moves.** The residue stays native, and that is fine.

## Layout

```
mobile/
  README.md      ← you are here
  DECISIONS.md   ← what was settled, what was ruled out, and why
  BUILD.md       ← Kotlin/Gradle boilerplate and the easiest build path
```

No source yet — Tier 2 is deferred. `BUILD.md` carries working boilerplate for when it is not.

## Related

- [`roadmap/OFFLINE.md`](../roadmap/OFFLINE.md) — the offline model both tiers share
- [`roadmap/MOBILE.md`](../roadmap/MOBILE.md) — box-attached telephony (⚠️ pending refactor; currently
  describes the ruled-out Vulos-on-a-Linux-phone path)
- [`roadmap/NOTIFICATIONS.md`](../roadmap/NOTIFICATIONS.md) — the existing sovereign push stack
- [`backend/internal/webpush/webpush.go`](../backend/internal/webpush/webpush.go) — RFC 8291 / VAPID sender
