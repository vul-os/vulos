import { useShell } from '../providers/ShellProvider'
import AppIcon from '../core/AppIcons'

// Dock — a bottom-center taskbar of the windows open on the active desktop.
// Its main job is to make MINIMIZED windows recoverable (Mission Control hides
// them), but it doubles as a fast window switcher:
//   · click a floating window        → focus/raise it
//   · click the focused window        → minimize it (toggle)
//   · click a minimized window        → restore + focus it
// It only renders when at least one window exists, so an empty desktop keeps
// the clean Home backdrop. Matches the shell's dark, blurred menu-bar aesthetic.
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
    <div className="fixed bottom-2.5 left-1/2 -translate-x-1/2 z-30 pointer-events-none">
      <div
        className="flex items-end gap-1 px-2 py-1.5 rounded-2xl backdrop-blur-xl pointer-events-auto shadow-2xl shadow-black/40"
        style={{ background: 'var(--bg-elevated, rgba(38,38,38,0.72))', border: '1px solid var(--border-default, rgba(64,64,64,0.5))' }}
      >
        {windows.map(win => {
          const active = win.id === activeWindow && !win.minimized
          return (
            <button
              key={win.id}
              onClick={() => onClick(win)}
              title={win.title}
              aria-label={`${win.title}${win.minimized ? ' (minimized)' : ''}`}
              aria-pressed={active}
              className="focus-primary rounded-xl group relative flex flex-col items-center"
            >
              <span
                className={`flex items-center justify-center w-10 h-10 rounded-xl transition-all duration-150
                  ${active ? 'bg-white/10' : 'hover:bg-white/5'}
                  ${win.minimized ? 'opacity-45 hover:opacity-80' : 'opacity-100'}`}
              >
                <AppIcon id={win.appId} size={22} />
              </span>
              {/* Running indicator — a dot under the icon; bright when focused. */}
              <span
                style={active ? { background: 'var(--accent)' } : undefined}
                className={`absolute -bottom-0.5 w-1 h-1 rounded-full transition-colors
                  ${active ? '' : 'bg-neutral-500 group-hover:bg-neutral-300'}`}
              />
              {/* Tooltip */}
              <span
                className="pointer-events-none absolute bottom-full mb-1.5 whitespace-nowrap px-2 py-0.5 rounded-md text-[11px] opacity-0 group-hover:opacity-100 transition-opacity"
                style={{ background: 'var(--bg-surface, #1c1c1c)', border: '1px solid var(--border-default, rgba(64,64,64,0.6))', color: 'var(--text-secondary, #d4d4d4)' }}
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
