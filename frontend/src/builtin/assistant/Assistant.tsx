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
 *
 * ── LAYOUT NOTE (read before changing any width/visibility class) ────────────
 * This component renders at TWO very different sizes from the SAME markup:
 *
 *   • a window (the `assistant` builtin) — up to ~1600px wide, and
 *   • the shell slide-over (shell/AssistantPanel.tsx) — min(420px, 42vw).
 *
 * Tailwind's `sm:` / `lg:` breakpoints key off the VIEWPORT, so inside the
 * 420px panel on a 1440px desktop every one of them is ON — which is exactly
 * how the composer's footer hints ended up wrapping and truncating in the
 * panel while looking fine in the window. Every size-dependent rule here is
 * therefore a CONTAINER query (`@container` on the root + `@xl:` variants),
 * which measures the surface the component actually occupies.
 */
import { useState, useEffect, useRef, useCallback, type ReactNode } from 'react'
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

// ── Measure ──────────────────────────────────────────────────────────────────
// Every band of the app (header, tier picker, transcript, composer) renders its
// content inside this one centred column, so a maximized 1600px window shows a
// readable ~44rem conversation instead of 1500px-wide lines of text with the
// send button a screen away from the words. The bands themselves still span the
// window, so borders/backgrounds read as full-width chrome.
const COLUMN = 'mx-auto w-full max-w-[44rem]'

// Width reserved on the right of every full-width band for the instance rail,
// which only exists once the SURFACE is at least 64rem wide (container query —
// the 420px slide-over never shows it, however wide the desktop behind it is).
const RAIL_GUTTER = '@5xl:pr-[16rem]'

// ── Sovereignty badge ────────────────────────────────────────────────────────

interface SovereigntyBadgeProps {
  status: AssistantStatus | null
  open: boolean
  onClick: () => void
}

