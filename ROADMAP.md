# Vula OS — Roadmap

## Upcoming

### Browser
- [ ] Upgrade to Chromium-based rendering engine
- [ ] Improve browser compatibility and web standards support
- [ ] Tab management and session restore
- [ ] Extension support
- [ ] Better scrolling on mobile and desktop — smooth scroll passthrough, momentum scrolling, touch gesture support

### Terminal
- [ ] Add full bash terminal with shell history
- [ ] Improved PTY handling and resize support
- [ ] Custom themes and font configuration

### Default Applications
- [ ] Calculator
- [ ] Calendar
- [ ] Music player
- [ ] Video player
- [ ] Image editor
- [ ] Text editor with syntax highlighting
- [ ] Email client
- [ ] Contacts

### Theming & Display
- [ ] Night Shift — auto-adjust colour temperature during evening/night hours (warmer tones at sunset, cool at sunrise)
- [ ] Wallpaper customization — user-uploaded backgrounds, dynamic wallpapers
- [ ] Accent colour picker — let users choose system accent colour beyond blue

### Improve Existing Apps
- [ ] File Manager — drag-and-drop, bulk operations, preview pane
- [ ] Notes — rich text editing, markdown support, tagging
- [ ] Gallery — albums, slideshow, basic editing
- [ ] Activity Monitor — graphs, process management, resource alerts
- [ ] Settings — more configuration options

### Security
- [ ] Full security audit of all backend services
- [ ] Verify sandbox isolation is safe for untrusted code
- [ ] Review auth middleware for edge cases and bypasses
- [ ] Dependency vulnerability scanning in CI
- [ ] Container image scanning for CVEs
- [ ] Rate limiting and abuse prevention review

### Tunnel Performance
- [ ] Switch WebSocket library from `x/net/websocket` to `gorilla/websocket` with `permessage-deflate` compression (PTY, notify, telemetry) — ~70-80% bandwidth reduction on terminal output
- [ ] Coalesce PTY output into 16ms frames — fewer packets over tunnel, smoother scrolling during heavy output

### App Streaming Layer

The current browser service (`browser.go`) has Chromium launch flags, Xvfb setup, GStreamer capture, and WebRTC transport all hardcoded together. This needs to become a generic streaming layer that any graphical app can plug into.

Every remote-rendered app needs the same three things: a framebuffer source, input injection, and WebRTC transport. Only the first two change per app — encoding, WebRTC negotiation, ICE, and data channels are always identical.

```
StreamBackend interface:
    Start(width, height)        — launch the app
    Stop()
    FrameSource()               — where GStreamer reads pixels from
    SendInput(event)            — mouse/keyboard injection
```

**Backends:**

- **XvfbBackend** — for anything that renders to X11 (Chromium, Wine/Lutris games, any Linux GUI app like GIMP, Blender, LibreOffice)
- **FramebufferBackend** — for apps that output a raw bitmap directly (Ladybird headless)
- **VNCBackend** — for streaming a VNC source

One shared GStreamer/WebRTC pipeline. Adding a new streamable app = writing a new backend (~50 lines), not duplicating the entire 700-line browser service.

**Implementation steps:**

- [ ] Extract the GStreamer/WebRTC pipeline from `browser.go` into a shared `stream` package with the `StreamBackend` interface
- [ ] Implement `XvfbBackend` — refactor current Chromium code into this backend (Xvfb + ximagesrc capture + socat input)
- [ ] Implement `FramebufferBackend` — direct `appsrc` bridge for apps that provide raw bitmaps
- [ ] Implement `VNCBackend` — connect to a VNC source, decode, feed into the shared pipeline
- [ ] Refactor `browser.go` to use `XvfbBackend` under the shared stream layer (no behaviour change, just cleaner architecture)
- [ ] Expose generic app streaming — let users stream any installed Linux GUI app through the OS

#### Ladybird Browser Engine (Long-term)

Replace Chromium with Ladybird's LibWeb as the remote browser engine. Chromium idles at 300-500MB and every frame copies three times (compositor → X11 shared memory → GStreamer capture). Ladybird's headless WebContent renderer outputs directly to an offscreen framebuffer, enabling a zero-copy path into GStreamer's `appsrc` — encoding only when the page changes instead of polling a display.

| | Chromium + Xvfb | Ladybird headless |
|---|---|---|
| Idle RAM | 300-500MB | ~50-80MB |
| Frame path | 3 copies (Chromium → X11 → GStreamer) | 1 copy, zero-copy possible |
| Startup | 2-5s | <1s |
| X11 dependency | Required | None |

**Why Ladybird can be faster than Chromium for this use case:**

Chromium is optimised for running locally on a user's machine with a real GPU and display — its multi-process compositor, GPU acceleration pipeline, and sandbox architecture are overhead when we're just capturing pixels off a virtual display. Ladybird has none of that baggage. Specifically:

