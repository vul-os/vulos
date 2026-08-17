# Architecture across synced instances (ARCH-SYNC)

> **Status, 2026-08-15.** Design + measurement. **Nothing in this document is built
> yet** beyond what §2 records as already existing (ARCH-01, which another agent
> shipped in `backend/services/appnet/arch.go`). Every number here was measured on
> this machine or read out of this repository's code; where something is a design
> proposal it says so in the heading.

## 1. The requirement

Founder, verbatim:

> *"since vulos syncs between multiple os, we need emulation for apps that don't run
> on arm — since it's one os on multiple instances that sync, we need lightweight
> robust emulation."*
>
> *"no I'm saying for the actual instances streaming, some might be arm, some might be
> x86 but we must be able to sync between all instances to make it like one os"*
>
> **"EVERYTHING MUST SYNC, EACH INSTANCE IS ALMOST A DIRECT CLONE OF NEXT WITH FEW
> EXCEPTIONS"**

The bar this sets: **an app set that forks by architecture is a defect.** "GIMP is on
your desktop but not on your ARM laptop" breaks the one-OS promise. Divergence is
permitted only as a **named, justified, user-visible exception** — never as an
implementation detail and never silently.

This document answers: *what does the OS do when a synced app cannot run on the
architecture of the instance receiving it?*

---

## 2. What actually exists today

This project's recorded failure mode is designs that read as built. So, plainly, per
mechanism:

| Mechanism | Reality |
|---|---|
| **Per-box arch capability (ARCH-01)** | 🟢 **Built and enforced.** `backend/services/appnet/arch.go` — `NormalizeArch` (single alias table across Debian/Flatpak/GOARCH spellings), `BoxArch()`, `SupportedArches()` (native + `flatpak --supported-arches`, 1-min cache), `ArchSupported`, `ArchUnavailableReason`. Enforced on the **install path** at `registry.go:964`, not only in the listing. |
| **App Hub arch labelling** | 🟡 **Built, but the UI re-derives the answer instead of reading the server's.** See §7. |
| **Cross-node app placement / "run it on the x86 box and stream it here"** | 🔴 **Does not exist.** `stream.LaunchOpts` (`backend/services/stream/pool.go:154`) has no node/host/endpoint field; GStreamer sinks to `udpsink host=127.0.0.1`; input is injected into the local `/dev/uinput`. The one host-taking entry point, `POST /api/stream/vnc`, is admin-only and `isBlockedVNCHost` **rejects RFC1918/loopback/CGNAT** — it can reach a public VNC server and specifically *not* another Vulos box on your LAN. No peering or cluster code references `stream.Pool`. |
| **`cluster` service scheduling** | 🔴 **No scheduler.** `backend/services/cluster/cluster.go` is S3 announce (`nodes/{id}/meta.json`) + peer discovery + staleness + leases. `NodeMeta` is `{ID, Mode, Hostname, LastSeen, Storage}` — **no arch, no GPU, no capability of any kind**. `ReconcileApps()` exists only as prose in `CLUSTER.md:438`; the `reconcile.go` that would have held it was deleted 2026-07-20. |
| **`appnet` "cluster-wide app scheduling policy"** | 🟡 **A validated string, plus a mutual-exclusion lease.** `manifest.go`'s `Concurrency` field is real and `launcher.go` really acquires a run-lease for singleton apps. But a run-lease is **exclusion, not placement**: it answers "may I start this here", never "which node should run this". A node that loses the race gets `ErrNotHolder` and the user gets an error — nothing redirects them to the holder. |
| **`needs_gpu` as a capability precedent** | 🔴 **Gates nothing.** `registry.json`'s `lane.needs_gpu` is not even parsed into `RegistryEntry` — it survives only in the `Extra` passthrough map that keeps the Ed25519 signature valid. The live copy is a hand-maintained Go table (`routes_router.go:29`). Its sole effect is which of two launch endpoints the shell calls. An app tagged `needs_gpu` launches identically on a software-only box. **ARCH-01, not GPU, is this codebase's one working capability gate** — so arch is the precedent to copy from, not to copy toward. |
| **The installed app set as *synced* state** | 🔴 **Not synced as desired state, and not written at all.** See §3. |

### 2.1 The finding that decides the design

`app_registry` (`backend/internal/multiinstance/migrations/0001_initial.sql:61`) is keyed:

```sql
PRIMARY KEY (instance_ulid, app_id)
```

That is **a per-instance observation table, not a shared app set.** Installing GIMP on
instance A writes `(A, gimp, installed=1)`. Instance B merges that row and now *knows
A has GIMP* — B has no row of its own and installs nothing. The CRDT (LWW + OR-set +
signed uninstall quorum) is real, careful work, and it converges — but what it
converges on is *"who has what"*, not *"what this OS has"*.

