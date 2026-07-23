# Vulos Mobile — Decisions

Settled 2026-07-22. Each entry records the call, the reasoning, and what would reverse it.

---

## MOB-01 — The phone is a thin client, not an instance

**Decided.** Vulos runs on the box; the phone renders it over Relay.

Android fights long-lived background processes (doze, process death, background execution limits), has no
stable inbound reachability, no usable inbound ports, and a battery. The box-model is that the box is the
authority — a phone cannot be one, and pure box-model billing assumes it is not.

*Reverses if:* Android gains reliable unmetered background service execution. It will not.

---

## MOB-02 — Two delivery tiers, one shell. PWA first

**Decided.** Tier 1 is an installable PWA; Tier 2 is an APK bundling the same shell locally.

The PWA needs **no new code**: service workers already exist (see `docs/SW-CACHE-VERSIONS.md`) and Web Push
already works via the box's own VAPID stack. The APK adds real value (locally-bundled JS, durable
app-private storage) but is the **only tier that breaks the existing push architecture** — see MOB-06.

Sequence: deploy → mobile-good PWA + Web Push → ~10 real users on mail → let them show which wall the APK
is needed to cross. Building a mobile client for a product nobody can sign up for optimises the wrong end.

*Reverses if:* real users hit a concrete PWA limit (notification reliability in practice, share targets,
background behaviour). Let the wall be observed.

---

## MOB-03 — Kotlin, not Java

**Decided.** Kotlin for all Android code.

Google's official Android language since 2019. Compose, coroutines, Credential Manager, and every modern
API are Kotlin-first. Java on Android is maintenance-mode.

⚠️ **This conflicts with a stated invariant.** `ROADMAP.md` says *"Stack is frozen: Go backend; React/JSX
only; no Rust"* and *"reaches any device … without a separate app."* Kotlin + an APK breaks both. That is
an accepted, scoped exception — the *shell* stays React/JSX; Kotlin is a ~300-line frame around it, plus
whatever native bridging Tier 2 needs. It must be recorded in `docs/decisions.md`, not left as drift.

---

## MOB-04 — No client-side encryption in v1

**Decided.** Cache in plaintext IndexedDB; build the storage layer with a **pluggable codec** so encryption
drops in later without a rewrite.

Android's file-based encryption already protects storage at rest on a locked, non-rooted device. The
marginal gain from app-layer crypto is narrow (root, forensic extraction, cloud backup). The cost is not:
a crypto subsystem that must be correct, external review before it holds real data, a permanent
format-migration burden, and — worst — an unlock prompt on a surface that must feel instant.

*If/when it lands:* libsodium.js (Argon2id + XChaCha20-Poly1305 — the 192-bit nonce avoids AES-GCM's 2³²
bookkeeping), envelope encryption (passphrase → KEK wraps a random master key), HKDF for per-app keys, and
**record identity bound into the AAD** — without it an attacker with storage access can swap ciphertexts
between records and the auth tag still validates.

*Reverses if:* desktop browsers, shared devices, or enterprise enter scope.

---

## MOB-05 — `CATEGORY_HOME` (launcher) deferred

**Decided.** Ship the APK as a normal app. Adding the launcher role later is one manifest line plus an
opt-in toggle.

If the launcher is slow, crashes, or white-flashes, the user's **phone** is broken, not your app. That is a
brutal first impression against unproven value. Cheap to defer, expensive to get wrong early.

*When it ships:* the home surface must paint from local cache and **never** touch the network. Pre-warm the
WebView in `Application.onCreate()`, and set the activity's `windowBackground` to a drawable resembling the
home screen so there is no white flash on cold start.

---

## MOB-06 — Push: the APK's real cost

**Decided.** Tier 1 uses the existing stack unchanged. Tier 2 needs a plan before a line is written.

