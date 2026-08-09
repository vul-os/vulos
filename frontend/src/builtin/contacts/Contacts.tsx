/**
 * Contacts — the OS's standalone Contacts app (the "GNOME Contacts" of Vulos).
 *
 * A thin, self-contained address book over the box's PIM proxy
 * (/api/pim/contacts/*), which brokers to lilmail's /v1 contacts (CardDAV + any
 * OAuth-connected Google/Outlook address books lilmail aggregates). It owns NO
 * storage of its own — lilmail is the data server; this is the view.
 *
 * A two-pane list + detail with inline edit and full CRUD. Contact text (other
 * people's data) is rendered only as escaped React children, never HTML. A
 * backend failure degrades to an honest "Contacts unavailable — Connect Mail"
 * state (lilmail configures the account, incl. the OAuth "online accounts"
 * connect).
 */
import { useState, useEffect, useCallback, useMemo, type CSSProperties, type ReactNode } from 'react'
import { useShell } from '../../providers/ShellProvider'
import { getAppById } from '../../core/AppRegistry'
import { launchApp } from '../../shell/launchApp'
import { useNarrow } from '../../shell/useNarrow'
import { listContacts, createContact, updateContact, deleteContact } from './contactsApi'
import type { Contact, ContactFormInput } from './contactsApi'
import { nativeBridge } from '../../core/nativeBridge'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// A wire field is honoured only when it actually is the expected type —
// matches the original `a || b || ''` fallback chain for the realistic wire
// shape, while degrading a malformed field to the fallback instead of leaking
// an unexpected runtime type into the render tree.
function str(x: unknown): string {
  return typeof x === 'string' ? x : ''
}
function strList(x: unknown): string[] {
  return Array.isArray(x) ? x.filter((e): e is string => typeof e === 'string') : []
}

// errMessage extracts a real `.message` off a caught value honestly (`.catch`
// callers are always `unknown` under strict), matching the original
// `e?.message || fallback` for the realistic (Error-shaped) rejection.
function errMessage(e: unknown, fallback: string): string {
  return (isRecord(e) && typeof e.message === 'string' && e.message) || fallback
}

// Back chevron for the mobile single-pane view.
function BackChevron() {
  return (
    <svg viewBox="0 0 16 16" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><path d="M10 3L5 8l5 5" /></svg>
  )
}

// The single person mark used by every empty / unavailable state, so they all
// speak with one voice instead of three hand-rolled inline SVGs at three sizes.
function PersonGlyph({ className, stroke = 'currentColor' }: { className?: string; stroke?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill="none" stroke={stroke} strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="12" cy="8" r="3.5" /><path d="M4.5 20a7.5 7.5 0 0 1 15 0" />
    </svg>
  )
}

function initials(name: string): string {
  const parts = (name || '').trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0][0].toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}

// The form shape the create/edit editor works on — always has a trailing
// blank email/phone row so a new row appears as you type.
interface ContactForm {
  id: string
  name: string
  org: string
  title: string
  note: string
  emails: string[]
  phones: string[]
}

const EMPTY: ContactForm = { id: '', name: '', org: '', title: '', note: '', emails: [''], phones: [''] }

// A Contact annotated with which sources (Vulos/device/SIM) it was seen on —
// every row the list/detail panes render carries this.
interface MergedContact extends Contact {
  sources: string[]
  _readonly?: boolean
}

// ── Unified sources ────────────────────────────────────────────────────────
// The box merges the owner's CardDAV/Vulos cards with contacts pushed from the
// Android app (device + phone SIM) and a SIM plugged into the box itself, via
// GET /api/contacts/unified. Each merged contact carries a `sources` list; we
// badge it so it's clear where a contact lives (and that duplicates were fused).
const SOURCE_META: Record<string, { label: string; color: string }> = {
  vulos: { label: 'Vulos', color: 'var(--accent)' },
  phone: { label: 'Device', color: '#22C55E' },
  'box-sim': { label: 'Box SIM', color: '#F59E0B' },
}
const SOURCE_ORDER = ['vulos', 'phone', 'box-sim']

