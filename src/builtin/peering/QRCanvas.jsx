import { useEffect, useRef } from 'react'

// Tiny inline QR code generator — no external dependency.
// Implements QR Version 1–3, byte mode, ECC level M.
// Sufficient for Vula IDs (~50-80 chars).

// ---------------------------------------------------------------------------
// Reed-Solomon GF(256) with primitive polynomial 0x11d
// ---------------------------------------------------------------------------
const GF_EXP = new Uint8Array(512)
const GF_LOG = new Uint8Array(256)
;(function () {
  let x = 1
  for (let i = 0; i < 255; i++) {
    GF_EXP[i] = x
    GF_LOG[x] = i
    x <<= 1
    if (x & 0x100) x ^= 0x11d
  }
  for (let i = 255; i < 512; i++) GF_EXP[i] = GF_EXP[i - 255]
})()

function gfMul(a, b) {
  if (a === 0 || b === 0) return 0
  return GF_EXP[(GF_LOG[a] + GF_LOG[b]) % 255]
}

function rsGeneratorPoly(eccCount) {
  let g = [1]
  for (let i = 0; i < eccCount; i++) {
    const factor = [1, GF_EXP[i]]
    const result = new Array(g.length + factor.length - 1).fill(0)
    for (let j = 0; j < g.length; j++)
      for (let k = 0; k < factor.length; k++)
        result[j + k] ^= gfMul(g[j], factor[k])
    g = result
  }
  return g
}

function rsEncode(data, eccCount) {
  const gen = rsGeneratorPoly(eccCount)
  const msg = [...data, ...new Array(eccCount).fill(0)]
  for (let i = 0; i < data.length; i++) {
    const coef = msg[i]
    if (coef !== 0)
      for (let j = 1; j < gen.length; j++)
        msg[i + j] ^= gfMul(gen[j], coef)
  }
  return msg.slice(data.length)
}

// ---------------------------------------------------------------------------
// QR Version table (ECC level M)
// ver: [dataCodewords, eccPerBlock, blocksGroup1, dcPerBlock1, blocksGroup2, dcPerBlock2]
// ---------------------------------------------------------------------------
const QR_VERSIONS = [
  null, // 0-indexed padding
  { dc: 16, ecc: 10, b1: 1, d1: 16, b2: 0, d2: 0 }, // V1
  { dc: 28, ecc: 16, b1: 1, d1: 28, b2: 0, d2: 0 }, // V2
  { dc: 44, ecc: 26, b1: 1, d1: 44, b2: 0, d2: 0 }, // V3
  { dc: 64, ecc: 18, b1: 2, d1: 32, b2: 0, d2: 0 }, // V4
  { dc: 86, ecc: 24, b1: 2, d1: 43, b2: 0, d2: 0 }, // V5
  { dc: 108, ecc: 16, b1: 2, d1: 27, b2: 2, d2: 28 }, // V6
  { dc: 124, ecc: 18, b1: 4, d1: 31, b2: 0, d2: 0 }, // V7
  { dc: 154, ecc: 22, b1: 2, d1: 38, b2: 2, d2: 39 }, // V8
  { dc: 182, ecc: 22, b1: 3, d1: 36, b2: 2, d2: 37 }, // V9
  { dc: 216, ecc: 26, b1: 4, d1: 43, b2: 1, d2: 44 }, // V10
]

function chooseVersion(byteCount) {
  for (let v = 1; v <= 10; v++) {
    if (!QR_VERSIONS[v]) continue
    // Byte mode capacity for ECC M
    if (QR_VERSIONS[v].dc >= byteCount + 4) return v // rough check, header is 4 codewords overhead
  }
  return 10 // fallback
}

