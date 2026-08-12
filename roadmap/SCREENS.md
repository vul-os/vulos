# Screens — how a browser-rendered desktop spans more than one display

**Status: design note. Nothing here is built.** It exists because the question
"what happens with two monitors" has an answer that is not obvious for an OS
whose desktop is a web app, and because most of the machinery the answer needs
is already in the tree for other reasons.

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

The syntax above is from the manuals and is believed correct, but it has NOT
been run. A wrong rule fails the way everything in this area fails: silently,
with every browser on one monitor and nothing in any log saying why. Verify by
running labwc with two virtual outputs under QEMU and reading a screendump —
the verification this feature needs regardless.

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
5. **What a headless box does.** Today `vulos-kiosk` exits cleanly when it finds
   no display. Multi-screen must not turn that into an error path.

## Why write this down before building it

The kiosk work that preceded this note took several rebuild cycles to find that
a systemd `Condition` skips silently, that a diagnostic in the journal is
unreadable from a box showing no desktop, and that the test harness had no GPU
at all. Each was a case of the answer existing somewhere nobody was looking.

The same risk applies here in a specific way: (a) and (b) look similar from a
screenshot of one monitor, and the difference only appears on a desk with two.
Choosing deliberately, and recording why, is cheaper than discovering the choice
was made by whichever compositor happened to be launched.
