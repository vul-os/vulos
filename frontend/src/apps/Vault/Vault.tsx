import { useState, useEffect, useCallback, useRef, type CSSProperties, type ReactNode, type FormEvent, type ChangeEvent } from 'react'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// errorMessage pulls a server-supplied `{ error: string }` (or `{ message }`)
// out of an untrusted JSON body without ever casting the body to a nicer
// shape.
function errorMessage(x: unknown): string | undefined {
  if (!isRecord(x)) return undefined
  if (typeof x.error === 'string') return x.error
  if (typeof x.message === 'string') return x.message
  return undefined
}

// caughtMessage mirrors the exact `err?.message || fallback` shape used
// throughout this file's catch blocks, now that `catch` binds `unknown`
// under strict mode instead of the old implicit `any`.
function caughtMessage(err: unknown, fallback: string): string {
  return isRecord(err) && typeof err.message === 'string' && err.message ? err.message : fallback
}

// A vault entry as this file uses it. Every field is optional because it is
// narrowed from untrusted server JSON (see toVaultEntry) — nothing here is
// ever cast to this shape, only assembled into it field-by-field. The list
// endpoint and the single-entry endpoint return overlapping shapes (the list
// omits password/notes), so one type covers both.
interface VaultEntry {
  id?: string
  title?: string
  name?: string
  username?: string
  email?: string
  url?: string
  password?: string
  notes?: string
}

function toVaultEntry(x: unknown): VaultEntry {
  if (!isRecord(x)) return {}
  return {
    id: x.id !== undefined && x.id !== null ? String(x.id) : undefined,
    title: typeof x.title === 'string' ? x.title : undefined,
    name: typeof x.name === 'string' ? x.name : undefined,
    username: typeof x.username === 'string' ? x.username : undefined,
    email: typeof x.email === 'string' ? x.email : undefined,
    url: typeof x.url === 'string' ? x.url : undefined,
    password: typeof x.password === 'string' ? x.password : undefined,
    notes: typeof x.notes === 'string' ? x.notes : undefined,
  }
}

interface VaultImportResult {
  imported?: number
  skipped?: number
  errors?: number
  parsed?: number
  warnings?: string[]
}

function toVaultImportResult(x: unknown): VaultImportResult {
  if (!isRecord(x)) return {}
  return {
    imported: typeof x.imported === 'number' ? x.imported : undefined,
    skipped: typeof x.skipped === 'number' ? x.skipped : undefined,
    errors: typeof x.errors === 'number' ? x.errors : undefined,
    parsed: typeof x.parsed === 'number' ? x.parsed : undefined,
    warnings: Array.isArray(x.warnings) ? x.warnings.filter((w): w is string => typeof w === 'string') : undefined,
  }
}

// ── Inactivity relock timer (5 minutes) ──────────────────────────────────────
const RELOCK_MS = 5 * 60 * 1000

function useRelockTimer(locked: boolean, onLock: () => void): void {
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  const reset = useCallback(() => {
    if (locked) return
    clearTimeout(timerRef.current)
    timerRef.current = setTimeout(onLock, RELOCK_MS)
  }, [locked, onLock])

  useEffect(() => {
    if (locked) { clearTimeout(timerRef.current); return }
    const events = ['mousemove', 'mousedown', 'keydown', 'touchstart', 'scroll']
    events.forEach(e => window.addEventListener(e, reset, { passive: true }))
    reset()
    return () => {
      clearTimeout(timerRef.current)
      events.forEach(e => window.removeEventListener(e, reset))
    }
  }, [locked, reset])
}

// ── Password generator hook ───────────────────────────────────────────────────
interface GeneratorOpts {
  length: number
  upper: boolean
  lower: boolean
  digits: boolean
  symbols: boolean
}

type GeneratorBoolField = 'upper' | 'lower' | 'digits' | 'symbols'

// Character-set toggle buttons the generator panel renders. A typed const
// (rather than an inline array literal) so each `field` keeps its
// GeneratorBoolField literal type without a cast.
const GENERATOR_TOGGLES: { field: GeneratorBoolField; label: string }[] = [
  { field: 'upper', label: 'A–Z' },
  { field: 'lower', label: 'a–z' },
  { field: 'digits', label: '0–9' },
  { field: 'symbols', label: '!@#' },
]

function useGenerator() {
  const [opts, setOpts] = useState<GeneratorOpts>({ length: 20, upper: true, lower: true, digits: true, symbols: true })
  const [result, setResult] = useState('')
  const [loading, setLoading] = useState(false)

  const generate = useCallback(async () => {
    setLoading(true)
    try {
      const res = await fetch('/api/auth/vault/generate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(opts),
      })
      if (res.ok) {
        const data: unknown = await res.json()
        const password = isRecord(data) && typeof data.password === 'string' ? data.password
          : isRecord(data) && typeof data.value === 'string' ? data.value
          : ''
        setResult(password)
      }
    } catch { /* ignore */ }
    setLoading(false)
  }, [opts])

  return { opts, setOpts, result, setResult, loading, generate }
}

// ── Copy helper with confirm flash ───────────────────────────────────────────
function useCopy() {
  const [copied, setCopied] = useState<string | null>(null) // key string
  const copy = useCallback(async (text: string, key: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(key)
      setTimeout(() => setCopied(null), 1800)
    } catch { /* ignore */ }
  }, [])
  return { copied, copy }
}

// ── Password strength estimate (presentational only) ─────────────────────────
// Rough, on-device heuristic used purely to colour the generator's strength bar.
// `bar` fills the four segments; `text` labels them. They are two colours on
// purpose: one value cannot be both, and this component was drawing the fill
// hue as a 12px label — "Weak" in --status-danger measured 3.69:1 on light.
function estimateStrength(pw: string): { score: number; label: string; bar: string; text: string } {
  if (!pw) return { score: 0, label: '', bar: 'var(--text-muted)', text: 'var(--text-muted)' }
  let variety = 0
  if (/[a-z]/.test(pw)) variety++
  if (/[A-Z]/.test(pw)) variety++
  if (/[0-9]/.test(pw)) variety++
  if (/[^A-Za-z0-9]/.test(pw)) variety++
  const len = pw.length
  let score = 1
  if (len >= 12 && variety >= 3) score = 3
  else if (len >= 10 && variety >= 2) score = 2
  if (len >= 16 && variety >= 3) score = 4
  const meta = [
    { label: '', bar: 'var(--text-muted)', text: 'var(--text-muted)' },
    { label: 'Weak', bar: 'var(--status-danger)', text: 'var(--status-danger-text)' },
    { label: 'Fair', bar: 'var(--status-warning)', text: 'var(--status-warning-text)' },
    { label: 'Strong', bar: 'var(--status-success)', text: 'var(--status-success-text)' },
    { label: 'Excellent', bar: 'var(--status-success)', text: 'var(--status-success-text)' },
  ][score]
  return { score, label: meta.label, bar: meta.bar, text: meta.text }
}

