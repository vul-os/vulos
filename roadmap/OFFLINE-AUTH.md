# Offline Auth — the OS gate for offline access (OFFLINE-AUTH-01)

> **Status.** 🟢 **v1 built (shell side), reviewed, unit-tested.** Implemented: `src/lib/offlineAuth.js` (gate + per-app keys + attempt-cap/wipe), the `onEnvelope` cache hook in `src/lib/masterKey.js`, enrollment in `src/auth/LoginScreen.jsx`, offline detection + session in `src/auth/AuthProvider.jsx`, `src/auth/OfflineLockScreen.jsx`, and the boot wiring in `src/App.jsx`. **Not yet done:** apps adopting the gate (needs an `AppBridge` message — see Known gaps), a user-facing "forget offline data" control, and cache-at-rest encryption (deferred per MOB-04). Builds on the shipped client-side master-key unwrap (`src/lib/masterKey.js`) and the server device-PIN (`backend/services/auth/devicepin.go`).

**The split (the whole point):** **each app decides *what* it caches offline** (per [OFFLINE.md](OFFLINE.md)'s manifest — `none`/`read`/`read-write`); **the OS owns *auth*.** An app never renders cached data until the OS says the offline session is unlocked. Most offline logic lives in the apps; the OS owns exactly one thing here — the gate.

---

## The problem

Today, offline access is **impossible by design**, and that's the gap. `LockScreen.jsx` verifies the PIN at `POST /api/auth/pin/validate` and **fails closed when the box is unreachable** ("never unlock here"). Correct for security, but it means a dead zone / plane / rebooting box = you can't even see your cached mail. We want safe offline access **without** softening that fail-closed posture into a client-side bypass.

The trap to avoid: a **UI-only** offline lock — check a PIN, then render plaintext IndexedDB — is **security theater**. Anyone with the unlocked device opens devtools or copies the origin's IndexedDB and reads everything, gate bypassed. An offline gate is only real if a wrong credential **cryptographically** yields nothing.

---

## The construction — reuse the master key, don't invent a credential

The client already does the exact thing we need. At online login, `src/lib/masterKey.js` fetches the **password-wrapped master-key envelope** and unwraps it **entirely client-side**: `PBKDF2-HMAC-SHA256(password, salt, 600k) → AES-256-GCM unwrap`, **fail-closed** (a wrong password fails the GCM auth tag → no key), then derives per-content keys via `HKDF-SHA256(masterKey, "vulos-content:<domain>:<id>")`.

Offline auth is that same flow, sourced from a local cache instead of the network:

1. **Enroll (online, once):** after a successful login, cache the **opaque password-wrapped envelope** in IndexedDB. Per copy this is no weaker than the server holding it — it's *designed* to be unwrapped client-side, and an extracted blob still faces 600k-iteration PBKDF2 + AES-GCM. But caching does change the shape of the attack: a transient, network-gated artifact becomes a **durable local copy on every device that ever opted in**, so it multiplies the number of independently-attackable copies and shifts the attack from "compromise the server" to "get local/forensic access to any one enrolled device." A reasonable trade for the feature, but not strict equivalence.
2. **Unlock (offline):** user enters their account password → unwrap the cached envelope with `unwrapMasterKeyWithPassword` → recover the master key into memory. **Wrong password → GCM tag fails → no master key → no access.** Cryptographically enforced, not a UI check.
3. **Gate:** the OS holds the unwrapped key for the offline session and exposes only a boolean + per-app derived keys (below). Apps render cached data only once unlocked.

Why the **password**, not the quick numeric PIN: the server PIN is a 4–8 digit convenience that the box rate-limits (5 tries → lockout). Offline there is no server to rate-limit, so a short PIN over an extractable local blob is brute-forceable. The password path (600k PBKDF2, real entropy) is the secure offline credential. A PIN-wrapped **local** shortcut is possible later (mirroring how `devicepin.go` wraps the session token) but only with attempt-lockout-plus-wipe (below), and it's out of v1.

---

## What the OS provides

A small `@vulos/offline-auth` surface — the gate and nothing app-specific:

```
offlineAuth.isUnlocked(): boolean               // has the offline session been unlocked this session?
offlineAuth.unlockOffline(password): Promise<bool> // unwrap cached envelope; throws (fail-closed) on wrong password
offlineAuth.lock(): void                         // drop the in-memory key (idle/lock/logout)
offlineAuth.appKey(appId): Promise<CryptoKey>    // HKDF(masterKey, "vulos-offline-app", appId), non-extractable
offlineAuth.wipe(): Promise<void>                // forget the credential + signal apps to drop caches
```

- The OS shows the **offline lock screen** when the box is unreachable *and* the session isn't unlocked, and **blocks all cached-app rendering** behind it.
- `appKey` derives a **per-app** key via HKDF so an XSS in Notes cannot read Mail's cache — the same per-origin isolation the [mobile security floor](../mobile/DECISIONS.md#security-floor--do-these-regardless-of-mobile) mandates, extended to offline key material. Keys are non-extractable `CryptoKey`s; the raw master key never leaves the OS module. The HKDF domain separation is cryptographically real (length-prefixed `info`), but the isolation *guarantee* has a **precondition for whoever wires this up**: `appId` is a public label ("mail", "notes"), not a secret — anyone who can call `appKey("mail")` gets Mail's key. So the OS (`AppBridge`) MUST pass `appId` from a **trusted binding** (the iframe's registered origin/identity), NEVER an `appId` string an app supplies over `postMessage`. Until that binding exists, `appKey` is an unwired seam, not a live isolation boundary.
- The in-memory key is **never persisted**. Lock/idle/logout drops it; unlocking again requires the password.

## What apps provide

Per [OFFLINE.md](OFFLINE.md): each app declares `offline: none | read | read-write` in its manifest and owns its own cache + outbox. An app:
- checks `offlineAuth.isUnlocked()` before rendering cached data (renders the OS-provided locked/blocked state otherwise);
- (when cache encryption lands — see below) uses `offlineAuth.appKey(appId)` as the codec key for its IndexedDB cache.

The OS does not know or care what an app caches. It only answers "is the user allowed to see cached data right now, and with which per-app key."

---

## Security properties & honest threat model

**Fail-closed, everywhere:**
- No cached envelope (never enrolled) → **no offline access**. Offline is opt-in, earned by a prior successful online login.
- Wrong password → GCM auth failure → no key → no access. There is no "valid=true" offline path.
- Box reachable → defer to the existing **server** auth; offline unlock is a bridge, never a replacement. On reconnect, re-validate the real session; the offline session grants **only** cached reads + queued writes, never elevated privilege.

**Brute-force resistance (offline, attacker has the device):**
- The credential is the account **password** behind **600k-iteration PBKDF2 + AES-GCM**, not a short PIN — this is the *real* resistance. An attacker who extracts the cached envelope can brute-force it fully offline; the password's entropy + PBKDF2 cost is what stands in the way, nothing else.
- A **local attempt counter** wipes the cached envelope after `MAX_OFFLINE_ATTEMPTS` (10) consecutive wrong unlocks. ⚠️ **Be honest about what this is:** unlike `devicepin.go`'s counter — enforced by a **server the attacker cannot write to** — this counter lives in the same IndexedDB the attacker controls. Anyone who can run JS in the origin (devtools console, a malicious extension) or touch IndexedDB directly can **reset or delete the counter between guesses**, defeating the wipe entirely. It is a **UI-flow speed bump against a casual attacker poking the on-screen form**, NOT a cryptographic containment, and it does **not** mirror the server PIN's security property — only its UX shape. The module does serialize concurrent `unlockOffline` calls (closing a parallel-undercount race), but that does not make the counter tamper-proof. The only real brute-force resistance is the PBKDF2-hardened password.
- An **iteration floor** (`MIN_PBKDF2_ITERS`) is enforced at both cache-time and unlock-time, so a MITM/box-compromise can't slip a downgraded (e.g. `iter=1`) envelope that would sit on disk and unwrap cheaply.
- On wipe, the module clears the credential and **dispatches a `vulos:offline-wipe` event**; app-cache clearing depends on apps (or `AppBridge`) consuming it — see Known gaps. The OS guarantees only that **no further offline unlock is possible** (credential gone); it does not yet guarantee every app's cached bytes are erased.

**What v1 protects vs. what it does NOT (state this plainly, never overclaim):**
- **Protects:** casual/shoulder-surfer access to an unlocked device; "box is down, let me read my mail" convenience; per-app isolation of offline key material.
- **At rest, v1 relies on device encryption** (Android FBE / OS disk encryption), consistent with [MOB-04](../mobile/DECISIONS.md#mob-04--no-client-side-encryption-in-v1): the cache is plaintext IndexedDB in v1, so the offline lock gates the **running session**, and FBE protects **at rest**. It does **not** defend a rooted/forensically-extracted device until cache encryption lands.
- **Never call this end-to-end encrypted.** Positioning stays: *"Encrypted on your device. Your instance holds the plaintext, because that is what lets it search and sync"* — plus, for offline: *"unlocking offline verifies your password locally; at rest your data is protected by your device's encryption."*

**Closing the at-rest gap (the MOB-04 reversal, now concrete):** when client-side cache encryption is warranted, the codec key is simply `offlineAuth.appKey(appId)` — HKDF from the already-unwrapped master key. This makes [OFFLINE.md](OFFLINE.md)'s "pluggable codec" and MOB-04's "libsodium later" concrete: the key source already exists and is per-app. Preconditions for whoever adopts it:
- **`appId` from a trusted binding, not the app** (see the isolation note above).
- **Fresh random 12-byte IV per encryption.** `appKey` returns a *stable, long-lived* key (same app ⇒ same key until wipe/re-enroll), so an AES-GCM `(key, IV)` pair must never repeat — reuse under a long-lived key is catastrophic. The seam does not enforce this; the adopting app must.
- The password's strength becomes load-bearing (encourage a passphrase), and the search-index-leak caveat in [OFFLINE.md](OFFLINE.md#search) must be handled (encrypt index blocks or accept the leak — decide, don't discover).

---

## v1 scope

**In:** cache the wrapped envelope on login; the OS offline lock screen (reusing `LockScreen.jsx`'s UX, swapping server-validate for local unwrap when offline); `offlineAuth` gate API; per-app HKDF keys; fail-closed + attempt-wipe; apps gate cached rendering on `isUnlocked()`.

**Deferred:** encrypting the cache with the app keys (MOB-04 holds until reviewed); a PIN-wrapped local shortcut; WebAuthn/passkey unlock (Firefox Android PRF lags — [mobile browser scope](../mobile/DECISIONS.md#browser-scope)); biometric unlock.

**Non-goals:** making the phone authoritative; a second auth system (this reuses the master-key crypto); offline for real-time apps (`none` by definition).

---

## Known gaps / follow-ups

Recorded so they aren't mistaken for "done":

- ✅ **Apps can reach the seam** (done). `src/core/AppBridge.js` exposes `vulos.offline.state`/`vulos.offline.appKey` verbs, and the injected app client (`backend/services/gateway/origin_bridge.go`) surfaces `window.vulos.offline.{isUnlocked,appKey}`. `appId` is the trusted frame identity, never app-supplied, so an app can only ever get its own non-extractable key. **Remaining:** an actual app (Notes) *using* it to gate/encrypt — the seam exists; adoption is per-app.
- ✅ **User-facing control** (done). Settings → Account & Security → **Offline Data** discloses that enrollment is implicit-on-login and offers "Forget offline data on this device" (`wipe()`). Enrollment is still implicit (now disclosed); making it explicit opt-in is optional.
- ⚠️ **`wipe()` clears the shell-owned app data** (`AppBridge.clearAllAppData()` on the wipe event) **and** the credential — but an app's **own-origin** IndexedDB/caches can only be cleared by the app itself (on a shell→app wipe broadcast, not yet defined). The OS guarantees the credential is gone; per-app own-origin bytes need app cooperation.
- **Cloud sign-in (`CloudSignIn.jsx`) never enrolls** — and the master-key envelope is *password*-wrapped, so a passwordless cloud flow can't produce one. Offline access is currently a password-login feature; document/resolve for cloud users.
- **The attempt counter is not tamper-proof** (see the honesty note above) — real containment is the PBKDF2 password; the counter is a casual-attacker speed bump.
- **Pre-existing (ORIGIN-01, surfaced by the seam review; not introduced here):** in **path-prefix** deployment mode the gateway derives the injected client's trusted `SHELL` postMessage target from the raw `Host` header (`proxy.go` / `gateway.go`), unlike subdomain mode which validates against `VULOS_DOMAIN`. Not browser-exploitable today (session cookies are host-scoped), but the "exact shell origin" invariant should be server-asserted in path-prefix mode too. Defense-in-depth backlog item.
- **Cosmetic (bridge hygiene):** an app that self-navigates its own frame to another app's path keeps the appId bound at mount — it only gives away access it already had (never crosses into another app's key). Worth a comment if the model is extended.

## Build order

1. **Cache the wrapped envelope** in IndexedDB after successful online login (thin, no unlock logic yet).
2. **`offlineAuth.unlock()`** — local unwrap via `masterKey.js`, in-memory key, `isUnlocked()`.
3. **Offline lock screen** — when box unreachable + not unlocked, show it; wire `LockScreen.jsx`'s offline branch to `unlock()` instead of failing closed.
4. **Attempt counter + wipe.**
5. **`appKey(appId)`** HKDF derivation (unused until cache encryption, but ships the seam).
6. Apps adopt `isUnlocked()` gating (starting with Notes, per OFFLINE.md's first target).
