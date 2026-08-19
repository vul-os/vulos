# Per-architecture artefacts — and the three first-party apps it unblocked

> **Date.** 2026-08-16. **Scope.** `VersionRecipe.artifacts` / `binary_name`,
> zip support in the static-install path, and the App Hub entries for
> **diwan**, **wede** and **lilmail**. **kerf** is deleted.
>
> **One line.** A recipe could name one download URL, so a download-based app
> had to pick one architecture; all three of these apps publish working amd64
> **and** arm64 Linux binaries, and all three are now installable on both from
> the releases that already existed.
>
> Companion to [`FIRST-PARTY-REGISTRY-TRUTH.md`](FIRST-PARTY-REGISTRY-TRUTH.md),
> which established the artefact facts for diwan/wede/kerf and recommended
> exactly this change (§5). Nothing below is assumed; every digest was
> recomputed and every install was run.

---

## 1. The defect was entirely in the registry format

`VersionRecipe.DownloadURL` was a single string with a single `Checksum`, and
the per-recipe `arch` that three entries carried was **dead data** — the struct
had no `Arch` field, so it landed in `Extra` and was read by nothing.

No upstream release needed re-tagging. The arm64 binaries had been in
`diwan v0.1.0`, `wede v0.1.3` and `lilmail v1.14.0` all along; Vulos simply
could not express them. Staging these apps amd64-only would have told an arm64
owner that the OS's own office suite, IDE and mail client were unavailable
**while the binaries sat in the release** — and it would have forked the app set
by architecture, which the everything-syncs directive does not allow: an
instance is meant to be a near clone of its siblings, and an app that cannot
follow a user onto their arm64 box breaks that.

## 2. The shape, and why this one

```jsonc
"artifacts": {
  "amd64": { "download_url": "…/diwan-linux-amd64", "checksum": "467f27fc…" },
  "arm64": { "download_url": "…/diwan-linux-arm64", "checksum": "2e92fc07…" }
},
"binary_name": "diwan"
```

**The URL and its checksum live in the same object.** The obvious alternative —
letting `download_url` be *either* a string *or* an `{arch: url}` map while
`checksum` stayed a scalar — can express a recipe that pins two different
binaries to one hash. That is not a hypothetical: it is the shape a curator
reaches for first, and it would leave every architecture but one installing an
unverified artefact while *looking* pinned. A schema should not be able to say
the wrong thing.

**It is additive, not polymorphic.** `omitempty` means the 55 entries that use
`download_url` serialise byte-identically, so the schema change alone invalidates
no publisher signature (asserted by
`TestArtifacts_OmitEmptyKeepsExistingEntriesByteIdentical`). A "string or object"
field would have needed a hand-rolled `json.RawMessage` decoder, which is where
silent mis-parses live.

**Resolution is strict, and never falls back.** A per-arch recipe that does not
cover the box's architecture is a loud refusal naming what it does offer. Falling
back to `download_url` would hand an arm64 box an amd64 binary that passes its
checksum and then fails to `exec` — a *successful install of an app that cannot
run*, which is the entire defect class being removed. Resolution runs through
`arch.go`'s `NormalizeArch`, the single normalisation point, so a registry
spelling `x86_64` and a box saying `amd64` still match.

**`binary_name` is what makes it usable.** The release assets are called
`diwan-linux-amd64` and `diwan-linux-arm64`, but `command` is one string shared
by every architecture, so without a stable installed name no command could refer
to the binary on both. It pins the install to `bin/diwan`. The alternative — a
`${ARCH}` substitution inside `command`, expanded at launch — spreads
architecture spelling into free-text recipe strings and puts the fix in the
launcher, where the arch is not already known.

