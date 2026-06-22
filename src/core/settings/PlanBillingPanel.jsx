import { useState, useEffect, useCallback } from 'react'

// ---------------------------------------------------------------------------
// Plan & Billing
//
// A coherent account/plan surface that was previously fragmented. It reads the
// OS tier hint (window.__VULOS_TIER — set by the OS bootstrap / Setup, and
// previously read-then-unused) and surfaces real usage/quota for the three
// cloud-backed resources the OS already exposes:
//
//   storage  GET /api/storage/status  → { configured, size_gb, used_gb, bucket }
//   backup   GET /api/vault/status    → { initialized, last_backup }
//            GET /api/vault/sync       → { total_snapshots }
//   relay    GET /api/auth/cloud/status → { enrolled }
//
// Every fetch degrades gracefully — a missing/!ok endpoint renders an
// "unavailable" row rather than erroring, so the panel is useful on a fully
// standalone box too. All styling uses the shared semantic tokens.
// ---------------------------------------------------------------------------

function Section({ title, children }) {
  return <div><h2 className="text-lg font-medium mb-4">{title}</h2>{children}</div>
}

function InfoRow({ label, value, tone }) {
  const color =
    tone === 'good' ? 'text-green-400' :
    tone === 'warn' ? 'text-amber-400' :
    tone === 'bad' ? 'text-red-400' :
    'text-neutral-300'
  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
      <span className="text-xs text-neutral-500">{label}</span>
      <span className={`text-sm ${color}`}>{value ?? '—'}</span>
    </div>
  )
}

// QuotaBar — a usage/limit indicator with explicit near-limit (warn) and
// over-limit (exceeded) states. When `limit` is unknown it renders the used
// amount without a bar.
function QuotaBar({ label, used, limit, unit = 'GB', format }) {
  const fmt = format || ((n) => `${(n ?? 0).toFixed(1)} ${unit}`)
  const hasLimit = typeof limit === 'number' && limit > 0
  const pct = hasLimit ? Math.min(100, (used / limit) * 100) : 0
  const exceeded = hasLimit && used >= limit
  const near = hasLimit && !exceeded && pct >= 85

  const barColor = exceeded ? 'bg-red-500' : near ? 'bg-amber-500' : 'bg-blue-500'

  return (
    <div className="px-4 py-3 bg-neutral-900/40">
      <div className="flex items-center justify-between mb-1.5">
        <span className="text-xs text-neutral-400">{label}</span>
        <span className={`text-xs tabular-nums ${exceeded ? 'text-red-400' : near ? 'text-amber-400' : 'text-neutral-400'}`}>
          {fmt(used)}{hasLimit ? ` / ${fmt(limit)}` : ''}
        </span>
      </div>
      {hasLimit && (
        <div className="h-1.5 rounded-full bg-neutral-800 overflow-hidden">
          <div
            className={`h-full rounded-full transition-[width] duration-500 ${barColor}`}
            style={{ width: `${Math.max(2, pct)}%` }}
          />
        </div>
      )}
      {exceeded && (
        <p className="text-[11px] text-red-400 mt-1.5">
          Quota exceeded — upgrade your plan or free up space to keep syncing.
        </p>
      )}
      {near && (
        <p className="text-[11px] text-amber-400 mt-1.5">
          You’re close to your limit.
        </p>
      )}
    </div>
  )
}

// Tier presentation. Caps here are display defaults the UI uses to draw quota
// bars when the backend does not report a hard limit (storage/status may leave
// size_gb at 0 on a standalone box). They mirror the public plan tiers.
const TIERS = {
  free: {
    label: 'Free',
    blurb: 'Local-first, single device. Cloud relay and backup are limited.',
    storageGB: 5,
    features: ['1 device', '5 GB cloud storage', 'Community support'],
  },
  pro: {
    label: 'Pro',
    blurb: 'Multi-device sync, encrypted backup, and priority relay.',
    storageGB: 100,
    features: ['Unlimited devices', '100 GB cloud storage', 'Encrypted backup', 'Priority relay'],
  },
  team: {
    label: 'Team',
    blurb: 'Shared spaces, pooled storage, and admin controls.',
    storageGB: 1000,
    features: ['Everything in Pro', '1 TB pooled storage', 'Shared spaces', 'Admin console'],
  },
}

function resolveTier() {
  const raw = (typeof window !== 'undefined' && typeof window.__VULOS_TIER === 'string')
    ? window.__VULOS_TIER.toLowerCase()
    : 'free'
  return TIERS[raw] ? raw : 'free'
}

