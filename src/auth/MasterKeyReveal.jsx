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
    <div>
      <div className="mb-6">
        <div className="w-14 h-14 rounded-2xl bg-amber-500/15 flex items-center justify-center mb-4">
          <svg viewBox="0 0 24 24" className="w-7 h-7 text-amber-400" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
            <rect x="3" y="11" width="18" height="11" rx="2" />
            <path d="M7 11V7a5 5 0 0110 0v4" />
          </svg>
        </div>
        <h2 className="text-2xl font-light text-neutral-100">Save your recovery phrase</h2>
        <p className="text-sm text-neutral-500 mt-1 leading-relaxed">
          These 24 words are the master key to your account. Write them down and store them
          somewhere safe and offline. <span className="text-amber-400/90">We can never show them again,
          and without them a lost password means lost access.</span>
        </p>
      </div>

      {/* Phrase grid — blurred until explicitly revealed */}
      <div className="relative">
        <div className={`grid grid-cols-2 sm:grid-cols-3 gap-2 mb-3 transition-all ${revealed ? '' : 'blur-sm select-none pointer-events-none'}`}>
          {words.map((w, i) => (
            <div key={i} className="flex items-center gap-2 bg-neutral-900/60 border border-neutral-800/60 rounded-lg px-3 py-2">
              <span className="text-[10px] font-mono text-neutral-600 w-5 text-right">{i + 1}</span>
              <span className="text-sm font-medium text-neutral-200 font-mono tracking-tight">{w}</span>
            </div>
          ))}
        </div>
        {!revealed && (
          <button
            onClick={() => setRevealed(true)}
            className="absolute inset-0 flex items-center justify-center"
          >
            <span className="px-4 py-2 rounded-xl bg-neutral-800/90 border border-neutral-700 text-sm text-neutral-200 hover:bg-neutral-700 transition-colors">
              Tap to reveal
            </span>
          </button>
        )}
      </div>

      {revealed && (
        <button
          onClick={copy}
          className="w-full py-2.5 rounded-xl text-sm font-medium mb-4 bg-neutral-900/60 border border-neutral-800/60 text-neutral-300 hover:border-neutral-700 hover:text-white transition-all"
        >
          {copied ? 'Copied to clipboard' : 'Copy phrase'}
        </button>
      )}

      {!confirmingSkip ? (
        <>
          <label className="flex items-start gap-3 bg-neutral-900/40 border border-neutral-800/50 rounded-xl px-4 py-3 mb-3 cursor-pointer">
            <input
              type="checkbox"
              checked={savedChecked}
              onChange={(e) => setSavedChecked(e.target.checked)}
              disabled={!revealed}
              className="mt-0.5 accent-blue-500 w-4 h-4"
            />
            <span className="text-sm text-neutral-300">
              I have written down or securely stored my 24-word recovery phrase.
            </span>
          </label>

          <div className="flex items-center justify-between mt-2 pt-4 border-t border-neutral-800/30">
            <button
              onClick={() => setConfirmingSkip(true)}
              className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors"
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
        <div className="bg-red-900/20 border border-red-800/40 rounded-xl px-4 py-4 mt-2">
          <p className="text-sm text-red-300 mb-3 leading-relaxed">
            Without your recovery phrase, <span className="font-medium">a forgotten password cannot be
            recovered</span> and your account may be permanently inaccessible. Skip anyway?
          </p>
          <div className="flex items-center justify-between">
            <button
              onClick={() => setConfirmingSkip(false)}
              className="text-sm text-neutral-400 hover:text-neutral-200 transition-colors"
            >
              ← Go back
            </button>
            <button
              onClick={onSkip}
              className="px-4 py-2 rounded-xl text-sm font-medium bg-red-600/20 border border-red-500/40 text-red-300 hover:bg-red-600/30 transition-all"
            >
              Skip and accept the risk
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