// ── Small icon components ─────────────────────────────────────────────────────
const IconLock = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path fillRule="evenodd" d="M10 1a4.5 4.5 0 00-4.5 4.5V9H5a2 2 0 00-2 2v6a2 2 0 002 2h10a2 2 0 002-2v-6a2 2 0 00-2-2h-.5V5.5A4.5 4.5 0 0010 1zm3 8V5.5a3 3 0 10-6 0V9h6z" clipRule="evenodd" />
  </svg>
)

const IconUnlock = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path d="M10 2a5 5 0 00-5 5v2a2 2 0 00-2 2v5a2 2 0 002 2h10a2 2 0 002-2v-5a2 2 0 00-2-2H7V7a3 3 0 015.905-.75 1 1 0 001.937-.5A5.002 5.002 0 0010 2z" />
  </svg>
)

const IconSearch = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" className="w-4 h-4">
    <circle cx="7" cy="7" r="5" />
    <path d="M11 11l3.5 3.5" strokeLinecap="round" />
  </svg>
)

const IconPlus = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path fillRule="evenodd" d="M10 3a1 1 0 011 1v5h5a1 1 0 110 2h-5v5a1 1 0 11-2 0v-5H4a1 1 0 110-2h5V4a1 1 0 011-1z" clipRule="evenodd" />
  </svg>
)

const IconEye = ({ open }: { open: boolean }) => open ? (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path d="M10 12a2 2 0 100-4 2 2 0 000 4z" />
    <path fillRule="evenodd" d="M.458 10C1.732 5.943 5.522 3 10 3s8.268 2.943 9.542 7c-1.274 4.057-5.064 7-9.542 7S1.732 14.057.458 10zM14 10a4 4 0 11-8 0 4 4 0 018 0z" clipRule="evenodd" />
  </svg>
) : (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path fillRule="evenodd" d="M3.707 2.293a1 1 0 00-1.414 1.414l14 14a1 1 0 001.414-1.414l-1.473-1.473A10.014 10.014 0 0019.542 10C18.268 5.943 14.478 3 10 3a9.958 9.958 0 00-4.512 1.074l-1.78-1.781zm4.261 4.26l1.514 1.515a2.003 2.003 0 012.45 2.45l1.514 1.514a4 4 0 00-5.478-5.478z" clipRule="evenodd" />
    <path d="M12.454 16.697L9.75 13.992a4 4 0 01-3.742-3.741L2.335 6.578A9.98 9.98 0 00.458 10c1.274 4.057 5.064 7 9.542 7 .847 0 1.669-.105 2.454-.303z" />
  </svg>
)

const IconCopy = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path d="M8 2a1 1 0 000 2h2a1 1 0 100-2H8z" />
    <path d="M3 5a2 2 0 012-2 3 3 0 003 3h2a3 3 0 003-3 2 2 0 012 2v6h-4.586l1.293-1.293a1 1 0 00-1.414-1.414l-3 3a1 1 0 000 1.414l3 3a1 1 0 001.414-1.414L10.414 13H15v3a2 2 0 01-2 2H5a2 2 0 01-2-2V5zM15 11h2a1 1 0 110 2h-2v-2z" />
  </svg>
)

const IconTrash = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path fillRule="evenodd" d="M9 2a1 1 0 00-.894.553L7.382 4H4a1 1 0 000 2v10a2 2 0 002 2h8a2 2 0 002-2V6a1 1 0 100-2h-3.382l-.724-1.447A1 1 0 0011 2H9zM7 8a1 1 0 012 0v6a1 1 0 11-2 0V8zm5-1a1 1 0 00-1 1v6a1 1 0 102 0V8a1 1 0 00-1-1z" clipRule="evenodd" />
  </svg>
)

const IconEdit = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path d="M13.586 3.586a2 2 0 112.828 2.828l-.793.793-2.828-2.828.793-.793zM11.379 5.793L3 14.172V17h2.828l8.38-8.379-2.83-2.828z" />
  </svg>
)

const IconKey = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path fillRule="evenodd" d="M18 8a6 6 0 01-7.743 5.743L10 14l-1 1-1 1H6v2H2v-4l4.257-4.257A6 6 0 1118 8zm-6-4a1 1 0 100 2 2 2 0 012 2 1 1 0 102 0 4 4 0 00-4-4z" clipRule="evenodd" />
  </svg>
)

const IconBack = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path fillRule="evenodd" d="M9.707 16.707a1 1 0 01-1.414 0l-6-6a1 1 0 010-1.414l6-6a1 1 0 011.414 1.414L5.414 9H17a1 1 0 110 2H5.414l4.293 4.293a1 1 0 010 1.414z" clipRule="evenodd" />
  </svg>
)

const IconWand = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path fillRule="evenodd" d="M5 2a1 1 0 011 1v1h1a1 1 0 010 2H6v1a1 1 0 01-2 0V6H3a1 1 0 010-2h1V3a1 1 0 011-1zm0 10a1 1 0 011 1v1h1a1 1 0 110 2H6v1a1 1 0 11-2 0v-1H3a1 1 0 110-2h1v-1a1 1 0 011-1zM12 2a1 1 0 01.967.744L14.146 7.2 17.5 9.134a1 1 0 010 1.732l-3.354 1.935-1.18 4.455a1 1 0 01-1.933 0L9.854 12.8 6.5 10.866a1 1 0 010-1.732l3.354-1.935 1.18-4.455A1 1 0 0112 2z" clipRule="evenodd" />
  </svg>
)

const IconTransfer = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4">
    <path fillRule="evenodd" d="M3 5a1 1 0 011-1h9.586l-1.293-1.293a1 1 0 111.414-1.414l3 3a1 1 0 010 1.414l-3 3a1 1 0 11-1.414-1.414L13.586 6H4a1 1 0 01-1-1zm14 10a1 1 0 01-1 1H6.414l1.293 1.293a1 1 0 11-1.414 1.414l-3-3a1 1 0 010-1.414l3-3a1 1 0 111.414 1.414L6.414 14H16a1 1 0 011 1z" clipRule="evenodd" />
  </svg>
)

// ── Import / Export helpers ──────────────────────────────────────────────────

interface ImportFormatOption {
  value: string
  label: string
  ext: string
}

// The formats a user can bring a vault in from. `ext` drives the file picker.
const IMPORT_FORMATS: ImportFormatOption[] = [
  { value: 'bitwarden', label: 'Bitwarden (.json)', ext: '.json' },
  { value: 'chrome-csv', label: 'Chrome / Chromium (.csv)', ext: '.csv' },
  { value: 'keepass-csv', label: 'KeePass (.csv)', ext: '.csv' },
  { value: '1password-csv', label: '1Password (.csv)', ext: '.csv' },
  { value: '1password-1pif', label: '1Password (.1pif)', ext: '.1pif' },
  { value: 'encrypted', label: 'Vulos encrypted backup', ext: '.vault' },
]

