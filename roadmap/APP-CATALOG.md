# App Hub catalogue — roadmap

> **Status: in progress, 2026-08-15.** The target is a curated catalogue of ~120
> desktop apps reachable in one click from the App Hub. This file is the todo and
> the policy record. Nothing here is shipped until it appears in `registry.json`
> **and** has passed `scripts/verify-app-recipe.sh`.

## Where the store is today, measured

`registry.json` carries **55 apps**: 28 `type: web`, 26 `type: desktop`, 1 `service`.
Desktop apps are **streamed into the browser** (`backend/services/stream/`), not run
on the user's own machine — `lane` (`{"web"}`, `{"needs_gpu"}`, `{"game"}`) is set on
only 16 of the 55, so `type` alone does not tell you how an app reaches a user.

Of the 26 desktop apps, **13 install via Flatpak** and **13 via `apt-get`**:

| Flatpak today | apt today — being converted |
|---|---|
| audacity · element · filezilla · firefox · geany · gimp · inkscape · jitsi-meet · kdenlive · keepassxc · kicad · qbittorrent · vlc | ardour · blender · darktable · gnucash · libreoffice · lmms · lutris · obs-studio · octave · qgis · shotcut · steam · wine |

The Flatpak machinery is real and shipping: `backend/services/appnet/flatpak.go`
(`FlatpakInstall`, `FlatpakUninstall`, `InstalledFlatpaks`, `ensureFlatpakUserDirs`),
the `FlatpakID` recipe field, Flathub pre-registered by `build.sh`, and
`services/desktop` already scanning `/var/lib/flatpak/exports/share/applications`.

**Two first-party entries install nothing** and are being rewritten against real
releases: `kerf` swallows a failed `git clone` and writes a placeholder HTML page, so
the install *succeeds* and the user gets a stub; `wede` runs `/root/.local/bin/wede`
with no install step at all. `diwan` is absent from the registry entirely. All three
have published releases (`diwan v0.1.0`, `kerf v0.1.9`, `wede v0.1.3`).

## Policy decisions

**1 — Filter Flathub. Do not mirror it.**
Some proprietary apps on Flathub are packaged as **extra-data**: the package is a thin
manifest that downloads the vendor's real binary at install time, so mirroring it into
a Vulos-run repo carries no payload — the client still contacts the vendor's servers
and the vendor's terms still govern. Vulos curates **which apps appear**, not where the
bytes come from, and Flathub's own signing stays intact.

**Corrected 2026-08-15, by reading the manifests rather than assuming.** Of the apps
suspected of this, only **Chrome** is genuinely extra-data (`com.google.Chrome.yaml`
lines 98, 112). **Vivaldi, Brave and Bitwarden are not** — their manifests use
`type: file` against the vendor's download host per architecture, consumed at Flathub
*build* time, so Flathub does hold and sign those payloads. Spotify, Discord, Slack,
Zoom, Steam and Obsidian are **unverified either way** and must not be flagged as
extra-data until someone reads their manifests. The distinction matters: it decides
whether an app is redistributable and whether a mirror would ever work for it.

**1a — Proprietary apps are excluded, for now (founder call, 2026-08-15).**
No proprietary app ships in the catalogue at this stage. This is scoped as *for now* —
reversible, not a permanent position — and it resolves several open problems at once
rather than requiring each to be solved:

- **extra-data becomes moot.** The only confirmed extra-data app was Chrome, which is
  proprietary and therefore out.
- **the unverified-publisher problem shrinks to its mildest form.** Policy 2's
  scrutinise-hardest category was *community-maintained packages of proprietary
  software*. With proprietary out, what remains unverified is open source — packaged by
  a third party but auditable at source, e.g. Chromium.
- **the emulation burden shrinks.** The x86_64-only set is very largely the proprietary
  set, so most of what could not follow a user onto an arm instance is now out of scope
  anyway.

**What this removes**, and the second item is a real product cost worth stating rather
than burying: Chrome and Vivaldi from browsers; Discord, Slack and Zoom from comms;
Spotify from media; Obsidian from notes; VS Code from development (**VSCodium, the FOSS
build, stays and is the direct substitute**); and **Steam from gaming** — which takes
the flagship of that category with it, leaving Heroic, Lutris, Bottles, ProtonUp-Qt and
the emulators, all open source. If gaming is a priority, Steam is the entry to revisit
first when this call is reopened.

**2 — `verified` is a first-class field.**
Roughly a third of this catalogue has no verified publisher. Flathub exposes the flag
in its AppStream metadata and API. Unverified apps are either badged in the UI or held
out of the default catalogue. **Community-maintained packages of proprietary software
are the specific category to scrutinise** — that is where a supply-chain problem looks
most like a convenience.

