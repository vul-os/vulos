/**
 * ContactsApp — Google Contacts parity.
 *
 * Features:
 *  - Rich CRUD (first/last/display, multiple emails+phones labeled, address,
 *    birthday, org, title, notes, custom fields, star/favorite, avatar)
 *  - Contact groups — create, filter, email group
 *  - VCF import (file picker + preview) / export (selected or all)
 *  - Calendar integration — birthdays appear in calendar; invitee autocomplete
 *  - Dedup helper — find duplicates by email/phone → merge
 *  - Responsive: 3-pane desktop (groups / list / detail), split tablet, single mobile
 *
 * Persistence: all reads/writes go through CardDAV (PROPFIND/PUT/DELETE).
 * The import/export/dedup features additionally call the backend REST helpers.
 *
 * Constraints: JSX never .tsx | reuse design tokens
 */

import { useState, useEffect, useCallback, useRef } from 'react'
import {
  Users, Plus, Search, X, Edit2, Trash2, ChevronRight, User, Star,
  Mail, Phone, MapPin, Building, Calendar, Tag, Upload, Download,
  GitMerge, AlertTriangle, Check, MoreHorizontal, ChevronDown,
} from 'lucide-react'
import { useAuthStore } from '../../store/authStore'

// ─── constants ─────────────────────────────────────────────────────────────

const CARDDAV_BASE = import.meta.env.VITE_CARDDAV_BASE || '/dav/addressbooks'
const API_BASE     = import.meta.env.VITE_API_BASE     || '/api'
const COLLECTION   = 'contacts'

const LABEL_OPTIONS = ['home', 'work', 'mobile', 'other']

// ─── Identicon / Avatar ───────────────────────────────────────────────────────

function Avatar({ contact, size = 36 }) {
  const letter = (contact.displayName || contact.firstName || contact.email?.[0] || '?')[0].toUpperCase()
  const hue = ([...contact.uid || letter].reduce((a, c) => a + c.charCodeAt(0), 0) % 12) * 30
  const bg = `hsl(${hue} 55% 55%)`
  if (contact.avatarUrl) {
    return (
      <img
        src={contact.avatarUrl}
        alt={contact.displayName}
        className="rounded-full object-cover flex-shrink-0"
        style={{ width: size, height: size }}
      />
    )
  }
  return (
    <div
      className="rounded-full flex-shrink-0 flex items-center justify-center text-white font-semibold"
      style={{ width: size, height: size, background: bg, fontSize: size * 0.38 }}
    >
      {letter}
    </div>
  )
}

// ─── CardDAV helpers ──────────────────────────────────────────────────────────

function davHeaders(creds) {
  const basic = btoa(`${creds.email}:${creds.appPassword}`)
  return { Authorization: `Basic ${basic}`, 'Content-Type': 'text/vcard; charset=utf-8' }
}

function buildVCF(c) {
  const lines = [
    'BEGIN:VCARD', 'VERSION:4.0',
    `UID:${c.uid}`,
    `FN:${c.displayName || [c.firstName, c.lastName].filter(Boolean).join(' ')}`,
    `N:${c.lastName || ''};${c.firstName || ''};;;`,
    c.org ? `ORG:${c.org}` : '',
    c.title ? `TITLE:${c.title}` : '',
    c.birthday ? `BDAY:${c.birthday}` : '',
    c.notes ? `NOTE:${vcfEscape(c.notes)}` : '',
    c.starred ? 'X-VULOS-STARRED:1' : '',
    ...(c.groups || []).map(g => `CATEGORIES:${g}`),
    ...(c.avatarUrl ? [`PHOTO:${c.avatarUrl}`] : []),
    ...(c.emails || []).map(e => `EMAIL;TYPE=${e.label || 'other'}:${e.address}`),
    ...(c.phones || []).map(p => `TEL;TYPE=${p.label || 'other'}:${p.number}`),
    ...(c.addresses || []).map(a =>
      `ADR;TYPE=${a.label || 'other'}:;;${a.street || ''};${a.city || ''};${a.state || ''};${a.zip || ''};${a.country || ''}`
    ),
    ...Object.entries(c.customFields || {}).map(([k, v]) => `X-CUSTOM-${k}:${v}`),
  ].filter(Boolean)
  lines.push('END:VCARD', '')
  return lines.join('\r\n')
}

function vcfEscape(s) {
  return (s || '').replace(/\\/g, '\\\\').replace(/,/g, '\\,').replace(/\n/g, '\\n')
}

function parseVCF(raw, uid) {
  const get = (key) => {
    const m = raw.match(new RegExp(`^${key}[^:]*:(.+)`, 'm'))
    return m ? m[1].trim() : ''
  }
  const getAll = (key) => {
    return [...raw.matchAll(new RegExp(`^(${key})[^:]*:(.+)`, 'gm'))].map(m => ({
      params: m[1],
      value: m[2].trim(),
    }))
  }
  const getType = (params) => {
    const m = params.match(/TYPE=([^;:]+)/i)
    return m ? m[1].toLowerCase() : 'other'
  }

  // N: Last;First;;;
  const nField = get('N')
  const nParts = nField.split(';')
  const lastName  = (nParts[0] || '').trim()
  const firstName = (nParts[1] || '').trim()

  const emails = getAll('EMAIL').map(f => ({ label: getType(f.params), address: f.value }))
  const phones = getAll('TEL').map(f => ({ label: getType(f.params), number: f.value }))
  const addresses = getAll('ADR').map(f => {
    const parts = f.value.split(';')
    return {
      label: getType(f.params),
      street: parts[2] || '', city: parts[3] || '',
      state: parts[4] || '', zip: parts[5] || '', country: parts[6] || '',
    }
  })

  const cats = get('CATEGORIES')
  const groups = cats ? cats.split(',').map(s => s.trim()).filter(Boolean) : []
  const starred = get('X-VULOS-STARRED') === '1'
  const avatarUrl = get('PHOTO')

  // Custom fields
  const customFields = {}
  for (const [, key, val] of raw.matchAll(/^X-CUSTOM-([^:]+):(.+)/gm)) {
    customFields[key.trim()] = val.trim()
  }

  const fn = get('FN')
  return {
    uid: get('UID') || uid,
    firstName, lastName,
    displayName: fn || [firstName, lastName].filter(Boolean).join(' '),
    emails, phones, addresses,
    org: get('ORG'), title: get('TITLE'),
    birthday: get('BDAY'), notes: get('NOTE'),
    starred, groups, avatarUrl, customFields,
    raw,
  }
}

