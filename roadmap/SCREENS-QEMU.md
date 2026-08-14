# SCREENS-02 — the two-display QEMU boot

Method, findings and limits for `scripts/smoke-multiscreen-qemu.sh`, the
harness that boots a real Vulos image with two virtual displays and
photographs both of them.

`roadmap/SCREENS.md` is the authority on the feature. This file exists so the
*method* — and the two things it got wrong before it got them right — is
written down somewhere that is not a commit message.

---

## Why this harness exists

Everything else about multi-screen was proved somewhere that is not a booted
OS. `scripts/smoke-multiscreen.sh` proves labwc's `windowRule` +
`MoveToOutput` place windows, using a config the test wrote, `foot` terminals,
and wlroots' *headless* backend. `scripts/smoke-kiosk-multiscreen.sh` proves
the real launcher enumerates outputs and takes the multi-output branch, in a
container, against a faked `/sys/class/drm`, with the compositor never
starting.

What was left was the whole claim: **the real launcher, on a real boot,
driving real browsers onto real DRM connectors.**

## What it found on the first run

The multi-output kiosk **did not work at all.** Not "placed the windows
wrongly" — no browser started on either monitor.

    [ERROR] [../src/common/spawn.c:120] Failed to execute primary client
            /run/vulos-kiosk/session.sh: Permission denied
    [ERROR] [../src/server.c:148] spawned child 590 exited with 1

The cause is neither ours nor labwc's, and it is not a QEMU artefact. Debian's
initramfs mounts `/run` **noexec** and then *moves that very mount* onto the
real root, so `/run` is noexec for the entire life of every booted system.
Read out of the image's own `initrd.img` rather than assumed:

    mount -t tmpfs -o "nodev,noexec,nosuid,size=${RUNSIZE:-10%},mode=0755" tmpfs /run
    ...
    mount -n -o move /run ${rootmnt}/run

`vulos-kiosk-genconfig` writes `session.sh` there and `chmod +x`'s it. The
chmod succeeds and buys nothing: `execve()` on a noexec mount returns `EACCES`
whatever the mode bits say. `/run` is still the right home — the installed
root is a read-only dm-verity squashfs — so the fix was to hand the file to an
interpreter, which reads it as data:

    exec labwc -C "$cfg" -S "/bin/sh $cfg/session.sh"

**Only the multi-output path was affected.** The single-output path execs
`cage -- cog "$URL"` with no generated script, which is why every one-monitor
box — that is, every box anyone has ever booted — was fine.

Worth naming why nothing caught it. Every existing kiosk test asserts that
**labwc launched**, and labwc *did* launch. Not one assertion looked past the
compositor to whether a browser appeared. `TestKioskSessionIsRunByAnInterpreter`
now asserts on the argv the launcher actually exec'd, and is mutation-tested:
restoring the old `-S` turns it red.

## The QEMU shape, and two things assumed wrongly first

**Two `-device virtio-gpu-pci` is not the right shape** — but not for the
reason first written down here. The first draft claimed the two cards'
connectors would both be called `Virtual-1` and collide in the launcher's name
derivation. That was reasoning, and booting it disproved it: DRM numbers
connectors per *type* across the system, so the second device's connector is
`Virtual-2` and nothing collides.

    [drm] pci: virtio-gpu-pci detected at 0000:00:02.0
    [drm] forcing Virtual-1 connector on
    [drm] Initialized virtio_gpu 0.1.0 for 0000:00:02.0 on minor 0
    [drm] pci: virtio-gpu-pci detected at 0000:00:03.0
    [drm] forcing Virtual-2 connector on
    [drm] Initialized virtio_gpu 0.1.0 for 0000:00:03.0 on minor 1

The real reason is narrower: two devices are two GPUs on two DRM minors, which
puts the run on wlroots' multi-GPU path — a rarer hardware shape than the one
this feature is for. `max_outputs=2` gives one card with two connectors, which
is what a desk with two monitors on one graphics card looks like.

