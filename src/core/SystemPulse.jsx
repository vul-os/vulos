import { useState, useEffect, useRef, useCallback } from 'react'
import { useTelemetry } from './useTelemetry'
import { useTheme } from './ThemeProvider'
import { useAuth } from '../auth/AuthProvider'

// --- Hooks ---
function useTime() {
  const [now, setNow] = useState(new Date())
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(id)
  }, [])
  return now
}

function useClickOutside(ref, handler) {
  useEffect(() => {
    const listener = (e) => { if (ref.current && !ref.current.contains(e.target)) handler() }
    document.addEventListener('mousedown', listener)
    return () => document.removeEventListener('mousedown', listener)
  }, [ref, handler])
}

function formatTime(date) {
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}
function formatTimeSec(date) {
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}
function formatDate(date) {
  return date.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' })
}
function formatDateLong(date) {
  return date.toLocaleDateString([], { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' })
}
function fmtBytes(b) {
  if (!b || b <= 0) return '0'
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0, v = b
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i > 0 ? 1 : 0)} ${units[i]}`
}

// --- Battery icon SVG ---
function BatteryIcon({ percent, charging, size = 14 }) {
  const fill = percent > 20 ? (percent > 50 ? '#22c55e' : '#eab308') : '#ef4444'
  const w = Math.max(0, Math.min(100, percent)) / 100 * 8
  return (
    <svg width={size} height={size} viewBox="0 0 16 12" fill="none">
      <rect x="0.5" y="1.5" width="12" height="9" rx="1.5" stroke="currentColor" strokeWidth="1" />
      <rect x="12.5" y="4" width="2" height="4" rx="0.5" fill="currentColor" opacity="0.4" />
      <rect x="2" y="3" width={w} height="6" rx="0.5" fill={fill} />
      {charging && <path d="M7 2l-2 4h3l-2 4" stroke="#eab308" strokeWidth="1.2" strokeLinecap="round" strokeLinejoin="round" fill="none" />}
    </svg>
  )
}

// --- WiFi icon SVG ---
function WifiIcon({ connected, size = 14 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none">
      {connected ? (
        <>
          <path d="M1 6c3.5-4 10.5-4 14 0" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" opacity="0.4" />
          <path d="M3 9c2.5-3 7.5-3 10 0" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" opacity="0.6" />
          <path d="M5.5 12c1.5-2 3.5-2 5 0" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
          <circle cx="8" cy="14" r="1" fill="currentColor" />
        </>
      ) : (
        <>
          <path d="M1 6c3.5-4 10.5-4 14 0" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" opacity="0.15" />
          <path d="M3 9c2.5-3 7.5-3 10 0" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" opacity="0.15" />
          <path d="M5.5 12c1.5-2 3.5-2 5 0" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" opacity="0.15" />
          <line x1="2" y1="2" x2="14" y2="14" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" opacity="0.5" />
        </>
      )}
    </svg>
  )
}

// --- Dropdown wrapper ---
function Dropdown({ open, onClose, align = 'right', containerRef, children }) {
  const ref = useRef(null)
  // Close on click outside, but ignore clicks on the trigger button
  useEffect(() => {
    if (!open) return
    const listener = (e) => {
      if (ref.current && !ref.current.contains(e.target) &&
          (!containerRef?.current || !containerRef.current.contains(e.target))) {
        onClose()
      }
    }
    document.addEventListener('mousedown', listener)
    return () => document.removeEventListener('mousedown', listener)
  }, [open, onClose, containerRef])

  if (!open) return null
  return (
    <div ref={ref} className={`absolute top-full mt-1.5 ${align === 'right' ? 'right-0' : 'left-0'} z-[100]
      bg-neutral-900/95 backdrop-blur-xl border border-neutral-700/50 rounded-xl shadow-2xl shadow-black/50
      overflow-hidden animate-[fadeIn_0.12s_ease-out]`}>
      {children}
    </div>
  )
}

// --- Clock + Calendar Dropdown ---
function ClockDropdown({ now }) {
  const [monthOffset, setMonthOffset] = useState(0)

  const viewDate = new Date(now.getFullYear(), now.getMonth() + monthOffset, 1)
  const year = viewDate.getFullYear()
  const month = viewDate.getMonth()
  const daysInMonth = new Date(year, month + 1, 0).getDate()
  const firstDay = new Date(year, month, 1).getDay()
  const today = now.getDate()
  const isCurrentMonth = monthOffset === 0
  const monthName = viewDate.toLocaleDateString([], { month: 'long', year: 'numeric' })

  const days = []
  for (let i = 0; i < firstDay; i++) days.push(null)
  for (let d = 1; d <= daysInMonth; d++) days.push(d)

  // Analog clock
  const hours = now.getHours() % 12
  const minutes = now.getMinutes()
  const seconds = now.getSeconds()
  const hourAngle = (hours + minutes / 60) * 30
  const minuteAngle = (minutes + seconds / 60) * 6
  const secondAngle = seconds * 6

  return (
    <div className="w-[280px]">
      {/* Analog clock */}
      <div className="flex flex-col items-center pt-5 pb-3">
        <svg width="120" height="120" viewBox="0 0 120 120">
          <circle cx="60" cy="60" r="56" fill="none" stroke="#333" strokeWidth="1" />
          {/* Hour markers */}
          {[...Array(12)].map((_, i) => {
            const angle = i * 30 * Math.PI / 180
            const x1 = 60 + 48 * Math.sin(angle), y1 = 60 - 48 * Math.cos(angle)
            const x2 = 60 + 52 * Math.sin(angle), y2 = 60 - 52 * Math.cos(angle)
            return <line key={i} x1={x1} y1={y1} x2={x2} y2={y2} stroke="#555" strokeWidth={i % 3 === 0 ? 2 : 1} strokeLinecap="round" />
          })}
          {/* Hour hand */}
          <line x1="60" y1="60"
            x2={60 + 30 * Math.sin(hourAngle * Math.PI / 180)}
            y2={60 - 30 * Math.cos(hourAngle * Math.PI / 180)}
            stroke="#e5e5e5" strokeWidth="2.5" strokeLinecap="round" />
          {/* Minute hand */}
          <line x1="60" y1="60"
            x2={60 + 40 * Math.sin(minuteAngle * Math.PI / 180)}
            y2={60 - 40 * Math.cos(minuteAngle * Math.PI / 180)}
            stroke="#e5e5e5" strokeWidth="1.5" strokeLinecap="round" />
          {/* Second hand */}
          <line x1="60" y1="60"
            x2={60 + 44 * Math.sin(secondAngle * Math.PI / 180)}
            y2={60 - 44 * Math.cos(secondAngle * Math.PI / 180)}
            stroke="#3b82f6" strokeWidth="0.8" strokeLinecap="round" />
          <circle cx="60" cy="60" r="3" fill="#e5e5e5" />
        </svg>
        <div className="text-lg font-mono text-neutral-200 mt-2">{formatTimeSec(now)}</div>
        <div className="text-xs text-neutral-500">{formatDateLong(now)}</div>
      </div>

      <div className="border-t border-neutral-800/60" />

      {/* Calendar */}
      <div className="p-3">
        <div className="flex items-center justify-between mb-2">
          <button onClick={() => setMonthOffset(o => o - 1)} className="w-6 h-6 flex items-center justify-center rounded hover:bg-neutral-800 text-neutral-400 text-sm">&lsaquo;</button>
          <span className="text-xs font-medium text-neutral-300">{monthName}</span>
          <button onClick={() => setMonthOffset(o => o + 1)} className="w-6 h-6 flex items-center justify-center rounded hover:bg-neutral-800 text-neutral-400 text-sm">&rsaquo;</button>
        </div>
        {/* Day headers */}
        <div className="grid grid-cols-7 gap-0 mb-1">
          {['Su', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa'].map(d => (
            <div key={d} className="text-center text-[10px] text-neutral-600 py-0.5">{d}</div>
          ))}
        </div>
        {/* Days grid */}
        <div className="grid grid-cols-7 gap-0">
          {days.map((d, i) => (
            <div key={i} className={`text-center py-1 text-xs rounded-md transition-colors
              ${!d ? '' :
                (d === today && isCurrentMonth)
                  ? 'bg-blue-600 text-white font-semibold'
                  : 'text-neutral-400 hover:bg-neutral-800/60 cursor-default'
              }`}>
              {d || ''}
            </div>
          ))}
        </div>
        {monthOffset !== 0 && (
          <button onClick={() => setMonthOffset(0)} className="w-full mt-2 text-[11px] text-blue-400 hover:text-blue-300 text-center">
            Today
          </button>
        )}
      </div>
    </div>
  )
}

// --- WiFi Dropdown ---
function WifiDropdown({ connected }) {
  const [status, setStatus] = useState(null)
  const [networks, setNetworks] = useState(null)
  const [scanning, setScanning] = useState(false)

  useEffect(() => {
    fetch('/api/wifi/status').then(r => r.json()).then(setStatus).catch(() => {})
  }, [])

  const scan = async () => {
    setScanning(true)
    try {
      const res = await fetch('/api/wifi/scan')
      setNetworks(await res.json())
    } catch { setNetworks([]) }
    setScanning(false)
  }

  return (
    <div className="w-[260px]">
      <div className="px-4 pt-3 pb-2">
        <div className="flex items-center justify-between">
          <span className="text-xs font-medium text-neutral-300">Wi-Fi</span>
          <span className={`text-[10px] px-1.5 py-0.5 rounded-full ${
            status?.connected ? 'bg-green-900/40 text-green-400' : 'bg-neutral-800 text-neutral-500'}`}>
            {status?.connected ? 'Connected' : 'Off'}
          </span>
        </div>
        {status?.connected && (
          <div className="mt-1.5">
            <div className="text-sm text-neutral-200">{status.ssid}</div>
            <div className="text-[11px] text-neutral-600">{status.ip}</div>
          </div>
        )}
      </div>

      <div className="border-t border-neutral-800/60" />

      <div className="p-2">
        <button onClick={scan} disabled={scanning}
          className="w-full text-left px-2 py-1.5 text-xs text-neutral-400 hover:bg-neutral-800/60 rounded-lg transition-colors">
          {scanning ? 'Scanning...' : 'Scan for Networks'}
        </button>
        {networks?.slice(0, 8).map(n => (
          <div key={n.bssid || n.ssid} className="flex items-center justify-between px-2 py-1.5 hover:bg-neutral-800/60 rounded-lg cursor-pointer">
            <div className="flex items-center gap-2 min-w-0">
              <WifiIcon connected size={12} />
              <span className="text-xs text-neutral-300 truncate">{n.ssid || '(hidden)'}</span>
            </div>
            <span className="text-[10px] text-neutral-600 shrink-0">{n.signal}dBm</span>
          </div>
        ))}
      </div>

      <div className="border-t border-neutral-800/60 px-2 py-1.5">
        <button className="w-full text-left px-2 py-1 text-xs text-neutral-500 hover:text-neutral-300">
          Network Settings...
        </button>
      </div>
    </div>
  )
}

// --- Battery Dropdown ---
function BatteryDropdown({ battery, charging, temp, uptime }) {
  const timeLeft = charging ? 'Charging' : battery > 80 ? 'Full day' : battery > 50 ? 'Several hours' : battery > 20 ? 'A few hours' : 'Low'
  return (
    <div className="w-[240px]">
      <div className="px-4 pt-3 pb-3">
        <div className="flex items-center gap-3">
          <BatteryIcon percent={battery} charging={charging} size={28} />
          <div>
            <div className="text-lg font-semibold text-neutral-200">{battery}%</div>
            <div className="text-[11px] text-neutral-500">{charging ? 'Charging' : 'On Battery'}</div>
          </div>
        </div>
        {/* Battery bar */}
        <div className="mt-3 w-full h-2 bg-neutral-800 rounded-full overflow-hidden">
          <div className={`h-full rounded-full transition-all ${
            battery > 50 ? 'bg-green-500' : battery > 20 ? 'bg-yellow-500' : 'bg-red-500'}`}
            style={{ width: `${battery}%` }} />
        </div>
      </div>

      <div className="border-t border-neutral-800/60 px-4 py-2 space-y-1.5">
        <div className="flex justify-between text-xs">
          <span className="text-neutral-500">Estimate</span>
          <span className="text-neutral-300">{timeLeft}</span>
        </div>
        {temp > 0 && (
          <div className="flex justify-between text-xs">
            <span className="text-neutral-500">Temperature</span>
            <span className="text-neutral-300">{temp}°C</span>
          </div>
        )}
        {uptime && (
          <div className="flex justify-between text-xs">
            <span className="text-neutral-500">Uptime</span>
            <span className="text-neutral-300">{uptime}</span>
          </div>
        )}
      </div>
    </div>
  )
}

// --- System Info Dropdown (from topbar logo) ---
function SystemDropdown({ stats, connected, profile, onLogout }) {
  const cpu = stats ? Math.round(stats.cpu) : null
  const mem = stats ? Math.round(stats.mem_percent) : null
  const battery = stats?.battery >= 0 ? stats.battery : null
  const charging = stats?.charging ?? false
  const temp = stats?.temp > 0 ? Math.round(stats.temp) : null
  const uptime = stats?.uptime || null
  const hostname = stats?.hostname || 'vula'

  return (
    <div className="w-[260px]">
      {/* Profile */}
      <div className="px-4 pt-3 pb-2.5 flex items-center gap-3">
        <div className="w-8 h-8 rounded-full bg-neutral-700 flex items-center justify-center text-sm font-semibold text-neutral-300 shrink-0">
          {(profile?.display_name || profile?.username || '?')[0].toUpperCase()}
        </div>
        <div className="min-w-0">
          <div className="text-sm font-medium text-neutral-200 truncate">{profile?.display_name || profile?.username}</div>
          <div className="text-[11px] text-neutral-500 truncate">{profile?.username}</div>
        </div>
      </div>

      <div className="border-t border-neutral-800/60" />

      {/* System stats */}
      <div className="px-4 py-2">
        <div className="text-[10px] text-neutral-500 uppercase tracking-wider mb-1.5">System</div>
        <div className="text-[11px] text-neutral-500 mb-2">{hostname}{uptime ? ` \u00b7 Up ${uptime}` : ''}</div>
      </div>
      {connected && cpu !== null ? (
        <div className="px-4 pb-2.5 space-y-2">
          <div className="flex justify-between text-xs">
            <span className="text-neutral-500">CPU</span>
            <span className="text-neutral-300 font-mono">{cpu}%</span>
          </div>
          <div className="w-full h-1.5 bg-neutral-800 rounded-full overflow-hidden">
            <div className={`h-full rounded-full ${cpu > 80 ? 'bg-red-500' : cpu > 50 ? 'bg-yellow-500' : 'bg-green-500'}`} style={{ width: `${cpu}%` }} />
          </div>
          <div className="flex justify-between text-xs">
            <span className="text-neutral-500">Memory</span>
            <span className="text-neutral-300 font-mono">{mem}%</span>
          </div>
          <div className="w-full h-1.5 bg-neutral-800 rounded-full overflow-hidden">
            <div className={`h-full rounded-full ${mem > 80 ? 'bg-red-500' : mem > 50 ? 'bg-yellow-500' : 'bg-blue-500'}`} style={{ width: `${mem}%` }} />
          </div>
          {stats?.mem_used && (
            <div className="text-[10px] text-neutral-600">{fmtBytes(stats.mem_used)} / {fmtBytes(stats.mem_total)}</div>
          )}
          {temp > 0 && (
            <div className="flex justify-between text-xs">
              <span className="text-neutral-500">Temperature</span>
              <span className="text-neutral-300">{temp}\u00b0C</span>
            </div>
          )}
          {battery !== null && (
            <div className="flex justify-between text-xs">
              <span className="text-neutral-500">Battery</span>
              <span className="text-neutral-300">{battery}%{charging ? ' (charging)' : ''}</span>
            </div>
          )}
        </div>
      ) : (
        <div className="px-4 pb-2.5 text-xs text-neutral-500">
          {connected ? 'Loading...' : 'Connecting...'}
        </div>
      )}

      <div className="border-t border-neutral-800/60" />

      {/* Logout */}
      <div className="p-1.5">
        <button
          onClick={onLogout}
          className="w-full text-left px-3 py-1.5 text-xs text-red-400 hover:bg-red-500/10 rounded-lg transition-colors"
        >
          Log Out
        </button>
      </div>
    </div>
  )
}

// ============================================================
// MAIN EXPORT — Compact topbar mode
// ============================================================
export default function LifePulse({ compact = false, className = '' }) {
  const now = useTime()
  const { stats, connected } = useTelemetry()
  const { isDark, toggle } = useTheme()
  const [openDropdown, setOpenDropdown] = useState(null)

  const battery = stats?.battery >= 0 ? stats.battery : null
  const charging = stats?.charging ?? false
  const temp = stats?.temp > 0 ? Math.round(stats.temp) : null
  const uptime = stats?.uptime || null

  // Refs for each trigger button so Dropdown can ignore clicks on them
  const wifiRef = useRef(null)
  const batteryRef = useRef(null)
  const clockRef = useRef(null)
  const systemRef = useRef(null)

  const toggleDropdown = useCallback((name) => {
    setOpenDropdown(prev => prev === name ? null : name)
  }, [])
  const closeDropdown = useCallback(() => setOpenDropdown(null), [])

  if (compact) {
    return (
      <div className={`flex items-center gap-0.5 ${className}`}>
        {/* WiFi */}
        <div className="relative" ref={wifiRef}>
          <StatusButton onClick={() => toggleDropdown('wifi')} active={openDropdown === 'wifi'}>
            <WifiIcon connected={connected} size={14} />
          </StatusButton>
          <Dropdown open={openDropdown === 'wifi'} onClose={closeDropdown} containerRef={wifiRef}>
            <WifiDropdown connected={connected} />
          </Dropdown>
        </div>

        {/* Battery */}
        {battery !== null && (
          <div className="relative" ref={batteryRef}>
            <StatusButton onClick={() => toggleDropdown('battery')} active={openDropdown === 'battery'}>
              <div className="flex items-center gap-1">
                <BatteryIcon percent={battery} charging={charging} size={16} />
                <span className="text-[11px] font-mono text-neutral-400">{battery}%</span>
              </div>
            </StatusButton>
            <Dropdown open={openDropdown === 'battery'} onClose={closeDropdown} containerRef={batteryRef}>
              <BatteryDropdown battery={battery} charging={charging} temp={temp} uptime={uptime} />
            </Dropdown>
          </div>
        )}

        {/* Theme toggle */}
        <StatusButton onClick={toggle}>
          {isDark ? (
            <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="currentColor">
              <path d="M8 1a.5.5 0 01.5.5v1a.5.5 0 01-1 0v-1A.5.5 0 018 1zm0 11a.5.5 0 01.5.5v1a.5.5 0 01-1 0v-1A.5.5 0 018 12zm7-4a.5.5 0 010 1h-1a.5.5 0 010-1h1zM3 8a.5.5 0 010 1H2a.5.5 0 010-1h1zm9.354-3.646a.5.5 0 010 .708l-.708.707a.5.5 0 11-.707-.708l.707-.707a.5.5 0 01.708 0zM5.06 10.232a.5.5 0 010 .707l-.707.708a.5.5 0 11-.708-.708l.708-.707a.5.5 0 01.707 0zm7.678.708a.5.5 0 01-.708 0l-.707-.708a.5.5 0 01.707-.707l.708.707a.5.5 0 010 .708zM5.06 5.768a.5.5 0 01-.707 0l-.708-.707a.5.5 0 11.708-.708l.707.708a.5.5 0 010 .707zM8 4.5a3.5 3.5 0 100 7 3.5 3.5 0 000-7z" />
            </svg>
          ) : (
            <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="currentColor">
              <path d="M6 .278a.768.768 0 01.08.858 7.2 7.2 0 00-.878 3.46c0 4.021 3.278 7.277 7.318 7.277.527 0 1.04-.055 1.533-.16a.787.787 0 01.81.316.733.733 0 01-.031.893A8.349 8.349 0 018.344 16C3.734 16 0 12.286 0 7.71 0 4.266 2.114 1.312 5.124.06A.752.752 0 016 .278z" />
            </svg>
          )}
        </StatusButton>

        <div className="w-px h-3.5 bg-neutral-700/40 mx-0.5" />

        {/* Clock + Calendar */}
        <div className="relative" ref={clockRef}>
          <StatusButton onClick={() => toggleDropdown('clock')} active={openDropdown === 'clock'} wide>
            <div className="flex items-center gap-2">
              <span className="text-xs font-medium text-neutral-300">{formatTime(now)}</span>
              <span className="text-[11px] text-neutral-500">{formatDate(now)}</span>
            </div>
          </StatusButton>
          <Dropdown open={openDropdown === 'clock'} onClose={closeDropdown} containerRef={clockRef}>
            <ClockDropdown now={now} />
          </Dropdown>
        </div>
      </div>
    )
  }

  // Full mode — used as topbar left-side system dropdown trigger
  const { profile, logout } = useAuth()

  return (
    <div className={`relative ${className}`} ref={systemRef}>
      <StatusButton onClick={() => toggleDropdown('system')} active={openDropdown === 'system'}>
        <div className="flex items-center gap-2">
          <img src="/vulos.png" alt="" className="w-4 h-4 opacity-70" />
          <span className="text-xs font-semibold text-neutral-300 tracking-wide">vula</span>
        </div>
      </StatusButton>
      <Dropdown open={openDropdown === 'system'} onClose={closeDropdown} align="left" containerRef={systemRef}>
        <SystemDropdown stats={stats} connected={connected} profile={profile} onLogout={logout} />
      </Dropdown>
    </div>
  )
}

function StatusButton({ children, onClick, active, wide }) {
  return (
    <button onClick={onClick}
      className={`h-8 flex items-center justify-center rounded-md transition-colors text-neutral-400 hover:text-neutral-200
        ${wide ? 'px-2' : 'px-1.5'}
        ${active ? 'bg-neutral-700/60 text-neutral-200' : 'hover:bg-neutral-800/60'}`}>
      {children}
    </button>
  )
}

function PulseCard({ label, value, sub, dot }) {
  return (
    <div className="bg-neutral-900/60 backdrop-blur-sm rounded-lg border border-neutral-800/50 px-3 py-2.5">
      <div className="flex items-center gap-1.5">
        {dot && <span className={`w-1.5 h-1.5 rounded-full ${
          dot === 'green' ? 'bg-green-500' : dot === 'yellow' ? 'bg-yellow-500' : 'bg-red-500'
        }`} />}
        <span className="text-[10px] text-neutral-500 uppercase tracking-wider">{label}</span>
      </div>
      <div className="text-sm text-neutral-300 mt-0.5 truncate">{value}</div>
      {sub && <div className="text-[10px] text-neutral-600 truncate">{sub}</div>}
    </div>
  )
}
