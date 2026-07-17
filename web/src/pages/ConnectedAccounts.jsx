/**
 * vulos-cloud — Connected accounts (social login) settings surface
 *
 * Account model (LOCKED, 2026-07): email+password is the mandatory centre of every
 * Vulos account; social logins (Google / Microsoft / GitHub / Discord) are
 * convenience links layered on top and MULTIPLE providers may be linked to one
 * account. This page lets the signed-in user:
 *   - see which providers are linked (GET /api/auth/oauth/identities)
 *   - connect another  (GET /api/auth/oauth/<id>/start?mode=connect — the callback
 *     binds it to THIS account and redirects back here)
 *   - disconnect one   (DELETE /api/auth/oauth/identities/<id>)
 *
 * Disconnecting is always safe: the account still has its password, so it can
 * never be stranded without a sign-in method.
 */

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Section } from '../ui/index.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { clientReplace } from '../auth/nav.js'
import { toFullPath } from '../router.jsx'
import { AuthFormStyles } from './Login.jsx'

const IDENTITIES_URL = '/api/auth/oauth/identities'

const CONNECT_ERRORS = {
  already_linked: 'That account is already linked to a different Vulos account.',
  connect_failed: 'Could not connect that account. Please try again.',
  not_authenticated: 'Your session expired. Sign in again to connect an account.',
}

function titleCase(s) {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s
}

function readFlash() {
  try {
    const q = new URL(window.location.href).searchParams
    return { connected: q.get('connected') || '', error: q.get('error') || '' }
  } catch {
    return { connected: '', error: '' }
  }
}

