import { useState, useEffect, useRef, useSyncExternalStore, type CSSProperties, type FormEvent } from 'react'
import { useShell } from '../providers/ShellProvider'
import { getApps, searchApps, subscribeApps, getAppsVersion } from '../core/AppRegistry'
import { launchApp } from './launchApp'
import { AppIconTile } from '../core/AppIcons'
import { useFocusTrap } from './useFocusTrap'
import type { AppEntry } from './appTypes'
import './shell-chrome.css'

// The full lane-dispatch launch logic lives in the shared ./launchApp module so
// the Launchpad and the ⌘K command palette open apps identically.
// Mail is the gateway-proxied `lilmail` connector; Diwan is the standalone
// office web app; Calendar and Contacts are standalone builtin React
// apps over lilmail's /v1 (via /api/pim/*). Real-time comms (Talk/Meet) are
// third-party, reached as external services rather than registered as OS apps.

const categoryLabels: Record<string, string> = {
  internet: 'Internet',
  productivity: 'Productivity',
  utilities: 'Utilities',
  media: 'Media',
  developer: 'Developer',
  system: 'System',
}

// Shape of one entry returned by GET /api/desktop/entries (apt-installed GUI
// apps, freedesktop.org .desktop file fields). Narrowed from `unknown` JSON —
// see isRecord()/toDesktopEntry() below, matching lib/offlineAuth.ts's
// isRecord() narrowing pattern used across the converted TS surfaces.
interface DesktopEntry {
  id: string
  name: string
  icon?: string
  no_display?: boolean
  exec?: string
  categories?: string[]
}

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function toDesktopEntry(x: unknown): DesktopEntry | null {
  if (!isRecord(x) || typeof x.id !== 'string' || typeof x.name !== 'string') return null
  return {
    id: x.id,
    name: x.name,
    icon: typeof x.icon === 'string' ? x.icon : undefined,
    no_display: typeof x.no_display === 'boolean' ? x.no_display : undefined,
    exec: typeof x.exec === 'string' ? x.exec : undefined,
    categories: Array.isArray(x.categories) ? x.categories.filter((c): c is string => typeof c === 'string') : undefined,
  }
}

function toDesktopEntries(x: unknown): DesktopEntry[] {
  return Array.isArray(x) ? x.map(toDesktopEntry).filter((e): e is DesktopEntry => e !== null) : []
}

// AppTile's '--tile-i' staggers the entrance animation via a CSS custom
// property; CSSProperties has no index signature for those, so extend it
// the same way Contacts.tsx's RingStyle does for '--tw-ring-color'.
type TileStyle = CSSProperties & { '--tile-i'?: number }

