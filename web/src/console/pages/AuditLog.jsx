/**
 * src/pages/app/AuditLog.jsx  (/console/audit)
 * Vulos Cloud Control Plane — Org audit trail (ORGADMIN-AUDIT-01)
 *
 * An admin-facing, read-only view of WHO did WHAT in the caller's organisation:
 * member add/remove, role changes, product-status changes, billing events and
 * invites. Every row is a real record from the CP admin audit log, surfaced via
 * GET /api/org/audit — session-authed, OWN-ORG only (the tenant is resolved
 * server-side; there is no org-id in any request), admin-gated (owner / admin /
 * billing-admin; a plain member gets 403), bounded + keyset-paginated.
 *
 * Data source (session-authed, own-org, admin-gated, IDOR-safe):
 *   GET /api/org/audit?action=<label>&cursor=<seq>&limit=<n>
 *     → { entries: [{ seq, id, ts, actor, action, target, metadata }],
 *         count, limit, next_cursor }
 *
 * Design contract (mirrors /console/usage + /console/invoices):
 *   - JSX only; zero hex — tokens only via var(--…)
 *   - Reuses Section/Card/Pill/Button from ../../ui/index.jsx
 *   - No own <nav>/<header>/<Footer>
 *   - Mono numerics; loading / empty / forbidden / error states everywhere
 *   - Responsive; ≥44px taps; prefers-reduced-motion honoured; a11y-first
 */

import { useState, useEffect, useCallback } from 'react'
import { Section, Card, Pill, Button } from '../../ui/index.jsx'
import {
  humanizeAction, actionTone, fmtAuditTime, relativeAuditTime,
  shortenId, metadataPairs, paginationView,
} from '../audit-format.js'

const PAGE_SIZE = 50

/* ── Data hook ─────────────────────────────────────────────────────────────
   Distinguishes a 403 (caller is not an org admin) from a real load error so
   the two render distinct, honest states. A null next_cursor ends pagination. */
function useAudit({ action, cursor }) {
  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [forbidden, setForbidden] = useState(false)

  const load = useCallback(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    setForbidden(false)
    const qs = new URLSearchParams({ limit: String(PAGE_SIZE) })
    if (action) qs.set('action', action)
    if (cursor) qs.set('cursor', cursor)
    fetch(`/api/org/audit?${qs.toString()}`, {
      credentials: 'include',
      headers: { Accept: 'application/json' },
    })
      .then(res => {
        if (res.status === 403) { if (!cancelled) { setForbidden(true); setLoading(false) } ; return null }
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json()
      })
      .then(json => { if (!cancelled && json) { setData(json); setLoading(false) } })
      .catch(err => { if (!cancelled) { setError(err.message); setLoading(false) } })
    return () => { cancelled = true }
  }, [action, cursor])

  // eslint-disable-next-line react-hooks/set-state-in-effect -- syncs the audit ledger; loading reflects the in-flight request.
  useEffect(() => load(), [load])

  return { data, loading, error, forbidden, reload: load }
}

/* ── Sub-components ────────────────────────────────────────────────────── */

