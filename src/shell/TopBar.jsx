import { useShell } from '../providers/ShellProvider'
import LifePulse from '../core/SystemPulse'
import TrustBadge from './TrustBadge'
import ThemeToggle from '../core/ThemeToggle'
import { useNarrow } from './useNarrow'
import './shell-chrome.css'

// ── Virtual-desktop indicator ────────────────────────────────────────────────
// Shows "Desktop N" with a close affordance, only when more than one space
// exists. On narrow screens the label collapses to a compact "N" pill so the
// bar never crowds the system tray.
function DesktopIndicator({ narrow }) {
  const { desktops, activeDesktop, removeDesktop } = useShell()
  const list = Object.values(desktops)
  if (list.length <= 1) return null
  const idx = list.findIndex((d) => d.id === activeDesktop)

  return (
    <div className="flex items-center gap-1 ml-1.5">
      <span
        className="text-[11px] leading-none px-1.5 py-0.5 rounded-md"
        style={{
          color: 'var(--text-tertiary)',
          background: 'color-mix(in srgb, var(--bg-hover) 60%, transparent)',
        }}
        title={`Desktop ${idx + 1} of ${list.length}`}
      >
        {narrow ? idx + 1 : `Desktop ${idx + 1}`}
      </span>
      <button
        onClick={() => removeDesktop(activeDesktop)}
        title="Close desktop (windows move to next)"
        aria-label="Close desktop"
        className="vshell-btn focus-primary w-4 h-4 flex items-center justify-center rounded text-[10px]"
      >
        {'×'}
      </button>
    </div>
  )
}

// ── Shared icon button ───────────────────────────────────────────────────────
function BarButton({ onClick, title, label, active, pressed, className = '', children }) {
  return (
    <button
      onClick={onClick}
      title={title}
      aria-label={label || title}
      aria-pressed={pressed}
      data-active={active ? 'true' : undefined}
      className={`vshell-btn focus-primary w-6 h-6 flex items-center justify-center rounded-md ${className}`}
    >
      {children}
    </button>
  )
}

// ── The desktop top bar / menu bar ───────────────────────────────────────────
// Premium, token-driven, translucent chrome. Left: system menu, spaces,
// launchpad + mission control. Right: sovereignty + theme + comms + tray.
// Collapses gracefully on small screens (non-essential affordances hide first).
export default function TopBar() {
  const { chatOpen, toggleMissionControl, toggleLaunchpad, toggleChat } = useShell()
  const narrow = useNarrow(680)

  return (
    <div className="vshell-bar absolute top-0 left-0 right-0 z-40 h-8 flex items-center justify-between px-1.5 backdrop-blur-xl">
      {/* Left cluster */}
      <div className="flex items-center gap-0.5 min-w-0">
        <LifePulse />
        <DesktopIndicator narrow={narrow} />
        <span className="vshell-div mx-1 hidden sm:block" />
        {/* Launchpad — rocket */}
        <BarButton onClick={toggleLaunchpad} title="Applications">
          <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="none" stroke="currentColor" strokeWidth="1.3">
            <path d="M8 1.5c0 0-3 3-3 7.5h6c0-4.5-3-7.5-3-7.5z" fill="currentColor" opacity="0.4" stroke="none" />
            <path d="M8 1.5c0 0-3 3-3 7.5h6c0-4.5-3-7.5-3-7.5z" />
            <path d="M5 9l-1.5 3L5 11" />
            <path d="M11 9l1.5 3L11 11" />
            <path d="M6.5 12.5h3" strokeLinecap="round" />
            <circle cx="8" cy="6.5" r="1" fill="currentColor" stroke="none" opacity="0.7" />
          </svg>
        </BarButton>
        {/* Mission Control — staggered windows */}
        <BarButton onClick={toggleMissionControl} title="Mission Control (F3)">
          <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="none" stroke="currentColor" strokeWidth="1.2">
            <rect x="1" y="4" width="8" height="6" rx="1" fill="currentColor" opacity="0.25" />
            <rect x="1" y="4" width="8" height="6" rx="1" />
            <line x1="1" y1="6" x2="9" y2="6" />
            <rect x="7" y="1.5" width="8" height="6" rx="1" fill="currentColor" opacity="0.15" />
            <rect x="7" y="1.5" width="8" height="6" rx="1" />
            <line x1="7" y1="3.5" x2="15" y2="3.5" />
          </svg>
        </BarButton>
      </div>

      {/* Right cluster — status tray */}
      <div className="flex items-center gap-0.5 min-w-0">
        {/* Always-on sovereignty indicator — AI tier + egress + at-rest lock. */}
        <TrustBadge />
        <span className="vshell-div mx-1" />
        {/* Quick theme cycle: System → Light → Dark */}
        <ThemeToggle variant="bar" />
        {/* Chat toggle */}
        <BarButton onClick={toggleChat} title="Chat (Ctrl+K)" label="Chat" active={chatOpen} pressed={chatOpen}>
          <svg viewBox="0 0 16 16" className="w-3.5 h-3.5">
            <path d="M2 3a2 2 0 012-2h8a2 2 0 012 2v6a2 2 0 01-2 2H6l-3 3V11H4a2 2 0 01-2-2V3z" fill="currentColor" opacity="0.75" />
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
            <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round">
              <path d="M2 5V2h3M11 2h3v3M14 11v3h-3M5 14H2v-3" />
            </svg>
          </BarButton>
        )}
        <span className="vshell-div mx-1 hidden sm:block" />
        {/* System tray — wifi · battery · notifications · clock · exposure chip */}
        <LifePulse compact />
      </div>
    </div>
  )
}
