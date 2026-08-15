# First-party App Hub entries — what is actually installable

> **Date.** 2026-08-15. **Scope.** The three first-party products the App Hub
> claims or should claim: **diwan**, **kerf**, **wede** — plus a survey of the
> other Vulos siblings.
>
> **Outcome in one line.** Two of the three have real, pinned, boot-verified
> release artefacts and are staged in `registry.d/vulos-first-party.json`; the
> third (**kerf**) has none, and its current entry must be **deleted** from
> `registry.json` rather than repaired.
>
> Nothing in this document was assumed. Every claim below is either a quoted
> file in this repo or a command that was run and whose output is reproduced.

---

## 1. The starting state, verified

| id | in `registry.json`? | what its recipe actually does |
| --- | --- | --- |
| `diwan` | **no** | absent entirely, while `frontend/src/core/AppRegistry.ts` describes Diwan as an always-present owned productivity app |
| `kerf` | yes, signed, `vetted: true` | `git clone https://github.com/kerf-cad/kerf.git … 2>/dev/null \|\| (mkdir -p static && printf '<html>…placeholder…' > static/index.html)` |
| `wede` | yes, signed, `_disabled: true` | `command` is `/root/.local/bin/wede -port ${PORT}`; `install` is `""`, `download_url` is `""` — nothing ever places that binary |

**`kerf-cad/kerf` does not exist.**

```
$ gh repo view kerf-cad/kerf
GraphQL: Could not resolve to a Repository with the name 'kerf-cad/kerf'. (repository)
```

Kerf's real repository is `vul-os/kerf` (`git -C /Users/pc/code/vulos/kerf remote -v`).
So the clone in the kerf recipe does not merely *sometimes* fail — it fails
**every time**, and `2>/dev/null || (…)` converts that certainty into a green
install and a placeholder page. Every user who ever installed Kerf from the App
Hub got the stub. The entry's `homepage` points at the same non-existent repo.

---

## 2. Diwan — a real single-binary web app

**What it is.** A self-hostable collaborative office suite (documents,
spreadsheets, slides, whiteboards), CRDT-native, frontend embedded in one Go
binary. `docs/SELFHOST.md`: *"Run — single-user / local mode, no auth, no cloud
… → http://localhost:8080"*. It is a **web app served on a port**, not a static
bundle and not a desktop app.

**Release.** `vul-os/diwan` `v0.1.0`, 2026-08-07. (The working tree is at
`VERSION` 0.3.0 — ahead of the release. Only the released artefact counts.)

| asset | size | note |
| --- | --- | --- |
| `diwan-linux-amd64` | 54,243,512 | **used by the entry** |
| `diwan-linux-arm64` | 52,494,520 | verified, not usable — see §5 |
| `diwan-darwin-{amd64,arm64}` | — | not a box target |
| `office-client-v0.1.0-dist.tar.gz` | 20,443,555 | client dist, not needed: the binary embeds the frontend |
| `checksums.txt` | 338 | covers all four binaries |

**Integrity.** Downloaded fresh and hashed locally:
`467f27fcc68133488394bae758bb3c629e76092c89473e4f29a5ad861668e293`. That value
equals **both** the digest GitHub publishes for the asset **and** the line for it
in the release's own `checksums.txt` — three independent statements of one hash.
`file` reports *ELF 64-bit LSB executable, x86-64, statically linked, stripped*.

**Boot evidence.** Installed and started exactly as the staged recipe specifies
(binary at `bin/diwan-linux-amd64`, config written by `post_install`, launched
with `-config data/diwan.yaml`) in a `debian:trixie-slim` container, on
`linux/amd64` **and** `linux/arm64`:

```
diwan 0.1.0 starting
[deploymode] running as "standalone" (DEPLOY_MODE="")
Diwan running → http://localhost:8081
GET / => 200 4863B text/html; charset=utf-8   <title>Diwan</title>
GET /api/files => 200  []
data/: audit.db  fileacl.db  invites.db  notify.db  userauth.db  versions/ …
```

**Port.** `diwan v0.1.0` has no port flag (`-config`, `-no-rate-limit-writes`
only) and `applyEnvOverrides` carries no override for `server.addr`. The port is
therefore written as a **literal** into `data/diwan.yaml` by `post_install`. That
is correct rather than brittle — see §6.2.