- **No compositor overhead** — Chromium composites layers through its GPU process even on Xvfb where there's no real GPU. Ladybird's LibWeb renders the final bitmap directly in the WebContent process — one step, no inter-process pixel shuffling.
- **Event-driven frame delivery** — Chromium renders at a fixed refresh rate regardless of page activity. Ladybird's headless mode can signal "frame ready" only when the bitmap actually changes. Combined with the `FramebufferBackend`, this means zero wasted encodes on static pages — significant CPU and bandwidth savings, especially over a tunnel.
- **Smaller attack surface, fewer processes** — Chromium spawns GPU, renderer, utility, and network processes even headless. Ladybird's headless mode is a single WebContent process plus a RequestServer. Fewer processes = less context switching, less memory, faster startup.
- **No X11 round-trips** — Every mouse click and scroll in the Chromium pipeline goes: WebRTC data channel → Go server → socat → X11 → Chromium's input handling. With Ladybird, input goes directly into LibWeb's event system — cutting out X11 entirely. This shaves 2-5ms off every input event, which compounds into noticeably snappier scrolling and interaction.
- **Docker image size** — Removing Chromium (~400MB), Xvfb, and X11 libraries from the image is a major reduction. Ladybird's headless binary is a fraction of that.

The net effect: for a tunneled web OS where every millisecond and megabyte matters, Ladybird's architecture is closer to purpose-built for this than Chromium will ever be.

**Implementation steps:**

- [ ] Track Ladybird headless mode API — currently used for WPT tests, needs stable framebuffer access
- [ ] Prototype: render a page via `FramebufferBackend`, pipe into the shared stream layer
- [ ] Implement event-driven frame signalling — only encode when LibWeb marks the bitmap as dirty
- [ ] Implement Ladybird input injection — pipe events directly into LibWeb's event system, bypassing X11
- [ ] Port ad blocker and profile isolation to LibWeb's content filtering
- [ ] Run both engines side-by-side during transition (user toggle in Settings)
- [ ] Remove Xvfb/Chromium/X11 deps from Docker image once Ladybird is viable

**Blocked on:** Ladybird alpha (targeting late 2026). Web compat limits real-world use until 2027+.

#### GPU Acceleration Tiers

The stream layer should detect available GPU hardware at startup and automatically select the best rendering and encoding path. All tiers use the same `XvfbBackend` and WebRTC pipeline — only the Vulkan ICD (driver) and GStreamer encoder element change.

**Tier 1 — No GPU (software only):**

For any VPS, container, or device without a GPU. Wine's GDI rendering paints directly to X11 with no GPU needed. Software VP8 encoding. Covers Windows productivity apps, retro games, older titles, and any non-3D software. This is the baseline — it runs everywhere.

```
App → GDI/software rendering → Xvfb → GStreamer ximagesrc → VP8 (software) → WebRTC
```

| | Performance |
|---|---|
| Encode latency | 10-20ms (VP8 software) |
| Frame rate | 30fps |
| 3D support | None (lavapipe fallback is too slow for real games) |
| RAM overhead | Low (~50-100MB for Wine + app) |
| Use cases | Windows apps, office software, retro/2D games |

**Tier 2 — Shared host GPU (VirGL/Venus):**

The container uses the host GPU without full passthrough via VirGL (OpenGL) or Venus (Vulkan). Multiple containers share one GPU. No device passthrough needed — works with standard container runtimes. DXVK translates DirectX to Vulkan, Venus sends Vulkan calls to the host GPU over a virtio channel.

```
App → DXVK → Venus (Vulkan over virtio) → host GPU → Xvfb → GStreamer → H.264 (VA-API) → WebRTC
```

| | Performance |
|---|---|
| Encode latency | <2ms (VA-API hardware) |
| Frame rate | 60fps |
| 3D support | ~70-80% of native GPU performance |
| GPU requirement | Host GPU with VirGL/Venus support |
| Use cases | Modern games, 3D apps, multiple users sharing one GPU |

**Tier 3 — GPU passthrough (full hardware):**

Direct GPU passthrough to the container. DXVK renders on the actual GPU, DMA-BUF captures frames without copying through X11, hardware NVENC/VA-API encodes. Near-native performance. This is what cloud gaming services (GeForce Now, Xbox Cloud Gaming) use.

```
App → DXVK → real GPU (Vulkan) → DMA-BUF → GStreamer → H.264/AV1 (NVENC/VA-API) → WebRTC
```

| | Performance |
|---|---|
| Encode latency | <1ms (NVENC) |
| Frame rate | 60-120fps |
| 3D support | Native GPU performance |
| GPU requirement | Dedicated GPU passed through to container |
| Use cases | AAA games, competitive gaming, GPU compute |

