/**
 * console/admin/AdminAccounts.jsx  (/console/admin/accounts)
 *
 * Operator account search + inline detail. Mirrors the Go superadmin accounts
 * list + detail pages, reading:
 *   GET /api/superadmin/accounts?q=&limit=&offset=   → [ AccountRow ]
 *   GET /api/superadmin/accounts/{id}                → AccountDetail
 *
 * Read-only in this pass (suspend / 2FA-reset mutations exist server-side but
 * are not surfaced here yet — see report).
 */

import { useState, useEffect, useCallback } from 'react'
import { Card, Pill, Button, Stat } from '../../ui/index.jsx'
import { adminFetch } from './useAdmin.js'
import { OpPage, OpHeader, OpGate } from './AdminShell.jsx'
import { fmtTime, shorten, boolPill } from './format.js'

function useAccounts(q) {
  const [state, setState] = useState({ loading: true, data: [], error: null, needsAdminSession: false, notOperator: false })
  const load = useCallback(() => {
    let cancelled = false
    setState((s) => ({ ...s, loading: true, error: null, needsAdminSession: false, notOperator: false }))
    const qs = new URLSearchParams({ limit: '50' })
    if (q) qs.set('q', q)
    adminFetch(`/api/superadmin/accounts?${qs.toString()}`)
      .then((json) => { if (!cancelled) setState({ loading: false, data: Array.isArray(json) ? json : (json?.accounts ?? []), error: null, needsAdminSession: false, notOperator: false }) })
      .catch((err) => {
        if (cancelled) return
        setState({ loading: false, data: [], error: err.needsAdminSession || err.notOperator ? null : err.message, needsAdminSession: !!err.needsAdminSession, notOperator: !!err.notOperator })
      })
    return () => { cancelled = true }
  }, [q])
  // eslint-disable-next-line react-hooks/set-state-in-effect -- syncs the account search result.
  useEffect(() => load(), [load])
  return { ...state, reload: load }
}

function AccountDetail({ id, onClose }) {
  const [d, setD] = useState(null)
  const [err, setErr] = useState(null)
  useEffect(() => {
    let cancelled = false
    setD(null); setErr(null)
    adminFetch(`/api/superadmin/accounts/${encodeURIComponent(id)}`)
      .then((json) => { if (!cancelled) setD(json) })
      .catch((e) => { if (!cancelled) setErr(e.message) })
    return () => { cancelled = true }
  }, [id])

  const susp = d ? boolPill(d.suspended, 'Suspended', 'Active') : null

  return (
    <Card elevated hover={false} style={{ marginBottom: 'var(--sp-3)' }}>
      <div className="op-header">
        <span className="op-title" style={{ fontSize: '1rem' }}>{d?.email || id}</span>
        <Button variant="ghost" size="sm" onClick={onClose}>Close ×</Button>
      </div>
      {err && <div className="op-empty-sub">Couldn&apos;t load account detail.</div>}
      {d && (
        <>
          <div className="op-statrow">
            <Stat label="Active sessions" value={String(d.active_sessions ?? 0)} />
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              <Pill color={susp.tone} dot>{susp.label}</Pill>
              {d.fleet_admin && <Pill color="accent" dot>Fleet admin</Pill>}
              {d.totp_enabled && <Pill color="good" dot>2FA on</Pill>}
              {d.email_verified && <Pill color="good" dot>Email verified</Pill>}
            </div>
          </div>
          <dl className="op-kv">
            <dt>Account ID</dt><dd>{d.id}</dd>
            <dt>Email</dt><dd>{d.email}</dd>
            <dt>Created</dt><dd>{fmtTime(d.created_at)}</dd>
            {d.last_login_at && (<><dt>Last login</dt><dd>{fmtTime(d.last_login_at)}</dd></>)}
          </dl>
          {Array.isArray(d.transactions) && d.transactions.length > 0 && (
            <div style={{ marginTop: 'var(--sp-3)' }}>
              <div className="op-kicker">Transactions</div>
              <div className="op-thead" style={{ gridTemplateColumns: '1fr 120px 120px 160px' }} aria-hidden="true">
                <span>Txn</span><span>Amount</span><span>Status</span><span>When</span>
              </div>
              {d.transactions.map((t) => (
                <div key={t.txn_id} className="op-trow" style={{ gridTemplateColumns: '1fr 120px 120px 160px' }}>
                  <span className="op-cell dim">{shorten(t.txn_id)}</span>
                  <span className="op-cell mono">{(t.amount_zar_cents / 100).toFixed(2)}</span>
                  <span className="op-cell">{t.status}</span>
                  <span className="op-cell mono">{fmtTime(t.created_at)}</span>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </Card>
  )
}

export default function AdminAccounts() {
  const [input, setInput] = useState('')
  const [q, setQ] = useState('')
  const [selected, setSelected] = useState(null)
  const { loading, data, error, needsAdminSession, notOperator, reload } = useAccounts(q)

  useEffect(() => {
    const id = setTimeout(() => setQ(input.trim()), 350)
    return () => clearTimeout(id)
  }, [input])

  return (
    <OpPage>
      <OpHeader title="Accounts" sub={`${data.length} shown`} />

      <div className="op-filter">
        <label className="op-filter-label" htmlFor="op-acct-q">Search</label>
        <input
          id="op-acct-q" className="op-input" type="text" value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="email or account id" autoComplete="off" spellCheck={false}
        />
      </div>

      {selected && <AccountDetail id={selected} onClose={() => setSelected(null)} />}

      <OpGate
        loading={loading} error={error}
        needsAdminSession={needsAdminSession} notOperator={notOperator}
        onRetry={reload}
      >
        {data.length === 0 ? (
          <Card>
            <div className="op-empty">
              <div className="op-empty-title">{q ? 'No matching accounts' : 'No accounts yet'}</div>
              <div className="op-empty-sub">{q ? `Nothing matches “${q}”.` : 'Accounts will appear here as they sign up.'}</div>
            </div>
          </Card>
        ) : (
          <Card hover={false} style={{ padding: 0, overflow: 'hidden' }}>
            <div className="op-thead" style={{ gridTemplateColumns: '1fr 220px 120px 150px' }} aria-hidden="true">
              <span>Email</span><span>Account ID</span><span>Status</span><span>Created</span>
            </div>
            {data.map((a) => {
              const susp = boolPill(a.suspended, 'Suspended', 'Active')
              return (
                <button
                  key={a.id} className="op-trow"
                  style={{ gridTemplateColumns: '1fr 220px 120px 150px' }}
                  onClick={() => setSelected(a.id)}
                  aria-label={`Open ${a.email}`}
                >
                  <span className="op-cell" title={a.email}>{a.email}</span>
                  <span className="op-cell dim" title={a.id}>{shorten(a.id, 12, 6)}</span>
                  <span className="op-cell"><Pill color={susp.tone} dot>{susp.label}</Pill></span>
                  <span className="op-cell mono">{fmtTime(a.created_at)}</span>
                </button>
              )
            })}
          </Card>
        )}
      </OpGate>
    </OpPage>
  )
}
