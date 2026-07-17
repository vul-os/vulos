/**
 * console/admin/AdminSecurity.jsx  (/console/admin/security)
 *
 * The operator security dashboard: recent WAF hits, bot flags, step-up
 * challenges, account-takeover (ATO) anomalies, honeypot hits, egress anomalies
 * and CT-log certs. Mirrors the Go security dashboard, reading
 *   GET /api/superadmin/security
 *
 * The security store encodes its rows with Go field names (no JSON tags), so the
 * payload keys are PascalCase (Ts, ClientIP, …) — reflected in the accessors below.
 */

import { useState } from 'react'
import { Card, Pill, Stat } from '../../ui/index.jsx'
import { useAdminResource } from './useAdmin.js'
import { OpPage, OpHeader, OpGate } from './AdminShell.jsx'
import { fmtTime, shorten } from './format.js'

/* Each section: how to render one row of a given event kind. */
const SECTIONS = [
  {
    key: 'waf', title: 'WAF hits', tone: 'danger',
    cols: ['Rule', 'Pattern', 'Client IP', 'Path', 'When'],
    grid: '140px 1fr 140px 1fr 150px',
    row: (e) => [e.RuleID, e.Pattern, e.ClientIP, e.Path, fmtTime(e.Ts)],
  },
  {
    key: 'bots', title: 'Bot flags', tone: 'warn',
    cols: ['Client IP', 'Score', 'Reason', 'When'],
    grid: '140px 90px 1fr 150px',
    row: (e) => [e.ClientIP, (e.Score ?? 0).toFixed(2), e.Reason, fmtTime(e.Ts)],
  },
  {
    key: 'stepups', title: 'Step-up challenges', tone: 'accent',
    cols: ['User', 'Client IP', 'Risk', 'When'],
    grid: '1fr 140px 90px 150px',
    row: (e) => [shorten(e.UserID), e.ClientIP, (e.RiskScore ?? 0).toFixed(2), fmtTime(e.Ts)],
  },
  {
    key: 'ato', title: 'Account-takeover anomalies', tone: 'danger',
    cols: ['User', 'Action', 'Client IP', 'When'],
    grid: '1fr 160px 140px 150px',
    row: (e) => [shorten(e.UserID), e.Action, e.ClientIP, fmtTime(e.Ts)],
  },
  {
    key: 'honeypot', title: 'Honeypot hits', tone: 'warn',
    cols: ['Email', 'Client IP', 'When'],
    grid: '1fr 140px 150px',
    row: (e) => [e.Email, e.ClientIP, fmtTime(e.Ts)],
  },
  {
    key: 'egress', title: 'Egress anomalies', tone: 'warn',
    cols: ['User', 'Bytes/hr', 'Baseline', 'σ', 'When'],
    grid: '1fr 120px 120px 80px 150px',
    row: (e) => [shorten(e.UserID), String(e.BytesHr ?? 0), (e.Baseline ?? 0).toFixed(0), (e.Sigma ?? 0).toFixed(1), fmtTime(e.Ts)],
  },
]

function EventTable({ section, rows }) {
  return (
    <Card hover={false} style={{ padding: 0, overflow: 'hidden', marginBottom: 'var(--sp-3)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--sp-2)', padding: 'var(--sp-3) var(--sp-3) var(--sp-2)' }}>
        <Pill color={section.tone} dot>{section.title}</Pill>
        <span className="op-sub">{rows.length}</span>
      </div>
      {rows.length === 0 ? (
        <div style={{ padding: '0 var(--sp-3) var(--sp-3)' }}>
          <span className="op-empty-sub">None recorded.</span>
        </div>
      ) : (
        <>
          <div className="op-thead" style={{ gridTemplateColumns: section.grid }} aria-hidden="true">
            {section.cols.map((c) => <span key={c}>{c}</span>)}
          </div>
          {rows.slice(0, 25).map((e, i) => (
            <div key={e.ID ?? i} className="op-trow" style={{ gridTemplateColumns: section.grid }}>
              {section.row(e).map((cell, j) => (
                <span key={j} className={`op-cell${j === section.cols.length - 1 ? ' mono' : ' dim'}`} title={String(cell)}>{cell || '—'}</span>
              ))}
            </div>
          ))}
        </>
      )}
    </Card>
  )
}

export default function AdminSecurity() {
  const { data, loading, error, needsAdminSession, notOperator, reload } =
    useAdminResource('/api/superadmin/security')
  const [tab, setTab] = useState('waf')

  const counts = SECTIONS.map((s) => ({ ...s, rows: data?.[s.key] ?? [] }))
  const active = counts.find((s) => s.key === tab) ?? counts[0]

  return (
    <OpPage>
      <OpHeader title="Security" sub="Recent defensive telemetry" />
      <OpGate loading={loading} error={error} needsAdminSession={needsAdminSession} notOperator={notOperator} onRetry={reload}>
        <div className="op-statrow">
          {counts.map((s) => (
            <button key={s.key} onClick={() => setTab(s.key)}
              style={{ textAlign: 'left', background: 'transparent', border: 'none', cursor: 'pointer', padding: 0 }}
              aria-pressed={tab === s.key}>
              <Card elevated={tab === s.key} hover>
                <Stat label={s.title} value={String(s.rows.length)} />
              </Card>
            </button>
          ))}
        </div>
        <EventTable section={active} rows={active.rows} />
      </OpGate>
    </OpPage>
  )
}
