// contactsApi.js — the standalone Contacts surface's read/write seam over the
// box's PIM proxy (/api/pim/contacts/*), which brokers to lilmail's /v1 contacts
// (CardDAV + any OAuth-connected Google/Outlook address books lilmail aggregates).
//
// The browser never sees mail credentials: it calls the box with its session
// cookie and the box injects the brokered creds (see backend routes_pim.go).
//
// Reads degrade to an empty list with an error flag (honest "unavailable" state),
// never a crash — contact data is other people's text and is always rendered as
// escaped React children, never HTML.

const BASE = '/api/pim/contacts'

// normalizeContact maps lilmail's models.Contact onto the lean shape the UI edits.
export function normalizeContact(c) {
  if (!c || typeof c !== 'object') return null
  return {
    id: c.uid || c.id || '',
    name: c.name || '',
    org: c.org || '',
    title: c.title || '',
    note: c.note || c.notes || '',
    emails: Array.isArray(c.emails) ? c.emails.filter(Boolean) : [],
    phones: Array.isArray(c.phones) ? c.phones.filter(Boolean) : [],
  }
}

async function req(method, path, body) {
  const opts = { method, credentials: 'include', headers: {} }
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json'
    opts.body = JSON.stringify(body)
  }
  const res = await fetch(BASE + path, opts)
  if (!res.ok) {
    const txt = await res.text().catch(() => '')
    const err = new Error(txt || `HTTP ${res.status}`)
    err.status = res.status
    throw err
  }
  const text = await res.text()
  if (!text) return null
  try { return JSON.parse(text) } catch { return null }
}

// listContacts reads the full-card list (optionally filtered by q), sorted by name.
export async function listContacts(q) {
  const qs = new URLSearchParams({ limit: '500' })
  if (q && q.trim()) qs.set('q', q.trim())
  const data = await req('GET', '/cards?' + qs.toString())
  const raw = Array.isArray(data) ? data : Array.isArray(data?.contacts) ? data.contacts : []
  return raw
    .map(normalizeContact)
    .filter(Boolean)
    .sort((a, b) => (a.name || '').localeCompare(b.name || ''))
}

function contactPayload(c) {
  const body = { name: c.name || '' }
  const emails = (c.emails || []).map((e) => e.trim()).filter(Boolean)
  const phones = (c.phones || []).map((p) => p.trim()).filter(Boolean)
  if (emails.length) body.emails = emails
  if (phones.length) body.phones = phones
  if (c.org?.trim()) body.org = c.org.trim()
  if (c.title?.trim()) body.title = c.title.trim()
  if (c.note?.trim()) body.note = c.note.trim()
  return body
}

export async function createContact(c) {
  return req('POST', '', contactPayload(c))
}

export async function updateContact(id, c) {
  return req('PUT', '/' + encodeURIComponent(id), contactPayload(c))
}

export async function deleteContact(id) {
  return req('DELETE', '/' + encodeURIComponent(id))
}