Vulos already has a **sovereign** push implementation: `backend/internal/webpush/webpush.go` is a
from-scratch RFC 8291 / VAPID sender (PUSH-CELL-01). The box holds its own keypair and POSTs encrypted
payloads directly to whatever service the subscription endpoint names — for Chrome that *is* FCM, but as
dumb transport: no Firebase project, no Google credential. Outbound-only, so it works behind NAT. Vendors
route but cannot read. `services/notify/cpregister.go` adds CP send-on-behalf for scale-to-zero cells,
flag-gated so self-host boxes never contact a CP.

**WebView does not implement the Push API.** So Tier 2 implies three paths:

| Channel | Mechanism | Note |
|---------|-----------|------|
| PWA | Web Push / VAPID | Already built and tested |
| APK (F-Droid) | UnifiedPush | ntfy/NextPush distributors; niche but aligned |
| APK (Play) | FCM natively | Needs a Google **service-account credential on the box** — contradicts "no central dependency" |

**UnifiedPush needs both sides**, but the box side may be near-free: UP 3.0 reportedly adopted RFC
8030/8291/8292, so a UP endpoint should look like a Web Push subscription and the existing sender works
unchanged. ⚠️ **Unverified — confirm before relying on it.**

⚠️ **Gotcha:** the SSRF guard in `webpush.go` will block a self-hosted `ntfy` on a LAN address. That guard
is correct. Fix with an explicit opt-in allowlist for user-declared private endpoints — do **not** loosen
the guard, that quietly reopens an SSRF hole.

---

## MOB-07 — SMS yes, call log yes, in-call UI no

**Decided.** Deferred to v2, and scoped when it happens.

