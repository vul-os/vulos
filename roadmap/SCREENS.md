# Screens — how a browser-rendered desktop spans more than one display

**Status: TESTED AND FAILED. As of 2026-08-14 the real launcher has been booted
on two displays — twice — and it does NOT place one browser per output.** Head
0 renders the real Vulos UI on a real DRM connector; head 1 is a single flat
colour. Both browsers start, neither dies, and labwc drives both outputs. The
windows simply both land on output 1.

**The placement design cannot work as built**, and that is now settled from
labwc's source rather than inferred from a screendump — see "What the QEMU
verification actually found" below. The fix is known and unverified.

Read this before trusting any other line in this file: **everything that was
green stayed green while the feature was dead.** `SCREENS-01` passes, the
frontend suite passes, twelve CI jobs pass, and the multi-output kiosk had
never once worked on a real boot. Two of the defects were found by booting it
and could not have been found any other way. The CI gate is honest about what
it tests — labwc places windows when given rules that match — and that turned
out not to be the question.

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

**~~NOT built: anything that SETS the parameter.~~ NO LONGER TRUE — corrected
2026-08-13.** On 2026-08-12 neither launcher wrote `?screen=`, `?screens=` or
`?screenIndex=` into the URL it opened, so `readScreenIdentity()` returned null
on every real boot, `isMultiScreen()` was always false, and the indicator
rendered for nobody. That was the "green suite over a feature reachable from no
surface" shape this repository has shipped before, which is why it was written
down.

The launcher work closed it. `scripts/vulos-kiosk.sh:48-49` appends all three
on the single-screen path (with `screens=1`, so the parser is exercised on
every boot rather than only on the rare one), and
`scripts/vulos-kiosk-genconfig.sh:71` writes a distinct triple per output on
the multi-output path. Verified by grep, as the original claim was.

**~~One asymmetry, recorded rather than fixed~~ — FIXED 2026-08-13.**
`backend/cmd/init/main.go` opened a bare `http://localhost:8080` while the
shell copy had been appending the triple since this work started. It now
derives the connector name the same way — first connected
`/sys/class/drm/*/status`, card prefix stripped — and emits the same
single-screen triple, behind a `drmRoot` seam mirroring the shell's
`$VULOS_DRM_ROOT`.

Where no connector is readable, the parameters are **omitted entirely rather
than guessed**. `vulos.kiosk=force` under virtio-gpu is the real instance of
this: QEMU reports no connected output while still rendering.
`readScreenIdentity()` refuses a partial identity, so an invented name is
either discarded — work that changes nothing — or believed, in which case the
window title and any future `MoveToOutput` rule name an output the box is not
on.

This was never user-visible, and that caveat stands unchanged: the initramfs
path runs cage, one window on one output, and `isMultiScreen()` needs `total>1`
to render anything. It would have become visible the first time that path drove
a second output.

**The drift itself is now pinned** by `TestKioskURLIdentityMatchesInit`
(mutation-tested, 9 mutations, including dropping `screenIndex` and
re-hardcoding the URL). Worth naming why it happened: the URL was the *third*
thing duplicated across the two launchers, and the only one nothing was
checking — the browser list and the wlroots environment were already guarded,
which is exactly why this one drifted in silence.

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

## What the QEMU verification actually found (RUN 2026-08-14)

**It ran. It failed. Full method, controls and limits in `roadmap/SCREENS-QEMU.md`.**

Two boots of `scripts/smoke-multiscreen-qemu.sh`, the second against an image
carrying both kiosk fixes (run `31764201607`, `8bc592a7`):

| | |
|---|---|
| launcher sees two real DRM connectors | yes |
| takes the labwc multi-output branch | yes |
| both browsers start, neither dies | yes (PIDs 496, 600, 601) |
| labwc drives both outputs | yes — it mode-set head 1 to 5120x2160 |
| head 0 renders the real Vulos UI | yes — 672 colours, 99% fill |
| **head 1 renders anything** | **NO — 1 colour, 0% fill** |

Head 1's pixel hash was **byte-identical across both runs**. Nothing about the
second output changed.

