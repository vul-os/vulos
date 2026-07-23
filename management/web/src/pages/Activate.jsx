/**
 * vulos-cloud — /activate device approval page
 *
 * Public route (but auth-gated: unauthenticated users are redirected to
 * /login?return=/activate preserving any ?code= param).
 *
 * Flow:
 *   QR scan  → /activate?code=WXYZ-1234  (pre-fills the input)
 *   Manual   → /activate                  (user types their user_code)
 *
 * States:
 *   loading  — auth probe in flight (skeleton)
 *   entry    — code input + Approve / Deny buttons
 *   pending  — network in flight (buttons disabled, spinner)
 *   success  — approval confirmed
 *   denied   — denial confirmed
 *   error    — expired | invalid code | server error (inline message)
 *
 * API surface (session cookie required):
 *   POST /api/enroll/approve  { user_code }  → 204 No Content
 *   POST /api/enroll/deny     { user_code }  → 204 No Content
 *
 * OSS-native aesthetic: near-black canvas, mono type, single accent moment.
 * Self-contained — all styles inline in this file.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { Section } from '../ui/index.jsx'
import { LogoMark } from '../components/Logo.jsx'
import { useAuth } from '../auth/AuthProvider.jsx'
import { clientNavigate, clientReplace, currentAppPath } from '../auth/nav.js'

/* ─── Helpers ──────────────────────────────────────────────── */

/** Normalise the query-param code: strip whitespace, uppercase. */
function normaliseCode(raw) {
  if (!raw || typeof raw !== 'string') return ''
  return raw.trim().toUpperCase()
}

/**
 * Build the /login redirect URL (app-relative) preserving the current path +
 * search — e.g. /login?return=%2Factivate%3Fcode%3DWXYZ-1234
 */
function buildLoginRedirect() {
  return `/login?return=${encodeURIComponent(currentAppPath())}`
}

/** Validate the user_code format: exactly XXXX-XXXX (alphanum, dash at pos 4). */
const USER_CODE_RE = /^[A-Z0-9]{4}-[A-Z0-9]{4}$/

/** Format a raw user input into XXXX-XXXX as they type. */
function formatCodeInput(raw) {
  // Strip everything except alphanumeric chars and existing dashes
  const stripped = raw.toUpperCase().replace(/[^A-Z0-9]/g, '')
  if (stripped.length <= 4) return stripped
  return `${stripped.slice(0, 4)}-${stripped.slice(4, 8)}`
}

/** Pull the human-readable error label out of a server error response. */
async function extractServerError(res) {
  try {
    const text = await res.text()
    if (!text) return null
    const j = JSON.parse(text)
    if (j && typeof j.error === 'string') return j.error
    if (j && typeof j.message === 'string') return j.message
    return text || null
  } catch {
    return null
  }
}

function Spinner({ size = 14 }) {
  return (
    <span
      aria-hidden="true"
      style={{
        display: 'inline-block',
        width: size,
        height: size,
        borderRadius: '50%',
        border: `2px solid color-mix(in srgb, currentColor 25%, transparent)`,
        borderTopColor: 'currentColor',
        animation: 'vcActSpin 0.7s linear infinite',
        flexShrink: 0,
      }}
    />
  )
}

/* ─── Loading skeleton (mirrors RequireAuth skeleton aesthetic) ─ */
function AuthSkeleton() {
  return (
    <div
      style={{
        minHeight: '60dvh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: 'var(--sp-6, 48px) var(--sp-3, 24px)',
      }}
    >
      <span
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 10,
          fontFamily: 'var(--font-mono)',
          fontSize: '0.8125rem',
          letterSpacing: '0.02em',
          color: 'var(--text-faint)',
        }}
      >
        <span
          aria-hidden="true"
          style={{
            width: 6,
            height: 6,
            borderRadius: '50%',
            background: 'var(--accent)',
            animation: 'vcActPulse 1.1s ease-in-out infinite',
            flexShrink: 0,
          }}
        />
        Checking session…
      </span>
    </div>
  )
}

