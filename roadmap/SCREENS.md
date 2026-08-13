# Screens — how a browser-rendered desktop spans more than one display

**Status: built, and its placement mechanism is proven in CI. The real launcher
driving real browsers onto real outputs is NOT yet verified** — see "What is
built as of 2026-08-12" and "What the QEMU verification actually needs" below,
which are the authoritative sections and say precisely which half is which.

This began as a design note, and the reasoning above the status sections is
still the design argument rather than a report on shipped code. It was written
because the question "what happens with two monitors" has an answer that is not
obvious for an OS whose desktop is a web app, and because most of the machinery
the answer needs was already in the tree for other reasons.

Everything asserted below about existing code was read from the code, and the
file is named each time so it can be checked rather than believed.

---

## The awkward part, stated first

Vulos renders its desktop in a browser. A browser window belongs to one display.
So "the desktop spans three monitors" has to mean one of two quite different
things, and they are not interchangeable:

**(a) One window, one enormous viewport.** The compositor joins the outputs into
a single logical surface and the browser fills it. Simple to reach — it is
roughly what happens today if a compositor is configured that way — and it makes
the shell responsible for laying out across a 5760px-wide viewport with bezels
in the middle of it. Windows straddle gaps. A maximised window is unusable. The
shell would need to learn where the seams are, which is exactly the knowledge
the compositor already has and would be throwing away.

**(b) One browser instance per output, sharing one session.** Each screen is an
independent viewport onto the same desktop. Maximising fills *that* screen. A
window dragged off the right edge of one arrives on the next. The shell never
needs to know a bezel exists, because each viewport is an ordinary
single-display viewport.

(b) is the right shape for this product, and the reason is not aesthetic: the
hard part of (b) — keeping several viewports coherent over one shared state — is
already built.

---

## What already exists

**Outputs are modelled.** `backend/services/display/display.go` has an `Output`
with `Name`, `Connected`, `Enabled`, `Primary`, `Resolution`, `Refresh`,
`Position` (e.g. `"0x0"`) and available `Modes`, enumerated through `wlr-randr`
on Wayland or `xrandr` on X11, with `SetResolution` and `EnableOutput` to change
them. `Position` is the important one: the geometry a multi-screen arrangement
needs is already a first-class field, not something to invent.

**A multi-output compositor ships in the image.** `scripts/build-sh-packages.txt`
installs both `cage` and `labwc` into the rootfs. cage is a single-application
kiosk shell — it is what `vulos-kiosk` uses today, and it is deliberately
one-window-one-output. labwc is a full wlroots compositor and does handle
multiple outputs. Moving from one to the other is a change of kiosk launcher,
not a new dependency.

**Several viewports can already share one shell coherently.** This is the part
that would otherwise be the whole project.
`frontend/src/providers/shellSession.ts` exists because shell state persisted to
one localStorage key on a debounce while every tab kept its own copy, so two
tabs were last-writer-wins: open a window in one, move a window in the other,
and the next save silently wiped the first. It replaces that with a leader/
follower session over a `BroadcastChannel`, and
`frontend/src/providers/ShellProvider.tsx` publishes the serialisable shell
state through it.

That is precisely the primitive (b) needs. A second browser window on a second
output is, to that code, a second tab.

---

## The shape this suggests

- **One kiosk browser per connected output**, each pointed at the same local
  server, joined by the existing shell session. Windows and desktops live in the
  shared state; which viewport *shows* a given desktop is per-viewport.
- **The compositor keeps the geometry.** labwc knows the outputs, their
  positions and their scale factors. The shell should ask, not model — the
  `Output` type above is already the right vocabulary for the answer.
- **A window belongs to a desktop; a desktop is shown on a screen.** Dragging a
  window "to the next monitor" is then moving it between desktops, which the
  shell already supports, rather than a new spatial concept.

## What is built as of 2026-08-12, and what is NOT

Stated precisely, because the built half looks like a working feature and is
not one yet.

**Built and tested.** `frontend/src/providers/screenIdentity.ts` parses a screen
identity out of a URL query string, and `frontend/src/shell/TopBar.tsx` renders
the output's name in the top bar when more than one screen was launched. 14
tests, mutation-tested three ways: removing the index>total consistency check,
loosening the connector-name pattern, and making `isMultiScreen` accept
`total=1` each turn it red.

**NOT built: anything that SETS the parameter.** Neither `build.sh`'s
`vulos-kiosk` script nor `backend/cmd/init/main.go` writes `?screen=`,
`?screens=` or `?screenIndex=` into the URL it opens. Verified by grep, not
assumed.

