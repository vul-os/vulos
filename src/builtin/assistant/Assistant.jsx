/**
 * Assistant — the Vulos sovereign mail assistant (the wedge).
 *
 * A private AI assistant that reasons over the user's MAIL, running on the
 * user's own instance with no third-party egress by default. Talks to the
 * backend /api/assistant/* endpoints (see backend/cmd/server/routes_assistant.go).
 *
 * The headline is the SOVEREIGNTY badge: it surfaces exactly which TIER the
 * model runs in — "where your AI runs" — so the posture is honest, visible, and
 * auditable. A minimal picker lets the operator choose the tier.
 */
import { useState, useEffect, useRef, useCallback } from 'react'

// ── Sovereignty tiers ────────────────────────────────────────────────────────
// The tier vocabulary + labels are the shared contract with the backend Guard
// and the llmux gateway. Ordered most → least private.

const TIERS = {
  local:     { dot: '#22c55e', label: 'On your device',                    tone: 'text-emerald-400', blurb: 'Inference runs on this box. Nothing leaves your server.' },
  sovereign: { dot: '#22c55e', label: 'Vulos sovereign · in-region, no-train', tone: 'text-emerald-400', blurb: 'A Vulos-operated in-region endpoint inside the sovereignty boundary. No training on your data.' },
  brokered:  { dot: '#f59e0b', label: 'Brokered · no-train',              tone: 'text-amber-400',   blurb: 'A named third-party model under a no-train agreement. Requires the egress opt-in.' },
  external:  { dot: '#ef4444', label: 'External · not private',           tone: 'text-red-400',     blurb: 'An off-box endpoint that may mine or train on your data. Blocked unless explicitly authorized.' },
}

const tierInfo = (tier) => TIERS[tier] || TIERS.external

function SovereigntyBadge({ status, onClick }) {
  if (!status) return null
  const tier = status.tier || status.sovereignty?.tier || 'external'
  const info = tierInfo(tier)
  const label = status.label || info.label
  const model = [status.sovereignty?.provider, status.sovereignty?.model].filter(Boolean).join(' · ')
  return (
    <button
      type="button"
      onClick={onClick}
      title={status.sovereignty?.reason || info.blurb}
      className="flex items-center gap-2 text-[11px] rounded-md px-1.5 py-1 -mr-1 hover:bg-neutral-800/60 transition-colors"
    >
      <span className="inline-block w-2 h-2 rounded-full" style={{ background: info.dot }} />
      <span className={info.tone}>{label}</span>
      {model && <span className="text-neutral-600 hidden sm:inline">{model}</span>}
      <svg viewBox="0 0 20 20" fill="currentColor" width="11" height="11" className="text-neutral-600">
        <path fillRule="evenodd" d="M5.23 7.21a.75.75 0 011.06.02L10 11.17l3.71-3.94a.75.75 0 111.08 1.04l-4.25 4.5a.75.75 0 01-1.08 0l-4.25-4.5a.75.75 0 01.02-1.06z" clipRule="evenodd" />
      </svg>
    </button>
  )
}

// ── Tier picker ──────────────────────────────────────────────────────────────
// Lets the operator declare "where your AI runs". POSTs to /api/assistant/tier;
// the backend Guard stays authoritative (loopback is always local, brokered/
// external still need the egress opt-in), so this labels the posture honestly —
// it can never weaken the guarantee.

