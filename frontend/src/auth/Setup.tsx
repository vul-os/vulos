import { useState, useEffect, useRef, type ReactNode } from 'react'
import QRCode from 'qrcode'
import FullscreenHint from './FullscreenHint'
import ThemeToggle from '../core/ThemeToggle'
import { useTheme } from '../core/ThemeProvider'
import { useI18n } from '../core/i18n'
import MasterKeyReveal from './MasterKeyReveal'
import { nativeBridge } from '../core/nativeBridge'
import './setup.css'

// Setup wizard config — every field the wizard steps read/write via
// `config`/`update`. This is purely OS-local wizard state (never itself
// network input), so it is typed directly rather than treated as a trust
// boundary; the actual trust-boundary data (server responses) is narrowed
// separately at each fetch call below.
export interface SetupConfig {
  deviceProfile: string
  locale: string
  timezone: string
  wifiSSID: string
  wifiPassword: string
  displayName: string
  username: string
  password: string
  pin: string
  // INIT-05 fields
  IS05_ulid: string
  IS05_hostname: string
  IS05_storageEnabled: boolean
  IS05_storageSkipped: boolean
  IS05_storageSizeGb: number
  IS05_storagePassword: string
  IS05_storagePassphrase: string
  IS05_storageMode: string
  IS05_storageMinioEndpoint: string
  IS05_storageMinioRegion: string
  IS05_storageMinioBucket: string
  IS05_storageMinioCredsRef: string
  IS05_sshPubkey: string
  IS05_sshFingerprint: string
  IS05_s3AccessKey: string
  IS05_s3SecretKey: string
  // BUNDLE-01 fields
  suiteEmail: boolean
  suiteWorkspace: boolean
}

// update() is the wizard's single generic setter — `<K extends keyof
// SetupConfig>` keeps the key and its value in lockstep so a step can't
// accidentally write a boolean into a string field (or vice versa) while
// still sharing one function across every step.
type SetupUpdate = <K extends keyof SetupConfig>(key: K, val: SetupConfig[K]) => void

// Shared prop shape for the (majority of) steps that read/write the full
// wizard config. A handful of steps take a narrower slice of these props —
// declared inline at each of those signatures instead of reusing this type.
interface StepProps {
  config: SetupConfig
  update: SetupUpdate
  onNext: () => void
  onPrev: () => void
}

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// useI18n() / useTheme() (../core/i18n.jsx, ../core/ThemeProvider.jsx — both
// outside src/auth and out of scope for this pass) create their React
// context via `createContext(null)` with no type parameter. That means the
// context's value type is the literal `null`, which — only once consumed
// from a checked .tsx file like this one — makes TS statically prove each
// hook's `if (!ctx) throw ...` guard always fires and infer the hook's
// return type as `never`. The real runtime shape is visible directly in
// each file's source; these wrappers restate the (narrow) slice of it Setup
// actually uses so the file can typecheck. This is the one unavoidable cast
// in this file — an upstream typing gap, not a trust-boundary shortcut —
// and it is reported here rather than hidden.
interface I18nShape {
  t: (key: string, vars?: Record<string, unknown>) => string
  setLocale: (code: string) => void
}
function useI18nTyped(): I18nShape {
  return useI18n() as unknown as I18nShape
}

interface ThemeShape {
  theme: string
  setTheme: (t: string) => void
  nightShiftMode: string
  setNightShiftMode: (m: string) => void
}
function useThemeTyped(): ThemeShape {
  return useTheme() as unknown as ThemeShape
}

// NATIVE-QR-01: a join code from GET /api/cluster/join-code looks like
// VULOS-XXXX-XXXX-XXXX (backend/services/joincode). The QR payload may carry
// the bare code or wrap it in a URL — either way we just need the token.
const JOIN_CODE_RE = /VULOS-[0-9A-Z]{4}-[0-9A-Z]{4}-[0-9A-Z]{4}/i

// Vulos is free, self-hosted software — there is no Vulos Cloud account, no
// sign-in/enrolment, and no paid add-on tier. The only account concept is the
// user's own local account on their own box.
// BUNDLE-01: the 'apps' step ("Your apps") reflects the default-everything
// (batteries-included, opt-out) bundling model. It is pre-checked for EVERYTHING
// — a Vulos account handle (enables Mail, still bring-your-own) + the full Diwan
// productivity suite — and lets a lean user opt out. Inserted after 'account' and
// before 'appearance' so it shows in every new-system flow.
// EXPORTED FOR TEST: Setup.test.ts asserts against THIS array. It used to keep
// a private copy of the list and assert against that, so the copy — which still
// named long-deleted 'cloudAccount'/'intent' steps — stayed green while the real
// flow changed underneath it. Never re-declare this list in a test.
// eslint-disable-next-line react-refresh/only-export-components -- the step lists and baseStepsFor are exported here ON PURPOSE so a test walks the SAME list the wizard walks; a copy in another module is exactly the drift the comment above warns about.
export const STEPS = ['welcome', 'IS09_chooser', 'device', 'language', 'timezone', 'network', 'account', 'pin', 'apps', 'appearance', 'identity', 'storage', 'ssh', 'recoverykit', 'ready']

// INIT-09: join-flow step list (used when the chooser picks "Join", or when
// setup mode === 'sync'). Shares indices 0–1 (welcome, IS09_chooser) with
// STEPS so flipping flowType at the chooser keeps `step` aligned; then the
// join-only steps, then the shared pin + ready. Lost in a merge — restored.
// eslint-disable-next-line react-refresh/only-export-components -- see STEPS above.
export const IS09_JOIN_STEPS = ['welcome', 'IS09_chooser', 'IS09_join_storage', 'IS09_syncing', 'pin', 'ready']

// INIT-09: the step list the wizard actually walks, chosen by flow type. The
// component below calls this exact function, so a test that calls it is
// exercising the real selection and not a re-implementation of it.
// eslint-disable-next-line react-refresh/only-export-components -- see STEPS above.
export function baseStepsFor(flowType: string): string[] {
  return flowType === 'join' ? IS09_JOIN_STEPS : STEPS
}

type DeviceAccent = 'blue' | 'violet' | 'amber' | 'emerald'

interface DeviceProfile {
  id: string
  label: string
  desc: string
  icon: ReactNode
  accent: DeviceAccent
}

const DEVICE_PROFILES: DeviceProfile[] = [
  {
    id: 'pc',
    label: 'PC / Tablet / Mobile',
    desc: 'Full desktop & responsive experience',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" className="w-8 h-8">
        <rect x="2" y="3" width="20" height="14" rx="2" />
        <path d="M8 21h8M12 17v4" />
      </svg>
    ),
    accent: 'blue',
  },
  {
    id: 'tv',
    label: 'TV',
    desc: '10-foot UI, remote navigation, media focus',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" className="w-8 h-8">
        <rect x="2" y="4" width="20" height="14" rx="2" />
        <path d="M8 20h8M7 4l5-3 5 3" />
      </svg>
    ),
    accent: 'violet',
  },
  {
    id: 'car',
    label: 'Car',
    desc: 'Large touch targets, voice-first, glanceable',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" className="w-8 h-8">
        <path d="M5 12l1.5-4.5A2 2 0 018.4 6h7.2a2 2 0 011.9 1.5L19 12" />
        <rect x="2" y="12" width="20" height="6" rx="2" />
        <circle cx="7" cy="18" r="2" />
        <circle cx="17" cy="18" r="2" />
      </svg>
    ),
    accent: 'amber',
  },
  {
    id: 'watch',
    label: 'Watch',
    desc: 'Companion device, AI chat, notifications',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" className="w-8 h-8">
        <rect x="7" y="5" width="10" height="14" rx="3" />
        <path d="M9 5V3M15 5V3M9 19v2M15 19v2" />
        <circle cx="12" cy="12" r="2" fill="currentColor" />
      </svg>
    ),
    accent: 'emerald',
  },
]

const PROFILE_ACCENT_CLASSES: Record<DeviceAccent, { selected: string; icon: string; check: string }> = {
  blue:    { selected: 'accent-bg-soft accent-border', icon: 'accent-text', check: 'accent-bg' },
  violet:  { selected: 'accent-bg-soft accent-border', icon: 'accent-text', check: 'accent-bg' },
  amber:   { selected: 'bg-warning-soft border-warning-soft', icon: 'wz-warn', check: 'bg-warning' },
  emerald: { selected: 'bg-success-soft border-success-soft', icon: 'wz-ok', check: 'bg-success' },
}

// Timezone data with approximate map positions (% from top-left)
const TIMEZONES = [
  { id: 'Pacific/Auckland', label: 'Auckland', offset: 'UTC+12', x: 92, y: 62 },
  { id: 'Asia/Tokyo', label: 'Tokyo', offset: 'UTC+9', x: 82, y: 35 },
  { id: 'Asia/Shanghai', label: 'Shanghai', offset: 'UTC+8', x: 77, y: 38 },
  { id: 'Asia/Kolkata', label: 'Mumbai', offset: 'UTC+5:30', x: 68, y: 44 },
  { id: 'Asia/Dubai', label: 'Dubai', offset: 'UTC+4', x: 62, y: 42 },
  { id: 'Europe/Moscow', label: 'Moscow', offset: 'UTC+3', x: 58, y: 26 },
  { id: 'Africa/Nairobi', label: 'Nairobi', offset: 'UTC+3', x: 57, y: 55 },
  { id: 'Africa/Johannesburg', label: 'Johannesburg', offset: 'UTC+2', x: 53, y: 68 },
  { id: 'Africa/Lagos', label: 'Lagos', offset: 'UTC+1', x: 44, y: 50 },
  { id: 'Africa/Cairo', label: 'Cairo', offset: 'UTC+2', x: 54, y: 38 },
  { id: 'Europe/Berlin', label: 'Berlin', offset: 'UTC+1', x: 48, y: 27 },
  { id: 'Europe/Paris', label: 'Paris', offset: 'UTC+1', x: 45, y: 29 },
  { id: 'Europe/London', label: 'London', offset: 'UTC+0', x: 43, y: 27 },
  { id: 'America/Sao_Paulo', label: 'São Paulo', offset: 'UTC-3', x: 30, y: 64 },
  { id: 'America/New_York', label: 'New York', offset: 'UTC-5', x: 22, y: 34 },
  { id: 'America/Chicago', label: 'Chicago', offset: 'UTC-6', x: 19, y: 33 },
  { id: 'America/Denver', label: 'Denver', offset: 'UTC-7', x: 16, y: 34 },
  { id: 'America/Los_Angeles', label: 'Los Angeles', offset: 'UTC-8', x: 12, y: 36 },
  { id: 'America/Anchorage', label: 'Anchorage', offset: 'UTC-9', x: 8, y: 22 },
]

const LANGUAGES = [
  { code: 'en', name: 'English', native: 'English', flag: '🇬🇧' },
  { code: 'af', name: 'Afrikaans', native: 'Afrikaans', flag: '🇿🇦' },
  { code: 'zu', name: 'Zulu', native: 'isiZulu', flag: '🇿🇦' },
  { code: 'xh', name: 'Xhosa', native: 'isiXhosa', flag: '🇿🇦' },
  { code: 'st', name: 'Sotho', native: 'Sesotho', flag: '🇿🇦' },
  { code: 'tn', name: 'Tswana', native: 'Setswana', flag: '🇿🇦' },
  { code: 'fr', name: 'French', native: 'Français', flag: '🇫🇷' },
  { code: 'pt', name: 'Portuguese', native: 'Português', flag: '🇵🇹' },
  { code: 'es', name: 'Spanish', native: 'Español', flag: '🇪🇸' },
  { code: 'de', name: 'German', native: 'Deutsch', flag: '🇩🇪' },
  { code: 'sw', name: 'Swahili', native: 'Kiswahili', flag: '🇰🇪' },
  { code: 'ar', name: 'Arabic', native: 'العربية', flag: '🇸🇦' },
  { code: 'zh', name: 'Chinese', native: '中文', flag: '🇨🇳' },
  { code: 'hi', name: 'Hindi', native: 'हिन्दी', flag: '🇮🇳' },
  { code: 'ja', name: 'Japanese', native: '日本語', flag: '🇯🇵' },
]

