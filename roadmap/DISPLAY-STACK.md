# Display Stack — X11 vs Wayland Consolidation

> **Goal.** Settle whether Vulos should keep shipping two display stacks (X11: `xvfb` / `xdotool` / `matchbox-window-manager` / `x11-xserver-utils`; Wayland: `labwc` / `cage` / `xdg-desktop-portal-wlr`), with *measured* answers to the two questions that have blocked the decision: VA-API under Wayland, and a replacement for xdotool's input injection.
> **Non-goals.** Doing the consolidation. This document is evidence and a recommendation. It changes no code and no packages.
> **Status.** Investigation complete, 2026-08-10. **The premise behind the deferral turned out to be wrong in three separate ways** (see *Headline*). No decision has been executed; `docs/decisions.md` still needs a `D100` entry pointing here once someone acts.

---

## How to read the evidence tags

Every factual claim below carries one of:

- **[MEASURED]** — produced by running something on this machine on 2026-08-10 and reading the output. The exact command is reproducible from *Appendix A*.
- **[READ]** — read directly out of the repository source at the cited `file:line`.
- **[ASSUMED]** — an inference I could not test here. Treated as a hypothesis, never as a result.

Measurements were taken on **Docker/OrbStack, `linux/arm64`, Debian trixie**. Where architecture matters, it is called out. **Nothing was measured on amd64 or on any machine with a GPU** — see *What remains unverifiable here*.

---

## Headline

Three claims motivated leaving both stacks installed. All three are wrong, and the third is inverted.

1. **"Consolidating is the biggest remaining image-size win."** It is not close to the biggest. Dropping *both* stacks entirely saves **49.3 MiB of a 1371 MiB image — 3.6%** **[MEASURED]**. `chromium` + `chromium-common` alone are **366.7 MiB (24.3%)**, and `libllvm19` + `mesa-vulkan-drivers` + `mesa-libgallium` are **220.8 MiB (14.6%)** **[MEASURED]**.

2. **"Consolidating is the biggest remaining CVE win."** Dropping both stacks removes **32 of 687 open CVE/package pairs — 4.7%** **[MEASURED]**. `python3.13` (35), `libsoup-3.0` (35) and `libav*` (23 each) each carry more than the entire X11 stack.

3. **"Dropping X11 is the security win."** Inverted. Dropping the X11 packages removes **3** open CVEs. Dropping the *Wayland* packages removes **26** **[MEASURED]** — because **`cage` and `labwc` both hard-`Depends` on `xwayland`**, which carries 25 open CVEs by itself **[MEASURED]**. The Wayland stack is currently the *more* CVE-exposed of the two, and it drags an X server into the image regardless.

And the two blocking unknowns are in very different shape than assumed:

- **xdotool's replacement already exists and needs no new package.** The exact `cage` and `labwc` binaries this image ships both advertise `zwp_virtual_keyboard_manager_v1` **and** `zwlr_virtual_pointer_manager_v1` on a headless session **[MEASURED]**. Separately, `xdotool` itself *works unmodified* under cage via the XWayland that cage already pulls in **[MEASURED]**.
- **VA-API under Wayland could not be tested here at all**, in either stack, because this machine has no `/dev/dri` **[MEASURED]**. That is not a Wayland finding; it is a hardware finding. See *What remains unverifiable here*.

---

## 1. What is actually true today

### 1.1 The Dockerfile is not one target — it is two targets' package sets merged

This is the single most important structural fact, and it reframes the whole question.

- `Dockerfile:125-148` installs both stacks **[READ]**.
- `build.sh:580-598` — the debootstrap rootfs for the **real bare-metal OS image** — installs `matchbox-window-manager x11-xserver-utils labwc cage` but **no `xvfb`, no `xdotool`, no `chromium`, no `pipewire*`, no `gstreamer1.0-pipewire`, no `xdg-desktop-portal-wlr`, no `libgbm1`/`libegl1`** **[READ]**.

So the two stacks are not two competing implementations of one thing. They belong to two different products:

