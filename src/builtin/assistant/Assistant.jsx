/**
 * Assistant — the Vulos sovereign mail assistant (the wedge).
 *
 * A private AI assistant that reasons over the user's MAIL, running on the
 * user's own instance with no third-party egress by default. Talks to the
 * backend /api/assistant/* endpoints (see backend/cmd/server/routes_assistant.go).
 *
 * The headline is the SOVEREIGNTY badge: it surfaces exactly where the model
 * runs so the "nothing leaves your server" promise is visible and auditable.
 */
import { useState, useEffect, useRef, useCallback } from 'react'

// ── Sovereignty badge ────────────────────────────────────────────────────────

const EGRESS = {
  'on-instance': { dot: '#22c55e', label: 'Private · on your server', tone: 'text-neutral-400' },
  'external-configured': { dot: '#f59e0b', label: 'External model (you authorized)', tone: 'text-amber-400' },
  blocked: { dot: '#ef4444', label: 'Egress blocked — configure a local model', tone: 'text-red-400' },
}

function SovereigntyBadge({ status }) {
  if (!status) return null
  const s = status.sovereignty || {}
  const info = EGRESS[s.egress] || EGRESS['on-instance']
  const model = [s.provider, s.model].filter(Boolean).join(' · ')
  return (
    <div className="flex items-center gap-2 text-[11px]" title={s.reason || ''}>
      <span className="inline-block w-2 h-2 rounded-full" style={{ background: info.dot }} />
      <span className={info.tone}>{info.label}</span>
      {model && <span className="text-neutral-600">{model}</span>}
    </div>
  )
}

// ── Quick actions ────────────────────────────────────────────────────────────

const QUICK = [
  { id: 'attention', label: 'What needs my attention', prompt: null },
  { id: 'summarize', label: 'Summarize my inbox', prompt: null },
]

// ── Message bubble ───────────────────────────────────────────────────────────

function Bubble({ role, content, pending }) {
  const isUser = role === 'user'
  return (
    <div className={`flex ${isUser ? 'justify-end' : 'justify-start'}`}>
      <div
        className={`max-w-[85%] rounded-2xl px-3.5 py-2.5 text-[13px] leading-relaxed whitespace-pre-wrap ${
          isUser
            ? 'bg-blue-600/90 text-white rounded-br-sm'
            : 'bg-neutral-800/70 text-neutral-200 rounded-bl-sm'
        }`}
      >
        {content}
        {pending && <span className="inline-block w-1.5 h-3.5 ml-0.5 align-middle bg-neutral-400 animate-pulse" />}
      </div>
    </div>
  )
}

// ── Panel ────────────────────────────────────────────────────────────────────

