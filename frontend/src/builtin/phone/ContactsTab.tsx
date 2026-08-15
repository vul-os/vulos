// ContactsTab.tsx — the address book, inside the Phone app.
//
// This is the "together, like on Android" half: a contact and the things you do
// with a contact (call, text) are the same surface, not two apps you alt-tab
// between. It reads the box's UNIFIED book (/api/contacts/unified), which is
// already the merge of the owner's CardDAV/Vulos cards, contacts pushed from an
// Android handset, and a SIM phonebook read off a modem in the box — so a number
// stored on the SIM in the USB stick is callable here with no extra step.
//
// Editing still belongs to the full Contacts app: that one owns the CardDAV
// write path (create/update/delete against /api/pim/contacts). Duplicating a
// write path is how two surfaces drift apart, so this one reads and dials.

import { useState, useMemo } from 'react'
import { Avatar, CallButton, ErrorNotice, EmptyNote } from './PhoneChrome'
import type { Size } from './phoneLayout'
import { displayNumber } from './phoneUtils'
import type { PhoneContact } from './telephonyApi'

const SOURCE_META: Record<string, { label: string; color: string }> = {
  vulos: { label: 'Vulos', color: 'var(--accent)' },
  phone: { label: 'Device', color: 'var(--status-success)' },
  'box-sim': { label: 'Box SIM', color: 'var(--status-warning)' },
}
const SOURCE_ORDER = ['vulos', 'phone', 'box-sim']

function SourceDots({ sources }: { sources: string[] }) {
  const list = SOURCE_ORDER.filter((s) => sources.includes(s) && SOURCE_META[s])
  if (list.length === 0) return null
  const labels = list.map((s) => SOURCE_META[s].label).join(', ')
  return (
    <span className="inline-flex items-center gap-[3px] shrink-0">
      {list.map((s) => (
        <span key={s} aria-hidden="true" className="w-1.5 h-1.5 rounded-full" style={{ background: SOURCE_META[s].color }} />
      ))}
      <span className="sr-only">{`On ${labels}`}</span>
    </span>
  )
}

function groupKey(name: string): string {
  const ch = (name || '').trim().charAt(0).toUpperCase()
  return /[A-Z]/.test(ch) ? ch : '#'
}

interface ContactsTabProps {
  contacts: PhoneContact[]
  loading: boolean
  error: string
  size: Size
  canCall: boolean
  callBlockedReason: string
  canSms: boolean
  onCall: (number: string) => void
  onMessage: (number: string) => void
  onRetry: () => void
  onOpenContactsApp: () => void
}

