import { useState } from 'react'

// MasterKeyReveal — WAVE2-RECOVERY forced recovery-phrase screen (Proton-style).
//
// Shown ONCE, right after the account is created, before setup can complete. The
// 24-word phrase is the only way to recover the account (and, in wave 3, decrypt
// content) if the password is lost. The user must either explicitly confirm they
// saved it, or explicitly acknowledge the risk to skip. The phrase is held only in
// component state for display and is never persisted or sent anywhere by this
// component.
//
// Props:
//   phrase     — the 24-word space-separated recovery phrase (from register resp)
//   onConfirm  — called when the user confirms they saved it
//   onSkip     — called when the user explicitly skips (accepting the risk)
export default function MasterKeyReveal({ phrase, onConfirm, onSkip }) {
  const words = String(phrase || '').trim().split(/\s+/)
  const [revealed, setRevealed] = useState(false)
  const [copied, setCopied] = useState(false)
  const [savedChecked, setSavedChecked] = useState(false)
  const [confirmingSkip, setConfirmingSkip] = useState(false)

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(words.join(' '))
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch { /* clipboard may be unavailable; user can transcribe manually */ }
  }

  return (
    <div className="animate-[fadeIn_0.3s_ease-out]">
      <div className="mb-6">
        <div
          className="w-14 h-14 rounded-2xl flex items-center justify-center mb-4"
          style={{ background: 'var(--status-warning-soft)' }}
        >
          <svg viewBox="0 0 24 24" className="w-7 h-7 text-warning" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
            <rect x="3" y="11" width="18" height="11" rx="2" />
            <path d="M7 11V7a5 5 0 0110 0v4" />
          </svg>
        </div>
        <h2 className="text-2xl font-light" style={{ color: 'var(--text-primary)' }}>Save your recovery phrase</h2>
        <p className="text-sm mt-1.5 leading-relaxed" style={{ color: 'var(--text-muted)' }}>
          These 24 words are the master key to your account. Write them down and store them
          somewhere safe and offline. <span className="text-warning">We can never show them again,
          and without them a lost password means lost access.</span>
        </p>
      </div>

      {/* Phrase grid — blurred until explicitly revealed */}
      <div className="relative">
        <div className={`grid grid-cols-2 sm:grid-cols-3 gap-2 mb-3 transition-all duration-300 ${revealed ? '' : 'blur-sm select-none pointer-events-none'}`}>
          {words.map((w, i) => (
            <div
              key={i}
              className="flex items-center gap-2 rounded-lg px-3 py-2"
              style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border-default)' }}
            >
              <span className="text-[12px] font-mono w-5 text-right tabular-nums" style={{ color: 'var(--text-faint)' }}>{i + 1}</span>
              <span className="text-sm font-medium font-mono tracking-tight" style={{ color: 'var(--text-primary)' }}>{w}</span>
            </div>
          ))}
        </div>
        {!revealed && (
          <button
            onClick={() => setRevealed(true)}
            className="absolute inset-0 flex items-center justify-center"
          >
            <span
              className="px-5 py-2.5 rounded-xl text-sm font-medium elevate-md transition-all hover:scale-[1.03]"
              style={{ background: 'var(--bg-active)', border: '1px solid var(--border-strong)', color: 'var(--text-primary)' }}
            >
              Tap to reveal
            </span>
          </button>
        )}
      </div>

      {revealed && (
        <button
          onClick={copy}
          className="w-full py-2.5 rounded-xl text-sm font-medium mb-4 transition-all animate-[fadeIn_0.2s_ease-out]"
          style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-default)', color: copied ? 'var(--status-success)' : 'var(--text-secondary)' }}
        >
          {copied ? 'Copied to clipboard' : 'Copy phrase'}
        </button>
      )}

      {!confirmingSkip ? (
        <>
          <label
            className="flex items-start gap-3 rounded-xl px-4 py-3 mb-3 cursor-pointer transition-colors"
            style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-default)' }}
          >
            <input
              type="checkbox"
              checked={savedChecked}
              onChange={(e) => setSavedChecked(e.target.checked)}
              disabled={!revealed}
              className="mt-0.5 w-4 h-4"
              style={{ accentColor: 'var(--accent)' }}
            />
            <span className="text-sm" style={{ color: 'var(--text-secondary)' }}>
              I have written down or securely stored my 24-word recovery phrase.
            </span>
          </label>

          <div className="flex items-center justify-between mt-2 pt-4" style={{ borderTop: '1px solid var(--border-subtle)' }}>
            <button
              onClick={() => setConfirmingSkip(true)}
              className="text-sm transition-colors"
              style={{ color: 'var(--text-muted)' }}
            >
              Skip for now
            </button>
            <button
              onClick={onConfirm}
              disabled={!savedChecked}
              className="btn-primary disabled:opacity-40 disabled:cursor-not-allowed"
            >
              Continue →
            </button>
          </div>
        </>
      ) : (
        <div
          className="rounded-xl px-4 py-4 mt-2 animate-[fadeIn_0.2s_ease-out]"
          style={{ background: 'var(--status-danger-soft)', border: '1px solid color-mix(in srgb, var(--status-danger) 34%, transparent)' }}
        >
          <p className="text-sm mb-3 leading-relaxed text-danger">
            Without your recovery phrase, <span className="font-medium">a forgotten password cannot be
            recovered</span> and your account may be permanently inaccessible. Skip anyway?
          </p>
          <div className="flex items-center justify-between">
            <button
              onClick={() => setConfirmingSkip(false)}
              className="text-sm transition-colors"
              style={{ color: 'var(--text-secondary)' }}
            >
              ← Go back
            </button>
            <button
              onClick={onSkip}
              className="px-4 py-2 rounded-xl text-sm font-medium text-danger transition-all"
              style={{ background: 'var(--status-danger-soft)', border: '1px solid color-mix(in srgb, var(--status-danger) 42%, transparent)' }}
            >
              Skip and accept the risk
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