**Both fields are guarded rather than trusted.** `validateRecipeSecurity` refuses:
both forms set at once; a top-level `checksum` beside `artifacts`; an artifact
with no checksum (SECAUDIT2-H1, per artifact); an artifact with no URL; a null
artifact; one architecture spelled two ways (which would make the winner depend
on Go's randomised map order); and `binary_name` on an archive or carrying a path
separator. A `binary_name` that silently did nothing would just be per-recipe
`arch` all over again.

## 3. The zip gap was real, and the way it hid is the interesting part

**Answer to the question asked: the static-install path handled `.tar.gz`,
`.tgz`, `.tar.bz2` and `.tar.xz` only.** A `.zip` fell through to the
plain-binary branch — copied to `bin/<name>.zip`, `chmod 0755`, **install
reported success**, app could never start. lilmail is the first entry to ship
zips, so this had never been hit.

`extractZip` closes it using Go's `archive/zip` in-process, so the gated image
package set (`scripts/image-packages.txt`) needs no `unzip`.

The first fix was wrong in a way a green suite could not show. Putting `.zip`
into the same extension list the `tar` branch reads meant a mutation that
disabled zip detection **survived the entire suite**: macOS ships BSD tar
(libarchive), which reads zips happily, while the shipped Debian image ships GNU
tar, which cannot read one at all. Green on the test machine, fatal on every box
a user owns — the host-is-not-the-target trap, the same family as
`syscall.Getsid` compiling here and breaking CI. The branches are now mutually
exclusive, and `TestStaticInstall_ZipDoesNotDependOnTar` runs the install with a
`tar` on `PATH` that always fails, so the verdict no longer depends on which tar
the test machine has.

The traversal screen had the mirror-image problem. A textual `../` match ran
first, which made the containment check behind it **unreachable — a guard that
could never fire**, the failure this codebase keeps finding. The textual
pre-filter is gone. Absolute names are still refused outright (containment alone
would let `/etc/cron.d/evil` trim harmlessly *inside* the app dir, the way `tar`
silently strips a leading slash); traversal is caught on the resolved
destination, and the traversal tests now exercise that line.

## 4. Mutation coverage

21 mutations against the new code, **21 killed, 0 survivors**. Each was verified
to have matched its anchor exactly once and to have produced a real `--- FAIL`;
a non-zero exit with no test-failure marker was treated as an ERROR, not a kill.

Two survivors were found and fixed rather than explained away:

- **the install dispatch ignoring `artifacts` entirely** survived until
  `TestInstallFromRegistry_ArtifactsReachTheStaticPath` was added. Every other
  test called `staticInstall` directly, so nothing noticed that nothing *routed*
  to it. A per-arch recipe that resolves perfectly and is never invoked is the
  same green-install-dead-app failure by another road.
- **routing zip to the tar branch** survived for the bsdtar reason in §3.

## 5. The three entries

Every digest below was recomputed **from a fresh download on 2026-08-16** and
agrees with **two independent published sources**: the release's own
`checksums.txt` / `SHA256SUMS`, and GitHub's own asset digest from the releases
API. Two sources agreeing is the point; one is a value copied twice.

| app | version | arch | sha256 |
| --- | --- | --- | --- |
| diwan | 0.1.0 | amd64 | `467f27fcc68133488394bae758bb3c629e76092c89473e4f29a5ad861668e293` |
| diwan | 0.1.0 | arm64 | `2e92fc0713227f7e3380a4ebe292768930b54e6d3e5d167aa2117ef3d7891ce5` |
| wede | 0.1.3 | amd64 | `275ed918bd4539a9710c9189b682294293011a50ef11a8cdfd1092e4a3a0c8a7` |
| wede | 0.1.3 | arm64 | `73800e85e9577e2def5d62d59847e8333a3c030c07651c748675277aaeed6ae8` |
| lilmail | 1.14.0 | amd64 | `c3d0fa7834e60eba3ed724ae4b6324d718e3372d0d9b2c4fe5ff1cce47e65814` |
| lilmail | 1.14.0 | arm64 | `7689dd62a46ce52a89a99d6acec86b48640932a4d4f86eccf957c58fb063cc3b` |

### How they were proved

Not by re-running the recipe by hand. A driver whose body is
`appnet.InstallFromRegistry` — **the same function the box reaches at
`POST /api/store/install`** — was cross-compiled for `linux/amd64` and
`linux/arm64` and run in `debian:trixie-slim` on both platforms. Signature
verification was **fully on**: `VULOS_ENV` unset (which `services/env` treats as
prod), `VULOS_REGISTRY_INSECURE` never set. The entry under test was signed
inside the container with an **ephemeral** root + release key generated there,
with `VULOS_TRUST_ANCHOR` / `VULOS_RELEASE_CERT` pointed at that ephemeral
material. The repo's production keys were never touched and the shipped
`registry.json` stays unsigned — this proves the **recipe**, and deliberately
proves nothing about who vetted the entry.

Assertions per app, per arch: install-path, manifest-written, command-declared,
artifact-provenance, **arch-correct** (`file` confirms the installed binary
really is x86-64 resp. ARM aarch64 — the assertion that would catch a resolver
handing over the wrong architecture), and a real HTTP response from the launched
app.

### Per-app notes

**diwan** — `bin/diwan` via `binary_name`. v0.1.0 has no port flag and no env
override for `server.addr`, so the port is a **literal** in `data/diwan.yaml`
written by `post_install`. That is correct rather than fragile: `appPort` is
taken verbatim from the recipe's `port` field and is *not* pool-allocated (only
the host port is), so `${PORT}` at launch always equals it. It **must** be a
literal because `${PORT}` is not expanded during `post_install` — only into
`command`, at launch.

