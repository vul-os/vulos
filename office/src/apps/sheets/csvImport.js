/**
 * src/apps/sheets/csvImport.js
 *
 * CSV import — parses CSV text (quoted fields, custom delimiters) into
 * a Fortune Sheet celldata array, ready to merge as a new sheet.
 */

/**
 * parseCSV(text, delimiter) → string[][]
 * RFC 4180-compliant parser: handles quoted fields, embedded newlines,
 * escaped double-quotes (""). Returns a 2D array of raw strings.
 */
export function parseCSV(text, delimiter = ',') {
  const rows = []
  let col = []
  let i = 0
  const n = text.length

  while (i < n) {
    if (text[i] === '"') {
      // Quoted field.
      let val = ''
      i++ // skip opening quote
      while (i < n) {
        if (text[i] === '"') {
          if (text[i + 1] === '"') {
            val += '"'
            i += 2
          } else {
            i++ // skip closing quote
            break
          }
        } else {
          val += text[i]
          i++
        }
      }
      col.push(val)
      // Skip delimiter or newline after quoted field.
      if (i < n && text[i] === delimiter) i++
      else if (i < n && text[i] === '\r') { i++; if (text[i] === '\n') i++; rows.push(col); col = [] }
      else if (i < n && text[i] === '\n') { i++; rows.push(col); col = [] }
    } else {
      // Unquoted field — scan to delimiter or newline.
      let start = i
      while (i < n && text[i] !== delimiter && text[i] !== '\n' && text[i] !== '\r') i++
      col.push(text.slice(start, i))
      if (i < n && text[i] === delimiter) {
        i++
      } else if (i < n && text[i] === '\r') {
        i++
        if (text[i] === '\n') i++
        rows.push(col); col = []
      } else if (i < n && text[i] === '\n') {
        i++
        rows.push(col); col = []
      }
    }
  }
  if (col.length > 0) rows.push(col)
  return rows
}

/**
 * csvToSheet(text, sheetName, delimiter) → Fortune Sheet Sheet object.
 */
export function csvToSheet(text, sheetName = 'Imported', delimiter = ',') {
  const rows = parseCSV(text, delimiter)
  const celldata = []
  for (let r = 0; r < rows.length; r++) {
    for (let c = 0; c < rows[r].length; c++) {
      const raw = rows[r][c]
      if (raw === '') continue
      const num = Number(raw)
      const isNum = raw !== '' && !isNaN(num) && raw.trim() !== ''
      celldata.push({
        r, c,
        v: {
          v: isNum ? num : raw,
          m: raw,
          ct: { fa: 'General', t: isNum ? 'n' : 's' },
        },
      })
    }
  }
  return { name: sheetName, celldata, config: {} }
}

/**
 * importCSVFile(file, delimiter) → Promise<Sheet>
 * Reads a File object and returns a Fortune Sheet sheet.
 */
export function importCSVFile(file, delimiter = ',') {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = (e) => {
      try {
        const sheet = csvToSheet(e.target.result, file.name.replace(/\.csv$/i, ''), delimiter)
        resolve(sheet)
      } catch (err) {
        reject(err)
      }
    }
    reader.onerror = () => reject(new Error('Failed to read file'))
    reader.readAsText(file, 'UTF-8')
  })
}

/**
 * sheetsToCSV(sheet) → CSV string for the given Fortune Sheet sheet.
 */
export function sheetsToCSV(sheet) {
  const cells = sheet.celldata || []
  let maxR = 0, maxC = 0
  for (const { r, c } of cells) {
    if (r > maxR) maxR = r
    if (c > maxC) maxC = c
  }
  const grid = Array.from({ length: maxR + 1 }, () => new Array(maxC + 1).fill(''))
  for (const { r, c, v } of cells) {
    if (!v) continue
    const val = v.v !== undefined ? v.v : (v.m ?? '')
    grid[r][c] = String(val)
  }
  return grid.map((row) =>
    row.map((cell) => {
      if (cell.includes(',') || cell.includes('"') || cell.includes('\n')) {
        return `"${cell.replace(/"/g, '""')}"`
      }
      return cell
    }).join(',')
  ).join('\n')
}
