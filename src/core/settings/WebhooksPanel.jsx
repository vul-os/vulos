import { useState, useEffect, useCallback } from 'react'
import { requireStepUp } from '../../lib/stepup'

// ---------------------------------------------------------------------------
// WebhooksPanel — Settings -> Developer -> Webhooks (box-owner only).
//
// Outbound webhook subscriptions: point a URL at one or more box events and
// Vulos will POST a signed (X-Vulos-Signature: HMAC-SHA256) JSON payload to
// it whenever that event fires. Every subscription URL is screened server
// side by an SSRF guard (loopback/RFC1918/CGNAT/link-local/metadata/IPv6 ULA
// are all rejected) both when you save it AND again at delivery time, so this
// panel never needs to duplicate that logic — it just surfaces the backend's
// verdict.
//
// Endpoints (backend/cmd/server/routes_webhooks.go):
//   GET    /api/webhooks/topics
//   GET    /api/webhooks
//   POST   /api/webhooks                    (step-up)
//   PATCH  /api/webhooks/{id}                (step-up)
//   DELETE /api/webhooks/{id}
//   POST   /api/webhooks/{id}/rotate-secret
//   POST   /api/webhooks/{id}/test
//   GET    /api/webhooks/{id}/deliveries
//
// Creating and editing a subscription require requireStepUp() first: a
// webhook is an exfiltration vector (every event payload gets POSTed to
// whatever URL is configured), so pointing it at a new/different URL needs a
// freshly-elevated session — same bar as other sensitive owner actions. The
// backend independently re-checks stepup.Require(r); this is UX, not the
// security boundary.
// ---------------------------------------------------------------------------

function Section({ title, desc, children }) {
  return (
    <div>
      <header className="mb-5 pb-4 border-b border-[var(--border-default)]">
        <h2 className="text-xl font-semibold tracking-tight text-[var(--text-primary)]">{title}</h2>
        {desc && <p className="mt-1 text-sm text-[var(--text-tertiary)] leading-relaxed">{desc}</p>}
      </header>
      {children}
    </div>
  )
}