function TierPicker({ status, options, current, onPick, busy, onClose }) {
  const opts = options && options.length ? options : [
    { tier: 'local', label: TIERS.local.label },
    { tier: 'sovereign', label: TIERS.sovereign.label },
    { tier: 'brokered', label: TIERS.brokered.label },
  ]
  return (
    <div className="flex-shrink-0 px-4 py-3 border-b border-neutral-800/60 bg-neutral-900/40">
      <div className="flex items-center justify-between mb-2">
        <div className="text-[11px] font-medium text-neutral-300">Where your AI runs</div>
        <button type="button" onClick={onClose} className="text-neutral-600 hover:text-neutral-300 text-[11px]">Done</button>
      </div>
      <div className="flex flex-col gap-1.5">
        {opts.map(o => {
          const info = tierInfo(o.tier)
          const active = current === o.tier
          return (
            <button
              key={o.tier}
              type="button"
              disabled={busy}
              onClick={() => onPick(o.tier)}
              className={`text-left rounded-lg px-3 py-2 border transition-colors disabled:opacity-50 ${
                active
                  ? 'bg-neutral-800/80 border-neutral-600'
                  : 'bg-neutral-900/60 border-neutral-800 hover:border-neutral-700'
              }`}
            >
              <div className="flex items-center gap-2">
                <span className="inline-block w-2 h-2 rounded-full" style={{ background: info.dot }} />
                <span className={`text-[12px] ${info.tone}`}>{o.label || info.label}</span>
                {active && <span className="ml-auto text-[10px] text-neutral-500">current</span>}
              </div>
              <div className="text-[10.5px] text-neutral-500 mt-0.5 leading-snug pl-4">{info.blurb}</div>
            </button>
          )
        })}
      </div>
      {status?.sovereignty && !status.sovereignty.allowed && (
        <div className="text-[10.5px] text-amber-400/80 mt-2 leading-snug">
          This tier needs the egress opt-in (VULOS_ASSISTANT_ALLOW_EXTERNAL=1) before mail is sent to it.
        </div>
      )}
    </div>
  )
}

// ── Quick actions ────────────────────────────────────────────────────────────

const QUICK = [
  { id: 'attention', label: 'What needs my attention', prompt: null },
  { id: 'summarize', label: 'Summarize my inbox', prompt: null },
]

// ── Proposal card ────────────────────────────────────────────────────────────
// The CONFIRMATION GATE surface. When the agent wants to DO something that
// mutates state (send an email, schedule an event, add a contact, triage a
// message) it returns a PROPOSAL — nothing has happened yet. The user must
// Approve before /api/assistant/execute runs the real action, or Reject to drop
// it. Read-only tools never reach here.

const PROPOSAL_VERB = {
  send_email: 'Send email',
  create_calendar_event: 'Create event',
  add_contact: 'Add contact',
  triage: 'Change mailbox',
}

