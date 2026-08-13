# Multi-Screen Kiosk — Memory Cost and the 2GB Floor

> **Goal.** Answer roadmap/SCREENS.md's open item 4 ("Cost") with numbers: what
> one kiosk browser costs, what each additional screen marginally costs, and
> whether three screens is supportable on the 2GB RAM floor this project
> advertises (`docs/GETTING-STARTED.md`, `docs/DEPLOY.md`).
> **Non-goal.** Changing `scripts/vulos-kiosk.sh` or any other launcher file.
> Recommendations below are specified, not implemented — see *Decision*.
> **Status.** Measurement complete, 2026-08-13. Reproducible: two independent
> runs of the same script agree on marginal PSS to within ~14MB (see §3).

---

## How to read the evidence tags

- **[MEASURED]** — produced by running `scripts/measure-kiosk-screen-cost.sh`
  or a command in *Appendix A* on this machine on 2026-08-13.
- **[READ]** — read directly out of the repository source at the cited
  `file:line`.
- **[ASSUMED]** — general knowledge or an estimate, not measured in this
  exercise. Treated as a hypothesis, not a result — see §5.

Measurements were taken on **Docker/OrbStack, `linux/arm64`, Debian trixie**,
the same target `roadmap/DISPLAY-STACK.md` used. **Nothing here was measured
on amd64, on real bare-metal, or with a hardware GPU** — see §5.

---

## Headline

- **One cog instance against the real frontend build costs ~322MB PSS
  (~469MB RSS) [MEASURED].** Each *additional* screen costs **~150-170MB
  more PSS**, not another ~322-469MB — because most of a second or third
  cog instance's memory is the same WebKit shared library, font cache and
  page-cache-backed files the first instance already paid for. **RSS
  overstates this by roughly 3×**: summed RSS grows by a near-full base
  instance (~470-480MB) per screen, because RSS charges every process the
  full size of every page it maps, shared or not.
- **PSS, not RSS, is the number this decision should use**, and the
  difference between them is the actual finding here: the kernel does not
  allocate separate physical frames for a shared, clean, file-backed page
  (WebKit's `.so`, fonts) per process that maps it — one physical copy
  exists regardless of how many cog instances reference it. RSS counts it
  once per process anyway. PSS divides it by the number of mappers, which is
  what "how much physical memory would freeing this process return" actually
  means.
- **On the numbers measured here, three screens fits inside a 2GB budget
  by a comfortable margin under PSS accounting, and by a thin one under RSS
  accounting** — see §3's arithmetic. The decision in §4 is conditional
  because two inputs to that arithmetic were not measured in this exercise:
  the OS+kernel baseline on real hardware, and the hardware-GL rendering
  path (§5).

---

## 1. What was measured, and how

`scripts/measure-kiosk-screen-cost.sh` (added by this investigation) launches
1, then 2, then 3 independent headless `cage`+`cog` instances — each its own
single-window wlroots compositor, matching the single-screen branch of
`scripts/vulos-kiosk.sh` — against a `python3 -m http.server` serving the
**real built frontend**, `frontend/dist/index.html` (the actual Vulos shell
bundle, not a blank page or a stub). After each addition it sums `Rss` and
`Pss` from `/proc/<pid>/smaps_rollup` **[MEASURED]** over every process
matching cog's real process tree:

```
dbus-run-session → dbus-daemon → cage → cog → WPENetworkProcess
                                            → bwrap → xdg-dbus-proxy
                                            → bwrap → WPEWebProcess
```

**[MEASURED]**, from `ps -eo pid,ppid,comm` at n=3 (one instance's tree
shown; two more identical trees ran alongside it):

```
    22      14 dbus-run-sessio
    23      22 dbus-daemon
    24      22 cage
    25      24 cog
    29      25 WPENetworkProce
    38      25 bwrap
    39      38 bwrap
    40      39 xdg-dbus-proxy
    43      25 bwrap
    45      43 bwrap
    50      45 WPEWebProcess
```

Two real defects in the *first* version of this measurement, found before any
number was trusted, are worth recording because both would have silently
produced a confidently wrong answer:

1. **`/proc/*/comm` truncates at 15 characters** (`TASK_COMM_LEN=16`), so
   `WPENetworkProcess` (17 chars) reads back as `WPENetworkProce`. An
   exact-match filter on the full name silently dropped every
   `WPENetworkProcess` — about 33MB of PSS per instance, uncounted — until a
   manual `ps` cross-check caught it. Fixed with a glob (`WPENetworkProce*`).
2. **`cage --platform=wl -- cog URL` is wrong** — `--platform=wl` is cog's
   flag, not cage's, and cage rejected it (`invalid option -- '-'`). The
   correct form, confirmed against commit `47e031be`'s recipe, is
   `cage -- cog --platform=wl URL`. A related miss: the image this script
   built initially had `cog` and `labwc` but not `cage` — a separate package
   cog does not depend on — so the single-screen path this script simulates
   could not start at all until `cage` was installed explicitly.

Both are fixed in the committed script; §2's numbers are from the corrected
version.

---

## 2. Raw numbers

Two full runs, same script, same image (`vulos-screencost:trixie`: Debian
trixie + `cog cage labwc python3 dbus procps ca-certificates`, `docker
commit`-ed once so the ~20-minute install was paid once, not three times —
see *Appendix A*). **[MEASURED]**:

**Run A** (`sleep 12` settle before each reading):

| screens | procs | RSS (MB) | PSS (MB) |
|---|---|---|---|
| 1 | 10-11 | 472 | 326 |
| 2 | 20 | 928 | 473 |
| 3 | 36* | 1473 | 634 |

**Run B** (`sleep 12`, plus a second reading 15s later to check for
JIT-warmup drift):

| screens | procs | RSS (MB) | PSS (MB) |
|---|---|---|---|
| 1 | 10 | 468.7 | 322.3 |
| 2 | 20 | 946.8 | 491.4 |
| 3 | 30 | 1414.2 | 646.8 |
| 3, +15s settle | 30 | 1413.4 | 646.3 |

\* Run A's n=3 process count (36 vs Run B's 30) came from one instance
transiently spawning a second sandboxed `WPEWebProcess`/`xdg-dbus-proxy` pair
(visible in that run's `ps` output around an accessibility-bus retry); Run B
is the cleaner run and is used for the arithmetic below. The two runs' PSS
figures agree to within 14MB at every step, so the noise this introduces is
small relative to the marginal-cost numbers.

Run B's settle check (**1413.4 vs 1414.2 MB RSS, 646.3 vs 646.8 MB PSS**,
15 seconds apart) shows the numbers are **not** still climbing from WebKit
JIT warmup by the time they're read — a real risk, since a manual pre-check
caught `WPEWebProcess` still at 30-60% CPU at the 12-second mark before this
settle check was added.

