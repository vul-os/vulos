import { useState, useEffect, useCallback } from 'react'
import { requireStepUp } from '../../lib/stepup'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function errorMessage(err: unknown): string {
  return isRecord(err) && typeof err.message === 'string' ? err.message : String(err)
}

type AlertStatus = 'pending' | 'locked' | 'dismissed' | string

interface SecurityAlert {
  id: string
  action?: string
  reason?: string
  ts?: string
  client_ip?: string
  status?: AlertStatus
}

interface SecurityAction {
  id: string
  action?: string
  ts?: string
  client_ip?: string
}

interface SecurityFeed {
  actions?: SecurityAction[]
  alerts?: SecurityAlert[]
}

function toSecurityAlert(x: unknown): SecurityAlert | null {
  if (!isRecord(x) || typeof x.id !== 'string') return null
  return {
    id: x.id,
    action: typeof x.action === 'string' ? x.action : undefined,
    reason: typeof x.reason === 'string' ? x.reason : undefined,
    ts: typeof x.ts === 'string' ? x.ts : undefined,
    client_ip: typeof x.client_ip === 'string' ? x.client_ip : undefined,
    status: typeof x.status === 'string' ? x.status : undefined,
  }
}

function toSecurityAction(x: unknown): SecurityAction | null {
  if (!isRecord(x) || typeof x.id !== 'string') return null
  return {
    id: x.id,
    action: typeof x.action === 'string' ? x.action : undefined,
    ts: typeof x.ts === 'string' ? x.ts : undefined,
    client_ip: typeof x.client_ip === 'string' ? x.client_ip : undefined,
  }
}

// `undefined` here means THE BOX DID NOT SAY, and it is kept distinct from an
// empty array all the way to the render. Collapsing the two with `|| []` is what
// let this panel print "Nothing recorded yet" — a clean bill of health — over a
// reply that carried no `actions` key at all. A non-2xx is caught before this
// (load() rejects on !r.ok), so the live shape of that failure is a 200 whose
// body is missing the field: the "answered, but told you nothing" case
// settings-honesty.e2e.ts exists for. On a security page that is the one
// sentence that must never be guessed.
function toSecurityFeed(x: unknown): SecurityFeed {
  if (!isRecord(x)) return {}
  return {
    actions: Array.isArray(x.actions) ? x.actions.map(toSecurityAction).filter((a): a is SecurityAction => a !== null) : undefined,
    alerts: Array.isArray(x.alerts) ? x.alerts.map(toSecurityAlert).filter((a): a is SecurityAlert => a !== null) : undefined,
  }
}

// SecurityPanel — account-takeover (ATO) monitoring for this profile.
//
// Backend: backend/services/accountsecurity (ported from management's
// pkg/security ato.go/store.go, trimmed to single-box-owner semantics).
//   GET  /api/accountsecurity/feed                  → { actions:[...], alerts:[...] }
//   POST /api/accountsecurity/alerts/{id}/dismiss    → "this was me"
//   POST /api/accountsecurity/alerts/{id}/lock       → "this wasn't me": signs out
//                                                       every device, step-up gated
//
// A pending alert means a sensitive account change (password, recovery/master
// key, passkeys, role, a bulk export or mass download) looked anomalous — a
// burst of changes, or one from a network/device we haven't seen recently.
// Alerts are also pushed as a cross-device notification the moment they fire
// (backend/services/notify); this panel is where you act on them.

const ACTION_LABELS: Record<string, string> = {
  password_change: 'Password changed',
  recovery_used: 'Account recovery used',
  masterkey_reset: 'Encryption key reset',
  passkey_change: 'Passkey added or removed',
  role_change: 'Account role changed',
  bulk_export: 'Full data export',
  mass_download: 'Large file download',
}

const REASON_LABELS: Record<string, string> = {
  multiple_sensitive_actions: 'Several sensitive changes in a short time',
  new_ip_sensitive_action: 'Change came from an unfamiliar device or network',
}

function actionLabel(action: string | undefined): string {
  return (action && ACTION_LABELS[action]) || action || ''
}

function formatTs(ts: string | undefined): string {
  if (!ts) return '—'
  try {
    return new Date(ts).toLocaleString()
  } catch {
    return ts
  }
}

