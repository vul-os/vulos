// api.js — AIROT-06: AI Assistant API client via the OS AI Router.
//
// All AI calls go through POST /api/ai/chat on the local OS server.
// There are NO direct provider SDK imports, NO hardcoded provider keys, and
// NO provider-specific base URLs here. The router handles both `byo` and
// `cloud` modes transparently.
//
// Public surface:
//   streamChat(messages, { onChunk, onDone, onError, signal })
//   fetchStatus()
//   fetchModels()

/** Base path for the local OS server AI Router endpoints. */
const AI_CHAT_URL = '/api/ai/chat'
const AI_STATUS_URL = '/api/ai/status'
const AI_MODELS_URL = '/api/ai/models'

/**
 * Stream a chat completion from the OS AI Router.
 *
 * Sends `messages` to POST /api/ai/chat with stream:true and delivers each
 * SSE delta to `onChunk`. Calls `onDone` when the stream ends normally, or
 * `onError` on failure (network error, non-2xx response, or abort).
 *
 * The conversation history format mirrors OpenAI/Anthropic:
 *   [{ role: 'system'|'user'|'assistant', content: string }, ...]
 *
 * Works identically in both `byo` and `cloud` router modes — the router
 * selects the correct provider/credentials without client involvement.
 *
 * @param {Array<{role: string, content: string}>} messages
 * @param {{
 *   model?: string,
 *   onChunk?: (delta: string) => void,
 *   onDone?: (fullText: string) => void,
 *   onError?: (err: Error) => void,
 *   signal?: AbortSignal
 * }} opts
 * @returns {Promise<void>}
 */
export async function streamChat(messages, {
  model = '',
  onChunk = () => {},
  onDone = () => {},
  onError = () => {},
  signal,
} = {}) {
  let full = ''

  try {
    const res = await fetch(AI_CHAT_URL, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ model, messages, stream: true }),
      signal,
    })

    if (!res.ok) {
      const body = await res.json().catch(() => ({}))
      const msg = body.error || `HTTP ${res.status}`
      // 422 = model_not_found, 503 = AI Router not configured
      throw new Error(msg)
    }

    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buf = ''

    while (true) {
      const { done, value } = await reader.read()
      if (done) break

      buf += decoder.decode(value, { stream: true })

      // Process complete SSE lines from the buffer.
      const lines = buf.split('\n')
      buf = lines.pop() // keep any incomplete trailing line

      for (const line of lines) {
        if (!line.startsWith('data: ')) continue
        const payload = line.slice(6).trim()
        if (payload === '[DONE]') break

        try {
          const ev = JSON.parse(payload)
          // Support both the airouter chunk format { content, done }
          // and the OpenAI-compat format { choices[0].delta.content }.
          const delta =
            ev.content ??
            ev.choices?.[0]?.delta?.content ??
            ''
          if (delta) {
            full += delta
            onChunk(delta)
          }
          if (ev.done) break
        } catch {
          // skip malformed SSE chunk
        }
      }
    }

    onDone(full)
  } catch (err) {
    if (err.name !== 'AbortError') {
      onError(err)
    }
    // AbortError = user cancelled; call onDone with whatever was received.
    else {
      onDone(full)
    }
  }
}

/**
 * Fetch the current AI Router status.
 * Returns { mode, active_model } or null on failure.
 *
 * @returns {Promise<{mode: string, active_model: string}|null>}
 */
export async function fetchStatus() {
  try {
    const res = await fetch(AI_STATUS_URL)
    if (!res.ok) return null
    return await res.json()
  } catch {
    return null
  }
}

/**
 * Fetch the list of available models from the AI Router.
 * Returns an array of { model, provider } entries, or [] on failure.
 *
 * @returns {Promise<Array<{model: string, provider: string}>>}
 */
export async function fetchModels() {
  try {
    const res = await fetch(AI_MODELS_URL, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })
    if (!res.ok) return []
    const data = await res.json()
    return data.models ?? []
  } catch {
    return []
  }
}
