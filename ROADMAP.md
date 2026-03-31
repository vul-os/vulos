# Vula OS — Roadmap

## Upcoming

### App Store & Registry
- [x] Registry overhaul — ~25 curated apt packages (kicad, gimp, blender, inkscape, libreoffice, audacity, lutris, etc.) with categories, metadata, icons
- [x] App store UI redesign — snap-style layout with categories, search, type badges (green web app / purple apt), streaming info
- [x] Unified desktop entries — `.desktop` file parser (`services/desktop/desktop.go`), merged into launchpad with web apps
- [x] Install/uninstall via apt — backend runs `apt install`/`apt remove`, parses `.desktop` file, app appears in launchpad

### App Streaming
- [x] `VNCBackend` — GStreamer `rfbsrc` capture from VNC source, same encode/WebRTC pipeline (`stream/vnc.go`)
- [x] Stream input improvements — scroll coalescing (16ms frames), touch gesture translation, client-side momentum scrolling

### GPU
- [x] DMA-BUF capture path — PipeWire `pipewiresrc` when available, falls back to `ximagesrc` (`gpu.CaptureArgs()`)
- [x] NVIDIA container toolkit setup docs — host-side setup guide in DEVELOPMENT.md
- [x] GPU tier, codec, AV1 support, and capture method shown in Settings UI

### Browser
- [x] Tab management via Chrome DevTools Protocol — tab bar, new/close/activate/navigate, session persist to localStorage
- [x] Extension support — `--enable-extensions --load-extension` from `~/.vulos/browser/extensions/`, list/delete API

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
- [ ] Night Shift — auto-adjust colour temperature during evening/night hours
- [ ] Wallpaper customization — user-uploaded backgrounds, dynamic wallpapers
- [ ] Accent colour picker — system accent colour beyond blue

### Terminal
- [ ] Custom themes and font configuration

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

### Platform
- [ ] Improved mobile responsiveness
- [ ] Accessibility improvements
- [ ] Internationalization (i18n)
- [ ] Plugin/extension API for third-party developers

---

## Future (Ladybird)

Blocked on Ladybird alpha (targeting late 2026). Ladybird's headless mode outputs raw bitmaps instead of rendering to X11, so it needs its own streaming backend.

- [ ] `FramebufferBackend` — direct `appsrc` bridge for apps that provide raw bitmaps
- [ ] Replace Chromium with Ladybird's LibWeb — event-driven frame encoding, zero X11 dependency, smaller image, faster startup

---

## Completed

### Tunnel Performance
- [x] Switch WebSocket library from `x/net/websocket` to `gorilla/websocket` with `permessage-deflate` compression — ~70-80% bandwidth reduction
- [x] Coalesce PTY output into 16ms frames — fewer packets over tunnel, smoother scrolling

### App Streaming Layer
- [x] Extract GStreamer/WebRTC pipeline from `browser.go` into shared `stream` package (`stream/pool.go`, `stream/stream.go`)
- [x] Implement `XvfbBackend` — Xvfb + ximagesrc capture + input injection, display allocation from `:10`
- [x] Refactor `browser.go` to use stream pool — thin wrapper around generic `stream.Pool`
- [x] Generic app streaming — `POST /api/stream/launch` streams any Linux GUI app
- [x] Adaptive bitrate — RTCP packet loss + RTT monitoring, auto-adjusts encode quality (`stream/bitrate.go`)

### GPU-Accelerated Encoding
- [x] GPU detection at startup (`services/gpu/gpu.go`) — probes nvidia-smi, vainfo, /dev/dri
- [x] GStreamer encoder auto-selection — `nvh264enc` → `vaapih264enc` → `vp8enc` fallback chain
- [x] AV1 hardware encode — `nvav1enc` (RTX 4000+) / `vaav1enc` (Intel Arc, AMD RX 7000+)
- [x] WebRTC codec auto-matches encoder (H.264 for hardware, VP8 for software)
- [x] VA-API drivers in Dockerfile (mesa-va-drivers, intel-media-va-driver-non-free)
- [x] GPU info exposed in telemetry (About page) and browser status API

### Wine
- [x] Wine integration — prefix management, create/delete per user (`services/wine/wine.go`)
- [x] Wine virtual desktop launcher — launch `.exe` with WINEPREFIX into dedicated Xvfb display
- [x] DXVK/VKD3D auto-install — detects GPU tier, installs DXVK for VA-API+ tiers
- [x] Windows version management — registry-based win7/win81/win10 selection per prefix

### Input
- [x] Zero-overhead uinput path — direct `/dev/uinput` writes, no process spawning per event (`services/input/uinput.go`)
- [x] Gamepad support — virtual Xbox 360 controller via uinput, maps W3C Gamepad API to Linux button codes
- [x] Mouse + keyboard injection with xdotool fallback for restricted environments
- [x] Audio passthrough — PulseAudio → GStreamer Opus encode in stream pipeline

### Browser
- [x] Chromium-based rendering engine via stream pool
- [x] Full bash terminal with shell history and PTY resize support

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
