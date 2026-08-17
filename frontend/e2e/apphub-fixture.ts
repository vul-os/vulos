/**
 * apphub-fixture.ts — the registry payload the App Hub E2E specs browse.
 *
 * Two halves, and the second is the point.
 *
 * REAL: 22 entries copied verbatim out of this repo's own `registry.json`, so
 * the widths, the word lengths and the badge mix the specs measure are the ones
 * a user actually sees. A fixture of "App One / App Two" would lay out cleanly
 * at every width and prove nothing — the real catalogue is where a 92-character
 * description meets a 176px column.
 *
 * ADVERSARIAL: entries for the states nobody designs and therefore nobody
 * checks — a name with no natural break point, an empty description, a missing
 * icon, an unlabelled category slug, an arch this box cannot run, a
 * multi-version picker. Each one existed as a possible server response before
 * it existed here; the registry simply has no example of it today, which is
 * exactly why the layout was never tested against one.
 */

/**
 * The BOX's verdict for one app, as services/appnet/arch.go's EvaluateArch
 * emits it on GET /api/store/registry.
 *
 * The App Hub renders these strings VERBATIM and composes no architecture
 * sentence of its own — that is the whole point of the field, and it is why the
 * fixture has to carry it: without an `availability` the hub makes no
 * compatibility claim at all, so a payload lacking it would render every card
 * as plainly installable and quietly delete the incompatible state these specs
 * measure.
 *
 * The WORDING below is transcribed from arch.go and the Go tests own it
 * (TestEvaluateArch_NoUnmeasuredClaimReachesTheUser sweeps every rung's copy).
 * What these specs measure is LAYOUT and CONTRAST — how a long badge and a long
 * sentence behave in a dragged 390px window — so what matters here is that the
 * strings are the right SHAPE and the right LENGTH, not that a copy edit on the
 * box is mirrored the same afternoon.
 */
export interface FixtureAvailability {
  state: 'native' | 'emulated' | 'other-instance' | 'unavailable'
  installable: boolean
  requires_emulation: boolean
  badge: string
  card_badge: string
  detail: string
  box_arch: string
  undeclared: boolean
  needs: string[]
}

export interface FixtureApp {
  id: string
  name: string
  type: string
  arch: string[]
  availability?: FixtureAvailability
  flatpak_id: string
  description: string
  category: string
  author: string
  icon: string
  vetted: boolean
  versions: string[]
  latest: string
  installed: boolean
  homepage: string
  license: string
  keywords: string[]
}

