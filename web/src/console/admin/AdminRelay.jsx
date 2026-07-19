/**
 * console/admin/AdminRelay.jsx  (/console/admin/relay)
 *
 * The operator RELAY-SCALING panel: the live state of the #41 control loop that
 * decides how many relays each region should run and (for an actuating
 * provisioner) converges the fleet toward it. Reads
 *   GET /api/superadmin/relay-scale
 * which snapshots the SAME running loop the control plane acts on — real data,
 * never a mock — behind the full RequireSuperAdmin gate.
 *
 * Payload shape:
 *   { controller: { provisioner, actuated, last_error, regions: [
 *       { region, current, desired, draining, last_action, last_action_at, reason } ] },
 *     demand:     { regions: [ { region, saturation } ] } }
 *
 * `actuated=false` (manual/external) means the CP does not scale the fleet
 * itself — the desired counts are advisory, for the operator's own scaler. That
 * distinction is surfaced honestly here, not hidden.
 */

import { Card, Pill, Stat } from '../../ui/index.jsx'
import { useAdminResource } from './useAdmin.js'
import { OpPage, OpHeader, OpGate } from './AdminShell.jsx'
import { relTime } from './format.js'

/** Map a control-loop action label to a Pill tone. */
function actionTone(action) {
  switch (action) {
    case 'scale_up': return 'good'
    case 'destroy':
    case 'drain': return 'warn'
    case 'advisory': return 'accent'
    case 'cooldown': return 'faint'
    default: return 'faint' // hold / unknown
  }
}

export default function AdminRelay() {
  const { data, loading, error, needsAdminSession, notOperator, reload } =
    useAdminResource('/api/superadmin/relay-scale')

  const ctrl = data?.controller ?? {}
  const regions = ctrl.regions ?? []
  const actuated = !!ctrl.actuated
  const provisioner = ctrl.provisioner || 'manual'

  // Join per-region saturation from the demand snapshot (same store).
  const satByRegion = {}
  for (const r of data?.demand?.regions ?? []) satByRegion[r.region] = r.saturation

  const draining = regions.reduce((n, r) => n + (r.draining || 0), 0)
  const grid = '150px 90px 90px 90px 110px 130px 1fr'

  return (
    <OpPage>
      <OpHeader title="Relay scaling" sub="Live #41 control-loop state" />
      <OpGate loading={loading} error={error} needsAdminSession={needsAdminSession} notOperator={notOperator} onRetry={reload}>
        <div className="op-statrow">
          <Card><Stat label="Provisioner" value={provisioner} /></Card>
          <Card>
            <Stat
              label="Mode"
              value={actuated ? 'Actuating' : 'Advisory'}
              sublabel={actuated ? 'CP scales the fleet' : 'your scaler acts on desired'}
            />
          </Card>
          <Card><Stat label="Regions" value={String(regions.length)} /></Card>
          <Card><Stat label="Draining" value={String(draining)} sublabel="nodes bleeding off" /></Card>
        </div>

        {ctrl.last_error && (
          <Card hover={false} style={{ marginBottom: 'var(--sp-3)', borderColor: 'color-mix(in srgb, var(--danger) 40%, var(--border-strong))' }}>
            <Pill color="danger" dot>Provider holding</Pill>
            <span className="op-sub" style={{ marginLeft: 'var(--sp-2)' }}>{ctrl.last_error}</span>
          </Card>
        )}

        <Card hover={false} style={{ padding: 0, overflow: 'hidden' }}>
          <div className="op-thead" style={{ gridTemplateColumns: grid }} aria-hidden="true">
            <span>Region</span><span>Current</span><span>Desired</span><span>Draining</span>
            <span>Saturation</span><span>Last action</span><span>Reason</span>
          </div>
          {regions.length === 0 ? (
            <div className="op-empty">
              <div className="op-empty-title">No region load observed yet</div>
              <div className="op-empty-sub">
                Relay PoPs (or an aggregator) push per-region load to
                <code> POST /api/relay/scale/observe</code>; once load arrives, the
                loop&apos;s per-region desired counts appear here.
              </div>
            </div>
          ) : (
            regions.map((r) => {
              const sat = satByRegion[r.region]
              return (
                <div key={r.region} className="op-trow" style={{ gridTemplateColumns: grid }}>
                  <span className="op-cell" title={r.region}>{r.region}</span>
                  <span className="op-cell mono">{r.current ?? 0}</span>
                  <span className="op-cell mono" style={{ color: 'var(--text-primary)' }}>{r.desired ?? 0}</span>
                  <span className="op-cell mono">{r.draining ?? 0}</span>
                  <span className="op-cell mono">{sat == null ? '—' : `${(sat * 100).toFixed(0)}%`}</span>
                  <span className="op-cell">
                    <Pill color={actionTone(r.last_action)} dot>{r.last_action || 'hold'}</Pill>
                    {r.last_action_at && <span className="op-sub" style={{ marginLeft: 6 }}>{relTime(r.last_action_at)}</span>}
                  </span>
                  <span className="op-cell dim" title={r.reason}>{r.reason || '—'}</span>
                </div>
              )
            })
          )}
        </Card>
      </OpGate>
    </OpPage>
  )
}
