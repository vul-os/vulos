// Apps with builtin:true render as React components in the shell.
// Apps with url/port are external services launched via the backend.
// Installed apps are fetched from /api/store/installed and merged dynamically.

const builtinRegistry = [
  // --- Built-in (React components in src/builtin/, no port needed) ---
  {
    id: 'terminal',
    name: 'Terminal',
    icon: '>_',
    description: 'System shell',
    keywords: ['terminal', 'shell', 'bash', 'sh', 'command', 'cli', 'console'],
    category: 'system',
    builtin: true,
  },
  {
    id: 'messages',
    name: 'Messages',
    icon: '✉',
    description: 'Peer-to-peer encrypted messaging',
    keywords: ['messages', 'chat', 'inbox', 'peering', 'dm', 'direct', 'messaging'],
    category: 'internet',
    builtin: true,
  },
  {
    id: 'activity',
    name: 'Activity Monitor',
    icon: '◉',
    description: 'System resource monitor',
    keywords: ['htop', 'cpu', 'ram', 'monitor', 'process', 'activity', 'task'],
    category: 'system',
    builtin: true,
  },
  {
    id: 'files',
    name: 'File Explorer',
    icon: '⊡',
    description: 'Browse files with semantic search',
    keywords: ['files', 'browse', 'folder', 'explorer', 'finder', 'search'],
    category: 'system',
    builtin: true,
  },
  {
    // FILES PHASE-2A: the canonical Drive UI over the OS Files control plane
    // (/api/files/*). Distinct from `files` (the local-FS explorer): `drive`
    // browses the user's per-user Drive — upload/download, folders, move/rename/
    // delete, sharing (user grants + expiring links), "Shared with me", versions.
    id: 'drive',
    name: 'Files',
    icon: '🗂',
    description: 'Your Drive — store, organize, share and version your files',
    keywords: ['drive', 'files', 'cloud', 'storage', 'documents', 'upload', 'share', 'folder'],
    category: 'productivity',
    builtin: true,
  },
  {
    id: 'assistant',
    name: 'Assistant',
    icon: '✦',
    description: 'Private AI over your mail — runs on your own server',
    keywords: ['assistant', 'ai', 'vula', 'mail', 'inbox', 'summarize', 'draft', 'triage', 'search', 'sovereign', 'private'],
    category: 'productivity',
    builtin: true,
  },
  {
    id: 'persona',
    name: 'Settings',
    icon: '⚙',
    description: 'System settings & AI config',
    keywords: ['settings', 'preferences', 'config', 'persona', 'ai'],
    category: 'system',
    builtin: true,
  },
  {
    id: 'apphub',
    name: 'App Hub',
    icon: '◈',
    description: 'Browse & install applications',
    keywords: ['apps', 'store', 'install', 'download', 'hub', 'marketplace', 'software'],
    category: 'system',
    builtin: true,
  },

  {
    id: 'disks',
    name: 'Disk Usage',
    icon: '◔',
    description: 'Storage analyzer with pie charts',
    keywords: ['disk', 'storage', 'space', 'usage', 'size', 'filesystem', 'du'],
    category: 'system',
    builtin: true,
  },
  {
    id: 'packages',
    name: 'Packages',
    icon: '⊞',
    description: 'System package manager',
    keywords: ['packages', 'apt', 'install', 'software', 'update', 'upgrade'],
    category: 'system',
    builtin: true,
  },
  {
    id: 'drivers',
    name: 'Drivers',
    icon: '⛁',
    description: 'Hardware devices & kernel modules',
    keywords: ['drivers', 'hardware', 'kernel', 'module', 'gpu', 'device', 'pci', 'usb'],
    category: 'system',
    builtin: true,
  },
  {
    id: 'dashboard',
    name: 'Dashboard',
    icon: '◈',
    description: 'Web publishing toggle and per-app resource usage',
    keywords: ['dashboard', 'publish', 'web', 'visibility', 'cgroups', 'resources', 'cpu', 'ram', 'public', 'instances', 'devices', 'cloud', 'online', 'offline', 'multiinstance'],
    category: 'system',
    builtin: true,
  },
  {
    id: 'authenticator',
    name: 'Authenticator',
    icon: '⊛',
    description: 'TOTP two-factor authentication codes',
    keywords: ['totp', 'otp', '2fa', 'authenticator', 'mfa', 'two-factor', 'security', 'codes'],
    category: 'system',
    builtin: true,
  },

  {
    id: 'vault',
    name: 'Vault',
    icon: '⊕',
    description: 'Password manager & credential store',
    keywords: ['vault', 'password', 'passwords', 'credentials', 'secrets', 'login', 'keychain', 'manager', 'security'],
    category: 'system',
    builtin: true,
  },

  // Mail is the gateway-proxied `lilmail` connector; Ofisi is the standalone
  // office app; Calendar/Contacts are standalone OS builtins over lilmail's /v1
  // (see the MAIL-PIM block below).
  //
  // Real-time comms (chat/video) are THIRD-PARTY now, not first-party OS apps:
  // Talk → Matrix (Cinny/Element), Meet → Jitsi Meet/Element Call. All four are
  // installable one-click from the App Store (registry.json: cinny, element,
  // jitsi-meet, element-call) — they are reached as external services, so no
  // `vulos-talk`/`vulos-meet` launcher tiles are registered here. The OS keeps
  // its own sovereign peer-to-peer `messages` builtin for direct encrypted
  // messaging.

  // Two user-selectable browsers, side by side:
  //
  //  1. "Smart Browser" (id: browser) — the client-side web app (apps/browser/),
  //     opens in the host browser as a WebApp lane entry, zero stream.Session.
  //  2. "Streaming Chrome" (id: browser-stream) — a REAL Chromium instance
  //     running on the box, streamed over WebRTC (Xvfb + GStreamer + pion), with
  //     a persistent PER-USER profile (cookies/history/logins). Restored from
  //     the services/webbrowser package; launched via POST /api/browser/launch.
  {
    id: 'browser',
    name: 'Smart Browser',
    icon: 'chrome',
    description: 'Web browser — opens in host browser (no streaming)',
    keywords: ['browser', 'web', 'internet', 'surf', 'chromium', 'smart'],
    category: 'internet',
    builtin: true,
    type: 'web',
    url: '/apps/browser/',
  },
  {
    id: 'browser-stream',
    name: 'Streaming Chrome',
    icon: 'chrome',
    description: 'Real Chromium streamed from your box, with your own persistent profile',
    keywords: ['browser', 'web', 'internet', 'chrome', 'chromium', 'streaming', 'stream', 'remote'],
    category: 'internet',
    builtin: true,
    // stream_browser marks the dedicated launch path in launchApp.js:
    // POST /api/browser/launch → per-user stream.Session → StreamViewer.
    stream_browser: true,
  },

  // --- Installed app services (have implementations in /apps/) ---
  {
    id: 'library',
    name: 'Universal Memory',
    icon: '☰',
    description: 'Notes & knowledge base',
    keywords: ['notes', 'write', 'knowledge', 'library', 'wiki', 'memory'],
    port: 80,
    category: 'productivity',
  },
  {
    id: 'gallery',
    name: 'Media Gallery',
    icon: '◫',
    description: 'Photos, videos & media',
    keywords: ['photos', 'video', 'media', 'gallery', 'images'],
    port: 80,
    category: 'media',
  },

  // --- Vulos Suite (UNIFIED-STORAGE) ---
  // The suite apps are separate products served as EXTERNAL, gateway-proxied web
  // apps: Browser → :8080/app/{id}/* → [auth] → app namespace. They open in an
  // in-shell iframe (type:'web' + url), exactly like the Smart Browser and the
  // default web apps. The gateway injects identity (X-Vulos-User-ID), integration
  // tokens, and — for apps that declare the "storage" permission in their app.json
  // — the per-user object-store credentials (X-Vulos-Storage-*, prefix "{id}/").
  //
  // `permissions: ['storage']` here documents which apps are storage-bearing; the
  // authoritative grant is the app's signed manifest, which the box gateway reads.
  {
    id: 'lilmail',
    name: 'Mail',
    icon: '✉',
    description: 'LilMail — IMAP/SMTP email. Mail lives on the IMAP server, so no object store is needed.',
    keywords: ['mail', 'email', 'inbox', 'compose', 'send', 'imap', 'smtp', 'lilmail'],
    category: 'internet',
    type: 'web',
    url: '/app/lilmail/',
    port: 80,
    // No storage permission: lilmail is IMAP/SMTP-backed (server is the store).
  },
  // WORKSPACE REMOVED: the standalone "Workspace" shell app is dead — the OS IS
  // the shell. Its launcher tile, gateway deep-link, and hub embedding are gone.
  //
  // BOARD FOLDED INTO OFISI: the standalone "Board" app is gone as its own
  // launcher tile — a whiteboard is now just another Ofisi document type
  // (docs/sheets/slides/pdf/whiteboards), so there is ONE productivity app.
  // Its keywords (whiteboard/canvas/draw/diagram) are folded into Ofisi above so
  // searching "whiteboard" still surfaces Ofisi.
  //
  // COMMS ARE THIRD-PARTY: Talk and Meet are no longer first-party built-ins.
  // Real-time chat/video is delegated to established third-party platforms
  // (Talk → Matrix/Element; Meet → Element-Call/Jitsi, final pick TBD), reached
  // as external services rather than shipped as OS apps. The OS keeps its own
  // sovereign peer-to-peer `messages` builtin for direct encrypted messaging.
  // MAIL-PIM (GNOME model): Calendar and Contacts are STANDALONE built-in OS
  // surfaces (React components in src/builtin/), the "GNOME Calendar/Contacts" of
  // Vulos. They read/write lilmail's stable /v1 (CalDAV/CardDAV + any OAuth-
  // connected Google/Outlook accounts lilmail aggregates) through the box's PIM
  // proxy — /api/pim/{calendar,contacts}/* — so mail credentials never reach the
  // browser (see backend routes_pim.go). lilmail is the Evolution-Data-Server;
  // these widgets are just the view. They own no object store of their own.
  {
    id: 'vulos-calendar',
    name: 'Calendar',
    icon: '⊡',
    description: 'Calendar — month + agenda, event CRUD over your connected calendars (CalDAV / Google / Outlook)',
    keywords: ['calendar', 'events', 'schedule', 'caldav', 'reminders', 'agenda', 'month'],
    category: 'productivity',
    builtin: true,
  },
  {
    id: 'vulos-contacts',
    name: 'Contacts',
    icon: '☻',
    description: 'Contacts — your address book over your connected accounts (CardDAV / Google / Outlook)',
    keywords: ['contacts', 'address', 'book', 'carddav', 'people'],
    category: 'productivity',
    builtin: true,
  },
]