**3 — Per-app install strategy, chosen deliberately.**
Because the catalogue is curated, each app gets the vehicle that is actually best —
Flathub, an official Debian repository, an official vendor package, a web app, or a
Vulos first-party release — recorded with the reason. Flathub is the default, not the
rule.

**4 — Architecture is explicit, and the hub shows what *this* box can install.**
Vulos publishes **amd64 and arm64** images. Many Flathub apps are `x86_64`-only. An
entry that appears in the App Hub on an arm64 box and cannot install is a defect, so
`arch` is set from measurement, never assumption — and the hub compares it against the
box's own architecture and makes the result visible.

The architecture that governs is **the box's, not the browser's**. Desktop apps are
streamed from the box, so a user on an ARM Mac connected to an amd64 box must be
offered amd64 apps. Anything derived from `navigator` or a client-controlled header is
wrong in exactly the way that looks right when tested on one machine.

Three naming schemes collide here and a comparison that mixes them matches nothing and
fails silently: Debian `amd64`/`arm64`, Flatpak `x86_64`/`aarch64`, Go's
`runtime.GOARCH` `amd64`/`arm64`. The registry uses the Debian spelling; normalisation
happens in exactly one place.

Unavailable apps are **shown with a reason rather than hidden**. An app that silently
vanishes produces "why can't I find Steam?"; an app marked unavailable for a stated
reason teaches the user something true about their hardware.

**5 — Icons are never invented.**
Founder directive. Icon resolution is keyed by app **id** into `ART`/`appArt.tsx` and
`APP_LOGOS`/`AppIcons.tsx`; `registry.json`'s own `icon`/`icon_url` are *not* used for
resolution, so a new app with no entry there renders a bare letter tile. A Flatpak
exports its official icon to `/var/lib/flatpak/exports/share/icons/…` on the box and
`/api/desktop/icon/<id>` is already a tier in the chain; Flathub's AppStream data
carries the official icon for the pre-install listing. First-party marks come from
`/brand`, copied and never redrawn.

**6 — Two sandboxes, and they are not the same thing.**
A recipe's `permissions` array plus Vulos's per-app network namespace is one model;
Flatpak's bwrap + portals + `--filesystem=` overrides is another. The mapping between
them is being established rather than assumed. **Flatseal exists in this catalogue
because of that gap** — on a non-GNOME OS with no other permission UI, a user has no
way to repair a broken sandbox without it.

## Verification standard — nothing lands unverified

Every entry must pass `scripts/verify-app-recipe.sh <app-id>`, which runs the **real
install path** in a `debian:trixie` container and asserts the app is genuinely present
and launchable — not that a command exited 0. The harness exercises the product's own
installer rather than re-implementing it, and ships a self-test proving it goes red on
a bad Flathub id, a wrong checksum and a missing command.

**Testing is local and sequential — deliberately not CI.** One app at a time,
**smallest first** by real measured download size, **deleted immediately after** its
assertions run so the next app starts from a known disk state. Progress is recorded in
a durable, resumable ledger — one row per app with the source, the date, the size, the
result, and what was actually asserted — so a later session skips what is already
verified instead of starting over.

**The arm64 limit is stated, not worked around.** This Mac is arm64, so x86_64-only
apps — Steam, Chrome, Spotify, Zoom, VS Code among them — cannot be install-tested
here, and with CI off the table they stay **untested**. The ledger records that as
`untestable-on-arm64`, distinct from `untested` and from `passed`. No emulation, no
pass inferred from metadata: a row saying "not tested, and here is why" is worth more
than a green tick that means nothing.

## Ids are case-sensitive, and a typo is a 404

`io.lmms.lmms` in an earlier revision of this list was wrong — the real id is
`io.lmms.LMMS`, caught by resolving it rather than reading it. Together with
`org.cryptomator.Crypt` (404), `fr.romainvigier.MetadataCleaner` (EOL) and
`org.raspberrypi.rpi-imager` (EOL), that is **four bad ids found in the first
~40 apps**. Assume the same rate across the remaining 100+ and resolve every id
before it is written into an entry.

**A multi-branch app needs its branch pinned.** `FlatpakInstall` runs with no
branch, so an app publishing more than one — QGIS ships `stable` *and* `lts` —
exits 1 on the bare id, while Flathub's API reports a single `"branch":"stable"`.
An entry built from API metadata therefore looks correct and fails at first
click. QGIS is pinned `org.qgis.qgis//stable`. Wine publishes seven branches,
none named plain `stable`.

