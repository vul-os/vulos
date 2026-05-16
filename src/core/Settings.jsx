import { useState, useEffect, useCallback, useRef } from 'react'
import { useAuth } from '../auth/AuthProvider'
import { useTheme, DEFAULT_ACCENT } from './ThemeProvider'
import { useWallpaper, DEFAULT_WALLPAPER } from './useWallpaper.jsx'
import { PamVisibilityControl } from './PublicAppsManager'

const sections = [
  { id: 'ai', label: 'AI Assistant' },
  { id: 'aiapps', label: 'AI Apps' },
  { id: 'appearance', label: 'Appearance' },
  { id: 'wifi', label: 'WiFi' },
  { id: 'bluetooth', label: 'Bluetooth' },
  { id: 'audio', label: 'Sound' },
  { id: 'display', label: 'Display' },
  { id: 'energy', label: 'Battery & Energy' },
  { id: 'vault', label: 'Backup & Sync' },
  { id: 'recall', label: 'Search & Index' },
  { id: 'storage', label: 'Storage' },
  { id: 'connmode', label: 'Connection Mode' },
  { id: 'network', label: 'Remote Access' },
  { id: 'turnSettings', label: 'TURN / WebRTC' },
  { id: 'users', label: 'Users & Profiles' },
  { id: 'account', label: 'Account' },
  { id: 'about', label: 'About' },
]

export default function Settings() {
  const [active, setActive] = useState('ai')
  const { profile, updateProfile, logout } = useAuth()

  return (
    <div className="flex h-full bg-neutral-950 text-neutral-200">
      {/* Sidebar */}
      <div className="w-48 shrink-0 border-r border-neutral-800/50 py-4 overflow-y-auto">
        <h2 className="px-4 text-sm font-semibold text-neutral-400 mb-3">Settings</h2>
        {sections.map(s => (
          <button
            key={s.id}
            onClick={() => setActive(s.id)}
            className={`w-full text-left px-4 py-2 text-sm transition-colors
              ${active === s.id ? 'bg-neutral-800/60 text-white' : 'text-neutral-400 hover:text-neutral-200 hover:bg-neutral-800/30'}`}
          >
            {s.label}
          </button>
        ))}
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-6 max-w-2xl">
        {active === 'ai' && <AISettings profile={profile} updateProfile={updateProfile} />}
        {active === 'aiapps' && <AIAppsSettings />}
        {active === 'appearance' && <AppearanceSettings />}
        {active === 'wifi' && <WiFiSettings />}
        {active === 'bluetooth' && <BluetoothSettings />}
        {active === 'audio' && <AudioSettings />}
        {active === 'display' && <DisplaySettings />}
        {active === 'energy' && <EnergySettings />}
        {active === 'vault' && <VaultSettings />}
        {active === 'recall' && <RecallSettings />}
        {active === 'storage' && <StorageSettings />}
        {active === 'connmode' && <NET9_ConnectionModeSettings />}
        {active === 'network' && <NetworkSettings />}
        {active === 'turnSettings' && <TURNSettingsSection />}
        {active === 'users' && <UsersSettings profile={profile} />}
        {active === 'account' && <AccountSettings profile={profile} updateProfile={updateProfile} logout={logout} />}
        {active === 'about' && <AboutSettings />}
      </div>
    </div>
  )
}

