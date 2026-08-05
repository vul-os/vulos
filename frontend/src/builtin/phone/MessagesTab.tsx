import { useState, useEffect, useCallback, useRef, type FormEvent } from 'react'
import { nativeBridge } from '../../core/nativeBridge'
import { formatRelative, friendlyError, isRecord, toPerms, type TelephonyPerms } from './phoneUtils'

// SMS rows show up in two raw shapes depending on source: `listSms` results
// use address/date, live `onSms` pushes use from/timestamp — both are kept
// here (rather than folded into one field) so rendering stays byte-for-byte
// the same fallback chain as before.
interface Message {
  address?: string
  from?: string
  body?: string
  date?: unknown
  timestamp?: unknown
  incoming?: boolean
}

// Raw SMS rows come back from TelephonyBridge.kt as loosely-shaped JSON
// (native-bridge boundary) — narrow field-by-field rather than trusting it.
function toMessage(r: Record<string, unknown>): Message {
  return {
    address: typeof r.address === 'string' ? r.address : undefined,
    from: typeof r.from === 'string' ? r.from : undefined,
    body: typeof r.body === 'string' ? r.body : undefined,
    date: r.date,
    timestamp: r.timestamp,
    incoming: typeof r.incoming === 'boolean' ? r.incoming : undefined,
  }
}

interface MessagesTabProps {
  perms?: TelephonyPerms | null
  onPermsChange: (perms: TelephonyPerms | null) => void
}

// A flat, newest-first SMS inbox (not per-thread) — every text the device has,
// most recent first, with a compose bar to start a new one. Inbound texts
// arrive live via onSms while the tab is mounted.
export default function MessagesTab({ onPermsChange }: MessagesTabProps) {
  const [messages, setMessages] = useState<Message[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [to, setTo] = useState('')
  const [text, setText] = useState('')
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState('')
  const mounted = useRef(true)

  const load = useCallback(() => {
    setLoading(true)
    nativeBridge.telephony.listSms(50)
      .then((list: unknown) => {
        if (!mounted.current) return
        setMessages(Array.isArray(list) ? list.filter(isRecord).map(toMessage) : [])
        setError('')
      })
      .catch((e: unknown) => { if (mounted.current) setError(friendlyError(e)) })
      .finally(() => { if (mounted.current) setLoading(false) })
    nativeBridge.telephony.perms().then((p: unknown) => onPermsChange(toPerms(p))).catch(() => {})
  }, [onPermsChange])

  useEffect(() => {
    mounted.current = true
    load()
    const off = nativeBridge.telephony.onSms((m: unknown) => {
      if (!mounted.current || !isRecord(m) || m.event !== 'sms') return
      setMessages((prev) => [{
        address: typeof m.from === 'string' ? m.from : undefined,
        body: typeof m.body === 'string' ? m.body : undefined,
        date: m.timestamp,
        incoming: true,
      }, ...prev])
    })
    return () => { mounted.current = false; off() }
  }, [load])

  const send = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const num = to.trim()
    const body = text.trim()
    if (!num || !body || sending) return
    setSending(true)
    setSendError('')
    try {
      await nativeBridge.telephony.sendSms(num, body)
      setMessages((prev) => [{ address: num, body, date: Date.now(), incoming: false }, ...prev])
      setText('')
    } catch (err) {
      setSendError(friendlyError(err))
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="h-full flex flex-col">
      <form onSubmit={send} className="shrink-0 p-2.5 border-b flex flex-col gap-1.5"
        style={{ borderColor: 'var(--border-subtle, rgba(255,255,255,0.08))' }}>
        <div className="flex items-center gap-1.5">
          <input value={to} onChange={(e) => setTo(e.target.value)} placeholder="Number (e.g. +254 700 000 000)"
            inputMode="tel" aria-label="Recipient number"
            className="flex-1 min-w-0 bg-transparent border rounded-md px-2.5 py-2 text-[12.5px] focus-primary"
            style={{ borderColor: 'var(--border-subtle, rgba(255,255,255,0.15))', borderRadius: 'var(--radius-md, 0.5rem)' }} />
        </div>
        <div className="flex items-center gap-1.5">
          <input value={text} onChange={(e) => setText(e.target.value)} placeholder="Type a message…"
            aria-label="Message text"
            className="flex-1 min-w-0 bg-transparent border rounded-md px-2.5 py-2 text-[12.5px] focus-primary"
            style={{ borderColor: 'var(--border-subtle, rgba(255,255,255,0.15))', borderRadius: 'var(--radius-md, 0.5rem)' }} />
          <button type="submit" disabled={sending || !to.trim() || !text.trim()}
            className="shrink-0 px-3.5 py-2 rounded-md text-[12.5px] font-medium text-white disabled:opacity-40 hover:brightness-110 active:scale-95 focus-primary transition-all"
            style={{ background: 'var(--accent)', borderRadius: 'var(--radius-md, 0.5rem)' }}>
            {sending ? 'Sending…' : 'Send'}
          </button>
        </div>
        {sendError && <div className="text-[12px] text-danger">{sendError}</div>}
      </form>
      <div className="flex-1 min-h-0 overflow-y-auto">
        {loading && messages.length === 0 ? (
          <div className="p-4 flex items-center gap-2 text-[12px]" style={{ color: 'var(--text-secondary, #888)' }}>
            <span className="w-3.5 h-3.5 spinner" /> Loading…
          </div>
        ) : error && messages.length === 0 ? (
          <div className="p-8 text-center text-[12px]" style={{ color: 'var(--text-secondary, #888)' }}>{error}</div>
        ) : messages.length === 0 ? (
          <div className="p-10 text-center text-[12px]" style={{ color: 'var(--text-secondary, #888)' }}>
            No messages yet. Texts sent to this SIM will show up here.
          </div>
        ) : (
          <ul className="divide-y" style={{ borderColor: 'var(--border-subtle, rgba(255,255,255,0.06))' }}>
            {messages.map((m, i) => (
              <li key={i} className="px-3 py-2.5 flex flex-col gap-0.5">
                <div className="flex items-center gap-2">
                  <span className="text-[12.5px] font-medium truncate">{m.address || m.from || 'Unknown'}</span>
                  <span className="ml-auto text-[12px] shrink-0" style={{ color: 'var(--text-secondary, #888)' }}>
                    {formatRelative(m.date ?? m.timestamp)}
                  </span>
                </div>
                <div className="text-[12px] whitespace-pre-wrap break-words" style={{ color: 'var(--text-secondary, #aaa)' }}>{m.body}</div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
