import { useCallback, useRef, useState } from 'react'
import {
  AlertCircle,
  CheckCircle,
  ChevronDown,
  ChevronRight,
  FileSearch,
  Hash,
  Link as LinkIcon,
  Loader2,
  Shield,
  ShieldAlert,
  ShieldCheck,
  Upload,
  User,
  XCircle,
} from 'lucide-react'
import { Button, Card, Input } from './ui'

/*
 * Verify — public verification page.
 *
 * Aesthetic direction (mirrors SignView):
 *   - Public surface — force light data-theme so a visitor's OS dark mode
 *     never colours a trust-anchor page in slate.
 *   - Warm paper card for the drag-drop zone; serif headline; calm reveal
 *     of the verdict with a sage `ShieldCheck` or persimmon `ShieldAlert`.
 *   - Per-signer rows in serif body, collapsible — quiet by default.
 *   - All "yes" affordances use the single accent; signal colours stay
 *     in the sage / honey / persimmon palette.
 */

// ─────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────

function Pill({ ok, label }) {
  return (
    <span
      className={[
        'inline-flex items-center gap-1 px-2 py-0.5 rounded-pill text-2xs font-semibold tracking-tightish',
        ok ? 'bg-success-bg text-success' : 'bg-danger-bg text-danger',
      ].join(' ')}
    >
      {ok ? <CheckCircle size={11} /> : <XCircle size={11} />}
      {label}
    </span>
  )
}

function HashDisplay({ hash, label }) {
  if (!hash) return null
  return (
    <div className="flex items-start gap-2 mt-1.5">
      <Hash size={12} className="mt-0.5 text-ink-faint shrink-0" />
      <div className="min-w-0">
        <span className="text-2xs text-ink-faint tracking-tightish">{label}: </span>
        <span className="font-mono text-2xs text-ink-muted break-all">{hash}</span>
      </div>
    </div>
  )
}

