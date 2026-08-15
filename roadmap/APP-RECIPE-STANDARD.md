# App Recipe Standard — where the bytes come from, and how we prove it

> **Purpose.** The App Hub is being expanded from 55 to ~120 curated desktop apps.
> This is the one document a curator (human or agent) needs: the fixed set of
> install **sources**, the fields each one requires, how trust is expressed, and
> the harness that has to go green before an entry ships.
>
> **Status.** The install engine described in §1 is shipped and is what
> `backend/services/appnet` does today. §3's field additions are **specified,
> not yet applied** — the `registry.go` diff is written out in §7 for the single
> writer of `registry.json` to apply. The verification harness in §5
> (`scripts/verify-app-recipe.sh`) is shipped and self-tested.
>
> **Two decisions already taken by the founder, which this standard encodes:**
>
> 1. **Filter Flathub, do not mirror it.** Some proprietary apps on Flathub are
>    `extra-data`: the Flathub package is a thin manifest that downloads the
>    vendor's real binary at install time, so a mirror would carry no payload —
>    the client still contacts vendor servers and the vendor's terms still
>    govern. So Vulos curates *which* Flathub apps appear and runs no repository
>    of its own. **Which apps are actually extra-data is a manifest-reading
>    question, not an assumption** — of the suspected set only Chrome is
>    confirmed; see [`APP-CATALOG.md`](APP-CATALOG.md) policy 1.
> 1a. **Proprietary apps are excluded for now** (founder call, 2026-08-15,
>    `APP-CATALOG.md` policy 1a). `proprietary` stays a required field: it is
>    what the exclusion is enforced on, and the call is explicitly reversible.
>    It also removes most of the x86_64-only set, so the share of the catalogue
>    that can actually be install-tested on the founder's arm64 machine is much
>    higher than it first looked.
> 2. **Flathub is not always the right vehicle.** Because the catalogue is
>    curated per app, an official Debian package or an official vendor repository
>    is sometimes the better source. §2 makes that choice explicit and forces the
>    curator to say *why*.

---

## 1. What the engine actually does today

`registry.json` → `appnet.Registry` → per-app `RegistryEntry` → per-version
`VersionRecipe`. Install goes through **one** function,
`appnet.InstallFromRegistry` (`backend/services/appnet/registry.go:918`), which
the box reaches at `POST /api/store/install` →
`AppStore.InstallFromRegistry` (`store.go:156`). In order:

1. **Publisher signature** — `TrustedKey()` resolves the baked trust anchor →
   root-signed release cert → release key; `VerifyEntrySignature` checks the
   entry's Ed25519 signature over `signing.Canonical({"app_id", "entry"})`.
   Fail-closed: no anchor, bad cert, missing or wrong signature ⇒ refused.
   (REGISTRY-SIGN-01. `VULOS_REGISTRY_INSECURE=1` is dev-only and refused when
   `VULOS_ENV=prod`, which is also the default when `VULOS_ENV` is unset.)
2. **`_disabled`** on entry or recipe ⇒ refused. **`min_version`** downgrade floor enforced.
3. **`validateRecipeSecurity`** — rejects `curl|bash` / `wget|bash` / `sh -c "…|…"`;
   requires a non-empty `checksum` whenever the recipe downloads a binary
   directly, and unconditionally when `download_url` is set (SECAUDIT2-H1).
4. **One of three install paths**, chosen by which fields are set:
   - `flatpak_id` non-empty → `FlatpakInstall` → `flatpak install -y --noninteractive flathub <id>`, then `~/.var/app/<id>/{cache,config,data}` is created and chowned for every user in `/home`.
   - else `download_url` non-empty → `staticInstall`: stream to a temp file, verify sha256, screen every tar member for path traversal, extract with `--strip-components=<archive_strip>` (or drop a single binary into `bin/` and chmod 0755).
   - else `install` (a shell command) is run with `cwd=<appDir>`, `APP_DIR` and `HOME` in the environment; `apt-get update` is run first if `/var/lib/apt/lists` is empty.