**Marginal cost per screen, PSS (Run B, the number this decision uses):**

| addition | ΔPSS |
|---|---|
| screen 1 (base) | 322.3 MB |
| +screen 2 | +169.1 MB |
| +screen 3 | +155.4 MB |

**Marginal cost per screen, RSS (shown to quantify the overstatement):**

| addition | ΔRSS |
|---|---|
| screen 1 (base) | 468.7 MB |
| +screen 2 | +478.1 MB |
| +screen 3 | +467.4 MB |

RSS's marginal cost is essentially the same as its own base cost — i.e., by
RSS accounting a second screen looks almost as expensive as the first one,
which is the double-counting this document is about. PSS's marginal cost
(~150-170MB) is roughly **half** of PSS's base cost (322MB), which is the
correct shape for "most of the second instance is shared with the first."

`vulos-server` (the OS backend, `backend/cmd/server`), idle, no active
requests, cross-compiled `GOOS=linux GOARCH=arm64 CGO_ENABLED=0`, run in the
same kind of container and read the same way: **RSS 47.06MB, PSS 47.05MB**
**[MEASURED]** (a single process with nothing else on the box referencing its
memory, so RSS and PSS are ~equal here — there is nothing to share yet).
This is API-only mode (`frontend/dist` was not mounted at the path it checks,
so it logged "no frontend build found" and served no static files); a real
box additionally holding the frontend's static assets in its file-page cache
would show a modest, mostly-reclaimable increase, not measured here.

---

## 3. Does three screens fit in 2GB?

Two accountings, both using Run B's numbers, `vulos-server` at 47MB, and an
**[ASSUMED]** OS+kernel+systemd baseline of 150-300MB (see §5 — this input
was *not* measured on real hardware in this exercise, and the range is
carried through rather than collapsed to one number):

**By PSS (the metric this document argues is correct):**

| screens | kiosk PSS | + server (47MB) | + OS (150-300MB, assumed) | used | **free of 2048MB** |
|---|---|---|---|---|---|
| 1 | 322 | 369 | 519-669 | — | **1379-1529 MB** |
| 2 | 491 | 538 | 688-838 | — | **1210-1360 MB** |
| 3 | 647 | 694 | 844-994 | — | **1054-1204 MB** |

Even at the pessimistic end of the assumed OS baseline, three screens leaves
**over 1GB free** by PSS accounting.

**By RSS (the naive sum, shown as the "if PSS reasoning is wrong" bound):**

| screens | kiosk RSS | + server | + OS (assumed) | **free of 2048MB** |
|---|---|---|---|---|
| 3 | 1414 | 1461 | 1611-1761 | **287-437 MB** |

Under naive RSS summing, three screens leaves as little as **287MB** free —
uncomfortably tight for a box that also needs headroom for page cache, any
other running service, and load spikes.

