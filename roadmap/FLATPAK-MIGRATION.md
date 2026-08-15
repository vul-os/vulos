# apt → Flathub migration — the 13 desktop apps

> **Status: staged, not merged, 2026-08-15.** Entries live in
> `registry.d/apt-to-flatpak.json`. Verified with `scripts/verify-flatpak-candidates.sh`.
> Companion to `roadmap/APP-CATALOG.md`, whose six policy decisions this file applies.

## Scope change

The brief listed 13 apps. **Steam is out** — APP-CATALOG policy 1a excludes proprietary
apps from the catalogue for now, scoped as reversible. No Flatpak entry is produced for
`com.valvesoftware.Steam`. Its measurements are kept in the appendix so revisiting is
cheap rather than a fresh investigation. **Twelve apps remain.**

## What was measured, and what was not

Honest boundary, stated first because the rest of this document is only worth reading
if this line is trusted:

| Claim | Status |
|---|---|
| The Flathub app id exists | **Measured** — Flathub API v2, with a 404 control (`com.example.DefinitelyNotARealApp1234`) proving 200 means something |
| Published architectures | **Measured twice** — Flathub API `arches` **and** real `flatpak remote-info flathub --arch=<a> <id>` in a `debian:trixie-slim` container. Both agree on all 14 ids probed |
| The id resolves under the argv `FlatpakInstall` actually runs | **Measured** — real `flatpak install -y --noninteractive flathub <id>` |
| Publisher verification | **Measured** — Flathub API `verification/<id>/status` |
| Runtime, licence, sandbox grants | **Measured** — Flathub API `summary/<id>` `metadata` |
| Debian availability and per-arch binaries | **Measured** — `sources.debian.org` API and `packages.debian.org` for trixie |
| One conversion installs end-to-end | **Measured** — Ardour, real install on arm64 (see "Proof of one conversion") |
| The other eleven install and launch | **NOT verified.** Metadata does not prove a launch. They are gigabytes each, and this Mac is arm64 so the four x86_64-only apps cannot be install-tested locally at all — CI on `ubuntu-latest` covers those. Full proof is `scripts/verify-app-recipe.sh`'s job |
| The apps render correctly under GPU streaming | **NOT verified.** Requires a real box with a GPU |

Nothing below says "will work". Where this document says an app *should* work, the
reason is named so the claim can be attacked.

## The table

`V` = Flathub publisher verified. Arch is **measured**, in Debian spelling.

| app | Flathub id | verified how | arches | runtime | licence | V | lane | decision |
|---|---|---|---|---|---|---|---|---|
| ardour | `org.ardour.Ardour` | API 200 + `remote-info` both arches | amd64, arm64 | freedesktop 25.08 | GPL-2.0+ | no | — | **→ Flatpak**, arch widens |
| blender | `org.blender.Blender` | API 200; `remote-info` **fails on aarch64** | amd64 only | freedesktop 25.08 | GPL-3.0 | no | needs_gpu | **stays apt** |
| darktable | `org.darktable.Darktable` | API 200 + `remote-info` both | amd64, arm64 | GNOME 50 | GPL-3.0+ | no | needs_gpu | **→ Flatpak**, arch widens |
| gnucash | `org.gnucash.GnuCash` | API 200 + `remote-info` both | amd64, arm64 | GNOME 50 | GPL-2.0+ | **yes** | — | **→ Flatpak**, arch widens |
| libreoffice | `org.libreoffice.LibreOffice` | API 200 + `remote-info` both | amd64, arm64 | freedesktop 25.08 | MPL-2.0 | **yes** | — | **→ Flatpak** |
| lmms | `io.lmms.LMMS` | API 200 + `remote-info` both | amd64, arm64 | KDE 5.15-25.08 | GPL-2.0+ | no | — | **→ Flatpak**, arch widens |
| lutris | `net.lutris.Lutris` | API 200; `remote-info` **fails on aarch64** | amd64 only | GNOME 49 | GPL-3.0+ | **yes** | game | **→ Flatpak**, arch **corrected** |
| obs-studio | `com.obsproject.Studio` | API 200; `remote-info` **fails on aarch64** | amd64 only | freedesktop 25.08 | GPL-2.0+ | **yes** | needs_gpu | **→ Flatpak**, arch already right |
| octave | `org.octave.Octave` | API 200 + `remote-info` both | amd64, arm64 | **KDE _Sdk_ 6.10** | GPL-3.0+ | **yes** | — | **→ Flatpak**, arch widens |
| qgis | `org.qgis.qgis//stable` | API 200; bare id **ambiguous**, `//stable` resolves | amd64, arm64 | KDE 6.10 | GPL-2.0+ | **yes** | — | **→ Flatpak**, branch-pinned |
| shotcut | `org.shotcut.Shotcut` | API 200 + `remote-info` both | amd64, arm64 | KDE 6.11 | GPL-3.0-only | **yes** | needs_gpu | **→ Flatpak**, arch widens |
| wine | `org.winehq.Wine` | API 200; **7 branches, none `stable`**; x86_64 only | amd64 only | freedesktop 25.08 | LGPL-2.1+ | no | game | **stays apt** |

