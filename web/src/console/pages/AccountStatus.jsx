/**
 * console/pages/AccountStatus.jsx — /account-status
 *
 * AUTHENTICATED per-account status: the health of the things THIS account owns —
 * its box(es) reachability, its relay usage/health, its provisioned services, and
 * recent events. Ported from vulos-cloud onto management's cproutes.
 *
 * API: GET /api/account/status (session cookie; backend derives the account id
 * from the session — no identifier is ever sent, so there is no IDOR). The
 * endpoint is fail-safe: an unavailable source returns an empty section, so this
 * page degrades to "nothing provisioned yet" rather than breaking.
 */

import { useState, useEffect, useCallback } from 'react'
import { Section, Pill } from '../../ui/index.jsx'
import { toFullPath } from '../../router.jsx'
import {
  statusMeta,
  overallHeadline,
  relativeTime,
  fmtBytes,
  relayHealthStatus,
} from '../status-format.js'

function useAccountStatus(refreshToken) {
  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)

  const load = useCallback(() => {
    setLoading(true)
    setError(null)
    fetch('/api/account/status', {
      credentials: 'include',
      headers: { Accept: 'application/json' },
    })
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json()
      })
      .then((json) => { setData(json); setLoading(false) })
      .catch((err) => { setError(err.message); setLoading(false) })
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { load() }, [load, refreshToken])
  return { data, loading, error, reload: load }
}

function OverallBanner({ overall, loading }) {
  if (loading) {
    return (
      <div className="acs-banner" aria-live="polite">
        <span className="acs-banner-dot" style={{ background: 'var(--text-ghost)' }} aria-hidden="true" />
        <span>Checking your services…</span>
      </div>
    )
  }
  const m = statusMeta(overall)
  return (
    <div className="acs-banner" data-state={overall} role="status">
      <span className="acs-banner-dot" style={{ background: m.tone }} aria-hidden="true" />
      <span>{overallHeadline(overall).replace('cloud services', 'services')}</span>
    </div>
  )
}

function BoxRow({ box }) {
  const m = statusMeta(box.health)
  return (
    <div className="acs-row">
      <div className="acs-row-main">
        <span className="acs-dot" style={{ background: box.reachable ? 'var(--good)' : 'var(--warn)' }} aria-hidden="true" />
        <div className="acs-row-text">
          <span className="acs-row-name">{box.name || box.ulid || 'Box'}</span>
          {box.ulid && <span className="acs-row-sub">{box.ulid}</span>}
        </div>
      </div>
      <div className="acs-row-right">
        <span className="acs-row-when">{box.last_seen ? `seen ${relativeTime(box.last_seen)}` : 'never seen'}</span>
        <Pill color={m.pill} dot>{box.reachable ? 'reachable' : 'unreachable'}</Pill>
      </div>
    </div>
  )
}

export default function AccountStatus() {
  const [refreshToken, setRefreshToken] = useState(0)
  const { data, loading, error, reload } = useAccountStatus(refreshToken)

  function handleRefresh() {
    setRefreshToken((t) => t + 1)
    reload()
  }

  const boxes = data?.boxes ?? []
  const services = data?.services ?? []
  const events = data?.events ?? []
  const relay = data?.relay ?? {}
  const relayStatus = relayHealthStatus(relay.health)

  return (
    <Section slim>
      <style>{STYLES}</style>

      <div className="acs-head">
        <div>
          <h1 className="acs-title">Account status</h1>
          <p className="acs-sub">The health of the boxes and services on your account.</p>
        </div>
        <button className="acs-reload" onClick={handleRefresh} disabled={loading} aria-busy={loading}>
          ↺ {loading ? 'Refreshing…' : 'Refresh'}
        </button>
      </div>

      {error && (
        <div className="acs-error" role="alert">Could not load account status: {error}</div>
      )}

      {!error && <OverallBanner overall={data?.overall} loading={loading} />}

      {!error && !loading && (
        <>
          <h2 className="acs-h2">Your boxes</h2>
          {boxes.length === 0 ? (
            <div className="acs-empty">
              No boxes on this account yet.{' '}
              <a href={toFullPath('/enroll')} data-router>Enroll a box →</a>
            </div>
          ) : (
            <div className="acs-list" role="list">
              {boxes.map((b) => (
                <div role="listitem" key={b.ulid || b.name}>
                  <BoxRow box={b} />
                </div>
              ))}
            </div>
          )}

          <h2 className="acs-h2">Relay</h2>
          <div className="acs-relay">
            <div className="acs-relay-cell">
              <span className="acs-cell-label">Fabric health</span>
              <span className="acs-cell-value">
                <Pill color={statusMeta(relayStatus).pill} dot>
                  {relay.health || 'unavailable'}
                </Pill>
              </span>
            </div>
            <div className="acs-relay-cell">
              <span className="acs-cell-label">Relayed this month</span>
              <span className="acs-cell-value acs-num">{fmtBytes(relay.bytes ?? 0)}</span>
            </div>
            <div className="acs-relay-cell">
              <span className="acs-cell-label">Sessions</span>
              <span className="acs-cell-value acs-num">{relay.sessions ?? 0}</span>
            </div>
            <div className="acs-relay-cell">
              <span className="acs-cell-label">Period</span>
              <span className="acs-cell-value acs-num">{relay.period || '—'}</span>
            </div>
          </div>

          <h2 className="acs-h2">Provisioned services</h2>
          {services.length === 0 ? (
            <div className="acs-empty">No provisioned services.</div>
          ) : (
            <div className="acs-list" role="list">
              {services.map((s, i) => {
                const m = statusMeta(s.status)
                return (
                  <div className="acs-row" role="listitem" key={`${s.name}-${i}`}>
                    <div className="acs-row-main">
                      <span className="acs-dot" style={{ background: m.tone }} aria-hidden="true" />
                      <span className="acs-row-name">{s.name}</span>
                    </div>
                    <Pill color={m.pill} dot>{m.label}</Pill>
                  </div>
                )
              })}
            </div>
          )}

          <h2 className="acs-h2">Recent events</h2>
          {events.length === 0 ? (
            <div className="acs-empty">No recent events.</div>
          ) : (
            <ol className="acs-events" role="list">
              {events.map((e, i) => (
                <li key={i}>
                  <time dateTime={e.at}>{relativeTime(e.at)}</time>
                  <span className="acs-event-kind">{e.kind}</span>
                  <span>{e.text}</span>
                </li>
              ))}
            </ol>
          )}
        </>
      )}
    </Section>
  )
}

