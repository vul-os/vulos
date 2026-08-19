# App Hub catalogue — roadmap

> **Status: measured 2026-08-19.** The target is a curated catalogue of ~120
> desktop apps reachable in one click from the App Hub. This file is the todo and
> the policy record. Nothing here is shipped until it appears in `registry.json`
> **and** its claims are backed by a measurement named beside them.
>
> **`registry.json` has one writer.** Everything below is staged in
> `registry.d/*.json` fragments for that writer to merge. Nothing in this pass
> edited `registry.json`.

## Where the store is today, measured

`registry.json` carries **74 entries**. **55 are unsigned**, awaiting the
founder's offline ceremony — expected, and the reason no entry added since can be
install-tested (see *What "verified" means here*). **19 are `_disabled`**: withdrawn
by Vulos for a licensing decision, a dependency the image does not carry, or an
install nobody has run.

Of the 42 shipped entries that install from Flathub, **all 42 were checked against
Flathub's AppStream API** on 2026-08-19 — every id still exists and **none is
end-of-life**. That is the weaker of the two checks: 13 of them were additionally
resolved through a real `flatpak remote-info` per architecture, as part of the arch
fragment. The other 29 were not re-resolved through flatpak itself in this pass,
and the distinction is kept because "the API answered" and "flatpak resolved it
under the argv the installer runs" are not the same statement — QGIS is the
standing proof that the second can fail while the first succeeds.

## What is staged for merge

| Fragment | Entries | New apps | What it is |
|---|---|---|---|
| `registry.d/wave1-flatpak.json` | 20 | 20 | Wave 1: communication, gaming, emulation, media playback |
| `registry.d/wave2-creative-office.json` | 21 | 21 | Wave 2: audio, graphics, video capture, office |
| `registry.d/wave2-dev-net-science.json` | 27 | 27 | Wave 2: reading, development, networking, CAD/science |
| `registry.d/arch-declarations.json` | 13 | 0 | **Edits.** `arch` for the 13 shipped Flatpak entries that had none |
| `registry.d/apt-to-flatpak.json` | 12 | 0 | **Edits.** `flathub_verified`/`extra_data` added; obs-studio gains `network` |
| `registry.d/system-utilities.json` | 18 | 0 | **Edits.** google-chrome and vivaldi withdrawn under policy 1a |
| `registry.d/apt-retired.json` | 7 | 0 | **Edits.** steam's two invented permission strings corrected |
| `registry.d/vulos-native.json` | 15 | 0 | Already merged; its `_not_verified` caveat still stands |
| `registry.d/vulos-first-party.json` | 3 | 0 | Already merged (diwan, wede, lilmail) |

**68 new apps.** After merge `registry.json` holds **142 entries**.

**Merging `arch-declarations.json` fires a ratchet, in the good direction.**
`undeclaredArchCeiling` in `backend/services/appnet/arch_test.go` is 19. After the
merge the count is 6, and `TestRegistry_UndeclaredArchOnlyEverFalls` fails with
*"below the ceiling — lower it to 6"*: the test refusing to let its own bound go
slack. **Set `undeclaredArchCeiling = 6` in the same commit that merges the
fragment.** That is the ratchet tightening, not the anti-shrinkage relaxation the
standing rule forbids, and this pass deliberately did not make the edit itself.

## The catalogue — every listed id accounted for

119 ids are named in the catalogue list further down. None is now unaccounted for:

| | Count |
|---|---|
| **Shipped** in `registry.json` | 40 |
| **Staged** in a fragment | 68 |
| **Excluded — proprietary** (policy 1a) | 9 |
| **Excluded — dead id** (Flathub `is_eol`) | 2 |
| **Missing** | **0** |

**Excluded as proprietary**, each confirmed `LicenseRef-proprietary` in Flathub's
own AppStream data rather than assumed: Chrome, Vivaldi, Discord, Slack, Zoom,
Spotify, Steam, Obsidian, VS Code, Android Studio, Sober. Chrome and Vivaldi were
**shipping enabled** while this document recorded them as removed; they are now
withdrawn in `system-utilities.json`. VSCodium is included as the substitute for
VS Code, exactly as policy 1a says.

**Excluded as dead**: `org.duckstation.DuckStation` and
`com.github.iwalton3.jellyfin-media-player` are both `is_eol` on Flathub. Neither
has a live replacement id, so they are excluded as dead, not as policy.

## The catalogue — every listed id, with its state