// CSSProperties has no index signature for CSS custom properties (Tailwind's
// ring color var), so a plain `CSSProperties` rejects the `--tw-ring-color`
// key — extend it rather than casting (mirrors core/AppIcons.tsx's TileStyle).
type RingStyle = CSSProperties & { '--tw-ring-color'?: string }

function SourceBadges({ sources }: { sources: string[] | undefined }) {
  const list = (sources || []).filter((s) => SOURCE_META[s])
  if (list.length === 0) return null
  return (
    <span className="inline-flex flex-wrap items-center gap-1 align-middle">
      {SOURCE_ORDER.filter((s) => list.includes(s)).map((s) => {
        const style: RingStyle = { color: SOURCE_META[s].color, background: `color-mix(in srgb, ${SOURCE_META[s].color} 14%, transparent)`, '--tw-ring-color': `color-mix(in srgb, ${SOURCE_META[s].color} 35%, transparent)` }
        return (
        <span key={s}
          className="inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[11.5px] font-medium leading-none ring-1 ring-inset"
          style={style}>
          <span className="w-1 h-1 rounded-full" style={{ background: SOURCE_META[s].color }} />
          {SOURCE_META[s].label}
        </span>
        )
      })}
    </span>
  )
}

// List-row source indicator. The labelled pills (above) are right for the detail
// header, but inline in a 17rem rail they wrapped onto two or three extra lines
// and shoved the contact's own NAME into an ellipsis ("Priya Men…"), giving the
// list a ragged, uneven row rhythm. In the rail we show the same information as
// a compact dot cluster with the labels in the accessible name, so every row is
// exactly one height and the name always wins the space.
function SourceDots({ sources }: { sources: string[] | undefined }) {
  const list = SOURCE_ORDER.filter((s) => (sources || []).includes(s) && SOURCE_META[s])
  if (list.length === 0) return null
  const labels = list.map((s) => SOURCE_META[s].label).join(', ')
  return (
    <span className="inline-flex items-center gap-[3px] shrink-0" title={`On ${labels}`}>
      {list.map((s) => (
        <span key={s} aria-hidden="true" className="w-1.5 h-1.5 rounded-full"
          style={{ background: SOURCE_META[s].color }} />
      ))}
      <span className="sr-only">{`On ${labels}`}</span>
    </span>
  )
}

// Alphabetical section key for the rail's sticky group headers.
function groupKey(name: string): string {
  const ch = (name || '').trim().charAt(0).toUpperCase()
  return /[A-Z]/.test(ch) ? ch : '#'
}

// keyOf — a match key set (by email + normalized name) shared by both the
// editable CardDAV list (Contact) and raw unified-endpoint records.
function keyOf(x: { name: string; emails: string[] }): string[] {
  return [
    ...x.emails.map((e) => 'e:' + e.toLowerCase().trim()),
    'n:' + x.name.toLowerCase().trim(),
  ]
}

// Merge the unified list onto the editable CardDAV list: annotate each CardDAV
// contact with its sources (matched by email/name), and append device/SIM-only
// contacts as read-only rows. Falls back to plain CardDAV when unified is off.
function mergeUnified(cardContacts: Contact[], unified: Record<string, unknown>[]): MergedContact[] {
  const uByKey = new Map<string, Record<string, unknown>>()
  for (const u of unified) for (const k of keyOf({ name: str(u.name), emails: strList(u.emails) })) if (!uByKey.has(k)) uByKey.set(k, u)

  const matchedU = new Set<Record<string, unknown>>()
  const annotated: MergedContact[] = cardContacts.map((c) => {
    let sources = ['vulos']
    for (const k of keyOf(c)) {
      const u = uByKey.get(k)
      if (u) { sources = Array.isArray(u.sources) ? strList(u.sources) : sources; matchedU.add(u); break }
    }
    return { ...c, sources }
  })

  const extras: MergedContact[] = unified
    .filter((u) => !matchedU.has(u) && !strList(u.sources).includes('vulos'))
    .map((u) => ({
      id: 'u:' + (str(u.id) || str(u.name)), name: str(u.name) || '(no name)', org: str(u.org), title: '',
      note: str(u.note), emails: strList(u.emails), phones: strList(u.phones),
      sources: strList(u.sources), _readonly: true,
    }))
  return [...annotated, ...extras]
}

