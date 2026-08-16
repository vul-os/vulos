# Screenshot provenance — what the product's images actually showed

Status: **done** — mechanism, the fifteenth live shot, and the unreferenced-fixture cleanup.

## The question

The founder booted a real box, found the bundled apps failing, and asked:

> "in screenshots these all work, why when I'm trying is it not?"

## What the harness actually was

Two separate pipelines existed, and only one of them was what the product is
presented with.

**`frontend/scripts/screenshots.mjs`** (1567 lines) is the real one. It drives
the genuine production `vite build` bundle and mocks the entire backend at the
browser network layer via `frontend/e2e/mock-backend.js`. It writes 48 PNGs to
`docs/screenshots/`, all committed (13 MB). 20 of them are consumed: 13 by
`README.md` and the rest by `docs/USER-GUIDE.md`, `APPS.md`, `SETTINGS.md`,
`MAIL-CALENDAR-CONTACTS.md`, `ADD-DEVICE.md`, `REMOVE-DEVICE.md`,
`CUSTOM-DOMAIN.md`, `GETTING-STARTED.md`, `FILES.md`, `ASSISTANT.md`,
`TERMINAL.md`. Every consumer references the `-light` variant. There is no
`site/` directory in this repo, and no CI job generates, uploads or link-checks
screenshots.

**`frontend/e2e/_polish-shots.e2e.ts`** — since deleted — was the file whose header read
`NOT part of the suite — never commit this file`. It was committed, and it
matched `playwright.config.ts`'s `**/*.e2e.{js,ts}`, so it ran on every CI E2E
run. Its output went to `/tmp/polish-shots`, which **nothing reads**. It was
cost with no consumer.

So the file with the alarming comment was the harmless one. The images the
product is actually sold with come from `screenshots.mjs` — and they are
fixture-backed too.

### What the mock does that matters

`mock-backend.js` answers `POST /api/apps/launch` with `{ok:true}` and starts
nothing. That single line is the whole gap: the screenshots could not, even in
principle, distinguish a working box from a broken one. v0.2.0 shipped with
`/opt/vulos/apps/` empty and every bundled app answering
`{"error":"app not running"}`, and these images stayed green throughout.

### The finding that reframed the work

**Not one of the 48 committed screenshots showed a process-backed app.** Every
shot — hero, files, settings, calendar, contacts, apphub, dashboard, instances,
terminal, launchpad, stacked, tiled, mobile — is a built-in React surface inside
the shell. The 15 apps under `frontend/apps/` (browser, calculator, camera,
clock, gallery, image-editor, music, notes, phone, screenshot, system-info,
text-editor, video, voice-recorder, weather) had **no screenshot at all**, in
any state.

The founder was not misled by a fake picture of Notes. He was shown a polished
desktop and reasonably inferred the apps in it worked, when nothing had ever
photographed them.

## `ip netns` was not the obstacle

The expectation going in was that real app screenshots would need a Linux
container, because app isolation uses `ip netns`. That is wrong, and worth
recording because it nearly stopped the work.

`ip netns` is how the **box routes to** an app. It is not needed to **run**
one. Every bundled app is a plain Python stdlib HTTP server invoked as
`python3 server.py`. `python3 scripts/check-apps-run.py` starts and serves all
15 **on this macOS host**, in 0.1–0.5s each. No container, no VM.

The Linux-only part is the namespace-plus-gateway path, which keeps its own
coverage (`check-apps-run.py` for start-and-serve; the container run that
verified namespace reclaim). A green live-shot run means "the app runs and
renders", not "the box's gateway routes to it" — stated in the spec header.

## What now exists

| Class | Location | Count | Backed by |
|---|---|---|---|
| FIXTURE | `docs/screenshots/` | 24 | real shipping UI, mocked data |
| LIVE | `docs/screenshots/live-apps/` | 15 | a real process, really serving |

`frontend/e2e/shots-live-apps.e2e.ts` imports nothing from `mock-backend.js`,
installs no `page.route`, spawns each app from its own manifest `command` in a
throwaway `HOME`, and points the browser at the port it really bound.

**Liveness proof.** After each capture the app is killed and the same URL is
re-fetched from inside the page with `cache:'no-store'`. That fetch must fail.
A mocked or cached page would keep answering, so the image cannot come from
anything but a process that was genuinely up. A mutation that skips the kill
fails the spec, which is the proof the check bites. (Numbered mutations below
are a later, separate series — do not read this as one of them.)

`scripts/check-screenshot-provenance.py` (CI job `SHOTS-PROV`) checks the labels
against the code: every live spec must import no mock and keep its proof, the
fixture generator must still actually mock, every process-backed app must have a
live shot, and `PROVENANCE.md` must match the shots on disk.

### What the live shots show

Real empty states, because that is what a freshly-booted box looks like:
Weather reports "Location detection unavailable", Notes is an empty editor,
Browser is a blank viewport. `VULOS_API` is pinned to the dead `127.0.0.1:1`
(matching `check-apps-run.py`), so an app reporting itself offline is telling
the truth rather than being staged.

These are more informative about "will this work when I boot it" than any of the
fixture shots, and they are also less flattering. Both facts are the point.

