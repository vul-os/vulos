/**
 * box-math.js — pure, framework-free helpers for the Hosted OS box console and
 * the Invoice history page.
 *
 * Extracted so the money/usage math (ZAR-cents formatting, byte→GB, minute→hour,
 * USD formatting, status derivation) can be unit tested in the node environment
 * without React/JSX. These MUST stay honest: amounts come from the CP's
 * unified billing store (subscription_events amounts in ZAR, box_compute
 * usage/cost in USD via GET /api/box/billing — BOX-COMPUTE-UNIFY-01, no
 * separate wallet any more) — this module only FORMATS them, it never
 * fabricates a figure.
 */

/** Round to at most `dp` decimals, trimming trailing zeros; null-safe. */
export function trim(n, dp = 2) {
  if (n == null || Number.isNaN(Number(n))) return '0'
  const r = Number(Number(n).toFixed(dp))
  return String(r)
}

/**
 * Format an integer ZAR-cents amount as "R123.45". This is the single source of
 * truth for rendering money: every cents figure from the backend (wallet
 * balance, top-ups, invoice amounts) passes through here so a rounding rule is
 * applied consistently. Null/NaN → "R0.00".
 */
export function fmtZARCents(cents) {
  const c = Math.round(Number(cents) || 0)
  const rand = c / 100
  return `R${rand.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
}

/** Bytes → GB string ("1.24 GB"); null-safe. Uses 1e9 (decimal GB, matching the plan caps). */
export function fmtBytes(bytes) {
  const b = Number(bytes) || 0
  if (b <= 0) return '0 GB'
  const gb = b / 1e9
  if (gb < 0.01) {
    const mb = b / 1e6
    return `${trim(mb, mb < 10 ? 1 : 0)} MB`
  }
  return `${trim(gb, gb < 10 ? 2 : 1)} GB`
}

/** Minutes → human duration ("3h 20m" / "45m"); null-safe. */
export function fmtMinutes(minutes) {
  const m = Math.max(0, Math.round(Number(minutes) || 0))
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  const rem = m % 60
  return rem === 0 ? `${h}h` : `${h}h ${rem}m`
}

/**
 * Derive a customer-facing box status descriptor from the /api/box status
 * response. Returns { label, tone } where tone maps to a Pill color.
 * The backend `state` (running|suspended|stopped|destroyed|...) is authoritative;
 * `billing_state` (active|grace|suspended) refines the customer message.
 */
export function boxStatus(box) {
  if (!box) return { label: 'No box', tone: 'faint' }
  const state = box.state || ''
  const billing = box.billing_state || ''

  if (state === 'destroyed') return { label: 'Destroyed', tone: 'faint' }
  if (billing === 'suspended' || state === 'suspended') {
    return { label: 'Suspended', tone: 'danger' }
  }
  if (billing === 'grace') return { label: 'Grace period', tone: 'warn' }
  if (state === 'running') return { label: 'Running', tone: 'good' }
  if (state === 'stopped' || state === 'sleeping') return { label: 'Sleeping', tone: 'warn' }
  if (state === 'provisioning' || state === 'pending') return { label: 'Provisioning', tone: 'accent' }
  return { label: state || 'Unknown', tone: 'faint' }
}

/**
 * Format a USD number as "$1.23"; null-safe. Used for box_compute cost figures
 * from GET /api/box/billing (compute_cost_usd_this_month).
 */
export function fmtUSD(usd) {
  const v = Number(usd) || 0
  return `$${v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
}

/**
 * Billing-readiness tone for the box console's Billing card
 * (BOX-COMPUTE-UNIFY-01): danger when no card on file (box will fail to
 * provision/re-provision), warn when a card is on file but metered billing is
 * not yet enabled (same fail-closed gate), good once both are true.
 */
export function boxBillingTone(meteredEnabled, cardOnFile) {
  if (!cardOnFile) return 'danger'
  if (!meteredEnabled) return 'warn'
  return 'good'
}

/** Map a status string ("paid"|"failed"|"issued"|...) to a Pill tone. */
export function invoiceStatusTone(status) {
  switch (status) {
    case 'paid':   return 'good'
    case 'failed': return 'danger'
    case 'issued': return 'warn'
    default:       return 'faint'
  }
}

/** Format an ISO date as a short human date; null-safe. */
export function fmtDate(iso) {
  if (!iso) return '—'
  try {
    return new Date(iso).toLocaleDateString(undefined, {
      year: 'numeric', month: 'short', day: 'numeric',
    })
  } catch { return '—' }
}

