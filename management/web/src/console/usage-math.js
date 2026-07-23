/**
 * usage-math.js — pure, framework-free helpers for the Usage & billing page.
 *
 * Extracted from Usage.jsx so the billing math (usage percentages, quantity
 * formatting, and the transparent next-invoice overage estimate) can be unit
 * tested in the node test environment without pulling in React/JSX.
 *
 * These MUST stay honest: the overage rates mirror the published metered rates
 * (billingmodel/TIERS.md "extras" + internal/billing/metered_gate.go for the
 * opt-in suite overage) and the estimate is explicitly labelled an estimate in
 * the UI. Suite overage only bills when the account has enabled metered billing;
 * otherwise usage soft-caps at the plan allowance (no charge).
 */

/** Round to at most `dp` decimals, trimming trailing zeros; null-safe. */
export function trim(n, dp = 2) {
  if (n == null || Number.isNaN(n)) return '0'
  const r = Number(Number(n).toFixed(dp))
  return String(r)
}

/** Human unit formatting per dimension unit. */
export function fmtQuantity(value, unit) {
  const v = value ?? 0
  switch (unit) {
    case 'usd':      return `$${trim(v, 2)}`
    case 'gb':       return `${trim(v, v < 10 ? 2 : 1)} GB`
    case 'minutes':  return `${Math.round(v)} min`
    case 'boxes':    return `${Math.round(v)}`
    case 'sessions': return `${Math.round(v)}`
    default:         return trim(v, 2)
  }
}

/** Percentage used (0–100+), or null when there's no cap to measure against. */
export function pctUsed(used, cap) {
  if (!cap || cap <= 0) return null
  return (used / cap) * 100
}

/** Usage tone from percentage: good < 80 ≤ warn < 100 ≤ danger; null → faint. */
export function usageTone(pct) {
  if (pct == null) return 'faint'
  if (pct >= 100) return 'danger'
  if (pct >= 80)  return 'warn'
  return 'good'
}

/** Days until an ISO date (>=0), or null when unparseable. */
export function daysUntil(iso, now = Date.now()) {
  if (!iso) return null
  const ms = new Date(iso).getTime() - now
  if (Number.isNaN(ms)) return null
  return Math.max(0, Math.ceil(ms / 86_400_000))
}

/**
 * Overage unit prices (USD). Mirrors the published metered rates
 * (billingmodel/TIERS.md "extras" + internal/billing/metered_gate.go:
 * OverageStorageUSDCentsPerGB = $0.10/GB-mo, OverageRelayUSDCentsPerGB =
 * $0.05/GB). Only the dimensions that can bill overage above their included cap
 * appear here. These only bill if the account has enabled metered billing; the
 * estimate below is what WOULD be charged if it is on (the UI states this).
 */
export const OVERAGE = {
  storage: { unit: 'gb',      price: 0.10,    label: 'Storage over cap',        per: '/GB-mo' },
  relay:   { unit: 'gb',      price: 0.05,    label: 'Relay transfer',          per: '/GB' },
  llm:     { unit: 'usd',     price: 1.0,     label: 'AI (LLM) budget',         per: '' },
  // BOX-COMPUTE-UNIFY-01: hosted OS box compute is a metered dimension in the
  // SAME unified system as storage/relay/llm — no separate wallet. Rate
  // mirrors backend/cp/internal/billing/boxcompute.go's
  // BoxComputeUSDMicrosPerMinute (91 micros/min = $0.000091/min).
  box_compute: { unit: 'minutes', price: 0.000091, label: 'Hosted OS box compute', per: '/min' },
}

/**
 * estimateOverage — transparent, client-side next-invoice overage estimate from
 * the per-dimension usage summary. Returns { lines:[{label,amount,detail}], total }.
 * Dimensions with no overage (used ≤ cap) contribute nothing.
 */
export function estimateOverage(dimensions) {
  const lines = []
  let total = 0
  for (const d of dimensions ?? []) {
    const over = OVERAGE[d.key]
    if (!over) continue
    if (d.key === 'llm') {
      const overspend = Math.max(0, (d.used || 0) - (d.cap || 0))
      if (overspend > 0.005) {
        lines.push({ label: over.label, amount: overspend, detail: `$${trim(overspend, 2)} over $${trim(d.cap, 2)} budget` })
        total += overspend
      }
      continue
    }
    const overUnits = Math.max(0, (d.used || 0) - (d.cap || 0))
    if (overUnits > 0.0001) {
      const amount = overUnits * over.price
      lines.push({ label: over.label, amount, detail: `${trim(overUnits, 2)} ${String(d.unit).toUpperCase()} × $${over.price}${over.per}` })
      total += amount
    }
  }
  return { lines, total }
}
