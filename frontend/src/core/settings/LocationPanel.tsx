import { useState, useEffect, useCallback } from 'react'
import {
  startLocationReporting,
  stopLocationReporting,
  getLocationReportingStatus,
} from '../location/reporter'
import { Section, Card, SettingRow, Toggle } from './ui'

interface LocationStatus {
  active: boolean
  lastError: string | null
  lastSentTs: number | null
}

type LocationErrorCode = 'permission_denied' | 'position_unavailable' | 'timeout' | 'unavailable'

function isLocationErrorCode(x: string): x is LocationErrorCode {
  return x === 'permission_denied' || x === 'position_unavailable' || x === 'timeout' || x === 'unavailable'
}

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

function isEnabled(): boolean {
  try {
    return localStorage.getItem(LOCATION_SHARE_KEY) === 'on'
  } catch {
    return false
  }
}

const ERROR_LABEL: Record<LocationErrorCode, string> = {
  permission_denied: 'Location permission was denied in your browser.',
  position_unavailable: 'Your device could not determine a position.',
  timeout: 'Getting a position timed out — trying again.',
  unavailable: 'This device has no geolocation available.',
}

export default function LocationPanel() {
  const [enabled, setEnabled] = useState(isEnabled)
  const [status, setStatus] = useState<LocationStatus>(() => getLocationReportingStatus())

  // Poll the reporter status while the panel is open so the user sees it go
  // active / surface a permission error.
  useEffect(() => {
    const id = setInterval(() => setStatus(getLocationReportingStatus()), 2000)
    return () => clearInterval(id)
  }, [])

  const toggle = useCallback((next: boolean) => {
    try {
      if (next) localStorage.setItem(LOCATION_SHARE_KEY, 'on')
      else localStorage.removeItem(LOCATION_SHARE_KEY)
    } catch {
      /* localStorage unavailable — the live start/stop below still applies */
    }
    if (next) startLocationReporting()
    else stopLocationReporting()
    setStatus(getLocationReportingStatus())
    setEnabled(next)
  }, [])

  return (
    <Section
      icon="location"
      title="Location"
      desc="Share this device's location with your box so apps like maps and weather can use it — without each app asking you separately. Your position is sent only to your own box and never leaves it."
    >
      <Card>
        <SettingRow
          label="Share this device's location"
          desc="Off by default. Sends your position to your box while this device is signed in."
          control={<Toggle checked={enabled} onChange={toggle} ariaLabel="Share this device's location" />}
        />

        {enabled && (
          <p className="mt-3 pt-3 border-t border-[var(--border-subtle)] text-xs leading-relaxed">
            {status.lastError ? (
              <span className="text-warning">
                {isLocationErrorCode(status.lastError) ? ERROR_LABEL[status.lastError] : `Reporting issue: ${status.lastError}`}
              </span>
            ) : status.active && status.lastSentTs ? (
              <span className="text-success">
                Reporting your location to your box — last update just now.
              </span>
            ) : status.active ? (
              // ARMED IS NOT REPORTING. startLocationReporting() sets active
              // BEFORE it calls watchPosition, so `active` becomes true while
              // the browser's permission prompt is still on screen — nothing
              // has been read and nothing has been sent. This branch used to
              // share the green "Reporting your location to your box…" with the
              // one above, distinguished only by a trailing ellipsis, so the
              // panel claimed in success green that the user's position was
              // going to their box while the user was still deciding whether to
              // allow it. If they said no, the claim stood until handleError
              // fired and the 2s poll caught up.
              //
              // It is not only a brief window. Indoors, or with no fix, a
              // watch can legitimately never call back — and then this was the
              // permanent state of the panel. lastSentTs is the only evidence a
              // report was actually delivered, and the line above was already
              // reading it, so the panel had what it needed to tell the truth.
              <span className="text-[var(--text-tertiary)]">
                Waiting for this device to report a position…
              </span>
            ) : (
              <span className="text-[var(--text-tertiary)]">Starting…</span>
            )}
          </p>
        )}
      </Card>
    </Section>
  )
}