Legend: **✓** in `registry.json` · **▲** staged in a fragment · **P** excluded, proprietary (policy 1a) · **P⊘** proprietary AND still holding a withdrawn entry in `registry.json` · **X** excluded, dead id (Flathub `is_eol` or 404) · **⚑** id corrected from the one previously listed · **†** x86_64-only, so `untestable-on-arm64` and unavailable on an arm64 box.

### Wave 1 — the first 40

**Flatpak / system management** — `com.github.tchx84.Flatseal` ✓ · `io.github.flattool.Warehouse` ✓ · `io.github.giantpinkrobots.flatsweep` ✓ · `net.nokyan.Resources` ✓

**Browsers** — `org.mozilla.firefox` ✓ · `com.google.Chrome` P⊘ · `org.chromium.Chromium` ✓ · `com.brave.Browser` ✓ · `io.gitlab.librewolf-community` ✓ · `com.vivaldi.Vivaldi` P⊘

**Communication** — `org.signal.Signal` ▲ † · `org.telegram.desktop` ▲ · `im.riot.Riot` ✓ · `com.discordapp.Discord` P · `com.slack.Slack` P · `us.zoom.Zoom` P · `com.rtosta.zapzap` ▲ · `info.mumble.Mumble` ▲ · `org.mozilla.thunderbird` ▲ ⚑ †

**Gaming & compatibility** — `com.valvesoftware.Steam` P⊘ · `com.heroicgameslauncher.hgl` ▲ † · `net.lutris.Lutris` ✓ † · `com.usebottles.bottles` ▲ † · `net.davidotek.pupgui2` ▲ · `com.github.Matoking.protontricks` ▲ † · `org.prismlauncher.PrismLauncher` ▲ · `org.vinegarhq.Sober` P · `io.github.benjamimgois.goverlay` ▲ · `com.github.mtkennerly.ludusavi` ▲

**Emulation** — `org.libretro.RetroArch` ▲ · `org.DolphinEmu.dolphin-emu` ▲ · `net.pcsx2.PCSX2` ▲ † · `net.rpcs3.RPCS3` ▲ · `org.ppsspp.PPSSPP` ▲ · `io.mgba.mGBA` ▲ · `org.duckstation.DuckStation` X

**Media playback** — `org.videolan.VLC` ✓ · `io.mpv.Mpv` ▲ · `io.github.celluloid_player.Celluloid` ▲ · `com.spotify.Client` P · `com.github.iwalton3.jellyfin-media-player` X

### Wave 2 — the remainder

**Audio production** — `org.audacityteam.Audacity` ✓ · `org.ardour.Ardour` ✓ · `io.lmms.LMMS` ✓ · `org.musescore.MuseScore` ▲ · `com.github.wwmm.easyeffects` ▲ · `org.mixxx.Mixxx` ▲

**Graphics & photo** — `org.gimp.GIMP` ✓ · `org.kde.krita` ▲ ⚑ · `org.inkscape.Inkscape` ✓ · `org.blender.Blender` ✓ † · `org.darktable.Darktable` ✓ · `com.rawtherapee.RawTherapee` ▲ · `org.kde.digikam` ▲ · `net.scribus.Scribus` ▲ · `com.github.PintaProject.Pinta` ▲ · `org.upscayl.Upscayl` ▲ † · `com.orama_interactive.Pixelorama` ▲

**Video & screen capture** — `org.kde.kdenlive` ✓ · `org.shotcut.Shotcut` ✓ · `com.obsproject.Studio` ✓ † · `fr.handbrake.ghb` ▲ † · `io.github.seadve.Kooha` ▲ · `org.openshot.OpenShot` ▲ · `com.dec05eba.gpu_screen_recorder` ▲

**Office & documents** — `org.libreoffice.LibreOffice` ✓ · `org.onlyoffice.desktopeditors` ▲ · `org.kde.okular` ▲ · `com.github.xournalpp.xournalpp` ▲ · `org.gnucash.GnuCash` ✓ · `fr.free.Homebank` ▲ · `com.github.jeromerobert.pdfarranger` ▲ · `org.cvfosammmm.Setzer` ▲ · `com.github.marktext.marktext` ▲ †

**Reading & notes** — `md.obsidian.Obsidian` P · `com.logseq.Logseq` ▲ · `org.standardnotes.standardnotes` ▲ · `com.calibre_ebook.calibre` ▲ · `org.zotero.Zotero` ▲ · `com.github.johnfactotum.Foliate` ▲

