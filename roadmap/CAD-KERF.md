# CAD — kerf (browser-native)

A parametric CAD tool for the Vulos ecosystem. This is a **separate tool**, parallel to how `roadmap/REALTIME-AUDIO-DAW.md` describes a separate DAW tool — not a feature bolted onto an existing app.

> **Goal.** Make sovereign, Vulos-native CAD work *without* streaming a heavy native GUI. The trick: the interactive geometry kernel already runs in the browser as WASM, so interactive CAD costs ~zero server-side — and only the genuinely heavy, non-interactive solves are pushed to Vulos compute workers.
> **Non-goals.** Streaming a native CAD GUI (FreeCAD/Fusion) over WebRTC for interactive modelling. Running the interactive sketch/regen loop server-side. (Both are unnecessary — the kernel is client-side WASM.)
> **Status.** Design direction, captured from discussion (2026-05-26). Not implemented in the OS; consumed as a web app via `registry.json`.

---

## Context

CAD is reflexively classified as "heavy native" and shoved onto a streamed GPU desktop. That classification is wrong for the modern browser. **kerf** (kerf.sh) is a Vite web app whose entire geometry stack runs **client-side**:

- **JSCAD** code-CAD authoring,
- **OpenCascade** B-rep kernel compiled to **WASM**,
- **planegcs** constraint sketcher as **WASM**,
- a **WebGL** viewport.

Because the kernel is WASM in the host browser, the interactive modelling loop (sketch → constrain → extrude → orbit) happens on the user's own device at near-native speed — exactly the same insight as the DAW doc's "the real-time loop stays on the client."

---

## Core principle

**Don't stream CAD. Split it by latency, like kerf already does.**

The interactive path stays in the **host browser** (WASM kernel + WebGL). Only the genuinely heavy, non-interactive solves — where a few seconds of latency is irrelevant — go to Vulos.

| Side | Responsibility |
|---|---|
| **Client (interactive)** | Sketching, constraints, parametric regen, B-rep ops, orbit/zoom, the live viewport — all WASM in the host browser. |
| **Vulos (non-interactive)** | FEA / structural solves, large-assembly regen, mesh generation, render farm output, format conversion, project storage + collaboration sync. |

This maps cleanly onto the OS dispatch lanes (ROADMAP.md §0):

- **Interactive CAD → Web-app lane.** kerf opens in the host browser; geometry runs client-side; **zero `stream.Session`, zero server compute**.
- **Heavy CAD compute → Compute-worker lane.** A batch job (kerf already ships a `fly.worker.toml`); results return to the browser — **not** a streamed GUI session.

---

## Integration with Vulos

1. **Registry entry.** Add `kerf` to `registry.json` tagged `web` (host-browser lane). Default all CAD intents in the Open Router to kerf.
2. **Compute-worker jobs.** Heavy solves dispatch through the `compute_job` lane to a kerf worker (Fly Machine, `fly.worker.toml`-style), metered like any other job; the result blob lands back in the host browser. No interactive session is streamed.
3. **Storage + collaboration.** Projects live in the standard Vulos storage layer; project state (parameters, sketches, assembly tree) would sync via the **planned** sync stack (roadmap/SYNC.md's forward plan — a CRDT op algebra + version-vector reconciliation; cr-sqlite itself is not integrated, see SYNC.md/CLUSTER.md's reality checks) — the same local-first CRDT merge intended elsewhere. Geometry artifacts sync as files.
4. **No server-side GUI.** kerf never enters the CPU-stream or GPU-route lanes for interactive use. Those lanes remain for genuine native-only apps (e.g. Blender for mesh/sculpt/render, which kerf does **not** replace).

---

## Tiering

| Use | Path | Server cost |
|---|---|---|
| Interactive parametric modelling / sketching | kerf in the host browser (WASM kernel + WebGL) | ~zero |
| Heavy FEA solve / big-assembly regen / mesh gen | Vulos compute worker (`compute_job` lane) | per-job |
| GPU-bound mesh sculpt / photoreal render | Blender on the GPU route (BYO peer) — separate from kerf | paid / BYO |
| Project storage + multi-user collaboration | sync (planned CRDT spec, see SYNC.md) + storage, local-first | non-interactive |

---

## What kerf does *not* cover

- **Mesh sculpting, organic modelling, photoreal rendering** — that's Blender, which stays on the **GPU route (BYO)**, not kerf and not a web app.
- **Heavy simulation in the interactive loop** — FEA and large-assembly regen are compute-worker jobs, never the live viewport.

---

## Open questions

- **Build vs adopt/extend** kerf as the canonical Vulos CAD web app (kerf is the kerf-grade open browser CAD that the DAW doc notes is *missing* for audio).
- **Compute-worker protocol** for dispatching heavy solves and returning result blobs to the browser (shared with other `compute_job` workloads).
- **Collaboration granularity** — how much of the assembly tree / constraint graph merges cleanly via CRDT vs needs a lock.
- **Format conversion** (STEP/IGES/STL) — client-side WASM vs compute-worker.

---

*Early design captured from discussion (dated 2026-05-26). It describes a separate tool, parallel to `roadmap/REALTIME-AUDIO-DAW.md` for audio.*
