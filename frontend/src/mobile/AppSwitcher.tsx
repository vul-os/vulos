import { useCallback, useRef, useState } from 'react'
import { useShell } from '../providers/ShellProvider'
import { AppIconTile } from '../core/AppIcons'
import './mobile.css'

// MOBILE-09 — the app switcher is what you reach for when the phone is
// struggling. It must not be the thing making it struggle.
//
// THE DEFECT THIS REPLACES. The previous switcher rendered
//
//     <div className="vmob-card-body h-[58vh] …"><MobileAppFrame win={win} /></div>
//
// for every running window — the same component the fullscreen app stack already
// has mounted. Measured on the real production build at 390×844 with Calendar
// open, opening the switcher took the document from one mount to two:
//
//     document.querySelectorAll('[data-calendar-app]').length === 2
//     document.querySelectorAll('iframe').length            === 2
//
// So a *second live instance* of every running app — a second iframe, a second
// AppBridge attachment, a second set of timers and fetches, a second React tree.
// The surface whose stated purpose is "see what is running and close it when
// memory is tight" doubled the memory of everything running, at the exact moment
// the user opened it because memory was tight. It also does not even look right:
// re-laying an app out at 76% width produces something that does not resemble
// the app you left, so it fails as a *preview* too.
//
// WHAT IT DOES INSTEAD. A card is an identity card, not a live app: the app's
// mark on a tinted plate, its title, and its state. That is what a phone recents
// card actually is — a static snapshot. The web platform has no API to snapshot
// a cross-origin iframe (and html2canvas-style tricks cannot see into one at
// all), so rather than fake a preview badly we show the identity clearly and
// spend the space on the two things the surface exists for: switching, and
// closing.
//
// CLOSING. Two paths, deliberately:
//   - an explicit ✕ button, ≥44px, always visible (discoverable, accessible,
//     and the only path that works with a screen reader or a keyboard); and
//   - drag the card UPWARD to dismiss (the idiom every phone user already has).
// The drag is a pointer-event gesture on the card body — in the middle of the
// screen, nowhere near an edge Android's gesture navigation owns (see
// roadmap/MOBILE-SHELL.md §3 for what edges are actually available). Using
// pointer events rather than touch events means the same code path is driven by
// a mouse, which is what makes it testable in Playwright.
//
// NOT BUILT HERE: "this app is not responding" and force-quit. A separate
// workstream owns process health and the Activity Monitor; the API this surface
// needs from it is specified in roadmap/MOBILE-SHELL.md §5 rather than
// re-implemented as a parallel poller.

// How far a card must travel upward before release dismisses it. Below this the
// card springs back. 25% of the card's own height, floored at 64px so a short
// card on a small phone still needs a deliberate drag rather than a twitch.
const DISMISS_RATIO = 0.25
const DISMISS_FLOOR = 64

interface AppSwitcherProps {
  onOpen: (id: number) => void
  onHome: () => void
}

