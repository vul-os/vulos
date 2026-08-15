import { useState, useEffect, useCallback, useRef, useSyncExternalStore, type ReactNode, type FormEvent, type ChangeEvent } from 'react'
import {
  subscribePrefs, getPrefs, setMuted, setSound, setSourceEnabled, getSources,
} from './notificationStore'
import { useAuth } from '../auth/AuthProvider'
import type { AuthProfile } from '../auth/AuthProvider'
import { useTheme, DEFAULT_ACCENT } from './ThemeProvider'
import { accentSolid } from './accentContrast'
import { useWallpaper, DEFAULT_WALLPAPER } from './useWallpaper'
import { PamVisibilityControl } from './PublicAppsManager'
import { useFocusTrap } from '../shell/useFocusTrap'
import { useViewport } from '../shell/useViewport'
import StoragePanel from './settings/StoragePanel'
import DataExportPanel from './settings/DataExportPanel'
import ModelsPanel from './settings/ModelsPanel'
import BoxHealthPanel from './settings/BoxHealthPanel'
import OfflineDataPanel from './settings/OfflineDataPanel'
import RelayPanel from './settings/RelayPanel'
import WebPushToggle from './notifiers/WebPushToggle'
import SecurityPanel from './settings/SecurityPanel'
import WebhooksPanel from './settings/WebhooksPanel'
import DeveloperPanel from './settings/DeveloperPanel'
import DomainPanel from './settings/DomainPanel'
import CDNPanel from './settings/CDNPanel'
import LANPairingPanel from './settings/LANPairingPanel'
import LocationPanel from './settings/LocationPanel'
import DevicePanel from './settings/DevicePanel'
import { nativeBridge } from './nativeBridge'
import { SettingsIcon } from './AppIcons'
import {
  DOCK_ALIGNS, DOCK_EDGES, DOCK_SIZES, LAYOUT_PRESETS, MOBILE_EDGES, MOBILE_SIZES,
  applyPreset, isStock, resetToStock, setWindowControls, updateDock, useDesktopLayout,
} from '../desktop'
import {
  Section, Field, Toggle, Card, SettingRow, Divider, Pill, Meter,
  StatTile, InfoList, InfoRow, EmptyState, Banner, Actions, Narrow,
} from './settings/ui'

// isRecord narrows an `unknown` value (typically parsed JSON from a fetch
// response) to a plain object before any property access — the same guard
// AuthProvider.tsx / BoxHealthPanel.tsx / nativeBridge.ts use at their own
// trust boundaries. Every `/api/...` response in this file is untrusted wire
// JSON and is narrowed through this (or a per-endpoint `toX` built on it)
// rather than trusted wholesale.
function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// errorMessage stringifies a *caught exception* (e.g. from a rejected fetch
// promise) — distinct from errField below, which reads a `.error` string off
// an untrusted JSON *response body*.
function errorMessage(err: unknown): string {
  return isRecord(err) && typeof err.message === 'string' ? err.message : String(err)
}

// errField reads the common `{ error: "..." }` failure shape used across this
// file's `/api/...` endpoints off an untrusted parsed JSON response body.
function errField(x: unknown): string | undefined {
  return isRecord(x) && typeof x.error === 'string' ? x.error : undefined
}

/**
 * apiGet — read a JSON `/api` route, insisting the box actually ANSWERED it.
 *
 * `fetch(p).then(r => r.json())` — the shape a dozen sections in this file used
 * — cannot tell a reply apart from a refusal. Every `/api` service on this box
 * answers 5xx with a JSON error body, so that body parses cleanly and is handed
 * straight to a `toX()` narrower, which keeps the fields it recognises (none of
 * them) and returns a well-formed record of `undefined`s. The section then
 * renders that as a confident, WRONG answer — "Not connected", "No networks
 * found", "Vault not configured", zero audio devices — with nothing to say the
 * box never replied. A dead backend is drawn as a designed empty state.
 *
 * Throwing on non-2xx is what lets a caller tell the two apart and say so.
 */
async function apiGet(path: string): Promise<unknown> {
  const res = await fetch(path)
  const body: unknown = await res.json().catch(() => null)
  if (!res.ok) throw new Error(errField(body) || `The box could not answer (${res.status})`)
  return body
}

/**
 * apiSend — perform a WRITE and insist it landed.
 *
 * Checks three things that a bare `fetch(...).then(refresh)` checks none of:
 *   • the request reached the box at all (a rejected fetch),
 *   • the box accepted it (non-2xx),
 *   • the box did not answer 200 WITH an error body — a real shape in this
 *     codebase (`POST /telephony/call` and `/sms/send` both do it), where
 *     `res.ok` alone reports success for something that never happened.
 *
 * Defaults to POST + JSON because that is what every write in this file is;
 * pass `method` for the DELETE/PUT call sites.
 */
async function apiSend(path: string, init: RequestInit = {}): Promise<unknown> {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  const body: unknown = await res.json().catch(() => null)
  const err = errField(body)
  if (!res.ok) throw new Error(err || `The box refused the change (${res.status})`)
  if (err) throw new Error(err)
  return body
}

/**
 * useApiError — the piece of state every hardware section in this file was
 * missing.
 *
 * Returns the current failure message (null once an operation succeeds) and
 * `attempt`, which runs an apiGet/apiSend and turns a throw into a message the
 * section can RENDER, rather than a swallowed `.catch(() => {})`. Sections show
 * it in a `<Banner tone="danger">`, which carries role="alert".
 *
 * `attempt` resolves to `undefined` on failure, so a caller can also decline to
 * apply an optimistic state change — the difference between a "Connect" dialog
 * that closes on a 500 as though it worked and one that stays open with the
 * reason.
 */
function useApiError() {
  const [error, setError] = useState<string | null>(null)
  const attempt = useCallback(async <T,>(op: () => Promise<T>): Promise<T | undefined> => {
    try {
      const out = await op()
      setError(null)
      return out
    } catch (e) {
      setError(errorMessage(e))
      return undefined
    }
  }, [])
  return { error, attempt, setError }
}

// sectionGroups organise the settings sections into labelled clusters for a
// clear, scannable nav. Each item carries an id + label (+ owner:true for
// owner-only sections) and a compact glyph used as a subtle nav affordance.
// Ordering within a group is intentional. Flattened into `baseSections` below.
interface SettingsSectionDef {
  id: string
  label: string
  owner?: boolean
  nativeOnly?: boolean
}

interface SettingsSectionGroup {
  label: string
  items: SettingsSectionDef[]
}

const sectionGroups: SettingsSectionGroup[] = [
  {
    label: 'Intelligence',
    items: [
      { id: 'ai', label: 'AI Assistant' },
      { id: 'models', label: 'AI Models', owner: true },
      { id: 'aiapps', label: 'AI Apps' },
    ],
  },
  {
    label: 'Appearance',
    items: [
      { id: 'appearance', label: 'Appearance' },
      { id: 'notifications', label: 'Notifications' },
    ],
  },
  {
    label: 'Devices',
    items: [
      { id: 'wifi', label: 'WiFi' },
      { id: 'bluetooth', label: 'Bluetooth' },
      { id: 'audio', label: 'Sound' },
      { id: 'display', label: 'Display' },
      { id: 'energy', label: 'Battery & Energy' },
      { id: 'location', label: 'Location' },
      { id: 'device', label: 'This device', nativeOnly: true },
    ],
  },
  {
    label: 'Data',
    items: [
      { id: 'vault', label: 'Backup & Sync' },
      { id: 'recall', label: 'Search & Index' },
      { id: 'storage', label: 'Storage' },
      { id: 'storagemode', label: 'Storage Mode' },
    ],
  },
  {
    label: 'Network',
    items: [
      { id: 'connmode', label: 'Connection Mode' },
      { id: 'network', label: 'Remote Access' },
      { id: 'lanpairing', label: 'Native Pairing' },
      { id: 'domain', label: 'Custom Domain' },
      { id: 'relay', label: 'Relay & Reachability', owner: true },
      { id: 'cdn', label: 'CDN', owner: true },
      { id: 'turnSettings', label: 'TURN / WebRTC' },
    ],
  },
  {
    label: 'Developer',
    items: [
      { id: 'webhooks', label: 'Webhooks', owner: true },
      { id: 'developer', label: 'Developer' },
    ],
  },
  {
    label: 'Account & Security',
    items: [
      { id: 'users', label: 'Users & Profiles' },
      { id: 'pin', label: 'Device PIN' },
      { id: 'fingerprint', label: 'Fingerprint' },
      { id: 'account', label: 'Account' },
      { id: 'offlinedata', label: 'Offline Data' },
      { id: 'dataexport', label: 'Export My Data' },
      { id: 'security', label: 'Sign-in security' },
    ],
  },
  {
    label: 'System',
    items: [
      { id: 'osupdate', label: 'OS Update' },
      { id: 'boxhealth', label: 'Box Health', owner: true },
      { id: 'about', label: 'About' },
    ],
  },
]

// baseSections is the flat list (order preserved from the groups) used for
// lookups: initial-section validation and the active-label header.
const baseSections: SettingsSectionDef[] = sectionGroups.flatMap(g => g.items)

// sectionsFor returns the sections visible to a given role. Owner-only sections
// (marked owner:true) are hidden from non-owners so the surface stays owner-
// facing; the backend independently 403s their endpoints, so this is UX, not
// the security boundary.
// visible — owner-gate AND the nativeOnly gate (Android-app-only sections,
// e.g. "This device", stay hidden in a plain browser/PWA).
function visible(s: SettingsSectionDef, isOwner: boolean): boolean {
  return (!s.owner || isOwner) && (!s.nativeOnly || nativeBridge.inApp)
}

function sectionsFor(isOwner: boolean): SettingsSectionDef[] {
  return baseSections.filter(s => visible(s, isOwner))
}

// groupsFor returns the section groups visible to a given role, dropping any
// group left empty after owner-only/native-only filtering.
function groupsFor(isOwner: boolean): SettingsSectionGroup[] {
  return sectionGroups
    .map(g => ({ ...g, items: g.items.filter(s => visible(s, isOwner)) }))
    .filter(g => g.items.length > 0)
}

// SettingsModal — a small, accessible dialog wrapper: focus trap + focus
// restore, Esc + backdrop click to close, role/aria-modal, responsive width.
// Shared so any Settings panel modal gets the same keyboard contract.
interface SettingsModalProps {
  title?: string
  onClose: () => void
  children?: ReactNode
}

function SettingsModal({ title, onClose, children }: SettingsModalProps) {
  const trapRef = useFocusTrap(true)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      <div
        ref={trapRef}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="bg-[var(--bg-surface)] border border-[var(--border-strong)] rounded-2xl p-6 w-full max-w-sm shadow-2xl"
      >
        {title && <h3 className="text-base font-semibold mb-4">{title}</h3>}
        {children}
      </div>
    </div>
  )
}

// SettingsNav — the grouped section list. Shared between the desktop rail and
// the mobile drawer so aria-current + styling stay identical. Groups are
// rendered with a small uppercase label; the active item carries an accent left
// bar + accent-tinted surface so the current section reads at a glance.
interface SettingsNavProps {
  active: string
  onSelect: (id: string) => void
  idPrefix?: string
  groups: SettingsSectionGroup[]
}

function SettingsNav({ active, onSelect, idPrefix, groups }: SettingsNavProps) {
  return (
    <div className="px-2 space-y-5">
      {groups.map(g => (
        <div key={g.label}>
          <div className="px-2.5 mb-1.5 text-[12px] font-semibold uppercase tracking-[0.14em] text-[var(--text-tertiary)] select-none">
            {g.label}
          </div>
          <div className="space-y-0.5">
            {g.items.map(s => {
              const isActive = active === s.id
              return (
                <button
                  key={s.id}
                  id={idPrefix ? `${idPrefix}-${s.id}` : undefined}
                  onClick={() => onSelect(s.id)}
                  aria-current={isActive ? 'page' : undefined}
                  className={`group relative flex w-full items-center gap-3 rounded-lg pl-3 pr-2.5 py-2 text-[13.5px] transition-colors
                    ${isActive
                      ? 'accent-bg-soft accent-text font-medium'
                      : 'text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-hover)]'}`}
                >
                  <span
                    aria-hidden="true"
                    className={`absolute left-0 top-1/2 -translate-y-1/2 w-[3px] rounded-r-full transition-all
                      ${isActive ? 'h-6 accent-bg' : 'h-0'}`}
                  />
                  <span
                    aria-hidden="true"
                    className={`shrink-0 flex items-center justify-center w-5 transition-opacity
                      ${isActive ? 'opacity-100' : 'opacity-70 group-hover:opacity-100'}`}
                  >
                    <SettingsIcon name={s.id} size={20} />
                  </span>
                  <span className="truncate">{s.label}</span>
                </button>
              )
            })}
          </div>
        </div>
      ))}
    </div>
  )
}

export interface SettingsProps {
  // May be `null` (e.g. from settingsNav.ts's consumePendingSettingsSection(),
  // which types its "nothing pending" case as null) as well as `undefined`
  // (e.g. IntentRouter's optional `section?: string`) — both real call sites,
  // both meaning "no preference, default to the first section".
  initialSection?: string | null
}

export default function Settings({ initialSection }: SettingsProps) {
  const { profile, updateProfile, logout } = useAuth()
  const isOwner = profile?.role === 'admin'
  const sections = sectionsFor(isOwner)
  const groups = groupsFor(isOwner)
  const [active, setActive] = useState<string>(
    initialSection && sections.some(s => s.id === initialSection) ? initialSection : 'ai',
  )
  const layout = useViewport()
  const isMobile = layout === 'mobile'
  const [drawerOpen, setDrawerOpen] = useState(false)
  const drawerRef = useFocusTrap(isMobile && drawerOpen)
  const activeLabel = sections.find(s => s.id === active)?.label || 'Settings'

  // Close the drawer + return focus when navigating (mobile) or on Esc.
  const selectSection = (id: string) => { setActive(id); setDrawerOpen(false) }
  useEffect(() => {
    if (!isMobile || !drawerOpen) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setDrawerOpen(false) }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [isMobile, drawerOpen])

  return (
    // `@container/win`: Settings is a WINDOW, not a page. It opens at 860x620
    // and can be maximised to the whole screen — but Tailwind's `sm:`/`lg:`
    // breakpoints measure the VIEWPORT, so on any desktop they resolve
    // identically at both sizes and the layout could never adapt to the window
    // it actually lives in. Every proportion below (rail width, content
    // measure, gutters) is therefore a CONTAINER query against this element,
    // which is exactly the window's content box. The viewport-keyed `sm:`
    // rail/drawer swap is deliberately left alone: it is paired with the JS
    // `useViewport()` that mounts the drawer, and the two must agree.
    <div className="@container/win flex flex-col sm:flex-row h-full bg-[var(--bg-base)] text-[var(--text-primary)]">
      {/* Desktop sidebar rail */}
      {/* Solid fill, not `/60`: the sticky header below is already solid for
          legibility, and the bottom scrim has to fade to the rail's ACTUAL
          colour — over a translucent rail it faded to the wrong one and the
          cut edge stayed visible. */}
      <nav aria-label="Settings sections" className="hidden sm:flex sm:flex-col w-52 @4xl/win:w-[15rem] @6xl/win:w-64 shrink-0 border-r border-[var(--border-default)] bg-[var(--bg-surface)] overflow-y-auto">
        {/* Solid (not translucent) — this header is `sticky`, so the section
            list scrolls underneath it. A semi-transparent fill here let the
            nav item scrolled directly beneath show through and overlap
            "Configure your device", producing illegible ghosted text once a
            group scrolled under the fold (visible in both themes). */}
        <div className="sticky top-0 z-10 px-4 pt-5 pb-3 bg-[var(--bg-surface)] border-b border-[var(--border-subtle)]">
          <h2 className="text-base font-semibold tracking-tight text-[var(--text-primary)]">Settings</h2>
          <p className="text-[12px] text-[var(--text-tertiary)] mt-0.5">Configure your device</p>
        </div>
        {/* pb-10 is load-bearing, not padding taste: it is the landing strip
            for the scrim below. */}
        <div className="pt-4 pb-10">
          <SettingsNav active={active} onSelect={selectSection} groups={groups} />
        </div>
        {/* Scroll affordance. The rail holds 30+ items in eight groups and is
            taller than the window at every size, so the last visible item was
            always sliced by the window edge and read as broken chrome rather
            than as a list that continues. This sticky scrim fades that cut.
            The `-mt-8` pulls it over the strip of padding above, so when the
            rail IS scrolled to the end it covers empty space and never hides a
            section — a scrim that always sits on the final item would be a
            worse bug than the one it fixes. pointer-events-none so it can
            never eat a click on the item beneath. */}
        <div
          aria-hidden="true"
          className="sticky bottom-0 z-10 h-8 -mt-8 shrink-0 pointer-events-none bg-gradient-to-t from-[var(--bg-surface)] to-transparent"
        />
      </nav>

      {/* Mobile top bar — opens the section drawer */}
      <div className="sm:hidden flex items-center gap-3 shrink-0 border-b border-[var(--border-default)] bg-[var(--bg-surface)]/70 backdrop-blur-sm px-3 py-2.5">
        <button
          onClick={() => setDrawerOpen(true)}
          aria-label="Open settings sections"
          aria-expanded={drawerOpen}
          className="flex items-center gap-2 rounded-lg px-2.5 py-1.5 text-sm text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-hover)] transition-colors"
        >
          <span aria-hidden="true" className="text-base leading-none">☰</span>
          <span className="font-medium truncate max-w-[60vw]">{activeLabel}</span>
        </button>
      </div>

      {/* Mobile drawer */}
      {isMobile && drawerOpen && (
        <div
          className="sm:hidden fixed inset-0 z-50 flex"
          onMouseDown={(e) => { if (e.target === e.currentTarget) setDrawerOpen(false) }}
        >
          <nav
            ref={drawerRef}
            aria-label="Settings sections"
            className="w-72 max-w-[82vw] h-full bg-[var(--bg-surface)] border-r border-[var(--border-default)] overflow-y-auto shadow-2xl animate-[fadeIn_0.12s_ease-out]"
          >
            {/* Solid — same reasoning as the desktop rail's sticky header above. */}
            <div className="sticky top-0 z-10 flex items-center justify-between px-4 py-3.5 bg-[var(--bg-surface)] border-b border-[var(--border-subtle)]">
              <h2 className="text-base font-semibold tracking-tight text-[var(--text-primary)]">Settings</h2>
              <button
                onClick={() => setDrawerOpen(false)}
                aria-label="Close settings sections"
                className="rounded-md p-1 text-[var(--text-tertiary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-hover)] transition-colors"
              >
                <span aria-hidden="true">✕</span>
              </button>
            </div>
            <div className="py-4">
              <SettingsNav active={active} onSelect={selectSection} idPrefix="settings-drawer" groups={groups} />
            </div>
          </nav>
          <div className="flex-1 bg-black/60" aria-hidden="true" />
        </div>
      )}

      {/* Content.
          The column is LEFT-ALIGNED against the rail, not centred in the space
          left over. Centring a 42rem column inside a maximised 1360px pane put
          a ~380px void between the rail and the content AND another ~380px on
          the right: the eye read rail → gap → a narrow strip floating in the
          middle. Anchoring the measure to the rail keeps the two as one
          continuous surface and pushes all the slack to the outer edge, where
          empty space is just margin. The measure still grows with the window
          (42 → 48rem) and then, on a genuinely wide window, uncapped: a
          maximised Settings was leaving ~460px of empty gutter on the right
          because the column stopped at 56rem. Cards and label/value rows are
          happy full-bleed; the only thing that must NOT stretch is prose, so
          each panel's description carries its own 68ch measure. */}
      <div className="flex-1 overflow-y-auto overflow-x-hidden min-w-0">
       <div className="w-full max-w-2xl @5xl/win:max-w-3xl @7xl/win:max-w-none px-5 py-6 @3xl/win:px-8 @3xl/win:py-8 @6xl/win:px-10 @6xl/win:py-9">
        {active === 'ai' && <AISettings profile={profile} updateProfile={updateProfile} />}
        {active === 'models' && <ModelsPanel />}
        {active === 'aiapps' && <AIAppsSettings />}
        {active === 'appearance' && <AppearanceSettings />}
        {active === 'notifications' && <NotificationsSettings />}
        {active === 'wifi' && <WiFiSettings />}
        {active === 'bluetooth' && <BluetoothSettings />}
        {active === 'audio' && <AudioSettings />}
        {active === 'display' && <DisplaySettings />}
        {active === 'energy' && <EnergySettings />}
        {active === 'vault' && <VaultSettings />}
        {active === 'recall' && <RecallSettings />}
        {active === 'storage' && <StoragePanel />}
        {active === 'storagemode' && <StorageModeSettings />}
        {active === 'connmode' && <NET9_ConnectionModeSettings />}
        {active === 'network' && <NetworkSettings />}
        {active === 'lanpairing' && <LANPairingPanel />}
        {active === 'domain' && <DomainPanel />}
        {active === 'relay' && <RelayPanel />}
        {active === 'cdn' && <CDNPanel />}
        {active === 'turnSettings' && <TURNSettingsSection />}
        {active === 'webhooks' && <WebhooksPanel />}
        {active === 'developer' && <DeveloperPanel />}
        {active === 'users' && <UsersSettings profile={profile} />}
        {active === 'pin' && <DevicePINSettings />}
        {active === 'fingerprint' && <FingerprintSettings />}
        {active === 'account' && <AccountSettings profile={profile} updateProfile={updateProfile} logout={logout} />}
        {active === 'location' && <LocationPanel />}
        {active === 'device' && <DevicePanel />}
        {active === 'offlinedata' && <OfflineDataPanel />}
        {active === 'dataexport' && <DataExportPanel />}
        {active === 'security' && <SecurityPanel />}
        {active === 'osupdate' && <OSUpdateSettings />}
        {active === 'boxhealth' && <BoxHealthPanel />}
        {active === 'about' && <AboutSettings />}
       </div>
      </div>
    </div>
  )
}

