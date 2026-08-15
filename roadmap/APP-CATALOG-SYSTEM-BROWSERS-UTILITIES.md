# App catalogue — system management, browsers, system utilities

> **Status: staged, not merged, not signed. 2026-08-15.**
> Covers the three catalogue sections in `roadmap/APP-CATALOG.md` wave 1/2:
> Flatpak/system management (4), browsers (6), system utilities (12).
> Entries are staged in `registry.d/system-utilities.json`. **18 new entries; 3 of the
> 22 ids were already in `registry.json` and were verified rather than duplicated;
> 1 was rejected; 2 ids in the catalogue were wrong and are corrected here.**

## What was verified, and what was not

**Verified, per app, against Flathub itself on 2026-08-15 — no id written from memory:**

| Question | Method |
|---|---|
| Does the id exist? | `GET https://flathub.org/api/v2/appstream/<id>` → HTTP 200 |
| Which architectures? | `GET https://flathub.org/api/v2/summary/<id>` → `.arches` |
| Runtime + size? | same → `.metadata.runtime`, `.runtimeInstalledSize`, `.installed_size` |
| Flatpak sandbox? | same → `.metadata.permissions` (filesystems / devices / sockets / shared / session-bus / system-bus) |
| Licence? | appstream → `.project_license`, `.is_free_license` |
| End of life? | appstream → `.is_eol` |
| Publisher verified? | `GET https://flathub.org/api/v2/verification/<id>/status` → `.verified`, `.method` |
| extra-data? | **read the actual build manifest** at `raw.githubusercontent.com/flathub/<id>/master/<id>.{yaml,yml,json}` and grep for `type: extra-data` |
| Icon provenance? | upstream repo tree via the GitHub/GitLab APIs, then every candidate URL fetched and checked for HTTP 200 + `image/svg+xml` |

**Not verified — stated plainly:**

- **No app was installed or launched.** These are multi-GB desktop apps and this Mac is
  arm64. Real install-and-launch is `scripts/verify-app-recipe.sh`'s job.
- **No claim that an app is *usable* over the stream.** Sandbox grants and runtime
  versions are measured; the interaction quality of, say, a Chromium inside Xvfb + VP8
  is not.
- **`arch` is measured from Flathub's published branches, not from an arm64 install.**
  Every one of the 18 publishes both `aarch64` and `x86_64`, so `arch` is
  `["amd64","arm64"]` on all of them. If Flathub drops an arch later, this file is stale.
- **Icon *files* have not been downloaded into the repo.** This pass produces a
  specification and verified source URLs; §6 is the handover.

## 1. Three defects in the catalogue's own id list

These were found by resolving the ids, not by reading them.

| Catalogue id | Result | Action |
|---|---|---|
| `org.cryptomator.Crypt` | **HTTP 404 — does not exist on Flathub** | Corrected to `org.cryptomator.Cryptomator` (200, verified publisher, not EOL) |
| `fr.romainvigier.MetadataCleaner` | **`is_eol: true`** — project moved namespace | Corrected to `io.gitlab.metadatacleaner.metadatacleaner` (200, `is_eol: false`, verified by website) |
| `org.raspberrypi.rpi-imager` | **`is_eol: true` AND publisher unverified AND runtime `org.kde.Platform/6.9`** (older than the 6.10 everything else uses, so it pulls a third KDE runtime) | **REJECTED — not staged.** See §4. |

An EOL id installs fine and then silently stops receiving updates. That is the failure
mode this catalogue exists to prevent, so it is worth saying that all three were found
by a 200-check, and none of them by reading the list.

## 2. A correction to `APP-CATALOG.md` policy 1 — extra-data

`APP-CATALOG.md:36-37` states that "Chrome, Spotify, Discord, Slack, Zoom, Steam,
Obsidian and **Vivaldi** all work this way [extra-data]".

**Vivaldi does not.** Read from `flathub/com.vivaldi.Vivaldi/master/com.vivaldi.Vivaldi.yaml`:

```yaml
    sources:
      - type: file                                    # <- NOT extra-data
        url: https://downloads.vivaldi.com/stable/vivaldi-stable_8.1.4087.64-1_amd64.deb
        only-arches: [x86_64]
      - type: file
        url: https://downloads.vivaldi.com/stable/vivaldi-stable_8.1.4087.64-1_arm64.deb
        only-arches: [aarch64]
```