The consequence, plainly: `readScreenIdentity()` returns null on every real
boot today, `isMultiScreen()` is therefore always false, and the indicator
renders for nobody. **The feature is reachable from no surface.** Its tests all
pass, which is exactly what makes this worth writing down — a green suite over
an unreachable feature is the shape this repository has shipped before, and the
tests are honest about the parser while saying nothing about whether anything
calls it.

**What the launcher side needs**, so the remaining work is not re-derived:
enumerate connected outputs (`backend/services/display/display.go` already
models them, including `Position`), start one browser per output under a
multi-output compositor — `labwc` is already in
`scripts/build-sh-packages.txt`, alongside the single-output `cage` used today
— and append `screen=<output name>&screens=<n>&screenIndex=<i>` to each URL.
The parser refuses partial or inconsistent input, so a launcher that sets only
some of the three gets single-screen behaviour rather than a broken chip.

Two constraints that must survive it, both currently asserted by
`backend/internal/docsref/kiosk_test.go`: the browser list and the wlroots
environment are duplicated between `build.sh` and `cmd/init/main.go` and must
stay identical, and a headless box must still exit 0 rather than restart
forever.

## The one genuinely unknown mechanism, narrowed

Everything else about the launcher is understood. The part that was not, and is
now half-answered: **how do you make browser instance N appear on output N?**

A browser cannot choose its own Wayland output. The compositor places it. So
the launcher cannot simply start two browsers and hope.

**CORRECTION.** An earlier version of this section said a labwc window rule
takes an optional `output` attribute. It does not. That came from a web search
summary rather than the manual, and it was wrong. Read from
`labwc-config(5)` and `labwc-actions(5)` directly, the mechanism is:

```xml
<windowRules>
  <windowRule identifier="vulos-screen-1" matchOnce="yes">
    <action name="MoveToOutput" output="HDMI-A-1" />
  </windowRule>
</windowRules>
```

- `<windowRule>` matches on **`identifier`** (app_id on Wayland, WM_CLASS on
  X11), **`title`**, or **`type`**, with **`matchOnce`** to apply only to the
  first instance. It carries no output attribute of its own.
- Placement comes from the **`MoveToOutput` action**, whose exact attributes
  are `output="<name>"`, or `direction="left|right|up|down"` with
  `wrap="yes|no"` when `output` is omitted.
- `FocusOutput` exists with the same attributes and is NOT what this needs — it
  focuses the topmost window on another output rather than moving one.
- The output name is the connector name, e.g. `HDMI-A-1` — the same string
  `vulos-kiosk` already derives from `/sys/class/drm/*/status` and passes to the
  shell as `screen=`. One name, one source, both sides.

So each browser instance needs a distinct **app_id** for `identifier` to match.
`wlr-randr` still handles output geometry, and `services/display` already
shells out to it.

So the remaining work is: give each instance a distinct identity, generate a
labwc rc.xml with one rule per output binding that identity to that output, and
pass each instance its own `screen`/`screens`/`screenIndex` parameters (the
parser and indicator for those are built, tested and rendered — see above).

**VERIFIED 2026-08-13 against labwc 0.8.3.** The generated rc.xml was fed to a
real labwc running headless (`WLR_BACKENDS=headless`, `WLR_HEADLESS_OUTPUTS=2`,
pixman renderer) in a Debian trixie container. It produced no errors.

That result only means something because the same check was run against two
deliberately broken configs, and both were rejected:

    MoveToOutput → NotARealAction
      [ERROR] [../src/action.c:503] Invalid action: NotARealAction
      [ERROR] [../src/action.c:488] Invalid argument for action INVALID: 'output'

    </windowRules> removed
      Entity: line 1: parser error : Opening and ending tag mismatch
      [ERROR] [../src/config/rcxml.c:1396] error parsing config file

So labwc validates both the action name and the XML structure, and accepts
ours. `MoveToOutput` is a real action in 0.8.3 and `output` is a valid argument
to it — which was the specific thing taken from the manual and never run.

**PLACEMENT VERIFIED 2026-08-13**, by `scripts/smoke-multiscreen.sh`. A real
labwc runs against two headless wlroots outputs, two windows are launched with
the titles the shell actually sets, and each output is photographed.

The control is what makes it evidence. Two windows landing on two screens
proves nothing alone — a compositor might distribute them by default, in which
case the rules are decoration. So the scenario runs twice:

    with rules:    HEADLESS-1 = 9922 bytes   HEADLESS-2 = 10502 bytes
    without rules: HEADLESS-1 = 2759 bytes   HEADLESS-2 = 14678 bytes