| | Container image (`Dockerfile`) | Bare-metal OS (`build.sh`) |
|---|---|---|
| What runs | `stream.Pool` — streamed apps over WebRTC | `vulos-init` kiosk seat |
| Display server actually used | **Xvfb (X11)** — see §1.2 | **cage** (v1 default) or **labwc** (v2, opt-in) |
| Can the other stack run? | cage path exists but is non-functional (§1.4) | Xvfb path cannot run — the binaries are absent |

"Pick one stack" is the wrong shape of decision. There is no single runtime that has a choice to make.

### 1.2 In the container image, the X11 path is the only path — always

`pool.go:280-281` **[READ]**:

```go
cageBin, cageErr := lookPath("cage")
useCage := gpuInfo.Tier != gpu.TierSoftware && cageErr == nil
```

`gpu.probeVAAPI` returns nil unless `/dev/dri` exists (`gpu.go:343-350`) **[READ]**. A container without `--device /dev/dri` therefore lands on `TierSoftware`, `useCage` is false, and `Launch` starts `Xvfb` at `pool.go:332` **[READ]**. Confirmed on this machine: the container has no `/dev/dri` at all **[MEASURED]**.

Consequence: **every stream session in the published Docker image runs on X11**, and the Wayland packages in that image are inert. That is measured-adjacent rather than measured — I established the input (`/dev/dri` absent) by measurement and the branch by reading.

### 1.3 xdotool is a *fallback*, not the primary input path — and every call site is in one package

`input.NewInjector` tries `/dev/uinput` first and only builds an `xdotoolPipe` when that fails (`uinput.go:400-420`) **[READ]**. Complete xdotool surface **[READ]**:

- `backend/services/input/xdotool.go` — the only two `exec` sites: `xdotoolExec` (`:15`) and the persistent `xdotool -` pipe (`:39`). Both set `Env = []string{"DISPLAY=" + display}` (`:16`, `:40`) — a *replacement* env, so no `WAYLAND_DISPLAY` is ever passed.
- `backend/services/input/uinput.go` — 8 call sites into that pipe: `:466` (mousemove), `:479` (mousemove_relative), `:508` (button), `:529` (click), `:565`/`:573` (modifier keydown/keyup), `:631` (key), routed through `inj.xdotool` at `:726-731`.
- X11 keysym mapping table, `uinput.go:734` onward.
- Two constructor sites that create an injector at all: `pool.go:368` (X11 branch only) and `vnc.go:157-163`.

That is the entire blast radius: **one file plus one mapping table**. It is much smaller than the deferral implies.

### 1.4 The cage/Wayland streaming path is scaffolding that cannot work as written

Four independent breaks, all **[READ]**:

1. **No input injector at all.** `input.NewInjector` is called at `pool.go:368`, which is inside the `if !useCage` block. Cage sessions get `sess.injector == nil`, and every input handler early-returns (`stream.go:473,546,584`).
2. **Capture points at an X display that was never started.** `pool.go:444` calls `gpuInfo.CaptureArgs(display, …)` with the X display string even on the cage branch. `CaptureArgs` only emits `pipewiresrc` when `HasPipeWire && HasDRI && gstHasElement("pipewiresrc")` (`gpu.go:71`); otherwise it emits `ximagesrc display-name=:N` (`gpu.go:84-88`). Cage starts no X server, so with PipeWire down this is a pipeline against nothing.
3. **`pipewiresrc` has no node to bind to.** There is no xdg-desktop-portal ScreenCast code anywhere in the repo — no `org.freedesktop.portal`, no `SelectSources`, no D-Bus portal client. `xdg-desktop-portal-wlr` is installed and has **zero** Go references. `gpu.go:72-74` uses bare `pipewiresrc` with no `path`/`fd`/target, unlike the audio path which does target a node (`pool.go:739`).
4. **Resize is X11-only and ungated.** `stream.go:138-168` runs `xrandr --fb` with `DISPLAY=s.Display` for every session including cage ones.

So the thing the image pays 26 CVEs and 10 MiB for is a path that, read end to end, cannot carry a frame or a keystroke today.

### 1.5 Five binaries the Go code execs are not in the image

**[MEASURED]** by `command -v` inside the built image:

