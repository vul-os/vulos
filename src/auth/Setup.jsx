import { useState, useEffect, useRef } from 'react'
import FullscreenHint from './FullscreenHint'
import ThemeToggle from '../core/ThemeToggle'
import { useTheme } from '../core/ThemeProvider'

const STEPS = ['welcome', 'language', 'timezone', 'network', 'account', 'pin', 'appearance', 'identity', 'storage', 'ssh', 'recoverykit', 'ready']

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
    IS05_storageSizeGb: 20,
    IS05_storagePassword: '',
    IS05_storagePassphrase: '',
    IS05_sshPubkey: '',
    IS05_sshFingerprint: '',
    IS05_s3AccessKey: '',
    IS05_s3SecretKey: '',
  })
  const [transitioning, setTransitioning] = useState(false)

  const current = STEPS[step]
  const update = (key, val) => setConfig(c => ({ ...c, [key]: val }))

  const goTo = (idx) => {
    setTransitioning(true)
    setTimeout(() => { setStep(idx); setTransitioning(false) }, 200)
  }
  const next = () => goTo(Math.min(step + 1, STEPS.length - 1))
  const prev = () => goTo(Math.max(step - 1, 0))

  const finish = async () => {
    try {
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
    } catch {}
    onComplete()
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

        {/* Progress dots */}
        <div className="absolute top-8 flex gap-2">
          {STEPS.map((_, i) => (
            <button
              key={i}
              onClick={() => i < step && goTo(i)}
              className={`w-2 h-2 rounded-full transition-all duration-500
                ${i === step ? 'bg-blue-500 w-6' : i < step ? 'bg-blue-500/50 cursor-pointer hover:bg-blue-400' : 'bg-neutral-800'}`}
            />
          ))}
        </div>

        {/* Content */}
        <div className={`w-full max-w-xl transition-all duration-200 ${transitioning ? 'opacity-0 translate-y-4' : 'opacity-100 translate-y-0'}`}>
          {current === 'welcome' && <WelcomeStep onNext={next} />}
          {current === 'language' && <LanguageStep config={config} update={update} onNext={next} onPrev={prev} />}
          {current === 'timezone' && <TimezoneStep config={config} update={update} onNext={next} onPrev={prev} />}
          {current === 'network' && <NetworkStep config={config} update={update} onNext={next} onPrev={prev} />}
          {current === 'account' && <AccountStep config={config} update={update} onNext={next} onPrev={prev} />}
          {current === 'pin' && <PinStep config={config} update={update} onNext={next} onPrev={prev} />}
          {current === 'appearance' && <AppearanceStep onNext={next} onPrev={prev} />}
          {current === 'identity' && <IS05_IdentityStep config={config} update={update} onNext={next} onPrev={prev} />}
          {current === 'storage' && <IS05_StorageStep config={config} update={update} onNext={next} onPrev={prev} />}
          {current === 'ssh' && <IS05_SSHStep config={config} update={update} onNext={next} onPrev={prev} />}
          {current === 'recoverykit' && <IS05_RecoveryKitStep config={config} update={update} onNext={next} onPrev={prev} />}
          {current === 'ready' && <ReadyStep config={config} onFinish={finish} onPrev={prev} />}
        </div>
      </div>
    </div>
  )
}

// ═══════════════════════════════════
// Welcome
// ═══════════════════════════════════
function WelcomeStep({ onNext }) {
  return (
    <div className="text-center">
      <div className="mb-6 flex flex-col items-center">
        <img src="/icon-128.png" alt="Vula OS" className="w-20 h-20 mb-4" />
        <div className="text-5xl font-extralight text-neutral-100 tracking-[0.2em] mb-3">vula</div>
        <div className="h-px w-16 mx-auto bg-gradient-to-r from-transparent via-blue-500 to-transparent mb-3" />
        <p className="text-neutral-500 text-lg font-light">an open operating system</p>
        <p className="text-neutral-700 text-sm mt-1 italic">"vula" — isiZulu for "open"</p>
      </div>
      <button onClick={onNext} className="btn-primary px-10 py-3 text-base mt-8">
        Get Started
      </button>
    </div>
  )
}

