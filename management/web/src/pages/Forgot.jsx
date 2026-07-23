/**
 * vulos-cloud — Reset your Vulos password
 *
 * Accepts a Vulos handle (bare, e.g. "alice") or a full @<VULOS_DOMAIN> login address.
 * Auto-appends the identity domain when the user types a bare handle.
 * POSTs {handle} to POST /api/auth/forgot-password.
 *
 * The server always returns the same success message whether or not the account
 * exists (enumeration safety). External-email input is rejected 400 client-side
 * before submit.
 *
 * THE CIRCLE: the reset link is delivered to the user's own Vulos inbox — the
 * one they may be locked out of. So this page also offers the escape hatch that
 * needs no inbox at all: a recovery code (handed out at signup) resets the
 * password directly via POST /api/auth/password/recovery-code. The code is
 * burned on use and every session is revoked.
 *
 * Branch B: if the user has lost the codes too, a quiet link to
 * /account-recovery (the reviewed 14-day flow) is surfaced.
 */

import { useState } from 'react'
import { Section } from '../ui/index.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { AuthFormStyles } from './Login.jsx'
import { VULOS_DOMAIN } from '../config.js'
import { powFetch } from '../auth/pow.js'
import { clientNavigate } from '../auth/nav.js'

// Accepts bare handle (no @) or handle@<VULOS_DOMAIN> only. The domain part is
// derived from the configured base domain so dev builds accept the dev domain.
const HANDLE_RE = /^[a-z0-9._-]+$/i
const VULOS_RE = new RegExp(
  '^[a-z0-9._-]+@' + VULOS_DOMAIN.replace(/[.]/g, '\\.') + '$',
  'i',
)


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

// Normalise the input: strip whitespace, lowercase, strip the identity domain.
function normaliseHandle(raw) {
  const s = raw.trim().toLowerCase()
  if (s.endsWith('@' + VULOS_DOMAIN)) return s.slice(0, s.length - VULOS_DOMAIN.length - 1)
  return s
}

function validateHandle(raw) {
  const s = raw.trim()
  if (!s) return 'Enter your Vulos handle or email.'
  // If the user typed an "@", it must end with the identity domain.
  if (s.includes('@')) {
    if (!VULOS_RE.test(s)) return `Only @${VULOS_DOMAIN} addresses are accepted for recovery.`
    return null
  }
  // Bare handle.
  if (!HANDLE_RE.test(s)) return 'Handle may only contain letters, numbers, dots, hyphens, underscores.'
  return null
}

const MIN_PWD = 12

/**
 * RecoveryCodeForm — reset the password with a recovery code.
 *
 * No mailbox, no session, no TOTP device: the only thing needed is one of the
 * codes handed out at signup. The server answers 401 for both a wrong code and
 * an unknown handle, so this form never reveals which accounts exist.
 */
