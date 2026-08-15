# The app launch path: what was broken, what is fixed, what is proven

**Date:** 2026-08-15 · **Scope:** why `calculator` answered `{"error":"app not running"}` on a real box, and the state of all 16 bundled apps afterwards.

This closes the investigation begun in `dbebd593`/`6783b1a1`/`1070b559`/`8e2d6b7f`, whose author was interrupted before writing it up. Those four commits are re-verified against the code here, not taken on trust.

---

## 1. The launch path, end to end

Read from the click to the process. Files read, in the order the path runs:

| Step | File | What it does |
|---|---|---|
| 1 | `frontend/src/shell/launchApp.ts:64` | The single shared launch lane for the Launchpad, ⌘K, and every other surface. |
| 2 | `launchApp.ts:67` | `isBuiltinComponent(id)` → a React component renders in-shell. The gateway is never involved. |
| 3 | `launchApp.ts:103` → `backend/cmd/server/routes_router.go:143` | `GET /api/router/classify?app=<id>` resolves the execution lane. `calculator` is `{Web: true}` (`routes_router.go:44`) → **lane `WebApp`**. |
| 4 | `launchApp.ts:106` | The WebApp lane. |
| 5 | `launchApp.ts:107` | **`if (app.url)` → `openWindow(...)` and `return`.** |
| 6 | `frontend/src/core/AppRegistry.ts` | Every bundled app declares `url: '/app/<id>/'`. |
| 7 | `backend/services/gateway/gateway.go:479` | The window's iframe loads `/app/calculator/` → `Gateway.Handler()`. |
| 8 | `gateway.go:591` | `netMgr.GetForProfile(appID, userID, profile)` — `appnet/namespace.go:324`, **a pure map lookup**. |
| 9 | `gateway.go:593` | Miss → `{"error":"app not running"}`, 404. |

Step 5 is the gap. The `else` branch at `launchApp.ts:113` *does* call `POST /api/apps/launch` — but only for an app with **no** `url`, and every bundled app has one. That branch is dead for all of them.

Three independent things had to be true for the bug, and all three were:

- **The shell never calls launch for these apps** (step 5 above).
- **Nothing starts them at boot.** All 16 manifests ship `"auto_start": false`, and `AutoStart` is *read nowhere* in the backend — `grep` finds it only in the two struct definitions (`manifest.go:119`, `registry.go:229`) and one copy (`registry.go:1043`). Even flipping it to `true` would have started nothing.
- **`GetForProfile` never launches.** It is a map lookup with an ADOPT-A-PORT fallback. There was no on-demand launch anywhere in the codebase.

### The prior agent's four commits — verified

| Commit | Claim | Verified |
|---|---|---|
| `dbebd593` | `build.sh` copied apps from `$ROOT_DIR/apps` after they moved to `frontend/apps/`; `/opt/vulos/apps` shipped empty | Yes. Fix present; `scripts/check-apps-run.py` now runs build.sh's own copy block. |
| `1070b559` | `Validate` demanded a command unconditionally, so a static `type: web` app could never be valid | Yes — `manifest.go:158-170` now requires `index.html` instead for `AppTypeWeb`. |
| `8e2d6b7f` | `GetManifest` read only `$HOME/.vulos/apps` | Yes — `store.go:591` now falls back to `s.bundledDirs`. |
| `62cb3006`/`6783b1a1` | A gate that every bundled app starts and serves | Yes, and it is real — but see §3, it could not have caught the port-bind defect. |

---

## 2. Where the gap was closed, and why

Three candidates. The decision is recorded in full in `backend/services/gateway/activate.go`; the short form:

### Rejected: the shell calls launch before opening the WebApp window

- **`POST /api/apps/launch` is admin-only.** `main.go:1805`: `p.Role != auth.RoleAdmin` → 403. Every non-admin user on the box would get a 403 and then a 404. Relaxing that gate to make an app open work would widen a *privileged exec endpoint* for every caller, not just app opens.
- **The shell is not the only caller.** A bookmark, a restored window, a PWA entry, a deep link, the service worker, and **every subresource of the app document** reach `Gateway.Handler()` without passing through `launchApp.ts`. A shell-side launch fixes the first click and nothing else.
- **It races the window.** A fire-and-forget `fetch` runs concurrently with the iframe navigation. The first request usually loses and still gets "app not running".

