// contactsApi.ts — the standalone Contacts surface's read/write seam over the
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

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// A wire field is honoured only when it actually is the expected string type
// — matches the original `a || b || ''` fallback chain for the realistic wire
// shape, while degrading a malformed field to the fallback instead of leaking
// an unexpected runtime type into the render tree.
function str(x: unknown): string {
  return typeof x === 'string' ? x : ''
}

// strList mirrors `Array.isArray(x) ? x.filter(Boolean) : []` for the
// realistic wire shape (an array of strings), narrowing element-wise so the
// result is honestly `string[]` rather than `unknown[]`.
function strList(x: unknown): string[] {
  return Array.isArray(x) ? x.filter((e): e is string => typeof e === 'string' && !!e) : []
}

// HttpError — thrown by req() on a non-ok response. Carries `status` (nothing
// currently reads it; callers read `.message`) without resorting to
// `(err as any).status = …`.
export class HttpError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.name = 'HttpError'
    this.status = status
  }
}

// The lean shape the UI edits.
export interface Contact {
  id: string
  name: string
  org: string
  title: string
  note: string
  emails: string[]
  phones: string[]
}

// normalizeContact maps lilmail's models.Contact onto the lean shape the UI edits.
export function normalizeContact(c: unknown): Contact | null {
  if (!isRecord(c)) return null
  return {
    id: str(c.uid) || str(c.id),
    name: str(c.name),
    org: str(c.org),
    title: str(c.title),
    note: str(c.note) || str(c.notes),
    emails: strList(c.emails),
    phones: strList(c.phones),
  }
}

async function req(method: string, path: string, body?: unknown): Promise<unknown> {
  const headers: Record<string, string> = {}
  const opts: RequestInit = { method, credentials: 'include', headers }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    opts.body = JSON.stringify(body)
  }
  const res = await fetch(BASE + path, opts)
  if (!res.ok) {
    const txt = await res.text().catch(() => '')
    throw new HttpError(txt || `HTTP ${res.status}`, res.status)
  }
  const text = await res.text()
  if (!text) return null
  try { return JSON.parse(text) } catch { return null }
}

// listContacts reads the full-card list (optionally filtered by q), sorted by name.
export async function listContacts(q?: string): Promise<Contact[]> {
  const qs = new URLSearchParams({ limit: '500' })
  if (q && q.trim()) qs.set('q', q.trim())
  const data = await req('GET', '/cards?' + qs.toString())
  const raw: unknown[] = Array.isArray(data)
    ? data
    : isRecord(data) && Array.isArray(data.contacts)
      ? data.contacts
      : []
  return raw
    .map(normalizeContact)
    .filter((c): c is Contact => c !== null)
    .sort((a, b) => (a.name || '').localeCompare(b.name || ''))
}

// ContactFormInput — the shape the editor form (Contacts.tsx) hands to
// create/update.
export interface ContactFormInput {
  name?: string
  org?: string
  title?: string
  note?: string
  emails?: string[]
  phones?: string[]
}

interface ContactPayload {
  name: string
  emails?: string[]
  phones?: string[]
  org?: string
  title?: string
  note?: string
}

function contactPayload(c: ContactFormInput): ContactPayload {
  const body: ContactPayload = { name: c.name || '' }
  const emails = (c.emails || []).map((e) => e.trim()).filter(Boolean)
  const phones = (c.phones || []).map((p) => p.trim()).filter(Boolean)
  if (emails.length) body.emails = emails
  if (phones.length) body.phones = phones
  if (c.org?.trim()) body.org = c.org.trim()
  if (c.title?.trim()) body.title = c.title.trim()
  if (c.note?.trim()) body.note = c.note.trim()
  return body
}

export async function createContact(c: ContactFormInput): Promise<unknown> {
  return req('POST', '', contactPayload(c))
}

export async function updateContact(id: string, c: ContactFormInput): Promise<unknown> {
  return req('PUT', '/' + encodeURIComponent(id), contactPayload(c))
}

export async function deleteContact(id: string): Promise<unknown> {
  return req('DELETE', '/' + encodeURIComponent(id))
}
