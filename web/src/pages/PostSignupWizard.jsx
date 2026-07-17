/**
 * vulos-cloud — PostSignupWizard
 *
 * Two-step wizard shown after successful signup, before the user
 * reaches /app. Steps:
 *
 *   Step 1 — 2FA setup (skippable for non-fleet_admin)
 *     • POST /api/auth/totp/enroll → provisioning URI + 10 recovery codes
 *     • Copyable otpauth:// URI (no heavy QR dep, mirrors LAND-05 Account.jsx pattern)
 *     • Copy-all + Download-as-txt for recovery codes
 *     • 6-digit confirm → POST /api/auth/totp/confirm
 *     • Skip button disabled + explanation when fleet_admin=1
 *
 *   Step 2 — Email verification nudge
 *     • Shows user email, Resend button → POST /api/auth/verify-email/resend (204)
 *     • Rate-limited resend UI (30 s cooldown)
 *     • "Continue" → /app
 *
 * Design: reuses acc-* token classes defined inline here (mirrors Account.jsx).
 * All styles are scoped under .psw-* to avoid collisions.
 */

import { useState, useCallback, useEffect, useRef } from 'react'
import { useAuth } from '../auth/AuthProvider.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { clientNavigate } from '../auth/nav.js'

/* ─── Clipboard helper ──────────────────────────────────── */
async function copyToClipboard(text) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

/* ─── Download-as-txt helper ────────────────────────────── */
function downloadTxt(filename, text) {
  const blob = new Blob([text], { type: 'text/plain' })
  const url  = URL.createObjectURL(blob)
  const a    = Object.assign(document.createElement('a'), {
    href: url, download: filename,
  })
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

/* ─── Spinner ────────────────────────────────────────────── */
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
        animation: 'pswSpin 0.7s linear infinite',
      }}
    />
  )
}