export default function Setup({ onComplete }: { onComplete: () => void }) {
  const [step, setStep] = useState(0)
  const [config, setConfig] = useState<SetupConfig>({
    deviceProfile: '',
    locale: 'en',
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || '',
    wifiSSID: '',
    wifiPassword: '',
    displayName: '',
    username: '',
    password: '',
    pin: '',
    // INIT-05 fields
    IS05_ulid: '',
    IS05_hostname: '',
    IS05_storageEnabled: false,
    IS05_storageSkipped: false,
    IS05_storageSizeGb: 20,
    IS05_storagePassword: '',
    IS05_storagePassphrase: '',
    // STORE-LOCAL-01 / D-STORE-LOCAL-DEFAULT: bundle storage mode. Defaults to
    // local-fs — this box's own disk, no third-party service. 'local-minio-sync'
    // opts into the local-MinIO-with-sync path; 'central-tigris' opts into
    // hosted storage. A fresh box must never be pushed onto hosted storage by
    // the wizard's default.
    IS05_storageMode: 'local-fs',
    IS05_storageMinioEndpoint: 'http://127.0.0.1:9000',
    IS05_storageMinioRegion: 'auto',
    IS05_storageMinioBucket: 'vulos-bundle',
    IS05_storageMinioCredsRef: '/var/lib/vulos/minio/.minio_secret',
    IS05_sshPubkey: '',
    IS05_sshFingerprint: '',
    IS05_s3AccessKey: '',
    IS05_s3SecretKey: '',
    // BUNDLE-01: default-everything (batteries-included, opt-out) suite selection.
    // Everything is pre-selected; a lean user can uncheck these to trim down.
    //   suiteEmail     — claim a Vulos account handle (also enables the Mail app; Mail itself
    //                    stays a bring-your-own connector, no mailbox is provisioned)
    //   suiteWorkspace — install the Diwan productivity app (Docs/Sheets/Slides/PDF/Whiteboards)
    suiteEmail: true,
    suiteWorkspace: true,
  })
  const [transitioning, setTransitioning] = useState(false)

  // INIT-09: flow type — 'new' (default) or 'join'
  const [IS09_flowType, IS09_setFlowType] = useState('new')
  // INIT-09: whether mode check is done
  const [IS09_modeChecked, IS09_setModeChecked] = useState(false)

  // INIT-09: On mount, check /api/setup/mode. If mode==="sync", jump straight to syncing.
  useEffect(() => {
    fetch('/api/setup/mode')
      .then(r => r.ok ? r.json() : null)
      .then((data: unknown) => {
        if (isRecord(data) && data.mode === 'normal') {
          // Already set up — shouldn't be here, but complete gracefully
          onComplete()
          return
        }
        if (isRecord(data) && data.mode === 'sync') {
          IS09_setFlowType('join')
          // Jump straight to the syncing step in the join flow
          const syncIdx = IS09_JOIN_STEPS.indexOf('IS09_syncing')
          setStep(syncIdx >= 0 ? syncIdx : 0)
        }
        IS09_setModeChecked(true)
      })
      .catch(() => {
        IS09_setModeChecked(true)
      })
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // INIT-09: choose active step list based on flow type
  const IS09_baseSteps = baseStepsFor(IS09_flowType)


  const current = IS09_baseSteps[step]
  const update: SetupUpdate = (key, val) => setConfig(c => ({ ...c, [key]: val }))

  const goTo = (idx: number) => {
    setTransitioning(true)
    setTimeout(() => { setStep(idx); setTransitioning(false) }, 200)
  }
  const next = () => goTo(Math.min(step + 1, IS09_baseSteps.length - 1))
  const prev = () => goTo(Math.max(step - 1, 0))

  // INIT-09: chooser handler — pick flow type and advance
  const IS09_handleChooseNew = () => {
    IS09_setFlowType('new')
    goTo(step + 1)
  }
  const IS09_handleChooseJoin = () => {
    IS09_setFlowType('join')
    // After choosing join, the next step in IS09_JOIN_STEPS after chooser is IS09_join_storage
    goTo(step + 1)
  }

  const finish = async () => {
    try {
      if (config.deviceProfile) {
        await fetch('/api/device-profile', {
          method: 'PUT', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ profile: config.deviceProfile }),
        }).catch(() => {})
      }
      if (config.timezone) {
        await fetch('/api/exec', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ command: `ln -sf /usr/share/zoneinfo/${config.timezone} /etc/localtime 2>/dev/null; echo done` }),
        }).catch(() => {})
      }
      if (config.wifiSSID) {
        await fetch('/api/wifi/connect', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ ssid: config.wifiSSID, password: config.wifiPassword }),
        }).catch(() => {})
      }
      await fetch('/api/exec', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ command: 'mkdir -p /var/lib/vulos && touch /var/lib/vulos/.setup-complete' }),
      }).catch(() => {})
    } catch { /* best-effort finish — proceed to complete regardless */ }
    onComplete()
  }

  // INIT-09: show loading until mode check resolves
  if (!IS09_modeChecked) {
    return (
      <div
        className="fixed inset-0 flex flex-col items-center justify-center gap-4"
        style={{ background: 'var(--bg-base)' }}
      >
        <span className="spinner w-7 h-7" aria-hidden="true" />
        <span className="text-sm" style={{ color: 'var(--text-muted)' }}>
          Checking system mode…
        </span>
      </div>
    )
  }

  // Steps that genuinely need horizontal room. Everything else stays on a
  // reading measure — a 40rem column on a 1440px screen is a deliberate choice,
  // not wasted space, because setup is a sequence of single decisions.
  const wide = current === 'language' || current === 'timezone' || current === 'appearance'

  return (
    <div className={`wz-root${wide ? ' wz-wide' : ''}`}>
      <div className="wz-bg" aria-hidden="true" />

      {/* Header: step progress, fullscreen affordance, theme toggle */}
      <header className="wz-header">
        <div className="wz-header-inner">
          <WizardProgress steps={IS09_baseSteps} step={step} onJump={goTo} />
          <FullscreenHint />
          <ThemeToggle />
        </div>
      </header>

      {/* The one scroll container. Steps render their own sticky action bar at
          the bottom of it, so the primary action can never leave the viewport. */}
      <main className="wz-body">
        <div className="wz-body-inner">
          <div key={current} className={transitioning ? 'opacity-0' : 'wz-step'}>
            {current === 'welcome' && <WelcomeStep onNext={next} />}
            {current === 'device' && <DeviceStep config={config} update={update} onNext={next} onPrev={prev} />}
            {current === 'IS09_chooser' && (
              <IS09_NewJoinChooserStep
                onChooseNew={IS09_handleChooseNew}
                onChooseJoin={IS09_handleChooseJoin}
                onPrev={prev}
              />
            )}
            {/* New-system flow steps */}
            {current === 'language' && <LanguageStep config={config} update={update} onNext={next} onPrev={prev} />}
            {current === 'timezone' && <TimezoneStep config={config} update={update} onNext={next} onPrev={prev} />}
            {current === 'network' && <NetworkStep config={config} update={update} onNext={next} onPrev={prev} />}
            {current === 'account' && (
              <AccountStep
                config={config}
                update={update}
                onNext={next}
                onPrev={prev}
              />
            )}
            {/* BUNDLE-01: default-everything (batteries-included, opt-out) app selection */}
            {current === 'apps' && (
              <AppsStep config={config} update={update} onNext={next} onPrev={prev} />
            )}
            {current === 'appearance' && <AppearanceStep onNext={next} onPrev={prev} />}
            {current === 'identity' && <IS05_IdentityStep config={config} update={update} onNext={next} onPrev={prev} />}
            {current === 'storage' && <IS05_StorageStep config={config} update={update} onNext={next} onPrev={prev} />}
            {current === 'ssh' && <IS05_SSHStep config={config} update={update} onNext={next} onPrev={prev} />}
            {current === 'recoverykit' && <IS05_RecoveryKitStep config={config} onNext={next} onPrev={prev} />}
            {/* Join-flow steps */}
            {current === 'IS09_join_storage' && (
              <IS09_JoinConnectStorageStep onNext={next} onPrev={prev} />
            )}
            {current === 'IS09_syncing' && (
              <IS09_SyncingStep onNext={next} onComplete={onComplete} />
            )}
            {/* Shared steps (pin + ready used by both flows) */}
            {current === 'pin' && <PinStep config={config} update={update} onNext={next} onPrev={prev} />}
            {current === 'ready' && <ReadyStep config={config} onFinish={finish} onPrev={prev} />}
          </div>
        </div>
      </main>
    </div>
  )
}

// ═══════════════════════════════════
// Wizard progress — a slim segmented bar that scales to any step count and
// stays legible on a phone (18 dots would overflow). Completed segments are
// clickable to jump back; the future is dimmed and inert.
// ═══════════════════════════════════
function WizardProgress({ steps, step, onJump }: { steps: string[]; step: number; onJump: (idx: number) => void }) {
  const total = steps.length
  return (
    <div className="wz-rail">
      <div
        className="wz-rail"
        role="progressbar"
        aria-valuenow={step + 1}
        aria-valuemin={1}
        aria-valuemax={total}
        aria-label={`Setup step ${step + 1} of ${total}`}
      >
        {steps.map((_, i) => {
          const done = i < step
          const active = i === step
          // Only completed segments are buttons. Rendering the future as
          // fifteen disabled buttons put fifteen inert stops in the tab order
          // of the first screen anyone meets — and a wizard is exactly the
          // place someone is driving with a TV remote or a keyboard.
          if (!done) {
            return (
              <span
                key={i}
                className={`wz-seg${active ? ' wz-seg--now' : ''}`}
                aria-current={active ? 'step' : undefined}
              />
            )
          }
          return (
            <button
              key={i}
              type="button"
              onClick={() => onJump(i)}
              aria-label={`Go back to step ${i + 1} of ${total}`}
              className="wz-seg wz-seg--done"
            />
          )
        })}
      </div>
      {/* Was `01 / 15` in --text-ghost: 2.11:1 dark, 2.18:1 light. It sits in
          the header, so that single pair was a WCAG failure on all 15 steps in
          both themes — the most-repeated contrast defect in the wizard. */}
      <span className="wz-count">
        Step <b>{step + 1}</b> of {total}
      </span>
    </div>
  )
}

// ═══════════════════════════════════
// Welcome
// ═══════════════════════════════════
function WelcomeStep({ onNext }: { onNext: () => void }) {
  const { t } = useI18nTyped()
  return (
    <div className="text-center animate-[fadeIn_0.4s_ease-out]">
      <div className="mb-8 flex flex-col items-center">
        <div className="relative mb-6">
          <div
            className="absolute inset-0 -m-6 rounded-full blur-2xl opacity-60"
            style={{ background: 'radial-gradient(circle, color-mix(in srgb, var(--accent) 45%, transparent), transparent 70%)' }}
            aria-hidden="true"
          />
          <img src="/icon-128.png" alt="Vulos OS" className="relative w-24 h-24 drop-shadow-xl" />
        </div>
        <div className="text-6xl font-extralight tracking-[0.22em] mb-4" style={{ color: 'var(--text-primary)' }}>vulos</div>
        <div className="h-px w-20 mx-auto mb-4" style={{ background: 'linear-gradient(to right, transparent, var(--accent), transparent)' }} />
        <p className="text-lg sm:text-xl font-light" style={{ color: 'var(--text-tertiary)' }}>{t('setup.welcome.tagline')}</p>
      </div>
      <button
        onClick={onNext}
        className="btn-primary px-12 py-3.5 text-base mt-4 elevate-md hover:elevate-lg transition-shadow"
      >
        {t('setup.welcome.cta')}
      </button>
    </div>
  )
}

// ═══════════════════════════════════
// Device Profile
// ═══════════════════════════════════
function DeviceStep({ config, update, onNext, onPrev }: StepProps) {
  const [loading, setLoading] = useState(true)
  const [detected, setDetected] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    fetch('/api/device-profile')
      .then(r => r.json())
      .then((data: unknown) => {
        if (cancelled) return
        const d = isRecord(data) ? data : {}
        const suggested = (typeof d.suggested === 'string' && d.suggested)
          || (typeof d.profile === 'string' && d.profile)
          || null
        setDetected(suggested)
        if (suggested && !config.deviceProfile) {
          update('deviceProfile', suggested)
        }
      })
      .catch(() => {})
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const selected = config.deviceProfile || detected || ''

  return (
    <div>
      <StepHeader
        title="What kind of device is this?"
        subtitle={loading ? 'Detecting your device…' : detected ? `Auto-detected: ${DEVICE_PROFILES.find(p => p.id === detected)?.label || detected}` : 'Choose how Vulos OS should behave on this device'}
      />

      <div className="grid grid-cols-2 gap-3">
        {DEVICE_PROFILES.map(profile => {
          const isSelected = selected === profile.id
          const ac = PROFILE_ACCENT_CLASSES[profile.accent]
          return (
            <button
              key={profile.id}
              onClick={() => update('deviceProfile', profile.id)}
              style={isSelected ? { color: 'var(--text-primary)' } : undefined}
              className={`relative flex flex-col items-center gap-2 px-4 py-5 rounded-2xl text-center transition-all border-2
                ${isSelected
                  ? `${ac.selected} elevate-lg`
                  : 'wz-surface wz-hairline wz-body hover:wz-edge hover:wz-strong'}`}
            >
              <span className={isSelected ? ac.icon : 'wz-dim'}>
                {profile.icon}
              </span>
              <div>
                <div className="text-sm font-medium leading-snug">{profile.label}</div>
                <div className="text-[12px] mt-0.5 leading-snug" style={{ color: 'var(--text-muted)' }}>{profile.desc}</div>
              </div>
              {detected === profile.id && !isSelected && (
                <div className="absolute top-2 right-2 text-[12px] font-semibold tracking-wider wz-dim uppercase">
                  Detected
                </div>
              )}
              {detected === profile.id && isSelected && (
                <div className="absolute top-2 left-2 text-[12px] font-semibold tracking-wider wz-body uppercase">
                  Detected
                </div>
              )}
              {isSelected && (
                <div className={`absolute top-2 right-2 w-5 h-5 rounded-full ${ac.check} flex items-center justify-center`}>
                  <svg viewBox="0 0 16 16" className="w-3 h-3 text-white">
                    <path d="M3.5 8l3 3 6-6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
                  </svg>
                </div>
              )}
            </button>
          )
        })}
      </div>

      <NavBar onPrev={onPrev} onNext={onNext} nextLabel={selected ? 'Continue' : 'Skip'} />
    </div>
  )
}

// ═══════════════════════════════════
// INIT-09: New vs Join chooser
// ═══════════════════════════════════
function IS09_NewJoinChooserStep({ onChooseNew, onChooseJoin, onPrev }: { onChooseNew: () => void; onChooseJoin: () => void; onPrev: () => void }) {
  return (
    <div className="text-center">
      <StepHeader
        title="How would you like to set up?"
        subtitle="Start fresh or join an existing Vulos OS installation"
      />
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mt-2 mb-6">
        {/* New system card */}
        <button
          onClick={onChooseNew}
          className="group flex flex-col items-center gap-3 px-6 py-7 rounded-2xl border-2 wz-hairline wz-surface text-left hover-accent-border hover-accent-bg-soft transition-all"
        >
          <div className="w-14 h-14 rounded-2xl accent-bg-soft accent-bg-hover flex items-center justify-center transition-colors">
            <svg viewBox="0 0 24 24" className="w-7 h-7 accent-text" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
              <circle cx="12" cy="12" r="9" />
              <path d="M12 8v8M8 12h8" />
            </svg>
          </div>
          <div>
            <div className="text-base font-semibold wz-strong mb-1">New System</div>
            <div className="text-sm wz-dim">Set up this device from scratch with a new identity and storage.</div>
          </div>
        </button>
        {/* Join existing card */}
        <button
          onClick={onChooseJoin}
          className="group flex flex-col items-center gap-3 px-6 py-7 rounded-2xl border-2 wz-hairline wz-surface text-left hover-accent-border hover-accent-bg-soft transition-all"
        >
          <div className="w-14 h-14 rounded-2xl accent-bg-soft flex items-center justify-center transition-colors">
            <svg viewBox="0 0 24 24" className="w-7 h-7 accent-text" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
              <path d="M17 21v-2a4 4 0 00-4-4H5a4 4 0 00-4 4v2" />
              <circle cx="9" cy="7" r="4" />
              <path d="M23 21v-2a4 4 0 00-3-3.87M16 3.13a4 4 0 010 7.75" />
            </svg>
          </div>
          <div>
            <div className="text-base font-semibold wz-strong mb-1">Join Existing</div>
            <div className="text-sm wz-dim">Connect to an existing Vulos OS cluster by providing storage credentials.</div>
          </div>
        </button>
      </div>
      <div className="flex justify-start pt-4 border-t wz-hairline">
        <button onClick={onPrev} className="text-sm wz-dim hover:wz-body transition-colors">
          ← Back
        </button>
      </div>
    </div>
  )
}

