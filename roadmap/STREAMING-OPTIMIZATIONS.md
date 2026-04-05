# Streaming System Optimisation

GPU-accelerated path when hardware is available, fallback to current system when not.
uinput for all input injection when available, fallback to xdotool when not.

---

## Principle

Every change is conditional. If the hardware or dependency is not present, the current working system is used unchanged. No regressions on software-only environments.

---

## Input Injection

### uinput (default when `/dev/uinput` available)
- [ ] Use uinput for all input: mouse, keyboard, scroll, gamepad — not just gaming
- [ ] Single unified uinput path for both browser and app streaming sessions
- [ ] Direct `/dev/uinput` writes (~10-50us per event, zero fork overhead)

### xdotool (fallback when uinput unavailable)
- [ ] Persistent pipe mode (`xdotool -` on stdin) to avoid fork-per-event
- [ ] Covers restricted environments (containers without `/dev/uinput` access)

---

## Display Server

### Wayland compositor via cage (streaming, when GPU detected)

cage for streaming (headless, one app per session). labwc for bare metal (real display, multi-window) — see `baremetal_init.md`. Both are wlroots-based, same DMA-BUF/PipeWire capture path. Combined install: <1MB.

- [ ] Replace Xvfb with cage (single-app Wayland kiosk) for GPU streaming sessions
- [ ] `WLR_BACKENDS=headless` — no physical display, PipeWire captures compositor output
- [ ] DMA-BUF buffer sharing — app renders on GPU, frames stay in GPU memory
- [ ] PipeWire `pipewiresrc` captures DMA-BUF directly — zero CPU copies
- [ ] `WLR_RENDERER=vulkan` (or `gles2`) for GPU rendering
- [ ] Per-session Wayland socket isolation (like current per-session X11 sockets)
- [ ] cage over labwc for streaming because: minimal overhead (76KB, single-app kiosk, no SSD/stacking logic), purpose-built for one app + one output

### Xvfb (fallback when no GPU)
- [ ] Current system unchanged — Xvfb + ximagesrc + SHM capture
- [ ] No modifications to software-only path

---

## Screen Capture

### PipeWire DMA-BUF (when GPU + PipeWire available)
- [ ] `pipewiresrc do-timestamp=true` — zero-copy from compositor
- [ ] Frames arrive as DMA-BUF fd, passed directly to hardware encoder
- [ ] No `cudaupload` or `vaapipostproc` needed — frames already in GPU memory

### ximagesrc (fallback)
- [ ] Current X11 SHM capture path unchanged
- [ ] `videoconvert` / `cudaupload` / `vaapipostproc` for colour conversion as-is

---

## Video Encoding

### Hardware encoder tuning (when GPU detected)

#### NVENC (NVIDIA)
- [ ] Add `zerolatency=true` — disable reordering buffer
- [ ] Add `b-adapt=false` — no B-frames (each adds a frame of latency)
- [ ] Add `rc-lookahead=0` — encode immediately, no lookahead buffering
- [ ] Add `aud=true` — access unit delimiters for WebRTC compatibility
- [ ] Prefer AV1 on RTX 4000+ (already detected), H.264 fallback

#### VA-API (Intel/AMD)
- [ ] Add `tune=low-power` — use dedicated encode ASIC instead of shader-based encode
- [ ] Add `cabac-entropy-coding=true` for better compression at same bitrate
- [ ] Prefer AV1 on Intel Arc / AMD RX 7000+ (already detected), H.264 fallback

### Software VP8 (fallback when no GPU)
- [ ] Current `vp8enc` config unchanged (cpu-used=8, deadline=1, 4 threads)

---

## Adaptive Bitrate — Wire It Up

The bitrate controller (`bitrate.go`) calculates quality levels from RTCP stats but never applies them to the running encoder. Fix this.

- [ ] Expose GStreamer encoder element as a named element in the pipeline
- [ ] On quality change callback, adjust encoder `bitrate` property via GstBus or pipeline property
- [ ] Quality levels: 500 / 1500 / 2500 / 4000 kbps (already defined)
- [ ] Fallback: if live property change not supported, restart encode segment with new bitrate

---

## Audio