## `system-info.png` — closed 2026-08-16

The fifteenth app had no live shot: its entire surface IS the machine it runs on,
so a capture on this laptop published `pcs-MacBook-Air.local`, Darwin 24.6.0,
792.8/926.4 GB into a public repository. The `NO_LIVE_SHOT` exemption recording
that was written to expire the moment a shot appeared. It has, and it is gone.

### The container was measured, and rejected

The suggested remedy was "run it in the Linux container". That was tried first
and is **the wrong answer**, which is only visible once you look at the numbers.
Debian trixie under OrbStack, `frontend/apps/system-info/server.py` against a
dead `VULOS_API`:

| Panel | What the container reports | What it actually is |
|---|---|---|
| hostname | `aff034300851` | a Docker container ID |
| kernel | `6.17.8-orbstack-00308-g8f9c941121b1` | the developer's VM kernel |
| uptime | `1d 14h 6m` | the VM's uptime |
| memory | 7995 MB | the VM's allocation |
| storage | 327 GB total / 108 GB used | this laptop's docker store, one layer down |
| disks | `/etc/resolv.conf`, `/etc/hostname`, `/etc/hosts` **listed as disks** | Docker bind-mounts them from `/dev/vdb1`, and the app's mount filter passes anything under `/dev/` |
| network | interfaces with **no addresses** | no `iproute2` in the image |

Four panels wrong in ways a reader cannot distinguish from "System Info is
broken", while still implying "this is your Vulos box". It also exercises the
`/proc` FALLBACK path, which a real box never takes because its backend answers
`/api/system/info`. A smaller lie is still a lie about the one app whose whole
job is to be accurate.

### What was done instead: a real box

`frontend/e2e/shots-live-system-info.e2e.ts` photographs it on a booted Vulos OS
image (`output/vulos-arm64.img`, QEMU + HVF, backend port forwarded), started by
**the box's own launcher** through `POST /api/apps/launch` — which runs it inside
a per-app network namespace, the Linux-only `ip netns` path that
`shots-live-apps.e2e.ts` explicitly does not exercise.

The published image shows `vula`, Debian 13, kernel `6.12.86+deb13-arm64`,
`aarch64`, **QEMU Virtual Machine**, 4 cores, 6.2 GB, `/dev/vda2` ext4 1.7/2.7 GB,
interface `vn_94e897` at `10.200.67.2`. Nothing on it is this laptop, and it does
not pretend to be bare metal — it says it is a virtual machine, on screen.

Two properties are asserted rather than trusted:

- the hostname the box reports must differ from `os.hostname()` on the machine
  running the test, so a capture that quietly fell back to localhost fails
  instead of shipping (proved: pointed at a fake backend serving
  `pcs-MacBook-Air.local`, the spec refuses);
- the app's own `/api/info` must agree with the box backend's
  `/api/system/info` on hostname and kernel — two independent readers of the
  same `/proc`, cross-checked.

### Defects this capture surfaced

Reaching that shot required getting the app to actually run on a box, and four
things stood between. They are recorded because each is a real finding, not
harness friction:

1. **Bundled apps break behind the default gateway.** `system-info/index.html`
   fetches ABSOLUTE `/api/info`, `/api/disks`, `/api/network`, `/api/live`. With
   app origins disabled — the DEFAULT (`GET /api/apps/origins` →
   `{"enabled":false}`), and what `src/core/AppOrigins.ts` falls back to — the
   shell opens an app at `/app/<id>/` on the SHELL's origin, so those fetches
   reach the box's backend instead of the app and every panel renders its empty
   state ("No storage data available", "No active interfaces detected", 0%). The
   gateway itself is fine: `/app/system-info/api/info` returns the app's real
   JSON. **This is in current source, not just an old image.** It is why the
   capture is taken at the app's own origin, and it is filed rather than framed.
2. **`output/vulos-arm64.img` ships `/opt/vulos/apps` EMPTY** — the same
   condition that produced the founder's original "why do the apps not work".
3. That image's launcher **does not default `work_dir` to the app directory**, so
   `python3 server.py` runs from `/` and exits 2. Current source does default it
   (`LoadAndValidateManifest`), so this is fixed-but-shipped.
4. That image's launcher **drops privileges without lowering
   `net.ipv4.ip_unprivileged_port_start` in the namespace**, so a manifest
   `"port": 80` cannot be bound (`PermissionError`), and the advertised
   `host:7070 → app` DNAT is **`OUTPUT`-only for `127.0.0.1`**, so the published
   app port is unreachable from off-box and fails locally too.

Also seen, and worth someone's attention: the box answered
`{"setup_complete":true}` while still accepting a brand-new **first** user via
`POST /api/auth/register`, which returned an admin session and a master recovery
phrase.

### Re-running it