// --- AI ---
function AISettings({ profile, updateProfile }) {
  const [provider, setProvider] = useState(profile?.ai_provider || 'ollama')
  const [model, setModel] = useState(profile?.ai_model || '')
  const [apiKey, setApiKey] = useState('')
  const [status, setStatus] = useState(null)

  useEffect(() => {
    fetch('/api/ai/status').then(r => r.json()).then(setStatus).catch(() => {})
  }, [])

  const save = () => updateProfile({ ai_provider: provider, ai_model: model, ai_api_key: apiKey || undefined })

  return (
    <Section title="AI Assistant">
      <Field label="Provider">
        <select value={provider} onChange={e => setProvider(e.target.value)} className="input">
          <option value="ollama">Ollama (local)</option>
          <option value="claude">Claude (Anthropic)</option>
          <option value="openai">OpenAI</option>
          <option value="custom">Custom (OpenAI-compatible)</option>
        </select>
      </Field>
      <Field label="Model">
        <input value={model} onChange={e => setModel(e.target.value)} placeholder={provider === 'ollama' ? 'llama3' : provider === 'claude' ? 'claude-sonnet-4-20250514' : 'gpt-4o'} className="input" />
      </Field>
      <Field label="API Key">
        <input type="password" value={apiKey} onChange={e => setApiKey(e.target.value)} placeholder="••••••" className="input" />
      </Field>
      {status && (
        <div className={`text-xs mt-2 ${status.available ? 'text-green-500' : 'text-red-400'}`}>
          {status.available ? `Connected: ${status.provider} / ${status.model}` : `Not available: ${status.error || 'check config'}`}
        </div>
      )}
      <button onClick={save} className="btn mt-4">Save</button>
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
    <Section title="Appearance">
      <Field label="Theme">
        <div className="flex gap-2">
          {[
            { value: 'dark', label: 'Dark', icon: '\u{263E}' },
            { value: 'light', label: 'Light', icon: '\u{2600}' },
            { value: 'auto', label: 'Auto', icon: '\u{25D1}' },
            { value: 'schedule', label: 'Schedule', icon: '\u{23F0}' },
          ].map(opt => (
            <button
              key={opt.value}
              onClick={() => setTheme(opt.value)}
              className={`flex-1 py-3 rounded-xl text-sm transition-all border
                ${theme === opt.value
                  ? 'bg-blue-600/20 border-blue-500/50 text-blue-400'
                  : 'bg-neutral-900/50 border-neutral-800/50 text-neutral-400 hover:border-neutral-700 hover:text-neutral-200'}`}
            >
              <div className="text-center">
                <div className="text-lg mb-1">{opt.icon}</div>
                {opt.label}
              </div>
            </button>
          ))}
        </div>
      </Field>
      <p className="text-xs text-neutral-600 mt-2">
        {theme === 'auto' && `Follows system preference. Currently ${isDark ? 'dark' : 'light'}.`}
        {theme === 'schedule' && `Switches by time (${tz}). Currently ${resolved}.`}
        {theme === 'dark' && 'Always dark.'}
        {theme === 'light' && 'Always light.'}
      </p>

      {theme === 'schedule' && (
        <div className="mt-3 p-3 rounded-lg bg-neutral-900/50 border border-neutral-800/50 space-y-3">
          <div className="flex gap-4">
            <Field label="Dark mode from">
              <input type="time" value={scheduleDark} onChange={e => setScheduleDark(e.target.value)} className="input" />
            </Field>
            <Field label="Light mode from">
              <input type="time" value={scheduleLight} onChange={e => setScheduleLight(e.target.value)} className="input" />
            </Field>
          </div>
          <p className="text-[11px] text-neutral-600">Timezone: {tz}</p>
        </div>
      )}

      {/* Night Shift */}
      <div className="mt-6 pt-4 border-t border-neutral-800/50">
        <h3 className="text-sm font-medium mb-3">Night Shift</h3>
        <p className="text-xs text-neutral-600 mb-3">Warms screen colors to reduce blue light during evening hours.</p>
        <Field label="Mode">
          <div className="flex gap-2">
            {[
              { value: 'off', label: 'Off' },
              { value: 'auto', label: 'Sunset to Sunrise' },
              { value: 'custom', label: 'Custom' },
            ].map(opt => (
              <button
                key={opt.value}
                onClick={() => setNightShiftMode(opt.value)}
                className={`flex-1 py-2 rounded-lg text-sm transition-all border
                  ${nightShiftMode === opt.value
                    ? 'bg-amber-600/20 border-amber-500/50 text-amber-400'
                    : 'bg-neutral-900/50 border-neutral-800/50 text-neutral-400 hover:border-neutral-700 hover:text-neutral-200'}`}
              >
                {opt.label}
              </button>
            ))}
          </div>
        </Field>

        {nightShiftMode === 'auto' && (
          <p className="text-xs text-neutral-600 mt-2">
            Based on approximate sunrise/sunset for your timezone ({tz}).
            {nightShiftActive ? ' Currently active.' : ' Currently off.'}
          </p>
        )}

        {nightShiftMode === 'custom' && (
          <div className="mt-3 p-3 rounded-lg bg-neutral-900/50 border border-neutral-800/50 space-y-3">
            <div className="flex gap-4">
              <Field label="From">
                <input type="time" value={nightShiftFrom} onChange={e => setNightShiftFrom(e.target.value)} className="input" />
              </Field>
              <Field label="To">
                <input type="time" value={nightShiftTo} onChange={e => setNightShiftTo(e.target.value)} className="input" />
              </Field>
            </div>
            <p className="text-[11px] text-neutral-600">
              {nightShiftActive ? 'Currently active.' : 'Currently off.'} Timezone: {tz}
            </p>
          </div>
        )}

        {nightShiftMode !== 'off' && (
          <Field label={`Warmth (${nightShiftWarmth}%)`}>
            <input
              type="range" min="10" max="100" value={nightShiftWarmth}
              onChange={e => setNightShiftWarmth(parseInt(e.target.value))}
              className="w-full h-1 appearance-none bg-neutral-800 rounded-full
                [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:w-3 [&::-webkit-slider-thumb]:h-3
                [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:bg-amber-400"
            />
            <div className="flex justify-between text-[10px] text-neutral-600 mt-1">
              <span>Less warm</span>
              <span>More warm</span>
            </div>
          </Field>
        )}
      </div>

      {/* Accent Colour */}
      <div className="mt-6 pt-4 border-t border-neutral-800/50">
        <h3 className="text-sm font-medium mb-1">Accent Colour</h3>
        <p className="text-xs text-neutral-600 mb-3">Applied to primary buttons and focus rings across the system.</p>
        <AccentPicker accent={accent} setAccent={setAccent} />
      </div>

      {/* Wallpaper */}
      <div className="mt-6 pt-4 border-t border-neutral-800/50">
        <h3 className="text-sm font-medium mb-3">Wallpaper</h3>
        <WallpaperPicker />
      </div>
    </Section>
  )
}

function WallpaperPicker() {
  const { wallpaper, setWallpaper } = useWallpaper()
  const fileRef = useRef(null)

  const handleFile = (e) => {
    const file = e.target.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = () => setWallpaper(reader.result)
    reader.readAsDataURL(file)
  }

  const previewSrc = wallpaper || DEFAULT_WALLPAPER

  return (
    <div>
      <div className="rounded-lg overflow-hidden border border-neutral-800 mb-3" style={{ maxWidth: 320 }}>
        <img src={previewSrc} alt="Current wallpaper" className="w-full aspect-video object-cover" />
      </div>
      <div className="flex gap-3">
        <button
          onClick={() => fileRef.current?.click()}
          className="text-xs px-3 py-1.5 rounded-lg bg-neutral-800 text-neutral-300 hover:bg-neutral-700 transition-colors"
        >
          Choose Image...
        </button>
        {wallpaper && (
          <button
            onClick={() => setWallpaper(null)}
            className="text-xs px-3 py-1.5 rounded-lg text-neutral-500 hover:text-neutral-300 transition-colors"
          >
            Reset to Default
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

function AccentPicker({ accent, setAccent }) {
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
                ? 'border-white scale-110 shadow-lg'
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
          className="w-8 h-8 rounded cursor-pointer border border-neutral-700 bg-transparent p-0.5"
          title="Custom colour"
        />
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
          className="input w-32 font-mono"
        />
        {accent !== DEFAULT_ACCENT && (
          <button
            onClick={() => setAccent(DEFAULT_ACCENT)}
            className="text-xs text-neutral-500 hover:text-neutral-300 transition-colors"
          >
            Reset
          </button>
        )}
      </div>

      {/* Live preview */}
      <div className="mt-3 flex items-center gap-3">
        <button
          className="btn-primary text-sm"
          style={{ background: accent }}
          tabIndex={-1}
        >
          Preview button
        </button>
        <span className="text-xs text-neutral-600">Live preview</span>
      </div>
    </div>
  )
}

// --- WiFi ---
function WiFiSettings() {
  const [status, setStatus] = useState(null)
  const [networks, setNetworks] = useState(null)
  const [scanning, setScanning] = useState(false)
  const [connectSSID, setConnectSSID] = useState(null)
  const [password, setPassword] = useState('')

  const refresh = () => fetch('/api/wifi/status').then(r => r.json()).then(setStatus).catch(() => {})
  useEffect(() => { refresh() }, [])

  const scan = async () => {
    setScanning(true)
    const res = await fetch('/api/wifi/scan').then(r => r.json()).then(d => Array.isArray(d) ? d : []).catch(() => [])
    setNetworks(res)
    setScanning(false)
  }

  const connect = async () => {
    await fetch('/api/wifi/connect', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ssid: connectSSID, password }) })
    setConnectSSID(null)
    setPassword('')
    setTimeout(refresh, 3000)
  }

  return (
    <Section title="WiFi">
      {status && (
        <div className={`text-sm mb-4 ${status.connected ? 'text-green-400' : 'text-neutral-500'}`}>
          {status.connected ? `Connected to ${status.ssid} (${status.ip})` : 'Not connected'}
        </div>
      )}
      <button onClick={scan} disabled={scanning} className="btn mb-4">{scanning ? 'Scanning...' : 'Scan Networks'}</button>
      {networks && networks.map(n => (
        <div key={n.bssid || n.ssid} className="flex items-center justify-between py-2 border-b border-neutral-800/30">
          <div>
            <span className="text-sm">{n.ssid || '(hidden)'}</span>
            <span className="text-xs text-neutral-600 ml-2">{n.signal}dBm · {n.band} · {n.security || 'open'}</span>
          </div>
          <button onClick={() => setConnectSSID(n.ssid)} className="text-xs text-blue-400 hover:text-blue-300">Connect</button>
        </div>
      ))}
      {connectSSID && (
        <div className="mt-3 p-3 bg-neutral-900 rounded-lg">
          <p className="text-sm mb-2">Connect to {connectSSID}</p>
          <input type="password" value={password} onChange={e => setPassword(e.target.value)} placeholder="Password" className="input mb-2" />
          <div className="flex gap-2">
            <button onClick={connect} className="btn">Connect</button>
            <button onClick={() => setConnectSSID(null)} className="btn-ghost">Cancel</button>
          </div>
        </div>
      )}
    </Section>
  )
}

// --- Bluetooth ---
function BluetoothSettings() {
  const [status, setStatus] = useState(null)
  const refresh = () => fetch('/api/bluetooth/status').then(r => r.json()).then(setStatus).catch(() => {})
  useEffect(() => { refresh() }, [])

  const setPower = (on) => fetch('/api/bluetooth/power', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ on }) }).then(refresh)
  const scan = (on) => fetch('/api/bluetooth/scan', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ on }) }).then(() => setTimeout(refresh, 3000))
  const pair = (addr) => fetch('/api/bluetooth/pair', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ address: addr }) }).then(refresh)
  const connect = (addr) => fetch('/api/bluetooth/connect', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ address: addr }) }).then(refresh)
  const disconnect = (addr) => fetch('/api/bluetooth/disconnect', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ address: addr }) }).then(refresh)
  const remove = (addr) => fetch('/api/bluetooth/remove', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ address: addr }) }).then(refresh)

  return (
    <Section title="Bluetooth">
      <Toggle label="Bluetooth" checked={status?.powered} onChange={(v) => setPower(v)} />
      {status?.powered && (
        <>
          <button onClick={() => scan(true)} className="btn mt-3 mb-3">Scan for Devices</button>
          {status?.devices?.map(d => (
            <div key={d.address} className="flex items-center justify-between py-2 border-b border-neutral-800/30">
              <div>
                <span className="text-sm">{d.name || d.address}</span>
                <span className="text-xs text-neutral-600 ml-2">{d.type}{d.connected ? ' · connected' : d.paired ? ' · paired' : ''}</span>
              </div>
              <div className="flex gap-2">
                {!d.paired && <button onClick={() => pair(d.address)} className="text-xs text-blue-400">Pair</button>}
                {d.paired && !d.connected && <button onClick={() => connect(d.address)} className="text-xs text-blue-400">Connect</button>}
                {d.connected && <button onClick={() => disconnect(d.address)} className="text-xs text-amber-400">Disconnect</button>}
                {d.paired && <button onClick={() => remove(d.address)} className="text-xs text-red-400">Remove</button>}
              </div>
            </div>
          ))}
        </>
      )}
    </Section>
  )
}

