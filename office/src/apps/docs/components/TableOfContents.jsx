/**
 * TableOfContents — inserts a navigable ToC block at the current cursor.
 * Reads headings from the editor document and renders them as a linked list.
 * The inserted block is static HTML (not a live node type); the user can
 * update it by clicking "Update ToC" which re-reads headings.
 *
 * Props:
 *   editor  {Editor}    TipTap editor instance
 *   onClose {function}  dismiss the popover
 */

import { useMemo } from 'react'
import { List } from 'lucide-react'

function slugify(text) {
  return text.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

export function extractHeadings(editor) {
  const headings = []
  if (!editor) return headings
  editor.state.doc.descendants((node) => {
    if (node.type.name === 'heading') {
      const text = node.textContent
      headings.push({ level: node.attrs.level, text, slug: slugify(text) })
    }
  })
  return headings
}

export function buildTocHtml(headings) {
  if (headings.length === 0) return '<p><em>No headings found.</em></p>'
  const items = headings.map((h) => {
    const indent = (h.level - 1) * 16
    return `<p style="margin-left:${indent}px;margin-bottom:2px">
      <a href="#${h.slug}" style="color:inherit;text-decoration:none">
        ${h.text}
      </a>
    </p>`
  })
  return `<div class="toc-block" style="border:1px solid var(--color-line,#e2e2e0);border-radius:6px;padding:12px 16px;margin:12px 0;background:var(--color-bg-elev2,#fafafa)">
    <p style="font-size:0.7rem;font-weight:600;text-transform:uppercase;letter-spacing:0.06em;color:var(--color-ink-faint,#888);margin-bottom:8px">Table of Contents</p>
    ${items.join('\n')}
  </div>`
}

export default function TableOfContents({ editor, onClose }) {
  const headings = useMemo(() => extractHeadings(editor), [editor])

  const handleInsert = () => {
    if (!editor) return
    const html = buildTocHtml(headings)
    editor.chain().focus().insertContent(html).run()
    onClose?.()
  }

  return (
    <div className="w-52 bg-paper border border-line rounded-lg shadow-e2 overflow-hidden animate-scale-in text-sm">
      <div className="flex items-center gap-2 px-3 py-2 border-b border-line bg-bg-elev2">
        <List size={13} className="text-ink-muted" />
        <span className="text-xs font-semibold text-ink tracking-tightish">Table of Contents</span>
      </div>
      <div className="px-3 py-2 max-h-48 overflow-y-auto space-y-1">
        {headings.length === 0 ? (
          <p className="text-2xs text-ink-faint italic">No headings in document.</p>
        ) : headings.map((h, i) => (
          <div
            key={i}
            className="text-2xs text-ink-muted truncate"
            style={{ paddingLeft: (h.level - 1) * 10 }}
          >
            {h.text}
          </div>
        ))}
      </div>
      <div className="px-3 py-2 border-t border-line flex gap-2">
        <button
          onClick={handleInsert}
          className="flex-1 h-7 text-xs font-medium bg-accent text-white rounded-sm hover:bg-accent-hover transition-colors"
        >
          Insert
        </button>
        <button
          onClick={onClose}
          className="h-7 px-2 text-xs border border-line text-ink-muted rounded-sm hover:bg-bg-elev2 transition-colors"
        >
          Cancel
        </button>
      </div>
    </div>
  )
}
