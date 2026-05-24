import * as XLSX from 'xlsx'
import { saveAs } from 'file-saver'

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
  // Restore merged cells from Fortune Sheet config.merge → xlsx !merges
  const mc = sheet.config?.merge
  if (mc && typeof mc === 'object') {
    ws['!merges'] = Object.values(mc).map(m => ({
      s: { r: m.r, c: m.c },
      e: { r: m.r + (m.rs || 1) - 1, c: m.c + (m.cs || 1) - 1 },
    }))
  }
  return ws
}

export function exportSheetsToXlsx(data, filename) {
  const wb = XLSX.utils.book_new()
  for (const sheet of data) {
    XLSX.utils.book_append_sheet(wb, fortuneToWorksheet(sheet), sheet.name || 'Sheet')
  }
  const buf = XLSX.write(wb, { bookType: 'xlsx', type: 'array' })
  saveAs(new Blob([buf], { type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' }), `${filename}.xlsx`)
}

export function exportSheetsToCsv(data, filename) {
  const sheet = data[0]
  if (!sheet) return
  const cells = sheet.celldata || []
  let maxR = 0, maxC = 0
  for (const { r, c } of cells) { if (r > maxR) maxR = r; if (c > maxC) maxC = c }
  const grid = Array.from({ length: maxR + 1 }, () => new Array(maxC + 1).fill(''))
  for (const { r, c, v } of cells) {
    if (!v) continue
    grid[r][c] = v.v !== undefined ? v.v : (v.m ?? '')
  }
  const csv = grid.map((row) =>
    row.map((cell) => {
      const s = String(cell)
      return s.includes(',') || s.includes('"') || s.includes('\n') ? `"${s.replace(/"/g, '""')}"` : s
    }).join(',')
  ).join('\n')
  saveAs(new Blob([csv], { type: 'text/csv;charset=utf-8' }), `${filename}.csv`)
}
