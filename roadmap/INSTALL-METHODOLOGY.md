# The Vulos install methodology — one format, two vehicles

> **Status: code shipped 2026-08-17; catalogue STAGED, not merged.** The
> installer enforces this document today. The migrated entries live in
> `registry.d/` and are inert until the founder's signing ceremony runs. What is
> still red, and why, is §9.
>
> **The ask.** *"i dont like these, have one solid internal methodology for these
> rather thats documented and follows best practices like apt but modern and
> platform agnostic"* — said about a catalogue that installed apps five different
> ways.
>
> **The answer in three sentences.** An app is a **signed registry entry** that
> declares **one install vehicle**: a Flathub app id, or a map of
> **per-architecture artefacts, each with its own SHA-256**, unpacked into that
> app's own directory. There is no install shell, no package manager, no
> dependency solver, and no way for a recipe shape nobody classified to acquire
> an install path. Architecture is resolved **locally on each box** from a
> fleet-identical signed entry, so nothing architecture-specific ever crosses the
> sync wire.

---

## 1. What the five ways were, and why one is enough

Measured on `registry.json`, 56 entries:

| mechanism | entries | what it actually was |
| --- | ---: | --- |
| `apt` | 28 | `sh -c "apt-get install -y …"` |
| `flatpak` | 13 | `flatpak install -y --noninteractive flathub <id>` |
| `download` | 7 | one `download_url` + one `checksum` |
| `command-only` | 5 | raw shell: `wget -qO /tmp/x.tar.gz … && tar …` |
| per-arch `artifacts` | 3 | the format this document standardises on |

Four of the five were **the same code path**. `InstallFromRegistry` dispatched
on Flatpak, then on `download_url`, and everything else fell through to
`sh -c recipe.Install`. So `apt-get install -y blender`, `git clone --depth=1
https://github.com/pkalogiros/AudioMass.git static/`, and this —

```
curl -fsSL …/code-server_4.100.2_amd64.deb -o /tmp/code-server.deb &&
echo '0e8c35b53e…  /tmp/code-server.deb' | sha256sum --status -c - 2>/dev/null || true &&
dpkg -i /tmp/code-server.deb
```

— were indistinguishable to the engine. That last one is the defect the whole
change is organised around: **the digest is fabricated** (it matches nothing
upstream publishes), and it is piped into `|| true`, so `dpkg -i` runs whatever
the network returned. No pattern list catches that. Only not accepting a shell
string does.

The same field produced the BSD-vs-GNU `tar` trap (a `.zip` routed to `tar`
works on a developer Mac and fails on every Debian box), and the "install
succeeded, app cannot start" shape three separate times.

**One format is enough because this is not a distribution.** apt exists to solve
dependency resolution across ~60,000 packages with conflicts, alternatives and
transitive constraints. This is ~120 curated, self-contained applications. Paying
for a solver you never use, while inheriting Debian-specific packaging in a
product that must be platform-agnostic, is the opposite of the requirement. What
120 curated apps with pinned artefacts need is **a manifest and a downloader** —
and both already existed in this tree.

---

## 2. Why apt goes — and one of the two usual reasons is weaker than it looks

### 2.1 Reproducibility. This is the load-bearing reason.

`apt-get install -y blender` installs whatever Debian ships **that day**. Two
instances that install a month apart get two different Blenders, and neither the
registry entry nor the sync wire records which. Against the standing directive —
*"EVERYTHING MUST SYNC, EACH INSTANCE IS ALMOST A DIRECT CLONE OF NEXT WITH FEW
EXCEPTIONS"* — "near-identical instances" and "unpinned package manager" are
simply incompatible requirements.

| vehicle | what it pins | how tight |
| --- | --- | --- |
| per-arch `artifacts` | an exact URL and an exact SHA-256, per architecture | exact bytes |
| Flatpak | `(app id, branch, upstream version)`; Flathub resolves a commit | exact build, per arch |
| apt | nothing | whatever the mirror has today |

### 2.2 Persistence. Real, but apt is **not** the variable — and saying so matters.

`roadmap/APT-INSTALL-PERSISTENCE.md` measured this by executing the initramfs
hook against every kernel command line this repo writes. Its finding, stated
there in bold:

> **apt is not the variable.** On the three overlay boot paths (live-USB, live
> ESP re-flash, netboot-installed) flatpak installs, static-download installs
> and apt installs are **all equally volatile** — the writable layer is a tmpfs
> and `$HOME/.vulos/apps` is on the same filesystem. On the two `--disk` paths
> all three equally persist.

So "an apt install survives a reboot on `--disk` and on nothing else" is true,
and it is **equally true of every other vehicle**. Migrating away from apt does
not fix the netboot persistence defect; that defect is real, separate, and
unfixed. Writing this down rather than letting the persistence argument carry
weight it cannot bear is the point: a decision defended by a reason that does
not hold is a decision that gets reversed by the first person who checks.

There **is** one apt-specific cost, and it is about RAM rather than persistence.
The writable tmpfs is mounted with no `size=`, so it defaults to half of RAM, and
`build.sh` clears the apt lists before packing. The first registry install
therefore runs `apt-get update`, pulling tens to hundreds of megabytes of Debian
package lists into RAM before the package itself. A pinned artefact downloads
one file.

### 2.3 Platform-agnosticism.

`apt-get` is Debian. Flatpak app ids are architecture-independent and
distribution-independent. A per-arch artefact map is a URL and a digest, which
every platform understands. The founder asked for platform-agnostic; a Debian
package manager in the install path is the one thing that cannot be.

---

## 3. Why Flatpak stays

It is the one third-party vehicle that already satisfies the requirements:

- **It pins.** A Flathub ref resolves to a commit; the same `(id, branch,
  version)` yields the same build.
- **It installs outside the sealed base**, into `/var/lib/flatpak`, not into the
  dm-verity-sealed system tree that apt writes to.
- **Its app ids are architecture-independent** — `org.gimp.GIMP` is one string
  on both arches, and Flatpak resolves the per-arch ref locally.
- **It is verified working end to end.** `flatpak install -y --noninteractive
  flathub org.ardour.Ardour` exited 0 on arm64 in a `debian:trixie-slim`
  container, deployed a non-empty tree, and exported its own icon
  (`FLATPAK-MIGRATION.md`, "Proof of one conversion").

### The nuance that governs the schema: **you cannot pin a Flatpak commit across arches**

The architecture is *inside the ref* — `app/org.gimp.GIMP/x86_64/stable` — so the
commit for x86_64 and the commit for aarch64 are different values. Putting a
Flatpak commit in a registry entry would therefore either be wrong on one
architecture, or would make the entry arch-specific, which the sync rule forbids
(§5). **Flatpak is pinned by `(id, branch, upstream version)` and nothing
finer.** Only the Vulos-native vehicle can pin per-arch checksums, because those
live one level deeper, inside the signed entry.

### What Flatpak does NOT give you, stated plainly

`FLATPAK-MIGRATION.md` measured that **nothing maps a recipe's `permissions`
onto Flatpak** — no `flatpak override`, no `--unshare=network`, nothing. Several
Flathub manifests grant *more* than the Vulos recipe declares: GnuCash, Octave
and LibreOffice all get `--filesystem=host` **and network** while their recipes
say `["filesystem"]`. That is a real gap and it is not closed by this document.

---

## 4. The format

### 4.1 The entry

```jsonc
{
  "name": "Navidrome",
  "type": "web",
  "arch": ["amd64", "arm64"],     // EXPLICIT. never null. see §5.
  "source": "vulos-native",       // or "flathub"
  "verified": true,
  "verified_by": "sha256-pin",    // flathub | debian | vendor-gpg | sha256-pin | none
  "proprietary": false,
  "vetted": true,
  "_note": "why this vehicle, in one defensible line",
  "signature": "",                // Ed25519, minted only by the human ceremony
  "versions": { "0.54.5": { /* the recipe */ } }
}
```

