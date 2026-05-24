import { useEffect, useState, useCallback, useRef } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useEditor, EditorContent } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import Image from '@tiptap/extension-image'
import TextAlign from '@tiptap/extension-text-align'
import Underline from '@tiptap/extension-underline'
import TextStyle from '@tiptap/extension-text-style'
import Color from '@tiptap/extension-color'
import Placeholder from '@tiptap/extension-placeholder'
import {
  ArrowLeft, Save, Loader2, Play, Plus, Trash2,
  ChevronUp, ChevronDown, Download, EyeOff, MessageSquare,
  Bold, Italic, Underline as UnderlineIcon,
  AlignLeft, AlignCenter, AlignRight, List, Image as ImageIcon,
  Check, Circle, AlertCircle, StickyNote, Palette, Layout,
  Copy, FileText, GripVertical, Monitor, Zap,
  ChevronDown as ChevronDownIcon,
} from 'lucide-react'
import DOMPurify from 'dompurify'
import { useFilesStore } from '../../store/filesStore'
import { api } from '../../lib/api'
import SlidePreview from './SlidePreview'
import { exportSlidesToPdf, exportSlidesToPptx } from './slidesExport'
import { TreeSession, getTreeReplicaId, ordKeyBetween } from '../../lib/crdt/tree.js'
import CommentsPanel from '../../components/CommentsPanel'
import { useLiveCursors } from '../../lib/useLiveCursors.js'
import { getSlideViewers } from '../../components/RemoteCursors.jsx'
import { Button, IconButton, Tooltip, Topbar } from '../../components/ui'
import ThemeGallery from './ThemeGallery.jsx'
import MasterSlideEditor from './MasterSlideEditor.jsx'
import TransitionPanel from './TransitionPanel.jsx'
import InsertPanel from './InsertPanel.jsx'
import TemplateGallery from './TemplateGallery.jsx'
import { usePresenterView } from './PresenterView.jsx'
import { getTheme } from './themes.js'

const PURIFY_CONFIG = {
  USE_PROFILES: { html: true },
  FORBID_TAGS: ['script', 'object', 'embed', 'form', 'input', 'button'],
  FORBID_ATTR: ['onerror', 'onload', 'onclick', 'onmouseover', 'onfocus', 'onblur',
                'onchange', 'onsubmit', 'onkeydown', 'onkeyup', 'onkeypress'],
}

const sanitize = (html) => DOMPurify.sanitize(html ?? '', PURIFY_CONFIG)

// Reveal.js theme names (kept for backward compatibility with legacy decks).
const LEGACY_TRANSITIONS = ['none', 'fade', 'slide', 'convex', 'concave', 'zoom']

function newSlide(master = 'content') {
  return {
    id: crypto.randomUUID(),
    title: '',
    content: '<p></p>',
    notes: '',
    background: '',
    master,
    transition: 'none',
    animations: [],
  }
}

// ── Sidebar tabs ────────────────────────────────────────────────────────────
const SIDEBAR_TABS = [
  { id: 'slides', icon: FileText, label: 'Slides' },
  { id: 'transitions', icon: Zap, label: 'Transitions' },
]

