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
Proprietary apps on Flathub are packaged as **extra-data**: the Flathub package is a
thin manifest that downloads the vendor's real binary at install time. Chrome,
Spotify, Discord, Slack, Zoom, Steam, Obsidian and Vivaldi all work this way.
Mirroring them into a Vulos-run repo therefore does not carry the payload — the
client still contacts the vendor's servers and the vendor's terms still govern.
Vulos curates **which apps appear**, not where the bytes come from, and Flathub's own
signing stays intact.

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

**4 — Architecture is explicit.**
Vulos publishes **amd64 and arm64** images. Many Flathub apps are `x86_64`-only. An
entry that appears in the App Hub on an arm64 box and cannot install is a defect, so
`arch` is set from measurement, never assumption.

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
`com.brave.Browser` + · `io.gitlab.librewolf-community` + · `com.vivaldi.Vivaldi` + P X

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
`org.audacityteam.Audacity` ✓ · `org.ardour.Ardour` ↻ · `io.lmms.lmms` ↻ ·
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
`io.github.peazip.PeaZip` + · `org.cryptomator.Crypt` + ·
`io.gitlab.adhami3310.Impression` + · `org.raspberrypi.rpi-imager` + ·
`fr.romainvigier.MetadataCleaner` + · `com.github.tenderowl.frog` +

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
