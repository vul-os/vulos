import { useEffect, useRef, useState, useCallback } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import * as pdfjsLib from 'pdfjs-dist'
import { PDFDocument, rgb, StandardFonts, degrees } from 'pdf-lib'
import SignaturePad from 'signature_pad'
import {
  ArrowLeft, Download, ZoomIn, ZoomOut, Maximize2,
  MousePointer2, Type, PenLine, Pencil, LayoutList,
  SlidersHorizontal, Plus, X, Trash2, Bold, Italic,
  Underline as UnderlineIcon, ChevronLeft, ChevronRight,
  Upload, RotateCw, FilePlus, Trash, FileSignature,
} from 'lucide-react'
import { Button, IconButton, Tabs, Topbar, Tooltip } from '../../components/ui'

/*
 * PDFEditor — single-user PDF editor with annotate / sign / page operations.
 *
 * Aesthetic direction:
 *   - Quiet design-system Topbar (Save status + meta), "Prepare to Sign" as the
 *     single primary affordance.
 *   - Warm paper canvas, oat sidebars.
 *   - Page thumbnails in the LEFT sidebar use the design-system Sidebar
 *     active-rail (accent left rail) for the selected page.
 *   - Field/annotation overlays use the SINGLE accent palette — no rainbow.
 */

// Set worker
pdfjsLib.GlobalWorkerOptions.workerSrc = new URL(
  'pdfjs-dist/build/pdf.worker.min.mjs',
  import.meta.url
).href

function genId() {
  return Math.random().toString(36).slice(2, 11)
}

function hexToRgb01(hex) {
  const r = parseInt(hex.slice(1, 3), 16) / 255
  const g = parseInt(hex.slice(3, 5), 16) / 255
  const b = parseInt(hex.slice(5, 7), 16) / 255
  return { r, g, b }
}

const TOOLS = {
  SELECT: 'select',
  TEXT: 'text',
  SIGNATURE: 'signature',
  DRAW: 'draw',
}

const CURSORS = {
  select: 'default',
  text: 'text',
  signature: 'crosshair',
  draw: 'crosshair',
}

// Annotation outline colour — warm ink, not bright blue.
const ANNOT_INK = '#1a1916'

