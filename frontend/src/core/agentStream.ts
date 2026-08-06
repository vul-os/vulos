// agentStream.js — the shared client for the STREAMING agentic assistant turn
// (Wave 17). It POSTs to /api/assistant/agent/stream (SSE) and dispatches the
// server's events to callbacks so the UI can render the answer LIVE, token by
// token, instead of waiting for the whole request/response.
//
// SSE event protocol (one JSON object per `data:` frame), mirroring the backend
// (see routes_assistant.go POST /api/assistant/agent/stream):
//
//   {type:"status",  tool, content}   — a read-only tool is running server-side
//   {type:"token",   content}         — a piece of the final answer (repeated)
//   {type:"proposal", proposal, steps}— terminal: a MUTATING action to approve
//   {type:"done"}                     — terminal success
//   {type:"error",   error}           — terminal failure (e.g. egress blocked)
//
// SECURITY: this client never executes anything. A mutating action arrives ONLY
// as a {type:"proposal"} event; the server has already stored it in the ledger
// (bound to the session) and the caller must POST its opaque id to
// /api/assistant/execute after the user approves — unchanged from the wave-9
// flow. Streaming changes presentation only, never the confirmation gate.
//
// GRACEFUL FALLBACK: if the stream cannot be established or consumed (older box,
// proxy that buffers SSE, network hiccup, or no ReadableStream support) BEFORE
// any event has been received, we transparently fall back to the non-streaming
// POST /api/assistant/agent and surface its {answer|proposal} as a single token
// batch. A well-formed terminal {type:"error"} is NOT a fallback trigger — the
// server spoke, so we surface it directly (this keeps egress-blocked to one
// round-trip and one clear message).

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// AgentProposal — a mutating action awaiting approval (the wave-9
// confirmation-gate card; see builtin/assistant/ProposalCard.tsx, which reads
// tool/args/summary/from_content/warning off exactly this object) plus the
// opaque `id` the caller POSTs back to /api/assistant/execute.
//
// This used to be mirrored by shell/agentTypes.ts, a hand-written typed view
// of this module kept in sync by field name. That file existed only because
// this module was untyped JavaScript; the TypeScript migration made it
// redundant by construction, so it has been deleted rather than left to drift
// against the definition it duplicated.
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

// AgentStep — a tool-trace entry. Two shapes accumulate into the same `steps`
// array: a live {tool, content} entry recorded off each 'status' event, later
// possibly REPLACED wholesale by the terminal proposal event's richer
// {tool, args, result} trace (see the comment on runAgentTurn below).
export interface AgentStep {
  tool?: string
  content?: string
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
  history?: AgentHistoryTurn[]
  signal?: AbortSignal
  onStatus?: (ev: AgentStatusEvent) => void
  onToken?: (delta: string, full: string) => void
  onProposal?: (proposal: AgentProposal) => void
  onStep?: (step: AgentStep, steps: AgentStep[]) => void
}

// A proposal without a usable id can never be executed (ProposalCard POSTs
// this id back to /api/assistant/execute), so it is narrowed out here rather
// than surfaced as an unusable card.
function toAgentProposal(x: unknown): AgentProposal | null {
  if (!isRecord(x) || typeof x.id !== 'string') return null
  return {
    id: x.id,
    tool: typeof x.tool === 'string' ? x.tool : undefined,
    args: isRecord(x.args) ? x.args : undefined,
    summary: typeof x.summary === 'string' ? x.summary : undefined,
    from_content: typeof x.from_content === 'boolean' ? x.from_content : undefined,
    warning: typeof x.warning === 'string' ? x.warning : undefined,
  }
}

function toAgentStep(x: unknown): AgentStep | null {
  if (!isRecord(x)) return null
  return {
    tool: typeof x.tool === 'string' ? x.tool : undefined,
    content: typeof x.content === 'string' ? x.content : undefined,
    args: isRecord(x.args) ? x.args : undefined,
    result: 'result' in x ? x.result : undefined,
  }
}

type AgentSSEEvent =
  | { type: 'status'; tool?: string; content?: string }
  | { type: 'token'; content?: string }
  | { type: 'proposal'; proposal: AgentProposal | null; steps?: AgentStep[] }
  | { type: 'done' }
  | { type: 'error'; error?: string }
  | { type: 'unknown' }

// toAgentSSEEvent narrows one parsed `data:` frame off the wire (see the SSE
// event protocol comment above) — a trust boundary, so every field is checked
// rather than assumed.
function toAgentSSEEvent(x: unknown): AgentSSEEvent | null {
  if (!isRecord(x) || typeof x.type !== 'string') return null
  switch (x.type) {
    case 'status':
      return {
        type: 'status',
        tool: typeof x.tool === 'string' ? x.tool : undefined,
        content: typeof x.content === 'string' ? x.content : undefined,
      }
    case 'token':
      return { type: 'token', content: typeof x.content === 'string' ? x.content : undefined }
    case 'proposal':
      return {
        type: 'proposal',
        proposal: toAgentProposal(x.proposal),
        steps: Array.isArray(x.steps)
          ? x.steps.map(toAgentStep).filter((s): s is AgentStep => s !== null)
          : undefined,
      }
    case 'error':
      return { type: 'error', error: typeof x.error === 'string' ? x.error : undefined }
    case 'done':
      return { type: 'done' }
    default:
      return { type: 'unknown' }
  }
}

