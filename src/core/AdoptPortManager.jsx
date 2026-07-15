/**
 * AdoptPortManager — ADOPT-A-PORT
 * Self-contained popover for adopting an already-running LOCAL (loopback) service
 * so it becomes a first-class OS app, reachable through the :8080 auth gateway
 * (never the public-web path). Opens via window CustomEvent `vulos:open-adopt-port`.
 *
 * Backend seam (all session-authed, owner-scoped):
 *   GET    /api/apps/proxy        — list the caller's adopted upstreams (+ health)
 *   POST   /api/apps/proxy        — adopt {name, port, requires_product?}
 *   DELETE /api/apps/proxy/{id}   — revoke
 *
 * All identifiers are APM- / AdoptPort- prefixed to avoid collisions.
 */
import { useState, useEffect, useCallback, useRef } from 'react'

const APM_EVENT = 'vulos:open-adopt-port'

// ── API helpers ───────────────────────────────────────────────────────────────

async function apmList() {
  const res = await fetch('/api/apps/proxy')
  if (!res.ok) throw new Error('list failed')
  return res.json() // [{id, name, port, url, healthy, requires_product}]
}

async function apmAdopt({ name, port, requiresProduct }) {
  const res = await fetch('/api/apps/proxy', {
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
  const res = await fetch(`/api/apps/proxy/${encodeURIComponent(id)}`, { method: 'DELETE' })
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
          className="flex-1 min-w-0 text-xs bg-neutral-800 border border-neutral-700/50 rounded-lg px-2 py-1.5 text-neutral-200 focus:outline-none focus:border-blue-500/50"
        />
        <input
          value={port}
          onChange={e => setPort(e.target.value.replace(/[^0-9]/g, ''))}
          placeholder="Port"
          inputMode="numeric"
          aria-label="Local port"
          className="w-20 text-xs bg-neutral-800 border border-neutral-700/50 rounded-lg px-2 py-1.5 text-neutral-200 focus:outline-none focus:border-blue-500/50"
        />
        <button
          type="submit"
          disabled={busy}
          className="text-xs px-3 py-1.5 rounded-lg font-medium bg-blue-700/80 hover:bg-blue-600/80 text-white transition-colors disabled:opacity-40"
        >
          Adopt
        </button>
      </div>
      {err && <p className="text-[11px] text-red-400">{err}</p>}
      <p className="text-[10px] text-neutral-600">
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
    <div className="flex items-center justify-between py-2.5 border-b border-neutral-800/40 last:border-0 gap-3">
      <div className="flex items-center gap-2 min-w-0">
        <span
          className={`shrink-0 w-2 h-2 rounded-full ${item.healthy ? 'bg-green-500' : 'bg-neutral-600'}`}
          title={item.healthy ? 'Reachable' : 'Not responding'}
          aria-label={item.healthy ? 'healthy' : 'unhealthy'}
        />
        <span className="text-sm text-neutral-200 truncate">{item.name || item.id}</span>
        <span className="text-[10px] text-neutral-500 shrink-0">:{item.port}</span>
      </div>
      <div className="flex items-center gap-1.5 shrink-0">
        <a
          href={item.url}
          className="text-[11px] px-2 py-1 rounded-lg bg-neutral-800 text-blue-400 hover:bg-neutral-700 hover:text-blue-300 transition-colors"
        >
          Open
        </a>
        <button
          onClick={revoke}
          disabled={busy}
          className="text-[11px] px-2 py-1 rounded-lg bg-neutral-800 text-neutral-400 hover:bg-red-900/40 hover:text-red-300 transition-colors disabled:opacity-40"
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
        className="w-[420px] max-h-[70vh] flex flex-col rounded-2xl bg-neutral-900/97 backdrop-blur-2xl border border-neutral-700/50 shadow-2xl shadow-black/60 animate-[fadeIn_0.15s_ease-out] overflow-hidden"
      >
        <div className="flex items-center justify-between px-4 py-3 border-b border-neutral-800/60 shrink-0">
          <div>
            <h2 className="text-sm font-semibold text-neutral-100">Adopt a Local Port</h2>
            <p className="text-[11px] text-neutral-500 mt-0.5">Front a loopback service through the OS gateway</p>
          </div>
          <button
            onClick={onClose}
            className="w-6 h-6 flex items-center justify-center rounded-lg text-neutral-500 hover:text-neutral-200 hover:bg-neutral-800 transition-colors text-sm"
          >
            {'×'}
          </button>
        </div>

        <div className="overflow-y-auto flex-1 px-4 py-2">
          <ApmAdoptForm onAdopted={load} />
          <div className="border-t border-neutral-800/60 mt-1 pt-1">
            {items === null && (
              <div className="flex items-center gap-2 py-6 justify-center text-neutral-600 text-xs">
                <span className="w-3 h-3 spinner" /> Loading...
              </div>
            )}
            {error && <p className="text-xs text-red-400 py-4 text-center">{error}</p>}
            {items !== null && items.length === 0 && !error && (
              <p className="text-xs text-neutral-500 py-6 text-center">No adopted ports yet.</p>
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