With the rules, each output holds exactly one window. Without them, both
windows pile onto HEADLESS-2 — overlapping, one partly behind the other — and
HEADLESS-1 is empty. So labwc does NOT distribute windows across outputs on its
own, and the windowRule/MoveToOutput pair is doing the work.

That failure mode is precisely what this design exists to prevent, and it is
now reproducible on demand rather than imagined.

Still short of a real boot: the harness uses `foot` on HEADLESS-N outputs
rather than `cog` on DRM connectors, because the mechanism under test is
labwc's and not the browser's. The last mile is a QEMU boot with two virtual
displays. A wrong rule fails the way everything in this area fails: silently,
with every browser on one monitor and nothing in any log saying why. Verify by
running labwc with two virtual outputs under QEMU and reading a screendump —
the verification this feature needs regardless.

## What the QEMU verification actually needs (checked 2026-08-13)

The remaining claim is that the real launcher places real browsers on real
outputs. Precisely what stands in the way, measured rather than assumed:

- **qemu is present** on the dev host (`qemu-system-x86_64`, `-aarch64`).
- **Bootable images exist** — `output/vulos-live-arm64.img` and the two
  installed-disk images the netboot smoke test produces.
- **`debootstrap` is ABSENT**, so those images cannot be rebuilt here. They
  predate the multi-output launcher, the screen-identity parameters and both
  kiosk fixes made today.

So booting what is on disk would exercise the OLD kiosk and prove nothing about
the new path. The verification needs an image built on a Linux host (or in a
privileged Linux container with debootstrap), after which
`scripts/netboot-install-smoke.sh` supplies the machinery: it already boots
QEMU, already screendumps, and already measures pixels — a second
`-device virtio-gpu-pci` and a screendump of each head is the whole change.

Worth stating because it is easy to get wrong twice: the blocker is the IMAGE
BUILD, not QEMU and not the harness.

**Attempted 2026-08-13 in a privileged arm64 container** (repo mounted
read-only, output to /tmp, so nothing root-owned could land in the tree — worth
keeping, and it worked: the tree was untouched afterwards). The build **took
down the Docker daemon**: `error waiting for container: unexpected EOF`,
followed by the socket becoming unreachable. OrbStack had already been logging
NFS warnings earlier in the day, so it was likely strained before this started.

**Attempted a SECOND time after restarting OrbStack, with the container capped
at `-m 6g --memory-swap 6g` so it could not exhaust the VM. The daemon died
again.** So it is NOT container memory exhaustion — the cap made no difference.
A privileged build performs loop-device and kernel-level operations (chroot,
squashfs, initramfs, partitioning), and something in that destabilises the
OrbStack VM itself rather than the container.

**Do not attempt this a third time on macOS.** Build the image on a real Linux
host. Two crashes with different memory configurations is enough evidence, and
each one takes the daemon down with it — which also stops SCREENS-01 and the
NAT harness from running locally.

Progress worth keeping from attempt two: it got as far as building all Go
binaries and failed at `npm: not found` (install `npm`, not just `nodejs`).
And a trap for whoever retries — `cp -a /src /build` copies the repo's existing
`output/` directory, so stale images appear in the destination and look like a
successful build. Delete `output/` after copying, and capture the build's real
exit code rather than a pipeline's.

Note the collateral: while the daemon is down, `SCREENS-01` and the NAT harness
cannot run locally either, since both are container-based. CI is unaffected.

## One thing that looked unresolved and is now settled: the dead session bus

Worth its own section because it was, for a day, the most serious open question
in this file — and because it turned out to be an accusation the harness made
against shipping code rather than a defect.

`scripts/vulos-kiosk.sh` exports
`DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-unix:path=/dev/null}"` on
the software-rendering path, and `backend/cmd/init/main.go`'s `startKiosk` does
the same. That path is taken by **every box with no hardware GPU**, on the
single-output branch as much as the multi-output one — which is what every real
install boots. ROUND 5 of `scripts/smoke-kiosk-multiscreen.sh` recorded cog
dying instantly with `Failed to fully launch dbus-proxy` and blamed that
setting. If the blame were correct, every GPU-less box showed a black screen.