| Binary | Called from | In image? |
|---|---|---|
| `wlopm` | `energy.go:308` (DPMS on/off) | **MISSING** |
| `wlr-randr` | `display.go:104,122,144` | **MISSING** |
| `lswt` | `wltoplevel.go:104` | **MISSING** |
| `wlrctl` | `wltoplevel.go:206` | **MISSING** |
| `cog` | `init/main.go:1409`, `server/main.go:3346` | **MISSING** |
| `brightnessctl` | `display.go:72` | **MISSING** (sysfs fallback exists) |
| `gst-client` | `pool.go:490-517` (live bitrate) | **MISSING** |

Every one of these is on the **Wayland/native** side. The consequences **[READ]** + **[MEASURED]**:

- `detectCompositor()` (`display.go:353-364`) can never return `"wlroots"` — `wlr-randr` is absent — so it returns `"cage"`, and then every `wlr-randr` exec fails anyway. In the container, where `Dockerfile:240` sets `DISPLAY=:99` and `WAYLAND_DISPLAY` is unset, it returns `"x11"` and shells `xrandr --query` at a display nothing ever starts.
- `/api/shell/windows` (`wltoplevel`, registered unconditionally at `server/main.go:3434`) always returns `[]`.
- DPMS (`energy.go:308`) is a silent no-op — the error is discarded.

**`x11-xserver-utils` is installed to obtain exactly one binary, `xrandr`**, and it `Depends: cpp`, which drags in **31 MiB** of C preprocessor (`cpp-14-aarch64-linux-gnu`, 30,958 KB; `cpp-14-x86-64-linux-gnu` on amd64 is 34,503 KB) **[MEASURED]**. That is ~75% of the entire "drop X11" size win, spent on a compiler nobody runs, to get a resolution-setting tool.

### 1.6 `build.sh`'s remote-deploy package list is corrupted, and the gate does not cover it

`build.sh:249-270` **[READ]**. The list terminates at `plymouth plymouth-themes` (no trailing backslash), and the following lines then run as *new commands*:

```
    plymouth plymouth-themes
    flatpak rsync systemd systemd-sysv \
    avahi-daemon avahi-utils dhcpcd5 wpasupplicant
    flatpak rsync systemd systemd-sysv \
    openssh-server
```

`avahi-*`, `dhcpcd5`, `wpasupplicant` and `openssh-server` are being passed as **arguments to `flatpak`**, not installed. This is the exact failure mode the comment at `build.sh:576-578` says was already found and fixed *in the other list*. `scripts/check-image-packages.sh` reads only the `Dockerfile` **[READ]**, which is why the two `build.sh` lists drifted unnoticed. Not fixed here — out of scope for an investigation, but it should be a separate commit.

---

## 2. Evidence on the two blocking unknowns

### 2.1 xdotool replacement — RESOLVED, with a measured answer

Three findings, in order of usefulness.

**(a) Both shipped compositors already advertise the virtual-input protocols.** Started the image's own `cage` and `labwc` headless (`WLR_BACKENDS=headless WLR_RENDERER=pixman`) and enumerated globals with `wayland-info` **[MEASURED]**:

| Protocol | cage 0.2.0 | labwc | Replaces |
|---|---|---|---|
| `zwp_virtual_keyboard_manager_v1` | ✅ | ✅ | `xdotool key/keydown/keyup` |
| `zwlr_virtual_pointer_manager_v1` v2 | ✅ | ✅ | `xdotool mousemove/click/button` |
| `zwlr_screencopy_manager_v1` v3 | ✅ | ✅ | `ximagesrc` capture |
| `zwlr_export_dmabuf_manager_v1` | ✅ | ✅ | zero-copy capture → VA-API |
| `zwlr_output_manager_v1` v4 | ✅ | ✅ | `wlr-randr` (resolution) |
| `zwlr_output_power_manager_v1` | ❌ | ✅ | `wlopm` (DPMS) |
| `zwlr_foreign_toplevel_manager_v1` | ❌ | ✅ | `lswt` / `wlrctl` |
| `ext_foreign_toplevel_list_v1` | ❌ | ✅ | ditto |
| `zwlr_data_control_manager_v1` | ❌ | ✅ | clipboard (nothing exists today) |
| `xwayland_shell_v1` | ✅ | — | XWayland is live under cage |

So the replacement for `xdotool` is **a Wayland client speaking two stable wlroots protocols** — no new package, no `ydotool`, no `wtype`, no `/dev/uinput`. It also happens to replace `wlr-randr`, `wlopm`, `lswt` and `wlrctl`, all of which are *missing binaries today* (§1.5). One client removes four broken shell-outs.

