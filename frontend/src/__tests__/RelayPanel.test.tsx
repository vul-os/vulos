import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import RelayPanel, { DEFAULT_PROVIDER } from '../core/settings/RelayPanel'

// The Settings → Relay panel used to claim, in three separate places, that
// POST /api/relayconfig/reset reverts to "ephor":
//
//   - the button read "Reset to ephor"
//   - the success message read "Reverted to the ephor default."
//   - the button was gated on `config?.provider !== 'ephor'`
//
// It does not. relayconfig.DefaultConfig() returns ProviderVulos; the endpoint
// calls ResetToEphor(), which is a deprecated alias for ResetToDefault(). So the
// UI named the wrong provider and, worse, HID the reset button whenever the box
// was on 'ephor' — the one configuration where someone would reach for it —
// while offering it on 'vulos', where it does nothing.
//
// This is not fallout from the ephor→Pier rename: the dropdown label already
// said "Pier relay". The UI was describing an action the backend never performs,
// which is the failure mode this project keeps paying for.

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

function mockConfig(provider: string) {
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    const u = String(url)
    if (u.includes('/api/relayconfig')) {
      return Promise.resolve({
        ok: true, status: 200,
        json: () => Promise.resolve({ config: { provider }, effective: { provider } }),
      })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  }))
}

describe('RelayPanel — the reset button tells the truth about what reset does', () => {
  // The cross-language pin. A frontend-only assertion would happily agree with
  // itself forever; the defect was that the UI and the Go default disagreed, so
  // the test has to read the Go default. Parsing DefaultConfig's body rather
  // than grepping the file for "vulos" matters: the string appears in several
  // unrelated places in that file, and a grep would pass on any of them.
  it('DEFAULT_PROVIDER matches what relayconfig.DefaultConfig() actually returns', () => {
    const go = readFileSync(
      resolve(__dirname, '../../../backend/services/relayconfig/relayconfig.go'),
      'utf8',
    )
    const body = go.match(/func DefaultConfig\(\) Config \{([\s\S]*?)\n\}/)
    expect(body, 'DefaultConfig() not found — did relayconfig.go move or get renamed?').toBeTruthy()

    const provider = body![1].match(/Provider:\s*Provider(\w+)/)
    expect(provider, 'DefaultConfig() no longer sets Provider: Provider<Name>').toBeTruthy()

    expect(provider![1].toLowerCase()).toBe(DEFAULT_PROVIDER)
  })

  it('offers the reset when the box is on a NON-default provider', async () => {
    mockConfig('ephor')
    render(<RelayPanel />)
    const btn = await screen.findByRole('button', { name: /reset to the vulos relay/i })
    expect(btn).toBeTruthy()
  })

  it('hides the reset when the box is already on the default provider', async () => {
    mockConfig(DEFAULT_PROVIDER)
    render(<RelayPanel />)
    // Wait for the config to land before asserting an ABSENCE — otherwise this
    // passes on the initial render and proves nothing about the gate.
    await waitFor(() => expect(screen.getByRole('button', { name: /^save$/i })).toBeTruthy())
    expect(screen.queryByRole('button', { name: /reset to/i })).toBeNull()
  })

  it('never offers to reset to ephor anywhere in the panel', async () => {
    mockConfig('turn')
    const { container } = render(<RelayPanel />)
    await screen.findByRole('button', { name: /reset to the vulos relay/i })
    // 'ephor' survives as a persisted provider VALUE (existing boxes have it
    // stored), so it is fine in an <option value>; it must not appear in prose
    // the user reads.
    expect(container.textContent?.toLowerCase()).not.toContain('ephor')
  })
})
