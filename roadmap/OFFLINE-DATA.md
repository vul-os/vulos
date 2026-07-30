<!-- no-broker-dep:allow-file: roadmap comparison table's 'Host coupling' cell names an Ephor endpoint
     only as an example of an optional https:// peer URL -- illustrative,
     no import, no default endpoint. -->

# Offline Data — the generic `@vulos/offline` layer (OFFLINE-DATA-01)

> **Status.** 🟢 Core built + unit-tested (`src/lib/offline/`). 🟢 First app adoption (Notes) wired **and executing** — Yjs vendored, importmap + static serving in place. Complements [OFFLINE-AUTH.md](OFFLINE-AUTH.md) (the OS auth *gate*) and [OFFLINE.md](OFFLINE.md) (the client-offline model). This is the *data* half: how an app keeps its own data offline.

**The principle (from the founder):** apps must be able to go offline **for themselves** (standalone, any host, no Vulos) **and with Vulos** (better safety when the OS is present), **safely even without Vulos.** So this layer is a **generic, OSS, host-agnostic** module an app vendors — never a hard dependency on the shell.

---

## Grounding: what diwan and flowstock already do

Both reference apps already embody this exact philosophy — the library generalizes them rather than inventing:

| | **diwan** (Office) | **flowstock** (inventory) |
|---|---|---|
| Model | Browser-CRDT (Yjs + hand-rolled) + server-authoritative baseline | Local **Go server** + SQLite + append-only **oplog** + HLC + version-vector |
| Offline | app-shell PWA + IndexedDB drafts + localStorage CRDT snapshots | the whole node runs locally; browser is a thin HTTP client |
| Host coupling | **none required** — optional `window.__VULOS_ENDPOINTS__`, `bootstrapOffline({onBoot, tierHint})`, same-origin fallback | **none required** — optional `FrameAncestors` knob; an Ephor endpoint is just an `https://` peer URL |
| Merge | CRDT **union-merge, never count-gated** (offline edits on both sides preserved) | LWW register (catalog) + add-only set (ledgers) |
| At rest | **plaintext**, relies on device encryption; SW refuses to cache doc bytes | **plaintext** SQLite, relies on device encryption |

**Two models, not one.** diwan is browser-CRDT; flowstock is local-server. They don't collapse into a single library. `@vulos/offline` targets the **browser-CRDT** case (diwan, Notes, whiteboards); the local-server case (flowstock) already *is* its own offline node and needs nothing from us.

**The shared, liftable philosophy** (identical in both): self-contained by default; *optional* host seams that light up under Vulos and degrade safely without it; CRDT convergence so offline never loses writes; content-blind sync.

**The one net-new thing:** both store their local cache **in plaintext**. The founder's "safe" ask makes at-rest encryption a first-class *option* — used when a key is available (Vulos `appKey`), honest plaintext + device-encryption otherwise.

---

## The library (`src/lib/offline/index.js`)

Dependency-light: `yjs` (peer) + WebCrypto only. **No Vulos imports** — the host is detected via a global, so the file is copy/vendor-able into any OSS app.

```js
import { persistYDoc, resolveOfflineKey, offlineStatus } from '@vulos/offline'

const key = await resolveOfflineKey()          // Vulos appKey if present+unlocked, else null
const p = await persistYDoc(ydoc, { name: `notes:${id}`, key })
await p.whenSynced                             // local state loaded into ydoc
// edits auto-persist (debounced); p.flush() / p.destroy() / p.clear()
```

- **`persistYDoc(ydoc, {name, key?})`** — offline-first local persistence for a Yjs doc. A single debounced snapshot (`Y.encodeStateAsUpdate`) per doc in IndexedDB. If `key` is present the snapshot is **AES-GCM sealed** at rest (fresh 12-byte IV per write); otherwise plaintext. It is the LOCAL half only — the app's existing transport (Notes' collab WebSocket, a relay, WebRTC) still syncs to the server/peers, and a Yjs doc converges on reconnect regardless, so local + remote compose cleanly.
- **`resolveOfflineKey(override?)`** — the key to seal the cache with: an explicit override, else the Vulos host's per-app key (`window.vulos.offline.appKey()`, OFFLINE-AUTH-01), else `null` (plaintext). Never throws.
- **`offlineStatus()`** — `{ host, unlocked, encryptedAtRest }` so the app can **tell the user which protection is active** and never overclaim.
- **`offlineHost()`** — the OS gate if a compatible host injected one, else `null`.

