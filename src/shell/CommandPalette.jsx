// CommandPalette.jsx — the unified OS command palette (WAVE-12).
//
// ⌘K / Ctrl+K from anywhere opens one fast, keyboard-driven palette. It is the
// signature "fast, coherent home": jump to any app, search your mail, run a
// curated action, or ask the agentic assistant — without leaving the keyboard.
//
// Reconciliation with the pre-existing ⌘K:
//   • The old ⌘K opened the right-side CHAT PANEL (src/core/Portal.jsx → /api/ai).
//     That binding has been REMOVED from Portal; this palette now owns ⌘K. The
//     chat panel stays reachable via its dock button.
//   • The mail app (vulos-mail-ui) keeps its own app-scoped ⌘K — untouched.
//
// Sections, all backed by real state:
//   • Apps    — the shared AppRegistry (getApps), launched via the shared
//               launchApp() lane dispatch (same path as the Launchpad).
//   • Mail    — live, debounced GET /api/mail/search (lilmail /v1/search behind
//               the box). Enter opens the message in Mail.
//   • Actions — the commandRegistry seam (built-ins + app-contributed via
//               registerCommand). Fuzzy-ranked.
//   • Ask     — question-like queries (see askRouting) route to the agentic
//               assistant (/api/assistant/agent), rendering the answer or a
//               proposal→approve card inline (the wave-9 flow, /execute).
//
// Graceful degradation: Apps/Actions are pure client state and always work.
// Mail and Ask depend on the box; when unreachable they show an inline note
// instead of failing the palette.
import { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import { useShell } from '../providers/ShellProvider'
import { useTheme } from '../core/ThemeProvider'
import { useSovereignty } from '../core/useSovereignty'
import { getApps, getAppById } from '../core/AppRegistry'
import { launchApp } from './launchApp'
import { fuzzyRank } from '../core/fuzzy'
import { classifyAsk } from '../core/askRouting'
import { getCommands, subscribeCommands } from '../core/commandRegistry'

const RECENT_KEY = 'vulos-cmdk-recent'
const MAX_APPS = 6
const MAX_ACTIONS = 5
const MAX_MAIL = 5

function loadRecent() {
  try {
    const raw = localStorage.getItem(RECENT_KEY)
    const ids = raw ? JSON.parse(raw) : []
    return Array.isArray(ids) ? ids : []
  } catch { return [] }
}
function pushRecent(id) {
  try {
    const ids = loadRecent().filter(x => x !== id)
    ids.unshift(id)
    localStorage.setItem(RECENT_KEY, JSON.stringify(ids.slice(0, 8)))
  } catch { /* noop */ }
}

// A monospace keyboard chip, matching the shell's mono-accent system.
function Kbd({ children }) {
  return (
    <kbd className="font-mono text-[10px] leading-none px-1.5 py-1 rounded border border-neutral-700/70 bg-neutral-800/60 text-neutral-400">
      {children}
    </kbd>
  )
}

const SECTION_LABEL = { app: 'Apps', mail: 'Mail', action: 'Actions', ask: 'Ask' }

const PROPOSAL_VERB = {
  send_email: 'Send email',
  create_calendar_event: 'Create event',
  add_contact: 'Add contact',
  triage: 'Change mailbox',
}

export default function CommandPalette() {
  const {
    openWindow, windows, minimizeWindow,
  } = useShell()
  const theme = useTheme()
  const sovereignty = useSovereignty()

  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [selectedIdx, setSelectedIdx] = useState(0)
  const [recentIds, setRecentIds] = useState([])
  const [, forceCmdsTick] = useState(0)

  // Mail search state (debounced).
  const [mailResults, setMailResults] = useState([])
  const [mailState, setMailState] = useState('idle') // idle|loading|ok|error|empty

  // Ask (assistant) inline state.
  const [ask, setAsk] = useState(null) // { status, answer, proposal, proposalState }

  const inputRef = useRef(null)

  // Re-render when apps contribute commands via registerCommand.
  useEffect(() => subscribeCommands(() => forceCmdsTick(t => t + 1)), [])

  const close = useCallback(() => {
    setOpen(false)
    setQuery('')
    setSelectedIdx(0)
    setMailResults([])
    setMailState('idle')
    setAsk(null)
  }, [])

  // Global ⌘K / Ctrl+K to open, Esc to close.
  useEffect(() => {
    const handler = (e) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
        e.preventDefault()
        setOpen(v => {
          const next = !v
          if (next) { setRecentIds(loadRecent()); setTimeout(() => inputRef.current?.focus(), 30) }
          return next
        })
      } else if (e.key === 'Escape' && open) {
        e.preventDefault()
        e.stopPropagation()
        close()
      }
    }
    window.addEventListener('keydown', handler, true)
    return () => window.removeEventListener('keydown', handler, true)
  }, [open, close])

  // ── The command context handed to every action's run(ctx) ─────────────────
  const openApp = useCallback((appId, opts = {}) => {
    const app = getAppById(appId)
    if (!app) return
    pushRecent(appId)
    if ((opts.query || opts.hash) && app.url) {
      // Deep-link a web app by augmenting its URL (best-effort — the app honors
      // ?compose=1 / #… if it supports it, otherwise it just opens).
      let url = app.url
      if (opts.query) url += (url.includes('?') ? '&' : '?') + opts.query
      if (opts.hash) url += '#' + opts.hash
      openWindow({ appId: app.id, title: app.name, url, icon: app.icon })
    } else {
      launchApp(app, { openWindow })
    }
    close()
  }, [openWindow, close])

  const openUrl = useCallback((url, title, icon) => {
    openWindow({ appId: '_webview_' + Date.now(), title, url, icon })
    close()
  }, [openWindow, close])

  const ctx = useMemo(() => ({
    openApp,
    openUrl,
    openWindow,
    close,
    openHome: () => {
      // Reveal Home by minimizing every visible window (DesktopCanvas shows the
      // Home surface when no non-minimized window remains).
      windows.filter(w => !w.minimized).forEach(w => minimizeWindow(w.id))
      close()
    },
    openSettings: () => openApp('persona'),
    openTransparency: () => { sovereignty.openPanel?.(); close() },
    toggleTheme: () => { theme.toggle?.(); close() },
    lock: () => {
      // App.jsx's energy state locks on Ctrl+L (window keydown listener).
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'l', ctrlKey: true, bubbles: true }))
      close()
    },
  }), [openApp, openUrl, openWindow, close, windows, minimizeWindow, sovereignty, theme])

  // ── Derived, ranked results ───────────────────────────────────────────────
  const askInfo = useMemo(() => classifyAsk(query), [query])
  // Apps/Actions/Mail match against the prompt when the user explicitly forced
  // the Ask route (so a leading "?" / "ask " doesn't pollute name matching).
  const searchText = (askInfo.explicit ? askInfo.prompt : query).trim()

  const appRows = useMemo(() => {
    if (!searchText) {
      // Empty state → recent apps.
      return recentIds.map(getAppById).filter(Boolean).slice(0, MAX_APPS)
    }
    return fuzzyRank(searchText, getApps(),
      a => [a.name, ...(a.keywords || []), a.description || ''],
      { limit: MAX_APPS },
    ).map(r => r.item)
  }, [searchText, recentIds])

  const commands = getCommands(ctx)
  const actionRows = useMemo(() => {
    if (!searchText) return commands.slice(0, MAX_ACTIONS)
    return fuzzyRank(searchText, commands,
      c => [c.title, c.subtitle || '', ...(c.keywords || [])],
      { limit: MAX_ACTIONS },
    ).map(r => r.item)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchText, commands.length])

  // ── Mail search (debounced) ───────────────────────────────────────────────
  useEffect(() => {
    if (!open) return
    const q = searchText
    if (!q || q.length < 2 || askInfo.explicit) { setMailResults([]); setMailState('idle'); return }
    setMailState('loading')
    const ctrl = new AbortController()
    const t = setTimeout(() => {
      fetch(`/api/mail/search?q=${encodeURIComponent(q)}&limit=${MAX_MAIL}`, { credentials: 'include', signal: ctrl.signal })
        .then(r => r.ok ? r.json() : Promise.reject(new Error(String(r.status))))
        .then(data => {
          const msgs = data?.messages || []
          setMailResults(msgs)
          setMailState(msgs.length ? 'ok' : 'empty')
        })
        .catch(err => {
          if (err.name === 'AbortError') return
          setMailResults([])
          setMailState('error')
        })
    }, 220)
    return () => { clearTimeout(t); ctrl.abort() }
  }, [open, searchText, askInfo.explicit])

  // ── Flattened, navigable row list (order = display order) ─────────────────
  const rows = useMemo(() => {
    const out = []
    if (askInfo.ask) {
      out.push({ kind: 'ask', id: 'ask', prompt: askInfo.prompt })
    }
    for (const a of appRows) out.push({ kind: 'app', id: 'app:' + a.id, app: a })
    for (const m of mailResults) out.push({ kind: 'mail', id: 'mail:' + (m.uid || m.message_id || m.subject), msg: m })
    for (const c of actionRows) out.push({ kind: 'action', id: 'action:' + c.id, cmd: c })
    return out
  }, [askInfo, appRows, mailResults, actionRows])

  useEffect(() => { setSelectedIdx(0) }, [query])
  useEffect(() => { if (selectedIdx >= rows.length) setSelectedIdx(Math.max(0, rows.length - 1)) }, [rows.length, selectedIdx])

  // ── Ask (agentic assistant) ───────────────────────────────────────────────
  const runAsk = useCallback(async (prompt) => {
    const p = (prompt || '').trim()
    if (!p) return
    setAsk({ status: 'thinking', answer: '', proposal: null })
    try {
      const res = await fetch('/api/assistant/agent', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message: p, history: [] }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        setAsk({ status: 'error', answer: data.error || `Assistant unavailable (${res.status}).` })
        return
      }
      if (data.proposal) {
        setAsk({ status: 'proposal', answer: data.answer || '', proposal: data.proposal, proposalState: 'pending' })
      } else {
        setAsk({ status: 'answer', answer: data.answer || 'No response.' })
      }
    } catch {
      setAsk({ status: 'error', answer: 'Could not reach the assistant. Is the box online?' })
    }
  }, [])

  const approveProposal = useCallback(async () => {
    setAsk(a => a ? { ...a, proposalState: 'busy' } : a)
    try {
      const res = await fetch('/api/assistant/execute', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(ask?.proposal),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        setAsk(a => a ? { ...a, proposalState: 'pending', answer: (a.answer ? a.answer + '\n' : '') + (data.error || `Could not complete (${res.status}).`) } : a)
        return
      }
      setAsk(a => a ? { ...a, proposalState: 'done', answer: data.result || a.answer } : a)
    } catch {
      setAsk(a => a ? { ...a, proposalState: 'pending', answer: 'Could not reach the assistant to run the action.' } : a)
    }
  }, [ask])

  // ── Activate a row (Enter / click) ────────────────────────────────────────
  const activate = useCallback((row) => {
    if (!row) {
      // No row selected but the query is a question → ask anyway.
      if (askInfo.ask) runAsk(askInfo.prompt)
      return
    }
    switch (row.kind) {
      case 'ask': runAsk(row.prompt); break
      case 'app': pushRecent(row.app.id); launchApp(row.app, { openWindow }); close(); break
      case 'action': row.cmd.run(ctx); break
      case 'mail': {
        const m = row.msg
        const uid = m.uid || m.message_id || ''
        openUrl(uid ? `/app/lilmail/?uid=${encodeURIComponent(uid)}` : '/app/lilmail/', m.subject || 'Mail', '✉')
        break
      }
      default: break
    }
  }, [askInfo, runAsk, openWindow, close, ctx, openUrl])

  // ── Keyboard nav ──────────────────────────────────────────────────────────
  const onKeyDown = useCallback((e) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSelectedIdx(i => Math.min(i + 1, rows.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSelectedIdx(i => Math.max(i - 1, 0))
    } else if (e.key === 'Tab') {
      // Tab jumps to the first row of the NEXT section (Shift+Tab: previous).
      e.preventDefault()
      if (rows.length === 0) return
      const curKind = rows[selectedIdx]?.kind
      if (e.shiftKey) {
        for (let i = selectedIdx - 1; i >= 0; i--) {
          if (rows[i].kind !== curKind) {
            const k = rows[i].kind
            let j = i
            while (j - 1 >= 0 && rows[j - 1].kind === k) j--
            setSelectedIdx(j); return
          }
        }
      } else {
        for (let i = selectedIdx + 1; i < rows.length; i++) {
          if (rows[i].kind !== curKind) { setSelectedIdx(i); return }
        }
      }
    } else if (e.key === 'Enter') {
      e.preventDefault()
      activate(rows[selectedIdx])
    }
  }, [rows, selectedIdx, activate])

  if (!open) return null

  const showAskSection = askInfo.ask
  const hasResults = rows.length > 0

  return (
    <div
      className="fixed inset-0 z-[300] flex items-start justify-center pt-[12vh] px-4 bg-black/50 backdrop-blur-sm animate-[fadeIn_0.12s_ease-out]"
      onMouseDown={(e) => { if (e.target === e.currentTarget) close() }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        className="w-full max-w-2xl overflow-hidden rounded-2xl border border-neutral-700/60 bg-neutral-900/95 backdrop-blur-xl shadow-2xl shadow-black/60"
        onMouseDown={(e) => e.stopPropagation()}
      >
        {/* Input row */}
        <div className="flex items-center gap-3 px-4 py-3.5 border-b border-neutral-800/70">
          <span className="text-neutral-500 font-mono text-sm select-none">⌘K</span>
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Search apps, mail, actions — or ask a question…"
            className="flex-1 bg-transparent text-[14px] text-neutral-100 placeholder-neutral-600 outline-none"
            autoComplete="off" autoCorrect="off" autoCapitalize="off" spellCheck={false}
          />
          <Kbd>esc</Kbd>
        </div>

        {/* Results */}
        <div className="max-h-[52vh] overflow-y-auto py-1.5">
          {!searchText && !showAskSection && (
            <div className="px-4 pt-1 pb-0.5 text-[10px] text-neutral-600">
              {appRows.length ? 'Jump back in, or run an action.' : 'Start typing, or run an action.'}
            </div>
          )}

          {renderSections({
            rows, selectedIdx, setSelectedIdx, activate, mailState, showAskSection,
          })}

          {!hasResults && searchText && (
            <div className="px-4 py-6 text-center text-[13px] text-neutral-600">
              No matches for “{searchText}”.
              <div className="mt-1 text-[11px] text-neutral-700">Prefix with <span className="font-mono">?</span> to ask the assistant.</div>
            </div>
          )}
        </div>

        {/* Inline Ask result */}
        {ask && (
          <AskResult ask={ask} onApprove={approveProposal} onReject={() => setAsk(a => a ? { ...a, proposalState: 'rejected' } : a)} />
        )}

        {/* Footer legend */}
        <div className="flex items-center gap-4 px-4 py-2 border-t border-neutral-800/70 text-[10.5px] text-neutral-600">
          <span className="flex items-center gap-1.5"><Kbd>↑</Kbd><Kbd>↓</Kbd> navigate</span>
          <span className="flex items-center gap-1.5"><Kbd>⏎</Kbd> open</span>
          <span className="flex items-center gap-1.5"><Kbd>tab</Kbd> section</span>
          <span className="ml-auto flex items-center gap-1.5"><Kbd>?</Kbd> ask</span>
        </div>
      </div>
    </div>
  )
}