**Implementation steps:**

- [ ] GPU detection at startup — probe for VA-API, NVENC, VirGL, Venus; set a capability flag per tier
- [ ] GStreamer encoder auto-selection — `nvh264enc` (Tier 3) → `vaapih264enc` (Tier 2) → `vp8enc` (Tier 1) fallback chain
- [ ] DMA-BUF capture path for Tier 3 — bypass `ximagesrc`, use `kmssrc` or DMA-BUF import directly into the encoder
- [ ] Venus/VirGL container setup — document and script the host-side GPU sharing configuration
- [ ] Expose GPU tier in Settings and telemetry — so users know what's available and apps can adapt (e.g. skip launching a heavy game on Tier 1)
- [ ] AV1 hardware encode support — `nvav1enc` / `vaav1enc` as a future option for better quality at lower bitrate
- [ ] Adaptive bitrate — adjust encode quality based on network conditions reported by WebRTC stats

#### Wine & Lutris

Wine translates Windows API calls to Linux equivalents. For graphics: DirectX 9/10/11 goes through DXVK to Vulkan, DirectX 12 goes through VKD3D to Vulkan, and GDI apps (older Windows software) render to X11 directly. All of these paint to an X11 window, which means Wine apps plug directly into the `XvfbBackend` with no extra work.

**Wine virtual desktop mode** (`--virtual-desktop WxH`) is the most efficient way to run Wine through the stream layer. It creates its own root window at a fixed resolution inside Xvfb — no window manager needed, no desktop chrome, just the app. The fixed resolution matches GStreamer's capture size exactly (no scaling artifacts), and each Wine app gets its own Xvfb display (`:1`, `:2`, etc.) for full isolation.

**Lutris** is a game launcher/manager that configures Wine prefixes, installs DXVK, downloads runner versions, and handles per-game tweaks. Rather than reimplement this, stream Lutris itself as a GUI app through the `XvfbBackend` — the user sees the full Lutris UI, manages their library, clicks Play, and the game launches in the same streaming session. The entire Lutris ecosystem comes for free.

**How it maps to GPU tiers:**

| Tier | What works | Example |
|---|---|---|
| Tier 1 (no GPU) | GDI apps, 2D games, old Windows software | MS Paint, Age of Empires II (original), WinRAR, older Office |
| Tier 2 (shared GPU) | DXVK games, modern 3D at good performance | Skyrim, Stardew Valley, Hollow Knight, Hades |
| Tier 3 (passthrough) | AAA titles, competitive, high FPS | Elden Ring, Cyberpunk, CS2 |

**Implementation steps:**

- [ ] Wine integration — install Wine in the Docker image, create and manage Wine prefixes per user (`~/.vulos/wine/`)
- [ ] Wine virtual desktop launcher — launch Wine apps with `--virtual-desktop` into a dedicated Xvfb display via `XvfbBackend`
- [ ] DXVK/VKD3D auto-install — detect GPU tier and install DXVK (Tier 2/3) or skip (Tier 1) per Wine prefix
- [ ] Lutris as a streamable app — package Lutris, stream its UI, games launch within the same session
- [ ] Game library UI — list installed Wine/Lutris games in the Vula OS shell, one-click launch into a streaming session
- [ ] Per-game settings — resolution, DXVK toggles, Wine runner version, environment variables
- [ ] Input latency optimization — shortest path from WebRTC data channel to X11 input injection, bypass socat where possible
- [ ] Gamepad support — forward gamepad events from the browser's Gamepad API through the WebRTC data channel to a virtual gamepad device (`uinput`)
- [ ] Audio passthrough — route Wine's PulseAudio output into the GStreamer Opus encode path (already used for browser audio)

**Realistic latency:**

| Tier | Glass-to-glass (local network) | Through tunnel (same region) |
|---|---|---|
| Tier 1 | ~40-60ms | ~60-80ms |
| Tier 2 | ~25-35ms | ~35-55ms |
| Tier 3 | ~15-25ms | ~25-45ms |

Tier 2/3 are playable for most games. Tier 3 approaches commercial cloud gaming quality. Tier 1 is fine for non-gaming Windows apps and retro titles.

### Multi-Distro Support

Vula OS currently runs on Alpine Linux only. The Go backend is a static binary and the React frontend runs in the browser — neither cares about the host distro. The only Alpine-specific code is the package manager service (`services/packages/packages.go`, ~200 lines of `apk` wrappers) and a handful of error messages that say "install with `apk add X`".

The system should be designed so that adding a new base OS is a checklist, not a rewrite. A new distro needs three things: a `PackageManager` implementation (~100 lines), a Dockerfile, and an entry in the package name mapping table. Everything else is shared.