WebView can only *hand off*: `tel:` and `sms:` open the system apps with a prefilled draft (and even that
needs `shouldOverrideUrlLoading` in Kotlin — WebView won't handle those schemes by default). Reading or
receiving either is native-only.

- **SMS** — `RoleManager.ROLE_SMS`. Requires `SMS_DELIVER` + `WAP_PUSH_DELIVER` receivers, an
  `ACTION_SENDTO` activity, a `RESPOND_VIA_MESSAGE` service, and writing to the system SMS provider. Play's
  restricted-permission review is friction, not a wall — default-SMS-handler is an accepted justification.
  MMS is genuinely messy (carrier APNs, MMSC endpoints). **RCS is impossible** — Google controls it via Jibe.
- **Why it still fits this market:** in SA, WhatsApp owns conversation and SMS is a *utility* channel — OTPs,
  bank alerts, delivery notices. There is no RCS richness to lose, and having that searchable inside your
  instance next to your mail is real value.
- **Calls** — `ROLE_DIALER` + `InCallService`, native UI mandatory (audio focus, proximity blanking,
  lock-screen surface, sub-frame responsiveness). And **if you own the dialer, you own emergency calls.**
  Breaking 112/10111 is not a bug report.
- **The compromise:** `READ_CALL_LOG` alone (restricted, but declarable) puts call history inside Vulos
  without taking the in-call surface.

**Cleaner alternative:** SMS arriving on the **box's own SIM** via a USB modem sidesteps Play's restricted
permissions entirely — it is just data arriving at your box, rendered in a plain web app. `roadmap/MOBILE.md`
records this telephony service (ModemManager D-Bus) as already shipped. Costs a second number.

---

## Ruled out

Do not revisit without new information.

**Phone as a Vulos instance / box** — see MOB-01.

**Own mobile distro / postmarketOS-style port** — pmOS's cost is not the distro (Alpine + a UI is trivial),
it is **per-device porting**: every Android phone ships a downstream vendor kernel fork with closed GPU,
modem, camera and audio blobs, none mainlined. ~10 years in, pmOS has a handful of daily-driver devices out
of thousands. Halium/libhybris dodges it by reusing Android's blobs and takes on a permanent per-device
maintenance surface instead. The ask is not "build a distro", it is "become a device-support organisation
forever."

**Custom ROM as the product** — Project Treble/GSI means a LineageOS flavor *is* technically buildable
without per-device porting. But unlocking the bootloader fails **Play Integrity**, which breaks banking,
Wallet and tap-to-pay — precisely the apps that cannot move to the box. For most users flashing is a
*downgrade*. GrapheneOS partly escapes via Pixel bootloader relocking, but that is Pixel-only and hardware
attestation often still fails.
→ **Instead:** one APK with **runtime capability detection**, no build variants. LineageOS stays an
*unsupported community recipe* plus an F-Droid repo.

| Granted | Unlocks |
|---------|---------|
| Plain install | Baseline — app tiles, search, sync |
| Default HOME | Launcher surface, drawer, gestures |
| `ROLE_SMS` / `ROLE_DIALER` | Comms inside Vulos |
| System/privileged (Lineage only) | Background survival, silent install, signature perms |

Document the boundary explicitly: **Lineage is a community recipe, unsupported.** Soften that and you have
taken on device support through the back door.

**Waydroid/emulation shipped in the product** — Waydroid is not banned and anecdotally works (WhatsApp runs
on non-GMS Huawei devices, so absence of Play Services is not itself a trigger; registration from a
datacenter IP is the reported failure point). But it is unsupported, breakable by any Meta release, and the
thing at risk is the **user's** account. Keep it a documented power-user recipe, never a feature.
WhatsApp stays **inbound-only** per the DMTAP legacy-gateway spec; the primary stays on the phone the user
already owns, with WhatsApp Web as a companion inside Vulos — which is just a browser tab, and carries zero
risk.

---

## Security floor — do these regardless of mobile

- **Per-app origins.** Already have `*.os.vulos.org` with host-scoped cookies and audience tokens. Lean on
  it: an XSS in Notes must not read Mail's IndexedDB. Highest-leverage control available, because it is
  architecture rather than vigilance. Offline key derivation must respect the same boundary.
- **Sandboxed mail rendering.** ✅ lilmail already does this (`templates/partials/email-viewer.html`) —
  `srcdoc` in an iframe **without `allow-scripts`**, so mail JS cannot execute.
  ⚠️ It grants `allow-same-origin`. Harmless today, but `allow-scripts` + `allow-same-origin` together is a
  full sandbox escape — framed content can strip its own sandbox attribute. Drop `allow-same-origin` unless
  something needs it, and comment why it must never return alongside `allow-scripts`.
- **CSP** without `unsafe-inline` / `unsafe-eval`, plus **Trusted Types** (`require-trusted-types-for
  'script'`). Chrome-only, which is the target, and it removes DOM XSS sinks structurally.

**Positioning honesty:** never describe this as end-to-end encrypted or "we cannot read your data" — the box
holds plaintext by design. Say: *"Encrypted on your device. Your instance holds the plaintext, because that
is what lets it search and sync."* Overclaiming on browser crypto is what gets products taken apart publicly,
and it is always the marketing copy that does the damage.

---

## Browser scope

**Android Chrome + Firefox.** Safari/iOS out of scope.

Firefox is the constraining browser and costs exactly two things: **Background Sync** (Chromium-only — build
the manual outbox anyway, it is more reliable everywhere) and likely **WebAuthn PRF** (Firefox Android's
WebAuthn support lags badly; treat as unavailable until proven otherwise). Everything else — service
workers, IndexedDB, OPFS, Web Push, `navigator.storage.persist()` — works in both.

The APK's WebView is always Chromium regardless of the user's default browser, so Tier 2 is
Chrome-equivalent by construction.

---

## Open verifications

None blocking, all worth closing before the relevant code is written.

1. **UnifiedPush 3.0's RFC 8291 compatibility** — determines whether the box side of MOB-06 is free or real work
2. **Android WebView's WebAuthn support** — only matters if MOB-04 reverses
3. **Argon2id params that will not OOM low-end Android** — same
4. **Waydroid/WhatsApp ban reality, and a vetted GSM modem list** — only if those docs get written