**Development** — `com.visualstudio.code` P · `com.vscodium.codium` ▲ · `dev.zed.Zed` ▲ · `com.jetbrains.IntelliJ-IDEA-Community` ▲ · `com.google.AndroidStudio` P · `io.dbeaver.DBeaverCommunity` ▲ · `org.gnome.meld` ▲ · `io.podman_desktop.PodmanDesktop` ▲ · `rest.insomnia.Insomnia` ▲ † · `org.wireshark.Wireshark` ▲ · `io.github.shiftey.Desktop` ▲ · `io.neovim.nvim` ▲ · `org.godotengine.Godot` ▲ · `io.github.dvlv.boxbuddyrs` ▲

**System utilities** — `org.qbittorrent.qBittorrent` ✓ · `de.haeckerfelix.Fragments` ✓ · `com.bitwarden.desktop` ✓ · `org.keepassxc.KeePassXC` ✓ · `org.gnome.World.PikaBackup` ✓ · `com.github.qarmin.czkawka` ✓ · `io.github.peazip.PeaZip` ✓ · `org.cryptomator.Cryptomator` ✓ · `io.gitlab.adhami3310.Impression` ✓ · `io.gitlab.metadatacleaner.metadatacleaner` ✓ · `com.github.tenderowl.frog` ✓

**Networking & remote** — `org.remmina.Remmina` ▲ · `com.rustdesk.RustDesk` ▲ · `org.filezillaproject.Filezilla` ✓ · `com.nextcloud.desktopclient.nextcloud` ▲ · `org.localsend.localsend_app` ▲ · `org.torproject.torbrowser-launcher` ▲ ⚑ †

**CAD, science & engineering** — `org.freecad.FreeCAD` ▲ · `org.kicad.KiCad` ✓ · `org.openscad.OpenSCAD` ▲ † · `com.ultimaker.cura` ▲ † · `org.qgis.qgis` ✓ · `org.octave.Octave` ✓ · `org.stellarium.Stellarium` ▲ · `org.kde.labplot` ▲ ⚑

## Seven bad ids, four of them found here

`io.lmms.lmms` → `io.lmms.LMMS` was the first, and the rate has not fallen. Three
more were found in this pass by resolving every id before writing it:

| Listed | Reality | Correct id |
|---|---|---|
| `org.mozilla.Thunderbird` | `is_eol: true` | `org.mozilla.thunderbird` (lower case) |
| `org.krita.krita` | 404 | `org.kde.krita` |
| `org.kde.labplot2` | `is_eol: true` | `org.kde.labplot` |
| `com.github.micahflee.torbrowser-launcher` | community namespace, no licence in AppStream | `org.torproject.torbrowser-launcher` |

Together with `org.cryptomator.Crypt`, `fr.romainvigier.MetadataCleaner` and
`org.raspberrypi.rpi-imager` from the earlier pass, that is **seven bad ids in
roughly 120**. Assume the same rate on anything added later.

**A multi-branch app needs its branch pinned.** `FlatpakInstall` runs with no
branch, so an app publishing more than one — QGIS ships `stable` *and* `lts` —
exits 1 on the bare id. QGIS is pinned `org.qgis.qgis//stable`. Wine publishes
seven branches and none is named plain `stable`, which is one of the three reasons
it is parked.

## What "verified" means here, and what it does not

Two different measurements, and they must not be confused.

**Resolvability and architecture: MEASURED, for every entry added or edited.**
`scripts/verify-flatpak-candidates.sh` resolves each id through a real
`flatpak remote-info flathub --arch=<arch>` in a `debian:trixie` container, once
per declared architecture, and compares the entry's `arch` and `flathub_verified`
against Flathub's API. Tallies, all with zero failures:

| Fragment | Checks |
|---|---|
| `wave1-flatpak.json` | 74 |
| `wave2-creative-office.json` | 102 |
| `wave2-dev-net-science.json` | 131 |
| `arch-declarations.json` | 65 |

Every entry in all four also had its recorded `flathub_verified` compared against
Flathub's verification API — 81 comparisons, all in agreement. The script's own
self-test induces nine failures to prove it can go red, and one TLS flake during
the arch run was re-run rather than recorded as a verdict (see below).

**Launch and render: NOT MEASURED, for anything added since the last signing.**
`scripts/verify-app-recipe.sh` exits 2 with *"entry has no signature"* before any
container starts. Every entry staged here is unsigned by construction, so **none
of them has been install-tested, and `roadmap/app-verification-ledger.json` still
holds only nine rows.** No entry in this pass claims otherwise.