export default function Contacts() {
  const { openWindow } = useShell()
  const [contacts, setContacts] = useState<MergedContact[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [query, setQuery] = useState('')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [editing, setEditing] = useState<ContactForm | null>(null)
  const [saving, setSaving] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    // The editable CardDAV list is authoritative for create/edit/delete; the
    // unified list (best-effort) adds source badges + any device/SIM-only rows.
    const unified: Promise<Record<string, unknown>[]> = fetch('/api/contacts/unified')
      .then((r) => (r.ok ? r.json() : null))
      .then((d: unknown) => (isRecord(d) && Array.isArray(d.contacts) ? d.contacts.filter(isRecord) : []))
      .catch(() => [])
    listContacts()
      .then(async (cs) => { setContacts(mergeUnified(cs, await unified)); setError('') })
      .catch((e: unknown) => { setError(errMessage(e, 'unavailable')); setContacts([]) })
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    const matched = !q ? contacts : contacts.filter((c) =>
      c.name.toLowerCase().includes(q) ||
      c.org.toLowerCase().includes(q) ||
      c.emails.some((e) => e.toLowerCase().includes(q)))
    // An address book is read alphabetically. The wire order is the merge order
    // (CardDAV first, then device/SIM-only extras appended), which read as
    // random — "Mum" landed after "Thabo" purely because it came from the SIM.
    return [...matched].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }))
  }, [contacts, query])

  // Sticky A–Z section headers, derived from the (already sorted) list.
  const grouped = useMemo(() => {
    const out: { letter: string; items: MergedContact[] }[] = []
    for (const c of filtered) {
      const k = groupKey(c.name)
      const tail = out[out.length - 1]
      if (tail && tail.letter === k) tail.items.push(c)
      else out.push({ letter: k, items: [c] })
    }
    return out
  }, [filtered])

  const selected = useMemo(
    () => contacts.find((c) => c.id === selectedId) || null,
    [contacts, selectedId])

  const startCreate = () => setEditing({ ...EMPTY })
  const startEdit = (c: MergedContact) => setEditing({
    id: c.id, name: c.name, org: c.org, title: c.title, note: c.note,
    emails: c.emails.length ? [...c.emails] : [''],
    phones: c.phones.length ? [...c.phones] : [''],
  })

  const save = async () => {
    if (!editing) return
    setSaving(true)
    const payload: ContactFormInput = {
      name: editing.name.trim() || '(no name)',
      org: editing.org, title: editing.title, note: editing.note,
      emails: editing.emails, phones: editing.phones,
    }
    try {
      const res = editing.id ? await updateContact(editing.id, payload) : await createContact(payload)
      setEditing(null)
      const contactField = isRecord(res) && isRecord(res.contact) ? res.contact : null
      const savedId = (contactField && typeof contactField.uid === 'string' && contactField.uid)
        || (isRecord(res) && typeof res.uid === 'string' && res.uid)
        || editing.id
      if (savedId) setSelectedId(savedId)
      load()
    } catch (e: unknown) {
      setError(errMessage(e, 'save failed'))
    } finally {
      setSaving(false)
    }
  }

  const remove = async (c: MergedContact | null) => {
    if (!c?.id) return
    setSaving(true)
    try {
      await deleteContact(c.id)
      if (selectedId === c.id) setSelectedId(null)
      load()
    } catch (e: unknown) {
      setError(errMessage(e, 'delete failed'))
    } finally {
      setSaving(false)
    }
  }

  const connectMail = () => {
    const app = getAppById('lilmail')
    if (app) launchApp(app, { openWindow })
  }

  // NATIVE (Android app only): read the phone's device + SIM address book via the
  // native bridge and push it to the box, which merges it into the unified list
  // (see backend/services/contacts). No-op / hidden in a plain browser.
  const [syncingPhone, setSyncingPhone] = useState(false)
  const canSyncPhone = nativeBridge.contacts.available
  const syncPhone = async () => {
    setSyncingPhone(true)
    try {
      const [device, sim]: [DeviceContact[], DeviceContact[]] = await Promise.all([
        nativeBridge.contacts.list().catch(() => []),
        nativeBridge.contacts.sim().catch(() => []),
      ])
      const deviceContacts = [...device, ...sim].filter((c) => c && (c.name || (c.phones && c.phones.length)))
      const res = await fetch('/api/contacts/ingest/device', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ contacts: deviceContacts }),
      })
      if (!res.ok) throw new Error('sync failed')
      load()
    } catch (e: unknown) {
      setError(isRecord(e) && e.message === 'native-unavailable' ? 'Phone sync is only available in the Vulos app.' : 'Could not sync phone contacts.')
    } finally {
      setSyncingPhone(false)
    }
  }

  const unavailable = !!error && contacts.length === 0

  // MOBILE-ADAPTIVE: below `sm` show ONE pane — the list, or the detail/editor
  // (with a back button) once a contact is picked. The desktop layout keeps
  // both panes side by side.
  const narrow = useNarrow()
  const hasDetail = !!editing || !!selected
  const showList = !narrow || !hasDetail
  const showDetail = !narrow || hasDetail
  const backToList = () => { setEditing(null); setSelectedId(null) }

  if (unavailable) {
    return (
      <div className="h-full grid place-items-center bg-neutral-950 text-center px-6 py-10 overflow-y-auto animate-[fadeIn_0.2s_ease-out]" data-contacts-app>
        <div className="w-full max-w-sm">
          <div className="w-16 h-16 mx-auto mb-5 grid place-items-center rounded-2xl border border-neutral-800"
            style={{ background: 'var(--accent-soft)' }}>
            <PersonGlyph className="w-8 h-8" stroke="var(--accent)" />
          </div>
          <div className="text-neutral-100 text-[16px] font-semibold tracking-tight">Contacts unavailable.</div>
          <p className="text-neutral-500 text-[13px] mt-2 leading-relaxed text-balance">
            Connect a mail account (Gmail, Outlook, or IMAP/CardDAV) to see and manage your contacts.
          </p>
          <button type="button" onClick={connectMail}
            className="mt-5 inline-flex items-center gap-2 text-[13px] font-medium px-4 h-10 rounded-lg text-white transition-all hover:brightness-110 active:scale-[0.98] focus-primary shadow-sm"
            style={{ background: 'var(--accent)' }}>
            Connect Mail →
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="h-full flex bg-neutral-950 text-neutral-100 select-none" data-contacts-app>
      {/* List rail — full width on mobile, a sized rail on desktop. The rail
          grows a step at ≥1280px so long names/addresses stop truncating on the
          wide desktop the founder actually runs it on. */}
      {showList && (
      <div className={`${narrow ? 'w-full' : 'w-[17.5rem] xl:w-[20rem] shrink-0 border-r border-neutral-800/70 bg-neutral-900/25'} flex flex-col min-h-0`}>
        <div className="shrink-0 px-3 pt-3 pb-2.5 border-b border-neutral-800/70">
          <div className="flex items-center gap-2 mb-2.5">
            <h1 className="text-[15px] font-semibold tracking-tight text-neutral-100">Contacts</h1>
            {!loading && (
              <span className="mono text-[11.5px] px-1.5 py-0.5 rounded-md bg-neutral-800/70 text-neutral-500 leading-none">
                {contacts.length}
              </span>
            )}
            <div className="ml-auto flex items-center gap-1.5">
              {canSyncPhone && (
                <button type="button" onClick={syncPhone} disabled={syncingPhone} aria-label="Sync phone contacts"
                  title="Sync your phone's device + SIM contacts into this box"
                  className="w-9 h-9 shrink-0 grid place-items-center rounded-lg border border-neutral-800 text-neutral-400 hover:bg-neutral-800/60 hover:text-neutral-100 hover:border-neutral-700 active:scale-95 focus-primary transition-colors disabled:opacity-50">
                  {syncingPhone
                    ? <span className="w-4 h-4 spinner" />
                    : <svg viewBox="0 0 20 20" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"><rect x="6" y="2.5" width="8" height="15" rx="2"/><path d="M9 14.5h2"/></svg>}
                </button>
              )}
              <button type="button" onClick={startCreate} aria-label="New contact" title="New contact"
                className="w-9 h-9 shrink-0 grid place-items-center rounded-lg text-white transition-all hover:brightness-110 active:scale-95 focus-primary shadow-sm"
                style={{ background: 'var(--accent)' }}>
                <svg viewBox="0 0 16 16" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round"><path d="M8 3.5v9M3.5 8h9" /></svg>
              </button>
            </div>
          </div>
          <div className="relative">
            <svg viewBox="0 0 16 16" className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-neutral-500 pointer-events-none" fill="none" stroke="currentColor" strokeWidth="1.5"><circle cx="7" cy="7" r="4.5"/><path d="M11 11l3 3" strokeLinecap="round"/></svg>
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search contacts"
              aria-label="Search contacts"
              className="w-full min-w-0 h-9 bg-neutral-800/50 border border-neutral-800 rounded-lg pl-8 pr-8 text-[13px] text-neutral-100 placeholder-neutral-600 focus-primary transition-colors focus:border-neutral-700"
            />
            {query && (
              <button type="button" onClick={() => setQuery('')} aria-label="Clear search"
                className="absolute right-1 top-1/2 -translate-y-1/2 w-7 h-7 grid place-items-center rounded-md text-neutral-600 hover:text-neutral-200 transition-colors focus-primary">
                <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"><path d="M4 4l8 8M12 4l-8 8" /></svg>
              </button>
            )}
          </div>
        </div>
        <div className="flex-1 overflow-y-auto overscroll-contain">
          {loading && contacts.length === 0 ? (
            // Skeleton rows rather than a lone "Loading…" line, so the rail keeps
            // its shape and the list doesn't jump when the real rows land.
            <ul className="py-2 px-2 flex flex-col gap-1.5" aria-hidden="true">
              {[0, 1, 2, 3, 4, 5].map((i) => (
                <li key={i} className="flex items-center gap-2.5 px-1.5 py-1.5 animate-pulse" style={{ opacity: 1 - i * 0.13 }}>
                  <span className="w-9 h-9 shrink-0 rounded-full bg-neutral-800/80" />
                  <span className="flex-1 min-w-0 flex flex-col gap-1.5">
                    <span className="block h-2.5 rounded-full bg-neutral-800/80" style={{ width: `${58 + ((i * 13) % 30)}%` }} />
                    <span className="block h-2 rounded-full bg-neutral-800/50" style={{ width: `${38 + ((i * 17) % 34)}%` }} />
                  </span>
                </li>
              ))}
              <li className="sr-only" aria-hidden="false">Loading…</li>
            </ul>
          ) : filtered.length === 0 ? (
            <div className="px-6 py-10 text-center animate-[fadeIn_0.2s_ease-out]">
              <div className="w-11 h-11 mx-auto mb-3 grid place-items-center rounded-xl border border-neutral-800 text-neutral-700">
                <PersonGlyph className="w-5 h-5" />
              </div>
              <div className="text-[13px] text-neutral-400 font-medium">{query ? 'No matches.' : 'No contacts yet.'}</div>
              <div className="text-[12px] text-neutral-600 mt-1 leading-relaxed">
                {query ? 'Try a different name, address or company.' : 'Add someone, or connect a mail account to sync an address book.'}
              </div>
              {!query && (
                <button type="button" onClick={startCreate}
                  className="mt-3.5 text-[12.5px] font-medium px-3 h-8 rounded-lg border border-neutral-800 text-neutral-300 hover:bg-neutral-800/60 hover:border-neutral-700 transition-colors focus-primary">
                  New contact
                </button>
              )}
            </div>
          ) : (
            <div className="pb-3">
              {grouped.map((g) => (
                <section key={g.letter}>
                  <h2 className="sticky top-0 z-10 mono px-3.5 py-1 text-[11px] font-semibold tracking-[0.12em] text-neutral-600 bg-neutral-950/85 backdrop-blur-sm border-b border-neutral-900">
                    {g.letter}
                  </h2>
                  <ul>
                    {g.items.map((c) => {
                      const active = selectedId === c.id
                      return (
                      <li key={c.id || c.name}>
                        <button type="button" onClick={() => { setSelectedId(c.id); setEditing(null) }}
                          aria-current={active ? 'true' : undefined}
                          className={`group w-full flex items-center gap-2.5 pl-3 pr-2.5 py-2 text-left transition-colors focus-primary border-l-2 ${active ? 'bg-neutral-800/70 border-[var(--accent)]' : 'border-transparent hover:bg-neutral-800/40'}`}>
                          <span className="w-9 h-9 shrink-0 grid place-items-center rounded-full text-[12px] mono font-semibold ring-1 ring-inset ring-white/5"
                            style={{ background: 'var(--accent-soft)', color: 'var(--accent)' }}>{initials(c.name)}</span>
                          <span className="min-w-0 flex-1">
                            <span className="flex items-center gap-1.5">
                              <span className={`block text-[13px] truncate ${active ? 'text-neutral-50 font-medium' : 'text-neutral-100'}`}>{c.name || '(no name)'}</span>
                              {(c.sources.length > 1 || c.sources.some((s) => s !== 'vulos')) && (
                                <SourceDots sources={c.sources} />
                              )}
                            </span>
                            <span className="block text-[12px] text-neutral-500 truncate leading-snug">
                              {c.emails[0] || c.phones[0] || c.org || '—'}
                            </span>
                          </span>
                        </button>
                      </li>
                      )
                    })}
                  </ul>
                </section>
              ))}
            </div>
          )}
        </div>
      </div>
      )}

      {/* Detail / editor pane. Its content is width-capped and centred: at 1600px
          the old `flex-1` pane left a 16px avatar and one line of text stranded
          in ~1300px of empty canvas, with Edit/Delete flung to the far edge. */}
      {showDetail && (
      <div className="flex-1 min-w-0 flex flex-col bg-neutral-950">
        {narrow && hasDetail && (
          <button type="button" onClick={backToList}
            className="shrink-0 flex items-center gap-1.5 px-3 h-11 text-[13px] text-neutral-400 border-b border-neutral-800/70 focus-primary">
            <BackChevron /> Contacts
          </button>
        )}
        <div className="flex-1 min-h-0 overflow-y-auto overscroll-contain">
          {editing ? (
            <ContactEditor form={editing} setForm={(fn) => setEditing((f) => (f ? fn(f) : f))} onSave={save}
              onCancel={() => { if (narrow) backToList(); else setEditing(null) }} saving={saving} />
          ) : selected ? (
            <ContactDetail contact={selected} onEdit={() => startEdit(selected)}
              onDelete={() => remove(selected)} saving={saving} />
          ) : (
            <div className="h-full grid place-items-center text-center px-6 py-10 animate-[fadeIn_0.2s_ease-out]">
              <div className="max-w-xs">
                <div className="w-14 h-14 mx-auto mb-4 grid place-items-center rounded-2xl border border-neutral-800/80 text-neutral-700 bg-neutral-900/40">
                  <PersonGlyph className="w-7 h-7" />
                </div>
                <div className="text-[14px] text-neutral-400 font-medium">No contact selected</div>
                <div className="text-[12.5px] text-neutral-600 mt-1.5 leading-relaxed">
                  Pick someone from the list to see their card, or add a new one.
                </div>
              </div>
            </div>
          )}
        </div>
      </div>
      )}
    </div>
  )
}

interface ContactDetailProps {
  contact: MergedContact
  onEdit: () => void
  onDelete: () => void
  saving: boolean
}

function ContactDetail({ contact, onEdit, onDelete, saving }: ContactDetailProps) {
  return (
    <div className="p-6 animate-[fadeIn_0.18s_ease-out]" data-contact-detail>
      <div className="flex items-center gap-4">
        <span className="w-16 h-16 shrink-0 grid place-items-center rounded-full text-[22px] font-mono font-semibold ring-1 ring-inset ring-white/5"
          style={{ background: 'var(--accent-soft)', color: 'var(--accent)' }}>{initials(contact.name)}</span>
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <div className="text-[19px] font-semibold truncate tracking-tight">{contact.name || '(no name)'}</div>
            <SourceBadges sources={contact.sources} />
          </div>
          {(contact.title || contact.org) && (
            <div className="text-[13px] text-neutral-400 truncate">
              {[contact.title, contact.org].filter(Boolean).join(' · ')}
            </div>
          )}
        </div>
        {contact._readonly ? (
          <div className="ml-auto text-[12px] text-neutral-500 max-w-[9rem] text-right leading-snug">
            From your phone — edit it on the device it lives on.
          </div>
        ) : (
          <div className="ml-auto flex items-center gap-2">
            <button type="button" onClick={onEdit}
              className="text-[12px] px-3 py-2 rounded-md border border-neutral-700 hover:bg-neutral-800/60 hover:border-neutral-600 transition-colors focus-primary">Edit</button>
            <button type="button" onClick={onDelete} disabled={saving}
              className="text-[12px] px-3 py-2 rounded-md text-danger hover:bg-danger-soft transition-colors focus-primary disabled:opacity-50">Delete</button>
          </div>
        )}
      </div>

      <div className="mt-6 flex flex-col gap-4">
        {contact.emails.length > 0 && (
          <Field label="Email">
            <div className="flex flex-col gap-1">
              {contact.emails.map((e, i) => (
                <a key={i} href={`mailto:${e}`} className="flex items-center gap-2 text-[13px] px-2 py-1.5 -mx-2 rounded-md hover:bg-neutral-800/50 transition-colors focus-primary" style={{ color: 'var(--accent)' }}>
                  <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 shrink-0 opacity-70" fill="none" stroke="currentColor" strokeWidth="1.3"><rect x="1.5" y="3" width="13" height="10" rx="1.5"/><path d="M2 4l6 4.5L14 4"/></svg>
                  <span className="truncate">{e}</span>
                </a>
              ))}
            </div>
          </Field>
        )}
        {contact.phones.length > 0 && (
          <Field label="Phone">
            <div className="flex flex-col gap-1">
              {contact.phones.map((p, i) => (
                <a key={i} href={`tel:${p}`} className="flex items-center gap-2 text-[13px] text-neutral-200 px-2 py-1.5 -mx-2 rounded-md hover:bg-neutral-800/50 transition-colors focus-primary">
                  <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 shrink-0 text-neutral-500" fill="none" stroke="currentColor" strokeWidth="1.3"><path d="M3 2.5h2.5l1 3-1.5 1a8 8 0 0 0 3.5 3.5l1-1.5 3 1V15c-6 0-11-5-11-11z" strokeLinejoin="round"/></svg>
                  <span className="truncate">{p}</span>
                </a>
              ))}
            </div>
          </Field>
        )}
        {contact.note && (
          <Field label="Notes">
            <p className="text-[13px] text-neutral-300 whitespace-pre-wrap">{contact.note}</p>
          </Field>
        )}
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <div className="text-[12px] font-mono uppercase tracking-wider text-neutral-500 mb-1">{label}</div>
      {children}
    </div>
  )
}

interface ContactEditorProps {
  form: ContactForm
  setForm: (fn: (f: ContactForm) => ContactForm) => void
  onSave: () => void
  onCancel: () => void
  saving: boolean
}

function ContactEditor({ form, setForm, onSave, onCancel, saving }: ContactEditorProps) {
  const set = (patch: Partial<ContactForm>) => setForm((f) => ({ ...f, ...patch }))
  const setList = (key: 'emails' | 'phones', i: number, v: string) => setForm((f) => {
    const arr = [...f[key]]; arr[i] = v
    // keep exactly one trailing blank so a new row appears as you type
    const cleaned = arr.filter((x, idx) => x.trim() || idx === arr.length - 1)
    if (cleaned[cleaned.length - 1]?.trim()) cleaned.push('')
    return { ...f, [key]: cleaned }
  })
  return (
    <div className="p-6 max-w-md animate-[fadeIn_0.18s_ease-out]" data-contact-editor>
      <div className="flex items-center gap-2 text-[15px] font-semibold mb-4 tracking-tight">
        <span className="w-1.5 h-5 rounded-full" style={{ background: 'var(--accent)' }} />
        {form.id ? 'Edit contact' : 'New contact'}
      </div>
      <div className="flex flex-col gap-3">
        <Input label="Name" value={form.name} onChange={(v) => set({ name: v })} autoFocus />
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
          <Input label="Title" value={form.title} onChange={(v) => set({ title: v })} />
          <Input label="Organization" value={form.org} onChange={(v) => set({ org: v })} />
        </div>

        <ListField label="Email" type="email" values={form.emails} onChange={(i, v) => setList('emails', i, v)} />
        <ListField label="Phone" type="tel" values={form.phones} onChange={(i, v) => setList('phones', i, v)} />

        <label className="flex flex-col gap-1">
          <span className="text-[12px] font-mono uppercase tracking-wider text-neutral-500">Notes</span>
          <textarea value={form.note} onChange={(e) => set({ note: e.target.value })} rows={3}
            className="bg-neutral-800/60 border border-neutral-700/80 rounded-md px-2.5 py-1.5 text-[13px] transition-colors focus:border-neutral-600 resize-none focus-primary" />
        </label>
      </div>
      <div className="mt-5 flex items-center gap-2">
        <button type="button" onClick={onCancel} disabled={saving}
          className="ml-auto text-[12px] px-3 py-1.5 rounded-md border border-neutral-700 hover:bg-neutral-800/60 focus-primary disabled:opacity-50">Cancel</button>
        <button type="button" onClick={onSave} disabled={saving}
          className="text-[12px] px-3 py-1.5 rounded-md text-white font-medium transition-all hover:brightness-110 active:scale-[0.97] focus-primary disabled:opacity-50 shadow-sm"
          style={{ background: 'var(--accent)' }}>{saving ? 'Saving…' : 'Save'}</button>
      </div>
    </div>
  )
}

interface InputProps {
  label: string
  value: string
  onChange: (v: string) => void
  autoFocus?: boolean
}

function Input({ label, value, onChange, autoFocus }: InputProps) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[12px] font-mono uppercase tracking-wider text-neutral-500">{label}</span>
      <input autoFocus={autoFocus} value={value} onChange={(e) => onChange(e.target.value)}
        className="bg-neutral-800/60 border border-neutral-700/80 rounded-md px-2.5 py-1.5 text-[13px] transition-colors focus:border-neutral-600 focus-primary" />
    </label>
  )
}

interface ListFieldProps {
  label: string
  type: string
  values: string[]
  onChange: (i: number, v: string) => void
}

function ListField({ label, type, values, onChange }: ListFieldProps) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-[12px] font-mono uppercase tracking-wider text-neutral-500">{label}</span>
      <div className="flex flex-col gap-1.5">
        {values.map((v, i) => (
          <input key={i} type={type} value={v} onChange={(e) => onChange(i, e.target.value)}
            aria-label={`${label} ${i + 1}`}
            placeholder={type === 'email' ? 'name@example.com' : ''}
            className="bg-neutral-800/60 border border-neutral-700/80 rounded-md px-2.5 py-1.5 text-[13px] transition-colors focus:border-neutral-600 focus-primary" />
        ))}
      </div>
    </div>
  )
}

// DeviceContact — the shape read off the native bridge's device/SIM address
// book (mobile/app/.../BridgeBase.kt; see core/nativeBridge.js, untyped/out of
// scope). Restated here (like CalendarWidget.tsx restates CalendarEvent) so
// the sync path gets a real type instead of inheriting `any` from the
// untyped bridge.
interface DeviceContact {
  name?: string
  phones?: string[]
}
