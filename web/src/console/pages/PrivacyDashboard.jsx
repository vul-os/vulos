/**
 * console/pages/PrivacyDashboard.jsx — /privacy
 *
 * Your data rights: request a full data export or account erasure, download your
 * account export directly, and review the requests you've filed. Wired to
 * management's cproutes:
 *   POST /api/compliance/request   { kind: "export" | "erasure", note? } → 201
 *   GET  /api/compliance/requests  → { requests: [{ id, kind, status, note, created_at }] }
 *   GET  /api/account/export       → downloadable JSON of the account's data
 *
 * Adapted from vulos-cloud's PrivacyDashboard.jsx onto the OSS compliance store.
 */

import { useState, useEffect, useCallback } from 'react'
import { Section, Card, Pill, Button } from '../../ui/index.jsx'

function statusTone(s) {
  const v = (s || '').toLowerCase()
  if (v === 'completed' || v === 'fulfilled' || v === 'done') return 'good'
  if (v === 'rejected' || v === 'failed' || v === 'denied') return 'danger'
  if (v === 'pending' || v === 'open' || v === 'received') return 'warn'
  return 'faint'
}

function fmtDate(iso) {
  if (!iso) return '—'
  try { return new Date(iso).toLocaleString(undefined, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) }
  catch { return '—' }
}

export default function PrivacyDashboard() {
  const [requests, setRequests] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(null) // 'export' | 'erasure' | null
  const [note, setNote] = useState('')
  const [flash, setFlash] = useState(null)

  const load = useCallback(() => {
    setLoading(true)
    setError(null)
    fetch('/api/compliance/requests', { credentials: 'include', headers: { Accept: 'application/json' } })
      .then((res) => { if (!res.ok) throw new Error(`HTTP ${res.status}`); return res.json() })
      .then((json) => { setRequests(json?.requests ?? []); setLoading(false) })
      .catch((err) => { setError(err.message); setLoading(false) })
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { load() }, [load])

  async function fileRequest(kind) {
    if (submitting) return
    if (kind === 'erasure' && !window.confirm('Request account erasure? This begins the process of permanently deleting your account and data.')) return
    setSubmitting(kind)
    setFlash(null)
    try {
      const res = await fetch('/api/compliance/request', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ kind, note: note.trim() || undefined }),
      })
      if (!res.ok) { const t = await res.text(); throw new Error(t || `HTTP ${res.status}`) }
      setFlash({ kind: 'good', text: `Your ${kind} request has been recorded.` })
      setNote('')
      load()
    } catch (err) {
      setFlash({ kind: 'danger', text: err.message })
    } finally {
      setSubmitting(null)
    }
  }

  return (
    <Section slim>
      <style>{STYLES}</style>
      <div className="pd-header">
        <h1 className="pd-title">Privacy &amp; your data</h1>
        <p className="pd-sub">Export or erase your account data, and track the requests you&apos;ve made.</p>
      </div>

      <div className="pd-actions-grid">
        <Card hover={false} className="pd-action">
          <h3 className="pd-action-title">Export your data</h3>
          <p className="pd-action-sub">Download a machine-readable copy of the data on your account, or file a formal export request.</p>
          <div className="pd-action-btns">
            <Button size="sm" href="/api/account/export">Download export</Button>
            <Button size="sm" variant="ghost" onClick={() => fileRequest('export')} disabled={submitting === 'export'}>
              {submitting === 'export' ? 'Filing…' : 'File export request'}
            </Button>
          </div>
        </Card>

        <Card hover={false} className="pd-action">
          <h3 className="pd-action-title">Erase your account</h3>
          <p className="pd-action-sub">Request permanent deletion of your account and its data. This is processed as a compliance request.</p>
          <div className="pd-action-btns">
            <Button size="sm" variant="ghost" onClick={() => fileRequest('erasure')} disabled={submitting === 'erasure'}>
              {submitting === 'erasure' ? 'Filing…' : 'Request erasure'}
            </Button>
          </div>
        </Card>
      </div>

      <label className="pd-note-label" htmlFor="pd-note">Add a note (optional)</label>
      <textarea
        id="pd-note"
        className="pd-input"
        rows={2}
        value={note}
        onChange={(e) => setNote(e.target.value)}
        placeholder="Context for your request…"
        maxLength={2000}
      />

      {flash && <div className={`pd-flash pd-flash-${flash.kind}`} role="status">{flash.text}</div>}

      <h2 className="pd-h2">Your requests</h2>
      {error && <div className="pd-flash pd-flash-danger" role="alert">Could not load requests: {error}</div>}
      {!error && loading && <div className="pd-muted">Loading…</div>}
      {!error && !loading && requests.length === 0 && (
        <div className="pd-muted">You haven&apos;t filed any data requests.</div>
      )}
      {!error && !loading && requests.length > 0 && (
        <div className="pd-list">
          {requests.map((r) => (
            <div className="pd-req" key={r.id}>
              <div className="pd-req-main">
                <span className="pd-req-kind">{r.kind}</span>
                {r.note && <span className="pd-req-note">{r.note}</span>}
                <span className="pd-req-when">{fmtDate(r.created_at)}</span>
              </div>
              <Pill color={statusTone(r.status)} dot>{r.status || 'pending'}</Pill>
            </div>
          ))}
        </div>
      )}
    </Section>
  )
}