5. **`deps`** are installed via `packages.InstallDeps`.
6. **`app.json` manifest** is generated from entry + recipe (`command`, `port`,
   `type`, `env`, `permissions`, `singleton`, `auto_start`, `deps`, …). For a
   Flatpak with an empty `command`, the manifest command becomes
   `flatpak run <flatpak_id>`. A placeholder `icon.svg` is written if absent.
7. **`post_install`** runs (failures are logged, **not** fatal).
8. `data/` is symlinked to the user data dir on a fresh install.

Fields the structs do **not** model are preserved verbatim through
`RegistryEntry.Extra` / `VersionRecipe.Extra`, because the publisher signature
covers the marshalled entry — dropping an unknown key would put it outside the
signature. In practice `_note`, `lane`, `admin_only` (entry) and `_note`,
`arch` (recipe) live there today.

### 1.1 Two defects this standard exists to stop

- **Per-recipe `arch` is dead data.** `steam`, `wine` and `lutris` carry
  `"arch": "amd64"` *inside the recipe*, where nothing reads it — it lands in
  `Extra`. The field the App Hub actually surfaces is the **entry-level**
  `arch []string`, and for all three of those apps it is `null`, i.e. "all
  architectures". On an arm64 box they are listed as installable and cannot
  install. `arch: null` is currently the value for 47 of 55 entries.
- **Nothing anywhere enforces architecture at install time.** The arch gate is
  advisory metadata only. Until that changes, a wrong `arch` is a defect that
  only shows up as a failed install on a user's box.

---

## 2. The install sources

Every entry declares exactly one `source`. The list is closed — a curator who
needs a seventh source must extend this document first.

| `source` | Bytes come from | Who signs them | Use it when |
| --- | --- | --- | --- |
| `flathub` | Flathub's OSTree repo (or, for `extra-data`, the **vendor's** server at install time) | Flathub's repo GPG key; the app itself may or may not be publisher-`verified` | A maintained Flathub build exists for every arch we ship, and it is the app's normal distribution channel |
| `debian` | `deb.debian.org`, the suite the image is built from (`trixie`) | Debian archive keyring, already trusted by the image | Debian ships a current-enough build. **Preferred over Flathub** for small tools: no runtime download, no sandbox, no duplicate stack |
| `vendor-apt` | The vendor's own apt repository | The vendor's GPG key, which the recipe pins | The vendor maintains a proper apt repo (Grafana, Jellyfin, Syncthing…) and Debian's build is absent or too old |
| `vendor-download` | A pinned URL on the vendor's own domain / release host | **Nothing but our own `checksum`** — this is the weakest source | Upstream ships a release tarball or static binary and no repo (Vaultwarden, Navidrome, Memos, conduwuit…) |
| `webapp` | A static bundle we fetch and serve ourselves | Same as `vendor-download` — a pinned artifact + `checksum` | The app is pure client-side web (Excalidraw, drawio, Cinny…) and is served by the box |
| `vulos` | A Vulos first-party release | The Vulos release key — the same key that signs the registry | Our own products (kerf, wede, …) |

**Required fields per source** (⬛ = required, ▫ = optional):