**Three defects, none of them predicted anywhere in this file.**

1. **`/run` is mounted noexec**, so labwc could not exec the generated session
   script: `Failed to execute primary client /run/vulos-kiosk/session.sh:
   Permission denied`. Debian's initramfs mounts `/run` noexec and *moves that
   mount* onto the real root, so the `chmod +x` in the generator succeeds and
   buys nothing. **The multi-output kiosk had therefore never worked at all, on
   any boot, ever.** Fixed by running the session through an interpreter;
   pinned by a mutation-tested test.

2. **The window title was never set before login.** The `screen=` →
   `document.title` link was written only by `TopBar`'s `ScreenIndicator`
   effect, and `TopBar` mounts inside `DesktopCanvas` — after setup AND after
   login. A first boot shows the setup wizard, so no title was ever set. Fixed
   in `8bc592a7` by applying it at module scope in `App.tsx`. **Correct, and
   not sufficient — see 3.**

3. **Matching on `title` cannot work, at all.** Settled from labwc 0.8.3's
   source, not from a screendump: `enum window_rule_event` has exactly one
   member, `LAB_WINDOW_RULE_EVENT_ON_FIRST_MAP`, and `window_rules_apply()` is
   called from exactly one place — inside `view_impl_map()`, under
   `if (!view->been_mapped)`. Rules are evaluated **once, at first map, never
   on a title change**; `view_update_title()` refreshes the titlebar and emits
   `new_title` without re-applying anything.

   cog maps its surface **before any page JavaScript runs**, so the title
   labwc matches against is always the static one in `frontend/index.html`.
   Defect 2's fix lands too late **by construction** — module scope is still
   page JS, and no earlier position in the page exists. This is not a race that
   could be tightened; it is an ordering the protocol imposes.

**The fix, known and NOT yet verified:** match on `identifier` (Wayland app_id)
instead of `title`. app_id is set at toplevel creation, before the mapping
commit, so it is present at `ON_FIRST_MAP`. cog 0.18.4 accepts
`--application-id` and passes it to `xdg_toplevel_set_app_id()`; both instances
currently share the default `com.igalia.Cog`, which is why matching on
`identifier` does nothing today. Read from cog's and labwc's source; it needs
its own image build and boot before it may be called anything but a hypothesis.

**What this cost, and the lesson worth keeping:** the design note asserted that
placement was "the one genuinely unknown mechanism, narrowed" and that a labwc
`windowRule` was the answer. The syntax was verified against the manual and
against labwc itself — a made-up action and a malformed tag were both rejected,
so the check discriminated. It still established only that the *config parses*,
never that the *rule fires*. `SCREENS-01` then proved labwc places windows when
given rules that match, which is true and was never the question.

---

## What the QEMU verification needed (checked 2026-08-13, now historical)

Kept because it is the reasoning that got the verification built, and because
its central claim — that the blocker was a Linux host — was wrong in a way
worth remembering: `release.yml` already had a `workflow_dispatch` path that
builds both images on `ubuntu-latest` and publishes nothing, added for exactly
this purpose. The blocker was never the host. Nobody read the workflows.

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

## What was genuinely unresolved — all five now closed, in three different senses

Named rather than glossed, because these were the parts that would decide
whether it works. As of 2026-08-13 every one has been answered, but **not to
the same standard, and the difference matters more than the count**:

- **SETTLED** (item 5) — the thing itself was executed and a test drives it.
- **MEASURED AND DECIDED** (item 4) — real numbers, a recorded product
  decision, and a written list of what the numbers do not cover.
- **ADDRESSED IN CODE** (items 1, 2, 3) — a real defect found and fixed, with
  unit tests over a case constructed in software. None of these three has met
  two physical monitors, and two of them rest on a premise no one has observed.

Read the individual entries for which is which. A summary that flattened these
into "all resolved" would be the exact failure this file exists to prevent.

In all three of items 1, 2 and 3 **the defect was not the one predicted here** —
worth noting before trusting the framing of any remaining design note:

