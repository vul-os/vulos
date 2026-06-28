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

  // UNIFIED-STORAGE de-dup: the embedded `mail`, `office`, and `calendar`
  // builtins are RETIRED. Each is superseded by its gateway-proxied suite app
  // registered below — `lilmail`, `vulos-office`, and `vulos-calendar`
  // respectively — which are the canonical entries going forward. Keeping both
  // produced duplicate launcher tiles (and a colliding `calendar` id shared with
  // defaultWebApps). ORPHAN-FIX: the retired `apps/mail/` and `apps/calendar/`
  // build dirs have since been deleted so they are no longer shipped.

  // Spaces was extracted out of vulos-office into the standalone Vulos Talk
  // product. The embedded builtin (which imported @vulos/office-client/spaces)
  // is retired; users reach chat/channels via the gateway-proxied `vulos-talk`
  // web app registered below (/app/vulos-talk/).

  // The embedded "meet" builtin also relied on @vulos/office-client/spaces
  // (the spaces lib provided the call/meeting components). With Spaces moved to
  // Vulos Talk, video meetings are served by the gateway-proxied `vulos-meet`
  // web app registered below (/app/vulos-meet/); the builtin is retired.

  // BROWSER-01: "Smart Browser" is the client-side web app (apps/browser/).
  // It opens in the host browser as a WebApp lane entry — zero stream.Session.
  // The server-side streamed Chromium ("browser" via stream pool) is retired.
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
  {
    id: 'social',
    name: 'Fediverse',
    icon: '◎',
    description: 'Read-only ActivityPub social client — browse public Mastodon timelines',
    keywords: ['social', 'fediverse', 'mastodon', 'activitypub', 'timeline', 'feed'],
    port: 80,
    category: 'network',
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
  {
    id: 'vulos-office',
    name: 'Office',
    icon: '⊟',
    description: 'Docs, Sheets, Slides, and PDF — collaborative editing with per-user file storage',
    keywords: ['docs', 'sheets', 'slides', 'pdf', 'word', 'excel', 'spreadsheet', 'presentation', 'document', 'office'],
    category: 'office',
    type: 'web',
    url: '/app/vulos-office/',
    port: 80,
    permissions: ['storage'], // file-bearing: documents persisted to the object store
  },
  {
    id: 'vulos-workspace',
    name: 'Workspace',
    icon: '⊞',
    description: 'Unified workspace shell that ties the suite together',
    keywords: ['workspace', 'suite', 'home', 'shell', 'launcher'],
    category: 'office',
    type: 'web',
    url: '/app/vulos-workspace/',
    port: 80,
    // No storage permission: Workspace is a stateless shell — it owns no files.
  },
  {
    id: 'vulos-talk',
    name: 'Talk',
    icon: '☷',
    description: 'Channels, DMs, and threads with file sharing',
    keywords: ['talk', 'chat', 'channels', 'dm', 'direct', 'threads', 'messaging', 'spaces'],
    category: 'internet',
    type: 'web',
    url: '/app/vulos-talk/',
    port: 80,
    permissions: ['storage'], // file-bearing: shared files/attachments
  },
  {
    id: 'vulos-meet',
    name: 'Meet',
    icon: '◉',
    description: 'Video meetings with recordings and shared files',
    keywords: ['meet', 'video', 'call', 'meeting', 'join', 'conference', 'recording'],
    category: 'internet',
    type: 'web',
    url: '/app/vulos-meet/',
    port: 80,
    permissions: ['storage'], // file-bearing: recordings/shared files
  },
  {
    id: 'board',
    name: 'Board',
    icon: '▦',
    description: 'Vulos Board — collaborative infinite whiteboard with realtime sync',
    keywords: ['board', 'whiteboard', 'canvas', 'draw', 'diagram', 'sketch', 'collaborate', 'realtime'],
    category: 'productivity',
    type: 'web',
    url: '/app/board/',
    port: 80,
    // No storage permission: the board *sync server* uses the storage seam
    // itself (it is not gateway header-injected). The OS mints its websocket
    // auth token via GET /api/board/token (see backend routes_board.go).
  },
  // MAIL-PIM: Calendar and Contacts are surfaces of the Mail product now (Office
  // stopped serving them). They are NOT separate gateway apps — there is no
  // `vulos-calendar`/`vulos-contacts` namespace to proxy to (that backend was
  // removed, which left these tiles 404'ing). Instead they deep-link into the
  // already-running Mail app (`lilmail`), whose webmail serves the standalone
  // /calendar and /contacts surfaces (@vulos/mail-ui <Calendar/>/<Contacts/>).
  // Reusing the lilmail gateway route means no dangling app/grant of their own.
  {
    id: 'vulos-calendar',
    name: 'Calendar',
    icon: '⊡',
    description: 'Calendar — a surface of the Mail product (events via CalDAV)',
    keywords: ['calendar', 'events', 'schedule', 'caldav', 'reminders', 'mail'],
    category: 'office',
    type: 'web',
    url: '/app/lilmail/calendar',
    port: 80,
    // No storage permission: CalDAV-backed (server is the store). Served by the
    // Mail app (lilmail), so no separate gateway grant for this id.
  },
  {
    id: 'vulos-contacts',
    name: 'Contacts',
    icon: '☻',
    description: 'Contacts — a surface of the Mail product (address book via CardDAV)',
    keywords: ['contacts', 'address', 'book', 'carddav', 'people', 'mail'],
    category: 'office',
    type: 'web',
    url: '/app/lilmail/contacts',
    port: 80,
    // No storage permission: CardDAV-backed (server is the store). Served by the
    // Mail app (lilmail), so no separate gateway grant for this id.
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
    // SANDBOX-01: persists history/mode in localStorage at load — needs a
    // real (non-opaque) origin. See needsSameOrigin() below.
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
    // SANDBOX-01: reads world-clock config from localStorage at load.
    needsSameOrigin: true,
  },
  {
    id: 'pdf-viewer',
    name: 'PDF Viewer',
    icon: '▤',
    description: 'Local PDF reader with notes and quick controls',
    keywords: ['pdf', 'reader', 'document', 'annotations', 'notes'],
    category: 'productivity',
    url: '/app/pdf-viewer/',
    port: 80,
    // SANDBOX-01: stores per-doc notes/annotations + recents in localStorage.
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
    // SANDBOX-01: persists theme/font/wrap/last-file in localStorage.
    needsSameOrigin: true,
  },
  {
    id: 'sheets',
    name: 'Sheets',
    icon: '⊞',
    description: 'Collaborative spreadsheet with live multi-user editing via Yjs',
    keywords: ['sheets', 'spreadsheet', 'collab', 'table', 'data', 'collaborate'],
    category: 'productivity',
    url: '/app/sheets/',
    port: 80,
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
    // SANDBOX-01: reads unit/location prefs from localStorage at load.
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
    id: 'maps',
    name: 'Maps',
    icon: '⊕',
    description: 'OpenStreetMap navigation with place search, directions and favourites',
    keywords: ['maps', 'navigation', 'directions', 'location', 'gps', 'osm', 'openstreetmap'],
    category: 'productivity',
    url: '/app/maps/',
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

export function getApps() {
  // Merge defaultWebApps + AI apps; installed apps override by id; builtinRegistry unaffected.
  const installedIds = new Set(installedApps.map(a => a.id))
  const webAppsNotInstalled = defaultWebApps.filter(a => !installedIds.has(a.id))
  const ai5Deduped = ai5AIApps.filter(a => !installedIds.has(a.id) && !defaultWebApps.some(b => b.id === a.id) && !builtinRegistry.some(b => b.id === a.id))
  return [...builtinRegistry, ...webAppsNotInstalled, ...installedApps, ...ai5Deduped]
}

export function getAppById(id) {
  return getApps().find(app => app.id === id)
}

// SANDBOX-01: iframe origin isolation.
//
// App iframes are served from SAME-ORIGIN paths (/app/<id>/, /apps/browser/…).
// A same-origin iframe with `allow-scripts` is NOT a security boundary: it can
// reach window.top, the shell's localStorage/cookies, and any gateway-injected
// auth headers. The correct long-term fix is to serve each app from its own
// origin (e.g. <id>.apps.host) so the browser enforces isolation for us.
//
// SANDBOX-02 (security hardening): `allow-same-origin` is now restricted to
// FIRST-PARTY apps only (apps shipped with the OS in builtinRegistry or
// defaultWebApps).  Third-party installed apps from /api/store/installed are
// NEVER granted allow-same-origin even if they declare needs_same_origin: true
// in their app.json — a malicious or compromised store app could otherwise read
// the shell's localStorage, cookies, and gateway-injected auth headers.
//
// The only first-party apps that currently opt in are those that read/write
// localStorage at load (calculator, clock, pdf-viewer, text-editor, weather) —
// an opaque origin makes localStorage throw, white-screening them.  All other
// first-party apps run in opaque-origin sandboxes.
//
// Follow-up (SANDBOX-03): serve each app from its own subdomain origin
// (<id>--default.<host>) using the gateway's existing subdomain routing.  When
// that lands, allow-same-origin will mean a per-app origin (not the shell), and
// the restriction below can be relaxed.  See backend/services/gateway/gateway.go
// and appnet/subdomain.go for the routing infrastructure.

// firstPartyIds is the set of app IDs shipped with the OS.  Only these apps may
// receive the allow-same-origin sandbox token.
const firstPartyIds = new Set([
  ...builtinRegistry.map(a => a.id),
  ...defaultWebApps.map(a => a.id),
])

export function needsSameOrigin(appId) {
  const app = getAppById(appId)
  // SANDBOX-02: installed (third-party) apps are never granted same-origin,
  // regardless of what their manifest declares.
  return !!(app && app.needsSameOrigin && firstPartyIds.has(appId))
}

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