// --- Audio ---
function AudioSettings() {
  const [status, setStatus] = useState(null)
  const refresh = () => fetch('/api/audio/status').then(r => r.json()).then(setStatus).catch(() => {})
  useEffect(() => { refresh() }, [])

  const setVol = (id, type, volume) => fetch('/api/audio/volume', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ device_id: id, type, volume }) }).then(r => r.json()).then(setStatus)
  const setMute = (id, type, muted) => fetch('/api/audio/mute', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ device_id: id, type, muted }) }).then(r => r.json()).then(setStatus)
  const setDef = (id, type) => fetch('/api/audio/default', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ device_id: id, type }) }).then(r => r.json()).then(setStatus)

  return (
    <Section title="Sound">
      {status && <p className="text-xs text-neutral-600 mb-4">Backend: {status.backend}</p>}
      <h3 className="text-xs uppercase text-neutral-500 tracking-wider mb-2">Output</h3>
      {status?.outputs?.map(d => (
        <AudioDevice key={d.id} device={d} type="output" onVolume={setVol} onMute={setMute} onDefault={setDef} />
      ))}
      <h3 className="text-xs uppercase text-neutral-500 tracking-wider mt-4 mb-2">Input</h3>
      {status?.inputs?.map(d => (
        <AudioDevice key={d.id} device={d} type="input" onVolume={setVol} onMute={setMute} onDefault={setDef} />
      ))}
    </Section>
  )
}

function AudioDevice({ device, type, onVolume, onMute, onDefault }) {
  return (
    <div className="py-2 border-b border-neutral-800/30">
      <div className="flex items-center justify-between mb-1">
        <div className="flex items-center gap-2">
          <button onClick={() => onDefault(device.id, type)} className={`w-3 h-3 rounded-full border ${device.default ? 'bg-blue-500 border-blue-500' : 'border-neutral-600'}`} />
          <span className="text-sm">{device.name}</span>
        </div>
        <button onClick={() => onMute(device.id, type, !device.muted)} className={`text-xs ${device.muted ? 'text-red-400' : 'text-neutral-500'}`}>
          {device.muted ? 'Muted' : 'Mute'}
        </button>
      </div>
      <input type="range" min="0" max="100" value={device.volume} onChange={e => onVolume(device.id, type, parseInt(e.target.value))}
        className="w-full h-1 appearance-none bg-neutral-800 rounded-full [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:w-3 [&::-webkit-slider-thumb]:h-3 [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:bg-white" />
    </div>
  )
}