export default function PDFEditor() {
  const navigate = useNavigate()
  const location = useLocation()

  // PDF state
  const [pdfArrayBuffer, setPdfArrayBuffer] = useState(null)
  const [pdfJsDoc, setPdfJsDoc] = useState(null)
  const [totalPages, setTotalPages] = useState(0)
  const [currentPage, setCurrentPage] = useState(1)
  const [zoom, setZoom] = useState(1.0)
  const [filename, setFilename] = useState('')
  const [loadingPdf, setLoadingPdf] = useState(false)

  // Page operations state
  const [pageOrder, setPageOrder] = useState([])
  const [pageRotations, setPageRotations] = useState({})
  const blankPageBuffers = useRef({})
  const [thumbDragSrc, setThumbDragSrc] = useState(null)
  const insertFileInputRef = useRef(null)

  // Tool state
  const [activeTool, setActiveTool] = useState(TOOLS.SELECT)
  const [textDefaults, setTextDefaults] = useState({
    fontSize: 14,
    fontFamily: 'Helvetica',
    color: ANNOT_INK,
    bold: false,
    italic: false,
    underline: false,
  })

  // Annotations: { [pageNum]: [annotation, ...] }
  const [annotations, setAnnotations] = useState({})
  const [selectedId, setSelectedId] = useState(null)

  // Signatures
  const [savedSigs, setSavedSigs] = useState(() => {
    try { return JSON.parse(localStorage.getItem('vulos_pdf_sigs') || '[]') }
    catch { return [] }
  })

  // UI state
  const [sidebarOpen, setSidebarOpen] = useState(true)
  const [panelOpen, setPanelOpen] = useState(true)
  const [sigModalOpen, setSigModalOpen] = useState(false)
  const [sigTab, setSigTab] = useState('draw')
  const [sigFont, setSigFont] = useState('Dancing Script')
  const [typedName, setTypedName] = useState('')
  const [saveToLib, setSaveToLib] = useState(true)
  const [toast, setToast] = useState(null)
  const [dragOver, setDragOver] = useState(false)

  // Draw state
  const [isDrawing, setIsDrawing] = useState(false)
  const drawPathsRef = useRef({})
  const currentDrawPoints = useRef([])

  // Refs
  const pageCanvasRef = useRef(null)
  const drawCanvasRef = useRef(null)
  const annotLayerRef = useRef(null)
  const canvasAreaRef = useRef(null)
  const sigCanvasRef = useRef(null)
  const sigPadRef = useRef(null)
  const pendingSigPos = useRef(null)
  const dragState = useRef({ active: false })
  const thumbnailRefs = useRef({})
  const fileInputRef = useRef(null)

  // ─── Toast ───────────────────────────────────────────────
  const showToast = useCallback((msg) => {
    setToast(msg)
    setTimeout(() => setToast(null), 2800)
  }, [])

  // ─── Persist signatures ───────────────────────────────────
  useEffect(() => {
    localStorage.setItem('vulos_pdf_sigs', JSON.stringify(savedSigs))
  }, [savedSigs])

  // ─── Load PDF ─────────────────────────────────────────────
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
      setAnnotations({})
      setSelectedId(null)
      setFilename(file.name)
      setPageOrder(Array.from({ length: doc.numPages }, (_, i) => i + 1))
      setPageRotations({})
      blankPageBuffers.current = {}
      showToast('PDF loaded')
    } catch (e) {
      showToast('Error loading PDF: ' + e.message)
    } finally {
      setLoadingPdf(false)
    }
  }, [showToast])

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
      setAnnotations({})
      setSelectedId(null)
      setFilename(name || 'document.pdf')
      setPageOrder(Array.from({ length: doc.numPages }, (_, i) => i + 1))
      setPageRotations({})
      blankPageBuffers.current = {}
      showToast('PDF loaded')
    } catch (e) {
      showToast('Error loading PDF: ' + e.message)
    } finally {
      setLoadingPdf(false)
    }
  }, [showToast])

  // Auto-load from sessionStorage (set by importFile.js) or router state
  useEffect(() => {
    const pending = sessionStorage.getItem('pendingPDF')
    if (pending) {
      sessionStorage.removeItem('pendingPDF')
      try {
        const { name, url, data } = JSON.parse(pending)
        if (url) {
          loadPDFFromUrl(url, name)
        } else if (data) {
          const bytes = Uint8Array.from(atob(data), c => c.charCodeAt(0))
          const file = new File([bytes], name, { type: 'application/pdf' })
          loadPDF(file)
        }
      } catch (e) {
        console.error('Failed to load pending PDF', e)
      }
      return
    }
    const { localFileUrl, localFileName } = location.state || {}
    if (localFileUrl) loadPDFFromUrl(localFileUrl, localFileName)
  }, [])

  const handleFileInput = (e) => {
    if (e.target.files[0]) loadPDF(e.target.files[0])
  }

  const handleDrop = (e) => {
    e.preventDefault()
    setDragOver(false)
    const file = e.dataTransfer.files[0]
    if (file) loadPDF(file)
  }

  // ─── Render Page ──────────────────────────────────────────
  const renderPage = useCallback(async (displaySlot, scale) => {
    if (!pdfJsDoc) return
    const canvas = pageCanvasRef.current
    const drawCanvas = drawCanvasRef.current
    if (!canvas) return

    const origPageNum = pageOrder.length > 0 ? pageOrder[displaySlot - 1] : displaySlot
    if (!origPageNum || origPageNum < 1) return

    const page = await pdfJsDoc.getPage(origPageNum)
    const extraRot = pageRotations[displaySlot] || 0
    const viewport = page.getViewport({ scale, rotation: (page.rotate + extraRot) % 360 })

    canvas.width = viewport.width
    canvas.height = viewport.height
    if (drawCanvas) {
      drawCanvas.width = viewport.width
      drawCanvas.height = viewport.height
    }

    const ctx = canvas.getContext('2d')
    await page.render({ canvasContext: ctx, viewport }).promise

    redrawPaths(displaySlot, drawCanvas)
  }, [pdfJsDoc, pageOrder, pageRotations])

  useEffect(() => {
    if (pdfJsDoc) renderPage(currentPage, zoom)
  }, [pdfJsDoc, currentPage, zoom, renderPage])

  useEffect(() => {
    if (!pdfJsDoc || pageOrder.length === 0) return
    for (let i = 1; i <= pageOrder.length; i++) {
      renderThumbnail(i)
    }
  }, [pdfJsDoc, pageOrder, pageRotations])

  const renderThumbnail = async (displaySlot) => {
    const canvas = thumbnailRefs.current[displaySlot]
    if (!canvas || !pdfJsDoc) return
    const origPageNum = pageOrder[displaySlot - 1]
    if (!origPageNum || origPageNum < 1) {
      canvas.width = 100
      canvas.height = 130
      const ctx = canvas.getContext('2d')
      ctx.fillStyle = '#ffffff'
      ctx.fillRect(0, 0, 100, 130)
      ctx.strokeStyle = '#ece5da'
      ctx.strokeRect(0, 0, 100, 130)
      return
    }
    try {
      const page = await pdfJsDoc.getPage(origPageNum)
      const extraRot = pageRotations[displaySlot] || 0
      const vp = page.getViewport({ scale: 0.22, rotation: (page.rotate + extraRot) % 360 })
      canvas.width = vp.width
      canvas.height = vp.height
      const ctx = canvas.getContext('2d')
      await page.render({ canvasContext: ctx, viewport: vp }).promise
    } catch {}
  }

  // ─── Page operations ─────────────────────────────────────

  const remapPageData = (oldOrder, newOrder) => {
    const slotMap = {}
    oldOrder.forEach((origPage, oldIdx) => {
      const newIdx = newOrder.indexOf(origPage)
      if (newIdx !== -1) {
        slotMap[oldIdx + 1] = newIdx + 1
      }
    })

    setAnnotations(prev => {
      const next = {}
      for (const [slot, anns] of Object.entries(prev)) {
        const newSlot = slotMap[parseInt(slot)]
        if (newSlot != null) {
          next[newSlot] = anns.map(a => ({ ...a, pageIndex: newSlot }))
        }
      }
      return next
    })

    setPageRotations(prev => {
      const next = {}
      for (const [slot, rot] of Object.entries(prev)) {
        const newSlot = slotMap[parseInt(slot)]
        if (newSlot != null) next[newSlot] = rot
      }
      return next
    })

    const oldPaths = { ...drawPathsRef.current }
    const newPaths = {}
    for (const [slot, paths] of Object.entries(oldPaths)) {
      const newSlot = slotMap[parseInt(slot)]
      if (newSlot != null) newPaths[newSlot] = paths
    }
    drawPathsRef.current = newPaths
  }

  const reorderPages = (fromSlot, toSlot) => {
    if (fromSlot === toSlot || !pdfJsDoc) return
    const oldOrder = [...pageOrder]
    const newOrder = [...pageOrder]
    const [moved] = newOrder.splice(fromSlot - 1, 1)
    newOrder.splice(toSlot - 1, 0, moved)

    remapPageData(oldOrder, newOrder)
    setPageOrder(newOrder)
    setTotalPages(newOrder.length)
    setCurrentPage(c => {
      if (c === fromSlot) return toSlot
      return c
    })
  }

  const deletePageSlot = (displaySlot) => {
    if (!pdfJsDoc || pageOrder.length <= 1) {
      showToast('Cannot delete the only page')
      return
    }
    const oldOrder = [...pageOrder]
    const newOrder = oldOrder.filter((_, i) => i !== displaySlot - 1)
    const slotMap = {}
    oldOrder.forEach((origPage, oldIdx) => {
      if (oldIdx === displaySlot - 1) return
      const newIdx = newOrder.indexOf(origPage)
      if (newIdx !== -1) slotMap[oldIdx + 1] = newIdx + 1
    })

    setAnnotations(prev => {
      const next = {}
      for (const [slot, anns] of Object.entries(prev)) {
        const newSlot = slotMap[parseInt(slot)]
        if (newSlot != null) next[newSlot] = anns.map(a => ({ ...a, pageIndex: newSlot }))
      }
      return next
    })

    setPageRotations(prev => {
      const next = {}
      for (const [slot, rot] of Object.entries(prev)) {
        const newSlot = slotMap[parseInt(slot)]
        if (newSlot != null) next[newSlot] = rot
      }
      return next
    })

    const oldPaths = { ...drawPathsRef.current }
    const newPaths = {}
    for (const [slot, paths] of Object.entries(oldPaths)) {
      const newSlot = slotMap[parseInt(slot)]
      if (newSlot != null) newPaths[newSlot] = paths
    }
    drawPathsRef.current = newPaths

    setPageOrder(newOrder)
    setTotalPages(newOrder.length)
    setCurrentPage(c => {
      if (c === displaySlot) return Math.min(displaySlot, newOrder.length)
      if (c > displaySlot) return c - 1
      return c
    })
    showToast('Page deleted')
  }

  const rotatePage = (displaySlot, by = 90) => {
    if (!pdfJsDoc) return
    setPageRotations(prev => ({
      ...prev,
      [displaySlot]: ((prev[displaySlot] || 0) + by) % 360,
    }))
  }

  const insertBlankPage = async (afterSlot) => {
    if (!pdfJsDoc) return
    const blankId = -(Date.now())
    const blankDoc = await PDFDocument.create()
    blankDoc.addPage([612, 792])
    const blankBytes = await blankDoc.save()
    blankPageBuffers.current[blankId] = blankBytes.buffer.slice(0)

    const newOrder = [...pageOrder]
    newOrder.splice(afterSlot, 0, blankId)

    const oldOrder = [...pageOrder]
    const oldLen = oldOrder.length
    const slotMap = {}
    for (let i = 0; i < oldLen; i++) {
      const oldSlot = i + 1
      const newSlot = oldSlot <= afterSlot ? oldSlot : oldSlot + 1
      slotMap[oldSlot] = newSlot
    }

    setAnnotations(prev => {
      const next = {}
      for (const [slot, anns] of Object.entries(prev)) {
        const newSlot = slotMap[parseInt(slot)]
        if (newSlot != null) next[newSlot] = anns.map(a => ({ ...a, pageIndex: newSlot }))
      }
      return next
    })

    setPageRotations(prev => {
      const next = {}
      for (const [slot, rot] of Object.entries(prev)) {
        const newSlot = slotMap[parseInt(slot)]
        if (newSlot != null) next[newSlot] = rot
      }
      return next
    })

    const oldPaths = { ...drawPathsRef.current }
    const newPaths = {}
    for (const [slot, paths] of Object.entries(oldPaths)) {
      const newSlot = slotMap[parseInt(slot)]
      if (newSlot != null) newPaths[newSlot] = paths
    }
    drawPathsRef.current = newPaths

    setPageOrder(newOrder)
    setTotalPages(newOrder.length)
    setCurrentPage(afterSlot + 1)
    showToast('Blank page inserted')
  }

  const insertPDFPage = async (file, afterSlot) => {
    if (!file || file.type !== 'application/pdf') return
    try {
      const buf = await file.arrayBuffer()
      const importedDoc = await pdfjsLib.getDocument({ data: buf.slice() }).promise
      if (importedDoc.numPages < 1) return

      const baseDoc = await PDFDocument.load(pdfArrayBuffer)
      const srcDoc = await PDFDocument.load(buf)
      const [importedPage] = await baseDoc.copyPages(srcDoc, [0])

      const newOrigPageNum = baseDoc.getPageCount() + 1
      baseDoc.addPage(importedPage)
      const newBuf = await baseDoc.save()
      const newBufCopy = newBuf.buffer.slice(0)

      const newDoc = await pdfjsLib.getDocument({ data: newBuf.slice() }).promise

      const oldOrder = [...pageOrder]
      const newOrder = [...pageOrder]
      newOrder.splice(afterSlot, 0, newOrigPageNum)

      const slotMap = {}
      for (let i = 0; i < oldOrder.length; i++) {
        const oldSlot = i + 1
        const newSlot = oldSlot <= afterSlot ? oldSlot : oldSlot + 1
        slotMap[oldSlot] = newSlot
      }

      setAnnotations(prev => {
        const next = {}
        for (const [slot, anns] of Object.entries(prev)) {
          const ns = slotMap[parseInt(slot)]
          if (ns != null) next[ns] = anns.map(a => ({ ...a, pageIndex: ns }))
        }
        return next
      })

      setPageRotations(prev => {
        const next = {}
        for (const [slot, rot] of Object.entries(prev)) {
          const ns = slotMap[parseInt(slot)]
          if (ns != null) next[ns] = rot
        }
        return next
      })

      const oldPaths = { ...drawPathsRef.current }
      const newPaths = {}
      for (const [slot, paths] of Object.entries(oldPaths)) {
        const ns = slotMap[parseInt(slot)]
        if (ns != null) newPaths[ns] = paths
      }
      drawPathsRef.current = newPaths

      setPdfArrayBuffer(newBufCopy)
      setPdfJsDoc(newDoc)
      setPageOrder(newOrder)
      setTotalPages(newOrder.length)
      setCurrentPage(afterSlot + 1)
      showToast('Page inserted from PDF')
    } catch (e) {
      showToast('Error inserting page: ' + e.message)
    }
  }

  // ─── Draw paths ───────────────────────────────────────────
  const redrawPaths = (pageNum, canvas) => {
    const c = canvas || drawCanvasRef.current
    if (!c) return
    const ctx = c.getContext('2d')
    ctx.clearRect(0, 0, c.width, c.height)
    const paths = drawPathsRef.current[pageNum] || []
    paths.forEach(({ points, color, size }) => {
      if (points.length < 2) return
      ctx.beginPath()
      ctx.strokeStyle = color
      ctx.lineWidth = size
      ctx.lineCap = 'round'
      ctx.lineJoin = 'round'
      ctx.moveTo(points[0].x, points[0].y)
      for (let i = 1; i < points.length; i++) ctx.lineTo(points[i].x, points[i].y)
      ctx.stroke()
    })
  }

  const getCanvasPoint = (e, canvas) => {
    const rect = canvas.getBoundingClientRect()
    return { x: e.clientX - rect.left, y: e.clientY - rect.top }
  }

  const onDrawStart = (e) => {
    if (activeTool !== TOOLS.DRAW) return
    setIsDrawing(true)
    const pt = getCanvasPoint(e, drawCanvasRef.current)
    currentDrawPoints.current = [pt]
    e.preventDefault()
  }

  const onDrawMove = (e) => {
    if (!isDrawing || activeTool !== TOOLS.DRAW) return
    const pt = getCanvasPoint(e, drawCanvasRef.current)
    currentDrawPoints.current.push(pt)
    const ctx = drawCanvasRef.current.getContext('2d')
    ctx.clearRect(0, 0, drawCanvasRef.current.width, drawCanvasRef.current.height)
    ;(drawPathsRef.current[currentPage] || []).forEach(({ points, color, size }) => {
      if (points.length < 2) return
      ctx.beginPath(); ctx.strokeStyle = color; ctx.lineWidth = size
      ctx.lineCap = 'round'; ctx.lineJoin = 'round'
      ctx.moveTo(points[0].x, points[0].y)
      points.slice(1).forEach(p => ctx.lineTo(p.x, p.y))
      ctx.stroke()
    })
    const pts = currentDrawPoints.current
    if (pts.length > 1) {
      ctx.beginPath()
      ctx.strokeStyle = textDefaults.color
      ctx.lineWidth = 2.5
      ctx.lineCap = 'round'; ctx.lineJoin = 'round'
      ctx.moveTo(pts[0].x, pts[0].y)
      pts.slice(1).forEach(p => ctx.lineTo(p.x, p.y))
      ctx.stroke()
    }
    e.preventDefault()
  }

  const onDrawEnd = () => {
    if (!isDrawing) return
    setIsDrawing(false)
    const pts = currentDrawPoints.current
    if (pts.length > 1) {
      drawPathsRef.current = {
        ...drawPathsRef.current,
        [currentPage]: [
          ...(drawPathsRef.current[currentPage] || []),
          { id: genId(), points: pts, color: textDefaults.color, size: 2.5 },
        ],
      }
    }
    currentDrawPoints.current = []
  }

  // ─── Annotations ──────────────────────────────────────────
  const getPageAnns = (page) => annotations[page] || []

  const addAnn = (ann) => {
    setAnnotations(prev => ({
      ...prev,
      [ann.pageIndex]: [...(prev[ann.pageIndex] || []), ann],
    }))
  }

  const updateAnn = (id, changes) => {
    setAnnotations(prev => {
      const next = { ...prev }
      for (const key of Object.keys(next)) {
        next[key] = next[key].map(a => a.id === id ? { ...a, ...changes } : a)
      }
      return next
    })
  }

  const deleteAnn = (id) => {
    setAnnotations(prev => {
      const next = { ...prev }
      for (const key of Object.keys(next)) {
        next[key] = next[key].filter(a => a.id !== id)
      }
      return next
    })
    if (selectedId === id) setSelectedId(null)
  }

  const findAnn = (id) => {
    for (const anns of Object.values(annotations)) {
      const a = anns.find(a => a.id === id)
      if (a) return a
    }
    return null
  }

  const selectedAnn = selectedId ? findAnn(selectedId) : null

  // ─── Canvas interactions ──────────────────────────────────
  const handleCanvasClick = (e) => {
    if (!pdfJsDoc) return
    if (dragState.current.moved) return
    const rect = pageCanvasRef.current.getBoundingClientRect()
    const x = e.clientX - rect.left
    const y = e.clientY - rect.top

    if (activeTool === TOOLS.TEXT) {
      const id = genId()
      addAnn({
        id,
        type: 'text',
        pageIndex: currentPage,
        x, y,
        content: '',
        fontSize: textDefaults.fontSize,
        fontFamily: textDefaults.fontFamily,
        color: textDefaults.color,
        bold: textDefaults.bold,
        italic: textDefaults.italic,
        underline: textDefaults.underline,
        editing: true,
      })
      setSelectedId(id)
    } else if (activeTool === TOOLS.SIGNATURE) {
      pendingSigPos.current = { x, y }
      openSigModal()
    }
  }

  // ─── Annotation drag ─────────────────────────────────────
  const onAnnMouseDown = (e, ann) => {
    if (activeTool !== TOOLS.SELECT) return
    e.stopPropagation()
    setSelectedId(ann.id)
    dragState.current = {
      active: true,
      annId: ann.id,
      startX: e.clientX,
      startY: e.clientY,
      origX: ann.x,
      origY: ann.y,
      moved: false,
    }
    const onMove = (me) => {
      const dx = me.clientX - dragState.current.startX
      const dy = me.clientY - dragState.current.startY
      if (Math.abs(dx) + Math.abs(dy) > 2) dragState.current.moved = true
      updateAnn(dragState.current.annId, {
        x: dragState.current.origX + dx,
        y: dragState.current.origY + dy,
      })
    }
    const onUp = () => {
      dragState.current.active = false
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }

  // ─── Signature modal ─────────────────────────────────────
  const openSigModal = () => {
    setSigModalOpen(true)
    setTimeout(() => {
      const canvas = sigCanvasRef.current
      if (!canvas) return
      const wrap = canvas.parentElement
      canvas.width = wrap ? wrap.clientWidth - 2 : 516
      canvas.height = 180
      if (sigPadRef.current) sigPadRef.current.off()
      sigPadRef.current = new SignaturePad(canvas, {
        backgroundColor: 'rgba(0,0,0,0)',
        penColor: ANNOT_INK,
        velocityFilterWeight: 0.7,
        minWidth: 1,
        maxWidth: 3,
      })
    }, 80)
  }

  const closeSigModal = () => {
    setSigModalOpen(false)
    pendingSigPos.current = null
    if (sigPadRef.current) sigPadRef.current.clear()
    setTypedName('')
  }

  const renderTypedSig = async (text, font) => {
    return new Promise(resolve => {
      const c = document.createElement('canvas')
      const ctx = c.getContext('2d')
      c.width = 600; c.height = 120
      ctx.font = `64px '${font}'`
      ctx.fillStyle = ANNOT_INK
      ctx.textBaseline = 'middle'
      ctx.fillText(text, 20, 60)
      const d = ctx.getImageData(0, 0, c.width, c.height)
      let minX = c.width, minY = c.height, maxX = 0, maxY = 0
      for (let py = 0; py < c.height; py++) {
        for (let px = 0; px < c.width; px++) {
          if (d.data[(py * c.width + px) * 4 + 3] > 8) {
            minX = Math.min(minX, px); maxX = Math.max(maxX, px)
            minY = Math.min(minY, py); maxY = Math.max(maxY, py)
          }
        }
      }
      if (maxX > minX) {
        const out = document.createElement('canvas')
        out.width = maxX - minX + 24; out.height = maxY - minY + 16
        out.getContext('2d').drawImage(c, minX - 12, minY - 8, out.width, out.height, 0, 0, out.width, out.height)
        resolve(out.toDataURL('image/png'))
      } else {
        resolve(c.toDataURL('image/png'))
      }
    })
  }

  const applySig = async () => {
    let imageData = null
    if (sigTab === 'draw') {
      if (!sigPadRef.current || sigPadRef.current.isEmpty()) {
        showToast('Please draw your signature first')
        return
      }
      imageData = sigPadRef.current.toDataURL('image/png')
    } else {
      if (!typedName.trim()) { showToast('Please type your name'); return }
      imageData = await renderTypedSig(typedName.trim(), sigFont)
    }

    if (saveToLib) {
      const newSig = { id: genId(), imageData }
      setSavedSigs(prev => [...prev, newSig])
    }

    if (pendingSigPos.current && pdfJsDoc) {
      placeSig(imageData, pendingSigPos.current.x, pendingSigPos.current.y)
    }

    closeSigModal()
  }

  const placeSig = (imageData, x, y) => {
    addAnn({
      id: genId(),
      type: 'signature',
      pageIndex: currentPage,
      x: x - 100,
      y: y - 40,
      width: 220,
      height: 88,
      imageData,
    })
    setActiveTool(TOOLS.SELECT)
  }

  // ─── Zoom ─────────────────────────────────────────────────
  const changeZoom = (delta) => {
    setZoom(z => Math.round(Math.min(3, Math.max(0.25, z + delta)) * 10) / 10)
  }

  const fitPage = async () => {
    if (!pdfJsDoc) return
    const area = canvasAreaRef.current
    const page = await pdfJsDoc.getPage(currentPage)
    const vp = page.getViewport({ scale: 1 })
    const sw = (area.clientWidth - 80) / vp.width
    const sh = (area.clientHeight - 80) / vp.height
    setZoom(Math.round(Math.min(sw, sh, 2) * 10) / 10)
  }

  // ─── Navigate pages ───────────────────────────────────────
  const goToPage = (n) => {
    if (n < 1 || n > totalPages) return
    setCurrentPage(n)
    setSelectedId(null)
  }

  // ─── Keyboard shortcuts ───────────────────────────────────
  useEffect(() => {
    const onKey = (e) => {
      const tag = document.activeElement?.tagName
      if (['INPUT', 'TEXTAREA'].includes(tag) || document.activeElement?.contentEditable === 'true') return
      switch (e.key) {
        case 'v': case 'V': setActiveTool(TOOLS.SELECT); break
        case 't': case 'T': setActiveTool(TOOLS.TEXT); break
        case 's': case 'S': setActiveTool(TOOLS.SIGNATURE); break
        case 'd': case 'D': setActiveTool(TOOLS.DRAW); break
        case 'Delete': case 'Backspace':
          if (selectedId) { deleteAnn(selectedId); e.preventDefault() }
          break
        case 'Escape': setSelectedId(null); setActiveTool(TOOLS.SELECT); break
        case '=': case '+': changeZoom(0.1); break
        case '-': changeZoom(-0.1); break
        case 'ArrowLeft': goToPage(currentPage - 1); break
        case 'ArrowRight': goToPage(currentPage + 1); break
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [selectedId, currentPage, totalPages])

  // ─── Save PDF ─────────────────────────────────────────────
  const savePDF = async () => {
    if (!pdfArrayBuffer) return
    showToast('Preparing PDF…')
    try {
      const srcDoc = await PDFDocument.load(pdfArrayBuffer)
      const outDoc = await PDFDocument.create()
      const hv = await outDoc.embedFont(StandardFonts.Helvetica)
      const hvB = await outDoc.embedFont(StandardFonts.HelveticaBold)
      const hvI = await outDoc.embedFont(StandardFonts.HelveticaOblique)
      const hvBI = await outDoc.embedFont(StandardFonts.HelveticaBoldOblique)

      const effectiveOrder = pageOrder.length > 0 ? pageOrder : Array.from({ length: totalPages }, (_, i) => i + 1)

      for (let displaySlot = 1; displaySlot <= effectiveOrder.length; displaySlot++) {
        const origPageNum = effectiveOrder[displaySlot - 1]
        let outPage

        if (origPageNum < 1) {
          const blankBuf = blankPageBuffers.current[origPageNum]
          if (blankBuf) {
            const blankSrc = await PDFDocument.load(blankBuf)
            const [bp] = await outDoc.copyPages(blankSrc, [0])
            outDoc.addPage(bp)
          } else {
            outDoc.addPage([612, 792])
          }
          outPage = outDoc.getPage(outDoc.getPageCount() - 1)
        } else {
          const [copiedPage] = await outDoc.copyPages(srcDoc, [origPageNum - 1])
          outDoc.addPage(copiedPage)
          outPage = outDoc.getPage(outDoc.getPageCount() - 1)
        }

        const extraRot = pageRotations[displaySlot] || 0
        if (extraRot !== 0) {
          outPage.setRotation(degrees((outPage.getRotation().angle + extraRot) % 360))
        }

        const { width: pW, height: pH } = outPage.getSize()
        let cW = pW, cH = pH
        if (origPageNum >= 1 && pdfJsDoc) {
          try {
            const jsPage = await pdfJsDoc.getPage(origPageNum)
            const vp = jsPage.getViewport({ scale: zoom, rotation: (jsPage.rotate + extraRot) % 360 })
            cW = vp.width; cH = vp.height
          } catch {}
        }

        const anns = annotations[displaySlot] || []
        for (const ann of anns) {
          if (ann.type === 'text' && ann.content?.trim()) {
            const pdfX = (ann.x / cW) * pW
            const pdfY = pH - ((ann.y / cH) * pH) - ann.fontSize * (pH / cH)
            const font = ann.bold && ann.italic ? hvBI : ann.bold ? hvB : ann.italic ? hvI : hv
            const size = ann.fontSize * (pH / cH)
            const { r, g, b } = hexToRgb01(ann.color || '#000000')
            outPage.drawText(ann.content, {
              x: Math.max(0, pdfX),
              y: Math.max(0, pdfY),
              size, font, color: rgb(r, g, b),
            })
          } else if (ann.type === 'signature' && ann.imageData) {
            try {
              const res = await fetch(ann.imageData)
              const blob = await res.blob()
              const bytes = new Uint8Array(await blob.arrayBuffer())
              const img = await outDoc.embedPng(bytes)
              const pdfX = (ann.x / cW) * pW
              const pdfY = pH - ((ann.y + ann.height) / cH) * pH
              outPage.drawImage(img, {
                x: Math.max(0, pdfX),
                y: Math.max(0, pdfY),
                width: (ann.width / cW) * pW,
                height: (ann.height / cH) * pH,
              })
            } catch {}
          }
        }

        const paths = drawPathsRef.current[displaySlot]
        if (paths?.length) {
          const tmp = document.createElement('canvas')
          tmp.width = cW; tmp.height = cH
          const ctx = tmp.getContext('2d')
          paths.forEach(({ points, color, size }) => {
            if (points.length < 2) return
            ctx.beginPath(); ctx.strokeStyle = color; ctx.lineWidth = size
            ctx.lineCap = 'round'; ctx.lineJoin = 'round'
            ctx.moveTo(points[0].x, points[0].y)
            points.slice(1).forEach(p => ctx.lineTo(p.x, p.y))
            ctx.stroke()
          })
          try {
            const res = await fetch(tmp.toDataURL('image/png'))
            const bytes = new Uint8Array(await (await res.blob()).arrayBuffer())
            const img = await outDoc.embedPng(bytes)
            outPage.drawImage(img, { x: 0, y: 0, width: pW, height: pH })
          } catch {}
        }
      }

      const bytes = await outDoc.save()
      const blob = new Blob([bytes], { type: 'application/pdf' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = (filename.replace(/\.pdf$/i, '') || 'document') + '_edited.pdf'
      a.click()
      URL.revokeObjectURL(url)
      showToast('PDF downloaded')
    } catch (e) {
      showToast('Save error: ' + e.message)
      console.error(e)
    }
  }

  // ─── Total annotations count ──────────────────────────────
  const totalAnns = Object.values(annotations).reduce((s, a) => s + a.filter(x => x.type === 'signature' || x.content?.trim()).length, 0)
    + Object.values(drawPathsRef.current).reduce((s, p) => s + p.length, 0)

  const textActive = activeTool === TOOLS.TEXT || (selectedAnn?.type === 'text')

  // ─── Render ───────────────────────────────────────────────
  return (
    <div className="flex-1 flex flex-col bg-bg text-ink overflow-hidden">

      {/* Topbar */}
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
              <span className="text-sm font-semibold tracking-tightish">PDF</span>
            </div>
            {filename && (
              <span className="text-2xs text-ink-faint truncate max-w-[18ch] font-serif italic">
                — {filename}
              </span>
            )}
          </>
        }
        title={null}
        meta={
          pdfJsDoc && (
            <span className="text-2xs text-ink-faint tracking-tightish">
              {totalAnns} annotation{totalAnns === 1 ? '' : 's'} · {Math.round(zoom * 100)}%
            </span>
          )
        }
        actions={
          <>
            {pdfJsDoc && (
              <>
                <Tooltip label="Zoom out (−)">
                  <IconButton size="sm" onClick={() => changeZoom(-0.15)}>
                    <ZoomOut size={14} />
                  </IconButton>
                </Tooltip>
                <Tooltip label="Zoom in (+)">
                  <IconButton size="sm" onClick={() => changeZoom(0.15)}>
                    <ZoomIn size={14} />
                  </IconButton>
                </Tooltip>
                <Tooltip label="Fit page">
                  <IconButton size="sm" onClick={fitPage}>
                    <Maximize2 size={13} />
                  </IconButton>
                </Tooltip>
                <span aria-hidden className="h-5 w-px bg-line mx-1" />
              </>
            )}
            <Button
              variant="secondary"
              size="sm"
              onClick={() => fileInputRef.current?.click()}
            >
              <Upload size={13} /> Open PDF
            </Button>
            {pdfJsDoc && (
              /* Prepare to Sign = the distinguished primary affordance */
              <Button
                variant="primary"
                size="sm"
                onClick={() => navigate('/signing-setup', { state: { localFileUrl: filename ? undefined : null, pdfReady: true } })}
              >
                <FileSignature size={13} /> Prepare to Sign
              </Button>
            )}
            {pdfJsDoc && (
              <Button
                variant="secondary"
                size="sm"
                onClick={savePDF}
              >
                <Download size={13} /> Download
              </Button>
            )}
          </>
        }
      />

      {/* Tool toolbar */}
      <div className="bg-paper border-b border-line h-11 flex items-center gap-1 px-3 flex-shrink-0">
        <span className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase mr-1">
          Tools
        </span>
        {[
          { tool: TOOLS.SELECT, icon: MousePointer2, label: 'Select (V)' },
          { tool: TOOLS.TEXT, icon: Type, label: 'Text (T)' },
          { tool: TOOLS.SIGNATURE, icon: PenLine, label: 'Signature (S)' },
          { tool: TOOLS.DRAW, icon: Pencil, label: 'Draw (D)' },
        ].map(({ tool, icon: Icon, label }) => (
          <Tooltip key={tool} label={label}>
            <IconButton
              size="sm"
              active={activeTool === tool}
              onClick={() => setActiveTool(tool)}
            >
              <Icon size={14} />
            </IconButton>
          </Tooltip>
        ))}

        <span aria-hidden className="h-5 w-px bg-line mx-1" />

        {/* Text formatting */}
        <div
          className={[
            'flex items-center gap-1.5 transition-opacity duration-fast ease-out',
            textActive ? 'opacity-100' : 'opacity-30 pointer-events-none',
          ].join(' ')}
        >
          <select
            value={textDefaults.fontSize}
            onChange={e => {
              const v = parseInt(e.target.value)
              setTextDefaults(p => ({ ...p, fontSize: v }))
              if (selectedAnn?.type === 'text') updateAnn(selectedAnn.id, { fontSize: v })
            }}
            className="h-7 px-1.5 rounded-sm bg-bg-elev2 border border-line text-2xs text-ink outline-none focus:border-accent focus:shadow-focus"
          >
            {[8,10,11,12,14,16,18,20,24,28,32,36,42,48,60,72].map(s => (
              <option key={s} value={s}>{s}px</option>
            ))}
          </select>

          <select
            value={textDefaults.fontFamily}
            onChange={e => {
              setTextDefaults(p => ({ ...p, fontFamily: e.target.value }))
              if (selectedAnn?.type === 'text') updateAnn(selectedAnn.id, { fontFamily: e.target.value })
            }}
            className="h-7 px-1.5 rounded-sm bg-bg-elev2 border border-line text-2xs text-ink outline-none focus:border-accent focus:shadow-focus w-28"
          >
            <option value="Helvetica">Helvetica</option>
            <option value="Times New Roman">Times New Roman</option>
            <option value="Courier">Courier</option>
            <option value="Georgia">Georgia</option>
          </select>

          {[
            { key: 'bold',      icon: Bold,          label: 'Bold' },
            { key: 'italic',    icon: Italic,        label: 'Italic' },
            { key: 'underline', icon: UnderlineIcon, label: 'Underline' },
          ].map(({ key, icon: Icon, label }) => (
            <Tooltip key={key} label={label}>
              <IconButton
                size="sm"
                active={textDefaults[key]}
                onClick={() => {
                  const val = !textDefaults[key]
                  setTextDefaults(p => ({ ...p, [key]: val }))
                  if (selectedAnn?.type === 'text') updateAnn(selectedAnn.id, { [key]: val })
                }}
              >
                <Icon size={13} />
              </IconButton>
            </Tooltip>
          ))}

          {/* Colour picker */}
          <div className="relative w-7 h-7">
            <span
              className="absolute inset-1 rounded-full border border-line-strong pointer-events-none"
              style={{ background: textDefaults.color }}
            />
            <input
              type="color"
              value={textDefaults.color}
              onChange={e => {
                setTextDefaults(p => ({ ...p, color: e.target.value }))
                if (selectedAnn?.type === 'text') updateAnn(selectedAnn.id, { color: e.target.value })
              }}
              className="absolute inset-0 opacity-0 cursor-pointer w-full h-full"
            />
          </div>
        </div>

        <div className="flex-1" />

        <Tooltip label="Toggle page thumbnails">
          <IconButton
            size="sm"
            active={sidebarOpen}
            onClick={() => setSidebarOpen(v => !v)}
          >
            <LayoutList size={14} />
          </IconButton>
        </Tooltip>
        <Tooltip label="Toggle properties">
          <IconButton
            size="sm"
            active={panelOpen}
            onClick={() => setPanelOpen(v => !v)}
          >
            <SlidersHorizontal size={14} />
          </IconButton>
        </Tooltip>
      </div>

      {/* WORKSPACE */}
      <div className="flex-1 flex overflow-hidden">

        {/* LEFT SIDEBAR — page thumbnails */}
        {sidebarOpen && (
          <aside className="w-52 bg-bg-elev2 border-r border-line flex flex-col flex-shrink-0 overflow-hidden">
            <div className="px-3 py-2.5 border-b border-line flex items-center justify-between">
              <span className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
                Pages
              </span>
              {totalPages > 0 && (
                <span className="text-2xs text-ink-faint">{totalPages} total</span>
              )}
            </div>

            {pdfJsDoc && (
              <div className="flex gap-1 px-2 py-2 border-b border-line">
                <Button
                  variant="secondary"
                  size="sm"
                  className="flex-1"
                  onClick={() => insertBlankPage(currentPage)}
                  title="Insert blank page after current"
                >
                  <FilePlus size={12} /> Blank
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  className="flex-1"
                  onClick={() => insertFileInputRef.current?.click()}
                  title="Insert page from PDF after current"
                >
                  <Upload size={12} /> PDF
                </Button>
              </div>
            )}

            <div className="flex-1 overflow-y-auto p-2 flex flex-col gap-1.5">
              {!pdfJsDoc ? (
                <p className="text-2xs text-ink-faint text-center py-6 font-serif italic">
                  Open a PDF to see pages.
                </p>
              ) : (
                Array.from({ length: pageOrder.length || totalPages }, (_, i) => i + 1).map(n => {
                  const isActive = currentPage === n
                  return (
                    <div
                      key={n}
                      draggable
                      onDragStart={() => setThumbDragSrc(n)}
                      onDragOver={e => { e.preventDefault() }}
                      onDrop={e => {
                        e.preventDefault()
                        if (thumbDragSrc != null && thumbDragSrc !== n) {
                          reorderPages(thumbDragSrc, n)
                        }
                        setThumbDragSrc(null)
                      }}
                      onDragEnd={() => setThumbDragSrc(null)}
                      onClick={() => goToPage(n)}
                      className={[
                        'relative flex flex-col items-center gap-1 p-1.5 rounded-md cursor-grab',
                        'transition-colors duration-fast ease-out',
                        isActive ? 'bg-accent-tint' : 'hover:bg-paper',
                        thumbDragSrc === n ? 'opacity-50' : '',
                        thumbDragSrc != null && thumbDragSrc !== n
                          ? 'outline outline-1 outline-dashed outline-accent/40'
                          : '',
                      ].join(' ')}
                    >
                      <span
                        aria-hidden
                        className={[
                          'absolute left-0 top-1 bottom-1 w-[2px] rounded-r-full transition-colors duration-fast',
                          isActive ? 'bg-accent' : 'bg-transparent',
                        ].join(' ')}
                      />
                      <div
                        className={[
                          'relative w-full rounded-xs overflow-hidden bg-paper border shadow-e1 transition-colors duration-fast',
                          isActive ? 'border-accent' : 'border-line',
                        ].join(' ')}
                      >
                        <canvas
                          ref={el => { if (el) thumbnailRefs.current[n] = el }}
                          className="block w-full"
                        />
                        {(pageRotations[n] || 0) !== 0 && (
                          <span className="absolute top-1 right-1 bg-accent text-white rounded-pill text-2xs px-1.5 py-0">
                            {pageRotations[n] || 0}°
                          </span>
                        )}
                      </div>
                      <div className="flex items-center gap-1 w-full">
                        <span
                          className={[
                            'text-2xs flex-1 text-center tracking-tightish',
                            isActive ? 'text-accent-press font-medium' : 'text-ink-faint',
                          ].join(' ')}
                        >
                          {n}
                        </span>
                        <Tooltip label="Rotate 90°">
                          <button
                            type="button"
                            onClick={e => { e.stopPropagation(); rotatePage(n, 90) }}
                            className="w-5 h-5 rounded-sm text-ink-faint hover:bg-accent-tint hover:text-accent-press transition-colors flex items-center justify-center"
                          >
                            <RotateCw size={11} />
                          </button>
                        </Tooltip>
                        <Tooltip label="Delete page">
                          <button
                            type="button"
                            onClick={e => { e.stopPropagation(); deletePageSlot(n) }}
                            className="w-5 h-5 rounded-sm text-ink-faint hover:bg-danger-bg hover:text-danger transition-colors flex items-center justify-center"
                          >
                            <Trash size={11} />
                          </button>
                        </Tooltip>
                      </div>
                    </div>
                  )
                })
              )}
            </div>
          </aside>
        )}

        {/* MAIN CANVAS AREA */}
        <div
          ref={canvasAreaRef}
          className="flex-1 overflow-auto bg-bg paper-grain flex flex-col items-center px-8 py-8 relative"
          onDragOver={e => { e.preventDefault(); setDragOver(true) }}
          onDragLeave={() => setDragOver(false)}
          onDrop={handleDrop}
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
                  Open a PDF to get started
                </h2>
                <p className="text-sm text-ink-muted mt-1 font-serif italic">
                  Drag & drop here, or click to browse.
                </p>
              </div>
              <Button variant="primary">Browse files</Button>
            </div>
          ) : (
            <>
              <div
                className="relative shadow-e2 rounded-sm overflow-hidden bg-paper animate-fade-in"
                onClick={handleCanvasClick}
                style={{ cursor: CURSORS[activeTool] }}
              >
                {loadingPdf && (
                  <div className="absolute inset-0 bg-paper/70 flex items-center justify-center z-50">
                    <div className="w-7 h-7 border-2 border-accent border-t-transparent rounded-full animate-spin" />
                  </div>
                )}
                <canvas ref={pageCanvasRef} className="block" />

                {/* Draw canvas */}
                <canvas
                  ref={drawCanvasRef}
                  className="absolute top-0 left-0"
                  style={{
                    pointerEvents: activeTool === TOOLS.DRAW ? 'all' : 'none',
                    cursor: activeTool === TOOLS.DRAW ? 'crosshair' : 'default',
                  }}
                  onMouseDown={onDrawStart}
                  onMouseMove={onDrawMove}
                  onMouseUp={onDrawEnd}
                  onMouseLeave={onDrawEnd}
                />

                {/* Annotations layer */}
                <div ref={annotLayerRef} className="absolute top-0 left-0 w-full h-full pointer-events-none">
                  {getPageAnns(currentPage).map(ann => (
                    <AnnotationElement
                      key={ann.id}
                      ann={ann}
                      selected={selectedId === ann.id}
                      activeTool={activeTool}
                      onMouseDown={onAnnMouseDown}
                      onDelete={() => deleteAnn(ann.id)}
                      onUpdate={changes => updateAnn(ann.id, changes)}
                      onSelect={() => setSelectedId(ann.id)}
                    />
                  ))}
                </div>
              </div>

              {/* Page navigation */}
              {totalPages > 1 && (
                <div className="flex items-center gap-3 mt-5">
                  <IconButton
                    size="sm"
                    onClick={() => goToPage(currentPage - 1)}
                    disabled={currentPage <= 1}
                  >
                    <ChevronLeft size={15} />
                  </IconButton>
                  <span className="text-xs text-ink-muted tracking-tightish bg-paper border border-line rounded-md px-3 py-1">
                    Page {currentPage} of {totalPages}
                  </span>
                  <IconButton
                    size="sm"
                    onClick={() => goToPage(currentPage + 1)}
                    disabled={currentPage >= totalPages}
                  >
                    <ChevronRight size={15} />
                  </IconButton>
                </div>
              )}
            </>
          )}
        </div>

        {/* RIGHT PANEL — properties */}
        {panelOpen && (
          <aside className="w-64 bg-bg-elev2 border-l border-line flex flex-col flex-shrink-0 overflow-hidden">
            <div className="px-3 py-2.5 border-b border-line">
              <span className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
                Properties
              </span>
            </div>
            <div className="flex-1 overflow-y-auto p-3 space-y-4">

              {selectedAnn && (
                <div className="bg-paper border border-line rounded-md p-3 space-y-3">
                  <div className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
                    Selection
                  </div>
                  {selectedAnn.type === 'text' && (
                    <>
                      <PanelRow label="Size">
                        <input
                          type="number"
                          min={6}
                          max={120}
                          value={selectedAnn.fontSize}
                          onChange={e => updateAnn(selectedAnn.id, { fontSize: parseInt(e.target.value) || 12 })}
                          className="w-full h-7 bg-paper border border-line rounded-sm px-2 text-xs text-ink outline-none focus:border-accent focus:shadow-focus"
                        />
                      </PanelRow>
                      <PanelRow label="Color">
                        <input
                          type="color"
                          value={selectedAnn.color}
                          onChange={e => updateAnn(selectedAnn.id, { color: e.target.value })}
                          className="w-full h-7 rounded-sm border border-line cursor-pointer p-0.5 bg-paper"
                        />
                      </PanelRow>
                    </>
                  )}
                  {selectedAnn.type === 'signature' && (
                    <div className="space-y-1.5">
                      <img
                        src={selectedAnn.imageData}
                        alt="sig"
                        className="w-full object-contain max-h-16 bg-paper border border-line rounded-sm p-1"
                      />
                      <p className="text-2xs text-ink-faint font-serif italic">
                        {Math.round(selectedAnn.width)} × {Math.round(selectedAnn.height)} px
                        — drag to reposition, resize from corner.
                      </p>
                    </div>
                  )}
                  <Button
                    variant="destructive"
                    size="sm"
                    fullWidth
                    onClick={() => deleteAnn(selectedAnn.id)}
                  >
                    <Trash2 size={12} /> Delete
                  </Button>
                </div>
              )}

              {/* Saved signatures */}
              <div className="space-y-2">
                <div className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
                  Saved signatures
                </div>
                {savedSigs.length === 0 ? (
                  <p className="text-2xs text-ink-faint font-serif italic">
                    No signatures saved yet.
                  </p>
                ) : (
                  <div className="flex flex-col gap-1.5">
                    {savedSigs.map(sig => (
                      <div
                        key={sig.id}
                        className={[
                          'flex items-center gap-2 px-2 py-1.5 rounded-sm border bg-paper transition-colors',
                          pdfJsDoc ? 'cursor-pointer hover:border-accent' : 'cursor-default',
                          'border-line',
                        ].join(' ')}
                        onClick={() => {
                          if (!pdfJsDoc) { showToast('Open a PDF first'); return }
                          const canvas = pageCanvasRef.current
                          if (!canvas) return
                          placeSig(sig.imageData, canvas.width / 2, canvas.height / 2)
                          showToast('Signature placed — drag to position')
                        }}
                      >
                        <img
                          src={sig.imageData}
                          alt="sig"
                          className="h-7 max-w-[110px] object-contain flex-1"
                        />
                        <Tooltip label="Remove">
                          <button
                            type="button"
                            onClick={e => { e.stopPropagation(); setSavedSigs(p => p.filter(s => s.id !== sig.id)) }}
                            className="w-5 h-5 rounded-sm text-ink-faint hover:bg-danger-bg hover:text-danger transition-colors flex items-center justify-center"
                          >
                            <X size={12} />
                          </button>
                        </Tooltip>
                      </div>
                    ))}
                  </div>
                )}
                <Button
                  variant="secondary"
                  size="sm"
                  fullWidth
                  onClick={() => { pendingSigPos.current = null; openSigModal() }}
                >
                  <Plus size={12} /> New signature
                </Button>
              </div>

              {/* Stats */}
              <div className="bg-paper border border-line rounded-md p-3 space-y-1.5">
                <div className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase mb-1">
                  Document
                </div>
                {[
                  ['Pages', totalPages || '—'],
                  ['Annotations', totalAnns],
                  ['Zoom', Math.round(zoom * 100) + '%'],
                  ['Current page', pdfJsDoc ? currentPage : '—'],
                ].map(([k, v]) => (
                  <div key={k} className="flex justify-between text-xs text-ink-muted tracking-tightish">
                    <span>{k}</span>
                    <span className="text-ink font-medium">{v}</span>
                  </div>
                ))}
              </div>
            </div>
          </aside>
        )}
      </div>

      {/* STATUS BAR — quiet hairline */}
      <div className="bg-paper border-t border-line h-7 flex items-center gap-4 px-3 flex-shrink-0">
        {[
          ['Tool', activeTool.charAt(0).toUpperCase() + activeTool.slice(1)],
          ['Zoom', Math.round(zoom * 100) + '%'],
          ['Page', pdfJsDoc ? `${currentPage} / ${totalPages}` : '—'],
          ['Annotations', totalAnns],
        ].map(([k, v]) => (
          <div key={k} className="flex gap-1.5 items-center text-2xs text-ink-faint tracking-tightish">
            {k}: <span className="text-ink-muted">{v}</span>
          </div>
        ))}
        <div className="flex-1" />
        <span className="text-2xs text-ink-faint tracking-tightish">
          V · Select &nbsp; T · Text &nbsp; S · Signature &nbsp; D · Draw &nbsp; Del · Delete
        </span>
      </div>

      {/* SIGNATURE MODAL */}
      {sigModalOpen && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center p-4 animate-fade-in"
          style={{ background: 'rgba(26, 25, 22, 0.36)', backdropFilter: 'blur(2px)' }}
          onClick={e => { if (e.target === e.currentTarget) closeSigModal() }}
        >
          <div
            role="dialog"
            aria-modal="true"
            className="bg-paper text-ink rounded-xl border border-line shadow-e3 w-full max-w-xl overflow-hidden animate-scale-in"
          >
            <div className="flex items-center justify-between px-5 py-3.5 border-b border-line">
              <h3 className="text-md font-semibold tracking-tightish">Add signature</h3>
              <IconButton size="sm" onClick={closeSigModal} title="Close">
                <X size={15} />
              </IconButton>
            </div>

            <div className="px-5 py-4 space-y-4">
              <Tabs
                value={sigTab}
                onChange={setSigTab}
                items={[
                  { value: 'draw', label: 'Draw' },
                  { value: 'type', label: 'Type' },
                ]}
              />

              {sigTab === 'draw' ? (
                <div>
                  <div className="relative bg-paper border border-line rounded-md overflow-hidden">
                    <canvas ref={sigCanvasRef} className="block touch-none" />
                    <div
                      id="sig-hint"
                      className="absolute inset-0 flex items-center justify-center text-ink-faint text-sm font-serif italic pointer-events-none"
                    >
                      Sign with your mouse or touch
                    </div>
                  </div>
                  <div className="flex justify-end mt-2">
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => {
                        sigPadRef.current?.clear()
                        const el = document.getElementById('sig-hint')
                        if (el) el.style.opacity = 1
                      }}
                    >
                      Clear
                    </Button>
                  </div>
                </div>
              ) : (
                <div className="bg-bg-elev2 border border-line rounded-md p-5">
                  <input
                    type="text"
                    placeholder="Type your name…"
                    value={typedName}
                    onChange={e => setTypedName(e.target.value)}
                    maxLength={60}
                    className="w-full bg-transparent border-none outline-none text-center text-4xl text-ink mb-3"
                    style={{ fontFamily: `'${sigFont}', cursive` }}
                  />
                  <div className="bg-paper border border-line rounded-sm py-3 px-3 text-center min-h-[70px] flex items-center justify-center">
                    <span
                      className="text-4xl text-ink"
                      style={{ fontFamily: `'${sigFont}', cursive` }}
                    >
                      {typedName || ''}
                    </span>
                  </div>
                  <div className="flex gap-2 justify-center mt-3">
                    {[['Dancing Script', 'Signature'], ['Pinyon Script', 'Elegant']].map(([f, label]) => (
                      <button
                        key={f}
                        type="button"
                        onClick={() => setSigFont(f)}
                        className={[
                          'px-4 py-1 rounded-pill border text-md transition-colors',
                          sigFont === f
                            ? 'border-accent bg-accent-tint text-accent-press'
                            : 'border-line bg-paper text-ink-muted hover:border-line-strong',
                        ].join(' ')}
                        style={{ fontFamily: `'${f}', cursive` }}
                      >
                        Aa
                      </button>
                    ))}
                  </div>
                </div>
              )}

              <label className="flex items-center gap-2 text-sm text-ink-muted cursor-pointer">
                <input
                  type="checkbox"
                  checked={saveToLib}
                  onChange={e => setSaveToLib(e.target.checked)}
                  style={{ accentColor: 'var(--accent)' }}
                />
                Save this signature for future use
              </label>
            </div>

            <div className="px-5 py-3 border-t border-line bg-bg-elev2 flex items-center justify-end gap-2">
              <Button variant="secondary" size="sm" onClick={closeSigModal}>Cancel</Button>
              <Button variant="primary" size="sm" onClick={applySig}>Apply signature</Button>
            </div>
          </div>
        </div>
      )}

      {/* Toast — quiet pill */}
      {toast && (
        <div className="fixed bottom-10 left-1/2 -translate-x-1/2 z-[2000] bg-ink text-paper px-4 py-2 rounded-md shadow-e2 text-xs tracking-tightish animate-fade-in pointer-events-none">
          {toast}
        </div>
      )}

      <input ref={fileInputRef} type="file" accept=".pdf" className="hidden" onChange={handleFileInput} />
      <input
        ref={insertFileInputRef}
        type="file"
        accept=".pdf"
        className="hidden"
        onChange={e => {
          const f = e.target.files[0]
          if (f) insertPDFPage(f, currentPage)
          e.target.value = ''
        }}
      />

      {/* Cursive fonts for typed signatures */}
      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=Dancing+Script:wght@700&family=Pinyon+Script&display=swap');
        .pdf-annot-text:focus { outline: none; }
        .pdf-annot-text[contenteditable="true"] { cursor: text; }
        .pdf-annot-text {
          cursor: default; white-space: pre-wrap; word-break: break-word;
          min-width: 20px; min-height: 1em;
        }
      `}</style>
    </div>
  )
}

// ─── Annotation Element ────────────────────────────────────
function AnnotationElement({ ann, selected, activeTool, onMouseDown, onDelete, onUpdate, onSelect }) {
  const textRef = useRef(null)
  const [editing, setEditing] = useState(ann.editing || false)
  const [, setResizing] = useState(false)
  const resizeStart = useRef({})

  useEffect(() => {
    if (editing && textRef.current) {
      textRef.current.focus()
      const range = document.createRange()
      range.selectNodeContents(textRef.current)
      range.collapse(false)
      window.getSelection()?.removeAllRanges()
      window.getSelection()?.addRange(range)
    }
  }, [editing])

  const handleMouseDown = (e) => {
    if (activeTool !== 'select') return
    onSelect()
    onMouseDown(e, ann)
  }

  const handleDblClick = (e) => {
    if (ann.type !== 'text') return
    e.stopPropagation()
    setEditing(true)
    onSelect()
  }

  const handleBlur = () => {
    setEditing(false)
    const content = textRef.current?.textContent || ''
    onUpdate({ content, editing: false })
    if (!content.trim()) onDelete()
  }

  const handleKeyDown = (e) => {
    if (e.key === 'Escape') { e.preventDefault(); textRef.current?.blur() }
    e.stopPropagation()
  }

  const onResizeDown = (e) => {
    e.stopPropagation(); e.preventDefault()
    resizeStart.current = { mx: e.clientX, my: e.clientY, w: ann.width, h: ann.height }
    setResizing(true)
    const onMove = (me) => {
      onUpdate({
        width: Math.max(50, resizeStart.current.w + me.clientX - resizeStart.current.mx),
        height: Math.max(20, resizeStart.current.h + me.clientY - resizeStart.current.my),
      })
    }
    const onUp = () => {
      setResizing(false)
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }

  if (ann.type === 'text') {
    return (
      <div
        className="absolute pointer-events-auto select-none"
        style={{ left: ann.x, top: ann.y, zIndex: selected ? 10 : 5 }}
        onMouseDown={handleMouseDown}
        onDoubleClick={handleDblClick}
      >
        <div
          className={[
            'relative px-1 py-0.5 rounded-xs',
            selected ? 'outline outline-2 outline-accent outline-offset-2' :
              editing ? 'outline outline-2 outline-dashed outline-accent outline-offset-2' : '',
          ].join(' ')}
        >
          <span
            ref={textRef}
            className="pdf-annot-text block"
            contentEditable={editing}
            suppressContentEditableWarning
            onBlur={handleBlur}
            onKeyDown={handleKeyDown}
            onClick={e => { if (editing) e.stopPropagation() }}
            style={{
              fontSize: ann.fontSize,
              fontFamily: ann.fontFamily,
              color: ann.color,
              fontWeight: ann.bold ? 700 : 400,
              fontStyle: ann.italic ? 'italic' : 'normal',
              textDecoration: ann.underline ? 'underline' : 'none',
              minWidth: 20,
              minHeight: '1em',
              cursor: editing ? 'text' : activeTool === 'select' ? 'move' : 'default',
            }}
          >
            {ann.content}
          </span>
          {selected && (
            <button
              type="button"
              onClick={e => { e.stopPropagation(); onDelete() }}
              aria-label="Delete"
              className="absolute -top-2.5 -right-2.5 w-5 h-5 bg-danger border-2 border-paper rounded-full flex items-center justify-center text-white shadow-e1 z-20"
            >
              <X size={11} />
            </button>
          )}
        </div>
      </div>
    )
  }

  if (ann.type === 'signature') {
    return (
      <div
        className="absolute pointer-events-auto select-none"
        style={{
          left: ann.x, top: ann.y,
          width: ann.width, height: ann.height,
          zIndex: selected ? 10 : 5,
        }}
        onMouseDown={handleMouseDown}
      >
        <div
          className={[
            'relative w-full h-full',
            selected ? 'outline outline-2 outline-accent outline-offset-2' : '',
          ].join(' ')}
        >
          <img
            src={ann.imageData}
            alt="signature"
            draggable={false}
            className="w-full h-full object-contain block"
          />
          {selected && (
            <>
              <button
                type="button"
                onClick={e => { e.stopPropagation(); onDelete() }}
                aria-label="Delete"
                className="absolute -top-2.5 -right-2.5 w-5 h-5 bg-danger border-2 border-paper rounded-full flex items-center justify-center text-white shadow-e1 z-20"
              >
                <X size={11} />
              </button>
              <div
                onMouseDown={onResizeDown}
                className="absolute -bottom-1 -right-1 w-3 h-3 bg-accent border-2 border-paper rounded-xs cursor-se-resize z-20"
              />
            </>
          )}
        </div>
      </div>
    )
  }

  return null
}

function PanelRow({ label, children }) {
  return (
    <div className="flex items-center gap-2">
      <span className="text-2xs text-ink-faint w-12 flex-shrink-0 tracking-tightish">
        {label}
      </span>
      <div className="flex-1">{children}</div>
    </div>
  )
}
