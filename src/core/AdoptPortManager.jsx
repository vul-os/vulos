/**
 * AdoptPortManager — ADOPT-A-PORT
 * Self-contained popover for adopting an already-running LOCAL (loopback) service
 * so it becomes a first-class OS app, reachable through the :8080 auth gateway
 * (never the public-web path). Opens via window CustomEvent `vulos:open-adopt-port`.
 *
 * Backend seam (all session-authed, owner-scoped):
 *   GET    /api/proxyadopt        — list the caller's adopted upstreams (+ health)
 *   POST   /api/proxyadopt        — adopt {name, port, requires_product?}
 *   DELETE /api/proxyadopt/{id}   — revoke
 *
 * All identifiers are APM- / AdoptPort- prefixed to avoid collisions.
 */
import { useState, useEffect, useCallback, useRef } from 'react'

const APM_EVENT = 'vulos:open-adopt-port'

// ── API helpers ───────────────────────────────────────────────────────────────

async function apmList() {
  const res = await fetch('/api/proxyadopt')
  if (!res.ok) throw new Error('list failed')
  return res.json() // [{id, name, port, url, healthy, requires_product}]
}

async function apmAdopt({ name, port, requiresProduct }) {
  const res = await fetch('/api/proxyadopt', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      name,
      port: Number(port),
      requires_product: requiresProduct || undefined,
    }),
  })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(body?.error || 'adopt failed')
  return body
}

async function apmRevoke(id) {
  const res = await fetch(`/api/proxyadopt/${encodeURIComponent(id)}`, { method: 'DELETE' })
  if (!res.ok) throw new Error('revoke failed')
  return res.json()
}

// ── Adopt form ─────────────────────────────────────────────────────────────────

function ApmAdoptForm({ onAdopted }) {
  const [name, setName] = useState('')
  const [port, setPort] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState(null)

  const submit = useCallback(async (e) => {
    e.preventDefault()
    setErr(null)
    if (!name.trim() || !port) { setErr('Name and port are required.'); return }
    setBusy(true)
    try {
      await apmAdopt({ name: name.trim(), port })
      setName(''); setPort('')
      onAdopted()
    } catch (ex) {
      setErr(ex.message || 'Could not adopt port.')
    } finally { setBusy(false) }
  }, [name, port, onAdopted])

  return (
    <form onSubmit={submit} className="flex flex-col gap-2 py-2" aria-label="Adopt a local port">
      <div className="flex gap-2">
        <input
          value={name}
          onChange={e => setName(e.target.value)}
          placeholder="App name (e.g. Grafana)"
          aria-label="App name"
          className="flex-1 min-w-0 text-xs bg-[var(--bg-elevated)] border border-[var(--border-strong)] rounded-lg px-2 py-1.5 text-[var(--text-primary)] focus:outline-none focus:border-[var(--accent)]"
        />
        <input
          value={port}
          onChange={e => setPort(e.target.value.replace(/[^0-9]/g, ''))}
          placeholder="Port"
          inputMode="numeric"
          aria-label="Local port"
          className="w-20 text-xs bg-[var(--bg-elevated)] border border-[var(--border-strong)] rounded-lg px-2 py-1.5 text-[var(--text-primary)] focus:outline-none focus:border-[var(--accent)]"
        />
        <button
          type="submit"
          disabled={busy}
          className="text-xs px-3 py-1.5 rounded-lg font-medium bg-[var(--accent)] hover:bg-[var(--accent)] text-white transition-colors disabled:opacity-40"
        >
          Adopt
        </button>
      </div>
      {err && <p className="text-[11px] text-[var(--status-danger)]">{err}</p>}
      <p className="text-[10px] text-[var(--text-faint)]">
        The service must be listening on 127.0.0.1. It stays behind your sign-in — it is never exposed to the public web.
      </p>
    </form>
  )
}

// ── Row ─────────────────────────────────────────────────────────────────────────

