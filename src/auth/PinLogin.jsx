// CLOGIN-06: Device-local PIN login screen.
//
// Shows on subsequent logins once a PIN has been set by the user (in Settings).
// Enforces the lockout rules enforced server-side:
//   - 5 wrong PINs → 15-min lockout (server tells us; we reflect it).
//   - 3 lockouts → permanent lock → must switch to full re-auth.
//
// The PIN never leaves the device; the server validates it locally and returns
// an OS session cookie on success.

import { useState, useEffect, useRef } from 'react'

// ─── PinLogin ─────────────────────────────────────────────────────────────────

/**
 * PinLogin — standalone PIN unlock screen.
 *
 * Props:
 *   onSuccess()         — called after a successful PIN unlock (session cookie set).
 *   onFallback()        — called when the user clicks "Use password instead" or when
 *                          the PIN is permanently locked.
 *   userName            — optional display name shown above the PIN input.
 */
export default function PinLogin({ onSuccess, onFallback, userName }) {
  const [pin, setPin] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [lockout, setLockout] = useState(null)   // { locked, permanent, lockedUntil, attemptsLeft }
  const [shaking, setShaking] = useState(false)
  const inputRef = useRef(null)

  // Load initial lockout status on mount.
  useEffect(() => {
    fetch('/api/auth/pin/status')
      .then(r => r.ok ? r.json() : null)
      .then(data => {
        if (data) setLockout(data)
      })
      .catch(() => {})
    inputRef.current?.focus()
  }, [])

  // Shake animation helper.
  const shake = () => {
    setShaking(true)
    setTimeout(() => setShaking(false), 500)
  }

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!pin || loading) return

    setError('')
    setLoading(true)
    try {
      const res = await fetch('/api/auth/pin/unlock', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pin }),
      })
      const data = await res.json()

      if (res.ok) {
        onSuccess()
        return
      }

      // Update lockout state from response.
      if (data.lockout) {
        setLockout(data.lockout)
      }

      shake()
      setPin('')

      if (data.error?.includes('permanently locked')) {
        setError('Too many attempts. Use your password to unlock.')
      } else if (data.error?.includes('locked out')) {
        const until = data.lockout?.locked_until
          ? formatLockoutTime(data.lockout.locked_until)
          : '15 minutes'
        setError(`Locked out — try again ${until}`)
      } else {
        const left = data.lockout?.attempts_left
        if (left !== undefined && left > 0) {
          setError(`Wrong PIN — ${left} attempt${left === 1 ? '' : 's'} left`)
        } else {
          setError('Wrong PIN')
        }
      }
    } catch {
      setError('Could not reach server')
      shake()
      setPin('')
    } finally {
      setLoading(false)
    }
  }

  const isPermanentlyLocked = lockout?.permanent_lock
  const isTemporarilyLocked = lockout?.locked && !lockout?.permanent_lock

  // Digits-only input handler.
  const handlePinChange = (e) => {
    const v = e.target.value.replace(/[^0-9]/g, '').slice(0, 8)
    setPin(v)
    if (error) setError('')
  }

  return (
    <div className="fixed inset-0 bg-neutral-950 flex flex-col items-center justify-center z-[200] px-6">
      {/* Ambient glow */}
      <div className="absolute inset-0 pointer-events-none">
        <div className="absolute top-[25%] left-[35%] w-[500px] h-[500px] rounded-full bg-blue-600 opacity-[0.02] blur-[150px]" />
        <div className="absolute bottom-[25%] right-[25%] w-[400px] h-[400px] rounded-full bg-violet-600 opacity-[0.02] blur-[150px]" />
      </div>

      {/* Lock icon */}
      <div className="relative mb-6">
        <div className={`w-16 h-16 rounded-2xl flex items-center justify-center mb-2
          ${isPermanentlyLocked
            ? 'bg-red-900/30 border border-red-800/50'
            : 'bg-neutral-900/60 border border-neutral-800/50'}`}>
          <LockIcon locked={isPermanentlyLocked || isTemporarilyLocked} />
        </div>
      </div>

      {/* User name */}
      {userName && (
        <p className="relative text-sm text-neutral-400 mb-1">{userName}</p>
      )}

      <h2 className="relative text-lg font-light text-neutral-200 mb-6">
        {isPermanentlyLocked ? 'Device locked' : 'Enter PIN'}
      </h2>

      {/* PIN form */}
      {!isPermanentlyLocked && (
        <form onSubmit={handleSubmit} className="relative flex flex-col items-center gap-4 w-full max-w-xs">
          {/* PIN dots display */}
          <PinDots length={pin.length} max={8} shaking={shaking} />

          {/* Hidden text input captures keyboard */}
          <input
            ref={inputRef}
            type="password"
            inputMode="numeric"
            pattern="[0-9]*"
            value={pin}
            onChange={handlePinChange}
            maxLength={8}
            disabled={loading || isTemporarilyLocked}
            autoFocus
            autoComplete="off"
            className="sr-only"
            aria-label="PIN"
          />

          {/* Numeric keypad */}
          <NumPad
            onDigit={(d) => {
              if (pin.length < 8) setPin(p => p + d)
            }}
            onDelete={() => setPin(p => p.slice(0, -1))}
            onSubmit={() => {
              if (pin.length >= 4) handleSubmit({ preventDefault: () => {} })
            }}
            disabled={loading || isTemporarilyLocked}
          />

          {/* Error / lockout message */}
          {error && (
            <p className="text-sm text-red-400 text-center mt-1">{error}</p>
          )}

          {isTemporarilyLocked && !error && (
            <LockoutCountdown until={lockout?.locked_until} />
          )}
        </form>
      )}

      {/* Permanent lock — must use password */}
      {isPermanentlyLocked && (
        <div className="relative text-center">
          <p className="text-sm text-neutral-400 mb-4 max-w-xs">
            Too many wrong PINs. Sign in with your password to reset the PIN.
          </p>
        </div>
      )}

      {/* Fallback link */}
      <button
        type="button"
        onClick={onFallback}
        className="relative mt-6 text-xs text-neutral-600 hover:text-neutral-400 transition-colors"
      >
        Use password instead
      </button>
    </div>
  )
}

