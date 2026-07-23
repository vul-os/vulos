import { useState, useEffect, useCallback } from 'react'
import {
  startLocationReporting,
  stopLocationReporting,
  getLocationReportingStatus,
} from '../location/reporter.js'

// ---------------------------------------------------------------------------
// LocationPanel — opt-in device location sharing (LOCATION-01, client half).
//
// The box has no GPS. When you turn this on, this device's browser reports its
// position to YOUR box (POST /api/location), which caches it per-user so box
// apps (maps, weather) can use a location without each one prompting you again.
//
// Privacy model, stated plainly: OFF by default; the position is sent only to
// your own box and never leaves it; turning it off stops reporting immediately.
// This is the switch that arms src/core/location/reporter.js — the OS boot hook
// in App.jsx reads the same preference to resume reporting on next launch.
// ---------------------------------------------------------------------------

// Shared preference key — App.jsx's boot hook reads the same key.
export const LOCATION_SHARE_KEY = 'vulos.location.share'

function isEnabled() {
  try {
    return localStorage.getItem(LOCATION_SHARE_KEY) === 'on'
  } catch {
    return false
  }
}

export default function LocationPanel() {
  const [enabled, setEnabled] = useState(isEnabled)
  const [status, setStatus] = useState(() => getLocationReportingStatus())

  // Poll the reporter status while the panel is open so the user sees it go
  // active / surface a permission error.
  useEffect(() => {
    const id = setInterval(() => setStatus(getLocationReportingStatus()), 2000)
    return () => clearInterval(id)
  }, [])

  const toggle = useCallback(() => {
    setEnabled((on) => {
      const next = !on
      try {
        if (next) localStorage.setItem(LOCATION_SHARE_KEY, 'on')
        else localStorage.removeItem(LOCATION_SHARE_KEY)
      } catch {
        /* localStorage unavailable — the live start/stop below still applies */
      }
      if (next) startLocationReporting()
      else stopLocationReporting()
      setStatus(getLocationReportingStatus())
      return next
    })
  }, [])

  const errorLabel = {
    permission_denied: 'Location permission was denied in your browser.',
    position_unavailable: 'Your device could not determine a position.',
    timeout: 'Getting a position timed out — trying again.',
    unavailable: 'This device has no geolocation available.',
  }

  return (
    <div style={{ maxWidth: 560 }}>
      <h2 style={{ margin: 0, fontSize: 18, fontWeight: 650, color: 'var(--text-primary)' }}>Location</h2>
      <p style={{ marginTop: 8, fontSize: 14, lineHeight: 1.55, color: 'var(--text-tertiary)' }}>
        Share this device’s location with your box so apps like maps and weather can use it —
        without each app asking you separately. Your position is sent only to your own box and
        never leaves it.
      </p>

      <label
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 16,
          marginTop: 20,
          padding: '14px 16px',
          borderRadius: 'var(--radius-md, 10px)',
          background: 'var(--bg-elevated, rgba(255,255,255,0.04))',
          border: '1px solid var(--border-default, rgba(255,255,255,0.08))',
          cursor: 'pointer',
        }}
      >
        <span>
          <span style={{ display: 'block', fontWeight: 550, color: 'var(--text-primary)' }}>
            Share this device’s location
          </span>
          <span style={{ display: 'block', fontSize: 12.5, color: 'var(--text-muted)', marginTop: 2 }}>
            Off by default. Sends your position to your box while this device is signed in.
          </span>
        </span>
        <input
          type="checkbox"
          checked={enabled}
          onChange={toggle}
          style={{ width: 18, height: 18, accentColor: 'var(--accent, #5b6cff)', flexShrink: 0 }}
        />
      </label>

      {enabled && (
        <div style={{ marginTop: 14, fontSize: 13, color: 'var(--text-tertiary)' }}>
          {status.lastError ? (
            <span style={{ color: 'var(--status-warning, #f0b232)' }}>
              {errorLabel[status.lastError] || `Reporting issue: ${status.lastError}`}
            </span>
          ) : status.active ? (
            <span style={{ color: 'var(--status-success, #35c081)' }}>
              Reporting your location to your box{status.lastSentTs ? ' — last update just now.' : '…'}
            </span>
          ) : (
            <span>Starting…</span>
          )}
        </div>
      )}
    </div>
  )
}