const STYLES = `
  .acs-head { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-2); flex-wrap: wrap; margin-bottom: var(--sp-4); }
  .acs-title { font-family: var(--font-mono); font-size: clamp(1rem, 2vw, 1.25rem); font-weight: 700; letter-spacing: -0.02em; margin: 0; }
  .acs-sub { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); margin-top: var(--sp-0-5); }
  .acs-reload {
    font-family: var(--font-mono); font-size: var(--text-xs); color: var(--accent);
    background: transparent; border: 1px solid var(--border-strong); border-radius: var(--radius-sm);
    padding: 8px 12px; min-height: 36px; cursor: pointer; flex-shrink: 0;
    transition: border-color 120ms var(--ease);
  }
  .acs-reload:hover { border-color: var(--border-emphasis); }
  .acs-reload:focus-visible { outline: none; box-shadow: var(--focus-ring); }

  .acs-banner {
    display: flex; align-items: center; gap: var(--sp-1-5);
    padding: var(--sp-2) var(--sp-3); border: 1px solid var(--border-strong);
    border-radius: var(--radius-lg); margin-bottom: var(--sp-4);
    font-family: var(--font-mono); font-size: var(--text-sm); font-weight: 500; color: var(--text-primary);
  }
  .acs-banner-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
  .acs-banner[data-state="operational"] { border-color: color-mix(in srgb, var(--good) 30%, var(--border-strong)); }
  .acs-banner[data-state="degraded"]    { border-color: color-mix(in srgb, var(--warn) 35%, var(--border-strong)); }
  .acs-banner[data-state="down"]        { border-color: color-mix(in srgb, var(--danger) 35%, var(--border-strong)); }

  .acs-h2 { font-family: var(--font-mono); font-size: var(--text-xs); font-weight: 500; letter-spacing: 0.09em; text-transform: uppercase; color: var(--text-ghost); margin: var(--sp-5) 0 var(--sp-1-5); }

  .acs-list { border: 1px solid var(--border-strong); border-radius: var(--radius-lg); overflow: hidden; background: var(--bg-surface); }
  .acs-row {
    display: flex; align-items: center; justify-content: space-between; gap: var(--sp-2);
    padding: var(--sp-2) var(--sp-2-5); border-bottom: 1px solid var(--border-strong); min-height: 52px;
  }
  .acs-list > *:last-child .acs-row, .acs-row:last-child { border-bottom: none; }
  .acs-row-main { display: flex; align-items: center; gap: var(--sp-1-5); min-width: 0; flex: 1; }
  .acs-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
  .acs-row-text { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
  .acs-row-name { font-family: var(--font-mono); font-size: var(--text-sm); font-weight: 600; color: var(--text-primary); }
  .acs-row-sub { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-ghost); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .acs-row-right { display: flex; align-items: center; gap: var(--sp-1-5); flex-shrink: 0; }
  .acs-row-when { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-faint); white-space: nowrap; }

  .acs-relay { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: var(--sp-2); }
  .acs-relay-cell {
    display: flex; flex-direction: column; gap: 4px;
    padding: var(--sp-2) var(--sp-2-5); border: 1px solid var(--border-strong);
    border-radius: var(--radius-lg); background: var(--bg-surface);
  }
  .acs-cell-label { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-ghost); text-transform: uppercase; letter-spacing: 0.06em; }
  .acs-cell-value { font-size: var(--text-sm); color: var(--text-primary); }
  .acs-num { font-family: var(--font-mono); font-weight: 600; font-variant-numeric: tabular-nums; }

  .acs-events { list-style: none; margin: 0; padding: 0; display: grid; gap: var(--sp-1); }
  .acs-events li {
    display: grid; grid-template-columns: 90px 90px 1fr; gap: var(--sp-1-5); align-items: baseline;
    padding: var(--sp-1) var(--sp-2); border: 1px solid var(--border-strong); border-radius: var(--radius);
    background: var(--bg-surface); font-size: var(--text-sm); color: var(--text-secondary);
  }
  .acs-events time { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-faint); }
  .acs-event-kind { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--accent); }

  .acs-empty { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); padding: var(--sp-3); border: 1px dashed var(--border-strong); border-radius: var(--radius-lg); }
  .acs-empty a { color: var(--accent); text-decoration: none; }
  .acs-error { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--danger); padding: var(--sp-2) var(--sp-3); border: 1px solid color-mix(in srgb, var(--danger) 30%, var(--border-strong)); border-radius: var(--radius-lg); margin-bottom: var(--sp-3); }

  @media (max-width: 480px) {
    .acs-events li { grid-template-columns: 1fr; gap: 2px; }
    .acs-row-when { display: none; }
  }
`