Ten convert. Two stay on apt. Five of the twelve have **no verified publisher**
(ardour, blender, darktable, lmms, wine) — all five are open-source projects whose
Flathub packages are community-maintained, which is a milder case than the category
APP-CATALOG policy 2 flags hardest (community packages of *proprietary* software).

## Two defects this work found, which metadata alone would not have

**1. `FlatpakInstall` cannot install a multi-branch app.**
`backend/services/appnet/flatpak.go` runs `flatpak install -y --noninteractive flathub <id>`
with no branch. QGIS publishes **two** branches:

```
$ flatpak install -y --noninteractive flathub org.qgis.qgis
Similar refs found for "org.qgis.qgis" in remote "flathub" (system):
   1) app/org.qgis.qgis/aarch64/stable
exit=1
```

Flathub's API reports a single `"branch": "stable"` for QGIS, so an entry built from
API metadata would look correct and fail at the first click. Fixed **in data, without
touching code**: the recipe carries `org.qgis.qgis//stable`, and the `//branch` suffix
was verified to resolve through the same unmodified argv. Wine has the same shape and
is worse — seven branches, none named plain `stable`.

**2. Two entries are offered on an architecture where they cannot install.**
`lutris` and `wine` both carry entry-level `arch: null`, which means *all arches*, while
their payloads are amd64-only. Entry-level `arch` **is** enforced — `registry.go:962`
refuses the install with a reason, and `registry.go:1215` marks the listing
`Installable: false` — so setting it correctly turns a failed install into an honest
"not available for this box". Both corrected to `["amd64"]`.

Related, and worth a separate fix by whoever owns the recipe schema: the **per-recipe**
`"arch": "amd64"` that sits *inside* the lutris/wine/steam version blocks is **dead
data**. `VersionRecipe` has no `Arch` field; it lands in the `Extra` passthrough map and
nothing ever reads it. It looks like protection and is not. That also means a
**dual-vehicle entry is not expressible** — you cannot ship "Flatpak on amd64, apt on
arm64" for one app, because version selection is not arch-aware. Every app gets exactly
one vehicle. That constraint drove the Blender decision below.

Also found: `"display"` (lutris, wine, steam) and `"audio"` (steam) are **not valid
permission strings** — `manifest.go:20-31` allows only `network, filesystem, camera,
microphone, bluetooth, usb, gpu, background, notifications, storage`. They survive today
only because `registry.go` writes `app.json` without calling `Validate()`. Dropped from
the entries I touched.

## The permission model — the honest version

This is the part most likely to ship a plausible-looking defect, so it is stated bluntly.

**Nothing maps recipe `permissions` onto Flatpak.** There is no code anywhere in the
tree that emits `flatpak override`, `--filesystem=`, `--device=`, `--share=` or
`--socket=`, and nothing writes `~/.local/share/flatpak/overrides/<id>` or
`/var/lib/flatpak/overrides/<id>`. The entire Flatpak surface is the 119 lines of
`flatpak.go`: list, install, uninstall, uninstall-unused, chown.

**And `permissions` barely does anything for *any* app, flatpak or not.** Of the ten
valid strings, exactly one — `storage` — has runtime effect (it scopes gateway
storage headers). The other nine are validated and then ignored: no namespace flag, no
bind mount, no device node, no cgroup device rule is keyed off them. The sandbox that
does exist (netns + veth + iptables, uid 65534, `CLONE_NEWNS`) is applied
**unconditionally and identically** to every process app, and is not parameterised by
permissions at all.

So `permissions` is documentation today, not enforcement. Converting these apps to
Flatpak does not *break* a permission bridge — **there is no bridge to break.**

