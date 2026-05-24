import { useEffect, useRef, useState, useCallback, lazy, Suspense } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { Workbook } from '@fortune-sheet/react'
import '@fortune-sheet/react/dist/index.css'
import {
  ArrowLeft, Save, Loader2, Download, Upload, AlertCircle, MessageSquare,
  Check, Circle, ChevronDown, BarChart2, Filter, Table2, Tag, Sliders, Keyboard,
} from 'lucide-react'
import { useFilesStore, getSaveState, onSaveStateChange } from '../../store/filesStore'
import { api } from '../../lib/api'
import { readDraft, clearDraft } from '../../lib/draftStore'
import { exportSheetsToXlsx, exportSheetsToCsv } from './sheetsExport'
import { importCSVFile } from './csvImport'
import { GridSession, getGridReplicaId } from '../../lib/crdt/grid.js'
import CommentsPanel from '../../components/CommentsPanel'
import { useLiveCursors } from '../../lib/useLiveCursors.js'
import { SheetsCursorLayer } from '../../components/RemoteCursors.jsx'
import { Button, IconButton, Tooltip, Topbar } from '../../components/ui'
import { useSheetKeyboardShortcuts, KeyboardShortcutsHelp, useShortcutsHelp } from './KeyboardShortcuts.jsx'

// Side panels — lazily loaded so they don't bloat the initial bundle.
const PivotPanel              = lazy(() => import('./PivotPanel.jsx'))
const FilterPanel             = lazy(() => import('./FilterPanel.jsx'))
const ConditionalFormatPanel  = lazy(() => import('./ConditionalFormatPanel.jsx'))
const ChartWizard             = lazy(() => import('./ChartWizard.jsx'))
const NamedRangesPanel        = lazy(() => import('./NamedRangesPanel.jsx'))

const RETRY_DELAY_MS  = 4000
const AUTOSAVE_DELAY_MS = 3000

