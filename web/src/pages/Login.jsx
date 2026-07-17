/**
 * vulos-cloud — Login page
 *
 * OSS-native: near-black canvas, hairline borders, mono-led type, ONE
 * measured accent moment (the submit button). Real <LogoMark> at the top
 * with a subtle "vulos.org" wordmark beside it.
 *
 * Flow:
 *   1. user types email + password
 *   2. submit → AuthProvider.login(email, password) → POST /api/auth/login
 *   3. on success: navigate to ?return=… (same-origin paths only) or /app
 *   4. on error: dedicated role="alert" region above the form; the form
 *      stays interactive so the user can retry.
 *
 * Auth: email + password is the mandatory centre of every account. A passkey is an
 * additional sign-in path, and "Continue with Google/Microsoft" is a secondary
 * convenience login (rendered below the password form, only when the deployment has
 * configured those providers). A social sign-up still sets a Vulos password before
 * the account is usable; a social login that collides with an existing account must
 * prove that account's password. Connecting Google/Microsoft for DATA (Drive/Gmail
 * import) remains a separate, separately-consented integration.
 *
 * All form chrome is inline here (Login owns the visual contract for the
 * auth surface; Signup/Forgot/Reset mirror these class names so the look
 * stays cohesive even though the styles are scoped per page).
 */

import { useEffect, useMemo, useState } from 'react'
import { Section } from '../ui/index.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { useAuth } from '../auth/AuthProvider.jsx'
import { resolvePostLoginDestination } from '../auth/postLogin.js'
import { signInWithPasskey, passkeySupported } from '../auth/passkey.js'
import { clientNavigate, goPostLogin } from '../auth/nav.js'
import SocialButtons from '../components/SocialButtons.jsx'

// Human-readable copy for the ?error= codes the OAuth callback redirects back with.
const OAUTH_ERROR_MESSAGES = {
  provider_not_configured: 'That sign-in provider is not available.',
  oauth_state_missing: 'Your sign-in session expired. Please try again.',
  oauth_state_invalid: 'Your sign-in session was invalid. Please try again.',
  oauth_state_expired: 'Your sign-in session expired. Please try again.',
  oauth_state_mismatch: 'Your sign-in session could not be verified. Please try again.',
  oauth_denied: 'Sign-in was cancelled.',
  oauth_no_code: 'Sign-in did not complete. Please try again.',
  oauth_no_email: 'That account did not share a verified email, so we can’t sign you in.',
  oauth_exchange_failed: 'We couldn’t complete sign-in with that provider. Please try again.',
  email_unverified: 'That provider hasn’t verified this email, so it can’t be linked to your account.',
  email_taken: 'An account with that email already exists — sign in with your password to link it.',
  email_verification_required: 'Please verify your email before signing in.',
}

function oauthErrorFromQuery() {
  try {
    const code = new URL(window.location.href).searchParams.get('error')
    if (code) return OAUTH_ERROR_MESSAGES[code] || 'Sign in failed. Please try again.'
  } catch { /* ignore */ }
  return null
}

// You bring your own email — that external address IS your Vulos account.
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

// Normalise login identifier: trim + lowercase, sent as typed otherwise.
function normaliseEmail(raw) {
  return raw.trim().toLowerCase()
}

function safeReturn() {
  try {
    const url = new URL(window.location.href)
    const raw = url.searchParams.get('return')
    if (raw && raw.startsWith('/') && !raw.startsWith('//')) return raw
  } catch { /* ignore */ }
  // No explicit return target: the destination is resolved post-auth (your
  // OS box, else the hosted-OS console) — never the Workspace bundle.
  return null
}

async function navigateAfterAuth(returnTo) {
  const dest = returnTo || (await resolvePostLoginDestination())
  // dest is an app-relative console path (or, in future, a box origin URL);
  // goPostLogin (auth/nav.js) prefixes the /console basename for in-app paths.
  goPostLogin(dest)
}

