/**
 * vulos-cloud — Link a social login to an existing account (safe linking)
 *
 * Account model (LOCKED, 2026-07): a social login (Sign in with Google/Microsoft)
 * whose VERIFIED email matches an EXISTING Vulos account must NEVER silently take it
 * over. The OAuth callback issues no session and redirects here with a signed
 * link-token. This page proves ownership of the existing account by asking for its
 * password, then POSTs /api/auth/oauth/link/confirm — the server links the social
 * identity only after the password verifies, and signs the user in (honouring 2FA).
 *
 * Query params: ?provider=<id>&email=<addr>&token=<signed link-token>
 */

import { useEffect, useMemo, useState } from 'react'
import { Section } from '../ui/index.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { useAuth } from '../auth/AuthProvider.jsx'
import { resolvePostLoginDestination } from '../auth/postLogin.js'
import { clientNavigate, goPostLogin } from '../auth/nav.js'
import { AuthFormStyles } from './Login.jsx'

const LINK_CONFIRM_URL = '/api/auth/oauth/link/confirm'

async function goOnward() {
  const dest = await resolvePostLoginDestination()
  goPostLogin(dest)
}

function readParams() {
  try {
    const q = new URL(window.location.href).searchParams
    return { provider: q.get('provider') || '', email: q.get('email') || '', token: q.get('token') || '' }
  } catch {
    return { provider: '', email: '', token: '' }
  }
}

export default function LinkAccount() {
  const { refresh } = useAuth()
  const params = useMemo(() => readParams(), [])

  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState(null)

  // Without a token there is nothing to link — bounce to /login.
  useEffect(() => {
    if (!params.token) clientNavigate('/login')
  }, [params.token])

  const providerName = params.provider
    ? params.provider.charAt(0).toUpperCase() + params.provider.slice(1)
    : 'your provider'

  const canSubmit = password.length > 0 && !!params.token && !submitting

  async function handleSubmit(e) {
    e.preventDefault()
    if (!canSubmit) return
    setError(null)
    setSubmitting(true)
    try {
      const res = await fetch(LINK_CONFIRM_URL, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ link_token: params.token, password }),
      })
      const body = await res.json().catch(() => null)
      if (!res.ok) {
        if (res.status === 401) throw new Error('Incorrect password for that account.')
        if (res.status === 409) throw new Error('That social account is already linked elsewhere.')
        if (res.status === 400) throw new Error('This link request has expired — please sign in with Google/Microsoft again.')
        throw new Error((body && (body.message || body.error)) || 'Could not link your account.')
      }
      // Linked. Honour any 2FA step the account requires.
      if (body && body.step === 'totp_required') { clientNavigate('/2fa'); return }
      if (body && body.step === 'passkey_2fa_required') { clientNavigate('/login?step=passkey'); return }
      if (body && body.step === 'email_verification_required') { clientNavigate('/login?error=email_verification_required'); return }
      await refresh()
      await goOnward()
    } catch (err) {
      setError(err && err.message ? err.message : 'Could not link your account.')
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
            <p className="vc-auth-eyebrow">Link accounts</p>
            <h1 className="vc-auth-title">Confirm it’s you</h1>
            <p className="vc-auth-subtitle">
              A Vulos account already exists for {params.email ? <strong>{params.email}</strong> : 'this email'}.
              Enter that account’s password to link {providerName} sign-in to it. We never link automatically.
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
              <label htmlFor="link-password" className="vc-auth-label">Your Vulos password</label>
              <div className="vc-auth-input-wrap">
                <input
                  id="link-password"
                  name="password"
                  type={showPassword ? 'text' : 'password'}
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => { setPassword(e.target.value); if (error) setError(null) }}
                  placeholder="Password for this account"
                  className="vc-auth-input has-trailing"
                  required
                  autoFocus
                />
                <button
                  type="button"
                  onClick={() => setShowPassword((s) => !s)}
                  className="vc-auth-eye"
                  aria-label={showPassword ? 'Hide password' : 'Show password'}
                  aria-pressed={showPassword}
                >
                  {showPassword ? 'Hide' : 'Show'}
                </button>
              </div>
            </div>

            <button type="submit" className="vc-auth-submit" disabled={!canSubmit} aria-busy={submitting}>
              {submitting ? 'Linking…' : `Link ${providerName}`}
            </button>

            <div className="vc-auth-foot">
              <span className="vc-auth-foot-text">
                Not your account?{' '}
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
