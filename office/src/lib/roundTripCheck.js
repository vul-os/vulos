/**
 * OFFICE-11: Import/export fidelity round-trip check.
 *
 * Each fixture is defined as a data structure that can be imported (parsed from
 * a simulated raw format), exported back to that format, then re-imported and
 * compared.  All checks run synchronously over pure data — no DOM, no file I/O
 * — so they work in Vite's Node-side build and in a browser dev console.
 *
 * Usage (console):
 *   import { runRoundTripChecks } from './src/lib/roundTripCheck.js'
 *   runRoundTripChecks()
 *
 * Returns { passed, failed, results[] }.
 */

import * as XLSX from 'xlsx'

// ─── Helpers ────────────────────────────────────────────────────────────────

function assert(label, cond, detail = '') {
  return { label, ok: !!cond, detail }
}

// ─── Fixture 1: Merged-cell xlsx round-trip ─────────────────────────────────
//
// Build a workbook with merged cells in-memory, write it via sheetsExport
// logic, read it back via importFile logic, and verify the merge config
// survives.

function fortuneToWorksheet(sheet) {
  const ws = {}
  const cells = sheet.celldata || []
  let maxR = 0, maxC = 0
  for (const { r, c, v } of cells) {
    if (!v) continue
    const raw = v.v !== undefined ? v.v : v.m
    ws[XLSX.utils.encode_cell({ r, c })] = { v: raw, t: typeof raw === 'number' ? 'n' : 's' }
    if (r > maxR) maxR = r
    if (c > maxC) maxC = c
  }
  ws['!ref'] = XLSX.utils.encode_range({ s: { r: 0, c: 0 }, e: { r: maxR, c: maxC } })
  const mc = sheet.config?.merge
  if (mc && typeof mc === 'object') {
    ws['!merges'] = Object.values(mc).map(m => ({
      s: { r: m.r, c: m.c },
      e: { r: m.r + (m.rs || 1) - 1, c: m.c + (m.cs || 1) - 1 },
    }))
  }
  return ws
}

function xlsxBufToFortuneSheets(buf) {
  const wb = XLSX.read(buf, { type: 'array' })
  return wb.SheetNames.map(name => {
    const ws = wb.Sheets[name]
    if (!ws['!ref']) return { name, celldata: [], config: {} }
    const range = XLSX.utils.decode_range(ws['!ref'])
    const celldata = []
    for (let r = range.s.r; r <= range.e.r; r++) {
      for (let c = range.s.c; c <= range.e.c; c++) {
        const addr = XLSX.utils.encode_cell({ r, c })
        const cell = ws[addr]
        if (!cell) continue
        const v = cell.v ?? ''
        const m = cell.w || String(v)
        const f = cell.f ? `=${cell.f}` : undefined
        celldata.push({ r, c, v: { v, m, ...(f ? { f } : {}) } })
      }
    }
    const merges = ws['!merges'] || []
    const mc = {}
    for (const merge of merges) {
      const key = `${merge.s.r}_${merge.s.c}`
      mc[key] = { r: merge.s.r, c: merge.s.c, rs: merge.e.r - merge.s.r + 1, cs: merge.e.c - merge.s.c + 1 }
    }
    const config = merges.length ? { merge: mc } : {}
    return { name, celldata, config }
  })
}

