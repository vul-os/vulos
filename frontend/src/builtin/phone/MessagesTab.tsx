// MessagesTab.tsx — SMS, threaded, on whichever line is active.
//
// WHY MESSAGES STAYS IN THIS APP rather than splitting out (Android ships a
// separate Messages app, so this is a deliberate divergence):
//
// SMS is a property of the same line as calls. It shares the modem, the SIM,
// the operator, the own-number, the voice/SMS capability probe, the "which of
// the three lines am I on" switcher, and the no-modem state. Splitting it out
// duplicates every one of those into a second app that must be kept in step —
// and the thing that most often needs fixing after a call is a text to the
// person you just failed to reach. It is one line; it is one app.
//
// If SMS ever grows into real messaging here (RCS, MMS, delivery receipts,
// per-thread notification settings), that is the point to reconsider.

import { useState, useEffect, useCallback, useRef, useMemo, type FormEvent } from 'react'
import { nativeBridge } from '../../core/nativeBridge'
import { getThreads, getThread, sendSms, type SmsThread, type SmsMessage } from './telephonyApi'
import { Avatar, ErrorNotice, EmptyNote } from './PhoneChrome'
import { ACCENT_FILL, type Size } from './phoneLayout'
import { formatRelative, formatClock, friendlyError, isRecord, displayNumber, secondsToMs } from './phoneUtils'
import type { LineId } from './usePhoneData'

interface MessagesTabProps {
  lineId: LineId | null
  size: Size
  names: Map<string, string>
  canSms: boolean
  /** A number handed over from Recents/Contacts/Keypad's "Message" action. */
  composeTo: string
  onComposeToConsumed: () => void
}

function digits(raw: string): string {
  const d = (raw || '').replace(/\D/g, '')
  return d.length > 9 ? d.slice(-9) : d
}

// The Android bridge returns a FLAT list of SMS rows, not threads. Group them
// here so the same threaded UI renders either line — the box already groups
// server-side (Threads()), the handset does not.
function bridgeThreads(raw: unknown): { threads: SmsThread[]; byNumber: Map<string, SmsMessage[]> } {
  const rows = Array.isArray(raw) ? raw.filter(isRecord) : []
  const byNumber = new Map<string, SmsMessage[]>()
  const order: string[] = []
  for (const r of rows) {
    const number = (typeof r.address === 'string' && r.address) || (typeof r.from === 'string' && r.from) || ''
    if (!number) continue
    const ts = Math.floor(Number(r.date ?? r.timestamp) / 1000) || 0
    const msg: SmsMessage = {
      direction: r.incoming === false ? 'outgoing' : 'incoming',
      body: typeof r.body === 'string' ? r.body : '',
      ts,
      status: '',
    }
    if (!byNumber.has(number)) { byNumber.set(number, []); order.push(number) }
    byNumber.get(number)!.push(msg)
  }
  const threads: SmsThread[] = order.map((number) => {
    const msgs = byNumber.get(number)!.slice().sort((a, b) => a.ts - b.ts)
    byNumber.set(number, msgs)
    const last = msgs[msgs.length - 1]
    return { number, contactName: '', lastBody: last?.body || '', lastTs: last?.ts || 0, unread: 0 }
  }).sort((a, b) => b.lastTs - a.lastTs)
  return { threads, byNumber }
}