// Default web apps shipped under apps/ — surfaced as launcher entries.
// Installed versions from /api/store/installed take priority (see getApps()).
const defaultWebApps = [
  {
    id: 'calculator',
    name: 'Calculator',
    icon: '≛',
    description: 'Standard and scientific calculator with history tape',
    keywords: ['calculator', 'math', 'scientific', 'arithmetic'],
    category: 'utilities',
    url: '/app/calculator/',
    port: 80,
    // ORIGIN-01: persists history/mode in localStorage at load. Storage now
    // arrives over the shell↔app bridge (core/AppBridge.js); this flag is inert.
    needsSameOrigin: true,
  },
  // UNIFIED-STORAGE de-dup: the standalone `calendar` web app is retired here
  // too — it duplicated both the (now-removed) `calendar` builtin's id and the
  // `vulos-calendar` suite app's concept. The canonical Calendar going forward
  // is the `vulos-calendar` tile above, which deep-links into the Mail app's
  // /calendar surface (Mail/PIM now serves Calendar). ORPHAN-FIX: the
  // `apps/calendar/` build dir has been deleted so it is no longer shipped.
  {
    id: 'clock',
    name: 'Clock',
    icon: '◷',
    description: 'Time, world clocks, stopwatch, and timer',
    keywords: ['clock', 'time', 'world clock', 'stopwatch', 'timer', 'alarm'],
    category: 'utilities',
    url: '/app/clock/',
    port: 80,
    // ORIGIN-01: reads world-clock config from localStorage at load (bridge-backed).
    needsSameOrigin: true,
  },
  {
    id: 'text-editor',
    name: 'Text Editor',
    icon: '⌨',
    description: 'Code & plain text editor with syntax highlighting, find/replace, and live collaboration',
    keywords: ['text', 'editor', 'code', 'syntax', 'developer', 'collab'],
    category: 'productivity',
    url: '/app/text-editor/',
    port: 80,
    // ORIGIN-01: persists theme/font/wrap/last-file in localStorage (bridge-backed).
    needsSameOrigin: true,
  },
  {
    id: 'weather',
    name: 'Weather',
    icon: '☀',
    description: 'Current conditions and 7-day forecast powered by Open-Meteo',
    keywords: ['weather', 'forecast', 'temperature', 'climate'],
    category: 'utilities',
    url: '/app/weather/',
    port: 80,
    // ORIGIN-01: reads unit/location prefs from localStorage at load (bridge-backed).
    needsSameOrigin: true,
  },
  // ORPHAN-FIX: these apps ship full builds under apps/ (app.json + server.py)
  // but were not registered anywhere, so they were unreachable. They are
  // first-party, structurally identical to the entries above (python server.py,
  // port 80, launched on demand via /api/apps/launch), so they are registered
  // here as launcher entries. None reads localStorage at load, so none opts into
  // a same-origin iframe (they run in the secure opaque-origin sandbox).
  {
    id: 'camera',
    name: 'Camera',
    icon: '⬤',
    description: 'Take photos and record videos with your device camera',
    keywords: ['camera', 'photo', 'video', 'capture', 'record', 'selfie'],
    category: 'media',
    url: '/app/camera/',
    port: 80,
  },
  {
    id: 'image-editor',
    name: 'Image Editor',
    icon: '✏',
    description: 'Edit images with crop, rotate, filters, and annotation tools',
    keywords: ['image', 'edit', 'photo', 'crop', 'filter', 'draw', 'annotate'],
    category: 'media',
    url: '/app/image-editor/',
    port: 80,
  },
  {
    id: 'music',
    name: 'Music Player',
    icon: '♪',
    description: 'Music player with library, playlists, album art and keyboard shortcuts',
    keywords: ['music', 'audio', 'player', 'playlist', 'mp3', 'flac'],
    category: 'media',
    url: '/app/music/',
    port: 80,
  },
  {
    id: 'phone',
    name: 'Phone',
    icon: '📞',
    description: 'Calls, SMS and modem management via ModemManager',
    keywords: ['phone', 'calls', 'sms', 'modem', 'telephony', 'cellular'],
    category: 'other',
    url: '/app/phone/',
    port: 80,
  },
  {
    id: 'screenshot',
    name: 'Screenshot',
    icon: '◉',
    description: 'Screenshot and screen recording — capture, annotate, and save',
    keywords: ['screenshot', 'screen', 'capture', 'record', 'annotate'],
    category: 'system',
    url: '/app/screenshot/',
    port: 80,
  },
  {
    id: 'system-info',
    name: 'System Info',
    icon: '💻',
    description: 'Live dashboard — OS, CPU, RAM, storage, GPU, network and uptime',
    keywords: ['system', 'info', 'about', 'hardware', 'cpu', 'ram', 'gpu', 'network', 'uptime'],
    category: 'system',
    url: '/app/system-info/',
    port: 80,
  },
  {
    id: 'video',
    name: 'Video Player',
    icon: '▶',
    description: 'Play mp4, webm and mkv videos with subtitles, queue, and picture-in-picture',
    keywords: ['video', 'player', 'mp4', 'webm', 'mkv', 'subtitles', 'media'],
    category: 'media',
    url: '/app/video/',
    port: 80,
  },
  {
    id: 'voice-recorder',
    name: 'Voice Recorder',
    icon: '⏺',
    description: 'Record audio from microphone with live waveform, playback, trim, and timestamped history',
    keywords: ['voice', 'record', 'audio', 'microphone', 'memo', 'sound', 'capture'],
    category: 'media',
    url: '/app/voice-recorder/',
    port: 80,
  },
]