// ═══════════════════════════════════
// INIT-09: Join — Connect Storage
// ═══════════════════════════════════
function IS09_JoinConnectStorageStep({ onNext, onPrev }: { onNext: () => void; onPrev: () => void }) {
  const [IS09_s3Bucket, IS09_setS3Bucket] = useState('')
  const [IS09_s3Region, IS09_setS3Region] = useState('')
  const [IS09_s3AccessKey, IS09_setS3AccessKey] = useState('')
  const [IS09_s3SecretKey, IS09_setS3SecretKey] = useState('')
  const [IS09_passphrase, IS09_setPassphrase] = useState('')
  const [IS09_joining, IS09_setJoining] = useState(false)
  const [IS09_error, IS09_setError] = useState('')
  const [IS09_joinCode, IS09_setJoinCode] = useState('')
  const [IS09_redeeming, IS09_setRedeeming] = useState(false)
  const [IS09_redeemMsg, IS09_setRedeemMsg] = useState('')

  // NATIVE-QR-01: redeem a VULOS-XXXX-XXXX-XXXX short code → autofills the S3
  // fields below. Shared by the manual "Redeem" button and the QR scan path.
  const IS09_redeemJoinCode = async (codeArg?: string) => {
    const code = (codeArg ?? IS09_joinCode).trim().toUpperCase()
    if (!code) return
    IS09_setRedeeming(true)
    IS09_setRedeemMsg('')
    try {
      const res = await fetch('/api/setup/join-code', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ short_code: code }),
      })
      // Untrusted network JSON — narrowed field-by-field, never cast.
      const raw: unknown = await res.json().catch(() => ({}))
      const data = isRecord(raw) ? raw : {}
      if (!res.ok) {
        IS09_setRedeemMsg((typeof data.error === 'string' && data.error) || `Could not redeem code (${res.status}).`)
        return
      }
      IS09_setS3Bucket(typeof data.bucket === 'string' ? data.bucket : '')
      IS09_setS3Region(typeof data.region === 'string' ? data.region : '')
      IS09_setS3AccessKey(typeof data.access_key === 'string' ? data.access_key : '')
      IS09_setS3SecretKey(typeof data.secret_key === 'string' ? data.secret_key : '')
      IS09_setRedeemMsg('Storage details filled in — add your encryption passphrase below.')
    } catch {
      IS09_setRedeemMsg('Could not reach the server to redeem the code.')
    } finally {
      IS09_setRedeeming(false)
    }
  }

  // NATIVE-QR-01: scan the pairing QR (Android bridge only — no-op/invisible
  // elsewhere). Cancel/error falls back to manual code entry silently.
  const IS09_handleScanQR = async () => {
    try {
      const { text } = await nativeBridge.camera.scanQR('Scan the pairing code')
      const match = text && text.match(JOIN_CODE_RE)
      if (!match) return
      const code = match[0].toUpperCase()
      IS09_setJoinCode(code)
      IS09_redeemJoinCode(code)
    } catch {
      // Cancelled or native error — user can still type the code.
    }
  }

  const IS09_handleJoin = async () => {
    if (!IS09_s3Bucket || !IS09_s3AccessKey || !IS09_s3SecretKey || !IS09_passphrase) {
      IS09_setError('Please fill in all required fields.')
      return
    }
    IS09_setJoining(true)
    IS09_setError('')
    try {
      const res = await fetch('/api/setup/join', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          bucket: IS09_s3Bucket,
          region: IS09_s3Region || 'us-east-1',
          access: IS09_s3AccessKey,
          secret: IS09_s3SecretKey,
          passphrase: IS09_passphrase,
        }),
      })
      if (res.status === 404) {
        IS09_setError('Backend not yet available — join endpoint is not ready. Please retry later.')
        IS09_setJoining(false)
        return
      }
      if (!res.ok) {
        const raw: unknown = await res.json().catch(() => ({}))
        const data = isRecord(raw) ? raw : {}
        IS09_setError((typeof data.error === 'string' && data.error) || `Unexpected error (${res.status}). Please retry.`)
        IS09_setJoining(false)
        return
      }
      // Success — proceed to syncing screen
      onNext()
    } catch {
      IS09_setError('Could not reach the server. Check your network and retry.')
      IS09_setJoining(false)
    }
  }

  return (
    <div>
      <StepHeader
        title="Connect to existing storage"
        subtitle="Provide your S3-compatible storage credentials and encryption passphrase"
      />
      <div className="space-y-3">
        <div>
          <label className="block text-xs wz-dim mb-1.5">Join code (optional quick-fill)</label>
          <div className="flex items-center gap-2">
            <input
              value={IS09_joinCode}
              onChange={e => IS09_setJoinCode(e.target.value.toUpperCase())}
              placeholder="VULOS-XXXX-XXXX-XXXX"
              className="input text-sm py-2.5 font-mono flex-1"
            />
            <button type="button" onClick={() => IS09_redeemJoinCode()} disabled={IS09_redeeming || !IS09_joinCode.trim()} className="btn-secondary text-sm py-2.5 px-3 whitespace-nowrap disabled:opacity-50">
              {IS09_redeeming ? 'Redeeming…' : 'Redeem'}
            </button>
            {nativeBridge.camera.available && (
              <button type="button" onClick={IS09_handleScanQR} className="btn-secondary text-sm py-2.5 px-3 whitespace-nowrap">
                Scan QR
              </button>
            )}
          </div>
          {IS09_redeemMsg && <p className="text-xs wz-dim mt-1.5">{IS09_redeemMsg}</p>}
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-xs wz-dim mb-1.5">S3 Bucket <span className="wz-danger">*</span></label>
            <input
              value={IS09_s3Bucket}
              onChange={e => IS09_setS3Bucket(e.target.value.trim())}
              placeholder="my-vulos-backup"
              autoFocus
              className="input text-sm py-2.5"
            />
          </div>
          <div>
            <label className="block text-xs wz-dim mb-1.5">Region</label>
            <input
              value={IS09_s3Region}
              onChange={e => IS09_setS3Region(e.target.value.trim())}
              placeholder="us-east-1"
              className="input text-sm py-2.5"
            />
          </div>
        </div>
        <div>
          <label className="block text-xs wz-dim mb-1.5">Access Key <span className="wz-danger">*</span></label>
          <input
            value={IS09_s3AccessKey}
            onChange={e => IS09_setS3AccessKey(e.target.value.trim())}
            placeholder="AKIAIOSFODNN7EXAMPLE"
            className="input text-sm py-2.5 font-mono"
          />
        </div>
        <div>
          <label className="block text-xs wz-dim mb-1.5">Secret Key <span className="wz-danger">*</span></label>
          <input
            type="password"
            value={IS09_s3SecretKey}
            onChange={e => IS09_setS3SecretKey(e.target.value)}
            placeholder="Secret access key"
            className="input text-sm py-2.5 font-mono"
          />
        </div>
        <div>
          <label className="block text-xs wz-dim mb-1.5">Encryption Passphrase <span className="wz-danger">*</span></label>
          <input
            type="password"
            value={IS09_passphrase}
            onChange={e => IS09_setPassphrase(e.target.value)}
            placeholder="Decryption passphrase for existing backup"
            className="input text-sm py-2.5"
          />
        </div>
        {IS09_error && (
          <div className="flex flex-col gap-2 bg-danger-soft border border-danger-soft rounded-xl px-4 py-3">
            <p className="text-sm wz-danger">{IS09_error}</p>
            {IS09_error.includes('not yet available') && (
              <button
                onClick={IS09_handleJoin}
                className="self-start text-xs wz-danger underline underline-offset-2 hover:opacity-80"
              >
                Retry
              </button>
            )}
          </div>
        )}
      </div>
      <div className="flex items-center justify-between mt-6 pt-4 border-t wz-hairline">
        <button onClick={onPrev} className="text-sm wz-dim hover:wz-body transition-colors">
          ← Back
        </button>
        <button
          onClick={IS09_handleJoin}
          disabled={IS09_joining}
          className="btn-primary flex items-center gap-2"
        >
          {IS09_joining ? (
            <>
              <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />
              Connecting...
            </>
          ) : (
            'Connect & Sync →'
          )}
        </button>
      </div>
    </div>
  )
}

// ═══════════════════════════════════
// INIT-09: Syncing screen
// ═══════════════════════════════════
const IS09_SYNC_PHASES = [
  { key: 'init', label: 'Initialising sync' },
  { key: 'keys', label: 'Fetching encryption keys' },
  { key: 'identity', label: 'Restoring identity' },
  { key: 'storage', label: 'Syncing storage' },
  { key: 'apps', label: 'Restoring applications' },
  { key: 'done', label: 'Finalising' },
]

function IS09_SyncingStep({ onNext, onComplete }: { onNext: () => void; onComplete: () => void }) {
  const [IS09_phase, IS09_setPhase] = useState('init')
  const [IS09_phaseIdx, IS09_setPhaseIdx] = useState(0)
  const [IS09_error, IS09_setError] = useState('')
  const [IS09_done, IS09_setDone] = useState(false)
  const [IS09_bgMode, IS09_setBgMode] = useState(false)
  const IS09_pollRef = useRef<ReturnType<typeof setInterval> | undefined>(undefined)

  const IS09_poll = async () => {
    try {
      // Try /api/setup/join/status first, fall back to /api/setup/mode.
      // Untrusted network JSON either way — narrowed to a record (and each
      // field typeof-checked below), never cast to a nicer shape.
      let data: Record<string, unknown> | null = null
      const statusRes = await fetch('/api/setup/join/status')
      if (statusRes.ok) {
        const raw: unknown = await statusRes.json()
        data = isRecord(raw) ? raw : null
      } else if (statusRes.status === 404) {
        const modeRes = await fetch('/api/setup/mode')
        if (modeRes.ok) {
          const modeRaw: unknown = await modeRes.json()
          const modeData = isRecord(modeRaw) ? modeRaw : {}
          const syncState = isRecord(modeData.sync_state) ? modeData.sync_state : {}
          data = {
            phase: (typeof syncState.phase === 'string' && syncState.phase) || 'init',
            done: modeData.mode !== 'sync',
          }
        }
      }
      if (!data) return

      const currentPhase = (typeof data.phase === 'string' && data.phase) || 'init'
      const phaseIdx = IS09_SYNC_PHASES.findIndex(p => p.key === currentPhase)
      IS09_setPhase(currentPhase)
      IS09_setPhaseIdx(phaseIdx >= 0 ? phaseIdx : 0)

      if (data.done || data.phase === 'done' || data.mode === 'normal') {
        IS09_setDone(true)
        clearInterval(IS09_pollRef.current)
        if (IS09_bgMode) {
          onComplete()
        }
      }
    } catch {
      // Network error — keep polling silently
    }
  }

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    IS09_poll()
    IS09_pollRef.current = setInterval(IS09_poll, 3000)
    return () => clearInterval(IS09_pollRef.current)
  }, [IS09_bgMode]) // eslint-disable-line react-hooks/exhaustive-deps

  const IS09_progress = IS09_SYNC_PHASES.length > 0
    ? ((IS09_phaseIdx + 1) / IS09_SYNC_PHASES.length) * 100
    : 0

  if (IS09_bgMode) {
    return (
      <div className="text-center">
        <div className="text-4xl mb-4">🔄</div>
        <StepHeader title="Syncing in the background" subtitle="You can use Vulos OS while sync continues." />
        <p className="text-sm wz-dim">Setup will complete automatically when sync finishes.</p>
      </div>
    )
  }

  return (
    <div className="text-center">
      <div className="mb-6 flex flex-col items-center">
        <div className="w-16 h-16 rounded-2xl accent-bg-soft flex items-center justify-center mb-4">
          {IS09_done ? (
            <svg viewBox="0 0 24 24" className="w-8 h-8 wz-ok" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M20 6L9 17l-5-5" />
            </svg>
          ) : (
            <svg viewBox="0 0 24 24" className="w-8 h-8 accent-text animate-spin" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" style={{ animationDuration: '2s' }}>
              <path d="M21 12a9 9 0 11-6.219-8.56" />
            </svg>
          )}
        </div>
        <h2 className="text-2xl font-light wz-strong">
          {IS09_done ? 'Sync complete' : 'Syncing your data'}
        </h2>
        <p className="text-sm wz-dim mt-1">
          {IS09_done
            ? 'Everything has been restored. Continue to set your PIN.'
            : 'Restoring your identity and data from storage...'}
        </p>
      </div>

      {/* Phase progress bar */}
      <div className="mb-4">
        <div className="flex justify-between text-xs wz-dim mb-1.5">
          <span>{IS09_SYNC_PHASES[IS09_phaseIdx]?.label || 'Initialising'}</span>
          <span>{Math.round(IS09_progress)}%</span>
        </div>
        <div className="h-1.5 w-full wz-surface-2 rounded-full overflow-hidden">
          <div
            className="h-full rounded-full transition-all duration-700"
            style={{
              width: `${IS09_progress}%`,
              background: 'linear-gradient(to right, color-mix(in srgb, var(--accent) 65%, #7c3aed), var(--accent))',
            }}
          />
        </div>
      </div>

      {/* Phase steps */}
      <div className="space-y-1.5 mb-6 text-left">
        {IS09_SYNC_PHASES.map((phase, i) => {
          const isDone = i < IS09_phaseIdx || IS09_done
          const isCurrent = i === IS09_phaseIdx && !IS09_done
          return (
            <div key={phase.key} className={`flex items-center gap-2.5 px-3 py-2 rounded-lg transition-colors
              ${isCurrent ? 'accent-bg-soft border accent-border-soft' : ''}
            `}>
              <span className={`flex-shrink-0 w-4 h-4 rounded-full flex items-center justify-center text-[10px]
                ${isDone ? 'bg-success-soft wz-ok' : isCurrent ? 'accent-bg-soft accent-text' : 'wz-surface-2 wz-dim'}`}>
                {isDone ? '✓' : isCurrent ? '·' : '○'}
              </span>
              <span className={`text-sm ${isDone ? 'wz-body' : isCurrent ? 'wz-strong' : 'wz-dim'}`}>
                {phase.label}
              </span>
              {isCurrent && (
                <span className="ml-auto flex gap-0.5">
                  {[0, 1, 2].map(d => (
                    <span key={d} className="w-1 h-1 accent-bg rounded-full animate-bounce" style={{ animationDelay: `${d * 0.15}s` }} />
                  ))}
                </span>
              )}
            </div>
          )
        })}
      </div>

      {IS09_error && (
        <div className="mb-4 bg-danger-soft border border-danger-soft rounded-xl px-4 py-3">
          <p className="text-sm wz-danger">{IS09_error}</p>
        </div>
      )}

      <div className="flex flex-col gap-2">
        {IS09_done ? (
          <button onClick={onNext} className="btn-primary px-10 py-3 text-base">
            Continue →
          </button>
        ) : (
          <button
            onClick={() => { IS09_setBgMode(true); onComplete() }}
            className="text-sm wz-dim hover:wz-body transition-colors underline underline-offset-2"
          >
            Continue in Background
          </button>
        )}
      </div>
    </div>
  )
}

