import { useEffect, useState } from 'react'
import { useShell } from '../providers/ShellProvider'
import { useFocusTrap } from '../shell/useFocusTrap'

const STORAGE_KEY = 'vulos-ai-firstrun-done'

const CAPABILITIES = [
  {
    icon: (
      <svg viewBox="0 0 20 20" className="w-5 h-5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
        <rect x="2" y="3" width="16" height="12" rx="2" />
        <path d="M6 7h8M6 10h5" />
      </svg>
    ),
    label: 'Build apps',
    desc: 'Describe what you want and get a working app in seconds.',
  },
  {
    icon: (
      <svg viewBox="0 0 20 20" className="w-5 h-5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
        <circle cx="10" cy="10" r="7" />
        <path d="M10 6v4l3 2" />
      </svg>
    ),
    label: 'Control the OS',
    desc: 'Open apps, change settings, or run system commands by asking.',
  },
  {
    icon: (
      <svg viewBox="0 0 20 20" className="w-5 h-5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
        <circle cx="8.5" cy="8.5" r="5.5" />
        <path d="M15.5 15.5l3 3" />
      </svg>
    ),
    label: 'Search files',
    desc: 'Find anything on your system — files, apps, or information.',
  },
]

export default function AIFirstRun() {
  const { setChat } = useShell()
  const [visible, setVisible] = useState(false)
  const [dismissed, setDismissed] = useState(false)
  // Trap focus inside the intro dialog while it is open + restore on close, so a
  // keyboard/AT user can't tab out into the desktop behind the modal overlay.
  const trapRef = useFocusTrap(visible && !dismissed)

  useEffect(() => {
    // Only show once — check persisted flag
    let done = false
    try { done = !!localStorage.getItem(STORAGE_KEY) } catch { done = false }
    if (done) return
    // Brief delay so the desktop fully renders before the overlay appears
    const timer = setTimeout(() => setVisible(true), 800)
    return () => clearTimeout(timer)
  }, [])

  // Esc dismisses (matches the shell's overlay-dismissal contract).
  useEffect(() => {
    if (!visible || dismissed) return
    const onKey = (e) => { if (e.key === 'Escape') { e.stopPropagation(); handleDismiss() } }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, dismissed])

  const handleOpen = () => {
    persist()
    setDismissed(true)
    setChat(true)
  }

  const handleDismiss = () => {
    persist()
    setDismissed(true)
  }

  function persist() {
    try { localStorage.setItem(STORAGE_KEY, '1') } catch (e) { void e }
  }

  if (!visible || dismissed) return null

  return (
    <div
      className="absolute inset-0 z-50 flex items-center justify-center bg-[var(--overlay)] backdrop-blur-sm"
      onClick={handleDismiss}
      aria-modal="true"
      role="dialog"
      aria-labelledby="ai-firstrun-title"
    >
      <div
        ref={trapRef}
        className="relative w-full max-w-sm mx-4 rounded-2xl bg-[var(--bg-surface)] border border-[var(--border-default)] shadow-2xl shadow-black/60 overflow-hidden animate-[fadeIn_0.12s_ease-out]"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Ambient glow — follows the operator's accent */}
        <div className="absolute -top-12 left-1/2 -translate-x-1/2 w-48 h-24 accent-bg opacity-[0.08] blur-3xl rounded-full pointer-events-none" />

        {/* Header */}
        <div className="px-6 pt-7 pb-4 text-center">
          <div className="inline-flex items-center justify-center w-12 h-12 rounded-2xl accent-bg-soft accent-border-soft border mb-4">
            <svg viewBox="0 0 24 24" className="w-6 h-6 accent-text" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
              <path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z" fill="currentColor" fillOpacity="0.12" />
              <path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z" />
            </svg>
          </div>
          <h2 id="ai-firstrun-title" className="text-lg font-semibold text-[var(--text-primary)]">Meet your AI assistant</h2>
          <p className="text-sm text-[var(--text-muted)] mt-1.5 leading-relaxed">
            Press <kbd className="px-1.5 py-0.5 text-[12px] rounded bg-[var(--bg-elevated)] border border-[var(--border-strong)] text-[var(--text-secondary)] font-mono">Ctrl+K</kbd> any time to open the chat.
          </p>
        </div>

        {/* Capabilities */}
        <ul className="px-5 pb-5 space-y-2.5">
          {CAPABILITIES.map((cap) => (
            <li key={cap.label} className="flex items-start gap-3 bg-[var(--bg-elevated)] rounded-xl px-4 py-3 border border-[var(--border-default)]">
              <span className="mt-0.5 accent-text shrink-0">{cap.icon}</span>
              <div>
                <div className="text-sm font-medium text-[var(--text-primary)]">{cap.label}</div>
                <div className="text-xs text-[var(--text-muted)] mt-0.5 leading-relaxed">{cap.desc}</div>
              </div>
            </li>
          ))}
        </ul>

        {/* Actions */}
        <div className="px-5 pb-6 flex gap-3">
          <button
            onClick={handleDismiss}
            className="flex-1 py-2.5 rounded-xl text-sm font-medium text-[var(--text-tertiary)] bg-[var(--bg-elevated)] hover:bg-[var(--bg-elevated)] transition-colors"
          >
            Later
          </button>
          <button
            onClick={handleOpen}
            autoFocus
            style={{ background: 'var(--accent)' }}
            className="flex-1 py-2.5 rounded-xl text-sm font-semibold text-white hover:brightness-110 transition-[filter]"
          >
            Open Chat
          </button>
        </div>
      </div>
    </div>
  )
}
