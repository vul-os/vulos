# App Store — Installable Apps

Apps available for install from the Vulos app store. These are not bundled with the OS — users install what they need. Mix of web-native apps (run in browser) and streamed apps (run natively, UI streamed via WebRTC).

---

## Full App Checklist

| Category | App | Type | In Registry | In Roadmap | Status |
|---|---|---|---|---|---|
| **Gaming** | Steam | Streamed | No | Yes | Planned |
| | Lutris | Streamed | No | Yes | Planned |
| | Wine (base) | Streamed | No | Yes | Planned |
| | Flatpak games | Streamed | No | Yes | Planned |
|---|---|---|---|---|---|
| **Video Editing** | Kdenlive | Streamed | Yes | Yes | Done |
| | Shotcut | Streamed | No | Yes | Planned |
| | FFmpeg.wasm Editor | Web-native | No | Yes | Planned |
| **Audio / Music** | Audacity | Streamed | Yes | Yes | Done |
| | Ardour (DAW) | Streamed | No | Yes | Planned |
| | LMMS | Streamed | No | Yes | Planned |
| | Web Audio Editor | Web-native | No | Yes | Planned |
| **3D / VFX** | Blender | Streamed | Yes | Yes | Done |
| | Three.js Editor | Web-native | No | Yes | Planned |
| **Photo Editing** | GIMP | Streamed | Yes | Yes | Done |
| | Darktable (RAW) | Streamed | No | Yes | Planned |
| | Photopea | Web-native | No | Yes | Planned |
| **Vector / Design** | Inkscape | Streamed | Yes | Yes | Done |
| | Penpot (Figma alt) | Web-native | No | Yes | Planned |
| **Streaming** | OBS Studio | Streamed | No | Yes | Planned |
| **GIS / Mapping** | MapLibre GIS Editor | Web-native | No | Yes | Planned |
| | QGIS | Streamed | No | Yes | Planned |
| **Science / Research** | Jupyter Lab | Web-native | Yes | Yes | Done |
| | GNU Octave | Streamed | No | Yes | Planned |
| **Finance** | GnuCash | Streamed | No | Yes | Planned |
| | Firefly III | Web-native | No | Yes (WEB-APPS) | Planned |
| **Office** | LibreOffice | Streamed | Yes | Yes | Done |
| **Dev Tools** | VS Code (code-server) | Web-native | No | Yes (WEB-APPS) | Planned |
| | Gitea | Web-native | Yes | Yes | Done |
| | Wede (code editor) | Web-native | Yes | Yes | Done |
| | Geany | Streamed | Yes | Yes | Done |
| | Grafana | Web-native | Yes | Yes | Done |
| **CAD / EDA** | KiCad | Streamed | Yes | Yes | Done |
| | FreeCAD | Streamed | No | Yes (future/) | Planned |
| **Internet** | Firefox | Streamed | Yes | Yes | Done |
| | FileZilla | Streamed | Yes | Yes | Done |
| | qBittorrent | Streamed | Yes | Yes | Done |
| | Transmission | Web-native | Yes | Yes | Done |
| **Productivity** | Syncthing | Web-native | Yes | Yes | Done |
| | Thunderbird | Streamed | Yes | Yes | Done |
| | KeePassXC | Streamed | Yes | Yes | Done |
| | Vaultwarden | Web-native | No | Yes (WEB-APPS) | Planned |
| | Excalidraw | Web-native | No | Yes (WEB-APPS) | Planned |
| | draw.io | Web-native | No | Yes (WEB-APPS) | Planned |
| **Media Servers** | Navidrome (music) | Web-native | No | Yes | Planned |
| | Jellyfin (video) | Web-native | No | Yes | Planned |
| **System** | Cockpit | Web-native | Yes | Yes | Done |
| | Uptime Kuma | Web-native | No | Yes | Planned |
| | Stirling PDF | Web-native | No | Yes | Planned |
| **Notes** | Memos | Web-native | No | Yes | Planned |
| **Whiteboard / Diagrams** | Excalidraw | Web-native | No | Yes | Planned |
| | draw.io | Web-native | No | Yes | Planned |
| **Password Manager** | KeePassXC | Streamed | Yes | Yes | Done |
| | Vaultwarden | Web-native | No | Yes | Planned |
| **Messaging** | Matrix Client | Web-native | No | Yes | Planned |
| **Telephony** | Vulos Phone (Go webapp) | Web-native | No | Yes | Planned |
| **Media Player** | VLC | Streamed | Yes | Yes | Done |
| **Video Calling** | Jitsi Meet | Web-native | No | Yes | Planned |
| **Developer** | Hoppscotch (API testing) | Web-native | No | Yes | Planned |
| **Translation** | LibreTranslate | Web-native | No | Yes | Planned |