Worse: **`LocalInstall` / `LocalUninstall` have no production caller.** Repo-wide, the
only non-test references are inside `appsync.go` itself and a *comment* in
`main.go:4624`. The real install path (`InstallFromRegistry` → `FlatpakInstall`) never
writes a row. So the table is empty in practice.

The parallel sync audit reached the same conclusion from the other end and added the
piece I had not found — **two engines that each defer to the other:**

- "Installed" is not a record at all, it is a **filesystem fact**: `appnet/store.go:521`
  is a directory scan, and `Install()` is a `MkdirAll` + extract that writes no row
  anywhere.
- A full CRDT replicator for `app_registry` exists and **is wired** — but nothing ever
  writes the table (`LocalInstall`/`LocalUninstall`, zero non-test callers).
- The general CRDT engine **explicitly refuses** `app_registry`
  (`crdtsync/policy.go:135`) on the grounds that appsync already handles it.

Each engine defers to the other and neither moves anything.

> ### The consequence, stated plainly
>
> **The installed app set does not sync today, by any mechanism. Therefore the
> arch-mismatch case cannot arise yet.** Nothing ever asks an arm64 instance to
> install an app because an amd64 instance has it. This document describes a problem
> that is *coming*, not one a user is hitting.
>
> That reorders everything below: **emulation is not on the critical path, and neither
> is arch.** The first fix is a synced app set, which is not an architecture problem —
> installing an app on one instance fails to install it on another *even when both are
> x86_64*.

### 2.2 The right shape: a fleet desired set + a per-instance realised set

The audit proposes, and **I agree and adopt**, that `app_registry`'s
`(instance_ulid, app_id)` key is the wrong shape — it is a *per-instance inventory*,
which can only ever answer "who has what", never "what this OS has". The fix is two
tables rather than one:

| | Key | Meaning | Who writes |
|---|---|---|---|
| **Desired set** | `(app_id)` | "this OS has GIMP" — fleet-level intent | any instance; CRDT-merged |
| **Realised set** | `(instance_ulid, app_id)` | "instance A actually has GIMP, or failed to, and why" | each instance about itself |

Today's `app_registry` is exactly the realised set, so this is additive rather than a
rewrite: keep it, stop treating it as the source of truth, and add the desired set above
it.

**Why this is the right home for the arch answer, and not just a tidier schema:**
architecture stops being a special case. An instance reconciling desired → realised
either succeeds, or records a **realisation failure with a reason**. Arch mismatch is
then one reason among several that already exist and are not architecture at all — disk
full, network down, upstream 404, package withdrawn, install script failed. All of them
need the same thing: a per-instance status with a cause the UI can render. `CLUSTER.md`
already anticipated precisely this (`local_app_status` with `state` + `error`, and
"**Arch mismatch** … mark `skipped` with reason", line 502) — it was simply never built.

So the recommendation in §5 is not "build arch placement". It is **build the desired/
realised split, with a reason field**, and arch is one value that field takes. That is a
strictly smaller and more general piece of work than anything arch-specific, and it is
the piece that makes the App Hub labels in §7 renderable at all.

**One caution on adopting it.** The desired set must carry a **removal** semantic as
careful as the existing one. `appsync` already has a signed distinct-origin uninstall
quorum precisely because a naive uninstall row propagates as fleet-wide deletion. A
fleet-level desired set makes that blast radius larger, not smaller: one bad "remove"
row uninstalls an app from every instance the user owns. The quorum machinery already
written for the realised set is the thing to reuse, not to leave behind.

---

## 3. How big is the problem, measured

Measured with `scripts/arch-catalogue-audit.py`, which reads every Flatpak id out of
`roadmap/APP-CATALOG.md` and asks Flathub (`/api/v2/summary/<id>`) which architectures
each app is **actually published** for. Not assumed, not inferred from reputation.

**Flatpak app ids are architecture-independent** — `org.gimp.GIMP` resolves to whatever
build the local machine needs. So any app published for both arches costs nothing:
sync the id, each instance pulls its own build, and the app set stays identical with
**no emulation whatever**.

| Set | Count | Share |
|---|---|---|
| Catalogue apps queried | 119 | |
| Published for **both** aarch64 + x86_64 | **96** | 80.7% |
| **x86_64-only** | 23 | 19.3% |
| x86_64-only **and proprietary** (already out under policy 1a) | 6 | 5.0% |
| **x86_64-only and NOT proprietary — the real problem set** | **17** | **14.3%** |

**The problem set (17):**

`com.github.Matoking.protontricks` · `com.github.marktext.marktext` ·
`com.github.micahflee.torbrowser-launcher` · `com.heroicgameslauncher.hgl` ·
`com.obsproject.Studio` · `com.ultimaker.cura` · `com.usebottles.bottles` ·
`fr.handbrake.ghb` · `net.lutris.Lutris` · `net.pcsx2.PCSX2` · `org.blender.Blender` ·
`org.mozilla.Thunderbird` · `org.openscad.OpenSCAD` · `org.signal.Signal` ·
`org.upscayl.Upscayl` · `org.vinegarhq.Sober` · `rest.insomnia.Insomnia`