1. ~~**Which viewport owns the leader role**, and what happens when that screen
   is the one unplugged.~~ **ADDRESSED IN CODE 2026-08-13; unverified on real
   hardware.** Reading `shellSession.ts` found something sharper than this
   entry described: role only ever moved follower→writer. There was **no
   demotion path at all**, so once a tab decided it was the writer nothing
   could tell it otherwise.

   That is fine for a tab closing — it sends `bye` and is gone. It is not fine
   for an unplugged display, because a browser instance behind a dead output
   need not exit, and a backgrounded non-painting tab is exactly what browsers
   throttle: its heartbeat goes stale past `WRITER_TIMEOUT_MS`, a follower
   correctly promotes itself, and then the original **resumes**, still claiming
   writer. Two tabs writing localStorage and publishing state — the original
   last-writer-wins bug, re-entered from the one direction promotion-only logic
   could not close.

   `shouldStepDown` adds the missing transition, reusing `nextWriter`'s
   lowest-tabId tie-break so both sides converge without negotiation. Covered
   by a hook-level test that drops the writer's traffic **without unmounting it
   or sending `bye`** — the ungraceful path, since a test that calls the clean
   teardown proves nothing about a yanked monitor.

   **The caveat that keeps this out of "settled":** whether a compositor
   actually keeps a client alive when its output is unplugged is unverified,
   and sits on the far side of the same hardware gap as everything else here.
   The fix holds either way — if the process dies instead, the pre-existing
   crash path already covers it — but the premise itself has not been observed.

2. ~~**Per-output scale.**~~ **ADDRESSED IN CODE 2026-08-13; unverified on real
   hardware.** The hazard was not where this entry pointed. Nothing about
   resolution or scale reaches the shell at all — `screen=`/`screens=`/
   `screenIndex=` carry a connector name and an ordinal, nothing more. The leak
   was downstream: `ShellProvider.tsx` copied each window's raw CSS-px
   `position`/`size` verbatim into the published snapshot, `useShellSession.ts`
   broadcast it unconverted, and a follower applied it straight back through
   `RESTORE_STATE`. A window position meaningful on a 1920-wide writer is
   off-screen on a 3840-wide follower — a units bug wearing a layout bug's
   clothes.

   `frontend/src/providers/screenScale.ts` defines the canonical unit as a
   **fraction of the writer's own viewport**, deliberately not DPR-adjusted
   pixels: CSS px is already DPR-normalised for its own screen, so what differs
   between two instances is viewport *extent*, not DPR. `devicePixelRatio` is
   kept out of the conversion path and retained for diagnostics only.

   Two decisions worth not re-deriving. The unit boundary is enforced by
   **branded types**, so mixing raw px into a canonical field fails
   `tsc --noEmit`, which CI runs — a convention living only in a comment is the
   failure mode this file keeps warning about. And because
   `serializableShellState` feeds **both** localStorage and the cross-tab
   publish, the read side was made symmetric rather than converting only on
   publish; legacy untagged payloads are read as raw px, so an explicit
   `geomUnit: 'canonical-v1'` tag — not magnitude — is what distinguishes them.

   **Unverified:** the 4K-beside-1080p case is exercised by construction in
   tests, not by two real monitors.