function RecoveryCodeForm({ onBack }) {
  const [handle,     setHandle]     = useState('')
  const [code,       setCode]       = useState('')
  const [password,   setPassword]   = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [done,       setDone]       = useState(false)
  const [error,      setError]      = useState('')

  const canSubmit =
    !validateHandle(handle) &&
    code.trim().length >= 8 &&
    password.length >= MIN_PWD &&
    !submitting

  async function handleSubmit(e) {
    e.preventDefault()
    if (!canSubmit) return
    setSubmitting(true)
    setError('')
    try {
      const res = await powFetch('/api/auth/password/recovery-code', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({
          handle: normaliseHandle(handle),
          recovery_code: code.trim(),
          new_password: password,
        }),
      })
      if (res.status === 401) {
        setError('That recovery code is not valid, or has already been used.')
        return
      }
      if (res.status === 429) {
        setError('Too many attempts. Wait a few minutes and try again.')
        return
      }
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        setError(body.message || body.error || 'Could not reset your password. Try again.')
        return
      }
      setDone(true)
    } catch {
      setError('Network error. Check your connection and try again.')
    } finally {
      setSubmitting(false)
    }
  }

  if (done) {
    return (
      <div className="vc-auth-form">
        <div className="vc-auth-ok" role="status" aria-live="polite">
          <span className="vc-auth-ok-tag">Done</span>
          <span className="vc-auth-ok-msg">
            Your password is reset and that code is now used up. Every other session
            has been signed out.
          </span>
        </div>
        <button type="button" className="vc-auth-submit" onClick={() => clientNavigate('/login')}>
          Sign in
        </button>
      </div>
    )
  }

  return (
    <form noValidate onSubmit={handleSubmit} className="vc-auth-form">
      {error && (
        <div role="alert" className="vc-auth-alert" aria-live="assertive">
          <span className="vc-auth-alert-tag">Error</span>
          <span className="vc-auth-alert-msg">{error}</span>
        </div>
      )}

      <div className="vc-auth-field">
        <div className="vc-auth-label-row">
          <label htmlFor="rc-handle" className="vc-auth-label">Vulos handle</label>
        </div>
        <input
          id="rc-handle"
          type="text"
          autoComplete="username"
          spellCheck={false}
          autoCapitalize="none"
          value={handle}
          onChange={e => setHandle(e.target.value)}
          placeholder={`alice  or  alice@${VULOS_DOMAIN}`}
          className="vc-auth-input"
          required
          autoFocus
        />
      </div>

      <div className="vc-auth-field">
        <div className="vc-auth-label-row">
          <label htmlFor="rc-code" className="vc-auth-label">Recovery code</label>
        </div>
        <input
          id="rc-code"
          type="text"
          autoComplete="one-time-code"
          spellCheck={false}
          value={code}
          onChange={e => setCode(e.target.value)}
          placeholder="One of the codes you saved at signup"
          className="vc-auth-input"
          required
        />
        <p className="vc-auth-fieldhint">
          Each code works once. Using one signs out every other session.
        </p>
      </div>

      <div className="vc-auth-field">
        <div className="vc-auth-label-row">
          <label htmlFor="rc-password" className="vc-auth-label">New password</label>
        </div>
        <input
          id="rc-password"
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={e => setPassword(e.target.value)}
          placeholder={`At least ${MIN_PWD} characters`}
          minLength={MIN_PWD}
          className="vc-auth-input"
          required
        />
      </div>

      <button
        type="submit"
        className="vc-auth-submit"
        disabled={!canSubmit}
        aria-busy={submitting ? 'true' : 'false'}
      >
        {submitting ? (<><Spinner size={14} /><span>Resetting…</span></>) : 'Reset password'}
      </button>

      <div className="vc-auth-foot">
        <span className="vc-auth-foot-text">
          <a
            href="#link"
            onClick={e => { e.preventDefault(); onBack() }}
            className="vc-auth-foot-link"
          >
            Email me a reset link instead
          </a>
        </span>
      </div>
    </form>
  )
}