// ---------------------------------------------------------------------------
// Build data codewords: byte mode
// ---------------------------------------------------------------------------
function buildData(text, ver) {
  const bytes = new TextEncoder().encode(text)
  const vInfo = QR_VERSIONS[ver]
  const bits = []

  // Mode indicator: 0100 = byte mode
  bits.push(0, 1, 0, 0)

  // Character count indicator (8 bits for V1-9, 16 for V10-26)
  const ccBits = ver <= 9 ? 8 : 16
  for (let i = ccBits - 1; i >= 0; i--) bits.push((bytes.length >> i) & 1)

  // Data bits
  for (const b of bytes)
    for (let i = 7; i >= 0; i--) bits.push((b >> i) & 1)

  // Terminator
  const maxBits = vInfo.dc * 8
  for (let i = 0; i < 4 && bits.length < maxBits; i++) bits.push(0)

  // Pad to byte boundary
  while (bits.length % 8 !== 0) bits.push(0)

  // Pad codewords
  const padWords = [0xec, 0x11]
  let pi = 0
  while (bits.length < maxBits) {
    const w = padWords[pi++ % 2]
    for (let i = 7; i >= 0; i--) bits.push((w >> i) & 1)
  }

  // Convert to bytes
  const codewords = []
  for (let i = 0; i < bits.length; i += 8) {
    let b = 0
    for (let j = 0; j < 8; j++) b = (b << 1) | (bits[i + j] || 0)
    codewords.push(b)
  }
  return codewords
}

// ---------------------------------------------------------------------------
// Interleave blocks and add ECC
// ---------------------------------------------------------------------------
function buildMessage(text, ver) {
  const vInfo = QR_VERSIONS[ver]
  const data = buildData(text, ver)

  const blocks = []
  let offset = 0
  for (let g = 0; g < 2; g++) {
    const count = g === 0 ? vInfo.b1 : vInfo.b2
    const size  = g === 0 ? vInfo.d1 : vInfo.d2
    if (count === 0) continue
    for (let b = 0; b < count; b++) {
      const dc = data.slice(offset, offset + size)
      offset += size
      const ec = rsEncode(dc, vInfo.ecc)
      blocks.push({ dc, ec })
    }
  }

  // Interleave data
  const maxDc = Math.max(...blocks.map(b => b.dc.length))
  const result = []
  for (let i = 0; i < maxDc; i++)
    for (const blk of blocks)
      if (i < blk.dc.length) result.push(blk.dc[i])

  // Interleave ECC
  for (let i = 0; i < vInfo.ecc; i++)
    for (const blk of blocks)
      if (i < blk.ec.length) result.push(blk.ec[i])

  return result
}

// ---------------------------------------------------------------------------
// Matrix construction
// ---------------------------------------------------------------------------
const DARK = 1, LIGHT = 0, FUNC = 2 // FUNC = reserved function module

function makeMatrix(ver) {
  const size = ver * 4 + 17
  const mat = Array.from({ length: size }, () => new Int8Array(size).fill(-1))
  return mat
}

function setModule(mat, row, col, dark) {
  mat[row][col] = dark ? DARK : LIGHT
}

function addFinderPattern(mat, row, col) {
  for (let r = -1; r <= 7; r++) {
    for (let c = -1; c <= 7; c++) {
      const mr = row + r, mc = col + c
      if (mr < 0 || mr >= mat.length || mc < 0 || mc >= mat.length) continue
      const dark = (r >= 0 && r <= 6 && (c === 0 || c === 6)) ||
                   (c >= 0 && c <= 6 && (r === 0 || r === 6)) ||
                   (r >= 2 && r <= 4 && c >= 2 && c <= 4)
      mat[mr][mc] = dark ? DARK : LIGHT
    }
  }
}

function addAlignmentPattern(mat, row, col) {
  for (let r = -2; r <= 2; r++) {
    for (let c = -2; c <= 2; c++) {
      const dark = Math.abs(r) === 2 || Math.abs(c) === 2 || (r === 0 && c === 0)
      mat[row + r][col + c] = dark ? DARK : LIGHT
    }
  }
}