// ═══════════════════════════════════
// Language
// ═══════════════════════════════════
function LanguageStep({ config, update, onNext, onPrev }) {
  return (
    <div>
      <StepHeader title="Choose your language" subtitle="You can change this later in Settings" />
      <div className="grid grid-cols-2 sm:grid-cols-3 gap-2 max-h-[50vh] overflow-y-auto pr-1">
        {LANGUAGES.map(lang => (
          <button
            key={lang.code}
            onClick={() => update('locale', lang.code)}
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
  const selected = TIMEZONES.find(t => t.id === config.timezone)

  return (
    <div>
      <StepHeader title="Select your timezone" subtitle={selected ? `${selected.label} (${selected.offset})` : 'Click a city on the map'} />

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
      <StepHeader title="Connect to the internet" subtitle="WiFi or Ethernet — you can configure this later" />

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
            Scanning...
          </span>
        ) : networks ? 'Scan Again' : 'Scan for WiFi Networks'}
      </button>

      {/* Network list */}
      {networks && (
        <div className="max-h-[35vh] overflow-y-auto rounded-xl border border-neutral-800/50 mb-4">
          {networks.length === 0 && (
            <div className="p-4 text-sm text-neutral-600 text-center">No networks found</div>
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
              {config.wifiSSID === n.ssid && <span className="text-blue-500 text-xs">Selected</span>}
            </button>
          ))}
        </div>
      )}

      {/* Password input */}
      {config.wifiSSID && showPassword && (
        <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl p-4 mb-4 animate-[fadeIn_0.2s_ease-out]">
          <div className="flex items-center gap-2 mb-2">
            <span className="text-sm text-neutral-300">{config.wifiSSID}</span>
            <button onClick={() => { update('wifiSSID', ''); setShowPassword(false) }} className="text-xs text-neutral-600 hover:text-neutral-400">Change</button>
          </div>
          <input
            type="password"
            value={config.wifiPassword}
            onChange={e => update('wifiPassword', e.target.value)}
            placeholder="WiFi password"
            autoFocus
            className="input"
          />
        </div>
      )}

      <NavBar onPrev={onPrev} onNext={onNext} skipLabel="Skip — use Ethernet" onSkip={onNext} />
    </div>
  )
}

// ═══════════════════════════════════
// Account
// ═══════════════════════════════════
function AccountStep({ config, update, onNext, onPrev }) {
  const [error, setError] = useState('')

  const validate = () => {
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

  return (
    <div>
      <StepHeader title="Create your account" subtitle="This will be the administrator account" />

      <div className="space-y-4">
        <div>
          <label className="block text-xs text-neutral-500 mb-1.5">Your name</label>
          <input
            value={config.displayName}
            onChange={e => update('displayName', e.target.value)}
            placeholder="What should we call you?"
            autoFocus
            className="input text-base py-3"
          />
        </div>

        <div>
          <label className="block text-xs text-neutral-500 mb-1.5">Username</label>
          <input
            value={config.username}
            onChange={e => update('username', e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ''))}
            placeholder="Username for login"
            className="input text-base py-3 font-mono"
          />
        </div>

        <div>
          <label className="block text-xs text-neutral-500 mb-1.5">Password</label>
          <input
            type="password"
            value={config.password}
            onChange={e => update('password', e.target.value)}
            placeholder="Choose a password"
            className="input text-base py-3"
          />
        </div>

        {error && <p className="text-sm text-red-400">{error}</p>}
      </div>

      <NavBar onPrev={onPrev} onNext={validate} nextLabel="Continue" />
    </div>
  )
}

// ═══════════════════════════════════
// Appearance
// ═══════════════════════════════════
function PinStep({ config, update, onNext, onPrev }) {
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')

  const handleNext = () => {
    if (config.pin && config.pin !== confirm) {
      setError('PINs do not match')
      return
    }
    if (config.pin && config.pin.length < 4) {
      setError('PIN must be at least 4 digits')
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
      <h2 className="text-xl font-semibold text-neutral-100 text-center">Lock Screen PIN</h2>
      <p className="text-sm text-neutral-500 text-center mt-2 mb-6">
        Set a PIN to lock your screen. You can skip this and set it later in Settings.
      </p>

      {error && <p className="text-sm text-red-400 mb-3">{error}</p>}

      <input
        type="password"
        inputMode="numeric"
        pattern="[0-9]*"
        value={config.pin}
        onChange={e => { update('pin', e.target.value.replace(/\D/g, '')); setError('') }}
        placeholder="Enter PIN (4+ digits)"
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
          placeholder="Confirm PIN"
          maxLength={8}
          className="w-full bg-neutral-900/60 border border-neutral-800/50 rounded-xl px-4 py-3 text-center text-lg tracking-[0.3em] text-neutral-100 outline-none placeholder:text-neutral-600 placeholder:tracking-normal placeholder:text-sm focus:border-amber-500/50 mb-3"
        />
      )}

      <div className="flex gap-3 mt-4 w-full">
        <button onClick={onPrev} className="flex-1 py-3 rounded-xl text-sm font-medium text-neutral-400 bg-neutral-800/60 hover:bg-neutral-800 transition-colors">
          Back
        </button>
        <button onClick={() => { update('pin', ''); onNext() }} className="py-3 px-5 rounded-xl text-sm text-neutral-500 hover:text-neutral-300 transition-colors">
          Skip
        </button>
        <button onClick={handleNext} disabled={config.pin && config.pin.length < 4} className="flex-1 py-3 rounded-xl text-sm font-semibold text-white bg-amber-600 hover:bg-amber-500 disabled:opacity-40 transition-colors">
          {config.pin ? 'Set PIN' : 'Skip'}
        </button>
      </div>
    </div>
  )
}