| Field | flathub | debian | vendor-apt | vendor-download | webapp | vulos |
| --- | :-: | :-: | :-: | :-: | :-: | :-: |
| `source` | ⬛ | ⬛ | ⬛ | ⬛ | ⬛ | ⬛ |
| `arch` (entry-level, explicit, never `null`) | ⬛ | ⬛ | ⬛ | ⬛ | ⬛ | ⬛ |
| `verified` + `verified_by` | ⬛ | ⬛ | ⬛ | ⬛ | ⬛ | ⬛ |
| `proprietary` | ⬛ | ⬛ | ⬛ | ⬛ | ⬛ | ⬛ |
| `extra_data` | ⬛ | – | – | – | – | – |
| `flatpak_id` | ⬛ | – | – | – | – | – |
| `flatpak_runtime` | ⬛ | – | – | – | – | – |
| `install` | – | ⬛ | ⬛ | ▫ | ⬛ | ▫ |
| `packages` | – | ⬛ | ⬛ | – | ▫ | ▫ |
| `apt_repo` + `apt_key_url` + `apt_key_sha256` | – | – | ⬛ | – | – | – |
| `download_url` + `checksum` | – | – | – | ⬛ | ⬛ | ⬛ |
| `command` | ▫ (defaults to `flatpak run <id>`) | ⬛ | ⬛ | ⬛ | ⬛ | ⬛ |
| `port` | – | ▫ | ▫ | ▫ | ⬛ | ▫ |

Choosing a source is a decision the curator must be able to defend in one line;
put it in the entry's `_note`. "Flathub because Debian's build is 2 years old"
is a reason. "Flathub because it was first in the search results" is not.

### 2.1 `flathub` specifics

- `flatpak_id` must be the **exact** AppStream ID (`org.videolan.VLC`), which is
  case-sensitive and is not the app's name.
- `flatpak_runtime` records the runtime the build expects
  (`org.gnome.Platform/aarch64/48`, `org.freedesktop.Sdk/x86_64/25.08`, …). It
  is the harness's runtime assertion and it is how a curator sees the *real*
  cost: Geany is 184 MB, its Freedesktop SDK runtime is another 1.68 GB.
- `extra_data: true` means the Flathub package is a downloader stub for the
  vendor's binary. Then: `proprietary` is almost always `true`, the install
  reaches the **vendor's** servers (not Flathub's), the vendor's terms govern,
  and the app can break when the vendor moves a URL. Mark it, surface it, and
  never describe such an app as "from Flathub" in the UI without that caveat.
  **Set it only after reading the app's Flathub manifest** — a `type: extra-data`
  source consumed at *install* time is extra-data; a `type: file` against the
  vendor's host consumed at Flathub *build* time is not, and Flathub does hold
  and sign those bytes. Guessing here gets the redistributability question
  backwards.
- **`verified` is Flathub's publisher flag**, read from
  `https://flathub.org/api/v2/verification/<id>/status`. Roughly a third of
  Flathub has no verified publisher. It is a first-class field, not a footnote,
  and the App Hub must be able to show "published by the developer" versus
  "packaged by a third party".

### 2.2 `vendor-apt` specifics

`install` must add the repo and key **without** piping anything into a shell —
`validateRecipeSecurity` rejects `curl … | bash` outright. The pattern is:
fetch the key to a file, verify `apt_key_sha256`, `gpg --dearmor` it into
`/usr/share/keyrings/`, write a `signed-by=` source line, `apt-get update`,
`apt-get install`. A `vendor-apt` recipe whose key is not pinned by sha256 is
just `vendor-download` with extra steps and must be declared as such.

### 2.3 `vendor-download` / `webapp` specifics

`checksum` is mandatory and enforced by the engine. Pin the exact release
artifact, never a `latest` redirect: a moving URL with a fixed checksum is a
recipe that will fail on the next upstream release, and a moving URL *without* a
checksum will not install at all.

---

## 3. Trust and architecture fields

```jsonc
{
  "source": "flathub",          // one of the six in §2
  "verified": true,             // publisher identity attested by the source
  "verified_by": "flathub",     // flathub | debian | vendor-gpg | sha256-pin | none
  "proprietary": false,         // non-free payload / vendor EULA governs
  "extra_data": false,          // flathub only: package downloads vendor bytes at install time
  "arch": ["amd64", "arm64"],   // EXPLICIT. never null, never omitted.
  "vetted": true                // unchanged meaning: reviewed by Vulos
}
```

- `verified` ≠ `vetted`. `vetted` is *our* review. `verified` is the **source's**
  attestation that the publisher is who they claim to be. An app can be vetted
  and unverified (we read the manifest ourselves and accepted the risk), and it
  can be verified and unvetted (nobody here has looked at it yet).
