/**
 * vulos-cloud — Mandatory-email entry for social sign-in (Vulos as OAuth CLIENT)
 *
 * Account model (LOCKED, 2026-07): every Vulos account is keyed by an email
 * address. Some social providers (e.g. a GitHub account with no verified email,
 * or a Discord login without the email scope granted) return NO email. When that
 * happens the OAuth callback creates/links NOTHING — it redirects here with a
 * signed token and forces the user to type an email before the account is created
 * or linked.
 *
 * POST /api/auth/oauth/complete-email {email_token, email} resolves it:
 *   - step:"set_password" → brand-new (or password-less) account → set a password
 *   - step:"link"         → the email matches an existing account → prove its
 *                            password on /onboarding/link-account
 *   - step:"ok"/2FA       → already-linked account → sign in / 2FA
 *
 * Query params: ?provider=<id>&token=<signed email-token>
 */

import { useEffect, useMemo, useState } from 'react'
import { Section } from '../ui/index.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { useAuth } from '../auth/AuthProvider.jsx'
import { resolvePostLoginDestination } from '../auth/postLogin.js'
import { clientNavigate, goPostLogin } from '../auth/nav.js'
import { AuthFormStyles } from './Login.jsx'

const COMPLETE_URL = '/api/auth/oauth/complete-email'

async function goOnward() {
  const dest = await resolvePostLoginDestination()
  goPostLogin(dest)
}

function readParams() {
  try {
    const q = new URL(window.location.href).searchParams
    return { provider: q.get('provider') || '', token: q.get('token') || '' }
  } catch {
    return { provider: '', token: '' }
  }
}

function looksLikeEmail(s) {
  const at = s.indexOf('@')
  return at > 0 && at < s.length - 1 && !/\s/.test(s) && s.indexOf('.', at) > at
}

export default function OAuthEmail() {
  const { refresh } = useAuth()
  const params = useMemo(() => readParams(), [])

  const [email, setEmail] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState(null)

  // Without a token there is nothing to complete — bounce to /login.
  useEffect(() => {
    if (!params.token) clientNavigate('/login')
  }, [params.token])

  const providerName = params.provider
    ? params.provider.charAt(0).toUpperCase() + params.provider.slice(1)
    : 'your provider'

  const canSubmit = looksLikeEmail(email.trim()) && !!params.token && !submitting

  async function handleSubmit(e) {
    e.preventDefault()
    if (!canSubmit) return
    setError(null)
    setSubmitting(true)
    try {
      const res = await fetch(COMPLETE_URL, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ email_token: params.token, email: email.trim() }),
      })
      const body = await res.json().catch(() => null)
      if (!res.ok) {
        if (res.status === 400) throw new Error('Enter a valid email address, then try again.')
        if (res.status === 409) throw new Error('An account with that email already exists — sign in to link it.')
        throw new Error((body && (body.message || body.error)) || 'Could not complete sign-in.')
      }
      const step = body && body.step
      if (step === 'set_password') { clientNavigate('/onboarding/set-password'); return }
      if (step === 'link') {
        const q = new URLSearchParams({
          provider: body.provider || params.provider,
          email: body.email || email.trim(),
          token: body.link_token || '',
        })
        clientNavigate('/onboarding/link-account?' + q.toString())
        return
      }
      if (step === 'totp_required') { clientNavigate('/2fa'); return }
      if (step === 'passkey_2fa_required') { clientNavigate('/login?step=passkey'); return }
      await refresh()
      await goOnward()
    } catch (err) {
      setError(err && err.message ? err.message : 'Could not complete sign-in.')
      setSubmitting(false)
    }
  }

  return (
    <Section slim>
      <AuthFormStyles />
      <div className="vc-auth-wrap">
        <div className="vc-auth-card vc-auth-reveal">
          <div className="vc-auth-head">
            <span className="vc-auth-brand" aria-hidden="true">
              <LogoMark size={36} tone="on-dark" />
              <span className="vc-auth-wordmark">vulos<span className="vc-auth-wordmark-suffix">.org</span></span>
            </span>
            <p className="vc-auth-eyebrow">One more thing</p>
            <h1 className="vc-auth-title">What’s your email?</h1>
            <p className="vc-auth-subtitle">
              {providerName} didn’t share an email address with us, and every Vulos
              account needs one. Enter the email you’d like to use — you’ll set a
              password next.
            </p>
          </div>

          <form noValidate onSubmit={handleSubmit} className="vc-auth-form">
            {error && (
              <div role="alert" className="vc-auth-alert" aria-live="assertive">
                <span className="vc-auth-alert-tag">Error</span>
                <span className="vc-auth-alert-msg">{error}</span>
              </div>
            )}

            <div className="vc-auth-field">
              <label htmlFor="oauth-email" className="vc-auth-label">Email address</label>
              <div className="vc-auth-input-wrap">
                <input
                  id="oauth-email"
                  name="email"
                  type="email"
                  inputMode="email"
                  autoComplete="email"
                  value={email}
                  onChange={(e) => { setEmail(e.target.value); if (error) setError(null) }}
                  placeholder="you@example.com"
                  className="vc-auth-input"
                  required
                  autoFocus
                />
              </div>
            </div>

            <button type="submit" className="vc-auth-submit" disabled={!canSubmit} aria-busy={submitting}>
              {submitting ? 'Continuing…' : 'Continue'}
            </button>

            <div className="vc-auth-foot">
              <span className="vc-auth-foot-text">
                Changed your mind?{' '}
                <a
                  href="/login"
                  onClick={(e) => { e.preventDefault(); clientNavigate('/login') }}
                  className="vc-auth-foot-link"
                >
                  Back to sign in
                </a>
              </span>
            </div>
          </form>
        </div>
      </div>
    </Section>
  )
}