// ═══════════════════════════════════
// Language
// ═══════════════════════════════════
function LanguageStep({ config, update, onNext, onPrev }: StepProps) {
  const { t, setLocale } = useI18nTyped()
  return (
    <div>
      <StepHeader title={t('setup.language.title')} subtitle={t('setup.language.subtitle')} />
      <div className="grid grid-cols-2 sm:grid-cols-3 gap-2 max-h-[50vh] overflow-y-auto pr-1">
        {LANGUAGES.map(lang => (
          <button
            key={lang.code}
            onClick={() => { update('locale', lang.code); setLocale(lang.code) }}
            className={`flex items-center gap-3 px-4 py-3 rounded-xl text-left transition-all
              ${config.locale === lang.code
                ? 'accent-bg-soft accent-border wz-strong border'
                : 'wz-surface border wz-hairline wz-body hover:wz-edge hover:wz-strong'}`}
          >
            <span className="text-xl">{lang.flag}</span>
            <div>
              <div className="text-sm font-medium">{lang.native}</div>
              <div className="text-xs wz-dim">{lang.name}</div>
            </div>
          </button>
        ))}
      </div>
      <NavBar onPrev={onPrev} onNext={onNext} />
    </div>
  )
}

// ═══════════════════════════════════
// Timezone (interactive map)
// ═══════════════════════════════════
function TimezoneStep({ config, update, onNext, onPrev }: StepProps) {
  const { t } = useI18nTyped()
  const selected = TIMEZONES.find(tz => tz.id === config.timezone)

  return (
    <div>
      <StepHeader title={t('setup.timezone.title')} subtitle={selected ? `${selected.label} (${selected.offset})` : t('setup.timezone.subtitle_none')} />

      {/* World map */}
      <div className="relative w-full aspect-[2/1] wz-surface rounded-2xl border wz-hairline overflow-hidden mb-4">
        {/* Simplified world outline via CSS gradients */}
        <div className="absolute inset-0 opacity-10">
          <svg viewBox="0 0 100 50" className="w-full h-full" preserveAspectRatio="none">
            {/* Simplified continents as rough shapes */}
            <ellipse cx="20" cy="35" rx="12" ry="15" fill="#3b82f6" opacity="0.3" />
            <ellipse cx="47" cy="28" rx="8" ry="10" fill="#3b82f6" opacity="0.3" />
            <ellipse cx="52" cy="55" rx="6" ry="12" fill="#3b82f6" opacity="0.3" />
            <ellipse cx="70" cy="40" rx="14" ry="12" fill="#3b82f6" opacity="0.3" />
            <ellipse cx="80" cy="35" rx="8" ry="10" fill="#3b82f6" opacity="0.3" />
            <ellipse cx="90" cy="60" rx="5" ry="5" fill="#3b82f6" opacity="0.3" />
          </svg>
        </div>

        {/* Timezone grid lines */}
        {Array.from({ length: 24 }, (_, i) => (
          <div key={i} className="absolute top-0 bottom-0 w-px wz-surface-2" style={{ left: `${(i / 24) * 100}%` }} />
        ))}

        {/* City markers */}
        {TIMEZONES.map(tz => (
          <button
            key={tz.id}
            onClick={() => update('timezone', tz.id)}
            className="absolute group"
            style={{ left: `${tz.x}%`, top: `${tz.y}%`, transform: 'translate(-50%, -50%)' }}
          >
            {/* Dot */}
            <div className={`w-3 h-3 rounded-full transition-all border-2
              ${config.timezone === tz.id
                ? 'accent-bg accent-border scale-150 shadow-lg'
                : 'wz-surface-2 wz-edge hover-accent-bg hover-accent-border group-hover:scale-125'}`}
            />
            {/* Label (shows on hover or when selected) */}
            <div className={`absolute left-1/2 -translate-x-1/2 mt-1 whitespace-nowrap text-[12px] font-medium transition-opacity
              ${config.timezone === tz.id ? 'opacity-100 accent-text' : 'opacity-0 group-hover:opacity-100 wz-body'}`}>
              {tz.label}
            </div>
            {/* Pulse ring when selected */}
            {config.timezone === tz.id && (
              <div className="absolute inset-0 w-3 h-3 rounded-full border accent-border animate-ping opacity-30" />
            )}
          </button>
        ))}
      </div>

      {/* List fallback (scrollable) */}
      <div className="flex gap-2 overflow-x-auto pb-2 -mx-2 px-2">
        {TIMEZONES.map(tz => (
          <button
            key={tz.id}
            onClick={() => update('timezone', tz.id)}
            className={`shrink-0 px-3 py-1.5 rounded-lg text-xs transition-all whitespace-nowrap
              ${config.timezone === tz.id
                ? 'accent-bg-soft accent-text border accent-border'
                : 'wz-surface wz-dim border wz-hairline hover:wz-body'}`}
          >
            {tz.label}
          </button>
        ))}
      </div>

      <NavBar onPrev={onPrev} onNext={onNext} />
    </div>
  )
}

// ═══════════════════════════════════
// Network
// ═══════════════════════════════════
interface WifiNetwork {
  bssid?: string
  ssid?: string
  signal?: number
  band?: string
  security?: string
}

function toWifiNetwork(x: unknown): WifiNetwork {
  if (!isRecord(x)) return {}
  return {
    bssid: typeof x.bssid === 'string' ? x.bssid : undefined,
    ssid: typeof x.ssid === 'string' ? x.ssid : undefined,
    signal: typeof x.signal === 'number' ? x.signal : undefined,
    band: typeof x.band === 'string' ? x.band : undefined,
    security: typeof x.security === 'string' ? x.security : undefined,
  }
}

function NetworkStep({ config, update, onNext, onPrev }: StepProps) {
  const { t } = useI18nTyped()
  const [networks, setNetworks] = useState<WifiNetwork[] | null>(null)
  const [scanning, setScanning] = useState(false)
  const [showPassword, setShowPassword] = useState(false)

  const scan = async () => {
    setScanning(true)
    try {
      const res = await fetch('/api/wifi/scan')
      // Untrusted network JSON — each entry narrowed via toWifiNetwork rather
      // than cast to WifiNetwork[].
      const data: unknown = await res.json()
      setNetworks(Array.isArray(data) ? data.map(toWifiNetwork) : [])
    } catch { setNetworks([]) }
    setScanning(false)
  }

  const signalIcon = (dbm: number | undefined) => {
    if (typeof dbm === 'number' && dbm > -50) return '████'
    if (typeof dbm === 'number' && dbm > -60) return '███░'
    if (typeof dbm === 'number' && dbm > -70) return '██░░'
    return '█░░░'
  }

  return (
    <div>
      <StepHeader title={t('setup.network.title')} subtitle={t('setup.network.subtitle')} />

      <button
        onClick={scan}
        disabled={scanning}
        className={`w-full py-3 rounded-xl text-sm font-medium transition-all mb-4
          ${scanning
            ? 'wz-surface-2 wz-dim'
            : 'wz-surface border wz-edge wz-body hover-accent-border hover-accent-text'}`}
      >
        {scanning ? (
          <span className="flex items-center justify-center gap-2">
            <span className="w-4 h-4 border-2 wz-edge rounded-full animate-spin" style={{ borderTopColor: 'var(--accent)' }} />
            {t('setup.network.scanning')}
          </span>
        ) : networks ? t('setup.network.scan_again') : t('setup.network.scan')}
      </button>

      {/* Network list */}
      {networks && (
        <div className="max-h-[35vh] overflow-y-auto rounded-xl border wz-hairline mb-4">
          {networks.length === 0 && (
            <div className="p-4 text-sm wz-dim text-center">{t('setup.network.no_networks')}</div>
          )}
          {networks.map((n, i) => (
            <button
              key={n.bssid || n.ssid || i}
              onClick={() => { update('wifiSSID', n.ssid || ''); setShowPassword(true) }}
              className={`w-full flex items-center gap-3 px-4 py-3 text-left border-b wz-hairline transition-colors
                ${config.wifiSSID === n.ssid
                  ? 'accent-bg-soft wz-strong'
                  : 'wz-body hover:wz-surface-2'}`}
            >
              <span className="text-[10px] font-mono wz-dim w-10">{signalIcon(n.signal)}</span>
              <div className="flex-1 min-w-0">
                <div className="text-sm truncate">{n.ssid || '(hidden)'}</div>
                <div className="text-[12px] wz-dim">{n.band || '2.4GHz'} · {n.security || 'Open'}</div>
              </div>
              {config.wifiSSID === n.ssid && <span className="accent-text text-xs">{t('setup.network.selected')}</span>}
            </button>
          ))}
        </div>
      )}

      {/* Password input */}
      {config.wifiSSID && showPassword && (
        <div className="wz-surface border wz-hairline rounded-xl p-4 mb-4 animate-[fadeIn_0.2s_ease-out]">
          <div className="flex items-center gap-2 mb-2">
            <span className="text-sm wz-body">{config.wifiSSID}</span>
            <button onClick={() => { update('wifiSSID', ''); setShowPassword(false) }} className="text-xs wz-dim hover:wz-body">{t('setup.network.change')}</button>
          </div>
          <input
            type="password"
            value={config.wifiPassword}
            onChange={e => update('wifiPassword', e.target.value)}
            placeholder={t('setup.network.wifi_password')}
            autoFocus
            className="input"
          />
        </div>
      )}

      <NavBar onPrev={onPrev} onNext={onNext} skipLabel={t('setup.network.skip_ethernet')} onSkip={onNext} />
    </div>
  )
}

// ═══════════════════════════════════
// Account
// ═══════════════════════════════════

function AccountStep({ config, update, onNext, onPrev }: StepProps) {
  const { t } = useI18nTyped()
  const [error, setError] = useState('')

  // Local account only — Vulos is self-hosted software with no cloud account
  // to sign in to or create.
  const handleNext = () => {
    if (!config.username || config.username.length < 2) {
      setError(t('setup.account.error_username'))
      return
    }
    if (!config.password || config.password.length < 4) {
      setError(t('setup.account.error_password'))
      return
    }
    setError('')
    onNext()
  }

  return (
    <div>
      <StepHeader title={t('setup.account.title')} subtitle={t('setup.account.subtitle')} />

      <div className="space-y-4">
        <div>
          <label className="block text-xs wz-dim mb-1.5">{t('setup.account.name_label')}</label>
          <input
            value={config.displayName}
            onChange={e => update('displayName', e.target.value)}
            placeholder={t('setup.account.name_placeholder')}
            autoFocus
            className="input text-base py-3"
          />
        </div>
        <div>
          <label className="block text-xs wz-dim mb-1.5">{t('setup.account.username_label')}</label>
          <input
            value={config.username}
            onChange={e => update('username', e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ''))}
            placeholder={t('setup.account.username_placeholder')}
            className="input text-base py-3 font-mono"
          />
        </div>
        <div>
          <label className="block text-xs wz-dim mb-1.5">{t('setup.account.password_label')}</label>
          <input
            type="password"
            value={config.password}
            onChange={e => update('password', e.target.value)}
            placeholder={t('setup.account.password_placeholder')}
            className="input text-base py-3"
          />
        </div>
      </div>

      {error && <p className="text-sm wz-danger mt-2">{error}</p>}

      <NavBar onPrev={onPrev} onNext={handleNext} />
    </div>
  )
}

// ═══════════════════════════════════
// PIN
// ═══════════════════════════════════
function PinStep({ config, update, onNext, onPrev }: StepProps) {
  const { t } = useI18nTyped()
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')

  const handleNext = () => {
    if (config.pin && config.pin !== confirm) {
      setError(t('setup.pin.error_match'))
      return
    }
    if (config.pin && config.pin.length < 4) {
      setError(t('setup.pin.error_length'))
      return
    }
    onNext()
  }

  const mismatch = Boolean(confirm) && config.pin !== confirm

  return (
    <div>
      <StepHeader title={t('setup.pin.title')} subtitle={t('setup.pin.subtitle')} />

      {error && <p role="alert" className="wz-note wz-note--danger mb-4">{error}</p>}

      <div className="wz-panel">
        {/* Labelled, not placeholder-only. These were two unlabelled password
            boxes distinguishable only by their placeholder, which a screen
            reader announces last and a filled field does not announce at all. */}
        <label className="wz-label" htmlFor="wz-pin">{t('setup.pin.placeholder')}</label>
        <input
          id="wz-pin"
          type="password"
          inputMode="numeric"
          pattern="[0-9]*"
          autoComplete="new-password"
          value={config.pin}
          onChange={e => { update('pin', e.target.value.replace(/\D/g, '')); setError('') }}
          maxLength={8}
          className="input wz-pin-input"
        />

        {config.pin && (
          <div className="mt-3">
            <label className="wz-label" htmlFor="wz-pin-confirm">{t('setup.pin.confirm_placeholder')}</label>
            <input
              id="wz-pin-confirm"
              type="password"
              inputMode="numeric"
              pattern="[0-9]*"
              autoComplete="new-password"
              value={confirm}
              onChange={e => { setConfirm(e.target.value.replace(/\D/g, '')); setError('') }}
              maxLength={8}
              aria-describedby={confirm ? 'pin-confirm-msg' : undefined}
              aria-invalid={mismatch || undefined}
              className="input wz-pin-input"
            />
            {confirm && (
              <p
                id="pin-confirm-msg"
                role={mismatch ? 'alert' : undefined}
                className={`wz-hint mt-1.5 flex items-center gap-1.5 ${mismatch ? 'wz-danger' : 'wz-ok'}`}
              >
                <span aria-hidden="true">{mismatch ? '✕' : '✓'}</span>
                {mismatch ? t('setup.pin.error_match') : t('setup.pin.match')}
              </p>
            )}
          </div>
        )}
      </div>

      {/* The PIN unlocks a screen; it does not replace the account password and
          it never leaves this device. Worth saying, because the step otherwise
          reads like a second password being demanded. */}
      <p className="wz-hint mt-3">
        This PIN only unlocks the screen on this device. Your account password still signs you in,
        and the PIN is never sent anywhere.
      </p>

      {/* Was a bespoke three-button row whose primary action was WHITE ON AMBER:
          1.89:1 dark, 3.19:1 light, the single worst contrast failure in the
          wizard. Amber also read as a warning on the one control the user is
          meant to press. It uses the standard action bar now, so the primary
          action of this step looks and measures like every other step's. */}
      <NavBar
        onPrev={onPrev}
        onNext={handleNext}
        nextLabel={config.pin ? t('setup.pin.set') : undefined}
        skipLabel={t('setup.pin.skip')}
        onSkip={() => { update('pin', ''); onNext() }}
        nextDisabled={Boolean(config.pin && config.pin.length < 4)}
      />
    </div>
  )
}

