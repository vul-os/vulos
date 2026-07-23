/**
 * vulos-cloud — Signup (bring-your-own-email account wizard)
 *
 * The hosted "@vulos.org identity" model is retired. You sign up with YOUR OWN
 * external email address (gmail / outlook / anything) plus a password — that
 * email IS your Vulos account. There is no hosted handle and no hosted mailbox.
 *
 * Steps:
 *   1 — Email + password (strength meter, min 12, HIBP enforced server-side).
 *       Submitting creates the account (POST /api/auth/signup).
 *   2 — Save recovery codes (backup for if you lose access to that email).
 *   3 — 2FA / TOTP enroll (skippable unless fleet_admin).
 *   4 — Welcome — your account is ready → continue to the OS / console.
 *
 * Constraints:
 *   - JSX, never .tsx
 *   - email + password is the mandatory centre of every account; social sign-up
 *     ("Continue with Google/Microsoft") is a secondary convenience path that
 *     still routes through /onboarding/set-password before the account is usable.
 */

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Section } from '../ui/index.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { useAuth } from '../auth/AuthProvider.jsx'
import { AuthFormStyles } from './Login.jsx'
import SocialButtons from '../components/SocialButtons.jsx'
import { resolvePostLoginDestination } from '../auth/postLogin.js'
import { clientNavigate, goPostLogin } from '../auth/nav.js'

/* ─── Constants ─────────────────────────────────────────── */
const MIN_PWD = 12
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

/* ─── Helpers ───────────────────────────────────────────── */
function safeReturn() {
  try {
    const url = new URL(window.location.href)
    const raw = url.searchParams.get('return')
    if (raw && raw.startsWith('/') && !raw.startsWith('//')) return raw
  } catch { /* ignore */ }
  // No explicit return target: resolved post-auth (your OS box, else the
  // hosted-OS console) — never the Workspace bundle.
  return null
}

async function navigateAfterAuth(returnTo) {
  const dest = returnTo || (await resolvePostLoginDestination())
  goPostLogin(dest)
}

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
const STRENGTH_LABEL = ['', 'Weak', 'Fair', 'Good', 'Strong']
const STRENGTH_CLASS = ['', 'weak', 'okay', 'good', 'strong']

async function copyToClipboard(text) {
  try { await navigator.clipboard.writeText(text); return true } catch { return false }
}

function downloadTxt(filename, text) {
  const blob = new Blob([text], { type: 'text/plain' })
  const url  = URL.createObjectURL(blob)
  const a    = Object.assign(document.createElement('a'), { href: url, download: filename })
  document.body.appendChild(a); a.click(); document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

/* ─── Sub-components ─────────────────────────────────────── */

function Spinner({ size = 14 }) {
  return (
    <span aria-hidden="true" style={{
      display: 'inline-block', width: size, height: size,
      borderRadius: '50%',
      border: `2px solid color-mix(in srgb, currentColor 25%, transparent)`,
      borderTopColor: 'currentColor',
      animation: 'vcAuthSpin 0.7s linear infinite',
    }} />
  )
}

function CopyBtn({ text, label = 'Copy' }) {
  const [copied, setCopied] = useState(false)
  const handle = useCallback(async () => {
    const ok = await copyToClipboard(text)
    if (ok) { setCopied(true); setTimeout(() => setCopied(false), 2000) }
  }, [text])
  return (
    <button type="button" className="su-btn su-btn--ghost su-btn--sm" onClick={handle}>
      {copied ? 'Copied!' : label}
    </button>
  )
}

function RecoveryCodes({ codes }) {
  const flat = codes.join('\n')
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <p className="su-body" style={{ marginBottom: 4 }}>
        <strong style={{ color: 'var(--warn)' }}>Save these recovery codes.</strong>
        {' '}They will not be shown again.
      </p>
      <div className="su-code-box">
        <div className="su-codes-grid">
          {codes.map((c, i) => <span key={i} className="su-code">{c}</span>)}
        </div>
      </div>
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <CopyBtn text={flat} label="Copy all codes" />
        <button type="button" className="su-btn su-btn--ghost su-btn--sm"
          onClick={() => downloadTxt('vulos-recovery-codes.txt', flat)}>
          Download .txt
        </button>
      </div>
    </div>
  )
}

