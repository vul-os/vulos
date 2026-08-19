import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent } from '@testing-library/react'
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

// ---------------------------------------------------------------------------
// The TURN credential round trip.
//
// GET /api/relayconfig serves relayconfig.PublicView, which carries
// has_credential and NEVER the credential itself — the secret is write-only.
// So the panel renders a saved TURN server with an empty credential box, and
// this used to save that box as `credential: undefined`, i.e. an absent field.
// POST /api/relayconfig hands the whole Config to relayconfig.Set, which
// overwrites. Opening Settings, changing anything else, and pressing Save
// therefore DESTROYED the stored TURN credential — and the panel answered
// "Relay configuration saved."
//
// TURN is the fallback path for call media when a direct connection fails, so
// nothing looks wrong until later, when calls stop connecting for whoever
// needed the relay path — nowhere near the Save button that caused it.
//
// Both directions are pinned. Testing preservation alone would let "never send
// anything" ship, and the owner could then never remove a credential: the same
// data bug pointing the other way.
// ---------------------------------------------------------------------------

vi.mock('../lib/stepup', () => ({ requireStepUp: vi.fn(() => Promise.resolve()) }))

interface PostedIceServer {
  urls: string[]
  username?: string
  credential?: string
  clear_credential?: boolean
}
interface PostedPayload {
  provider: string
  turn: { ice_servers: PostedIceServer[] }
  libp2p: { relay_peers: string[] }
  wireguard: { endpoint: string; network?: string }
}

// mockRelayBackend serves a config the way the real backend does — a TURN
// server with has_credential and no credential — and records what gets POSTed.
function mockRelayBackend(config: Record<string, unknown>): { posts: PostedPayload[] } {
  const posts: PostedPayload[] = []
  vi.stubGlobal('fetch', vi.fn((url: string, opts?: RequestInit) => {
    const u = String(url)
    if (u.includes('/api/relayconfig') && opts?.method === 'POST') {
      posts.push(JSON.parse(String(opts.body)) as PostedPayload)
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(config) })
    }
    if (u.includes('/api/relayconfig')) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ config, effective: {} }) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  }))
  return { posts }
}

// A box with one saved TURN server that HAS a credential, plus previously
// entered libp2p and wireguard settings.
const savedTurnConfig = {
  provider: 'turn',
  turn: { ice_servers: [{ urls: ['turn:relay.example.org:3478'], username: 'alice', has_credential: true }] },
  libp2p: { relay_peers: ['/dns4/relay.example.org/tcp/4001/p2p/12D3KooWtest'] },
  wireguard: { endpoint: 'headscale.example.org:8080', network: 'my-tailnet' },
}

async function renderAndSave(config: Record<string, unknown>, before?: () => Promise<void> | void) {
  const { posts } = mockRelayBackend(config)
  render(<RelayPanel />)
  await screen.findByDisplayValue('turn:relay.example.org:3478')
  if (before) await before()
  fireEvent.click(screen.getByRole('button', { name: /^save$/i }))
  await waitFor(() => expect(posts.length).toBe(1))
  return posts[0]
}

describe('RelayPanel — a saved TURN credential survives a save that did not touch it', () => {
  // DIRECTION 1, the panel's half of it. Preservation ITSELF is the box's job
  // and is proved in Go (relayconfig.TestSet_OmittedCredential_
  // PreservesStoredCredential): `credential: undefined` and an omitted key
  // serialise to the same bytes, so no assertion here can tell the old code
  // from the new one — restoring `credential: trim() || undefined` is an
  // equivalent mutant, which is precisely why the fix belongs on the box.
  //
  // What this test does defend is the panel's two remaining ways to destroy
  // the secret from here: asking for a removal nobody requested, and sending
  // the empty box as a value rather than as silence. Both mutations redden it.
  it('omits the credential entirely when the box was never touched', async () => {
    const body = await renderAndSave(savedTurnConfig)
    const server = body.turn.ice_servers[0]
    expect(server.urls).toEqual(['turn:relay.example.org:3478'])
    // `in`, not a truthiness check: `credential: ''` and `credential: undefined`
    // both read as falsy while still being the destructive payload on the wire.
    expect('credential' in server, 'an untouched credential box must not be sent at all').toBe(false)
    expect('clear_credential' in server, 'an untouched credential box is not a removal').toBe(false)
  })

  // DIRECTION 2. Removal must stay possible, and must be explicit.
  it('sends an explicit clear_credential when the owner removes the credential', async () => {
    const body = await renderAndSave(savedTurnConfig, () => {
      fireEvent.click(screen.getByRole('button', { name: /remove credential/i }))
    })
    const server = body.turn.ice_servers[0]
    expect(server.clear_credential, 'removing must send the explicit flag').toBe(true)
    expect('credential' in server, 'a removal must not also carry a credential').toBe(false)
  })

  it('sends the new credential when one is typed, and never both', async () => {
    const body = await renderAndSave(savedTurnConfig, () => {
      fireEvent.change(screen.getByLabelText(/credential for server 1/i), { target: { value: 'rotated' } })
    })
    const server = body.turn.ice_servers[0]
    expect(server.credential).toBe('rotated')
    expect('clear_credential' in server).toBe(false)
  })

  it('offers a way back after asking to remove — the removal only happens on save', async () => {
    const body = await renderAndSave(savedTurnConfig, () => {
      fireEvent.click(screen.getByRole('button', { name: /remove credential/i }))
      fireEvent.click(screen.getByRole('button', { name: /keep the saved credential/i }))
    })
    const server = body.turn.ice_servers[0]
    expect('clear_credential' in server).toBe(false)
    expect('credential' in server).toBe(false)
  })

  // The box matches a stored credential by URL set + username, because a
  // secret must never follow an edited address onto a different host. That is
  // the right rule, and it means editing the address silently drops the
  // credential unless the owner is told.
  it('warns that a saved credential will not follow an edited address', async () => {
    mockRelayBackend(savedTurnConfig)
    render(<RelayPanel />)
    const urls = await screen.findByDisplayValue('turn:relay.example.org:3478')
    expect(screen.queryByText(/will not carry over/i), 'no warning before anything is edited').toBeNull()
    fireEvent.change(urls, { target: { value: 'turn:somewhere-else.example.net:3478' } })
    expect(screen.getByText(/will not carry over/i)).toBeTruthy()
  })

  it('does not warn when the same server’s URLs are merely reordered', async () => {
    const twoUrls = {
      ...savedTurnConfig,
      turn: { ice_servers: [{ urls: ['turn:a.example.org:3478', 'turn:b.example.org:3478'], username: 'alice', has_credential: true }] },
    }
    mockRelayBackend(twoUrls)
    render(<RelayPanel />)
    const urls = await screen.findByDisplayValue('turn:a.example.org:3478, turn:b.example.org:3478')
    fireEvent.change(urls, { target: { value: 'turn:b.example.org:3478, turn:a.example.org:3478' } })
    expect(screen.queryByText(/will not carry over/i)).toBeNull()
  })
})

