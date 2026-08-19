// callAudioPath.test.tsx — the GSM call path must say WHERE THE AUDIO IS,
// before or at the moment of dialling, and it must be right per platform.
//
// The defect this pins shut: Vulos telephony is call CONTROL only. The box
// shells out to `mmcli` to dial, answer and hang up, and the modem owns the
// audio path (backend/services/telephony/calls.go). There is no getUserMedia
// anywhere in this repo, so no software voice path is possible. Yet a user
// could press Call on a contact, get a working in-call bar and a live call,
// and discover only by silence that they could neither hear nor be heard.
//
// The three properties, and why each matters:
//
//   1. ON A BOX WITH A VOICE MODEM the caveat appears BEFORE the number is
//      dialled (keypad, contact card) and again AT the moment of the call
//      (in-call bar). "After" is not a fix — silence gets there first.
//   2. ON ANDROID it appears NOWHERE. The shell hands the number to the system
//      dialer (Intent.ACTION_CALL) and the handset carries the call over its
//      own earpiece. The caveat would be plainly FALSE, and this path is
//      deliberate so emergency calls stay safe.
//   3. ON A BOX WITH NO MODEM it appears nowhere either. There is already a
//      blockedReason explaining the real problem; an audio caveat about a call
//      that cannot happen is noise stacked on top of it.

import { render, screen, waitFor, cleanup, act } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Contact } from '../../contacts/contactsApi'
import { AUDIO_ON_MODEM, AUDIO_ON_MODEM_SHORT } from '../useCallSession'

const openWindow = vi.fn()
vi.mock('../../../providers/ShellProvider', () => ({ useShell: () => ({ openWindow }) }))
vi.mock('../../../shell/launchApp', () => ({ launchApp: vi.fn() }))
vi.mock('../../../core/AppRegistry', () => ({ getAppById: (id: string) => ({ id }) }))

// The Android handset bridge, switchable per test. Absent by default, which is
// what a real browser on a box reports.
const bridge = vi.hoisted(() => ({ onAndroid: false }))
vi.mock('../../../core/nativeBridge', () => ({
  nativeBridge: {
    get inApp() { return bridge.onAndroid },
    telephony: {
      get available() { return bridge.onAndroid },
      dial: vi.fn(async () => {}),
      listCallLog: vi.fn(async () => []),
    },
    contacts: {
      get available() { return false },
      list: vi.fn(async () => []),
      sim: vi.fn(async () => []),
    },
  },
}))

const api = vi.hoisted(() => ({ listContacts: vi.fn<() => Promise<Contact[]>>() }))
vi.mock('../../contacts/contactsApi', async () => {
  const real = await vi.importActual<typeof import('../../contacts/contactsApi')>('../../contacts/contactsApi')
  return { ...real, ...api }
})

import Contacts from '../../contacts/Contacts'

const CARD: Contact = {
  id: 'u1', name: 'Ada Lovelace', org: 'Vulos', title: 'Eng', note: '',
  emails: [], phones: ['+27 83 111 2222'],
}

const VOICE_MODEM = { available: true, state: 'registered', signal_quality: 72, operator: 'Test Net', number: '+27830000001', voice: true, sms: true }
const NO_MODEM = { available: false }

let routes: Record<string, unknown> = {}

function installFetch() {
  global.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const path = url.replace(/^https?:\/\/[^/]+/, '').split('?')[0]
    const key = `${method} ${path}`
    const body = key in routes ? routes[key] : defaultFor(key)
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as unknown as typeof fetch
}

function defaultFor(key: string): unknown {
  if (key === 'GET /api/telephony/status') return NO_MODEM
  if (key === 'GET /api/telephony/virtual/status') return { configured: false, can_call: false }
  if (key === 'GET /api/telephony/call/active') return { active: false }
  if (key === 'GET /api/telephony/calls') return []
  if (key === 'GET /api/telephony/sms/threads') return []
  if (key === 'GET /api/peering/call/history') return []
  if (key === 'GET /api/contacts/unified') return { contacts: [], sources_active: [] }
  return {}
}

beforeEach(() => {
  routes = {}
  bridge.onAndroid = false
  api.listContacts.mockReset().mockResolvedValue([CARD])
  openWindow.mockReset()
  installFetch()
})
afterEach(() => { cleanup(); vi.restoreAllMocks() })

