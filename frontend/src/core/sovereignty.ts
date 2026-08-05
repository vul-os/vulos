/**
 * sovereignty.js — pure, component-free helpers for the shell's legible-trust
 * surface (Wave 10). Kept separate from useSovereignty.jsx so the provider file
 * only exports React things (fast-refresh friendly) and this logic stays unit-
 * testable in isolation.
 */

export interface SovereigntyTier {
  dot: string
  tone: string
  label: string
  blurb: string
}

// The sovereignty block as narrowed off /api/assistant/status (a trust
// boundary — see useSovereignty.tsx's toAssistantStatus). Mirrors
// shell/sovereigntyTypes.ts's SovereigntyBlock (that file is a read-only,
// out-of-scope view of this one — see the comment at its top).
export interface SovereigntyBlock {
  reason?: string
  external_allowed?: boolean
  provider?: string
  model?: string
  endpoint?: string
  tier?: string
}

export interface SovereigntyEgress {
  level: 'unknown' | 'none' | 'sovereign' | 'external'
  dest: string | null
  text: string
}

// TIERS — the shared vocabulary with the backend Guard + llmux gateway, ordered
// most → least private. Kept in sync with backend TierLabel() and the Assistant.
export const TIERS: Record<string, SovereigntyTier> = {
  local:     { dot: '#22c55e', tone: 'text-emerald-400', label: 'On your device',
               blurb: 'Inference runs on this box. Nothing leaves your server.' },
  sovereign: { dot: '#eab308', tone: 'text-yellow-400', label: 'Operator-declared endpoint (unverified)',
               blurb: 'An off-box endpoint the operator declared as in-region / no-train. Vulos does not operate or verify it — treat it as off-box egress you have chosen to trust.' },
  brokered:  { dot: '#f59e0b', tone: 'text-amber-400', label: 'Brokered · no-train',
               blurb: 'A named third-party model under a no-train agreement. Requires the egress opt-in.' },
  external:  { dot: '#ef4444', tone: 'text-red-400', label: 'External · not private',
               blurb: 'An off-box endpoint that may mine or train on your data. Blocked unless explicitly authorized.' },
}

export const tierInfo = (tier: string | null | undefined): SovereigntyTier => TIERS[tier ?? ''] || TIERS.external

// deriveEgress turns the sovereignty block into the ambient "what leaves this
// box" state. This is the "watch the network tab" claim made honest and
// continuous:
//   none      → tier local: literally nothing leaves the instance (green).
//   sovereign → tier sovereign: content may reach an OPERATOR-DECLARED off-box
//               endpoint (unverified by Vulos). It IS off-box egress — treated as
//               caution, NOT painted safe-green like the genuinely-local tier.
//   external  → brokered/external: off-box egress to a named destination (amber).
export function deriveEgress(sov: SovereigntyBlock | null | undefined, tier: string | null | undefined): SovereigntyEgress {
  if (!sov) return { level: 'unknown', dest: null, text: 'Checking…' }
  const dest = [sov.provider, sov.model].filter(Boolean).join(' · ') ||
               destFromEndpoint(sov.endpoint) || 'external endpoint'
  if (tier === 'local') {
    return { level: 'none', dest: null, text: 'Nothing leaves your instance' }
  }
  if (tier === 'sovereign') {
    return { level: 'sovereign', dest, text: 'Off-box to a declared endpoint' }
  }
  // brokered / external — off-box egress to a named destination.
  return { level: 'external', dest, text: `Leaves to ${dest}` }
}

export function destFromEndpoint(endpoint: string | null | undefined): string | null {
  if (!endpoint) return null
  try {
    const u = new URL(endpoint.includes('://') ? endpoint : `https://${endpoint}`)
    return u.hostname
  } catch { return endpoint }
}