/** Entries lifted from registry.json — the real catalogue's real strings. */
export const REAL_APPS: FixtureApp[] = [
  {"id": "ardour", "name": "Ardour", "type": "desktop", "arch": ["amd64"], "flatpak_id": "", "description": "Professional digital audio workstation — multi-track recording, editing, mixing, and mastering", "category": "media", "author": "Paul Davis and The Ardour Community", "icon": "ardour", "vetted": true, "versions": ["latest"], "latest": "latest", "installed": false, "homepage": "https://ardour.org", "license": "GPL-2.0", "keywords": ["audio", "daw", "recording", "mixing", "mastering", "music", "studio"]},
  {"id": "blender", "name": "Blender", "type": "desktop", "arch": [], "flatpak_id": "", "description": "3D creation suite — modelling, animation, rendering, compositing, and video editing", "category": "media", "author": "Blender Foundation", "icon": "blender", "vetted": true, "versions": ["4"], "latest": "4", "installed": false, "homepage": "https://www.blender.org", "license": "GPL-3.0", "keywords": ["3d", "modelling", "animation", "rendering", "video", "compositing"]},
  {"id": "cinny", "name": "Cinny", "type": "web", "arch": [], "flatpak_id": "", "description": "Modern Matrix client — clean, fast web UI for chatting on any Matrix homeserver including a local Conduit instance", "category": "network", "author": "Cinny Contributors", "icon": "C", "vetted": true, "versions": ["4.12.1"], "latest": "4.12.1", "installed": false, "homepage": "https://cinny.in", "license": "AGPL-3.0", "keywords": ["matrix", "chat", "client", "messaging", "web", "element-alternative"]},
  {"id": "cockpit", "name": "Cockpit", "type": "web", "arch": [], "flatpak_id": "", "description": "Web-based server management — terminal, logs, processes, networking, storage, and system monitoring in one dashboard", "category": "system", "author": "Red Hat / Cockpit Project", "icon": "cockpit", "vetted": true, "versions": ["latest"], "latest": "latest", "installed": false, "homepage": "https://cockpit-project.org", "license": "LGPL-2.1", "keywords": ["admin", "server", "management", "terminal", "monitoring", "logs", "dashboard"]},
  {"id": "conduit", "name": "Conduit", "type": "service", "arch": ["amd64"], "flatpak_id": "", "description": "Lightweight Matrix homeserver written in Rust — RocksDB backend, federation-ready, single binary, runs on localhost. Ships as Continuwuity, the actively maintained continuation of Conduit/conduwuit.", "category": "network", "author": "Continuwuity Contributors (community continuation of Famedly's Conduit / conduwuit)", "icon": "C", "vetted": true, "versions": ["0.5.9"], "latest": "0.5.9", "installed": false, "homepage": "https://continuwuity.org", "license": "Apache-2.0", "keywords": ["matrix", "chat", "homeserver", "messaging", "federation", "self-hosted", "conduwuit", "continuwuity"]},
  {"id": "darktable", "name": "Darktable", "type": "desktop", "arch": ["amd64"], "flatpak_id": "", "description": "Photography workflow and RAW developer — non-destructive editing, colour grading, and print/export pipeline", "category": "media", "author": "Darktable Developers", "icon": "darktable", "vetted": true, "versions": ["latest"], "latest": "latest", "installed": false, "homepage": "https://www.darktable.org", "license": "GPL-3.0", "keywords": ["photography", "raw", "editor", "colour", "grading", "lightroom", "darkroom"]},
  {"id": "element", "name": "Element", "type": "desktop", "arch": [], "flatpak_id": "im.riot.Riot", "description": "Secure, decentralized messaging and collaboration client for the Matrix protocol — chat, voice, and video calls across any Matrix homeserver (works with a local Conduit instance or any public server)", "category": "network", "author": "Element (New Vector Ltd / Matrix.org Foundation)", "icon": "E", "vetted": true, "versions": ["latest"], "latest": "latest", "installed": false, "homepage": "https://element.io", "license": "AGPL-3.0", "keywords": ["matrix", "chat", "messaging", "client", "collaboration", "voice", "video", "cinny-alternative"]},
  {"id": "firefox", "name": "Firefox", "type": "desktop", "arch": [], "flatpak_id": "org.mozilla.firefox", "description": "Privacy-focused web browser by Mozilla — built-in DRM support for Netflix/Spotify, containers, and strong extension ecosystem", "category": "internet", "author": "Mozilla Foundation", "icon": "firefox", "vetted": true, "versions": ["latest"], "latest": "latest", "installed": false, "homepage": "https://www.mozilla.org/firefox", "license": "MPL-2.0", "keywords": ["browser", "web", "internet", "firefox", "mozilla", "privacy", "drm", "streaming"]},
  {"id": "gimp", "name": "GIMP", "type": "desktop", "arch": [], "flatpak_id": "org.gimp.GIMP", "description": "Professional image editor — layers, filters, brushes, and advanced photo manipulation tools", "category": "media", "author": "GIMP Team", "icon": "gimp", "vetted": true, "versions": ["latest"], "latest": "latest", "installed": false, "homepage": "https://www.gimp.org", "license": "GPL-3.0", "keywords": ["image", "editor", "photo", "graphics", "design", "painting"]},
  {"id": "gitea", "name": "Gitea", "type": "web", "arch": [], "flatpak_id": "", "description": "Lightweight self-hosted Git forge with issues, pull requests, CI/CD, and package registry", "category": "developer", "author": "Gitea", "icon": "gitea", "vetted": true, "versions": ["1.22.0"], "latest": "1.22.0", "installed": false, "homepage": "https://gitea.io", "license": "MIT", "keywords": ["git", "repository", "code", "version-control", "forge", "ci"]},
  {"id": "grafana", "name": "Grafana", "type": "web", "arch": [], "flatpak_id": "", "description": "Monitoring dashboards — visualize metrics from Prometheus, InfluxDB, PostgreSQL, and more", "category": "developer", "author": "Grafana Labs", "icon": "grafana", "vetted": true, "versions": ["11"], "latest": "11", "installed": false, "homepage": "https://grafana.com", "license": "AGPL-3.0", "keywords": ["monitoring", "dashboard", "metrics", "grafana", "visualization", "observability"]},
  {"id": "kdenlive", "name": "Kdenlive", "type": "desktop", "arch": [], "flatpak_id": "org.kde.kdenlive", "description": "Non-linear video editor — multi-track timeline, effects, transitions, and proxy editing", "category": "media", "author": "KDE Community", "icon": "kdenlive", "vetted": true, "versions": ["latest"], "latest": "latest", "installed": false, "homepage": "https://kdenlive.org", "license": "GPL-3.0", "keywords": ["video", "editor", "timeline", "effects", "rendering", "film"]},
  {"id": "keepassxc", "name": "KeePassXC", "type": "desktop", "arch": [], "flatpak_id": "org.keepassxc.KeePassXC", "description": "Offline password manager — AES-256 encrypted vault, TOTP, browser integration, auto-type", "category": "system", "author": "KeePassXC Team", "icon": "keepassxc", "vetted": true, "versions": ["latest"], "latest": "latest", "installed": false, "homepage": "https://keepassxc.org", "license": "GPL-3.0", "keywords": ["password", "security", "vault", "encryption", "totp", "manager"]},
  {"id": "kicad", "name": "KiCad", "type": "desktop", "arch": [], "flatpak_id": "org.kicad.KiCad", "description": "Electronic design automation — schematic capture, PCB layout, 3D viewer, and Gerber export", "category": "developer", "author": "KiCad Developers", "icon": "kicad", "vetted": true, "versions": ["latest"], "latest": "latest", "installed": false, "homepage": "https://www.kicad.org", "license": "GPL-3.0", "keywords": ["eda", "pcb", "schematic", "electronics", "circuit", "hardware", "cad"]},
  {"id": "libreoffice", "name": "LibreOffice", "type": "desktop", "arch": [], "flatpak_id": "", "description": "Full office suite — word processor, spreadsheets, presentations, and database with MS Office compatibility", "category": "productivity", "author": "The Document Foundation", "icon": "libreoffice", "vetted": true, "versions": ["26.2"], "latest": "26.2", "installed": false, "homepage": "https://www.libreoffice.org", "license": "MPL-2.0", "keywords": ["office", "word", "spreadsheet", "presentation", "document", "writer", "calc", "excel", "docx", "xlsx"]},
  {"id": "libretranslate", "name": "LibreTranslate", "type": "web", "arch": [], "flatpak_id": "", "description": "Offline machine translation server — translate text between languages locally with no data leaving your device", "category": "productivity", "author": "LibreTranslate", "icon": "🌐", "vetted": true, "versions": ["1.5.3"], "latest": "1.5.3", "installed": false, "homepage": "https://libretranslate.com", "license": "AGPL-3.0", "keywords": ["translation", "translate", "language", "nlp", "offline", "privacy"]},
  {"id": "navidrome", "name": "Navidrome", "type": "web", "arch": [], "flatpak_id": "", "description": "Self-hosted music streaming server — your own Spotify with web UI and Subsonic API", "category": "media", "author": "Navidrome Project", "icon": "🎵", "vetted": true, "versions": ["0.54.5"], "latest": "0.54.5", "installed": false, "homepage": "https://www.navidrome.org", "license": "GPL-3.0", "keywords": ["music", "streaming", "audio", "subsonic", "self-hosted"]},
  {"id": "qbittorrent", "name": "qBittorrent", "type": "desktop", "arch": [], "flatpak_id": "org.qbittorrent.qBittorrent", "description": "Feature-rich BitTorrent client — search, RSS, sequential download, IP filtering, and torrent creation", "category": "network", "author": "qBittorrent Project", "icon": "qbittorrent", "vetted": true, "versions": ["latest"], "latest": "latest", "installed": false, "homepage": "https://www.qbittorrent.org", "license": "GPL-2.0", "keywords": ["torrent", "bittorrent", "download", "p2p", "transfer"]},
  {"id": "syncthing", "name": "Syncthing", "type": "web", "arch": [], "flatpak_id": "", "description": "Peer-to-peer file synchronization with web UI — sync folders across devices without a cloud", "category": "productivity", "author": "Syncthing Foundation", "icon": "syncthing", "vetted": true, "versions": ["1"], "latest": "1", "installed": false, "homepage": "https://syncthing.net", "license": "MPL-2.0", "keywords": ["sync", "files", "p2p", "backup", "share", "folders", "web"]},
  {"id": "transmission", "name": "Transmission", "type": "web", "arch": [], "flatpak_id": "", "description": "Lightweight BitTorrent client with web UI — simple, fast, low resource usage", "category": "network", "author": "Transmission Project", "icon": "transmission", "vetted": true, "versions": ["4"], "latest": "4", "installed": false, "homepage": "https://transmissionbt.com", "license": "GPL-2.0", "keywords": ["torrent", "bittorrent", "download", "p2p", "transfer", "web"]},
  {"id": "vaultwarden", "name": "Vaultwarden", "type": "web", "arch": [], "flatpak_id": "", "description": "Bitwarden-compatible self-hosted password vault — end-to-end encrypted, supports all Bitwarden clients and browser extensions", "category": "system", "author": "dani-garcia", "icon": "🔑", "vetted": true, "versions": ["1.32.7"], "latest": "1.32.7", "installed": false, "homepage": "https://github.com/dani-garcia/vaultwarden", "license": "AGPL-3.0", "keywords": ["password", "vault", "bitwarden", "security", "encryption", "credentials", "secrets"]},
  {"id": "wede", "name": "Wede", "type": "web", "arch": [], "flatpak_id": "", "description": "Lightweight web-based code editor — edit files and projects from your browser with syntax highlighting", "category": "developer", "author": "Vulos", "icon": "wede", "vetted": false, "versions": ["latest"], "latest": "latest", "installed": false, "homepage": "https://github.com/vul-os/wede", "license": "MIT", "keywords": ["editor", "code", "web", "ide", "files", "syntax"]},
]

