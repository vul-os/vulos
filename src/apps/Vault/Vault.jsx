import { useState, useEffect, useCallback, useRef } from 'react'

// ── Inactivity relock timer (5 minutes) ──────────────────────────────────────
const RELOCK_MS = 5 * 60 * 1000

function useRelockTimer(locked, onLock) {
  const timerRef = useRef(null)

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
function useGenerator() {
  const [opts, setOpts] = useState({ length: 20, upper: true, lower: true, digits: true, symbols: true })
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
        const data = await res.json()
        setResult(data.password || data.value || '')
      }
    } catch { /* ignore */ }
    setLoading(false)
  }, [opts])

  return { opts, setOpts, result, setResult, loading, generate }
}

// ── Copy helper with confirm flash ───────────────────────────────────────────
function useCopy() {
  const [copied, setCopied] = useState(null) // key string
  const copy = useCallback(async (text, key) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(key)
      setTimeout(() => setCopied(null), 1800)
    } catch { /* ignore */ }
  }, [])
  return { copied, copy }
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

const IconEye = ({ open }) => open ? (
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

// ── Unlock screen ─────────────────────────────────────────────────────────────
function UnlockScreen({ onUnlock }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const inputRef = useRef(null)

  useEffect(() => { inputRef.current?.focus() }, [])

  const handleSubmit = async (e) => {
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
        const data = await res.json().catch(() => ({}))
        setError(data.error || data.message || 'Wrong master password')
      }
    } catch {
      setError('Could not reach vault service')
    }
    setLoading(false)
  }

  return (
    <div className="flex flex-col items-center justify-center h-full min-h-[300px] px-8 gap-6">
      <div className="flex flex-col items-center gap-2">
        <div className="w-14 h-14 rounded-2xl bg-neutral-800 flex items-center justify-center text-neutral-400">
          <IconLock />
        </div>
        <h2 className="text-white font-semibold text-lg">Vault Locked</h2>
        <p className="text-neutral-500 text-sm text-center">Enter your master password to unlock</p>
      </div>

      <form onSubmit={handleSubmit} className="w-full max-w-xs flex flex-col gap-3">
        <input
          ref={inputRef}
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="Master password"
          className="w-full bg-neutral-800 border border-neutral-700 rounded-lg px-4 py-2.5 text-sm text-white outline-none placeholder:text-neutral-600 focus:border-neutral-500 transition-colors"
          autoComplete="current-password"
        />
        {error && (
          <p className="text-red-400 text-xs text-center">{error}</p>
        )}
        <button
          type="submit"
          disabled={loading || !password}
          className="w-full bg-blue-600 hover:bg-blue-500 disabled:opacity-40 disabled:cursor-default text-white rounded-lg py-2.5 text-sm font-medium transition-colors"
        >
          {loading ? 'Unlocking…' : 'Unlock Vault'}
        </button>
      </form>
    </div>
  )
}