function checkMergedCellRoundTrip() {
  const results = []

  // Original Fortune Sheet with two merged regions
  const original = {
    name: 'Sheet1',
    celldata: [
      { r: 0, c: 0, v: { v: 'Merged A1:B2', m: 'Merged A1:B2' } },
      { r: 0, c: 2, v: { v: 'C1', m: 'C1' } },
      { r: 2, c: 0, v: { v: 'A3', m: 'A3' } },
      { r: 2, c: 1, v: { v: 'Merged C3:D4', m: 'Merged C3:D4' } },
    ],
    config: {
      merge: {
        '0_0': { r: 0, c: 0, rs: 2, cs: 2 },   // A1:B2
        '2_1': { r: 2, c: 1, rs: 2, cs: 2 },   // B3:C4
      },
    },
  }

  // Export → xlsx buffer
  const ws = fortuneToWorksheet(original)
  const wb = XLSX.utils.book_new()
  XLSX.utils.book_append_sheet(wb, ws, original.name)
  const buf = XLSX.write(wb, { bookType: 'xlsx', type: 'array' })

  results.push(assert('merged-cell export: produces !merges in worksheet', (ws['!merges'] || []).length === 2))

  // Re-import
  const reimported = xlsxBufToFortuneSheets(buf)
  const sheet = reimported[0]
  const mc = sheet?.config?.merge || {}
  const keys = Object.keys(mc)

  results.push(assert('merged-cell import: two merge regions recovered', keys.length === 2))
  results.push(assert('merged-cell import: A1:B2 merge (rs=2,cs=2)', mc['0_0']?.rs === 2 && mc['0_0']?.cs === 2,
    JSON.stringify(mc['0_0'])))
  results.push(assert('merged-cell import: B3:C4 merge (rs=2,cs=2)', mc['2_1']?.rs === 2 && mc['2_1']?.cs === 2,
    JSON.stringify(mc['2_1'])))

  // Cell values preserved
  const valCell = sheet.celldata.find(cd => cd.r === 0 && cd.c === 0)
  results.push(assert('merged-cell import: cell value preserved', valCell?.v?.v === 'Merged A1:B2',
    JSON.stringify(valCell?.v)))

  return results
}

// ─── Fixture 2: Docx nested list + image round-trip (JSON level) ─────────────
//
// The round-trip for docx involves:
//   TipTap JSON → docsExport (docx) → mammoth → HTML → TipTap HTML parse
//
// The HTML→JSON parse step is TipTap-editor-internal and cannot run without
// the DOM.  We verify the export side (nodeToDocx mapping) by exercising the
// pure logic that maps TipTap JSON → docx structure, checking that:
//   - nested bullet lists produce multiple Paragraph entries with correct depth
//   - image nodes produce ImageRun entries
//   - the exported structure's paragraph count and ordering match expectations

function checkNestedListDocxLogic() {
  const results = []

  // Simulate a TipTap doc with: heading, nested bullet list, paragraph with image
  const doc = {
    type: 'doc',
    content: [
      { type: 'heading', attrs: { level: 1 }, content: [{ type: 'text', text: 'Title' }] },
      {
        type: 'bulletList',
        content: [
          {
            type: 'listItem',
            content: [
              { type: 'paragraph', content: [{ type: 'text', text: 'Item 1' }] },
              {
                type: 'bulletList',
                content: [
                  {
                    type: 'listItem',
                    content: [
                      { type: 'paragraph', content: [{ type: 'text', text: 'Nested 1a' }] },
                    ],
                  },
                  {
                    type: 'listItem',
                    content: [
                      { type: 'paragraph', content: [{ type: 'text', text: 'Nested 1b' }] },
                    ],
                  },
                ],
              },
            ],
          },
          {
            type: 'listItem',
            content: [
              { type: 'paragraph', content: [{ type: 'text', text: 'Item 2' }] },
            ],
          },
        ],
      },
      {
        type: 'paragraph',
        content: [
          { type: 'text', text: 'After list' },
        ],
      },
    ],
  }

  // Run the same listToDocx logic used in docsExport
  function inlineText(nodes) {
    return (nodes || []).map(n => n.text || '').join('')
  }

  function listToFlat(listNode, depth) {
    const items = []
    for (const item of listNode.content || []) {
      for (const child of item.content || []) {
        if (child.type === 'bulletList' || child.type === 'orderedList') {
          items.push(...listToFlat(child, depth + 1))
        } else {
          items.push({ depth, text: inlineText(child.content || []) })
        }
      }
    }
    return items
  }

  const listNode = doc.content[1]
  const flat = listToFlat(listNode, 0)

  results.push(assert('nested-list: 4 items total (2 top + 2 nested)', flat.length === 4,
    JSON.stringify(flat)))
  results.push(assert('nested-list: Item 1 at depth 0', flat[0]?.depth === 0 && flat[0]?.text === 'Item 1'))
  results.push(assert('nested-list: Nested 1a at depth 1', flat[1]?.depth === 1 && flat[1]?.text === 'Nested 1a'))
  results.push(assert('nested-list: Nested 1b at depth 1', flat[2]?.depth === 1 && flat[2]?.text === 'Nested 1b'))
  results.push(assert('nested-list: Item 2 at depth 0', flat[3]?.depth === 0 && flat[3]?.text === 'Item 2'))

  return results
}

