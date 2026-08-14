import { useMemo, type MouseEventHandler, type ReactNode, type SVGProps } from 'react'
import { useShell } from '../providers/ShellProvider'
import { isMultiScreen, readScreenIdentity } from '../providers/screenIdentity'
import LifePulse from '../core/SystemPulse'
import TrustBadge from './TrustBadge'
import ThemeToggle from '../core/ThemeToggle'
import { useNarrow } from './useNarrow'
import './shell-chrome.css'

// ── Virtual-desktop indicator ────────────────────────────────────────────────
// Shows "Desktop N" with a close affordance, only when more than one space
// exists. On narrow screens the label collapses to a compact "N" pill so the
// bar never crowds the system tray.
function DesktopIndicator({ narrow }: { narrow: boolean }) {
  const { desktops, activeDesktop, removeDesktop } = useShell()
  const list = Object.values(desktops)
  if (list.length <= 1) return null
  const idx = list.findIndex((d) => d.id === activeDesktop)

  return (
    <div className="flex items-center gap-1 ml-1">
      <span
        className="text-[12px] font-medium leading-none px-2 py-1 rounded-md"
        style={{
          color: 'var(--text-tertiary)',
          background: 'color-mix(in srgb, var(--bg-hover) 55%, transparent)',
        }}
        title={`Desktop ${idx + 1} of ${list.length}`}
      >
        {narrow ? idx + 1 : `Desktop ${idx + 1}`}
      </span>
      <button
        onClick={() => removeDesktop(activeDesktop)}
        title="Close desktop (windows move to next)"
        aria-label="Close desktop"
        className="vshell-btn focus-primary w-5 h-5 flex items-center justify-center rounded-md text-[11px] leading-none"
      >
        {'×'}
      </button>
    </div>
  )
}

// ── Screen indicator ─────────────────────────────────────────────────────────
// A machine with two monitors runs one kiosk browser per connected output (see
// roadmap/SCREENS.md and providers/screenIdentity.ts). Those instances are
// otherwise indistinguishable on screen — same shell, same origin, same
// desktop — so "which monitor am I looking at, and is this the one that will
// answer my keyboard?" has no visible answer at all.
//
// This is that answer, and it is the same affordance every other desktop OS
// grew for the same reason ("Identify displays"): the output's own name, next
// to the space indicator, in the chrome that is already there.
//
// Renders NOTHING on a single screen — which is every hand-opened tab, every
// phone on the LAN, and a one-monitor box. A chip saying "Screen 1 of 1" is
// noise, and the single-display path must look exactly as it did before
// multi-screen existed.
function ScreenIndicator({ narrow }: { narrow: boolean }) {
  // Read once per mount: the identity is fixed for the life of the document —
  // the launcher assigns it in the URL and a browser cannot migrate between
  // outputs without being relaunched.
  const id = useMemo(() => readScreenIdentity(), [])

  // This component renders the chip and NOTHING ELSE. It deliberately does not
  // touch document.title.
  //
  // The window title is a compositor contract — labwc matches a windowRule on
  // it to place this window on the right output — and it used to be set from an
  // effect right here. That was wrong twice over. It made the contract depend on
  // this component mounting, and TopBar mounts only inside DesktopCanvas, i.e.
  // only after setup and after login: the first two-display boot landed on the
  // setup wizard and the second monitor stayed black. And if BOTH places set the
  // title, whichever ran last would silently win, which is a race with no
  // failure mode you could see.
  //
  // So there is exactly one writer, applyScreenWindowTitle() at module scope in
  // App.tsx, and it has already run by the time anything renders. This is the
  // reader.

  if (!isMultiScreen(id) || !id) return null

  return (
    <span
      className="text-[12px] font-medium leading-none px-2 py-1 rounded-md ml-1"
      style={{
        color: 'var(--text-tertiary)',
        background: 'color-mix(in srgb, var(--bg-hover) 55%, transparent)',
      }}
      // The full form always reaches assistive tech and hover, even when the bar
      // is too narrow to show it.
      title={`Screen ${id.index} of ${id.total} — ${id.name}`}
    >
      {narrow ? `S${id.index}` : `Screen ${id.index} · ${id.name}`}
    </span>
  )
}

