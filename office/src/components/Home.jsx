import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  FileText, Table2, Presentation, FileSearch, Clock,
  ArrowUpRight, FolderSearch, HardDrive, RefreshCw, Loader2, Plus,
} from 'lucide-react'
import { useFilesStore } from '../store/filesStore'
import { useLocalFilesStore } from '../store/localFilesStore'
import { importFromUrl } from '../lib/importFile'
import NewFileModal from './NewFileModal'
import { Card, Button, Tooltip } from './ui'

// ─── Token-aligned type map (no raw indigo/emerald/amber) ───────────────────
const typeInfo = {
  doc:   { icon: FileText,     iconCn: 'text-accent',         bgCn: 'bg-accent-tint',         route: 'docs'   },
  sheet: { icon: Table2,       iconCn: 'text-success',        bgCn: 'bg-success-bg',          route: 'sheets' },
  slide: { icon: Presentation, iconCn: 'text-warning',        bgCn: 'bg-warning-bg',          route: 'slides' },
}

const localTypeInfo = {
  doc:   { icon: FileText,     iconCn: 'text-accent',   bgCn: 'bg-accent-tint',  label: 'Document'     },
  sheet: { icon: Table2,       iconCn: 'text-success',  bgCn: 'bg-success-bg',   label: 'Spreadsheet'  },
  slide: { icon: Presentation, iconCn: 'text-warning',  bgCn: 'bg-warning-bg',   label: 'Presentation' },
  pdf:   { icon: FileSearch,   iconCn: 'text-danger',   bgCn: 'bg-danger-bg',    label: 'PDF'          },
}

