import { useState, useEffect, useCallback, type ReactNode } from 'react'
import { requireStepUp } from '../../lib/stepup'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function errorMessage(err: unknown, fallback: string): string {
  return (isRecord(err) && typeof err.message === 'string' ? err.message : '') || fallback
}

interface DeveloperKey {
  id: string
  name?: string
  key?: string
  created_at?: string
  revoked_at?: string
}

function toDeveloperKey(x: unknown): DeveloperKey | null {
  if (!isRecord(x) || typeof x.id !== 'string') return null
  return {
    id: x.id,
    name: typeof x.name === 'string' ? x.name : undefined,
    key: typeof x.key === 'string' ? x.key : undefined,
    created_at: typeof x.created_at === 'string' ? x.created_at : undefined,
    revoked_at: typeof x.revoked_at === 'string' ? x.revoked_at : undefined,
  }
}

// ---------------------------------------------------------------------------
// PUBLICAPI-01 — Developer settings: manage keys for this box's public REST
// API v1 (/api/v1/…) and surface where to read the docs.
//
// Endpoints (backend/cmd/server/routes_publicapi.go):
//   GET    /api/developer/keys       list this user's keys (metadata only)
//   POST   /api/developer/keys       issue a new key (raw secret shown once) — step-up gated
//   DELETE /api/developer/keys/{id}  revoke a key
//
// Keys are issued locally by this box (backend/internal/apikey's
// LocalStore) and are bearer-authed against /api/v1/… — distinct from your
// browser session and from any cross-product "vk_…" entitlement key.
// ---------------------------------------------------------------------------

interface SectionProps {
  title: ReactNode
  desc?: ReactNode
  children?: ReactNode
}

function Section({ title, desc, children }: SectionProps) {
  return (
    <div>
      <header className="mb-5 pb-4 border-b border-[var(--border-default)]">
        <h2 className="text-xl font-semibold tracking-tight text-[var(--text-primary)]">{title}</h2>
        {desc && <p className="mt-1 text-sm text-[var(--text-tertiary)] leading-relaxed max-w-[68ch]">{desc}</p>}
      </header>
      {children}
    </div>
  )
}

interface InfoRowProps {
  label: ReactNode
  value: ReactNode
  mono?: boolean
}

function InfoRow({ label, value, mono }: InfoRowProps) {
  return (
    <div className="flex items-center justify-between gap-3 px-4 py-2.5 bg-[var(--bg-surface)]">
      <span className="text-xs text-[var(--text-muted)]">{label}</span>
      <span className={`text-sm text-[var(--text-secondary)] truncate ${mono ? 'font-mono' : ''}`}>{value || '—'}</span>
    </div>
  )
}

function formatDate(iso: string | undefined): string {
  if (!iso) return '—'
  try {
    return new Date(iso).toLocaleString()
  } catch {
    return iso
  }
}

