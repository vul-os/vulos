import { useCallback, useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router-dom'
import * as pdfjsLib from 'pdfjs-dist'
import SignaturePad from 'signature_pad'
import {
  AlertCircle,
  Check,
  CheckCircle,
  ChevronDown,
  FileText,
  Loader2,
  Lock,
  Pen,
  RefreshCw,
  Type,
  Upload,
} from 'lucide-react'

pdfjsLib.GlobalWorkerOptions.workerSrc = new URL(
  'pdfjs-dist/build/pdf.worker.min.mjs',
  import.meta.url
).href

/*
 * SignView — public signer page.
 *
 * Aesthetic direction:
 *   The signer has no Vulos account; this page is their first (and possibly
 *   only) impression of the product.  Lean into "quiet, trustworthy paper":
 *     - oat/paper background, not slate
 *     - serif for the document context (signer name, doc title)
 *     - ONE accent (deep teal) on every "Sign here" affordance — so the path
 *       through the document is obvious without being shouty
 *     - all field types share the accent; differentiation is the LABEL, not
 *       a rainbow of borders (the previous design read like a kindergarten).
 */
const FIELD_LABELS_AND_HUE = {
  // Single accent for all fields keeps the page calm.  We vary the label only.
  signature: 'Signature',
  initial:   'Initial',
  date:      'Date',
  name:      'Full name',
  text:      'Text',
}

// Kept for backwards-compat in case any code reads from it; new layout uses
// the unified `field-affordance` style.
const FIELD_COLORS = {
  signature: 'border-accent bg-accent-tint',
  initial:   'border-accent bg-accent-tint',
  date:      'border-accent bg-accent-tint',
  name:      'border-accent bg-accent-tint',
  text:      'border-accent bg-accent-tint',
}

const FIELD_LABELS = FIELD_LABELS_AND_HUE

const TYPED_FONTS = [
  { label: 'Elegant', value: '"Dancing Script", cursive' },
  { label: 'Classic', value: '"Pacifico", cursive' },
  { label: 'Neat',    value: '"Satisfy", cursive' },
  { label: 'Formal',  value: 'Georgia, serif' },
]

// Load Google Fonts for typed signature preview
const GFONTS_LINK = 'https://fonts.googleapis.com/css2?family=Dancing+Script:wght@600&family=Pacifico&family=Satisfy&display=swap'
function ensureGFonts() {
  if (document.querySelector(`link[href="${GFONTS_LINK}"]`)) return
  const link = document.createElement('link')
  link.rel = 'stylesheet'
  link.href = GFONTS_LINK
  document.head.appendChild(link)
}

// ── DrawPad: canvas-based draw mode using signature_pad ──────────
function DrawPad({ onDataUrl }) {
  const canvasRef = useRef(null)
  const padRef    = useRef(null)

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const pad = new SignaturePad(canvas, { backgroundColor: 'rgb(255,255,255)' })
    padRef.current = pad
    const notify = () => {
      if (!pad.isEmpty()) onDataUrl(pad.toDataURL('image/png'))
      else onDataUrl(null)
    }
    pad.addEventListener('endStroke', notify)
    return () => {
      pad.removeEventListener('endStroke', notify)
      pad.off()
    }
  }, [onDataUrl])

  const clear = () => {
    padRef.current?.clear()
    onDataUrl(null)
  }

  return (
    <div className="space-y-2">
      <canvas
        ref={canvasRef}
        width={400}
        height={120}
        className="w-full border border-line rounded-md bg-paper touch-none"
        style={{ maxHeight: 120 }}
      />
      <button
        type="button"
        onClick={clear}
        className="flex items-center gap-1 text-xs text-ink-muted hover:text-ink transition-colors"
      >
        <RefreshCw className="w-3 h-3" /> Clear
      </button>
    </div>
  )
}