// runAgentTurn drives one streaming turn.
//
//   opts.message   — the user's text (required)
//   opts.history   — prior [{role, content}] turns (proposals excluded)
//   opts.signal    — optional AbortSignal
//   opts.onStatus  — (ev) => void            a tool started running
//   opts.onToken   — (delta, full) => void   a new piece of the answer arrived
//   opts.onProposal— (proposal) => void      a mutating action awaits approval
//   opts.onStep    — (step, steps) => void   a read-only tool ran (trace entry)
//
// Resolves to { answer, proposal, error, streamed, steps }:
//   answer   — the full accumulated answer text ('' if none)
//   proposal — the mutating proposal object, or null
//   error    — an error string if the turn failed, else null
//   streamed — true if the live SSE path served the turn (false ⇒ fallback used)
//   steps    — the read-only TOOL TRACE for transparency: [{tool, args?, result?}]
//              (accumulated from status events; the terminal proposal event may
//              carry a richer trace with args+result, which we prefer)
export async function runAgentTurn({ message, history, signal, onStatus, onToken, onProposal, onStep }: RunAgentTurnOptions): Promise<AgentTurnResult> {
  let answer = ''
  let proposal: AgentProposal | null = null
  let steps: AgentStep[] = [] // read-only tool trace, surfaced to the UI (never executed)
  let progressed = false // any event received ⇒ the server is speaking; don't fall back on transport errors

  try {
    const res = await fetch('/api/assistant/agent/stream', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message, history: history || [] }),
      signal,
    })
    // A non-OK response (e.g. 400/401) or a body we can't stream ⇒ fall back.
    if (!res.ok || !res.body || !res.body.getReader) {
      throw new StreamUnavailable()
    }

    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    let terminalError: string | null = null

    const handle = (rawEv: unknown) => {
      const ev = toAgentSSEEvent(rawEv)
      if (!ev) return
      progressed = true
      switch (ev.type) {
        case 'status': {
          // A read-only tool ran on the box. Record it as a trace entry so the
          // UI can show WHAT the assistant did (transparency), not just spin.
          const step: AgentStep = { tool: ev.tool, content: ev.content }
          steps.push(step)
          onStatus?.(ev)
          onStep?.(step, steps)
          break
        }
        case 'token': {
          const delta = ev.content || ''
          if (delta) {
            answer += delta
            onToken?.(delta, answer)
          }
          break
        }
        case 'proposal':
          proposal = ev.proposal || null
          // The terminal proposal event carries a richer trace (args + result);
          // prefer it over the status-only entries accumulated above.
          if (Array.isArray(ev.steps) && ev.steps.length) steps = ev.steps
          if (proposal) onProposal?.(proposal)
          break
        case 'error':
          terminalError = ev.error || 'assistant error'
          break
        case 'done':
        default:
          break
      }
    }

    // Read the SSE stream, splitting on the blank-line frame separator.
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      let sep
      while ((sep = buffer.indexOf('\n\n')) >= 0) {
        const frame = buffer.slice(0, sep)
        buffer = buffer.slice(sep + 2)
        for (const line of frame.split('\n')) {
          if (!line.startsWith('data: ')) continue
          let ev: unknown
          try { ev = JSON.parse(line.slice(6)) } catch { continue }
          handle(ev)
        }
      }
    }
    // Flush any trailing frame without a terminating blank line.
    for (const line of buffer.split('\n')) {
      if (!line.startsWith('data: ')) continue
      let ev: unknown
      try { ev = JSON.parse(line.slice(6)) } catch { continue }
      handle(ev)
    }

    // If the connection dropped before ANY event, the stream never really
    // started — fall back. Otherwise honour what the server told us.
    if (!progressed) throw new StreamUnavailable()
    return { answer, proposal, error: terminalError, streamed: true, steps }
  } catch (err) {
    if (isRecord(err) && err.name === 'AbortError') throw err
    // Fall back to non-streaming ONLY if the stream never produced an event.
    if (progressed && !(err instanceof StreamUnavailable)) {
      return { answer, proposal, error: 'The assistant connection was interrupted.', streamed: true, steps }
    }
    return runAgentFallback({ message, history, onToken, onProposal })
  }
}

interface RunAgentFallbackOptions {
  message: string
  history?: AgentHistoryTurn[]
  onToken?: (delta: string, full: string) => void
  onProposal?: (proposal: AgentProposal) => void
}

// runAgentFallback is the non-streaming path (POST /api/assistant/agent). It
// surfaces the same shape as the streaming path so callers need one code path.
async function runAgentFallback({ message, history, onToken, onProposal }: RunAgentFallbackOptions): Promise<AgentTurnResult> {
  try {
    const res = await fetch('/api/assistant/agent', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message, history: history || [] }),
    })
    const data: unknown = await res.json().catch(() => ({}))
    const d = isRecord(data) ? data : {}
    // The non-streaming /agent returns the same {answer|proposal, steps} shape.
    const steps: AgentStep[] = Array.isArray(d.steps) ? d.steps.map(toAgentStep).filter((s): s is AgentStep => s !== null) : []
    if (!res.ok) {
      const errMsg = typeof d.error === 'string' ? d.error : `Assistant unavailable (${res.status}).`
      return { answer: '', proposal: null, error: errMsg, streamed: false, steps }
    }
    const proposal = toAgentProposal(d.proposal)
    if (proposal) {
      onProposal?.(proposal)
      const answer = typeof d.answer === 'string' ? d.answer : ''
      return { answer, proposal, error: null, streamed: false, steps }
    }
    const answer = typeof d.answer === 'string' ? d.answer : ''
    if (answer) onToken?.(answer, answer)
    return { answer, proposal: null, error: null, streamed: false, steps }
  } catch {
    return { answer: '', proposal: null, error: 'Could not reach the assistant. Is the box online?', streamed: false, steps: [] }
  }
}

// StreamUnavailable marks a "stream never started" condition that should trigger
// the non-streaming fallback (as opposed to a mid-stream interruption).
class StreamUnavailable extends Error {
  constructor() {
    super('assistant stream unavailable')
    this.name = 'StreamUnavailable'
  }
}
