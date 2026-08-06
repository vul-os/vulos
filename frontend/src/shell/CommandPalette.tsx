// CommandPalette.tsx — the unified OS command palette (WAVE-12).
//
// ⌘K / Ctrl+K from anywhere opens one fast, keyboard-driven palette. It is the
// signature "fast, coherent home": jump to any app, search your mail, run a
// curated action, or ask the agentic assistant — without leaving the keyboard.
//
// Reconciliation with the pre-existing ⌘K:
//   • The old ⌘K opened the right-side CHAT PANEL (src/core/Portal.jsx → /api/ai).
//     That binding has been REMOVED from Portal; this palette now owns ⌘K. The
//     chat panel stays reachable via its dock button.
//   • The lilmail app keeps its own app-scoped ⌘K — untouched.
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
import {
  useState, useEffect, useRef, useCallback, useMemo,
  type ReactNode, type ReactElement, type RefObject, type Dispatch, type SetStateAction,
  type KeyboardEvent as ReactKeyboardEvent,
} from 'react'
import { useFocusTrap } from './useFocusTrap'
import { useNarrow } from './useNarrow'
import './shell-chrome.css'
import { useShell } from '../providers/ShellProvider'
import { useTheme } from '../core/ThemeProvider'
import { useSovereignty } from '../core/useSovereignty'
import { getApps, getAppById, type App } from '../core/AppRegistry'
import { launchApp } from './launchApp'
import { fuzzyRank } from '../core/fuzzy'
import { classifyAsk } from '../core/askRouting'
import { runAgentTurn, type AgentProposal, type AgentStatusEvent } from '../core/agentStream'
import { getCommands, subscribeCommands, type Command } from '../core/commandRegistry'
import { setPendingSettingsSection } from '../core/settingsNav'
import { setPendingLaunchQuery } from '../core/launchParams'
import { isBuiltinComponent } from './builtinApps'
// Shared confirmation-gate card — keeps the inline Ask proposal identical to the
// Assistant panel + Home (tokenised colours, structured "what will happen").
import { ProposalCard } from '../builtin/assistant/ProposalCard'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

const RECENT_KEY = 'vulos-cmdk-recent'
const MAX_APPS = 6
const MAX_ACTIONS = 5
const MAX_MAIL = 5

function loadRecent(): string[] {
  try {
    const raw = localStorage.getItem(RECENT_KEY)
    const ids: unknown = raw ? JSON.parse(raw) : []
    return Array.isArray(ids) ? ids.filter((x): x is string => typeof x === 'string') : []
  } catch { return [] }
}
function pushRecent(id: string): void {
  try {
    const ids = loadRecent().filter(x => x !== id)
    ids.unshift(id)
    localStorage.setItem(RECENT_KEY, JSON.stringify(ids.slice(0, 8)))
  } catch { /* noop */ }
}

// A monospace keyboard chip, matching the shell's mono-accent system.
// Token-driven (vshell-kbd) so it stays legible in both light and dark themes.
function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="vshell-kbd">{children}</kbd>
}

// ── Mail search result shape ─────────────────────────────────────────────────
// Narrowed from `unknown` JSON off GET /api/mail/search — a trust boundary, so
// every field is checked rather than assumed (mirrors AppRegistry.ts's
// toInstalledApp/toAI5App narrowing pattern).
interface MailMessage {
  uid?: string
  message_id?: string
  subject?: string
  from_name?: string
  from?: string
  preview?: string
  unread?: boolean
}

function toMailMessage(x: Record<string, unknown>): MailMessage {
  return {
    uid: typeof x.uid === 'string' ? x.uid : undefined,
    message_id: typeof x.message_id === 'string' ? x.message_id : undefined,
    subject: typeof x.subject === 'string' ? x.subject : undefined,
    from_name: typeof x.from_name === 'string' ? x.from_name : undefined,
    from: typeof x.from === 'string' ? x.from : undefined,
    preview: typeof x.preview === 'string' ? x.preview : undefined,
    unread: typeof x.unread === 'boolean' ? x.unread : undefined,
  }
}

type MailState = 'idle' | 'loading' | 'ok' | 'error' | 'empty'

