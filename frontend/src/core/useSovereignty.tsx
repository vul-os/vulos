/**
 * useSovereignty — the single source of truth for the shell's legible-trust
 * surface (Wave 10). Everything the sovereignty badge, the "what leaves this
 * box" egress indicator, and the transparency panel render is derived HERE from
 * REAL backend state — never a hardcoded claim:
 *
 *   • /api/assistant/status        → AI tier + honest label + full sovereignty
 *                                     block (provider/endpoint/model, external
 *                                     opt-in) + the tiers the operator may pick.
 *   • /api/auth/masterkey/envelope → whether this account holds a client-side
 *                                     master key (200) or is legacy (404). This
 *                                     is the at-rest / "your keys" posture.
 *
 * The provider polls status so the badge stays live if the operator flips the
 * tier from the Assistant, and exposes setTier() so the transparency panel can
 * offer the picker inline (POST /api/assistant/tier — the backend Guard stays
 * authoritative, so this only ever LABELS the posture, never weakens it).
 */
import { createContext, useContext, useState, useEffect, useCallback, useRef, type ReactNode } from 'react'
import { tierInfo, deriveEgress, type SovereigntyBlock, type SovereigntyEgress } from './sovereignty'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

export interface SovereigntyTierOption {
  tier: string
  label?: string
}

// AssistantStatus mirrors /api/assistant/status's response shape, narrowed
// from `unknown` at the fetch boundary rather than trusted wholesale.
export interface AssistantStatus {
  tier?: string
  sovereignty?: SovereigntyBlock
  tier_options?: SovereigntyTierOption[]
  label?: string
}

function toTierOption(x: unknown): SovereigntyTierOption | null {
  if (!isRecord(x) || typeof x.tier !== 'string') return null
  return { tier: x.tier, label: typeof x.label === 'string' ? x.label : undefined }
}

function toSovereigntyBlock(x: unknown): SovereigntyBlock | undefined {
  if (!isRecord(x)) return undefined
  return {
    reason: typeof x.reason === 'string' ? x.reason : undefined,
    external_allowed: typeof x.external_allowed === 'boolean' ? x.external_allowed : undefined,
    provider: typeof x.provider === 'string' ? x.provider : undefined,
    model: typeof x.model === 'string' ? x.model : undefined,
    endpoint: typeof x.endpoint === 'string' ? x.endpoint : undefined,
    tier: typeof x.tier === 'string' ? x.tier : undefined,
  }
}

function toAssistantStatus(x: unknown): AssistantStatus {
  if (!isRecord(x)) return {}
  return {
    tier: typeof x.tier === 'string' ? x.tier : undefined,
    sovereignty: toSovereigntyBlock(x.sovereignty),
    tier_options: Array.isArray(x.tier_options)
      ? x.tier_options.map(toTierOption).filter((o): o is SovereigntyTierOption => o !== null)
      : undefined,
    label: typeof x.label === 'string' ? x.label : undefined,
  }
}

export interface SovereigntyContextValue {
  status: AssistantStatus | null
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

const SovereigntyContext = createContext<SovereigntyContextValue | null>(null)

export function SovereigntyProvider({ children }: { children?: ReactNode }) {
  const [status, setStatus] = useState<AssistantStatus | null>(null)
  const [hasMasterKey, setHasMasterKey] = useState<boolean | null>(null) // null=unknown, true/false
  const [panelOpen, setPanelOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const mounted = useRef(true)

  const refresh = useCallback(async () => {
    try {
      const res = await fetch('/api/assistant/status', { credentials: 'include' })
      if (res.ok && mounted.current) setStatus(toAssistantStatus(await res.json()))
    } catch { /* keep last-known; the badge degrades gracefully */ }
  }, [])

  // Poll status so the badge tracks a tier change made from the Assistant app.
  useEffect(() => {
    mounted.current = true
    refresh()
    const id = setInterval(refresh, 30000)
    return () => { mounted.current = false; clearInterval(id) }
  }, [refresh])

  // At-rest / "your keys" posture: does this account hold a client-side master
  // key? 200 = yes (zero-access envelope), 404 = legacy account without one.
  useEffect(() => {
    let alive = true
    fetch('/api/auth/masterkey/envelope', { credentials: 'include' })
      .then(r => { if (alive) setHasMasterKey(r.status === 200 ? true : r.status === 404 ? false : null) })
      .catch(() => { if (alive) setHasMasterKey(null) })
    return () => { alive = false }
  }, [])

  const setTier = useCallback(async (tier: string) => {
    setBusy(true)
    try {
      const res = await fetch('/api/assistant/tier', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tier }),
      })
      if (res.ok) {
        const next = toAssistantStatus(await res.json())
        // /tier returns the same sovereignty shape but without tier_options-less
        // fields; merge so the picker keeps its options.
        setStatus(prev => ({ ...prev, ...next }))
      }
      return res.ok
    } catch { return false } finally { setBusy(false) }
  }, [])

  const tier = status?.tier || status?.sovereignty?.tier || null
  const sov = status?.sovereignty || null
  const egress = deriveEgress(sov, tier)

  const value = {
    status, sov, tier,
    tierOptions: status?.tier_options || null,
    label: status?.label || (tier ? tierInfo(tier).label : null),
    egress,
    hasMasterKey,
    externalArmed: !!sov?.external_allowed,
    busy,
    panelOpen,
    openPanel: () => setPanelOpen(true),
    closePanel: () => setPanelOpen(false),
    togglePanel: () => setPanelOpen(o => !o),
    refresh, setTier,
  }
  return <SovereigntyContext.Provider value={value}>{children}</SovereigntyContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useSovereignty(): SovereigntyContextValue {
  const ctx = useContext(SovereigntyContext)
  if (!ctx) throw new Error('useSovereignty must be used within <SovereigntyProvider>')
  return ctx
}
