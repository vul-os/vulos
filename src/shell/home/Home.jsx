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
 *
 * LAYOUT (wave-79 hero redesign): a composed, responsive "home canvas" rather
 * than a single narrow list. A prominent greeting + sovereignty posture, a hero
 * ask-bar with an ambient accent glow, then a bento split — the assistant brief
 * and focus items lead on the left while the day's agenda / invites / reminders
 * stack on the right; recent activity + quick-launch anchor a full-width lower
 * row. Everything stacks to one clean column on narrow screens. Pure
 * presentation: no state, endpoints, labels, or a11y semantics changed.
 */
import { useState, useEffect, useCallback, useRef } from 'react'
import { useShell } from '../../providers/ShellProvider'
import { getAppById } from '../../core/AppRegistry'
import { builtinComponent, isBuiltinComponent, BUILTIN_SINGLETONS } from '../builtinApps'
import { runAgentTurn } from '../../core/agentStream'
import { useAutoGrow } from '../../core/useAutoGrow'
import { notify } from '../../core/notificationStore'
// Shared confirmation-gate card — one source of truth across every assistant
// surface (the Assistant panel, this composer, the Command Palette).
import { ProposalCard } from '../../builtin/assistant/ProposalCard'

// Curated quick-launch tiles — the everyday surfaces. "All apps" opens the
// full Launchpad, so Home complements rather than replaces it.
const QUICK_LAUNCH = ['lilmail', 'vulos-calendar', 'drive', 'assistant', 'vulos-office', 'terminal', 'persona']

const TIER_DOT = {
  local: 'var(--status-success)',
  sovereign: 'var(--status-success)',
  brokered: 'var(--status-warning)',
  external: 'var(--status-danger)',
}

// ── shared surface + control vocabulary ───────────────────────────────────────
// One card / button / link language so every section reads as the same system —
// consistent radius, border, translucency and hover, tuned for the dark scrim
// Home floats over. Kept as constants (not ad-hoc repeated strings) so the whole
// surface stays coherent and is retuned in one place.
const CARD = 'rounded-2xl border border-neutral-800/70 bg-neutral-900/50 backdrop-blur-sm'
const CARD_ROW = `${CARD} px-3.5 py-3 transition-colors hover:border-neutral-700/90 hover:bg-neutral-900/70`
const BTN = 'text-[11px] px-2.5 py-1 rounded-md bg-neutral-800/80 text-neutral-300 hover:bg-neutral-700 transition-colors disabled:opacity-40'
const LINK = 'text-[11px] font-mono text-neutral-500 hover:text-neutral-300 transition-colors'

// Module-scoped cache of the last Home payload. Home remounts each time you
// close all windows (it's the desktop backdrop), so we render the cached brief/
// agenda instantly and refresh in the background — no skeleton + no repeat model
// call on every return to Home.
let cachedHome = null

// Honour the OS reduced-motion preference for the composer's scroll choreography
// (checked at call time so a mid-session preference change is respected).
const scrollBehavior = () =>
  (typeof window !== 'undefined' && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches)
    ? 'auto' : 'smooth'

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
// A reminder's fire time as "Today · 15:00" / "Mon Jul 7 · 15:00".
function reminderWhen(iso) {
  const d = new Date(iso); if (isNaN(d)) return ''
  return `${relDay(iso)} · ${fmtTime(d)}`
}