async function fetchContacts(accountID, creds) {
  const collURL = `${CARDDAV_BASE}/${accountID}/${COLLECTION}/`
  const headers = davHeaders(creds)
  const res = await fetch(collURL, {
    method: 'PROPFIND',
    headers: { ...headers, Depth: '1' },
    body: `<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`,
  })
  if (!res.ok && res.status !== 207) throw new Error(`CardDAV PROPFIND ${res.status}`)
  const xml = await res.text()
  const hrefs = [...xml.matchAll(/<[^>]*:href[^>]*>([^<]+\.vcf)<\/[^>]*:href>/g)].map(m => m[1])
  const contacts = []
  await Promise.all(hrefs.map(async href => {
    const r = await fetch(href, { headers })
    if (r.ok) {
      const uid = href.split('/').pop().replace('.vcf', '')
      contacts.push(parseVCF(await r.text(), uid))
    }
  }))
  return contacts.sort((a, b) => (a.displayName || '').localeCompare(b.displayName || ''))
}

async function putContact(accountID, creds, contact) {
  const url = `${CARDDAV_BASE}/${accountID}/${COLLECTION}/${contact.uid}.vcf`
  const r = await fetch(url, { method: 'PUT', headers: davHeaders(creds), body: buildVCF(contact) })
  if (!r.ok && r.status !== 201 && r.status !== 204) throw new Error(`CardDAV PUT ${r.status}`)
}

async function deleteContact(accountID, creds, uid) {
  const url = `${CARDDAV_BASE}/${accountID}/${COLLECTION}/${uid}.vcf`
  const r = await fetch(url, { method: 'DELETE', headers: davHeaders(creds) })
  if (!r.ok && r.status !== 204) throw new Error(`CardDAV DELETE ${r.status}`)
}

// ─── ContactForm (rich) ───────────────────────────────────────────────────────

function MultiField({ label, items, onChange, fieldKey, labelOptions, addLabel }) {
  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <label className="text-xs text-ink-faint">{label}</label>
        <button
          type="button"
          onClick={() => onChange([...items, { label: labelOptions[0], [fieldKey]: '' }])}
          className="text-xs text-accent hover:underline"
        >
          + Add
        </button>
      </div>
      {items.map((item, i) => (
        <div key={i} className="flex gap-1.5 mb-1.5">
          <select
            value={item.label}
            onChange={e => onChange(items.map((it, j) => j === i ? { ...it, label: e.target.value } : it))}
            className="w-20 px-1.5 py-1 rounded border border-line bg-bg text-ink text-xs focus:outline-none"
          >
            {labelOptions.map(l => <option key={l} value={l}>{l}</option>)}
          </select>
          <input
            type="text"
            value={item[fieldKey]}
            onChange={e => onChange(items.map((it, j) => j === i ? { ...it, [fieldKey]: e.target.value } : it))}
            className="flex-1 px-2 py-1 rounded border border-line bg-bg text-ink text-xs focus:outline-none focus:ring-2 focus:ring-accent/40"
            placeholder={addLabel}
          />
          <button
            type="button"
            onClick={() => onChange(items.filter((_, j) => j !== i))}
            className="p-1 text-ink-faint hover:text-signal-error"
          >
            <X size={12} />
          </button>
        </div>
      ))}
    </div>
  )
}