export default function AppSwitcher({ onOpen, onHome }: AppSwitcherProps) {
  const { windows, closeWindow } = useShell()

  const closeAll = useCallback(() => {
    // Snapshot first: closeWindow mutates the list we are iterating.
    for (const id of windows.map(w => w.id)) closeWindow(id)
    onHome()
  }, [windows, closeWindow, onHome])

  return (
    <div
      data-mobile-switcher
      className="vmob-switcher absolute inset-0 z-10 flex flex-col overscroll-contain anim-sheet-up"
    >
      <div className="safe-px-4 pt-2.5 pb-1 shrink-0">
        <div className="mx-auto mb-3.5 h-1 w-9 rounded-full" style={{ background: 'var(--border-emphasis)' }} aria-hidden="true" />
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-[15px] font-semibold tracking-[-0.01em] text-[color:var(--text-primary)]">Running apps</h2>
          {windows.length > 1 ? (
            <button
              onClick={closeAll}
              className="focus-primary touch-target -mr-2 px-2 flex items-center justify-center rounded-[var(--radius-md)] text-[13px] font-medium text-[color:var(--text-tertiary)] active:text-[color:var(--status-danger)] active:bg-[color:var(--status-danger-soft)] transition-colors"
            >
              Close all
            </button>
          ) : (
            <span className="text-xs font-medium text-[color:var(--text-faint)] tabular-nums">{windows.length} open</span>
          )}
        </div>
      </div>

      {windows.length === 0 ? (
        <div className="flex-1 flex flex-col items-center justify-center text-center px-6">
          <p className="text-sm text-[color:var(--text-tertiary)]">No apps are running</p>
          <button onClick={onHome} className="focus-primary touch-target mt-2 px-3 text-[13px] font-medium accent-text active:opacity-70 transition-opacity">Back to home</button>
        </div>
      ) : (
        <>
          {/* A horizontally-snapping deck: the phone-recents metaphor, and the
              only layout at 390px where more than one running app is legible at
              once. sm: opens into a two-up grid for tablets. */}
          {/* items-center, not stretch: with no live preview to fill it, a card
              stretched to the full column height is ~1400px of empty plate
              around a 76px mark — that reads as broken, not as recents. The card
              sizes itself and the deck rides the vertical centre, which is both
              where phone recents put it and squarely in the thumb arc. */}
          <div className="flex-1 min-h-0 safe-px-4 py-3 flex items-center sm:grid sm:grid-cols-2 sm:content-start gap-3.5 overflow-x-auto sm:overflow-y-auto sm:overflow-x-hidden snap-x snap-mandatory sm:snap-none no-scrollbar [-webkit-overflow-scrolling:touch]">
            {windows.map(win => (
              <SwitcherCard
                key={win.id}
                appId={win.appId}
                title={win.title || win.appId}
                onOpen={() => onOpen(win.id)}
                onClose={() => closeWindow(win.id)}
              />
            ))}
          </div>
          <p className="shrink-0 safe-px-4 pb-2 text-center text-[12px] text-[color:var(--text-faint)]">
            Swipe a card up to close it
          </p>
        </>
      )}
    </div>
  )
}

interface SwitcherCardProps {
  appId: string
  title: string
  onOpen: () => void
  onClose: () => void
}

function SwitcherCard({ appId, title, onOpen, onClose }: SwitcherCardProps) {
  const ref = useRef<HTMLDivElement>(null)
  const start = useRef<number | null>(null)
  const [dy, setDy] = useState(0)
  const [dragging, setDragging] = useState(false)

  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    // Only a primary press starts a drag; a right-click or a second finger must
    // not hijack a card that is already moving.
    if (!e.isPrimary) return
    start.current = e.clientY
    setDragging(true)
    e.currentTarget.setPointerCapture(e.pointerId)
  }

  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    if (start.current === null) return
    // Upward only. Clamping downward travel to 0 keeps the card from being
    // dragged into the dock, and means a horizontal deck swipe (which carries a
    // little vertical noise) never visually disturbs the card.
    setDy(Math.min(0, e.clientY - start.current))
  }

  const finish = (e: React.PointerEvent<HTMLDivElement>) => {
    if (start.current === null) return
    const travelled = -Math.min(0, e.clientY - start.current)
    const h = ref.current?.getBoundingClientRect().height ?? 0
    start.current = null
    setDragging(false)
    if (travelled >= Math.max(DISMISS_FLOOR, h * DISMISS_RATIO)) onClose()
    else setDy(0)
  }

  const progress = Math.min(1, -dy / 220)

  return (
    <div
      ref={ref}
      data-switcher-card={appId}
      data-dragging={dragging}
      className="vmob-swipecard vmob-card rounded-[var(--radius-xl)] overflow-hidden shrink-0 w-[76%] snap-center sm:w-auto sm:shrink flex flex-col"
      style={{ transform: `translateY(${dy}px)`, opacity: 1 - progress * 0.85 }}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={finish}
      onPointerCancel={finish}
    >
      <div className="flex items-center gap-2.5 px-3 h-12 shrink-0">
        <span className="text-[13px] font-medium text-[color:var(--text-secondary)] truncate flex-1">{title}</span>
        <button
          onClick={onClose}
          aria-label={`Close ${title}`}
          className="focus-primary touch-target -mr-1.5 flex items-center justify-center rounded-full text-[color:var(--text-tertiary)] active:text-[color:var(--status-danger)] active:bg-[color:var(--status-danger-soft)] transition-colors"
        >
          <svg viewBox="0 0 16 16" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"><path d="M4 4l8 8M12 4l-8 8" /></svg>
        </button>
      </div>
      <button
        onClick={onOpen}
        aria-label={`Switch to ${title}`}
        className="vmob-cardart h-[44vh] sm:h-40 w-full flex flex-col items-center justify-center gap-3.5"
      >
        <AppIconTile id={appId} size={76} />
        <span className="text-[12px] font-medium text-[color:var(--text-tertiary)]">Running</span>
      </button>
    </div>
  )
}
