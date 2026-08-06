import { useState, useEffect, useCallback } from 'react'
import { Section, Card, Field, InfoList, InfoRow, Banner, Pill } from './ui'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function errorMessage(err: unknown): string {
  return isRecord(err) && typeof err.message === 'string' ? err.message : String(err)
}

interface StorageConfig {
  mode?: string
  minio_endpoint?: string
  minio_region?: string
  minio_bucket?: string
  minio_creds_ref?: string
}

function toStorageConfig(x: unknown): StorageConfig {
  if (!isRecord(x)) return {}
  return {
    mode: typeof x.mode === 'string' ? x.mode : undefined,
    minio_endpoint: typeof x.minio_endpoint === 'string' ? x.minio_endpoint : undefined,
    minio_region: typeof x.minio_region === 'string' ? x.minio_region : undefined,
    minio_bucket: typeof x.minio_bucket === 'string' ? x.minio_bucket : undefined,
    minio_creds_ref: typeof x.minio_creds_ref === 'string' ? x.minio_creds_ref : undefined,
  }
}

// ---------------------------------------------------------------------------
// STORE-BYO-01: Per-account storage-backend selector
// Endpoints:
//   GET /api/storagemode  → { mode, minio_endpoint, minio_region, minio_bucket, minio_creds_ref }
//   PUT /api/storagemode  → same shape, persists the selection
//
// The anchor inbox is NOT an exception to the choice made here, and the old
// "always kept on Tigris regardless of this setting" copy was wrong: the box
// creates no anchor-inbox bucket and uploads nothing for it. It is a ~1 GiB
// entitlement recorded for cloud-connected accounts and created cloud-side —
// see backend/services/anchorinbox (package doc). Nothing below should imply
// this device hands data to a hosted service unless the user picked a hosted
// mode above.
// ---------------------------------------------------------------------------

// D-STORE-LOCAL-DEFAULT: the default is this device's own disk. Both other
// modes are opt-ins, and the hosted one is labelled as hosted. Keep these
// values in sync with backend/internal/storagemode (Mode constants).
interface StorageModeOption {
  value: string
  label: string
  desc: string
}

const MODES: StorageModeOption[] = [
  {
    value: 'local-fs',
    label: 'This device (default)',
    desc: 'Keep everything on this device’s own disk. No object store, no credentials, and nothing is sent to a third-party service.',
  },
  {
    value: 'local-minio-sync',
    label: 'Local MinIO + Sync',
    desc: 'Read and write a co-located MinIO source-of-truth. The CRDT sync layer replicates between nodes. Still self-hosted.',
  },
  {
    value: 'central-tigris',
    label: 'Tigris (hosted — opt in)',
    desc: 'Read and write directly to the hosted Tigris bucket. A third party stores your data. No local sync layer engaged.',
  },
]