// ── section shell ─────────────────────────────────────────────────────────────
// A labelled block: an accent tick + mono eyebrow on the left, an optional
// action/status on the right. The tick and tightened tracking give each section
// a crisp, intentional header instead of a lone grey caption.
function Section({ label, right, children, className = '' }) {
  return (
    <section className={className}>
      <div className="flex items-center justify-between gap-3 mb-3">
        <h2 className="flex items-center gap-2 text-[10.5px] font-mono uppercase tracking-[0.2em] text-neutral-400">
          <span aria-hidden="true" className="inline-block h-px w-4 rounded-full" style={{ background: 'var(--accent)' }} />
          {label}
        </h2>
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
  const inputRef = useAutoGrow(input, { maxHeight: 160 })
  // Aborts an in-flight streaming agent turn on unmount so the SSE fetch is torn
  // down instead of leaking and writing to dead component state.
  const agentCtl = useRef(null)

  const load = useCallback(() => {
    setLoading(true)
    fetch('/api/assistant/home', { credentials: 'include' })
      .then(r => { if (!r.ok) throw new Error(String(r.status)); return r.json() })
      .then(d => { cachedHome = d; setData(d); setOffline(false) })
      .catch(() => setOffline(true))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])
  useEffect(() => () => { agentCtl.current?.abort() }, [])
  useEffect(() => { const t = setInterval(() => setClock(new Date()), 30000); return () => clearInterval(t) }, [])
  useEffect(() => { transcriptRef.current?.scrollTo({ top: transcriptRef.current.scrollHeight, behavior: scrollBehavior() }) }, [turns])

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
    const ctl = new AbortController()
    agentCtl.current = ctl
    try {
      const result = await runAgentTurn({
        message: text,
        history,
        signal: ctl.signal,
        onToken: (_delta, full) => patchAid({ content: full, pending: true }),
        onStatus: (ev) => patchAid({ content: ev.content || 'thinking…', pending: true }),
        onProposal: (proposal) => patchAid({ pending: false, proposal, state: 'pending' }),
      })
      if (result.error) patchAid({ pending: false, content: result.error })
      else if (result.proposal) patchAid({ pending: false })
      else patchAid({ pending: false, content: result.answer || 'No response.' })
    } catch (err) {
      // Aborted by unmount: component is gone — do not write dead state.
      if (err?.name !== 'AbortError') patchAid({ pending: false, content: 'Could not reach the assistant.' })
    } finally {
      if (agentCtl.current === ctl) agentCtl.current = null
      if (!ctl.signal.aborted) setBusy(false)
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
    composerRef.current?.scrollIntoView({ behavior: scrollBehavior(), block: 'center' })
    runAgent(prompt)
  }, [runAgent])

  // RSVP to a calendar invite. This routes through the agent so the mutating
  // response arrives as a LEDGER-GATED proposal in the composer (Approve/Reject
  // → /execute) — never an auto-executed action. It only names the source
  // message id + the chosen response; the model builds the rsvp_invite proposal.
  const rsvpInvite = useCallback((inv, response) => {
    const iv = inv.invite || {}
    const prompt = `RSVP ${response} to the calendar invite "${iv.summary || inv.subject}" (message ${inv.message_uid}).`
    setInput('')
    composerRef.current?.scrollIntoView({ behavior: scrollBehavior(), block: 'center' })
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
      else notify({ title: 'Could not snooze', body: `“${item.subject}” stayed in your inbox.`, level: 'warning', source: 'assistant' })
    } catch {
      // Leave it in place on failure, but tell the user rather than silently no-op.
      notify({ title: 'Could not snooze', body: 'The assistant is unreachable — nothing was changed.', level: 'warning', source: 'assistant' })
    }
    setSnoozing(null)
  }, [])

  // ── reminders: DIRECT done/cancel of a reminder the user can see (wave-62).
  // Like snooze, this is a deterministic, session-authed action on the user's
  // OWN reminder — NOT an LLM proposal — so it hits the dedicated endpoint
  // (server scopes the cancel by user id). Setting a reminder still goes through
  // the ledger-gated agent flow (the composer), never auto-created. ───────────
  const [remindersDismissed, setRemindersDismissed] = useState(() => new Set())
  const [remindersBusy, setRemindersBusy] = useState(null) // id being cancelled
  const cancelReminder = useCallback(async (rem) => {
    setRemindersBusy(rem.id)
    try {
      const res = await fetch('/api/assistant/reminders/cancel', {
        method: 'POST', credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: rem.id }),
      })
      if (res.ok) setRemindersDismissed(s => new Set(s).add(rem.id))
      else notify({ title: 'Could not cancel reminder', body: 'It is still set.', level: 'warning', source: 'assistant' })
    } catch {
      notify({ title: 'Could not cancel reminder', body: 'The assistant is unreachable — nothing was changed.', level: 'warning', source: 'assistant' })
    }
    setRemindersBusy(null)
  }, [])

  const focus = (data?.focus || []).filter(f => !dismissed.has(f.uid))
  const agenda = data?.agenda || []
  const invites = data?.invites || []
  const reminders = (data?.reminders || []).filter(r => !remindersDismissed.has(r.id))
  const activity = data?.activity || []
  const tier = data?.sovereignty?.tier || 'local'
  const tierLabel = data?.sovereignty?.label || ''

  // ── section renderers — kept as locals so the JSX below stays a legible
  // composition map (header → ask-bar → bento) instead of one giant tree. ──────

  const briefSection = (
    <Section label="What needs you today"
      right={data?.brief && <button onClick={openAssistant} className={LINK}>open assistant →</button>}>
      {loading && !data ? (
        <div className="space-y-2">
          <div className="h-3.5 bg-neutral-800/70 rounded animate-pulse w-4/5" />
          <div className="h-3.5 bg-neutral-800/70 rounded animate-pulse w-3/5" />
        </div>
      ) : offline ? (
        <p className="text-[13px] text-neutral-500">Assistant offline — Home is still here. Reconnect your box to get today's brief.</p>
      ) : data?.brief ? (
        <div className="text-[14px] text-neutral-200 leading-relaxed whitespace-pre-wrap">{data.brief}</div>
      ) : data?.brief_error ? (
        <p className="text-[13px] text-neutral-500">
          The assistant couldn't produce a brief right now ({data.mail_error ? 'mail unavailable' : 'model offline'}).
          Your mail and agenda below are still live.
        </p>
      ) : (
        <p className="text-[13px] text-neutral-500">Nothing urgent — you're clear. Enjoy the calm.</p>
      )}

      {focus.length > 0 && (
        <ul className="mt-4 space-y-2.5">
          {focus.map(item => (
            <li key={item.uid} className={`group relative overflow-hidden ${CARD_ROW} pl-4`}>
              {/* accent spine — a quiet "this wants you" marker down the card edge */}
              <span aria-hidden="true" className="absolute inset-y-0 left-0 w-[3px]" style={{ background: 'var(--accent)' }} />
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="inline-block w-1.5 h-1.5 rounded-full flex-shrink-0" style={{ background: 'var(--accent)' }} />
                    <span className="text-[13px] text-neutral-100 font-medium truncate">{item.subject}</span>
                  </div>
                  <div className="text-[11.5px] text-neutral-500 mt-0.5 truncate pl-3.5">{item.from_name || item.from}</div>
                  {item.preview && <div className="text-[12px] text-neutral-500/90 mt-1 line-clamp-2 pl-3.5">{item.preview}</div>}
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-1.5 mt-2.5 pl-3.5">
                <button onClick={openMail} className={BTN}>Open</button>
                <button onClick={() => replyWith(item)} disabled={busy} className={BTN}>Reply with assistant</button>
                {snoozing === item.uid ? (
                  <>
                    <button onClick={() => snooze(item)} className="text-[11px] px-2.5 py-1 rounded-md text-white bg-warning transition-[filter] hover:brightness-110">Confirm snooze</button>
                    <button onClick={() => setSnoozing(null)} className="text-[11px] px-2 py-1 rounded-md text-neutral-500 hover:text-neutral-300 transition-colors">Cancel</button>
                  </>
                ) : (
                  <button onClick={() => setSnoozing(item.uid)} disabled={snoozing === `busy:${item.uid}`} className={BTN}>
                    {snoozing === `busy:${item.uid}` ? 'Snoozing…' : 'Snooze'}
                  </button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </Section>
  )

  const agendaSection = (
    <Section label="Agenda"
      right={
        <div className="flex items-center gap-2.5">
          {data && (
            <span className="flex items-center gap-1.5 text-[10px] font-mono uppercase tracking-wider text-neutral-600" title={data.agenda_fresh ? 'Calendar is live' : 'Calendar unavailable'}>
              <span className="inline-block w-1.5 h-1.5 rounded-full" style={{ background: data.agenda_fresh ? 'var(--status-success)' : 'var(--status-danger)' }} />
              {data.agenda_fresh ? 'live' : 'stale'}
            </span>
          )}
          <button onClick={() => openApp('vulos-calendar')} className={LINK}>calendar →</button>
        </div>
      }>
      {loading && !data ? (
        <div className="h-10 bg-neutral-800/50 rounded-xl animate-pulse" />
      ) : agenda.length === 0 ? (
        <p className="text-[13px] text-neutral-500">
          {data?.agenda_error ? 'Calendar unavailable right now.' : 'Nothing on your calendar for the week ahead.'}
        </p>
      ) : (
        <ul className="space-y-2">
          {agenda.map((ev, i) => (
            <li key={ev.id || i} className={`flex items-center gap-3.5 ${CARD_ROW}`}>
              <div className="flex-shrink-0 w-[64px] rounded-lg px-2 py-1.5 text-center" style={{ background: 'var(--accent-soft)' }}>
                <div className="text-[12px] font-mono text-neutral-100 leading-tight">{ev.all_day ? 'All day' : eventTime(ev.start)}</div>
                <div className="text-[9.5px] font-mono uppercase tracking-wide text-neutral-400 mt-0.5">{relDay(ev.start)}</div>
              </div>
              <div className="min-w-0">
                <div className="text-[13px] text-neutral-100 truncate">{ev.title || '(untitled)'}</div>
                {ev.location && <div className="text-[11px] text-neutral-500 truncate">{ev.location}</div>}
              </div>
            </li>
          ))}
        </ul>
      )}
    </Section>
  )

  const invitesSection = (invites.length > 0 || data?.invites_error) && (
    <Section label="Invites awaiting your response"
      right={invites.length > 0 && (
        <span className="text-[11px] font-mono text-neutral-500">
          {invites.length} · soonest {relDay(invites[0]?.invite?.start)}
        </span>
      )}>
      {data?.invites_error ? (
        <p className="text-[13px] text-neutral-500">Couldn't check for invites right now.</p>
      ) : (
        <ul className="space-y-2">
          {invites.map((inv) => {
            const iv = inv.invite || {}
            return (
              <li key={inv.message_uid} className={CARD_ROW}>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="text-[13px] text-neutral-100 font-medium truncate">{iv.summary || inv.subject || '(untitled invite)'}</div>
                    <div className="text-[11.5px] text-neutral-500 mt-0.5 truncate">
                      {!isNaN(new Date(iv.start)) && `${relDay(iv.start)}${iv.all_day ? ' · all day' : ` · ${eventTime(iv.start)}`}`}
                      {iv.location ? ` · ${iv.location}` : ''}
                    </div>
                    <div className="text-[11px] text-neutral-600 mt-0.5 truncate">from {iv.organizer || inv.from}</div>
                  </div>
                </div>
                <div className="flex flex-wrap items-center gap-1.5 mt-2.5">
                  <button onClick={() => rsvpInvite(inv, 'accept')} disabled={busy}
                    className="text-[11px] px-2.5 py-1 rounded-md bg-emerald-700/70 text-emerald-100 hover:bg-emerald-600 transition-colors disabled:opacity-40">Accept</button>
                  <button onClick={() => rsvpInvite(inv, 'tentative')} disabled={busy} className={BTN}>Tentative</button>
                  <button onClick={() => rsvpInvite(inv, 'decline')} disabled={busy} className={BTN}>Decline</button>
                  <button onClick={openMail} className="text-[11px] px-2.5 py-1 rounded-md text-neutral-500 hover:text-neutral-300 transition-colors">Open</button>
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </Section>
  )

  const remindersSection = (reminders.length > 0 || data?.reminders_error) && (
    <Section label="Reminders"
      right={reminders.length > 0 && (
        <span className="text-[11px] font-mono text-neutral-500">
          {reminders.length} · next {reminderWhen(reminders[0]?.remind_at)}
        </span>
      )}>
      {data?.reminders_error ? (
        <p className="text-[13px] text-neutral-500">Couldn't load your reminders right now.</p>
      ) : (
        <ul className="space-y-2">
          {reminders.map((rem) => (
            <li key={rem.id} className={CARD_ROW}>
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="text-[13px] text-neutral-100 truncate">{rem.text}</div>
                  <div className="text-[11.5px] text-neutral-500 mt-0.5 truncate">{reminderWhen(rem.remind_at)}</div>
                </div>
                <button
                  onClick={() => cancelReminder(rem)}
                  disabled={remindersBusy === rem.id}
                  className={`flex-shrink-0 ${BTN}`}>
                  {remindersBusy === rem.id ? 'Cancelling…' : 'Done'}
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </Section>
  )

  const activitySection = (
    <Section label="Recent activity"
      right={<button onClick={openMail} className={LINK}>mail →</button>}>
      {loading && !data ? (
        <div className="h-10 bg-neutral-800/50 rounded-xl animate-pulse" />
      ) : activity.length === 0 ? (
        <p className="text-[13px] text-neutral-500">{data?.mail_error ? 'Mail unavailable right now.' : 'No recent activity.'}</p>
      ) : (
        <ul className={`${CARD} divide-y divide-neutral-800/70 overflow-hidden`}>
          {activity.map((a, i) => (
            <li key={a.uid || i}>
              <button onClick={openMail} className="w-full text-left px-3.5 py-2.5 hover:bg-neutral-800/40 transition-colors flex items-center gap-3">
                <span className="inline-block w-1.5 h-1.5 rounded-full flex-shrink-0" style={{ background: a.unread ? 'var(--accent)' : 'var(--border-strong)' }} />
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
  )

  const quickLaunchSection = (
    <Section label="Quick launch"
      right={<button onClick={() => setLaunchpad(true)} className={LINK}>all apps →</button>}>
      <div className="grid grid-cols-2 sm:grid-cols-3 gap-2.5">
        {QUICK_LAUNCH.map(id => {
          const app = getAppById(id)
          if (!app) return null
          return (
            <button key={id} onClick={() => openApp(id)}
              className="group flex items-center gap-2.5 rounded-xl border border-neutral-800/70 bg-neutral-900/50 px-3 py-2.5 hover:border-neutral-700/90 hover:bg-neutral-900/80 transition-colors">
              <span className="flex-shrink-0 flex items-center justify-center w-7 h-7 rounded-lg text-[15px] leading-none transition-colors group-hover:brightness-125" style={{ background: 'var(--accent-soft)' }}>{app.icon}</span>
              <span className="text-[12.5px] text-neutral-200 truncate">{app.name}</span>
            </button>
          )
        })}
      </div>
    </Section>
  )

  return (
    <div className="absolute inset-0 overflow-y-auto bg-neutral-950/55 backdrop-blur-md">
      <div className="relative mx-auto w-full max-w-5xl px-5 sm:px-8 py-9 sm:py-12">

        {/* Ambient accent glow — pure decoration behind the greeting + ask-bar,
            giving the hero depth without introducing any chrome. */}
        <div aria-hidden="true"
          className="pointer-events-none absolute -top-24 left-1/2 -translate-x-1/2 h-80 w-[820px] max-w-full opacity-70"
          style={{ background: 'radial-gradient(50% 60% at 50% 0%, var(--accent-soft), transparent 72%)' }} />

        {/* Header — greeting + live clock + sovereignty posture */}
        <header className="relative mb-8 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <h1
              className="text-[30px] sm:text-[38px] font-light leading-[1.04] tracking-tight text-transparent bg-clip-text"
              style={{ backgroundImage: 'linear-gradient(135deg, #ffffff 0%, #c9c9cf 100%)' }}>
              {data?.greeting || 'Welcome'}
            </h1>
            <div className="mt-2 text-[12px] font-mono text-neutral-500">
              {fmtDate(clock)} · {fmtTime(clock)}
            </div>
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={openAssistant}
              title="Where your AI runs"
              className="flex items-center gap-2 rounded-full border border-neutral-800 bg-neutral-900/60 backdrop-blur px-3 py-1.5 text-[11px] text-neutral-300 hover:border-neutral-700 hover:text-neutral-100 transition-colors"
            >
              <span className="inline-block w-2 h-2 rounded-full" style={{ background: TIER_DOT[tier] || TIER_DOT.external }} />
              <span className="font-mono">{tierLabel || 'sovereign'}</span>
            </button>
            <button
              type="button"
              onClick={load}
              disabled={loading}
              title="Refresh"
              className="w-8 h-8 flex items-center justify-center rounded-full border border-neutral-800 bg-neutral-900/60 text-neutral-500 hover:text-neutral-200 hover:border-neutral-700 transition-colors disabled:opacity-40"
            >
              <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.4" className={loading ? 'animate-spin' : ''}>
                <path d="M13.5 8a5.5 5.5 0 10-1.6 3.9M13.5 12.5V9h-3.5" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
            </button>
          </div>
        </header>

        {/* Ask / act — the always-there composer that opens the agent. The hero
            control: elevated, glowing, unmissable. */}
        <div ref={composerRef} className="relative mb-9">
          <div className="relative">
            <div aria-hidden="true"
              className="pointer-events-none absolute -inset-x-3 -top-2 -bottom-2 rounded-3xl opacity-60"
              style={{ background: 'radial-gradient(60% 130% at 50% 0%, var(--accent-soft), transparent 70%)' }} />
            <form onSubmit={submitAsk}
              className="relative rounded-2xl border border-neutral-700/70 bg-neutral-900/70 backdrop-blur-md px-4 py-3.5 shadow-xl shadow-black/30 transition-colors focus-within:border-[var(--accent)]">
              <div className="flex items-end gap-3">
                <span className="text-lg leading-none mb-1 select-none" style={{ color: 'var(--accent)' }}>✦</span>
                <textarea
                  ref={inputRef}
                  rows={1}
                  value={input}
                  onChange={e => setInput(e.target.value)}
                  onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) submitAsk(e) }}
                  placeholder="Ask your assistant, or tell it to do something…"
                  aria-label="Ask your assistant"
                  className="flex-1 resize-none bg-transparent text-[14px] text-neutral-100 placeholder-neutral-600 focus:outline-none leading-relaxed py-1"
                />
                <button type="submit" disabled={busy || !input.trim()}
                  style={{ background: 'var(--accent)' }}
                  className="flex-shrink-0 w-9 h-9 rounded-xl text-white flex items-center justify-center transition-[filter] hover:brightness-110 disabled:opacity-40 disabled:hover:brightness-100 focus-primary"
                  aria-label="Send" title="Send (Enter)">
                  <svg viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
                    <path d="M10.894 2.553a1 1 0 00-1.788 0l-7 14a1 1 0 001.169 1.409l5-1.429A1 1 0 009 15.571V11a1 1 0 112 0v4.571a1 1 0 00.725.962l5 1.428a1 1 0 001.17-1.408l-7-14z" />
                  </svg>
                </button>
              </div>
            </form>
          </div>

          {turns.length > 0 && (
            <div ref={transcriptRef} role="log" aria-label="Assistant conversation" className="mt-3 max-h-72 overflow-y-auto space-y-2.5 px-1">
              {turns.map(t => (
                t.proposal ? (
                  <div key={t.id} className="space-y-2">
                    {t.content && <div className="text-[13px] text-neutral-300 leading-relaxed whitespace-pre-wrap">{t.content}</div>}
                    <ProposalCard proposal={t.proposal} state={t.state} compact
                      onApprove={() => approve(t.id, t.proposal)} onReject={() => reject(t.id)} />
                  </div>
                ) : (
                  <div key={t.id} className={`flex ${t.role === 'user' ? 'justify-end' : 'justify-start'}`}>
                    {/* Only the assistant's answer is a live region, so a screen reader
                        announces the streamed reply once — not every prior turn per token. */}
                    <div
                      style={t.role === 'user' ? { background: 'var(--accent)' } : undefined}
                      aria-live={t.role === 'assistant' ? 'polite' : undefined}
                      className={`max-w-[85%] rounded-2xl px-3.5 py-2 text-[13px] leading-relaxed whitespace-pre-wrap break-words ${
                      t.role === 'user' ? 'text-white rounded-br-sm' : 'bg-neutral-800/70 text-neutral-200 rounded-bl-sm'}`}>
                      {t.content}
                      {t.pending && <span className="inline-block w-1.5 h-3.5 ml-0.5 align-middle bg-neutral-400 animate-pulse" aria-hidden="true" />}
                    </div>
                  </div>
                )
              ))}
            </div>
          )}
        </div>

        {/* Bento split — the assistant brief + focus items lead; the day's
            schedule (agenda / invites / reminders) stacks alongside. Collapses to
            one column below lg. */}
        <div className="grid gap-x-10 gap-y-8 lg:grid-cols-12">
          <div className="lg:col-span-7">
            {briefSection}
          </div>
          <div className="lg:col-span-5 space-y-8">
            {agendaSection}
            {invitesSection}
            {remindersSection}
          </div>
        </div>

        {/* Lower row — recent activity + quick launch anchor the canvas. */}
        <div className="mt-8 grid gap-x-10 gap-y-8 lg:grid-cols-12">
          <div className="lg:col-span-7">
            {activitySection}
          </div>
          <div className="lg:col-span-5">
            {quickLaunchSection}
          </div>
        </div>

        <div className="h-6" />
      </div>
    </div>
  )
}