### Rejected: start `auto_start: false` apps at boot

15 python processes on every box, for apps nobody opened.

### Chosen: the gateway starts the app on the request that needs it

`gateway.go` calls an `Activator` seam on a namespace miss; `backend/cmd/server/activate_apps.go` implements it (manifest → port → app secret → `LaunchManifest` → wait until reachable). It is the single choke point every caller passes through, and the request that needed the app is the one that waits for it.

| Failure mode | Answer |
|---|---|
| Slow first open | Bounded at 20 s (`activateTimeout`), then **504 with the reason**. Measured cold open: 560 ms. |
| Thundering herd | Single-flighted on `(user, profile, app)` — the same three dimensions the namespace is keyed on. Keyed on `appID` alone, one user's launch would satisfy another user's request, which would then find no namespace of its own. |
| Launch races the window | The activator returns only after dialling `NSIP:port` successfully. `LaunchManifest` returns at fork/exec, which says nothing about whether the server has bound. |
| An app dies and never restarts | The launcher now tears the namespace down on process exit, so the next request re-activates. See §3.3. |
| Anonymous activation | **Not wired.** `PublicHandler` keeps the plain lookup — an unauthenticated public-web visitor can never fork work on the box. |

Resolution mirrors `/api/apps/launch` exactly: command, work dir and port come from the *validated manifest* and nothing else. `VULOS_DISABLE_EXEC` still stops it. The seam defaults to `nil`, so a gateway with no activator behaves exactly as before.

---

## 3. Defects found and fixed

### 3.1 LAUNCH-01 — nothing ever started a bundled app (`3b2899f1`)

Described above. Fixed by on-demand activation in the gateway.

### 3.2 PORTBIND-01 — no app could bind the port its own manifest declares (`cf219d54`)

**This one was invisible to every existing gate, and it would have made the LAUNCH-01 fix look broken.**

All 16 apps declare `"port": 80`. The launcher enters the netns and drops to nobody:

```
ip netns exec <ns> setpriv --reuid=65534 --regid=65534 --clear-groups --no-new-privs sh -c "python3 server.py"
```

Changing uid clears the capability set, so no `CAP_NET_BIND_SERVICE`. And `ip netns add` gives a new namespace the kernel default `net.ipv4.ip_unprivileged_port_start=1024` **regardless of the host's value**. `bind(0.0.0.0, 80)` → `EACCES`, process exits in milliseconds, gateway proxies to nothing.

Measured on Linux (`scripts/prove-portbind.sh`, privileged `python:3.13-slim`, arm64):

```
host  /proc/sys/net/ipv4/ip_unprivileged_port_start = 0     (Docker sets this)
fresh `ip netns add` namespace                      = 1024  (kernel default)
bind 80 as nobody inside it                         = PermissionError EACCES
after lowering the floor to 80                      = OK
```

Docker setting the host value to `0` is *why no container ever reproduced it*: `ip netns add` gets 1024 back.

**Why the existing gate could not catch it.** `scripts/check-apps-run.py` runs each app as the current user, on an **ephemeral high port** (`free_port()`, `PORT=<random>`), with no namespace. It correctly proves the app's *code* works. It cannot see the privileged-port bind, and no change to it short of running as root in a namespace would.

Fix: namespace setup lowers the floor to the app's own declared port, inside the app's own namespace. It re-permits exactly the range the manifest asked for and nothing below it; the host is untouched. Hard step, not best-effort — an app that cannot bind should fail namespace creation with the reason.

### 3.3 A dead app left its namespace behind forever (`3b2899f1`)