async function jsonFetch(url, opts) {
  const r = await fetch(url, { credentials: 'same-origin', ...opts })
  const d = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`)
  return d
}

const STATUS_TONE = {
  delivered: 'bg-[var(--status-success)]',
  pending: 'bg-[var(--status-warning)]',
  failed: 'bg-[var(--status-warning)]',
  dead: 'bg-[var(--status-danger)]',
}

function StatusDot({ status }) {
  return <span className={`inline-block w-2 h-2 rounded-full ${STATUS_TONE[status] || 'bg-[var(--text-faint)]'}`} aria-hidden="true" />
}

// SecretReveal — a one-time, copy-once box for a freshly issued/rotated secret.
// The backend never returns the secret again after this response, so this is
// the owner's only chance to grab it.
function SecretReveal({ label, secret, onDismiss }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard?.writeText(secret).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }
  return (
    <div className="rounded-xl border border-warning-soft bg-warning-soft px-4 py-3 mb-4 space-y-2">
      <p className="text-xs text-warning font-medium">{label} — shown once, copy it now:</p>
      <div className="flex items-center gap-2">
        <code className="flex-1 min-w-0 text-xs font-mono break-all text-[var(--text-primary)] bg-[var(--bg-surface)] rounded-lg px-3 py-2">
          {secret}
        </code>
        <button onClick={copy} className="btn-secondary text-xs px-3 whitespace-nowrap">{copied ? 'Copied' : 'Copy'}</button>
      </div>
      <button onClick={onDismiss} className="text-xs text-[var(--text-muted)] hover:text-[var(--text-secondary)]">Done</button>
    </div>
  )
}

function TopicPicker({ allTopics, selected, onChange }) {
  const toggle = (t) => {
    onChange(selected.includes(t) ? selected.filter(x => x !== t) : [...selected, t])
  }
  return (
    <div className="flex flex-wrap gap-2">
      {allTopics.map(t => (
        <label
          key={t}
          className={`text-xs font-mono px-2.5 py-1.5 rounded-full border cursor-pointer transition-colors ${
            selected.includes(t)
              ? 'accent-border bg-[var(--accent-soft)] text-[var(--text-primary)]'
              : 'border-[var(--border-default)] text-[var(--text-tertiary)] hover:bg-[var(--bg-surface)]'
          }`}
        >
          <input type="checkbox" checked={selected.includes(t)} onChange={() => toggle(t)} className="sr-only" />
          {t}
        </label>
      ))}
    </div>
  )
}

// DeliveryLog — lazy-loaded delivery history for one subscription.
function DeliveryLog({ subId }) {
  const [deliveries, setDeliveries] = useState(null)
  const [error, setError] = useState('')

  const load = useCallback(() => {
    jsonFetch(`/api/webhooks/${encodeURIComponent(subId)}/deliveries?limit=25`)
      .then(d => setDeliveries(d.deliveries || []))
      .catch(e => setError(e.message))
  }, [subId])

  useEffect(() => { load() }, [load])

  if (error) return <p className="text-xs text-danger px-4 py-3">{error}</p>
  if (deliveries === null) return <p className="text-xs text-[var(--text-faint)] px-4 py-3">Loading delivery log…</p>
  if (deliveries.length === 0) return <p className="text-xs text-[var(--text-faint)] px-4 py-3">No deliveries yet.</p>

  return (
    <div className="divide-y divide-[var(--border-default)]">
      {deliveries.map(d => (
        <div key={d.id} className="flex items-center justify-between px-4 py-2 text-xs">
          <div className="flex items-center gap-2 min-w-0">
            <StatusDot status={d.status} />
            <span className="font-mono text-[var(--text-secondary)] truncate">{d.topic}</span>
          </div>
          <div className="flex items-center gap-3 text-[var(--text-faint)] shrink-0">
            {d.last_status_code != null && <span>HTTP {d.last_status_code}</span>}
            <span>attempt {d.attempt_count}</span>
            <span>{d.status}</span>
            <span>{new Date(d.created_at).toLocaleString()}</span>
          </div>
        </div>
      ))}
    </div>
  )
}

// SubscriptionRow — one webhook subscription: view/edit/rotate/delete/test.
function SubscriptionRow({ sub, allTopics, onChanged, onSecret }) {
  const [editing, setEditing] = useState(false)
  const [url, setUrl] = useState(sub.url)
  const [topics, setTopics] = useState(sub.topics)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [expanded, setExpanded] = useState(false)
  const [testResult, setTestResult] = useState(null)

  const startEdit = () => {
    setUrl(sub.url)
    setTopics(sub.topics)
    setError('')
    setEditing(true)
  }

  const save = async () => {
    setBusy(true)
    setError('')
    try {
      await requireStepUp()
      await jsonFetch(`/api/webhooks/${encodeURIComponent(sub.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url, topics }),
      })
      setEditing(false)
      onChanged()
    } catch (e) {
      if (e?.code !== 'CANCELLED') setError(e.message || 'Could not save changes.')
    } finally {
      setBusy(false)
    }
  }

  const toggleActive = async () => {
    setBusy(true)
    setError('')
    try {
      await requireStepUp()
      await jsonFetch(`/api/webhooks/${encodeURIComponent(sub.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ active: !sub.active }),
      })
      onChanged()
    } catch (e) {
      if (e?.code !== 'CANCELLED') setError(e.message || 'Could not update.')
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!window.confirm(`Delete this webhook (${sub.url})? This cannot be undone.`)) return
    setBusy(true)
    setError('')
    try {
      await jsonFetch(`/api/webhooks/${encodeURIComponent(sub.id)}`, { method: 'DELETE' })
      onChanged()
    } catch (e) {
      setError(e.message || 'Could not delete.')
    } finally {
      setBusy(false)
    }
  }

  const rotate = async () => {
    if (!window.confirm('Rotate the HMAC secret? Existing signatures your receiver has cached will stop validating.')) return
    setBusy(true)
    setError('')
    try {
      const d = await jsonFetch(`/api/webhooks/${encodeURIComponent(sub.id)}/rotate-secret`, { method: 'POST' })
      onSecret('New secret', d.secret)
    } catch (e) {
      setError(e.message || 'Could not rotate secret.')
    } finally {
      setBusy(false)
    }
  }

  const sendTest = async () => {
    setBusy(true)
    setTestResult(null)
    setError('')
    try {
      const d = await jsonFetch(`/api/webhooks/${encodeURIComponent(sub.id)}/test`, { method: 'POST' })
      setTestResult(d)
    } catch (e) {
      setError(e.message || 'Could not send test.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="rounded-xl border border-[var(--border-default)] overflow-hidden mb-3">
      <div className="px-4 py-3 bg-[var(--bg-surface)]">
        {!editing ? (
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className={`inline-block w-2 h-2 rounded-full ${sub.active ? 'bg-[var(--status-success)]' : 'bg-[var(--text-faint)]'}`} aria-hidden="true" />
                <span className="text-sm font-mono text-[var(--text-primary)] truncate">{sub.url}</span>
              </div>
              <div className="flex flex-wrap gap-1 mt-2">
                {sub.topics.map(t => (
                  <span key={t} className="text-[12px] font-mono px-1.5 py-0.5 rounded bg-[var(--bg-elevated)] text-[var(--text-tertiary)]">{t}</span>
                ))}
              </div>
            </div>
            <div className="flex flex-wrap gap-1.5 shrink-0">
              <button onClick={toggleActive} disabled={busy} className="btn-secondary text-xs px-2.5 py-1 disabled:opacity-40">
                {sub.active ? 'Pause' : 'Resume'}
              </button>
              <button onClick={sendTest} disabled={busy} className="btn-secondary text-xs px-2.5 py-1 disabled:opacity-40">Send test</button>
              <button onClick={startEdit} disabled={busy} className="btn-secondary text-xs px-2.5 py-1 disabled:opacity-40">Edit</button>
              <button onClick={rotate} disabled={busy} className="btn-secondary text-xs px-2.5 py-1 disabled:opacity-40">Rotate secret</button>
              <button onClick={remove} disabled={busy} className="btn-secondary text-xs px-2.5 py-1 text-danger disabled:opacity-40">Delete</button>
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <input
              type="url"
              value={url}
              onChange={e => setUrl(e.target.value)}
              placeholder="https://example.org/webhooks/vulos"
              autoComplete="off"
              spellCheck={false}
              className="input text-sm py-2 font-mono w-full"
            />
            <TopicPicker allTopics={allTopics} selected={topics} onChange={setTopics} />
            <div className="flex gap-2">
              <button onClick={save} disabled={busy || !url || topics.length === 0} className="btn-primary text-xs px-3 py-1.5 disabled:opacity-40">
                {busy ? 'Saving…' : 'Save changes'}
              </button>
              <button onClick={() => setEditing(false)} disabled={busy} className="btn-secondary text-xs px-3 py-1.5">Cancel</button>
            </div>
          </div>
        )}

        {testResult && (
          <p className={`mt-2 text-xs ${testResult.delivered ? 'text-[var(--status-success)]' : 'text-danger'}`}>
            {testResult.delivered
              ? `Test delivered (HTTP ${testResult.status_code}).`
              : `Test failed${testResult.status_code ? ` (HTTP ${testResult.status_code})` : ''}${testResult.error ? `: ${testResult.error}` : '.'}`}
          </p>
        )}
        {error && <p className="mt-2 text-xs text-danger" role="alert">{error}</p>}

        <button
          onClick={() => setExpanded(v => !v)}
          className="mt-2 text-[12px] text-[var(--text-muted)] hover:text-[var(--text-secondary)]"
        >
          {expanded ? 'Hide delivery log' : 'Show delivery log'}
        </button>
      </div>
      {expanded && (
        <div className="border-t border-[var(--border-default)]">
          <DeliveryLog subId={sub.id} />
        </div>
      )}
    </div>
  )
}

export default function WebhooksPanel() {
  const [subs, setSubs] = useState(null)
  const [allTopics, setAllTopics] = useState([])
  const [loadError, setLoadError] = useState('')

  const [newUrl, setNewUrl] = useState('')
  const [newTopics, setNewTopics] = useState([])
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState('')

  const [secretBox, setSecretBox] = useState(null) // { label, secret }

  const load = useCallback(() => {
    setLoadError('')
    Promise.all([
      jsonFetch('/api/webhooks'),
      jsonFetch('/api/webhooks/topics'),
    ])
      .then(([subsRes, topicsRes]) => {
        setSubs(subsRes.subscriptions || [])
        setAllTopics((topicsRes.topics || []).sort())
      })
      .catch(e => setLoadError(e.message || 'Could not load webhooks.'))
  }, [])

  useEffect(() => { load() }, [load])

  const create = async () => {
    setCreating(true)
    setCreateError('')
    try {
      await requireStepUp()
      const d = await jsonFetch('/api/webhooks', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url: newUrl, topics: newTopics }),
      })
      setNewUrl('')
      setNewTopics([])
      setSecretBox({ label: 'Webhook secret', secret: d.secret })
      load()
    } catch (e) {
      if (e?.code !== 'CANCELLED') setCreateError(e.message || 'Could not create webhook.')
    } finally {
      setCreating(false)
    }
  }

  return (
    <Section
      title="Webhooks"
      desc="Send a signed HTTP POST to a URL of your choice whenever a box event fires. Every payload carries an X-Vulos-Signature header (HMAC-SHA256) so your receiver can verify it came from this box. Internal and reserved network addresses (localhost, private ranges, cloud metadata endpoints) can never be used as a webhook target."
    >
      {secretBox && (
        <SecretReveal label={secretBox.label} secret={secretBox.secret} onDismiss={() => setSecretBox(null)} />
      )}

      {loadError && (
        <div className="rounded-xl border border-[var(--border-strong)] bg-[var(--bg-elevated)] text-[var(--text-secondary)] px-4 py-3 mb-5 text-sm" role="alert">
          {loadError}
        </div>
      )}

      {subs === null && !loadError && (
        <p className="text-sm text-[var(--text-faint)]">Loading…</p>
      )}

      {subs !== null && subs.length === 0 && (
        <p className="text-sm text-[var(--text-faint)] mb-5">No webhooks configured yet.</p>
      )}

      {subs !== null && subs.length > 0 && (
        <div className="mb-6">
          {subs.map(sub => (
            <SubscriptionRow key={sub.id} sub={sub} allTopics={allTopics} onChanged={load} onSecret={(label, secret) => setSecretBox({ label, secret })} />
          ))}
        </div>
      )}

      <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] p-4 space-y-3">
        <h3 className="text-sm font-medium text-[var(--text-primary)]">Add a webhook</h3>
        <input
          type="url"
          value={newUrl}
          onChange={e => setNewUrl(e.target.value)}
          placeholder="https://example.org/webhooks/vulos"
          autoComplete="off"
          spellCheck={false}
          className="input text-sm py-2.5 font-mono w-full"
        />
        {allTopics.length > 0 ? (
          <TopicPicker allTopics={allTopics} selected={newTopics} onChange={setNewTopics} />
        ) : (
          <p className="text-xs text-[var(--text-faint)]">Loading topics…</p>
        )}
        {createError && <p className="text-xs text-danger" role="alert">{createError}</p>}
        <button
          onClick={create}
          disabled={creating || !newUrl || newTopics.length === 0}
          className="btn-primary text-sm disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {creating ? 'Creating…' : 'Add webhook'}
        </button>
      </div>
    </Section>
  )
}
