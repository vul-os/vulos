import { useShell } from '../providers/ShellProvider'
import AppIcon from '../core/AppIcons'

export default function Dock() {
  const {
    windows, activeWindow, focusWindow, minimizeWindow,
    desktops, activeDesktop, switchDesktop, addDesktop,
    popoutApp,
    toggleLaunchpad, toggleChat, chatOpen,
  } = useShell()

  const desktopList = Object.values(desktops)

  return (
    <div className="fixed bottom-3 left-1/2 -translate-x-1/2 z-40 flex items-end gap-2">
      {/* Desktop switcher (above dock, only shows if >1 desktop) */}
      {desktopList.length > 1 && (
        <div className="flex items-center gap-1 bg-neutral-900/60 backdrop-blur-xl border border-neutral-800/40 rounded-xl px-1.5 py-1 mb-0.5">
          {desktopList.map((d, i) => (
            <button
              key={d.id}
              onClick={() => switchDesktop(d.id)}
              className={`w-6 h-4 rounded text-[8px] font-mono transition-colors
                ${d.id === activeDesktop
                  ? 'bg-neutral-600 text-white'
                  : 'bg-neutral-800/60 text-neutral-500 hover:bg-neutral-700/60'}`}
            >
              {i + 1}
            </button>
          ))}
          <button
            onClick={() => addDesktop()}
            className="w-4 h-4 rounded text-[10px] text-neutral-600 hover:text-neutral-400 hover:bg-neutral-800/60 transition-colors"
          >
            +
          </button>
        </div>
      )}

      {/* Main dock */}
      <div className="flex items-center gap-1 bg-neutral-900/70 backdrop-blur-xl border border-neutral-800/60 rounded-2xl px-2 py-1.5">
        {/* Launchpad */}
        <DockButton label="Applications" onClick={toggleLaunchpad}>
          <svg viewBox="0 0 16 16" className="w-5 h-5 text-neutral-400">
            <rect x="1" y="1" width="6" height="6" rx="1.5" fill="currentColor" opacity="0.7" />
            <rect x="9" y="1" width="6" height="6" rx="1.5" fill="currentColor" opacity="0.5" />
            <rect x="1" y="9" width="6" height="6" rx="1.5" fill="currentColor" opacity="0.5" />
            <rect x="9" y="9" width="6" height="6" rx="1.5" fill="currentColor" opacity="0.3" />
          </svg>
        </DockButton>

        {/* Desktop indicator (single desktop shows nothing, multi shows number) */}
        {desktopList.length <= 1 && desktopList.length > 0 && (
          <DockButton label="Add Desktop" onClick={() => addDesktop()}>
            <svg viewBox="0 0 16 16" className="w-5 h-5 text-neutral-400">
              <rect x="2" y="3" width="12" height="9" rx="1" fill="none" stroke="currentColor" strokeWidth="1.2" opacity="0.5" />
              <path d="M5 14h6" stroke="currentColor" strokeWidth="1.2" opacity="0.3" />
            </svg>
          </DockButton>
        )}

        {windows.length > 0 && <div className="w-px h-6 bg-neutral-700/50 mx-1" />}

        {/* Open windows */}
        {windows.map(win => (
          <DockButton
            key={win.id}
            label={win.title}
            active={activeWindow === win.id}
            minimized={win.minimized}
            onClick={() => win.minimized ? minimizeWindow(win.id) : focusWindow(win.id)}
            onDoubleClick={() => popoutApp({ title: win.title, url: win.url, icon: win.icon, appId: win.appId })}
          >
            <AppIcon id={win.appId} size={18} color="#a3a3a3" />
          </DockButton>
        ))}

        <div className="w-px h-6 bg-neutral-700/50 mx-1" />

        {/* Chat */}
        <DockButton label="Chat" active={chatOpen} onClick={toggleChat}>
          <svg viewBox="0 0 16 16" className="w-5 h-5 text-neutral-400">
            <path d="M2 3a2 2 0 012-2h8a2 2 0 012 2v6a2 2 0 01-2 2H6l-3 3V11H4a2 2 0 01-2-2V3z" fill="currentColor" opacity="0.6" />
          </svg>
        </DockButton>
      </div>
    </div>
  )
}

function DockButton({ children, label, active, minimized, onClick, onDoubleClick }) {
  return (
    <button
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      title={label + (onDoubleClick ? ' (double-click to pop out)' : '')}
      className={`relative w-10 h-10 flex items-center justify-center rounded-xl transition-all
        ${active ? 'bg-neutral-700/60' : 'hover:bg-neutral-800/60'}
        ${minimized ? 'opacity-50' : ''}`}
    >
      {children}
      {active && (
        <span className="absolute -bottom-0.5 left-1/2 -translate-x-1/2 w-1 h-1 rounded-full bg-neutral-400" />
      )}
    </button>
  )
}
