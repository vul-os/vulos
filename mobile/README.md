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
  BUILD.md       ← the boilerplate + the easiest build path
  settings.gradle.kts  build.gradle.kts  gradle.properties   ← Gradle project root
  app/
    build.gradle.kts   ← :app module (deps, syncShell task, INSTANCE_HOST)
    src/main/
      AndroidManifest.xml            LAUNCHER active; HOME (launcher) commented per MOB-05
      java/org/vulos/mobile/MainActivity.kt   the WebView frame (Kotlin)
      assets/shell/index.html        placeholder; the real shell is copied from vulos/dist/
      res/                           theme (dark window bg, no white flash), adaptive icon, strings
```

**The Tier-2 project now exists** — a real, conventional Kotlin/Gradle Android project built from `BUILD.md`. It is still **deferred for shipping** (Tier 1 PWA is the path, MOB-02); this is so Tier 2 is a build, not a research project, when a real PWA wall appears.

## Build

Kotlin ([MOB-03](DECISIONS.md#mob-03--kotlin-not-java)). From `mobile/`:

1. **Bundle the shell first:** run the web build so `vulos/dist/` exists — the Gradle `syncShell` task copies it into `app/src/main/assets/shell/` on every build (no drift between tiers). Without it, the app shows a "shell not bundled" placeholder.
2. **Open in Android Studio** (it provisions the Gradle wrapper + Android SDK automatically), **or** from the CLI run `gradle wrapper` once to generate `gradle/wrapper/gradle-wrapper.jar` (not committed — binary), then `./gradlew assembleDebug`.
3. The APK's WebView loads the **local** shell (`appassets.androidplatform.net/assets/shell/…`); content/data load from the paired instance over the network.

**What it does today:**
- Loads the bundled shell locally; wires `ServiceWorkerControllerCompat` so the SW's fetches resolve to bundled assets (offline); predictive back over SPA history; file chooser for uploads.
- **Enabled launcher** — the `HOME` filter is active (a selectable home app), with the MOB-05 safeguards (pre-warmed WebView in `VulosApplication`, dark window background, cache-painted home). See [TEL-01](DECISIONS.md#tel-01--embedded-sms--calling-launcher-on-2026-07-23).
- **Embedded SMS + calling** — send/read/receive SMS and place calls + read call history, natively, exposed to the shell through an **origin-gated bridge** (`window.vulosTelephony`, `WebViewCompat.addWebMessageListener` restricted to Vulos origins; main-frame-only; runtime permissions). The **system dialer owns the in-call UI** (emergency calls stay safe — no `ROLE_DIALER`).

**Shell-facing bridge** (`vulosTelephony.postMessage(JSON)`): `{action:"subscribe"}` (inbound-SMS pushes), `sms.send`/`sms.list`, `call.dial`, `calllog.list`, `perms`. Replies + inbound-SMS events arrive on the message channel. Only reachable from trusted origins.

**Deliberately not yet:** Web Push (WebView has no Push API — the reason the *push* story is deferred, [MOB-06](DECISIONS.md#mob-06--push-the-apks-real-cost)); camera/mic/geolocation (need manifest perms + runtime requests — denied for now); becoming the **default SMS handler** (`ROLE_SMS`) and MMS/RCS; client-side crypto ([MOB-04](DECISIONS.md#mob-04--no-client-side-encryption-in-v1)).

## Related

- [`roadmap/OFFLINE.md`](../roadmap/OFFLINE.md) — the offline model both tiers share
- [`roadmap/MOBILE.md`](../roadmap/MOBILE.md) — box-attached telephony (⚠️ pending refactor; currently
  describes the ruled-out Vulos-on-a-Linux-phone path)
- [`roadmap/NOTIFICATIONS.md`](../roadmap/NOTIFICATIONS.md) — the existing sovereign push stack
- [`backend/internal/webpush/webpush.go`](../backend/internal/webpush/webpush.go) — RFC 8291 / VAPID sender
