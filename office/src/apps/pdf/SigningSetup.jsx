import { useEffect, useRef, useState, useCallback } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import * as pdfjsLib from 'pdfjs-dist'
import {
  ArrowLeft, Plus, Trash2, X, Save, ChevronLeft, ChevronRight,
  User, FileSignature, Type as TypeIcon, Calendar, Pen, AlignLeft,
  CheckSquare, Square, Upload,
} from 'lucide-react'
import { api } from '../../lib/api.js'
import { Button, IconButton, Input, Card, Tabs, Topbar, Tooltip } from '../../components/ui'

pdfjsLib.GlobalWorkerOptions.workerSrc = new URL(
  'pdfjs-dist/build/pdf.worker.min.mjs',
  import.meta.url
).href

/*
 * SigningSetup — "Prepare to Sign" editor.
 *
 * Aesthetic direction:
 *   - Warm paper canvas, oat sidebar, quiet topbar.
 *   - Field type chips as IconButtons w/ serif labels.
 *   - Signers as Cards with a colour stripe (their signer-color) — name in
 *     serif italic, email below in serif italic, role chip below.
 *   - Sequential / parallel chosen via underline Tabs.
 *   - Field overlays use single-accent palette; only the SIGNER stripe carries
 *     their personal color, so the page never reads like a rainbow.
 */

function genId() {
  return Math.random().toString(36).slice(2, 11)
}

// Signer colour palette — warm, used only for per-signer stripes and chips
// (the field overlays remain in the single accent palette).
const SIGNER_COLORS = [
  '#0f6a6c', // teal-600
  '#c08436', // honey
  '#4f7a4d', // sage
  '#4a6b8a', // dusty navy
  '#b8453a', // persimmon
  '#7b5ea0', // muted plum
  '#a07845', // amber-brown
  '#5e7a5e', // moss
  '#a06038', // burnt sienna
  '#6b6457', // oat-600
]

const FIELD_TYPES = [
  { type: 'signature', label: 'Signature', icon: FileSignature, w: 200, h: 60 },
  { type: 'initial',   label: 'Initial',   icon: Pen,           w: 100, h: 50 },
  { type: 'date',      label: 'Date',      icon: Calendar,      w: 130, h: 36 },
  { type: 'name',      label: 'Full Name', icon: User,          w: 180, h: 36 },
  { type: 'text',      label: 'Text',      icon: AlignLeft,     w: 200, h: 36 },
]

