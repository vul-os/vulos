// Contacts.test.tsx — the standalone Contacts app.
//
// Guards: it lists contacts, shows a detail pane on select, opens the editor to
// create/edit (write goes to the /v1 seam), and degrades to "Connect Mail" when
// /v1 is down.

import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { Contact, ContactFormInput } from '../contactsApi'

const openWindow = vi.fn()
vi.mock('../../../providers/ShellProvider', () => ({ useShell: () => ({ openWindow }) }))
const launchApp = vi.fn()
vi.mock('../../../shell/launchApp', () => ({ launchApp: (...a: unknown[]) => launchApp(...a) }))
vi.mock('../../../core/AppRegistry', () => ({ getAppById: (id: string) => ({ id }) }))

const api = vi.hoisted(() => ({
  listContacts: vi.fn<(q?: string) => Promise<Contact[]>>(),
  createContact: vi.fn<(c: ContactFormInput) => Promise<unknown>>(),
  updateContact: vi.fn<(id: string, c: ContactFormInput) => Promise<unknown>>(),
  deleteContact: vi.fn<(id: string) => Promise<unknown>>(),
}))
vi.mock('../contactsApi', async () => {
  const real = await vi.importActual<typeof import('../contactsApi')>('../contactsApi')
  return { ...real, ...api }
})

import Contacts from '../Contacts'

function contact(over: Partial<Contact> = {}): Contact {
  return { id: 'u1', name: 'Ada Lovelace', org: 'Vulos', title: 'Eng', note: 'note', emails: ['ada@x.com'], phones: ['+1 555'], ...over }
}

// Structural selectors (data-contact-editor input) aren't reachable via
// getByLabelText, so narrow the querySelector result to a real input rather
// than casting it.
function inputEl(sel: string): HTMLInputElement {
  const el = document.querySelector(sel)
  if (!(el instanceof HTMLInputElement)) throw new Error(`expected an input: ${sel}`)
  return el
}

beforeEach(() => {
  api.listContacts.mockReset().mockResolvedValue([contact()])
  api.createContact.mockReset().mockResolvedValue({ contact: { uid: 'new' } })
  api.updateContact.mockReset().mockResolvedValue({ contact: { uid: 'u1' } })
  api.deleteContact.mockReset().mockResolvedValue(null)
  openWindow.mockReset(); launchApp.mockReset()
})
afterEach(cleanup)

it('lists contacts and shows detail on select', async () => {
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Ada Lovelace'))
  // Detail pane renders the email as a mailto link (also shown in the list row).
  await waitFor(() => expect(screen.getByText('Eng · Vulos')).toBeInTheDocument())
  const mailto = document.querySelector('[data-contact-detail] a[href="mailto:ada@x.com"]')
  expect(mailto).toBeTruthy()
})

it('creates a contact via the /v1 seam', async () => {
  render(<Contacts />)
  await waitFor(() => expect(api.listContacts).toHaveBeenCalled())
  fireEvent.click(screen.getByLabelText('New contact'))
  const name = await screen.findByText('New contact')
  expect(name).toBeInTheDocument()
  const nameInput = inputEl('[data-contact-editor] input')
  fireEvent.change(nameInput, { target: { value: 'Grace Hopper' } })
  fireEvent.click(screen.getByText('Save'))
  await waitFor(() => expect(api.createContact).toHaveBeenCalled())
  expect(api.createContact.mock.calls[0][0]).toMatchObject({ name: 'Grace Hopper' })
})

it('edits an existing contact via the /v1 update seam', async () => {
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Ada Lovelace'))
  fireEvent.click(await screen.findByText('Edit'))
  const nameInput = inputEl('[data-contact-editor] input')
  fireEvent.change(nameInput, { target: { value: 'Ada L.' } })
  fireEvent.click(screen.getByText('Save'))
  await waitFor(() => expect(api.updateContact).toHaveBeenCalled())
  expect(api.updateContact.mock.calls[0][0]).toBe('u1')
  expect(api.updateContact.mock.calls[0][1]).toMatchObject({ name: 'Ada L.' })
})

it('deletes a contact via the /v1 seam', async () => {
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Ada Lovelace'))
  fireEvent.click(await screen.findByText('Delete'))
  await waitFor(() => expect(api.deleteContact).toHaveBeenCalledWith('u1'))
})

it('filters the list by the search box and shows an honest no-match state', async () => {
  api.listContacts.mockResolvedValue([contact(), contact({ id: 'u2', name: 'Grace Hopper', emails: ['grace@x.com'], org: 'Navy' })])
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Grace Hopper')).toBeInTheDocument())
  const search = screen.getByLabelText('Search contacts')
  fireEvent.change(search, { target: { value: 'grace' } })
  expect(screen.queryByText('Ada Lovelace')).not.toBeInTheDocument()
  expect(screen.getByText('Grace Hopper')).toBeInTheDocument()
  // A query with no hits shows the honest empty state (not a crash).
  fireEvent.change(search, { target: { value: 'zzz-nobody' } })
  expect(screen.getByText('No matches.')).toBeInTheDocument()
})

it('degrades to Connect Mail when /v1 is down', async () => {
  api.listContacts.mockRejectedValue(new Error('mail service unreachable'))
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Contacts unavailable.')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Connect Mail →'))
  expect(launchApp).toHaveBeenCalled()
})