**`registry.d/vulos-native.json`'s caveat stands unchanged and is repeated here
because it is easy to lose:** *NO INSTALL WAS RUN* for its 15 entries. Their
checksums were computed from downloaded bytes by a script, and their archive
layouts were read out of the archives — but `command`, `port` and `post_install`
are **unproven**. Nothing in this pass changed that, and nothing upgraded their
status.

**A flaky check that accuses is worse than one that fails.** The candidate
verifier reported *"inkscape: DOES NOT RESOLVE"* for an app that plainly exists,
because a TLS handshake error was not in its transient list and fell through to
the verdict branch. Re-run alone, inkscape resolved on both architectures. The
transient list now covers the transport.

### `untestable-on-arm64` — 17 entries

This Mac is arm64, and with CI off the table these cannot be install-tested here
at all. They are recorded as `untestable-on-arm64`, which is neither `untested`
nor `passed`, and **no result is inferred for them from metadata**:

`blender` · `bottles` · `cura` · `handbrake` · `heroic` · `insomnia` · `lutris` ·
`marktext` · `obs-studio` · `openscad` · `pcsx2` · `protontricks` · `signal` ·
`thunderbird` · `torbrowser-launcher` · `upscayl` · `wine`

Everything else added in this pass publishes **both** architectures, so it follows
a user onto either instance unchanged — the standing directive that every instance
is near a clone of the next, satisfied with no emulation at all.

## Policy decisions

**1 — Filter Flathub. Do not mirror it.**
Measured by reading manifests, not by inference: of everything examined, only
**Slack, Zoom, Spotify, VS Code and Android Studio** are `extra-data`. All five are
proprietary and excluded anyway. **Chrome** is extra-data too and was already known.
**Discord, Obsidian and Steam are NOT extra-data** — their manifests fetch vendor
payloads at Flathub *build* time — which settles the "unverified either way" list
this document used to carry. Vivaldi, Brave and Bitwarden are likewise not extra-data.

**1a — Proprietary apps are excluded, for now (founder call, 2026-08-15).**
Unchanged, and now actually applied: the two entries that contradicted it are
withdrawn. It costs the catalogue Steam, Chrome, Vivaldi, Discord, Slack, Zoom,
Spotify, Obsidian, VS Code and Android Studio. Gaming keeps Heroic, Lutris,
Bottles, ProtonUp-Qt, Prism Launcher and six emulators; development keeps VSCodium.

**2 — `verified` is a first-class field, and now something reads it.**
It was first-class in prose only. The twelve apt→Flatpak entries recorded publisher
verification in a note and carried no field at all, so a badge or a filter would
have treated all twelve as unverified. Every Flatpak entry now carries
`flathub_verified`, measured, and the candidate verifier **fails** when an entry's
flag disagrees with Flathub or is missing. Of the 68 staged apps, **26 have no
verified publisher** — all open source, packaged by a third party, and each says so
in its own `_note`.

**Still open, and it is a UI question, not a data one:** nothing in the frontend
reads `flathub_verified`. The field reaches the API as unmodelled `Extra` data.
Badging or filtering on it is unbuilt.

**3 — Per-app install strategy, chosen deliberately.** Unchanged.

**4 — Architecture is explicit.** 13 shipped entries still declared `arch: null`,
which `ArchSupported` reads as an unverified claim to every architecture Vulos
builds. All 13 are measured and staged; all 13 publish both. The 6 that remain
undeclared after that merge are all `_disabled` and none is installable.

**5 — Icons are never invented.** Unchanged, and unaddressed: the 68 staged apps
render as bare letter tiles until `ART`/`appArt.tsx` and `APP_LOGOS`/`AppIcons.tsx`
gain entries keyed by app id. Flatpak exports each app's official icon to the box
on install, and `/api/desktop/icon/<id>` is already a tier in the chain, so the
post-install case resolves itself; the **pre-install listing** is the gap.

**6 — Two sandboxes — and as of 2026-08-19 there is a bridge between them.**

`permissions` used to decide nothing for a Flatpak app. `FlatpakInstall` ran
`flatpak install` and stopped, so an app kept whatever its Flathub manifest asked
for — commonly `--filesystem=host` **and** `--share=network` — while its recipe
said `["filesystem"]` and the App Hub displayed that. A declared permission model
whose declaration has no effect is worse than none, because the manifest reads as
a sandbox to anyone reviewing it.

