import { useState, useEffect, useRef } from 'react'
import QRCode from 'qrcode'
import { Section, Card, InfoList, InfoRow, Banner } from './ui'

// ---------------------------------------------------------------------------
// LANPairingPanel — Settings -> Network -> Native Pairing (PAIR-01).
//
// Surfaces GET /api/lan/pairing (backend/cmd/server/lan_pairing.go): this
// box's LAN TLS certificate fingerprint, and the vulos://pair?... payload a
// native client is meant to scan to pin it on first connection.
//
// Honesty note (clients/README.md's trust-model section): clients/core/
// implements the pin/verify/store logic, but no shell (desktop/Android/iOS)
// calls it yet and there is no camera-scanning UI anywhere. This panel is
// only the box-side half — it lets a user SEE the code, but nothing today
// can scan it. The copy below says so rather than implying a working
// end-to-end handshake.
// ---------------------------------------------------------------------------

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}
function errorMessage(err: unknown, fallback: string): string {
  return (isRecord(err) && typeof err.message === 'string' ? err.message : '') || fallback
}

// PairingInfo — the fields this panel reads off GET /api/lan/pairing's wire
// shape (pairingInfo in lan_pairing.go). `spki` (the raw base64 pin) is
// intentionally not surfaced here — spki_hex is the human-comparable form.
interface PairingInfo {
  name?: string
  addr?: string
  spkiHex?: string
  payload?: string
}

function toPairingInfo(x: unknown): PairingInfo | null {
  if (!isRecord(x)) return null
  return {
    name: typeof x.name === 'string' ? x.name : undefined,
    addr: typeof x.addr === 'string' ? x.addr : undefined,
    spkiHex: typeof x.spki_hex === 'string' ? x.spki_hex : undefined,
    payload: typeof x.payload === 'string' ? x.payload : undefined,
  }
}

// PairingQR — renders `content` (the vulos://pair?... payload) as a QR code
// on a fixed white card so it stays scannable regardless of the OS theme —
// QR contrast needs to be theme-independent, not just legible. Calls the
// same narrow ambient 'qrcode' declaration (qrcode.d.ts) that
// auth/Setup.tsx's recovery-kit QR canvas uses.
//
// Exported for its own test: this panel fetches once, has no retry control and
// never changes the payload it passes down, so the failure-then-recovery path
// below cannot be driven through the panel at all.
export function PairingQR({ content }: { content: string }) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [renderError, setRenderError] = useState('')

  // The canvas stays MOUNTED while the error text is showing. It used to be
  // swapped out for the error, which left canvasRef.current null, so the guard
  // below early-returned for every subsequent `content` and the first failure
  // became permanent — and the optimistic clear that used to sit here ran
  // AFTER that guard, so it could never undo it. (Same fault, same shape, as
  // LANRootCertPanel's DownloadQR, which this component was written to match.)
  useEffect(() => {
    const canvas = canvasRef.current
    if (!content || !canvas) return
    let cancelled = false
    QRCode.toCanvas(canvas, content, {
      width: 200,
      margin: 2,
      color: { dark: '#0a0a0a', light: '#ffffff' },
      errorCorrectionLevel: 'M',
    })
      // Cleared by a draw that SUCCEEDED, not by one that was merely started.
      // While a retry is in flight the previous failure is still the last thing
      // known to be true, so the pointer at the printed fingerprint stays on
      // screen until there is actually a QR code to replace it with.
      .then(() => { if (!cancelled) setRenderError('') })
      .catch((err: unknown) => { if (!cancelled) setRenderError(errorMessage(err, 'QR render failed')) })
    return () => { cancelled = true }
  }, [content])

  return (
    <>
      {renderError && (
        <div className="text-xs text-[var(--text-muted)] text-center p-4 rounded-xl border border-[var(--border-default)]">
          Could not render the QR code ({renderError}). Use the fingerprint below instead.
        </div>
      )}
      <canvas
        ref={canvasRef}
        role="img"
        aria-label="Pairing QR code"
        className="rounded-xl mx-auto block"
        // Hidden via `display`, NOT the `hidden` attribute: `block` in the
        // className above out-specifies the UA's [hidden] rule (both are one
        // selector, author beats UA) and the canvas would stay visible.
        style={{ imageRendering: 'pixelated', background: '#ffffff', padding: 12, display: renderError ? 'none' : 'block' }}
        aria-hidden={renderError ? true : undefined}
      />
    </>
  )
}

export default function LANPairingPanel() {
  const [info, setInfo] = useState<PairingInfo | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  // One fetch, on mount. There is no retry control on this panel, so the
  // `setLoading(true)` / `setError(null)` that used to open this — and the
  // suppression that covered them — were writing values the initial state
  // already held, on the only pass that could ever run them. Nothing is set
  // synchronously now; `loading` starts true and only the response moves it.
  useEffect(() => {
    let cancelled = false
    fetch('/api/lan/pairing', { credentials: 'include' })
      .then(async r => {
        // Untrusted network JSON — narrowed via toPairingInfo, never cast.
        const raw: unknown = await r.json().catch(() => null)
        if (!r.ok) {
          throw new Error((isRecord(raw) && typeof raw.error === 'string' && raw.error) || `HTTP ${r.status}`)
        }
        const parsed = toPairingInfo(raw)
        if (!parsed || !parsed.payload) throw new Error('The box did not return a pairing payload.')
        if (!cancelled) setInfo(parsed)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setInfo(null)
        setError(errorMessage(err, 'Could not load pairing info.'))
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [])

  return (
    <Section
      title="Native Pairing"
      desc="This box's LAN certificate fingerprint, for pinning a native client on first connection."
    >
      <Banner tone="info" title="What this does">
        Native clients (see clients/core/) are meant to pin this box&rsquo;s TLS certificate to the
        fingerprint below instead of relying on a public certificate authority. No native client
        app exists yet to scan this code or complete a pairing handshake, so nothing here connects
        end to end for a real user today &mdash; this is preparation for when one does.
      </Banner>

      {loading && (
        <Card>
          <p className="text-sm text-[var(--text-tertiary)]">Loading pairing info&hellip;</p>
        </Card>
      )}

      {!loading && error && (
        <Banner tone="danger" title="Pairing info unavailable">
          {error}
        </Banner>
      )}

      {!loading && !error && info && (
        <Card
          title="Pairing code"
          desc="Scan this once a native client exists, or compare the fingerprint below over a trusted channel in the meantime."
        >
          <div className="flex flex-col items-center gap-4 py-2">
            <PairingQR content={info.payload || ''} />
          </div>
          <InfoList className="mt-4">
            <InfoRow label="Box name" value={info.name} />
            <InfoRow label="LAN address" value={info.addr} mono />
            <InfoRow label="Fingerprint (SPKI SHA-256)" value={info.spkiHex} mono />
          </InfoList>
        </Card>
      )}
    </Section>
  )
}
