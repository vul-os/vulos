import { useState, useEffect, useRef } from 'react'
import QRCode from 'qrcode'
import FullscreenHint from './FullscreenHint'
import ThemeToggle from '../core/ThemeToggle'
import { useTheme } from '../core/ThemeProvider'
import { useI18n } from '../core/i18n'
import MasterKeyReveal from './MasterKeyReveal'

// Vulos is free, self-hosted software — there is no Vulos Cloud account, no
// sign-in/enrolment, and no paid add-on tier. The only account concept is the
// user's own local account on their own box.
// BUNDLE-01: the 'apps' step ("Your apps") reflects the default-everything
// (batteries-included, opt-out) bundling model. It is pre-checked for EVERYTHING
// — a Vulos account handle (enables Mail, still bring-your-own) + the full Ofisi
// productivity suite — and lets a lean user opt out. Inserted after 'account' and
// before 'appearance' so it shows in every new-system flow.
const STEPS = ['welcome', 'IS09_chooser', 'device', 'language', 'timezone', 'network', 'account', 'pin', 'apps', 'appearance', 'identity', 'storage', 'ssh', 'recoverykit', 'ready']

// INIT-09: join-flow step list (used when the chooser picks "Join", or when
// setup mode === 'sync'). Shares indices 0–1 (welcome, IS09_chooser) with
// STEPS so flipping flowType at the chooser keeps `step` aligned; then the
// join-only steps, then the shared pin + ready. Lost in a merge — restored.
const IS09_JOIN_STEPS = ['welcome', 'IS09_chooser', 'IS09_join_storage', 'IS09_syncing', 'pin', 'ready']

