import { useState, useEffect, useRef } from 'react'
import FullscreenHint from './FullscreenHint'
import ThemeToggle from '../core/ThemeToggle'
import { useTheme } from '../core/ThemeProvider'
import { useI18n } from '../core/i18n'

const STEPS = ['welcome', 'language', 'timezone', 'network', 'account', 'pin', 'appearance', 'ready']

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
// Account
// ═══════════════════════════════════
function AccountStep({ config, update, onNext, onPrev }) {
  const { t } = useI18n()
  const [error, setError] = useState('')

  const validate = () => {
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

        {error && <p className="text-sm text-red-400">{error}</p>}
      </div>

      <NavBar onPrev={onPrev} onNext={validate} />
    </div>
  )
}

// ═══════════════════════════════════
// Appearance
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

function NavBar({ onPrev, onNext, nextLabel, skipLabel, onSkip }) {
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
        <button onClick={onNext} className="btn-primary">
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
