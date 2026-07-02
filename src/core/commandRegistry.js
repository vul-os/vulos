// commandRegistry.js — the "Actions" seam behind the ⌘K palette.
//
// A tiny, framework-agnostic registry of curated commands. The OS ships a set
// of built-in actions (Compose, New event, Open Home, Transparency, Settings,
// Lock, Toggle theme). Any app or subsystem can contribute its own via
// registerCommand(), which is how the palette stays extensible without the
// palette component needing to know about every feature.
//
// A command is a plain object:
//   {
//     id:        string   unique id (re-registering the same id replaces it)
//     title:     string   primary label shown in the palette
//     subtitle?: string   secondary/mono hint
//     keywords?: string[] extra terms the fuzzy matcher can hit
//     icon?:     string   short glyph
//     section?:  string   defaults to 'Actions'
//     run(ctx):  fn        performs the action; receives the palette context
//     when?(ctx): fn       optional predicate — hide the command when it returns false
//   }
//
// `run` and `when` receive a CONTEXT object assembled by the palette at dispatch
// time (see CommandPalette.jsx). Commands stay decoupled from React by only
// touching ctx helpers:
//   ctx.openApp(appId, opts?)   launch a registered app
//   ctx.openUrl(url, title, icon)  open a web view window
//   ctx.openHome()              reveal the Home surface
//   ctx.openSettings(section?)  open Settings
//   ctx.openTransparency()      open the Transparency / "where your AI runs" panel
//   ctx.toggleTheme()           flip light/dark
//   ctx.lock()                  lock the screen
//   ctx.close()                 close the palette

const commands = new Map()
const listeners = new Set()

function emit() {
  for (const fn of listeners) {
    try { fn() } catch { /* a bad listener must not break dispatch */ }
  }
}

/**
 * Register (or replace, by id) a command. Returns an unregister function.
 */
export function registerCommand(cmd) {
  if (!cmd || !cmd.id || typeof cmd.run !== 'function') {
    throw new Error('registerCommand: command needs an id and a run() function')
  }
  commands.set(cmd.id, { section: 'Actions', ...cmd })
  emit()
  return () => unregisterCommand(cmd.id)
}

/**
 * Register several commands at once. Returns a single unregister function.
 */
export function registerCommands(list) {
  const offs = (list || []).map(registerCommand)
  return () => offs.forEach(off => off())
}

export function unregisterCommand(id) {
  if (commands.delete(id)) emit()
}

/**
 * All registered commands, optionally filtered by a context predicate (`when`).
 */
export function getCommands(ctx) {
  const all = [...commands.values()]
  if (!ctx) return all
  return all.filter(c => (typeof c.when === 'function' ? safeWhen(c.when, ctx) : true))
}

function safeWhen(when, ctx) {
  try { return !!when(ctx) } catch { return true }
}

/**
 * Subscribe to registry changes (a new app contributed a command, etc.).
 * Returns an unsubscribe function.
 */
export function subscribeCommands(fn) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

/**
 * Dispatch a command by id against a context. Returns true if it ran.
 */
export function runCommand(id, ctx) {
  const cmd = commands.get(id)
  if (!cmd) return false
  cmd.run(ctx)
  return true
}

// ── Built-in actions ─────────────────────────────────────────────────────────
// Registered once at module load. They are ordinary commands — nothing about
// them is privileged over app-contributed ones.

const BUILTIN_COMMANDS = [
  {
    id: 'compose',
    title: 'Compose email',
    subtitle: 'New message in Mail',
    keywords: ['mail', 'email', 'write', 'send', 'new', 'compose'],
    icon: '✉',
    run: (ctx) => ctx.openApp('lilmail', { query: 'compose=1' }),
  },
  {
    id: 'new-event',
    title: 'New event',
    subtitle: 'Add to Calendar',
    keywords: ['calendar', 'event', 'schedule', 'meeting', 'new', 'appointment'],
    icon: '⊡',
    run: (ctx) => ctx.openApp('vulos-calendar', { query: 'action=new' }),
  },
  {
    id: 'open-home',
    title: 'Go Home',
    subtitle: 'Reveal the Home surface',
    keywords: ['home', 'desktop', 'dashboard', 'overview'],
    icon: '⌂',
    run: (ctx) => ctx.openHome(),
  },
  {
    id: 'open-transparency',
    title: 'Transparency — where your AI runs',
    subtitle: 'Sovereignty tier & egress',
    keywords: ['transparency', 'sovereignty', 'tier', 'privacy', 'egress', 'ai', 'trust', 'where'],
    icon: '◈',
    run: (ctx) => ctx.openTransparency(),
  },
  {
    id: 'open-settings',
    title: 'Open Settings',
    subtitle: 'System settings & AI config',
    keywords: ['settings', 'preferences', 'config', 'system'],
    icon: '⚙',
    run: (ctx) => ctx.openSettings(),
  },
  {
    id: 'open-notifications',
    title: 'Show notifications',
    subtitle: 'Open the notification center',
    keywords: ['notifications', 'notification', 'center', 'bell', 'alerts', 'unread'],
    icon: '🔔',
    run: (ctx) => {
      // Framework-agnostic: the shell bell listens for this event.
      try { window.dispatchEvent(new CustomEvent('vulos:open-notifications')) } catch { /* noop */ }
      ctx.close?.()
    },
  },
  {
    id: 'notification-settings',
    title: 'Notification settings',
    subtitle: 'Mute, sounds, per-source',
    keywords: ['notifications', 'do not disturb', 'dnd', 'mute', 'sound', 'settings'],
    icon: '⚙',
    run: (ctx) => ctx.openSettings('notifications'),
  },
  {
    id: 'lock',
    title: 'Lock screen',
    subtitle: 'Ctrl+L',
    keywords: ['lock', 'screen', 'secure', 'away'],
    icon: '⊘',
    run: (ctx) => ctx.lock(),
  },
  {
    id: 'toggle-theme',
    title: 'Toggle theme',
    subtitle: 'Light / dark',
    keywords: ['theme', 'dark', 'light', 'appearance', 'mode'],
    icon: '◐',
    run: (ctx) => ctx.toggleTheme(),
  },
  {
    id: 'keyboard-shortcuts',
    title: 'Keyboard shortcuts',
    subtitle: 'Show the shortcut cheat-sheet (?)',
    keywords: ['keyboard', 'shortcuts', 'keys', 'hotkeys', 'cheatsheet', 'help', 'bindings'],
    icon: '⌨',
    run: (ctx) => {
      // The ShortcutsLegend overlay listens for this event.
      try { window.dispatchEvent(new CustomEvent('vulos:open-shortcuts')) } catch { /* noop */ }
      ctx.close?.()
    },
  },
]

// Register the built-ins immediately so they exist before the palette mounts.
// Re-registering by id is idempotent, so hot-reload / repeated imports are safe.
registerCommands(BUILTIN_COMMANDS)

export { BUILTIN_COMMANDS }
