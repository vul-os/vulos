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
import { runAgentTurn } from '../../core/agentStream'
import { useAutoGrow } from '../../core/useAutoGrow'
// The tier vocabulary + labels + dot colors are the SHARED contract with the
// backend Guard, the TrustBadge and the TransparencyPanel — import the single
// source of truth so the same tier never renders a different green here than it
// does in the shell chrome (they had drifted: sovereign was #22c55e locally vs
// #34d399 shared). See core/sovereignty.js.
import { TIERS, tierInfo } from '../../core/sovereignty'
// The confirmation-gate + tool-trace surfaces are shared across every assistant
// surface (this panel, Home, the Command Palette) so they stay pixel-identical
// and retheme with the shell's --status-* tokens. See ./ProposalCard.jsx.
import { ProposalCard, StepTrace } from './ProposalCard'

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
      className="flex items-center gap-2 text-[11px] rounded-md px-1.5 py-1 -mr-1 hover:bg-neutral-800/60 transition-colors focus-primary"
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
        <div className="text-[10.5px] text-warning mt-2 leading-snug">
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

// ── Message bubble ───────────────────────────────────────────────────────────

function Bubble({ role, content, pending, error, onRetry }) {
  const isUser = role === 'user'
  return (
    <div className={`flex min-w-0 ${isUser ? 'justify-end' : 'justify-start'}`}>
      <div
        style={isUser ? { background: 'var(--accent)' } : undefined}
        className={`max-w-[86%] sm:max-w-[80%] min-w-0 rounded-2xl px-3.5 py-2.5 text-[13px] leading-relaxed whitespace-pre-wrap break-words ${
          isUser
            ? 'text-white rounded-br-md shadow-[0_1px_2px_rgba(0,0,0,0.18)]'
            : error
              ? 'bg-danger-soft border border-danger-soft text-danger rounded-bl-md'
              : 'bg-neutral-800/60 border border-neutral-800/70 text-neutral-200 rounded-bl-md'
        }`}
      >
        {error && <span aria-hidden="true" className="mr-1.5">⚠</span>}
        {content || (pending && !isUser && (
          <span className="inline-flex items-center gap-2 text-neutral-500">
            <span className="va-dots" aria-hidden="true"><i /><i /><i /></span>
            Thinking…
          </span>
        ))}
        {pending && content && (
          <span
            className="va-caret inline-block w-[3px] h-[1.05em] ml-0.5 -mb-[0.12em] rounded-full align-baseline"
            style={{ background: 'var(--accent)' }}
            aria-hidden="true"
          />
        )}
        {error && onRetry && (
          <button
            type="button"
            onClick={onRetry}
            className="block mt-1.5 text-[12px] font-medium text-danger underline decoration-danger/40 underline-offset-2 hover:decoration-danger transition-colors focus-primary rounded"
          >
            Retry
          </button>
        )}
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
  const inputRef = useAutoGrow(input, { maxHeight: 128 })
  // Aborts the in-flight streaming turn when the window is closed mid-stream.
  // Without this the SSE fetch + its reader keep running after unmount and the
  // token/status callbacks call setState on a gone component (React warning +
  // a leaked reader holding the response buffer). Cleared on turn completion.
  const streamCtl = useRef(null)

  useEffect(() => {
    fetch('/api/assistant/status', { credentials: 'include' })
      .then(r => (r.ok ? r.json() : null))
      .then(setStatus)
      .catch(() => {})
  }, [])

  // Abort any in-flight streaming turn on unmount (window close / view switch),
  // so the stream is torn down instead of leaking and writing to dead state.
  useEffect(() => () => { streamCtl.current?.abort() }, [])

  // Esc closes the tier picker (matches the shell's overlay dismissal contract).
  useEffect(() => {
    if (!pickerOpen) return
    const onKey = (e) => { if (e.key === 'Escape') { e.stopPropagation(); setPickerOpen(false) } }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [pickerOpen])

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

  const patchLast = useCallback((content, pending, error = false) => {
    setMessages(m => {
      const copy = m.slice()
      const last = copy[copy.length - 1]
      if (last && last.role === 'assistant') copy[copy.length - 1] = { ...last, content, pending, error }
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
        patchLast(data.error || `Request failed (${res.status})`, false, true)
        return
      }
      patchLast(data.answer ?? data.draft ?? JSON.stringify(data), false)
    } catch {
      patchLast('Could not reach the assistant. Is the box online?', false, true)
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

  // Freeform chat — the TOOL-USING agent turn, STREAMED (Wave 17). The model may
  // call read-only tools (which run on the box; each surfaces a live "using …"
  // status) and either STREAMS a final answer token-by-token or returns a
  // PROPOSAL for a mutating action, which we render with Approve/Reject. History
  // is the prior user/assistant text turns (proposals excluded). runAgentTurn
  // falls back to the non-streaming /agent if streaming can't be established.
  const sendChat = useCallback(async (text) => {
    if (busy || !text.trim()) return
    // Errored turns (failed streams / unreachable box) are excluded from history
    // so a Retry — which may still see the failed bubble in this closure before
    // its removal commits — never feeds the model its own error text.
    const history = messages
      .filter(m => (m.role === 'user' || m.role === 'assistant') && !m.proposal && m.content && !m.error)
      .map(m => ({ role: m.role, content: m.content }))
    push('user', text)
    push('assistant', '')
    patchLast('', true)
    setBusy(true)
    // Accumulate the read-only tool trace so we can show WHAT the assistant did.
    const liveSteps = []
    const ctl = new AbortController()
    streamCtl.current = ctl
    try {
      const result = await runAgentTurn({
        message: text,
        history,
        signal: ctl.signal,
        // Live tokens: keep the bubble pending (spinner) while text streams in.
        onToken: (_delta, full) => patchLast(full, true),
        // A read-only tool started: show a transient status line + record it.
        onStatus: (ev) => patchLast(ev.content || 'thinking…', true),
        onStep: (step) => { liveSteps.push(step) },
        // A mutating action: convert the pending bubble into a proposal card.
        onProposal: (proposal) => {
          setMessages(m => {
            const copy = m.slice()
            const last = copy[copy.length - 1]
            if (last && last.role === 'assistant') {
              copy[copy.length - 1] = { ...last, pending: false, proposal, state: 'pending', content: last.content || '' }
            }
            return copy
          })
        },
      })
      // The terminal event may carry a richer trace (args + result); prefer it,
      // else fall back to the status-derived steps collected above.
      const finalSteps = (result.steps && result.steps.length) ? result.steps : liveSteps
      if (result.error) {
        patchLast(result.error, false, true)
      } else if (result.proposal) {
        // onProposal already rendered the card; just clear the pending flag.
        setMessages(m => {
          const copy = m.slice()
          const last = copy[copy.length - 1]
          if (last && last.role === 'assistant') copy[copy.length - 1] = { ...last, pending: false }
          return copy
        })
      } else {
        patchLast(result.answer || 'No response.', false)
      }
      // Attach the tool trace to the last assistant message so it renders.
      if (finalSteps.length) {
        setMessages(m => {
          const copy = m.slice()
          const last = copy[copy.length - 1]
          if (last && last.role === 'assistant') copy[copy.length - 1] = { ...last, steps: finalSteps }
          return copy
        })
      }
    } catch (err) {
      // Aborted by unmount/window-close: the component is gone, so do NOT touch
      // state (that is the leak/warning we are preventing). Any other failure is
      // surfaced in the bubble.
      if (err?.name !== 'AbortError') patchLast('Could not reach the assistant.', false, true)
    } finally {
      if (streamCtl.current === ctl) streamCtl.current = null
      // Skip the state update if this turn was aborted (component unmounted).
      if (!ctl.signal.aborted) setBusy(false)
    }
  }, [busy, messages, push, patchLast])

  const submit = (e) => {
    e?.preventDefault()
    const text = input.trim()
    if (!text) return
    setInput('')
    sendChat(text)
  }

  // Retry a failed turn: drop the trailing failed user→assistant pair (so the
  // failed error bubble + its prompt don't linger in the transcript or in the
  // history sent to the model) and re-send the same prompt.
  const retryLast = useCallback(() => {
    if (busy) return
    let text = ''
    setMessages(m => {
      const copy = m.slice()
      // Trailing errored assistant bubble.
      if (copy.length && copy[copy.length - 1].role === 'assistant') copy.pop()
      // Its originating user turn.
      if (copy.length && copy[copy.length - 1].role === 'user') text = copy.pop().content
      return copy
    })
    if (text) setTimeout(() => sendChat(text), 0)
  }, [busy, sendChat])

  const onQuick = (q) => {
    if (q.id === 'attention') runSkill('/api/assistant/attention', {}, 'What needs my attention today?')
    else if (q.id === 'summarize') runSkill('/api/assistant/summarize', { scope: 'inbox' }, 'Summarize my inbox')
  }

  const currentTier = status?.tier || status?.sovereignty?.tier
  const blocked = status?.sovereignty && status.sovereignty.allowed === false

  return (
    <div className="flex flex-col h-full bg-neutral-950 text-neutral-200 overflow-hidden">
      <style>{`
        @keyframes vaCaret { 0%,45% { opacity: 1 } 55%,100% { opacity: 0.12 } }
        .va-caret { animation: vaCaret 1s var(--ease-standard) infinite; }
        @keyframes vaDot { 0%,80%,100% { transform: translateY(0); opacity: .35 } 40% { transform: translateY(-3px); opacity: 1 } }
        .va-dots { display: inline-flex; gap: 3px; align-items: center; }
        .va-dots i { width: 5px; height: 5px; border-radius: 9999px; background: currentColor; display: inline-block; animation: vaDot 1.2s var(--ease-standard) infinite; }
        .va-dots i:nth-child(2) { animation-delay: .15s }
        .va-dots i:nth-child(3) { animation-delay: .3s }
        @keyframes vaFloat { 0%,100% { transform: translateY(0) } 50% { transform: translateY(-4px) } }
        .va-float { animation: vaFloat 4s var(--ease-standard) infinite; }
        @media (prefers-reduced-motion: reduce) {
          .va-caret, .va-dots i, .va-float { animation: none !important }
        }
      `}</style>
      {/* Header */}
      <div className="flex-shrink-0 px-3 sm:px-4 py-3 border-b border-neutral-800/60 flex items-center justify-between gap-3">
        <div className="flex items-center gap-2.5 min-w-0">
          <span className="w-7 h-7 rounded-lg flex items-center justify-center accent-bg-soft accent-text text-[15px] leading-none flex-shrink-0" aria-hidden="true">✦</span>
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
        <div className="flex-shrink-0 px-3 sm:px-4 py-2 text-[11px] text-danger bg-danger-soft border-b border-danger-soft">
          This endpoint's tier ({tierInfo(currentTier).label}) is not permitted, so your mail stays inside the
          sovereignty boundary. Pick a local or sovereign endpoint, or set VULOS_ASSISTANT_ALLOW_EXTERNAL=1 to
          authorize a brokered/external one.
        </div>
      )}

      {/* Conversation */}
      <div ref={scrollRef} role="log" aria-live="polite" aria-atomic="false" aria-busy={busy}
        className="flex-1 overflow-y-auto px-3 sm:px-4 py-4 space-y-3.5">
        {messages.length === 0 && (
          <div className="h-full flex flex-col items-center justify-center text-center gap-5 select-none px-2">
            <div
              className="va-float w-14 h-14 rounded-2xl flex items-center justify-center text-[24px] leading-none accent-bg-soft accent-text ring-1 ring-[var(--border-default)]"
              aria-hidden="true"
            >
              ✦
            </div>
            <div className="text-neutral-400 text-[13px] max-w-xs leading-relaxed">
              Ask about your mail. Everything you type and every message it reads
              stays on your own server.
            </div>
            <div className="flex flex-col gap-2 w-full max-w-xs">
              {QUICK.map(q => (
                <button
                  key={q.id}
                  onClick={() => onQuick(q)}
                  disabled={busy}
                  className="group flex items-center gap-2.5 text-left text-[12.5px] px-3.5 py-2.5 rounded-xl bg-neutral-900/70 border border-neutral-800 text-neutral-300 hover:border-neutral-700 hover:bg-neutral-900 hover:text-neutral-100 transition-colors disabled:opacity-50 focus-primary"
                >
                  <span className="accent-text opacity-50 group-hover:opacity-100 transition-opacity" aria-hidden="true">→</span>
                  <span className="min-w-0">{q.label}</span>
                </button>
              ))}
            </div>
          </div>
        )}
        {messages.map((m, i) => {
          const isLast = i === messages.length - 1
          return m.proposal ? (
            <div key={m.id} className="flex justify-start flex-col gap-2 items-start min-w-0 w-full">
              {m.content && <Bubble role="assistant" content={m.content} />}
              <StepTrace steps={m.steps} className="max-w-[86%] sm:max-w-[80%]" />
              <div className="max-w-[86%] sm:max-w-[80%] w-full">
                <ProposalCard
                  proposal={m.proposal}
                  state={m.state}
                  onApprove={() => approveProposal(m.id, m.proposal)}
                  onReject={() => rejectProposal(m.id)}
                />
              </div>
            </div>
          ) : m.steps ? (
            <div key={m.id} className="flex justify-start flex-col gap-1.5 items-start min-w-0 w-full">
              <Bubble role={m.role} content={m.content} pending={m.pending} error={m.error}
                onRetry={m.error && isLast ? retryLast : undefined} />
              <StepTrace steps={m.steps} className="max-w-[86%] sm:max-w-[80%]" />
            </div>
          ) : (
            <Bubble key={m.id} role={m.role} content={m.content} pending={m.pending} error={m.error}
              onRetry={m.error && isLast ? retryLast : undefined} />
          )
        })}
      </div>

      {/* Composer */}
      <form onSubmit={submit} className="flex-shrink-0 border-t border-neutral-800/60 bg-neutral-950/60 p-3">
        {messages.length > 0 && (
          <div className="flex gap-1.5 mb-2 flex-wrap">
            {QUICK.map(q => (
              <button
                key={q.id}
                type="button"
                onClick={() => onQuick(q)}
                disabled={busy}
                className="text-[11px] px-2.5 py-1 rounded-full bg-neutral-900 border border-neutral-800 text-neutral-400 hover:text-neutral-200 hover:border-neutral-700 transition-colors disabled:opacity-40 focus-primary"
              >
                {q.label}
              </button>
            ))}
          </div>
        )}
        <div className="flex items-end gap-2">
          <div className="flex-1 min-w-0 rounded-2xl bg-neutral-900 border border-neutral-800 transition-colors focus-within:border-[color-mix(in_srgb,var(--accent)_55%,var(--border-emphasis))]">
            <textarea
              ref={inputRef}
              rows={1}
              value={input}
              onChange={e => setInput(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) submit(e) }}
              placeholder="Ask about your mail…"
              aria-label="Ask about your mail"
              className="w-full min-w-0 resize-none bg-transparent rounded-2xl px-3.5 py-2.5 text-[13px] text-neutral-100 placeholder-neutral-600 focus:outline-none"
            />
          </div>
          <button
            type="submit"
            disabled={busy || !input.trim()}
            style={{ background: 'var(--accent)' }}
            className="flex-shrink-0 w-11 h-11 rounded-2xl text-white flex items-center justify-center transition-[filter,opacity] hover:brightness-110 disabled:opacity-40 disabled:hover:brightness-100 focus-primary"
            aria-label="Send"
            title="Send (Enter)"
          >
            <svg viewBox="0 0 20 20" fill="currentColor" width="17" height="17">
              <path d="M10.894 2.553a1 1 0 00-1.788 0l-7 14a1 1 0 001.169 1.409l5-1.429A1 1 0 009 15.571V11a1 1 0 112 0v4.571a1 1 0 00.725.962l5 1.428a1 1 0 001.17-1.408l-7-14z" />
            </svg>
          </button>
        </div>
      </form>
    </div>
  )
}