### 3.1 The "x86-only ≈ proprietary" hypothesis is measurably wrong

This was the expected result — policy 1a's own text says *"the x86_64-only set is very
largely the proprietary set, so most of what could not follow a user onto an arm
instance is now out of scope anyway"*, and I was asked twice to requantify on the
expectation that the answer would come out near zero. **It does not. I measured it
rather than assumed it, and the expectation is wrong:**

- Of the **23** x86_64-only apps, only **6** are proprietary. Excluding proprietary
  removes **about a quarter** of the problem, not "most" of it. **17 remain.**
- Of the **10** proprietary apps, **4 are dual-arch** — Chrome, Vivaldi, Obsidian and
  VS Code all ship aarch64. Excluding them cost architecture coverage nothing at all.

The two sets overlap far less than they look like they should. Policy 1a is a good call
for other reasons; it is not an architecture fix.

So policy 1a does **not** dissolve the emulation question. **14.3% of the catalogue
still cannot follow a user onto an ARM instance**, and it includes Blender, OBS Studio,
Thunderbird, Signal, Lutris, Bottles and HandBrake — not a fringe. Blender, OBS, PCSX2
and Cura are also the worst possible candidates for emulation (see §4.4).

Anyone wishing to re-test this should run `scripts/arch-catalogue-audit.py`, which prints
both the pre- and post-exclusion numbers from live Flathub data and names every app in
each set.

**This does not make emulation urgent** — §2.1 does that, by establishing that the app
set does not sync at all yet. The two findings compose to: *the problem set is real and
larger than assumed, and it is also not reachable yet.* Both halves matter. The first
means emulation cannot be written off; the second means it is not what to build next.

### 3.2 Two catalogue defects found while measuring

Both would fail at install with an opaque Flathub error. For the catalogue owner:

| In `APP-CATALOG.md` | Correct id | Arches |
|---|---|---|
| `org.krita.krita` | **`org.kde.krita`** | both |
| `io.lmms.lmms` | **`io.lmms.LMMS`** (capitals) | both |

Both are dual-arch once corrected, which is why the totals above use 119 resolvable
apps rather than 117.

---

## 4. Emulation, evaluated by measurement

### 4.1 What is actually packaged for Debian trixie arm64

Measured in a `debian:trixie-slim` arm64 container (`apt-cache policy`):

| Candidate | Debian trixie arm64 | Verdict |
|---|---|---|
| **qemu-user / qemu-user-static / qemu-user-binfmt** | `1:10.0.11+ds-0+deb13u1` | ✅ packaged |
| **box64** | `0.3.4+dfsg-1` | ✅ packaged |
| **binfmt-support** | `2.2.2-7+b1` | ✅ packaged |
| **FEX-Emu** | *no candidate* | ❌ **not in Debian.** Would have to be built from source or taken from an Ubuntu PPA, i.e. an unpackaged third-party binary in the boot path of a security-positioned OS. |
| box86 | *no candidate* | ❌ irrelevant anyway (32-bit x86 on 32-bit ARM) |

FEX being unpackaged is decisive for a "lightweight robust" requirement: adopting it
means owning its build and its security updates for two architectures forever.

### 4.2 The measurement trap this nearly fell into

The first version of the benchmark compared `docker run --platform linux/arm64` against
`--platform linux/amd64` on this Mac. **The emulated case came out faster than native.**

Cause: on Apple Silicon, Docker/OrbStack services x86_64 through **Rosetta 2**, not
qemu-user — verified directly (`grep -c rosetta /proc/self/maps` inside an amd64
container returns 3). Rosetta is an Apple-only AOT translator at a large fraction of
native speed. **It does not exist on any real arm64 Vulos box** — a Pi, an Ampere
server, an arm64 VM. Reporting that number as "emulation cost" would have been a
confident wrong answer of exactly the kind this project has been burned by before.

The committed benchmark therefore runs everything in **one arm64 container** and invokes
`qemu-x86_64` and `box64` **by name**, so binfmt — and thus Rosetta — is never consulted.
Cases are interleaved and medians reported, because host load drifts wall times ~80%
run to run.

### 4.3 The measured numbers

Measured with `scripts/arch-emulation-bench.sh`, in a `debian:trixie-slim` **arm64**
container on this machine. Workload: `busybox gzip -9` over a fixed 54 MB deterministic
payload, with `busybox-static` taken from Debian for **both** arm64 and amd64 — same
upstream source, same Debian version, statically linked, so the only variable is the
instruction set and who executes it.

**The x86_64 binary really executed.** That was the brief's minimum bar and it is met:

```
--- PROOF the x86_64 binary actually executes under each emulator ---
qemu-x86_64: OK-x86_64-ran-under-qemu
box64      : Illegal instruction
```

| | qemu-x86_64 10.0.11 | box64 0.3.4 |
|---|---|---|
| Ran the x86_64 binary at all | ✅ yes | ❌ **no — SIGILL** |
| Throughput, median of 5 interleaved reps | **8684 ms vs 2287 ms native = 3.80× slower** | *(see below — invalid)* |
| Per-process start-up (20 execs) | 308 ms vs 27 ms native = **11.4× slower per exec** | 459 ms = 17× |
| Peak RSS, one gzip pass | **12.0 MB vs 1.6 MB native (+10.4 MB, 7.5×)** | *(crashed, exit 128)* |

**On box64's number specifically — and why it is not reported as 1.57×.** The timing
loop did produce a plausible-looking 3588 ms median for box64 (1.57× native), and
reporting it would have been the easy thing to do. It is not real: the same binary
raised `Illegal instruction` in the execution proof, and the memory pass exited with
status 128. **A timing harness that does not check exit status will happily time a
crash.** The figure is discarded.

The cause is structural rather than a bug, and it matters for the recommendation:
**box64 gets its speed by *not* emulating libraries** — it intercepts calls and
substitutes native aarch64 ones. That requires the binary to be **dynamically linked**.
The static binary that made this a clean control is precisely the shape box64 cannot
handle. A fair test on a dynamically linked x86_64 binary is therefore reported
separately (§4.3.1) rather than letting an unfair control stand as box64's verdict.

**Caveat stated rather than buried:** the host was at load average ~210 (many
concurrent agents) throughout. Absolute milliseconds are inflated. This is why cases are
interleaved and medians reported — the *ratios* are the durable figures, and the 3.80×
is consistent across all five reps (3.1×, 3.8×, 4.2×, 3.8×, 3.1×).

**What 3.80× means for the actual product.** It is a *scalar, SIMD-light* figure — the
friendly case, deliberately chosen so aarch64's crypto extensions could not distort it.
Real desktop apps are heavier on exactly what qemu handles worst. And the **11.4×
per-exec** cost is arguably the more important number: a desktop app is not one process,
it is a process that spawns dozens of short-lived helpers, and that overhead is felt as
pervasive sluggishness that a throughput ratio hides entirely.

### 4.3.1 box64 on a dynamically linked binary — the fair test

> **Measured 2026-08-17. This section reverses §5's recommendation.** Full
> working, the harness, and the GL result are in
> **`roadmap/DISTRO-SOURCED-APPS.md` §5**; reproduce with
> `scripts/arch-emulation-bench.sh dynamic`.

**box64 runs a dynamically linked x86_64 binary correctly, and faster than
qemu-user.** The sysroot was assembled with `dpkg-deb -x` from Debian's own
`amd64` packages; both emulators got the same tree; every case read stdin and
wrote stdout so no case opened a path; a rep was discarded unless it exited 0
**and** emitted byte-identical output to the native run.

```
native reference: exit=0 bytes=2061295 md5=0089e3b1bbf60c781d6a27b0c99f865a
box64             exit=0 bytes=2061295 md5=0089e3b1bbf60c781d6a27b0c99f865a
qemu              exit=0 bytes=2061295 md5=0089e3b1bbf60c781d6a27b0c99f865a
```

| median of 7 interleaved reps | native | box64 0.3.4 | qemu-x86_64 10.0.11 |
|---|---:|---:|---:|
| Throughput | 4941 ms | **6923 ms = 1.40×** | 11467 ms = 2.32× |
| Per-exec, 100 execs | 44 ms | 1128 ms = 25.6× | 2697 ms = 61.3× |
| Peak RSS | 1816 KB | 27156 KB = 15.0× | 12240 KB = 6.7× |
| Exited 0 on 100 execs | 100/100 | **98/100** | 100/100 |

**Why §4.3's result was not box64's verdict.** The `bench` control is
`busybox-static`. §4.3 itself explains, in the paragraph immediately after the
number it discarded, that box64's speed comes from *substituting native aarch64
libraries rather than emulating them*, which **requires dynamic linking**. A
static binary is therefore the one shape box64 structurally cannot serve. The
measurement exercised box64's known non-case, and the conclusion — "box64 failed"
— was generalised from it. That is the same bad-measurement pattern §4.3 was
written to guard against, one level up: the harness was fixed, the *control* was
not.

**GL, which matters more than the ratio for this catalogue.** An x86_64
`glxinfo` under box64 reported the **host's own aarch64 Mesa 25.0.7 / LLVM
19.1.7** — the identical `Device:` string, version and `direct rendering: Yes` as
the native aarch64 control. Under qemu-user the same binary printed
`Error: couldn't find RGB GLX visual or fbconfig`. box64's native-library
substitution reaches the GL stack. **This does not prove hardware acceleration**
— a container has no GPU, so both lines report `llvmpipe` and `Accelerated: no`;
what is proved is *which stack was bound*.