## The registry cannot express a per-architecture download

Measured 2026-08-15, and it is the sharpest limitation found in the format.

`VersionRecipe.DownloadURL` is a single static string with a single `Checksum`,
and there is no `${ARCH}` substitution anywhere on the static-install path
(`services/appnet/registry.go:219`, `:1245`). The per-recipe `arch` field inside
a version block is **dead data** — `VersionRecipe` has no `Arch` field at all, so
only the entry-level `arch` exists, and it describes the whole entry.

**So every download-based app must pick one architecture**, and the consequence
landed on Vulos's own products first:

| App | Publishes | Staged entry | Result on an arm64 box |
|---|---|---|---|
| `diwan` | `linux-amd64` **and** `linux-arm64` | amd64 URL, `arch: ["amd64"]` | unavailable |
| `wede` | `linux-amd64` **and** `linux-arm64` | amd64 URL, `arch: ["amd64"]` | unavailable |

Both arm64 binaries exist and are published. Vulos ships an arm64 image. So a
user on the arm64 build of this OS is told the OS's own office suite and IDE are
not available for their machine — while the artefact sits in the release.

That is also a direct collision with the standing directive that **everything
syncs and each instance is almost a clone of the next**: a first-party app that
cannot follow a user onto their arm64 instance forks the app set by
architecture, which is precisely what emulation was meant to prevent. Here no
emulation is needed — only the ability to name a second URL.

**The fix is a format change, not an emulator**: per-architecture artefacts, each
with its own checksum, chosen against the box's own arch (which
`services/appnet/arch.go` already resolves and enforces at install time). The
recipe-standard work specified a recipe-level `arch []string` for related
reasons; this needs the URL and checksum to move with it. Until then, `diwan`
and `wede` must not be merged as amd64-only entries — that ships a false
"unavailable" to every arm64 owner.

**Genuinely x86_64-only, and unaffected by the proprietary exclusion:** `lutris`,
`obs-studio` and `wine` publish no aarch64 build on Flathub. These are the real
population for any emulation question — three FOSS apps, not the proprietary set.

## The catalogue — 120 apps

Legend: **✓** already in the registry as a Flatpak · **↻** in the registry via apt,
being converted · **+** new. Flags to be filled in by verification, never assumed:
`P` proprietary · `X` extra-data (vendor download at install time) · `?` publisher
verification unconfirmed.

### Wave 1 — the first 40

**Flatpak / system management (4)** — ship these or the store is broken
`com.github.tchx84.Flatseal` + · `io.github.flattool.Warehouse` + ·
`io.github.giantpinkrobots.flatsweep` + · `net.nokyan.Resources` +

**Browsers (6)**
`org.mozilla.firefox` ✓ · `com.google.Chrome` + P X · `org.chromium.Chromium` + ·
`com.brave.Browser` + · `io.gitlab.librewolf-community` + · `com.vivaldi.Vivaldi` + P

**Communication (9)**
`org.signal.Signal` + · `org.telegram.desktop` + · `im.riot.Riot` ✓ ·
`com.discordapp.Discord` + P X · `com.slack.Slack` + P X · `us.zoom.Zoom` + P X ·
`com.rtosta.zapzap` + · `info.mumble.Mumble` + · `org.mozilla.Thunderbird` +

**Gaming & compatibility (10)**
`com.valvesoftware.Steam` ↻ P X · `com.heroicgameslauncher.hgl` + ·
`net.lutris.Lutris` ↻ · `com.usebottles.bottles` + · `net.davidotek.pupgui2` + ·
`com.github.Matoking.protontricks` + · `org.prismlauncher.PrismLauncher` + ·
`org.vinegarhq.Sober` + · `io.github.benjamimgois.goverlay` + ·
`com.github.mtkennerly.ludusavi` +

**Emulation (7)**
`org.libretro.RetroArch` + · `org.DolphinEmu.dolphin-emu` + · `net.pcsx2.PCSX2` + ·
`net.rpcs3.RPCS3` + · `org.ppsspp.PPSSPP` + · `io.mgba.mGBA` + ·
`org.duckstation.DuckStation` +

**Media playback (5)**
`org.videolan.VLC` ✓ · `io.mpv.Mpv` + · `io.github.celluloid_player.Celluloid` + ·
`com.spotify.Client` + P X · `com.github.iwalton3.jellyfin-media-player` +

### Wave 2 — the remainder

**Audio production (6)**
`org.audacityteam.Audacity` ✓ · `org.ardour.Ardour` ↻ · `io.lmms.LMMS` ↻ ·
`org.musescore.MuseScore` + · `com.github.wwmm.easyeffects` + · `org.mixxx.Mixxx` +

