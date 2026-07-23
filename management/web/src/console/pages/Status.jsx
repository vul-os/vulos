/**
 * console/pages/Status.jsx — /status (device telemetry)
 *
 * Per-device support telemetry: pick one of your boxes and read its latest health
 * signals and any open alerts. Wired to management's cproutes:
 *   GET /api/fleet/devices?account_id=<id>   — the device picker
 *   GET /api/support/status?ulid=<ulid>       — { ulid, sampled_at, signals[], alerts[], has_data }
 *
 * Adapted from vulos-cloud's Status.jsx (the /console/status support-telemetry
 * view, distinct from the per-account /account-status roll-up).
 */

import { useState, useEffect, useCallback } from 'react'
import { Section, Card, Pill } from '../../ui/index.jsx'
import { useFleetDevices } from '../useFleet.js'
import { relativeTime } from '../status-format.js'

function severityTone(sev) {
  const v = (sev || '').toLowerCase()
  if (v === 'critical') return 'danger'
  if (v === 'warn' || v === 'warning') return 'warn'
  if (v === 'ok' || v === 'healthy') return 'good'
  return 'faint'
}

function fmtSignalValue(sig) {
  if (sig.value == null) return '—'
  const v = typeof sig.value === 'number' ? sig.value.toLocaleString(undefined, { maximumFractionDigits: 2 }) : String(sig.value)
  return sig.unit ? `${v} ${sig.unit}` : v
}

function useSupportStatus(ulid) {
  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const load = useCallback(() => {
    if (!ulid) { setData(null); return }
    setLoading(true)
    setError(null)
    fetch(`/api/support/status?ulid=${encodeURIComponent(ulid)}`, { credentials: 'include', headers: { Accept: 'application/json' } })
      .then((res) => { if (!res.ok) throw new Error(`HTTP ${res.status}`); return res.json() })
      .then((json) => { setData(json); setLoading(false) })
      .catch((err) => { setError(err.message); setLoading(false) })
  }, [ulid])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { load() }, [load])
  return { data, loading, error, reload: load }
}

export default function Status() {
  const { devices, loading: devLoading } = useFleetDevices()
  const [selected, setSelected] = useState('')

  // Default to the first device once loaded.
  useEffect(() => {
    if (!selected && devices.length > 0) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setSelected(devices[0].ulid)
    }
  }, [devices, selected])

  const { data, loading, error, reload } = useSupportStatus(selected)
  const signals = data?.signals ?? []
  const alerts = data?.alerts ?? []

  return (
    <Section slim>
      <style>{STYLES}</style>
      <div className="sp-header">
        <div>
          <h1 className="sp-page-title">Telemetry</h1>
          <p className="sp-page-sub">Support diagnostics for a device on your account.</p>
        </div>
        {selected && (
          <button className="sp-reload-btn" onClick={reload} disabled={loading} aria-busy={loading}>
            ↺ {loading ? 'Refreshing…' : 'Refresh'}
          </button>
        )}
      </div>

      {devLoading ? (
        <div className="sp-muted">Loading devices…</div>
      ) : devices.length === 0 ? (
        <div className="sp-muted">No devices on your account yet.</div>
      ) : (
        <div className="sp-picker">
          <label className="sp-picker-label" htmlFor="sp-device">Device</label>
          <select id="sp-device" className="sp-select" value={selected} onChange={(e) => setSelected(e.target.value)}>
            {devices.map((d) => (
              <option key={d.ulid} value={d.ulid}>{d.name || d.ulid}</option>
            ))}
          </select>
        </div>
      )}

      {selected && (
        <>
          {error && <div className="sp-error-box" role="alert">Could not load telemetry: {error}</div>}

          {!error && data && (
            <>
              <div className="sp-banner" data-has={data.has_data ? 'yes' : 'no'}>
                <span className="sp-banner-dot" aria-hidden="true" />
                {data.has_data
                  ? `Last sample ${data.sampled_at ? relativeTime(data.sampled_at) : 'recently'}`
                  : 'No telemetry sampled for this device yet.'}
              </div>

              {data.has_data && (
                <>
                  <h2 className="sp-group-label">Signals</h2>
                  {signals.length === 0 ? (
                    <div className="sp-muted">No signals reported.</div>
                  ) : (
                    <div className="sp-signals">
                      {signals.map((s, i) => (
                        <div className="sp-signal" key={`${s.signal}-${i}`}>
                          <div className="sp-signal-top">
                            <span className="sp-signal-name">{s.signal}</span>
                            <Pill color={severityTone(s.severity)} dot>{s.severity || 'ok'}</Pill>
                          </div>
                          <span className="sp-signal-value">{fmtSignalValue(s)}</span>
                          {s.detail && <span className="sp-signal-detail">{s.detail}</span>}
                        </div>
                      ))}
                    </div>
                  )}

                  <h2 className="sp-group-label sp-alerts-head">Open alerts</h2>
                  {alerts.length === 0 ? (
                    <div className="sp-no-alerts">No open alerts. This device looks healthy.</div>
                  ) : (
                    <div className="sp-alerts">
                      {alerts.map((a) => (
                        <div className="sp-alert" key={a.id}>
                          <Pill color={severityTone(a.severity)} dot>{a.severity}</Pill>
                          <div className="sp-alert-body">
                            <span className="sp-alert-signal">{a.signal}</span>
                            <span className="sp-alert-detail">{a.detail}</span>
                          </div>
                          <span className="sp-alert-when">{a.fired_at ? relativeTime(a.fired_at) : ''}</span>
                        </div>
                      ))}
                    </div>
                  )}
                </>
              )}
            </>
          )}
        </>
      )}
    </Section>
  )
}

