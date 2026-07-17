/**
 * vulos-cloud — Two-Factor Authentication challenge page
 *
 * Flow:
 *   1. AuthProvider.login() returns {step:"totp_required"} → redirects here
 *      with partial-session cookie (5 min TTL) already set by the server.
 *   2. User enters 6-digit TOTP code (or toggles to recovery code input).
 *   3. Submit → POST /api/auth/totp/verify {code|recovery_code, remember_device}
 *   4. 204 success → AuthProvider.refresh() → full session → redirect to /app (or ?return=)
 *   5. 429 lockout → shows retry countdown timer.
 *   6. 401 invalid code → inline error, form stays live.
 *
 * Styles mirror the vc-auth-* class set from Login.jsx (same design contract).
 * AuthFormStyles is re-exported from Login.jsx and reused here.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { Section } from '../ui/index.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { useAuth } from '../auth/AuthProvider.jsx'
import { clientNavigate } from '../auth/nav.js'
import { AuthFormStyles } from './Login.jsx'

const TOTP_URL = '/api/auth/totp/verify'

function safeReturn() {
  try {
    const url = new URL(window.location.href)
    const raw = url.searchParams.get('return')
    if (raw && raw.startsWith('/') && !raw.startsWith('//')) return raw
  } catch { /* ignore */ }
  // Console home (App.jsx renders the dashboard shell at "/").
  return '/'
}

function Spinner({ size = 14 }) {
  return (
    <span
      aria-hidden="true"
      style={{
        display: 'inline-block',
        width: size, height: size,
        borderRadius: '50%',
        border: `2px solid color-mix(in srgb, currentColor 25%, transparent)`,
        borderTopColor: 'currentColor',
        animation: 'vcAuthSpin 0.7s linear infinite',
      }}
    />
  )
}