/* ─── Success state ─────────────────────────────────────────── */
function SuccessView({ userCode, onActivateAnother }) {
  return (
    <div className="vca-card vca-reveal">
      <ActivateHead eyebrow="Device Enrollment" title="Device approved" />

      <div className="vca-ok" role="status" aria-live="polite">
        <span className="vca-ok-tag">Approved</span>
        <span className="vca-ok-msg">
          The device with code{' '}
          <code className="vca-code-inline">{userCode}</code> has been enrolled
          in your fleet. It will complete activation within a few seconds.
        </span>
      </div>

      <p
        style={{
          fontFamily: 'var(--font-sans)',
          fontSize: '0.875rem',
          color: 'var(--text-tertiary)',
          lineHeight: 1.6,
          margin: 'var(--sp-3, 24px) 0 0',
        }}
      >
        New to bare-metal boot?{' '}
        <a
          href="/first-boot"
          onClick={e => { e.preventDefault(); clientNavigate('/first-boot') }}
          style={{
            color: 'var(--text-secondary)',
            textDecoration: 'underline',
            textUnderlineOffset: '2px',
          }}
        >
          See the first-boot guide
        </a>{' '}
        to complete your first Vulos install.
      </p>

      <button
        type="button"
        className="vca-btn-ghost"
        style={{ marginTop: 'var(--sp-3, 24px)', width: '100%' }}
        onClick={onActivateAnother}
      >
        Activate another device
      </button>
    </div>
  )
}

/* ─── Deny success state ────────────────────────────────────── */
function DeniedView({ userCode, onActivateAnother }) {
  return (
    <div className="vca-card vca-reveal">
      <ActivateHead eyebrow="Device Enrollment" title="Device denied" />

      <div className="vca-warn" role="status" aria-live="polite">
        <span className="vca-warn-tag">Denied</span>
        <span className="vca-warn-msg">
          The request from{' '}
          <code className="vca-code-inline">{userCode}</code> was denied. The
          device will not be added to your fleet.
        </span>
      </div>

      <button
        type="button"
        className="vca-btn-ghost"
        style={{ marginTop: 'var(--sp-3, 24px)', width: '100%' }}
        onClick={onActivateAnother}
      >
        Go back
      </button>
    </div>
  )
}

/* ─── Shared header ─────────────────────────────────────────── */
function ActivateHead({ eyebrow, title, subtitle }) {
  return (
    <div className="vca-head">
      <a
        href="/"
        onClick={e => { e.preventDefault(); clientNavigate('/') }}
        className="vca-brand"
        aria-label="Vulos — home"
      >
        <LogoMark size={36} tone="on-dark" />
        <span className="vca-wordmark">
          vulos<span className="vca-wordmark-suffix">.org</span>
        </span>
      </a>
      {eyebrow && <p className="vca-eyebrow">{eyebrow}</p>}
      {title   && <h1 className="vca-title">{title}</h1>}
      {subtitle && <p className="vca-subtitle">{subtitle}</p>}
    </div>
  )
}

