# App Manifest

The `app.json` that describes every installable app — its identity, command, ports, permissions, **visibility**, and **concurrency** posture. The manifest is the contract between an app and the OS; fields here are validated (and, for security-relevant fields, **signed**) in `backend/services/appnet/manifest.go`.

For visibility (private/local/public) and the topbar warning see AI.md § Public Apps. For the new concurrency posture see CONCURRENCY.md. For how apps reach the registry see APP-STORE.md. For per-profile namespacing of running apps see NETWORK.md.

> **Goal.** One declarative file that fully describes an app to the OS so launching, routing, sandboxing, replication, and exposure can all be driven from data — not code.
> **Non-goals.** A general package format. Encoding runtime state in the manifest (manifests are static; runtime state lives in the store).
> **Status.** Shipped + extending. `id/name/command/port/type/category/permissions/visibility` are implemented and validated today. The `concurrency` field is **new work** (CONC-01) and extends this same struct.

---

## Current Shape

`AppManifest` (`backend/services/appnet/manifest.go`) carries identity (`id`, `name`, `version`, icons), launch (`command`, `port`, `type`, `work_dir`, `env`, `deps`, `auto_start`, `singleton`), classification (`category`, `keywords`), provenance (`author`, `license`, `homepage`), and policy (`permissions`, `visibility`).

- **`permissions`** — validated against `ValidPermissions` (`network`, `filesystem`, `camera`, `microphone`, `bluetooth`, `usb`, `gpu`, `background`, `notifications`).
- **`category`** — validated against `ValidCategories`.
- **`visibility`** — `private | local | public`, default `private` (AI.md § Public Apps; AI-01..04).
- **`id` / naming** — validated by the shared `naming` rules (NET-03): no `--`, no leading/trailing `-`.

---

## New: `concurrency` (opt-in)

Adds a single field declaring how the app behaves when its profile is **live in multiple locations at once** (CONCURRENCY.md):

```json
{ "concurrency": "singleton" | "replicated" | "collaborative" }
```

| Value | Meaning | Infra behavior |
|---|---|---|
| **`singleton`** (default) | active-passive, one owner | infra-enforced **run-lease** (COORDINATION.md); fails over on holder loss |
| **`replicated`** | active-active | CRDT merge across live instances |
| **`collaborative`** | active-active + live co-edit | CRDT merge **+** presence/awareness channel on the peering/relay hot path |

Validation + signing rules:
- Empty/absent ⇒ defaults to **`singleton`** (the safe mode). Apps must **opt INTO** `replicated`/`collaborative`.
- Validated to one of the three values (mirrors how `visibility` is validated).
- **Signed with the manifest** — like the rest of the manifest, the concurrency declaration is integrity-protected so it can't be silently flipped to active-active post-publish (CONCURRENCY.md).

Note the existing boolean **`singleton`** field is about "only one *instance* on this machine"; the new **`concurrency`** field is about cross-instance/cross-location behavior. They are related but distinct — `concurrency: singleton` is the cluster-wide active-passive policy enforced by a run-lease, whereas the legacy `singleton` bool is a local launcher constraint. CONC-01 should reconcile the naming in the doc/manifest comments so the two aren't confused.