// ── TypedPad: text → PNG via canvas ─────────────────────────────
function TypedPad({ signerName, onDataUrl }) {
  const [text, setText] = useState(signerName || '')
  const [fontIdx, setFontIdx] = useState(0)
  const canvasRef = useRef(null)

  const render = useCallback(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const ctx = canvas.getContext('2d')
    ctx.clearRect(0, 0, canvas.width, canvas.height)
    ctx.fillStyle = '#fff'
    ctx.fillRect(0, 0, canvas.width, canvas.height)
    const font = TYPED_FONTS[fontIdx].value
    ctx.font = `36px ${font}`
    ctx.fillStyle = '#1a1a2e'
    ctx.textBaseline = 'middle'
    ctx.fillText(text || '', 12, canvas.height / 2)
    if (text.trim()) onDataUrl(canvas.toDataURL('image/png'))
    else onDataUrl(null)
  }, [text, fontIdx, onDataUrl])

  useEffect(() => { ensureGFonts() }, [])
  useEffect(() => { render() }, [render])

  return (
    <div className="space-y-3">
      <input
        type="text"
        value={text}
        onChange={e => setText(e.target.value)}
        placeholder="Type your name"
        className="w-full bg-paper border border-line rounded-md px-3 py-2 text-sm outline-none focus:border-accent focus:shadow-focus transition-colors"
      />

      {/* font picker */}
      <div className="flex gap-2 flex-wrap">
        {TYPED_FONTS.map((f, i) => (
          <button
            key={f.label}
            type="button"
            onClick={() => setFontIdx(i)}
            className={`px-3 py-1 text-sm rounded-pill border transition-colors ${
              fontIdx === i
                ? 'border-accent bg-accent-tint text-accent-press'
                : 'border-line text-ink-muted hover:border-line-strong'
            }`}
            style={{ fontFamily: f.value }}
          >
            {f.label}
          </button>
        ))}
      </div>

      {/* preview canvas (hidden — just used for PNG generation) */}
      <canvas
        ref={canvasRef}
        width={400}
        height={80}
        className="w-full border border-line rounded-md bg-paper"
        style={{ fontFamily: TYPED_FONTS[fontIdx].value }}
      />
    </div>
  )
}

// ── UploadPad: file upload → base64 data URL ─────────────────────
function UploadPad({ onDataUrl }) {
  const [preview, setPreview] = useState(null)

  const onFile = e => {
    const file = e.target.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = ev => {
      const url = ev.target.result
      setPreview(url)
      onDataUrl(url)
    }
    reader.readAsDataURL(file)
  }

  return (
    <div className="space-y-3">
      <label className="flex flex-col items-center justify-center border border-dashed border-line-strong rounded-md py-6 cursor-pointer hover:border-accent hover:bg-accent-tint transition-colors bg-bg-elev2">
        <Upload className="w-6 h-6 text-ink-faint mb-1" />
        <span className="text-sm text-ink-muted">Click to upload PNG / JPG</span>
        <input type="file" accept="image/png,image/jpeg" className="hidden" onChange={onFile} />
      </label>
      {preview && (
        <img src={preview} alt="uploaded signature" className="max-h-20 object-contain rounded-sm border border-line" />
      )}
    </div>
  )
}

