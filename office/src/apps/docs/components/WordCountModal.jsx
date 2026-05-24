/**
 * WordCountModal — detailed word/character/page count breakdown.
 *
 * Opens from Tools → Word count or clicking the status bar.
 *
 * Props:
 *   editor   {Editor}    TipTap editor instance
 *   onClose  {function}
 */

import { useMemo } from 'react'
import { X, Type, AlignLeft, FileText } from 'lucide-react'

// Estimate pages from word count (250 words/page, Google Docs default)
const WORDS_PER_PAGE = 250

function countWords(text) {
  if (!text) return 0
  return text.trim().split(/\s+/).filter(Boolean).length
}

function countChars(text, includeSpaces = true) {
  if (!text) return 0
  return includeSpaces ? text.length : text.replace(/\s/g, '').length
}

function getSelectionText(editor) {
  if (!editor) return ''
  const { from, to } = editor.state.selection
  if (from === to) return ''
  return editor.state.doc.textBetween(from, to, ' ')
}

export default function WordCountModal({ editor, onClose }) {
  const stats = useMemo(() => {
    if (!editor) return null
    const fullText = editor.getText()
    const selText = getSelectionText(editor)
    const words = countWords(fullText)
    const chars = countChars(fullText)
    const charsNoSpaces = countChars(fullText, false)
    const pages = Math.max(1, Math.ceil(words / WORDS_PER_PAGE))
    const paragraphs = (editor.state.doc.content.content || [])
      .filter((n) => n.type.name === 'paragraph').length

    const selWords = countWords(selText)
    const selChars = countChars(selText)
    const hasSelection = selText.length > 0

    return { words, chars, charsNoSpaces, pages, paragraphs, selWords, selChars, hasSelection }
  }, [editor])

  if (!stats) return null

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="Word count"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 animate-fade-in"
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      <div className="bg-paper border border-line rounded-xl shadow-e3 w-80 overflow-hidden animate-scale-in">
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-line bg-bg-elev2">
          <div className="flex items-center gap-2">
            <Type size={14} className="text-ink-muted" />
            <span className="text-sm font-semibold text-ink tracking-tightish">Word Count</span>
          </div>
          <button
            onClick={onClose}
            aria-label="Close"
            className="w-6 h-6 flex items-center justify-center rounded-sm text-ink-faint hover:bg-bg-elev2 hover:text-ink transition-colors"
          >
            <X size={13} />
          </button>
        </div>

        {/* Stats grid */}
        <div className="px-4 py-4 space-y-3">
          <StatRow icon={<AlignLeft size={13} />} label="Words" value={stats.words.toLocaleString()} />
          <StatRow icon={<Type size={13} />} label="Characters (with spaces)" value={stats.chars.toLocaleString()} />
          <StatRow icon={<Type size={13} />} label="Characters (no spaces)" value={stats.charsNoSpaces.toLocaleString()} />
          <StatRow icon={<FileText size={13} />} label="Pages (est.)" value={stats.pages.toLocaleString()} />
          <StatRow icon={<AlignLeft size={13} />} label="Paragraphs" value={stats.paragraphs.toLocaleString()} />
        </div>

        {/* Selection stats */}
        {stats.hasSelection && (
          <>
            <div className="mx-4 border-t border-line" />
            <div className="px-4 py-3 space-y-2">
              <p className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">Selection</p>
              <StatRow label="Words" value={stats.selWords.toLocaleString()} />
              <StatRow label="Characters" value={stats.selChars.toLocaleString()} />
            </div>
          </>
        )}

        {/* Footer note */}
        <div className="px-4 py-2.5 border-t border-line bg-bg-elev2">
          <p className="text-2xs text-ink-faint">Pages estimated at {WORDS_PER_PAGE} words per page.</p>
        </div>
      </div>
    </div>
  )
}

function StatRow({ icon, label, value }) {
  return (
    <div className="flex items-center justify-between">
      <div className="flex items-center gap-2 text-xs text-ink-muted">
        {icon && <span className="text-ink-faint">{icon}</span>}
        {label}
      </div>
      <span className="text-sm font-semibold text-ink tabular-nums">{value}</span>
    </div>
  )
}