// BUNDLE-01: default-everything (batteries-included, opt-out) app selection.
//
// At install/onboarding the user gets EVERYTHING pre-selected. A lean user can
// OPT OUT of Mail (declining the mail connector) and/or the productivity app
// bundle (→ drops the owned Ofisi app). This map records which tiles
// belong to which opt-out group so getApps() can hide them. The persisted flag
// for the productivity group is still named `workspace` for backend-contract
// compatibility, but there is no "Workspace" shell any more — the OS IS the shell.
//
//   - 'email'     — Mail (lilmail connector). Also the backend for the built-in
//                   Calendar/Contacts PIM widgets, which are always shown and
//                   degrade honestly to "Connect Mail" when no account is linked.
//   - 'workspace' — the owned productivity app (Ofisi/Docs, which now includes
//                   whiteboards as a document type; Board is no longer separate).
//
// Anything not listed here (Files/Drive, Assistant, Calendar, Contacts, Messages,
// the utilities and system apps) is always shown. Default (no selection
// persisted) ⇒ every group enabled, so this is invisible to existing installs.
const suiteBundleOf = new Map([
  ['lilmail', 'email'],
])

// suiteSelection mirrors GET /api/setup/apps. Defaults to everything-on so tiles
// are never hidden until the selection loads and an explicit opt-out is present.
let suiteSelection = { email: true, workspace: true, chosen: false }
let suiteFetchPromise = null

