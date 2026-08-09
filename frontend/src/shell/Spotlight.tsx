// Spotlight.tsx — the shell's app launcher.
//
// One keyboard-first surface that replaces the old Launchpad grid on the
// desktop: ⌘Space (or Ctrl+Space, or the menu-bar Applications button) opens a
// centred search field. Typing fuzzy-ranks every launchable app — builtin,
// installed, AI-generated and apt-installed .desktop entries — over its NAME,
// its KEYWORDS and its DESCRIPTION (the three fields AppEntry actually
// carries), best match first. An empty query shows recents plus the full grid,
// so it is still the "all apps" surface it replaces.
//
// State: it deliberately reuses the shell store's existing `launchpadOpen`
// flag rather than inventing a parallel one, so every existing opener
// (TopBar's Applications button, Home's "All apps", the ⌘K palette's
// launchpad command, MobileStack's dock) drives it unchanged — and the mobile
// stack keeps rendering the touch grid from the same bit.
//
// Launching goes through the shared launchApp() lane dispatch, identical to
// every other launch surface.
import { useState, useEffect, useRef, useMemo, useCallback, useSyncExternalStore } from 'react'
import { useShell } from '../providers/ShellProvider'
import { getApps, subscribeApps, getAppsVersion } from '../core/AppRegistry'
import { rankApps } from './appSearch'
import { launchApp } from './launchApp'
import { AppIconTile } from '../core/AppIcons'
import { useFocusTrap } from './useFocusTrap'
import type { AppEntry } from './appTypes'
import './shell-chrome.css'

const RECENT_KEY = 'vulos-spotlight-recent'
const MAX_RESULTS = 8

function loadRecent(): string[] {
  try {
    const raw = localStorage.getItem(RECENT_KEY)
    const ids: unknown = raw ? JSON.parse(raw) : []
    return Array.isArray(ids) ? ids.filter((x): x is string => typeof x === 'string') : []
  } catch { return [] }
}

function pushRecent(id: string): void {
  try {
    const ids = loadRecent().filter(x => x !== id)
    ids.unshift(id)
    localStorage.setItem(RECENT_KEY, JSON.stringify(ids.slice(0, 8)))
  } catch { /* noop */ }
}

// ── apt-installed .desktop entries ───────────────────────────────────────────
// Same GET /api/desktop/entries source the Launchpad grid used; narrowed from
// `unknown` JSON rather than trusted (a network trust boundary).
interface DesktopEntry { id: string; name: string; icon?: string; no_display?: boolean; exec?: string; categories?: string[] }

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function toDesktopEntries(x: unknown): DesktopEntry[] {
  if (!Array.isArray(x)) return []
  const out: DesktopEntry[] = []
  for (const e of x) {
    if (!isRecord(e) || typeof e.id !== 'string' || typeof e.name !== 'string') continue
    out.push({
      id: e.id,
      name: e.name,
      icon: typeof e.icon === 'string' ? e.icon : undefined,
      no_display: e.no_display === true,
      exec: typeof e.exec === 'string' ? e.exec : undefined,
      categories: Array.isArray(e.categories) ? e.categories.filter((c): c is string => typeof c === 'string') : undefined,
    })
  }
  return out
}

const CATEGORY_LABEL: Record<string, string> = {
  internet: 'Internet',
  productivity: 'Productivity',
  utilities: 'Utilities',
  media: 'Media',
  developer: 'Developer',
  system: 'System',
  desktop: 'Installed',
}

// Duplicate .desktop entries for apps the OS already ships as first-class.
const HIDDEN_DESKTOP_IDS = new Set(['chromium-browser', 'chromium', 'org.chromium.Chromium'])