// ── Generator panel (inline, can inject into a setter) ───────────────────────
function GeneratorPanel({ onInsert, onClose }) {
  const gen = useGenerator()
  const { copied, copy } = useCopy()

  const handleGenerate = () => { gen.generate() }

  const toggle = (field) => gen.setOpts(prev => ({ ...prev, [field]: !prev[field] }))

  return (
    <div className="bg-neutral-850 border border-neutral-700/60 rounded-xl p-4 flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-neutral-400 uppercase tracking-wider">Password Generator</span>
        {onClose && (
          <button onClick={onClose} className="text-neutral-600 hover:text-neutral-400 text-lg leading-none">×</button>
        )}
      </div>

      {/* Length slider */}
      <div className="flex items-center gap-3">
        <span className="text-xs text-neutral-500 w-12">Length</span>
        <input
          type="range"
          min={8}
          max={64}
          value={gen.opts.length}
          onChange={(e) => gen.setOpts(prev => ({ ...prev, length: Number(e.target.value) }))}
          className="flex-1 accent-blue-500"
        />
        <span className="text-xs text-neutral-300 w-6 text-right">{gen.opts.length}</span>
      </div>

      {/* Character set toggles */}
      <div className="flex gap-2 flex-wrap">
        {[
          { field: 'upper', label: 'A–Z' },
          { field: 'lower', label: 'a–z' },
          { field: 'digits', label: '0–9' },
          { field: 'symbols', label: '!@#' },
        ].map(({ field, label }) => (
          <button
            key={field}
            onClick={() => toggle(field)}
            className={`px-2.5 py-1 rounded-md text-xs font-mono transition-colors ${
              gen.opts[field]
                ? 'bg-blue-600/30 text-blue-300 border border-blue-500/40'
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
        className="flex items-center justify-center gap-2 w-full bg-neutral-700/60 hover:bg-neutral-600/60 disabled:opacity-40 text-neutral-200 rounded-lg py-2 text-sm transition-colors"
      >
        <IconWand />
        {gen.loading ? 'Generating…' : 'Generate'}
      </button>

      {/* Result row */}
      {gen.result && (
        <div className="flex items-center gap-2 bg-neutral-900 border border-neutral-700/50 rounded-lg px-3 py-2">
          <span className="flex-1 font-mono text-xs text-green-400 break-all">{gen.result}</span>
          <button
            onClick={() => copy(gen.result, 'gen')}
            className="text-neutral-500 hover:text-neutral-200 shrink-0 transition-colors"
            title="Copy"
          >
            {copied === 'gen' ? <span className="text-green-400 text-xs">Copied!</span> : <IconCopy />}
          </button>
          {onInsert && (
            <button
              onClick={() => { onInsert(gen.result); onClose && onClose() }}
              className="text-xs text-blue-400 hover:text-blue-300 shrink-0 ml-1 transition-colors"
            >
              Use
            </button>
          )}
        </div>
      )}
    </div>
  )
}

// ── Entry form (add / edit) ───────────────────────────────────────────────────
function EntryForm({ existing, onSave, onCancel }) {
  const isEdit = !!existing
  const [form, setForm] = useState({
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

  const set = (k) => (e) => setForm(prev => ({ ...prev, [k]: e.target.value }))

  const handleSave = async (e) => {
    e.preventDefault()
    if (!form.title) { setError('Title is required'); return }
    setSaving(true)
    setError('')
    try {
      const payload = { ...form }
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
        const data = await res.json()
        onSave(data)
      } else {
        const data = await res.json().catch(() => ({}))
        setError(data.error || data.message || 'Save failed')
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
        <button type="button" onClick={onCancel} className="text-neutral-500 hover:text-neutral-200 transition-colors">
          <IconBack />
        </button>
        <h3 className="text-sm font-semibold text-white flex-1">
          {isEdit ? 'Edit Entry' : 'New Entry'}
        </h3>
        <button
          type="submit"
          disabled={saving}
          className="px-3 py-1.5 bg-blue-600 hover:bg-blue-500 disabled:opacity-40 text-white rounded-lg text-xs font-medium transition-colors"
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>

      <div className="flex flex-col gap-3 px-4 py-3">
        {error && <p className="text-red-400 text-xs bg-red-900/20 border border-red-800/40 rounded-lg px-3 py-2">{error}</p>}

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
              className={`${inputCls} pr-16`}
              autoComplete="new-password"
            />
            <div className="absolute right-2 top-1/2 -translate-y-1/2 flex gap-1">
              <button
                type="button"
                onClick={() => setShowPass(s => !s)}
                className="text-neutral-500 hover:text-neutral-300 p-1 transition-colors"
                title={showPass ? 'Hide' : 'Show'}
              >
                <IconEye open={showPass} />
              </button>
              <button
                type="button"
                onClick={() => setShowGen(s => !s)}
                className="text-neutral-500 hover:text-neutral-300 p-1 transition-colors"
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

function Field({ label, required, children }) {
  return (
    <div className="flex flex-col gap-1">
      <label className="text-xs text-neutral-500 font-medium">
        {label}{required && <span className="text-red-500 ml-0.5">*</span>}
      </label>
      {children}
    </div>
  )
}

const inputCls = 'w-full bg-neutral-800 border border-neutral-700 rounded-lg px-3 py-2 text-sm text-white outline-none placeholder:text-neutral-600 focus:border-neutral-500 transition-colors'

// ── Entry detail view ─────────────────────────────────────────────────────────
function EntryDetail({ entryMeta, onBack, onEdit, onDelete }) {
  const [entry, setEntry] = useState(null)
  const [loading, setLoading] = useState(true)
  const [showPass, setShowPass] = useState(false)
  const [delConfirm, setDelConfirm] = useState(false)
  const { copied, copy } = useCopy()

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoading(true)
    fetch(`/api/auth/vault/entry/${entryMeta.id}`)
      .then(r => r.ok ? r.json() : null)
      .then(data => { setEntry(data); setLoading(false) })
      .catch(() => setLoading(false))
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
        <button onClick={onBack} className="text-blue-400 hover:text-blue-300 text-sm">Back</button>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full overflow-y-auto">
      {/* Header */}
      <div className="flex items-center gap-3 px-4 py-3 border-b border-neutral-800 shrink-0">
        <button onClick={onBack} className="text-neutral-500 hover:text-neutral-200 transition-colors">
          <IconBack />
        </button>
        <h3 className="text-sm font-semibold text-white flex-1 truncate">
          {entry.title || entry.name || entryMeta.title || entryMeta.name}
        </h3>
        <button
          onClick={() => onEdit(entry)}
          className="text-neutral-500 hover:text-neutral-200 p-1.5 rounded-lg hover:bg-white/5 transition-colors"
          title="Edit"
        >
          <IconEdit />
        </button>
        <button
          onClick={handleDelete}
          className={`p-1.5 rounded-lg transition-colors ${
            delConfirm
              ? 'text-red-400 bg-red-900/20 hover:bg-red-900/40'
              : 'text-neutral-500 hover:text-red-400 hover:bg-white/5'
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
            value={entry.username || entry.email}
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
                  className="text-neutral-500 hover:text-neutral-300 p-1 rounded transition-colors"
                  title={showPass ? 'Hide' : 'Reveal'}
                >
                  <IconEye open={showPass} />
                </button>
                <button
                  onClick={() => copy(entry.password, 'password')}
                  className="text-neutral-500 hover:text-neutral-300 p-1 rounded transition-colors"
                  title="Copy"
                >
                  {copied === 'password'
                    ? <span className="text-green-400 text-xs font-medium">Copied!</span>
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
            <p className="text-sm text-neutral-300 whitespace-pre-wrap">{entry.notes}</p>
          </div>
        )}
      </div>

      {delConfirm && (
        <div className="mx-4 mb-4 bg-red-900/20 border border-red-800/40 rounded-lg px-3 py-2 text-xs text-red-400">
          Click the trash icon again to permanently delete this entry.
        </div>
      )}
    </div>
  )
}

function DetailRow({ label, value, copyKey, copied, copy }) {
  return (
    <div className="py-3 border-b border-neutral-800/60 last:border-0">
      <div className="flex items-center justify-between mb-1">
        <span className="text-xs text-neutral-500 font-medium">{label}</span>
        <button
          onClick={() => copy(value, copyKey)}
          className="text-neutral-500 hover:text-neutral-300 p-1 rounded transition-colors"
          title="Copy"
        >
          {copied === copyKey
            ? <span className="text-green-400 text-xs font-medium">Copied!</span>
            : <IconCopy />
          }
        </button>
      </div>
      <span className="text-sm text-neutral-200 break-all">{value}</span>
    </div>
  )
}

// ── Entry list item ───────────────────────────────────────────────────────────
function EntryTile({ entry, onSelect }) {
  const initials = (entry.title || entry.name || '?').slice(0, 2).toUpperCase()
  const domain = entry.url ? (() => { try { return new URL(entry.url).hostname } catch { return '' } })() : ''

  return (
    <button
      onClick={() => onSelect(entry)}
      className="flex items-center gap-3 w-full px-3 py-2.5 rounded-xl hover:bg-white/5 transition-colors text-left group"
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

// ── Main Vault component ──────────────────────────────────────────────────────
export default function Vault() {
  const [locked, setLocked] = useState(true)
  const [entries, setEntries] = useState([])
  const [search, setSearch] = useState('')
  const [view, setView] = useState('list') // 'list' | 'detail' | 'add' | 'edit' | 'generator'
  const [selected, setSelected] = useState(null)
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
        const data = await res.json()
        setEntries(data.entries || data || [])
      }
    } catch { /* ignore */ }
    setLoadingEntries(false)
  }, [])

  const handleUnlock = useCallback(() => {
    setLocked(false)
    loadEntries()
  }, [loadEntries])

  const handleSaveEntry = useCallback((_savedEntry) => { // eslint-disable-line no-unused-vars
    loadEntries()
    setView('list')
    setSelected(null)
  }, [loadEntries])

  const handleDeleteEntry = useCallback((id) => {
    setEntries(prev => prev.filter(e => e.id !== id))
    setView('list')
    setSelected(null)
  }, [])

  const handleSelectEntry = useCallback((entry) => {
    setSelected(entry)
    setView('detail')
  }, [])

  const handleEditEntry = useCallback((entry) => {
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
            <div className="relative flex-1">
              <div className="absolute left-3 top-1/2 -translate-y-1/2 text-neutral-600 pointer-events-none">
                <IconSearch />
              </div>
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search vault…"
                className="w-full bg-neutral-800/60 border border-neutral-700/50 rounded-lg pl-9 pr-3 py-1.5 text-sm text-white outline-none placeholder:text-neutral-600 focus:border-neutral-500/70 transition-colors"
              />
              {search && (
                <button
                  onClick={() => setSearch('')}
                  className="absolute right-2.5 top-1/2 -translate-y-1/2 text-neutral-600 hover:text-neutral-400 text-base"
                >
                  ×
                </button>
              )}
            </div>

            {/* Generator shortcut */}
            <button
              onClick={() => setView('generator')}
              className="p-2 rounded-lg text-neutral-500 hover:text-neutral-200 hover:bg-white/5 transition-colors"
              title="Password Generator"
            >
              <IconWand />
            </button>

            {/* Add */}
            <button
              onClick={() => setView('add')}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-xs font-medium transition-colors"
            >
              <IconPlus />
              Add
            </button>

            {/* Lock */}
            <button
              onClick={lockVault}
              className="p-2 rounded-lg text-neutral-500 hover:text-neutral-200 hover:bg-white/5 transition-colors"
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
              <div className="flex flex-col items-center justify-center py-16 gap-3">
                {entries.length === 0 ? (
                  <>
                    <div className="w-12 h-12 rounded-2xl bg-neutral-800 flex items-center justify-center text-neutral-600">
                      <IconKey />
                    </div>
                    <div className="text-center">
                      <p className="text-neutral-500 text-sm font-medium">Vault is empty</p>
                      <p className="text-neutral-600 text-xs mt-1">Add your first credential to get started</p>
                    </div>
                    <button
                      onClick={() => setView('add')}
                      className="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-sm font-medium transition-colors"
                    >
                      <IconPlus />
                      Add credential
                    </button>
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

      {/* Standalone generator view */}
      {view === 'generator' && (
        <div className="flex flex-col h-full">
          <div className="flex items-center gap-3 px-4 py-3 border-b border-neutral-800 shrink-0">
            <button onClick={handleBack} className="text-neutral-500 hover:text-neutral-200 transition-colors">
              <IconBack />
            </button>
            <h3 className="text-sm font-semibold text-white">Password Generator</h3>
          </div>
          <div className="p-4">
            <GeneratorPanel />
          </div>
        </div>
      )}
    </div>
  )
}
