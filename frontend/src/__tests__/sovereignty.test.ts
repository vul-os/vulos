import { describe, it, expect } from 'vitest'
import { deriveEgress, tierInfo, TIERS } from '../core/sovereignty.js'

// deriveEgress is the "what leaves this box" logic behind the ambient egress
// indicator. It must be driven by REAL sovereignty state — green ONLY when
// nothing leaves; the sovereign tier is off-box egress to an operator-declared
// (unverified) endpoint, so it is a distinct "caution" state, and brokered/
// external are amber and NAMED. These assertions pin that mapping.
describe('deriveEgress', () => {
  it('local tier: nothing leaves the instance (green)', () => {
    const e = deriveEgress({ provider: 'ollama', model: 'llama3' }, 'local')
    expect(e.level).toBe('none')
    expect(e.dest).toBeNull()
    expect(e.text).toMatch(/nothing leaves/i)
  })

  it('sovereign tier: off-box to a declared (unverified) endpoint, destination named', () => {
    const e = deriveEgress({ provider: 'vulos', model: 'vulos-1' }, 'sovereign')
    expect(e.level).toBe('sovereign')
    expect(e.dest).toBe('vulos · vulos-1')
    expect(e.text).toMatch(/off-box|declared/i)
    // Honest label: no "Vulos-operated" / "no-train guarantee" overclaim.
    expect(e.text).not.toMatch(/in-region|no-train/i)
  })

  it('brokered/external tier: egress off-box, destination named (amber)', () => {
    const e = deriveEgress({ provider: 'anthropic', model: 'claude' }, 'brokered')
    expect(e.level).toBe('external')
    expect(e.dest).toBe('anthropic · claude')
    expect(e.text).toContain('anthropic · claude')
  })

  it('falls back to the endpoint host when no provider/model is present', () => {
    const e = deriveEgress({ endpoint: 'https://api.example.com/v1' }, 'external')
    expect(e.dest).toBe('api.example.com')
  })

  it('no sovereignty block yet: unknown, no false green/amber claim', () => {
    const e = deriveEgress(null, null)
    expect(e.level).toBe('unknown')
  })
})

describe('tierInfo', () => {
  it('maps each tier to its honest label + fails closed to external', () => {
    expect(tierInfo('local')).toBe(TIERS.local)
    expect(tierInfo('sovereign')).toBe(TIERS.sovereign)
    expect(tierInfo('nonsense')).toBe(TIERS.external)
  })
})