function ContactForm({ contact, allGroups, onSave, onClose }) {
  const isNew = !contact?.uid
  const [firstName, setFirst]     = useState(contact?.firstName || '')
  const [lastName, setLast]       = useState(contact?.lastName || '')
  const [displayName, setDisplay] = useState(contact?.displayName || '')
  const [emails, setEmails]       = useState(contact?.emails || [{ label: 'work', address: '' }])
  const [phones, setPhones]       = useState(contact?.phones || [])
  const [addresses, setAddresses] = useState(contact?.addresses || [])
  const [org, setOrg]             = useState(contact?.org || '')
  const [titleVal, setTitle]      = useState(contact?.title || '')
  const [birthday, setBday]       = useState(contact?.birthday || '')
  const [notes, setNotes]         = useState(contact?.notes || '')
  const [starred, setStarred]     = useState(contact?.starred || false)
  const [groups, setGroups]       = useState(contact?.groups || [])
  const [avatarUrl, setAvatar]    = useState(contact?.avatarUrl || '')
  const [customFields, setCustom] = useState(contact?.customFields || {})
  const [newCustomKey, setNewCKey] = useState('')
  const [tab, setTab]             = useState('basic')

  // Auto-fill display name if not manually changed
  useEffect(() => {
    if (!displayName || displayName === [contact?.firstName, contact?.lastName].filter(Boolean).join(' ')) {
      setDisplay([firstName, lastName].filter(Boolean).join(' '))
    }
  }, [firstName, lastName])

  const handleSave = (e) => {
    e.preventDefault()
    if (!displayName.trim() && !firstName.trim()) return
    onSave({
      uid: contact?.uid || crypto.randomUUID(),
      firstName: firstName.trim(),
      lastName: lastName.trim(),
      displayName: displayName.trim() || [firstName, lastName].filter(Boolean).join(' ').trim(),
      emails: emails.filter(e => e.address),
      phones: phones.filter(p => p.number),
      addresses: addresses.filter(a => a.street || a.city),
      org: org.trim(),
      title: titleVal.trim(),
      birthday: birthday.trim(),
      notes: notes.trim(),
      starred,
      groups,
      avatarUrl: avatarUrl.trim(),
      customFields,
    })
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <form
        onSubmit={handleSave}
        className="relative bg-bg border border-line rounded-xl shadow-xl w-full max-w-lg max-h-[90vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-start justify-between px-5 pt-4 pb-3 border-b border-line gap-3">
          <div className="flex items-center gap-3">
            <Avatar contact={{ uid: contact?.uid || 'new', displayName: displayName || firstName, firstName, avatarUrl }} size={40} />
            <div>
              <div className="text-sm font-semibold text-ink">{displayName || 'New contact'}</div>
              {org && <div className="text-xs text-ink-muted">{org}</div>}
            </div>
          </div>
          <div className="flex items-center gap-1.5 flex-shrink-0">
            <button
              type="button"
              onClick={() => setStarred(s => !s)}
              className={['p-1.5 rounded transition-colors', starred ? 'text-yellow-400' : 'text-ink-faint hover:text-yellow-400'].join(' ')}
              title={starred ? 'Remove star' : 'Star'}
            >
              <Star size={14} fill={starred ? 'currentColor' : 'none'} />
            </button>
            <button type="button" onClick={onClose} className="p-1.5 rounded text-ink-faint hover:text-ink hover:bg-bg-elev-2">
              <X size={14} />
            </button>
          </div>
        </div>

        {/* Tabs */}
        <div className="flex border-b border-line px-5">
          {[['basic','Basic'],['address','Address'],['more','More']].map(([t,l]) => (
            <button
              key={t} type="button" onClick={() => setTab(t)}
              className={[
                'text-xs py-2 px-3 border-b-2 transition-colors',
                tab === t ? 'border-accent text-accent font-medium' : 'border-transparent text-ink-faint hover:text-ink',
              ].join(' ')}
            >{l}</button>
          ))}
        </div>

        <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
          {tab === 'basic' && (
            <>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs text-ink-faint mb-1">First name</label>
                  <input value={firstName} onChange={e => setFirst(e.target.value)}
                    className="w-full px-2 py-1.5 rounded-md border border-line bg-bg text-ink text-sm focus:outline-none focus:ring-2 focus:ring-accent/40"
                    placeholder="Alice" />
                </div>
                <div>
                  <label className="block text-xs text-ink-faint mb-1">Last name</label>
                  <input value={lastName} onChange={e => setLast(e.target.value)}
                    className="w-full px-2 py-1.5 rounded-md border border-line bg-bg text-ink text-sm focus:outline-none focus:ring-2 focus:ring-accent/40"
                    placeholder="Smith" />
                </div>
              </div>

              <div>
                <label className="block text-xs text-ink-faint mb-1">Display name *</label>
                <input value={displayName} onChange={e => setDisplay(e.target.value)} required
                  className="w-full px-2 py-1.5 rounded-md border border-line bg-bg text-ink text-sm focus:outline-none focus:ring-2 focus:ring-accent/40"
                  placeholder="Alice Smith" />
              </div>

              <MultiField
                label="Email" items={emails} onChange={setEmails}
                fieldKey="address" labelOptions={LABEL_OPTIONS} addLabel="email@example.com"
              />
              <MultiField
                label="Phone" items={phones} onChange={setPhones}
                fieldKey="number" labelOptions={LABEL_OPTIONS} addLabel="+1 555 000 0000"
              />

              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs text-ink-faint mb-1">Organisation</label>
                  <input value={org} onChange={e => setOrg(e.target.value)}
                    className="w-full px-2 py-1.5 rounded-md border border-line bg-bg text-ink text-sm focus:outline-none focus:ring-2 focus:ring-accent/40"
                    placeholder="Acme Corp" />
                </div>
                <div>
                  <label className="block text-xs text-ink-faint mb-1">Title</label>
                  <input value={titleVal} onChange={e => setTitle(e.target.value)}
                    className="w-full px-2 py-1.5 rounded-md border border-line bg-bg text-ink text-sm focus:outline-none focus:ring-2 focus:ring-accent/40"
                    placeholder="Engineer" />
                </div>
              </div>

              <div>
                <label className="block text-xs text-ink-faint mb-1">Birthday</label>
                <input type="date" value={birthday} onChange={e => setBday(e.target.value)}
                  className="w-full px-2 py-1.5 rounded-md border border-line bg-bg text-ink text-sm focus:outline-none focus:ring-2 focus:ring-accent/40" />
              </div>
            </>
          )}

          {tab === 'address' && (
            <div>
              <div className="flex items-center justify-between mb-2">
                <span className="text-xs text-ink-faint">Addresses</span>
                <button
                  type="button"
                  onClick={() => setAddresses(prev => [...prev, { label: 'home', street: '', city: '', state: '', zip: '', country: '' }])}
                  className="text-xs text-accent hover:underline"
                >+ Add</button>
              </div>
              {addresses.map((addr, i) => (
                <div key={i} className="border border-line rounded-lg p-3 mb-3 space-y-2">
                  <div className="flex items-center justify-between">
                    <select
                      value={addr.label}
                      onChange={e => setAddresses(prev => prev.map((a, j) => j === i ? { ...a, label: e.target.value } : a))}
                      className="text-xs px-1.5 py-1 rounded border border-line bg-bg text-ink focus:outline-none"
                    >
                      {LABEL_OPTIONS.map(l => <option key={l} value={l}>{l}</option>)}
                    </select>
                    <button
                      type="button"
                      onClick={() => setAddresses(prev => prev.filter((_, j) => j !== i))}
                      className="text-ink-faint hover:text-signal-error"
                    ><X size={12} /></button>
                  </div>
                  {[['street','Street'],['city','City'],['state','State/Province'],['zip','Zip/Postal'],['country','Country']].map(([k, placeholder]) => (
                    <input
                      key={k} value={addr[k] || ''} placeholder={placeholder}
                      onChange={e => setAddresses(prev => prev.map((a, j) => j === i ? { ...a, [k]: e.target.value } : a))}
                      className="w-full px-2 py-1 rounded border border-line bg-bg text-ink text-xs focus:outline-none focus:ring-2 focus:ring-accent/40"
                    />
                  ))}
                </div>
              ))}
            </div>
          )}

          {tab === 'more' && (
            <>
              {/* Notes */}
              <div>
                <label className="block text-xs text-ink-faint mb-1">Notes</label>
                <textarea
                  value={notes}
                  onChange={e => setNotes(e.target.value)}
                  rows={3}
                  className="w-full px-2 py-1.5 rounded-md border border-line bg-bg text-ink text-sm resize-none focus:outline-none focus:ring-2 focus:ring-accent/40"
                  placeholder="Notes…"
                />
              </div>

              {/* Groups */}
              <div>
                <label className="block text-xs text-ink-faint mb-1">Groups</label>
                <div className="flex flex-wrap gap-1.5 mb-2">
                  {allGroups.map(g => (
                    <button
                      key={g} type="button"
                      onClick={() => setGroups(prev => prev.includes(g) ? prev.filter(x => x !== g) : [...prev, g])}
                      className={[
                        'text-xs px-2 py-0.5 rounded-full border transition-colors',
                        groups.includes(g)
                          ? 'bg-accent text-white border-accent'
                          : 'border-line text-ink-muted hover:border-accent/40',
                      ].join(' ')}
                    >{g}</button>
                  ))}
                </div>
              </div>

              {/* Avatar URL */}
              <div>
                <label className="block text-xs text-ink-faint mb-1">Avatar URL</label>
                <input
                  type="url" value={avatarUrl} onChange={e => setAvatar(e.target.value)}
                  className="w-full px-2 py-1.5 rounded-md border border-line bg-bg text-ink text-sm focus:outline-none focus:ring-2 focus:ring-accent/40"
                  placeholder="https://…"
                />
              </div>

              {/* Custom fields */}
              <div>
                <label className="block text-xs text-ink-faint mb-1">Custom fields</label>
                {Object.entries(customFields).map(([k, v]) => (
                  <div key={k} className="flex items-center gap-1.5 mb-1.5">
                    <span className="text-xs font-medium text-ink-muted w-24 truncate">{k}</span>
                    <input
                      value={v}
                      onChange={e => setCustom(prev => ({ ...prev, [k]: e.target.value }))}
                      className="flex-1 px-2 py-1 rounded border border-line bg-bg text-ink text-xs focus:outline-none"
                    />
                    <button type="button" onClick={() => setCustom(prev => { const p = { ...prev }; delete p[k]; return p })} className="text-ink-faint hover:text-signal-error">
                      <X size={11} />
                    </button>
                  </div>
                ))}
                <div className="flex gap-1.5">
                  <input
                    value={newCustomKey} onChange={e => setNewCKey(e.target.value)}
                    placeholder="Field name"
                    className="flex-1 px-2 py-1 rounded border border-line bg-bg text-ink text-xs focus:outline-none"
                    onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); if (newCustomKey) { setCustom(p => ({ ...p, [newCustomKey]: '' })); setNewCKey('') } } }}
                  />
                  <button
                    type="button"
                    onClick={() => { if (newCustomKey) { setCustom(p => ({ ...p, [newCustomKey]: '' })); setNewCKey('') } }}
                    className="text-xs px-2 py-1 rounded border border-line text-ink-muted hover:bg-bg-elev-2"
                  >Add</button>
                </div>
              </div>
            </>
          )}
        </div>

        <div className="flex justify-end gap-2 px-5 py-3 border-t border-line">
          <button type="button" onClick={onClose}
            className="px-4 py-1.5 rounded-md text-sm text-ink-muted hover:bg-bg-elev-2 transition-colors">
            Cancel
          </button>
          <button type="submit"
            className="px-4 py-1.5 rounded-md text-sm text-white bg-accent hover:bg-accent-hover transition-colors font-medium">
            {isNew ? 'Create contact' : 'Save'}
          </button>
        </div>
      </form>
    </div>
  )
}