/**
 * The states the real catalogue happens not to contain.
 *
 * `installed: true` on two of them because "already installed" is a card state
 * with its own affordance, and a grid that only ever renders the "Get" variant
 * never proves the two align.
 */
export const EDGE_APPS: FixtureApp[] = [
  {
    // No spaces: `overflow-wrap` cannot break it, so a column narrower than the
    // word is the case that pushes a grid track past its container.
    id: 'longname',
    name: 'Hyperconvergent-Infrastructure-Orchestrator',
    type: 'service', arch: [], flatpak_id: '',
    description: 'One word with no break opportunity anywhere in it, which is what defeats a truncation rule that assumes spaces.',
    category: 'system', author: 'Vulos Test Corpus', icon: '', vetted: false,
    versions: ['latest'], latest: 'latest', installed: false,
    homepage: 'https://example.invalid/hyperconvergent-infrastructure-orchestrator',
    license: 'Apache-2.0', keywords: ['long', 'name'],
  },
  {
    // Empty description: the card's second line has nothing to render.
    id: 'nodesc',
    name: 'Quiet',
    type: 'web', arch: [], flatpak_id: '',
    description: '', category: 'productivity', author: '', icon: '', vetted: false,
    versions: ['latest'], latest: 'latest', installed: false,
    homepage: '', license: '', keywords: [],
  },
  {
    // 250 characters — the registry's own longest is 252.
    id: 'longdesc',
    name: 'Verbose',
    type: 'web', arch: [], flatpak_id: '',
    description: 'A deliberately long description that keeps going well past the point where any card could show all of it, so that the truncation rule is exercised rather than assumed, and so the line it truncates on is a line somebody has looked at.',
    category: 'developer', author: 'Vulos Test Corpus', icon: '', vetted: true,
    versions: ['latest'], latest: 'latest', installed: false,
    homepage: 'https://example.invalid/verbose', license: 'MIT', keywords: ['long'],
  },
  {
    // Category slug with no entry in CATEGORY_LABELS.
    id: 'unlabelled',
    name: 'Sundry',
    type: 'web', arch: [], flatpak_id: '',
    description: 'Sits in a category the label table has never heard of.',
    category: 'storage', author: 'Vulos Test Corpus', icon: '', vetted: false,
    versions: ['latest'], latest: 'latest', installed: false,
    homepage: '', license: 'MIT', keywords: ['storage'],
  },
  {
    // arch this box is not — renders the incompatible state.
    id: 'wrongarch',
    name: 'PowerOnly',
    type: 'desktop', arch: ['ppc64el'], flatpak_id: '',
    description: 'Built for an architecture this box does not run.',
    category: 'system', author: 'Vulos Test Corpus', icon: '', vetted: false,
    versions: ['latest'], latest: 'latest', installed: false,
    homepage: '', license: 'GPL-2.0', keywords: [],
  },
  {
    // Several versions — the only path that renders the version picker.
    id: 'manyversions',
    name: 'Cascade',
    type: 'desktop', arch: [], flatpak_id: 'org.example.Cascade',
    description: 'Ships a version picker, which nothing in the real registry does.',
    category: 'media', author: 'Vulos Test Corpus', icon: '', vetted: true,
    versions: ['1.0.0', '2.4.1', '3.0.0-rc.2', 'latest'], latest: 'latest', installed: false,
    homepage: 'https://example.invalid/cascade', license: 'MPL-2.0', keywords: ['versions'],
  },
  {
    id: 'installedone',
    name: 'Already Here',
    type: 'web', arch: [], flatpak_id: '',
    description: 'Installed already, so the card renders its installed affordance.',
    category: 'productivity', author: 'Vulos Test Corpus', icon: '', vetted: true,
    versions: ['latest'], latest: 'latest', installed: true,
    homepage: '', license: 'MIT', keywords: [],
  },
  {
    id: 'installedtwo',
    name: 'Also Here',
    type: 'desktop', arch: [], flatpak_id: '',
    description: 'A second installed app, so the Installed tab has more than one row.',
    category: 'media', author: 'Vulos Test Corpus', icon: '', vetted: false,
    versions: ['latest'], latest: 'latest', installed: true,
    homepage: '', license: 'GPL-3.0', keywords: [],
  },
]