- `verified_by` says what the claim rests on: `flathub` = Flathub's verified
  flag; `debian` = it is in the Debian archive; `vendor-gpg` = we pin the
  vendor's repo key; `sha256-pin` = nothing but our own checksum stands behind
  it; `none` = say so out loud.
- `proprietary: true` is not disqualifying — it is disclosure. The UI must be
  able to filter on it.

### Architecture rules

1. `arch` is **entry-level and mandatory**, listing Debian arch names
   (`amd64`, `arm64`). `null`/omitted is a defect, not "all".
2. A recipe-level `arch` may narrow an entry (one version amd64-only). It must
   be an **array**, and it only counts once `VersionRecipe.Arch` is a modelled
   field — today a recipe-level `arch` is invisible dead data (§1.1).
3. For `source: flathub`, `arch` must be exactly the set Flathub actually
   builds, mapped `x86_64→amd64`, `aarch64→arm64`. The harness reads
   `https://flathub.org/api/v2/summary/<flatpak_id>` and **fails the entry** if
   the registry claims an arch Flathub does not publish.
4. Vulos ships amd64 **and** arm64 images. An entry that appears in the App Hub
   on a box that cannot install it is a bug of the same weight as an install
   that errors out.

---

## 4. Signing — what adding an entry requires

`registry.json` is Ed25519 publisher-signed per entry (REGISTRY-SIGN-01), and
`make verify-registry-prod` passes 55/55 today against
`keys/trust-anchor.pub` → `keys/release-cert.json` (release key
`release-2026-08`). **None of that is negotiable, bypassable or stubbable.** In
particular: do not set `VULOS_REGISTRY_INSECURE`, do not add an exception path
to `verify-registry`, do not hand-write a `signature` field.

Adding entries:

1. Author the entry (no `signature` field, or an empty one).
2. `make sign-registry RELEASE_PRIV=<release private key>` — signs **every**
   entry with the release key and re-runs `make verify-registry`.
3. `make verify-registry-prod` must pass, including its coverage assertion
   (*N* of *N* app IDs verified, 0 skipped).
4. `make publish-feed` appends the signed anti-rollback feed entry.

The signature covers `{"app_id", "entry-without-signature"}` canonically, so an
entry cannot be moved to another app id, and **every** key in the entry is
signed — including keys the Go structs do not model. Adding a field to the JSON
therefore invalidates the signature: re-sign, never patch.

An entry that is not ready to be signed goes in `registry-unverified.json`
(quarantine: unsigned, never loaded, never shipped, cross-checked by
`make verify-registry`). Promotion is validation-then-sign, never
signature-then-validation.

---

## 5. Verification — `scripts/verify-app-recipe.sh`

```
scripts/verify-app-recipe.sh <app-id>       # install-test one app, then remove it
scripts/verify-app-recipe.sh --self-test    # prove the harness can go red
scripts/verify-app-recipe.sh --plan         # size-ordered work list, smallest first
scripts/verify-app-recipe.sh --sweep --limit N
```

It spins a throwaway `debian:trixie` container (the suite `build.sh` builds the
image from, `SUITE="trixie"`) with flatpak + the flathub remote configured
exactly as the shipped image configures them, and then:

**It runs the product's own installer.** Not `flatpak install`, not
`apt-get install` — a ~120-line generated driver whose entire body is
`appnet.LoadRegistry` + `appnet.InstallFromRegistry`, the same function the
box's `POST /api/store/install` reaches. Signature verification, the security
gate, the checksum enforcement, the tar traversal screen, the manifest writer
and `post_install` are all the shipping ones. If the product's installer
breaks, the harness goes red. (This repo has been burned by the opposite shape:
a CI gate asserted window placement using `foot --title` while the shipping
client was `cog`, which never sets a title — the gate stayed green through a
feature that had never once worked.)

