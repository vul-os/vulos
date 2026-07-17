/**
 * console/pages/Developer.jsx — /developer
 *
 * The developer console: issue + manage API keys, register outbound webhooks, and
 * (once the store lands) MCP servers. Wired to management's cproutes:
 *   GET/POST    /api/developer/keys            — list / issue (secret shown once)
 *   DELETE      /api/developer/keys/{id}        — revoke
 *   GET/POST    /api/developer/webhooks         — list / create
 *   DELETE      /api/developer/webhooks/{id}    — delete
 *   POST        /api/developer/webhooks/{id}/test
 *   GET         /api/developer/mcp-servers      — list (stub: empty until DEVCON-MCP-01)
 *
 * All endpoints are session-authed and account-scoped server-side (no id is sent).
 */

import { useState, useEffect, useCallback } from 'react'
import { Section, Card, Pill, Button } from '../../ui/index.jsx'

const KNOWN_SCOPES = ['documents.read', 'documents.write', 'drive.read', 'drive.write', 'relay.send']
const KNOWN_PRODUCTS = ['office', 'relay', 'drive', 'board', 'files']

async function apiJSON(url, init) {
  const res = await fetch(url, {
    credentials: 'include',
    headers: { Accept: 'application/json', ...(init?.body ? { 'Content-Type': 'application/json' } : {}) },
    ...init,
  })
  if (res.status === 204) return null
  const text = await res.text()
  const body = text ? JSON.parse(text) : null
  if (!res.ok) {
    const err = new Error((body && (body.error || body.message)) || `HTTP ${res.status}`)
    err.status = res.status
    throw err
  }
  return body
}

/* ── API keys ────────────────────────────────────────────────────────────── */
function ApiKeys() {
  const [keys, setKeys] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState([])
  const [products, setProducts] = useState([])
  const [issuing, setIssuing] = useState(false)
  const [newSecret, setNewSecret] = useState(null)
  const [copied, setCopied] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    setError(null)
    apiJSON('/api/developer/keys')
      .then((j) => { setKeys(Array.isArray(j) ? j : (j?.keys ?? [])); setLoading(false) })
      .catch((e) => { setError(e.message); setLoading(false) })
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { load() }, [load])

  function toggle(list, setList, v) {
    setList(list.includes(v) ? list.filter((x) => x !== v) : [...list, v])
  }

  async function issue(e) {
    e.preventDefault()
    if (!name.trim() || issuing) return
    setIssuing(true)
    setError(null)
    try {
      const body = await apiJSON('/api/developer/keys', {
        method: 'POST',
        body: JSON.stringify({ name: name.trim(), scopes, products }),
      })
      setNewSecret(body?.key || null)
      setName(''); setScopes([]); setProducts([])
      load()
    } catch (err) {
      setError(err.message)
    } finally {
      setIssuing(false)
    }
  }

  async function revoke(id) {
    if (!window.confirm('Revoke this key? Applications using it will stop working immediately.')) return
    try {
      await apiJSON(`/api/developer/keys/${encodeURIComponent(id)}`, { method: 'DELETE' })
      load()
    } catch (err) {
      setError(err.message)
    }
  }

  function copySecret() {
    if (!newSecret) return
    navigator.clipboard?.writeText(newSecret).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }

  return (
    <Card hover={false}>
      <div className="dev-card-head">
        <div>
          <h3 className="dev-title">API keys</h3>
          <p className="dev-subtitle">Programmatic access to your Vulos services. Each key carries only the scopes you grant.</p>
        </div>
      </div>

      {newSecret && (
        <div className="dev-secret" role="status">
          <div className="dev-secret-label">New key — copy it now, it won&apos;t be shown again.</div>
          <div className="dev-secret-row">
            <code className="dev-secret-code">{newSecret}</code>
            <Button size="sm" variant="ghost" onClick={copySecret}>{copied ? 'Copied' : 'Copy'}</Button>
            <Button size="sm" variant="ghost" onClick={() => setNewSecret(null)}>Done</Button>
          </div>
        </div>
      )}

      <form className="dev-form" onSubmit={issue}>
        <label className="dev-field-label" htmlFor="dev-key-name">Key name</label>
        <input
          id="dev-key-name"
          className="dev-input"
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="e.g. CI deploy key"
          autoComplete="off"
        />
        <div className="dev-chip-group">
          <span className="dev-chip-label">Scopes</span>
          {KNOWN_SCOPES.map((s) => (
            <button type="button" key={s} className={`dev-chip${scopes.includes(s) ? ' on' : ''}`} onClick={() => toggle(scopes, setScopes, s)}>
              {s}
            </button>
          ))}
        </div>
        <div className="dev-chip-group">
          <span className="dev-chip-label">Products</span>
          {KNOWN_PRODUCTS.map((p) => (
            <button type="button" key={p} className={`dev-chip${products.includes(p) ? ' on' : ''}`} onClick={() => toggle(products, setProducts, p)}>
              {p}
            </button>
          ))}
        </div>
        <Button type="submit" size="sm" disabled={!name.trim() || issuing}>
          {issuing ? 'Issuing…' : 'Issue key'}
        </Button>
      </form>

      {error && <div className="dev-error" role="alert">{error}</div>}

      <div className="dev-list">
        {loading && <div className="dev-muted">Loading keys…</div>}
        {!loading && keys.length === 0 && <div className="dev-muted">No keys yet.</div>}
        {!loading && keys.map((k) => (
          <div className="dev-row" key={k.id}>
            <div className="dev-row-main">
              <span className="dev-row-name">{k.name || 'Unnamed key'}</span>
              <span className="dev-row-meta">
                {(k.scopes || []).length ? (k.scopes || []).join(' · ') : 'no scopes'}
                {(k.products || []).length ? ` — ${(k.products || []).join(', ')}` : ''}
              </span>
            </div>
            <div className="dev-row-right">
              {k.created_at && <span className="dev-row-when">{new Date(k.created_at).toLocaleDateString()}</span>}
              <Button size="sm" variant="ghost" onClick={() => revoke(k.id)}>Revoke</Button>
            </div>
          </div>
        ))}
      </div>
    </Card>
  )
}