/**
 * Stamp each entry with the verdict a box of `boxArch` would return.
 *
 * A transcription of EvaluateArch's decision order for the inputs a fixture can
 * express — no emulator installed, no sibling instance, no entry opted in — which
 * is the state of a plain box and the state every one of these specs is about.
 * The Debian/Flatpak spelling fold is applied here for the same reason it is
 * applied on the box: an entry declaring `x86_64` and a box calling itself
 * `amd64` are the same machine, and a fixture that got that wrong would render
 * an incompatible state the product does not have.
 */
const ALIASES: Record<string, string> = {
  x86_64: 'amd64', amd64: 'amd64', aarch64: 'arm64', arm64: 'arm64',
  i686: 'i386', i386: 'i386', armv7l: 'armhf', armhf: 'armhf',
}
const fold = (a: string) => ALIASES[a.trim().toLowerCase()] ?? a.trim().toLowerCase()

export function forBox(apps: FixtureApp[], boxArch: string): FixtureApp[] {
  const box = fold(boxArch)
  return apps.map((app) => {
    const needs = [...new Set(app.arch.map(fold))]
    // An UNDECLARED arch is permitted today — arch.go's migration policy, which
    // 19 shipped entries still rely on. It is not "runs anywhere"; it is
    // "nobody checked", and the hub prints "Not stated" for it.
    const native = needs.length === 0 || needs.includes(box)
    if (native) {
      return {
        ...app,
        availability: {
          state: 'native', installable: true, requires_emulation: false,
          badge: '', card_badge: '', detail: '',
          box_arch: box, undeclared: needs.length === 0, needs,
        },
      }
    }
    const needsStr = needs.join(' or ')
    // Flatpak CAN install a foreign-arch ref — measured — and is declined on
    // graphics grounds; anything else has no build to fetch at all. Two facts,
    // two sentences, exactly as the box tells them apart.
    const detail = app.flatpak_id
      ? `${app.name} ships for ${needsStr} only. This box is ${box} and could install the ` +
        `${needsStr} build, but it would bring its own ${needsStr} graphics libraries, which ` +
        `emulation cannot accelerate — so it is not offered here. It stays available on any ` +
        `${needsStr} instance you run.`
      : `${app.name} ships for ${needsStr} only, and this box is ${box}. No build is published ` +
        `for this box's architecture. It stays available on any ${needsStr} instance you run.`
    return {
      ...app,
      availability: {
        state: 'unavailable', installable: false, requires_emulation: false,
        badge: 'Not available on this box',
        card_badge: `Needs ${needs.join('/')}`,
        detail, box_arch: box, undeclared: false, needs,
      },
    }
  })
}