**Signatures are verified for real.** The container runs with `VULOS_ENV`
unset — which `services/env` treats as prod — and the repo's real
`keys/trust-anchor.pub` + `keys/release-cert.json`. `VULOS_REGISTRY_INSECURE`
is never set. An unsigned entry fails.

**Assertions** (each one recorded individually in the ledger):

| Assertion | What it proves |
| --- | --- |
| `install-path` | `appnet.InstallFromRegistry` returned success |
| `manifest-written` | `app.json` exists in the app dir — the product actually registered the app |
| `command-declared` | the manifest carries a command for the launcher to run |
| `flatpak-present` | `flatpak info <id>` resolves |
| `flatpak-deployed` | the deployed tree exists and is non-empty (an ostree ref with no files satisfies `flatpak info` and is a broken app) |
| `flatpak-runtime` | the runtime the app's metadata names **is installed** — a missing runtime is the classic install-succeeds/app-won't-start |
| `flatpak-runtime-pinned` | the installed runtime matches the registry's `flatpak_runtime` |
| `command-resolves` | the entry-point command resolves **inside the app's own sandbox** (or, natively, in `PATH`/the app dir) |
| `command-executes` | the binary is really `execve`'d — exit 127 or a loader failure is a FAIL; a non-zero app exit or a timeout is a pass, because the process started |
| `artifact-provenance` | apt recipes: a dpkg package owns the binary. download recipes: the artifact landed under the app dir |
| `packages-installed` | every package named in the recipe's `packages` is `dpkg -s`-installed |
| `uninstall` | the product's own `FlatpakUninstall` removed it again, and how much disk came back |

**What it does not prove:** that the GUI starts. There is no display, no
D-Bus session and no GPU in the container. `command-executes` proves the binary
is real and loadable, and that is exactly what it claims — nothing more.

**The self-test** (`--self-test`) runs five synthetic recipes against an
ephemeral trust root generated inside the container (the repo's real signing
keys are never used, `registry.json` is never touched): one control that must go
**green**, and four that must go **red** — an unsigned entry, a nonexistent
Flathub id, a wrong checksum, and a recipe that installs perfectly but whose
`command` does not exist. The last one is the one that proves the *assertions*
work rather than only the install path. A harness that has never failed is not
evidence.

---

## 6. The ledger

- `roadmap/app-verification-ledger.json` — **authoritative**, machine-written by
  the harness, one row per app, committed.
- `roadmap/APP-VERIFICATION-LEDGER.md` — rendered from it by
  `--ledger-render`. Never hand-edited.

Statuses are deliberately distinct:

| Status | Meaning |
| --- | --- |
| `passed` | a container really ran, the product's installer installed it, every assertion held, it was removed again |
| `failed` | it ran and something did not hold — the row says which assertion |
| `untestable-on-arm64` | upstream publishes no aarch64 build, so **this machine cannot install it at all**. Not a pass. Not a claim it works. |
| `disabled` | the entry or its latest recipe carries `_disabled`, which `appnet` refuses by design. Nothing was run. **11 of the 55 entries are in this state**, and calling them `failed` would assert something much louder — that the recipe is broken — about apps nobody has turned on yet. |
| *(absent)* | untested |

**Architecture is a stated limit, not a workaround.** Verification runs on the
founder's arm64 Mac; there is no CI job (founder ruling: *"don't test in ci,
test what you can here, test one at a time, small ones first, keep list of
whats tested, delete after its tested"*). x86_64-only apps — Steam, Chrome,
Spotify, Zoom, VS Code and friends — therefore stay untested. Emulating them
would take hours per app and prove nothing about the box anyone runs. A row
saying "not tested, here is why" is worth more than a green tick that means
nothing.

---

## 7. `registry.go` changes to apply — specified, not applied

**Do not apply these by editing `registry.json` first.** These are Go struct
changes; the registry data follows, and then the whole registry is re-signed
(§4). I am not the writer of `registry.json` — this section is written out for
whoever is.