/* ─── ProgressBar ────────────────────────────────────────── */
function ProgressBar({ step, total }) {
  return (
    <div className="su-progress-wrap" aria-label={`Step ${step} of ${total}`}>
      {Array.from({ length: total }, (_, i) => (
        <span
          key={i}
          className={`su-progress-seg${i < step ? ' done' : i === step - 1 ? ' active' : ''}`}
        />
      ))}
    </div>
  )
}

/* ══════════════════════════════════════════════════════════
   STEP 1 — Email + password (creates the account)

   You bring your own email address. It becomes your Vulos account identity;
   Vulos does not host a mailbox for it. Submitting POSTs {email, password} to
   /api/auth/signup.
══════════════════════════════════════════════════════════ */
function StepCredentials({ onNext }) {
  const [email,        setEmail]        = useState('')
  const [password,     setPassword]     = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [touched,      setTouched]      = useState({ email: false, password: false })
  const [submitting,   setSubmitting]   = useState(false)
  const [submitError,  setSubmitError]  = useState(null)
  const { signup } = useAuth()

  const score = scorePassword(password)
  const emailValid = EMAIL_RE.test(email.trim().toLowerCase())
  const emailError = touched.email && email.length > 0 && !emailValid
    ? 'Enter a valid email address.'
    : null
  const passwordError = touched.password && password.length > 0 && password.length < MIN_PWD
    ? `Use at least ${MIN_PWD} characters.`
    : null
  const canSubmit = emailValid && password.length >= MIN_PWD && !submitting

  async function handleSubmit(e) {
    e.preventDefault()
    setTouched({ email: true, password: true })
    if (!canSubmit) return
    setSubmitError(null)
    setSubmitting(true)
    const normalisedEmail = email.trim().toLowerCase()
    try {
      const body = await signup(normalisedEmail, password)
      onNext(normalisedEmail, body)
    } catch (err) {
      setSubmitError(err && err.message ? err.message : 'Could not create your account.')
      setSubmitting(false)
    }
  }

  return (
    <form noValidate onSubmit={handleSubmit} className="su-step">
      <h2 className="su-step-title">Create your Vulos account</h2>
      <p className="su-step-desc">
        Sign up with your own email address — Gmail, Outlook, your own domain, anything.
        That address is how you sign in to your OS, apps and cloud console.
      </p>

      {submitError && (
        <div role="alert" className="vc-auth-alert" aria-live="assertive">
          <span className="vc-auth-alert-tag">Error</span>
          <span className="vc-auth-alert-msg">{submitError}</span>
        </div>
      )}

      <div className="vc-auth-field">
        <div className="vc-auth-label-row">
          <label htmlFor="su-email" className="vc-auth-label">Email</label>
        </div>
        <input
          id="su-email"
          name="email"
          type="email"
          autoComplete="email"
          inputMode="email"
          spellCheck={false}
          autoCapitalize="none"
          value={email}
          onChange={e => { setEmail(e.target.value); if (submitError) setSubmitError(null) }}
          onBlur={() => setTouched(t => ({ ...t, email: true }))}
          placeholder="you@example.com"
          required
          aria-invalid={emailError ? 'true' : 'false'}
          aria-describedby={emailError ? 'su-email-err' : undefined}
          className={`vc-auth-input${emailError ? ' has-error' : ''}`}
          autoFocus
        />
        {emailError && <p id="su-email-err" className="vc-auth-fielderr">{emailError}</p>}
      </div>

      <div className="vc-auth-field">
        <div className="vc-auth-label-row">
          <label htmlFor="su-password" className="vc-auth-label">Password</label>
        </div>
        <div className="vc-auth-input-wrap">
          <input
            id="su-password"
            name="password"
            type={showPassword ? 'text' : 'password'}
            autoComplete="new-password"
            value={password}
            onChange={e => { setPassword(e.target.value); if (submitError) setSubmitError(null) }}
            onBlur={() => setTouched(t => ({ ...t, password: true }))}
            placeholder={`At least ${MIN_PWD} characters`}
            required
            minLength={MIN_PWD}
            aria-invalid={passwordError ? 'true' : 'false'}
            aria-describedby={`su-pw-meter${passwordError ? ' su-pw-err' : ''}`}
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

        <div id="su-pw-meter" aria-live="polite">
          <div className="vc-auth-meter" aria-hidden={password ? 'false' : 'true'}>
            {[1, 2, 3, 4].map(i => (
              <span key={i} className={`vc-auth-meter-seg${score >= i ? ' lit-' + STRENGTH_CLASS[score] : ''}`} />
            ))}
          </div>
          {password ? (
            <p className={`vc-auth-meter-label ${STRENGTH_CLASS[score]}`}>
              {STRENGTH_LABEL[score] || 'Too short'}
            </p>
          ) : (
            <p className="vc-auth-hint" style={{ marginTop: 6 }}>
              Choose something only you would know — at least {MIN_PWD} characters.
            </p>
          )}
        </div>
        {passwordError && <p id="su-pw-err" className="vc-auth-fielderr">{passwordError}</p>}
      </div>

      <button
        type="submit"
        className="vc-auth-submit"
        disabled={!canSubmit}
        aria-busy={submitting ? 'true' : 'false'}
      >
        {submitting ? (<><Spinner size={14} /><span>Creating account…</span></>) : 'Create account'}
      </button>

      <p className="vc-auth-hint" style={{ textAlign: 'center', marginTop: 4 }}>
        By continuing you agree to the{' '}
        <a href="/terms" onClick={e => { e.preventDefault(); clientNavigate('/terms') }} className="vc-auth-foot-link">Terms</a>
        {' '}and{' '}
        <a href="/privacy" onClick={e => { e.preventDefault(); clientNavigate('/privacy') }} className="vc-auth-foot-link">Privacy Policy</a>.
      </p>

      {/* Secondary convenience: sign up with Google/Microsoft instead. Renders
          nothing unless the deployment configured providers. The social path
          still routes through /onboarding/set-password. */}
      <SocialButtons returnTo={safeReturn()} />
    </form>
  )
}

