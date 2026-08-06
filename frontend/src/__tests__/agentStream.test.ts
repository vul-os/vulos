/**
 * agentStream.test.js — the STREAMING agentic assistant client (Wave 17).
 *
 * Verifies the SSE consumer:
 *   • streams token events into onToken and reconstructs the full answer;
 *   • surfaces a mutating action as a proposal (never executes it);
 *   • surfaces a terminal error event directly (no fallback double-request);
 *   • falls back to the non-streaming /agent when the stream can't be started.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { runAgentTurn, type AgentProposal, type AgentStatusEvent } from '../core/agentStream'

interface SSEFrame {
  type: string
  tool?: string
  content?: string
  proposal?: unknown
  error?: string
}

// sseResponse builds a fetch-like Response whose body streams the given SSE
// frames (each already a full `data: {...}` line) as UTF-8 chunks.
function sseResponse(frames: SSEFrame[]) {
  const enc = new TextEncoder()
  let i = 0
  return {
    ok: true,
    body: {
      getReader() {
        return {
          read() {
            if (i >= frames.length) return Promise.resolve({ done: true })
            const frame = frames[i++]
            return Promise.resolve({ done: false, value: enc.encode(`data: ${JSON.stringify(frame)}\n\n`) })
          },
        }
      },
    },
  }
}

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('runAgentTurn (streaming)', () => {
  it('streams token events and reconstructs the answer, ending in done', async () => {
    const fetchMock = vi.fn().mockResolvedValue(sseResponse([
      { type: 'status', tool: 'search_mail', content: 'using search_mail…' },
      { type: 'token', content: 'You owe ' },
      { type: 'token', content: '$128.40.' },
      { type: 'done' },
    ]))
    vi.stubGlobal('fetch', fetchMock)
    const tokens: string[] = []
    const statuses: string[] = []
    const result = await runAgentTurn({
      message: 'when is my invoice due?',
      onToken: (delta: string) => tokens.push(delta),
      onStatus: (ev: AgentStatusEvent) => statuses.push(ev.tool || ''),
    })
    expect(tokens).toEqual(['You owe ', '$128.40.'])
    expect(statuses).toEqual(['search_mail'])
    expect(result.answer).toBe('You owe $128.40.')
    expect(result.proposal).toBeNull()
    expect(result.error).toBeNull()
    expect(result.streamed).toBe(true)
    // Streaming was used — no fallback to /agent.
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/assistant/agent/stream')
  })

  it('surfaces a mutating proposal without executing anything', async () => {
    const proposal = { id: 'prop_abc', tool: 'send_email', summary: 'Send email to dana@acme.io' }
    const fetchMock = vi.fn().mockResolvedValue(sseResponse([
      { type: 'proposal', proposal },
      { type: 'done' },
    ]))
    vi.stubGlobal('fetch', fetchMock)
    let gotProposal: AgentProposal | null = null
    const result = await runAgentTurn({ message: 'email dana', onProposal: (p: AgentProposal) => { gotProposal = p } })
    expect(gotProposal).toEqual(proposal)
    expect(result.proposal).toEqual(proposal)
    expect(result.answer).toBe('')
    // Only the stream endpoint was hit — no /execute (that requires user approval).
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls.every((c: unknown[]) => c[0] !== '/api/assistant/execute')).toBe(true)
  })

  it('surfaces a terminal error event directly (egress blocked) with no fallback', async () => {
    const fetchMock = vi.fn().mockResolvedValue(sseResponse([
      { type: 'error', error: 'sovereignty tier not permitted' },
    ]))
    vi.stubGlobal('fetch', fetchMock)
    const result = await runAgentTurn({ message: 'send an email' })
    expect(result.error).toContain('sovereignty')
    expect(result.streamed).toBe(true)
    // Server spoke → exactly one request, no fallback to /agent.
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('falls back to non-streaming /agent when the stream cannot be established', async () => {
    const fetchMock = vi.fn()
      // First call: /agent/stream is not OK (older box / no SSE) → fallback.
      .mockResolvedValueOnce({ ok: false, status: 404, body: null })
      // Fallback: /agent returns a plain answer.
      .mockResolvedValueOnce({ ok: true, json: async () => ({ answer: 'Fallback answer.' }) })
    vi.stubGlobal('fetch', fetchMock)
    const tokens: string[] = []
    const result = await runAgentTurn({ message: 'hello', onToken: (d: string) => tokens.push(d) })
    expect(result.answer).toBe('Fallback answer.')
    expect(result.streamed).toBe(false)
    expect(tokens).toEqual(['Fallback answer.'])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/assistant/agent/stream')
    expect(fetchMock.mock.calls[1][0]).toBe('/api/assistant/agent')
  })

  it('falls back and surfaces a proposal from the non-streaming path', async () => {
    const proposal = { id: 'prop_fb', tool: 'send_email', summary: 'Send' }
    const fetchMock = vi.fn()
      .mockRejectedValueOnce(new TypeError('network down'))
      .mockResolvedValueOnce({ ok: true, json: async () => ({ proposal }) })
    vi.stubGlobal('fetch', fetchMock)
    let gotProposal: AgentProposal | null = null
    const result = await runAgentTurn({ message: 'email x', onProposal: (p: AgentProposal) => { gotProposal = p } })
    expect(gotProposal).toEqual(proposal)
    expect(result.proposal).toEqual(proposal)
    expect(result.streamed).toBe(false)
  })

  // Unmount/window-close mid-stream: the consumer aborts the AbortController it
  // passed as `signal`. runAgentTurn MUST re-throw the AbortError (so the caller
  // knows to tear down) and MUST NOT fall back to the non-streaming /agent —
  // otherwise a closed Assistant window would keep talking to the model and then
  // write to dead component state (the leak/warning this guards against).
  it('re-throws AbortError on abort mid-stream and does not fall back', async () => {
    const abortErr = new Error('aborted')
    abortErr.name = 'AbortError'
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve({
      ok: true,
      body: {
        getReader() {
          let served = false
          return {
            read() {
              if (!served) {
                served = true
                // One real token frame first, so `progressed` is set — proving
                // even a mid-stream (not pre-stream) abort re-throws rather than
                // being misclassified as "stream never started" → fallback.
                return Promise.resolve({
                  done: false,
                  value: new TextEncoder().encode('data: {"type":"token","content":"hi"}\n\n'),
                })
              }
              return Promise.reject(abortErr) // the abort surfaces here
            },
          }
        },
      },
    }))
    vi.stubGlobal('fetch', fetchMock)
    // A real (never-triggered) AbortSignal — the abort in this test is
    // simulated by the mocked reader rejecting, not by actually calling
    // AbortController.abort(), so an un-aborted signal exercises the same
    // "signal was passed through" path honestly, without a fake stand-in.
    await expect(runAgentTurn({ message: 'x', signal: new AbortController().signal })).rejects.toMatchObject({ name: 'AbortError' })
    // Exactly one request — no fallback to /agent after an abort.
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/assistant/agent/stream')
  })
})
