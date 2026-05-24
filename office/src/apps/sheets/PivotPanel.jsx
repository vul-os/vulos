/**
 * src/apps/sheets/PivotPanel.jsx
 *
 * Pivot table side panel.
 * Reads Fortune Sheet celldata from the active sheet, lets the user configure
 * rows / columns / values (with aggregation), then renders the pivot result
 * into a new sheet appended to `data`.
 *
 * Props:
 *   data        {Sheet[]}  — current workbook sheets
 *   onClose     {fn}       — close the panel
 *   onInsert    {fn(Sheet[])} — replace workbook data with pivot sheet appended
 */
import { useState, useMemo } from 'react'
import { X, RefreshCw } from 'lucide-react'
import { Button, IconButton } from '../../components/ui'

// ── Aggregation functions ─────────────────────────────────────────────────────

const AGG = {
  SUM:    (vals) => vals.reduce((a, b) => a + (Number(b) || 0), 0),
  AVG:    (vals) => vals.length ? vals.reduce((a, b) => a + (Number(b) || 0), 0) / vals.length : 0,
  COUNT:  (vals) => vals.length,
  COUNTA: (vals) => vals.filter((v) => v !== '' && v !== null && v !== undefined).length,
  MAX:    (vals) => vals.length ? Math.max(...vals.map(Number)) : 0,
  MIN:    (vals) => vals.length ? Math.min(...vals.map(Number)) : 0,
}

// ── Extract a 2D array from Fortune Sheet celldata ────────────────────────────

function sheetToTable(sheet) {
  const cells = sheet.celldata || []
  let maxR = 0, maxC = 0
  for (const { r, c } of cells) {
    if (r > maxR) maxR = r
    if (c > maxC) maxC = c
  }
  const grid = Array.from({ length: maxR + 1 }, () => new Array(maxC + 1).fill(''))
  for (const { r, c, v } of cells) {
    if (!v) continue
    grid[r][c] = v.v !== undefined ? v.v : (v.m ?? '')
  }
  return grid
}

// ── Build pivot table ─────────────────────────────────────────────────────────

function buildPivot(table, rowField, colField, valueField, aggFn) {
  if (!table || table.length < 2) return null
  const headers = table[0]
  const rowIdx   = headers.indexOf(rowField)
  const colIdx   = headers.indexOf(colField)
  const valIdx   = headers.indexOf(valueField)
  if (rowIdx < 0 || valIdx < 0) return null

  const rows   = new Set()
  const cols   = new Set()
  const groups = {}

  for (let i = 1; i < table.length; i++) {
    const row = table[i]
    const rv  = String(row[rowIdx] ?? '')
    const cv  = colIdx >= 0 ? String(row[colIdx] ?? '') : '__value__'
    const vv  = row[valIdx]
    rows.add(rv)
    cols.add(cv)
    const key = `${rv}||${cv}`
    if (!groups[key]) groups[key] = []
    groups[key].push(vv)
  }

  const rowArr = [...rows].sort()
  const colArr = [...cols].sort()

  // Header row
  const result = [[rowField, ...colArr, 'Total']]

  for (const rv of rowArr) {
    const dataRow = [rv]
    let rowTotal = 0
    for (const cv of colArr) {
      const vals = groups[`${rv}||${cv}`] || []
      const agg  = AGG[aggFn] ? AGG[aggFn](vals) : 0
      dataRow.push(agg)
      rowTotal += Number(agg) || 0
    }
    dataRow.push(rowTotal)
    result.push(dataRow)
  }

  // Grand total row
  const totRow = ['Total']
  let grandTotal = 0
  for (const cv of colArr) {
    let colTotal = 0
    for (const rv of rowArr) {
      const vals = groups[`${rv}||${cv}`] || []
      colTotal += AGG[aggFn] ? Number(AGG[aggFn](vals)) || 0 : 0
    }
    totRow.push(colTotal)
    grandTotal += colTotal
  }
  totRow.push(grandTotal)
  result.push(totRow)

  return result
}

// ── Table → Fortune Sheet celldata ────────────────────────────────────────────

function tableToCelldata(table) {
  const celldata = []
  for (let r = 0; r < table.length; r++) {
    for (let c = 0; c < table[r].length; c++) {
      const val = table[r][c]
      if (val === '' || val === null || val === undefined) continue
      const isNum = typeof val === 'number'
      celldata.push({
        r, c,
        v: {
          v: val,
          m: String(val),
          ct: { fa: 'General', t: isNum ? 'n' : 's' },
          ...(r === 0 || c === 0 ? { bl: 1 } : {}),
        },
      })
    }
  }
  return celldata
}