**Measured 2026-08-13. It is not correct.** One privileged arm64 debian:trixie
container, cog 0.18.4 / WPE WebKit 2.48.3 / cage 0.2.0 on
`WLR_BACKENDS=headless` + `WLR_RENDERER=pixman`, a python3 stub server, and
four runs differing only in the bus:

    a  /dev/null bus, --platform=wl    Loaded successfully.  +  GET / 200
    b  bus unset                       Loaded successfully.  +  GET / 200
    c  dbus-run-session, real bus      Loaded successfully.  +  GET / 200
    d  /dev/null bus, no --platform    Loaded successfully.  +  GET / 200

Two independent success signals on purpose: cog's own `Loaded successfully.`
and the server's `GET / 200`. A browser that starts and paints nothing is the
failure this project keeps getting fooled by, so a live process was not
accepted as a pass. Arm (a) is the command line `cmd/init` builds and arm (d)
the one the shell script runs — they differ, and both were run rather than
assumed equivalent.

The dead bus costs exactly one line, `Failed to connect to bus: Could not
connect: Connection refused`, which is the one-shot refusal the setting exists
to produce; arm (b) has no such line, so the variable was demonstrably in
effect. What makes the comparison evidence rather than one lucky run is the
control the original round lacked: the container was first proved to allow the
sandbox at all (`bwrap --unshare-user --unshare-pid --ro-bind / / /bin/true` →
OK). Round 5 ran before `--cap-add SYS_ADMIN --security-opt seccomp=unconfined`
were added, and xdg-dbus-proxy is itself launched inside that sandbox — so
rounds 5 and 6 are one environmental failure and the dbus-proxy error was its
symptom.

Two corrections came out of it. The comments justifying the line named
**Chromium**, which the bare-metal image does not install at all —
`scripts/build-sh-packages.txt` ships `cog` and chromium appears only in the
Dockerfile, which never runs this code. And `bubblewrap` + `xdg-dbus-proxy`,
named nowhere in that package list, arrive as dependencies of cog even under
build.sh's own `--no-install-recommends`, so the image does have the sandbox
the browser expects.

**Still not claimed:** any of this on real DRM hardware. Headless wlroots, no
GPU, no `/dev/dri` — the same limitation as commit `a10c47fc`, which verified
cog loading a page and is the run this one was designed to discriminate
against.

## What is genuinely unresolved

Named rather than glossed, because these are the parts that will decide whether
it works:

1. **Which viewport owns the leader role**, and what happens when that screen is
   the one unplugged. `shellSession.ts` has a leader/follower model; a display
   disappearing is not the same event as a tab closing.
2. **Per-output scale.** A 4K screen beside a 1080p one needs different device
   pixel ratios in two browser instances of the same session. The shared state
   must not carry one viewport's pixel assumptions to the other.
3. **Input focus across instances.** Two browser windows, one keyboard. The
   compositor decides focus; the shell has to follow it rather than compete.
4. **Cost.** One browser process per screen is not free on a 2GB box, which is
   the floor this project advertises. Whether three monitors is a supported
   configuration on minimum hardware is a product decision, not a technical one.
5. ~~**What a headless box does.**~~ **SETTLED 2026-08-13, and now executed
   rather than read.** The multi-output work did not turn the headless path
   into an error path: with no render nodes and no connected connectors,
   `scripts/vulos-kiosk.sh` logs `no display found` and exits 0, before the
   multi-output branch is anywhere near.

   Proved by running the file, not by grepping it —
   `TestKioskHeadlessExitsZero` in `backend/internal/docsref/kiosk_test.go`
   executes the real script with empty DRM roots and asserts the status, the
   reason, and that it DECLINED (a browser that started and quit would satisfy
   the first two while meaning the opposite). Running it needed one seam,
   `VULOS_DRI_ROOT`, mirroring the `VULOS_DRM_ROOT` that already existed; a
   real boot sets neither.

   The reason a grep was not enough is `Restart=on-failure` in
   `vulos-kiosk.service` (build.sh). A non-zero exit here is not a bad log
   line — it is systemd restarting the kiosk every three seconds forever on a
   machine with no screen to show the reason on. Mutation-tested both ways:
   `exit 0` → `exit 1` fails the test, and detection that ignores the override
   fails it too.

## Why write this down before building it

The kiosk work that preceded this note took several rebuild cycles to find that
a systemd `Condition` skips silently, that a diagnostic in the journal is
unreadable from a box showing no desktop, and that the test harness had no GPU
at all. Each was a case of the answer existing somewhere nobody was looking.

The same risk applies here in a specific way: (a) and (b) look similar from a
screenshot of one monitor, and the difference only appears on a desk with two.
Choosing deliberately, and recording why, is cheaper than discovering the choice
was made by whichever compositor happened to be launched.