// ── Shared icon button ───────────────────────────────────────────────────────
// One calm, tappable affordance for every bar action. Lifts to a token surface
// on hover and shows an accent-tinted state when toggled (vshell-btn CSS).
interface BarButtonProps {
  onClick: MouseEventHandler<HTMLButtonElement>
  title: string
  label?: string
  active?: boolean
  pressed?: boolean
  className?: string
  children: ReactNode
}
function BarButton({ onClick, title, label, active, pressed, className = '', children }: BarButtonProps) {
  return (
    <button
      onClick={onClick}
      title={title}
      aria-label={label || title}
      aria-pressed={pressed}
      data-active={active ? 'true' : undefined}
      className={`vshell-btn focus-primary w-7 h-7 flex items-center justify-center rounded-md ${className}`}
    >
      {children}
    </button>
  )
}

// Shared stroke-icon presets — one weight (1.7), rounded joins, 24-unit grid,
// monochrome (inherits currentColor). The whole bar reads as one icon system.
const ICON: SVGProps<SVGSVGElement> = {
  viewBox: '0 0 24 24',
  className: 'w-4 h-4',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.7,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
}

// ── The desktop top bar / menu bar ───────────────────────────────────────────
// Premium, token-driven, translucent chrome. Left: system pulse, spaces,
// launchpad + mission control. Right: sovereignty + theme + comms + tray.
// Collapses gracefully on small screens (non-essential affordances hide first),
// and respects horizontal safe-area insets on notched/rounded displays.
export default function TopBar() {
  const { chatOpen, toggleMissionControl, toggleLaunchpad, toggleChat } = useShell()
  const narrow = useNarrow(680)

  return (
    <div className="vshell-bar absolute top-0 left-0 right-0 z-40 h-8 flex items-center justify-between px-2 backdrop-blur-xl safe-px">
      {/* Left cluster */}
      <div className="flex items-center gap-0.5 min-w-0">
        <LifePulse />
        <DesktopIndicator narrow={narrow} />
        <ScreenIndicator narrow={narrow} />
        <span className="vshell-div mx-1.5 hidden sm:block" />
        {/* Launchpad — application grid */}
        <BarButton onClick={toggleLaunchpad} title="Applications" label="Applications">
          <svg {...ICON}>
            <rect x="4" y="4" width="6.2" height="6.2" rx="1.9" />
            <rect x="13.8" y="4" width="6.2" height="6.2" rx="1.9" />
            <rect x="4" y="13.8" width="6.2" height="6.2" rx="1.9" />
            <rect x="13.8" y="13.8" width="6.2" height="6.2" rx="1.9" />
          </svg>
        </BarButton>
        {/* Mission Control — staggered spaces */}
        <BarButton onClick={toggleMissionControl} title="Mission Control (F3)" label="Mission Control">
          <svg {...ICON}>
            <rect x="2.6" y="6.4" width="11.4" height="8" rx="2.1" />
            <rect x="10.4" y="3" width="10.6" height="7.4" rx="2.1" />
          </svg>
        </BarButton>
      </div>

      {/* Right cluster — status tray */}
      <div className="flex items-center gap-0.5 min-w-0">
        {/* Always-on sovereignty indicator — AI tier + egress + at-rest lock. */}
        <TrustBadge />
        <span className="vshell-div mx-1.5" />
        {/* Quick theme cycle: System → Light → Dark */}
        <ThemeToggle variant="bar" />
        {/* Chat toggle */}
        <BarButton onClick={toggleChat} title="Chat (Ctrl+K)" label="Chat" active={chatOpen} pressed={chatOpen}>
          <svg {...ICON}>
            <path d="M4 6.5A2.5 2.5 0 016.5 4h11A2.5 2.5 0 0120 6.5v6A2.5 2.5 0 0117.5 15H9l-4 3.2V15h-.2A.8.8 0 014 14.2z" />
          </svg>
        </BarButton>
        {/* Fullscreen — first to hide on small screens */}
        {!narrow && (
          <BarButton
            onClick={() => {
              if (document.fullscreenElement) document.exitFullscreen()
              else document.documentElement.requestFullscreen?.()
            }}
            title="Toggle fullscreen"
          >
            <svg {...ICON}>
              <path d="M4 8.5V4h4.5M15.5 4H20v4.5M20 15.5V20h-4.5M8.5 20H4v-4.5" />
            </svg>
          </BarButton>
        )}
        <span className="vshell-div mx-1.5 hidden sm:block" />
        {/* System tray — wifi · battery · notifications · clock · exposure chip */}
        <LifePulse compact />
      </div>
    </div>
  )
}