### PipeWire (when available)
- [ ] Replace PulseAudio with PipeWire + pipewire-pulse compatibility layer
- [ ] Lower latency (~5-10ms vs ~20-40ms with PulseAudio)
- [ ] Single daemon handles both screen capture and audio routing
- [ ] Virtual speaker/mic via PipeWire filter nodes instead of PulseAudio null-sinks
- [ ] Telephony audio (calls via ModemManager) routes through the same PipeWire → WebRTC path — see `TELEPHONY.md`

### PulseAudio (fallback)
- [ ] Current system unchanged — null-sink capture, opusenc 128kbps

### Gaming Mode Audio Tuning
- [ ] Gaming mode: `PULSE_LATENCY_MSEC=30` (vs 60 default) / PipeWire quantum=256 rate=48000 (~5ms)
- [ ] Opus `frame-size=10` in gaming mode (10ms vs 20ms default — halves audio latency)
- [ ] Passthrough multichannel audio (5.1/7.1) when app outputs it — Opus supports up to 8 channels, fallback downmix to stereo
- [ ] Route client microphone → WebRTC inbound audio track → PulseAudio/PipeWire virtual_mic source — games and voice chat pick up from virtual_mic (null-sink already exists, needs WebRTC inbound track wired up)

---

## Chromium Browser (GPU-Specific)

### GPU rendering (when GPU detected)
- [ ] Remove `--disable-gpu` and `--disable-software-rasterizer`
- [ ] Add `--enable-gpu --enable-gpu-rasterization --enable-zero-copy`
- [ ] Add `--enable-gpu-compositing --use-gl=egl`
- [ ] Add `--enable-features=VaapiVideoDecoder` for hardware video decode
- [ ] Remove `--disable-dev-shm-usage` (needs `--shm-size=1g` on docker run)

### Ozone/Wayland (when Wayland compositor active)
- [ ] Add `--ozone-platform=wayland --enable-features=UseOzonePlatform`
- [ ] Chrome talks directly to Wayland compositor via DMA-BUF — no XWayland

### Software rendering (fallback, current)
- [ ] Keep `--disable-gpu --disable-software-rasterizer --disable-dev-shm-usage`

---

## Telephony Audio Path

Call audio from ModemManager shares the existing streaming pipeline. No separate streaming system needed.

```
ModemManager (modem voice call)
  → ALSA / serial audio device
    → PipeWire (route call audio to virtual sink)
      → Opus encode → WebRTC data channel or audio track
        → Remote browser (user hears call, speaks back)

Remote mic (browser)
  → WebRTC audio track
    → PipeWire (route to modem audio input)
      → ModemManager → modem → cellular network
```

- [ ] Route modem audio device into PipeWire graph
- [ ] Bidirectional audio: remote user can speak into calls via WebRTC mic
- [ ] Echo cancellation via PipeWire filter node (webrtc-audio-processing)
- [ ] SMS and call signaling via WebSocket (no streaming needed, just JSON messages)

See `TELEPHONY.md` for full telephony roadmap.

---

## Data Flow Comparison

### With GPU (target)
```
App (GPU render)
  → Wayland compositor (DMA-BUF, GPU memory)
    → PipeWire pipewiresrc (DMA-BUF, zero-copy)
      → Hardware encoder (NVENC/VA-API, GPU memory)
        → RTP → WebRTC

CPU copies: 0
GPU copies: 0 (same memory throughout)
```

### Without GPU (current, unchanged)
```
App (CPU render)
  → Xvfb (X11 SHM, CPU memory)
    → ximagesrc (CPU memcpy)
      → videoconvert (CPU)
        → vp8enc (CPU, 4 threads)
          → RTP → WebRTC

CPU copies: 3-4
```

---

## Dockerfile — GPU Support

The current Dockerfile produces a single image that works without a GPU. GPU support needs to be baked into the same image so it auto-detects at runtime, or built as a separate GPU variant.

### Strategy: Single image, runtime detection

One image ships everything. At startup, `gpu.Detect()` picks the right path. No separate Dockerfile needed.