// Inline key/passkey glyph — an SVG, not a unicode symbol, so it renders on every
// platform (the ⚿ codepoint is absent from most system + mono fonts → tofu).
function PasskeyIcon() {
  return (
    <svg
      aria-hidden="true"
      className="vc-auth-passkey-key"
      width="17"
      height="17"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <circle cx="8" cy="8" r="5" />
      <path d="M11.5 11.5 L21 21" />
      <path d="M17.5 17.5 L20 15" />
      <path d="M15 15 L17.5 12.5" />
    </svg>
  )
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

export default function Login() {
  const { user, login, refresh, loading: authLoading } = useAuth()

  const [email,        setEmail]        = useState('')
  const [password,     setPassword]     = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [touched,      setTouched]      = useState({ email: false, password: false })
  const [submitting,   setSubmitting]   = useState(false)
  const [submitError,  setSubmitError]  = useState(() => oauthErrorFromQuery())
  const [passkeyBusy,  setPasskeyBusy]  = useState(false)

  // Passkeys are an ADDITIONAL sign-in path (WebAuthn). Only offer the button on
  // browsers that support it; the password form is always the baseline.
  const canPasskey = useMemo(() => passkeySupported(), [])

  const returnTo = useMemo(() => safeReturn(), [])

  useEffect(() => {
    if (!authLoading && user) navigateAfterAuth(returnTo)
  }, [authLoading, user, returnTo])

  // Valid if it's a well-formed email address.
  const emailValid = EMAIL_RE.test(email.trim().toLowerCase())
  const emailError = touched.email && !emailValid
    ? 'Enter the email address you signed up with.'
    : null
  const passwordError = touched.password && password.length < 1
    ? 'Enter your password.'
    : null

  const canSubmit = emailValid && password.length > 0 && !submitting

  async function handleSubmit(e) {
    e.preventDefault()
    setTouched({ email: true, password: true })
    if (!canSubmit) return
    setSubmitError(null)
    setSubmitting(true)
    try {
      await login(normaliseEmail(email), password)
      await navigateAfterAuth(returnTo)
    } catch (err) {
      setSubmitError(err && err.message ? err.message : 'Sign in failed.')
      setSubmitting(false)
    }
  }

  async function handlePasskey() {
    setTouched(t => ({ ...t, email: true }))
    if (!emailValid) {
      setSubmitError('Enter your email address first, then sign in with your passkey.')
      return
    }
    setSubmitError(null)
    setPasskeyBusy(true)
    try {
      await signInWithPasskey(normaliseEmail(email))
      // The server set the vc_session cookie; sync the auth context, then go.
      await refresh()
      await navigateAfterAuth(returnTo)
    } catch (err) {
      // AbortError = the user dismissed the platform prompt; not an error worth shouting.
      const name = err && err.name
      if (name !== 'AbortError' && name !== 'NotAllowedError') {
        setSubmitError(err && err.message ? err.message : 'Passkey sign-in failed.')
      }
      setPasskeyBusy(false)
    }
  }

  const signupHref = returnTo ? `/signup?return=${encodeURIComponent(returnTo)}` : '/signup'

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
            <p className="vc-auth-eyebrow">Vulos account</p>
            <h1 className="vc-auth-title">Welcome back</h1>
            <p className="vc-auth-subtitle">One sign-in for your OS, your apps and your cloud console.</p>
          </div>

          <form noValidate onSubmit={handleSubmit} className="vc-auth-form">
            {submitError && (
              <div
                role="alert"
                id="login-error"
                className="vc-auth-alert"
                aria-live="assertive"
              >
                <span className="vc-auth-alert-tag">Error</span>
                <span className="vc-auth-alert-msg">{submitError}</span>
              </div>
            )}

            <div className="vc-auth-field">
              <div className="vc-auth-label-row">
                <label htmlFor="login-email" className="vc-auth-label">Email</label>
              </div>
              <input
                id="login-email"
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
                aria-describedby={emailError ? 'login-email-err' : undefined}
                className={`vc-auth-input${emailError ? ' has-error' : ''}`}
                autoFocus
              />
              {emailError && (
                <p id="login-email-err" className="vc-auth-fielderr">{emailError}</p>
              )}
            </div>

            <div className="vc-auth-field">
              <div className="vc-auth-label-row">
                <label htmlFor="login-password" className="vc-auth-label">Password</label>
                <a
                  href="/forgot"
                  onClick={e => { e.preventDefault(); clientNavigate('/forgot') }}
                  className="vc-auth-aux"
                >
                  Forgot?
                </a>
              </div>
              <div className="vc-auth-input-wrap">
                <input
                  id="login-password"
                  name="password"
                  type={showPassword ? 'text' : 'password'}
                  autoComplete="current-password"
                  value={password}
                  onChange={e => { setPassword(e.target.value); if (submitError) setSubmitError(null) }}
                  onBlur={() => setTouched(t => ({ ...t, password: true }))}
                  placeholder="Your password"
                  required
                  aria-invalid={passwordError ? 'true' : 'false'}
                  aria-describedby={passwordError ? 'login-password-err' : undefined}
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
              {passwordError && (
                <p id="login-password-err" className="vc-auth-fielderr">{passwordError}</p>
              )}
            </div>

            <button
              type="submit"
              className="vc-auth-submit"
              disabled={!canSubmit}
              aria-busy={submitting ? 'true' : 'false'}
            >
              {submitting ? (<><Spinner size={14} /><span>Signing in…</span></>) : 'Sign in'}
            </button>

            {canPasskey && (
              <>
                <div className="vc-auth-or" role="separator" aria-label="or">
                  <span>or</span>
                </div>
                <button
                  type="button"
                  className="vc-auth-passkey"
                  onClick={handlePasskey}
                  disabled={passkeyBusy || submitting}
                  aria-busy={passkeyBusy ? 'true' : 'false'}
                >
                  {passkeyBusy
                    ? (<><Spinner size={14} /><span>Waiting for passkey…</span></>)
                    : (<><PasskeyIcon /><span>Sign in with a passkey</span></>)}
                </button>
              </>
            )}

            {/* Secondary convenience row: "or continue with" social login.
                Renders nothing unless the deployment configured providers. */}
            <SocialButtons returnTo={returnTo} />

            <div className="vc-auth-foot">
              <span className="vc-auth-foot-text">
                Don&apos;t have an account?{' '}
                <a
                  href={signupHref}
                  onClick={e => { e.preventDefault(); clientNavigate(signupHref) }}
                  className="vc-auth-foot-link"
                >
                  Sign up
                </a>
              </span>
            </div>
          </form>
        </div>
      </div>
    </Section>
  )
}