const DEVICE_PROFILES = [
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

const PROFILE_ACCENT_CLASSES = {
  blue:    { selected: 'accent-bg-soft accent-border', icon: 'accent-text', check: 'accent-bg' },
  violet:  { selected: 'accent-bg-soft accent-border', icon: 'accent-text', check: 'accent-bg' },
  amber:   { selected: 'bg-warning-soft border-warning-soft', icon: 'text-warning', check: 'bg-warning' },
  emerald: { selected: 'bg-success-soft border-success-soft', icon: 'text-success', check: 'bg-success' },
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

export default function Setup({ onComplete }) {
  const [step, setStep] = useState(0)
  const [config, setConfig] = useState({
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
    // STORE-LOCAL-01: bundle storage mode. Defaults to central-tigris.
    // 'local-minio-sync' opts the bundle into the local-MinIO-with-sync path.
    IS05_storageMode: 'central-tigris',
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
    //   suiteWorkspace — install the Ofisi productivity app (Docs/Sheets/Slides/PDF/Whiteboards)
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
      .then(data => {
        if (data && data.mode === 'normal') {
          // Already set up — shouldn't be here, but complete gracefully
          onComplete()
          return
        }
        if (data && data.mode === 'sync') {
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
  const IS09_baseSteps = IS09_flowType === 'join' ? IS09_JOIN_STEPS : STEPS


  const current = IS09_baseSteps[step]
  const update = (key, val) => setConfig(c => ({ ...c, [key]: val }))

  const goTo = (idx) => {
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

  return (
    <div className="fixed inset-0 overflow-hidden" style={{ background: 'var(--bg-base)' }}>
      {/* Animated background */}
      <div className="absolute inset-0 pointer-events-none">
        <div className="absolute top-[15%] left-[25%] w-[600px] h-[600px] rounded-full opacity-[0.05] blur-[180px] animate-pulse" style={{ animationDuration: '8s', background: 'var(--accent)' }} />
        <div className="absolute bottom-[15%] right-[15%] w-[500px] h-[500px] rounded-full opacity-[0.04] blur-[180px] animate-pulse" style={{ animationDuration: '12s', background: 'color-mix(in srgb, var(--accent) 60%, #a855f7)' }} />
        <div className="absolute top-[50%] left-[60%] w-[300px] h-[300px] rounded-full opacity-[0.02] blur-[120px] animate-pulse" style={{ animationDuration: '10s', background: 'color-mix(in srgb, var(--accent) 40%, #06b6d4)' }} />
      </div>

      <div className="relative h-full flex flex-col">
        {/* Header: step progress + theme toggle */}
        <header className="shrink-0 safe-pt safe-px">
          <div className="mx-auto w-full max-w-xl px-6 pt-5 pb-2 flex items-center gap-4">
            <WizardProgress steps={IS09_baseSteps} step={step} onJump={goTo} />
            <ThemeToggle />
          </div>
        </header>

        {/* Scrollable content region — centers when there's room, scrolls when not */}
        <div className="flex-1 overflow-y-auto overflow-x-hidden safe-px">
          <div className="min-h-full flex flex-col items-center justify-center px-6 py-8">
            <div
              className={`w-full max-w-xl transition-all duration-300 ${transitioning ? 'opacity-0 translate-y-3' : 'opacity-100 translate-y-0'}`}
              style={{ transitionTimingFunction: 'var(--ease-out)' }}
            >
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
            {current === 'recoverykit' && <IS05_RecoveryKitStep config={config} update={update} onNext={next} onPrev={prev} />}
            {/* Join-flow steps */}
            {current === 'IS09_join_storage' && (
              <IS09_JoinConnectStorageStep config={config} update={update} onNext={next} onPrev={prev} />
            )}
            {current === 'IS09_syncing' && (
              <IS09_SyncingStep onNext={next} onComplete={onComplete} />
            )}
            {/* Shared steps (pin + ready used by both flows) */}
            {current === 'pin' && <PinStep config={config} update={update} onNext={next} onPrev={prev} />}
            {current === 'ready' && <ReadyStep config={config} onFinish={finish} onPrev={prev} />}
            </div>
          </div>
        </div>

        {/* Footer: fullscreen hint */}
        <footer className="shrink-0 safe-pb safe-px">
          <div className="flex justify-center pb-4 pt-1">
            <FullscreenHint />
          </div>
        </footer>
      </div>
    </div>
  )
}

// ═══════════════════════════════════
// Wizard progress — a slim segmented bar that scales to any step count and
// stays legible on a phone (18 dots would overflow). Completed segments are
// clickable to jump back; the future is dimmed and inert.
// ═══════════════════════════════════
function WizardProgress({ steps, step, onJump }) {
  const total = steps.length
  return (
    <div className="flex-1 flex items-center gap-3 min-w-0">
      <div
        className="flex-1 flex items-center gap-1"
        role="progressbar"
        aria-valuenow={step + 1}
        aria-valuemin={1}
        aria-valuemax={total}
        aria-label={`Setup step ${step + 1} of ${total}`}
      >
        {steps.map((_, i) => {
          const done = i < step
          const active = i === step
          return (
            <button
              key={i}
              type="button"
              onClick={() => done && onJump(i)}
              aria-label={`Go to step ${i + 1}`}
              aria-current={active ? 'step' : undefined}
              disabled={!done}
              className={`h-1.5 flex-1 rounded-full transition-all duration-500 ${
                done ? 'cursor-pointer hover:opacity-80' : active ? '' : ''
              }`}
              style={{
                background: done || active ? 'var(--accent)' : 'var(--border-strong)',
                opacity: done ? 0.55 : active ? 1 : 1,
                boxShadow: active ? '0 0 12px color-mix(in srgb, var(--accent) 55%, transparent)' : 'none',
              }}
            />
          )
        })}
      </div>
      <span
        className="text-[11px] font-mono tabular-nums shrink-0 whitespace-nowrap"
        style={{ color: 'var(--text-muted)' }}
      >
        {String(step + 1).padStart(2, '0')}
        <span style={{ color: 'var(--text-ghost)' }}> / {String(total).padStart(2, '0')}</span>
      </span>
    </div>
  )
}

// ═══════════════════════════════════
// Welcome
// ═══════════════════════════════════
function WelcomeStep({ onNext }) {
  const { t } = useI18n()
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
        <div className="text-6xl font-extralight tracking-[0.22em] mb-4" style={{ color: 'var(--text-primary)' }}>vula</div>
        <div className="h-px w-20 mx-auto mb-4" style={{ background: 'linear-gradient(to right, transparent, var(--accent), transparent)' }} />
        <p className="text-lg sm:text-xl font-light" style={{ color: 'var(--text-tertiary)' }}>{t('setup.welcome.tagline')}</p>
        <p className="text-sm mt-2 italic" style={{ color: 'var(--text-faint)' }}>{t('setup.welcome.zulu')}</p>
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
function DeviceStep({ config, update, onNext, onPrev }) {
  const [loading, setLoading] = useState(true)
  const [detected, setDetected] = useState(null)

  useEffect(() => {
    let cancelled = false
    fetch('/api/device-profile')
      .then(r => r.json())
      .then(data => {
        if (cancelled) return
        const suggested = data?.suggested || data?.profile || null
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
                  : 'bg-neutral-900/50 border-neutral-800/50 text-neutral-400 hover:border-neutral-700 hover:text-neutral-200'}`}
            >
              <span className={isSelected ? ac.icon : 'text-neutral-500'}>
                {profile.icon}
              </span>
              <div>
                <div className="text-sm font-medium leading-snug">{profile.label}</div>
                <div className="text-[11px] mt-0.5 leading-snug" style={{ color: 'var(--text-muted)' }}>{profile.desc}</div>
              </div>
              {detected === profile.id && !isSelected && (
                <div className="absolute top-2 right-2 text-[9px] font-semibold tracking-wider text-neutral-500 uppercase">
                  Detected
                </div>
              )}
              {detected === profile.id && isSelected && (
                <div className="absolute top-2 left-2 text-[9px] font-semibold tracking-wider text-neutral-400 uppercase">
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
function IS09_NewJoinChooserStep({ onChooseNew, onChooseJoin, onPrev }) {
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
          className="group flex flex-col items-center gap-3 px-6 py-7 rounded-2xl border-2 border-neutral-800/60 bg-neutral-900/50 text-left hover-accent-border hover-accent-bg-soft transition-all"
        >
          <div className="w-14 h-14 rounded-2xl accent-bg-soft accent-bg-hover flex items-center justify-center transition-colors">
            <svg viewBox="0 0 24 24" className="w-7 h-7 accent-text" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
              <circle cx="12" cy="12" r="9" />
              <path d="M12 8v8M8 12h8" />
            </svg>
          </div>
          <div>
            <div className="text-base font-semibold text-neutral-100 mb-1">New System</div>
            <div className="text-sm text-neutral-500">Set up this device from scratch with a new identity and storage.</div>
          </div>
        </button>
        {/* Join existing card */}
        <button
          onClick={onChooseJoin}
          className="group flex flex-col items-center gap-3 px-6 py-7 rounded-2xl border-2 border-neutral-800/60 bg-neutral-900/50 text-left hover-accent-border hover-accent-bg-soft transition-all"
        >
          <div className="w-14 h-14 rounded-2xl accent-bg-soft flex items-center justify-center transition-colors">
            <svg viewBox="0 0 24 24" className="w-7 h-7 accent-text" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
              <path d="M17 21v-2a4 4 0 00-4-4H5a4 4 0 00-4 4v2" />
              <circle cx="9" cy="7" r="4" />
              <path d="M23 21v-2a4 4 0 00-3-3.87M16 3.13a4 4 0 010 7.75" />
            </svg>
          </div>
          <div>
            <div className="text-base font-semibold text-neutral-100 mb-1">Join Existing</div>
            <div className="text-sm text-neutral-500">Connect to an existing Vulos OS cluster by providing storage credentials.</div>
          </div>
        </button>
      </div>
      <div className="flex justify-start pt-4 border-t border-neutral-800/30">
        <button onClick={onPrev} className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors">
          ← Back
        </button>
      </div>
    </div>
  )
}

// ═══════════════════════════════════
// INIT-09: Join — Connect Storage
// ═══════════════════════════════════
function IS09_JoinConnectStorageStep({ onNext, onPrev }) {
  const [IS09_s3Bucket, IS09_setS3Bucket] = useState('')
  const [IS09_s3Region, IS09_setS3Region] = useState('')
  const [IS09_s3AccessKey, IS09_setS3AccessKey] = useState('')
  const [IS09_s3SecretKey, IS09_setS3SecretKey] = useState('')
  const [IS09_passphrase, IS09_setPassphrase] = useState('')
  const [IS09_joining, IS09_setJoining] = useState(false)
  const [IS09_error, IS09_setError] = useState('')

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
        const data = await res.json().catch(() => ({}))
        IS09_setError(data.error || `Unexpected error (${res.status}). Please retry.`)
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
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">S3 Bucket <span className="text-danger">*</span></label>
            <input
              value={IS09_s3Bucket}
              onChange={e => IS09_setS3Bucket(e.target.value.trim())}
              placeholder="my-vula-backup"
              autoFocus
              className="input text-sm py-2.5"
            />
          </div>
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Region</label>
            <input
              value={IS09_s3Region}
              onChange={e => IS09_setS3Region(e.target.value.trim())}
              placeholder="us-east-1"
              className="input text-sm py-2.5"
            />
          </div>
        </div>
        <div>
          <label className="block text-xs text-neutral-500 mb-1.5">Access Key <span className="text-danger">*</span></label>
          <input
            value={IS09_s3AccessKey}
            onChange={e => IS09_setS3AccessKey(e.target.value.trim())}
            placeholder="AKIAIOSFODNN7EXAMPLE"
            className="input text-sm py-2.5 font-mono"
          />
        </div>
        <div>
          <label className="block text-xs text-neutral-500 mb-1.5">Secret Key <span className="text-danger">*</span></label>
          <input
            type="password"
            value={IS09_s3SecretKey}
            onChange={e => IS09_setS3SecretKey(e.target.value)}
            placeholder="Secret access key"
            className="input text-sm py-2.5 font-mono"
          />
        </div>
        <div>
          <label className="block text-xs text-neutral-500 mb-1.5">Encryption Passphrase <span className="text-danger">*</span></label>
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
            <p className="text-sm text-danger">{IS09_error}</p>
            {IS09_error.includes('not yet available') && (
              <button
                onClick={IS09_handleJoin}
                className="self-start text-xs text-danger underline underline-offset-2 hover:opacity-80"
              >
                Retry
              </button>
            )}
          </div>
        )}
      </div>
      <div className="flex items-center justify-between mt-6 pt-4 border-t border-neutral-800/30">
        <button onClick={onPrev} className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors">
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

function IS09_SyncingStep({ onNext, onComplete }) {
  const [IS09_phase, IS09_setPhase] = useState('init')
  const [IS09_phaseIdx, IS09_setPhaseIdx] = useState(0)
  const [IS09_error, IS09_setError] = useState('')
  const [IS09_done, IS09_setDone] = useState(false)
  const [IS09_bgMode, IS09_setBgMode] = useState(false)
  const IS09_pollRef = useRef(null)

  const IS09_poll = async () => {
    try {
      // Try /api/setup/join/status first, fall back to /api/setup/mode
      let data = null
      const statusRes = await fetch('/api/setup/join/status')
      if (statusRes.ok) {
        data = await statusRes.json()
      } else if (statusRes.status === 404) {
        const modeRes = await fetch('/api/setup/mode')
        if (modeRes.ok) {
          const modeData = await modeRes.json()
          data = { phase: modeData.sync_state?.phase || 'init', done: modeData.mode !== 'sync' }
        }
      }
      if (!data) return

      const currentPhase = data.phase || 'init'
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
        <p className="text-sm text-neutral-500">Setup will complete automatically when sync finishes.</p>
      </div>
    )
  }

  return (
    <div className="text-center">
      <div className="mb-6 flex flex-col items-center">
        <div className="w-16 h-16 rounded-2xl accent-bg-soft flex items-center justify-center mb-4">
          {IS09_done ? (
            <svg viewBox="0 0 24 24" className="w-8 h-8 text-success" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M20 6L9 17l-5-5" />
            </svg>
          ) : (
            <svg viewBox="0 0 24 24" className="w-8 h-8 accent-text animate-spin" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" style={{ animationDuration: '2s' }}>
              <path d="M21 12a9 9 0 11-6.219-8.56" />
            </svg>
          )}
        </div>
        <h2 className="text-2xl font-light text-neutral-100">
          {IS09_done ? 'Sync complete' : 'Syncing your data'}
        </h2>
        <p className="text-sm text-neutral-500 mt-1">
          {IS09_done
            ? 'Everything has been restored. Continue to set your PIN.'
            : 'Restoring your identity and data from storage...'}
        </p>
      </div>

      {/* Phase progress bar */}
      <div className="mb-4">
        <div className="flex justify-between text-xs text-neutral-600 mb-1.5">
          <span>{IS09_SYNC_PHASES[IS09_phaseIdx]?.label || 'Initialising'}</span>
          <span>{Math.round(IS09_progress)}%</span>
        </div>
        <div className="h-1.5 w-full bg-neutral-800 rounded-full overflow-hidden">
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
                ${isDone ? 'bg-success-soft text-success' : isCurrent ? 'accent-bg-soft accent-text' : 'bg-neutral-800 text-neutral-600'}`}>
                {isDone ? '✓' : isCurrent ? '·' : '○'}
              </span>
              <span className={`text-sm ${isDone ? 'text-neutral-400' : isCurrent ? 'text-neutral-200' : 'text-neutral-600'}`}>
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
          <p className="text-sm text-danger">{IS09_error}</p>
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
            className="text-sm text-neutral-500 hover:text-neutral-300 transition-colors underline underline-offset-2"
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
function LanguageStep({ config, update, onNext, onPrev }) {
  const { t, setLocale } = useI18n()
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
                ? 'accent-bg-soft accent-border text-neutral-100 border'
                : 'bg-neutral-900/50 border border-neutral-800/50 text-neutral-400 hover:border-neutral-700 hover:text-neutral-200'}`}
          >
            <span className="text-xl">{lang.flag}</span>
            <div>
              <div className="text-sm font-medium">{lang.native}</div>
              <div className="text-xs text-neutral-500">{lang.name}</div>
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
function TimezoneStep({ config, update, onNext, onPrev }) {
  const { t } = useI18n()
  const selected = TIMEZONES.find(tz => tz.id === config.timezone)

  return (
    <div>
      <StepHeader title={t('setup.timezone.title')} subtitle={selected ? `${selected.label} (${selected.offset})` : t('setup.timezone.subtitle_none')} />

      {/* World map */}
      <div className="relative w-full aspect-[2/1] bg-neutral-900/50 rounded-2xl border border-neutral-800/50 overflow-hidden mb-4">
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
          <div key={i} className="absolute top-0 bottom-0 w-px bg-neutral-800/30" style={{ left: `${(i / 24) * 100}%` }} />
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
                : 'bg-neutral-600 border-neutral-500 hover-accent-bg hover-accent-border group-hover:scale-125'}`}
            />
            {/* Label (shows on hover or when selected) */}
            <div className={`absolute left-1/2 -translate-x-1/2 mt-1 whitespace-nowrap text-[10px] font-medium transition-opacity
              ${config.timezone === tz.id ? 'opacity-100 accent-text' : 'opacity-0 group-hover:opacity-100 text-neutral-400'}`}>
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
                : 'bg-neutral-900/50 text-neutral-500 border border-neutral-800/50 hover:text-neutral-300'}`}
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
function NetworkStep({ config, update, onNext, onPrev }) {
  const { t } = useI18n()
  const [networks, setNetworks] = useState(null)
  const [scanning, setScanning] = useState(false)
  const [showPassword, setShowPassword] = useState(false)

  const scan = async () => {
    setScanning(true)
    try {
      const res = await fetch('/api/wifi/scan')
      const data = await res.json()
      setNetworks(Array.isArray(data) ? data : [])
    } catch { setNetworks([]) }
    setScanning(false)
  }

  const signalIcon = (dbm) => {
    if (dbm > -50) return '████'
    if (dbm > -60) return '███░'
    if (dbm > -70) return '██░░'
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
            ? 'bg-neutral-800 text-neutral-500'
            : 'bg-neutral-900/80 border border-neutral-700/50 text-neutral-300 hover-accent-border hover-accent-text'}`}
      >
        {scanning ? (
          <span className="flex items-center justify-center gap-2">
            <span className="w-4 h-4 border-2 border-neutral-600 rounded-full animate-spin" style={{ borderTopColor: 'var(--accent)' }} />
            {t('setup.network.scanning')}
          </span>
        ) : networks ? t('setup.network.scan_again') : t('setup.network.scan')}
      </button>

      {/* Network list */}
      {networks && (
        <div className="max-h-[35vh] overflow-y-auto rounded-xl border border-neutral-800/50 mb-4">
          {networks.length === 0 && (
            <div className="p-4 text-sm text-neutral-600 text-center">{t('setup.network.no_networks')}</div>
          )}
          {networks.map((n, i) => (
            <button
              key={n.bssid || n.ssid || i}
              onClick={() => { update('wifiSSID', n.ssid); setShowPassword(true) }}
              className={`w-full flex items-center gap-3 px-4 py-3 text-left border-b border-neutral-800/30 transition-colors
                ${config.wifiSSID === n.ssid
                  ? 'accent-bg-soft text-neutral-100'
                  : 'text-neutral-300 hover:bg-neutral-800/40'}`}
            >
              <span className="text-[10px] font-mono text-neutral-500 w-10">{signalIcon(n.signal)}</span>
              <div className="flex-1 min-w-0">
                <div className="text-sm truncate">{n.ssid || '(hidden)'}</div>
                <div className="text-[10px] text-neutral-600">{n.band || '2.4GHz'} · {n.security || 'Open'}</div>
              </div>
              {config.wifiSSID === n.ssid && <span className="accent-text text-xs">{t('setup.network.selected')}</span>}
            </button>
          ))}
        </div>
      )}

      {/* Password input */}
      {config.wifiSSID && showPassword && (
        <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl p-4 mb-4 animate-[fadeIn_0.2s_ease-out]">
          <div className="flex items-center gap-2 mb-2">
            <span className="text-sm text-neutral-300">{config.wifiSSID}</span>
            <button onClick={() => { update('wifiSSID', ''); setShowPassword(false) }} className="text-xs text-neutral-600 hover:text-neutral-400">{t('setup.network.change')}</button>
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

function AccountStep({ config, update, onNext, onPrev }) {
  const { t } = useI18n()
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
          <label className="block text-xs text-neutral-500 mb-1.5">{t('setup.account.name_label')}</label>
          <input
            value={config.displayName}
            onChange={e => update('displayName', e.target.value)}
            placeholder={t('setup.account.name_placeholder')}
            autoFocus
            className="input text-base py-3"
          />
        </div>
        <div>
          <label className="block text-xs text-neutral-500 mb-1.5">{t('setup.account.username_label')}</label>
          <input
            value={config.username}
            onChange={e => update('username', e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ''))}
            placeholder={t('setup.account.username_placeholder')}
            className="input text-base py-3 font-mono"
          />
        </div>
        <div>
          <label className="block text-xs text-neutral-500 mb-1.5">{t('setup.account.password_label')}</label>
          <input
            type="password"
            value={config.password}
            onChange={e => update('password', e.target.value)}
            placeholder={t('setup.account.password_placeholder')}
            className="input text-base py-3"
          />
        </div>
      </div>

      {error && <p className="text-sm text-danger mt-2">{error}</p>}

      <NavBar onPrev={onPrev} onNext={handleNext} />
    </div>
  )
}

// ═══════════════════════════════════
// PIN
// ═══════════════════════════════════
function PinStep({ config, update, onNext, onPrev }) {
  const { t } = useI18n()
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

  return (
    <div className="flex flex-col items-center justify-center h-full px-6 max-w-sm mx-auto">
      <div className="w-12 h-12 rounded-2xl bg-warning-soft flex items-center justify-center mb-4">
        <svg viewBox="0 0 24 24" className="w-6 h-6 text-warning" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
          <rect x="3" y="11" width="18" height="11" rx="2" />
          <path d="M7 11V7a5 5 0 0110 0v4" />
          <circle cx="12" cy="16.5" r="1.5" fill="currentColor" />
        </svg>
      </div>
      <h2 className="text-xl font-semibold text-neutral-100 text-center">{t('setup.pin.title')}</h2>
      <p className="text-sm text-neutral-500 text-center mt-2 mb-6">
        {t('setup.pin.subtitle')}
      </p>

      {error && <p className="text-sm text-danger mb-3">{error}</p>}

      <input
        type="password"
        inputMode="numeric"
        pattern="[0-9]*"
        value={config.pin}
        onChange={e => { update('pin', e.target.value.replace(/\D/g, '')); setError('') }}
        placeholder={t('setup.pin.placeholder')}
        maxLength={8}
        className="w-full rounded-xl px-4 py-3 text-center text-lg tracking-[0.3em] outline-none placeholder:tracking-normal placeholder:text-sm mb-3 transition-colors focus:border-[color-mix(in_srgb,var(--status-warning)_55%,transparent)]"
        style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border-default)', color: 'var(--text-primary)' }}
      />

      {config.pin && (
        <>
          <input
            type="password"
            inputMode="numeric"
            pattern="[0-9]*"
            value={confirm}
            onChange={e => { setConfirm(e.target.value.replace(/\D/g, '')); setError('') }}
            placeholder={t('setup.pin.confirm_placeholder')}
            maxLength={8}
            aria-describedby={confirm ? 'pin-confirm-msg' : undefined}
            aria-invalid={(confirm && config.pin !== confirm) || undefined}
            className="w-full rounded-xl px-4 py-3 text-center text-lg tracking-[0.3em] outline-none placeholder:tracking-normal placeholder:text-sm mb-1 transition-colors focus:border-[color-mix(in_srgb,var(--status-warning)_55%,transparent)]"
            style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border-default)', color: 'var(--text-primary)' }}
          />
          {confirm && (
            <p
              id="pin-confirm-msg"
              role={config.pin !== confirm ? 'alert' : undefined}
              className={`text-[11px] mb-3 flex items-center gap-1 self-start ${config.pin !== confirm ? 'text-danger' : 'text-success'}`}
            >
              <span aria-hidden="true">{config.pin !== confirm ? '✕' : '✓'}</span>
              {config.pin !== confirm ? t('setup.pin.error_match') : t('setup.pin.match')}
            </p>
          )}
        </>
      )}

      <div className="flex gap-3 mt-4 w-full">
        <button onClick={onPrev} className="flex-1 py-3 rounded-xl text-sm font-medium text-neutral-400 bg-neutral-800/60 hover:bg-neutral-800 transition-colors">
          {t('setup.pin.back')}
        </button>
        <button onClick={() => { update('pin', ''); onNext() }} className="py-3 px-5 rounded-xl text-sm text-neutral-500 hover:text-neutral-300 transition-colors">
          {t('setup.pin.skip')}
        </button>
        <button onClick={handleNext} disabled={config.pin && config.pin.length < 4} className="flex-1 py-3 rounded-xl text-sm font-semibold text-white bg-warning hover:opacity-90 disabled:opacity-40 transition-opacity">
          {config.pin ? t('setup.pin.set') : t('setup.pin.skip')}
        </button>
      </div>
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
// Calendar/Contacts widgets) plus the owned productivity app (Ofisi/Docs,
// which now includes whiteboards as a document type). A lean user (e.g. a
// gamer) can OPT OUT here:
//   - uncheck productivity apps → drops Ofisi/Docs
//   - uncheck Mail              → drops the Mail connector
// There is no "Workspace" shell — the OS IS the shell. Files, Calendar and
// Contacts are always present (Calendar/Contacts degrade to "Connect Mail" when
// no account is linked). The persisted flag is still `workspace` for
// backend-contract compatibility.
//
// On advance we persist the choice to POST /api/setup/apps so the launcher hides
// the tiles the user opted out of. Best-effort: a failed write just means the
// batteries-included default (everything shown) — never a broken install.
function AppsStep({ config, update, onNext, onPrev }) {
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

  const OptRow = ({ checked, onToggle, title, desc, accent }) => (
    <button
      type="button"
      onClick={onToggle}
      className={`w-full flex items-start gap-3 rounded-2xl border-2 px-4 py-4 text-left transition-all
        ${checked
          ? `${accent} shadow-sm`
          : 'border-neutral-800/60 bg-neutral-900/40 hover:border-neutral-700'}`}
    >
      {/* Checkbox */}
      <span className={`mt-0.5 flex-shrink-0 w-5 h-5 rounded-md flex items-center justify-center border-2 transition-colors
        ${checked ? 'accent-bg accent-border' : 'border-neutral-600 bg-transparent'}`}>
        {checked && (
          <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 text-white">
            <path d="M3.5 8l3 3 6-6" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          </svg>
        )}
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium text-neutral-100">{title}</div>
        <p className="text-xs text-neutral-500 leading-relaxed mt-0.5">{desc}</p>
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
          title="Install the productivity app — Ofisi"
          desc="Ofisi — Docs, Sheets, Slides, PDF and Whiteboards. Uncheck for a lean OS without the productivity app. Files, Calendar and Contacts are always included."
          accent="accent-border accent-bg-soft"
        />
      </div>

      {minimal && (
        <div className="mb-4 rounded-xl bg-neutral-900/50 border border-neutral-800/50 px-4 py-3">
          <p className="text-xs text-neutral-500 leading-relaxed">
            <span className="text-neutral-400 font-medium">Minimal OS.</span>{' '}
            You'll get a clean Vulos install without Mail or the productivity apps — great for gaming or a single-purpose device. Everything stays available to add later from the App Hub.
          </p>
        </div>
      )}

      <div className="flex items-center justify-between mt-6 pt-4 border-t border-neutral-800/30">
        <button onClick={onPrev} className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors">
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

function AppearanceStep({ onNext, onPrev }) {
  const { t } = useI18n()
  const { theme, setTheme, nightShiftMode, setNightShiftMode } = useTheme()

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
                ? 'accent-bg-soft border-2 accent-border text-neutral-100 shadow-lg'
                : 'bg-neutral-900/50 border-2 border-neutral-800/50 text-neutral-400 hover:border-neutral-700 hover:text-neutral-200'}`}
          >
            {/* Preview swatch */}
            <div className="w-12 h-12 rounded-xl border border-neutral-700/50 flex items-center justify-center overflow-hidden"
              style={{ background: thm.preview }}>
              <span className={theme === thm.value ? 'accent-text' : 'text-neutral-400'}>{thm.icon}</span>
            </div>
            <div>
              <div className="text-sm font-medium">{thm.label}</div>
              <div className="text-[11px] text-neutral-500 mt-0.5">{thm.desc}</div>
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
      <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl px-4 py-3 mb-2">
        <div className="flex items-center justify-between">
          <div>
            <div className="text-sm text-neutral-300">{t('setup.appearance.night_shift')}</div>
            <div className="text-[11px] text-neutral-600">{t('setup.appearance.night_shift_desc')}</div>
          </div>
          <button
            onClick={() => setNightShiftMode(nightShiftMode === 'off' ? 'auto' : 'off')}
            className={`w-10 h-5 rounded-full transition-colors relative ${nightShiftMode !== 'off' ? 'bg-warning' : 'bg-neutral-700'}`}
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
function IS05_IdentityStep({ config, update, onNext, onPrev }) {
  const [IS05_loading, IS05_setLoading] = useState(true)
  const [IS05_saving, IS05_setSaving] = useState(false)
  const [IS05_error, IS05_setError] = useState('')
  const [IS05_hostnameEdited, IS05_setHostnameEdited] = useState(false)

  const t = (s) => s

  useEffect(() => {
    fetch('/api/identity')
      .then(r => {
        if (!r.ok) throw new Error('not found')
        return r.json()
      })
      .then(data => {
        update('IS05_ulid', data.ulid || data.instance_id || 'auto-generated')
        if (!IS05_hostnameEdited) {
          update('IS05_hostname', data.hostname || '')
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
        <div className="bg-neutral-900/60 border border-neutral-800/50 rounded-xl px-4 py-4">
          <div className="text-[10px] text-neutral-500 uppercase tracking-wider mb-2">{t('Instance ID (ULID)')}</div>
          {IS05_loading ? (
            <div className="h-5 w-48 bg-neutral-800 rounded animate-pulse" />
          ) : (
            <div className="font-mono text-sm accent-text select-all break-all">
              {config.IS05_ulid}
            </div>
          )}
          <div className="text-[10px] text-neutral-600 mt-1">{t('Read-only — cryptographically unique, auto-assigned')}</div>
        </div>

        {/* Hostname input */}
        <div>
          <label className="block text-xs text-neutral-500 mb-1.5">{t('Hostname')}</label>
          <input
            value={config.IS05_hostname}
            onChange={e => {
              IS05_setHostnameEdited(true)
              update('IS05_hostname', e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))
            }}
            placeholder="my-vula-node"
            className="input text-base py-3 font-mono"
          />
          <p className="text-[11px] text-neutral-600 mt-1">{t('Lowercase letters, numbers and hyphens only')}</p>
        </div>

        {IS05_error && <p className="text-sm text-danger">{IS05_error}</p>}
      </div>

      <NavBar onPrev={onPrev} onNext={handleNext} nextLabel={IS05_saving ? t('Saving...') : t('Continue')} />
    </div>
  )
}

// ═══════════════════════════════════
// INIT-05: Storage Step
// ═══════════════════════════════════
function IS05_StorageStep({ config, update, onNext, onPrev }) {
  const [IS05_passphraseConfirm, IS05_setPassphraseConfirm] = useState('')
  const [IS05_error, IS05_setError] = useState('')
  const [IS05_saving, IS05_setSaving] = useState(false)

  const t = (s) => s

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
    // STORE-LOCAL-01: persist the bundle storage mode. Default (central-tigris)
    // is still POSTed so the row is materialised and the dashboard reflects it
    // — the env contract for the default mode is just VULOS_STORAGE_MODE.
    try {
      const modeBody = config.IS05_storageMode === 'local-minio-sync'
        ? {
            mode: 'local-minio-sync',
            minio_endpoint: config.IS05_storageMinioEndpoint,
            minio_region: config.IS05_storageMinioRegion,
            minio_bucket: config.IS05_storageMinioBucket,
            minio_creds_ref: config.IS05_storageMinioCredsRef,
          }
        : { mode: 'central-tigris' }
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
      <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl px-4 py-3 mb-4">
        <div className="flex items-center justify-between">
          <div>
            <div className="text-sm text-neutral-200">{t('Enable Cluster Sync')}</div>
            <div className="text-[11px] text-neutral-600">{t('Shared encrypted storage across cluster nodes')}</div>
          </div>
          <button
            onClick={() => { update('IS05_storageEnabled', !config.IS05_storageEnabled); IS05_setError('') }}
            className={`w-10 h-5 rounded-full transition-colors relative ${config.IS05_storageEnabled ? 'accent-bg' : 'bg-neutral-700'}`}
          >
            <span className={`absolute top-0.5 w-4 h-4 rounded-full bg-white transition-transform ${config.IS05_storageEnabled ? 'left-5' : 'left-0.5'}`} />
          </button>
        </div>
      </div>

      {/* STORE-LOCAL-01: bundle storage-mode selector. Always visible so the
          operator chooses a backend even when cluster-sync is off. */}
      <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl px-4 py-3 mb-4">
        <div className="text-sm text-neutral-200 mb-2">{t('Bundle Storage Mode')}</div>
        <div className="text-[11px] text-neutral-600 mb-2">
          {t('Central Tigris is the default — direct hosted S3. Local MinIO + sync runs a local source-of-truth and replicates between Vulos nodes via the CRDT layer.')}
        </div>
        <select
          value={config.IS05_storageMode}
          onChange={e => update('IS05_storageMode', e.target.value)}
          className="input text-sm py-2"
        >
          <option value="central-tigris">{t('Central Tigris (default — direct hosted S3)')}</option>
          <option value="local-minio-sync">{t('Local MinIO + CRDT sync (opt-in)')}</option>
        </select>

        {config.IS05_storageMode === 'local-minio-sync' && (
          <div className="space-y-2 mt-3 animate-[fadeIn_0.2s_ease-out]">
            <div>
              <label className="block text-[11px] text-neutral-500 mb-1">{t('MinIO endpoint')}</label>
              <input
                value={config.IS05_storageMinioEndpoint}
                onChange={e => update('IS05_storageMinioEndpoint', e.target.value)}
                placeholder="http://127.0.0.1:9000"
                className="input text-sm py-2"
              />
            </div>
            <div>
              <label className="block text-[11px] text-neutral-500 mb-1">{t('Region')}</label>
              <input
                value={config.IS05_storageMinioRegion}
                onChange={e => update('IS05_storageMinioRegion', e.target.value)}
                placeholder="auto"
                className="input text-sm py-2"
              />
            </div>
            <div>
              <label className="block text-[11px] text-neutral-500 mb-1">{t('Bucket')}</label>
              <input
                value={config.IS05_storageMinioBucket}
                onChange={e => update('IS05_storageMinioBucket', e.target.value)}
                placeholder="vulos-bundle"
                className="input text-sm py-2"
              />
            </div>
            <div>
              <label className="block text-[11px] text-neutral-500 mb-1">{t('Credentials reference (file path or secret-store key)')}</label>
              <input
                value={config.IS05_storageMinioCredsRef}
                onChange={e => update('IS05_storageMinioCredsRef', e.target.value)}
                placeholder="/var/lib/vulos/minio/.minio_secret"
                className="input text-sm py-2"
              />
            </div>
            <p className="text-[10px] text-neutral-600">
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
              <label className="text-xs text-neutral-500">{t('Allocated Size')}</label>
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
            <div className="flex justify-between text-[10px] text-neutral-700 mt-1">
              <span>5 GB</span><span>100 GB</span>
            </div>
          </div>

          {/* Password */}
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">{t('Storage Password')}</label>
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
            <label className="block text-xs text-neutral-500 mb-1.5">{t('Encryption Passphrase')}</label>
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

          {IS05_error && <p className="text-sm text-danger">{IS05_error}</p>}
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
function IS05_SSHStep({ config, update, onNext, onPrev }) {
  const [IS05_generating, IS05_setGenerating] = useState(false)
  const [IS05_privateKey, IS05_setPrivateKey] = useState('')
  const [IS05_confirmed, IS05_setConfirmed] = useState(false)
  const [IS05_copied, IS05_setCopied] = useState(false)
  const [IS05_error, IS05_setError] = useState('')
  const [IS05_saving, IS05_setSaving] = useState(false)

  const t = (s) => s

  // Convert ArrayBuffer to base64
  const IS05_bufToB64 = (buf) => {
    const bytes = new Uint8Array(buf)
    let bin = ''
    for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i])
    return btoa(bin)
  }

  // Build OpenSSH Ed25519 public key wire format
  const IS05_buildOpenSSHPubkey = (rawPub) => {
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
  const IS05_buildPrivkeyPEM = async (privKeyRaw) => {
    const pkcs8 = await crypto.subtle.exportKey('pkcs8', privKeyRaw)
    const b64 = IS05_bufToB64(pkcs8)
    const lines = b64.match(/.{1,64}/g) || []
    return `-----BEGIN PRIVATE KEY-----\n${lines.join('\n')}\n-----END PRIVATE KEY-----`
  }

  // SHA-256 fingerprint of raw public key bytes
  const IS05_fingerprint = async (rawPub) => {
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
    } catch (err) {
      IS05_setError(t('Key generation failed. Your browser may not support Ed25519. ') + (err?.message || ''))
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
              ? 'bg-neutral-800 text-neutral-500'
              : 'bg-neutral-900/80 border border-neutral-700/50 text-neutral-300 hover-accent-border hover-accent-text'}`}
        >
          {IS05_generating ? (
            <span className="flex items-center justify-center gap-2">
              <span className="w-4 h-4 border-2 border-neutral-600 rounded-full animate-spin" style={{ borderTopColor: 'var(--accent)' }} />
              {t('Generating keypair...')}
            </span>
          ) : config.IS05_sshPubkey ? t('Regenerate Keypair') : t('Generate Ed25519 Keypair')}
        </button>

        {/* Private key display (shown once) */}
        {IS05_privateKey && (
          <div className="bg-neutral-950 border border-neutral-800 rounded-xl overflow-hidden animate-[fadeIn_0.2s_ease-out]">
            <div className="flex items-center justify-between px-4 py-2 bg-neutral-900/60 border-b border-neutral-800">
              <span className="text-[11px] text-warning font-medium">{t('Private Key — shown once, copy now')}</span>
              <button
                onClick={IS05_copy}
                className={`text-xs px-3 py-1 rounded-lg transition-colors ${IS05_copied ? 'bg-success-soft text-success' : 'bg-neutral-800 text-neutral-400 hover-accent-text'}`}
              >
                {IS05_copied ? t('Copied!') : t('Copy')}
              </button>
            </div>
            <pre className="text-[10px] font-mono text-neutral-400 p-4 overflow-x-auto whitespace-pre-wrap break-all select-all">
              {IS05_privateKey}
            </pre>
          </div>
        )}

        {/* Public key fingerprint */}
        {config.IS05_sshFingerprint && (
          <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl px-4 py-3">
            <div className="text-[10px] text-neutral-500 uppercase tracking-wider mb-1">{t('Public Key Fingerprint')}</div>
            <div className="font-mono text-xs text-success break-all">{config.IS05_sshFingerprint}</div>
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
                ${IS05_confirmed ? 'accent-bg accent-border' : 'border-neutral-600 group-hover:border-neutral-400'}`}>
                {IS05_confirmed && (
                  <svg viewBox="0 0 16 16" className="w-3 h-3 text-white"><path d="M3.5 8l3 3 6-6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none"/></svg>
                )}
              </div>
            </div>
            <span className="text-sm text-neutral-400 group-hover:text-neutral-200 transition-colors leading-snug">
              {t('I have saved this private key in a secure location. I understand it will not be shown again.')}
            </span>
          </label>
        )}

        {IS05_error && <p className="text-sm text-danger">{IS05_error}</p>}
      </div>

      <NavBar
        onPrev={onPrev}
        onNext={handleNext}
        nextLabel={IS05_saving ? t('Saving...') : t('Continue')}
        nextDisabled={IS05_saving || (config.IS05_sshPubkey && IS05_privateKey && !IS05_confirmed)}
      />
    </div>
  )
}

// ═══════════════════════════════════
// INIT-05 + INIT-06: Recovery Kit Step
// ═══════════════════════════════════

// INIT-06: build a versioned kit object from wizard config
function IK06_buildKitObject(config) {
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
async function IK06_sha256hex(obj) {
  const canonical = JSON.stringify(obj)
  const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(canonical))
  return Array.from(new Uint8Array(buf)).map(b => b.toString(16).padStart(2, '0')).join('')
}

// INIT-06: assemble the versioned download payload
async function IK06_buildDownloadPayload(config) {
  const kit = IK06_buildKitObject(config)
  const checksumSha256 = await IK06_sha256hex(kit)
  return {
    schema_version: 1,
    kit,
    checksum_sha256: checksumSha256,
  }
}

// INIT-06: QR canvas component — renders the kit checksum + ULID as a QR code
function IK06_QRCanvas({ content }) {
  const canvasRef = useRef(null)
  const [IK06_qrError, IK06_setQRError] = useState('')

  useEffect(() => {
    if (!content || !canvasRef.current) return
    QRCode.toCanvas(canvasRef.current, content, {
      width: 160,
      margin: 2,
      color: { dark: '#e2e8f0', light: '#0a0a0a' },
      errorCorrectionLevel: 'M',
    }).catch(err => {
      IK06_setQRError(err?.message || 'QR render failed')
    })
  }, [content])

  if (IK06_qrError) {
    return (
      <div className="text-[10px] text-neutral-600 text-center p-2">
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

function IS05_RecoveryKitStep({ config, onNext, onPrev }) {
  const [IS05_confirmText, IS05_setConfirmText] = useState('')
  const [IS05_downloading, IS05_setDownloading] = useState(false)
  const [IS05_downloaded, IS05_setDownloaded] = useState(false)
  const [IS05_error, IS05_setError] = useState('')
  // INIT-06: versioned payload built client-side (fallback when server unavailable)
  const [IK06_payload, IK06_setPayload] = useState(null)
  const [IK06_qrContent, IK06_setQRContent] = useState('')
  const [IK06_buildingPayload, IK06_setBuildingPayload] = useState(true)

  const t = (s) => s

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
      a.download = `vula-recovery-kit-${config.IS05_ulid || 'node'}.json`
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
      IS05_setDownloaded(true)
    } catch (err) {
      IS05_setError(t('Download failed: ') + (err?.message || t('unknown error')))
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
        <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl px-4 py-3">
          <div className="text-[10px] text-neutral-500 uppercase tracking-wider mb-2">{t('Identity')}</div>
          <div className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
            <div>
              <div className="text-neutral-600 mb-0.5">{t('ULID')}</div>
              <div className="font-mono accent-text text-[10px] break-all">{config.IS05_ulid || '—'}</div>
            </div>
            <div>
              <div className="text-neutral-600 mb-0.5">{t('Hostname')}</div>
              <div className="font-mono text-neutral-300 text-[10px]">{config.IS05_hostname || '—'}</div>
            </div>
          </div>
        </div>

        {config.IS05_storageEnabled && (
          <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl px-4 py-3 animate-[fadeIn_0.2s_ease-out]">
            <div className="text-[10px] text-neutral-500 uppercase tracking-wider mb-2">{t('Cluster Storage')}</div>
            <div className="text-xs">
              <div className="text-neutral-600 mb-0.5">{t('Status')}</div>
              <div className="text-success">{t('Enabled')} · {config.IS05_storageSizeGb} GB</div>
              {config.IS05_s3AccessKey && (
                <>
                  <div className="text-neutral-600 mt-2 mb-0.5">{t('S3 Access Key')}</div>
                  <div className="font-mono text-[10px] text-neutral-300 break-all">{config.IS05_s3AccessKey}</div>
                </>
              )}
            </div>
          </div>
        )}

        {/* INIT-06: storage-skipped notice — shown when user skipped storage */}
        {IS06_storageSkipped && (
          <div className="bg-neutral-900/30 border border-neutral-800/30 rounded-xl px-4 py-3 animate-[fadeIn_0.2s_ease-out]">
            <div className="text-[10px] text-neutral-600 uppercase tracking-wider mb-1">{t('Cluster Storage')}</div>
            <div className="text-xs text-neutral-600">{t('Skipped — can be enabled later in Settings')}</div>
          </div>
        )}

        {config.IS05_sshFingerprint && (
          <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl px-4 py-3">
            <div className="text-[10px] text-neutral-500 uppercase tracking-wider mb-2">{t('SSH Access')}</div>
            <div className="text-[10px] font-mono text-success break-all">{config.IS05_sshFingerprint}</div>
          </div>
        )}
      </div>

      {/* INIT-06: Inline QR code — encodes recovery identity token */}
      {!IK06_buildingPayload && IK06_qrContent && (
        <div className="bg-neutral-950 border border-neutral-800/50 rounded-xl p-4 mb-4 flex flex-col items-center gap-2 animate-[fadeIn_0.2s_ease-out]">
          <div className="text-[10px] text-neutral-500 uppercase tracking-wider">{t('Recovery QR — scan to verify identity')}</div>
          <IK06_QRCanvas content={IK06_qrContent} />
          <div className="text-[9px] font-mono text-neutral-700 break-all text-center max-w-[200px]">
            {IK06_qrContent}
          </div>
        </div>
      )}

      {/* INIT-06: Checksum preview */}
      {IK06_payload && (
        <div className="bg-neutral-900/40 border border-neutral-800/30 rounded-xl px-4 py-2 mb-4">
          <div className="text-[10px] text-neutral-600 uppercase tracking-wider mb-1">{t('Kit checksum (SHA-256)')}</div>
          <div className="font-mono text-[10px] text-neutral-500 break-all">{IK06_payload.checksum_sha256}</div>
        </div>
      )}

      {/* Download button */}
      <button
        onClick={IS05_downloadKit}
        disabled={IS05_downloading || IK06_buildingPayload}
        className={`w-full py-3 rounded-xl text-sm font-medium transition-all mb-4
          ${IS05_downloaded
            ? 'bg-success-soft border border-success-soft text-success'
            : IS05_downloading
              ? 'bg-neutral-800 text-neutral-500'
              : 'accent-bg-soft border accent-border accent-text accent-bg-hover'}`}
      >
        {IS05_downloading ? (
          <span className="flex items-center justify-center gap-2">
            <span className="w-4 h-4 border-2 border-neutral-600 rounded-full animate-spin" style={{ borderTopColor: 'var(--accent)' }} />
            {t('Preparing download...')}
          </span>
        ) : IS05_downloaded
          ? t('Recovery Kit Downloaded')
          : t('Download Recovery Kit')}
      </button>

      {IS05_error && <p className="text-sm text-danger mb-3">{IS05_error}</p>}

      {/* Type-to-confirm gate — applies to ALL variants including storage-skipped */}
      <div className="bg-neutral-900/60 border border-neutral-800/50 rounded-xl px-4 py-4 mb-2">
        <p className="text-sm text-neutral-400 mb-3">
          {t('Type ')}
          <code className="bg-neutral-800 text-warning px-1.5 py-0.5 rounded text-xs font-mono">confirm</code>
          {t(' to proceed')}
        </p>
        <input
          value={IS05_confirmText}
          onChange={e => IS05_setConfirmText(e.target.value)}
          placeholder="confirm"
          className={`input py-2 font-mono text-sm ${IS05_canProceed ? 'border-success-soft text-success' : ''}`}
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
function ReadyStep({ config, onFinish, onPrev }) {
  const { t } = useI18n()
  const { theme } = useTheme()
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
          const data = await res.json().catch(() => ({}))
          setError(data.error || 'Failed to create account')
          setCreating(false)
          return
        }
        const data = await res.json().catch(() => ({}))
        // If the server minted a master-key recovery phrase, force the user to save
        // it before setup can complete (Proton-style). We never persist it here.
        if (data.master_recovery_phrase) {
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
  const themeLabels = {
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
        <svg viewBox="0 0 24 24" className="w-8 h-8 text-success" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
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

      {error && <p className="text-sm text-danger mb-4">{error}</p>}

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
          <code className="accent-text text-[11px]">GET /api/recovery/kit</code>
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
export function PrivateAIStep({ onDone }) {
  const [state, setState] = useState('offer') // offer | downloading | done | error
  const [error, setError] = useState('')
  const [entry, setEntry] = useState(null) // recommended catalog entry (for size/name)
  const [deps, setDeps] = useState(null)

  // Load the catalog + python-deps status so the offer shows the true size and an
  // honest note about the one-time python dependencies.
  useEffect(() => {
    let cancelled = false
    fetch('/api/models', { credentials: 'include' })
      .then(r => r.ok ? r.json() : null)
      .then(d => {
        if (cancelled || !d?.embeddings) return
        const cat = d.embeddings.catalog || []
        setEntry(cat.find(e => e.recommended) || cat[0] || null)
        setDeps(d.embeddings.python_deps || null)
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
      const data = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(data.error || `Error ${res.status}`)
      setState('done')
    } catch (e) {
      setError(e.message || 'Download failed. You can install the model later in Settings → AI Models.')
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

      <div className="mx-auto max-w-md text-left rounded-xl border border-neutral-800/50 bg-neutral-900/40 px-4 py-3 mb-5">
        <p className="text-sm text-neutral-400 leading-relaxed">
          Downloads the recommended <span className="font-mono text-neutral-300">{modelName}</span> model
          {totalMB ? <> (~{totalMB} MB)</> : null} from a pinned source and verifies it by checksum.
          Nothing leaves your box — this powers <span className="text-neutral-300">semantic search</span> locally.
        </p>
        <p className="text-xs text-neutral-500 leading-relaxed mt-2">
          Without it, search still works in <span className="text-neutral-400">lexical/degraded mode</span> — you can
          add the model any time in <span className="text-neutral-400">Settings → AI Models</span>.
        </p>
        {deps && !deps.ready && (
          <p className="text-[11px] text-warning leading-relaxed mt-2">
            Note: running embeddings also needs the vula-embed Python packages on the box:
            <code className="block mt-1 font-mono text-warning bg-neutral-950/60 rounded px-2 py-1 select-all">
              {deps.install_hint || 'pip install onnxruntime tokenizers numpy'}
            </code>
            These are never installed automatically.
          </p>
        )}
      </div>

      {state === 'error' && error && (
        <div role="alert" className="mx-auto max-w-md mb-4 bg-danger-soft border border-danger-soft rounded-xl px-4 py-3 text-left">
          <p className="text-sm text-danger">{error}</p>
        </div>
      )}

      {state === 'done' ? (
        <div role="status" className="mx-auto max-w-md mb-4 bg-success-soft border border-success-soft rounded-xl px-4 py-3 text-left">
          <p className="text-sm text-success">Model installed. Semantic search is ready on your box.</p>
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
              className="text-sm text-neutral-500 hover:text-neutral-300 transition-colors disabled:opacity-40"
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
function StepHeader({ title, subtitle }) {
  return (
    <div className="mb-6">
      <h2 className="text-2xl font-light tracking-tight" style={{ color: 'var(--text-primary)' }}>{title}</h2>
      {subtitle && <p className="text-sm mt-1.5 leading-relaxed" style={{ color: 'var(--text-muted)' }}>{subtitle}</p>}
    </div>
  )
}

function NavBar({ onPrev, onNext, nextLabel, skipLabel, onSkip, nextDisabled }) {
  const { t } = useI18n()
  const resolvedNext = nextLabel ?? t('nav.continue')
  return (
    <div className="flex items-center justify-between mt-6 pt-4" style={{ borderTop: '1px solid var(--border-subtle)' }}>
      <button onClick={onPrev} className="text-sm transition-colors hover-accent-text" style={{ color: 'var(--text-muted)' }}>
        {t('nav.back')}
      </button>
      <div className="flex items-center gap-3">
        {skipLabel && (
          <button onClick={onSkip} className="text-sm transition-colors hover-accent-text" style={{ color: 'var(--text-muted)' }}>
            {skipLabel}
          </button>
        )}
        <button onClick={onNext} disabled={nextDisabled} className="btn-primary disabled:opacity-40 disabled:cursor-not-allowed">
          {resolvedNext} →
        </button>
      </div>
    </div>
  )
}

function SummaryCard({ icon, label, value }) {
  return (
    <div
      className="rounded-xl px-4 py-3 transition-colors"
      style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-default)' }}
    >
      <div className="flex items-center gap-2 mb-1">
        <span className="text-sm">{icon}</span>
        <span className="text-[10px] uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>{label}</span>
      </div>
      <div className="text-sm truncate" style={{ color: 'var(--text-secondary)' }}>{value}</div>
    </div>
  )
}