// ═══════════════════════════════════
// Appearance
// ═══════════════════════════════════
// ═══════════════════════════════════
// BUNDLE-01: Your apps — default-everything (batteries-included, opt-out)
// ═══════════════════════════════════
//
// The founder-confirmed model: the OS ships batteries-included. EVERYTHING is
// pre-checked — Mail (the lilmail connector, which also backs the built-in
// Calendar/Contacts widgets) plus the owned productivity app (Diwan/Docs,
// which now includes whiteboards as a document type). A lean user (e.g. a
// gamer) can OPT OUT here:
//   - uncheck productivity apps → drops Diwan/Docs
//   - uncheck Mail              → drops the Mail connector
// There is no "Workspace" shell — the OS IS the shell. Files, Calendar and
// Contacts are always present (Calendar/Contacts degrade to "Connect Mail" when
// no account is linked). The persisted flag is still `workspace` for
// backend-contract compatibility.
//
// On advance we persist the choice to POST /api/setup/apps so the launcher hides
// the tiles the user opted out of. Best-effort: a failed write just means the
// batteries-included default (everything shown) — never a broken install.
function AppsStep({ config, update, onNext, onPrev }: StepProps) {
  const email = config.suiteEmail !== false
  const workspace = config.suiteWorkspace !== false
  const [saving, setSaving] = useState(false)

  const handleNext = async () => {
    setSaving(true)
    try {
      await fetch('/api/setup/apps', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email, workspace }),
      }).catch(() => {})
    } finally {
      setSaving(false)
      onNext()
    }
  }

  const OptRow = ({ checked, onToggle, title, desc, accent }: { checked: boolean; onToggle: () => void; title: string; desc: string; accent: string }) => (
    <button
      type="button"
      onClick={onToggle}
      className={`w-full flex items-start gap-3 rounded-2xl border-2 px-4 py-4 text-left transition-all
        ${checked
          ? `${accent} shadow-sm`
          : 'wz-hairline wz-surface hover:wz-edge'}`}
    >
      {/* Checkbox */}
      <span className={`mt-0.5 flex-shrink-0 w-5 h-5 rounded-md flex items-center justify-center border-2 transition-colors
        ${checked ? 'accent-bg accent-border' : 'wz-edge bg-transparent'}`}>
        {checked && (
          <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 text-white">
            <path d="M3.5 8l3 3 6-6" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          </svg>
        )}
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium wz-strong">{title}</div>
        <p className="text-xs wz-dim leading-relaxed mt-0.5">{desc}</p>
      </div>
    </button>
  )

  const minimal = !email && !workspace

  return (
    <div>
      <StepHeader
        title="Your apps"
        subtitle="Everything's included by default. Keep the full experience, or trim it down — you can add any of these back later."
      />

      <div className="space-y-3 mb-4">
        <OptRow
          checked={email}
          onToggle={() => update('suiteEmail', !email)}
          title="Claim your Vulos username — enables Mail"
          desc="Reserves your account handle across the suite and enables the Mail app, which connects to a mailbox you already own (Gmail/Outlook/any IMAP/SMTP) — there is no Vulos-hosted mailbox. Declining is the only way to skip Mail."
          accent="accent-border accent-bg-soft"
        />
        <OptRow
          checked={workspace}
          onToggle={() => update('suiteWorkspace', !workspace)}
          title="Install the productivity app — Diwan"
          desc="Diwan — Docs, Sheets, Slides, PDF and Whiteboards. Uncheck for a lean OS without the productivity app. Files, Calendar and Contacts are always included."
          accent="accent-border accent-bg-soft"
        />
      </div>

      {minimal && (
        <div className="mb-4 rounded-xl wz-surface border wz-hairline px-4 py-3">
          <p className="text-xs wz-dim leading-relaxed">
            <span className="wz-body font-medium">Minimal OS.</span>{' '}
            You'll get a clean Vulos install without Mail or the productivity apps — great for gaming or a single-purpose device. Everything stays available to add later from the App Hub.
          </p>
        </div>
      )}

      <div className="flex items-center justify-between mt-6 pt-4 border-t wz-hairline">
        <button onClick={onPrev} className="text-sm wz-dim hover:wz-body transition-colors">
          ← Back
        </button>
        <button onClick={handleNext} disabled={saving} className="btn-primary flex items-center gap-2">
          {saving ? (
            <>
              <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />
              Saving…
            </>
          ) : (
            'Continue →'
          )}
        </button>
      </div>
    </div>
  )
}

function AppearanceStep({ onNext, onPrev }: { onNext: () => void; onPrev: () => void }) {
  const { t } = useI18nTyped()
  const { theme, setTheme, nightShiftMode, setNightShiftMode } = useThemeTyped()

  const themes = [
    { value: 'dark', label: t('setup.appearance.theme_dark'), desc: t('setup.appearance.theme_dark_desc'), preview: '#0c0c0c',
      icon: <svg viewBox="0 0 24 24" className="w-8 h-8"><path d="M12 3a9 9 0 109 9c0-.46-.04-.92-.1-1.36a5.39 5.39 0 01-4.4 2.26 5.4 5.4 0 01-3.14-9.8A9.06 9.06 0 0012 3z" fill="currentColor"/></svg> },
    { value: 'light', label: t('setup.appearance.theme_light'), desc: t('setup.appearance.theme_light_desc'), preview: '#ffffff',
      icon: <svg viewBox="0 0 24 24" className="w-8 h-8"><circle cx="12" cy="12" r="5" fill="currentColor"/><path d="M12 1v2m0 18v2M4.22 4.22l1.42 1.42m12.72 12.72l1.42 1.42M1 12h2m18 0h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42" stroke="currentColor" strokeWidth="2" strokeLinecap="round"/></svg> },
    { value: 'auto', label: t('setup.appearance.theme_auto'), desc: t('setup.appearance.theme_auto_desc'), preview: 'linear-gradient(135deg, #0c0c0c 50%, #ffffff 50%)',
      icon: <svg viewBox="0 0 24 24" className="w-8 h-8"><circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeWidth="2"/><path d="M12 3a9 9 0 010 18V3z" fill="currentColor"/></svg> },
    { value: 'schedule', label: t('setup.appearance.theme_schedule'), desc: t('setup.appearance.theme_schedule_desc'), preview: 'linear-gradient(180deg, #1a1a2e 0%, #f5a623 100%)',
      icon: <svg viewBox="0 0 24 24" className="w-8 h-8"><circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeWidth="2"/><path d="M12 6v6l4 2" stroke="currentColor" strokeWidth="2" strokeLinecap="round"/></svg> },
  ]

  return (
    <div>
      <StepHeader title={t('setup.appearance.title')} subtitle={t('setup.appearance.subtitle')} />

      <div className="grid grid-cols-2 gap-3 mb-6">
        {themes.map(thm => (
          <button
            key={thm.value}
            onClick={() => setTheme(thm.value)}
            className={`relative flex flex-col items-center gap-2 px-4 py-5 rounded-2xl text-center transition-all
              ${theme === thm.value
                ? 'accent-bg-soft border-2 accent-border wz-strong shadow-lg'
                : 'wz-surface border-2 wz-hairline wz-body hover:wz-edge hover:wz-strong'}`}
          >
            {/* Preview swatch */}
            <div className="w-12 h-12 rounded-xl border wz-edge flex items-center justify-center overflow-hidden"
              style={{ background: thm.preview }}>
              <span className={theme === thm.value ? 'accent-text' : 'wz-body'}>{thm.icon}</span>
            </div>
            <div>
              <div className="text-sm font-medium">{thm.label}</div>
              <div className="text-[12px] wz-dim mt-0.5">{thm.desc}</div>
            </div>
            {theme === thm.value && (
              <div className="absolute top-2 right-2 w-5 h-5 rounded-full accent-bg flex items-center justify-center">
                <svg viewBox="0 0 16 16" className="w-3 h-3 text-white"><path d="M3.5 8l3 3 6-6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none"/></svg>
              </div>
            )}
          </button>
        ))}
      </div>

      {/* Night Shift quick toggle */}
      <div className="wz-surface border wz-hairline rounded-xl px-4 py-3 mb-2">
        <div className="flex items-center justify-between">
          <div>
            <div className="text-sm wz-body">{t('setup.appearance.night_shift')}</div>
            <div className="text-[12px] wz-dim">{t('setup.appearance.night_shift_desc')}</div>
          </div>
          <button
            onClick={() => setNightShiftMode(nightShiftMode === 'off' ? 'auto' : 'off')}
            className={`w-10 h-5 rounded-full transition-colors relative ${nightShiftMode !== 'off' ? 'bg-warning' : 'wz-surface-2'}`}
          >
            <span className={`absolute top-0.5 w-4 h-4 rounded-full bg-white transition-transform ${nightShiftMode !== 'off' ? 'left-5' : 'left-0.5'}`} />
          </button>
        </div>
      </div>

      <NavBar onPrev={onPrev} onNext={onNext} />
    </div>
  )
}

// ═══════════════════════════════════
// INIT-05: Identity Step
// ═══════════════════════════════════
function IS05_IdentityStep({ config, update, onNext, onPrev }: StepProps) {
  const [IS05_loading, IS05_setLoading] = useState(true)
  const [IS05_saving, IS05_setSaving] = useState(false)
  const [IS05_error, IS05_setError] = useState('')
  const [IS05_hostnameEdited, IS05_setHostnameEdited] = useState(false)

  const t = (s: string) => s

  useEffect(() => {
    fetch('/api/identity')
      .then(r => {
        if (!r.ok) throw new Error('not found')
        return r.json()
      })
      .then((raw: unknown) => {
        // Untrusted network JSON — narrowed field-by-field, never cast.
        const data = isRecord(raw) ? raw : {}
        const ulid = (typeof data.ulid === 'string' && data.ulid) || (typeof data.instance_id === 'string' && data.instance_id) || 'auto-generated'
        update('IS05_ulid', ulid)
        if (!IS05_hostnameEdited) {
          update('IS05_hostname', (typeof data.hostname === 'string' && data.hostname) || '')
        }
      })
      .catch(() => {
        update('IS05_ulid', 'auto-generated')
      })
      .finally(() => IS05_setLoading(false))
  }, [])

  const handleNext = async () => {
    IS05_setError('')
    if (config.IS05_hostname && IS05_hostnameEdited) {
      IS05_setSaving(true)
      try {
        await fetch('/api/identity/hostname', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ hostname: config.IS05_hostname }),
        })
      } catch {
        // degrade gracefully — hostname save is best-effort
      }
      IS05_setSaving(false)
    }
    onNext()
  }

  return (
    <div>
      <StepHeader
        title={t('Your Node Identity')}
        subtitle={t('Every Vulos OS node has a unique identifier. You can customise the hostname.')}
      />

      <div className="space-y-4 mb-2">
        {/* ULID display */}
        <div className="wz-surface border wz-hairline rounded-xl px-4 py-4">
          <div className="text-[12px] wz-dim uppercase tracking-wider mb-2">{t('Instance ID (ULID)')}</div>
          {IS05_loading ? (
            <div className="h-5 w-48 wz-surface-2 rounded animate-pulse" />
          ) : (
            <div className="font-mono text-sm accent-text select-all break-all">
              {config.IS05_ulid}
            </div>
          )}
          <div className="text-[12px] wz-dim mt-1">{t('Read-only — cryptographically unique, auto-assigned')}</div>
        </div>

        {/* Hostname input */}
        <div>
          <label className="block text-xs wz-dim mb-1.5">{t('Hostname')}</label>
          <input
            value={config.IS05_hostname}
            onChange={e => {
              IS05_setHostnameEdited(true)
              update('IS05_hostname', e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))
            }}
            placeholder="my-vulos-node"
            className="input text-base py-3 font-mono"
          />
          <p className="text-[12px] wz-dim mt-1">{t('Lowercase letters, numbers and hyphens only')}</p>
        </div>

        {IS05_error && <p className="text-sm wz-danger">{IS05_error}</p>}
      </div>

      <NavBar onPrev={onPrev} onNext={handleNext} nextLabel={IS05_saving ? t('Saving...') : t('Continue')} />
    </div>
  )
}