`Launcher`'s exit goroutine deleted the app from `l.apps` but **never destroyed the namespace** — only `Stop()` did that. So a process that exited on its own (crash, OOM, bad config, a port it could not bind) left a registered namespace. `GetForProfile` kept answering with it, so the gateway proxied to a dead `10.200.x.2`: every later request 502'd, and because a namespace still "existed", nothing would ever start the app again. The exit path now tears it down, turning a dead app back into a namespace *miss* that activation heals.

---

## 4. Every bundled app: ships, launches, serves

**Method.** `frontend/apps/` (what `build.sh` copies to `/opt/vulos/apps`) walked entry by entry; each app opened cold through the real gateway on a real Linux kernel — real netns, real iptables, real python process as nobody, real HTTP. `backend/cmd/server/activate_linux_test.go`, run by `scripts/prove-launch.sh` in a privileged `golang:1.25` arm64 container. Nothing mocked.

The walk carries a coverage assertion: fewer than 15 process-backed apps examined is a hard failure, so a gate that checked nothing cannot report a pass.

| App | Ships | Manifest | Launches | Serves | Evidence |
|---|---|---|---|---|---|
| browser | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| calculator | yes | `python3 server.py`, port 80 | yes | yes | cold open 560 ms, 22 872 bytes |
| camera | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| clock | yes | `python3 server.py`, port 80 | yes | yes | e2e walk + crash-recovery test |
| gallery | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| image-editor | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| music | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| notes | yes | `python3 server.py`, port 80 | yes | yes | bound port 80 as nobody, `10.200.164.2:80` |
| phone | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| screenshot | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| system-info | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| text-editor | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| video | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| voice-recorder | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| weather | yes | `python3 server.py`, port 80 | yes | yes | e2e walk |
| **site-template** | yes | `type: web`, **no command** | **n/a** | **no** | permanent 404 with a reason |

`TestLaunchEndToEnd_EveryBundledAppOpens`: **15/15 bundled process apps launched on demand and served their page.**

### site-template does not serve, and that is not fixed

It is a static `type: "web"` app — a directory of files with no process. `1070b559` made its manifest *valid*, but **the gateway has no static-serving lane**: `Handler()` only ever resolves a namespace and reverse-proxies to it. There is nothing on the box that serves a static app's files.

Not fixed here, deliberately. Adding a static file server to the gateway means path-traversal handling, content-type resolution, and interaction with the ORIGIN-01 opaque-origin rules — real security surface, in the security-critical component, in a wave about a different bug. It now returns a **permanent 404 that explains itself** (`ErrNoProcess`) rather than a 504 telling the caller to retry something that can never succeed. `vulos web deploy` remains unusable until a static lane exists.

---

## 5. Mail

### The truth

- The shell registers **Mail** as `lilmail`, `type: 'web'`, `url: '/app/lilmail/'` (`AppRegistry.ts:262`).
- ⌘K opens `/app/lilmail/?uid=…` (`CommandPalette.tsx:391`).
- There is **no `lilmail` in `frontend/apps/`**, none in `src/builtin/`, and **zero mail entries in `registry.json`** (55 entries, none matching `mail`).
- The only lilmail artifact in the shipped rootfs is `out-arm64/webroot/product-logos/lilmail.svg` — a logo.
- `backend/cmd/server/routes_mail.go` exposes `GET /api/mail/url`, defaulting to `http://localhost:3000` — lilmail is a **separate product the operator runs themselves**. And **nothing in the frontend reads that endpoint**: the WebApp lane uses `app.url`, not `/api/mail/url`.

So on a stock image the Mail tile resolves to nothing, and before this wave it failed with the *same* `{"error":"app not running"}` as a Calculator that simply had not started — indistinguishable.

### Decision: made honest, not made to work

Making it *work* means shipping lilmail — a separate Go product with IMAP/SMTP account configuration. That is a product decision and a release, not a bug fix.

Made honest instead, in the backend, where the failure actually happens: the activator now distinguishes **"the box does not have this app"** (`ErrNotInstalled`) from "this app has not started yet". `/app/lilmail/` answers **404 with `"detail": "\"lilmail\" is offered by the shell but no manifest for it exists in the install dir or in the image's bundled apps"`.