function AuditRow({ entry }) {
  const [open, setOpen] = useState(false)
  const tone = actionTone(entry.action)
  const pairs = metadataPairs(entry.metadata)
  const hasDetail = pairs.length > 0 || !!entry.target
  const detailId = `au-detail-${entry.seq}`
  const label = humanizeAction(entry.action)
  const rel = relativeAuditTime(entry.ts)

  const Head = hasDetail ? 'button' : 'div'
  const headProps = hasDetail
    ? {
        onClick: () => setOpen(v => !v),
        'aria-expanded': open,
        'aria-controls': detailId,
        'aria-label': `${label}${entry.actor ? ` by ${entry.actor}` : ''} — ${fmtAuditTime(entry.ts)}. ${open ? 'Hide' : 'Show'} details`,
      }
    : {}

  return (
    <div className={`au-row${open ? ' open' : ''}`}>
      <Head className="au-row-main" {...headProps}>
        <span className="au-cell au-action">
          <Pill color={tone === 'faint' ? 'faint' : tone} dot>{label}</Pill>
        </span>
        <span className="au-cell au-actor" title={entry.actor || undefined}>
          {shortenId(entry.actor)}
        </span>
        <span className="au-cell au-target" title={entry.target || undefined}>
          {shortenId(entry.target)}
        </span>
        <span className="au-cell au-time">
          <time dateTime={entry.ts || undefined} title={fmtAuditTime(entry.ts)}>
            {fmtAuditTime(entry.ts)}
          </time>
          {rel && <span className="au-time-rel">{rel}</span>}
        </span>
        {hasDetail
          ? <span className="au-chevron" aria-hidden="true">{open ? '▾' : '▸'}</span>
          : <span className="au-chevron" aria-hidden="true" />}
      </Head>

      {open && hasDetail && (
        <div className="au-detail" id={detailId}>
          {entry.target && (
            <div className="au-detail-line">
              <span className="au-detail-key">Target</span>
              <span className="au-detail-val au-mono">{entry.target}</span>
            </div>
          )}
          {pairs.map(p => (
            <div className="au-detail-line" key={p.key}>
              <span className="au-detail-key">{p.key}</span>
              <span className="au-detail-val au-mono">{p.value}</span>
            </div>
          ))}
          <div className="au-detail-line">
            <span className="au-detail-key">Entry ID</span>
            <span className="au-detail-val au-mono">{entry.id || '—'}</span>
          </div>
        </div>
      )}
    </div>
  )
}

