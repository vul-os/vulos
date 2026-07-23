/**
 * console/pages/Devices.jsx — /devices
 *
 * The account's enrolled devices as a manageable table: version, channel, health,
 * last heartbeat, with a decommission action. Wired to management's fleet API:
 *   GET  /api/fleet/devices?account_id=<id>   — list (account-scoped)
 *   POST /api/fleet/decommission { ulid }      — retire a device
 *
 * Adapted from vulos-cloud's Devices.jsx — the cloud version carried org / OTA /
 * GPU / SSH-recovery surfaces that are commercial or not part of the OSS control
 * plane; this is the operational device roster every self-hoster needs.
 */

import { useState } from 'react'
import { Section, Card, Pill, Button } from '../../ui/index.jsx'
import { toFullPath } from '../../router.jsx'
import { useFleetDevices, deviceTone } from '../useFleet.js'
import { relativeTime } from '../status-format.js'

export default function Devices() {
  const { devices, loading, error, reload } = useFleetDevices()
  const [busyId, setBusyId] = useState(null)
  const [actionErr, setActionErr] = useState(null)

  async function decommission(ulid) {
    if (!window.confirm('Decommission this device? It will lose access to your account.')) return
    setBusyId(ulid)
    setActionErr(null)
    try {
      const res = await fetch('/api/fleet/decommission', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ ulid }),
      })
      if (!res.ok && res.status !== 204) {
        const t = await res.text()
        throw new Error(t || `HTTP ${res.status}`)
      }
      reload()
    } catch (err) {
      setActionErr(err.message)
    } finally {
      setBusyId(null)
    }
  }

  return (
    <Section slim>
      <style>{STYLES}</style>
      <div className="dv-header">
        <div>
          <span className="dv-eyebrow">Operate</span>
          <h1 className="dv-title">Devices</h1>
          <p className="dv-sub">Every device enrolled to your account.</p>
        </div>
        <button className="dv-retry-btn" onClick={reload} disabled={loading} aria-busy={loading}>
          ↺ {loading ? 'Refreshing…' : 'Refresh'}
        </button>
      </div>

      {(error || actionErr) && (
        <div className="dv-state-error" role="alert">{error ? `Could not load devices: ${error}` : actionErr}</div>
      )}

      {!error && loading && <div className="dv-muted">Loading devices…</div>}

      {!error && !loading && devices.length === 0 && (
        <Card hover={false} className="dv-empty">
          <div className="dv-empty-title">No devices enrolled</div>
          <div className="dv-empty-sub">Enroll a box to see it listed here.</div>
          <Button size="sm" href={toFullPath('/enroll')} data-router>Enroll a device</Button>
        </Card>
      )}

      {!error && !loading && devices.length > 0 && (
        <Card hover={false} style={{ padding: 0, overflow: 'hidden' }}>
          <div className="dv-table" role="table">
            <div className="dv-thead" role="row" aria-hidden="true">
              <span>Device</span>
              <span>Version</span>
              <span>Health</span>
              <span>Last seen</span>
              <span />
            </div>
            {devices.map((d) => (
              <div className="dv-trow" role="row" key={d.ulid} data-off={d.decommissioned ? '' : undefined}>
                <span className="dv-cell dv-name" role="cell">
                  <span className="dv-name-main">{d.name || 'Unnamed'}</span>
                  <span className="dv-name-ulid">{d.ulid}</span>
                </span>
                <span className="dv-cell dv-version" role="cell">
                  {d.version || '—'}{d.channel ? <span className="dv-channel"> · {d.channel}</span> : null}
                </span>
                <span className="dv-cell" role="cell">
                  <Pill color={deviceTone(d)} dot>{d.decommissioned ? 'decommissioned' : (d.status || d.health || 'unknown')}</Pill>
                </span>
                <span className="dv-cell dv-when" role="cell">{d.last_heartbeat ? relativeTime(d.last_heartbeat) : 'never'}</span>
                <span className="dv-cell dv-act" role="cell">
                  {!d.decommissioned && (
                    <Button size="sm" variant="ghost" onClick={() => decommission(d.ulid)} disabled={busyId === d.ulid}>
                      {busyId === d.ulid ? '…' : 'Decommission'}
                    </Button>
                  )}
                </span>
              </div>
            ))}
          </div>
        </Card>
      )}
    </Section>
  )
}

const STYLES = `
  .dv-header { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-2); flex-wrap: wrap; margin-bottom: var(--sp-3); }
  .dv-eyebrow { font-family: var(--font-mono); font-size: var(--text-xs); letter-spacing: 0.14em; text-transform: uppercase; color: var(--text-ghost); }
  .dv-title { font-family: var(--font-mono); font-size: clamp(1.125rem, 2.2vw, 1.375rem); font-weight: 700; letter-spacing: -0.025em; margin: var(--sp-1) 0 var(--sp-0-5); }
  .dv-sub { font-size: var(--text-sm); color: var(--text-faint); }
  .dv-retry-btn { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--accent); background: transparent; border: 1px solid var(--border-strong); border-radius: var(--radius-sm); padding: 8px 12px; min-height: 36px; cursor: pointer; flex-shrink: 0; }
  .dv-retry-btn:hover { border-color: var(--border-emphasis); }
  .dv-table { display: flex; flex-direction: column; }
  .dv-thead, .dv-trow { display: grid; grid-template-columns: minmax(0, 2fr) 1.2fr 1fr 1fr auto; gap: var(--sp-2); align-items: center; padding: var(--sp-2) var(--sp-3); }
  .dv-thead { font-family: var(--font-mono); font-size: var(--text-xs); letter-spacing: 0.08em; text-transform: uppercase; color: var(--text-ghost); border-bottom: 1px solid var(--border-strong); }
  .dv-trow { border-bottom: 1px solid var(--border-subtle); min-height: 56px; }
  .dv-trow:last-child { border-bottom: none; }
  .dv-trow[data-off] { opacity: 0.5; }
  .dv-cell { min-width: 0; font-size: var(--text-sm); }
  .dv-name { display: flex; flex-direction: column; gap: 1px; }
  .dv-name-main { font-family: var(--font-mono); font-weight: 600; color: var(--text-primary); }
  .dv-name-ulid { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-ghost); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dv-version { font-family: var(--font-mono); color: var(--text-secondary); font-variant-numeric: tabular-nums; }
  .dv-channel { color: var(--text-ghost); }
  .dv-when { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-faint); }
  .dv-act { text-align: right; }
  .dv-muted { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); padding: var(--sp-3) 0; }
  .dv-empty { text-align: center; padding: var(--sp-5) var(--sp-3); }
  .dv-empty-title { font-family: var(--font-mono); font-size: var(--text-base); font-weight: 700; color: var(--text-secondary); margin-bottom: var(--sp-1); }
  .dv-empty-sub { font-size: var(--text-sm); color: var(--text-faint); margin-bottom: var(--sp-2-5); }
  .dv-state-error { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--danger); padding: var(--sp-2) var(--sp-3); border: 1px solid color-mix(in srgb, var(--danger) 30%, var(--border-strong)); border-radius: var(--radius-lg); margin-bottom: var(--sp-3); }
  @media (max-width: 720px) {
    .dv-thead { display: none; }
    .dv-trow { grid-template-columns: 1fr auto; grid-auto-rows: min-content; row-gap: var(--sp-1); }
    .dv-version, .dv-when { grid-column: 1; }
    .dv-act { grid-column: 2; grid-row: 1 / span 2; align-self: center; }
  }
`