// ── FieldFillModal: let the signer fill one field ───────────────
function FieldFillModal({ field, signerName, onSave, onClose }) {
  const isSignatureOrInitial = field.type === 'signature' || field.type === 'initial'
  const [mode, setMode] = useState('draw') // draw | type | upload
  const [dataUrl, setDataUrl] = useState(null)
  const [textValue, setTextValue] = useState(
    field.type === 'date' ? new Date().toLocaleDateString() : ''
  )

  const canSave = isSignatureOrInitial ? !!dataUrl : !!textValue.trim()

  const save = () => {
    if (!canSave) return
    onSave(field.id, isSignatureOrInitial ? dataUrl : textValue.trim())
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center p-4 animate-fade-in"
      onClick={e => { if (e.target === e.currentTarget) onClose() }}
      style={{ background: 'rgba(26, 25, 22, 0.36)', backdropFilter: 'blur(2px)' }}
    >
      <div className="bg-paper rounded-xl shadow-e3 border border-line w-full max-w-lg overflow-hidden animate-scale-in">
        <div className="px-5 py-3.5 border-b border-line flex items-center justify-between">
          <h3 className="font-semibold text-ink text-md tracking-tightish">
            Fill {FIELD_LABELS[field.type] ?? field.type}
            {field.required && <span className="ml-1 text-danger">*</span>}
          </h3>
          <button onClick={onClose} className="text-ink-faint hover:text-ink text-lg leading-none transition-colors">&times;</button>
        </div>

        <div className="p-5 space-y-4">
          {/* signature / initial: draw / type / upload — segmented control */}
          {isSignatureOrInitial && (
            <>
              <div className="flex p-0.5 bg-bg-elev2 rounded-md border border-line">
                {[
                  { id: 'draw',   label: 'Draw',   icon: Pen },
                  { id: 'type',   label: 'Type',   icon: Type },
                  { id: 'upload', label: 'Upload', icon: Upload },
                ].map(({ id, label, icon: Icon }) => (
                  <button
                    key={id}
                    type="button"
                    onClick={() => { setMode(id); setDataUrl(null) }}
                    className={`flex-1 flex items-center justify-center gap-1.5 h-8 text-xs font-medium rounded-sm transition-all ${
                      mode === id
                        ? 'bg-paper text-ink shadow-e1'
                        : 'bg-transparent text-ink-muted hover:text-ink'
                    }`}
                  >
                    <Icon className="w-3.5 h-3.5" />
                    {label}
                  </button>
                ))}
              </div>

              {mode === 'draw'   && <DrawPad   onDataUrl={setDataUrl} />}
              {mode === 'type'   && <TypedPad  signerName={signerName} onDataUrl={setDataUrl} />}
              {mode === 'upload' && <UploadPad onDataUrl={setDataUrl} />}
            </>
          )}

          {/* date: auto-filled, editable */}
          {field.type === 'date' && (
            <div>
              <label className="text-xs text-ink-muted font-medium mb-1.5 block tracking-tightish">Date</label>
              <input
                type="text"
                value={textValue}
                onChange={e => setTextValue(e.target.value)}
                className="w-full bg-paper border border-line rounded-md px-3 py-2 text-sm outline-none focus:border-accent focus:shadow-focus transition-colors"
              />
            </div>
          )}

          {/* name / text */}
          {(field.type === 'name' || field.type === 'text') && (
            <div>
              <label className="text-xs text-ink-muted font-medium mb-1.5 block tracking-tightish">
                {FIELD_LABELS[field.type]}
              </label>
              <input
                type="text"
                value={textValue}
                onChange={e => setTextValue(e.target.value)}
                placeholder={field.type === 'name' ? 'Your full name' : 'Enter text'}
                className="w-full bg-paper border border-line rounded-md px-3 py-2 text-sm outline-none focus:border-accent focus:shadow-focus transition-colors"
              />
            </div>
          )}
        </div>

        <div className="px-5 py-3 border-t border-line bg-bg-elev2 flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="h-8 px-3 text-sm text-ink-muted bg-paper border border-line rounded-md hover:bg-bg-elev2 hover:border-line-strong transition-colors"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={save}
            disabled={!canSave}
            className="h-8 px-3 text-sm font-medium text-white bg-accent rounded-md hover:bg-accent-hover disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
          >
            Apply
          </button>
        </div>
      </div>
    </div>
  )
}