export default function SlidesEditor() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { files, updateFile } = useFilesStore()
  const stored = files.find((f) => f.id === id)
  const [file, setFile] = useState(stored)

  const defaultData = file?.content && file.content.slides
    ? file.content
    : { themeId: 'obsidian', theme: 'black', transition: 'slide', slides: [newSlide()], masters: null, customTheme: null }

  const [title, setTitle] = useState(file?.name || 'Untitled Presentation')
  const [slidesData, setSlidesData] = useState(defaultData)
  const [activeIdx, setActiveIdx] = useState(0)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(true)
  const [presenting, setPresenting] = useState(false)
  const [showComments, setShowComments] = useState(false)
  const [sidebarTab, setSidebarTab] = useState('slides')

  // Modal states
  const [showThemeGallery, setShowThemeGallery] = useState(false)
  const [showMasterEditor, setShowMasterEditor] = useState(false)
  const [showTemplateGallery, setShowTemplateGallery] = useState(false)

  // Notes panel height (resizable)
  const [notesHeight, setNotesHeight] = useState(80)
  const notesResizeRef = useRef(null)
  const isResizingNotes = useRef(false)

  const saveTimer = useRef(null)
  const imgInput = useRef(null)
  const treeSessionRef = useRef(null)

  // Drag-drop for slide reorder
  const [dragSlideIdx, setDragSlideIdx] = useState(null)
  const [dragOverIdx, setDragOverIdx] = useState(null)

  const activeSlide = slidesData.slides[activeIdx] ?? slidesData.slides[0]

  // Presenter view hook
  const { openPresenter, syncSlide } = usePresenterView(slidesData)

  // Live cursors (OFFICE-25)
  const { remoteCursors, broadcastSlideCursor } = useLiveCursors({
    fabric: null, localIdentity: null, color: 'var(--signal-warning)',
  })

  const editor = useEditor({
    extensions: [
      StarterKit.configure({ heading: { levels: [1, 2, 3] } }),
      Image.configure({ allowBase64: true }),
      TextAlign.configure({ types: ['heading', 'paragraph'] }),
      Underline,
      TextStyle,
      Color,
      Placeholder.configure({ placeholder: 'Slide content…' }),
    ],
    content: activeSlide?.content || '<p></p>',
    onUpdate: ({ editor }) => {
      updateSlideField(activeIdx, 'content', editor.getHTML())
    },
  })

  // ── Load file ─────────────────────────────────────────────────────────────
  useEffect(() => {
    if (!file && id) {
      api.getFile(id).then((f) => {
        setFile(f)
        setTitle(f.name)
        if (f.content?.slides) setSlidesData(f.content)
      }).catch(() => navigate('/slides'))
    }
  }, [id])

  // ── CRDT session (OFFICE-23) ──────────────────────────────────────────────
  useEffect(() => {
    if (!id) return
    const replicaId = getTreeReplicaId()
    const session = new TreeSession({ sessionId: id, replicaId, fabricClient: null })
    treeSessionRef.current = session

    const seedTimer = setTimeout(() => {
      setSlidesData((current) => {
        const existing = session.orderedSlides().map((s) => s.nodeId)
        current.slides.forEach((slide, idx) => {
          if (!existing.includes(slide.id)) {
            const ordKey = String(idx).padStart(10, '0')
            session.insertSlide(ordKey, slide)
          }
        })
        return current
      })
    }, 0)

    session.requestSnapshot()

    const onRemote = () => {
      const crdtSlides = session.orderedSlides()
      if (crdtSlides.length === 0) return
      setSlidesData((prev) => {
        const next = {
          ...prev,
          slides: crdtSlides
            .filter((s) => s.data && typeof s.data === 'object')
            .map((s) => ({ ...s.data })),
        }
        schedule(next)
        return next
      })
    }

    session.addEventListener('remoteOp', onRemote)
    return () => {
      clearTimeout(seedTimer)
      session.removeEventListener('remoteOp', onRemote)
      session.destroy()
      treeSessionRef.current = null
    }
  }, [id]) // eslint-disable-line

  // ── Sync editor when switching slides ─────────────────────────────────────
  useEffect(() => {
    if (editor && activeSlide) {
      editor.commands.setContent(activeSlide.content || '<p></p>', false)
    }
    if (activeSlide?.id) {
      broadcastSlideCursor(activeSlide.id)
      syncSlide(activeIdx)
    }
  }, [activeIdx]) // eslint-disable-line

  // ── Notes panel resize ────────────────────────────────────────────────────
  useEffect(() => {
    const onMouseMove = (e) => {
      if (!isResizingNotes.current) return
      const container = notesResizeRef.current?.closest('.slides-layout')
      if (!container) return
      const rect = container.getBoundingClientRect()
      const newH = rect.bottom - e.clientY
      setNotesHeight(Math.max(40, Math.min(300, newH)))
    }
    const onMouseUp = () => { isResizingNotes.current = false }
    window.addEventListener('mousemove', onMouseMove)
    window.addEventListener('mouseup', onMouseUp)
    return () => {
      window.removeEventListener('mousemove', onMouseMove)
      window.removeEventListener('mouseup', onMouseUp)
    }
  }, [])

  // ── Global keyboard shortcuts ─────────────────────────────────────────────
  useEffect(() => {
    const onKeyDown = (e) => {
      // Only fire when not in an input/textarea/contenteditable.
      const tag = e.target.tagName
      const isEditing = tag === 'INPUT' || tag === 'TEXTAREA' || e.target.isContentEditable
      const meta = e.metaKey || e.ctrlKey

      if (meta && e.key === 'm' && !isEditing) {
        e.preventDefault()
        addSlide()
      }
      if (meta && e.key === 'd' && !isEditing) {
        e.preventDefault()
        duplicateSlide(activeIdx)
      }
      if (meta && e.shiftKey && e.key === 'Enter') {
        e.preventDefault()
        openPresenter(activeIdx)
      }
      // Arrow key navigation in sidebar
      if (!isEditing) {
        if (e.key === 'ArrowDown') {
          e.preventDefault()
          setActiveIdx((i) => Math.min(i + 1, slidesData.slides.length - 1))
        }
        if (e.key === 'ArrowUp') {
          e.preventDefault()
          setActiveIdx((i) => Math.max(i - 1, 0))
        }
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [activeIdx, slidesData.slides.length]) // eslint-disable-line

  // ── Autosave ──────────────────────────────────────────────────────────────
  const autosave = useCallback(async (sd) => {
    if (!id) return
    setSaving(true)
    try {
      await updateFile(id, title, sd)
      setSaved(true)
    } finally {
      setSaving(false)
    }
  }, [id, title])

  const schedule = (sd) => {
    setSaved(false)
    clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => autosave(sd), 2000)
  }

  // ── Slide field update ────────────────────────────────────────────────────
  const updateSlideField = (idx, field, value) => {
    setSlidesData((prev) => {
      const slides = [...prev.slides]
      slides[idx] = { ...slides[idx], [field]: value }
      const next = { ...prev, slides }
      schedule(next)
      const session = treeSessionRef.current
      if (session) {
        const slide = slides[idx]
        session.setSlide(slide.id, slide)
        session.saveLocal()
      }
      return next
    })
  }

  const updateSlideMeta = (idx, updates) => {
    setSlidesData((prev) => {
      const slides = [...prev.slides]
      slides[idx] = { ...slides[idx], ...updates }
      const next = { ...prev, slides }
      schedule(next)
      return next
    })
  }

  // ── Slide operations ──────────────────────────────────────────────────────
  const addSlide = (master = 'content') => {
    setSlidesData((prev) => {
      const slide = newSlide(master)
      const slides = [...prev.slides, slide]
      const next = { ...prev, slides }
      schedule(next)
      setActiveIdx(slides.length - 1)
      const session = treeSessionRef.current
      if (session) {
        const prevOrdKey = slides.length >= 2 ? String(slides.length - 2).padStart(10, '0') : ''
        const ordKey = ordKeyBetween(prevOrdKey, '')
        session.insertSlide(ordKey, slide)
        session.saveLocal()
      }
      return next
    })
  }

  const duplicateSlide = (idx) => {
    setSlidesData((prev) => {
      const original = prev.slides[idx]
      const copy = { ...original, id: crypto.randomUUID() }
      const slides = [
        ...prev.slides.slice(0, idx + 1),
        copy,
        ...prev.slides.slice(idx + 1),
      ]
      const next = { ...prev, slides }
      schedule(next)
      setActiveIdx(idx + 1)
      const session = treeSessionRef.current
      if (session) {
        const ordKey = ordKeyBetween(
          String(idx).padStart(10, '0'),
          String(idx + 1).padStart(10, '0')
        )
        session.insertSlide(ordKey, copy)
        session.saveLocal()
      }
      return next
    })
  }

  const deleteSlide = (idx) => {
    setSlidesData((prev) => {
      if (prev.slides.length === 1) return prev
      const slide = prev.slides[idx]
      const slides = prev.slides.filter((_, i) => i !== idx)
      const next = { ...prev, slides }
      schedule(next)
      setActiveIdx(Math.min(idx, slides.length - 1))
      const session = treeSessionRef.current
      if (session) {
        session.deleteSlide(slide.id)
        session.saveLocal()
      }
      return next
    })
  }

  const moveSlide = (idx, dir) => {
    const newIdx = idx + dir
    if (newIdx < 0 || newIdx >= slidesData.slides.length) return
    setSlidesData((prev) => {
      const slides = [...prev.slides];
      [slides[idx], slides[newIdx]] = [slides[newIdx], slides[idx]]
      const next = { ...prev, slides }
      schedule(next)
      setActiveIdx(newIdx)
      const session = treeSessionRef.current
      if (session) {
        const beforeKey = newIdx > 0 ? String(newIdx - 1).padStart(10, '0') : ''
        const afterKey  = newIdx < slides.length - 1 ? String(newIdx + 1).padStart(10, '0') : ''
        const newOrdKey = ordKeyBetween(beforeKey, afterKey)
        session.moveSlide(slides[newIdx].id, newOrdKey)
        session.saveLocal()
      }
      return next
    })
  }

  // Drag-drop reorder
  const handleSlideDropped = () => {
    if (dragSlideIdx === null || dragOverIdx === null || dragSlideIdx === dragOverIdx) {
      setDragSlideIdx(null); setDragOverIdx(null); return
    }
    setSlidesData((prev) => {
      const slides = [...prev.slides]
      const [item] = slides.splice(dragSlideIdx, 1)
      slides.splice(dragOverIdx, 0, item)
      const next = { ...prev, slides }
      schedule(next)
      setActiveIdx(dragOverIdx)
      return next
    })
    setDragSlideIdx(null); setDragOverIdx(null)
  }

  const updateMeta = (key, value) => {
    const next = { ...slidesData, [key]: value }
    setSlidesData(next)
    schedule(next)
  }

  // ── Theme application ─────────────────────────────────────────────────────
  const applyTheme = ({ themeId, customTheme }) => {
    const theme = getTheme(themeId)
    const next = {
      ...slidesData,
      themeId,
      theme: theme.revealTheme,
      customTheme: customTheme || null,
    }
    setSlidesData(next)
    schedule(next)
  }

  // ── Master save ───────────────────────────────────────────────────────────
  const saveMasters = (masters) => {
    const next = { ...slidesData, masters }
    setSlidesData(next)
    schedule(next)
  }

  // ── Export (server PDF or client PPTX) ───────────────────────────────────
  const handleServerPdfExport = async () => {
    if (!id) {
      // Fallback to client-side print PDF if not saved.
      exportSlidesToPdf(title)
      return
    }
    try {
      const res = await fetch(`/api/slides/${id}/export?format=pdf`, { credentials: 'include' })
      if (!res.ok) throw new Error('Server PDF failed')
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url; a.download = `${title}.pdf`; a.click()
      setTimeout(() => URL.revokeObjectURL(url), 5000)
    } catch {
      // Fallback.
      exportSlidesToPdf(title)
    }
  }

  // ── Speaker notes print ───────────────────────────────────────────────────
  const handlePrintNotes = () => {
    const printWindow = window.open('', '_blank', 'width=800,height=600')
    if (!printWindow) return
    const slidesHtml = slidesData.slides.map((slide, i) => `
      <div style="page-break-after:always;padding:20px;border-bottom:2px solid #eee">
        <h2 style="font-size:18px">${i + 1}. ${slide.title || 'Untitled'}</h2>
        <div style="background:#f5f5f5;padding:12px;border-radius:4px;margin:8px 0;font-size:12px">
          ${sanitize(slide.content)}
        </div>
        <div style="margin-top:12px">
          <strong style="font-size:11px;text-transform:uppercase;color:#666">Notes</strong>
          <p style="font-size:13px;white-space:pre-wrap">${slide.notes || '(no notes)'}</p>
        </div>
      </div>
    `).join('')
    printWindow.document.write(`<!DOCTYPE html><html><head><title>${title} — Notes</title>
      <style>body{font-family:Georgia,serif;margin:0;padding:0}</style>
    </head><body>${slidesHtml}</body></html>`)
    printWindow.document.close()
    printWindow.print()
  }

  if (!editor) {
    return (
      <div className="flex-1 flex items-center justify-center bg-bg">
        <Loader2 className="animate-spin text-accent" size={22} />
      </div>
    )
  }

  const statusInfo = (() => {
    if (saving)  return { text: 'Saving',  tone: 'muted',   icon: Loader2,       spin: true  }
    if (saved)   return { text: 'Saved',   tone: 'success', icon: Check,         spin: false }
    return         { text: 'Unsaved', tone: 'muted',   icon: Circle,        spin: false }
  })()
  const StatusIcon = statusInfo.icon

  const currentTheme = slidesData.customTheme
    ? { ...getTheme(slidesData.themeId || 'obsidian'), ...slidesData.customTheme }
    : getTheme(slidesData.themeId || 'obsidian')

  return (
    <div className="flex-1 flex flex-col overflow-hidden bg-bg">
      {/* ── Top bar ──────────────────────────────────────────────────────── */}
      <Topbar
        leading={
          <Tooltip label="Back to Slides">
            <IconButton size="sm" onClick={() => navigate('/slides')}>
              <ArrowLeft size={15} />
            </IconButton>
          </Tooltip>
        }
        title={
          <input
            value={title}
            onChange={(e) => { setTitle(e.target.value); setSaved(false) }}
            placeholder="Untitled presentation"
            aria-label="Presentation title"
            className={[
              'flex-1 min-w-0 text-sm font-semibold tracking-tightish',
              'bg-transparent border border-transparent rounded-sm px-2 py-1',
              'text-ink placeholder:text-ink-faint',
              'hover:border-line focus:border-line-strong focus:bg-paper',
              'transition-[border-color,background] duration-fast ease-out outline-none',
            ].join(' ')}
          />
        }
        meta={
          <span
            className={[
              'inline-flex items-center gap-1.5 px-1.5 py-0.5 rounded-sm',
              statusInfo.tone === 'success' ? 'text-success' :
              statusInfo.tone === 'danger'  ? 'text-danger' : 'text-ink-faint',
            ].join(' ')}
          >
            <StatusIcon size={11} className={statusInfo.spin ? 'animate-spin' : ''} />
            {statusInfo.text}
          </span>
        }
        actions={
          <>
            {/* New from Template */}
            <Tooltip label="New from Template">
              <IconButton size="sm" onClick={() => setShowTemplateGallery(true)} aria-label="New from template">
                <FileText size={14} />
              </IconButton>
            </Tooltip>
            {/* Theme gallery */}
            <Tooltip label="Theme Gallery">
              <IconButton size="sm" onClick={() => setShowThemeGallery(true)} aria-label="Theme gallery">
                <Palette size={14} />
              </IconButton>
            </Tooltip>
            {/* Master slide editor */}
            <Tooltip label="Master Slides (View → Master)">
              <IconButton size="sm" onClick={() => setShowMasterEditor(true)} aria-label="Master slide editor">
                <Layout size={14} />
              </IconButton>
            </Tooltip>
            <Tooltip label="Comments">
              <IconButton size="sm" active={showComments} onClick={() => setShowComments((v) => !v)}>
                <MessageSquare size={14} />
              </IconButton>
            </Tooltip>
            {/* Present */}
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setPresenting(!presenting)}
              aria-pressed={presenting}
            >
              {presenting ? <><EyeOff size={13} /> Edit</> : <><Play size={13} /> Present</>}
            </Button>
            {/* Presenter view */}
            <Tooltip label="Presenter view (⌘⇧↵)">
              <IconButton
                size="sm"
                onClick={() => openPresenter(activeIdx)}
                aria-label="Open presenter view"
              >
                <Monitor size={14} />
              </IconButton>
            </Tooltip>
            {/* Export menu */}
            <div className="relative group">
              <button
                type="button"
                aria-haspopup="menu"
                className={[
                  'inline-flex items-center gap-1.5 h-7 px-2.5 rounded-md',
                  'bg-paper border border-line text-xs font-medium tracking-tightish',
                  'text-ink-muted hover:border-line-strong hover:text-ink',
                  'transition-colors duration-fast ease-out',
                  'focus-visible:outline-none focus-visible:shadow-focus',
                ].join(' ')}
              >
                <Download size={12} /> Export
                <ChevronDownIcon size={11} className="opacity-60" />
              </button>
              <div
                role="menu"
                className={[
                  'absolute right-0 top-full mt-0.5 w-52 py-1',
                  'bg-paper border border-line rounded-md shadow-e2 z-30 text-sm',
                  'hidden group-hover:block animate-scale-in',
                ].join(' ')}
              >
                <button
                  role="menuitem"
                  onClick={handleServerPdfExport}
                  className="w-full text-left px-3 py-2 hover:bg-accent-tint text-ink-muted flex items-center gap-2"
                >
                  <span className="text-2xs font-bold tracking-eyebrow text-danger w-10">PDF</span>
                  Export as PDF
                </button>
                <button
                  role="menuitem"
                  onClick={() => exportSlidesToPptx(slidesData, title)}
                  className="w-full text-left px-3 py-2 hover:bg-accent-tint text-ink-muted flex items-center gap-2"
                >
                  <span className="text-2xs font-bold tracking-eyebrow text-accent w-10">PPTX</span>
                  Export as PowerPoint
                </button>
                <hr className="border-line my-1" />
                <button
                  role="menuitem"
                  onClick={handlePrintNotes}
                  className="w-full text-left px-3 py-2 hover:bg-accent-tint text-ink-muted flex items-center gap-2"
                >
                  <span className="text-2xs font-bold tracking-eyebrow text-warning w-10">NOTE</span>
                  Print speaker notes
                </button>
              </div>
            </div>
            <Button
              variant="primary"
              size="sm"
              onClick={() => { clearTimeout(saveTimer.current); autosave(slidesData) }}
              disabled={saving}
            >
              {saving ? <Loader2 size={13} className="animate-spin" /> : <Save size={13} />}
              Save
            </Button>
          </>
        }
      />

      {/* ── Presentation preview ──────────────────────────────────────────── */}
      {presenting ? (
        <SlidePreview data={slidesData} onClose={() => setPresenting(false)} />
      ) : (
        <div className="flex-1 flex overflow-hidden slides-layout">
          {/* ── Slide thumbnail sidebar ────────────────────────────────── */}
          <aside className="w-56 flex-shrink-0 bg-clay border-r border-line flex flex-col overflow-hidden">
            {/* Sidebar tabs */}
            <div className="flex border-b border-line">
              {SIDEBAR_TABS.map(({ id: tabId, icon: Icon, label }) => (
                <button
                  key={tabId}
                  type="button"
                  aria-pressed={sidebarTab === tabId}
                  onClick={() => setSidebarTab(tabId)}
                  className={[
                    'flex-1 flex items-center justify-center gap-1 px-2 py-2 text-2xs font-semibold transition-colors border-b-2 -mb-px',
                    sidebarTab === tabId
                      ? 'border-accent text-accent'
                      : 'border-transparent text-ink-muted hover:text-ink',
                  ].join(' ')}
                  title={label}
                >
                  <Icon size={11} />
                  <span className="hidden sm:inline">{label}</span>
                </button>
              ))}
            </div>

            {sidebarTab === 'slides' && (
              <>
                <div className="flex items-center justify-between px-3 h-9 border-b border-line">
                  <span className="text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow">
                    Slides · {slidesData.slides.length}
                  </span>
                  <div className="flex items-center gap-0.5">
                    <Tooltip label="Duplicate slide (⌘D)">
                      <IconButton size="sm" onClick={() => duplicateSlide(activeIdx)} aria-label="Duplicate slide">
                        <Copy size={11} />
                      </IconButton>
                    </Tooltip>
                    <Tooltip label="Add slide (⌘M)">
                      <IconButton size="sm" onClick={() => addSlide()} aria-label="Add slide">
                        <Plus size={13} />
                      </IconButton>
                    </Tooltip>
                  </div>
                </div>

                <div className="flex-1 overflow-y-auto px-2 py-2 space-y-1.5">
                  {slidesData.slides.map((slide, idx) => {
                    const viewers = getSlideViewers(remoteCursors, slide.id)
                    const isActive = idx === activeIdx
                    const isDragTarget = dragOverIdx === idx && dragSlideIdx !== idx
                    return (
                      <div
                        key={slide.id}
                        role="button"
                        tabIndex={0}
                        aria-current={isActive ? 'true' : undefined}
                        draggable
                        onDragStart={() => setDragSlideIdx(idx)}
                        onDragOver={(e) => { e.preventDefault(); setDragOverIdx(idx) }}
                        onDragEnd={handleSlideDropped}
                        onClick={() => setActiveIdx(idx)}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter' || e.key === ' ') {
                            e.preventDefault(); setActiveIdx(idx)
                          }
                        }}
                        className={[
                          'group relative cursor-pointer rounded-md overflow-hidden',
                          'transition-[box-shadow,background,border] duration-fast ease-out',
                          'focus-visible:outline-none focus-visible:shadow-focus',
                          isDragTarget ? 'border-2 border-accent' : '',
                          isActive ? 'bg-paper shadow-e1' : 'bg-paper/60 hover:bg-paper',
                        ].join(' ')}
                      >
                        {isActive && (
                          <span
                            aria-hidden="true"
                            className="absolute left-0 top-0 bottom-0 w-0.5 bg-accent rounded-r-sm"
                          />
                        )}
                        <div
                          className="h-20 flex flex-col items-start justify-start p-2 text-left border border-line rounded-md"
                          style={{ background: slide.background || undefined }}
                        >
                          <div
                            className={[
                              'text-2xs font-semibold truncate w-full tracking-tightish',
                              slide.background ? 'text-white' : 'text-ink',
                            ].join(' ')}
                          >
                            {slide.title || `Slide ${idx + 1}`}
                          </div>
                          <div
                            className={[
                              'text-[10px] mt-1 w-full overflow-hidden line-clamp-3 leading-snug',
                              slide.background ? 'text-white/70' : 'text-ink-faint',
                            ].join(' ')}
                            dangerouslySetInnerHTML={{ __html: sanitize(slide.content) }}
                          />
                          {/* Layout badge */}
                          {slide.master && slide.master !== 'content' && (
                            <div className="mt-1">
                              <span className="text-[8px] px-1 rounded-sm bg-accent-tint text-accent font-semibold uppercase">
                                {slide.master}
                              </span>
                            </div>
                          )}
                        </div>
                        <div
                          className={[
                            'absolute top-1 left-1.5 text-[9px] font-semibold tracking-eyebrow uppercase px-1 rounded-sm',
                            slide.background ? 'text-white/80 bg-black/30' : 'text-ink-faint bg-bg-elev2',
                          ].join(' ')}
                        >
                          {String(idx + 1).padStart(2, '0')}
                        </div>
                        {/* Drag handle */}
                        <div className="absolute top-1 right-8 hidden group-hover:flex items-center text-ink-faint">
                          <GripVertical size={9} className="cursor-grab" />
                        </div>
                        {/* Remote viewer badges */}
                        {viewers.length > 0 && (
                          <div className="absolute bottom-1 left-1.5 flex gap-0.5">
                            {viewers.map((v) => (
                              <span
                                key={v.accountId}
                                title={v.displayName}
                                className="flex items-center justify-center rounded-pill text-white font-bold select-none"
                                style={{ background: v.color, width: 14, height: 14, fontSize: 8 }}
                              >
                                {(v.displayName || '?')[0].toUpperCase()}
                              </span>
                            ))}
                          </div>
                        )}
                        <div className="absolute top-1 right-1 hidden group-hover:flex gap-0.5">
                          <IconButton
                            size="sm" title="Move up" className="h-5 w-5"
                            onClick={(e) => { e.stopPropagation(); moveSlide(idx, -1) }}
                          ><ChevronUp size={10} /></IconButton>
                          <IconButton
                            size="sm" title="Move down" className="h-5 w-5"
                            onClick={(e) => { e.stopPropagation(); moveSlide(idx, 1) }}
                          ><ChevronDown size={10} /></IconButton>
                          <IconButton
                            size="sm" title="Duplicate" className="h-5 w-5"
                            onClick={(e) => { e.stopPropagation(); duplicateSlide(idx) }}
                          ><Copy size={10} /></IconButton>
                          <IconButton
                            size="sm" title="Delete slide" className="h-5 w-5 hover:text-danger"
                            onClick={(e) => { e.stopPropagation(); deleteSlide(idx) }}
                          ><Trash2 size={10} /></IconButton>
                        </div>
                      </div>
                    )
                  })}
                </div>
              </>
            )}

            {sidebarTab === 'transitions' && activeSlide && (
              <div className="flex-1 overflow-y-auto p-3">
                <TransitionPanel
                  slide={activeSlide}
                  onChange={(updated) => {
                    const idx = slidesData.slides.findIndex((s) => s.id === activeSlide.id)
                    if (idx >= 0) updateSlideMeta(idx, { transition: updated.transition, animations: updated.animations })
                  }}
                />
              </div>
            )}

            {/* Meta controls — theme + transition (legacy reveal.js global) */}
            <div className="px-3 py-3 border-t border-line space-y-2.5 bg-clay">
              {/* Current theme badge */}
              <button
                type="button"
                onClick={() => setShowThemeGallery(true)}
                className="w-full flex items-center gap-2 px-2 py-1.5 rounded-md border border-line hover:border-line-strong text-xs text-ink-muted hover:text-ink transition-colors"
              >
                <span
                  className="inline-block w-3 h-3 rounded-full flex-shrink-0"
                  style={{ background: currentTheme.primary }}
                />
                <span className="truncate">{currentTheme.label}</span>
                <Palette size={10} className="ml-auto opacity-50" />
              </button>
              <div>
                <label className="text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow block mb-1">
                  Global transition
                </label>
                <select
                  value={slidesData.transition}
                  onChange={(e) => updateMeta('transition', e.target.value)}
                  className={[
                    'w-full bg-paper text-ink text-xs rounded-sm px-2 h-7',
                    'border border-line hover:border-line-strong',
                    'focus-visible:outline-none focus-visible:shadow-focus',
                  ].join(' ')}
                >
                  {LEGACY_TRANSITIONS.map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
              </div>
              {/* Master assignment for current slide */}
              <div>
                <label className="text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow block mb-1">
                  Slide layout
                </label>
                <select
                  value={activeSlide?.master || 'content'}
                  onChange={(e) => updateSlideMeta(activeIdx, { master: e.target.value })}
                  className={[
                    'w-full bg-paper text-ink text-xs rounded-sm px-2 h-7',
                    'border border-line hover:border-line-strong',
                    'focus-visible:outline-none focus-visible:shadow-focus',
                  ].join(' ')}
                >
                  <option value="title">Title Master</option>
                  <option value="content">Content Master</option>
                  <option value="section">Section Master</option>
                </select>
              </div>
            </div>
          </aside>

          {/* ── Slide editor ─────────────────────────────────────────────── */}
          {activeSlide && (
            <div className="flex-1 flex flex-col overflow-hidden bg-bg">
              <div className="px-6 pt-4 pb-2 bg-paper border-b border-line">
                <input
                  value={activeSlide.title}
                  onChange={(e) => updateSlideField(activeIdx, 'title', e.target.value)}
                  className={[
                    'w-full text-2xl font-bold tracking-tightish font-serif',
                    'bg-transparent border-none outline-none',
                    'text-ink placeholder:text-ink-faint',
                  ].join(' ')}
                  placeholder="Slide title…"
                  aria-label="Slide title"
                />
              </div>

              {/* Formatting toolbar + Insert panel */}
              <div
                className="flex items-center gap-0.5 px-3 h-10 bg-paper border-b border-line flex-wrap"
                role="toolbar"
                aria-label="Slide formatting"
              >
                <Tooltip label="Bold (⌘B)">
                  <IconButton size="sm" active={editor.isActive('bold')}
                    onClick={() => editor.chain().focus().toggleBold().run()} aria-label="Bold">
                    <Bold size={14} />
                  </IconButton>
                </Tooltip>
                <Tooltip label="Italic (⌘I)">
                  <IconButton size="sm" active={editor.isActive('italic')}
                    onClick={() => editor.chain().focus().toggleItalic().run()} aria-label="Italic">
                    <Italic size={14} />
                  </IconButton>
                </Tooltip>
                <Tooltip label="Underline (⌘U)">
                  <IconButton size="sm" active={editor.isActive('underline')}
                    onClick={() => editor.chain().focus().toggleUnderline().run()} aria-label="Underline">
                    <UnderlineIcon size={14} />
                  </IconButton>
                </Tooltip>
                <span className="toolbar-divider" />
                <Tooltip label="Align left">
                  <IconButton size="sm" active={editor.isActive({ textAlign: 'left' })}
                    onClick={() => editor.chain().focus().setTextAlign('left').run()} aria-label="Align left">
                    <AlignLeft size={14} />
                  </IconButton>
                </Tooltip>
                <Tooltip label="Align center">
                  <IconButton size="sm" active={editor.isActive({ textAlign: 'center' })}
                    onClick={() => editor.chain().focus().setTextAlign('center').run()} aria-label="Align center">
                    <AlignCenter size={14} />
                  </IconButton>
                </Tooltip>
                <Tooltip label="Align right">
                  <IconButton size="sm" active={editor.isActive({ textAlign: 'right' })}
                    onClick={() => editor.chain().focus().setTextAlign('right').run()} aria-label="Align right">
                    <AlignRight size={14} />
                  </IconButton>
                </Tooltip>
                <span className="toolbar-divider" />
                <Tooltip label="Bullet list">
                  <IconButton size="sm" active={editor.isActive('bulletList')}
                    onClick={() => editor.chain().focus().toggleBulletList().run()} aria-label="Bullet list">
                    <List size={14} />
                  </IconButton>
                </Tooltip>
                <span className="toolbar-divider" />
                {/* Insert panel */}
                <InsertPanel editor={editor} api={api} />
                <span className="toolbar-divider" />
                {/* Slide background */}
                <label
                  className="toolbar-btn flex items-center gap-1.5 cursor-pointer text-xs px-2"
                  title="Slide background"
                >
                  <span className="text-2xs font-semibold tracking-eyebrow uppercase text-ink-faint">BG</span>
                  <span
                    aria-hidden="true"
                    className="inline-block w-4 h-4 rounded-xs border border-line"
                    style={{ background: activeSlide.background || 'var(--paper)' }}
                  />
                  <input
                    type="color"
                    className="sr-only"
                    value={activeSlide.background || '#1a1a2e'}
                    onChange={(e) => updateSlideField(activeIdx, 'background', e.target.value)}
                    aria-label="Slide background colour"
                  />
                </label>
              </div>

              {/* Slide canvas */}
              <div className="flex-1 overflow-auto px-6 py-8 bg-bg">
                <article
                  className="paper-grain mx-auto bg-paper border border-line rounded-lg shadow-e1 px-12 py-10 animate-fade-in"
                  style={{ maxWidth: '900px', minHeight: '420px' }}
                >
                  <EditorContent editor={editor} className="tiptap" />
                </article>
              </div>

              {/* Speaker notes — resizable */}
              <div
                className="bg-warning-bg border-t border-line flex-shrink-0 flex flex-col"
                style={{ height: notesHeight }}
              >
                {/* Resize handle */}
                <div
                  ref={notesResizeRef}
                  onMouseDown={(e) => { e.preventDefault(); isResizingNotes.current = true }}
                  className="h-1 cursor-row-resize bg-transparent hover:bg-warning/30 transition-colors flex-shrink-0"
                  title="Drag to resize notes panel"
                />
                <div className="flex items-center justify-between px-6 py-1 flex-shrink-0">
                  <label
                    htmlFor="slide-speaker-notes"
                    className="flex items-center gap-1.5 text-2xs font-semibold uppercase tracking-eyebrow text-warning"
                  >
                    <StickyNote size={11} />
                    Speaker notes
                  </label>
                  <button
                    type="button"
                    onClick={handlePrintNotes}
                    className="text-2xs text-ink-faint hover:text-ink transition-colors"
                    title="Print notes"
                  >
                    Print notes PDF
                  </button>
                </div>
                <textarea
                  id="slide-speaker-notes"
                  value={activeSlide.notes}
                  onChange={(e) => updateSlideField(activeIdx, 'notes', e.target.value)}
                  className={[
                    'flex-1 w-full text-sm bg-transparent border-none outline-none resize-none px-6',
                    'text-ink-muted placeholder:text-ink-faint',
                  ].join(' ')}
                  placeholder="Notes for the presenter…"
                />
              </div>
            </div>
          )}

          {/* Comments panel */}
          {showComments && (
            <CommentsPanel
              fileId={id}
              anchorCtx={activeSlide ? { type: 'slide', slide_id: activeSlide.id, snapshot: activeSlide.title || `Slide ${activeIdx + 1}` } : null}
              onClose={() => setShowComments(false)}
            />
          )}
        </div>
      )}

      {/* ── Modals ──────────────────────────────────────────────────────── */}
      {showThemeGallery && (
        <ThemeGallery
          currentThemeId={slidesData.themeId || 'obsidian'}
          customTheme={slidesData.customTheme}
          onApply={applyTheme}
          onClose={() => setShowThemeGallery(false)}
        />
      )}
      {showMasterEditor && (
        <MasterSlideEditor
          masters={slidesData.masters}
          onSave={saveMasters}
          onClose={() => setShowMasterEditor(false)}
        />
      )}
      {showTemplateGallery && (
        <TemplateGallery
          onApply={(tplData) => {
            setSlidesData((prev) => ({ ...prev, ...tplData }))
            schedule({ ...slidesData, ...tplData })
            setActiveIdx(0)
          }}
          onClose={() => setShowTemplateGallery(false)}
        />
      )}
    </div>
  )
}
