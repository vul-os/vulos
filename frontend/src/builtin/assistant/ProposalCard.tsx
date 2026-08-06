/**
 * ProposalCard + StepTrace — the shared confirmation-gate + tool-trace surfaces
 * for the sovereign assistant, rendered identically wherever the agent turn runs
 * (the Assistant panel, Home's composer, the Command Palette's inline Ask).
 *
 * Previously each of those three surfaces carried its own hand-copied proposal
 * markup, which had drifted (different padding, hardcoded amber-/emerald-/red-
 * Tailwind classes that ignored the shell's --status-* tokens). This is the one
 * source of truth so the confirmation gate looks and behaves the same — and
 * retheme with the operator's chosen accent + semantic tokens — everywhere.
 *
 * SECURITY / BEHAVIOUR is unchanged: the card only renders a PROPOSAL and calls
 * back onApprove / onReject. Approval elsewhere POSTs ONLY the opaque proposal id
 * to /api/assistant/execute; nothing here executes anything. Every field is
 * escaped React text (never innerHTML) — a proposal or tool result may quote
 * untrusted mail content, so it must never be interpreted as markup.
 */
import type { AgentProposal } from '../../core/agentStream'

const PROPOSAL_VERB: Record<string, string> = {
  send_email: 'Send email',
  create_calendar_event: 'Create event',
  add_contact: 'Add contact',
  triage: 'Change mailbox',
  rsvp_invite: 'RSVP to invite',
}

type ProposalArgs = Record<string, unknown>

// The legible "what will happen" summary line: pull the few args that name the
// TARGET of the action (who/what/when) so the user sees the concrete effect at a
// glance, above the free-text body. Order is deliberate; unknown tools fall back
// to nothing (the summary line already carries the intent).
function keyFacts(tool: string | undefined, args: ProposalArgs): Array<[string, string]> {
  const facts: Array<[string, string]> = []
  const add = (label: string, value: unknown) => { if (value != null && value !== '') facts.push([label, String(value)]) }
  switch (tool) {
    case 'send_email':
      add('To', args.to)
      add('Subject', args.subject)
      break
    case 'create_calendar_event':
      add('Event', args.summary || args.title)
      add('When', args.start || args.when)
      add('Where', args.location)
      break
    case 'add_contact':
      add('Name', args.name)
      add('Email', args.email)
      break
    case 'triage':
      add('Move to', args.folder || args.mailbox)
      break
    case 'rsvp_invite':
      add('Response', args.response)
      break
    default:
      break
  }
  return facts
}

/**
 * ProposalCard — the CONFIRMATION GATE. Nothing has happened yet; the user must
 * Approve before the action runs, or Reject to drop it.
 *
 * Props:
 *   proposal   — { tool, summary, args, from_content?, warning? }
 *   state      — 'pending' | 'busy' | 'done' | 'rejected'
 *   onApprove  — () => void
 *   onReject   — () => void
 *   compact    — tighter radius/padding for the inline palette surface
 *   approveKey / rejectKey — optional aria-keyshortcuts (e.g. the palette binds
 *                Y/N globally, so it advertises them on the buttons for AT users)
 */
export type ProposalState = 'pending' | 'busy' | 'done' | 'rejected'

export interface ProposalCardProps {
  proposal: AgentProposal
  state?: ProposalState
  onApprove: () => void
  onReject: () => void
  compact?: boolean
  approveKey?: string
  rejectKey?: string
}

