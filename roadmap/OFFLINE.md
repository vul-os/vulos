# Client Offline

How the browser-side shell keeps working when the box is unreachable — dead zone, plane, capped data, box rebooting.

This is **client↔box**, not box↔box. For multi-instance cluster replication see [SYNC.md](SYNC.md); for the durable S3 model see [CLUSTER.md](CLUSTER.md). Nothing here is a CRDT.

**Who may see the cache is a separate concern:** each app decides *what* it caches (below); the **OS owns the auth gate** that decides whether cached data may be shown at all when the box is unreachable — see [OFFLINE-AUTH.md](OFFLINE-AUTH.md). Offline today fails closed (no access); OFFLINE-AUTH is how it opens safely.

> **Goal.** Parity with a normal offline Android phone, plus the things web does better than native: read your mail, notes, docs, contacts and cached files; queue the obvious writes; never show a broken screen.
> **Non-goals.** A sync engine. Conflict resolution. Offline for real-time apps (Meet, Messages live). Client-side encryption in v1 ([clients/android/DECISIONS.md § MOB-04](../clients/android/DECISIONS.md#mob-04--no-client-side-encryption-in-v1)). Making the phone authoritative — the box is the authority, always.
> **Status.** 📐 **DESIGN ONLY — no code written against this doc.** Service worker shell caching already exists (see [`../docs/SW-CACHE-VERSIONS.md`](../docs/SW-CACHE-VERSIONS.md)) and Web Push already works ([NOTIFICATIONS.md](NOTIFICATIONS.md)). The `@vulos/offline` package, the outbox, and the per-app manifest declaration below are **unbuilt**.

---

## The rule

**Nothing required to paint the first screen may cross the network.**

Everything else follows from this. A spinner on a surface the user cannot escape reads as a broken phone, not a slow app — and that is doubly true if the launcher role is ever enabled ([MOB-05](../clients/android/DECISIONS.md#mob-05--category_home-launcher-deferred)).

---

## Layering

| Layer | Lives | Offline behaviour |
|---|---|---|
| Shell — grid, dock, search UI, settings | Service worker precache (APK: bundled assets) | **Always** |
| Tile metadata — app names, icons, order | Local, refreshed opportunistically | **Always** |
| App shells — HTML/JS/CSS per app | Service worker cache, warmed on first open | After first open |
| App data | IndexedDB cache → box | Cached reads; writes queue |
| Native apps (APK only) | `LauncherApps` | Always |

---

## Storage

**IndexedDB is a cache. Never the sole copy of anything.**

That constraint fits the architecture — the box is the authority and local storage is a replica, so losing it costs a re-sync, not data. It also reflects reality: browsers evict origin data under storage pressure, users clear browsing data with one tap, and IndexedDB has a real history of corruption on abrupt shutdown.

- Access through a thin wrapper (`idb`), never the raw API.
- Call `navigator.storage.persist()` **after first successful sign-in**, not on first load — Chrome's grant heuristics favour engaged origins, so timing measurably affects the outcome.
- Bound the working set explicitly, per app. Unbounded caching is what makes apps slow, blows quota, and turns eviction from an annoyance into data loss.
- **Pluggable codec** at the storage boundary from day one, so encryption can drop in later without a rewrite ([MOB-04](../clients/android/DECISIONS.md#mob-04--no-client-side-encryption-in-v1)).

### The one genuinely fragile spot

**The outbox.** For as long as a write is queued, IndexedDB *is* the only copy of it. So: flush on the `online` event and on app open, keep the queue bounded, and make unsynced state visible in the UI ("3 items not yet synced"). Silent loss here is the failure that would actually damage trust.

No Background Sync branch — it is Chromium-only and unreliable where it exists. One manual queue, everywhere.

---

## Sync

**Server-authoritative with a change cursor.** The client pulls changes since its cursor; writes go through the outbox and the box wins.

Explicitly **not** a CRDT. [SYNC.md](SYNC.md)'s reality check already records the cost of assuming a CRDT engine that was never integrated, and the decentralization audit counts ~5 hand-rolled sync engines across the suite as an existing problem. A client cache does not need conflict resolution, and adding a sixth engine while trying to consolidate five would be a poor trade.

---

## Apps declare their own scope

Each app states what it supports; the shell enforces the budget and renders the honest state. Requires a new field in [APP-MANIFEST.md](APP-MANIFEST.md).

```json
{ "offline": "read-write", "cacheBudgetMB": 200, "cachePolicy": "recent-30d" }
```

`offline`: `none` | `read` | `read-write`. An app declaring `none` shows a clear offline state rather than half-working — which is strictly better than a screen that looks live and silently is not.

| App | Realistic scope |
|---|---|
| Notes, text-editor | `read-write` — full offline, small data, obvious first target |
| Mail (lilmail) | `read` + queued send |
| Calendar, contacts | `read` + queued local edits |
| Files | `read` — cached and user-pinned only |
| Gallery, PDF viewer, music, video | `read` — cached items |
| Maps | `read` — genuinely full offline via PMTiles, if [dira](https://github.com/vul-os) happens |
| Messages (live), Meet, streamed apps | `none` — needs a live peer by definition |

---

## Search

Search over your own data is the strongest reason to keep this on a phone at all — and it is dead offline if it queries the box.

**v1:** a small local index of recent items (last N mails, files, notes), results labelled as cached, full search when connected.

**Later:** SQLite-WASM over OPFS gives FTS5 for free, real transactional semantics, and one durable file instead of a scattered object store. Supported in both Chrome and Firefox on Android. Do not hand-roll an inverted index over IndexedDB — it is real work and you would throw it away.

⚠️ **If encryption ever lands (MOB-04), the search index leaks.** An FTS index over encrypted content is plaintext sitting next to the ciphertext. Either encrypt index blocks and search after decrypt in memory, or record the leak as accepted. Do not discover this after shipping.

---

## Browser support

**Android Chrome + Firefox.** The floor — service workers, IndexedDB, WebCrypto — is universal, so no browser needs excluding. Firefox costs Background Sync (not used anyway, see above) and likely WebAuthn PRF (irrelevant while MOB-04 holds). OPFS works in both.

Safari/iOS out of scope.

---

## Degraded state is a feature, not an error path

- A visible **offline badge**, never a spinner.
- Network-dependent actions **disabled**, not allowed to fail.
- Unsynced count surfaced, not hidden.
- Cached results **labelled** as cached.

Users accept that an offline phone does very little. What they do not accept is not knowing which state they are in.

---

## Not in v1

Background Sync · Periodic Background Sync · CRDT sync · client-side encryption · offline writes for anything beyond the obvious cases (compose mail, edit note). Start read-mostly. That already covers the realistic offline moments.
