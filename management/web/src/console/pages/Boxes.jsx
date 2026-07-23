/**
 * console/pages/Boxes.jsx — /boxes
 *
 * Your Vulos OS boxes — the machines running the OS, bound to your account via
 * enrollment. Wired to GET /api/fleet/devices (account-scoped). Managed / hosted
 * box PROVISIONING is a commercial capability: it renders through the commercial
 * seam's <ProvisioningSlot/>, which is empty in the OSS management build (a
 * self-hoster brings their own boxes and enrolls them) and injected by the cloud
 * build.
 *
 * Adapted from vulos-cloud's Boxes.jsx: in cloud "Boxes" was the hosted-OS-box
 * console over the commercial /api/box/* backend (which does not exist in the OSS
 * control plane); here it is the operational view of the boxes you own, plus the
 * commercial provisioning slot.
 */

import { Section, Card, Pill, Button } from '../../ui/index.jsx'
import { toFullPath } from '../../router.jsx'
import { ProvisioningSlot, commercial } from '@vulos/commercial-seam'
import { useFleetDevices, deviceTone } from '../useFleet.js'
import { relativeTime } from '../status-format.js'

function BoxCard({ d }) {
  const tone = deviceTone(d)
  const status = d.decommissioned ? 'decommissioned' : (d.status || d.health || 'unknown')
  return (
    <Card className="bx-card">
      <div className="bx-card-top">
        <div className="bx-card-id">
          <span className="bx-card-name">{d.name || 'Unnamed box'}</span>
          <span className="bx-card-ulid">{d.ulid}</span>
        </div>
        <Pill color={tone} dot>{status}</Pill>
      </div>
      <div className="bx-card-grid">
        <div className="bx-cell">
          <span className="bx-cell-label">Version</span>
          <span className="bx-cell-value">{d.version || '—'}</span>
        </div>
        <div className="bx-cell">
          <span className="bx-cell-label">Channel</span>
          <span className="bx-cell-value">{d.channel || '—'}</span>
        </div>
        <div className="bx-cell">
          <span className="bx-cell-label">Last seen</span>
          <span className="bx-cell-value">{d.last_heartbeat ? relativeTime(d.last_heartbeat) : 'never'}</span>
        </div>
        <div className="bx-cell">
          <span className="bx-cell-label">Crashes</span>
          <span className="bx-cell-value">{d.crash_count ?? 0}</span>
        </div>
      </div>
    </Card>
  )
}

export default function Boxes() {
  const { devices, loading, error, reload } = useFleetDevices()
  const active = devices.filter((d) => !d.decommissioned)

  return (
    <Section slim>
      <style>{STYLES}</style>
      <div className="bx-header">
        <div>
          <span className="bx-eyebrow">Operate</span>
          <h1 className="bx-header-title">Boxes</h1>
          <p className="bx-header-sub">The machines running your Vulos OS.</p>
        </div>
        <button className="bx-retry-btn" onClick={reload} disabled={loading} aria-busy={loading}>
          ↺ {loading ? 'Refreshing…' : 'Refresh'}
        </button>
      </div>

      {/* Commercial: managed-box provisioning (empty/null in OSS). */}
      <ProvisioningSlot context={{ surface: 'boxes' }} onChange={reload} />

      {error && <div className="bx-state-error" role="alert">Could not load your boxes: {error}</div>}

      {!error && loading && <div className="bx-muted">Loading boxes…</div>}

      {!error && !loading && active.length === 0 && (
        <Card hover={false} className="bx-firstrun">
          <div className="bx-firstrun-title">No boxes yet</div>
          <div className="bx-firstrun-sub">
            Boot the Vulos OS on a machine and enroll it to see it here.
            {commercial.enabled ? ' Or provision a managed box above.' : ''}
          </div>
          <Button size="sm" href={toFullPath('/enroll')} data-router>Enroll a box</Button>
        </Card>
      )}

      {!error && !loading && active.length > 0 && (
        <div className="bx-grid">
          {active.map((d) => <BoxCard key={d.ulid} d={d} />)}
        </div>
      )}
    </Section>
  )
}

const STYLES = `
  .bx-header { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-2); flex-wrap: wrap; margin-bottom: var(--sp-3); }
  .bx-eyebrow { font-family: var(--font-mono); font-size: var(--text-xs); letter-spacing: 0.14em; text-transform: uppercase; color: var(--text-ghost); }
  .bx-header-title { font-family: var(--font-mono); font-size: clamp(1.125rem, 2.2vw, 1.375rem); font-weight: 700; letter-spacing: -0.025em; margin: var(--sp-1) 0 var(--sp-0-5); }
  .bx-header-sub { font-size: var(--text-sm); color: var(--text-faint); }
  .bx-retry-btn { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--accent); background: transparent; border: 1px solid var(--border-strong); border-radius: var(--radius-sm); padding: 8px 12px; min-height: 36px; cursor: pointer; flex-shrink: 0; }
  .bx-retry-btn:hover { border-color: var(--border-emphasis); }
  .bx-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: var(--sp-2-5); margin-top: var(--sp-2); }
  .bx-card-top { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-2); margin-bottom: var(--sp-2-5); }
  .bx-card-id { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .bx-card-name { font-family: var(--font-mono); font-size: var(--text-base, 0.9rem); font-weight: 700; color: var(--text-primary); }
  .bx-card-ulid { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-ghost); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bx-card-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--sp-2); }
  .bx-cell { display: flex; flex-direction: column; gap: 2px; }
  .bx-cell-label { font-family: var(--font-mono); font-size: var(--text-xs); letter-spacing: 0.06em; text-transform: uppercase; color: var(--text-ghost); }
  .bx-cell-value { font-family: var(--font-mono); font-size: var(--text-sm); font-weight: 600; color: var(--text-primary); font-variant-numeric: tabular-nums; }
  .bx-firstrun { text-align: center; padding: var(--sp-5) var(--sp-3); }
  .bx-firstrun-title { font-family: var(--font-mono); font-size: var(--text-base); font-weight: 700; color: var(--text-secondary); margin-bottom: var(--sp-1); }
  .bx-firstrun-sub { font-size: var(--text-sm); color: var(--text-faint); line-height: 1.6; max-width: 46ch; margin: 0 auto var(--sp-2-5); }
  .bx-muted { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); padding: var(--sp-3) 0; }
  .bx-state-error { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--danger); padding: var(--sp-2) var(--sp-3); border: 1px solid color-mix(in srgb, var(--danger) 30%, var(--border-strong)); border-radius: var(--radius-lg); margin-bottom: var(--sp-3); }
`