export function ProposalCard({ proposal, state, onApprove, onReject, compact = false, approveKey, rejectKey }: ProposalCardProps) {
  const verb = PROPOSAL_VERB[proposal.tool ?? ''] || 'Action'
  const args: ProposalArgs = proposal.args || {}
  const facts = keyFacts(proposal.tool, args)
  // args.body / args.notes are free-text mail/event content, always a string
  // from the backend's JSON — narrowed rather than rendered as `unknown`.
  const bodyRaw = args.body || args.notes
  const body = typeof bodyRaw === 'string' ? bodyRaw : undefined
  const settled = state === 'done' || state === 'rejected'

  return (
    <div
      role="group"
      aria-label={`Assistant proposal: ${verb}. Needs your approval.`}
      className={`text-[13px] border bg-warning-soft border-warning-soft shadow-[0_1px_3px_rgba(0,0,0,0.12)] min-w-0 ${
        compact ? 'rounded-xl px-3 py-2.5' : 'rounded-2xl rounded-bl-md px-3.5 py-3'
      }`}
    >
      <div className="flex items-center gap-2 mb-1.5">
        <span className="inline-block w-2 h-2 rounded-full bg-warning shrink-0" aria-hidden="true" />
        <span className="text-warning font-medium text-[12px]">Needs your approval · {verb}</span>
      </div>

      <div className="text-neutral-200 leading-relaxed">{proposal.summary}</div>

      {facts.length > 0 && (
        <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[12px]">
          {facts.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-neutral-500">{k}</dt>
              <dd className="text-neutral-200 break-words min-w-0">{v}</dd>
            </div>
          ))}
        </dl>
      )}

      {proposal.from_content && (
        <div
          role="alert"
          className="text-[12px] text-danger bg-danger-soft border border-danger-soft rounded-lg px-2.5 py-1.5 mt-2 flex gap-1.5"
        >
          <span aria-hidden="true">⚠</span>
          <span>{proposal.warning || "This action's target came from message content — review it carefully before approving."}</span>
        </div>
      )}

      {body && (
        <div className="text-[12px] text-neutral-400 whitespace-pre-wrap break-words bg-neutral-900/50 border border-neutral-800/60 rounded-lg px-2.5 py-2 mt-2 max-h-40 overflow-y-auto">
          {body}
        </div>
      )}

      {state === 'done' ? (
        <div className="text-[12px] text-success mt-2.5">✓ Approved and executed.</div>
      ) : state === 'rejected' ? (
        <div className="text-[12px] text-neutral-500 mt-2.5">Rejected — nothing was done.</div>
      ) : (
        <div className="flex items-center gap-2 mt-3">
          <button
            type="button"
            disabled={state === 'busy'}
            onClick={onApprove}
            aria-keyshortcuts={approveKey || undefined}
            className="inline-flex items-center justify-center gap-1.5 text-[12px] min-h-[40px] px-3.5 py-2 rounded-lg bg-success text-black font-medium hover:brightness-110 transition-[filter] disabled:opacity-50 disabled:hover:brightness-100 focus-primary"
          >
            {state === 'busy' ? (
              <>
                <span className="inline-block w-3.5 h-3.5 rounded-full border-2 border-black/30 border-t-black animate-spin motion-reduce:animate-none" aria-hidden="true" />
                Working…
              </>
            ) : (
              <>
                <svg viewBox="0 0 20 20" fill="currentColor" width="13" height="13" aria-hidden="true">
                  <path fillRule="evenodd" d="M16.7 5.3a1 1 0 010 1.4l-7.5 7.5a1 1 0 01-1.4 0l-3.5-3.5a1 1 0 111.4-1.4l2.8 2.79 6.8-6.79a1 1 0 011.4 0z" clipRule="evenodd" />
                </svg>
                Approve
              </>
            )}
          </button>
          <button
            type="button"
            disabled={state === 'busy'}
            onClick={onReject}
            aria-keyshortcuts={rejectKey || undefined}
            className="inline-flex items-center justify-center gap-1.5 text-[12px] min-h-[40px] px-3.5 py-2 rounded-lg bg-neutral-800 text-neutral-300 hover:bg-neutral-700 transition-colors disabled:opacity-50 focus-primary"
          >
            <svg viewBox="0 0 20 20" fill="currentColor" width="12" height="12" aria-hidden="true">
              <path fillRule="evenodd" d="M4.3 4.3a1 1 0 011.4 0L10 8.6l4.3-4.3a1 1 0 111.4 1.4L11.4 10l4.3 4.3a1 1 0 01-1.4 1.4L10 11.4l-4.3 4.3a1 1 0 01-1.4-1.4L8.6 10 4.3 5.7a1 1 0 010-1.4z" clipRule="evenodd" />
            </svg>
            Reject
          </button>
        </div>
      )}

      {settled && <span className="sr-only" role="status">{state === 'done' ? 'The action ran.' : 'The action was cancelled.'}</span>}
    </div>
  )
}

