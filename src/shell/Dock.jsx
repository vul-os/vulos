import { useShell } from '../providers/ShellProvider'
import AppIcon, { APP_LOGOS, APP_COLORS } from '../core/AppIcons'
import './shell-chrome.css'

// Dock — a bottom-center taskbar of the windows open on the active desktop.
// Its main job is to make MINIMIZED windows recoverable (Mission Control hides
// them), but it doubles as a fast window switcher:
//   · click a floating window        → focus/raise it
//   · click the focused window        → minimize it (toggle)
//   · click a minimized window        → restore + focus it
// It only renders when at least one window exists, so an empty desktop keeps the
// clean Home backdrop. Matches the shell's frosted-glass chrome and is
// token-driven throughout: every colour, radius and shadow resolves from an --*
// variable so it tracks theme + accent with zero per-theme branching. On small
// screens it caps its width and scrolls horizontally rather than overflowing the
// viewport, and it lifts clear of the home-indicator via the safe-area inset.
export default function Dock() {
  const { windows, activeWindow, focusWindow, minimizeWindow } = useShell()

  if (!windows || windows.length === 0) return null

  const onClick = (win) => {
    if (win.minimized) {
      minimizeWindow(win.id) // toggles minimized → visible
      focusWindow(win.id)
    } else if (win.id === activeWindow) {
      minimizeWindow(win.id)
    } else {
      focusWindow(win.id)
    }
  }

  return (
    <div
      className="fixed left-1/2 -translate-x-1/2 z-30 pointer-events-none max-w-[calc(100vw-1.25rem)]"
      style={{ bottom: 'max(0.625rem, calc(env(safe-area-inset-bottom, 0px) + 0.375rem))' }}
    >
      <div className="vshell-dock flex items-end gap-1.5 px-2.5 py-2 rounded-2xl backdrop-blur-xl pointer-events-auto overflow-x-auto no-scrollbar">
        {windows.map((win) => {
          const active = win.id === activeWindow && !win.minimized
          return (
            <button
              key={win.id}
              onClick={() => onClick(win)}
              title={win.title}
              aria-label={`${win.title}${win.minimized ? ' (minimized)' : ''}`}
              aria-pressed={active}
              className="vshell-dock-item focus-primary rounded-2xl group relative flex flex-col items-center shrink-0"
            >
              <span
                data-active={active ? 'true' : undefined}
                className={`vshell-dock-tile flex items-center justify-center w-11 h-11 rounded-[13px]
                  ${win.minimized ? 'opacity-45 hover:opacity-90' : 'opacity-100'}`}
              >
                {APP_LOGOS[win.appId]
                  ? <img src={APP_LOGOS[win.appId]} alt="" className="w-[26px] h-[26px] object-contain" loading="lazy" />
                  : <AppIcon id={win.appId} size={26} color={APP_COLORS[win.appId]} />}
              </span>
              {/* Running indicator — a dot under the icon; accent when focused. */}
              <span
                data-active={active ? 'true' : undefined}
                className="vshell-dot absolute -bottom-1 w-1 h-1 rounded-full"
              />
              {/* Tooltip — frosted chip that reads in both themes. */}
              <span
                className="pointer-events-none absolute bottom-full mb-2 whitespace-nowrap px-2.5 py-1 rounded-lg text-[11px] font-medium opacity-0 translate-y-0.5 group-hover:opacity-100 group-hover:translate-y-0 transition-all duration-150"
                style={{
                  background: 'color-mix(in srgb, var(--bg-elevated) 92%, transparent)',
                  border: '1px solid color-mix(in srgb, var(--border-strong) 65%, transparent)',
                  color: 'var(--text-secondary)',
                  boxShadow: 'var(--shadow-md)',
                }}
              >
                {win.title}
              </span>
            </button>
          )
        })}
      </div>
    </div>
  )
}
