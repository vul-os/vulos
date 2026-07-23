/**
 * vulos-cloud — Set new Vulos password
 *
 * Reads `?token=…` from the URL (the link delivered to the user's Vulos inbox).
 *
 * Flow:
 *   1. On mount, GET /api/auth/reset?token=<x> to validate the token and learn
 *      whether TOTP is enabled for the account.
 *   2. Present: TOTP code field (required) + new-password + confirm.
 *   3. POST /api/auth/reset {token, totp, new_password}.
 *
 * Outcomes:
 *   - 204 → flash "Password reset — please sign in" → /login
 *   - 410 → "This reset link has expired or already been used."
 *   - 401 → "Invalid or missing TOTP code."
 *   - 400/422 → inline error from server (e.g. weak password)
 *   - missing token → friendly empty-state with CTA to /forgot
 *
 * Branch B callout: if the user can't complete the TOTP step (lost TOTP),
 * they must use /account-recovery (14-day flow).
 */

import { useEffect, useMemo, useState } from 'react'
import { Section } from '../ui/index.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { AuthFormStyles } from './Login.jsx'
import { clientNavigate } from '../auth/nav.js'

const MIN_PWD = 12

function scorePassword(pw) {
  if (!pw) return 0
  let s = 0
  if (pw.length >= 8)  s += 1
  if (pw.length >= 12) s += 1
  if (/[A-Z]/.test(pw) && /[a-z]/.test(pw)) s += 1
  if (/\d/.test(pw) && /[^A-Za-z0-9]/.test(pw)) s += 1
  if (pw.length >= 16) s = Math.max(s, 4)
  return Math.min(s, 4)
}
const STRENGTH_LABEL = ['', 'Weak', 'Okay', 'Good', 'Strong']
const STRENGTH_CLASS = ['', 'weak', 'okay', 'good', 'strong']

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

async function readJSON(res) {
  const text = await res.text()
  if (!text) return null
  try { return JSON.parse(text) } catch { return null }
}