This is deliberately generic, not lilmail-specific: it makes *every* shell entry point that resolves to nothing explain itself, including the ones found below.

Proven: `TestLaunchEndToEnd_MailIsHonest` (Linux e2e) and `TestUninstalledApp_SaysSo` (unit).

### Frontend changes needed (not made — not my files)

1. **`CommandPalette.tsx:388-391`** — the Mail row opens `/app/lilmail/`. It should be hidden, or routed through a "Connect Mail" affordance, when mail is not available. The mail search at line 280 (`GET /api/mail/search`) has the same problem; `mailState` already models a degraded state, so the row could degrade rather than opening a dead window.
2. **`AppRegistry.ts:262`** — the `lilmail` tile should be gated on mail actually being present. `suiteBundleOf` already maps `lilmail → 'email'` for opt-out, so the mechanism exists; it needs to key on availability, not just user preference.

---

## 6. Two more dead entry points, same shape as Mail

Audited every `url: '/app/<id>/'` in `AppRegistry.ts` against `frontend/apps/`, `registry.json`, and the builtin-component table. **13 such entries; 12 resolve, 1 does not (`lilmail`).** Two further entries fail differently:

- **`library` ("Universal Memory", `AppRegistry.ts:232`)** — no `url`, so it takes the `else` branch: calls the admin-only `/api/apps/launch` (403 for a normal user), then opens `/app/library/`. There is **no `library` in `frontend/apps/` and none in `registry.json`.** Meanwhile `frontend/apps/notes` ships a complete, working app — and `builtinAliases` (`AppRegistry.ts:551`) maps `notes → library`, which *filters `notes` out* of the installed list (line 584). **The app that works is hidden by an alias pointing at a tile that resolves to nothing.** Fix: either point the `library` entry at `notes`, or drop the alias.
- **`browser` (`AppRegistry.ts:216`)** — declares `url: '/apps/browser/'`. The gateway route is `/app/{id}/`, singular. `/apps/…` never reaches `Gateway.Handler()`. `frontend/apps/browser` ships and works (it serves in the e2e walk when addressed correctly), so this is a one-character path defect hiding a working app.

Both are in `frontend/src/core/AppRegistry.ts`, which this wave does not own. They now fail *legibly* (404 naming the app as not installed) rather than as "app not running".

---

## 6a. The built-in browser: absent from the image, and failing silently

The founder's second report — the browser does not work on a real box — is two defects.

### It was never in the image

`services/webbrowser/chrome.go`'s `findBin` looks for `chromium-browser`, `chromium`, `google-chrome`, `google-chrome-stable`. `build.sh`'s debootstrap rootfs installed **none of them**; `Dockerfile:145` installs `chromium`. The released `v0.2.0-arm64` rootfs contains `./usr/bin/cog` and `./usr/bin/cogctl` and no browser at all.

`cog` does not substitute: it is a single-surface WPE kiosk shell with no tabs, address bar or profile. It renders the Vulos UI; it is not a browser a user can browse with.

Two further binaries on the same execution path were also Dockerfile-only. `pool.go:281` uses `cage` (headless Wayland) **only** when `gpuInfo.Tier != TierSoftware`; every other box falls back to Xvfb — the common bare-metal case. The rootfs had no `xvfb` (no display for *any* streamed app) and no `xdotool` (the X11 injector's fallback when uinput is unavailable, and the VNC path's only injector — the window renders but cannot be typed into).

**Decision: ship Chromium, not Chrome.** Chromium is open source and redistributable inside an image; Chrome is proprietary, and shipping it is a licensing decision rather than a packaging one. `findBin` prefers `chromium` anyway, and the gate now *fails* if a `google-chrome*` package appears in the rootfs list.

