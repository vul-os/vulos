import { useState, useRef, useEffect, useCallback, createElement, lazy, Suspense, type FormEvent, type KeyboardEvent } from 'react'
import { useShell } from '../providers/ShellProvider'
import type { ShellMessage } from '../providers/ShellProvider'
import { classifyIntent } from './IntentRouter'
import { searchApps, type App } from './AppRegistry'
import { appFrameURLFor } from './AppOrigins'
import { useVoice } from './useVoice'
import Settings from './Settings'
import { BUILTIN_WINDOW_SIZE } from '../shell/builtinApps'
// Lazy-loaded so it stays in its own chunk (Launchpad also lazy-loads it).
const FileManager = lazy(() => import('../builtin/files/FileManager'))

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

interface AiEditRequest {
  id: string
  title?: string
  changeRequest: string
}

interface PortalProps {
  mode?: string
}

// eslint-disable-next-line @typescript-eslint/no-unused-vars
export default function Portal({ mode = 'panel' }: PortalProps) {
  const {
    layout, conversation, thinking, addMessage, setThinking,
    openWindow, chatOpen, setChat,
  } = useShell()
  const [query, setQuery] = useState('')
  const [suggestions, setSuggestions] = useState<App[]>([])
  const [selectedIdx, setSelectedIdx] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const scrollRef = useRef<HTMLDivElement>(null)

  const { listening, supported: voiceSupported, start: startVoice, stop: stopVoice } = useVoice((transcript) => {
    setQuery(transcript)
    // Auto-submit after voice input
    setTimeout(() => {
      if (transcript.trim()) handleIntent(transcript.trim())
      setQuery('')
    }, 100)
  })

  const isMobile = layout === 'mobile'

  // Load chat history from backend on mount
  useEffect(() => {
    fetch('/api/ai/history').then(r => r.ok ? r.json() : []).then((convs: unknown) => {
      if (Array.isArray(convs) && convs.length > 0) {
        const latest: unknown = convs[0]
        if (isRecord(latest) && Array.isArray(latest.messages)) {
          for (const msg of latest.messages) {
            if (isRecord(msg) && typeof msg.role === 'string' && typeof msg.content === 'string') {
              addMessage(msg.role, msg.content)
            }
          }
        }
      }
    }).catch(() => {})
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Global shortcut — Esc closes the chat panel.
  //
  // WAVE-12: ⌘K/Ctrl+K is now owned by the unified OS command palette
  // (src/shell/CommandPalette.jsx), NOT this chat panel — registering it here
  // too would double-bind the shortcut. The palette's "Ask" section routes to
  // the agentic assistant, superseding this panel as the ⌘K entry point; the
  // chat panel remains reachable via its dock button (toggleChat).
  useEffect(() => {
    const handler = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape' && chatOpen) setChat(false)
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [chatOpen, setChat])

  // Auto-focus
  useEffect(() => {
    if (chatOpen || isMobile) inputRef.current?.focus()
  }, [chatOpen, isMobile])

  // Auto-scroll
  useEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight
  }, [conversation, thinking])

  // Suggestions
  useEffect(() => {
    const q = query.trim()
    if (!q) { setSuggestions([]); return }
    setSuggestions(searchApps(q).slice(0, 4))
    setSelectedIdx(0)
  }, [query])

  // AI-06: Edit-with-AI — fetch current code, send AI chat with context, parse viewport,
  // POST /api/ai-apps/{id}/update, reopen viewport with new content.
  // Prefix: aiEdit_  (zero collision with existing identifiers)
  // Declared BEFORE handleIntent so it can be referenced in that callback's deps.
  const aiEdit_runEditWithAI = useCallback(async ({ id, title, changeRequest }: AiEditRequest) => {
    if (!id || !changeRequest) return

    addMessage('user', `Edit "${title || id}": ${changeRequest}`)
    setThinking(true)

    // Step 1: Fetch current html + python from the saved app
    let currentHTML = ''
    let currentPython = ''
    try {
      const [htmlRes, pyRes] = await Promise.allSettled([
        fetch(`/api/ai-apps/${encodeURIComponent(id)}/html`),
        fetch(`/api/ai-apps/${encodeURIComponent(id)}/python`),
      ])
      if (htmlRes.status === 'fulfilled' && htmlRes.value.ok) {
        currentHTML = await htmlRes.value.text()
      }
      if (pyRes.status === 'fulfilled' && pyRes.value.ok) {
        currentPython = await pyRes.value.text()
      }
    } catch {
      addMessage('system', 'Could not fetch app source code.')
      setThinking(false)
      return
    }

    // Step 2: Build AI prompt with current code as context
    const codeContext = [
      currentHTML ? `Current index.html:\n\`\`\`html\n${currentHTML}\n\`\`\`` : '',
      currentPython ? `Current server.py:\n\`\`\`python\n${currentPython}\n\`\`\`` : '',
    ].filter(Boolean).join('\n\n')

    const systemPrompt = `You are modifying an existing AI-generated app called "${title || id}".
Return the complete updated app wrapped in a <viewport title="${title || id}"> block, exactly like you would when creating a new app.
If the app has Python, include <script type="text/python"> inside the viewport.
Only output the viewport block — no explanations outside it.`

    const messages = [
      { role: 'system', content: systemPrompt },
      { role: 'user', content: `${codeContext}\n\nChange request: ${changeRequest}` },
    ]

    // Step 3: Stream AI response, parse <viewport>
    let full = ''
    try {
      const res = await fetch('/api/ai/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ messages, stream: true }),
      })
      if (!res.ok) {
        const err: unknown = await res.json().catch(() => ({}))
        addMessage('assistant', (isRecord(err) && typeof err.error === 'string' && err.error) || 'AI provider not available. Open /settings to configure.')
        setThinking(false)
        return
      }
      const reader = res.body?.getReader()
      if (!reader) {
        addMessage('assistant', 'AI provider not available. Open /settings to configure.')
        setThinking(false)
        return
      }
      const decoder = new TextDecoder()
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        const text = decoder.decode(value)
        for (const line of text.split('\n')) {
          if (!line.startsWith('data: ')) continue
          try {
            const chunk: unknown = JSON.parse(line.slice(6))
            if (isRecord(chunk)) {
              if (typeof chunk.content === 'string') full += chunk.content
              if (chunk.done) break
            }
          } catch { /* noop */ }
        }
      }
    } catch {
      addMessage('assistant', 'Could not reach AI backend. Check /settings.')
      setThinking(false)
      return
    }

    if (!full) {
      addMessage('assistant', 'No response from AI.')
      setThinking(false)
      return
    }

    // Step 4: Parse <viewport> from AI response to extract new html + python
    const AI6_viewportRe = /<viewport\s+title="([^"]*)">([\s\S]*?)<\/viewport>/
    const vpMatch = AI6_viewportRe.exec(full)
    if (!vpMatch) {
      // No viewport — fall back to normal processAIResponse so nothing is swallowed
      processAIResponse(full)
      setThinking(false)
      return
    }

    const newTitle = vpMatch[1] || title || id
    const vpContent = vpMatch[2].trim()
    const AI6_pyRe = /<script\s+type="text\/python">([\s\S]*?)<\/script>/
    const pyMatch = AI6_pyRe.exec(vpContent)
    const newPython = pyMatch ? pyMatch[1].trim() : ''
    const newHTML = pyMatch ? vpContent.replace(pyMatch[0], '').trim() : vpContent

    // Step 5: POST /api/ai-apps/{id}/update to persist changes
    try {
      const updateRes = await fetch(`/api/ai-apps/${encodeURIComponent(id)}/update`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ html: newHTML, python: newPython }),
      })
      if (!updateRes.ok) {
        const errData: unknown = await updateRes.json().catch(() => ({}))
        const errMsg = isRecord(errData) && typeof errData.error === 'string' ? errData.error : updateRes.status
        addMessage('system', `Failed to save update: ${errMsg}`)
        setThinking(false)
        return
      }
    } catch {
      addMessage('system', 'Could not save app update — backend unreachable.')
      setThinking(false)
      return
    }

    addMessage('system', `"${newTitle}" updated and reopened.`)

    // Step 6: Reopen the viewport with the new content (mirrors processAIResponse logic)
    let sandboxUrl: string | null = null
    if (newPython) {
      try {
        const sbRes = await fetch('/api/sandbox/run', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: `vp-edit-${id}-${Date.now()}`, code: newPython }),
        })
        if (sbRes.ok) {
          const sbData: unknown = await sbRes.json()
          sandboxUrl = isRecord(sbData) && typeof sbData.url === 'string' ? sbData.url : null
          await new Promise(r => setTimeout(r, 500))
        }
      } catch { /* noop */ }
    }

    let finalHTML = newHTML
    if (sandboxUrl) {
      const inject = `<script>const VULOS_SANDBOX_URL="${sandboxUrl}";</script>`
      finalHTML = finalHTML.includes('<head>')
        ? finalHTML.replace('<head>', '<head>' + inject)
        : inject + finalHTML
    }

    openWindow({
      appId: `ai-viewport-edit-${id}-${Date.now()}`,
      title: newTitle,
      icon: '◬',
      html: finalHTML,
      _saveable: { title: newTitle, html: finalHTML, python: newPython },
    })

    setThinking(false)
  // processAIResponse is used as fallback when AI returns no <viewport>.
  // It's intentionally NOT in the dep array: it's declared later in the
  // component (line 387) and putting it here was a TDZ violation — React
  // evaluates dep arrays synchronously during render, hitting the not-yet-
  // initialized `const` and crashing the entire mount with "Cannot access
  // processAIResponse before initialization" (silent black kiosk). The
  // closure captures it from lexical scope at call time, which is always
  // post-render and therefore initialized. Both functions are useCallback-
  // stable so invalidation isn't needed anyway.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [addMessage, setThinking, openWindow])

  const handleIntent = useCallback((input: string) => {
    // AI-06: "edit <app-id>: <change request>" shorthand — triggers Edit-with-AI directly.
    // Pattern: edit <id>[: <what to change>]
    const AI6_editRe = /^edit\s+([a-z0-9_-]+)(?:[:\s]+(.+))?$/i
    const AI6_editMatch = AI6_editRe.exec(input.trim())
    if (AI6_editMatch) {
      const AI6_appId = AI6_editMatch[1]
      const AI6_changeReq = (AI6_editMatch[2] || '').trim() || 'Improve this app.'
      aiEdit_runEditWithAI({ id: AI6_appId, title: AI6_appId, changeRequest: AI6_changeReq })
      setQuery('')
      return
    }

    const intent = classifyIntent(input)

    switch (intent.type) {
      case 'launch_service':
        addMessage('user', input)
        addMessage('system', `Opening ${intent.service.name}`)
        // NET-04: prefer subdomain URL ({app}--default.{host}) so the gateway
        // resolves via ParseSubdomain (profile="default").  The /app/{id}/
        // path-prefix fallback is kept as the url when the fetch fails or
        // subdomain DNS is unavailable; here we optimistically use the
        // subdomain form and rely on the gateway to fall back if needed.
        // The /app/{id}/ path-prefix fallback is preserved in Launchpad's
        // catch block and in the gateway itself.
        // ORIGIN-01: appFrameURLFor mints the app's own origin when this
        // deployment can serve one, else the /app/{id}/ path prefix.
        openWindow({ appId: intent.service.id, title: intent.service.name, url: appFrameURLFor(intent.service.id), icon: intent.service.icon })
        break
      case 'service_suggestions':
        addMessage('user', input)
        addMessage('system', `Did you mean: ${intent.matches.map(m => m.name).join(', ')}?`)
        break
      case 'system':
        addMessage('user', input)
        if (intent.action === 'open_files') {
          openWindow({ appId: 'files', title: 'File Explorer', icon: '⊡', component: createElement(Suspense, { fallback: null }, createElement(FileManager)) })
        } else if (intent.action === 'open_settings') {
          // Same initial size as the command-palette/Launchpad path (appId
          // differs — 'settings' here vs the builtin registry's 'persona' —
          // this is a pre-existing split this pass doesn't otherwise touch;
          // see BUILTIN_WINDOW_SIZE's comment for why Settings gets one).
          openWindow({ appId: 'settings', title: intent.label || 'Settings', icon: '⚙', component: createElement(Settings, { initialSection: intent.section }), size: BUILTIN_WINDOW_SIZE.persona })
        } else {
          addMessage('system', `${intent.label}`)
        }
        break
      case 'math':
        addMessage('user', input)
        try {
          const expr = intent.value.replace(/[×]/g, '*').replace(/[÷]/g, '/')
            .replace(/(\d+)%\s*of\s*(\d+)/gi, '($1/100)*$2')
            .replace(/(\d+)%\s*(?:tax|on)\s*(\d+)/gi, '($1/100)*$2')
          const result = Function('"use strict"; return (' + expr + ')')()
          addMessage('assistant', `= ${result}`)
        } catch {
          addMessage('assistant', `Couldn't calculate that. Try rephrasing.`)
        }
        break
      case 'command':
        addMessage('user', `/${intent.value}`)
        setThinking(true)
        fetch('/api/exec', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ command: intent.value }),
        }).then(r => r.json()).then((result: unknown) => {
          const output = isRecord(result) && typeof result.output === 'string' ? result.output.trim() : '(no output)'
          const exitCode = isRecord(result) && typeof result.exit_code === 'number' ? result.exit_code : 0
          const duration = isRecord(result) && typeof result.duration === 'string' ? result.duration : ''
          addMessage('system', `$ ${intent.value}\n${output}${exitCode !== 0 ? `\n[exit ${exitCode}]` : ''} (${duration})`)
          setThinking(false)
        }).catch(() => {
          addMessage('system', 'Backend not reachable.')
          setThinking(false)
        })
        break
      // AI6-empty-guard: EmptyIntent has no `value` field, unlike MissionIntent
      // below — classifyIntent('') can only be reached if a caller ever passed
      // blank input, which none currently do (handleSubmit/voice/suggestions all
      // guard on a trimmed non-empty string first), so this is a true no-op.
      case 'empty':
        break
      case 'mission':
      default: {
        addMessage('user', input)
        setThinking(true)
        // Build message history for context
        const history: { role: string; content: string }[] = conversation
          .filter(m => m.role === 'user' || m.role === 'assistant')
          .slice(-20)
          .map(m => ({ role: m.role, content: m.text }))
        history.push({ role: 'user', content: intent.value })

        // Stream from AI backend
        fetch('/api/ai/chat', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ messages: history, stream: true }),
        }).then(async (res) => {
          if (!res.ok) {
            const err: unknown = await res.json().catch(() => ({}))
            addMessage('assistant', (isRecord(err) && typeof err.error === 'string' && err.error) || 'AI provider not available. Open /settings to configure.')
            setThinking(false)
            return
          }
          const reader = res.body?.getReader()
          if (!reader) {
            addMessage('assistant', 'AI provider not available. Open /settings to configure.')
            setThinking(false)
            return
          }
          const decoder = new TextDecoder()
          let full = ''

          while (true) {
            const { done, value } = await reader.read()
            if (done) break
            const text = decoder.decode(value)
            for (const line of text.split('\n')) {
              if (!line.startsWith('data: ')) continue
              try {
                const chunk: unknown = JSON.parse(line.slice(6))
                if (isRecord(chunk)) {
                  if (typeof chunk.content === 'string') full += chunk.content
                  if (chunk.done) break
                }
              } catch { /* noop */ }
            }
          }
          processAIResponse(full || 'No response.')
          setThinking(false)
        }).catch(() => {
          addMessage('assistant', 'Could not reach AI backend. Check /settings.')
          setThinking(false)
        })
        break
      }
    }
  }, [addMessage, openWindow, setThinking, conversation, aiEdit_runEditWithAI])

  // Listen for chat messages from launchpad
  useEffect(() => {
    const handler = (e: Event) => {
      const text = (e as CustomEvent<string>).detail
      if (text) handleIntent(text)
    }
    window.addEventListener('vulos:chat', handler)
    return () => window.removeEventListener('vulos:chat', handler)
  }, [handleIntent])

  // Listen for vulos:aiEdit events dispatched by other UI (e.g. App Hub)
  // Event detail: { id, title, changeRequest }
  useEffect(() => {
    const AI6_handler = (e: Event) => {
      const detail = (e as CustomEvent<Partial<AiEditRequest>>).detail || {}
      const { id, title, changeRequest } = detail
      if (id && changeRequest) aiEdit_runEditWithAI({ id, title, changeRequest })
    }
    window.addEventListener('vulos:aiEdit', AI6_handler)
    return () => window.removeEventListener('vulos:aiEdit', AI6_handler)
  }, [aiEdit_runEditWithAI])

  // Parse AI response for <viewport> blocks → open as windows
  // Supports optional <script type="text/python"> for backend code
  const processAIResponse = useCallback(async (text: string) => {
    let remaining = text
    let opened = 0

    // --- Parse <os-action> blocks (safe actions only) ---
    const SAFE_EXEC_PREFIXES = ['ls ', 'cat ', 'head ', 'date', 'whoami', 'hostname', 'uptime', 'df ', 'free', 'uname']
    const actionRegex = /<os-action\s+([^/]*?)\/>/g
    let actionMatch: RegExpExecArray | null
    while ((actionMatch = actionRegex.exec(text)) !== null) {
      remaining = remaining.replace(actionMatch[0], '').trim()
      const attrs: Record<string, string> = {}
      actionMatch[1].replace(/(\w+)="([^"]*)"/g, (_, k: string, v: string) => { attrs[k] = v; return _ })

      switch (attrs.type) {
        case 'open-app':
          if (/^[a-z0-9_-]+$/.test(attrs.app_id || '')) {
            fetch('/api/os/open-app', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ app_id: attrs.app_id }) }).catch(() => {})
          }
          break
        case 'close-app':
          if (/^[a-z0-9_-]+$/.test(attrs.app_id || '')) {
            fetch('/api/os/close-app', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ app_id: attrs.app_id }) }).catch(() => {})
          }
          break
        case 'notify':
          fetch('/api/os/notify', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title: (attrs.title || '').slice(0, 100), body: (attrs.body || '').slice(0, 500), level: ['info', 'warning', 'urgent'].includes(attrs.level) ? attrs.level : 'info' }) }).catch(() => {})
          break
        case 'energy-mode':
          if (['performance', 'balanced', 'saver'].includes(attrs.mode)) {
            fetch('/api/os/energy-mode', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode: attrs.mode }) }).catch(() => {})
          }
          break
        case 'exec': {
          const cmd = attrs.command || ''
          // Only allow safe read-only commands from AI
          if (cmd && SAFE_EXEC_PREFIXES.some(p => cmd.startsWith(p)) && !cmd.includes(';') && !cmd.includes('|') && !cmd.includes('`') && !cmd.includes('$(')) {
            fetch('/api/exec', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ command: cmd }) })
              .then(r => r.json()).then((d: unknown) => addMessage('system', `$ ${cmd}\n${isRecord(d) && typeof d.output === 'string' ? d.output : ''}`)).catch(() => {})
          } else {
            addMessage('system', `Blocked unsafe command: ${cmd}`)
          }
          break
        }
      }
    }

    // --- Parse <viewport> blocks ---
    const viewportRegex = /<viewport\s+title="([^"]*)">([\s\S]*?)<\/viewport>/g
    let match: RegExpExecArray | null
    while ((match = viewportRegex.exec(text)) !== null) {
      const title = match[1]
      const content = match[2].trim()
      remaining = remaining.replace(match[0], '').trim()
      opened++

      const pyMatch = content.match(/<script\s+type="text\/python">([\s\S]*?)<\/script>/)
      let html = content
      let pythonCode: string | null = null
      let sandboxUrl: string | null = null

      if (pyMatch) {
        pythonCode = pyMatch[1].trim()
        html = content.replace(pyMatch[0], '').trim()
        try {
          const res = await fetch('/api/sandbox/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: `vp-${Date.now()}-${opened}`, code: pythonCode }),
          })
          if (res.ok) {
            const data: unknown = await res.json()
            sandboxUrl = isRecord(data) && typeof data.url === 'string' ? data.url : null
            await new Promise(r => setTimeout(r, 500))
          }
        } catch { /* noop */ }
      }

      if (sandboxUrl) {
        const inject = `<script>const VULOS_SANDBOX_URL="${sandboxUrl}";</script>`
        html = html.replace('<head>', '<head>' + inject)
        if (!html.includes('<head>')) html = inject + html
      }

      openWindow({
        appId: `ai-viewport-${Date.now()}-${opened}`,
        title,
        icon: '◬',
        html,
        _saveable: { title, html, python: pythonCode || undefined },
      })
    }

    if (remaining) {
      addMessage('assistant', remaining)
    } else if (opened > 0) {
      addMessage('system', `Opened ${opened} viewport${opened > 1 ? 's' : ''}`)
    }
  }, [addMessage, openWindow])

  const handleSubmit = useCallback((e: FormEvent) => {
    e.preventDefault()
    const input = query.trim()
    if (!input || thinking) return
    if (suggestions.length > 0 && selectedIdx < suggestions.length && suggestions[selectedIdx]) {
      handleIntent(suggestions[selectedIdx].name)
    } else {
      handleIntent(input)
    }
    setQuery('')
    setSuggestions([])
  }, [query, thinking, suggestions, selectedIdx, handleIntent])

  const handleKeyNav = useCallback((e: KeyboardEvent<HTMLInputElement>) => {
    if (suggestions.length === 0) return
    if (e.key === 'ArrowDown') { e.preventDefault(); setSelectedIdx(i => Math.min(i + 1, suggestions.length - 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setSelectedIdx(i => Math.max(i - 1, 0)) }
  }, [suggestions])

  // Desktop panel mode: only render when chatOpen
  if (!isMobile && !chatOpen) return null

  return (
    <div className={`flex flex-col bg-neutral-950/95 backdrop-blur-xl
      ${isMobile ? 'h-full' : 'h-full border-l border-neutral-800/50'}`}>

      {/* Header */}
      {!isMobile && (
        <div className="flex items-center justify-between px-4 py-2.5 border-b border-neutral-800/50 shrink-0">
          <span className="text-xs text-neutral-500">vula</span>
          <button onClick={() => setChat(false)} className="text-xs text-neutral-600 hover:text-neutral-400">✕</button>
        </div>
      )}

      {/* Conversation */}
      <div ref={scrollRef} className="flex-1 overflow-y-auto px-4 py-3 space-y-3">
        {conversation.length === 0 && (
          <div className="flex flex-col items-center justify-center h-full text-neutral-400 text-sm">
            <p>What do you need?</p>
          </div>
        )}
        {conversation.map((msg) => (
          <Bubble key={msg.id} message={msg} />
        ))}
        {thinking && (
          <div className="flex items-center gap-2 text-sm text-neutral-500">
            <span className="inline-flex gap-1">
              <span className="w-1.5 h-1.5 bg-neutral-600 rounded-full animate-pulse" />
              <span className="w-1.5 h-1.5 bg-neutral-600 rounded-full animate-pulse [animation-delay:150ms]" />
              <span className="w-1.5 h-1.5 bg-neutral-600 rounded-full animate-pulse [animation-delay:300ms]" />
            </span>
          </div>
        )}
      </div>

      {/* Suggestions */}
      {suggestions.length > 0 && (
        <div className="border-t border-neutral-800/30 shrink-0">
          {suggestions.map((app, i) => (
            <button
              key={app.id}
              onClick={() => { handleIntent(app.name); setQuery(''); setSuggestions([]) }}
              className={`w-full flex items-center gap-3 px-4 py-2 text-left transition-colors
                ${i === selectedIdx ? 'bg-neutral-800/60 text-[var(--text-primary)]' : 'text-neutral-400 hover:bg-neutral-800/30'}`}
            >
              <span className="text-sm w-5 text-center opacity-50">{app.icon}</span>
              <span className="text-sm">{app.name}</span>
              <span className="text-xs text-neutral-600 ml-auto">{app.description}</span>
            </button>
          ))}
        </div>
      )}

      {/* Input */}
      <form onSubmit={handleSubmit} className="flex items-center gap-3 px-4 py-3 border-t border-neutral-800/50 shrink-0">
        <div className={`w-2 h-2 rounded-full ${thinking ? 'bg-amber-500 animate-pulse' : 'bg-neutral-700'}`} />
        <input
          ref={inputRef}
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={handleKeyNav}
          placeholder="What do you need?"
          disabled={thinking}
          className="flex-1 bg-transparent text-[var(--text-primary)] text-sm outline-none placeholder:text-neutral-400"
        />
        {voiceSupported && (
          <button
            type="button"
            onClick={listening ? stopVoice : startVoice}
            className={`w-7 h-7 flex items-center justify-center rounded-full transition-colors
              ${listening ? 'bg-red-600 text-white animate-pulse' : 'bg-neutral-800 text-neutral-500 hover:text-[var(--text-primary)]'}`}
          >
            <svg viewBox="0 0 16 16" className="w-3.5 h-3.5">
              <path d="M8 1a2 2 0 012 2v4a2 2 0 11-4 0V3a2 2 0 012-2z" fill="currentColor" />
              <path d="M4 7a4 4 0 008 0M8 13v2" stroke="currentColor" strokeWidth="1.5" fill="none" />
            </svg>
          </button>
        )}
      </form>
    </div>
  )
}

function Bubble({ message }: { message: ShellMessage }) {
  const { role, text, timestamp } = message
  const time = timestamp ? new Date(timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''

  if (role === 'system') {
    return <div className="text-center"><span className="text-[12px] text-neutral-600">{text}</span></div>
  }
  if (role === 'user') {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] bg-neutral-800 rounded-2xl rounded-br-sm px-3.5 py-2">
          <p className="text-sm text-neutral-200 whitespace-pre-wrap">{text}</p>
          <span className="text-[12px] text-neutral-600 mt-0.5 block text-right">{time}</span>
        </div>
      </div>
    )
  }
  return (
    <div className="flex justify-start">
      <div className="max-w-[85%]">
        <p className="text-sm text-neutral-300 whitespace-pre-wrap">{text}</p>
        <span className="text-[12px] text-neutral-600 mt-0.5 block">{time}</span>
      </div>
    </div>
  )
}