/* ── Lockout countdown ──────────────────────────────────────────────── */
// LockoutBanner is always mounted with a `key={retryAfter}` from the parent
// so it re-mounts (and re-initialises state) each time retryAfter changes,
// avoiding the need to call setState synchronously inside an effect.
function LockoutBanner({ retryAfter, onExpired }) {
  // Initialise from prop; state won't drift because the parent re-mounts us
  // (via key) whenever retryAfter changes.
  const [remaining, setRemaining] = useState(retryAfter)
  const intervalRef = useRef(null)
  // Stable ref so the interval callback can call the latest onExpired without
  // being re-captured in a closure.
  const onExpiredRef = useRef(onExpired)
  useEffect(() => { onExpiredRef.current = onExpired })

  useEffect(() => {
    if (retryAfter <= 0) {
      onExpiredRef.current()
      return
    }
    intervalRef.current = setInterval(() => {
      setRemaining(prev => {
        if (prev <= 1) {
          clearInterval(intervalRef.current)
          onExpiredRef.current()
          return 0
        }
        return prev - 1
      })
    }, 1000)
    return () => clearInterval(intervalRef.current)
  // retryAfter is baked into initial state; we only need to start the interval
  // once per mount (key handles the re-init).
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const mins  = String(Math.floor(remaining / 60)).padStart(2, '0')
  const secs  = String(remaining % 60).padStart(2, '0')

  return (
    <div
      role="alert"
      aria-live="assertive"
      className="vc-auth-alert"
      style={{ flexDirection: 'column', gap: 8 }}
    >
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
        <span className="vc-auth-alert-tag">Locked</span>
        <span className="vc-auth-alert-msg">
          Too many failed attempts. Try again in{' '}
          <span
            style={{ fontFamily: 'var(--font-mono)', fontWeight: 600, color: 'var(--warn)' }}
            aria-live="off"
          >
            {mins}:{secs}
          </span>.
        </span>
      </div>
    </div>
  )
}

/* ─── TwoFactor ──────────────────────────────────────────────────────── */
export default function TwoFactor() {
  const { user, loading: authLoading, refresh } = useAuth()

  const [useRecovery,    setUseRecovery]    = useState(false)
  const [code,           setCode]           = useState('')
  const [recoveryCode,   setRecoveryCode]   = useState('')
  const [rememberDevice, setRememberDevice] = useState(false)
  const [submitting,     setSubmitting]     = useState(false)
  const [submitError,    setSubmitError]    = useState(null)
  const [lockout,        setLockout]        = useState(null) // seconds | null

  const returnTo = useMemo(() => safeReturn(), [])

  // If already fully authenticated, skip the challenge.
  useEffect(() => {
    if (!authLoading && user) clientNavigate(returnTo)
  }, [authLoading, user, returnTo])

  const codeValue     = useRecovery ? recoveryCode : code
  const canSubmit     = codeValue.trim().length > 0 && !submitting && lockout === null

  function handleCodeChange(e) {
    if (submitError) setSubmitError(null)
    if (useRecovery) {
      setRecoveryCode(e.target.value)
    } else {
      // Strip non-digits, max 6
      const digits = e.target.value.replace(/\D/g, '').slice(0, 6)
      setCode(digits)
    }
  }

  function handleToggleMode(e) {
    e.preventDefault()
    setUseRecovery(v => !v)
    setCode('')
    setRecoveryCode('')
    setSubmitError(null)
  }

  async function handleSubmit(e) {
    e.preventDefault()
    if (!canSubmit) return
    setSubmitError(null)
    setSubmitting(true)

    try {
      const body = {
        remember_device: rememberDevice,
        ...(useRecovery
          ? { recovery_code: recoveryCode.trim() }
          : { code: code.trim() }),
      }

      const res = await fetch(TOTP_URL, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify(body),
      })

      if (res.status === 429) {
        let retryAfter = 60
        try {
          const data = await res.json()
          if (typeof data.retry_after === 'number' && data.retry_after > 0) {
            retryAfter = data.retry_after
          }
        } catch { /* fallback to 60s */ }
        setLockout(retryAfter)
        setSubmitting(false)
        return
      }

      if (!res.ok) {
        let msg = 'Invalid code. Please try again.'
        try {
          const data = await res.json()
          if (data && typeof data.error === 'string' && data.error) msg = data.error
        } catch { /* ignore */ }
        setSubmitError(msg)
        setSubmitting(false)
        return
      }

      // 204 — full session cookie has been set by the server.
      // Re-probe /api/auth/me to populate user state, then navigate.
      await refresh()
      clientNavigate(returnTo)
    } catch {
      setSubmitError('Network error. Please check your connection and try again.')
      setSubmitting(false)
    }
  }

  return (
    <Section slim>
      <AuthFormStyles />
      <style>{`
        .vc-2fa-digits {
          letter-spacing: 0.35em;
          text-align: center;
          font-size: 1.5rem;
          font-family: var(--font-mono);
          font-weight: 600;
        }
        .vc-2fa-digits::placeholder {
          letter-spacing: 0.1em;
          font-size: 0.9375rem;
          font-weight: 400;
        }
        .vc-auth-check-row {
          display: flex;
          align-items: center;
          gap: 10px;
          cursor: pointer;
          user-select: none;
        }
        .vc-auth-check-row input[type=checkbox] {
          width: 16px;
          height: 16px;
          accent-color: var(--accent);
          cursor: pointer;
          flex-shrink: 0;
        }
        .vc-auth-check-label {
          font-family: var(--font-mono);
          font-size: 0.8125rem;
          color: var(--text-secondary);
          line-height: 1.4;
        }
        .vc-auth-mode-toggle {
          background: none;
          border: none;
          padding: 0;
          font-family: var(--font-mono);
          font-size: 0.8125rem;
          color: var(--accent);
          cursor: pointer;
          text-decoration: underline;
          text-underline-offset: 2px;
          text-decoration-color: color-mix(in srgb, var(--accent) 40%, transparent);
          transition: color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
        }
        .vc-auth-mode-toggle:hover {
          color: var(--accent-hover);
          text-decoration-color: var(--accent-hover);
        }
        .vc-auth-mode-toggle:focus-visible {
          outline: none;
          box-shadow: var(--focus-ring);
          border-radius: var(--radius-sm, 6px);
        }
      `}</style>

      <div className="vc-auth-wrap">
        <div className="vc-auth-card vc-auth-reveal">

          {/* ── Header ───────────────────────────────────────── */}
          <div className="vc-auth-head">
            <a
              href="/"
              onClick={e => { e.preventDefault(); clientNavigate('/') }}
              className="vc-auth-brand"
              aria-label="Vulos — home"
            >
              <LogoMark size={36} tone="on-dark" />
              <span className="vc-auth-wordmark">
                vulos<span className="vc-auth-wordmark-suffix">.org</span>
              </span>
            </a>
            <p className="vc-auth-eyebrow">Two-Factor Authentication</p>
            <h1 className="vc-auth-title">Verify your identity</h1>
            <p className="vc-auth-subtitle">
              {useRecovery
                ? 'Enter one of your saved recovery codes to regain access.'
                : 'Enter the 6-digit code from your authenticator app.'}
            </p>
          </div>

          {/* ── Form ─────────────────────────────────────────── */}
          <form noValidate onSubmit={handleSubmit} className="vc-auth-form">

            {/* Lockout banner — key forces re-mount on each new lockout value */}
            {lockout !== null && (
              <LockoutBanner
                key={lockout}
                retryAfter={lockout}
                onExpired={() => setLockout(null)}
              />
            )}

            {/* Inline error (non-lockout) */}
            {submitError && lockout === null && (
              <div
                role="alert"
                id="tfa-error"
                className="vc-auth-alert"
                aria-live="assertive"
              >
                <span className="vc-auth-alert-tag">Error</span>
                <span className="vc-auth-alert-msg">{submitError}</span>
              </div>
            )}

            {/* Code input */}
            <div className="vc-auth-field">
              <div className="vc-auth-label-row">
                <label htmlFor="tfa-code" className="vc-auth-label">
                  {useRecovery ? 'Recovery code' : 'Authentication code'}
                </label>
              </div>
              {useRecovery ? (
                <input
                  id="tfa-code"
                  name="recovery_code"
                  type="text"
                  autoComplete="one-time-code"
                  spellCheck={false}
                  autoCapitalize="characters"
                  value={recoveryCode}
                  onChange={handleCodeChange}
                  placeholder="XXXX-XXXX-XXXX"
                  required
                  disabled={lockout !== null || submitting}
                  aria-invalid={submitError ? 'true' : 'false'}
                  aria-describedby={submitError ? 'tfa-error' : undefined}
                  className={`vc-auth-input${submitError ? ' has-error' : ''}`}
                  autoFocus
                />
              ) : (
                <input
                  id="tfa-code"
                  name="code"
                  type="text"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  pattern="[0-9]*"
                  maxLength={6}
                  spellCheck={false}
                  value={code}
                  onChange={handleCodeChange}
                  placeholder="000000"
                  required
                  disabled={lockout !== null || submitting}
                  aria-invalid={submitError ? 'true' : 'false'}
                  aria-describedby={submitError ? 'tfa-error' : undefined}
                  className={`vc-auth-input vc-2fa-digits${submitError ? ' has-error' : ''}`}
                  autoFocus
                />
              )}
            </div>

            {/* Remember-device checkbox */}
            <label className="vc-auth-check-row">
              <input
                type="checkbox"
                checked={rememberDevice}
                onChange={e => setRememberDevice(e.target.checked)}
                disabled={lockout !== null || submitting}
                aria-label="Trust this device for 30 days"
              />
              <span className="vc-auth-check-label">
                Remember this device for 30 days
              </span>
            </label>

            {/* Submit */}
            <button
              type="submit"
              className="vc-auth-submit"
              disabled={!canSubmit}
              aria-busy={submitting ? 'true' : 'false'}
            >
              {submitting
                ? (<><Spinner size={14} /><span>Verifying…</span></>)
                : 'Verify'}
            </button>

            {/* Toggle recovery / TOTP */}
            <div className="vc-auth-foot">
              <span className="vc-auth-foot-text">
                {useRecovery ? 'Have your authenticator? ' : 'Lost access to your app? '}
                <button
                  type="button"
                  className="vc-auth-mode-toggle"
                  onClick={handleToggleMode}
                >
                  {useRecovery ? 'Use authenticator code' : 'Use a recovery code'}
                </button>
              </span>
            </div>

          </form>
        </div>
      </div>
    </Section>
  )
}