// --- Display ---
function DisplaySettings() {
  const [status, setStatus] = useState(null)
  const refresh = () => fetch('/api/display/status').then(r => r.json()).then(setStatus).catch(() => {})
  useEffect(() => { refresh() }, [])

  const setBrightness = (v) => fetch('/api/display/brightness', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ brightness: v }) }).then(r => r.json()).then(setStatus)
  const setRes = (output, res) => fetch('/api/display/resolution', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ output, resolution: res }) }).then(r => r.json()).then(setStatus)

  return (
    <Section title="Display">
      {status?.brightness?.device !== 'none' && (
        <Field label={`Brightness (${status?.brightness?.current}%)`}>
          <input type="range" min="5" max="100" value={status?.brightness?.current || 100} onChange={e => setBrightness(parseInt(e.target.value))}
            className="w-full h-1 appearance-none bg-neutral-800 rounded-full [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:w-3 [&::-webkit-slider-thumb]:h-3 [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:bg-white" />
        </Field>
      )}
      <p className="text-xs text-neutral-600 mb-3">Compositor: {status?.compositor}</p>
      {status?.outputs?.map(o => (
        <div key={o.name} className="py-3 border-b border-neutral-800/30">
          <div className="flex items-center gap-2 mb-1">
            <span className={`w-2 h-2 rounded-full ${o.connected ? 'bg-green-500' : 'bg-neutral-600'}`} />
            <span className="text-sm font-medium">{o.name}</span>
            {o.primary && <span className="text-[10px] text-blue-400">primary</span>}
          </div>
          {o.connected && o.modes?.length > 0 && (
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
function EnergySettings() {
  const [status, setStatus] = useState(null)
  const refresh = () => fetch('/api/energy/status').then(r => r.json()).then(setStatus).catch(() => {})
  useEffect(() => { refresh() }, [])

  const setMode = (mode) => fetch('/api/energy/mode', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode }) }).then(r => r.json()).then(setStatus)

  return (
    <Section title="Battery & Energy">
      {status?.battery_percent >= 0 && (
        <div className="mb-4">
          <span className="text-2xl font-light">{status.battery_percent}%</span>
          <span className="text-sm text-neutral-500 ml-2">{status.battery_charging ? 'Charging' : 'On Battery'}</span>
        </div>
      )}
      <Field label="Power Mode">
        <div className="flex gap-2">
          {['performance', 'balanced', 'saver'].map(m => (
            <button key={m} onClick={() => setMode(m)}
              className={`flex-1 py-2 rounded-lg text-sm capitalize transition-colors ${status?.mode === m ? 'bg-neutral-700 text-white' : 'bg-neutral-800/50 text-neutral-400 hover:bg-neutral-800'}`}>
              {m}
            </button>
          ))}
        </div>
      </Field>
      {status && (
        <div className="text-xs text-neutral-600 mt-3 space-y-1">
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
const NET9_MODES = [
  {
    id: 'fabric',
    label: 'Fabric',
    desc: 'Route through the Vulos relay fabric (default; works behind NAT).',
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

function NET9_ConnectionModeSettings() {
  const [current, setCurrent] = useState(null) // server-confirmed mode
  const [pending, setPending] = useState(null) // user-selected mode (pre-apply)
  const [blocked, setBlocked] = useState(false)
  const [status, setStatus] = useState(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')

  const NET9_refresh = useCallback(() => {
    setLoading(true)
    fetch('/api/network/mode')
      .then(r => (r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status))))
      .then(d => {
        setCurrent(d.mode)
        setPending(d.mode)
        setBlocked(!!d.external_listener_blocked)
        setStatus(d.status || null)
        setError('')
      })
      .catch(e => setError(e.message || 'failed to load'))
      .finally(() => setLoading(false))
  }, [])

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
        const body = await r.json().catch(() => ({}))
        if (!r.ok) throw new Error(body.error || ('HTTP ' + r.status))
        return body
      })
      .then(d => {
        setCurrent(d.mode)
        setPending(d.mode)
        setBlocked(!!d.external_listener_blocked)
        setStatus(d.status || null)
        setSaved(true)
        setTimeout(() => setSaved(false), 2000)
      })
      .catch(e => setError(e.message || 'failed to apply'))
      .finally(() => setSaving(false))
  }

  const dirty = pending && pending !== current

  return (
    <Section title="Connection Mode">
      <p className="text-xs text-neutral-600 mb-5">
        Choose how this device reaches the outside world. The setting is persisted to
        <code className="mx-1 text-neutral-400">~/.vulos/db/network-mode.json</code>
        and survives reboots. Switching modes never changes this node's identity (ULID).
      </p>

      <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50 mb-5">
        <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
          <span className="text-xs text-neutral-500">Active mode</span>
          <span className={`text-sm font-medium ${current ? 'text-green-400' : 'text-neutral-500'}`}>
            {loading ? '…' : (current || 'unknown')}
          </span>
        </div>
        <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
          <span className="text-xs text-neutral-500">External listener</span>
          <span className={`text-sm font-medium ${blocked ? 'text-yellow-400' : 'text-neutral-300'}`}>
            {blocked ? 'blocked (local-only)' : 'enabled'}
          </span>
        </div>
        {status?.domain && (
          <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
            <span className="text-xs text-neutral-500">Resolved domain</span>
            <span className="text-sm text-neutral-300">{status.domain}</span>
          </div>
        )}
        {status?.instance_id && (
          <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
            <span className="text-xs text-neutral-500">Instance ID</span>
            <span className="text-sm text-neutral-300 font-mono truncate ml-3">{status.instance_id}</span>
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
                  ? 'border-blue-600/60 bg-blue-600/10'
                  : 'border-neutral-800/60 bg-neutral-900/30 hover:bg-neutral-900/60'
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
                  <span className="text-sm font-medium text-neutral-200">{m.label}</span>
                  {current === m.id && (
                    <span className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-green-900/40 text-green-400">
                      Active
                    </span>
                  )}
                </div>
                <p className="text-xs text-neutral-500 mt-0.5">{m.desc}</p>
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

      {error && (
        <div className="mt-3 text-xs rounded px-3 py-2 bg-red-900/30 text-red-400">
          {error}
        </div>
      )}
    </Section>
  )
}

function NetworkSettings() {
  const [config, setConfig] = useState({ app_url: 'http://localhost:8080' })
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    fetch('/api/network/config').then(r => r.ok ? r.json() : null).then(d => d && !d.error && setConfig(d)).catch(() => {})
  }, [])

  const saveConfig = () => {
    setSaving(true)
    fetch('/api/network/configure', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(config)
    }).then(r => r.json()).then(d => { setConfig(d); setSaved(true); setTimeout(() => setSaved(false), 2000) }).finally(() => setSaving(false))
  }

  return (
    <Section title="Remote Access">
      <p className="text-xs text-neutral-600 mb-5">Configure how this device is reached from the network. If you're accessing remotely, set the URL to your public IP or domain.</p>

      <label className="block text-sm text-neutral-400 mb-1">Access URL</label>
      <input
        value={config.app_url || ''}
        onChange={e => setConfig(c => ({ ...c, app_url: e.target.value }))}
        placeholder="http://localhost:8080"
        className="w-full bg-neutral-900 border border-neutral-700 rounded px-3 py-1.5 text-sm mb-1"
      />
      <p className="text-xs text-neutral-600 mb-5">The address used to reach this device. For local use, leave as localhost. For remote access, set to your IP or domain (e.g. http://41.193.144.126:8080).</p>

      <button onClick={saveConfig} disabled={saving} className="btn text-sm">
        {saving ? 'Saving...' : saved ? 'Saved' : 'Save'}
      </button>
    </Section>
  )
}

// --- TURN / WebRTC (NET-10) ---
function TURNSettingsSection() {
  const [host, setHost] = useState('')
  const [port, setPort] = useState(3478)
  const [realm, setRealm] = useState('vulos')
  const [secret, setSecret] = useState('')
  const [configured, setConfigured] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [testResult, setTestResult] = useState(null)
  const [testing, setTesting] = useState(false)

  useEffect(() => {
    fetch('/api/turn/config')
      .then(r => r.ok ? r.json() : null)
      .then(d => {
        if (!d || d.error) return
        setHost(d.host || '')
        setPort(d.port || 3478)
        setRealm(d.realm || 'vulos')
        setConfigured(!!d.configured)
      })
      .catch(() => {})
  }, [])

  const turnSettings_save = () => {
    setSaving(true)
    setSaved(false)
    fetch('/api/turn/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ host, port: Number(port), realm, secret: secret || undefined }),
    })
      .then(r => r.json())
      .then(d => {
        if (d && !d.error) {
          setConfigured(!!d.configured)
          setSecret('')
          setSaved(true)
          setTimeout(() => setSaved(false), 2000)
        }
      })
      .finally(() => setSaving(false))
  }

  const turnSettings_test = () => {
    setTesting(true)
    setTestResult(null)
    fetch('/api/turn/test', { method: 'POST' })
      .then(r => r.json())
      .then(setTestResult)
      .catch(e => setTestResult({ success: false, error: e.message }))
      .finally(() => setTesting(false))
  }

  return (
    <Section title="TURN / WebRTC Relay">
      <p className="text-xs text-neutral-600 mb-5">
        Users behind strict NAT need a TURN relay for reliable WebRTC.
        Point at your own coturn server or a tier-provided one.
        The shared secret is stored securely and never returned by the API.
      </p>

      {configured && (
        <div className="text-xs text-green-500 mb-3">TURN server configured</div>
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
        <p className="text-xs text-neutral-600 mt-1">Write-only — the secret is never returned once saved.</p>
      </Field>

      <div className="flex gap-3 mt-4 items-center">
        <button onClick={turnSettings_save} disabled={saving} className="btn text-sm">
          {saving ? 'Saving…' : saved ? 'Saved' : 'Save'}
        </button>
        <button onClick={turnSettings_test} disabled={testing || !configured} className="btn text-sm">
          {testing ? 'Testing…' : 'Test Reachability'}
        </button>
      </div>

      {testResult && (
        <div className={`mt-3 text-xs rounded px-3 py-2 ${testResult.success ? 'bg-green-900/30 text-green-400' : 'bg-red-900/30 text-red-400'}`}>
          {testResult.success
            ? `Reachable — latency ${testResult.latency_ms} ms`
            : `Unreachable: ${testResult.error || 'connection failed'}`}
        </div>
      )}
    </Section>
  )
}

// --- Account ---
function AccountSettings({ profile, updateProfile, logout }) {
  const [name, setName] = useState(profile?.display_name || '')
  const [locale, setLocale] = useState(profile?.locale || 'en')
  const [tz, setTz] = useState(profile?.timezone || '')

  const save = () => updateProfile({ display_name: name, locale, timezone: tz })

  return (
    <Section title="Account">
      <Field label="Display Name"><input value={name} onChange={e => setName(e.target.value)} className="input" /></Field>
      <Field label="Language"><input value={locale} onChange={e => setLocale(e.target.value)} placeholder="en" className="input" /></Field>
      <Field label="Timezone"><input value={tz} onChange={e => setTz(e.target.value)} placeholder="Africa/Johannesburg" className="input" /></Field>
      <button onClick={save} className="btn mt-3">Save</button>
      <button onClick={logout} className="btn-ghost mt-6 text-red-400">Log Out</button>
    </Section>
  )
}

// --- AI Apps Gallery ---
function AIAppVersions({ appId, onClose }) {
  const [versions, setVersions] = useState([])
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState('')

  useEffect(() => {
    fetch(`/api/ai-apps/${appId}/versions`)
      .then(r => r.json())
      .then(setVersions)
      .catch(() => setVersions([]))
  }, [appId])

  const rollback = async (version) => {
    setBusy(true)
    setMsg('')
    try {
      const r = await fetch(`/api/ai-apps/${appId}/rollback`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ version }),
      })
      const d = await r.json()
      if (!r.ok) setMsg(d.error || 'Rollback failed')
      else setMsg('Rolled back successfully')
    } catch {
      setMsg('Request failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mt-2 mb-4 rounded border border-neutral-700 bg-neutral-900 p-3">
      <div className="flex items-center justify-between mb-2">
        <span className="text-xs font-semibold text-neutral-300">Version History</span>
        <button onClick={onClose} className="text-xs text-neutral-500 hover:text-neutral-300">Close</button>
      </div>
      {versions.length === 0 && <p className="text-xs text-neutral-500">No snapshots yet.</p>}
      {versions.map(v => (
        <div key={v.version} className="flex items-center justify-between py-1 border-b border-neutral-800/30">
          <div>
            <span className="text-xs text-neutral-400">{v.timestamp}</span>
            {v.brief && <span className="text-[10px] text-neutral-600 ml-2">{v.brief}</span>}
          </div>
          <button
            onClick={() => rollback(v.version)}
            disabled={busy}
            className="text-[10px] text-amber-400 hover:text-amber-300 disabled:opacity-50"
          >
            Restore
          </button>
        </div>
      ))}
      {msg && <p className="text-xs mt-2 text-green-400">{msg}</p>}
    </div>
  )
}

function AIAppsSettings() {
  const [apps, setApps] = useState([])
  const [visRefreshKey, setVisRefreshKey] = useState(0)
  const [versionsOpen, setVersionsOpen] = useState(null)
  const refresh = useCallback(() => fetch('/api/ai-apps').then(r => r.json()).then(setApps).catch(() => {}), [])
  useEffect(() => { refresh() }, [refresh])

  const remove = async (id) => {
    await fetch(`/api/ai-apps/${id}`, { method: 'DELETE' })
    if (versionsOpen === id) setVersionsOpen(null)
    refresh()
  }

  const handleVisChanged = useCallback(() => { setVisRefreshKey(k => k + 1) }, [])
  const toggleVersions = (id) => { setVersionsOpen(prev => prev === id ? null : id) }

  return (
    <Section title="AI-Generated Apps">
      <p className="text-xs text-neutral-600 mb-4">Apps created by the AI assistant. Click to reopen.</p>
      {apps?.length === 0 && <p className="text-sm text-neutral-500">No saved apps yet. Ask the AI to build something visual.</p>}
      {apps?.map(app => (
        <div key={app.id} className="flex items-center justify-between py-2 border-b border-neutral-800/30 gap-2">
          <div className="min-w-0">
            <span className="text-sm">{app.title || 'Untitled'}</span>
            <span className="text-xs text-neutral-600 ml-2">{app.created?.slice(0, 10)}</span>
            {app.has_python === 'true' && <span className="text-[10px] text-blue-400 ml-2">Python</span>}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <PamVisibilityControl key={`${app.id}-${visRefreshKey}`} appId={app.id} onChanged={handleVisChanged} />
            <button onClick={() => toggleVersions(app.id)} className="text-xs text-neutral-400">Versions</button>
            <button onClick={() => window.open(`/api/ai-apps/${app.id}/html`, '_blank')} className="text-xs text-blue-400">Open</button>
            <button onClick={() => remove(app.id)} className="text-xs text-red-400">Delete</button>
          </div>
          {versionsOpen === app.id && (
            <AIAppVersions appId={app.id} onClose={() => setVersionsOpen(null)} />
          )}
        </div>
      ))}
    </Section>
  )
}

// --- Vault / Backup ---
function VaultSettings() {
  const [status, setStatus] = useState(null)
  const [sync, setSync] = useState(null)
  const refresh = () => {
    fetch('/api/vault/status').then(r => r.json()).then(setStatus).catch(() => {})
    fetch('/api/vault/sync').then(r => r.json()).then(setSync).catch(() => {})
  }
  useEffect(() => { refresh() }, [])

  const backup = () => fetch('/api/vault/backup', { method: 'POST' }).then(refresh)
  const syncDevice = () => fetch('/api/vault/sync', { method: 'POST' }).then(refresh)

  return (
    <Section title="Backup & Sync">
      <div className={`text-sm mb-3 ${status?.initialized ? 'text-green-400' : 'text-neutral-500'}`}>
        {status?.initialized ? 'Vault initialized' : 'Vault not configured'}
      </div>
      {status?.initialized && (
        <>
          <p className="text-xs text-neutral-500 mb-1">Last backup: {status?.last_backup || 'never'}</p>
          <p className="text-xs text-neutral-500 mb-3">Snapshots: {sync?.total_snapshots || 0}</p>
          <div className="flex gap-2 mb-4">
            <button onClick={backup} className="btn">Backup Now</button>
            <button onClick={syncDevice} className="btn">Sync to This Device</button>
          </div>
          {sync?.other_devices?.length > 0 && (
            <>
              <h3 className="text-xs uppercase text-neutral-500 tracking-wider mb-2">Other Devices</h3>
              {sync.other_devices.map(d => <p key={d} className="text-sm text-neutral-400">{d}</p>)}
            </>
          )}
        </>
      )}
      {!status?.initialized && (
        <p className="text-xs text-neutral-600">Set S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY, S3_SECRET_KEY to enable backups.</p>
      )}
    </Section>
  )
}

// --- Recall / Search ---
function RecallSettings() {
  const [status, setStatus] = useState(null)
  const refresh = () => fetch('/api/recall/status').then(r => r.json()).then(setStatus).catch(() => {})
  useEffect(() => { refresh() }, [])

  const reindex = () => fetch('/api/recall/index', { method: 'POST' }).then(refresh)

  return (
    <Section title="Search & Index">
      <p className="text-xs text-neutral-600 mb-3">Recall indexes your files for semantic search. The AI uses this to answer questions about your data.</p>
      {status && (
        <div className="space-y-1 text-sm mb-4">
          <p>Files indexed: <span className="text-neutral-300">{status.indexed_files || 0}</span></p>
          <p>Total scanned: <span className="text-neutral-300">{status.total_files || 0}</span></p>
          <p>Last index: <span className="text-neutral-300">{status.last_index || 'never'}</span></p>
          <p>Status: <span className={status.indexing ? 'text-amber-400' : 'text-green-400'}>{status.indexing ? 'Indexing...' : 'Ready'}</span></p>
        </div>
      )}
      <button onClick={reindex} className="btn">Re-index Now</button>
    </Section>
  )
}

// --- Storage / Cluster Sync (CLUSTER-06) ---
function StorageSettings() {
  const [status, setStatus] = useState(null)
  const [showModal, setShowModal] = useState(false)
  const [form, setForm] = useState({ endpoint: '', bucket: '', region: 'us-east-1', access_key: '', secret_key: '' })
  const [saving, setSaving] = useState(false)
  const [saveMsg, setSaveMsg] = useState('')

  const refresh = () =>
    fetch('/api/storage/status')
      .then(r => r.json())
      .then(setStatus)
      .catch(() => {})

  useEffect(() => { refresh() }, [])

  const enable = async (e) => {
    e.preventDefault()
    setSaving(true)
    setSaveMsg('')
    try {
      const res = await fetch('/api/setup/storage', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(form),
      })
      if (res.ok) {
        setSaveMsg('Saved')
        setShowModal(false)
        setTimeout(() => setSaveMsg(''), 3000)
        refresh()
      } else {
        const d = await res.json().catch(() => ({}))
        setSaveMsg(d.error || 'Failed to save')
      }
    } catch {
      setSaveMsg('Could not reach server')
    } finally {
      setSaving(false)
    }
  }

  const fmtGB = (gb) => (gb > 0 ? `${gb.toFixed(1)} GB` : '—')

  return (
    <Section title="Storage / Cluster Sync">
      <p className="text-xs text-neutral-600 mb-4">
        Configure an S3-compatible object store (AWS S3, MinIO, Wasabi, Backblaze) used for vault backups and cluster sync.
      </p>

      <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50 mb-5">
        <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
          <span className="text-xs text-neutral-500">Status</span>
          <span className={`text-sm font-medium ${status?.configured ? 'text-green-400' : 'text-neutral-500'}`}>
            {status == null ? '…' : status.configured ? 'Configured' : 'Disabled'}
          </span>
        </div>
        {status?.configured && (
          <>
            <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
              <span className="text-xs text-neutral-500">Bucket</span>
              <span className="text-sm text-neutral-300">{status.bucket || '—'}</span>
            </div>
            <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
              <span className="text-xs text-neutral-500">Region</span>
              <span className="text-sm text-neutral-300">{status.region || '—'}</span>
            </div>
            <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
              <span className="text-xs text-neutral-500">Allocated</span>
              <span className="text-sm text-neutral-300">{fmtGB(status.size_gb)}</span>
            </div>
            <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
              <span className="text-xs text-neutral-500">Used</span>
              <span className="text-sm text-neutral-300">{fmtGB(status.used_gb)}</span>
            </div>
          </>
        )}
      </div>

      <div className="flex gap-3 items-center">
        <button onClick={() => setShowModal(true)} className="btn text-sm">
          {status?.configured ? 'Reconfigure' : 'Enable Storage'}
        </button>
        {saveMsg && <span className="text-xs text-green-400">{saveMsg}</span>}
      </div>

      {showModal && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
          <div className="bg-neutral-900 border border-neutral-700 rounded-2xl p-6 w-full max-w-sm shadow-2xl">
            <h3 className="text-base font-semibold mb-4">Configure Object Storage</h3>
            <form onSubmit={enable} className="space-y-3">
              <Field label="Endpoint">
                <input
                  value={form.endpoint}
                  onChange={e => setForm(f => ({ ...f, endpoint: e.target.value }))}
                  placeholder="s3.amazonaws.com or minio:9000"
                  className="input"
                  required
                />
              </Field>
              <Field label="Bucket">
                <input
                  value={form.bucket}
                  onChange={e => setForm(f => ({ ...f, bucket: e.target.value }))}
                  placeholder="my-vulos-bucket"
                  className="input"
                  required
                />
              </Field>
              <Field label="Region">
                <input
                  value={form.region}
                  onChange={e => setForm(f => ({ ...f, region: e.target.value }))}
                  placeholder="us-east-1"
                  className="input"
                />
              </Field>
              <Field label="Access Key">
                <input
                  value={form.access_key}
                  onChange={e => setForm(f => ({ ...f, access_key: e.target.value }))}
                  placeholder="AKIA…"
                  className="input"
                  required
                />
              </Field>
              <Field label="Secret Key">
                <input
                  type="password"
                  value={form.secret_key}
                  onChange={e => setForm(f => ({ ...f, secret_key: e.target.value }))}
                  placeholder="••••••••"
                  className="input"
                  required
                />
              </Field>
              {saveMsg && <p className="text-xs text-red-400">{saveMsg}</p>}
              <div className="flex gap-2 pt-1">
                <button type="submit" disabled={saving} className="btn flex-1">
                  {saving ? 'Saving…' : 'Save'}
                </button>
                <button type="button" onClick={() => { setShowModal(false); setSaveMsg('') }} className="btn-ghost flex-1">
                  Cancel
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </Section>
  )
}

// --- Users & Profiles ---
function UsersSettings({ profile }) {
  const [profiles, setProfiles] = useState([])
  const [pin, setPin] = useState('')
  const [newUser, setNewUser] = useState({ username: '', password: '', displayName: '' })
  const [addError, setAddError] = useState('')
  const [addSuccess, setAddSuccess] = useState('')
  const refresh = () => fetch('/api/profiles').then(r => r.json()).then(setProfiles).catch(() => {})
  useEffect(() => { refresh() }, [])

  const isAdmin = profile?.role === 'admin'

  const setRole = async (userId, role) => {
    await fetch(`/api/profiles/${userId}/role`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ role }),
    })
    refresh()
  }

  const removeUser = async (userId) => {
    if (!confirm('Remove this user? This cannot be undone.')) return
    await fetch(`/api/profiles/${userId}`, { method: 'DELETE' })
    refresh()
  }

  const addUser = async (e) => {
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
      const data = await res.json()
      if (!res.ok) {
        setAddError(data.error || 'Failed to create user')
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

  const savePin = async () => {
    await fetch('/api/auth/pin/set', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ pin }),
    })
    setPin('')
  }

  return (
    <Section title="Users & Profiles">
      {/* PIN */}
      <div className="mb-6 pb-4 border-b border-neutral-800/50">
        <h3 className="text-sm font-medium mb-2">Lock Screen PIN</h3>
        <div className="flex gap-2">
          <input type="password" value={pin} onChange={e => setPin(e.target.value.replace(/[^0-9]/g, ''))}
            placeholder="4-6 digit PIN" maxLength={6} className="input w-40" />
          <button onClick={savePin} className="btn">{pin ? 'Set PIN' : 'Remove PIN'}</button>
        </div>
      </div>

      {/* Add user (admin only) */}
      {isAdmin && (
        <div className="mb-6 pb-4 border-b border-neutral-800/50">
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
            {addError && <p className="text-xs text-red-400">{addError}</p>}
            {addSuccess && <p className="text-xs text-green-400">{addSuccess}</p>}
          </form>
        </div>
      )}

      {/* User list */}
      <h3 className="text-sm font-medium mb-2">All Users</h3>
      {profiles.map(p => (
        <div key={p.user_id} className="flex items-center justify-between py-2 border-b border-neutral-800/30">
          <div>
            <span className="text-sm">{p.display_name || 'Unnamed'}</span>
            <span className={`ml-2 text-[10px] px-1.5 py-0.5 rounded-full ${
              p.role === 'admin' ? 'bg-blue-900/50 text-blue-300' :
              p.role === 'guest' ? 'bg-neutral-800 text-neutral-500' :
              'bg-neutral-800 text-neutral-400'
            }`}>{p.role}</span>
          </div>
          {isAdmin && p.user_id !== profile.user_id && (
            <div className="flex gap-2">
              <select value={p.role} onChange={e => setRole(p.user_id, e.target.value)} className="input text-xs py-1 w-24">
                <option value="admin">Admin</option>
                <option value="user">User</option>
                <option value="guest">Guest</option>
              </select>
              <button onClick={() => removeUser(p.user_id)} className="text-xs text-red-400 hover:text-red-300">Remove</button>
            </div>
          )}
        </div>
      ))}
      {!isAdmin && <p className="text-xs text-neutral-600 mt-2">Only admins can manage users.</p>}
    </Section>
  )
}

// --- About ---
function AboutSettings() {
  const [health, setHealth] = useState(null)
  const [sys, setSys] = useState(null)

  useEffect(() => {
    fetch('/health').then(r => r.json()).then(setHealth).catch(() => {})
    fetch('/api/system/info').then(r => r.json()).then(setSys).catch(() => {})
  }, [])

  const fmtMB = (mb) => {
    if (!mb) return '—'
    return mb >= 1024 ? (mb / 1024).toFixed(1) + ' GB' : mb + ' MB'
  }

  return (
    <div>
      {/* Branding header */}
      <div className="flex flex-col items-center text-center mb-8 pt-2">
        <img src="/vulos.png" alt="Vula OS" className="w-20 h-20 mb-4 opacity-90" />
        <h1 className="text-2xl font-semibold tracking-tight">Vula OS</h1>
        <p className="text-sm text-neutral-500 mt-1">Open OS</p>
        <p className="text-xs text-neutral-600 mt-0.5">"vula" — Zulu for "open"</p>
      </div>

      {/* System info */}
      <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50 mb-6">
        <InfoRow label="Device" value={sys?.device_model || sys?.hostname || '—'} />
        <InfoRow label="Hostname" value={sys?.hostname} />
        <InfoRow label="OS" value={sys?.os_version ? `Debian ${sys.os_version}` : 'Debian Linux'} />
        <InfoRow label="Kernel" value={sys?.kernel} />
        <InfoRow label="Architecture" value={sys?.arch} />
      </div>

      {/* Hardware */}
      <h3 className="text-xs uppercase text-neutral-500 tracking-wider mb-2">Hardware</h3>
      <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50 mb-6">
        <InfoRow label="Processor" value={sys?.cpu_model || `${sys?.cpu_cores || '—'} cores`} />
        <InfoRow label="CPU Cores" value={sys?.cpu_cores} />
        <InfoRow label="Memory" value={sys ? `${fmtMB(sys.mem_used_mb)} used of ${fmtMB(sys.mem_total_mb)}` : '—'} />
        {sys?.mem_percent > 0 && (
          <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
            <span className="text-xs text-neutral-500">RAM Usage</span>
            <div className="flex items-center gap-2">
              <div className="w-32 h-1.5 bg-neutral-800 rounded-full overflow-hidden">
                <div
                  className={`h-full rounded-full transition-all ${sys.mem_percent > 80 ? 'bg-red-500' : sys.mem_percent > 60 ? 'bg-amber-500' : 'bg-blue-500'}`}
                  style={{ width: `${Math.min(sys.mem_percent, 100)}%` }}
                />
              </div>
              <span className="text-xs text-neutral-400 w-10 text-right">{Math.round(sys.mem_percent)}%</span>
            </div>
          </div>
        )}
        <InfoRow label="Storage" value={sys?.storage_total_mb ? `${fmtMB(sys.storage_used_mb)} used of ${fmtMB(sys.storage_total_mb)}` : '—'} />
        {sys?.battery >= 0 && (
          <InfoRow label="Battery" value={`${sys.battery}%${sys.charging ? ' (Charging)' : ''}`} />
        )}
      </div>

      {/* GPU */}
      {sys && (
        <>
          <h3 className="text-xs uppercase text-neutral-500 tracking-wider mb-2">Graphics</h3>
          <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50 mb-6">
            <InfoRow label="GPU" value={sys.gpu_device || '—'} />
            <InfoRow label="Vendor" value={sys.gpu_vendor !== 'none' ? sys.gpu_vendor : 'None'} />
            <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
              <span className="text-xs text-neutral-500">Tier</span>
              <span className={`text-sm font-medium ${
                sys.gpu_tier === 'nvenc' ? 'text-green-400' :
                sys.gpu_tier === 'vaapi' ? 'text-blue-400' :
                'text-neutral-400'
              }`}>
                {sys.gpu_tier === 'nvenc' ? 'Tier 2 — NVENC' :
                 sys.gpu_tier === 'vaapi' ? 'Tier 1 — VA-API' :
                 'Tier 0 — Software'}
              </span>
            </div>
            <InfoRow label="Encoder" value={sys.gpu_encoder} />
            <InfoRow label="Codec" value={sys.gpu_codec || '—'} />
            {sys.gpu_av1 && (
              <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
                <span className="text-xs text-neutral-500">AV1 Encode</span>
                <span className="text-sm text-green-400 font-medium">Supported</span>
              </div>
            )}
            <InfoRow label="Capture" value={sys.gpu_pipewire ? 'PipeWire DMA-BUF' : 'X11 SHM'} />
          </div>
        </>
      )}

      {/* Runtime */}
      <h3 className="text-xs uppercase text-neutral-500 tracking-wider mb-2">Runtime</h3>
      <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50 mb-6">
        <InfoRow label="Uptime" value={sys?.uptime} />
        <InfoRow label="Server" value={health?.status === 'ok' ? 'Running' : 'Unreachable'} ok={health?.status === 'ok'} />
        <InfoRow label="Shell" value="React 19 + Tailwind 4 + Vite" />
        <InfoRow label="Backend" value="Go + Debian Linux" />
      </div>

      {/* Powered by */}
      <div className="flex items-center justify-center gap-3 mt-6 pt-4 border-t border-neutral-800/30">
        <span className="text-[11px] text-neutral-600">Powered by</span>
        <span className="text-xs text-neutral-400 font-medium">Debian Linux</span>
      </div>
    </div>
  )
}

function InfoRow({ label, value, ok }) {
  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
      <span className="text-xs text-neutral-500">{label}</span>
      <span className={`text-sm ${ok != null ? (ok ? 'text-green-400' : 'text-red-400') : 'text-neutral-300'}`}>
        {value || '—'}
      </span>
    </div>
  )
}

// --- Shared UI components ---
function Section({ title, children }) {
  return <div><h2 className="text-lg font-medium mb-4">{title}</h2>{children}</div>
}

function Field({ label, children }) {
  return <div className="mb-3"><label className="block text-xs text-neutral-500 mb-1">{label}</label>{children}</div>
}

function Toggle({ label, checked, onChange }) {
  return (
    <div className="flex items-center justify-between py-2">
      <span className="text-sm">{label}</span>
      <button onClick={() => onChange(!checked)}
        className={`w-10 h-5 rounded-full transition-colors relative ${checked ? 'bg-blue-600' : 'bg-neutral-700'}`}>
        <span className={`absolute top-0.5 w-4 h-4 rounded-full bg-white transition-transform ${checked ? 'left-5' : 'left-0.5'}`} />
      </button>
    </div>
  )
}
