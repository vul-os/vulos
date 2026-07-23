/**
 * audit-format.js — pure, framework-free helpers for the org audit-log console
 * (/console/audit → GET /api/org/audit, ORGADMIN-AUDIT-01).
 *
 * Extracted so the label/tone/timestamp derivation is unit-tested in the node
 * environment without React/JSX. This module only PRESENTS audit rows the CP
 * returns ({ seq, id, ts, actor, action, target, metadata }); it never
 * fabricates or mutates an audit record.
 */

/**
 * Humanise a backend action label ("member_added", "role.changed",
 * "product-status-changed") into a Title Case phrase for display. Null-safe.
 */
export function humanizeAction(action) {
  if (!action || typeof action !== 'string') return 'Unknown action'
  const words = action.trim().replace(/[._-]+/g, ' ').split(/\s+/).filter(Boolean)
  if (words.length === 0) return 'Unknown action'
  return words
    .map((w, i) => (i === 0 ? w.charAt(0).toUpperCase() + w.slice(1) : w))
    .join(' ')
}

/**
 * Map an action label to a Pill tone. Destructive / removal / suspend actions
 * read as danger; grants / additions / verifications read as good; billing and
 * invites are accent; everything else is a neutral faint. Matching is on the
 * lowercased label so casing/separator variants normalise the same way.
 */
export function actionTone(action) {
  const a = (action || '').toLowerCase()
  if (/(remov|delete|destroy|revok|suspend|disabl|deactivat|ban|fail)/.test(a)) return 'danger'
  if (/(add|creat|grant|invit|accept|enabl|verif|activat|restor|resum)/.test(a)) return 'good'
  if (/(bill|invoice|payment|charge|topup|top_up|subscription|upgrad|downgrad)/.test(a)) return 'accent'
  if (/(role|permission|owner|admin)/.test(a)) return 'warn'
  return 'faint'
}

/** Format an ISO timestamp as an absolute local date-time; null-safe → "—". */
export function fmtAuditTime(iso) {
  if (!iso) return '—'
  try {
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return '—'
    return d.toLocaleString(undefined, {
      year: 'numeric', month: 'short', day: 'numeric',
      hour: '2-digit', minute: '2-digit',
    })
  } catch { return '—' }
}

/**
 * Short relative "how long ago" for an ISO timestamp; null-safe → "". Used as a
 * secondary, glanceable time. `now` is injectable for deterministic tests.
 */
export function relativeAuditTime(iso, now = Date.now()) {
  if (!iso) return ''
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const secs = Math.max(0, Math.floor((now - t) / 1000))
  if (secs < 45) return 'just now'
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  const days = Math.floor(hrs / 24)
  if (days < 30) return `${days}d ago`
  const months = Math.floor(days / 30)
  if (months < 12) return `${months}mo ago`
  return `${Math.floor(months / 12)}y ago`
}

/**
 * Shorten a long actor/target identifier for compact display while preserving
 * both ends (e.g. an email is kept whole; a raw ULID/UUID is middle-elided).
 * Null-safe → "—". An identifier that looks like an email is never elided.
 */
export function shortenId(id, max = 20) {
  if (id == null || id === '') return '—'
  const s = String(id)
  if (s.includes('@') || s.length <= max) return s
  const head = Math.ceil((max - 1) / 2)
  const tail = Math.floor((max - 1) / 2)
  return `${s.slice(0, head)}…${s.slice(s.length - tail)}`
}

/**
 * Flatten a metadata object into stable, sorted "key: value" pairs for display.
 * Null / non-object → []. Keys are sorted so the render order is deterministic.
 */
export function metadataPairs(metadata) {
  if (!metadata || typeof metadata !== 'object' || Array.isArray(metadata)) return []
  return Object.keys(metadata)
    .sort()
    .map(k => ({ key: k, value: String(metadata[k]) }))
}

/**
 * Derive the pager view for keyset (cursor-stack) pagination. `cursorStack` is
 * the history of cursors we've paged forward through ([] = newest page); a page
 * response's `next_cursor` (empty when there are no older rows) drives "Older".
 * Returns { hasPrev, hasNext, pageNumber, cursor } — pure so the state machine
 * can be unit-tested. `cursor` is the cursor for the CURRENT page ('' = newest).
 */
export function paginationView(cursorStack, nextCursor) {
  const stack = Array.isArray(cursorStack) ? cursorStack : []
  return {
    hasPrev: stack.length > 0,
    hasNext: !!nextCursor,
    pageNumber: stack.length + 1,
    cursor: stack.length > 0 ? stack[stack.length - 1] : '',
  }
}