**Summary: 17 Done, 31 Planned**

---

## GIS / Mapping

All web-native, open source, run fully in the browser.

### MapLibre GIS Editor
- **Core**: [MapLibre GL JS](https://github.com/maplibre/maplibre-gl-js) — BSD-3, GPU-accelerated vector map rendering
- **Analysis**: [Turf.js](https://github.com/Turfjs/turf) — MIT, spatial analysis (buffer, intersect, distance, area, centroid, etc.)
- **Data editing**: [geojson.io](https://github.com/mapbox/geojson.io) — MIT, draw/edit features on a map, import/export GeoJSON/KML/CSV
- **Visualization**: [deck.gl](https://github.com/visgl/deck.gl) — MIT, large-scale data visualization layers (heatmaps, point clouds, 3D)
- **Install**: static files + tile server (or use free OpenStreetMap tiles)
- **Capabilities**:
  - Draw points, lines, polygons on the map
  - Import/export GeoJSON, KML, Shapefile, CSV
  - Spatial queries — buffer zones, intersections, distance calculations
  - Style custom map layers (choropleth, heatmap, 3D buildings)
  - Offline tile caching for fieldwork without internet
- **Replaces**: Google Maps (for GIS work), basic QGIS workflows

### QGIS (streamed)
- **What**: full professional GIS — raster analysis, large datasets, geoprocessing
- **Type**: streamed via WebRTC (native app, too heavy for browser)
- **Install**: `apk add qgis` / `apt install qgis`
- **When to use**: when MapLibre isn't enough — raster processing, advanced spatial analysis, print layouts

---

## Video Editing

No mature open-source web-based video editors exist. Best approach: lightweight web tools for simple edits, stream native apps for professional work.

### Video Editor (web-native)
- **Core**: [FFmpeg.wasm](https://github.com/nicoleahmed/FFmpeg.wasm) — MIT, FFmpeg compiled to WebAssembly
- **UI**: custom timeline editor built on Canvas API
- **Capabilities**:
  - Cut, trim, split, merge clips
  - Add text overlays and subtitles
  - Basic transitions (fade, crossfade)
  - Audio track management (mute, volume, add music)
  - Export to mp4, webm
  - Runs entirely in-browser, no server needed
- **Limitations**: slower than native for long videos, limited effects
- **Good for**: quick edits, social media clips, trimming recordings

### Kdenlive (streamed)
- **What**: full non-linear video editor — multi-track, effects, color grading, keyframes
- **Project**: [KDE/kdenlive](https://github.com/KDE/kdenlive) — GPLv2, 4k+ stars
- **Type**: streamed via WebRTC
- **Install**: `apk add kdenlive` / `apt install kdenlive`
- **Replaces**: Premiere Pro, Final Cut Pro, DaVinci Resolve
- **When to use**: professional editing, long-form content, complex multi-track projects

### Shotcut (streamed)
- **What**: cross-platform video editor with wide format support
- **Project**: [mltframework/shotcut](https://github.com/mltframework/shotcut) — GPLv3, 11k+ stars
- **Type**: streamed via WebRTC
- **Install**: binary download or package manager
- **Replaces**: simpler alternative to Kdenlive, good for intermediate users

---

## Audio Editing / Music Production

### Audio Editor (web-native)
- **Waveform**: [wavesurfer.js](https://github.com/katspaugh/wavesurfer.js) — BSD-3, waveform visualization and playback
- **Audio engine**: [Tone.js](https://github.com/Tonejs/Tone.js) — MIT, Web Audio framework for synthesis, effects, scheduling
- **Processing**: Web Audio API (browser-native) — filters, gain, compression, reverb, delay
- **Capabilities**:
  - Record from microphone
  - Cut, trim, split, merge audio clips
  - Multi-track mixing with volume and pan per track
  - Effects — EQ, reverb, delay, compression, noise gate
  - Waveform and spectrogram visualization
  - Export to wav, mp3 (via lamejs), ogg
- **Good for**: podcast editing, voice cleanup, simple mixing, sound design

### Audacity (streamed)
- **What**: the standard open-source audio editor
- **Project**: [audacity/audacity](https://github.com/audacity/audacity) — GPLv3, 13k+ stars
- **Type**: streamed via WebRTC
- **Install**: `apk add audacity` / `apt install audacity`
- **Replaces**: Adobe Audition, GarageBand (for audio editing)
- **When to use**: detailed audio editing, noise reduction, batch processing, plugin support (VST/LV2)

### Ardour (streamed)
- **What**: professional digital audio workstation (DAW) — multi-track recording, mixing, mastering
- **Project**: [Ardour/ardour](https://github.com/Ardour/ardour) — GPLv2, 4k+ stars
- **Type**: streamed via WebRTC
- **Install**: `apk add ardour` / `apt install ardour`
- **Replaces**: Pro Tools, Logic Pro, Ableton Live
- **When to use**: music production, studio recording, professional mixing/mastering

### LMMS (streamed)
- **What**: music production — beat making, synthesizers, samples, MIDI
- **Project**: [LMMS/lmms](https://github.com/LMMS/lmms) — GPLv2, 8k+ stars
- **Type**: streamed via WebRTC
- **Install**: `apk add lmms` / `apt install lmms`
- **Replaces**: FL Studio, GarageBand (for beat making)
- **When to use**: electronic music, beat production, MIDI composition

---

## 3D / Creative

### Blender (streamed)
- **What**: 3D modeling, animation, VFX, compositing, video editing
- **Project**: [blender/blender](https://projects.blender.org/blender/blender) — GPLv2, industry standard
- **Type**: streamed via WebRTC (requires GPU)
- **Install**: `apk add blender` / `apt install blender`
- **Replaces**: Maya, 3ds Max, Cinema 4D, After Effects (compositing)
- **Note**: headless mode (`blender -b`) available for batch rendering on remote GPU instances (paid compute)

### Three.js Editor (web-native)
- **What**: lightweight 3D scene editor in the browser
- **Project**: [mrdoob/three.js](https://github.com/mrdoob/three.js) — MIT, 104k+ stars, includes an editor
- **Install**: static files
- **Capabilities**: basic 3D modeling, scene composition, material editing, export to glTF
- **Good for**: simple 3D scenes, web 3D prototyping, learning — not a Blender replacement

### Penpot (web-native)
- **What**: open-source design tool (Figma alternative)
- **Project**: [penpot/penpot](https://github.com/penpot/penpot) — MPL-2.0, 35k+ stars
- **Install**: Docker (Clojure backend + web frontend)
- **Replaces**: Figma, Sketch, Adobe XD

---

## Photo Editing

### Photopea (web-native)
- **What**: advanced image editor — PSD, XCF, Sketch, RAW support
- **Note**: free to use, fully client-side, but **not open source**
- **Capabilities**: layers, masks, filters, brushes, pen tool, text — near Photoshop-level in a browser
- **Replaces**: Photoshop, GIMP (for users who want browser-native)

### GIMP (streamed)
- **What**: full image editor — layers, masks, filters, brushes, scripting
- **Project**: [GNOME/gimp](https://gitlab.gnome.org/GNOME/gimp) — GPLv3, industry standard open-source image editor
- **Type**: streamed via WebRTC
- **Install**: `apk add gimp` / `apt install gimp`
- **Replaces**: Photoshop
- **When to use**: complex image manipulation, batch processing with Script-Fu/Python, plugin ecosystem

### Darktable (streamed)
- **What**: RAW photo processing and photography workflow — non-destructive editing, color management
- **Project**: [darktable-org/darktable](https://github.com/darktable-org/darktable) — GPLv3, 10k+ stars
- **Type**: streamed via WebRTC
- **Install**: `apk add darktable` / `apt install darktable`
- **Replaces**: Adobe Lightroom
- **When to use**: RAW photo development, batch processing, professional photography workflow

---

## Vector / Graphic Design

### Inkscape (streamed)
- **What**: vector graphics editor — SVG editing, illustrations, logos, diagrams
- **Project**: [inkscape/inkscape](https://gitlab.com/inkscape/inkscape) — GPLv2, industry standard
- **Type**: streamed via WebRTC
- **Install**: `apk add inkscape` / `apt install inkscape`
- **Replaces**: Adobe Illustrator, CorelDRAW
- **When to use**: logos, icons, illustrations, print-ready vector art, SVG editing

### Penpot (web-native)
- Already listed under 3D / Creative — also covers vector design and UI/UX prototyping

---

## Streaming / Broadcasting

### OBS Studio (streamed)
- **What**: live streaming and screen recording — scenes, sources, transitions, filters
- **Project**: [obsproject/obs-studio](https://github.com/obsproject/obs-studio) — GPLv2, 62k+ stars
- **Type**: streamed via WebRTC
- **Install**: `apk add obs-studio` / `apt install obs-studio`
- **Replaces**: Streamlabs, XSplit, proprietary streaming tools
- **Capabilities**:
  - Multi-source compositing (camera, screen, images, browser sources)
  - Stream to Twitch, YouTube, any RTMP endpoint
  - Local recording to mp4/mkv
  - Scene transitions, audio mixing, filters
  - Plugin ecosystem
- **Note**: pairs well with the paid managed TURN service for users behind NAT

---

## Science / Research / Education

### Jupyter Lab (web-native)
- **What**: interactive notebooks — code, visualizations, markdown in one document
- **Project**: [jupyterlab/jupyterlab](https://github.com/jupyterlab/jupyterlab) — BSD-3, 14k+ stars
- **Install**: `pip install jupyterlab` / single command
- **Capabilities**: Python, R, Julia kernels, inline charts, LaTeX, data exploration
- **Replaces**: MATLAB notebooks, Google Colab (self-hosted)
- **Already in registry** (WEB-APPS.md)

### GNU Octave (streamed)
- **What**: MATLAB-compatible numerical computing — matrices, plotting, signal processing
- **Project**: [gnu-octave/octave](https://www.gnu.org/software/octave/) — GPLv3
- **Type**: streamed via WebRTC
- **Install**: `apk add octave` / `apt install octave`
- **Replaces**: MATLAB
- **When to use**: engineering, mathematics, signal processing, control systems — anywhere MATLAB is used

---

## Finance / Business

### GnuCash (streamed)
- **What**: double-entry accounting — invoicing, accounts payable/receivable, reports, tax
- **Project**: [Gnucash/gnucash](https://github.com/Gnucash/gnucash) — GPLv2, 6k+ stars
- **Type**: streamed via WebRTC
- **Install**: `apk add gnucash` / `apt install gnucash`
- **Replaces**: QuickBooks, Xero, Sage
- **When to use**: small business accounting, personal finance, invoicing

### Firefly III (web-native)
- **What**: personal finance manager — budgeting, transaction tracking, reports
- **Project**: [firefly-iii/firefly-iii](https://github.com/firefly-iii/firefly-iii) — AGPLv3, 17k+ stars
- **Install**: PHP app or Docker
- **Replaces**: Mint, YNAB
- **Already in registry** (WEB-APPS.md)

---

## Video Calling

### Jitsi Meet (web-native)
- **What**: video conferencing — no account needed, share a link and join
- **Project**: [jitsi/jitsi-meet](https://github.com/jitsi/jitsi-meet) — Apache-2.0, 24k+ stars
- **Install**: self-hosted (Ocker or deb packages)
- **Capabilities**: video/audio calls, screen sharing, chat, recording, breakout rooms, up to 100+ participants
- **Replaces**: Zoom, Google Meet, Microsoft Teams
- **Why**: the only serious open-source self-hosted video calling platform. Web-native — callers just click a link, no app install.

---

## Whiteboard / Diagrams

### Excalidraw (web-native)
- **What**: collaborative whiteboard and diagramming
- **Project**: [excalidraw/excalidraw](https://github.com/excalidraw/excalidraw) — MIT, 90k+ stars
- **Install**: static files, no backend needed
- **Capabilities**: freehand drawing, shapes, text, arrows, real-time collaboration
- **Replaces**: Miro, FigJam, draw.io (for informal diagrams)

### draw.io (web-native)
- **What**: structured diagramming — flowcharts, UML, ERDs, network diagrams
- **Project**: [jgraph/drawio](https://github.com/jgraph/drawio) — Apache-2.0, 42k+ stars
- **Install**: static files, no backend needed
- **Replaces**: Lucidchart, Visio, diagrams.net desktop (Electron)

---

## Password Manager

### Vaultwarden (web-native)
- **What**: Bitwarden-compatible password manager with web vault
- **Project**: [dani-garcia/vaultwarden](https://github.com/dani-garcia/vaultwarden) — AGPLv3, 40k+ stars
- **Install**: single Rust binary
- **Replaces**: Bitwarden (Electron), 1Password, LastPass
- **Why**: lightweight, self-hosted, full Bitwarden client compatibility (browser extension, mobile apps all work)

---

## Media Servers

### Navidrome (web-native)
- **What**: personal music streaming server — your own Spotify
- **Project**: [navidrome/navidrome](https://github.com/navidrome/navidrome) — GPLv3, 13k+ stars
- **Install**: single Go binary
- **Capabilities**: web UI, Subsonic API (works with existing mobile music apps), scrobbling, playlists
- **Replaces**: Spotify, Apple Music (for your own library)

### Jellyfin (web-native)
- **What**: self-hosted media server — movies, TV, music, photos
- **Project**: [jellyfin/jellyfin](https://github.com/jellyfin/jellyfin) — GPLv2, 37k+ stars
- **Install**: apt package or binary, web UI
- **Replaces**: Plex, Netflix (for personal media)

---

## Notes

### Memos (web-native)
- **What**: lightweight self-hosted memo / microblog — quick capture, tags, search
- **Project**: [usememos/memos](https://github.com/usememos/memos) — MIT, 35k+ stars
- **Install**: single Go binary
- **Replaces**: Apple Notes (for quick capture), Twitter-like personal feed

---

## System Utilities

### Uptime Kuma (web-native)
- **What**: self-hosted monitoring and status pages
- **Project**: [louislam/uptime-kuma](https://github.com/louislam/uptime-kuma) — MIT, 62k+ stars
- **Install**: Node.js app, single command
- **Capabilities**: HTTP/TCP/DNS/ping monitoring, notifications (email, Slack, Telegram), status pages
- **Replaces**: Pingdom, StatusPage, UptimeRobot

### Stirling PDF (web-native)
- **What**: PDF toolkit — merge, split, rotate, compress, OCR, convert, sign
- **Project**: [Stirling-Tools/Stirling-PDF](https://github.com/Stirling-Tools/Stirling-PDF) — GPLv3, 50k+ stars
- **Install**: Java app with web UI
- **Replaces**: Adobe Acrobat, SmallPDF, iLovePDF

---

## Developer Tools

### VS Code (code-server) (web-native)
- **What**: Microsoft's VS Code running in the browser
- **Project**: [coder/code-server](https://github.com/coder/code-server) — MIT, 70k+ stars
- **Install**: single binary
- **Replaces**: VS Code (Electron), Sublime Text, Atom

### Hoppscotch (web-native)
- **What**: API testing and development — requests, collections, environments, WebSocket, GraphQL
- **Project**: [hoppscotch/hoppscotch](https://github.com/hoppscotch/hoppscotch) — MIT, 66k+ stars
- **Install**: static files or self-hosted
- **Replaces**: Postman, Insomnia

---

## Translation

### LibreTranslate (web-native)
- **What**: self-hosted machine translation — no Google/DeepL dependency
- **Project**: [LibreTranslate/LibreTranslate](https://github.com/LibreTranslate/LibreTranslate) — AGPLv3, 10k+ stars
- **Install**: Python app, downloads language models on first run
- **Capabilities**: 30+ languages, API for other apps to use, works fully offline after model download
- **Replaces**: Google Translate, DeepL

---

## Messaging — Matrix Client

Unified messaging app with bridges to WhatsApp, Telegram, Signal, and more. Built on the Matrix protocol.

### Lightweight open-source web clients (pick one as base)

| Client | Tech | License | Stars | Notes |
|--------|------|---------|-------|-------|
| [Cinny](https://github.com/cinnyapp/cinny) | React | AGPLv3 | 4k+ | Clean, Discord-like UI. Lightweight. **Best candidate** — modern React, easy to theme for Vula OS |
| [Hydrogen](https://github.com/nicoleahmed/hydrogen-web) | Vanilla JS | Apache-2.0 | 3k+ | Ultra-lightweight, designed for low-end devices and embedded use. Minimal dependencies |
| [Element Web](https://github.com/element-hq/element-web) | React | AGPLv3 | 11k+ | Most feature-complete but heavy (Electron heritage). Reference implementation |
| [FluffyChat](https://github.com/krille-chan/fluffychat) | Flutter | AGPLv3 | 1k+ | Cross-platform (mobile + web). Lighter than Element |

### Recommendation

**Cinny** as the base — React (matches our stack), lightweight, clean UI, easy to restyle for Vula OS. Hydrogen as fallback for extremely constrained devices.

### Bridges (run as services)

Matrix bridges let users receive messages from proprietary networks inside the Matrix client — one inbox for everything.

| Bridge | Connects to | Project |
|--------|-------------|---------|
| [mautrix-whatsapp](https://github.com/mautrix/whatsapp) | WhatsApp | Go, no phone needed (multi-device) |
| [mautrix-telegram](https://github.com/mautrix/telegram) | Telegram | Python |
| [mautrix-signal](https://github.com/mautrix/signal) | Signal | Go |
| [mautrix-instagram](https://github.com/mautrix/instagram) | Instagram DMs | Python |
| [mautrix-facebook](https://github.com/mautrix/facebook) | Messenger | Python |
| [mautrix-slack](https://github.com/mautrix/slack) | Slack | Go |
| [mautrix-discord](https://github.com/mautrix/discord) | Discord | Go |
| [heisenbridge](https://github.com/hifi/heisenbridge) | IRC | Python |

### Architecture

```
┌─────────────────────────────────┐
│  Cinny (web UI in WebKit)       │
│  ← Matrix client-server API →   │
└──────────────┬──────────────────┘
               │
┌──────────────▼──────────────────┐
│  Conduit or Dendrite             │
│  (lightweight Matrix homeserver) │
│  ┌──────────────────────────┐   │
│  │ Runs on localhost         │   │
│  │ SQLite storage            │   │
│  │ Federation optional       │   │
│  └──────────────────────────┘   │
└──────────────┬──────────────────┘
               │
┌──────────────▼──────────────────┐
│  Bridges (optional services)     │
│  mautrix-whatsapp                │
│  mautrix-telegram                │
│  mautrix-signal                  │
│  ...                             │
└──────────────────────────────────┘
```

### Homeserver options

| Server | Language | Notes |
|--------|----------|-------|
| [Conduit](https://gitlab.com/famedly/conduit) | Rust | Single binary, SQLite, very lightweight. Best for Vula OS |
| [Dendrite](https://github.com/matrix-org/dendrite) | Go | Next-gen official server, lighter than Synapse |
| [Synapse](https://github.com/element-hq/synapse) | Python | Reference server, heavy, not ideal for mobile/embedded |

### Install plan

- [ ] Bundle Conduit as a Vula OS service (single Rust binary, ~10MB)
- [ ] Cinny as the web UI, themed to match Vula OS
- [ ] Bridge installer in settings: "Connect WhatsApp", "Connect Telegram", etc.
- [ ] First-run wizard: create local Matrix account, optionally connect bridges
- [ ] E2E encryption enabled by default

---

## Implementation Notes

### Web-native apps
Run entirely in the browser. No streaming overhead, instant response, works offline. Preferred when possible.

```json
{
  "id": "gis-editor",
  "name": "GIS Editor",
  "type": "web",
  "category": "professional",
  "install": "static",
  "port": 80
}
```

### Streamed apps
Run natively on the device (or remote GPU), UI streamed via WebRTC to the browser. Used for heavy apps that can't run in a browser.

```json
{
  "id": "kdenlive",
  "name": "Kdenlive",
  "type": "streamed",
  "category": "professional",
  "install": "apk add kdenlive",
  "stream": true,
  "gpu": true
}
```

### Hybrid approach
For video/audio, the web-native editor handles quick tasks (trim a clip, clean up audio). When the user needs more power, they can open the full streamed app (Kdenlive, Audacity) from within the same workflow.

---

## Gaming

Gaming apps are streamed via WebRTC. Installing any of them auto-enables gaming mode for their sessions (higher FPS, bitrate, low-latency encoder). See GAMING.md for streaming mode config.

### Wine (base)
- **What**: run Windows games and apps on Linux
- **Type**: streamed
- **Install**: `apt install wine wine64`
- **Post-install config** (Settings → Wine):
  - Create/manage Wine prefixes (Windows version: Win10 / Win7 / WinXP)
  - Install DXVK (DirectX 9/10/11 → Vulkan) — one click, auto-downloads
  - Install VKD3D-Proton (DirectX 12 → Vulkan) — one click
  - Env var overrides per prefix (`DXVK_ASYNC`, `WINE_FULLSCREEN_FSR`, etc.)
  - DXVK state cache — persists across prefix recreations
  - Gaming prefix template: Win10, 64-bit, DXVK + VKD3D pre-installed, one click

### Lutris
- **What**: game manager — installs and runs games from GOG, itch.io, and manual sources. Manages Wine/Proton runners, DXVK versions, per-game config.
- **Type**: streamed
- **Install**: `apt install lutris`
- **Post-install config** (via Lutris UI, streamed):
  - Runner management (Wine-GE, Proton-GE, download in-app)
  - Per-game Wine prefix, DXVK version, env vars
  - Built-in GameMode support (auto CPU/GPU governor tuning)
  - Game library from GOG / itch.io / manual installs
- **API**: `GET /api/lutris/games` — list installed games; `POST /api/stream/launch` with `lutris:rungameid/<id>` to launch directly

### Steam
- **What**: PC gaming platform — access Steam library, Proton for Windows games on Linux
- **Type**: streamed
- **Install**: `apt install steam` (or Flatpak)
- **Post-install config** (via Steam UI, streamed):
  - Steam Play / Proton — enable for all titles or per-game
  - Proton version per game (Proton-GE via ProtonUp-Qt for better compatibility)
  - Steam Big Picture mode recommended for streamed use (controller-friendly UI)
- **Note**: requires GPU for good performance. Proton compatibility layer handles most Windows-only titles.

### Supporting tools (installed alongside gaming apps)

| Tool | Purpose | Auto-installed with |
|------|---------|-------------------|
| `gamemode` | CPU/GPU governor, scheduler tuning when game is running | Wine, Lutris, Steam |
| `mangohud` | FPS/latency overlay, toggleable in stream toolbar | Wine, Lutris, Steam |
| `winetricks` | Install Windows runtime libraries into Wine prefixes | Wine |
| `protontricks` | Same as winetricks but for Steam/Proton prefixes | Steam |
| `protonup-qt` | Download and manage Proton-GE / Wine-GE runners | Steam, Lutris |

### Docker requirements for gaming apps

```bash
# GPU required for good gaming performance
docker run --gpus all \            # NVIDIA
  --device /dev/dri \              # or Intel/AMD
  --device /dev/uinput \           # gamepad + mouse input
  --cap-add SYS_NICE \             # process priority (GameMode, SCHED_FIFO)
  --shm-size=2g \                  # Wine/DXVK use shared memory heavily
  -p 8080:8080 vulos
```