export function refreshSuiteSelection() {
  suiteFetchPromise = fetch('/api/setup/apps')
    .then(r => (r.ok ? r.json() : null))
    .then(sel => {
      // Only a real, chosen selection may hide tiles. Absent/invalid ⇒ all-on.
      if (sel && typeof sel === 'object') {
        suiteSelection = {
          email: sel.email !== false,
          workspace: sel.workspace !== false,
          chosen: !!sel.chosen,
        }
      }
      return suiteSelection
    })
    .catch(() => suiteSelection) // fail-open: never hide the suite on a fetch error
  return suiteFetchPromise
}

// suiteAppEnabled returns false only when the user explicitly opted a bundled app
// out during onboarding. Non-suite apps and the default (unchosen) state are true.
function suiteAppEnabled(id) {
  const bundle = suiteBundleOf.get(id)
  if (!bundle) return true
  if (!suiteSelection.chosen) return true // batteries-included until a choice is made
  if (bundle === 'email') return suiteSelection.email
  if (bundle === 'workspace') return suiteSelection.workspace
  return true
}

// Dynamic installed apps from backend
let installedApps = []
let fetchPromise = null
const builtinAliases = new Map([
  ['notes', 'library'],
])

export function refreshInstalled() {
  fetchPromise = fetch('/api/store/installed')
    .then(r => r.ok ? r.json() : [])
    .then(apps => {
      installedApps = (apps || [])
        .filter(a => !builtinRegistry.some(b => b.id === (builtinAliases.get(a.id) || a.id)))
        .map(a => ({
          id: a.id,
          name: a.name,
          icon: a.icon || a.name?.[0]?.toUpperCase() || '?',
          description: a.description || '',
          keywords: a.keywords || [],
          category: a.category || 'other',
          type: a.type || 'web',
          port: a.port || 80,
          command: a.command || '',
          workDir: a.work_dir || '',
          // SANDBOX-01: installed apps opt into same-origin iframes explicitly.
          needsSameOrigin: !!a.needs_same_origin,
          installed: true,
        }))
      return installedApps
    })
    .catch(() => { installedApps = [] })
  return fetchPromise
}

