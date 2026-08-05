// agentTypes.ts — a typed view of core/agentStream.js's runAgentTurn() (untyped
// JS, out of scope for this pass — its own header comment documents this exact
// shape). Shared by CommandPalette.tsx and home/Home.tsx, the two shell
// surfaces that drive an agent turn, so both read the same real type instead
// of two independently-guessed ones.
import { runAgentTurn as runAgentTurnUntyped } from '../core/agentStream'

/** A mutating action awaiting approval — the wave-9 confirmation-gate card
 *  (see builtin/assistant/ProposalCard.jsx, which reads tool/args/summary/
 *  from_content/warning off exactly this object) plus the opaque `id` the
 *  caller POSTs back to /api/assistant/execute. */
export interface AgentProposal {
  id: string
  tool?: string
  args?: Record<string, unknown>
  summary?: string
  from_content?: boolean
  warning?: string
}

export interface AgentStatusEvent {
  tool?: string
  content?: string
}

export interface AgentStep {
  tool?: string
  args?: Record<string, unknown>
  result?: unknown
}

export interface AgentTurnResult {
  answer: string
  proposal: AgentProposal | null
  error: string | null
  streamed: boolean
  steps: AgentStep[]
}

export interface AgentHistoryTurn {
  role: string
  content: string
}

export interface RunAgentTurnOptions {
  message: string
  history: AgentHistoryTurn[]
  signal?: AbortSignal
  onStatus?: (ev: AgentStatusEvent) => void
  onToken?: (delta: string, full: string) => void
  onProposal?: (proposal: AgentProposal) => void
  onStep?: (step: AgentStep, steps: AgentStep[]) => void
}

export function runAgentTurn(opts: RunAgentTurnOptions): Promise<AgentTurnResult> {
  return runAgentTurnUntyped(opts) as Promise<AgentTurnResult>
}
