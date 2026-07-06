import { useEffect, useRef, useState, useCallback, useSyncExternalStore } from 'react'
import { useShell } from '../providers/ShellProvider'
import { getAppById } from '../core/AppRegistry'
import { launchApp } from './launchApp'
import { classifyIntent } from '../core/IntentRouter'
import ConflictResolver from '../core/ConflictResolver'
import {
  subscribeToasts, getToasts, dismissToast,
  subscribePrefs, getPrefs, markRead,
} from '../core/notificationStore'

// Store-driven Toaster (Wave 13). Renders the transient toast queue from the
// notificationStore. Toasts auto-dismiss (per level / ttl); actionable ones
// route through the shell's app-launch path. A11Y: the stack is an aria-live
// region; urgent/critical announce assertively.

// ── sound ────────────────────────────────────────────────────────────────────
function playChime(level) {
  try {
    const AudioCtx = window.AudioContext || window.webkitAudioContext
    if (!AudioCtx) return
    const ctx = new AudioCtx()
    const schedule = (level === 'urgent' || level === 'critical')
      ? [{ freq: 880, start: 0, dur: 0.12 }, { freq: 1100, start: 0.14, dur: 0.18 }]
      : [{ freq: 660, start: 0, dur: 0.1 }]
    schedule.forEach(({ freq, start, dur }) => {
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.connect(gain); gain.connect(ctx.destination)
      osc.type = 'sine'
      osc.frequency.setValueAtTime(freq, ctx.currentTime + start)
      gain.gain.setValueAtTime(0, ctx.currentTime + start)
      gain.gain.linearRampToValueAtTime(0.16, ctx.currentTime + start + 0.02)
      gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + start + dur)
      osc.start(ctx.currentTime + start)
      osc.stop(ctx.currentTime + start + dur + 0.05)
    })
    setTimeout(() => ctx.close(), 800)
  } catch { /* Web Audio unavailable */ }
}

