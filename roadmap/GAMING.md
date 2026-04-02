# Gaming Optimisation

Cloud gaming stack for Wine and Lutris. Normal app streaming capped at 60fps by default (configurable in Settings). Gaming mode removes the cap and applies aggressive low-latency tuning across the entire pipeline.

---

## Streaming Modes

### Normal Mode (default)
- 60fps cap (configurable in Settings: 30 / 60)
- Adaptive bitrate: 500–4000 kbps
- Standard encode latency
- Used for: browser, productivity apps, general app streaming

### Gaming Mode (per-session flag)
- FPS uncapped — 60 / 90 / 120 / 144 / unlimited (match display)
- Higher bitrate floor: 4000–12000 kbps
- Ultra-low-latency encoder tuning (no B-frames, no lookahead, zerolatency)
- Reduced input polling interval
- Priority process scheduling
- Used for: Wine games, Lutris, native Linux games

### Settings UI
- [ ] Default FPS limit dropdown in Settings > Streaming (30 / 60)
- [ ] Gaming mode toggle per stream session (auto-enabled for Wine/Lutris launches)
- [ ] Manual override: user can force gaming mode on any stream window
- [ ] Bitrate ceiling slider for gaming mode (4000–12000 kbps)

---

## FPS Control

### Current state
- Hardcoded 30fps at launch (`pool.go` LaunchOpts.FPS default)
- Set once via GStreamer `framerate=N/1` cap — cannot change without pipeline restart
- No UI to configure

### Changes
- [ ] Default FPS: 60 for normal mode
- [ ] Gaming mode FPS: configurable 60 / 90 / 120 / 144 / unlimited
- [ ] Store FPS preference in user settings (`~/.vulos/settings.json`)
- [ ] `POST /api/stream/fps` endpoint — restarts capture segment with new framerate
- [ ] Frontend: FPS selector in stream window toolbar (gaming mode only)
- [ ] GStreamer pipeline: use `videorate` element to allow dynamic FPS changes without full restart

---

## Gaming Mode Encoder Profiles

When a session is launched with gaming mode, override the default encoder args.

### NVENC (NVIDIA)
```
nvh264enc
  bitrate=6000          (3x normal)
  preset=low-latency-hp (high performance, not high quality)
  rc-mode=cbr
  gop-size=120          (keyframe every 2s at 60fps)
  zerolatency=true
  b-adapt=false
  rc-lookahead=0
  aud=true
  bframes=0
```

### NVENC AV1 (RTX 4000+)
```
nvav1enc
  bitrate=5000
  preset=low-latency-hp
  rc-mode=cbr
  gop-size=120
  zerolatency=true
```

### VA-API (Intel/AMD)
```
vaapih264enc
  bitrate=6000
  rate-control=cbr
  keyframe-period=120
  tune=low-power
  cabac-entropy-coding=true
  max-bframes=0
```

### Software VP8 (no GPU — gaming still works, just heavier)
```
vp8enc
  target-bitrate=6000000
  cpu-used=16           (max speed, sacrifice compression)
  deadline=1
  keyframe-max-dist=120
  threads=8             (use more cores)
  end-usage=cbr
  lag-in-frames=0
  error-resilient=1
```

---

## Adaptive Bitrate — Gaming Tier

Currently max quality is 4000 kbps. Gaming needs higher tiers.

- [ ] Add `QualityGaming` level: 6000 kbps
- [ ] Add `QualityGamingMax` level: 10000 kbps
- [ ] Gaming sessions start at `QualityGaming` instead of `QualityMedium`
- [ ] Same RTCP loss/RTT monitoring applies — degrades gracefully on bad connections
- [ ] Wire bitrate changes to running encoder (currently dead code — see `streaming_optimisation.md`)

```go
QualityLow       = 500    // high loss / slow
QualityMedium    = 1500   // normal default
QualityHigh      = 2500   // good connection
QualityMax       = 4000   // excellent connection
QualityGaming    = 6000   // gaming default
QualityGamingMax = 10000  // gaming, excellent connection
```

---

## Input Latency — Gaming Tuning

### Mouse
- [ ] Reduce mouse event throttle from current coalescing to raw passthrough in gaming mode
- [ ] Disable 16ms frame coalescing for mouse in gaming mode — send every event immediately
- [ ] Use unreliable WebRTC data channel (already done) with `maxRetransmits=0`

### Keyboard
- [ ] Keep reliable/ordered channel (every keypress must arrive) — no change needed
- [ ] Modifier reconciliation via bitmask already handles packet recovery

### Gamepad
- [ ] Increase polling rate from 60Hz to 120Hz or requestAnimationFrame in gaming mode
- [ ] Deadzone filtering already client-side (0.05 threshold) — make configurable
- [ ] Add rumble/vibration feedback via WebRTC data channel (Gamepad Haptics API)
- [ ] Support multiple gamepads (currently only `gamepads[0]`)

### uinput
- [ ] Already zero-overhead (~10-50us per event) — no changes needed
- [ ] Ensure `/dev/uinput` access in Docker: `--device /dev/uinput`

---

## Wine — Gaming Optimisations

