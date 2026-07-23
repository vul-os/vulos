import { useState, useEffect, useCallback } from 'react'
import { requireStepUp } from '../../lib/stepup'

// ---------------------------------------------------------------------------
// Settings → Encryption: BYO-KMS (customer/owner-held encryption keys).
//
// Endpoints (backend/cmd/server/routes_kms.go):
//   GET  /api/kms/status            → { configured, config?, dek_count }
//   POST /api/kms/configure         → register the KEK reference (first time)
//   POST /api/kms/rotate            → rotate the KEK reference (step-up gated)
//   GET  /api/kms/deks              → list wrapped-DEK records
//   POST /api/kms/deks/{id}/decrypt → unwrap-export (step-up gated)
//   POST /api/kms/deks/{id}/revoke  → revoke a DEK
//
// This panel is complementary to StoragePanel (which picks WHERE data lives).
// EncryptionPanel is about WHO holds the key that makes that data readable —
// it never touches the storage backend selection.
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

function Field({ label, hint, children }) {
  return (
    <div className="mb-3">
      <label className="block text-xs text-[var(--text-muted)] mb-1">{label}</label>
      {children}
      {hint && <p className="text-[11px] text-[var(--text-faint)] mt-1">{hint}</p>}
    </div>
  )
}

function InfoRow({ label, value }) {
  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
      <span className="text-xs text-[var(--text-muted)]">{label}</span>
      <span className="text-sm text-[var(--text-secondary)]">{value ?? '—'}</span>
    </div>
  )
}

const KINDS = [
  {
    value: 'symmetric',
    label: 'Local key (symmetric)',
    desc: 'The box generates — or you supply — a 32-byte AES key. Kept on the box only encrypted under its own local secret. Weaker sovereignty guarantee: the box briefly observes the plaintext key at setup time.',
  },
  {
    value: 'http',
    label: 'Your own KMS endpoint (HTTP)',
    desc: 'Point at a KMS you operate (e.g. a self-hosted HashiCorp Vault transit engine). The key never leaves your infrastructure — this box only ever sees wrapped blobs.',
  },
]

async function readJSON(r) {
  const d = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status)
  return d
}