`type: file` is consumed at **Flathub build time**, so Flathub does hold and sign the
Vivaldi payload. Same for **Brave** (official release zip per arch) and **Bitwarden**
(GitHub release tarball per arch).

Only **Chrome** is genuinely extra-data, and that is confirmed rather than assumed
(`com.google.Chrome.yaml:98` and `:112`, `type: extra-data`, `url: dl.google.com/...`,
per-arch, with a `sha256` that pins Google's bytes but not Google's availability).

The distinction matters for the policy's own conclusion: for Vivaldi and Brave, Flathub's
signing *does* cover the payload, so the "a Flatpak install promises less than it appears
to" caveat applies to Chrome and not to them. It was 1 app in this batch, not 2.

**Recommendation:** amend `APP-CATALOG.md:36-37` to name Chrome only (of this batch), and
treat every other `P X` marking in the catalogue as unverified until its manifest is read
the same way. The `X` flags on Spotify/Discord/Slack/Zoom/Steam/Obsidian were **not**
checked by this pass and should not be trusted.

## 3. Publisher verification (new policy 2), measured

`GET /api/v2/verification/<id>/status`, all 22 ids in scope:

| Verified | method | Apps |
|---|---|---|
| ✅ | `login_provider` (github/gitlab) | Flatseal, Warehouse, Flatsweep, LibreWolf, PikaBackup, Czkawka, PeaZip, Impression, Frog |
| ✅ | `website` (DNS/well-known on the vendor domain) | Resources, Brave, qBittorrent ✓, KeePassXC ✓, Fragments, Bitwarden, Cryptomator, Metadata Cleaner |
| ✅ | `manual` | Firefox ✓ |
| ❌ | none | **Chromium, Google Chrome, Vivaldi**, ~~rpi-imager~~ |

The policy singles out "community-maintained packages of proprietary software" as the
category to scrutinise. In this batch that is exactly **Google Chrome** and **Vivaldi** —
unverified publisher *and* closed source, so nobody outside the packager can audit either
the manifest or the payload. **Chromium is also unverified but is not the same risk**: it
is built from public source on Flathub's builders, so the packaging is auditable even
though the packager is anonymous. That distinction should drive the badge, not the boolean.

Both are staged, with `flathub_verified: false`, `license: "Proprietary"` and a
description that says the vendor's servers and terms govern. Whether they are badged or
held out of the default catalogue is `APP-CATALOG.md`'s open question, and this pass does
not pre-empt it — but the data to decide is now in the entries.

## 4. The streamed model — which of these become misleading

`type: desktop` means the app runs **on the box** and is streamed into the browser
(`backend/services/stream/`). Four of the staged apps are materially misleading unless the
description says so, and all four now do:

1. **Impression** — the sharpest. It lists **the box's** block devices via
   `org.freedesktop.UDisks2`. A user reaching for it to flash a USB stick sitting in
   *their own laptop* will find that stick absent and the box's disks present. The
   consequence is not confusion, it is data loss.
2. **Fragments** — torrents land on the box's disk. Correct for a sovereign box; not what
   a desktop user assumes.
3. **Pika Backup** — backs up the box. This one is *right* by construction and is arguably
   the most valuable app in the batch.
4. **Resources / Czkawka** — report and delete against the box's filesystem.

**Rejected on exactly this ground: `org.raspberrypi.rpi-imager`.** It is EOL and
unverified, which alone disqualifies it, but the deciding factor is that it is a
raw-block-device writer with `devices: [all]` and `filesystems: [/media, /run/media]`,
streamed from a machine whose disks are the ones on offer — and it duplicates Impression,
which is maintained, verified, and reaches drives through UDisks2 + portals rather than
static device grants. Two image writers is one too many; the one to drop is the dead one.

**Not rejected but flagged: there is no disk *management* tool available at all.**
`org.gnome.DiskUtility`, `org.gnome.Disks`, `org.gnome.gparted`, `org.gparted.gparted` and
`org.kde.partitionmanager` **all return HTTP 404 from Flathub** — measured, not assumed.
That is not an oversight by Flathub: partitioning needs root and raw block access, which
is the thing a Flatpak sandbox exists to prevent. If Vulos wants partition management it
must ship it natively (Debian `gparted`/`gnome-disk-utility` in `build.sh`) or extend the
builtin `disks` app. **A Flathub entry cannot fill that gap and the catalogue should stop
implying one will.**

