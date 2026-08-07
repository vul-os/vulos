import { useState, useEffect, useCallback, useRef } from 'react'
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
function PairingQR({ content }: { content: string }) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [renderError, setRenderError] = useState('')

  useEffect(() => {
    if (!content || !canvasRef.current) return
    // eslint-disable-next-line react-hooks/set-state-in-effect -- clears a stale error from a previous payload before this draw attempt settles.
    setRenderError('')
    QRCode.toCanvas(canvasRef.current, content, {
      width: 200,
      margin: 2,
      color: { dark: '#0a0a0a', light: '#ffffff' },
      errorCorrectionLevel: 'M',
    }).catch((err: unknown) => setRenderError(errorMessage(err, 'QR render failed')))
  }, [content])

  if (renderError) {
    return (
      <div className="text-xs text-[var(--text-muted)] text-center p-4 rounded-xl border border-[var(--border-default)]">
        Could not render the QR code ({renderError}). Use the fingerprint below instead.
      </div>
    )
  }

  return (
    <canvas
      ref={canvasRef}
      role="img"
      aria-label="Pairing QR code"
      className="rounded-xl mx-auto block"
      style={{ imageRendering: 'pixelated', background: '#ffffff', padding: 12 }}
    />
  )
}

export default function LANPairingPanel() {
  const [info, setInfo] = useState<PairingInfo | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  const load = useCallback(() => {
    setLoading(true)
    setError(null)
    fetch('/api/lan/pairing', { credentials: 'include' })
      .then(async r => {
        // Untrusted network JSON — narrowed via toPairingInfo, never cast.
        const raw: unknown = await r.json().catch(() => null)
        if (!r.ok) {
          throw new Error((isRecord(raw) && typeof raw.error === 'string' && raw.error) || `HTTP ${r.status}`)
        }
        const parsed = toPairingInfo(raw)
        if (!parsed || !parsed.payload) throw new Error('The box did not return a pairing payload.')
        setInfo(parsed)
      })
      .catch((err: unknown) => { setInfo(null); setError(errorMessage(err, 'Could not load pairing info.')) })
      .finally(() => setLoading(false))
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { load() }, [load])

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