export default function Forgot() {
  const [handle,      setHandle]      = useState('')
  const [touched,     setTouched]     = useState(false)
  const [submitting,  setSubmitting]  = useState(false)
  const [submitted,   setSubmitted]   = useState(false)
  const [useCode,     setUseCode]     = useState(false)

  const handleError = touched ? validateHandle(handle) : null
  const canSubmit = !validateHandle(handle) && !submitting

  async function handleSubmit(e) {
    e.preventDefault()
    setTouched(true)
    if (!canSubmit) return
    setSubmitting(true)
    try {
      // We do not branch on the response — server always pretend-200.
      // powFetch solves the CaptchaGate hashcash (403 otherwise).
      await powFetch('/api/auth/forgot-password', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ handle: handle.trim() }),
      })
    } catch {
      /* swallow — see header comment */
    }
    setSubmitting(false)
    setSubmitted(true)
  }

  const displayEmail = normaliseHandle(handle) + '@' + VULOS_DOMAIN

  return (
    <Section slim>
      <AuthFormStyles />
      <div className="vc-auth-wrap">
        <div className="vc-auth-card vc-auth-reveal">
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
            <p className="vc-auth-eyebrow">Password reset</p>
            <h1 className="vc-auth-title">
              {useCode
                ? 'Use a recovery code'
                : submitted ? 'Check your Vulos inbox' : 'Reset your Vulos password'}
            </h1>
            <p className="vc-auth-subtitle">
              {useCode
                ? 'No inbox, no session, no 2FA device — just one of the codes you saved when you created the account.'
                : submitted
                  ? 'We’ve sent a recovery link to your Vulos inbox. It expires in 15 minutes.'
                  : `Enter your Vulos handle or @${VULOS_DOMAIN} address. The link is delivered to your Vulos inbox.`}
            </p>
          </div>

          {useCode ? (
            <RecoveryCodeForm onBack={() => setUseCode(false)} />
          ) : submitted ? (
            <div className="vc-auth-form">
              <div className="vc-auth-ok" role="status" aria-live="polite">
                <span className="vc-auth-ok-tag">Sent</span>
                <span className="vc-auth-ok-msg">
                  If <strong>{displayEmail}</strong> is a Vulos account, a reset link is waiting in your Vulos inbox.
                  Open it from any device where you’re still signed in.
                </span>
              </div>

              <button
                type="button"
                className="vc-auth-submit"
                onClick={() => clientNavigate('/login')}
              >
                Back to sign in
              </button>

              <div className="vc-auth-foot">
                <span className="vc-auth-foot-text">
                  Wrong handle?{' '}
                  <a
                    href="#retry"
                    onClick={e => {
                      e.preventDefault()
                      setSubmitted(false)
                      setHandle('')
                      setTouched(false)
                    }}
                    className="vc-auth-foot-link"
                  >
                    Try another
                  </a>
                </span>
              </div>

              {/* Branch B: fully locked out — can't read Vulos inbox */}
              <div className="vc-auth-foot" style={{ marginTop: '0.5rem', borderTop: '1px solid rgba(255,255,255,0.08)', paddingTop: '0.75rem' }}>
                <span className="vc-auth-foot-text" style={{ fontSize: '0.78rem', opacity: 0.65 }}>
                  Can&apos;t access your Vulos inbox either?{' '}
                  <a
                    href="/account-recovery"
                    onClick={e => { e.preventDefault(); clientNavigate('/account-recovery') }}
                    className="vc-auth-foot-link"
                  >
                    Fully locked out? Use account recovery (14-day process)
                  </a>
                </span>
              </div>
            </div>
          ) : (
            <form noValidate onSubmit={handleSubmit} className="vc-auth-form">
              <div className="vc-auth-field">
                <div className="vc-auth-label-row">
                  <label htmlFor="forgot-handle" className="vc-auth-label">Vulos handle or email</label>
                </div>
                <input
                  id="forgot-handle"
                  name="handle"
                  type="text"
                  autoComplete="username"
                  inputMode="email"
                  spellCheck={false}
                  autoCapitalize="none"
                  value={handle}
                  onChange={e => setHandle(e.target.value)}
                  onBlur={() => setTouched(true)}
                  placeholder={`alice  or  alice@${VULOS_DOMAIN}`}
                  required
                  aria-invalid={handleError ? 'true' : 'false'}
                  aria-describedby={handleError ? 'forgot-handle-err' : 'forgot-handle-hint'}
                  className={`vc-auth-input${handleError ? ' has-error' : ''}`}
                  autoFocus
                />
                <p id="forgot-handle-hint" className="vc-auth-fieldhint">
                  Vulos only supports in-network recovery — we don&apos;t send to external email.
                </p>
                {handleError && (
                  <p id="forgot-handle-err" className="vc-auth-fielderr">{handleError}</p>
                )}
              </div>

              <button
                type="submit"
                className="vc-auth-submit"
                disabled={!canSubmit}
                aria-busy={submitting ? 'true' : 'false'}
              >
                {submitting ? (<><Spinner size={14} /><span>Sending link…</span></>) : 'Send reset link'}
              </button>

              <div className="vc-auth-foot">
                <span className="vc-auth-foot-text">
                  Can&apos;t reach your Vulos inbox?{' '}
                  <a
                    href="#recovery-code"
                    onClick={e => { e.preventDefault(); setUseCode(true) }}
                    className="vc-auth-foot-link"
                  >
                    Use a recovery code instead
                  </a>
                </span>
              </div>

              <div className="vc-auth-foot">
                <span className="vc-auth-foot-text">
                  Remembered it?{' '}
                  <a
                    href="/login"
                    onClick={e => { e.preventDefault(); clientNavigate('/login') }}
                    className="vc-auth-foot-link"
                  >
                    Back to sign in
                  </a>
                </span>
              </div>

              {/* Branch B: lost the codes too — the reviewed 14-day flow */}
              <div className="vc-auth-foot" style={{ marginTop: '0.25rem' }}>
                <span className="vc-auth-foot-text" style={{ fontSize: '0.78rem', opacity: 0.6 }}>
                  <a
                    href="/account-recovery"
                    onClick={e => { e.preventDefault(); clientNavigate('/account-recovery') }}
                    className="vc-auth-foot-link"
                  >
                    Fully locked out? Use account recovery (14-day process)
                  </a>
                </span>
              </div>
            </form>
          )}
        </div>
      </div>
    </Section>
  )
}