**Two things this does not overturn.** (1) box64 crashed twice in 100 identical
execs with `Illegal instruction` — §4.5's "lightweight and not robust" is now
measured, not asserted. (2) §4.4's Flatpak conclusion stands on its own grounds:
box64's advantage needs the host's libraries reachable, and bwrap supplies the
runtime's x86_64 `/usr` instead.

**Consequence for §5 and §6.** The fallback recommendation should be **box64 for
apps delivered as ELF files into a prefix we control** (a per-arch `artifacts`
recipe, or the frozen-closure vehicle in `DISTRO-SOURCED-APPS.md`), with
qemu-user as the robust fallback when box64 crashes on a given app. Exception
**E3** — GPU-bound apps unavailable even with emulation — is correct as a
property of **Flatpak delivery** and over-broad as a property of the app.

### 4.4 The three questions that matter more than the ratio

**Does it work with *Flatpak*?** This is the question, because the whole catalogue is
Flatpak, and it is where "lightweight robust" breaks down:

> **Measured 2026-08-17 in a `debian:trixie-slim` arm64 container.** Full output
> in `roadmap/DISTRO-SOURCED-APPS.md` §6.

**A foreign-architecture Flatpak installs. The "it cannot" claim was wrong on the
install half.** `flatpak --supported-arches` on an aarch64 box does not list
`x86_64`, and that is what `arch.go`'s comment reports — but `--arch=x86_64` is an
**explicit flag** and it is honoured:

```
$ flatpak remote-info --arch=x86_64 flathub org.blender.Blender
       Ref: app/org.blender.Blender/x86_64/stable
  Download: 477.4 MB      Installed: 1.1 GB
   Runtime: org.freedesktop.Platform/x86_64/25.08
    Commit: f97247d9e87dca0bc28c6a01e51cd6425cf8b21c636d28514898b7315b21d521

$ flatpak remote-info --arch=aarch64 flathub org.blender.Blender
error: Error searching remote flathub: Can't find ref org.blender.Blender/aarch64

$ flatpak install -y --arch=x86_64 flathub org.gnome.Calculator
Installing runtime/org.gnome.Platform/x86_64/50
Installing app/org.gnome.Calculator/x86_64/stable
$ flatpak list --columns=application,arch,branch,installation
org.gnome.Calculator                x86_64  stable  system
org.gnome.Platform                  x86_64  50      system
org.freedesktop.Platform.GL.default x86_64  25.08   system
```

An x86_64 application and its whole x86_64 GNOME platform runtime — 1.4 GB —
deployed onto an aarch64 installation.

**Whether it RUNS is NOT established, and probably cannot be established here.**
`flatpak run --arch=x86_64` failed with
`bwrap: Creating new namespace failed: Operation not permitted`, which is a
container-privilege limit and would hit a *native* aarch64 app identically.
Three independent blockers: bwrap cannot create user namespaces under Docker's
default seccomp; `/proc/sys/fs/binfmt_misc` is not visible in these containers;
and on this Apple-Silicon host a binfmt handler would be serviced by **Rosetta
2**, which does not exist on any real arm64 Vulos box (§4.2) — so even a success
would have been a confidently wrong number. **Recorded as
`untestable-on-arm64-mac`; needs a real arm64 Linux box.**

**What this changes.** `EmulationCanServe` conflates *can the bits get here* with
*would the result be worth offering*. The first is now measured as **yes** for
Flatpak; the second is still **no**, because §4.3.1 shows box64's advantage
depends on reaching the host's native libraries and bwrap deliberately supplies
the runtime's x86_64 `/usr` instead. The two answers deserve two functions and
two different sentences to the user — see `DISTRO-SOURCED-APPS.md` §9.1.

**Also worth recording as a method note:** every failing command in this
experiment exited **0**. `flatpak remote-info` on a missing ref, `flatpak
install` after bwrap failed, and `glxinfo` with no GL visual all returned 0 while
printing an error. Checking exit status is necessary and **not sufficient** —
the output has to be read too.

**What happens to GPU acceleration?** This is where emulating a *GUI* app diverges
sharply from emulating a CLI tool, and the honest answer is split:

- Vulos gets a structural break here that most setups do not. The app renders into a
  headless compositor (Xvfb/cage) and a **separate native `gst-launch-1.0` process**
  captures and encodes with VA-API/NVENC. So **the encode path stays native aarch64 no
  matter what the app is** — emulation never touches it. That is genuinely good news
  and it is a consequence of the streaming architecture, not of any emulation work.
- The app's own rendering is the problem. An x86_64 Flatpak brings an **x86_64 Mesa**
  inside `org.freedesktop.Platform/x86_64`. Under qemu-user that entire driver stack is
  emulated instruction by instruction, so hardware acceleration is effectively lost and
  GL falls back to emulated software rasterisation — the worst of both costs.