// Alignment pattern centers by version
const ALIGN_POS = [
  [], [], [6, 18], [6, 22], [6, 26], [6, 30], [6, 34],
  [6, 22, 38], [6, 24, 42], [6, 26, 46], [6, 28, 50],
]

function setupFunctionModules(mat, ver) {
  const size = mat.length

  // Finder patterns
  addFinderPattern(mat, 0, 0)
  addFinderPattern(mat, 0, size - 7)
  addFinderPattern(mat, size - 7, 0)

  // Separators (already covered by finder borders being LIGHT)

  // Timing patterns
  for (let i = 8; i < size - 8; i++) {
    mat[6][i] = i % 2 === 0 ? DARK : LIGHT
    mat[i][6] = i % 2 === 0 ? DARK : LIGHT
  }

  // Dark module
  mat[size - 8][8] = DARK

  // Alignment patterns
  const pos = ALIGN_POS[ver] || []
  for (const r of pos) {
    for (const c of pos) {
      if (mat[r][c] === -1) addAlignmentPattern(mat, r, c)
    }
  }

  // Reserve format info areas
  for (let i = 0; i < 9; i++) {
    if (mat[8][i] === -1) mat[8][i] = LIGHT
    if (mat[i][8] === -1) mat[i][8] = LIGHT
  }
  for (let i = 0; i < 8; i++) {
    if (mat[8][size - 1 - i] === -1) mat[8][size - 1 - i] = LIGHT
    if (mat[size - 1 - i][8] === -1) mat[size - 1 - i][8] = LIGHT
  }
}

// ---------------------------------------------------------------------------
// Place data bits
// ---------------------------------------------------------------------------
function placeData(mat, message) {
  const size = mat.length
  const bits = []
  for (const byte of message)
    for (let i = 7; i >= 0; i--) bits.push((byte >> i) & 1)

  let bitIdx = 0
  let goingUp = true
  let col = size - 1

  while (col > 0) {
    if (col === 6) col-- // skip timing column

    for (let rowStep = 0; rowStep < size; rowStep++) {
      const row = goingUp ? size - 1 - rowStep : rowStep
      for (let dc = 0; dc <= 1; dc++) {
        const c = col - dc
        if (mat[row][c] === -1) {
          mat[row][c] = bitIdx < bits.length ? bits[bitIdx++] : 0
        }
      }
    }
    goingUp = !goingUp
    col -= 2
  }
}

// ---------------------------------------------------------------------------
// Masking (pattern 0: (i+j) % 2 == 0)
// ---------------------------------------------------------------------------
function applyMask(mat, pattern) {
  const size = mat.length
  const maskFn = [
    (r, c) => (r + c) % 2 === 0,
    (r) => r % 2 === 0,
    (_r, c) => c % 3 === 0,
    (r, c) => (r + c) % 3 === 0,
    (r, c) => (Math.floor(r / 2) + Math.floor(c / 3)) % 2 === 0,
    (r, c) => ((r * c) % 2 + (r * c) % 3) === 0,
    (r, c) => ((r * c) % 2 + (r * c) % 3) % 2 === 0,
    (r, c) => ((r + c) % 2 + (r * c) % 3) % 2 === 0,
  ][pattern]

  for (let r = 0; r < size; r++) {
    for (let c = 0; c < size; c++) {
      // Only mask data modules (value 0 or 1, not function modules which are 0/1 too — but we used -1 for empty)
      // After placement, mat contains 0/1. Function modules placed first with 0/1.
      // We skip timing/finder by checking if it was set during function setup.
      // Simple approach: re-mark function modules after masking won't work;
      // instead, store function module positions separately.
      // For simplicity, use isFunctionModule helper:
      if (!isFunctionModule(mat, r, c, size)) {
        if (maskFn(r, c)) mat[r][c] ^= 1
      }
    }
  }
}