export default function StoragePanel() {
  const [cfg, setCfg] = useState<StorageConfig | null>(null)
  const [mode, setMode] = useState('local-fs')
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
      .then((raw: unknown) => {
        const d = toStorageConfig(raw)
        setCfg(d)
        setMode(d.mode || 'local-fs')
        setEndpoint(d.minio_endpoint || '')
        setRegion(d.minio_region || '')
        setBucket(d.minio_bucket || '')
        setCredsRef(d.minio_creds_ref || '')
      })
      .catch(e => setError(errorMessage(e)))
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
      const raw: unknown = await r.json().catch(() => ({}))
      if (!r.ok) {
        const errMsg = isRecord(raw) && typeof raw.error === 'string' ? raw.error : 'HTTP ' + r.status
        throw new Error(errMsg)
      }
      setCfg(toStorageConfig(raw))
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  // -------------------------------------------------------------------------
  // Render
  // -------------------------------------------------------------------------
  return (
    <Section
      icon="storagemode"
      title="Storage Backend"
      desc="Choose where Vulos stores your mail, files, and office data on this device."
    >
      <Card
        icon="storagemode"
        title="Backend"
        desc="The default keeps everything on this device. Local MinIO makes a co-located node the source of truth and replicates over CRDT sync. Tigris hands your data to a hosted third party — pick it deliberately."
        footer={
          <div className="flex items-center justify-between gap-3">
            <span className="text-xs text-[var(--text-tertiary)]">
              The ~1&nbsp;GiB anchor inbox is a hosted-account allowance, set up by Vulos cloud only if you
              connect this device to a Vulos account. This device neither stores nor uploads anything for it.
            </span>
            <div className="flex items-center gap-2 shrink-0">
              <button onClick={load} disabled={loading || saving} className="btn-ghost text-sm">Refresh</button>
              <button onClick={handleSave} disabled={saving || loading} className="btn-primary text-sm">
                {saving ? 'Saving…' : saved ? 'Saved' : 'Save changes'}
              </button>
            </div>
          </div>
        }
      >
        <div className="space-y-2">
          {MODES.map(opt => {
            const selected = mode === opt.value
            return (
              <label
                key={opt.value}
                htmlFor={`storage-mode-${opt.value}`}
                className={`flex items-start gap-3 rounded-xl border px-4 py-3 cursor-pointer transition-colors duration-[var(--motion-base)] ${
                  selected
                    ? 'accent-border accent-bg-soft'
                    : 'border-[var(--border-default)] bg-[var(--bg-elevated)]/40 hover:border-[var(--border-strong)]'
                }`}
              >
                <input
                  id={`storage-mode-${opt.value}`}
                  type="radio"
                  name="storage-mode-radio"
                  value={opt.value}
                  checked={selected}
                  onChange={() => setMode(opt.value)}
                  className="mt-1 accent-blue-500"
                />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium text-[var(--text-primary)]">{opt.label}</span>
                    {cfg?.mode === opt.value && <Pill tone="success">Active</Pill>}
                  </div>
                  <p className="text-xs text-[var(--text-tertiary)] mt-0.5 leading-relaxed">{opt.desc}</p>
                </div>
              </label>
            )
          })}
        </div>
      </Card>

      {/* MinIO fields — only shown when local-minio-sync is selected */}
      {mode === 'local-minio-sync' && (
        <Card icon="storage" title="MinIO connection" desc="Point Vulos at your co-located MinIO server. Credentials are read from the referenced file — they are never stored here.">
          <Field label="Endpoint" hint="The URL of your co-located MinIO server, e.g. http://localhost:9000">
            <input value={endpoint} onChange={e => setEndpoint(e.target.value)} placeholder="http://localhost:9000" className="input" />
          </Field>
          <Field label="Bucket" hint="The MinIO bucket that stores this device's data.">
            <input value={bucket} onChange={e => setBucket(e.target.value)} placeholder="vulos-local" className="input" />
          </Field>
          <Field label="Region" hint="Leave blank to use 'auto'. Only required for some MinIO configurations.">
            <input value={region} onChange={e => setRegion(e.target.value)} placeholder="auto" className="input" />
          </Field>
          <Field label="Credentials reference" hint="Path to the credentials file written by the installer (e.g. /data/minio/.minio_secret). The credentials themselves are never stored here.">
            <input value={credsRef} onChange={e => setCredsRef(e.target.value)} placeholder="/data/minio/.minio_secret" className="input" />
          </Field>
        </Card>
      )}

      {/* Status summary */}
      {cfg && !loading && (
        <Card title="Active configuration">
          <InfoList>
            <InfoRow label="Active backend" value={cfg.mode || '—'} />
            {cfg.mode === 'local-minio-sync' && (
              <>
                <InfoRow label="MinIO endpoint" value={cfg.minio_endpoint || '—'} mono />
                <InfoRow label="Bucket" value={cfg.minio_bucket || '—'} mono />
                <InfoRow label="Region" value={cfg.minio_region || '—'} />
              </>
            )}
          </InfoList>
        </Card>
      )}

      {!loading && !cfg && (
        <Banner tone="warning">
          Storage backend not yet reachable — configuration will be applied when available.
        </Banner>
      )}

      {error && <Banner tone="danger">{error}</Banner>}
    </Section>
  )
}