// updateProfile's shape (a partial patch merged server-side) — mirrors
// AuthProvider.tsx's `updateProfile: (updates: Record<string, unknown>) =>
// Promise<void>`, imported by every panel below that saves profile fields.
type UpdateProfileFn = (updates: Record<string, unknown>) => Promise<void>

// --- AI ---
interface AiStatus {
  available?: boolean
  error?: string
  provider?: string
  model?: string
}
function toAiStatus(x: unknown): AiStatus | null {
  if (!isRecord(x)) return null
  return {
    available: typeof x.available === 'boolean' ? x.available : undefined,
    error: typeof x.error === 'string' ? x.error : undefined,
    provider: typeof x.provider === 'string' ? x.provider : undefined,
    model: typeof x.model === 'string' ? x.model : undefined,
  }
}

interface AISettingsProps {
  profile: AuthProfile | null
  updateProfile: UpdateProfileFn
}

function AISettings({ profile, updateProfile }: AISettingsProps) {
  const [provider, setProvider] = useState(typeof profile?.ai_provider === 'string' ? profile.ai_provider : 'ollama')
  const [model, setModel] = useState(typeof profile?.ai_model === 'string' ? profile.ai_model : '')
  const [apiKey, setApiKey] = useState('')
  const [status, setStatus] = useState<AiStatus | null>(null)

  useEffect(() => {
    // NOTE: GET /api/ai/status cannot currently fail — none of the three
    // backends' Status() has an error path — but it is read through apiGet
    // anyway so this call site does not model "any answer is the truth". A
    // separate honesty gap lives on the box side and is NOT visible from here:
    // a CONFIGURED but unreachable remote gateway still answers
    // {"status":"ok"} because Status() never dials it (backend.go's
    // remoteBackend.Status). Only the wholly-unconfigured case is flagged.
    apiGet('/api/ai/status').then((d: unknown) => setStatus(toAiStatus(d))).catch(() => {})
  }, [])

  const save = () => updateProfile({ ai_provider: provider, ai_model: model, ai_api_key: apiKey || undefined })

  const isLocal = provider === 'ollama'
  const PROVIDER_LABELS: Record<string, string> = { ollama: 'Ollama (on-device)', claude: 'Claude (Anthropic)', openai: 'OpenAI', custom: 'Custom (OpenAI-compatible)' }
  const providerLabel = PROVIDER_LABELS[provider]

  return (
    <Section
      icon="ai"
      title="AI Assistant"
      desc="Choose the model that powers your private assistant. Run it fully on-device with Ollama, or bring your own hosted key — your prompts and data never leave your box unless you pick a hosted provider."
      actions={status && (
        <Pill tone={status.available ? 'success' : 'danger'}>
          {status.available ? 'Connected' : 'Unavailable'}
        </Pill>
      )}
    >
      <Card
        icon="ai"
        title="Model provider"
        desc="Ollama keeps everything local. Hosted providers send prompts to that vendor over your key."
        footer={
          <div className="flex items-center justify-between gap-3">
            <span className="text-xs text-[var(--text-tertiary)]">
              {isLocal ? 'Running on-device — sovereign by default.' : `Using your ${providerLabel} key.`}
            </span>
            <button onClick={save} className="btn-primary text-sm">Save changes</button>
          </div>
        }
      >
        <Field label="Provider" htmlFor="ai-provider">
          <select id="ai-provider" value={provider} onChange={e => setProvider(e.target.value)} className="input">
            <option value="ollama">Ollama (on-device)</option>
            <option value="claude">Claude (Anthropic)</option>
            <option value="openai">OpenAI</option>
            <option value="custom">Custom (OpenAI-compatible)</option>
          </select>
        </Field>
        <Field label="Model" htmlFor="ai-model">
          <input id="ai-model" value={model} onChange={e => setModel(e.target.value)} placeholder={isLocal ? 'llama3' : provider === 'claude' ? 'claude-sonnet-4-20250514' : 'gpt-4o'} className="input" />
        </Field>
        {!isLocal && (
          <Field label="API key" htmlFor="ai-key" hint="Stored on your box and used only to reach the provider. Write-only — it is never shown again.">
            <input id="ai-key" type="password" value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder="••••••••••••" className="input" />
          </Field>
        )}
      </Card>

      <Card title="Status">
        {status ? (
          <InfoList>
            <InfoRow label="Availability" value={status.available ? 'Reachable' : (status.error || 'Not reachable')} />
            <InfoRow label="Active provider" value={status.provider || provider} />
            <InfoRow label="Active model" value={status.model || model || '—'} mono />
          </InfoList>
        ) : (
          <div className="flex items-center gap-2 text-xs text-[var(--text-muted)] py-1">
            <span className="w-3.5 h-3.5 spinner" /> Checking model availability…
          </div>
        )}
      </Card>
    </Section>
  )
}

// --- Appearance ---
function AppearanceSettings() {
  const {
    theme, setTheme, isDark, resolved,
    scheduleDark, scheduleLight, setScheduleDark, setScheduleLight,
    nightShiftMode, nightShiftActive, nightShiftWarmth,
    nightShiftFrom, nightShiftTo,
    setNightShiftMode, setNightShiftFrom, setNightShiftTo, setNightShiftWarmth,
    accent, setAccent,
  } = useTheme()

  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone

  return (
    <Section icon="appearance" title="Appearance" desc="Theme, accent, density, and wallpaper for this device.">
      <Card
        icon="sun"
        title="Theme"
        desc={
          theme === 'auto' ? `Follows your device (OS / browser) appearance, updating live. Currently ${isDark ? 'dark' : 'light'}.`
            : theme === 'schedule' ? `Switches by time (${tz}). Currently ${resolved}.`
              : theme === 'dark' ? 'Always dark.' : 'Always light.'
        }
      >
        {/* Capped, not stretched: four options across the full width of a
            maximised pane made each "Light"/"Dark" chip ~200px wide, which read
            as four empty cards rather than as one control. */}
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 max-w-[34rem]" role="radiogroup" aria-label="Theme mode">
          {[
            { value: 'light', label: 'Light', icon: 'sun' },
            { value: 'dark', label: 'Dark', icon: 'moon' },
            { value: 'auto', label: 'System', icon: 'display' },
            { value: 'schedule', label: 'Schedule', icon: 'clock' },
          ].map(opt => (
            <button
              key={opt.value}
              role="radio"
              aria-checked={theme === opt.value}
              onClick={() => setTheme(opt.value)}
              className={`py-3 rounded-xl text-sm transition-all border
                ${theme === opt.value
                  ? 'accent-bg-soft accent-border accent-text font-medium'
                  : 'bg-[var(--bg-elevated)]/50 border-[var(--border-default)] text-[var(--text-secondary)] hover:border-[var(--border-emphasis)] hover:text-[var(--text-primary)]'}`}
            >
              <div className="flex flex-col items-center gap-1.5">
                <SettingsIcon name={opt.icon} size={20} />
                {opt.label}
              </div>
            </button>
          ))}
        </div>

        {theme === 'schedule' && (
          <div className="mt-4 flex flex-wrap gap-4">
            <Field label="Dark mode from">
              <Narrow size="sm"><input type="time" value={scheduleDark} onChange={e => setScheduleDark(e.target.value)} className="input" /></Narrow>
            </Field>
            <Field label="Light mode from">
              <Narrow size="sm"><input type="time" value={scheduleLight} onChange={e => setScheduleLight(e.target.value)} className="input" /></Narrow>
            </Field>
            <p className="w-full text-[12px] text-[var(--text-faint)]">Timezone: {tz}</p>
          </div>
        )}
      </Card>

      <Card
        icon="moon"
        title="Night Shift"
        desc="Warms screen colours to reduce blue light during evening hours."
        aside={<Pill tone={nightShiftActive ? 'warning' : 'neutral'}>{nightShiftActive ? 'Active' : 'Off'}</Pill>}
      >
        <div className="flex flex-wrap gap-2 max-w-[34rem]" role="radiogroup" aria-label="Night Shift mode">
          {[
            { value: 'off', label: 'Off' },
            { value: 'auto', label: 'Sunset to Sunrise' },
            { value: 'custom', label: 'Custom' },
          ].map(opt => (
            <button
              key={opt.value}
              role="radio"
              aria-checked={nightShiftMode === opt.value}
              onClick={() => setNightShiftMode(opt.value)}
              className={`flex-1 min-w-[7rem] py-2 rounded-lg text-sm transition-all border
                ${nightShiftMode === opt.value
                  ? 'accent-bg-soft accent-border accent-text font-medium'
                  : 'bg-[var(--bg-elevated)]/50 border-[var(--border-default)] text-[var(--text-secondary)] hover:border-[var(--border-emphasis)] hover:text-[var(--text-primary)]'}`}
            >
              {opt.label}
            </button>
          ))}
        </div>

        {nightShiftMode === 'auto' && (
          <p className="text-xs text-[var(--text-faint)] mt-3">
            Based on approximate sunrise/sunset for your timezone ({tz}).
            {nightShiftActive ? ' Currently active.' : ' Currently off.'}
          </p>
        )}

        {nightShiftMode === 'custom' && (
          <div className="mt-4 flex flex-wrap gap-4">
            <Field label="From">
              <Narrow size="sm"><input type="time" value={nightShiftFrom} onChange={e => setNightShiftFrom(e.target.value)} className="input" /></Narrow>
            </Field>
            <Field label="To">
              <Narrow size="sm"><input type="time" value={nightShiftTo} onChange={e => setNightShiftTo(e.target.value)} className="input" /></Narrow>
            </Field>
            <p className="w-full text-[12px] text-[var(--text-faint)]">Timezone: {tz}</p>
          </div>
        )}

        {nightShiftMode !== 'off' && (
          <div className="mt-4 max-w-[34rem]">
            <Field label={`Warmth (${nightShiftWarmth}%)`}>
              <input
                type="range" min="10" max="100" value={nightShiftWarmth}
                onChange={e => setNightShiftWarmth(parseInt(e.target.value))}
                className="w-full h-1 appearance-none bg-[var(--bg-elevated)] rounded-full
                  [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:w-3 [&::-webkit-slider-thumb]:h-3
                  [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:bg-[var(--status-warning)]"
              />
              <div className="flex justify-between text-[12px] text-[var(--text-faint)] mt-1">
                <span>Less warm</span>
                <span>More warm</span>
              </div>
            </Field>
          </div>
        )}
      </Card>

      <Card
        icon="display"
        title="Desktop layout"
        desc="Where the dock lives, what it holds, and which side the window controls sit on. Presets match the habits people bring from other desktops — Vulos still looks like Vulos in every one."
      >
        <DesktopLayoutSettings />
      </Card>

      <Card title="Accent colour" desc="Applied to primary buttons and focus rings across the system.">
        <AccentPicker accent={accent} setAccent={setAccent} />
      </Card>

      <Card title="Density" desc="Compact tightens spacing across the shell.">
        <DensityPicker />
      </Card>

      <Card title="Wallpaper" desc="Shown behind the desktop and on the lock screen.">
        <WallpaperPicker />
      </Card>
    </Section>
  )
}

/* ── Desktop layout ───────────────────────────────────────────────────────────
   The Settings face of src/desktop. Every control here writes through the
   store's validated mutations — this component cannot construct a layout the
   validator would reject, and if it tried, the store would refuse it rather
   than apply it. The UI is not the boundary; store.ts is.

   Note what is NOT here: any field that accepts CSS, a URL, a selector or a
   free-form colour beyond a hex accent. See roadmap/CUSTOMIZATION.md for why a
   theme that can restyle chrome is a theme that can spoof the trust badge. */

interface ChoiceRowProps<T extends string> {
  label: string
  hint?: string
  value: T
  options: { value: T; label: string }[]
  onChange: (v: T) => void
}
function ChoiceRow<T extends string>({ label, hint, value, options, onChange }: ChoiceRowProps<T>) {
  return (
    <Field label={label} hint={hint}>
      <div className="flex flex-wrap gap-2" role="radiogroup" aria-label={label}>
        {options.map(opt => (
          <button
            key={opt.value}
            role="radio"
            aria-checked={value === opt.value}
            onClick={() => onChange(opt.value)}
            className={`px-3 py-1.5 rounded-lg text-sm transition-all border
              ${value === opt.value
                ? 'accent-bg-soft accent-border accent-text font-medium'
                : 'bg-[var(--bg-elevated)]/50 border-[var(--border-default)] text-[var(--text-secondary)] hover:border-[var(--border-emphasis)] hover:text-[var(--text-primary)]'}`}
          >
            {opt.label}
          </button>
        ))}
      </div>
    </Field>
  )
}

function DesktopLayoutSettings() {
  const layout = useDesktopLayout()
  const desktopDock = layout.dock.desktop
  const mobileDock = layout.dock.mobile
  const stock = isStock()

  return (
    <div className="flex flex-col gap-6">
      {/* 1 — the preset. Presented as full cards, because the reason to pick one
             is the sentence, not the name. */}
      <Field label="Preset" hint="Pick the arrangement closest to what you are used to, then adjust anything below.">
        <div className="grid gap-2 sm:grid-cols-2 max-w-[46rem]" role="radiogroup" aria-label="Desktop layout preset">
          {LAYOUT_PRESETS.map(preset => (
            <button
              key={preset.id}
              role="radio"
              aria-checked={layout.presetId === preset.id}
              onClick={() => applyPreset(preset.id)}
              className={`text-left p-3 rounded-xl border transition-all
                ${layout.presetId === preset.id
                  ? 'accent-bg-soft accent-border'
                  : 'bg-[var(--bg-elevated)]/50 border-[var(--border-default)] hover:border-[var(--border-emphasis)]'}`}
            >
              <div className={`text-sm font-medium ${layout.presetId === preset.id ? 'accent-text' : 'text-[var(--text-primary)]'}`}>
                {preset.name}
              </div>
              <div className="text-[12px] mt-0.5 text-[var(--text-tertiary)]">{preset.familiar}</div>
              <div className="text-[12px] mt-1.5 leading-snug text-[var(--text-secondary)]">{preset.summary}</div>
            </button>
          ))}
        </div>
      </Field>

      {/* 2 — this machine's dock. */}
      <div className="flex flex-col gap-4">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--text-faint)]">Dock on this desktop</div>
        <ChoiceRow
          label="Position"
          value={desktopDock.edge}
          options={DOCK_EDGES.map(e => ({ value: e, label: e[0].toUpperCase() + e.slice(1) }))}
          onChange={(edge) => updateDock('desktop', { edge })}
        />
        <ChoiceRow
          label="Style"
          hint="A floating island inset from the edge, or a full-width bar flush with it."
          value={desktopDock.style}
          options={[{ value: 'floating' as const, label: 'Floating' }, { value: 'bar' as const, label: 'Bar' }]}
          onChange={(style) => updateDock('desktop', { style })}
        />
        <ChoiceRow
          label="Size"
          value={desktopDock.size}
          options={DOCK_SIZES.map(s => ({ value: s, label: s[0].toUpperCase() + s.slice(1) }))}
          onChange={(size) => updateDock('desktop', { size })}
        />
        <ChoiceRow
          label="Alignment"
          value={desktopDock.align}
          options={DOCK_ALIGNS.map(a => ({ value: a, label: a === 'start' ? 'Start' : a === 'end' ? 'End' : 'Centre' }))}
          onChange={(align) => updateDock('desktop', { align })}
        />
        <Toggle
          label="Hide until pointed at"
          checked={desktopDock.autohide}
          onChange={(autohide) => updateDock('desktop', { autohide })}
        />
        <Toggle
          label="Show the app-drawer button"
          checked={desktopDock.drawer}
          onChange={(drawer) => updateDock('desktop', { drawer })}
        />
      </div>

      {/* 3 — the PHONE dock, edited separately on purpose. */}
      <div className="flex flex-col gap-4">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--text-faint)]">Dock on a phone</div>
        <p className="text-[12px] -mt-2 text-[var(--text-tertiary)]">
          A phone dock is its own arrangement, not a narrow copy of this one — it holds fewer items, always keeps the
          app drawer, and stays on a horizontal edge. Editing it here never changes your desktop dock, and editing your
          desktop dock never changes it.
        </p>
        <ChoiceRow
          label="Position"
          value={mobileDock.edge}
          options={MOBILE_EDGES.map(e => ({ value: e, label: e[0].toUpperCase() + e.slice(1) }))}
          onChange={(edge) => updateDock('mobile', { edge })}
        />
        <ChoiceRow
          label="Size"
          value={mobileDock.size}
          options={MOBILE_SIZES.map(s => ({ value: s, label: s[0].toUpperCase() + s.slice(1) }))}
          onChange={(size) => updateDock('mobile', { size })}
        />
      </div>

      {/* 4 — window controls. */}
      <ChoiceRow
        label="Window controls"
        hint="Which side of a window's title bar the close, minimise and maximise buttons sit on."
        value={layout.windowControls}
        options={[{ value: 'left' as const, label: 'Left' }, { value: 'right' as const, label: 'Right' }]}
        onChange={(side) => setWindowControls(side)}
      />

      {/* 5 — the way back. Always visible, drawn from stock tokens only, so no
             layout choice can affect how legible it is. */}
      <div className="flex flex-wrap items-center gap-3 pt-1">
        <button onClick={() => resetToStock()} className="btn-secondary text-sm" disabled={stock}>
          Reset to stock layout
        </button>
        <span className="text-[12px] text-[var(--text-faint)]">
          {stock
            ? 'This is the stock Vulos layout.'
            : 'Also available anywhere with Ctrl + Alt + Shift + Backspace, or by loading the shell with ?desktop-layout=stock.'}
        </span>
      </div>
    </div>
  )
}