### 4.2 Vehicle A — Flathub

```jsonc
{
  "flatpak_id": "org.qgis.qgis//stable",   // //branch when the app has several
  "command": "flatpak run org.qgis.qgis",  // defaults to this if empty
  "permissions": ["filesystem", "network"],
  "artifacts": {}, "install": "", "download_url": "", "checksum": ""
}
```

The `//branch` suffix is not decoration. QGIS publishes two branches and the
bare id fails with `Similar refs found` at the first click, while Flathub's own
API reports a single `"branch": "stable"` — so an entry built from the API looks
correct and does not install. Measured, not reasoned about.

### 4.3 Vehicle B — Vulos-native per-architecture artefacts

```jsonc
{
  "artifacts": {
    "amd64": {
      "download_url": "https://github.com/navidrome/navidrome/releases/download/v0.54.5/navidrome_0.54.5_linux_amd64.tar.gz",
      "checksum": "73c1a42958dc2c96fa9787fb060e36f664bb0d9f58f66c07b3b3ba12be4a3ca1"
    },
    "arm64": {
      "download_url": "https://github.com/navidrome/navidrome/releases/download/v0.54.5/navidrome_0.54.5_linux_arm64.tar.gz",
      "checksum": "1e5372efbdc36ee907f9bb122026f5858eb322eea165dc1c2d37e814160ab455"
    }
  },
  "archive_strip": 0,          // tar --strip-components, applied to zips identically
  "extract_dir": "bin",        // subdirectory of the app dir an ARCHIVE unpacks into
  "binary_name": "navidrome",  // stable installed name for a SINGLE BINARY
  "command": "bin/navidrome --configfile data/navidrome.toml",
  "port": 4533,
  "post_install": "…",         // configure only — see §4.5
  "deps": null,
  "permissions": ["network", "filesystem"],
  "singleton": true, "auto_start": false,
  "install": "", "download_url": "", "checksum": ""
}
```

**The URL and its digest live in the same object.** The alternative a curator
reaches for first — `download_url` as either a string or an `{arch: url}` map,
with `checksum` staying a scalar — can express *two different binaries pinned to
one hash*. A schema should not be able to say the wrong thing.

**Resolution is strict and never falls back.** A box whose architecture is not
in the map gets a loud refusal naming what the recipe does offer. Falling back
would hand an arm64 box an amd64 binary that passes its checksum and then fails
to `exec` — a successful install of an app that cannot run, which is the entire
defect class being removed. Both sides go through `NormalizeArch`, the single
normalisation point, so a registry saying `x86_64` still matches a box saying
`amd64`.

**An architecture-independent payload uses the exclusive `"any"` key** — a
static web bundle, a WAR, a source archive:

```jsonc
"artifacts": { "any": { "download_url": "…/cinny-v4.12.1.tar.gz", "checksum": "b67f8a59…" } }
```

The first draft of this standard did the obvious thing instead: list the same
URL and digest under both `amd64` and `arm64`. **An independent checker refused
it, and it was right.** That encoding is indistinguishable, from the data alone,
from the real defect — a curator pinning the *amd64* asset under both keys, so
an arm64 box downloads an x86-64 binary, passes its checksum, installs cleanly
and cannot `exec`. That is not hypothetical: kerf v0.1.9 published three
"per-platform" tarballs that were one identical file, and the entry looked
plausible while being unusable.

So the recipe **states which it means** rather than a checker guessing. `any` is
a claim by the curator that the payload carries no machine code, and
**ARTIFACTS-02 makes it exclusive**: combining it with a real architecture key is
refused, because that would make one of the two a silent fallback — and a
fallback is how an arm64 box ends up with an amd64 binary in the first place.
`scripts/verify-firstparty-artifacts.sh`'s "two architectures cannot share one
sha256" rule is **unchanged in strictness** for every recipe that really claims
two architectures.

### 4.4 The two path fields, and why each is refused rather than ignored

| field | applies to | what it does |
| --- | --- | --- |
| `binary_name` | a single downloaded binary | installs it as `bin/<name>` so `command` — one string for every arch — can name it. Without it, per-arch assets land as `bin/diwan-linux-amd64` on one box and `bin/diwan-linux-arm64` on another |
| `extract_dir` | an archive | the subdirectory of the app dir the archive unpacks into. Empty means the app dir itself |

Setting `binary_name` on an archive, or `extract_dir` on a single binary, is
**refused**, not ignored. The precedent is per-recipe `arch`, which three entries
carried for months while the struct had no field to receive it: it landed in the
passthrough map, was read by nothing, and looked like protection.

`extract_dir` is not sugar. A static web app is served with
`python3 -m http.server ${PORT} --directory static/`, and every such recipe used
to get its bytes there by running `unzip -d static/` in the install shell.
Unpacking into the app dir and serving `.` instead is **not** an equivalent
substitute: the app dir also holds `data/`, which is a symlink to the owner's
data directory, so that version publishes the app's own database over HTTP.

`extract_dir` is screened **before anything is downloaded**. A box that fetches
the payload and only then notices the path is bad has already done the
attacker's transfer for them.

### 4.5 `post_install` — what it is, and what it is no longer

The install shell is gone. `post_install` survives, **narrowed**, and this
document does not pretend otherwise, because pretending is how the same
`wget … && tar …` line moves one field down and the claim becomes false.

It survives because three verified first-party entries — diwan, wede, lilmail —
mint their configuration and their secrets there, and those installs are proved
to work. Removing it would break the only recipes in the catalogue with real
end-to-end evidence behind them.

It is narrowed to **arranging bytes that are already on disk**:

- **POSTINSTALL-01** — a failed `post_install` is **fatal** and the half-built
  app directory is removed. (It used to log a warning; that shipped an app whose
  every launch died with "Failed to load config" while the installer reported
  success.)
- **POSTINSTALL-02** — a `post_install` that reaches the network or a package
  manager is **refused**: `curl`, `wget`, `git clone`, `apt`, `apt-get`, `dpkg`,
  `pip`, `npm`, `yarn`, `gem`, `go install`, `cargo install`, `nix-env`. The
  moment it can fetch, the signed per-artefact digest is decorative — the entry
  says sha256 X and the box runs whatever the network returned. `uptime-kuma`'s
  `post_install` is literally `npm install --production 2>/dev/null || true`.
- **POSTINSTALL-03** — `|| true` and `|| :` are **refused**. They re-open
  POSTINSTALL-01 from inside the string: the command fails, `sh` exits 0, the
  installer reports success, the app is unconfigured. A real fallback that can
  still fail (`test -d data || mkdir data`) is fine; the rule is about
  discarding a failure, not about `||`.

A `${PORT}` reference is **not** expanded in `post_install` — only in `command`,
at launch. Because `appPort` is taken verbatim from the recipe's `port` and is
not pool-allocated, a config file written here must use a **literal** port equal
to that field. Six shipped entries got this wrong.

### 4.6 What is deliberately NOT in the format

- **No dependency solver.** 120 curated self-contained apps do not need one, and
  building one is how a manifest becomes a distribution.
- **No optional integrity check anywhere.** Every artefact in every vehicle
  carries a mandatory SHA-256, including the unsigned external-catalog path
  (§6.0, CATALOG-01).
- **No install shell.** Not narrowed, not allow-listed — absent.
- **No package manager**, in any field.
- **No `latest` URLs.** A moving URL with a fixed checksum breaks at the next
  upstream release; a moving URL without one will not install at all.
- **No mirror.** Vulos curates *which* Flathub apps appear and runs no repository
  of its own. Some Flathub packages are `extra-data` — a thin manifest that
  fetches the vendor's real binary at install time — so a mirror would carry no
  payload for them anyway.
- **No arch-specific state on the sync wire.** §5.

