// contactsApi.test.js — the Contacts surface's /v1 seam (via /api/pim/contacts).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { normalizeContact, listContacts, createContact, updateContact, deleteContact } from '../contactsApi'

function mockFetch(status, body) {
  return vi.fn().mockResolvedValue({
    ok: status < 300,
    status,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body ?? '')),
  })
}

beforeEach(() => { global.fetch = mockFetch(200, { contacts: [] }) })
afterEach(() => { vi.restoreAllMocks() })

describe('normalizeContact', () => {
  it('maps uid/name/emails/phones/org/title/note', () => {
    const c = normalizeContact({ uid: 'u1', name: 'Ada', emails: ['a@x.com', ''], phones: ['123'], org: 'Vulos', title: 'Eng', note: 'hi' })
    expect(c).toMatchObject({ id: 'u1', name: 'Ada', org: 'Vulos', title: 'Eng', note: 'hi' })
    expect(c.emails).toEqual(['a@x.com']) // blank filtered
    expect(c.phones).toEqual(['123'])
  })
  it('tolerates missing arrays', () => {
    const c = normalizeContact({ name: 'X' })
    expect(c.emails).toEqual([])
    expect(c.phones).toEqual([])
  })
})

describe('listContacts', () => {
  it('reads {contacts:[]} and sorts by name', async () => {
    global.fetch = mockFetch(200, { contacts: [{ uid: '2', name: 'Bob' }, { uid: '1', name: 'Ada' }] })
    const cs = await listContacts()
    expect(cs.map((c) => c.name)).toEqual(['Ada', 'Bob'])
  })
  it('throws on non-ok so the UI can show "unavailable"', async () => {
    global.fetch = mockFetch(502, 'mail service unreachable')
    await expect(listContacts()).rejects.toThrow()
  })
})

describe('write path', () => {
  it('createContact POSTs to /api/pim/contacts and omits empty fields', async () => {
    const spy = mockFetch(201, { contact: { uid: 'new' } })
    global.fetch = spy
    await createContact({ name: 'Ada', emails: ['a@x.com', ''], phones: [''], org: '', note: 'n' })
    const [url, opts] = spy.mock.calls[0]
    expect(url).toBe('/api/pim/contacts')
    expect(opts.method).toBe('POST')
    const sent = JSON.parse(opts.body)
    expect(sent).toMatchObject({ name: 'Ada', emails: ['a@x.com'], note: 'n' })
    expect(sent.phones).toBeUndefined() // all-blank list dropped
    expect(sent.org).toBeUndefined()
  })
  it('updateContact PUTs the id-scoped path', async () => {
    const spy = mockFetch(200, { contact: {} })
    global.fetch = spy
    await updateContact('u 1', { name: 'X' })
    expect(spy.mock.calls[0][0]).toBe('/api/pim/contacts/u%201')
    expect(spy.mock.calls[0][1].method).toBe('PUT')
  })
  it('deleteContact DELETEs and tolerates an empty body', async () => {
    const spy = mockFetch(204, '')
    global.fetch = spy
    await expect(deleteContact('u1')).resolves.toBeNull()
    expect(spy.mock.calls[0][1].method).toBe('DELETE')
  })
})