**What this means for the GPU worry specifically.** The brief's concern was a
needs_gpu app that installs and then cannot reach `/dev/dri`. Measured, that failure
does **not** occur for these apps — but not for the reason the recipe implies. The
`"gpu"` permission produces nothing. `/dev/dri` comes entirely from what Flathub's own
manifest baked in, and every needs_gpu app here already has it:

| app | Flathub `devices` | reaches /dev/dri |
|---|---|---|
| blender | `dri` | yes (stays apt anyway) |
| darktable | `all` | yes |
| obs-studio | `all` | yes |
| shotcut | `all` | yes |
| qgis, gnucash, libreoffice, octave, lmms | `dri` | yes |
| ardour | `all` | yes |
| lutris | `all` | yes |

That is a real answer, and it is luck rather than design: it holds because upstream
packagers chose well, not because Vulos asked for it. If a future app ships a Flathub
manifest without `dri`, nothing in Vulos will notice or correct it.

**The mismatch runs the other way too, and this is the finding worth acting on.**
Several Flathub manifests grant *more* than the Vulos recipe declares. Measured:

| app | recipe declares | Flathub actually grants |
|---|---|---|
| gnucash | `filesystem` | `host` filesystem + **network** + dri |
| octave | `filesystem` | `host` filesystem + **network** + dri |
| libreoffice | `filesystem` | `host` filesystem + **network** + cups + dri |
| qgis | `filesystem`, `network` | `host` filesystem + network + dri |

Converting GnuCash, Octave and LibreOffice to Flatpak therefore **silently grants them
network access the recipe never asked for**, and `--filesystem=host` is considerably
broader than "filesystem" reads. For a product whose pitch is trust, an accounting app
quietly gaining network on an install that advertises `["filesystem"]` is the kind of
thing that should be a decision, not a side effect.

**Recommendation.** Wire the bridge, in one narrow direction: on Flatpak install, emit
`flatpak override --user <id>` derived from the recipe — at minimum `--unshare=network`
when the recipe does *not* declare `network`. That is a small, testable change to
`FlatpakInstall`, it makes `permissions` mean something for the first time, and it turns
the table above from a silent widening into an enforced contract. Until that exists,
the entries here are honest about what Flatpak grants but Vulos does not constrain it.
Flatseal (already scheduled in APP-CATALOG) is the user-facing escape hatch, not a
substitute for this.

One further note for whoever wires it: flatpak (desktop) apps do **not** currently go
through the netns launcher at all. The desktop path runs `flatpak run …` directly from
the stream pool with `Setpgid`, no `ip netns exec`, no uid drop, no cgroup. So the
"network namespace vs Flatpak" conflict the brief anticipated does not exist yet —
because the namespace is never applied to these apps in the first place.

## Per-app decisions that are product calls, not mechanics

**Blender stays on apt.** This is the one app where Flathub strictly loses capability.
`org.blender.Blender` is x86_64-only (`remote-info --arch=aarch64` → "Can't find ref"),
while Debian trixie ships `blender` 4.3.2 for **both** amd64 and arm64. Because a
dual-vehicle entry is not expressible (see above), converting means setting
`arch: ["amd64"]` and **deleting Blender from every arm64 box** to gain a newer version
on amd64. Losing the app entirely on an architecture Vulos ships is a worse defect than
shipping an older version. Revisit the moment Flathub publishes an aarch64 build.
Flagging this as founder-reversible: Blender users care unusually much about version
currency, so if arm64 Blender is judged not to matter, the conversion is one edit.

**Wine stays on apt, and Bottles is a separate entry — not a substitution.**
With Steam gone this is the only remaining gaming/compatibility question, so it is made
explicitly. Three options were weighed:

| option | verdict |
|---|---|
| `org.winehq.Wine` on Flathub | **rejected.** Publisher not verified; x86_64-only; publishes seven branches (`stable-21.08` … `stable-25.08`, `wow64-24.08`, `wow64-25.08`) and **none is named plain `stable`**, so the bare id fails under `FlatpakInstall` and any fix pins a dated branch that ages out and forces a re-sign every cycle |
| swap `wine` → `com.usebottles.bottles` | **rejected as a swap.** Bottles is verified (usebottles.com), GPL-3.0, ships a plain `stable` branch — a better package, but a *different product*: a prefix manager that wraps wine, not the wine runtime. Shipping it under the id `wine` would be exactly the silent substitution the brief warns against |
| keep `wine` on apt, add Bottles as its own entry | **chosen.** Debian trixie ships wine 10.0. `APP-CATALOG.md` already schedules `com.usebottles.bottles` as a separate wave-1 gaming entry, so the user gets both and picks |

