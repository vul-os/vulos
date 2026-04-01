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
]

// Dynamic installed apps from backend
let installedApps = []
let fetchPromise = null

export function refreshInstalled() {
  fetchPromise = fetch('/api/store/installed')
    .then(r => r.ok ? r.json() : [])
    .then(apps => {
      installedApps = (apps || [])
        .filter(a => !builtinRegistry.some(b => b.id === a.id))
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

// Initial fetch
refreshInstalled()

export function getApps() {
  return [...builtinRegistry, ...installedApps]
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