const STYLES = `
  .sp-header { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-2); flex-wrap: wrap; margin-bottom: var(--sp-3); }
  .sp-page-title { font-family: var(--font-mono); font-size: clamp(1.125rem, 2.2vw, 1.375rem); font-weight: 700; letter-spacing: -0.025em; margin: 0; }
  .sp-page-sub { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); margin-top: var(--sp-0-5); }
  .sp-reload-btn { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--accent); background: transparent; border: 1px solid var(--border-strong); border-radius: var(--radius-sm); padding: 8px 12px; min-height: 36px; cursor: pointer; flex-shrink: 0; }
  .sp-reload-btn:hover { border-color: var(--border-emphasis); }
  .sp-picker { display: flex; align-items: center; gap: var(--sp-1-5); margin-bottom: var(--sp-3); }
  .sp-picker-label { font-family: var(--font-mono); font-size: var(--text-xs); letter-spacing: 0.06em; text-transform: uppercase; color: var(--text-ghost); }
  .sp-select { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-primary); background: var(--bg-elevated); border: 1px solid var(--border-strong); border-radius: var(--radius-sm); padding: 8px 12px; min-height: 40px; min-width: 220px; }
  .sp-select:focus-visible { outline: none; box-shadow: var(--focus-ring); border-color: var(--accent); }
  .sp-banner { display: flex; align-items: center; gap: var(--sp-1-5); padding: var(--sp-2) var(--sp-2-5); border: 1px solid var(--border-strong); border-radius: var(--radius-lg); font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-secondary); margin-bottom: var(--sp-3); }
  .sp-banner-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--text-ghost); flex-shrink: 0; }
  .sp-banner[data-has="yes"] .sp-banner-dot { background: var(--good); }
  .sp-group-label { font-family: var(--font-mono); font-size: var(--text-xs); font-weight: 500; letter-spacing: 0.09em; text-transform: uppercase; color: var(--text-ghost); margin: var(--sp-4) 0 var(--sp-1-5); }
  .sp-signals { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: var(--sp-2); }
  .sp-signal { display: flex; flex-direction: column; gap: 4px; padding: var(--sp-2) var(--sp-2-5); border: 1px solid var(--border-strong); border-radius: var(--radius-lg); background: var(--bg-surface); }
  .sp-signal-top { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-1); }
  .sp-signal-name { font-family: var(--font-mono); font-size: var(--text-xs); letter-spacing: 0.04em; text-transform: uppercase; color: var(--text-ghost); }
  .sp-signal-value { font-family: var(--font-mono); font-size: 1.125rem; font-weight: 700; color: var(--text-primary); font-variant-numeric: tabular-nums; }
  .sp-signal-detail { font-size: var(--text-xs); color: var(--text-faint); }
  .sp-no-alerts { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); padding: var(--sp-2) var(--sp-3); border: 1px dashed var(--border-strong); border-radius: var(--radius-lg); }
  .sp-alerts { display: flex; flex-direction: column; gap: var(--sp-1-5); }
  .sp-alert { display: flex; align-items: center; gap: var(--sp-2); padding: var(--sp-2) var(--sp-2-5); border: 1px solid var(--border-strong); border-radius: var(--radius-lg); background: var(--bg-surface); }
  .sp-alert-body { display: flex; flex-direction: column; gap: 1px; min-width: 0; flex: 1; }
  .sp-alert-signal { font-family: var(--font-mono); font-size: var(--text-sm); font-weight: 600; color: var(--text-primary); }
  .sp-alert-detail { font-size: var(--text-sm); color: var(--text-faint); }
  .sp-alert-when { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-ghost); flex-shrink: 0; }
  .sp-error-box { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--danger); padding: var(--sp-2) var(--sp-3); border: 1px solid color-mix(in srgb, var(--danger) 30%, var(--border-strong)); border-radius: var(--radius-lg); margin-bottom: var(--sp-3); }
  .sp-muted { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); padding: var(--sp-2) 0; }
`
