/**
 * console/pages/Enroll.jsx — /enroll
 *
 * Approve (or deny) a NEW box joining your account. A box that boots the Vulos OS
 * displays a short user code; you enter it here and approve it, which binds the
 * device to your account, mints its device certificate, and creates its fleet row.
 *
 * API (session-authed; the account is derived server-side):
 *   POST /enroll/approve  { user_code }  → 204 on success
 *   POST /enroll/deny     { user_code }  → 204 on success
 *
 * Adapted from vulos-cloud's Enroll.jsx onto management's device-grant flow.
 */

import { useState } from 'react'
import { Section, Card, Button } from '../../ui/index.jsx'
import { toFullPath } from '../../router.jsx'

async function postCode(url, userCode) {
  const res = await fetch(url, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ user_code: userCode }),
  })
  if (res.status === 204) return
  const text = await res.text()
  let body = null
  try { body = text ? JSON.parse(text) : null } catch { /* non-JSON */ }
  const msg = (body && (body.error || body.message)) || `HTTP ${res.status}`
  const err = new Error(msg)
  err.status = res.status
  throw err
}

function normalise(v) {
  return v.toUpperCase().replace(/[^A-Z0-9-]/g, '').slice(0, 12)
}

export default function Enroll() {
  const [code, setCode] = useState('')
  const [state, setState] = useState('idle') // idle | approving | denying | approved | denied
  const [error, setError] = useState(null)

  const busy = state === 'approving' || state === 'denying'
  const done = state === 'approved' || state === 'denied'

  async function run(action) {
    if (!code.trim() || busy) return
    setError(null)
    setState(action === 'approve' ? 'approving' : 'denying')
    try {
      await postCode(action === 'approve' ? '/enroll/approve' : '/enroll/deny', code.trim())
      setState(action === 'approve' ? 'approved' : 'denied')
    } catch (err) {
      setError(err.message)
      setState('idle')
    }
  }

  function reset() {
    setCode(''); setError(null); setState('idle')
  }

  return (
    <Section slim>
      <style>{STYLES}</style>
      <div className="enr-header">
        <span className="enr-eyebrow">Operate</span>
        <h1 className="enr-title">Enroll a box</h1>
        <p className="enr-sub">
          Boot the Vulos OS on your machine. It will display a short code — enter it
          below to bind that box to your account.
        </p>
      </div>

      <div className="enr-grid">
        <Card hover={false}>
          {done ? (
            <div className="enr-result" data-kind={state}>
              <div className="enr-result-icon" aria-hidden="true">{state === 'approved' ? '✓' : '✕'}</div>
              <h2 className="enr-result-title">
                {state === 'approved' ? 'Box approved' : 'Request denied'}
              </h2>
              <p className="enr-result-sub">
                {state === 'approved'
                  ? 'The device now has its certificate and will appear in your fleet shortly.'
                  : 'The enrollment request was rejected. No device was bound.'}
              </p>
              <div className="enr-result-actions">
                {state === 'approved' && (
                  <Button size="sm" href={toFullPath('/boxes')} data-router>View boxes</Button>
                )}
                <Button size="sm" variant="ghost" onClick={reset}>Enroll another</Button>
              </div>
            </div>
          ) : (
            <form className="enr-form" onSubmit={(e) => { e.preventDefault(); run('approve') }}>
              <label className="enr-field-label" htmlFor="enr-code">Device code</label>
              <input
                id="enr-code"
                className="enr-input"
                type="text"
                inputMode="text"
                autoComplete="off"
                autoCapitalize="characters"
                spellCheck={false}
                value={code}
                onChange={(e) => setCode(normalise(e.target.value))}
                placeholder="WXYZ-1234"
                aria-label="The code displayed on the booting box"
              />
              {error && <div className="enr-error" role="alert">{error}</div>}
              <div className="enr-actions">
                <Button type="submit" size="md" disabled={!code.trim() || busy}>
                  {state === 'approving' ? 'Approving…' : 'Approve box'}
                </Button>
                <Button type="button" size="md" variant="ghost" onClick={() => run('deny')} disabled={!code.trim() || busy}>
                  {state === 'denying' ? 'Denying…' : 'Deny'}
                </Button>
              </div>
            </form>
          )}
        </Card>

        <Card hover={false} className="enr-facts">
          <span className="enr-eyebrow">How it works</span>
          <ol className="enr-steps">
            <li><span className="enr-step-n">1</span><span className="enr-step-label">Install and boot the Vulos OS on your box.</span></li>
            <li><span className="enr-step-n">2</span><span className="enr-step-label">It shows a one-time device code.</span></li>
            <li><span className="enr-step-n">3</span><span className="enr-step-label">Enter and approve it here — the box binds to your account.</span></li>
            <li><span className="enr-step-n">4</span><span className="enr-step-label">It receives its certificate and joins your fleet.</span></li>
          </ol>
        </Card>
      </div>
    </Section>
  )
}