3. ~~**Input focus across instances.**~~ **ADDRESSED IN CODE 2026-08-13;
   unverified on real hardware.** The entry predicted instances fighting over
   keystrokes. That half turns out to be safe by construction and was never the
   problem: a browser only dispatches `keydown` to the OS/compositor-focused
   top-level window, so an unfocused instance's listener never fires. "Act
   once" already held for the literal keypress.

   **The real bug was `activeWindow` crossing the cross-tab boundary.** It is
   the field `shell/useWindowShortcuts.ts` reads to decide which window a
   keystroke acts on, and it was part of the published shell state. Every
   `RESTORE_STATE` — including a follower applying a peer's mirrored publish,
   which fires on a ~500ms debounce whenever *anything* changes on the writer's
   desktop — adopted the writer's `activeWindow` verbatim. Because a follower
   can perfectly well be the focused viewport (leader/follower tracks
   persistence ownership, not compositor attention), an unrelated edit on the
   other screen silently reassigned this screen's keyboard target mid-use.

   Fixed with a `fromMirror` flag on `RESTORE_STATE`: a mirrored sync keeps
   this instance's own `activeWindow` if that window still exists, deferring to
   the writer only on first sync or once the local pick was closed remotely.
   `providers/viewportFocus.ts` adds a read-only focus primitive that never
   calls `.focus()`. A regression test flips which instance is focused and
   confirms the leader election is unaffected — the two concepts must not be
   allowed to merge.

   **Deliberately NOT done:** wiring `hasFocus` into the global keydown
   listeners in `shell/useWindowShortcuts.ts` and `App.tsx`. Those are already
   safe by browser event routing, and a focus gate there can only ever
   *suppress* input — if it were ever wrong, every shortcut on the box would
   stop working, which is a worse failure than the double-fire it would guard
   against. The primitive exists if a real need appears.

   **Unverified:** whether a real compositor drives two instances'
   `document.hasFocus()` apart across two physical outputs. This is also
   genuinely untestable in the suite — vitest's jsdom gives every test one
   shared global `document`, so two readers of the zero-arg `hasFocus()` always
   see the same value. The primitive takes an injectable host to prove the
   branching logic, which is not the same as proving the DOM plumbing.
4. ~~**Cost.**~~ **MEASURED AND DECIDED 2026-08-13 — full numbers, method and
   limits in `roadmap/SCREENS-COST.md`.** Three screens is a supported
   configuration on the 2GB floor, under the software-rendering path that was
   measured.

   Per screen, real `frontend/dist` build, whole cog process tree (cog, cage,
   WPEWebProcess, WPENetworkProcess, bwrap×4, xdg-dbus-proxy, dbus):

   | screens | RSS | PSS | marginal PSS |
   |---|---|---|---|
   | 1 | 469MB | 322MB | — |
   | 2 | 947MB | 491MB | +169MB |
   | 3 | 1414MB | 647MB | +155MB |

   **The RSS/PSS gap is the finding, not a footnote.** Naive summed RSS charges
   every process the full size of each page it maps, shared or not, so it puts
   the marginal cost of a screen (~475MB) at almost the same as the first one —
   which would have said three screens does not fit. PSS apportions the shared
   WebKit and font pages the kernel holds one physical copy of, giving ~150-
   170MB marginal. Even the pessimistic RSS-naive bound leaves ~287MB free
   after `vulos-server` (47MB); PSS leaves over 1GB.

   Two measurement bugs were caught before either number was trusted, both of
   which would have produced a confident wrong answer: `/proc/*/comm` truncates
   to 15 characters, so an exact-match filter silently dropped
   `WPENetworkProcess` (~33MB/instance), and `cage --platform=wl -- cog URL`
   is wrong syntax — the flag is cog's, not cage's.

   **~~Recommended, not implemented~~ — both SHIPPED same day.**
   `docs/GETTING-STARTED.md` recommends 4 GB for multi-monitor boxes without
   moving the 2 GB minimum, and `vulos-kiosk` warns — never refuses — when more
   than one output is connected under ~3 GiB, naming the actual RAM and screen
   count so the message is actionable. Unknown memory is silent, not assumed
   low. Both are pinned by tests that execute the real launcher, mutation-tested
   against the three ways the warning could have become a refusal (`exit 0`,
   `exit 1`, and a silent drop to one output) — because a box that quietly
   decides you get one monitor is a worse failure than a slow one.

   `docs/DEPLOY.md` deliberately did NOT get the same sentence: it is the
   Docker/SSH server guide and has no display path at all, so a multi-monitor
   memory note there would describe a configuration it cannot produce.

   **What these numbers do NOT establish**, kept because the temptation is to
   quote the table alone: the OS/kernel baseline was estimated rather than
   measured; only `WLR_RENDERER=pixman` was exercised, never a hardware GL
   path; it ran arm64 inside OrbStack's VM, not bare metal; and it is a ~30s
   snapshot, not a soak test. Tracked as V1-V4 in the cost doc.
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