function SignerRow({ signer }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="bg-paper border border-line rounded-md overflow-hidden">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="w-full flex items-center gap-3 px-4 py-3 hover:bg-bg-elev2 transition-colors duration-fast ease-out text-left"
      >
        <User size={14} className="text-ink-faint shrink-0" />
        <div className="flex-1 min-w-0">
          <span className="font-serif italic text-sm text-ink">
            {signer.name || signer.signer_id}
          </span>
          {signer.email && (
            <span className="ml-2 font-serif italic text-2xs text-ink-faint">
              {signer.email}
            </span>
          )}
        </div>
        <Pill ok={signer.token_ok} label={signer.token_ok ? 'Valid' : 'Invalid'} />
        {open
          ? <ChevronDown size={14} className="text-ink-faint" />
          : <ChevronRight size={14} className="text-ink-faint" />}
      </button>
      {open && (
        <div className="px-4 pb-4 pt-2 border-t border-line bg-bg-elev2 space-y-1.5 animate-fade-in">
          <div className="flex items-center gap-2 text-2xs text-ink-muted">
            <span className="font-medium tracking-tightish">Signer ID:</span>
            <span className="font-mono break-all">{signer.signer_id}</span>
          </div>
          {signer.identity && (
            <div className="flex items-center gap-2 text-2xs text-ink-muted">
              <span className="font-medium tracking-tightish">Identity:</span>
              <span className="font-serif italic">{signer.identity}</span>
            </div>
          )}
          {signer.signed_at && signer.signed_at !== '0001-01-01T00:00:00Z' && (
            <div className="flex items-center gap-2 text-2xs text-ink-muted">
              <span className="font-medium tracking-tightish">Signed at:</span>
              <span>{new Date(signer.signed_at).toLocaleString()}</span>
            </div>
          )}
          {signer.token_error && (
            <div className="mt-2 px-2 py-1.5 rounded-sm bg-danger-bg border border-line text-2xs text-danger flex items-center gap-1.5">
              <AlertCircle size={12} className="shrink-0" />
              {signer.token_error}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ─────────────────────────────────────────────────────────
// Main component
// ─────────────────────────────────────────────────────────

export default function Verify() {
  const [dragging, setDragging] = useState(false)
  const [file, setFile] = useState(null)
  const [envelopeId, setEnvelopeId] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [result, setResult] = useState(null)
  const inputRef = useRef(null)

  // ── drag-and-drop ──
  const onDragOver = useCallback((e) => {
    e.preventDefault()
    setDragging(true)
  }, [])
  const onDragLeave = useCallback(() => setDragging(false), [])
  const onDrop = useCallback((e) => {
    e.preventDefault()
    setDragging(false)
    const dropped = e.dataTransfer.files[0]
    if (dropped && dropped.type === 'application/pdf') {
      setFile(dropped)
      setResult(null)
      setError(null)
    }
  }, [])

  function onFileInput(e) {
    const chosen = e.target.files[0]
    if (chosen) {
      setFile(chosen)
      setResult(null)
      setError(null)
    }
  }

  // ── submit ──
  async function handleVerify(e) {
    e.preventDefault()
    setError(null)
    setResult(null)
    setLoading(true)

    try {
      let res
      if (file) {
        const form = new FormData()
        form.append('pdf', file)
        res = await fetch('/api/sign/verify', { method: 'POST', body: form })
      } else if (envelopeId.trim()) {
        res = await fetch('/api/sign/verify', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ envelope_id: envelopeId.trim() }),
        })
      } else {
        setError('Upload a sealed PDF or enter an envelope ID.')
        setLoading(false)
        return
      }

      const data = await res.json()
      if (res.ok || res.status === 422) {
        setResult(data)
      } else {
        setError(data.error || 'Verification request failed.')
      }
    } catch (err) {
      setError('Network error: ' + err.message)
    } finally {
      setLoading(false)
    }
  }

  function reset() {
    setFile(null)
    setResult(null)
    setError(null)
    setEnvelopeId('')
    if (inputRef.current) inputRef.current.value = ''
  }

  // ─────────────────────────────────────────────────────
  // Render
  // ─────────────────────────────────────────────────────
  return (
    <div data-theme="light" className="min-h-screen bg-bg paper-grain py-12 px-4">
      <div className="max-w-2xl mx-auto space-y-8">

        {/* Quiet, trustworthy header */}
        <div className="text-center space-y-2">
          <div className="flex justify-center mb-1">
            <div className="w-12 h-12 rounded-md bg-accent text-white flex items-center justify-center">
              <FileSearch size={22} />
            </div>
          </div>
          <p className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
            Public verification
          </p>
          <h1 className="font-serif text-3xl text-ink leading-tight">
            Verify a Vulos-signed document
          </h1>
          <p className="text-sm text-ink-muted leading-snug max-w-md mx-auto">
            Drop a sealed PDF to check its cryptographic integrity and audit chain.
            No account required.
          </p>
        </div>

        {/* Upload form */}
        {!result && (
          <form onSubmit={handleVerify} className="animate-fade-in">
            <Card variant="raised" className="p-6 space-y-5">

              {/* Drop zone — warm paper, single accent on hover */}
              <div
                onDragOver={onDragOver}
                onDragLeave={onDragLeave}
                onDrop={onDrop}
                onClick={() => inputRef.current?.click()}
                className={[
                  'relative cursor-pointer border-2 border-dashed rounded-lg',
                  'p-10 text-center transition-colors duration-fast ease-out',
                  dragging
                    ? 'border-accent bg-accent-tint'
                    : 'border-line-strong hover:border-accent hover:bg-accent-tint',
                ].join(' ')}
              >
                <input
                  ref={inputRef}
                  type="file"
                  accept="application/pdf,.pdf"
                  className="hidden"
                  onChange={onFileInput}
                />
                <Upload size={26} className="mx-auto mb-3 text-ink-faint" />
                {file ? (
                  <p className="text-sm font-medium text-ink tracking-tightish">{file.name}</p>
                ) : (
                  <>
                    <p className="text-sm text-ink tracking-tightish">
                      Drop a sealed PDF here, or{' '}
                      <span className="text-accent font-medium">click to browse</span>
                    </p>
                    <p className="text-2xs text-ink-faint mt-1 font-serif italic">
                      We never store your document.
                    </p>
                  </>
                )}
              </div>

              {/* Divider — quiet */}
              <div className="flex items-center gap-3">
                <div className="flex-1 border-t border-line" />
                <span className="text-2xs text-ink-faint tracking-eyebrow uppercase">
                  or verify by ID
                </span>
                <div className="flex-1 border-t border-line" />
              </div>

              {/* Envelope ID input */}
              <Input
                leading={<LinkIcon size={14} />}
                value={envelopeId}
                onChange={(e) => setEnvelopeId(e.target.value)}
                placeholder="Envelope ID (e.g. abc123-...)"
              />

              {error && (
                <div className="flex items-center gap-2 px-3 py-2 rounded-sm bg-danger-bg text-xs text-danger">
                  <AlertCircle size={14} className="shrink-0" />
                  {error}
                </div>
              )}

              <Button
                type="submit"
                variant="primary"
                size="lg"
                fullWidth
                disabled={loading || (!file && !envelopeId.trim())}
              >
                {loading
                  ? <><Loader2 size={15} className="animate-spin" /> Verifying…</>
                  : <><Shield size={15} /> Verify document</>}
              </Button>
            </Card>
          </form>
        )}

        {/* Result panel — calm reveal */}
        {result && (
          <div className="space-y-5 animate-fade-in">

            {/* Overall verdict — quiet card, single sage / persimmon icon */}
            <Card
              variant="raised"
              className={[
                'p-6 flex items-center gap-5',
                result.ok ? 'border-success' : 'border-danger',
              ].join(' ')}
            >
              <div
                className={[
                  'w-14 h-14 rounded-full flex items-center justify-center flex-shrink-0',
                  result.ok ? 'bg-success-bg' : 'bg-danger-bg',
                ].join(' ')}
              >
                {result.ok
                  ? <ShieldCheck size={28} className="text-success animate-scale-in" />
                  : <ShieldAlert size={28} className="text-danger animate-scale-in" />}
              </div>
              <div className="flex-1 min-w-0">
                <p className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase mb-1">
                  {result.ok ? 'Verified' : 'Tampering detected'}
                </p>
                <p className="font-serif text-xl text-ink leading-tight">
                  {result.ok ? 'All checks passed.' : 'Verification failed.'}
                </p>
                {result.title && (
                  <p className="text-sm text-ink-muted mt-1.5 font-serif italic">
                    {result.title}
                  </p>
                )}
                {result.envelope_id && (
                  <p className="font-mono text-2xs text-ink-faint mt-1 break-all">
                    {result.envelope_id}
                  </p>
                )}
              </div>
            </Card>

            {/* Check rows */}
            <Card>
              <div className="divide-y divide-line">

                {/* Hash match */}
                <div className="px-4 py-3 flex items-start gap-3">
                  <div className="pt-0.5">
                    {result.hash_match
                      ? <CheckCircle size={16} className="text-success" />
                      : <XCircle size={16} className="text-danger" />}
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="text-sm font-medium text-ink tracking-tightish">
                        Document hash
                      </span>
                      <Pill ok={result.hash_match} label={result.hash_match ? 'Match' : 'Mismatch'} />
                    </div>
                    <HashDisplay hash={result.final_doc_hash} label="Expected" />
                    {result.hash_error && (
                      <p className="mt-1.5 text-2xs text-danger">{result.hash_error}</p>
                    )}
                  </div>
                </div>

                {/* Chain integrity */}
                <div className="px-4 py-3 flex items-start gap-3">
                  <div className="pt-0.5">
                    {result.chain_ok
                      ? <CheckCircle size={16} className="text-success" />
                      : <XCircle size={16} className="text-danger" />}
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="text-sm font-medium text-ink tracking-tightish">
                        Audit chain integrity
                      </span>
                      <Pill ok={result.chain_ok} label={result.chain_ok ? 'Intact' : 'Broken'} />
                    </div>
                    <p className="text-2xs text-ink-faint mt-1">
                      {result.total_audit_events} audit event{result.total_audit_events !== 1 ? 's' : ''}
                    </p>
                    {result.chain_error && (
                      <p className="mt-1.5 text-2xs text-danger">{result.chain_error}</p>
                    )}
                  </div>
                </div>

                {/* Sealed at */}
                {result.sealed_at && result.sealed_at !== '0001-01-01T00:00:00Z' && (
                  <div className="px-4 py-3 text-2xs text-ink-faint tracking-tightish">
                    Sealed {new Date(result.sealed_at).toLocaleString()}
                  </div>
                )}
              </div>
            </Card>

            {/* Per-signer results — collapsible */}
            {result.signers && result.signers.length > 0 && (
              <div className="space-y-3">
                <h2 className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase px-1">
                  Signers ({result.signers.length})
                </h2>
                <div className="space-y-2">
                  {result.signers.map((s) => (
                    <SignerRow key={s.signer_id} signer={s} />
                  ))}
                </div>
              </div>
            )}

            {/* Verify another */}
            <Button variant="secondary" fullWidth onClick={reset}>
              Verify another document
            </Button>
          </div>
        )}

        {/* Quiet provenance footer */}
        <p className="text-2xs text-ink-faint text-center tracking-eyebrow uppercase pt-2">
          Powered by Vulos Office
        </p>
      </div>
    </div>
  )
}
