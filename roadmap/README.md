# Vulos Roadmap

This directory holds the design documents — one per system area. Each document explains *what* a part of Vulos is meant to do and *how* it's structured, but **not** the day-to-day "what's left to ship" view. That lives in [GitHub Issues](https://github.com/vul-os/vulos/issues); the high-level themes are in [`../ROADMAP.md`](../ROADMAP.md).

Read it like this:

1. Start with the area that interests you.
2. Skim the **Goal** + **Non-goals** at the top of the file.
3. If you want to *do* something rather than just read, browse [GitHub Issues](https://github.com/vul-os/vulos/issues) filtered to that area, or open one.

> **Most roadmap areas are fully implemented.**
> Remaining shipped-area work is intentional later-phase *depth*, tracked as
> explicit notes inside the relevant design docs (NOTIFICATIONS → push-over-peering).
> The Ladybird browser spike has been **removed** — Chromium is the sole engine.
>
> **Exception: CLUSTER/SYNC/CONCURRENCY are not "full cr-sqlite CRDT."** Those
> three docs originally described a shipped cr-sqlite CRDT engine; it was never
> integrated and cannot be under the pure-Go/no-CGO rule (`docs/decisions.md`
> D23/D94-J). Each of those docs now carries a reality-check callout at the top
> describing what is actually shipped (S3 snapshot cold path + a LAN-only
> pure-Go app-registry CRDT) versus the forward plan (a shared DMTAP-substrate
> Sync spec, relay as WAN rendezvous — not yet built).
>
> **Design track: image-based OS distribution — status is now mixed, not uniformly planned.**
> Seven new design docs lock in the shift from flash-and-SSH to a signed,
> immutable, A/B-updated OS pulled from a public bucket, netbooted and installed,
> with a leaderless multi-instance data layer: **OS-DISTRIBUTION.md**,
> **SEED-TRUST.md**, **NETBOOT.md**, **SIGNING.md**, **COORDINATION.md**,
> **SYNC.md**, **CONCURRENCY.md**, plus the extracted **APP-MANIFEST.md**. **Three
> of the seven have since shipped real, wired code** — OS-DISTRIBUTION.md (A/B
> slots + `osdist` service), SIGNING.md (dm-verity + Ed25519 PKI), and SYNC.md (a
> LAN-only, four-domain CRDT engine, narrower than the doc's own full scope but
> real). The table below was corrected to match each doc's own Status line
> instead of restating this paragraph's old blanket "planned." SEED-TRUST.md,
> NETBOOT.md, COORDINATION.md and CONCURRENCY.md remain design-only. Their open
> work is tracked in GitHub Issues. Cloud/control-plane features are developed in
> a separate (non-public) repository and are out of scope for this roadmap — the
> OSS side works correctly without any external control plane.

## Areas

| Area | File | What it's about | Status |
|---|---|---|---|
| AI assistant | [`AI.md`](AI.md) | The Cmd+K / chat panel, AI-generated mini-apps, sandbox, harness choice, where chat is surfaced across the desktop | shipped |
| App store | [`APP-STORE.md`](APP-STORE.md) | apt / Flatpak / static-binary recipes, how installable apps reach the registry, web-native vs streamed apps | shipped |
| Default web apps | [`DEFAULT-WEB-APPS.md`](DEFAULT-WEB-APPS.md) | The small apps that ship in the OS: calculator, calendar, clock, text editor, weather, maps, music, video, etc. | shipped |
| Bare-metal init | [`BAREMETAL-INIT.md`](BAREMETAL-INIT.md) | Power-on → compositor → desktop. labwc + cage, Plymouth splash, live USB + installer, ARM device variants | shipped |
| First-boot setup | [`INIT.md`](INIT.md) | The setup wizard that runs the first time the desktop loads: identity, storage, SSH, recovery kit, join flow | shipped |
| Cluster & storage | [`CLUSTER.md`](CLUSTER.md) | Multi-node sync via S3 (MinIO): node presence, encrypted snapshot cold path, file sync, presence leases, conflicts | partially shipped — see doc's reality check (cr-sqlite CRDT layer not integrated; app-registry CRDT is LAN-only) |
| Network & remote access | [`NETWORK.md`](NETWORK.md) | Subdomain routing, connection modes (fabric / direct / local), TURN/coturn, `{app}--{profile}` naming | shipped |
| Notifications | [`NOTIFICATIONS.md`](NOTIFICATIONS.md) | Structured notification model, notification center, DND, action buttons, push-via-peering | shipped |
| Client offline | [`OFFLINE.md`](OFFLINE.md) | Client↔box offline: cache-not-truth, outbox queue, per-app offline scope, degraded state. Not a CRDT — see SYNC.md for box↔box | design only |
| Offline auth | [`OFFLINE-AUTH.md`](OFFLINE-AUTH.md) | The OS auth gate for offline access: local unwrap of the cached master-key envelope (reuses `masterKey.js`), fail-closed, per-app HKDF keys. Apps own cache; OS owns the gate | design only |
| Process control | [`PROCESS-CONTROL.md`](PROCESS-CONTROL.md) | Activity Monitor: listing processes/apps/connections, `(pid, starttime)` identity, the never-signal list, SIGTERM→SIGKILL escalation, and what "not responding" actually measures | shipped |
| Peering | [`PEERING.md`](PEERING.md) | The big one: Ed25519 identity, contacts, signed S2S envelopes, messaging, media, WebRTC calls, SFU, drop, relays, feeds | shipped |
| Device profiles | [`DEVICE-PROFILES.md`](DEVICE-PROFILES.md) | pc / tv / car / watch — different layouts, focus models, and behaviors per form factor | shipped |
| Streaming optimizations | [`STREAMING-OPTIMIZATIONS.md`](STREAMING-OPTIMIZATIONS.md) | GPU encoder selection, NVENC/VA-API low-latency tuning, adaptive bitrate, audio backends, Wayland capture | shipped |
| Gaming | [`GAMING.md`](GAMING.md) | Per-session gaming mode: FPS, encoder profiles, pointer-lock, gamepad rumble, process priority, MangoHud | shipped |
| Client type safety | [`TYPE-SAFETY.md`](TYPE-SAFETY.md) | Gradual JSDoc type-checking of the JSX shell (`tsc --noEmit`), security-critical `src/lib/` first, generated types for the Go→JS API boundary. No `.ts`/`.tsx` — frozen stack unchanged | design only |
| Other | [`OTHER.md`](OTHER.md) | Catch-all: theming, i18n, accessibility — small items that don't deserve their own file yet | shipped |
| Display stack (X11 vs Wayland) | [`DISPLAY-STACK.md`](DISPLAY-STACK.md) | Investigation into consolidating the container image's Xvfb/X11 stack and the bare-metal OS's cage/labwc Wayland stack; measured image-size and CVE evidence, no code changed | investigation complete, decision not yet executed |

### Image-based OS distribution & multi-instance data (mixed — three shipped, four design-only)

The OS distribution model is moving from "flash a disk, patch over SSH" to "pull a signed immutable image from a public bucket, verify it, A/B-update it." **This section previously marked all seven docs "planned" — three had already shipped real code and the table just hadn't been updated to say so.** Status below is copied from each doc's own Status line, not re-derived; open work is tracked in [GitHub Issues](https://github.com/vul-os/vulos/issues).

| Area | File | What it's about | Status |
|---|---|---|---|
| OS distribution & updates | [`OS-DISTRIBUTION.md`](OS-DISTRIBUTION.md) | Signed immutable squashfs in a public bucket, `stable.json` manifest, A/B slots, boot-counter auto-rollback | shipped — A/B slots, `stable.json`, and the `osdist` service (`backend/services/osdist/`) are implemented and wired |
| Local seed & trust anchor | [`SEED-TRUST.md`](SEED-TRUST.md) | The irreducible flashed seed (bootloader + initramfs + baked key + soft bucket URL); forkability | planned |
| Netboot & first boot | [`NETBOOT.md`](NETBOOT.md) | UEFI HTTP Boot / ~1 MB iPXE stick → live-RAM "Try Vulos" → netboot-to-install; TLS+signing two-layer safety | planned — the underlying `--live` squashfs path and installer already exist; HTTP-Boot/iPXE chainloading itself is not built |
| Signing, verity & key rotation | [`SIGNING.md`](SIGNING.md) | dm-verity, per-stage boot-chain verification, offline root-signs-intermediate PKI, monotonic min-epoch revocation | shipped — Ed25519 PKI, `imgverify`, `scripts/verity/gen-verity.sh`, and the rollback guard are implemented; runtime dm-verity activation is wired but not yet smoke-tested on bare metal (see the doc's own caveat) |
| Coordination primitives | [`COORDINATION.md`](COORDINATION.md) | Bucket-backed leases with fencing tokens (`If-Match` CAS); run-leases, singleton jobs, snapshot ownership | planned |
| Multi-instance data sync | [`SYNC.md`](SYNC.md) | Two-tier sync (instance↔instance hot path + durable bucket cold path) + snapshot/compaction | partially shipped — a real, wired, pure-Go leaderless CRDT engine exists (`backend/internal/crdtsync/`) for four domains, LAN-only; the bucket cold path and a WAN path are not built. See the doc's own scope line before treating this as done |
| Concurrency model | [`CONCURRENCY.md`](CONCURRENCY.md) | Per-data-type conflict policy + manifest-declared singleton/replicated/collaborative; live collaboration | planned |
| App manifest | [`APP-MANIFEST.md`](APP-MANIFEST.md) | The `app.json` contract: identity, command, permissions, `visibility`, the new `concurrency` field | shipped + extending |

### Future / exploratory

These areas are designed but not actively being shipped (or where the tasks are still long-tail backlog). They're real designs, not napkin sketches — just lower priority than the items above. (The Ladybird browser spike in the `future/` subdirectory was removed — Chromium is the sole engine; its doc is retained there as historical context only.)

| Area | File | What it's about |
|---|---|---|
| Authentication | [`AUTHENTICATION.md`](AUTHENTICATION.md) | TPM-sealed device identity, TOTP, password manager, FIDO2 passkeys, mTLS client certs, SMS-over-VoIP |
| Mobile / telephony | [`MOBILE.md`](MOBILE.md) | ModemManager-backed SMS, voice calls, eSIM management; Messages + Dialer apps |
| ActivityPub | [`ACTIVITYPUB.md`](ACTIVITYPUB.md) | A single Fediverse client (Mastodon, Pixelfed, PeerTube, Lemmy) as a default app |

## Where to start reading

If you're new to the project and want to understand it end to end, read in roughly this order:

1. [`AI.md`](AI.md) — small, conversational, sets the tone of "the OS should feel like an assistant."
2. [`DEFAULT-WEB-APPS.md`](DEFAULT-WEB-APPS.md) — concrete: what shipping apps look like inside the shell.
3. [`APP-STORE.md`](APP-STORE.md) — how the OS *gains* apps: the registry, recipes, web vs streamed.
4. [`NETWORK.md`](NETWORK.md) — domain model and how an instance is reachable. Sets up the next two.
5. [`PEERING.md`](PEERING.md) — the most distinctive idea in the project. Long, but the design is unusually concrete.
6. [`BAREMETAL-INIT.md`](BAREMETAL-INIT.md) — what happens when you flash to USB. Closes the loop.

The rest you can pick up as you need them.

## Status labels

The badge column above uses three values:

- **shipped** — design and implementation are both complete. Later-phase depth
  (if any) is noted inside that area's design doc rather than tracked as open
  issues.