/* ── Webhooks ────────────────────────────────────────────────────────────── */
function Webhooks() {
  const [hooks, setHooks] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [url, setUrl] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    apiJSON('/api/developer/webhooks')
      .then((j) => { setHooks(Array.isArray(j) ? j : (j?.webhooks ?? [])); setLoading(false) })
      .catch((e) => { setError(e.message); setLoading(false) })
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { load() }, [load])

  async function create(e) {
    e.preventDefault()
    if (!url.trim() || busy) return
    setBusy(true); setError(null)
    try {
      await apiJSON('/api/developer/webhooks', { method: 'POST', body: JSON.stringify({ url: url.trim() }) })
      setUrl(''); load()
    } catch (err) { setError(err.message) } finally { setBusy(false) }
  }

  async function remove(id) {
    try { await apiJSON(`/api/developer/webhooks/${encodeURIComponent(id)}`, { method: 'DELETE' }); load() }
    catch (err) { setError(err.message) }
  }

  return (
    <Card hover={false}>
      <div className="dev-card-head">
        <div>
          <h3 className="dev-title">Webhooks</h3>
          <p className="dev-subtitle">Receive server-to-server event callbacks at an HTTPS endpoint you control.</p>
        </div>
      </div>
      <form className="dev-form dev-form-inline" onSubmit={create}>
        <input className="dev-input" type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://example.com/hooks/vulos" autoComplete="off" />
        <Button type="submit" size="sm" disabled={!url.trim() || busy}>{busy ? 'Adding…' : 'Add'}</Button>
      </form>
      {error && <div className="dev-error" role="alert">{error}</div>}
      <div className="dev-list">
        {loading && <div className="dev-muted">Loading webhooks…</div>}
        {!loading && hooks.length === 0 && <div className="dev-muted">No webhooks registered.</div>}
        {!loading && hooks.map((h) => (
          <div className="dev-row" key={h.id}>
            <div className="dev-row-main">
              <span className="dev-row-name" style={{ wordBreak: 'break-all' }}>{h.url}</span>
              {h.state && <span className="dev-row-meta">{h.state}</span>}
            </div>
            <div className="dev-row-right">
              <Button size="sm" variant="ghost" onClick={() => remove(h.id)}>Delete</Button>
            </div>
          </div>
        ))}
      </div>
    </Card>
  )
}

