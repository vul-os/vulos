// CLOGIN-01: OS login page — Local/Cloud toggle.
//
// Local path:   username + password → POST /api/auth/login (unchanged).
// Cloud path:   email + password → (optional 2FA code) → POST /api/auth/cloudlogin
//               The backend verifies the cloud-signed token locally; no live
//               cloud dependency.
//
// LOGINISO-01: passkey (WebAuthn) login promoted from re-auth gate to full
// login.  Shown as the primary CTA in LocalForm with "use password instead"
// fallback.  PasskeyRegisterButton lives in Settings/Security.
//
// LOGINISO-02: QR / phone-approval kiosk login — shown when the device is in
// kiosk/streamed mode (data-device-profile="kiosk") or the user clicks the QR
// icon on the login screen.
//
// The component is a drop-in replacement for LoginScreen.jsx used when a
// Login.jsx-aware router is in place.  It accepts the same onSuccess callback
// as LoginScreen and calls it after a successful login.

import { useState, useEffect } from 'react'
import FullscreenHint from './FullscreenHint'
import ThemeToggle from '../core/ThemeToggle'
import { useAuth } from './AuthProvider'
import { PasskeyLoginButton } from './PasskeyButton'
import QRLoginPanel from './QRLogin'

// ─── Main export ─────────────────────────────────────────────────────────────