**QEMU does not give the guest a second output on its own.** With
`-display none`, only scanout 0 is ever advertised as enabled: the guest's
second connector stays *disconnected*, head 1's framebuffer sits at QEMU's
unconfigured 640x480 forever, and the launcher counts ONE screen. A harness
that did not check this would have been exercising the single-screen path
while reporting on two.

`video=Virtual-2:1024x768e` forces the connector on from the kernel side. The
mode is not arbitrary: `1280x800` is rejected by the virtio-gpu driver
(`User-defined mode not supported`) and a bare `:e` lands on 5120x2160 — the
largest mode in the generated EDID — which is an absurd viewport to render
under pixman and would also make "the two heads differ" a statement about
geometry instead of content.

## The discriminator, and exactly what it cannot see

Two heads both showing a desktop is necessary and **not sufficient** — that is
satisfied by a single window mirrored to both outputs. The harness therefore
also requires the two heads to differ, in the top band where
`frontend/src/shell/TopBar.tsx` renders `Screen N · <connector>`.

"The frames differ" is *itself* not safe, and the harness's own dry run proved
it: two blank text consoles alternated between two hashes because of the
blinking cursor, and the heads are photographed milliseconds apart. So each
run measures its own noise floor — three shots, `head0` / `head1` / `head0`
again, so the self-comparison brackets the cross-comparison — and the
cross-difference must strictly exceed the self-difference.

**What it can distinguish:** one browser per output, from both browsers on one
output with the other left empty. That is the failure `roadmap/SCREENS.md`
exists to prevent.

**What it cannot distinguish:** a correct mapping from a *swapped* one.
Nothing here reads text off the screen, so a configuration that put
`Virtual-1`'s browser on `Virtual-2` and vice versa passes every assertion in
the file. Closing that needs OCR or a per-screen colour, and neither exists.

## The controls

A green run means nothing without a run that should go red and does.

`--control break-title` keeps everything identical — two heads, two
connectors, two browsers, labwc, the real launcher — and breaks only the
contract the placement rests on. `VULOS_KIOSK_URL` is set, via
`systemd.setenv` on the kernel cmdline, to a URL that already carries a query
string; `vulos-kiosk-genconfig` appends its own `?screen=…`, and the shell
sees `vulos-control=1?screen=Virtual-1` as one parameter with no `screen` in
it at all. `parseScreenIdentity()` then returns null.

That breaks **two** things, which is worth stating rather than glossing: no
identity means no window title (so no `windowRule` matches and placement dies)
*and* no screen chip (so the heads have no per-screen content to differ by).
The evidence block reports P0, P1 and D separately for exactly this reason —
which one goes red says which half was doing the work.

`--control single-head` is the cheap one: one head, so there is genuinely no
second output.

## How the image is patched, and what is NOT changed

The kernel cmdline lives in a FAT32 ESP inside the image, edited with mtools
at the partition offset parsed out of the GPT. Three changes, all additive or
diagnostic:

- `quiet` dropped, so the DRM connector decisions are readable on serial.
- `video=Virtual-N:1024x768e` added, per head.
- `systemd.setenv=VULOS_KIOSK_URL=…` added, in `--control break-title` only.

`splash`/plymouth are left exactly as shipped, because plymouth owns the
framebuffer early in boot and removing it would change what the compositor
inherits. No byte of the OS's own files is modified. The patch is read back
out of the image before booting — an unpatched cmdline shows one connector and
looks exactly like a product failure.

The harness **refuses to guess an image**. `output/vulos-live-arm64.img` on a
developer machine predates the multi-output launcher entirely, and booting it
shows one screen and looks like the feature is broken.

## Standing limits

- QEMU virtio-gpu under `hvf`, software rendering (labwc falls back to
  `kms_swrast`; `eglInitialize` fails with `EGL_NOT_INITIALIZED`). **No
  hardware GPU and no physical monitor has been involved at any point.**
- Two outputs of identical geometry. Mixed resolutions and mixed scale — item
  2 in `roadmap/SCREENS.md` — are untouched by this.
- Nothing here observes hotplug, unplug, or the leader/follower demotion path
  (item 1). The second connector is forced on at boot and stays.
- A swapped mapping passes. See the discriminator section.