// ── Component ─────────────────────────────────────────────────────────────────

export default function PivotPanel({ data, onClose, onInsert }) {
  const activeSheet = data?.[0]
  const table       = useMemo(() => sheetToTable(activeSheet || {}), [activeSheet])
  const headers     = table.length > 0 ? table[0].map(String).filter(Boolean) : []

  const [rowField,   setRowField]   = useState(headers[0] || '')
  const [colField,   setColField]   = useState(headers[1] || '')
  const [valueField, setValueField] = useState(headers[2] || headers[1] || '')
  const [aggFn,      setAggFn]      = useState('SUM')
  const [preview,    setPreview]    = useState(null)

  function handlePreview() {
    const result = buildPivot(table, rowField, colField, valueField, aggFn)
    setPreview(result)
  }

  function handleInsert() {
    const result = buildPivot(table, rowField, colField, valueField, aggFn)
    if (!result) return
    const celldata = tableToCelldata(result)
    const pivotSheet = {
      name: `Pivot_${rowField}_${valueField}`.slice(0, 31),
      celldata,
      config: {},
    }
    onInsert([...data, pivotSheet])
    onClose()
  }

  const sel = 'w-full rounded-md border border-line bg-bg px-2 py-1.5 text-xs text-ink focus:outline-none focus:border-line-strong'

  return (
    <div className="flex flex-col w-72 h-full border-l border-line bg-paper overflow-y-auto">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b border-line">
        <span className="text-xs font-semibold text-ink tracking-tightish">Pivot table</span>
        <IconButton size="xs" onClick={onClose}><X size={13} /></IconButton>
      </div>

      <div className="flex-1 px-3 py-3 space-y-4 text-xs">
        {headers.length === 0 && (
          <p className="text-ink-faint">No data in first sheet. Add headers and rows first.</p>
        )}

        {headers.length > 0 && (
          <>
            <div className="space-y-1">
              <label className="block text-ink-muted font-medium">Row field</label>
              <select value={rowField} onChange={(e) => setRowField(e.target.value)} className={sel}>
                {headers.map((h) => <option key={h} value={h}>{h}</option>)}
              </select>
            </div>

            <div className="space-y-1">
              <label className="block text-ink-muted font-medium">Column field</label>
              <select value={colField} onChange={(e) => setColField(e.target.value)} className={sel}>
                <option value="">— none —</option>
                {headers.map((h) => <option key={h} value={h}>{h}</option>)}
              </select>
            </div>

            <div className="space-y-1">
              <label className="block text-ink-muted font-medium">Value field</label>
              <select value={valueField} onChange={(e) => setValueField(e.target.value)} className={sel}>
                {headers.map((h) => <option key={h} value={h}>{h}</option>)}
              </select>
            </div>

            <div className="space-y-1">
              <label className="block text-ink-muted font-medium">Aggregation</label>
              <select value={aggFn} onChange={(e) => setAggFn(e.target.value)} className={sel}>
                {Object.keys(AGG).map((k) => <option key={k} value={k}>{k}</option>)}
              </select>
            </div>

            <div className="flex gap-2">
              <Button variant="secondary" size="sm" onClick={handlePreview} className="flex-1">
                <RefreshCw size={11} className="mr-1" /> Preview
              </Button>
              <Button variant="primary" size="sm" onClick={handleInsert} className="flex-1">
                Insert
              </Button>
            </div>

            {preview && (
              <div className="overflow-auto border border-line rounded-md">
                <table className="text-xs w-full border-collapse">
                  <thead>
                    <tr>
                      {preview[0].map((h, i) => (
                        <th key={i} className="px-1.5 py-1 border border-line bg-bg text-ink-muted font-semibold text-left whitespace-nowrap">
                          {String(h)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {preview.slice(1).map((row, ri) => (
                      <tr key={ri} className={ri % 2 === 0 ? '' : 'bg-bg'}>
                        {row.map((cell, ci) => (
                          <td key={ci} className="px-1.5 py-0.5 border border-line text-ink whitespace-nowrap">
                            {typeof cell === 'number' ? cell.toLocaleString(undefined, { maximumFractionDigits: 4 }) : String(cell)}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
