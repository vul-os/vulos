/**
 * Home — the proactive, AI-driven default surface of Vulos OS (Wave 11).
 *
 * Not a launcher: a *home*. The first thing you see is a curated brief of what
 * needs you today (the assistant's guarded Attention skill), your agenda, a
 * light recent-activity feed, and an always-there "ask your assistant" composer
 * that reuses the wave-9 agent turn + proposal/approve flow — so Home is where
 * you both SEE and DO. Quick launch complements (does not replace) the Launchpad.
 *
 * Everything is backed by real state: GET /api/assistant/home aggregates the
 * brief + agenda + activity + sovereignty in one round-trip, each section
 * failing independently so Home always renders (brief shows "assistant offline",
 * never a crash). The composer talks to /api/assistant/agent + /api/assistant/
 * execute. When unauthed/offline the backend serves a demo fixture so Home is
 * still alive.
 *
 * SOVEREIGN: the brief uses the on-instance assistant behind the egress Guard —
 * no new egress is introduced here.
 */
import { useState, useEffect, useCallback, useRef } from 'react'
import { useShell } from '../../providers/ShellProvider'
import { getAppById } from '../../core/AppRegistry'
import { builtinComponent, isBuiltinComponent, BUILTIN_SINGLETONS } from '../builtinApps'
import { runAgentTurn } from '../../core/agentStream'

// Curated quick-launch tiles — the everyday surfaces. "All apps" opens the
// full Launchpad, so Home complements rather than replaces it.
const QUICK_LAUNCH = ['lilmail', 'vulos-calendar', 'drive', 'assistant', 'vulos-office', 'terminal', 'persona']

const TIER_DOT = { local: '#22c55e', sovereign: '#22c55e', brokered: '#f59e0b', external: '#ef4444' }

// Module-scoped cache of the last Home payload. Home remounts each time you
// close all windows (it's the desktop backdrop), so we render the cached brief/
// agenda instantly and refresh in the background — no skeleton + no repeat model
// call on every return to Home.
let cachedHome = null

// ── tiny date/time helpers ───────────────────────────────────────────────────
const fmtDate = (d) => d.toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric' })
const fmtTime = (d) => d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
function eventTime(iso) {
  const d = new Date(iso)
  if (isNaN(d)) return ''
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}
function isToday(iso) {
  const d = new Date(iso); const n = new Date()
  return d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate()
}
function relDay(iso) {
  const d = new Date(iso); if (isNaN(d)) return ''
  if (isToday(iso)) return 'Today'
  const t = new Date(); t.setDate(t.getDate() + 1)
  if (d.toDateString() === t.toDateString()) return 'Tomorrow'
  return d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })
}

// ── proposal card (mirrors the assistant's confirmation-gate surface) ─────────
const PROPOSAL_VERB = {
  send_email: 'Send email', create_calendar_event: 'Create event',
  add_contact: 'Add contact', triage: 'Change mailbox',
}
function ProposalCard({ proposal, state, onApprove, onReject }) {
  const verb = PROPOSAL_VERB[proposal.tool] || 'Action'
  const args = proposal.args || {}
  return (
    <div className="rounded-xl border border-amber-500/30 bg-amber-950/20 px-3.5 py-3 text-[13px]">
      <div className="flex items-center gap-2 mb-1.5">
        <span className="inline-block w-2 h-2 rounded-full bg-amber-400" />
        <span className="text-amber-300 font-medium text-[12px]">Needs your approval · {verb}</span>
      </div>
      <div className="text-neutral-200 leading-relaxed">{proposal.summary}</div>
      {proposal.from_content && (
        <div className="text-[12px] text-red-300 bg-red-950/30 border border-red-500/30 rounded-lg px-2.5 py-1.5 mt-1.5">
          ⚠ {proposal.warning || "This action's target came from message content — review carefully."}
        </div>
      )}
      {(args.body || args.notes) && (
        <div className="text-[12px] text-neutral-400 whitespace-pre-wrap bg-neutral-900/50 rounded-lg px-2.5 py-2 mt-2 max-h-40 overflow-y-auto">
          {args.body || args.notes}
        </div>
      )}
      {state === 'done' ? (
        <div className="text-[12px] text-emerald-400 mt-2">✓ Approved and executed.</div>
      ) : state === 'rejected' ? (
        <div className="text-[12px] text-neutral-500 mt-2">Rejected — nothing was done.</div>
      ) : (
        <div className="flex gap-2 mt-2.5">
          <button type="button" disabled={state === 'busy'} onClick={onApprove}
            className="text-[12px] px-3 py-1.5 rounded-lg bg-emerald-600 text-white hover:bg-emerald-500 transition-colors disabled:opacity-50">
            {state === 'busy' ? 'Working…' : 'Approve'}
          </button>
          <button type="button" disabled={state === 'busy'} onClick={onReject}
            className="text-[12px] px-3 py-1.5 rounded-lg bg-neutral-800 text-neutral-300 hover:bg-neutral-700 transition-colors disabled:opacity-50">
            Reject
          </button>
        </div>
      )}
    </div>
  )
}