// Render the flat row list grouped under section headers, in order.
function renderSections({ rows, selectedIdx, setSelectedIdx, activate, mailState, showAskSection }) {
  const out = []
  let lastKind = null
  rows.forEach((row, i) => {
    if (row.kind !== lastKind) {
      lastKind = row.kind
      out.push(
        <div key={'h:' + row.kind} className="px-4 pt-2 pb-1 flex items-center gap-2">
          <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-600">{SECTION_LABEL[row.kind]}</span>
          {row.kind === 'mail' && mailState === 'loading' && <span className="text-[10px] text-neutral-700">searching…</span>}
        </div>
      )
    }
    out.push(
      <Row key={row.id} row={row} active={i === selectedIdx} onHover={() => setSelectedIdx(i)} onClick={() => activate(row)} />
    )
  })
  // Mail degradation note (only when the user is searching and mail failed).
  if (mailState === 'error' && !showAskSection) {
    out.push(
      <div key="mail-err" className="px-4 py-1.5 text-[11px] text-neutral-600">
        <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-700">Mail</span>
        <span className="ml-2">search unavailable — is Mail connected?</span>
      </div>
    )
  }
  return out
}

function Row({ row, active, onHover, onClick }) {
  const base = `w-full flex items-center gap-3 px-4 py-2 text-left transition-colors cursor-pointer ${
    active ? 'bg-neutral-800/70' : 'hover:bg-neutral-800/30'
  }`
  if (row.kind === 'app') {
    const a = row.app
    return (
      <div className={base} onMouseMove={onHover} onClick={onClick}>
        <span className="w-5 text-center text-neutral-400 shrink-0">{a.icon}</span>
        <span className={`text-[13px] ${active ? 'text-neutral-100' : 'text-neutral-300'}`}>{a.name}</span>
        <span className="ml-auto text-[11px] text-neutral-600 truncate max-w-[45%]">{a.description}</span>
      </div>
    )
  }
  if (row.kind === 'action') {
    const c = row.cmd
    return (
      <div className={base} onMouseMove={onHover} onClick={onClick}>
        <span className="w-5 text-center text-neutral-400 shrink-0">{c.icon || '▸'}</span>
        <span className={`text-[13px] ${active ? 'text-neutral-100' : 'text-neutral-300'}`}>{c.title}</span>
        {c.subtitle && <span className="ml-auto text-[11px] font-mono text-neutral-600 truncate max-w-[45%]">{c.subtitle}</span>}
      </div>
    )
  }
  if (row.kind === 'mail') {
    const m = row.msg
    const sender = m.from_name || m.from || ''
    return (
      <div className={base} onMouseMove={onHover} onClick={onClick}>
        <span className="w-5 text-center text-neutral-500 shrink-0">✉</span>
        <span className="min-w-0 flex-1">
          <span className={`text-[13px] block truncate ${active ? 'text-neutral-100' : 'text-neutral-300'}`}>{m.subject || '(no subject)'}</span>
          <span className="text-[11px] text-neutral-600 block truncate">{sender}{m.preview ? ` — ${m.preview}` : ''}</span>
        </span>
        {m.unread && <span className="w-1.5 h-1.5 rounded-full bg-blue-500 shrink-0" />}
      </div>
    )
  }
  if (row.kind === 'ask') {
    return (
      <div className={base} onMouseMove={onHover} onClick={onClick}>
        <span className="w-5 text-center text-neutral-400 shrink-0">✦</span>
        <span className={`text-[13px] ${active ? 'text-neutral-100' : 'text-neutral-300'}`}>
          Ask the assistant{row.prompt ? <span className="text-neutral-500"> — “{row.prompt}”</span> : ''}
        </span>
        <span className="ml-auto"><span className="font-mono text-[10px] px-1.5 py-1 rounded border border-neutral-700/70 bg-neutral-800/60 text-neutral-500">⏎</span></span>
      </div>
    )
  }
  return null
}

