import { useState, useEffect, useRef, useCallback, useMemo, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { refreshInstalled } from '../../core/AppRegistry'
import { AppIconTile } from '../../core/AppIcons'
import { useFocusTrap } from '../../shell/useFocusTrap'
import { useHubMode } from './hubMode'
import { toArchAvailability, fetchBoxArch, type ArchAvailability } from './arch'
import './apphub.css'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function errorMessage(err: unknown): string {
  return isRecord(err) && typeof err.message === 'string' ? err.message : String(err)
}

// ── Wire shapes (untyped JSON in, narrowed before use) ─────────────────────────
//
// The App Hub browses a RICHER shape than core/AppRegistry.ts's `App` (the
// shell-launcher-tile type): GET /api/store/registry returns backend's
// RegistryListEntry (services/appnet/registry.go), which carries store-only
// fields — flatpak_id, versions, latest, vetted, author, homepage, license —
// that `App` does not model. Declared locally rather than reusing `App`,
// which would silently drop those fields.
interface StoreApp {
  id: string
  name: string
  type?: string
  /**
   * The BOX's verdict for this app — which rung it lands on, the badge, the
   * sentence and the architectures it needs.
   *
   * `arch` is deliberately NOT narrowed onto this type even though the registry
   * still sends it. Keeping it would leave the raw declaration within reach of
   * the next person who wants "just a quick check", which is exactly how
   * `app.arch.includes(systemArch)` got here. The only architecture fact this
   * component can see is one the box has already decided.
   */
  availability: ArchAvailability | null
  flatpak_id?: string
  description: string
  category: string
  author?: string
  icon?: string
  vetted?: boolean
  versions?: string[]
  latest?: string
  installed?: boolean
  homepage?: string
  license?: string
  keywords?: string[]
}

function toStoreApp(x: unknown): StoreApp | null {
  if (!isRecord(x) || typeof x.id !== 'string' || typeof x.name !== 'string') return null
  return {
    id: x.id,
    name: x.name,
    type: typeof x.type === 'string' ? x.type : undefined,
    availability: toArchAvailability(x),
    flatpak_id: typeof x.flatpak_id === 'string' ? x.flatpak_id : undefined,
    description: typeof x.description === 'string' ? x.description : '',
    category: typeof x.category === 'string' ? x.category : '',
    author: typeof x.author === 'string' ? x.author : undefined,
    icon: typeof x.icon === 'string' ? x.icon : undefined,
    vetted: typeof x.vetted === 'boolean' ? x.vetted : undefined,
    versions: Array.isArray(x.versions) ? x.versions.filter((v): v is string => typeof v === 'string') : undefined,
    latest: typeof x.latest === 'string' ? x.latest : undefined,
    installed: typeof x.installed === 'boolean' ? x.installed : undefined,
    homepage: typeof x.homepage === 'string' ? x.homepage : undefined,
    license: typeof x.license === 'string' ? x.license : undefined,
    keywords: Array.isArray(x.keywords) ? x.keywords.filter((k): k is string => typeof k === 'string') : undefined,
  }
}

function toStoreApps(x: unknown): StoreApp[] {
  return Array.isArray(x) ? x.map(toStoreApp).filter((a): a is StoreApp => a !== null) : []
}

// GET /api/store/installed returns []*AppManifest (services/appnet/manifest.go).
// Only `id` is ever read here (membership + count), so that is all this narrows.
interface InstalledAppRef {
  id: string
}

function toInstalledAppRefs(x: unknown): InstalledAppRef[] {
  return Array.isArray(x)
    ? x.filter((a): a is InstalledAppRef => isRecord(a) && typeof a.id === 'string').map(a => ({ id: a.id }))
    : []
}

// GET /api/packages/cache reports whether the Debian package index has been
// read. It also carries an `arch`, which this component NO LONGER READS: an
// endpoint about the apt index is not the home for the box's identity, its arch
// is null until the index has been read once, and the box now states its own
// architecture on every entry it answers about. Two sources for one fact is the
// shape that produces a hub disagreeing with itself.
function toPackageCacheReady(x: unknown): boolean {
  return isRecord(x) && typeof x.ready === 'boolean' ? x.ready : false
}

// ── Labels ────────────────────────────────────────────────────────────────────

const CATEGORY_LABELS: Record<string, string> = {
  all: 'All apps',
  internet: 'Internet',
  media: 'Media',
  developer: 'Developer',
  productivity: 'Productivity',
  network: 'Network',
  database: 'Database',
  system: 'System',
}

/**
 * A registry category the label table has never heard of still has to render.
 *
 * The shipped catalogue contains `games`, `storage` and `development`, none of
 * which are in the table, so the sidebar listed them as bare lowercase slugs
 * with no icon, wedged between "Productivity" and "System". Three of eight rows
 * looked like a rendering fault. Title-casing the slug is not as good as a
 * curated label, but it is a correct one for every value the registry can ever
 * invent, and it is what makes the list read as a list.
 */
function categoryLabel(slug: string): string {
  return CATEGORY_LABELS[slug] || slug.charAt(0).toUpperCase() + slug.slice(1)
}

const CATEGORY_ICONS: Record<string, string> = {
  all: 'M4 8h4V4H4v4zm6 12h4v-4h-4v4zm-6 0h4v-4H4v4zm0-6h4v-4H4v4zm6 0h4v-4h-4v4zm6-10v4h4V4h-4zm-6 4h4V4h-4v4zm6 6h4v-4h-4v4zm0 6h4v-4h-4v4z',
  internet: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z',
  media: 'M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z',
  developer: 'M9.4 16.6L4.8 12l4.6-4.6L8 6l-6 6 6 6 1.4-1.4zm5.2 0l4.6-4.6-4.6-4.6L16 6l6 6-6 6-1.4-1.4z',
  development: 'M9.4 16.6L4.8 12l4.6-4.6L8 6l-6 6 6 6 1.4-1.4zm5.2 0l4.6-4.6-4.6-4.6L16 6l6 6-6 6-1.4-1.4z',
  productivity: 'M19 3h-4.18C14.4 1.84 13.3 1 12 1c-1.3 0-2.4.84-2.82 2H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm-7 0c.55 0 1 .45 1 1s-.45 1-1 1-1-.45-1-1 .45-1 1-1zm2 14H7v-2h7v2zm3-4H7v-2h10v2zm0-4H7V7h10v2z',
  network: 'M1 9l2 2c4.97-4.97 13.03-4.97 18 0l2-2C16.93 2.93 7.08 2.93 1 9zm8 8l3 3 3-3c-1.65-1.66-4.34-1.66-6 0zm-4-4l2 2c2.76-2.76 7.24-2.76 10 0l2-2C15.14 9.14 8.87 9.14 5 13z',
  database: 'M12 3C7.58 3 4 4.79 4 7v10c0 2.21 3.58 4 8 4s8-1.79 8-4V7c0-2.21-3.58-4-8-4zm0 2c3.87 0 6 1.5 6 2s-2.13 2-6 2-6-1.5-6-2 2.13-2 6-2zM6 17v-1.29c1.58.74 3.74 1.29 6 1.29s4.42-.55 6-1.29V17c0 .5-2.13 2-6 2s-6-1.5-6-2z',
  storage: 'M12 3C7.58 3 4 4.79 4 7v10c0 2.21 3.58 4 8 4s8-1.79 8-4V7c0-2.21-3.58-4-8-4zm0 2c3.87 0 6 1.5 6 2s-2.13 2-6 2-6-1.5-6-2 2.13-2 6-2zM6 17v-1.29c1.58.74 3.74 1.29 6 1.29s4.42-.55 6-1.29V17c0 .5-2.13 2-6 2s-6-1.5-6-2z',
  games: 'M21 6H3c-1.1 0-2 .9-2 2v8c0 1.1.9 2 2 2h18c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2zM11 13H9v2H7v-2H5v-2h2V9h2v2h2v2zm4.5 2a1.5 1.5 0 110-3 1.5 1.5 0 010 3zm3-3a1.5 1.5 0 110-3 1.5 1.5 0 010 3z',
  system: 'M19.14 12.94c.04-.3.06-.61.06-.94 0-.32-.02-.64-.07-.94l2.03-1.58c.18-.14.23-.41.12-.61l-1.92-3.32c-.12-.22-.37-.29-.59-.22l-2.39.96c-.5-.38-1.03-.7-1.62-.94l-.36-2.54c-.04-.24-.24-.41-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.57-1.62.94l-2.39-.96c-.22-.08-.47 0-.59.22L2.74 8.87c-.12.21-.08.47.12.61l2.03 1.58c-.05.3-.07.62-.07.94s.02.64.07.94l-2.03 1.58c-.18.14-.23.41-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32c.12-.22.07-.47-.12-.61l-2.01-1.58zM12 15.6c-1.98 0-3.6-1.62-3.6-3.6s1.62-3.6 3.6-3.6 3.6 1.62 3.6 3.6-1.62 3.6-3.6 3.6z',
}

const FALLBACK_CATEGORY_ICON = 'M4 4h7v7H4V4zm9 0h7v7h-7V4zM4 13h7v7H4v-7zm9 0h7v7h-7v-7z'

type SourceKey = 'flatpak' | 'apt' | 'web'
type TypeKey = 'web' | 'desktop' | 'service'

const SOURCE_LABEL: Record<SourceKey, string> = { flatpak: 'Flatpak', apt: 'Apt', web: 'Web' }
const APPTYPE_LABEL: Record<TypeKey, string> = { web: 'Web', desktop: 'Streamed', service: 'Service' }

/**
 * Badge hues.
 *
 * These are the OS chart tokens, and they are used ONLY as a 6px dot — never as
 * text colour. The badges this replaced were `text-sky-400` / `text-amber-400` /
 * `text-emerald-400` over a 10%-alpha fill of the same hue, which on the light
 * theme's surfaces measures between 1.6:1 and 2.9:1 against a 4.5:1 requirement.
 * The colour coding was worth keeping; drawing words in it was not.
 */
const TYPE_DOT: Record<TypeKey, string> = {
  web: 'var(--chart-1)',
  desktop: 'var(--chart-4)',
  service: 'var(--chart-2)',
}

function appTypeKey(app: StoreApp): TypeKey | null {
  if (app.type === 'web' || app.type === 'desktop' || app.type === 'service') return app.type
  return null
}

function getSourceType(app: StoreApp): SourceKey {
  if (app.flatpak_id) return 'flatpak'
  if (app.type === 'web') return 'web'
  return 'apt'
}

function SourceBadge({ app }: { app: StoreApp }) {
  return <span className="hub-badge">{SOURCE_LABEL[getSourceType(app)]}</span>
}

/**
 * The per-card badge derived from the `type` field.
 *
 * The two badges answer different questions — SourceBadge is where the app comes
 * FROM (Apt / Flatpak / Web), AppTypeBadge is how it RUNS (Web / Streamed /
 * Service) — and for most apps that reads well: "Apt · Streamed" tells you both
 * things at once.
 *
 * For a web app the two axes collapse onto the same word, and every web card in
 * the catalogue rendered "WEB WEB" side by side. That is not a second fact, it is
 * the same fact twice, and on a 63-app grid it was the most repeated text on the
 * screen. When the labels coincide the type badge is suppressed and the source
 * badge carries it alone.
 */
function AppTypeBadge({ app }: { app: StoreApp }) {
  const key = appTypeKey(app)
  if (!key) return null
  const label = APPTYPE_LABEL[key]
  if (label.toLowerCase() === SOURCE_LABEL[getSourceType(app)].toLowerCase()) return null
  return (
    <span className="hub-badge">
      <i className="hub-dot" style={{ '--hub-dot-color': TYPE_DOT[key] } as React.CSSProperties} aria-hidden="true" />
      {label}
    </span>
  )
}

// ── Icons ─────────────────────────────────────────────────────────────────────

const I = {
  search: 'M11 11l3.2 3.2',
  check: 'M8 16A8 8 0 108 0a8 8 0 000 16zm3.78-9.72a.75.75 0 00-1.06-1.06L7 8.94 5.28 7.22a.75.75 0 00-1.06 1.06l2.5 2.5a.75.75 0 001.06 0l4-4z',
  cross: 'M5.28 4.22a.75.75 0 00-1.06 1.06L6.94 8l-2.72 2.72a.75.75 0 101.06 1.06L8 9.06l2.72 2.72a.75.75 0 101.06-1.06L9.06 8l2.72-2.72a.75.75 0 00-1.06-1.06L8 6.94 5.28 4.22z',
  block: 'M8 16A8 8 0 108 0a8 8 0 000 16zM4.22 4.22a.75.75 0 011.06 0L8 6.94l2.72-2.72a.75.75 0 111.06 1.06L9.06 8l2.72 2.72a.75.75 0 11-1.06 1.06L8 9.06l-2.72 2.72a.75.75 0 01-1.06-1.06L6.94 8 4.22 5.28a.75.75 0 010-1.06z',
  alert: 'M8 16A8 8 0 108 0a8 8 0 000 16zM8.75 4.5a.75.75 0 00-1.5 0v4.25a.75.75 0 001.5 0V4.5zM8 12.5a1 1 0 100-2 1 1 0 000 2z',
  info: 'M8 16A8 8 0 108 0a8 8 0 000 16zM8.75 7a.75.75 0 00-1.5 0v4.5a.75.75 0 001.5 0V7zM8 5.5a1 1 0 100-2 1 1 0 000 2z',
  back: 'M10 3L5 8l5 5',
}

function Glyph({ d, viewBox = '0 0 16 16' }: { d: string; viewBox?: string }) {
  return <svg viewBox={viewBox} fill="currentColor" aria-hidden="true"><path fillRule="evenodd" d={d} clipRule="evenodd" /></svg>
}

// ── Component ─────────────────────────────────────────────────────────────────

type Tab = 'browse' | 'installed'
type TypeFilter = 'all' | TypeKey

export default function AppHub() {
  const [apps, setApps] = useState<StoreApp[]>([])
  const [installed, setInstalled] = useState<InstalledAppRef[]>([])
  const [loading, setLoading] = useState(true)
  // A failed registry fetch used to be indistinguishable from an empty
  // catalogue: the catch set `apps` to [] and the grid rendered "No apps match
  // your search". A box with no network told the user its store was empty.
  const [loadError, setLoadError] = useState<string | null>(null)
  const [installing, setInstalling] = useState<string | null>(null)
  const [installPhase, setInstallPhase] = useState('')
  const [uninstalling, setUninstalling] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState('all')
  const [appTypeFilter, setAppTypeFilter] = useState<TypeFilter>('all')
  const [selectedApp, setSelectedApp] = useState<StoreApp | null>(null)
  const [selectedVersion, setSelectedVersion] = useState('')
  const [tab, setTab] = useState<Tab>('browse')
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)
  const [cacheReady, setCacheReady] = useState<boolean | null>(null)
  const [systemArch, setSystemArch] = useState<string | null>(null)
  // Default OFF: see browseList — an app that vanishes teaches the user
  // nothing, an app labelled "Needs amd64" teaches them something true about
  // their hardware.
  const [onlyCompatible, setOnlyCompatible] = useState(false)
  const [updatingCache, setUpdatingCache] = useState(false)

  const rootRef = useRef<HTMLDivElement | null>(null)
  const searchRef = useRef<HTMLInputElement | null>(null)
  const gridRef = useRef<HTMLDivElement | null>(null)
  const mode = useHubMode(rootRef)
  const modal = mode !== 'wide'
  const trapRef = useFocusTrap(!!selectedApp && modal)

  const fetchData = useCallback(async () => {
    setLoading(true)
    try {
      const [regRes, instRes, cacheRes, dedicatedArch] = await Promise.all([
        fetch('/api/store/registry'),
        fetch('/api/store/installed'),
        fetch('/api/packages/cache'),
        fetchBoxArch(),
      ])
      if (!regRes.ok) throw new Error(`registry unavailable (HTTP ${regRes.status})`)
      const regData = toStoreApps(await regRes.json())
      const instData = toInstalledAppRefs(await instRes.json().catch(() => []))
      setApps(regData)
      setInstalled(instData)
      setCacheReady(toPackageCacheReady(await cacheRes.json().catch(() => ({}))))
      // The BOX's architecture, for the header label only.
      //
      // The catalogue's own answer wins: every entry carries the box_arch the
      // verdicts were computed against, so the label and the badges cannot
      // describe two different machines. /api/system/arch is the fallback for
      // the case the catalogue cannot cover — an empty catalogue, where there
      // is no entry to read it from.
      const fromCatalogue = regData.find(a => a.availability?.boxArch)?.availability?.boxArch
      setSystemArch(fromCatalogue || dedicatedArch)
      setLoadError(null)
    } catch (e) {
      setApps([])
      setInstalled([])
      setLoadError(errorMessage(e))
    }
    setLoading(false)
  }, [])

  useEffect(() => { fetchData() }, [fetchData])

  const updateAptCache = async () => {
    setUpdatingCache(true)
    setError(null)
    try {
      const res = await fetch('/api/packages/update', { method: 'POST' })
      if (!res.ok) throw new Error('Update failed')
      setCacheReady(true)
    } catch (e) {
      setError('Failed to update package index: ' + errorMessage(e))
    }
    setUpdatingCache(false)
  }

  const installApp = async (appId: string, version?: string) => {
    if (installing) return
    setInstalling(appId)
    const app = apps.find(a => a.id === appId)
    setInstallPhase(app?.flatpak_id ? 'Downloading from Flathub…' : 'Installing packages…')
    setError(null)
    setSuccess(null)
    try {
      const res = await fetch('/api/store/registry/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ app_id: appId, version: version || '' }),
      })
      const data: unknown = await res.json().catch(() => ({}))
      if (!res.ok) {
        const msg = (isRecord(data) && typeof data.error === 'string' ? data.error : '') || 'Install failed'
        const detail = isRecord(data) && typeof data.detail === 'string' ? data.detail : ''
        throw new Error(detail ? `${msg}\n${detail}` : msg)
      }
      setSuccess(`${app?.name || appId} installed`)
      await refreshInstalled()
      await fetchData()
    } catch (e) {
      setError(errorMessage(e))
    }
    setInstalling(null)
    setInstallPhase('')
  }

  const uninstallApp = async (appId: string) => {
    if (uninstalling) return
    setUninstalling(appId)
    setError(null)
    try {
      const res = await fetch('/api/store/uninstall', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ app_id: appId }),
      })
      if (!res.ok) throw new Error('Uninstall failed')
      setSuccess(`${apps.find(a => a.id === appId)?.name || appId} removed`)
      await refreshInstalled()
      await fetchData()
      if (selectedApp?.id === appId) setSelectedApp(null)
    } catch (e) {
      setError(errorMessage(e))
    }
    setUninstalling(null)
  }

  /**
   * Can this box run this app?
   *
   * ASKED, not answered. The verdict is computed by services/appnet/arch.go's
   * EvaluateArch on the box, which is the only place that can see binfmt_misc,
   * box64 and qemu-user on PATH, which of the two would serve the app, the
   * architectures the installed flatpak will resolve, and the recipe's delivery
   * mechanism. This component has none of those and used to decide anyway:
   * first with `app.arch.includes(systemArch)`, which never matched `x86_64`
   * against `amd64`, and then with a spelling fold that was right and was still
   * a second implementation of the box's own policy.
   *
   * `null` means the box did not answer, and it is NOT a verdict — see
   * toArchAvailability. An app with no answer is offered with no claim
   * attached, which is what `installableIn` and the badges below check for.
   */
  const availabilityOf = useCallback((app: StoreApp) => app.availability, [])

  /** True only when the box SAID this app cannot be installed here. An
   *  unanswered app is not counted as refused. */
  const refused = useCallback(
    (app: StoreApp) => !!app.availability && !app.availability.installable,
    [],
  )

  const installedIds = useMemo(() => new Set(installed.map(a => a.id)), [installed])
  const isInstalled = useCallback(
    (app: StoreApp) => !!app.installed || installedIds.has(app.id),
    [installedIds],
  )

  /** Apps in the current TAB and TYPE filter — the population the category
   *  counts are drawn from, so a count never promises rows a click cannot show. */
  const scoped = useMemo(() => apps.filter(app => {
    if (tab === 'installed' && !isInstalled(app)) return false
    if (appTypeFilter !== 'all' && appTypeKey(app) !== appTypeFilter) return false
    return true
  }), [apps, tab, appTypeFilter, isInstalled])

  const query = search.trim().toLowerCase()

  const matched = useMemo(() => scoped.filter(app => {
    if (category !== 'all' && app.category !== category) return false
    if (!query) return true
    return app.name.toLowerCase().includes(query) ||
      app.description.toLowerCase().includes(query) ||
      app.id.toLowerCase().includes(query) ||
      (app.author || '').toLowerCase().includes(query) ||
      (app.keywords || []).some(k => k.toLowerCase().includes(query))
  }), [scoped, category, query])

  /** How many of the current matches this box will not install. */
  const incompatibleCount = useMemo(
    () => matched.filter(refused).length,
    [matched, refused],
  )

  /**
   * The list the grid renders.
   *
   * ── Shown-with-a-reason, not hidden ─────────────────────────────────────
   *
   * Vulos publishes both amd64 and arm64 images, and a share of Flathub's
   * desktop catalogue is x86_64-only — Lutris, Bottles, Heroic and ProtonUp-Qt
   * among what this product actually ships. (The most famous x86_64-only names
   * are proprietary and therefore out of the catalogue entirely for now, under
   * roadmap/APP-CATALOG.md policy 1a; that shrinks the incompatible set without
   * emptying it, which is why this still has to be got right.)
   *
   * Hiding them silently produces the worst outcome: the user searches for
   * Lutris, finds nothing, and learns nothing. They cannot tell "this OS has
   * never heard of it" from "your box cannot run it", and only one of those is
   * true. So incompatible apps are always RENDERED, always LABELLED
   * with the architecture they need, and sorted BELOW the ones that work —
   * which gets the practical benefit of a filter (what you can install is at
   * the top) without the cost of an app vanishing.
   *
   * `onlyCompatible` then exists for the user who wants the shorter list, and
   * it deliberately does NOT apply while searching: a search is a question
   * about a specific app, and answering "no results" to someone who typed
   * "steam" — on a box that simply cannot run it — is the exact failure the
   * default avoids.
   */
  const browseList = useMemo(() => {
    const list = (onlyCompatible && !query) ? matched.filter(a => !refused(a)) : matched
    // Stable: equal-rank apps keep the registry's alphabetical order.
    return [...list].sort((a, b) => Number(refused(a)) - Number(refused(b)))
  }, [matched, onlyCompatible, query, refused])

  const categories = useMemo(() => {
    const counts = new Map<string, number>()
    for (const a of scoped) if (a.category) counts.set(a.category, (counts.get(a.category) || 0) + 1)
    return [
      { id: 'all', count: scoped.length },
      ...[...counts.entries()].sort((a, b) => a[0].localeCompare(b[0])).map(([id, count]) => ({ id, count })),
    ]
  }, [scoped])

  // A category can go empty when the tab or type filter changes underneath it,
  // which would leave the user staring at "no apps" with no clue why. Fall back
  // to All rather than showing a dead end.
  useEffect(() => {
    if (category !== 'all' && !categories.some(c => c.id === category)) setCategory('all')
  }, [categories, category])

  const selectApp = (app: StoreApp) => {
    setSelectedApp(app)
    setSelectedVersion(app.latest || '')
    setError(null)
  }

  // Keep the open panel in step with refetched data (install/uninstall rewrite
  // `installed`), so the panel's own action button is never a frame behind the
  // card's.
  const liveSelected = selectedApp ? apps.find(a => a.id === selectedApp.id) || selectedApp : null

  /**
   * Keyboard navigation for the grid.
   *
   * Arrow keys move between cards along the REAL column count, read from the
   * resolved `grid-template-columns` rather than recomputed from a breakpoint —
   * the grid is `auto-fill`, so nothing in JS knows how many columns there are
   * until the browser has laid it out.
   */
  const onGridKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const keys = ['ArrowRight', 'ArrowLeft', 'ArrowDown', 'ArrowUp', 'Home', 'End']
    if (!keys.includes(e.key)) return
    const grid = gridRef.current
    if (!grid) return
    const cards = Array.from(grid.querySelectorAll<HTMLButtonElement>('.hub-card-open'))
    const here = cards.indexOf(document.activeElement as HTMLButtonElement)
    if (here < 0) return
    const cols = Math.max(1, getComputedStyle(grid).gridTemplateColumns.split(' ').filter(Boolean).length)
    let next = here
    if (e.key === 'ArrowRight') next = here + 1
    else if (e.key === 'ArrowLeft') next = here - 1
    else if (e.key === 'ArrowDown') next = here + cols
    else if (e.key === 'ArrowUp') next = here - cols
    else if (e.key === 'Home') next = 0
    else if (e.key === 'End') next = cards.length - 1
    if (next < 0 || next >= cards.length) return
    e.preventDefault()
    cards[next].focus()
  }

  /**
   * Hub-scoped shortcuts. Bound to the hub's own element, never to window: a
   * builtin that steals ⌘F from the whole OS is a worse bug than not having the
   * shortcut, and the shell already owns ⌘K.
   */
  const onRootKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const target = e.target as HTMLElement
    const typing = target.tagName === 'INPUT' || target.tagName === 'TEXTAREA'
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'f') {
      e.preventDefault()
      searchRef.current?.focus()
      searchRef.current?.select()
      return
    }
    if (e.key === '/' && !typing && !e.metaKey && !e.ctrlKey && !e.altKey) {
      e.preventDefault()
      searchRef.current?.focus()
      return
    }
    if (e.key === 'Escape') {
      if (selectedApp) { e.stopPropagation(); setSelectedApp(null) }
      else if (search) { e.stopPropagation(); setSearch('') }
    }
  }

  const installedCount = useMemo(() => apps.filter(isInstalled).length, [apps, isInstalled])
  const heading = tab === 'installed' ? 'Installed' : categoryLabel(category)

  const typeFilters: { id: TypeFilter; label: string }[] = [
    { id: 'all', label: 'All types' },
    { id: 'web', label: 'Web' },
    { id: 'desktop', label: 'Streamed' },
    { id: 'service', label: 'Service' },
  ]

  const notices = (
    <>
      {cacheReady === false && (
        <div className="hub-notice" data-tone="accent">
          <Glyph d={I.info} />
          <div className="hub-notice-body">
            <div className="hub-notice-title">Package index required</div>
            <p className="hub-notice-text">Update it to install apps from the Debian repositories.</p>
          </div>
          <div className="hub-notice-actions">
            <button className="hub-btn hub-btn-primary" onClick={updateAptCache} disabled={updatingCache}>
              {updatingCache ? <><span className="spinner" style={{ width: 12, height: 12 }} />Updating…</> : 'Update'}
            </button>
          </div>
        </div>
      )}
      {success && (
        <div className="hub-notice" data-tone="success">
          <Glyph d={I.check} />
          <div className="hub-notice-body">
            <div className="hub-notice-title">{success}</div>
          </div>
          <div className="hub-notice-actions">
            <button className="hub-icon-btn" onClick={() => setSuccess(null)} aria-label="Dismiss message"><Glyph d={I.cross} /></button>
          </div>
        </div>
      )}
      {error && (
        <div className="hub-notice" data-tone="danger">
          <Glyph d={I.alert} />
          <div className="hub-notice-body">
            <div className="hub-notice-title">Something went wrong</div>
            <pre>{error}</pre>
          </div>
          <div className="hub-notice-actions">
            <button className="hub-icon-btn" onClick={() => setError(null)} aria-label="Dismiss error"><Glyph d={I.cross} /></button>
          </div>
        </div>
      )}
    </>
  )

  return (
    <div
      className="hub"
      data-app="apphub"
      data-hub-mode={mode}
      ref={rootRef}
      onKeyDown={onRootKeyDown}
    >
      <div className="hub-main">
        <header className="hub-head">
          <div className="hub-head-row">
            <div className="hub-brand">
              <h1>App Hub</h1>
              {/* The box's architecture is stated here because it is the fact
                  that decides what half the catalogue can do, and the user has
                  no other way to learn it. It is the SERVER's answer — see
                  arch.ts — so on a browser whose own CPU differs it still reads
                  correctly. */}
              <p>
                {apps.length} available · {installedCount} installed
                {systemArch ? <> · <span className="mono">{systemArch}</span> box</> : null}
              </p>
            </div>

            <div className="hub-search">
              <svg className="hub-search-icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true">
                <circle cx="7" cy="7" r="4.6" /><path d={I.search} strokeLinecap="round" />
              </svg>
              <input
                ref={searchRef}
                type="search"
                value={search}
                onChange={e => setSearch(e.target.value)}
                placeholder="Search apps"
                aria-label="Search apps"
                aria-describedby="hub-result-count"
              />
              {search
                ? (
                  <button className="hub-search-clear" onClick={() => { setSearch(''); searchRef.current?.focus() }} aria-label="Clear search">
                    <Glyph d={I.cross} />
                  </button>
                )
                : <span className="hub-search-kbd mono" aria-hidden="true">/</span>}
            </div>

            <div className="hub-seg" role="tablist" aria-label="Library">
              {([['browse', 'Browse'], ['installed', 'Installed']] as const).map(([id, label]) => (
                <button
                  key={id}
                  role="tab"
                  aria-selected={tab === id}
                  onClick={() => setTab(id)}
                >
                  {label}
                  {id === 'installed' && installedCount > 0 && <span className="hub-count">{installedCount}</span>}
                </button>
              ))}
            </div>
          </div>

          {/* Compact + narrow: the rail's contents as one horizontal scroller.
              Rendered at every width and hidden by CSS at the wide ones, so the
              two lists cannot drift apart. */}
          <div className="hub-chips" role="group" aria-label="Filters">
            {/* Only offered when this box actually cannot run something in the
                current view. A toggle that can only ever hide zero apps is
                furniture, and on an amd64 box browsing an amd64 catalogue that
                is exactly what it would be. */}
            {incompatibleCount > 0 && (
              <>
                <button
                  className="hub-chip"
                  aria-pressed={onlyCompatible}
                  onClick={() => setOnlyCompatible(v => !v)}
                  title={`${incompatibleCount} app(s) here need a different architecture`}
                >
                  Runs on this box
                  <span className="hub-count">{incompatibleCount}</span>
                </button>
                <span className="hub-chip-sep" aria-hidden="true" />
              </>
            )}
            {categories.map(c => (
              <button
                key={c.id}
                className="hub-chip"
                aria-pressed={category === c.id}
                onClick={() => setCategory(c.id)}
              >
                {categoryLabel(c.id)}
                <span className="hub-count">{c.count}</span>
              </button>
            ))}
            <span className="hub-chip-sep" aria-hidden="true" />
            {typeFilters.filter(t => t.id !== 'all').map(t => (
              <button
                key={t.id}
                className="hub-chip"
                aria-pressed={appTypeFilter === t.id}
                onClick={() => setAppTypeFilter(appTypeFilter === t.id ? 'all' : t.id)}
              >
                <i className="hub-dot" style={{ '--hub-dot-color': TYPE_DOT[t.id as TypeKey] } as React.CSSProperties} aria-hidden="true" />
                {t.label}
              </button>
            ))}
          </div>
        </header>

        <div className="hub-notices" aria-live="polite">{notices}</div>

        <div className="hub-body">
          <nav className="hub-rail" aria-label="Browse by type and category">
            {incompatibleCount > 0 && (
              <>
                <div className="hub-rail-label">This box</div>
                <button
                  className="hub-rail-item"
                  aria-pressed={onlyCompatible}
                  onClick={() => setOnlyCompatible(v => !v)}
                >
                  <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d={CATEGORY_ICONS.system} /></svg>
                  <span>Runs on {systemArch || 'this box'}</span>
                  <span className="hub-count">{incompatibleCount}</span>
                </button>
              </>
            )}

            <div className="hub-rail-label">App type</div>
            {typeFilters.map(t => (
              <button
                key={t.id}
                className="hub-rail-item"
                aria-pressed={appTypeFilter === t.id}
                onClick={() => setAppTypeFilter(t.id)}
              >
                {t.id === 'all'
                  ? <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d={CATEGORY_ICONS.all} /></svg>
                  : <i className="hub-dot" style={{ '--hub-dot-color': TYPE_DOT[t.id as TypeKey], width: 8, height: 8, margin: '0 3px' } as React.CSSProperties} aria-hidden="true" />}
                <span>{t.label}</span>
              </button>
            ))}

            <div className="hub-rail-label">Categories</div>
            {categories.map(c => (
              <button
                key={c.id}
                className="hub-rail-item"
                aria-pressed={category === c.id}
                onClick={() => setCategory(c.id)}
              >
                <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                  <path d={CATEGORY_ICONS[c.id] || FALLBACK_CATEGORY_ICON} />
                </svg>
                <span>{categoryLabel(c.id)}</span>
                <span className="hub-count">{c.count}</span>
              </button>
            ))}
          </nav>

          <div className="hub-scroll" data-hub-ready={loading ? '0' : '1'}>
            <div className="hub-section">
              <h2>{heading}</h2>
              <span id="hub-result-count">
                {loading ? 'Loading…' : `${browseList.length} ${browseList.length === 1 ? 'app' : 'apps'}`}
              </span>
            </div>

            {loading ? (
              <div className="hub-grid" aria-hidden="true">
                {Array.from({ length: 6 }).map((_, i) => (
                  <div className="hub-skeleton" key={i}>
                    <div className="hub-skeleton-tile hub-pulse" />
                    <div className="hub-skeleton-lines">
                      <i className="hub-pulse" /><i className="hub-pulse" />
                    </div>
                  </div>
                ))}
              </div>
            ) : loadError ? (
              <div className="hub-empty">
                <div className="hub-empty-mark"><Glyph d={I.alert} /></div>
                <h3>The app catalogue could not be loaded</h3>
                <p>{loadError}</p>
                <button className="hub-btn hub-btn-primary" onClick={fetchData}>Try again</button>
              </div>
            ) : browseList.length === 0 ? (
              <EmptyState
                tab={tab}
                query={search.trim()}
                filtered={category !== 'all' || appTypeFilter !== 'all'}
                catalogueEmpty={apps.length === 0}
                onClear={() => { setSearch(''); setCategory('all'); setAppTypeFilter('all') }}
                onBrowse={() => setTab('browse')}
                onReload={fetchData}
              />
            ) : (
              <div className="hub-grid" ref={gridRef} onKeyDown={onGridKeyDown}>
                {browseList.map(app => (
                  <AppCard
                    key={app.id}
                    app={app}
                    selected={selectedApp?.id === app.id}
                    installed={isInstalled(app)}
                    installing={installing === app.id}
                    removing={uninstalling === app.id}
                    availability={availabilityOf(app)}
                    busy={!!installing}
                    onOpen={() => selectApp(app)}
                    onInstall={() => installApp(app.id, app.latest)}
                  />
                ))}
              </div>
            )}
          </div>
        </div>
      </div>

      {liveSelected && (
        <aside
          className="hub-detail"
          ref={trapRef}
          role={modal ? 'dialog' : 'complementary'}
          aria-modal={modal || undefined}
          aria-label={`${liveSelected.name} details`}
        >
          {/*
            ONE dismiss control, and which one depends on what the panel is.

            Both buttons called setSelectedApp(null); on the modal sheet that
            put a "Back to the app list" chevron and a "Close details" cross in
            the same 40px-tall bar, doing the identical thing under two
            different names. Two controls for one action is not a convenience —
            a reader has to work out what the difference is, and there isn't
            one.

            As a full-screen SHEET the gesture is "go back" (the list is
            underneath it); as a DOCKED column beside a list that never went
            away, it is "close this". Same handler, honest label.
          */}
          <div className="hub-detail-bar">
            {modal
              ? (
                <button className="hub-icon-btn" onClick={() => setSelectedApp(null)} aria-label="Back to the app list">
                  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={I.back} /></svg>
                </button>
              )
              : null}
            <strong>{liveSelected.name}</strong>
            {modal
              ? null
              : (
                <button className="hub-icon-btn" onClick={() => setSelectedApp(null)} aria-label="Close details">
                  <Glyph d={I.cross} />
                </button>
              )}
          </div>

          <div className="hub-detail-scroll">
            <div className="hub-hero">
              <AppIconTile id={liveSelected.id} size={72} unicode={liveSelected.icon} />
              <h2>
                {liveSelected.name}
                {liveSelected.vetted && <Glyph d={I.check} />}
              </h2>
              {liveSelected.author && <div className="hub-hero-by">{liveSelected.author}</div>}
              <div className="hub-hero-tags">
                <SourceBadge app={liveSelected} />
                <AppTypeBadge app={liveSelected} />
              </div>
            </div>

            <div>
              {installing === liveSelected.id ? (
                <div className="hub-notice" data-tone="accent" style={{ marginBottom: 0, flexDirection: 'column', alignItems: 'stretch' }}>
                  <div className="hub-state" style={{ justifyContent: 'flex-start' }}>
                    <span className="spinner" style={{ width: 14, height: 14 }} />
                    {installPhase || 'Installing…'}
                  </div>
                  <div className="hub-progress"><i /></div>
                </div>
              ) : uninstalling === liveSelected.id ? (
                <div className="hub-state" style={{ justifyContent: 'flex-start' }}>
                  <span className="spinner" style={{ width: 14, height: 14 }} />
                  Removing…
                </div>
              ) : isInstalled(liveSelected) ? (
                <div className="hub-actions">
                  <div className="hub-btn" aria-live="polite" style={{ pointerEvents: 'none' }}>
                    <Glyph d={I.check} />
                    Installed
                  </div>
                  <button className="hub-btn hub-btn-danger" onClick={() => uninstallApp(liveSelected.id)}>Remove</button>
                </div>
              ) : refused(liveSelected) ? (
                /*
                  The refusal, in the box's own words.

                  What was here composed its own sentence — "Installing it here
                  would fail" — from the arch list and the box's architecture.
                  Two things were wrong with that beyond the duplication. It
                  asserted an OUTCOME nobody measured: for a foreign-arch
                  Flatpak the install genuinely succeeds (`flatpak install
                  --arch=x86_64` deploys the app and its 1.4 GB platform onto an
                  aarch64 box, measured), and whether the result then runs is
                  recorded untestable-on-arm64-mac. And it had exactly one
                  sentence for five different reasons — no build published, a
                  build that could be installed and is declined on graphics
                  grounds, a GPU app whose only emulator has no GL, no emulator
                  installed, an entry not cleared for emulation. The box knows
                  which; the browser could not.
                */
                <div
                  className="hub-notice"
                  data-tone={availabilityOf(liveSelected)?.state === 'other-instance' ? 'accent' : 'danger'}
                  style={{ marginBottom: 0 }}
                >
                  <Glyph d={availabilityOf(liveSelected)?.state === 'other-instance' ? I.info : I.block} />
                  <div className="hub-notice-body">
                    <div className="hub-notice-title">{availabilityOf(liveSelected)?.badge}</div>
                    <p className="hub-notice-text">{availabilityOf(liveSelected)?.detail}</p>
                  </div>
                </div>
              ) : (
                <div className="hub-actions" style={{ flexDirection: 'column', alignItems: 'stretch' }}>
                  {/* Rung 3: installable, and the user is TOLD what they are
                      getting BEFORE they press the button. That labelling is the
                      whole difference between rung 3 and rung 2 — an app that is
                      present and crawls reads as a Vulos defect rather than as a
                      hardware limit. */}
                  {availabilityOf(liveSelected)?.requiresEmulation && (
                    <div className="hub-notice" data-tone="accent" style={{ marginBottom: 0 }}>
                      <Glyph d={I.info} />
                      <div className="hub-notice-body">
                        <div className="hub-notice-title">{availabilityOf(liveSelected)?.badge}</div>
                        <p className="hub-notice-text">{availabilityOf(liveSelected)?.detail}</p>
                      </div>
                    </div>
                  )}
                  <button
                    className="hub-btn hub-btn-primary"
                    disabled={!!installing}
                    onClick={() => installApp(liveSelected.id, selectedVersion)}
                  >
                    Install{selectedVersion && selectedVersion !== 'latest' ? ` ${selectedVersion}` : ''}
                  </button>
                </div>
              )}
            </div>

            {liveSelected.description && <p className="hub-body-text">{liveSelected.description}</p>}

            {(liveSelected.versions || []).length > 1 && (
              <div>
                <div className="hub-field-label">Version</div>
                <div className="hub-versions">
                  {(liveSelected.versions || []).map(v => (
                    <button
                      key={v}
                      className="hub-chip"
                      aria-pressed={selectedVersion === v}
                      onClick={() => setSelectedVersion(v)}
                    >
                      {v}
                    </button>
                  ))}
                </div>
              </div>
            )}

            <div>
              <div className="hub-field-label">Details</div>
              <dl className="hub-facts">
                <Fact label="Source" value={liveSelected.flatpak_id ? 'Flathub' : liveSelected.type === 'web' ? 'Web service' : 'Debian'} />
                <Fact label="Category" value={categoryLabel(liveSelected.category)} />
                <Fact label="License" value={liveSelected.license || 'Not stated'} />
                {/* The box's list, already folded — a Flathub entry saying
                    `x86_64` and a Debian one saying `amd64` do not read as two
                    different requirements for the same silicon.

                    "Not stated" and "Any" are NOT the same answer and are not
                    collapsed. An entry that declares no architecture means
                    NOBODY CHECKED (arch.go's policy block: 19 shipped entries
                    are in that state), and printing "Any" over it turns an
                    unverified entry into a claim to every machine Vulos ships.
                    The box marks that case `undeclared`. */}
                <Fact
                  label="Architecture"
                  value={
                    availabilityOf(liveSelected)?.needs.join(', ') ||
                    (availabilityOf(liveSelected)?.undeclared ? 'Not stated' : 'Any')
                  }
                />
                {liveSelected.homepage && <Fact label="Website" value={liveSelected.homepage} link />}
              </dl>
            </div>
          </div>
        </aside>
      )}
    </div>
  )
}

