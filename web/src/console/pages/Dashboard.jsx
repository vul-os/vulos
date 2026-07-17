/**
 * console/pages/Dashboard.jsx — / (console overview)
 *
 * The operational landing page: a greeting, at-a-glance stats over the account's
 * fleet and service health, quick actions, and a recent-events feed. Wired to
 * management's cproutes:
 *   GET /api/fleet/devices?account_id=<id>  — box/device counts (via useFleet)
 *   GET /api/account/status                  — overall health, relay, events
 * The commercial <UsageSlot/> is rendered for the metering widget — null in OSS.
 *
 * Fleshes out the phase-1 placeholder Dashboard.
 */

import { useState, useEffect, useCallback } from 'react'
import { Section, Card, Pill, Stat, Button } from '../../ui/index.jsx'
import { toFullPath } from '../../router.jsx'
import { useAuth } from '../../auth/AuthProvider.jsx'
import { UsageSlot } from '@vulos/commercial-seam'
import { useFleetDevices } from '../useFleet.js'
import { statusMeta, overallHeadline, relativeTime, fmtBytes } from '../status-format.js'

function useAccountStatus() {
  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(true)

  const load = useCallback(() => {
    setLoading(true)
    fetch('/api/account/status', { credentials: 'include', headers: { Accept: 'application/json' } })
      .then((res) => (res.ok ? res.json() : null))
      .then((json) => { setData(json); setLoading(false) })
      .catch(() => { setLoading(false) })
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { load() }, [load])
  return { data, loading }
}

function greeting() {
  const h = new Date().getHours()
  if (h < 5) return 'Good evening'
  if (h < 12) return 'Good morning'
  if (h < 18) return 'Good afternoon'
  return 'Good evening'
}

export default function Dashboard() {
  const { user } = useAuth()
  const { devices, loading: fleetLoading } = useFleetDevices()
  const { data: status, loading: statusLoading } = useAccountStatus()

  const activeBoxes = devices.filter((d) => !d.decommissioned)
  const overall = status?.overall
  const overallM = statusMeta(overall)
  const relay = status?.relay ?? {}
  const events = status?.events ?? []
  const name = (user?.email || user?.handle || '').split('@')[0]

  return (
    <Section slim>
      <style>{STYLES}</style>

      <div className="db-greeting">
        <div>
          <span className="db-greeting-text">{greeting()}{name ? `, ${name}` : ''}</span>
          <p className="db-greeting-sub">Here&apos;s the state of your Vulos control plane.</p>
        </div>
        {overall && (
          <div className="db-health" data-state={overall}>
            <span className="db-health-dot" style={{ background: overallM.tone }} aria-hidden="true" />
            <span className="db-health-headline">{overallHeadline(overall).replace('cloud services', 'services')}</span>
          </div>
        )}
      </div>

      {/* Stats */}
      <div className="db-stats">
        <Card className="db-stat-card">
          <Stat label="Boxes" value={fleetLoading ? '—' : String(activeBoxes.length)} />
        </Card>
        <Card className="db-stat-card">
          <Stat label="Devices" value={fleetLoading ? '—' : String(devices.length)} />
        </Card>
        <Card className="db-stat-card">
          <Stat label="Relayed this month" value={statusLoading ? '—' : fmtBytes(relay.bytes ?? 0)} />
        </Card>
        <Card className="db-stat-card">
          <Stat label="Relay sessions" value={statusLoading ? '—' : String(relay.sessions ?? 0)} />
        </Card>
      </div>

      {/* Commercial metering widget — null in OSS. */}
      <UsageSlot context={{ surface: 'dashboard' }} />

      <div className="db-cols">
        {/* Quick actions */}
        <Card hover={false} className="db-panel">
          <span className="db-section-head">Quick actions</span>
          <div className="db-actions">
            <Button size="sm" href={toFullPath('/enroll')} data-router>Enroll a box</Button>
            <Button size="sm" variant="ghost" href={toFullPath('/devices')} data-router>Manage devices</Button>
            <Button size="sm" variant="ghost" href={toFullPath('/developer')} data-router>Developer keys</Button>
            <Button size="sm" variant="ghost" href={toFullPath('/account-status')} data-router>Account status</Button>
          </div>
        </Card>

        {/* Recent events */}
        <Card hover={false} className="db-panel">
          <span className="db-section-head">Recent activity</span>
          {statusLoading ? (
            <div className="db-muted">Loading…</div>
          ) : events.length === 0 ? (
            <div className="db-muted">No recent events.</div>
          ) : (
            <ol className="db-events">
              {events.slice(0, 8).map((e, i) => (
                <li key={i}>
                  <span className="db-event-when">{relativeTime(e.at)}</span>
                  <span className="db-event-kind">{e.kind}</span>
                  <span className="db-event-detail">{e.text}</span>
                </li>
              ))}
            </ol>
          )}
        </Card>
      </div>

      {activeBoxes.length === 0 && !fleetLoading && (
        <Card hover={false} className="db-onboard">
          <div className="db-onboard-title">Get started</div>
          <div className="db-onboard-sub">You don&apos;t have any boxes yet. Boot the Vulos OS on a machine and enroll it to bring your control plane online.</div>
          <Button size="sm" href={toFullPath('/enroll')} data-router>Enroll your first box</Button>
        </Card>
      )}
    </Section>
  )
}

const STYLES = `
  .db-greeting { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-2); flex-wrap: wrap; margin-bottom: var(--sp-4); }
  .db-greeting-text { font-family: var(--font-sans); font-size: clamp(1.25rem, 2.6vw, 1.6rem); font-weight: 650; letter-spacing: -0.018em; color: var(--text-primary); }
  .db-greeting-sub { font-size: var(--text-sm); color: var(--text-faint); margin-top: 4px; }
  .db-health { display: flex; align-items: center; gap: var(--sp-1); padding: 8px 14px; border: 1px solid var(--border-strong); border-radius: 99px; font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-primary); }
  .db-health[data-state="operational"] { border-color: color-mix(in srgb, var(--good) 30%, var(--border-strong)); }
  .db-health[data-state="degraded"] { border-color: color-mix(in srgb, var(--warn) 35%, var(--border-strong)); }
  .db-health[data-state="down"] { border-color: color-mix(in srgb, var(--danger) 35%, var(--border-strong)); }
  .db-health-dot { width: 8px; height: 8px; border-radius: 50%; }
  .db-stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: var(--sp-2); margin-bottom: var(--sp-3); }
  .db-stat-card { padding: var(--sp-3); }
  .db-cols { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.4fr); gap: var(--sp-3); margin-top: var(--sp-3); align-items: start; }
  @media (max-width: 760px) { .db-cols { grid-template-columns: 1fr; } }
  .db-panel { padding: var(--sp-3); }
  .db-section-head { display: block; font-family: var(--font-mono); font-size: var(--text-xs); font-weight: 500; letter-spacing: 0.1em; text-transform: uppercase; color: var(--text-ghost); margin-bottom: var(--sp-2); }
  .db-actions { display: flex; flex-wrap: wrap; gap: var(--sp-1-5); }
  .db-muted { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); padding: var(--sp-1) 0; }
  .db-events { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: var(--sp-1); }
  .db-events li { display: grid; grid-template-columns: 88px 96px 1fr; gap: var(--sp-1-5); align-items: baseline; padding: var(--sp-1) 0; border-bottom: 1px solid var(--border-subtle); }
  .db-events li:last-child { border-bottom: none; }
  .db-event-when { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-faint); }
  .db-event-kind { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--accent); }
  .db-event-detail { font-size: var(--text-sm); color: var(--text-secondary); }
  .db-onboard { text-align: center; padding: var(--sp-5) var(--sp-3); margin-top: var(--sp-3); }
  .db-onboard-title { font-family: var(--font-mono); font-size: var(--text-base); font-weight: 700; color: var(--text-secondary); margin-bottom: var(--sp-1); }
  .db-onboard-sub { font-size: var(--text-sm); color: var(--text-faint); line-height: 1.6; max-width: 52ch; margin: 0 auto var(--sp-2-5); }
  @media (max-width: 480px) { .db-events li { grid-template-columns: 1fr; gap: 2px; } }
`
