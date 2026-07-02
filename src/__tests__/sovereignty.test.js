import { describe, it, expect } from 'vitest'
import { deriveEgress, tierInfo, TIERS } from '../core/sovereignty.js'

// deriveEgress is the "what leaves this box" logic behind the ambient egress
// indicator. It must be driven by REAL sovereignty state — green when nothing
// (or only the in-region no-train endpoint) leaves, amber when a brokered/
// external destination is enabled and NAMED. These assertions pin that mapping.
describe('deriveEgress', () => {
  it('local tier: nothing leaves the instance (green)', () => {
    const e = deriveEgress({ provider: 'ollama', model: 'llama3', local: true }, 'local')
    expect(e.level).toBe('none')
    expect(e.dest).toBeNull()
    expect(e.text).toMatch(/nothing leaves/i)
  })

  it('sovereign tier: in-region only, destination named (green)', () => {
    const e = deriveEgress({ provider: 'vulos', model: 'vulos-1' }, 'sovereign')
    expect(e.level).toBe('sovereign')
    expect(e.dest).toBe('vulos · vulos-1')
    expect(e.text).toMatch(/in-region/i)
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