### Prefix Configuration
- [ ] Gaming-specific prefix template: win10, 64-bit, DXVK pre-installed
- [ ] `POST /api/wine/prefixes` accepts `template: "gaming"` — auto-configures everything below
- [ ] One-click "Create Gaming Prefix" in Settings UI

### DXVK (DirectX 9/10/11 → Vulkan)
- [ ] Auto-install DXVK on any prefix with GPU tier >= VA-API (already done)
- [ ] DXVK HUD toggle: `DXVK_HUD=fps,frametimes` env var — shows in-game overlay
- [ ] DXVK async shader compilation: `DXVK_ASYNC=1` — reduces stutter on first-run
- [ ] DXVK state cache: persist `*.dxvk-cache` files across prefix recreations

### VKD3D-Proton (DirectX 12 → Vulkan)
- [ ] Install VKD3D-Proton alongside DXVK for D3D12 games
- [ ] Detection: check for `d3d12.dll` override in prefix
- [ ] `POST /api/wine/vkd3d` endpoint to install on existing prefix
- [ ] Auto-install on gaming template prefixes

### Wine Env Vars for Gaming
```bash
# Performance
STAGING_SHARED_MEMORY=1        # faster memory allocation
WINE_LARGE_ADDRESS_AWARE=1     # allow >2GB memory for 32-bit games
__GL_THREADED_OPTIMIZATIONS=1  # NVIDIA threaded GL

# DXVK / Vulkan
DXVK_ASYNC=1                  # async shader compilation
VKD3D_CONFIG=dxr11             # DXR raytracing support (where available)
RADV_PERFTEST=aco              # AMD: use ACO shader compiler (faster)

# Audio
PULSE_LATENCY_MSEC=30          # lower PulseAudio buffer (default 60)

# Display
WINE_FULLSCREEN_FSR=1          # AMD FidelityFX Super Resolution upscaling
WINE_FSR_SHARPNESS=2           # FSR sharpness (0-5, lower = sharper)
```

### Windows Version
- [ ] Default gaming prefix to Windows 10 (already default)
- [ ] Some older games need Windows 7 — make switchable in UI per prefix

---

## Lutris Integration

### Current State
- No Lutris launcher in the system
- Only reference is `winetricks` fallback for DXVK install

### Add Lutris as a First-Class App
- [ ] Install Lutris in Dockerfile (`apt-get install lutris` or Flatpak)
- [ ] Add to registry.json as a streamable app (category: gaming)
- [ ] `POST /api/stream/launch` with `command: "lutris"` — streams Lutris UI
- [ ] Lutris manages its own Wine/Proton runners, DXVK versions, game configs

### Lutris Game Library
- [ ] Parse Lutris game library (`~/.local/share/lutris/`) for installed games
- [ ] `GET /api/lutris/games` — list installed games with metadata
- [ ] Launch games directly: `lutris lutris:rungameid/<id>` via stream pool
- [ ] Auto-enable gaming mode when launching via Lutris

### Lutris Runners
- [ ] Expose Lutris runner management in Settings UI
- [ ] Wine-GE / Proton-GE runner downloads via Lutris
- [ ] Steam runtime compatibility layer via Lutris

---

## Native Linux Games

### Steam
- [ ] Install Steam in Dockerfile (or Flatpak)
- [ ] Stream Steam Big Picture mode via stream pool
- [ ] Proton/Steam Play for Windows games on Linux
- [ ] Add to registry.json (category: gaming)

### Flatpak Gaming
- [ ] Gaming category in app store with curated Flatpak games
- [ ] Auto-enable gaming mode for apps in gaming category

---

## Process Priority & Scheduling

When gaming mode is active for a session, tune the OS scheduler.

- [ ] Set game process to `SCHED_FIFO` or high `nice` priority (-10)
- [ ] Set GStreamer encoder process to high priority (must keep up with game FPS)
- [ ] Lower priority on non-gaming sessions if system is under load
- [ ] `CAP_SYS_NICE` capability needed in Docker: `--cap-add SYS_NICE`

---

## Audio — Gaming Specific

### Lower Latency
- [ ] Gaming mode: `PULSE_LATENCY_MSEC=30` (vs 60 default)
- [ ] PipeWire (when available): quantum=256 rate=48000 (~5ms buffer)
- [ ] Opus encode: `frame-size=10` in gaming mode (10ms vs 20ms default — halves audio latency)

### Spatial Audio
- [ ] Passthrough multichannel audio (5.1/7.1) when game outputs it
- [ ] Opus supports up to 8 channels — encode as multichannel if client supports
- [ ] Fallback: downmix to stereo (current behaviour)

### Microphone Input
- [ ] Route client microphone → WebRTC audio track → PulseAudio virtual_mic
- [ ] Games and voice chat pick up from virtual_mic_input source
- [ ] Already partially wired (virtual_mic null-sink exists) — need WebRTC inbound audio track

---

## Docker — Gaming Specific