export default function SigningSetup() {
  const navigate = useNavigate()
  const location = useLocation()

  // PDF rendering
  const [pdfJsDoc, setPdfJsDoc]       = useState(null)
  const [totalPages, setTotalPages]    = useState(0)
  const [currentPage, setCurrentPage] = useState(1)
  const [zoom, setZoom]               = useState(1.0)
  const [filename, setFilename]       = useState('')
  const [fileId, setFileId]           = useState(null)
  const [envelopeId, setEnvelopeId]   = useState(null) // null = new, string = editing
  const [loadingPdf, setLoadingPdf]   = useState(false)
  const pageCanvasRef    = useRef(null)
  const canvasAreaRef    = useRef(null)
  const thumbnailRefs    = useRef({})
  const fileInputRef     = useRef(null)

  // Signers: [{ id, name, email, order, color }]
  const [signers, setSigners] = useState([])
  const [orderMode, setOrderMode] = useState('sequential') // sequential | parallel
  const [envelopeTitle, setEnvelopeTitle] = useState('')

  // Fields: [{ id, page, x, y, w, h, type, signerId, required }]
  const [fields, setFields] = useState([])
  const [selectedFieldId, setSelectedFieldId] = useState(null)
  const [activePlaceType, setActivePlaceType] = useState(null) // placing a field of this type

  // Drag state for field repositioning
  const dragRef = useRef({ active: false })

  // UI
  const [toast, setToast]           = useState(null)
  const [saving, setSaving]         = useState(false)
  const [dragOver, setDragOver]     = useState(false)
  const [pdfArrayBuffer, setPdfArrayBuffer] = useState(null)

  const showToast = useCallback((msg) => {
    setToast(msg)
    setTimeout(() => setToast(null), 3000)
  }, [])

  // ── Load PDF ────────────────────────────────────────────────
  const loadPDF = useCallback(async (file) => {
    if (!file || file.type !== 'application/pdf') {
      showToast('Please provide a valid PDF file')
      return
    }
    setLoadingPdf(true)
    try {
      const buf = await file.arrayBuffer()
      setPdfArrayBuffer(buf.slice())
      const doc = await pdfjsLib.getDocument({ data: buf }).promise
      setPdfJsDoc(doc)
      setTotalPages(doc.numPages)
      setCurrentPage(1)
      setFilename(file.name)
      if (!envelopeTitle) setEnvelopeTitle(file.name.replace(/\.pdf$/i, ''))
      showToast('PDF loaded')
    } catch (e) {
      showToast('Error loading PDF: ' + e.message)
    } finally {
      setLoadingPdf(false)
    }
  }, [showToast, envelopeTitle])

  const loadPDFFromUrl = useCallback(async (url, name) => {
    setLoadingPdf(true)
    try {
      const res = await fetch(url)
      const buf = await res.arrayBuffer()
      setPdfArrayBuffer(buf.slice())
      const doc = await pdfjsLib.getDocument({ data: buf }).promise
      setPdfJsDoc(doc)
      setTotalPages(doc.numPages)
      setCurrentPage(1)
      setFilename(name || 'document.pdf')
      if (!envelopeTitle) setEnvelopeTitle((name || 'document.pdf').replace(/\.pdf$/i, ''))
      showToast('PDF loaded')
    } catch (e) {
      showToast('Error loading PDF: ' + e.message)
    } finally {
      setLoadingPdf(false)
    }
  }, [showToast, envelopeTitle])

  // Auto-load from router state or session (same as PDFEditor)
  useEffect(() => {
    const state = location.state || {}

    // Load existing envelope if provided.
    if (state.envelopeId) {
      setEnvelopeId(state.envelopeId)
      api.getEnvelope(state.envelopeId).then(env => {
        setEnvelopeTitle(env.title || '')
        setOrderMode(env.order_mode || 'sequential')
        const loadedSigners = (env.signers || []).map((s, i) => ({
          id: s.id,
          name: s.name,
          email: s.email || '',
          order: s.order,
          color: SIGNER_COLORS[i % SIGNER_COLORS.length],
        }))
        setSigners(loadedSigners)
        const loadedFields = (env.fields || []).map(f => ({
          id: f.id,
          page: f.page,
          x: f.x, y: f.y, w: f.w, h: f.h,
          type: f.type,
          signerId: f.signer_id,
          required: f.required,
        }))
        setFields(loadedFields)
      }).catch(() => showToast('Could not load envelope'))
    }

    // Load PDF.
    if (state.fileId) setFileId(state.fileId)
    const pending = sessionStorage.getItem('pendingPDF')
    if (pending) {
      sessionStorage.removeItem('pendingPDF')
      try {
        const { name, url, data, fileId: fid } = JSON.parse(pending)
        if (fid) setFileId(fid)
        if (url) loadPDFFromUrl(url, name)
        else if (data) {
          const bytes = Uint8Array.from(atob(data), c => c.charCodeAt(0))
          loadPDF(new File([bytes], name, { type: 'application/pdf' }))
        }
      } catch {}
      return
    }
    const { localFileUrl, localFileName } = state
    if (localFileUrl) loadPDFFromUrl(localFileUrl, localFileName)
  }, [])

  // ── Render page ─────────────────────────────────────────────
  const renderPage = useCallback(async (pageNum, scale) => {
    if (!pdfJsDoc || !pageCanvasRef.current) return
    try {
      const page = await pdfJsDoc.getPage(pageNum)
      const viewport = page.getViewport({ scale })
      const canvas = pageCanvasRef.current
      canvas.width = viewport.width
      canvas.height = viewport.height
      await page.render({ canvasContext: canvas.getContext('2d'), viewport }).promise
    } catch {}
  }, [pdfJsDoc])

  useEffect(() => {
    if (pdfJsDoc) renderPage(currentPage, zoom)
  }, [pdfJsDoc, currentPage, zoom, renderPage])

  // Thumbnails
  useEffect(() => {
    if (!pdfJsDoc) return
    for (let i = 1; i <= totalPages; i++) renderThumbnail(i)
  }, [pdfJsDoc, totalPages])

  const renderThumbnail = async (pageNum) => {
    const canvas = thumbnailRefs.current[pageNum]
    if (!canvas || !pdfJsDoc) return
    try {
      const page = await pdfJsDoc.getPage(pageNum)
      const vp = page.getViewport({ scale: 0.22 })
      canvas.width = vp.width; canvas.height = vp.height
      await page.render({ canvasContext: canvas.getContext('2d'), viewport: vp }).promise
    } catch {}
  }

  // ── Esc cancels active place type ───────────────────────────
  useEffect(() => {
    const onKey = (e) => {
      if (e.key === 'Escape' && activePlaceType) setActivePlaceType(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [activePlaceType])

  // ── Signer management ────────────────────────────────────────
  const addSigner = () => {
    const newSigner = {
      id: genId(),
      name: `Signer ${signers.length + 1}`,
      email: '',
      order: signers.length + 1,
      color: SIGNER_COLORS[signers.length % SIGNER_COLORS.length],
    }
    setSigners(prev => [...prev, newSigner])
  }

  const updateSigner = (id, changes) => {
    setSigners(prev => prev.map(s => s.id === id ? { ...s, ...changes } : s))
  }

  const removeSigner = (id) => {
    setSigners(prev => prev.filter(s => s.id !== id))
    // Unassign fields from this signer
    setFields(prev => prev.map(f => f.signerId === id ? { ...f, signerId: null } : f))
  }

  // ── Field placement ──────────────────────────────────────────
  const handleCanvasClick = (e) => {
    if (!pdfJsDoc || !activePlaceType) return
    const canvas = pageCanvasRef.current
    if (!canvas) return
    const rect = canvas.getBoundingClientRect()
    const x = e.clientX - rect.left
    const y = e.clientY - rect.top
    const ft = FIELD_TYPES.find(f => f.type === activePlaceType)
    const newField = {
      id: genId(),
      page: currentPage,
      x: x - ft.w / 2,
      y: y - ft.h / 2,
      w: ft.w,
      h: ft.h,
      type: activePlaceType,
      signerId: signers.length === 1 ? signers[0].id : null,
      required: true,
    }
    setFields(prev => [...prev, newField])
    setSelectedFieldId(newField.id)
    setActivePlaceType(null)
  }

  const removeField = (id) => {
    setFields(prev => prev.filter(f => f.id !== id))
    if (selectedFieldId === id) setSelectedFieldId(null)
  }

  const updateField = (id, changes) => {
    setFields(prev => prev.map(f => f.id === id ? { ...f, ...changes } : f))
  }

  // Drag to reposition a field
  const onFieldMouseDown = (e, field) => {
    e.stopPropagation()
    if (activePlaceType) return
    setSelectedFieldId(field.id)
    const startX = e.clientX, startY = e.clientY
    const origX = field.x, origY = field.y
    let moved = false
    dragRef.current = { active: true }

    const onMove = (me) => {
      moved = true
      dragRef.current.moved = true
      updateField(field.id, {
        x: origX + (me.clientX - startX),
        y: origY + (me.clientY - startY),
      })
    }
    const onUp = () => {
      dragRef.current.active = false
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
      if (!moved) setSelectedFieldId(prev => prev === field.id ? prev : field.id)
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }

  // ── Save envelope ────────────────────────────────────────────
  const saveEnvelope = async () => {
    if (!envelopeTitle.trim()) { showToast('Please enter an envelope title'); return }
    if (signers.length === 0) { showToast('Add at least one signer'); return }
    setSaving(true)
    try {
      const payload = {
        source_file_id: fileId || '',
        title: envelopeTitle.trim(),
        order_mode: orderMode,
        status: 'draft',
        fields: fields.map(f => ({
          id: f.id,
          page: f.page,
          x: f.x, y: f.y, w: f.w, h: f.h,
          type: f.type,
          signer_id: f.signerId || '',
          required: f.required,
        })),
        signers: signers.map(s => ({
          id: s.id,
          name: s.name,
          email: s.email || '',
          order: s.order,
          status: 'pending',
        })),
      }
      let saved
      if (envelopeId) {
        saved = await api.updateEnvelope(envelopeId, payload)
      } else {
        saved = await api.createEnvelope(payload)
        setEnvelopeId(saved.id)
      }
      showToast('Envelope saved!')
    } catch (e) {
      showToast('Save error: ' + (e.message || 'Unknown error'))
    } finally {
      setSaving(false)
    }
  }

  // ── Helpers ──────────────────────────────────────────────────
  const currentPageFields = fields.filter(f => f.page === currentPage)
  const selectedField = selectedFieldId ? fields.find(f => f.id === selectedFieldId) : null
  const signerForField = (f) => signers.find(s => s.id === f?.signerId)

  // ── Render ───────────────────────────────────────────────────
  return (
    <div className="flex-1 flex flex-col bg-bg text-ink overflow-hidden">

      {/* TOP BAR — quiet, design-system Topbar */}
      <Topbar
        leading={
          <>
            <Tooltip label="Back">
              <IconButton size="sm" onClick={() => navigate(-1)}>
                <ArrowLeft size={15} />
              </IconButton>
            </Tooltip>
            <span aria-hidden className="h-5 w-px bg-line" />
            <div className="flex items-center gap-2 text-ink">
              <div className="w-7 h-7 rounded-md bg-accent text-white flex items-center justify-center">
                <FileSignature size={14} />
              </div>
              <span className="text-sm font-semibold tracking-tightish">Prepare to Sign</span>
            </div>
          </>
        }
        title={
          <input
            value={envelopeTitle}
            onChange={e => setEnvelopeTitle(e.target.value)}
            placeholder="Envelope title…"
            className={[
              'flex-1 max-w-md h-8 px-3 rounded-md',
              'bg-bg-elev2 border border-line text-ink text-sm tracking-tightish',
              'outline-none transition-colors duration-fast ease-out',
              'focus:border-accent focus:shadow-focus',
              'placeholder:text-ink-faint placeholder:font-serif placeholder:italic',
            ].join(' ')}
          />
        }
        actions={
          <>
            <Tooltip label="Open PDF">
              <Button
                variant="secondary"
                size="sm"
                onClick={() => fileInputRef.current?.click()}
              >
                <Upload size={13} /> Open PDF
              </Button>
            </Tooltip>
            <Button
              variant="primary"
              size="sm"
              onClick={saveEnvelope}
              disabled={saving || !pdfJsDoc}
            >
              <Save size={13} /> {saving ? 'Saving…' : 'Save envelope'}
            </Button>
          </>
        }
      />

      {/* FIELD TYPE TOOLBAR — chips as IconButtons w/ serif labels */}
      <div className="bg-paper border-b border-line h-12 flex items-center gap-1.5 px-3 flex-shrink-0">
        <span className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase mr-2">
          Place field
        </span>
        {FIELD_TYPES.map(({ type, label, icon: Icon }) => {
          const active = activePlaceType === type
          return (
            <Tooltip key={type} label={`Place ${label} field`}>
              <button
                type="button"
                onClick={() => setActivePlaceType(prev => prev === type ? null : type)}
                className={[
                  'inline-flex items-center gap-1.5 h-8 px-3 rounded-md',
                  'transition-[background,color,box-shadow] duration-fast ease-out',
                  'focus-visible:outline-none focus-visible:shadow-focus',
                  active
                    ? 'bg-accent-tint-2 text-accent-press border border-accent'
                    : 'bg-bg-elev2 border border-line text-ink-muted hover:bg-accent-tint hover:text-ink',
                ].join(' ')}
              >
                <Icon size={13} />
                <span className="font-serif text-sm italic tracking-tightish">{label}</span>
              </button>
            </Tooltip>
          )
        })}
        {activePlaceType && (
          <span className="ml-2 text-2xs text-warning font-serif italic">
            Click on the PDF to place a{' '}
            <b className="not-italic font-medium">
              {FIELD_TYPES.find(f => f.type === activePlaceType)?.label}
            </b>{' '}
            field — or press Esc to cancel
          </span>
        )}
        <div className="flex-1" />
        {/* Signing order Tabs — sequential / parallel */}
        <Tabs
          value={orderMode}
          onChange={setOrderMode}
          items={[
            { value: 'sequential', label: 'Sequential' },
            { value: 'parallel',   label: 'Parallel' },
          ]}
        />
      </div>

      {/* WORKSPACE */}
      <div className="flex-1 flex overflow-hidden">

        {/* LEFT: page thumbnails */}
        <aside className="w-44 bg-bg-elev2 border-r border-line flex flex-col flex-shrink-0 overflow-hidden">
          <div className="px-3 py-2.5 border-b border-line">
            <span className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
              Pages
            </span>
          </div>
          <div className="flex-1 overflow-y-auto p-2 flex flex-col gap-1.5">
            {!pdfJsDoc ? (
              <p className="text-2xs text-ink-faint text-center py-6 font-serif italic">
                Open a PDF to start
              </p>
            ) : (
              Array.from({ length: totalPages }, (_, i) => i + 1).map(n => {
                const pageFieldCount = fields.filter(f => f.page === n).length
                const isActive = currentPage === n
                return (
                  <div
                    key={n}
                    onClick={() => setCurrentPage(n)}
                    className={[
                      'relative flex flex-col items-center gap-1 p-1.5 rounded-md cursor-pointer',
                      'transition-colors duration-fast ease-out',
                      isActive
                        ? 'bg-accent-tint'
                        : 'hover:bg-paper',
                    ].join(' ')}
                  >
                    {/* Accent left-rail for selected page */}
                    <span
                      aria-hidden
                      className={[
                        'absolute left-0 top-1 bottom-1 w-[2px] rounded-r-full transition-colors duration-fast',
                        isActive ? 'bg-accent' : 'bg-transparent',
                      ].join(' ')}
                    />
                    <div
                      className={[
                        'relative w-full rounded-xs overflow-hidden bg-paper border',
                        'shadow-e1 transition-colors duration-fast',
                        isActive ? 'border-accent' : 'border-line',
                      ].join(' ')}
                    >
                      <canvas
                        ref={el => { if (el) thumbnailRefs.current[n] = el }}
                        className="block w-full"
                      />
                      {pageFieldCount > 0 && (
                        <span className="absolute top-1 right-1 bg-accent text-white rounded-pill text-2xs font-semibold tracking-tightish px-1.5 py-0">
                          {pageFieldCount}
                        </span>
                      )}
                    </div>
                    <span
                      className={[
                        'text-2xs tracking-tightish',
                        isActive ? 'text-accent-press font-medium' : 'text-ink-faint',
                      ].join(' ')}
                    >
                      {n}
                    </span>
                  </div>
                )
              })
            )}
          </div>
        </aside>

        {/* CENTER: PDF canvas */}
        <div
          ref={canvasAreaRef}
          className="flex-1 overflow-auto bg-bg paper-grain flex flex-col items-center px-8 py-8 relative"
          onDragOver={e => { e.preventDefault(); setDragOver(true) }}
          onDragLeave={() => setDragOver(false)}
          onDrop={e => { e.preventDefault(); setDragOver(false); const f = e.dataTransfer.files[0]; if (f) loadPDF(f) }}
        >
          {!pdfJsDoc ? (
            <div
              onClick={() => fileInputRef.current?.click()}
              className={[
                'flex flex-col items-center justify-center gap-4 w-full max-w-lg my-auto',
                'border-2 border-dashed rounded-lg bg-paper paper-grain py-14 px-10 cursor-pointer',
                'transition-colors duration-fast ease-out',
                dragOver
                  ? 'border-accent bg-accent-tint'
                  : 'border-line-strong hover:border-accent hover:bg-accent-tint',
              ].join(' ')}
            >
              <div className="w-14 h-14 rounded-md bg-accent-tint flex items-center justify-center">
                <Upload size={24} className="text-accent" />
              </div>
              <div className="text-center">
                <h2 className="font-serif text-xl text-ink leading-tight">
                  Open a PDF to set up signing
                </h2>
                <p className="text-sm text-ink-muted mt-1 font-serif italic">
                  Drag & drop here, or click to browse.
                </p>
              </div>
            </div>
          ) : (
            <>
              <div
                onClick={handleCanvasClick}
                className="relative shadow-e2 rounded-sm overflow-hidden bg-paper animate-fade-in"
                style={{ cursor: activePlaceType ? 'crosshair' : 'default' }}
              >
                {loadingPdf && (
                  <div className="absolute inset-0 bg-paper/70 flex items-center justify-center z-50">
                    <div className="w-7 h-7 border-2 border-accent border-t-transparent rounded-full animate-spin" />
                  </div>
                )}
                <canvas ref={pageCanvasRef} className="block" />

                {/* Field overlay — single accent palette; signer stripe carries color */}
                {currentPageFields.map(field => {
                  const signer = signerForField(field)
                  const stripeColor = signer?.color || 'var(--ink-faint)'
                  const isSelected = selectedFieldId === field.id
                  const ft = FIELD_TYPES.find(f => f.type === field.type)
                  return (
                    <div
                      key={field.id}
                      onMouseDown={e => onFieldMouseDown(e, field)}
                      className={[
                        'absolute flex flex-col items-center justify-center gap-1',
                        'rounded-sm cursor-move select-none',
                        'transition-[box-shadow,background-color] duration-fast ease-out',
                        isSelected
                          ? 'bg-accent-tint-2 border-2 border-accent shadow-e1'
                          : 'bg-accent-tint border-2 border-accent/60 hover:border-accent',
                      ].join(' ')}
                      style={{
                        left: field.x, top: field.y,
                        width: field.w, height: field.h,
                        zIndex: isSelected ? 10 : 5,
                      }}
                    >
                      {/* Signer color stripe — the only place colour varies */}
                      {signer && (
                        <span
                          aria-hidden
                          className="absolute left-0 top-0 bottom-0 w-[3px]"
                          style={{ background: stripeColor }}
                        />
                      )}
                      <span className="text-2xs font-semibold tracking-eyebrow uppercase text-accent-press select-none">
                        {ft?.label}
                      </span>
                      {signer && (
                        <span
                          className="text-2xs font-serif italic tracking-tightish"
                          style={{ color: stripeColor }}
                        >
                          {signer.name}
                        </span>
                      )}
                      {!field.required && (
                        <span className="text-2xs text-ink-faint font-serif italic">optional</span>
                      )}

                      {/* Delete chip */}
                      {isSelected && (
                        <button
                          onMouseDown={e => { e.stopPropagation(); removeField(field.id) }}
                          aria-label="Delete field"
                          className="absolute -top-2 -right-2 w-5 h-5 rounded-full bg-danger text-white border-2 border-paper flex items-center justify-center shadow-e1 hover:bg-danger/90 z-20"
                        >
                          <X size={11} />
                        </button>
                      )}
                    </div>
                  )
                })}
              </div>

              {/* Page nav — quiet */}
              {totalPages > 1 && (
                <div className="flex items-center gap-3 mt-5">
                  <IconButton
                    size="sm"
                    onClick={() => { if (currentPage > 1) setCurrentPage(p => p - 1) }}
                    disabled={currentPage <= 1}
                  >
                    <ChevronLeft size={15} />
                  </IconButton>
                  <span className="text-xs text-ink-muted tracking-tightish bg-paper border border-line rounded-md px-3 py-1">
                    Page {currentPage} of {totalPages}
                  </span>
                  <IconButton
                    size="sm"
                    onClick={() => { if (currentPage < totalPages) setCurrentPage(p => p + 1) }}
                    disabled={currentPage >= totalPages}
                  >
                    <ChevronRight size={15} />
                  </IconButton>
                </div>
              )}
            </>
          )}
        </div>

        {/* RIGHT RAIL — signers + field properties */}
        <aside className="w-72 bg-bg-elev2 border-l border-line flex flex-col flex-shrink-0 overflow-hidden">

          {/* Signers section */}
          <div className="border-b border-line">
            <div className="flex items-center justify-between px-3 py-2.5 border-b border-line">
              <span className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
                Signers
              </span>
              <Button variant="ghost" size="sm" onClick={addSigner}>
                <Plus size={12} /> Add
              </Button>
            </div>
            <div className="px-3 py-3 flex flex-col gap-2 max-h-72 overflow-y-auto">
              {signers.length === 0 ? (
                <p className="text-2xs text-ink-faint font-serif italic">
                  No signers yet — click Add.
                </p>
              ) : signers.map((signer) => (
                <Card key={signer.id} className="relative overflow-hidden">
                  {/* Colour stripe — the per-signer accent */}
                  <span
                    aria-hidden
                    className="absolute left-0 top-0 bottom-0 w-1"
                    style={{ background: signer.color }}
                  />
                  <div className="pl-3 pr-2 py-2.5 space-y-1.5">
                    <div className="flex items-center gap-2">
                      <input
                        value={signer.name}
                        onChange={e => updateSigner(signer.id, { name: e.target.value })}
                        placeholder="Signer name"
                        className="flex-1 bg-transparent border-none outline-none text-sm font-serif italic text-ink placeholder:text-ink-faint tracking-tightish"
                      />
                      <Tooltip label="Remove signer">
                        <IconButton
                          size="sm"
                          onClick={() => removeSigner(signer.id)}
                          className="hover:bg-danger-bg hover:text-danger"
                        >
                          <X size={12} />
                        </IconButton>
                      </Tooltip>
                    </div>
                    <input
                      value={signer.email}
                      onChange={e => updateSigner(signer.id, { email: e.target.value })}
                      placeholder="email@example.com"
                      type="email"
                      className="w-full bg-paper border border-line rounded-sm px-2 py-1 text-2xs font-serif italic text-ink-muted placeholder:text-ink-faint outline-none focus:border-accent focus:shadow-focus transition-colors"
                    />
                    {orderMode === 'sequential' && (
                      <div className="flex items-center gap-1.5">
                        <span className="text-2xs text-ink-faint tracking-tightish">
                          Order
                        </span>
                        <input
                          type="number"
                          min={1}
                          max={signers.length}
                          value={signer.order}
                          onChange={e => updateSigner(signer.id, { order: parseInt(e.target.value) || 1 })}
                          className="w-12 bg-paper border border-line rounded-sm px-1.5 py-0.5 text-2xs text-ink text-center outline-none focus:border-accent focus:shadow-focus"
                        />
                      </div>
                    )}
                  </div>
                </Card>
              ))}
            </div>
          </div>

          {/* Field properties / list */}
          <div className="flex-1 overflow-y-auto px-3 py-3">
            <div className="mb-2">
              <span className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
                {selectedField ? 'Field properties' : 'Fields'}
              </span>
            </div>

            {selectedField ? (
              <div className="flex flex-col gap-3">
                {/* Type badge — single accent */}
                <div className="flex items-center gap-2 px-3 py-2 rounded-md bg-accent-tint border border-accent/40">
                  {(() => {
                    const ft = FIELD_TYPES.find(f => f.type === selectedField.type)
                    const Icon = ft?.icon
                    return Icon ? <Icon size={13} className="text-accent" /> : null
                  })()}
                  <span className="text-xs font-medium text-accent-press tracking-tightish">
                    {FIELD_TYPES.find(f => f.type === selectedField.type)?.label}
                    <span className="text-ink-faint font-normal"> · Page {selectedField.page}</span>
                  </span>
                </div>

                {/* Assign signer */}
                <div>
                  <label className="block text-2xs text-ink-muted font-medium mb-1 tracking-tightish">
                    Assigned signer
                  </label>
                  <select
                    value={selectedField.signerId || ''}
                    onChange={e => updateField(selectedField.id, { signerId: e.target.value || null })}
                    className="w-full bg-paper border border-line rounded-sm px-2 py-1.5 text-xs text-ink outline-none focus:border-accent focus:shadow-focus transition-colors"
                  >
                    <option value="">— Unassigned —</option>
                    {signers.map(s => (
                      <option key={s.id} value={s.id}>{s.name}</option>
                    ))}
                  </select>
                </div>

                {/* Required toggle — clear control */}
                <button
                  type="button"
                  onClick={() => updateField(selectedField.id, { required: !selectedField.required })}
                  className="flex items-center gap-2 text-left group"
                >
                  <span
                    className={[
                      'w-4 h-4 rounded-xs flex items-center justify-center flex-shrink-0 border-2',
                      'transition-colors duration-fast ease-out',
                      selectedField.required
                        ? 'bg-accent border-accent'
                        : 'bg-paper border-line-strong group-hover:border-accent',
                    ].join(' ')}
                  >
                    {selectedField.required && <CheckSquare size={10} className="text-white" />}
                  </span>
                  <span
                    className={[
                      'text-xs tracking-tightish',
                      selectedField.required ? 'text-ink' : 'text-ink-muted',
                    ].join(' ')}
                  >
                    Required
                  </span>
                </button>

                {/* Position / size */}
                <div className="grid grid-cols-2 gap-2">
                  {[['x', 'X'], ['y', 'Y'], ['w', 'W'], ['h', 'H']].map(([key, label]) => (
                    <div key={key}>
                      <label className="block text-2xs text-ink-faint mb-1 tracking-tightish">
                        {label} (px)
                      </label>
                      <input
                        type="number"
                        value={Math.round(selectedField[key])}
                        onChange={e => updateField(selectedField.id, { [key]: parseFloat(e.target.value) || 0 })}
                        className="w-full bg-paper border border-line rounded-sm px-2 py-1 text-2xs text-ink outline-none focus:border-accent focus:shadow-focus"
                      />
                    </div>
                  ))}
                </div>

                <Button
                  variant="destructive"
                  size="sm"
                  fullWidth
                  onClick={() => removeField(selectedField.id)}
                >
                  <Trash2 size={12} /> Delete field
                </Button>
              </div>
            ) : (
              <div>
                {fields.length === 0 ? (
                  <p className="text-2xs text-ink-faint font-serif italic leading-snug">
                    No fields yet.<br />
                    Use the toolbar above to place Signature, Date, Name, or Text fields onto the PDF.
                  </p>
                ) : (
                  <div className="flex flex-col gap-1">
                    {fields.map(f => {
                      const signer = signerForField(f)
                      const stripeColor = signer?.color || 'var(--ink-faint)'
                      return (
                        <button
                          key={f.id}
                          type="button"
                          onClick={() => { setCurrentPage(f.page); setSelectedFieldId(f.id) }}
                          className="relative flex items-center gap-2 pl-3 pr-2 py-1.5 rounded-sm bg-paper border border-line hover:border-line-strong hover:bg-bg-elev2 transition-colors"
                        >
                          <span
                            aria-hidden
                            className="absolute left-0 top-1 bottom-1 w-[2px] rounded-r-full"
                            style={{ background: stripeColor }}
                          />
                          <span className="text-xs text-ink flex-1 text-left tracking-tightish">
                            {FIELD_TYPES.find(ft => ft.type === f.type)?.label}
                            <span className="text-ink-faint"> · p.{f.page}</span>
                          </span>
                          {signer && (
                            <span
                              className="text-2xs font-serif italic"
                              style={{ color: stripeColor }}
                            >
                              {signer.name}
                            </span>
                          )}
                          {!f.required && (
                            <span className="text-2xs text-ink-faint font-serif italic">opt</span>
                          )}
                        </button>
                      )
                    })}
                  </div>
                )}

                {/* Summary */}
                {fields.length > 0 && (
                  <div className="mt-4 px-3 py-2.5 rounded-md bg-paper border border-line">
                    <div className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase mb-1.5">
                      Summary
                    </div>
                    {signers.map(s => {
                      const count = fields.filter(f => f.signerId === s.id).length
                      return (
                        <div
                          key={s.id}
                          className="flex justify-between text-xs text-ink-muted mb-0.5 tracking-tightish"
                        >
                          <span style={{ color: s.color }} className="font-serif italic">
                            {s.name}
                          </span>
                          <span>{count} field{count !== 1 ? 's' : ''}</span>
                        </div>
                      )
                    })}
                    {fields.filter(f => !f.signerId).length > 0 && (
                      <div className="flex justify-between text-xs text-danger mt-1 tracking-tightish">
                        <span>Unassigned</span>
                        <span>{fields.filter(f => !f.signerId).length}</span>
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
        </aside>
      </div>

      {/* Toast — quiet inline pill */}
      {toast && (
        <div className="fixed bottom-10 left-1/2 -translate-x-1/2 z-50 bg-ink text-paper px-4 py-2 rounded-md shadow-e2 text-xs tracking-tightish animate-fade-in">
          {toast}
        </div>
      )}

      <input
        ref={fileInputRef}
        type="file"
        accept=".pdf"
        className="hidden"
        onChange={e => { if (e.target.files[0]) loadPDF(e.target.files[0]); e.target.value = '' }}
      />
    </div>
  )
}