function formatDate(ms) {
  const diff = Date.now() - ms
  if (diff < 60000)    return 'just now'
  if (diff < 3600000)  return `${Math.floor(diff / 60000)}m ago`
  if (diff < 86400000) return `${Math.floor(diff / 3600000)}h ago`
  if (diff < 604800000)return `${Math.floor(diff / 86400000)}d ago`
  return new Date(ms).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

function formatSize(bytes) {
  if (bytes < 1024)         return bytes + ' B'
  if (bytes < 1024 * 1024)  return (bytes / 1024).toFixed(0) + ' KB'
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB'
}

async function openLocalFile(file, navigate, setImporting) {
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

// ─── Quick-start cluster ─────────────────────────────────────────────────────
const quickStarts = [
  { label: 'New Document',     shortLabel: 'Document',     icon: FileText,     type: 'doc'   },
  { label: 'New Spreadsheet',  shortLabel: 'Spreadsheet',  icon: Table2,       type: 'sheet' },
  { label: 'New Presentation', shortLabel: 'Presentation', icon: Presentation, type: 'slide' },
]

export default function Home() {
  const navigate = useNavigate()
  const { files, loading: filesLoading, fetchFiles } = useFilesStore()
  const { files: localFiles, loading: localLoading, scanned, scan } = useLocalFilesStore()
  const [showLocalAll, setShowLocalAll] = useState(false)
  const [importing, setImporting] = useState(null)
  const [showNew, setShowNew] = useState(false)
  const [newType, setNewType] = useState(null)

  useEffect(() => { fetchFiles() }, [])
  useEffect(() => { if (!scanned) scan() }, [scanned])

  const recentFiles = files.filter(f => typeInfo[f.type]).slice(0, 12)
  const visibleLocal = showLocalAll ? localFiles : localFiles.slice(0, 8)

  const openNew = (type) => { setNewType(type); setShowNew(true) }

  return (
    <div className="flex-1 overflow-auto bg-bg">
      <div className="max-w-4xl mx-auto px-8 py-10 space-y-10">

        {/* ── Quick-start cluster ── */}
        <section>
          <p className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase mb-4">
            Start something new
          </p>
          <div className="grid grid-cols-3 gap-3">
            {quickStarts.map(({ label, shortLabel, icon: Icon, type }) => {
              const info = typeInfo[type]
              return (
                <button
                  key={type}
                  onClick={() => openNew(type)}
                  className={[
                    'group flex flex-col items-center gap-3 p-5 rounded-lg border border-line',
                    'bg-paper hover:border-line-strong hover:shadow-e1',
                    'transition-[border-color,box-shadow] duration-fast ease-out',
                    'text-left cursor-pointer',
                  ].join(' ')}
                >
                  <div className={`w-11 h-11 rounded-lg ${info.bgCn} flex items-center justify-center flex-shrink-0`}>
                    <Icon size={22} className={info.iconCn} />
                  </div>
                  <div>
                    <p className="text-sm font-semibold text-ink tracking-tightish text-center leading-snug">{shortLabel}</p>
                    <p className="text-2xs text-ink-faint text-center mt-0.5 tracking-tightish">Blank</p>
                  </div>
                  <div className="opacity-0 group-hover:opacity-100 transition-opacity duration-fast">
                    <Plus size={13} className="text-ink-faint" />
                  </div>
                </button>
              )
            })}
          </div>
        </section>

        {/* ── Recent documents ── */}
        {filesLoading && (
          <div className="flex items-center justify-center py-12">
            <Loader2 size={18} className="animate-spin text-accent" />
          </div>
        )}

        {!filesLoading && recentFiles.length > 0 && (
          <section>
            <p className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase mb-4">
              Recent
            </p>
            <Card>
              {recentFiles.map((file, i) => {
                const info = typeInfo[file.type]
                const Icon = info.icon
                return (
                  <button
                    key={file.id}
                    onClick={() => navigate(`/${info.route}/${file.id}`)}
                    className={[
                      'w-full flex items-center gap-3 px-4 py-3 text-left group',
                      'hover:bg-accent-tint transition-colors duration-fast ease-out',
                      i < recentFiles.length - 1 ? 'border-b border-line' : '',
                    ].join(' ')}
                  >
                    <div className={`w-8 h-8 ${info.bgCn} rounded-md flex items-center justify-center flex-shrink-0`}>
                      <Icon size={14} className={info.iconCn} />
                    </div>
                    <span className="text-sm font-medium text-ink flex-1 truncate tracking-tightish">{file.name}</span>
                    <span className="text-2xs text-ink-faint flex items-center gap-1 flex-shrink-0 tracking-tightish">
                      <Clock size={10} />
                      {formatDate(new Date(file.updated_at).getTime())}
                    </span>
                    <ArrowUpRight
                      size={13}
                      className="text-ink-faint opacity-0 group-hover:opacity-100 transition-opacity flex-shrink-0"
                    />
                  </button>
                )
              })}
            </Card>
          </section>
        )}

        {/* ── Empty state — gentle serif tagline ── */}
        {!filesLoading && recentFiles.length === 0 && (
          <section className="flex flex-col items-center justify-center py-16 text-center">
            <div className="w-16 h-16 rounded-full bg-accent-tint flex items-center justify-center mb-5">
              <FileText size={28} className="text-accent opacity-60" />
            </div>
            <p className="font-serif text-xl text-ink leading-snug mb-2">
              Your workspace awaits.
            </p>
            <p className="text-sm text-ink-muted max-w-xs leading-relaxed mb-6">
              Start with a blank document, spreadsheet, or presentation — or open a file from your computer.
            </p>
            <Button variant="primary" size="md" onClick={() => setShowNew(true)}>
              <Plus size={14} /> New file
            </Button>
          </section>
        )}

        {/* ── Local files from disk ── */}
        <section>
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2">
              <HardDrive size={13} className="text-ink-faint" />
              <p className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
                On Your Computer
              </p>
              {localFiles.length > 0 && (
                <span className="text-2xs text-ink-faint bg-bg-elev2 border border-line rounded-pill px-2 py-0.5 tracking-tightish">
                  {localFiles.length}
                </span>
              )}
            </div>
            <Tooltip label={localLoading ? 'Scanning…' : 'Rescan files'} side="left">
              <button
                onClick={() => scan()}
                className="flex items-center gap-1.5 text-2xs text-ink-faint hover:text-ink-muted transition-colors"
              >
                <RefreshCw size={12} className={localLoading ? 'animate-spin' : ''} />
                {localLoading ? 'Scanning…' : 'Rescan'}
              </button>
            </Tooltip>
          </div>

          {localLoading && !scanned && (
            <Card className="p-6 flex items-center gap-3 text-sm text-ink-muted">
              <FolderSearch size={16} className="text-ink-faint animate-pulse flex-shrink-0" />
              Scanning Documents, Downloads &amp; Desktop…
            </Card>
          )}

          {scanned && localFiles.length === 0 && (
            <Card className="p-8 text-center">
              <FolderSearch size={26} className="text-ink-faint mx-auto mb-2" />
              <p className="text-sm text-ink-muted">No supported files found in Documents, Downloads or Desktop</p>
            </Card>
          )}

          {localFiles.length > 0 && (
            <>
              <Card>
                {visibleLocal.map((file, i) => {
                  const info = localTypeInfo[file.appType]
                  if (!info) return null
                  const Icon = info.icon
                  return (
                    <button
                      key={file.path}
                      onClick={() => openLocalFile(file, navigate, setImporting)}
                      disabled={importing === file.path}
                      className={[
                        'w-full flex items-center gap-3 px-4 py-2.5 text-left group',
                        'hover:bg-accent-tint transition-colors duration-fast ease-out',
                        'disabled:opacity-60',
                        i < visibleLocal.length - 1 ? 'border-b border-line' : '',
                      ].join(' ')}
                    >
                      <div className={`w-7 h-7 ${info.bgCn} rounded-md flex items-center justify-center flex-shrink-0`}>
                        <Icon size={13} className={info.iconCn} />
                      </div>
                      <div className="flex-1 min-w-0">
                        <p className="text-sm font-medium text-ink truncate tracking-tightish">{file.name}</p>
                        <p className="text-2xs text-ink-faint truncate">
                          {file.path.replace(/\/Users\/[^/]+/, '~')}
                        </p>
                      </div>
                      <div className="flex items-center gap-3 flex-shrink-0 text-2xs text-ink-faint tracking-tightish">
                        <span>{formatSize(file.size)}</span>
                        <span>{formatDate(file.modified)}</span>
                        <span className={`px-1.5 py-0.5 rounded-xs ${info.bgCn} ${info.iconCn} font-semibold uppercase text-[9px]`}>
                          {file.ext.slice(1)}
                        </span>
                        {importing === file.path
                          ? <Loader2 size={12} className="animate-spin text-accent" />
                          : <ArrowUpRight size={12} className="opacity-0 group-hover:opacity-100 transition-opacity" />
                        }
                      </div>
                    </button>
                  )
                })}
              </Card>

              {localFiles.length > 8 && (
                <button
                  onClick={() => setShowLocalAll(v => !v)}
                  className="mt-3 w-full py-2 text-2xs text-ink-faint hover:text-ink-muted bg-paper border border-line rounded-md hover:bg-bg-elev2 transition-colors tracking-tightish"
                >
                  {showLocalAll ? 'Show less' : `Show all ${localFiles.length} files`}
                </button>
              )}
            </>
          )}
        </section>
      </div>

      {showNew && (
        <NewFileModal
          defaultType={newType || 'doc'}
          onClose={() => { setShowNew(false); setNewType(null) }}
        />
      )}
    </div>
  )
}
