/**
 * RemoteCursors.jsx — OFFICE-25: Remote cursor + selection rendering.
 *
 * Three sub-exports for the three editors:
 *
 *   <DocsCursorLayer editor={editor} remoteCursors={remoteCursors} />
 *     Renders remote TipTap carets/selections as absolutely-positioned
 *     overlays on the editor DOM.  No ProseMirror decoration API needed;
 *     we read .coordsAtPos() to obtain screen coordinates and render
 *     a thin colored caret line + label via a React portal-style overlay.
 *
 *   <SheetsCursorLayer remoteCursors={remoteCursors} getCellRect={fn} />
 *     Renders remote cell selection highlights (colored border) over the
 *     Fortune Sheet canvas.  getCellRect(row,col) → {top,left,width,height}
 *     or null; callers pass a ref-backed helper.
 *
 *   <SlidesCursorLayer remoteCursors={remoteCursors} activeSlideId={id} />
 *     Shows a small avatar badge on the slide thumbnail for each peer
 *     viewing that slide (handled inline in SlidesEditor via
 *     a thin hook result; this component is the label strip).
 *
 * JSX only — no .tsx.
 */

import { useEffect, useRef, useState } from 'react'

// ─── helpers ─────────────────────────────────────────────────────────────────

/** Parse "row,col" string back to numbers. */
function parseCell(str) {
  if (typeof str !== 'string') return null
  const [r, c] = str.split(',').map(Number)
  if (isNaN(r) || isNaN(c)) return null
  return { row: r, col: c }
}

/** Initials (up to 2 chars) from display name. */
function initials(name) {
  if (!name) return '?'
  const parts = name.trim().split(/\s+/)
  return parts.length >= 2
    ? (parts[0][0] + parts[1][0]).toUpperCase()
    : name.slice(0, 2).toUpperCase()
}

// ─── DocsCursorLayer ─────────────────────────────────────────────────────────

/**
 * Renders remote carets and selection highlights in a TipTap editor.
 *
 * @param {{ editor: object|null, remoteCursors: Map }} props
 */
export function DocsCursorLayer({ editor, remoteCursors }) {
  const [cursors, setCursors] = useState([])
  const containerRef = useRef(null)

  useEffect(() => {
    if (!editor || !remoteCursors || remoteCursors.size === 0) {
      setCursors([])
      return
    }

    const compute = () => {
      if (!editor.view) return
      const view = editor.view
      const dom = view.dom
      const parentRect = dom.closest('.tiptap-cursor-host')?.getBoundingClientRect()
        ?? dom.getBoundingClientRect()

      const results = []
      for (const peer of remoteCursors.values()) {
        if (peer.type !== 'doc') continue
        const from = typeof peer.from === 'number' ? peer.from : null
        const to   = typeof peer.to   === 'number' ? peer.to   : null
        if (from === null) continue

        const docSize = view.state.doc.content.size
        const safeTo   = Math.min(Math.max(to   ?? from, 0), docSize)
        const safeFrom = Math.min(Math.max(from, 0), safeTo)

        try {
          const caretPos = view.coordsAtPos(safeTo)
          const top  = caretPos.top  - parentRect.top
          const left = caretPos.left - parentRect.left

          // Selection highlight: only when from ≠ to
          let selRects = []
          if (safeFrom !== safeTo) {
            try {
              const range = document.createRange()
              const fromCoords = view.domAtPos(safeFrom)
              const toCoords   = view.domAtPos(safeTo)
              range.setStart(fromCoords.node, fromCoords.offset)
              range.setEnd(toCoords.node, toCoords.offset)
              selRects = [...range.getClientRects()].map((r) => ({
                top:    r.top    - parentRect.top,
                left:   r.left   - parentRect.left,
                width:  r.width,
                height: r.height,
              }))
            } catch {
              selRects = []
            }
          }

          results.push({ peer, top, left, selRects, safeFrom, safeTo })
        } catch {
          // coordsAtPos can throw if position is out of range; skip.
        }
      }
      setCursors(results)
    }

    // Recompute whenever the editor's transaction updates.
    const onTx = () => compute()
    editor.on('transaction', onTx)

    // Also recompute immediately (covers roster changes).
    compute()

    return () => {
      editor.off('transaction', onTx)
    }
  }, [editor, remoteCursors])

  if (cursors.length === 0) return null

  return (
    <div
      ref={containerRef}
      className="tiptap-cursor-host"
      style={{ position: 'absolute', inset: 0, pointerEvents: 'none', zIndex: 10 }}
    >
      {cursors.map(({ peer, top, left, selRects }) => (
        <span key={peer.accountId}>
          {/* Selection highlight rects */}
          {selRects.map((r, i) => (
            <span
              key={i}
              style={{
                position: 'absolute',
                top:    r.top,
                left:   r.left,
                width:  r.width,
                height: r.height,
                background: peer.color,
                opacity: 0.22,
                borderRadius: 2,
              }}
            />
          ))}

          {/* Caret line */}
          <span
            style={{
              position: 'absolute',
              top,
              left,
              width: 2,
              height: 18,
              background: peer.color,
              borderRadius: 1,
              transform: 'translateY(-1px)',
            }}
          />

          {/* Name label — serif italic per design system */}
          <span
            style={{
              position: 'absolute',
              top:  top - 20,
              left: left - 1,
              background: peer.color,
              color: '#fff',
              fontFamily: 'var(--font-serif)',
              fontStyle: 'italic',
              fontSize: 11,
              fontWeight: 500,
              letterSpacing: 'var(--tracking-tight)',
              lineHeight: '16px',
              padding: '1px 6px',
              borderRadius: '4px 4px 4px 0',
              whiteSpace: 'nowrap',
              userSelect: 'none',
              boxShadow: '0 1px 2px rgba(36,28,16,0.18)',
            }}
          >
            {peer.displayName || initials(peer.displayName)}
          </span>
        </span>
      ))}
    </div>
  )
}

