import { useState, useEffect, useCallback } from 'react'

// ---------------------------------------------------------------------------
// SupportPanel — Settings → Help & Support.
//
// Native port of the former vulos-management SUPPORT-03 three-tier support
// model (see backend/services/support). The box owner submits a support
// request; its channel and SLA target are classified from the owner's plan
// tier (Free / Pro / Team — same tiers as Plan & Billing):
//
//   Free  — no ticket channel. This panel points you at docs/community instead.
//   Pro   — "email" channel, ~1-business-day SLA target.
//   Team  — "priority" channel, ~1-hour SLA target for urgent (P1) requests.
//
// Endpoints:
//   GET  /api/support/eligibility        → { tier, channel, available }
//   POST /api/support/requests           → { subject, body, priority } → Request
//   GET  /api/support/requests           → { requests: [Request] }
//   POST /api/support/requests/{id}/close
//
// Honesty note shown in the UI: this records and classifies the request on
// this box. It is not a live chat — delivery to a real support desk depends
// on your Vulos Cloud enrollment.
// ---------------------------------------------------------------------------

const PRIORITIES = [
  { value: 'P3', label: 'Normal' },
  { value: 'P2', label: 'High' },
  { value: 'P1', label: 'Urgent' },
]

const CHANNEL_LABELS = {
  email: 'Email support (about 1 business day)',
  priority: 'Priority support (about 1 hour for urgent requests)',
}

const TIER_LABELS = { free: 'Free', pro: 'Pro', team: 'Team' }

function Section({ title, desc, children }) {
  return (
    <div>
      <header className="mb-5 pb-4 border-b border-[var(--border-default)]">
        <h2 className="text-xl font-semibold tracking-tight text-[var(--text-primary)]">{title}</h2>
        {desc && <p className="mt-1 text-sm text-[var(--text-tertiary)] leading-relaxed">{desc}</p>}
      </header>
      {children}
    </div>
  )
}

function Field({ label, hint, children }) {
  return (
    <div className="mb-3">
      <label className="block text-xs text-[var(--text-muted)] mb-1">{label}</label>
      {children}
      {hint && <p className="text-[11px] text-[var(--text-faint)] mt-1">{hint}</p>}
    </div>
  )
}

