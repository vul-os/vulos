// DesktopWidgets.tsx — the desktop's glanceable column.
//
// The desktop used to answer "what's going on" by covering itself with a
// full-bleed dashboard whenever no window was open, which is why it read as a
// web page rather than an OS. This is the replacement: a narrow, ambient
// column pinned to the right of the wallpaper that shows the same kind of
// state without owning the screen — a clock, the next thing on the calendar,
// and whatever the box is trying to tell you.
//
// Every widget is real state or an honest empty; none of them invent content.
// The column hides itself when Mission Control or the Assistant panel occupies
// the same layer, and on phones (MobileStack has its own home surface).
import { useEffect, useState, useSyncExternalStore } from 'react'
import CalendarWidget from './CalendarWidget'
import { subscribe, getItems, getUnreadCount, markAllRead } from '../core/notificationStore'
import type { ShellNotification } from './notificationTypes'
import { Z_DESKTOP_WIDGETS } from './zLayers'
import './shell-chrome.css'

// ── Clock ────────────────────────────────────────────────────────────────────
// Ticks on the minute boundary rather than every second: the display has no
// seconds, so a 1 Hz interval would re-render 59 times for nothing.
function ClockWidget() {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout>
    const schedule = () => {
      const msToNextMinute = 60_000 - (Date.now() % 60_000)
      timer = setTimeout(() => { setNow(new Date()); schedule() }, msToNextMinute + 20)
    }
    schedule()
    return () => clearTimeout(timer)
  }, [])

  return (
    <div className="vshell-surface rounded-2xl px-4 py-3.5 select-none">
      <div
        className="text-[34px] leading-none font-semibold tracking-[-0.03em]"
        style={{ color: 'var(--text-primary)', fontVariantNumeric: 'tabular-nums' }}
      >
        {now.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })}
      </div>
      <div className="mt-1.5 text-[12px] font-medium" style={{ color: 'var(--text-tertiary)' }}>
        {now.toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric' })}
      </div>
    </div>
  )
}

// ── Notifications ────────────────────────────────────────────────────────────
// A glance, not a second notification centre: the newest few, and a way into
// the real one (the tray bell owns the full list + per-source prefs).
function NotificationsWidget() {
  const items = useSyncExternalStore<ShellNotification[]>(subscribe, getItems as () => ShellNotification[])
  const unread = useSyncExternalStore<number>(subscribe, getUnreadCount as () => number)
  const recent = items.slice(0, 3)

  return (
    <div className="vshell-surface rounded-2xl overflow-hidden select-none">
      <div className="px-3.5 pt-3 pb-2 flex items-center justify-between">
        <span className="text-[10.5px] font-semibold uppercase tracking-[0.1em]" style={{ color: 'var(--text-faint)' }}>
          Notifications
        </span>
        {unread > 0 && (
          <button
            onClick={() => markAllRead()}
            className="text-[11px] focus-primary rounded"
            style={{ color: 'var(--accent)' }}
          >
            Mark all read
          </button>
        )}
      </div>
      {recent.length === 0 ? (
        <div className="px-3.5 pb-3.5 text-[12.5px]" style={{ color: 'var(--text-tertiary)' }}>
          Nothing needs you.
        </div>
      ) : (
        <ul className="pb-1.5">
          {recent.map(n => (
            <li key={n.id} className="px-3.5 py-2 flex items-start gap-2.5">
              <span
                className="mt-[6px] w-1.5 h-1.5 rounded-full shrink-0"
                style={{ background: n.read ? 'var(--border-emphasis)' : 'var(--accent)' }}
                aria-hidden="true"
              />
              <span className="min-w-0">
                <span className="block text-[12.5px] font-medium truncate" style={{ color: 'var(--text-secondary)' }}>{n.title}</span>
                {n.body && <span className="block text-[11.5px] line-clamp-2" style={{ color: 'var(--text-tertiary)' }}>{n.body}</span>}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

export default function DesktopWidgets() {
  return (
    // Z_DESKTOP_WIDGETS puts the column ON THE DESKTOP, beneath every window.
    // This was written as a literal z-20 — the same value Window.tsx gives the
    // ACTIVE window — and because the column renders later in the DOM it won
    // the tie and floated OVER windows: the clock sat across Activity Monitor's
    // title bar and a notification card covered one of its metrics. Widgets are
    // wallpaper furniture, not an overlay; a window that reaches them should
    // cover them, exactly as on any desktop. The ordering now lives in
    // zLayers.ts and is asserted by zLayers.test.ts.
    <div
      data-desktop-widgets
      className="fixed right-3 w-60 max-w-[calc(100vw-1.5rem)] flex flex-col gap-3 pointer-events-auto"
      style={{ top: '2.75rem', zIndex: Z_DESKTOP_WIDGETS }}
    >
      <ClockWidget />
      {/* The agenda widget owns its own data seam (/api/pim/calendar/events)
          and its own honest empty/offline states — rendered embedded so it
          stacks in this column instead of pinning itself to the viewport. */}
      <CalendarWidget embedded />
      <NotificationsWidget />
    </div>
  )
}
