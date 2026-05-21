/**
 * LiveBanner.jsx — NETB-04
 *
 * Persistent, non-dismissable banner shown while the OS runs in live-RAM mode
 * (booted from the --live squashfs+overlay, BMINIT-14).  Reminds the user
 * that the session is temporary and surfaces an "Install Vulos" action.
 *
 * Props:
 *   onInstall  () => void   — invoked when the user clicks "Install Vulos"
 *   onDismiss  () => void   — optional; if provided, renders a close button
 *                             (use only when the call-site re-checks live mode)
 *
 * The banner self-hides when /api/installer/live-session reports is_live:false
 * so it disappears automatically after a successful install + reboot-in-place
 * scenario (unlikely but safe).
 */

import { useState, useEffect } from 'react'

export default function LiveBanner({ onInstall, onDismiss }) {
  const [isLive, setIsLive] = useState(true)  // optimistic: assume live until confirmed
  const [checked, setChecked] = useState(false)

  useEffect(() => {
    fetch('/api/installer/live-session')
      .then(r => r.ok ? r.json() : null)
      .then(d => {
        if (d != null) setIsLive(d.is_live)
        setChecked(true)
      })
      .catch(() => setChecked(true))
  }, [])

  // Do not render until we have confirmed the boot mode, or if installed.
  if (!checked || !isLive) return null

  return (
    <div
      role="status"
      aria-live="polite"
      className="
        w-full flex items-center gap-3 px-4 py-2.5
        bg-amber-950/60 border-b border-amber-800/40
        text-amber-300 select-none
      "
    >
      {/* Live indicator dot */}
      <span className="shrink-0 flex items-center gap-1.5 text-xs font-medium">
        <span className="w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse shrink-0" />
        Live session
      </span>

      {/* Separator */}
      <span className="shrink-0 text-amber-800/60 text-xs" aria-hidden="true">·</span>

      {/* Description */}
      <span className="flex-1 text-xs text-amber-400/80 truncate">
        Changes are not saved to disk. Install Vulos to make them permanent.
      </span>

      {/* Install CTA */}
      <button
        onClick={onInstall}
        className="
          shrink-0 px-3 py-1 text-xs font-semibold rounded-md
          bg-amber-600/80 hover:bg-amber-500/90 text-amber-50
          transition-colors focus:outline-none focus:ring-2 focus:ring-amber-400 focus:ring-offset-1
          focus:ring-offset-amber-950
        "
      >
        Install Vulos
      </button>

      {/* Optional dismiss button (e.g. for re-checkable call-sites) */}
      {onDismiss && (
        <button
          onClick={onDismiss}
          aria-label="Dismiss live banner"
          className="
            shrink-0 w-5 h-5 flex items-center justify-center rounded
            text-amber-700 hover:text-amber-400 transition-colors
            focus:outline-none focus:ring-1 focus:ring-amber-500
          "
        >
          <svg width="8" height="8" viewBox="0 0 8 8" fill="none" aria-hidden="true">
            <path d="M1 1 L7 7 M7 1 L1 7" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
          </svg>
        </button>
      )}
    </div>
  )
}
