import { useState, useRef, useEffect, useCallback, createElement } from 'react'
import { useShell } from '../providers/ShellProvider'
import { classifyIntent } from './IntentRouter'
import { searchApps } from './AppRegistry'
import { useVoice } from './useVoice'
import Settings from './Settings'

export default function Portal({ mode = 'panel' }) {
  const {
    layout, conversation, thinking, addMessage, setThinking,
    openWindow, chatOpen, setChat,
  } = useShell()
  const [query, setQuery] = useState('')
  const [suggestions, setSuggestions] = useState([])
  const [selectedIdx, setSelectedIdx] = useState(0)
  const inputRef = useRef(null)
  const scrollRef = useRef(null)

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
    fetch('/api/ai/history').then(r => r.ok ? r.json() : []).then(convs => {
      if (convs?.length > 0) {
        const latest = convs[0]
        if (latest.messages) {
          for (const msg of latest.messages) {
            addMessage(msg.role, msg.content)
          }
        }
      }
    }).catch(() => {})
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Global shortcut
  useEffect(() => {
    const handler = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault()
        setChat(true)
        setTimeout(() => inputRef.current?.focus(), 50)
      }
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

  const handleIntent = useCallback((input) => {
    const intent = classifyIntent(input)

    switch (intent.type) {
      case 'launch_service':
        addMessage('user', input)
        addMessage('system', `Opening ${intent.service.name}`)
        // Use gateway URL — auth-protected
        openWindow({ appId: intent.service.id, title: intent.service.name, url: `/app/${intent.service.id}/`, icon: intent.service.icon })
        break
      case 'service_suggestions':
        addMessage('user', input)
        addMessage('system', `Did you mean: ${intent.matches.map(m => m.name).join(', ')}?`)
        break
      case 'system':
        addMessage('user', input)
        if (intent.action === 'open_persona') {
          openWindow({ appId: 'settings', title: 'Settings', icon: '⚙', component: createElement(Settings) })
        } else if (intent.url) {
          openWindow({ appId: intent.action, title: intent.label, url: intent.url, icon: '⚙' })
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
        }).then(r => r.json()).then(result => {
          const output = result.output?.trim() || '(no output)'
          addMessage('system', `$ ${intent.value}\n${output}${result.exit_code !== 0 ? `\n[exit ${result.exit_code}]` : ''} (${result.duration})`)
          setThinking(false)
        }).catch(() => {
          addMessage('system', 'Backend not reachable.')
          setThinking(false)
        })
        break
      case 'mission':
      default:
        addMessage('user', input)
        setThinking(true)
        // Build message history for context
        const history = conversation
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
            const err = await res.json().catch(() => ({}))
            addMessage('assistant', err.error || 'AI provider not available. Open /settings to configure.')
            setThinking(false)
            return
          }
          const reader = res.body.getReader()
          const decoder = new TextDecoder()
          let full = ''
          const msgId = Date.now() + Math.random()

          while (true) {
            const { done, value } = await reader.read()
            if (done) break
            const text = decoder.decode(value)
            for (const line of text.split('\n')) {
              if (!line.startsWith('data: ')) continue
              try {
                const chunk = JSON.parse(line.slice(6))
                if (chunk.content) full += chunk.content
                if (chunk.done) break
              } catch {}
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
  }, [addMessage, openWindow, setThinking, conversation])

  // Listen for chat messages from launchpad
  useEffect(() => {
    const handler = (e) => {
      const text = e.detail
      if (text) handleIntent(text)
    }
    window.addEventListener('vulos:chat', handler)
    return () => window.removeEventListener('vulos:chat', handler)
  }, [handleIntent])

  // Parse AI response for <viewport> blocks → open as windows
  // Supports optional <script type="text/python"> for backend code
  const processAIResponse = useCallback(async (text) => {
    let remaining = text
    let opened = 0

    // --- Parse <os-action> blocks (safe actions only) ---
    const SAFE_EXEC_PREFIXES = ['ls ', 'cat ', 'head ', 'date', 'whoami', 'hostname', 'uptime', 'df ', 'free', 'uname']
    const actionRegex = /<os-action\s+([^/]*?)\/>/g
    let actionMatch
    while ((actionMatch = actionRegex.exec(text)) !== null) {
      remaining = remaining.replace(actionMatch[0], '').trim()
      const attrs = {}
      actionMatch[1].replace(/(\w+)="([^"]*)"/g, (_, k, v) => { attrs[k] = v })

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
          fetch('/api/os/notify', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title: (attrs.title || '').slice(0, 100), body: (attrs.body || '').slice(0, 500), level: ['info','warning','urgent'].includes(attrs.level) ? attrs.level : 'info' }) }).catch(() => {})
          break
        case 'energy-mode':
          if (['performance','balanced','saver'].includes(attrs.mode)) {
            fetch('/api/os/energy-mode', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode: attrs.mode }) }).catch(() => {})
          }
          break
        case 'exec': {
          const cmd = attrs.command || ''
          // Only allow safe read-only commands from AI
          if (cmd && SAFE_EXEC_PREFIXES.some(p => cmd.startsWith(p)) && !cmd.includes(';') && !cmd.includes('|') && !cmd.includes('`') && !cmd.includes('$(')) {
            fetch('/api/exec', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ command: cmd }) })
              .then(r => r.json()).then(d => addMessage('system', `$ ${cmd}\n${d.output || ''}`)).catch(() => {})
          } else {
            addMessage('system', `Blocked unsafe command: ${cmd}`)
          }
          break
        }
      }
    }

    // --- Parse <viewport> blocks ---
    const viewportRegex = /<viewport\s+title="([^"]*)">([\s\S]*?)<\/viewport>/g
    let match
    while ((match = viewportRegex.exec(text)) !== null) {
      const title = match[1]
      let content = match[2].trim()
      remaining = remaining.replace(match[0], '').trim()
      opened++

      const pyMatch = content.match(/<script\s+type="text\/python">([\s\S]*?)<\/script>/)
      let html = content
      let pythonCode = null
      let sandboxUrl = null

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
            const data = await res.json()
            sandboxUrl = data.url
            await new Promise(r => setTimeout(r, 500))
          }
        } catch {}
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
        _saveable: { title, html, python: pythonCode },
      })
    }

    if (remaining) {
      addMessage('assistant', remaining)
    } else if (opened > 0) {
      addMessage('system', `Opened ${opened} viewport${opened > 1 ? 's' : ''}`)
    }
  }, [addMessage, openWindow])

  const handleSubmit = useCallback((e) => {
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

  const handleKeyNav = useCallback((e) => {
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
          <div className="flex flex-col items-center justify-center h-full text-neutral-600 text-sm">
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
                ${i === selectedIdx ? 'bg-neutral-800/60 text-white' : 'text-neutral-400 hover:bg-neutral-800/30'}`}
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
          className="flex-1 bg-transparent text-white text-sm outline-none placeholder:text-neutral-600"
        />
        {voiceSupported && (
          <button
            type="button"
            onClick={listening ? stopVoice : startVoice}
            className={`w-7 h-7 flex items-center justify-center rounded-full transition-colors
              ${listening ? 'bg-red-600 text-white animate-pulse' : 'bg-neutral-800 text-neutral-500 hover:text-white'}`}
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

function Bubble({ message }) {
  const { role, text, timestamp } = message
  const time = timestamp ? new Date(timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''

  if (role === 'system') {
    return <div className="text-center"><span className="text-[11px] text-neutral-600">{text}</span></div>
  }
  if (role === 'user') {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] bg-neutral-800 rounded-2xl rounded-br-sm px-3.5 py-2">
          <p className="text-sm text-neutral-200 whitespace-pre-wrap">{text}</p>
          <span className="text-[10px] text-neutral-600 mt-0.5 block text-right">{time}</span>
        </div>
      </div>
    )
  }
  return (
    <div className="flex justify-start">
      <div className="max-w-[85%]">
        <p className="text-sm text-neutral-300 whitespace-pre-wrap">{text}</p>
        <span className="text-[10px] text-neutral-600 mt-0.5 block">{time}</span>
      </div>
    </div>
  )
}