// DensityPicker — a real, persisted appearance pref. Writes
// document.documentElement.dataset.density (consumed by index.css) and
// localStorage so it survives reloads. Applied eagerly on load in main.jsx.
const DENSITY_KEY = 'vulos.density'
function DensityPicker() {
  const [density, setDensity] = useState<string>(() => {
    try { return localStorage.getItem(DENSITY_KEY) || 'comfortable' } catch { return 'comfortable' }
  })
  // Apply the DOM/localStorage side-effects reactively when density changes.
  useEffect(() => {
    try { localStorage.setItem(DENSITY_KEY, density) } catch { /* noop */ }
    if (typeof document !== 'undefined') document.documentElement.dataset.density = density
  }, [density])
  const apply = (v: string) => setDensity(v)
  return (
    <div className="flex gap-2 max-w-[24rem]" role="radiogroup" aria-label="Interface density">
      {[{ value: 'comfortable', label: 'Comfortable' }, { value: 'compact', label: 'Compact' }].map(opt => (
        <button
          key={opt.value}
          role="radio"
          aria-checked={density === opt.value}
          onClick={() => apply(opt.value)}
          className={`flex-1 py-2 rounded-lg text-sm transition-all border
            ${density === opt.value
              ? 'accent-bg-soft accent-border accent-text font-medium'
              : 'bg-[var(--bg-elevated)]/50 border-[var(--border-default)] text-[var(--text-secondary)] hover:border-[var(--border-emphasis)] hover:text-[var(--text-primary)]'}`}
        >
          {opt.label}
        </button>
      ))}
    </div>
  )
}

function WallpaperPicker() {
  // useWallpaper() types as `WallpaperContextValue | null` (null only before
  // WallpaperProvider mounts — see core/useWallpaper.tsx). App.tsx wraps the
  // whole app in WallpaperProvider, so this is never actually null at runtime
  // here; handled null-safely (no-op fallback) rather than asserted non-null,
  // to keep this an honest type rather than a cast on another file's context.
  const wallpaperCtx = useWallpaper()
  const wallpaper = wallpaperCtx?.wallpaper ?? null
  const setWallpaper = wallpaperCtx?.setWallpaper ?? (() => {})
  const fileRef = useRef<HTMLInputElement>(null)

  const handleFile = (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = () => {
      // FileReader.result is `string | ArrayBuffer | null`; readAsDataURL
      // always yields a string on success, narrowed explicitly rather than
      // asserted so a genuinely non-string result (which would indicate a
      // wrong read* method was used) degrades to null instead of lying.
      const result = reader.result
      setWallpaper(typeof result === 'string' ? result : null)
    }
    reader.readAsDataURL(file)
  }

  const previewSrc = wallpaper || DEFAULT_WALLPAPER

  return (
    <div>
      <div className="rounded-lg overflow-hidden border border-[var(--border-default)] mb-3" style={{ maxWidth: 320 }}>
        <img src={previewSrc} alt="Current wallpaper" className="w-full aspect-video object-cover" />
      </div>
      <div className="flex flex-wrap gap-2.5">
        <button onClick={() => fileRef.current?.click()} className="btn-secondary text-sm">
          Choose image…
        </button>
        {wallpaper && (
          <button onClick={() => setWallpaper(null)} className="btn-ghost text-sm">
            Reset to default
          </button>
        )}
      </div>
      <input ref={fileRef} type="file" accept="image/*" onChange={handleFile} className="hidden" />
    </div>
  )
}

const ACCENT_PRESETS = [
  { label: 'Blue',    value: '#3b82f6' },
  { label: 'Indigo',  value: '#6366f1' },
  { label: 'Violet',  value: '#8b5cf6' },
  { label: 'Pink',    value: '#ec4899' },
  { label: 'Rose',    value: '#f43f5e' },
  { label: 'Orange',  value: '#f97316' },
  { label: 'Amber',   value: '#f59e0b' },
  { label: 'Green',   value: '#22c55e' },
  { label: 'Teal',    value: '#14b8a6' },
  { label: 'Cyan',    value: '#06b6d4' },
]

