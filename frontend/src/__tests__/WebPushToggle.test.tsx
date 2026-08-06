// WebPushToggle.test.jsx — the settings-panel opt-in surface for Web Push.
//
// Covers probeState's decision table (unsupported / unconfigured / denied / on /
// off) and the component's honest non-actionable states, so a browser without
// push, or a box without VAPID keys, renders an explanatory row rather than a
// dead switch.

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import WebPushToggle from '../core/notifiers/WebPushToggle'
import { probeState } from '../core/notifiers/webPushProbe'

const PUBKEY = 'BPabc-DEF_ghi'

function setSupport({
  perm = 'granted',
  subscription = null,
}: { perm?: NotificationPermission; subscription?: { endpoint: string } | null } = {}) {
  globalThis.navigator = globalThis.navigator || {}
  Object.defineProperty(globalThis.navigator, 'serviceWorker', {
    value: {
      ready: Promise.resolve({
        pushManager: { getSubscription: async () => subscription },
      }),
    },
    configurable: true,
  })
  Object.defineProperty(globalThis, 'PushManager', { value: function () {}, configurable: true })
  Object.defineProperty(globalThis, 'Notification', {
    value: { permission: perm, requestPermission: async () => perm },
    configurable: true,
  })
}

function unsetSupport() {
  Reflect.deleteProperty(globalThis, 'PushManager')
  Reflect.deleteProperty(globalThis, 'Notification')
}

// Real Response instances so these mocks structurally satisfy WebPushDeps'
// `fetch?: typeof fetch` (which returns a real Response, not a partial shape).
function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), { status: 200, ...init })
}

const okVapid = (enabled: boolean) => async () =>
  jsonResponse({ enabled, publicKey: enabled ? PUBKEY : undefined })

beforeEach(() => setSupport())
afterEach(() => { unsetSupport() })

describe('probeState', () => {
  it('unsupported when the browser lacks Web Push APIs', async () => {
    unsetSupport()
    expect(await probeState({ fetch: async () => jsonResponse({}) })).toBe('unsupported')
  })

  it('unconfigured when the box reports push disabled', async () => {
    expect(await probeState({ fetch: okVapid(false) })).toBe('unconfigured')
  })

  it('unconfigured when the vapid endpoint errors', async () => {
    expect(await probeState({ fetch: async () => new Response(null, { status: 500 }) })).toBe('unconfigured')
  })

  it('denied when notifications are blocked at the browser level', async () => {
    setSupport({ perm: 'denied' })
    expect(await probeState({ fetch: okVapid(true) })).toBe('denied')
  })

  it('off when configured but this device has no subscription', async () => {
    setSupport({ perm: 'granted', subscription: null })
    expect(await probeState({ fetch: okVapid(true) })).toBe('off')
  })

  it('on when this device already holds a subscription', async () => {
    setSupport({ perm: 'granted', subscription: { endpoint: 'https://push.example/d1' } })
    expect(await probeState({ fetch: okVapid(true) })).toBe('on')
  })
})

describe('<WebPushToggle />', () => {
  it('renders an explanatory row (no live switch) when unavailable on the box', async () => {
    const deps = { fetch: okVapid(false) }
    render(<WebPushToggle deps={deps} />)
    await waitFor(() =>
      expect(screen.getByText(/hasn’t enabled the Web Push send-path/i)).toBeInTheDocument()
    )
    // Non-actionable: no switch role is rendered.
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
  })

  it('renders a live switch (off) when configured but not subscribed', async () => {
    const deps = { fetch: okVapid(true) }
    render(<WebPushToggle deps={deps} />)
    const sw = await screen.findByRole('switch', { name: /push notifications on this device/i })
    expect(sw).toHaveAttribute('aria-checked', 'false')
  })
})