## 5. The two sandboxes (policy 6), and the Flatseal verdict

### What Vulos's own `permissions` array actually does

Measured across the backend, `VersionRecipe.Permissions` has **four** read sites and only
one of them grants anything:

- `backend/services/appnet/manifest.go:192-204` — validates the *strings* against
  `ValidPermissions`. Grants nothing.
- `backend/services/appnet/registry.go:1045` — copies recipe → `app.json`.
- `backend/cmd/server/main.go:729-734` and `:837-842` — `if p == "storage"`, the only
  behavioural branch, wiring `appGateway.AllowStorage`.

So **`storage` is enforced** (gateway header injection, genuinely default-deny at
`services/gateway/gateway.go:334-395`) and **`network`, `filesystem`, `camera`,
`microphone`, `bluetooth`, `usb`, `gpu`, `background`, `notifications` are decorative** —
no consumer anywhere. Concretely: every app gets an identical netns with identical egress
rules whether or not it declares `network` (`appnet/namespace.go:100`,
`launcher.go:238`); there is no chroot, bind-mount set or device cgroup, so `filesystem`
and the device permissions are granted to everything by construction.

Two supporting facts that make this worse than it reads:

- **The validator is off the hot path.** `AppStore.Installed()` → `ScanApps` →
  `LoadManifest` never calls `Validate()`, and `InstallFromRegistry` writes `app.json`
  without validating. Proof in the shipped file: `registry.json` gives `steam`, `lutris`
  and `wine` the permission strings `display` and `audio`, **neither of which is in
  `ValidPermissions`**, and nothing has ever complained.
- **No shipped app declares `storage`**, the one enforced string. So in the shipped
  configuration the permission system enforces nothing at all.

*(The 18 staged entries use only strings that are in `ValidPermissions` — checked
programmatically — so they do not add to the `display`/`audio` problem. They are
documentation of the measured Flatpak sandbox, and this file is the place that says so.)*

### What Vulos does to Flatpak's sandbox

**Nothing.** `backend/services/appnet/flatpak.go` is 119 lines. It runs
`flatpak install -y --noninteractive flathub <id>` (`:64`, system-wide, not `--user`),
`flatpak uninstall` (`:104`), and builds the run command as the bare string
`"flatpak run " + flatpakID` (`:117-119`). A repo-wide grep for `flatpak override`,
`--filesystem=`, `--nosocket`, `--unshare`, `--device=` finds **no call site** that
tightens or loosens any Flatpak sandbox, and **no code path connects
`VersionRecipe.Permissions` to Flatpak at all**.

So for every Flatpak app in the store, the effective sandbox is 100% whatever the Flathub
manifest ships — which is why §6 of every staged entry's `_note` records the measured
grants. They are the real ones.

### The third thing, which is the actual problem

A `type: desktop` app is **not** launched through the appnet sandbox. `stream/pool.go:416`
is a plain `exec.CommandContext` — **no netns, no uid drop, no mount namespace, no cgroup,
and the full server `os.Environ()` inherited**. The shipped unit
(`build.sh:1240-1249`) has no `User=`, so the server, and therefore every streamed desktop
app, **runs as root**. The codebase already admits this at
`stream/gaming_session.go:283-288`.

### Verdict: Flatseal is a requirement *and* a symptom — ship it, and do not let it close the ticket

**It is a requirement.** Today Flatseal is the **only** mechanism by which a box owner can
change any app permission, of any kind, anywhere in Vulos. The recipe array is decorative;
Vulos issues no overrides; there is no permission UI in the shell. If a Flatpak's sandbox
is wrong — too tight to open the user's files, or too loose — Flatseal is the entire
remedy. On a box with no GNOME Software and no other permission editor, removing it leaves
a user with a broken app and no recourse. The founder's reasoning holds exactly as stated.

**It works here, and that was checked rather than assumed.** Flatseal's own sandbox is
`filesystems: [xdg-data/flatpak/overrides:create, /var/lib/flatpak/app:ro,
xdg-data/flatpak/app:ro]` — it writes **user** overrides, and user overrides do apply to
the **system-wide** installs `FlatpakInstall` creates. One caveat to confirm on a real box:
it talks `org.freedesktop.impl.portal.PermissionStore`, and the Wayland stack's
`xdg-desktop-portal-wlr` is proposed for removal in `roadmap/DISPLAY-STACK.md` R1. Without
a portal implementation running, Flatseal's portal-permissions pane is inert while the
static-override pane still works.

