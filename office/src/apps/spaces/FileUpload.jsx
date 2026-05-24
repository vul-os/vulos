/**
 * FileUpload.jsx — drag-drop file upload for ChannelView.
 * Multiple files, image thumbnails, icon+name for non-images.
 * PDF preview via <embed>, others trigger download.
 */
import { useState, useRef, useCallback } from 'react'
import { Paperclip, X, Image, FileText, Download, Eye } from 'lucide-react'

const IMAGE_TYPES = new Set(['image/jpeg','image/png','image/gif','image/webp','image/svg+xml'])
const PDF_TYPE = 'application/pdf'

function FileIcon({ mime }) {
  if (IMAGE_TYPES.has(mime)) return <Image size={14} className="text-accent flex-shrink-0" />
  if (mime === PDF_TYPE) return <FileText size={14} className="text-warning flex-shrink-0" />
  return <Paperclip size={14} className="text-ink-muted flex-shrink-0" />
}

function formatBytes(n) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

/**
 * AttachmentPreview — renders an attachment reference in a message bubble.
 *
 * Props:
 *   attachment — { url, name, mime, size, thumbnail_url? }
 */
export function AttachmentPreview({ attachment }) {
  const [lightbox, setLightbox] = useState(false)
  const [pdfOpen, setPdfOpen] = useState(false)
  if (!attachment) return null

  const isImage = IMAGE_TYPES.has(attachment.mime)
  const isPdf = attachment.mime === PDF_TYPE

  return (
    <div className="mt-2 inline-flex flex-col gap-1 max-w-xs">
      {isImage && attachment.thumbnail_url && (
        <button
          type="button"
          onClick={() => setLightbox(true)}
          className="rounded-md overflow-hidden border border-line hover:border-accent transition-colors"
          title="View image"
        >
          <img
            src={attachment.thumbnail_url}
            alt={attachment.name}
            className="max-w-full max-h-48 object-cover block"
          />
        </button>
      )}

      <div className="flex items-center gap-2 bg-bg-elev2 border border-line rounded-md px-2 py-1.5">
        <FileIcon mime={attachment.mime} />
        <div className="flex-1 min-w-0">
          <p className="text-xs font-medium text-ink truncate tracking-tightish">{attachment.name}</p>
          <p className="text-2xs text-ink-faint">{formatBytes(attachment.size || 0)}</p>
        </div>
        {isPdf && (
          <button
            type="button"
            onClick={() => setPdfOpen(true)}
            className="p-1 rounded-sm text-ink-muted hover:text-ink hover:bg-accent-tint transition-colors"
            title="Preview PDF"
          >
            <Eye size={12} />
          </button>
        )}
        <a
          href={attachment.url}
          download={attachment.name}
          className="p-1 rounded-sm text-ink-muted hover:text-ink hover:bg-accent-tint transition-colors"
          title="Download"
        >
          <Download size={12} />
        </a>
      </div>

      {/* Image lightbox */}
      {lightbox && (
        <div
          className="fixed inset-0 z-50 bg-ink/70 flex items-center justify-center"
          onClick={() => setLightbox(false)}
        >
          <div className="relative max-w-4xl max-h-screen p-4">
            <img src={attachment.url} alt={attachment.name} className="max-w-full max-h-[90vh] rounded-md shadow-e3" />
            <button
              type="button"
              onClick={() => setLightbox(false)}
              className="absolute top-2 right-2 p-1 rounded-full bg-paper text-ink hover:bg-bg-elev2"
            >
              <X size={16} />
            </button>
          </div>
        </div>
      )}

      {/* PDF viewer */}
      {pdfOpen && (
        <div className="fixed inset-0 z-50 bg-ink/70 flex items-center justify-center" onClick={() => setPdfOpen(false)}>
          <div className="relative w-[80vw] h-[90vh] bg-paper rounded-lg shadow-e3 overflow-hidden" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between px-3 h-10 border-b border-line">
              <span className="text-sm font-medium text-ink tracking-tightish truncate">{attachment.name}</span>
              <button type="button" onClick={() => setPdfOpen(false)} className="p-1 rounded-sm text-ink-faint hover:text-ink">
                <X size={14} />
              </button>
            </div>
            <embed src={attachment.url} type="application/pdf" className="w-full h-full" />
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * FileUploadZone — invisible overlay on ChannelView for drag-drop.
 *
 * Props:
 *   onFiles — (files: File[]) => void
 *   children
 */
export function FileUploadZone({ onFiles, children }) {
  const [dragging, setDragging] = useState(false)
  const counter = useRef(0)

  const onDragEnter = useCallback((e) => {
    e.preventDefault()
    counter.current++
    if (counter.current === 1) setDragging(true)
  }, [])

  const onDragLeave = useCallback(() => {
    counter.current--
    if (counter.current === 0) setDragging(false)
  }, [])

  const onDrop = useCallback((e) => {
    e.preventDefault()
    counter.current = 0
    setDragging(false)
    const files = Array.from(e.dataTransfer.files)
    if (files.length > 0) onFiles(files)
  }, [onFiles])

  const onDragOver = useCallback((e) => { e.preventDefault() }, [])

  return (
    <div
      className="relative flex-1 flex flex-col min-h-0"
      onDragEnter={onDragEnter}
      onDragLeave={onDragLeave}
      onDragOver={onDragOver}
      onDrop={onDrop}
    >
      {children}
      {dragging && (
        <div className="absolute inset-0 z-40 bg-accent/10 border-2 border-dashed border-accent rounded-md flex items-center justify-center pointer-events-none">
          <div className="bg-paper rounded-lg px-6 py-4 shadow-e2 flex flex-col items-center gap-2">
            <Paperclip size={24} className="text-accent" />
            <p className="text-sm font-semibold text-ink tracking-tightish">Drop files to upload</p>
            <p className="text-xs text-ink-faint">Multiple files supported</p>
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * PendingFileList — shows staged files before send.
 *
 * Props:
 *   files     — File[]
 *   onRemove  — (idx) => void
 */
export function PendingFileList({ files = [], onRemove }) {
  if (files.length === 0) return null
  return (
    <div className="flex flex-wrap gap-2 px-3 pt-2">
      {files.map((f, i) => (
        <div
          key={i}
          className="flex items-center gap-1.5 bg-bg-elev2 border border-line rounded-md px-2 py-1"
        >
          <FileIcon mime={f.type} />
          <span className="text-xs text-ink-muted truncate max-w-[120px]">{f.name}</span>
          <span className="text-2xs text-ink-faint">{formatBytes(f.size)}</span>
          <button
            type="button"
            onClick={() => onRemove(i)}
            className="ml-0.5 text-ink-faint hover:text-danger transition-colors"
          >
            <X size={11} />
          </button>
        </div>
      ))}
    </div>
  )
}