interface AccentPickerProps {
  accent: string
  setAccent: (c: string) => void
}
function AccentPicker({ accent, setAccent }: AccentPickerProps) {
  return (
    <div>
      {/* Preset swatches */}
      <div className="flex flex-wrap gap-2 mb-3">
        {ACCENT_PRESETS.map(p => (
          <button
            key={p.value}
            title={p.label}
            onClick={() => setAccent(p.value)}
            style={{ background: p.value }}
            className={`w-7 h-7 rounded-full transition-all border-2 ${
              accent === p.value
                ? 'border-[var(--text-primary)] scale-110 shadow-lg'
                : 'border-transparent opacity-80 hover:opacity-100 hover:scale-105'
            }`}
          />
        ))}
      </div>

      {/* Custom hex input */}
      <div className="flex items-center gap-3">
        <input
          type="color"
          value={accent}
          onChange={e => setAccent(e.target.value)}
          className="w-8 h-8 rounded cursor-pointer border border-[var(--border-strong)] bg-transparent p-0.5"
          title="Custom colour"
        />
        {/* `w-32` here was inert — see Narrow's note in settings/ui.tsx — so a
            7-character hex value rendered in a box the full width of the pane. */}
        <Narrow size="sm">
          <input
            type="text"
            value={accent}
            onChange={e => {
              const v = e.target.value.trim()
              if (/^#[0-9a-fA-F]{0,6}$/.test(v)) setAccent(v)
            }}
            onBlur={e => {
              const v = e.target.value.trim()
              if (!/^#[0-9a-fA-F]{6}$/.test(v)) setAccent(DEFAULT_ACCENT)
            }}
            placeholder="#3b82f6"
            aria-label="Accent colour hex value"
            className="input font-mono"
          />
        </Narrow>
        {accent !== DEFAULT_ACCENT && (
          <button
            onClick={() => setAccent(DEFAULT_ACCENT)}
            className="text-xs text-[var(--text-muted)] hover:text-[var(--text-secondary)] transition-colors"
          >
            Reset
          </button>
        )}
      </div>

      {/* Live preview */}
      <div className="mt-3 flex items-center gap-3">
        {/* accentSolid() darkens the chosen accent just enough that the white
            label on it clears 4.5:1. Painting the raw accent here rendered the
            default #3b82f6 with white text at 3.68:1 — and this is the swatch
            the user judges every accent by, so it was the one button in
            Settings guaranteed to be looked at closely. */}
        <button
          className="btn-primary text-sm"
          style={{ background: accentSolid(accent) || accent }}
          tabIndex={-1}
        >
          Preview button
        </button>
        <span className="text-xs text-[var(--text-faint)]">Live preview</span>
      </div>
    </div>
  )
}

// --- WiFi ---
interface WifiStatus {
  connected?: boolean
  ssid?: string
  ip?: string
}
function toWifiStatus(x: unknown): WifiStatus | null {
  if (!isRecord(x)) return null
  return {
    connected: typeof x.connected === 'boolean' ? x.connected : undefined,
    ssid: typeof x.ssid === 'string' ? x.ssid : undefined,
    ip: typeof x.ip === 'string' ? x.ip : undefined,
  }
}
interface WifiNetwork {
  bssid?: string
  ssid?: string
  signal?: number
  band?: string
  security?: string
}
function toWifiNetwork(x: unknown): WifiNetwork | null {
  if (!isRecord(x)) return null
  return {
    bssid: typeof x.bssid === 'string' ? x.bssid : undefined,
    ssid: typeof x.ssid === 'string' ? x.ssid : undefined,
    signal: typeof x.signal === 'number' ? x.signal : undefined,
    band: typeof x.band === 'string' ? x.band : undefined,
    security: typeof x.security === 'string' ? x.security : undefined,
  }
}
function toWifiNetworks(x: unknown): WifiNetwork[] {
  if (!Array.isArray(x)) return []
  return x.map(toWifiNetwork).filter((n): n is WifiNetwork => n !== null)
}

function WiFiSettings() {
  const [status, setStatus] = useState<WifiStatus | null>(null)
  const [networks, setNetworks] = useState<WifiNetwork[] | null>(null)
  const [scanning, setScanning] = useState(false)
  const [connectSSID, setConnectSSID] = useState<string | null>(null)
  const [password, setPassword] = useState('')

  const [connecting, setConnecting] = useState(false)
  const { error, attempt } = useApiError()

  const refresh = useCallback(
    () => attempt(async () => setStatus(toWifiStatus(await apiGet('/api/wifi/status')))),
    [attempt],
  )
  useEffect(() => { refresh() }, [refresh])

  const scan = async () => {
    setScanning(true)
    // A failed scan must NOT fall back to []. It used to, and the empty list
    // rendered the "No networks found — move closer to your router, or try
    // scanning again" empty state, which tells the user to go fix their router
    // when what actually happened is that the box's wifi service did not
    // answer. Leaving `networks` untouched keeps the banner as the only claim.
    const nets = await attempt(async () => toWifiNetworks(await apiGet('/api/wifi/scan')))
    if (nets) setNetworks(nets)
    setScanning(false)
  }

  const connect = async () => {
    // This posted a network password and then closed the dialog, cleared the
    // field and scheduled a refresh WHATEVER came back — 200, 403 or 500 all
    // looked identical to the user, who was left believing they had joined a
    // network they had not. On failure the dialog now stays open, with the
    // password still in it, and says why.
    setConnecting(true)
    const ok = await attempt(async () => {
      await apiSend('/api/wifi/connect', { body: JSON.stringify({ ssid: connectSSID, password }) })
      return true
    })
    setConnecting(false)
    if (!ok) return
    setConnectSSID(null)
    setPassword('')
    setTimeout(refresh, 3000)
  }

  return (
    <Section
      icon="wifi"
      title="WiFi"
      desc="Wireless networks this box can see, and the one it is using."
      actions={status && (
        <Pill tone={status.connected ? 'success' : 'neutral'}>
          {status.connected ? `Connected — ${status.ssid}` : 'Not connected'}
        </Pill>
      )}
    >
      {error && <Banner tone="danger" title="WiFi is not responding">{error}</Banner>}

      {status?.connected && (
        <Card title="Current network">
          <InfoList>
            <InfoRow label="Network" value={status.ssid} />
            <InfoRow label="IP address" value={status.ip} mono />
          </InfoList>
        </Card>
      )}

      <Card
        icon="wifi"
        title="Available networks"
        desc="Scanning does not connect to anything — it only lists what is in range."
        aside={
          <button onClick={scan} disabled={scanning} className="btn-secondary text-sm">
            {scanning ? 'Scanning…' : 'Scan Networks'}
          </button>
        }
        bodyClassName={networks && networks.length ? 'divide-y divide-[var(--border-subtle)]' : ''}
      >
        {/* A scan that legitimately finds nothing previously rendered no
            feedback at all — indistinguishable from "haven't scanned yet". */}
        {!networks && <p className="text-sm text-[var(--text-muted)]">Scan to see networks in range.</p>}
        {networks && networks.length === 0 && (
          <EmptyState icon="wifi" title="No networks found" hint="Move closer to your router, or try scanning again." />
        )}
        {networks?.map(n => (
          <SettingRow
            key={n.bssid || n.ssid || ''}
            label={n.ssid || '(hidden)'}
            desc={`${n.signal}dBm · ${n.band} · ${n.security || 'open'}`}
            control={
              <button
                onClick={() => setConnectSSID(n.ssid ?? null)}
                aria-label={`Connect to ${n.ssid || 'hidden network'}`}
                className="btn-secondary text-sm"
              >
                Connect
              </button>
            }
          />
        ))}
      </Card>

      {connectSSID && (
        <Card
          title={`Connect to ${connectSSID}`}
          footer={
            <Actions>
              <button onClick={() => setConnectSSID(null)} className="btn-ghost text-sm">Cancel</button>
              <button onClick={connect} disabled={connecting} className="btn-primary text-sm">
                {connecting ? 'Connecting…' : 'Connect'}
              </button>
            </Actions>
          }
        >
          <Field label="Password" htmlFor="wifi-password">
            <Narrow size="lg">
              <input id="wifi-password" type="password" value={password} onChange={e => setPassword(e.target.value)} placeholder="Network password" className="input" />
            </Narrow>
          </Field>
        </Card>
      )}
    </Section>
  )
}

// --- Bluetooth ---
interface BluetoothDevice {
  address: string
  name?: string
  type?: string
  connected?: boolean
  paired?: boolean
}
function toBluetoothDevice(x: unknown): BluetoothDevice | null {
  if (!isRecord(x) || typeof x.address !== 'string') return null
  return {
    address: x.address,
    name: typeof x.name === 'string' ? x.name : undefined,
    type: typeof x.type === 'string' ? x.type : undefined,
    connected: typeof x.connected === 'boolean' ? x.connected : undefined,
    paired: typeof x.paired === 'boolean' ? x.paired : undefined,
  }
}
interface BluetoothStatus {
  powered?: boolean
  devices?: BluetoothDevice[]
}
function toBluetoothStatus(x: unknown): BluetoothStatus | null {
  if (!isRecord(x)) return null
  return {
    powered: typeof x.powered === 'boolean' ? x.powered : undefined,
    devices: Array.isArray(x.devices) ? x.devices.map(toBluetoothDevice).filter((d): d is BluetoothDevice => d !== null) : undefined,
  }
}

function BluetoothSettings() {
  const [status, setStatus] = useState<BluetoothStatus | null>(null)
  const { error, attempt } = useApiError()

  const refresh = useCallback(
    () => attempt(async () => setStatus(toBluetoothStatus(await apiGet('/api/bluetooth/status')))),
    [attempt],
  )
  useEffect(() => { refresh() }, [refresh])

  // Every one of these six was `fetch(…).then(refresh)`: the response was never
  // looked at, so a refused pair, a failed connect or a radio that would not
  // power on produced exactly the same thing as success — a silent re-read that
  // redrew the unchanged state. `send` routes them all through apiSend, so a
  // failure reaches the banner instead of vanishing.
  const send = (path: string, body: unknown, after: () => void = refresh) =>
    attempt(async () => {
      await apiSend(path, { body: JSON.stringify(body) })
      after()
    })

  const setPower = (on: boolean) => send('/api/bluetooth/power', { on })
  const scan = (on: boolean) => send('/api/bluetooth/scan', { on }, () => setTimeout(refresh, 3000))
  const pair = (addr: string) => send('/api/bluetooth/pair', { address: addr })
  const connect = (addr: string) => send('/api/bluetooth/connect', { address: addr })
  const disconnect = (addr: string) => send('/api/bluetooth/disconnect', { address: addr })
  const remove = (addr: string) => send('/api/bluetooth/remove', { address: addr })

  return (
    <Section
      icon="bluetooth"
      title="Bluetooth"
      desc="Pair keyboards, headsets and other peripherals with this box."
      actions={<Pill tone={status?.powered ? 'success' : 'neutral'}>{status?.powered ? 'On' : 'Off'}</Pill>}
    >
      {error && <Banner tone="danger" title="Bluetooth is not responding">{error}</Banner>}

      <Card title="Radio">
        <SettingRow
          label="Bluetooth"
          desc="Turning the radio off disconnects every paired device."
          control={<Toggle ariaLabel="Bluetooth" checked={status?.powered} onChange={(v) => setPower(v)} />}
        />
      </Card>

      {status?.powered && (
        <Card
          title="Devices"
          desc="Devices in range, plus anything already paired with this box."
          aside={<button onClick={() => scan(true)} className="btn-secondary text-sm">Scan for Devices</button>}
          bodyClassName={status?.devices?.length ? 'divide-y divide-[var(--border-subtle)]' : ''}
        >
          {!status?.devices?.length && (
            <EmptyState icon="bluetooth" title="No devices yet" hint="Put a device into pairing mode, then scan." />
          )}
          {status?.devices?.map(d => {
            const dn = d.name || d.address
            return (
              <SettingRow
                key={d.address}
                label={dn}
                desc={`${d.type || 'device'}${d.connected ? ' · connected' : d.paired ? ' · paired' : ''}`}
                control={
                  <div className="flex gap-2 flex-wrap justify-end">
                    {!d.paired && <button onClick={() => pair(d.address)} aria-label={`Pair with ${dn}`} className="btn-secondary text-sm">Pair</button>}
                    {d.paired && !d.connected && <button onClick={() => connect(d.address)} aria-label={`Connect to ${dn}`} className="btn-secondary text-sm">Connect</button>}
                    {d.connected && <button onClick={() => disconnect(d.address)} aria-label={`Disconnect from ${dn}`} className="btn-ghost text-sm">Disconnect</button>}
                    {d.paired && <button onClick={() => remove(d.address)} aria-label={`Forget ${dn}`} className="btn-ghost text-sm text-[var(--status-danger)]">Remove</button>}
                  </div>
                }
              />
            )
          })}
        </Card>
      )}
    </Section>
  )
}

// --- Audio ---
interface AudioDeviceT {
  id: string | number
  name: string
  volume: number
  muted?: boolean
  default?: boolean
}
function toAudioDevice(x: unknown): AudioDeviceT | null {
  if (!isRecord(x)) return null
  if (typeof x.id !== 'string' && typeof x.id !== 'number') return null
  if (typeof x.name !== 'string') return null
  return {
    id: x.id,
    name: x.name,
    volume: typeof x.volume === 'number' ? x.volume : 0,
    muted: typeof x.muted === 'boolean' ? x.muted : undefined,
    default: typeof x.default === 'boolean' ? x.default : undefined,
  }
}
interface AudioStatus {
  backend?: string
  outputs?: AudioDeviceT[]
  inputs?: AudioDeviceT[]
}
function toAudioStatus(x: unknown): AudioStatus | null {
  if (!isRecord(x)) return null
  return {
    backend: typeof x.backend === 'string' ? x.backend : undefined,
    outputs: Array.isArray(x.outputs) ? x.outputs.map(toAudioDevice).filter((d): d is AudioDeviceT => d !== null) : undefined,
    inputs: Array.isArray(x.inputs) ? x.inputs.map(toAudioDevice).filter((d): d is AudioDeviceT => d !== null) : undefined,
  }
}

function AudioSettings() {
  const [status, setStatus] = useState<AudioStatus | null>(null)
  const { error, attempt } = useApiError()

  const refresh = useCallback(
    () => attempt(async () => setStatus(toAudioStatus(await apiGet('/api/audio/status')))),
    [attempt],
  )
  useEffect(() => { refresh() }, [refresh])

  // These three did `.then(r => r.json()).then(d => setStatus(toAudioStatus(d)))`
  // with no res.ok and no catch, which failed in two ways at once. The parsed
  // 500 body `{"error": …}` passed through toAudioStatus and came back a record
  // of undefineds, so a failed volume nudge WIPED THE WHOLE DEVICE LIST off the
  // panel; and with no .catch, a network drop raised an unhandled rejection.
  // Now the status is only replaced when the box actually returned one.
  const write = (path: string, body: unknown) =>
    attempt(async () => {
      const next = toAudioStatus(await apiSend(path, { body: JSON.stringify(body) }))
      if (next) setStatus(next)
    })

  const setVol = (id: string | number, type: string, volume: number) => write('/api/audio/volume', { device_id: id, type, volume })
  const setMute = (id: string | number, type: string, muted: boolean) => write('/api/audio/mute', { device_id: id, type, muted })
  const setDef = (id: string | number, type: string) => write('/api/audio/default', { device_id: id, type })

  return (
    <Section title="Sound">
      {error && <Banner tone="danger" title="Sound is not responding">{error}</Banner>}
      {/* Gated on the VALUE, not on `status` being present: with a status body
          that omits `backend` this rendered the label "Backend:" followed by
          nothing — the same shape as Display's "Compositor:" and its
          "Brightness (undefined%)". Caught by looking at the render, not by a
          test. */}
      {status?.backend && <p className="text-xs text-[var(--text-faint)] mb-4 mt-3">Backend: {status.backend}</p>}
      <h3 className="text-xs uppercase text-[var(--text-muted)] tracking-wider mb-2">Output</h3>
      {/* Both lists previously rendered a bare heading with nothing under it
          when the box reported no devices — no empty state, no explanation,
          just a label over blank space that read as a half-drawn panel. */}
      {!error && !status?.outputs?.length && (
        <EmptyState icon="audio" title="No output devices" hint="This box is not reporting any speakers or headphones." />
      )}
      {status?.outputs?.map(d => (
        <AudioDevice key={d.id} device={d} type="output" onVolume={setVol} onMute={setMute} onDefault={setDef} />
      ))}
      <h3 className="text-xs uppercase text-[var(--text-muted)] tracking-wider mt-4 mb-2">Input</h3>
      {!error && !status?.inputs?.length && (
        <EmptyState icon="audio" title="No input devices" hint="This box is not reporting any microphones." />
      )}
      {status?.inputs?.map(d => (
        <AudioDevice key={d.id} device={d} type="input" onVolume={setVol} onMute={setMute} onDefault={setDef} />
      ))}
    </Section>
  )
}

interface AudioDeviceProps {
  device: AudioDeviceT
  type: 'output' | 'input'
  onVolume: (id: string | number, type: string, volume: number) => void
  onMute: (id: string | number, type: string, muted: boolean) => void
  onDefault: (id: string | number, type: string) => void
}
function AudioDevice({ device, type, onVolume, onMute, onDefault }: AudioDeviceProps) {
  return (
    <div className="py-2 border-b border-[var(--border-default)]">
      <div className="flex items-center justify-between gap-3 mb-1">
        <div className="flex items-center gap-2 min-w-0">
          <button
            onClick={() => onDefault(device.id, type)}
            aria-label={device.default ? `${device.name} is the default ${type} device` : `Set ${device.name} as default ${type} device`}
            aria-pressed={!!device.default}
            className={`shrink-0 w-3 h-3 rounded-full border ${device.default ? 'bg-[var(--accent)] accent-border' : 'border-[var(--border-emphasis)]'}`}
          />
          <span className="text-sm truncate">{device.name}</span>
        </div>
        <button
          onClick={() => onMute(device.id, type, !device.muted)}
          aria-label={device.muted ? `Unmute ${device.name}` : `Mute ${device.name}`}
          aria-pressed={!!device.muted}
          className={`shrink-0 text-xs ${device.muted ? 'text-[var(--status-danger)]' : 'text-[var(--text-tertiary)] hover:text-[var(--text-primary)]'}`}
        >
          {device.muted ? 'Muted' : 'Mute'}
        </button>
      </div>
      <input type="range" min="0" max="100" value={device.volume} onChange={e => onVolume(device.id, type, parseInt(e.target.value))}
        aria-label={`${device.name} volume`}
        className="w-full h-1 appearance-none bg-[var(--bg-elevated)] rounded-full [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:w-3 [&::-webkit-slider-thumb]:h-3 [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:bg-[var(--text-primary)]" />
    </div>
  )
}

// --- Display ---
interface DisplayOutput {
  name: string
  connected?: boolean
  primary?: boolean
  modes?: string[]
  resolution?: string
}
function toDisplayOutput(x: unknown): DisplayOutput | null {
  if (!isRecord(x) || typeof x.name !== 'string') return null
  return {
    name: x.name,
    connected: typeof x.connected === 'boolean' ? x.connected : undefined,
    primary: typeof x.primary === 'boolean' ? x.primary : undefined,
    modes: Array.isArray(x.modes) ? x.modes.filter((m): m is string => typeof m === 'string') : undefined,
    resolution: typeof x.resolution === 'string' ? x.resolution : undefined,
  }
}
interface DisplayBrightness {
  device?: string
  current?: number
}
function toDisplayBrightness(x: unknown): DisplayBrightness | undefined {
  if (!isRecord(x)) return undefined
  return {
    device: typeof x.device === 'string' ? x.device : undefined,
    current: typeof x.current === 'number' ? x.current : undefined,
  }
}
interface DisplayStatus {
  brightness?: DisplayBrightness
  compositor?: string
  outputs?: DisplayOutput[]
}
function toDisplayStatus(x: unknown): DisplayStatus | null {
  if (!isRecord(x)) return null
  return {
    brightness: toDisplayBrightness(x.brightness),
    compositor: typeof x.compositor === 'string' ? x.compositor : undefined,
    outputs: Array.isArray(x.outputs) ? x.outputs.map(toDisplayOutput).filter((o): o is DisplayOutput => o !== null) : undefined,
  }
}

function DisplaySettings() {
  const [status, setStatus] = useState<DisplayStatus | null>(null)
  const { error, attempt } = useApiError()

  const refresh = useCallback(
    () => attempt(async () => setStatus(toDisplayStatus(await apiGet('/api/display/status')))),
    [attempt],
  )
  useEffect(() => { refresh() }, [refresh])

  // Same wipe-on-failure as Sound: the parsed error body narrowed to a record
  // of undefineds and blanked every output off the panel.
  const write = (path: string, body: unknown) =>
    attempt(async () => {
      const next = toDisplayStatus(await apiSend(path, { body: JSON.stringify(body) }))
      if (next) setStatus(next)
    })

  const setBrightness = (v: number) => write('/api/display/brightness', { brightness: v })
  const setRes = (output: string, res: string) => write('/api/display/resolution', { output, resolution: res })

  return (
    <Section title="Display">
      {error && <Banner tone="danger" title="Display is not responding">{error}</Banner>}
      {/* Gated on a brightness reading actually existing. The test was
          `status?.brightness?.device !== 'none'`, which is TRUE while status is
          still null — so on first paint, and on every failure, this rendered a
          slider labelled literally "Brightness (undefined%)". */}
      {status?.brightness && status.brightness.device !== 'none' && (
        <Field label={`Brightness (${status.brightness.current ?? 100}%)`}>
          <input type="range" min="5" max="100" value={status.brightness.current || 100} onChange={e => setBrightness(parseInt(e.target.value))}
            aria-label="Screen brightness"
            className="w-full h-1 appearance-none bg-[var(--bg-elevated)] rounded-full [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:w-3 [&::-webkit-slider-thumb]:h-3 [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:bg-[var(--text-primary)]" />
        </Field>
      )}
      {status?.compositor && <p className="text-xs text-[var(--text-faint)] mb-3">Compositor: {status.compositor}</p>}
      {!error && !status?.outputs?.length && (
        <EmptyState icon="display" title="No displays detected" hint="This box is not reporting any connected outputs." />
      )}
      {status?.outputs?.map(o => (
        <div key={o.name} className="py-3 border-b border-[var(--border-default)]">
          <div className="flex items-center gap-2 mb-1">
            <span className={`w-2 h-2 rounded-full ${o.connected ? 'bg-[var(--status-success)]' : 'bg-[var(--bg-active)]'}`} />
            <span className="text-sm font-medium">{o.name}</span>
            {o.primary && <span className="text-[12px] text-[var(--accent)]">primary</span>}
          </div>
          {o.connected && !!o.modes && o.modes.length > 0 && (
            <select value={o.resolution || ''} onChange={e => setRes(o.name, e.target.value)} className="input mt-1">
              {o.modes.map(m => <option key={m} value={m}>{m}{m === o.resolution ? ' (current)' : ''}</option>)}
            </select>
          )}
        </div>
      ))}
    </Section>
  )
}

// --- Energy ---
interface EnergyStatus {
  battery_percent?: number
  battery_charging?: boolean
  mode?: string
  cpu_governor?: string
  screen_on?: boolean
  screen_dimmed?: boolean
  idle_duration?: string
}
function toEnergyStatus(x: unknown): EnergyStatus | null {
  if (!isRecord(x)) return null
  return {
    battery_percent: typeof x.battery_percent === 'number' ? x.battery_percent : undefined,
    battery_charging: typeof x.battery_charging === 'boolean' ? x.battery_charging : undefined,
    mode: typeof x.mode === 'string' ? x.mode : undefined,
    cpu_governor: typeof x.cpu_governor === 'string' ? x.cpu_governor : undefined,
    screen_on: typeof x.screen_on === 'boolean' ? x.screen_on : undefined,
    screen_dimmed: typeof x.screen_dimmed === 'boolean' ? x.screen_dimmed : undefined,
    idle_duration: typeof x.idle_duration === 'string' ? x.idle_duration : undefined,
  }
}

function EnergySettings() {
  const [status, setStatus] = useState<EnergyStatus | null>(null)
  const { error, attempt } = useApiError()

  const refresh = useCallback(
    () => attempt(async () => setStatus(toEnergyStatus(await apiGet('/api/energy/status')))),
    [attempt],
  )
  useEffect(() => { refresh() }, [refresh])

  // POST /api/energy/mode answers 400 on a mode the box will not accept. The
  // old call read the body without checking the status, so a rejected mode
  // narrowed to undefineds and cleared the panel — and the three mode buttons
  // all went unselected, which reads as "no mode set" rather than "refused".
  const setMode = (mode: string) =>
    attempt(async () => {
      const next = toEnergyStatus(await apiSend('/api/energy/mode', { body: JSON.stringify({ mode }) }))
      if (next) setStatus(next)
    })

  const batteryPercent = status?.battery_percent

  return (
    <Section title="Battery & Energy">
      {error && <Banner tone="danger" title="Power settings are not responding">{error}</Banner>}
      {batteryPercent !== undefined && batteryPercent >= 0 && (
        <div className="mb-4">
          <span className="text-2xl font-light">{batteryPercent}%</span>
          <span className="text-sm text-[var(--text-muted)] ml-2">{status?.battery_charging ? 'Charging' : 'On Battery'}</span>
        </div>
      )}
      <Field label="Power Mode">
        <div className="flex gap-2">
          {['performance', 'balanced', 'saver'].map(m => (
            <button key={m} onClick={() => setMode(m)}
              className={`flex-1 py-2 rounded-lg text-sm capitalize transition-colors ${status?.mode === m ? 'bg-[var(--bg-hover)] text-[var(--text-primary)]' : 'bg-[var(--bg-elevated)] text-[var(--text-tertiary)] hover:bg-[var(--bg-elevated)]'}`}>
              {m}
            </button>
          ))}
        </div>
      </Field>
      {status && (
        <div className="text-xs text-[var(--text-faint)] mt-3 space-y-1">
          <p>CPU Governor: {status.cpu_governor}</p>
          <p>Screen: {status.screen_on ? (status.screen_dimmed ? 'Dimmed' : 'On') : 'Off'}</p>
          <p>Idle: {status.idle_duration}</p>
        </div>
      )}
    </Section>
  )
}

// --- Remote Access ---
// --- NET-09: Connection Mode ---
// Additive section. Read/POST /api/network/mode. Matches the visual style of
// the TURN section above. All identifiers are prefixed NET9_ or connmode-.
interface NET9_Mode {
  id: string
  label: string
  desc: string
}
const NET9_MODES: NET9_Mode[] = [
  {
    id: 'fabric',
    label: 'Fabric',
    desc: 'Route through the reachability relay fabric (default; works behind NAT).',
  },
  {
    id: 'direct',
    label: 'Direct',
    desc: 'Expose this node directly on the public internet (re-enrolls DNS).',
  },
  {
    id: 'own',
    label: 'Own Domain',
    desc: 'Use your own domain + reverse proxy; bypasses Vulos DNS.',
  },
  {
    id: 'local',
    label: 'Local Only',
    desc: 'LAN-only; external listeners are blocked. Useful for air-gapped use.',
  },
]

interface NetworkModeStatusDetail {
  domain?: string
  instance_id?: string
}
function toNetworkModeStatusDetail(x: unknown): NetworkModeStatusDetail | undefined {
  if (!isRecord(x)) return undefined
  return {
    domain: typeof x.domain === 'string' ? x.domain : undefined,
    instance_id: typeof x.instance_id === 'string' ? x.instance_id : undefined,
  }
}
interface NetworkModeResponse {
  mode?: string
  external_listener_blocked?: boolean
  status?: NetworkModeStatusDetail
  error?: string
}
function toNetworkModeResponse(x: unknown): NetworkModeResponse {
  if (!isRecord(x)) return {}
  return {
    mode: typeof x.mode === 'string' ? x.mode : undefined,
    external_listener_blocked: typeof x.external_listener_blocked === 'boolean' ? x.external_listener_blocked : undefined,
    status: toNetworkModeStatusDetail(x.status),
    error: typeof x.error === 'string' ? x.error : undefined,
  }
}

function NET9_ConnectionModeSettings() {
  const [current, setCurrent] = useState<string | null>(null) // server-confirmed mode
  const [pending, setPending] = useState<string | null>(null) // user-selected mode (pre-apply)
  const [blocked, setBlocked] = useState(false)
  const [status, setStatus] = useState<NetworkModeStatusDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')

  const NET9_refresh = useCallback(() => {
    setLoading(true)
    fetch('/api/network/mode')
      .then(r => (r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status))))
      .then((raw: unknown) => {
        const d = toNetworkModeResponse(raw)
        setCurrent(d.mode ?? null)
        setPending(d.mode ?? null)
        setBlocked(!!d.external_listener_blocked)
        setStatus(d.status || null)
        setError('')
      })
      .catch((e: unknown) => setError(errorMessage(e) || 'failed to load'))
      .finally(() => setLoading(false))
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { NET9_refresh() }, [NET9_refresh])

  const NET9_apply = () => {
    if (!pending || pending === current) return
    setSaving(true)
    setSaved(false)
    setError('')
    fetch('/api/network/mode', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: pending }),
    })
      .then(async r => {
        const raw: unknown = await r.json().catch(() => ({}))
        const body = toNetworkModeResponse(raw)
        if (!r.ok) throw new Error(body.error || ('HTTP ' + r.status))
        return body
      })
      .then(d => {
        setCurrent(d.mode ?? null)
        setPending(d.mode ?? null)
        setBlocked(!!d.external_listener_blocked)
        setStatus(d.status || null)
        setSaved(true)
        setTimeout(() => setSaved(false), 2000)
      })
      .catch((e: unknown) => setError(errorMessage(e) || 'failed to apply'))
      .finally(() => setSaving(false))
  }

  const dirty = pending && pending !== current

  return (
    <Section title="Connection Mode">
      <p className="text-xs text-[var(--text-faint)] mb-5">
        Choose how this device reaches the outside world. The setting is persisted to
        <code className="mx-1 text-[var(--text-tertiary)]">~/.vulos/db/network-mode.json</code>
        and survives reboots. Switching modes never changes this node's identity (ULID).
      </p>

      <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)] mb-5">
        <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
          <span className="text-xs text-[var(--text-muted)]">Active mode</span>
          <span className={`text-sm font-medium ${current ? 'text-[var(--status-success)]' : 'text-[var(--text-muted)]'}`}>
            {loading ? '…' : (current || 'unknown')}
          </span>
        </div>
        <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
          <span className="text-xs text-[var(--text-muted)]">External listener</span>
          <span className={`text-sm font-medium ${blocked ? 'text-[var(--status-warning)]' : 'text-[var(--text-secondary)]'}`}>
            {blocked ? 'blocked (local-only)' : 'enabled'}
          </span>
        </div>
        {status?.domain && (
          <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
            <span className="text-xs text-[var(--text-muted)]">Resolved domain</span>
            <span className="text-sm text-[var(--text-secondary)]">{status.domain}</span>
          </div>
        )}
        {status?.instance_id && (
          <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
            <span className="text-xs text-[var(--text-muted)]">Instance ID</span>
            <span className="text-sm text-[var(--text-secondary)] font-mono truncate ml-3">{status.instance_id}</span>
          </div>
        )}
      </div>

      <div className="space-y-2 mb-5">
        {NET9_MODES.map(m => {
          const selected = pending === m.id
          return (
            <label
              key={m.id}
              htmlFor={`connmode-${m.id}`}
              className={`flex items-start gap-3 rounded-lg border px-4 py-3 cursor-pointer transition-colors ${
                selected
                  ? 'accent-border bg-[var(--accent-soft)]'
                  : 'border-[var(--border-default)] bg-[var(--bg-surface)] hover:bg-[var(--bg-surface)]'
              }`}
            >
              <input
                id={`connmode-${m.id}`}
                type="radio"
                name="connmode-radio"
                value={m.id}
                checked={selected}
                onChange={() => setPending(m.id)}
                className="mt-1 accent-blue-500"
              />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium text-[var(--text-primary)]">{m.label}</span>
                  {current === m.id && (
                    <span className="text-[12px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-[var(--status-success-soft)] text-[var(--status-success)]">
                      Active
                    </span>
                  )}
                </div>
                <p className="text-xs text-[var(--text-muted)] mt-0.5">{m.desc}</p>
              </div>
            </label>
          )
        })}
      </div>

      <div className="flex gap-3 items-center">
        <button
          onClick={NET9_apply}
          disabled={!dirty || saving || loading}
          className="btn text-sm"
        >
          {saving ? 'Applying…' : saved ? 'Applied' : 'Apply'}
        </button>
        <button
          onClick={NET9_refresh}
          disabled={loading || saving}
          className="btn-ghost text-sm"
        >
          Refresh
        </button>
      </div>

      {error && <Banner tone="danger">{error}</Banner>}
    </Section>
  )
}

interface NetworkConfig {
  app_url?: string
  error?: string
}
function toNetworkConfig(x: unknown): NetworkConfig | null {
  if (!isRecord(x)) return null
  return {
    app_url: typeof x.app_url === 'string' ? x.app_url : undefined,
    error: typeof x.error === 'string' ? x.error : undefined,
  }
}

