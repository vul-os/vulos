/**
 * status-format.js — pure, framework-free helpers for the cloud status surfaces.
 *
 * Kept separate from the JSX so the classification + formatting logic is unit-
 * testable in the node vitest environment (no DOM), matching this repo's
 * convention (see pricing.js / calculator.js and their *.test.js).
 *
 * Consumed by:
 *   - src/pages/Status.jsx            (public cloud-service health)
 *   - src/pages/app/AccountStatus.jsx (authenticated per-account status)
 */

/** Status vocabulary shared by both surfaces. */
export const STATUS_META = {
  operational: { label: 'Operational', pill: 'good', tone: 'var(--good)' },
  degraded: { label: 'Degraded', pill: 'warn', tone: 'var(--warn)' },
  down: { label: 'Down', pill: 'danger', tone: 'var(--danger)' },
  unknown: { label: 'Unknown', pill: 'faint', tone: 'var(--text-faint)' },
}

export function statusMeta(s) {
  return STATUS_META[s] || STATUS_META.unknown
}

/** Headline shown for the overall roll-up. */
export const OVERALL_HEADLINE = {
  operational: 'All cloud services operational',
  degraded: 'Some cloud services degraded',
  down: 'Cloud service disruption',
  unknown: 'Status unavailable',
}

export function overallHeadline(overall) {
  return OVERALL_HEADLINE[overall] || OVERALL_HEADLINE.unknown
}

/**
 * worstStatus folds a list of component statuses into a single roll-up, treating
 * "unknown" as no-worse-than operational (a not-yet-configured component is not
 * an outage) — mirrors the backend `overall()` so the UI and API agree even if
 * the API's `overall` field is ever missing.
 */
export function worstStatus(statuses) {
  const rank = { operational: 0, unknown: 1, degraded: 2, down: 3 }
  let worst = 0
  for (const s of statuses || []) {
    const r = rank[s]
    if (r != null && r > worst) worst = r
  }
  return ['operational', 'operational', 'degraded', 'down'][worst]
}

/** Format an ISO timestamp for compact display; '—' when absent/invalid. */
export function fmtTime(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

/** Relative "3m ago" style time; '—' when absent/invalid. */
export function relativeTime(iso, now = Date.now()) {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '—'
  const diffMin = Math.floor((now - t) / 60000)
  if (diffMin < 1) return 'just now'
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24) return `${diffHr}h ago`
  return `${Math.floor(diffHr / 24)}d ago`
}

/** Human-readable bytes (binary units). */
export function fmtBytes(n) {
  if (n == null || Number.isNaN(n)) return '—'
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB', 'PB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  // One decimal for non-integers, otherwise a clean whole number.
  const s = Number.isInteger(v) ? String(v) : v.toFixed(1)
  return `${s} ${units[i]}`
}

/** Map the account relay health enum → a status token for the shared vocabulary. */
export function relayHealthStatus(h) {
  switch (h) {
    case 'ok':
      return 'operational'
    case 'degraded':
      return 'degraded'
    default:
      return 'unknown'
  }
}
