<!-- no-broker-dep:allow-file: handover doc states the mobile/offline track depends on NONE of the
     listed items, and reachability for the phone is just the existing
     Ephor an operator may have configured (an alternative to the built-in
     Vulos relay) -- states an ABSENCE of dependency, not a dependency. -->

# Handover — Vulos Mobile & Client-Offline workstream

You are picking up the **mobile + client-offline** track for Vulos. The core team is focused elsewhere
(the DMTAP spec and the envoir project — the gateway lives inside envoir) — **those are out of scope for you.** Your remit
is: make Vulos great on a phone, and make it work offline.

## Read first (source of truth — do not re-derive)

1. [`android/DECISIONS.md`](DECISIONS.md) — every settled call (MOB-01…07), what's ruled out, the security floor. **This governs.**
2. [`android/README.md`](README.md) — the model and the two tiers.
3. [`android/BUILD.md`](BUILD.md) — Kotlin/Gradle boilerplate for the APK tier (deferred, but ready).
4. [`roadmap/OFFLINE.md`](../roadmap/OFFLINE.md) — the offline model both tiers share.

## The model in one paragraph

The phone is a **thin client to the user's box, reached over Relay** — it is *not* a Vulos instance/box.
One web shell, two delivery tiers: **Tier 1 = installable PWA (ship this first, needs almost no new code);
Tier 2 = an APK bundling the same shell locally (deferred until a real wall is observed).** Everything that
can move to the box, moves; an irreducible native floor (banking, tap-to-pay, WhatsApp) stays on the phone,
and that's fine.

## Settled — do not relitigate (details in DECISIONS.md)

- **Kotlin**, not Java. The web shell stays React/JSX; Kotlin is a thin frame. (This is a recorded exception
  to the "React/JSX only / no separate app" invariant — see the note to add in `docs/decisions.md` below.)
- **PWA first.** Web Push already works via the box's own VAPID stack (`backend/internal/webpush/`). The APK
  is the *only* tier that breaks push (WebView has no Push API) → 3 push paths. That's why it's deferred.
- **No client-side crypto in v1.** Android FBE already encrypts at rest. Build storage with a *pluggable
  codec* so encryption can drop in later. (libsodium + XChaCha20 + envelope + AAD-bound-to-record if it ever lands.)
- **Launcher (`CATEGORY_HOME`) deferred.** One manifest line later. The app *can* prompt to become default
  HOME via `RoleManager.ROLE_HOME` — that's the "app chooses if it wants to be the launcher" the founder asked for.
- **Offline = cache, never the sole copy.** Server-authoritative + change cursor + outbox. **Not a CRDT**
  (would be a 6th hand-rolled sync engine). Apps declare offline scope in their manifest.
- **SMS yes / call-log yes / in-call UI no** — all v2. `tel:`/`sms:` handoff only in v1.
- **Desktop wrapper:** same pattern as mobile — a thin webview over the local instance. If you must pick a
  framework, **Wails (Go, fits the stack). NOT Tauri (Rust, violates the frozen no-Rust rule).** Most likely
  you need no framework at all.
- **Browser scope:** Android Chrome + Firefox. No Safari/iOS.
- **Ruled out permanently:** phone-as-instance, custom ROM as product, postmarketOS-style porting,
  Waydroid-in-the-product. LineageOS is an unsupported community recipe only.

## Security floor — do these regardless (highest priority correctness work)

- **Per-app origins** (`*.os.vulos.org`) so an XSS in one app can't read another's IndexedDB. Architecture, not vigilance.
- **Mail renderer sandbox** — lilmail already does `srcdoc` + iframe *without* `allow-scripts` ✅. **But it
  grants `allow-same-origin`** (`templates/partials/email-viewer.html`) — harmless today, but a full sandbox
  escape the day someone adds `allow-scripts`. Drop `allow-same-origin` and comment why it must never return.
- CSP without `unsafe-inline`/`unsafe-eval` + Trusted Types (Chrome).
- **Positioning honesty:** never say "E2E" or "we can't read your data" — the box holds plaintext by design.
  Say "encrypted on your device; your instance holds the plaintext so it can search and sync."

## Work plan — do in this order

1. **PWA installability + Web Push polish.** Manifest, standalone display, icon, install prompt, verify push
   end-to-end on Chrome + Firefox Android. Biggest win, reuses existing service workers + VAPID stack.