**Initial targets:**

| Image | Base | Use case | Size |
|---|---|---|---|
| `vulos-alpine` | Alpine Linux (musl) | Lean server, containers, embedded, IoT | ~200MB |
| `vulos-debian` | Debian Bookworm (glibc) | Desktop, Steam, Wine, gaming, widest package ecosystem | ~400MB |

Both ship the same Go binary and React frontend. The difference is the base OS, available packages, and what works natively (glibc apps like Steam/Wine on Debian, musl-only on Alpine).

**Package manager abstraction:**

```
PackageManager interface:
    ID() string                     — "apk", "apt", "dnf", "pacman"
    List() []Package
    Search(query) []Package
    Install(name)
    Remove(name)
    Update()
    Upgrade()
    Info(name) PackageDetail
```

Auto-detect at startup by parsing `/etc/os-release` and checking which binary exists. The rest of the backend references the interface, never a concrete package manager.

**Package name mapping:**

A single JSON mapping file (`pkg-map.json`) translates generic names to distro-specific ones. Most packages share the same name — only exceptions need entries. App manifests use generic names and the mapping resolves them at install time.

```json
{
  "nginx": { "alpine": "nginx", "debian": "nginx-light" },
  "bluetooth": { "alpine": "bluez", "debian": "bluez" }
}
```

If a package has no mapping entry, the generic name is passed directly to the package manager (works for 90%+ of packages).

**Adding a new OS — the checklist:**

1. Write a `PackageManager` implementation (~100 lines — wrap the distro's CLI: `dnf`, `pacman`, `zypper`, etc.)
2. Write a Dockerfile (`FROM` the base image, install the same dependency list using the distro's package manager)
3. Add any package name exceptions to `pkg-map.json`
4. Add the distro to the CI build matrix
5. Done — the Go binary, React frontend, stream layer, and all services work unchanged

**Implementation steps:**

- [ ] Abstract `services/packages/` behind a `PackageManager` interface
- [ ] Implement `ApkManager` — extract current `apk` code into this backend (no behaviour change)
- [ ] Implement `AptManager` — wrap `apt-get`/`dpkg`
- [ ] Distro auto-detection at startup via `/etc/os-release` — set the active `PackageManager` and expose distro info to telemetry/About page
- [ ] Create `pkg-map.json` — seed with known name differences between Alpine and Debian
- [ ] Update app manifest format to use generic package names resolved through the mapping
- [ ] Update all error messages and install hints to use the detected package manager dynamically
- [ ] Alpine Dockerfile — current, keep as default
- [ ] Debian Dockerfile — `FROM debian:bookworm-slim`, same packages via `apt-get`
- [ ] CI matrix — build and publish all image variants on release
- [ ] Document the "adding a new OS" checklist in DEVELOPMENT.md
- [ ] Steam/Wine on Debian image works natively (glibc) — no chroot needed

### Platform
- [ ] Improved mobile responsiveness
- [ ] Accessibility improvements
- [ ] Internationalization (i18n)
- [ ] Plugin/extension API for third-party developers

---

## Completed

### Auth Enforcement
- [x] Middleware enforces auth — returns 401 on all /api/ and /app/ routes without valid session
- [x] Public endpoint whitelist: /health, /api/auth/providers, /login/\*, /callback/\*
- [x] Frontend assets served without auth (React handles its own gate)

### Sandbox Security
- [x] Dangerous code validation (blocks subprocess, os.system, eval, exec, fork bombs)
- [x] 100KB code size limit
- [x] 5-minute execution timeout per sandbox script
- [x] Sandbox proxy protected by auth middleware

### Dev Mode Bypass
- [x] "Continue without login" only shows in Vite dev mode
- [x] Production builds never show the bypass

### AI-Generated Apps
- [x] Save button in AI viewport window title bar
- [x] CRUD API for persisted AI apps (~/.vulos/ai-apps/)
- [x] List, retrieve, and delete saved AI apps

### Browser Profiles
- [x] Firefox-style profile isolation (Personal, Work, Private)
- [x] Bind apps to profiles
- [x] Clear data per profile without deleting it
- [x] REST API: CRUD + bind + clear

### AI OS Control
- [x] AI can include `<os-action>` blocks to control the OS
- [x] Supported actions: open-app, close-app, notify, energy-mode, exec
- [x] System prompt teaches AI about OS control capabilities

### Persistence
- [x] Chat history restored from backend on Portal mount
- [x] Window/desktop state persisted to localStorage
- [x] AppRegistry cleaned — removed unimplemented stubs

### Polish
- [x] Vault/Backup settings UI
- [x] Recall/Search settings UI
- [x] AI Apps gallery in Settings
- [x] Ad blocker — 50+ domains, EasyList-format blocklist, class/id matching
