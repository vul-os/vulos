import { useState, useEffect, useRef, useCallback } from 'react'

// ─── NC04_ namespace — no identifier collisions with other shell modules ───

// ---- Helpers ----

function NC04_fmtRelTime(dateStr) {
  const d = new Date(dateStr)
  if (isNaN(d)) return ''
  const diff = Math.floor((Date.now() - d) / 1000)
  if (diff < 60) return 'just now'
  if (diff < 3600) return `${Math.floor(diff / 60)}m`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`
  return `${Math.floor(diff / 86400)}d`
}

function NC04_dayLabel(dateStr) {
  const d = new Date(dateStr)
  if (isNaN(d)) return 'Unknown'
  const now = new Date()
  const todayMidnight = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const yesterdayMidnight = new Date(todayMidnight - 86400000)
  const itemMidnight = new Date(d.getFullYear(), d.getMonth(), d.getDate())
  if (itemMidnight >= todayMidnight) return 'Today'
  if (itemMidnight >= yesterdayMidnight) return 'Yesterday'
  return d.toLocaleDateString([], { weekday: 'long', month: 'short', day: 'numeric' })
}

// Group notifications: first by day-label, then within each day by source.
// Returns [{ day, groups: [{ source, items: [notif, ...] }, ...] }, ...]
function NC04_groupNotifs(notifs) {
  const dayMap = new Map()
  for (const n of notifs) {
    const day = NC04_dayLabel(n.created_at)
    if (!dayMap.has(day)) dayMap.set(day, new Map())
    const sourceMap = dayMap.get(day)
    const src = n.source || 'system'
    if (!sourceMap.has(src)) sourceMap.set(src, [])
    sourceMap.get(src).push(n)
  }
  const result = []
  for (const [day, sourceMap] of dayMap) {
    const groups = []
    for (const [source, items] of sourceMap) {
      groups.push({ source, items })
    }
    result.push({ day, groups })
  }
  return result
}

function NC04_levelDot(level) {
  if (level === 'urgent') return 'bg-red-500'
  if (level === 'warning') return 'bg-amber-500'
  return 'bg-blue-400'
}

// ---- Bell icon with badge ----
function NC04_BellIcon({ unread }) {
  return (
    <div className="relative flex items-center justify-center">
      <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="currentColor">
        <path d="M8 1a.5.5 0 0 1 .5.5v.5A5 5 0 0 1 13 7v3l1.5 1.5a.5.5 0 0 1-.354.854H1.854A.5.5 0 0 1 1.5 11.5L3 10V7a5 5 0 0 1 4.5-4.99V1.5A.5.5 0 0 1 8 1zM8 15a2 2 0 0 0 2-2H6a2 2 0 0 0 2 2z" />
      </svg>
      {unread > 0 && (
        <span className="absolute -top-1 -right-1.5 min-w-[14px] h-3.5 px-0.5 flex items-center justify-center
          rounded-full bg-red-500 text-white text-[9px] font-bold leading-none">
          {unread > 99 ? '99+' : unread}
        </span>
      )}
    </div>
  )
}

// ---- DND Row ----
function NC04_DndRow() {
  const [dndState, setDndState] = useState(null) // null = loading / hidden (404)
  const [hidden, setHidden] = useState(false)

  useEffect(() => {
    fetch('/api/notifications/dnd')
      .then(r => {
        if (r.status === 404) { setHidden(true); return null }
        if (!r.ok) throw new Error('err')
        return r.json()
      })
      .then(data => { if (data) setDndState(data) })
      .catch(() => setHidden(true))
  }, [])

  if (hidden || dndState === null) return null

  const toggle = async () => {
    const next = !dndState.enabled
    try {
      const r = await fetch('/api/notifications/dnd', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: next }),
      })
      if (r.ok) {
        const data = await r.json()
        setDndState(data)
      }
    } catch { /* noop */ }
  }

  return (
    <div className="flex items-center justify-between px-3 py-2 border-t border-neutral-800/60">
      <span className="text-[11px] text-neutral-400">Do Not Disturb</span>
      <button
        onClick={toggle}
        className={`relative w-8 h-4.5 rounded-full transition-colors focus:outline-none
          ${dndState.enabled ? 'bg-blue-500' : 'bg-neutral-700'}`}
        style={{ height: '18px', width: '32px' }}
        aria-label="Toggle Do Not Disturb"
      >
        <span className={`absolute top-0.5 w-3.5 h-3.5 rounded-full bg-white shadow transition-transform
          ${dndState.enabled ? 'translate-x-4' : 'translate-x-0.5'}`} />
      </button>
    </div>
  )
}

// ---- Panel ----
function NC04_Panel({ onClose }) {
  const [notifs, setNotifs] = useState([])
  const [unread, setUnread] = useState(0)
  const [loading, setLoading] = useState(true)

  const NC04_fetchAll = useCallback(async () => {
    try {
      const [nr, nu] = await Promise.all([
        fetch('/api/notifications').then(r => r.ok ? r.json() : []),
        fetch('/api/notifications/unread').then(r => r.ok ? r.json() : { count: 0 }),
      ])
      setNotifs(Array.isArray(nr) ? nr : [])
      setUnread(typeof nu?.count === 'number' ? nu.count : 0)
    } catch {
      setNotifs([])
    } finally {
      setLoading(false)
    }
  }, [])

  // Fetch on mount
  useEffect(() => { NC04_fetchAll() }, [NC04_fetchAll])

  // Poll every 10s
  useEffect(() => {
    const id = setInterval(NC04_fetchAll, 10000)
    return () => clearInterval(id)
  }, [NC04_fetchAll])

  const NC04_markRead = useCallback(async (id) => {
    try {
      await fetch(`/api/notifications/${id}/read`, { method: 'POST' })
      setNotifs(prev => prev.map(n => n.id === id ? { ...n, read: true } : n))
      setUnread(prev => Math.max(0, prev - 1))
    } catch { /* noop */ }
  }, [])

  const NC04_markAllRead = useCallback(async () => {
    try {
      await fetch('/api/notifications/read-all', { method: 'POST' })
      setNotifs(prev => prev.map(n => ({ ...n, read: true })))
      setUnread(0)
    } catch { /* noop */ }
  }, [])

  const NC04_clear = useCallback(async () => {
    try {
      await fetch('/api/notifications/clear', { method: 'POST' })
      setNotifs([])
      setUnread(0)
    } catch { /* noop */ }
  }, [])

  const grouped = NC04_groupNotifs(notifs)

  return (
    <div className="w-[320px] max-h-[480px] flex flex-col overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2.5 border-b border-neutral-800/60 shrink-0">
        <span className="text-xs font-semibold text-neutral-200">Notifications</span>
        <div className="flex items-center gap-1.5">
          {unread > 0 && (
            <button
              onClick={NC04_markAllRead}
              className="text-[10px] text-blue-400 hover:text-blue-300 transition-colors px-1.5 py-0.5 rounded hover:bg-blue-500/10"
            >
              Mark all read
            </button>
          )}
          {notifs.length > 0 && (
            <button
              onClick={NC04_clear}
              className="text-[10px] text-neutral-500 hover:text-red-400 transition-colors px-1.5 py-0.5 rounded hover:bg-red-500/10"
            >
              Clear
            </button>
          )}
          <button
            onClick={onClose}
            className="w-5 h-5 flex items-center justify-center rounded text-neutral-500 hover:text-neutral-300 hover:bg-neutral-800/60 transition-colors text-xs"
          >
            ✕
          </button>
        </div>
      </div>

      {/* Body */}
      <div className="overflow-y-auto flex-1 min-h-0">
        {loading && (
          <div className="flex items-center justify-center py-8 text-neutral-600 text-xs">
            Loading...
          </div>
        )}
        {!loading && notifs.length === 0 && (
          <div className="flex flex-col items-center justify-center py-10 gap-2">
            <svg viewBox="0 0 24 24" className="w-8 h-8 text-neutral-700" fill="currentColor">
              <path d="M12 2a.75.75 0 01.75.75v.75A7.25 7.25 0 0119.5 10.5v3.75l2.25 2.25a.75.75 0 01-.53 1.28H2.78a.75.75 0 01-.53-1.28L4.5 14.25V10.5A7.25 7.25 0 0111.25 3.5V2.75A.75.75 0 0112 2zm0 20a3 3 0 003-3H9a3 3 0 003 3z" />
            </svg>
            <span className="text-xs text-neutral-600">No notifications</span>
          </div>
        )}
        {!loading && grouped.map(({ day, groups }) => (
          <div key={day}>
            {/* Day separator */}
            <div className="flex items-center gap-2 px-3 pt-3 pb-1">
              <span className="text-[10px] text-neutral-500 uppercase tracking-wider font-medium">{day}</span>
              <div className="flex-1 h-px bg-neutral-800/60" />
            </div>
            {groups.map(({ source, items }) => (
              <div key={source} className="mb-1">
                {/* Source label — only show if not "system" */}
                {source !== 'system' && (
                  <div className="px-3 pb-0.5">
                    <span className="text-[9px] text-neutral-600 uppercase tracking-wider">{source}</span>
                  </div>
                )}
                {items.map(n => (
                  <button
                    key={n.id}
                    onClick={() => { if (!n.read) NC04_markRead(n.id) }}
                    className={`w-full text-left px-3 py-2 flex items-start gap-2.5 transition-colors
                      ${n.read ? 'hover:bg-neutral-800/30' : 'hover:bg-neutral-800/50 bg-neutral-800/20'}`}
                  >
                    <span className={`mt-1.5 w-1.5 h-1.5 rounded-full shrink-0 ${
                      n.read ? 'bg-neutral-700' : NC04_levelDot(n.level)
                    }`} />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center justify-between gap-1">
                        <span className={`text-xs font-medium truncate ${n.read ? 'text-neutral-400' : 'text-neutral-200'}`}>
                          {n.title}
                        </span>
                        <span className="text-[10px] text-neutral-600 shrink-0">{NC04_fmtRelTime(n.created_at)}</span>
                      </div>
                      {n.body && (
                        <p className="text-[11px] text-neutral-500 mt-0.5 line-clamp-2">{n.body}</p>
                      )}
                    </div>
                  </button>
                ))}
              </div>
            ))}
          </div>
        ))}
      </div>

      {/* DND row — degrades gracefully on 404 */}
      <NC04_DndRow />
    </div>
  )
}

// ---- Main export ----
export default function NotificationCenter() {
  const [open, setOpen] = useState(false)
  const [unread, setUnread] = useState(0)
  const triggerRef = useRef(null)
  const panelRef = useRef(null)

  // Fetch unread count on mount + poll every 10s (background, even when panel is closed)
  const NC04_fetchUnread = useCallback(async () => {
    try {
      const r = await fetch('/api/notifications/unread')
      if (r.ok) {
        const data = await r.json()
        setUnread(typeof data?.count === 'number' ? data.count : 0)
      }
    } catch { /* noop */ }
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { NC04_fetchUnread() }, [NC04_fetchUnread])

  useEffect(() => {
    const id = setInterval(NC04_fetchUnread, 10000)
    return () => clearInterval(id)
  }, [NC04_fetchUnread])

  // Close on outside click
  useEffect(() => {
    if (!open) return
    const handler = (e) => {
      if (
        panelRef.current && !panelRef.current.contains(e.target) &&
        triggerRef.current && !triggerRef.current.contains(e.target)
      ) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  const toggle = useCallback(() => setOpen(v => !v), [])
  const close = useCallback(() => setOpen(false), [])

  return (
    <div className="relative">
      {/* Bell button */}
      <button
        ref={triggerRef}
        onClick={toggle}
        title="Notifications"
        className={`h-8 px-1.5 flex items-center justify-center rounded-md transition-colors text-neutral-400 hover:text-neutral-200
          ${open ? 'bg-neutral-700/60 text-neutral-200' : 'hover:bg-neutral-800/60'}`}
      >
        <NC04_BellIcon unread={unread} />
      </button>

      {/* Pull-down panel */}
      {open && (
        <div
          ref={panelRef}
          className="absolute top-full right-0 mt-1.5 z-[100]
            bg-neutral-900/95 backdrop-blur-xl border border-neutral-700/50
            rounded-xl shadow-2xl shadow-black/50 overflow-hidden
            animate-[fadeIn_0.12s_ease-out]"
        >
          <NC04_Panel onClose={close} />
        </div>
      )}
    </div>
  )
}