`backend/services/appnet/flatpak_permissions.go` now narrows an installed app to
what its recipe declares:

| Declared | Undeclared becomes |
|---|---|
| `network` | `--unshare=network` |
| `filesystem` | `--nofilesystem=host --nofilesystem=home` |
| `bluetooth` | `--disallow=bluetooth` |

`host` and `home` are negated specifically rather than wholesale, so an app's own
`xdg-download` survives — Element's attachments and Firefox's downloads keep
working.

**Measured, not argued.** `io.mpv.Mpv` (manifest: `shared=network;ipc`,
`filesystems=…;host:ro`) was installed in a container with a canary file in `/opt`.
Inside the sandbox: before, `/proc/net/dev` listed `eth0` and the canary was
readable; after the flags, `eth0` was gone and the canary was *"No such file or
directory"*; after `--reset`, both returned.

**The seven permissions this cannot enforce, with the reason each is refused
rather than faked** — enumerated in `unenforcedFlatpakPermissions`, with a test
that fails if any permission is neither enforced nor named there:

- `camera`, `microphone` — not separable in Flatpak's model. Capture and playback
  ride one `pulseaudio` socket; camera rides `--device=all` alongside `/dev/dri`
  and `/dev/input`. Enforcing them would silence playback and break controllers.
- `gpu` — would need `--nodevice=all`, which also removes `shm` (the shared-memory
  transport every streamed X11 app here uses) and `input`.
- `usb`, `background`, `notifications`, `storage` — not Flatpak concepts at all.

**The bridge only ever removes.** Nothing grants a permission the publisher
withheld. It is **fail-closed**: an app whose narrowing cannot be applied is
uninstalled, because leaving it is the original defect in a worse form.

**Consequence to expect, and the blast radius is not small.** Counted across the
merged catalogue: of **108 enabled Flatpak entries, 48 lose network** and **9 lose
host/home filesystem**. That is the model finally doing something, and it is
deliberate — GIMP, Inkscape, Audacity, Blender, Kdenlive, LibreOffice, KeePassXC,
Octave, GnuCash, Meld, Krita, Okular and the rest work on local files. The nine
that lose host/home are Element, Firefox, LibreWolf, Jitsi Meet, Impression,
Mumble, Signal, Telegram and ZapZap — none of which requests `host` in its Flathub
manifest anyway, so for them the negation removes nothing that was there.

Five entries were **corrected instead**, because the declaration was wrong about
the app rather than strict: **VLC** and **KiCad** gained `network` (network
streams; the Plugin and Content Manager), **OBS Studio** (streaming is what it is
for), and **Dolphin** and **PPSSPP** (netplay). Each correction is recorded in the
entry's own `_note` with the reason.

**Flatseal is the escape hatch**, and it is in this catalogue precisely so an owner
can restore something a recipe was too strict about. Anything discovered to be
genuinely broken should be fixed in the entry, not in the bridge.

**Two limits of the bridge, stated because neither is obvious.** It applies at
*install* time, so an app installed before this change keeps its old, wider
sandbox until it is reinstalled — there is no sweep over what is already on a
box. And it applies only to Flatpak apps: for a `type: web` or native entry the
`permissions` array still decides nothing, because Vulos's own per-app namespace
does not read it either.

**A permission string that is not a permission is now refused** (PERMS-01). Under
the bridge an unrecognised string matches no enforced name, so the access it was
meant to declare is *revoked*. `steam` declared `"display"` and `"audio"`, neither
of which Vulos has ever had.

## The apt → Flatpak conversion

**Closed in this pass:** the twelve converted entries now carry measured
`flathub_verified` and `extra_data` fields rather than prose; `obs-studio` declares
the `network` its purpose depends on; and the permission model those entries were
written against is now enforced rather than notional.

**Closed earlier and still true:** arch widened from `["amd64"]` to both for
ardour, darktable, gnucash, libreoffice, lmms, octave, qgis and shotcut — eight
apps that arm64 boxes could not previously install.

**Still open, and both are real losses:**

- **`blender` loses arm64.** Debian built it for arm64; Flathub does not, and
  blender.org publishes no Linux aarch64 build, so no `artifacts` entry can recover
  it. With apt gone as a vehicle there is nowhere else to go. An arm64 owner is
  told Blender is unavailable, and that is now true rather than a defect.
