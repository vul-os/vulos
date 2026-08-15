import { useState, useEffect, useCallback, useRef, type CSSProperties, type FormEvent } from 'react'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// errorMessage pulls a server-supplied `{ error: string }` out of an
// untrusted JSON body without ever casting the body to a nicer shape.
function errorMessage(x: unknown): string | undefined {
  return isRecord(x) && typeof x.error === 'string' ? x.error : undefined
}

// caughtMessage mirrors the exact `err?.message || fallback` shape used
// throughout this file's catch blocks, now that `catch` binds `unknown`
// under strict mode instead of the old implicit `any`.
function caughtMessage(err: unknown, fallback: string): string {
  return isRecord(err) && typeof err.message === 'string' && err.message ? err.message : fallback
}

// A TOTP account as this file uses it. Every field is optional because it is
// narrowed from untrusted server JSON (see toTotpAccount) — nothing here is
// ever cast to this shape, only assembled into it field-by-field.
interface TotpAccount {
  id?: string
  name?: string
  issuer?: string
}

function toTotpAccount(x: unknown): TotpAccount {
  if (!isRecord(x)) return {}
  return {
    id: x.id !== undefined && x.id !== null ? String(x.id) : undefined,
    name: typeof x.name === 'string' ? x.name : undefined,
    issuer: typeof x.issuer === 'string' ? x.issuer : undefined,
  }
}

function extractTotpCode(x: unknown): string | null {
  if (!isRecord(x)) return null
  if (typeof x.code === 'string') return x.code
  if (typeof x.otp === 'string') return x.otp
  return null
}

interface TotpImportResult {
  imported?: number
  failed?: number
  parsed?: number
  warnings?: string[]
}

function toTotpImportResult(x: unknown): TotpImportResult {
  if (!isRecord(x)) return {}
  return {
    imported: typeof x.imported === 'number' ? x.imported : undefined,
    failed: typeof x.failed === 'number' ? x.failed : undefined,
    parsed: typeof x.parsed === 'number' ? x.parsed : undefined,
    warnings: Array.isArray(x.warnings) ? x.warnings.filter((w): w is string => typeof w === 'string') : undefined,
  }
}

// Seconds remaining in the current 30-second TOTP window
function useTotpClock(): number {
  const [secondsLeft, setSecondsLeft] = useState(() => 30 - (Math.floor(Date.now() / 1000) % 30))

  useEffect(() => {
    const tick = () => {
      const s = 30 - (Math.floor(Date.now() / 1000) % 30)
      setSecondsLeft(s)
    }
    tick()
    const id = setInterval(tick, 500)
    return () => clearInterval(id)
  }, [])

  return secondsLeft
}