export default function DeveloperPanel() {
  const [keys, setKeys] = useState<DeveloperKey[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const [name, setName] = useState('')
  const [issuing, setIssuing] = useState(false)
  const [newKey, setNewKey] = useState<DeveloperKey | null>(null) // { id, name, key, created_at } — shown once
  const [copied, setCopied] = useState(false)
  const [revokingId, setRevokingId] = useState<string | null>(null)

  const load = useCallback(() => {
    setLoading(true)
    setError('')
    fetch('/api/developer/keys')
      .then(r => (r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status))))
      .then((d: unknown) => setKeys(Array.isArray(d) ? d.map(toDeveloperKey).filter((k): k is DeveloperKey => k !== null) : []))
      .catch(e => setError(errorMessage(e, 'failed to load keys')))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])

  const issueKey = async () => {
    setError('')
    try {
      await requireStepUp()
    } catch {
      return // cancelled
    }
    setIssuing(true)
    try {
      const r = await fetch('/api/developer/keys', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: name.trim() || 'API key' }),
      })
      const d: unknown = await r.json().catch(() => ({}))
      if (!r.ok) throw new Error((isRecord(d) && typeof d.error === 'string' ? d.error : '') || 'HTTP ' + r.status)
      setNewKey(toDeveloperKey(d))
      setName('')
      setCopied(false)
      load()
    } catch (e) {
      setError(errorMessage(e, 'failed to issue key'))
    } finally {
      setIssuing(false)
    }
  }

  const revokeKey = async (id: string) => {
    setError('')
    setRevokingId(id)
    try {
      const r = await fetch(`/api/developer/keys/${encodeURIComponent(id)}`, { method: 'DELETE' })
      if (!r.ok && r.status !== 204) {
        const d: unknown = await r.json().catch(() => ({}))
        throw new Error((isRecord(d) && typeof d.error === 'string' ? d.error : '') || 'HTTP ' + r.status)
      }
      load()
    } catch (e) {
      setError(errorMessage(e, 'failed to revoke key'))
    } finally {
      setRevokingId(null)
    }
  }

  const copyKey = async () => {
    if (!newKey?.key) return
    try {
      await navigator.clipboard.writeText(newKey.key)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // clipboard API unavailable — the key is still selectable/visible below
    }
  }

  return (
    <Section
      title="Developer"
      desc="Manage API keys and integrate with this box's public REST API."
    >
      {/* ------------------------------------------------------------------ */}
      {/* API surface info                                                     */}
      {/* ------------------------------------------------------------------ */}
      <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)] mb-6">
        <InfoRow label="Base URL" value={`${typeof window !== 'undefined' ? window.location.origin : ''}/api/v1`} mono />
        <InfoRow label="Rate limit" value="60 requests / minute per key" />
        <InfoRow label="Auth header" value="Authorization: Bearer vkl_…" mono />
      </div>
      <p className="text-xs text-[var(--text-faint)] mb-6 leading-relaxed">
        Available endpoints: <code className="text-[var(--text-tertiary)]">GET /api/v1/account/me</code>,{' '}
        <code className="text-[var(--text-tertiary)]">GET /api/v1/account/usage</code>,{' '}
        <code className="text-[var(--text-tertiary)]">GET /api/v1/devices</code>.
        Each key is scoped to this box only and can be revoked at any time below.
      </p>

      {/* ------------------------------------------------------------------ */}
      {/* Freshly-issued key — shown exactly once                             */}
      {/* ------------------------------------------------------------------ */}
      {newKey && (
        <div className="mb-6 p-4 rounded-lg bg-[var(--status-warning-soft)] border border-warning-soft">
          <p className="text-xs font-medium text-warning mb-2">
            Copy this key now — it will not be shown again.
          </p>
          <div className="flex items-center gap-2">
            <code className="flex-1 min-w-0 text-xs font-mono px-2 py-1.5 rounded bg-[var(--bg-surface)] text-[var(--text-primary)] break-all select-all">
              {newKey.key}
            </code>
            <button onClick={copyKey} className="btn-ghost text-xs shrink-0">
              {copied ? 'Copied' : 'Copy'}
            </button>
          </div>
          <button onClick={() => setNewKey(null)} className="text-xs text-[var(--text-muted)] hover:text-[var(--text-secondary)] mt-3">
            Done
          </button>
        </div>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* Issue new key                                                        */}
      {/* ------------------------------------------------------------------ */}
      <div className="mb-6">
        <label className="block text-xs text-[var(--text-muted)] mb-1">New key name</label>
        <div className="flex gap-2">
          <input
            value={name}
            onChange={e => setName(e.target.value)}
            placeholder="e.g. CLI on my laptop"
            className="input flex-1"
            onKeyDown={e => { if (e.key === 'Enter' && !issuing) issueKey() }}
          />
          <button onClick={issueKey} disabled={issuing} className="btn text-sm disabled:opacity-50 shrink-0">
            {issuing ? 'Issuing…' : 'Issue Key'}
          </button>
        </div>
        <p className="text-[12px] text-[var(--text-faint)] mt-1">Requires confirming your password.</p>
      </div>

      {/* ------------------------------------------------------------------ */}
      {/* Existing keys                                                        */}
      {/* ------------------------------------------------------------------ */}
      <div className="pt-4 border-t border-[var(--border-default)]">
        <h3 className="text-sm font-medium text-[var(--text-secondary)] mb-3">Your keys</h3>
        {loading && <p className="text-xs text-[var(--text-faint)]">Loading…</p>}
        {!loading && keys && keys.length === 0 && (
          <p className="text-xs text-[var(--text-faint)]">No API keys yet.</p>
        )}
        {!loading && keys && keys.length > 0 && (
          <div className="space-y-2">
            {keys.map(k => {
              const revoked = !!k.revoked_at
              return (
                <div
                  key={k.id}
                  className={`flex items-center justify-between gap-3 rounded-lg border px-4 py-3 ${
                    revoked
                      ? 'border-[var(--border-default)] bg-[var(--bg-surface)] opacity-60'
                      : 'border-[var(--border-default)] bg-[var(--bg-surface)]'
                  }`}
                >
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium text-[var(--text-primary)] truncate">{k.name}</span>
                      {revoked && (
                        <span className="text-[12px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-[var(--status-danger-soft)] text-danger">
                          Revoked
                        </span>
                      )}
                    </div>
                    <p className="text-xs text-[var(--text-muted)] mt-0.5">
                      Issued {formatDate(k.created_at)}
                      {revoked && ` · Revoked ${formatDate(k.revoked_at)}`}
                    </p>
                  </div>
                  {!revoked && (
                    <button
                      onClick={() => revokeKey(k.id)}
                      disabled={revokingId === k.id}
                      aria-label={`Revoke ${k.name}`}
                      className="shrink-0 text-xs text-danger hover:text-danger disabled:opacity-50"
                    >
                      {revokingId === k.id ? 'Revoking…' : 'Revoke'}
                    </button>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </div>

      {error && (
        <div className="mt-4 text-xs rounded px-3 py-2 bg-[var(--status-danger-soft)] text-danger">
          {error}
        </div>
      )}
    </Section>
  )
}
