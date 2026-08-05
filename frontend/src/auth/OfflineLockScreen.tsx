import { useState, useEffect, useRef, type FormEvent, type ChangeEvent } from 'react'
import { nativeBridge } from '../core/nativeBridge'

// NATIVE-BIO-01: biometric unlock is a LOCAL convenience gate, never a bypass.
// It re-submits the SAME password through the SAME onUnlock() crypto unwrap —
// so it only ever "works" once a password has been typed correctly at least
// once this runtime session. Module-scoped (not component state) so it
// survives this screen unmounting on success and remounting on the next lock.
let NATIVE_BIO_cachedPassword: string | null = null
const NATIVE_BIO_PREF_KEY = 'vulos.biometric.unlock'
function NATIVE_BIO_optedIn() {
  try { return localStorage.getItem(NATIVE_BIO_PREF_KEY) === 'on' } catch { return false }
}

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

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// The rejection thrown by AuthProvider.unlockOffline (ultimately
// lib/offlineAuth.ts's taggedError / Object.assign(e, { attemptsLeft })) is
// caught here as `unknown` (strict `useUnknownInCatchVariables`). Narrow
// rather than cast: a thrown value that ISN'T the expected tagged error
// (e.g. a plain network Error) must fall through to the generic message
// exactly as `err?.code` / `err?.attemptsLeft` did before.
function offlineUnlockErrorCode(err: unknown): string | undefined {
  return isRecord(err) && typeof err.code === 'string' ? err.code : undefined
}
function offlineUnlockAttemptsLeft(err: unknown): number | undefined {
  return isRecord(err) && typeof err.attemptsLeft === 'number' ? err.attemptsLeft : undefined
}

interface OfflineIdentity {
  name?: string
}

interface OfflineLockScreenProps {
  onUnlock: (password: string) => Promise<boolean>
  identity?: OfflineIdentity | null
}

export default function OfflineLockScreen({ onUnlock, identity }: OfflineLockScreenProps) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState(false)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [wiped, setWiped] = useState(false)
  const [bioBusy, setBioBusy] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const errorTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const now = useClock()
  const canBiometric = nativeBridge.biometric.available && NATIVE_BIO_optedIn() && !!NATIVE_BIO_cachedPassword

  useEffect(() => { setTimeout(() => inputRef.current?.focus(), 50) }, [])
  useEffect(() => () => clearTimeout(errorTimer.current), [])

  const time = now.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const date = now.toLocaleDateString([], { weekday: 'long', month: 'long', day: 'numeric' })

  const flash = (text: string, permanent = false) => {
    clearTimeout(errorTimer.current)
    setError(true)
    setMessage(text)
    setPassword('')
    if (permanent) { setWiped(true); return }
    errorTimer.current = setTimeout(() => setError(false), 1500)
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || wiped || !password) return
    setBusy(true)
    setError(false)
    try {
      await onUnlock(password)
      // Success: AuthProvider flips to the offline session and this screen unmounts.
      if (NATIVE_BIO_optedIn()) NATIVE_BIO_cachedPassword = password
    } catch (err: unknown) {
      const code = offlineUnlockErrorCode(err)
      const attemptsLeft = offlineUnlockAttemptsLeft(err)
      if (code === 'WIPED') {
        flash('Too many attempts — offline data on this device was wiped. Sign in online to restore it.', true)
      } else if (code === 'NOT_ENROLLED') {
        flash('No offline access is set up on this device. Sign in online first.', true)
      } else if (code === 'CORRUPT') {
        flash('Your offline data is corrupted. Sign in online to restore it.', true)
      } else if (typeof attemptsLeft === 'number') {
        flash(`Incorrect password — ${attemptsLeft} attempt${attemptsLeft === 1 ? '' : 's'} left before offline data is wiped.`)
      } else {
        flash('Incorrect password, try again.')
      }
    } finally {
      setBusy(false)
    }
  }

  // NATIVE-BIO-01: re-submit the cached password through the exact same
  // onUnlock() unwrap — biometrics gate reuse, they never substitute for it.
  const handleBiometricUnlock = async () => {
    if (busy || wiped || bioBusy || !NATIVE_BIO_cachedPassword) return
    setBioBusy(true)
    try {
      const ok = await nativeBridge.biometric.authenticate({ title: 'Unlock Vulos', allowDeviceCredential: true })
      if (ok) {
        setBusy(true)
        try { await onUnlock(NATIVE_BIO_cachedPassword) }
        catch { NATIVE_BIO_cachedPassword = null } // stale/rotated — fall back to typing
        finally { setBusy(false) }
      }
    } catch {
      // Cancelled or unenrolled — fall back to the passphrase field silently.
    } finally {
      setBioBusy(false)
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
          onChange={(e: ChangeEvent<HTMLInputElement>) => { setPassword(e.target.value); if (error && !wiped) setError(false) }}
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
        {canBiometric && (
          <button
            type="button"
            onClick={handleBiometricUnlock}
            disabled={busy || bioBusy || wiped}
            className="focus-primary rounded text-xs text-neutral-600 hover:text-neutral-400 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {bioBusy ? 'Verifying…' : 'Unlock with biometrics'}
          </button>
        )}
      </form>
    </div>
  )
}