// Single TOTP account row — fetches its own code and refreshes on window boundary
function AccountRow({ account, onDelete }: { account: TotpAccount; onDelete: (id: string | undefined) => void }) {
  const [code, setCode] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState(false)
  const secondsLeft = useTotpClock()
  const prevWindowRef = useRef<number | null>(null)

  const fetchCode = useCallback(async () => {
    try {
      const res = await fetch(`/api/auth/totp/code/${account.id}`)
      if (!res.ok) { setError(true); return }
      const data: unknown = await res.json()
      setCode(extractTotpCode(data))
      setError(false)
    } catch {
      setError(true)
    }
  }, [account.id])

  // Fetch on mount and whenever the 30-second window rolls over
  useEffect(() => {
    const currentWindow = Math.floor(Date.now() / 1000 / 30)
    if (prevWindowRef.current === null || prevWindowRef.current !== currentWindow) {
      prevWindowRef.current = currentWindow
      // eslint-disable-next-line react-hooks/set-state-in-effect
      fetchCode()
    }
  }, [secondsLeft, fetchCode])

  const handleCopy = async () => {
    if (!code) return
    try {
      await navigator.clipboard.writeText(code.replace(/\s/g, ''))
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Fallback: select hidden input
      const el = document.createElement('input')
      el.value = code.replace(/\s/g, '')
      document.body.appendChild(el)
      el.select()
      document.execCommand('copy')
      document.body.removeChild(el)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }

  const handleDelete = async () => {
    if (!window.confirm(`Remove "${account.name}"?`)) return
    try {
      await fetch(`/api/auth/totp/${account.id}`, { method: 'DELETE' })
      onDelete(account.id)
    } catch { /* noop */ }
  }

  // Format code with a space in the middle: "847 291"
  const displayCode = code
    ? code.replace(/\s/g, '').replace(/^(\d{3})(\d{3})$/, '$1 $2')
    : error ? '— —' : '···'

  const progress = secondsLeft / 30 // 1.0 → 0.0
  const danger = secondsLeft <= 5
  const warn = secondsLeft <= 8 && !danger
  const urgency = danger

  // Countdown color shifts accent → warning → danger as the window depletes
  const ringColor = danger ? 'var(--status-danger)' : warn ? 'var(--status-warning)' : 'var(--accent)'
  const timeColor = danger ? 'var(--status-danger)' : warn ? 'var(--status-warning)' : 'var(--text-muted)'

  // SVG ring parameters
  const R = 10
  const CIRC = 2 * Math.PI * R
  const dash = progress * CIRC

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        padding: '12px 14px',
        borderRadius: 12,
        background: 'var(--bg-surface)',
        border: '1px solid var(--border-subtle)',
        marginBottom: 8,
        cursor: 'pointer',
        transition: 'background var(--motion-fast, 0.15s) var(--ease-out, ease), border-color var(--motion-fast, 0.15s) var(--ease-out, ease)',
        userSelect: 'none',
      }}
      onClick={handleCopy}
      onMouseEnter={e => { e.currentTarget.style.background = 'var(--bg-hover)'; e.currentTarget.style.borderColor = 'var(--border-default)' }}
      onMouseLeave={e => { e.currentTarget.style.background = 'var(--bg-surface)'; e.currentTarget.style.borderColor = 'var(--border-subtle)' }}
      role="button"
      aria-label={`Copy ${account.name} code`}
      title="Click to copy"
    >
      {/* Account icon / initial */}
      <div style={{
        width: 36,
        height: 36,
        borderRadius: 9,
        background: 'var(--accent-soft)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        fontSize: 15,
        fontWeight: 700,
        color: 'var(--accent)',
        flexShrink: 0,
        textTransform: 'uppercase',
      }}>
        {(account.name?.[0] || '?')}
      </div>

      {/* Name + code */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{
          fontSize: 11,
          color: 'var(--text-tertiary)',
          marginBottom: 2,
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
        }}>
          {account.issuer || account.name}
        </div>
        <div style={{
          fontSize: 24,
          fontWeight: 600,
          letterSpacing: '0.12em',
          fontFamily: "'SF Mono', 'Cascadia Code', 'Fira Code', ui-monospace, monospace",
          fontVariantNumeric: 'tabular-nums',
          color: urgency ? 'var(--status-danger)' : copied ? 'var(--status-success)' : 'var(--text-primary)',
          transition: 'color var(--motion-base, 0.2s) var(--ease-out, ease)',
        }}>
          {copied ? 'Copied!' : displayCode}
        </div>
      </div>

      {/* Countdown ring */}
      <div style={{ flexShrink: 0, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 3 }}>
        <svg width={28} height={28} viewBox="0 0 28 28" aria-hidden="true">
          {/* Background track */}
          <circle cx={14} cy={14} r={R} fill="none" stroke="var(--border-default)" strokeWidth={2.5} />
          {/* Progress arc — rotated so it depletes clockwise */}
          <circle
            cx={14} cy={14} r={R}
            fill="none"
            stroke={ringColor}
            strokeWidth={2.5}
            strokeLinecap="round"
            strokeDasharray={`${dash} ${CIRC}`}
            style={{ transform: 'rotate(-90deg)', transformOrigin: '14px 14px', transition: 'stroke var(--motion-slow, 0.3s) var(--ease-out, ease)' }}
          />
        </svg>
        <span style={{
          fontSize: 9,
          color: timeColor,
          fontVariantNumeric: 'tabular-nums',
          transition: 'color var(--motion-slow, 0.3s) var(--ease-out, ease)',
        }}>
          {secondsLeft}s
        </span>
      </div>

      {/* Delete button — shown on hover via inline state not possible, so always show but subtle */}
      <button
        onClick={(e) => { e.stopPropagation(); handleDelete() }}
        aria-label={`Remove ${account.name}`}
        style={{
          background: 'transparent',
          border: 'none',
          color: 'var(--text-ghost)',
          cursor: 'pointer',
          padding: 8,
          borderRadius: 8,
          fontSize: 14,
          lineHeight: 1,
          flexShrink: 0,
          transition: 'color var(--motion-fast, 0.15s) var(--ease-out, ease), background var(--motion-fast, 0.15s) var(--ease-out, ease)',
        }}
        onMouseEnter={e => { e.currentTarget.style.color = 'var(--status-danger)'; e.currentTarget.style.background = 'var(--status-danger-soft)' }}
        onMouseLeave={e => { e.currentTarget.style.color = 'var(--text-ghost)'; e.currentTarget.style.background = 'transparent' }}
      >
        ✕
      </button>
    </div>
  )
}

interface ParsedOtpauth {
  name: string
  issuer: string
  secret: string
  algorithm: string
  digits: number
  period: number
}

interface AddAccountPayload {
  name: string
  issuer: string
  secret: string
  algorithm?: string
  digits?: number
  period?: number
}

