import { useState, useEffect, useCallback } from 'react'

// ---------------------------------------------------------------------------
// PrivacyPanel — POPIA / GDPR data-subject request intake (Settings → Privacy).
//
// Endpoints:
//   POST /api/compliance/requests  { kind: 'export'|'erasure', note }  → Request
//   GET  /api/compliance/requests  → Request[] (caller's own, newest first)
//
// This is deliberately a REQUEST RECORDER, not an automated engine: submitting
// a request creates a timestamped, referenceable record for the box owner to
// act on within the statutory window (GDPR Art.15/17/20, POPIA s23-25). It does
// NOT itself export or delete anything, and the copy below says so plainly.
//
// COMPLEMENTARY to Settings → Export My Data (instant self-serve .zip download):
// use that when you just want your data now. Use this form when you want a
// formal, dated record of the request on file — including erasure, which has
// no self-serve mechanic at all.
// ---------------------------------------------------------------------------

const KINDS = [
  {
    value: 'export',
    label: 'Access / export my data',
    desc: 'A formal record that you requested a copy of your personal data (GDPR Art. 15/20, POPIA s23).',
  },
  {
    value: 'erasure',
    label: 'Erase my data',
    desc: 'A formal record that you requested deletion of your account and personal data (GDPR Art. 17, POPIA s24-25).',
  },
]

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

function statusBadgeClass(status) {
  // Only "received" exists today (fulfilment is manual, off-system), but the
  // badge is written to degrade gracefully if a future status is added.
  if (status === 'received') return 'bg-warning-soft text-warning'
  return 'bg-success-soft text-success'
}

function formatDate(iso) {
  try {
    return new Date(iso).toLocaleString(undefined, {
      year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
    })
  } catch {
    return iso
  }
}