/** Every rendering of the caveat currently on screen, long or short. */
const caveats = () => [
  ...screen.queryAllByText(AUDIO_ON_MODEM),
  ...screen.queryAllByText(AUDIO_ON_MODEM_SHORT),
]

const openTab = async (name: RegExp) => {
  const tab = screen.getAllByRole('tab').find((t) => name.test(t.textContent ?? ''))
  if (!tab) throw new Error(`no tab matching ${name}`)
  await act(async () => { tab.click() })
}

describe('a box calling on its own modem', () => {
  beforeEach(() => { routes = { 'GET /api/telephony/status': VOICE_MODEM } })

  it('says where the audio is on the keypad, before a number is dialled', async () => {
    render(<Contacts />)
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
    await openTab(/Keypad/)

    const note = await screen.findByText(AUDIO_ON_MODEM)
    expect(note).toBeInTheDocument()
    // Before, not after: the caveat is on screen with the dial pad, while the
    // Call button is still untouched.
    expect(screen.getByLabelText(/^Call$/)).toBeInTheDocument()
  })

  it('says it again on the contact, which is where a call is actually placed from', async () => {
    render(<Contacts />)
    const row = await screen.findByText('Ada Lovelace')
    await act(async () => { row.click() })

    await waitFor(() => expect(screen.getByLabelText('Call +27 83 111 2222')).toBeEnabled())
    expect(screen.getByText(AUDIO_ON_MODEM)).toBeInTheDocument()
  })

  it('says it in the in-call bar, at the moment there is a call to be heard', async () => {
    routes = {
      'GET /api/telephony/status': VOICE_MODEM,
      'GET /api/telephony/call/active': { active: true, number: '+27831112222', direction: 'outgoing', state: 'active' },
    }
    render(<Contacts />)
    await waitFor(() => expect(document.querySelector('[data-in-call-bar]')).toBeInTheDocument())

    const bar = document.querySelector('[data-in-call-bar]')
    expect(bar?.textContent).toContain(AUDIO_ON_MODEM_SHORT)
    // And the controls that DO work are untouched beside it.
    expect(bar?.querySelector('[data-call-hangup]')).toBeInTheDocument()
  })

  it('states both halves — not in the browser, and where it is instead', () => {
    expect(AUDIO_ON_MODEM).toMatch(/browser/i)
    expect(AUDIO_ON_MODEM).toMatch(/modem/i)
    expect(AUDIO_ON_MODEM_SHORT).toMatch(/modem/i)
    expect(AUDIO_ON_MODEM_SHORT).toMatch(/browser/i)
    // It must not read as a failure. The call is real and useful; a phone you
    // operate from your desk while you talk on the handset is the product.
    expect(AUDIO_ON_MODEM).not.toMatch(/error|failed|unsupported|cannot call|can’t call/i)
  })
})

describe('the Vulos app on an Android handset', () => {
  beforeEach(() => {
    bridge.onAndroid = true
    // No box modem: the handset SIM is the only line, so the system dialer
    // takes the call and the audio is exactly where the user expects it.
    routes = { 'GET /api/telephony/status': NO_MODEM }
  })

  it('says nothing about audio anywhere, because the caveat would be false there', async () => {
    render(<Contacts />)
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
    expect(caveats()).toHaveLength(0)

    await act(async () => { screen.getByText('Ada Lovelace').click() })
    await waitFor(() => expect(screen.getByLabelText('Call +27 83 111 2222')).toBeEnabled())
    expect(caveats()).toHaveLength(0)

    await openTab(/Keypad/)
    expect(caveats()).toHaveLength(0)
  })

  it('still offers the call — the handset path works and must not be warned away', async () => {
    render(<Contacts />)
    const row = await screen.findByText('Ada Lovelace')
    await act(async () => { row.click() })
    await waitFor(() => expect(screen.getByLabelText('Call +27 83 111 2222')).toBeEnabled())
  })
})

describe('a box with no modem at all', () => {
  it('explains the missing hardware and does not stack an audio caveat on top', async () => {
    render(<Contacts />)
    const row = await screen.findByText('Ada Lovelace')
    await act(async () => { row.click() })

    await waitFor(() => expect(screen.getByLabelText('Call +27 83 111 2222')).toBeDisabled())
    expect(caveats()).toHaveLength(0)
    expect(screen.getByText(/No modem is connected to this box/)).toBeInTheDocument()
  })
})