// ─── SheetsCursorLayer ───────────────────────────────────────────────────────

/**
 * Renders remote cell selection borders over the Fortune Sheet workbook.
 *
 * The Fortune Sheet DOM doesn't expose a stable cell element API, so we use a
 * transparent overlay div that sits on top of the workbook and draws colored
 * borders at the computed cell position.
 *
 * getCellRect(row, col) should be a function provided by the parent component
 * that queries the Fortune Sheet DOM for the bounding rect of a cell.
 * When it returns null (cells not yet rendered) we skip that peer.
 *
 * @param {{ remoteCursors: Map, getCellRect: Function }} props
 */
export function SheetsCursorLayer({ remoteCursors, getCellRect }) {
  if (!remoteCursors || remoteCursors.size === 0) return null

  const peers = [...remoteCursors.values()].filter((p) => p.type === 'sheet')
  if (peers.length === 0) return null

  return (
    <div
      style={{
        position: 'absolute',
        inset: 0,
        pointerEvents: 'none',
        zIndex: 20,
        overflow: 'hidden',
      }}
    >
      {peers.map((peer) => {
        const cell = parseCell(peer.from)
        if (!cell) return null
        const rect = getCellRect ? getCellRect(cell.row, cell.col) : null
        if (!rect) return null

        return (
          <span key={peer.accountId}>
            {/* Cell border highlight */}
            <span
              style={{
                position: 'absolute',
                top:    rect.top,
                left:   rect.left,
                width:  rect.width,
                height: rect.height,
                border: `2px solid ${peer.color}`,
                borderRadius: 1,
                boxSizing: 'border-box',
              }}
            />
            {/* Name badge — serif italic per design system */}
            <span
              style={{
                position: 'absolute',
                top:  rect.top - 18,
                left: rect.left,
                background: peer.color,
                color: '#fff',
                fontFamily: 'var(--font-serif)',
                fontStyle: 'italic',
                fontSize: 11,
                fontWeight: 500,
                letterSpacing: 'var(--tracking-tight)',
                lineHeight: '16px',
                padding: '1px 6px',
                borderRadius: '4px 4px 4px 0',
                whiteSpace: 'nowrap',
                userSelect: 'none',
                boxShadow: '0 1px 2px rgba(36,28,16,0.18)',
              }}
            >
              {peer.displayName || '?'}
            </span>
          </span>
        )
      })}
    </div>
  )
}

// ─── SlidesCursorLayer ───────────────────────────────────────────────────────

/**
 * Returns an array of { peer, watching } for slides — used by SlidesEditor
 * to overlay avatar badges on the slide thumbnail list.
 *
 * This is a pure data helper (not a DOM overlay) since slide thumbnails are
 * rendered in a list and it's easier to inject badges inline.
 *
 * Usage:
 *   const viewers = getSlideViewers(remoteCursors, slideId)
 *   viewers.forEach(p => <Avatar peer={p} />)
 *
 * @param {Map}    remoteCursors
 * @param {string} slideId
 * @returns {Array}
 */
export function getSlideViewers(remoteCursors, slideId) {
  if (!remoteCursors || !slideId) return []
  return [...remoteCursors.values()].filter(
    (p) => p.type === 'slide' && p.slideId === slideId,
  )
}
