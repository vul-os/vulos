/**
 * vulos-cloud — Set your Vulos password (mandatory gate for social sign-ups)
 *
 * Account model (LOCKED, 2026-07): email+password is the mandatory CENTRE of every
 * account. A social sign-up (Sign in with Google/Microsoft) creates an account with
 * NO usable password and is routed here by the OAuth callback. The account is NOT
 * usable until a Vulos password is set (POST /api/auth/password/set-initial), which
 * also finalises the account (mints one-time recovery codes). GET /api/auth/me
 * reports `password_setup_required: true` until then.
 *
 * States:
 *   - not authenticated → bounce to /login
 *   - already has a password → nothing to do → continue onward
 *   - password-less → the form; on success show recovery codes, then continue
 */

import { useEffect, useMemo, useState } from 'react'
import { Section } from '../ui/index.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { useAuth } from '../auth/AuthProvider.jsx'
import { resolvePostLoginDestination } from '../auth/postLogin.js'
import { clientNavigate, goPostLogin } from '../auth/nav.js'
import { AuthFormStyles } from './Login.jsx'

const SET_INITIAL_URL = '/api/auth/password/set-initial'

async function goOnward() {
  const dest = await resolvePostLoginDestination()
  goPostLogin(dest)
}

export default function SetPassword() {
  const { user, loading, refresh } = useAuth()

  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState(null)
  const [codes, setCodes] = useState(null)

  // Gate: only a password-less authenticated account belongs here.
  useEffect(() => {
    if (loading) return
    if (!user) { clientNavigate('/login'); return }
    // If a password already exists (not a fresh social sign-up), move on. Never
    // trap a normal account on this page. Only redirect BEFORE a successful set
    // (codes === null) so the recovery-codes screen isn't skipped.
    if (user.password_setup_required === false && codes === null) { void goOnward() }
  }, [loading, user, codes])

  const passwordValid = password.length >= 12
  const confirmValid = confirm.length > 0 && confirm === password
  const canSubmit = passwordValid && confirmValid && !submitting

  const passwordError = useMemo(() => {
    if (!password) return null
    if (password.length < 12) return 'Use at least 12 characters.'
    return null
  }, [password])

  async function handleSubmit(e) {
    e.preventDefault()
    if (!canSubmit) return
    setError(null)
    setSubmitting(true)
    try {
      const res = await fetch(SET_INITIAL_URL, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ password }),
      })
      const body = await res.json().catch(() => null)
      if (!res.ok) {
        if (res.status === 422) throw new Error('That password appears in a known breach corpus — choose a different one.')
        if (res.status === 409) { await refresh(); await goOnward(); return }
        throw new Error((body && (body.message || body.error)) || 'Could not set your password.')
      }
      // Success: the account is now usable. Refresh session state and, if the
      // server minted recovery codes, show them once before continuing.
      await refresh()
      if (body && Array.isArray(body.recovery_codes) && body.recovery_codes.length) {
        setCodes(body.recovery_codes)
      } else {
        await goOnward()
      }
    } catch (err) {
      setError(err && err.message ? err.message : 'Could not set your password.')
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
            <p className="vc-auth-eyebrow">Finish setup</p>
            <h1 className="vc-auth-title">Set your password</h1>
            <p className="vc-auth-subtitle">
              Every Vulos account has its own password — it’s the key you own, independent of Google or Microsoft.
              Set one now to finish creating your account.
            </p>
          </div>

          {codes ? (
            <div className="vc-auth-form">
              <div className="vc-auth-ok" role="status">
                <span className="vc-auth-ok-tag">Saved</span>
                <span className="vc-auth-ok-msg">
                  Your password is set. Save these one-time recovery codes somewhere safe — each works once if you ever lose access.
                </span>
              </div>
              <ul style={{
                listStyle: 'none', margin: 0, padding: '12px 14px', display: 'grid',
                gridTemplateColumns: '1fr 1fr', gap: 8, background: 'var(--bg-elevated)',
                border: '1px solid var(--border-strong)', borderRadius: 10,
                fontFamily: 'var(--font-mono)', fontSize: '0.9rem',
              }}>
                {codes.map((c) => <li key={c} data-testid="recovery-code">{c}</li>)}
              </ul>
              <button type="button" className="vc-auth-submit" onClick={() => { void goOnward() }}>
                Continue
              </button>
            </div>
          ) : (
            <form noValidate onSubmit={handleSubmit} className="vc-auth-form">
              {error && (
                <div role="alert" className="vc-auth-alert" aria-live="assertive">
                  <span className="vc-auth-alert-tag">Error</span>
                  <span className="vc-auth-alert-msg">{error}</span>
                </div>
              )}

              <div className="vc-auth-field">
                <label htmlFor="set-password" className="vc-auth-label">New password</label>
                <div className="vc-auth-input-wrap">
                  <input
                    id="set-password"
                    name="new-password"
                    type={showPassword ? 'text' : 'password'}
                    autoComplete="new-password"
                    value={password}
                    onChange={(e) => { setPassword(e.target.value); if (error) setError(null) }}
                    placeholder="At least 12 characters"
                    className={`vc-auth-input has-trailing${passwordError ? ' has-error' : ''}`}
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
                {passwordError && <p className="vc-auth-fielderr">{passwordError}</p>}
              </div>

              <div className="vc-auth-field">
                <label htmlFor="set-password-confirm" className="vc-auth-label">Confirm password</label>
                <input
                  id="set-password-confirm"
                  name="confirm-password"
                  type={showPassword ? 'text' : 'password'}
                  autoComplete="new-password"
                  value={confirm}
                  onChange={(e) => { setConfirm(e.target.value); if (error) setError(null) }}
                  placeholder="Re-enter your password"
                  className={`vc-auth-input${confirm && !confirmValid ? ' has-error' : ''}`}
                  required
                />
                {confirm && !confirmValid && <p className="vc-auth-fielderr">Passwords don’t match.</p>}
              </div>

              <button type="submit" className="vc-auth-submit" disabled={!canSubmit} aria-busy={submitting}>
                {submitting ? 'Saving…' : 'Set password'}
              </button>
            </form>
          )}
        </div>
      </div>
    </Section>
  )
}