// ── The inline Ask (assistant) card state ────────────────────────────────────
interface AskState {
  status: 'thinking' | 'answer' | 'proposal' | 'error'
  answer: string
  proposal?: AgentProposal | null
  proposalState?: 'pending' | 'busy' | 'done' | 'rejected'
  statusLine?: string
}

// ── The flattened, navigable row list ────────────────────────────────────────
type RowKind = 'app' | 'mail' | 'action' | 'ask'

interface AppRow { kind: 'app'; id: string; app: App }
interface MailRow { kind: 'mail'; id: string; msg: MailMessage }
interface ActionRow { kind: 'action'; id: string; cmd: Command }
interface AskRow { kind: 'ask'; id: string; prompt: string }
type PaletteRow = AppRow | MailRow | ActionRow | AskRow

const SECTION_LABEL: Record<RowKind, string> = { app: 'Apps', mail: 'Mail', action: 'Actions', ask: 'Ask' }

export default function CommandPalette() {
  const {
    openWindow, windows, minimizeWindow,
  } = useShell()
  const theme = useTheme()
  const sovereignty = useSovereignty()

  const narrow = useNarrow(640)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [selectedIdx, setSelectedIdx] = useState(0)
  const [recentIds, setRecentIds] = useState<string[]>([])
  const [, forceCmdsTick] = useState(0)

  // Mail search state (debounced).
  const [mailResults, setMailResults] = useState<MailMessage[]>([])
  const [mailState, setMailState] = useState<MailState>('idle')

  // Ask (assistant) inline state.
  const [ask, setAsk] = useState<AskState | null>(null)

  const inputRef = useRef<HTMLInputElement>(null)
  const rowRefs = useRef<(HTMLDivElement | null)[]>([])
  // Aborts an in-flight streaming Ask turn when the palette closes/unmounts, so
  // the SSE fetch is torn down instead of streaming into a dismissed card.
  const askCtl = useRef<AbortController | null>(null)
  // A11Y: trap focus inside the dialog while open + restore to the opener on close.
  const trapRef = useFocusTrap(open)

  // Re-render when apps contribute commands via registerCommand.
  useEffect(() => subscribeCommands(() => forceCmdsTick(t => t + 1)), [])

  // Abort any in-flight Ask stream on unmount so it does not leak / write to
  // dead state after the palette is gone.
  useEffect(() => () => { askCtl.current?.abort() }, [])

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
    const handler = (e: KeyboardEvent) => {
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
  const openApp = useCallback((appId: string, opts: Record<string, unknown> = {}) => {
    const app = getAppById(appId)
    if (!app) return
    pushRecent(appId)
    const optQuery = typeof opts.query === 'string' ? opts.query : undefined
    const optHash = typeof opts.hash === 'string' ? opts.hash : undefined
    if ((optQuery || optHash) && app.url) {
      // Deep-link a web app by augmenting its URL (best-effort — the app honors
      // ?compose=1 / #… if it supports it, otherwise it just opens).
      let url = app.url
      if (optQuery) url += (url.includes('?') ? '&' : '?') + optQuery
      if (optHash) url += '#' + optHash
      openWindow({ appId: app.id, title: app.name, url, icon: app.icon })
    } else {
      // Deep-link a BUILTIN app: it has no URL, so stash the query for its
      // factory to consume on mount (e.g. Calendar honours ?action=new by
      // pre-opening the new-event editor). Must be set BEFORE launchApp, which
      // instantiates the builtin component synchronously.
      if (optQuery && isBuiltinComponent(app.id)) setPendingLaunchQuery(app.id, optQuery)
      launchApp(app, { openWindow })
    }
    close()
  }, [openWindow, close])

  const openUrl = useCallback((url: string, title?: string, icon?: string) => {
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
    openSettings: (section?: string) => { setPendingSettingsSection(section); openApp('persona') },
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

  const appRows = useMemo<App[]>(() => {
    if (!searchText) {
      // Empty state → recent apps.
      return recentIds.map(getAppById).filter((a): a is App => Boolean(a)).slice(0, MAX_APPS)
    }
    return fuzzyRank(searchText, getApps(),
      a => [a.name, ...(a.keywords || []), a.description || ''],
      { limit: MAX_APPS },
    ).map(r => r.item)
  }, [searchText, recentIds])

  const commands = getCommands(ctx)
  const actionRows = useMemo<Command[]>(() => {
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
        .then((data: unknown) => {
          const msgs = isRecord(data) && Array.isArray(data.messages)
            ? data.messages.filter(isRecord).map(toMailMessage)
            : []
          setMailResults(msgs)
          setMailState(msgs.length ? 'ok' : 'empty')
        })
        .catch(err => {
          if (isRecord(err) && err.name === 'AbortError') return
          setMailResults([])
          setMailState('error')
        })
    }, 220)
    return () => { clearTimeout(t); ctrl.abort() }
  }, [open, searchText, askInfo.explicit])

  // ── Flattened, navigable row list (order = display order) ─────────────────
  const rows = useMemo<PaletteRow[]>(() => {
    const out: PaletteRow[] = []
    if (askInfo.ask) {
      out.push({ kind: 'ask', id: 'ask', prompt: askInfo.prompt })
    }
    for (const a of appRows) out.push({ kind: 'app', id: 'app:' + a.id, app: a })
    for (const m of mailResults) out.push({ kind: 'mail', id: 'mail:' + (m.uid || m.message_id || m.subject || ''), msg: m })
    for (const c of actionRows) out.push({ kind: 'action', id: 'action:' + c.id, cmd: c })
    return out
  }, [askInfo, appRows, mailResults, actionRows])

  useEffect(() => { setSelectedIdx(0) }, [query])
  useEffect(() => { if (selectedIdx >= rows.length) setSelectedIdx(Math.max(0, rows.length - 1)) }, [rows.length, selectedIdx])

  // Keep the active row visible as arrow-key navigation moves the selection —
  // long result lists could otherwise scroll the highlight out of view.
  useEffect(() => {
    const el = rowRefs.current[selectedIdx]
    if (el?.scrollIntoView) el.scrollIntoView({ block: 'nearest' })
  }, [selectedIdx, rows.length])

  // ── Ask (agentic assistant) ───────────────────────────────────────────────
  // STREAMED (wave-17): the answer streams token-by-token into the inline card;
  // a mutating action still arrives as a PROPOSAL (Approve → /execute). Falls
  // back to the non-streaming /agent if streaming can't be established.
  const runAsk = useCallback(async (prompt: string) => {
    const p = (prompt || '').trim()
    if (!p) return
    setAsk({ status: 'thinking', answer: '', proposal: null })
    const ctl = new AbortController()
    askCtl.current = ctl
    try {
      const result = await runAgentTurn({
        message: p,
        history: [],
        signal: ctl.signal,
        onToken: (_delta: string, full: string) => setAsk({ status: 'answer', answer: full }),
        onStatus: (ev: AgentStatusEvent) => setAsk(a => (a && a.status === 'thinking' ? { ...a, statusLine: ev.content } : a)),
        onProposal: (proposal: AgentProposal) => setAsk({ status: 'proposal', answer: '', proposal, proposalState: 'pending' }),
      })
      if (result.error) {
        setAsk({ status: 'error', answer: result.error })
      } else if (result.proposal) {
        setAsk({ status: 'proposal', answer: result.answer || '', proposal: result.proposal, proposalState: 'pending' })
      } else {
        setAsk({ status: 'answer', answer: result.answer || 'No response.' })
      }
    } catch (err) {
      // Aborted by unmount/close: the card is gone — do not write dead state.
      if (!(isRecord(err) && err.name === 'AbortError')) setAsk({ status: 'error', answer: 'Could not reach the assistant. Is the box online?' })
    } finally {
      if (askCtl.current === ctl) askCtl.current = null
    }
  }, [])

  const approveProposal = useCallback(async () => {
    setAsk(a => a ? { ...a, proposalState: 'busy' } : a)
    try {
      // Send ONLY the opaque proposal id; the server runs its stored args.
      const res = await fetch('/api/assistant/execute', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: ask?.proposal?.id }),
      })
      const data: unknown = await res.json().catch(() => ({}))
      const d = isRecord(data) ? data : {}
      if (!res.ok) {
        const errMsg = typeof d.error === 'string' ? d.error : `Could not complete (${res.status}).`
        setAsk(a => a ? { ...a, proposalState: 'pending', answer: (a.answer ? a.answer + '\n' : '') + errMsg } : a)
        return
      }
      setAsk(a => a ? { ...a, proposalState: 'done', answer: typeof d.result === 'string' ? d.result : a.answer } : a)
    } catch {
      setAsk(a => a ? { ...a, proposalState: 'pending', answer: 'Could not reach the assistant to run the action.' } : a)
    }
  }, [ask])

  // ── Activate a row (Enter / click) ────────────────────────────────────────
  const activate = useCallback((row: PaletteRow | undefined) => {
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

  // A pending proposal is showing its Approve/Reject buttons.
  const proposalPending = ask?.status === 'proposal' && ask?.proposalState === 'pending'

  // ── Keyboard nav ──────────────────────────────────────────────────────────
  const onKeyDown = useCallback((e: ReactKeyboardEvent<HTMLInputElement>) => {
    // When a proposal awaits approval, offer explicit Y/N shortcuts and let Tab
    // fall through to the focus trap so the Approve/Reject buttons are reachable
    // (Tab is otherwise captured for section-cycling — see below).
    if (proposalPending) {
      if (e.key === 'y' || e.key === 'Y') { e.preventDefault(); approveProposal(); return }
      if (e.key === 'n' || e.key === 'N') { e.preventDefault(); setAsk(a => a ? { ...a, proposalState: 'rejected' } : a); return }
      if (e.key === 'Tab') return // don't preventDefault → focus trap cycles to the buttons
    }
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
  }, [rows, selectedIdx, activate, proposalPending, approveProposal])

  if (!open) return null

  const showAskSection = askInfo.ask
  const hasResults = rows.length > 0

  return (
    <div
      className={`vshell-scrim fixed inset-0 z-[300] flex justify-center ${
        narrow ? 'items-end p-0' : 'items-start pt-[11vh] px-4'
      }`}
      onMouseDown={(e) => { if (e.target === e.currentTarget) close() }}
    >
      <div
        ref={trapRef}
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        className={`vshell-surface w-full max-w-2xl flex flex-col overflow-hidden ${
          narrow
            ? 'vshell-sheet rounded-t-2xl max-h-[88vh] pb-[env(safe-area-inset-bottom)]'
            : 'vshell-pop rounded-2xl'
        }`}
        onMouseDown={(e) => e.stopPropagation()}
      >
        {/* Mobile grab handle */}
        {narrow && (
          <div className="flex justify-center pt-2 pb-1 shrink-0" aria-hidden="true">
            <span className="w-9 h-1 rounded-full" style={{ background: 'var(--border-emphasis)' }} />
          </div>
        )}

        {/* Input row */}
        <div className="vshell-border-b flex items-center gap-3 px-4 py-3.5 shrink-0">
          <svg viewBox="0 0 16 16" className="w-4 h-4 shrink-0" fill="none" stroke="currentColor" strokeWidth="1.5" style={{ color: 'var(--text-faint)' }}>
            <circle cx="7" cy="7" r="5" />
            <path d="M11 11l3.5 3.5" strokeLinecap="round" />
          </svg>
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Search apps, mail, actions — or ask a question…"
            className="flex-1 min-w-0 bg-transparent text-[14px] outline-none"
            style={{ color: 'var(--text-primary)' }}
            autoComplete="off" autoCorrect="off" autoCapitalize="off" spellCheck={false}
          />
          {narrow ? (
            <button onClick={close} aria-label="Close" className="vshell-kbd focus-primary">esc</button>
          ) : (
            <Kbd>esc</Kbd>
          )}
        </div>

        {/* Results */}
        <div className={`flex-1 overflow-y-auto py-1.5 ${narrow ? '' : 'max-h-[52vh]'}`}>
          {!searchText && !showAskSection && (
            <div className="px-4 pt-1 pb-0.5 text-[12px]" style={{ color: 'var(--text-faint)' }}>
              {appRows.length ? 'Jump back in, or run an action.' : 'Start typing, or run an action.'}
            </div>
          )}

          {renderSections({
            rows, selectedIdx, setSelectedIdx, activate, mailState, showAskSection, rowRefs,
          })}

          {!hasResults && searchText && (
            <div className="px-4 py-6 text-center text-[13px]" style={{ color: 'var(--text-muted)' }}>
              No matches for “{searchText}”.
              <div className="mt-1 text-[12px]" style={{ color: 'var(--text-faint)' }}>Prefix with <span className="font-mono">?</span> to ask the assistant.</div>
            </div>
          )}
        </div>

        {/* Inline Ask result */}
        {ask && (
          <AskResult ask={ask} onApprove={approveProposal} onReject={() => setAsk(a => a ? { ...a, proposalState: 'rejected' } : a)} />
        )}

        {/* Footer legend — hidden on mobile (no keyboard) to save vertical room */}
        {!narrow && (
          <div className="vshell-border-t flex items-center gap-4 px-4 py-2 text-[12px] shrink-0" style={{ color: 'var(--text-faint)' }}>
            <span className="flex items-center gap-1.5"><Kbd>↑</Kbd><Kbd>↓</Kbd> navigate</span>
            <span className="flex items-center gap-1.5"><Kbd>⏎</Kbd> open</span>
            <span className="flex items-center gap-1.5"><Kbd>tab</Kbd> section</span>
            <span className="ml-auto flex items-center gap-1.5"><Kbd>?</Kbd> ask</span>
          </div>
        )}
      </div>
    </div>
  )
}

interface RenderSectionsArgs {
  rows: PaletteRow[]
  selectedIdx: number
  setSelectedIdx: Dispatch<SetStateAction<number>>
  activate: (row: PaletteRow | undefined) => void
  mailState: MailState
  showAskSection: boolean
  rowRefs: RefObject<(HTMLDivElement | null)[]>
}

// Render the flat row list grouped under section headers, in order.
function renderSections({ rows, selectedIdx, setSelectedIdx, activate, mailState, showAskSection, rowRefs }: RenderSectionsArgs): ReactElement[] {
  const out: ReactElement[] = []
  let lastKind: RowKind | null = null
  if (rowRefs) rowRefs.current = []
  rows.forEach((row, i) => {
    if (row.kind !== lastKind) {
      lastKind = row.kind
      out.push(
        <div key={'h:' + row.kind} className="px-4 pt-2.5 pb-1 flex items-center gap-2">
          <span className="text-[12px] font-mono uppercase tracking-[0.14em]" style={{ color: 'var(--text-faint)' }}>{SECTION_LABEL[row.kind]}</span>
          {row.kind === 'mail' && mailState === 'loading' && <span className="text-[12px]" style={{ color: 'var(--text-ghost)' }}>searching…</span>}
        </div>
      )
    }
    out.push(
      <Row key={row.id} row={row} active={i === selectedIdx}
        rowRef={rowRefs ? (el) => { rowRefs.current[i] = el } : undefined}
        onHover={() => setSelectedIdx(i)} onClick={() => activate(row)} />
    )
  })
  // Mail degradation note (only when the user is searching and mail failed).
  if (mailState === 'error' && !showAskSection) {
    out.push(
      <div key="mail-err" className="px-4 py-1.5 text-[12px]" style={{ color: 'var(--text-muted)' }}>
        <span className="text-[12px] font-mono uppercase tracking-wider" style={{ color: 'var(--text-faint)' }}>Mail</span>
        <span className="ml-2">search unavailable — is Mail connected?</span>
      </div>
    )
  }
  return out
}

interface RowProps {
  row: PaletteRow
  active: boolean
  onHover: () => void
  onClick: () => void
  rowRef?: (el: HTMLDivElement | null) => void
}

function Row({ row, active, onHover, onClick, rowRef }: RowProps): ReactElement | null {
  const base = 'vshell-row w-full flex items-center gap-3 px-4 py-2 mx-0 text-left cursor-pointer'
  const titleColor = { color: active ? 'var(--text-primary)' : 'var(--text-secondary)' }
  const iconStyle = { color: 'var(--text-tertiary)' }
  if (row.kind === 'app') {
    const a = row.app
    return (
      <div ref={rowRef} data-active={active || undefined} className={base} onMouseMove={onHover} onClick={onClick}>
        <span className="w-5 text-center shrink-0" style={iconStyle}>{a.icon}</span>
        <span className="vshell-row-title text-[13px]" style={titleColor}>{a.name}</span>
        <span className="ml-auto text-[12px] truncate max-w-[45%]" style={{ color: 'var(--text-faint)' }}>{a.description}</span>
      </div>
    )
  }
  if (row.kind === 'action') {
    const c = row.cmd
    return (
      <div ref={rowRef} data-active={active || undefined} className={base} onMouseMove={onHover} onClick={onClick}>
        <span className="w-5 text-center shrink-0" style={iconStyle}>{c.icon || '▸'}</span>
        <span className="vshell-row-title text-[13px]" style={titleColor}>{c.title}</span>
        {c.subtitle && <span className="ml-auto text-[12px] font-mono truncate max-w-[45%]" style={{ color: 'var(--text-faint)' }}>{c.subtitle}</span>}
      </div>
    )
  }
  if (row.kind === 'mail') {
    const m = row.msg
    const sender = m.from_name || m.from || ''
    return (
      <div ref={rowRef} data-active={active || undefined} className={base} onMouseMove={onHover} onClick={onClick}>
        <span className="w-5 text-center shrink-0" style={{ color: 'var(--text-faint)' }}>✉</span>
        <span className="min-w-0 flex-1">
          <span className="vshell-row-title text-[13px] block truncate" style={titleColor}>{m.subject || '(no subject)'}</span>
          <span className="text-[12px] block truncate" style={{ color: 'var(--text-faint)' }}>{sender}{m.preview ? ` — ${m.preview}` : ''}</span>
        </span>
        {m.unread && <span className="w-1.5 h-1.5 rounded-full shrink-0" style={{ background: 'var(--accent)' }} />}
      </div>
    )
  }
  if (row.kind === 'ask') {
    return (
      <div ref={rowRef} data-active={active || undefined} className={base} onMouseMove={onHover} onClick={onClick}>
        <span className="w-5 text-center shrink-0" style={{ color: 'var(--accent)' }}>✦</span>
        <span className="vshell-row-title text-[13px]" style={titleColor}>
          Ask the assistant{row.prompt ? <span style={{ color: 'var(--text-faint)' }}> — “{row.prompt}”</span> : ''}
        </span>
        <span className="ml-auto"><span className="vshell-kbd">⏎</span></span>
      </div>
    )
  }
  return null
}

interface AskResultProps {
  ask: AskState
  onApprove: () => void
  onReject: () => void
}

// Inline assistant answer / proposal — a compact reuse of the wave-9 flow.
function AskResult({ ask, onApprove, onReject }: AskResultProps) {
  return (
    <div aria-live="polite" className="vshell-border-t px-4 py-3 max-h-[30vh] overflow-y-auto shrink-0" style={{ background: 'color-mix(in srgb, var(--bg-base) 30%, transparent)' }}>
      <div className="flex items-center gap-2 mb-1.5">
        <span style={{ color: 'var(--accent)' }}>✦</span>
        <span className="text-[12px] font-mono uppercase tracking-wider" style={{ color: 'var(--text-faint)' }}>Assistant</span>
        {ask.status === 'thinking' && (
          <span className="inline-flex gap-1 ml-1">
            <span className="vshell-typing w-1.5 h-1.5 rounded-full" />
            <span className="vshell-typing w-1.5 h-1.5 rounded-full" style={{ animationDelay: '150ms' }} />
            <span className="vshell-typing w-1.5 h-1.5 rounded-full" style={{ animationDelay: '300ms' }} />
          </span>
        )}
      </div>
      {ask.answer && (
        <div className="text-[13px] whitespace-pre-wrap leading-relaxed" style={{ color: ask.status === 'error' ? 'var(--status-danger)' : 'var(--text-secondary)' }}>
          {ask.answer}
        </div>
      )}
      {ask.status === 'proposal' && ask.proposal && (
        <div className="mt-2.5">
          <ProposalCard
            proposal={ask.proposal}
            state={ask.proposalState}
            onApprove={onApprove}
            onReject={onReject}
            approveKey="Y"
            rejectKey="N"
            compact
          />
          {(!ask.proposalState || ask.proposalState === 'pending') && (
            <div className="mt-1.5 text-[12px] flex items-center gap-1.5" style={{ color: 'var(--text-muted)' }} aria-hidden="true">
              <Kbd>Y</Kbd> approve <Kbd>N</Kbd> reject · or <Kbd>tab</Kbd> to focus
            </div>
          )}
        </div>
      )}
    </div>
  )
}
