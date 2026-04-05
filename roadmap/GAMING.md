# Gaming Mode

Gaming mode is a per-session configuration flag that raises FPS, bitrate, encoder aggressiveness, and input polling rate. Normal app streaming is unchanged.

---

## Streaming Modes

| | Normal (default) | Gaming mode |
|---|---|---|
| FPS | 60 (configurable: 30/60) | 60 / 90 / 120 / 144 / unlimited |
| Bitrate | 500–4000 kbps | 4000–12000 kbps |
| Encoder | standard latency | zerolatency, no B-frames, no lookahead |
| Audio buffer | 20ms Opus frames | 10ms Opus frames |
| Mouse | coalesced (16ms) | raw passthrough |
| Gamepad polling | 60Hz | 120Hz |
| Process priority | normal | SCHED_FIFO / high nice |

Gaming mode auto-enables for Wine, Lutris, Steam, and any app in the gaming category. Can be manually toggled on any stream window.

---

## Settings

- [ ] Default FPS limit: 30 / 60 (Settings → Streaming)
- [ ] Gaming mode toggle per stream session
- [ ] Bitrate ceiling slider for gaming mode (4000–12000 kbps)
- [ ] FPS selector in stream toolbar when gaming mode is active (60 / 90 / 120 / 144 / unlimited)

## FPS Control

Current state: hardcoded 30fps at launch (`pool.go` LaunchOpts.FPS), set once via GStreamer `framerate=N/1`, no UI.

- [ ] Default FPS raised to 60 for normal mode
- [ ] Store FPS preference in `~/.vulos/settings.json`
- [ ] `POST /api/stream/fps` — restarts capture segment with new framerate
- [ ] GStreamer: use `videorate` element to allow dynamic FPS changes without full pipeline restart

---

## Encoder Profiles (gaming mode)

### NVENC (NVIDIA)
```
preset=low-latency-hp  zerolatency=true  b-adapt=false
rc-lookahead=0  rc-mode=cbr  bitrate=6000  gop-size=120  bframes=0
```

### VA-API (Intel/AMD)
```
tune=low-power  rate-control=cbr  bitrate=6000
keyframe-period=120  max-bframes=0
```

### Software VP8 (no GPU)
```
cpu-used=16  deadline=1  lag-in-frames=0  end-usage=cbr
target-bitrate=6000000  threads=8  keyframe-max-dist=120
```

Bitrate tiers added for gaming:
```go
QualityGaming    = 6000   // gaming default
QualityGamingMax = 10000  // gaming, excellent connection
```

---

## Input (gaming mode)

### Mouse
- [ ] Disable 16ms coalescing — send every event immediately
- [ ] Pointer lock on stream canvas — raw mouse deltas for FPS games
- [ ] `Escape` exits pointer lock

### Gamepad
- [ ] Increase polling from 60Hz to 120Hz
- [ ] Configurable deadzone (currently hardcoded 0.05)
- [ ] Rumble/vibration via WebRTC data channel (Gamepad Haptics API)
- [ ] Multiple gamepad support (currently only `gamepads[0]`)
- [ ] Port gamepad polling from `RemoteBrowser.jsx` to `StreamViewer.jsx` via shared `useGamepad` hook

---

## Process Priority

- [ ] Game process: `SCHED_FIFO` or `nice -10`
- [ ] GStreamer encoder: high priority (must keep up with game FPS)
- [ ] Requires `--cap-add SYS_NICE` in Docker

---

## Stream Toolbar (gaming mode)

- [ ] FPS counter (from MangoHud or RTCP stats)
- [ ] Latency display (WebRTC RTT)
- [ ] Quality indicator (current adaptive bitrate tier)
- [ ] FPS limit selector (60 / 90 / 120 / 144 / unlimited)
- [ ] MangoHud toggle (`MANGOHUD=1` env var — FPS, frametimes, GPU/CPU temp)
- [ ] Fullscreen button (browser fullscreen API + pointer lock)

---

## Data Flow (gaming mode vs normal)

### Gaming mode (GPU)
```
Game (Vulkan via DXVK, GPU render)
  → Wayland/cage (DMA-BUF, zero-copy)
    → PipeWire pipewiresrc
      → NVENC / VA-API (zerolatency, no B-frames, 6000–10000 kbps, 60–144fps)
        → RTP → WebRTC

Input: WebRTC → uinput (10–50µs)
Audio: PipeWire 5ms → Opus 10ms frames → WebRTC
Glass-to-glass target: <30ms (local network)
```

### Normal mode (no GPU, unchanged)
```
App (CPU render) → Xvfb → ximagesrc → vp8enc → WebRTC (1500–4000 kbps, 60fps)
Input: xdotool pipe (1–5ms)
Audio: PulseAudio 20ms → Opus 20ms frames → WebRTC
```

---

## Supported Apps

Wine, Lutris, and Steam are installable from the app store. Gaming mode activates automatically for all of them, and for any app in the `gaming` category (including Flatpak games). See APP-STORE.md → Gaming for install details, Wine prefix config, DXVK, Proton, GameMode, and MangoHud setup.

---

## Docker

```bash
# NVIDIA
docker run --gpus all --device /dev/uinput --cap-add SYS_NICE --shm-size=2g -p 8080:8080 vulos

# AMD/Intel
docker run --device /dev/dri --device /dev/uinput --cap-add SYS_NICE --shm-size=2g -p 8080:8080 vulos
```

`--shm-size=2g` — Wine and DXVK use shared memory heavily.

---

## Implementation Order

1. Raise default FPS to 60, add per-session FPS config
2. Gaming mode flag on LaunchOpts — encoder profile + bitrate tiers
3. Pointer lock + relative mouse in frontend
4. Gamepad: port to StreamViewer, increase poll rate, rumble
5. Process priority scheduling
6. Auto-enable gaming mode for Wine/Lutris/Steam/gaming category apps