export default function Launchpad() {
  const { launchpadOpen, setLaunchpad, openWindow, setChat } = useShell()
  const [search, setSearch] = useState('')
  const [chatInput, setChatInput] = useState('')
  const [desktopEntries, setDesktopEntries] = useState<DesktopEntry[]>([])
  const searchRef = useRef<HTMLInputElement>(null)
  const chatRef = useRef<HTMLInputElement>(null)
  // A11Y: trap focus inside the launchpad while open + restore on close.
  const trapRef = useFocusTrap(launchpadOpen)

  // Load desktop entries (apt-installed GUI apps)
  useEffect(() => {
    if (!launchpadOpen) return
    fetch('/api/desktop/entries')
      .then(r => r.json())
      .then((entries: unknown) => setDesktopEntries(toDesktopEntries(entries)))
      .catch(() => {})
  }, [launchpadOpen])

  // ESC to close + focus search on open
  useEffect(() => {
    if (!launchpadOpen) return
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        setLaunchpad(false)
        setSearch('')
        setChatInput('')
      }
    }
    window.addEventListener('keydown', handler, true)
    // Auto-focus search
    setTimeout(() => searchRef.current?.focus(), 50)
    return () => window.removeEventListener('keydown', handler, true)
  }, [launchpadOpen, setLaunchpad])

  // Re-render when the installed/AI app lists populate after boot. MUST be
  // called before the early return below — hooks run unconditionally.
  useSyncExternalStore(subscribeApps, getAppsVersion, getAppsVersion)

  if (!launchpadOpen) return null

  const close = () => { setLaunchpad(false); setSearch(''); setChatInput('') }

  // Merge builtin/registry apps with desktop entries (apt-installed GUI apps)
  const baseApps = search.trim() ? searchApps(search) : getApps()
  const baseIds = new Set(baseApps.map(a => a.id))

  // IDs of desktop entries that duplicate built-in apps (e.g. Chromium browser)
  const hiddenDesktopIds = new Set(['chromium-browser', 'chromium', 'org.chromium.Chromium'])

  // Convert desktop entries to app format, filter out duplicates and hidden entries
  const desktopApps: AppEntry[] = desktopEntries
    .filter((e): e is DesktopEntry & { exec: string } => !e.no_display && !!e.exec && !baseIds.has(e.id) && !hiddenDesktopIds.has(e.id))
    .map(e => ({
      id: e.id,
      name: e.name,
      icon: e.icon || e.id,
      // No description/keywords come from a .desktop entry — AppRegistry's
      // App type requires them, so default empty; nothing downstream (the
      // tile grid, search filter below, or launchApp's lane dispatch) reads
      // either field for a desktop app.
      description: '',
      keywords: [],
      category: mapDesktopCategory(e.categories),
      _desktop: true, // Flag for stream-based launch
      _exec: e.exec,
    }))
    .filter(a => {
      if (!search.trim()) return true
      const q = search.toLowerCase()
      return a.name.toLowerCase().includes(q) || a.id.toLowerCase().includes(q)
    })

  const apps: AppEntry[] = [...baseApps, ...desktopApps]
  const grouped = search.trim() ? null : groupByCategory(apps)

  const launch = async (app: AppEntry) => {
    // All lane dispatch (builtin · web · stream · native · fallback) lives in
    // the shared launchApp helper, which the ⌘K palette also uses.
    await launchApp(app, { openWindow })
    close()
  }

  const handleChatSubmit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    if (!chatInput.trim()) return
    close()
    // Open chat panel and send the message
    setChat(true)
    // Dispatch a custom event so Portal can pick it up
    window.dispatchEvent(new CustomEvent('vulos:chat', { detail: chatInput.trim() }))
    setChatInput('')
  }

  return (
    <div
      ref={trapRef}
      role="dialog"
      aria-modal="true"
      aria-label="Application launcher"
      className="vshell-scrim vshell-scrim-strong fixed inset-0 z-50 flex flex-col"
      onClick={(e) => { if (e.target === e.currentTarget) close() }}
    >
      {/* Search bar */}
      <div className="flex justify-center px-6 pb-4" style={{ paddingTop: 'max(clamp(1.75rem, 6vh, 3.5rem), var(--safe-top))' }}>
        <div className="vshell-pop relative w-full max-w-[520px]">
          <div className="pointer-events-none absolute left-4 top-1/2 -translate-y-1/2" style={{ color: 'var(--text-faint)' }}>
            <svg viewBox="0 0 16 16" className="h-4 w-4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
              <circle cx="7" cy="7" r="5" />
              <path d="M11 11l3.5 3.5" />
            </svg>
          </div>
          <input
            ref={searchRef}
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search applications"
            className="vshell-input w-full rounded-[13px] py-3 pl-11 pr-10 text-sm"
          />
          {search && (
            <button
              onClick={() => setSearch('')}
              aria-label="Clear search"
              className="focus-primary absolute right-2.5 top-1/2 flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded-full transition-colors"
              style={{ color: 'var(--text-faint)', background: 'color-mix(in srgb, var(--bg-hover) 70%, transparent)' }}
            >
              <svg viewBox="0 0 16 16" className="h-3 w-3" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
                <path d="M4 4l8 8M12 4l-8 8" />
              </svg>
            </button>
          )}
        </div>
      </div>

      {/* App grid */}
      <div className="flex-1 overflow-y-auto px-5 pb-8 sm:px-6">
        <div className="mx-auto w-full max-w-[820px]">
          {grouped ? (
            Object.entries(grouped).map(([cat, catApps], i) => (
              <div key={cat} className={i === 0 ? '' : 'mt-6'}>
                <h3
                  className="mb-3 mt-4 px-0.5 text-[12px] font-semibold uppercase tracking-[0.09em]"
                  style={{ color: 'var(--text-faint)' }}
                >
                  {categoryLabels[cat] || cat}
                </h3>
                <AppGrid apps={catApps} onLaunch={launch} />
              </div>
            ))
          ) : (
            <div className="pt-1">
              <AppGrid apps={apps} onLaunch={launch} />
            </div>
          )}

          {apps.length === 0 && (
            <div className="flex flex-col items-center gap-3 py-20 text-center">
              <div
                className="flex h-12 w-12 items-center justify-center rounded-[14px]"
                style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border-default)', color: 'var(--text-faint)' }}
              >
                <svg viewBox="0 0 20 20" className="h-5 w-5" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round">
                  <circle cx="9" cy="9" r="6" />
                  <path d="M13.5 13.5L17 17" />
                </svg>
              </div>
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>
                No applications match {search.trim() ? `"${search.trim()}"` : 'that'}
              </p>
            </div>
          )}
        </div>
      </div>

      {/* Bottom bar — chat input + ESC hint */}
      <div className="vshell-border-t flex-shrink-0" style={{ background: 'color-mix(in srgb, var(--bg-elevated) 40%, transparent)', paddingBottom: 'var(--safe-bottom)' }}>
        <div className="mx-auto max-w-[520px] px-6 py-3">
          <form onSubmit={handleChatSubmit} className="flex items-center gap-2">
            <div className="relative flex-1">
              <div className="pointer-events-none absolute left-3.5 top-1/2 -translate-y-1/2" style={{ color: 'var(--text-faint)' }}>
                <svg viewBox="0 0 16 16" className="h-3.5 w-3.5" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
                  <path d="M2.5 4a2 2 0 012-2h7a2 2 0 012 2v5a2 2 0 01-2 2H6l-3 2.5V11a2 2 0 01-.5-1.3z" />
                </svg>
              </div>
              <input
                ref={chatRef}
                type="text"
                value={chatInput}
                onChange={(e) => setChatInput(e.target.value)}
                placeholder="Ask anything"
                className="vshell-input w-full rounded-[11px] py-2 pl-10 pr-3 text-sm"
              />
            </div>
            <button
              type="submit"
              disabled={!chatInput.trim()}
              className="focus-primary rounded-[11px] px-3.5 py-2 text-xs font-medium transition-colors disabled:cursor-default disabled:opacity-30"
              style={{ background: chatInput.trim() ? 'var(--accent)' : 'color-mix(in srgb, var(--bg-hover) 70%, transparent)', color: chatInput.trim() ? '#fff' : 'var(--text-tertiary)' }}
            >
              Send
            </button>
            <kbd className="vshell-kbd ml-1 hidden sm:inline-flex">esc</kbd>
          </form>
        </div>
      </div>
    </div>
  )
}

