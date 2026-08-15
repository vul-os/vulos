import { useState, useEffect, useCallback, useRef, type ReactNode } from 'react'

// isRecord narrows an `unknown` value (parsed JSON from a fetch response — a
// trust boundary) to a plain object before any property access, same guard
// as lib/offlineAuth.ts's isRecord().
function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

interface PkgRepo {
  url?: string
  enabled?: boolean
}

interface PkgStatus {
  installed_count?: number
  repos?: PkgRepo[]
}

function toPkgRepo(x: Record<string, unknown>): PkgRepo {
  return {
    url: typeof x.url === 'string' ? x.url : undefined,
    enabled: typeof x.enabled === 'boolean' ? x.enabled : undefined,
  }
}

function toPkgStatus(x: unknown): PkgStatus | null {
  if (!isRecord(x)) return null
  return {
    installed_count: typeof x.installed_count === 'number' ? x.installed_count : undefined,
    repos: Array.isArray(x.repos) ? x.repos.filter(isRecord).map(toPkgRepo) : undefined,
  }
}

// Installed/search-result package entries. `name` is required by every call
// site (used as a React key and for the icon initial), so a malformed entry
// missing it degrades to '' rather than crashing the list render.
interface Pkg {
  name: string
  version?: string
  description?: string
}

interface SearchPkg extends Pkg {
  installed?: boolean
}

function toPkg(x: Record<string, unknown>): Pkg {
  return {
    name: typeof x.name === 'string' ? x.name : '',
    version: typeof x.version === 'string' ? x.version : undefined,
    description: typeof x.description === 'string' ? x.description : undefined,
  }
}

function toPkgList(x: unknown): Pkg[] | null {
  if (!Array.isArray(x)) return null
  return x.filter(isRecord).map(toPkg)
}

function toSearchPkgList(x: unknown): SearchPkg[] | null {
  if (!Array.isArray(x)) return null
  return x.filter(isRecord).map(r => ({
    ...toPkg(r),
    installed: typeof r.installed === 'boolean' ? r.installed : undefined,
  }))
}

// Reads the `{ error }` shape the /api/packages/* endpoints send on failure.
// Only a string `.error` is trusted (matches the isRecord narrowing pattern
// used elsewhere); a non-string/missing field falls back to an empty Error
// message exactly as `new Error(undefined)` did in the original untyped code.
async function extractErrorMessage(res: Response): Promise<string | undefined> {
  const data: unknown = await res.json()
  return isRecord(data) && typeof data.error === 'string' ? data.error : undefined
}

function errMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

const ICON_COLORS = [
  'bg-blue-500', 'bg-emerald-500', 'bg-violet-500', 'bg-amber-500',
  'bg-rose-500', 'bg-cyan-500', 'bg-indigo-500', 'bg-teal-500',
  'bg-pink-500', 'bg-orange-500', 'bg-lime-500', 'bg-fuchsia-500',
]

function colorForName(name: string): string {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) | 0
  return ICON_COLORS[Math.abs(h) % ICON_COLORS.length]
}

interface PkgIconProps {
  name: string
  size?: 'md' | 'lg'
}
function PkgIcon({ name, size = 'md' }: PkgIconProps) {
  const sz = size === 'lg' ? 'w-10 h-10 text-base' : 'w-8 h-8 text-sm'
  return (
    <div className={`${sz} ${colorForName(name)} rounded-xl flex items-center justify-center font-bold text-white shrink-0 shadow-sm`}>
      {name[0]?.toUpperCase()}
    </div>
  )
}

interface ActionMsg {
  text: string
  type: 'ok' | 'err' | 'info'
}

interface ToastProps {
  msg: ActionMsg
  onDismiss: () => void
}
function Toast({ msg, onDismiss }: ToastProps) {
  const [visible, setVisible] = useState(false)
  useEffect(() => {
    requestAnimationFrame(() => setVisible(true))
    if (msg.type !== 'info') {
      const t = setTimeout(() => { setVisible(false); setTimeout(onDismiss, 300) }, 3500)
      return () => clearTimeout(t)
    }
  }, [msg, onDismiss])

  const colors = msg.type === 'ok'
    ? 'bg-success-soft border-success-soft text-success'
    : msg.type === 'err'
    ? 'bg-danger-soft border-danger-soft text-danger'
    : 'accent-bg-soft accent-border-soft accent-text'

  const icon = msg.type === 'ok' ? (
    <svg className="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
    </svg>
  ) : msg.type === 'err' ? (
    <svg className="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
    </svg>
  ) : (
    <svg className="w-4 h-4 shrink-0 animate-spin" fill="none" viewBox="0 0 24 24">
      <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
      <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
    </svg>
  )

  return (
    <div role="status" aria-live="polite"
      className={`fixed bottom-5 right-4 sm:right-5 z-50 flex items-center gap-2.5 px-4 py-3 rounded-xl border backdrop-blur-sm shadow-xl text-sm font-medium transition-all duration-300 max-w-[calc(100vw-2rem)] ${colors} ${visible ? 'translate-y-0 opacity-100' : 'translate-y-4 opacity-0'}`}>
      {icon}
      <span className="min-w-0 truncate">{msg.text}</span>
    </div>
  )
}