**Cost.** Measured on `debian:trixie-slim` arm64 — `chromium xvfb xdotool` pulled in **176 packages, +662 MB on disk, +251 MB gzipped**. That is an upper bound: it was measured against a bare slim base, and the real Vulos rootfs already carries several of those packages (`systemd`, `mesa-*`, `gstreamer*`, `fonts-noto`, `flatpak`). The largest single items are `chromium` (292 MB), `libllvm19` (120 MB) and `chromium-common` (84 MB). Against a 2 GB image floor this is a material but affordable share, and it buys a headline feature that was entirely non-functional on the target hardware.

### The failure was silent

`DesktopCanvas.tsx` launched it as `r.ok ? r.json() : null`, then `(data && data.id) || 'browser'`. Every failure collapsed to one value: the 500 became `null`, `null` fell back to the literal session id `'browser'` that nothing had created, and the window opened regardless — titled Chromium, spinning forever, with no error in the UI or the console.

Shipping the binary does not retire that: a launch can still fail on a box with no free display or a killed stream pool. `frontend/src/layouts/streamedBrowser.ts` now treats a non-ok response as an error carrying the server's own reason, and treats a 200 with no session id as a failure too — that is the same defect in a different disguise.

---

## 6b. The defect class: verified in Docker, broken on bare metal

Four defects in one session share a single cause, and they are worth naming as a class rather than listing as four unrelated bugs.

**A check that only ever runs against the Docker image is structurally blind to all four.** Docker has the file, the binary, the credential drop — so the check passes, and the product is still broken on the hardware it ships to.

| # | Defect | Docker | Bare metal |
|---|---|---|---|
| 1 | `/opt/vulos/apps` shipped empty — `build.sh` copied from `$ROOT_DIR/apps` after the tree moved to `frontend/apps/` (`dbebd593`) | apps present | **zero apps**, two releases running |
| 2 | `scripts/vulos.service` sets `User=vulos`, but on the bare-metal path `cmd/init/main.go` is PID 1 and performs no credential drop | streamed apps run as `vulos` | streamed apps run as **root** |
| 3 | The built-in browser binary. `services/webbrowser/chrome.go` execs chromium; `Dockerfile:145` installs it; the debootstrap rootfs never did | Chrome works | `500 {"error":"chromium not found"}` on every box |
| 4 | `xrandr` (`x11-xserver-utils`). `pool.go:384` resizes the Xvfb display from its 3840×2160 maximum down to the requested resolution; `Dockerfile:153` installs it **with a comment naming that exact binary**; the rootfs did not | correct resolution | every streamed window captured at **4K regardless of the size requested** — a logged warning, not an error |

Nos. 3 and 4 also cover `xvfb`, `xdotool` and `matchbox-window-manager`, all Dockerfile-only and all on the streamed-app execution path (`pool.go` uses cage **only** when the GPU tier is not software; every other box takes the Xvfb path).

**The gate:** `backend/internal/docsref/imagebins_test.go` reads the *source* against the *rootfs package list*, so the container it happens to run in is irrelevant. It scans `services/{webbrowser,stream,desktop}` for every literal binary handed to `exec.Command` / `lookPath` / `findBin`, and requires each to be either mapped to the package that provides it or exempted **with a reason**. An unclassified binary is a hard failure naming the binary and the file that execs it.

It found no. 4 on its first run, having been written for no. 3.

Coverage assertions, because this check's own failure mode is examining nothing: minimum total files scanned (9, the real count), minimum binaries found (8), and **per-directory** coverage — a total alone would stay green if one service were renamed away while another grew.

**Where to look next:** anything else that is true of the container and assumed of the image — file paths, users and credential drops, unit files that the init path does not read, kernel and sysctl defaults (PORTBIND-01 in §3.2 is the same shape one layer down: Docker sets `ip_unprivileged_port_start=0`, a fresh `ip netns` gets 1024 back), and device nodes.

---

## 7. Open defects, not fixed