function NetworkSettings() {
  const [config, setConfig] = useState<NetworkConfig>({ app_url: 'http://localhost:8080' })
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const { error, attempt } = useApiError()

  useEffect(() => {
    fetch('/api/network/config').then(r => r.ok ? r.json() : null).then((raw: unknown) => {
      const d = toNetworkConfig(raw)
      if (d && !d.error) setConfig(d)
    }).catch(() => {})
  }, [])

  // This read no status and set `saved` unconditionally, so a 403 (not owner)
  // or a 500 still flashed "Saved" — the button did not merely fail quietly, it
  // actively asserted a write that had not happened.
  const saveConfig = async () => {
    setSaving(true)
    const ok = await attempt(async () => {
      const next = toNetworkConfig(await apiSend('/api/network/configure', {
        body: JSON.stringify(config),
      }))
      if (next) setConfig(next)
      return true
    })
    setSaving(false)
    if (!ok) return
    setSaved(true)
    setTimeout(() => setSaved(false), 2000)
  }

  return (
    <Section title="Remote Access">
      <p className="text-xs text-[var(--text-faint)] mb-5">Configure how this device is reached from the network. If you're accessing remotely, set the URL to your public IP or domain.</p>

      {error && <Banner tone="danger" title="Could not save the access URL">{error}</Banner>}

      <label className="block text-sm text-[var(--text-tertiary)] mb-1">Access URL</label>
      <input
        value={config.app_url || ''}
        onChange={e => setConfig(c => ({ ...c, app_url: e.target.value }))}
        placeholder="http://localhost:8080"
        className="w-full bg-[var(--bg-surface)] border border-[var(--border-strong)] rounded px-3 py-1.5 text-sm mb-1"
      />
      {/* The example address is RFC 5737 TEST-NET-3 (203.0.113.0/24), which is
          reserved for documentation and is not routable. It replaces a real,
          routable public IP that was shipped here as the "e.g." — that address
          belonged to a live host and had no business in the product UI. */}
      <p className="text-xs text-[var(--text-faint)] mb-5">The address used to reach this device. For local use, leave as localhost. For remote access, set to your IP or domain (e.g. http://203.0.113.10:8080).</p>

      <button onClick={saveConfig} disabled={saving} className="btn text-sm">
        {saving ? 'Saving...' : saved ? 'Saved' : 'Save'}
      </button>
    </Section>
  )
}

// --- TURN / WebRTC (NET-10) ---
interface TurnConfig {
  host?: string
  port?: number
  realm?: string
  configured?: boolean
  error?: string
}
function toTurnConfig(x: unknown): TurnConfig | null {
  if (!isRecord(x)) return null
  return {
    host: typeof x.host === 'string' ? x.host : undefined,
    port: typeof x.port === 'number' ? x.port : undefined,
    realm: typeof x.realm === 'string' ? x.realm : undefined,
    configured: typeof x.configured === 'boolean' ? x.configured : undefined,
    error: typeof x.error === 'string' ? x.error : undefined,
  }
}
interface TurnTestResult {
  success?: boolean
  latency_ms?: number
  error?: string
}
function toTurnTestResult(x: unknown): TurnTestResult | null {
  if (!isRecord(x)) return null
  return {
    success: typeof x.success === 'boolean' ? x.success : undefined,
    latency_ms: typeof x.latency_ms === 'number' ? x.latency_ms : undefined,
    error: typeof x.error === 'string' ? x.error : undefined,
  }
}

function TURNSettingsSection() {
  const [host, setHost] = useState('')
  const [port, setPort] = useState<number | string>(3478)
  const [realm, setRealm] = useState('vulos')
  const [secret, setSecret] = useState('')
  const [configured, setConfigured] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [testResult, setTestResult] = useState<TurnTestResult | null>(null)
  const [testing, setTesting] = useState(false)
  const { error, attempt } = useApiError()

  useEffect(() => {
    fetch('/api/turn/config')
      .then(r => r.ok ? r.json() : null)
      .then((raw: unknown) => {
        const d = toTurnConfig(raw)
        if (!d || d.error) return
        setHost(d.host || '')
        setPort(d.port || 3478)
        setRealm(d.realm || 'vulos')
        setConfigured(!!d.configured)
      })
      .catch(() => {})
  }, [])

  // Saving a rejected TURN config used to be entirely silent: there was no
  // res.ok check, and a body carrying `error` simply failed the `!d.error`
  // guard, so the whole success block was skipped and the button slid back from
  // "Saving…" to "Save" with nothing said. A 403 (not owner) and a 400 (bad
  // host) both looked exactly like never having pressed the button.
  const turnSettings_save = async () => {
    setSaving(true)
    setSaved(false)
    const ok = await attempt(async () => {
      const d = toTurnConfig(await apiSend('/api/turn/config', {
        body: JSON.stringify({ host, port: Number(port), realm, secret: secret || undefined }),
      }))
      setConfigured(!!d?.configured)
      return true
    })
    setSaving(false)
    if (!ok) return
    setSecret('')
    setSaved(true)
    setTimeout(() => setSaved(false), 2000)
  }

  const turnSettings_test = () => {
    setTesting(true)
    setTestResult(null)
    fetch('/api/turn/test', { method: 'POST' })
      .then(r => r.json())
      .then((raw: unknown) => setTestResult(toTurnTestResult(raw)))
      .catch((e: unknown) => setTestResult({ success: false, error: errorMessage(e) }))
      .finally(() => setTesting(false))
  }

  return (
    <Section title="TURN / WebRTC Relay">
      <p className="text-xs text-[var(--text-faint)] mb-5">
        Users behind strict NAT need a TURN relay for reliable WebRTC.
        Point at your own coturn server or a tier-provided one.
        The shared secret is stored securely and never returned by the API.
      </p>

      {configured && (
        <div className="text-xs text-[var(--status-success)] mb-3">TURN server configured</div>
      )}

      <Field label="TURN Host">
        <input
          value={host}
          onChange={e => setHost(e.target.value)}
          placeholder="turn.example.com"
          className="input"
        />
      </Field>

      <Field label="Port">
        <input
          type="number"
          value={port}
          onChange={e => setPort(e.target.value)}
          placeholder="3478"
          className="input"
        />
      </Field>

      <Field label="Realm">
        <input
          value={realm}
          onChange={e => setRealm(e.target.value)}
          placeholder="vulos"
          className="input"
        />
      </Field>

      <Field label="Shared Secret">
        <input
          type="password"
          value={secret}
          onChange={e => setSecret(e.target.value)}
          placeholder={configured ? '(leave blank to keep existing)' : 'enter shared secret'}
          className="input"
        />
        <p className="text-xs text-[var(--text-faint)] mt-1">Write-only — the secret is never returned once saved.</p>
      </Field>

      <div className="flex gap-3 mt-4 items-center">
        <button onClick={turnSettings_save} disabled={saving} className="btn text-sm">
          {saving ? 'Saving…' : saved ? 'Saved' : 'Save'}
        </button>
        <button onClick={turnSettings_test} disabled={testing || !configured} className="btn text-sm">
          {testing ? 'Testing…' : 'Test Reachability'}
        </button>
      </div>

      {error && <Banner tone="danger" title="Could not save the TURN settings">{error}</Banner>}

      {/* NOTE: POST /api/turn/test deliberately answers HTTP 200 whether the
          relay was reachable or not, and puts the outcome in `success` —
          routes_turn.go says so in a comment. Reading `success` here rather
          than trusting the status code is therefore load-bearing, not
          defensive: `res.ok` alone would report every unreachable relay as
          reachable. */}
      {testResult && (
        <Banner tone={testResult.success ? 'success' : 'danger'}>
          {testResult.success
            ? `Reachable — latency ${testResult.latency_ms} ms`
            : `Unreachable: ${testResult.error || 'connection failed'}`}
        </Banner>
      )}
    </Section>
  )
}

// --- Account ---
interface AccountSettingsProps {
  profile: AuthProfile | null
  updateProfile: UpdateProfileFn
  logout: () => Promise<void>
}
function AccountSettings({ profile, updateProfile, logout }: AccountSettingsProps) {
  const [name, setName] = useState(typeof profile?.display_name === 'string' ? profile.display_name : '')
  const [locale, setLocale] = useState(typeof profile?.locale === 'string' ? profile.locale : 'en')
  const [tz, setTz] = useState(typeof profile?.timezone === 'string' ? profile.timezone : '')
  // Every other panel in this file (WiFi's connect, OS Update's stage,
  // TURN's save, …) confirms an action with a saving/saved/error cycle —
  // this Save button silently did nothing, the one place in Settings you
  // couldn't tell whether your click landed.
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')

  const save = async () => {
    setSaving(true)
    setSaved(false)
    setError('')
    try {
      await updateProfile({ display_name: name, locale, timezone: tz })
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch (e: unknown) {
      setError(errorMessage(e) || 'Failed to save')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Section
      icon="account"
      title="Account"
      desc="Your name and regional preferences on this box. These travel with your profile, not with this device."
    >
      <Card
        icon="account"
        title="Profile"
        desc="Shown on the lock screen, in shared documents, and anywhere Vulos names you."
        footer={
          <Actions hint={error ? undefined : 'Changes apply everywhere you are signed in.'}>
            <button onClick={save} disabled={saving} className="btn-primary text-sm">
              {saving ? 'Saving…' : saved ? 'Saved' : 'Save'}
            </button>
          </Actions>
        }
      >
        <Field label="Display name" htmlFor="account-name">
          <Narrow size="lg"><input id="account-name" value={name} onChange={e => setName(e.target.value)} className="input" /></Narrow>
        </Field>
        <div className="flex flex-wrap gap-4">
          <Field label="Language" htmlFor="account-locale" hint="Two-letter language code.">
            <Narrow size="sm"><input id="account-locale" value={locale} onChange={e => setLocale(e.target.value)} placeholder="en" className="input" /></Narrow>
          </Field>
          <Field label="Timezone" htmlFor="account-tz" hint="IANA zone name.">
            <Narrow size="md"><input id="account-tz" value={tz} onChange={e => setTz(e.target.value)} placeholder="Africa/Johannesburg" className="input" /></Narrow>
          </Field>
        </div>
        {error && <Banner tone="danger">{error}</Banner>}
      </Card>

      <Card title="Session" desc="Ends this browser session. Your data stays on the box.">
        <Actions>
          <button onClick={logout} className="btn-secondary text-sm text-[var(--status-danger)]">Log Out</button>
        </Actions>
      </Card>
    </Section>
  )
}

// A `{ type: 'ok' | 'err'; text }` status line, shared by the two lock-screen-
// credential panels below (Device PIN + Fingerprint).
interface StatusMsg { type: 'ok' | 'err'; text: string }

// --- Device PIN (CLOGIN-06) ---
//
// Allows the user to set / change / disable the device-local PIN used for
// lock-screen unlock. Requires a full-auth session to change the PIN
// (enforced server-side; the UI shows an informational note).
interface PinStatus {
  has_pin?: boolean
  permanent_lock?: boolean
  locked?: boolean
  locked_until?: string
  attempts_left?: number
}
function toPinStatus(x: unknown): PinStatus | null {
  if (!isRecord(x)) return null
  return {
    has_pin: typeof x.has_pin === 'boolean' ? x.has_pin : undefined,
    permanent_lock: typeof x.permanent_lock === 'boolean' ? x.permanent_lock : undefined,
    locked: typeof x.locked === 'boolean' ? x.locked : undefined,
    locked_until: typeof x.locked_until === 'string' ? x.locked_until : undefined,
    attempts_left: typeof x.attempts_left === 'number' ? x.attempts_left : undefined,
  }
}

function DevicePINSettings() {
  const [status, setStatus] = useState<PinStatus | null>(null)       // lockout status from server
  const [hasPIN, setHasPIN] = useState<boolean | null>(null)       // whether a PIN is currently set
  const [newPIN, setNewPIN] = useState('')
  const [confirmPIN, setConfirmPIN] = useState('')
  const [msg, setMsg] = useState<StatusMsg | null>(null)             // { type: 'ok'|'err', text }
  const [busy, setBusy] = useState(false)

  const loadStatus = () => {
    fetch('/api/auth/pin/status')
      .then(r => r.ok ? r.json() : null)
      .then((raw: unknown) => {
        const data = toPinStatus(raw)
        if (data) {
          setStatus(data)
          setHasPIN(data.has_pin !== false)
        }
      })
      .catch(() => {})
  }

  useEffect(() => { loadStatus() }, [])

  const handleSet = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    setMsg(null)

    if (newPIN.length < 4) {
      setMsg({ type: 'err', text: 'PIN must be at least 4 digits' })
      return
    }
    if (newPIN.length > 8) {
      setMsg({ type: 'err', text: 'PIN must be at most 8 digits' })
      return
    }
    if (newPIN !== confirmPIN) {
      setMsg({ type: 'err', text: 'PINs do not match' })
      return
    }

    setBusy(true)
    try {
      const res = await fetch('/api/auth/pin/device/set', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pin: newPIN }),
      })
      const data: unknown = await res.json()
      if (!res.ok) {
        setMsg({ type: 'err', text: errField(data) || 'Failed to set PIN' })
      } else {
        setMsg({ type: 'ok', text: 'PIN set — takes effect on next lock' })
        setNewPIN('')
        setConfirmPIN('')
        loadStatus()
      }
    } catch {
      setMsg({ type: 'err', text: 'Could not reach server' })
    } finally {
      setBusy(false)
    }
  }

  const handleDisable = async () => {
    if (!confirm('Remove the device PIN? The lock screen will allow entry without a PIN.')) return
    setBusy(true)
    setMsg(null)
    try {
      const res = await fetch('/api/auth/pin/device/set', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pin: '' }),
      })
      const data: unknown = await res.json()
      if (!res.ok) {
        setMsg({ type: 'err', text: errField(data) || 'Failed to remove PIN' })
      } else {
        setMsg({ type: 'ok', text: 'PIN removed' })
        setHasPIN(false)
        loadStatus()
      }
    } catch {
      setMsg({ type: 'err', text: 'Could not reach server' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section title="Device PIN">
      {/* Current status */}
      <div className="mb-6 rounded-xl border border-[var(--border-default)] overflow-hidden">
        <div className="flex items-center justify-between px-4 py-3 bg-[var(--bg-surface)]">
          <span className="text-xs text-[var(--text-muted)]">PIN status</span>
          <span className={`text-sm font-medium ${hasPIN ? 'text-[var(--status-success)]' : 'text-[var(--text-muted)]'}`}>
            {hasPIN === null ? '—' : hasPIN ? 'Set' : 'Not set'}
          </span>
        </div>
        {status && (
          <>
            <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
              <span className="text-xs text-[var(--text-muted)]">Lockout state</span>
              <span className={`text-sm ${
                status.permanent_lock ? 'text-[var(--status-danger)]' :
                status.locked ? 'text-[var(--status-warning)]' :
                'text-[var(--text-tertiary)]'
              }`}>
                {status.permanent_lock ? 'Permanently locked — re-auth required' :
                 status.locked ? 'Temporarily locked' :
                 `${status.attempts_left ?? 5} attempts remaining`}
              </span>
            </div>
            {status.locked && !status.permanent_lock && status.locked_until && (
              <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
                <span className="text-xs text-[var(--text-muted)]">Unlocks at</span>
                <span className="text-sm text-[var(--text-tertiary)]">
                  {new Date(status.locked_until).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                </span>
              </div>
            )}
          </>
        )}
      </div>

      {/* Full-auth requirement note */}
      <p id="pin-help" className="text-xs text-[var(--text-muted)] mb-5 leading-relaxed">
        Setting or changing the PIN requires a full-auth (password) session. The PIN
        never leaves this device — it is derived locally with argon2id and sealed via
        the TPM where available. Use 4 to 8 digits.
      </p>

      {/* Set / change PIN form */}
      <form onSubmit={handleSet} className="space-y-3 mb-6">
        <h3 className="text-sm font-medium text-[var(--text-secondary)]">{hasPIN ? 'Change PIN' : 'Set PIN'}</h3>
        <Field label="New PIN (4–8 digits)">
          <input
            type="password"
            inputMode="numeric"
            pattern="[0-9]*"
            value={newPIN}
            onChange={e => setNewPIN(e.target.value.replace(/[^0-9]/g, '').slice(0, 8))}
            placeholder="••••"
            maxLength={8}
            className="input w-40"
            autoComplete="new-password"
            aria-describedby="pin-help"
          />
        </Field>
        <Field label="Confirm PIN">
          <input
            type="password"
            inputMode="numeric"
            pattern="[0-9]*"
            value={confirmPIN}
            onChange={e => setConfirmPIN(e.target.value.replace(/[^0-9]/g, '').slice(0, 8))}
            placeholder="••••"
            maxLength={8}
            className="input w-40"
            autoComplete="new-password"
            aria-describedby="pin-help"
          />
        </Field>
        <button
          type="submit"
          disabled={busy || !newPIN}
          className="btn disabled:opacity-50"
        >
          {busy ? 'Saving…' : hasPIN ? 'Change PIN' : 'Set PIN'}
        </button>
      </form>

      {/* Remove PIN */}
      {hasPIN && (
        <div className="pt-4 border-t border-[var(--border-default)]">
          <h3 className="text-sm font-medium text-[var(--text-secondary)] mb-2">Remove PIN</h3>
          <p className="text-xs text-[var(--text-faint)] mb-3">
            Removes the PIN from this device. The lock screen will allow entry without a code.
          </p>
          <button
            onClick={handleDisable}
            disabled={busy}
            className="btn-ghost text-[var(--status-danger)] disabled:opacity-50"
          >
            Remove PIN
          </button>
        </div>
      )}

      {/* Status message */}
      {msg && (
        <p className={`mt-4 text-sm ${msg.type === 'ok' ? 'text-[var(--status-success)]' : 'text-[var(--status-danger)]'}`}>
          {msg.text}
        </p>
      )}
    </Section>
  )
}

// --- Fingerprint Unlock (CLOGIN-07) ---
//
// Shows "Add fingerprint" only when the backend reports a supported fprintd
// device (available=true from GET /api/auth/fingerprint/status). Hidden when
// no hardware is detected. Configurable per-profile; disabling requires a
// full-auth session (enforced server-side).
interface FingerprintStatus {
  available?: boolean
  enrolled?: boolean
  hardware_note?: string
  failures_left?: number
}
function toFingerprintStatus(x: unknown): FingerprintStatus | null {
  if (!isRecord(x)) return null
  return {
    available: typeof x.available === 'boolean' ? x.available : undefined,
    enrolled: typeof x.enrolled === 'boolean' ? x.enrolled : undefined,
    hardware_note: typeof x.hardware_note === 'string' ? x.hardware_note : undefined,
    failures_left: typeof x.failures_left === 'number' ? x.failures_left : undefined,
  }
}

function FingerprintSettings() {
  const [status, setStatus] = useState<FingerprintStatus | null>(null)   // FingerprintStatus from server
  const [msg, setMsg] = useState<StatusMsg | null>(null)          // { type: 'ok'|'err', text }
  const [busy, setBusy] = useState(false)
  const [enrolling, setEnrolling] = useState(false)

  const loadStatus = () => {
    fetch('/api/auth/fingerprint/status')
      .then(r => r.ok ? r.json() : null)
      .then((raw: unknown) => { const data = toFingerprintStatus(raw); if (data) setStatus(data) })
      .catch(() => {})
  }

  useEffect(() => { loadStatus() }, [])

  const handleEnrollStart = async () => {
    setBusy(true)
    setMsg(null)
    try {
      const res = await fetch('/api/auth/fingerprint/enroll/start', { method: 'POST' })
      const data: unknown = await res.json()
      if (!res.ok) {
        setMsg({ type: 'err', text: errField(data) || 'Could not start enrollment' })
      } else {
        setEnrolling(true)
        setMsg({ type: 'ok', text: 'Scan your finger on the reader — then press Done.' })
      }
    } catch {
      setMsg({ type: 'err', text: 'Could not reach server' })
    } finally {
      setBusy(false)
    }
  }

  const handleEnrollStop = async () => {
    setBusy(true)
    setMsg(null)
    try {
      const res = await fetch('/api/auth/fingerprint/enroll/stop', { method: 'POST' })
      const data: unknown = await res.json()
      if (!res.ok) {
        setMsg({ type: 'err', text: errField(data) || 'Enrollment did not complete' })
      } else if (isRecord(data) && data.enrolled) {
        setMsg({ type: 'ok', text: 'Fingerprint enrolled — you can now unlock with your finger.' })
        setEnrolling(false)
        loadStatus()
      } else {
        setMsg({ type: 'err', text: 'No finger was enrolled — please try again.' })
        setEnrolling(false)
      }
    } catch {
      setMsg({ type: 'err', text: 'Could not reach server' })
    } finally {
      setBusy(false)
    }
  }

  const handleRemove = async () => {
    if (!confirm('Remove fingerprint unlock? You will need to use your PIN or password to unlock.')) return
    setBusy(true)
    setMsg(null)
    try {
      const res = await fetch('/api/auth/fingerprint/remove', { method: 'POST' })
      const data: unknown = await res.json()
      if (!res.ok) {
        setMsg({ type: 'err', text: errField(data) || 'Could not remove fingerprint' })
      } else {
        setMsg({ type: 'ok', text: 'Fingerprint unlock removed.' })
        loadStatus()
      }
    } catch {
      setMsg({ type: 'err', text: 'Could not reach server' })
    } finally {
      setBusy(false)
    }
  }

  // Loading state
  if (status === null) {
    return (
      <Section title="Fingerprint Unlock">
        <p className="text-sm text-[var(--text-muted)]">Checking hardware…</p>
      </Section>
    )
  }

  // No supported hardware — gate the entire UI
  if (!status.available) {
    return (
      <Section title="Fingerprint Unlock">
        <div className="rounded-xl border border-[var(--border-default)] overflow-hidden mb-5">
          <div className="flex items-center justify-between px-4 py-3 bg-[var(--bg-surface)]">
            <span className="text-xs text-[var(--text-muted)]">Hardware status</span>
            <span className="text-sm text-[var(--text-muted)]">Not available</span>
          </div>
        </div>
        <p className="text-xs text-[var(--text-faint)] mb-4 leading-relaxed">
          No supported fingerprint reader was detected on this device.
          Fingerprint unlock requires a libfprint-compatible reader registered with fprintd.
        </p>
        <div className="rounded-xl border border-[var(--border-default)] overflow-hidden">
          <div className="px-4 py-3 bg-[var(--bg-surface)]">
            <p className="text-xs font-medium text-[var(--text-tertiary)] mb-2">Supported hardware (examples)</p>
            <ul className="text-xs text-[var(--text-faint)] space-y-0.5">
              <li>Synaptics (USB 06cb:xxxx)</li>
              <li>Goodix (USB 27c6:xxxx)</li>
              <li>ELAN (USB 04f3:xxxx)</li>
              <li>AuthenTec (USB 08ff:xxxx)</li>
              <li>DigitalPersona (USB 05ba:xxxx)</li>
              <li>Validity Sensors (USB 138a:xxxx)</li>
            </ul>
            <p className="text-[12px] text-[var(--text-tertiary)] mt-2">
              Virtual machines without USB passthrough and macOS/Windows are not supported.
              Install fprintd and libfprint2 to enable this feature.
            </p>
          </div>
        </div>
      </Section>
    )
  }

  return (
    <Section title="Fingerprint Unlock">
      {/* Status card */}
      <div className="mb-5 rounded-xl border border-[var(--border-default)] overflow-hidden">
        <div className="flex items-center justify-between px-4 py-3 bg-[var(--bg-surface)]">
          <span className="text-xs text-[var(--text-muted)]">Status</span>
          <span className={`text-sm font-medium ${status.enrolled ? 'text-[var(--status-success)]' : 'text-[var(--text-muted)]'}`}>
            {status.enrolled ? 'Enrolled' : 'Not enrolled'}
          </span>
        </div>
        {status.hardware_note && (
          <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
            <span className="text-xs text-[var(--text-muted)]">Reader</span>
            <span className="text-sm text-[var(--text-tertiary)]">{status.hardware_note}</span>
          </div>
        )}
        {status.enrolled && (
          <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
            <span className="text-xs text-[var(--text-muted)]">Unlock attempts left</span>
            <span className={`text-sm ${(status.failures_left ?? 0) <= 1 ? 'text-[var(--status-warning)]' : 'text-[var(--text-tertiary)]'}`}>
              {status.failures_left} of 3
            </span>
          </div>
        )}
      </div>

      {/* Info note */}
      <p className="text-xs text-[var(--text-faint)] mb-5 leading-relaxed">
        Fingerprint unlock releases the same device-local credential as your PIN.
        After 3 failed scans the lock screen falls back to PIN or password.
        Disabling fingerprint unlock requires a full-auth (password) session.
      </p>

      {/* Enroll flow */}
      {!status.enrolled && !enrolling && (
        <button
          onClick={handleEnrollStart}
          disabled={busy}
          className="btn disabled:opacity-50 mb-4"
        >
          {busy ? 'Starting…' : 'Add fingerprint'}
        </button>
      )}

      {enrolling && (
        <div className="mb-4 p-4 rounded-xl bg-[var(--accent-soft)] border accent-border-soft">
          <p className="text-sm text-[var(--accent)] mb-3">
            Place your finger on the reader and lift it several times as prompted.
          </p>
          <button
            onClick={handleEnrollStop}
            disabled={busy}
            className="btn disabled:opacity-50"
          >
            {busy ? 'Saving…' : 'Done — save fingerprint'}
          </button>
        </div>
      )}

      {/* Re-enroll when already enrolled */}
      {status.enrolled && !enrolling && (
        <button
          onClick={handleEnrollStart}
          disabled={busy}
          className="btn disabled:opacity-50 mb-4"
        >
          {busy ? 'Starting…' : 'Re-enroll fingerprint'}
        </button>
      )}

      {/* Remove */}
      {status.enrolled && (
        <div className="pt-4 border-t border-[var(--border-default)]">
          <h3 className="text-sm font-medium text-[var(--text-secondary)] mb-2">Remove fingerprint</h3>
          <p className="text-xs text-[var(--text-faint)] mb-3">
            Removes fingerprint unlock from this device. The lock screen will
            require your PIN or password.
          </p>
          <button
            onClick={handleRemove}
            disabled={busy}
            className="btn-ghost text-[var(--status-danger)] disabled:opacity-50"
          >
            Remove fingerprint
          </button>
        </div>
      )}

      {/* Hardware support matrix */}
      <div className="mt-6 rounded-xl border border-[var(--border-default)] overflow-hidden">
        <div className="px-4 py-3 bg-[var(--bg-surface)]">
          <p className="text-xs font-medium text-[var(--text-tertiary)] mb-2">Supported hardware (examples)</p>
          <ul className="text-xs text-[var(--text-faint)] space-y-0.5">
            <li>Synaptics (USB 06cb:xxxx)</li>
            <li>Goodix (USB 27c6:xxxx)</li>
            <li>ELAN (USB 04f3:xxxx)</li>
            <li>AuthenTec (USB 08ff:xxxx)</li>
            <li>DigitalPersona (USB 05ba:xxxx)</li>
            <li>Validity Sensors (USB 138a:xxxx)</li>
          </ul>
          <p className="text-[12px] text-[var(--text-tertiary)] mt-2">
            Requires fprintd + libfprint2 on Linux.
            Virtual machines without USB passthrough are not supported.
          </p>
        </div>
      </div>

      {/* Status message */}
      {msg && (
        <p className={`mt-4 text-sm ${msg.type === 'ok' ? 'text-[var(--status-success)]' : 'text-[var(--status-danger)]'}`}>
          {msg.text}
        </p>
      )}
    </Section>
  )
}

// --- AI Apps Gallery ---
interface AiApp {
  id: string
  title?: string
  created?: string
  has_python?: string
}
function toAiApp(x: unknown): AiApp | null {
  if (!isRecord(x) || typeof x.id !== 'string') return null
  return {
    id: x.id,
    title: typeof x.title === 'string' ? x.title : undefined,
    created: typeof x.created === 'string' ? x.created : undefined,
    has_python: typeof x.has_python === 'string' ? x.has_python : undefined,
  }
}
function toAiApps(x: unknown): AiApp[] {
  if (!Array.isArray(x)) return []
  return x.map(toAiApp).filter((a): a is AiApp => a !== null)
}
interface AiAppVersion {
  version: string
  timestamp?: string
  brief?: string
}
function toAiAppVersion(x: unknown): AiAppVersion | null {
  if (!isRecord(x) || (typeof x.version !== 'string' && typeof x.version !== 'number')) return null
  return {
    version: String(x.version),
    timestamp: typeof x.timestamp === 'string' ? x.timestamp : undefined,
    brief: typeof x.brief === 'string' ? x.brief : undefined,
  }
}
function toAiAppVersions(x: unknown): AiAppVersion[] {
  if (!Array.isArray(x)) return []
  return x.map(toAiAppVersion).filter((v): v is AiAppVersion => v !== null)
}
interface AiAppConfig {
  edit_disabled?: boolean
}
function toAiAppConfig(x: unknown): AiAppConfig | null {
  if (!isRecord(x)) return null
  return { edit_disabled: typeof x.edit_disabled === 'boolean' ? x.edit_disabled : undefined }
}

// AIAppPreview — opens a saved AI app inside a SANDBOXED iframe (opaque origin:
// `allow-scripts` only, deliberately NO `allow-same-origin`) instead of the old
// window.open('/api/ai-apps/{id}/html') escape hatch, which rendered untrusted
// AI-generated HTML top-level and same-origin — giving it the OS session cookie,
// localStorage, and gateway API. Here the app runs in a null origin and cannot
// reach the OS origin. The served HTML also ships a `sandbox` CSP as
// defense-in-depth (see routes_aiapps_security.go), so it is inert even if
// re-opened top-level. This mirrors the shell's in-window sandbox for AI apps.
interface AIAppPreviewProps {
  app: AiApp
  onClose: () => void
}
function AIAppPreview({ app, onClose }: AIAppPreviewProps) {
  return (
    <SettingsModal title={app.title || 'AI App'} onClose={onClose}>
      <div className="w-full h-[60vh] rounded-lg overflow-hidden border border-[var(--border-default)] bg-[var(--bg-base)]">
        <iframe
          src={`/api/ai-apps/${encodeURIComponent(app.id)}/html`}
          title={app.title || app.id}
          className="w-full h-full border-0"
          sandbox="allow-scripts allow-forms allow-popups"
          referrerPolicy="no-referrer"
        />
      </div>
      <p className="text-[12px] text-[var(--text-faint)] mt-2">
        Runs in an isolated sandbox — no access to your session or data.
      </p>
    </SettingsModal>
  )
}

interface AIAppVersionsProps {
  appId: string
  onClose: () => void
  editDisabled: boolean
}
function AIAppVersions({ appId, onClose, editDisabled }: AIAppVersionsProps) {
  const [versions, setVersions] = useState<AiAppVersion[]>([])
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState('')

  const [loadErr, setLoadErr] = useState<string | null>(null)

  // `.catch(() => setVersions([]))` turned a failed read into "this app has no
  // previous versions" — a fabricated empty state on the one panel whose whole
  // purpose is to let a user roll back. Failing to LIST versions and having
  // none to list must not look the same.
  useEffect(() => {
    apiGet(`/api/ai-apps/${appId}/versions`)
      .then((raw: unknown) => { setVersions(toAiAppVersions(raw)); setLoadErr(null) })
      .catch((e: unknown) => setLoadErr(errorMessage(e)))
  }, [appId])

  const rollback = async (version: string) => {
    setBusy(true)
    setMsg('')
    try {
      const r = await fetch(`/api/ai-apps/${appId}/rollback`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ version }),
      })
      if (r.status === 503) { setMsg('AI-app editing is disabled by the administrator'); return }
      const d: unknown = await r.json()
      if (!r.ok) setMsg(errField(d) || 'Rollback failed')
      else setMsg('Rolled back successfully')
    } catch {
      setMsg('Request failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mt-2 mb-4 rounded border border-[var(--border-strong)] bg-[var(--bg-surface)] p-3">
      <div className="flex items-center justify-between mb-2">
        <span className="text-xs font-semibold text-[var(--text-secondary)]">Version History</span>
        <button onClick={onClose} className="text-xs text-[var(--text-muted)] hover:text-[var(--text-secondary)]">Close</button>
      </div>
      {loadErr && <p role="alert" className="text-xs text-[var(--status-danger)]">Could not load the version list: {loadErr}</p>}
      {!loadErr && versions.length === 0 && <p className="text-xs text-[var(--text-muted)]">No snapshots yet.</p>}
      {versions.map(v => (
        <div key={v.version} className="flex items-center justify-between py-1 border-b border-[var(--border-default)]">
          <div>
            <span className="text-xs text-[var(--text-tertiary)]">{v.timestamp}</span>
            {v.brief && <span className="text-[12px] text-[var(--text-faint)] ml-2">{v.brief}</span>}
          </div>
          <button
            onClick={() => rollback(v.version)}
            disabled={busy || editDisabled}
            title={editDisabled ? 'Editing disabled by administrator' : 'Restore this version'}
            className="text-[12px] text-[var(--status-warning)] hover:text-[var(--status-warning)] disabled:opacity-50 disabled:cursor-not-allowed"
          >
            Restore
          </button>
        </div>
      ))}
      {msg && <p className="text-xs mt-2 text-[var(--status-success)]">{msg}</p>}
    </div>
  )
}

function AIAppsSettings() {
  const [apps, setApps] = useState<AiApp[]>([])
  const [visRefreshKey, setVisRefreshKey] = useState(0)
  const [versionsOpen, setVersionsOpen] = useState<string | null>(null)
  const [preview, setPreview] = useState<AiApp | null>(null)
  const [editDisabled, setEditDisabled] = useState(false)
  const refresh = useCallback(() => apiGet('/api/ai-apps').then((raw: unknown) => setApps(toAiApps(raw))).catch(() => {}), [])
  useEffect(() => { refresh() }, [refresh])
  // Surface the DISABLE_AI_APP_EDIT kill-switch so mutating actions render as
  // clearly-disabled controls + a banner, not just a failure toast on click.
  useEffect(() => {
    fetch('/api/ai-apps/config')
      .then(r => r.ok ? r.json() : null)
      .then((raw: unknown) => setEditDisabled(!!toAiAppConfig(raw)?.edit_disabled))
      .catch(() => {})
  }, [])

  const remove = async (id: string) => {
    const r = await fetch(`/api/ai-apps/${id}`, { method: 'DELETE' })
    if (r.status === 503) { setEditDisabled(true); return }
    if (versionsOpen === id) setVersionsOpen(null)
    refresh()
  }

  const handleVisChanged = useCallback(() => { setVisRefreshKey(k => k + 1) }, [])
  const toggleVersions = (id: string) => { setVersionsOpen(prev => prev === id ? null : id) }

  return (
    <Section title="AI-Generated Apps">
      <p className="text-xs text-[var(--text-faint)] mb-4">Apps created by the AI assistant. Open runs each app in an isolated sandbox.</p>
      {editDisabled && (
        <div className="mb-4 rounded border border-warning-soft bg-[var(--status-warning-soft)] px-3 py-2 text-xs text-[var(--status-warning)]">
          AI-app editing is disabled by the administrator (DISABLE_AI_APP_EDIT). You can still open existing apps, but saving, updating, deleting, snapshotting and rollback are turned off.
        </div>
      )}
      {apps?.length === 0 && <p className="text-sm text-[var(--text-muted)]">No saved apps yet. Ask the AI to build something visual.</p>}
      {apps?.map(app => (
        <div key={app.id} className="flex items-center justify-between py-2 border-b border-[var(--border-default)] gap-2">
          <div className="min-w-0">
            <span className="text-sm">{app.title || 'Untitled'}</span>
            <span className="text-xs text-[var(--text-faint)] ml-2">{app.created?.slice(0, 10)}</span>
            {app.has_python === 'true' && <span className="text-[12px] text-[var(--accent)] ml-2">Python</span>}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <PamVisibilityControl key={`${app.id}-${visRefreshKey}`} appId={app.id} onChanged={handleVisChanged} />
            <button onClick={() => toggleVersions(app.id)} className="text-xs text-[var(--text-tertiary)]">Versions</button>
            <button onClick={() => setPreview(app)} className="text-xs text-[var(--accent)]">Open</button>
            <button
              onClick={() => remove(app.id)}
              disabled={editDisabled}
              title={editDisabled ? 'Editing disabled by administrator' : 'Delete app'}
              className="text-xs text-[var(--status-danger)] disabled:opacity-40 disabled:cursor-not-allowed"
            >
              Delete
            </button>
          </div>
          {versionsOpen === app.id && (
            <AIAppVersions appId={app.id} editDisabled={editDisabled} onClose={() => setVersionsOpen(null)} />
          )}
        </div>
      ))}
      {preview && <AIAppPreview app={preview} onClose={() => setPreview(null)} />}
    </Section>
  )
}

// --- Vault / Backup ---
interface VaultStatus {
  initialized?: boolean
  last_backup?: string
}
function toVaultStatus(x: unknown): VaultStatus | null {
  if (!isRecord(x)) return null
  return {
    initialized: typeof x.initialized === 'boolean' ? x.initialized : undefined,
    last_backup: typeof x.last_backup === 'string' ? x.last_backup : undefined,
  }
}
interface VaultSync {
  total_snapshots?: number
  other_devices?: string[]
}
function toVaultSync(x: unknown): VaultSync | null {
  if (!isRecord(x)) return null
  return {
    total_snapshots: typeof x.total_snapshots === 'number' ? x.total_snapshots : undefined,
    other_devices: Array.isArray(x.other_devices) ? x.other_devices.filter((d): d is string => typeof d === 'string') : undefined,
  }
}

function VaultSettings() {
  const [status, setStatus] = useState<VaultStatus | null>(null)
  const [sync, setSync] = useState<VaultSync | null>(null)
  const { error, attempt } = useApiError()
  const [msg, setMsg] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const refresh = useCallback(() => attempt(async () => {
    setStatus(toVaultStatus(await apiGet('/api/vault/status')))
    setSync(toVaultSync(await apiGet('/api/vault/sync')))
  }), [attempt])
  useEffect(() => { refresh() }, [refresh])

  // "Backup Now" was `fetch(…).then(refresh)`. Of every silent write found in
  // this file this is the one that matters most: a backup button that reports
  // nothing is a backup button the user believes in, and the failure only
  // surfaces on the day they try to restore. The box answers 403/500 here.
  const run = async (path: string, done: string) => {
    setBusy(true)
    setMsg(null)
    const ok = await attempt(async () => { await apiSend(path); return true })
    setBusy(false)
    if (!ok) return
    setMsg(done)
    setTimeout(() => setMsg(null), 4000)
    refresh()
  }
  const backup = () => run('/api/vault/backup', 'Backup complete')
  const syncDevice = () => run('/api/vault/sync', 'Synced to this device')

  return (
    <Section title="Backup & Sync">
      {error && <Banner tone="danger" title="Backup failed">{error}</Banner>}
      {msg && <Banner tone="success">{msg}</Banner>}
      <div className={`text-sm mb-3 ${status?.initialized ? 'text-[var(--status-success)]' : 'text-[var(--text-muted)]'}`}>
        {status?.initialized ? 'Vault initialized' : 'Vault not configured'}
      </div>
      {status?.initialized && (
        <>
          <p className="text-xs text-[var(--text-muted)] mb-1">Last backup: {status?.last_backup || 'never'}</p>
          <p className="text-xs text-[var(--text-muted)] mb-3">Snapshots: {sync?.total_snapshots || 0}</p>
          <div className="flex gap-2 mb-4">
            <button onClick={backup} disabled={busy} className="btn">{busy ? 'Working…' : 'Backup Now'}</button>
            <button onClick={syncDevice} disabled={busy} className="btn">Sync to This Device</button>
          </div>
          {(sync?.other_devices?.length ?? 0) > 0 && (
            <>
              <h3 className="text-xs uppercase text-[var(--text-muted)] tracking-wider mb-2">Other Devices</h3>
              {sync?.other_devices?.map(d => <p key={d} className="text-sm text-[var(--text-tertiary)]">{d}</p>)}
            </>
          )}
        </>
      )}
      {!status?.initialized && (
        <p className="text-xs text-[var(--text-faint)]">Set S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY, S3_SECRET_KEY to enable backups.</p>
      )}
    </Section>
  )
}

// --- Recall / Search ---
interface RecallStatus {
  indexed_files?: number
  total_files?: number
  last_index?: string
  indexing?: boolean
}
function toRecallStatus(x: unknown): RecallStatus | null {
  if (!isRecord(x)) return null
  return {
    indexed_files: typeof x.indexed_files === 'number' ? x.indexed_files : undefined,
    total_files: typeof x.total_files === 'number' ? x.total_files : undefined,
    last_index: typeof x.last_index === 'string' ? x.last_index : undefined,
    indexing: typeof x.indexing === 'boolean' ? x.indexing : undefined,
  }
}

function RecallSettings() {
  const [status, setStatus] = useState<RecallStatus | null>(null)
  const { error, attempt } = useApiError()
  const [msg, setMsg] = useState<string | null>(null)

  const refresh = useCallback(
    () => attempt(async () => setStatus(toRecallStatus(await apiGet('/api/recall/status')))),
    [attempt],
  )
  useEffect(() => { refresh() }, [refresh])

  // The box answers 503 here when the recall service is not running at all —
  // which the old code read as JSON and threw away, leaving "Files indexed: 0 /
  // Status: Ready". A box with no search service claimed to be ready.
  const reindex = async () => {
    setMsg(null)
    const ok = await attempt(async () => { await apiSend('/api/recall/index'); return true })
    if (!ok) return
    setMsg('Indexing started')
    setTimeout(() => setMsg(null), 4000)
    refresh()
  }

  return (
    <Section title="Search & Index">
      {error && <Banner tone="danger" title="Search index is not available">{error}</Banner>}
      {msg && <Banner tone="success">{msg}</Banner>}
      <p className="text-xs text-[var(--text-faint)] mb-3">Recall indexes your files for semantic search. The AI uses this to answer questions about your data.</p>
      {status && (
        <div className="space-y-1 text-sm mb-4">
          <p>Files indexed: <span className="text-[var(--text-secondary)]">{status.indexed_files || 0}</span></p>
          <p>Total scanned: <span className="text-[var(--text-secondary)]">{status.total_files || 0}</span></p>
          <p>Last index: <span className="text-[var(--text-secondary)]">{status.last_index || 'never'}</span></p>
          <p>Status: <span className={status.indexing ? 'text-[var(--status-warning)]' : 'text-[var(--status-success)]'}>{status.indexing ? 'Indexing...' : 'Ready'}</span></p>
        </div>
      )}
      <button onClick={reindex} className="btn">Re-index Now</button>
    </Section>
  )
}

// --- Storage Mode (STORE-LOCAL-01 / D-STORE-LOCAL-DEFAULT) ---
// Bundle-wide selector. The default is local-fs — this box's own disk, no
// object store and no third-party service. Local MinIO-with-sync and hosted
// central Tigris are both opt-ins. Toggling to local-minio-sync passes the
// endpoint + bucket + creds-ref to the co-located lilmail and diwan
// services via VULOS_STORAGE_MODE / VULOS_MINIO_* env vars. The CRDT sync
// layer (STORE-SYNC-01 / OFFICE-SYNC-01 / SYNC-P2P-01) lives in the sibling
// repos and is engaged purely by the mode flip — no UI plumbing required.
interface StorageModeConfig {
  mode?: string
  minio_endpoint?: string
  minio_region?: string
  minio_bucket?: string
  minio_creds_ref?: string
}
function toStorageModeConfig(x: unknown): StorageModeConfig | null {
  if (!isRecord(x)) return null
  return {
    mode: typeof x.mode === 'string' ? x.mode : undefined,
    minio_endpoint: typeof x.minio_endpoint === 'string' ? x.minio_endpoint : undefined,
    minio_region: typeof x.minio_region === 'string' ? x.minio_region : undefined,
    minio_bucket: typeof x.minio_bucket === 'string' ? x.minio_bucket : undefined,
    minio_creds_ref: typeof x.minio_creds_ref === 'string' ? x.minio_creds_ref : undefined,
  }
}
interface StorageModeDraft {
  mode: string
  minio_endpoint: string
  minio_region: string
  minio_bucket: string
  minio_creds_ref: string
}
function StorageModeSettings() {
  const [cfg, setCfg] = useState<StorageModeConfig | null>(null)
  const [draft, setDraft] = useState<StorageModeDraft>({
    mode: 'local-fs',
    minio_endpoint: 'http://127.0.0.1:9000',
    minio_region: 'auto',
    minio_bucket: 'vulos-bundle',
    minio_creds_ref: '/var/lib/vulos/minio/.minio_secret',
  })
  const [saving, setSaving] = useState(false)
  const [msg, setMsg] = useState('')

  useEffect(() => {
    fetch('/api/storagemode')
      .then(r => r.json())
      .then((raw: unknown) => {
        const d = toStorageModeConfig(raw) || {}
        setCfg(d)
        // Seed the draft from the current config so the form is in sync.
        setDraft(prev => ({
          ...prev,
          mode: d.mode || 'local-fs',
          minio_endpoint: d.minio_endpoint || prev.minio_endpoint,
          minio_region: d.minio_region || prev.minio_region,
          minio_bucket: d.minio_bucket || prev.minio_bucket,
          minio_creds_ref: d.minio_creds_ref || prev.minio_creds_ref,
        }))
      })
      .catch(() => {})
  }, [])

  const save = async () => {
    setSaving(true)
    setMsg('')
    try {
      // Send the SELECTED mode — never a hard-coded backend, or the picker
      // silently becomes a two-state toggle that always lands on hosted.
      const body: StorageModeDraft | { mode: string } = draft.mode === 'local-minio-sync' ? draft : { mode: draft.mode || 'local-fs' }
      const res = await fetch('/api/storagemode', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (res.ok) {
        const d = toStorageModeConfig(await res.json())
        setCfg(d)
        setMsg('Saved — restart vulos-bundle.target to apply')
        setTimeout(() => setMsg(''), 4000)
      } else {
        const e: unknown = await res.json().catch(() => ({}))
        setMsg(errField(e) || 'Failed to save')
      }
    } catch {
      setMsg('Could not reach server')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Section title="Storage Mode">
      <p className="text-xs text-[var(--text-faint)] mb-4">
        Where the bundle (mail, office, OS) reads and writes its objects. The default sends every read and write
        directly to hosted Tigris. Switch to local-MinIO-with-sync to make a co-located MinIO the source of truth and
        let the CRDT sync layer replicate to peer Vulos nodes (mirrors what scripts/install-vulos.sh --storage=minio
        provisions: a local /usr/local/bin/minio service plus /etc/vulos/storage.yaml).
      </p>

      <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)] mb-4">
        <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
          <span className="text-xs text-[var(--text-muted)]">Current mode</span>
          <span className="text-sm font-medium text-[var(--text-primary)]">
            {cfg == null ? '…' : cfg.mode === 'local-minio-sync' ? 'Local MinIO + sync' : 'Central Tigris (default)'}
          </span>
        </div>
        {cfg?.mode === 'local-minio-sync' && (
          <>
            <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
              <span className="text-xs text-[var(--text-muted)]">MinIO endpoint</span>
              <span className="text-sm text-[var(--text-secondary)]">{cfg.minio_endpoint || '—'}</span>
            </div>
            <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
              <span className="text-xs text-[var(--text-muted)]">Bucket</span>
              <span className="text-sm text-[var(--text-secondary)]">{cfg.minio_bucket || '—'}</span>
            </div>
          </>
        )}
      </div>

      <Field label="Mode">
        <select
          value={draft.mode}
          onChange={e => setDraft(d => ({ ...d, mode: e.target.value }))}
          className="input"
        >
          <option value="local-fs">This device (default — no third-party service)</option>
          <option value="local-minio-sync">Local MinIO + CRDT sync (opt-in)</option>
          <option value="central-tigris">Central Tigris (opt-in — hosted third-party S3)</option>
        </select>
      </Field>

      {draft.mode === 'local-minio-sync' && (
        <div className="space-y-3 mt-3 animate-[fadeIn_0.2s_ease-out]">
          <Field label="MinIO endpoint">
            <input
              value={draft.minio_endpoint}
              onChange={e => setDraft(d => ({ ...d, minio_endpoint: e.target.value }))}
              placeholder="http://127.0.0.1:9000"
              className="input"
            />
          </Field>
          <Field label="Region">
            <input
              value={draft.minio_region}
              onChange={e => setDraft(d => ({ ...d, minio_region: e.target.value }))}
              placeholder="auto"
              className="input"
            />
          </Field>
          <Field label="Bucket">
            <input
              value={draft.minio_bucket}
              onChange={e => setDraft(d => ({ ...d, minio_bucket: e.target.value }))}
              placeholder="vulos-bundle"
              className="input"
            />
          </Field>
          <Field label="Credentials reference">
            <input
              value={draft.minio_creds_ref}
              onChange={e => setDraft(d => ({ ...d, minio_creds_ref: e.target.value }))}
              placeholder="/var/lib/vulos/minio/.minio_secret"
              className="input"
            />
            <p className="text-[12px] text-[var(--text-faint)] mt-1">
              Path or secret-store key — the secret itself is never stored here. The installer writes
              /var/lib/vulos/minio/.minio_secret when run with --storage=minio.
            </p>
          </Field>
        </div>
      )}

      <div className="flex items-center gap-3 mt-4">
        <button onClick={save} disabled={saving} className="btn">
          {saving ? 'Saving…' : 'Save'}
        </button>
        {msg && <span className="text-xs text-[var(--text-tertiary)]">{msg}</span>}
      </div>
    </Section>
  )
}

// --- Users & Profiles ---
interface ProfileEntry {
  user_id: string
  display_name?: string
  role: string
}
function toProfileEntry(x: unknown): ProfileEntry | null {
  if (!isRecord(x) || typeof x.user_id !== 'string' || typeof x.role !== 'string') return null
  return {
    user_id: x.user_id,
    display_name: typeof x.display_name === 'string' ? x.display_name : undefined,
    role: x.role,
  }
}
function toProfileEntries(x: unknown): ProfileEntry[] {
  if (!Array.isArray(x)) return []
  return x.map(toProfileEntry).filter((p): p is ProfileEntry => p !== null)
}
interface NewUserDraft {
  username: string
  password: string
  displayName: string
}
interface UsersSettingsProps {
  profile: AuthProfile | null
}
function UsersSettings({ profile }: UsersSettingsProps) {
  const [profiles, setProfiles] = useState<ProfileEntry[]>([])
  const [pin, setPin] = useState('')
  const [newUser, setNewUser] = useState<NewUserDraft>({ username: '', password: '', displayName: '' })
  const [addError, setAddError] = useState('')
  const [addSuccess, setAddSuccess] = useState('')
  // Two separate error slots, because these are two different failures and
  // collapsing them would report the wrong one. `listError` is "the user list
  // could not be loaded"; `error` is "the change you just made was refused".
  // Sharing one slot also let a re-read quietly overwrite a write failure.
  const { error, attempt } = useApiError()
  const { error: listError, attempt: attemptList } = useApiError()

  const refresh = useCallback(
    () => attemptList(async () => setProfiles(toProfileEntries(await apiGet('/api/profiles')))),
    [attemptList],
  )
  useEffect(() => { refresh() }, [refresh])

  const isAdmin = profile?.role === 'admin'

  // Both of these are privilege operations and both ignored their response
  // entirely. The box answers 403 to a non-admin, 400 to a self-delete and 404
  // to an unknown id — and every one of those came back as a silent re-read of
  // an unchanged list, so "make this person an admin" and "remove this user"
  // reported exactly the same thing whether they happened or not.
  // Note the `if (ok)` guard on the re-read rather than an unconditional
  // refresh(): a refresh that succeeds resolves the shared error state back to
  // null, so re-reading the list straight after a REFUSED write erased the
  // banner explaining the refusal before it could be seen.
  const setRole = async (userId: string, role: string) => {
    const ok = await attempt(async () => {
      await apiSend(`/api/profiles/${userId}/role`, { method: 'PUT', body: JSON.stringify({ role }) })
      return true
    })
    if (ok) refresh()
  }

  const removeUser = async (userId: string) => {
    if (!confirm('Remove this user? This cannot be undone.')) return
    const ok = await attempt(async () => {
      await apiSend(`/api/profiles/${userId}`, { method: 'DELETE' })
      return true
    })
    if (ok) refresh()
  }

  const addUser = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    setAddError('')
    setAddSuccess('')
    try {
      const res = await fetch('/api/auth/register', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          username: newUser.username,
          password: newUser.password,
          display_name: newUser.displayName || newUser.username,
        }),
      })
      const data: unknown = await res.json()
      if (!res.ok) {
        setAddError(errField(data) || 'Failed to create user')
        return
      }
      setNewUser({ username: '', password: '', displayName: '' })
      setAddSuccess(`User "${newUser.username}" created`)
      setTimeout(() => setAddSuccess(''), 3000)
      refresh()
    } catch {
      setAddError('Could not reach server')
    }
  }

  // This request previously had no failure path at all — a rejected fetch
  // (offline, 500, …) left the PIN field silently cleared with no sign
  // anything went wrong, unlike every other mutating action in this file.
  const [pinMsg, setPinMsg] = useState<StatusMsg | null>(null)
  const [pinSaving, setPinSaving] = useState(false)
  const savePin = async () => {
    setPinSaving(true)
    setPinMsg(null)
    try {
      const res = await fetch('/api/auth/pin/set', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pin }),
      })
      const data: unknown = await res.json().catch(() => ({}))
      if (!res.ok) {
        setPinMsg({ type: 'err', text: errField(data) || 'Failed to update PIN' })
        return
      }
      setPinMsg({ type: 'ok', text: pin ? 'PIN set' : 'PIN removed' })
      setPin('')
    } catch {
      setPinMsg({ type: 'err', text: 'Could not reach server' })
    } finally {
      setPinSaving(false)
    }
  }

  return (
    <Section title="Users & Profiles">
      {error && <Banner tone="danger" title="That change did not go through">{error}</Banner>}
      {/* PIN */}
      <div className="mb-6 pb-4 border-b border-[var(--border-default)]">
        <h3 className="text-sm font-medium mb-2">Lock Screen PIN</h3>
        <div className="flex gap-2">
          <input type="password" inputMode="numeric" value={pin} onChange={e => setPin(e.target.value.replace(/[^0-9]/g, ''))}
            aria-label="Lock screen PIN"
            placeholder="4–8 digit PIN" maxLength={8} className="input w-40 max-w-[55vw]" />
          <button onClick={savePin} disabled={pinSaving} className="btn disabled:opacity-50">
            {pinSaving ? 'Saving…' : pin ? 'Set PIN' : 'Remove PIN'}
          </button>
        </div>
        {pinMsg && (
          <p className={`mt-2 text-xs ${pinMsg.type === 'ok' ? 'text-[var(--status-success)]' : 'text-[var(--status-danger)]'}`}>
            {pinMsg.text}
          </p>
        )}
      </div>

      {/* Add user (admin only) */}
      {isAdmin && (
        <div className="mb-6 pb-4 border-b border-[var(--border-default)]">
          <h3 className="text-sm font-medium mb-3">Add User</h3>
          <form onSubmit={addUser} className="space-y-2">
            <div className="flex gap-2">
              <input
                value={newUser.displayName}
                onChange={e => setNewUser({ ...newUser, displayName: e.target.value })}
                placeholder="Display name"
                className="input flex-1"
              />
              <input
                value={newUser.username}
                onChange={e => setNewUser({ ...newUser, username: e.target.value })}
                placeholder="Username"
                required
                className="input flex-1"
              />
            </div>
            <div className="flex gap-2">
              <input
                type="password"
                value={newUser.password}
                onChange={e => setNewUser({ ...newUser, password: e.target.value })}
                placeholder="Password (4+ chars)"
                required
                className="input flex-1"
              />
              <button type="submit" className="btn">Add</button>
            </div>
            {addError && <p className="text-xs text-[var(--status-danger)]">{addError}</p>}
            {addSuccess && <p className="text-xs text-[var(--status-success)]">{addSuccess}</p>}
          </form>
        </div>
      )}

      {/* User list */}
      <h3 className="text-sm font-medium mb-2">All Users</h3>
      {/* The failure is stated WITH its context in one string rather than
          echoing the bare server message: "boom" on its own tells the reader
          nothing about which of the several things on this panel broke. */}
      {listError && (
        <p role="alert" className="text-xs text-[var(--status-danger)] mb-2">
          Could not load the user list: {listError}
        </p>
      )}
      {profiles.map(p => (
        <div key={p.user_id} className="flex items-center justify-between py-2 border-b border-[var(--border-default)]">
          <div>
            <span className="text-sm">{p.display_name || 'Unnamed'}</span>
            <span className={`ml-2 text-[12px] px-1.5 py-0.5 rounded-full ${
              p.role === 'admin' ? 'bg-[var(--accent-soft)] text-[var(--accent)]' :
              p.role === 'guest' ? 'bg-[var(--bg-elevated)] text-[var(--text-muted)]' :
              'bg-[var(--bg-elevated)] text-[var(--text-tertiary)]'
            }`}>{p.role}</span>
          </div>
          {isAdmin && p.user_id !== profile?.user_id && (
            <div className="flex gap-2 shrink-0">
              <select value={p.role} onChange={e => setRole(p.user_id, e.target.value)}
                aria-label={`Role for ${p.display_name || 'user'}`}
                className="input text-xs py-1 w-24">
                <option value="admin">Admin</option>
                <option value="user">User</option>
                <option value="guest">Guest</option>
              </select>
              <button onClick={() => removeUser(p.user_id)} aria-label={`Remove ${p.display_name || 'user'}`} className="text-xs text-[var(--status-danger)] hover:text-[var(--status-danger)]">Remove</button>
            </div>
          )}
        </div>
      ))}
      {!isAdmin && <p className="text-xs text-[var(--text-faint)] mt-2">Only admins can manage users.</p>}
    </Section>
  )
}