```go
// RegistryEntry — add after Arch:

    // Source names the install channel this entry uses. Closed set:
    // "flathub", "debian", "vendor-apt", "vendor-download", "webapp", "vulos".
    // See roadmap/APP-RECIPE-STANDARD.md §2.
    Source string `json:"source,omitempty"`

    // Verified reports that the SOURCE attests the publisher's identity —
    // Flathub's verified flag for source=="flathub", the Debian archive for
    // "debian", a pinned vendor GPG key for "vendor-apt". It is NOT Vetted
    // (which is our own review) and it is NOT a claim about code quality.
    Verified bool `json:"verified,omitempty"`

    // VerifiedBy says what Verified rests on: "flathub" | "debian" |
    // "vendor-gpg" | "sha256-pin" | "none".
    VerifiedBy string `json:"verified_by,omitempty"`

    // Proprietary marks a non-free payload whose vendor terms govern use.
    // Disclosure, not disqualification — the App Hub filters on it.
    Proprietary bool `json:"proprietary,omitempty"`

    // ExtraData marks a Flathub package that is a downloader stub: the bytes
    // come from the VENDOR's servers at install time, not from Flathub. This
    // is why Vulos filters Flathub rather than mirroring it — a mirror would
    // not carry these payloads.
    ExtraData bool `json:"extra_data,omitempty"`
```

```go
// VersionRecipe — add after ArchiveStrip:

    // Arch narrows the entry's architectures for this version. Debian arch
    // names ("amd64","arm64"). MUST be an array: three entries today carry a
    // recipe-level string "arch" that lands in Extra and is read by nothing.
    Arch []string `json:"arch,omitempty"`

    // FlatpakRuntime is the runtime ref the Flathub build expects, e.g.
    // "org.gnome.Platform/aarch64/48". Asserted by scripts/verify-app-recipe.sh:
    // a missing runtime installs fine and never starts.
    FlatpakRuntime string `json:"flatpak_runtime,omitempty"`

    // Packages are the dpkg package names the recipe must leave installed.
    // Lets the verifier assert `dpkg -s` per package instead of parsing the
    // install shell line, which is not parseable in general.
    Packages []string `json:"packages,omitempty"`

    // AptRepo / AptKeyURL / AptKeySHA256 pin a vendor apt repository and the
    // key that signs it. A vendor-apt recipe whose key is not sha256-pinned is
    // vendor-download with extra steps.
    AptRepo      string `json:"apt_repo,omitempty"`
    AptKeyURL    string `json:"apt_key_url,omitempty"`
    AptKeySHA256 string `json:"apt_key_sha256,omitempty"`
```

Notes for whoever applies it:

- `jsonKeySet` is reflection-derived, so these keys automatically stop being
  counted in `Extra` — no double-counting, and `MarshalJSON` still merges
  `Extra` for anything still unmodelled. The three existing recipe-level
  `"arch": "amd64"` **strings** will fail to unmarshal into `[]string`; they
  must be changed to arrays (`["amd64"]`) in the same change, and those entries
  re-signed.
- `omitempty` everywhere means entries without the new fields serialise
  byte-identically, so **existing signatures stay valid** until an entry's own
  data changes.
- Surfacing to the UI needs the same fields added to `RegistryListEntry` and
  filled in `ListEntries` (`Source`, `Verified`, `VerifiedBy`, `Proprietary`,
  `ExtraData`) — otherwise the trust data reaches the API and dies there, which
  is exactly what happened to `Keywords` before it was wired up.