- box64's answer is to **wrap the host's native aarch64 GL/Vulkan libraries** so GL
  calls escape emulation. That is why box64 is fast on games. But it requires box64 and
  the host's native libraries to be **inside the sandbox**, and a Flatpak's bwrap mount
  namespace deliberately supplies the runtime's x86_64 `/usr` instead. box64's strength
  is precisely the thing Flatpak's isolation model prevents.
- Net: **Blender, OBS Studio, PCSX2 and Cura — GPU-bound apps — are the least
  emulatable entries in the problem set**, and they are 4 of the 17.

**What does it cost in memory?** Reported with the numbers in §4.3.

### 4.5 "Lightweight robust" is a genuine tension, stated rather than resolved

- **qemu-user is robust and not lightweight.** It runs anything; it is packaged; it is
  slow, and it is slowest exactly on the vectorised, GPU-adjacent code that desktop apps
  are full of.
- **box64 is lightweight and not robust.** It is fast because it wraps native libraries
  and does not emulate them — and *that is a partial-coverage strategy by construction*.
  Its coverage is best on games and known binaries and worst on the long tail.
- **FEX is the closest to both and is not packaged for Debian**, so choosing it converts
  an emulation problem into a supply-chain problem.

There is no option that is all three of fast, complete and packaged. Any claim to the
contrary is wishful.

---

## 5. Recommendation

**Primary — and it is not an architecture change: build the desired/realised split from
§2.2, with a reason field.** This satisfies "everything must sync" for **96 of 119 apps
(81%) at zero emulation cost**, because a Flatpak id is already architecture-independent
— each instance resolves its own build from the same id. It also fixes the larger bug
that installing an app on one instance does not install it on another at all, on any
architecture. Arch mismatch then arrives for free as one value of the reason field,
alongside disk-full and upstream-404, which need the same plumbing regardless.

**Sequencing, explicitly:** there is no useful arch work before this lands, because
until an instance is asked to install something it cannot install, there is nothing for
an arch check to refuse. The one exception already shipped — ARCH-01's install gate —
was worth doing on its own because a *user* can still click Install on an entry their
box cannot handle.

**Fallback for the 17 — box64 where the app is ELF-in-a-prefix, qemu-user as the
robust fallback. REVISED 2026-08-17 on measurement (§4.3.1).**
This paragraph previously recommended qemu-user *because* §4.3 measured box64
failing. That measurement used a **static** binary, which §4.3 itself explains is
the one shape box64 cannot serve. Re-measured on a dynamically linked binary:

- **box64** runs it correctly (byte-identical output) at **1.40× native**, and
  binds the **host's own aarch64 GL stack** — the property that makes a 3D app
  worth offering at all. It crashed **2 times in 100** identical execs.
- **qemu-user** runs it correctly at **2.32× native**, 61× per-exec, and did not
  fail once in 100 — but cannot obtain a GL visual at all.

So: **box64 first for anything delivered as ELF files into a prefix we control**
(per-arch `artifacts`, or the frozen-closure vehicle in
`roadmap/DISTRO-SOURCED-APPS.md`), **qemu-user as the fallback** for apps box64
crashes on. Neither helps a Flatpak, for the reason in §4.4 — and note that the
prior recommendation is not merely refined here, it is **reversed**.