function ApmRow({ item, onRevoke }) {
  const [busy, setBusy] = useState(false)
  const revoke = useCallback(async () => {
    setBusy(true)
    try { await apmRevoke(item.id); onRevoke() }
    catch { /* list will refresh */ }
    finally { setBusy(false) }
  }, [item.id, onRevoke])

  return (
    <div className="flex items-center justify-between py-2.5 border-b border-[var(--border-default)] last:border-0 gap-3">
      <div className="flex items-center gap-2 min-w-0">
        <span
          className={`shrink-0 w-2 h-2 rounded-full ${item.healthy ? 'bg-[var(--status-success)]' : 'bg-[var(--bg-active)]'}`}
          title={item.healthy ? 'Reachable' : 'Not responding'}
          aria-label={item.healthy ? 'healthy' : 'unhealthy'}
        />
        <span className="text-sm text-[var(--text-primary)] truncate">{item.name || item.id}</span>
        <span className="text-[10px] text-[var(--text-muted)] shrink-0">:{item.port}</span>
      </div>
      <div className="flex items-center gap-1.5 shrink-0">
        <a
          href={item.url}
          className="text-[11px] px-2 py-1 rounded-lg bg-[var(--bg-elevated)] text-[var(--accent)] hover:bg-[var(--bg-hover)] hover:text-[var(--accent)] transition-colors"
        >
          Open
        </a>
        <button
          onClick={revoke}
          disabled={busy}
          className="text-[11px] px-2 py-1 rounded-lg bg-[var(--bg-elevated)] text-[var(--text-tertiary)] hover:bg-[var(--status-danger-soft)] hover:text-[var(--status-danger)] transition-colors disabled:opacity-40"
        >
          Revoke
        </button>
      </div>
    </div>
  )
}

// ── Popover ─────────────────────────────────────────────────────────────────────

function ApmPopover({ onClose }) {
  const [items, setItems] = useState(null)
  const [error, setError] = useState(null)
  const panelRef = useRef(null)

  const load = useCallback(() => {
    setError(null)
    apmList()
      .then(data => setItems(data || []))
      .catch(() => { setError('Could not load adopted ports.'); setItems([]) })
  }, [])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    load()
  }, [load])

  useEffect(() => {
    const handler = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  useEffect(() => {
    const handler = (e) => {
      if (panelRef.current && !panelRef.current.contains(e.target)) onClose()
    }
    const tid = setTimeout(() => document.addEventListener('mousedown', handler), 80)
    return () => { clearTimeout(tid); document.removeEventListener('mousedown', handler) }
  }, [onClose])

  return (
    <div className="fixed inset-0 z-[150] flex items-start justify-end pt-10 pr-4">
      <div
        ref={panelRef}
        role="dialog"
        aria-label="Adopt a local port"
        className="w-[420px] max-w-[calc(100vw-2rem)] max-h-[70vh] flex flex-col rounded-2xl bg-[var(--glass-bg)] backdrop-blur-2xl border border-[var(--border-strong)] shadow-2xl shadow-black/60 animate-[fadeIn_0.15s_ease-out] overflow-hidden"
      >
        <div className="flex items-center justify-between px-4 py-3 border-b border-[var(--border-default)] shrink-0">
          <div>
            <h2 className="text-sm font-semibold text-[var(--text-primary)]">Adopt a Local Port</h2>
            <p className="text-[11px] text-[var(--text-muted)] mt-0.5">Front a loopback service through the OS gateway</p>
          </div>
          <button
            onClick={onClose}
            className="w-6 h-6 flex items-center justify-center rounded-lg text-[var(--text-muted)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-elevated)] transition-colors text-sm"
          >
            {'×'}
          </button>
        </div>

        <div className="overflow-y-auto flex-1 px-4 py-2">
          <ApmAdoptForm onAdopted={load} />
          <div className="border-t border-[var(--border-default)] mt-1 pt-1">
            {items === null && (
              <div className="flex items-center gap-2 py-6 justify-center text-[var(--text-faint)] text-xs">
                <span className="w-3 h-3 spinner" /> Loading...
              </div>
            )}
            {error && <p className="text-xs text-[var(--status-danger)] py-4 text-center">{error}</p>}
            {items !== null && items.length === 0 && !error && (
              <p className="text-xs text-[var(--text-muted)] py-6 text-center">No adopted ports yet.</p>
            )}
            {items && items.map(item => (
              <ApmRow key={item.id} item={item} onRevoke={load} />
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

// ── Exported component ────────────────────────────────────────────────────────

export default function AdoptPortManager() {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    const handler = () => setOpen(true)
    window.addEventListener(APM_EVENT, handler)
    return () => window.removeEventListener(APM_EVENT, handler)
  }, [])
  if (!open) return null
  return <ApmPopover onClose={() => setOpen(false)} />
}