function fmtDate(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

function StateBadge({ state, breached }) {
  if (state === 'closed') {
    return (
      <span className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-[var(--bg-elevated)] text-[var(--text-muted)]">
        Closed
      </span>
    )
  }
  if (breached) {
    return (
      <span className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-danger-soft text-danger">
        SLA passed
      </span>
    )
  }
  return (
    <span className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-success-soft text-success">
      Open
    </span>
  )
}

export default function SupportPanel() {
  const [eligibility, setEligibility] = useState(null) // { tier, channel, available }
  const [requests, setRequests] = useState([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')

  const [subject, setSubject] = useState('')
  const [body, setBody] = useState('')
  const [priority, setPriority] = useState('P3')
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState('')
  const [wall, setWall] = useState(null) // { docs_url, community_url } when the tier wall is hit
  const [closingID, setClosingID] = useState(null)

  const load = useCallback(async () => {
    setLoading(true)
    setLoadError('')
    try {
      const safe = (p) => p.then(r => (r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status))))
      const [elig, list] = await Promise.all([
        safe(fetch('/api/support/eligibility')),
        safe(fetch('/api/support/requests')),
      ])
      setEligibility(elig)
      setRequests(list.requests || [])
    } catch (e) {
      setLoadError(e.message || 'Could not load Help & Support.')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const submit = useCallback(async (e) => {
    e.preventDefault()
    if (!subject.trim()) return
    setSubmitting(true)
    setSubmitError('')
    setWall(null)
    try {
      const res = await fetch('/api/support/requests', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ subject: subject.trim(), body, priority }),
      })
      const data = await res.json().catch(() => ({}))
      if (res.status === 402) {
        setWall({ docs_url: data.docs_url, community_url: data.community_url, tier: data.tier })
        return
      }
      if (!res.ok) throw new Error(data.error || 'HTTP ' + res.status)
      setSubject('')
      setBody('')
      setPriority('P3')
      setRequests(prev => [data, ...prev])
    } catch (e) {
      setSubmitError(e.message || 'Could not submit your request.')
    } finally {
      setSubmitting(false)
    }
  }, [subject, body, priority])

  const closeRequest = useCallback(async (id) => {
    setClosingID(id)
    try {
      const res = await fetch(`/api/support/requests/${id}/close`, { method: 'POST' })
      if (!res.ok) throw new Error('HTTP ' + res.status)
      setRequests(prev => prev.map(r => (r.id === id ? { ...r, state: 'closed', breached: false } : r)))
    } catch {
      /* best-effort — a refresh will show the true state */
    } finally {
      setClosingID(null)
    }
  }, [])

  const tierLabel = TIER_LABELS[eligibility?.tier] || eligibility?.tier || '—'
  const walled = eligibility && !eligibility.available

  return (
    <Section
      title="Help & Support"
      desc="Submit a support request and track its status. Available channel and response target depend on your plan."
    >
      {loading && <p className="text-xs text-[var(--text-faint)]">Loading…</p>}
      {loadError && (
        <div className="mb-4 text-xs rounded px-3 py-2 bg-danger-soft text-danger">
          {loadError} <button onClick={load} className="focus-primary rounded underline hover:no-underline ml-1">Retry</button>
        </div>
      )}

      {!loading && eligibility && (
        <div className="mb-5 rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] px-4 py-3 flex items-center justify-between gap-4">
          <div className="min-w-0">
            <p className="text-sm text-[var(--text-primary)]">
              Plan: <span className="font-medium">{tierLabel}</span>
            </p>
            <p className="text-xs text-[var(--text-muted)] mt-0.5">
              {eligibility.available
                ? CHANNEL_LABELS[eligibility.channel] || eligibility.channel
                : 'No ticket channel on the Free plan — see documentation or community below.'}
            </p>
          </div>
          {!eligibility.available && (
            <a
              href="https://vulos.org/account/billing"
              target="_blank"
              rel="noreferrer"
              className="btn-primary text-sm shrink-0 no-underline"
            >
              Upgrade
            </a>
          )}
        </div>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* Submit form — only meaningfully actionable off the Free wall, but   */}
      {/* left visible (disabled) so the owner can see what upgrading unlocks. */}
      {/* ------------------------------------------------------------------ */}
      {!loading && (
        <form onSubmit={submit} className="mb-6">
          <fieldset disabled={walled || submitting} className="disabled:opacity-60">
            <Field label="Subject">
              <input
                value={subject}
                onChange={e => setSubject(e.target.value)}
                placeholder="Brief summary of the issue"
                maxLength={200}
                className="input"
                required
              />
            </Field>
            <Field label="Details" hint="Steps to reproduce, what you expected, what happened instead.">
              <textarea
                value={body}
                onChange={e => setBody(e.target.value)}
                rows={4}
                placeholder="Describe what's going on…"
                className="input"
              />
            </Field>
            <Field label="Priority">
              <div className="flex gap-2">
                {PRIORITIES.map(p => (
                  <button
                    key={p.value}
                    type="button"
                    onClick={() => setPriority(p.value)}
                    className={`text-xs px-3 py-1.5 rounded-lg border transition-colors ${
                      priority === p.value
                        ? 'accent-border bg-[var(--accent-soft)] text-[var(--text-primary)]'
                        : 'border-[var(--border-default)] bg-[var(--bg-surface)] text-[var(--text-tertiary)]'
                    }`}
                  >
                    {p.label}
                  </button>
                ))}
              </div>
            </Field>
          </fieldset>

          <button
            type="submit"
            disabled={walled || submitting || !subject.trim()}
            className="btn-primary focus-primary text-sm disabled:opacity-50"
          >
            {submitting ? 'Submitting…' : 'Submit request'}
          </button>

          {walled && (
            <p className="text-xs text-[var(--text-faint)] mt-2">
              Submitting is disabled on the Free plan. Upgrade above, or use the links below.
            </p>
          )}
          {submitError && (
            <div className="mt-3 text-xs rounded px-3 py-2 bg-danger-soft text-danger">{submitError}</div>
          )}
          {wall && (
            <div className="mt-3 text-xs rounded px-3 py-2 bg-warning-soft text-warning space-x-3">
              <span>No ticket channel on the Free plan.</span>
              {wall.docs_url && (
                <a href={wall.docs_url} target="_blank" rel="noreferrer" className="underline hover:no-underline">Docs</a>
              )}
              {wall.community_url && (
                <a href={wall.community_url} target="_blank" rel="noreferrer" className="underline hover:no-underline">Community</a>
              )}
            </div>
          )}
        </form>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* Prior requests                                                       */}
      {/* ------------------------------------------------------------------ */}
      {!loading && (
        <>
          <h3 className="text-xs uppercase tracking-wider text-[var(--text-muted)] font-medium mb-2">
            Your requests
          </h3>
          {requests.length === 0 && (
            <p className="text-xs text-[var(--text-faint)]">No support requests yet.</p>
          )}
          {requests.length > 0 && (
            <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)]">
              {requests.map(req => (
                <div key={req.id} className="px-4 py-3 bg-[var(--bg-surface)]">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="text-sm font-medium text-[var(--text-primary)] truncate">{req.subject}</span>
                        <StateBadge state={req.state} breached={req.breached} />
                      </div>
                      <p className="text-[11px] text-[var(--text-muted)] mt-0.5">
                        {TIER_LABELS[req.tier] || req.tier} · {req.priority} · {CHANNEL_LABELS[req.channel] || req.channel}
                      </p>
                      <p className="text-[11px] text-[var(--text-faint)] mt-0.5">
                        Opened {fmtDate(req.opened_at)}
                        {req.state === 'closed' && req.resolved_at ? ` · Closed ${fmtDate(req.resolved_at)}` : ''}
                        {req.state === 'open' ? ` · SLA target ${fmtDate(req.breach_at)}` : ''}
                      </p>
                    </div>
                    {req.state === 'open' && (
                      <button
                        onClick={() => closeRequest(req.id)}
                        disabled={closingID === req.id}
                        className="shrink-0 text-xs px-3 py-1.5 rounded-lg bg-[var(--bg-elevated)] text-[var(--text-secondary)] hover:text-[var(--text-primary)] transition-colors focus-primary disabled:opacity-40"
                      >
                        {closingID === req.id ? 'Closing…' : 'Close'}
                      </button>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </>
      )}

      <p className="text-[11px] text-[var(--text-faint)] mt-5 leading-relaxed">
        Requests are recorded on this box and classified by your plan's response target. This panel does not
        provide live chat; how a request reaches a human depends on your Vulos Cloud enrollment.
      </p>
    </Section>
  )
}