// --- OS Update (box-side OTA client: verify-only, opt-in) ---
//
// The box polls vulos.org's release channel in the background and verifies
// every manifest against the baked release-signing trust chain — see
// backend/services/ota. That poll NEVER downloads the OS image and NEVER
// stages anything on its own. This panel just shows what the last verified
// check found and lets the owner explicitly choose to download + stage it;
// a security release additionally fires a priority notification, but even
// then nothing is staged until the button below is clicked.
interface OsUpdateStatus {
  latest_version?: string
  current_version?: string
  available?: boolean
  is_security?: boolean
  severity?: string
  published_at?: string
  checked_at?: string
  channel_url?: string
  notes?: string
  last_error?: string
}
function toOsUpdateStatus(x: unknown): OsUpdateStatus | null {
  if (!isRecord(x)) return null
  return {
    latest_version: typeof x.latest_version === 'string' ? x.latest_version : undefined,
    current_version: typeof x.current_version === 'string' ? x.current_version : undefined,
    available: typeof x.available === 'boolean' ? x.available : undefined,
    is_security: typeof x.is_security === 'boolean' ? x.is_security : undefined,
    severity: typeof x.severity === 'string' ? x.severity : undefined,
    published_at: typeof x.published_at === 'string' ? x.published_at : undefined,
    checked_at: typeof x.checked_at === 'string' ? x.checked_at : undefined,
    channel_url: typeof x.channel_url === 'string' ? x.channel_url : undefined,
    notes: typeof x.notes === 'string' ? x.notes : undefined,
    last_error: typeof x.last_error === 'string' ? x.last_error : undefined,
  }
}

