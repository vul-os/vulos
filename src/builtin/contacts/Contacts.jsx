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
import { useState, useEffect, useCallback, useMemo } from 'react'
import { useShell } from '../../providers/ShellProvider'
import { getAppById } from '../../core/AppRegistry'
import { launchApp } from '../../shell/launchApp'
import { listContacts, createContact, updateContact, deleteContact } from './contactsApi'

function initials(name) {
  const parts = (name || '').trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0][0].toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}

const EMPTY = { id: '', name: '', org: '', title: '', note: '', emails: [''], phones: [''] }

export default function Contacts() {
  const { openWindow } = useShell()
  const [contacts, setContacts] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [query, setQuery] = useState('')
  const [selectedId, setSelectedId] = useState(null)
  const [editing, setEditing] = useState(null) // form object or null
  const [saving, setSaving] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    listContacts()
      .then((cs) => { setContacts(cs); setError('') })
      .catch((e) => { setError(e?.message || 'unavailable'); setContacts([]) })
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return contacts
    return contacts.filter((c) =>
      c.name.toLowerCase().includes(q) ||
      c.org.toLowerCase().includes(q) ||
      c.emails.some((e) => e.toLowerCase().includes(q)))
  }, [contacts, query])

  const selected = useMemo(
    () => contacts.find((c) => c.id === selectedId) || null,
    [contacts, selectedId])

  const startCreate = () => setEditing({ ...EMPTY })
  const startEdit = (c) => setEditing({
    id: c.id, name: c.name, org: c.org, title: c.title, note: c.note,
    emails: c.emails.length ? [...c.emails] : [''],
    phones: c.phones.length ? [...c.phones] : [''],
  })

  const save = async () => {
    if (!editing) return
    setSaving(true)
    const payload = {
      name: editing.name.trim() || '(no name)',
      org: editing.org, title: editing.title, note: editing.note,
      emails: editing.emails, phones: editing.phones,
    }
    try {
      const res = editing.id ? await updateContact(editing.id, payload) : await createContact(payload)
      setEditing(null)
      const savedId = res?.contact?.uid || res?.uid || editing.id
      if (savedId) setSelectedId(savedId)
      load()
    } catch (e) {
      setError(e?.message || 'save failed')
    } finally {
      setSaving(false)
    }
  }

  const remove = async (c) => {
    if (!c?.id) return
    setSaving(true)
    try {
      await deleteContact(c.id)
      if (selectedId === c.id) setSelectedId(null)
      load()
    } catch (e) {
      setError(e?.message || 'delete failed')
    } finally {
      setSaving(false)
    }
  }

  const connectMail = () => {
    const app = getAppById('lilmail')
    if (app) launchApp(app, { openWindow })
  }

  const unavailable = !!error && contacts.length === 0

  if (unavailable) {
    return (
      <div className="h-full grid place-items-center bg-neutral-950 text-center px-6" data-contacts-app>
        <div>
          <div className="text-neutral-300 text-sm">Contacts unavailable.</div>
          <div className="text-neutral-500 text-[12px] mt-1 max-w-xs mx-auto">
            Connect a mail account (Gmail, Outlook, or IMAP/CardDAV) to see and manage your contacts.
          </div>
          <button type="button" onClick={connectMail}
            className="mt-3 text-[12px] font-mono px-3 py-1.5 rounded-md border border-neutral-700 hover:bg-neutral-800/60 focus-primary">
            Connect Mail →
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="h-full flex bg-neutral-950 text-neutral-100 select-none" data-contacts-app>
      {/* List pane */}
      <div className="w-64 shrink-0 border-r border-neutral-800/70 flex flex-col min-h-0">
        <div className="p-2.5 border-b border-neutral-800/70 flex items-center gap-2">
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search contacts"
            aria-label="Search contacts"
            className="flex-1 min-w-0 bg-neutral-800/60 border border-neutral-700 rounded-md px-2.5 py-1.5 text-[12px] focus-primary"
          />
          <button type="button" onClick={startCreate} aria-label="New contact"
            className="w-8 h-8 shrink-0 grid place-items-center rounded-md text-white focus-primary"
            style={{ background: 'var(--accent)' }}>+</button>
        </div>
        <div className="flex-1 overflow-y-auto">
          {loading && contacts.length === 0 ? (
            <div className="p-4 text-neutral-500 text-[12px]">Loading…</div>
          ) : filtered.length === 0 ? (
            <div className="p-4 text-neutral-500 text-[12px]">{query ? 'No matches.' : 'No contacts yet.'}</div>
          ) : (
            <ul>
              {filtered.map((c) => (
                <li key={c.id || c.name}>
                  <button type="button" onClick={() => { setSelectedId(c.id); setEditing(null) }}
                    className={`w-full flex items-center gap-2.5 px-2.5 py-2 text-left transition-colors focus-primary ${selectedId === c.id ? 'bg-neutral-800/70' : 'hover:bg-neutral-800/40'}`}>
                    <span className="w-8 h-8 shrink-0 grid place-items-center rounded-full text-[11px] font-mono"
                      style={{ background: 'var(--accent-soft)', color: 'var(--accent)' }}>{initials(c.name)}</span>
                    <span className="min-w-0">
                      <span className="block text-[12.5px] text-neutral-100 truncate">{c.name || '(no name)'}</span>
                      {c.emails[0] && <span className="block text-[10.5px] text-neutral-500 truncate">{c.emails[0]}</span>}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>

      {/* Detail / editor pane */}
      <div className="flex-1 min-w-0 overflow-y-auto">
        {editing ? (
          <ContactEditor form={editing} setForm={setEditing} onSave={save}
            onCancel={() => setEditing(null)} saving={saving} />
        ) : selected ? (
          <ContactDetail contact={selected} onEdit={() => startEdit(selected)}
            onDelete={() => remove(selected)} saving={saving} />
        ) : (
          <div className="h-full grid place-items-center text-neutral-600 text-[13px]">
            Select a contact, or add a new one.
          </div>
        )}
      </div>
    </div>
  )
}

function ContactDetail({ contact, onEdit, onDelete, saving }) {
  return (
    <div className="p-6" data-contact-detail>
      <div className="flex items-center gap-4">
        <span className="w-16 h-16 grid place-items-center rounded-full text-[22px] font-mono"
          style={{ background: 'var(--accent-soft)', color: 'var(--accent)' }}>{initials(contact.name)}</span>
        <div className="min-w-0">
          <div className="text-[19px] font-semibold truncate">{contact.name || '(no name)'}</div>
          {(contact.title || contact.org) && (
            <div className="text-[13px] text-neutral-400 truncate">
              {[contact.title, contact.org].filter(Boolean).join(' · ')}
            </div>
          )}
        </div>
        <div className="ml-auto flex items-center gap-2">
          <button type="button" onClick={onEdit}
            className="text-[12px] px-3 py-1.5 rounded-md border border-neutral-700 hover:bg-neutral-800/60 focus-primary">Edit</button>
          <button type="button" onClick={onDelete} disabled={saving}
            className="text-[12px] px-3 py-1.5 rounded-md text-red-300 hover:bg-red-500/10 focus-primary disabled:opacity-50">Delete</button>
        </div>
      </div>

      <div className="mt-6 flex flex-col gap-4">
        {contact.emails.length > 0 && (
          <Field label="Email">
            {contact.emails.map((e, i) => (
              <a key={i} href={`mailto:${e}`} className="block text-[13px] hover:underline" style={{ color: 'var(--accent)' }}>{e}</a>
            ))}
          </Field>
        )}
        {contact.phones.length > 0 && (
          <Field label="Phone">
            {contact.phones.map((p, i) => (
              <a key={i} href={`tel:${p}`} className="block text-[13px] text-neutral-200 hover:underline">{p}</a>
            ))}
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

function Field({ label, children }) {
  return (
    <div>
      <div className="text-[10px] font-mono uppercase tracking-wider text-neutral-500 mb-1">{label}</div>
      {children}
    </div>
  )
}

function ContactEditor({ form, setForm, onSave, onCancel, saving }) {
  const set = (patch) => setForm((f) => ({ ...f, ...patch }))
  const setList = (key, i, v) => setForm((f) => {
    const arr = [...f[key]]; arr[i] = v
    // keep exactly one trailing blank so a new row appears as you type
    const cleaned = arr.filter((x, idx) => x.trim() || idx === arr.length - 1)
    if (cleaned[cleaned.length - 1]?.trim()) cleaned.push('')
    return { ...f, [key]: cleaned }
  })
  return (
    <div className="p-6 max-w-md" data-contact-editor>
      <div className="text-[15px] font-semibold mb-4">{form.id ? 'Edit contact' : 'New contact'}</div>
      <div className="flex flex-col gap-3">
        <Input label="Name" value={form.name} onChange={(v) => set({ name: v })} autoFocus />
        <div className="grid grid-cols-2 gap-2">
          <Input label="Title" value={form.title} onChange={(v) => set({ title: v })} />
          <Input label="Organization" value={form.org} onChange={(v) => set({ org: v })} />
        </div>

        <ListField label="Email" type="email" values={form.emails} onChange={(i, v) => setList('emails', i, v)} />
        <ListField label="Phone" type="tel" values={form.phones} onChange={(i, v) => setList('phones', i, v)} />

        <label className="flex flex-col gap-1">
          <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-500">Notes</span>
          <textarea value={form.note} onChange={(e) => set({ note: e.target.value })} rows={3}
            className="bg-neutral-800/60 border border-neutral-700 rounded-md px-2.5 py-1.5 text-[13px] resize-none focus-primary" />
        </label>
      </div>
      <div className="mt-5 flex items-center gap-2">
        <button type="button" onClick={onCancel} disabled={saving}
          className="ml-auto text-[12px] px-3 py-1.5 rounded-md border border-neutral-700 hover:bg-neutral-800/60 focus-primary disabled:opacity-50">Cancel</button>
        <button type="button" onClick={onSave} disabled={saving}
          className="text-[12px] px-3 py-1.5 rounded-md text-white focus-primary disabled:opacity-50"
          style={{ background: 'var(--accent)' }}>{saving ? 'Saving…' : 'Save'}</button>
      </div>
    </div>
  )
}

function Input({ label, value, onChange, autoFocus }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-500">{label}</span>
      <input autoFocus={autoFocus} value={value} onChange={(e) => onChange(e.target.value)}
        className="bg-neutral-800/60 border border-neutral-700 rounded-md px-2.5 py-1.5 text-[13px] focus-primary" />
    </label>
  )
}

function ListField({ label, type, values, onChange }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-500">{label}</span>
      <div className="flex flex-col gap-1.5">
        {values.map((v, i) => (
          <input key={i} type={type} value={v} onChange={(e) => onChange(i, e.target.value)}
            aria-label={`${label} ${i + 1}`}
            placeholder={type === 'email' ? 'name@example.com' : ''}
            className="bg-neutral-800/60 border border-neutral-700 rounded-md px-2.5 py-1.5 text-[13px] focus-primary" />
        ))}
      </div>
    </div>
  )
}