```bash
# 1. boot the box (see scripts/baremetal-smoke.sh for the QEMU line)
#    forward the backend: hostfwd=tcp:127.0.0.1:8099-:8080
# 2. get an admin session cookie on it, then set the energy profile —
#    a `balanced` box SUSPENDS after 15 minutes idle and cannot be woken
#    over QMP, which ate one run:
curl -b cookies -X POST $BOX/api/energy/mode -d '{"mode":"performance"}'
# 3. install the app under the server's datadir (HOME=/ → /.vulos/apps), then
cd frontend && VULOS_BOX_URL=… VULOS_BOX_SESSION=… VULOS_BOX_APP_URL=… \
  npx playwright test e2e/shots-live-system-info.e2e.ts
```

Without the three env vars the spec SKIPS: there is no box, and it has nothing it
could honestly do. What CI still enforces without a box is every label —
`SHOTS-PROV` checks the spec keeps its proofs, and `COVERAGE` fails if the
committed shot disappears.

## The 28 unreferenced fixtures — closed 2026-08-16

The previous note here argued they were "stable rather than churning, and several
document features whose docs are unwritten". Tested per file, that splits in two,
and both halves were acted on. `TestDocImagesResolve` was verified capable of
failing first (deleting `tiled-light.png` fails it, naming both consumers).

**24 deleted.** Twenty-three are the DARK twin of a shot whose `-light` variant is
embedded. `screenshots.mjs` called dark "canonical" in its own header; it never
was — README.md and eleven `docs/` pages embed `-light`, every one, without
exception. The twenty-fourth is `settings-relay.png`/`-light`: captured for the
relay guide, found unusable for it (the generator's own comment says so — "it
frames the top of the panel, where the options are not yet visible"), superseded
by two better framings, and never removed.

**4 kept — and embedded, because a figure with no page is a job half done:**

| Shot | Now embedded in | Why it was real |
|---|---|---|
| `settings-relay-providers-light` | `SETTINGS.md` | the prose already listed exactly those six providers, with no picture |
| `settings-relay-nodes-light` | `REACH.md` § Multiple relays | three tunnels up, a Pier broker beside two Vulos relays — that section's exact claim |
| `stacked-light` | `USER-GUIDE.md` | the floating/overlapping layout, next to the tiled one |
| `mobile-assistant-light` | `ASSISTANT.md` | the sovereignty tier on a phone: "On your device", "Stays on your box" |

`apphub.png` is dark and unembedded but **not** deleted: `AppHub.test.tsx` cites
it as the artifact that caught a duplicated badge. A path named in a comment is a
reference — somebody is expected to be able to open it, and the gate treats it
that way.

The generator no longer re-creates what was deleted: default is light only,
`SHOT_THEMES=dark` opts back in, and an unknown theme name exits 1 rather than
capturing nothing and reporting a clean run.

## The gate, and why it is set where it is

`REFERENCED`: every shipped FIXTURE must be embedded or cited by some tracked
file. An **error**, with a declared-exception escape hatch.

Not a warning, because the failure mode is silent ACCUMULATION — nothing here was
wrong on the day it landed; 24 images arrived one plausible run at a time over
months and were found only because someone went looking. A warning is what a
build that already prints hundreds of lines swallows.

Not the strictest rule either. Error-with-no-escape would force a legitimately
staged figure to be deleted because its page is still being written, which is
worse than keeping it. So an exception costs one line, must carry a reason, and
goes stale in BOTH directions: writing the doc (the file becomes referenced) and
deleting the file both fail the entry. That is `NO_LIVE_SHOT`'s pattern, which
worked — it expired on schedule and forced its own deletion above.

LIVE shots are deliberately exempt. A fixture is a FIGURE; being embedded is its
only purpose. A live shot is EVIDENCE that an app runs, and README.md and
PROVENANCE.md link the directory as a set. Requiring each of the 15 to be
embedded somewhere would push docs to embed images to satisfy a linter — a gate
generating the thing it measures.

### Every check, shown failing

| # | Mutation | Killed by |
|---|---|---|
| M1 | drop `shots-live-system-info.e2e.ts` from `LIVE_SPECS` | `SPEC-COVERAGE` |
| M2 | spec stops comparing the box host to `os.hostname()` | `LIVE-IS-LIVE` |
| M3 | spec stops asking the box to stop the app | `LIVE-IS-LIVE` |
| M4 | re-add the `NO_LIVE_SHOT` entry now that a shot exists | `COVERAGE` |
| M5 | point the spec at a "box" reporting this laptop's hostname | the spec itself |
| M6 | delete `tiled-light.png`, a referenced shot | `TestDocImagesResolve` |
| M7 | a doc drops its `<img>` — the fixture becomes an orphan | `REFERENCED` |
| M8 | declare an exception for a file that IS referenced | `REFERENCED` (stale) |
| M9 | declare an exception for a file that no longer exists | `REFERENCED` (stale) |
| M10 | an exception whose reason is `"keep"` | `REFERENCED` |
| M11 | break all three reference regexes | `REFERENCED` vacuity guard |
| M12 | make the matcher count any mention for every shot | `REFERENCED` canary |
| M13 | `SHOT_THEMES=lite` | `screenshots.mjs` exits 1 |

M12's canary had to be assembled from string pieces: written out in full, it
matched ITSELF, because this script is tracked and the scan reads code comments
as references. The check failing on its own text was the scan proving it does
exactly what it claims.