const STYLES = `
  .pd-header { margin-bottom: var(--sp-4); }
  .pd-title { font-family: var(--font-mono); font-size: clamp(1.125rem, 2.2vw, 1.375rem); font-weight: 700; letter-spacing: -0.025em; margin: 0; }
  .pd-sub { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); margin-top: var(--sp-0-5); }
  .pd-actions-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: var(--sp-2-5); }
  .pd-action-title { font-size: var(--text-lg, 1rem); font-weight: 600; margin: 0 0 var(--sp-1); color: var(--text-primary); }
  .pd-action-sub { font-size: var(--text-sm); color: var(--text-faint); line-height: 1.6; margin-bottom: var(--sp-2-5); }
  .pd-action-btns { display: flex; gap: var(--sp-1-5); flex-wrap: wrap; }
  .pd-note-label { display: block; font-family: var(--font-mono); font-size: var(--text-xs); letter-spacing: 0.06em; text-transform: uppercase; color: var(--text-ghost); margin: var(--sp-3) 0 var(--sp-1); }
  .pd-input { width: 100%; font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-primary); background: var(--bg-elevated); border: 1px solid var(--border-strong); border-radius: var(--radius-sm); padding: 10px 12px; resize: vertical; box-sizing: border-box; }
  .pd-input:focus-visible { outline: none; box-shadow: var(--focus-ring); border-color: var(--accent); }
  .pd-flash { font-family: var(--font-mono); font-size: var(--text-sm); padding: var(--sp-1-5) var(--sp-2-5); border-radius: var(--radius-lg); margin-top: var(--sp-2); }
  .pd-flash-good { color: var(--good); border: 1px solid color-mix(in srgb, var(--good) 30%, var(--border-strong)); }
  .pd-flash-danger { color: var(--danger); border: 1px solid color-mix(in srgb, var(--danger) 30%, var(--border-strong)); }
  .pd-h2 { font-family: var(--font-mono); font-size: var(--text-xs); font-weight: 500; letter-spacing: 0.09em; text-transform: uppercase; color: var(--text-ghost); margin: var(--sp-5) 0 var(--sp-1-5); }
  .pd-muted { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); padding: var(--sp-2) 0; }
  .pd-list { border: 1px solid var(--border-strong); border-radius: var(--radius-lg); overflow: hidden; background: var(--bg-surface); }
  .pd-req { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-2); padding: var(--sp-2) var(--sp-2-5); border-bottom: 1px solid var(--border-strong); min-height: 52px; }
  .pd-req:last-child { border-bottom: none; }
  .pd-req-main { display: flex; align-items: baseline; gap: var(--sp-1-5); min-width: 0; flex-wrap: wrap; }
  .pd-req-kind { font-family: var(--font-mono); font-size: var(--text-sm); font-weight: 600; color: var(--text-primary); text-transform: capitalize; }
  .pd-req-note { font-size: var(--text-sm); color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; }
  .pd-req-when { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-ghost); }
`
