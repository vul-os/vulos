/**
 * sovereignty.js — pure, component-free helpers for the shell's legible-trust
 * surface (Wave 10). Kept separate from useSovereignty.jsx so the provider file
 * only exports React things (fast-refresh friendly) and this logic stays unit-
 * testable in isolation.
 */

// TIERS — the shared vocabulary with the backend Guard + llmux gateway, ordered
// most → least private. Kept in sync with backend TierLabel() and the Assistant.
export const TIERS = {
  local:     { dot: '#22c55e', tone: 'text-emerald-400', label: 'On your device',
               blurb: 'Inference runs on this box. Nothing leaves your server.' },
  sovereign: { dot: '#34d399', tone: 'text-emerald-400', label: 'Vulos sovereign · in-region, no-train',
               blurb: 'A Vulos-operated in-region endpoint inside the sovereignty boundary. No training on your data.' },
  brokered:  { dot: '#f59e0b', tone: 'text-amber-400', label: 'Brokered · no-train',
               blurb: 'A named third-party model under a no-train agreement. Requires the egress opt-in.' },
  external:  { dot: '#ef4444', tone: 'text-red-400', label: 'External · not private',
               blurb: 'An off-box endpoint that may mine or train on your data. Blocked unless explicitly authorized.' },
}

export const tierInfo = (tier) => TIERS[tier] || TIERS.external

// deriveEgress turns the sovereignty block into the ambient "what leaves this
// box" state. This is the "watch the network tab" claim made honest and
// continuous:
//   none      → tier local: literally nothing leaves the instance (green).
//   sovereign → tier sovereign: content may reach the Vulos in-region, no-train
//               endpoint only (green, but named — it IS off-box).
//   external  → brokered/external: off-box egress to a named destination (amber).
export function deriveEgress(sov, tier) {
  if (!sov) return { level: 'unknown', dest: null, text: 'Checking…' }
  const dest = [sov.provider, sov.model].filter(Boolean).join(' · ') ||
               destFromEndpoint(sov.endpoint) || 'external endpoint'
  if (tier === 'local') {
    return { level: 'none', dest: null, text: 'Nothing leaves your instance' }
  }
  if (tier === 'sovereign') {
    return { level: 'sovereign', dest, text: 'In-region only · no-train' }
  }
  // brokered / external — off-box egress to a named destination.
  return { level: 'external', dest, text: `Leaves to ${dest}` }
}

export function destFromEndpoint(endpoint) {
  if (!endpoint) return null
  try {
    const u = new URL(endpoint.includes('://') ? endpoint : `https://${endpoint}`)
    return u.hostname
  } catch { return endpoint }
}
