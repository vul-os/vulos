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
import { runAgentTurn, type AgentProposal, type AgentStep } from '../../core/agentStream'
import { useAutoGrow } from '../../core/useAutoGrow'
// The tier vocabulary + labels + dot colors are the SHARED contract with the
// backend Guard, the TrustBadge and the TransparencyPanel — import the single
// source of truth so the same tier never renders a different green here than it
// does in the shell chrome (they had drifted: sovereign was #22c55e locally vs
// #34d399 shared). See core/sovereignty.js.
import { TIERS, tierInfo } from '../../core/sovereignty'
// The confirmation-gate + tool-trace surfaces are shared across every assistant
// surface (this panel, Home, the Command Palette) so they stay pixel-identical
// and retheme with the shell's --status-* tokens. See ./ProposalCard.tsx.
import { ProposalCard, StepTrace, type ProposalState, type StepTraceItem } from './ProposalCard'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// ── /api/assistant/status shape ─────────────────────────────────────────────
// Narrowed off the wire (a trust boundary) rather than trusted as `any` — see
// isRecord()-style narrowing in src/lib/offlineAuth.ts. Mirrors the JSON built
// by GET /api/assistant/status and POST /api/assistant/tier in
// routes_assistant.go (the `sovereignty` block is backend Sovereignty, see
// services/assistant/sovereign.go — note `allowed` there, distinct from
// core/sovereignty.ts's SovereigntyBlock, which is a different consumer's
// narrower view and doesn't carry it).
interface AssistantSovereignty {
  tier?: string
  label?: string
  reason?: string
  provider?: string
  model?: string
  endpoint?: string
  allowed?: boolean
  external_allowed?: boolean
}

interface AssistantTierOption {
  tier: string
  label?: string
}

interface AssistantStatus {
  tier?: string
  label?: string
  sovereignty?: AssistantSovereignty
  tier_options?: AssistantTierOption[]
  mail_source?: string
  semantic_index?: boolean
  files_enabled?: boolean
  reminders_enabled?: boolean
}

function toAssistantSovereignty(x: unknown): AssistantSovereignty | undefined {
  if (!isRecord(x)) return undefined
  const out: AssistantSovereignty = {}
  if (typeof x.tier === 'string') out.tier = x.tier
  if (typeof x.label === 'string') out.label = x.label
  if (typeof x.reason === 'string') out.reason = x.reason
  if (typeof x.provider === 'string') out.provider = x.provider
  if (typeof x.model === 'string') out.model = x.model
  if (typeof x.endpoint === 'string') out.endpoint = x.endpoint
  if (typeof x.allowed === 'boolean') out.allowed = x.allowed
  if (typeof x.external_allowed === 'boolean') out.external_allowed = x.external_allowed
  return out
}

function toAssistantTierOptions(x: unknown): AssistantTierOption[] | undefined {
  if (!Array.isArray(x)) return undefined
  const opts: AssistantTierOption[] = []
  for (const o of x) {
    if (isRecord(o) && typeof o.tier === 'string') {
      opts.push({ tier: o.tier, label: typeof o.label === 'string' ? o.label : undefined })
    }
  }
  return opts
}

// Only assigns keys that were actually present on the wire, so merging this
// into the previous status (`{...prev, ...toAssistantStatus(raw)}`) matches
// the original untyped `{...prev, ...raw}` spread — a key absent from the
// response (e.g. POST /tier's narrower reply) must NOT clobber a field
// `prev` already had with `undefined`.
function toAssistantStatus(x: unknown): AssistantStatus | null {
  if (!isRecord(x)) return null
  const out: AssistantStatus = {}
  if (typeof x.tier === 'string') out.tier = x.tier
  if (typeof x.label === 'string') out.label = x.label
  const sovereignty = toAssistantSovereignty(x.sovereignty)
  if (sovereignty) out.sovereignty = sovereignty
  const tierOptions = toAssistantTierOptions(x.tier_options)
  if (tierOptions) out.tier_options = tierOptions
  if (typeof x.mail_source === 'string') out.mail_source = x.mail_source
  if (typeof x.semantic_index === 'boolean') out.semantic_index = x.semantic_index
  if (typeof x.files_enabled === 'boolean') out.files_enabled = x.files_enabled
  if (typeof x.reminders_enabled === 'boolean') out.reminders_enabled = x.reminders_enabled
  return out
}

