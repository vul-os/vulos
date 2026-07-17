/**
 * /oauth/consent — OAuth2 / OIDC consent screen (OAUTHP-01)
 *
 * The backend /oauth/authorize endpoint validates the request (client_id,
 * exact redirect_uri match, scopes, PKCE S256) and — for a logged-in Vulos
 * user — redirects the browser here with the original query string. This page:
 *
 *   1. fetches the VALIDATED display info from GET /api/oauth/authorize/info
 *      (401 → bounce to /login?return=… ; 400 → show the error);
 *   2. shows the requesting app, the Vulos account, and the requested scopes;
 *   3. posts the user's Approve/Deny to POST /api/oauth/authorize/decision and
 *      navigates the browser to the returned redirect (code or error → client).
 *
 * The raw OAuth params (code_challenge, state, nonce, …) are read from the URL
 * and echoed back verbatim in the decision body — this page is purely a relay
 * for the user's consent; it never mints anything itself.
 *
 * Design: OSS-native — design tokens only, mono labels, accent only on Approve.
 */

import { useState, useEffect, useCallback } from 'react'
import { clientReplace, currentAppPath } from '../auth/nav.js'

const SCOPE_LABELS = {
  openid: { title: 'Sign you in', desc: 'Confirm your Vulos identity' },
  email: { title: 'Email address', desc: 'Your email and whether it’s verified' },
  'mail.read': { title: 'Read your mail', desc: 'Read messages in your connected mailbox' },
  'mail.send': { title: 'Send mail', desc: 'Send mail on your behalf' },
}

function paramsFromURL() {
  const q = new URLSearchParams(window.location.search)
  const keys = [
    'response_type', 'client_id', 'redirect_uri', 'scope', 'state',
    'code_challenge', 'code_challenge_method', 'nonce',
  ]
  const out = {}
  for (const k of keys) out[k] = q.get(k) || ''
  return out
}

export default function Consent() {
  const [params] = useState(paramsFromURL)
  const [info, setInfo] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  // Load validated display info. 401 → login (preserving this URL).
  useEffect(() => {
    const qs = new URLSearchParams(params).toString()
    fetch(`/api/oauth/authorize/info?${qs}`, {
      credentials: 'include',
      headers: { Accept: 'application/json' },
    })
      .then(async (res) => {
        if (res.status === 401) {
          clientReplace(`/login?return=${encodeURIComponent(currentAppPath())}`)
          return null
        }
        const body = await res.json().catch(() => ({}))
        if (!res.ok) throw new Error(body?.error || `HTTP ${res.status}`)
        return body
      })
      .then((body) => { if (body) setInfo(body) })
      .catch((err) => setError(err.message))
  }, [params])

  const decide = useCallback(async (approve) => {
    setBusy(true)
    setError('')
    try {
      const res = await fetch('/api/oauth/authorize/decision', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ ...params, approve }),
      })
      const body = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(body?.error || `HTTP ${res.status}`)
      if (body.redirect) {
        window.location.assign(body.redirect)
      } else {
        throw new Error('No redirect returned')
      }
    } catch (err) {
      setError(err.message)
      setBusy(false)
    }
  }, [params])

  return (
    <div className="cns-wrap">
      <style>{CONSENT_CSS}</style>
      <div className="cns-card">
        <div className="cns-logo" aria-hidden="true">V</div>

        {error && <div className="cns-notice cns-notice--warn" role="alert">{error}</div>}

        {!info && !error && <div className="cns-loading">Loading…</div>}

        {info && (
          <>
            <h1 className="cns-title">
              <strong>{info.client_name}</strong> wants to access your Vulos account
            </h1>
            <p className="cns-sub">
              Signed in as <span className="cns-mono">{info.user_email}</span>
            </p>

            <div className="cns-scopes">
              <span className="cns-scopes-label">This will allow {info.client_name} to:</span>
              <ul className="cns-scope-list">
                {(info.scopes || []).map((s) => {
                  const meta = SCOPE_LABELS[s] || { title: s, desc: s }
                  return (
                    <li key={s} className="cns-scope">
                      <span className="cns-check" aria-hidden="true">✓</span>
                      <span>
                        <span className="cns-scope-title">{meta.title}</span>
                        <span className="cns-scope-desc">{meta.desc}</span>
                      </span>
                    </li>
                  )
                })}
              </ul>
            </div>

            <p className="cns-redirect">
              You’ll be returned to <span className="cns-mono">{info.redirect_uri}</span>
            </p>

            <div className="cns-actions">
              <button className="cns-btn cns-btn--ghost" onClick={() => decide(false)} disabled={busy}>
                Deny
              </button>
              <button className="cns-btn cns-btn--primary" onClick={() => decide(true)} disabled={busy}>
                {busy ? 'Please wait…' : 'Allow access'}
              </button>
            </div>

            <p className="cns-fine">
              Only allow access if you trust this application. You can revoke access at any time from your Vulos account.
            </p>
          </>
        )}
      </div>
    </div>
  )
}

