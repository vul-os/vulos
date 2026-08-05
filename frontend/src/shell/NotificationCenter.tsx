import { useState, useEffect, useRef, useCallback, useSyncExternalStore } from 'react'
import { useFocusTrap } from './useFocusTrap'
import { useNarrow } from './useNarrow'
import './shell-chrome.css'
import {
  subscribe, getItems, getUnreadCount,
  subscribePrefs, getPrefs, setMuted,
  markRead, markAllRead, dismiss, clear,
} from '../core/notificationStore'
import type { ShellNotification, NotificationPrefs } from './notificationTypes'

// ─── NC13_ namespace — no identifier collisions with other shell modules ───
// Store-driven Notification Center: the bell + pull-down panel. All state lives
// in the framework-agnostic notificationStore (client notify() + bridged backend
// notifications), so this component is a thin, reactive view over that seam.

// ---- Helpers ----
function NC13_fmtRelTime(ts: number): string {
  const diff = Math.floor((Date.now() - ts) / 1000)
  if (isNaN(diff) || diff < 0) return ''
  if (diff < 60) return 'just now'
  if (diff < 3600) return `${Math.floor(diff / 60)}m`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`
  return `${Math.floor(diff / 86400)}d`
}

function NC13_dayLabel(ts: number): string {
  const d = new Date(ts)
  if (isNaN(d.getTime())) return 'Earlier'
  const now = new Date()
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const yesterday = new Date(today.getTime() - 86400000)
  const item = new Date(d.getFullYear(), d.getMonth(), d.getDate())
  if (item >= today) return 'Today'
  if (item >= yesterday) return 'Yesterday'
  return d.toLocaleDateString([], { weekday: 'long', month: 'short', day: 'numeric' })
}

interface DayGroup {
  day: string
  groups: { source: string; items: ShellNotification[] }[]
}

// Group by day-label, then by source (per-app grouping) within each day.
function NC13_group(list: ShellNotification[]): DayGroup[] {
  const days = new Map<string, Map<string, ShellNotification[]>>()
  for (const n of list) {
    const day = NC13_dayLabel(n.ts)
    if (!days.has(day)) days.set(day, new Map())
    const bySrc = days.get(day)!
    const src = n.source || 'system'
    if (!bySrc.has(src)) bySrc.set(src, [])
    bySrc.get(src)!.push(n)
  }
  return [...days].map(([day, bySrc]) => ({
    day,
    groups: [...bySrc].map(([source, items]) => ({ source, items })),
  }))
}

// Returns an inline style for the level dot. Info-level (the default) follows
// the user's accent token; the semantic danger/warning tints stay fixed.
function NC13_levelDot(level: string, read: boolean): { background: string } {
  if (read) return { background: 'var(--border-strong)' }
  if (level === 'critical' || level === 'urgent') return { background: 'var(--status-danger)' }
  if (level === 'warning') return { background: 'var(--status-warning)' }
  return { background: 'var(--accent)' }
}

// ---- Bell icon with badge ----
function NC13_BellIcon({ unread, muted }: { unread: number; muted: boolean }) {
  return (
    <div className="relative flex items-center justify-center">
      <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="currentColor">
        <path d="M8 1a.5.5 0 0 1 .5.5v.5A5 5 0 0 1 13 7v3l1.5 1.5a.5.5 0 0 1-.354.854H1.854A.5.5 0 0 1 1.5 11.5L3 10V7a5 5 0 0 1 4.5-4.99V1.5A.5.5 0 0 1 8 1zM8 15a2 2 0 0 0 2-2H6a2 2 0 0 0 2 2z" />
      </svg>
      {muted && (
        <span className="absolute inset-0 flex items-center justify-center pointer-events-none">
          <span className="w-[18px] h-px bg-current rotate-45 opacity-80" />
        </span>
      )}
      {!muted && unread > 0 && (
        <span
          style={{ background: 'var(--status-danger)' }}
          className="absolute -top-1 -right-1.5 min-w-[14px] h-3.5 px-0.5 flex items-center justify-center
          rounded-full text-white text-[9px] font-bold leading-none">
          {unread > 99 ? '99+' : unread}
        </span>
      )}
    </div>
  )
}

// ---- Panel ----
function NC13_Panel({ onClose }: { onClose: () => void }) {
  const items = useSyncExternalStore<ShellNotification[]>(subscribe, getItems as () => ShellNotification[])
  const prefs = useSyncExternalStore<NotificationPrefs>(subscribePrefs, getPrefs as () => NotificationPrefs)
  const unread = items.reduce((n, x) => n + (x.read ? 0 : 1), 0)
  const grouped = NC13_group(items)

  return (
    <div className="w-full sm:w-[min(340px,calc(100vw-1rem))] max-h-[min(70vh,520px)] flex flex-col overflow-hidden">
      {/* Header */}
      <div className="vshell-border-b flex items-center justify-between px-3.5 py-2.5 shrink-0">
        <span className="text-xs font-semibold" style={{ color: 'var(--text-primary)' }}>Notifications</span>
        <div className="flex items-center gap-1.5">
          {unread > 0 && (
            <button
              onClick={markAllRead}
              style={{ color: 'var(--accent)' }}
              className="focus-primary text-[12px] transition-colors px-1.5 py-0.5 rounded hover:brightness-125"
            >
              Mark all read
            </button>
          )}
          {items.length > 0 && (
            <button
              onClick={clear}
              className="focus-primary text-[12px] transition-colors px-1.5 py-0.5 rounded"
              style={{ color: 'var(--text-muted)' }}
            >
              Clear
            </button>
          )}
          <button
            onClick={onClose}
            aria-label="Close notifications"
            className="vshell-menu-item focus-primary w-6 h-6 flex items-center justify-center rounded text-xs"
          >
            ✕
          </button>
        </div>
      </div>

      {/* Body */}
      <div className="overflow-y-auto flex-1 min-h-0">
        {items.length === 0 && (
          <div className="flex flex-col items-center justify-center py-10 gap-2">
            <svg viewBox="0 0 24 24" className="w-8 h-8" style={{ color: 'var(--text-faint)' }} fill="currentColor">
              <path d="M12 2a.75.75 0 01.75.75v.75A7.25 7.25 0 0119.5 10.5v3.75l2.25 2.25a.75.75 0 01-.53 1.28H2.78a.75.75 0 01-.53-1.28L4.5 14.25V10.5A7.25 7.25 0 0111.25 3.5V2.75A.75.75 0 0112 2zm0 20a3 3 0 003-3H9a3 3 0 003 3z" />
            </svg>
            <span className="text-xs" style={{ color: 'var(--text-muted)' }}>You're all caught up</span>
          </div>
        )}
        {grouped.map(({ day, groups }) => (
          <div key={day}>
            <div className="flex items-center gap-2 px-3.5 pt-3 pb-1">
              <span className="text-[12px] font-mono uppercase tracking-[0.14em]" style={{ color: 'var(--text-faint)' }}>{day}</span>
              <div className="vshell-hairline flex-1 h-px" />
            </div>
            {groups.map(({ source, items: rows }) => (
              <div key={source} className="mb-1">
                {source !== 'system' && (
                  <div className="px-3.5 pb-0.5">
                    <span className="text-[12px] font-mono uppercase tracking-wider" style={{ color: 'var(--text-ghost)' }}>{source}</span>
                  </div>
                )}
                {rows.map(n => (
                  <div
                    key={n.id}
                    data-active={n.read ? undefined : true}
                    className="vshell-row group/row w-full px-3.5 py-2 flex items-start gap-2.5"
                    style={n.read ? undefined : { background: 'color-mix(in srgb, var(--accent) 5%, transparent)' }}
                  >
                    <button
                      onClick={() => { if (!n.read) markRead(n.id) }}
                      className="focus-primary rounded flex items-start gap-2.5 flex-1 min-w-0 text-left"
                      aria-label={n.read ? n.title : `${n.title}, unread — mark read`}
                    >
                      <span className="mt-1.5 w-1.5 h-1.5 rounded-full shrink-0" style={NC13_levelDot(n.level, n.read)} />
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center justify-between gap-1">
                          <span className="text-xs font-medium truncate" style={{ color: n.read ? 'var(--text-tertiary)' : 'var(--text-primary)' }}>
                            {n.title}
                          </span>
                          <span className="text-[12px] shrink-0" style={{ color: 'var(--text-faint)' }}>{NC13_fmtRelTime(n.ts)}</span>
                        </div>
                        {n.body && (
                          <p className="text-[12px] mt-0.5 line-clamp-2" style={{ color: 'var(--text-muted)' }}>{n.body}</p>
                        )}
                      </div>
                    </button>
                    <button
                      onClick={() => dismiss(n.id)}
                      aria-label="Dismiss notification"
                      className="vshell-pip focus-primary opacity-0 group-hover/row:opacity-100 focus:opacity-100 w-5 h-5 flex items-center justify-center rounded transition-all text-[10px] shrink-0 mt-0.5"
                    >
                      ✕
                    </button>
                  </div>
                ))}
              </div>
            ))}
          </div>
        ))}
      </div>

      {/* Do Not Disturb — global mute lives in the store; also surfaced in Settings. */}
      <div className="vshell-border-t flex items-center justify-between px-3.5 py-2.5 shrink-0">
        <span className="text-[12px]" style={{ color: 'var(--text-tertiary)' }}>Do Not Disturb</span>
        <button
          onClick={() => setMuted(!prefs.muted)}
          role="switch"
          aria-checked={prefs.muted}
          aria-label="Toggle Do Not Disturb"
          className="focus-primary relative rounded-full transition-colors"
          style={{ height: '18px', width: '32px', background: prefs.muted ? 'var(--accent)' : 'var(--border-strong)' }}
        >
          <span className={`absolute top-0.5 w-3.5 h-3.5 rounded-full bg-white shadow transition-transform
            ${prefs.muted ? 'translate-x-4' : 'translate-x-0.5'}`} />
        </button>
      </div>
    </div>
  )
}

// ---- Main export ----
export default function NotificationCenter() {
  const [open, setOpen] = useState(false)
  const narrow = useNarrow(640)
  const unread = useSyncExternalStore<number>(subscribe, getUnreadCount as () => number)
  const prefs = useSyncExternalStore<NotificationPrefs>(subscribePrefs, getPrefs as () => NotificationPrefs)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const panelRef = useRef<HTMLDivElement | null>(null)
  // A11Y: trap focus inside the panel while open + restore to the bell on close.
  const trapRef = useFocusTrap(open)

  // Let other surfaces (⌘K command, Home) open the panel via a shell event.
  useEffect(() => {
    const openHandler = () => setOpen(true)
    window.addEventListener('vulos:open-notifications', openHandler)
    return () => window.removeEventListener('vulos:open-notifications', openHandler)
  }, [])

  // Close on outside click or Escape.
  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (
        panelRef.current && !panelRef.current.contains(e.target as Node) &&
        triggerRef.current && !triggerRef.current.contains(e.target as Node)
      ) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', handler)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', handler)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const toggle = useCallback(() => setOpen(v => !v), [])
  const close = useCallback(() => setOpen(false), [])

  return (
    <div className="relative">
      <button
        ref={triggerRef}
        onClick={toggle}
        title={prefs.muted ? 'Notifications (Do Not Disturb on)' : 'Notifications'}
        aria-label={unread > 0 ? `Notifications, ${unread} unread` : 'Notifications'}
        aria-haspopup="dialog"
        aria-expanded={open}
        data-active={open || undefined}
        className="vshell-btn focus-primary h-8 px-1.5 flex items-center justify-center rounded-md"
      >
        <NC13_BellIcon unread={unread} muted={prefs.muted} />
      </button>

      {open && (
        narrow ? (
          <>
            {/* Mobile: dim the desktop and float a sheet under the bar */}
            <div className="vshell-scrim fixed inset-0 z-[99]" onClick={close} aria-hidden="true" />
            <div
              ref={(el) => { panelRef.current = el; trapRef.current = el }}
              role="dialog"
              aria-modal="true"
              aria-label="Notifications"
              className="vshell-surface vshell-sheet fixed left-2 right-2 top-9 z-[100] rounded-2xl overflow-hidden"
            >
              <NC13_Panel onClose={close} />
            </div>
          </>
        ) : (
          <div
            ref={(el) => { panelRef.current = el; trapRef.current = el }}
            role="dialog"
            aria-modal="true"
            aria-label="Notifications"
            className="vshell-surface vshell-pop absolute top-full right-0 mt-1.5 z-[100] max-w-[calc(100vw-1rem)] rounded-2xl overflow-hidden"
          >
            <NC13_Panel onClose={close} />
          </div>
        )
      )}
    </div>
  )
}