// Map freedesktop.org categories to our launchpad categories
function mapDesktopCategory(cats: string[] | undefined): string {
  if (!cats || cats.length === 0) return 'utilities'
  const c = cats.map(s => s.toLowerCase())
  if (c.some(s => ['game', 'amusement', 'blocksgame', 'boardgame', 'cardgame'].includes(s))) return 'media'
  if (c.some(s => ['development', 'ide', 'debugger', 'building', 'database'].includes(s))) return 'developer'
  if (c.some(s => ['graphics', 'audio', 'video', 'audiovisual', 'music', 'player', 'recorder'].includes(s))) return 'media'
  if (c.some(s => ['network', 'webbrowser', 'email', 'chat', 'instantmessaging', 'remoteaccess'].includes(s))) return 'internet'
  if (c.some(s => ['office', 'wordprocessor', 'spreadsheet', 'presentation', 'calendar', 'contactmanagement'].includes(s))) return 'productivity'
  if (c.some(s => ['system', 'monitor', 'settings', 'terminalemulator', 'filesystem'].includes(s))) return 'system'
  if (c.some(s => ['utility', 'texteditor', 'archiving', 'calculator', 'clock'].includes(s))) return 'utilities'
  return 'utilities'
}

// Group apps by category (replacement for getAppsByCategory that works with merged list)
function groupByCategory(apps: AppEntry[]): Record<string, AppEntry[]> {
  const groups: Record<string, AppEntry[]> = {}
  for (const app of apps) {
    const cat = app.category || 'utilities'
    if (!groups[cat]) groups[cat] = []
    groups[cat].push(app)
  }
  return groups
}

interface AppGridProps {
  apps: AppEntry[]
  onLaunch: (app: AppEntry) => Promise<void>
}

// The responsive tile grid. Columns reflow from 4 (phone) up to 7 (wide /
// ultrawide) using the design-direction breakpoints (520 / 720 / 920), with a
// wider row gap than column gap so labels breathe without drifting apart.
function AppGrid({ apps, onLaunch }: AppGridProps) {
  return (
    <div className="grid grid-cols-4 gap-x-2 gap-y-5 min-[520px]:grid-cols-5 min-[720px]:grid-cols-6 min-[920px]:grid-cols-7">
      {apps.map((app, i) => (
        <AppTile key={app.id} app={app} onLaunch={onLaunch} index={i} />
      ))}
    </div>
  )
}

interface AppTileProps {
  app: AppEntry
  onLaunch: (app: AppEntry) => Promise<void>
  index?: number
}

function AppTile({ app, onLaunch, index = 0 }: AppTileProps) {
  const tileStyle: TileStyle = { '--tile-i': Math.min(index, 28) }
  return (
    <button
      onClick={() => onLaunch(app)}
      aria-label={`Open ${app.name}`}
      title={app.name}
      className="vshell-tile vulos-tile-in focus-primary group flex flex-col items-center gap-2 rounded-[13px] px-1 py-2"
      style={tileStyle}
    >
      <AppIconTile id={app.id} size={60} unicode={app.icon} />
      <span className="vshell-tile-label max-w-[80px] truncate text-center text-[12px] leading-tight">
        {app.name}
      </span>
    </button>
  )
}
