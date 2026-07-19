# Real-Time Audio / DAW

A Digital Audio Workstation tool for the Vulos ecosystem. This is a **separate tool**, parallel to how `kerf.sh` is a separate browser-native CAD tool — not a feature bolted onto an existing app.

> **Goal.** Make a sovereign, Vulos-native DAW workable despite audio's reputation as "local-only." The trick: don't try to solve "the DAW" — solve only the one piece that is genuinely latency-critical (live input monitoring) on the client, and push everything else to Vulos.
> **Non-goals.** Streaming a full DAW GUI for live tracking. Running monitoring plugins server-side in the real-time path. (Both are ruled out by physics — see below.)
> **Status.** Early design, captured from discussion (2026-05-26). Not implemented.

---

## Context

A DAW is normally classified `local_only` because audio is latency-sensitive. That classification is too coarse. The key insight is that **only one thing in a DAW is truly latency-critical: live monitoring while recording** — you play or sing and must hear yourself with a round-trip of roughly **<10ms** (<5ms is ideal). Above that the monitored sound drifts away from your physical playing and becomes unusable.

Everything else in a DAW tolerates latency just fine:

- **Mixing, editing, arranging, automation, mastering:** 100–200ms is totally OK. These are not real-time loops; you make a change and hear the result a fraction of a second later, which is imperceptible in workflow terms.
- **Playback of existing tracks:** just needs to be time-aligned, not instant. You can buffer ahead and align to a shared clock.

So the design does not stream "the DAW." It isolates **real-time input monitoring** and keeps only that on the client. Everything else is free to live on Vulos.

---

## Core principle

**Don't stream the DAW. Split it.**

The real-time audio path stays on the **client**. The client's own audio hardware — interface, drivers, MIDI — must handle the real-time loop, because you cannot beat the speed-of-light round-trip to a server. No amount of cleverness makes a remote machine close a sub-10ms monitoring loop over a network. Vulos handles everything that is **not** real-time.

This is the **same pattern as kerf**: the real-time interactive work happens browser-native / client-side, and heavy compute is offloaded to Vulos workers.

| Side | Responsibility |
|---|---|
| **Client (real-time)** | Audio I/O, live monitoring, MIDI input, the tracks being actively recorded. |
| **Vulos (non-real-time)** | Full project storage, large sample libraries, heavy offline plugin rendering, mixdown/bounce, AI mastering, collaboration sync. |

---

## The honest physics limit

State this plainly because it bounds every architecture below:

**You cannot monitor through a server-side plugin in real time.** "The amp sim runs in the cloud *and* I hear myself through it with <10ms latency" is physically impossible — the network round-trip alone blows the budget before any processing happens.

The pro workaround — which people already do locally today — is to **monitor through a local placeholder effect**, then **commit / re-amp through the real server-side plugin on playback**. You hear an approximate (or dry) signal live with zero/near-zero latency; the authoritative processed signal is rendered after the fact, where latency doesn't matter.

---

## Three concrete architectures

### 1. Browser-native DAW — the "kerf-of-audio"

Fits the sovereign / web-native thesis best.

- **Real-time path (client):** Web Audio API + **AudioWorklet** runs the real-time audio graph on a dedicated audio thread, entirely client-side, with roughly **15–50ms** achievable latency. **Web MIDI API** for hardware controllers. DSP plugins as **WASM** modules using the **Web Audio Modules (WAM)** standard and WASM-SIMD.
- **Non-real-time path (Vulos):** Vulos compute workers — the **same mechanism as kerf's `fly.worker.toml`** — handle the heavy lifting: render stems, stream large sample libraries, offline plugin processing, AI mastering.
- **Streaming:** none for the real-time path. The audio graph runs fully client-side; zero streaming.
- **Good for:** beat-making, MIDI / loop production, casual recording.
- **Honest limit:** ~20–50ms browser latency is **marginal for pro live-instrument tracking**, which wants <10ms.

### 2. Native bare-metal DAW — the `local_only` tier

For pro tracking. Uncompromised.

- A real native DAW (Ardour / Reaper) running on **bare-metal Vulos** with **ASIO / CoreAudio / JACK** direct hardware access → **<10ms**, full professional tracking.
- The tradeoff: requires being **at the machine**. This is the classic local DAW experience, just hosted on Vulos hardware.

### 3. Hybrid — thin client + server project

The clever middle ground.

- **Local monitoring (client):** either **hardware direct monitoring** on the audio interface (a zero-latency dry signal routed in the interface itself), *or* a **thin local monitor engine** running a local placeholder effect.
- **Server project (Vulos):** holds the full project and **streams the backing mix down** to the client. Latency here is fine — it's playback, so the client buffers and time-aligns it.
- **Recording:** the performer monitors **live local input** against the **buffered backing**. Each recorded take is **timestamped against a shared clock**, and the server places it **sample-accurately** using standard DAW delay / latency compensation — applied across the network.

The result: the heavy project lives in the cloud, the thin client only does the minimal real-time monitoring, and takes land in the right place after the fact.

---

## Collaboration

**Local-first + CRDT**, reusing Vulos's **sync + storage** stack — the CRDT merge itself is the planned forward-plan Sync spec (roadmap/SYNC.md), not yet built; cr-sqlite is not integrated (see SYNC.md/CLUSTER.md reality checks):

- Everyone runs **their own local audio engine** (low latency, no shared real-time path).
- **Project state** — arrangement, automation, notes, MIDI — syncs via **CRDT**.
- **Audio stems** sync as **files via storage**.
- **No real-time audio streaming for editing.** Each collaborator works locally and the project merges. Nobody waits on anyone else's audio loop.

This is the same local-first merge model used elsewhere in Vulos, applied to a project graph that happens to reference audio files.

---

## Adoption reality

Be honest about ecosystem maturity: the **web-DAW ecosystem is less mature than web CAD / IDE**. There is **no kerf-grade open browser DAW** yet.

- **GridSound** (open source, GPL, self-hostable) is the closest analog — the "AudioMass but full DAW" — but it is **immature**.
- **BandLab / Soundtrap / Audiotool** are capable but **hosted SaaS**, which **breaks sovereignty** and is therefore off the table as a base.

So the realistic plan:

- **Casual / production tier:** adopt and extend a **GridSound-class** browser DAW.
- **Pro tracking tier:** **native bare-metal** DAW.
- **Heavy / collaboration:** **Vulos compute workers + CRDT**.

---

## Tiering

| Use | Path | Latency |
|---|---|---|
| Production / MIDI / beats / casual recording | Browser-native DAW (Web Audio + WASM) + Vulos compute workers | ~20–50ms (fine) |
| Pro live-instrument tracking | Native DAW, bare-metal `local_only` | <10ms |
| Tracking against a cloud project on a thin client | Hybrid — local monitoring + server project + timestamped record | local monitor ~0; backing buffered |
| Heavy render / collaboration | Vulos compute workers + CRDT sync | non-real-time |

---

## Non-goals

- **Not** streaming a full DAW GUI for live tracking — latency makes it unusable.
- **Not** running monitoring plugins server-side in the real-time path — physics limit (see above).

---

## Open questions

- **Build vs adopt/extend** a GridSound-class browser DAW for the casual tier.
- **WAM (Web Audio Modules)** plugin-format support, and whether to stand up a **plugin marketplace**.
- **How sample libraries stream** from Vulos storage with low-enough latency for playback.
- **Exact delay-compensation / clock-sync protocol** for the hybrid thin-client tracking mode.

---

*This is an early design captured from discussion (dated 2026-05-26). It describes a separate tool, parallel to `kerf.sh` for CAD.*