// fileToBase64 reads a File into base64 without blowing the call stack on a
// large file (String.fromCharCode(...bytes) overflows well before our size cap).
function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('Could not read that file'))
    reader.onload = () => {
      // readAsArrayBuffer below guarantees this, but narrow explicitly rather
      // than casting — a non-ArrayBuffer result becomes the same "could not
      // read" rejection as an actual read error, never a runtime throw.
      if (!(reader.result instanceof ArrayBuffer)) { reject(new Error('Could not read that file')); return }
      const bytes = new Uint8Array(reader.result)
      let binary = ''
      const CHUNK = 0x8000
      for (let i = 0; i < bytes.length; i += CHUNK) {
        binary += String.fromCharCode(...bytes.subarray(i, i + CHUNK))
      }
      resolve(btoa(binary))
    }
    reader.readAsArrayBuffer(file)
  })
}

// downloadBlob triggers a browser download of `data` (a Uint8Array).
function downloadBlob(data: Uint8Array<ArrayBuffer>, filename: string): void {
  const url = URL.createObjectURL(new Blob([data], { type: 'application/octet-stream' }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

function base64ToBytes(b64: string): Uint8Array<ArrayBuffer> {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

// ── Import / Export panel ────────────────────────────────────────────────────
//
// Import shows EXACTLY what happened — imported, skipped (duplicates) and failed,
// plus any warnings the server returned. A password import that quietly drops
// rows leaves the user believing they migrated when they did not, so there is no
// "success" state here that hides a partial result.
function TransferPanel({ onBack, onImported }: { onBack: () => void; onImported: () => void }) {
  const [tab, setTab] = useState<'import' | 'export'>('import') // 'import' | 'export'

  // --- import state ---
  const [format, setFormat] = useState('bitwarden')
  const [file, setFile] = useState<File | null>(null)
  const [filePassword, setFilePassword] = useState('')
  const [importing, setImporting] = useState(false)
  const [importErr, setImportErr] = useState('')
  const [result, setResult] = useState<VaultImportResult | null>(null)

  // --- export state ---
  const [masterPassword, setMasterPassword] = useState('')
  const [exportPassword, setExportPassword] = useState('')
  const [exportConfirm, setExportConfirm] = useState('')
  const [exporting, setExporting] = useState(false)
  const [exportErr, setExportErr] = useState('')
  const [exportDone, setExportDone] = useState(false)

  const selected = IMPORT_FORMATS.find(f => f.value === format)

  const handleImport = async (e: FormEvent) => {
    e.preventDefault()
    if (!file) { setImportErr('Choose a file to import'); return }
    setImporting(true)
    setImportErr('')
    setResult(null)
    try {
      const data = await fileToBase64(file)
      const res = await fetch('/api/auth/vault/import', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ format, data, password: filePassword }),
      })
      const body: unknown = await res.json().catch(() => ({}))
      if (!res.ok) {
        setImportErr(errorMessage(body) || `Import failed (${res.status})`)
      } else {
        setResult(toVaultImportResult(body))
        onImported()
      }
    } catch (err: unknown) {
      setImportErr(caughtMessage(err, 'Could not read that file'))
    }
    setImporting(false)
  }

  const handleExport = async (e: FormEvent) => {
    e.preventDefault()
    setExportErr('')
    setExportDone(false)
    if (exportPassword !== exportConfirm) { setExportErr('The two backup passwords do not match'); return }
    if (exportPassword.length < 8) { setExportErr('Backup password must be at least 8 characters'); return }
    setExporting(true)
    try {
      const res = await fetch('/api/auth/vault/export', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ master_password: masterPassword, password: exportPassword }),
      })
      const body: unknown = await res.json().catch(() => ({}))
      if (!res.ok) {
        setExportErr(errorMessage(body) || `Export failed (${res.status})`)
      } else {
        const stamp = new Date().toISOString().slice(0, 10)
        const exportData = isRecord(body) && typeof body.data === 'string' ? body.data : ''
        downloadBlob(base64ToBytes(exportData), `vulos-vault-${stamp}.vault`)
        setExportDone(true)
        setMasterPassword('')
        setExportPassword('')
        setExportConfirm('')
      }
    } catch {
      setExportErr('Could not reach the vault service')
    }
    setExporting(false)
  }

  const tabCls = (t: 'import' | 'export') =>
    `flex-1 min-h-[36px] py-1.5 rounded-lg text-xs font-medium transition-colors ${
      tab === t ? 'bg-neutral-700/70 text-[var(--text-primary)]' : 'text-neutral-500 hover:text-neutral-300'
    }`

  return (
    <div className="flex flex-col h-full overflow-y-auto">
      <div className="flex items-center gap-3 px-4 py-3 border-b border-neutral-800 shrink-0">
        <button onClick={onBack} className="text-neutral-500 hover:text-neutral-200 transition-colors min-w-[44px] min-h-[44px] flex items-center justify-center -ml-2" aria-label="Back">
          <IconBack />
        </button>
        <h3 className="text-sm font-semibold text-[var(--text-primary)]">Import &amp; Export</h3>
      </div>

      <div className="px-4 py-3 flex flex-col gap-3">
        {/* Tabs */}
        <div role="tablist" className="flex gap-1 bg-neutral-900 border border-neutral-800 rounded-xl p-1">
          <button role="tab" aria-selected={tab === 'import'} onClick={() => setTab('import')} className={tabCls('import')}>
            Import
          </button>
          <button role="tab" aria-selected={tab === 'export'} onClick={() => setTab('export')} className={tabCls('export')}>
            Export
          </button>
        </div>

        {/* ── Import ── */}
        {tab === 'import' && (
          <form onSubmit={handleImport} className="flex flex-col gap-3">
            <p className="text-xs text-neutral-500 leading-relaxed">
              Bring your passwords over from another password manager. Duplicates
              (same site and username) are skipped, never overwritten.
            </p>

            <Field label="Where are they coming from?">
              <select
                value={format}
                onChange={(e) => { setFormat(e.target.value); setResult(null); setImportErr('') }}
                className={inputCls}
              >
                {IMPORT_FORMATS.map(f => (
                  <option key={f.value} value={f.value}>{f.label}</option>
                ))}
              </select>
            </Field>

            <Field label="File" required>
              <input
                type="file"
                accept={selected?.ext}
                onChange={(e) => { setFile(e.target.files?.[0] || null); setResult(null); setImportErr('') }}
                className="w-full text-xs text-neutral-400 file:mr-3 file:py-1.5 file:px-3 file:rounded-lg file:border-0 file:text-xs file:font-medium file:bg-neutral-700 file:text-neutral-200 hover:file:bg-neutral-600 file:cursor-pointer"
              />
            </Field>

            {format === 'encrypted' && (
              <Field label="Backup password" required>
                <input
                  type="password"
                  value={filePassword}
                  onChange={(e) => setFilePassword(e.target.value)}
                  placeholder="The password you set when exporting"
                  className={inputCls}
                  autoComplete="off"
                />
              </Field>
            )}

            {importErr && (
              <p role="alert" className="text-danger text-xs bg-[var(--status-danger-soft)] border border-[var(--status-danger-soft)] rounded-lg px-3 py-2">
                {importErr}
              </p>
            )}

            {/* Full, honest accounting — never a bare "success". */}
            {result && (
              <div role="status" className="bg-neutral-850 border border-neutral-700/60 rounded-xl p-3 flex flex-col gap-2">
                <span className="text-xs font-medium text-neutral-300">Import finished</span>
                <div className="grid grid-cols-3 gap-2 text-center">
                  <div className="bg-neutral-900 rounded-lg py-2">
                    <div className="text-success text-sm font-semibold">{result.imported ?? 0}</div>
                    <div className="text-neutral-600 text-[12px] uppercase tracking-wide">Imported</div>
                  </div>
                  <div className="bg-neutral-900 rounded-lg py-2">
                    <div className="text-neutral-300 text-sm font-semibold">{result.skipped ?? 0}</div>
                    <div className="text-neutral-600 text-[12px] uppercase tracking-wide">Skipped</div>
                  </div>
                  <div className="bg-neutral-900 rounded-lg py-2">
                    <div className={`text-sm font-semibold ${result.errors ? 'text-danger' : 'text-neutral-300'}`}>
                      {result.errors ?? 0}
                    </div>
                    <div className="text-neutral-600 text-[12px] uppercase tracking-wide">Failed</div>
                  </div>
                </div>
                <p className="text-[12px] text-neutral-600">
                  {result.parsed ?? 0} entr{(result.parsed ?? 0) === 1 ? 'y' : 'ies'} found in the file.
                  {(result.skipped ?? 0) > 0 && ' Skipped entries were already in your vault.'}
                </p>
                {Array.isArray(result.warnings) && result.warnings.length > 0 && (
                  <ul className="flex flex-col gap-1 mt-1">
                    {result.warnings.map((wmsg, i) => (
                      <li key={i} className="text-[12px] text-warning bg-[var(--status-warning-soft)] border border-[var(--status-warning-soft)] rounded-md px-2 py-1">
                        {wmsg}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            )}

            <button
              type="submit"
              disabled={importing || !file}
              className="w-full min-h-[44px] bg-[var(--accent)] hover:bg-[var(--accent-hover)] disabled:opacity-40 disabled:cursor-default text-[var(--text-on-accent,#fff)] rounded-lg py-2 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]/50"
            >
              {importing ? 'Importing…' : 'Import'}
            </button>
          </form>
        )}

        {/* ── Export ── */}
        {tab === 'export' && (
          <form onSubmit={handleExport} className="flex flex-col gap-3">
            <p className="text-xs text-neutral-500 leading-relaxed">
              Download an encrypted copy of every credential in your vault. Keep it
              somewhere safe — anyone who has the file <em>and</em> its password has
              your passwords.
            </p>

            <Field label="Master password" required>
              <input
                type="password"
                value={masterPassword}
                onChange={(e) => setMasterPassword(e.target.value)}
                placeholder="Confirm it's really you"
                className={inputCls}
                autoComplete="current-password"
              />
            </Field>

            <Field label="Password for the backup file" required>
              <input
                type="password"
                value={exportPassword}
                onChange={(e) => setExportPassword(e.target.value)}
                placeholder="At least 8 characters"
                className={inputCls}
                autoComplete="new-password"
              />
            </Field>

            <Field label="Repeat backup password" required>
              <input
                type="password"
                value={exportConfirm}
                onChange={(e) => setExportConfirm(e.target.value)}
                placeholder="Repeat it"
                className={inputCls}
                autoComplete="new-password"
              />
            </Field>

            <p className="text-[12px] text-warning bg-[var(--status-warning-soft)] border border-[var(--status-warning-soft)] rounded-lg px-3 py-2">
              This password is not stored anywhere. If you lose it, the backup
              cannot be opened — not even by us.
            </p>

            {exportErr && (
              <p role="alert" className="text-danger text-xs bg-[var(--status-danger-soft)] border border-[var(--status-danger-soft)] rounded-lg px-3 py-2">
                {exportErr}
              </p>
            )}
            {exportDone && (
              <p role="status" className="text-success text-xs bg-[var(--status-success-soft)] border border-[var(--status-success-soft)] rounded-lg px-3 py-2">
                Backup downloaded.
              </p>
            )}

            <button
              type="submit"
              disabled={exporting || !masterPassword || !exportPassword || !exportConfirm}
              className="w-full min-h-[44px] bg-[var(--accent)] hover:bg-[var(--accent-hover)] disabled:opacity-40 disabled:cursor-default text-[var(--text-on-accent,#fff)] rounded-lg py-2 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]/50"
            >
              {exporting ? 'Exporting…' : 'Export vault'}
            </button>
          </form>
        )}
      </div>
    </div>
  )
}

// ── Unlock screen ─────────────────────────────────────────────────────────────
function UnlockScreen({ onUnlock }: { onUnlock: () => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => { inputRef.current?.focus() }, [])

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!password) return
    setLoading(true)
    setError('')
    try {
      const res = await fetch('/api/auth/vault/unlock', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password }),
      })
      if (res.ok) {
        onUnlock()
      } else {
        const data: unknown = await res.json().catch(() => ({}))
        setError(errorMessage(data) || 'Wrong master password')
      }
    } catch {
      setError('Could not reach vault service')
    }
    setLoading(false)
  }

  return (
    <div className="flex flex-col items-center justify-center h-full min-h-[300px] px-6 sm:px-8 gap-6">
      <div className="flex flex-col items-center gap-2">
        <div className="w-16 h-16 rounded-2xl bg-[var(--accent-soft)] flex items-center justify-center text-[var(--accent)] [&_svg]:w-7 [&_svg]:h-7">
          <IconLock />
        </div>
        <h2 className="text-[var(--text-primary)] font-semibold text-lg">Vault Locked</h2>
        <p className="text-neutral-500 text-sm text-center">Enter your master password to unlock</p>
      </div>

      <form onSubmit={handleSubmit} className="w-full max-w-xs flex flex-col gap-3">
        <input
          ref={inputRef}
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="Master password"
          className="w-full bg-neutral-800 border border-neutral-700 rounded-lg px-4 py-3 text-sm text-[var(--text-primary)] outline-none placeholder:text-neutral-400 focus:border-[var(--accent)] transition-colors focus-visible:ring-2 focus-visible:ring-[var(--accent)]/40"
          autoComplete="current-password"
        />
        {error && (
          <p className="text-danger text-xs text-center">{error}</p>
        )}
        <button
          type="submit"
          disabled={loading || !password}
          className="w-full min-h-[44px] bg-[var(--accent)] hover:bg-[var(--accent-hover)] disabled:opacity-40 disabled:cursor-default text-[var(--text-on-accent,#fff)] rounded-lg py-2.5 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]/50"
        >
          {loading ? 'Unlocking…' : 'Unlock Vault'}
        </button>
      </form>
    </div>
  )
}