const CONSENT_CSS = `
.cns-wrap { min-height: 100dvh; display: flex; align-items: center; justify-content: center; padding: var(--sp-4, 24px); font-family: var(--font-mono); color: var(--text); background: var(--bg, var(--bg-base)); }
.cns-card { width: 100%; max-width: 440px; border: 1px solid var(--border-strong, var(--border)); border-radius: var(--radius-lg, 16px); background: var(--bg-elev, var(--bg-surface)); padding: var(--sp-5, 32px); }
.cns-logo { width: 44px; height: 44px; border-radius: 12px; background: var(--accent); color: white; display: flex; align-items: center; justify-content: center; font-weight: 700; font-size: 1.4rem; margin: 0 auto var(--sp-4, 20px); }
.cns-title { font-size: 1.2rem; line-height: 1.4; text-align: center; margin: 0 0 6px; font-weight: 500; }
.cns-title strong { font-weight: 700; }
.cns-sub { text-align: center; color: var(--text-muted); font-size: var(--text-sm, .875rem); margin: 0 0 var(--sp-4, 24px); }
.cns-mono { font-family: var(--font-mono); font-size: .8125rem; word-break: break-all; }

.cns-scopes { border: 1px solid var(--border); border-radius: var(--radius, 12px); padding: var(--sp-3, 16px); margin-bottom: var(--sp-3, 16px); }
.cns-scopes-label { display: block; font-size: var(--text-sm, .875rem); color: var(--text-muted); margin-bottom: 10px; }
.cns-scope-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 10px; }
.cns-scope { display: flex; gap: 10px; align-items: flex-start; }
.cns-check { color: var(--accent); font-weight: 700; line-height: 1.3; }
.cns-scope-title { display: block; font-size: .9rem; }
.cns-scope-desc { display: block; color: var(--text-muted); font-size: var(--text-xs, .75rem); }

.cns-redirect { text-align: center; color: var(--text-faint); font-size: var(--text-xs, .75rem); margin: 0 0 var(--sp-4, 24px); }
.cns-actions { display: flex; gap: 10px; }
.cns-btn { flex: 1; border: 1px solid var(--border-strong, var(--border)); background: transparent; color: var(--text); border-radius: var(--radius-sm, 8px); padding: 12px 16px; font-family: var(--font-mono); font-size: .9rem; cursor: pointer; min-height: 46px; }
.cns-btn--primary { background: var(--accent); border-color: var(--accent); color: white; font-weight: 600; }
.cns-btn--primary:hover { background: var(--accent-hover, var(--accent)); }
.cns-btn--ghost:hover { background: var(--bg-hover, transparent); }
.cns-btn:disabled { opacity: .55; cursor: not-allowed; }

.cns-fine { color: var(--text-faint); font-size: var(--text-xs, .72rem); text-align: center; margin: var(--sp-3, 16px) 0 0; line-height: 1.5; }
.cns-loading { text-align: center; color: var(--text-muted); padding: var(--sp-5, 32px); }
.cns-notice { border: 1px solid var(--border-strong, var(--border)); border-radius: var(--radius-sm, 8px); padding: 10px 12px; font-size: var(--text-sm, .875rem); margin-bottom: var(--sp-3, 16px); }
.cns-notice--warn { border-color: var(--warn); color: var(--warn); }
`