// ═══════════════════════════════════
// INIT-05: Storage Step
// ═══════════════════════════════════
function IS05_StorageStep({ config, update, onNext, onPrev }: StepProps) {
  const [IS05_passphraseConfirm, IS05_setPassphraseConfirm] = useState('')
  const [IS05_error, IS05_setError] = useState('')
  const [IS05_saving, IS05_setSaving] = useState(false)

  const t = (s: string) => s

  const handleNext = async () => {
    IS05_setError('')
    if (config.IS05_storageEnabled) {
      if (!config.IS05_storagePassword) {
        IS05_setError(t('Storage password is required'))
        return
      }
      if (!config.IS05_storagePassphrase) {
        IS05_setError(t('Passphrase is required'))
        return
      }
      if (config.IS05_storagePassphrase !== IS05_passphraseConfirm) {
        IS05_setError(t('Passphrases do not match'))
        return
      }
    }
    IS05_setSaving(true)
    try {
      await fetch('/api/setup/storage', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(
          config.IS05_storageEnabled
            ? { enable: true, size_gb: config.IS05_storageSizeGb, password: config.IS05_storagePassword, passphrase: config.IS05_storagePassphrase }
            : { enable: false }
        ),
      })
    } catch {
      // degrade gracefully
    }
    // STORE-LOCAL-01: persist the bundle storage mode. Whatever the operator
    // left selected is POSTed so the row is materialised and the dashboard
    // reflects it — the env contract for the local-fs default is just
    // VULOS_STORAGE_MODE. This must send the SELECTED mode, not a hard-coded
    // one: hard-coding a hosted backend here would put every fresh box on
    // hosted storage no matter what the default says.
    try {
      const modeBody = config.IS05_storageMode === 'local-minio-sync'
        ? {
            mode: 'local-minio-sync',
            minio_endpoint: config.IS05_storageMinioEndpoint,
            minio_region: config.IS05_storageMinioRegion,
            minio_bucket: config.IS05_storageMinioBucket,
            minio_creds_ref: config.IS05_storageMinioCredsRef,
          }
        : { mode: config.IS05_storageMode || 'local-fs' }
      await fetch('/api/storagemode', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(modeBody),
      })
    } catch { /* mode is best-effort during setup — admin can change later */ }
    IS05_setSaving(false)
    // Record that storage was not enabled (skipped via Continue with toggle off)
    if (!config.IS05_storageEnabled) {
      update('IS05_storageSkipped', true)
    }
    onNext()
  }

  const handleSkip = async () => {
    IS05_setSaving(true)
    try {
      await fetch('/api/setup/storage', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enable: false }),
      })
    } catch { /* storage disable is best-effort — proceed regardless */ }
    IS05_setSaving(false)
    update('IS05_storageSkipped', true)
    onNext()
  }

  return (
    <div>
      <StepHeader
        title={t('Cluster Storage')}
        subtitle={t('Enable distributed cluster sync storage — optional, can be configured later.')}
      />

      {/* Toggle */}
      <div className="wz-surface border wz-hairline rounded-xl px-4 py-3 mb-4">
        <div className="flex items-center justify-between">
          <div>
            <div className="text-sm wz-strong">{t('Enable Cluster Sync')}</div>
            <div className="text-[12px] wz-dim">{t('Shared encrypted storage across cluster nodes')}</div>
          </div>
          <button
            onClick={() => { update('IS05_storageEnabled', !config.IS05_storageEnabled); IS05_setError('') }}
            className={`w-10 h-5 rounded-full transition-colors relative ${config.IS05_storageEnabled ? 'accent-bg' : 'wz-surface-2'}`}
          >
            <span className={`absolute top-0.5 w-4 h-4 rounded-full bg-white transition-transform ${config.IS05_storageEnabled ? 'left-5' : 'left-0.5'}`} />
          </button>
        </div>
      </div>

      {/* STORE-LOCAL-01: bundle storage-mode selector. Always visible so the
          operator chooses a backend even when cluster-sync is off. */}
      <div className="wz-surface border wz-hairline rounded-xl px-4 py-3 mb-4">
        <div className="text-sm wz-strong mb-2">{t('Bundle Storage Mode')}</div>
        <div className="text-[12px] wz-dim mb-2">
          {t('This device is the default — your data stays on this box, with no object store and no third-party service. Local MinIO + sync runs a local source-of-truth and replicates between Vulos nodes via the CRDT layer. Central Tigris hands your data to a hosted third party.')}
        </div>
        <select
          value={config.IS05_storageMode}
          onChange={e => update('IS05_storageMode', e.target.value)}
          className="input text-sm py-2"
        >
          <option value="local-fs">{t('This device (default — no third-party service)')}</option>
          <option value="local-minio-sync">{t('Local MinIO + CRDT sync (opt-in)')}</option>
          <option value="central-tigris">{t('Central Tigris (opt-in — hosted third-party S3)')}</option>
        </select>

        {config.IS05_storageMode === 'local-minio-sync' && (
          <div className="space-y-2 mt-3 animate-[fadeIn_0.2s_ease-out]">
            <div>
              <label className="block text-[12px] wz-dim mb-1">{t('MinIO endpoint')}</label>
              <input
                value={config.IS05_storageMinioEndpoint}
                onChange={e => update('IS05_storageMinioEndpoint', e.target.value)}
                placeholder="http://127.0.0.1:9000"
                className="input text-sm py-2"
              />
            </div>
            <div>
              <label className="block text-[12px] wz-dim mb-1">{t('Region')}</label>
              <input
                value={config.IS05_storageMinioRegion}
                onChange={e => update('IS05_storageMinioRegion', e.target.value)}
                placeholder="auto"
                className="input text-sm py-2"
              />
            </div>
            <div>
              <label className="block text-[12px] wz-dim mb-1">{t('Bucket')}</label>
              <input
                value={config.IS05_storageMinioBucket}
                onChange={e => update('IS05_storageMinioBucket', e.target.value)}
                placeholder="vulos-bundle"
                className="input text-sm py-2"
              />
            </div>
            <div>
              <label className="block text-[12px] wz-dim mb-1">{t('Credentials reference (file path or secret-store key)')}</label>
              <input
                value={config.IS05_storageMinioCredsRef}
                onChange={e => update('IS05_storageMinioCredsRef', e.target.value)}
                placeholder="/var/lib/vulos/minio/.minio_secret"
                className="input text-sm py-2"
              />
            </div>
            <p className="text-[12px] wz-dim">
              {t('The installer writes /var/lib/vulos/minio/.minio_secret when run with --storage=minio.')}
            </p>
          </div>
        )}
      </div>

      {config.IS05_storageEnabled && (
        <div className="space-y-4 mb-2 animate-[fadeIn_0.2s_ease-out]">
          {/* Size slider */}
          <div>
            <div className="flex items-center justify-between mb-1.5">
              <label className="text-xs wz-dim">{t('Allocated Size')}</label>
              <span className="text-sm font-mono accent-text">{config.IS05_storageSizeGb} GB</span>
            </div>
            <input
              type="range"
              min="5"
              max="100"
              step="5"
              value={config.IS05_storageSizeGb}
              onChange={e => update('IS05_storageSizeGb', Number(e.target.value))}
              className="w-full" style={{ accentColor: 'var(--accent)' }}
            />
            <div className="flex justify-between text-[12px] wz-dim mt-1">
              <span>5 GB</span><span>100 GB</span>
            </div>
          </div>

          {/* Password */}
          <div>
            <label className="block text-xs wz-dim mb-1.5">{t('Storage Password')}</label>
            <input
              type="password"
              value={config.IS05_storagePassword}
              onChange={e => { update('IS05_storagePassword', e.target.value); IS05_setError('') }}
              placeholder={t('Access password for this storage bucket')}
              className="input text-base py-3"
            />
          </div>

          {/* Passphrase + confirm */}
          <div>
            <label className="block text-xs wz-dim mb-1.5">{t('Encryption Passphrase')}</label>
            <input
              type="password"
              value={config.IS05_storagePassphrase}
              onChange={e => { update('IS05_storagePassphrase', e.target.value); IS05_setError('') }}
              placeholder={t('Strong passphrase for encryption at rest')}
              className="input text-base py-3 mb-2"
            />
            <input
              type="password"
              value={IS05_passphraseConfirm}
              onChange={e => { IS05_setPassphraseConfirm(e.target.value); IS05_setError('') }}
              placeholder={t('Confirm passphrase')}
              className={`input text-base py-3 ${IS05_passphraseConfirm && config.IS05_storagePassphrase !== IS05_passphraseConfirm ? 'border-danger-soft' : ''}`}
            />
          </div>

          {IS05_error && <p className="text-sm wz-danger">{IS05_error}</p>}
        </div>
      )}

      <NavBar
        onPrev={onPrev}
        onNext={handleNext}
        nextLabel={IS05_saving ? t('Saving...') : t('Continue')}
        skipLabel={config.IS05_storageEnabled ? undefined : t('Skip for now')}
        onSkip={config.IS05_storageEnabled ? undefined : handleSkip}
      />
    </div>
  )
}

// ═══════════════════════════════════
// INIT-05: SSH Step
// ═══════════════════════════════════
function IS05_SSHStep({ config, update, onNext, onPrev }: StepProps) {
  const [IS05_generating, IS05_setGenerating] = useState(false)
  const [IS05_privateKey, IS05_setPrivateKey] = useState('')
  const [IS05_confirmed, IS05_setConfirmed] = useState(false)
  const [IS05_copied, IS05_setCopied] = useState(false)
  const [IS05_error, IS05_setError] = useState('')
  const [IS05_saving, IS05_setSaving] = useState(false)

  const t = (s: string) => s

  // Convert ArrayBuffer to base64
  const IS05_bufToB64 = (buf: ArrayBuffer) => {
    const bytes = new Uint8Array(buf)
    let bin = ''
    for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i])
    return btoa(bin)
  }

  // Build OpenSSH Ed25519 public key wire format
  const IS05_buildOpenSSHPubkey = (rawPub: ArrayBuffer) => {
    const keyType = 'ssh-ed25519'
    const typeBytes = new TextEncoder().encode(keyType)
    const rawBytes = new Uint8Array(rawPub)
    const buf = new ArrayBuffer(4 + typeBytes.length + 4 + rawBytes.length)
    const view = new DataView(buf)
    let off = 0
    view.setUint32(off, typeBytes.length); off += 4
    new Uint8Array(buf, off, typeBytes.length).set(typeBytes); off += typeBytes.length
    view.setUint32(off, rawBytes.length); off += 4
    new Uint8Array(buf, off, rawBytes.length).set(rawBytes)
    return `ssh-ed25519 ${btoa(String.fromCharCode(...new Uint8Array(buf)))} setup`
  }

  // Build PEM-style private key representation (PKCS8)
  const IS05_buildPrivkeyPEM = async (privKeyRaw: CryptoKey) => {
    const pkcs8 = await crypto.subtle.exportKey('pkcs8', privKeyRaw)
    const b64 = IS05_bufToB64(pkcs8)
    const lines = b64.match(/.{1,64}/g) || []
    return `-----BEGIN PRIVATE KEY-----\n${lines.join('\n')}\n-----END PRIVATE KEY-----`
  }

  // SHA-256 fingerprint of raw public key bytes
  const IS05_fingerprint = async (rawPub: ArrayBuffer) => {
    const hash = await crypto.subtle.digest('SHA-256', rawPub)
    const b64 = IS05_bufToB64(hash)
    return `SHA256:${b64.replace(/=+$/, '')}`
  }

  const IS05_generate = async () => {
    IS05_setGenerating(true)
    IS05_setError('')
    IS05_setConfirmed(false)
    IS05_setPrivateKey('')
    try {
      const keyPair = await crypto.subtle.generateKey(
        { name: 'Ed25519' },
        true,
        ['sign', 'verify']
      )
      const rawPub = await crypto.subtle.exportKey('raw', keyPair.publicKey)
      const pubkeyStr = IS05_buildOpenSSHPubkey(rawPub)
      const privPEM = await IS05_buildPrivkeyPEM(keyPair.privateKey)
      const fp = await IS05_fingerprint(rawPub)

      update('IS05_sshPubkey', pubkeyStr)
      update('IS05_sshFingerprint', fp)
      IS05_setPrivateKey(privPEM)
    } catch (err: unknown) {
      const msg = isRecord(err) && typeof err.message === 'string' ? err.message : ''
      IS05_setError(t('Key generation failed. Your browser may not support Ed25519. ') + msg)
    }
    IS05_setGenerating(false)
  }

  const IS05_copy = async () => {
    try {
      await navigator.clipboard.writeText(IS05_privateKey)
      IS05_setCopied(true)
      setTimeout(() => IS05_setCopied(false), 2000)
    } catch {
      IS05_setError(t('Could not copy to clipboard — please select and copy manually'))
    }
  }

  const handleNext = async () => {
    if (!config.IS05_sshPubkey) {
      IS05_setError(t('Please generate an SSH keypair first'))
      return
    }
    if (!IS05_confirmed) {
      IS05_setError(t('Please confirm you have saved your private key'))
      return
    }
    IS05_setSaving(true)
    try {
      await fetch('/api/ssh/authorized', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ comment: 'setup', pubkey: config.IS05_sshPubkey }),
      })
    } catch {
      // degrade gracefully
    }
    IS05_setSaving(false)
    onNext()
  }

  return (
    <div>
      <StepHeader
        title={t('SSH Access Key')}
        subtitle={t('Generate an Ed25519 keypair for secure remote access to this node.')}
      />

      <div className="space-y-4 mb-2">
        {/* Generate button */}
        <button
          onClick={IS05_generate}
          disabled={IS05_generating}
          className={`w-full py-3 rounded-xl text-sm font-medium transition-all
            ${IS05_generating
              ? 'wz-surface-2 wz-dim'
              : 'wz-surface border wz-edge wz-body hover-accent-border hover-accent-text'}`}
        >
          {IS05_generating ? (
            <span className="flex items-center justify-center gap-2">
              <span className="w-4 h-4 border-2 wz-edge rounded-full animate-spin" style={{ borderTopColor: 'var(--accent)' }} />
              {t('Generating keypair...')}
            </span>
          ) : config.IS05_sshPubkey ? t('Regenerate Keypair') : t('Generate Ed25519 Keypair')}
        </button>

        {/* Private key display (shown once) */}
        {IS05_privateKey && (
          <div className="wz-surface-deep border wz-hairline rounded-xl overflow-hidden animate-[fadeIn_0.2s_ease-out]">
            <div className="flex items-center justify-between px-4 py-2 wz-surface border-b wz-hairline">
              <span className="text-[12px] wz-warn font-medium">{t('Private Key — shown once, copy now')}</span>
              <button
                onClick={IS05_copy}
                className={`text-xs px-3 py-1 rounded-lg transition-colors ${IS05_copied ? 'bg-success-soft wz-ok' : 'wz-surface-2 wz-body hover-accent-text'}`}
              >
                {IS05_copied ? t('Copied!') : t('Copy')}
              </button>
            </div>
            <pre className="text-[12px] font-mono wz-body p-4 overflow-x-auto whitespace-pre-wrap break-all select-all">
              {IS05_privateKey}
            </pre>
          </div>
        )}

        {/* Public key fingerprint */}
        {config.IS05_sshFingerprint && (
          <div className="wz-surface border wz-hairline rounded-xl px-4 py-3">
            <div className="text-[12px] wz-dim uppercase tracking-wider mb-1">{t('Public Key Fingerprint')}</div>
            <div className="font-mono text-xs wz-ok break-all">{config.IS05_sshFingerprint}</div>
          </div>
        )}

        {/* Confirmation checkbox */}
        {config.IS05_sshPubkey && IS05_privateKey && (
          <label className="flex items-start gap-3 cursor-pointer group animate-[fadeIn_0.2s_ease-out]">
            <div className="relative mt-0.5 shrink-0">
              <input
                type="checkbox"
                checked={IS05_confirmed}
                onChange={e => { IS05_setConfirmed(e.target.checked); IS05_setError('') }}
                className="sr-only"
              />
              <div className={`w-5 h-5 rounded border-2 flex items-center justify-center transition-colors
                ${IS05_confirmed ? 'accent-bg accent-border' : 'wz-edge group-hover:wz-edge'}`}>
                {IS05_confirmed && (
                  <svg viewBox="0 0 16 16" className="w-3 h-3 text-white"><path d="M3.5 8l3 3 6-6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none"/></svg>
                )}
              </div>
            </div>
            <span className="text-sm wz-body group-hover:wz-strong transition-colors leading-snug">
              {t('I have saved this private key in a secure location. I understand it will not be shown again.')}
            </span>
          </label>
        )}

        {IS05_error && <p className="text-sm wz-danger">{IS05_error}</p>}
      </div>

      <NavBar
        onPrev={onPrev}
        onNext={handleNext}
        nextLabel={IS05_saving ? t('Saving...') : t('Continue')}
        nextDisabled={Boolean(IS05_saving || (config.IS05_sshPubkey && IS05_privateKey && !IS05_confirmed))}
      />
    </div>
  )
}

