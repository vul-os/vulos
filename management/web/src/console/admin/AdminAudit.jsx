/**
 * console/admin/AdminAudit.jsx  (/console/admin/audit)
 *
 * The platform-wide, hash-chained audit trail (operator view — every tenant +
 * platform rows). Mirrors the Go superadmin auditlog page, reading
 *   GET /api/superadmin/audit?actor=&action=&after=&limit=
 * which is served from the real auditlog store (not tenant-scoped; operator-gated).
 */

import { useState, useEffect, Fragment } from 'react'
import { Card, Pill, Button } from '../../ui/index.jsx'
import { adminFetch } from './useAdmin.js'
import { OpPage, OpHeader, OpGate } from './AdminShell.jsx'
import { fmtTime, relTime, shorten, actionTone } from './format.js'

const PAGE = 50

export default function AdminAudit() {
  const [actorInput, setActorInput] = useState('')
  const [actionInput, setActionInput] = useState('')
  const [actor, setActor] = useState('')
  const [action, setAction] = useState('')
  const [stack, setStack] = useState([]) // after-seq cursors we've paged through
  const [state, setState] = useState({ loading: true, rows: [], error: null, needsAdminSession: false, notOperator: false })
  const [open, setOpen] = useState(null)

  useEffect(() => {
    const id = setTimeout(() => { setActor(actorInput.trim()); setStack([]) }, 350)
    return () => clearTimeout(id)
  }, [actorInput])
  useEffect(() => {
    const id = setTimeout(() => { setAction(actionInput.trim()); setStack([]) }, 350)
    return () => clearTimeout(id)
  }, [actionInput])

  const after = stack.length > 0 ? stack[stack.length - 1] : ''

  useEffect(() => {
    let cancelled = false
    setState((s) => ({ ...s, loading: true, error: null, needsAdminSession: false, notOperator: false }))
    const qs = new URLSearchParams({ limit: String(PAGE) })
    if (actor) qs.set('actor', actor)
    if (action) qs.set('action', action)
    if (after) qs.set('after', String(after))
    adminFetch(`/api/superadmin/audit?${qs.toString()}`)
      .then((json) => { if (!cancelled) setState({ loading: false, rows: json?.rows ?? [], error: null, needsAdminSession: false, notOperator: false }) })
      .catch((err) => {
        if (cancelled) return
        setState({ loading: false, rows: [], error: err.needsAdminSession || err.notOperator ? null : err.message, needsAdminSession: !!err.needsAdminSession, notOperator: !!err.notOperator })
      })
    return () => { cancelled = true }
  }, [actor, action, after])

  const { loading, rows, error, needsAdminSession, notOperator } = state
  const hasPrev = stack.length > 0
  const hasNext = rows.length === PAGE
  const goNext = () => { if (rows.length > 0) setStack((s) => [...s, rows[rows.length - 1].seq]) }
  const goPrev = () => setStack((s) => s.slice(0, -1))

  return (
    <OpPage>
      <OpHeader title="Audit log" sub={hasPrev ? `Page ${stack.length + 1}` : 'Platform-wide, newest first'} />

      <div className="op-filter">
        <label className="op-filter-label" htmlFor="op-audit-actor">Actor</label>
        <input id="op-audit-actor" className="op-input" type="text" value={actorInput}
          onChange={(e) => setActorInput(e.target.value)} placeholder="email or id" autoComplete="off" spellCheck={false} />
        <label className="op-filter-label" htmlFor="op-audit-action">Action</label>
        <input id="op-audit-action" className="op-input" type="text" value={actionInput}
          onChange={(e) => setActionInput(e.target.value)} placeholder="e.g. admin.login" autoComplete="off" spellCheck={false} />
      </div>

      <OpGate loading={loading} error={error} needsAdminSession={needsAdminSession} notOperator={notOperator}>
        {rows.length === 0 ? (
          <Card>
            <div className="op-empty">
              <div className="op-empty-title">{actor || action ? 'No matching events' : 'No activity recorded yet'}</div>
              <div className="op-empty-sub">Operator, admin and system actions across the platform will appear here.</div>
            </div>
          </Card>
        ) : (
          <>
            <Card hover={false} style={{ padding: 0, overflow: 'hidden' }}>
              <div className="op-thead" style={{ gridTemplateColumns: '190px 1fr 1fr 160px' }} aria-hidden="true">
                <span>Action</span><span>Actor</span><span>Target</span><span>When</span>
              </div>
              {rows.map((e) => {
                const meta = e.metadata && Object.keys(e.metadata).length > 0
                const isOpen = open === (e.seq ?? e.id)
                return (
                  <div key={e.seq ?? e.id}>
                    <button className="op-trow" style={{ gridTemplateColumns: '190px 1fr 1fr 160px' }}
                      onClick={() => setOpen(isOpen ? null : (e.seq ?? e.id))}
                      aria-expanded={isOpen} aria-label={`${e.action} — ${fmtTime(e.ts)}`}>
                      <span className="op-cell"><Pill color={actionTone(e.action)} dot>{e.action}</Pill></span>
                      <span className="op-cell dim" title={e.actor}>{shorten(e.actor)}</span>
                      <span className="op-cell dim" title={e.target}>{shorten(e.target)}</span>
                      <span className="op-cell mono" title={fmtTime(e.ts)}>{relTime(e.ts) || fmtTime(e.ts)}</span>
                    </button>
                    {isOpen && (
                      <div style={{ padding: 'var(--sp-2) var(--sp-3) var(--sp-3)', background: 'var(--bg-surface)' }}>
                        <dl className="op-kv">
                          <dt>Seq</dt><dd>{e.seq}</dd>
                          {e.target && (<><dt>Target</dt><dd>{e.target}</dd></>)}
                          {meta && Object.entries(e.metadata).map(([k, v]) => (
                            <Fragment key={k}><dt>{k}</dt><dd>{String(v)}</dd></Fragment>
                          ))}
                          <dt>Entry ID</dt><dd>{e.id || '—'}</dd>
                        </dl>
                      </div>
                    )}
                  </div>
                )
              })}
            </Card>
            {(hasPrev || hasNext) && (
              <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 'var(--sp-3)', gap: 'var(--sp-2)' }}>
                <span className="op-sub">{hasPrev ? `Page ${stack.length + 1}` : 'Newest events'}</span>
                <div style={{ display: 'flex', gap: 'var(--sp-1-5)' }}>
                  <Button variant="ghost" size="sm" onClick={goPrev} disabled={!hasPrev}>← Newer</Button>
                  <Button variant="ghost" size="sm" onClick={goNext} disabled={!hasNext}>Older →</Button>
                </div>
              </div>
            )}
          </>
        )}
      </OpGate>
    </OpPage>
  )
}
