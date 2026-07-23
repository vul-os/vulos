import { useState, useEffect, useRef } from 'react'

// OfflineLockScreen — the OS auth gate when the box is unreachable (OFFLINE-AUTH-01).
//
// Distinct from LockScreen (the online, server-verified PIN idle-lock): here the
// box can't be reached, so we unlock LOCALLY by unwrapping the cached master-key
// envelope with the account PASSWORD (fail-closed via AES-GCM — see
// lib/offlineAuth.js). A wrong password yields nothing; too many wipe the cache.
//
// `onUnlock(password)` is AuthProvider.unlockOffline: it resolves on success and
// rejects with a coded error (WRONG_PASSWORD carries attemptsLeft, WIPED, etc.).

function useClock() {
  const [now, setNow] = useState(new Date())
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(id)
  }, [])
  return now
}

export default function OfflineLockScreen({ onUnlock, identity }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState(false)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [wiped, setWiped] = useState(false)
  const inputRef = useRef(null)
  const errorTimer = useRef(null)
  const now = useClock()

  useEffect(() => { setTimeout(() => inputRef.current?.focus(), 50) }, [])
  useEffect(() => () => clearTimeout(errorTimer.current), [])

  const time = now.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const date = now.toLocaleDateString([], { weekday: 'long', month: 'long', day: 'numeric' })

  const flash = (text, permanent = false) => {
    clearTimeout(errorTimer.current)
    setError(true)
    setMessage(text)
    setPassword('')
    if (permanent) { setWiped(true); return }
    errorTimer.current = setTimeout(() => setError(false), 1500)
  }

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (busy || wiped || !password) return
    setBusy(true)
    setError(false)
    try {
      await onUnlock(password)
      // Success: AuthProvider flips to the offline session and this screen unmounts.
    } catch (err) {
      if (err?.code === 'WIPED') {
        flash('Too many attempts — offline data on this device was wiped. Sign in online to restore it.', true)
      } else if (err?.code === 'NOT_ENROLLED') {
        flash('No offline access is set up on this device. Sign in online first.', true)
      } else if (err?.code === 'CORRUPT') {
        flash('Your offline data is corrupted. Sign in online to restore it.', true)
      } else if (typeof err?.attemptsLeft === 'number') {
        flash(`Incorrect password — ${err.attemptsLeft} attempt${err.attemptsLeft === 1 ? '' : 's'} left before offline data is wiped.`)
      } else {
        flash('Incorrect password, try again.')
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 bg-neutral-950 flex flex-col items-center justify-center z-[200]">
      <div className="absolute inset-0 pointer-events-none">
        <div className="absolute top-[25%] left-[35%] w-[500px] h-[500px] rounded-full bg-blue-600 opacity-[0.02] blur-[150px]" />
        <div className="absolute bottom-[25%] right-[25%] w-[400px] h-[400px] rounded-full bg-violet-600 opacity-[0.02] blur-[150px]" />
      </div>

      <div className="relative text-center mb-8">
        <div className="text-7xl font-extralight text-neutral-200 font-mono tracking-widest">{time}</div>
        <div className="text-lg text-neutral-500 mt-2 font-light">{date}</div>
      </div>

      {/* Honest offline indicator — you are NOT talking to your box right now. */}
      <div className="relative mb-4 flex items-center gap-2 text-xs text-amber-400/90">
        <span className="w-1.5 h-1.5 rounded-full bg-amber-400/80" />
        Offline — unlock to view your cached data
      </div>

      <form onSubmit={handleSubmit} className="relative flex flex-col items-center gap-4">
        {identity?.name && <p className="text-sm text-neutral-500">{identity.name}</p>}
        <input
          ref={inputRef}
          type="password"
          value={password}
          onChange={(e) => { setPassword(e.target.value); if (error && !wiped) setError(false) }}
          placeholder="Password"
          autoComplete="current-password"
          disabled={wiped || busy}
          aria-label="Account password"
          aria-invalid={error}
          aria-describedby="offline-lock-error"
          style={error ? { borderColor: 'var(--status-danger)' } : undefined}
          className={`focus-primary w-64 text-center text-base bg-neutral-900/60 border rounded-xl px-4 py-3 text-[var(--text-primary)] outline-none transition-colors disabled:opacity-50 disabled:cursor-not-allowed
            ${error ? 'animate-[shake_0.3s_ease-in-out]' : 'border-neutral-800 focus:border-neutral-600'}`}
        />
        <p
          id="offline-lock-error"
          role="alert"
          aria-live="assertive"
          className="text-xs max-w-xs text-center leading-relaxed transition-colors"
          style={{ color: message ? 'var(--status-danger)' : 'transparent' }}
        >
          {message || ' '}
        </p>
        <button
          type="submit"
          disabled={wiped || busy || !password}
          className="focus-primary rounded text-sm text-neutral-500 hover:text-neutral-300 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {busy ? 'Unlocking…' : wiped ? 'Offline data wiped' : 'Unlock'}
        </button>
      </form>
    </div>
  )
}