**And note what is being given up.** `DISTRO-SOURCED-APPS.md` measures that **6
of these 17** — Blender, OBS Studio, HandBrake, Thunderbird, OpenSCAD, Cura —
have a **native Debian arm64 build**, and that Blender installs from one and
renders images. Consuming those builds needs a pinned-closure install vehicle,
which was **parked by founder decision on 2026-08-17** ("one app does not justify
a third vehicle"). So these six stay on the emulation path or unavailable, **by
choice rather than because no build exists** — a distinction worth keeping,
because the second invites nobody to look again.

Either way this is an **opt-in, per-app, non-GPU** path (exceptions E2/E3), never a
default posture.

**Not recommended:** system-wide `binfmt_misc` + qemu-user as the default answer.
It is the tempting move because it is one `apt install` and it makes x86_64 binaries
"just run". It would make the 17 apps *appear* to work while delivering a
software-rendered, multiple-times-slower GUI over a video stream — which is a worse
user experience than an honest "not available here", and it is the kind of thing that
gets recorded as shipped.

**Explicitly rejected: cross-node placement** ("run it on the x86 box, stream it to the
ARM one"). It was my brief's first candidate and the founder's correction is right on
both counts. It fails the requirement — it makes the app set a function of connectivity,
so a box going offline changes which apps exist — and §2 shows it is not remotely
buildable today: `stream` has no node parameter anywhere in its type system, and the
one host-taking endpoint actively refuses private addresses.

---

## 6. The exception list, per "everything must sync"

The directive requires that anything not syncing be **named and justified**. For the app
set, the exceptions are:

| # | Exception | Justification | User-visible? |
|---|---|---|---|
| **E1** | An app in the **17** is installed on an x86_64 instance; an arm64 instance cannot install it natively. | Upstream publishes no aarch64 build. Vulos cannot manufacture one. | **Yes — must be.** §7. |
| **E2** | Emulated availability is **opt-in per app**, not automatic. | Silently emulating Blender produces an app that is present and unusable, which reads as a Vulos defect rather than a hardware limit. An off-by-default switch the user turns on knowing the cost keeps the promise honest. | Yes — the toggle and its warning. |
| **E3** | GPU-bound entries (Blender, OBS Studio, PCSX2, Cura) stay unavailable on arm64 **even with emulation enabled**. | §4.4: an x86_64 Mesa under emulation loses acceleration, and box64's native-GL wrapping cannot reach inside Flatpak's sandbox. Offering these would be offering a defect. | Yes — distinct label. |
| **E4** | App **binaries** never sync; only the app **identity** does. | Pre-existing and correct (`CLUSTER.md:490`). Each instance installs its own arch's build — this is *why* 96/119 need no emulation. | No — invisible by design. |

E1–E3 are exceptions to *uniform availability*. **None of them is an exception to
uniform visibility**: the app must appear in the App Hub on every instance, in the same
place, with the same name. What differs is a state badge. That distinction is what keeps
"one OS" true — the user sees the same OS everywhere and is told the truth about one
piece of hardware, rather than watching an app vanish.

---

## 7. What the App Hub shows — exact copy

The failure mode to design against is the silent one. An app that disappears on the ARM
laptop produces *"why is Blender gone?"*; an app that is present and labelled teaches the
user something true about their hardware. `APP-CATALOG.md` already commits to this
("Unavailable apps are **shown with a reason rather than hidden**") — these are the
strings for the synced-instance case, which that policy does not yet cover.

Four states, and the third is the one that does not exist today:

| State | Badge | Detail-pane sentence |
|---|---|---|
| Installable natively | *(Install button)* | — |
| **Runs on another of your instances** | **`On your other instance`** | **`{App} is installed on {instance name}. This box is {arm64} and {App} ships for {amd64} only, so it cannot be installed here.`** |
| **Emulation available (opt-in)** | **`Needs emulation`** | **`{App} ships for {amd64} only. This box is {arm64} and can run it through emulation — noticeably slower, and without graphics acceleration. Turn on emulated apps in Settings to install it.`** |
| **Not available, no path** | **`Not available on this box`** | **`{App} ships for {amd64} only and needs graphics acceleration, which emulation cannot provide. It stays available on your {amd64} instances.`** |

Copy rules:
- **Never** the bare word "Unavailable" for a synced app — it reads as broken rather
  than as a hardware fact.
- Always name the **other instance** where the app does work when one exists. That single
  clause is what makes a heterogeneous fleet feel like one OS rather than a fleet.
- Always use the **Debian spelling** the API speaks (`amd64` / `arm64`) and never mix in
  `x86_64` / `aarch64`, or the same box appears to have two architectures.

**Existing UI defects this depends on** (App Hub owner, not me):
1. `AppHub.tsx:346` re-implements the arch comparison client-side with a raw
   `app.arch.includes(systemArch)`, ignoring the server's `installable` /
   `installable_reason` / `box_arch` fields **and** the `normalizeArch` helper it
   imports at line 6 but never calls. An entry spelled `x86_64` will not match a box
   reporting `amd64` — the exact failure `arch.go` was written to prevent.
2. `fetchBoxArch()` calls `GET /api/system/arch`, **which does not exist**. It always
   falls through to `GET /api/packages/cache`, which returns raw `runtime.GOARCH` rather
   than `appnet.BoxArch()` — so `VULOS_BOX_ARCH` cannot be used to test the UI.
3. **Not one Flatpak entry in `registry.json` declares `arch`.** All 13 have
   `"arch": null` → "any" → shown installable on arm64. The 9 entries that do declare
   `["amd64"]` are all apt entries, a disjoint set. **The enforcement in §2 is correct
   and currently guards nothing**, because the data it reads is empty. §3's table is the
   measurement needed to populate it.

---

## 8. Exact changes needed in `appnet` (owned by another agent)

`appnet` is not mine to edit. Precisely what is needed, smallest first:

**8.1 — Populate `RegistryEntry.Arch` for Flatpak entries.** Ship
`scripts/arch-catalogue-audit.py --json` output into the registry so the 17 declare
`["amd64"]` and the 96 declare nothing (= any). Without this, `arch.go` is dead code.
*This is the highest-value item in this document and it is a data change, not code.*

**Operational note that makes it more than a data edit:** `Arch` is a modelled field, so
it is inside the Ed25519 signature (`signablePayload` signs the whole entry minus
`signature`). **Changing `arch` invalidates every touched entry's signature and each
must be re-signed** with the registry key, or `VerifyEntrySignature` rejects it and the
app becomes uninstallable — turning a correctness fix into an outage. Whoever does 8.1
needs the signing key in the same change.

**8.2 — `RegistryEntry.Arch` must stay a modelled field, never `Extra`.** It already is,
and that is what makes the install gate trustworthy. Note the contrast with `lane` /
`needs_gpu`, which live in the `Extra` passthrough and are duplicated by hand into a Go
table — an arrangement that is survivable only because `needs_gpu` gates nothing. Do not
let `arch` drift into that shape: an arch claim outside the signature is a downgrade
vector, since flipping an entry to "any" opens the install gate.

**8.3 — Add a capability field to the manifest, mirroring `Concurrency`'s shape.**
Proposed, name negotiable:

```go
// EmulationPolicy declares whether this app may be offered on an instance whose
// architecture it does not natively support.
//   "never"   — do not offer under emulation (default; GPU-bound apps)
//   "opt-in"  — offer behind the user's emulated-apps setting
const (
    EmulationNever = "never"   // default — the safe zero value is "do not pretend"
    EmulationOptIn = "opt-in"
)
```

Default **must** be `never` so the zero value and every existing entry mean "do not
offer", matching `StoreOnly`'s reasoning in `NODE-CAPABILITY.md` — the safe default is
structural rather than something each call site remembers.

**8.4 — Do not put arch in the run-lease path.** `launchWithConcurrency` acquires the
run-lease before checking anything about the machine. If arch ever gates launch, it must
be checked **before** `AcquireRunLease`, or a node that cannot run the app will take the
lease and then fail, denying it to a node that could.

**8.5 — Nothing in `arch.go` needs to change.** It is correct as written, including the
decision to report identity rather than binfmt capability. §4.4 independently confirms
its reasoning: a binfmt handler does not make an x86_64 ref appear in an aarch64 flatpak
installation, so consulting binfmt would produce a confidently wrong answer. If
emulation ships, the right change is a **separate** `EmulatedArches()` — never widening
`SupportedArches()`, which would silently reclassify "runs badly" as "runs".

---

## 9. What a user with a single ARM box gets today — plainly

- **96 of 119 catalogued apps**, installing natively, once §8.1 populates `arch` so the
  registry actually declares it. Today they see all 119 offered and 23 fail at install
  with a Flathub error rather than a Vulos explanation.
- **17 apps they cannot have**, with no emulation available in the product.
- **No cross-instance anything.** Not because it is switched off, but because the app
  set does not sync at all (§2.1) and the stream service has no concept of another node.

The gap between that and "one OS" is closed almost entirely by §5's primary
recommendation, which is a sync change, and only lastly by emulation.

---

## 10. Summary for the impatient

1. **The installed app set does not sync, by any mechanism** — two engines each defer to
   the other. So the arch-mismatch case **cannot arise yet**. Build the desired/realised
   split first; it is not an arch problem.
2. **The x86_64-only set is 17 apps (14.3%), not ~0.** Excluding proprietary removed 6
   of 23. Measured against live Flathub, not assumed.
3. **81% of the catalogue needs no emulation ever** — a Flatpak id is arch-independent
   and each instance pulls its own build.
4. **REVISED 2026-08-17 — box64 beats qemu-user on a DYNAMIC binary: 1.40× vs
   2.32×, and it binds the host's native GL stack while qemu cannot get a GL
   visual.** §4.3's "box64 failed" used a *static* binary, the one shape box64
   structurally cannot serve. box64 is not robust though: 2 crashes in 100 execs.
   FEX is still not packaged for Debian at all.
5. **GPU-bound apps are not emulatable IN THE FLATPAK SANDBOX** — that is a fact
   about the delivery vehicle, not about the app. Outside bwrap, in a prefix we
   control, box64 reaches the host's real GL stack (§4.3.1).
5a. **A foreign-arch Flatpak DOES install** (`--arch=x86_64`, measured, 1.4 GB
   deployed on an aarch64 box). Whether it runs is `untestable-on-arm64-mac`.
5b. **6 of the 17 have a native Debian arm64 build** and need not reach the
   emulation question at all — but consuming them needs an install vehicle that
   was **parked by founder decision 2026-08-17**. Blender was installed from one
   and rendered an image before the decision landed:
   `roadmap/DISTRO-SOURCED-APPS.md`.
6. **Ship labels, not silence.** An app that vanishes on the ARM laptop is a bug report;
   an app that says *"installed on studio-box, which is amd64"* is one OS being honest.
7. **Two catalogue ids are wrong today** and will fail at install: `org.krita.krita` →
   `org.kde.krita`, `io.lmms.lmms` → `io.lmms.LMMS`.
