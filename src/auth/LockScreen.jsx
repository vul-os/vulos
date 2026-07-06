import { useState, useEffect, useRef } from 'react'

function useTime() {
  const [now, setNow] = useState(new Date())
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(id)
  }, [])
  return now
}

export default function LockScreen({ onUnlock, userName }) {
  const [pin, setPin] = useState('')
  const [error, setError] = useState(false)
  // Human-readable status message (wrong PIN / lockout), announced to AT via the
  // aria-live region. Distinct from `error` (which drives the shake + red border)
  // so lockout copy can persist without re-triggering the shake on every render.
  const [message, setMessage] = useState('')
  const [locked, setLocked] = useState(false)
  const [showInput, setShowInput] = useState(false)
  const inputRef = useRef(null)
  const errorTimer = useRef(null)
  const now = useTime()

  useEffect(() => () => clearTimeout(errorTimer.current), [])

  const time = now.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const date = now.toLocaleDateString([], { weekday: 'long', month: 'long', day: 'numeric' })

  useEffect(() => {
    const reveal = () => {
      setShowInput(true)
      setTimeout(() => inputRef.current?.focus(), 50)
    }
    const onKey = (e) => { if (!showInput && e.key !== 'Escape') reveal() }
    const onPointer = () => { if (!showInput) reveal() }
    window.addEventListener('keydown', onKey)
    window.addEventListener('pointerdown', onPointer)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('pointerdown', onPointer)
    }
  }, [showInput])

  // Flash the error/shake state and announce a message. If `permanent` (server
  // lockout), the copy persists and the input is disabled; otherwise it clears
  // after a beat so the field is usable again.
  const flashError = (text, permanent = false) => {
    clearTimeout(errorTimer.current)
    setError(true)
    setMessage(text)
    setPin('')
    if (permanent) { setLocked(true); return }
    errorTimer.current = setTimeout(() => setError(false), 1500)
  }

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (locked) return
    try {
      const res = await fetch('/api/auth/pin/validate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pin }),
      })
      const data = await res.json().catch(() => ({}))
      if (data.valid) {
        onUnlock()
      } else if (!data.has_pin && pin.length === 0) {
        // No PIN set — just unlock
        onUnlock()
      } else if (data.permanent_lock) {
        // Server has permanently locked this device after repeated failures —
        // re-auth (full sign-in) is required; a PIN retry cannot recover it.
        flashError('Locked after too many attempts. Sign in to unlock.', true)
      } else if (data.locked || res.status === 429) {
        // Temporary lockout — surface the honest "try again later" copy.
        flashError('Too many attempts. Try again shortly.')
      } else {
        // Wrong PIN. Surface remaining attempts when the server reports them.
        const left = data.attempts_left
        flashError(
          typeof left === 'number' && left >= 0
            ? `Incorrect PIN — ${left} attempt${left === 1 ? '' : 's'} left`
            : 'Incorrect PIN, try again',
        )
      }
    } catch {
      // Backend unreachable — allow unlock with empty PIN
      if (pin.length === 0) onUnlock()
      else flashError('Can’t reach the server. Try again.')
    }
  }

  return (
    <div className="fixed inset-0 bg-neutral-950 flex flex-col items-center justify-center z-[200]">
      {/* Background */}
      <div className="absolute inset-0 pointer-events-none">
        <div className="absolute top-[25%] left-[35%] w-[500px] h-[500px] rounded-full bg-blue-600 opacity-[0.02] blur-[150px]" />
        <div className="absolute bottom-[25%] right-[25%] w-[400px] h-[400px] rounded-full bg-violet-600 opacity-[0.02] blur-[150px]" />
      </div>

      {/* Clock */}
      <div className="relative text-center mb-8">
        <div className="text-7xl font-extralight text-neutral-200 font-mono tracking-widest">
          {time}
        </div>
        <div className="text-lg text-neutral-500 mt-2 font-light">
          {date}
        </div>
      </div>

      {/* Unlock area */}
      {showInput ? (
        <form onSubmit={handleSubmit} className="relative flex flex-col items-center gap-4">
          {userName && <p className="text-sm text-neutral-500">{userName}</p>}
          <div className="flex items-center gap-2">
            <input
              ref={inputRef}
              type="password"
              value={pin}
              onChange={(e) => { setPin(e.target.value.replace(/[^0-9]/g, '')); if (error && !locked) setError(false) }}
              placeholder="PIN"
              inputMode="numeric"
              autoComplete="off"
              maxLength={8}
              disabled={locked}
              aria-label="Unlock PIN"
              aria-invalid={error}
              aria-describedby="lockscreen-error"
              style={error ? { borderColor: 'var(--status-danger)' } : undefined}
              className={`focus-primary w-40 text-center text-lg tracking-[0.5em] bg-neutral-900/60 border rounded-xl px-4 py-3 text-white outline-none transition-colors disabled:opacity-50 disabled:cursor-not-allowed
                ${error ? 'animate-[shake_0.3s_ease-in-out]' : 'border-neutral-800 focus:border-neutral-600'}`}
            />
          </div>
          {/* Live region — announced to AT on change, and shown so sighted users
              get the same wrong-PIN / lockout feedback. Always present so the
              announcement fires reliably rather than mounting/unmounting. */}
          <p
            id="lockscreen-error"
            role="alert"
            aria-live="assertive"
            className="text-xs h-4 text-center transition-colors"
            style={{ color: message ? 'var(--status-danger)' : 'transparent' }}
          >
            {message || ' '}
          </p>
          <button
            type="submit"
            disabled={locked}
            className="focus-primary rounded text-sm text-neutral-600 hover:text-neutral-400 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {locked ? 'Locked' : pin.length > 0 ? 'Unlock' : 'Enter without PIN'}
          </button>
        </form>
      ) : (
        <p className="text-sm text-neutral-700 animate-pulse">
          Tap or press any key
        </p>
      )}
    </div>
  )
}