interface SovereigntyBadgeProps {
  status: AssistantStatus | null
  onClick: () => void
}

function SovereigntyBadge({ status, onClick }: SovereigntyBadgeProps) {
  if (!status) return null
  const tier = status.tier || status.sovereignty?.tier || 'external'
  const info = tierInfo(tier)
  const label = status.label || info.label
  const model = [status.sovereignty?.provider, status.sovereignty?.model].filter(Boolean).join(' · ')
  // The sovereignty posture is the app's headline claim, so it reads as a real
  // pill (bordered, dot + label + model) rather than three loose spans of text
  // floating at the right edge of a 1600px header.
  return (
    <button
      type="button"
      onClick={onClick}
      title={status.sovereignty?.reason || info.blurb}
      className="group flex items-center gap-2 h-9 pl-2.5 pr-2 rounded-full border border-neutral-800 bg-neutral-900/60 hover:bg-neutral-800/70 hover:border-neutral-700 transition-colors focus-primary shrink-0"
    >
      <span className="inline-block w-2 h-2 rounded-full shrink-0" style={{ background: info.dot, boxShadow: `0 0 0 3px color-mix(in srgb, ${info.dot} 18%, transparent)` }} />
      <span className={`text-[12.5px] font-medium ${info.tone}`}>{label}</span>
      {model && <span className="mono text-[11.5px] text-neutral-600 hidden lg:inline truncate max-w-[14rem]">{model}</span>}
      <svg viewBox="0 0 20 20" fill="currentColor" width="12" height="12" className="text-neutral-600 group-hover:text-neutral-400 transition-colors shrink-0">
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

interface TierPickerProps {
  status: AssistantStatus | null
  options?: AssistantTierOption[]
  current?: string
  onPick: (tier: string) => void
  busy: boolean
  onClose: () => void
}

function TierPicker({ status, options, current, onPick, busy, onClose }: TierPickerProps) {
  const opts = options && options.length ? options : [
    { tier: 'local', label: TIERS.local.label },
    { tier: 'sovereign', label: TIERS.sovereign.label },
    { tier: 'brokered', label: TIERS.brokered.label },
  ]
  return (
    <div className="flex-shrink-0 border-b border-neutral-800/60 bg-neutral-900/40 animate-[fadeIn_0.16s_ease-out]">
      <div className={`${COLUMN} px-4 sm:px-6 py-4`}>
        <div className="flex items-center justify-between mb-3">
          <div>
            <div className="text-[13.5px] font-semibold text-neutral-100 tracking-tight">Where your AI runs</div>
            <div className="text-[12px] text-neutral-500 mt-0.5">Your box stays authoritative — the backend guard has the final say.</div>
          </div>
          <button type="button" onClick={onClose}
            className="text-[12.5px] font-medium px-3 h-8 rounded-lg border border-neutral-800 text-neutral-400 hover:text-neutral-100 hover:bg-neutral-800/60 hover:border-neutral-700 transition-colors focus-primary">Done</button>
        </div>
        {/* Side-by-side at desktop width: three stacked full-width rows across a
            1600px window were a wall of near-empty bars. */}
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
          {opts.map(o => {
            const info = tierInfo(o.tier)
            const active = current === o.tier
            return (
              <button
                key={o.tier}
                type="button"
                disabled={busy}
                aria-pressed={active}
                onClick={() => onPick(o.tier)}
                className={`text-left rounded-xl px-3.5 py-3 border transition-colors disabled:opacity-50 focus-primary ${
                  active
                    ? 'bg-neutral-800/80 border-neutral-600'
                    : 'bg-neutral-900/60 border-neutral-800 hover:border-neutral-700 hover:bg-neutral-900'
                }`}
              >
                <div className="flex items-center gap-2">
                  <span className="inline-block w-2 h-2 rounded-full shrink-0" style={{ background: info.dot }} />
                  <span className={`text-[12.5px] font-medium ${info.tone}`}>{o.label || info.label}</span>
                  {active && <span className="ml-auto mono text-[10.5px] uppercase tracking-[0.1em] text-neutral-500">current</span>}
                </div>
                <div className="text-[12px] text-neutral-500 mt-1.5 leading-relaxed">{info.blurb}</div>
              </button>
            )
          })}
        </div>
        {status?.sovereignty && !status.sovereignty.allowed && (
          <div className="text-[12px] text-warning mt-3 leading-relaxed">
            This tier needs the egress opt-in (VULOS_ASSISTANT_ALLOW_EXTERNAL=1) before mail is sent to it.
          </div>
        )}
      </div>
    </div>
  )
}

// ── Measure ──────────────────────────────────────────────────────────────────
// THE fix for "ugly on desktop". Every band of the app (header, tier picker,
// transcript, composer) renders its content inside this one centred column, so
// a maximized 1600px window shows a readable ~48rem conversation instead of
// 1500px-wide lines of text with the send button a screen away from the words.
// The bands themselves still span the window, so borders/backgrounds read as
// full-width chrome.
const COLUMN = 'mx-auto w-full max-w-[48rem]'

// ── Quick actions ────────────────────────────────────────────────────────────

interface QuickAction {
  id: string
  label: string
  /** One-line "what this actually does", shown on the empty-state cards. */
  hint: string
  prompt: null
}

const QUICK: QuickAction[] = [
  { id: 'attention', label: 'What needs my attention', hint: 'Today’s unanswered threads, deadlines and asks', prompt: null },
  { id: 'summarize', label: 'Summarize my inbox', hint: 'A short readout of everything currently unread', prompt: null },
]

// ── Message bubble ───────────────────────────────────────────────────────────

interface BubbleProps {
  role: 'user' | 'assistant'
  content?: string
  pending?: boolean
  error?: boolean
  onRetry?: () => void
}

// Inside the 48rem COLUMN this is a comfortable ~38rem measure; the old
// `max-w-[80%]` was 80% of the WINDOW, i.e. ~1280px lines at desktop width.
const BUBBLE_MEASURE = 'max-w-[92%] sm:max-w-[80%]'

function Bubble({ role, content, pending, error, onRetry }: BubbleProps) {
  const isUser = role === 'user'
  return (
    <div className={`flex min-w-0 ${isUser ? 'justify-end' : 'justify-start'}`}>
      <div
        style={isUser ? { background: 'var(--accent)' } : undefined}
        className={`${BUBBLE_MEASURE} min-w-0 rounded-2xl px-4 py-3 text-[13.5px] leading-[1.65] whitespace-pre-wrap break-words ${
          isUser
            ? 'text-white rounded-br-md shadow-[0_1px_2px_rgba(0,0,0,0.18)]'
            : error
              ? 'bg-danger-soft border border-danger-soft text-danger rounded-bl-md'
              : 'bg-neutral-900/70 border border-neutral-800 text-neutral-200 rounded-bl-md'
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

// core/agentStream.ts's AgentStep declares `args?: Record<string, unknown>` and
// `result?: unknown`, but the wire value is always a pre-formatted STRING —
// ToolStep.Args/Result in backend/services/assistant/agent.go are `string`
// (compactArgs/truncate build them server-side), so agentStream.ts's
// `isRecord(x.args)` narrowing check can never match a real response and
// silently drops it to `undefined`. That mistyping is in an already-typed
// file out of scope for this pass; this narrows honestly at the boundary
// instead of casting, and renders exactly what StepTrace expects (see
// StepTraceItem in ./ProposalCard.tsx).
function toStepTraceItem(s: AgentStep): StepTraceItem {
  return {
    tool: s.tool,
    content: s.content,
    args: typeof s.args === 'string' ? s.args : undefined,
    result: typeof s.result === 'string' ? s.result : undefined,
  }
}

// ── Panel ────────────────────────────────────────────────────────────────────

// One transcript entry. `proposal` + `state` render the ProposalCard gate;
// `steps` renders the read-only tool trace (see ./ProposalCard.tsx).
interface Message {
  id: string
  role: 'user' | 'assistant'
  content: string
  pending?: boolean
  error?: boolean
  proposal?: AgentProposal
  state?: ProposalState
  steps?: StepTraceItem[]
}

export default function Assistant() {
  const [status, setStatus] = useState<AssistantStatus | null>(null)
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [tierBusy, setTierBusy] = useState(false)
  const scrollRef = useRef<HTMLDivElement>(null)
  const inputRef = useAutoGrow(input, { maxHeight: 128 })
  // Aborts the in-flight streaming turn when the window is closed mid-stream.
  // Without this the SSE fetch + its reader keep running after unmount and the
  // token/status callbacks call setState on a gone component (React warning +
  // a leaked reader holding the response buffer). Cleared on turn completion.
  const streamCtl = useRef<AbortController | null>(null)

  useEffect(() => {
    fetch('/api/assistant/status', { credentials: 'include' })
      .then(r => (r.ok ? r.json() : null))
      .then((raw: unknown) => setStatus(toAssistantStatus(raw)))
      .catch(() => {})
  }, [])

  // Abort any in-flight streaming turn on unmount (window close / view switch),
  // so the stream is torn down instead of leaking and writing to dead state.
  useEffect(() => () => { streamCtl.current?.abort() }, [])

  // Esc closes the tier picker (matches the shell's overlay dismissal contract).
  useEffect(() => {
    if (!pickerOpen) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.stopPropagation(); setPickerOpen(false) } }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [pickerOpen])

  // Operator picks the sovereignty tier. The backend Guard remains
  // authoritative; the returned status carries the honest resulting tier.
  const pickTier = useCallback(async (tier: string) => {
    setTierBusy(true)
    try {
      const res = await fetch('/api/assistant/tier', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tier }),
      })
      if (res.ok) {
        const raw: unknown = await res.json().catch(() => null)
        const data = toAssistantStatus(raw)
        if (data) setStatus(s => ({ ...(s || {}), ...data }))
      }
    } catch { /* leave status as-is */ } finally {
      setTierBusy(false)
    }
  }, [])

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' })
  }, [messages])

  const push = useCallback((role: Message['role'], content: string) => {
    setMessages(m => [...m, { role, content, id: Math.random().toString(36).slice(2) }])
  }, [])

  const patchLast = useCallback((content: string, pending: boolean, error = false) => {
    setMessages(m => {
      const copy = m.slice()
      const last = copy[copy.length - 1]
      if (last && last.role === 'assistant') copy[copy.length - 1] = { ...last, content, pending, error }
      return copy
    })
  }, [])

  const patchById = useCallback((id: string, patch: Partial<Message>) => {
    setMessages(m => m.map(msg => (msg.id === id ? { ...msg, ...patch } : msg)))
  }, [])

  // JSON skills (attention / summarize / search) — single-shot answers.
  const runSkill = useCallback(async (path: string, body?: Record<string, unknown>, userLabel?: string) => {
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
      const raw: unknown = await res.json().catch(() => ({}))
      const data = isRecord(raw) ? raw : {}
      if (!res.ok) {
        const errMsg = typeof data.error === 'string' ? data.error : `Request failed (${res.status})`
        patchLast(errMsg, false, true)
        return
      }
      const answer = typeof data.answer === 'string' ? data.answer
        : typeof data.draft === 'string' ? data.draft
          : JSON.stringify(data)
      patchLast(answer, false)
    } catch {
      patchLast('Could not reach the assistant. Is the box online?', false, true)
    } finally {
      setBusy(false)
    }
  }, [busy, push, patchLast])

  // Approve a proposal → POST it to /api/assistant/execute (the second half of
  // the confirmation round-trip). Only here does a mutating action actually run.
  const approveProposal = useCallback(async (msgId: string, proposal: AgentProposal) => {
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
      const raw: unknown = await res.json().catch(() => ({}))
      const data = isRecord(raw) ? raw : {}
      if (!res.ok) {
        patchById(msgId, { state: 'pending' })
        const errMsg = typeof data.error === 'string' ? data.error : `Could not complete the action (${res.status}).`
        push('assistant', errMsg)
        return
      }
      patchById(msgId, { state: 'done' })
      if (typeof data.result === 'string') push('assistant', data.result)
    } catch {
      patchById(msgId, { state: 'pending' })
      push('assistant', 'Could not reach the assistant to run the action.')
    }
  }, [patchById, push])

  const rejectProposal = useCallback((msgId: string) => {
    patchById(msgId, { state: 'rejected' })
  }, [patchById])

  // Freeform chat — the TOOL-USING agent turn, STREAMED (Wave 17). The model may
  // call read-only tools (which run on the box; each surfaces a live "using …"
  // status) and either STREAMS a final answer token-by-token or returns a
  // PROPOSAL for a mutating action, which we render with Approve/Reject. History
  // is the prior user/assistant text turns (proposals excluded). runAgentTurn
  // falls back to the non-streaming /agent if streaming can't be established.
  const sendChat = useCallback(async (text: string) => {
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
    const liveSteps: StepTraceItem[] = []
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
        onStep: (step) => { liveSteps.push(toStepTraceItem(step)) },
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
      const finalSteps = (result.steps && result.steps.length) ? result.steps.map(toStepTraceItem) : liveSteps
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
      const isAbort = isRecord(err) && err.name === 'AbortError'
      if (!isAbort) patchLast('Could not reach the assistant.', false, true)
    } finally {
      if (streamCtl.current === ctl) streamCtl.current = null
      // Skip the state update if this turn was aborted (component unmounted).
      if (!ctl.signal.aborted) setBusy(false)
    }
  }, [busy, messages, push, patchLast])

  // The composer <form>'s onSubmit and the textarea's Enter-key onKeyDown call
  // submit with their own (different) event types — this is the minimal shape
  // both React.FormEvent and React.KeyboardEvent satisfy.
  const submit = (e?: { preventDefault: () => void }) => {
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
      const trailingUser = copy.length && copy[copy.length - 1].role === 'user' ? copy.pop() : undefined
      if (trailingUser) text = trailingUser.content
      return copy
    })
    if (text) setTimeout(() => sendChat(text), 0)
  }, [busy, sendChat])

  const onQuick = (q: QuickAction) => {
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
      {/* Header — a full-width band whose CONTENT is held in the shared column,
          so the badge sits beside the title instead of 1400px away from it. */}
      <div className="flex-shrink-0 border-b border-neutral-800/60 bg-neutral-950/70">
        <div className={`${COLUMN} px-4 sm:px-6 py-3 flex items-center justify-between gap-3`}>
          <div className="flex items-center gap-3 min-w-0">
            <span className="w-9 h-9 rounded-xl flex items-center justify-center accent-bg-soft accent-text text-[17px] leading-none flex-shrink-0 ring-1 ring-inset ring-white/5" aria-hidden="true">✦</span>
            <div className="min-w-0">
              <h1 className="text-[14.5px] font-semibold text-neutral-100 leading-tight tracking-tight">Assistant</h1>
              <div className="text-[12px] text-neutral-500 leading-tight truncate">
                {/* Only surface the mail source when it's an EXTERNAL provider the
                    user connected (e.g. "Gmail"); the built-in mail engine's
                    internal id is not a user-facing brand. */}
                Private AI over your mail{status?.mail_source && status.mail_source !== 'lilmail' ? ` · ${status.mail_source}` : ''}
              </div>
            </div>
          </div>
          <SovereigntyBadge status={status} onClick={() => setPickerOpen(o => !o)} />
        </div>
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
        <div className="flex-shrink-0 bg-danger-soft border-b border-danger-soft">
          <div className={`${COLUMN} px-4 sm:px-6 py-2.5 flex items-start gap-2.5 text-[12.5px] text-danger leading-relaxed`}>
            <svg viewBox="0 0 16 16" className="w-4 h-4 shrink-0 mt-px" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
              <path d="M8 1.75l6.25 11.5H1.75L8 1.75z" strokeLinejoin="round" /><path d="M8 6.5v3.25M8 11.6v.1" strokeLinecap="round" />
            </svg>
            <span>
              This endpoint's tier ({tierInfo(currentTier).label}) is not permitted, so your mail stays inside the
              sovereignty boundary. Pick a local or sovereign endpoint, or set VULOS_ASSISTANT_ALLOW_EXTERNAL=1 to
              authorize a brokered/external one.
            </span>
          </div>
        </div>
      )}

      {/* Conversation */}
      <div ref={scrollRef} role="log" aria-live="polite" aria-atomic="false" aria-busy={busy}
        className="flex-1 overflow-y-auto overscroll-contain">
        <div className={`${COLUMN} px-4 sm:px-6 py-5 space-y-4 ${messages.length === 0 ? 'h-full' : ''}`}>
        {messages.length === 0 && (
          <div className="h-full flex flex-col items-center justify-center text-center select-none py-8">
            <div
              className="va-float w-16 h-16 rounded-2xl flex items-center justify-center text-[27px] leading-none accent-bg-soft accent-text ring-1 ring-[var(--border-default)]"
              aria-hidden="true"
            >
              ✦
            </div>
            <h2 className="mt-6 text-[20px] sm:text-[23px] font-semibold tracking-[-0.02em] text-neutral-100 text-balance">
              An assistant that knows your day
            </h2>
            {/* GUARDED COPY: "stays on your own server" is the app's honest
                sovereignty claim and is asserted by
                src/__tests__/integration/Assistant.integration.test.tsx. Keep
                the phrase intact when rewording around it. */}
            <p className="mt-2.5 text-neutral-400 text-[13.5px] max-w-[34rem] leading-relaxed text-balance">
              Ask about your mail. Everything you type and every message it reads stays
              on your own server — and it never acts without your OK.
            </p>
            {/* Two columns at ≥sm. The old single 20rem stack left an enormous
                void either side of it on a maximized desktop window. */}
            <div className="mt-7 grid grid-cols-1 sm:grid-cols-2 gap-2.5 w-full max-w-[34rem] text-left">
              {QUICK.map(q => (
                <button
                  key={q.id}
                  onClick={() => onQuick(q)}
                  disabled={busy}
                  className="group flex flex-col gap-1 text-left px-4 py-3.5 rounded-xl bg-neutral-900/70 border border-neutral-800 hover:border-neutral-700 hover:bg-neutral-900 transition-colors disabled:opacity-50 focus-primary"
                >
                  <span className="flex items-center gap-2 text-[13px] font-medium text-neutral-200 group-hover:text-neutral-50 transition-colors">
                    <span className="accent-text opacity-60 group-hover:opacity-100 transition-opacity" aria-hidden="true">→</span>
                    <span className="min-w-0">{q.label}</span>
                  </span>
                  <span className="text-[12px] text-neutral-500 leading-snug pl-[1.4rem]">{q.hint}</span>
                </button>
              ))}
            </div>
            <p className="mt-6 flex items-center gap-1.5 text-[11.5px] text-neutral-600">
              <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true">
                <rect x="3.25" y="7" width="9.5" height="6.25" rx="1.6" /><path d="M5.5 7V5.25a2.5 2.5 0 0 1 5 0V7" />
              </svg>
              No mail leaves your instance
            </p>
          </div>
        )}
        {messages.map((m, i) => {
          const isLast = i === messages.length - 1
          const proposal = m.proposal
          return proposal ? (
            <div key={m.id} className="flex justify-start flex-col gap-2 items-start min-w-0 w-full">
              {m.content && <Bubble role="assistant" content={m.content} />}
              <StepTrace steps={m.steps} className={BUBBLE_MEASURE} />
              <div className={`${BUBBLE_MEASURE} w-full`}>
                <ProposalCard
                  proposal={proposal}
                  state={m.state}
                  onApprove={() => approveProposal(m.id, proposal)}
                  onReject={() => rejectProposal(m.id)}
                />
              </div>
            </div>
          ) : m.steps ? (
            <div key={m.id} className="flex justify-start flex-col gap-1.5 items-start min-w-0 w-full">
              <Bubble role={m.role} content={m.content} pending={m.pending} error={m.error}
                onRetry={m.error && isLast ? retryLast : undefined} />
              <StepTrace steps={m.steps} className={BUBBLE_MEASURE} />
            </div>
          ) : (
            <Bubble key={m.id} role={m.role} content={m.content} pending={m.pending} error={m.error}
              onRetry={m.error && isLast ? retryLast : undefined} />
          )
        })}
        </div>
      </div>

      {/* Composer */}
      <form onSubmit={submit} className="flex-shrink-0 border-t border-neutral-800/60 bg-neutral-950/60">
        <div className={`${COLUMN} px-4 sm:px-6 py-3.5`}>
          {messages.length > 0 && (
            <div className="flex gap-1.5 mb-2.5 flex-wrap">
              {QUICK.map(q => (
                <button
                  key={q.id}
                  type="button"
                  onClick={() => onQuick(q)}
                  disabled={busy}
                  className="text-[12px] px-3 h-7 rounded-full bg-neutral-900 border border-neutral-800 text-neutral-400 hover:text-neutral-200 hover:border-neutral-700 hover:bg-neutral-800/60 transition-colors disabled:opacity-40 focus-primary"
                >
                  {q.label}
                </button>
              ))}
            </div>
          )}
          {/* The send button lives INSIDE the field. It used to be flush against
              the right edge of the window — at 1600px that put it a full screen
              away from the caret you were typing at. */}
          <div className="flex items-end gap-2 rounded-2xl bg-neutral-900 border border-neutral-800 pr-2 py-1.5 transition-colors focus-within:border-[color-mix(in_srgb,var(--accent)_55%,var(--border-emphasis))]">
            <textarea
              ref={inputRef}
              rows={1}
              value={input}
              onChange={e => setInput(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) submit(e) }}
              placeholder="Ask about your mail…"
              aria-label="Ask about your mail"
              className="flex-1 min-w-0 resize-none bg-transparent px-3.5 py-2 text-[13.5px] leading-relaxed text-neutral-100 placeholder-neutral-600 focus:outline-none"
            />
            <button
              type="submit"
              disabled={busy || !input.trim()}
              style={{ background: 'var(--accent)' }}
              className="flex-shrink-0 w-9 h-9 rounded-xl text-white flex items-center justify-center transition-[filter,opacity] hover:brightness-110 disabled:opacity-30 disabled:hover:brightness-100 focus-primary"
              aria-label="Send"
              title="Send (Enter)"
            >
              {busy
                ? <span className="w-4 h-4 spinner border-white/40 border-t-white" />
                : (
                  <svg viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
                    <path d="M10.894 2.553a1 1 0 00-1.788 0l-7 14a1 1 0 001.169 1.409l5-1.429A1 1 0 009 15.571V11a1 1 0 112 0v4.571a1 1 0 00.725.962l5 1.428a1 1 0 001.17-1.408l-7-14z" />
                  </svg>
                )}
            </button>
          </div>
          <div className="mt-2 flex items-center justify-between gap-3 text-[11.5px] text-neutral-600">
            <span className="hidden sm:inline">
              <kbd className="mono">Enter</kbd> to send · <kbd className="mono">Shift</kbd>+<kbd className="mono">Enter</kbd> for a new line
            </span>
            <span className="ml-auto truncate">Runs on your box · nothing is sent to a third party</span>
          </div>
        </div>
      </form>
    </div>
  )
}