// ── Generator panel (inline, can inject into a setter) ───────────────────────
function GeneratorPanel({ onInsert, onClose }: { onInsert?: (pwd: string) => void; onClose?: () => void }) {
  const gen = useGenerator()
  const { copied, copy } = useCopy()

  const handleGenerate = () => { gen.generate() }

  const toggle = (field: GeneratorBoolField) => gen.setOpts(prev => ({ ...prev, [field]: !prev[field] }))

  const strength = estimateStrength(gen.result)

  return (
    <div className="bg-neutral-850 border border-neutral-700/60 rounded-xl p-4 flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-neutral-400 uppercase tracking-wider">Password Generator</span>
        {onClose && (
          <button onClick={onClose} className="text-neutral-600 hover:text-neutral-400 text-lg leading-none w-8 h-8 flex items-center justify-center -mr-1">×</button>
        )}
      </div>

      {/* Length slider */}
      <div className="flex items-center gap-3">
        <span className="text-xs text-neutral-500 w-12 shrink-0">Length</span>
        <input
          type="range"
          min={8}
          max={64}
          value={gen.opts.length}
          onChange={(e) => gen.setOpts(prev => ({ ...prev, length: Number(e.target.value) }))}
          className="flex-1 min-w-0 accent-[var(--accent)]"
        />
        <span className="text-xs text-neutral-300 w-6 text-right shrink-0 font-mono tabular-nums">{gen.opts.length}</span>
      </div>

      {/* Character set toggles */}
      <div className="flex gap-2 flex-wrap">
        {GENERATOR_TOGGLES.map(({ field, label }) => (
          <button
            key={field}
            onClick={() => toggle(field)}
            className={`px-2.5 py-1.5 min-h-[36px] rounded-md text-xs font-mono transition-colors ${
              gen.opts[field]
                ? 'bg-[var(--accent-soft)] text-[var(--accent)] border border-[var(--accent)]'
                : 'bg-neutral-800 text-neutral-500 border border-neutral-700/50'
            }`}
          >
            {label}
          </button>
        ))}
      </div>

      {/* Generate button */}
      <button
        onClick={handleGenerate}
        disabled={gen.loading}
        className="flex items-center justify-center gap-2 w-full min-h-[44px] bg-neutral-700/60 hover:bg-neutral-600/60 disabled:opacity-40 text-neutral-200 rounded-lg py-2 text-sm transition-colors"
      >
        <IconWand />
        {gen.loading ? 'Generating…' : 'Generate'}
      </button>

      {/* Result row */}
      {gen.result && (
        <div className="flex flex-col gap-2">
          <div className="flex items-center gap-2 bg-neutral-900 border border-neutral-700/50 rounded-lg px-3 py-2">
            <span className="flex-1 min-w-0 font-mono text-xs text-success break-all">{gen.result}</span>
            <button
              onClick={() => copy(gen.result, 'gen')}
              className="text-neutral-500 hover:text-neutral-200 shrink-0 transition-colors w-9 h-9 flex items-center justify-center"
              title="Copy"
            >
              {copied === 'gen' ? <span className="text-success text-xs">Copied!</span> : <IconCopy />}
            </button>
            {onInsert && (
              <button
                onClick={() => { onInsert(gen.result); if (onClose) onClose() }}
                className="text-xs text-[var(--accent)] hover:text-[var(--accent-hover)] shrink-0 ml-1 transition-colors px-2 py-1"
              >
                Use
              </button>
            )}
          </div>
          {/* Strength indicator (presentational) */}
          <div className="flex items-center gap-2" aria-hidden="true">
            <div className="flex-1 flex gap-1">
              {[1, 2, 3, 4].map(i => (
                <div
                  key={i}
                  className="h-1 flex-1 rounded-full transition-colors"
                  style={{ background: i <= strength.score ? strength.bar : 'var(--border-default)' }}
                />
              ))}
            </div>
            {strength.label && (
              <span className="text-[12px] font-medium shrink-0" style={{ color: strength.text }}>
                {strength.label}
              </span>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

// ── Entry form (add / edit) ───────────────────────────────────────────────────
interface EntryFormState {
  title: string
  username: string
  password: string
  url: string
  notes: string
}

function EntryForm({ existing, onSave, onCancel }: { existing?: VaultEntry; onSave: (entry: VaultEntry) => void; onCancel: () => void }) {
  const isEdit = !!existing
  const [form, setForm] = useState<EntryFormState>({
    title: existing?.title || existing?.name || '',
    username: existing?.username || '',
    password: '',
    url: existing?.url || '',
    notes: existing?.notes || '',
  })
  const [showPass, setShowPass] = useState(false)
  const [showGen, setShowGen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const set = (k: keyof EntryFormState) => (e: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
    setForm(prev => ({ ...prev, [k]: e.target.value }))

  const handleSave = async (e: FormEvent) => {
    e.preventDefault()
    if (!form.title) { setError('Title is required'); return }
    setSaving(true)
    setError('')
    try {
      const payload: Partial<EntryFormState> = { ...form }
      // For edits, only send password if changed
      if (isEdit && !form.password) delete payload.password

      const url = isEdit
        ? `/api/auth/vault/entry/${existing.id}`
        : '/api/auth/vault/entry'
      const method = isEdit ? 'PUT' : 'POST'

      const res = await fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
      if (res.ok) {
        const data: unknown = await res.json()
        onSave(toVaultEntry(data))
      } else {
        const data: unknown = await res.json().catch(() => ({}))
        setError(errorMessage(data) || 'Save failed')
      }
    } catch {
      setError('Network error')
    }
    setSaving(false)
  }

  return (
    <form onSubmit={handleSave} className="flex flex-col gap-0 h-full overflow-y-auto">
      {/* Header */}
      <div className="flex items-center gap-3 px-4 py-3 border-b border-neutral-800 shrink-0">
        <button type="button" onClick={onCancel} className="text-neutral-500 hover:text-neutral-200 transition-colors min-w-[44px] min-h-[44px] flex items-center justify-center -ml-2">
          <IconBack />
        </button>
        <h3 className="text-sm font-semibold text-[var(--text-primary)] flex-1 min-w-0 truncate">
          {isEdit ? 'Edit Entry' : 'New Entry'}
        </h3>
        <button
          type="submit"
          disabled={saving}
          className="px-3 py-1.5 min-h-[36px] bg-[var(--accent)] hover:bg-[var(--accent-hover)] disabled:opacity-40 text-[var(--text-on-accent,#fff)] rounded-lg text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]/50"
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>

      <div className="flex flex-col gap-3 px-4 py-3">
        {error && <p className="text-danger text-xs bg-[var(--status-danger-soft)] border border-[var(--status-danger-soft)] rounded-lg px-3 py-2">{error}</p>}

        <Field label="Title" required>
          <input
            type="text"
            value={form.title}
            onChange={set('title')}
            placeholder="e.g. GitHub"
            className={inputCls}
            autoFocus
          />
        </Field>

        <Field label="Username / Email">
          <input
            type="text"
            value={form.username}
            onChange={set('username')}
            placeholder="user@example.com"
            className={inputCls}
            autoComplete="off"
          />
        </Field>

        <Field label={isEdit ? 'Password (leave blank to keep)' : 'Password'}>
          <div className="relative">
            <input
              type={showPass ? 'text' : 'password'}
              value={form.password}
              onChange={set('password')}
              placeholder={isEdit ? '(unchanged)' : 'Enter or generate a password'}
              className={`${inputCls} pr-20 font-mono`}
              autoComplete="new-password"
            />
            <div className="absolute right-1.5 top-1/2 -translate-y-1/2 flex gap-0.5">
              <button
                type="button"
                onClick={() => setShowPass(s => !s)}
                className="text-neutral-500 hover:text-neutral-300 w-9 h-9 flex items-center justify-center transition-colors"
                title={showPass ? 'Hide' : 'Show'}
              >
                <IconEye open={showPass} />
              </button>
              <button
                type="button"
                onClick={() => setShowGen(s => !s)}
                className="text-neutral-500 hover:text-neutral-300 w-9 h-9 flex items-center justify-center transition-colors"
                title="Generate password"
              >
                <IconWand />
              </button>
            </div>
          </div>
        </Field>

        {showGen && (
          <GeneratorPanel
            onInsert={(pwd) => setForm(prev => ({ ...prev, password: pwd }))}
            onClose={() => setShowGen(false)}
          />
        )}

        <Field label="URL">
          <input
            type="url"
            value={form.url}
            onChange={set('url')}
            placeholder="https://example.com"
            className={inputCls}
          />
        </Field>

        <Field label="Notes">
          <textarea
            value={form.notes}
            onChange={set('notes')}
            placeholder="Optional notes…"
            rows={3}
            className={`${inputCls} resize-none`}
          />
        </Field>
      </div>
    </form>
  )
}

// Field wraps its control INSIDE the <label>. The label used to be a sibling
// with no htmlFor, so it was associated with nothing: screen readers announced
// the inputs as unlabelled. Wrapping gives implicit association for every form
// control (input, select, textarea) with no id plumbing.
function Field({ label, required, children }: { label: ReactNode; required?: boolean; children: ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-xs text-neutral-500 font-medium">
        {label}{required && <span className="text-danger ml-0.5">*</span>}
      </span>
      {children}
    </label>
  )
}

const inputCls = 'w-full bg-neutral-800 border border-neutral-700 rounded-lg px-3 py-2 text-sm text-[var(--text-primary)] outline-none placeholder:text-neutral-400 focus:border-[var(--accent)] transition-colors focus-visible:ring-2 focus-visible:ring-[var(--accent)]/30'

// ── Entry detail view ─────────────────────────────────────────────────────────
function EntryDetail({ entryMeta, onBack, onEdit, onDelete }: {
  entryMeta: VaultEntry
  onBack: () => void
  onEdit: (entry: VaultEntry) => void
  onDelete: (id: string | undefined) => void
}) {
  // A fetched entry belongs to the ID IT WAS FETCHED FOR, so the id is stored
  // with it and the two are compared during render. The `setLoading(true)` that
  // used to open the effect — and the suppression covering it — set nothing on
  // mount, because `loading` starts true; it was there for the case where
  // entryMeta.id changes in place. Deriving covers that case without writing
  // anything synchronously, and covers the one setLoading(true) never did: a
  // slow answer for the entry the user has left cannot land on the one they are
  // looking at.
  const [loaded, setLoaded] = useState<{ id: string | undefined, entry: VaultEntry | null } | null>(null)
  const [showPass, setShowPass] = useState(false)
  const [delConfirm, setDelConfirm] = useState(false)
  const { copied, copy } = useCopy()

  const current = loaded && loaded.id === entryMeta.id ? loaded : null
  const entry = current ? current.entry : null
  const loading = current === null

  useEffect(() => {
    const forId = entryMeta.id
    let cancelled = false
    fetch(`/api/auth/vault/entry/${forId}`)
      .then(r => (r.ok ? r.json() : null))
      .then((data: unknown) => { if (!cancelled) setLoaded({ id: forId, entry: data ? toVaultEntry(data) : null }) })
      // A failed fetch resolves to "no entry", which renders the designed
      // "Could not load entry" state with its Back button. It used to only
      // clear the spinner, leaving `entry` null anyway — the same screen, but
      // reached by accident rather than on purpose.
      .catch(() => { if (!cancelled) setLoaded({ id: forId, entry: null }) })
    return () => { cancelled = true }
  }, [entryMeta.id])

  const handleDelete = async () => {
    if (!delConfirm) { setDelConfirm(true); return }
    try {
      const res = await fetch(`/api/auth/vault/entry/${entryMeta.id}`, { method: 'DELETE' })
      if (res.ok) onDelete(entryMeta.id)
    } catch { /* ignore */ }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full text-neutral-600 text-sm">
        Loading…
      </div>
    )
  }

  if (!entry) {
    return (
      <div className="flex flex-col items-center justify-center h-full gap-3">
        <p className="text-neutral-500 text-sm">Could not load entry</p>
        <button onClick={onBack} className="text-[var(--accent)] hover:text-[var(--accent-hover)] text-sm">Back</button>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full overflow-y-auto">
      {/* Header */}
      <div className="flex items-center gap-3 px-4 py-3 border-b border-neutral-800 shrink-0">
        <button onClick={onBack} className="text-neutral-500 hover:text-neutral-200 transition-colors min-w-[44px] min-h-[44px] flex items-center justify-center -ml-2">
          <IconBack />
        </button>
        <h3 className="text-sm font-semibold text-[var(--text-primary)] flex-1 min-w-0 truncate">
          {entry.title || entry.name || entryMeta.title || entryMeta.name}
        </h3>
        <button
          onClick={() => onEdit(entry)}
          className="text-neutral-500 hover:text-neutral-200 w-9 h-9 flex items-center justify-center rounded-lg hover:bg-[var(--bg-hover)] transition-colors"
          title="Edit"
        >
          <IconEdit />
        </button>
        <button
          onClick={handleDelete}
          className={`w-9 h-9 flex items-center justify-center rounded-lg transition-colors ${
            delConfirm
              ? 'text-danger bg-[var(--status-danger-soft)] hover:bg-[var(--status-danger-soft)]'
              : 'text-neutral-500 hover:text-danger hover:bg-[var(--bg-hover)]'
          }`}
          title={delConfirm ? 'Click again to confirm' : 'Delete'}
        >
          <IconTrash />
        </button>
      </div>

      {/* Fields */}
      <div className="flex flex-col gap-0 px-4 py-3">
        {entry.url && (
          <DetailRow
            label="URL"
            value={entry.url}
            copyKey="url"
            copied={copied}
            copy={copy}
          />
        )}
        {(entry.username || entry.email) && (
          <DetailRow
            label="Username"
            value={entry.username || entry.email || ''}
            copyKey="username"
            copied={copied}
            copy={copy}
          />
        )}
        {entry.password && (
          <div className="py-3 border-b border-neutral-800/60 last:border-0">
            <div className="flex items-center justify-between mb-1">
              <span className="text-xs text-neutral-500 font-medium">Password</span>
              <div className="flex gap-1">
                <button
                  onClick={() => setShowPass(s => !s)}
                  className="text-neutral-500 hover:text-neutral-300 w-9 h-9 flex items-center justify-center rounded transition-colors"
                  title={showPass ? 'Hide' : 'Reveal'}
                >
                  <IconEye open={showPass} />
                </button>
                <button
                  onClick={() => entry.password && copy(entry.password, 'password')}
                  className="text-neutral-500 hover:text-neutral-300 min-w-9 h-9 px-1 flex items-center justify-center rounded transition-colors"
                  title="Copy"
                >
                  {copied === 'password'
                    ? <span className="text-success text-xs font-medium">Copied!</span>
                    : <IconCopy />
                  }
                </button>
              </div>
            </div>
            <span className="font-mono text-sm text-neutral-200 break-all">
              {showPass ? entry.password : '••••••••••••'}
            </span>
          </div>
        )}
        {entry.notes && (
          <div className="py-3 border-b border-neutral-800/60 last:border-0">
            <span className="text-xs text-neutral-500 font-medium block mb-1">Notes</span>
            <p className="text-sm text-neutral-300 whitespace-pre-wrap break-words">{entry.notes}</p>
          </div>
        )}
      </div>

      {delConfirm && (
        <div className="mx-4 mb-4 bg-[var(--status-danger-soft)] border border-[var(--status-danger-soft)] rounded-lg px-3 py-2 text-xs text-danger">
          Click the trash icon again to permanently delete this entry.
        </div>
      )}
    </div>
  )
}

function DetailRow({ label, value, copyKey, copied, copy }: {
  label: string
  value: string
  copyKey: string
  copied: string | null
  copy: (text: string, key: string) => void
}) {
  return (
    <div className="py-3 border-b border-neutral-800/60 last:border-0">
      <div className="flex items-center justify-between mb-1 gap-2">
        <span className="text-xs text-neutral-500 font-medium">{label}</span>
        <button
          onClick={() => copy(value, copyKey)}
          className="text-neutral-500 hover:text-neutral-300 min-w-9 h-9 px-1 flex items-center justify-center rounded transition-colors shrink-0"
          title="Copy"
        >
          {copied === copyKey
            ? <span className="text-success text-xs font-medium">Copied!</span>
            : <IconCopy />
          }
        </button>
      </div>
      <span className="text-sm text-neutral-200 break-all">{value}</span>
    </div>
  )
}

// ── Entry list item ───────────────────────────────────────────────────────────
function EntryTile({ entry, onSelect }: { entry: VaultEntry; onSelect: (entry: VaultEntry) => void }) {
  const initials = (entry.title || entry.name || '?').slice(0, 2).toUpperCase()
  const domain = entry.url ? (() => { try { return new URL(entry.url ?? '').hostname } catch { return '' } })() : ''

  return (
    <button
      onClick={() => onSelect(entry)}
      className="flex items-center gap-3 w-full px-3 py-2.5 min-h-[52px] rounded-xl hover:bg-[var(--bg-hover)] transition-colors text-left group"
    >
      {/* Avatar */}
      <div className="w-9 h-9 rounded-xl bg-neutral-800 border border-neutral-700/50 flex items-center justify-center text-xs font-semibold text-neutral-400 shrink-0 group-hover:border-neutral-600/50 transition-colors">
        {initials}
      </div>
      <div className="flex flex-col min-w-0">
        <span className="text-sm text-neutral-200 truncate font-medium">{entry.title || entry.name}</span>
        <span className="text-xs text-neutral-500 truncate">
          {entry.username || entry.email || domain || 'No username'}
        </span>
      </div>
      <svg viewBox="0 0 20 20" fill="currentColor" className="w-3.5 h-3.5 text-neutral-700 group-hover:text-neutral-500 shrink-0 ml-auto transition-colors">
        <path fillRule="evenodd" d="M7.293 14.707a1 1 0 010-1.414L10.586 10 7.293 6.707a1 1 0 011.414-1.414l4 4a1 1 0 010 1.414l-4 4a1 1 0 01-1.414 0z" clipRule="evenodd" />
      </svg>
    </button>
  )
}

type VaultView = 'list' | 'detail' | 'add' | 'edit' | 'generator' | 'transfer'

// ── Main Vault component ──────────────────────────────────────────────────────
export default function Vault() {
  const [locked, setLocked] = useState(true)
  const [entries, setEntries] = useState<VaultEntry[]>([])
  const [search, setSearch] = useState('')
  const [view, setView] = useState<VaultView>('list') // 'list' | 'detail' | 'add' | 'edit' | 'generator'
  const [selected, setSelected] = useState<VaultEntry | null>(null)
  const [loadingEntries, setLoadingEntries] = useState(false)

  // Relock on inactivity
  const lockVault = useCallback(async () => {
    try { await fetch('/api/auth/vault/lock', { method: 'POST' }) } catch { /* ignore */ }
    setLocked(true)
    setView('list')
    setSelected(null)
    setEntries([])
  }, [])

  useRelockTimer(locked, lockVault)

  const loadEntries = useCallback(async () => {
    setLoadingEntries(true)
    try {
      const res = await fetch('/api/auth/vault/entries')
      if (res.ok) {
        const data: unknown = await res.json()
        // GET /api/auth/vault/entries returns a BARE JSON ARRAY.
        //
        // The old code here was `setEntries(data.entries || data || [])`, which
        // blank-screened the whole app on every unlock: for an array, `.entries`
        // is not undefined — it is the built-in Array.prototype.entries METHOD,
        // which is truthy. React then took that function to be a state-updater,
        // invoked it with no receiver, and threw "Cannot convert undefined or
        // null to object", tearing down the tree. Check the shape explicitly.
        const list = Array.isArray(data)
          ? data
          : (isRecord(data) && Array.isArray(data.entries) ? data.entries : [])
        setEntries(list.map(toVaultEntry))
      }
    } catch { /* ignore */ }
    setLoadingEntries(false)
  }, [])

  const handleUnlock = useCallback(() => {
    setLocked(false)
    loadEntries()
  }, [loadEntries])

  const handleSaveEntry = useCallback((_savedEntry: VaultEntry) => { // eslint-disable-line @typescript-eslint/no-unused-vars
    loadEntries()
    setView('list')
    setSelected(null)
  }, [loadEntries])

  const handleDeleteEntry = useCallback((id: string | undefined) => {
    setEntries(prev => prev.filter(e => e.id !== id))
    setView('list')
    setSelected(null)
  }, [])

  const handleSelectEntry = useCallback((entry: VaultEntry) => {
    setSelected(entry)
    setView('detail')
  }, [])

  const handleEditEntry = useCallback((entry: VaultEntry) => {
    setSelected(entry)
    setView('edit')
  }, [])

  const handleBack = useCallback(() => {
    setView('list')
    setSelected(null)
  }, [])

  // Filtered entries
  const filtered = search.trim()
    ? entries.filter(e => {
        const q = search.toLowerCase()
        return (
          (e.title || e.name || '').toLowerCase().includes(q) ||
          (e.username || e.email || '').toLowerCase().includes(q) ||
          (e.url || '').toLowerCase().includes(q)
        )
      })
    : entries

  if (locked) {
    return (
      <div className="flex flex-col h-full bg-neutral-950">
        <UnlockScreen onUnlock={handleUnlock} />
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full bg-neutral-950 overflow-hidden">
      {/* Detail / form views */}
      {view === 'detail' && selected && (
        <EntryDetail
          entryMeta={selected}
          onBack={handleBack}
          onEdit={handleEditEntry}
          onDelete={handleDeleteEntry}
        />
      )}

      {view === 'add' && (
        <EntryForm
          onSave={handleSaveEntry}
          onCancel={handleBack}
        />
      )}

      {view === 'edit' && selected && (
        <EntryForm
          existing={selected}
          onSave={handleSaveEntry}
          onCancel={handleBack}
        />
      )}

      {/* List view */}
      {view === 'list' && (
        <>
          {/* Toolbar */}
          <div className="flex items-center gap-2 px-3 py-2.5 border-b border-neutral-800 shrink-0">
            {/* Search */}
            <div className="relative flex-1 min-w-0">
              <div className="absolute left-3 top-1/2 -translate-y-1/2 text-neutral-600 pointer-events-none">
                <IconSearch />
              </div>
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search vault…"
                className="w-full bg-neutral-800/60 border border-neutral-700/50 rounded-lg pl-9 pr-8 py-1.5 text-sm text-[var(--text-primary)] outline-none placeholder:text-neutral-400 focus:border-[var(--accent)] transition-colors focus-visible:ring-2 focus-visible:ring-[var(--accent)]/30"
              />
              {search && (
                <button
                  onClick={() => setSearch('')}
                  className="absolute right-1 top-1/2 -translate-y-1/2 text-neutral-600 hover:text-neutral-400 text-base w-7 h-7 flex items-center justify-center"
                >
                  ×
                </button>
              )}
            </div>

            {/* Generator shortcut */}
            <button
              onClick={() => setView('generator')}
              className="w-9 h-9 flex items-center justify-center rounded-lg text-neutral-500 hover:text-neutral-200 hover:bg-[var(--bg-hover)] transition-colors shrink-0"
              title="Password Generator"
              aria-label="Password Generator"
            >
              <IconWand />
            </button>

            {/* Import / Export */}
            <button
              onClick={() => setView('transfer')}
              className="w-9 h-9 flex items-center justify-center rounded-lg text-neutral-500 hover:text-neutral-200 hover:bg-[var(--bg-hover)] transition-colors shrink-0"
              title="Import / Export"
              aria-label="Import or export vault"
            >
              <IconTransfer />
            </button>

            {/* Add */}
            <button
              onClick={() => setView('add')}
              className="flex items-center gap-1.5 px-3 py-1.5 min-h-[36px] rounded-lg bg-[var(--accent)] hover:bg-[var(--accent-hover)] text-[var(--text-on-accent,#fff)] text-xs font-medium transition-colors shrink-0 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]/50"
            >
              <IconPlus />
              Add
            </button>

            {/* Lock */}
            <button
              onClick={lockVault}
              className="w-9 h-9 flex items-center justify-center rounded-lg text-neutral-500 hover:text-neutral-200 hover:bg-[var(--bg-hover)] transition-colors shrink-0"
              title="Lock vault"
            >
              <IconLock />
            </button>
          </div>

          {/* Entries */}
          <div className="flex-1 overflow-y-auto px-2 py-2">
            {loadingEntries ? (
              <div className="flex items-center justify-center py-16 text-neutral-600 text-sm">
                Loading…
              </div>
            ) : filtered.length === 0 ? (
              <div className="flex flex-col items-center justify-center py-16 px-4 gap-3">
                {entries.length === 0 ? (
                  <>
                    <div className="w-14 h-14 rounded-2xl bg-[var(--accent-soft)] flex items-center justify-center text-[var(--accent)] [&_svg]:w-6 [&_svg]:h-6">
                      <IconKey />
                    </div>
                    <div className="text-center">
                      <p className="text-neutral-400 text-sm font-medium">Vault is empty</p>
                      <p className="text-neutral-600 text-xs mt-1">Add your first credential to get started</p>
                    </div>
                    <div className="flex items-center gap-2 flex-wrap justify-center">
                      <button
                        onClick={() => setView('add')}
                        className="flex items-center gap-1.5 px-4 py-2 min-h-[40px] rounded-lg bg-[var(--accent)] hover:bg-[var(--accent-hover)] text-[var(--text-on-accent,#fff)] text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]/50"
                      >
                        <IconPlus />
                        Add credential
                      </button>
                      <button
                        onClick={() => setView('transfer')}
                        className="flex items-center gap-1.5 px-4 py-2 min-h-[40px] rounded-lg bg-neutral-800 hover:bg-neutral-700 border border-neutral-700/60 text-neutral-200 text-sm font-medium transition-colors"
                      >
                        <IconTransfer />
                        Import
                      </button>
                    </div>
                  </>
                ) : (
                  <p className="text-neutral-600 text-sm">No results for "{search}"</p>
                )}
              </div>
            ) : (
              <div className="flex flex-col gap-0.5">
                {filtered.map(entry => (
                  <EntryTile key={entry.id} entry={entry} onSelect={handleSelectEntry} />
                ))}
              </div>
            )}
          </div>

          {/* Footer count */}
          {entries.length > 0 && (
            <div className="shrink-0 px-4 py-2 border-t border-neutral-800/60 flex items-center justify-between">
              <span className="text-xs text-neutral-600">
                {filtered.length === entries.length
                  ? `${entries.length} item${entries.length !== 1 ? 's' : ''}`
                  : `${filtered.length} of ${entries.length}`}
              </span>
              <div className="flex items-center gap-1 text-xs text-neutral-600">
                <IconUnlock />
                <span>Unlocked</span>
              </div>
            </div>
          )}
        </>
      )}

      {/* Import / export view */}
      {view === 'transfer' && (
        <TransferPanel onBack={handleBack} onImported={loadEntries} />
      )}

      {/* Standalone generator view */}
      {view === 'generator' && (
        <div className="flex flex-col h-full">
          <div className="flex items-center gap-3 px-4 py-3 border-b border-neutral-800 shrink-0">
            <button onClick={handleBack} className="text-neutral-500 hover:text-neutral-200 transition-colors min-w-[44px] min-h-[44px] flex items-center justify-center -ml-2">
              <IconBack />
            </button>
            <h3 className="text-sm font-semibold text-[var(--text-primary)]">Password Generator</h3>
          </div>
          <div className="p-4">
            <GeneratorPanel />
          </div>
        </div>
      )}
    </div>
  )
}