export default function ConnectedAccounts() {
  const flash = useMemo(() => readFlash(), [])
  const [identities, setIdentities] = useState([])
  const [available, setAvailable] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [busy, setBusy] = useState('')

  // No synchronous setState before the first await, so this is safe to call
  // straight from the mount effect (avoids cascading renders).
  const load = useCallback(async () => {
    try {
      const res = await fetch(IDENTITIES_URL, {
        credentials: 'include',
        headers: { Accept: 'application/json' },
        cache: 'no-store',
      })
      if (res.status === 401) { clientReplace('/login?return=/account/social'); return }
      if (!res.ok) throw new Error('Could not load your connected accounts.')
      const body = await res.json().catch(() => null)
      setIdentities((body && Array.isArray(body.identities) ? body.identities : []))
      setAvailable((body && Array.isArray(body.available) ? body.available : []))
      setError(null)
    } catch (err) {
      setError(err && err.message ? err.message : 'Could not load your connected accounts.')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    let live = true
    ;(async () => { if (live) await load() })()
    return () => { live = false }
  }, [load])

  const linkedIds = useMemo(() => new Set(identities.map((i) => i.provider)), [identities])
  const connectable = available.filter((p) => !linkedIds.has(p.id))

  function connect(id) {
    // Full pathname (under the /console basename) so the post-connect redirect
    // lands back on this console surface.
    const ret = encodeURIComponent(toFullPath('/account/social'))
    window.location.assign(`/api/auth/oauth/${encodeURIComponent(id)}/start?mode=connect&return=${ret}`)
  }

  async function disconnect(provider) {
    setBusy(provider)
    setError(null)
    try {
      const res = await fetch(`${IDENTITIES_URL}/${encodeURIComponent(provider)}`, {
        method: 'DELETE',
        credentials: 'include',
        headers: { Accept: 'application/json' },
      })
      if (!res.ok && res.status !== 404) throw new Error('Could not disconnect that account.')
      await load()
    } catch (err) {
      setError(err && err.message ? err.message : 'Could not disconnect that account.')
    } finally {
      setBusy('')
    }
  }

  return (
    <Section slim>
      <AuthFormStyles />
      <style>{`
        .vc-ca-list { display: flex; flex-direction: column; gap: 10px; margin: 4px 0 18px; }
        .vc-ca-row {
          display: flex; align-items: center; justify-content: space-between; gap: 12px;
          width: 100%; min-height: 52px; padding: 10px 16px;
          border: 1px solid var(--border-strong, #2a2a2e); border-radius: var(--radius, 10px);
          background: transparent;
        }
        .vc-ca-name { font-family: var(--font-mono, monospace); font-weight: 600; color: var(--text, #e8e8ea); }
        .vc-ca-sub { display: block; font-size: 0.75rem; color: var(--text-dim, #8a8f98); font-weight: 400; margin-top: 2px; }
        .vc-ca-btn {
          min-height: 40px; padding: 8px 16px; cursor: pointer;
          border-radius: var(--radius, 10px); border: 1px solid var(--border-strong, #2a2a2e);
          background: transparent; color: var(--text, #e8e8ea);
          font-family: var(--font-mono, monospace); font-size: 0.8125rem; font-weight: 600;
          transition: border-color 160ms, background 160ms;
        }
        .vc-ca-btn:hover { border-color: var(--accent, #6366f1); background: color-mix(in srgb, var(--accent,#6366f1) 8%, transparent); }
        .vc-ca-btn[disabled] { opacity: 0.5; cursor: default; }
        .vc-ca-btn.is-danger:hover { border-color: #e5484d; background: color-mix(in srgb, #e5484d 10%, transparent); }
        .vc-ca-empty { color: var(--text-dim, #8a8f98); font-size: 0.875rem; margin: 4px 0 16px; }
        .vc-ca-ok {
          margin-bottom: 14px; padding: 10px 14px; border-radius: var(--radius, 10px);
          border: 1px solid color-mix(in srgb, #30a46c 45%, transparent);
          background: color-mix(in srgb, #30a46c 12%, transparent); color: var(--text, #e8e8ea); font-size: 0.875rem;
        }
      `}</style>
      <div className="vc-auth-wrap">
        <div className="vc-auth-card">
          <div className="vc-auth-head">
            <span className="vc-auth-brand" aria-hidden="true">
              <LogoMark size={36} tone="on-dark" />
              <span className="vc-auth-wordmark">vulos<span className="vc-auth-wordmark-suffix">.org</span></span>
            </span>
            <p className="vc-auth-eyebrow">Account settings</p>
            <h1 className="vc-auth-title">Connected accounts</h1>
            <p className="vc-auth-subtitle">
              Link Google, Microsoft, GitHub or Discord for one-tap sign-in. Your
              email and password always stay active — disconnecting a provider never
              locks you out.
            </p>
          </div>

          {flash.connected && (
            <div className="vc-ca-ok" role="status">
              Connected {titleCase(flash.connected)} to your account.
            </div>
          )}
          {flash.error && (
            <div role="alert" className="vc-auth-alert" aria-live="assertive">
              <span className="vc-auth-alert-tag">Error</span>
              <span className="vc-auth-alert-msg">{CONNECT_ERRORS[flash.error] || 'Could not connect that account.'}</span>
            </div>
          )}
          {error && (
            <div role="alert" className="vc-auth-alert" aria-live="assertive">
              <span className="vc-auth-alert-tag">Error</span>
              <span className="vc-auth-alert-msg">{error}</span>
            </div>
          )}

          {loading ? (
            <p className="vc-ca-empty">Loading…</p>
          ) : (
            <>
              <h2 className="vc-auth-label" style={{ marginTop: 8 }}>Linked</h2>
              {identities.length === 0 ? (
                <p className="vc-ca-empty">No social providers linked yet.</p>
              ) : (
                <div className="vc-ca-list">
                  {identities.map((i) => (
                    <div key={i.provider} className="vc-ca-row" data-provider={i.provider}>
                      <span className="vc-ca-name">
                        {titleCase(i.provider)}
                        {i.email ? <span className="vc-ca-sub">{i.email}</span> : null}
                      </span>
                      <button
                        type="button"
                        className="vc-ca-btn is-danger"
                        onClick={() => disconnect(i.provider)}
                        disabled={busy === i.provider}
                      >
                        {busy === i.provider ? 'Removing…' : 'Disconnect'}
                      </button>
                    </div>
                  ))}
                </div>
              )}

              {connectable.length > 0 && (
                <>
                  <h2 className="vc-auth-label" style={{ marginTop: 8 }}>Available to connect</h2>
                  <div className="vc-ca-list">
                    {connectable.map((p) => (
                      <div key={p.id} className="vc-ca-row" data-provider={p.id}>
                        <span className="vc-ca-name">{p.name}</span>
                        <button type="button" className="vc-ca-btn" onClick={() => connect(p.id)}>
                          Connect
                        </button>
                      </div>
                    ))}
                  </div>
                </>
              )}
            </>
          )}
        </div>
      </div>
    </Section>
  )
}