// ─── PIN dots indicator ───────────────────────────────────────────────────────

function PinDots({ length, max, shaking }) {
  const dots = Array.from({ length: max }, (_, i) => i < length)
  return (
    <div className={`flex gap-2.5 py-2 ${shaking ? 'animate-[shake_0.4s_ease-in-out]' : ''}`}>
      {dots.map((filled, i) => (
        <div
          key={i}
          className={`w-3 h-3 rounded-full transition-all duration-150
            ${filled ? 'bg-blue-400 scale-110' : 'bg-neutral-700'}`}
        />
      ))}
    </div>
  )
}

// ─── Numeric keypad ───────────────────────────────────────────────────────────

function NumPad({ onDigit, onDelete, onSubmit, disabled }) {
  const rows = [
    ['1', '2', '3'],
    ['4', '5', '6'],
    ['7', '8', '9'],
    ['ok', '0', 'del'],
  ]

  return (
    <div className="grid grid-cols-3 gap-2 w-full max-w-[240px]">
      {rows.flat().map((key, i) => {
        if (key === 'ok') {
          return (
            <button
              key={i}
              type="button"
              onClick={onSubmit}
              disabled={disabled}
              className="h-14 rounded-2xl bg-blue-600/20 text-blue-400 hover:bg-blue-600/30 hover:text-blue-300
                active:scale-95 transition-all disabled:opacity-30 flex items-center justify-center text-xs font-medium"
              aria-label="Submit PIN"
            >
              OK
            </button>
          )
        }
        if (key === 'del') {
          return (
            <button
              key={i}
              type="button"
              onClick={onDelete}
              disabled={disabled}
              className="h-14 rounded-2xl bg-neutral-800/60 text-neutral-400 hover:bg-neutral-700/60 hover:text-neutral-200
                active:scale-95 transition-all disabled:opacity-30 flex items-center justify-center"
              aria-label="Delete"
            >
              <DeleteIcon />
            </button>
          )
        }
        return (
          <button
            key={i}
            type="button"
            onClick={() => {
              onDigit(key)
            }}
            disabled={disabled}
            className="h-14 rounded-2xl bg-neutral-800/60 text-neutral-200 text-xl font-light
              hover:bg-neutral-700/60 active:scale-95 transition-all disabled:opacity-30"
          >
            {key}
          </button>
        )
      })}
    </div>
  )
}

// ─── Lockout countdown ────────────────────────────────────────────────────────

function LockoutCountdown({ until }) {
  const [remaining, setRemaining] = useState('')

  useEffect(() => {
    if (!until) return
    const target = new Date(until)
    const update = () => {
      const diff = Math.max(0, target - Date.now())
      if (diff === 0) {
        setRemaining('Try now')
        return
      }
      const mins = Math.floor(diff / 60000)
      const secs = Math.floor((diff % 60000) / 1000)
      setRemaining(`${mins}:${String(secs).padStart(2, '0')}`)
    }
    update()
    const id = setInterval(update, 1000)
    return () => clearInterval(id)
  }, [until])

  return (
    <p className="text-sm text-amber-400 text-center">
      Locked — {remaining ? `try again in ${remaining}` : 'try again later'}
    </p>
  )
}

// ─── helpers ──────────────────────────────────────────────────────────────────

function formatLockoutTime(isoString) {
  try {
    const d = new Date(isoString)
    return `at ${d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
  } catch {
    return 'soon'
  }
}

// ─── Icons ────────────────────────────────────────────────────────────────────

function LockIcon({ locked }) {
  return (
    <svg viewBox="0 0 24 24" className={`w-8 h-8 ${locked ? 'text-red-400' : 'text-blue-400'}`}
      fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <rect x="5" y="11" width="14" height="10" rx="2" />
      {locked
        ? <path d="M8 11V7a4 4 0 018 0v4" />
        : <path d="M8 11V7a4 4 0 018 0" />}
      <circle cx="12" cy="16" r="1.5" fill="currentColor" stroke="none" />
    </svg>
  )
}

function DeleteIcon() {
  return (
    <svg viewBox="0 0 24 24" className="w-5 h-5" fill="none" stroke="currentColor"
      strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <path d="M21 4H8l-7 8 7 8h13a2 2 0 002-2V6a2 2 0 00-2-2z" />
      <line x1="18" y1="9" x2="13" y2="14" />
      <line x1="13" y1="9" x2="18" y2="14" />
    </svg>
  )
}
