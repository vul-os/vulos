// sovereigntyTypes.ts — a typed view of core/useSovereignty.jsx's context value.
//
// core/useSovereignty.jsx (untyped, out of scope for this pass) creates its
// React context via `createContext(null)` with no type parameter, so the
// context's value type is the literal `null`. Consumed from a *checked*
// .tsx file, TS can statically prove useSovereignty()'s `if (!ctx) throw ...`
// guard always fires, and infers the hook's return type as `never` — not
// useful to three different shell files (TrustBadge, TransparencyPanel,
// CommandPalette) that each read a real slice of the live object it actually
// returns. Centralised here (instead of three drifting copies) so the shape
// is declared once, from what useSovereignty.jsx's `value` object at the
// bottom of that file really assembles.
import { useSovereignty as useSovereigntyUntyped } from '../core/useSovereignty'

export interface SovereigntyEgress {
  level: 'unknown' | 'none' | 'sovereign' | 'external'
  dest: string | null
  text: string
}

export interface SovereigntyTierOption {
  tier: string
  label?: string
}

export interface SovereigntyBlock {
  reason?: string
  external_allowed?: boolean
  provider?: string
  model?: string
  endpoint?: string
}

export interface SovereigntyShape {
  status: unknown
  sov: SovereigntyBlock | null
  tier: string | null
  tierOptions: SovereigntyTierOption[] | null
  label: string | null
  egress: SovereigntyEgress
  hasMasterKey: boolean | null
  externalArmed: boolean
  busy: boolean
  panelOpen: boolean
  openPanel: () => void
  closePanel: () => void
  togglePanel: () => void
  refresh: () => void
  setTier: (tier: string) => Promise<boolean>
}

export function useSovereignty(): SovereigntyShape {
  return useSovereigntyUntyped() as unknown as SovereigntyShape
}