/* ─── Main entry form ───────────────────────────────────────── */
function EntryForm({ initialCode, onApproved, onDenied }) {
  const [code,        setCode]        = useState(normaliseCode(initialCode))
  const [touched,     setTouched]     = useState(false)
  const [submitting,  setSubmitting]  = useState(false)
  const [submitError, setSubmitError] = useState(null)
  const inputRef = useRef(null)

  // Auto-focus on mount
  useEffect(() => {
    if (!initialCode && inputRef.current) inputRef.current.focus()
  }, [initialCode])

  const codeError = touched && !USER_CODE_RE.test(code)
    ? 'Enter a valid device code in XXXX-XXXX format.'
    : null

  const canSubmit = USER_CODE_RE.test(code) && !submitting

  function handleCodeChange(e) {
    const formatted = formatCodeInput(e.target.value)
    setCode(formatted)
    if (submitError) setSubmitError(null)
  }

  async function doRequest(endpoint) {
    setTouched(true)
    if (!USER_CODE_RE.test(code)) return
    setSubmitError(null)
    setSubmitting(true)
    try {
      const res = await fetch(endpoint, {
        method: 'POST',
        credentials: 'include',
        headers: {
          'Content-Type': 'application/json',
          Accept: 'application/json',
        },
        body: JSON.stringify({ user_code: code }),
      })
      if (res.ok || res.status === 204) {
        if (endpoint.endsWith('approve')) {
          onApproved(code)
        } else {
          onDenied(code)
        }
        return
      }
      // Map HTTP status codes to user-facing messages
      let msg = await extractServerError(res)
      if (!msg) {
        if (res.status === 410 || (msg && msg.includes('expired'))) {
          msg = 'This device code has expired. Ask the device to restart enrollment and scan a new QR code.'
        } else if (res.status === 403 || (msg && msg.includes('denied'))) {
          msg = 'Access denied. The device request has already been denied.'
        } else if (res.status === 404 || (msg && msg.includes('unknown'))) {
          msg = 'Device code not found. Check the code and try again.'
        } else if (res.status === 409) {
          msg = 'This enrollment has already been processed.'
        } else if (res.status === 429) {
          msg = 'Too many attempts — please wait a moment and try again.'
        } else if (res.status >= 500) {
          msg = 'Something went wrong on our end. Please try again.'
        } else {
          msg = 'Request failed. Please try again.'
        }
      } else {
        // Enrich known error strings from the backend
        if (msg.includes('expired_token') || msg.includes('expired')) {
          msg = 'This device code has expired. Ask the device to restart enrollment and scan a new QR code.'
        } else if (msg.includes('access_denied') || msg.includes('denied')) {
          msg = 'Access denied. The device request has already been denied.'
        } else if (msg.includes('unknown_user_code') || msg.includes('unknown user_code')) {
          msg = 'Device code not found. Check the code and try again.'
        }
      }
      setSubmitError(msg)
    } catch (err) {
      setSubmitError(err && err.message ? err.message : 'Network error. Please check your connection.')
    } finally {
      setSubmitting(false)
    }
  }

  const handleApprove = (e) => { e.preventDefault(); doRequest('/api/enroll/approve') }
  const handleDeny    = (e) => { e.preventDefault(); doRequest('/api/enroll/deny') }

  return (
    <div className="vca-card vca-reveal">
      <ActivateHead
        eyebrow="Device Enrollment"
        title="Approve device"
        subtitle="Enter the code shown on your device display to add it to your fleet."
      />

      <form noValidate onSubmit={handleApprove} className="vca-form">
        {submitError && (
          <div
            role="alert"
            aria-live="assertive"
            className="vca-alert"
          >
            <span className="vca-alert-tag">Error</span>
            <span className="vca-alert-msg">{submitError}</span>
          </div>
        )}

        <div className="vca-field">
          <div className="vca-label-row">
            <label htmlFor="activate-code" className="vca-label">
              Device code
            </label>
          </div>
          <input
            ref={inputRef}
            id="activate-code"
            name="user_code"
            type="text"
            autoComplete="off"
            spellCheck={false}
            autoCapitalize="characters"
            value={code}
            onChange={handleCodeChange}
            onBlur={() => setTouched(true)}
            placeholder="WXYZ-1234"
            maxLength={9}
            required
            aria-invalid={codeError ? 'true' : 'false'}
            aria-describedby={codeError ? 'activate-code-err' : 'activate-code-hint'}
            className={`vca-input vca-input-code${codeError ? ' has-error' : ''}`}
            autoFocus={!!initialCode}
            disabled={submitting}
          />
          {codeError ? (
            <p id="activate-code-err" className="vca-fielderr">{codeError}</p>
          ) : (
            <p id="activate-code-hint" className="vca-hint">
              Format: 4 characters, dash, 4 characters — e.g.{' '}
              <code className="vca-code-inline">WXYZ-1234</code>
            </p>
          )}
        </div>

        {/* Device summary panel */}
        <div className="vca-summary" aria-label="Approval summary">
          <div className="vca-summary-row">
            <span className="vca-summary-key">Action</span>
            <span className="vca-summary-val">Add device to fleet</span>
          </div>
          <div className="vca-summary-row">
            <span className="vca-summary-key">Code</span>
            <span className="vca-summary-val vca-summary-code">
              {USER_CODE_RE.test(code) ? code : <span style={{ color: 'var(--text-ghost)' }}>—</span>}
            </span>
          </div>
          <div className="vca-summary-row">
            <span className="vca-summary-key">Time</span>
            <span className="vca-summary-val">{new Date().toLocaleTimeString()}</span>
          </div>
        </div>

        {/* Approve — primary accent button */}
        <button
          type="submit"
          className="vca-btn-approve"
          disabled={!canSubmit}
          aria-busy={submitting ? 'true' : 'false'}
        >
          {submitting
            ? (<><Spinner size={14} /><span>Processing…</span></>)
            : 'Approve device'}
        </button>

        {/* Deny — destructive ghost */}
        <button
          type="button"
          className="vca-btn-deny"
          disabled={submitting || !USER_CODE_RE.test(code)}
          onClick={handleDeny}
          aria-busy={submitting ? 'true' : 'false'}
        >
          Deny
        </button>
      </form>
    </div>
  )
}