// Add-account form — supports paste otpauth URI or manual entry
function AddAccountForm({ onAdd, onCancel }: { onAdd: (account: TotpAccount) => void; onCancel: () => void }) {
  const [uri, setUri] = useState('')
  const [manual, setManual] = useState(false)
  const [name, setName] = useState('')
  const [issuer, setIssuer] = useState('')
  const [secret, setSecret] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [err, setErr] = useState('')
  const firstRef = useRef<HTMLInputElement & HTMLTextAreaElement>(null)

  useEffect(() => { firstRef.current?.focus() }, [])

  const parseOtpauth = (rawUri: string): ParsedOtpauth | null => {
    try {
      const u = new URL(rawUri.trim())
      if (u.protocol !== 'otpauth:') return null
      const label = decodeURIComponent(u.pathname.replace(/^\/\/[^/]+\//, '').replace(/^\/+/, ''))
      const params = u.searchParams
      return {
        name: label || params.get('accountname') || '',
        issuer: params.get('issuer') || '',
        secret: params.get('secret') || '',
        algorithm: params.get('algorithm') || 'SHA1',
        digits: parseInt(params.get('digits') || '6', 10),
        period: parseInt(params.get('period') || '30', 10),
      }
    } catch { return null }
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setErr('')
    setSubmitting(true)

    let payload: AddAccountPayload
    if (manual) {
      if (!name.trim() || !secret.trim()) { setErr('Name and secret are required.'); setSubmitting(false); return }
      payload = { name: name.trim(), issuer: issuer.trim(), secret: secret.trim().replace(/\s/g, '').toUpperCase() }
    } else {
      const parsed = parseOtpauth(uri)
      if (!parsed || !parsed.secret) { setErr('Invalid otpauth:// URI. Paste the full URI from your authenticator app or QR scanner.'); setSubmitting(false); return }
      payload = {
        name: parsed.name || name.trim() || 'Account',
        issuer: parsed.issuer || issuer.trim(),
        secret: parsed.secret,
        algorithm: parsed.algorithm,
        digits: parsed.digits,
        period: parsed.period,
      }
    }

    try {
      const res = await fetch('/api/auth/totp/add', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
      if (!res.ok) {
        const body: unknown = await res.json().catch(() => ({}))
        setErr(errorMessage(body) || `Server error ${res.status}`)
        setSubmitting(false)
        return
      }
      const added: unknown = await res.json()
      onAdd(toTotpAccount(added))
    } catch {
      setErr('Network error — could not reach the server.')
    }
    setSubmitting(false)
  }

  const inputStyle: CSSProperties = {
    width: '100%',
    background: 'var(--bg-surface)',
    border: '1px solid var(--border-default)',
    borderRadius: 8,
    color: 'var(--text-primary)',
    fontSize: 13,
    padding: '10px 12px',
    outline: 'none',
    boxSizing: 'border-box',
    transition: 'border-color var(--motion-fast, 0.15s) var(--ease-out, ease)',
  }

  const labelStyle: CSSProperties = {
    fontSize: 11,
    color: 'var(--text-tertiary)',
    marginBottom: 4,
    display: 'block',
  }

  return (
    <form onSubmit={submit} style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      <div style={{ display: 'flex', gap: 8, marginBottom: 4 }}>
        <button
          type="button"
          onClick={() => setManual(false)}
          style={{
            flex: 1, minHeight: 40, padding: '8px 0', borderRadius: 8, fontSize: 12, fontWeight: 500, cursor: 'pointer',
            background: !manual ? 'var(--accent-soft)' : 'var(--bg-surface)',
            border: !manual ? '1px solid var(--accent)' : '1px solid var(--border-default)',
            color: !manual ? 'var(--accent)' : 'var(--text-secondary)',
            transition: 'all var(--motion-fast, 0.15s) var(--ease-out, ease)',
          }}
        >
          Paste URI
        </button>
        <button
          type="button"
          onClick={() => setManual(true)}
          style={{
            flex: 1, minHeight: 40, padding: '8px 0', borderRadius: 8, fontSize: 12, fontWeight: 500, cursor: 'pointer',
            background: manual ? 'var(--accent-soft)' : 'var(--bg-surface)',
            border: manual ? '1px solid var(--accent)' : '1px solid var(--border-default)',
            color: manual ? 'var(--accent)' : 'var(--text-secondary)',
            transition: 'all var(--motion-fast, 0.15s) var(--ease-out, ease)',
          }}
        >
          Manual
        </button>
      </div>

      {!manual ? (
        <div>
          <label style={labelStyle}>otpauth:// URI</label>
          <textarea
            ref={firstRef}
            value={uri}
            onChange={e => setUri(e.target.value)}
            placeholder="otpauth://totp/Example:user@email.com?secret=BASE32SECRET&issuer=Example"
            rows={3}
            style={{ ...inputStyle, resize: 'vertical', fontFamily: "'SF Mono', ui-monospace, monospace", fontSize: 11 }}
          />
          <div style={{ fontSize: 10, color: 'var(--text-muted)', marginTop: 4 }}>
            Use a QR code scanner app to get the URI, then paste it here.
          </div>
        </div>
      ) : (
        <>
          <div>
            <label style={labelStyle}>Account name *</label>
            <input ref={firstRef} style={inputStyle} value={name} onChange={e => setName(e.target.value)} placeholder="e.g. user@example.com" />
          </div>
          <div>
            <label style={labelStyle}>Issuer (optional)</label>
            <input style={inputStyle} value={issuer} onChange={e => setIssuer(e.target.value)} placeholder="e.g. GitHub" />
          </div>
          <div>
            <label style={labelStyle}>Secret key (Base32) *</label>
            <input
              style={{ ...inputStyle, fontFamily: "'SF Mono', ui-monospace, monospace", letterSpacing: '0.08em' }}
              value={secret}
              onChange={e => setSecret(e.target.value.toUpperCase().replace(/[^A-Z2-7=]/g, ''))}
              placeholder="JBSWY3DPEHPK3PXP"
              spellCheck={false}
              autoCorrect="off"
              autoCapitalize="none"
            />
          </div>
        </>
      )}

      {err && (
        <div style={{ fontSize: 12, color: 'var(--status-danger-text)', padding: '8px 10px', background: 'var(--status-danger-soft)', borderRadius: 8, border: '1px solid var(--status-danger-soft)' }}>
          {err}
        </div>
      )}

      <div style={{ display: 'flex', gap: 8, marginTop: 4 }}>
        <button
          type="button"
          onClick={onCancel}
          style={{
            flex: 1, minHeight: 44, padding: '10px 0', borderRadius: 8, fontSize: 13, cursor: 'pointer',
            background: 'var(--bg-surface)', border: '1px solid var(--border-default)',
            color: 'var(--text-secondary)',
            transition: 'all var(--motion-fast, 0.15s) var(--ease-out, ease)',
          }}
        >
          Cancel
        </button>
        <button
          type="submit"
          disabled={submitting}
          style={{
            flex: 2, minHeight: 44, padding: '10px 0', borderRadius: 8, fontSize: 13, fontWeight: 600, cursor: submitting ? 'default' : 'pointer',
            background: 'var(--accent)', opacity: submitting ? 0.55 : 1,
            border: '1px solid var(--accent)',
            color: 'var(--text-on-accent, #fff)',
            transition: 'all var(--motion-fast, 0.15s) var(--ease-out, ease)',
          }}
        >
          {submitting ? 'Adding...' : 'Add Account'}
        </button>
      </div>
    </form>
  )
}

// ── Import / Export ──────────────────────────────────────────────────────────
//
// Import accepts the otpauth-migration:// payload that Google Authenticator puts
// in its "Transfer accounts" QR code, or a Vulos backup file.
//
// Export writes an encrypted backup that is decryptable with the PASSPHRASE the
// user chooses — deliberately not with any key held by this box, because the
// scenario a 2FA backup exists for is "the box is gone".
function TransferPanel({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const [tab, setTab] = useState<'import' | 'export'>('import')

  const [uri, setUri] = useState('')
  const [backupFile, setBackupFile] = useState<File | null>(null)
  const [importPassphrase, setImportPassphrase] = useState('')
  const [importing, setImporting] = useState(false)
  const [importErr, setImportErr] = useState('')
  const [result, setResult] = useState<TotpImportResult | null>(null)

  const [accountPassword, setAccountPassword] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [passphrase2, setPassphrase2] = useState('')
  const [exporting, setExporting] = useState(false)
  const [exportErr, setExportErr] = useState('')
  const [exportDone, setExportDone] = useState(false)

  const inputStyle: CSSProperties = {
    width: '100%',
    background: 'var(--bg-surface)',
    border: '1px solid var(--border-default)',
    borderRadius: 8,
    color: 'var(--text-primary)',
    fontSize: 13,
    padding: '10px 12px',
    outline: 'none',
    boxSizing: 'border-box',
    transition: 'border-color var(--motion-fast, 0.15s) var(--ease-out, ease)',
  }
  const labelStyle: CSSProperties = { fontSize: 11, color: 'var(--text-tertiary)', marginBottom: 4, display: 'block' }
  const primaryBtn = (disabled: boolean): CSSProperties => ({
    width: '100%', minHeight: 44, padding: '10px 14px', borderRadius: 8, fontSize: 13, fontWeight: 600,
    cursor: disabled ? 'default' : 'pointer', opacity: disabled ? 0.45 : 1,
    background: 'var(--accent)', border: '1px solid var(--accent)', color: 'var(--text-on-accent, #fff)',
    transition: 'all var(--motion-fast, 0.15s) var(--ease-out, ease)',
  })
  const noteStyle: CSSProperties = {
    fontSize: 11, lineHeight: 1.5, color: 'var(--status-warning-text)',
    background: 'var(--status-warning-soft)', border: '1px solid var(--status-warning-soft)',
    borderRadius: 8, padding: '8px 10px',
  }
  const errStyle: CSSProperties = {
    fontSize: 12, color: 'var(--status-danger-text)', background: 'var(--status-danger-soft)',
    border: '1px solid var(--status-danger-soft)', borderRadius: 8, padding: '8px 10px',
  }
  const tabStyle = (t: 'import' | 'export'): CSSProperties => ({
    flex: 1, minHeight: 36, padding: '7px 0', borderRadius: 7, fontSize: 12, fontWeight: 500, cursor: 'pointer',
    background: tab === t ? 'var(--bg-hover)' : 'transparent',
    border: 'none',
    color: tab === t ? 'var(--text-primary)' : 'var(--text-muted)',
    transition: 'all var(--motion-fast, 0.15s) var(--ease-out, ease)',
  })

  const submitImport = async (e: FormEvent) => {
    e.preventDefault()
    setImporting(true)
    setImportErr('')
    setResult(null)
    try {
      let payload: { blob: unknown; passphrase: string } | { uri: string }
      if (backupFile) {
        const text = await backupFile.text()
        let blob: unknown
        try { blob = JSON.parse(text) } catch { throw new Error('That file is not a Vulos backup') }
        payload = { blob, passphrase: importPassphrase }
      } else {
        if (!uri.trim()) { setImportErr('Paste the otpauth-migration:// link, or choose a backup file'); setImporting(false); return }
        payload = { uri: uri.trim() }
      }
      const res = await fetch('/api/auth/totp/import', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
      const body: unknown = await res.json().catch(() => ({}))
      if (!res.ok) {
        setImportErr(errorMessage(body) || `Import failed (${res.status})`)
      } else {
        setResult(toTotpImportResult(body))
        onDone()
      }
    } catch (err: unknown) {
      setImportErr(caughtMessage(err, 'Could not read that file'))
    }
    setImporting(false)
  }

  const submitExport = async (e: FormEvent) => {
    e.preventDefault()
    setExportErr('')
    setExportDone(false)
    if (passphrase !== passphrase2) { setExportErr('The two backup passwords do not match'); return }
    if (passphrase.length < 8) { setExportErr('Backup password must be at least 8 characters'); return }
    setExporting(true)
    try {
      const res = await fetch('/api/auth/totp/export', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ account_password: accountPassword, passphrase }),
      })
      const body: unknown = await res.json().catch(() => ({}))
      if (!res.ok) {
        setExportErr(errorMessage(body) || `Export failed (${res.status})`)
      } else {
        const stamp = new Date().toISOString().slice(0, 10)
        const url = URL.createObjectURL(new Blob([JSON.stringify(body, null, 2)], { type: 'application/json' }))
        const a = document.createElement('a')
        a.href = url
        a.download = `vulos-2fa-${stamp}.json`
        document.body.appendChild(a)
        a.click()
        document.body.removeChild(a)
        URL.revokeObjectURL(url)
        setExportDone(true)
        setAccountPassword('')
        setPassphrase('')
        setPassphrase2('')
      }
    } catch {
      setExportErr('Could not reach the TOTP service')
    }
    setExporting(false)
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      <div role="tablist" style={{
        display: 'flex', gap: 4, padding: 4, borderRadius: 9,
        background: 'var(--bg-surface)', border: '1px solid var(--border-subtle)',
      }}>
        <button role="tab" aria-selected={tab === 'import'} style={tabStyle('import')} onClick={() => setTab('import')}>Import</button>
        <button role="tab" aria-selected={tab === 'export'} style={tabStyle('export')} onClick={() => setTab('export')}>Export</button>
      </div>

      {tab === 'import' && (
        <form onSubmit={submitImport} style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <p style={{ fontSize: 11, lineHeight: 1.6, color: 'var(--text-tertiary)', margin: 0 }}>
            In Google Authenticator, tap <strong>Transfer accounts → Export</strong>, scan the QR
            code it shows, and paste the <code>otpauth-migration://</code> link here.
          </p>

          <div>
            <label style={labelStyle} htmlFor="totp-migration-uri">Migration link</label>
            <input
              id="totp-migration-uri"
              type="text"
              value={uri}
              onChange={(e) => { setUri(e.target.value); setBackupFile(null); setResult(null); setImportErr('') }}
              placeholder="otpauth-migration://offline?data=..."
              style={inputStyle}
              autoComplete="off"
              spellCheck={false}
            />
          </div>

          <div>
            <label style={labelStyle} htmlFor="totp-backup-file">…or restore a Vulos backup</label>
            <input
              id="totp-backup-file"
              type="file"
              accept=".json"
              onChange={(e) => { setBackupFile(e.target.files?.[0] || null); setUri(''); setResult(null); setImportErr('') }}
              style={{ ...inputStyle, padding: '8px 10px', fontSize: 12 }}
            />
          </div>

          {backupFile && (
            <div>
              <label style={labelStyle} htmlFor="totp-backup-pass">Backup password</label>
              <input
                id="totp-backup-pass"
                type="password"
                value={importPassphrase}
                onChange={(e) => setImportPassphrase(e.target.value)}
                placeholder="The password you set when exporting"
                style={inputStyle}
                autoComplete="off"
              />
            </div>
          )}

          {importErr && <div role="alert" style={errStyle}>{importErr}</div>}

          {/* Honest accounting: what landed, what did not. */}
          {result && (
            <div role="status" style={{
              background: 'var(--bg-surface)', border: '1px solid var(--border-default)',
              borderRadius: 9, padding: 12, display: 'flex', flexDirection: 'column', gap: 8,
            }}>
              <div style={{ fontSize: 12, fontWeight: 600, color: 'var(--text-secondary)' }}>Import finished</div>
              <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
                <div>
                  <div style={{ fontSize: 15, fontWeight: 600, color: 'var(--status-success-text)' }}>{result.imported ?? 0}</div>
                  <div style={{ fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase' }}>Imported</div>
                </div>
                <div>
                  <div style={{ fontSize: 15, fontWeight: 600, color: (result.failed ?? 0) > 0 ? 'var(--status-danger)' : 'var(--text-secondary)' }}>
                    {result.failed ?? 0}
                  </div>
                  <div style={{ fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase' }}>Skipped</div>
                </div>
                {result.parsed != null && (
                  <div>
                    <div style={{ fontSize: 15, fontWeight: 600, color: 'var(--text-secondary)' }}>{result.parsed}</div>
                    <div style={{ fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase' }}>Found</div>
                  </div>
                )}
              </div>
              {Array.isArray(result.warnings) && result.warnings.length > 0 && (
                <ul style={{ margin: 0, paddingLeft: 16, display: 'flex', flexDirection: 'column', gap: 3 }}>
                  {result.warnings.map((wmsg, i) => (
                    <li key={i} style={{ fontSize: 11, color: 'var(--status-warning-text)' }}>{wmsg}</li>
                  ))}
                </ul>
              )}
            </div>
          )}

          <div style={{ display: 'flex', gap: 8 }}>
            <button type="button" onClick={onCancel} style={{
              flex: 1, minHeight: 44, padding: '10px 14px', borderRadius: 8, fontSize: 13, cursor: 'pointer',
              background: 'var(--bg-surface)', border: '1px solid var(--border-default)',
              color: 'var(--text-secondary)',
            }}>
              Done
            </button>
            <button type="submit" disabled={importing} style={primaryBtn(importing)}>
              {importing ? 'Importing…' : 'Import'}
            </button>
          </div>
        </form>
      )}

      {tab === 'export' && (
        <form onSubmit={submitExport} style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <p style={{ fontSize: 11, lineHeight: 1.6, color: 'var(--text-tertiary)', margin: 0 }}>
            Download an encrypted backup of your 2FA seeds. Without one, losing this
            box means losing access to every account these codes protect.
          </p>

          <div>
            <label style={labelStyle} htmlFor="totp-acct-pass">Your account password</label>
            <input
              id="totp-acct-pass"
              type="password"
              value={accountPassword}
              onChange={(e) => setAccountPassword(e.target.value)}
              placeholder="Confirm it's really you"
              style={inputStyle}
              autoComplete="current-password"
            />
          </div>

          <div>
            <label style={labelStyle} htmlFor="totp-exp-pass">Password for the backup file</label>
            <input
              id="totp-exp-pass"
              type="password"
              value={passphrase}
              onChange={(e) => setPassphrase(e.target.value)}
              placeholder="At least 8 characters"
              style={inputStyle}
              autoComplete="new-password"
            />
          </div>

          <div>
            <label style={labelStyle} htmlFor="totp-exp-pass2">Repeat backup password</label>
            <input
              id="totp-exp-pass2"
              type="password"
              value={passphrase2}
              onChange={(e) => setPassphrase2(e.target.value)}
              placeholder="Repeat it"
              style={inputStyle}
              autoComplete="new-password"
            />
          </div>

          <div style={noteStyle}>
            This password is not stored anywhere. If you lose it, the backup cannot
            be opened — not even by us.
          </div>

          {exportErr && <div role="alert" style={errStyle}>{exportErr}</div>}
          {exportDone && (
            <div role="status" style={{
              fontSize: 12, color: 'var(--status-success-text)', background: 'var(--status-success-soft)',
              border: '1px solid var(--status-success-soft)', borderRadius: 8, padding: '8px 10px',
            }}>
              Backup downloaded.
            </div>
          )}

          <div style={{ display: 'flex', gap: 8 }}>
            <button type="button" onClick={onCancel} style={{
              flex: 1, minHeight: 44, padding: '10px 14px', borderRadius: 8, fontSize: 13, cursor: 'pointer',
              background: 'var(--bg-surface)', border: '1px solid var(--border-default)',
              color: 'var(--text-secondary)',
            }}>
              Done
            </button>
            <button
              type="submit"
              disabled={exporting || !accountPassword || !passphrase || !passphrase2}
              style={primaryBtn(exporting || !accountPassword || !passphrase || !passphrase2)}
            >
              {exporting ? 'Exporting…' : 'Export'}
            </button>
          </div>
        </form>
      )}
    </div>
  )
}

// Main Authenticator component
export default function Authenticator() {
  const [accounts, setAccounts] = useState<TotpAccount[]>([])
  const [loading, setLoading] = useState(true)
  const [showAdd, setShowAdd] = useState(false)
  const [showTransfer, setShowTransfer] = useState(false)
  const [fetchErr, setFetchErr] = useState(false)

  const loadAccounts = useCallback(async () => {
    try {
      const res = await fetch('/api/auth/totp/list')
      if (!res.ok) { setFetchErr(true); setLoading(false); return }
      const data: unknown = await res.json()
      const list = Array.isArray(data)
        ? data
        : (isRecord(data) && Array.isArray(data.accounts) ? data.accounts : [])
      setAccounts(list.map(toTotpAccount))
      setFetchErr(false)
    } catch {
      setFetchErr(true)
    }
    setLoading(false)
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { loadAccounts() }, [loadAccounts])

  const handleAdded = (account: TotpAccount) => {
    // Merge new account; if the server returns the full record use it, otherwise refresh
    if (account && account.id) {
      setAccounts(prev => [...prev.filter(a => a.id !== account.id), account])
    } else {
      loadAccounts()
    }
    setShowAdd(false)
  }

  const handleDelete = (id: string | undefined) => {
    setAccounts(prev => prev.filter(a => a.id !== id))
  }

  const root: CSSProperties = {
    height: '100%',
    display: 'flex',
    flexDirection: 'column',
    background: 'var(--bg-base)',
    color: 'var(--text-primary)',
    fontFamily: "-apple-system, 'Inter', 'Segoe UI', sans-serif",
  }

  const header: CSSProperties = {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    gap: 12,
    flexWrap: 'wrap',
    padding: '16px 20px 12px',
    borderBottom: '1px solid var(--border-subtle)',
    flexShrink: 0,
  }

  const addBtn: CSSProperties = {
    display: 'flex',
    alignItems: 'center',
    gap: 5,
    minHeight: 36,
    padding: '7px 12px',
    borderRadius: 8,
    background: 'var(--accent)',
    border: '1px solid var(--accent)',
    color: 'var(--text-on-accent, #fff)',
    fontSize: 12,
    fontWeight: 600,
    cursor: 'pointer',
    transition: 'opacity var(--motion-fast, 0.15s) var(--ease-out, ease)',
  }

  return (
    <div style={root}>
      {/* Header */}
      <div style={header}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
          <div style={{
            width: 28, height: 28, borderRadius: 7,
            background: 'var(--accent-soft)',
            color: 'var(--accent)',
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            fontSize: 14, flexShrink: 0,
          }}>
            ⊛
          </div>
          <div style={{ minWidth: 0 }}>
            <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--text-primary)' }}>Authenticator</div>
            <div style={{ fontSize: 10, color: 'var(--text-muted)', marginTop: 1 }}>
              {accounts.length} account{accounts.length !== 1 ? 's' : ''}
            </div>
          </div>
        </div>
        {!showAdd && !showTransfer && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
            <button
              style={{
                display: 'flex', alignItems: 'center', gap: 5,
                minHeight: 36, padding: '7px 12px', borderRadius: 8,
                background: 'var(--bg-surface)',
                border: '1px solid var(--border-default)',
                color: 'var(--text-secondary)',
                fontSize: 12, fontWeight: 500, cursor: 'pointer',
                transition: 'background var(--motion-fast, 0.15s) var(--ease-out, ease)',
              }}
              onClick={() => setShowTransfer(true)}
              title="Import or export your 2FA accounts"
              aria-label="Import or export your 2FA accounts"
              onMouseEnter={e => e.currentTarget.style.background = 'var(--bg-hover)'}
              onMouseLeave={e => e.currentTarget.style.background = 'var(--bg-surface)'}
            >
              ⇄ Import / Export
            </button>
            <button
              style={addBtn}
              onClick={() => setShowAdd(true)}
              onMouseEnter={e => e.currentTarget.style.opacity = '0.9'}
              onMouseLeave={e => e.currentTarget.style.opacity = '1'}
            >
              <span style={{ fontSize: 16, lineHeight: 1, marginTop: -1 }}>+</span>
              Add account
            </button>
          </div>
        )}
      </div>

      {/* Body */}
      <div style={{ flex: 1, overflowY: 'auto', padding: '14px 16px' }}>
        {showTransfer ? (
          <div>
            <div style={{ fontSize: 12, fontWeight: 600, color: 'var(--text-secondary)', marginBottom: 14 }}>
              Import &amp; export accounts
            </div>
            <TransferPanel
              onDone={loadAccounts}
              onCancel={() => setShowTransfer(false)}
            />
          </div>
        ) : showAdd ? (
          <div>
            <div style={{ fontSize: 12, fontWeight: 600, color: 'var(--text-secondary)', marginBottom: 14 }}>
              Add new account
            </div>
            <AddAccountForm onAdd={handleAdded} onCancel={() => setShowAdd(false)} />
          </div>
        ) : loading ? (
          <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: 120, color: 'var(--text-muted)', fontSize: 13 }}>
            <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span className="vulos-auth-spin" style={{
                width: 14, height: 14,
                border: '2px solid var(--border-default)',
                borderTopColor: 'var(--accent)',
                borderRadius: '50%',
                display: 'inline-block',
              }} />
              Loading...
            </span>
          </div>
        ) : fetchErr ? (
          <div style={{
            textAlign: 'center', color: 'var(--status-danger-text)', fontSize: 13, padding: 24,
            background: 'var(--status-danger-soft)', borderRadius: 12,
            border: '1px solid var(--status-danger-soft)',
          }}>
            <div style={{ fontSize: 20, marginBottom: 8 }}>⚠</div>
            Could not connect to the TOTP service.
            <br />
            <button
              onClick={loadAccounts}
              style={{
                marginTop: 12, minHeight: 36, padding: '8px 14px', borderRadius: 8, fontSize: 12, cursor: 'pointer',
                background: 'var(--status-danger-soft)', border: '1px solid var(--status-danger)', color: 'var(--status-danger-text)',
              }}
            >
              Retry
            </button>
          </div>
        ) : accounts.length === 0 ? (
          <div style={{
            display: 'flex', flexDirection: 'column', alignItems: 'center',
            justifyContent: 'center', minHeight: 200, gap: 12, padding: '24px 8px',
            color: 'var(--text-muted)',
          }}>
            <div style={{
              width: 64, height: 64, borderRadius: 16, display: 'flex', alignItems: 'center', justifyContent: 'center',
              background: 'var(--accent-soft)', color: 'var(--accent)', fontSize: 30,
            }}>⊛</div>
            <div style={{ fontSize: 13, color: 'var(--text-secondary)' }}>No accounts yet</div>
            <div style={{ display: 'flex', gap: 8, marginTop: 4, flexWrap: 'wrap', justifyContent: 'center' }}>
              <button
                onClick={() => setShowAdd(true)}
                style={{
                  minHeight: 40, padding: '9px 18px', borderRadius: 8, fontSize: 12, cursor: 'pointer',
                  background: 'var(--accent)', border: '1px solid var(--accent)',
                  color: 'var(--text-on-accent, #fff)', fontWeight: 600,
                }}
              >
                + Add your first account
              </button>
              <button
                onClick={() => setShowTransfer(true)}
                style={{
                  minHeight: 40, padding: '9px 18px', borderRadius: 8, fontSize: 12, cursor: 'pointer',
                  background: 'var(--bg-surface)', border: '1px solid var(--border-default)',
                  color: 'var(--text-secondary)', fontWeight: 500,
                }}
              >
                Import from Google Authenticator
              </button>
            </div>
          </div>
        ) : (
          accounts.map(account => (
            <AccountRow key={account.id} account={account} onDelete={handleDelete} />
          ))
        )}
      </div>

      {/* Keyframe for spinner — injected once via a style tag, disabled under reduced motion */}
      <style>{`
        @keyframes vulos-auth-spin { to { transform: rotate(360deg); } }
        .vulos-auth-spin { animation: vulos-auth-spin 0.8s linear infinite; }
        @media (prefers-reduced-motion: reduce) {
          .vulos-auth-spin { animation-duration: 2s; }
        }
      `}</style>
    </div>
  )
}
