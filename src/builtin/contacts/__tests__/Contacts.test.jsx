// Contacts.test.jsx — the standalone Contacts app.
//
// Guards: it lists contacts, shows a detail pane on select, opens the editor to
// create/edit (write goes to the /v1 seam), and degrades to "Connect Mail" when
// /v1 is down.

import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

const openWindow = vi.fn()
vi.mock('../../../providers/ShellProvider', () => ({ useShell: () => ({ openWindow }) }))
const launchApp = vi.fn()
vi.mock('../../../shell/launchApp', () => ({ launchApp: (...a) => launchApp(...a) }))
vi.mock('../../../core/AppRegistry', () => ({ getAppById: (id) => ({ id }) }))

const api = vi.hoisted(() => ({
  listContacts: vi.fn(),
  createContact: vi.fn(),
  updateContact: vi.fn(),
  deleteContact: vi.fn(),
}))
vi.mock('../contactsApi', async () => {
  const real = await vi.importActual('../contactsApi')
  return { ...real, ...api }
})

import Contacts from '../Contacts'

function contact(over = {}) {
  return { id: 'u1', name: 'Ada Lovelace', org: 'Vulos', title: 'Eng', note: 'note', emails: ['ada@x.com'], phones: ['+1 555'], ...over }
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
  const nameInput = document.querySelector('[data-contact-editor] input')
  fireEvent.change(nameInput, { target: { value: 'Grace Hopper' } })
  fireEvent.click(screen.getByText('Save'))
  await waitFor(() => expect(api.createContact).toHaveBeenCalled())
  expect(api.createContact.mock.calls[0][0]).toMatchObject({ name: 'Grace Hopper' })
})

it('degrades to Connect Mail when /v1 is down', async () => {
  api.listContacts.mockRejectedValue(new Error('mail service unreachable'))
  render(<Contacts />)
  await waitFor(() => expect(screen.getByText('Contacts unavailable.')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Connect Mail →'))
  expect(launchApp).toHaveBeenCalled()
})