// ── Card ──────────────────────────────────────────────────────────────────────

interface AppCardProps {
  app: StoreApp
  selected: boolean
  installed: boolean
  installing: boolean
  removing: boolean
  availability: ArchAvailability | null
  busy: boolean
  onOpen: () => void
  onInstall: () => void
}

function AppCard({ app, selected, installed, installing, removing, availability, busy, onOpen, onInstall }: AppCardProps) {
  // The box said no. An unanswered app (`null`) is not refused — see arch.ts.
  const refused = !!availability && !availability.installable
  return (
    <article
      className="hub-card"
      data-selected={selected}
      data-app-id={app.id}
      // Drives the de-emphasis in CSS. An attribute rather than a class so the
      // e2e gates can assert on the STATE, not on a styling detail that could
      // be renamed underneath them — and it carries the RUNG the box named
      // rather than a yes/no the browser worked out, because "runs here
      // translated" and "runs on your other box" are two different things a
      // single boolean cannot hold.
      data-arch-state={availability?.state ?? 'unknown'}
    >
      <span className="hub-card-icon">
        <AppIconTile id={app.id} size={42} unicode={app.icon} />
      </span>
      <div className="hub-card-body">
        <button className="hub-card-open" onClick={onOpen}>{app.name}</button>
        {app.description && <p className="hub-card-desc">{app.description}</p>}
        <div className="hub-card-tags">
          <SourceBadge app={app} />
          <AppTypeBadge app={app} />
          {/*
            "Needs amd64" lives in the TAG ROW, not in the action column.

            It is a fact about the app, like the source and type beside it —
            and, decisively, the action column is `flex: none`, so a status put
            there claims its full intrinsic width from the card. Measured with
            it in the action column: an app needing `ppc64el` squeezed
            .hub-card-body to 74px at EVERY width from 768 to 1600, and the two
            badges it still had to hold overflowed it by 9px. The tag row wraps,
            so a long architecture name costs a line instead of the layout.

            The WORDS are the box's — availability.card_badge, written by
            EvaluateArch, whose tests own the wording. That matters beyond
            tidiness: an emulated app must read "Runs emulated" and one a
            sibling instance holds must name the sibling, and a browser deciding
            between those from an arch list would be guessing at the two facts
            it cannot see.
          */}
          {availability && availability.cardBadge !== '' && (
            <span className="hub-badge" data-tone={refused ? 'off' : 'warn'} title={availability.detail}>
              <Glyph d={refused ? I.block : I.info} />
              {availability.cardBadge}
            </span>
          )}
        </div>
      </div>
      {/* No action column at all when there is no action: an empty 30px slot
          beside a card that cannot be installed is just a hole in the row. */}
      {!refused && <div className="hub-card-action">
        {installing ? (
          <span className="hub-state" role="status">
            <span className="spinner" style={{ width: 14, height: 14 }} />
            Installing…
          </span>
        ) : removing ? (
          <span className="hub-state" role="status">
            <span className="spinner" style={{ width: 14, height: 14 }} />
            Removing…
          </span>
        ) : installed ? (
          <span className="hub-state" data-tone="ok">
            <Glyph d={I.check} />
            Installed
          </span>
        ) : (
          <button className="hub-get" disabled={busy} onClick={onInstall} aria-label={`Install ${app.name}`}>
            Get
          </button>
        )}
      </div>}
    </article>
  )
}