/* ── Scoped styles — zero hex; all var(--…) ────────────────────────────── */
const STYLES = `
.au-visually-hidden {
  position: absolute;
  width: 1px; height: 1px;
  padding: 0; margin: -1px;
  overflow: hidden; clip: rect(0 0 0 0);
  white-space: nowrap; border: 0;
}
.au-header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--sp-2);
  flex-wrap: wrap;
  margin-bottom: var(--sp-3);
}
.au-header-title {
  font-family: var(--font-mono);
  font-size: clamp(1.125rem, 2.2vw, 1.375rem);
  font-weight: 700;
  letter-spacing: -0.025em;
  color: var(--text-primary);
}
.au-header-sub { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); }

/* Filter bar */
.au-filter {
  display: flex;
  align-items: center;
  gap: var(--sp-1-5);
  flex-wrap: wrap;
  margin-bottom: var(--sp-3);
}
.au-filter-label {
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--text-ghost);
}
.au-filter-input {
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text-primary);
  background: var(--bg-elevated);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-sm);
  padding: 8px 12px;
  min-height: 40px;
  min-width: 200px;
}
.au-filter-input:focus-visible { outline: none; box-shadow: var(--focus-ring); border-color: var(--accent); }

/* Table */
.au-table-head {
  display: grid;
  grid-template-columns: 190px 1fr 1fr 170px 24px;
  gap: var(--sp-2);
  padding: 0 var(--sp-3) var(--sp-1-5);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  font-weight: 500;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--text-ghost);
}
.au-row { border-bottom: 1px solid var(--border-subtle); }
.au-row:last-child { border-bottom: none; }
.au-row-main {
  display: grid;
  grid-template-columns: 190px 1fr 1fr 170px 24px;
  gap: var(--sp-2);
  align-items: center;
  width: 100%;
  padding: var(--sp-2) var(--sp-3);
  background: transparent;
  border: none;
  text-align: left;
  min-height: 52px;
  font-family: var(--font-mono);
}
button.au-row-main { cursor: pointer; transition: background 120ms var(--ease); }
button.au-row-main:hover { background: var(--bg-hover); }
button.au-row-main:focus-visible { outline: none; box-shadow: inset var(--focus-ring); }
.au-cell { font-size: var(--text-sm); min-width: 0; }
.au-actor { color: var(--text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.au-target { color: var(--text-tertiary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.au-time {
  display: flex; flex-direction: column; gap: 1px;
  color: var(--text-tertiary);
  font-variant-numeric: tabular-nums;
  text-align: right;
}
.au-time-rel { font-size: var(--text-xs); color: var(--text-ghost); }
.au-chevron { color: var(--text-ghost); font-size: var(--text-xs); text-align: center; }

.au-detail {
  padding: var(--sp-2) var(--sp-3) var(--sp-3);
  background: var(--bg-surface);
  display: flex;
  flex-direction: column;
  gap: var(--sp-1);
}
.au-detail-line {
  display: grid;
  grid-template-columns: 140px 1fr;
  gap: var(--sp-2);
  align-items: baseline;
}
.au-detail-key {
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--text-ghost);
}
.au-detail-val {
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text-secondary);
}
.au-mono { word-break: break-all; }

/* Pager */
.au-pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--sp-2);
  margin-top: var(--sp-3);
  flex-wrap: wrap;
}
.au-pager-info { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-faint); }
.au-pager-btns { display: flex; gap: var(--sp-1-5); }

/* States */
.au-empty { text-align: center; padding: var(--sp-6) var(--sp-3); }
.au-empty-icon { color: var(--text-ghost); margin-bottom: var(--sp-2); display: flex; justify-content: center; }
.au-empty-title {
  font-family: var(--font-mono);
  font-size: var(--text-base);
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: var(--sp-1);
}
.au-empty-sub {
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text-faint);
  line-height: 1.6;
  max-width: 46ch;
  margin: 0 auto;
}
.au-state-error {
  padding: var(--sp-3);
  border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent);
  border-radius: var(--radius-lg);
  background: color-mix(in srgb, var(--danger) 6%, transparent);
  color: var(--text-secondary);
  display: flex; align-items: center; justify-content: space-between;
  gap: var(--sp-2); flex-wrap: wrap;
  font-family: var(--font-mono); font-size: var(--text-sm);
  margin-bottom: var(--sp-3);
}
.au-retry-btn {
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  font-weight: 500;
  color: var(--text-primary);
  background: var(--bg-elevated);
  border: 1px solid var(--border-emphasis);
  border-radius: var(--radius-sm);
  padding: 8px 14px;
  cursor: pointer;
  min-height: 44px;
  transition: border-color 120ms var(--ease), background 120ms var(--ease);
}
.au-retry-btn:hover { border-color: var(--accent); background: var(--bg-hover); }
.au-retry-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); border-color: var(--accent); }
.au-skel {
  border-radius: var(--radius-lg);
  background: linear-gradient(90deg, var(--bg-surface) 0%, var(--bg-hover) 50%, var(--bg-surface) 100%);
  background-size: 200% 100%;
  animation: auShimmer 1.4s ease-in-out infinite;
  height: 52px;
  margin-bottom: 2px;
}
@keyframes auShimmer { from { background-position: 200% 0; } to { background-position: -200% 0; } }
@media (prefers-reduced-motion: reduce) { .au-skel { animation: none; } }

/* Responsive: drop the target column, then the actor column, on narrow viewports */
@media (max-width: 860px) {
  .au-table-head { grid-template-columns: 160px 1fr 150px 20px; }
  .au-row-main   { grid-template-columns: 160px 1fr 150px 20px; }
  .au-table-head .au-target, .au-row-main .au-target { display: none; }
}
@media (max-width: 560px) {
  .au-table-head { grid-template-columns: 1fr 120px 20px; }
  .au-row-main   { grid-template-columns: 1fr 120px 20px; }
  .au-table-head .au-actor, .au-row-main .au-actor { display: none; }
  .au-detail-line { grid-template-columns: 1fr; gap: 2px; }
}
`