/* ══════════════════════════════════════════════════════════
   STEP 2 — Account recovery codes

   With bring-your-own-email, a password-reset link is delivered to your real
   external inbox. These one-time codes are a backup for if you ever lose access
   to that inbox — POST /api/auth/password/recovery-code resets the password
   with one of them, and burns it.
══════════════════════════════════════════════════════════ */
function StepRecoveryCodes({ codes, onNext }) {
  const [saved, setSaved] = useState(false)

  return (
    <div className="su-step">
      <h2 className="su-step-title">Save your recovery codes</h2>
      <p className="su-step-desc">
        Normally a password-reset link goes to your email inbox. These codes are your
        backup for if you ever lose access to that inbox — any one of them resets your
        password without it. Keep them somewhere safe and separate from your email.
      </p>

      <RecoveryCodes codes={codes} />

      <label className="su-check-row">
        <input
          type="checkbox"
          checked={saved}
          onChange={e => setSaved(e.target.checked)}
        />
        <span>I have saved my recovery codes somewhere safe.</span>
      </label>

      <button
        type="button"
        className="vc-auth-submit"
        disabled={!saved}
        onClick={onNext}
      >
        Continue
      </button>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════
   STEP 3 — TOTP setup (skippable)

   NOTE: enrolling TOTP mints a FRESH set of recovery codes and retires the
   ones from step 2 — the pool is one per account. The enroll panel shows the
   new set for exactly that reason.
══════════════════════════════════════════════════════════ */
function Step2FA({ user, onDone, onSkip }) {
  const isFleetAdmin = Boolean(user?.fleet_admin)
  const [phase,       setPhase]       = useState('idle') // idle | enrolling | enrolled | confirming | confirmed | error
  const [enrollData,  setEnrollData]  = useState(null)
  const [confirmCode, setConfirmCode] = useState('')
  const [error,       setError]       = useState('')

  const handleEnroll = useCallback(async () => {
    setPhase('enrolling'); setError('')
    try {
      const res = await fetch('/api/auth/totp/enroll', {
        method: 'POST', credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      })
      const body = await res.json().catch(() => null)
      if (!res.ok) throw new Error(body?.error || body?.message || `Error ${res.status}`)
      setEnrollData(body); setPhase('enrolled')
    } catch (e) {
      setError(e.message || 'Failed to start 2FA setup. Try again.')
      setPhase('error')
    }
  }, [])

  const handleConfirm = useCallback(async () => {
    const code = confirmCode.replace(/\s/g, '')
    if (code.length < 6) return
    setPhase('confirming'); setError('')
    try {
      const res = await fetch('/api/auth/totp/confirm', {
        method: 'POST', credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ code }),
      })
      const body = await res.json().catch(() => null)
      if (!res.ok) throw new Error(body?.error || body?.message || `Error ${res.status}`)
      setPhase('confirmed')
    } catch (e) {
      setError(e.message || 'Invalid code — check your authenticator app.')
      setPhase('enrolled')
    }
  }, [confirmCode])

  return (
    <div className="su-step">
      <h2 className="su-step-title">Secure your account</h2>
      <p className="su-step-desc">
        Two-factor authentication adds a one-time code from your authenticator app.
        Takes about 60 seconds.
      </p>

      {phase === 'idle' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div className="su-notice su-notice--good">2FA is strongly recommended.</div>
          <div className="su-btn-row">
            <button type="button" className="vc-auth-submit" onClick={handleEnroll}>
              Set up 2FA
            </button>
            {!isFleetAdmin && (
              <button type="button" className="su-btn su-btn--ghost" onClick={onSkip}>
                Skip for now
              </button>
            )}
          </div>
          {isFleetAdmin && (
            <p className="su-help su-help--warn">Fleet administrators must enable 2FA.</p>
          )}
        </div>
      )}

      {phase === 'enrolling' && (
        <p className="su-body su-muted"><Spinner size={13} /> Preparing your authenticator secret…</p>
      )}

      {phase === 'error' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div className="su-notice su-notice--warn" role="alert">{error}</div>
          <button type="button" className="su-btn su-btn--ghost"
            onClick={() => { setPhase('idle'); setError('') }}>
            Try again
          </button>
        </div>
      )}

      {(phase === 'enrolled' || phase === 'confirming') && enrollData && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <div>
            <label className="su-label">Authenticator URI</label>
            <p className="su-help" style={{ marginBottom: 8 }}>
              Open your authenticator app → "Add account" → "Enter setup key manually" and paste the URI.
            </p>
            <div className="su-uri-box">
              <span className="su-uri-text">{enrollData.provisioning_uri}</span>
            </div>
            <div style={{ display: 'flex', gap: 8, marginTop: 8, flexWrap: 'wrap' }}>
              <CopyBtn text={enrollData.provisioning_uri} label="Copy URI" />
              {enrollData.secret && <CopyBtn text={enrollData.secret} label="Copy secret" />}
            </div>
          </div>

          {enrollData.recovery_codes?.length > 0 && (
            <RecoveryCodes codes={enrollData.recovery_codes} />
          )}

          <div>
            <label htmlFor="su-totp-code" className="su-label">
              Enter the 6-digit code from your authenticator to activate 2FA
            </label>
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <input
                id="su-totp-code"
                type="text"
                inputMode="numeric"
                autoComplete="one-time-code"
                placeholder="000 000"
                value={confirmCode}
                onChange={e => setConfirmCode(e.target.value.replace(/[^0-9 ]/g, '').slice(0, 7))}
                disabled={phase === 'confirming'}
                className="su-totp-input"
              />
              <button
                type="button"
                className="su-btn su-btn--primary"
                onClick={handleConfirm}
                disabled={phase === 'confirming' || confirmCode.replace(/\s/g, '').length < 6}
              >
                {phase === 'confirming' ? (<><Spinner size={13} /> Verifying…</>) : 'Confirm & activate'}
              </button>
            </div>
            {error && phase !== 'confirming' && (
              <div className="su-notice su-notice--warn" role="alert" style={{ marginTop: 10 }}>{error}</div>
            )}
          </div>

          {!isFleetAdmin && (
            <button type="button" className="su-btn su-btn--ghost su-btn--sm" onClick={onSkip}>
              Skip 2FA for now
            </button>
          )}
        </div>
      )}

      {phase === 'confirmed' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div className="su-notice su-notice--good" role="status">
            2FA enabled. Your account is protected with TOTP.
          </div>
          <button type="button" className="vc-auth-submit" onClick={onDone}>Continue</button>
        </div>
      )}
    </div>
  )
}