// ═══════════════════════════════════
// INIT-05 + INIT-06: Recovery Kit Step
// ═══════════════════════════════════

interface RecoveryKitStorage {
  enabled: true
  size_gb: number
  s3_access_key: string
}

interface RecoveryKit {
  ulid: string
  hostname: string
  storage?: RecoveryKitStorage
  ssh_fingerprint: string
  issued_at: string
}

interface RecoveryKitPayload {
  schema_version: number
  kit: RecoveryKit
  checksum_sha256: string
}

// INIT-06: build a versioned kit object from wizard config
function IK06_buildKitObject(config: SetupConfig): RecoveryKit {
  const issuedAt = new Date().toISOString()
  return {
    ulid: config.IS05_ulid || '',
    hostname: config.IS05_hostname || '',
    ...(config.IS05_storageEnabled
      ? {
          storage: {
            enabled: true,
            size_gb: config.IS05_storageSizeGb,
            s3_access_key: config.IS05_s3AccessKey || '',
          },
        }
      : {}),
    ssh_fingerprint: config.IS05_sshFingerprint || '',
    issued_at: issuedAt,
  }
}

// INIT-06: compute SHA-256 over canonical JSON, return hex string
async function IK06_sha256hex(obj: unknown): Promise<string> {
  const canonical = JSON.stringify(obj)
  const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(canonical))
  return Array.from(new Uint8Array(buf)).map(b => b.toString(16).padStart(2, '0')).join('')
}

// INIT-06: assemble the versioned download payload
async function IK06_buildDownloadPayload(config: SetupConfig): Promise<RecoveryKitPayload> {
  const kit = IK06_buildKitObject(config)
  const checksumSha256 = await IK06_sha256hex(kit)
  return {
    schema_version: 1,
    kit,
    checksum_sha256: checksumSha256,
  }
}

// INIT-06: QR canvas component — renders the kit checksum + ULID as a QR code
function IK06_QRCanvas({ content }: { content: string }) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [IK06_qrError, IK06_setQRError] = useState('')

  useEffect(() => {
    if (!content || !canvasRef.current) return
    QRCode.toCanvas(canvasRef.current, content, {
      width: 160,
      margin: 2,
      color: { dark: '#e2e8f0', light: '#0a0a0a' },
      errorCorrectionLevel: 'M',
    }).catch((err: unknown) => {
      const msg = isRecord(err) && typeof err.message === 'string' && err.message ? err.message : 'QR render failed'
      IK06_setQRError(msg)
    })
  }, [content])

  if (IK06_qrError) {
    return (
      <div className="text-[12px] wz-dim text-center p-2">
        (QR rendering pending — verify checksum below)
      </div>
    )
  }

  return (
    <canvas
      ref={canvasRef}
      className="rounded-lg mx-auto block"
      style={{ imageRendering: 'pixelated' }}
    />
  )
}

function IS05_RecoveryKitStep({ config, onNext, onPrev }: { config: SetupConfig; onNext: () => void; onPrev: () => void }) {
  const [IS05_confirmText, IS05_setConfirmText] = useState('')
  const [IS05_downloading, IS05_setDownloading] = useState(false)
  const [IS05_downloaded, IS05_setDownloaded] = useState(false)
  const [IS05_error, IS05_setError] = useState('')
  // INIT-06: versioned payload built client-side (fallback when server unavailable)
  const [IK06_payload, IK06_setPayload] = useState<RecoveryKitPayload | null>(null)
  const [IK06_qrContent, IK06_setQRContent] = useState('')
  const [IK06_buildingPayload, IK06_setBuildingPayload] = useState(true)

  const t = (s: string) => s

  // INIT-06: build the payload on mount / whenever config changes
  useEffect(() => {
    IK06_setBuildingPayload(true)
    IK06_buildDownloadPayload(config)
      .then(payload => {
        IK06_setPayload(payload)
        // QR encodes: "vulos-recovery:v1:<ulid>:<checksum_sha256_prefix16>"
        const qrStr = `vulos-recovery:v1:${payload.kit.ulid}:${payload.checksum_sha256.slice(0, 16)}`
        IK06_setQRContent(qrStr)
      })
      .catch(() => {
        IK06_setQRContent('')
      })
      .finally(() => IK06_setBuildingPayload(false))
  }, [config.IS05_ulid, config.IS05_hostname, config.IS05_storageEnabled, config.IS05_sshFingerprint])

  // INIT-05 + INIT-06: confirm gate applies regardless of storage variant
  const IS05_canProceed = IS05_confirmText === 'confirm'

  // INIT-06: download uses the versioned schema with checksum; falls back to
  // server endpoint if local payload is unavailable
  const IS05_downloadKit = async () => {
    IS05_setDownloading(true)
    IS05_setError('')
    try {
      let blob
      if (IK06_payload) {
        // Client-built versioned payload (includes schema_version + checksum_sha256)
        const json = JSON.stringify(IK06_payload, null, 2)
        blob = new Blob([json], { type: 'application/json' })
      } else {
        // Fallback: fetch from server (INIT-11 endpoint)
        const res = await fetch('/api/recovery/kit')
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        blob = await res.blob()
      }
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `vulos-recovery-kit-${config.IS05_ulid || 'node'}.json`
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
      IS05_setDownloaded(true)
    } catch (err: unknown) {
      const msg = isRecord(err) && typeof err.message === 'string' && err.message ? err.message : t('unknown error')
      IS05_setError(t('Download failed: ') + msg)
    }
    IS05_setDownloading(false)
  }

  // INIT-06: storage-skipped notice
  const IS06_storageSkipped = config.IS05_storageSkipped && !config.IS05_storageEnabled

  return (
    <div>
      <StepHeader
        title={t('Recovery Kit')}
        subtitle={t('Store your recovery credentials somewhere safe — you will need them if you lose access.')}
      />

      {/* Credentials summary */}
      <div className="space-y-2 mb-4">
        <div className="wz-surface border wz-hairline rounded-xl px-4 py-3">
          <div className="text-[12px] wz-dim uppercase tracking-wider mb-2">{t('Identity')}</div>
          <div className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
            <div>
              <div className="wz-dim mb-0.5">{t('ULID')}</div>
              <div className="font-mono accent-text text-[12px] break-all">{config.IS05_ulid || '—'}</div>
            </div>
            <div>
              <div className="wz-dim mb-0.5">{t('Hostname')}</div>
              <div className="font-mono wz-body text-[12px]">{config.IS05_hostname || '—'}</div>
            </div>
          </div>
        </div>

        {config.IS05_storageEnabled && (
          <div className="wz-surface border wz-hairline rounded-xl px-4 py-3 animate-[fadeIn_0.2s_ease-out]">
            <div className="text-[12px] wz-dim uppercase tracking-wider mb-2">{t('Cluster Storage')}</div>
            <div className="text-xs">
              <div className="wz-dim mb-0.5">{t('Status')}</div>
              <div className="wz-ok">{t('Enabled')} · {config.IS05_storageSizeGb} GB</div>
              {config.IS05_s3AccessKey && (
                <>
                  <div className="wz-dim mt-2 mb-0.5">{t('S3 Access Key')}</div>
                  <div className="font-mono text-[12px] wz-body break-all">{config.IS05_s3AccessKey}</div>
                </>
              )}
            </div>
          </div>
        )}

        {/* INIT-06: storage-skipped notice — shown when user skipped storage */}
        {IS06_storageSkipped && (
          <div className="wz-surface border wz-hairline rounded-xl px-4 py-3 animate-[fadeIn_0.2s_ease-out]">
            <div className="text-[12px] wz-dim uppercase tracking-wider mb-1">{t('Cluster Storage')}</div>
            <div className="text-xs wz-dim">{t('Skipped — can be enabled later in Settings')}</div>
          </div>
        )}

        {config.IS05_sshFingerprint && (
          <div className="wz-surface border wz-hairline rounded-xl px-4 py-3">
            <div className="text-[12px] wz-dim uppercase tracking-wider mb-2">{t('SSH Access')}</div>
            <div className="text-[12px] font-mono wz-ok break-all">{config.IS05_sshFingerprint}</div>
          </div>
        )}
      </div>

      {/* INIT-06: Inline QR code — encodes recovery identity token */}
      {!IK06_buildingPayload && IK06_qrContent && (
        <div className="wz-surface-deep border wz-hairline rounded-xl p-4 mb-4 flex flex-col items-center gap-2 animate-[fadeIn_0.2s_ease-out]">
          <div className="text-[12px] wz-dim uppercase tracking-wider">{t('Recovery QR — scan to verify identity')}</div>
          <IK06_QRCanvas content={IK06_qrContent} />
          <div className="text-[12px] font-mono wz-dim break-all text-center max-w-[200px]">
            {IK06_qrContent}
          </div>
        </div>
      )}

      {/* INIT-06: Checksum preview */}
      {IK06_payload && (
        <div className="wz-surface border wz-hairline rounded-xl px-4 py-2 mb-4">
          <div className="text-[12px] wz-dim uppercase tracking-wider mb-1">{t('Kit checksum (SHA-256)')}</div>
          <div className="font-mono text-[12px] wz-dim break-all">{IK06_payload.checksum_sha256}</div>
        </div>
      )}

      {/* Download button */}
      <button
        onClick={IS05_downloadKit}
        disabled={IS05_downloading || IK06_buildingPayload}
        className={`w-full py-3 rounded-xl text-sm font-medium transition-all mb-4
          ${IS05_downloaded
            ? 'bg-success-soft border border-success-soft wz-ok'
            : IS05_downloading
              ? 'wz-surface-2 wz-dim'
              : 'accent-bg-soft border accent-border accent-text accent-bg-hover'}`}
      >
        {IS05_downloading ? (
          <span className="flex items-center justify-center gap-2">
            <span className="w-4 h-4 border-2 wz-edge rounded-full animate-spin" style={{ borderTopColor: 'var(--accent)' }} />
            {t('Preparing download...')}
          </span>
        ) : IS05_downloaded
          ? t('Recovery Kit Downloaded')
          : t('Download Recovery Kit')}
      </button>

      {IS05_error && <p className="text-sm wz-danger mb-3">{IS05_error}</p>}

      {/* Type-to-confirm gate — applies to ALL variants including storage-skipped */}
      <div className="wz-surface border wz-hairline rounded-xl px-4 py-4 mb-2">
        <p className="text-sm wz-body mb-3">
          {t('Type ')}
          <code className="wz-surface-2 wz-warn px-1.5 py-0.5 rounded text-xs font-mono">confirm</code>
          {t(' to proceed')}
        </p>
        <input
          value={IS05_confirmText}
          onChange={e => IS05_setConfirmText(e.target.value)}
          placeholder="confirm"
          className={`input py-2 font-mono text-sm ${IS05_canProceed ? 'border-success-soft wz-ok' : ''}`}
          autoComplete="off"
          autoCorrect="off"
          autoCapitalize="off"
          spellCheck={false}
        />
      </div>

      <NavBar
        onPrev={onPrev}
        onNext={onNext}
        nextLabel={t('Finish Setup')}
        nextDisabled={!IS05_canProceed}
      />
    </div>
  )
}