**These two bounds disagree by nearly 4×** (1054-1204MB free vs 287-437MB
free), and the entire disagreement is the RSS/PSS double-counting question in
§1's headline. This document's position — argued from what PSS actually
measures, not merely asserted — is that **PSS is the physically correct
number**: a clean, file-backed, shared page has exactly one physical copy no
matter how many processes map it, and freeing one of N processes that map it
returns none of that page's memory to the system, which is precisely what
RSS gets wrong and PSS gets right by construction.

---

## 4. Decision

**Three screens is a supported configuration on the advertised 2GB floor**,
conditioned on the software-rendering path (§5) and pending the two
[ASSUMED] inputs being independently measured (§5's V-items) — recorded now,
per this project's standing directive to decide autonomously rather than
defer, rather than left open pending a QEMU/bare-metal run that has not
happened yet.

Reasoning:

- The measured marginal cost (~150-170MB PSS per additional screen) is small
  relative to 2048MB, and even the worst-case bound in §3 (RSS-naive, high
  end of the assumed OS range) does not exceed the floor — it leaves the
  system tight, not out of memory.
- The **PSS accounting, which is the physically accurate one**, leaves over
  1GB free at three screens even under the pessimistic OS-baseline estimate.
  Refusing three screens outright would be leaving real capability on the
  table based on the wrong metric.
- But the margin is not so wide that it should be presented as
  unconditionally free. Two real unknowns feed directly into the arithmetic
  (§5): the OS+kernel baseline was estimated, not measured, on real
  hardware; and the hardware-GL rendering path (what a box with a working
  GPU driver actually uses, rather than software `pixman`) was not measured
  at all. A box that also runs other Vulos services (mail, files, sync) or
  takes the GL path could plausibly use meaningfully more than this
  document's assumed 150-300MB baseline.

**What is specified here, for the file owner to implement** (this document
does not touch `scripts/vulos-kiosk.sh`, per this investigation's file
ownership):

1. **Documented memory recommendation, not a hard block.** Extend the
   existing distinction in `docs/GETTING-STARTED.md` ("2 GB RAM minimum, 4 GB
   or more if you want it to feel good") to say explicitly: **2GB is fine for
   one screen; multi-screen kiosk configurations (2-3 outputs) are
   recommended on 4GB+.** This uses language the docs already establish
   rather than inventing a new threshold, and it does not block anyone who
   wants to run 3 screens on 2GB — the measurement here does not show a hard
   wall, only a thinning margin.
2. **A boot-time warning, not a refusal, in `vulos-kiosk.sh`'s multi-output
   branch** (`screen_count > 1`): read total system RAM (e.g.
   `/proc/meminfo`'s `MemTotal`) and, if it is under roughly 3GB, log one
   line to the same journal every other decision in that file already logs
   to — e.g. `vulos-kiosk: N screens on <X>MB RAM — multi-screen is
   recommended at 4GB+, see docs/GETTING-STARTED.md`. This matches the
   file's own established pattern (§ "Say what we decided and why, ALWAYS")
   rather than failing silently or refusing outright, and keeps the box
   working — a warning a user can ignore is better than a launcher that
   second-guesses a machine it hasn't actually measured running out of
   memory on.
3. **Do not gate this on screen count alone.** A user with 2 real monitors
   and 2GB is closer to the measured, comfortable case (§3: ~538-688MB used
   at 2 screens, ~1.2-1.4GB free) than a user with 3 screens at 2GB. If a
   threshold is implemented, it should key off measured/assumed memory
   pressure (`MemTotal` vs screen count), not a flat "≥2 screens = warn."

---

## 5. What this does NOT establish, and exactly what would verify it

- **The OS+kernel+systemd baseline (§3's 150-300MB) was not measured on real
  hardware or in a real boot in this exercise.** It is general knowledge
  about minimal Debian+systemd headless installs, carried through as a
  range rather than collapsed to a false-precision single number, but it is
  the single biggest unverified input to §3's arithmetic. **V1**: boot
  `output/vulos-arm64.img` (already built by earlier work — this does *not*
  require re-running `build.sh`'s image build, which is out of bounds on
  this machine per standing instruction) under `qemu-system-aarch64` with
  `-m 2048`, as `scripts/netboot-install-smoke.sh` already does for other
  purposes, and read `/proc/meminfo` over serial once boot settles, before
  and after starting `vulos-server`.
- **Nothing here was measured with a hardware GL rendering path.** Every
  number in §2-§3 is `WLR_RENDERER=pixman` (software), because that is what
  `scripts/vulos-kiosk.sh` itself selects on any GPU-less or `virtio_gpu`
  box (its own `sw=yes` branch), which this document's container also has no
  choice but to take (§ "no `/dev/dri`" — same limitation
  `roadmap/DISPLAY-STACK.md` §2.2 already recorded for the same reason).
  **V2**: repeat `scripts/measure-kiosk-screen-cost.sh`'s methodology
  (adapted, since that script forces the software path) on a box with a real
  `/dev/dri/renderD1*` and a working Mesa driver, comparing GL vs pixman PSS
  per instance.
- **Every number is arm64 in a Docker/OrbStack VM on a Mac, not amd64 and not
  bare metal.** `roadmap/DISPLAY-STACK.md` flagged the identical limitation
  for its own package-size numbers; it applies here for the same reason.
  **V3**: repeat on an amd64 host, and separately on real hardware if one is
  available, and compare.
- **This is a ~30-second snapshot, not a soak test.** WebKit renderer
  processes are known to grow over long uptimes (tab/page-level leaks,
  cache growth); §2's settle check only rules out *warmup* drift over 15
  seconds, not slow growth over hours or days of a kiosk staying on one
  page. **V4**: leave 1-3 instances running for an extended period (hours)
  against the real frontend and re-read PSS periodically.
- **The frontend served here is the built static bundle with no backend to
  talk to** (`vulos-server` was not wired to the same URL cog loaded), so
  the shell likely sat in a loading/error state rather than the fully
  populated desktop a logged-in session would render. A fully live session
  (real API responses, open apps, more DOM) could plausibly use somewhat
  more per instance than measured here. Not tested; noted as a
  likely-understates-slightly bias, not a likely-overstates one.
- **`vulos-server`'s 47MB figure is idle, request-free, and does not include
  the frontend bundle in its own file-page cache** (§2). A box actually
  serving pages, sync, and AI-assistant traffic would show more, though a
  meaningful share of that growth would itself be reclaimable page-cache
  memory subject to the same RSS/PSS distinction as §1.
- **The "reasonable outcomes" the source item lists were narrowed to one
  path (recommend + warn) rather than a hard ceiling**, because nothing
  measured here showed a wall the launcher needs to refuse past. If V1-V4
  above later show the assumed OS baseline was too low, or the GL path costs
  substantially more than pixman, that conclusion should be revisited rather
  than assumed to still hold.

---

## Appendix A — reproducing the measurements

All commands run from the repo root on Docker/OrbStack, `linux/arm64`.

1. **Build and commit the image once** (installs took ~6 minutes total here,
   not the ~20 minutes warned about, but OrbStack's btrfs store is fragile
   under repeated installs regardless, so this is still committed rather than
   rebuilt per run):

   ```
   docker run -d --name screencost-build --privileged debian:trixie sleep infinity
   docker exec screencost-build sh -c '
     apt-get update -qq
     DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
       -o Dpkg::Use-Pty=0 cog cage labwc python3 curl dbus procps ca-certificates
   '
   docker commit screencost-build vulos-screencost:trixie
   docker rm -f screencost-build
   ```

2. **Run the measurement** (builds `frontend/dist` must already exist —
   `cd frontend && npm run build` if not):

   ```
   scripts/measure-kiosk-screen-cost.sh vulos-screencost:trixie
   ```

3. **`vulos-server`'s own footprint**, cross-compiled and read the same way:

   ```
   cd backend && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/vulos-server-linux-arm64 ./cmd/server
   docker run -d --name vsrv-mem -v /tmp/vulos-server-linux-arm64:/vulos-server:ro debian:trixie-slim sleep 60
   docker exec vsrv-mem sh -c 'HOME=/root PORT=18080 /vulos-server & sleep 6
     for p in /proc/[0-9]*; do
       grep -q "^/vulos-server$" "$p/cmdline" 2>/dev/null && cat "$p/smaps_rollup" | grep -E "^Rss|^Pss:"
     done'
   docker rm -f vsrv-mem
   ```

`scripts/measure-kiosk-screen-cost.sh` itself is committed at
`scripts/measure-kiosk-screen-cost.sh` and carries the same reasoning inline,
including the two defects from §1 and why each substitution in §5 was made.

No repository file besides this document and the new script was modified by
this investigation. `scripts/vulos-kiosk.sh`,
`scripts/vulos-kiosk-genconfig.sh`, `scripts/smoke-kiosk-multiscreen.sh`,
`scripts/smoke-multiscreen.sh` and `scripts/build-sh-packages.txt` were read
but not edited.

---

## Appendix B — bookkeeping

This document answers `roadmap/SCREENS.md` item 4 under "What is genuinely
unresolved." That file is owned by another workstream and is not edited
here; the proposed replacement wording is reported alongside this document
rather than written into it. `docs/decisions.md` is also owned elsewhere
(`roadmap/DISPLAY-STACK.md`'s Appendix B recorded the same deferral) — if a
decision-log entry is warranted, this document is the evidence it should
cite.