**But it is a symptom, and it papers over the bigger hole.** Three gaps Vulos should close
itself, none of which Flatseal can reach:

1. **The recipe `permissions` array is a promise the product does not keep.**
   `docs/APPS.md` renders all ten permissions in a table with a "Grants" column. Nine grant
   nothing. Either wire them (translate to `flatpak override --filesystem=` / `--nosocket=`
   / `--device=` at install time — the natural home is `flatpak.go` beside
   `ensureFlatpakUserDirs`) or delete the column and say the sandbox is Flathub's. Shipping
   Flatseal *instead* of deciding converts a design gap into a support burden.
2. **Flatseal has no jurisdiction over half the desktop catalogue.** 13 of the 26 current
   `type: desktop` apps install via `apt-get`, not Flatpak. Those have no Flatpak sandbox
   for Flatseal to edit and no Vulos sandbox either — they are root processes on the host.
   A permission UI that silently covers half the apps is worse than none, unless the shell
   says which half.
3. **The largest exposure is outside Flatpak entirely.** A streamed desktop app runs as
   root with the whole host filesystem and every block device. For a Flatpak, bwrap still
   contains it — that is real and worth having. For everything else, nothing does.

**Recommended framing for the open question at `APP-CATALOG.md:197-198`:** ship Flatseal
now because a user needs a repair tool today, and log a separate defect for the
`permissions` array. Do not let the presence of Flatseal be the answer to "does Vulos
control app permissions" — currently, it does not.

## 6. Icons — specification for whoever wires the files

**Nothing was drawn.** Every URL below was fetched and returned HTTP 200 with the
content-type shown.

Icon resolution is keyed by **app id**, and `registry.json`'s `icon` / `icon_url` are not
used for it (`icon_url` is read nowhere in the repo; `icon` is only the one-character
letter-tile seed). The relevant tiers in `frontend/src/core/AppIcons.tsx` are:
`hasArt(id)` → `APP_LOGOS[id]` → private glyph → `/api/desktop/icon/<id>` → **bare letter**.

> **This is a hard gate, not a cosmetic step.**
> `frontend/src/core/AppIcons.test.ts:179-182` asserts that every reachable app id —
> and the roster is re-derived from `Object.keys(registry.json.apps)`, not hand-listed —
> has an entry in `ART` **or** `APP_LOGOS`. **Merging these 18 entries without adding 18
> icons turns that test red.** `AppIcons.test.ts:65-70` additionally requires each
> `APP_LOGOS` URL to exist on disk, so the file must be committed, not just referenced.

Per app: save the file as `frontend/public/icons/<id>.svg`, add
`<id>: '/icons/<id>.svg'` to `APP_LOGOS` (`AppIcons.tsx:43`), and add a hue to
`APP_COLORS` (`AppIcons.tsx:160`). Do **not** hand-edit `INSET_LOGOS` — it is
machine-derived by `AppIcons.test.ts:72-88`.

### Upstream official SVG — preferred, 13 apps (all verified `200 image/svg+xml`)

| id | source URL |
|---|---|
| `flatseal` | `https://raw.githubusercontent.com/tchx84/Flatseal/master/data/icons/com.github.tchx84.Flatseal.svg` |
| `warehouse` | `https://raw.githubusercontent.com/flattool/warehouse/main/data/icons/hicolor/scalable/apps/io.github.flattool.Warehouse.svg` |
| `flatsweep` | `https://raw.githubusercontent.com/giantpinkrobots/flatsweep/main/data/icons/io.github.giantpinkrobots.flatsweep.svg` |
| `resources` | `https://raw.githubusercontent.com/nokyan/resources/main/data/icons/net.nokyan.Resources.svg` |
| `librewolf` | `https://gitlab.com/librewolf-community/branding/-/raw/master/icon/icon.svg` |
| `fragments` | `https://gitlab.gnome.org/World/Fragments/-/raw/main/data/icons/hicolor/scalable/apps/de.haeckerfelix.Fragments.svg` |
| `pika-backup` | `https://gitlab.gnome.org/World/pika-backup/-/raw/main/data/app.svg` |
| `czkawka` | `https://raw.githubusercontent.com/qarmin/czkawka/master/data/icons/com.github.qarmin.czkawka.svg` |
| `peazip` | `https://raw.githubusercontent.com/peazip/PeaZip/sources/peazip-sources/res/share/batch/freedesktop_integration/alternative-icons/svg/peazip.svg` |
| `cryptomator` | `https://raw.githubusercontent.com/cryptomator/cryptomator/develop/dist/linux/common/org.cryptomator.Cryptomator.svg` |
| `impression` | `https://gitlab.com/adhami3310/Impression/-/raw/main/data/resources/icons/hicolor/scalable/apps/io.gitlab.adhami3310.Impression.svg` |
| `metadata-cleaner` | `https://gitlab.com/metadatacleaner/metadatacleaner/-/raw/main/data/icons/io.gitlab.metadatacleaner.metadatacleaner.svg` |
| `frog` | `https://raw.githubusercontent.com/TenderOwl/Frog/master/data/icons/com.github.tenderowl.frog.svg` |

