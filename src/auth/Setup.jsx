import { useState, useEffect, useRef } from 'react'
import QRCode from 'qrcode'
import FullscreenHint from './FullscreenHint'
import ThemeToggle from '../core/ThemeToggle'
import { useTheme } from '../core/ThemeProvider'
import { useI18n } from '../core/i18n'
import PostSignupWizard from './PostSignupWizard'
import VulosAccountStep from '../../apps/setup-wizard/src/steps/VulosAccountStep'
import IntentStep from '../../apps/setup-wizard/src/steps/IntentStep'

// IDENTITY-01: 'cloudAccount' step ("Pick your Vulos username") is inserted after 'account' and before 'pin'.
// 'intent' step ("How will you use Vulos?") is inserted after 'pin' and before 'appearance'.
// The cloudAccount step is mandatory — no skip option in production (frozen invariant).
const STEPS = ['welcome', 'IS09_chooser', 'device', 'language', 'timezone', 'network', 'NETB05_account_choice', 'account', 'cloudAccount', 'pin', 'intent', 'appearance', 'identity', 'storage', 'ssh', 'recoverykit', 'ready']

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
  blue:    { selected: 'bg-blue-600/15 border-blue-500/60 text-white shadow-blue-500/10', icon: 'text-blue-400', check: 'bg-blue-500' },
  violet:  { selected: 'bg-violet-600/15 border-violet-500/60 text-white shadow-violet-500/10', icon: 'text-violet-400', check: 'bg-violet-500' },
  amber:   { selected: 'bg-amber-600/15 border-amber-500/60 text-white shadow-amber-500/10', icon: 'text-amber-400', check: 'bg-amber-500' },
  emerald: { selected: 'bg-emerald-600/15 border-emerald-500/60 text-white shadow-emerald-500/10', icon: 'text-emerald-400', check: 'bg-emerald-500' },
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
    IS05_sshPubkey: '',
    IS05_sshFingerprint: '',
    IS05_s3AccessKey: '',
    IS05_s3SecretKey: '',
    // CLOGIN-01/04: account mode for this install
    CL01_accountMode: 'local', // 'local' | 'cloud' | 'create'
    CL01_cloudEmail: '',
    CL01_cloudPassword: '',
    // CLOGIN-04: create-cloud-account fields
    CL04_createEmail: '',
    CL04_createPassword: '',
    CL04_createConfirm: '',
    CL04_createFullName: '',
    // NETB-05: install-time account choice
    NETB05_choice: '', // 'local' | 'cloud'
    NETB05_clusterPassphrase: '',
    // IDENTITY-01: claimed Vulos account address (populated by VulosAccountStep)
    cloudAccountAddress: '',
    // INTENT: how the user intends to use Vulos — 'none' | 'personal' | 'business'
    intent: 'none',
  })
  const [transitioning, setTransitioning] = useState(false)

  // CLOGIN-05: post-signup wizard state
  const [CL05_showWizard, CL05_setShowWizard] = useState(false)
  const [CL05_wizardEmail, CL05_setWizardEmail] = useState('')
  const [CL05_wizardIsAdmin, CL05_setWizardIsAdmin] = useState(false)

  // INIT-09: flow type — 'new' (default) or 'join'
  const [IS09_flowType, IS09_setFlowType] = useState('new')
  // INIT-09: whether mode check is done
  const [IS09_modeChecked, IS09_setModeChecked] = useState(false)

  // CLOGIN-01: On mount, check whether this device is cloud-enrolled. If so,
  // default the account step to cloud mode so cloud-managed instances default to
  // "Cloud account" in the install wizard without any user action.
  // NOTE: update() is called inside a .then() callback, not synchronously in
  // the effect body, so this does not violate react-hooks/set-state-in-effect.
  useEffect(() => {
    let cancelled = false
    fetch('/api/auth/cloud/status')
      .then(r => r.ok ? r.json() : null)
      .then(data => {
        if (!cancelled && data?.enrolled) {
          update('CL01_accountMode', 'cloud')
        }
      })
      .catch(() => {}) // not enrolled or endpoint absent — stay local
    return () => { cancelled = true }
  }, [])

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

  // NETB05 / IDENTITY-01: for local-only installs, cloudAccount and intent are
  // not applicable (no Vulos Cloud account will be created at setup time).
  // Compute effectiveSteps at render time — never mutate the STEPS constant.
  const IS09_activeSteps = (IS09_flowType !== 'join' && config.NETB05_choice === 'local')
    ? IS09_baseSteps.filter(s => s !== 'cloudAccount' && s !== 'intent')
    : IS09_baseSteps

  const current = IS09_activeSteps[step]
  const update = (key, val) => setConfig(c => ({ ...c, [key]: val }))

  const goTo = (idx) => {
    setTransitioning(true)
    setTimeout(() => { setStep(idx); setTransitioning(false) }, 200)
  }
  const next = () => goTo(Math.min(step + 1, IS09_activeSteps.length - 1))
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
      <div className="fixed inset-0 bg-neutral-950 flex items-center justify-center">
        <span className="text-neutral-600 text-sm">Checking system mode...</span>
      </div>
    )
  }

  return (
    <div className="fixed inset-0 bg-neutral-950 overflow-hidden">
      {/* Animated background */}
      <div className="absolute inset-0 pointer-events-none">
        <div className="absolute top-[15%] left-[25%] w-[600px] h-[600px] rounded-full bg-blue-600 opacity-[0.04] blur-[180px] animate-pulse" style={{ animationDuration: '8s' }} />
        <div className="absolute bottom-[15%] right-[15%] w-[500px] h-[500px] rounded-full bg-violet-600 opacity-[0.04] blur-[180px] animate-pulse" style={{ animationDuration: '12s' }} />
        <div className="absolute top-[50%] left-[60%] w-[300px] h-[300px] rounded-full bg-cyan-600 opacity-[0.02] blur-[120px] animate-pulse" style={{ animationDuration: '10s' }} />
      </div>

      <div className="relative h-full flex flex-col items-center justify-center px-6">
        {/* Theme toggle + fullscreen hint */}
        <div className="absolute top-4 right-4">
          <ThemeToggle />
        </div>
        <div className="absolute bottom-4">
          <FullscreenHint />
        </div>

        {/* Progress dots — hidden during CLOGIN-05 wizard (wizard has its own dots) */}
        {!CL05_showWizard && (
          <div className="absolute top-8 flex gap-2">
            {IS09_activeSteps.map((_, i) => (
              <button
                key={i}
                onClick={() => i < step && goTo(i)}
                className={`w-2 h-2 rounded-full transition-all duration-500
                  ${i === step ? 'bg-blue-500 w-6' : i < step ? 'bg-blue-500/50 cursor-pointer hover:bg-blue-400' : 'bg-neutral-800'}`}
              />
            ))}
          </div>
        )}

        {/* Content */}
        <div className={`w-full max-w-xl transition-all duration-200 ${transitioning ? 'opacity-0 translate-y-4' : 'opacity-100 translate-y-0'}`}>
          {/* CLOGIN-05: post-signup wizard — overlays the normal step flow */}
          {CL05_showWizard ? (
            <PostSignupWizard
              email={CL05_wizardEmail}
              isFleetAdmin={CL05_wizardIsAdmin}
              onComplete={() => {
                CL05_setShowWizard(false)
                next()
              }}
            />
          ) : (
            <>
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
              {current === 'NETB05_account_choice' && (
                <NETB05_AccountChoiceStep
                  config={config}
                  update={update}
                  onNext={next}
                  onPrev={prev}
                  onSignupComplete={(email, isAdmin) => {
                    CL05_setWizardEmail(email)
                    CL05_setWizardIsAdmin(isAdmin)
                    CL05_setShowWizard(true)
                  }}
                />
              )}
              {current === 'account' && (
                <AccountStep
                  config={config}
                  update={update}
                  onNext={next}
                  onPrev={prev}
                  onSignupComplete={(email, isAdmin) => {
                    CL05_setWizardEmail(email)
                    CL05_setWizardIsAdmin(isAdmin)
                    CL05_setShowWizard(true)
                  }}
                />
              )}
              {/* IDENTITY-01: mandatory Vulos account username step — no skip in production */}
              {current === 'cloudAccount' && (
                <VulosAccountStep
                  config={config}
                  update={update}
                  onNext={next}
                  onPrev={prev}
                  customDomain={config.NETB05_choice === 'cloud' ? undefined : undefined}
                />
              )}
              {/* INTENT: Business / Personal intent + Vulos Cloud add-on pitch */}
              {current === 'intent' && (
                <IntentStep
                  config={config}
                  update={update}
                  onNext={next}
                  onPrev={prev}
                />
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
            </>
          )}
        </div>
      </div>
    </div>
  )
}

// ═══════════════════════════════════
// Welcome
// ═══════════════════════════════════
function WelcomeStep({ onNext }) {
  const { t } = useI18n()
  return (
    <div className="text-center">
      <div className="mb-6 flex flex-col items-center">
        <img src="/icon-128.png" alt="Vula OS" className="w-20 h-20 mb-4" />
        <div className="text-5xl font-extralight text-neutral-100 tracking-[0.2em] mb-3">vula</div>
        <div className="h-px w-16 mx-auto bg-gradient-to-r from-transparent via-blue-500 to-transparent mb-3" />
        <p className="text-neutral-500 text-lg font-light">{t('setup.welcome.tagline')}</p>
        <p className="text-neutral-700 text-sm mt-1 italic">{t('setup.welcome.zulu')}</p>
      </div>
      <button onClick={onNext} className="btn-primary px-10 py-3 text-base mt-8">
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
        subtitle={loading ? 'Detecting your device…' : detected ? `Auto-detected: ${DEVICE_PROFILES.find(p => p.id === detected)?.label || detected}` : 'Choose how Vula OS should behave on this device'}
      />

      <div className="grid grid-cols-2 gap-3">
        {DEVICE_PROFILES.map(profile => {
          const isSelected = selected === profile.id
          const ac = PROFILE_ACCENT_CLASSES[profile.accent]
          return (
            <button
              key={profile.id}
              onClick={() => update('deviceProfile', profile.id)}
              className={`relative flex flex-col items-center gap-2 px-4 py-5 rounded-2xl text-center transition-all border-2
                ${isSelected
                  ? `${ac.selected} shadow-lg`
                  : 'bg-neutral-900/50 border-neutral-800/50 text-neutral-400 hover:border-neutral-700 hover:text-neutral-200'}`}
            >
              <span className={isSelected ? ac.icon : 'text-neutral-500'}>
                {profile.icon}
              </span>
              <div>
                <div className="text-sm font-medium leading-snug">{profile.label}</div>
                <div className="text-[11px] text-neutral-500 mt-0.5 leading-snug">{profile.desc}</div>
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
        subtitle="Start fresh or join an existing Vula OS installation"
      />
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mt-2 mb-6">
        {/* New system card */}
        <button
          onClick={onChooseNew}
          className="group flex flex-col items-center gap-3 px-6 py-7 rounded-2xl border-2 border-neutral-800/60 bg-neutral-900/50 text-left hover:border-blue-500/50 hover:bg-blue-600/5 transition-all"
        >
          <div className="w-14 h-14 rounded-2xl bg-blue-600/15 flex items-center justify-center group-hover:bg-blue-600/25 transition-colors">
            <svg viewBox="0 0 24 24" className="w-7 h-7 text-blue-400" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
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
          className="group flex flex-col items-center gap-3 px-6 py-7 rounded-2xl border-2 border-neutral-800/60 bg-neutral-900/50 text-left hover:border-violet-500/50 hover:bg-violet-600/5 transition-all"
        >
          <div className="w-14 h-14 rounded-2xl bg-violet-600/15 flex items-center justify-center group-hover:bg-violet-600/25 transition-colors">
            <svg viewBox="0 0 24 24" className="w-7 h-7 text-violet-400" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
              <path d="M17 21v-2a4 4 0 00-4-4H5a4 4 0 00-4 4v2" />
              <circle cx="9" cy="7" r="4" />
              <path d="M23 21v-2a4 4 0 00-3-3.87M16 3.13a4 4 0 010 7.75" />
            </svg>
          </div>
          <div>
            <div className="text-base font-semibold text-neutral-100 mb-1">Join Existing</div>
            <div className="text-sm text-neutral-500">Connect to an existing Vula OS cluster by providing storage credentials.</div>
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
            <label className="block text-xs text-neutral-500 mb-1.5">S3 Bucket <span className="text-red-400">*</span></label>
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
          <label className="block text-xs text-neutral-500 mb-1.5">Access Key <span className="text-red-400">*</span></label>
          <input
            value={IS09_s3AccessKey}
            onChange={e => IS09_setS3AccessKey(e.target.value.trim())}
            placeholder="AKIAIOSFODNN7EXAMPLE"
            className="input text-sm py-2.5 font-mono"
          />
        </div>
        <div>
          <label className="block text-xs text-neutral-500 mb-1.5">Secret Key <span className="text-red-400">*</span></label>
          <input
            type="password"
            value={IS09_s3SecretKey}
            onChange={e => IS09_setS3SecretKey(e.target.value)}
            placeholder="Secret access key"
            className="input text-sm py-2.5 font-mono"
          />
        </div>
        <div>
          <label className="block text-xs text-neutral-500 mb-1.5">Encryption Passphrase <span className="text-red-400">*</span></label>
          <input
            type="password"
            value={IS09_passphrase}
            onChange={e => IS09_setPassphrase(e.target.value)}
            placeholder="Decryption passphrase for existing backup"
            className="input text-sm py-2.5"
          />
        </div>
        {IS09_error && (
          <div className="flex flex-col gap-2 bg-red-900/20 border border-red-800/40 rounded-xl px-4 py-3">
            <p className="text-sm text-red-400">{IS09_error}</p>
            {IS09_error.includes('not yet available') && (
              <button
                onClick={IS09_handleJoin}
                className="self-start text-xs text-red-300 underline underline-offset-2 hover:text-red-200"
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
        <StepHeader title="Syncing in the background" subtitle="You can use Vula OS while sync continues." />
        <p className="text-sm text-neutral-500">Setup will complete automatically when sync finishes.</p>
      </div>
    )
  }

  return (
    <div className="text-center">
      <div className="mb-6 flex flex-col items-center">
        <div className="w-16 h-16 rounded-2xl bg-violet-600/15 flex items-center justify-center mb-4">
          {IS09_done ? (
            <svg viewBox="0 0 24 24" className="w-8 h-8 text-green-400" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M20 6L9 17l-5-5" />
            </svg>
          ) : (
            <svg viewBox="0 0 24 24" className="w-8 h-8 text-violet-400 animate-spin" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" style={{ animationDuration: '2s' }}>
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
            className="h-full bg-gradient-to-r from-violet-600 to-blue-500 rounded-full transition-all duration-700"
            style={{ width: `${IS09_progress}%` }}
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
              ${isCurrent ? 'bg-violet-600/10 border border-violet-500/20' : ''}
            `}>
              <span className={`flex-shrink-0 w-4 h-4 rounded-full flex items-center justify-center text-[10px]
                ${isDone ? 'bg-green-500/20 text-green-400' : isCurrent ? 'bg-violet-500/20 text-violet-400' : 'bg-neutral-800 text-neutral-600'}`}>
                {isDone ? '✓' : isCurrent ? '·' : '○'}
              </span>
              <span className={`text-sm ${isDone ? 'text-neutral-400' : isCurrent ? 'text-neutral-200' : 'text-neutral-600'}`}>
                {phase.label}
              </span>
              {isCurrent && (
                <span className="ml-auto flex gap-0.5">
                  {[0, 1, 2].map(d => (
                    <span key={d} className="w-1 h-1 bg-violet-500 rounded-full animate-bounce" style={{ animationDelay: `${d * 0.15}s` }} />
                  ))}
                </span>
              )}
            </div>
          )
        })}
      </div>

      {IS09_error && (
        <div className="mb-4 bg-red-900/20 border border-red-800/40 rounded-xl px-4 py-3">
          <p className="text-sm text-red-400">{IS09_error}</p>
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
                ? 'bg-blue-600/20 border border-blue-500/50 text-white'
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
                ? 'bg-blue-500 border-blue-400 scale-150 shadow-lg shadow-blue-500/50'
                : 'bg-neutral-600 border-neutral-500 group-hover:bg-blue-400 group-hover:border-blue-300 group-hover:scale-125'}`}
            />
            {/* Label (shows on hover or when selected) */}
            <div className={`absolute left-1/2 -translate-x-1/2 mt-1 whitespace-nowrap text-[10px] font-medium transition-opacity
              ${config.timezone === tz.id ? 'opacity-100 text-blue-400' : 'opacity-0 group-hover:opacity-100 text-neutral-400'}`}>
              {tz.label}
            </div>
            {/* Pulse ring when selected */}
            {config.timezone === tz.id && (
              <div className="absolute inset-0 w-3 h-3 rounded-full border border-blue-500 animate-ping opacity-30" />
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
                ? 'bg-blue-600/30 text-blue-300 border border-blue-500/50'
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
            : 'bg-neutral-900/80 border border-neutral-700/50 text-neutral-300 hover:border-blue-500/50 hover:text-white'}`}
      >
        {scanning ? (
          <span className="flex items-center justify-center gap-2">
            <span className="w-4 h-4 border-2 border-neutral-600 border-t-blue-500 rounded-full animate-spin" />
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
                  ? 'bg-blue-600/10 text-white'
                  : 'text-neutral-300 hover:bg-neutral-800/40'}`}
            >
              <span className="text-[10px] font-mono text-neutral-500 w-10">{signalIcon(n.signal)}</span>
              <div className="flex-1 min-w-0">
                <div className="text-sm truncate">{n.ssid || '(hidden)'}</div>
                <div className="text-[10px] text-neutral-600">{n.band || '2.4GHz'} · {n.security || 'Open'}</div>
              </div>
              {config.wifiSSID === n.ssid && <span className="text-blue-500 text-xs">{t('setup.network.selected')}</span>}
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
// NETB-05: Install-time account choice — Local-only vs Connect Vulos Cloud
// ═══════════════════════════════════

function NETB05_AccountChoiceStep({ config, update, onNext, onPrev, onSignupComplete }) {
  // 'pick' = top-level choice card; 'local' = local OS account form; 'cloud-login' | 'cloud-create' = cloud sub-forms
  const [view, setView] = useState(() => {
    if (config.NETB05_choice === 'local') return 'local'
    if (config.NETB05_choice === 'cloud') {
      return config.CL01_accountMode === 'create' ? 'cloud-create' : 'cloud-login'
    }
    return 'pick'
  })

  // Auto-fill hostname into displayName / username when entering local form
  const [NB_hostnameLoaded, NB_setHostnameLoaded] = useState(false)
  useEffect(() => {
    if (view !== 'local' || NB_hostnameLoaded) return
    // Use already-fetched IS05_hostname if present; otherwise fall back to /api/identity
    if (config.IS05_hostname) {
      // Suggest hostname as username if username is blank
      if (!config.username) {
        update('username', config.IS05_hostname.toLowerCase().replace(/[^a-z0-9_-]/g, '').slice(0, 32))
      }
      NB_setHostnameLoaded(true)
      return
    }
    fetch('/api/identity')
      .then(r => r.ok ? r.json() : null)
      .then(data => {
        if (data?.hostname && !config.username) {
          update('username', data.hostname.toLowerCase().replace(/[^a-z0-9_-]/g, '').slice(0, 32))
        }
        if (data?.hostname) update('IS05_hostname', data.hostname)
      })
      .catch(() => {})
      .finally(() => NB_setHostnameLoaded(true))
  }, [view]) // eslint-disable-line react-hooks/exhaustive-deps

  const [error, setError] = useState('')
  const [NB_cloudSubmitting, NB_setCloudSubmitting] = useState(false)
  const [NB_serverHint, NB_setServerHint] = useState('')
  const [NB_showClusterJoin, NB_setShowClusterJoin] = useState(false)

  // ── choose local ──────────────────────────────────────────────────────────
  const pickLocal = () => {
    update('NETB05_choice', 'local')
    update('CL01_accountMode', 'local')
    setView('local')
    setError('')
  }

  // ── choose cloud ──────────────────────────────────────────────────────────
  const pickCloud = () => {
    update('NETB05_choice', 'cloud')
    update('CL01_accountMode', config.CL01_accountMode === 'local' ? 'cloud' : config.CL01_accountMode)
    setView(config.CL01_accountMode === 'create' ? 'cloud-create' : 'cloud-login')
    setError('')
  }

  // ── validate local OS account ─────────────────────────────────────────────
  const validateLocal = () => {
    if (!config.username || config.username.length < 2) {
      setError('Username must be at least 2 characters')
      return
    }
    if (!config.password || config.password.length < 4) {
      setError('Password must be at least 4 characters')
      return
    }
    setError('')
    onNext()
  }

  // ── validate cloud sign-in ────────────────────────────────────────────────
  const validateCloudLogin = () => {
    if (!config.CL01_cloudEmail || !config.CL01_cloudEmail.includes('@')) {
      setError('Enter a valid email address')
      return
    }
    if (!config.CL01_cloudPassword || config.CL01_cloudPassword.length < 4) {
      setError('Password is required')
      return
    }
    setError('')
    onNext()
  }

  // ── create cloud account ──────────────────────────────────────────────────
  const handleCloudCreate = async () => {
    NB_setServerHint('')
    setError('')
    if (!config.CL04_createEmail || !config.CL04_createEmail.includes('@')) {
      setError('Enter a valid email address')
      return
    }
    if (config.CL04_createPassword.length < 12) {
      setError('Password must be at least 12 characters')
      return
    }
    if (config.CL04_createPassword !== config.CL04_createConfirm) {
      setError('Passwords do not match')
      return
    }
    if (!config.CL04_createFullName || config.CL04_createFullName.trim().length < 2) {
      setError('Enter your full name')
      return
    }
    NB_setCloudSubmitting(true)
    try {
      const res = await fetch('/api/auth/cloud/signup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          email: config.CL04_createEmail.trim().toLowerCase(),
          password: config.CL04_createPassword,
          full_name: config.CL04_createFullName.trim(),
        }),
      })
      const data = await res.json().catch(() => ({}))
      if (res.ok || res.status === 201) {
        const email = config.CL04_createEmail.trim().toLowerCase()
        const isAdmin = Boolean(data?.fleet_admin)
        update('CL01_cloudEmail', email)
        update('CL01_accountMode', 'cloud')
        if (onSignupComplete) {
          onSignupComplete(email, isAdmin)
        } else {
          onNext()
        }
        return
      }
      NB_setServerHint(data.hint || data.error || 'Sign-up failed — please try again')
    } catch {
      NB_setServerHint('Could not reach Vulos Cloud. Check your network and try again.')
    } finally {
      NB_setCloudSubmitting(false)
    }
  }

  // ─────────────────────────────────────────────────────────────────────────
  // TOP-LEVEL PICKER
  // ─────────────────────────────────────────────────────────────────────────
  if (view === 'pick') {
    return (
      <div>
        <StepHeader
          title="How do you want to manage this device?"
          subtitle="You can always connect to Vulos Cloud later from Settings."
        />

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mb-6">
          {/* Local-only card */}
          <button
            onClick={pickLocal}
            className="group flex flex-col items-start gap-3 px-6 py-7 rounded-2xl border-2 border-neutral-800/60 bg-neutral-900/50 text-left hover:border-blue-500/50 hover:bg-blue-600/5 transition-all"
          >
            <div className="w-14 h-14 rounded-2xl bg-blue-600/15 flex items-center justify-center group-hover:bg-blue-600/25 transition-colors">
              <svg viewBox="0 0 24 24" className="w-7 h-7 text-blue-400" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
                <rect x="2" y="3" width="20" height="14" rx="2" />
                <path d="M8 21h8M12 17v4" />
              </svg>
            </div>
            <div>
              <div className="text-base font-semibold text-neutral-100 mb-1">Local only</div>
              <div className="text-sm text-neutral-500 leading-relaxed">
                Create a local OS account. No external service required — works fully offline and self-hosted.
              </div>
            </div>
            <div className="flex flex-wrap gap-1.5 mt-1">
              {['Offline', 'No account required', 'Full control'].map(tag => (
                <span key={tag} className="text-[10px] font-medium px-2 py-0.5 rounded-full bg-blue-600/10 text-blue-400 border border-blue-500/20">
                  {tag}
                </span>
              ))}
            </div>
          </button>

          {/* Connect Vulos Cloud card */}
          <button
            onClick={pickCloud}
            className="group flex flex-col items-start gap-3 px-6 py-7 rounded-2xl border-2 border-neutral-800/60 bg-neutral-900/50 text-left hover:border-violet-500/50 hover:bg-violet-600/5 transition-all"
          >
            <div className="w-14 h-14 rounded-2xl bg-violet-600/15 flex items-center justify-center group-hover:bg-violet-600/25 transition-colors">
              <svg viewBox="0 0 24 24" className="w-7 h-7 text-violet-400" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
                <path d="M17.5 19H9a7 7 0 110-14 7.5 7.5 0 017.5 7.5" />
                <path d="M17 12h5M19 10l3 2-3 2" />
              </svg>
            </div>
            <div>
              <div className="text-base font-semibold text-neutral-100 mb-1">Connect Vulos Cloud</div>
              <div className="text-sm text-neutral-500 leading-relaxed">
                Link this device to a Vulos Cloud account for remote access, 2FA, and optional cluster sync.
              </div>
            </div>
            <div className="flex flex-wrap gap-1.5 mt-1">
              {['Remote access', 'Sync', '2FA'].map(tag => (
                <span key={tag} className="text-[10px] font-medium px-2 py-0.5 rounded-full bg-violet-600/10 text-violet-400 border border-violet-500/20">
                  {tag}
                </span>
              ))}
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

  // ─────────────────────────────────────────────────────────────────────────
  // LOCAL OS ACCOUNT FORM
  // ─────────────────────────────────────────────────────────────────────────
  if (view === 'local') {
    return (
      <div>
        <StepHeader
          title="Create your local account"
          subtitle={config.IS05_hostname ? `This device: ${config.IS05_hostname}` : 'Set up your OS user — no external service required.'}
        />

        {/* Network name (hostname) — read-only autofill display */}
        {config.IS05_hostname && (
          <div className="flex items-center gap-3 bg-neutral-900/50 border border-neutral-800/50 rounded-xl px-4 py-3 mb-4">
            <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4 text-blue-400 shrink-0">
              <path fillRule="evenodd" d="M2 5a2 2 0 012-2h12a2 2 0 012 2v2a2 2 0 01-2 2H4a2 2 0 01-2-2V5zm14 1a1 1 0 11-2 0 1 1 0 012 0zM2 13a2 2 0 012-2h12a2 2 0 012 2v2a2 2 0 01-2 2H4a2 2 0 01-2-2v-2zm14 1a1 1 0 11-2 0 1 1 0 012 0z" clipRule="evenodd" />
            </svg>
            <div>
              <div className="text-[10px] text-neutral-500 uppercase tracking-wider">Network name (hostname)</div>
              <div className="font-mono text-sm text-blue-300">{config.IS05_hostname}</div>
            </div>
          </div>
        )}

        <div className="space-y-4">
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Full name</label>
            <input
              value={config.displayName}
              onChange={e => { update('displayName', e.target.value); setError('') }}
              placeholder="Ada Lovelace"
              autoFocus
              autoComplete="name"
              className="input text-base py-3"
            />
          </div>
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Username</label>
            <input
              value={config.username}
              onChange={e => { update('username', e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, '')); setError('') }}
              placeholder="ada"
              autoComplete="username"
              className="input text-base py-3 font-mono"
            />
            <p className="text-[11px] text-neutral-600 mt-1">Lowercase letters, numbers, underscore and hyphen</p>
          </div>
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Password</label>
            <input
              type="password"
              value={config.password}
              onChange={e => { update('password', e.target.value); setError('') }}
              placeholder="Choose a strong password"
              autoComplete="new-password"
              className="input text-base py-3"
            />
          </div>
        </div>

        {/* Credentials-separation note */}
        <div className="mt-4 flex items-start gap-2 bg-neutral-900/40 border border-neutral-800/30 rounded-xl px-4 py-3">
          <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4 text-neutral-500 mt-0.5 shrink-0">
            <path fillRule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a.75.75 0 000 1.5h.253a.25.25 0 01.244.304l-.459 2.066A1.75 1.75 0 0010.747 15H11a.75.75 0 000-1.5h-.253a.25.25 0 01-.244-.304l.459-2.066A1.75 1.75 0 009.253 9H9z" clipRule="evenodd" />
          </svg>
          <p className="text-xs text-neutral-500 leading-relaxed">
            This is your <strong className="text-neutral-300">local OS account</strong> only. It is stored on-device and has no connection to any external service.
          </p>
        </div>

        {error && <p className="text-sm text-red-400 mt-3">{error}</p>}

        <div className="flex items-center justify-between mt-6 pt-4 border-t border-neutral-800/30">
          <button onClick={() => { setView('pick'); setError('') }} className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors">
            ← Back
          </button>
          <button onClick={validateLocal} className="btn-primary">
            Continue →
          </button>
        </div>
      </div>
    )
  }

  // ─────────────────────────────────────────────────────────────────────────
  // CLOUD FORMS — sub-tab bar: Sign in / Create account
  // ─────────────────────────────────────────────────────────────────────────
  const cloudTabs = [
    { id: 'cloud-login', label: 'Sign in' },
    { id: 'cloud-create', label: 'Create account' },
  ]

  return (
    <div>
      <StepHeader
        title="Connect Vulos Cloud"
        subtitle="Sign in to an existing account or create a new one."
      />

      {/* Sub-tab strip */}
      <div className="flex rounded-xl bg-neutral-900/70 border border-neutral-800/60 p-1 gap-1 mb-5">
        {cloudTabs.map(tab => (
          <button
            key={tab.id}
            onClick={() => {
              setView(tab.id)
              update('CL01_accountMode', tab.id === 'cloud-create' ? 'create' : 'cloud')
              setError('')
              NB_setServerHint('')
            }}
            className={`flex-1 py-2 rounded-lg text-xs font-medium transition-all
              ${view === tab.id
                ? 'bg-neutral-800 text-neutral-100 shadow-sm'
                : 'text-neutral-500 hover:text-neutral-300'}`}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {/* ── Sign-in form ── */}
      {view === 'cloud-login' && (
        <div className="space-y-4 animate-[fadeIn_0.15s_ease-out]">
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Email</label>
            <input
              type="email"
              value={config.CL01_cloudEmail}
              onChange={e => { update('CL01_cloudEmail', e.target.value); setError('') }}
              placeholder="you@example.com"
              autoFocus
              autoComplete="email"
              className="input text-base py-3"
            />
          </div>
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Password</label>
            <input
              type="password"
              value={config.CL01_cloudPassword}
              onChange={e => { update('CL01_cloudPassword', e.target.value); setError('') }}
              placeholder="Password"
              autoComplete="current-password"
              className="input text-base py-3"
            />
          </div>
          <p className="text-[11px] text-neutral-600">2FA will be requested after setup if enabled on your account.</p>
        </div>
      )}

      {/* ── Create-account form ── */}
      {view === 'cloud-create' && (
        <div className="space-y-4 animate-[fadeIn_0.15s_ease-out]">
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Full name</label>
            <input
              value={config.CL04_createFullName}
              onChange={e => { update('CL04_createFullName', e.target.value); setError('') }}
              placeholder="Ada Lovelace"
              autoFocus
              autoComplete="name"
              className="input text-base py-3"
            />
          </div>
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Email</label>
            <input
              type="email"
              value={config.CL04_createEmail}
              onChange={e => { update('CL04_createEmail', e.target.value); setError('') }}
              placeholder="you@example.com"
              autoComplete="email"
              className="input text-base py-3"
            />
          </div>
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">
              Password
              <span className="ml-1.5 text-neutral-600 font-normal">(12+ characters)</span>
            </label>
            <input
              type="password"
              value={config.CL04_createPassword}
              onChange={e => { update('CL04_createPassword', e.target.value); setError(''); NB_setServerHint('') }}
              placeholder="Choose a strong password"
              autoComplete="new-password"
              className="input text-base py-3"
            />
            <CL04_PasswordStrength password={config.CL04_createPassword} />
          </div>
          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Confirm password</label>
            <input
              type="password"
              value={config.CL04_createConfirm}
              onChange={e => { update('CL04_createConfirm', e.target.value); setError('') }}
              placeholder="Re-enter password"
              autoComplete="new-password"
              className={`input text-base py-3 ${
                config.CL04_createConfirm && config.CL04_createPassword !== config.CL04_createConfirm
                  ? 'border-red-500/60'
                  : config.CL04_createConfirm && config.CL04_createPassword === config.CL04_createConfirm
                    ? 'border-green-500/40'
                    : ''
              }`}
            />
          </div>
          {NB_serverHint && (
            <div className="flex items-start gap-2 bg-amber-900/20 border border-amber-700/30 rounded-xl px-4 py-3">
              <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4 text-amber-400 mt-0.5 shrink-0">
                <path fillRule="evenodd" d="M8.485 2.495c.673-1.167 2.357-1.167 3.03 0l6.28 10.875c.673 1.167-.17 2.625-1.516 2.625H3.72c-1.347 0-2.189-1.458-1.515-2.625L8.485 2.495zM10 5a.75.75 0 01.75.75v3.5a.75.75 0 01-1.5 0v-3.5A.75.75 0 0110 5zm0 9a1 1 0 100-2 1 1 0 000 2z" clipRule="evenodd" />
              </svg>
              <p className="text-sm text-amber-300">{NB_serverHint}</p>
            </div>
          )}
        </div>
      )}

      {/* ── Optional: join existing data cluster ── */}
      <div className="mt-5">
        <button
          type="button"
          onClick={() => NB_setShowClusterJoin(v => !v)}
          className="flex items-center gap-2 text-xs text-neutral-500 hover:text-neutral-300 transition-colors"
        >
          <svg
            viewBox="0 0 16 16"
            className={`w-3.5 h-3.5 transition-transform ${NB_showClusterJoin ? 'rotate-90' : ''}`}
            fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"
          >
            <path d="M6 4l4 4-4 4" />
          </svg>
          Join an existing data cluster (optional)
        </button>

        {NB_showClusterJoin && (
          <div className="mt-3 animate-[fadeIn_0.15s_ease-out]">
            <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl p-4 space-y-3">
              <p className="text-xs text-neutral-500 leading-relaxed">
                Provide your cluster passphrase to join an existing data cluster. This passphrase is stored <strong className="text-neutral-400">only on this device</strong> — it is separate from your cloud account credentials.
              </p>
              <div>
                <label className="block text-xs text-neutral-500 mb-1.5">Cluster passphrase</label>
                <input
                  type="password"
                  value={config.NETB05_clusterPassphrase}
                  onChange={e => update('NETB05_clusterPassphrase', e.target.value)}
                  placeholder="Cluster encryption passphrase"
                  autoComplete="off"
                  className="input text-sm py-2.5 font-mono"
                />
              </div>
              <p className="text-[11px] text-neutral-600">
                Leave blank to skip — you can join a cluster later from Settings.
              </p>
            </div>
          </div>
        )}
      </div>

      {/* Credentials-separation note */}
      <div className="mt-4 flex items-start gap-2 bg-neutral-900/40 border border-neutral-800/30 rounded-xl px-4 py-3">
        <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4 text-neutral-500 mt-0.5 shrink-0">
          <path fillRule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a.75.75 0 000 1.5h.253a.25.25 0 01.244.304l-.459 2.066A1.75 1.75 0 0010.747 15H11a.75.75 0 000-1.5h-.253a.25.25 0 01-.244-.304l.459-2.066A1.75 1.75 0 009.253 9H9z" clipRule="evenodd" />
        </svg>
        <p className="text-xs text-neutral-500 leading-relaxed">
          Your <strong className="text-neutral-300">cloud account</strong> credentials are separate from your <strong className="text-neutral-300">local OS account</strong> and from any <strong className="text-neutral-300">cluster passphrase</strong>. Each is managed independently.
        </p>
      </div>

      {error && <p className="text-sm text-red-400 mt-3">{error}</p>}

      <div className="flex items-center justify-between mt-6 pt-4 border-t border-neutral-800/30">
        <button onClick={() => { setView('pick'); setError('') }} className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors">
          ← Back
        </button>
        <button
          onClick={view === 'cloud-create' ? handleCloudCreate : validateCloudLogin}
          disabled={NB_cloudSubmitting}
          className="btn-primary disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-2"
        >
          {NB_cloudSubmitting ? (
            <>
              <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />
              Creating account…
            </>
          ) : (
            'Continue →'
          )}
        </button>
      </div>
    </div>
  )
}

// ═══════════════════════════════════
// Account  (CLOGIN-01/04: Local / Sign in / Create)
// ═══════════════════════════════════

// CL04_PasswordStrength evaluates password strength for NIST length-first hints.
function CL04_PasswordStrength({ password }) {
  if (!password) return null
  const len = password.length
  const hasUpper = /[A-Z]/.test(password)
  const hasDigit = /\d/.test(password)
  const hasSymbol = /[^A-Za-z0-9]/.test(password)
  const varietyBonus = (hasUpper ? 1 : 0) + (hasDigit ? 1 : 0) + (hasSymbol ? 1 : 0)

  let level = 0
  let label = ''
  let colour = ''

  if (len < 12) {
    level = 1; label = 'Too short — needs 12+ characters'; colour = 'bg-red-500'
  } else if (len < 16 && varietyBonus < 2) {
    level = 2; label = 'Weak'; colour = 'bg-orange-500'
  } else if (len < 20) {
    level = 3; label = 'Fair'; colour = 'bg-yellow-500'
  } else {
    level = 4; label = 'Strong'; colour = 'bg-green-500'
  }

  const bars = [1, 2, 3, 4]
  return (
    <div className="mt-1.5">
      <div className="flex gap-1 mb-1">
        {bars.map(b => (
          <div
            key={b}
            className={`h-1 flex-1 rounded-full transition-all duration-300 ${b <= level ? colour : 'bg-neutral-800'}`}
          />
        ))}
      </div>
      <p className={`text-[11px] ${level <= 1 ? 'text-red-400' : level === 2 ? 'text-orange-400' : level === 3 ? 'text-yellow-400' : 'text-green-400'}`}>
        {label}
      </p>
    </div>
  )
}

function AccountStep({ config, update, onNext, onPrev, onSignupComplete }) {
  const { t } = useI18n()
  const [error, setError] = useState('')
  const [CL04_submitting, CL04_setSubmitting] = useState(false)
  const [CL04_serverHint, CL04_setServerHint] = useState('')

  const mode = config.CL01_accountMode // 'local' | 'cloud' | 'create'

  // ── Local path validation ─────────────────────────────────────────────────
  const validateLocal = () => {
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

  // ── Cloud sign-in path validation ─────────────────────────────────────────
  const validateCloud = () => {
    if (!config.CL01_cloudEmail || !config.CL01_cloudEmail.includes('@')) {
      setError('Enter a valid email address')
      return
    }
    if (!config.CL01_cloudPassword || config.CL01_cloudPassword.length < 4) {
      setError('Cloud account password is required')
      return
    }
    setError('')
    onNext()
  }

  // ── Create cloud account ──────────────────────────────────────────────────
  const handleCreate = async () => {
    CL04_setServerHint('')
    setError('')

    if (!config.CL04_createEmail || !config.CL04_createEmail.includes('@')) {
      setError('Enter a valid email address')
      return
    }
    if (config.CL04_createPassword.length < 12) {
      setError('Password must be at least 12 characters')
      return
    }
    if (config.CL04_createPassword !== config.CL04_createConfirm) {
      setError('Passwords do not match')
      return
    }
    if (!config.CL04_createFullName || config.CL04_createFullName.trim().length < 2) {
      setError('Enter your full name')
      return
    }

    CL04_setSubmitting(true)
    try {
      const res = await fetch('/api/auth/cloud/signup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          email: config.CL04_createEmail.trim().toLowerCase(),
          password: config.CL04_createPassword,
          full_name: config.CL04_createFullName.trim(),
        }),
      })
      const data = await res.json().catch(() => ({}))

      if (res.ok || res.status === 201) {
        // Success — hand off to CLOGIN-05 post-signup wizard
        const email = config.CL04_createEmail.trim().toLowerCase()
        const isAdmin = Boolean(data?.fleet_admin)
        update('CL01_cloudEmail', email)
        update('CL01_accountMode', 'cloud')
        if (onSignupComplete) {
          onSignupComplete(email, isAdmin)
        } else {
          onNext()
        }
        return
      }

      // Surface specific server guidance
      const hint = data.hint || data.error || 'Sign-up failed — please try again'
      CL04_setServerHint(hint)
    } catch {
      CL04_setServerHint('Could not reach Vulos Cloud. Check your network and try again.')
    } finally {
      CL04_setSubmitting(false)
    }
  }

  const handleNext =
    mode === 'create' ? handleCreate :
    mode === 'cloud'  ? validateCloud :
    validateLocal

  const tabs = [
    {
      id: 'local',
      label: 'Local only',
      icon: (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" className="w-4 h-4">
          <circle cx="8" cy="5.5" r="2.5" />
          <path d="M2.5 13c0-3 2.5-5 5.5-5s5.5 2 5.5 5" />
        </svg>
      ),
      activeColour: 'text-blue-400',
    },
    {
      id: 'create',
      label: 'Create account',
      icon: (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" className="w-4 h-4">
          <path d="M12.5 9.5a3 3 0 00-.5-5.9A4.5 4.5 0 003.5 6a2.5 2.5 0 00.5 5h8.5z" />
          <path d="M8 10v4M6 12h4" />
        </svg>
      ),
      activeColour: 'text-emerald-400',
    },
    {
      id: 'cloud',
      label: 'Sign in',
      icon: (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" className="w-4 h-4">
          <path d="M12.5 9.5a3 3 0 00-.5-5.9A4.5 4.5 0 003.5 6a2.5 2.5 0 00.5 5h8.5z" />
        </svg>
      ),
      activeColour: 'text-violet-400',
    },
  ]

  return (
    <div>
      <StepHeader title={t('setup.account.title')} subtitle={t('setup.account.subtitle')} />

      {/* CLOGIN-01/04: three-mode tab strip */}
      <div className="flex rounded-xl bg-neutral-900/70 border border-neutral-800/60 p-1 gap-1 mb-5">
        {tabs.map(tab => (
          <button
            key={tab.id}
            onClick={() => { update('CL01_accountMode', tab.id); setError(''); CL04_setServerHint('') }}
            className={`flex-1 flex items-center justify-center gap-1.5 py-2 rounded-lg text-xs font-medium transition-all
              ${mode === tab.id
                ? 'bg-neutral-800 text-neutral-100 shadow-sm'
                : 'text-neutral-500 hover:text-neutral-300'}`}
          >
            <span className={mode === tab.id ? tab.activeColour : 'text-neutral-600'}>
              {tab.icon}
            </span>
            {tab.label}
          </button>
        ))}
      </div>

      {/* ── Local account form ─────────────────────────────────────────── */}
      {mode === 'local' && (
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
      )}

      {/* ── CLOGIN-04: Create Cloud Account form ──────────────────────── */}
      {mode === 'create' && (
        <div className="space-y-4 animate-[fadeIn_0.15s_ease-out]">
          {/* Info banner */}
          <div className="flex items-start gap-3 bg-emerald-600/10 border border-emerald-500/20 rounded-xl px-4 py-3">
            <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4 text-emerald-400 mt-0.5 shrink-0">
              <path fillRule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a.75.75 0 000 1.5h.253a.25.25 0 01.244.304l-.459 2.066A1.75 1.75 0 0010.747 15H11a.75.75 0 000-1.5h-.253a.25.25 0 01-.244-.304l.459-2.066A1.75 1.75 0 009.253 9H9z" clipRule="evenodd" />
            </svg>
            <p className="text-xs text-emerald-300 leading-relaxed">
              Create a free Vulos Cloud account. Enables remote access, sync, and 2FA across your devices.
            </p>
          </div>

          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Full name</label>
            <input
              value={config.CL04_createFullName}
              onChange={e => { update('CL04_createFullName', e.target.value); setError('') }}
              placeholder="Ada Lovelace"
              autoFocus
              autoComplete="name"
              className="input text-base py-3"
            />
          </div>

          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Email</label>
            <input
              type="email"
              value={config.CL04_createEmail}
              onChange={e => { update('CL04_createEmail', e.target.value); setError('') }}
              placeholder="you@example.com"
              autoComplete="email"
              className="input text-base py-3"
            />
          </div>

          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">
              Password
              <span className="ml-1.5 text-neutral-600 font-normal">(12+ characters)</span>
            </label>
            <input
              type="password"
              value={config.CL04_createPassword}
              onChange={e => { update('CL04_createPassword', e.target.value); setError(''); CL04_setServerHint('') }}
              placeholder="Choose a strong password"
              autoComplete="new-password"
              className="input text-base py-3"
            />
            <CL04_PasswordStrength password={config.CL04_createPassword} />
          </div>

          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Confirm password</label>
            <input
              type="password"
              value={config.CL04_createConfirm}
              onChange={e => { update('CL04_createConfirm', e.target.value); setError('') }}
              placeholder="Re-enter password"
              autoComplete="new-password"
              className={`input text-base py-3 ${
                config.CL04_createConfirm && config.CL04_createPassword !== config.CL04_createConfirm
                  ? 'border-red-500/60'
                  : config.CL04_createConfirm && config.CL04_createPassword === config.CL04_createConfirm
                    ? 'border-green-500/40'
                    : ''
              }`}
            />
          </div>

          {/* Server hint (breach / taken / etc.) */}
          {CL04_serverHint && (
            <div className="flex items-start gap-2 bg-amber-900/20 border border-amber-700/30 rounded-xl px-4 py-3">
              <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4 text-amber-400 mt-0.5 shrink-0">
                <path fillRule="evenodd" d="M8.485 2.495c.673-1.167 2.357-1.167 3.03 0l6.28 10.875c.673 1.167-.17 2.625-1.516 2.625H3.72c-1.347 0-2.189-1.458-1.515-2.625L8.485 2.495zM10 5a.75.75 0 01.75.75v3.5a.75.75 0 01-1.5 0v-3.5A.75.75 0 0110 5zm0 9a1 1 0 100-2 1 1 0 000 2z" clipRule="evenodd" />
              </svg>
              <p className="text-sm text-amber-300">{CL04_serverHint}</p>
            </div>
          )}
        </div>
      )}

      {/* ── Cloud sign-in form ─────────────────────────────────────────── */}
      {mode === 'cloud' && (
        <div className="space-y-4 animate-[fadeIn_0.15s_ease-out]">
          {/* Info banner */}
          <div className="flex items-start gap-3 bg-violet-600/10 border border-violet-500/20 rounded-xl px-4 py-3">
            <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4 text-violet-400 mt-0.5 shrink-0">
              <path fillRule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a.75.75 0 000 1.5h.253a.25.25 0 01.244.304l-.459 2.066A1.75 1.75 0 0010.747 15H11a.75.75 0 000-1.5h-.253a.25.25 0 01-.244-.304l.459-2.066A1.75 1.75 0 009.253 9H9z" clipRule="evenodd" />
            </svg>
            <p className="text-xs text-violet-300 leading-relaxed">
              Sign in with your existing Vulos Cloud account. Your cloud credentials will be used for OS login; a local user will be created automatically.
            </p>
          </div>

          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Cloud email</label>
            <input
              type="email"
              value={config.CL01_cloudEmail}
              onChange={e => { update('CL01_cloudEmail', e.target.value); setError('') }}
              placeholder="you@example.com"
              autoFocus
              autoComplete="email"
              className="input text-base py-3"
            />
          </div>

          <div>
            <label className="block text-xs text-neutral-500 mb-1.5">Cloud account password</label>
            <input
              type="password"
              value={config.CL01_cloudPassword}
              onChange={e => { update('CL01_cloudPassword', e.target.value); setError('') }}
              placeholder="Password"
              autoComplete="current-password"
              className="input text-base py-3"
            />
          </div>

          <p className="text-[11px] text-neutral-600">
            2FA will be requested after setup if enabled on your account.
          </p>
        </div>
      )}

      {error && <p className="text-sm text-red-400 mt-2">{error}</p>}

      <NavBar
        onPrev={onPrev}
        onNext={handleNext}
        nextLabel={mode === 'create' && CL04_submitting ? 'Creating account…' : undefined}
        nextDisabled={mode === 'create' && CL04_submitting}
      />
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
      <div className="w-12 h-12 rounded-2xl bg-amber-500/15 flex items-center justify-center mb-4">
        <svg viewBox="0 0 24 24" className="w-6 h-6 text-amber-400" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
          <rect x="3" y="11" width="18" height="11" rx="2" />
          <path d="M7 11V7a5 5 0 0110 0v4" />
          <circle cx="12" cy="16.5" r="1.5" fill="currentColor" />
        </svg>
      </div>
      <h2 className="text-xl font-semibold text-neutral-100 text-center">{t('setup.pin.title')}</h2>
      <p className="text-sm text-neutral-500 text-center mt-2 mb-6">
        {t('setup.pin.subtitle')}
      </p>

      {error && <p className="text-sm text-red-400 mb-3">{error}</p>}

      <input
        type="password"
        inputMode="numeric"
        pattern="[0-9]*"
        value={config.pin}
        onChange={e => { update('pin', e.target.value.replace(/\D/g, '')); setError('') }}
        placeholder={t('setup.pin.placeholder')}
        maxLength={8}
        className="w-full bg-neutral-900/60 border border-neutral-800/50 rounded-xl px-4 py-3 text-center text-lg tracking-[0.3em] text-neutral-100 outline-none placeholder:text-neutral-600 placeholder:tracking-normal placeholder:text-sm focus:border-amber-500/50 mb-3"
      />

      {config.pin && (
        <input
          type="password"
          inputMode="numeric"
          pattern="[0-9]*"
          value={confirm}
          onChange={e => { setConfirm(e.target.value.replace(/\D/g, '')); setError('') }}
          placeholder={t('setup.pin.confirm_placeholder')}
          maxLength={8}
          className="w-full bg-neutral-900/60 border border-neutral-800/50 rounded-xl px-4 py-3 text-center text-lg tracking-[0.3em] text-neutral-100 outline-none placeholder:text-neutral-600 placeholder:tracking-normal placeholder:text-sm focus:border-amber-500/50 mb-3"
        />
      )}

      <div className="flex gap-3 mt-4 w-full">
        <button onClick={onPrev} className="flex-1 py-3 rounded-xl text-sm font-medium text-neutral-400 bg-neutral-800/60 hover:bg-neutral-800 transition-colors">
          {t('setup.pin.back')}
        </button>
        <button onClick={() => { update('pin', ''); onNext() }} className="py-3 px-5 rounded-xl text-sm text-neutral-500 hover:text-neutral-300 transition-colors">
          {t('setup.pin.skip')}
        </button>
        <button onClick={handleNext} disabled={config.pin && config.pin.length < 4} className="flex-1 py-3 rounded-xl text-sm font-semibold text-white bg-amber-600 hover:bg-amber-500 disabled:opacity-40 transition-colors">
          {config.pin ? t('setup.pin.set') : t('setup.pin.skip')}
        </button>
      </div>
    </div>
  )
}

// ═══════════════════════════════════
// Appearance
// ═══════════════════════════════════
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
                ? 'bg-blue-600/15 border-2 border-blue-500/60 text-white shadow-lg shadow-blue-500/10'
                : 'bg-neutral-900/50 border-2 border-neutral-800/50 text-neutral-400 hover:border-neutral-700 hover:text-neutral-200'}`}
          >
            {/* Preview swatch */}
            <div className="w-12 h-12 rounded-xl border border-neutral-700/50 flex items-center justify-center overflow-hidden"
              style={{ background: thm.preview }}>
              <span className={theme === thm.value ? 'text-blue-400' : 'text-neutral-400'}>{thm.icon}</span>
            </div>
            <div>
              <div className="text-sm font-medium">{thm.label}</div>
              <div className="text-[11px] text-neutral-500 mt-0.5">{thm.desc}</div>
            </div>
            {theme === thm.value && (
              <div className="absolute top-2 right-2 w-5 h-5 rounded-full bg-blue-500 flex items-center justify-center">
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
            className={`w-10 h-5 rounded-full transition-colors relative ${nightShiftMode !== 'off' ? 'bg-amber-600' : 'bg-neutral-700'}`}
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
        subtitle={t('Every Vula OS node has a unique identifier. You can customise the hostname.')}
      />

      <div className="space-y-4 mb-2">
        {/* ULID display */}
        <div className="bg-neutral-900/60 border border-neutral-800/50 rounded-xl px-4 py-4">
          <div className="text-[10px] text-neutral-500 uppercase tracking-wider mb-2">{t('Instance ID (ULID)')}</div>
          {IS05_loading ? (
            <div className="h-5 w-48 bg-neutral-800 rounded animate-pulse" />
          ) : (
            <div className="font-mono text-sm text-blue-300 select-all break-all">
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

        {IS05_error && <p className="text-sm text-red-400">{IS05_error}</p>}
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
            className={`w-10 h-5 rounded-full transition-colors relative ${config.IS05_storageEnabled ? 'bg-blue-600' : 'bg-neutral-700'}`}
          >
            <span className={`absolute top-0.5 w-4 h-4 rounded-full bg-white transition-transform ${config.IS05_storageEnabled ? 'left-5' : 'left-0.5'}`} />
          </button>
        </div>
      </div>

      {config.IS05_storageEnabled && (
        <div className="space-y-4 mb-2 animate-[fadeIn_0.2s_ease-out]">
          {/* Size slider */}
          <div>
            <div className="flex items-center justify-between mb-1.5">
              <label className="text-xs text-neutral-500">{t('Allocated Size')}</label>
              <span className="text-sm font-mono text-blue-300">{config.IS05_storageSizeGb} GB</span>
            </div>
            <input
              type="range"
              min="5"
              max="100"
              step="5"
              value={config.IS05_storageSizeGb}
              onChange={e => update('IS05_storageSizeGb', Number(e.target.value))}
              className="w-full accent-blue-500"
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
              className={`input text-base py-3 ${IS05_passphraseConfirm && config.IS05_storagePassphrase !== IS05_passphraseConfirm ? 'border-red-500/60' : ''}`}
            />
          </div>

          {IS05_error && <p className="text-sm text-red-400">{IS05_error}</p>}
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
              : 'bg-neutral-900/80 border border-neutral-700/50 text-neutral-300 hover:border-blue-500/50 hover:text-white'}`}
        >
          {IS05_generating ? (
            <span className="flex items-center justify-center gap-2">
              <span className="w-4 h-4 border-2 border-neutral-600 border-t-blue-500 rounded-full animate-spin" />
              {t('Generating keypair...')}
            </span>
          ) : config.IS05_sshPubkey ? t('Regenerate Keypair') : t('Generate Ed25519 Keypair')}
        </button>

        {/* Private key display (shown once) */}
        {IS05_privateKey && (
          <div className="bg-neutral-950 border border-neutral-800 rounded-xl overflow-hidden animate-[fadeIn_0.2s_ease-out]">
            <div className="flex items-center justify-between px-4 py-2 bg-neutral-900/60 border-b border-neutral-800">
              <span className="text-[11px] text-amber-400 font-medium">{t('Private Key — shown once, copy now')}</span>
              <button
                onClick={IS05_copy}
                className={`text-xs px-3 py-1 rounded-lg transition-colors ${IS05_copied ? 'bg-green-700/30 text-green-400' : 'bg-neutral-800 text-neutral-400 hover:text-white'}`}
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
            <div className="font-mono text-xs text-green-400 break-all">{config.IS05_sshFingerprint}</div>
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
                ${IS05_confirmed ? 'bg-blue-600 border-blue-600' : 'border-neutral-600 group-hover:border-neutral-400'}`}>
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

        {IS05_error && <p className="text-sm text-red-400">{IS05_error}</p>}
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
              <div className="font-mono text-blue-300 text-[10px] break-all">{config.IS05_ulid || '—'}</div>
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
              <div className="text-green-400">{t('Enabled')} · {config.IS05_storageSizeGb} GB</div>
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
            <div className="text-[10px] font-mono text-green-400 break-all">{config.IS05_sshFingerprint}</div>
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
            ? 'bg-green-700/20 border border-green-600/40 text-green-400'
            : IS05_downloading
              ? 'bg-neutral-800 text-neutral-500'
              : 'bg-blue-600/20 border border-blue-500/40 text-blue-300 hover:bg-blue-600/30 hover:text-blue-200'}`}
      >
        {IS05_downloading ? (
          <span className="flex items-center justify-center gap-2">
            <span className="w-4 h-4 border-2 border-neutral-600 border-t-blue-400 rounded-full animate-spin" />
            {t('Preparing download...')}
          </span>
        ) : IS05_downloaded
          ? t('Recovery Kit Downloaded')
          : t('Download Recovery Kit')}
      </button>

      {IS05_error && <p className="text-sm text-red-400 mb-3">{IS05_error}</p>}

      {/* Type-to-confirm gate — applies to ALL variants including storage-skipped */}
      <div className="bg-neutral-900/60 border border-neutral-800/50 rounded-xl px-4 py-4 mb-2">
        <p className="text-sm text-neutral-400 mb-3">
          {t('Type ')}
          <code className="bg-neutral-800 text-amber-400 px-1.5 py-0.5 rounded text-xs font-mono">confirm</code>
          {t(' to proceed')}
        </p>
        <input
          value={IS05_confirmText}
          onChange={e => IS05_setConfirmText(e.target.value)}
          placeholder="confirm"
          className={`input py-2 font-mono text-sm ${IS05_canProceed ? 'border-green-500/50 text-green-400' : ''}`}
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
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')

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
      } catch {
        setError(t('setup.ready.error_server'))
        setCreating(false)
        return
      }
    }

    // Set PIN if configured
    if (config.pin) {
      await fetch('/api/auth/pin/set', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pin: config.pin }),
      }).catch(() => {})
    }

    await onFinish()
  }

  const selectedTz = TIMEZONES.find(tz => tz.id === config.timezone)
  const selectedLang = LANGUAGES.find(l => l.code === config.locale)
  const { theme } = useTheme()
  const themeLabels = {
    dark: t('setup.ready.theme_dark'),
    light: t('setup.ready.theme_light'),
    auto: t('setup.ready.theme_auto'),
    schedule: t('setup.ready.theme_schedule'),
  }

  return (
    <div className="text-center">
      <div className="text-4xl mb-2">✓</div>
      <StepHeader title={t('setup.ready.title')} subtitle={t('setup.ready.subtitle')} />

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

      {error && <p className="text-sm text-red-400 mb-4">{error}</p>}

      <button
        onClick={handleFinish}
        disabled={creating}
        className="btn-primary px-10 py-3 text-base"
      >
        {creating ? (
          <span className="flex items-center gap-2">
            <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />
            {t('setup.ready.setting_up')}
          </span>
        ) : t('setup.ready.enter')}
      </button>

      <button onClick={onPrev} className="block mx-auto mt-4 text-sm text-neutral-600 hover:text-neutral-400">
        {t('setup.ready.go_back')}
      </button>

      {/* kitBackup-IK11: recovery kit hint — additive, admin context */}
      <div className="mt-6 mx-auto max-w-sm rounded-xl border border-neutral-800/50 bg-neutral-900/40 px-4 py-3 text-left">
        <p className="text-xs text-neutral-500 leading-relaxed">
          <span className="text-neutral-400 font-medium">Recovery kit</span> — your credentials JSON was shown once during setup.
          You can re-download it any time as an admin via{' '}
          <code className="text-blue-400/80 text-[11px]">GET /api/recovery/kit</code>
          {' '}from a trusted local session.
        </p>
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
      <h2 className="text-2xl font-light text-neutral-100">{title}</h2>
      {subtitle && <p className="text-sm text-neutral-500 mt-1">{subtitle}</p>}
    </div>
  )
}

function NavBar({ onPrev, onNext, nextLabel, skipLabel, onSkip, nextDisabled }) {
  const { t } = useI18n()
  const resolvedNext = nextLabel ?? t('nav.continue')
  return (
    <div className="flex items-center justify-between mt-6 pt-4 border-t border-neutral-800/30">
      <button onClick={onPrev} className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors">
        {t('nav.back')}
      </button>
      <div className="flex items-center gap-3">
        {skipLabel && (
          <button onClick={onSkip} className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors">
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
    <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl px-4 py-3">
      <div className="flex items-center gap-2 mb-1">
        <span className="text-sm">{icon}</span>
        <span className="text-[10px] text-neutral-500 uppercase tracking-wider">{label}</span>
      </div>
      <div className="text-sm text-neutral-300 truncate">{value}</div>
    </div>
  )
}