// ═══════════════════════════════════
// Ready
// ═══════════════════════════════════
function ReadyStep({ config, onFinish, onPrev }: { config: SetupConfig; onFinish: () => Promise<void>; onPrev: () => void }) {
  const { t } = useI18nTyped()
  const { theme } = useThemeTyped()
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')
  // WAVE2-RECOVERY: the 24-word master-key recovery phrase, returned once by the
  // register endpoint. When set, we FORCE the phrase-reveal screen before finishing.
  const [masterPhrase, setMasterPhrase] = useState('')
  // MODEL-DL-01: optional "Enable private AI search" install-time offer. After the
  // owner account exists (register set the session), we show a clearly-optional
  // step to download the on-box embedding model before entering the desktop.
  const [showPrivateAI, setShowPrivateAI] = useState(false)

  // finalize runs the post-account steps (PIN) and completes setup.
  const finalize = async () => {
    if (config.pin) {
      await fetch('/api/auth/pin/set', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pin: config.pin }),
      }).catch(() => {})
    }
    await onFinish()
  }

  const handleFinish = async () => {
    setCreating(true)
    setError('')

    // Create account first
    if (config.username && config.password) {
      try {
        const res = await fetch('/api/auth/register', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            username: config.username,
            password: config.password,
            display_name: config.displayName || config.username,
          }),
        })
        if (!res.ok) {
          // Untrusted network JSON — narrowed before use, never cast.
          const errRaw: unknown = await res.json().catch(() => ({}))
          const errData = isRecord(errRaw) ? errRaw : {}
          setError((typeof errData.error === 'string' && errData.error) || 'Failed to create account')
          setCreating(false)
          return
        }
        // WAVE2-RECOVERY trust boundary: the register response may carry the
        // one-time master-key recovery phrase. Narrowed with an explicit
        // typeof check — never cast to "the phrase is a string" without
        // verifying it, since this value is about to be shown to the user as
        // their only account-recovery credential.
        const raw: unknown = await res.json().catch(() => ({}))
        const data = isRecord(raw) ? raw : {}
        if (typeof data.master_recovery_phrase === 'string' && data.master_recovery_phrase) {
          setMasterPhrase(data.master_recovery_phrase)
          setCreating(false)
          return
        }
      } catch {
        setError(t('setup.ready.error_server'))
        setCreating(false)
        return
      }
    }

    // MODEL-DL-01: with an owner account now created (session established), offer
    // the optional private-AI model download BEFORE finalizing. Only meaningful
    // for a local OS account (a real owner session on this box); the join flow
    // (account already exists on the synced cluster) skips straight to finalize.
    if (config.username && config.password) {
      setCreating(false)
      setShowPrivateAI(true)
      return
    }

    await finalize()
  }

  // WAVE2-RECOVERY: forced recovery-phrase reveal gate. After the phrase is saved,
  // route into the optional private-AI offer (MODEL-DL-01) rather than finishing
  // directly, so the model download is offered on every local-account install.
  if (masterPhrase) {
    const afterPhrase = () => {
      setMasterPhrase('')
      if (config.username && config.password) { setShowPrivateAI(true); return }
      setCreating(true); finalize()
    }
    return (
      <MasterKeyReveal
        phrase={masterPhrase}
        onConfirm={afterPhrase}
        onSkip={afterPhrase}
      />
    )
  }

  // MODEL-DL-01: optional "Enable private AI search" install-time step. Clearly
  // optional — RAG works in lexical mode without it — and honest about the size
  // and the one-time python deps. Both "Enable" and "Skip" proceed to finalize.
  if (showPrivateAI) {
    return (
      <PrivateAIStep
        onDone={async () => { setShowPrivateAI(false); setCreating(true); await finalize() }}
      />
    )
  }

  const selectedTz = TIMEZONES.find(tz => tz.id === config.timezone)
  const selectedLang = LANGUAGES.find(l => l.code === config.locale)
  const themeLabels: Record<string, string> = {
    dark: t('setup.ready.theme_dark'),
    light: t('setup.ready.theme_light'),
    auto: t('setup.ready.theme_auto'),
    schedule: t('setup.ready.theme_schedule'),
  }

  return (
    <div className="text-center animate-[fadeIn_0.3s_ease-out]">
      <div
        className="w-16 h-16 mx-auto mb-5 rounded-2xl flex items-center justify-center"
        style={{ background: 'var(--status-success-soft)' }}
      >
        <svg viewBox="0 0 24 24" className="w-8 h-8 wz-ok" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M20 6L9 17l-5-5" />
        </svg>
      </div>
      <div className="flex flex-col items-center">
        <StepHeader title={t('setup.ready.title')} subtitle={t('setup.ready.subtitle')} />
      </div>

      <div className="grid grid-cols-2 gap-3 text-left mb-8">
        {config.deviceProfile && (
          <SummaryCard icon="💻" label="Device" value={DEVICE_PROFILES.find(p => p.id === config.deviceProfile)?.label || config.deviceProfile} />
        )}
                <SummaryCard icon="🌍" label={t('setup.ready.label_language')} value={selectedLang?.native || config.locale} />
        <SummaryCard icon="🕐" label={t('setup.ready.label_timezone')} value={selectedTz?.label || config.timezone || t('setup.ready.timezone_auto')} />
        <SummaryCard icon="📶" label={t('setup.ready.label_wifi')} value={config.wifiSSID || t('setup.ready.wifi_none')} />
        <SummaryCard icon="👤" label={t('setup.ready.label_account')} value={config.username || t('setup.ready.account_none')} />
        <SummaryCard icon="🎨" label={t('setup.ready.label_theme')} value={themeLabels[theme] || theme} />
      </div>

      {error && <p className="text-sm wz-danger mb-4">{error}</p>}

      <button
        onClick={handleFinish}
        disabled={creating}
        className="btn-primary px-10 py-3.5 text-base elevate-md hover:elevate-lg transition-shadow disabled:opacity-60"
      >
        {creating ? (
          <span className="flex items-center gap-2">
            <span className="spinner w-4 h-4" style={{ borderTopColor: '#fff', borderColor: 'rgba(255,255,255,0.3)' }} />
            {t('setup.ready.setting_up')}
          </span>
        ) : t('setup.ready.enter')}
      </button>

      <button onClick={onPrev} className="block mx-auto mt-4 text-sm transition-colors hover-accent-text" style={{ color: 'var(--text-muted)' }}>
        {t('setup.ready.go_back')}
      </button>

      {/* kitBackup-IK11: recovery kit hint — additive, admin context */}
      <div className="mt-6 mx-auto max-w-sm rounded-xl px-4 py-3 text-left" style={{ border: '1px solid var(--border-default)', background: 'var(--bg-surface)' }}>
        <p className="text-xs leading-relaxed" style={{ color: 'var(--text-muted)' }}>
          <span className="font-medium" style={{ color: 'var(--text-secondary)' }}>Recovery kit</span> — your credentials JSON was shown once during setup.
          You can re-download it any time as an admin via{' '}
          <code className="accent-text text-[12px]">GET /api/recovery/kit</code>
          {' '}from a trusted local session.
        </p>
      </div>
    </div>
  )
}

// ═══════════════════════════════════
// MODEL-DL-01: Optional private-AI (on-box embedding model) install offer
// ═══════════════════════════════════
//
// Shown once, right before entering the desktop, AFTER the owner account exists
// (so there is a real owner session for the owner-gated download endpoint). It
// is fully OPTIONAL and honest: RAG works in lexical mode with no model at all;
// downloading the curated, pinned all-MiniLM-L6-v2 upgrades it to genuine
// semantic search, entirely on the box. The model is fetched on demand from a
// pinned source and SHA-256-verified — nothing is bundled in the image.
//
// Exported for unit testing (its offer/downloading/done/error states); the
// wizard renders it inline via the ReadyStep flow, not via this named import.
type PrivateAIState = 'offer' | 'downloading' | 'done' | 'error'

interface ModelCatalogEntry {
  id?: string
  name?: string
  recommended?: boolean
  model?: { size_bytes?: number }
  tokenizer?: { size_bytes?: number }
}

interface PythonDeps {
  ready?: boolean
  install_hint?: string
}

function toModelCatalogEntry(x: unknown): ModelCatalogEntry {
  if (!isRecord(x)) return {}
  const model = isRecord(x.model) ? x.model : {}
  const tokenizer = isRecord(x.tokenizer) ? x.tokenizer : {}
  return {
    id: typeof x.id === 'string' ? x.id : undefined,
    name: typeof x.name === 'string' ? x.name : undefined,
    recommended: x.recommended === true,
    model: { size_bytes: typeof model.size_bytes === 'number' ? model.size_bytes : undefined },
    tokenizer: { size_bytes: typeof tokenizer.size_bytes === 'number' ? tokenizer.size_bytes : undefined },
  }
}

function toPythonDeps(x: unknown): PythonDeps {
  if (!isRecord(x)) return {}
  return {
    ready: x.ready === true,
    install_hint: typeof x.install_hint === 'string' ? x.install_hint : undefined,
  }
}

export function PrivateAIStep({ onDone }: { onDone: () => void | Promise<void> }) {
  const [state, setState] = useState<PrivateAIState>('offer')
  const [error, setError] = useState('')
  const [entry, setEntry] = useState<ModelCatalogEntry | null>(null) // recommended catalog entry (for size/name)
  const [deps, setDeps] = useState<PythonDeps | null>(null)

  // Load the catalog + python-deps status so the offer shows the true size and an
  // honest note about the one-time python dependencies.
  useEffect(() => {
    let cancelled = false
    fetch('/api/models', { credentials: 'include' })
      .then(r => r.ok ? r.json() : null)
      .then((raw: unknown) => {
        if (cancelled) return
        // Untrusted network JSON — narrowed field-by-field via
        // toModelCatalogEntry/toPythonDeps, never cast.
        const d = isRecord(raw) ? raw : {}
        const embeddings = isRecord(d.embeddings) ? d.embeddings : null
        if (!embeddings) return
        const cat = Array.isArray(embeddings.catalog) ? embeddings.catalog.map(toModelCatalogEntry) : []
        setEntry(cat.find(e => e.recommended) || cat[0] || null)
        setDeps(embeddings.python_deps == null ? null : toPythonDeps(embeddings.python_deps))
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  const totalMB = entry
    ? Math.round(((entry.model?.size_bytes || 0) + (entry.tokenizer?.size_bytes || 0)) / (1024 * 1024))
    : null
  const modelName = entry?.name || 'all-MiniLM-L6-v2'

  const enable = async () => {
    setState('downloading')
    setError('')
    try {
      const res = await fetch('/api/models/download', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: entry?.id || 'all-MiniLM-L6-v2' }),
      })
      const raw: unknown = await res.json().catch(() => ({}))
      const data = isRecord(raw) ? raw : {}
      if (!res.ok) throw new Error((typeof data.error === 'string' && data.error) || `Error ${res.status}`)
      setState('done')
    } catch (e: unknown) {
      const msg = isRecord(e) && typeof e.message === 'string' && e.message ? e.message : 'Download failed. You can install the model later in Settings → AI Models.'
      setError(msg)
      setState('error')
    }
  }

  return (
    <div className="text-center">
      <div className="text-4xl mb-2">🔎</div>
      <StepHeader
        title="Enable private AI search?"
        subtitle="Optional — download the on-box embedding model so your assistant can search your mail by meaning, entirely on your box."
      />

      <div className="mx-auto max-w-md text-left rounded-xl border wz-hairline wz-surface px-4 py-3 mb-5">
        <p className="text-sm wz-body leading-relaxed">
          Downloads the recommended <span className="font-mono wz-body">{modelName}</span> model
          {totalMB ? <> (~{totalMB} MB)</> : null} from a pinned source and verifies it by checksum.
          Nothing leaves your box — this powers <span className="wz-body">semantic search</span> locally.
        </p>
        <p className="text-xs wz-dim leading-relaxed mt-2">
          Without it, search still works in <span className="wz-body">lexical/degraded mode</span> — you can
          add the model any time in <span className="wz-body">Settings → AI Models</span>.
        </p>
        {deps && !deps.ready && (
          <p className="text-[12px] wz-warn leading-relaxed mt-2">
            Note: running embeddings also needs the vulos-embed Python packages on the box:
            <code className="block mt-1 font-mono wz-warn wz-surface-deep rounded px-2 py-1 select-all">
              {deps.install_hint || 'pip install onnxruntime tokenizers numpy'}
            </code>
            These are never installed automatically.
          </p>
        )}
      </div>

      {state === 'error' && error && (
        <div role="alert" className="mx-auto max-w-md mb-4 bg-danger-soft border border-danger-soft rounded-xl px-4 py-3 text-left">
          <p className="text-sm wz-danger">{error}</p>
        </div>
      )}

      {state === 'done' ? (
        <div role="status" className="mx-auto max-w-md mb-4 bg-success-soft border border-success-soft rounded-xl px-4 py-3 text-left">
          <p className="text-sm wz-ok">Model installed. Semantic search is ready on your box.</p>
        </div>
      ) : null}

      <div className="flex flex-col items-center gap-3">
        {state === 'done' || state === 'error' ? (
          <button onClick={onDone} className="btn-primary px-10 py-3 text-base">
            Continue →
          </button>
        ) : (
          <>
            <button
              onClick={enable}
              disabled={state === 'downloading'}
              className="btn-primary px-10 py-3 text-base flex items-center gap-2"
            >
              {state === 'downloading' ? (
                <>
                  <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />
                  Downloading &amp; verifying…
                </>
              ) : (
                <>Enable private AI search{totalMB ? ` (~${totalMB} MB)` : ''}</>
              )}
            </button>
            <button
              onClick={onDone}
              disabled={state === 'downloading'}
              className="text-sm wz-dim hover:wz-body transition-colors disabled:opacity-40"
            >
              Skip for now
            </button>
          </>
        )}
      </div>
    </div>
  )
}

// ═══════════════════════════════════
// Shared components
// ═══════════════════════════════════
function StepHeader({ title, subtitle }: { title: ReactNode; subtitle?: ReactNode }) {
  return (
    <div className="mb-6">
      {/* h2, and the only h2 on the step: the wizard is one screen per view, so
          each step's title is the document's heading for as long as it is up. */}
      <h2 className="wz-title">{title}</h2>
      {/* Was --text-muted at 14px; now --text-secondary, because this is a
          sentence the user is expected to read, not a caption. */}
      {subtitle && <p className="wz-sub">{subtitle}</p>}
    </div>
  )
}

interface NavBarProps {
  onPrev: () => void
  onNext: () => void
  nextLabel?: string
  skipLabel?: string
  onSkip?: () => void
  nextDisabled?: boolean
}

/**
 * The action bar. STICKY to the bottom of the wizard's scroll container.
 *
 * That is the fix for a measured defect, not styling. At 1440x900 the
 * recovery-kit step rendered 231px of content below the fold, and those 231px
 * held the type-to-confirm field and the "Finish Setup" button — the primary
 * action of the last step of first boot, invisible on an ordinary laptop, with
 * the fullscreen pill sitting across the cut so the page did not even look
 * truncated. Sticky means no step can ever hide its own way forward, at any
 * viewport, without anyone having to remember to check.
 */
function NavBar({ onPrev, onNext, nextLabel, skipLabel, onSkip, nextDisabled }: NavBarProps) {
  const { t } = useI18nTyped()
  const resolvedNext = nextLabel ?? t('nav.continue')
  return (
    <div className="wz-nav">
      <button type="button" onClick={onPrev} className="wz-quiet">
        {t('nav.back')}
      </button>
      <div className="wz-footer-spacer" />
      {skipLabel && (
        <button type="button" onClick={onSkip} className="wz-quiet">
          {skipLabel}
        </button>
      )}
      <button type="button" onClick={onNext} disabled={nextDisabled} className="btn-primary">
        {resolvedNext} →
      </button>
    </div>
  )
}

function SummaryCard({ icon, label, value }: { icon: string; label: string; value: string }) {
  return (
    <div
      className="rounded-xl px-4 py-3 transition-colors"
      style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-default)' }}
    >
      <div className="flex items-center gap-2 mb-1">
        <span className="text-sm">{icon}</span>
        <span className="text-[12px] uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>{label}</span>
      </div>
      <div className="text-sm truncate" style={{ color: 'var(--text-secondary)' }}>{value}</div>
    </div>
  )
}