const STYLES = `
  .enr-header { margin-bottom: var(--sp-4); max-width: 60ch; }
  .enr-eyebrow { font-family: var(--font-mono); font-size: var(--text-xs); letter-spacing: 0.14em; text-transform: uppercase; color: var(--text-ghost); }
  .enr-title { font-family: var(--font-mono); font-size: clamp(1.125rem, 2.2vw, 1.375rem); font-weight: 700; letter-spacing: -0.025em; margin: var(--sp-1) 0 var(--sp-0-5); }
  .enr-sub { font-size: var(--text-sm); color: var(--text-faint); line-height: 1.6; }
  .enr-grid { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr); gap: var(--sp-3); align-items: start; }
  @media (max-width: 720px) { .enr-grid { grid-template-columns: 1fr; } }
  .enr-form { display: flex; flex-direction: column; gap: var(--sp-2); }
  .enr-field-label { font-family: var(--font-mono); font-size: var(--text-xs); letter-spacing: 0.06em; text-transform: uppercase; color: var(--text-ghost); }
  .enr-input {
    font-family: var(--font-mono); font-size: clamp(1.1rem, 3vw, 1.5rem); letter-spacing: 0.18em; font-weight: 600;
    color: var(--text-primary); text-align: center;
    background: var(--bg-base); border: 1px solid var(--border-strong); border-radius: var(--radius-lg);
    padding: 16px; min-height: 56px;
  }
  .enr-input:focus-visible { outline: none; box-shadow: var(--focus-ring); border-color: var(--accent); }
  .enr-actions { display: flex; gap: var(--sp-1-5); flex-wrap: wrap; }
  .enr-error { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--danger); }
  .enr-facts { background: var(--bg-surface); }
  .enr-steps { list-style: none; margin: var(--sp-2) 0 0; padding: 0; display: flex; flex-direction: column; gap: var(--sp-2); }
  .enr-steps li { display: flex; align-items: flex-start; gap: var(--sp-1-5); }
  .enr-step-n { font-family: var(--font-mono); font-size: var(--text-xs); font-weight: 700; color: var(--accent); width: 22px; height: 22px; border-radius: 50%; border: 1px solid color-mix(in srgb, var(--accent) 40%, transparent); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
  .enr-step-label { font-size: var(--text-sm); color: var(--text-secondary); line-height: 1.5; }
  .enr-result { text-align: center; padding: var(--sp-3) var(--sp-2); }
  .enr-result-icon { font-size: 2rem; width: 56px; height: 56px; border-radius: 50%; display: inline-flex; align-items: center; justify-content: center; margin-bottom: var(--sp-2); }
  .enr-result[data-kind="approved"] .enr-result-icon { color: var(--good); border: 1px solid color-mix(in srgb, var(--good) 40%, transparent); background: color-mix(in srgb, var(--good) 8%, transparent); }
  .enr-result[data-kind="denied"] .enr-result-icon { color: var(--danger); border: 1px solid color-mix(in srgb, var(--danger) 40%, transparent); background: color-mix(in srgb, var(--danger) 8%, transparent); }
  .enr-result-title { font-family: var(--font-mono); font-size: var(--text-lg, 1.05rem); font-weight: 700; margin: 0 0 var(--sp-0-5); }
  .enr-result-sub { font-size: var(--text-sm); color: var(--text-faint); line-height: 1.6; max-width: 40ch; margin: 0 auto var(--sp-2-5); }
  .enr-result-actions { display: flex; gap: var(--sp-1-5); justify-content: center; flex-wrap: wrap; }
`
