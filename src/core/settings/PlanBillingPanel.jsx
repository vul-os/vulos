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

function InfoRow({ label, value, tone }) {
  const color =
    tone === 'good' ? 'text-[var(--status-success)]' :
    tone === 'warn' ? 'text-[var(--status-warning)]' :
    tone === 'bad' ? 'text-[var(--status-danger)]' :
    'text-[var(--text-secondary)]'
  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
      <span className="text-xs text-[var(--text-muted)]">{label}</span>
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

  const barColor = exceeded ? 'var(--status-danger)' : near ? 'var(--status-warning)' : 'var(--accent)'
  const textColor = exceeded ? 'var(--status-danger)' : near ? 'var(--status-warning)' : undefined

  return (
    <div className="px-4 py-3 bg-[var(--bg-surface)]">
      <div className="flex items-center justify-between mb-1.5">
        <span className="text-xs text-[var(--text-tertiary)]">{label}</span>
        <span className="text-xs tabular-nums text-[var(--text-tertiary)]" style={textColor ? { color: textColor } : undefined}>
          {fmt(used)}{hasLimit ? ` / ${fmt(limit)}` : ''}
        </span>
      </div>
      {hasLimit && (
        <div
          className="h-1.5 rounded-full bg-[var(--bg-elevated)] overflow-hidden"
          role="progressbar"
          aria-label={`${label} usage`}
          aria-valuenow={Math.round(pct)}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <div
            className="h-full rounded-full transition-[width] duration-500"
            style={{ width: `${Math.max(2, pct)}%`, background: barColor }}
          />
        </div>
      )}
      {exceeded && (
        <p className="text-[11px] text-[var(--status-danger)] mt-1.5">
          Quota exceeded — upgrade your plan or free up space to keep syncing.
        </p>
      )}
      {near && (
        <p className="text-[11px] text-[var(--status-warning)] mt-1.5">
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
      <p className="text-xs text-[var(--text-faint)] mb-5 leading-relaxed">
        Your Vulos plan and how much of your cloud entitlement you’re using.
        Storage, backup, and relay are billed against your account tier.
      </p>

      {/* ── Plan card ───────────────────────────────────────────────────── */}
      <div className="rounded-xl border border-[var(--border-default)] overflow-hidden mb-5">
        <div className="flex items-start justify-between gap-4 px-4 py-4 bg-[var(--bg-surface)]">
          <div className="min-w-0">
            <div className="flex items-center gap-2 mb-1">
              <span className="text-base font-semibold text-[var(--text-primary)]">{tier.label}</span>
              <span
                className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded"
                style={{ background: 'color-mix(in srgb, var(--accent) 18%, transparent)', color: 'var(--accent)' }}
              >
                Current plan
              </span>
            </div>
            <p className="text-xs text-[var(--text-muted)] leading-relaxed">{tier.blurb}</p>
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
        <ul className="px-4 py-3 grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-1.5 border-t border-[var(--border-default)]">
          {tier.features.map(f => (
            <li key={f} className="flex items-center gap-2 text-xs text-[var(--text-tertiary)]">
              <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 shrink-0" style={{ color: 'var(--accent)' }} fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M3 8.5l3 3 7-7" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
              {f}
            </li>
          ))}
        </ul>
      </div>

      {/* ── Usage & quotas ──────────────────────────────────────────────── */}
      <h3 className="text-xs uppercase tracking-wider text-[var(--text-muted)] font-medium mb-2">Usage</h3>
      <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)] mb-2">
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

      <p className="text-[11px] text-[var(--text-faint)] mt-4 leading-relaxed">
        Billing and plan changes are managed in your Vulos Cloud account.
        Usage shown here reflects this device’s view and may lag the cloud by a
        few minutes.
      </p>
    </Section>
  )
}