function SovereigntyBadge({ status, open, onClick }: SovereigntyBadgeProps) {
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
      aria-expanded={open}
      title={status.sovereignty?.reason || info.blurb}
      className="group flex items-center gap-2 h-8 pl-2.5 pr-1.5 rounded-full border transition-colors focus-primary shrink-0 border-[var(--border-default)] bg-[var(--bg-base)] hover:border-[var(--border-strong)] hover:bg-[var(--bg-hover)]"
    >
      <span className="inline-block w-[7px] h-[7px] rounded-full shrink-0" style={{ background: info.dot, boxShadow: `0 0 0 3px color-mix(in srgb, ${info.dot} 18%, transparent)` }} />
      <span className={`text-[12px] font-medium ${info.tone}`}>{label}</span>
      {/* The model id is detail, not headline — only surfaced when the surface
          is actually wide enough for it (container, not viewport: the 420px
          slide-over is not "wide" just because the desktop behind it is). */}
      {model && <span className="mono text-[11px] text-neutral-600 hidden @2xl:inline truncate max-w-[12rem]">{model}</span>}
      <svg viewBox="0 0 20 20" fill="currentColor" width="12" height="12" aria-hidden="true"
        className={`text-neutral-600 group-hover:text-neutral-400 transition-transform shrink-0 ${open ? 'rotate-180' : ''}`}>
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
    <div className="flex-shrink-0 border-b border-[var(--border-default)] bg-[var(--bg-surface)] animate-[fadeIn_0.16s_ease-out]">
      <div className={`${COLUMN} px-4 @xl:px-6 py-4`}>
        <div className="flex items-start justify-between gap-3 mb-3">
          <div className="min-w-0">
            <div className="text-[13px] font-semibold text-neutral-100 tracking-tight">Where your AI runs</div>
            <div className="text-[12px] text-neutral-500 mt-0.5 leading-snug">Your box stays authoritative — the backend guard has the final say.</div>
          </div>
          <button type="button" onClick={onClose}
            className="shrink-0 text-[12px] font-medium px-3 h-8 rounded-lg border border-[var(--border-default)] text-neutral-400 hover:text-neutral-100 hover:bg-[var(--bg-hover)] transition-colors focus-primary">Done</button>
        </div>
        {/* Side-by-side once the SURFACE is wide (container query): three stacked
            full-width rows across a 1600px window were a wall of near-empty
            bars, and three columns inside the 420px panel were unreadable. */}
        <div className="grid grid-cols-1 @xl:grid-cols-3 gap-2">
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
                    ? 'bg-[var(--bg-selected)] border-[var(--bg-selected-border)]'
                    : 'bg-[var(--bg-base)] border-[var(--border-default)] hover:border-[var(--border-strong)] hover:bg-[var(--bg-hover)]'
                }`}
              >
                <div className="flex items-center gap-2">
                  <span className="inline-block w-[7px] h-[7px] rounded-full shrink-0" style={{ background: info.dot }} />
                  <span className={`text-[12.5px] font-medium ${info.tone}`}>{o.label || info.label}</span>
                  {active && <span className="ml-auto mono text-[10px] uppercase tracking-[0.1em] text-neutral-500">current</span>}
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

// ── Instance rail ────────────────────────────────────────────────────────────
//
// THE answer to "a maximized window is 1600px of chat with a paragraph in it".
// A conversation does not want to be 1600px wide, so the leftover width is not
// a reading problem to solve with more measure — it is space the app should
// spend on something. It spends it on the one thing this assistant has that a
// hosted one does not: a standing, honest account of WHERE it runs, WHAT it can
// reach, and the fact that it stops before it acts.
//
// Every value here comes off GET /api/assistant/status. Nothing is asserted
// that the backend did not report: a capability whose flag is absent from the
// payload is not rendered at all, rather than rendered as "off".

function RailHeading({ children }: { children: ReactNode }) {
  return <div className="mono text-[10px] uppercase tracking-[0.14em] text-neutral-600">{children}</div>
}

function RailFact({ label, value, mono }: { label: string; value: ReactNode; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <div className="text-[11px] text-neutral-500 leading-tight">{label}</div>
      <div className={`text-[12px] text-neutral-300 leading-snug break-words ${mono ? 'mono' : ''}`}>{value}</div>
    </div>
  )
}

function RailCapability({ label, on }: { label: string; on: boolean }) {
  return (
    <div className="flex items-center gap-2 text-[12px]">
      <span className={`shrink-0 w-3.5 h-3.5 rounded-full flex items-center justify-center ${on ? 'bg-success-soft text-success' : 'text-neutral-600'}`}>
        {on ? (
          <svg viewBox="0 0 20 20" fill="currentColor" width="9" height="9" aria-hidden="true">
            <path fillRule="evenodd" d="M16.7 5.3a1 1 0 010 1.4l-7.5 7.5a1 1 0 01-1.4 0l-3.5-3.5a1 1 0 111.4-1.4l2.8 2.79 6.8-6.79a1 1 0 011.4 0z" clipRule="evenodd" />
          </svg>
        ) : (
          <svg viewBox="0 0 20 20" fill="currentColor" width="9" height="9" aria-hidden="true"><rect x="4" y="9" width="12" height="2" rx="1" /></svg>
        )}
      </span>
      <span className={on ? 'text-neutral-300' : 'text-neutral-600'}>{label}</span>
      <span className="ml-auto mono text-[10px] uppercase tracking-[0.08em] text-neutral-600">{on ? 'on' : 'off'}</span>
    </div>
  )
}

function InstanceRail({ status, onChangeTier }: { status: AssistantStatus | null; onChangeTier: () => void }) {
  // No status means the box never answered. An empty rail of headings with
  // nothing under them is worse than no rail, and inventing values would be a
  // lie about the very thing this panel exists to be honest about.
  if (!status) return null
  const tier = status.tier || status.sovereignty?.tier || 'external'
  const info = tierInfo(tier)
  const model = [status.sovereignty?.provider, status.sovereignty?.model].filter(Boolean).join(' · ')
  const endpoint = status.sovereignty?.endpoint
  const caps: Array<[string, boolean]> = []
  if (typeof status.semantic_index === 'boolean') caps.push(['Semantic search', status.semantic_index])
  if (typeof status.files_enabled === 'boolean') caps.push(['Your files', status.files_enabled])
  if (typeof status.reminders_enabled === 'boolean') caps.push(['Reminders', status.reminders_enabled])

  return (
    <aside
      aria-label="This instance"
      className="hidden @5xl:flex w-[16rem] shrink-0 flex-col gap-6 overflow-y-auto border-l border-[var(--border-default)] bg-[var(--bg-surface)] px-5 py-6"
    >
      <section className="flex flex-col gap-2.5">
        <RailHeading>Where it runs</RailHeading>
        <div className="flex items-center gap-2">
          <span className="inline-block w-2 h-2 rounded-full shrink-0" style={{ background: info.dot, boxShadow: `0 0 0 3px color-mix(in srgb, ${info.dot} 16%, transparent)` }} />
          <span className={`text-[13px] font-medium ${info.tone}`}>{status.label || info.label}</span>
        </div>
        <p className="text-[12px] text-neutral-500 leading-relaxed">{status.sovereignty?.reason || info.blurb}</p>
        {model && <RailFact label="Model" value={model} mono />}
        {endpoint && <RailFact label="Endpoint" value={endpoint} mono />}
        <button
          type="button"
          onClick={onChangeTier}
          className="self-start text-[12px] font-medium accent-text hover:underline underline-offset-2 focus-primary rounded"
        >
          Change where it runs
        </button>
      </section>

      {(status.mail_source || caps.length > 0) && (
        <section className="flex flex-col gap-2.5">
          <RailHeading>What it can reach</RailHeading>
          {status.mail_source && (
            <RailFact
              label="Mail"
              // The built-in engine's internal id is not a user-facing brand;
              // only a provider the user actually connected gets named.
              value={status.mail_source === 'lilmail' ? 'Your mailbox, on this box' : status.mail_source}
            />
          )}
          {caps.length > 0 && (
            <div className="flex flex-col gap-1.5 pt-0.5">
              {caps.map(([label, on]) => <RailCapability key={label} label={label} on={on} />)}
            </div>
          )}
        </section>
      )}

      <section className="mt-auto flex flex-col gap-2 border-t border-[var(--border-subtle)] pt-4">
        <RailHeading>Before it acts</RailHeading>
        <p className="text-[12px] text-neutral-500 leading-relaxed">
          Reading happens on its own. Anything that sends, changes or creates stops here and waits for
          you to approve it.
        </p>
      </section>
    </aside>
  )
}

// ── Quick actions ────────────────────────────────────────────────────────────

interface QuickAction {
  id: string
  label: string
  /** One-line "what this actually does", shown on the empty-state cards. */
  hint: string
}

const QUICK: QuickAction[] = [
  { id: 'attention', label: 'What needs my attention', hint: 'Today’s unanswered threads, deadlines and asks' },
  { id: 'summarize', label: 'Summarize my inbox', hint: 'A short readout of everything currently unread' },
]

// ── Turns ────────────────────────────────────────────────────────────────────
//
// The user speaks in a bubble; the assistant answers in PROSE beside a small
// mark. Two facing grey bubbles read as a toy chat and waste the measure — an
// answer that may run to a paragraph, carry a tool trace and then a proposal
// card wants to be a block of text in the reading column, not a balloon.

function UserTurn({ content }: { content: string }) {
  return (
    <div className="flex justify-end min-w-0">
      <div
        style={{ background: 'var(--accent)' }}
        className="max-w-[85%] min-w-0 rounded-2xl rounded-br-md px-3.5 py-2.5 text-[13.5px] leading-[1.6] text-white whitespace-pre-wrap break-words shadow-[0_1px_2px_rgba(0,0,0,0.18)]"
      >
        {content}
      </div>
    </div>
  )
}

interface AssistantTurnProps {
  content?: string
  pending?: boolean
  error?: boolean
  onRetry?: () => void
  steps?: StepTraceItem[]
  children?: ReactNode
}

function AssistantTurn({ content, pending, error, onRetry, steps, children }: AssistantTurnProps) {
  return (
    <div className="flex gap-3 min-w-0">
      <span
        aria-hidden="true"
        className="mt-[3px] w-6 h-6 rounded-lg accent-bg-soft accent-text flex items-center justify-center text-[12px] leading-none shrink-0"
      >
        ✦
      </span>
      <div className="min-w-0 flex-1 flex flex-col items-start gap-2.5">
        {error ? (
          <div className="w-full rounded-xl border border-danger-soft bg-danger-soft px-3.5 py-2.5 text-[13px] leading-relaxed text-danger">
            <span aria-hidden="true" className="mr-1.5">⚠</span>
            {content}
            {onRetry && (
              <button
                type="button"
                onClick={onRetry}
                className="block mt-1.5 text-[12px] font-medium text-danger underline decoration-danger/40 underline-offset-2 hover:decoration-danger transition-colors focus-primary rounded"
              >
                Retry
              </button>
            )}
          </div>
        ) : content ? (
          /* ~78 characters at this size. The COLUMN is sized for the composer
             and the header, which want to be wider than a line of prose. */
          <div className="w-full max-w-[33rem] text-[13.5px] @xl:text-[14px] leading-[1.75] text-neutral-200 whitespace-pre-wrap break-words">
            {content}
            {pending && (
              <span
                className="va-caret inline-block w-[3px] h-[1.05em] ml-0.5 -mb-[0.12em] rounded-full align-baseline"
                style={{ background: 'var(--accent)' }}
                aria-hidden="true"
              />
            )}
          </div>
        ) : pending ? (
          <span className="inline-flex items-center gap-2 text-[13px] text-neutral-500 py-0.5">
            <span className="va-dots" aria-hidden="true"><i /><i /><i /></span>
            Thinking…
          </span>
        ) : null}
        <StepTrace steps={steps} className="w-full" />
        {children}
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
  // Quick actions already run this session. A follow-up chip offering to do the
  // thing you just did is noise, so each one retires once used.
  const [usedQuick, setUsedQuick] = useState<string[]>([])
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

  const onQuick = useCallback((q: QuickAction) => {
    setUsedQuick(u => (u.includes(q.id) ? u : [...u, q.id]))
    if (q.id === 'attention') runSkill('/api/assistant/attention', {}, 'What needs my attention today?')
    else if (q.id === 'summarize') runSkill('/api/assistant/summarize', { scope: 'inbox' }, 'Summarize my inbox')
  }, [runSkill])

  const newChat = useCallback(() => {
    setMessages([])
    setUsedQuick([])
    setInput('')
  }, [])

  const currentTier = status?.tier || status?.sovereignty?.tier
  const blocked = status?.sovereignty && status.sovereignty.allowed === false
  const empty = messages.length === 0
  const last = messages[messages.length - 1]
  // Follow-ups live at the END of the transcript, not in a loose chip band above
  // the composer: they are part of the conversation ("here is what I could do
  // next"), and putting them there also lets the composer be a single object.
  const followUps = QUICK.filter(q => !usedQuick.includes(q.id))
  const showFollowUps = !empty && !busy && followUps.length > 0
    && last?.role === 'assistant' && !last.error && !last.pending

  return (
    <div className="@container flex flex-col h-full bg-[var(--bg-base)] text-neutral-200 overflow-hidden">
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
        @keyframes vaRise { from { opacity: 0; transform: translateY(6px) } to { opacity: 1; transform: none } }
        .va-rise { animation: vaRise var(--motion-base, .22s) var(--ease-out, ease-out) both; }
        @media (prefers-reduced-motion: reduce) {
          .va-caret, .va-dots i, .va-float, .va-rise { animation: none !important }
        }
      `}</style>

      {/* Header — a full-width band whose CONTENT is held in the shared column,
          so the badge sits beside the title instead of 1400px away from it. */}
      {/* `@5xl:pr-[16rem]` on every full-width band reserves exactly the rail's
          width, so the band's inner COLUMN centres over the SAME box the
          conversation column centres over. Without it the header's title would
          sit 128px left of the answer beneath it. */}
      <header className={`flex-shrink-0 border-b border-[var(--border-default)] bg-[var(--bg-surface)] ${RAIL_GUTTER}`}>
        <div className={`${COLUMN} px-4 @xl:px-6 py-2.5 flex items-center gap-3`}>
          <span className="w-8 h-8 rounded-[10px] flex items-center justify-center accent-bg-soft accent-text text-[15px] leading-none flex-shrink-0" aria-hidden="true">✦</span>
          <div className="min-w-0">
            <h1 className="text-[14px] font-semibold text-neutral-100 leading-tight tracking-[-0.01em]">Assistant</h1>
            <div className="text-[11.5px] text-neutral-500 leading-tight truncate">
              {/* Only surface the mail source when it's an EXTERNAL provider the
                  user connected (e.g. "Gmail"); the built-in mail engine's
                  internal id is not a user-facing brand. */}
              Private AI over your mail{status?.mail_source && status.mail_source !== 'lilmail' ? ` · ${status.mail_source}` : ''}
            </div>
          </div>
          <div className="ml-auto flex items-center gap-1.5 shrink-0">
            {!empty && (
              <button
                type="button"
                onClick={newChat}
                title="Start a new conversation"
                aria-label="New conversation"
                className="h-8 px-2.5 rounded-full border border-[var(--border-default)] text-neutral-500 hover:text-neutral-100 hover:bg-[var(--bg-hover)] hover:border-[var(--border-strong)] transition-colors focus-primary flex items-center gap-1.5 text-[12px] font-medium"
              >
                <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true">
                  <path d="M8 3.25v9.5M3.25 8h9.5" />
                </svg>
                <span className="hidden @xl:inline">New</span>
              </button>
            )}
            <SovereigntyBadge status={status} open={pickerOpen} onClick={() => setPickerOpen(o => !o)} />
          </div>
        </div>
      </header>

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
        <div className={`flex-shrink-0 bg-danger-soft border-b border-danger-soft ${RAIL_GUTTER}`}>
          <div className={`${COLUMN} px-4 @xl:px-6 py-2.5 flex items-start gap-2.5 text-[12.5px] text-danger leading-relaxed`}>
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

      {/* Body: the conversation column, and — once the surface is wide enough
          to have width to spare — the instance rail beside it, running the
          full height from the header to the foot of the window. */}
      <div className="flex-1 min-h-0 flex">
      <div className="flex-1 min-w-0 flex flex-col">
      {/* Conversation.
          TOP-anchored, deliberately. The previous pass bottom-anchored short
          transcripts (`min-h-full` + `justify-end`) to close the gap above the
          composer; on a maximized 1600x1000 window that traded ~700px of dead
          canvas BELOW the answer for ~700px of dead canvas ABOVE it, which
          reads as a broken page rather than an unfinished one. A conversation
          grows downward from where it started — and the tail of it is now the
          follow-up row, so the space under the last answer is offered back to
          the user instead of left blank. */}
      <div ref={scrollRef} role="log" aria-live="polite" aria-atomic="false" aria-busy={busy}
        className="flex-1 min-h-0 overflow-y-auto overscroll-contain">
        <div className={`${COLUMN} px-4 @xl:px-6 min-h-full ${empty ? '' : 'py-6 flex flex-col gap-5'}`}>
          {empty ? (
            /* `justify-center-safe`, not `justify-center`: on a short surface
               (a phone window, or any surface where the tier picker's banner
               also pushes the composer up) this block is taller than the
               space left for it. Plain `center` clips the OVERFLOW evenly on
               both ends with no way to scroll to the top half — scrollTop
               can't go negative — so the heading and icon became permanently
               unreachable, not merely off-screen. `safe center` falls back to
               start-alignment exactly when centering would clip, which keeps
               every bit of the empty state reachable by scrolling down from
               the top instead. */
            <div className="min-h-full flex flex-col items-center justify-center-safe text-center select-none py-8 pb-14">
              <div
                className="va-float w-14 h-14 shrink-0 rounded-2xl flex items-center justify-center text-[24px] leading-none accent-bg-soft accent-text"
                aria-hidden="true"
              >
                ✦
              </div>
              <h2 className="mt-5 text-[19px] @xl:text-[22px] font-semibold tracking-[-0.02em] text-neutral-100 text-balance">
                An assistant that knows your day
              </h2>
              {/* GUARDED COPY: "stays on your own server" is the app's honest
                  sovereignty claim and is asserted by
                  src/__tests__/integration/Assistant.integration.test.tsx. Keep
                  the phrase intact when rewording around it. */}
              <p className="mt-2.5 text-neutral-400 text-[13px] max-w-[30rem] leading-relaxed text-balance">
                Ask about your mail. Everything you type and every message it reads stays
                on your own server — and it never acts without your OK.
              </p>
              {/* Two columns once the SURFACE is wide. A single 20rem stack left
                  an enormous void either side on a maximized window; two columns
                  inside the 420px slide-over would be two slivers. */}
              <div className="mt-7 grid grid-cols-1 @xl:grid-cols-2 gap-2.5 w-full max-w-[34rem] text-left">
                {QUICK.map(q => (
                  <button
                    key={q.id}
                    onClick={() => onQuick(q)}
                    disabled={busy}
                    className="group flex flex-col gap-1 text-left px-4 py-3.5 rounded-xl bg-[var(--bg-surface)] border border-[var(--border-default)] hover:border-[var(--border-strong)] hover:bg-[var(--bg-hover)] transition-colors disabled:opacity-50 focus-primary"
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
          ) : (
            <>
              {messages.map((m, i) => {
                const isLast = i === messages.length - 1
                if (m.role === 'user') return <UserTurn key={m.id} content={m.content} />
                const proposal = m.proposal
                return (
                  <AssistantTurn
                    key={m.id}
                    content={m.content}
                    pending={m.pending}
                    error={m.error}
                    steps={m.steps}
                    onRetry={m.error && isLast ? retryLast : undefined}
                  >
                    {proposal && (
                      <div className="w-full max-w-[32rem]">
                        <ProposalCard
                          proposal={proposal}
                          state={m.state}
                          onApprove={() => approveProposal(m.id, proposal)}
                          onReject={() => rejectProposal(m.id)}
                        />
                      </div>
                    )}
                  </AssistantTurn>
                )
              })}
              {showFollowUps && (
                /* Directly under the answer, NOT floated to the foot of the
                   canvas with `mt-auto` — that was tried, and a lone chip
                   stranded 600px below the text it follows reads as an orphan
                   and puts the hole in the middle of the page instead of at
                   the end of it. */
                <div className="va-rise flex flex-wrap items-center gap-2 pl-9 pt-0.5">
                  <span className="mono text-[10px] uppercase tracking-[0.14em] text-neutral-600 pr-0.5">Next</span>
                  {followUps.map(q => (
                    <button
                      key={q.id}
                      type="button"
                      onClick={() => onQuick(q)}
                      className="text-[12px] px-3 h-7 rounded-full bg-[var(--bg-surface)] border border-[var(--border-default)] text-neutral-400 hover:text-neutral-100 hover:border-[var(--border-strong)] hover:bg-[var(--bg-hover)] transition-colors focus-primary"
                    >
                      {q.label}
                    </button>
                  ))}
                </div>
              )}
            </>
          )}
        </div>
      </div>

      {/* Composer — ONE object. The field, the keyboard hint and the send button
          used to be three separately-floating things stacked in a loose band;
          the hint and the sovereignty caption now sit on a footer rail INSIDE
          the field's border, so the whole thing reads as a single control. */}
      <form onSubmit={submit} className="flex-shrink-0 border-t border-[var(--border-default)] bg-[var(--bg-surface)]">
        <div className={`${COLUMN} px-4 @xl:px-6 py-3`}>
          <div className="rounded-2xl bg-[var(--bg-base)] border border-[var(--border-default)] transition-colors focus-within:border-[color-mix(in_srgb,var(--accent)_55%,var(--border-emphasis))] shadow-[var(--shadow-sm)]">
            <textarea
              ref={inputRef}
              rows={1}
              value={input}
              onChange={e => setInput(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) submit(e) }}
              placeholder="Ask about your mail…"
              aria-label="Ask about your mail"
              className="block w-full resize-none bg-transparent px-3.5 pt-3 pb-1 text-[13.5px] leading-relaxed text-neutral-100 placeholder-neutral-400 focus:outline-none"
            />
            <div className="flex items-center gap-3 pl-3.5 pr-2 pb-2 pt-0.5">
              {/* Container query, not `sm:` — inside the 420px slide-over these
                  hints wrapped onto two lines and truncated mid-word precisely
                  because a viewport breakpoint said the desktop was wide. */}
              <span className="hidden @xl:flex items-center gap-1 text-[11px] text-neutral-600 min-w-0">
                <kbd className="mono">Enter</kbd> to send · <kbd className="mono">Shift</kbd>+<kbd className="mono">Enter</kbd> for a new line
              </span>
              <span className="flex @xl:hidden items-center gap-1.5 text-[11px] text-neutral-600 min-w-0 truncate">
                <svg viewBox="0 0 16 16" className="w-3 h-3 shrink-0" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true">
                  <rect x="3.25" y="7" width="9.5" height="6.25" rx="1.6" /><path d="M5.5 7V5.25a2.5 2.5 0 0 1 5 0V7" />
                </svg>
                Stays on your box
              </span>
              <span className="ml-auto hidden @2xl:flex items-center gap-1.5 text-[11px] text-neutral-600 shrink-0">
                <svg viewBox="0 0 16 16" className="w-3 h-3" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true">
                  <rect x="3.25" y="7" width="9.5" height="6.25" rx="1.6" /><path d="M5.5 7V5.25a2.5 2.5 0 0 1 5 0V7" />
                </svg>
                Nothing is sent to a third party
              </span>
              <button
                type="submit"
                disabled={busy || !input.trim()}
                style={{ background: 'var(--accent)' }}
                className="ml-auto @2xl:ml-0 flex-shrink-0 w-8 h-8 rounded-lg text-white flex items-center justify-center transition-[filter,opacity] hover:brightness-110 disabled:opacity-30 disabled:hover:brightness-100 focus-primary"
                aria-label="Send"
                title="Send (Enter)"
              >
                {busy
                  ? <span className="w-3.5 h-3.5 spinner border-white/40 border-t-white" />
                  : (
                    <svg viewBox="0 0 20 20" fill="currentColor" width="15" height="15">
                      <path d="M10.894 2.553a1 1 0 00-1.788 0l-7 14a1 1 0 001.169 1.409l5-1.429A1 1 0 009 15.571V11a1 1 0 112 0v4.571a1 1 0 00.725.962l5 1.428a1 1 0 001.17-1.408l-7-14z" />
                    </svg>
                  )}
              </button>
            </div>
          </div>
        </div>
      </form>
      </div>
        <InstanceRail status={status} onChangeTier={() => setPickerOpen(true)} />
      </div>
    </div>
  )
}