**Graphics & photo (11)**
`org.gimp.GIMP` ✓ · `org.krita.krita` + · `org.inkscape.Inkscape` ✓ ·
`org.blender.Blender` ↻ · `org.darktable.Darktable` ↻ · `com.rawtherapee.RawTherapee` + ·
`org.kde.digikam` + · `net.scribus.Scribus` + · `com.github.PintaProject.Pinta` + ·
`org.upscayl.Upscayl` + · `com.orama_interactive.Pixelorama` +

**Video & screen capture (7)**
`org.kde.kdenlive` ✓ · `org.shotcut.Shotcut` ↻ · `com.obsproject.Studio` ↻ ·
`fr.handbrake.ghb` + · `io.github.seadve.Kooha` + · `org.openshot.OpenShot` + ·
`com.dec05eba.gpu_screen_recorder` +

**Office & documents (9)**
`org.libreoffice.LibreOffice` ↻ · `org.onlyoffice.desktopeditors` + · `org.kde.okular` + ·
`com.github.xournalpp.xournalpp` + · `org.gnucash.GnuCash` ↻ · `fr.free.Homebank` + ·
`com.github.jeromerobert.pdfarranger` + · `org.cvfosammmm.Setzer` + ·
`com.github.marktext.marktext` +

**Reading & notes (6)**
`md.obsidian.Obsidian` + P X · `com.logseq.Logseq` + · `org.standardnotes.standardnotes` + ·
`com.calibre_ebook.calibre` + · `org.zotero.Zotero` + · `com.github.johnfactotum.Foliate` +

**Development (14)**
`com.visualstudio.code` + P X · `com.vscodium.codium` + · `dev.zed.Zed` + ·
`com.jetbrains.IntelliJ-IDEA-Community` + · `com.google.AndroidStudio` + P X ·
`io.dbeaver.DBeaverCommunity` + · `org.gnome.meld` + ·
`io.podman_desktop.PodmanDesktop` + · `rest.insomnia.Insomnia` + ·
`org.wireshark.Wireshark` + · `io.github.shiftey.Desktop` + · `io.neovim.nvim` + ·
`org.godotengine.Godot` + · `io.github.dvlv.boxbuddyrs` +

**System utilities (12)**
`org.qbittorrent.qBittorrent` ✓ · `de.haeckerfelix.Fragments` + ·
`com.bitwarden.desktop` + · `org.keepassxc.KeePassXC` ✓ ·
`org.gnome.World.PikaBackup` + · `com.github.qarmin.czkawka` + ·
`io.github.peazip.PeaZip` + · `org.cryptomator.Cryptomator` + ·
`io.gitlab.adhami3310.Impression` + · 
`io.gitlab.metadatacleaner.metadatacleaner` + · `com.github.tenderowl.frog` +

**Networking & remote (6)**
`org.remmina.Remmina` + · `com.rustdesk.RustDesk` + ·
`org.filezillaproject.Filezilla` ✓ · `com.nextcloud.desktopclient.nextcloud` + ·
`org.localsend.localsend_app` + · `com.github.micahflee.torbrowser-launcher` +

**CAD, science & engineering (8)**
`org.freecad.FreeCAD` + · `org.kicad.KiCad` ✓ · `org.openscad.OpenSCAD` + ·
`com.ultimaker.cura` + · `org.qgis.qgis` ↻ · `org.octave.Octave` ↻ ·
`org.stellarium.Stellarium` + · `org.kde.labplot2` +

### Vulos first-party

`diwan` (absent — releases exist) · `kerf` (recipe installs a stub) ·
`wede` (recipe runs a binary nothing installs). Other siblings — basin, lilmail,
openrate, slipscan, patala, evermesh, magnetite, molao, zana, aql, pier, kotva — are
being assessed for whether they are box-owner apps at all rather than added
speculatively.

## Open questions, not yet decided

- **Which apps the streamed model makes misleading.** A disk utility streamed from a
  remote box manages *that box's* disks. Some entries in this list need that said out
  loud in their description, or they do not belong.
- **`wine` vs Bottles.** Bottles is the Flathub answer; swapping which app ships is a
  product decision, not a silent substitution.
- **Whether unverified publishers are badged or excluded** — badge and let the user
  choose, or hold them out of the default catalogue. Trust is the product's pitch.
- **The permission bridge.** Whether Vulos maps its recipe `permissions` onto Flatpak
  overrides itself, or defers entirely to Flatpak plus Flatseal.