**One honest exception, named rather than buried:** `deps` still calls
`packages.InstallDeps`, which is `apt-get install -y`. **It is the last apt call
in the install path and it is still open.** What changed on 2026-08-17 is that
the three things this paragraph asserted were measured, and two of them were
wrong:

- **"Exactly one shipped entry uses it" was wrong.** Four do: `conduit`
  (`liburing2`, `ca-certificates`), `diwan` and `lilmail` (`ca-certificates`),
  and `wede` (`ca-certificates`, `git`). Three of the four are the *verified
  first-party* entries. `ca-certificates` is in `scripts/image-packages.txt`;
  `liburing2` and `git` are **not**.
- **"Dropping `deps` from conduit would make a working app stop working" is
  true, and now it is measured rather than assumed.** The DT_NEEDED list of
  conduwuit v0.5.9 was read out of both published binaries:

  ```
  conduwuit-linux-amd64  (83751128 bytes)  liburing.so.2, libstdc++.so.6, libgcc_s.so.1, libm.so.6, libc.so.6, ld-linux-x86-64.so.2
  conduwuit-linux-arm64  (74653640 bytes)  liburing.so.2, libstdc++.so.6, libgcc_s.so.1, libm.so.6, libc.so.6
  ```

  Both architectures need `liburing.so.2` at load time, and the release
  publishes no static or musl variant — its four assets are
  `conduwuit-linux-{amd64,arm64}` and two `-maxperf` builds of the same shape.
  So the dependency is real; there is no artefact swap that removes it.

The right model is unchanged: `deps` names packages the **base image must
already carry**, verified at install rather than installed. Reaching it needs
`liburing2` (and `git`, for wede) added to `scripts/image-packages.txt` — an
image change, not an installer change — and only then can the call site go.

Two defects in the current path are named here so they are not rediscovered:

- **DEPS-01 — the return value is discarded.** `packages.InstallDeps(ctx,
  recipe.Deps)` is called and its error is dropped, so a dependency that could
  not be installed produces a **successful install of an app that cannot exec**.
  That is POSTINSTALL-01's defect one field down, and it was left alone
  deliberately: making it fatal changes the install outcome for three verified
  first-party entries on a box this session could not run, and a change that
  turns working installs into refusals needs the container run this session did
  not have.
- **DEPS-02 — the box very likely cannot satisfy it anyway.** `build.sh` clears
  the apt lists, nothing in the install path runs `apt-get update`, and
  `liburing2` is not in the image, so `apt-get install -y liburing2` on a fresh
  box is expected to fail with "Unable to locate package" — and DEPS-01 then
  reports success. conduit is therefore probably already broken on a fresh box,
  silently. **Expected, not measured**: it needs a boot, and it is written down
  as the first thing to check rather than as a finding.

---

## 5. Architecture: resolved locally, never synced

**The rule.** Nothing architecture-specific may cross the sync wire. The
replicated row is:

```
app_desired (app_id, desired_version, desired, actor_ulid, updated_at)
```

No arch. No URL. No checksum. No Flatpak commit. A heterogeneous fleet — an
amd64 box and an arm64 box owned by the same person — must converge on *the same
app set*, and each box resolves its own bytes from **the same signed entry**.

That is why the per-arch map lives inside the entry rather than being chosen by
whichever box happened to install first, and why §3's Flatpak-commit nuance
matters: a commit is per-arch, so pinning one would put an arch into fleet
state.

**Three spellings are in play and mixing them silently matches nothing:**

| | 64-bit x86 | 64-bit ARM |
| --- | --- | --- |
| Debian / dpkg — **what the registry and the API speak** | `amd64` | `arm64` |
| Flatpak / `uname -m` | `x86_64` | `aarch64` |
| Go `runtime.GOARCH` | `amd64` | `arm64` |

`NormalizeArch` (`backend/services/appnet/arch.go`) is the only place a foreign
spelling becomes a Vulos one, on **both** sides of every comparison.

**The architecture that governs is the box's, not the browser's.** Desktop apps
are streamed *from* the box, so a user on an arm64 Mac driving an amd64 box must
be offered amd64 apps. Nothing reads a request header.

**An unset `arch` means nobody checked** — not "works everywhere". It is
currently permitted, ratcheted so the count may fall and never rise, and becomes
a refusal when the ratchet reaches zero.

---

## 6. Enforcement — where each rule lives

Install goes through **one** function: `appnet.InstallFromRegistry`
(`backend/services/appnet/registry.go`), which the box reaches at
`POST /api/store/registry/install`.

In order:

1. **Publisher signature.** Baked trust anchor → root-signed release cert →
   release key; Ed25519 over `signing.Canonical({"app_id", "entry"})`. Fail
   closed. `VULOS_REGISTRY_INSECURE=1` is dev-only and refused when
   `VULOS_ENV=prod` — which is also the default when `VULOS_ENV` is unset.
2. **`_disabled`** on entry or recipe → refused. **`min_version`** downgrade
   floor enforced.
3. **Architecture** — `ArchSupported(entry.Arch, SupportedArches())`, refused
   with a sentence the UI can show verbatim.
4. **`validateRecipeSecurity`**:

| id | rule |
| --- | --- |
| SEC-F/G | pipe-to-shell (`curl\|bash`, `wget\|bash`, `sh -c "…\|…"`) refused |
| SEC-H3 | an install string that downloads a binary needs a checksum |
| SECAUDIT2-H1 | a `download_url` with no checksum refused; **per artefact** in the map |
| **INSTALL-01** | a non-empty `install` refused — there is no shell install path |
| POSTINSTALL-02 | a `post_install` that fetches refused |
| POSTINSTALL-03 | a `post_install` that swallows failure refused |
| ARTIFACTS-01 | both forms set; a top-level checksum beside `artifacts`; a null artefact; an artefact with no URL or no checksum; one arch spelled two ways; `binary_name` on an archive or containing a path |
| **DOWNLOAD-01** | a non-empty `download_url` refused — use `artifacts` |
| **ARTIFACTS-02** | `any` combined with a real architecture key — `any` is exclusive, never a fallback |
| **EXTRACT-01** | `extract_dir` absolute, traversing, or on a non-archive |
| **INSTALL-02** | neither `flatpak_id` nor `artifacts` — an unclassified recipe gets no install path |
| **ARCH-02** | an architecture name (`amd64`, `arm64`, `x86_64`, `aarch64`, `armhf`, …) in `command`, `post_install`, `binary_name`, `extract_dir` or `env` — one string, every architecture. Checked LAST, so every older rule still answers for the input it was written for; the artefact URLs are deliberately exempt, because the `artifacts` key is the one correct place for an architecture |

### 6.0 The sixth way in, which the count of five missed

`POST /api/store/install` does **not** reach `InstallFromRegistry`. It reaches
`AppStore.Install`, the external-catalog path (`VULOS_APP_CATALOG`, unset by
default). It takes a `download_url` straight out of a request body, carries **no
publisher signature**, and verified its archive only `if entry.Checksum != ""` —
so omitting the field **skipped verification** rather than failing it. The path
with no signature was the one place a digest was optional.

**CATALOG-01** now refuses an empty checksum there, ordered after the SSRF guard
so that guard keeps answering for the inputs it was written for. This does not
make the path equivalent to the registry one — without a signature a checksum
only proves the bytes match what the *request* asked for — and no shipped caller
uses it: the App Hub calls `POST /api/store/registry/install`.


5. **The dispatch**, which has **no default branch that installs**:

```go
switch {
case recipe.FlatpakID != "":  FlatpakInstall(...)
case recipe.HasArtifacts():   staticInstall(...)
default:                      return error   // INSTALL-02
}
```

Gate and dispatch are **two independent closures of the same hole**. Deleting
either one still leaves the catalogue shut, which is the point: a single
mutation cannot re-open a shell.

