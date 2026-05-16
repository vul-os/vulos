# Vula OS — Roadmap Tasks

Backlog for autonomous Sonnet coding agents. One task = one focused PR on branch `task/<ID>`.

**Status legend:** `todo` · `in_progress` · `review` (branch committed, awaiting merge) · `done` (merged to main) · `blocked`

**Worker contract:** work only on your assigned task in your isolated worktree; implement fully;
run the relevant build/lint; commit to `task/<ID>`; do not push; report build status + blockers.

Operating model & decisions: see `decisions.md`. More roadmap areas (App Store, Peering, Init,
Baremetal, Cluster, Network, Notifications, Device Profiles, Gaming, Other, future/*) are being
decomposed by Opus agents and appended below as they complete.

---

## AI (roadmap/AI.md)

### [AI-01] Add visibility field to app manifest and persistent visibility store
- **Status:** todo
- **Priority:** P0
- **Effort:** M
- **Roadmap:** roadmap/AI.md § Public Apps
- **Depends on:** none
- **Parallel-safe:** no — modifies `backend/services/appnet/manifest.go` and `store.go` (both already locally modified — coordinate).
- **Context:** `AppManifest` (`backend/services/appnet/manifest.go:43`) has no `visibility` field; valid values must be `private | local | public` with `private` default. No API or persistence for per-app visibility. AI apps live separately in `~/.vulos/ai-apps/<id>/meta.json` (`main.go:1160`).
- **Scope:** Add a `Visibility string` field (`private`/`local`/`public`, default `private`) to `AppManifest` with validation. Add a small persistent visibility store (JSON under `~/.vulos/db/`) keyed by app id that also covers AI apps. Provide getter/setter methods. No HTTP endpoints in this task.
- **Acceptance criteria:**
  - [ ] `AppManifest.Visibility` validated to one of three values; empty defaults to `private`.
  - [ ] A `VisibilityStore` persists/loads per-app visibility for manifest apps and AI apps.
  - [ ] Unit test covers default + validation + round-trip persistence.
  - [ ] `go build ./...` passes.
- **Key files:** `backend/services/appnet/manifest.go`, `backend/services/appnet/store.go`, new `backend/services/appnet/visibility.go`, `backend/cmd/server/main.go`.

### [AI-02] API endpoints to get/set per-app visibility
- **Status:** todo
- **Priority:** P0
- **Effort:** S
- **Roadmap:** roadmap/AI.md § Public Apps
- **Depends on:** AI-01
- **Parallel-safe:** no — adds handlers in `backend/cmd/server/main.go`.
- **Context:** No endpoints exist to toggle visibility. AI-apps endpoints registered around `main.go:1160-1221` use `mux.HandleFunc` + `writeJSON`/`writeErr`.
- **Scope:** Add `GET /api/apps/visibility` (list all apps incl. AI apps with current visibility) and `POST /api/apps/{id}/visibility` (body `{"visibility":"private|local|public"}`) backed by the AI-01 store. Validate; return updated state.
- **Acceptance criteria:**
  - [ ] `GET /api/apps/visibility` returns each known app id + visibility (defaults private).
  - [ ] `POST /api/apps/{id}/visibility` updates/persists; rejects invalid values 400.
  - [ ] Endpoints require same auth as other `/api/*` endpoints.
- **Key files:** `backend/cmd/server/main.go`, `backend/services/appnet/visibility.go`.

### [AI-03] Topbar always-visible public-app warning indicator
- **Status:** todo
- **Priority:** P0
- **Effort:** M
- **Roadmap:** roadmap/AI.md § Public Apps → Topbar warning
- **Depends on:** AI-02
- **Parallel-safe:** yes — adds a component in `src/core/`, mounts in topbar; coordinate with AI-04 on popover.
- **Context:** Topbar rendered via `src/core/SystemPulse.jsx` (compact mode line 395) + `src/shell/Dock.jsx`. No public-app warning exists. Needs non-dismissable indicator, color-coded yellow (local) / red (public).
- **Scope:** `PublicAppsWarning` component polls `GET /api/apps/visibility`, counts `local`/`public`, renders a persistent topbar badge only when any non-private app exists. Red if any public, else yellow. No dismiss. Click opens AI-04 manager.
- **Acceptance criteria:**
  - [ ] Badge appears whenever ≥1 app local/public; hidden when all private.
  - [ ] Color: red if any public, yellow if only local.
  - [ ] No dismiss; disappears only when no non-private apps remain.
  - [ ] Count text reflects actual number; refreshes on changes.
- **Key files:** `src/core/SystemPulse.jsx`, new `src/core/PublicAppsWarning.jsx`.

### [AI-04] Public apps manager popover + first-time confirmation dialog
- **Status:** todo
- **Priority:** P1
- **Effort:** M
- **Roadmap:** roadmap/AI.md § Public Apps → Topbar warning / Settings UI
- **Depends on:** AI-02, AI-03
- **Parallel-safe:** yes — new component + a section in `src/core/Settings.jsx`.
- **Context:** `Settings.jsx` has an `aiapps` tab (`Settings.jsx:8,547`) listing AI apps with delete. No per-app visibility toggle UI exists.
- **Scope:** Popover (from AI-03 badge) listing non-private apps with one-click "Make private" + visibility selector. Same control in Settings AI Apps + general apps. Confirmation dialog first time an app is made public.
- **Acceptance criteria:**
  - [ ] Topbar badge click opens list of local/public apps with toggles calling `POST /api/apps/{id}/visibility`.
  - [ ] First-time set-to-public triggers confirmation; cancel aborts.
  - [ ] Settings exposes per-app visibility; list refreshes after change.
- **Key files:** new `src/core/PublicAppsManager.jsx`, `src/core/Settings.jsx`, `src/core/PublicAppsWarning.jsx`.

### [AI-05] Make saved AI apps appear in the app launcher with icons and categories
- **Status:** todo
- **Priority:** P1
- **Effort:** M
- **Roadmap:** roadmap/AI.md § AI Apps (persistence, icons, categories)
- **Depends on:** none
- **Parallel-safe:** no — modifies `src/core/AppRegistry.js` (locally modified) and `backend/cmd/server/main.go` ai-apps handlers.
- **Context:** Saved AI apps persist to `~/.vulos/ai-apps/<id>/{meta.json,index.html,server.py}` (`main.go:1160-1221`), only surfaced in Settings → AI Apps. `AppRegistry.js` merges builtins + `/api/store/installed` (`AppRegistry.js:119-149`) but never fetches `/api/ai-apps`. `meta.json` lacks icon/category.
- **Scope:** Store `icon` (emoji heuristic ok) + `category` in `meta.json`; extend `GET /api/ai-apps` to return them. Add `refreshAIApps()` in `AppRegistry.js` merging `/api/ai-apps` into `getApps()` as launchable apps opening `/api/ai-apps/{id}/html`.
- **Acceptance criteria:**
  - [ ] Saved AI apps appear in Launchpad + Cmd+K after reload.
  - [ ] Each AI app has icon + category in `meta.json` and registry merge.
  - [ ] Launching opens saved HTML (starts Python backend if present).
  - [ ] AI apps persist/reappear after server restart.
- **Key files:** `src/core/AppRegistry.js`, `backend/cmd/server/main.go`, `src/shell/Launchpad.jsx`.

### [AI-06] AI app editing/iteration workflow ("make the button bigger")
- **Status:** todo
- **Priority:** P1
- **Effort:** M
- **Roadmap:** roadmap/AI.md § AI Apps → Editing
- **Depends on:** none
- **Parallel-safe:** no — touches `src/core/Portal.jsx` and ai-apps backend handlers in `main.go`.
- **Context:** AI apps generated once via `<viewport>` parsing in `Portal.jsx:191-288`, saved via `Window.jsx:137`. No way to reopen + ask AI to modify; no endpoint to fetch current HTML/Python back into chat as edit context.
- **Scope:** "Edit with AI" action loads app's current `index.html`/`server.py` (existing `GET /api/ai-apps/{id}/html|python`) into a chat turn as context, sends change request, overwrites saved app via new `POST /api/ai-apps/{id}/update`. Viewport reopens with new version.
- **Acceptance criteria:**
  - [ ] User opens saved AI app, submits modification request including current code as context.
  - [ ] AI response replaces saved HTML/Python on disk via update endpoint.
  - [ ] Updated app reopens reflecting the change.
- **Key files:** `src/core/Portal.jsx`, `src/core/Settings.jsx`, `backend/cmd/server/main.go`.

### [AI-07] AI app version history and rollback
- **Status:** todo
- **Priority:** P2
- **Effort:** M
- **Roadmap:** roadmap/AI.md § AI Apps → Versioning
- **Depends on:** AI-06
- **Parallel-safe:** no — extends ai-apps backend handlers in `main.go` (same area as AI-06).
- **Context:** Save handler (`main.go:1162`) overwrites in place — no history; AI-06 edit destroys prior versions.
- **Scope:** On save/update snapshot prior files into `versions/<timestamp>/` + `versions.json`. Add `GET /api/ai-apps/{id}/versions`, `POST /api/ai-apps/{id}/rollback`, minimal Settings UI.
- **Acceptance criteria:**
  - [ ] Each save/update creates a versioned snapshot (cap last 20).
  - [ ] `versions` lists; `rollback` restores chosen one.
  - [ ] Settings AI Apps shows version history + rollback button.
- **Key files:** `backend/cmd/server/main.go`, `src/core/Settings.jsx`.

### [AI-08] Pre-warmed Python process pool for sandbox startup
- **Status:** done
- **Priority:** P2
- **Effort:** M
- **Roadmap:** roadmap/AI.md § AI Apps → Performance
- **Depends on:** none
- **Parallel-safe:** yes — isolated to `backend/services/sandbox/`.
- **Context:** `Sandbox.Run` (`backend/services/sandbox/sandbox.go:52`) cold-starts `python3` per request; `Portal.jsx:263` adds blind 500ms wait. No pre-warming/readiness probe.
- **Scope:** Warm pool of pre-spawned Python processes reused by `Run`; real readiness probe (poll port) replacing the 500ms wait. Keep `containsDangerousCode` checks + timeout. Pool size env-configurable.
- **Acceptance criteria:**
  - [ ] Configurable warm pool; `Run` measurably faster than cold start.
  - [ ] Readiness check confirms port listening before returning URL.
  - [ ] Dangerous-code filtering + 5-min timeout still enforced.
  - [ ] Pool cleaned up on `StopAll`/shutdown (no leaks).
- **Key files:** `backend/services/sandbox/sandbox.go`, `backend/cmd/server/main.go`.

### [AI-09] First-run experience that introduces the AI chat
- **Status:** done
- **Priority:** P1
- **Effort:** M
- **Roadmap:** roadmap/AI.md § Desktop & Apps Must Lead to Chat
- **Depends on:** none
- **Parallel-safe:** yes — new component + a mount in `src/App.jsx`/desktop layout.
- **Context:** Setup wizard `src/auth/Setup.jsx` never introduces AI chat. Chat opens via Cmd+K / `setChat(true)` (`Portal.jsx:46-56`). No first-run chat prompt/flag.
- **Scope:** One-time dismissable first-run overlay (persisted flag) that on first desktop load opens chat + short explainer (build apps, control OS, search files). Never reappears once dismissed.
- **Acceptance criteria:**
  - [ ] First desktop load opens chat with intro explainer.
  - [ ] Persisted flag prevents re-show.
  - [ ] Doesn't interfere with Setup wizard.
- **Key files:** new `src/core/AIFirstRun.jsx`, `src/App.jsx` or `src/layouts/DesktopCanvas.jsx`, `src/providers/ShellProvider.jsx`.

### [AI-10] Persistent dock/taskbar AI chat entry point
- **Status:** done
- **Priority:** P1
- **Effort:** S
- **Roadmap:** roadmap/AI.md § Desktop & Apps Must Lead to Chat → Dock/taskbar
- **Depends on:** none
- **Parallel-safe:** yes — touches only `src/shell/Dock.jsx`.
- **Context:** Chat reachable via Cmd+K and `vulos:chat` events (`Portal.jsx:46,180`). `src/shell/Dock.jsx` has no always-visible AI button.
- **Scope:** Persistent AI icon in Dock opening chat (`setChat(true)` via `useShell`). Pinned, always visible on desktop + mobile layouts.
- **Acceptance criteria:**
  - [ ] Persistent AI icon always present in Dock.
  - [ ] Click/tap opens AI chat panel.
  - [ ] Works in desktop + mobile dock rendering.
- **Key files:** `src/shell/Dock.jsx`, `src/providers/ShellProvider.jsx`.

### [AI-11] Context menu "Ask AI about this" for files and selected text
- **Status:** done
- **Priority:** P2
- **Effort:** M
- **Roadmap:** roadmap/AI.md § Desktop & Apps Must Lead to Chat → Context menu
- **Depends on:** none
- **Parallel-safe:** yes — touches `src/shell/DesktopContextMenu.jsx` and file manager context menu.
- **Context:** `DesktopContextMenu.jsx` only handles desktop-background right-click; `FileManager.jsx` has no AI hook. Chat accepts external input via `vulos:chat` window event (`Portal.jsx:180-187`).
- **Scope:** Add "Ask AI about this" entry to desktop/file context menu opening chat pre-filled with context (file path/name or selected text) via `vulos:chat` event.
- **Acceptance criteria:**
  - [ ] Right-click file (and/or selected text) shows "Ask AI about this".
  - [ ] Selecting opens chat with prompt containing the context.
  - [ ] Existing native-window context menu behavior preserved.
- **Key files:** `src/shell/DesktopContextMenu.jsx`, `src/builtin/files/FileManager.jsx`.

### [AI-12] App empty-state and error-state "Ask AI" affordances
- **Status:** todo
- **Priority:** P2
- **Effort:** M
- **Roadmap:** roadmap/AI.md § Desktop & Apps Must Lead to Chat → empty/error states
- **Depends on:** none
- **Parallel-safe:** yes — small additive changes across a few builtin apps + a shared helper.
- **Context:** Builtin apps have empty/error states with no AI funnel. Portal exposes `vulos:chat`. No shared "Ask AI" helper.
- **Scope:** Reusable `<AskAIButton context="...">` dispatching `vulos:chat`; wire into 2-3 representative states (File Explorer empty folder, a failed terminal/exec output).
- **Acceptance criteria:**
  - [ ] Reusable AskAI helper opens chat with context.
  - [ ] Wired into ≥ File Explorer empty state + one error state.
  - [ ] Click opens chat pre-filled with relevant context.
- **Key files:** new `src/core/AskAIButton.jsx`, `src/builtin/files/FileManager.jsx`, one error-state location (e.g. `src/builtin/terminal/Terminal.jsx`).

### [AI-13] Expand proactive agent system checks (memory, disk, thermal)
- **Status:** done
- **Priority:** P2
- **Effort:** M
- **Roadmap:** roadmap/AI.md § Current Stack (Proactive agent)
- **Depends on:** none
- **Parallel-safe:** no — registers checks in `backend/cmd/server/main.go` near line 173.
- **Context:** `ProactiveAgent` (`backend/services/ai/proactive.go`) supports many checks but only low-battery registered (`main.go:173-182`).
- **Scope:** Register memory-pressure, low-disk, high-CPU-temp checks using existing telemetry/energy/disks services. Thresholds via constants/env.
- **Acceptance criteria:**
  - [ ] ≥3 new checks registered using existing service data.
  - [ ] Each fires only above thresholds, dedupes via notify path.
  - [ ] No regression to battery check; `go build ./...` passes.
- **Key files:** `backend/cmd/server/main.go`, `backend/services/ai/proactive.go`.

---

## STREAM (roadmap/STREAMING-OPTIMIZATIONS.md)

### [STREAM-01] Add conditional Chromium GPU flags driven by gpu.Detect()
- **Status:** todo
- **Priority:** P0
- **Effort:** S
- **Roadmap:** roadmap/STREAMING-OPTIMIZATIONS.md § Chromium Browser (GPU-Specific)
- **Depends on:** none
- **Parallel-safe:** yes — touches Chromium launch arg construction (`src/shell/Launchpad.jsx:119` area / backend webbrowser launcher); coordinate with STREAM-08.
- **Context:** Chromium launched with hardcoded software flags (`src/shell/Launchpad.jsx:119`). `gpu.Detect()` exists (`backend/services/gpu/gpu.go:179`) but not consulted for browser flags.
- **Scope:** Expose `gpu.Detect()` via endpoint (reuse or add `GET /api/gpu/info`); build Chromium flags conditionally — GPU flags when tier != software, current software flags otherwise.
- **Acceptance criteria:**
  - [ ] Backend handler returns `gpu.Detect()` JSON.
  - [ ] GPU tier → GPU flag set, no `--disable-gpu`.
  - [ ] Software tier → exact current flags unchanged.
  - [ ] Flag selection isolated/pure-tested.
- **Key files:** `backend/services/gpu/gpu.go`, `backend/cmd/server/main.go`, `src/shell/Launchpad.jsx`, `src/builtin/webbrowser/RemoteBrowser.jsx`.

### [STREAM-02] Add NVENC/VA-API low-latency encoder tuning flags
- **Status:** done
- **Priority:** P0
- **Effort:** S
- **Roadmap:** roadmap/STREAMING-OPTIMIZATIONS.md § Video Encoding
- **Depends on:** none
- **Parallel-safe:** yes — touches only `backend/services/gpu/gpu.go` `EncoderArgs()`.
- **Context:** `gpu.EncoderArgs()` (`gpu.go:105`) returns minimal args; roadmap wants low-latency flags. `vp8enc` (line 136) must stay unchanged.
- **Scope:** NVENC: `zerolatency=true`, `b-adapt=false`, `rc-lookahead=0`, `aud=true`. VA-API: `tune=low-power`, `cabac-entropy-coding=true` where applicable. Guard property names. Leave `vp8enc` untouched.
- **Acceptance criteria:**
  - [ ] NVENC args include the four properties.
  - [ ] VA-API args include the two properties.
  - [ ] Software (`vp8enc`) args byte-for-byte unchanged.
  - [ ] `go build ./...` passes.
- **Key files:** `backend/services/gpu/gpu.go`.

### [STREAM-03] Expose GStreamer video encoder as a named element
- **Status:** todo
- **Priority:** P0
- **Effort:** M
- **Roadmap:** roadmap/STREAMING-OPTIMIZATIONS.md § Adaptive Bitrate — Wire It Up
- **Depends on:** none
- **Parallel-safe:** no — modifies `backend/services/stream/pool.go` + `gpu.go` `EncoderArgs()`; conflicts STREAM-02/04.
- **Context:** Video pipeline built `pool.go:226-248` via `gst-launch-1.0`, encoder has no `name=`. `bitrate.go` computes quality but `stream.go:160` only stores it.
- **Scope:** Add stable `name=venc` to encoder element; store encoder element name + gst process handle on `Session`. Enabling refactor for STREAM-04; no behavior change.
- **Acceptance criteria:**
  - [ ] Pipeline includes `name=venc` for all tiers incl. `vp8enc`.
  - [ ] `Session` exposes encoder element name + bitrate-change stub.
  - [ ] Streaming works, no element-name parse errors.
- **Key files:** `backend/services/gpu/gpu.go`, `backend/services/stream/pool.go`, `backend/services/stream/stream.go`.

### [STREAM-04] Apply adaptive bitrate changes to the running encoder
- **Status:** todo
- **Priority:** P0
- **Effort:** M
- **Roadmap:** roadmap/STREAMING-OPTIMIZATIONS.md § Adaptive Bitrate — Wire It Up
- **Depends on:** STREAM-03
- **Parallel-safe:** no — modifies `backend/services/stream/stream.go` + `pool.go`.
- **Context:** `newBitrateController` (`stream.go:160`) `onChange` only updates fields; encoder bitrate never changed. Quality levels in `bitrate.go:22`.
- **Scope:** In `onChange`, change live encoder bitrate by tearing down/restarting video gst segment via `runWithBackoff` with new bitrate; map kbps→correct per-element units; debounce.
- **Acceptance criteria:**
  - [ ] Quality change restarts/updates encoder with new bitrate (sw/NVENC/VA-API).
  - [ ] Correct units per element (bps vp8enc, kbps h264/av1).
  - [ ] Debounced (≥5s) and logged.
  - [ ] No goroutine/process leak.
- **Key files:** `backend/services/stream/stream.go`, `backend/services/stream/pool.go`, `backend/services/stream/bitrate.go`.

### [STREAM-05] Add gamepad data channel to StreamViewer client
- **Status:** done
- **Priority:** P1
- **Effort:** S
- **Roadmap:** roadmap/STREAMING-OPTIMIZATIONS.md § Input Injection
- **Depends on:** none
- **Parallel-safe:** yes — touches only `src/builtin/stream/StreamViewer.jsx`.
- **Context:** Backend handles `gamepad` data channel (`stream.go:188,345`). `RemoteBrowser.jsx` creates it (lines 106,334) but `StreamViewer.jsx` only creates `mouse`/`keyboard` (lines 93-98).
- **Scope:** Port gamepad channel + polling loop from `RemoteBrowser.jsx` into `StreamViewer.jsx`; payload matches `handleGamepad` struct.
- **Acceptance criteria:**
  - [ ] StreamViewer opens `gamepad` channel, sends snapshots when gamepad connected.
  - [ ] Payload matches `buttons[]bool, axes[]float64, triggers[]float64`.
  - [ ] No polling when no gamepad; loop cleaned on unmount.
- **Key files:** `src/builtin/stream/StreamViewer.jsx` (reference `src/builtin/webbrowser/RemoteBrowser.jsx`).

### [STREAM-06] Add PipeWire/PulseAudio backend detection to the audio capture pipeline
- **Status:** todo
- **Priority:** P1
- **Effort:** M
- **Roadmap:** roadmap/STREAMING-OPTIMIZATIONS.md § Audio
- **Depends on:** none
- **Parallel-safe:** no — modifies audio gst pipeline in `backend/services/stream/pool.go`.
- **Context:** Audio hardcoded `pulsesrc device=virtual_speaker.monitor` + `opusenc` (`pool.go:251-267`). `audio.go:200 detectBackend()` exists but ignored.
- **Scope:** Conditional source on detected backend (PipeWire path when available; identical PulseAudio fallback). Optional gaming-mode `opusenc frame-size=10`.
- **Acceptance criteria:**
  - [ ] PipeWire capture when available; PulseAudio path identical to current otherwise.
  - [ ] Gaming-mode option → `frame-size=10`; default `20`.
  - [ ] Software/PulseAudio path no regression.
- **Key files:** `backend/services/stream/pool.go`, `backend/services/gpu/gpu.go`, `backend/services/audio/audio.go`.

### [STREAM-07] Add GPU streaming packages to the Dockerfile
- **Status:** done
- **Priority:** P1
- **Effort:** S
- **Roadmap:** roadmap/STREAMING-OPTIMIZATIONS.md § Dockerfile — GPU Support
- **Depends on:** none
- **Parallel-safe:** yes — touches only `Dockerfile`.
- **Context:** Dockerfile lacks Wayland/PipeWire stack; already sets `WLR_BACKENDS=headless`/`WLR_RENDERER=pixman` (lines 89-90).
- **Scope:** Add cage, labwc, pipewire, pipewire-pulse, wireplumber, gstreamer1.0-pipewire, xdg-desktop-portal-wlr, libgbm1, libegl1; add env vars (`XDG_SESSION_TYPE=wayland`, `MOZ_ENABLE_WAYLAND=1`, keep `DISPLAY=:99`); keep existing packages.
- **Acceptance criteria:**
  - [ ] Dockerfile installs the listed packages.
  - [ ] New env vars added; existing X11 defaults retained.
  - [ ] `docker build` resolves on trixie-slim; no removed packages.
- **Key files:** `Dockerfile`.

### [STREAM-08] Add cage headless Wayland compositor path for GPU streaming sessions
- **Status:** todo
- **Priority:** P2
- **Effort:** L
- **Roadmap:** roadmap/STREAMING-OPTIMIZATIONS.md § Display Server / Screen Capture
- **Depends on:** STREAM-07
- **Parallel-safe:** no — heavily modifies `backend/services/stream/pool.go` session startup.
- **Context:** `pool.go:118` always starts Xvfb + matchbox; `gpu.CaptureArgs()` returns `pipewiresrc` when capable but nothing produces PipeWire frames.
- **Scope:** When `gpu.Detect().Tier != software` and cage present, run per-session `cage` (headless wlroots) with per-session Wayland socket isolation (partly at `pool.go:155-177`); keep Xvfb path unchanged otherwise; teardown kills cage + cleans socket.
- **Acceptance criteria:**
  - [ ] GPU+cage → session runs cage (no Xvfb) with socket isolation.
  - [ ] No GPU/cage missing → Xvfb+matchbox unchanged, works.
  - [ ] `Stop()` kills cage + removes socket/runtime dir.
  - [ ] Capture feeds WebRTC track both modes.
- **Key files:** `backend/services/stream/pool.go`, `backend/services/stream/stream.go`, `backend/services/gpu/gpu.go`.

---

## APPSTORE / WEBAPP (roadmap/APP-STORE.md, roadmap/DEFAULT-WEB-APPS.md)

### [WEBAPP-01] Fix invalid `notifications` permission in calendar+clock manifests
`todo` · P0 · S · dep: none · parallel: no — apps/calendar/app.json, apps/clock/app.json, backend/services/appnet/manifest.go
Scope: Add `notifications` to `ValidPermissions` (manifest.go:18-27) with a comment (or remove from both manifests). Calendar/clock currently fail `ScanAndValidateApps`.
AC: [ ] calendar+clock pass validation [ ] `notifications` in ValidPermissions w/ comment [ ] `/api/store/validate` lists them not errors

### [WEBAPP-02] Implement PDF.js rendering in PDF Viewer
`done` · P0 · M · dep: none · parallel: yes — apps/pdf-viewer/ only
Scope: Vendor PDF.js locally (no CDN) into apps/pdf-viewer/, render to canvas w/ page nav, zoom, fit-to-width; update server.py to serve assets.
AC: [ ] local PDF renders on canvas [ ] prev/next/zoom/fit work [ ] no external network [ ] server.py serves assets

### [WEBAPP-03] Add CodeMirror editing to Text Editor
`done` · P0 · M · dep: none · parallel: yes — apps/text-editor/ only
Scope: Vendor CodeMirror 6 locally; highlighting (JS/Py/Go/HTML/CSS/JSON/MD/Bash), line numbers, wrap toggle, find/replace, theme, font-size; keep localStorage persistence; server.py serves assets.
AC: [ ] CM editor w/ 8-lang highlight [ ] linenums/wrap/find/theme/font work [ ] no ext network [ ] localStorage docs still work

### [WEBAPP-04] Filesystem persistence API for default apps
`done` · P0 · M · dep: none · parallel: no — new backend/services/appfs/, backend/cmd/server/main.go
Scope: Sandboxed `GET/PUT/DELETE /api/appdata/{app}/{path}` + list under `~/.vulos/<app>/` w/ path-traversal protection (realpath prefix like apps/calculator/server.py:19-22).
AC: [ ] PUT then GET round-trips [ ] `../`/abs rejected 400 [ ] list scoped to app subdir [ ] go test passes; route registered

### [WEBAPP-05] Wire default web apps into AppRegistry builtinRegistry
`todo` · P1 · S · dep: WEBAPP-01 · parallel: no — src/core/AppRegistry.js (LOCKED dirty — defer until resolved)
Scope: Curated registry entries/aliases for calculator/calendar/clock/pdf-viewer/text-editor/weather (icon/name/category/keywords), pattern of library/gallery entries.
AC: [ ] getApps() returns the 6 w/ metadata [ ] no dupes vs /api/store/installed [ ] searchApps finds them

### [WEBAPP-06] Server-side persistence to Calendar app
`done` · P1 · S · dep: WEBAPP-04 · parallel: yes — apps/calendar/ only
Scope: Replace localStorage events with WEBAPP-04 appdata API persisting under `~/.vulos/calendar/`; keep views/recurrence/.ics.
AC: [ ] events survive restart [ ] views/recurrence/.ics still work [ ] no CRUD UI regression

### [WEBAPP-07] Complete Weather app: hourly + geolocation + UV
`done` · P2 · S · dep: none · parallel: yes — apps/weather/ only
Scope: Add Open-Meteo hourly strip, browser geolocation w/ IP/manual fallback, UV index in current conditions.
AC: [ ] hourly strip renders [ ] geolocation auto-detect w/ fallback [ ] UV shown [ ] manual search still works

### [WEBAPP-08] Music Player default app
`done` · P2 · M · dep: WEBAPP-04 · parallel: yes — new apps/music/
Scope: New app (app.json+server.py+index.html+icon.svg): play `~/.vulos/music/`, playlists, ID3 art, transport, shuffle/repeat, library, kbd shortcuts; valid permissions only.
AC: [ ] passes ScanAndValidateApps [ ] plays mp3/ogg/wav/m4a [ ] playlist/shuffle/seek/volume [ ] space/arrows

### [WEBAPP-09] Video Player default app
`done` · P2 · M · dep: WEBAPP-04 · parallel: yes — new apps/video/
Scope: New app: mp4/webm/mkv playback, transport/volume/fullscreen/speed, srt/vtt drag-drop subtitles, PiP, queue, kbd.
AC: [ ] passes validation [ ] plays mp4/webm, PiP+fullscreen [ ] srt/vtt loads [ ] queue+space/arrows/F

### [WEBAPP-10] Image Editor default app
`done` · P3 · M · dep: WEBAPP-04 · parallel: yes — new apps/image-editor/
Scope: Canvas editor: crop/rotate/flip/resize, adjust sliders, ≥5 filters, annotate, undo/redo, export jpg/png/webp to `~/.vulos/pictures/`.
AC: [ ] passes validation [ ] crop/rotate/flip/resize+sliders [ ] ≥5 filters+undo/redo [ ] export works

### [WEBAPP-11] Screenshot/Screen Capture app
`done` · P3 · M · dep: WEBAPP-04 · parallel: yes — new apps/screenshot/
Scope: getDisplayMedia screenshot + MediaRecorder .webm, annotate (arrow/text/blur), region crop, save `~/.vulos/screenshots/`, clipboard copy.
AC: [ ] passes validation [ ] screenshot+webm [ ] annotate+crop [ ] save+clipboard

### [WEBAPP-12] Voice Recorder app
`done` · P3 · S · dep: WEBAPP-04 · parallel: yes — new apps/voice-recorder/
Scope: MediaRecorder mic capture, live waveform, playback, trim, save via WEBAPP-04, timestamped list; `microphone` permission.
AC: [ ] passes validation w/ microphone [ ] record+waveform+playback [ ] trim+timestamped list

### [WEBAPP-13] Camera app
`done` · P3 · S · dep: WEBAPP-04 · parallel: yes — new apps/camera/
Scope: getUserMedia photo+video, front/back flip, optional filters, save to pictures/videos; camera+microphone perms.
AC: [ ] passes validation w/ perms [ ] photo+video saved [ ] camera switch

### [WEBAPP-14] Maps app (Leaflet+OSM)
`done` · P3 · M · dep: WEBAPP-04 · parallel: yes — new apps/maps/
Scope: Vendored Leaflet, OSM tiles, Nominatim search, OSRM routing, geolocation, favourites via WEBAPP-04; `network` perm.
AC: [ ] passes validation [ ] map+search recenters [ ] directions route [ ] favourites persist

### [WEBAPP-15] System Info app
`done` · P3 · S · dep: none · parallel: yes — new apps/system-info/
Scope: Read-only dashboard from existing backend endpoints (OS/kernel/arch, CPU/RAM/storage, GPU, net, uptime); add thin /api/system/info aggregator only if missing.
AC: [ ] passes validation [ ] shows live hw data [ ] no mock values

### [APPSTORE-01] Static (download) install path in registry
`done` · P0 · M · dep: none · parallel: no — backend/services/appnet/registry.go, registry.json
Scope: Add Static/DownloadURL recipe to VersionRecipe (registry.go:61-73): download+extract+manifest+port, checksum-verified; reuse store.go:178-183 tar logic; keep apt/Flatpak.
AC: [ ] static recipe w/ checksum [ ] installs end-to-end [ ] apt/flatpak unchanged, go test passes [ ] 1 real static entry works

### [APPSTORE-02] Single-binary web apps: Navidrome, Memos, Uptime Kuma
`todo` · P1 · M · dep: APPSTORE-01 · parallel: no — registry.json
Scope: Add 3 type:web entries (gitea single-binary pattern), correct ports, 0.0.0.0:${PORT}, data dir, perms network+filesystem.
AC: [ ] 3 entries valid [ ] each installs+serves UI [ ] registry.json valid JSON

### [APPSTORE-03] Static web apps: Excalidraw, draw.io, Hoppscotch
`todo` · P1 · M · dep: APPSTORE-01 · parallel: no — registry.json
Scope: Add 3 static SPA entries via APPSTORE-01 recipe, pinned release+checksum, static file server cmd+port.
AC: [ ] 3 static entries w/ checksums [ ] each serves SPA, zero ext net for shell [ ] valid JSON

### [APPSTORE-04] Vaultwarden + LibreTranslate registry entries
`todo` · P1 · M · dep: APPSTORE-01 · parallel: no — registry.json
Scope: Vaultwarden single-binary; LibreTranslate pip + post_install model fetch (PostInstall recipe registry.go:234-253).
AC: [ ] 2 entries [ ] vaultwarden serves vault; libretranslate post_install runs [ ] valid JSON

### [APPSTORE-05] Streamed apt apps: Shotcut/Ardour/LMMS/Darktable/OBS/QGIS/Octave/GnuCash
`todo` · P2 · M · dep: none · parallel: no — registry.json
Scope: 8 type:desktop apt entries mirroring gimp/kdenlive (category/arch/homepage/license/icon/keywords). Data only.
AC: [ ] 8 valid entries [ ] ≥2 install+launch via stream pool [ ] valid JSON

### [APPSTORE-06] Gaming apps + auto gaming-mode (Steam/Lutris/Wine)
`todo` · P2 · L · dep: APPSTORE-05, GAME-02 · parallel: no — registry.json, backend/services/stream/, backend/services/wine/wine.go
Scope: Wine/Lutris/Steam type:desktop entries + deps (gamemode/mangohud/winetricks); flag gaming-category sessions for gaming mode (wire to stream LaunchOpts/bitrate).
AC: [ ] 3 entries w/ deps [ ] gaming-category session gets elevated bitrate/fps/low-latency [ ] non-gaming unaffected, go test passes

### [APPSTORE-07] Matrix: Conduit homeserver + Cinny client (phase 1)
`todo` · P3 · L · dep: APPSTORE-01 · parallel: no — registry.json, new apps/cinny/
Scope: Conduit static-binary type:service homeserver (SQLite, localhost) + Cinny static web entry pointed at it. Defer bridges/wizard.
AC: [ ] conduit runs localhost SQLite [ ] cinny loads, local register/login works [ ] E2EE local DM works

### [APPSTORE-08] Surface streamed vs web-native type in App Hub UI
`todo` · P2 · S · dep: none · parallel: yes — src/builtin/apphub/AppHub.jsx
Scope: Show Web vs Streamed badge from existing `type` field; add type filter alongside category.
AC: [ ] badge per app from type [ ] type filter toggles list [ ] no backend change, category/search still work

---

## NET / CLUSTER (roadmap/NETWORK.md, roadmap/CLUSTER.md)

### [NET-01] Subdomain parser `{app}--{profile}.{ulid}.{domain}`
`todo` · P0 · M · dep: none · parallel: no — backend/services/gateway/gateway.go, backend/cmd/server/main.go, backend/services/appnet/dns.go
Scope: Shared `parseSubdomain(host,baseDomain)->(app,profile,ok)` split on `--` w/ `default` fallback; wire gateway + main.go router; keep /app/{id}/ fallback.
AC: [ ] browser--work parses app+profile [ ] terminal. → default [ ] /app/cockpit/ resolves default [ ] unit test 2/1/none-part

### [NET-02] Namespace keying by `{profile}-{appId}`
`todo` · P0 · M · dep: NET-01 · parallel: no — backend/services/appnet/namespace.go, launcher.go, gateway/gateway.go
Scope: Add profile dimension to namespace/launcher keying; GetForProfile; Launch accepts profile; backward-compat for `default`.
AC: [ ] same app 2 profiles = 2 ns [ ] gateway routes per profile [ ] default still resolves [ ] Stop targets single profile

### [NET-03] `--` naming validation (usernames/profiles/appIDs)
`done` · P0 · S · dep: none · parallel: no — backend/services/appnet/manifest.go, backend/services/auth/auth.go, backend/services/profiles/browser.go
Scope: Shared regex `^[a-z0-9][a-z0-9-]*[a-z0-9]$` (forbids `--`, leading/trailing `-`); apply to app id, username Register, profile Create/Update.
AC: [ ] appID w/ `--` fails [ ] username `--`/lead/trail rejected [ ] profile `--` rejected [ ] single-hyphen still ok

### [NET-04] DNS + frontend URLs use `{app}--{profile}`
`todo` · P1 · M · dep: NET-01, NET-02 · parallel: no — backend/services/appnet/dns.go, src/shell/Launchpad.jsx, src/core/Portal.jsx
Scope: /etc/hosts + Resolve use new format; Launchpad/Portal build {app}--{profile} URLs (default profile); keep path fallback.
AC: [ ] /etc/hosts new format [ ] Resolve parses it [ ] Launchpad opens {app}--default [ ] path fallback unchanged

### [NET-05] Cookie domain across `{app}--{profile}.{ulid}.{domain}`
`done` · P1 · S · dep: NET-01 · parallel: yes — backend/services/auth/handlers.go
Scope: cookieDomain = per-instance base (strip {app}--{profile} label) so session shared across instance apps not instances.
AC: [ ] cookie = .{ulid}.vulos.org [ ] IP/localhost dev still ok [ ] unit test subdomain/IP/localhost

### [NET-06] Node/instance identity config (ULID/hostname/domain mode)
`done` · P0 · M · dep: none · parallel: no — backend/internal/config/config.go, backend/services/network/network.go, main.go
Scope: Load VULOS_INSTANCE_ID/HOSTNAME/DOMAIN_MODE/NODE_ID/MODE; persist ULID to ~/.vulos/instance-id first boot (no net call); Domain() derives {ulid}.vulos.org; expose /api/network/status. (overlaps INIT-01 — coordinate, prefer shared identity)
AC: [ ] ULID persisted stable [ ] Domain() = {ulid}.vulos.org fabric/direct [ ] /api/network/status has fields [ ] no net call

### [NET-07] `/api/health` cluster health endpoint
`todo` · P1 · S · dep: none · parallel: yes — backend/cmd/server/main.go (new handler)
Scope: Public GET /api/health: data-dir writable + disk threshold + sync-lag placeholder; 200 healthy / 503 degraded JSON.
AC: [ ] 200+JSON healthy [ ] 503 when not writable/low disk [ ] no auth

### [NET-08] Direct mode (Mode B) enrollment + acme-dns
`todo` · P1 · L · dep: NET-06 · parallel: no — new backend/services/network/enroll.go, main.go
Scope: Detect public IP, POST control API /api/enroll/direct {ulid,ip,email}, persist acme-dns creds, PUT /api/dns/update on IP change; local trigger route. Ziti/cert issuance out of scope.
AC: [ ] public IP detected [ ] enroll stores creds [ ] IP-change triggers DNS update [ ] control URL configurable

### [NET-09] Connection-mode switching API + Settings UI
`todo` · P2 · M · dep: NET-06, NET-08 · parallel: no — src/core/Settings.jsx, main.go, backend/services/network/network.go
Scope: Mode field + POST /api/network/mode (fabric/direct/own/local), never regen ULID; radio-group UI w/ status.
AC: [ ] UI 4 modes w/ active+status [ ] local-only stops ext listener [ ] direct triggers NET-08 [ ] ULID never regen

### [NET-10] TURN/coturn Settings UI + test endpoint
`todo` · P2 · S · dep: none · parallel: yes — src/core/Settings.jsx, backend/services/network/turn.go, main.go (new TURN routes)
Scope: GET/POST /api/turn/config (host, write-only secret) + POST /api/turn/test reachability; Network settings section.
AC: [ ] host+secret save/reload (secret never returned) [ ] test reports success/fail [ ] creds use configured host

### [CLUSTER-01] SQLite store package w/ cr-sqlite extension
`done` · P0 · L · dep: none · parallel: yes — new backend/services/store/, backend/go.mod (GO.MOD OWNER)
Scope: SQLite opener loading cr-sqlite ext, migration runner, crsql_as_crr helpers, schema (users/sessions/profiles/settings/installed_apps). Package+schema+tests only.
AC: [ ] opens ~/.vulos/db/vulos.db + migrations [ ] cr-sqlite loads, crsql_as_crr ok (graceful if absent) [ ] idempotent migrations [ ] unit test CRUD

### [CLUSTER-02] Migrate auth.Store to SQLite
`todo` · P0 · L · dep: CLUSTER-01 · parallel: no — backend/services/auth/auth.go, profiles.go
Scope: Back auth.Store w/ SQLite, preserve public API, Flush()=no-op, one-time auth.json→SQLite import.
AC: [ ] all methods work SQLite-backed [ ] auth.json imported once [ ] Flush no-op, sessions survive restart [ ] expired sessions not returned

### [CLUSTER-03] S3 cluster client (SSE-C, Argon2id)
`done` · P0 · M · dep: NET-06 · parallel: yes — new backend/services/cluster/s3.go, backend/go.mod (go.mod — serialize w/ CLUSTER-01)
Scope: minio-go wrapper putEncrypted/getEncrypted SSE-C, Argon2id key from passphrase+salt, salt at cluster/encryption-salt; VULOS_S3_* env.
AC: [ ] encrypted PUT/GET round-trip [ ] Argon2id(3,64MiB,4,32) [ ] salt created/reused [ ] wrong passphrase fails

### [CLUSTER-04] cluster package: Node identity/Register/Peers
`done` · P1 · M · dep: NET-06, CLUSTER-03 · parallel: yes — new backend/services/cluster/cluster.go, main.go
Scope: Node/Cluster types; Register() writes nodes/{id}/meta.json heartbeat; Peers() lists; Health() reuses NET-07; wire heartbeat at startup when S3 configured.
AC: [ ] Register writes+refreshes last_seen [ ] Peers returns all incl stale [ ] disabled cleanly w/o S3 [ ] unit test mock S3

### [CLUSTER-05] cr-sqlite changeset sync loop to S3
`todo` · P1 · L · dep: CLUSTER-01, CLUSTER-03, CLUSTER-04 · parallel: yes — new backend/services/cluster/sync.go
Scope: Every VULOS_SYNC_INTERVAL: push crsql_changes>last_pushed encrypted to nodes/{id}/changes/{ver}.bin; pull+apply peers; per-peer cursors.
AC: [ ] 2 DBs sync insert in 1 cycle [ ] concurrent writes merge (CRDT) [ ] cursors persisted, resume [ ] stops on ctx cancel

### [CLUSTER-06] MinIO registry entry + storage settings UI
`todo` · P2 · M · dep: none · parallel: no — registry.json, src/core/Settings.jsx, main.go
Scope: minio type:service registry entry (per roadmap, ${PORT}/${VULOS_STORAGE_*}, singleton, auto_start); Storage settings panel + GET /api/storage/status + enable toggle via store install.
AC: [ ] minio entry valid [ ] panel w/ toggle+status [ ] enable installs MinIO via /api/store [ ] JSON still parses

### [CLUSTER-07] App registry sync: installed_apps reconciler
`todo` · P2 · L · dep: CLUSTER-01, CLUSTER-05 · parallel: no — new backend/services/cluster/reconcile.go, backend/services/appnet/store.go
Scope: installed_apps (CRR) + local_app_status (local) tables; write desired-state on install/uninstall; ReconcileApps() diffs DB vs Installed() w/ backoff.
AC: [ ] install records row [ ] reconciler installs missing [ ] uninstalls status=removed [ ] failures recorded+retried

### [CLUSTER-08] File sync (fsnotify→S3) w/ conflict copies
`todo` · P2 · L · dep: CLUSTER-03, CLUSTER-04 · parallel: yes — new backend/services/sync/, backend/go.mod
Scope: fsnotify watch ~/.vulos/data + db/browser-profiles (ignore apps/bin); upload encrypted files/{rel}+.meta; pull overwrite-if-unchanged else name.conflict-{node}-{ts}.
AC: [ ] edit uploads file+meta [ ] pull overwrites unchanged [ ] divergent edit = conflict copy no loss [ ] apps/bin ignored

### [CLUSTER-09] Presence leases (advisory)
`todo` · P3 · M · dep: CLUSTER-03, CLUSTER-08 · parallel: yes — new backend/services/cluster/presence.go, main.go
Scope: leases/{user}/{hash} 30s heartbeat/60s stale; AcquireLease/CheckLease/ReleaseLease; GET /api/presence/check, POST /api/presence/lease; advisory only.
AC: [ ] acquire writes+renews [ ] 2nd node sees fresh lease [ ] >60s stale non-blocking [ ] release removes

### [CLUSTER-10] Conflict notification toasts + resolver UI
`todo` · P3 · M · dep: CLUSTER-08 · parallel: no — src/shell/Toasts.jsx, new viewer, backend/services/notify/, main.go
Scope: Emit notify on conflict copy; conflict-resolution view lists *.conflict-* + GET /api/sync/conflicts + POST resolve (keep one).
AC: [ ] conflict pushes toast [ ] view lists conflicts w/ node+ts [ ] resolve keeps chosen [ ] no-op when none

---

## INIT / BMINIT (roadmap/INIT.md, roadmap/BAREMETAL-INIT.md)

### [INIT-01] Instance ULID + auto hostname at first boot
`todo` · P0 · M · dep: none · parallel: no — new backend/services/identity/, main.go, backend/go.mod (overlaps NET-06 — share identity pkg; coordinate)
Scope: First boot gen ULID (oklog/ulid/v2), default hostname {user}-{device}, persist ~/.vulos/db/instance.json + /etc/hostname; GET /api/identity, POST /api/identity/hostname.
AC: [ ] first boot 26-char ULID persisted [ ] reused on 2nd boot [ ] GET /api/identity [ ] POST hostname updates [ ] go build, ulid in go.mod

### [INIT-02] Hardened OpenSSH in image + first-boot host keys
`todo` · P0 · M · dep: none · parallel: no — Dockerfile, build.sh, backend/cmd/init/main.go
Scope: openssh-server in 3 apt blocks; sshd_config.d/vulos.conf hardened; init gen host keys + start sshd idempotent; EXPOSE 22.
AC: [ ] openssh in Dockerfile+2 build.sh blocks [ ] vulos.conf no-password/prohibit-password [ ] init gens keys+sshd idempotent [ ] docker build ok

### [INIT-03] SSH emergency-key endpoint (return-once)
`todo` · P0 · M · dep: INIT-02 · parallel: yes — new backend/services/sshkey/, main.go (route reg only)
Scope: POST /api/setup/ssh-key: gen Ed25519, append pub to /root/.ssh/authorized_keys (0700/0600), return privkey once, never persist.
AC: [ ] returns valid ed25519 pair [ ] pub appended no dupe [ ] privkey never written [ ] go build

### [INIT-04] MinIO storage provisioning endpoint
`todo` · P0 · L · dep: none · parallel: yes — new backend/services/storageprov/, registry.json, main.go (route reg only)
Scope: POST /api/setup/storage{enable,size_gb,password,passphrase}: install MinIO, gen keys, bucket vulos-cluster, SSE-C, persist storage.json (not passphrase); skip case.
AC: [ ] enable starts MinIO+bucket [ ] returns keys, passphrase not persisted [ ] enable:false no-op [ ] storage.json written, go build

### [INIT-05] New-system wizard: Identity/Storage/SSH/RecoveryKit steps
`todo` · P0 · L · dep: INIT-01, INIT-03, INIT-04 · parallel: no — src/auth/Setup.jsx
Scope: Add 4 step components per roadmap order; Identity (GET /api/identity, edit hostname), Storage (toggle+slider+pwd+passphrase+skip), SSH (POST ssh-key, show once+copy).
AC: [ ] STEPS reflects roadmap order [ ] Identity shows ULID+DNS, edits hostname [ ] Storage posts+skip [ ] SSH shows privkey once [ ] npm build

### [INIT-06] Recovery Kit step: JSON download + QR + confirm-gate
`todo` · P0 · M · dep: INIT-05 · parallel: no — src/auth/Setup.jsx, package.json
Scope: RecoveryKitStep shows all creds w/ copy; build versioned JSON download (vula-recovery-kit.json), inline QR (add qrcode dep), Next disabled until typed `confirm`.
AC: [ ] creds+copy [ ] JSON matches schema v1 [ ] QR renders [ ] gated until `confirm` [ ] storage-skipped variant works, npm build

### [INIT-07] Server boot-mode router (setup/sync/normal)
`todo` · P0 · M · dep: none · parallel: yes — new backend/services/bootmode/, main.go (one call+route)
Scope: Detect(home)->{mode,syncState} per db-dir + sync-state.json rules; GET /api/setup/mode; decouple from pre-touched .setup-complete (gate on instance.json).
AC: [ ] fresh→setup [ ] sync-state syncing→sync [ ] db no sync-state→normal [ ] GET /api/setup/mode, go build

### [INIT-08] Join flow backend: validate S3 + sync-state + bg sync
`todo` · P1 · L · dep: INIT-07 · parallel: yes — new backend/services/joinsync/, main.go (route reg)
Scope: POST /api/setup/join validate bucket, write sync-state.json phased, bg sync goroutine; GET sync-status; POST sync-background; resume on boot if mode=sync.
AC: [ ] bad creds rejected, valid writes sync-state [ ] sync-status per-phase [ ] interrupted resumes [ ] sync-background flag, go build

### [INIT-09] Join flow UI: New/Join chooser, Connect Storage, Sync, PIN
`todo` · P1 · L · dep: INIT-08 · parallel: no — src/auth/Setup.jsx, src/App.jsx
Scope: New/Join chooser; Join sub-flow Connect Storage→/api/setup/join, Syncing screen polling sync-status + continue-in-bg, device PIN, Ready; driven by GET /api/setup/mode (jump to Syncing if mode=sync).
AC: [ ] Welcome→New/Join [ ] Join posts creds→Syncing [ ] live phase progress+bg [ ] reload mid-sync resumes [ ] ends PIN→Ready, npm build

### [INIT-10] Join codes: generate/decode + short-code/QR
`todo` · P2 · M · dep: INIT-08 · parallel: yes — new backend/services/joincode/, main.go (routes)
Scope: GET /api/cluster/join-code builds JoinCode (1h TTL) as base32 VULA-XXXX-XXXX-XXXX + QR payload; POST /api/setup/join-code decodes→INIT-08; scoped MinIO creds may stub.
AC: [ ] returns short code+QR [ ] round-trips, rejects expired [ ] 1h expiry, go build

### [INIT-11] Cloud backup of recovery kit to vulos.org
`todo` · P3 · M · dep: INIT-06 · parallel: yes — new backend/services/kitbackup/, src/auth/Setup.jsx (one button)
Scope: POST /api/setup/kit-backup{email,encrypted_kit} to VULOS_CLOUD_URL (default vulos.org); button in Recovery Kit; graceful if unset.
AC: [ ] posts encrypted blob [ ] unconfigured = graceful msg [ ] button works, go build+npm build

### [BMINIT-01] labwc config + Vula traffic-light openbox theme
`done` · P0 · M · dep: none · parallel: yes — new assets/labwc/, assets/themes/, Dockerfile, build.sh
Scope: Add labwc+cage to 3 apt blocks; rc.xml (browser bg layer) + vulos openbox-3 themerc (button.layout CMI) in-repo, copied to image paths.
AC: [ ] labwc+cage installed 3 places [ ] rc.xml+themerc in-repo+copied [ ] left close/min/max [ ] docker build

### [BMINIT-02] vulos-init cage→labwc, browser as background
`todo` · P0 · M · dep: BMINIT-01 · parallel: no — backend/cmd/init/main.go
Scope: Rework startKiosk(): display connected + labwc present → labwc + cog fullscreen (bg pin via rule); cage fallback; keep headless path; set WAYLAND_DISPLAY/XDG_RUNTIME_DIR.
AC: [ ] display+labwc → labwc+cog bg [ ] no display → headless still serves [ ] labwc missing → cage [ ] GOOS=linux build

### [BMINIT-03] `GET /api/shell/native-mode` endpoint
`todo` · P0 · S · dep: none · parallel: yes — backend/cmd/server/main.go (one route)
Scope: Register GET /api/shell/native-mode → {mode:detectNativeMode()} (main.go:1696-1749 exists); frontend useNativeMode.js:30 depends on it.
AC: [ ] returns {mode:...} [ ] useNativeMode resolves not catch [ ] go build

### [BMINIT-04] Native app launch endpoint (skip streaming)
`todo` · P1 · M · dep: BMINIT-02, BMINIT-03 · parallel: yes — main.go (new route), backend/services/appnet/launcher.go
Scope: POST /api/shell/native-launch{binary,args,app_id} guarded nativeMode==native; exec w/ WAYLAND_DISPLAY, track PID, return {pid}.
AC: [ ] spawns w/ WAYLAND_DISPLAY, returns pid [ ] 400 when not native [ ] PID reaped+logged [ ] go build

### [BMINIT-05] Dock: list/focus/close native windows (wlr-foreign-toplevel)
`todo` · P1 · L · dep: BMINIT-04 · parallel: yes — new backend/services/wltoplevel/, main.go (routes)
Scope: Enumerate Wayland toplevels (lswt-style/minimal client); GET /api/shell/windows + focus/minimize/close; empty list outside labwc; add helper to apt if used.
AC: [ ] /api/shell/windows under labwc [ ] focus/min/close act on handle [ ] empty outside native [ ] go build

### [BMINIT-06] Launchpad: native launch vs stream by mode
`todo` · P1 · M · dep: BMINIT-04 · parallel: no — src/shell/Launchpad.jsx, src/providers/ShellProvider.jsx
Scope: Branch on useNativeMode(): builtin unchanged; desktop/registry app + canSpawnNativeWindow → /api/shell/native-launch else stream; remote unchanged.
AC: [ ] native mode → native-launch no stream [ ] remote → stream unchanged [ ] builtin identical [ ] npm build

### [BMINIT-07] Plymouth boot splash + determinate progress
`todo` · P2 · M · dep: none · parallel: yes — new assets/plymouth/, Dockerfile, build.sh
Scope: assets/plymouth/themes/vulos (vulos.plymouth, vulos.script w/ determinate bar+Ctrl+V verbose, placeholder PNGs); install plymouth, set default, kernel cmdline `quiet splash plymouth.theme=vulos`.
AC: [ ] theme dir in-repo [ ] plymouth installed+default [ ] script has bar+Ctrl+V [ ] build ok

### [BMINIT-08] Plymouth→labwc handoff + per-phase progress
`todo` · P2 · M · dep: BMINIT-02, BMINIT-07 · parallel: no — backend/cmd/init/main.go, build.sh (systemd units)
Scope: init shells `plymouth update --progress=N` at roadmap milestones (no-op if absent); `plymouth quit --retain-splash` at compositor up; systemd ExecStart hooks.
AC: [ ] progress at milestones [ ] quit --retain-splash at ready [ ] no-op w/o plymouth [ ] units updated, GOOS=linux build

### [BMINIT-09] Init networking: DHCP, WiFi fallback, mDNS/Avahi
`todo` · P2 · M · dep: none · parallel: no — backend/cmd/init/main.go, Dockerfile, build.sh
Scope: Networking phase: wired DHCP, WiFi fallback from saved creds (reuse wifi.SavedNetworks/Connect), resolv.conf; install+enable avahi-daemon; localhost-only must still work.
AC: [ ] wired DHCP [ ] WiFi fallback [ ] avahi installed, hostname.local resolvable [ ] no-net still serves, GOOS=linux build

### [BMINIT-10] Phase-1 fs: efivars, /dev/shm size, data partition
`todo` · P2 · S · dep: none · parallel: no — backend/cmd/init/main.go
Scope: /dev/shm size=2g; mount efivarfs if /sys/firmware/efi exists; mount labeled vulos-data partition into ~/.vulos; best-effort.
AC: [ ] /dev/shm size=2g [ ] efivarfs only when present [ ] data partition mounted when present [ ] GOOS=linux build

### [BMINIT-11] Hardware detection phase + /var/log/vulos-boot.log
`done` · P2 · M · dep: none · parallel: yes — new backend/services/hwdetect/, backend/cmd/init/main.go (one call)
Scope: Probe GPU(reuse gpu)/audio/input/network/storage/battery; write /var/log/vulos-boot.log; new init phase best-effort non-fatal.
AC: [ ] returns hw info [ ] boot.log written [ ] failures non-fatal [ ] GOOS=linux build

### [BMINIT-12] Installer backend: disks/status/install/progress
`todo` · P2 · L · dep: none · parallel: yes — new backend/services/installer/, main.go (routes)
Scope: GET /api/installer/disks (lsblk -J), /status (live vs installed), POST /install (ESP+root part/format/rsync/bootctl), GET /progress WS (wsutil); guard destructive.
AC: [ ] disks lists drives [ ] status live vs installed [ ] install does part→format→rsync→bootctl [ ] progress WS % [ ] go build

### [BMINIT-13] Installer React app (live-USB)
`todo` · P3 · L · dep: BMINIT-12 · parallel: yes — new src/builtin/installer/, src/core/AppRegistry.js (LOCKED dirty — defer reg until resolved)
Scope: welcome→disk-select(visual map)→progress(WS)→success/reboot; shown only when status=live-USB.
AC: [ ] app shown only live-USB [ ] disk select+install [ ] WS progress+reboot [ ] error recovery, npm build

### [BMINIT-14] squashfs + live USB build
`todo` · P3 · L · dep: none · parallel: no — build.sh, new scripts/initramfs/vulos-live
Scope: build.sh --live: mksquashfs, GPT image (ESP+rootfs squashfs), initramfs overlay hook (squashfs RO + tmpfs upper → overlay → pivot_root); keep tarball path.
AC: [ ] --live produces squashfs+bootable GPT [ ] vulos-live hook installed [ ] non-live unchanged [ ] sh -n build.sh

### [BMINIT-15] ARM device variants (RPi, PinePhone)
`todo` · P3 · M · dep: none · parallel: no — build.sh
Scope: DEVICE=rpi|pinephone: rpi FAT32 boot (config.txt/kernel8.img/.dtb), pinephone U-Boot+dtb; emit vulos-arm64-{rpi,pinephone}.img.gz; reuse rootfs.
AC: [ ] rpi bootable image [ ] pinephone image [ ] generic arm64 unchanged [ ] sh -n build.sh

---

## NOTIF / DEVPROF / GAME / MISC (roadmap/NOTIFICATIONS.md, DEVICE-PROFILES.md, GAMING.md, OTHER.md)
> Peer-to-peer notification spec deferred until PEERING lands (see PEER-* tasks).

### [NOTIF-01] Structured notification model (type/subtype/priority/TTL)
`done` · P0 · M · dep: none · parallel: no — backend/services/notify/notify.go
Scope: Add Type/Subtype/Priority/TTL/Body/UUIDv7 ID + SendNotification w/ defaults; Send/SendWithAction wrappers map Level→Priority; clamp critical→high non-call; IsExpired.
AC: [ ] new fields [ ] legacy callers compile, priority=normal [ ] critical→high non-call [ ] unit test, go build

### [NOTIF-02] Persistent notification storage
`todo` · P0 · M · dep: NOTIF-01 · parallel: no — backend/services/notify/notify.go, new store.go, main.go
Scope: JSON store history(7d/1000)/queue(TTL,200)/settings; New(dataDir); atomic writes; load on start+flush.
AC: [ ] survive restart [ ] prune age+count [ ] queue drains on WS connect [ ] main.go passes dir, go build, unit test

### [NOTIF-03] Priority-aware toast UI + sound + click-to-context
`done` · P0 · M · dep: NOTIF-01 · parallel: yes — src/shell/Toasts.jsx (+audio asset)
Scope: priority→UI (low=none, high=persistent, critical=fullscreen), chime normal+ (settings gate), click opens action URL via IntentRouter; keep level compat.
AC: [ ] low no toast, high no auto-dismiss, normal sound [ ] click navigates [ ] legacy level renders

### [NOTIF-04] Notification Center pull-down + history grouping
`todo` · P1 · L · dep: NOTIF-02 · parallel: no — new src/shell/NotificationCenter.jsx, DesktopCanvas.jsx, MobileStack.jsx, SystemPulse.jsx
Scope: Panel grouped Today/Earlier/Week, dismiss-one+clear-all, bell+unread badge in menu bar+mobile; wire existing list/unread/read/clear.
AC: [ ] bell+badge desktop+mobile [ ] grouped+dismiss+clear [ ] badge clears on read, persists via /unread

### [NOTIF-05] Do Not Disturb (modes + schedule)
`todo` · P1 · M · dep: NOTIF-02 · parallel: no — backend/services/notify/notify.go, store.go, main.go
Scope: DND Off/On/Total + schedule; Send rules (low drop, normal queue+replay, high 2nd-attempt, critical always); GET/PUT /api/notifications/settings; ticker.
AC: [ ] On: low drop, normal queue+replay [ ] critical always [ ] schedule auto-toggles [ ] settings round-trip, go build

### [NOTIF-06] Action notifications w/ inline buttons (local)
`todo` · P2 · M · dep: NOTIF-01, NOTIF-03 · parallel: no — src/shell/Toasts.jsx, NotificationCenter.jsx, main.go, notify.go
Scope: Render body.actions buttons; POST /api/notifications/action records choice+resolves; auto-resolve default at TTL.
AC: [ ] buttons render+post [ ] auto-resolve at TTL [ ] resolved don't reappear

### [DEVPROF-01] Profile model + form-factor detection backend
`done` · P0 · M · dep: none · parallel: no — new backend/services/profiles/device.go, main.go
Scope: DeviceProfile store pc|tv|car|watch → ~/.vulos/db/device-profile.json; detection heuristic (DMI/screen); GET/PUT /api/device-profile.
AC: [ ] GET returns {profile,suggested} default pc [ ] PUT persists+restart [ ] detection no crash w/o DMI [ ] go build, unit test

### [DEVPROF-02] Profile selection step in setup wizard
`done` · P0 · M · dep: DEVPROF-01 · parallel: yes — src/auth/Setup.jsx
Scope: Insert `device` step (after welcome) PC/TV/Car/Watch cards, detected preselected (GET /api/device-profile), PUT on finish.
AC: [ ] device step w/ 4 cards [ ] detected preselected [ ] persisted via PUT

### [DEVPROF-03] Profile context provider + responsive root class
`done` · P1 · S · dep: DEVPROF-01 · parallel: no — new src/core/useDeviceProfile.jsx, src/App.jsx, src/providers/
Scope: DeviceProfileProvider/useDeviceProfile fetch once; data-device-profile attr on root; no visual change.
AC: [ ] hook returns profile app-wide [ ] root has data-device-profile [ ] pc unchanged

### [DEVPROF-04] TV profile: D-pad spatial navigation
`done` · P1 · L · dep: DEVPROF-03 · parallel: yes — new src/core/useSpatialNav.js, src/index.css
Scope: Arrow-key nearest-focus traversal + Enter, gated TV profile; high-contrast focus outline scoped [data-device-profile=tv].
AC: [ ] TV arrows move focus ring, Enter activates [ ] no-op non-TV [ ] outline visible at distance

### [DEVPROF-05] TV profile: 10-foot home layout
`done` · P2 · M · dep: DEVPROF-03, DEVPROF-04 · parallel: yes — new src/layouts/TVHome.jsx, src/App.jsx
Scope: Large-card focusable home selected when profile=tv, from AppRegistry.
AC: [ ] TV renders TVHome large focusable cards [ ] 10ft readable [ ] non-TV unaffected

### [DEVPROF-06] Car profile: driving mode (large targets + DND)
`todo` · P2 · M · dep: DEVPROF-03, NOTIF-05 · parallel: yes — src/index.css, new src/core/useDrivingMode.js, src/App.jsx
Scope: data-device-profile=car CSS enlarges targets, auto-enable DND via NOTIF-05 settings; useDrivingMode toggle default on car.
AC: [ ] car enlarges targets scoped CSS [ ] car auto-enables DND [ ] no pc/tv effect

### [GAME-01] Configurable FPS: default 60 + per-session
`done` · P0 · S · dep: none · parallel: no — backend/services/stream/pool.go
Scope: Default FPS 60 in Launch, clamp to 30/60/90/120/144 (0=60); keep fps request field.
AC: [ ] new sessions 60, explicit honored+clamped [ ] existing callers unaffected [ ] go build

### [GAME-02] Gaming-mode flag + encoder profiles + bitrate tiers
`done` · P0 · L · dep: GAME-01 · parallel: no — backend/services/stream/pool.go, bitrate.go, backend/services/gpu/gpu.go
Scope: LaunchOpts.Gaming; gaming encoder args per tier (zerolatency/no-Bframe/no-lookahead), 10ms Opus, QualityGaming=6000/Max=10000, GamingEncoderArgs().
AC: [ ] gaming:true uses zerolatency args+gaming tiers [ ] non-gaming byte-identical [ ] 10ms Opus [ ] go build, table test

### [GAME-03] Pointer lock + relative-mouse passthrough
`done` · P0 · M · dep: none · parallel: no — src/builtin/stream/StreamViewer.jsx, backend/services/stream/stream.go
Scope: requestPointerLock on click (gaming), send raw movementX/Y (`mr`) uncoalesced while locked, Esc exits; backend relative-move branch in handleMouse → MouseMoveRel.
AC: [ ] click acquires lock, Esc releases [ ] raw deltas move cursor [ ] non-gaming unchanged

### [GAME-04] Shared useGamepad hook → StreamViewer (multi-pad, deadzone)
`todo` · P1 · M · dep: none · parallel: no — new src/core/useGamepad.js, src/builtin/stream/StreamViewer.jsx, src/builtin/webbrowser/RemoteBrowser.jsx
Scope: Extract shared useGamepad({send,deadzone,pollHz}), all pads (index in payload), 120Hz; wire StreamViewer gamepad channel; refactor RemoteBrowser. (NOTE: STREAM-05 already added basic StreamViewer gamepad — reconcile/extend, don't duplicate)
AC: [ ] StreamViewer gamepad channel [ ] multi-pad w/ index [ ] deadzone+poll params [ ] RemoteBrowser still works

### [GAME-05] Gamepad rumble (data channel + uinput FF)
`todo` · P2 · L · dep: GAME-04 · parallel: no — backend/services/input/uinput.go, backend/services/stream/stream.go, src/core/useGamepad.js
Scope: Enable EV_FF/FF_RUMBLE on uinput pad, read FF uploads, forward server→client over gamepad channel, apply via vibrationActuator.playEffect.
AC: [ ] uinput advertises FF_RUMBLE, captures FF [ ] rumble reaches browser playEffect [ ] go build

### [GAME-06] Process priority scheduling (game+encoder)
`todo` · P2 · M · dep: GAME-02 · parallel: no — backend/services/stream/pool.go
Scope: opts.Gaming → app nice -10 + try SCHED_FIFO (soft-fail nice on EPERM), raise encoder priority; degrade w/o SYS_NICE.
AC: [ ] gaming elevated priority, non-gaming unchanged [ ] missing SYS_NICE warns no fail [ ] go build

### [GAME-07] Auto gaming-mode for Wine/Lutris/Steam/gaming-category
`todo` · P1 · M · dep: GAME-02 · parallel: no — backend/services/appnet/manifest.go, backend/services/wine/wine.go, main.go
Scope: Add `gaming` to ValidCategories; set Gaming:true when cmd is wine/lutris/steam or manifest category=gaming. (manifest.go LOCKED dirty — coordinate)
AC: [ ] gaming valid category [ ] wine/lutris/steam/gaming-cat sets Gaming:true [ ] non-gaming unaffected, go build

### [GAME-08] Stream toolbar: FPS/latency/quality/fullscreen/MangoHud
`todo` · P2 · L · dep: GAME-01, GAME-02 · parallel: no — src/builtin/stream/StreamViewer.jsx, main.go, backend/services/stream/pool.go
Scope: Overlay toolbar (gaming): FPS selector→new POST /api/stream/fps (restart capture), RTT from getStats, quality tier, fullscreen+pointer-lock, MangoHud toggle (MANGOHUD=1 env relaunch).
AC: [ ] toolbar FPS changes framerate [ ] RTT+quality live [ ] fullscreen+MangoHud toggle [ ] go build

### [MISC-01] Accent colour picker (system CSS var)
`done` · P1 · M · dep: none · parallel: no — src/core/ThemeProvider.jsx, src/core/Settings.jsx, src/index.css
Scope: Accent in ThemeProvider→localStorage→--accent root var; swatch picker in Settings Appearance; .btn-primary/focus use --accent, blue default.
AC: [ ] picker persists+live --accent [ ] primary follows --accent, default blue [ ] reload preserves

### [MISC-02] Terminal theme + font config
`done` · P2 · M · dep: none · parallel: yes — src/builtin/terminal/Terminal.jsx (+localStorage)
Scope: In-terminal settings: color theme (Default/Solarized/Dracula/Light) + font family/size, localStorage, applied to xterm; default = current exactly.
AC: [ ] theme+font controls apply immediately [ ] persist [ ] default byte-identical

### [MISC-03] Harden /api/exec (restrict + audit)
`todo` · P0 · M · dep: none · parallel: no — backend/cmd/server/main.go, backend/services/auth/handlers.go
Scope: Require admin role for /api/exec, structured audit log (user/cmd/ts/exit), env kill-switch; keep setup flows working (admin-gated or server-side).
AC: [ ] non-admin 403 [ ] every call audit-logged [ ] env flag disables [ ] setup completes, go build

### [MISC-04] Dependency + container CVE scanning in CI
`done` · P1 · S · dep: none · parallel: yes — new .github/workflows/security-scan.yml
Scope: CI govulncheck + npm audit --audit-level=high + Trivy container scan; fail on high/critical w/ documented ignore.
AC: [ ] CI runs 3 scans on PRs [ ] fails high/critical, ignore documented [ ] workflow valid

### [MISC-05] i18n scaffold (provider + locale wiring)
`todo` · P2 · L · dep: none · parallel: no — new src/core/i18n.jsx, src/locales/{en,af}.json, src/App.jsx, src/auth/Setup.jsx, src/core/Settings.jsx
Scope: Lightweight i18n provider t()+locale ctx, JSON catalogs en+af, init from profile locale, migrate Setup wizard strings; pattern only, not whole app.
AC: [ ] t() app-wide, locale from profile [ ] Setup renders via catalog en+af [ ] locale switch updates w/o reload

---

## PEER (roadmap/PEERING.md) — greenfield; phasing in agent notes
Phases: P1 ident/foundation PEER-01..05 · P2 trust/contacts 06-09 · P3 verify/profile 10-13 · P4 messaging/media 14-18 · P5 calls 19-24 · P6 multiparty 25-29 · P7 collab 30-33 · P8 drop 34-36 · P9 security/ext 37-41

### [PEER-01] Scaffold peering service: package, storage, routes
`done` · P0 · M · dep: none · parallel: no — new backend/services/peering/peering.go, main.go
Scope: peering.Service owning ~/.vulos/peering/ tree (identity/profile/inbox/outbox/media/groups/contacts.json), RegisterHandlers stub (501), wire in main.go.
AC: [ ] New(home) creates tree idempotent [ ] RegisterHandlers wired, GET /api/peering/identity 200 stub [ ] go build

### [PEER-02] Ed25519 identity: keypair, Vula ID, import/export
`done` · P0 · M · dep: PEER-01 · parallel: yes — backend/services/peering/identity.go
Scope: Gen Ed25519 first boot (priv 0600), Vula ID vula:ed25519:<base58> encode/decode, parse <id>@<server>:<port>; GET identity, POST identity/export(enc)/import.
AC: [ ] first boot persists, reload same [ ] GET returns Vula ID+pubkey [ ] export→import same ID [ ] unit test encode/parse

### [PEER-03] Signed canonical-JSON envelope (sign+verify)
`done` · P0 · M · dep: PEER-02 · parallel: yes — backend/services/peering/envelope.go
Scope: Deterministic canonical JSON (no sig field in signed bytes), Sign/Verify for message/contact-request/signaling/feed.
AC: [ ] canonical byte-stable [ ] Verify rejects tamper+wrong key [ ] unit tests

### [PEER-04] S2S HTTP client + signature/allow-list inbound middleware
`todo` · P0 · M · dep: PEER-03, PEER-06 · parallel: no — backend/services/peering/transport.go, inbound.go
Scope: Outbound signed POST to peer /api/peering/inbound/* (TLS, timeout, SSRF guard reuse webproxy isPrivate); inbound mw verifies sig + allow list (except inbound/request) → 401/403.
AC: [ ] mw rejects unsigned 401 [ ] non-approved 403 (except request) [ ] outbound refuses private+timeout [ ] table tests states

### [PEER-05] Peering WS multiplex channel (browser↔own server)
`todo` · P0 · M · dep: PEER-01 · parallel: yes — new backend/services/peering/ws.go, src/core/usePeering.js
Scope: WS /api/peering/stream (wsutil.Upgrader), per-user register, channel-tagged frames (message/signal/collab/notification/presence), server Push(); frontend usePeering() reconnect.
AC: [ ] browser opens stream, Push delivers [ ] channel discriminator, multi-sub [ ] hook reconnects backoff

### [PEER-06] Contacts store: allow list, permissions, persistence
`todo` · P0 · M · dep: PEER-02 · parallel: yes — backend/services/peering/contacts.go
Scope: in-mem + contacts.json: add/list/update/remove, state pending/approved/blocked, per-contact perms (message/media/call/video), IsApproved/Can predicates.
AC: [ ] persists across restart [ ] state graph enforced [ ] Can reflects grants [ ] unit tests

### [PEER-07] Contact request/approve/block + inbound endpoint
`todo` · P0 · M · dep: PEER-04, PEER-06 · parallel: no — backend/services/peering/contacts_api.go, inbound.go
Scope: POST contacts/request (sign+send), inbound/request (store pending+notify), requests list, approve/block/delete; approve mutual+notify.
AC: [ ] request creates pending on recipient [ ] inbound/request allowed w/o approval, others require [ ] approve→approved+notify [ ] block silent drop

### [PEER-08] Peering settings + contacts/requests UI
`todo` · P1 · L · dep: PEER-07 · parallel: yes — new src/builtin/peering/Peering.jsx, src/core/AppRegistry.js (LOCKED dirty — defer reg), src/App.jsx, src/core/Settings.jsx
Scope: Builtin Peering app: Vula ID/QR, contacts list w/ state+perm toggles, pending-requests approve/block; Settings Peering section.
AC: [ ] app in launcher opens [ ] contacts+requests from API, approve/block [ ] perm toggles persist [ ] Settings section

### [PEER-09] Vula ID exchange: QR generate+scan, paste
`todo` · P2 · S · dep: PEER-08 · parallel: yes — src/builtin/peering/AddContact.jsx, package.json (QR dep)
Scope: QR of own full Vula address; Add-contact via paste or camera QR scan → POST contacts/request.
AC: [ ] own address QR scannable [ ] paste/scan triggers request [ ] malformed rejected

### [PEER-10] vulos.org email verification (send/confirm + token)
`todo` · P1 · M · dep: PEER-02 · parallel: yes — backend/services/peering/verify.go
Scope: Call vulos.org verify/send+confirm (configurable base URL), store signed token, validate vulos.org sig; POST identity/verify+confirm, VerifiedEmail().
AC: [ ] verify sends, confirm stores token [ ] token sig validated [ ] unverified still works [ ] base URL configurable

### [PEER-11] Profile model: fields, avatar resize, visibility
`todo` · P1 · M · dep: PEER-02 · parallel: yes — backend/services/peering/profile.go
Scope: Profile store + GET/PUT profile, POST profile/image (resize 256² WebP), GET profile/image (ETag, visibility-gated); visibility resolver.
AC: [ ] avatar resized WebP at path [ ] image honors ETag+visibility [ ] fields persist w/ default visibility

### [PEER-12] Peer profile fetch/sync + well-known endpoint
`todo` · P1 · M · dep: PEER-11, PEER-07 · parallel: no — backend/services/peering/profile.go, new wellknown.go, main.go
Scope: Unauth GET /.well-known/vula-id (public fields+verified+endpoints placeholder) at root mux; GET /api/peering/profile/:vula_id fetch+cache; profile-changed push.
AC: [ ] well-known no auth public only [ ] peer profile cached, respects visibility [ ] approve triggers fetch

### [PEER-13] Email/directory discovery lookup
`todo` · P3 · S · dep: PEER-10 · parallel: yes — new backend/services/peering/discovery.go, src/builtin/peering/AddContact.jsx
Scope: GET /api/peering/discover?email/name proxy to vulos.org verify/lookup + optional directory → Vula ID+server; configurable.
AC: [ ] email lookup resolves when opted-in [ ] name search returns matches [ ] graceful empty

### [PEER-14] S2S text message delivery (send+inbound+store)
`todo` · P0 · L · dep: PEER-04, PEER-05 · parallel: no — new backend/services/peering/messages.go, inbox.go, inbound.go
Scope: create→sign→deliver peer inbound/message; inbound verify+store ~/.vulos/peering/inbox/<conv>/, push message frame; conversations list+history.
AC: [ ] msg to approved peer stored their inbox [ ] inbound rejects non-approved/bad sig [ ] list+history persist [ ] recipient gets realtime frame

### [PEER-15] Offline queue: outbox, retry/backoff, ACK, reconnect sync
`todo` · P1 · M · dep: PEER-14 · parallel: yes — new backend/services/peering/outbox.go
Scope: Persist unacked outbox, retry 1s/5s/30s/5m/1h then periodic, ACK removes, reconnect pull since last-seen.
AC: [ ] unreachable stays+retried [ ] ACK removes [ ] online peer pulls missed

### [PEER-16] Media transfer: upload, hash ref, S2S fetch, thumbnails
`todo` · P1 · L · dep: PEER-14 · parallel: yes — new backend/services/peering/media.go
Scope: media store ~/.vulos/peering/media/, upload→hash+signed URL, S2S fetch on inbound refs, image/video thumbnails.
AC: [ ] upload→stable hash+signed URL [ ] recipient fetches own copy post-offline [ ] thumbnails [ ] signed URL rejects tamper/expire

### [PEER-17] Inbox UI: conversations, thread, composer, media
`todo` · P1 · L · dep: PEER-14, PEER-16, PEER-08 · parallel: yes — new src/builtin/peering/Messages.jsx, src/core/usePeering.js
Scope: Messages view: conversation list, thread, composer, drag media, live message channel, contact profile.
AC: [ ] conversations+threads from API [ ] text+media end-to-end [ ] incoming realtime no refresh

### [PEER-18] Groups/rooms: definition, membership, fan-out
`todo` · P2 · M · dep: PEER-14 · parallel: yes — new backend/services/peering/groups.go
Scope: group create/list/add-member, store ~/.vulos/peering/groups/, fan-out via PEER-14+PEER-15, signed+verified per recipient.
AC: [ ] create distributes def to members [ ] group msg delivered each member [ ] add-member policy-gated propagates

### [PEER-19] Call signaling relay (S2S SDP/ICE)
`todo` · P0 · M · dep: PEER-04, PEER-05 · parallel: no — new backend/services/peering/call.go, inbound.go
Scope: call lifecycle relay: initiate→peer inbound/signal→callee frame; answer/reject/hangup; signal relays opaque SDP/ICE via signal channel; servers no media.
AC: [ ] initiate → callee incoming-call frame [ ] SDP/ICE relay end-to-end [ ] reject/hangup terminates both [ ] rejected for non-call contacts

### [PEER-20] Bandwidth measurement + /api/peering/bandwidth
`todo` · P1 · M · dep: PEER-01 · parallel: yes — new backend/services/peering/bandwidth.go
Scope: periodic speed test (configurable endpoint) or traffic estimate, cache, GET bandwidth, peer-query path.
AC: [ ] returns up/down+latency periodic [ ] peer can request approved peer's [ ] non-blocking startup

### [PEER-21] STUN/TURN ICE config endpoint for peering calls
`todo` · P0 · S · dep: PEER-19 · parallel: yes — new backend/services/peering/ice.go (reuse network/turn.go)
Scope: GET /api/peering/ice → STUN list + TURN short-lived creds (reuse network.TURNConfig.GenerateCredentials) when TURN_SECRET set.
AC: [ ] STUN always, TURN creds when secret set [ ] short-lived HMAC [ ] no new TURN code

### [PEER-22] 1:1 voice call (browser↔browser WebRTC audio)
`todo` · P0 · L · dep: PEER-19, PEER-21, PEER-05 · parallel: yes — new src/builtin/peering/call/useWebRTCCall.js, CallView.jsx
Scope: RTCPeerConnection audio, getUserMedia, offer/answer+ICE over signal channel w/ PEER-21 config, mute, hangup; wire call UI.
AC: [ ] 2 browsers direct audio via signaling only [ ] mute/hangup, media not via servers [ ] ICE-restart on drop

### [PEER-23] 1:1 video + screen sharing
`todo` · P1 · M · dep: PEER-22 · parallel: yes — src/builtin/peering/call/
Scope: video track 2-layer simulcast, camera on/off, getDisplayMedia screen-share swap, PiP, quality indicator (getStats).
AC: [ ] video call toggleable camera [ ] screen share add/stop [ ] quality+PiP

### [PEER-24] Incoming-call UI, ring, call history
`todo` · P1 · M · dep: PEER-22, PEER-08 · parallel: yes — new src/builtin/peering/call/IncomingCall.jsx, backend/services/peering/callhistory.go
Scope: shell-wide incoming-call modal on signal call-request + ringtone; backend call-history + GET endpoint + UI panel.
AC: [ ] modal regardless of focus [ ] accept/reject drives signaling [ ] completed/missed recorded+listed

### [PEER-25] Pre-call lobby: bandwidth, host select, capacity
`todo` · P2 · M · dep: PEER-20, PEER-22 · parallel: yes — new src/builtin/peering/call/Lobby.jsx, backend/services/peering/call.go
Scope: collect bandwidth reports, table, volunteer SFU host, capacity estimate from host upload per formula.
AC: [ ] lists ▲up▼down latency [ ] host dropdown defaults initiator, updates capacity [ ] estimate matches math

### [PEER-26] Mesh group calls (3–4 full-mesh)
`todo` · P2 · L · dep: PEER-22, PEER-25 · parallel: yes — new src/builtin/peering/call/useMeshCall.js, CallView.jsx
Scope: multiple RTCPeerConnections full mesh, per-peer signaling, grid, SFU-recommend guard when low bandwidth.
AC: [ ] 3–4 mesh A/V call [ ] join/leave updates mesh no drop [ ] low-bw triggers SFU prompt

### [PEER-27] Pion SFU on host (forward, simulcast, Last-N)
`todo` · P2 · L · dep: PEER-19, PEER-21 · parallel: no — new backend/services/peering/sfu/room.go, sfu.go
Scope: Pion SFU room: N PCs, accept 2-layer simulcast, forward selected layer per receiver, Last-N (4/6/9), join/leave; 5+ routes through host SFU.
AC: [ ] 5+ routes through SFU [ ] simulcast received+forwarded per receiver [ ] Last-N limits [ ] no transcoding

### [PEER-28] SFU dominant speaker + audio mixing (top 3)
`todo` · P3 · M · dep: PEER-27 · parallel: yes — new backend/services/peering/sfu/audio.go, room.go
Scope: VAD/audio-level detection, dominant→high simulcast layer, mix top-3 audio per participant excl self.
AC: [ ] dominant gets high layer [ ] ≤3 audio streams per participant [ ] never hears self

### [PEER-29] SFU host handoff + 50-participant cap
`todo` · P3 · M · dep: PEER-27, PEER-25 · parallel: no — new backend/services/peering/sfu/handoff.go, src/builtin/peering/call/useSFUCall.js
Scope: detect host loss, auto-select highest-upload new host (PEER-20/25 data), orchestrate browser reconnect, enforce cap 50.
AC: [ ] kill host → failover best-bw [ ] resumes few sec no full drop [ ] 51st rejected

### [PEER-30] Yjs collab transport: sync WS + awareness
`todo` · P2 · L · dep: PEER-05, PEER-14 · parallel: no — new backend/services/peering/collab.go, inbound.go, src/core/useYDoc.js, package.json (yjs)
Scope: store Yjs doc binaries+meta, relay opaque CRDT blobs S2S, broadcast updates+awareness on collab channel; useYDoc(docId) hook.
AC: [ ] 2 browsers same doc merge realtime [ ] awareness broadcasts+clears on disconnect [ ] yjs state persists

### [PEER-31] Document share/accept + per-peer permissions
`todo` · P2 · M · dep: PEER-30 · parallel: yes — backend/services/peering/collab.go, new src/builtin/peering/ShareDialog.jsx
Scope: doc-share invitation send/recv, accept adds w/ Shared badge, edit/view enforce (view recv-only), owner revoke, documents list/leave.
AC: [ ] share→invitation, accept registers [ ] view-only sends rejected [ ] revoke stops updates

### [PEER-32] Collaboration in Docs (TipTap + y-tiptap)
`todo` · P3 · M · dep: PEER-30, PEER-31 · parallel: yes — Docs/Notes app (apps/notes/), src/core/useYDoc.js
Scope: wire Docs/Notes editor to useYDoc via y-tiptap, shared-doc badge, remote cursors from awareness, Share entry point.
AC: [ ] 2 users co-edit live merge [ ] remote cursors name+color [ ] Share grants access

### [PEER-33] Collab in Sheets/Notes/Text Editor + offline state-vector
`todo` · P3 · L · dep: PEER-32 · parallel: yes — apps/text-editor/, Sheets app, backend/services/peering/collab.go
Scope: Sheets y-json, Notes, Text Editor CodeMirror/Monaco binding; reconnect catch-up via state vectors GET inbound/collab-sync.
AC: [ ] Sheets+TextEditor live multi-user [ ] offline reconnect gets only diff [ ] time-travel from history

### [PEER-34] Drop: mDNS LAN discovery + nearby + send/accept
`todo` · P2 · L · dep: PEER-07, PEER-16 · parallel: no — new backend/services/peering/drop.go, src/builtin/peering/Drop.jsx
Scope: mDNS advertise/browse _vula-drop._tcp, nearby endpoint, discoverability everyone/peers/nobody, send (LAN else internet), inbound drop accept/decline+auto-accept-contact; Drop UI tiles+progress.
AC: [ ] 2 LAN instances discover when discoverable [ ] discoverability filters ads [ ] drop transfers+accept/decline+progress

### [PEER-35] Drop: proximity code (gen/redeem + rendezvous)
`todo` · P3 · M · dep: PEER-34 · parallel: yes — backend/services/peering/drop.go, src/builtin/peering/Drop.jsx
Scope: 6-digit code TTL 5min/single-use, stateless vulos.org rendezvous fallback (configurable) when no mDNS, then normal transfer.
AC: [ ] code 6-digit expires 5min/first use [ ] valid code connects+transfers [ ] works cross-network via rendezvous

### [PEER-36] Drop: BLE advertise/scan for bare-metal
`todo` · P3 · M · dep: PEER-34 · parallel: yes — new backend/services/peering/drop_ble.go, backend/go.mod
Scope: BLE advertise service UUID + truncated Vula ID hash w/ rotation, scan to surface devices into nearby; clean no-op w/o BLE hw.
AC: [ ] advertises vula-drop BLE when discoverable [ ] scan surfaces devices [ ] payload rotates, no hw=no-op

### [PEER-37] E2E encryption: X25519 + XChaCha20-Poly1305
`todo` · P1 · L · dep: PEER-14 · parallel: yes — new backend/services/peering/crypto.go
Scope: X25519 from identity, per-conversation shared secret, encrypt/decrypt message bodies+CRDT payloads XChaCha20-Poly1305 transparently; servers store ciphertext only.
AC: [ ] bodies ciphertext at rest+transit, only endpoints decrypt [ ] wrong key fails closed [ ] round-trip+key exchange tests

### [PEER-38] Relay peers: deposit/pickup/ack + config/store
`todo` · P3 · L · dep: PEER-37, PEER-15 · parallel: yes — new backend/services/peering/relay.go
Scope: relay role config (enabled/capacity/TTL/allowed), deposit (mutual-trust+limits), signed pickup, ack-delete; sender uses relay when recipient unreachable. Limits 100MB/recip, 72h, 25MB blob, 100/h.
AC: [ ] deposit stores by recipient, relay never decrypts [ ] signed pickup returns, ack deletes [ ] limits enforced, mutual-trust only

### [PEER-39] Relay attestation: verify TEE before send
`todo` · P3 · M · dep: PEER-38 · parallel: yes — new backend/services/peering/relay_attest.go
Scope: relay exposes attestation doc; sender validates vs policy before deposit, pluggable verifier (start AWS Nitro), strict reject-on-failure.
AC: [ ] sender verifies attestation before deposit [ ] failed/absent rejects relay [ ] verifier interface extensible

### [PEER-40] Cluster anycast: multi-endpoint registry + failover
`todo` · P3 · M · dep: PEER-12, PEER-14 · parallel: no — new backend/services/peering/endpoints.go, wellknown.go, transport.go
Scope: endpoint registry (register/list/remove/priority), include endpoints in well-known (extends PEER-12), outbound races cached endpoints w/ failover, UUIDv7 dedup inbound.
AC: [ ] Vula ID advertises multi endpoints [ ] delivery succeeds via live one [ ] duplicate msg ID no-op

### [PEER-41] Signed feeds: append-only log, pub/sub, content-addr
`todo` · P3 · L · dep: PEER-03, PEER-12 · parallel: yes — new backend/services/peering/feeds.go
Scope: feed create/list/publish/get/entries, hash-chained signed entries (prev_hash), access public/peers/link, subscriber pull by seq, push to approved, content hash sha256(canonical).
AC: [ ] publish appends chained signed entry [ ] tamper breaks chain verify [ ] public/link no auth, peers gated [ ] subscribers since last seq

---

## AUTH / FED / MOBILE / LADYBIRD (roadmap/future/*) — backlog default P3 unless foundational

### [AUTH-01] TOTP secret store and code generation backend
`done` · P2 · M · dep: none · parallel: yes — new backend/services/authvault/ (go.mod: pquerna/otp)
Scope: RFC6238 TOTP; AES-256-GCM secrets at ~/.vulos/auth/totp/keychain.enc + accounts.json; otpauth:// parse; add/list/code/delete struct methods (no HTTP).
AC: [x] merged 43d65d3 (11 tests incl RFC6238 vectors pass)

### [AUTH-02] TOTP HTTP API endpoints
`done` · P2 · S · dep: AUTH-01 · parallel: no — backend/cmd/server/main.go, new authvault/handlers.go
Scope: POST /api/auth/totp/add, GET /list, GET /code/:id, DELETE /:id; scoped per X-User-ID; wire in main.go.
AC: [ ] 4 endpoints, auth-required, correct JSON [ ] add-then-code returns valid 6-digit [ ] go build

### [AUTH-03] TOTP UI panel (authenticator overlay)
`todo` · P2 · M · dep: AUTH-02 · parallel: yes — new src/apps/Authenticator/, src/core/AppRegistry.js
Scope: React list of accounts w/ rolling codes + 30s countdown, tap-to-copy, add-account (paste otpauth URI / manual). Register in AppRegistry.
AC: [ ] codes refresh 30s no reload [ ] click-copy w/ confirm [ ] add posts /totp/add [ ] in launcher

### [AUTH-04] Google Authenticator import/export for TOTP
`todo` · P3 · M · dep: AUTH-01, AUTH-02 · parallel: yes — new authvault/migration.go
Scope: decode otpauth-migration:// protobuf → entries; POST /import + /export (encrypted blob).
AC: [ ] sample migration imports all [ ] export re-imports identical [ ] unit test protobuf parse

### [AUTH-05] Credential vault store (encrypted password manager backend)
`done` · P2 · L · dep: none · parallel: yes — new backend/services/credvault/
Scope: AES-256-GCM vault.enc, Argon2id master key, lock/unlock state machine + auto-lock, entry CRUD (url/user/pass/notes/totp-id), password generator. Library only.
AC: [ ] vault opaque, wrong pwd fails [ ] lock clears key, inaccessible [ ] generator random+passphrase [ ] go test encrypt/decrypt round-trip

### [AUTH-06] Credential vault HTTP API
`todo` · P2 · M · dep: AUTH-05 · parallel: no — backend/cmd/server/main.go
Scope: POST /api/auth/vault/unlock|lock, GET /entries (metadata), GET /entry/:id, POST/PUT/DELETE /entry, POST /generate; 423 when locked.
AC: [ ] list metadata only, detail requires unlock [ ] locked → clear error [ ] go build

### [AUTH-07] Password manager UI
`todo` · P3 · M · dep: AUTH-06 · parallel: yes — new src/apps/Vault/, src/core/AppRegistry.js
Scope: master-pwd unlock screen, entry list/search, detail reveal/copy, add/edit, generator, auto-relock.
AC: [ ] unlock gates list [ ] copy user/pass w/ confirm [ ] CRUD persists [ ] generator inserts into form

### [AUTH-08] Credential vault import (Bitwarden/1Password/KeePass/Chrome)
`todo` · P3 · M · dep: AUTH-05, AUTH-06 · parallel: yes — new credvault/import.go
Scope: parsers for 4 formats → vault entries; POST /import + /export; dedupe url+username.
AC: [ ] 4 formats import in unit tests [ ] export re-imports equivalent [ ] dupes merged

### [AUTH-09] TPM/software-keystore abstraction for key sealing
`done` · P1 · L · dep: none · parallel: yes — new backend/services/devicekey/ (go.mod: go-tpm)
Scope: KeyStore iface Seal/Unseal/Sign/DeviceIdentity; go-tpm tpm2 vs /dev/tpmrm0, software-encrypted fallback ~/.vulos/auth/tpm/; report backend type.
AC: [ ] software fallback round-trips w/o TPM [ ] tpm/status reports type [ ] stable device identity [ ] builds+tests w/o hw TPM

### [AUTH-10] Device identity & TPM status API
`todo` · P2 · S · dep: AUTH-09 · parallel: no — backend/cmd/server/main.go
Scope: GET /api/auth/device/identity, /tpm/status, POST /seal, /unseal (admin-only seal/unseal).
AC: [ ] tpm/status returns backend type [ ] seal→unseal returns original [ ] go build

### [AUTH-11] Client certificate (mTLS) store + management API
`todo` · P2 · M · dep: AUTH-09 · parallel: yes — new backend/services/clientcerts/
Scope: per-domain X.509 cert+key under ~/.vulos/auth/certificates/<domain>/ (key sealed via AUTH-09), CSR gen; install/list/delete/status/generate-csr endpoints.
AC: [ ] install+list shows issuer/expiry [ ] CSR valid PEM w/ CN/SAN [ ] key sealed not plaintext [ ] unit test install+status

### [AUTH-12] Server-side passkey (FIDO2) authenticator + API
`todo` · P2 · L · dep: AUTH-09 · parallel: yes — new backend/services/passkeys/
Scope: server-resident FIDO2 (go-webauthn): create/store credentials per RP sealed via AUTH-09, assertions, list/delete; passkeys endpoints.
AC: [ ] register persists sealed, listed [ ] reg+assertion verifies in test [ ] delete removes [ ] go build

### [AUTH-13] WebAuthn bridge data channel (server side)
`todo` · P3 · M · dep: none · parallel: no — backend/services/stream/stream.go, new webauthn.go
Scope: add `case "webauthn"` to OnDataChannel switch routing challenge/assertion to per-session relay + Go hook. Browser-side out of scope.
AC: [ ] webauthn channel accepted bidirectional [ ] round-trips via relay in test [ ] input channels unaffected, go build

### [AUTH-14] SMS receive via VoIP provider webhook
`done` · P3 · M · dep: none · parallel: yes — new backend/services/smsotp/
Scope: POST /api/auth/sms/webhook (Twilio form) extract OTP regex, store ~/.vulos/auth/sms/history.json 24h, notify; recent/number/settings endpoints.
AC: [ ] Twilio payload stores+notifies [ ] OTP regex on real samples in test [ ] >24h pruned

### [FED-01] ActivityPub social app scaffold (read-only public timeline)
`done` · P3 · M · dep: none · parallel: yes — new apps/social/, src/core/AppRegistry.js (defer reg if AppRegistry contended)
Scope: social app (manifest+server+UI), read-only: enter instance host, GET /api/v1/timelines/public, render statuses. No auth/posting.
AC: [ ] app.json validates [ ] public host renders timeline [ ] launches

### [FED-02] OAuth2 login to existing Mastodon/Pixelfed
`done` · P3 · M · dep: FED-01 · parallel: yes — apps/social/
Scope: dynamic client reg /api/v1/apps, OAuth2 code flow, token store, home timeline, verify-credentials.
AC: [ ] login real instance returns token [ ] home timeline renders [ ] token persists, logout clears

### [FED-03] Feed interactions — post/boost/favourite/reply
`todo` · P3 · M · dep: FED-02 · parallel: yes — apps/social/
Scope: compose (500 char + CW), thread view, boost/fav/reply/bookmark optimistic UI vs Mastodon API.
AC: [ ] post appears in home [ ] fav/boost persist server-side [ ] reply opens thread in-reply-to

### [FED-04] Photos + Video views (Pixelfed grid, PeerTube HLS)
`todo` · P3 · M · dep: FED-02 · parallel: yes — apps/social/
Scope: photo grid + fullscreen viewer + carousel; video list inline HLS (hls.js) + reply comments.
AC: [ ] photo grid + fullscreen [ ] multi-attachment carousel [ ] video plays inline HLS

### [FED-05] Forums view — Lemmy communities
`todo` · P3 · M · dep: FED-01 · parallel: yes — apps/social/
Scope: Lemmy API client: communities, post listing w/ sort, comment trees, vote/subscribe (JWT), read-only fallback.
AC: [ ] browse public Lemmy communities/posts [ ] sort hot/new/top/active [ ] logged-in vote+comment

### [FED-06] Push notifications + share-to-Fediverse
`todo` · P3 · S · dep: FED-02, FED-03 · parallel: yes — apps/social/, one notifySvc call
Scope: Mastodon streaming WS → POST /api/notifications/send on mention; share target → compose prefilled.
AC: [ ] new mention triggers Vula notification [ ] share opens compose prefilled

### [MOBILE-01] Telephony service scaffold (Go + WS + D-Bus ModemManager)
`todo` · P3 · M · dep: none · parallel: yes — new backend/services/telephony/, apps/phone/app.json, main.go
Scope: HTTP server + WS hub + ModemManager D-Bus client enumerate modems/signal/SIM; graceful no-modem fallback.
AC: [ ] status endpoint lists modems (empty when none) [ ] WS connects/stays open [ ] no panic w/o D-Bus [ ] go build

### [MOBILE-02] ModemManager SMS send/receive + SQLite history
`todo` · P3 · M · dep: MOBILE-01 · parallel: yes — backend/services/telephony/
Scope: SMS send/list/delete via D-Bus Messaging, incoming-signal listener → WS push, SQLite thread-grouped history, search; mockable D-Bus.
AC: [ ] send/list/delete exposed [ ] incoming (mocked) persisted+pushed [ ] thread-grouped query

### [MOBILE-03] ModemManager voice calls (dial/answer/hangup/DTMF)
`todo` · P3 · M · dep: MOBILE-01 · parallel: yes — backend/services/telephony/
Scope: voice control via D-Bus Voice, call-state listener → WS; audio path excluded.
AC: [ ] dial/answer/hangup/DTMF exposed [ ] call-state (mocked) pushes WS [ ] no hw to build/test

### [MOBILE-04] Messages + Dialer React UI
`todo` · P3 · L · dep: MOBILE-02, MOBILE-03 · parallel: yes — apps/phone/
Scope: Messages (thread list/compose/search/status) + Dialer (T9, history, in-call screen, incoming banner) consuming MOBILE WS.
AC: [ ] Messages lists threads, send/recv WS [ ] Dialer places call + in-call screen [ ] incoming surfaces realtime

### [MOBILE-05] eSIM profile management via lpac
`todo` · P3 · M · dep: MOBILE-01 · parallel: yes — backend/services/telephony/, apps/phone/
Scope: lpac CLI wrapper list/enable/disable/delete/add-by-code, endpoints, eSIM manager UI; graceful w/o lpac.
AC: [ ] list/enable/disable/delete (mock lpac) [ ] add-by-activation-code [ ] UI lists+toggles [ ] missing lpac clear error

### [MOBILE-06] Responsive / device-profile-aware UI shell
`todo` · P3 · M · dep: none · parallel: no — src/ shell/layout (overlaps DEVPROF-03)
Scope: useDeviceProfile hook (viewport+override), responsive breakpoints collapse desktop→mobile single-column, notification behavior stub per profile. (coordinate w/ DEVPROF-03 — share hook)
AC: [ ] useDeviceProfile updates on resize [ ] mobile single-column at narrow [ ] profile overridable

### [LADYBIRD-01] Ladybird headless engine spike behind Settings toggle
`todo` · P3 · L · dep: none · parallel: yes — new backend/services/ladybird/, backend/services/webbrowser/chrome.go, main.go
Scope: guarded experimental Ladybird launcher via stream.Pool, feature-flagged, fallback to Chromium when absent; no Chromium/Xvfb removal; log engine in /api/browser/status.
AC: [ ] toggle off = Chromium unchanged [ ] no binary → clean Chromium fallback [ ] engine logged + in /api/browser/status [ ] go build

<!-- END-BACKLOG -->