// AI5: Dynamic AI-generated apps from ~/.vulos/ai-apps/
let ai5AIApps = []
let ai5FetchPromise = null

export function refreshAIApps() {
  ai5FetchPromise = fetch('/api/ai-apps')
    .then(r => r.ok ? r.json() : [])
    .then(apps => {
      ai5AIApps = (apps || [])
        .filter(a => a.id && a.has_html === 'true') // only apps with a launchable HTML page
        .filter(a => !builtinRegistry.some(b => b.id === a.id)) // dedup against builtins
        .map(a => ({
          id: a.id,
          name: a.title || a.id,
          icon: a.icon || '🤖',
          description: a.title || 'AI-generated app',
          keywords: ['ai', 'generated', ...(a.title ? a.title.toLowerCase().split(/\s+/) : [])],
          category: a.category || 'ai',
          url: `/api/ai-apps/${a.id}/html`,
          ai5AIApp: true,
        }))
      return ai5AIApps
    })
    .catch(() => { ai5AIApps = [] })
  return ai5FetchPromise
}

// Initial fetch
refreshInstalled()
refreshAIApps()
refreshSuiteSelection()

export function getApps() {
  // Merge defaultWebApps + AI apps; installed apps override by id; builtinRegistry unaffected.
  const installedIds = new Set(installedApps.map(a => a.id))
  const webAppsNotInstalled = defaultWebApps.filter(a => !installedIds.has(a.id))
  const ai5Deduped = ai5AIApps.filter(a => !installedIds.has(a.id) && !defaultWebApps.some(b => b.id === a.id) && !builtinRegistry.some(b => b.id === a.id))
  // BUNDLE-01: drop suite tiles the user explicitly opted out of at onboarding.
  // Only ever filters the email/workspace bundle members; default state is all-on.
  return [...builtinRegistry, ...webAppsNotInstalled, ...installedApps, ...ai5Deduped]
    .filter(a => suiteAppEnabled(a.id))
}

