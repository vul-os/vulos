import { useState, useEffect, useCallback } from 'react'

// ---------------------------------------------------------------------------
// STORE-BYO-01: Per-account storage-backend selector
// Endpoints:
//   GET /api/storagemode  → { mode, minio_endpoint, minio_region, minio_bucket, minio_creds_ref }
//   PUT /api/storagemode  → same shape, persists the selection
//
// The anchor inbox is ALWAYS on Tigris regardless of the mode selected here.
// ---------------------------------------------------------------------------

function Section({ title, children }) {
  return <div><h2 className="text-lg font-medium mb-4">{title}</h2>{children}</div>
}

function Field({ label, hint, children }) {
  return (
    <div className="mb-3">
      <label className="block text-xs text-neutral-500 mb-1">{label}</label>
      {children}
      {hint && <p className="text-[11px] text-neutral-600 mt-1">{hint}</p>}
    </div>
  )
}

function InfoRow({ label, value }) {
  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
      <span className="text-xs text-neutral-500">{label}</span>
      <span className="text-sm text-neutral-300">{value || '—'}</span>
    </div>
  )
}

const MODES = [
  {
    value: 'central-tigris',
    label: 'Tigris (default)',
    desc: 'Read and write directly to the hosted Tigris bucket. No local sync layer engaged.',
  },
  {
    value: 'local-minio-sync',
    label: 'Local MinIO + Sync',
    desc: 'Read and write a co-located MinIO source-of-truth. The CRDT sync layer replicates between nodes.',
  },
]