/* ══════════════════════════════════════════════════════════
   STEP 4 — Welcome
══════════════════════════════════════════════════════════ */
function StepWelcome({ email, onNext }) {
  return (
    <div className="su-step su-step--center">
      <div className="su-welcome-ring" aria-hidden="true">
        <LogoMark size={56} tone="on-dark" />
      </div>
      <h2 className="su-step-title">Your account is ready</h2>
      <p className="su-step-desc">
        You&apos;re all set. You sign in with:
      </p>
      <div className="su-identity-pill">
        <span className="su-identity-handle">{email}</span>
      </div>
      <p className="su-body su-muted" style={{ textAlign: 'center', maxWidth: 320, margin: '0 auto' }}>
        This email signs you in across every Vulos service — your OS, your apps, and your
        cloud console.
      </p>
      <button type="button" className="vc-auth-submit" onClick={onNext}>
        Continue
      </button>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════
   MAIN WIZARD
══════════════════════════════════════════════════════════ */
const TOTAL_STEPS = 4

export default function Signup() {
  const { user, loading: authLoading } = useAuth()

  const [step,  setStep]  = useState(1)  // 1-4
  const [email, setEmail] = useState('')
  const [codes, setCodes] = useState([])

  const returnTo = useMemo(() => safeReturn(), [])

  // Already logged in when landing on /signup? Skip straight to the app. Only
  // on the first step — once the wizard has created the account (and the /me
  // probe populates `user`) we must stay put to show recovery codes / 2FA.
  useEffect(() => {
    if (!authLoading && user && step === 1) navigateAfterAuth(returnTo)
  }, [authLoading, user, returnTo, step])

  /* ── Step callbacks ─── */
  // POST /api/auth/signup hands back the account's one-time recovery codes.
  // They are shown once, at step 2; if the server could not mint them the step
  // is skipped and we go straight to 2FA.
  const handleCredentialsNext = useCallback((newEmail, body) => {
    setEmail(newEmail)
    const minted = Array.isArray(body?.recovery_codes) ? body.recovery_codes : []
    setCodes(minted)
    setStep(minted.length > 0 ? 2 : 3)
  }, [])

  const handleCodesNext     = useCallback(() => setStep(3), [])
  const handle2FADoneOrSkip = useCallback(() => setStep(4), [])

  const handleDone = useCallback(() => {
    try { sessionStorage.removeItem('vc:post-signup-return') } catch { /* ignore */ }
    navigateAfterAuth(returnTo)
  }, [returnTo])

  const loginHref = returnTo ? `/login?return=${encodeURIComponent(returnTo)}` : '/login'

  return (
    <Section slim>
      <AuthFormStyles />
      <SuStyles />
      <div className="vc-auth-wrap">
        <div className="vc-auth-card vc-auth-reveal">
          <div className="vc-auth-head" style={{ marginBottom: 0 }}>
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
          </div>

          {/* Progress bar */}
          <ProgressBar step={step} total={TOTAL_STEPS} />

          {step === 1 && <StepCredentials onNext={handleCredentialsNext} />}
          {step === 2 && <StepRecoveryCodes codes={codes} onNext={handleCodesNext} />}
          {step === 3 && (
            <Step2FA
              user={user}
              onDone={handle2FADoneOrSkip}
              onSkip={handle2FADoneOrSkip}
            />
          )}
          {step === 4 && <StepWelcome email={email} onNext={handleDone} />}

          {step === 1 && (
            <div className="vc-auth-foot">
              <span className="vc-auth-foot-text">
                Already have an account?{' '}
                <a
                  href={loginHref}
                  onClick={e => { e.preventDefault(); clientNavigate(loginHref) }}
                  className="vc-auth-foot-link"
                >
                  Sign in
                </a>
              </span>
            </div>
          )}
        </div>
      </div>
    </Section>
  )
}

/* ══════════════════════════════════════════════════════════
   SCOPED STYLES
══════════════════════════════════════════════════════════ */
function SuStyles() {
  return (
    <style>{`
      /* ── Progress bar ─────────────────────────────────── */
      .su-progress-wrap {
        display: flex;
        gap: 4px;
        margin: 16px 0 24px;
      }
      .su-progress-seg {
        flex: 1;
        height: 3px;
        border-radius: 99px;
        background: var(--border-strong);
        transition: background 200ms var(--ease, ease);
      }
      .su-progress-seg.active { background: var(--accent); }
      .su-progress-seg.done   { background: var(--good, #2dd4bf); opacity: 0.7; }

      /* ── Step container ──────────────────────────────── */
      .su-step {
        display: flex;
        flex-direction: column;
        gap: 18px;
      }
      .su-step--center { align-items: center; text-align: center; }
      .su-step-title {
        font-family: var(--font-mono);
        font-size: 1.375rem;
        font-weight: 700;
        letter-spacing: -0.025em;
        color: var(--text-primary);
        margin: 0;
        line-height: 1.2;
      }
      .su-step-desc {
        font-family: var(--font-sans);
        font-size: 0.9375rem;
        color: var(--text-tertiary);
        margin: 0;
        line-height: 1.55;
      }

      /* ── Buttons ─────────────────────────────────────── */
      .su-btn {
        display: inline-flex; align-items: center; justify-content: center;
        gap: 6px; font-family: var(--font-mono); font-weight: 500;
        letter-spacing: 0.01em; line-height: 1; cursor: pointer;
        border-radius: var(--radius, 10px); border: 1px solid transparent;
        white-space: nowrap;
        transition: background 120ms ease, border-color 120ms ease, color 120ms ease, transform 120ms ease;
      }
      .su-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
      .su-btn:disabled { opacity: 0.45; cursor: not-allowed; }

      .su-btn--sm  { padding: 7px 14px; font-size: 0.8125rem; min-height: 34px; }
      .su-btn--primary {
        background: var(--accent); border-color: var(--accent); color: #fff;
        padding: 11px 22px; font-size: 0.875rem; min-height: 44px;
      }
      .su-btn--primary:hover:not(:disabled) {
        background: var(--accent-hover); border-color: var(--accent-hover);
        transform: translateY(-1px);
      }
      .su-btn--ghost {
        background: transparent; border-color: var(--border-strong); color: var(--text-tertiary);
        padding: 11px 22px; font-size: 0.875rem; min-height: 44px;
      }
      .su-btn--ghost:hover:not(:disabled) {
        color: var(--text-primary); border-color: var(--border-emphasis);
        background: rgba(255,255,255,.04);
      }

      /* ── Button rows ─────────────────────────────────── */
      .su-btn-row {
        display: flex;
        gap: 10px;
        flex-wrap: wrap;
      }
      .su-flex-1 { flex: 1; }

      /* ── Notices ─────────────────────────────────────── */
      .su-notice {
        padding: 12px 14px; border-radius: var(--radius, 10px);
        font-family: var(--font-mono); font-size: 0.8125rem; line-height: 1.55;
        border: 1px solid transparent;
      }
      .su-notice--good {
        background: rgba(45,212,191,.07); color: var(--good);
        border-color: rgba(45,212,191,.18);
      }
      .su-notice--warn {
        background: rgba(245,158,11,.07); color: var(--warn);
        border-color: rgba(245,158,11,.18);
      }

      /* ── Labels / help ───────────────────────────────── */
      .su-label {
        font-family: var(--font-mono); font-size: 0.75rem;
        color: var(--text-tertiary); display: block; margin-bottom: 6px;
      }
      .su-help {
        font-family: var(--font-mono); font-size: 0.75rem;
        color: var(--text-tertiary); line-height: 1.55; margin: 0;
      }
      .su-help--warn { color: var(--warn); }
      .su-body {
        font-family: var(--font-mono); font-size: 0.8125rem;
        color: var(--text-secondary); line-height: 1.65; margin: 0;
      }
      .su-muted { color: var(--text-tertiary); }

      /* ── URI box ─────────────────────────────────────── */
      .su-uri-box {
        background: var(--bg-elevated); border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px); padding: 12px 14px;
        overflow-x: auto; word-break: break-all;
      }
      .su-uri-text {
        font-family: var(--font-mono); font-size: 0.75rem;
        color: var(--text-secondary); line-height: 1.55; display: block;
      }

      /* ── Recovery codes ──────────────────────────────── */
      .su-code-box {
        background: var(--bg-elevated); border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px); padding: 14px;
      }
      .su-codes-grid {
        display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px 16px;
      }
      .su-code {
        font-family: var(--font-mono); font-size: 0.875rem; letter-spacing: 0.12em;
        color: var(--text-primary); padding: 4px 0;
        border-bottom: 1px solid var(--border-subtle);
      }
      .su-check-row {
        display: flex; align-items: center; gap: 10px;
        font-size: 0.875rem; color: var(--text-secondary); cursor: pointer;
      }
      .su-check-row input { accent-color: var(--accent); }

      /* ── TOTP input ──────────────────────────────────── */
      .su-totp-input {
        font-family: var(--font-mono); font-size: 1rem; letter-spacing: 0.2em;
        color: var(--text-primary); background: var(--bg-elevated);
        border: 1px solid var(--border-strong); border-radius: var(--radius, 10px);
        padding: 10px 14px; width: 140px; outline: none;
        transition: border-color 120ms ease, box-shadow 120ms ease;
      }
      .su-totp-input:focus-visible { border-color: var(--accent); box-shadow: var(--focus-ring); }
      .su-totp-input::placeholder { color: var(--text-ghost); letter-spacing: normal; }

      /* ── Welcome ring ────────────────────────────────── */
      .su-welcome-ring {
        width: 96px; height: 96px; border-radius: 50%;
        background: rgba(255,255,255,.04);
        border: 1px solid var(--border-strong);
        display: flex; align-items: center; justify-content: center;
        margin-bottom: 4px;
      }

      /* ── Identity pill (shows your own email) ────────── */
      .su-identity-pill {
        display: inline-flex; align-items: baseline; gap: 1px;
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: 24px;
        padding: 8px 18px;
        font-family: var(--font-mono);
        font-size: 1rem;
        max-width: 100%;
      }
      .su-identity-handle {
        color: var(--text-primary); font-weight: 700; letter-spacing: -0.02em;
        overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
      }

      /* ── Responsive ──────────────────────────────────── */
      @media (max-width: 360px) {
        .su-btn-row { flex-direction: column; }
        .su-btn-row .su-flex-1 { width: 100%; }
      }
      @media (prefers-reduced-motion: reduce) {
        .su-btn--primary, .su-btn--ghost { transform: none !important; }
      }
    `}</style>
  )
}
