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

  {
    id: 'mail',
    name: 'Mail',
    icon: '✉',
    description: 'Vulos mail — inbox, compose, send, and thread view',
    keywords: ['mail', 'email', 'inbox', 'compose', 'send', 'vumail', 'messages', 'thread'],
    category: 'internet',
    builtin: true,
  },

  {
    id: 'browser',
    name: 'Chrome',
    icon: 'chrome',
    description: 'Web browser',
    keywords: ['browser', 'web', 'internet', 'surf', 'chromium', 'chrome'],
    category: 'internet',
    builtin: true,
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
  },
  {
    id: 'calendar',
    name: 'Calendar',
    icon: '📅',
    description: 'Month, week, and day planning with server-side persistent events',
    keywords: ['calendar', 'schedule', 'events', 'reminders', 'ics'],
    category: 'productivity',
    url: '/app/calendar/',
    port: 80,
  },
  {
    id: 'clock',
    name: 'Clock',
    icon: '◷',
    description: 'Time, world clocks, stopwatch, and timer',
    keywords: ['clock', 'time', 'world clock', 'stopwatch', 'timer', 'alarm'],
    category: 'utilities',
    url: '/app/clock/',
    port: 80,
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