The cage/labwc split matters: **`wltoplevel` can never work under cage** (no foreign-toplevel global) but would work under labwc **[MEASURED]**. That is a v1-vs-v2 constraint nothing in the code or docs currently states.

**(b) uinput cannot be the answer for a headless-wlroots session.** With cage running headless, `/proc/<cage-pid>/fd` contains **no `/dev/input` and no `/dev/dri` descriptors** **[MEASURED]**. wlroots' headless backend enumerates no libinput devices, and `init/main.go:1045` sets `WLR_LIBINPUT_NO_DEVICES=1` unconditionally anyway **[READ]**. A virtual evdev device created via `/dev/uinput` has nothing reading it. **The primary input path in `services/input` — the fast one, the one the whole package exists for — does not reach a Wayland session at all.** This is the opposite of the framing in the Dockerfile comment: uinput is not the stack-neutral path; it is X11's other path.

**(c) `xdotool` works *unmodified* under cage, via XWayland.** Running `cage -- bash -c '…'` in the container **[MEASURED]**:

```
CHILD_DISPLAY=:0
CHILD_WAYLAND=wayland-0
1280 720              # xdotool getdisplaygeometry
XDOTOOL_KEY_OK        # xdotool key a
XDOTOOL_MOVE_OK       # xdotool mousemove 100 100
```

cage hands its child a working `DISPLAY`, and xdotool's XTEST calls succeed against it. **Important limit on this result:** I measured that the *commands succeed*, not that the events reach a native Wayland client. XTEST fake input is confined to the X server, so it should drive XWayland clients and **not** native Wayland ones — **[ASSUMED]**, untested, and the assumption a real implementation must break before relying on (c). The reason (c) matters is narrower: *"Wayland removes xdotool"* is false as stated. Wayland removes xdotool only for native-Wayland clients.

### 2.2 VA-API under Wayland — NOT TESTABLE HERE, and here is exactly why

**[MEASURED]** inside the built image on this machine:

```
/dev/dri            : No such file or directory
/dev/uinput         : No such file or directory
/sys/class/misc     : no `uinput` entry (driver absent from the OrbStack kernel 6.17.8)
vainfo              : failed to initialize display (wayland, x11, drm all fail)
gst-inspect vaapih264enc / vaapipostproc / vaav1enc / vah264enc : ABSENT
```

The absent GStreamer elements are **not** a packaging defect. `gstreamer1.0-vaapi` **is** installed and its plugin **does** load — `gst-inspect-1.0 vaapi` prints full plugin details, and the blacklist is empty **[MEASURED]**. The VA-API plugins register elements only when a VA display can be opened, and there is no DRM node to open. Same for the modern `va` plugin.

**This is a host-hardware limitation, not a Wayland finding.** VA-API is equally untestable under X11 here. Any statement of the form "VA-API works/doesn't work under Wayland" produced on this machine would be fabricated. It is not in this document.

Two adjacent things I *could* establish:

- **The modern `va` plugin is already in the image.** `libgstva.so` ships in `gstreamer1.0-plugins-bad`, which is already installed **[MEASURED]**. But `gpu.probeVAAPI` gates solely on `gstHasElement("vaapih264enc")` (`gpu.go:362`) **[READ]** — the *deprecated* plugin, which upstream GStreamer has removed in 1.28. When Debian drops `gstreamer1.0-vaapi`, hardware encode silently degrades to software VP8 with no error beyond one log line. That is a latent regression independent of this decision.
- **Wayland capture does not need a portal.** `grim` — an off-the-shelf `zwlr_screencopy` client — captured a real **1280×720 PNG from the headless cage session** with no portal, no PipeWire, and no X server **[MEASURED]**. So the capture half of "VA-API under Wayland" has a working, demonstrated mechanism available today; what is missing is Go code, not a capability. Whether the *encode* half accelerates is the part that needs hardware.

---

## 3. The prize, quantified

### 3.1 Image size — real builds, not estimates

Four images built from the Dockerfile's exact apt invocation with one line varied each time. These contain **only** the apt layer — no `frontend/dist`, no Go binaries, no `registry.json` — because those are identical across variants. `linux/arm64`. **[MEASURED]**