`brave` also has an upstream SVG in its Flathub packaging repo:
`https://raw.githubusercontent.com/flathub/com.brave.Browser/master/brave_lion.svg`
(verified 200) — prefer it over the PNG below.

### Flathub AppStream PNG — 5 apps with no upstream SVG (all verified `200 image/png`)

`chromium`, `google-chrome`, `brave`, `vivaldi`, `bitwarden`. The URLs embed a content
hash that changes when the app updates, so **re-resolve at download time** with
`GET https://flathub.org/api/v2/appstream/<flatpak-id>` → `.icon` rather than pasting a
snapshot. As of 2026-08-15:

```
org.chromium.Chromium   https://dl.flathub.org/media/org/chromium/Chromium/682a2b6f2969e4a19da7b69af2f9d617/icons/128x128/org.chromium.Chromium.png
com.google.Chrome       https://dl.flathub.org/media/com/google/Chrome/b385eea1b44c96758801ce1d76b3b110/icons/128x128/com.google.Chrome.png
com.brave.Browser       https://dl.flathub.org/media/com/brave/Browser/94f633aaad7b8a1567a2f56b38c54fc2/icons/128x128/com.brave.Browser.png
com.vivaldi.Vivaldi     https://dl.flathub.org/media/com/vivaldi/Vivaldi/55533477763ff67b77617568fb690498/icons/128x128/com.vivaldi.Vivaldi.png
com.bitwarden.desktop   https://dl.flathub.org/media/com/bitwarden/desktop/4a8d76590fdec22c773e4b7db7c4b834/icons/128x128/com.bitwarden.desktop.png
```

`@2` (256px) variants exist for each — append `@2` to the `128x128` path segment.

**Trademark, flagged not resolved.** `AppIcons.tsx:28-41` requires shipped icons to be
redistributable. The Chrome and Vivaldi marks are vendor trademarks, and Brave's lion is
too. The repo already ships `/icons/firefox.svg` and `steam`, so there is precedent for
using a vendor mark to identify that vendor's product — nominative use — but this pass is
not a legal opinion and the three should get a look before merge. The open-source 15 have
no such question.