function OSUpdateSettings() {
  const [status, setStatus] = useState<OsUpdateStatus | null>(null)
  const [staging, setStaging] = useState(false)
  const [stageResult, setStageResult] = useState('')
  const [error, setError] = useState('')

  const refresh = () => {
    fetch('/api/os/update/status')
      .then(async r => {
        const raw: unknown = await r.json().catch(() => ({}))
        if (!r.ok) throw new Error(errField(raw) || ('HTTP ' + r.status))
        setStatus(toOsUpdateStatus(raw))
        setError('')
      })
      .catch((e: unknown) => setError(errorMessage(e) || 'Could not reach server'))
  }

  useEffect(() => { refresh() }, [])

  const stage = async () => {
    if (!confirm(
      `Download and stage Vulos ${status?.latest_version || 'the update'}?\n\n` +
      'It will be verified and written to the inactive slot. Nothing is applied ' +
      'until you separately reboot — the box keeps running the current version until then.'
    )) return
    setStaging(true)
    setStageResult('')
    setError('')
    try {
      const r = await fetch('/api/os/update/stage', { method: 'POST' })
      const d: unknown = await r.json().catch(() => ({}))
      if (r.ok) {
        setStageResult((isRecord(d) && typeof d.message === 'string' ? d.message : '') || 'Staged — reboot to activate')
        refresh()
      } else {
        setError(errField(d) || ('HTTP ' + r.status))
      }
    } catch {
      setError('Request failed')
    } finally {
      setStaging(false)
    }
  }

  const isSecurity = !!status?.is_security

  return (
    <Section
      title="OS Update"
      desc="Vulos checks vulos.org for new releases and verifies them cryptographically — nothing is ever installed automatically. You choose when to stage an update, and when to reboot into it."
    >
      {isSecurity && (
        <div className="mb-4 flex items-start gap-2.5 rounded-lg px-3.5 py-3 bg-[var(--status-danger-soft)] border border-[var(--status-danger)]/30">
          <span className="text-base leading-none mt-0.5">⚠</span>
          <div>
            <p className="text-sm font-semibold text-[var(--status-danger)]">
              Security update{status?.severity ? ` — ${status.severity}` : ''}
            </p>
            <p className="text-xs text-[var(--status-danger)]/90 mt-0.5">
              Version {status?.latest_version} addresses a security issue. Staging and rebooting soon is recommended.
            </p>
          </div>
        </div>
      )}

      <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)] mb-5">
        <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
          <span className="text-xs text-[var(--text-muted)]">Running version</span>
          <span className="text-sm font-mono text-[var(--text-secondary)]">
            {status == null ? '…' : (status.current_version || '—')}
          </span>
        </div>
        <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
          <span className="text-xs text-[var(--text-muted)]">Latest available</span>
          {status?.available ? (
            <span className="flex items-center gap-1.5 text-sm font-mono text-[var(--status-success)]">
              {status.latest_version}
              {isSecurity && (
                <span className="px-1.5 py-0.5 rounded text-[12px] font-semibold tracking-wide bg-[var(--status-danger)] text-white">
                  SECURITY
                </span>
              )}
            </span>
          ) : (
            <span className="text-sm text-[var(--text-tertiary)]">{status == null ? '…' : 'up to date'}</span>
          )}
        </div>
        {status?.published_at && (
          <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
            <span className="text-xs text-[var(--text-muted)]">Published</span>
            <span className="text-sm text-[var(--text-tertiary)]">
              {new Date(status.published_at).toLocaleDateString()}
            </span>
          </div>
        )}
        {status?.checked_at && (
          <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
            <span className="text-xs text-[var(--text-muted)]">Last checked</span>
            <span className="text-sm text-[var(--text-tertiary)]">
              {new Date(status.checked_at).toLocaleString()}
            </span>
          </div>
        )}
        <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
          <span className="text-xs text-[var(--text-muted)]">Channel</span>
          <span className="text-sm font-mono text-[var(--text-faint)] truncate max-w-[60%]" title={status?.channel_url}>
            {status?.channel_url || '—'}
          </span>
        </div>
      </div>

      {status?.notes && (
        <div className="mb-4 text-xs rounded-lg px-3.5 py-3 bg-[var(--bg-surface)] border border-[var(--border-default)] text-[var(--text-secondary)] leading-relaxed whitespace-pre-wrap">
          {status.notes}
        </div>
      )}

      {status?.last_error && (
        <div className="mb-4 text-xs rounded px-3 py-2 bg-[var(--status-danger-soft)] text-[var(--status-danger)]">
          Last check error: {status.last_error}
        </div>
      )}

      {stageResult && (
        <div className="mb-4 text-xs rounded px-3 py-2 bg-[var(--status-success-soft)] text-[var(--status-success)]">
          {stageResult}
        </div>
      )}

      <div className="flex gap-3 items-center flex-wrap">
        {status?.available ? (
          <button
            onClick={stage}
            disabled={staging}
            className={'btn text-sm' + (isSecurity ? ' !bg-[var(--status-danger)] !border-[var(--status-danger)]' : '')}
          >
            {staging ? 'Downloading & staging…' : 'Download & stage update'}
          </button>
        ) : (
          <button disabled className="btn text-sm opacity-40 cursor-not-allowed">Up to date</button>
        )}
        <button onClick={refresh} className="btn-ghost text-sm">Refresh</button>
      </div>

      {status?.available && (
        <p className="text-xs text-[var(--text-faint)] mt-3">
          Staging downloads and verifies the release, then writes it to the inactive slot — the box keeps
          running the current version. Reboot separately whenever you're ready to switch to it.
        </p>
      )}

      {error && (
        <div className="mt-3 text-xs rounded px-3 py-2 bg-[var(--status-danger-soft)] text-[var(--status-danger)]">
          {error}
        </div>
      )}
    </Section>
  )
}