| Variant | `docker image inspect .Size` | Δ vs full | Δ % |
|---|---|---|---|
| full (as shipped) | 1,437,611,076 B (1371.0 MiB) | — | — |
| drop X11 (`xvfb xdotool matchbox-window-manager x11-xserver-utils`) | 1,397,829,647 B | **−37.9 MiB** | −2.77% |
| drop Wayland (`labwc cage xdg-desktop-portal-wlr`) | 1,426,887,188 B | **−10.2 MiB** | −0.75% |
| drop both | 1,385,922,572 B | **−49.3 MiB** | −3.59% |

Corroborated independently by apt dependency-closure arithmetic (sum of `Installed-Size` over the exact set of packages that leave the closure): −41.0 MiB / −11.5 MiB / −56.0 MiB **[MEASURED]**. The two methods agree to within the usual installed-size-vs-layer-bytes gap.

Note "drop both" (45 packages) exceeds the sum of the two individually (17 + 24) — `libfontenc1`, `libxfont2`, `x11-xkb-utils` and `xserver-common` are shared between `xvfb` and `xwayland` and only leave when both stacks go **[MEASURED]**.

**For scale, in the same image [MEASURED]:** 604 packages, 1,545,087 KB installed. `chromium` 291,608 KB · `chromium-common` 83,865 KB · `libllvm19` 120,416 KB · `mesa-vulkan-drivers` 71,444 KB · `mesa-libgallium` 34,238 KB · `fonts-noto-core` 42,551 KB · `libicu76` 37,547 KB · `cpp-14-aarch64-linux-gnu` 30,958 KB.

**amd64 [ASSUMED, from package metadata not a built image]:** the same drops compute to roughly the same magnitude (`xvfb` 4,525 KB, `cpp-14-x86-64-linux-gnu` 34,503 KB, `xwayland` 2,472 KB, `labwc` 713 KB, `cage` 76 KB). amd64 additionally installs `intel-media-va-driver-non-free` at **40,457 KB — on its own, comparable to the entire X11 stack.**

### 3.2 CVEs — measured against the real package set

`debsecan --suite trixie` run inside the built full image, 2026-08-10 **[MEASURED]**: **687 open CVE/package pairs across 132 distinct packages.**

| Removal | CVE pairs removed | Which |
|---|---|---|
| X11 packages | **3** | `xvfb` ×3 |
| Wayland packages | **26** | `xwayland` ×25, `xdg-desktop-portal` ×1 |
| Both | **32** (4.7% of 687) | + `xserver-common` ×3 |

Top open-CVE packages, none of them display-stack: `python3.13` / `libpython3.13-*` 35 each · `libsoup-3.0-*` 35 each · **`xwayland` 25** · `libavcodec61` / `libavutil59` / `libswresample5` 23 each · `libopenexr-3-1-30` 17 · `perl-base` 15 · `openssh-server` / `openssh-client` / `openssh-sftp-server` 10 each.

Two things follow:

- **`cage` and `labwc` both `Depends: xwayland`** **[MEASURED]** via `apt-cache rdepends`. Choosing Wayland does not remove the X server from the image; it swaps `Xvfb` (3 CVEs) for `Xwayland` (25).
- **The X11 *client* libraries never leave.** `libX11.so.6` is present in all four variants including "drop both", because `chromium` depends on it **[MEASURED]**. `libx11-6`, `libxcb1`, `libxext6`, `libxfixes3`, `libxrandr2`, `libxi6`, `libxtst6` all carry **0** open CVEs today anyway **[MEASURED]**.

---

## 4. Recommendation

**Reject the framing. Do not "pick one stack" — split the package set by target.**

The two stacks are not competing implementations; they are the dependency sets of two different runtimes that happen to share one apt list (§1.1). Consolidating them into one *global* choice would break one of the two products. Splitting them costs nothing that currently works.

### R1 — Remove `labwc`, `cage`, `xdg-desktop-portal-wlr` from the **Dockerfile only**

Best measured ratio available in this whole investigation: **−10.2 MiB and −26 of the 32 available CVEs (81%)**, for a path that (§1.4) has no input injector, no working capture source, and no portal client.

