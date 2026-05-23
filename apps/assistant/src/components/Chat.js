// Chat.js — AIROT-06: AI Assistant chat UI component (vanilla JS / DOM).
//
// Renders a conversation panel and wires the submit form to streamChat()
// from api.js. No direct provider calls; all AI I/O flows through the router.

import { streamChat, fetchStatus, fetchModels } from '../lib/api.js'

/** Maximum messages retained in local history for context. */
const MAX_HISTORY = 40

/**
 * Mount the assistant chat UI into `container`.
 *
 * @param {HTMLElement} container
 */
export function mount(container) {
  /** @type {Array<{role: string, content: string}>} */
  const history = []
  let abortCtrl = null
  let activeModel = ''

  // Build DOM
  container.innerHTML = `
    <div class="chat-wrap">
      <div class="chat-header">
        <span class="chat-title">Assistant</span>
        <span class="chat-status" id="chat-status">checking…</span>
      </div>
      <div class="chat-body" id="chat-body">
        <div class="chat-empty">What can I help you with?</div>
      </div>
      <form class="chat-form" id="chat-form" autocomplete="off">
        <input
          id="chat-input"
          type="text"
          placeholder="Ask anything…"
          autocomplete="off"
          spellcheck="false"
        />
        <button type="submit" id="chat-send">Send</button>
        <button type="button" id="chat-cancel" disabled>Cancel</button>
      </form>
    </div>
  `

  const body = container.querySelector('#chat-body')
  const form = container.querySelector('#chat-form')
  const input = container.querySelector('#chat-input')
  const sendBtn = container.querySelector('#chat-send')
  const cancelBtn = container.querySelector('#chat-cancel')
  const statusEl = container.querySelector('#chat-status')

  // ── Router status ──────────────────────────────────────────────────────────
  fetchStatus().then(status => {
    if (status && (status.active_model || status.mode)) {
      activeModel = status.active_model || ''
      statusEl.textContent = status.active_model
        ? `Model: ${status.active_model}`
        : `Mode: ${status.mode}`
      statusEl.className = 'chat-status ok'
    } else {
      statusEl.textContent = 'AI Router not configured'
      statusEl.className = 'chat-status err'
      input.placeholder = 'Configure AI Router in Settings first'
      input.disabled = true
      sendBtn.disabled = true
    }
  })

  // Populate model from models list if status didn't give us one.
  fetchModels().then(models => {
    if (!activeModel && models.length > 0) {
      activeModel = models[0].model
    }
  })

  // ── Helpers ────────────────────────────────────────────────────────────────
  function clearEmpty() {
    const empty = body.querySelector('.chat-empty')
    if (empty) empty.remove()
  }

  function addBubble(role, text) {
    clearEmpty()
    const div = document.createElement('div')
    div.className = `bubble bubble-${role}`
    div.textContent = text
    body.appendChild(div)
    body.scrollTop = body.scrollHeight
    return div
  }

  function setStreaming(on) {
    sendBtn.disabled = on
    cancelBtn.disabled = !on
    input.disabled = on
  }

  // ── Form submit ────────────────────────────────────────────────────────────
  form.addEventListener('submit', async (e) => {
    e.preventDefault()
    const text = input.value.trim()
    if (!text) return

    input.value = ''
    addBubble('user', text)

    // Build context window from local history.
    history.push({ role: 'user', content: text })
    if (history.length > MAX_HISTORY) history.splice(0, history.length - MAX_HISTORY)

    // Start streaming assistant reply.
    const replyBubble = addBubble('assistant', '')
    setStreaming(true)

    abortCtrl = new AbortController()

    let replyText = ''
    await streamChat([...history], {
      model: activeModel,
      signal: abortCtrl.signal,
      onChunk(delta) {
        replyText += delta
        replyBubble.textContent = replyText
        body.scrollTop = body.scrollHeight
      },
      onDone(full) {
        if (!full && !replyText) {
          replyBubble.textContent = '(no response)'
        }
        history.push({ role: 'assistant', content: full || replyText })
        if (history.length > MAX_HISTORY) history.splice(0, history.length - MAX_HISTORY)
        setStreaming(false)
        abortCtrl = null
        input.focus()
      },
      onError(err) {
        replyBubble.textContent = `Error: ${err.message}`
        replyBubble.className = 'bubble bubble-error'
        // Remove failed user message from history so next attempt is clean.
        const last = history[history.length - 1]
        if (last && last.role === 'user') history.pop()
        setStreaming(false)
        abortCtrl = null
        input.focus()
      },
    })
  })

  // ── Cancel ─────────────────────────────────────────────────────────────────
  cancelBtn.addEventListener('click', () => {
    if (abortCtrl) {
      abortCtrl.abort()
      abortCtrl = null
    }
    setStreaming(false)
    input.focus()
  })

  input.focus()
}
