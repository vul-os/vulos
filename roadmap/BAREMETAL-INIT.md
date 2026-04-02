# Bare Metal Init

How Vula OS boots on real hardware — from power-on to the desktop.

Works two ways simultaneously:
1. **Remote** — access from any browser on the network (current system, unchanged)
2. **Local** — user sits at the physical machine, interacts directly

On bare metal, apps launch as real native windows on the Wayland compositor — not streamed via WebRTC. The browser (showing the Vula OS shell) sits in the background like a wallpaper. Native windows get the same traffic light UI as in-browser windows.

---

## How Modern OS Boot Works

```
Power on
  → UEFI firmware (POST, probe hardware, find boot device)
    → Reads GPT partition table, finds EFI System Partition
      → Loads bootloader (GRUB / systemd-boot)

Bootloader
  → Loads Linux kernel + initramfs into RAM

Kernel
  → Probes hardware (PCI, USB, ACPI, device tree)
  → Mounts initramfs as temporary root (/)
  → Runs /init from initramfs

initramfs
  → Loads drivers (NVMe, ext4, GPU KMS)
  → Finds real root partition, mounts it
  → pivot_root → executes /sbin/init (PID 1)

PID 1 (systemd or vulos-init)
  → Mounts filesystems, starts services
  → Display server + browser → user sees the desktop
```

There is no assembly "start screen". The kernel provides a framebuffer via KMS/DRM from very early in boot. Plymouth draws a splash on that framebuffer. By the time PID 1 runs, you have pixels on screen. Starting Wayland is just launching a userspace program.

For a **live USB**: the kernel loads a **squashfs** (compressed read-only filesystem) into RAM, overlays tmpfs for writes. The whole OS runs from memory. An installer app (running inside the OS) partitions the internal disk, copies files, installs a bootloader. No separate installer environment needed — the live OS IS the installer.

---

## Where Vula OS Is Today

### Current bare metal boot
```
UEFI → GRUB → kernel → initramfs → systemd → vulos.service (headless, port 8080)
```
- `build.sh` creates Debian trixie rootfs, `release.yml` packages as `.img.gz`
- No display server — headless only, access via browser on another device

### Current kiosk mode (vulos-init as PID 1)
```
vulos-init → mount filesystems → vulos-server → cage + Cog → http://localhost:8080
```
- Single-window kiosk: cage runs one Cog/WPE instance fullscreen
- No native windows, no app launching outside the browser
- Native mode detection exists (`detectNativeMode()`) but only works with sway/labwc
- Cog-based native windows exist (`POST /api/shell/native-window`) but untested on bare metal

---

## Target Architecture

### The two-layer model

```
Layer 1: Wayland compositor (labwc)
  ├── Browser window (fullscreen, background, always behind everything)
  │   └── Vula OS shell (React app: launchpad, dock, menu bar, chat)
  ├── Native app window (GIMP, LibreOffice, terminal, Wine game...)
  ├── Native app window
  └── Native app window
      ↑ each has Vula OS traffic light decorations via labwc SSD theme
```

- **labwc** — lightweight wlroots compositor with server-side decorations (SSD)
- The browser is a Wayland window pinned to the background (like a desktop wallpaper)
- Apps launch as real Wayland windows on top of the browser
- labwc draws the title bar + traffic light buttons around each window (configured via theme)
- No WebRTC streaming needed on local bare metal — apps render directly to the compositor
- Remote users still connect via browser and get the WebRTC-streamed experience

### Why labwc for bare metal, cage for streaming

Two compositors, each optimal for its job. Both wlroots-based, <1MB combined install (cage 76KB + labwc 713KB).

**Bare metal (labwc)** — user sits at the machine, needs multiple windows on a real display:

| | cage | sway | labwc |
|---|---|---|---|
| Multi-window | No (single app kiosk) | Yes | Yes |
| Server-side decorations | No | No (CSD only) | Yes — themed title bars |
| Config complexity | None | High (i3-style tiling) | Low (openbox XML) |
| Resource usage | Minimal | Medium | Low |
| Window stacking | N/A | Tiling-focused | Floating (desktop-style) |
| Custom themes | N/A | No SSD | Yes — openbox themes |