function ProposalCard({ proposal, state, onApprove, onReject }) {
  const verb = PROPOSAL_VERB[proposal.tool] || 'Action'
  const args = proposal.args || {}
  return (
    <div className="max-w-[85%] rounded-2xl rounded-bl-sm border border-amber-500/30 bg-amber-950/20 px-3.5 py-3 text-[13px]">
      <div className="flex items-center gap-2 mb-1.5">
        <span className="inline-block w-2 h-2 rounded-full bg-amber-400" />
        <span className="text-amber-300 font-medium text-[12px]">Needs your approval · {verb}</span>
      </div>
      <div className="text-neutral-200 leading-relaxed mb-1">{proposal.summary}</div>
      {(args.body || args.notes) && (
        <div className="text-[12px] text-neutral-400 whitespace-pre-wrap bg-neutral-900/50 rounded-lg px-2.5 py-2 mt-1.5 max-h-40 overflow-y-auto">
          {args.body || args.notes}
        </div>
      )}
      {state === 'done' ? (
        <div className="text-[12px] text-emerald-400 mt-2">✓ Approved and executed.</div>
      ) : state === 'rejected' ? (
        <div className="text-[12px] text-neutral-500 mt-2">Rejected — nothing was done.</div>
      ) : (
        <div className="flex gap-2 mt-2.5">
          <button
            type="button"
            disabled={state === 'busy'}
            onClick={onApprove}
            className="text-[12px] px-3 py-1.5 rounded-lg bg-emerald-600 text-white hover:bg-emerald-500 transition-colors disabled:opacity-50"
          >
            {state === 'busy' ? 'Working…' : 'Approve'}
          </button>
          <button
            type="button"
            disabled={state === 'busy'}
            onClick={onReject}
            className="text-[12px] px-3 py-1.5 rounded-lg bg-neutral-800 text-neutral-300 hover:bg-neutral-700 transition-colors disabled:opacity-50"
          >
            Reject
          </button>
        </div>
      )}
    </div>
  )
}

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
  const [pickerOpen, setPickerOpen] = useState(false)
  const [tierBusy, setTierBusy] = useState(false)
  const scrollRef = useRef(null)
  const inputRef = useRef(null)

  useEffect(() => {
    fetch('/api/assistant/status', { credentials: 'include' })
      .then(r => (r.ok ? r.json() : null))
      .then(setStatus)
      .catch(() => {})
  }, [])

  // Operator picks the sovereignty tier. The backend Guard remains
  // authoritative; the returned status carries the honest resulting tier.
  const pickTier = useCallback(async (tier) => {
    setTierBusy(true)
    try {
      const res = await fetch('/api/assistant/tier', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tier }),
      })
      if (res.ok) {
        const data = await res.json().catch(() => null)
        if (data) setStatus(s => ({ ...(s || {}), ...data }))
      }
    } catch { /* leave status as-is */ } finally {
      setTierBusy(false)
    }
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

  const patchById = useCallback((id, patch) => {
    setMessages(m => m.map(msg => (msg.id === id ? { ...msg, ...patch } : msg)))
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

  // Approve a proposal → POST it to /api/assistant/execute (the second half of
  // the confirmation round-trip). Only here does a mutating action actually run.
  const approveProposal = useCallback(async (msgId, proposal) => {
    patchById(msgId, { state: 'busy' })
    try {
      // Send ONLY the opaque proposal id: the server executes the args it stored
      // when it issued the proposal (never client-supplied args), so a forged
      // proposal can't run. See routes_assistant.go /execute.
      const res = await fetch('/api/assistant/execute', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: proposal.id }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        patchById(msgId, { state: 'pending' })
        push('assistant', data.error || `Could not complete the action (${res.status}).`)
        return
      }
      patchById(msgId, { state: 'done' })
      if (data.result) push('assistant', data.result)
    } catch {
      patchById(msgId, { state: 'pending' })
      push('assistant', 'Could not reach the assistant to run the action.')
    }
  }, [patchById, push])

  const rejectProposal = useCallback((msgId) => {
    patchById(msgId, { state: 'rejected' })
  }, [patchById])

  // Freeform chat — the TOOL-USING agent turn. The model may call read-only
  // tools (which run on the box) and return either a final answer or a PROPOSAL
  // for a mutating action, which we render with Approve/Reject. History is the
  // prior user/assistant text turns (proposals are excluded).
  const sendChat = useCallback(async (text) => {
    if (busy || !text.trim()) return
    const history = messages
      .filter(m => (m.role === 'user' || m.role === 'assistant') && !m.proposal && m.content)
      .map(m => ({ role: m.role, content: m.content }))
    push('user', text)
    push('assistant', '')
    patchLast('', true)
    setBusy(true)
    try {
      const res = await fetch('/api/assistant/agent', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message: text, history }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        patchLast(data.error || `Assistant unavailable (${res.status}).`, false)
        return
      }
      if (data.proposal) {
        // Replace the pending assistant bubble with a proposal card.
        setMessages(m => {
          const copy = m.slice()
          const last = copy[copy.length - 1]
          if (last && last.role === 'assistant') {
            copy[copy.length - 1] = { ...last, pending: false, proposal: data.proposal, state: 'pending', content: data.answer || '' }
          }
          return copy
        })
      } else {
        patchLast(data.answer || 'No response.', false)
      }
    } catch {
      patchLast('Could not reach the assistant.', false)
    } finally {
      setBusy(false)
    }
  }, [busy, messages, push, patchLast])

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

  const currentTier = status?.tier || status?.sovereignty?.tier
  const blocked = status?.sovereignty && status.sovereignty.allowed === false

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
        <SovereigntyBadge status={status} onClick={() => setPickerOpen(o => !o)} />
      </div>

      {pickerOpen && (
        <TierPicker
          status={status}
          options={status?.tier_options}
          current={currentTier}
          busy={tierBusy}
          onPick={pickTier}
          onClose={() => setPickerOpen(false)}
        />
      )}

      {blocked && (
        <div className="flex-shrink-0 px-4 py-2 text-[11px] text-red-300 bg-red-950/40 border-b border-red-900/40">
          This endpoint's tier ({tierInfo(currentTier).label}) is not permitted, so your mail stays inside the
          sovereignty boundary. Pick a local or sovereign endpoint, or set VULOS_ASSISTANT_ALLOW_EXTERNAL=1 to
          authorize a brokered/external one.
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
          m.proposal ? (
            <div key={m.id} className="flex justify-start flex-col gap-2 items-start">
              {m.content && <Bubble role="assistant" content={m.content} />}
              <ProposalCard
                proposal={m.proposal}
                state={m.state}
                onApprove={() => approveProposal(m.id, m.proposal)}
                onReject={() => rejectProposal(m.id)}
              />
            </div>
          ) : (
            <Bubble key={m.id} role={m.role} content={m.content} pending={m.pending} />
          )
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