- **`wine` is parked with `_disabled` and has no expressible vehicle.** WineHQ ships
  `.deb`s and no portable tarball, and `org.winehq.Wine` publishes seven dated
  branches with no plain `stable`. **Bottles answers the "wine vs Bottles" question
  by being installable**: `com.usebottles.bottles` resolves on a bare id and is
  staged. Both are x86_64-only, as the Wine underneath them is.

## Vulos first-party

| App | State |
|---|---|
| `diwan` | Shipped. Both Linux binaries pinned per architecture with digests that agree with the release's own `checksums.txt` *and* GitHub's published digest. Installed and booted on both arches. |
| `wede` | Shipped, per-architecture, port 9090. |
| `lilmail` | Shipped, per-architecture, port 8090. |
| `kerf` | **Absent by decision, and it should stay absent.** Its recipe cloned `kerf-cad/kerf`, which does not exist, and swallowed the failure into a placeholder HTML page — so the install "succeeded" and the owner got a stub. Worse, `vul-os/kerf` v0.1.9 publishes `linux-x64`, `macos-arm64` and `macos-x64` archives with the **identical sha256**, so the Linux asset is not a Linux build. There is nothing here to pin until upstream publishes distinct artefacts. |

Other siblings — basin, openrate, slipscan, patala, evermesh, magnetite, molao,
zana, aql, pier, kotva — are still unassessed as box-owner apps, and none was
added speculatively in this pass.

## The registry CAN express a per-architecture download

This section previously recorded the opposite, and it is now stale in the good
direction. `VersionRecipe.Artifacts` is a `{arch: {download_url, checksum}}` map,
enforced by `ARTIFACTS-01`, and `ResolveArtifact` picks against the box's own arch.
`diwan` and `wede` ship both Linux binaries and are installable on arm64.

## Verification standard

Every claim needs the measurement that backs it named beside it. In force:

- **`install` shell strings are refused outright** (INSTALL-01). The fabricated
  `code-server` checksum piped into `|| true` cannot be expressed any more — not
  because a pattern list caught it, but because the shape is gone.
- **`post_install` may not fetch, and may not swallow its own failure**
  (POSTINSTALL-02/03), and a failure is **fatal with rollback** (POSTINSTALL-01).
- **`${PORT}` is exported to `post_install`, and a reference without a declared
  port is refused** (POSTINSTALL-04). It was not exported before, so `sh` expanded
  it to the empty string and exited 0: `nginx` wrote `listen ;`, `transmission`
  wrote `"rpc-port":`, and the installer reported success both times.
- **A `_disabled` entry is no longer offered** (DISABLED-01). It refused at install
  on every box while the hub showed an Install button for all 19.
- **Checksums are mandatory and verified** for every downloaded artefact, per
  architecture. Three entries still carry a `download_url` with an empty checksum —
  `excalidraw`, `hoppscotch`, `uptime-kuma` — and all three are `_disabled`, so they
  are refused twice over.

**Testing is local and sequential**, smallest first, deleted after each, with a
resumable ledger. **It has not run for anything in this pass** and cannot until the
signing ceremony.

## Open questions, not yet decided

- **Nothing reads `flathub_verified`.** Badge unverified publishers, or hold them
  out of the default catalogue? 26 of the 68 staged apps are affected.
- **Icons for 68 new apps.** Bare letter tiles until the frontend art tables gain
  them; the pre-install listing is the only real gap.
- **`/api/router/classify` ignores the registry.** `backend/cmd/server/routes_router.go`
  maps app ids to stream lanes from a **hardcoded table** that has not grown since
  the original catalogue. Every app added since — chromium, brave, librewolf, and
  all 68 staged here — falls through to the CPU-stream default, so `lane.needs_gpu`
  in a registry entry does not reach the router. RetroArch, Godot, FreeCAD, Zed and
  the emulators are the ones this costs.
- **Which apps the streamed model makes misleading.** Named per entry now rather
  than in the abstract: digiKam catalogues *the box's* disks, Kooha records *the
  box's* screen, GOverlay reports *the box's* frame timing, Wireshark captures *the
  box's* traffic, Remmina connects *out* from the box, and a USB device plugged into
  the client machine is invisible to all of them.
- **Wireshark cannot capture without privileges** the Flatpak sandbox does not grant.
- **BoxBuddy and Podman Desktop need distrobox/podman on the box.** Whether Vulos
  ships them is a separate decision from whether these entries exist.
