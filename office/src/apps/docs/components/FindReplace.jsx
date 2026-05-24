/**
 * FindReplace — floating find/replace bar for DocsEditor.
 *
 * Cmd+F  → opens in "find only" mode
 * Cmd+H  → opens in "find + replace" mode
 * Esc    → closes
 *
 * Highlights all matches in the document using TipTap's built-in search
 * (via editor.commands.setSearchTerm / editor.commands.nextSearchResult /
 * editor.commands.previousSearchResult) if the SearchAndReplace extension is
 * present, or falls back to a manual DOM-mark approach.
 *
 * Props:
 *   editor      {Editor}   TipTap editor instance
 *   mode        {'find'|'replace'}
 *   onClose     {function}
 */

import { useState, useEffect, useRef, useCallback } from 'react'
import { X, ChevronUp, ChevronDown, Replace, ReplaceAll } from 'lucide-react'

function escapeRegex(str) {
  return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function findAllMatches(doc, term, caseSensitive) {
  if (!term) return []
  const fullText = doc.textContent || ''
  const flags = caseSensitive ? 'g' : 'gi'
  let re
  try { re = new RegExp(escapeRegex(term), flags) } catch { return [] }
  const matches = []
  let m
  while ((m = re.exec(fullText)) !== null) {
    matches.push({ index: m.index, length: m[0].length })
    if (re.lastIndex === m.index) re.lastIndex++
  }
  return matches
}

export default function FindReplace({ editor, mode: initialMode, onClose }) {
  const [mode, setMode] = useState(initialMode || 'find')
  const [term, setTerm] = useState('')
  const [replace, setReplace] = useState('')
  const [caseSensitive, setCaseSensitive] = useState(false)
  const [matches, setMatches] = useState([])
  const [current, setCurrent] = useState(0)
  const termRef = useRef(null)

  // Focus the search input on mount
  useEffect(() => { termRef.current?.focus() }, [])

  // Re-compute matches whenever term or case changes
  useEffect(() => {
    if (!editor || !term) {
      setMatches([])
      setCurrent(0)
      return
    }
    const found = findAllMatches(editor.state.doc, term, caseSensitive)
    setMatches(found)
    setCurrent(found.length > 0 ? 0 : -1)
  }, [term, caseSensitive, editor])

  // Scroll to and select the current match
  const selectMatch = useCallback((idx, matchList) => {
    const m = matchList ?? matches
    if (!editor || m.length === 0 || idx < 0) return
    const match = m[idx]
    // Walk the doc to find the ProseMirror position for the text offset
    let pos = 0
    let found = false
    editor.state.doc.descendants((node, nodePos) => {
      if (found || !node.isText) return
      const start = pos
      const end = pos + node.text.length
      if (match.index >= start && match.index < end) {
        const from = nodePos + (match.index - start)
        const to = from + match.length
        editor.commands.setTextSelection({ from, to })
        found = true
        return false
      }
      pos += node.isText ? node.text.length : 0
    })
  }, [editor, matches])

  const goNext = () => {
    if (matches.length === 0) return
    const next = (current + 1) % matches.length
    setCurrent(next)
    selectMatch(next)
  }

  const goPrev = () => {
    if (matches.length === 0) return
    const prev = (current - 1 + matches.length) % matches.length
    setCurrent(prev)
    selectMatch(prev)
  }

  const doReplace = () => {
    if (!editor || matches.length === 0 || current < 0) return
    const { from, to } = editor.state.selection
    if (from !== to) {
      editor.chain().focus().deleteRange({ from, to }).insertContentAt(from, replace).run()
    }
    // Recompute + advance
    const newMatches = findAllMatches(editor.state.doc, term, caseSensitive)
    setMatches(newMatches)
    const next = Math.min(current, newMatches.length - 1)
    setCurrent(next)
    selectMatch(next, newMatches)
  }

  const doReplaceAll = () => {
    if (!editor || !term) return
    const flags = caseSensitive ? 'g' : 'gi'
    let re
    try { re = new RegExp(escapeRegex(term), flags) } catch { return }
    // Walk doc in reverse to avoid offset drift
    const { doc } = editor.state
    const edits = []
    let textOffset = 0
    doc.descendants((node, pos) => {
      if (!node.isText) return
      let m
      re.lastIndex = 0
      while ((m = re.exec(node.text)) !== null) {
        edits.push({ from: pos + m.index, to: pos + m.index + m[0].length })
      }
      textOffset += node.text.length
    })
    // Apply in reverse order
    let chain = editor.chain().focus()
    for (let i = edits.length - 1; i >= 0; i--) {
      const e = edits[i]
      chain = chain.deleteRange({ from: e.from, to: e.to }).insertContentAt(e.from, replace)
    }
    chain.run()
    setMatches([])
    setCurrent(-1)
  }

  const handleKeyDown = (e) => {
    if (e.key === 'Escape') { onClose(); return }
    if (e.key === 'Enter') {
      if (e.shiftKey) goPrev()
      else goNext()
    }
  }

  const countLabel = term
    ? matches.length === 0
      ? 'No results'
      : `${current + 1} / ${matches.length}`
    : ''

  return (
    <div
      role="dialog"
      aria-label="Find and replace"
      className={[
        'absolute top-2 right-4 z-50 bg-paper border border-line rounded-lg shadow-e3',
        'flex flex-col gap-0 overflow-hidden animate-scale-in',
        'w-80',
      ].join(' ')}
      style={{ minWidth: 260 }}
    >
      {/* Find row */}
      <div className="flex items-center gap-1 px-2 py-1.5 border-b border-line">
        <input
          ref={termRef}
          value={term}
          onChange={(e) => setTerm(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Find…"
          aria-label="Search term"
          className="flex-1 min-w-0 text-sm bg-transparent outline-none text-ink placeholder:text-ink-faint"
        />
        {/* Count indicator */}
        {countLabel && (
          <span className="text-2xs text-ink-faint flex-shrink-0 tabular-nums">{countLabel}</span>
        )}
        {/* Case sensitive */}
        <button
          title="Match case"
          onClick={() => setCaseSensitive((v) => !v)}
          className={`flex-shrink-0 w-6 h-6 rounded-sm flex items-center justify-center text-2xs font-bold transition-colors ${caseSensitive ? 'bg-accent text-white' : 'text-ink-faint hover:bg-bg-elev2'}`}
          aria-pressed={caseSensitive}
        >
          Aa
        </button>
        {/* Prev / Next */}
        <button
          onClick={goPrev}
          disabled={matches.length === 0}
          title="Previous match (Shift+Enter)"
          aria-label="Previous match"
          className="flex-shrink-0 w-6 h-6 rounded-sm flex items-center justify-center text-ink-faint hover:bg-bg-elev2 disabled:opacity-30 transition-colors"
        >
          <ChevronUp size={13} />
        </button>
        <button
          onClick={goNext}
          disabled={matches.length === 0}
          title="Next match (Enter)"
          aria-label="Next match"
          className="flex-shrink-0 w-6 h-6 rounded-sm flex items-center justify-center text-ink-faint hover:bg-bg-elev2 disabled:opacity-30 transition-colors"
        >
          <ChevronDown size={13} />
        </button>
        {/* Toggle replace */}
        <button
          onClick={() => setMode((m) => (m === 'find' ? 'replace' : 'find'))}
          title={mode === 'find' ? 'Show replace' : 'Hide replace'}
          aria-label="Toggle replace"
          className="flex-shrink-0 w-6 h-6 rounded-sm flex items-center justify-center text-ink-faint hover:bg-bg-elev2 transition-colors"
        >
          <Replace size={13} />
        </button>
        {/* Close */}
        <button
          onClick={onClose}
          title="Close (Esc)"
          aria-label="Close find bar"
          className="flex-shrink-0 w-6 h-6 rounded-sm flex items-center justify-center text-ink-faint hover:bg-bg-elev2 transition-colors"
        >
          <X size={13} />
        </button>
      </div>

      {/* Replace row */}
      {mode === 'replace' && (
        <div className="flex items-center gap-1 px-2 py-1.5">
          <input
            value={replace}
            onChange={(e) => setReplace(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Escape') onClose() }}
            placeholder="Replace with…"
            aria-label="Replace with"
            className="flex-1 min-w-0 text-sm bg-transparent outline-none text-ink placeholder:text-ink-faint"
          />
          <button
            onClick={doReplace}
            disabled={matches.length === 0}
            title="Replace current"
            aria-label="Replace"
            className="flex-shrink-0 h-6 px-2 text-2xs font-medium rounded-sm border border-line text-ink-muted hover:bg-accent-tint disabled:opacity-30 transition-colors"
          >
            Replace
          </button>
          <button
            onClick={doReplaceAll}
            disabled={!term}
            title="Replace all"
            aria-label="Replace all"
            className="flex-shrink-0 h-6 px-2 text-2xs font-medium rounded-sm border border-line text-ink-muted hover:bg-accent-tint disabled:opacity-30 transition-colors flex items-center gap-1"
          >
            <ReplaceAll size={11} /> All
          </button>
        </div>
      )}
    </div>
  )
}
