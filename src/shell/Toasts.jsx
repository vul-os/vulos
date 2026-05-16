import { useState, useEffect, useRef, useCallback } from 'react'
import { classifyIntent } from '../core/IntentRouter'
import ConflictResolver from '../core/ConflictResolver'

const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/notifications/stream`

// localStorage key for notification sound preference
const SOUND_PREF_KEY = 'vulos_notif_sound'

// Derive effective priority from notification: prefer explicit priority, fall back to legacy level mapping
function effectivePriority(notif) {
  if (notif.priority) return notif.priority
  // Legacy level → priority mapping
  if (notif.level === 'urgent') return 'high'
  if (notif.level === 'warning') return 'normal'
  if (notif.level === 'info') return 'normal'
  return 'normal'
}

// Play a Web Audio chime appropriate for the given priority
function playChime(priority) {
  try {
    const AudioCtx = window.AudioContext || window.webkitAudioContext
    if (!AudioCtx) return
    const ctx = new AudioCtx()

    // Build a simple tone sequence: a short ascending chime for normal, a two-note ring for high
    const schedule = priority === 'high'
      ? [{ freq: 880, start: 0, dur: 0.12 }, { freq: 1100, start: 0.14, dur: 0.18 }]
      : [{ freq: 660, start: 0, dur: 0.10 }]

    schedule.forEach(({ freq, start, dur }) => {
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.connect(gain)
      gain.connect(ctx.destination)
      osc.type = 'sine'
      osc.frequency.setValueAtTime(freq, ctx.currentTime + start)
      gain.gain.setValueAtTime(0, ctx.currentTime + start)
      gain.gain.linearRampToValueAtTime(0.18, ctx.currentTime + start + 0.02)
      gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + start + dur)
      osc.start(ctx.currentTime + start)
      osc.stop(ctx.currentTime + start + dur + 0.05)
    })

    // Close context after sounds finish
    setTimeout(() => ctx.close(), 800)
  } catch {
    // Silently fail if Web Audio is unavailable
  }
}

// Toast card component
function ToastCard({ toast, onDismiss, onNavigate, soundEnabled }) {
  const priority = effectivePriority(toast)

  const handleClick = useCallback(() => {
    const actionUrl = toast.action?.url || toast.action
    if (actionUrl && typeof actionUrl === 'string') {
      // Try to route through IntentRouter; navigate to the URL
      try {
        const intent = classifyIntent(actionUrl)
        if (intent && intent.url) {
          window.location.href = intent.url
        } else {
          window.location.href = actionUrl
        }
      } catch {
        window.location.href = actionUrl
      }
      onDismiss()
    } else {
      onDismiss()
    }
  }, [toast, onDismiss])

  const hasAction = !!(toast.action?.url || (typeof toast.action === 'string' && toast.action))

  const colorClass = priority === 'critical' || priority === 'high'
    ? 'bg-red-950/90 border-red-700/60 text-red-100'
    : priority === 'normal' && (toast.level === 'warning')
      ? 'bg-amber-950/80 border-amber-800/50 text-amber-200'
      : 'bg-neutral-900/85 border-neutral-700/50 text-neutral-200'

  const dotClass = priority === 'critical' || priority === 'high'
    ? 'bg-red-400 animate-pulse'
    : priority === 'normal' && toast.level === 'warning'
      ? 'bg-amber-500'
      : 'bg-blue-500'

  return (
    <div
      onClick={handleClick}
      className={`px-4 py-3 rounded-xl backdrop-blur-xl border cursor-pointer
        transition-all animate-[slideIn_0.2s_ease-out] select-none
        ${colorClass}
        ${hasAction ? 'hover:brightness-110' : ''}`}
    >
      <div className="flex items-center gap-2">
        <span className={`w-2 h-2 rounded-full shrink-0 ${dotClass}`} />
        <span className="text-sm font-medium truncate flex-1">{toast.title}</span>
        {priority === 'high' && (
          <span className="text-[9px] uppercase tracking-wider text-red-400 font-semibold shrink-0">Urgent</span>
        )}
        <span className="text-[10px] text-neutral-500 shrink-0">{toast.source}</span>
      </div>
      {toast.body && (
        <p className="text-xs text-neutral-400 mt-1 line-clamp-2">
          {typeof toast.body === 'string' ? toast.body : toast.body.title || toast.body.detail || ''}
        </p>
      )}
      {hasAction && (
        <p className="text-[10px] text-blue-400 mt-1">Tap to open</p>
      )}
    </div>
  )
}

// Critical full-screen overlay
function CriticalOverlay({ toast, onDismiss }) {
  const actionUrl = toast.action?.url || (typeof toast.action === 'string' ? toast.action : null)

  const handleAction = useCallback(() => {
    if (actionUrl) {
      window.location.href = actionUrl
    }
    onDismiss()
  }, [actionUrl, onDismiss])

  return (
    <div className="fixed inset-0 z-[200] flex items-center justify-center bg-black/80 backdrop-blur-sm">
      <div className="bg-red-950 border border-red-700/60 rounded-2xl p-8 max-w-md w-full mx-4 shadow-2xl">
        <div className="flex items-center gap-3 mb-4">
          <span className="w-4 h-4 rounded-full bg-red-500 animate-pulse shrink-0" />
          <span className="text-lg font-semibold text-red-100">{toast.title}</span>
        </div>
        {toast.body && (
          <p className="text-sm text-red-200/80 mb-6">
            {typeof toast.body === 'string' ? toast.body : toast.body.title || toast.body.detail || ''}
          </p>
        )}
        <div className="flex gap-3 justify-end">
          <button
            onClick={onDismiss}
            className="px-4 py-2 rounded-lg bg-red-900/60 text-red-200 text-sm hover:bg-red-900 transition-colors"
          >
            Dismiss
          </button>
          {actionUrl && (
            <button
              onClick={handleAction}
              className="px-4 py-2 rounded-lg bg-red-600 text-white text-sm font-medium hover:bg-red-500 transition-colors"
            >
              Open
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

export default function Toasts() {
  const [toasts, setToasts] = useState([])
  const [soundEnabled, setSoundEnabled] = useState(() => {
    try {
      const stored = localStorage.getItem(SOUND_PREF_KEY)
      return stored === null ? true : stored === 'true'
    } catch {
      return true
    }
  })
  const wsRef = useRef(null)
  const soundEnabledRef = useRef(soundEnabled)

  // Keep ref in sync so WS callback always reads the latest value
  useEffect(() => {
    soundEnabledRef.current = soundEnabled
    try { localStorage.setItem(SOUND_PREF_KEY, String(soundEnabled)) } catch {}
  }, [soundEnabled])
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

          const priority = effectivePriority(notif)

          // low priority: badge only, suppress toast entirely
          if (priority === 'low') return

          const enriched = { ...notif, _key: Date.now() + Math.random(), _priority: priority }
          setToasts(prev => [...prev.slice(-4), enriched])

          // Play chime for normal+ if sound is enabled
          if (soundEnabledRef.current && (priority === 'normal' || priority === 'high' || priority === 'critical')) {
            playChime(priority)
          }
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

  // Auto-dismiss: normal=6s, high=persistent (no auto-dismiss), critical=persistent
  useEffect(() => {
    if (toasts.length === 0) return

    const timers = toasts
      .filter(t => t._priority === 'normal')
      .map(t => {
        // Respect TTL field if present, otherwise default 6s for normal
        const delay = t.ttl ? Math.min(t.ttl * 1000, 30000) : 6000
        return setTimeout(() => dismiss(t._key), delay)
      })

    return () => timers.forEach(clearTimeout)
  }, [toasts, dismiss])

  if (toasts.length === 0 && !cl10ResolverOpen) return null

  const criticalToasts = toasts.filter(t => t._priority === 'critical')
  const regularToasts = toasts.filter(t => t._priority !== 'critical')

  return (
    <>
      {/* Critical overlay — show topmost critical notification */}
      {criticalToasts.length > 0 && (
        <CriticalOverlay
          toast={criticalToasts[criticalToasts.length - 1]}
          onDismiss={() => dismiss(criticalToasts[criticalToasts.length - 1]._key)}
        />
      )}

      {/* Regular toast stack (normal + high) */}
      {regularToasts.length > 0 && (
        <div className="fixed top-10 right-3 z-[90] flex flex-col gap-2 max-w-sm">
          {regularToasts.map(t => (
            <ToastCard
              key={t._key}
              toast={t}
              onDismiss={() => dismiss(t._key)}
              soundEnabled={soundEnabled}
            />
          ))}

          {/* Sound toggle — subtle control within the toast stack */}
          <button
            onClick={() => setSoundEnabled(v => !v)}
            className="self-end text-[10px] text-neutral-600 hover:text-neutral-400 transition-colors px-1"
            title={soundEnabled ? 'Mute notification sounds' : 'Unmute notification sounds'}
          >
            {soundEnabled ? 'Sound on' : 'Sound off'}
          </button>
        </div>
      )}
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