// ── toast card ───────────────────────────────────────────────────────────────
function ToastCard({ notif, onAction, onDismiss }) {
  const urgent = notif.level === 'urgent' || notif.level === 'critical'
  const warn = notif.level === 'warning'
  const primary = notif.actions?.[0] || null

  const color = urgent
    ? 'bg-red-950/90 border-red-700/60'
    : warn
      ? 'bg-amber-950/80 border-amber-800/50'
      : 'bg-neutral-900/90 border-neutral-700/50'
  // Semantic dot: danger/warning stay fixed; info follows the user's accent.
  const dotCls = urgent ? 'animate-pulse' : ''
  const dotStyle = urgent
    ? { background: 'var(--status-danger)' }
    : warn
      ? { background: 'var(--status-warning)' }
      : { background: 'var(--accent)' }

  return (
    <div
      role={urgent ? 'alert' : 'status'}
      aria-live={urgent ? 'assertive' : 'polite'}
      className={`w-[min(20rem,calc(100vw-1.5rem))] px-3.5 py-2.5 rounded-xl backdrop-blur-xl border text-neutral-200 shadow-lg shadow-black/30
        transition-all animate-[slideIn_0.2s_ease-out] select-none ${color}`}
    >
      <div className="flex items-center gap-2">
        <span className={`w-2 h-2 rounded-full shrink-0 ${dotCls}`} style={dotStyle} />
        <span className="text-sm font-medium truncate flex-1">{notif.title}</span>
        {urgent && <span style={{ color: 'var(--status-danger)' }} className="text-[9px] uppercase tracking-wider font-semibold shrink-0">Urgent</span>}
        <span className="text-[10px] text-neutral-500 shrink-0">{notif.source}</span>
        <button
          onClick={onDismiss}
          aria-label="Dismiss"
          className="w-4 h-4 flex items-center justify-center rounded text-neutral-500 hover:text-neutral-200 text-[10px] shrink-0"
        >✕</button>
      </div>
      {notif.body && <p className="text-xs text-neutral-400 mt-1 line-clamp-2 pl-4">{notif.body}</p>}
      {notif.actions?.length > 0 && (
        <div className="flex gap-1.5 mt-2 pl-4">
          {notif.actions.map(a => (
            <button
              key={a.id}
              onClick={() => onAction(a)}
              style={a === primary ? { background: 'var(--accent)', color: '#fff' } : undefined}
              className={`text-[11px] px-2.5 py-1 rounded-md transition-colors
                ${a === primary ? 'hover:brightness-110' : 'bg-neutral-800/80 text-neutral-300 hover:bg-neutral-700'}`}
            >
              {a.label || 'Open'}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

// ── critical overlay ─────────────────────────────────────────────────────────
function CriticalOverlay({ notif, onAction, onDismiss }) {
  const primary = notif.actions?.[0] || null
  return (
    <div className="fixed inset-0 z-[200] flex items-center justify-center bg-black/80 backdrop-blur-sm">
      <div role="alertdialog" aria-modal="true" aria-live="assertive"
        className="bg-red-950 border border-red-700/60 rounded-2xl p-8 max-w-md w-full mx-4 shadow-2xl">
        <div className="flex items-center gap-3 mb-4">
          <span className="w-4 h-4 rounded-full bg-red-500 animate-pulse shrink-0" />
          <span className="text-lg font-semibold text-red-100">{notif.title}</span>
        </div>
        {notif.body && <p className="text-sm text-red-200/80 mb-6">{notif.body}</p>}
        <div className="flex gap-3 justify-end">
          <button onClick={onDismiss}
            className="px-4 py-2 rounded-lg bg-red-900/60 text-red-200 text-sm hover:bg-red-900 transition-colors">Dismiss</button>
          {primary && (
            <button onClick={() => onAction(primary)}
              className="px-4 py-2 rounded-lg bg-red-600 text-white text-sm font-medium hover:bg-red-500 transition-colors">
              {primary.label || 'Open'}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

export default function Toasts() {
  const queue = useSyncExternalStore(subscribeToasts, getToasts)
  const prefs = useSyncExternalStore(subscribePrefs, getPrefs)
  const { openWindow } = useShell()
  const soundRef = useRef(prefs.sound)
  useEffect(() => { soundRef.current = prefs.sound }, [prefs.sound])
  const chimedRef = useRef(new Set())

  // CLUSTER-10: a 'sync' notification with a deep-link opens the ConflictResolver.
  const [resolverOpen, setResolverOpen] = useState(false)
  const seenSyncRef = useRef(new Set())

  // Resolve a notification action through the shell's launch path.
  const runAction = useCallback((notif, action) => {
    markRead(notif.id)
    dismissToast(`${notif.id}:${notif.ts}`)
    try {
      if (typeof action?.run === 'function') { action.run(); return }
      if (action?.route?.app) {
        const app = getAppById(action.route.app)
        if (app) {
          if (app.url) openWindow({ appId: app.id, title: app.name, url: app.url, icon: app.icon })
          else launchApp(app, { openWindow })
        }
        return
      }
      const url = action?.url || notif.action
      if (url && typeof url === 'string') {
        const intent = classifyIntent(url)
        window.location.href = intent?.url || url
      }
    } catch { /* action best-effort */ }
  }, [openWindow])

  // Play a chime once per newly-arrived toast (sound honoured from store prefs).
  useEffect(() => {
    for (const t of queue) {
      if (chimedRef.current.has(t.key)) continue
      chimedRef.current.add(t.key)
      if (soundRef.current) playChime(t.notif.level)
      if (t.notif.source === 'sync' && (t.notif.action || t.notif.actions?.length)) {
        if (!seenSyncRef.current.has(t.notif.id)) {
          seenSyncRef.current.add(t.notif.id)
          // eslint-disable-next-line react-hooks/set-state-in-effect -- open the conflict resolver in response to an incoming sync toast
          setResolverOpen(true)
        }
      }
    }
    // Prune the "already-chimed" set to the live queue so it can't grow without
    // bound over a long session (toasts churn continuously; keys are one-shot).
    if (chimedRef.current.size > 64) {
      const live = new Set(queue.map(t => t.key))
      for (const k of chimedRef.current) if (!live.has(k)) chimedRef.current.delete(k)
    }
  }, [queue])

  // Auto-dismiss non-sticky toasts (ttl seconds; 0 = sticky for urgent/critical).
  useEffect(() => {
    const timers = queue
      .filter(t => t.notif.ttl > 0 && t.notif.level !== 'critical')
      .map(t => setTimeout(() => dismissToast(t.key), Math.min(t.notif.ttl * 1000, 30000)))
    return () => timers.forEach(clearTimeout)
  }, [queue])

  const critical = queue.filter(t => t.notif.level === 'critical')
  const regular = queue.filter(t => t.notif.level !== 'critical')

  if (queue.length === 0 && !resolverOpen) return null

  return (
    <>
      {critical.length > 0 && (
        <CriticalOverlay
          notif={critical[critical.length - 1].notif}
          onAction={(a) => runAction(critical[critical.length - 1].notif, a)}
          onDismiss={() => dismissToast(critical[critical.length - 1].key)}
        />
      )}

      {regular.length > 0 && (
        <div
          className="fixed top-10 right-3 z-[90] flex flex-col gap-2 max-w-[calc(100vw-1.5rem)]"
          aria-live="polite"
          aria-relevant="additions"
        >
          {regular.map(t => (
            <ToastCard
              key={t.key}
              notif={t.notif}
              onAction={(a) => runAction(t.notif, a)}
              onDismiss={() => dismissToast(t.key)}
            />
          ))}
        </div>
      )}

      {resolverOpen && (
        <ConflictResolver onClose={() => setResolverOpen(false)} />
      )}
    </>
  )
}