Wine's entry arch is still corrected to `["amd64"]` — that defect is independent of the
vehicle. Note Bottles is also x86_64-only, so it must ship `arch: ["amd64"]`.

**LibreOffice version key `26.2` → `latest`.** A Flatpak tracks Flathub's stable
branch; a pinned version number in the key would assert a version Vulos does not
control. Matches every existing Flatpak entry.

**`io.lmms.LMMS`, not `io.lmms.lmms`.** `APP-CATALOG.md` line 130 lists the lowercase
form, which returns 404 — flatpak ids are case-sensitive. Worth fixing in the catalogue
before the rest of that list is worked, since the same class of typo is cheap to repeat.

**Lutris keeps `VULOS_GAMING=1`? No — dropped.** Nothing in the tree reads that variable
(grep over `backend/`, `frontend/`, `scripts/`, `build.sh` returns nothing), and a bare
`flatpak run` would not carry it into the sandbox regardless.

## Icons — no action needed, and nothing drawn

Resolution is keyed on the Vulos app **id**, in this order:
`ART[id]` → `APP_LOGOS[id]` → first-party glyph → `/api/desktop/icon/<id>` → letter tile.
`registry.json`'s `icon`/`icon_url` are **not** used for resolution (`icon_url` appears
in no `.ts`/`.tsx`/`.go` file at all; `icon` is passed only as the letter-tile fallback).

**All twelve already have a real bundled logo in `APP_LOGOS`** under
`frontend/public/icons/`, so every one resolves at tier 1 — correctly, *and before
install*, which is what the App Hub listing needs. **No icon work is required for this
migration and none was invented.**

Two things the next person should know rather than rediscover:

- **`/api/desktop/icon/<id>` cannot serve Flatpak icons.** `findSystemIcon`
  (`desktop.go:216-241`) searches only `/usr/share/icons/hicolor`,
  `/usr/share/icons/Adwaita` and `/usr/share/pixmaps`. The Flatpak export path
  `/var/lib/flatpak/exports/share/icons` is **not** in that list — it is scanned for
  `.desktop` files only. So the "a Flatpak exports its own icon and the endpoint picks
  it up" tier described in APP-CATALOG policy 5 **does not work today**. It is masked
  here purely because all twelve short-circuit at tier 1. Any *new* Flathub app added
  without a bundled logo will fall through to a bare letter tile.
- It also keys on the Vulos id treated as a freedesktop *icon-theme name*, with no
  translation layer, so `obs-studio` would not find `com.obsproject.Studio.png` even if
  the directory were searched.

Fixing that endpoint is a prerequisite for the 100+ new apps in APP-CATALOG, not for
these twelve.

## Signing — what must happen for these entries to verify

`registry.d/` fragments are **unsigned by construction** and nothing verifies them.
`registry.json` is the only signed app artifact. Every entry in the staging file carries
`"signature": ""` deliberately.

What is signed, precisely: Ed25519 (pure, no pre-hash, no domain separator) over
`signing.Canonical({"app_id": "<registry map key>", "entry": <entry with signature
zeroed>})` — canonical JSON with keys sorted byte-lexicographically at every level, no
whitespace, no trailing newline. The `signature` field is `omitempty`, so it vanishes
from the payload rather than appearing as `""`. The app id is bound into the payload, so
an entry cannot be re-keyed under a different app name. Unmodelled keys (`_note`,
`lane`) round-trip through `Extra` and **are** signed, so the `_note` text in the
staging file is part of the signed bytes.

**Required after merge:**