// ── Tool-trace (steps) ───────────────────────────────────────────────────────
// A READ-ONLY, collapsible trace of the read-only tools the agent ran during a
// turn (search_mail, read_thread, …). A transparency win: the user can see WHAT
// the assistant looked at to reach its answer. Every field is escaped React text.

const STEP_VERB: Record<string, string> = {
  search_mail: 'Searched mail',
  read_thread: 'Read a thread',
  find_contact: 'Looked up a contact',
  find_file: 'Searched files',
  read_file: 'Read a file',
  list_reminders: 'Checked reminders',
}

// A tool-trace entry. Two shapes accumulate into the same `steps` array (see
// core/agentStream.ts's runAgentTurn comment): a live {tool, content} entry
// recorded off each 'status' event, later possibly REPLACED wholesale by the
// terminal proposal event's richer trace. args/result there are backend-
// formatted display strings (ToolStep.Args/Result in routes_assistant.go's
// agent.go are `string`, already summarised/truncated server-side) — not
// structured data, so they render directly as text.
export interface StepTraceItem {
  tool?: string
  content?: string
  args?: string
  result?: string
}

export interface StepTraceProps {
  steps?: StepTraceItem[] | null
  className?: string
}

export function StepTrace({ steps, className = '' }: StepTraceProps) {
  if (!steps || !steps.length) return null
  const n = steps.length
  return (
    <details className={`group va-steps min-w-0 ${className}`}>
      <style>{`
        @keyframes vaStepReveal { from { opacity: 0; transform: translateY(-2px) } to { opacity: 1; transform: none } }
        details.va-steps[open] > ol { animation: vaStepReveal var(--motion-base) var(--ease-out) }
        @media (prefers-reduced-motion: reduce) { details.va-steps[open] > ol { animation: none } }
      `}</style>
      <summary className="cursor-pointer text-[12px] text-neutral-500 hover:text-neutral-300 select-none list-none flex items-center gap-1.5 rounded focus-primary w-fit transition-colors">
        <svg viewBox="0 0 20 20" fill="currentColor" width="10" height="10" aria-hidden="true"
          className="transition-transform group-open:rotate-90 motion-reduce:transition-none">
          <path fillRule="evenodd" d="M7.21 5.23a.75.75 0 011.06.02l4.5 4.25a.75.75 0 010 1.08l-4.5 4.25a.75.75 0 11-1.04-1.08L11.17 10 7.23 6.29a.75.75 0 01-.02-1.06z" clipRule="evenodd" />
        </svg>
        {n === 1 ? '1 step' : `${n} steps`}
        <span className="text-neutral-600 group-open:hidden">· show work</span>
      </summary>
      <ol className="mt-1.5 space-y-1.5 pl-1 border-l border-neutral-800 ml-1">
        {steps.map((s, i) => (
          <li key={i} className="pl-3 text-[12px] leading-snug relative">
            <span className="absolute -left-[3px] top-1.5 w-1.5 h-1.5 rounded-full bg-neutral-700" aria-hidden="true" />
            <div className="text-neutral-300">{STEP_VERB[s.tool ?? ''] || (s.tool || 'tool')}</div>
            {s.args && <div className="text-neutral-600 font-mono break-words">{s.args}</div>}
            {s.result && (
              <div className="text-neutral-500 whitespace-pre-wrap break-words mt-0.5 max-h-24 overflow-y-auto">{s.result}</div>
            )}
          </li>
        ))}
      </ol>
    </details>
  )
}