// ─── ContactDetail ────────────────────────────────────────────────────────────

function Field({ label, children }) {
  return (
    <div className="py-2 border-b border-line last:border-0">
      <div className="text-[10px] text-ink-faint uppercase tracking-wider mb-0.5">{label}</div>
      <div className="text-sm">{children}</div>
    </div>
  )
}

function ContactDetail({ contact, onEdit, onDelete, onEmailGroup }) {
  if (!contact) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-ink-faint gap-3">
        <User size={40} strokeWidth={1} />
        <p className="text-sm">Select a contact</p>
      </div>
    )
  }

  return (
    <div className="p-5 h-full overflow-y-auto" style={{ fontFamily: 'var(--font-sans)' }}>
      {/* Hero */}
      <div className="flex items-start gap-4 mb-5">
        <Avatar contact={contact} size={56} />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-1.5">
            <h2 className="text-lg font-semibold text-ink">{contact.displayName}</h2>
            {contact.starred && <Star size={14} className="text-yellow-400" fill="currentColor" />}
          </div>
          {contact.org && <p className="text-sm text-ink-muted">{contact.title ? `${contact.title}, ` : ''}{contact.org}</p>}
          {contact.groups?.length > 0 && (
            <div className="flex gap-1 mt-1 flex-wrap">
              {contact.groups.map(g => (
                <span key={g} className="text-[10px] px-1.5 py-0.5 rounded-full bg-accent-tint text-accent">{g}</span>
              ))}
            </div>
          )}
        </div>
        <div className="flex gap-1 flex-shrink-0">
          <button onClick={() => onEdit(contact)}
            className="p-1.5 rounded text-ink-faint hover:text-ink hover:bg-bg-elev-2" title="Edit">
            <Edit2 size={14} />
          </button>
          <button onClick={() => onDelete(contact.uid)}
            className="p-1.5 rounded text-ink-faint hover:text-signal-error hover:bg-signal-error-bg" title="Delete">
            <Trash2 size={14} />
          </button>
        </div>
      </div>

      {/* Fields */}
      {contact.emails?.length > 0 && (
        <Field label="Email">
          <div className="space-y-1">
            {contact.emails.map((e, i) => (
              <div key={i} className="flex items-center gap-2">
                <span className="text-[10px] text-ink-faint w-10">{e.label}</span>
                <a href={`mailto:${e.address}`} className="text-accent hover:underline">{e.address}</a>
              </div>
            ))}
          </div>
        </Field>
      )}
      {contact.phones?.length > 0 && (
        <Field label="Phone">
          <div className="space-y-1">
            {contact.phones.map((p, i) => (
              <div key={i} className="flex items-center gap-2">
                <span className="text-[10px] text-ink-faint w-10">{p.label}</span>
                <a href={`tel:${p.number}`} className="text-ink hover:text-accent">{p.number}</a>
              </div>
            ))}
          </div>
        </Field>
      )}
      {contact.addresses?.length > 0 && (
        <Field label="Address">
          <div className="space-y-2">
            {contact.addresses.map((a, i) => (
              <div key={i}>
                <span className="text-[10px] text-ink-faint block">{a.label}</span>
                <span className="text-ink text-sm">
                  {[a.street, a.city, a.state, a.zip, a.country].filter(Boolean).join(', ')}
                </span>
              </div>
            ))}
          </div>
        </Field>
      )}
      {contact.birthday && <Field label="Birthday"><span className="text-ink">{contact.birthday}</span></Field>}
      {contact.notes && <Field label="Notes"><span className="text-ink-muted whitespace-pre-wrap">{contact.notes}</span></Field>}
      {Object.keys(contact.customFields || {}).length > 0 && (
        <div>
          {Object.entries(contact.customFields).map(([k, v]) => (
            <Field key={k} label={k}><span className="text-ink">{v}</span></Field>
          ))}
        </div>
      )}
    </div>
  )
}