// ── SignView — public signer page. No Vulos login required. ──────
// Route: /sign/:token
export default function SignView() {
  const { token } = useParams()

  const [state, setState] = useState('loading') // loading | locked | error | ready | done
  const [view, setView] = useState(null)         // SignerViewResponse from API
  const [errorMsg, setErrorMsg] = useState('')

  // PDF rendering state
  const [pdfPages, setPdfPages] = useState([])
  const [pdfLoading, setPdfLoading] = useState(false)

  // Ceremony state
  const [fieldValues, setFieldValues] = useState({}) // fieldId → value (dataUrl or text)
  const [activeField, setActiveField] = useState(null) // field being filled
  const [consent, setConsent] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState('')

  // ── fetch the scoped view from the backend ──────────────────
  useEffect(() => {
    if (!token) return

    fetch(`/api/sign/${token}`)
      .then(async (res) => {
        const data = await res.json()
        if (res.status === 403 && data.locked) {
          setState('locked')
          return
        }
        if (!res.ok) {
          setErrorMsg(data.error || 'Could not load signing session.')
          setState('error')
          return
        }
        setView(data)
        // Auto-fill date fields
        const autoValues = {}
        for (const f of (data.fields ?? [])) {
          if (f.type === 'date') autoValues[f.id] = new Date().toLocaleDateString()
        }
        setFieldValues(autoValues)
        setState('ready')
      })
      .catch(() => {
        setErrorMsg('Network error. Please try again.')
        setState('error')
      })
  }, [token])

  // ── render the source PDF once the view is ready ────────────
  useEffect(() => {
    if (state !== 'ready' || !view?.source_file) return

    const pdfUrl = view.source_file.startsWith('/')
      ? view.source_file
      : `/api/uploads/${view.source_file}`

    setPdfLoading(true)
    pdfjsLib.getDocument(pdfUrl).promise
      .then(async (pdf) => {
        const pages = []
        for (let i = 1; i <= pdf.numPages; i++) {
          const page = await pdf.getPage(i)
          const scale = 1.5
          const viewport = page.getViewport({ scale })
          const canvas = document.createElement('canvas')
          canvas.width = viewport.width
          canvas.height = viewport.height
          const ctx = canvas.getContext('2d')
          await page.render({ canvasContext: ctx, viewport }).promise
          pages.push({ canvas, width: viewport.width, height: viewport.height, pageNum: i })
        }
        setPdfPages(pages)
      })
      .catch(() => {})
      .finally(() => setPdfLoading(false))
  }, [state, view])

  // ── derived helpers ───────────────────────────────────────────
  const fields = view?.fields ?? []
  const requiredFields = fields.filter(f => f.required)
  const allRequiredFilled = requiredFields.every(f => !!fieldValues[f.id])
  const canSubmit = allRequiredFilled && consent && !submitting

  const isFilled = id => !!fieldValues[id]

  const handleFieldFill = (fieldId, value) => {
    setFieldValues(prev => ({ ...prev, [fieldId]: value }))
    setActiveField(null)
  }

  // ── submit ────────────────────────────────────────────────────
  const handleSubmit = async () => {
    if (!canSubmit) return
    setSubmitting(true)
    setSubmitError('')

    // Build fieldValues map: {fieldId: value, ...}
    const fieldValuesPayload = {}
    for (const f of fields) {
      fieldValuesPayload[f.id] = fieldValues[f.id] ?? ''
    }

    try {
      const res = await fetch(`/api/sign/${token}/complete`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(fieldValuesPayload),
      })
      const data = await res.json()
      if (!res.ok) {
        setSubmitError(data.error || 'Submission failed. Please try again.')
        return
      }
      setState('done')
    } catch {
      setSubmitError('Network error. Please try again.')
    } finally {
      setSubmitting(false)
    }
  }

  // ── render helpers ────────────────────────────────────────────

  // Shared centred-frame layout for the four "informational" states.
  // Force light data-theme on these surfaces — a public signer page should
  // always feel like warm paper, regardless of the visitor's OS dark mode.
  const StatusFrame = ({ children }) => (
    <div data-theme="light" className="h-screen flex flex-col items-center justify-center gap-4 bg-bg px-4 paper-grain">
      {children}
    </div>
  )

  if (state === 'loading') {
    return (
      <StatusFrame>
        <Loader2 className="w-7 h-7 text-accent animate-spin" />
        <p className="text-sm text-ink-muted font-serif italic">Loading your signing session…</p>
      </StatusFrame>
    )
  }

  if (state === 'locked') {
    return (
      <StatusFrame>
        <Lock className="w-10 h-10 text-warning" />
        <h1 className="font-serif text-2xl text-ink">Not your turn yet</h1>
        <p className="text-sm text-ink-muted text-center max-w-sm leading-snug">
          A prior signer must complete their signature before this link becomes active.
          You'll be notified when it's your turn.
        </p>
      </StatusFrame>
    )
  }

  if (state === 'error') {
    return (
      <StatusFrame>
        <AlertCircle className="w-10 h-10 text-danger" />
        <h1 className="font-serif text-2xl text-ink">Link unavailable</h1>
        <p className="text-sm text-ink-muted text-center max-w-sm leading-snug">{errorMsg}</p>
      </StatusFrame>
    )
  }

  if (state === 'done') {
    return (
      <StatusFrame>
        <div className="w-14 h-14 rounded-full bg-success-bg flex items-center justify-center">
          <CheckCircle className="w-8 h-8 text-success" />
        </div>
        <h1 className="font-serif text-3xl text-ink">Signed.</h1>
        <p className="text-sm text-ink-muted text-center max-w-sm leading-snug">
          Your signature has been submitted and recorded. You may close this window.
        </p>
        <p className="mt-6 text-2xs text-ink-faint tracking-eyebrow uppercase">
          Vulos Office
        </p>
      </StatusFrame>
    )
  }

  // ── state === 'ready' ─────────────────────────────────────────────────
  // Public surface — force light theme so the page always feels like paper,
  // and dial back the colour vocabulary: one accent for "act here", a single
  // sage success colour for "done", everything else warm neutral.

  const filledCount = fields.filter((f) => isFilled(f.id)).length
  const progressPct = fields.length ? Math.round((filledCount / fields.length) * 100) : 0

  return (
    <div data-theme="light" className="min-h-screen bg-bg paper-grain">
      {/* ── Header — quiet, doc-first ── */}
      <header className="bg-paper border-b border-line">
        <div className="max-w-4xl mx-auto px-6 py-5 flex items-center gap-4">
          <div className="w-10 h-10 rounded-md bg-accent text-white flex items-center justify-center flex-shrink-0">
            <FileText className="w-5 h-5" />
          </div>
          <div className="flex-1 min-w-0">
            <p className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase mb-0.5">
              Signature requested
            </p>
            {/* The signer's name is the document — use the serif. */}
            <h1 className="font-serif text-xl text-ink truncate leading-tight">
              {view.signer_name}
            </h1>
          </div>
          <div className="hidden sm:flex flex-col items-end gap-1 flex-shrink-0">
            <span className="text-2xs text-ink-faint tracking-eyebrow uppercase">
              {filledCount} of {fields.length} complete
            </span>
            {/* Tiny progress bar — calm, never alarming. */}
            <div className="w-32 h-1 rounded-pill bg-bg-elev2 overflow-hidden">
              <div
                className="h-full bg-accent transition-[width] duration-slow ease-spring"
                style={{ width: `${progressPct}%` }}
              />
            </div>
          </div>
        </div>
      </header>

      <div className="max-w-4xl mx-auto px-6 py-8 space-y-8">

        {/* ── Field checklist ── Quiet card, all fields share the accent label */}
        <section className="bg-paper rounded-lg border border-line overflow-hidden animate-fade-in">
          <div className="px-4 py-3 border-b border-line flex items-center justify-between">
            <h2 className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
              Fields to complete
            </h2>
            <span className="text-2xs text-ink-faint">
              {fields.length} {fields.length === 1 ? 'field' : 'fields'}
            </span>
          </div>

          {fields.length === 0 ? (
            <p className="text-sm text-ink-faint font-serif italic px-4 py-6">
              No fields assigned to you.
            </p>
          ) : (
            <ul className="divide-y divide-line">
              {fields.map((f, idx) => {
                const filled = isFilled(f.id)
                return (
                  <li
                    key={f.id}
                    className="flex items-center gap-3 px-4 py-3 hover:bg-bg-elev2 transition-colors"
                  >
                    {/* Step indicator: number, or check when filled */}
                    <span
                      className={[
                        'w-6 h-6 rounded-full flex items-center justify-center flex-shrink-0',
                        'text-2xs font-semibold tracking-tightish transition-colors',
                        filled
                          ? 'bg-success text-white'
                          : 'bg-bg-elev2 text-ink-muted border border-line',
                      ].join(' ')}
                    >
                      {filled ? <Check className="w-3 h-3" /> : idx + 1}
                    </span>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="text-sm text-ink font-medium tracking-tightish">
                          {FIELD_LABELS[f.type] ?? f.type}
                        </span>
                        {f.required && (
                          <span className="text-2xs text-danger">required</span>
                        )}
                      </div>
                      <span className="text-2xs text-ink-faint">
                        Page {f.page}
                      </span>
                    </div>
                    <button
                      type="button"
                      onClick={() => setActiveField(f)}
                      className={[
                        'h-7 px-3 text-xs font-medium rounded-md tracking-tightish',
                        'transition-colors duration-fast ease-out',
                        filled
                          ? 'text-ink-muted border border-line hover:bg-bg-elev2'
                          : 'text-white bg-accent hover:bg-accent-hover',
                      ].join(' ')}
                    >
                      {filled ? 'Edit' : 'Fill'}
                    </button>
                  </li>
                )
              })}
            </ul>
          )}
        </section>

        {/* ── PDF viewer with field overlays ── */}
        <section className="space-y-4">
          {pdfLoading && (
            <div className="flex items-center gap-2 text-sm text-ink-faint font-serif italic">
              <Loader2 className="w-4 h-4 animate-spin" />
              Loading document…
            </div>
          )}

          {pdfPages.map(({ canvas, width, height, pageNum }) => {
            const pageFields = fields.filter((f) => f.page === pageNum)
            return (
              <div
                key={pageNum}
                className="relative bg-paper border border-line shadow-e1 rounded-lg overflow-hidden mx-auto animate-fade-in"
                style={{ width, maxWidth: '100%' }}
              >
                <img
                  src={canvas.toDataURL()}
                  alt={`Page ${pageNum}`}
                  style={{ width: '100%', display: 'block' }}
                />

                {pageFields.map((f) => {
                  const filled = isFilled(f.id)
                  const isImg = filled && fieldValues[f.id]?.startsWith('data:image')
                  return (
                    <div
                      key={f.id}
                      onClick={() => setActiveField(f)}
                      className={[
                        'absolute border rounded-sm flex items-center justify-center cursor-pointer',
                        'transition-all duration-fast ease-out',
                        filled
                          ? 'border-success bg-success-bg'
                          : 'border-accent bg-accent-tint hover:bg-accent-tint-2 hover:border-accent-press',
                      ].join(' ')}
                      style={{ left: f.x, top: f.y, width: f.w, height: f.h }}
                    >
                      {filled && isImg ? (
                        <img
                          src={fieldValues[f.id]}
                          alt="signature"
                          style={{ maxWidth: '100%', maxHeight: '100%', objectFit: 'contain' }}
                        />
                      ) : filled ? (
                        <span className="text-xs font-medium text-ink px-1 truncate w-full text-center">
                          {fieldValues[f.id]}
                        </span>
                      ) : (
                        <span className="text-2xs font-semibold tracking-eyebrow uppercase text-accent-press select-none px-1 truncate">
                          {FIELD_LABELS[f.type] ?? f.type}{f.required ? ' *' : ''}
                        </span>
                      )}
                    </div>
                  )
                })}
              </div>
            )
          })}
        </section>

        {/* ── Consent + submit ── */}
        <section className="bg-paper rounded-lg border border-line p-5 space-y-4 animate-fade-in">
          <label className="flex items-start gap-3 cursor-pointer">
            {/* Custom checkbox — accent-tinted when on; sage box otherwise */}
            <div
              className={[
                'mt-0.5 w-4 h-4 rounded-xs flex items-center justify-center flex-shrink-0',
                'transition-all duration-fast ease-out',
                consent
                  ? 'bg-accent border-2 border-accent'
                  : 'bg-paper border-2 border-line-strong',
              ].join(' ')}
            >
              {consent && <Check className="w-2.5 h-2.5 text-white" />}
            </div>
            <input
              type="checkbox"
              className="sr-only"
              checked={consent}
              onChange={e => setConsent(e.target.checked)}
            />
            <span className="text-sm text-ink-muted leading-snug">
              I consent to signing this document electronically. My electronic
              signature is legally equivalent to a handwritten signature.
            </span>
          </label>

          {requiredFields.length > 0 && !allRequiredFilled && (
            <p className="text-xs text-warning flex items-center gap-1.5 bg-warning-bg rounded-sm px-2 py-1.5">
              <AlertCircle className="w-3.5 h-3.5 flex-shrink-0" />
              Please fill all required fields before submitting.
            </p>
          )}

          {submitError && (
            <p className="text-xs text-danger flex items-center gap-1.5 bg-danger-bg rounded-sm px-2 py-1.5">
              <AlertCircle className="w-3.5 h-3.5 flex-shrink-0" />
              {submitError}
            </p>
          )}

          <button
            type="button"
            onClick={handleSubmit}
            disabled={!canSubmit}
            className="w-full py-3 rounded-md text-sm font-semibold text-white bg-accent
              hover:bg-accent-hover active:bg-accent-press disabled:opacity-40 disabled:cursor-not-allowed
              transition-colors duration-fast ease-out flex items-center justify-center gap-2 shadow-e1 tracking-tightish"
          >
            {submitting
              ? <><Loader2 className="w-4 h-4 animate-spin" /> Submitting…</>
              : <><CheckCircle className="w-4 h-4" /> Submit signature</>}
          </button>
        </section>

        {/* Quiet footer — establishes provenance without screaming */}
        <p className="text-2xs text-ink-faint text-center tracking-eyebrow uppercase pb-8">
          Powered by Vulos Office
        </p>
      </div>

      {/* ── field fill modal ── */}
      {activeField && (
        <FieldFillModal
          field={activeField}
          signerName={view.signer_name}
          onSave={handleFieldFill}
          onClose={() => setActiveField(null)}
        />
      )}
    </div>
  )
}
