import { useState, useCallback, useRef } from 'react'

// ---------------------------------------------------------------------------
// DataExportPanel — "Export my data" (account portability).
//
// The user-facing half of the anti-lock-in guarantee: sovereignty means you can
// leave with ALL your data. Streams GET /api/export/data — a single .zip of the
// signed-in user's mail (.eml), Drive files (real bytes), calendar (.ics),
// contacts (.vcf) and OS preferences (settings.json, secrets scrubbed), in
// STANDARD portable formats that need nothing Vulos to read back.
//
// Honesty is a feature here: the panel states plainly what the archive covers
// AND what it does not (per-app data that lives in Talk/Meet/Ofisi docs, and
// content held only on a peer via an end-to-end share), rather than implying
// completeness. The MANIFEST.txt inside the zip says the same.
//
// The download is session-authed by the same cookie the shell already carries;
// there is no way to request another user's export (the server derives identity
// from the session, never from the request). Streaming via a blob keeps the
// affordance simple while the SERVER streams the zip without buffering it whole.
// ---------------------------------------------------------------------------

const COVERED = [
  { label: 'Mail', detail: 'Messages as .eml (opens in any mail client) plus a JSON index, per folder.' },
  { label: 'Files', detail: 'Your Drive tree — real file bytes with original names and paths.' },
  { label: 'Calendar', detail: 'A standard .ics file, where your mail service exposes events.' },
  { label: 'Contacts', detail: 'A standard .vcf (vCard) file, where your mail service exposes contacts.' },
  { label: 'Settings', detail: 'Your OS preferences as settings.json — API keys, PINs and passwords are never included.' },
]

const NOT_COVERED = [
  'Talk / Meet call history, and Ofisi documents (docs, sheets, slides, whiteboards) — export those from within each app.',
  'Anything held only on another instance via an end-to-end (content-blind) share — that instance holds the keys, so export it there. This is the privacy guarantee working, not a gap in your access.',
]

function Section({ title, children }) {
  return <div><h2 className="text-lg font-medium mb-4">{title}</h2>{children}</div>
}

export default function DataExportPanel() {
  // status: 'idle' | 'preparing' | 'done' | 'error'
  const [status, setStatus] = useState('idle')
  const [error, setError] = useState('')
  const busyRef = useRef(false)

  const download = useCallback(async () => {
    if (busyRef.current) return
    busyRef.current = true
    setStatus('preparing')
    setError('')
    try {
      // Same-origin, credentialed: the shell's session cookie authenticates the
      // request. The server derives the user from that session, so this can only
      // ever return the signed-in user's own data.
      const res = await fetch('/api/export/data', { credentials: 'same-origin' })
      if (res.status === 401) throw new Error('Your session has expired. Sign in again, then retry.')
      if (!res.ok) throw new Error(`Export failed (HTTP ${res.status}).`)

      const blob = await res.blob()

      // Prefer the server-provided filename; fall back to a timestamped one.
      let filename = `vulos-export-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '')}.zip`
      const cd = res.headers.get('Content-Disposition') || ''
      const m = /filename="?([^"]+)"?/.exec(cd)
      if (m && m[1]) filename = m[1]

      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
      setStatus('done')
    } catch (err) {
      setError(err?.message || 'Something went wrong preparing your export.')
      setStatus('error')
    } finally {
      busyRef.current = false
    }
  }, [])

  const preparing = status === 'preparing'

  return (
    <Section title="Export My Data">
      {/* Sovereignty framing */}
      <p className="text-sm text-neutral-400 leading-relaxed mb-2">
        Your data is yours. Download a complete archive of it in{' '}
        <span className="text-neutral-200">standard, portable formats</span> — nothing here needs
        Vulos to open it. This is the concrete proof of sovereignty: you can walk away with everything,
        any time, no lock-in.
      </p>
      <p className="text-xs text-neutral-600 leading-relaxed mb-6">
        The archive is a single <code className="text-neutral-400">.zip</code> and includes a{' '}
        <code className="text-neutral-400">MANIFEST.txt</code> describing exactly what it does and does not contain.
      </p>

      {/* What's included */}
      <h3 className="text-xs uppercase text-neutral-500 tracking-wider mb-2">Included</h3>
      <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50 mb-6">
        {COVERED.map(item => (
          <div key={item.label} className="px-4 py-3 bg-neutral-900/40">
            <div className="flex items-center gap-2 mb-0.5">
              <span aria-hidden="true" className="text-success text-xs">✓</span>
              <span className="text-sm font-medium text-neutral-200">{item.label}</span>
            </div>
            <p className="text-xs text-neutral-500 leading-relaxed">{item.detail}</p>
          </div>
        ))}
      </div>

      {/* Download affordance — primary CTA (this is the whole point of the panel).
          focus-primary + a pending cursor so keyboard + busy states read clearly. */}
      <button
        onClick={download}
        disabled={preparing}
        aria-busy={preparing}
        className="btn-primary focus-primary text-sm disabled:opacity-60 disabled:cursor-progress"
      >
        {preparing ? 'Preparing your archive…' : 'Download My Data'}
      </button>
      <p className="text-xs text-neutral-600 mt-2">
        A large mailbox or Drive may take a moment. Keep this window open until the download starts.
      </p>

      {/* States */}
      <div aria-live="polite" className="mt-3">
        {preparing && (
          <div className="flex items-center gap-2 text-xs text-neutral-400">
            <span className="inline-block w-3 h-3 spinner" aria-hidden="true" />
            Gathering your mail, files, calendar, contacts and settings…
          </div>
        )}
        {status === 'done' && (
          <div className="text-xs rounded px-3 py-2 bg-success-soft text-success">
            Your archive is downloading. Check the MANIFEST.txt inside for the full contents.
          </div>
        )}
        {status === 'error' && (
          <div className="text-xs rounded px-3 py-2 bg-danger-soft text-danger">
            {error} <button onClick={download} className="focus-primary rounded underline hover:no-underline ml-1">Retry</button>
          </div>
        )}
      </div>

      {/* Honest boundaries */}
      <h3 className="text-xs uppercase text-neutral-500 tracking-wider mt-8 mb-2">Not in this archive (yet)</h3>
      <p className="text-xs text-neutral-600 mb-3 leading-relaxed">
        We would rather be honest than imply completeness. These parts live elsewhere and need their own export:
      </p>
      <ul className="space-y-2 mb-2">
        {NOT_COVERED.map((line, i) => (
          <li key={i} className="flex items-start gap-2 text-xs text-neutral-500 leading-relaxed">
            <span aria-hidden="true" className="text-warning mt-px">•</span>
            <span>{line}</span>
          </li>
        ))}
      </ul>
    </Section>
  )
}
