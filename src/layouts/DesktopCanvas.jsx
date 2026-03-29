import { useShell } from '../providers/ShellProvider'
import LifePulse from '../core/SystemPulse'
import Portal from '../core/Portal'
import Window from '../shell/Window'
import Dock from '../shell/Dock'
import Launchpad from '../shell/Launchpad'
import Toasts from '../shell/Toasts'
import { useWallpaper, DEFAULT_WALLPAPER } from '../core/useWallpaper.jsx'
import { useTheme } from '../core/ThemeProvider'

function DesktopIndicator() {
  const { desktops, activeDesktop, switchDesktop, removeDesktop } = useShell()
  const list = Object.values(desktops)
  if (list.length <= 1) return null
  const idx = list.findIndex(d => d.id === activeDesktop)

  return (
    <div className="flex items-center gap-1 ml-2">
      <span className="text-[11px] text-neutral-500 mr-0.5">Desktop {idx + 1}</span>
      <button
        onClick={() => removeDesktop(activeDesktop)}
        title="Close desktop (windows move to next)"
        className="w-4 h-4 flex items-center justify-center rounded text-neutral-600 hover:text-red-400 hover:bg-neutral-800/60 transition-colors text-[10px]"
      >
        {'\u00D7'}
      </button>
    </div>
  )
}

export default function DesktopCanvas() {
  const { windows, allWindows, chatOpen } = useShell()
  const { wallpaper } = useWallpaper()
  const { isDark } = useTheme()

  const bgSrc = wallpaper || DEFAULT_WALLPAPER

  return (
    <div className="fixed inset-0 bg-neutral-950 overflow-hidden">
      {/* Desktop wallpaper — always visible behind windows */}
      <div
        className="absolute inset-0 overflow-hidden flex items-center justify-center transition-colors duration-500"
        style={{ background: isDark ? '#0c0c0c' : '#f0f0f0' }}
      >
        {wallpaper ? (
          <img src={wallpaper} alt="" className="block w-full h-full object-cover" />
        ) : (
          <div className="flex flex-col items-center gap-3 select-none">
            <img src={DEFAULT_WALLPAPER} alt="" className="w-24 h-24" style={{ opacity: isDark ? 0.12 : 0.06, filter: isDark ? 'brightness(3)' : 'none' }} />
            <div style={{ opacity: isDark ? 0.12 : 0.06 }}>
              <div className="text-center text-2xl font-light tracking-[0.3em]" style={{ color: isDark ? '#fff' : '#000' }}>VulOS</div>
              <div className="text-center text-[10px] tracking-[0.2em] mt-1" style={{ color: isDark ? '#fff' : '#000' }}>alpha</div>
            </div>
          </div>
        )}
      </div>

      {/* Menu bar */}
      <div className={`absolute top-0 left-0 right-0 z-40 h-8 flex items-center justify-between px-1 backdrop-blur-xl ${isDark ? 'bg-neutral-800/70 border-b border-neutral-700/40' : 'bg-neutral-900/60 border-b border-neutral-800/30'}`}>
        <div className="flex items-center">
          <LifePulse />
          <DesktopIndicator />
        </div>
        <LifePulse compact />
      </div>

      {/* Windows area — render ALL windows persistently, hide inactive desktops via CSS */}
      <div className="absolute inset-0 pt-8 pb-16">
        {allWindows.map(win => (
          <Window key={win.id} win={{ ...win, minimized: win.minimized || !win._visible }} />
        ))}
      </div>

      {/* Chat panel — right side */}
      {chatOpen && (
        <div className="absolute top-8 right-0 bottom-16 w-[380px] z-30">
          <Portal />
        </div>
      )}

      {/* Dock */}
      <Dock />

      {/* Launchpad overlay */}
      <Launchpad />

      {/* Toast notifications */}
      <Toasts />
    </div>
  )
}