export default function SecurityPanel() {
  const [feed, setFeed] = useState<SecurityFeed | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [busyID, setBusyID] = useState<string | null>(null) // alert id currently being resolved
  const [confirmingLockID, setConfirmingLockID] = useState<string | null>(null)

  const load = useCallback(() => {
    setError('')
    fetch('/api/accountsecurity/feed')
      .then(r => (r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status))))
      .then((d: unknown) => setFeed(toSecurityFeed(d)))
      .catch(e => setError(errorMessage(e)))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])

  const dismiss = useCallback(async (id: string) => {
    setBusyID(id)
    setError('')
    try {
      const r = await fetch(`/api/accountsecurity/alerts/${id}/dismiss`, { method: 'POST' })
      const d: unknown = await r.json().catch(() => ({}))
      if (!r.ok) throw new Error((isRecord(d) && typeof d.error === 'string' ? d.error : '') || 'HTTP ' + r.status)
      load()
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusyID(null)
    }
  }, [load])

  const lock = useCallback(async (id: string) => {
    setBusyID(id)
    setError('')
    try {
      await requireStepUp() // confirm-password modal; throws if cancelled
      const r = await fetch(`/api/accountsecurity/alerts/${id}/lock`, { method: 'POST' })
      const d: unknown = await r.json().catch(() => ({}))
      if (!r.ok) throw new Error((isRecord(d) && typeof d.error === 'string' ? d.error : '') || 'HTTP ' + r.status)
      setConfirmingLockID(null)
      load()
    } catch (e) {
      const msg = errorMessage(e)
      if (msg) setError(msg)
    } finally {
      setBusyID(null)
    }
  }, [load])

  const alertsKnown = feed?.alerts !== undefined
  const actionsKnown = feed?.actions !== undefined
  const alerts = feed?.alerts || []
  const pending = alerts.filter(a => a.status === 'pending')
  // 'dismissed' and 'locked' are the only resolutions the backend records. An
  // alert whose status the box omitted is NOT resolved — it used to land here
  // and render as "Dismissed", telling you someone had cleared an alert nobody
  // had even seen.
  const resolved = alerts.filter(a => a.status === 'dismissed' || a.status === 'locked')
  const unknownState = alerts.filter(a => a.status !== 'pending' && a.status !== 'dismissed' && a.status !== 'locked')
  const actions = feed?.actions || []

  return (
    <div>
      <header className="mb-5 pb-4 border-b border-[var(--border-default)]">
        <h2 className="text-xl font-semibold tracking-tight text-[var(--text-primary)]">Security</h2>
        <p className="mt-1 text-sm text-[var(--text-tertiary)] leading-relaxed">
          Vulos watches sensitive changes to this account — password and encryption-key resets,
          passkey changes, role changes, and large exports or downloads — and flags anything that
          looks anomalous. Flagged activity is also pushed to every signed-in device the moment it
          happens.
        </p>
      </header>

      {error && (
        <div className="mb-4 rounded-lg border border-danger-soft bg-danger-soft px-3 py-2 text-xs text-danger">
          {error}
        </div>
      )}

      {loading ? (
        <p className="text-sm text-[var(--text-tertiary)]">Loading…</p>
      ) : (
        <>
          {!alertsKnown && (
            <div className="mb-4 rounded-lg border border-warning-soft bg-warning-soft px-3 py-2 text-xs text-warning">
              The box did not report an alerts list. Absence of alerts here is not
              evidence that none were raised.
            </div>
          )}

          {pending.length > 0 && (
            <div className="mb-6 space-y-3">
              {pending.map(a => (
                <div
                  key={a.id}
                  className="rounded-xl border border-danger-soft bg-danger-soft p-4"
                >
                  <div className="flex items-start justify-between gap-4">
                    <div className="min-w-0">
                      <p className="text-sm font-medium text-[var(--text-primary)]">
                        {actionLabel(a.action)}
                      </p>
                      <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
                        {(a.reason && REASON_LABELS[a.reason]) || a.reason}
                      </p>
                      <p className="mt-1 text-[12px] text-[var(--text-faint)]">
                        {formatTs(a.ts)} · {a.client_ip || 'unknown location'}
                      </p>
                    </div>
                  </div>

                  {confirmingLockID === a.id ? (
                    <div className="mt-3 pt-3 border-t border-danger-soft">
                      <p className="text-xs text-[var(--text-secondary)] leading-relaxed">
                        This signs every device out of this account — including this one — and
                        requires signing back in with your password. Continue?
                      </p>
                      <div className="mt-2 flex gap-2 justify-end">
                        <button
                          onClick={() => setConfirmingLockID(null)}
                          disabled={busyID === a.id}
                          className="text-xs px-3 py-1.5 rounded-lg bg-[var(--bg-elevated)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)] transition-colors focus-primary disabled:opacity-40"
                        >
                          Cancel
                        </button>
                        <button
                          onClick={() => lock(a.id)}
                          disabled={busyID === a.id}
                          className="text-xs px-3 py-1.5 rounded-lg bg-danger text-white font-medium hover:brightness-110 transition-all focus-primary disabled:opacity-40"
                        >
                          {busyID === a.id ? 'Locking…' : 'Sign out every device'}
                        </button>
                      </div>
                    </div>
                  ) : (
                    <div className="mt-3 flex gap-2 justify-end">
                      <button
                        onClick={() => dismiss(a.id)}
                        disabled={busyID === a.id}
                        className="text-xs px-3 py-1.5 rounded-lg bg-[var(--bg-elevated)] text-[var(--text-secondary)] hover:text-[var(--text-primary)] transition-colors focus-primary disabled:opacity-40"
                      >
                        {busyID === a.id ? 'Working…' : 'This was me'}
                      </button>
                      <button
                        onClick={() => setConfirmingLockID(a.id)}
                        disabled={busyID === a.id}
                        className="text-xs px-3 py-1.5 rounded-lg bg-danger text-white font-medium hover:brightness-110 transition-all focus-primary disabled:opacity-40"
                      >
                        Not me — lock account
                      </button>
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}

          <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] divide-y divide-[var(--border-default)]">
            <div className="px-4 py-2.5">
              <p className="text-xs font-medium text-[var(--text-muted)] uppercase tracking-wide">
                Recent sensitive activity
              </p>
            </div>
            {!actionsKnown ? (
              <div className="px-4 py-4">
                <p className="text-xs text-warning">
                  The box did not report any activity list. This is not the same as
                  &ldquo;nothing happened&rdquo; — reload, and if it persists check the box is healthy.
                </p>
              </div>
            ) : actions.length === 0 ? (
              <div className="px-4 py-4">
                <p className="text-xs text-[var(--text-faint)]">
                  Nothing recorded yet. Sensitive changes to this account will appear here.
                </p>
              </div>
            ) : (
              actions.slice(0, 20).map(rec => (
                <div key={rec.id} className="flex items-center justify-between gap-4 px-4 py-2.5">
                  <span className="text-sm text-[var(--text-secondary)]">{actionLabel(rec.action)}</span>
                  <span className="text-xs text-[var(--text-faint)]">
                    {formatTs(rec.ts)} · {rec.client_ip || 'unknown'}
                  </span>
                </div>
              ))
            )}
          </div>

          {unknownState.length > 0 && (
            <div className="mt-4 rounded-xl border border-warning-soft bg-[var(--bg-surface)] divide-y divide-[var(--border-default)]">
              <div className="px-4 py-2.5">
                <p className="text-xs font-medium text-warning uppercase tracking-wide">
                  Alerts in an unrecognised state
                </p>
              </div>
              {unknownState.slice(0, 10).map(a => (
                <div key={a.id} className="flex items-center justify-between gap-4 px-4 py-2.5">
                  <div className="min-w-0">
                    <span className="text-sm text-[var(--text-secondary)]">{actionLabel(a.action)}</span>
                    <span className="ml-2 text-[12px] text-warning">
                      {a.status ? `reported as “${a.status}”` : 'no status reported'}
                    </span>
                  </div>
                  <span className="text-xs text-[var(--text-faint)]">{formatTs(a.ts)}</span>
                </div>
              ))}
            </div>
          )}

          {resolved.length > 0 && (
            <div className="mt-4 rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] divide-y divide-[var(--border-default)]">
              <div className="px-4 py-2.5">
                <p className="text-xs font-medium text-[var(--text-muted)] uppercase tracking-wide">
                  Reviewed alerts
                </p>
              </div>
              {resolved.slice(0, 10).map(a => (
                <div key={a.id} className="flex items-center justify-between gap-4 px-4 py-2.5">
                  <div className="min-w-0">
                    <span className="text-sm text-[var(--text-tertiary)]">{actionLabel(a.action)}</span>
                    <span className="ml-2 text-[12px] text-[var(--text-faint)]">
                      {a.status === 'locked' ? 'Account locked' : 'Dismissed'}
                    </span>
                  </div>
                  <span className="text-xs text-[var(--text-faint)]">{formatTs(a.ts)}</span>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  )
}