2. **`navigator.storage.persist()` on sign-in + degraded-state UX** (offline badge, disabled network actions,
   unsynced count). Foundation everything else assumes. Persist is called *after* first sign-in, not on load.
3. **Notes offline (read-write).** Smallest app that proves the outbox end-to-end.
4. **Extract `@vulos/offline`** from what Notes needed — the pluggable-codec IndexedDB wrapper + outbox +
   connectivity state. Do not design it up front.
5. **Per-app offline manifest field** (`offline`, `cacheBudgetMB`, `cachePolicy`) — add to `APP-MANIFEST.md`,
   enforce budget in the shell.
6. **APK tier** — only when a real PWA wall is observed (notification reliability, share targets, background).
   `BUILD.md` is ready. First gotcha: service-worker fetches bypass `WebViewClient` — wire `ServiceWorkerClientCompat`.
7. **v2: SMS + call-log** natively (`ROLE_SMS`, `READ_CALL_LOG`), bridged to a WebView UI. No in-call UI.

## Concrete pending deliverables (discussed, not yet written)

- **`docs/banking-sms-otp.md`** — SA banks that support SMS-OTP for *desktop-browser* transactions (so a SIM
  in a USB LTE modem on the box replaces the phone). Founder-provided starting list, **all marked
  UNVERIFIED / to-be-tested**: African Bank, Standard Bank, Absa (fallback), Nedbank (fallback when no app
  bound). Frame around the "SIM into box via USB LTE modem" path. Do NOT present as verified — test each.
- **Refactor `roadmap/MOBILE.md`** — it's stale: it describes the *ruled-out* Vulos-on-a-Linux-phone path
  (PinePhone battery, libcamera, pmOS NFC). **Split it:** keep the box-attached telephony half (ModemManager
  D-Bus SMS/voice/eSIM — marked shipped, this is the good SIM-on-box path); delete/archive the handset-hardware
  half. Also fix the broken `future/MOBILE.md` link in `roadmap/README.md`.
- **`docs/gsm-hardware.md`** — capability tiers for box telephony: SMS-only USB LTE dongles (easy, ModemManager),
  voice (needs PCM audio path or a GSM-to-SIP gateway), 2G modules (cheap but sunset). Vetted device list —
  **research and verify model numbers before writing them.**
- **`docs/decisions.md` entry** — record the Kotlin/APK exception to "React/JSX only / reaches any device
  without a separate app." It's an accepted, scoped exception; log it rather than leave it as drift.
- **Radio app (open product question)** — founder floated "maybe a radio too." Clarify what "radio" means
  (FM receiver? SDR?) before scoping. Not committed.

## Open verifications (none blocking; close before the dependent code)

1. UnifiedPush 3.0's RFC 8291 compatibility — decides whether the box side of APK push is ~free or real work.
2. Android WebView WebAuthn support — only matters if client-side crypto (MOB-04) reverses.
3. Argon2id params that won't OOM low-end Android — same.
4. Waydroid/WhatsApp ban reality + the GSM hardware list — only when writing those docs.

## Cross-cutting the founder flagged — laptop-as-non-serving-instance (shipped box-side)

A laptop (or any personal device) can **join as a cluster member that SYNCS data but does NOT
serve routed traffic** — it participates in the data layer, but Relay must not route to it, the cloud
control plane must not bill it as hosting or health-check it as an ingress, and it must not be advertised
in routing. In the roles model this is clean: it runs the `store`/sync role, not the ingress/`relay` role.

**This shipped box-side as NODE-CAP-01** (`backend/internal/multiinstance`, the `store_only` flag — see
[`roadmap/NODE-CAPABILITY.md`](../roadmap/NODE-CAPABILITY.md)): the box honors its own flag, excludes
store-only members from routing and fan-out, and learns a peer's flag on sync. **What's left is the CP
side** (persist + echo + exclude from routing/DNS/billing, plus the box→CP push of a local change) — a
contract for the core team, not this workstream. Treat "connected member, not an ingress target" as a
first-class node state.

## Explicitly OUT of scope for this workstream

DMTAP spec, envoir (the gateway lives inside it). If your work seems to need them, it's a sign you've drifted — the
mobile/offline track depends on none of them. Reachability for the phone is just the existing Ephor
(client→box, SNI-passthrough); box↔box mesh relay (libp2p Circuit Relay v2) is the core team's concern, not yours.