/* ─── Styles ────────────────────────────────────────────────── */
function ActivateStyles() {
  return (
    <style>{`
      @keyframes vcActSpin  { to { transform: rotate(360deg); } }
      @keyframes vcActPulse {
        0%, 100% { opacity: 1;    transform: scale(1); }
        50%      { opacity: 0.35; transform: scale(0.65); }
      }
      @keyframes vcActRise {
        from { opacity: 0; transform: translateY(8px); }
        to   { opacity: 1; transform: translateY(0); }
      }

      /* ── Layout ─────────────────────────────────────────── */
      .vca-wrap {
        display: flex;
        align-items: flex-start;
        justify-content: center;
        width: 100%;
        padding-top: var(--sp-3, 24px);
      }
      .vca-card {
        width: 100%;
        max-width: 440px;
        background: var(--bg-surface);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius-lg, 16px);
        padding: var(--sp-6, 48px) var(--sp-5, 40px);
        box-shadow: var(--shadow);
      }
      .vca-reveal {
        animation: vcActRise 280ms var(--ease, cubic-bezier(.22,1,.36,1)) both;
      }
      @media (prefers-reduced-motion: reduce) {
        .vca-reveal { animation: none; }
      }

      /* ── Head ───────────────────────────────────────────── */
      .vca-head {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 6px;
        margin-bottom: var(--sp-4, 32px);
      }
      .vca-brand {
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
      .vca-brand:hover { opacity: 0.86; }
      .vca-brand:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
      }
      .vca-wordmark {
        font-family: var(--font-mono);
        font-weight: 600;
        font-size: 0.9375rem;
        letter-spacing: -0.01em;
        color: var(--text-primary);
        line-height: 1;
      }
      .vca-wordmark-suffix {
        color: var(--text-faint);
        font-weight: 400;
      }
      .vca-eyebrow {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 500;
        letter-spacing: 0.16em;
        text-transform: uppercase;
        color: var(--text-faint);
        margin: 0;
      }
      .vca-title {
        font-family: var(--font-mono);
        font-size: 1.625rem;
        font-weight: 700;
        letter-spacing: -0.025em;
        color: var(--text-primary);
        line-height: 1.15;
        margin: 4px 0 0;
      }
      .vca-subtitle {
        font-family: var(--font-sans);
        font-size: 0.9375rem;
        color: var(--text-tertiary);
        line-height: 1.55;
        margin: 8px 0 0;
      }

      /* ── Form ───────────────────────────────────────────── */
      .vca-form {
        display: flex;
        flex-direction: column;
        gap: 16px;
      }

      /* ── Alert ──────────────────────────────────────────── */
      .vca-alert {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 12px 14px;
        border-radius: var(--radius, 10px);
        background: color-mix(in srgb, var(--warn) 10%, var(--bg-elevated));
        border: 1px solid color-mix(in srgb, var(--warn) 35%, var(--border-emphasis));
      }
      .vca-alert-tag {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 600;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--warn);
        padding-top: 1px;
        flex-shrink: 0;
      }
      .vca-alert-msg {
        font-family: var(--font-sans);
        font-size: 0.875rem;
        color: var(--text-secondary);
        line-height: 1.5;
      }

      /* ── Ok (success) banner ────────────────────────────── */
      .vca-ok {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 14px 16px;
        border-radius: var(--radius, 10px);
        background: color-mix(in srgb, var(--good) 8%, var(--bg-elevated));
        border: 1px solid color-mix(in srgb, var(--good) 30%, var(--border-emphasis));
      }
      .vca-ok-tag {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 600;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--good);
        padding-top: 1px;
        flex-shrink: 0;
      }
      .vca-ok-msg {
        font-family: var(--font-sans);
        font-size: 0.875rem;
        color: var(--text-secondary);
        line-height: 1.55;
      }

      /* ── Warn (denied) banner ───────────────────────────── */
      .vca-warn {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 14px 16px;
        border-radius: var(--radius, 10px);
        background: color-mix(in srgb, var(--warn) 8%, var(--bg-elevated));
        border: 1px solid color-mix(in srgb, var(--warn) 28%, var(--border-emphasis));
      }
      .vca-warn-tag {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 600;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--warn);
        padding-top: 1px;
        flex-shrink: 0;
      }
      .vca-warn-msg {
        font-family: var(--font-sans);
        font-size: 0.875rem;
        color: var(--text-secondary);
        line-height: 1.55;
      }

      /* ── Field ──────────────────────────────────────────── */
      .vca-field {
        display: flex;
        flex-direction: column;
        gap: 6px;
      }
      .vca-label-row {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 12px;
      }
      .vca-label {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        font-weight: 500;
        letter-spacing: 0.06em;
        text-transform: uppercase;
        color: var(--text-tertiary);
        line-height: 1.2;
      }
      .vca-input {
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
        outline: none;
        transition:
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background    var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          box-shadow    var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vca-input::placeholder { color: var(--text-ghost); }
      .vca-input:hover  { border-color: var(--border-emphasis); }
      .vca-input:focus,
      .vca-input:focus-visible {
        border-color: var(--accent);
        box-shadow: var(--focus-ring);
      }
      .vca-input.has-error { border-color: var(--danger); }
      .vca-input.has-error:focus,
      .vca-input.has-error:focus-visible {
        box-shadow: 0 0 0 3px color-mix(in srgb, var(--danger) 30%, transparent);
      }
      .vca-input:disabled {
        opacity: 0.55;
        cursor: not-allowed;
      }

      /* Larger, centred input for the code */
      .vca-input-code {
        font-size: 1.5rem;
        font-weight: 600;
        letter-spacing: 0.12em;
        text-align: center;
        text-transform: uppercase;
        padding: 14px 18px;
      }

      .vca-fielderr {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--danger);
        margin: 0;
        line-height: 1.4;
      }
      .vca-hint {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-faint);
        margin: 0;
        line-height: 1.5;
      }

      /* ── Device summary card ─────────────────────────────── */
      .vca-summary {
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px);
        padding: 14px 16px;
        display: flex;
        flex-direction: column;
        gap: 8px;
      }
      .vca-summary-row {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 12px;
      }
      .vca-summary-key {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 500;
        letter-spacing: 0.12em;
        text-transform: uppercase;
        color: var(--text-faint);
        flex-shrink: 0;
      }
      .vca-summary-val {
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        color: var(--text-secondary);
        text-align: right;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
      }
      .vca-summary-code {
        letter-spacing: 0.08em;
        font-weight: 600;
        color: var(--text-primary);
      }

      /* ── Inline code snippet ─────────────────────────────── */
      .vca-code-inline {
        font-family: var(--font-mono);
        font-size: 0.875em;
        font-weight: 600;
        letter-spacing: 0.06em;
        color: var(--text-primary);
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius-sm, 6px);
        padding: 1px 5px;
      }

      /* ── Approve button (the ONE accent moment) ──────────── */
      .vca-btn-approve {
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
          background   var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          transform    var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          box-shadow   var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vca-btn-approve:hover:not(:disabled) {
        background: var(--accent-hover);
        border-color: var(--accent-hover);
        transform: translateY(-1px);
        box-shadow: 0 6px 18px color-mix(in srgb, var(--accent) 30%, transparent);
      }
      .vca-btn-approve:active:not(:disabled) { transform: translateY(0); box-shadow: none; }
      .vca-btn-approve:focus-visible { outline: none; box-shadow: var(--focus-ring); }
      .vca-btn-approve:disabled {
        opacity: 0.5;
        cursor: not-allowed;
        transform: none;
        box-shadow: none;
      }

      /* ── Deny button (destructive ghost) ─────────────────── */
      .vca-btn-deny {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        width: 100%;
        min-height: 44px;
        padding: 11px 16px;
        background: transparent;
        color: var(--text-faint);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px);
        font-family: var(--font-mono);
        font-size: 0.875rem;
        font-weight: 500;
        letter-spacing: 0.01em;
        cursor: pointer;
        transition:
          color        var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background   var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vca-btn-deny:hover:not(:disabled) {
        color: var(--danger);
        border-color: color-mix(in srgb, var(--danger) 45%, var(--border-strong));
        background: color-mix(in srgb, var(--danger) 6%, transparent);
      }
      .vca-btn-deny:focus-visible { outline: none; box-shadow: var(--focus-ring); }
      .vca-btn-deny:disabled {
        opacity: 0.4;
        cursor: not-allowed;
      }

      /* ── Ghost button (secondary actions) ───────────────── */
      .vca-btn-ghost {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 11px 16px;
        min-height: 44px;
        background: transparent;
        color: var(--text-faint);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px);
        font-family: var(--font-mono);
        font-size: 0.875rem;
        font-weight: 500;
        letter-spacing: 0.01em;
        cursor: pointer;
        transition:
          color        var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background   var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vca-btn-ghost:hover {
        color: var(--text-secondary);
        border-color: var(--border-emphasis);
        background: var(--bg-hover);
      }
      .vca-btn-ghost:focus-visible { outline: none; box-shadow: var(--focus-ring); }

      /* ── Responsive ─────────────────────────────────────── */
      @media (max-width: 480px) {
        .vca-card {
          padding: var(--sp-5, 40px) var(--sp-3, 24px);
          border-radius: var(--radius, 10px);
        }
        .vca-title { font-size: 1.4375rem; }
        .vca-input-code { font-size: 1.25rem; }
      }
      @media (max-width: 360px) {
        .vca-card { padding: var(--sp-4, 32px) 16px; }
      }
    `}</style>
  )
}