export default function Reset() {
  const { token } = useMemo(() => {
    try {
      const url = new URL(window.location.href)
      return { token: url.searchParams.get('token') || '' }
    } catch {
      return { token: '' }
    }
  }, [])

  // Token validation state (fetched on mount).
  const [validating,   setValidating]   = useState(!!token)
  const [tokenValid,   setTokenValid]   = useState(false)
  const [tokenExpired, setTokenExpired] = useState(false)

  const [totp,         setTotp]         = useState('')
  const [password,     setPassword]     = useState('')
  const [confirm,      setConfirm]      = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [touched,      setTouched]      = useState({ totp: false, password: false, confirm: false })
  const [submitting,   setSubmitting]   = useState(false)
  const [submitError,  setSubmitError]  = useState(null)
  const [expired,      setExpired]      = useState(false)

  useEffect(() => {
    if (!token) return
    fetch('/api/auth/reset?token=' + encodeURIComponent(token), { credentials: 'include' })
      .then(async res => {
        if (res.status === 410) { setTokenExpired(true); return }
        if (!res.ok) { setTokenExpired(true); return }
        setTokenValid(true)
      })
      .catch(() => setTokenExpired(true))
      .finally(() => setValidating(false))
  }, [token])

  const score = scorePassword(password)

  const totpError = touched.totp && totp.replace(/\s/g, '').length === 0
    ? 'Enter your 6-digit authenticator code.'
    : null
  const passwordError = touched.password && password.length > 0 && password.length < MIN_PWD
    ? `Use at least ${MIN_PWD} characters.`
    : null
  const confirmError = touched.confirm && confirm.length > 0 && confirm !== password
    ? "Passwords don’t match."
    : null

  const canSubmit =
    !!token &&
    tokenValid &&
    totp.replace(/\s/g, '').length >= 6 &&
    password.length >= MIN_PWD &&
    confirm === password &&
    !submitting

  async function handleSubmit(e) {
    e.preventDefault()
    setTouched({ totp: true, password: true, confirm: true })
    if (!canSubmit) return
    setSubmitError(null)
    setSubmitting(true)
    try {
      const res = await fetch('/api/auth/reset', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ token, totp: totp.replace(/\s/g, ''), new_password: password }),
      })
      if (res.status === 204) {
        clientNavigate('/login?flash=reset-ok')
        return
      }
      if (res.status === 410) {
        setExpired(true)
        setSubmitError('This reset link has expired or already been used.')
        setSubmitting(false)
        return
      }
      const body = await readJSON(res)
      const msg = (body && (body.error || body.message))
        || (res.status === 401
          ? 'Invalid TOTP code. Check your authenticator app and try again.'
          : res.status >= 500
            ? 'Something went wrong on our end. Please try again.'
            : "Couldn't reset your password.")
      setSubmitError(msg)
      setSubmitting(false)
    } catch (err) {
      setSubmitError(err && err.message ? err.message : 'Network error.')
      setSubmitting(false)
    }
  }

  // ── No token in URL ───────────────────────────────────────────────────────
  if (!token) {
    return (
      <Section slim>
        <AuthFormStyles />
        <div className="vc-auth-wrap">
          <div className="vc-auth-card vc-auth-reveal">
            <div className="vc-auth-head">
              <a href="/" onClick={e => { e.preventDefault(); clientNavigate('/') }} className="vc-auth-brand" aria-label="Vulos — home">
                <LogoMark size={36} tone="on-dark" />
                <span className="vc-auth-wordmark">vulos<span className="vc-auth-wordmark-suffix">.org</span></span>
              </a>
              <p className="vc-auth-eyebrow">Password reset</p>
              <h1 className="vc-auth-title">Reset link missing</h1>
              <p className="vc-auth-subtitle">
                This page needs a reset token in its URL. Open the link from your Vulos inbox
                or request a new one.
              </p>
            </div>
            <div className="vc-auth-form">
              <button type="button" className="vc-auth-submit" onClick={() => clientNavigate('/forgot')}>
                Request a new link
              </button>
              <div className="vc-auth-foot">
                <span className="vc-auth-foot-text">Or{' '}
                  <a href="/login" onClick={e => { e.preventDefault(); clientNavigate('/login') }} className="vc-auth-foot-link">back to sign in</a>
                </span>
              </div>
            </div>
          </div>
        </div>
      </Section>
    )
  }

  // ── Validating token (fetching) ───────────────────────────────────────────
  if (validating) {
    return (
      <Section slim>
        <AuthFormStyles />
        <div className="vc-auth-wrap">
          <div className="vc-auth-card vc-auth-reveal">
            <div className="vc-auth-head">
              <a href="/" onClick={e => { e.preventDefault(); clientNavigate('/') }} className="vc-auth-brand" aria-label="Vulos — home">
                <LogoMark size={36} tone="on-dark" />
                <span className="vc-auth-wordmark">vulos<span className="vc-auth-wordmark-suffix">.org</span></span>
              </a>
              <p className="vc-auth-eyebrow">Password reset</p>
              <h1 className="vc-auth-title">Validating link…</h1>
            </div>
            <div className="vc-auth-form" style={{ display: 'flex', justifyContent: 'center', padding: '1rem 0' }}>
              <Spinner size={24} />
            </div>
          </div>
        </div>
      </Section>
    )
  }

  // ── Token expired / invalid ───────────────────────────────────────────────
  if (tokenExpired) {
    return (
      <Section slim>
        <AuthFormStyles />
        <div className="vc-auth-wrap">
          <div className="vc-auth-card vc-auth-reveal">
            <div className="vc-auth-head">
              <a href="/" onClick={e => { e.preventDefault(); clientNavigate('/') }} className="vc-auth-brand" aria-label="Vulos — home">
                <LogoMark size={36} tone="on-dark" />
                <span className="vc-auth-wordmark">vulos<span className="vc-auth-wordmark-suffix">.org</span></span>
              </a>
              <p className="vc-auth-eyebrow">Password reset</p>
              <h1 className="vc-auth-title">Link expired</h1>
              <p className="vc-auth-subtitle">
                This reset link has expired or already been used. Reset links are valid for 15 minutes.
              </p>
            </div>
            <div className="vc-auth-form">
              <button type="button" className="vc-auth-submit" onClick={() => clientNavigate('/forgot')}>
                Request a new link
              </button>
              <div className="vc-auth-foot">
                <span className="vc-auth-foot-text" style={{ fontSize: '0.78rem', opacity: 0.6 }}>
                  <a href="/account-recovery" onClick={e => { e.preventDefault(); clientNavigate('/account-recovery') }} className="vc-auth-foot-link">
                    Fully locked out? Use account recovery (14-day process)
                  </a>
                </span>
              </div>
            </div>
          </div>
        </div>
      </Section>
    )
  }

  // ── Reset form ────────────────────────────────────────────────────────────
  return (
    <Section slim>
      <AuthFormStyles />
      <div className="vc-auth-wrap">
        <div className="vc-auth-card vc-auth-reveal">
          <div className="vc-auth-head">
            <a href="/" onClick={e => { e.preventDefault(); clientNavigate('/') }} className="vc-auth-brand" aria-label="Vulos — home">
              <LogoMark size={36} tone="on-dark" />
              <span className="vc-auth-wordmark">vulos<span className="vc-auth-wordmark-suffix">.org</span></span>
            </a>
            <p className="vc-auth-eyebrow">Password reset</p>
            <h1 className="vc-auth-title">Set a new Vulos password</h1>
            <p className="vc-auth-subtitle">
              Enter your authenticator code, then choose a new password.
              All active sessions will be signed out.
            </p>
          </div>

          <form noValidate onSubmit={handleSubmit} className="vc-auth-form">
            {submitError && (
              <div role="alert" id="reset-error" className="vc-auth-alert" aria-live="assertive">
                <span className="vc-auth-alert-tag">{expired ? 'Expired' : 'Error'}</span>
                <span className="vc-auth-alert-msg">
                  {submitError}
                  {expired && (
                    <>{' '}<a href="/forgot" onClick={e => { e.preventDefault(); clientNavigate('/forgot') }} className="vc-auth-foot-link">Request a new link</a></>
                  )}
                </span>
              </div>
            )}

            {/* TOTP field */}
            <div className="vc-auth-field">
              <div className="vc-auth-label-row">
                <label htmlFor="reset-totp" className="vc-auth-label">Authenticator code</label>
              </div>
              <input
                id="reset-totp"
                name="totp"
                type="text"
                autoComplete="one-time-code"
                inputMode="numeric"
                pattern="[0-9 ]*"
                maxLength={7}
                value={totp}
                onChange={e => { setTotp(e.target.value); if (submitError) setSubmitError(null) }}
                onBlur={() => setTouched(t => ({ ...t, totp: true }))}
                placeholder="000000"
                required
                disabled={expired}
                aria-invalid={totpError ? 'true' : 'false'}
                aria-describedby={totpError ? 'reset-totp-err' : 'reset-totp-hint'}
                className={`vc-auth-input${totpError ? ' has-error' : ''}`}
                autoFocus
              />
              <p id="reset-totp-hint" className="vc-auth-fieldhint">
                6-digit code from your authenticator app.{' '}
                <a href="/account-recovery" onClick={e => { e.preventDefault(); clientNavigate('/account-recovery') }} className="vc-auth-foot-link">
                  Lost your TOTP device?
                </a>
              </p>
              {totpError && <p id="reset-totp-err" className="vc-auth-fielderr">{totpError}</p>}
            </div>

            {/* New password */}
            <div className="vc-auth-field">
              <div className="vc-auth-label-row">
                <label htmlFor="reset-password" className="vc-auth-label">New password</label>
              </div>
              <div className="vc-auth-input-wrap">
                <input
                  id="reset-password"
                  name="new-password"
                  type={showPassword ? 'text' : 'password'}
                  autoComplete="new-password"
                  value={password}
                  onChange={e => { setPassword(e.target.value); if (submitError) setSubmitError(null) }}
                  onBlur={() => setTouched(t => ({ ...t, password: true }))}
                  placeholder={`At least ${MIN_PWD} characters`}
                  required
                  minLength={MIN_PWD}
                  disabled={expired}
                  aria-invalid={passwordError ? 'true' : 'false'}
                  aria-describedby={`reset-password-meter${passwordError ? ' reset-password-err' : ''}`}
                  className={`vc-auth-input has-trailing${passwordError ? ' has-error' : ''}`}
                />
                <button
                  type="button"
                  onClick={() => setShowPassword(s => !s)}
                  className="vc-auth-eye"
                  aria-label={showPassword ? 'Hide password' : 'Show password'}
                  aria-pressed={showPassword}
                >
                  {showPassword ? 'Hide' : 'Show'}
                </button>
              </div>
              <div id="reset-password-meter" aria-live="polite">
                <div className="vc-auth-meter" aria-hidden={password ? 'false' : 'true'}>
                  {[1, 2, 3, 4].map(i => (
                    <span key={i} className={`vc-auth-meter-seg${score >= i ? ' lit-' + STRENGTH_CLASS[score] : ''}`} />
                  ))}
                </div>
                {password && <p className={`vc-auth-meter-label ${STRENGTH_CLASS[score]}`}>{STRENGTH_LABEL[score] || 'Too short'}</p>}
              </div>
              {passwordError && <p id="reset-password-err" className="vc-auth-fielderr">{passwordError}</p>}
            </div>

            {/* Confirm password */}
            <div className="vc-auth-field">
              <div className="vc-auth-label-row">
                <label htmlFor="reset-confirm" className="vc-auth-label">Confirm password</label>
              </div>
              <input
                id="reset-confirm"
                name="confirm-password"
                type={showPassword ? 'text' : 'password'}
                autoComplete="new-password"
                value={confirm}
                onChange={e => { setConfirm(e.target.value); if (submitError) setSubmitError(null) }}
                onBlur={() => setTouched(t => ({ ...t, confirm: true }))}
                placeholder="Type it again"
                required
                disabled={expired}
                aria-invalid={confirmError ? 'true' : 'false'}
                aria-describedby={confirmError ? 'reset-confirm-err' : undefined}
                className={`vc-auth-input${confirmError ? ' has-error' : ''}`}
              />
              {confirmError && <p id="reset-confirm-err" className="vc-auth-fielderr">{confirmError}</p>}
            </div>

            <button
              type="submit"
              className="vc-auth-submit"
              disabled={!canSubmit || expired}
              aria-busy={submitting ? 'true' : 'false'}
            >
              {submitting ? (<><Spinner size={14} /><span>Updating…</span></>) : 'Set new password'}
            </button>

            <div className="vc-auth-foot">
              <span className="vc-auth-foot-text">
                Changed your mind?{' '}
                <a href="/login" onClick={e => { e.preventDefault(); clientNavigate('/login') }} className="vc-auth-foot-link">Back to sign in</a>
              </span>
            </div>

            <div className="vc-auth-foot" style={{ marginTop: '0.25rem' }}>
              <span className="vc-auth-foot-text" style={{ fontSize: '0.78rem', opacity: 0.6 }}>
                <a href="/account-recovery" onClick={e => { e.preventDefault(); clientNavigate('/account-recovery') }} className="vc-auth-foot-link">
                  Fully locked out? Use account recovery (14-day process)
                </a>
              </span>
            </div>
          </form>
        </div>
      </div>
    </Section>
  )
}