function isFunctionModule(mat, r, c, size) {
  // Finder patterns and separators (top-left, top-right, bottom-left)
  if (r < 9 && c < 9) return true
  if (r < 9 && c >= size - 8) return true
  if (r >= size - 8 && c < 9) return true
  // Timing strips
  if (r === 6 || c === 6) return true
  // Dark module
  if (r === size - 8 && c === 8) return true
  return false
}

// ---------------------------------------------------------------------------
// Format information (ECC level M = 01, mask pattern 0 = 000)
// ECC M + mask 0 => format bits = 101010000010010
// Pre-computed XOR with mask 101010000010010:
// Raw: 01 000 = 01000 => BCH encoded => 0100000101101
// With mask XOR 101010000010010 => 101000000100111
// ---------------------------------------------------------------------------
function writeFormatInfo(mat) {
  const size = mat.length
  // Format string for ECC level M (01) mask 0 (000)
  // Pre-computed: 101000000100111 (15 bits)
  const fmt = 0b101000000100111
  const bits = []
  for (let i = 14; i >= 0; i--) bits.push((fmt >> i) & 1)

  // Top-left horizontal (cols 0-5, skip 6 timing, col 7, col 8)
  const cols = [0, 1, 2, 3, 4, 5, 7, 8, 8, 8, 8, 8, 8, 8, 8]
  const rows1 = [8, 8, 8, 8, 8, 8, 8, 8, 7, 5, 4, 3, 2, 1, 0]
  for (let i = 0; i < 15; i++) {
    mat[rows1[i]][cols[i]] = bits[i]
    mat[cols[i]][rows1[i]] = bits[i]
  }

  // Top-right and bottom-left copies
  for (let i = 0; i < 8; i++) {
    mat[8][size - 1 - i] = bits[i]
    mat[size - 1 - i][8] = bits[i]
  }
  mat[size - 8][8] = DARK // dark module
}

// ---------------------------------------------------------------------------
// Main QR generator
// ---------------------------------------------------------------------------
function generateQR(text) {
  const ver = chooseVersion(new TextEncoder().encode(text).length)
  const size = ver * 4 + 17

  const mat = makeMatrix(ver)
  setupFunctionModules(mat, ver)

  // Build and place message
  const message = buildMessage(text, ver)
  placeData(mat, message)
  applyMask(mat, 0)
  writeFormatInfo(mat)

  return { mat, size }
}

// ---------------------------------------------------------------------------
// React component
// ---------------------------------------------------------------------------
export default function QRCanvas({ value, size = 200, darkColor = '#fff', lightColor = '#111827' }) {
  const canvasRef = useRef(null)

  useEffect(() => {
    if (!value || !canvasRef.current) return
    try {
      const { mat, size: modules } = generateQR(value)
      const canvas = canvasRef.current
      const ctx = canvas.getContext('2d')
      const scale = Math.floor(size / (modules + 8)) // 4 module quiet zone each side
      const quiet = 4
      const px = scale
      const total = (modules + quiet * 2) * px

      canvas.width = total
      canvas.height = total

      ctx.fillStyle = lightColor
      ctx.fillRect(0, 0, total, total)

      ctx.fillStyle = darkColor
      for (let r = 0; r < modules; r++) {
        for (let c = 0; c < modules; c++) {
          if (mat[r][c] === 1) {
            ctx.fillRect((c + quiet) * px, (r + quiet) * px, px, px)
          }
        }
      }
    } catch (e) {
      // If QR generation fails, show a fallback
      const canvas = canvasRef.current
      if (!canvas) return
      const ctx = canvas.getContext('2d')
      canvas.width = size
      canvas.height = size
      ctx.fillStyle = lightColor
      ctx.fillRect(0, 0, size, size)
      ctx.fillStyle = darkColor
      ctx.font = `${size * 0.08}px monospace`
      ctx.fillText('QR unavailable', size * 0.05, size * 0.5)
    }
  }, [value, size, darkColor, lightColor])

  return (
    <canvas
      ref={canvasRef}
      style={{ width: size, height: size, imageRendering: 'pixelated' }}
    />
  )
}