**Not needed:** `/api/desktop/icon/<id>` (`backend/services/desktop/desktop.go:111-122`)
searches `/usr/share/icons` and `/usr/share/pixmaps` **only** — it does not search
`/var/lib/flatpak/exports/share/icons`, so it will not resolve a Flatpak's exported icon
even after install. That tier is not a fallback for these apps. Fixing it (add the Flatpak
exports dir to `findSystemIcon`'s search roots, keyed on `flatpak_id` rather than app id)
would be a real improvement but is out of scope here and is not a substitute for the
committed files, since the test at `AppIcons.test.ts:179-182` checks the maps, not the
network.

## 7. Runtime cost — measured, and one avoidable duplicate

Flatpak runtimes are shared, so the marginal cost of an app is small *if* it matches a
runtime already on the box. Measured `runtimeInstalledSize`:

| Runtime | Size | Apps in this batch |
|---|---|---|
| `org.gnome.Platform/50` | 1073 MB | Flatseal, Flatsweep, Resources, Fragments, PikaBackup, Impression, Metadata Cleaner, Frog |
| `org.freedesktop.Platform/25.08` | 659 MB | Chromium, Chrome, Brave, LibreWolf, Vivaldi, Bitwarden — **and the existing `firefox` entry** |
| `org.kde.Platform/6.10` | 1046 MB | PeaZip — **and the existing `qbittorrent` entry** |
| `org.gnome.Platform/49` | 1069 MB | **Warehouse, Czkawka — only these two** |

**The `/49` row is the finding.** Two apps pull an entire second GNOME runtime, ~1.07 GB,
for no user-visible benefit. Neither is blocked on it; both upstreams will move to `/50`
in the normal course. Options, in order of preference: (a) merge them and accept ~1 GB
until upstream rebases; (b) hold Warehouse and Czkawka for one catalogue revision and
re-check the runtime then; (c) ship them and add a size warning in the App Hub detail
panel, which does not exist today. **This is a founder/product call, not a curation call** —
the measurement is here so it can be made deliberately.

Note also `org.kde.Platform/5.15-25.08` (974 MB) already on the box for the existing
`keepassxc` entry, so a box that installs everything here carries **four** runtimes,
≈3.8 GB, before any app bytes. Chromium (462 MB), Bitwarden (494 MB), Brave (495 MB),
LibreWolf (475 MB) and Vivaldi (468 MB) are the large apps.

## 8. `arch` and `lane` — what was set, and why `lane` was deliberately left empty

**`arch`: `["amd64","arm64"]` on all 18, from measurement.** Every id publishes both
`aarch64` and `x86_64`.

But the field is weaker than it looks, and merging these should not imply otherwise:
`entry.Arch` has exactly **one** backend read — `registry.go:1165`, copying it into the
`GET /api/store/registry` response. Filtering happens only in a frontend `useCallback`
(`AppHub.tsx:341-344`) against `runtime.GOARCH` from `/api/packages/cache`.
`InstallFromRegistry` never checks it. **A direct `POST /api/store/registry/install`
installs an amd64-only recipe on an arm64 box and fails messily at the OS layer**, or, for
a `download_url` recipe, succeeds and leaves an unrunnable binary. That is a real
fail-open and it is not created by this batch — but this batch is the first to set `arch`
on entries where it is *correct*, which makes the missing server-side check easier to
mistake for a working one.

**`lane`: not set on any of the 18. This is deliberate.**

Two reasons, both measured:

1. **`lane` in `registry.json` is dead data.** It is not a field on `RegistryEntry`; it
   falls into the `Extra` passthrough that exists only to keep the signature lossless
   (`registry.go:117-124`). The live routing table is a hand-maintained Go map,
   `registryLaneFlags` at `backend/cmd/server/routes_router.go:30-129`. Writing
   `{"needs_gpu": true}` into JSON changes nothing at runtime and would be a decorative
   field that reads as a live one — the exact pattern this repo keeps getting caught by.
2. **The lane it would select is currently unsafe for these apps.** `needs_gpu` routes to
   `POST /api/stream/launch-app`, which does **not** set `UserID`
   (`main.go:2587-2599`), so `OwnerID == ""`, and `pool.go:776-778` hands an ownerless
   session to *any* authenticated caller — attach, observe, resize, and inject
   mouse/keyboard. That handler also skips `validateLaunchEnv` (only called at
   `pool.go:917` and `gaming_session.go:156`). Routing a signed-in **browser** or a
   **password manager** onto that path would be a privacy regression, not an optimisation.

The five browsers genuinely would benefit from hardware encode. **Follow-up, in this
order:** (a) fix the ownerless-session and env-validation gaps on
`/api/stream/launch-app`; (b) add the browsers to `registryLaneFlags` in
`routes_router.go` — the Go map, which is what actually routes; (c) then, if the JSON
field is to mean anything, make `ListEntries` read it and delete the duplicate map.

## 9. What the signing gate needs

`registry.d/system-utilities.json` is a **staging file**. It is not signed, is not
`registry.json`, and is not loaded by anything.

**Established by reading the code, then exercised:**

- The signature is Ed25519 over `signing.Canonical({"app_id": <id>, "entry": <entry
  without signature>})` — `appnet/registry.go:829-854`. The app id is bound in, so an
  entry cannot be moved to a different id without re-signing (M4).
- **Unmodelled fields are covered by the signature.** `RegistryEntry.Extra`
  (`registry.go:117-124`) round-trips them verbatim so `SaveRegistry` is lossless. This
  was exercised: after signing, `flathub_verified`, `extra_data` and `_note` survive
  intact on a throwaway copy — so the new policy-2 fields are signed, not decoration
  outside the signature.
- `verify-registry` additionally asserts **coverage** (`cmd/sign/registry.go:415`, every
  raw app id in the file was verified) and **cross-checks the quarantine file**
  (`:466`, disjoint + all unsigned).

**Exercised on a throwaway copy in the scratchpad — `registry.json` was never touched:**

```
sign-registry: signed and verified 73 entries in <scratchpad>/merged-registry.json
```

i.e. the 55 existing + these 18 sign and re-verify, so **the entry shape is correct and
these 18 will sign cleanly.**

**What must happen for them to verify, exactly:**

1. Merge the 18 into `registry.json` under `apps`. Coverage goes **55 → 73**; both
   `verify-registry` and `verify-registry-prod` count every id in the file, so a partial
   merge fails.
2. Run `make sign-registry` **with the key the shipped certificate authorises**. This is
   the blocker and it is not optional:

   ```
   $ make check-release-key
   ✗ REFUSING to sign with keys/release.priv.json: its public half is ba8b1e8be03cb3cd…,
     but the certificate authorises dbc913bf7b1e806b… (key_id "release-2026-08")
   ```

   **The tracked `keys/release.priv.json` is not the key `keys/release-cert.json`
   authorises.** So `make sign-registry` with the default `RELEASE_PRIV` refuses outright.
   The gate is doing its job — it is a real check, and it fired.

   Two ways forward, and they are not equivalent:
   - **Release path (correct):** `make sign-registry RELEASE_PRIV=/path/to/ceremony/release.priv.json`
     using the offline key for `release-2026-08`. Preserves every existing signature and
     keeps `verify-registry-prod` passing.
   - **Dev path (destructive):** `VULOS_DEV_KEYS_OVERWRITE=1 make dev-keys` then
     `make sign-registry`. This regenerates the trust anchor and cert and **re-signs all 73
     entries with a key derived from a published seed**, which `verify-registry-prod`
     refuses by design (`cmd/sign/main.go:219`). Acceptable for local work, never for a tag.

   **Do not use `VULOS_SIGN_ALLOW_KEY_MISMATCH=1`.** It exists, it would "work", and it
   would rewrite the tracked `registry.json` into a file no released image can verify.
3. `make verify-registry` → expect `73/73`. `make verify-registry-prod` on the release
   workflow, which additionally refuses the dev keypair.
4. Independently, add the 18 icons (§6) or `AppIcons.test.ts:179-182` goes red. Signing
   and icons are separate gates and both must pass.

Nothing here weakens, bypasses or stubs verification, and the staged file deliberately
carries `signature: ""` on every entry so a merge that skips step 2 **fails loudly**
rather than shipping unsigned entries.

## 10. Incidental defects found on the way

Recorded because they were measured during this pass, not because they are in scope.

1. **A transient `flatpak list` failure deletes app directories.** `flatpak.go:37-42`
   caches an **empty** map on error; `registry.go:1145-1151` reads "absent from the map"
   as "removed externally" and runs `os.RemoveAll(appsDir/<id>)`. Destructive on a
   transient error.
2. **Ownerless GPURoute stream sessions** — `main.go:2587-2599` omits `UserID`;
   `pool.go:776-778` then returns the session to any authenticated caller. Affects the
   existing blender / obs-studio / steam / lutris / wine entries today. See §8.
3. **`/api/stream/launch-app` skips `validateLaunchEnv`**, which the sibling
   `/api/stream/launch` calls at `pool.go:917` to block `LD_PRELOAD`-style injection.
4. **Invalid permission strings ship today.** `steam`, `lutris` and `wine` declare
   `display` and `audio`; neither is in `ValidPermissions` (`manifest.go:19-31`). They pass
   because the runtime load path never validates. See §5.
5. **`/api/desktop/icon/<id>` cannot resolve Flatpak icons** — `desktop.go:216-240`
   searches `/usr/share/icons` and `/usr/share/pixmaps` but not
   `/var/lib/flatpak/exports/share/icons`, which is where a Flatpak actually exports.
   `desktop.go` already scans the sibling `applications` dir for `.desktop` files, so the
   omission looks accidental.
6. **Licence drift in existing entries.** `registry.json` records `qbittorrent` as
   `GPL-2.0`; Flathub's AppStream says `GPL-3.0-or-later and OpenSSL`. `keepassxc` is
   recorded as `GPL-3.0`; Flathub says `GPL-3.0-or-later`. Not corrected here —
   `registry.json` belongs to the merge — but on a sovereignty product the licence field
   should match the upstream declaration.