1. Merge the entries into `registry.json` (coordinator's step).
2. `make sign-registry` — re-signs **every** entry in sorted-id order and writes in
   place, then re-verifies from disk. Needs `keys/release.priv.json`, which is present
   in this tree (gitignored, 0600) and whose public half matches the shipped
   `keys/release-cert.json` (`release-2026-08`, expires 2027-08-03).
3. `make verify-registry-prod` — must report **57/57** if Bottles lands too, otherwise
   **55/55** unchanged (this migration adds no entries; it rewrites twelve).

Any byte changed after signing invalidates that entry. Note the failure mode is not
loud: a bad signature does **not** hide the app or fail the registry load — the entry
stays listed in the App Hub and is refused at *install* time (`registry.go:938-944`).
So "the App Hub looks fine" is not evidence that signing succeeded; only
`make verify-registry-prod` is. **No verification was weakened, bypassed or stubbed.**

## Proof of one conversion

Claiming twelve apps work on the strength of metadata is the overreach this project
keeps finding, so exactly one conversion was taken end-to-end: **Ardour**, chosen
because it is the smallest app that publishes an aarch64 build and can therefore run on
this Mac at all.

In a `debian:trixie-slim` arm64 container with flatpak and Flathub registered, the exact
unmodified `FlatpakInstall` argv — `flatpak install -y --noninteractive flathub
org.ardour.Ardour` — **exited 0**, and:

```
$ flatpak list --app --columns=application,arch,branch,origin
org.ardour.Ardour   aarch64   stable   flathub

$ ls /var/lib/flatpak/exports/share/applications/
org.ardour.Ardour.desktop

$ find /var/lib/flatpak/exports/share/icons -iname '*rdour*'
/var/lib/flatpak/exports/share/icons/hicolor/256x256/apps/org.ardour.Ardour.png
/var/lib/flatpak/exports/share/icons/hicolor/512x512/apps/org.ardour.Ardour.png
… (16x16, 22x22, 32x32, 48x48, plus mimetype icons)
```

So the app really installs on arm64, and Flathub's **own** official icon really is
exported onto the box. That last line also settles the icon question empirically rather
than by reasoning: the exported file is named **`org.ardour.Ardour.png`** — the *flatpak*
id — while `/api/desktop/icon/<id>` would be called with the Vulos id `ardour` and look
for `ardour.png`. Even with the Flatpak export directory added to `findSystemIcon`, that
tier would still 404 without an id→icon-name translation. Measured, not inferred.

**What this run did NOT prove:** `flatpak run` failed with
`bwrap: Creating new namespace failed: Operation not permitted`. That is Docker refusing
nested user namespaces under default seccomp, **not** an Ardour or Flathub defect — a
real box, or a container with `--privileged`, is required to exercise launch. So this is
proof of *install and export*, not of *launch*. The distinction is the whole point.

Two runs failed first on container DNS (`Could not resolve hostname`) under heavy host
load and were re-run with explicit resolvers — that is a network flake, **not** evidence
about the app, and it is called out here because a silent retry is how a bad measurement
becomes three bad conclusions.

Deliberate red controls, all confirmed to exit 1:

- `com.example.DefinitelyNotARealApp1234` → `Nothing matches … in remote flathub`
- `org.blender.Blender` on aarch64 → `Nothing matches` (the arch trap, live)
- bare `org.qgis.qgis` → `Similar refs found` / multiple branches (the branch trap, live)

`scripts/verify-flatpak-candidates.sh --self-test` induces all three and fails loudly if
they stop failing. Current result on the staged file:

```
SELF-TEST OK — the checks go red (5 failures induced deliberately)
verify-flatpak-candidates: 38 passed, 0 failed
```

The script found a bug in itself along the way, which is recorded because it is the same
shape as the defects this project keeps hitting: rows were delimited with a tab, and
**tab counts as IFS whitespace**, so bash collapsed a run of them and the two apt-only
entries had their `arch` value read as their flatpak id — producing the confident,
entirely wrong verdict `blender: Flathub has no app amd64,arm64`. A check that
misreports its own input is worse than no check. Delimiter is now `0x1f`.

## Appendix — Steam, kept for a cheap revisit

Excluded by policy 1a, measured before the exclusion landed:

- `com.valvesoftware.Steam`, **x86_64 only** (`remote-info --arch=aarch64` → no ref).
- Publisher **not verified** — a community-maintained package of proprietary software,
  precisely the category APP-CATALOG policy 2 says to scrutinise hardest.
- **extra-data confirmed by measurement**: Flathub reports a 35.5 MB download for a
  52.5 MB install, which is a thin manifest, not Steam. `metadata.tags == ["proprietary"]`.
  The real client is fetched from Valve at install time, so the Flathub entry does not
  make Steam self-contained, Valve's servers are still contacted and Valve's terms still
  govern.
- Sandbox: `devices: all`, `shared: network, ipc`, plus `/media`, `/run/media`, `/mnt`
  and `/run/udev:ro` — among the broadest in the catalogue.
- Its current registry recipe declares the **invalid** permissions `display` and `audio`.
- Debian's `steam-installer` is amd64-only too, so the arch answer is the same either way.
