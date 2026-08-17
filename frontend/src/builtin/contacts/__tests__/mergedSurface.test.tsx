// mergedSurface.test.tsx — contacts, calling and call history on ONE surface.
//
// The properties here are the ones that would put a lie on the screen, and the
// ones that decide whether a box with NO telephony hardware — which is most
// Vulos boxes — gets a good address book or a broken phone:
//
//   1. No line ⇒ no dial pad and no SMS inbox are OFFERED at all. A page that
//      can only ever be empty and can only ever fail is not a page.
//   2. The people pane is never conditional on a radio.
//   3. The in-call bar is drawn from the MODEM's answer, never from the fact
//      that a dial was posted. A dial the network rejects leaves the modem
//      idle; a request-driven bar would claim a call and offer to hang up
//      nothing.
//   4. Hang up / Answer / Decline reach the box, and do not clear the bar
//      locally on the assumption that they worked.
//   5. A modem Status body must not be mistaken for an active call — the two
//      are both JSON objects on neighbouring routes.

import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Contact } from '../contactsApi'
import { toActiveCall } from '../../phone/telephonyApi'

const openWindow = vi.fn()
vi.mock('../../../providers/ShellProvider', () => ({ useShell: () => ({ openWindow }) }))
vi.mock('../../../shell/launchApp', () => ({ launchApp: vi.fn() }))
vi.mock('../../../core/AppRegistry', () => ({ getAppById: (id: string) => ({ id }) }))

const api = vi.hoisted(() => ({ listContacts: vi.fn<() => Promise<Contact[]>>() }))
vi.mock('../contactsApi', async () => {
  const real = await vi.importActual<typeof import('../contactsApi')>('../contactsApi')
  return { ...real, ...api }
})

import Contacts from '../Contacts'

const CARD: Contact = {
  id: 'u1', name: 'Ada Lovelace', org: 'Vulos', title: 'Eng', note: '',
  emails: ['ada@x.com'], phones: ['+27 83 111 2222'],
}

const VOICE_MODEM = { available: true, state: 'registered', signal_quality: 72, operator: 'Test Net', number: '+27830000001', voice: true }
const NO_MODEM = { available: false }

/** Requests the box received, so a test can assert what was actually sent. */
let posted: { url: string; body: string }[] = []
/** Per-path bodies; a path missing here answers its documented empty shape. */
let routes: Record<string, unknown> = {}

function installFetch() {
  global.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    if (method === 'POST') posted.push({ url, body: String(init?.body ?? '') })
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
  posted = []
  routes = {}
  api.listContacts.mockReset().mockResolvedValue([CARD])
  openWindow.mockReset()
  installFetch()
})
afterEach(() => { cleanup(); vi.restoreAllMocks() })

const tabNames = () => screen.queryAllByRole('tab').map((t) => t.textContent?.replace(/[^A-Za-z]/g, '') ?? '')

describe('a box with no telephony hardware', () => {
  it('offers no keypad and no SMS inbox, because neither could ever work', async () => {
    render(<Contacts />)
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())

    const names = tabNames()
    expect(names).toContain('Contacts')
    expect(names).toContain('Recents')
    // These are the ones that must be ABSENT. A disabled dial pad is still a
    // dial pad offered to someone who has no radio.
    expect(names).not.toContain('Keypad')
    expect(names).not.toContain('Messages')
  })

  it('still gives a fully usable address book — an address book needs no radio', async () => {
    render(<Contacts />)
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())

    fireEvent.click(screen.getByText('Ada Lovelace'))
    await waitFor(() => expect(screen.getByText('Eng · Vulos')).toBeInTheDocument())
    // Editing — the thing the phone app's own contacts copy could never do.
    expect(screen.getByRole('button', { name: 'Edit' })).toBeEnabled()
  })

  it('names the hardware to plug in, generically, and never points at Android', async () => {
    render(<Contacts />)
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())

    fireEvent.click(screen.getByRole('tab', { name: /Recents/ }))
    await screen.findByText(/No SIM or modem on this box/i)
    const shown = document.body.textContent ?? ''
    expect(shown).toMatch(/USB LTE/i)
    expect(shown).toMatch(/ModemManager/i)
    // The claim this whole merge exists to bury: the box side has ALWAYS been
    // generic GSM. Nothing here may tell a USB-stick owner to go find a phone.
    expect(shown.toLowerCase()).not.toContain('android')
  })

  it('a BROKEN telephony service never wears the face of a missing modem', async () => {
    // These two ask the user to do completely different things: one means buy
    // hardware, the other means the box is broken and the modem may be sitting
    // there working fine. Rendering them the same way sends someone shopping.
    global.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input).replace(/^https?:\/\/[^/]+/, '').split('?')[0]
      if (path === '/api/telephony/status') {
        return new Response(JSON.stringify({ error: 'boom' }), { status: 500 })
      }
      return new Response(JSON.stringify(defaultFor(`GET ${path}`)), { status: 200 })
    }) as unknown as typeof fetch

    render(<Contacts />)
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('tab', { name: /Recents/ }))

    await screen.findByText(/Telephony isn’t answering/i)
    expect(screen.queryByText(/No SIM or modem on this box/i)).toBeNull()
  })

  it('says on the contact card itself why the number cannot be called', async () => {
    render(<Contacts />)
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
    fireEvent.click(screen.getByText('Ada Lovelace'))

    await screen.findByText(/No modem is connected to this box/i)
    expect(screen.getByRole('button', { name: /Call \+27 83 111 2222/ })).toBeDisabled()
  })
})

