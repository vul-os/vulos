# When a distribution is the only party doing the ARM build work

> ## Status: **PARKED by founder decision, 2026-08-17. Do not build this.**
>
> *"leave Blender out for now … Blender is one app and it is no longer worth the
> machinery."* **Blender ships amd64-only and badged honestly (§8.1). The
> frozen-closure vehicle is NOT to be built.**
>
> This document is kept because **the measurements outlive the decision** and
> three of them contradict things this repo currently records as settled:
>
> 1. **Debian builds an arm64 Blender** — `4.3.2+dfsg-2` — and 6 of the 17
>    x86_64-only catalogue apps have Debian arm64 builds (§2, §1.1).
>    INSTALL-METHODOLOGY §7.1's *"there is nothing to pin"* is true of
>    blender.org and false of the world.
> 2. **box64 was rejected on a test it cannot pass by construction** (§5). On a
>    dynamically linked binary it is correct, 1.40× native against qemu-user's
>    2.32×, and it binds the **host's own** aarch64 GL stack.
> 3. **A foreign-architecture Flatpak installs** (§6), which `arch.go`'s
>    `EmulationCanServe` comment says it cannot.
>
> §9 is the written handoff to the `backend/services/appnet/**` owner — **not
> edited here.** §10 is what was never proven. §11 is what was not reached.
> `scripts/freeze-debian-closure.sh` is committed, self-tested and **unused**;
> it is evidence for §4's costing, not a component of anything.

## 0. The one-paragraph answer

**Blender has an ARM64 build and Debian makes it.** `blender 4.3.2+dfsg-2` is in
Debian trixie for `arm64`, measured, not inferred. The migration's conclusion —
*"blender.org publishes no official Linux aarch64 build either, so no `artifacts`
entry can recover the coverage: there is nothing to pin"* — is true about
**blender.org** and false about **the world**. Debian compiles it from source for
arm64 and has for years. So the founder's *"if blender does have arm have a way
to install it"* is the branch that applies, and the answer is a **frozen Debian
closure**: run the solver once, at catalogue time, freeze the exact package set
with a SHA-256 per package pinned to `snapshot.debian.org`, and have the box
extract that fixed list into the app's own private prefix with `dpkg-deb -x`.
The box resolves nothing. **This was carried out end to end and Blender rendered
an image (§4.4)**, and it is **not** a Blender special case: **6 of the 17**
x86_64-only catalogue apps have a Debian arm64 build.

---

## 1. The per-app decision order

This is the rule the founder's two constraints resolve to. *"Efficient and good
choices"* orders the options; *"everything must work everywhere"* forbids
stopping before the bottom of the list. Both halves survive.

**Apply in order. Stop at the first that holds. Never skip a rung to reach a
more convenient answer.**

| # | Rung | Test | What the user gets |
|---|---|---|---|
| **1** | **Native, upstream** | upstream publishes a build for this arch (Flathub ref exists / vendor artefact exists) | Install button. No badge. |
| **2** | **Native, distribution-sourced** | a distribution builds it from source for this arch — measured with `apt-cache policy <pkg>` in a container of that arch | Install button. No badge. **PARKED 2026-08-17 — this rung is designed and costed (§4) but deliberately NOT built (§8). Until it is, apps that would land here fall through to rung 3 or 5.** |
| **3** | **Emulated, and the user is told what they are getting** | the app is delivered as ELF files into a prefix we control (vehicle B or C — **not** Flatpak), an emulator is present, and `emulation_policy: "opt-in"` | `Needs emulation` badge, the cost stated in the sentence, off until the user turns it on |
| **4** | **Available elsewhere in the fleet** | a synced sibling instance runs it | `On your other instance`, naming the instance |
| **5** | **Declared unavailable, with the reason** | none of the above | `Not available on this box` + why. **Never a bare "Unavailable", never a silent disappearance.** |

Three things this ordering is deliberately strict about:

- **Rung 2 is above rung 3.** A native Debian arm64 Blender beats an emulated
  x86_64 Blender on every axis — speed, memory, robustness, GL — and it is
  cheaper to download (§4). Reaching for emulation while a native build exists
  is the "efficient and good choices" half being dropped.
- **Rung 3 is above rung 5.** §5 measures that box64 runs a dynamically linked
  x86_64 binary correctly, at **1.40× native** (against qemu-user's 2.32× on the
  same binary), and binds the *host's own* aarch64 GL stack. Declaring an app unavailable when that path exists is the "everything
  must work everywhere" half being dropped. But rung 3 is **opt-in and labelled**
  — an app that is present and crawls reads as a Vulos defect rather than a
  hardware limit, which is why it is not rung 2.
- **Rung 5 still ships a row in the App Hub.** ARCH-PLACEMENT §6 is unchanged:
  E1–E3 are exceptions to uniform *availability*, never to uniform *visibility*.

### 1.1 What the order does to the 17

Measured (§3, §5). "Debian arm64" is rung 2; the rest fall to rung 3 or 5.