---

## 3. Wede — a real single-binary web app that was never installed

**What it is.** A self-hosted collaborative web IDE — multiplayer editing,
shared terminals, git, chat — *"a single ~19 MB Go binary"*. Again a **web app
on a port**. The old entry's `command` was in fact correct; only the install was
missing.

**Release.** `vul-os/wede` `v0.1.3`, 2026-08-07. (Tree is at `VERSION` 0.6.0.)

| asset | size |
| --- | --- |
| `wede-linux-amd64` | 19,845,304 — **used by the entry** |
| `wede-linux-arm64` | 19,267,768 — verified, not usable (§5) |
| `wede-darwin-*`, `wede-windows-amd64.exe`, `checksums.txt` | — |

**Integrity.** `275ed918bd4539a9710c9189b682294293011a50ef11a8cdfd1092e4a3a0c8a7`,
matching GitHub's asset digest and the release `checksums.txt`.

**The released binary really does take `-port`** — worth checking rather than
trusting, since the tree is three minor versions ahead of the tag:

```
$ /tmp/wede -version
wede v0.1.3
$ /tmp/wede -h
  -p string      Override port (shorthand)
  -port string   Override port (default: from config or 9090)
  -version       Print version and exit
```

**Wede fails loudly without a config — and that shapes the recipe.** With no
`wede.config.json` anywhere, v0.1.3 exits:

```
wede.config.json not found (searched cwd, parent dirs, ~/.config/wede/, and next to executable)
```