describe('a box with a voice-capable modem', () => {
  beforeEach(() => { routes['GET /api/telephony/status'] = VOICE_MODEM })

  it('offers the full set of pages, and still opens on people', async () => {
    render(<Contacts />)
    await waitFor(() => expect(tabNames()).toContain('Keypad'))

    expect(tabNames()).toEqual(['Contacts', 'Recents', 'Keypad', 'Messages'])
    // Contacts is FIRST and SELECTED: the founder asked for people on the front
    // page, not a dialler with an address book bolted on.
    expect(screen.getByRole('tab', { name: /Contacts/ })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByText('Ada Lovelace')).toBeInTheDocument()
  })
})

describe('the in-call bar shows what the MODEM says, not what we asked for', () => {
  beforeEach(() => { routes['GET /api/telephony/status'] = VOICE_MODEM })

  it('does not appear merely because a dial was posted', async () => {
    // The box accepted nothing: /call/active keeps reporting no call. A bar
    // driven by "we posted a dial" would appear here and offer a Hang up that
    // hangs up nothing.
    render(<Contacts />)
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
    fireEvent.click(screen.getByText('Ada Lovelace'))
    const callBtn = await screen.findByRole('button', { name: /Call \+27 83 111 2222/ })
    await waitFor(() => expect(callBtn).toBeEnabled())

    await act(async () => { fireEvent.click(callBtn) })

    expect(posted.some((p) => p.url.includes('/api/telephony/call'))).toBe(true)
    expect(document.querySelector('[data-in-call-bar]')).toBeNull()
  })

  it('appears when the modem reports a call, and hangs it up on the box', async () => {
    routes['GET /api/telephony/call/active'] = { active: true, number: '+27831112222', direction: 'outgoing', state: 'active' }
    render(<Contacts />)

    const bar = await waitFor(() => {
      const el = document.querySelector('[data-in-call-bar]')
      if (!el) throw new Error('no in-call bar')
      return el
    })
    expect(bar).toHaveAttribute('data-call-state', 'active')
    expect(bar.textContent).toContain('On a call')

    await act(async () => { fireEvent.click(screen.getByText('Hang up')) })
    expect(posted.map((p) => p.url).some((u) => u.endsWith('/api/telephony/call/hangup'))).toBe(true)
  })

  it('does not clear the bar locally — the modem decides when a call is over', async () => {
    // The box keeps reporting the call after the hangup (a hangup that failed,
    // or one the modem has not applied yet). Clearing the bar on the optimistic
    // assumption would strand a LIVE call with no control at all.
    routes['GET /api/telephony/call/active'] = { active: true, number: '+27831112222', direction: 'outgoing', state: 'active' }
    render(<Contacts />)
    await waitFor(() => expect(document.querySelector('[data-in-call-bar]')).not.toBeNull())

    await act(async () => { fireEvent.click(screen.getByText('Hang up')) })
    await waitFor(() => expect(posted.some((p) => p.url.endsWith('/call/hangup'))).toBe(true))

    expect(document.querySelector('[data-in-call-bar]')).not.toBeNull()
  })

  it('offers Answer and Decline for a ringing inbound call, not Hang up', async () => {
    routes['GET /api/telephony/call/active'] = { active: true, number: '+27831112222', direction: 'incoming', state: 'ringing-in' }
    render(<Contacts />)

    await waitFor(() => expect(document.querySelector('[data-in-call-bar]')).not.toBeNull())
    expect(screen.getByText('Incoming call')).toBeInTheDocument()
    expect(screen.queryByText('Hang up')).toBeNull()

    await act(async () => { fireEvent.click(screen.getByText('Answer')) })
    expect(posted.some((p) => p.url.endsWith('/api/telephony/call/answer'))).toBe(true)

    await act(async () => { fireEvent.click(screen.getByText('Decline')) })
    expect(posted.some((p) => p.url.endsWith('/api/telephony/call/decline'))).toBe(true)
  })

  it('names the caller from the address book when the number is known', async () => {
    routes['GET /api/contacts/unified'] = {
      contacts: [{ id: 'u1', name: 'Ada Lovelace', phones: ['+27 83 111 2222'], emails: [], org: '', sources: ['vulos'] }],
      sources_active: ['vulos'],
    }
    // The modem reports the raw number; the book stores it spaced. Matching on
    // the last nine digits is what makes an in-call bar show a person.
    routes['GET /api/telephony/call/active'] = { active: true, number: '+27831112222', direction: 'incoming', state: 'active' }
    render(<Contacts />)

    const bar = await waitFor(() => {
      const el = document.querySelector('[data-in-call-bar]')
      if (!el) throw new Error('no in-call bar')
      return el
    })
    await waitFor(() => expect(bar.textContent).toContain('Ada Lovelace'))
  })
})