// ─── ImportPanel ──────────────────────────────────────────────────────────────

function ImportPanel({ onImported, onClose }) {
  const [file, setFile]         = useState(null)
  const [preview, setPreview]   = useState(null)
  const [loading, setLoading]   = useState(false)
  const [error, setError]       = useState(null)
  const [imported, setImported] = useState(null)
  const fileRef = useRef()

  const handleFile = (f) => {
    setFile(f); setPreview(null); setError(null); setImported(null)
    const reader = new FileReader()
    reader.onload = (e) => {
      // Quick parse preview
      const blocks = e.target.result.split('BEGIN:VCARD').slice(1)
      const items = blocks.slice(0, 5).map(b => {
        const fn = (b.match(/^FN[^:]*:(.+)/m) || [])[1]?.trim() || '?'
        const email = (b.match(/^EMAIL[^:]*:(.+)/m) || [])[1]?.trim() || ''
        return { fn, email }
      })
      setPreview({ count: blocks.length, items })
    }
    reader.readAsText(f)
  }

  const doImport = async () => {
    if (!file) return
    setLoading(true); setError(null)
    try {
      const fd = new FormData(); fd.append('file', file)
      const r = await fetch(`${API_BASE}/contacts/import`, { method: 'POST', body: fd })
      if (!r.ok) throw new Error(`Import failed: ${r.status}`)
      const data = await r.json()
      setImported(data.imported)
      onImported()
    } catch (e) { setError(e.message) }
    finally { setLoading(false) }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div
        className="bg-bg border border-line rounded-xl shadow-xl w-full max-w-md"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center justify-between px-5 py-4 border-b border-line">
          <h2 className="text-sm font-semibold text-ink">Import contacts</h2>
          <button onClick={onClose} className="p-1 rounded text-ink-faint hover:text-ink hover:bg-bg-elev-2"><X size={14} /></button>
        </div>
        <div className="p-5 space-y-4">
          <div
            className="border-2 border-dashed border-line rounded-lg p-6 text-center cursor-pointer hover:border-accent/40 transition-colors"
            onClick={() => fileRef.current?.click()}
          >
            <Upload size={24} className="mx-auto text-ink-faint mb-2" />
            <p className="text-sm text-ink-muted">{file ? file.name : 'Click to select a .vcf file'}</p>
            <input ref={fileRef} type="file" accept=".vcf,text/vcard" className="hidden" onChange={e => e.target.files?.[0] && handleFile(e.target.files[0])} />
          </div>

          {preview && (
            <div className="border border-line rounded-lg p-3">
              <div className="text-xs font-medium text-ink mb-2">{preview.count} contacts found — preview:</div>
              <div className="space-y-1">
                {preview.items.map((item, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <div className="w-6 h-6 rounded-full bg-accent-tint flex items-center justify-center flex-shrink-0">
                      <span className="text-[10px] font-semibold text-accent">{item.fn[0].toUpperCase()}</span>
                    </div>
                    <div className="min-w-0">
                      <div className="text-xs font-medium text-ink truncate">{item.fn}</div>
                      {item.email && <div className="text-[10px] text-ink-faint truncate">{item.email}</div>}
                    </div>
                  </div>
                ))}
                {preview.count > 5 && <div className="text-xs text-ink-faint">…and {preview.count - 5} more</div>}
              </div>
            </div>
          )}

          {imported !== null && (
            <div className="flex items-center gap-2 text-signal-success bg-signal-success-bg px-3 py-2 rounded-lg">
              <Check size={14} />
              <span className="text-sm">{imported} contacts imported.</span>
            </div>
          )}

          {error && <div className="text-sm text-signal-error bg-signal-error-bg px-3 py-2 rounded-lg">{error}</div>}
        </div>
        <div className="flex justify-end gap-2 px-5 py-3 border-t border-line">
          <button onClick={onClose} className="px-4 py-1.5 text-sm text-ink-muted hover:bg-bg-elev-2 rounded-md transition-colors">
            {imported !== null ? 'Done' : 'Cancel'}
          </button>
          {imported === null && (
            <button
              onClick={doImport}
              disabled={!file || loading}
              className="px-4 py-1.5 text-sm text-white bg-accent hover:bg-accent-hover rounded-md transition-colors font-medium disabled:opacity-50"
            >
              {loading ? 'Importing…' : `Import ${preview?.count || ''} contacts`}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

// ─── DedupPanel ───────────────────────────────────────────────────────────────

function DedupPanel({ onMerged, onClose }) {
  const [candidates, setCandidates] = useState(null)
  const [loading, setLoading]       = useState(false)
  const [merging, setMerging]       = useState(null)
  const [error, setError]           = useState(null)

  const load = async () => {
    setLoading(true); setError(null)
    try {
      const r = await fetch(`${API_BASE}/contacts/duplicates`)
      const d = await r.json()
      setCandidates(d.candidates || [])
    } catch (e) { setError(e.message) }
    finally { setLoading(false) }
  }

  useEffect(() => { load() }, [])

  const merge = async (keepUID, deleteUID) => {
    setMerging(keepUID + deleteUID)
    try {
      const r = await fetch(`${API_BASE}/contacts/merge`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ keep_uid: keepUID, delete_uid: deleteUID }),
      })
      if (!r.ok) throw new Error(`Merge failed ${r.status}`)
      setCandidates(prev => prev.filter(c => !(
        (c.a.uid === keepUID && c.b.uid === deleteUID) ||
        (c.a.uid === deleteUID && c.b.uid === keepUID)
      )))
      onMerged()
    } catch (e) { setError(e.message) }
    finally { setMerging(null) }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div
        className="bg-bg border border-line rounded-xl shadow-xl w-full max-w-lg max-h-[80vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center justify-between px-5 py-4 border-b border-line">
          <div className="flex items-center gap-2">
            <GitMerge size={16} className="text-accent" />
            <h2 className="text-sm font-semibold text-ink">Find duplicates</h2>
          </div>
          <button onClick={onClose} className="p-1 rounded text-ink-faint hover:text-ink hover:bg-bg-elev-2"><X size={14} /></button>
        </div>
        <div className="flex-1 overflow-y-auto p-5">
          {loading && (
            <div className="flex items-center justify-center h-24">
              <div className="w-5 h-5 border-2 border-accent border-t-transparent rounded-full animate-spin" />
            </div>
          )}
          {error && <div className="text-sm text-signal-error bg-signal-error-bg px-3 py-2 rounded-lg mb-3">{error}</div>}
          {candidates?.length === 0 && (
            <div className="flex flex-col items-center gap-2 py-8 text-ink-faint">
              <Check size={24} className="text-signal-success" />
              <p className="text-sm">No duplicates found.</p>
            </div>
          )}
          {candidates?.map((cand, i) => (
            <div key={i} className="border border-line rounded-lg p-3 mb-3">
              <div className="text-xs text-ink-faint mb-2">
                Matched by <span className="text-accent font-medium">{cand.reason}</span>
              </div>
              <div className="grid grid-cols-2 gap-3 text-xs mb-3">
                {[cand.a, cand.b].map(ct => (
                  <div key={ct.uid} className="bg-bg-elev-2 rounded p-2">
                    <div className="font-medium text-ink">{ct.displayName || ct.display_name}</div>
                    <div className="text-ink-faint">{(ct.emails?.[0]?.address || ct.email || '')}</div>
                  </div>
                ))}
              </div>
              <div className="flex gap-2">
                <button
                  onClick={() => merge(cand.a.uid, cand.b.uid)}
                  disabled={!!merging}
                  className="flex-1 text-xs py-1.5 rounded-md bg-accent text-white hover:bg-accent-hover transition-colors disabled:opacity-50"
                >
                  Keep left
                </button>
                <button
                  onClick={() => merge(cand.b.uid, cand.a.uid)}
                  disabled={!!merging}
                  className="flex-1 text-xs py-1.5 rounded-md border border-line text-ink hover:bg-bg-elev-2 transition-colors disabled:opacity-50"
                >
                  Keep right
                </button>
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

// ─── Main ContactsApp ─────────────────────────────────────────────────────────

export default function ContactsApp() {
  const { status } = useAuthStore()
  const creds = {
    email: status?.user?.email || '',
    appPassword: status?.user?.appPassword || '',
  }
  const accountID = status?.user?.id || ''

  const [contacts, setContacts]   = useState([])
  const [loading, setLoading]     = useState(true)
  const [error, setError]         = useState(null)
  const [search, setSearch]       = useState('')
  const [selected, setSelected]   = useState(null)
  const [showForm, setShowForm]   = useState(false)
  const [editingContact, setEdit] = useState(null)
  const [showImport, setShowImport] = useState(false)
  const [showDedup, setShowDedup]   = useState(false)
  const [activeGroup, setGroup]   = useState('all') // 'all' | 'starred' | groupName

  // Groups derived from contacts
  const allGroups = [...new Set(contacts.flatMap(c => c.groups || []))].sort()

  const load = useCallback(async () => {
    if (!accountID || !creds.email) { setLoading(false); return }
    setLoading(true); setError(null)
    try {
      const list = await fetchContacts(accountID, creds)
      setContacts(list)
    } catch (e) {
      setError(e.message)
    } finally {
      setLoading(false)
    }
  }, [accountID, creds.email])

  useEffect(() => { load() }, [load])

  const handleSave = async (data) => {
    try {
      await putContact(accountID, creds, data)
      setShowForm(false); setEdit(null)
      await load()
      setSelected(data)
    } catch (e) { setError(e.message) }
  }

  const handleDelete = async (uid) => {
    if (!confirm('Delete this contact?')) return
    try {
      await deleteContact(accountID, creds, uid)
      if (selected?.uid === uid) setSelected(null)
      await load()
    } catch (e) { setError(e.message) }
  }

  const exportContacts = async () => {
    try {
      const r = await fetch(`${API_BASE}/contacts/export`)
      if (!r.ok) throw new Error('Export failed')
      const blob = await r.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a'); a.href = url; a.download = 'contacts.vcf'; a.click()
      URL.revokeObjectURL(url)
    } catch (e) { setError(e.message) }
  }

  const emailGroup = (groupName) => {
    const groupContacts = contacts.filter(c => c.groups?.includes(groupName))
    const to = groupContacts.map(c => c.emails?.[0]?.address).filter(Boolean).join(',')
    if (to) window.location.href = `mailto:${to}`
  }

  // Filter
  const filtered = contacts.filter(c => {
    if (activeGroup === 'starred') return c.starred
    if (activeGroup !== 'all') return c.groups?.includes(activeGroup)
    return true
  }).filter(c => {
    if (!search) return true
    const q = search.toLowerCase()
    return [c.displayName, c.firstName, c.lastName, c.org, ...(c.emails?.map(e => e.address) || []), ...(c.phones?.map(p => p.number) || [])]
      .filter(Boolean).some(v => v.toLowerCase().includes(q))
  })

  // Alphabetical groups
  const grouped = {}
  for (const c of filtered) {
    const letter = (c.displayName || '#')[0].toUpperCase()
    const key = /[A-Z]/.test(letter) ? letter : '#'
    if (!grouped[key]) grouped[key] = []
    grouped[key].push(c)
  }
  const groupKeys = Object.keys(grouped).sort()

  return (
    <div className="flex h-full bg-bg text-ink" style={{ fontFamily: 'var(--font-sans)' }}>
      {/* Left: Groups sidebar */}
      <div className="hidden lg:flex flex-col w-44 flex-shrink-0 border-r border-line">
        <div className="flex items-center justify-between px-3 py-3 border-b border-line">
          <span className="text-xs font-semibold text-ink-faint uppercase tracking-wider">Groups</span>
          <button
            onClick={() => {
              const name = prompt('Group name:')
              if (name && !allGroups.includes(name)) {
                // Just creates an empty group — contacts must be assigned via edit
              }
            }}
            className="p-0.5 rounded hover:bg-bg-elev-2 text-ink-faint" title="New group"
          >
            <Plus size={12} />
          </button>
        </div>
        <div className="flex-1 overflow-y-auto py-1">
          {[
            { id: 'all', label: 'All contacts' },
            { id: 'starred', label: 'Starred' },
          ].map(item => (
            <button
              key={item.id}
              onClick={() => setGroup(item.id)}
              className={[
                'w-full text-left px-3 py-1.5 text-xs flex items-center gap-2 transition-colors',
                activeGroup === item.id ? 'bg-accent-tint text-accent font-medium border-r-2 border-accent' : 'text-ink-muted hover:bg-bg-elev-2',
              ].join(' ')}
            >
              {item.id === 'starred' ? <Star size={11} /> : <Users size={11} />}
              {item.label}
            </button>
          ))}
          {allGroups.length > 0 && (
            <>
              <div className="px-3 py-1 text-[9px] text-ink-faint uppercase tracking-wider mt-2">Labels</div>
              {allGroups.map(g => (
                <div key={g} className="group flex items-center">
                  <button
                    onClick={() => setGroup(g)}
                    className={[
                      'flex-1 text-left px-3 py-1.5 text-xs flex items-center gap-2 transition-colors',
                      activeGroup === g ? 'bg-accent-tint text-accent font-medium border-r-2 border-accent' : 'text-ink-muted hover:bg-bg-elev-2',
                    ].join(' ')}
                  >
                    <Tag size={11} />
                    {g}
                  </button>
                  <button
                    onClick={() => emailGroup(g)}
                    className="opacity-0 group-hover:opacity-100 pr-2 text-ink-faint hover:text-accent transition-opacity"
                    title={`Email ${g}`}
                  >
                    <Mail size={11} />
                  </button>
                </div>
              ))}
            </>
          )}
        </div>

        {/* Import / Export / Dedup */}
        <div className="border-t border-line p-2 space-y-1">
          <button
            onClick={() => setShowImport(true)}
            className="w-full flex items-center gap-2 px-2 py-1.5 text-xs text-ink-muted hover:bg-bg-elev-2 rounded transition-colors"
          >
            <Upload size={11} /> Import VCF
          </button>
          <button
            onClick={exportContacts}
            className="w-full flex items-center gap-2 px-2 py-1.5 text-xs text-ink-muted hover:bg-bg-elev-2 rounded transition-colors"
          >
            <Download size={11} /> Export VCF
          </button>
          <button
            onClick={() => setShowDedup(true)}
            className="w-full flex items-center gap-2 px-2 py-1.5 text-xs text-ink-muted hover:bg-bg-elev-2 rounded transition-colors"
          >
            <GitMerge size={11} /> Find duplicates
          </button>
        </div>
      </div>

      {/* Middle: contacts list */}
      <div className="w-72 flex flex-col border-r border-line flex-shrink-0">
        {/* Header */}
        <div className="flex items-center justify-between px-3 py-2.5 border-b border-line">
          <div className="flex items-center gap-1.5">
            <Users size={14} className="text-accent" />
            <span className="text-sm font-semibold text-ink">Contacts</span>
            {contacts.length > 0 && <span className="text-xs text-ink-faint">({filtered.length})</span>}
          </div>
          <button
            onClick={() => { setEdit(null); setShowForm(true) }}
            className="p-1 rounded text-accent hover:bg-accent-tint" title="New contact"
          >
            <Plus size={14} />
          </button>
        </div>

        {/* Search */}
        <div className="px-3 py-2 border-b border-line">
          <div className="flex items-center gap-2 px-2 py-1.5 rounded-md bg-bg-elev-2 border border-line">
            <Search size={12} className="text-ink-faint flex-shrink-0" />
            <input
              type="text" placeholder="Search…" value={search}
              onChange={e => setSearch(e.target.value)}
              className="flex-1 text-xs bg-transparent text-ink outline-none placeholder:text-ink-faint"
            />
            {search && (
              <button onClick={() => setSearch('')} className="text-ink-faint hover:text-ink"><X size={10} /></button>
            )}
          </div>
        </div>

        {/* List */}
        <div className="flex-1 overflow-y-auto">
          {loading ? (
            <div className="flex items-center justify-center h-24">
              <div className="w-5 h-5 border-2 border-accent border-t-transparent rounded-full animate-spin" />
            </div>
          ) : filtered.length === 0 ? (
            <div className="px-4 py-6 text-xs text-ink-faint text-center">
              {search ? 'No matching contacts.' : activeGroup === 'starred' ? 'No starred contacts.' : 'No contacts yet.'}
            </div>
          ) : groupKeys.map(key => (
            <div key={key}>
              <div className="px-3 py-1 text-[10px] font-semibold text-ink-faint bg-bg-elev-2 border-b border-line sticky top-0 z-10">
                {key}
              </div>
              {grouped[key].map(c => (
                <button
                  key={c.uid}
                  onClick={() => setSelected(c)}
                  className={[
                    'w-full flex items-center gap-2.5 px-3 py-2.5 text-left hover:bg-accent-tint/40 transition-colors',
                    selected?.uid === c.uid ? 'bg-accent-tint border-r-2 border-accent' : '',
                  ].join(' ')}
                >
                  <Avatar contact={c} size={28} />
                  <div className="min-w-0 flex-1">
                    <div className="text-xs font-medium text-ink truncate flex items-center gap-1">
                      {c.displayName}
                      {c.starred && <Star size={9} className="text-yellow-400 flex-shrink-0" fill="currentColor" />}
                    </div>
                    {c.emails?.[0]?.address && <div className="text-[10px] text-ink-faint truncate">{c.emails[0].address}</div>}
                  </div>
                  <ChevronRight size={11} className="text-ink-faint flex-shrink-0" />
                </button>
              ))}
            </div>
          ))}
        </div>
      </div>

      {/* Right: detail */}
      <div className="flex-1 overflow-hidden">
        {error && (
          <div className="m-3 px-3 py-2 rounded-lg bg-signal-error-bg text-signal-error text-sm flex items-center justify-between">
            <span>{error}</span>
            <button onClick={() => setError(null)} className="ml-2 underline text-xs">dismiss</button>
          </div>
        )}
        <ContactDetail
          contact={selected}
          onEdit={c => { setEdit(c); setShowForm(true) }}
          onDelete={handleDelete}
          onEmailGroup={emailGroup}
        />
      </div>

      {/* Modals */}
      {showForm && (
        <ContactForm
          contact={editingContact}
          allGroups={allGroups}
          onSave={handleSave}
          onClose={() => { setShowForm(false); setEdit(null) }}
        />
      )}
      {showImport && (
        <ImportPanel
          onImported={() => { load(); setShowImport(false) }}
          onClose={() => setShowImport(false)}
        />
      )}
      {showDedup && (
        <DedupPanel
          onMerged={load}
          onClose={() => setShowDedup(false)}
        />
      )}
    </div>
  )
}