- Arch enforcement at install time (refuse when `runtime.GOARCH` is not in the
  entry's `arch`) is a **separate, deliberate** change; until it lands, `arch`
  is advisory and only the harness catches a wrong value.

---

## 8. Contract for a per-app agent

Copy-pasteable. This is the whole job for one app.

```bash
cd /Users/pc/code/vulos/vulos
./scripts/verify-app-recipe.sh <app-id>          # ~30 min, ~2 GB, one at a time
```

**Run one app at a time.** The host is saturated and the harness installs
gigabytes; two concurrent runs make both slower and make the disk numbers
meaningless. `--sweep --limit N` exists and does them sequentially, smallest
first, if you have several.

**Exit codes are the verdict.** `0` pass · `1` fail · `2` harness/infra problem
· `3` skip (this machine cannot install it). The script writes the ledger row
itself — you do not hand-write ledger entries, ever.

**Measured cost** (arm64 Mac, host load 70–260, 2026-08-15):

| | wall clock | disk during run | disk after |
| --- | ---: | ---: | ---: |
| Flatpak app, runtime not yet cached | ~30 min | ~2.0 GB | ~0 — the container is `--rm` |
| Flatpak app, second app sharing a runtime | less, unmeasured | app size + repo copy | ~0 |
| apt / static-download app | ~1–3 min | tens–hundreds of MB | ~0 |
| harness self-test (all five cases) | ~40 s | ~5 MB | 0 |
| base image, once for everything | ~15 min | 242 MB, kept | 242 MB |

The disk cost lives inside the container and goes away when it exits. What
persists is the 242 MB base image. Flathub's own "installed size" figure is
roughly a third of the truth: FileZilla reports 35 MB and cost 2059 MB, because
the runtime (629 MB) and ostree's repo copy dominate.

**A pass means** — and you may state exactly this and nothing more:

> `appnet.InstallFromRegistry` installed it in a debian:trixie container, the
> `app.json` manifest was written, the Flatpak deployed with a non-empty tree,
> its runtime was installed, its entry-point command resolved inside its own
> sandbox and was really `execve`'d, and the product's own uninstall removed it.

**A pass does not mean** the GUI starts. There is no display, no D-Bus session
and no GPU in the container. Do not write "works" or "launches" — write what
the assertions say.

**Put in your report:** the app id, the exit code, the ledger row verbatim,
the measured minutes and MB, and — if it failed — the failing assertion name
and the harness's message, unedited.

**Never:**

- weaken, skip or special-case an assertion to get an app green. A failing
  check is fixed by fixing the recipe, or by recording the failure;
- mark an app `verified` without the evidence — for Flathub that is
  `https://flathub.org/api/v2/verification/<flatpak-id>/status` returning
  `"verified": true`, quoted in your report;
- claim an install you did not run, or convert a `3` (skip) into a pass. An
  `untestable-on-arm64` row is a good outcome; a fake green one is the exact
  failure this harness was built to stop;
- hand-edit `roadmap/app-verification-ledger.json` or the rendered `.md`. A
  Go guard (`backend/internal/docsref/appledger_test.go`) fails the build on a
  row a real run could not have produced;
- edit `registry.json` — there is a single writer for it;
- set `VULOS_REGISTRY_INSECURE`, or touch the signing keys or `verify-registry`.

**If it fails**, that is a result, not a blocker: report it with the assertion
name. `flatpak-runtime` failing means the app would install and never start;
`command-resolves` failing means the recipe's `command` is wrong; `install-path`
failing means the recipe does not install at all.

---

## 9. Curator checklist

Before proposing an entry:

- [ ] `source` chosen and the reason written in `_note`.
- [ ] `arch` explicit, and for `flathub` equal to Flathub's real arch set.
- [ ] `verified` + `verified_by` filled from evidence, not from optimism.
      For Flathub, that is the verification API's answer, quoted.
- [ ] `proprietary` / `extra_data` honest.
- [ ] `command` is what the app actually runs; for Flatpak, cross-check it
      against `flatpak info --show-metadata`'s `command=`.
- [ ] `download_url` recipes: exact release URL + `checksum` you computed
      yourself from the downloaded bytes.
- [ ] `scripts/verify-app-recipe.sh <id>` run, ledger row committed —
      or the honest `untestable-on-arm64` row, with the reason.
- [ ] Entry re-signed and `make verify-registry-prod` passing.