describe('the box SIM and the linked phone are part of the address book', () => {
  // The unified list is read ONCE by the app shell and handed to the people
  // pane. Nothing covered what the pane then DOES with it, so the whole merge
  // could have been dropped in the re-plumbing and every other test stayed
  // green — a mutation that replaced the unified rows with [] survived until
  // these two existed.
  const UNIFIED_WITH_SIM = {
    contacts: [
      // Same person as the editable card, seen on the box SIM as well.
      { id: 'u1', name: 'Ada Lovelace', phones: ['+27 83 111 2222'], emails: ['ada@x.com'], org: 'Vulos', sources: ['vulos', 'box-sim'] },
      // Lives ONLY on the box SIM: no CardDAV card exists for her at all.
      { id: 's9', name: 'Nomsa Dube', phones: ['+27 82 444 5555'], emails: [], org: '', note: 'from the SIM', sources: ['box-sim'] },
    ],
    sources_active: ['vulos', 'box-sim'],
  }

  it('lists a contact that exists only on the box SIM', async () => {
    routes['GET /api/contacts/unified'] = UNIFIED_WITH_SIM
    render(<Contacts />)

    // She has no editable card, so if the unified rows are dropped she simply
    // vanishes — silently, with the list looking perfectly healthy.
    await waitFor(() => expect(screen.getByText('Nomsa Dube')).toBeInTheDocument())
  })

  it('badges a card with the other places that person also lives', async () => {
    routes['GET /api/contacts/unified'] = UNIFIED_WITH_SIM
    render(<Contacts />)
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())

    fireEvent.click(screen.getByText('Ada Lovelace'))
    const detail = await waitFor(() => {
      const el = document.querySelector('[data-contact-detail]')
      if (!el) throw new Error('no detail pane')
      return el
    })
    await waitFor(() => expect(detail.textContent).toContain('Box SIM'))
  })
})

describe('an incomplete address book is never rendered as a complete one', () => {
  it('says so when the box SIM / linked-phone sources fail to load', async () => {
    global.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input).replace(/^https?:\/\/[^/]+/, '').split('?')[0]
      if (path === '/api/contacts/unified') {
        return new Response(JSON.stringify({ error: 'boom' }), { status: 500 })
      }
      return new Response(JSON.stringify(defaultFor(`GET ${path}`)), { status: 200 })
    }) as unknown as typeof fetch

    render(<Contacts />)
    // The CardDAV half still works, so this is a WARNING, not the app's
    // "Contacts unavailable" state — but the list is short and must say why.
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
    await screen.findByText(/may be missing people/i)
  })

  it('says so when the mail half fails but the box SIM still has people', async () => {
    // The mirror image, and a case that only exists because the two apps
    // merged: before, a failed card read emptied the list and the app fell
    // into "Contacts unavailable". Now the SIM can still be carrying people,
    // and showing them without a word is the same defect in reverse.
    api.listContacts.mockRejectedValue(new Error('lilmail unreachable'))
    routes['GET /api/contacts/unified'] = {
      contacts: [{ id: 's9', name: 'Nomsa Dube', phones: ['+27 82 444 5555'], emails: [], org: '', sources: ['box-sim'] }],
      sources_active: ['box-sim'],
    }

    render(<Contacts />)
    await waitFor(() => expect(screen.getByText('Nomsa Dube')).toBeInTheDocument())
    await screen.findByText(/mail account.*couldn’t be loaded/i)
    expect(screen.queryByText(/Contacts unavailable/i)).toBeNull()
  })

  it('falls back to the honest dead end when EVERY source is down', async () => {
    api.listContacts.mockRejectedValue(new Error('lilmail unreachable'))
    global.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input).replace(/^https?:\/\/[^/]+/, '').split('?')[0]
      if (path === '/api/contacts/unified') return new Response(JSON.stringify({ error: 'boom' }), { status: 500 })
      return new Response(JSON.stringify(defaultFor(`GET ${path}`)), { status: 200 })
    }) as unknown as typeof fetch

    render(<Contacts />)
    await screen.findByText(/Contacts unavailable/i)
  })
})

describe('toActiveCall', () => {
  it('will not read a modem Status body as a call in progress', () => {
    // GET /api/telephony/ is a subtree catch-all that answers Status. If the
    // specific /call/active route ever stopped matching, this body is what the
    // in-call bar would be handed — and it carries a truthy-looking shape.
    expect(toActiveCall({ available: true, voice: true, operator: 'Test Net' }).active).toBe(false)
  })

  it('requires the wire to SAY active, not merely to carry a number', () => {
    expect(toActiveCall({ number: '+27831112222', state: 'active' }).active).toBe(false)
    expect(toActiveCall({ active: true, number: '+27831112222', state: 'active' }).active).toBe(true)
  })
})