labwc gives us floating windows with custom-themed title bars — exactly what we need to match the traffic light UI without modifying each app.

**Streaming (cage)** — one app per session, headless, captured via PipeWire for remote users:

| | cage | labwc |
|---|---|---|
| Purpose | Single-app kiosk (exactly what streaming needs) | Multi-window desktop |
| Headless | Rock solid (primary use case) | Had crash bugs in headless (#605) |
| Config needed | None — app maximized automatically | Needs `rc.xml` with fullscreen window rule |
| Memory per session | ~5MB | ~12-15MB |
| SSD overhead | None | Draws title bar pixels for nobody to see |
| 10 concurrent sessions | ~50MB | ~150MB |
| PipeWire/DMA-BUF path | wlroots (identical) | wlroots (identical) |

cage is purpose-built for "one app, one output, nothing else" — exactly the streaming use case. See `STREAMING-OPTIMIZATIONS.md` for the full streaming pipeline.

```
Bare metal local:     labwc (one instance, real display, multi-window, traffic lights)
Streaming remote:     cage per session (headless, one app, PipeWire capture)
No GPU fallback:      Xvfb + ximagesrc (current system, unchanged)
```

---

## Boot Sequence (Target)

```
UEFI → systemd-boot → Linux kernel + initramfs
  → Plymouth splash (Vula logo on screen)
  → systemd (or vulos-init as PID 1)
    → Phase 1: Filesystems
    → Phase 2: Hardware detection (GPU, audio, input, network)
    → Phase 3: Networking (DHCP)
    → Phase 4: PipeWire (audio + screen capture for remote)
    → Phase 5: labwc (Wayland compositor)
    → Phase 6: vulos-server (Go backend)
    → Phase 7: Browser (Cog/WPE, fullscreen background) → localhost:8080
    → Plymouth quit (seamless handoff, no TTY flash)
```

---

## Phase 1: Filesystems

Already implemented in `cmd/init/main.go`. Additions:

- [ ] Mount `/sys/firmware/efi/efivars` (UEFI boot management)
- [ ] Mount `/dev/shm` with size=2g (Chromium, Wine, DXVK)
- [ ] Mount user data partition if separate
- [ ] Overlay filesystem for live USB mode (squashfs + tmpfs)

---

## Phase 2: Hardware Detection

- [ ] GPU: reuse `gpu.Detect()` — probe `/dev/dri`, `nvidia-smi`, `vainfo`
- [ ] Audio: `/proc/asound/`, pick PipeWire or PulseAudio
- [ ] Input: enumerate `/dev/input/event*` — keyboard, mouse, touchscreen, gamepad
- [ ] Network: `/sys/class/net/` — wired vs wireless
- [ ] Storage: `/sys/block/` — for installer disk selection
- [ ] Battery: `/sys/class/power_supply/` — laptop detection
- [ ] Write results to `/var/log/vulos-boot.log`

---

## Phase 3: Networking

- [ ] DHCP on wired interfaces by default (systemd-networkd)
- [ ] WiFi: `wpa_supplicant` with saved credentials if no wired
- [ ] Fallback: `localhost:8080` — kiosk works locally without network
- [ ] mDNS/Avahi: advertise `vula.local` on LAN
- [ ] DNS resolution: ensure `/etc/resolv.conf` populated

---

## Phase 4: Compositor — labwc

### Configuration

labwc uses openbox-style XML config in `~/.config/labwc/`:

```xml
<!-- rc.xml — window behaviour -->
<labwc_config>
  <theme>
    <name>vulos</name>
    <!-- Server-side decorations with our traffic light buttons -->
    <titlebar>
      <height>28</height>
      <font>Noto Sans 11</font>
    </titlebar>
  </theme>

  <!-- Pin the browser window to the background -->
  <windowRules>
    <windowRule identifier="cog" type="full_maximise" skipTaskbar="yes">
      <action name="MoveToLayer" layer="background" />
    </windowRule>
  </windowRules>

  <!-- All other windows float on top -->
  <focus>
    <followMouse>no</followMouse>
    <raiseOnFocus>yes</raiseOnFocus>
  </focus>
</labwc_config>
```

### Traffic Light Theme

labwc supports openbox themes. Create `/usr/share/themes/vulos/openbox-3/themerc`:

```ini
# Title bar
window.active.title.bg: flat solid
window.active.title.bg.color: #1a1a1a
window.inactive.title.bg: flat solid
window.inactive.title.bg.color: #2a2a2a

# Traffic light buttons (rendered as coloured circles)
window.active.button.close.unpressed.image.color: #ff5f57
window.active.button.max.unpressed.image.color: #28c840
window.active.button.iconify.unpressed.image.color: #febc2e

window.active.button.close.hover.image.color: #ff3b30
window.active.button.max.hover.image.color: #00b341
window.active.button.iconify.hover.image.color: #f5a623

# Button layout: close, minimize, maximize on the LEFT (macOS-style)
window.active.button.layout: CMI

# Rounded corners
border.width: 1
border.color: #333333
window.handle.width: 0
padding.width: 8
```

This gives every native window the same red/yellow/green traffic lights as the in-browser window system, without modifying any app.

### GPU rendering

- [ ] GPU detected: `WLR_RENDERER=vulkan` or `WLR_RENDERER=gles2`
- [ ] No GPU: `WLR_RENDERER=pixman` (software, still works)
- [ ] Multi-monitor: labwc handles hotplug and multi-output natively
- [ ] HiDPI: `WLR_OUTPUT_SCALE=2` for Retina-style displays

### Headless fallback

- [ ] No display connected → skip labwc, run headless (network access only)
- [ ] Detect: check `/sys/class/drm/card*/status` for "connected"

---

## Phase 5: Browser as Desktop Background

The browser renders the Vula OS shell (dock, launchpad, menu bar, wallpaper, chat). It sits behind all native windows like a desktop wallpaper.

### How it works

1. labwc starts
2. Cog (WPE WebKit) launches fullscreen → `http://localhost:8080`
3. labwc window rule pins Cog to background layer (always behind)
4. User sees the Vula OS desktop — dock at bottom, menu bar at top, wallpaper
5. User clicks an app in launchpad → app launches as a real Wayland window on top
6. labwc decorates the window with the Vula OS traffic light theme

### Browser choice

**WPE WebKit via Cog (preferred)**
- Lightweight, no browser chrome (no URL bar, no tabs)
- ~150MB RAM vs ~400MB+ for Chromium
- Hardware-accelerated via EGL on Wayland
- `cog --platform=wl http://localhost:8080`

**Chromium (fallback)**
- `chromium --kiosk --ozone-platform=wayland http://localhost:8080`
- Already in the image, guaranteed to work

### Frontend detection

`useNativeMode.js` already detects this — when running under labwc, `detectNativeMode()` returns `"native"` and `canSpawnNativeWindow()` returns `true`.

---

## Phase 6: Native App Launching

### Current flow (remote/Docker — unchanged)
```
User clicks app → POST /api/stream/launch → Xvfb + GStreamer + WebRTC → streamed in browser
```

### Bare metal flow (new)
```
User clicks app → detect native mode → launch app directly on Wayland → real window appears
```

### Implementation

When `isOnDevice()` is true, Launchpad changes launch behaviour:

- [ ] **Built-in apps** (terminal, files, settings, etc.) — still React components inside the browser window (no change, they're already lightweight)
- [ ] **Desktop apps** (GIMP, LibreOffice, Blender, etc.) — launch natively on the compositor instead of streaming
  - `POST /api/shell/native-launch` → `exec.Command(binary, args...)` with `WAYLAND_DISPLAY` set
  - App appears as a real Wayland window, labwc decorates it with traffic light theme
  - No Xvfb, no GStreamer, no WebRTC — direct GPU rendering to screen
- [ ] **Wine/Lutris games** — launch natively with `WAYLAND_DISPLAY` (or XWayland for X11 games)
- [ ] **Browser tabs** — Cog spawns new windows (already implemented via `POST /api/shell/native-window`)

### The dock on bare metal

- [ ] Dock shows both in-browser windows AND native windows
- [ ] Native windows tracked via `wlr-foreign-toplevel-management-v1` protocol (labwc supports this)
- [ ] Backend: `GET /api/shell/windows` returns list of all Wayland windows (title, app_id, state)
- [ ] Clicking a native window in the dock focuses it (via `wlr-foreign-toplevel` activate)
- [ ] Minimise/close from dock works on native windows too

### Remote users see the same thing

When a remote user connects via browser:
- Built-in apps: same React components (no change)
- Desktop apps: streamed via WebRTC (current system, no change)
- The local user sees real windows, the remote user sees streamed windows — same apps, different transport

---

## Installer

### The easiest path: browser-based installer

Yes — install the browser first. The installer is just another Vula OS app.

```
USB boot
  → Plymouth splash (Vula logo)
  → squashfs + tmpfs overlay (OS runs from RAM)
  → systemd → PipeWire → labwc → Cog → http://localhost:8080
  → Full Vula OS desktop appears
  → "Install Vula OS" app pinned to dock
  → User clicks it → installer React app opens
  → Partitions disk, copies files, installs bootloader
  → Reboot → boots from internal disk
```

This is exactly what Ubuntu, Fedora, and ChromeOS do — boot into a live desktop, run the installer as an app. No separate installer environment. No assembly. No C program drawing pixels. The entire installer UI is React running in the browser.

### Boot splash (before browser is ready)

Plymouth handles this. From power-on to browser-ready takes ~5-15 seconds:

```
0s    UEFI POST
2s    Bootloader → kernel loading
3s    Plymouth splash appears (Vula logo + progress bar)
5s    systemd starts services
8s    labwc + Cog launch
10s   Browser loads React app from localhost:8080
12s   Plymouth fades out, desktop appears
```

Plymouth draws directly to the kernel framebuffer (KMS/DRM). No display server needed. When labwc starts, it takes over the DRM master and Plymouth hands off seamlessly — `plymouth quit --retain-splash`.

### Plymouth theme

```
/usr/share/plymouth/themes/vulos/
  ├── vulos.plymouth          (theme manifest)
  ├── vulos.script            (animation script)
  ├── logo.png                (Vula logo, centred)
  ├── progress.png            (progress bar sprite)
  └── background.png          (dark background)
```

```ini
# vulos.plymouth
[Plymouth Theme]
Name=Vula OS
Description=Vula OS boot splash
ModuleName=script

[script]
ImageDir=/usr/share/plymouth/themes/vulos
ScriptFile=/usr/share/plymouth/themes/vulos/vulos.script
```

### What the user sees

```
┌─────────────────────────────────────────────┐
│                                             │
│                                             │
│                                             │
│              ┌───────────┐                  │
│              │           │                  │
│              │  VULA OS  │                  │
│              │   logo    │                  │
│              │           │                  │
│              └───────────┘                  │
│                                             │
│          ████████████░░░░░░░  67%           │
│                                             │
│                                             │
│                                             │
│                                             │
│      Ctrl+V for verbose output              │
│                                             │
└─────────────────────────────────────────────┘
```

Dark background, centred Vula logo, **determinate progress bar** (not a spinner — user sees actual percentage), subtle hint text at bottom. Clean, confident, branded.

### Determinate progress bar

Plymouth supports `plymouth --update=<message>` and `plymouth system-update --progress=<percent>` from systemd service files. Each boot phase reports its progress:

```
 0%   Kernel loaded, initramfs running
10%   Filesystems mounted
20%   systemd started
30%   Networking up (DHCP acquired)
45%   PipeWire started
55%   labwc started (compositor ready)
65%   vulos-server started (HTTP 200 on /health)
80%   Browser launched, loading React app
95%   Desktop shell rendered
100%  Plymouth fades out → desktop visible
```

Implementation: each systemd unit and init script calls `plymouth update --status="phase" --progress=N` at key milestones. This is how Ubuntu/Fedora do their progress bars — they're not fake timers, they're tied to actual service completion.

```bash
# Example: in vulos.service ExecStartPre
ExecStartPre=/usr/bin/plymouth update --status="Starting Vula OS" --progress=65

# Example: in labwc.service ExecStartPost
ExecStartPost=/usr/bin/plymouth update --status="Display ready" --progress=55
```

### Verbose mode (Ctrl+V)

Press `Ctrl+V` during boot → splash dissolves, reveals live systemd journal output scrolling underneath:

```
┌─────────────────────────────────────────────┐
│ [  OK  ] Started systemd-networkd           │
│ [  OK  ] Started PipeWire Media Session     │
│ [  OK  ] Started WirePlumber                │
│ [  OK  ] Started labwc Wayland compositor   │
│ [  OK  ] Started Vula OS Server             │
│          Starting Cog WebKit browser...     │
│ [  OK  ] Started Cog WebKit browser         │
│                                             │
└─────────────────────────────────────────────┘
```

This is standard systemd + Plymouth behaviour. Plymouth renders the splash on top of the TTY. `Ctrl+V` (mapped via Plymouth key binding) switches to the TTY showing `systemd-journal` output. Press `Ctrl+V` again to go back to the splash.

The verbose output is **always running** behind the splash — Plymouth just hides it. No custom code needed, just configure Plymouth's key binding:

```ini
# In vulos.script
Plymouth.SetKey("v", Plymouth.ToggleVerbose);
```

### Boot experience comparison

| OS | What user sees | Verbose escape | Progress type |
|---|---|---|---|
| macOS | Apple logo + progress bar | Cmd+V | Determinate (fake timer) |
| Windows | Logo + spinning dots | None | Indeterminate |
| ChromeOS | Chrome logo | None | Indeterminate |
| Ubuntu | Logo + dot animation | Esc | Indeterminate |
| **Vula OS** | **Logo + progress bar + %** | **Ctrl+V** | **Determinate (real milestones)** |

We're the only one with a genuinely determinate progress bar tied to real boot phases. macOS fakes it with a timer. Ubuntu doesn't even try. We show actual progress because we control the entire boot chain.

### Installer app

Built as a React component in the Vula OS shell, backed by Go API endpoints.

**Backend endpoints:**
- [ ] `GET /api/installer/disks` — list internal drives (lsblk, size, model, existing partitions)
- [ ] `GET /api/installer/status` — check if running from live USB or installed
- [ ] `POST /api/installer/install` — trigger installation
- [ ] `GET /api/installer/progress` — WebSocket, streams install progress

**Installation steps:**
1. Select target disk
2. Partition: ESP (512MB FAT32) + root (ext4, rest of disk)
3. Format partitions
4. Copy rootfs (`rsync` from squashfs mount to target)
5. Install bootloader (`bootctl install` for systemd-boot)
6. Write `/etc/fstab`, hostname, timezone
7. Set up user account
8. Reboot prompt

**Installer UI:**
- [ ] Welcome screen with Vula logo
- [ ] Disk selection with visual disk map
- [ ] Progress bar during copy (rsync output parsed for percentage)
- [ ] Success screen with reboot button
- [ ] Error handling with recovery options

---

## squashfs + Live USB

### Image layout (GPT)

```
USB drive
  ├── p1  ESP        512MB   FAT32   (systemd-boot + kernel + initramfs)
  └── p2  rootfs     rest    ext4    (filesystem.squashfs + persistence)
```

### Boot flow

1. systemd-boot loads kernel with: `root=LABEL=vulos-live init=/sbin/vulos-init quiet splash`
2. initramfs detects squashfs on the partition
3. Mounts squashfs as read-only lower + tmpfs as upper → overlay root
4. pivot_root to overlay → full Vula OS running from RAM
5. USB can be removed after boot (if enough RAM, ~4GB minimum)

### Build changes

- [ ] `build.sh`: add `mksquashfs` step after debootstrap
- [ ] `release.yml`: create separate live USB `.img.gz` with squashfs layout
- [ ] initramfs hook: `/etc/initramfs-tools/scripts/local-bottom/vulos-live`

---

## Installed System Layout

### Partition table (GPT)

```
/dev/nvme0n1 (or /dev/sda)
  ├── p1  ESP     512MB   FAT32   /boot/efi   (bootloader, kernel, initramfs)
  ├── p2  root    rest    ext4    /            (Vula OS)
  └── (optional) p3  home         ext4    /home
```

### Kernel

- Debian stock kernel (`linux-image-amd64` / `linux-image-arm64`)
- Kernel command line: `root=UUID=... init=/sbin/vulos-init quiet splash plymouth.theme=vulos`
- All common hardware modules included (no custom build needed)

### initramfs

- Built by `update-initramfs`
- Includes: storage drivers, filesystem drivers, GPU KMS drivers, Plymouth
- Live USB variant includes squashfs + overlay logic

---

## ARM Support (Raspberry Pi, Pine64, PinePhone)

ARM boards use U-Boot or board firmware instead of UEFI/GRUB.

### Raspberry Pi
- [ ] Boot firmware reads `config.txt` + `kernel8.img` from FAT32 partition
- [ ] Device tree blob (`.dtb`) describes hardware
- [ ] GPU: VideoCore VI, mesa V3D driver
- [ ] labwc works on Pi 4/5 with mesa

### PinePhone
- [ ] U-Boot in SPI flash or SD card
- [ ] Touch input via libinput (labwc handles touch natively)
- [ ] Mobile display: labwc handles rotation + scaling
- [ ] postmarketOS kernel + device tree

### Build
- [ ] `build.sh` already supports `ARCH=arm64`
- [ ] `release.yml` already builds arm64 images
- [ ] Device-specific image variants: `vulos-arm64-rpi.img.gz`, `vulos-arm64-pinephone.img.gz`

---

## Docker vs Bare Metal

| | Docker | Bare Metal (remote) | Bare Metal (local) |
|---|---|---|---|
| PID 1 | tini | vulos-init | vulos-init |
| Display | Xvfb (virtual) | Xvfb (virtual) | labwc (real display) |
| Apps | Streamed (WebRTC) | Streamed (WebRTC) | Native Wayland windows |
| Window chrome | CSS (in-browser) | CSS (in-browser) | labwc SSD theme (traffic lights) |
| GPU | `--gpus all` | Direct | Direct |
| Input | `--device /dev/uinput` | uinput | libinput (real devices) |
| Audio | PulseAudio (virtual) | PulseAudio (virtual) | PipeWire (real hardware) |
| Boot | Instant | 5-15s | 5-15s |

---

## TTY Fallback

If labwc fails, drop to a text console:

```
┌─────────────────────────────────────┐
│          Vula OS v0.1.0             │
│                                     │
│  Open in browser:                   │
│    http://192.168.1.42:8080         │
│    http://vula.local:8080           │
│                                     │
│  Press Enter for recovery shell     │
└─────────────────────────────────────┘
```

Rendered via `getty` + bash script. No display server needed.

---

## Recovery & Debug

- [ ] `init=/bin/bash` kernel param → emergency root shell
- [ ] `console=ttyS0,115200` → serial console for headless debug
- [ ] Recovery mode in bootloader menu → root shell, no GUI
- [ ] `journalctl -u vulos` for service logs
- [ ] `/var/log/vulos-boot.log` for hardware detection

---

## Implementation Order

1. labwc config + Vula OS traffic light theme (openbox themerc)
2. Switch `vulos-init` from cage to labwc — browser as background window
3. Native app launching from launchpad (skip streaming on bare metal)
4. Dock integration with `wlr-foreign-toplevel` (show native windows in dock)
5. Plymouth boot splash with Vula branding
6. Seamless Plymouth → labwc handoff (no TTY flash)
7. squashfs + live USB overlay
8. Installer app (React UI + Go backend)
9. Networking in init (DHCP, WiFi, mDNS)
10. ARM device images (Raspberry Pi, PinePhone)