export default function EncryptionPanel() {
  const [status, setStatus] = useState(null)
  const [deks, setDeks] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  // Setup / rotate form state.
  const [kind, setKind] = useState('symmetric')
  const [endpoint, setEndpoint] = useState('')
  const [keyMaterial, setKeyMaterial] = useState('')
  const [busy, setBusy] = useState(false)
  const [revealedKey, setRevealedKey] = useState('') // shown ONCE after generate

  const load = useCallback(() => {
    setLoading(true)
    setError('')
    Promise.all([
      fetch('/api/kms/status').then(readJSON),
      fetch('/api/kms/deks').then(readJSON).catch(() => []),
    ])
      .then(([st, list]) => {
        setStatus(st)
        setDeks(Array.isArray(list) ? list : [])
        if (st.configured) {
          setKind(st.config?.kind || 'symmetric')
          setEndpoint(st.config?.endpoint || '')
        }
      })
      .catch(e => setError(e.message))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])

  const handleConfigure = async () => {
    setBusy(true)
    setError('')
    setRevealedKey('')
    try {
      const r = await fetch('/api/kms/configure', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kind, endpoint, key_material: keyMaterial }),
      })
      const d = await readJSON(r)
      if (d.generated_key_hex) setRevealedKey(d.generated_key_hex)
      setKeyMaterial('')
      load()
    } catch (e) {
      setError(e.message)
    } finally {
      setBusy(false)
    }
  }

  const handleRotate = async () => {
    try {
      await requireStepUp()
    } catch {
      return // cancelled
    }
    setBusy(true)
    setError('')
    setRevealedKey('')
    try {
      const r = await fetch('/api/kms/rotate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kind, endpoint, key_material: keyMaterial }),
      })
      const d = await readJSON(r)
      if (d.generated_key_hex) setRevealedKey(d.generated_key_hex)
      setKeyMaterial('')
      load()
    } catch (e) {
      setError(e.message)
    } finally {
      setBusy(false)
    }
  }

  const handleRevoke = async (id) => {
    setError('')
    try {
      await fetch(`/api/kms/deks/${encodeURIComponent(id)}/revoke`, { method: 'POST' }).then(readJSON)
      load()
    } catch (e) {
      setError(e.message)
    }
  }

  const configured = !!status?.configured

  return (
    <Section
      title="Encryption"
      desc="Bring your own encryption key (BYO-KMS): this box stores only wrapped data-keys and ciphertext. Without your key, nothing on the box — including a future cloud sync target — has a plaintext path to your data."
    >
      {/* -------------------------------------------------------------- */}
      {/* Sovereignty guarantee explainer                                  */}
      {/* -------------------------------------------------------------- */}
      <div className="mb-5 p-3 rounded-lg bg-[var(--accent-soft)] border accent-border">
        <p className="text-xs text-[var(--text-secondary)] leading-relaxed">
          <strong className="text-[var(--text-primary)]">How this works:</strong> every piece of data
          protected under BYO-KMS gets its own random Data Encryption Key (DEK). The DEK encrypts the
          data, then is itself encrypted ("wrapped") under <em>your</em> Key Encryption Key (KEK) — the
          one you register below. Only the wrapped DEK and the ciphertext are ever stored. Rotating your
          KEK re-wraps every DEK; the underlying data is never re-encrypted or touched.
        </p>
      </div>

      {/* -------------------------------------------------------------- */}
      {/* Status summary                                                    */}
      {/* -------------------------------------------------------------- */}
      {!loading && status && (
        <div className="mb-5">
          <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)]">
            <InfoRow
              label="Status"
              value={configured ? 'Configured' : 'Not configured'}
            />
            {configured && (
              <>
                <InfoRow label="Provider" value={status.config?.kind === 'http' ? 'Your KMS endpoint' : 'Local key'} />
                {status.config?.kind === 'http' && <InfoRow label="Endpoint" value={status.config?.endpoint} />}
                <InfoRow label="KEK version" value={status.config?.kek_version} />
                <InfoRow label="Wrapped keys" value={status.dek_count} />
              </>
            )}
          </div>
        </div>
      )}

      {/* -------------------------------------------------------------- */}
      {/* Revealed key — shown ONCE right after generation                 */}
      {/* -------------------------------------------------------------- */}
      {revealedKey && (
        <div className="mb-5 p-3 rounded-lg bg-[var(--status-warning-soft)] border border-warning-soft">
          <p className="text-xs text-[var(--status-warning)] font-medium mb-2">
            Save this key now — it will not be shown again. Anyone with this key (and physical access to
            an old backup) could decrypt data wrapped under it; anyone WITHOUT it, including this box, cannot.
          </p>
          <code className="block text-[11px] font-mono break-all p-2 rounded bg-[var(--bg-surface)] text-[var(--text-primary)]">
            {revealedKey}
          </code>
        </div>
      )}

      {/* -------------------------------------------------------------- */}
      {/* Register / rotate form                                           */}
      {/* -------------------------------------------------------------- */}
      <div className="pt-2">
        <h3 className="text-sm font-medium text-[var(--text-secondary)] mb-3">
          {configured ? 'Rotate your KEK' : 'Register your KEK'}
        </h3>

        <Field label="Key provider">
          <div className="space-y-2">
            {KINDS.map(opt => (
              <label
                key={opt.value}
                htmlFor={`kms-kind-${opt.value}`}
                className={`flex items-start gap-3 rounded-lg border px-4 py-3 cursor-pointer transition-colors ${
                  kind === opt.value
                    ? 'accent-border bg-[var(--accent-soft)]'
                    : 'border-[var(--border-default)] bg-[var(--bg-surface)] hover:bg-[var(--bg-surface)]'
                }`}
              >
                <input
                  id={`kms-kind-${opt.value}`}
                  type="radio"
                  name="kms-kind-radio"
                  value={opt.value}
                  checked={kind === opt.value}
                  onChange={() => setKind(opt.value)}
                  className="mt-1 accent-blue-500"
                />
                <div className="flex-1 min-w-0">
                  <span className="text-sm font-medium text-[var(--text-primary)]">{opt.label}</span>
                  <p className="text-xs text-[var(--text-muted)] mt-0.5">{opt.desc}</p>
                </div>
              </label>
            ))}
          </div>
        </Field>

        {kind === 'http' && (
          <Field label="KMS endpoint" hint="Base URL of your KMS service, e.g. https://vault.example.com/v1/transit">
            <input
              value={endpoint}
              onChange={e => setEndpoint(e.target.value)}
              placeholder="https://vault.example.com/v1/transit"
              className="input"
            />
          </Field>
        )}

        <Field
          label={kind === 'http' ? 'Bearer token (optional)' : 'Key material (optional)'}
          hint={
            kind === 'http'
              ? 'Sent as "Authorization: Bearer <token>" to your endpoint. Leave blank if your endpoint does not require one.'
              : 'Leave blank to have the box generate a fresh 32-byte key for you (shown once), or paste a 64-character hex key of your own.'
          }
        >
          <input
            value={keyMaterial}
            onChange={e => setKeyMaterial(e.target.value)}
            type="password"
            placeholder={kind === 'http' ? 'optional bearer token' : 'leave blank to auto-generate'}
            className="input"
            autoComplete="off"
          />
        </Field>

        <div className="flex gap-3 items-center mt-4">
          {!configured ? (
            <button onClick={handleConfigure} disabled={busy || loading} className="btn text-sm disabled:opacity-50">
              {busy ? 'Registering…' : 'Register KEK'}
            </button>
          ) : (
            <button onClick={handleRotate} disabled={busy || loading} className="btn text-sm disabled:opacity-50">
              {busy ? 'Rotating…' : 'Rotate KEK'}
            </button>
          )}
          <button onClick={load} disabled={loading || busy} className="btn-ghost text-sm">
            Refresh
          </button>
        </div>
        {configured && (
          <p className="text-[11px] text-[var(--text-faint)] mt-2">
            Rotating requires an extra security step and re-wraps every existing key — your data is never
            re-encrypted or touched.
          </p>
        )}
      </div>

      {/* -------------------------------------------------------------- */}
      {/* Wrapped-key inventory                                            */}
      {/* -------------------------------------------------------------- */}
      {configured && deks.length > 0 && (
        <div className="mt-6 pt-4 border-t border-[var(--border-default)]">
          <h3 className="text-sm font-medium text-[var(--text-secondary)] mb-3">Wrapped keys</h3>
          <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)]">
            {deks.map(d => (
              <div key={d.id} className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
                <div className="min-w-0">
                  <p className="text-sm text-[var(--text-secondary)] truncate">{d.object_ref}</p>
                  <p className="text-[11px] text-[var(--text-faint)]">
                    v{d.kek_version} · {d.revoked ? 'revoked' : 'active'}
                  </p>
                </div>
                {!d.revoked && (
                  <button onClick={() => handleRevoke(d.id)} className="btn-ghost text-xs shrink-0">
                    Revoke
                  </button>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      {!loading && !status && (
        <div className="mt-4 p-3 rounded-lg bg-[var(--status-warning-soft)] border border-warning-soft">
          <p className="text-xs text-[var(--status-warning)]">
            BYO-KMS is not reachable on this box (KMS_STORAGE_KEK may be unset).
          </p>
        </div>
      )}

      {error && (
        <div className="mt-3 text-xs rounded px-3 py-2 bg-[var(--status-danger-soft)] text-[var(--status-danger)]">
          {error}
        </div>
      )}
    </Section>
  )
}