export default function PrivacyPanel() {
  const [kind, setKind] = useState('export')
  const [note, setNote] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState('')
  const [submitted, setSubmitted] = useState(null) // last successfully created request

  const [requests, setRequests] = useState([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')

  const loadRequests = useCallback(async () => {
    setLoading(true)
    setLoadError('')
    try {
      const res = await fetch('/api/compliance/requests', { credentials: 'same-origin' })
      if (res.status === 401) throw new Error('Your session has expired. Sign in again to view your requests.')
      if (!res.ok) throw new Error(`Could not load requests (HTTP ${res.status}).`)
      const data = await res.json()
      setRequests(Array.isArray(data) ? data : [])
    } catch (err) {
      setLoadError(err?.message || 'Could not load your prior requests.')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { loadRequests() }, [loadRequests])

  const submit = useCallback(async (e) => {
    e.preventDefault()
    if (submitting) return
    setSubmitting(true)
    setSubmitError('')
    setSubmitted(null)
    try {
      const res = await fetch('/api/compliance/requests', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kind, note: note.trim() }),
      })
      if (res.status === 401) throw new Error('Your session has expired. Sign in again, then retry.')
      if (!res.ok) throw new Error(`Could not submit your request (HTTP ${res.status}).`)
      const req = await res.json()
      setSubmitted(req)
      setNote('')
      setRequests(prev => [req, ...prev])
    } catch (err) {
      setSubmitError(err?.message || 'Something went wrong submitting your request.')
    } finally {
      setSubmitting(false)
    }
  }, [kind, note, submitting])

  return (
    <Section
      title="Privacy Requests"
      desc="Submit a formal data-subject request under GDPR or POPIA. This creates a dated, referenceable record for the box owner to act on — it does not itself export or delete anything."
    >
      {/* Pointer to the self-serve export, so this doesn't read as a duplicate. */}
      <p className="text-xs text-[var(--text-faint)] leading-relaxed mb-6 px-4 py-3 rounded-lg bg-[var(--bg-surface)] border border-[var(--border-default)]">
        Want your data right now instead of filing a request? Use{' '}
        <span className="text-[var(--text-tertiary)]">Settings → Export My Data</span> for an instant,
        self-serve download. Use the form below only when you want a formal record on file — this is the
        only way to request erasure, which has no self-serve download.
      </p>

      {/* --------------------------------------------------------------- */}
      {/* Submit form                                                      */}
      {/* --------------------------------------------------------------- */}
      <form onSubmit={submit}>
        <div className="space-y-2 mb-4">
          {KINDS.map(opt => (
            <label
              key={opt.value}
              htmlFor={`privacy-kind-${opt.value}`}
              className={`flex items-start gap-3 rounded-lg border px-4 py-3 cursor-pointer transition-colors ${
                kind === opt.value
                  ? 'accent-border bg-[var(--accent-soft)]'
                  : 'border-[var(--border-default)] bg-[var(--bg-surface)]'
              }`}
            >
              <input
                id={`privacy-kind-${opt.value}`}
                type="radio"
                name="privacy-kind-radio"
                value={opt.value}
                checked={kind === opt.value}
                onChange={() => setKind(opt.value)}
                className="mt-1 accent-blue-500"
              />
              <div className="flex-1 min-w-0">
                <span className="text-sm font-medium text-[var(--text-primary)]">{opt.label}</span>
                <p className="text-xs text-[var(--text-muted)] mt-0.5">{opt.desc}</p>
              </div>
            </label>
          ))}
        </div>

        <label className="block text-xs text-[var(--text-muted)] mb-1" htmlFor="privacy-note">
          Note (optional)
        </label>
        <textarea
          id="privacy-note"
          value={note}
          onChange={e => setNote(e.target.value)}
          placeholder="Anything the operator should know about this request…"
          rows={3}
          maxLength={2000}
          className="input w-full resize-y"
        />

        <button
          type="submit"
          disabled={submitting}
          aria-busy={submitting}
          className="btn-primary focus-primary text-sm mt-3 disabled:opacity-60 disabled:cursor-progress"
        >
          {submitting ? 'Submitting…' : 'Submit Request'}
        </button>

        <div aria-live="polite" className="mt-3">
          {submitted && (
            <div className="text-xs rounded px-3 py-2 bg-success-soft text-success">
              Request received. Reference: <code className="text-[var(--text-primary)]">{submitted.id}</code>.
              The box owner will act on it within the statutory window.
            </div>
          )}
          {submitError && (
            <div className="text-xs rounded px-3 py-2 bg-danger-soft text-danger">
              {submitError}
            </div>
          )}
        </div>
      </form>

      {/* --------------------------------------------------------------- */}
      {/* Prior requests                                                   */}
      {/* --------------------------------------------------------------- */}
      <h3 className="text-xs uppercase text-[var(--text-muted)] tracking-wider mt-8 mb-2">
        Your requests
      </h3>

      {loading && (
        <div className="flex items-center gap-2 text-xs text-[var(--text-tertiary)]">
          <span className="inline-block w-3 h-3 spinner" aria-hidden="true" />
          Loading your requests…
        </div>
      )}

      {!loading && loadError && (
        <div className="text-xs rounded px-3 py-2 bg-danger-soft text-danger">
          {loadError}{' '}
          <button onClick={loadRequests} className="focus-primary rounded underline hover:no-underline ml-1">
            Retry
          </button>
        </div>
      )}

      {!loading && !loadError && requests.length === 0 && (
        <p className="text-xs text-[var(--text-faint)]">No requests filed yet.</p>
      )}

      {!loading && !loadError && requests.length > 0 && (
        <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)]">
          {requests.map(req => (
            <div key={req.id} className="px-4 py-3 bg-[var(--bg-surface)]">
              <div className="flex items-center justify-between gap-2 mb-0.5">
                <span className="text-sm font-medium text-[var(--text-primary)] capitalize">
                  {req.kind === 'export' ? 'Access / export' : 'Erasure'}
                </span>
                <span className={`text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded ${statusBadgeClass(req.status)}`}>
                  {req.status}
                </span>
              </div>
              <p className="text-xs text-[var(--text-muted)]">
                {formatDate(req.created_at)} · <code className="text-[var(--text-faint)]">{req.id}</code>
              </p>
              {req.note && <p className="text-xs text-[var(--text-faint)] mt-1 leading-relaxed">{req.note}</p>}
            </div>
          ))}
        </div>
      )}
    </Section>
  )
}