### Safety posture (honest, fail-safe not fail-open)

- **Standalone / no Vulos:** plaintext IndexedDB, protected by the device's own encryption — the *same* posture diwan/flowstock already ship, now explicit. Never described as more than it is (`offlineStatus().encryptedAtRest === false`).
- **Under Vulos:** the cache is sealed with the app's per-app `appKey` (non-extractable, HKDF-scoped to the app by the OS from the trusted frame identity — an app can only ever seal/open its own cache). Combine with `isUnlocked()` to gate cached UI.
- **Encryption absence never blocks the app.** Offline access must not depend on a host that may not be there. A missing/locked host → plaintext, app keeps working. An app that *requires* the encrypted cache checks `offlineStatus()` and shows its own gate — the library doesn't force one.
- A wrong/undecryptable key **fails closed for the data** (can't read the sealed cache) but **starts clean rather than throwing**, so a corrupt cache never bricks the app.

---

## Adoption

- **Notes** (Yjs already, `apps/notes/collab.js`) — first target: persist the collab `Y.Doc` locally via `persistYDoc` so edits survive a reload / dead zone and re-converge on reconnect. Under Vulos the cache is sealed with the Notes app key.
- Any Yjs app vendors the module and calls the same three functions. diwan could adopt it to replace its plaintext localStorage snapshots with the encrypted-optional provider.

---

## Not yet / follow-ups

- ✅ **Notes serve-infra fixed — the adoption now executes.** Investigation confirmed the gateway is a pure reverse-proxy to each app's own server (`gateway.go:30`), so an app must serve its own static assets — and neither Notes nor text-editor's `server.py` did (both 404'd `/vendor/*`). Fixed for Notes: (a) `server.py` now serves app static assets from `APP_DIR` behind an **extension allow-list + realpath containment guard** (`serve_static`); (b) `index.html` carries a same-origin importmap (`yjs` → `./vendor/yjs.js`); (c) `notes` is registered in `scripts/vendor-apps.mjs` (`APP_ENTRIES`), and a working `vendor/yjs.js` was generated (esbuild bundle of the installed yjs). collab.js + the vendored offline lib now resolve yjs and run. **Note:** the checked-in `vendor/yjs.js` is an interim single-file bundle of yjs@13.6.30; `node scripts/vendor-apps.mjs` regenerates it canonically (pinned 13.6.18, code-split) — `server.py` serves any resulting chunk files too. (`apps/text-editor` still has the same pre-existing `server.py` static gap — out of scope here, flagged for a separate fix.)
- **Cross-tab lost-update** — two tabs on the same note share one IndexedDB name and each debounce-writes a full snapshot; last writer wins with no `BroadcastChannel`/lock coordination. IndexedDB itself stays uncorrupted, but an offline divergent sibling tab's snapshot can be overwritten. Add tab coordination if it matters.
- **Key doesn't hot-swap mid-session** — `resolveOfflineKey()` is resolved once per `persistYDoc` call; if the host unlocks *after* start, the cache stays plaintext for that session (the downgrade guard still prevents overwriting a previously-sealed cache). Re-key path is deferred.
- **Debounce durability** — a hard close within the ~400ms debounce can lose the last edit from the *local* cache; `persistYDoc` now flushes best-effort on `pagehide`/`visibilitychange` and on `destroy()`, but an async write may not complete on a hard unload.
- **Incremental append-log** (diwan's `updateLog.js` `{load, append}` seam) instead of a full-snapshot rewrite — better for large docs. The snapshot model is correct and simple for v1.
- **Outbox for non-CRDT / server-authoritative apps** (queued writes with retry, per [OFFLINE.md](OFFLINE.md)) — Yjs apps get convergence for free; REST apps need the outbox. diwan's `draftStore.js` is the template.
- **Search-index-at-rest leak** — if an app builds a local search index over encrypted content, the index is plaintext next to the ciphertext (see [OFFLINE.md](OFFLINE.md#search)); decide per app.
- **The local-server model** (flowstock) is out of scope here — it is already its own offline node.