/* ── MCP servers (stub endpoint — renders empty until DEVCON-MCP-01) ─────── */
function McpServers() {
  const [servers, setServers] = useState([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    apiJSON('/api/developer/mcp-servers')
      // eslint-disable-next-line react-hooks/set-state-in-effect
      .then((j) => { if (!cancelled) { setServers(Array.isArray(j) ? j : (j?.servers ?? [])); setLoading(false) } })
      .catch(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [])

  return (
    <Card hover={false}>
      <div className="dev-card-head">
        <div>
          <h3 className="dev-title">MCP servers</h3>
          <p className="dev-subtitle">Register Model Context Protocol servers for your sovereign AI gateway.</p>
        </div>
        <Pill color="faint">soon</Pill>
      </div>
      <div className="dev-list">
        {loading && <div className="dev-muted">Loading…</div>}
        {!loading && servers.length === 0 && (
          <div className="dev-muted">No MCP servers registered. Registration lands in a later release.</div>
        )}
        {!loading && servers.map((s) => (
          <div className="dev-row" key={s.id}>
            <span className="dev-row-name">{s.name || s.url}</span>
          </div>
        ))}
      </div>
    </Card>
  )
}

export default function Developer() {
  return (
    <Section slim>
      <style>{STYLES}</style>
      <div className="dev-header">
        <h1 className="dev-page-title">Developer</h1>
        <p className="dev-page-sub">Keys, webhooks and integrations for building on your Vulos control plane.</p>
      </div>
      <div className="dev-grid">
        <ApiKeys />
        <Webhooks />
        <McpServers />
      </div>
    </Section>
  )
}

const STYLES = `
  .dev-header { margin-bottom: var(--sp-4); }
  .dev-page-title { font-family: var(--font-mono); font-size: clamp(1.125rem, 2.2vw, 1.375rem); font-weight: 700; letter-spacing: -0.025em; margin: 0; }
  .dev-page-sub { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); margin-top: var(--sp-0-5); }
  .dev-grid { display: grid; gap: var(--sp-3); }
  .dev-card-head { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-2); margin-bottom: var(--sp-2-5); }
  .dev-title { font-size: var(--text-lg, 1rem); font-weight: 600; margin: 0; color: var(--text-primary); }
  .dev-subtitle { font-size: var(--text-sm); color: var(--text-faint); margin-top: 2px; }
  .dev-form { display: flex; flex-direction: column; gap: var(--sp-1-5); margin-bottom: var(--sp-2-5); }
  .dev-form-inline { flex-direction: row; align-items: center; flex-wrap: wrap; }
  .dev-field-label, .dev-chip-label { font-family: var(--font-mono); font-size: var(--text-xs); letter-spacing: 0.06em; text-transform: uppercase; color: var(--text-ghost); }
  .dev-input {
    font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-primary);
    background: var(--bg-elevated); border: 1px solid var(--border-strong); border-radius: var(--radius-sm);
    padding: 9px 12px; min-height: 40px; flex: 1; min-width: 220px;
  }
  .dev-input:focus-visible { outline: none; box-shadow: var(--focus-ring); border-color: var(--accent); }
  .dev-chip-group { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
  .dev-chip {
    font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-secondary);
    background: transparent; border: 1px solid var(--border-strong); border-radius: 99px;
    padding: 5px 11px; cursor: pointer; transition: all 120ms var(--ease);
  }
  .dev-chip:hover { border-color: var(--border-emphasis); color: var(--text-primary); }
  .dev-chip.on { background: color-mix(in srgb, var(--accent) 12%, transparent); border-color: color-mix(in srgb, var(--accent) 40%, transparent); color: var(--accent); }
  .dev-secret { border: 1px solid color-mix(in srgb, var(--good) 30%, var(--border-strong)); background: color-mix(in srgb, var(--good) 5%, transparent); border-radius: var(--radius-lg); padding: var(--sp-2) var(--sp-2-5); margin-bottom: var(--sp-2-5); }
  .dev-secret-label { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--good); margin-bottom: 6px; }
  .dev-secret-row { display: flex; align-items: center; gap: var(--sp-1-5); flex-wrap: wrap; }
  .dev-secret-code { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-primary); background: var(--bg-base); padding: 6px 10px; border-radius: var(--radius-sm); word-break: break-all; flex: 1; min-width: 200px; }
  .dev-list { display: flex; flex-direction: column; gap: 2px; margin-top: var(--sp-1); }
  .dev-row { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-2); padding: var(--sp-1-5) var(--sp-1); border-top: 1px solid var(--border-subtle); min-height: 48px; }
  .dev-row-main { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .dev-row-name { font-family: var(--font-mono); font-size: var(--text-sm); font-weight: 600; color: var(--text-primary); }
  .dev-row-meta { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-ghost); }
  .dev-row-right { display: flex; align-items: center; gap: var(--sp-1-5); flex-shrink: 0; }
  .dev-row-when { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-faint); }
  .dev-muted { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); padding: var(--sp-2) 0; }
  .dev-error { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--danger); margin-bottom: var(--sp-2); }
`
