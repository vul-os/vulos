import { useState, useEffect, useRef, type FormEvent, type ChangeEvent } from 'react'
import { nativeBridge } from '../core/nativeBridge'

// NATIVE-BIO-01: biometric unlock is a LOCAL convenience gate, never a
// bypass — it re-submits the SAME pin through the SAME server-verified
// /api/auth/pin/validate call. Module-scoped so the cache survives this
// screen's mount/unmount across lock cycles within one runtime session.
let NATIVE_BIO_cachedPin: string | null = null
const NATIVE_BIO_PREF_KEY = 'vulos.biometric.unlock'
function NATIVE_BIO_optedIn() {
  try { return localStorage.getItem(NATIVE_BIO_PREF_KEY) === 'on' } catch { return false }
}

function useTime() {
  const [now, setNow] = useState(new Date())
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(id)
  }, [])
  return now
}

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// PinValidateResult — the narrowed shape of POST /api/auth/pin/validate's
// response. The raw JSON is untrusted network input; every field is
// typeof-checked here rather than cast, so a malformed/unexpected response
// degrades to "field absent" instead of the caller trusting a lie about its
// type (e.g. a non-boolean `valid` must NOT be treated as truthy — see below).
interface PinValidateResult {
  valid: boolean
  permanentLock: boolean
  locked: boolean
  attemptsLeft: number | undefined
}

function parsePinValidateResult(data: unknown): PinValidateResult {
  const d = isRecord(data) ? data : {}
  return {
    valid: d.valid === true,
    permanentLock: d.permanent_lock === true,
    locked: d.locked === true,
    attemptsLeft: typeof d.attempts_left === 'number' ? d.attempts_left : undefined,
  }
}

interface LockScreenProps {
  onUnlock: () => void
  userName?: string
}

export default function LockScreen({ onUnlock, userName }: LockScreenProps) {
  const [pin, setPin] = useState('')
  const [error, setError] = useState(false)
  // Human-readable status message (wrong PIN / lockout), announced to AT via the
  // aria-live region. Distinct from `error` (which drives the shake + red border)
  // so lockout copy can persist without re-triggering the shake on every render.
  const [message, setMessage] = useState('')
  const [locked, setLocked] = useState(false)
  const [showInput, setShowInput] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const errorTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const now = useTime()

  useEffect(() => () => clearTimeout(errorTimer.current), [])

  const time = now.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const date = now.toLocaleDateString([], { weekday: 'long', month: 'long', day: 'numeric' })

  useEffect(() => {
    const reveal = () => {
      setShowInput(true)
      setTimeout(() => inputRef.current?.focus(), 50)
    }
    const onKey = (e: globalThis.KeyboardEvent) => { if (!showInput && e.key !== 'Escape') reveal() }
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
  const flashError = (text: string, permanent = false) => {
    clearTimeout(errorTimer.current)
    setError(true)
    setMessage(text)
    setPin('')
    if (permanent) { setLocked(true); return }
    errorTimer.current = setTimeout(() => setError(false), 1500)
  }

  const [bioBusy, setBioBusy] = useState(false)

  const trySubmit = async (pinValue: string) => {
    if (locked) return
    try {
      const res = await fetch('/api/auth/pin/validate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pin: pinValue }),
      })
      const raw: unknown = await res.json().catch(() => ({}))
      const data = parsePinValidateResult(raw)
      if (data.valid) {
        // Server-verified. When no PIN is configured, the server's own
        // ValidatePIN contract (SEC-PIN-01) legitimately reports valid:true —
        // that is an intentional "quick lock, no credential" product decision,
        // not a client-side bypass. We never decide validity on the client.
        if (pinValue && NATIVE_BIO_optedIn()) NATIVE_BIO_cachedPin = pinValue
        onUnlock()
      } else if (data.permanentLock) {
        // Server has permanently locked this device after repeated failures —
        // re-auth (full sign-in) is required; a PIN retry cannot recover it.
        flashError('Locked after too many attempts. Sign in to unlock.', true)
      } else if (data.locked || res.status === 429) {
        // Temporary lockout — surface the honest "try again later" copy.
        flashError('Too many attempts. Try again shortly.')
      } else {
        // Wrong PIN. Surface remaining attempts when the server reports them.
        const left = data.attemptsLeft
        flashError(
          typeof left === 'number' && left >= 0
            ? `Incorrect PIN — ${left} attempt${left === 1 ? '' : 's'} left`
            : 'Incorrect PIN, try again',
        )
      }
    } catch {
      // Backend unreachable — fail CLOSED. We cannot verify a PIN (or the
      // "no PIN configured" passthrough) without the server, so never unlock
      // here regardless of what — if anything — was typed.
      flashError('Can’t reach the server. Try again.')
    }
  }

  const handleSubmit = (e: FormEvent) => { e.preventDefault(); trySubmit(pin) }

  // NATIVE-BIO-01: re-submit the cached PIN through the exact same
  // /api/auth/pin/validate call — biometrics gate reuse, they never
  // substitute for server verification.
  const handleBiometricUnlock = async () => {
    if (locked || bioBusy || !NATIVE_BIO_cachedPin) return
    setBioBusy(true)
    try {
      const ok = await nativeBridge.biometric.authenticate({ title: 'Unlock Vulos', allowDeviceCredential: true })
      if (ok) await trySubmit(NATIVE_BIO_cachedPin)
    } catch {
      // Cancelled or unenrolled — fall back to typing the PIN silently.
    } finally {
      setBioBusy(false)
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
              onChange={(e: ChangeEvent<HTMLInputElement>) => { setPin(e.target.value.replace(/[^0-9]/g, '')); if (error && !locked) setError(false) }}
              placeholder="PIN"
              inputMode="numeric"
              autoComplete="off"
              maxLength={8}
              disabled={locked}
              aria-label="Unlock PIN"
              aria-invalid={error}
              aria-describedby="lockscreen-error"
              style={error ? { borderColor: 'var(--status-danger)' } : undefined}
              className={`focus-primary w-40 text-center text-lg tracking-[0.5em] bg-neutral-900/60 border rounded-xl px-4 py-3 text-[var(--text-primary)] outline-none transition-colors disabled:opacity-50 disabled:cursor-not-allowed
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
            {message || ' '}
          </p>
          <button
            type="submit"
            disabled={locked}
            className="focus-primary rounded text-sm text-neutral-600 hover:text-neutral-400 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {locked ? 'Locked' : pin.length > 0 ? 'Unlock' : 'Enter without PIN'}
          </button>
          {nativeBridge.biometric.available && NATIVE_BIO_optedIn() && !!NATIVE_BIO_cachedPin && (
            <button
              type="button"
              onClick={handleBiometricUnlock}
              disabled={locked || bioBusy}
              className="focus-primary rounded text-xs text-neutral-700 hover:text-neutral-500 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {bioBusy ? 'Verifying…' : 'Unlock with biometrics'}
            </button>
          )}
        </form>
      ) : (
        <p className="text-sm text-neutral-700 animate-pulse">
          Tap or press any key
        </p>
      )}
    </div>
  )
}
