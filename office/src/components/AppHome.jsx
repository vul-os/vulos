import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  Plus, Search, LayoutGrid, List, MoreVertical, Clock,
  Trash2, Pencil, ArrowUpRight, FileText, Table2, Presentation,
  HardDrive, Loader2, RefreshCw, FileSearch, Upload,
} from 'lucide-react'
import { useFilesStore } from '../store/filesStore'
import { useLocalFilesStore } from '../store/localFilesStore'
import NewFileModal from './NewFileModal'
import { importFromUrl, importFile } from '../lib/importFile'
import { Button, IconButton, Input, Card, Tooltip } from './ui'

// ─── Token-aligned config ─────────────────────────────────────────────────────
const CONFIG = {
  doc: {
    label: 'Documents', singularLabel: 'Document',
    icon: FileText,
    iconCn: 'text-accent',       bgCn: 'bg-accent-tint',
    route: 'docs', emptyMsg: 'No documents yet',
    localExts: ['.doc', '.docx', '.txt', '.md', '.rtf', '.odt'],
    extLabel: 'docx, doc, txt, md',
    importExts: '.doc,.docx,.txt,.md,.rtf,.html',
    canCreate: true,
  },
  sheet: {
    label: 'Spreadsheets', singularLabel: 'Spreadsheet',
    icon: Table2,
    iconCn: 'text-success',      bgCn: 'bg-success-bg',
    route: 'sheets', emptyMsg: 'No spreadsheets yet',
    localExts: ['.xls', '.xlsx', '.csv', '.ods'],
    extLabel: 'xlsx, xls, csv',
    importExts: '.xls,.xlsx,.csv,.tsv',
    canCreate: true,
  },
  slide: {
    label: 'Presentations', singularLabel: 'Presentation',
    icon: Presentation,
    iconCn: 'text-warning',      bgCn: 'bg-warning-bg',
    route: 'slides', emptyMsg: 'No presentations yet',
    localExts: ['.ppt', '.pptx', '.odp'],
    extLabel: 'pptx, ppt',
    importExts: '.pptx,.ppt',
    canCreate: true,
  },
  pdf: {
    label: 'PDFs', singularLabel: 'PDF',
    icon: FileSearch,
    iconCn: 'text-danger',       bgCn: 'bg-danger-bg',
    route: 'pdf', emptyMsg: 'No PDFs yet',
    localExts: ['.pdf'],
    extLabel: 'pdf',
    importExts: '.pdf',
    canCreate: false,
  },
}