/** REAL + EDGE — the default payload for GET /api/store/registry, on an amd64 box. */
export const APPS: FixtureApp[] = forBox([...REAL_APPS, ...EDGE_APPS], 'amd64')

/** What GET /api/store/installed returns for the fixture above. */
export const INSTALLED = APPS.filter((a) => a.installed).map((a) => ({ id: a.id }))

/**
 * A catalogue several times the real one's size.
 *
 * "A hundred apps" is a layout state, not a performance one: it is where a grid
 * that reflows correctly for twelve cards starts to show its scroll container's
 * seams.
 */
export function manyApps(n: number): FixtureApp[] {
  const out: FixtureApp[] = []
  for (let i = 0; i < n; i++) {
    const base = APPS[i % APPS.length]
    out.push({ ...base, id: `${base.id}-${i}`, name: `${base.name} ${i}`, installed: false })
  }
  return out
}

/**
 * The same catalogue as an ARM64 box reports it, plus Lutris.
 *
 * Lutris is the honest x86_64-only stand-in: open source, kept by APP-CATALOG
 * policy 1a's own list of what remains in gaming, and genuinely x86_64-only on
 * Flathub. The famous x86_64-only names — Steam, Chrome, Spotify, Zoom — are
 * proprietary and therefore out of the catalogue entirely, which shrinks the
 * incompatible set without emptying it.
 *
 * Every entry is re-stamped for arm64 rather than reusing the amd64 verdicts:
 * a payload whose cards say "this box is amd64" while the spec calls it an arm64
 * box is a fixture that disagrees with its own premise, and the first person to
 * read a failure from it would spend the time on the wrong question.
 */
export const ARM_BOX_APPS: FixtureApp[] = forBox([
  ...REAL_APPS,
  ...EDGE_APPS,
  {
    ...REAL_APPS[0],
    id: 'lutris', name: 'Lutris', type: 'desktop',
    flatpak_id: 'net.lutris.Lutris', arch: ['x86_64'],
    description: 'Open gaming platform — install and manage games from many sources',
    category: 'games',
  },
], 'arm64')
