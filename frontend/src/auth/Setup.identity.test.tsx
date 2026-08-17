// Setup.identity.test.tsx — the first-boot wizard's "name this box" step.
//
// Three MEASURED backend defects converge on this one screen, and the UI is
// the last place each of them can be reintroduced:
//
//  1. TWO BOXES COLLIDE. Every box defaulted to the bare name "vulos", and
//     this field was left EMPTY when the box had no chosen name — an empty
//     field made the step skip its POST entirely, so the shared default stood.
//     Two boxes on one LAN then answered the same mDNS query: measured as a
//     coin flip (10 lookups split 8/2 between two boxes), with TLS SUCCEEDING
//     on the wrong box because both certificates carried that name.
//
//  2. A RENAME THAT REPORTS SUCCESS AND CHANGES NOTHING. The endpoint answers
//     200 with applied_live:false when it saved the name but could not apply
//     it to the running system. The step used to advance on any 200.
//
//  3. A COLLISION NOBODY IS TOLD ABOUT. avahi silently renames the losing box
//     to vulos-2 hours later — a name that is in no certificate.
//
// A test that only asserts "the field renders" would have passed throughout.

import { useState } from 'react'
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

// NavBar pulls in useI18n, which throws outside an I18nProvider. Mocked rather
// than wrapped so the test exercises the step, not the translation stack.
vi.mock('../core/i18n', () => ({
  useI18n: () => ({ t: (k: string) => k, setLocale: () => {}, locale: 'en' }),
}))

import { IS05_IdentityStep } from './Setup'

const baseConfig = { IS05_ulid: '', IS05_hostname: '' }

// A STATEFUL harness, because the real wizard is stateful.
//
// The step's input is controlled (value={config.IS05_hostname}), so React fires
// no onChange at all when the fired value equals the current one — with a
// static config prop, "typing" the name that was already there is a no-op and
// the step never learns the field was edited. Mirroring the real parent's
// update() is the only way the test exercises the same code path a user does.
function Harness({
  initialHostname = '',
  onNext,
}: { initialHostname?: string; onNext: () => void }) {
  const [hostname, setHostname] = useState(initialHostname)
  const update = (k: string, v: unknown) => {
    if (k === 'IS05_hostname') setHostname(String(v))
  }
  return (
    <IS05_IdentityStep
      config={{ ...baseConfig, IS05_hostname: hostname } as never}
      update={update as never}
      onNext={onNext}
      onPrev={() => {}}
    />
  )
}

function renderStep(overrides: Record<string, unknown> = {}) {
  const update = vi.fn()
  const onNext = vi.fn()
  const config = { ...baseConfig, ...overrides }
  const utils = render(
    <IS05_IdentityStep
      config={config as never}
      update={update as never}
      onNext={onNext}
      onPrev={vi.fn()}
    />,
  )
  return { update, onNext, ...utils }
}

// Type a name into the step the way a user would: a real edit, to a value that
// differs from what is in the field.
function typeHostname(value: string) {
  fireEvent.change(screen.getByLabelText(/hostname/i), { target: { value } })
}

function jsonResponse(body: unknown, ok = true, status = 200) {
  return { ok, status, json: async () => body }
}

describe('IS05 identity step', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
  })
  afterEach(() => {
    vi.useRealTimers()
    cleanup()
    vi.restoreAllMocks()
  })

  it('prefills the box\'s UNIQUE per-instance name, never the shared "vulos"', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({
      ulid: '01HZZZZZZZZZZZZZZZZZK3N7Q2',
      hostname: '',
      default_hostname: 'vulos-k3n7q2',
    })))

    const { update } = renderStep()

    await waitFor(() => {
      expect(update).toHaveBeenCalledWith('IS05_hostname', 'vulos-k3n7q2')
    })
    // The shared name is the collision itself.
    expect(update).not.toHaveBeenCalledWith('IS05_hostname', 'vulos')
    // An empty prefill makes the step skip its POST, leaving the shared default.
    expect(update).not.toHaveBeenCalledWith('IS05_hostname', '')
  })

  it('prefers a name the owner already chose over the derived default', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({
      ulid: '01HZZZZZZZZZZZZZZZZZK3N7Q2',
      hostname: 'study',
      default_hostname: 'vulos-k3n7q2',
    })))

    const { update } = renderStep()
    await waitFor(() => {
      expect(update).toHaveBeenCalledWith('IS05_hostname', 'study')
    })
  })

  it('tells the owner while they type that another box already has the name', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (String(url).startsWith('/api/identity/hostname/available')) {
        return jsonResponse({ available: false, taken_by: '192.168.1.9' })
      }
      return jsonResponse({ ulid: 'x', hostname: '', default_hostname: 'vulos-k3n7q2' })
    })
    vi.stubGlobal('fetch', fetchMock as never)

    render(<Harness onNext={vi.fn()} />)
    typeHostname('study')

    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toMatch(/already answers to that name/i)
    })
    expect(screen.getByRole('alert').textContent).toContain('192.168.1.9')
  })

  it('does NOT advance when the box says the rename is not live yet', async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        return jsonResponse({
          hostname: 'study',
          applied_live: false,
          notice: 'The name is saved, but this box is still answering to its previous name on the network. Restart the box to finish the rename.',
        })
      }
      if (String(url).startsWith('/api/identity/hostname/available')) {
        return jsonResponse({ available: true })
      }
      return jsonResponse({ ulid: 'x', hostname: '', default_hostname: 'vulos-k3n7q2' })
    })
    vi.stubGlobal('fetch', fetchMock as never)

    const onNext = vi.fn()
    render(<Harness onNext={onNext} />)
    typeHostname('study')

    fireEvent.click(screen.getByRole('button', { name: /continue/i }))

    await waitFor(() => {
      expect(screen.getByText(/still answering to its previous name/i)).toBeTruthy()
    })
    // THE assertion: a 200 that the box explicitly declined to call live must
    // not be reported to the user as a completed rename.
    expect(onNext).not.toHaveBeenCalled()

    // A second, informed press continues.
    fireEvent.click(screen.getByRole('button', { name: /continue anyway/i }))
    await waitFor(() => expect(onNext).toHaveBeenCalled())
  })

  it('advances normally when the rename DID take effect', async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        return jsonResponse({ hostname: 'study', applied_live: true, names: ['study.local'] })
      }
      if (String(url).startsWith('/api/identity/hostname/available')) {
        return jsonResponse({ available: true })
      }
      return jsonResponse({ ulid: 'x', hostname: '', default_hostname: 'vulos-k3n7q2' })
    })
    vi.stubGlobal('fetch', fetchMock as never)

    const onNext = vi.fn()
    render(<Harness onNext={onNext} />)
    typeHostname('study')
    fireEvent.click(screen.getByRole('button', { name: /continue/i }))

    await waitFor(() => expect(onNext).toHaveBeenCalled())
    expect(screen.queryByText(/still answering to its previous name/i)).toBeNull()
  })

  it('says nothing about availability when the check could not run', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (String(url).startsWith('/api/identity/hostname/available')) {
        throw new Error('offline')
      }
      return jsonResponse({ ulid: 'x', hostname: '', default_hostname: 'vulos-k3n7q2' })
    })
    vi.stubGlobal('fetch', fetchMock as never)

    render(<Harness onNext={vi.fn()} />)
    typeHostname('study')

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining('/api/identity/hostname/available'),
        expect.anything(),
      )
    })
    // Claiming a name is free when we never managed to check is worse than
    // staying quiet: it is a green tick for an unverified fact.
    expect(screen.queryByText(/is free on your network/i)).toBeNull()
  })
})