/* ── Default export ────────────────────────────────────────────────────── */
export default function AuditLog() {
  // Filter input is debounced into `action`; cursor stack drives pagination.
  const [actionInput, setActionInput] = useState('')
  const [action, setAction] = useState('')
  const [cursorStack, setCursorStack] = useState([]) // history of cursors we paged through
  const cursorView = paginationView(cursorStack, '')

  const { data, loading, error, forbidden, reload } = useAudit({ action, cursor: cursorView.cursor })

  // Debounce the action filter so typing doesn't fire a request per keystroke.
  useEffect(() => {
    const id = setTimeout(() => {
      setAction(actionInput.trim())
      setCursorStack([]) // a new filter resets pagination to the newest page
    }, 350)
    return () => clearTimeout(id)
  }, [actionInput])

  const entries = data?.entries ?? []
  const nextCursor = data?.next_cursor ?? ''
  const { hasPrev, hasNext, pageNumber } = paginationView(cursorStack, nextCursor)

  const goNext = () => { if (nextCursor) setCursorStack(s => [...s, nextCursor]) }
  const goPrev = () => setCursorStack(s => s.slice(0, -1))

  return (
    <Section slim>
      <style>{STYLES}</style>

      <div className="au-header">
        <span className="au-header-title">Audit log</span>
        <span className="au-header-sub">
          {entries.length > 0
            ? `${entries.length} event${entries.length === 1 ? '' : 's'}${hasPrev ? ' (older page)' : ''}`
            : 'Who did what in your organisation'}
        </span>
      </div>

      {/* Filter */}
      {!forbidden && (
        <div className="au-filter">
          <label className="au-filter-label" htmlFor="au-action-filter">Filter by action</label>
          <input
            id="au-action-filter"
            className="au-filter-input"
            type="text"
            value={actionInput}
            onChange={e => setActionInput(e.target.value)}
            placeholder="e.g. member_added, role_changed"
            autoComplete="off"
            spellCheck={false}
            aria-label="Filter audit events by action label"
          />
        </div>
      )}

      {/* Forbidden — caller is not an org admin */}
      {forbidden && (
        <Card>
          <div className="au-empty">
            <div className="au-empty-icon" aria-hidden="true">
              <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor"
                strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round">
                <rect x="4" y="10" width="16" height="10" rx="2" />
                <path d="M8 10V7a4 4 0 0 1 8 0v3" />
              </svg>
            </div>
            <div className="au-empty-title">Admins only</div>
            <div className="au-empty-sub">
              The organisation audit trail is available to owners and admins.
              Ask an admin for access if you need to review account activity.
            </div>
          </div>
        </Card>
      )}

      {/* Error */}
      {error && !loading && !forbidden && (
        <div className="au-state-error" role="alert">
          <span>Couldn&apos;t load the audit log right now.</span>
          <button className="au-retry-btn" onClick={() => reload()} aria-label="Retry loading the audit log">
            Try again
          </button>
        </div>
      )}

      {/* Loading */}
      {loading && !forbidden && (
        <Card hover={false} style={{ padding: 'var(--sp-2)' }} role="status" aria-live="polite" aria-busy="true">
          <span className="au-visually-hidden">Loading the audit log…</span>
          {[0, 1, 2, 3, 4, 5].map(i => <div key={i} className="au-skel" aria-hidden="true" />)}
        </Card>
      )}

      {/* Empty */}
      {!loading && !error && !forbidden && entries.length === 0 && (
        <Card>
          <div className="au-empty">
            <div className="au-empty-icon" aria-hidden="true">
              <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor"
                strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round">
                <path d="M4 5h16M4 12h16M4 19h10" />
              </svg>
            </div>
            <div className="au-empty-title">
              {action ? 'No matching events' : 'No activity recorded yet'}
            </div>
            <div className="au-empty-sub">
              {action
                ? `Nothing matches “${action}”. Clear the filter to see all events.`
                : 'Member changes, role updates, invites and billing events will appear here as they happen.'}
            </div>
          </div>
        </Card>
      )}

      {/* Loaded */}
      {!loading && !error && !forbidden && entries.length > 0 && (
        <>
          <Card hover={false} style={{ padding: 0, overflow: 'hidden' }}>
            <div className="au-table-head" aria-hidden="true">
              <span className="au-action">Action</span>
              <span className="au-actor">Actor</span>
              <span className="au-target">Target</span>
              <span className="au-time">When</span>
              <span />
            </div>
            {entries.map(entry => <AuditRow key={entry.seq ?? entry.id} entry={entry} />)}
          </Card>

          {(hasPrev || hasNext) && (
            <div className="au-pager">
              <span className="au-pager-info">
                {hasPrev ? `Page ${pageNumber}` : 'Newest events'}
              </span>
              <div className="au-pager-btns">
                <Button variant="ghost" size="sm" onClick={goPrev} disabled={!hasPrev} aria-label="Newer events">
                  ← Newer
                </Button>
                <Button variant="ghost" size="sm" onClick={goNext} disabled={!hasNext} aria-label="Older events">
                  Older →
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </Section>
  )
}