/* ─── CopyBtn ────────────────────────────────────────────── */
function CopyBtn({ text, label = 'Copy' }) {
  const [copied, setCopied] = useState(false)
  const handle = useCallback(async () => {
    const ok = await copyToClipboard(text)
    if (ok) {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }, [text])
  return (
    <button type="button" className="psw-btn psw-btn--ghost psw-btn--sm" onClick={handle}>
      {copied ? 'Copied!' : label}
    </button>
  )
}

/* ─── RecoveryCodes ─────────────────────────────────────── */
function RecoveryCodes({ codes }) {
  const flat = codes.join('\n')

  return (
    <div className="psw-codes-wrap">
      <p className="psw-body" style={{ marginBottom: '10px' }}>
        <strong style={{ color: 'var(--warn)' }}>Save these recovery codes now.</strong>
        {' '}They will not be shown again. Each code can be used once if you lose your authenticator.
      </p>
      <div className="psw-codes-box">
        <div className="psw-codes-grid">
          {codes.map((c, i) => (
            <span key={i} className="psw-code">{c}</span>
          ))}
        </div>
      </div>
      <div className="psw-row-gap">
        <CopyBtn text={flat} label="Copy all codes" />
        <button
          type="button"
          className="psw-btn psw-btn--ghost psw-btn--sm"
          onClick={() => downloadTxt('vulos-recovery-codes.txt', flat)}
        >
          Download as .txt
        </button>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════
   STEP 1 — 2FA Setup
══════════════════════════════════════════════════════════ */
function Step2FA({ user, onDone, onSkip }) {
  const isFleetAdmin = Boolean(user?.fleet_admin)

  /* enroll state: idle | enrolling | enrolled | confirming | confirmed | error */
  const [step,        setStep]        = useState('idle')
  const [enrollData,  setEnrollData]  = useState(null) // { provisioning_uri, secret, recovery_codes }
  const [confirmCode, setConfirmCode] = useState('')
  const [error,       setError]       = useState('')

  /* POST /api/auth/totp/enroll */
  const handleEnroll = useCallback(async () => {
    setStep('enrolling')
    setError('')
    try {
      const res = await fetch('/api/auth/totp/enroll', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      })
      const body = await res.json().catch(() => null)
      if (!res.ok) throw new Error(body?.error || body?.message || `Error ${res.status}`)
      setEnrollData(body)
      setStep('enrolled')
    } catch (e) {
      setError(e.message || 'Failed to start 2FA setup. Please try again.')
      setStep('error')
    }
  }, [])

  /* POST /api/auth/totp/confirm */
  const handleConfirm = useCallback(async () => {
    const code = confirmCode.replace(/\s/g, '')
    if (code.length < 6) return
    setStep('confirming')
    setError('')
    try {
      const res = await fetch('/api/auth/totp/confirm', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ code }),
      })
      const body = await res.json().catch(() => null)
      if (!res.ok) throw new Error(body?.error || body?.message || `Error ${res.status}`)
      setStep('confirmed')
    } catch (e) {
      setError(e.message || 'Invalid code — check your authenticator app and try again.')
      setStep('enrolled')
    }
  }, [confirmCode])

  return (
    <div className="psw-step">
      <div className="psw-step-header">
        <div className="psw-step-badge">Step 1 of 2</div>
        <h2 className="psw-step-title">Set up two-factor authentication</h2>
        <p className="psw-step-desc">
          2FA protects your account with a one-time code from your authenticator app (Google Authenticator, Authy, 1Password, etc.).
        </p>
      </div>

      {/* ── idle: invite to set up ── */}
      {step === 'idle' && (
        <div className="psw-body-area">
          <div className="psw-notice psw-notice--good" role="status">
            <span className="psw-notice-dot" aria-hidden="true" />
            Recommended — takes about 60 seconds.
          </div>
          <div className="psw-actions">
            <button type="button" className="psw-btn psw-btn--primary" onClick={handleEnroll}>
              Set up 2FA now
            </button>
            {!isFleetAdmin && (
              <button type="button" className="psw-btn psw-btn--ghost" onClick={onSkip}>
                Skip for now
              </button>
            )}
          </div>
          {isFleetAdmin && (
            <p className="psw-help psw-help--warn">
              Fleet administrators must enable 2FA. The skip option is not available for your account type.
            </p>
          )}
        </div>
      )}

      {/* ── enrolling ── */}
      {step === 'enrolling' && (
        <div className="psw-body-area">
          <p className="psw-body psw-muted">
            <Spinner size={13} /> Preparing your authenticator secret…
          </p>
        </div>
      )}

      {/* ── error ── */}
      {step === 'error' && (
        <div className="psw-body-area">
          <div className="psw-notice psw-notice--warn" role="alert">
            <span className="psw-notice-dot" aria-hidden="true" />
            {error}
          </div>
          <div className="psw-actions">
            <button
              type="button"
              className="psw-btn psw-btn--ghost"
              onClick={() => { setStep('idle'); setError('') }}
            >
              Try again
            </button>
          </div>
        </div>
      )}

      {/* ── enrolled / confirming: show URI + recovery codes + confirm input ── */}
      {(step === 'enrolled' || step === 'confirming') && enrollData && (
        <div className="psw-body-area">
          {/* Authenticator URI */}
          <div className="psw-uri-section">
            <label className="psw-label">Authenticator URI</label>
            <p className="psw-help" style={{ marginBottom: '8px' }}>
              Open your authenticator app, tap "Add account" → "Enter setup key manually" (or scan a QR), then paste the URI below.
            </p>
            <div className="psw-uri-box">
              <span className="psw-uri-text">{enrollData.provisioning_uri}</span>
            </div>
            <div className="psw-row-gap" style={{ marginTop: '8px' }}>
              <CopyBtn text={enrollData.provisioning_uri} label="Copy URI" />
              {enrollData.secret && (
                <CopyBtn text={enrollData.secret} label="Copy secret" />
              )}
            </div>
          </div>

          {/* Recovery codes */}
          {enrollData.recovery_codes && enrollData.recovery_codes.length > 0 && (
            <RecoveryCodes codes={enrollData.recovery_codes} />
          )}

          {/* Confirm TOTP code */}
          <div className="psw-confirm-section">
            <label htmlFor="psw-totp-code" className="psw-label">
              Enter the 6-digit code from your authenticator to activate 2FA
            </label>
            <div className="psw-confirm-row">
              <input
                id="psw-totp-code"
                type="text"
                inputMode="numeric"
                autoComplete="one-time-code"
                placeholder="000 000"
                value={confirmCode}
                onChange={e => setConfirmCode(e.target.value.replace(/[^0-9 ]/g, '').slice(0, 7))}
                disabled={step === 'confirming'}
                className="psw-input"
                style={{ maxWidth: '160px' }}
              />
              <button
                type="button"
                className="psw-btn psw-btn--primary"
                onClick={handleConfirm}
                disabled={step === 'confirming' || confirmCode.replace(/\s/g, '').length < 6}
              >
                {step === 'confirming' ? (<><Spinner size={13} /> Verifying…</>) : 'Confirm & activate'}
              </button>
            </div>
            {error && step !== 'confirming' && (
              <div className="psw-notice psw-notice--warn" role="alert" style={{ marginTop: '10px' }}>
                <span className="psw-notice-dot" aria-hidden="true" />
                {error}
              </div>
            )}
          </div>

          {!isFleetAdmin && (
            <div style={{ paddingTop: '4px' }}>
              <button type="button" className="psw-btn psw-btn--ghost psw-btn--sm" onClick={onSkip}>
                Skip 2FA for now
              </button>
            </div>
          )}
        </div>
      )}

      {/* ── confirmed ── */}
      {step === 'confirmed' && (
        <div className="psw-body-area">
          <div className="psw-notice psw-notice--good" role="status">
            <span className="psw-notice-dot" aria-hidden="true" />
            2FA enabled. Your account is now protected with TOTP.
          </div>
          <div className="psw-actions" style={{ paddingTop: '8px' }}>
            <button type="button" className="psw-btn psw-btn--primary" onClick={onDone}>
              Continue
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

/* ══════════════════════════════════════════════════════════
   STEP 2 — Email verification nudge
══════════════════════════════════════════════════════════ */
const RESEND_COOLDOWN = 30 // seconds

function StepEmailVerify({ user, onDone }) {
  const email = user?.email ?? ''
  const isVerified = Boolean(user?.email_verified)

  /* resend state: idle | sending | sent | error */
  const [resendState, setResendState] = useState('idle')
  const [resendError, setResendError] = useState('')
  const [cooldown,    setCooldown]    = useState(0)
  const timerRef = useRef(null)

  useEffect(() => {
    return () => { if (timerRef.current) clearInterval(timerRef.current) }
  }, [])

  const handleResend = useCallback(async () => {
    setResendState('sending')
    setResendError('')
    try {
      const res = await fetch('/api/auth/verify-email/resend', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      })
      if (!res.ok && res.status !== 204) {
        const body = await res.json().catch(() => null)
        throw new Error(body?.error || body?.message || `Error ${res.status}`)
      }
      setResendState('sent')
      // start cooldown
      let secs = RESEND_COOLDOWN
      setCooldown(secs)
      timerRef.current = setInterval(() => {
        secs -= 1
        setCooldown(secs)
        if (secs <= 0) {
          clearInterval(timerRef.current)
          setResendState('idle')
          setCooldown(0)
        }
      }, 1000)
    } catch (e) {
      setResendError(e.message || 'Could not resend. Please try again shortly.')
      setResendState('error')
    }
  }, [])

  return (
    <div className="psw-step">
      <div className="psw-step-header">
        <div className="psw-step-badge">Step 2 of 2</div>
        <h2 className="psw-step-title">Verify your email</h2>
        <p className="psw-step-desc">
          We sent a verification link to your inbox. Verifying your email unlocks all account features.
        </p>
      </div>

      <div className="psw-body-area">
        {isVerified ? (
          <div className="psw-notice psw-notice--good" role="status">
            <span className="psw-notice-dot" aria-hidden="true" />
            Email verified — you're all set!
          </div>
        ) : (
          <>
            <div className="psw-notice psw-notice--faint" role="status">
              <span className="psw-notice-dot" aria-hidden="true" />
              Check your inbox for{' '}
              <span className="psw-mono">{email || 'your email address'}</span>.
              {' '}If you don't see it, check your spam folder.
            </div>

            {resendError && (
              <div className="psw-notice psw-notice--warn" role="alert">
                <span className="psw-notice-dot" aria-hidden="true" />
                {resendError}
              </div>
            )}

            {resendState === 'sent' && (
              <div className="psw-notice psw-notice--good" role="status">
                <span className="psw-notice-dot" aria-hidden="true" />
                Verification email resent. Check your inbox.
              </div>
            )}

            <div className="psw-actions">
              <button
                type="button"
                className="psw-btn psw-btn--ghost psw-btn--sm"
                onClick={handleResend}
                disabled={resendState === 'sending' || cooldown > 0}
              >
                {resendState === 'sending'
                  ? (<><Spinner size={12} /> Sending…</>)
                  : cooldown > 0
                    ? `Resend in ${cooldown}s`
                    : 'Resend verification email'}
              </button>
            </div>

            <p className="psw-help" style={{ marginTop: '8px' }}>
              You can continue to your dashboard now — you'll see a reminder until your email is verified.
            </p>
          </>
        )}

        <div className="psw-actions" style={{ paddingTop: '8px', borderTop: '1px solid var(--border-subtle)', marginTop: '8px' }}>
          <button type="button" className="psw-btn psw-btn--primary" onClick={onDone}>
            {isVerified ? 'Go to dashboard' : 'Continue to dashboard'}
          </button>
        </div>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════
   WIZARD SHELL
══════════════════════════════════════════════════════════ */
export default function PostSignupWizard() {
  const { user, loading: authLoading, refresh } = useAuth()

  /* step: '2fa' | 'email' */
  const [currentStep, setCurrentStep] = useState('2fa')

  /* If user somehow ends up here while not logged in → go login */
  useEffect(() => {
    if (!authLoading && !user) {
      clientNavigate('/login')
    }
  }, [authLoading, user])

  /* 2FA step done (confirmed or skipped) → go to email step */
  const handle2FADone = useCallback(async () => {
    await refresh()
    setCurrentStep('email')
  }, [refresh])

  /* Email step done → go to the console dashboard shell */
  const handleEmailDone = useCallback(() => {
    clientNavigate('/')
  }, [])

  if (authLoading) {
    return (
      <div className="psw-page" style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', minHeight: '100dvh' }}>
        <Spinner size={20} />
      </div>
    )
  }

  if (!user) return null

  return (
    <>
      <PswStyles />

      <div className="psw-page">
        <div className="psw-card psw-reveal">
          {/* Brand header */}
          <div className="psw-brand-row">
            <button
              type="button"
              className="psw-brand-link"
              onClick={() => clientNavigate('/')}
              aria-label="Vulos — home"
            >
              <LogoMark size={32} tone="on-dark" />
              <span className="psw-wordmark">
                vulos<span className="psw-wordmark-suffix">.org</span>
              </span>
            </button>
          </div>

          {/* Progress dots */}
          <div className="psw-progress" aria-label="Wizard progress">
            <span
              className={`psw-dot${currentStep === '2fa' ? ' psw-dot--active' : ' psw-dot--done'}`}
              aria-label="Step 1: 2FA setup"
            />
            <span
              className={`psw-dot${currentStep === 'email' ? ' psw-dot--active' : ''}`}
              aria-label="Step 2: Email verification"
            />
          </div>

          {currentStep === '2fa' && (
            <Step2FA
              user={user}
              onDone={handle2FADone}
              onSkip={handle2FADone}
            />
          )}

          {currentStep === 'email' && (
            <StepEmailVerify
              user={user}
              onDone={handleEmailDone}
            />
          )}
        </div>
      </div>
    </>
  )
}

/* ══════════════════════════════════════════════════════════
   SCOPED STYLES
══════════════════════════════════════════════════════════ */
function PswStyles() {
  return (
    <style>{`
      @keyframes pswSpin {
        to { transform: rotate(360deg); }
      }
      @keyframes pswReveal {
        from { opacity: 0; transform: translateY(10px); }
        to   { opacity: 1; transform: translateY(0); }
      }

      /* ── Page ────────────────────────────────────────── */
      .psw-page {
        min-height: 100dvh;
        display: flex;
        align-items: flex-start;
        justify-content: center;
        padding: 64px 16px 80px;
        background: var(--bg-base);
      }

      /* ── Card ────────────────────────────────────────── */
      .psw-card {
        width: 100%;
        max-width: 520px;
        background: var(--bg-surface);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius-lg);
        padding: 32px;
        display: flex;
        flex-direction: column;
        gap: 24px;
      }
      .psw-reveal {
        animation: pswReveal 240ms var(--ease, ease) both;
      }

      /* ── Brand row ───────────────────────────────────── */
      .psw-brand-row {
        display: flex;
        justify-content: center;
      }
      .psw-brand-link {
        display: inline-flex;
        align-items: center;
        gap: 10px;
        text-decoration: none;
        background: none;
        border: none;
        cursor: pointer;
        padding: 0;
      }
      .psw-wordmark {
        font-family: var(--font-mono);
        font-size: 1.125rem;
        font-weight: 700;
        letter-spacing: -0.03em;
        color: var(--text-primary);
        line-height: 1;
      }
      .psw-wordmark-suffix {
        color: var(--text-faint);
        font-weight: 400;
      }

      /* ── Progress dots ───────────────────────────────── */
      .psw-progress {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
      }
      .psw-dot {
        display: inline-block;
        width: 8px;
        height: 8px;
        border-radius: 50%;
        background: var(--border-strong);
        transition: background 200ms ease, transform 200ms ease;
      }
      .psw-dot--active {
        background: var(--accent);
        transform: scale(1.25);
      }
      .psw-dot--done {
        background: var(--good, #2dd4bf);
        opacity: 0.7;
      }

      /* ── Step ────────────────────────────────────────── */
      .psw-step {
        display: flex;
        flex-direction: column;
        gap: 20px;
      }
      .psw-step-header {
        display: flex;
        flex-direction: column;
        gap: 8px;
      }
      .psw-step-badge {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 500;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--accent);
        line-height: 1;
      }
      .psw-step-title {
        font-family: var(--font-mono);
        font-size: 1.125rem;
        font-weight: 700;
        letter-spacing: -0.02em;
        color: var(--text-primary);
        margin: 0;
        line-height: 1.3;
      }
      .psw-step-desc {
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        color: var(--text-tertiary);
        line-height: 1.6;
        margin: 0;
      }

      /* ── Body area ───────────────────────────────────── */
      .psw-body-area {
        display: flex;
        flex-direction: column;
        gap: 16px;
      }
      .psw-body {
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        color: var(--text-secondary);
        line-height: 1.65;
        margin: 0;
      }
      .psw-muted { color: var(--text-tertiary); }
      .psw-mono  { font-family: var(--font-mono); }

      /* ── Label ───────────────────────────────────────── */
      .psw-label {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-tertiary);
        display: block;
        margin-bottom: 6px;
      }

      /* ── Input ───────────────────────────────────────── */
      .psw-input {
        width: 100%;
        font-family: var(--font-mono);
        font-size: 0.875rem;
        color: var(--text-primary);
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius);
        padding: 10px 14px;
        transition:
          border-color var(--dur-fast, 120ms) var(--ease, ease),
          box-shadow   var(--dur-fast, 120ms) var(--ease, ease);
        outline: none;
        letter-spacing: 0.15em;
      }
      .psw-input:focus-visible {
        border-color: var(--accent);
        box-shadow: var(--focus-ring);
      }
      .psw-input::placeholder { color: var(--text-muted); letter-spacing: normal; }
      .psw-input:disabled { opacity: 0.5; cursor: not-allowed; }

      /* ── Buttons ─────────────────────────────────────── */
      .psw-btn {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 6px;
        font-family: var(--font-mono);
        font-weight: 500;
        letter-spacing: 0.01em;
        line-height: 1;
        cursor: pointer;
        border-radius: var(--radius);
        border: 1px solid transparent;
        white-space: nowrap;
        transition:
          background   var(--dur-fast, 120ms) var(--ease, ease),
          border-color var(--dur-fast, 120ms) var(--ease, ease),
          color        var(--dur-fast, 120ms) var(--ease, ease),
          transform    var(--dur-fast, 120ms) var(--ease, ease);
      }
      .psw-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
      .psw-btn:disabled { opacity: 0.45; cursor: not-allowed; }
      .psw-btn:disabled:hover { transform: none; }

      .psw-btn--sm { padding: 7px 14px; font-size: 0.8125rem; min-height: 34px; }
      .psw-btn--md { padding: 11px 22px; font-size: 0.875rem;  min-height: 44px; }

      .psw-btn--primary {
        background: var(--accent);
        border-color: var(--accent);
        color: #fff;
        padding: 11px 22px;
        font-size: 0.875rem;
        min-height: 44px;
      }
      .psw-btn--primary:hover:not(:disabled) {
        background: var(--accent-hover);
        border-color: var(--accent-hover);
        transform: translateY(-1px);
      }
      .psw-btn--primary:active { transform: translateY(0); }

      .psw-btn--ghost {
        background: transparent;
        border-color: var(--border-strong);
        color: var(--text-tertiary);
      }
      .psw-btn--ghost:hover:not(:disabled) {
        color: var(--text-primary);
        border-color: var(--border-emphasis);
        background: rgba(255,255,255,.04);
        transform: translateY(-1px);
      }
      .psw-btn--ghost:active { transform: translateY(0); }

      /* ── Actions row ─────────────────────────────────── */
      .psw-actions {
        display: flex;
        align-items: center;
        gap: 10px;
        flex-wrap: wrap;
      }

      .psw-row-gap {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
      }

      /* ── Notices ─────────────────────────────────────── */
      .psw-notice {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        padding: 12px 14px;
        border-radius: var(--radius);
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        line-height: 1.55;
        border: 1px solid transparent;
      }
      .psw-notice--good {
        background: rgba(45,212,191,.07);
        color: var(--good);
        border-color: rgba(45,212,191,.18);
      }
      .psw-notice--warn {
        background: rgba(245,158,11,.07);
        color: var(--warn);
        border-color: rgba(245,158,11,.18);
      }
      .psw-notice--faint {
        background: rgba(255,255,255,.03);
        color: var(--text-secondary);
        border-color: var(--border-strong);
      }
      .psw-notice-dot {
        display: inline-block;
        width: 6px;
        height: 6px;
        border-radius: 50%;
        background: currentColor;
        flex-shrink: 0;
        margin-top: 5px;
        opacity: 0.75;
      }

      /* ── Help text ───────────────────────────────────── */
      .psw-help {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-tertiary);
        line-height: 1.55;
      }
      .psw-help--warn { color: var(--warn); }

      /* ── URI box ─────────────────────────────────────── */
      .psw-uri-section { display: flex; flex-direction: column; }
      .psw-uri-box {
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius);
        padding: 12px 14px;
        overflow-x: auto;
        word-break: break-all;
      }
      .psw-uri-text {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-secondary);
        line-height: 1.55;
        display: block;
      }

      /* ── Recovery codes ──────────────────────────────── */
      .psw-codes-wrap {
        display: flex;
        flex-direction: column;
        gap: 10px;
      }
      .psw-codes-box {
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius);
        padding: 14px;
      }
      .psw-codes-grid {
        display: grid;
        grid-template-columns: repeat(2, 1fr);
        gap: 6px 16px;
      }
      .psw-code {
        font-family: var(--font-mono);
        font-size: 0.875rem;
        letter-spacing: 0.12em;
        color: var(--text-primary);
        padding: 4px 0;
        border-bottom: 1px solid var(--border-subtle);
      }

      /* ── Confirm row ─────────────────────────────────── */
      .psw-confirm-section { display: flex; flex-direction: column; }
      .psw-confirm-row {
        display: flex;
        align-items: stretch;
        gap: 8px;
        flex-wrap: wrap;
      }

      /* ── Responsive ──────────────────────────────────── */
      @media (max-width: 560px) {
        .psw-page   { padding: 40px 12px 64px; }
        .psw-card   { padding: 24px 18px; gap: 20px; }
        .psw-codes-grid { grid-template-columns: 1fr; }
        .psw-confirm-row { flex-direction: column; }
        .psw-confirm-row .psw-input { max-width: 100% !important; }
        .psw-btn--primary { width: 100%; }
      }
      @media (max-width: 360px) {
        .psw-page   { padding: 28px 10px 56px; }
        .psw-card   { padding: 20px 14px; }
      }
      @media (prefers-reduced-motion: reduce) {
        .psw-reveal { animation: none; }
      }
    `}</style>
  )
}