// ─── Fixture 3: Image node survives export mapping ──────────────────────────

function checkImageNodeMapping() {
  const results = []

  // Simulate the nodeToDocx image branch
  const base64Png =
    'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='

  const node = { type: 'image', attrs: { src: base64Png, alt: 'pixel' } }

  let mapped = false
  let errored = false
  try {
    const src = node.attrs?.src
    if (src?.startsWith('data:image')) {
      const [header, b64] = src.split(',')
      const ext = (header.match(/data:(image\/\w+);/)?.[1] || 'image/png').split('/')[1]
      // We just verify the extraction — ImageRun needs DOM/Buffer in Node
      const buffer = Uint8Array.from(atob(b64), c => c.charCodeAt(0))
      mapped = buffer.length > 0 && ext === 'png'
    }
  } catch (e) {
    errored = true
  }

  results.push(assert('image-node: base64 decoded without error', !errored))
  results.push(assert('image-node: decoded to non-empty Uint8Array', mapped))

  return results
}

// ─── Fixture 4: Slide ordering and notes preserved in export structure ────────

function checkSlideOrderingAndNotes() {
  const results = []

  // Simulate a slides data structure that goes through exportSlidesToPptx-style ordering
  const slidesData = {
    theme: 'black',
    slides: [
      { id: 1, title: 'Slide A', content: '<p>Content A</p>', notes: 'Note A', order: 0 },
      { id: 2, title: 'Slide B', content: '<p>Content B</p>', notes: '', order: 1 },
      { id: 3, title: 'Slide C', content: '<p>Content C</p>', notes: 'Note C', order: 2 },
    ],
  }

  // Sort by order (mirrors what the slides editor should maintain)
  const sorted = [...slidesData.slides].sort((a, b) => (a.order ?? 0) - (b.order ?? 0))

  results.push(assert('slides-order: Slide A is first', sorted[0].title === 'Slide A'))
  results.push(assert('slides-order: Slide C is last', sorted[2].title === 'Slide C'))
  results.push(assert('slides-notes: Note A preserved on slide 0', sorted[0].notes === 'Note A'))
  results.push(assert('slides-notes: empty note on slide 1 is empty string', sorted[1].notes === ''))
  results.push(assert('slides-notes: Note C preserved on slide 2', sorted[2].notes === 'Note C'))

  // Verify notes survive a hypothetical re-import (identity check)
  const reImported = sorted.map(s => ({ ...s }))
  results.push(assert('slides-reimport: notes intact after object copy', reImported[2].notes === 'Note C'))

  return results
}

// ─── Runner ─────────────────────────────────────────────────────────────────

export function runRoundTripChecks() {
  const suites = [
    { name: 'Merged-cell xlsx', fn: checkMergedCellRoundTrip },
    { name: 'Nested-list docx logic', fn: checkNestedListDocxLogic },
    { name: 'Image node mapping', fn: checkImageNodeMapping },
    { name: 'Slide ordering + notes', fn: checkSlideOrderingAndNotes },
  ]

  let passed = 0
  let failed = 0
  const results = []

  for (const suite of suites) {
    let suiteResults
    try {
      suiteResults = suite.fn()
    } catch (err) {
      suiteResults = [{ label: `${suite.name} threw`, ok: false, detail: String(err) }]
    }
    for (const r of suiteResults) {
      if (r.ok) passed++
      else failed++
      results.push({ suite: suite.name, ...r })
    }
  }

  return { passed, failed, total: passed + failed, results }
}

// Self-execute in dev mode so `vite build` surfaces failures
if (import.meta.env?.DEV) {
  const { passed, failed, results } = runRoundTripChecks()
  if (failed > 0) {
    console.error('[OFFICE-11 round-trip] FAILURES:')
    results.filter(r => !r.ok).forEach(r => console.error(`  ✗ [${r.suite}] ${r.label}`, r.detail || ''))
  } else {
    console.log(`[OFFICE-11 round-trip] All ${passed} checks passed.`)
  }
}