export default function Spotlight() {
  const { launchpadOpen, setLaunchpad, openWindow } = useShell()
  const [query, setQuery] = useState('')
  const [sel, setSel] = useState(0)
  const [entries, setEntries] = useState<DesktopEntry[]>([])
  // Lazily seeded from localStorage and refreshed in open() (the only writer),
  // so no effect has to setState when the launcher opens.
  const [recentIds, setRecentIds] = useState<string[]>(loadRecent)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const trapRef = useFocusTrap(launchpadOpen)

  // Re-render when the installed / AI app lists arrive after boot.
  const appsVersion = useSyncExternalStore(subscribeApps, getAppsVersion, getAppsVersion)

  // ⌘Space / Ctrl+Space toggles. Registered in the capture phase so it wins
  // over an app's own handlers, and never fires while a text field owns the
  // keystroke via IME composition.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.altKey && (e.code === 'Space' || e.key === ' ')) {
        e.preventDefault()
        e.stopPropagation()
        setLaunchpad(!launchpadOpen)
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [launchpadOpen, setLaunchpad])

  // On open: take the caret, and refresh the apt-installed entries. The field
  // itself is cleared by close(), not here, so this effect writes no state.
  useEffect(() => {
    if (!launchpadOpen) return
    const t = setTimeout(() => inputRef.current?.focus(), 40)
    fetch('/api/desktop/entries')
      .then(r => r.ok ? r.json() : [])
      .then((data: unknown) => setEntries(toDesktopEntries(data)))
      .catch(() => { /* no desktop entries on this box — builtins still list */ })
    return () => clearTimeout(t)
  }, [launchpadOpen])

  const close = useCallback(() => { setLaunchpad(false); setQuery(''); setSel(0) }, [setLaunchpad])

  // Every launchable app: the registry merged with non-duplicate .desktop entries.
  const allApps: AppEntry[] = useMemo(() => {
    const base = getApps() as AppEntry[]
    const known = new Set(base.map(a => a.id))
    const desktop: AppEntry[] = entries
      .filter(e => !e.no_display && !!e.exec && !known.has(e.id) && !HIDDEN_DESKTOP_IDS.has(e.id))
      .map(e => ({
        id: e.id,
        name: e.name,
        icon: e.icon || '',
        description: 'Installed application',
        keywords: e.categories || [],
        category: 'desktop',
        type: 'desktop',
        _desktop: true,
        _exec: e.exec,
      }))
    return [...base, ...desktop]
    // getApps() is an external-store read; `appsVersion` (from the
    // useSyncExternalStore subscription above) is the value that actually
    // changes when the installed/AI lists repopulate, so it is the dependency.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [entries, appsVersion])

  // Ranked results — name + keywords + description, weighted (see appSearch.ts
  // for why those three are not equal evidence).
  const results: AppEntry[] = useMemo(
    () => rankApps(query, allApps, MAX_RESULTS),
    [query, allApps]
  )

  const recents = useMemo(
    () => recentIds.map(id => allApps.find(a => a.id === id)).filter((a): a is AppEntry => !!a).slice(0, 6),
    [recentIds, allApps]
  )

  // Empty query → the full grid, grouped by category (the "all apps" surface).
  const groups = useMemo(() => {
    const by = new Map<string, AppEntry[]>()
    for (const a of allApps) {
      const key = a.category || 'utilities'
      const list = by.get(key)
      if (list) list.push(a)
      else by.set(key, [a])
    }
    return [...by.entries()].sort((a, b) => (CATEGORY_LABEL[a[0]] || a[0]).localeCompare(CATEGORY_LABEL[b[0]] || b[0]))
  }, [allApps])

  const open = useCallback((app: AppEntry | undefined) => {
    if (!app) return
    pushRecent(app.id)
    setRecentIds(loadRecent())
    close()
    void launchApp(app, { openWindow })
  }, [close, openWindow])

  // Keep the highlighted row in view as the arrows walk past the fold.
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>('[data-sel="true"]')
    el?.scrollIntoView({ block: 'nearest' })
  }, [sel, query])

  if (!launchpadOpen) return null

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(); return }
    if (!results.length) return
    if (e.key === 'ArrowDown') { e.preventDefault(); setSel(s => (s + 1) % results.length) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setSel(s => (s - 1 + results.length) % results.length) }
    else if (e.key === 'Enter') { e.preventDefault(); open(results[sel]) }
  }

  return (
    <div
      className="fixed inset-0 z-[120] vshell-scrim-strong flex flex-col items-center px-4 pt-[12vh] pb-8 overflow-y-auto"
      onMouseDown={(e) => { if (e.target === e.currentTarget) close() }}
      role="presentation"
    >
      <div
        ref={trapRef as React.RefObject<HTMLDivElement>}
        role="dialog"
        aria-modal="true"
        aria-label="Search applications"
        className="vspot w-full max-w-[680px] rounded-[22px] overflow-hidden"
        onKeyDown={onKeyDown}
      >
        {/* Search field — the whole surface is one input, Spotlight-style. */}
        <div className="flex items-center gap-3 px-5 h-[62px]">
          <svg viewBox="0 0 24 24" className="w-[22px] h-[22px] shrink-0" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" style={{ color: 'var(--text-tertiary)' }} aria-hidden="true">
            <circle cx="10.5" cy="10.5" r="6.5" /><path d="M15.5 15.5L21 21" />
          </svg>
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => { setQuery(e.target.value); setSel(0) }}
            placeholder="Search apps"
            aria-label="Search apps"
            autoComplete="off"
            spellCheck={false}
            className="vspot-input flex-1 min-w-0 bg-transparent border-0 outline-none text-[21px] font-normal tracking-[-0.01em]"
          />
          <kbd className="vshell-kbd shrink-0">esc</kbd>
        </div>

        {/* Results — ranked rows while typing. */}
        {query.trim() ? (
          <div ref={listRef} className="vspot-body max-h-[52vh] overflow-y-auto no-scrollbar py-2">
            {results.length === 0 ? (
              <div className="px-5 py-8 text-center text-[13px]" style={{ color: 'var(--text-tertiary)' }}>
                No app matches “{query.trim()}”.
              </div>
            ) : results.map((app, i) => (
              <button
                key={app.id}
                type="button"
                data-sel={i === sel ? 'true' : undefined}
                onMouseMove={() => setSel(i)}
                onClick={() => open(app)}
                className="vspot-row w-full text-left flex items-center gap-3.5 px-3.5 py-2 mx-2 rounded-xl"
                style={{ width: 'calc(100% - 1rem)' }}
              >
                <span className="shrink-0"><AppIconTile id={app.id} size={38} unicode={app.icon} /></span>
                <span className="min-w-0 flex-1">
                  <span className="block text-[14.5px] font-medium truncate" style={{ color: 'var(--text-primary)' }}>{app.name}</span>
                  <span className="block text-[12.5px] truncate" style={{ color: 'var(--text-tertiary)' }}>{app.description}</span>
                </span>
                <span className="shrink-0 text-[11px] uppercase tracking-[0.09em] hidden sm:block" style={{ color: 'var(--text-faint)' }}>
                  {CATEGORY_LABEL[app.category] || app.category}
                </span>
              </button>
            ))}
          </div>
        ) : (
          // Empty query — recents, then the whole library.
          <div className="vspot-body max-h-[58vh] overflow-y-auto no-scrollbar px-5 pb-5 pt-1">
            {recents.length > 0 && (
              <section className="mb-5">
                <h2 className="vspot-heading">Recent</h2>
                <div className="vspot-grid">
                  {recents.map(app => <GridTile key={app.id} app={app} onOpen={open} />)}
                </div>
              </section>
            )}
            {groups.map(([cat, apps]) => (
              <section key={cat} className="mb-5 last:mb-1">
                <h2 className="vspot-heading">{CATEGORY_LABEL[cat] || cat}</h2>
                <div className="vspot-grid">
                  {apps.map(app => <GridTile key={app.id} app={app} onOpen={open} />)}
                </div>
              </section>
            ))}
          </div>
        )}

        {/* Footer hints — the keyboard contract, stated. */}
        <div className="vspot-foot flex items-center gap-4 px-5 h-9 text-[11.5px]" style={{ color: 'var(--text-faint)' }}>
          <span className="flex items-center gap-1.5"><kbd className="vshell-kbd">↑</kbd><kbd className="vshell-kbd">↓</kbd> navigate</span>
          <span className="flex items-center gap-1.5"><kbd className="vshell-kbd">⏎</kbd> open</span>
          <span className="ml-auto hidden sm:flex items-center gap-1.5"><kbd className="vshell-kbd">⌘</kbd><kbd className="vshell-kbd">space</kbd></span>
        </div>
      </div>
    </div>
  )
}

function GridTile({ app, onOpen }: { app: AppEntry; onOpen: (a: AppEntry) => void }) {
  return (
    <button
      type="button"
      onClick={() => onOpen(app)}
      title={app.description || app.name}
      className="vspot-tile flex flex-col items-center gap-1.5 p-2 rounded-xl"
    >
      <AppIconTile id={app.id} size={46} unicode={app.icon} />
      <span className="text-[11.5px] leading-tight text-center line-clamp-2 w-full" style={{ color: 'var(--text-secondary)' }}>{app.name}</span>
    </button>
  )
}
