import { useState, useEffect, useRef } from 'react'
import { request } from './api.js'

// StepUpModal — the confirm-password prompt behind requireStepUp() (see
// ./stepup.js). Visual shape mirrors OfflineLockScreen.jsx / LockScreen.jsx
// (same password-prompt affordances, shake-on-error, aria-live status) but as
// a small centred dialog rather than a full-screen lock, since the rest of
// the OS stays visible and usable behind it.
//
// Pure presentation + the one network call — `onResolve`/`onReject` are the
// imperative promise's settle functions, supplied by requireStepUp().
export default function StepUpModal({ onResolve, onReject }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState(false)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const inputRef = useRef(null)
  const errorTimer = useRef(null)

  useEffect(() => { setTimeout(() => inputRef.current?.focus(), 50) }, [])
  useEffect(() => () => clearTimeout(errorTimer.current), [])

  const cancel = () => {
    if (busy) return
    onReject(Object.assign(new Error('Step-up cancelled'), { code: 'CANCELLED' }))
  }

  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape') cancel() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [busy])

  const flash = (text) => {
    clearTimeout(errorTimer.current)
    setError(true)
    setMessage(text)
    setPassword('')
    errorTimer.current = setTimeout(() => setError(false), 1500)
  }

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (busy || !password) return
    setBusy(true)
    setError(false)
    try {
      const data = await request('/auth/stepup/verify', {
        method: 'POST',
        body: JSON.stringify({ password }),
      })
      onResolve(data)
    } catch (err) {
      if (/too many/i.test(err?.message || '')) {
        flash('Too many attempts. Try again shortly.')
      } else {
        flash('Incorrect password, try again.')
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      className="fixed inset-0 z-[300] flex items-center justify-center bg-black/60 backdrop-blur-sm"
      role="presentation"
      onMouseDown={(e) => { if (e.target === e.currentTarget) cancel() }}
    >
      <form
        onSubmit={handleSubmit}
        role="dialog"
        aria-modal="true"
        aria-labelledby="stepup-title"
        className="relative w-full max-w-sm mx-4 rounded-2xl border border-neutral-800 bg-neutral-950 p-6 shadow-2xl flex flex-col items-center gap-4"
      >
        <div className="text-center">
          <h2 id="stepup-title" className="text-base font-medium text-[var(--text-primary)]">
            Confirm it&rsquo;s you
          </h2>
          <p className="mt-1 text-xs text-neutral-500">
            This action needs your password before it can continue.
          </p>
        </div>

        <input
          ref={inputRef}
          type="password"
          value={password}
          onChange={(e) => { setPassword(e.target.value); if (error) setError(false) }}
          placeholder="Password"
          autoComplete="current-password"
          disabled={busy}
          aria-label="Account password"
          aria-invalid={error}
          aria-describedby="stepup-error"
          style={error ? { borderColor: 'var(--status-danger)' } : undefined}
          className={`focus-primary w-full text-center text-base bg-neutral-900/60 border rounded-xl px-4 py-3 text-[var(--text-primary)] outline-none transition-colors disabled:opacity-50 disabled:cursor-not-allowed
            ${error ? 'animate-[shake_0.3s_ease-in-out]' : 'border-neutral-800 focus:border-neutral-600'}`}
        />

        <p
          id="stepup-error"
          role="alert"
          aria-live="assertive"
          className="text-xs min-h-[1rem] text-center leading-relaxed transition-colors"
          style={{ color: message ? 'var(--status-danger)' : 'transparent' }}
        >
          {message || ' '}
        </p>

        <div className="flex w-full items-center justify-center gap-3">
          <button
            type="button"
            onClick={cancel}
            disabled={busy}
            className="focus-primary rounded text-sm text-neutral-500 hover:text-neutral-300 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={busy || !password}
            className="focus-primary rounded-lg bg-neutral-100 px-4 py-2 text-sm font-medium text-neutral-900 hover:bg-white transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {busy ? 'Verifying…' : 'Confirm'}
          </button>
        </div>
      </form>
    </div>
  )
}