**wede** — `bin/wede` via `binary_name`; the port arrives via `-port ${PORT}`,
which the launcher does expand. v0.1.3 `log.Fatal`s without `wede.config.json`
and rejects an empty password, so it fails loudly rather than starting
half-configured; `post_install` mints a 32-character password from
`/dev/urandom` and **exits 1** if it cannot produce 32. `git` is a real runtime
dependency — wede shells out to the git binary — not decoration.

**lilmail** — the zip case. `archive_strip: 1` drops the leading `lilmail/`
directory, leaving `./lilmail` and `config.toml.example` at the app root;
`binary_name` is deliberately **absent** because it applies only to
single-binary downloads and is refused on an archive rather than ignored.
`config.LoadConfig("config.toml")` is read relative to the working directory,
which the launcher sets to the app dir, and there is no port flag — so the port
is a literal, as for diwan. `[jwt].secret` and `[encryption].key` are
**required** (`LoadConfig` rejects a key that is not 16/24/32 bytes), so
`post_install` mints both and refuses rather than writing weak or empty ones.

One thing was found by running it rather than reading it: without a pre-existing
`cache/` directory lilmail logs
`scheduled send unavailable (store open failed)` and runs **degraded** — it
serves, so a shallower check would have called it a pass. `post_install` creates
`cache/` and `sessions/`.

lilmail hosts no mail and has no account system; the `[imap]`/`[smtp]` hosts in
the generated config are placeholders the owner edits to point at their **own**
mailbox.

**No recipe here swallows a failure.** The `|| (write a placeholder)` pattern
that made kerf's install "succeed" is the thing being removed, not reproduced.

## 6. kerf

Deleted from `registry.json`, not quarantined — quarantine is for an entry whose
caveat can be discharged by validation, and there is no artefact here to
validate. The evidence is in `FIRST-PARTY-REGISTRY-TRUTH.md` §4.

**`APP_LOGOS.kerf` and `frontend/public/product-logos/kerf.svg` are kept**, with
a comment recording why. The mark is byte-identical to kerf's own
`brand/logo.svg`, and brand assets are copied outward rather than redrawn — a
later re-derivation is precisely how this suite has previously ended up with a
*different* logo for the same product. It is also not the orphan it appears to
be: most of that block (`envoir`, `llmux`, `kotva`, `aql`, `vuna`, `kilio`,
`soko`, `gitstate`, `magnetite`) is first-party marks for products with no App
Hub entry, so an unregistered mark there is the normal state.

## 7. Icons

All four marks were checked byte-for-byte against their products' own approved
`brand/logo.svg` before being relied on — `cmp` clean for diwan (542 B),
lilmail (320 B), wede (843 B) and kerf (379 B). None contains an SVG `<text>`
element, so none is subject to the per-machine font hazard. All three new apps
already had `APP_LOGOS` entries, so no mark was drawn, refined or regenerated.

`AppIcons.test.ts` re-derives its roster from `Object.keys(registry.json.apps)`
and passes 9/9 against the new registry. **That green was tested, not assumed**:
injecting an app id carrying no art or logo turned it red on exactly the roster
assertion —

```
× every statically-registered app id has its OWN bundled art or logo …
AssertionError: expected [ 'zzz-no-icon-fixture' ] to deeply equal []
```

Had the control passed, the gate would have stopped reading the registry and the
9/9 would have meant nothing.

## 8. What the founder's signing ceremony must run

### ⚠ Two Go tests are RED right now, and that is the mechanism working

```
--- FAIL: TestAcceptance_ShippedAnchorVerifiesShippedRegistry
    registry entry "wede" is UNSIGNED — run `make sign-registry`
    registry entry "diwan" is UNSIGNED — run `make sign-registry`
    registry entry "lilmail" is UNSIGNED — run `make sign-registry`
    verified 56 shipped registry entries against the shipped trust anchor
--- FAIL: TestShippedRegistry_HoldsNoUnsignedEntry
    registry.json ships 3 UNSIGNED entry/entries [diwan lilmail wede] — either sign
    them (`make sign-registry`) or move them to registry-unverified.json; the
    signed set has no exception path
```