export default function SheetsEditor() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { files, saveFileWithDraft, markDirty } = useFilesStore()
  const [file, setFile] = useState(files.find((f) => f.id === id))
  const [title, setTitle] = useState(file?.name || 'Untitled Sheet')
  const [data, setData] = useState(file?.content || [{ name: 'Sheet1', celldata: [], config: {} }])
  const [saveStatus, setSaveStatus] = useState(getSaveState(id))
  const [draft, setDraft] = useState(null)
  const [retryCount, setRetryCount] = useState(0)

  // Panel visibility state
  const [showComments,      setShowComments]      = useState(false)
  const [showPivot,         setShowPivot]         = useState(false)
  const [showFilter,        setShowFilter]        = useState(false)
  const [showCondFormat,    setShowCondFormat]    = useState(false)
  const [showNamedRanges,   setShowNamedRanges]   = useState(false)
  const [showChartWizard,   setShowChartWizard]   = useState(false)

  const saveTimer   = useRef(null)
  const retryTimer  = useRef(null)
  const titleRef    = useRef(title)
  titleRef.current  = title
  const dataRef     = useRef(data)
  dataRef.current   = data
  const gridSessionRef = useRef(null)
  const workbookWrapRef = useRef(null)
  const importInputRef  = useRef(null)

  // ── Keyboard shortcuts ──────────────────────────────────────────────────────
  const { show: showShortcutsHelp, openHelp, closeHelp } = useShortcutsHelp()
  useSheetKeyboardShortcuts({
    containerRef: workbookWrapRef,
    data,
    onChange:    setData,
    onShowHelp:  openHelp,
  })

  // ── CRDT collaboration (OFFICE-23) ──────────────────────────────────────────
  useEffect(() => {
    if (!id) return
    const replicaId = getGridReplicaId()
    const session = new GridSession({ sessionId: id, replicaId, fabricClient: null })
    gridSessionRef.current = session
    session.requestSnapshot()

    const onRemote = () => {
      const crdtCells = session.cells()
      if (crdtCells.length === 0) return
      setData((prev) => {
        const sheets = prev.map((sheet, idx) => {
          if (idx !== 0) return sheet
          const existing = new Map((sheet.celldata || []).map((c) => [`${c.r}_${c.c}`, c]))
          for (const { r, c, v } of crdtCells) {
            const key = `${r}_${c}`
            const ex = existing.get(key)
            if (!ex || (typeof ex.v === 'object' ? ex.v?.v : ex.v) !== v) {
              existing.set(key, { r, c, v: { v, m: v, ct: { fa: 'General', t: 'n' } } })
            }
          }
          return { ...sheet, celldata: [...existing.values()] }
        })
        return sheets
      })
      markDirty(id)
    }

    session.addEventListener('remoteOp', onRemote)
    return () => {
      session.removeEventListener('remoteOp', onRemote)
      session.destroy()
      gridSessionRef.current = null
    }
  }, [id]) // eslint-disable-line

  useEffect(() => {
    const unsub = onSaveStateChange(id, (state) => setSaveStatus({ ...state }))
    return unsub
  }, [id])

  useEffect(() => {
    if (!file && id) {
      api.getFile(id).then((f) => {
        setFile(f)
        setTitle(f.name)
        setData(f.content || [{ name: 'Sheet1', celldata: [], config: {} }])
      }).catch(() => navigate('/sheets'))
    }
  }, [id])

  useEffect(() => {
    if (!id) return
    readDraft(id).then((d) => { if (d && d.ts) setDraft(d) })
  }, [id])

  // ── Live cursors (OFFICE-25) ────────────────────────────────────────────────
  const { remoteCursors, broadcastSheetCursor } = useLiveCursors({
    fabric: null, localIdentity: null, color: 'var(--teal-500)',
  })

  const getCellRect = useCallback((row, col) => {
    const container = workbookWrapRef.current
    if (!container) return null
    try {
      const tbody = container.querySelector('.luckysheet-cell-main tbody')
      if (!tbody) return null
      const tr = tbody.querySelectorAll('tr')[row]
      if (!tr) return null
      const td = tr.querySelectorAll('td')[col + 1]
      if (!td) return null
      const containerRect = container.getBoundingClientRect()
      const tdRect = td.getBoundingClientRect()
      return {
        top:    tdRect.top    - containerRect.top,
        left:   tdRect.left   - containerRect.left,
        width:  tdRect.width,
        height: tdRect.height,
      }
    } catch { return null }
  }, [])

  // ── Save / autosave ─────────────────────────────────────────────────────────
  const doSave = useCallback(async (contentOverride, retryNum = 0) => {
    if (!id) return
    const content = contentOverride !== undefined ? contentOverride : dataRef.current
    try {
      await saveFileWithDraft(id, titleRef.current, content)
      setRetryCount(0)
    } catch {
      if (retryNum < 3) {
        const delay = RETRY_DELAY_MS * (retryNum + 1)
        retryTimer.current = setTimeout(() => {
          setRetryCount(retryNum + 1)
          doSave(undefined, retryNum + 1)
        }, delay)
      }
    }
  }, [id, saveFileWithDraft])

  const handleChange = (newData) => {
    setData(newData)
    markDirty(id)
    clearTimeout(saveTimer.current)
    clearTimeout(retryTimer.current)
    setRetryCount(0)
    if (Array.isArray(newData) && newData[0]?.celldata?.length) {
      const last = newData[0].celldata[newData[0].celldata.length - 1]
      if (last) broadcastSheetCursor(last.r, last.c)
    }
    // CRDT ops
    const session = gridSessionRef.current
    if (session && Array.isArray(newData) && newData[0]?.celldata) {
      for (const cell of newData[0].celldata) {
        const row = cell.r
        const col = cell.c
        const val = typeof cell.v === 'object'
          ? (cell.v?.v ?? cell.v?.m ?? '')
          : (cell.v ?? '')
        const existing = session.cells().find((c) => c.r === row && c.c === col)
        if (!existing || existing.v !== String(val)) {
          if (val === '' || val === null || val === undefined) {
            session.clearCell(row, col)
          } else {
            session.setCell(row, col, String(val))
          }
        }
      }
      session.saveLocal()
    }
    saveTimer.current = setTimeout(() => doSave(newData), AUTOSAVE_DELAY_MS)
  }

  const handleSave = () => {
    clearTimeout(saveTimer.current)
    clearTimeout(retryTimer.current)
    setRetryCount(0)
    doSave(dataRef.current)
  }

  const handleTitleChange = (newTitle) => {
    setTitle(newTitle)
    markDirty(id)
    clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => doSave(dataRef.current), 1500)
  }

  const handleRestoreDraft  = () => { if (!draft) return; setData(draft.content); if (draft.name) setTitle(draft.name); setDraft(null); markDirty(id) }
  const handleDiscardDraft  = () => { clearDraft(id); setDraft(null) }

  // ── Import CSV ──────────────────────────────────────────────────────────────
  const handleImportCSV = async (e) => {
    const file = e.target.files?.[0]
    if (!file) return
    try {
      const sheet = await importCSVFile(file)
      // Append as a new sheet.
      setData((prev) => {
        const next = [...prev, sheet]
        markDirty(id)
        clearTimeout(saveTimer.current)
        saveTimer.current = setTimeout(() => doSave(next), AUTOSAVE_DELAY_MS)
        return next
      })
    } catch (err) {
      console.error('CSV import failed:', err)
    }
    e.target.value = ''
  }

  // ── Import XLSX (server-side) ───────────────────────────────────────────────
  const handleImportXLSX = async (e) => {
    const file = e.target.files?.[0]
    if (!file || !id) return
    const form = new FormData()
    form.append('file', file)
    try {
      const res = await fetch(`/api/sheets/${id}/import`, { method: 'POST', body: form })
      if (res.ok) {
        // Reload file content from server.
        const updated = await api.getFile(id)
        setData(updated.content || data)
      } else {
        console.error('XLSX import failed:', await res.text())
      }
    } catch (err) {
      console.error('XLSX import error:', err)
    }
    e.target.value = ''
  }

  // ── Filter view apply ───────────────────────────────────────────────────────
  const handleFilterApply = useCallback((hiddenRows) => {
    setData((prev) => prev.map((sheet, idx) => {
      if (idx !== 0) return sheet
      const rowhidden = {}
      for (const r of hiddenRows) rowhidden[r] = 1
      return { ...sheet, config: { ...sheet.config, rowhidden } }
    }))
  }, [])

  // ── Save status display ─────────────────────────────────────────────────────
  const statusInfo = (() => {
    switch (saveStatus.status) {
      case 'saving': return { text: 'Saving',  tone: 'muted',   icon: Loader2,    spin: true  }
      case 'saved':  return { text: 'Saved',   tone: 'success', icon: Check,      spin: false }
      case 'error':  return {
        text: retryCount > 0 ? `Retrying ${retryCount}/3` : 'Save failed',
        tone: 'danger', icon: AlertCircle, spin: false,
      }
      case 'dirty': return { text: 'Unsaved', tone: 'muted',   icon: Circle,     spin: false }
      default:      return null
    }
  })()
  const StatusIcon = statusInfo?.icon

  // Only one side panel open at a time (except comments).
  const closeAllPanels = () => {
    setShowPivot(false); setShowFilter(false)
    setShowCondFormat(false); setShowNamedRanges(false)
  }
  const togglePanel = (setter) => () => {
    setter((v) => {
      if (!v) closeAllPanels()
      return !v
    })
  }

  return (
    <div className="flex-1 flex flex-col overflow-hidden bg-bg">
      {/* Hidden file inputs for import */}
      <input ref={importInputRef} type="file" className="hidden" accept=".csv,.xlsx" onChange={(e) => {
        const name = e.target.files?.[0]?.name || ''
        if (name.endsWith('.xlsx')) handleImportXLSX(e)
        else handleImportCSV(e)
      }} />

      {/* Draft-restore banner */}
      {draft && (
        <div className="flex items-center gap-3 px-4 py-2 bg-warning-bg border-b border-line text-xs text-warning animate-fade-in">
          <AlertCircle size={14} className="flex-shrink-0" />
          <span className="flex-1 text-ink-muted">Unsaved changes from a previous session were found.</span>
          <Button variant="primary"   size="sm" onClick={handleRestoreDraft}>Restore</Button>
          <Button variant="secondary" size="sm" onClick={handleDiscardDraft}>Discard</Button>
        </div>
      )}

      {/* Top bar */}
      <Topbar
        leading={
          <Tooltip label="Back to Sheets">
            <IconButton size="sm" onClick={() => navigate('/sheets')}><ArrowLeft size={15} /></IconButton>
          </Tooltip>
        }
        title={
          <input
            value={title}
            onChange={(e) => handleTitleChange(e.target.value)}
            placeholder="Untitled sheet"
            aria-label="Sheet title"
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
          statusInfo && (
            <span
              className={[
                'inline-flex items-center gap-1.5 px-1.5 py-0.5 rounded-sm',
                statusInfo.tone === 'success' ? 'text-success' :
                statusInfo.tone === 'danger'  ? 'text-danger'  : 'text-ink-faint',
              ].join(' ')}
              title={saveStatus.error || ''}
            >
              {StatusIcon && <StatusIcon size={11} className={statusInfo.spin ? 'animate-spin' : ''} />}
              {statusInfo.text}
            </span>
          )
        }
        actions={
          <>
            {/* Toolbar shortcut buttons */}
            <Tooltip label="Pivot table (Insert → Pivot table)">
              <IconButton size="sm" active={showPivot} onClick={togglePanel(setShowPivot)}>
                <Table2 size={14} />
              </IconButton>
            </Tooltip>
            <Tooltip label="Filter views (Data → Filter views)">
              <IconButton size="sm" active={showFilter} onClick={togglePanel(setShowFilter)}>
                <Filter size={14} />
              </IconButton>
            </Tooltip>
            <Tooltip label="Conditional formatting (Format → Conditional formatting)">
              <IconButton size="sm" active={showCondFormat} onClick={togglePanel(setShowCondFormat)}>
                <Sliders size={14} />
              </IconButton>
            </Tooltip>
            <Tooltip label="Named ranges (Data → Named ranges)">
              <IconButton size="sm" active={showNamedRanges} onClick={togglePanel(setShowNamedRanges)}>
                <Tag size={14} />
              </IconButton>
            </Tooltip>
            <Tooltip label="Insert chart">
              <IconButton size="sm" onClick={() => setShowChartWizard(true)}>
                <BarChart2 size={14} />
              </IconButton>
            </Tooltip>
            <Tooltip label="Keyboard shortcuts (⌘/)">
              <IconButton size="sm" onClick={openHelp}>
                <Keyboard size={14} />
              </IconButton>
            </Tooltip>

            <Tooltip label="Comments">
              <IconButton size="sm" active={showComments} onClick={() => setShowComments((v) => !v)}>
                <MessageSquare size={14} />
              </IconButton>
            </Tooltip>

            {/* Import menu */}
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
                <Upload size={12} /> Import
                <ChevronDown size={11} className="opacity-60" />
              </button>
              <div
                role="menu"
                className={[
                  'absolute right-0 top-full mt-0.5 w-44 py-1',
                  'bg-paper border border-line rounded-md shadow-e2 z-30 text-sm',
                  'hidden group-hover:block animate-scale-in',
                ].join(' ')}
              >
                <button
                  role="menuitem"
                  onClick={() => { if (importInputRef.current) { importInputRef.current.accept = '.csv'; importInputRef.current.click() } }}
                  className="w-full text-left px-3 py-2 hover:bg-accent-tint text-ink-muted flex items-center gap-2"
                >
                  <span className="text-2xs font-bold tracking-eyebrow text-ink-faint w-10">CSV</span>
                  CSV file
                </button>
                <button
                  role="menuitem"
                  onClick={() => { if (importInputRef.current) { importInputRef.current.accept = '.xlsx'; importInputRef.current.click() } }}
                  className="w-full text-left px-3 py-2 hover:bg-accent-tint text-ink-muted flex items-center gap-2"
                >
                  <span className="text-2xs font-bold tracking-eyebrow text-accent w-10">XLSX</span>
                  Excel workbook
                </button>
              </div>
            </div>

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
                <ChevronDown size={11} className="opacity-60" />
              </button>
              <div
                role="menu"
                className={[
                  'absolute right-0 top-full mt-0.5 w-44 py-1',
                  'bg-paper border border-line rounded-md shadow-e2 z-30 text-sm',
                  'hidden group-hover:block animate-scale-in',
                ].join(' ')}
              >
                <button
                  role="menuitem"
                  onClick={() => exportSheetsToXlsx(data, title)}
                  className="w-full text-left px-3 py-2 hover:bg-accent-tint text-ink-muted flex items-center gap-2"
                >
                  <span className="text-2xs font-bold tracking-eyebrow text-accent w-10">XLSX</span>
                  Excel workbook
                </button>
                <button
                  role="menuitem"
                  onClick={() => exportSheetsToCsv(data, title)}
                  className="w-full text-left px-3 py-2 hover:bg-accent-tint text-ink-muted flex items-center gap-2"
                >
                  <span className="text-2xs font-bold tracking-eyebrow text-ink-faint w-10">CSV</span>
                  Current sheet (CSV)
                </button>
                {id && (
                  <a
                    role="menuitem"
                    href={`/api/sheets/${id}/export?format=xlsx`}
                    download
                    className="w-full text-left px-3 py-2 hover:bg-accent-tint text-ink-muted flex items-center gap-2"
                  >
                    <span className="text-2xs font-bold tracking-eyebrow text-accent w-10">SRV</span>
                    Server XLSX
                  </a>
                )}
              </div>
            </div>

            <Button
              variant="primary"
              size="sm"
              onClick={handleSave}
              disabled={saveStatus.status === 'saving'}
            >
              {saveStatus.status === 'saving' ? <Loader2 size={13} className="animate-spin" /> : <Save size={13} />}
              Save
            </Button>
          </>
        }
      />

      {/* Main content area: workbook + side panels */}
      <div className="flex-1 flex overflow-hidden bg-bg">
        <div
          className="flex-1 overflow-hidden relative bg-paper sheets-themed"
          ref={workbookWrapRef}
        >
          <Workbook
            data={data}
            onChange={handleChange}
          />
          <SheetsCursorLayer remoteCursors={remoteCursors} getCellRect={getCellRect} />
        </div>

        {/* Side panels — one open at a time */}
        <Suspense fallback={null}>
          {showPivot && (
            <PivotPanel
              data={data}
              onClose={() => setShowPivot(false)}
              onInsert={(next) => { handleChange(next) }}
            />
          )}
          {showFilter && (
            <FilterPanel
              data={data}
              onClose={() => setShowFilter(false)}
              onApply={handleFilterApply}
            />
          )}
          {showCondFormat && (
            <ConditionalFormatPanel
              data={data}
              onClose={() => setShowCondFormat(false)}
              onChange={(next) => { handleChange(next) }}
            />
          )}
          {showNamedRanges && (
            <NamedRangesPanel
              data={data}
              onClose={() => setShowNamedRanges(false)}
              onChange={(next) => { handleChange(next) }}
            />
          )}
        </Suspense>

        {/* Comments panel */}
        {showComments && (
          <CommentsPanel
            fileId={id}
            anchorCtx={{ type: 'cell', sheet: 'Sheet1', row: 0, col: 0, snapshot: '' }}
            onClose={() => setShowComments(false)}
          />
        )}
      </div>

      {/* Modals */}
      <Suspense fallback={null}>
        {showChartWizard && (
          <ChartWizard
            data={data}
            onClose={() => setShowChartWizard(false)}
            onChange={(next) => { handleChange(next); setShowChartWizard(false) }}
          />
        )}
      </Suspense>

      {/* Keyboard shortcuts help */}
      {showShortcutsHelp && <KeyboardShortcutsHelp onClose={closeHelp} />}
    </div>
  )
}