function formatDate(s) {
  const diff = Date.now() - new Date(s).getTime()
  if (diff < 60000)    return 'just now'
  if (diff < 3600000)  return `${Math.floor(diff / 60000)}m ago`
  if (diff < 86400000) return `${Math.floor(diff / 3600000)}h ago`
  if (diff < 604800000)return `${Math.floor(diff / 86400000)}d ago`
  return new Date(s).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

function formatSize(b) {
  if (b < 1024)         return b + ' B'
  if (b < 1024 * 1024)  return (b / 1024).toFixed(0) + ' KB'
  return (b / (1024 * 1024)).toFixed(1) + ' MB'
}

export default function AppHome({ type }) {
  const cfg = CONFIG[type]
  const Icon = cfg.icon
  const navigate = useNavigate()
  const { files, loading, fetchFiles, deleteFile, renameFile } = useFilesStore()
  const { files: localFiles, loading: localLoading, scanned, scan } = useLocalFilesStore()
  const [showNew, setShowNew] = useState(false)
  const [search, setSearch] = useState('')
  const [viewMode, setViewMode] = useState('grid')
  const [menuOpen, setMenuOpen] = useState(null)
  const [renaming, setRenaming] = useState(null)
  const [renameValue, setRenameValue] = useState('')
  const [importing, setImporting] = useState(null)
  const fileInputRef = useRef(null)

  const handleImportFile = async (e) => {
    const file = e.target.files[0]
    if (!file) return
    e.target.value = ''
    setImporting('__file__')
    try {
      await importFile(file, navigate)
    } catch (err) {
      alert(`Could not open ${file.name}: ${err.message}`)
    } finally {
      setImporting(null)
    }
  }

  useEffect(() => { fetchFiles() }, [])
  useEffect(() => { if (!scanned) scan() }, [scanned])

  const myFiles = files
    .filter(f => f.type === type)
    .filter(f => f.name.toLowerCase().includes(search.toLowerCase()))

  const myLocalFiles = localFiles
    .filter(f => cfg.localExts.includes(f.ext))
    .filter(f => f.name.toLowerCase().includes(search.toLowerCase()))

  const openFile = (f) => navigate(`/${cfg.route}/${f.id}`)
  const startRename = (f) => { setRenaming(f.id); setRenameValue(f.name); setMenuOpen(null) }
  const commitRename = async (id) => { if (renameValue.trim()) await renameFile(id, renameValue.trim()); setRenaming(null) }

  const openLocalFile = async (file) => {
    setImporting(file.path)
    try {
      await importFromUrl(file, navigate)
    } catch (e) {
      console.error(e)
      alert(`Could not open ${file.name}: ${e.message}`)
    } finally {
      setImporting(null)
    }
  }

  return (
    <div className="flex-1 overflow-auto bg-bg">
      {/* ── Topbar ── */}
      <div className="sticky top-0 z-10 flex items-center gap-3 px-5 h-11 bg-paper border-b border-line">
        {/* App icon */}
        <div className={`w-7 h-7 rounded-md ${cfg.bgCn} flex items-center justify-center flex-shrink-0`}>
          <Icon size={15} className={cfg.iconCn} />
        </div>
        <h1 className="text-sm font-semibold text-ink tracking-tightish">{cfg.label}</h1>

        {/* Search */}
        <div className="flex-1 max-w-xs mx-2">
          <Input
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search…"
            size="sm"
            leading={<Search size={13} />}
          />
        </div>

        {/* Actions cluster */}
        <div className="ml-auto flex items-center gap-1.5">
          {/* View toggle */}
          <div className="flex items-center gap-0.5 p-0.5 bg-bg-elev2 border border-line rounded-md mr-1">
            <Tooltip label="Grid view" side="bottom">
              <IconButton
                size="sm"
                active={viewMode === 'grid'}
                onClick={() => setViewMode('grid')}
              >
                <LayoutGrid size={13} />
              </IconButton>
            </Tooltip>
            <Tooltip label="List view" side="bottom">
              <IconButton
                size="sm"
                active={viewMode === 'list'}
                onClick={() => setViewMode('list')}
              >
                <List size={13} />
              </IconButton>
            </Tooltip>
          </div>

          <input
            ref={fileInputRef}
            type="file"
            accept={cfg.importExts}
            className="hidden"
            onChange={handleImportFile}
          />
          <Button
            variant="secondary"
            size="sm"
            onClick={() => fileInputRef.current?.click()}
            disabled={importing === '__file__'}
          >
            {importing === '__file__' ? <Loader2 size={13} className="animate-spin" /> : <Upload size={13} />}
            Open file
          </Button>
          {cfg.canCreate && (
            <Button variant="primary" size="sm" onClick={() => setShowNew(true)}>
              <Plus size={13} /> New {cfg.singularLabel}
            </Button>
          )}
        </div>
      </div>

      <div className="max-w-6xl mx-auto px-6 py-6 space-y-8">

        {/* ── Cloud files ── */}
        <section>
          {loading && (
            <div className="flex justify-center py-16">
              <Loader2 size={18} className="animate-spin text-accent" />
            </div>
          )}

          {!loading && myFiles.length === 0 && (
            <div className="flex flex-col items-center justify-center py-20 text-center animate-fade-in">
              <div className={`w-16 h-16 ${cfg.bgCn} rounded-xl flex items-center justify-center mb-4`}>
                <Icon size={28} className={`${cfg.iconCn} opacity-40`} />
              </div>
              <p className="font-serif text-lg text-ink mb-1">
                {search ? 'No results' : cfg.emptyMsg}
              </p>
              <p className="text-sm text-ink-muted mb-6">
                {search
                  ? 'Try a different search term'
                  : `Start fresh with a blank ${cfg.singularLabel.toLowerCase()}`
                }
              </p>
              {!search && (
                <div className="flex items-center gap-2">
                  <Button variant="secondary" size="md" onClick={() => fileInputRef.current?.click()}>
                    <Upload size={14} /> Open File
                  </Button>
                  {cfg.canCreate && (
                    <Button variant="primary" size="md" onClick={() => setShowNew(true)}>
                      <Plus size={14} /> New {cfg.singularLabel}
                    </Button>
                  )}
                </div>
              )}
            </div>
          )}

          {!loading && myFiles.length > 0 && viewMode === 'grid' && (
            <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 gap-4">
              {myFiles.map(file => (
                <FileCard
                  key={file.id}
                  file={file}
                  cfg={cfg}
                  Icon={Icon}
                  renaming={renaming}
                  renameValue={renameValue}
                  setRenaming={setRenaming}
                  setRenameValue={setRenameValue}
                  menuOpen={menuOpen}
                  setMenuOpen={setMenuOpen}
                  onOpen={() => openFile(file)}
                  onRename={() => startRename(file)}
                  onRenameCommit={() => commitRename(file.id)}
                  onDelete={() => { deleteFile(file.id); setMenuOpen(null) }}
                />
              ))}
            </div>
          )}

          {!loading && myFiles.length > 0 && viewMode === 'list' && (
            <FileListTable
              files={myFiles}
              cfg={cfg}
              Icon={Icon}
              renaming={renaming}
              renameValue={renameValue}
              setRenaming={setRenaming}
              setRenameValue={setRenameValue}
              menuOpen={menuOpen}
              setMenuOpen={setMenuOpen}
              onOpen={openFile}
              onRename={startRename}
              onRenameCommit={commitRename}
              onDelete={(id) => { deleteFile(id); setMenuOpen(null) }}
            />
          )}
        </section>

        {/* ── On Your Computer ── */}
        {myLocalFiles.length > 0 && (
          <section>
            <div className="flex items-center justify-between mb-3">
              <div className="flex items-center gap-2">
                <HardDrive size={13} className="text-ink-faint" />
                <p className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
                  On Your Computer
                </p>
                <span className="text-2xs text-ink-faint bg-bg-elev2 border border-line rounded-pill px-2 py-0.5">
                  {myLocalFiles.length}
                </span>
              </div>
              <button
                onClick={() => scan()}
                className="flex items-center gap-1.5 text-2xs text-ink-faint hover:text-ink-muted transition-colors"
              >
                <RefreshCw size={11} className={localLoading ? 'animate-spin' : ''} />
                Rescan
              </button>
            </div>
            <Card>
              {myLocalFiles.map((file, i) => (
                <button
                  key={file.path}
                  onClick={() => openLocalFile(file)}
                  disabled={importing === file.path}
                  className={[
                    'w-full flex items-center gap-3 px-4 py-2.5 text-left group',
                    'hover:bg-accent-tint transition-colors duration-fast ease-out',
                    'disabled:opacity-60',
                    i < myLocalFiles.length - 1 ? 'border-b border-line' : '',
                  ].join(' ')}
                >
                  <div className={`w-7 h-7 ${cfg.bgCn} rounded-md flex items-center justify-center flex-shrink-0`}>
                    <Icon size={13} className={cfg.iconCn} />
                  </div>
                  <div className="flex-1 min-w-0">
                    <p className="text-sm font-medium text-ink truncate tracking-tightish">{file.name}</p>
                    <p className="text-2xs text-ink-faint truncate">
                      {file.path.replace(/\/Users\/[^/]+/, '~')}
                    </p>
                  </div>
                  <div className="flex items-center gap-3 flex-shrink-0 text-2xs text-ink-faint tracking-tightish">
                    <span>{formatSize(file.size)}</span>
                    <span className={`px-1.5 py-0.5 rounded-xs ${cfg.bgCn} ${cfg.iconCn} font-semibold uppercase text-[9px]`}>
                      {file.ext.slice(1)}
                    </span>
                    {importing === file.path
                      ? <Loader2 size={12} className="animate-spin text-accent" />
                      : <ArrowUpRight size={12} className="opacity-0 group-hover:opacity-100 transition-opacity" />
                    }
                  </div>
                </button>
              ))}
            </Card>
          </section>
        )}
      </div>

      {showNew && <NewFileModal onClose={() => setShowNew(false)} lockType={type} />}
      {menuOpen && <div className="fixed inset-0 z-10" onClick={() => setMenuOpen(null)} />}
    </div>
  )
}

// ─── FileCard ─────────────────────────────────────────────────────────────────
function FileCard({
  file, cfg, Icon, renaming, renameValue, setRenaming, setRenameValue,
  menuOpen, setMenuOpen, onOpen, onRename, onRenameCommit, onDelete,
}) {
  return (
    <div className="group bg-paper rounded-lg border border-line hover:border-line-strong hover:shadow-e1 transition-all cursor-pointer overflow-hidden">
      {/* Thumbnail */}
      <div
        className={`h-28 ${cfg.bgCn} flex items-center justify-center relative`}
        onClick={onOpen}
      >
        <Icon size={32} className={`${cfg.iconCn} opacity-25`} />
        <div className="absolute top-2 right-2 opacity-0 group-hover:opacity-100 transition-opacity">
          <ArrowUpRight size={13} className="text-ink-faint" />
        </div>
      </div>
      {/* Meta */}
      <div className="p-3" onClick={onOpen}>
        {renaming === file.id ? (
          <input
            autoFocus
            value={renameValue}
            onChange={e => setRenameValue(e.target.value)}
            onBlur={onRenameCommit}
            onKeyDown={e => {
              if (e.key === 'Enter') onRenameCommit()
              if (e.key === 'Escape') setRenaming(null)
            }}
            className="w-full text-xs font-semibold border border-accent rounded-sm px-1 focus:outline-none bg-paper text-ink"
            onClick={e => e.stopPropagation()}
          />
        ) : (
          <p className="text-xs font-semibold text-ink truncate tracking-tightish">{file.name}</p>
        )}
        <div className="flex items-center justify-between mt-1.5">
          <span className="text-2xs text-ink-faint flex items-center gap-1 tracking-tightish">
            <Clock size={9} />{formatDate(file.updated_at)}
          </span>
          <div className="relative" onClick={e => e.stopPropagation()}>
            <button
              onClick={() => setMenuOpen(menuOpen === file.id ? null : file.id)}
              className="p-0.5 rounded-sm hover:bg-accent-tint text-ink-faint opacity-0 group-hover:opacity-100 transition-[opacity,background] duration-fast"
            >
              <MoreVertical size={12} />
            </button>
            {menuOpen === file.id && (
              <div className="absolute right-0 bottom-full mb-1 w-32 bg-paper border border-line rounded-lg shadow-e2 z-20 py-1 text-xs overflow-hidden animate-scale-in">
                <button
                  className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-accent-tint text-ink transition-colors"
                  onClick={onRename}
                >
                  <Pencil size={12} className="text-ink-faint" /> Rename
                </button>
                <button
                  className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-danger-bg text-danger transition-colors"
                  onClick={onDelete}
                >
                  <Trash2 size={12} /> Delete
                </button>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

// ─── FileListTable ────────────────────────────────────────────────────────────
function FileListTable({
  files, cfg, Icon, renaming, renameValue, setRenaming, setRenameValue,
  menuOpen, setMenuOpen, onOpen, onRename, onRenameCommit, onDelete,
}) {
  return (
    <Card>
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-line bg-bg-elev2">
            <th className="text-left px-4 py-2.5 text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">Name</th>
            <th className="text-left px-4 py-2.5 text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">Modified</th>
            <th className="w-10" />
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {files.map(file => (
            <tr
              key={file.id}
              className="group hover:bg-accent-tint cursor-pointer transition-colors duration-fast"
              onClick={() => onOpen(file)}
            >
              <td className="px-4 py-3">
                <div className="flex items-center gap-3">
                  <div className={`w-8 h-8 ${cfg.bgCn} rounded-md flex items-center justify-center flex-shrink-0`}>
                    <Icon size={14} className={cfg.iconCn} />
                  </div>
                  {renaming === file.id ? (
                    <input
                      autoFocus
                      value={renameValue}
                      onChange={e => setRenameValue(e.target.value)}
                      onBlur={() => onRenameCommit(file.id)}
                      onKeyDown={e => {
                        if (e.key === 'Enter') onRenameCommit(file.id)
                        if (e.key === 'Escape') setRenaming(null)
                      }}
                      className="font-medium border border-accent rounded-sm px-1 text-sm focus:outline-none bg-paper text-ink"
                      onClick={e => e.stopPropagation()}
                    />
                  ) : (
                    <span className="font-medium text-ink tracking-tightish">{file.name}</span>
                  )}
                </div>
              </td>
              <td className="px-4 py-3 text-2xs text-ink-faint tracking-tightish">{formatDate(file.updated_at)}</td>
              <td className="px-2 py-3" onClick={e => e.stopPropagation()}>
                <div className="relative">
                  <button
                    onClick={() => setMenuOpen(menuOpen === file.id ? null : file.id)}
                    className="p-1 rounded-sm hover:bg-accent-tint text-ink-faint opacity-0 group-hover:opacity-100 transition-[opacity,background] duration-fast"
                  >
                    <MoreVertical size={14} />
                  </button>
                  {menuOpen === file.id && (
                    <div className="absolute right-0 top-full mt-1 w-32 bg-paper border border-line rounded-lg shadow-e2 z-20 py-1 text-xs overflow-hidden animate-scale-in">
                      <button
                        className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-accent-tint text-ink transition-colors"
                        onClick={() => onRename(file)}
                      >
                        <Pencil size={12} className="text-ink-faint" /> Rename
                      </button>
                      <button
                        className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-danger-bg text-danger transition-colors"
                        onClick={() => onDelete(file.id)}
                      >
                        <Trash2 size={12} /> Delete
                      </button>
                    </div>
                  )}
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  )
}
