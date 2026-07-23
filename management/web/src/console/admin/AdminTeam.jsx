/**
 * console/admin/AdminTeam.jsx  (/console/admin/admins)
 *
 * The admin TEAM page: list current admins, grant admin access to an existing
 * account by email, and revoke it — so a self-hoster or the Vulos team can add
 * teammates without DB surgery. Backed by:
 *   GET    /api/superadmin/admins            → { admins:[…], count }
 *   POST   /api/superadmin/admins            ← { email, totp? }   (grant)
 *   DELETE /api/superadmin/admins/{id}?totp= (revoke)
 *
 * Grant/revoke are already gated by the hardware-key admin session; when the
 * deployment sets VULOS_ADMIN_REAUTH_TOTP=1 the operator must also supply a
 * fresh authenticator code (the optional field below). The last remaining admin
 * can never be revoked (enforced server-side; the button is also hidden here).
 */

import { useState } from 'react'
import { Card, Pill, Button, Stat } from '../../ui/index.jsx'
import { useAdminResource, adminSend } from './useAdmin.js'
import { OpPage, OpHeader, OpGate } from './AdminShell.jsx'
import { fmtTime, shorten } from './format.js'

export default function AdminTeam() {
  const { data, loading, error, needsAdminSession, notOperator, reload } =
    useAdminResource('/api/superadmin/admins')

  const admins = data?.admins ?? []
  const count = admins.length

  // Grant form state.
  const [email, setEmail] = useState('')
  const [totp, setTotp] = useState('')
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState(null) // { tone:'good'|'danger', text }

  async function grant(e) {
    e.preventDefault()
    const addr = email.trim()
    if (!addr || busy) return
    setBusy(true); setNotice(null)
    try {
      await adminSend('/api/superadmin/admins', { method: 'POST', body: { email: addr, totp: totp.trim() } })
      setNotice({ tone: 'good', text: `${addr} is now an admin.` })
      setEmail(''); setTotp('')
      reload()
    } catch (err) {
      setNotice({ tone: 'danger', text: err.message })
    } finally {
      setBusy(false)
    }
  }

  async function revoke(row) {
    if (busy) return
    if (!window.confirm(`Revoke admin access for ${row.email}? Their admin session ends immediately.`)) return
    setBusy(true); setNotice(null)
    try {
      const qs = totp.trim() ? `?totp=${encodeURIComponent(totp.trim())}` : ''
      await adminSend(`/api/superadmin/admins/${encodeURIComponent(row.account_id)}${qs}`, { method: 'DELETE' })
      setNotice({ tone: 'good', text: `Revoked admin access for ${row.email}.` })
      setTotp('')
      reload()
    } catch (err) {
      setNotice({ tone: 'danger', text: err.message })
    } finally {
      setBusy(false)
    }
  }

  return (
    <OpPage>
      <OpHeader title="Admins" sub={`${count} with access`} />

      <OpGate
        loading={loading} error={error}
        needsAdminSession={needsAdminSession} notOperator={notOperator}
        onRetry={reload}
      >
        <div className="op-statrow">
          <Card><Stat label="Admins" value={String(count)} sublabel="accounts with console access" /></Card>
        </div>

        {/* Grant form */}
        <Card elevated hover={false} style={{ marginBottom: 'var(--sp-3)' }}>
          <div className="op-kicker">Grant admin access</div>
          <form onSubmit={grant}>
            <div className="op-filter" style={{ marginBottom: 'var(--sp-2)' }}>
              <label className="op-filter-label" htmlFor="grant-email">Account email</label>
              <input
                id="grant-email" className="op-input" type="email" value={email}
                onChange={(e) => setEmail(e.target.value)}
                placeholder="teammate@example.com" autoComplete="off" spellCheck={false}
                disabled={busy} required
              />
              <label className="op-filter-label" htmlFor="grant-totp">Auth code</label>
              <input
                id="grant-totp" className="op-input" type="text" inputMode="numeric" value={totp}
                onChange={(e) => setTotp(e.target.value)}
                placeholder="if required" autoComplete="one-time-code" spellCheck={false}
                style={{ minWidth: 140 }} disabled={busy}
              />
              <Button type="submit" variant="primary" size="sm" disabled={busy}>
                {busy ? 'Working…' : 'Grant admin'}
              </Button>
            </div>
          </form>
          <div className="op-empty-sub" style={{ margin: 0, maxWidth: '60ch' }}>
            The account must already exist (they sign up first). The auth code is only
            needed on deployments that require operator step-up.
          </div>
          {notice && (
            <div style={{ marginTop: 'var(--sp-2)' }}>
              <Pill color={notice.tone === 'good' ? 'good' : 'danger'} dot>{notice.text}</Pill>
            </div>
          )}
        </Card>

        {/* Admin list */}
        <Card hover={false} style={{ padding: 0, overflow: 'hidden' }}>
          <div className="op-thead" style={{ gridTemplateColumns: '1fr 200px 160px 120px' }} aria-hidden="true">
            <span>Email</span><span>Promoted by</span><span>When</span><span></span>
          </div>
          {admins.length === 0 && (
            <div className="op-empty">
              <div className="op-empty-title">No admins listed</div>
              <div className="op-empty-sub">Grant admin access to an account above.</div>
            </div>
          )}
          {admins.map((a) => (
            <div key={a.account_id} className="op-trow" style={{ gridTemplateColumns: '1fr 200px 160px 120px' }}>
              <span className="op-cell" title={a.account_id}>
                {a.email}{' '}
                {a.bootstrap && <Pill color="accent" dot>seeded</Pill>}
              </span>
              <span className="op-cell dim" title={a.promoted_by_email || a.promoted_by}>
                {a.bootstrap ? '—' : (a.promoted_by_email || shorten(a.promoted_by))}
              </span>
              <span className="op-cell mono">{fmtTime(a.promoted_at)}</span>
              <span className="op-cell" style={{ textAlign: 'right' }}>
                {count > 1 ? (
                  <Button variant="ghost" size="sm" onClick={() => revoke(a)} disabled={busy}>Revoke</Button>
                ) : (
                  <span className="op-cell dim" title="The last admin can't be revoked">last admin</span>
                )}
              </span>
            </div>
          ))}
        </Card>
      </OpGate>
    </OpPage>
  )
}
