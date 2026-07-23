/**
 * vulos-cloud — Account Recovery page
 *
 * For users who have lost BOTH their TOTP device AND all recovery codes.
 *
 * SECURITY NOTE (non-negotiable): After submission, there is a mandatory
 * 14-day delay before any TOTP reset can occur. This delay is enforced
 * server-side and cannot be shortened. During this window, the legitimate
 * account holder will receive notification emails and can cancel the request
 * from any active session. See backend/cp/internal/auth/recovery/recovery.go
 * for the full rationale.
 *
 * Flow:
 *   1. User fills in email + optional last-4 of phone + manual-review token
 *      (the review token is issued offline by Vulos support after identity
 *      verification; it is NOT automated).
 *   2. POST /api/auth/recovery/submit
 *   3. Backend starts the 14-day clock. User receives a confirmation email.
 *   4. After 14 days + admin review the account recovery completes.
 *
 * Auth: email + password + TOTP only. No Google OAuth (AUTH-02).
 */

import { useState } from 'react'
import { Section } from '../ui/index.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { clientNavigate } from '../auth/nav.js'

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

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

const styles = `
  @keyframes vcAuthSpin { to { transform: rotate(360deg); } }

  .vc-ar-wrap {
    min-height: 100dvh;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 2rem 1rem;
    background: var(--bg);
  }

  .vc-ar-card {
    width: 100%;
    max-width: 420px;
    background: var(--surface, #0e1117);
    border: 1px solid var(--border, #1e2430);
    border-radius: 10px;
    padding: 2.5rem 2rem;
    display: flex;
    flex-direction: column;
    gap: 1.5rem;
  }

  .vc-ar-header {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.75rem;
    text-align: center;
  }

  .vc-ar-title {
    font-size: 1.2rem;
    font-weight: 600;
    color: var(--text);
    margin: 0;
  }

  .vc-ar-subtitle {
    font-size: 0.85rem;
    color: var(--text-faint, #6b7280);
    margin: 0;
    line-height: 1.5;
  }

  .vc-ar-notice {
    background: color-mix(in srgb, var(--warning, #f59e0b) 8%, transparent);
    border: 1px solid color-mix(in srgb, var(--warning, #f59e0b) 30%, transparent);
    border-radius: 7px;
    padding: 0.85rem 1rem;
    font-size: 0.8rem;
    color: var(--text-faint);
    line-height: 1.55;
  }

  .vc-ar-notice strong {
    color: var(--warning, #f59e0b);
  }

  .vc-ar-form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .vc-ar-field {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }

  .vc-ar-label {
    font-size: 0.78rem;
    font-weight: 500;
    color: var(--text-faint);
    letter-spacing: 0.01em;
  }

  .vc-ar-optional {
    font-size: 0.7rem;
    color: var(--text-faint, #6b7280);
    opacity: 0.6;
    margin-left: 0.25rem;
  }

  .vc-ar-input {
    width: 100%;
    box-sizing: border-box;
    background: var(--bg, #060a0f);
    border: 1px solid var(--border, #1e2430);
    border-radius: 6px;
    color: var(--text);
    font-size: 0.9rem;
    font-family: var(--font);
    padding: 0.55rem 0.75rem;
    outline: none;
    transition: border-color 0.15s;
  }

  .vc-ar-input:focus {
    border-color: var(--accent, #3b82f6);
  }

  .vc-ar-input[aria-invalid='true'] {
    border-color: var(--error, #ef4444);
  }

  .vc-ar-field-error {
    font-size: 0.75rem;
    color: var(--error, #ef4444);
  }

  .vc-ar-hint {
    font-size: 0.75rem;
    color: var(--text-faint);
    opacity: 0.7;
    line-height: 1.45;
  }

  .vc-ar-submit {
    width: 100%;
    background: var(--accent, #3b82f6);
    color: #fff;
    border: none;
    border-radius: 6px;
    font-size: 0.9rem;
    font-weight: 600;
    font-family: var(--font);
    padding: 0.65rem 1rem;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    transition: opacity 0.15s;
    margin-top: 0.25rem;
  }

  .vc-ar-submit:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .vc-ar-error-box {
    background: color-mix(in srgb, var(--error, #ef4444) 8%, transparent);
    border: 1px solid color-mix(in srgb, var(--error, #ef4444) 25%, transparent);
    border-radius: 7px;
    padding: 0.75rem 1rem;
    font-size: 0.82rem;
    color: var(--error, #ef4444);
  }

  .vc-ar-footer {
    text-align: center;
    font-size: 0.8rem;
    color: var(--text-faint);
  }

  .vc-ar-footer a {
    color: var(--accent, #3b82f6);
    text-decoration: none;
  }

  .vc-ar-footer a:hover { text-decoration: underline; }

  .vc-ar-success {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    text-align: center;
  }

  .vc-ar-success-icon {
    font-size: 2.5rem;
    line-height: 1;
  }

  .vc-ar-success-title {
    font-size: 1.1rem;
    font-weight: 600;
    color: var(--text);
    margin: 0;
  }

  .vc-ar-success-body {
    font-size: 0.85rem;
    color: var(--text-faint);
    line-height: 1.6;
    margin: 0;
  }

  .vc-ar-back-btn {
    background: transparent;
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text-faint);
    font-size: 0.85rem;
    font-family: var(--font);
    padding: 0.55rem 1rem;
    cursor: pointer;
    transition: border-color 0.15s;
  }

  .vc-ar-back-btn:hover { border-color: var(--accent); color: var(--accent); }
`

