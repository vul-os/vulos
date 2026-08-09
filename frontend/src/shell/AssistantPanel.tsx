// AssistantPanel.tsx — the Assistant's permanent home in the shell.
//
// Chat was previously a side effect of `chatOpen` rendering core/Portal, an
// older intent-router prompt box that is not the Assistant the OS actually
// ships. This gives the real Assistant (builtin/assistant/Assistant) a
// first-class, always-reachable slide-over: the dock's Assistant tile and the
// menu bar's chat button both toggle it, Esc closes it, and it survives every
// window operation because it lives outside the window layer entirely.
//
// It does NOT reimplement any assistant behaviour — the streaming turn, the
// sovereignty badge/tier picker and the proposal gate all belong to that
// component and are rendered as-is. This file is chrome: a docked column on
// desktop, a full-height sheet on a phone, and the two controls (pop out to a
// real window, close) that a panel needs and the app itself cannot provide.
import { lazy, Suspense, useEffect, useCallback } from 'react'
import { useShell } from '../providers/ShellProvider'
import { useNarrow } from './useNarrow'
import { getAppById } from '../core/AppRegistry'
import { launchApp } from './launchApp'
import './shell-chrome.css'

const Assistant = lazy(() => import('../builtin/assistant/Assistant'))

// A real skeleton, not a blank box: a lazy chunk that has not landed yet must
// still look like the panel it is about to become, or the panel reads as
// broken (and a screenshot check happily passes on the empty div).
function PanelFallback() {
  return (
    <div className="h-full flex flex-col" style={{ background: 'var(--bg-base)' }} aria-busy="true">
      <div className="px-4 py-3 flex items-center gap-3 vshell-border-b">
        <span className="w-9 h-9 rounded-xl accent-bg-soft accent-text flex items-center justify-center text-[17px]" aria-hidden="true">✦</span>
        <span className="text-[14.5px] font-semibold" style={{ color: 'var(--text-primary)' }}>Assistant</span>
      </div>
      <div className="flex-1 flex items-center justify-center gap-2 text-[13px]" style={{ color: 'var(--text-tertiary)' }}>
        <span className="w-4 h-4 spinner" aria-hidden="true" />
        Loading assistant…
      </div>
    </div>
  )
}

export default function AssistantPanel() {
  const { chatOpen, setChat, openWindow } = useShell()
  const narrow = useNarrow(640)

  // Esc closes the panel — the shell-wide overlay dismissal contract. Bubble
  // phase (not capture) so a field inside the Assistant that wants Esc for its
  // own dismissal (the tier picker) still gets first refusal.
  useEffect(() => {
    if (!chatOpen) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setChat(false) }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [chatOpen, setChat])

  const popOut = useCallback(() => {
    const app = getAppById('assistant')
    if (app) void launchApp(app, { openWindow })
    setChat(false)
  }, [openWindow, setChat])

  if (!chatOpen) return null

  return (
    <aside
      aria-label="Assistant"
      // `vassist-panel` never had a stylesheet — no rule for that class exists
      // anywhere in src — so the aside itself was fully TRANSPARENT. The
      // Assistant inside paints its own background, but the chrome strip above
      // it did not, and the desktop (widgets, wallpaper, the clock) showed
      // straight through the top of the panel. The surface is declared here.
      style={{
        background: 'var(--bg-surface)',
        boxShadow: 'var(--shadow-xl)',
        borderLeft: narrow ? undefined : '1px solid var(--border-default)',
      }}
      className={`fixed z-40 flex flex-col overflow-hidden ${
        narrow
          ? 'inset-x-0 bottom-0 top-8 rounded-t-2xl'
          : 'right-0 top-8 bottom-0 w-[min(420px,42vw)]'
      }`}
    >
      {/* Panel chrome. Deliberately titleless — the Assistant renders its own
          identity header directly below, and repeating it would read as two
          apps stacked. No bottom border either: a rule here plus the
          Assistant's own header rule 36px lower read as two stacked bars. */}
      <div className="shrink-0 h-9 flex items-center justify-end gap-1 px-2">
        <button onClick={popOut} title="Open in a window" aria-label="Open assistant in a window" className="vshell-btn focus-primary w-7 h-7 flex items-center justify-center rounded-md">
          <svg viewBox="0 0 24 24" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
            <path d="M14 4h6v6M20 4l-8 8M18 14v4.5A1.5 1.5 0 0116.5 20h-11A1.5 1.5 0 014 18.5v-11A1.5 1.5 0 015.5 6H10" />
          </svg>
        </button>
        <button onClick={() => setChat(false)} title="Close assistant" aria-label="Close assistant" className="vshell-btn focus-primary w-7 h-7 flex items-center justify-center rounded-md">
          <svg viewBox="0 0 24 24" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
            <path d="M6 6l12 12M18 6L6 18" />
          </svg>
        </button>
      </div>
      <div className="flex-1 min-h-0">
        <Suspense fallback={<PanelFallback />}>
          <Assistant />
        </Suspense>
      </div>
    </aside>
  )
}