- Risk: forecloses GPU-passthrough container sessions using cage. But `useCage` requires `/dev/dri` **and** `cage` on PATH (`pool.go:281`); with cage absent, `useCage` is false and the session takes the Xvfb branch — which keeps hardware encode (tier is still VA-API) and is the only branch that presently works. **Net behaviour change: none that functions today** — **[ASSUMED]**, since I could not run a GPU container to confirm the cage branch actually fails rather than half-works.
- Reversible in one line, and `scripts/check-image-packages.sh` forces the diff through review.

### R2 — Remove `matchbox-window-manager` and `x11-xserver-utils` from **`build.sh`'s rootfs only**

**−31 MiB** on the bare-metal image (almost all of it `cpp`, §1.5), 0 CVEs. `build.sh` installs no `Xvfb` **[READ]**, so no X server can exist there, so `matchbox` cannot run and `display.detectCompositor()` cannot return `"x11"` — `xrandr` is unreachable. This is pure dead weight on that target.

- Prerequisite: `check-image-packages.sh` must be taught to read `build.sh`'s two lists, or this drifts straight back (§1.6).

### R3 — Keep X11 in the container image. Do not drop `xvfb`/`xdotool` yet.

It is the only path that carries a frame today (§1.2), the entire prize for removing it is **37.9 MiB and 3 CVEs**, and 31 of those MiB come from `x11-xserver-utils`→`cpp` rather than from X itself. Dropping X from the container is the *most* work and the *least* reward of the options on the table.

### R4 — Build the Wayland client that the measurements say is buildable

One small Go Wayland client, bound to protocols that both shipped compositors already advertise **[MEASURED]**, would retire `xdotool`, `wlr-randr`, `wlopm`, `lswt` and `wlrctl` at once — the last four of which are **missing binaries today**, i.e. this fixes four currently-broken features rather than merely relocating a working one:

- `zwp_virtual_keyboard_manager_v1` + `zwlr_virtual_pointer_manager_v1` → injection (replaces `services/input`'s X11 half; the `/dev/uinput` half stays for bare metal, where libinput *is* live)
- `zwlr_screencopy_manager_v1` / `zwlr_export_dmabuf_manager_v1` → capture, no portal needed (`grim` demonstrated it end to end **[MEASURED]**)
- `zwlr_output_manager_v1` → resolution/resize, replacing `stream.go`'s `xrandr --fb`
- `zwlr_output_power_manager_v1` (labwc only) → DPMS
- `zwlr_foreign_toplevel_manager_v1` (labwc only) → `wltoplevel`; **note this can never work under cage** **[MEASURED]**

Only after R4 lands, and only after a VA-API-under-Wayland run on real hardware, does dropping X11 from the container become a decision rather than a gamble.

### R5 — Two defects found in passing, unrelated to the choice

- `build.sh:249-270`'s remote-deploy apt list is broken (§1.6) — `avahi-*`, `dhcpcd5`, `wpasupplicant`, `openssh-server` are passed as arguments to `flatpak`.
- `gpu.probeVAAPI` gates on the deprecated `vaapih264enc` (`gpu.go:362`) while the modern `va` plugin is already installed **[MEASURED]**. When Debian retires `gstreamer1.0-vaapi`, hardware encode silently falls back to software VP8.

---

## 5. What remains unverifiable here, and exactly what would verify it

I have Docker on an **arm64 macOS** host. That environment has **no `/dev/dri`, no `/dev/uinput`, no loadable `uinput` driver, and no GPU of any kind** **[MEASURED]**. The following are therefore **open**, and no result in this document should be read as covering them:

| # | Open question | What would actually settle it |
|---|---|---|
| V1 | Does VA-API H.264 encode work under a headless wlroots compositor? | An amd64 (Intel iGPU) or AMD box with `/dev/dri/renderD128`. Run `cage` headless, then `gst-launch-1.0 <screencopy or pipewire source> ! vapostproc ! vah264enc ! fakesink -v` and confirm non-zero output plus a driver name in `vainfo`. Compare against the same encode under Xvfb + `ximagesrc` on the same box. **Cannot be emulated — qemu has no VA-API.** |
| V2 | Does `vaapih264enc` still exist on the trixie image on real hardware, or only `vah264enc`? | Same box: `gst-inspect-1.0 vaapih264enc` with a DRM node present. This decides whether `gpu.go:362`'s gate is already wrong or merely will be. |
| V3 | Does a `zwlr_virtual_pointer`/`virtual_keyboard` client actually move the cursor in a **native Wayland** client (not XWayland)? | Write the R4 client, run `cage -- <a wayland-native app>`, inject, observe. The protocols are advertised **[MEASURED]**; that they are *implemented usefully* by cage 0.2.0 is **[ASSUMED]**. |
| V4 | Do XTEST events from `xdotool` under cage reach native Wayland clients? | Same rig. My expectation is **no** (§2.1c) and I did not test it. If someone repeats my `xdotool key a` measurement and reports it as "input works under Wayland", that is the trap. |
| V5 | Does the cage branch in `pool.go` fail cleanly or half-work on a GPU box? | Run the published image with `--device /dev/dri` on a VA-API host and watch for `[stream] cage launch failed … falling back to Xvfb`. R1's "no behaviour change" claim rests on this. |
| V6 | Do the amd64 size numbers match the arm64 ones? | Build the four variants on an amd64 runner. My amd64 figures are package-metadata arithmetic, not built images. |
| V7 | Is `mesa-vulkan-drivers` + `libllvm19` (220 MiB, 4.5× the entire display-stack prize) actually needed? | Out of scope here, but it is the size question that should have been asked first. |

One process note, in the spirit of not trusting a green result: **my first cage test reported a false negative.** I set `WAYLAND_DISPLAY=wayland-1` while cage created `wayland-0`, and concluded cage would not start headless. It starts fine. Had I stopped there, this document would have said "Wayland cannot run headless in a container" — confidently, and wrongly. Every measurement above was re-run after that, and the reproduction commands are in Appendix A so the next person can disagree with me from evidence.

---

## Appendix A — reproducing the measurements

All work was done in a scratch directory outside the repo. **No repository file was modified by this investigation** other than the addition of this document. The harness was four throwaway Dockerfiles and two shell scripts:

1. **Package-closure arithmetic.** `debian:trixie-slim`, non-free enabled, then `apt-get install -s --no-install-recommends <set>`; the closure is the `^Inst ` lines. Diff the full closure against each reduced closure; sum `apt-cache show <pkg> | Installed-Size` over the difference.
2. **Real image sizes.** Four Dockerfiles identical to `Dockerfile:125-147`'s apt invocation with exactly one line varied (`full`, `nox11`, `nowl`, `neither`), built `--platform linux/arm64` with BuildKit apt cache mounts, measured with `docker image inspect --format '{{.Size}}'`.
3. **CVEs.** `debsecan --suite trixie` inside the `full` image; attribution by exact package-name match against the closure-difference lists.
4. **Wayland globals.** A throwaway image `FROM` the measured full image adding `wayland-utils procps` (and, for the capture test, `grim`). Then:
   ```
   export XDG_RUNTIME_DIR=/tmp/xdg WLR_BACKENDS=headless WLR_RENDERER=pixman \
          WLR_LIBINPUT_NO_DEVICES=1 LIBSEAT_BACKEND=builtin
   cage -- sleep 8 &
   WAYLAND_DISPLAY=wayland-0 wayland-info | grep interface:
   WAYLAND_DISPLAY=wayland-0 grim /tmp/shot.png
   ls -l /proc/<cage-pid>/fd | grep /dev/input      # → empty
   ```
   `labwc -s "sleep 5"` for the labwc column.
5. **XWayland/xdotool.** `cage -- bash -c 'echo $DISPLAY; xdotool getdisplaygeometry; xdotool key a'`.

The `wayland-utils`, `procps`, `grim` and `debsecan` packages exist **only** in throwaway probe images derived from the measured one. They are not proposed for, and were not added to, the shipped package set.

---

## Appendix B — bookkeeping

`docs/decisions.md` is the repo's decision log and is append-only; its index currently ends at **D99**, so the entry recording whichever of R1–R5 is executed should be **D100**, with a row added to the index and a link back to this file. That file is owned by another workstream at the time of writing, so the entry is not made here — this document is the evidence D100 should cite, not a substitute for it.