export default function AccountRecovery() {
  const [email,         setEmail]       = useState('')
  const [phone,         setPhone]       = useState('')
  const [reviewToken,   setReviewToken] = useState('')
  const [touched,       setTouched]     = useState({})
  const [submitting,    setSubmitting]  = useState(false)
  const [submitted,     setSubmitted]   = useState(false)
  const [serverError,   setServerError] = useState('')

  function touch(field) {
    setTouched(prev => ({ ...prev, [field]: true }))
  }

  const emailError = touched.email && !EMAIL_RE.test(email.trim())
    ? 'Enter a valid email address.'
    : null

  const canSubmit =
    EMAIL_RE.test(email.trim()) &&
    reviewToken.trim().length >= 6 &&
    !submitting

  async function handleSubmit(e) {
    e.preventDefault()
    setTouched({ email: true, reviewToken: true })
    if (!canSubmit) return

    setSubmitting(true)
    setServerError('')

    try {
      const res = await fetch('/api/auth/recovery/submit', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          email: email.trim().toLowerCase(),
          phone_last4: phone.trim() || undefined,
          review_token: reviewToken.trim(),
        }),
      })

      if (res.status === 429) {
        setServerError(
          'Too many recovery attempts. Your account has been frozen for security review. ' +
          'Contact support at security@vulos.org.'
        )
        return
      }
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        setServerError(body.error || 'Something went wrong. Please try again or contact support.')
        return
      }
      setSubmitted(true)
    } catch {
      setServerError('Network error. Please check your connection and try again.')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <>
      <style>{styles}</style>

      <Section>
        <div className="vc-ar-wrap">
          <div className="vc-ar-card">

            <div className="vc-ar-header">
              <LogoMark size={32} />
              <h1 className="vc-ar-title">Account Recovery</h1>
              <p className="vc-ar-subtitle">
                For users who have lost access to both their 2FA device and all recovery codes.
              </p>
            </div>

            {submitted ? (
              <div className="vc-ar-success" role="status">
                <div className="vc-ar-success-icon" aria-hidden="true">&#10003;</div>
                <h2 className="vc-ar-success-title">Recovery request submitted</h2>
                <p className="vc-ar-success-body">
                  A confirmation email has been sent to <strong>{email}</strong>.
                  <br /><br />
                  <strong>A mandatory 14-day security review period now begins.</strong>
                  {' '}During this time you will receive notification emails and can cancel
                  the request from any active session. After 14 days and admin review your
                  TOTP will be reset and new recovery codes will be issued.
                  <br /><br />
                  If you did not initiate this request, sign in immediately and cancel it,
                  or contact <a href="mailto:security@vulos.org">security@vulos.org</a>.
                </p>
                <button
                  className="vc-ar-back-btn"
                  onClick={() => clientNavigate('/login')}
                >
                  Back to login
                </button>
              </div>
            ) : (
              <>
                <div className="vc-ar-notice" role="note">
                  <strong>14-day mandatory wait.</strong>{' '}
                  After submitting, a 14-day security review period begins before any account
                  changes take effect. This delay is non-negotiable and protects your account
                  from takeover attacks. You will receive email notifications throughout.
                </div>

                {serverError && (
                  <div className="vc-ar-error-box" role="alert">
                    {serverError}
                  </div>
                )}

                <form className="vc-ar-form" onSubmit={handleSubmit} noValidate>

                  <div className="vc-ar-field">
                    <label htmlFor="ar-email" className="vc-ar-label">
                      Account email
                    </label>
                    <input
                      id="ar-email"
                      type="email"
                      className="vc-ar-input"
                      autoComplete="email"
                      value={email}
                      onChange={e => setEmail(e.target.value)}
                      onBlur={() => touch('email')}
                      aria-invalid={emailError ? 'true' : undefined}
                      aria-describedby={emailError ? 'ar-email-error' : undefined}
                      placeholder="you@example.com"
                      required
                    />
                    {emailError && (
                      <span id="ar-email-error" className="vc-ar-field-error" role="alert">
                        {emailError}
                      </span>
                    )}
                  </div>

                  <div className="vc-ar-field">
                    <label htmlFor="ar-phone" className="vc-ar-label">
                      Last 4 digits of phone on file
                      <span className="vc-ar-optional">(optional)</span>
                    </label>
                    <input
                      id="ar-phone"
                      type="text"
                      inputMode="numeric"
                      maxLength={4}
                      pattern="\d{4}"
                      className="vc-ar-input"
                      autoComplete="tel"
                      value={phone}
                      onChange={e => setPhone(e.target.value.replace(/\D/g, '').slice(0, 4))}
                      placeholder="1234"
                    />
                    <span className="vc-ar-hint">
                      Providing this helps our team verify your identity faster.
                    </span>
                  </div>

                  <div className="vc-ar-field">
                    <label htmlFor="ar-token" className="vc-ar-label">
                      Manual review token
                    </label>
                    <input
                      id="ar-token"
                      type="text"
                      className="vc-ar-input"
                      autoComplete="off"
                      spellCheck={false}
                      value={reviewToken}
                      onChange={e => setReviewToken(e.target.value)}
                      onBlur={() => touch('reviewToken')}
                      placeholder="Provided by Vulos support"
                      required
                    />
                    <span className="vc-ar-hint">
                      Contact{' '}
                      <a href="mailto:security@vulos.org" style={{ color: 'var(--accent)' }}>
                        security@vulos.org
                      </a>{' '}
                      to obtain a review token. This is not automated — a team member will
                      verify your identity before issuing one.
                    </span>
                  </div>

                  <button
                    type="submit"
                    className="vc-ar-submit"
                    disabled={!canSubmit}
                    aria-busy={submitting}
                  >
                    {submitting ? (
                      <>
                        <Spinner />
                        Submitting&hellip;
                      </>
                    ) : (
                      'Submit recovery request'
                    )}
                  </button>
                </form>

                <div className="vc-ar-footer">
                  <a href="/login" onClick={e => { e.preventDefault(); clientNavigate('/login') }}>
                    Back to login
                  </a>
                  {' · '}
                  <a href="/forgot" onClick={e => { e.preventDefault(); clientNavigate('/forgot') }}>
                    Forgot password instead?
                  </a>
                </div>
              </>
            )}

          </div>
        </div>
      </Section>
    </>
  )
}