// Inline assistant answer / proposal — a compact reuse of the wave-9 flow.
function AskResult({ ask, onApprove, onReject }) {
  return (
    <div className="border-t border-neutral-800/70 px-4 py-3 bg-neutral-950/40 max-h-[30vh] overflow-y-auto">
      <div className="flex items-center gap-2 mb-1.5">
        <span className="text-neutral-400">✦</span>
        <span className="text-[11px] font-mono uppercase tracking-wider text-neutral-600">Assistant</span>
        {ask.status === 'thinking' && (
          <span className="inline-flex gap-1 ml-1">
            <span className="w-1.5 h-1.5 bg-neutral-600 rounded-full animate-pulse" />
            <span className="w-1.5 h-1.5 bg-neutral-600 rounded-full animate-pulse [animation-delay:150ms]" />
            <span className="w-1.5 h-1.5 bg-neutral-600 rounded-full animate-pulse [animation-delay:300ms]" />
          </span>
        )}
      </div>
      {ask.answer && (
        <div className={`text-[13px] whitespace-pre-wrap leading-relaxed ${ask.status === 'error' ? 'text-red-300' : 'text-neutral-200'}`}>
          {ask.answer}
        </div>
      )}
      {ask.status === 'proposal' && ask.proposal && (
        <div className="mt-2.5 rounded-xl border border-amber-500/30 bg-amber-950/20 px-3 py-2.5">
          <div className="flex items-center gap-2 mb-1.5">
            <span className="inline-block w-2 h-2 rounded-full bg-amber-400" />
            <span className="text-amber-300 font-medium text-[12px]">Needs your approval · {PROPOSAL_VERB[ask.proposal.tool] || 'Action'}</span>
          </div>
          <div className="text-neutral-200 text-[12.5px] leading-relaxed mb-1">{ask.proposal.summary}</div>
          {(ask.proposal.args?.body || ask.proposal.args?.notes) && (
            <div className="text-[12px] text-neutral-400 whitespace-pre-wrap bg-neutral-900/50 rounded-lg px-2.5 py-2 mt-1.5 max-h-32 overflow-y-auto">
              {ask.proposal.args.body || ask.proposal.args.notes}
            </div>
          )}
          {ask.proposalState === 'done' ? (
            <div className="text-[12px] text-emerald-400 mt-2">✓ Approved and executed.</div>
          ) : ask.proposalState === 'rejected' ? (
            <div className="text-[12px] text-neutral-500 mt-2">Rejected — nothing was done.</div>
          ) : (
            <div className="flex gap-2 mt-2.5">
              <button type="button" disabled={ask.proposalState === 'busy'} onClick={onApprove}
                className="text-[12px] px-3 py-1.5 rounded-lg bg-emerald-600 text-white hover:bg-emerald-500 transition-colors disabled:opacity-50">
                {ask.proposalState === 'busy' ? 'Working…' : 'Approve'}
              </button>
              <button type="button" disabled={ask.proposalState === 'busy'} onClick={onReject}
                className="text-[12px] px-3 py-1.5 rounded-lg bg-neutral-800 text-neutral-300 hover:bg-neutral-700 transition-colors disabled:opacity-50">
                Reject
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