export function getAppById(id) {
  return getApps().find(app => app.id === id)
}

// ORIGIN-01 supersedes SANDBOX-01/02/03. The needsSameOrigin() allowlist that
// used to live here is GONE, along with the `needsSameOrigin` manifest flag's
// power to do anything.
//
// What it did: it granted `allow-same-origin` to five first-party apps
// (calculator, clock, pdf-viewer, text-editor, weather) that read localStorage at
// load — localStorage throws in an opaque origin, which white-screened them. But
// those apps were served from /app/<id>/, i.e. the SHELL's own origin, so the
// grant handed each of them the shell's storage, cookies, DOM and gateway auth
// headers. Restricting the grant to first-party apps narrowed who could abuse it;
// it did not make the boundary real. And it could not survive third-party apps.
//
// What replaces it: apps are served from their OWN origin where the deployment
// can do it ({app}--{profile}.{base}), and the sandbox is derived from the frame
// URL's actual origin rather than from any flag — see core/AppOrigins.js. An app
// on the shell's origin now NEVER receives allow-same-origin, first-party or not.
// The five apps get their storage back over the shell↔app postMessage bridge in
// core/AppBridge.js.
//
// The `needsSameOrigin` / `needs_same_origin` fields are still parsed off app
// manifests (below) so old manifests keep loading, but nothing reads them. They
// grant NOTHING. Do not reintroduce a consumer.

export function getAppsByCategory() {
  const cats = {}
  for (const app of getApps()) {
    const c = app.category || 'other'
    if (!cats[c]) cats[c] = []
    cats[c].push(app)
  }
  return cats
}

export function searchApps(query) {
  const q = query.toLowerCase().trim()
  if (!q) return getApps()
  return getApps().filter(app =>
    app.name.toLowerCase().includes(q) ||
    app.description.toLowerCase().includes(q) ||
    app.keywords.some(k => k.includes(q))
  )
}