**Do not "fix" these by weakening them, and do not fix them by quarantining the
entries.** They are the loud failure that a merge-without-signing is supposed to
produce. The other 53 entries verify against the shipped anchor exactly as
before; these three fail for one reason only — `signature: ""` — and the
ceremony below clears both tests. An agent silencing them would be minting the
appearance of vetting without the vetting, which is the one thing this trust
model cannot survive.


The three entries carry `"signature": ""` **deliberately**.
`VerifyEntrySignature` fails closed on an empty signature, so they are inert:
they cannot be installed by accident, and a merge that skips signing fails
loudly rather than shipping unvetted trust material. In this trust model the
signature **is** the act of vetting.

`keys/release.priv.json` in this repo is a leftover **dev** key whose public half
is not what the shipped certificate authorises, so `make sign-registry` would be
refused by `check-release-key` — a good refusal. The matching private key lives
in the ceremony vault.

```sh
cd /Users/pc/code/vulos/vulos
make sign-registry RELEASE_PRIV=/Users/pc/code/vulos/vulos-cloud/signing-vault/release.priv.json
make verify-registry-prod      # must report 56 of 56 verified, 0 skipped
make publish-feed RELEASE_PRIV=<same key>
```

`sign-registry` re-signs **every** entry with that one key — normal, and what
the verify step then proves. Expected count is **56**: 55 today, minus `kerf`,
plus `diwan` and `lilmail` (`wede` is a replacement, not an addition).

Note that adding `artifacts`/`binary_name` to an entry changes its signed bytes,
so the three edited entries would need re-signing regardless; the 53 untouched
entries serialise byte-identically and their existing signatures remain valid
until `sign-registry` refreshes them.

## 9. Still open, in files this pass did not own

Carried forward from `FIRST-PARTY-REGISTRY-TRUTH.md` §6 and unchanged here:

- ~~**`post_install` failure is only a warning**~~ — **CLOSED, and verified
  against the shipping code 2026-08-19 (POSTINSTALL-01, `registry.go`).** A failed
  `post_install` is fatal, the half-built app directory is removed so a retry
  starts clean, and the error names the app and carries the last ten lines of
  stderr. The behaviour this bullet described is gone; it was still listed as open
  after being fixed, which is its own small defect and the reason this file is
  being read rather than trusted.

  Closed alongside it, 2026-08-19: **`${PORT}` was never exported to
  `post_install`** (POSTINSTALL-04). `sh` expands an unset `${PORT}` to the empty
  string and exits 0, so `nginx` wrote `listen ;` and `transmission` wrote
  `"rpc-port":` while the installer reported success. `PORT` now comes from
  `recipe.Port` — the same field the manifest carries and the launcher substitutes
  into `command`, so a config file and the command line that reads it cannot name
  two different ports — and a `${PORT}` reference with no declared port is refused
  outright rather than given a default.
- ~~**The installer never chowns the app dir**~~ — **CLOSED 2026-08-17
  (STATE-01, `backend/services/appnet/state_owner.go`).** The installer now hands
  `data/` — and the target of the `data/` symlink — to uid 65534 after
  `post_install`, and fails the install if it cannot. Not the whole app dir, on
  purpose: `bin/` and `static/` stay root-owned, because an app that can rewrite
  its own binary keeps any compromise across a restart. The seven migrated
  recipes that carried `chown -R 65534:65534 data` no longer do. The three
  first-party recipes still chown, and still should: lilmail's state lives in
  `cache/`, `sessions/` and `config.toml` at the app ROOT, which STATE-01
  deliberately does not touch.
- **`${PORT}` is not expanded in `post_install`** and six shipped entries rely on
  it (`conduit`, `gitea`, `grafana`, `navidrome`, `nginx`, `transmission`).
- **`code-server`'s checksum is verified and then ignored** via `|| true`.
- **Four entries can never install** (`download_url` set, `checksum` empty):
  `excalidraw`, `hoppscotch`, `memos`, `uptime-kuma`. They fail *closed*, which
  is the right direction, but they are listed as installable.
- **46 entries still declare `arch: null`**, which `ArchSupported` reads as
  "all".
