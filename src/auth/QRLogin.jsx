// LOGINISO-02: QR / phone-approval login panel for kiosk / streamed clients.
//
// The kiosk shows a QR code encoding a short-lived challenge.
// An already-authenticated phone scans the QR and approves via
// POST /api/auth/qr/approve.  The kiosk polls GET /api/auth/qr/poll until
// the session token arrives, then calls onSuccess() so the app re-checks auth.
//
// Usage:
//   <QRLoginPanel onSuccess={checkAuth} onCancel={() => setShowQR(false)} />

import { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import QRCodeLib from 'qrcode'

const POLL_INTERVAL_MS = 2000 // 2s poll cadence

export default function QRLoginPanel({ onSuccess, onCancel }) {
  const [state, setState] = useState('loading') // 'loading' | 'waiting' | 'approved' | 'expired' | 'error'
  const [qrData, setQrData] = useState(null)
  const [expiresAt, setExpiresAt] = useState(null)
  const [secondsLeft, setSecondsLeft] = useState(null)
  const pollTimer = useRef(null)
  const countdownTimer = useRef(null)
  const challengeId = useRef(null)

  const stopTimers = useCallback(() => {
    if (pollTimer.current) clearTimeout(pollTimer.current)
    if (countdownTimer.current) clearInterval(countdownTimer.current)
  }, [])

  const startChallenge = useCallback(async () => {
    setState('loading')
    setQrData(null)
    setExpiresAt(null)
    setSecondsLeft(null)
    stopTimers()

    try {
      const res = await fetch('/api/auth/qr/begin', { method: 'POST' })
      if (!res.ok) throw new Error('Could not create QR challenge')
      const data = await res.json()

      challengeId.current = data.challenge_id
      const exp = new Date(data.expires_at)
      setQrData(data.qr_data)
      setExpiresAt(exp)
      setState('waiting')

      // Countdown ticker.
      countdownTimer.current = setInterval(() => {
        const secs = Math.max(0, Math.round((exp - Date.now()) / 1000))
        setSecondsLeft(secs)
      }, 500)

      // Poll loop.
      const poll = async () => {
        try {
          const r = await fetch(`/api/auth/qr/poll?id=${encodeURIComponent(data.challenge_id)}`)
          if (!r.ok) {
            setState('error')
            stopTimers()
            return
          }
          const result = await r.json()

          if (result.approved) {
            stopTimers()
            setState('approved')
            // Session cookie is already set by the backend.
            await onSuccess?.()
            return
          }
          if (result.expired) {
            stopTimers()
            setState('expired')
            return
          }
          // Still pending — poll again.
          pollTimer.current = setTimeout(poll, POLL_INTERVAL_MS)
        } catch {
          setState('error')
          stopTimers()
        }
      }
      pollTimer.current = setTimeout(poll, POLL_INTERVAL_MS)
    } catch (err) {
      setState('error')
    }
  }, [onSuccess, stopTimers])

  useEffect(() => {
    startChallenge()
    return stopTimers
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="space-y-5">
      <div className="text-center">
        <div className="w-12 h-12 rounded-2xl bg-violet-600/15 flex items-center justify-center mx-auto mb-3">
          <QRIcon />
        </div>
        <h2 className="text-lg text-neutral-200 font-medium">Scan to sign in</h2>
        <p className="text-sm text-neutral-500 mt-1">
          Open Vulos on your phone and scan this QR code
        </p>
      </div>

      {/* QR code display area */}
      <div className="flex items-center justify-center">
        {state === 'loading' && (
          <div className="w-44 h-44 rounded-2xl bg-neutral-900/60 border border-neutral-800/60 flex items-center justify-center">
            <span className="w-6 h-6 border-2 border-neutral-700 border-t-neutral-300 rounded-full animate-spin" />
          </div>
        )}

        {state === 'waiting' && qrData && (
          <div className="relative">
            <QRCodeDisplay value={qrData} />
            {secondsLeft !== null && secondsLeft <= 30 && (
              <div className="absolute -bottom-6 left-0 right-0 text-center">
                <span className={`text-xs ${secondsLeft <= 10 ? 'text-red-400' : 'text-neutral-500'}`}>
                  Expires in {secondsLeft}s
                </span>
              </div>
            )}
          </div>
        )}

        {state === 'approved' && (
          <div className="w-44 h-44 rounded-2xl bg-green-900/20 border border-green-700/30 flex flex-col items-center justify-center gap-2">
            <svg viewBox="0 0 24 24" className="w-10 h-10 text-green-400" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <circle cx="12" cy="12" r="9" />
              <path d="M8 12l3 3 5-5" />
            </svg>
            <span className="text-sm text-green-400">Approved</span>
          </div>
        )}

        {(state === 'expired' || state === 'error') && (
          <div className="w-44 h-44 rounded-2xl bg-neutral-900/60 border border-neutral-800/60 flex flex-col items-center justify-center gap-3">
            <span className="text-neutral-500 text-sm">
              {state === 'expired' ? 'QR code expired' : 'Something went wrong'}
            </span>
            <button
              onClick={startChallenge}
              className="px-3 py-1.5 rounded-lg bg-neutral-800 text-xs text-neutral-300 hover:bg-neutral-700 transition-colors"
            >
              Refresh
            </button>
          </div>
        )}
      </div>

      <button
        type="button"
        onClick={onCancel}
        className="w-full text-sm text-neutral-600 hover:text-neutral-400 transition-colors text-center pt-2"
      >
        ← Use password instead
      </button>
    </div>
  )
}

// ─── QRCodeDisplay ─────────────────────────────────────────────────────────────
// Renders qr_data as a real, scannable SVG QR code using the `qrcode` package.
// The QR image is phone-scannable; the raw URL is NOT exposed in any attribute
// (no title/data-* leaking the challenge URL to the accessibility tree or DOM).
function QRCodeDisplay({ value }) {
  // Build the QR bit matrix synchronously using the qrcode library's core API.
  // QRCodeLib.create() returns { modules: BitMatrix, version, ... }.
  // modules.get(row, col) returns a truthy value for dark cells.
  const { cells, size } = useMemo(() => {
    try {
      const qr = QRCodeLib.create(value, { errorCorrectionLevel: 'M' })
      const sz = qr.modules.size
      const grid = []
      for (let r = 0; r < sz; r++) {
        const row = []
        for (let c = 0; c < sz; c++) {
          row.push(!!qr.modules.get(r, c))
        }
        grid.push(row)
      }
      return { cells: grid, size: sz }
    } catch {
      return { cells: [], size: 0 }
    }
  }, [value])

  if (!size) return null

  return (
    <div className="w-44 h-44 rounded-2xl bg-white p-3 shadow-lg">
      <svg
        viewBox={`0 0 ${size} ${size}`}
        className="w-full h-full"
        aria-label="QR code for phone sign-in"
        role="img"
      >
        {cells.flatMap((row, y) =>
          row.map((dark, x) =>
            dark ? (
              <rect
                key={`${x}-${y}`}
                x={x}
                y={y}
                width={1}
                height={1}
                fill="black"
              />
            ) : null
          )
        )}
      </svg>
    </div>
  )
}

// ─── Icons ─────────────────────────────────────────────────────────────────────

function QRIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6"
      strokeLinecap="round" strokeLinejoin="round" className="w-6 h-6 text-violet-400">
      <rect x="3" y="3" width="7" height="7" rx="1" />
      <rect x="14" y="3" width="7" height="7" rx="1" />
      <rect x="3" y="14" width="7" height="7" rx="1" />
      <path d="M14 14h3v3h-3zM17 17h3v3h-3zM14 20h3" />
    </svg>
  )
}
