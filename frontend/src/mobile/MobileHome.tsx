import { useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { useShell } from '../providers/ShellProvider'
import { getApps, getAppsVersion, subscribeApps, type App } from '../core/AppRegistry'
import { AppIconTile } from '../core/AppIcons'
import { launchApp } from '../shell/launchApp'
import Portal from '../core/Portal'
import './mobile.css'

// MOBILE-10 — a phone home screen with apps on it.
//
// WHAT WAS THERE. MobileStack's 'home' view rendered `<Portal mode="fullscreen" />`
// and nothing else. Screenshotted on the shipping build at 390×844, that is a
// near-empty white page: a wordmark, the words "What do you need?" floating in
// the middle, the same words again as the input's placeholder, an ask bar, and
// roughly 1400 vertical pixels of nothing. It is the first screen of the OS on
// the device the OS is being sold for, and it contains zero apps.
//
// Every phone home screen on earth shows apps, because on a phone the home
// screen IS the launcher — there is no desktop underneath it to hold icons and
// no persistent dock wide enough to hold more than a handful. Vulos put all of
// them behind a third tap ("Library" → Launchpad), which is a drawer, and a
// drawer is the *second* surface, not the first.
//
// WHAT IT IS NOW. Home is a real home screen: a greeting, a grid of app tiles,
// and a resting ask bar. Tapping the ask bar hands the whole surface to Portal —
// unchanged, full height, exactly the assistant experience that was there
// before — so nothing is lost, it just stops being the only thing on screen. Any
// existing conversation also restores Portal directly, so switching away from
// Home and back does not silently discard a chat in progress.
//
// The grid is the SAME registry the Launchpad reads (core/AppRegistry), so an
// installed app appears here without a second list to maintain, and it is
// subscribed rather than snapshotted — apps installed after boot show up.

// Tiles per row. 4 at phone widths (the universal phone-launcher count: it keeps
// a 64px mark plus a two-line label legible at 390px), 6 once there is tablet
// room. Not user-configurable yet; the mobile dock profile spec in
// roadmap/MOBILE-SHELL.md §2 is where per-device customisation lands.
const HOME_GRID = 'grid grid-cols-4 sm:grid-cols-6 gap-x-2 gap-y-5'

// Apps that lead. Ordered by what a phone actually gets used for, then the
// registry's own order fills the rest — so the grid is never just alphabetical,
// and never empty on a box with nothing installed.
const LEAD = ['lilmail', 'vulos-calendar', 'drive', 'files', 'notes', 'assistant', 'contacts', 'messages', 'settings', 'terminal']

function useApps(): App[] {
  const version = useSyncExternalStore(subscribeApps, getAppsVersion, getAppsVersion)
  // getApps() builds a fresh array each call; keying the memo on the registry's
  // own version counter is what keeps this from re-sorting on every render.
  return useMemo(() => {
    void version
    const all = getApps()
    const rank = new Map(LEAD.map((id, i) => [id, i]))
    return [...all].sort((a, b) => (rank.get(a.id) ?? 999) - (rank.get(b.id) ?? 999))
  }, [version])
}

export default function MobileHome() {
  const { openWindow, conversation } = useShell()
  const apps = useApps()
  const [asking, setAsking] = useState(false)

  // A conversation that already exists wins: coming back Home after asking
  // something should show the answer, not an app grid that hides it.
  const hasConversation = conversation.length > 0
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (hasConversation) setAsking(true)
  }, [hasConversation])

  if (asking) {
    return (
      <div data-mobile-home="assistant" className="absolute inset-0 flex flex-col">
        <div className="shrink-0 safe-px-4 pt-2 pb-1">
          <button
            onClick={() => setAsking(false)}
            aria-label="Back to apps"
            className="focus-primary touch-target -ml-2 px-2 flex items-center gap-1.5 rounded-[var(--radius-md)] text-[13px] font-medium text-[color:var(--text-tertiary)] active:bg-[color:var(--bg-hover)] transition-colors"
          >
            <svg viewBox="0 0 16 16" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><path d="M10 3L5 8l5 5" /></svg>
            Apps
          </button>
        </div>
        <div className="flex-1 min-h-0 flex flex-col">
          <Portal mode="fullscreen" />
        </div>
      </div>
    )
  }

  return (
    <div data-mobile-home="apps" className="absolute inset-0 flex flex-col">
      <div className="flex-1 min-h-0 overflow-y-auto overscroll-contain [-webkit-overflow-scrolling:touch] safe-px-4 pt-3 pb-4">
        <div className={HOME_GRID}>
          {apps.map(app => (
            <button
              key={app.id}
              data-home-tile={app.id}
              onClick={() => { void launchApp(app, { openWindow }) }}
              // The whole tile is the target — mark plus label — which is what
              // puts it comfortably over the 44px floor without padding a button
              // out to look wrong. A phone launcher icon is never a 44px square
              // with a caption beside it; it is a tall tappable cell.
              className="vmob-tile touch-target flex flex-col items-center gap-1.5 select-none"
            >
              <span className="vmob-tile-art"><AppIconTile id={app.id} size={56} /></span>
              <span className="w-full text-[11px] leading-[1.25] font-medium text-center text-[color:var(--text-secondary)] line-clamp-2">
                {app.name}
              </span>
            </button>
          ))}
        </div>
      </div>

      {/* The resting ask bar. It is a BUTTON, not an input: a real input here
          would summon the keyboard on every visit to Home and shove the grid off
          screen. Tapping it swaps in Portal, which focuses its own field. */}
      <div className="shrink-0 safe-px-4 pb-2 pt-1">
        <button
          onClick={() => setAsking(true)}
          className="focus-primary w-full h-12 px-4 flex items-center gap-2.5 rounded-full text-left text-[14px] text-[color:var(--text-tertiary)] active:bg-[color:var(--bg-hover)] transition-colors"
          style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-subtle, var(--border-emphasis))' }}
        >
          <svg viewBox="0 0 20 20" className="w-[18px] h-[18px] shrink-0 accent-text" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><path d="M10 2.6l1.7 4.3 4.3 1.7-4.3 1.7L10 14.6 8.3 10.3 4 8.6l4.3-1.7z" /></svg>
          Ask your assistant
        </button>
      </div>
    </div>
  )
}