export default function ContactsTab({
  contacts, loading, error, size, canCall, callBlockedReason, canSms, onCall, onMessage, onRetry, onOpenContactsApp,
}: ContactsTabProps) {
  const [query, setQuery] = useState('')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const wide = size === 'wide'

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return contacts
    const qd = q.replace(/\D/g, '')
    return contacts.filter((c) =>
      c.name.toLowerCase().includes(q) ||
      c.org.toLowerCase().includes(q) ||
      (qd !== '' && c.phones.some((p) => p.replace(/\D/g, '').includes(qd))))
  }, [contacts, query])

  const selected = useMemo(() => filtered.find((c) => c.id === selectedId) ?? null, [filtered, selectedId])

  const rows: { key: string; contact?: PhoneContact }[] = []
  let lastGroup = ''
  for (const c of filtered) {
    const g = groupKey(c.name)
    if (g !== lastGroup) { rows.push({ key: 'h:' + g }); lastGroup = g }
    rows.push({ key: c.id, contact: c })
  }

  const list = (
    <div className="h-full flex flex-col min-h-0">
      <div className="shrink-0 p-2.5" style={{ borderBottom: '1px solid var(--border-subtle)' }}>
        <input value={query} onChange={(e) => setQuery(e.target.value)} type="search"
          placeholder="Search name or number" aria-label="Search contacts"
          className="w-full rounded-lg px-3 py-2 text-[13px] focus-primary"
          style={{ background: 'var(--bg-surface)', color: 'var(--text-primary)', border: '1px solid var(--border-default)' }} />
      </div>
      <div className="flex-1 min-h-0 overflow-y-auto">
        {loading && contacts.length === 0 ? (
          <div className="p-4 flex items-center gap-2 text-[13px]" style={{ color: 'var(--text-tertiary)' }}>
            <span className="w-3.5 h-3.5 spinner" /> Loading…
          </div>
        ) : filtered.length === 0 ? (
          <EmptyNote glyph="👤"
            title={query ? 'No matches' : 'No contacts yet'}
            body={query ? 'Try a different name or number.' : 'Contacts from your Vulos address book, an Android handset and a SIM in the box all appear here.'} />
        ) : (
          <ul>
            {rows.map((r) => {
              if (!r.contact) {
                return (
                  <li key={r.key} className="sticky top-0 px-3.5 py-1 text-[11.5px] font-semibold z-[1]"
                    style={{ background: 'var(--bg-elevated)', color: 'var(--text-muted)', borderBottom: '1px solid var(--border-subtle)' }}>
                    {r.key.slice(2)}
                  </li>
                )
              }
              const c = r.contact
              const primary = c.phones[0] || ''
              const on = c.id === selectedId
              return (
                <li key={c.id}>
                  <div className="w-full flex items-center gap-3 px-3.5 py-2.5"
                    style={{ background: on ? 'var(--bg-selected)' : 'transparent', borderBottom: '1px solid var(--border-subtle)' }}>
                    <button type="button" onClick={() => setSelectedId(on ? null : c.id)}
                      className="min-w-0 flex-1 flex items-center gap-3 text-left focus-primary rounded-md">
                      <Avatar name={c.name} number={primary} size={38} />
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center gap-1.5">
                          <span className="text-[13.5px] font-medium truncate" style={{ color: 'var(--text-primary)' }}>
                            {c.name || displayNumber(primary) || '(no name)'}
                          </span>
                          <SourceDots sources={c.sources} />
                        </span>
                        <span className="block text-[12.5px] truncate mt-0.5" style={{ color: 'var(--text-tertiary)' }}>
                          {primary ? displayNumber(primary) : c.org || c.emails[0] || 'No number'}
                        </span>
                      </span>
                    </button>
                    {primary && (
                      <CallButton size={34} onClick={() => onCall(primary)} disabled={!canCall}
                        title={canCall ? `Call ${c.name || primary}` : callBlockedReason} />
                    )}
                  </div>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )

  const detail = selected ? (
    <div className="h-full overflow-y-auto p-5">
      <div className="flex items-center gap-3.5">
        <Avatar name={selected.name} number={selected.phones[0]} size={56} />
        <div className="min-w-0">
          <div className="text-[16px] font-semibold truncate" style={{ color: 'var(--text-primary)' }}>{selected.name || '(no name)'}</div>
          {selected.org && <div className="text-[13px] truncate" style={{ color: 'var(--text-tertiary)' }}>{selected.org}</div>}
        </div>
      </div>

      {selected.phones.length > 0 && (
        <>
          <div className="mt-6 text-[11.5px] font-semibold uppercase tracking-wide" style={{ color: 'var(--text-muted)' }}>Phone</div>
          <ul className="mt-2 flex flex-col gap-2">
            {selected.phones.map((p) => (
              <li key={p} className="flex items-center gap-2.5 py-1.5">
                <span className="flex-1 min-w-0 text-[13.5px] truncate" style={{ color: 'var(--text-primary)' }}>{displayNumber(p)}</span>
                {canSms && (
                  <button type="button" onClick={() => onMessage(p)}
                    className="shrink-0 px-2.5 py-1.5 rounded-lg text-[12.5px] font-medium focus-primary transition-colors"
                    style={{ background: 'var(--bg-elevated)', color: 'var(--text-primary)', border: '1px solid var(--border-default)' }}>
                    Message
                  </button>
                )}
                <CallButton size={34} onClick={() => onCall(p)} disabled={!canCall}
                  title={canCall ? `Call ${displayNumber(p)}` : callBlockedReason} />
              </li>
            ))}
          </ul>
        </>
      )}

      {selected.emails.length > 0 && (
        <>
          <div className="mt-6 text-[11.5px] font-semibold uppercase tracking-wide" style={{ color: 'var(--text-muted)' }}>Email</div>
          <ul className="mt-2 flex flex-col gap-1">
            {selected.emails.map((e) => (
              <li key={e} className="text-[13.5px] truncate" style={{ color: 'var(--text-primary)' }}>{e}</li>
            ))}
          </ul>
        </>
      )}

      <button type="button" onClick={onOpenContactsApp}
        className="mt-7 px-3 py-2 rounded-lg text-[13px] font-medium focus-primary transition-colors"
        style={{ background: 'var(--bg-elevated)', color: 'var(--text-primary)', border: '1px solid var(--border-default)' }}>
        Edit in Contacts
      </button>
    </div>
  ) : (
    <EmptyNote glyph="👤" title="Pick a contact" body="Select someone to see their numbers and call or text them." />
  )

  return (
    <div className="h-full flex flex-col min-h-0">
      <ErrorNotice message={error} onRetry={onRetry} />
      {wide ? (
        <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: 'minmax(17rem, 22rem) 1fr' }}>
          <div className="min-h-0" style={{ borderRight: '1px solid var(--border-default)' }}>{list}</div>
          <div className="min-h-0">{detail}</div>
        </div>
      ) : (
        <div className="flex-1 min-h-0">{list}</div>
      )}
    </div>
  )
}
