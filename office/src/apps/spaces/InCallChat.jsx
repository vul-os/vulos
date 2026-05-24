/**
 * InCallChat — lightweight in-call chat sidebar (OFFICE-66).
 *
 * Posts messages to the call's originating channel/thread via the Spaces API
 * (api.spacesSendMessage) and the CRDT MessageStore, so chat persists in Vulos Spaces
 * history after the call ends.
 *
 * Design pass: themed against the call surface (warm ink background,
 * accent-tint own-bubble, paper text), shares the slide-in animation with
 * the right-rail panels.
 *
 * Props:
 *   channelId   — Spaces channel/thread id tied to this call session
 *   threadParent — optional parent message id (for meeting-room threads)
 *   identity    — { displayName, accountAddress } used as author label
 *   onClose     — called when the panel is dismissed
 */
import { useEffect, useRef, useState, useCallback } from 'react'
import { Send, X } from 'lucide-react'
import { api } from '../../lib/api.js'
import { getDefaultStore } from '../../lib/crdt/messages.js'

const POLL_MS = 3000

export default function InCallChat({ channelId, threadParent = '', identity, onClose }) {
  const [messages, setMessages] = useState([])
  const [body, setBody] = useState('')
  const [sending, setSending] = useState(false)
  const bottomRef = useRef(null)
  const pollRef = useRef(null)
  const store = getDefaultStore()

  const loadMessages = useCallback(async () => {
    if (!channelId) return
    try {
      const remote = await api.spacesListMessages(channelId)
      store.mergeOps(
        remote.map((m) => ({
          op: m.state === 'deleted' ? 'tombstone' : m.state === 'edited' ? 'edit' : 'append',
          channel_id: channelId,
          msg: {
            id: m.id,
            channel_id: channelId,
            thread_parent: m.thread_parent || '',
            author_id: m.author_id,
            body: m.body,
            state: m.state,
            seq_clock: m.seq_clock || '',
            created_at: m.created_at,
            updated_at: m.updated_at,
          },
          applied_at: m.updated_at,
        })),
      )
    } catch (_) {
      // Offline-tolerant: show whatever is in the local CRDT store
    }
    const all = store.listMessages(channelId)
    const visible = threadParent
      ? all.filter((m) => m.thread_parent === threadParent || m.id === threadParent)
      : all
    setMessages(visible.filter((m) => m.state !== 'deleted'))
  }, [channelId, threadParent, store])

  useEffect(() => {
    loadMessages()
    pollRef.current = setInterval(loadMessages, POLL_MS)
    return () => clearInterval(pollRef.current)
  }, [loadMessages])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages.length])

  const handleSend = useCallback(async () => {
    const text = body.trim()
    if (!text || !channelId) return
    setSending(true)
    try {
      const authorId = identity?.accountAddress || identity?.displayName || 'you'
      store.send(channelId, authorId, text, threadParent)
      setMessages(
        store
          .listMessages(channelId)
          .filter((m) =>
            threadParent
              ? m.thread_parent === threadParent || m.id === threadParent
              : true,
          )
          .filter((m) => m.state !== 'deleted'),
      )
      setBody('')
      await api.spacesSendMessage(channelId, text, threadParent)
      await loadMessages()
    } catch (e) {
      console.error('[InCallChat] send failed', e)
    } finally {
      setSending(false)
    }
  }, [body, channelId, threadParent, identity, store, loadMessages])

  const handleKey = useCallback(
    (e) => {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault()
        handleSend()
      }
    },
    [handleSend],
  )

  return (
    <aside
      className="w-72 flex flex-col border-l border-paper/10 text-paper text-sm animate-slide-in-right"
      style={{ background: 'var(--ink)' }}
    >
      {/* Header */}
      <div className="flex items-center justify-between px-3 h-11 border-b border-paper/10 shrink-0">
        <span className="font-semibold text-paper tracking-tightish">In-call chat</span>
        {onClose && (
          <button
            type="button"
            onClick={onClose}
            className="inline-flex items-center justify-center w-7 h-7 rounded-sm text-paper/60 hover:text-paper hover:bg-paper/10 transition-colors duration-fast"
            title="Close chat"
            aria-label="Close chat"
          >
            <X size={14} />
          </button>
        )}
      </div>

      {/* Messages */}
      <div className="flex-1 overflow-y-auto px-3 py-3 space-y-3">
        {messages.length === 0 && (
          <p className="text-paper/50 text-xs text-center mt-4 font-serif italic">
            No messages yet. Chat here — it persists in Vulos Spaces after the call.
          </p>
        )}
        {messages.map((m) => (
          <ChatMessage
            key={m.id}
            message={m}
            selfId={identity?.accountAddress || identity?.displayName}
          />
        ))}
        <div ref={bottomRef} />
      </div>

      {/* Composer */}
      <div className="shrink-0 px-3 py-2 border-t border-paper/10 flex gap-2 items-end">
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          onKeyDown={handleKey}
          placeholder="Message…"
          rows={2}
          className="flex-1 resize-none bg-paper/5 text-paper placeholder-paper/40 rounded-sm px-2 py-1.5 text-xs border border-paper/10 focus:outline-none focus:border-accent focus:shadow-focus transition-[border-color,box-shadow] duration-fast"
        />
        <button
          type="button"
          onClick={handleSend}
          disabled={!body.trim() || sending}
          className="inline-flex items-center justify-center w-9 h-9 rounded-sm bg-accent text-white hover:bg-accent-hover disabled:opacity-40 transition-[background,opacity] duration-fast"
          title="Send"
          aria-label="Send"
        >
          <Send size={14} />
        </button>
      </div>
    </aside>
  )
}

function ChatMessage({ message, selfId }) {
  const isSelf = message.author_id === selfId
  return (
    <div className={`flex flex-col ${isSelf ? 'items-end' : 'items-start'} gap-0.5`}>
      <span className="text-[10px] text-paper/50 tracking-tightish">
        {isSelf ? 'You' : message.author_id}
      </span>
      <div
        className={[
          'max-w-[90%] px-2.5 py-1.5 rounded-md text-xs leading-relaxed whitespace-pre-wrap break-words tracking-tightish',
          isSelf
            ? 'bg-accent text-white'
            : 'bg-paper/10 text-paper border border-paper/10',
        ].join(' ')}
      >
        {message.body}
      </div>
    </div>
  )
}