// ═══════════════════════════════════
function AppearanceStep({ onNext, onPrev }) {
  const { theme, setTheme, nightShiftMode, setNightShiftMode } = useTheme()

  const themes = [
    { value: 'dark', label: 'Dark', desc: 'Easy on the eyes', preview: '#0c0c0c',
      icon: <svg viewBox="0 0 24 24" className="w-8 h-8"><path d="M12 3a9 9 0 109 9c0-.46-.04-.92-.1-1.36a5.39 5.39 0 01-4.4 2.26 5.4 5.4 0 01-3.14-9.8A9.06 9.06 0 0012 3z" fill="currentColor"/></svg> },
    { value: 'light', label: 'Light', desc: 'Clean and bright', preview: '#ffffff',
      icon: <svg viewBox="0 0 24 24" className="w-8 h-8"><circle cx="12" cy="12" r="5" fill="currentColor"/><path d="M12 1v2m0 18v2M4.22 4.22l1.42 1.42m12.72 12.72l1.42 1.42M1 12h2m18 0h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42" stroke="currentColor" strokeWidth="2" strokeLinecap="round"/></svg> },
    { value: 'auto', label: 'Auto', desc: 'Follows your system', preview: 'linear-gradient(135deg, #0c0c0c 50%, #ffffff 50%)',
      icon: <svg viewBox="0 0 24 24" className="w-8 h-8"><circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeWidth="2"/><path d="M12 3a9 9 0 010 18V3z" fill="currentColor"/></svg> },
    { value: 'schedule', label: 'Schedule', desc: 'Dark at night, light by day', preview: 'linear-gradient(180deg, #1a1a2e 0%, #f5a623 100%)',
      icon: <svg viewBox="0 0 24 24" className="w-8 h-8"><circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeWidth="2"/><path d="M12 6v6l4 2" stroke="currentColor" strokeWidth="2" strokeLinecap="round"/></svg> },
  ]

  return (
    <div>
      <StepHeader title="Choose your look" subtitle="Pick a theme — you can always change it later" />

      <div className="grid grid-cols-2 gap-3 mb-6">
        {themes.map(t => (
          <button
            key={t.value}
            onClick={() => setTheme(t.value)}
            className={`relative flex flex-col items-center gap-2 px-4 py-5 rounded-2xl text-center transition-all
              ${theme === t.value
                ? 'bg-blue-600/15 border-2 border-blue-500/60 text-white shadow-lg shadow-blue-500/10'
                : 'bg-neutral-900/50 border-2 border-neutral-800/50 text-neutral-400 hover:border-neutral-700 hover:text-neutral-200'}`}
          >
            {/* Preview swatch */}
            <div className="w-12 h-12 rounded-xl border border-neutral-700/50 flex items-center justify-center overflow-hidden"
              style={{ background: t.preview }}>
              <span className={theme === t.value ? 'text-blue-400' : 'text-neutral-400'}>{t.icon}</span>
            </div>
            <div>
              <div className="text-sm font-medium">{t.label}</div>
              <div className="text-[11px] text-neutral-500 mt-0.5">{t.desc}</div>
            </div>
            {theme === t.value && (
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
            <div className="text-sm text-neutral-300">Night Shift</div>
            <div className="text-[11px] text-neutral-600">Warm screen colours in the evening</div>
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
// Ready
// ═══════════════════════════════════
function ReadyStep({ config, onFinish, onPrev }) {
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
        setError('Could not reach server')
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

  const selectedTz = TIMEZONES.find(t => t.id === config.timezone)
  const selectedLang = LANGUAGES.find(l => l.code === config.locale)
  const { theme } = useTheme()
  const themeLabels = { dark: 'Dark', light: 'Light', auto: 'Auto', schedule: 'Scheduled' }

  return (
    <div className="text-center">
      <div className="text-4xl mb-2">✓</div>
      <StepHeader title="You're all set" subtitle="Here's what we'll configure" />

      <div className="grid grid-cols-2 gap-3 text-left mb-8">
        <SummaryCard icon="🌍" label="Language" value={selectedLang?.native || config.locale} />
        <SummaryCard icon="🕐" label="Timezone" value={selectedTz?.label || config.timezone || 'Auto'} />
        <SummaryCard icon="📶" label="WiFi" value={config.wifiSSID || 'Not configured'} />
        <SummaryCard icon="👤" label="Account" value={config.username || 'Not set'} />
        <SummaryCard icon="🎨" label="Theme" value={themeLabels[theme] || theme} />
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
            Setting up...
          </span>
        ) : 'Enter Vula OS'}
      </button>

      <button onClick={onPrev} className="block mx-auto mt-4 text-sm text-neutral-600 hover:text-neutral-400">
        Go back
      </button>
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

  const t = (s) => s

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
    } catch {}
    IS05_setSaving(false)
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
    // wire: uint32(len(type)) + type + uint32(len(key)) + key
    const buf = new ArrayBuffer(4 + typeBytes.length + 4 + rawBytes.length)
    const view = new DataView(buf)
    let off = 0
    view.setUint32(off, typeBytes.length); off += 4
    new Uint8Array(buf, off, typeBytes.length).set(typeBytes); off += typeBytes.length
    view.setUint32(off, rawBytes.length); off += 4
    new Uint8Array(buf, off, rawBytes.length).set(rawBytes)
    return `ssh-ed25519 ${btoa(String.fromCharCode(...new Uint8Array(buf)))} setup`
  }

  // Build PEM-style private key representation (OpenSSH format, simplified)
  const IS05_buildPrivkeyPEM = async (privKeyRaw, pubKeyRaw) => {
    // We export the private key bytes via PKCS8, wrap for display
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
      const privPEM = await IS05_buildPrivkeyPEM(keyPair.privateKey, rawPub)
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
// INIT-05: Recovery Kit Step
// ═══════════════════════════════════
function IS05_RecoveryKitStep({ config, update, onNext, onPrev }) {
  const [IS05_confirmText, IS05_setConfirmText] = useState('')
  const [IS05_downloading, IS05_setDownloading] = useState(false)
  const [IS05_downloaded, IS05_setDownloaded] = useState(false)
  const [IS05_error, IS05_setError] = useState('')

  const t = (s) => s

  const IS05_canProceed = IS05_confirmText === 'confirm'

  const IS05_downloadKit = async () => {
    IS05_setDownloading(true)
    IS05_setError('')
    try {
      const res = await fetch('/api/recovery/kit')
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const blob = await res.blob()
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

        {config.IS05_sshFingerprint && (
          <div className="bg-neutral-900/50 border border-neutral-800/50 rounded-xl px-4 py-3">
            <div className="text-[10px] text-neutral-500 uppercase tracking-wider mb-2">{t('SSH Access')}</div>
            <div className="text-[10px] font-mono text-green-400 break-all">{config.IS05_sshFingerprint}</div>
          </div>
        )}
      </div>

      {/* Download button */}
      <button
        onClick={IS05_downloadKit}
        disabled={IS05_downloading}
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

      {/* Type-to-confirm gate */}
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

function NavBar({ onPrev, onNext, nextLabel = 'Continue', skipLabel, onSkip, nextDisabled }) {
  return (
    <div className="flex items-center justify-between mt-6 pt-4 border-t border-neutral-800/30">
      <button onClick={onPrev} className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors">
        ← Back
      </button>
      <div className="flex items-center gap-3">
        {skipLabel && (
          <button onClick={onSkip} className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors">
            {skipLabel}
          </button>
        )}
        <button onClick={onNext} disabled={nextDisabled} className="btn-primary disabled:opacity-40 disabled:cursor-not-allowed">
          {nextLabel} →
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