/* ─── Shared form styling for the auth surface ──────────────
   Identical class set is mirrored by Signup/Forgot/Reset; we
   keep this <style> block here as the canonical source — each
   page renders its own copy in its own <style>, so any single
   page is self-contained and there are no cross-file imports
   beyond the kit + AuthProvider.
──────────────────────────────────────────────────────────── */
export function AuthFormStyles() {
  return (
    <style>{`
      @keyframes vcAuthSpin { to { transform: rotate(360deg); } }
      @keyframes vcAuthRise {
        from { opacity: 0; transform: translateY(8px); }
        to   { opacity: 1; transform: translateY(0); }
      }

      .vc-auth-wrap {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 100%;
      }

      .vc-auth-card {
        width: 100%;
        max-width: 420px;
        background: var(--bg-surface);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius-lg, 16px);
        padding: var(--sp-6, 48px) var(--sp-5, 40px);
        box-shadow: var(--shadow);
      }

      .vc-auth-reveal {
        animation: vcAuthRise 280ms var(--ease, cubic-bezier(.22,1,.36,1)) both;
      }
      @media (prefers-reduced-motion: reduce) {
        .vc-auth-reveal { animation: none; }
      }

      .vc-auth-head {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 6px;
        margin-bottom: var(--sp-4, 32px);
      }
      .vc-auth-brand {
        display: inline-flex;
        align-items: center;
        gap: 12px;
        text-decoration: none;
        margin-bottom: var(--sp-2-5, 20px);
        padding: 2px;
        margin-left: -2px;
        border-radius: var(--radius-sm, 6px);
        transition: opacity var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-brand:hover { opacity: 0.86; }
      .vc-auth-brand:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
      }
      .vc-auth-wordmark {
        font-family: var(--font-mono);
        font-weight: 600;
        font-size: 0.9375rem;
        letter-spacing: -0.01em;
        color: var(--text-primary);
        line-height: 1;
      }
      .vc-auth-wordmark-suffix {
        color: var(--text-faint);
        font-weight: 400;
      }
      .vc-auth-eyebrow {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 500;
        letter-spacing: 0.16em;
        text-transform: uppercase;
        color: var(--text-faint);
        margin: 0;
      }
      .vc-auth-title {
        font-family: var(--font-mono);
        font-size: 1.625rem;
        font-weight: 700;
        letter-spacing: -0.025em;
        color: var(--text-primary);
        line-height: 1.15;
        margin: 4px 0 0;
      }
      .vc-auth-subtitle {
        font-family: var(--font-sans);
        font-size: 0.9375rem;
        color: var(--text-tertiary);
        line-height: 1.55;
        margin: 8px 0 0;
        max-width: none;
      }

      .vc-auth-form {
        display: flex;
        flex-direction: column;
        gap: 16px;
      }

      /* ── Alert (above-form error region) ────────────────── */
      .vc-auth-alert {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 12px 14px;
        border-radius: var(--radius, 10px);
        background: color-mix(in srgb, var(--warn) 10%, var(--bg-elevated));
        border: 1px solid color-mix(in srgb, var(--warn) 35%, var(--border-emphasis));
      }
      .vc-auth-alert-tag {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 600;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--warn);
        padding-top: 1px;
        flex-shrink: 0;
      }
      .vc-auth-alert-msg {
        font-family: var(--font-sans);
        font-size: 0.875rem;
        color: var(--text-secondary);
        line-height: 1.5;
      }

      /* ── Field ─────────────────────────────────────────── */
      .vc-auth-field {
        display: flex;
        flex-direction: column;
        gap: 6px;
      }
      .vc-auth-label-row {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 12px;
      }
      .vc-auth-label {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        font-weight: 500;
        letter-spacing: 0.06em;
        text-transform: uppercase;
        color: var(--text-tertiary);
        line-height: 1.2;
      }
      .vc-auth-aux {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-faint);
        text-decoration: none;
        transition: color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-aux:hover { color: var(--accent); }
      .vc-auth-aux:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
        border-radius: var(--radius-sm, 6px);
      }

      .vc-auth-input-wrap {
        position: relative;
        display: flex;
        align-items: center;
      }

      .vc-auth-input {
        width: 100%;
        min-height: 44px;
        padding: 12px 14px;
        background: var(--bg-elevated);
        color: var(--text-primary);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px);
        font-family: var(--font-mono);
        font-size: 0.9375rem;
        line-height: 1.4;
        letter-spacing: 0;
        outline: none;
        transition:
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          box-shadow var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-input::placeholder { color: var(--text-ghost); }
      .vc-auth-input:hover {
        border-color: var(--border-emphasis);
      }
      .vc-auth-input:focus,
      .vc-auth-input:focus-visible {
        border-color: var(--accent);
        background: var(--bg-elevated);
        box-shadow: var(--focus-ring);
      }
      .vc-auth-input.has-trailing { padding-right: 64px; }
      .vc-auth-input.has-error {
        border-color: var(--danger);
      }
      .vc-auth-input.has-error:focus,
      .vc-auth-input.has-error:focus-visible {
        box-shadow: 0 0 0 3px color-mix(in srgb, var(--danger) 30%, transparent);
      }

      .vc-auth-eye {
        position: absolute;
        right: 4px;
        top: 50%;
        transform: translateY(-50%);
        min-height: 36px;
        padding: 6px 10px;
        background: transparent;
        border: 1px solid transparent;
        border-radius: var(--radius-sm, 6px);
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 500;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--text-faint);
        cursor: pointer;
        transition:
          color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-eye:hover { color: var(--text-secondary); background: rgba(255,255,255,0.04); }
      .vc-auth-eye:focus-visible {
        outline: none;
        color: var(--text-primary);
        box-shadow: var(--focus-ring);
      }

      .vc-auth-fielderr {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--danger);
        margin: 0;
        line-height: 1.4;
      }

      .vc-auth-hint {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-faint);
        margin: 0;
        line-height: 1.5;
      }

      /* ── Submit (the one accent moment) ─────────────────── */
      .vc-auth-submit {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        width: 100%;
        min-height: 48px;
        margin-top: 4px;
        padding: 13px 18px;
        background: var(--accent);
        color: #fff;
        border: 1px solid var(--accent);
        border-radius: var(--radius, 10px);
        font-family: var(--font-mono);
        font-size: 0.9375rem;
        font-weight: 600;
        letter-spacing: 0.01em;
        cursor: pointer;
        transition:
          background var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          transform var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          box-shadow var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-submit:hover:not(:disabled) {
        background: var(--accent-hover);
        border-color: var(--accent-hover);
        transform: translateY(-1px);
        box-shadow: 0 6px 18px color-mix(in srgb, var(--accent) 30%, transparent);
      }
      .vc-auth-submit:active:not(:disabled) {
        transform: translateY(0);
        box-shadow: none;
      }
      .vc-auth-submit:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
      }
      .vc-auth-submit:disabled {
        opacity: 0.5;
        cursor: not-allowed;
        transform: none;
        box-shadow: none;
      }

      /* ── Passkey (secondary auth path) ─────────────────── */
      .vc-auth-or {
        display: flex;
        align-items: center;
        gap: 12px;
        margin: 4px 0;
        color: var(--text-dim, #8a8f98);
        font-family: var(--font-mono);
        font-size: 0.75rem;
        text-transform: uppercase;
        letter-spacing: 0.08em;
      }
      .vc-auth-or::before,
      .vc-auth-or::after {
        content: '';
        flex: 1;
        height: 1px;
        background: var(--border-strong);
      }
      .vc-auth-passkey {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        width: 100%;
        min-height: 48px;
        padding: 13px 18px;
        background: transparent;
        color: var(--text, #e8e8ea);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px);
        font-family: var(--font-mono);
        font-size: 0.9375rem;
        font-weight: 600;
        letter-spacing: 0.01em;
        cursor: pointer;
        transition:
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          transform var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-passkey:hover:not(:disabled) {
        border-color: var(--accent);
        background: color-mix(in srgb, var(--accent) 8%, transparent);
        transform: translateY(-1px);
      }
      .vc-auth-passkey:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
      }
      .vc-auth-passkey:disabled {
        opacity: 0.5;
        cursor: not-allowed;
        transform: none;
      }
      .vc-auth-passkey-key {
        font-size: 1.1rem;
        line-height: 1;
      }

      /* ── Foot ──────────────────────────────────────────── */
      .vc-auth-foot {
        display: flex;
        justify-content: center;
        margin-top: 18px;
      }
      .vc-auth-foot-text {
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        color: var(--text-tertiary);
      }
      .vc-auth-foot-link {
        color: var(--text-primary);
        font-weight: 500;
        text-decoration: none;
        border-bottom: 1px solid var(--border-emphasis);
        transition:
          color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-foot-link:hover {
        color: var(--accent);
        border-bottom-color: var(--accent);
      }
      .vc-auth-foot-link:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
        border-radius: var(--radius-sm, 6px);
      }

      /* ── Inline ok / flash banner (used by Forgot/Reset) ── */
      .vc-auth-ok {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 14px 16px;
        border-radius: var(--radius, 10px);
        background: color-mix(in srgb, var(--good) 8%, var(--bg-elevated));
        border: 1px solid color-mix(in srgb, var(--good) 30%, var(--border-emphasis));
      }
      .vc-auth-ok-tag {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 600;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--good);
        padding-top: 1px;
        flex-shrink: 0;
      }
      .vc-auth-ok-msg {
        font-family: var(--font-sans);
        font-size: 0.875rem;
        color: var(--text-secondary);
        line-height: 1.55;
      }

      /* ── Password strength meter ───────────────────────── */
      .vc-auth-meter {
        display: grid;
        grid-template-columns: repeat(4, 1fr);
        gap: 4px;
        margin-top: 2px;
      }
      .vc-auth-meter-seg {
        height: 3px;
        border-radius: 99px;
        background: var(--border-strong);
        transition: background var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-meter-seg.lit-weak   { background: var(--danger); }
      .vc-auth-meter-seg.lit-okay   { background: var(--warn); }
      .vc-auth-meter-seg.lit-good   { background: var(--accent); }
      .vc-auth-meter-seg.lit-strong { background: var(--good); }
      .vc-auth-meter-label {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        margin-top: 6px;
        line-height: 1.2;
      }
      .vc-auth-meter-label.weak   { color: var(--danger); }
      .vc-auth-meter-label.okay   { color: var(--warn); }
      .vc-auth-meter-label.good   { color: var(--accent); }
      .vc-auth-meter-label.strong { color: var(--good); }

      /* ── Responsive ────────────────────────────────────── */
      @media (max-width: 480px) {
        .vc-auth-card {
          padding: var(--sp-5, 40px) var(--sp-3, 24px);
          border-radius: var(--radius, 10px);
        }
        .vc-auth-title { font-size: 1.4375rem; }
      }
      @media (max-width: 360px) {
        .vc-auth-card {
          padding: var(--sp-4, 32px) 16px;
        }
      }
    `}</style>
  )
}
