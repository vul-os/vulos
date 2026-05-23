/**
 * SmartBar — AIROT-05: Smart Summarise via the AI Router
 *
 * Adds a "Summarise" button to the browser toolbar. On click it extracts the
 * visible page text and POSTs to POST /api/ai/chat with a summarisation system
 * prompt. The SSE response is streamed into a slide-up panel below the toolbar.
 *
 * Panel footer: Copy | Share | Save to Notes | Cancel
 * The Cancel button aborts the in-flight fetch stream.
 * When AI Router is not configured the button is disabled with a config nudge.
 *
 * Props:
 *   pageText  {string}   — visible text extracted from the current page
 *   pageUrl   {string}   — current URL (used as context for notes title)
 *   onSaveToNotes {fn}   — callback({ title, body }) to create a new note
 *   aiStatus  {object|null} — result of GET /api/ai/status
 *                            null = not yet loaded, false = unavailable
 */

import { useState, useRef, useCallback } from 'react'

const SYSTEM_PROMPT =
  'You are a helpful web-page summariser. Summarise the supplied page text ' +
  'in 3–5 concise bullet points. Use plain text, no markdown headers.'

export default function SmartBar({ pageText, pageUrl, onSaveToNotes, aiStatus }) {
  const [open, setOpen] = useState(false)
  const [streaming, setStreaming] = useState(false)
  const [text, setText] = useState('')
  const [error, setError] = useState('')
  const abortRef = useRef(null)

  // Determine if AI Router is configured.
  // aiStatus === null → still loading; aiStatus.active_model → ready.
  const configured =
    aiStatus !== null &&
    aiStatus !== false &&
    Boolean(aiStatus?.active_model || aiStatus?.mode)

  const cancel = useCallback(() => {
    if (abortRef.current) {
      abortRef.current.abort()
      abortRef.current = null
    }
    setStreaming(false)
  }, [])

  const summarise = useCallback(async () => {
    if (!configured || !pageText) return

    cancel()
    setOpen(true)
    setStreaming(true)
    setText('')
    setError('')

    const ac = new AbortController()
    abortRef.current = ac

    try {
      const res = await fetch('/api/ai/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          model: aiStatus?.active_model || '',
          messages: [
            { role: 'system', content: SYSTEM_PROMPT },
            {
              role: 'user',
              content: `Page URL: ${pageUrl || 'unknown'}\n\n${pageText.slice(0, 8000)}`,
            },
          ],
          stream: true,
        }),
        signal: ac.signal,
      })

      if (!res.ok) {
        const j = await res.json().catch(() => ({}))
        throw new Error(j.error || `HTTP ${res.status}`)
      }

      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buf = ''

      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        // Parse SSE lines from buffer
        const lines = buf.split('\n')
        buf = lines.pop() // keep incomplete last line
        for (const line of lines) {
          if (!line.startsWith('data: ')) continue
          const payload = line.slice(6)
          if (payload === '[DONE]') break
          try {
            const ev = JSON.parse(payload)
            const delta =
              ev.choices?.[0]?.delta?.content ??
              ev.content ??
              ''
            if (delta) setText((prev) => prev + delta)
          } catch {
            // skip malformed chunk
          }
        }
      }
    } catch (err) {
      if (err.name !== 'AbortError') {
        setError(err.message || 'Summarisation failed')
      }
    } finally {
      abortRef.current = null
      setStreaming(false)
    }
  }, [configured, pageText, pageUrl, aiStatus, cancel])

  const copyToClipboard = useCallback(() => {
    if (text) navigator.clipboard.writeText(text).catch(() => {})
  }, [text])

  const share = useCallback(() => {
    if (navigator.share && text) {
      navigator.share({ title: 'Page Summary', text }).catch(() => {})
    } else {
      copyToClipboard()
    }
  }, [text, copyToClipboard])

  const saveToNotes = useCallback(() => {
    if (onSaveToNotes && text) {
      onSaveToNotes({
        title: `Summary: ${pageUrl || 'Web page'}`,
        body: text,
      })
    }
  }, [onSaveToNotes, text, pageUrl])

  return (
    <div className="smart-bar">
      {/* Summarise button */}
      {!configured ? (
        <button
          className="btn btn-disabled"
          title="Configure AI Router in Settings to enable Smart Summarise"
          disabled
        >
          Summarise
        </button>
      ) : (
        <button
          className={`btn${streaming ? ' btn-busy' : ''}`}
          onClick={summarise}
          disabled={streaming || !pageText}
          title="Summarise this page with AI"
        >
          {streaming ? 'Summarising…' : 'Summarise'}
        </button>
      )}

      {/* Slide-up summary panel */}
      {open && (
        <div className="summary-panel">
          <div className="summary-header">
            <span className="summary-label">AI Summary</span>
            <button
              className="close-btn"
              onClick={() => { cancel(); setOpen(false) }}
              title="Close"
            >
              ×
            </button>
          </div>

          <div className="summary-body">
            {error ? (
              <span className="summary-error">{error}</span>
            ) : text ? (
              <p className="summary-text">{text}</p>
            ) : streaming ? (
              <span className="summary-loading">Generating summary…</span>
            ) : (
              <span className="summary-empty">No summary yet.</span>
            )}
          </div>

          <div className="summary-footer">
            <button
              className="btn btn-sm"
              onClick={copyToClipboard}
              disabled={!text}
              title="Copy summary to clipboard"
            >
              Copy
            </button>
            <button
              className="btn btn-sm"
              onClick={share}
              disabled={!text}
              title="Share summary"
            >
              Share
            </button>
            <button
              className="btn btn-sm"
              onClick={saveToNotes}
              disabled={!text || !onSaveToNotes}
              title="Save summary to Notes"
            >
              Save to Notes
            </button>
            {streaming && (
              <button
                className="btn btn-sm btn-cancel"
                onClick={cancel}
                title="Cancel summarisation"
              >
                Cancel
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