| Defect | Where | Why not fixed here |
|---|---|---|
| `POST /api/apps/launch` allocates host ports keyed on the bare `appID` (`main.go:1866`), while namespaces are keyed `(user, profile, app)`. Two users launching the same app via the API share one host port and therefore one `127.0.0.1` DNAT rule. | `main.go` | Pre-existing; changing the key also changes `/api/apps/stop`'s release semantics. The **activator** allocates per-instance and is correct. |
| No static-serving lane in the gateway. | `services/gateway` | §4. Security surface in the wrong wave. |
| `AutoStart` is declared in two structs and read nowhere. | `appnet` | Now moot — on-demand activation is strictly better than boot-time start for these apps. Worth deleting or implementing, not both. |
| The installed app set does not sync between instances by any mechanism. A fully-wired replicator carries an `app_registry` table nothing ever writes: `AppSync.LocalInstall`/`LocalUninstall` have zero non-test callers, and `crdtsync/policy.go:135` refuses the table on the grounds that appsync handles it. | `appsync`, `crdtsync` | Established by the concurrent sync audit, not this wave. **Relevant here:** on-demand activation reads the manifest off the local filesystem and writes nothing, so it neither depends on nor worsens this. But an app installed on one instance does not become launchable on another — activation can only start what the local disk already has. |
| `/api/mail/url` is served but read by nothing. | `routes_mail.go` | Dead endpoint; removal is a separate call. |

---

## 8. What is proven, and where

### Proven on this Mac (portable, always-on)

- `services/gateway`: activation fires on a namespace miss; a nil activator keeps the old 404; a failed launch is a 504 **carrying the reason**; a static app and an uninstalled app are permanent 404s that explain themselves; 12 concurrent requests produce **one** launch; two concurrent users produce **two**; an already-running app is not re-launched; `PublicHandler` never activates. `4 mutations killed`, one of which **survived first** — see below.
- `services/appnet`: the namespace emits the unprivileged-port-floor step, inside the app's own namespace, set to the app's own port, only for privileged ports, before the app starts. `4 mutations killed`.
- `services/appnet` arch: cross-scheme normalisation, empty-means-any, fail-closed on unknown, per-entry `installable`, install refusal. `6 mutations killed`.

### Proven only on Linux, in a container

`ip netns` is Linux-only and needs `CAP_SYS_ADMIN`, so **the launch path cannot execute on this Mac at all**. Everything below ran in a privileged arm64 container against this checkout:

- `scripts/prove-portbind.sh` — the kernel behaviour: fresh netns floor is 1024, `bind(80)` as nobody fails, lowering the floor fixes it. Mutation: remove the fix → `NOT PROVEN`, rc=1.
- `scripts/prove-launch.sh` → `backend/cmd/server/activate_linux_test.go` — cold open serves (560 ms); **15/15** apps launch and serve; site-template 404s with a reason; Mail 404s as not-installed; a killed app's namespace is reclaimed and the next request re-activates it; `notes` bound its declared privileged port 80 as nobody at `10.200.164.2:80`.

These tests are opt-in via `VULOS_E2E_LAUNCH=1` and **hard-fail rather than skip** if that is set without root, so a run meant to prove something cannot quietly prove nothing.

### Not verifiable on this machine

- **Behaviour on a real booted v0.2.0 image.** The container proves the launch path against this checkout's code; it does not prove the *image* ships what it should. `dbebd593` fixed the ship bug and `check-apps-run.py` gates it, but no booted-image verification was run in this wave.
- **The shell actually opening a window on a real box.** The gateway half is proven; the browser half is not exercised here.
- **The App Hub UI** consuming the new `installable` field — the backend seam is proven; no UI exists yet.
- **`flatpak --supported-arches` on a box with flatpak installed.** The fallback path (flatpak absent) is exercised; the flatpak-present path is not — no flatpak on this Mac or in the test container.

### A mutation that survived

`TestDifferentUsers_LaunchSeparately` ran its two users **sequentially**, so the first activation had already cleared its single-flight key before the second began — the `appID`-only key mutation never collapsed anything and the test stayed green. **The test was fixed, not the check:** the two requests now overlap inside the activator, and the mutation is killed. Worth recording as a general shape: a single-flight test that does not actually overlap is testing nothing.