/* ─── Page ──────────────────────────────────────────────────── */
export default function Activate() {
  const { user, loading: authLoading } = useAuth()

  // View: 'entry' | 'success' | 'denied'
  const [view,           setView]           = useState('entry')
  const [confirmedCode,  setConfirmedCode]  = useState('')

  // Read ?code= from URL
  const initialCode = useMemo(() => {
    try {
      const url = new URL(window.location.href)
      return normaliseCode(url.searchParams.get('code') || '')
    } catch {
      return ''
    }
  }, [])

  // Auth gate: redirect to /login preserving the return URL (with ?code= if any)
  useEffect(() => {
    if (authLoading) return
    if (!user) clientReplace(buildLoginRedirect())
  }, [authLoading, user])

  // While auth is resolving (or we're about to redirect), show skeleton
  if (authLoading || !user) return <AuthSkeleton />

  function handleApproved(code) {
    setConfirmedCode(code)
    setView('success')
  }

  function handleDenied(code) {
    setConfirmedCode(code)
    setView('denied')
  }

  function handleReset() {
    setView('entry')
    setConfirmedCode('')
    // Clear the ?code= param so the fresh form is blank
    try {
      const url = new URL(window.location.href)
      url.searchParams.delete('code')
      window.history.replaceState({}, '', url.pathname)
    } catch { /* ignore */ }
  }

  return (
    <Section slim>
      <ActivateStyles />
      <div className="vca-wrap">
        {view === 'success' && (
          <SuccessView
            userCode={confirmedCode}
            onActivateAnother={handleReset}
          />
        )}
        {view === 'denied' && (
          <DeniedView
            userCode={confirmedCode}
            onActivateAnother={handleReset}
          />
        )}
        {view === 'entry' && (
          <EntryForm
            initialCode={initialCode}
            onApproved={handleApproved}
            onDenied={handleDenied}
          />
        )}
      </div>
    </Section>
  )
}
