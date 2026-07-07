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
export async function runAgentTurn({ message, history, signal, onStatus, onToken, onProposal, onStep } = {}) {
  let answer = ''
  let proposal = null
  let steps = [] // read-only tool trace, surfaced to the UI (never executed)
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
    let terminalError = null

    const handle = (ev) => {
      progressed = true
      switch (ev.type) {
        case 'status': {
          // A read-only tool ran on the box. Record it as a trace entry so the
          // UI can show WHAT the assistant did (transparency), not just spin.
          const step = { tool: ev.tool, content: ev.content }
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
          let ev
          try { ev = JSON.parse(line.slice(6)) } catch { continue }
          handle(ev)
        }
      }
    }
    // Flush any trailing frame without a terminating blank line.
    for (const line of buffer.split('\n')) {
      if (!line.startsWith('data: ')) continue
      let ev
      try { ev = JSON.parse(line.slice(6)) } catch { continue }
      handle(ev)
    }

    // If the connection dropped before ANY event, the stream never really
    // started — fall back. Otherwise honour what the server told us.
    if (!progressed) throw new StreamUnavailable()
    return { answer, proposal, error: terminalError, streamed: true, steps }
  } catch (err) {
    if (err?.name === 'AbortError') throw err
    // Fall back to non-streaming ONLY if the stream never produced an event.
    if (progressed && !(err instanceof StreamUnavailable)) {
      return { answer, proposal, error: 'The assistant connection was interrupted.', streamed: true, steps }
    }
    return runAgentFallback({ message, history, onToken, onProposal })
  }
}

// runAgentFallback is the non-streaming path (POST /api/assistant/agent). It
// surfaces the same shape as the streaming path so callers need one code path.
async function runAgentFallback({ message, history, onToken, onProposal }) {
  try {
    const res = await fetch('/api/assistant/agent', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message, history: history || [] }),
    })
    const data = await res.json().catch(() => ({}))
    // The non-streaming /agent returns the same {answer|proposal, steps} shape.
    const steps = Array.isArray(data.steps) ? data.steps : []
    if (!res.ok) {
      return { answer: '', proposal: null, error: data.error || `Assistant unavailable (${res.status}).`, streamed: false, steps }
    }
    if (data.proposal) {
      onProposal?.(data.proposal)
      return { answer: data.answer || '', proposal: data.proposal, error: null, streamed: false, steps }
    }
    const answer = data.answer || ''
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