type TabId = 'installed' | 'search' | 'updates' | 'repos'

interface SidebarItem {
  id: TabId
  label: string
  icon: ReactNode
}

const SIDEBAR_ITEMS: SidebarItem[] = [
  { id: 'installed', label: 'Installed', icon: (
    <svg className="w-[18px] h-[18px]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4" />
    </svg>
  )},
  { id: 'search', label: 'Find Packages', icon: (
    <svg className="w-[18px] h-[18px]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
    </svg>
  )},
  { id: 'updates', label: 'Updates', icon: (
    <svg className="w-[18px] h-[18px]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
    </svg>
  )},
  { id: 'repos', label: 'Repositories', icon: (
    <svg className="w-[18px] h-[18px]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M5 12h14M5 12a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v4a2 2 0 01-2 2M5 12a2 2 0 00-2 2v4a2 2 0 002 2h14a2 2 0 002-2v-4a2 2 0 00-2-2m-2-4h.01M17 16h.01" />
    </svg>
  )},
]

export default function Packages() {
  const [tab, setTab] = useState<TabId>('installed')
  const [status, setStatus] = useState<PkgStatus | null>(null)
  const [installed, setInstalled] = useState<Pkg[] | null>(null)
  const [installedError, setInstalledError] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [searchResults, setSearchResults] = useState<SearchPkg[] | null>(null)
  const [searching, setSearching] = useState(false)
  const [actionMsg, setActionMsg] = useState<ActionMsg | null>(null)
  const [updating, setUpdating] = useState(false)
  const [upgrading, setUpgrading] = useState(false)
  const [filter, setFilter] = useState('')
  const searchRef = useRef<HTMLInputElement>(null)

  const refreshStatus = () => fetch('/api/packages/status')
    .then(r => { if (!r.ok) throw new Error(`HTTP ${r.status}`); return r.json() })
    .then((data: unknown) => setStatus(toPkgStatus(data)))
    .catch(() => {})

  // BUILTIN-4: the old `.catch(() => {})` here swallowed the failure whole,
  // leaving `installed` null forever — and `!installed` is the spinner's only
  // condition, so a box with a dead package service showed "Loading
  // packages..." for the life of the window. A spinner that can never resolve
  // is a lie about what the app is doing.
  const refreshInstalled = () => {
    setInstalledError(null)
    return fetch('/api/packages/installed')
      .then(r => {
        // Without this, a 500 carrying a JSON error body parses fine and
        // toPkgList() answers [] for it, so an outage rendered as "this box
        // has no packages installed".
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((data: unknown) => setInstalled(toPkgList(data)))
      .catch((e: unknown) => setInstalledError(e instanceof Error ? e.message : String(e)))
  }

  useEffect(() => { refreshStatus(); refreshInstalled() }, [])

  const doSearch = useCallback(async () => {
    if (!search.trim()) { setSearchResults(null); return }
    setSearching(true)
    try {
      const res = await fetch('/api/packages/search?q=' + encodeURIComponent(search.trim()))
      const data: unknown = await res.json()
      setSearchResults(toSearchPkgList(data))
    } catch { setSearchResults([]) }
    setSearching(false)
  }, [search])

  useEffect(() => {
    if (tab !== 'search') return
    const t = setTimeout(doSearch, 400)
    return () => clearTimeout(t)
  }, [search, tab, doSearch])

  useEffect(() => {
    if (tab === 'search') searchRef.current?.focus()
  }, [tab])

  const action = async (endpoint: string, body: Record<string, unknown>, msg: string): Promise<void> => {
    setActionMsg({ text: msg + '...', type: 'info' })
    try {
      const res = await fetch(endpoint, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) throw new Error(await extractErrorMessage(res))
      setActionMsg({ text: msg + ' complete', type: 'ok' })
      refreshStatus(); refreshInstalled()
      if (tab === 'search') doSearch()
    } catch (e) {
      setActionMsg({ text: `Failed: ${errMessage(e)}`, type: 'err' })
    }
  }

  const doUpdate = async () => {
    setUpdating(true)
    setActionMsg({ text: 'Updating package index...', type: 'info' })
    try {
      const res = await fetch('/api/packages/update', { method: 'POST' })
      if (!res.ok) throw new Error(await extractErrorMessage(res))
      setActionMsg({ text: 'Package index updated', type: 'ok' })
      refreshStatus()
    } catch (e) {
      setActionMsg({ text: `Update failed: ${errMessage(e)}`, type: 'err' })
    }
    setUpdating(false)
  }

  const doUpgrade = async () => {
    setUpgrading(true)
    setActionMsg({ text: 'Upgrading all packages...', type: 'info' })
    try {
      const res = await fetch('/api/packages/upgrade', { method: 'POST' })
      if (!res.ok) throw new Error(await extractErrorMessage(res))
      setActionMsg({ text: 'System packages upgraded', type: 'ok' })
      refreshStatus(); refreshInstalled()
    } catch (e) {
      setActionMsg({ text: `Upgrade failed: ${errMessage(e)}`, type: 'err' })
    }
    setUpgrading(false)
  }

  const filteredInstalled = installed?.filter(
    p => !filter || p.name.toLowerCase().includes(filter.toLowerCase()) ||
         (p.description || '').toLowerCase().includes(filter.toLowerCase())
  ) || []

  return (
    <div className="h-full flex bg-neutral-950 text-neutral-200 overflow-hidden">
      {/* Sidebar (desktop) */}
      <div className="hidden sm:flex w-52 shrink-0 flex-col border-r border-neutral-800/60 bg-neutral-950/80">
        <div className="px-4 pt-5 pb-4">
          <h1 className="text-[15px] font-semibold tracking-tight">Software</h1>
          <p className="text-[12px] text-neutral-500 mt-0.5">Package Manager</p>
        </div>

        {/* Installed count card */}
        {status && (
          <div className="mx-3 mb-4 px-3 py-3 rounded-xl bg-neutral-900/60 border border-neutral-800/40">
            <div className="text-2xl font-bold text-[var(--text-primary)] leading-none tabular-nums">{status.installed_count?.toLocaleString()}</div>
            <div className="text-[12px] text-neutral-500 mt-1">packages installed</div>
          </div>
        )}

        {/* Nav items */}
        <nav className="flex-1 px-2 space-y-0.5">
          {SIDEBAR_ITEMS.map(item => (
            <button key={item.id} onClick={() => setTab(item.id)}
              aria-pressed={tab === item.id}
              className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-[13px] font-medium transition-colors duration-(--motion-fast) ${
                tab === item.id
                  ? 'accent-bg-soft accent-text'
                  : 'text-neutral-500 hover:text-neutral-300 hover:bg-neutral-900/60'
              }`}>
              <span className={tab === item.id ? 'accent-text' : 'text-neutral-600'}>{item.icon}</span>
              {item.label}
            </button>
          ))}
        </nav>

        {/* Sidebar bottom actions */}
        <div className="p-3 space-y-2 border-t border-neutral-800/40">
          <button onClick={doUpdate} disabled={updating}
            className="w-full flex items-center justify-center gap-2 px-3 py-2 text-xs font-medium bg-neutral-800/70 hover:bg-neutral-700/70 rounded-lg transition-colors duration-(--motion-fast) disabled:opacity-40">
            {updating ? (
              <svg className="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24">
                <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
              </svg>
            ) : (
              <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                <path strokeLinecap="round" strokeLinejoin="round" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
              </svg>
            )}
            {updating ? 'Refreshing...' : 'Refresh Index'}
          </button>
          <button onClick={doUpgrade} disabled={upgrading}
            className="w-full flex items-center justify-center gap-2 px-3 py-2 text-xs font-medium accent-bg hover:bg-[var(--accent-hover)] text-white rounded-lg transition-colors duration-(--motion-fast) disabled:opacity-40 shadow-sm">
            {upgrading ? (
              <svg className="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24">
                <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
              </svg>
            ) : (
              <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                <path strokeLinecap="round" strokeLinejoin="round" d="M7 11l5-5m0 0l5 5m-5-5v12" />
              </svg>
            )}
            {upgrading ? 'Upgrading...' : 'Upgrade All'}
          </button>
        </div>
      </div>

      {/* Main content */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">

        {/* Mobile top bar: title + horizontal tab strip (replaces sidebar < sm) */}
        <div className="sm:hidden shrink-0 border-b border-neutral-800/60 bg-neutral-950/80">
          <div className="flex items-baseline justify-between gap-2 px-4 pt-3">
            <h1 className="text-[15px] font-semibold tracking-tight">Software</h1>
            {status && (
              <span className="text-[12px] text-neutral-500 tabular-nums shrink-0">
                {status.installed_count?.toLocaleString()} installed
              </span>
            )}
          </div>
          <div className="flex gap-1 px-2 py-2 overflow-x-auto">
            {SIDEBAR_ITEMS.map(item => (
              <button key={item.id} onClick={() => setTab(item.id)}
                aria-pressed={tab === item.id}
                className={`shrink-0 flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors duration-(--motion-fast) ${
                  tab === item.id
                    ? 'accent-bg-soft accent-text'
                    : 'text-neutral-500 hover:text-neutral-300 hover:bg-neutral-900/60'
                }`}>
                <span className="shrink-0">{item.icon}</span>
                {item.label}
              </button>
            ))}
          </div>
        </div>


        {/* ===== INSTALLED TAB ===== */}
        {tab === 'installed' && (
          <>
            <div className="shrink-0 px-5 pt-5 pb-4">
              <h2 className="text-lg font-semibold mb-3">Installed Packages</h2>
              <div className="relative">
                <svg className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-neutral-600" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                </svg>
                <input type="text" value={filter} onChange={e => setFilter(e.target.value)}
                  placeholder="Filter installed packages..."
                  className="w-full pl-10 pr-4 py-2.5 bg-neutral-900/60 border border-neutral-800/50 rounded-xl text-sm text-neutral-200 outline-none placeholder:text-neutral-400 focus:border-neutral-600 focus:bg-neutral-900/80 transition-colors" />
                {filter && (
                  <button onClick={() => setFilter('')} className="absolute right-3 top-1/2 -translate-y-1/2 text-neutral-600 hover:text-neutral-400">
                    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                      <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
                    </svg>
                  </button>
                )}
              </div>
              {installed && (
                <p className="text-[12px] text-neutral-600 mt-2">
                  {filter ? `${filteredInstalled.length} matches` : `${installed.length} packages`}
                  {filteredInstalled.length > 300 && ' — showing first 300, narrow your filter'}
                </p>
              )}
            </div>

            <div className="flex-1 overflow-y-auto px-5 pb-5">
              {!installed && !installedError && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-8 h-8 animate-spin mb-3 text-neutral-600" fill="none" viewBox="0 0 24 24">
                    <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                    <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
                  </svg>
                  <span className="text-sm">Loading packages...</span>
                </div>
              )}

              {/* Deliberately spinner-free: this replaces the spinner, it does
                  not sit beside it. */}
              {installedError && (
                <div role="alert" className="flex flex-col items-center justify-center py-16 px-6 text-center text-neutral-500">
                  <span aria-hidden="true" className="text-2xl leading-none mb-2 text-neutral-700">⚠</span>
                  <span className="text-sm text-neutral-300">Could not list installed packages</span>
                  <span className="text-xs text-neutral-500 mt-1 max-w-sm break-words">
                    The package service did not answer ({installedError}). This is not the same as having no
                    packages installed.
                  </span>
                  <button onClick={() => { refreshStatus(); refreshInstalled() }}
                    className="mt-3 px-3 py-1.5 text-xs font-medium bg-neutral-800 hover:bg-neutral-700 rounded-lg transition-colors duration-(--motion-fast)">
                    Try again
                  </button>
                </div>
              )}

              {/* BUILTIN-3: with an empty list and no filter typed, none of the
                  branches around this one matched and the pane rendered as a
                  blank rectangle. */}
              {installed && installed.length === 0 && !filter && (
                <div className="flex flex-col items-center justify-center py-16 px-6 text-center text-neutral-500">
                  <svg className="w-10 h-10 mb-3 text-neutral-700" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4" />
                  </svg>
                  <span className="text-sm">No packages installed</span>
                  <span className="text-xs text-neutral-600 mt-1">
                    Anything you install from the Search tab will be listed here.
                  </span>
                </div>
              )}

              {installed && filteredInstalled.length === 0 && filter && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-10 h-10 mb-3 text-neutral-700" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                  </svg>
                  <span className="text-sm">No matches for "{filter}"</span>
                </div>
              )}

              {installed && filteredInstalled.length > 0 && (
                <div className="space-y-1">
                  {filteredInstalled.slice(0, 300).map(p => (
                    <div key={p.name}
                      className="group flex items-center gap-3 px-3.5 py-2.5 rounded-xl hover:bg-neutral-900/70 transition-colors">
                      <PkgIcon name={p.name} />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-medium text-neutral-100 truncate">{p.name}</span>
                          <span className="shrink-0 text-[12px] px-1.5 py-0.5 rounded-md bg-neutral-800/80 text-neutral-500 font-mono">{p.version}</span>
                        </div>
                        {p.description && (
                          <p className="text-[12px] text-neutral-500 mt-0.5 truncate leading-snug">{p.description}</p>
                        )}
                      </div>
                      <button
                        onClick={() => action('/api/packages/remove', { name: p.name }, `Removing ${p.name}`)}
                        className="shrink-0 opacity-0 group-hover:opacity-100 focus-visible:opacity-100 px-2.5 py-1 text-[12px] font-medium text-danger bg-danger-soft hover:bg-[color-mix(in_srgb,var(--status-danger)_24%,transparent)] rounded-lg transition-all duration-(--motion-fast)">
                        Remove
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        )}

        {/* ===== SEARCH TAB ===== */}
        {tab === 'search' && (
          <>
            <div className="shrink-0 px-5 pt-5 pb-4">
              <h2 className="text-lg font-semibold mb-3">Find Packages</h2>
              <div className="relative">
                <svg className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-neutral-600" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                </svg>
                <input ref={searchRef} type="text" value={search} onChange={e => setSearch(e.target.value)}
                  placeholder="Search for packages to install..."
                  className="w-full pl-10 pr-4 py-2.5 bg-neutral-900/60 border border-neutral-800/50 rounded-xl text-sm text-neutral-200 outline-none placeholder:text-neutral-400 focus:border-neutral-600 focus:bg-neutral-900/80 transition-colors" />
                {search && (
                  <button onClick={() => { setSearch(''); setSearchResults(null) }} className="absolute right-3 top-1/2 -translate-y-1/2 text-neutral-600 hover:text-neutral-400">
                    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                      <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
                    </svg>
                  </button>
                )}
              </div>
            </div>

            <div className="flex-1 overflow-y-auto px-5 pb-5">
              {searching && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-8 h-8 animate-spin mb-3 text-neutral-600" fill="none" viewBox="0 0 24 24">
                    <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                    <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
                  </svg>
                  <span className="text-sm">Searching...</span>
                </div>
              )}

              {!searching && !searchResults && !search && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-12 h-12 mb-3 text-neutral-800" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                  </svg>
                  <span className="text-sm text-neutral-500">Search for packages to install</span>
                  <span className="text-xs text-neutral-700 mt-1">Results appear as you type</span>
                </div>
              )}

              {!searching && searchResults?.length === 0 && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-10 h-10 mb-3 text-neutral-700" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4" />
                  </svg>
                  <span className="text-sm">No packages found</span>
                </div>
              )}

              {!searching && searchResults && searchResults.length > 0 && (
                <div className="space-y-1">
                  {searchResults.slice(0, 100).map(p => (
                    <div key={p.name + p.version}
                      className="group flex items-center gap-3 px-3.5 py-2.5 rounded-xl hover:bg-neutral-900/70 transition-colors">
                      <PkgIcon name={p.name} />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-medium text-neutral-100 truncate">{p.name}</span>
                          <span className="shrink-0 text-[12px] px-1.5 py-0.5 rounded-md bg-neutral-800/80 text-neutral-500 font-mono">{p.version}</span>
                          {p.installed && (
                            <span className="shrink-0 text-[12px] px-1.5 py-0.5 rounded-md bg-success-soft text-success font-medium">installed</span>
                          )}
                        </div>
                        {p.description && (
                          <p className="text-[12px] text-neutral-500 mt-0.5 truncate leading-snug">{p.description}</p>
                        )}
                      </div>
                      {p.installed ? (
                        <button
                          onClick={() => action('/api/packages/remove', { name: p.name }, `Removing ${p.name}`)}
                          className="shrink-0 px-3 py-1.5 text-[12px] font-medium text-danger bg-danger-soft hover:bg-[color-mix(in_srgb,var(--status-danger)_24%,transparent)] rounded-lg transition-colors duration-(--motion-fast)">
                          Remove
                        </button>
                      ) : (
                        <button
                          onClick={() => action('/api/packages/install', { name: p.name }, `Installing ${p.name}`)}
                          className="shrink-0 px-3 py-1.5 text-[12px] font-medium accent-text accent-bg-soft hover:bg-[color-mix(in_srgb,var(--accent)_24%,transparent)] rounded-lg transition-colors duration-(--motion-fast)">
                          Install
                        </button>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        )}

        {/* ===== UPDATES TAB ===== */}
        {tab === 'updates' && (
          <>
            <div className="shrink-0 px-5 pt-5 pb-4">
              <h2 className="text-lg font-semibold mb-1">Updates</h2>
              <p className="text-xs text-neutral-500">Keep your system up to date</p>
            </div>
            <div className="flex-1 overflow-y-auto px-5 pb-5">
              <div className="rounded-xl border border-neutral-800/50 bg-neutral-900/30 p-6 flex flex-col items-center text-center">
                <div className="w-14 h-14 rounded-2xl accent-bg-soft flex items-center justify-center mb-4">
                  <svg className="w-7 h-7 accent-text" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                  </svg>
                </div>
                <h3 className="text-sm font-medium text-neutral-200 mb-1">System Updates</h3>
                <p className="text-xs text-neutral-500 mb-5 max-w-xs">
                  Refresh the package index and upgrade all packages to their latest versions.
                </p>
                <div className="flex gap-3">
                  <button onClick={doUpdate} disabled={updating}
                    className="flex items-center gap-2 px-4 py-2 text-xs font-medium bg-neutral-800 hover:bg-neutral-700 rounded-lg transition-colors disabled:opacity-40">
                    {updating ? (
                      <svg className="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24">
                        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
                      </svg>
                    ) : (
                      <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                        <path strokeLinecap="round" strokeLinejoin="round" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                      </svg>
                    )}
                    {updating ? 'Refreshing...' : 'Refresh Index'}
                  </button>
                  <button onClick={doUpgrade} disabled={upgrading}
                    className="flex items-center gap-2 px-4 py-2 text-xs font-medium accent-bg hover:bg-[var(--accent-hover)] text-white rounded-lg transition-colors duration-(--motion-fast) disabled:opacity-40 shadow-sm">
                    {upgrading ? (
                      <svg className="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24">
                        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
                      </svg>
                    ) : (
                      <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                        <path strokeLinecap="round" strokeLinejoin="round" d="M7 11l5-5m0 0l5 5m-5-5v12" />
                      </svg>
                    )}
                    {upgrading ? 'Upgrading...' : 'Upgrade All'}
                  </button>
                </div>
              </div>
            </div>
          </>
        )}

        {/* ===== REPOS TAB ===== */}
        {tab === 'repos' && (
          <>
            <div className="shrink-0 px-5 pt-5 pb-4">
              <h2 className="text-lg font-semibold mb-1">Repositories</h2>
              <p className="text-xs text-neutral-500">Configured package sources</p>
            </div>
            <div className="flex-1 overflow-y-auto px-5 pb-5">
              {!status?.repos?.length && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-10 h-10 mb-3 text-neutral-700" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M5 12h14M5 12a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v4a2 2 0 01-2 2M5 12a2 2 0 00-2 2v4a2 2 0 002 2h14a2 2 0 002-2v-4a2 2 0 00-2-2m-2-4h.01M17 16h.01" />
                  </svg>
                  <span className="text-sm">No repositories configured</span>
                </div>
              )}
              {status?.repos && (
                <div className="space-y-2">
                  {status.repos.map((r, i) => (
                    <div key={i}
                      className="flex items-center gap-3 px-4 py-3.5 rounded-xl bg-neutral-900/40 border border-neutral-800/40 hover:border-neutral-700/40 transition-colors">
                      <div className={`w-2.5 h-2.5 rounded-full shrink-0 ${r.enabled ? 'bg-success shadow-sm' : 'bg-neutral-600'}`} />
                      <div className="min-w-0 flex-1">
                        <span className="text-sm font-mono text-neutral-300 truncate block">{r.url}</span>
                      </div>
                      <span className={`shrink-0 text-[12px] px-2 py-1 rounded-lg font-medium ${
                        r.enabled
                          ? 'bg-success-soft text-success'
                          : 'bg-neutral-800 text-neutral-500'
                      }`}>
                        {r.enabled ? 'Enabled' : 'Disabled'}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        )}
      </div>

      {/* Toast notification */}
      {actionMsg && <Toast msg={actionMsg} onDismiss={() => setActionMsg(null)} />}
    </div>
  )
}