describe('RelayPanel — a save never drops the sections it is not editing', () => {
  // POST /api/relayconfig overwrites the WHOLE config, so a section left out
  // of the body is a section deleted from the box. The panel used to send only
  // the section matching the selected provider, so switching to TURN destroyed
  // the saved libp2p peers and WireGuard endpoint — while relayconfig's own
  // package doc promises "switching providers back and forth never loses
  // previously-entered settings".
  it('sends every section, not just the selected provider’s', async () => {
    const body = await renderAndSave(savedTurnConfig)
    expect(body.provider).toBe('turn')
    expect(body.libp2p.relay_peers, 'saved libp2p peers were dropped by a TURN save')
      .toEqual(['/dns4/relay.example.org/tcp/4001/p2p/12D3KooWtest'])
    expect(body.wireguard.endpoint, 'a saved WireGuard endpoint was dropped by a TURN save')
      .toBe('headscale.example.org:8080')
    expect(body.wireguard.network).toBe('my-tailnet')
  })
})

describe('RelayPanel — the WireGuard copy does not promise what the provider does not do', () => {
  // The panel said "Reach this box over your own mesh instead of the relay
  // tunnel". wireguardProvider is report-only: Ingress() records the
  // coordinator without re-routing HTTP, and ResolvePeer returns ("", false).
  // Read the Go source rather than asserting the UI against itself — the
  // defect is a DISAGREEMENT between the two, so a frontend-only assertion
  // would agree with a lie forever.
  const providersGo = () => readFileSync(
    resolve(__dirname, '../../../backend/services/relayconfig/providers.go'),
    'utf8',
  )

  it('the Go provider still resolves no peers and claims ingress only', () => {
    const go = providersGo()
    expect(go, 'wireguardProvider.ResolvePeer no longer returns not-ok — if peer resolution is ' +
      'real now, the panel copy below should say so')
      .toMatch(/func \(wireguardProvider\) ResolvePeer\(context\.Context, string\) \(string, bool\) \{ return "", false \}/)
    expect(go, 'wireguardProvider.Capabilities changed — re-check what the panel claims')
      .toMatch(/func \(wireguardProvider\) Capabilities\(\) Facet\s+\{ return FacetIngress \}/)
  })

  it('the provider blurb says report-only instead of claiming the mesh is the ingress path', async () => {
    mockRelayBackend({ provider: 'vulos' })
    const { container } = render(<RelayPanel />)
    await screen.findByRole('button', { name: /^save$/i })
    const text = container.textContent || ''
    expect(text).toMatch(/report-only/i)
    expect(text, 'the blurb still claims the mesh reaches this box').not.toMatch(/Reach this box over your own mesh/i)
  })

  it('the WireGuard panel note discloses BOTH limits, like the libp2p one', async () => {
    mockRelayBackend({ provider: 'wireguard', wireguard: { endpoint: 'headscale.example.org:8080' } })
    const { container } = render(<RelayPanel />)
    await screen.findByDisplayValue('headscale.example.org:8080')
    const text = container.textContent || ''
    // Ingress alone was the old, half-true note: the provider does not resolve
    // peers either, and the backend's own ingress detail says so.
    expect(text).toMatch(/does not re-route real box ingress or resolve peers/i)
  })
})