// ── section shell ─────────────────────────────────────────────────────────────
function Section({ label, right, children }) {
  return (
    <section className="mb-6">
      <div className="flex items-center justify-between mb-2.5">
        <h2 className="text-[10.5px] font-mono uppercase tracking-[0.18em] text-neutral-500">{label}</h2>
        {right}
      </div>
      {children}
    </section>
  )
}

export default function Home() {
  const { openWindow, setLaunchpad } = useShell()
  const [data, setData] = useState(cachedHome)
  const [loading, setLoading] = useState(!cachedHome)
  const [offline, setOffline] = useState(false)
  const [clock, setClock] = useState(new Date())
  const [dismissed, setDismissed] = useState(() => new Set()) // snoozed/handled focus uids
  const [snoozing, setSnoozing] = useState(null) // uid awaiting confirm | 'busy:<uid>'

  // Composer + inline agent transcript
  const [input, setInput] = useState('')
  const [turns, setTurns] = useState([]) // {id, role, content, pending, proposal, state}
  const [busy, setBusy] = useState(false)
  const composerRef = useRef(null)
  const transcriptRef = useRef(null)

  const load = useCallback(() => {
    setLoading(true)
    fetch('/api/assistant/home', { credentials: 'include' })
      .then(r => { if (!r.ok) throw new Error(String(r.status)); return r.json() })
      .then(d => { cachedHome = d; setData(d); setOffline(false) })
      .catch(() => setOffline(true))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])
  useEffect(() => { const t = setInterval(() => setClock(new Date()), 30000); return () => clearInterval(t) }, [])
  useEffect(() => { transcriptRef.current?.scrollTo({ top: transcriptRef.current.scrollHeight, behavior: 'smooth' }) }, [turns])

  // ── app launching (shared builtin map; web apps open by url) ────────────────
  const openApp = useCallback((appId) => {
    const app = getAppById(appId)
    if (!app) { setLaunchpad(true); return }
    if (isBuiltinComponent(appId)) {
      openWindow({ appId, title: app.name, icon: app.icon, component: builtinComponent(appId), singleton: BUILTIN_SINGLETONS.has(appId) })
    } else if (app.url) {
      openWindow({ appId, title: app.name, url: app.url, icon: app.icon })
    } else {
      setLaunchpad(true)
    }
  }, [openWindow, setLaunchpad])

  const openAssistant = useCallback(() => openApp('assistant'), [openApp])
  const openMail = useCallback(() => openApp('lilmail'), [openApp])

  // ── agent composer (wave-9 proposal/approve flow, STREAMED in wave-17) ───────
  // The final answer streams token-by-token into the pending bubble; a mutating
  // action still arrives as a PROPOSAL (Approve/Reject → /execute). runAgentTurn
  // falls back to the non-streaming /agent if streaming can't be established.
  const runAgent = useCallback(async (text) => {
    if (busy || !text.trim()) return
    const history = turns
      .filter(t => (t.role === 'user' || t.role === 'assistant') && !t.proposal && t.content)
      .map(t => ({ role: t.role, content: t.content }))
    const uid = Math.random().toString(36).slice(2)
    const aid = Math.random().toString(36).slice(2)
    setTurns(t => [...t, { id: uid, role: 'user', content: text }, { id: aid, role: 'assistant', content: '', pending: true }])
    const patchAid = (patch) => setTurns(t => t.map(x => (x.id === aid ? { ...x, ...patch } : x)))
    setBusy(true)
    try {
      const result = await runAgentTurn({
        message: text,
        history,
        onToken: (_delta, full) => patchAid({ content: full, pending: true }),
        onStatus: (ev) => patchAid({ content: ev.content || 'thinking…', pending: true }),
        onProposal: (proposal) => patchAid({ pending: false, proposal, state: 'pending' }),
      })
      if (result.error) patchAid({ pending: false, content: result.error })
      else if (result.proposal) patchAid({ pending: false })
      else patchAid({ pending: false, content: result.answer || 'No response.' })
    } catch {
      patchAid({ pending: false, content: 'Could not reach the assistant.' })
    } finally {
      setBusy(false)
    }
  }, [busy, turns])

  const submitAsk = (e) => {
    e?.preventDefault()
    const text = input.trim()
    if (!text) return
    setInput('')
    runAgent(text)
  }

  const replyWith = useCallback((item) => {
    const who = item.from_name || item.from
    const prompt = `Draft a reply to "${item.subject}" from ${who}.`
    setInput('')
    composerRef.current?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    runAgent(prompt)
  }, [runAgent])

  // Approve/reject an inline proposal (send_email etc from the composer flow).
  const approve = useCallback(async (id, proposal) => {
    setTurns(t => t.map(x => x.id === id ? { ...x, state: 'busy' } : x))
    try {
      // Send ONLY the opaque proposal id; the server runs its stored args.
      const res = await fetch('/api/assistant/execute', {
        method: 'POST', credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: proposal.id }),
      })
      const d = await res.json().catch(() => ({}))
      setTurns(t => t.map(x => x.id === id ? { ...x, state: res.ok ? 'done' : 'pending' } : x))
      if (res.ok && d.result) {
        setTurns(t => [...t, { id: Math.random().toString(36).slice(2), role: 'assistant', content: d.result }])
      }
    } catch {
      setTurns(t => t.map(x => x.id === id ? { ...x, state: 'pending' } : x))
    }
  }, [])
  const reject = useCallback((id) => setTurns(t => t.map(x => x.id === id ? { ...x, state: 'rejected' } : x)), [])

  // ── snooze a focus item — a DIRECT, user-initiated triage on a message the
  // user can see. This is not an LLM proposal, so it uses the dedicated
  // /api/assistant/triage endpoint (deterministic, session-authed, triage-only)
  // rather than the ledger-gated /execute. ──────────────────────────────────
  const snooze = useCallback(async (item) => {
    setSnoozing(`busy:${item.uid}`)
    try {
      const res = await fetch('/api/assistant/triage', {
        method: 'POST', credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: item.uid, action: 'snooze', folder: item.folder || 'INBOX' }),
      })
      if (res.ok) setDismissed(s => new Set(s).add(item.uid))
    } catch { /* leave it in place on failure */ }
    setSnoozing(null)
  }, [])

  const focus = (data?.focus || []).filter(f => !dismissed.has(f.uid))
  const agenda = data?.agenda || []
  const activity = data?.activity || []
  const tier = data?.sovereignty?.tier || 'local'
  const tierLabel = data?.sovereignty?.label || ''

  return (
    <div className="absolute inset-0 overflow-y-auto bg-neutral-950/45 backdrop-blur-sm">
      <div className="max-w-3xl mx-auto px-6 py-10">

        {/* Header — greeting + live clock + sovereignty posture */}
        <header className="mb-7 flex items-end justify-between gap-4">
          <div>
            <div className="text-[26px] font-light text-neutral-100 tracking-tight">
              {data?.greeting || 'Welcome'}
            </div>
            <div className="text-[12px] font-mono text-neutral-500 mt-1">
              {fmtDate(clock)} · {fmtTime(clock)}
            </div>
          </div>
          <div className="flex items-center gap-3">
            <button
              type="button"
              onClick={openAssistant}
              title="Where your AI runs"
              className="flex items-center gap-1.5 text-[11px] text-neutral-400 hover:text-neutral-200 rounded-md px-2 py-1 hover:bg-neutral-800/60 transition-colors"
            >
              <span className="inline-block w-2 h-2 rounded-full" style={{ background: TIER_DOT[tier] || TIER_DOT.external }} />
              <span className="font-mono">{tierLabel || 'sovereign'}</span>
            </button>
            <button
              type="button"
              onClick={load}
              disabled={loading}
              title="Refresh"
              className="w-7 h-7 flex items-center justify-center rounded-md text-neutral-500 hover:text-neutral-200 hover:bg-neutral-800/60 transition-colors disabled:opacity-40"
            >
              <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.4" className={loading ? 'animate-spin' : ''}>
                <path d="M13.5 8a5.5 5.5 0 10-1.6 3.9M13.5 12.5V9h-3.5" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
            </button>
          </div>
        </header>

        {/* Ask / act — the always-there composer that opens the agent */}
        <div ref={composerRef} className="mb-7">
          <form onSubmit={submitAsk}
            className="rounded-2xl border border-neutral-700/70 bg-neutral-900/70 backdrop-blur px-3.5 py-3 focus-within:border-neutral-500 transition-colors">
            <div className="flex items-end gap-2.5">
              <span className="text-neutral-500 text-lg leading-none mb-1 select-none">✦</span>
              <textarea
                rows={1}
                value={input}
                onChange={e => setInput(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) submitAsk(e) }}
                placeholder="Ask your assistant, or tell it to do something…"
                className="flex-1 resize-none bg-transparent text-[14px] text-neutral-100 placeholder-neutral-600 focus:outline-none max-h-40 leading-relaxed py-1"
              />
              <button type="submit" disabled={busy || !input.trim()}
                className="flex-shrink-0 w-9 h-9 rounded-xl bg-blue-600 text-white flex items-center justify-center hover:bg-blue-500 transition-colors disabled:opacity-40"
                aria-label="Send">
                <svg viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
                  <path d="M10.894 2.553a1 1 0 00-1.788 0l-7 14a1 1 0 001.169 1.409l5-1.429A1 1 0 009 15.571V11a1 1 0 112 0v4.571a1 1 0 00.725.962l5 1.428a1 1 0 001.17-1.408l-7-14z" />
                </svg>
              </button>
            </div>
          </form>

          {turns.length > 0 && (
            <div ref={transcriptRef} className="mt-3 max-h-72 overflow-y-auto space-y-2.5 px-1">
              {turns.map(t => (
                t.proposal ? (
                  <div key={t.id} className="space-y-2">
                    {t.content && <div className="text-[13px] text-neutral-300 leading-relaxed whitespace-pre-wrap">{t.content}</div>}
                    <ProposalCard proposal={t.proposal} state={t.state}
                      onApprove={() => approve(t.id, t.proposal)} onReject={() => reject(t.id)} />
                  </div>
                ) : (
                  <div key={t.id} className={`flex ${t.role === 'user' ? 'justify-end' : 'justify-start'}`}>
                    <div className={`max-w-[85%] rounded-2xl px-3.5 py-2 text-[13px] leading-relaxed whitespace-pre-wrap ${
                      t.role === 'user' ? 'bg-blue-600/90 text-white rounded-br-sm' : 'bg-neutral-800/70 text-neutral-200 rounded-bl-sm'}`}>
                      {t.content}
                      {t.pending && <span className="inline-block w-1.5 h-3.5 ml-0.5 align-middle bg-neutral-400 animate-pulse" />}
                    </div>
                  </div>
                )
              ))}
            </div>
          )}
        </div>

        {/* What needs you today — the curated brief + actionable focus items */}
        <Section label="What needs you today"
          right={data?.brief && <button onClick={openAssistant} className="text-[11px] text-neutral-500 hover:text-neutral-300 transition-colors font-mono">open assistant →</button>}>
          {loading && !data ? (
            <div className="space-y-2">
              <div className="h-3.5 bg-neutral-800/70 rounded animate-pulse w-4/5" />
              <div className="h-3.5 bg-neutral-800/70 rounded animate-pulse w-3/5" />
            </div>
          ) : offline ? (
            <p className="text-[13px] text-neutral-500">Assistant offline — Home is still here. Reconnect your box to get today's brief.</p>
          ) : data?.brief ? (
            <div className="text-[13.5px] text-neutral-300 leading-relaxed whitespace-pre-wrap">{data.brief}</div>
          ) : data?.brief_error ? (
            <p className="text-[13px] text-neutral-500">
              The assistant couldn't produce a brief right now ({data.mail_error ? 'mail unavailable' : 'model offline'}).
              Your mail and agenda below are still live.
            </p>
          ) : (
            <p className="text-[13px] text-neutral-500">Nothing urgent — you're clear. Enjoy the calm.</p>
          )}

          {focus.length > 0 && (
            <ul className="mt-3.5 space-y-2">
              {focus.map(item => (
                <li key={item.uid} className="group rounded-xl border border-neutral-800/80 bg-neutral-900/50 px-3.5 py-2.5 hover:border-neutral-700 transition-colors">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="inline-block w-1.5 h-1.5 rounded-full bg-blue-400 flex-shrink-0" />
                        <span className="text-[13px] text-neutral-100 font-medium truncate">{item.subject}</span>
                      </div>
                      <div className="text-[11.5px] text-neutral-500 mt-0.5 truncate pl-3.5">{item.from_name || item.from}</div>
                      {item.preview && <div className="text-[12px] text-neutral-500/90 mt-1 line-clamp-2 pl-3.5">{item.preview}</div>}
                    </div>
                  </div>
                  <div className="flex items-center gap-1.5 mt-2 pl-3.5">
                    <button onClick={openMail} className="text-[11px] px-2.5 py-1 rounded-md bg-neutral-800/80 text-neutral-300 hover:bg-neutral-700 transition-colors">Open</button>
                    <button onClick={() => replyWith(item)} disabled={busy}
                      className="text-[11px] px-2.5 py-1 rounded-md bg-neutral-800/80 text-neutral-300 hover:bg-neutral-700 transition-colors disabled:opacity-40">Reply with assistant</button>
                    {snoozing === item.uid ? (
                      <>
                        <button onClick={() => snooze(item)} className="text-[11px] px-2.5 py-1 rounded-md bg-amber-600/80 text-white hover:bg-amber-500 transition-colors">Confirm snooze</button>
                        <button onClick={() => setSnoozing(null)} className="text-[11px] px-2 py-1 rounded-md text-neutral-500 hover:text-neutral-300 transition-colors">Cancel</button>
                      </>
                    ) : (
                      <button onClick={() => setSnoozing(item.uid)} disabled={snoozing === `busy:${item.uid}`}
                        className="text-[11px] px-2.5 py-1 rounded-md bg-neutral-800/80 text-neutral-300 hover:bg-neutral-700 transition-colors disabled:opacity-40">
                        {snoozing === `busy:${item.uid}` ? 'Snoozing…' : 'Snooze'}
                      </button>
                    )}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Section>

        {/* Today's agenda */}
        <Section label="Agenda"
          right={<button onClick={() => openApp('vulos-calendar')} className="text-[11px] text-neutral-500 hover:text-neutral-300 transition-colors font-mono">calendar →</button>}>
          {loading && !data ? (
            <div className="h-10 bg-neutral-800/50 rounded-xl animate-pulse" />
          ) : agenda.length === 0 ? (
            <p className="text-[13px] text-neutral-500">
              {data?.agenda_error ? 'Calendar unavailable right now.' : 'Nothing on your calendar for the week ahead.'}
            </p>
          ) : (
            <ul className="space-y-1.5">
              {agenda.map((ev, i) => (
                <li key={ev.id || i} className="flex items-center gap-3 rounded-xl border border-neutral-800/80 bg-neutral-900/50 px-3.5 py-2.5">
                  <div className="w-16 flex-shrink-0 text-right">
                    <div className="text-[12px] font-mono text-neutral-300">{ev.all_day ? 'All day' : eventTime(ev.start)}</div>
                    <div className="text-[10px] font-mono text-neutral-600">{relDay(ev.start)}</div>
                  </div>
                  <div className="w-px self-stretch bg-neutral-800" />
                  <div className="min-w-0">
                    <div className="text-[13px] text-neutral-100 truncate">{ev.title || '(untitled)'}</div>
                    {ev.location && <div className="text-[11px] text-neutral-500 truncate">{ev.location}</div>}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Section>

        {/* Recent activity — light cross-surface feed (mail today) */}
        <Section label="Recent activity"
          right={<button onClick={openMail} className="text-[11px] text-neutral-500 hover:text-neutral-300 transition-colors font-mono">mail →</button>}>
          {loading && !data ? (
            <div className="h-10 bg-neutral-800/50 rounded-xl animate-pulse" />
          ) : activity.length === 0 ? (
            <p className="text-[13px] text-neutral-500">{data?.mail_error ? 'Mail unavailable right now.' : 'No recent activity.'}</p>
          ) : (
            <ul className="divide-y divide-neutral-800/70 rounded-xl border border-neutral-800/80 bg-neutral-900/40 overflow-hidden">
              {activity.map((a, i) => (
                <li key={a.uid || i}>
                  <button onClick={openMail} className="w-full text-left px-3.5 py-2.5 hover:bg-neutral-800/40 transition-colors flex items-center gap-3">
                    <span className={`inline-block w-1.5 h-1.5 rounded-full flex-shrink-0 ${a.unread ? 'bg-blue-400' : 'bg-neutral-700'}`} />
                    <div className="min-w-0 flex-1">
                      <div className={`text-[13px] truncate ${a.unread ? 'text-neutral-100' : 'text-neutral-300'}`}>{a.title}</div>
                      <div className="text-[11px] text-neutral-500 truncate">{a.subtitle}</div>
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Section>

        {/* Quick launch — the everyday apps, tastefully */}
        <Section label="Quick launch"
          right={<button onClick={() => setLaunchpad(true)} className="text-[11px] text-neutral-500 hover:text-neutral-300 transition-colors font-mono">all apps →</button>}>
          <div className="flex flex-wrap gap-2">
            {QUICK_LAUNCH.map(id => {
              const app = getAppById(id)
              if (!app) return null
              return (
                <button key={id} onClick={() => openApp(id)}
                  className="flex items-center gap-2 rounded-xl border border-neutral-800/80 bg-neutral-900/50 px-3 py-2 hover:border-neutral-700 hover:bg-neutral-800/60 transition-colors">
                  <span className="text-[15px] leading-none w-5 text-center">{app.icon}</span>
                  <span className="text-[12.5px] text-neutral-200">{app.name}</span>
                </button>
              )
            })}
          </div>
        </Section>

        <div className="h-6" />
      </div>
    </div>
  )
}
