import { useState, useEffect, useCallback, useRef } from 'react'

// Seconds remaining in the current 30-second TOTP window
function useTotpClock() {
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
function AccountRow({ account, onDelete }) {
  const [code, setCode] = useState(null)
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState(false)
  const secondsLeft = useTotpClock()
  const prevWindowRef = useRef(null)

  const fetchCode = useCallback(async () => {
    try {
      const res = await fetch(`/api/auth/totp/code/${account.id}`)
      if (!res.ok) { setError(true); return }
      const data = await res.json()
      setCode(data.code || data.otp || null)
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
  const urgency = secondsLeft <= 5

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
        borderRadius: 10,
        background: 'rgba(255,255,255,0.04)',
        border: '1px solid rgba(255,255,255,0.07)',
        marginBottom: 8,
        cursor: 'pointer',
        transition: 'background 0.15s',
        userSelect: 'none',
      }}
      onClick={handleCopy}
      role="button"
      aria-label={`Copy ${account.name} code`}
      title="Click to copy"
    >
      {/* Account icon / initial */}
      <div style={{
        width: 34,
        height: 34,
        borderRadius: 8,
        background: 'rgba(99,102,241,0.25)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        fontSize: 15,
        fontWeight: 700,
        color: '#818cf8',
        flexShrink: 0,
        textTransform: 'uppercase',
      }}>
        {(account.name?.[0] || '?')}
      </div>

      {/* Name + code */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{
          fontSize: 11,
          color: 'rgba(255,255,255,0.45)',
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
          fontFamily: "'SF Mono', 'Cascadia Code', 'Fira Code', monospace",
          color: urgency ? '#f87171' : copied ? '#4ade80' : '#f5f5f5',
          transition: 'color 0.2s',
        }}>
          {copied ? 'Copied!' : displayCode}
        </div>
      </div>

      {/* Countdown ring */}
      <div style={{ flexShrink: 0, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 3 }}>
        <svg width={28} height={28} viewBox="0 0 28 28">
          {/* Background track */}
          <circle cx={14} cy={14} r={R} fill="none" stroke="rgba(255,255,255,0.08)" strokeWidth={2.5} />
          {/* Progress arc — rotated so it depletes clockwise */}
          <circle
            cx={14} cy={14} r={R}
            fill="none"
            stroke={urgency ? '#f87171' : '#818cf8'}
            strokeWidth={2.5}
            strokeLinecap="round"
            strokeDasharray={`${dash} ${CIRC}`}
            style={{ transform: 'rotate(-90deg)', transformOrigin: '14px 14px', transition: 'stroke 0.3s' }}
          />
        </svg>
        <span style={{
          fontSize: 9,
          color: urgency ? '#f87171' : 'rgba(255,255,255,0.3)',
          fontVariantNumeric: 'tabular-nums',
          transition: 'color 0.3s',
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
          color: 'rgba(255,255,255,0.2)',
          cursor: 'pointer',
          padding: '4px 6px',
          borderRadius: 6,
          fontSize: 14,
          lineHeight: 1,
          flexShrink: 0,
          transition: 'color 0.15s',
        }}
        onMouseEnter={e => e.currentTarget.style.color = '#f87171'}
        onMouseLeave={e => e.currentTarget.style.color = 'rgba(255,255,255,0.2)'}
      >
        ✕
      </button>
    </div>
  )
}

// Add-account form — supports paste otpauth URI or manual entry
function AddAccountForm({ onAdd, onCancel }) {
  const [uri, setUri] = useState('')
  const [manual, setManual] = useState(false)
  const [name, setName] = useState('')
  const [issuer, setIssuer] = useState('')
  const [secret, setSecret] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [err, setErr] = useState('')
  const firstRef = useRef(null)

  useEffect(() => { firstRef.current?.focus() }, [])

  const parseOtpauth = (rawUri) => {
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

  const submit = async (e) => {
    e.preventDefault()
    setErr('')
    setSubmitting(true)

    let payload
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
        const body = await res.json().catch(() => ({}))
        setErr(body.error || `Server error ${res.status}`)
        setSubmitting(false)
        return
      }
      const added = await res.json()
      onAdd(added)
    } catch {
      setErr('Network error — could not reach the server.')
    }
    setSubmitting(false)
  }

  const inputStyle = {
    width: '100%',
    background: 'rgba(255,255,255,0.06)',
    border: '1px solid rgba(255,255,255,0.12)',
    borderRadius: 8,
    color: '#f5f5f5',
    fontSize: 13,
    padding: '8px 12px',
    outline: 'none',
    boxSizing: 'border-box',
  }

  const labelStyle = {
    fontSize: 11,
    color: 'rgba(255,255,255,0.45)',
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
            flex: 1, padding: '6px 0', borderRadius: 7, fontSize: 12, cursor: 'pointer',
            background: !manual ? 'rgba(99,102,241,0.3)' : 'rgba(255,255,255,0.05)',
            border: !manual ? '1px solid rgba(99,102,241,0.5)' : '1px solid rgba(255,255,255,0.1)',
            color: !manual ? '#a5b4fc' : 'rgba(255,255,255,0.5)',
            transition: 'all 0.15s',
          }}
        >
          Paste URI
        </button>
        <button
          type="button"
          onClick={() => setManual(true)}
          style={{
            flex: 1, padding: '6px 0', borderRadius: 7, fontSize: 12, cursor: 'pointer',
            background: manual ? 'rgba(99,102,241,0.3)' : 'rgba(255,255,255,0.05)',
            border: manual ? '1px solid rgba(99,102,241,0.5)' : '1px solid rgba(255,255,255,0.1)',
            color: manual ? '#a5b4fc' : 'rgba(255,255,255,0.5)',
            transition: 'all 0.15s',
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
            style={{ ...inputStyle, resize: 'vertical', fontFamily: "'SF Mono', monospace", fontSize: 11 }}
          />
          <div style={{ fontSize: 10, color: 'rgba(255,255,255,0.25)', marginTop: 4 }}>
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
              style={{ ...inputStyle, fontFamily: "'SF Mono', monospace", letterSpacing: '0.08em' }}
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
        <div style={{ fontSize: 12, color: '#f87171', padding: '8px 10px', background: 'rgba(239,68,68,0.1)', borderRadius: 6, border: '1px solid rgba(239,68,68,0.2)' }}>
          {err}
        </div>
      )}

      <div style={{ display: 'flex', gap: 8, marginTop: 4 }}>
        <button
          type="button"
          onClick={onCancel}
          style={{
            flex: 1, padding: '8px 0', borderRadius: 8, fontSize: 13, cursor: 'pointer',
            background: 'rgba(255,255,255,0.05)', border: '1px solid rgba(255,255,255,0.1)',
            color: 'rgba(255,255,255,0.5)',
          }}
        >
          Cancel
        </button>
        <button
          type="submit"
          disabled={submitting}
          style={{
            flex: 2, padding: '8px 0', borderRadius: 8, fontSize: 13, fontWeight: 600, cursor: submitting ? 'default' : 'pointer',
            background: submitting ? 'rgba(99,102,241,0.2)' : 'rgba(99,102,241,0.5)',
            border: '1px solid rgba(99,102,241,0.4)',
            color: submitting ? 'rgba(165,180,252,0.5)' : '#a5b4fc',
            transition: 'all 0.15s',
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
function TransferPanel({ onDone, onCancel }) {
  const [tab, setTab] = useState('import')

  const [uri, setUri] = useState('')
  const [backupFile, setBackupFile] = useState(null)
  const [importPassphrase, setImportPassphrase] = useState('')
  const [importing, setImporting] = useState(false)
  const [importErr, setImportErr] = useState('')
  const [result, setResult] = useState(null)

  const [accountPassword, setAccountPassword] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [passphrase2, setPassphrase2] = useState('')
  const [exporting, setExporting] = useState(false)
  const [exportErr, setExportErr] = useState('')
  const [exportDone, setExportDone] = useState(false)

  const inputStyle = {
    width: '100%',
    background: 'rgba(255,255,255,0.06)',
    border: '1px solid rgba(255,255,255,0.12)',
    borderRadius: 8,
    color: '#f5f5f5',
    fontSize: 13,
    padding: '8px 12px',
    outline: 'none',
    boxSizing: 'border-box',
  }
  const labelStyle = { fontSize: 11, color: 'rgba(255,255,255,0.45)', marginBottom: 4, display: 'block' }
  const primaryBtn = (disabled) => ({
    width: '100%', padding: '9px 14px', borderRadius: 8, fontSize: 13, fontWeight: 500,
    cursor: disabled ? 'default' : 'pointer', opacity: disabled ? 0.4 : 1,
    background: 'rgba(99,102,241,0.2)', border: '1px solid rgba(99,102,241,0.35)', color: '#a5b4fc',
  })
  const noteStyle = {
    fontSize: 11, lineHeight: 1.5, color: 'rgba(251,191,36,0.85)',
    background: 'rgba(251,191,36,0.08)', border: '1px solid rgba(251,191,36,0.2)',
    borderRadius: 8, padding: '8px 10px',
  }
  const errStyle = {
    fontSize: 12, color: '#fca5a5', background: 'rgba(239,68,68,0.1)',
    border: '1px solid rgba(239,68,68,0.2)', borderRadius: 8, padding: '8px 10px',
  }
  const tabStyle = (t) => ({
    flex: 1, padding: '6px 0', borderRadius: 7, fontSize: 12, fontWeight: 500, cursor: 'pointer',
    background: tab === t ? 'rgba(255,255,255,0.1)' : 'transparent',
    border: 'none',
    color: tab === t ? '#f5f5f5' : 'rgba(255,255,255,0.4)',
  })

  const submitImport = async (e) => {
    e.preventDefault()
    setImporting(true)
    setImportErr('')
    setResult(null)
    try {
      let payload
      if (backupFile) {
        const text = await backupFile.text()
        let blob
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
      const body = await res.json().catch(() => ({}))
      if (!res.ok) {
        setImportErr(body.error || `Import failed (${res.status})`)
      } else {
        setResult(body)
        onDone()
      }
    } catch (err) {
      setImportErr(err.message || 'Could not read that file')
    }
    setImporting(false)
  }

  const submitExport = async (e) => {
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
      const body = await res.json().catch(() => ({}))
      if (!res.ok) {
        setExportErr(body.error || `Export failed (${res.status})`)
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
        background: 'rgba(255,255,255,0.04)', border: '1px solid rgba(255,255,255,0.08)',
      }}>
        <button role="tab" aria-selected={tab === 'import'} style={tabStyle('import')} onClick={() => setTab('import')}>Import</button>
        <button role="tab" aria-selected={tab === 'export'} style={tabStyle('export')} onClick={() => setTab('export')}>Export</button>
      </div>

      {tab === 'import' && (
        <form onSubmit={submitImport} style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <p style={{ fontSize: 11, lineHeight: 1.6, color: 'rgba(255,255,255,0.45)', margin: 0 }}>
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
              style={{ ...inputStyle, padding: '6px 10px', fontSize: 12 }}
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
              background: 'rgba(255,255,255,0.04)', border: '1px solid rgba(255,255,255,0.1)',
              borderRadius: 9, padding: 12, display: 'flex', flexDirection: 'column', gap: 8,
            }}>
              <div style={{ fontSize: 12, fontWeight: 600, color: 'rgba(255,255,255,0.75)' }}>Import finished</div>
              <div style={{ display: 'flex', gap: 16 }}>
                <div>
                  <div style={{ fontSize: 15, fontWeight: 600, color: '#4ade80' }}>{result.imported ?? 0}</div>
                  <div style={{ fontSize: 10, color: 'rgba(255,255,255,0.35)', textTransform: 'uppercase' }}>Imported</div>
                </div>
                <div>
                  <div style={{ fontSize: 15, fontWeight: 600, color: (result.failed ?? 0) > 0 ? '#f87171' : 'rgba(255,255,255,0.7)' }}>
                    {result.failed ?? 0}
                  </div>
                  <div style={{ fontSize: 10, color: 'rgba(255,255,255,0.35)', textTransform: 'uppercase' }}>Skipped</div>
                </div>
                {result.parsed != null && (
                  <div>
                    <div style={{ fontSize: 15, fontWeight: 600, color: 'rgba(255,255,255,0.7)' }}>{result.parsed}</div>
                    <div style={{ fontSize: 10, color: 'rgba(255,255,255,0.35)', textTransform: 'uppercase' }}>Found</div>
                  </div>
                )}
              </div>
              {Array.isArray(result.warnings) && result.warnings.length > 0 && (
                <ul style={{ margin: 0, paddingLeft: 16, display: 'flex', flexDirection: 'column', gap: 3 }}>
                  {result.warnings.map((wmsg, i) => (
                    <li key={i} style={{ fontSize: 11, color: 'rgba(251,191,36,0.85)' }}>{wmsg}</li>
                  ))}
                </ul>
              )}
            </div>
          )}

          <div style={{ display: 'flex', gap: 8 }}>
            <button type="button" onClick={onCancel} style={{
              flex: 1, padding: '9px 14px', borderRadius: 8, fontSize: 13, cursor: 'pointer',
              background: 'rgba(255,255,255,0.05)', border: '1px solid rgba(255,255,255,0.1)',
              color: 'rgba(255,255,255,0.6)',
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
          <p style={{ fontSize: 11, lineHeight: 1.6, color: 'rgba(255,255,255,0.45)', margin: 0 }}>
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
              fontSize: 12, color: '#4ade80', background: 'rgba(74,222,128,0.1)',
              border: '1px solid rgba(74,222,128,0.2)', borderRadius: 8, padding: '8px 10px',
            }}>
              Backup downloaded.
            </div>
          )}

          <div style={{ display: 'flex', gap: 8 }}>
            <button type="button" onClick={onCancel} style={{
              flex: 1, padding: '9px 14px', borderRadius: 8, fontSize: 13, cursor: 'pointer',
              background: 'rgba(255,255,255,0.05)', border: '1px solid rgba(255,255,255,0.1)',
              color: 'rgba(255,255,255,0.6)',
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
  const [accounts, setAccounts] = useState([])
  const [loading, setLoading] = useState(true)
  const [showAdd, setShowAdd] = useState(false)
  const [showTransfer, setShowTransfer] = useState(false)
  const [fetchErr, setFetchErr] = useState(false)

  const loadAccounts = useCallback(async () => {
    try {
      const res = await fetch('/api/auth/totp/list')
      if (!res.ok) { setFetchErr(true); setLoading(false); return }
      const data = await res.json()
      setAccounts(Array.isArray(data) ? data : (data.accounts || []))
      setFetchErr(false)
    } catch {
      setFetchErr(true)
    }
    setLoading(false)
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { loadAccounts() }, [loadAccounts])

  const handleAdded = (account) => {
    // Merge new account; if the server returns the full record use it, otherwise refresh
    if (account && account.id) {
      setAccounts(prev => [...prev.filter(a => a.id !== account.id), account])
    } else {
      loadAccounts()
    }
    setShowAdd(false)
  }

  const handleDelete = (id) => {
    setAccounts(prev => prev.filter(a => a.id !== id))
  }

  const root = {
    height: '100%',
    display: 'flex',
    flexDirection: 'column',
    background: '#0d0d0f',
    color: '#f5f5f5',
    fontFamily: "-apple-system, 'Inter', 'Segoe UI', sans-serif",
  }

  const header = {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '16px 20px 12px',
    borderBottom: '1px solid rgba(255,255,255,0.06)',
    flexShrink: 0,
  }

  const addBtn = {
    display: 'flex',
    alignItems: 'center',
    gap: 5,
    padding: '6px 12px',
    borderRadius: 8,
    background: 'rgba(99,102,241,0.2)',
    border: '1px solid rgba(99,102,241,0.35)',
    color: '#a5b4fc',
    fontSize: 12,
    fontWeight: 500,
    cursor: 'pointer',
    transition: 'background 0.15s',
  }

  return (
    <div style={root}>
      {/* Header */}
      <div style={header}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <div style={{
            width: 28, height: 28, borderRadius: 7,
            background: 'rgba(99,102,241,0.25)',
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            fontSize: 14,
          }}>
            ⊛
          </div>
          <div>
            <div style={{ fontSize: 13, fontWeight: 600 }}>Authenticator</div>
            <div style={{ fontSize: 10, color: 'rgba(255,255,255,0.35)', marginTop: 1 }}>
              {accounts.length} account{accounts.length !== 1 ? 's' : ''}
            </div>
          </div>
        </div>
        {!showAdd && !showTransfer && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <button
              style={{
                display: 'flex', alignItems: 'center', gap: 5,
                padding: '6px 12px', borderRadius: 8,
                background: 'rgba(255,255,255,0.05)',
                border: '1px solid rgba(255,255,255,0.1)',
                color: 'rgba(255,255,255,0.6)',
                fontSize: 12, fontWeight: 500, cursor: 'pointer',
                transition: 'background 0.15s',
              }}
              onClick={() => setShowTransfer(true)}
              title="Import or export your 2FA accounts"
              aria-label="Import or export your 2FA accounts"
              onMouseEnter={e => e.currentTarget.style.background = 'rgba(255,255,255,0.1)'}
              onMouseLeave={e => e.currentTarget.style.background = 'rgba(255,255,255,0.05)'}
            >
              ⇄ Import / Export
            </button>
            <button
              style={addBtn}
              onClick={() => setShowAdd(true)}
              onMouseEnter={e => e.currentTarget.style.background = 'rgba(99,102,241,0.35)'}
              onMouseLeave={e => e.currentTarget.style.background = 'rgba(99,102,241,0.2)'}
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
            <div style={{ fontSize: 12, fontWeight: 600, color: 'rgba(255,255,255,0.6)', marginBottom: 14 }}>
              Import &amp; export accounts
            </div>
            <TransferPanel
              onDone={loadAccounts}
              onCancel={() => setShowTransfer(false)}
            />
          </div>
        ) : showAdd ? (
          <div>
            <div style={{ fontSize: 12, fontWeight: 600, color: 'rgba(255,255,255,0.6)', marginBottom: 14 }}>
              Add new account
            </div>
            <AddAccountForm onAdd={handleAdded} onCancel={() => setShowAdd(false)} />
          </div>
        ) : loading ? (
          <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: 120, color: 'rgba(255,255,255,0.25)', fontSize: 13 }}>
            <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span style={{
                width: 14, height: 14,
                border: '2px solid rgba(255,255,255,0.1)',
                borderTopColor: '#818cf8',
                borderRadius: '50%',
                display: 'inline-block',
                animation: 'spin 0.8s linear infinite',
              }} />
              Loading...
            </span>
          </div>
        ) : fetchErr ? (
          <div style={{
            textAlign: 'center', color: '#f87171', fontSize: 13, padding: 24,
            background: 'rgba(239,68,68,0.08)', borderRadius: 10,
            border: '1px solid rgba(239,68,68,0.15)',
          }}>
            <div style={{ fontSize: 20, marginBottom: 8 }}>⚠</div>
            Could not connect to the TOTP service.
            <br />
            <button
              onClick={loadAccounts}
              style={{
                marginTop: 12, padding: '6px 14px', borderRadius: 7, fontSize: 12, cursor: 'pointer',
                background: 'rgba(239,68,68,0.15)', border: '1px solid rgba(239,68,68,0.25)', color: '#fca5a5',
              }}
            >
              Retry
            </button>
          </div>
        ) : accounts.length === 0 ? (
          <div style={{
            display: 'flex', flexDirection: 'column', alignItems: 'center',
            justifyContent: 'center', height: 160, gap: 12,
            color: 'rgba(255,255,255,0.3)',
          }}>
            <div style={{ fontSize: 36 }}>⊛</div>
            <div style={{ fontSize: 13 }}>No accounts yet</div>
            <div style={{ display: 'flex', gap: 8, marginTop: 4 }}>
              <button
                onClick={() => setShowAdd(true)}
                style={{
                  padding: '7px 18px', borderRadius: 8, fontSize: 12, cursor: 'pointer',
                  background: 'rgba(99,102,241,0.2)', border: '1px solid rgba(99,102,241,0.35)',
                  color: '#a5b4fc', fontWeight: 500,
                }}
              >
                + Add your first account
              </button>
              <button
                onClick={() => setShowTransfer(true)}
                style={{
                  padding: '7px 18px', borderRadius: 8, fontSize: 12, cursor: 'pointer',
                  background: 'rgba(255,255,255,0.05)', border: '1px solid rgba(255,255,255,0.1)',
                  color: 'rgba(255,255,255,0.6)', fontWeight: 500,
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

      {/* Keyframe for spinner — injected once via a style tag */}
      <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
    </div>
  )
}
