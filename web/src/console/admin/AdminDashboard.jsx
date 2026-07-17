/**
 * console/admin/AdminDashboard.jsx  (/console/admin)
 *
 * The operator cockpit: super-admin + account counts and the most recent
 * platform audit rows. Mirrors the Go superadmin dashboard (dashboardData),
 * reading GET /api/superadmin/dashboard.
 */

import { Card, Stat, Pill } from '../../ui/index.jsx'
import { useAdminResource } from './useAdmin.js'
import { OpPage, OpHeader, OpGate } from './AdminShell.jsx'
import { fmtTime, relTime, shorten, actionTone } from './format.js'

export default function AdminDashboard() {
  const { data, loading, error, needsAdminSession, notOperator, reload } =
    useAdminResource('/api/superadmin/dashboard')

  const recent = data?.recent_audit ?? []

  return (
    <OpPage>
      <OpHeader title="Dashboard" sub="Fleet health at a glance" />
      <OpGate
        loading={loading} error={error}
        needsAdminSession={needsAdminSession} notOperator={notOperator}
        onRetry={reload}
      >
        <div className="op-statrow">
          <Card><Stat label="Accounts" value={(data?.account_count ?? 0).toLocaleString()} /></Card>
          <Card><Stat label="Super-admins" value={(data?.superadmin_count ?? 0).toLocaleString()} /></Card>
          <Card><Stat label="Recent events" value={String(recent.length)} sublabel="last audit page" /></Card>
        </div>

        <Card hover={false} style={{ padding: 0, overflow: 'hidden' }}>
          <div className="op-thead" style={{ gridTemplateColumns: '180px 1fr 1fr 160px' }} aria-hidden="true">
            <span>Action</span><span>Actor</span><span>Target</span><span>When</span>
          </div>
          {recent.length === 0 && (
            <div className="op-empty">
              <div className="op-empty-title">No activity yet</div>
              <div className="op-empty-sub">Operator and admin actions will appear here as they happen.</div>
            </div>
          )}
          {recent.map((e) => (
            <div key={e.seq ?? e.id} className="op-trow" style={{ gridTemplateColumns: '180px 1fr 1fr 160px' }}>
              <span className="op-cell"><Pill color={actionTone(e.action)} dot>{e.action}</Pill></span>
              <span className="op-cell dim" title={e.actor}>{shorten(e.actor)}</span>
              <span className="op-cell dim" title={e.target}>{shorten(e.target)}</span>
              <span className="op-cell mono" title={fmtTime(e.ts)}>{relTime(e.ts) || fmtTime(e.ts)}</span>
            </div>
          ))}
        </Card>
      </OpGate>
    </OpPage>
  )
}