export default function MessagesTab({ lineId, size, names, canSms, composeTo, onComposeToConsumed }: MessagesTabProps) {
  const [threads, setThreads] = useState<SmsThread[]>([])
  const [deviceMsgs, setDeviceMsgs] = useState<Map<string, SmsMessage[]>>(new Map())
  const [open, setOpen] = useState<string | null>(null)
  const [messages, setMessages] = useState<SmsMessage[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState('')
  const alive = useRef(true)
  const wide = size === 'wide'

  const isDevice = lineId === 'device'

  // No synchronous setState here: this is called from a mount effect, and a
  // sync setState in an effect body cascades renders. `loading` starts true.
  const load = useCallback(() => {
    const go = async () => {
      try {
        if (isDevice) {
          const raw: unknown = await nativeBridge.telephony.listSms(200)
          const { threads: ts, byNumber } = bridgeThreads(raw)
          if (!alive.current) return
          setThreads(ts); setDeviceMsgs(byNumber); setError('')
        } else if (lineId === 'box' || lineId === 'virtual') {
          const ts = await getThreads()
          if (!alive.current) return
          setThreads(ts); setError('')
        } else {
          if (!alive.current) return
          setThreads([]); setError('')
        }
      } catch (e) {
        // NOT setThreads([]): a failing service must not look like an empty inbox.
        if (alive.current) setError(friendlyError(e))
      } finally {
        if (alive.current) setLoading(false)
      }
    }
    go()
  }, [isDevice, lineId])

  const reload = useCallback(() => { setLoading(true); load() }, [load])

  useEffect(() => { alive.current = true; load(); return () => { alive.current = false } }, [load])

  // A "Message X" action from another tab opens (or starts) that thread.
  useEffect(() => {
    if (!composeTo) return
    setOpen(composeTo)
    onComposeToConsumed()
  }, [composeTo, onComposeToConsumed])

  // Load the open conversation.
  useEffect(() => {
    if (open === null) { setMessages([]); return }
    if (isDevice) { setMessages(deviceMsgs.get(open) ?? []); return }
    let live = true
    getThread(open)
      .then((ms) => { if (live) setMessages(ms) })
      .catch((e: unknown) => { if (live) setSendError(friendlyError(e)) })
    return () => { live = false }
  }, [open, isDevice, deviceMsgs])

  // Live inbound push while the tab is mounted (Android line only; the box's
  // own inbound SMS arrive on /api/telephony/ws, which the shell surfaces as a
  // sovereign notification already).
  useEffect(() => {
    if (!nativeBridge.telephony.available) return
    return nativeBridge.telephony.onSms((m: unknown) => {
      if (!alive.current || !isRecord(m) || m.event !== 'sms') return
      load()
    })
  }, [load])

  const nameFor = useCallback((number: string, fallback = '') =>
    fallback || names.get(digits(number)) || displayNumber(number) || 'Unknown', [names])

  const send = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const body = draft.trim()
    if (!open || !body || sending) return
    setSending(true)
    setSendError('')
    try {
      if (isDevice) await nativeBridge.telephony.sendSms(open, body)
      else await sendSms(open, body)
      setMessages((prev) => [...prev, { direction: 'outgoing', body, ts: Math.floor(Date.now() / 1000), status: 'sent' }])
      setDraft('')
    } catch (err) {
      setSendError(friendlyError(err))
    } finally {
      setSending(false)
    }
  }

  const openThread = useMemo(() => threads.find((t) => t.number === open), [threads, open])

  const list = (
    <div className="h-full overflow-y-auto">
      {loading && threads.length === 0 ? (
        <div className="p-4 flex items-center gap-2 text-[13px]" style={{ color: 'var(--text-tertiary)' }}>
          <span className="w-3.5 h-3.5 spinner" /> Loading…
        </div>
      ) : threads.length === 0 && !error ? (
        <EmptyNote glyph="💬" title="No messages yet"
          body={canSms ? 'Texts sent to this line will appear here.' : 'This box has no line that can send or receive texts.'} />
      ) : (
        <ul>
          {threads.map((t) => {
            const on = t.number === open
            return (
              <li key={t.number}>
                <button type="button" onClick={() => setOpen(t.number)}
                  className="w-full flex items-center gap-3 px-3.5 py-2.5 text-left transition-colors focus-primary"
                  style={{ background: on ? 'var(--bg-selected)' : 'transparent', borderBottom: '1px solid var(--border-subtle)' }}>
                  <Avatar name={nameFor(t.number, t.contactName)} number={t.number} size={38} />
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-2">
                      <span className="text-[13.5px] font-medium truncate" style={{ color: 'var(--text-primary)' }}>
                        {nameFor(t.number, t.contactName)}
                      </span>
                      <span className="ml-auto shrink-0 text-[12px]" style={{ color: 'var(--text-tertiary)' }}>
                        {formatRelative(secondsToMs(t.lastTs))}
                      </span>
                    </span>
                    <span className="block text-[12.5px] truncate mt-0.5" style={{ color: 'var(--text-tertiary)' }}>{t.lastBody}</span>
                  </span>
                  {t.unread > 0 && (
                    <span className="shrink-0 min-w-[1.15rem] h-[1.15rem] px-1 grid place-items-center rounded-full text-[11px] font-semibold"
                      style={{ background: ACCENT_FILL, color: 'var(--accent-contrast, #fff)' }}>{t.unread}</span>
                  )}
                </button>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )

  const conversation = open === null ? (
    <EmptyNote glyph="💬" title="Pick a conversation" body="Select a thread to read it and reply." />
  ) : (
    <div className="h-full flex flex-col min-h-0">
      <div className="shrink-0 flex items-center gap-2.5 px-3 py-2" style={{ borderBottom: '1px solid var(--border-default)' }}>
        {!wide && (
          <button type="button" onClick={() => setOpen(null)} aria-label="Back to conversations"
            className="shrink-0 px-1.5 py-1 rounded-md text-[15px] focus-primary" style={{ color: 'var(--text-secondary)' }}>←</button>
        )}
        <Avatar name={nameFor(open, openThread?.contactName)} number={open} size={30} />
        <span className="min-w-0 text-[13.5px] font-medium truncate" style={{ color: 'var(--text-primary)' }}>
          {nameFor(open, openThread?.contactName)}
        </span>
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto px-3 py-3 flex flex-col gap-2">
        {messages.length === 0 ? (
          <p className="text-[13px] text-center py-6" style={{ color: 'var(--text-tertiary)' }}>No messages in this conversation yet.</p>
        ) : messages.map((m, i) => {
          const out = m.direction === 'outgoing' || m.direction === 'sent'
          return (
            <div key={i} className={`max-w-[80%] px-3 py-2 rounded-2xl ${out ? 'self-end' : 'self-start'}`}
              style={{
                background: out ? ACCENT_FILL : 'var(--bg-elevated)',
                color: out ? 'var(--accent-contrast, #fff)' : 'var(--text-primary)',
                border: out ? 'none' : '1px solid var(--border-default)',
              }}>
              <div className="text-[13px] whitespace-pre-wrap break-words">{m.body}</div>
              <div className="text-[11px] mt-1 text-right"
                style={{ color: out ? 'var(--accent-contrast, #fff)' : 'var(--text-tertiary)' }}>
                {formatClock(secondsToMs(m.ts))}
              </div>
            </div>
          )
        })}
      </div>

      <form onSubmit={send} className="shrink-0 p-2.5 flex items-center gap-2" style={{ borderTop: '1px solid var(--border-default)' }}>
        <input value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="Message" aria-label="Message text"
          disabled={!canSms}
          className="flex-1 min-w-0 rounded-full px-3.5 py-2 text-[13px] focus-primary disabled:opacity-50"
          style={{ background: 'var(--bg-surface)', color: 'var(--text-primary)', border: '1px solid var(--border-default)' }} />
        <button type="submit" disabled={sending || !draft.trim() || !canSms}
          className="shrink-0 px-3.5 py-2 rounded-full text-[13px] font-medium disabled:opacity-40 hover:brightness-110 active:scale-95 focus-primary transition-all"
          style={{ background: ACCENT_FILL, color: 'var(--accent-contrast, #fff)' }}>
          {sending ? 'Sending…' : 'Send'}
        </button>
      </form>
      {sendError && <div role="alert" className="shrink-0 px-3 pb-2 text-[12.5px] text-danger">{sendError}</div>}
    </div>
  )

  return (
    <div className="h-full flex flex-col min-h-0">
      <ErrorNotice message={error} onRetry={reload} />
      {wide ? (
        <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: 'minmax(17rem, 22rem) 1fr' }}>
          <div className="min-h-0" style={{ borderRight: '1px solid var(--border-default)' }}>{list}</div>
          <div className="min-h-0">{conversation}</div>
        </div>
      ) : (
        <div className="flex-1 min-h-0">{open === null ? list : conversation}</div>
      )}
    </div>
  )
}