export default function Assistant() {
  const [status, setStatus] = useState(null)
  const [messages, setMessages] = useState([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const scrollRef = useRef(null)
  const inputRef = useRef(null)

  useEffect(() => {
    fetch('/api/assistant/status', { credentials: 'include' })
      .then(r => (r.ok ? r.json() : null))
      .then(setStatus)
      .catch(() => {})
  }, [])

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' })
  }, [messages])

  const push = useCallback((role, content) => {
    setMessages(m => [...m, { role, content, id: Math.random().toString(36).slice(2) }])
  }, [])

  const patchLast = useCallback((content, pending) => {
    setMessages(m => {
      const copy = m.slice()
      const last = copy[copy.length - 1]
      if (last && last.role === 'assistant') copy[copy.length - 1] = { ...last, content, pending }
      return copy
    })
  }, [])

  // JSON skills (attention / summarize / search) — single-shot answers.
  const runSkill = useCallback(async (path, body, userLabel) => {
    if (busy) return
    if (userLabel) push('user', userLabel)
    push('assistant', '')
    patchLast('', true)
    setBusy(true)
    try {
      const res = await fetch(path, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body || {}),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        patchLast(data.error || `Request failed (${res.status})`, false)
        return
      }
      patchLast(data.answer ?? data.draft ?? JSON.stringify(data), false)
    } catch {
      patchLast('Could not reach the assistant. Is the box online?', false)
    } finally {
      setBusy(false)
    }
  }, [busy, push, patchLast])

  // Freeform chat — streamed, grounded in retrieved mail context.
  const sendChat = useCallback(async (text) => {
    if (busy || !text.trim()) return
    push('user', text)
    push('assistant', '')
    patchLast('', true)
    setBusy(true)
    let full = ''
    try {
      const res = await fetch('/api/assistant/chat', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message: text }),
      })
      if (!res.ok || !res.body) {
        const err = await res.json().catch(() => ({}))
        patchLast(err.error || 'Assistant unavailable.', false)
        return
      }
      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let done = false
      while (!done) {
        const { done: rdone, value } = await reader.read()
        if (rdone) break
        for (const line of decoder.decode(value).split('\n')) {
          if (!line.startsWith('data: ')) continue
          try {
            const chunk = JSON.parse(line.slice(6))
            if (chunk.error) { full = chunk.error; done = true }
            if (chunk.content) full += chunk.content
            patchLast(full, !done)
            if (chunk.done) done = true
          } catch { /* noop */ }
        }
      }
      patchLast(full || 'No response.', false)
    } catch {
      patchLast('Could not reach the assistant.', false)
    } finally {
      setBusy(false)
    }
  }, [busy, push, patchLast])

  const submit = (e) => {
    e?.preventDefault()
    const text = input.trim()
    if (!text) return
    setInput('')
    sendChat(text)
  }

  const onQuick = (q) => {
    if (q.id === 'attention') runSkill('/api/assistant/attention', {}, 'What needs my attention today?')
    else if (q.id === 'summarize') runSkill('/api/assistant/summarize', { scope: 'inbox' }, 'Summarize my inbox')
  }

  const blocked = status?.sovereignty?.egress === 'blocked'

  return (
    <div className="flex flex-col h-full bg-neutral-950 text-neutral-200 overflow-hidden">
      {/* Header */}
      <div className="flex-shrink-0 px-4 py-3 border-b border-neutral-800/60 flex items-center justify-between gap-3">
        <div className="flex items-center gap-2.5 min-w-0">
          <span className="text-lg leading-none">✦</span>
          <div className="min-w-0">
            <div className="text-[13px] font-medium text-neutral-100 leading-tight">Assistant</div>
            <div className="text-[11px] text-neutral-500 leading-tight truncate">
              Private AI over your mail{status?.mail_source ? ` · ${status.mail_source}` : ''}
            </div>
          </div>
        </div>
        <SovereigntyBadge status={status} />
      </div>

      {blocked && (
        <div className="flex-shrink-0 px-4 py-2 text-[11px] text-red-300 bg-red-950/40 border-b border-red-900/40">
          External AI egress is blocked so your mail stays on this box. Configure a local model (Ollama) or,
          to allow an external endpoint, set VULOS_ASSISTANT_ALLOW_EXTERNAL=1.
        </div>
      )}

      {/* Conversation */}
      <div ref={scrollRef} className="flex-1 overflow-y-auto px-4 py-4 space-y-3">
        {messages.length === 0 && (
          <div className="h-full flex flex-col items-center justify-center text-center gap-4 select-none">
            <div className="text-neutral-600 text-[13px] max-w-xs leading-relaxed">
              Ask about your mail. Everything you type and every message it reads stays on your own server.
            </div>
            <div className="flex flex-col gap-2 w-full max-w-xs">
              {QUICK.map(q => (
                <button
                  key={q.id}
                  onClick={() => onQuick(q)}
                  disabled={busy}
                  className="text-left text-[12px] px-3 py-2 rounded-lg bg-neutral-900/70 border border-neutral-800 text-neutral-300 hover:border-neutral-700 hover:text-neutral-100 transition-colors disabled:opacity-50"
                >
                  {q.label}
                </button>
              ))}
            </div>
          </div>
        )}
        {messages.map(m => (
          <Bubble key={m.id} role={m.role} content={m.content} pending={m.pending} />
        ))}
      </div>

      {/* Composer */}
      <form onSubmit={submit} className="flex-shrink-0 border-t border-neutral-800/60 p-3">
        {messages.length > 0 && (
          <div className="flex gap-1.5 mb-2 flex-wrap">
            {QUICK.map(q => (
              <button
                key={q.id}
                type="button"
                onClick={() => onQuick(q)}
                disabled={busy}
                className="text-[11px] px-2.5 py-1 rounded-full bg-neutral-900 border border-neutral-800 text-neutral-400 hover:text-neutral-200 hover:border-neutral-700 transition-colors disabled:opacity-40"
              >
                {q.label}
              </button>
            ))}
          </div>
        )}
        <div className="flex items-end gap-2">
          <textarea
            ref={inputRef}
            rows={1}
            value={input}
            onChange={e => setInput(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) submit(e) }}
            placeholder="Ask about your mail…"
            className="flex-1 resize-none bg-neutral-900 border border-neutral-800 rounded-xl px-3 py-2 text-[13px] text-neutral-100 placeholder-neutral-600 focus:outline-none focus:border-neutral-700 max-h-32"
          />
          <button
            type="submit"
            disabled={busy || !input.trim()}
            className="flex-shrink-0 w-9 h-9 rounded-xl bg-blue-600 text-white flex items-center justify-center hover:bg-blue-500 transition-colors disabled:opacity-40 disabled:hover:bg-blue-600"
            aria-label="Send"
          >
            <svg viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
              <path d="M10.894 2.553a1 1 0 00-1.788 0l-7 14a1 1 0 001.169 1.409l5-1.429A1 1 0 009 15.571V11a1 1 0 112 0v4.571a1 1 0 00.725.962l5 1.428a1 1 0 001.17-1.408l-7-14z" />
            </svg>
          </button>
        </div>
      </form>
    </div>
  )
}
