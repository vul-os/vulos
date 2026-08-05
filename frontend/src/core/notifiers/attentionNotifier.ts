// attentionNotifier.js — a REAL, sovereign notification producer (Wave 13).
//
// Polls GET /api/assistant/home (the same proactive aggregate the Home surface
// uses) on a slow cadence and surfaces two genuinely-new signals through the
// notificationStore seam:
//   • focus[]     — the assistant's guarded "what needs you today" items → a
//                   gentle "needs you" nudge (level info).
//   • activity[]  — recent mail; newly-seen UNREAD messages → a "New mail" nudge.
//
// SOVEREIGN: the assistant runs on-instance behind the egress Guard, so this
// introduces NO NEW EGRESS — it reuses one same-origin box endpoint the shell
// already calls.
//
// QUIET, by design:
//   • long poll interval (default 3 min) and only when the tab is visible;
//   • per-uid dedupe across the session (never re-notifies the same item);
//   • the FIRST poll only seeds the baseline — it never fires a burst of
//     notifications for the backlog you already have;
//   • the store additionally rate-limits per source and honours global mute.

import { notify } from '../notificationStore'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

interface FocusItem {
  uid?: string
  subject?: string
  from_name?: string
  from?: string
  preview?: string
}

interface ActivityItem {
  uid?: string
  unread?: boolean
  title?: string
  subtitle?: string
}

const POLL_MS = 3 * 60 * 1000 // 3 minutes
const MAX_PER_TICK = 3 // never surface more than a few at once (anti-spam)

const seenFocus = new Set<string>()
const seenMail = new Set<string>()
let seeded = false
let started = false

export function extractSignals(data: unknown): { focus: FocusItem[]; unreadMail: ActivityItem[] } {
  const focus: FocusItem[] = isRecord(data) && Array.isArray(data.focus) ? data.focus : []
  const activity: ActivityItem[] = isRecord(data) && Array.isArray(data.activity) ? data.activity : []
  return { focus, unreadMail: activity.filter((a) => a && a.unread) }
}

export function processHomeData(data: unknown): number {
  const { focus, unreadMail } = extractSignals(data)

  if (!seeded) {
    // Seed baseline without notifying — you only get pinged for what arrives
    // AFTER the shell is up, not your existing backlog.
    for (const f of focus) if (f.uid) seenFocus.add(f.uid)
    for (const m of unreadMail) if (m.uid) seenMail.add(m.uid)
    seeded = true
    return 0
  }

  let count = 0
  for (const f of focus) {
    if (count >= MAX_PER_TICK) break
    if (!f.uid || seenFocus.has(f.uid)) continue
    seenFocus.add(f.uid)
    notify({
      title: f.subject || 'Needs your attention',
      body: `From ${f.from_name || f.from || 'someone'}${f.preview ? ` — ${f.preview}` : ''}`,
      source: 'assistant',
      level: 'info',
      actions: [
        { id: 'open', label: 'Open Mail', route: { app: 'lilmail' } },
      ],
    })
    count++
  }
  for (const m of unreadMail) {
    if (count >= MAX_PER_TICK) break
    if (!m.uid || seenMail.has(m.uid)) continue
    seenMail.add(m.uid)
    notify({
      title: m.title || 'New mail',
      body: m.subtitle || '',
      source: 'mail',
      level: 'info',
      actions: [
        { id: 'open', label: 'Open Mail', route: { app: 'lilmail' } },
      ],
    })
    count++
  }
  return count
}

async function poll(): Promise<void> {
  if (typeof document !== 'undefined' && document.hidden) return
  try {
    const res = await fetch('/api/assistant/home', { credentials: 'include' })
    if (!res.ok) return
    const data: unknown = await res.json()
    processHomeData(data)
  } catch { /* assistant offline → notifications just stay empty */ }
}

/**
 * Start the notifier once. Returns a stop() function. Repeated calls are no-ops.
 */
export function startAttentionNotifier(): () => void {
  if (started) return () => {}
  started = true
  // Kick off after a short delay so it never competes with first paint.
  const boot = setTimeout(poll, 8000)
  const id = setInterval(poll, POLL_MS)
  return () => { started = false; clearTimeout(boot); clearInterval(id) }
}

// Test-only reset.
export function __resetForTests(): void {
  seenFocus.clear(); seenMail.clear(); seeded = false; started = false
}