export default function PlanBillingPanel() {
  const tierKey = resolveTier()
  const tier = TIERS[tierKey]
  const isTopTier = tierKey === 'team'

  const [storage, setStorage] = useState(null)
  const [vault, setVault] = useState(null)
  const [sync, setSync] = useState(null)
  const [cloud, setCloud] = useState(null)
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    setLoading(true)
    const safe = (p) => p.then(r => (r.ok ? r.json() : null)).catch(() => null)
    const [s, v, sy, c] = await Promise.all([
      safe(fetch('/api/storage/status')),
      safe(fetch('/api/vault/status')),
      safe(fetch('/api/vault/sync')),
      safe(fetch('/api/auth/cloud/status')),
    ])
    setStorage(s)
    setVault(v)
    setSync(sy)
    setCloud(c)
    setLoading(false)
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { load() }, [load])

  // Storage quota: prefer a backend-reported size, fall back to the tier cap.
  const storageUsed = storage?.used_gb ?? 0
  const storageLimit = (storage?.size_gb && storage.size_gb > 0)
    ? storage.size_gb
    : tier.storageGB

  return (
    <Section title="Plan & Billing">
      <p className="text-xs text-neutral-600 mb-5 leading-relaxed">
        Your Vulos plan and how much of your cloud entitlement you’re using.
        Storage, backup, and relay are billed against your account tier.
      </p>

      {/* ── Plan card ───────────────────────────────────────────────────── */}
      <div className="rounded-xl border border-neutral-800/60 overflow-hidden mb-5">
        <div className="flex items-start justify-between gap-4 px-4 py-4 bg-neutral-900/50">
          <div className="min-w-0">
            <div className="flex items-center gap-2 mb-1">
              <span className="text-base font-semibold text-neutral-100">{tier.label}</span>
              <span
                className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded"
                style={{ background: 'color-mix(in srgb, var(--accent) 18%, transparent)', color: 'var(--accent)' }}
              >
                Current plan
              </span>
            </div>
            <p className="text-xs text-neutral-500 leading-relaxed">{tier.blurb}</p>
          </div>
          {!isTopTier && (
            <a
              href="https://vulos.org/account/billing"
              target="_blank"
              rel="noreferrer"
              className="btn-primary text-sm shrink-0 no-underline"
            >
              Upgrade
            </a>
          )}
        </div>
        <ul className="px-4 py-3 grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-1.5 border-t border-neutral-800/50">
          {tier.features.map(f => (
            <li key={f} className="flex items-center gap-2 text-xs text-neutral-400">
              <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 shrink-0" style={{ color: 'var(--accent)' }} fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M3 8.5l3 3 7-7" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
              {f}
            </li>
          ))}
        </ul>
      </div>

      {/* ── Usage & quotas ──────────────────────────────────────────────── */}
      <h3 className="text-xs uppercase tracking-wider text-neutral-500 font-medium mb-2">Usage</h3>
      <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50 mb-2">
        <QuotaBar label="Cloud storage" used={storageUsed} limit={storageLimit} unit="GB" />
        <InfoRow
          label="Backup"
          value={vault?.initialized ? `Active · last ${vault.last_backup || 'never'}` : 'Not configured'}
          tone={vault?.initialized ? 'good' : undefined}
        />
        <InfoRow
          label="Snapshots"
          value={sync ? (sync.total_snapshots ?? 0) : (loading ? '…' : 'Unavailable')}
        />
        <InfoRow
          label="Cloud relay"
          value={cloud ? (cloud.enrolled ? 'Enrolled' : 'Not enrolled') : (loading ? '…' : 'Unavailable')}
          tone={cloud?.enrolled ? 'good' : undefined}
        />
      </div>

      <div className="flex gap-3 items-center mt-4">
        <button onClick={load} disabled={loading} className="btn-ghost text-sm disabled:opacity-50">
          {loading ? 'Refreshing…' : 'Refresh'}
        </button>
        <a
          href="https://vulos.org/account/billing"
          target="_blank"
          rel="noreferrer"
          className="btn text-sm no-underline"
        >
          Manage billing
        </a>
      </div>

      <p className="text-[11px] text-neutral-600 mt-4 leading-relaxed">
        Billing and plan changes are managed in your Vulos Cloud account.
        Usage shown here reflects this device’s view and may lag the cloud by a
        few minutes.
      </p>
    </Section>
  )
}