// ── Empty states ──────────────────────────────────────────────────────────────

interface EmptyStateProps {
  tab: Tab
  query: string
  filtered: boolean
  /** True when the CATALOGUE itself is empty, not just this view of it. */
  catalogueEmpty: boolean
  onClear: () => void
  onBrowse: () => void
  onReload: () => void
}

/**
 * "Nothing here" is several different situations and only some of them are the
 * user's fault. Each one gets the sentence that is true of it and an action that
 * can actually resolve it.
 */
function EmptyState({ tab, query, filtered, catalogueEmpty, onClear, onBrowse, onReload }: EmptyStateProps) {
  /**
   * The registry answered, and answered with nothing.
   *
   * This case used to fall through to the filter message below, so a hub with a
   * genuinely empty catalogue said "Nothing in this filter — try a different
   * word, or widen the category and type filters" and offered a "Clear filters"
   * button, with no search term entered and no filter set. Every instruction on
   * the screen was for a state the user was not in, and the one button did
   * nothing observable. The distinction the whole component is built around —
   * that a failed fetch must not read as an empty catalogue — was being drawn
   * one branch too early.
   */
  if (catalogueEmpty && !query && !filtered) {
    return (
      <div className="hub-empty">
        <div className="hub-empty-mark"><Glyph d={I.info} /></div>
        <h3>The catalogue is empty</h3>
        <p>The app registry answered, but offered nothing to install. It may still be syncing.</p>
        <button className="hub-btn hub-btn-primary" onClick={onReload}>Check again</button>
      </div>
    )
  }
  if (tab === 'installed' && !query && !filtered) {
    return (
      <div className="hub-empty">
        <div className="hub-empty-mark"><Glyph d={I.check} /></div>
        <h3>Nothing installed yet</h3>
        <p>Apps you install appear here, ready to launch from the dock.</p>
        <button className="hub-btn hub-btn-primary" onClick={onBrowse}>Browse the catalogue</button>
      </div>
    )
  }
  return (
    <div className="hub-empty">
      <div className="hub-empty-mark">
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true">
          <circle cx="7" cy="7" r="4.6" /><path d="M11 11l3.2 3.2" strokeLinecap="round" />
        </svg>
      </div>
      <h3>{query ? `No apps match “${query}”` : 'Nothing in this filter'}</h3>
      <p>
        {tab === 'installed'
          ? 'None of your installed apps match. Clear the filters to see them all.'
          : 'Try a different word, or widen the category and type filters.'}
      </p>
      <button className="hub-btn" onClick={onClear}>Clear filters</button>
    </div>
  )
}

// ── Detail rows ───────────────────────────────────────────────────────────────

function Fact({ label, value, link }: { label: string; value: string; link?: boolean }) {
  return (
    <div className="hub-fact">
      <dt>{label}</dt>
      <dd>
        {link
          ? <a href={value} target="_blank" rel="noopener noreferrer">{value.replace(/^https?:\/\/(www\.)?/, '')}</a>
          : value}
      </dd>
    </div>
  )
}