### Additional packages for GPU path
- [ ] `cage` — minimal wlroots Wayland compositor for streaming (76KB installed)
- [ ] `labwc` — wlroots compositor for bare metal desktop (713KB installed) — see `baremetal_init.md`
- [ ] `pipewire pipewire-pulse wireplumber` — PipeWire daemon + PulseAudio compat + session manager
- [ ] `gstreamer1.0-pipewire` — GStreamer pipewiresrc element
- [ ] `xdg-desktop-portal-wlr` — wlroots screen capture portal (PipeWire DMA-BUF)
- [ ] `libgbm1 libegl1` — GBM/EGL for headless GPU rendering

### NVIDIA Container Toolkit support
- [ ] Install `gstreamer1.0-plugins-bad` NVENC/CUDA elements (already present)
- [ ] Ensure `libnvidia-encode` and `libnvidia-gl` are accessible via mounted driver volumes
- [ ] No CUDA toolkit needed inside image — NVENC uses driver libs mounted by `--gpus all`
- [ ] Document: host must have `nvidia-container-toolkit` installed

### VA-API (Intel/AMD) support
- [ ] `mesa-va-drivers mesa-vulkan-drivers libva2 vainfo` — already in current Dockerfile
- [ ] `intel-media-va-driver-non-free` — already conditional on amd64
- [ ] Ensure `/dev/dri/renderD*` passed through via `--device /dev/dri`

### Updated apt-get block (additions in bold context)

```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends \
    # ... existing packages unchanged ...
    #
    # Wayland compositors (GPU path — cage for streaming, labwc for bare metal)
    cage labwc \
    # PipeWire stack (GPU path — audio + screen capture)
    pipewire pipewire-pulse wireplumber \
    gstreamer1.0-pipewire \
    xdg-desktop-portal-wlr \
    # GPU rendering deps
    libgbm1 libegl1 \
    && ...
```

### Environment variables

```dockerfile
# Wayland / GPU (runtime detection overrides these if no GPU)
ENV WLR_BACKENDS=headless
ENV WLR_RENDERER=vulkan
ENV XDG_SESSION_TYPE=wayland
ENV XDG_RUNTIME_DIR=/tmp/xdg-runtime
ENV MOZ_ENABLE_WAYLAND=1

# Current X11 defaults (used when no GPU detected at runtime)
ENV DISPLAY=:99
```

### Docker run commands

```bash
# No GPU (current, unchanged)
docker run -p 8080:8080 --shm-size=1g vulos

# NVIDIA GPU
docker run --gpus all -p 8080:8080 --shm-size=1g vulos

# Intel/AMD GPU
docker run --device /dev/dri -p 8080:8080 --shm-size=1g vulos

# Full (NVIDIA + uinput)
docker run --gpus all --device /dev/uinput -p 8080:8080 --shm-size=1g vulos

# Full (Intel/AMD + uinput)
docker run --device /dev/dri --device /dev/uinput -p 8080:8080 --shm-size=1g vulos
```

### Runtime startup logic (in Go or entrypoint)

```
1. gpu.Detect() — probe nvidia-smi, vainfo, /dev/dri
2. if GPU detected:
     - start PipeWire + WirePlumber
     - start cage (Wayland compositor) per streaming session
     - set Chromium GPU flags
     - use pipewiresrc + hardware encoder
3. if no GPU:
     - start PulseAudio (current)
     - start Xvfb per session (current)
     - set Chromium --disable-gpu flags
     - use ximagesrc + vp8enc

4. if /dev/uinput exists:
     - use uinput for all input (mouse, keyboard, scroll, gamepad)
5. if no /dev/uinput:
     - use xdotool persistent pipe fallback
```

### Image size impact
- PipeWire + wireplumber + cage + labwc adds ~25-35MB
- No CUDA toolkit — NVENC uses host-mounted driver libs (0MB)
- VA-API drivers already in image (0MB additional)
- Total increase: ~30MB on a ~1.2GB image

---

## Implementation Order

1. Chromium GPU flags (conditional on `gpu.Detect().Tier`) — immediate win, no infra changes
2. Encoder tuning (zerolatency, no B-frames) — small change, measurable latency drop
3. Wire adaptive bitrate to encoder — fixes dead code
4. Dockerfile: add PipeWire + cage + labwc packages
5. PipeWire audio replacement — lower audio latency
6. cage per streaming session (headless Wayland) — the big zero-copy win, most work
7. PipeWire screen capture integration — depends on 6