6. **PAYLOAD-01 — the payload must be the KIND of thing the URL claims.**
   `staticInstall` classifies a download by URL *extension*, and everything it
   does not recognise falls through to "must be a single binary": install at
   `bin/<name>`, `chmod 0755`, report success. That branch is how drawio's 52 MB
   `draw.war` became `bin/draw.war` while `static/` stayed empty. Teaching the
   zip list about `.war` fixed drawio and left the shape — `.tar.zst`, `.7z`,
   `.tar`, `.deb`, `.jar` and whatever an upstream invents next all still land
   there. So the installer now reads the **bytes**: if the payload carries an
   archive container's magic and is about to be installed as an executable, the
   install fails naming both facts. It runs after the checksum (so it classifies
   bytes the signed entry pinned) and before the file is placed (so a refusal
   leaves no 0755 archive behind). tar's magic sits 257 bytes in, so it reads
   512 rather than a prefix. A real binary — ELF, a script — matches nothing and
   is unaffected; `TestRealBinaryStillInstalls` is the control that says so.

7. **STATE-01 — the state directory is handed to the uid the app runs as.**
   The installer creates the app directory as root, `Launcher` execs the app as
   `setpriv --reuid=65534 --regid=65534`, and a 0755 root-owned `data/` is an
   app that installs cleanly and dies on its first write. Seven migrated recipes
   carried `chown -R 65534:65534 data` in `post_install` instead — a stopgap
   repeated seven times, absent from the eighth, and a **privileged** command in
   a string that also runs on a developer box, where it fails and POSTINSTALL-01
   turns that failure into a failed install. The model is stated once now:

   | path | owner | why |
   | --- | --- | --- |
   | `bin/`, `static/` | root, 0755 | **code**. An app that can rewrite its own binary keeps any compromise across a restart |
   | `data/` (and the symlink's target) | 65534 | **state**. The one directory the app writes |

   It runs **last**, so it covers what `post_install` wrote, and it follows the
   `data/` symlink, because chowning a symlink changes nothing on disk. Not
   being root is a logged skip — an unprivileged installer cannot `setpriv`
   either, so owner and app are already the same uid. Any other failure fails
   the install.

### 6.1 Ordering is load-bearing

`DOWNLOAD-01` runs **last** of the download rules. Putting it first — the
obvious place — made three shipped guards unreachable: SECAUDIT2-H1's missing
checksum, ARTIFACTS-01's both-forms ambiguity, and the binary_name-on-an-archive
refusal. Every one of their tests would still have passed, because each only
asserts "an error happened". That is the *guard that can never fire* defect
wearing a tightening as a disguise, and
`TestDownloadURLRulesStillReachable` now pins the order by asserting **which**
rule answers.

### 6.2 What the signing gate is, and what must never be done to it

`registry.json` is Ed25519 publisher-signed **per entry**. The signature covers
`{"app_id", "entry-with-signature-removed"}` canonically, so an entry cannot be
re-keyed under a different app id, and **every** key is signed — including keys
the Go structs do not model, which round-trip through the `Extra` passthrough.
Adding a field therefore invalidates the signature: **re-sign, never patch.**

`registry.json` has **one writer**. Proposed entries are staged as
`registry.d/<name>.json`, which is unsigned by construction and verified by
nothing. Every staged entry carries `"signature": ""`, and
`VerifyEntrySignature` **fails closed on an empty signature**, so staged entries
are inert: they cannot be installed by accident, and a merge that skips signing
fails loudly instead of shipping unvetted trust material.

**In this trust model the signature IS the act of vetting.** An agent must not
mint it. Do not set `VULOS_REGISTRY_INSECURE`. Do not set
`VULOS_SIGN_ALLOW_KEY_MISMATCH`. Do not weaken `check-release-key` — the
`keys/release.priv.json` in this tree is a leftover **dev** key whose public half
is not what the shipped certificate authorises, and `make sign-registry`
refusing it is that gate working.

---

## 7. The migration, and its cost

43 non-Flatpak entries; the 3 first-party ones were already in the target format.
**40 to move.**

### 7.1 Converted to Flathub — 11

`ardour`, `darktable`, `gnucash`, `libreoffice`, `lmms`, `lutris`, `obs-studio`,
`octave`, `qgis`, `shotcut` (staged in `registry.d/apt-to-flatpak.json`,
architectures measured twice — Flathub's API **and** a real `flatpak remote-info`
in a container), plus `blender`.

**Six of them gain arm64 for free**: ardour, darktable, gnucash, lmms, octave and
shotcut all publish aarch64 builds, and all six shipped as `arch: ["amd64"]`.

**Blender is a loss and it is the largest one here.** The earlier call — stay on
apt, because `org.blender.Blender` is x86_64-only on Flathub while Debian trixie
ships blender 4.3.2 for both — was correct *while apt was a vehicle*. It is not
any more, so the choice is no longer Flathub-versus-Debian, it is
**Flathub-versus-nothing**. blender.org publishes no official Linux aarch64 build
either, so no `artifacts` entry can recover the coverage: there is nothing to
pin. **arm64 boxes lose Blender**, and will see "requires amd64; this box is
arm64" rather than a failing install. Reversing this means keeping an unpinned
apt path for one app; it is the founder's call.

### 7.2 Re-expressed in the Vulos-native format — 15

Staged in `registry.d/vulos-native.json`. **Six gain arm64** — see the
correction below the table; the first count of this was wrong.

| app | version | vehicle before | now |
| --- | --- | --- | --- |
| conduit | 0.5.9 | single `download_url` | per-arch binary, `binary_name` |
| navidrome | 0.54.5 | single `download_url` | per-arch tar.gz → `bin/` |
| gitea | 1.22.0 | raw shell (curl + inline sha + symlink into `/usr/local/bin`) | per-arch binary |
| syncthing | 2.1.3 | vendor apt repo, key unpinned | per-arch tar.gz → `bin/` |
| grafana | 11.6.16 | vendor apt repo, key unpinned | per-arch tar.gz |
| memos | 0.22.4 | broken `download_url`, empty checksum | per-arch tar.gz → `bin/` |
| code-server | 4.100.2 | apt + **fabricated checksum** + `\|\| true` | per-arch tar.gz |
| cinny | 4.12.1 | raw shell (wget + inline sha + tar) | one artefact, both arches |
| element-call | 0.21.0 | raw shell | one artefact, both arches |
| drawio | 30.0.1 | `download_url` to a `.war` (**broken**) | one artefact, `extract_dir` |
| minipaint | 4.10.0 | `git clone --branch v4.10.0` | tag archive, checksum-pinned |
| audiomass | 2026-05-25 | `git clone` of the **default branch** | **commit**-pinned archive |
| jellyfin | 10.11.11 | vendor apt repo, GPG key fetched unpinned | per-arch tar.gz → `bin/`, **stays `_disabled`** |
| minio | RELEASE.2025-09-07T16-13-09Z | `wget` of an **unversioned** URL into `/usr/local/bin` | per-arch binary, `binary_name`, **stays `_disabled`** |
| svg-edit | 7.4.2 | shell `wget` of a release asset **that does not exist** | one `any` artefact → `static/`, **stays `_disabled`** |

**Correction, because the first count of this was too generous.** Only six of
the fifteen actually gain an architecture: `conduit` (shipped `arch: ["amd64"]`),
`navidrome`, `gitea`, `memos`, `code-server` (all amd64-only artefacts) and
`minio` (an amd64-only URL). `syncthing`, `grafana` and `jellyfin` came from
**vendor apt repositories that already publish arm64**, so their gain is
pinning, not coverage — the earlier commit message on this work claims nine and
is wrong. The remaining six (`cinny`, `element-call`, `drawio`, `minipaint`,
`audiomass`, `svg-edit`) are static bundles that were always
architecture-independent.

**Three of the fifteen ship `_disabled` on purpose**, and a fourth pair already
did. `code-server` and `memos` were disabled for trust failures; `jellyfin`,
`minio` and `svg-edit` are newly pinned but nobody has run their installs, and
`jellyfin` has a named functional gap (§7.3). Pinning an artefact is not the same
claim as "this app works", and an entry that conflates them is how the catalogue
got here.

`code-server` and `memos` stay `_disabled`. Both were disabled for trust
failures; re-enabling them on the strength of an install nobody has run is the
same mistake facing the other way. Enable after
`scripts/verify-app-recipe.sh <id>` passes.

### 7.3 Parked — 7, plus wine, with a per-app reason

`registry.d/apt-retired.json` sets `_disabled: true` on `cockpit`, `httpbin`,
`jupyter`, `libretranslate`, `nginx`, `steam`, `transmission`;
`wine` is parked in `apt-to-flatpak.json`. **`jellyfin` left this list on
2026-08-17** — see below.

- **cockpit, nginx, transmission** — distribution components. Upstream publishes
  source only; there is nothing self-contained to pin. If Vulos wants a
  Cockpit-like panel it belongs in the base image.
- **httpbin, jupyter, libretranslate** — `pip install`. pip is a package manager
  with the *same* unpinned-resolution problem as apt, so swapping one for the
  other trades nothing. **jupyter is the loss most likely to be felt**, and it is
  the strongest argument for a third vehicle later (a pinned wheel set, or a
  Flathub entry). It is deliberately not invented here.
- **jellyfin — MEASURED AND MIGRATED (still `_disabled`).** Both open questions
  were answered on 2026-08-17 and they answer differently.
  **Self-containment: yes.** The tarball carries `libcoreclr.so`,
  `libhostfxr.so`, `libhostpolicy.so`, `libclrjit.so` and the whole `System.*.dll`
  set, so the .NET runtime is *inside the payload* and the image does not need
  one; `jellyfin-web` ships in the same tarball (2331 members), so the web client
  is not a second package either. The two apphosts are genuinely different builds
  — `ELF 64-bit LSB pie executable, x86-64` and `… ARM aarch64` — so this is not
  the kerf shape. **ffmpeg: no.** Zero archive members match `ffmpeg` or
  `ffprobe`, and neither is in `scripts/image-packages.txt`. A Jellyfin that
  cannot probe or transcode is an app that installs and cannot do the thing it is
  for, so the recipe is pinned with real digests and **stays `_disabled`**: one
  edit from enabling once the image carries ffmpeg and
  `scripts/verify-app-recipe.sh jellyfin` passes.
  **A third defect surfaced while measuring**: the retired recipe ran
  `jellyfin --port ${PORT}`, and jellyfin has **no `--port` option** — the option
  names compiled into `jellyfin.dll` are exactly `cachedir`, `configdir`,
  `datadir`, `ffmpeg`, `logdir`, `nowebclient`, `published-server-url`, `service`,
  `webdir`. The port lives in `network.xml`, which the new `post_install` writes
  with the literal 8096 the recipe declares; the element names
  (`InternalHttpPort`, `PublicHttpPort`) were read out of the shipped
  `MediaBrowser.Common.dll`, not guessed.
  **Still unmeasured and named rather than assumed**: whether the image carries
  ICU. A self-contained .NET app needs `libicuuc`/`libicui18n` unless it was built
  invariant; `libicu` is not in `image-packages.txt` and chromium is *expected*
  to pull it in transitively, which is an inference.
- **steam** — already excluded by `APP-CATALOG.md` policy 1a (proprietary,
  founder call, explicitly reversible), amd64-only everywhere, and its Flathub
  package is extra-data.
- **wine** — no portable per-arch artefact exists (WineHQ ships `.deb` only), and
  `org.winehq.Wine` publishes **seven** branches with none named plain `stable`,
  so the bare id fails. The `//branch` suffix does resolve — proved on QGIS — but
  every candidate is a dated branch that ages out, and **which one currently
  resolves is a `flatpak remote-info` measurement that was not run**. Guessing it
  is exactly the fabricated-value failure this work exists to remove.

### 7.4 Already `_disabled`, left alone — 6

`diagrams-net`, `excalidraw`, `hoppscotch`, `immich`, `uptime-kuma`,
`vaultwarden` are refused today and stay refused; each needs a build step, a
service topology, or an artefact that does not exist. (The heading previously
said 9 while listing 8 names; it was miscounted, and `minio` and `svg-edit` have
since moved to §7.2.)

- **svg-edit — DEFECT CONFIRMED AND FIXED (still `_disabled`).** It pinned
  release `v7.3.0`, which **does not exist**: the tags are `v.7.3.3`, `v7.2.0`,
  `v.7.1.2-beta.1`, `v5.1.0` and older, and measured against the GitHub releases
  API **ten releases carry zero assets between them**. `_disabled` is the only
  reason nobody hit that 404. The one *published build* of SVG-Edit is its npm
  package, which is immutable per version and ships `dist/editor/` prebuilt
  (`index.html`, `Editor.js`, `svgedit.css`, `components/`, `extensions/`,
  `images/`), so no build step is needed. It is now pinned to `svgedit-7.4.2.tgz`,
  and this is one of only two entries in the catalogue with an independent second
  opinion on its bytes: our sha512 and sha1 both match npm's published
  `dist.integrity` and `dist.shasum`. `command` serves `static/dist/editor/`
  specifically — serving the app dir would publish `data/`, a symlink to the
  owner's data directory (§4.4).
- **minio — the 404 was real, and there was a pinnable artefact one release
  back.** `dl.min.io/…/archive/minio.RELEASE.2025-10-15T17-29-55Z` 404s because
  that release **publishes no binary at all** (its GitHub asset list is empty).
  `RELEASE.2025-09-07T16-13-09Z` publishes real per-architecture Linux binaries as
  GitHub release assets — a pinned immutable URL, unlike the unversioned
  `…/release/linux-amd64/minio` the old recipe used, which the format forbids.
  MinIO publishes a `.sha256sum` beside each asset and **both match ours byte for
  byte**, which makes this the other entry with two independent statements per
  artefact. It stays `_disabled`: no install has been run, and this build is from
  the series where the embedded console UI was removed upstream — a product
  question for the founder, not a packaging one.
- **excalidraw**'s `excalidraw-0.18.0.tgz` is an **npm library package**
  (`package/dist/{dev,prod}/…`, no `index.html`), measured by listing the
  downloaded archive. It is not a servable site build, so no `extract_dir` makes
  it work. (svg-edit's npm package is the opposite case, and the difference was
  measured the same way — by listing the archive, not by assuming npm means
  library.)

### 7.5 The honest total

| | count |
| --- | ---: |
| migrated to Flathub | 11 |
| migrated to Vulos-native | 15 |
| **migrated** | **26** |
| parked with a stated reason | 8 |
| already disabled, unchanged | 6 |
| **not migrated** | **14** |

Of the 26, **12 gain an architecture they could not previously install on** (six
via Flathub, six via per-arch artefacts) and **one — blender — loses one.**
Five of the 26 are pinned but ship `_disabled` (`code-server`, `memos`,
`jellyfin`, `minio`, `svg-edit`): a pinned artefact is a claim about bytes, not
about whether the app runs.

Of the 14 not migrated, **none is now blocked by a measurement nobody took.**
The three that were — jellyfin, minio, svg-edit — were measured on 2026-08-17 and
moved to §7.2; what blocks the remaining fourteen is an artefact that does not
exist, a build step, or a service topology.

---

## 8. Every checksum, and every one not computed

**Not one digest below was typed by hand.** They were computed from bytes
downloaded on 2026-08-17 and written into `registry.d/vulos-native.json` **by a
script reading those measurements** — hand-copying twenty-two digests is
precisely how a fabricated one enters a catalogue, and this catalogue already
had one.

### Computed

| app | arch | bytes | sha256 |
| --- | --- | ---: | --- |
| code-server 4.100.2 | amd64 | 103830749 | `dd43f789bd218f56985d771306c19c4424cd75adcf9955a9183e546f81a6ce44` |
| code-server 4.100.2 | arm64 | 103352942 | `13eb5e281c93080a5a0cbb807892e675d98e9ccd5144a9d4dd39533977b94a32` |
| conduit 0.5.9 | amd64 | 83751128 | `4189cd91086b0e46b6ab8b0b3677ccd4abfca6686e66915e1857a963430564de` ✔ |
| conduit 0.5.9 | arm64 | 74653640 | `d325133456241bf64e4dec5dc905fc0513b1e3fb7eaaa927f51726b801a9d3d2` |
| navidrome 0.54.5 | amd64 | 16103878 | `73c1a42958dc2c96fa9787fb060e36f664bb0d9f58f66c07b3b3ba12be4a3ca1` ✔ |
| navidrome 0.54.5 | arm64 | 15381291 | `1e5372efbdc36ee907f9bb122026f5858eb322eea165dc1c2d37e814160ab455` |
| gitea 1.22.0 | amd64 | 142783968 | `a31086f073cb9592d28611394b2de3655db515d961e4fdcf5b549cb40753ef3d` ✔ |
| gitea 1.22.0 | arm64 | 137004152 | `ef6afed370b14d33b2b8dcc0c7ea56105b73ce9a3361090708985a878475d94b` |
| syncthing 2.1.3 | amd64 | 11821325 | `f929eb8e5b72a85543eeeefb2c38f34a68e0c530e70758a2905b78840c76602c` |
| syncthing 2.1.3 | arm64 | 10893415 | `a5c046965b590a8de2f8c8c16a0dbf9201d99600b0cafd604040232b603e4586` |
| grafana 11.6.16 | amd64 | 184481026 | `b4f5e9d773c1400f2282de30ca2177d787fd6c67aba4de0e1c82f65dbab6d86a` |
| grafana 11.6.16 | arm64 | 175071128 | `8a7628c8f838369a4a3d35b6002822faac0fb1fd9762bc4f97fc009345b89bbe` |
| memos 0.22.4 | amd64 | 14686232 | `6d4d8954f561bf7f0551143a77100983e792b5d0d4d8fbeb02783b855ba65ef5` |
| memos 0.22.4 | arm64 | 13827532 | `419fa168b3ca0973cdb4635d8050b9b1bdc825a957f21dfd024d133cf1d11514` |
| cinny 4.12.1 | both | 19710050 | `b67f8a597d607eef7c286fcc7dc6999b4aad093238f4d48298902bc56a0d0dfc` ✔ |
| element-call 0.21.0 | both | 12954393 | `6d0ef79f76e670521ecf38688d29ec70ff439b64b6f969e83c144c8887197f36` ✔ |
| drawio 30.0.1 | both | 52518059 | `b412cb3203e394ad4e0aa8a07288088792ecec96d69c8a3eb7f56ac32f145aaf` ✔ |
| minipaint 4.10.0 | both | 4226993 | `1198efefdd9505a4d866d9ba4c6a7bccaae4fec300af9071fbcd3196ada4e1e9` |
| audiomass @2ac3801a | both | 13972572 | `0e6d9bbab74dc6864ba6925a960aae7650b2cd07002c46e678c0847de808f103` |

**Second run, 2026-08-17** — same rule, same script, no digit typed by hand:

| app | arch | bytes | sha256 |
| --- | --- | ---: | --- |
| jellyfin 10.11.11 | amd64 | 110228765 | `9f7f194a7e37777cfde0d107c088fc47e81c7904440046ac0ceb7a289546cf79` |
| jellyfin 10.11.11 | arm64 | 107416274 | `31f86f51f72f006dfe2d9daabc25e75a9bbbfdad8dc1cbac7d14ea616de1fa40` |
| minio RELEASE.2025-09-07T16-13-09Z | amd64 | 110989496 | `7c5bd8512c6e966455b1d198209358b2d191c77a83ab377c4073281065fb855f` ✚ |
| minio RELEASE.2025-09-07T16-13-09Z | arm64 | 105251000 | `5c83cd2cf151717ba0243f73e1c7802ff36e272b67144bdd7f1f7d684fd6f03d` ✚ |
| svg-edit 7.4.2 | any | 10265698 | `249cc66750a5b4ac39fc79ee8723ab71b581c48946aaea1ee58054fd91db98a7` ✚ |
| conduit 0.5.9 (re-measured) | amd64 | 83751128 | `4189cd91086b0e46b6ab8b0b3677ccd4abfca6686e66915e1857a963430564de` ✔ |
| conduit 0.5.9 (re-measured) | arm64 | 74653640 | `d325133456241bf64e4dec5dc905fc0513b1e3fb7eaaa927f51726b801a9d3d2` ✔ |

**✚ = agrees with a digest UPSTREAM publishes**, which is the second opinion §11
asks for and which the first run had for nothing: MinIO ships a `.sha256sum`
beside each asset (both match), and npm publishes `dist.integrity` and
`dist.shasum` for the exact svg-edit tarball (our sha512 **and** sha1 match both).
The conduit rows are a re-measurement taken to read the binaries' `DT_NEEDED`
lists (§4.6): amd64 reproduces the digest the shipped registry has carried all
along, and arm64 reproduces the one the first run computed — an independent
agreement on both halves of a per-arch pair.

Jellyfin publishes **no digest file at all** next to these assets (the release
directory holds only the `.tar.gz` and the `.tar.xz`), so those two sha256s rest
on one session's arithmetic and the entry says so.

**Independently re-verified.** `scripts/verify-firstparty-artifacts.sh
registry.d/vulos-native.json` re-downloaded all 22 artefacts and recomputed every
digest with a **different implementation** from the one that produced them. All
22 `artifact-digest` assertions report `matches`, with byte counts. Its
`--self-test` (17 synthetic fixtures — two controls that must go green, fifteen
rules that must go red) passes, so the checker is not a gate that prints PASS
while checking nothing.

**✔ = byte-identical to a digest the shipped registry already carried**, computed
independently and earlier by someone else. **Six comparisons were possible and
all six agree.** That is the control on this entire run; without it the other
thirteen would rest on nothing but one session's arithmetic.

The other shipped digests could not be compared and it is worth saying why
rather than counting them as silence: `memos`, `excalidraw`, `hoppscotch` and
`uptime-kuma` carried an **empty** checksum, and `code-server`'s
`0e8c35b53e52b1d5a9e1e6b0a5e8d6a7f3c2d1b0a9e8d7c6b5a4f3e2d1c0b9a8` is not a
counter-example to anything — it named the amd64 `.deb`, which this migration
does not use, and it matches nothing upstream publishes for that file either.

### NOT computed — and therefore not written into any entry

| app | why not |
| --- | --- |
| wine | no artefact exists to hash. |
| blender | no aarch64 artefact exists to hash; converted to Flathub instead. |
| cockpit, nginx, transmission, httpbin, jupyter, libretranslate, immich, uptime-kuma, hoppscotch, excalidraw, vaultwarden, diagrams-net, steam | no single self-contained artefact to hash. |

**Three rows left this table on 2026-08-17**, and it is worth saying what each
turned out to be, because "not computed" covered three different situations:

| app | what it actually was |
| --- | --- |
| jellyfin | a download nobody had run. Both artefacts hashed; the blocker was never the artefact, it was ffmpeg (§7.3). |
| minio | a 404 that was **true**: that release publishes no binary. One release back does, with upstream digests. |
| svg-edit | a pinned release that does not exist. The artefact is the npm package, which no one had looked for. |

### The caveat on the two source archives

`minipaint` pins a **tag** archive and `audiomass` pins a **commit** archive, both
generated on demand by GitHub. A regenerated archive would change the digest.
That fails **closed** — a loud checksum mismatch — which is strictly better than
a `git clone` silently fetching different bytes, which is what both did before.
The commit pin is the stronger of the two: a tag can be moved, a commit sha
cannot.

---

## 9. What is red right now, by name

**Four Go tests fail. All four are the mechanism working, and none may be
"fixed" by weakening it.**

Pre-existing, unrelated to this change — the three first-party entries merged
without signatures:

```
--- FAIL: TestAcceptance_ShippedAnchorVerifiesShippedRegistry
    registry entry "diwan" is UNSIGNED — run `make sign-registry`
    registry entry "lilmail" is UNSIGNED — run `make sign-registry`
    registry entry "wede" is UNSIGNED — run `make sign-registry`
--- FAIL: TestShippedRegistry_HoldsNoUnsignedEntry
    registry.json ships 3 UNSIGNED entry/entries [diwan lilmail wede]
```

Caused by this change, and cleared by the merge, not by an edit:

```
--- FAIL: TestAppStore_ConduitEntry_Enabled
    conduit: recipe fails validateRecipeSecurity: recipe sets `download_url`,
    which this installer no longer runs (DOWNLOAD-01)
--- FAIL: TestAppStore_CommsEntries_ElementCall
    element-call: recipe carries an `install` shell command, which this
    installer no longer runs (INSTALL-01)
```

Both assert that a **shipped `registry.json` entry** satisfies the gate. They are
red because the code enforces the standard and the data has not caught up — which
is the correct order, and the only order that does not involve an agent editing
`registry.json`.

> **CORRECTION, 2026-08-17: merging the fragments will NOT make these two go
> green, and whoever merges needs to know that before they run the ceremony.**
> Both tests assert the *retired vehicle* alongside the gate, and those
> assertions flip from green to red the moment the migrated entries land:
>
> | test | assertion | after the merge |
> | --- | --- | --- |
> | `TestAppStore_ConduitEntry_Enabled` | `recipe.DownloadURL == ""` → error | **red** — the migrated recipe has no `download_url`; DOWNLOAD-01 refuses one |
> | | `recipe.Checksum == ""` → error | **red** — the digest lives per artefact now |
> | | `len(recipe.Checksum) != 64` → error | **red** — same reason |
> | | `validateRecipeSecurity(recipe)` | green — this is the half that clears |
> | `TestAppStore_CommsEntries_ElementCall` | `recipe.Checksum == ""` → error | **red** — `artifacts.any` carries the digest |
> | | `validateRecipeSecurity(recipe)` | green |
>
> **These two tests are part of the defect they were written to protect
> against.** `TestAppStore_ConduitEntry_Enabled` pins the very shape that made
> conduit amd64-only, and its own comment says so out loud: *"a download_url, a
> non-empty sha256 checksum, deps for the binary's runtime libraries"*. They were
> left untouched deliberately — the coordinator owns `registry.json` and these
> assertions belong to the same edit as the merge — but they must be **rewritten
> in that edit**, not deleted: the replacements are `recipe.HasArtifacts()`, one
> 64-character digest **per artefact**, and `recipe.DownloadURL == ""` asserted
> the other way round. Deleting them would remove the only per-entry checks these
> two apps have.

`TestStagedRegistryFragmentsUseOnlyTheTwoVehicles` asserts the same thing about
everything in `registry.d/`, and is **green** at 53 recipes.

### The ceremony that clears them

```sh
cd /Users/pc/code/vulos/vulos
# 1. merge registry.d/{vulos-native,apt-retired,apt-to-flatpak}.json into registry.json
#    (single writer only)
make sign-registry RELEASE_PRIV=<the key only the founder holds>
make verify-registry-prod      # must report N of N verified, 0 skipped
make publish-feed RELEASE_PRIV=<same key>
```

A bad signature is **not** loud on its own: the entry stays listed in the App Hub
and is refused only at install time. "The App Hub looks fine" is not evidence
that signing succeeded. Only `make verify-registry-prod` is.

---

## 10. How the guards were proved

Reading a guard does not clear it. Twenty mutations were planted **one at a
time in the shipping source**, each anchor asserted to match exactly once, each
kill required to produce a real `--- FAIL` marker — a non-zero exit with no
marker is scored an ERROR, because a compile failure looks identical to a caught
mutation. **20 planted, 19 killed, 1 equivalent mutant, 0 unexplained
survivors**, working tree restored after every run.

| # | mutation | killed by |
| --- | --- | --- |
| M1 | INSTALL-01 never fires | `TestShellInstallRefused` |
| M2 | DOWNLOAD-01 never fires | `TestDownloadURLFormRefused` |
| M3 | INSTALL-02 never fires | `TestUnclassifiedRecipeGetsNoInstallPath` |
| M4 | the shell comes back in the dispatch default | `TestInstallFromRegistry_ExecsNoInstallShell` |
| M5 | POSTINSTALL-02 never fires | `TestPostInstallMayNotFetch` |
| M6 | POSTINSTALL-03 never fires | `TestPostInstallMayNotSwallowFailure` |
| M7 | `validateExtractDir` accepts everything | `TestExtractDirRefusals` |
| M8 | `staticInstall` ignores `extract_dir` | `TestStaticInstall_ExtractDirIsHonoured` |
| M9 | `.war` is no longer a zip | `TestWarIsExtractedAsAZip` |
| M10 | `extract_dir` screened only after the download | `TestStaticInstall_ExtractDirRefusesEscapeAtInstallTime` |
| M11 | `rejectNetworkFetch` refuses *every* post_install | `TestPostInstallMayNotFetch` (its control) |
| M12 | `rejectSwallowedFailure` refuses any `\|\|` | `TestPostInstallMayNotSwallowFailure` (its control) |
| M13 | `binary_name` stops seeing `.war` as an archive | `TestWarCountsAsAnArchiveForBinaryName` |
| M14 | the source guard reads a function that does not exist | `TestInstallFromRegistry_ExecsNoInstallShell` |
| M15 | CATALOG-01 never fires | `TestAppStore_Install_RequiresChecksum` |
| M17 | ARTIFACTS-02 never fires | `TestArtifactAny_IsExclusive` |
| M18 | the `any` artefact never resolves | `TestArtifactAny_ResolvesOnEveryArchitecture` |
| M19 | `any` becomes a general per-arch fallback | `TestArtifactPerArch_StillRefusesAMissingArch` |
| M20 | the checker treats *every* recipe as architecture-independent | `verify-firstparty-artifacts.sh --self-test` |

**Second round, 2026-08-17 — fourteen more, planted one at a time in the
shipping source, every one killed, working tree restored and verified clean
after each:**

| # | mutation | killed by |
| --- | --- | --- |
| S1 | the state handoff is a no-op | `TestInstall_HandsTheWholeStateTreeToTheAppUID` |
| S2 | state is handed to `0:0` instead of `65534:65534` | same, on the uid assertion |
| S3 | the top directory only — no recursion | same, on the nested file |
| S4 | the handoff does not follow the `data/` symlink | `TestInstall_HandsOverTheSymlinkTARGET` |
| S5 | blanket chown of the WHOLE app dir | `TestInstall_LeavesTheCodeDirectoriesRootOwned` |
| P1 | PAYLOAD-01 never fires | `TestArchiveWithAnUnknownExtensionIsRefused…` |
| P2 | the sniff reads a 4-byte prefix instead of 512 | `TestUncompressedTarIsRefused` (tar's magic is at 257) |
| P3 | PAYLOAD-01 refuses *every* single-binary install | `TestRealBinaryStillInstalls` (its control) |
| P4 | the sniff runs AFTER the file is placed 0755 | `TestArchiveWithAnUnknownExtensionIsRefused…`, on "nothing was placed" |
| A1 | ARCH-02 never fires | `TestArchInCommandIsRefused` |
| A2 | ARCH-02 refuses *every* recipe | `TestArchInArtifactURLsIsACCEPTED` (its control) |
| A3 | ARCH-02 also reads the artefact URLs | same control |
| A4 | ARCH-02 watches only `command` | `TestArchInEachSharedFieldIsRefused` |
| A5 | ARCH-02 is checked FIRST, shadowing DOWNLOAD-01 | `TestArchRuleDoesNotShadowTheOlderDownloadRules` |

**S5 found a hollow assertion of mine before it found anything else.** The
"code stays root-owned" test compared an unresolved `/var/…` app-dir path
against chowns recorded on the resolved `/private/var/…` path, so it could never
match and the blanket-chown mutant was killed by a *different* test while that
one stayed green. That is the same class as M7 in the first round: a measurement
that looked like a result. The comparison is resolved on both sides now, and S5
is killed by the test that is supposed to kill it.

**P3, A2 and A3 are over-broad on purpose**, for the reason M11/M12 record: a
rule that refuses everything passes every negative test written for it. A3 is the
sharper of the two — extending ARCH-02 to the artefact URLs looks like a
tightening and would refuse *every correct per-arch recipe in the catalogue*,
because the `artifacts` key is the one place an architecture belongs.

M11, M12 and M14 are **over-broad** mutations on purpose. A rule that refuses
everything passes every negative test ever written for it; only a control that
must be ACCEPTED can tell "correct" from "paranoid". M14 does the same for the
source guard: pointed at a function that does not exist, it must `t.Fatal`
rather than pass vacuously.

**Three mutations survived the first round, and every one was a defect in the
assertion, not in the code.** That is the entire reason for doing this:

- **M1** — INSTALL-01 disabled and `TestShellInstallRefused` stayed green,
  because INSTALL-02 was catching the fixtures instead: none of them declared a
  valid vehicle, so the test could not tell "the shell is refused" from "this
  recipe has no vehicle at all". Fixtures now carry real `artifacts` and assert
  *which* rule answered.
- **M7** was not a mutation at all. `if false && A || B || C` still refuses on
  `C`, because `&&` binds tighter than `||`. It scored as a survivor while
  changing nothing — a bad measurement, which is the costliest kind of error this
  project keeps finding.
- **M10** moved the `extract_dir` screen to after the download and stayed green,
  because the containment check still produced the same `EXTRACT-01` message —
  one HTTP fetch too late. An error string describes *what* happened; it cannot
  describe *when*. The test now counts HTTP requests and requires zero.

**One mutation survived and is reported as a survivor rather than dressed up.**
M16 made the store's `verifySHA256` conditional again
(`if entry.Checksum != ""`). It is an **equivalent mutant**: CATALOG-01
guarantees a non-empty checksum before that line is reached, so the guarded and
unguarded forms cannot be told apart from outside. It is recorded because the
equivalence is *load-bearing* — relax CATALOG-01 and M16 stops being equivalent
and becomes the original hole.

**M10 found a real ordering bug in the process.** `extract_dir` was being
screened only after `staticInstall` had already fetched the payload, so a box
refused a path it had already done the attacker's transfer for. The screen now
runs first, before `ResolveArtifact`.

The `arch-correct` assertion added to `scripts/verify-app-recipe.sh` was
exercised against the real `file -b` output measured from today's downloads,
including the crossed cases:

```
aarch64 + "ELF 64-bit LSB pie executable, ARM aarch64, ..."  -> OK
aarch64 + "ELF 64-bit LSB pie executable, x86-64, ..."       -> FAIL
x86_64  + "ELF 64-bit LSB executable, ARM aarch64, ..."      -> FAIL
aarch64 + "HTML document, ASCII text"                        -> INFO
```

---

## 11. What this does NOT prove

- **No install was run.** `scripts/verify-app-recipe.sh` needs docker and ~30
  minutes per app; the host was at load ~145. `roadmap/app-verification-ledger.json`
  is untouched and every migrated app remains **untested** in it. `command`,
  `port` and `post_install` in the staged entries are **unproven**.
- **Archive layouts are measured; app behaviour is not.** `archive_strip`,
  `extract_dir` and `binary_name` were derived by listing each downloaded
  archive. That the app then starts is a separate claim nobody has earned here.
- **The digests pin bytes, not trustworthiness.** `verified_by: "sha256-pin"`
  means exactly what it says: nothing stands behind these artefacts but our own
  checksum. No upstream GPG signature was verified, and where upstream publishes
  its own digest file (syncthing's `sha256sum.txt.asc`, grafana's `.sha256`,
  navidrome's `navidrome_checksums.txt`) it was **not** cross-checked — only the
  bytes were hashed. Doing so would give two independent statements per artefact,
  which is what `PER-ARCH-ARTIFACTS.md` §5 achieved for the three first-party
  apps and is the obvious next improvement.
- **Persistence is unchanged.** §2.2. On the three overlay boot paths nothing
  installed at runtime survives a reboot, and this migration does not alter that.
- **`permissions` still means nothing.** Of the ten valid strings exactly one
  (`storage`) has runtime effect. Converting apps to Flatpak does not break a
  permission bridge; there is no bridge.
- **STATE-01 was proved by observing the chowns, not by performing them.** No
  unprivileged test process can chown to uid 65534, so the tests inject a
  recorder and claim euid 0. What is proved is that the installer *asks* for the
  right ownership on the right paths; that the kernel then grants it on a real
  box is a claim only a box can make. The alternative — skipping the assertion
  when the chown cannot run — is a test that asserts nothing.
- **PAYLOAD-01 knows the containers it knows.** It refuses gzip, zip/war/jar,
  xz, bzip2, zstd, 7z, ar/deb, rar and uncompressed tar. A container format
  outside that list still reaches the single-binary branch — the guard narrows
  the hole to formats nobody has published yet, and does not close it by
  construction the way removing the branch would.
- **`deps` is still `apt-get install -y`, and conduit still needs it.** §4.6.
  DEPS-01 (the discarded error) and DEPS-02 (the package is not in the image) are
  both open and both named.

---

## 12. Curator checklist

- [ ] **Vehicle chosen and defended in one line** in `_note`. "Flathub because
      Debian's build is two years old" is a reason. "Flathub because it was first
      in the search results" is not.
- [ ] `arch` explicit. For Flathub, equal to the set Flathub actually publishes,
      **measured** with `flatpak remote-info flathub --arch=<a> <id>`.
- [ ] Flathub: exact case-sensitive AppStream id; `//branch` if the app publishes
      more than one; `command` cross-checked against
      `flatpak info --show-metadata`.
- [ ] Vulos-native: an entry per architecture, each with a URL you fetched and a
      digest **you computed from the bytes that arrived**. Never a `latest` URL.
- [ ] `archive_strip` / `extract_dir` / `binary_name` derived by **listing the
      archive**, not guessed.
- [ ] `post_install` writes config only — no fetch, no `|| true`, literal port,
      and **no `chown`**: the installer hands `data/` to uid 65534 itself
      (STATE-01). A recipe that chowns is either duplicating that or reaching
      outside `data/`, and the second one needs a sentence in `_note`.
- [ ] No architecture spelled anywhere except an `artifacts` key (ARCH-02).
- [ ] `scripts/verify-app-recipe.sh <id>` run, ledger row committed by the script
      (never hand-written), or an honest `untestable-on-arm64` row with the
      reason.
- [ ] Staged as `registry.d/<name>.json` with `"signature": ""`. **Never edit
      `registry.json`.**
- [ ] Entry signed by the human ceremony and `make verify-registry-prod` passing.

**If you cannot fetch and hash an artefact, the entry does not ship.** An
unmigrated app with a written-down reason is a good outcome. A fabricated
checksum is the worst defect this catalogue has ever contained, and it shipped
for months looking exactly like diligence.
