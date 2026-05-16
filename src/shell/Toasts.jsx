import { useState, useEffect, useRef, useCallback } from 'react'
import ConflictResolver from '../core/ConflictResolver'

const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/notifications/stream`

export default function Toasts() {
  const [toasts, setToasts] = useState([])
  const wsRef = useRef(null)
  // CLUSTER-10: show ConflictResolver when a sync-category deep-link notification arrives
  const [cl10ResolverOpen, setCl10ResolverOpen] = useState(false)

  useEffect(() => {
    let alive = true
    function connect() {
      if (!alive) return
      const ws = new WebSocket(WS_URL)
      wsRef.current = ws
      ws.onmessage = (e) => {
        try {
          const notif = JSON.parse(e.data)
          if (notif.source === 'xdg-open') return
          // CLUSTER-10: sync conflict notification with deep-link → open ConflictResolver
          if (notif.source === 'sync' && notif.action) {
            setCl10ResolverOpen(true)
          }
          setToasts(prev => [...prev.slice(-4), { ...notif, _key: Date.now() + Math.random() }])
        } catch {}
      }
      ws.onclose = () => { if (alive) setTimeout(connect, 3000) }
      ws.onerror = () => ws.close()
    }
    connect()
    return () => { alive = false; wsRef.current?.close() }
  }, [])

  const dismiss = useCallback((key) => {
    setToasts(prev => prev.filter(t => t._key !== key))
  }, [])

  // Auto-dismiss after 6s (urgent stays 12s)
  useEffect(() => {
    if (toasts.length === 0) return
    const latest = toasts[toasts.length - 1]
    const delay = latest.level === 'urgent' ? 12000 : 6000
    const timer = setTimeout(() => dismiss(latest._key), delay)
    return () => clearTimeout(timer)
  }, [toasts, dismiss])

  if (toasts.length === 0 && !cl10ResolverOpen) return null

  return (
    <>
      {toasts.length > 0 && (
        <div className="fixed top-10 right-3 z-[90] flex flex-col gap-2 max-w-sm">
          {toasts.map(t => (
            <div
              key={t._key}
              className={`px-4 py-3 rounded-xl backdrop-blur-xl border cursor-pointer
                transition-all animate-[slideIn_0.2s_ease-out]
                ${t.level === 'urgent'
                  ? 'bg-red-950/80 border-red-800/50 text-red-200'
                  : t.level === 'warning'
                    ? 'bg-amber-950/80 border-amber-800/50 text-amber-200'
                    : 'bg-neutral-900/80 border-neutral-700/50 text-neutral-200'
                }`}
            >
              <div className="flex items-center gap-2" onClick={() => dismiss(t._key)}>
                <span className={`w-2 h-2 rounded-full shrink-0 ${
                  t.level === 'urgent' ? 'bg-red-500' : t.level === 'warning' ? 'bg-amber-500' : 'bg-blue-500'
                }`} />
                <span className="text-sm font-medium truncate">{t.title}</span>
                <span className="text-[10px] text-neutral-500 ml-auto shrink-0">{t.source}</span>
              </div>
              {t.body && <p className="text-xs text-neutral-400 mt-1 line-clamp-2" onClick={() => dismiss(t._key)}>{t.body}</p>}
              {/* CLUSTER-10: sync conflict deep-link action button */}
              {t.source === 'sync' && t.action && (
                <button
                  className="mt-2 text-[10px] text-amber-400 underline hover:text-amber-200"
                  onClick={(e) => { e.stopPropagation(); dismiss(t._key); setCl10ResolverOpen(true) }}
                >
                  View conflicts
                </button>
              )}
            </div>
          ))}
        </div>
      )}
      {/* CLUSTER-10: Conflict Resolver modal */}
      {cl10ResolverOpen && (
        <ConflictResolver onClose={() => setCl10ResolverOpen(false)} />
      )}
    </>
  )
}