export default function Login({ defaultMode }) {
  const { checkAuth } = useAuth()

  // Detect cloud enrollment on mount — enrolled devices default to cloud mode.
  const [mode, setMode] = useState(defaultMode || 'local')
  // Start as checked when defaultMode is explicitly provided; otherwise wait for
  // the enrollment fetch so there's no flash of the wrong mode.
  const [enrolledChecked, setEnrolledChecked] = useState(!!defaultMode)

  useEffect(() => {
    if (defaultMode) return // defaultMode is already applied via useState initial value
    let cancelled = false
    fetch('/api/auth/cloud/status')
      .then(r => r.ok ? r.json() : null)
      .then(data => {
        if (cancelled) return
        if (data?.enrolled) setMode('cloud')
        setEnrolledChecked(true)
      })
      .catch(() => {
        if (!cancelled) setEnrolledChecked(true)
      })
    return () => { cancelled = true }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Check if this is first-time setup (no users yet — show registration).
  const [hasUsers, setHasUsers] = useState(null)
  useEffect(() => {
    fetch('/api/auth/status')
      .then(r => r.json())
      .then(data => setHasUsers(data.has_users))
      .catch(() => setHasUsers(true)) // safe default: assume users exist
  }, [])

  // LOGINISO-02: detect kiosk/streamed mode — show QR login as an extra option.
  const isKiosk = typeof document !== 'undefined' &&
    document.documentElement.dataset.deviceProfile === 'kiosk'

  // QR panel state — user can toggle it from the Local form.
  const [showQR, setShowQR] = useState(isKiosk)

  if (!enrolledChecked || hasUsers === null) {
    return (
      <div className="fixed inset-0 bg-neutral-950 flex items-center justify-center">
        <span className="text-neutral-600 text-sm">Loading...</span>
      </div>
    )
  }

  return (
    <div className="fixed inset-0 bg-neutral-950 flex flex-col items-center justify-center px-6 overflow-hidden">
      {/* Ambient glow */}
      <div className="absolute inset-0 pointer-events-none">
        <div className="absolute top-[30%] left-[40%] w-[400px] h-[400px] rounded-full bg-blue-600 opacity-[0.03] blur-[150px]" />
        <div className="absolute bottom-[30%] right-[30%] w-[300px] h-[300px] rounded-full bg-violet-600 opacity-[0.03] blur-[150px]" />
      </div>

      {/* Logo */}
      <div className="relative mb-8 flex flex-col items-center">
        <img src="/icon-96.png" alt="Vulos" className="w-16 h-16 mb-3" />
        <h1 className="text-3xl font-light text-neutral-200 tracking-wider">vulos</h1>
        <p className="text-xs text-neutral-700 mt-0.5">open</p>
      </div>

      {/* LOGINISO-02: QR panel overrides the normal form on kiosk mode */}
      {showQR && hasUsers !== false
        ? (
          <div className="relative w-full max-w-sm">
            <QRLoginPanel
              onSuccess={checkAuth}
              onCancel={() => setShowQR(false)}
            />
          </div>
        )
        : (
          <>
            {/* Mode toggle (only shown when users already exist) */}
            {hasUsers !== false && (
              <div className="relative mb-6 flex rounded-xl bg-neutral-900/70 border border-neutral-800/60 p-1 gap-1">
                <ModeTab
                  active={mode === 'local'}
                  onClick={() => setMode('local')}
                  label="Local"
                  icon={<LocalIcon />}
                />
                <ModeTab
                  active={mode === 'cloud'}
                  onClick={() => setMode('cloud')}
                  label="Cloud account"
                  icon={<CloudIcon />}
                />
                {/* LOGINISO-02: QR button (always available, not just kiosk) */}
                <ModeTab
                  active={false}
                  onClick={() => setShowQR(true)}
                  label="QR"
                  icon={<QRModeIcon />}
                />
              </div>
            )}

            {/* Form area */}
            <div className="relative w-full max-w-sm">
              {mode === 'local' || hasUsers === false
                ? <LocalForm isSetup={hasUsers === false} onSuccess={checkAuth} />
                : <CloudForm onSuccess={checkAuth} />
              }
            </div>
          </>
        )
      }

      {/* Top-right controls */}
      <div className="absolute top-4 right-4 flex items-center gap-2">
        <ThemeToggle />
      </div>

      {/* Fullscreen hint */}
      <div className="absolute bottom-4">
        <FullscreenHint />
      </div>
    </div>
  )
}

// ─── Mode tab button ──────────────────────────────────────────────────────────

function ModeTab({ active, onClick, label, icon }) {
  return (
    <button
      onClick={onClick}
      className={`flex items-center gap-1.5 px-4 py-2 rounded-lg text-sm font-medium transition-all
        ${active
          ? 'bg-neutral-800 text-neutral-100 shadow-sm'
          : 'text-neutral-500 hover:text-neutral-300'}`}
    >
      <span className={`w-4 h-4 ${active ? 'text-blue-400' : 'text-neutral-600'}`}>{icon}</span>
      {label}
    </button>
  )
}

// ─── Local form (username + password + passkey) ───────────────────────────────

function LocalForm({ isSetup, onSuccess }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  // LOGINISO-01: show passkey-first UX for returning users; fallback to password.
  const [usePassword, setUsePassword] = useState(isSetup)

  const handleSubmit = async (e) => {
    e.preventDefault()
    setError('')
    setLoading(true)

    const endpoint = isSetup ? '/api/auth/register' : '/api/auth/login'
    const body = isSetup
      ? { username, password, display_name: displayName || username }
      : { username, password }

    try {
      const res = await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      const data = await res.json()
      if (res.ok) {
        // WAVE2-RECOVERY: unwrap the per-user master key CLIENT-SIDE from the
        // password-wrapped envelope and hold it in memory for the session. Best-
        // effort for login UX (legacy accounts have no master key); the unwrap
        // itself is fail-closed and never persists the key.
        try {
          const { unlockMasterKeyForSession } = await import('../lib/masterKey.js')
          await unlockMasterKeyForSession(password)
        } catch (err) {
          console.warn('[masterkey] client-side unlock skipped:', err?.message || err)
        }
        await onSuccess()
      } else {
        setError(data.error || 'Login failed')
      }
    } catch {
      setError('Could not reach server')
    } finally {
      setLoading(false)
    }
  }

  // LOGINISO-01: passkey-first layout for returning users.
  if (!isSetup && !usePassword) {
    return (
      <div className="space-y-4">
        <h2 className="text-lg text-neutral-300 text-center mb-2">Sign in</h2>

        <div>
          <label className="block text-xs text-neutral-500 mb-1">Username</label>
          <input
            type="text"
            value={username}
            onChange={e => { setUsername(e.target.value); setError('') }}
            placeholder="Username"
            autoFocus
            autoComplete="username"
            className="input"
          />
        </div>

        {error && <p className="text-sm text-red-400 text-center">{error}</p>}

        {/* Primary CTA: passkey */}
        <PasskeyLoginButton
          username={username}
          onSuccess={onSuccess}
          onError={msg => setError(msg)}
        />

        {/* Fallback: password */}
        <button
          type="button"
          onClick={() => { setUsePassword(true); setError('') }}
          className="w-full text-sm text-neutral-600 hover:text-neutral-400 transition-colors text-center"
        >
          Use password instead
        </button>
      </div>
    )
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <h2 className="text-lg text-neutral-300 text-center mb-2">
        {isSetup ? 'Create your account' : 'Sign in'}
      </h2>

      {isSetup && (
        <div>
          <label className="block text-xs text-neutral-500 mb-1">Display Name</label>
          <input
            type="text"
            value={displayName}
            onChange={e => setDisplayName(e.target.value)}
            placeholder="Your name"
            className="input"
          />
        </div>
      )}

      <div>
        <label className="block text-xs text-neutral-500 mb-1">Username</label>
        <input
          type="text"
          value={username}
          onChange={e => setUsername(e.target.value)}
          placeholder="Username"
          required
          autoFocus={isSetup}
          autoComplete="username"
          className="input"
        />
      </div>

      <div>
        <label className="block text-xs text-neutral-500 mb-1">Password</label>
        <input
          type="password"
          value={password}
          onChange={e => setPassword(e.target.value)}
          placeholder="Password"
          required
          autoComplete={isSetup ? 'new-password' : 'current-password'}
          className="input"
        />
      </div>

      {error && <p className="text-sm text-red-400 text-center">{error}</p>}

      <button type="submit" disabled={loading} className="btn-primary w-full py-3 flex items-center justify-center gap-2">
        {loading && <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />}
        {isSetup ? 'Create Account' : 'Sign In'}
      </button>

      {/* LOGINISO-01: offer passkey switch for returning users */}
      {!isSetup && (
        <button
          type="button"
          onClick={() => { setUsePassword(false); setError('') }}
          className="w-full text-sm text-neutral-600 hover:text-neutral-400 transition-colors text-center"
        >
          Use passkey instead
        </button>
      )}
    </form>
  )
}

// ─── Cloud form (email + password + optional 2FA) ────────────────────────────

// Cloud login flow:
//   Step 1: email + password  →  POST /api/auth/cloudlogin
//           - Backend contacts cloud, gets a signed token, verifies it locally.
//           - If 2FA required, backend returns { require_2fa: true, challenge_id }.
//   Step 2 (2FA): TOTP code   →  POST /api/auth/cloudlogin/2fa
//           - Backend exchanges code+challenge_id for the final signed token.

function CloudForm({ onSuccess }) {
  const [step, setStep] = useState('credentials') // 'credentials' | '2fa'
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [challengeId, setChallengeId] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const handleCredentials = async (e) => {
    e.preventDefault()
    setError('')
    setLoading(true)

    try {
      const res = await fetch('/api/auth/cloudlogin', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email, password }),
      })
      const data = await res.json()

      if (!res.ok) {
        setError(data.error || 'Cloud login failed')
        return
      }

      if (data.require_2fa) {
        // Backend needs a 2FA code to complete login.
        setChallengeId(data.challenge_id || '')
        setStep('2fa')
        return
      }

      // Success — session cookie already set by the backend.
      await onSuccess()
    } catch {
      setError('Could not reach server')
    } finally {
      setLoading(false)
    }
  }

  const handle2FA = async (e) => {
    e.preventDefault()
    setError('')
    setLoading(true)

    try {
      const res = await fetch('/api/auth/cloudlogin/2fa', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code: code.replace(/\s/g, ''), challenge_id: challengeId }),
      })
      const data = await res.json()

      if (!res.ok) {
        setError(data.error || '2FA verification failed')
        return
      }

      await onSuccess()
    } catch {
      setError('Could not reach server')
    } finally {
      setLoading(false)
    }
  }

  if (step === '2fa') {
    return (
      <form onSubmit={handle2FA} className="space-y-5">
        <div className="text-center mb-2">
          <div className="w-12 h-12 rounded-2xl bg-blue-600/15 flex items-center justify-center mx-auto mb-3">
            <svg viewBox="0 0 24 24" className="w-6 h-6 text-blue-400" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
              <rect x="5" y="11" width="14" height="10" rx="2" />
              <path d="M8 11V7a4 4 0 018 0v4" />
              <circle cx="12" cy="16" r="1.5" fill="currentColor" stroke="none" />
            </svg>
          </div>
          <h2 className="text-lg text-neutral-200 font-medium">Two-factor authentication</h2>
          <p className="text-sm text-neutral-500 mt-1">Enter the 6-digit code from your authenticator app</p>
        </div>

        <div>
          <input
            type="text"
            inputMode="numeric"
            pattern="[0-9 ]*"
            maxLength={7}
            value={code}
            onChange={e => { setCode(e.target.value.replace(/[^0-9 ]/g, '')); setError('') }}
            placeholder="000 000"
            autoFocus
            autoComplete="one-time-code"
            className="input text-center text-xl tracking-[0.3em] py-3"
          />
        </div>

        {error && <p className="text-sm text-red-400 text-center">{error}</p>}

        <button type="submit" disabled={loading || code.replace(/\s/g, '').length < 6}
          className="btn-primary w-full py-3 flex items-center justify-center gap-2 disabled:opacity-50">
          {loading && <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />}
          Verify
        </button>

        <button
          type="button"
          onClick={() => { setStep('credentials'); setCode(''); setError('') }}
          className="w-full text-sm text-neutral-600 hover:text-neutral-400 transition-colors text-center"
        >
          ← Back to sign-in
        </button>
      </form>
    )
  }

  return (
    <form onSubmit={handleCredentials} className="space-y-4">
      <h2 className="text-lg text-neutral-300 text-center mb-2">Sign in with Cloud account</h2>

      <div>
        <label className="block text-xs text-neutral-500 mb-1">Email</label>
        <input
          type="email"
          value={email}
          onChange={e => { setEmail(e.target.value); setError('') }}
          placeholder="you@example.com"
          required
          autoFocus
          autoComplete="email"
          className="input"
        />
      </div>

      <div>
        <label className="block text-xs text-neutral-500 mb-1">Password</label>
        <input
          type="password"
          value={password}
          onChange={e => { setPassword(e.target.value); setError('') }}
          placeholder="Cloud account password"
          required
          autoComplete="current-password"
          className="input"
        />
      </div>

      {error && <p className="text-sm text-red-400 text-center">{error}</p>}

      <button type="submit" disabled={loading}
        className="btn-primary w-full py-3 flex items-center justify-center gap-2">
        {loading && <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />}
        Continue
      </button>

      <p className="text-center text-xs text-neutral-600">
        Token validated locally — your password is never stored on this device.
      </p>
    </form>
  )
}

// ─── Icons ────────────────────────────────────────────────────────────────────

function LocalIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" className="w-full h-full">
      <circle cx="8" cy="5.5" r="2.5" />
      <path d="M2.5 13c0-3 2.5-5 5.5-5s5.5 2 5.5 5" />
    </svg>
  )
}

function CloudIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" className="w-full h-full">
      <path d="M12.5 9.5a3 3 0 00-.5-5.9A4.5 4.5 0 003.5 6a2.5 2.5 0 00.5 5h8.5z" />
    </svg>
  )
}

// LOGINISO-02: QR mode tab icon.
function QRModeIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" className="w-full h-full">
      <rect x="2" y="2" width="5" height="5" rx="0.5" />
      <rect x="9" y="2" width="5" height="5" rx="0.5" />
      <rect x="2" y="9" width="5" height="5" rx="0.5" />
      <rect x="9.5" y="9.5" width="2" height="2" />
      <path d="M12 9.5v2M9.5 12h2M12 12v2" />
    </svg>
  )
}