### Additional Packages
```dockerfile
# Gaming
RUN apt-get install -y --no-install-recommends \
    wine wine64 wine32 \
    lutris \
    winetricks \
    vulkan-tools mesa-vulkan-drivers \
    libvulkan1 \
    gamemode libgamemode0 \        # Feral GameMode — auto CPU/GPU governor
    mangohud \                      # FPS overlay
    lib32-mesa-vulkan-drivers \     # 32-bit Vulkan (many Windows games are 32-bit)
    && ...
```

### Docker Run for Gaming
```bash
# Full gaming setup (NVIDIA)
docker run \
  --gpus all \
  --device /dev/uinput \
  --device /dev/input \
  --cap-add SYS_NICE \
  --shm-size=2g \
  -p 8080:8080 \
  vulos

# Full gaming setup (AMD/Intel)
docker run \
  --device /dev/dri \
  --device /dev/uinput \
  --device /dev/input \
  --cap-add SYS_NICE \
  --shm-size=2g \
  -p 8080:8080 \
  vulos
```

Note: `--shm-size=2g` (up from 1g) for gaming — Wine and DXVK use shared memory heavily.

---

## GameMode (Feral Interactive)

Linux tool that auto-optimises CPU governor, GPU clock, scheduler, and I/O priority when a game is running.

- [ ] Install `gamemode` in Dockerfile
- [ ] Wrap game launch with `gamemoderun`: `gamemoderun wine game.exe`
- [ ] Also wrap Lutris launches: Lutris has built-in GameMode support
- [ ] Effects: CPU set to performance governor, GPU clocks unlocked, I/O priority boosted

---

## MangoHud — In-Game Overlay

FPS counter, frame timing, CPU/GPU usage overlay for debugging performance.

- [ ] Install `mangohud` in Dockerfile
- [ ] Toggle via env var: `MANGOHUD=1`
- [ ] Configure via `MANGOHUD_CONFIG=fps,frametime,gpu_temp,cpu_temp`
- [ ] Expose toggle in stream window toolbar (gaming mode only)
- [ ] Works with DXVK, VKD3D, and native Vulkan games

---

## Frontend — Gaming UI

### Stream Window Toolbar (gaming mode)
- [ ] FPS counter (from MangoHud or RTCP stats)
- [ ] Latency display (WebRTC RTT)
- [ ] Quality indicator (current adaptive tier)
- [ ] FPS limit selector (60 / 90 / 120 / 144 / unlimited)
- [ ] MangoHud toggle
- [ ] DXVK HUD toggle
- [ ] Fullscreen button (browser fullscreen API + pointer lock)

### Pointer Lock
- [ ] Request pointer lock on stream canvas in gaming mode
- [ ] Send raw mouse deltas instead of absolute coordinates (FPS games need this)
- [ ] `Escape` exits pointer lock
- [ ] Relative mouse mode via uinput `evRel` (already supported in kernel module)

### Gamepad UI
- [ ] Connected gamepad indicator in stream toolbar
- [ ] Button mapping overlay (show Xbox layout)
- [ ] Deadzone configuration slider
- [ ] Support multiple gamepads — player 1/2/3/4

---

## StreamViewer — Gamepad Support

`StreamViewer.jsx` currently lacks gamepad support (only `RemoteBrowser.jsx` has it).

- [ ] Port gamepad polling from RemoteBrowser to StreamViewer
- [ ] All streamed apps (Wine, Lutris, native) get gamepad input
- [ ] Create shared `useGamepad` hook used by both components

---

## Data Flow — Gaming Mode vs Normal

### Gaming Mode (GPU)
```
Game (Wine/Lutris, GPU render, Vulkan via DXVK)
  → Wayland compositor (DMA-BUF, GPU memory)
    → PipeWire pipewiresrc (DMA-BUF, zero-copy)
      → nvh264enc / vaapih264enc (GPU, zerolatency, no B-frames)
        → RTP → WebRTC (6000-10000 kbps, 60-144fps)

Input: WebRTC data channel → uinput (10-50us)
Audio: PipeWire (5ms) → Opus 10ms frames → WebRTC
Total glass-to-glass latency target: <30ms (local network)
```

### Normal Mode (no GPU, unchanged)
```
App (CPU render)
  → Xvfb (X11 SHM)
    → ximagesrc (CPU memcpy)
      → vp8enc (CPU, 4 threads)
        → RTP → WebRTC (1500-4000 kbps, 60fps cap)

Input: xdotool pipe (1-5ms)
Audio: PulseAudio (20ms) → Opus 20ms frames → WebRTC
```

---

## Implementation Order

1. FPS control — raise default to 60, add per-session configurable FPS
2. Gaming mode flag on LaunchOpts — higher bitrate, encoder tuning
3. Wine gaming prefix template — DXVK + VKD3D + env vars auto-configured
4. Pointer lock + relative mouse in frontend
5. Gamepad support in StreamViewer (port from RemoteBrowser)
6. Lutris integration — install, registry entry, game library API
7. GameMode + MangoHud in Dockerfile
8. Gaming adaptive bitrate tiers (6000/10000 kbps)
9. Audio latency reduction (10ms Opus frames, mic input)
10. Process priority scheduling (SCHED_FIFO, CAP_SYS_NICE)