// --- About ---
interface HealthPayload {
  status?: string
}
function toHealthPayload(x: unknown): HealthPayload | null {
  if (!isRecord(x)) return null
  return { status: typeof x.status === 'string' ? x.status : undefined }
}
interface SysInfo {
  device_model?: string
  hostname?: string
  os_version?: string
  kernel?: string
  arch?: string
  cpu_model?: string
  cpu_cores?: number
  mem_used_mb?: number
  mem_total_mb?: number
  mem_percent?: number
  storage_total_mb?: number
  storage_used_mb?: number
  battery?: number
  charging?: boolean
  gpu_device?: string
  gpu_vendor?: string
  gpu_tier?: string
  gpu_encoder?: string
  gpu_codec?: string
  gpu_av1?: boolean
  gpu_pipewire?: boolean
  uptime?: string
}
function toSysInfo(x: unknown): SysInfo | null {
  if (!isRecord(x)) return null
  return {
    device_model: typeof x.device_model === 'string' ? x.device_model : undefined,
    hostname: typeof x.hostname === 'string' ? x.hostname : undefined,
    os_version: typeof x.os_version === 'string' ? x.os_version : undefined,
    kernel: typeof x.kernel === 'string' ? x.kernel : undefined,
    arch: typeof x.arch === 'string' ? x.arch : undefined,
    cpu_model: typeof x.cpu_model === 'string' ? x.cpu_model : undefined,
    cpu_cores: typeof x.cpu_cores === 'number' ? x.cpu_cores : undefined,
    mem_used_mb: typeof x.mem_used_mb === 'number' ? x.mem_used_mb : undefined,
    mem_total_mb: typeof x.mem_total_mb === 'number' ? x.mem_total_mb : undefined,
    mem_percent: typeof x.mem_percent === 'number' ? x.mem_percent : undefined,
    storage_total_mb: typeof x.storage_total_mb === 'number' ? x.storage_total_mb : undefined,
    storage_used_mb: typeof x.storage_used_mb === 'number' ? x.storage_used_mb : undefined,
    battery: typeof x.battery === 'number' ? x.battery : undefined,
    charging: typeof x.charging === 'boolean' ? x.charging : undefined,
    gpu_device: typeof x.gpu_device === 'string' ? x.gpu_device : undefined,
    gpu_vendor: typeof x.gpu_vendor === 'string' ? x.gpu_vendor : undefined,
    gpu_tier: typeof x.gpu_tier === 'string' ? x.gpu_tier : undefined,
    gpu_encoder: typeof x.gpu_encoder === 'string' ? x.gpu_encoder : undefined,
    gpu_codec: typeof x.gpu_codec === 'string' ? x.gpu_codec : undefined,
    gpu_av1: typeof x.gpu_av1 === 'boolean' ? x.gpu_av1 : undefined,
    gpu_pipewire: typeof x.gpu_pipewire === 'boolean' ? x.gpu_pipewire : undefined,
    uptime: typeof x.uptime === 'string' ? x.uptime : undefined,
  }
}
interface LegalDoc {
  title: string
  text: string
}

function AboutSettings() {
  const [health, setHealth] = useState<HealthPayload | null>(null)
  const [sys, setSys] = useState<SysInfo | null>(null)
  // Open-source licence notices + GPL/LGPL written offer, fetched on demand.
  const [legal, setLegal] = useState<LegalDoc | null>(null)      // { title, text } or null
  const [legalErr, setLegalErr] = useState<string | null>(null)
  const [legalLoading, setLegalLoading] = useState(false)

  useEffect(() => {
    fetch('/health').then(r => r.json()).then((raw: unknown) => setHealth(toHealthPayload(raw))).catch(() => {})
    fetch('/api/system/info').then(r => r.json()).then((raw: unknown) => setSys(toSysInfo(raw))).catch(() => {})
  }, [])

  const openLegal = (title: string, url: string) => {
    setLegalErr(null)
    setLegalLoading(true)
    setLegal({ title, text: '' })
    fetch(url)
      .then(r => r.ok ? r.text() : Promise.reject(new Error(`${r.status}`)))
      .then(text => setLegal({ title, text }))
      .catch(() => { setLegal(null); setLegalErr('Licence notices are not available on this system.') })
      .finally(() => setLegalLoading(false))
  }

  const fmtMB = (mb: number | undefined) => {
    if (!mb) return '—'
    return mb >= 1024 ? (mb / 1024).toFixed(1) + ' GB' : mb + ' MB'
  }

  const memPercent = sys?.mem_percent
  const battery = sys?.battery

  return (
    // About is the one pane with no <Section> masthead — deliberately, because
    // its own branding block IS the masthead. It still adopts the card language
    // of every other pane below it, where before it was bare bordered lists
    // under uppercase micro-headings that appeared nowhere else in Settings.
    <div className="animate-[fadeIn_0.18s_ease-out]">
      {/* Branding header. Left-aligned like every other pane's masthead: a
          centred logo block sat in the middle of a maximised window with the
          card stack starting hard left underneath it, so the page had two
          different centre lines. */}
      <div className="mb-6 pb-6 border-b border-[var(--border-subtle)] flex items-center gap-5">
        <img src="/vulos.png" alt="" aria-hidden="true" className="w-16 h-16 shrink-0 opacity-90" />
        <div className="min-w-0">
          <h2 className="text-[1.5rem] leading-[1.2] font-semibold tracking-[-0.015em]">Vulos OS</h2>
          <p className="text-sm text-[var(--text-tertiary)] mt-1">Open OS.</p>
        </div>
      </div>

      <div className="space-y-5">
        <Card icon="about" title="System">
          <InfoList>
            <InfoRow label="Device" value={sys?.device_model || sys?.hostname || '—'} />
            <InfoRow label="Hostname" value={sys?.hostname} />
            <InfoRow label="OS" value={sys?.os_version ? `Debian ${sys.os_version}` : 'Debian Linux'} />
            <InfoRow label="Kernel" value={sys?.kernel} />
            <InfoRow label="Architecture" value={sys?.arch} />
          </InfoList>
        </Card>

        <Card title="Hardware">
          <InfoList>
            <InfoRow label="Processor" value={sys?.cpu_model || `${sys?.cpu_cores || '—'} cores`} />
            <InfoRow label="CPU cores" value={sys?.cpu_cores} />
            <InfoRow label="Memory" value={sys ? `${fmtMB(sys.mem_used_mb)} used of ${fmtMB(sys.mem_total_mb)}` : '—'} />
            <InfoRow label="Storage" value={sys?.storage_total_mb ? `${fmtMB(sys.storage_used_mb)} used of ${fmtMB(sys.storage_total_mb)}` : '—'} />
            {battery !== undefined && battery >= 0 && (
              <InfoRow label="Battery" value={`${battery}%${sys?.charging ? ' (charging)' : ''}`} />
            )}
          </InfoList>
          {memPercent !== undefined && memPercent > 0 && (
            <Meter className="mt-4 max-w-[26rem]" label="RAM in use" pct={memPercent} />
          )}
        </Card>

        {sys && (
          <Card title="Graphics">
            <InfoList>
              <InfoRow label="GPU" value={sys.gpu_device || '—'} />
              <InfoRow label="Vendor" value={sys.gpu_vendor !== 'none' ? sys.gpu_vendor : 'None'} />
              <InfoRow
                label="Tier"
                value={sys.gpu_tier === 'nvenc' ? 'Tier 2 — NVENC' : sys.gpu_tier === 'vaapi' ? 'Tier 1 — VA-API' : 'Tier 0 — Software'}
                ok={sys.gpu_tier === 'nvenc' ? true : undefined}
              />
              <InfoRow label="Encoder" value={sys.gpu_encoder} />
              <InfoRow label="Codec" value={sys.gpu_codec || '—'} />
              {sys.gpu_av1 && <InfoRow label="AV1 encode" value="Supported" ok />}
              <InfoRow label="Capture" value={sys.gpu_pipewire ? 'PipeWire DMA-BUF' : 'X11 SHM'} />
            </InfoList>
          </Card>
        )}

        <Card title="Runtime">
          <InfoList>
            <InfoRow label="Uptime" value={sys?.uptime} />
            <InfoRow label="Server" value={health?.status === 'ok' ? 'Running' : 'Unreachable'} ok={health?.status === 'ok'} />
            <InfoRow label="Shell" value="React 19 + Tailwind 4 + Vite" />
            <InfoRow label="Backend" value="Go + Debian Linux" />
          </InfoList>
        </Card>

        <Card
          title="Open source"
          desc="Vulos includes open-source software. The notices reproduce each component's licence; the written offer covers how to obtain the corresponding source of the GPL/LGPL parts."
        >
          <Actions>
            <button onClick={() => openLegal('Open source licences', '/api/system/licenses')} className="btn-secondary text-sm">
              View licences
            </button>
            <button onClick={() => openLegal('Written offer for source code', '/api/system/written-offer')} className="btn-secondary text-sm">
              View written offer
            </button>
          </Actions>
          {legalErr && <div className="mt-4"><Banner tone="danger">{legalErr}</Banner></div>}
          {legal && (
            <div className="mt-4 rounded-xl border border-[var(--border-default)] overflow-hidden">
              <div className="flex items-center justify-between gap-3 px-4 py-2.5 bg-[var(--bg-elevated)]/40 border-b border-[var(--border-subtle)]">
                <span className="text-xs text-[var(--text-tertiary)] font-medium">{legal.title}</span>
                <button onClick={() => setLegal(null)} className="text-xs text-[var(--text-muted)] hover:text-[var(--text-secondary)]">Close ✕</button>
              </div>
              <pre className="max-h-96 overflow-auto px-4 py-3 text-[12px] leading-relaxed text-[var(--text-tertiary)] whitespace-pre-wrap break-words">
                {legalLoading ? 'Loading…' : legal.text}
              </pre>
            </div>
          )}
        </Card>

        <p className="text-[12px] text-[var(--text-faint)] pt-1">Powered by Debian Linux.</p>
      </div>
    </div>
  )
}

// InfoRow now comes from ./settings/ui.jsx (shared kit). Local copy removed.

// --- Shared UI components ---
// --- Notifications (WAVE-13) ---
// Wired to the framework-agnostic notificationStore. All prefs persist to
// localStorage via the store; the shell bell + toaster honour them live.
const NOTIF_SOURCE_LABELS: Record<string, string> = {
  mail: 'Mail', assistant: 'Assistant', system: 'System', sync: 'Sync', ai: 'AI',
}
function NotificationsSettings() {
  const prefs = useSyncExternalStore(subscribePrefs, getPrefs)
  // Always show the core producers, plus any other source seen in the feed.
  const sources = [...new Set(['mail', 'assistant', 'system', ...getSources()])]

  return (
    <Section
      icon="notifications"
      title="Notifications"
      desc="Notifications are computed on your box by the on-instance assistant — no new egress. Do Not Disturb silences pop-ups (they still collect quietly in the bell)."
      actions={prefs.muted ? <Pill tone="warning">Do Not Disturb on</Pill> : undefined}
    >
      <Card title="Delivery" bodyClassName="divide-y divide-[var(--border-subtle)]">
        <Toggle label="Do Not Disturb" checked={prefs.muted} onChange={setMuted} />
        <Toggle label="Notification sounds" checked={prefs.sound} onChange={setSound} />
      </Card>

      <Card
        title="This device"
        desc="Web Push lets your box notify this device even when Vulos is closed. Your box sends it directly and end-to-end encrypted — the push vendor routes it but can’t read it."
      >
        <WebPushToggle />
      </Card>

      <Card
        title="Sources"
        desc="Turn a source off to stop collecting its notifications entirely."
        bodyClassName="divide-y divide-[var(--border-subtle)]"
      >
        {sources.map(src => (
          <Toggle
            key={src}
            label={NOTIF_SOURCE_LABELS[src] || src.charAt(0).toUpperCase() + src.slice(1)}
            checked={prefs.sources?.[src] !== false}
            onChange={(v) => setSourceEnabled(src, v)}
          />
        ))}
      </Card>
    </Section>
  )
}

// Section — the shared panel header + body wrapper. A prominent title, an
// optional one-line description, and a hairline divider give every panel a
// consistent, premium masthead. `desc` is optional and back-compatible.
// Section, Field and Toggle now live in ./settings/ui.jsx (the shared kit) and
// are imported at the top of this file — the local copies were removed so every
// pane shares one design language.