export default function StoragePanel() {
  const [cfg, setCfg] = useState(null)
  const [mode, setMode] = useState('central-tigris')
  const [endpoint, setEndpoint] = useState('')
  const [region, setRegion] = useState('')
  const [bucket, setBucket] = useState('')
  const [credsRef, setCredsRef] = useState('')

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')

  // -------------------------------------------------------------------------
  // Load
  // -------------------------------------------------------------------------
  const load = useCallback(() => {
    setLoading(true)
    setError('')
    fetch('/api/storagemode')
      .then(r => r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)))
      .then(d => {
        setCfg(d)
        setMode(d.mode || 'central-tigris')
        setEndpoint(d.minio_endpoint || '')
        setRegion(d.minio_region || '')
        setBucket(d.minio_bucket || '')
        setCredsRef(d.minio_creds_ref || '')
      })
      .catch(e => setError(e.message))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])

  // -------------------------------------------------------------------------
  // Save
  // -------------------------------------------------------------------------
  const handleSave = async () => {
    setSaving(true)
    setSaved(false)
    setError('')
    try {
      const body = {
        mode,
        minio_endpoint: endpoint,
        minio_region: region,
        minio_bucket: bucket,
        minio_creds_ref: credsRef,
      }
      const r = await fetch('/api/storagemode', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      const d = await r.json().catch(() => ({}))
      if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status)
      setCfg(d)
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch (e) {
      setError(e.message)
    } finally {
      setSaving(false)
    }
  }

  // -------------------------------------------------------------------------
  // Render
  // -------------------------------------------------------------------------
  return (
    <Section title="Storage Backend">
      <p className="text-xs text-neutral-600 mb-5 leading-relaxed">
        Choose where Vulos stores your mail, files, and office data on this device.
        The <strong className="text-neutral-400">anchor inbox</strong> is always kept
        on Tigris regardless of this setting, guaranteeing a reliable landing zone.
      </p>

      {/* ------------------------------------------------------------------ */}
      {/* Backend selector                                                     */}
      {/* ------------------------------------------------------------------ */}
      <Field label="Storage backend">
        <div className="space-y-2">
          {MODES.map(opt => (
            <label
              key={opt.value}
              htmlFor={`storage-mode-${opt.value}`}
              className={`flex items-start gap-3 rounded-lg border px-4 py-3 cursor-pointer transition-colors ${
                mode === opt.value
                  ? 'border-blue-600/60 bg-blue-600/10'
                  : 'border-neutral-800/60 bg-neutral-900/30 hover:bg-neutral-900/60'
              }`}
            >
              <input
                id={`storage-mode-${opt.value}`}
                type="radio"
                name="storage-mode-radio"
                value={opt.value}
                checked={mode === opt.value}
                onChange={() => setMode(opt.value)}
                className="mt-1 accent-blue-500"
              />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium text-neutral-200">{opt.label}</span>
                  {cfg?.mode === opt.value && (
                    <span className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-green-900/40 text-green-400">
                      Active
                    </span>
                  )}
                </div>
                <p className="text-xs text-neutral-500 mt-0.5">{opt.desc}</p>
              </div>
            </label>
          ))}
        </div>
      </Field>

      {/* ------------------------------------------------------------------ */}
      {/* MinIO fields — only shown when local-minio-sync is selected          */}
      {/* ------------------------------------------------------------------ */}
      {mode === 'local-minio-sync' && (
        <div className="mt-4 pt-4 border-t border-neutral-800/50 space-y-0">
          <h3 className="text-sm font-medium text-neutral-300 mb-4">MinIO connection</h3>

          <Field
            label="Endpoint"
            hint="The URL of your co-located MinIO server, e.g. http://localhost:9000"
          >
            <input
              value={endpoint}
              onChange={e => setEndpoint(e.target.value)}
              placeholder="http://localhost:9000"
              className="input"
            />
          </Field>

          <Field
            label="Bucket"
            hint="The MinIO bucket that stores this device's data."
          >
            <input
              value={bucket}
              onChange={e => setBucket(e.target.value)}
              placeholder="vulos-local"
              className="input"
            />
          </Field>

          <Field
            label="Region"
            hint="Leave blank to use 'auto'. Only required for some MinIO configurations."
          >
            <input
              value={region}
              onChange={e => setRegion(e.target.value)}
              placeholder="auto"
              className="input"
            />
          </Field>

          <Field
            label="Credentials reference"
            hint="Path to the credentials file written by the installer (e.g. /data/minio/.minio_secret). The credentials themselves are never stored here."
          >
            <input
              value={credsRef}
              onChange={e => setCredsRef(e.target.value)}
              placeholder="/data/minio/.minio_secret"
              className="input"
            />
          </Field>
        </div>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* Status summary                                                       */}
      {/* ------------------------------------------------------------------ */}
      {cfg && !loading && (
        <div className="mt-5 pt-4 border-t border-neutral-800/50">
          <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
            <InfoRow label="Active backend" value={cfg.mode || '—'} />
            {cfg.mode === 'local-minio-sync' && (
              <>
                <InfoRow label="MinIO endpoint" value={cfg.minio_endpoint || '—'} />
                <InfoRow label="Bucket" value={cfg.minio_bucket || '—'} />
                <InfoRow label="Region" value={cfg.minio_region || '—'} />
              </>
            )}
          </div>
          <p className="text-[11px] text-neutral-600 mt-2 leading-relaxed">
            The anchor inbox always uses Tigris regardless of the backend selected above.
          </p>
        </div>
      )}

      {!loading && !cfg && (
        <div className="mt-4 p-3 rounded-lg bg-amber-900/20 border border-amber-800/30">
          <p className="text-xs text-amber-400">
            Storage backend not yet reachable — configuration will be applied when available.
          </p>
        </div>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* Save bar                                                             */}
      {/* ------------------------------------------------------------------ */}
      <div className="flex gap-3 items-center mt-5">
        <button
          onClick={handleSave}
          disabled={saving || loading}
          className="btn text-sm disabled:opacity-50"
        >
          {saving ? 'Saving…' : saved ? 'Saved' : 'Save'}
        </button>
        <button
          onClick={load}
          disabled={loading || saving}
          className="btn-ghost text-sm"
        >
          Refresh
        </button>
      </div>

      {error && (
        <div className="mt-3 text-xs rounded px-3 py-2 bg-red-900/30 text-red-400">
          {error}
        </div>
      )}
    </Section>
  )
}