and `config.parse` rejects an empty password (*"password is required in
wede.config.json"*). So the recipe must mint one. It generates 32 random
characters from `/dev/urandom` and **refuses (`exit 1`) if it cannot produce 32**
rather than writing a weak or empty password — and no credential is baked into
the registry. The owner reads it from `wede-password.txt` in the app directory.

**Boot evidence** (`debian:trixie-slim`, amd64 and arm64):

```
loaded config from /wede/wede.config.json
wede v0.1.3 running on http://0.0.0.0:9090
workspace: /wede/workspace
GET / => 200 2507B text/html   <title>WEDE | Web Development Environment</title>
POST /api/auth/login  => 200 {"role":"owner","token":"…"}
```

The last line is the one that matters: the credential the recipe mints actually
authenticates.

**`git` is a real dependency.** wede shells out to the git binary
(`backend/internal/chat/chat.go:509`, `exec.Command("git", fullArgs...)`), so the
git panel the README headlines is inert without it. It is in `deps`.

---

## 4. Kerf — no installable artefact exists. Ship nothing.

This is the judgement call, and the evidence made it easy.

**What Kerf actually is.** Not "browser-native WASM CAD" as the current entry
claims. It is a **Python (3.11+) + Node application**: ~60 `packages/kerf-*`
Python packages, a FastAPI-class server, and a pre-built frontend it serves. The
registry's description is false about the technology as well as the repository.

**The release does not contain what its filenames say.** `vul-os/kerf` `v0.1.9`
publishes `kerf-v0.1.9-linux-x64.tar.gz`, `kerf-v0.1.9-macos-arm64.tar.gz` and
`kerf-v0.1.9-macos-x64.tar.gz` — all three with the **identical** sha256
`e4c1f729c503890c5a1d95e54d1e9fe47320bf4333c6b53acd397b1f4f72536e` and the
identical size 41,935,871. They are one platform-agnostic source tarball wearing
three platform names. Kerf's own `install.sh` says so:

> *"Kerf is Python + Node, not a compiled binary — there is nothing to 'install'
> beyond a versioned source + pre-built-frontend bundle plus a venv. The per-OS
> tarballs (macos-arm64 / macos-x64 / linux-x64) are identical in content and
> exist for naming-convention parity with a future single-binary build (TODO)."*

**Its own installer does not work on its own release.** Extracted
`kerf-v0.1.9-linux-x64.tar.gz` in a `python:3.12-slim` container and ran the
bundled `setup.sh`:

```
[kerf setup] python3 3.12.13 — ok
[kerf setup] Creating venv at /tmp/kerf-v0.1.9/venv ...
[kerf setup] Installing Kerf packages (this can take a few minutes on first run) ...
ERROR: file:///tmp/kerf-v0.1.9/packages/kerf-sdk-go does not appear to be a Python project:
       neither 'setup.py' nor 'pyproject.toml' found.
```

`setup.sh` loops over `packages/kerf-*/` and skips only the literal `kerf-sdk`,
but the tarball ships `kerf-sdk-go`, `kerf-sdk-lua`, `kerf-sdk-rs` and
`kerf-sdk-ts` — non-Python SDKs that the skip does not match. The install aborts
at step 3 of 5. **This is an upstream bug in `vul-os/kerf`, and it is the thing
to fix; it is not something a registry recipe may work around.**

**Even had it succeeded, it is not an unattended install.** `setup.sh`'s own
closing instructions require the operator to:

1. *"Make sure Postgres 14+ is running, then create the database: `createdb kerf`"*
2. edit `kerf.toml` — `setup.sh` warns *"set `[database].url` and at least one
   `[llm.*] api_key` before starting"*
3. run migrations by hand
4. start `kerf-server` with `KERF_FRONTEND_DIST` and `KERF_CONFIG` set

and the README states that the `mech`/`full` solver stack (pythonOCC, dolfinx) is
**conda-only** and that *"`uv sync` currently doesn't work for any persona"*.

**The static-bundle escape hatch is also a fiction.** `kerf-frontend-0.1.9.tar.gz`
is a genuine built SPA (`index.html`, `planegcs.wasm`, `occt-import-js.wasm`,
root `./` so `archive_strip: 0`) and could be served by `python3 -m http.server`.
But its entry chunk references `/api/bootstrap`, `/api/config`, `/api/me`,
`/api/projects`, `/api/workspaces` (75 distinct `/api/*` paths across the
bundle). Served without the Python backend it is a shell that fails on first
load — the same lie as the current placeholder, in better clothes.

**`kerf-desktop-v0.1.9-linux-x64`** (190 MB) exists and `type: "desktop"` is a
supported shape, but nothing here verified that it starts, and an unverified
190 MB entry is not an improvement on an unverified 400-byte one.

### Recommendation for kerf

**Delete the `kerf` entry from `registry.json`** and re-sign. Do not move it to
`registry-unverified.json` either: quarantine is for an entry whose caveat can be
discharged by validation, and this one cannot be — there is no artefact to
validate. The absence is honest; the file `roadmap/CAD-KERF.md` already carries
the status. When `vul-os/kerf` publishes an artefact that installs unattended
(the natural candidate is a Docker image or a persona-scoped tarball whose
`setup.sh` works), a `vulos`-source entry can be written against it in an hour —
the pattern is now in `registry.d/vulos-first-party.json`.

---

## 5. Architecture: both arches exist, one recipe can carry one

`diwan` and `wede` both publish working `linux-arm64` binaries. Both were
boot-verified on `linux/arm64` in the same way as amd64. **They are not in the
staged entries**, because `VersionRecipe` has a single `download_url`
(`registry.go:219`) and the version key is a version, not an architecture. One
recipe cannot serve two architectures.

Declaring `arch: ["amd64"]` is therefore the honest expression of what these
recipes install, and it is not merely cosmetic — it is enforced twice:

- **at install**, `registry.go:964` → `ArchSupported(entry.Arch, SupportedArches())`
  refuses with *"cannot be installed on this box"*;
- **in the UI**, `RegistryListEntry.Installable` / `InstallableReason` render as
  *"requires amd64; this box is arm64"*.

An arm64 box is told the truth instead of being handed a binary it cannot `exec`.

**But Vulos ships arm64 images** (`build.sh --arm64` → rpi4, pinephone,
generic-arm64; `out-arm64/registry.json` exists), and `APP-RECIPE-STANDARD.md`
§3.4 says: *"An entry that appears in the App Hub on a box that cannot install it
is a bug of the same weight as an install that errors out."* Two first-party apps
that are simply unavailable on the arm64 image is a real gap.

**Recommendation (owner of `backend/services/appnet`): make `download_url` and
`checksum` resolvable per architecture** — the smallest change is to allow
`download_url`/`checksum` to be either a string or a `{"amd64": …, "arm64": …}`
map, resolved in `staticInstall` against `runtime.GOARCH`. Both fields are
already inside the signature, so the map is signed too. The arm64 digests are
recorded here so the follow-up needs no re-verification:

| artefact | sha256 |
| --- | --- |
| `diwan-linux-arm64` (v0.1.0) | `2e92fc0713227f7e3380a4ebe292768930b54e6d3e5d167aa2117ef3d7891ce5` |
| `wede-linux-arm64` (v0.1.3) | `73800e85e9577e2def5d62d59847e8333a3c030c07651c748675277aaeed6ae8` |

---

## 6. Defects found in the shared install machinery

These are **report-only** — `registry.json`, `registry.go` and the frontend files
belong to other agents. Each was found while establishing the facts above, and
each is the same defect class this task was set to remove.

### 6.1 A `post_install` that fails is only a warning

`registry.go:1095` —

```go
if err := cmd.Run(); err != nil {
    log.Printf("[registry] post-install warning for %s: %v\n%s", appID, err, errOutput)
}
```

The `install` command's failure is fatal (`return fmt.Errorf("install command
failed: …")`). `post_install`'s is not. For every entry that writes its config in
`post_install` — `conduit`, `gitea`, `navidrome`, and both staged entries — a
failed `post_install` produces a **successful install of an unconfigured app**.
That is the dominant defect class living in the installer itself.
*Suggested fix: make it fatal, and roll back the app dir.*

### 6.2 `${PORT}` is not expanded in `post_install` — six entries rely on it

`${PORT}` is substituted only into `command`, at launch (`launcher.go:248`), and
`PORT` is placed in the environment only at launch (`launcher.go:278`). The
`post_install` environment is `os.Environ()` plus `APP_DIR` and `DATA_DIR`
(`registry.go:1091`) — **no `PORT`**. Six shipped entries use `${PORT}` in
`post_install` and are writing an empty (or, worse, the *server's*) port into
their config files:

`conduit@0.5.9` (port 6167), `gitea@1.22.0` (3000), `grafana@11` (3000),
`navidrome@0.54.5` (4533), `nginx@1` (8080), `transmission@4` (9091).

For conduit that means `port = ` in `data/conduit.toml`.

The staged entries avoid this by writing the port as a **literal**, which is
sound because `appPort` is taken verbatim from the recipe's own `port` field
(`backend/cmd/server/main.go:1853`) and is *not* pool-allocated — only the host
port is — so `${PORT}` at launch always equals that literal.
`scripts/verify-firstparty-artifacts.sh` asserts the two agree, and fails any
recipe that puts `${PORT}` in `post_install`.

### 6.3 `code-server` verifies a checksum and then ignores the result

`registry.json`, `code-server@4.100.2`:

```
… | sha256sum --status -c - 2>/dev/null || true && dpkg -i /tmp/code-server.deb …
```

`|| true` means a **checksum mismatch proceeds to `dpkg -i`**. The check is
decorative. This is a security defect, not a style one: an entry that appears to
pin its artefact does not.
*Suggested fix: drop the `|| true`, or move it to `download_url` + `checksum` and
let the engine verify.*

### 6.4 Four entries can never install

`download_url` set with an empty `checksum` is refused unconditionally by
`validateRecipeSecurity` (SECAUDIT2-H1). Four entries are in that state and will
fail every install attempt: `excalidraw@0.18.0`, `hoppscotch@2026.4.1`,
`memos@0.22.4`, `uptime-kuma@1.23.16`. They fail *closed*, which is the right
direction — but they are listed as installable.

### 6.5 Other swallowed failures

`|| true` / `|| (…)` in an install or post_install, i.e. failures reported as
success: `code-server@4.100.2` (§6.3), `kerf@latest` (the subject of this
document), `libretranslate@1.5.3`, `lutris@latest`, `steam@latest`,
`uptime-kuma@1.23.16`.

### 6.6 46 of 55 entries have `arch: null`

Already recorded as a defect in `APP-RECIPE-STANDARD.md` §1.1, restated here
because `ArchSupported` *is* enforced at install time (`registry.go:964`), so
`null` is not harmless: it means "all", and an amd64-only app declared `null` is
offered on arm64 boxes and fails at install.

### 6.7 Apps run as uid 65534, but the installer never chowns the app dir

`launcher.go:52-53` drops to `nobody`/`nogroup` (65534/65534) via `setpriv`
before exec. `InstallFromRegistry` creates `bin/ static/ data/` with mode `0755`
as root (`registry.go:985`) and there is no `Chown` anywhere in the install path
(the only `chown` in `appnet` is `flatpak.go:97`, for Flatpak `~/.var` dirs). A
process-type app therefore cannot write its own state directory. Every entry with
a writable `data/` is affected. The staged entries hand their writable
directories to 65534 in `post_install` as a stopgap; **the right fix is one
`os.Chown` of the app dir in the installer**, not a line repeated in 55 recipes.

---

## 7. Signing — exactly what is required

Established by reading `Makefile` (targets `sign-registry`, `check-release-key`,
`verify-registry`, `verify-registry-prod`), `backend/cmd/sign/registry.go`,
`backend/services/appnet/registry.go` and `keys/README.md`.

1. **Signature form.** Ed25519 over `signing.Canonical({"app_id": <id>, "entry":
   <entry with `signature` blanked})`, base64 into `entry.signature`
   (`registry.go:829-854`). The app id is inside the signed bytes, so an entry
   cannot be relocated to another slot. **Every** key is signed, including ones
   the Go structs do not model — they are round-tripped through
   `RegistryEntry.Extra` precisely so they fall inside the signature. Adding or
   editing *any* field invalidates the signature: re-sign, never patch.
2. **Which key.** The entry signature must verify under the release key that the
   shipped, root-signed certificate authorises. `keys/release-cert.json` names
   `release_pubkey = dbc913bf7b1e806bf2a1c9a146bde683ea514e0c8845639907f2d75781f7a71c`,
   `key_id = "release-2026-08"`, `not_after = 2027-08-03`. This is **production
   ceremony material, not the dev keypair** — which is why
   `verify-registry-prod` (`-require-prod-keys`) passes today.
3. **The private half is not in this repo.** `keys/release.priv.json` is a
   **leftover dev key**: its public half is
   `ba8b1e8be03cb3cdc23acbb40812239827f81f83e9c576934096abeeb51a7f01`, which is
   **not** what the shipped certificate authorises. `make sign-registry` would
   therefore be **refused** by `check-release-key` with *"its public half is
   ba8b1e8b…, but the certificate authorises dbc913bf…"* — a good refusal, and
   exactly the trap that target exists to catch. The matching private key lives
   in the ceremony vault at
   `/Users/pc/code/vulos/vulos-cloud/signing-vault/release.priv.json` (verified:
   its `public_key` equals the cert's `release_pubkey`).
4. **Therefore, to land these entries:**

   ```sh
   # 1. merge registry.d/vulos-first-party.json's "apps" into registry.json
   #    (and delete the kerf entry — §4)
   scripts/verify-firstparty-artifacts.sh          # artefacts still match
   make sign-registry RELEASE_PRIV=/Users/pc/code/vulos/vulos-cloud/signing-vault/release.priv.json
   make verify-registry-prod                       # must report 55/55, 0 skipped
   make publish-feed RELEASE_PRIV=<same key>
   scripts/verify-app-recipe.sh diwan              # product's own installer, in a container
   scripts/verify-app-recipe.sh wede
   ```

   `sign-registry` re-signs **every** entry with that one key — that is normal
   and is what the verify step then proves.

5. **The staged entries are deliberately unsigned** (`"signature": ""`).
   `VerifyEntrySignature` fails closed on an empty signature, so they are inert
   until step 4 is run. This was a choice, not an omission: the release key is a
   human, offline operation (`docs/KEY-CEREMONY.md`, *"CI never holds a private
   key"*), and in this trust model **the signature is the act of vetting**. An
   agent minting production trust material for entries no human has reviewed
   would hollow out REGISTRY-SIGN-01 while leaving every check green — the very
   failure this document is about. The key is on this machine; it was not used.

---

## 8. Icons — already correct, nothing to change

Icon resolution is keyed by app **id**: `ART` (`frontend/src/core/appArt.tsx`)
wins, else `APP_LOGOS` (`frontend/src/core/AppIcons.tsx`), else a first-party
glyph, else `/api/desktop/icon/<id>`, else a letter tile.
`registry.json`'s own `icon` / `icon_url` are **not** consulted.

**All three products are already wired, with the approved marks already
vendored** — verified byte-identical to each product's own `brand/logo.svg`:

| id | `AppIcons.tsx` | file in this repo | approved source | identical? |
| --- | --- | --- | --- | --- |
| `diwan` | line 64 `diwan: '/product-logos/diwan.svg'` | `frontend/public/product-logos/diwan.svg` (542 B) | `/Users/pc/code/vulos/diwan/brand/logo.svg` | **yes** |
| `kerf` | line 68 `kerf: '/product-logos/kerf.svg'` | `frontend/public/product-logos/kerf.svg` (379 B) | `/Users/pc/code/vulos/kerf/brand/logo.svg` | **yes** |
| `wede` | line 76 `wede: '/product-logos/wede.svg'` | `frontend/public/product-logos/wede.svg` (843 B) | `/Users/pc/code/vulos/wede/brand/logo.svg` | **yes** |

Diwan's is the founder **iwan** mark — the vaulted portal that becomes a `D`
under a quarter turn (`#1B130E` ground, `#D0471F` arch, `#0E8B86` threshold).
Kerf's is the saw-kerf split (`#0a0b0d` / `#FFD633`). Wede's is the woven
stripes with the coral slash (`#2A2A46` / `#EDEDF6` / `#FF5E57`). **None contains
an SVG `<text>` element**, so none is subject to the per-machine font hazard.
All three draw their own full-bleed plate, so correctly none is in `INSET_LOGOS`.

**Required action: none.** Nothing is to be drawn, refined or regenerated.

### 8.1 Proven against the gate, not assumed

`frontend/src/core/AppIcons.test.ts` is a hard gate on exactly this: it re-derives
its roster as `builtinRegistry ∪ defaultWebApps ∪ Object.keys(registry.json.apps)`
(line 144), then asserts a coverage floor, that **every** id resolves via `ART` or
`APP_LOGOS` specifically (line 180), and that no two simultaneously-reachable ids
render the identical icon. Merging a first-party entry without a real mark turns
that suite red. So the claim was tested rather than reasoned about:

The suite was run against a **simulated future `registry.json`** — `kerf` removed,
`diwan` added, `wede` replaced — without modifying the tracked file, by running
vitest from a scratch tree whose `../registry.json` is the simulated one.

```
baseline (registry.json as tracked)             Tests  9 passed (9)
future   (kerf removed, diwan+wede staged)      Tests  9 passed (9)
```

**The simulation was itself verified before it was trusted.** A control run
injected an extra app id carrying no art or logo; it must go red, and did:

```
× every statically-registered app id has its OWN bundled art or logo …
AssertionError: expected [ 'zzz-no-icon-fixture' ] to deeply equal []
```

Had the control passed, the simulation would have been reading the tracked
registry and proving nothing.

Two consequences worth recording:

- **Removing `kerf` from `registry.json` is safe.** `APP_LOGOS.kerf` becomes a key
  with no registered app. The `every referenced logo file exists` test iterates
  `APP_LOGOS` and only requires the *file* to exist, which it does, so the
  orphaned key is harmless. Leave the mark in place for when Kerf ships an
  installable artefact.
- **The catalog count after the merge is 55, not 56** — 55 today, minus `kerf`,
  plus `diwan`; `wede` is a replacement, not an addition. Well clear of the
  `catalogIds >= 50` floor.

The three marks were also checked against the test's own `ownPlateRadius`
predicate: each draws its own full-bleed plate (diwan 0.219, wede 0.219, kerf
0.188 of viewBox width), so correctly **none** belongs in `INSET_LOGOS`, and none
is listed there.

One optional tidy-up for whoever owns the frontend: `FULL_CATALOG_IDS` in
`frontend/src/dev/IconLab.tsx:65-83` is a hand-maintained snapshot of catalog ids
and is not asserted by any test, so it will silently drift when `diwan` is added.

---

## 9. The other siblings — who else belongs in an App Hub

Surveyed 26 sibling repos under `/Users/pc/code/vulos/`: README, git remote, and
`gh release list` / `gh release view` for real assets. Criteria, applied
strictly: **(a)** an application a person opens and uses, and **(b)** a published
release carrying a Linux-installable artefact.

### Qualify — recommend adding, after the same verification these two got

| repo | slug | release | linux artefact |
| --- | --- | --- | --- |
| **lilmail** | `vul-os/lilmail` | v1.14.0, 2026-08-06 | `lilmail_1.14.0_linux_amd64.zip` (7.3 MB), `linux_arm64.zip` (6.6 MB) |
| **slipscan** | `vul-os/slipscan` | v0.1.0, 2026-08-07 | `SlipScan_0.1.0_amd64.AppImage` (86.9 MB), `SlipScan_0.1.0_amd64.deb` (10.1 MB) |

lilmail is a database-free PIM (mail + calendar + contacts) with a server-rendered
web UI in one Go binary — the same shape as wede and diwan, and the closest thing
to a drop-in. slipscan is a Tauri **desktop** app, so it would be
`type: "desktop"` and would need the `command-executes` evidence a desktop entry
requires, not a port probe. **Neither is staged here**: this task's remit was the
three first-party entries, and adding an app without boot-verifying it is the
habit being corrected. They are the obvious next two.

### App-shaped, but nothing published — do not add

`aql`, `athar`, `beepbite`, `cackle`, `magnetite`, `molao`, `pango`, `kilio`,
`gitstate`, `vuna`, `wibbly`, `evermesh` — twelve repos with real UIs and zero
GitHub releases. There is nothing for a recipe to point at. An absent app is
honest.

### Not applications — do not add regardless of artefacts

| repo | why not |
| --- | --- |
| `basin` | Postgres-on-a-bucket engine — infrastructure, not something a person opens (has a real Linux tarball) |
| `llmux` | library-first LLM gateway; its dashboard is an operator surface (has Linux zips) |
| `patala` | its Linux artefact is a **C-ABI shared library**; README: *"there is no GUI"* |
| `openrate` | library-first; its only release asset is a **source zip** |
| `pier` | KOTVA broker; v0.3.0 exists but publishes **zero assets** |
| `kotva` | a specification |
| `envoir`, `soko` | pre-alpha protocol reference implementations, no releases |
| `zana`, `sirboard` | open **hardware** (FreeCAD / KiCad), not installable software |
| `dotgithub` | org metadata; ships no product code |
| `ductus` | not a git repository at all — an unlabelled Python spike |

---

## 10. Deliverables from this pass

| file | what it is |
| --- | --- |
| `registry.d/vulos-first-party.json` | staged, **unsigned** entries for `diwan` (new) and `wede` (replacement). Not `kerf`. |
| `scripts/verify-firstparty-artifacts.sh` | re-downloads every pinned artefact and re-hashes it; asserts checksum-present, arch-explicit, command-matches-artifact, port-wired, no-`${PORT}`-in-post_install, no-pipe-to-shell, no-swallowed-failure, unsigned-in-staging, and a coverage floor. `--self-test` runs 10 fixtures: 1 control green, 9 red, one per rule. |
| `roadmap/FIRST-PARTY-REGISTRY-TRUTH.md` | this document |

### Changes needed in files owned by others

1. **`registry.json`** — merge the two entries from
   `registry.d/vulos-first-party.json`; **delete** the `kerf` entry (§4); re-sign
   (§7). Expected count afterwards: **55** (55 today, minus kerf, plus diwan;
   wede is a replacement, not an addition) — verified in §8.1.
2. **`frontend/src/core/AppIcons.tsx` / `appArt.tsx`** — **no change required**
   (§8). Optionally add `diwan` to `IconLab.tsx`'s `FULL_CATALOG_IDS`.
3. **`backend/services/appnet/registry.go`** — §6.1 (fatal `post_install`), §6.7
   (chown the app dir), §5 (per-arch `download_url`).
4. **`registry.json` hygiene** — §6.3 (`code-server`'s ignored checksum) is the
   one to fix first; then §6.2's six entries, §6.4's four, §6.5, §6.6.
