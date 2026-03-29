import { useState } from 'react'
import { useShell } from '../providers/ShellProvider'
import LifePulse from '../core/SystemPulse'
import Portal from '../core/Portal'
import Launchpad from '../shell/Launchpad'
import Toasts from '../shell/Toasts'

export default function MobileStack() {
  const { windows, conversation, toggleLaunchpad } = useShell()
  const [activeTab, setActiveTab] = useState('home')

  return (
    <div className="fixed inset-0 bg-neutral-950 flex flex-col overflow-hidden">
      {/* Status bar */}
      <div className="shrink-0 px-3 h-8 flex items-center justify-between bg-neutral-900/60 backdrop-blur-xl border-b border-neutral-800/30">
        <div className="flex items-center gap-2">
          <img src="/vulos.png" alt="" className="w-3.5 h-3.5 opacity-70" />
          <span className="text-xs font-semibold text-neutral-300">vula</span>
        </div>
        <LifePulse compact />
      </div>

      {/* Main content */}
      <div className="flex-1 overflow-hidden">
        {activeTab === 'home' && (
          <div className="h-full flex flex-col">
            {conversation.length === 0 && windows.length === 0 ? (
              <div className="flex-1 flex flex-col items-center justify-center px-6">
                <LifePulse />
              </div>
            ) : null}
            <div className={`${conversation.length > 0 || windows.length > 0 ? 'flex-1' : ''} flex flex-col`}>
              <Portal mode="fullscreen" />
            </div>
          </div>
        )}

        {activeTab === 'apps' && (
          <div className="h-full overflow-y-auto">
            {windows.length === 0 ? (
              <div className="flex items-center justify-center h-full text-neutral-600 text-sm">
                No active applications
              </div>
            ) : (
              <div className="p-3 space-y-3">
                {windows.map(win => (
                  <MobileCard key={win.id} win={win} />
                ))}
              </div>
            )}
          </div>
        )}
      </div>

      {/* Tab bar */}
      <div className="shrink-0 flex items-center bg-neutral-900/80 backdrop-blur-md border-t border-neutral-800/30">
        <TabButton label="Home" active={activeTab === 'home'} onClick={() => setActiveTab('home')}>
          <svg viewBox="0 0 16 16" className="w-5 h-5"><path d="M8 1L1 7h2v7h4v-4h2v4h4V7h2L8 1z" fill="currentColor" /></svg>
        </TabButton>
        <TabButton label="Apps" active={activeTab === 'apps'} onClick={() => setActiveTab('apps')} badge={windows.length || null}>
          <svg viewBox="0 0 16 16" className="w-5 h-5">
            <rect x="1" y="1" width="6" height="6" rx="1" fill="currentColor" />
            <rect x="9" y="1" width="6" height="6" rx="1" fill="currentColor" opacity="0.6" />
            <rect x="1" y="9" width="6" height="6" rx="1" fill="currentColor" opacity="0.6" />
            <rect x="9" y="9" width="6" height="6" rx="1" fill="currentColor" opacity="0.3" />
          </svg>
        </TabButton>
        <TabButton label="Applications" onClick={toggleLaunchpad}>
          <svg viewBox="0 0 16 16" className="w-5 h-5"><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" strokeWidth="1.5" /><path d="M8 5v6M5 8h6" stroke="currentColor" strokeWidth="1.5" /></svg>
        </TabButton>
      </div>

      {/* Launchpad overlay */}
      <Launchpad />
      <Toasts />
    </div>
  )
}

function MobileCard({ win }) {
  const { closeWindow } = useShell()

  return (
    <div className="rounded-xl overflow-hidden border border-neutral-800/50 bg-neutral-900">
      <div className="flex items-center justify-between px-3 py-2">
        <span className="text-sm text-neutral-400">{win.icon} {win.title}</span>
        <button onClick={() => closeWindow(win.id)} className="text-xs text-neutral-600 hover:text-red-400">✕</button>
      </div>
      <div className="relative h-[50vh] bg-neutral-950">
        {win.component ? (
          <div className="absolute inset-0 overflow-y-auto">{win.component}</div>
        ) : (
          <iframe
            src={win.html ? undefined : win.url}
            srcDoc={win.html || undefined}
            title={win.title}
            className="absolute inset-0 w-full h-full border-0"
            sandbox={win.html ? 'allow-scripts' : 'allow-scripts allow-same-origin allow-forms allow-popups'}
          />
        )}
      </div>
    </div>
  )
}

function TabButton({ children, label, active, badge, onClick }) {
  return (
    <button onClick={onClick} className={`flex-1 flex flex-col items-center gap-0.5 py-2 transition-colors ${active ? 'text-white' : 'text-neutral-600'}`}>
      <div className="relative">
        {children}
        {badge && (
          <span className="absolute -top-1 -right-1.5 w-3.5 h-3.5 bg-neutral-600 rounded-full text-[8px] text-white flex items-center justify-center">{badge}</span>
        )}
      </div>
      <span className="text-[10px]">{label}</span>
    </button>
  )
}