// ── Rail ordering + A–Z grouping (added with the desktop polish pass) ────────
// The rail used to render the wire order, which is the MERGE order (CardDAV
// cards first, then device/SIM-only extras appended) — so a SIM contact called
// "Mum" sorted after "Thabo". These assert the rendered DOM order, not just
// that the names exist, so a regression that drops the sort fails here.

function railNames(): string[] {
  return Array.from(document.querySelectorAll('[data-contacts-app] li button'))
    .map((b) => b.querySelector('span > span > span')?.textContent || '')
    .filter(Boolean)
}

it('orders the rail alphabetically regardless of the order the box returned', async () => {
  api.listContacts.mockResolvedValue([
    contact({ id: 'u1', name: 'Thabo Mokoena', emails: ['t@x.com'] }),
    contact({ id: 'u2', name: 'Ada Lovelace', emails: ['ada@x.com'] }),
    contact({ id: 'u3', name: 'Mum', emails: ['mum@x.com'] }),
  ])
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  expect(railNames()).toEqual(['Ada Lovelace', 'Mum', 'Thabo Mokoena'])
})

it('heads each alphabetical run with its letter, once', async () => {
  api.listContacts.mockResolvedValue([
    contact({ id: 'u1', name: 'Ada Lovelace', emails: ['ada@x.com'] }),
    contact({ id: 'u2', name: 'Alan Turing', emails: ['alan@x.com'] }),
    contact({ id: 'u3', name: 'Grace Hopper', emails: ['grace@x.com'] }),
  ])
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  const letters = Array.from(document.querySelectorAll('[data-contacts-app] section > h2'))
    .map((h) => h.textContent)
  // Two A's share ONE heading — a per-row heading would read ['A','A','G'].
  expect(letters).toEqual(['A', 'G'])
})

// ── Richer vCard fields (added when the detail pane was filled out) ─────────
// These come straight off the same /v1/contacts/cards payload as name/email —
// no new request — so a card that genuinely carries them should render them,
// and a card that doesn't should render NONE of this: no empty "Birthday"
// row, no broken avatar.

it('renders birthday, website, address and groups when the card carries them', async () => {
  api.listContacts.mockResolvedValue([contact({
    birthday: '1990-03-14',
    websites: [{ value: 'ada.dev', type: 'work' }],
    addresses: [{ type: 'home', locality: 'London', country: 'UK', poBox: '', extended: '', street: '', region: '', postal: '' }],
    groups: ['Family'],
  })])
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Ada Lovelace'))
  expect(await screen.findByText('March 14, 1990')).toBeInTheDocument()
  const site = screen.getByText('ada.dev')
  expect(site.closest('a')).toHaveAttribute('href', 'https://ada.dev/')
  expect(screen.getByText('London')).toBeInTheDocument()
  expect(screen.getByText('UK')).toBeInTheDocument()
  expect(screen.getByText('Family')).toBeInTheDocument()
})

it('shows the real photo as the avatar instead of initials', async () => {
  api.listContacts.mockResolvedValue([contact({ photo: 'data:image/png;base64,AAAA' })])
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Ada Lovelace'))
  const img = await waitFor(() => {
    const el = document.querySelector('[data-contact-detail] header img')
    if (!el) throw new Error('no avatar img yet')
    return el
  })
  expect(img).toHaveAttribute('src', 'data:image/png;base64,AAAA')
  // The initials tile is replaced, not layered underneath.
  expect(document.querySelector('[data-contact-detail] header span.mono')).not.toBeInTheDocument()
})

it('shows the fields card (not the empty-card prompt) when a birthday is the ONLY field present', async () => {
  api.listContacts.mockResolvedValue([contact({ emails: [], phones: [], note: '', birthday: '1990-03-14' })])
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Ada Lovelace'))
  expect(await screen.findByText('March 14, 1990')).toBeInTheDocument()
  expect(screen.queryByText(/No email, phone or notes on this card yet/)).not.toBeInTheDocument()
})

it('renders no rich-field rows for a card that carries none of them', async () => {
  render(<Contacts />) // default contact() has no birthday/website/address/groups/photo
  await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Ada Lovelace'))
  await waitFor(() => expect(screen.getByText('Eng · Vulos')).toBeInTheDocument())
  expect(screen.queryByText('Birthday')).not.toBeInTheDocument()
  expect(screen.queryByText('Website')).not.toBeInTheDocument()
  expect(screen.queryByText('Address')).not.toBeInTheDocument()
  expect(screen.queryByText('Groups')).not.toBeInTheDocument()
  expect(document.querySelector('[data-contact-detail] header img')).not.toBeInTheDocument()
  expect(screen.queryByText('Favorite')).not.toBeInTheDocument()
})

it('shows the favorite star only when the card is starred', async () => {
  api.listContacts.mockResolvedValue([contact({ starred: true })])
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Ada Lovelace'))
  expect(await screen.findByText('Favorite')).toBeInTheDocument()
})

it('offers to add details on a card that has none', async () => {
  api.listContacts.mockResolvedValue([
    contact({ id: 'u1', name: 'Ada Lovelace', emails: [], phones: [], note: '' }),
  ])
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Ada Lovelace'))
  expect(await screen.findByText(/No email, phone or notes on this card yet/)).toBeInTheDocument()
  fireEvent.click(screen.getByText('Add details'))
  expect(document.querySelector('[data-contact-editor]')).toBeTruthy()
})