| App | Rung | Why |
|---|---|---|
| Blender | **2** | `blender 4.3.2+dfsg-2` arm64 |
| OBS Studio | **2** | `obs-studio 30.2.3+dfsg-3` arm64 |
| HandBrake | **2** | `handbrake 1.9.2+ds1-1+b1` arm64 |
| Thunderbird | **2** | `thunderbird 1:140.13.0esr-2~deb13u1` arm64 |
| OpenSCAD | **2** | `openscad 2021.01-8+b2` arm64 |
| Cura | **2** | `cura 5.0.0-6` arm64 (old — 5.0.0 vs upstream's 5.x; a rung-2 entry must state the version gap) |
| torbrowser-launcher | 5 | package exists, **no arm64 candidate** |
| protontricks, marktext, heroic, bottles, lutris, PCSX2, Signal, Upscayl, Sober, Insomnia | 3 or 5 | not packaged by Debian at all; rung 3 only if a per-arch **binary** artefact exists to emulate, else rung 5 |

**Six of seventeen would move from "not available" to "native"** if rung 2 were
built. It is not, and today all six sit at rung 3 or 5. That count is the
trigger condition for revisiting the decision in §8: the argument against
building rung 2 is *"one app does not justify a third vehicle"*, and the honest
answer to that is **six**, not one.

---

## 2. Confirming the premise: does an ARM64 Blender exist?

**Yes.** In a `debian:trixie-slim` **arm64** container:

```
=== uname -m in container ===
aarch64
=== dpkg architecture ===
arm64
=== apt-cache policy blender ===
blender:
  Installed: (none)
  Candidate: 4.3.2+dfsg-2
  Version table:
     4.3.2+dfsg-2 500
        500 http://deb.debian.org/debian trixie/main arm64 Packages
EXIT_POLICY=0
```

Debian builds Blender from source for arm64. Flathub does not
(`flatpak remote-info --arch=aarch64 flathub org.blender.Blender` →
`Can't find ref org.blender.Blender/aarch64`, §6) and neither does blender.org.
**Debian is the only party in the world doing this build work**, which is exactly
the case the rule had no answer for.

---

## 3. Reframing the rule — pinned, per-architecture, checksummed

INSTALL-METHODOLOGY §2.1 states the load-bearing objection to apt:

> `apt-get install -y blender` installs whatever Debian ships **that day**. Two
> instances that install a month apart get two different Blenders, and neither
> the registry entry nor the sync wire records which.

The defect named there is **unpinned, time-varying resolution**, not the letter
`d`, `p`, `k`, `g`. Read the table in §2.1 again: the column is *"what it pins"*.
apt scores "nothing" because nobody froze its answer — not because a `.deb` is
unfreezable.

**So freeze it.** Run the solver **once, offline, at catalogue time**; record the
exact package set and a SHA-256 per package computed from bytes that arrived;
ship that inside the Ed25519-signed entry. Two instances a year apart then
install byte-identical Blenders, because they are fetching the same 315 files by
the same immutable URLs and verifying the same 315 digests.

Against §4.6's own list of what is deliberately not in the format:

| §4.6 rule | Does a frozen closure violate it? |
|---|---|
| **No dependency solver** | **No.** The solver runs on a curator's machine, months earlier. The box does zero resolution — it iterates a fixed array. |
| **No package manager** | **No, and this is the sharpest point.** `dpkg-deb -x` is an *archive extractor*: a `.deb` is an `ar` archive containing a `data.tar.xz`, and `-x` unpacks that tar. It touches no dpkg database, runs no maintainer script, resolves nothing, and writes nothing outside the target directory. It is `tar -x` with a different container format. |
| **No optional integrity check** | **No.** Every package carries a mandatory SHA-256; a manifest with one missing is refused by `CLOSURE-01`. |
| **No install shell** | **No.** There is no shell anywhere in this path. |
| **No `latest` URLs** | **No.** `snapshot.debian.org/archive/debian/<stamp>/…` is immutable by construction. This is *stricter* than the status quo: `deb.debian.org/pool/…` is itself a moving target that 404s when Debian publishes the next version, which is why the frozen URL must be a snapshot URL and `CLOSURE-02` refuses a pool URL. |
| **No mirror** | **No.** Vulos hosts nothing. The bytes come from Debian's own archive. |
| **No arch-specific state on the sync wire** | **No.** The closure lives *inside* the signed entry, keyed by arch, exactly like `artifacts`. The wire still carries `app_desired(app_id, …)`. |

The one rule it genuinely changes is *"one format, two vehicles"* → **three**.
That is a real cost and §8 argues it anyway.

### 3.1 The prefix, and why the system tree is not an option

Extraction target is `$HOME/.vulos/apps/<id>/prefix`, the app's own directory —
never `/usr`. On a live Vulos box the system tree is squashfs under dm-verity
(read-only) and its writable overlay is a tmpfs, so a system-tree write is both
**illegal** and **volatile**. A private prefix plus `LD_LIBRARY_PATH` is the only
shape that works, and it is the same shape vehicle B already produces.

It also gives the property that makes rung 3 possible at all: a private prefix of
dynamically linked ELF files is precisely what box64 is built to accelerate, and
precisely what a Flatpak sandbox denies it (§5.3).

---

## 4. The measured cost

Everything below was measured on 2026-08-17 in `debian:trixie-slim` **arm64**
containers on this machine.

### 4.1 Closure size

| Resolution | Packages | Download | Unpacked |
|---|---:|---:|---:|
| `apt-get install --print-uris -y blender` (with recommends) | 437 | 323.3 MB | — |
| `--no-install-recommends` | **350** | **261.9 MB** | **997 MiB** |
| `--no-install-recommends`, base already carrying curl/python3/ca-certificates | 315 | 251.4 MB | — |

`--no-install-recommends` is the correct setting and not merely the smaller one:
the 87 packages it drops are `systemd`, `dbus`, `adduser`, `gnupg` and friends —
init-system components that have no business inside an application prefix.

### 4.2 The comparison that decides it

`flatpak remote-info --arch=x86_64 flathub org.blender.Blender`, run from an
aarch64 installation:

```
        ID: org.blender.Blender
       Ref: app/org.blender.Blender/x86_64/stable
      Arch: x86_64
  Download: 477.4 MB
 Installed: 1.1 GB
   Runtime: org.freedesktop.Platform/x86_64/25.08
```

| Vehicle | Download | Installed |
|---|---:|---:|
| Flathub Blender x86_64 (**already an accepted vehicle**) | 477.4 MB | 1.1 GB **plus the platform runtime** |
| Debian arm64 frozen closure | **261.9 MB** | **997 MiB, self-contained** |

**The mechanism being questioned on cost is cheaper than the vehicle already in
the format**, on both axes, and it delivers a native build rather than an
emulated one. The 997 MiB figure includes everything — libc, Python 3.13, the
whole GL and codec stack — because the prefix carries its own world.

That is the answer to "measure the cost before committing": **the cost is
reasonable and the recommendation is to proceed.**

### 4.3 The costs that are real, stated plainly

- **315–350 HTTP fetches per app install.** One file per package.
  `snapshot.debian.org` is a single, historically slow host. **This is the
  weakest part of the mechanism** and it is an availability dependency on
  infrastructure Vulos does not run. It should be measured under load before
  rung 2 is enabled for a second app.
- **~70 KB of JSON per architecture inside the signed entry.** 315 objects of
  `{name, version, filename, url, size, sha256}`. Large but not absurd, and it
  is data inside the signature, which is the property that makes it auditable.
- **No sharing between apps.** Three rung-2 apps carry three copies of libc.
  Blender + OBS + Thunderbird ≈ 3 GB where a shared system would use far less.
  A content-addressed shared store would fix this and is **explicitly not
  proposed here** — that is how a manifest becomes a distribution.
- **A Debian security update re-freezes and re-signs the entry.** The frozen
  closure is a *pin*, so it does not float to the fixed version by itself. This
  is the same property `artifacts` already has, at 315× the surface.
- **The closure is DIFFERENTIAL against a declared base.** `--print-uris`
  reports what is missing from the container it ran in — 350 against
  `debian:trixie-slim`, 315 against a slightly richer one. The manifest
  therefore records `base_image`, `CLOSURE-03` refuses a manifest without one,
  and **a box whose base differs must not use the manifest.** Leaving this
  implicit is how a closure silently becomes wrong after an OS release.

### 4.4 The end-to-end proof: it actually runs

Resolved, fetched, extracted and executed in a **pristine `debian:trixie-slim`
arm64 container with nothing installed into it** — the base the manifest
declares, and nothing else:

```
base: debian 13.6 / arm64 / aarch64
packages installed in this container BEFORE: 78
extracted 350 .deb files into /app/blender/prefix  (size 1001M)

=== unresolved libraries ===
  count=0

############ 1. blender --version ############
Blender 4.3.2
VERSION_EXIT=0

############ 2. blender resolves its own resources from the prefix ############
RESOURCE_PATH: /app/blender/prefix/usr/share/blender
SCRIPT_PATHS: ['/app/blender/prefix/usr/share/blender/scripts']

############ 3. REAL WORK: headless Cycles CPU render of the default scene ############
Fra:1 Mem:10.88M (Peak 10.88M) | Time:00:00.02 | Scene, ViewLayer | Sample 8/8
Fra:1 Mem:10.88M (Peak 10.88M) | Time:00:00.02 | Scene, ViewLayer | Finished
Saved: '/out.png'
RENDER-DONE
RENDER_EXIT=0
--- the rendered image ---
-rw-r--r-- 1 root root 10580 Aug 17 02:03 /out.png
  PNG magic: 89 50 4e 47 0d 0a 1a 0a

=== the system tree was NEVER written ===
ls: cannot access '/usr/bin/blender': No such file or directory
dpkg-query: no packages found matching blender
packages installed in this container AFTER: 78
```

**Blender 4.3.2, native aarch64, ran from the app's own private prefix and
rendered a real image.** Not a version string — a 128×128 Cycles CPU render of
the default scene, 8 samples, written as a valid 10,580-byte PNG. All 350
packages verified against the apt index size and then hashed. The container
finished with the **same 78 packages it started with** and no `/usr/bin/blender`,
so nothing reached the system tree.

**Getting there took four failed attempts, and every failure is a design
requirement in disguise.** Each one would have shipped as "installed
successfully, app will not start":

| Attempt | Failure | What it means for the vehicle |
|---|---|---|
| 1 | `libraw_r.so.23: cannot open shared object file` | one `.deb` failed to download and one was **truncated**. The size check caught the truncation — a partial file that would otherwise have been hashed and pinned as a valid artefact. **The downloader must verify size before hashing, and delete partials.** |
| 2 | `libpulsecommon-17.0.so`, `liblapack.so.3`, `libblas.so.3` not found | these live in **subdirectories** (`pulseaudio/`, `lapack/`, `blas/`) that Debian wires up through `/etc/ld.so.conf.d/` fragments and `update-alternatives` symlinks. **`dpkg-deb -x` creates neither.** The launcher must *derive* `LD_LIBRARY_PATH` from the extracted tree — 11 entries here, not the 3 an assumption would give. |
| 3 | `libexpat.so.1: cannot open shared object file` | the closure had been resolved in a container that already had `python3`+`curl`, so `libexpat1` was omitted; against a pristine base it is missing. **This is CLOSURE-03 demonstrated rather than argued** — a differential closure used against the wrong base is silently broken. |
| 4 | `bpy: couldn't find 'scripts/modules'`, then SIGSEGV | the resource path variable is **`BLENDER_SYSTEM_RESOURCES`**, not the `BLENDER_SYSTEM_SCRIPTS`/`_DATAFILES` pair that reading the docs suggests, and Debian flattens the layout (`share/blender/scripts`, not `share/blender/4.3/scripts`). A six-variant matrix found the working combination; four of the six segfaulted. **A rung-2 recipe's env must be measured per app, never guessed.** |

A fifth issue is a *capability* gap rather than a packaging one: Debian's arm64
Blender is built **without OpenImageDenoise**, so a render with denoising enabled
fails with `Failed to denoise, build has no OpenImageDenoise support`. Rung 2
buys a native build, not feature parity with upstream, and a rung-2 entry must
say so.

### 4.5 snapshot.debian.org serves identical bytes — tested, not assumed

The design's central assumption is that a snapshot URL constructed by string
substitution serves the same bytes as the live pool URL. Five packages were
fetched from **both** and hashed:

```
--- blender-data_4.3.2+dfsg-2_all.deb
 snapshot: ab95b1d6f75a7e9e38d251e8720c13a5d45be264ca90d0dfb4658374f6a5d137
 measured: ab95b1d6f75a7e9e38d251e8720c13a5d45be264ca90d0dfb4658374f6a5d137
  IDENTICAL
--- libgl1_1.7.0-1+b2_arm64.deb            IDENTICAL (7943f999…)
--- libpython3.13_3.13.5-2+deb13u4_arm64.deb IDENTICAL (d8f0a0d8…)
--- blender_4.3.2+dfsg-2_arm64.deb         IDENTICAL (81b13a52…)
--- adduser_3.152_all.deb                  IDENTICAL (e50984d2…)
```

**5 of 5 agree, including Blender itself.** `snapshot.debian.org` is a usable
pin. **345 of the 350 remain unchecked** and §10 records that.

---

---

## 5. box64 re-measured — the rejection did rest on a bad measurement

ARCH-PLACEMENT §4.3 recorded box64 as failing with `Illegal instruction` and §5
recommended qemu-user on that basis. The test binary was **`busybox-static`**.

The same document explains, three paragraphs later, why that test could not have
come out any other way:

> **box64 gets its speed by *not* emulating libraries** — it intercepts calls and
> substitutes native aarch64 ones. That requires the binary to be **dynamically
> linked**.

**A static binary is the one shape box64 structurally cannot serve.** The
measurement exercised box64's known non-case and the verdict was generalised from
it. §4.3 even flagged this and deferred the fair test to an empty §4.3.1. This is
that test.

### 5.1 Does box64 run a dynamically linked x86_64 binary? Yes.

x86_64 sysroot assembled with `dpkg-deb -x` — the same shape vehicle C produces.
Both emulators got the same tree.

```
under test : ELF 64-bit LSB pie executable, x86-64, version 1 (SYSV), dynamically linked,
             interpreter /lib64/ld-linux-x86-64.so.2, ..., stripped
native ctrl: ELF 64-bit LSB pie executable, ARM aarch64, version 1 (SYSV), dynamically linked,
             interpreter /lib/ld-linux-aarch64.so.1, ..., stripped

native ref: exit=0 bytes=2061307 md5=fbec53519a4315a13a5d192518adf61f
box64       exit=0 bytes=2061307 md5=fbec53519a4315a13a5d192518adf61f
```

**Exit 0, and byte-identical output to the native run.** Not "it printed
something" — the md5 of 2,061,307 bytes of gzip output matches the aarch64
reference exactly. That is the correctness gate this document requires before any
timing is believed.

### 5.1.1 The three-way comparison, all cases byte-identical

One further correction was needed before the numbers meant anything. The first
fair attempt gave qemu an **incomplete x86_64 sysroot** — box64 sailed past a
missing `libcap.so.2` because it substitutes the native one, while qemu, which
needs the real x86_64 library, died. The resulting ratio would have measured the
sysroot, not the emulator. A second attempt then had qemu fail on a *path*
lookup. The final harness gives both the same tree and routes **every** case
through stdin/stdout, so no case opens a path at all:

```
native reference: exit=0 bytes=2061295 md5=0089e3b1bbf60c781d6a27b0c99f865a
box64             exit=0 bytes=2061295 md5=0089e3b1bbf60c781d6a27b0c99f865a
qemu              exit=0 bytes=2061295 md5=0089e3b1bbf60c781d6a27b0c99f865a

=== 7 interleaved reps, DISCARDED unless exit=0 and bytes==reference ===
rep=1 native ms=4719   box64 ms=6768   qemu ms=11467
rep=2 native ms=4553   box64 ms=6718   qemu ms=11202
rep=3 native ms=4914   box64 ms=6968   qemu ms=11760
rep=4 native ms=5120   box64 ms=6923   qemu ms=11401
rep=5 native ms=4941   box64 ms=6718   qemu ms=11124
rep=6 native ms=5170   box64 ms=7286   qemu ms=11772
rep=7 native ms=5134   box64 ms=7022   qemu ms=11655
```

| | native | box64 0.3.4 | qemu-x86_64 10.0.11 |
|---|---:|---:|---:|
| Throughput, median of 7 | 4941 ms | **6923 ms = 1.40×** | **11467 ms = 2.32×** |
| Per-exec, 100 execs | 44 ms | 1128 ms = 25.6× | 2697 ms = 61.3× |
| Peak RSS, one pass | 1816 KB | 27156 KB = 15.0× | 12240 KB = 6.7× |
| Correct output | ✅ | ✅ byte-identical | ✅ byte-identical |
| Exited 0 on 100 execs | 100/100 | **98/100** | 100/100 |

**box64 is 1.66× faster than qemu-user on throughput and 2.4× faster per exec.**
Both produce byte-identical output. This is the comparison ARCH-PLACEMENT §5
made on evidence that could not support it.

*(qemu's 2.32× here is not comparable with §4.3's 3.80×: that was busybox's gzip,
statically linked; this is Debian's gzip, dynamically linked. Only the
within-table ratios are meaningful.)*

### 5.2 But box64 is not robust, and that is measured too

```
=== ROBUSTNESS: 100 execs of `gzip --version`, successes counted ===
native: 44 ms / 100 execs, 100/100 exited 0
box64: 1128 ms / 100 execs, 98/100 exited 0
qemu:  2697 ms / 100 execs, 100/100 exited 0
```

and in a separate 30-exec run the failure was visible by name:

```
Illegal instruction  BOX64_LD_LIBRARY_PATH=... box64 /x86/usr/bin/gzip --version
box64: 526 ms / 30 execs, 29/30 exited 0
```

**Two SIGILLs in a hundred identical invocations of the same binary** — same
command, same arguments, non-deterministic. qemu-user did not fail once in 100.

ARCH-PLACEMENT §4.5 predicted exactly this and it is now measured rather than
asserted: **qemu-user is robust and slow; box64 is fast and not robust.** A ~2%
per-exec crash rate is severe for a desktop app, which is a process that spawns
dozens of short-lived helpers — and it is why rung 3 is opt-in and labelled
rather than a default posture. It is **not** a reason to keep box64 rejected:
2% is a defect to be measured per app, not an impossibility.

### 5.3 GL — the finding that matters most for desktop apps

Under Xvfb, `glxinfo -B`:

```
-- native aarch64 glxinfo (control) --
direct rendering: Yes
    Vendor: Mesa (0xffffffff)
    Device: llvmpipe (LLVM 19.1.7, 128 bits) (0xffffffff)
    Version: 25.0.7
D_NATIVE_EXIT=0

-- x86_64 glxinfo under box64 --
direct rendering: Yes
    Vendor: Mesa (0xffffffff)
    Device: llvmpipe (LLVM 19.1.7, 128 bits) (0xffffffff)
    Version: 25.0.7
    Max core profile version: 4.5
D_BOX64_EXIT=0

-- x86_64 glxinfo under qemu-user --
Error: couldn't find RGB GLX visual or fbconfig
D_QEMU_EXIT=0
```

An **x86_64** GL client under box64 reported the **host's own aarch64 Mesa
25.0.7 / LLVM 19.1.7** — the identical renderer string, version and
`direct rendering: Yes` as the native control. box64's native-library
substitution reached the GL stack. Under qemu-user the same binary could not
obtain a GL visual at all.

**Two honest limits on this result:**

1. **This is not proof of hardware acceleration.** A container has no GPU, so the
   native stack here is `llvmpipe` and both lines say `Accelerated: no`. What is
   proved is **which stack was bound** — box64 escaped emulation into the host's
   real driver stack, which is the mechanism that would deliver hardware
   acceleration on a box that has a GPU. That claim is untested and stays
   untested until someone runs it on real hardware.
2. **`D_QEMU_EXIT=0` on a run that plainly failed.** `glxinfo` exits 0 while
   printing `Error: couldn't find RGB GLX visual`. Exit status alone would have
   scored qemu as a success here. **Checking exit status is necessary and not
   sufficient; the output has to be read too.**

### 5.4 What this does to exception E3

ARCH-PLACEMENT E3 says GPU-bound apps stay unavailable on arm64 *even with
emulation enabled*, reasoning that "an x86_64 Mesa under emulation loses
acceleration, and box64's native-GL wrapping cannot reach inside Flatpak's
sandbox."

The **Flatpak half of that sentence stands** and §6 does not disturb it. The
**general half does not**: outside a Flatpak sandbox — in a private prefix, which
is what vehicles B and C produce — box64's GL wrapping demonstrably works. So E3
is correct *as a property of Flatpak delivery* and over-broad *as a property of
the app*. It should be re-scoped to delivery, which is the shape `DeliveryKind`
already has.

---

## 6. Foreign-architecture Flatpak — the asserted impossibility, tested

`arch.go` states that a qemu-user binfmt handler "does not make an x86_64 ref
appear in an aarch64 flatpak installation", so `EmulationCanServe` returns true
only for `DeliveryBinary`.

**On `flatpak --supported-arches`, that is right. On `--arch=x86_64`, it is not.**

**Q1 — can an aarch64 installation *see* the x86_64 ref?** Yes:

```
$ flatpak remote-info --arch=x86_64 flathub org.blender.Blender
        ID: org.blender.Blender
       Ref: app/org.blender.Blender/x86_64/stable
      Arch: x86_64
  Download: 477.4 MB
 Installed: 1.1 GB
   Runtime: org.freedesktop.Platform/x86_64/25.08
    Commit: f97247d9e87dca0bc28c6a01e51cd6425cf8b21c636d28514898b7315b21d521
Q1_EXIT=0

$ flatpak remote-info --arch=aarch64 flathub org.blender.Blender
error: Error searching remote flathub: Can't find ref org.blender.Blender/aarch64
```

The aarch64 installation resolved a full x86_64 ref including its commit. Note
also that the *failing* command reported `Q1B_EXIT=0` — flatpak exits 0 on that
error, a second instance of §5.3's trap.

**Q2 — does `flatpak install --arch=x86_64` actually deploy?** Yes, completely:

```
$ flatpak install -y --noninteractive --arch=x86_64 flathub org.gnome.Calculator
Installing runtime/org.freedesktop.Platform.GL.default/x86_64/25.08
Installing runtime/org.freedesktop.Platform.GL.default/x86_64/25.08-extra
Installing runtime/org.freedesktop.Platform.codecs-extra/x86_64/25.08-extra
Installing runtime/org.gnome.Calculator.Locale/x86_64/stable
Installing runtime/org.gnome.Platform.Locale/x86_64/50
Installing runtime/org.gnome.Platform/x86_64/50
Installing app/org.gnome.Calculator/x86_64/stable

$ flatpak list --columns=application,arch,branch,installation
org.freedesktop.Platform.GL.default    x86_64  25.08         system
org.freedesktop.Platform.GL.default    x86_64  25.08-extra   system
org.freedesktop.Platform.codecs-extra  x86_64  25.08-extra   system
org.gnome.Calculator                   x86_64  stable        system
org.gnome.Platform                     x86_64  50            system
```

**An x86_64 application and its entire x86_64 GNOME platform runtime are
installed on an aarch64 box** (1.4 GB deployed). The install-side claim in
`arch.go` is wrong: `--arch=x86_64` is an explicit flag and it is honoured.

**What this does and does not change.** The `EmulationCanServe` comment is right
about *why* it says what it says (`--supported-arches` does not widen) and wrong
about the *conclusion* (a foreign-arch Flatpak can be installed). But "can be
installed" is not "should be offered": §5.3 measured that box64's GL wrapping
cannot reach inside bwrap's namespace, which supplies the runtime's x86_64
`/usr`, and qemu-user cannot get a GL visual at all. So the honest revision is
that Flatpak's exclusion from rung 3 is a **policy** conclusion about quality,
not a **capability** conclusion about possibility — and the two should not share
one function. §9.1.

**Q3 — does it run?** **Not established, and the reason is not architecture:**

```
$ flatpak run --arch=x86_64 org.gnome.Calculator --help
bwrap: Creating new namespace failed: Operation not permitted
error: ldconfig failed, exit status 256
Q3_EXIT=0
```

`bwrap` could not create a user namespace — a **container privilege** limit, not
an architecture one. The same failure would occur for a *native* aarch64 app in
this container.

> **NOT PROVEN, and it may not be proven here at all.** Three independent
> reasons: (a) `bwrap` cannot create namespaces under Docker's default seccomp;
> (b) `/proc/sys/fs/binfmt_misc` is **not visible in these containers**, so no
> handler could be registered; (c) on this Apple-Silicon host a binfmt handler
> would be serviced by **Rosetta 2**, which does not exist on any real arm64
> Vulos box (ARCH-PLACEMENT §4.2) — so even a success would have been a
> confidently wrong number. **This needs a real arm64 Linux box and is recorded
> as `untestable-on-arm64-mac`.**

Note again that `Q2_EXIT=0` and `Q3_EXIT=0` on runs that printed `bwrap:
Operation not permitted`. That is the third command in this document to exit 0
while failing.

---

## 7. The frozen-closure format

`scripts/freeze-debian-closure.sh` emits, and `--self-test` enforces:

```jsonc
{
  "schema": "vulos.debian-closure/1",
  "app": "blender",
  "package": "blender",
  "suite": "trixie",
  "base_image": "debian:trixie-slim",      // CLOSURE-03: the closure is DIFFERENTIAL
  "snapshot": "20260816T000000Z",
  "arches": {
    "arm64": {
      "resolved_version": "4.3.2+dfsg-2",
      "package_count": 350,
      "download_bytes": 261877420,
      "packages": [
        { "name": "blender", "version": "4.3.2+dfsg-2",
          "filename": "blender_4.3.2+dfsg-2_arm64.deb",
          "url": "https://snapshot.debian.org/archive/debian/20260816T000000Z/pool/main/b/blender/blender_4.3.2+dfsg-2_arm64.deb",
          "size": 0, "sha256": "…" }
      ]
    }
  }
}
```

Rules, each with a fixture in `--self-test` that **names which rule must answer**:

| id | rule |
|---|---|
| CLOSURE-01/09 | every package carries a 64-hex SHA-256 |
| CLOSURE-02/04 | the URL is `https://snapshot.debian.org/archive/…` — a `deb.debian.org/pool/…` URL is a MOVING target and is refused |
| CLOSURE-03 | `base_image` non-empty — a differential closure with no declared base is unusable |
| CLOSURE-05 | two packages may not share one SHA-256 |
| CLOSURE-06 | `package_count` must equal the list length |
| CLOSURE-07 | `download_bytes` must equal the sum of sizes |
| CLOSURE-08 | size > 0 |
| CLOSURE-10 | arch keys are Debian spelling (`arm64`, never `aarch64`) |
| CLOSURE-11 | the arches map is non-empty |
| CLOSURE-12 | a filename may not escape the download directory |
| CLOSURE-13 | unknown schema refused |
| CLOSURE-14/16 | the snapshot stamp is well-formed and every URL is pinned to it |

**Not one digest is copied from apt.** `--print-uris` emits an MD5 and sometimes
nothing at all (trixie's `libexpat1` line carries no hash field, because it comes
from the security archive). Every SHA-256 is computed by the tool **from the
bytes the snapshot URL actually served**, so the digest and the URL cannot
disagree by construction.

### 7.1 How the guards were proved

Reading a guard does not clear it. Thirteen mutations planted one at a time in
the checker, each reverted programmatically from a private baseline copy:

| # | mutation | result |
|---|---|---|
| M1 | sha256 hex check never fires | killed — CLOSURE-01 and CLOSURE-09 both go red |
| M2 | snapshot-URL check never fires | killed — CLOSURE-04 red |
| M3 | duplicate-sha256 check never fires | killed — CLOSURE-05 red |
| M4 | **over-broad**: the validator refuses everything | killed **by the control**, not by any refusal fixture |
| M5 | base_image check never fires | killed — CLOSURE-03 red |
| M6 | arch-spelling check never fires | killed — CLOSURE-10 red |
| M7 | filename-traversal check never fires | killed — CLOSURE-12 red |
| M8 | size>0 check never fires | killed — CLOSURE-08 red |
| M9 | package_count check never fires | killed — CLOSURE-06 red |
| M10 | download_bytes check never fires | killed — CLOSURE-07 red |
| M11 | schema check never fires | killed — CLOSURE-13 red |
| M12 | empty-arches check never fires | killed — CLOSURE-11 red |

**Two defects were found this way, in the checker, before it was used on
anything.**

1. **The first `--self-test` run reported 13 of 13 refusals behaving and its one
   CONTROL rejected.** `validate_manifest` was defined *below* the self-test
   block, so bash had not seen the function: every call died with "command not
   found", which scores as a refusal. **All thirteen guards were passing
   vacuously and only the control could tell.** This is why M4 exists — a rule
   that refuses everything passes every negative test ever written for it.

2. **Two fixtures were being answered by a neighbouring rule.** With the
   snapshot-URL check disabled, CLOSURE-02's fixture stayed green because
   CLOSURE-16 (*url not pinned to the declared snapshot*) answered instead; with
   the size>0 check disabled, CLOSURE-08's fixture stayed green because
   CLOSURE-07 (*download_bytes disagrees with the sizes*) answered instead.
   Both guards could have been deleted with the suite green. Fixtures now assert
   **which rule id answered**, and the same mutations now report
   `wrong rule answered` naming the rule that actually fired. This is
   INSTALL-METHODOLOGY §10's M1 defect, caught in a new checker before it
   shipped.

A third defect was found by the measurement itself rather than by a fixture: the
downloader ran `curl` with `--retry` but **no `--connect-timeout` or
`--max-time`**, and one `deb.debian.org` socket hung for over five minutes during
the closure run. A 315-file sequential loop with one stalled socket is
indistinguishable from a slow network. Timeouts and `--speed-limit` are now set.

---

## 8. Recommendation, and the decision that overrode it

**What the measurement recommended:** build it for the six rung-2 apps. The cost
question has a measured answer — a frozen Debian arm64 closure for Blender is
**262 MB / 997 MiB**, **less** than the 477 MB / 1.1 GB Flathub x86_64 build
already accepted as a vehicle, and it yields a native app that **renders images**
(§4.4) rather than an emulated one.

**What was decided, 2026-08-17:** *"leave Blender out for now … Blender is one
app and it is no longer worth the machinery."* **The mechanism is parked.**

Both are recorded because the reasoning differs from the outcome and a future
reader needs to know which is which. The decision is not a rebuttal of §4 — it
is a judgement that **one app does not justify a third install vehicle**, which
is a different and entirely defensible claim. The number that would reverse it is
not Blender's cost; it is **how many apps land on rung 2**. That number is **6**
today (§1.1) and every one of them is a desktop application a user would notice.

### 8.1 What Blender ships as

`registry.d/apt-to-flatpak.json` already stages Blender correctly for this
outcome — `arch: ["amd64"]`, Flathub vehicle, `_disabled` absent — so **no
registry change is required and none was made.** `EvaluateArch` will render:

> **Not available on this box** — *Blender ships for amd64 only and needs
> graphics acceleration, which emulation cannot provide. It stays available on
> your amd64 instances.*

That is honest and it is the accepted outcome. **One correction is owed to that
entry's `_note`, which is now factually wrong** and is a handoff rather than an
edit, because the fragment belongs to another agent:

> The `_note` currently reads *"blender.org publishes no official Linux aarch64
> build either, so no `artifacts` entry can recover arm64 coverage; there is
> nothing to pin."*
> **Measured false.** Debian trixie ships `blender 4.3.2+dfsg-2` for arm64 and it
> was installed and made to render on this machine (§4.4). The accurate sentence
> is: *"An arm64 build exists — Debian compiles one — but consuming it needs a
> pinned-closure install vehicle that Vulos deliberately does not have. Parked by
> founder decision 2026-08-17: one app does not justify a third vehicle. See
> roadmap/DISTRO-SOURCED-APPS.md."*

The distinction matters: **"no build exists" invites nobody to look again;
"a build exists and we chose not to consume it" is a decision with a trigger
condition.**

### 8.2 If it is ever unparked

Do not build a shared package store. One prefix per app, libc duplicated per
app. The moment packages are shared the box must know which app needs which
version of what, and that is a dependency solver — the thing §4.6 refuses. The
duplication is the price of not becoming a distribution.

---

## 9. Handoff to the `appnet` owner — do not apply these here

`backend/services/appnet/**` is another agent's file. These are recommendations,
not edits.

### 9.1 `EmulationCanServe` conflates two questions and should be split

Today: `func EmulationCanServe(k DeliveryKind) bool { return k == DeliveryBinary }`.

The block comment above it justifies excluding Flatpak with *"the ref does not
exist for this installation and the install fails at resolve time"*. §6 measured
that the ref **does** resolve for an aarch64 installation via `--arch=x86_64`,
and that x86_64 refs **do** deploy. The comment is right about
`--supported-arches` and wrong about the conclusion drawn from it.

The distinction that survives measurement is not *can it be installed* but *would
it be any good*:

```go
// EmulationCanInstall — could the bits get here at all?
//   binary  : yes (an ELF and a kernel handler)
//   closure : yes (ELFs in a prefix we control)
//   flatpak : yes — MEASURED 2026-08-17, `flatpak install --arch=x86_64`
//             deploys x86_64 refs into an aarch64 installation.
//   package : no  (dpkg architecture refuses first)
//
// EmulationRunsWell — would the result be worth offering?
//   binary, closure : yes — box64 binds the HOST's native aarch64 GL stack,
//                     measured identical renderer string to native.
//   flatpak         : NO — bwrap supplies the runtime's x86_64 /usr, so box64
//                     cannot reach the host's libraries and qemu-user cannot
//                     obtain a GL visual at all.
```

`EvaluateArch` should gate on `CanInstall && RunsWell`, and the two failures
should produce **different sentences**, because they are different facts about
the user's box.

### 9.2 `NeedsGPU` should be scoped to delivery, not applied to the app

`EvaluateArch`'s first switch arm makes `NeedsGPU` an unconditional
`ArchStateUnavailable`. §5.3 measured that in a private prefix box64 reaches the
host GL stack, so for `DeliveryBinary`/closure that arm is a false negative. E3
is a fact about **Flatpak**, not about Blender.

### 9.3 `EmulatedArches()` misses box64 entirely

It probes `/proc/sys/fs/binfmt_misc` only. **box64 does not register a binfmt
handler by default** — it is invoked by name, which is how every measurement in
§5 was taken. A box with box64 installed and working therefore reports *no
emulation available*. It needs a second source: the presence and successful
`--version` of a `box64` / `qemu-x86_64` binary. Keep it separate from
`SupportedArches()`, exactly as §8.5 argues.

### 9.4 A new `DeliveryKind` and a new vehicle

`DeliveryKindOf` needs a `DeliveryDebianClosure` arm, and
`validateRecipeSecurity` needs the CLOSURE-\* rules from §7 mirrored in Go with
the same ordering discipline as §6.1 — `TestDownloadURLRulesStillReachable`'s
lesson is that a rule added at the wrong end of the chain makes its neighbours
unreachable while every test stays green. The dispatch gains a third arm and must
keep its property that **no default branch installs**.

### 9.5 A rung-2 entry is not a Flatpak entry wearing a hat

`registry.d/apt-to-flatpak.json`'s `blender` entry currently declares
`arch: ["amd64"]`, `lane.needs_gpu: true` and `flatpak_id:
org.blender.Blender`. A rung-2 Blender is a **different entry shape**: `arch:
["amd64","arm64"]`, a `flathub` vehicle for amd64 and a `debian-closure` vehicle
for arm64 — which the current schema cannot express, because a recipe declares
*one* vehicle. **Whether a single entry may mix vehicles per architecture is the
first design question the `appnet` owner has to answer**, and it is not mine to
settle. The alternative — a whole `debian-closure` entry for both arches — is
simpler and gives up Flathub's amd64 build.

---

## 10. What is NOT proven

- **No frozen manifest has been written by `freeze-debian-closure.sh` end to
  end.** `--self-test` passes and every step it performs was executed by hand in
  §4.4, but the tool itself has not been run to completion against a real
  package. **The staged registry fragment is therefore hand-assembled from
  measured values, not tool-emitted**, which is a weaker provenance than the
  standard §8 of INSTALL-METHODOLOGY sets, and it is why the fragment is marked
  `_disabled`.
- **Only 5 of 350 packages were cross-checked against `snapshot.debian.org`.**
  The five agree byte-for-byte (§4.5). The other 345 are pinned to
  `deb.debian.org` URLs in the fragment, which is a **moving target** — this is
  the single largest piece of unfinished work and `CLOSURE-02` exists to refuse
  exactly that shape.
- **Nothing was run on a real arm64 Linux box.** Every measurement is from an
  arm64 container on an Apple-Silicon Mac. That is the right architecture but
  not the right machine.
- **The App Hub copy in §1 is not implemented.** No UI change was made.
- **Hardware GL acceleration under box64 is unproven** — §5.3 proves which
  stack was bound, on a machine with no GPU.
- **Whether a foreign-arch Flatpak runs** — §6, `untestable-on-arm64-mac`.
- **Cura 5.0.0 in Debian is well behind upstream.** Rung 2 trades currency for
  coverage and a rung-2 entry must state its version gap.

---

## 11. What was NOT reached

Ordered by how much it would cost someone to pick up.

- **`freeze-debian-closure.sh` has never produced a manifest.** Two full runs
  were started against `snapshot.debian.org`; the first was sequential and
  abandoned as unusable, the second was parallelised and stopped mid-run when
  the work was parked. `--self-test` passes (14 fixtures, 13 mutations killed),
  `--resolve-only` is verified against a real package, and every step it
  performs was executed by hand in §4.4 — **but the tool end to end is
  unexercised.** Its `FETCH_SECONDS` figure, which is the only thing that would
  settle whether snapshot.debian.org can serve a 350-file closure at an
  acceptable rate, **was never obtained.** That is the single most important
  missing number in this document.
- **345 of 350 packages were never fetched from `snapshot.debian.org`.** Five
  were, and all five match (§4.5). The end-to-end proof in §4.4 used
  `deb.debian.org`, which is a **moving target** — so the app that ran is the
  right app, but not from the URLs a real manifest would carry.
- **The amd64 side of every closure is unmeasured.** Only arm64 was resolved.
- **The other five rung-2 candidates were confirmed only by `apt-cache
  policy`** — OBS Studio, HandBrake, Thunderbird, OpenSCAD and Cura have arm64
  candidates, and nothing beyond that was measured. No closure size, no install,
  no launch. Blender's four failed attempts (§4.4) are a warning that
  "the package exists" is a long way from "the app runs".
- **Whether a foreign-arch Flatpak RUNS with binfmt registered** (§6, Q3).
  Blocked three ways on this machine — bwrap namespaces, no visible
  `binfmt_misc`, and Rosetta contaminating any result. **Needs a real arm64
  Linux box.** This is the one open question that directly changes §9.1's
  recommendation, because if it runs badly that is evidence, and if it does not
  run at all `EmulationCanServe`'s current answer is right for the wrong reason.
- **box64 was never run against a GPU.** §5.3 proves it binds the host's native
  GL stack; on a GPU-less container that stack is `llvmpipe`. **Hardware
  acceleration under box64 is asserted by mechanism, not measured.**
- **No Go was written or run.** §9 is prose. `go build` was not invoked for
  either target, because nothing in `backend/` was touched.
- **No App Hub copy was implemented.** §1's badge table is a specification.
- **`roadmap/app-verification-ledger.json` is untouched.** Blender was launched
  in a container, not through `scripts/verify-app-recipe.sh`, so no ledger row
  was earned and none was written.
