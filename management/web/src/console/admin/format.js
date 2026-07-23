/**
 * console/admin/format.js — small presentational helpers for the Operator pages.
 * Pure functions, no side effects. Times are formatted in the viewer's locale.
 */

/** fmtTime — RFC3339 / SQL timestamp → locale short datetime. */
export function fmtTime(ts) {
  if (!ts) return '—'
  const d = new Date(ts.includes('T') ? ts : ts.replace(' ', 'T') + 'Z')
  if (isNaN(d.getTime())) return ts
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

/** relTime — coarse relative time ("3m ago", "2h ago", "5d ago"). */
export function relTime(ts) {
  if (!ts) return ''
  const d = new Date(ts.includes('T') ? ts : ts.replace(' ', 'T') + 'Z')
  if (isNaN(d.getTime())) return ''
  const secs = Math.round((Date.now() - d.getTime()) / 1000)
  if (secs < 0) return ''
  if (secs < 60) return 'just now'
  const mins = Math.round(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  const days = Math.round(hrs / 24)
  if (days < 30) return `${days}d ago`
  const months = Math.round(days / 30)
  if (months < 12) return `${months}mo ago`
  return `${Math.round(months / 12)}y ago`
}

/** shorten — truncate a long id/handle for a table cell, keeping head + tail. */
export function shorten(v, head = 10, tail = 6) {
  if (!v) return '—'
  if (v.length <= head + tail + 1) return v
  return `${v.slice(0, head)}…${v.slice(-tail)}`
}

/** actionTone — map an audit action label to a Pill colour. */
export function actionTone(action) {
  const a = (action || '').toLowerCase()
  if (a.includes('denied') || a.includes('locked') || a.includes('suspend') || a.includes('delete') || a.includes('block')) return 'danger'
  if (a.includes('reset') || a.includes('throttle') || a.includes('revoke') || a.includes('unsuspend')) return 'warn'
  if (a.includes('login') || a.includes('request') || a.includes('create') || a.includes('add')) return 'good'
  return 'faint'
}

/** boolPill — {tone,label} for a boolean flag. */
export function boolPill(v, trueLabel, falseLabel) {
  return v ? { tone: 'danger', label: trueLabel } : { tone: 'good', label: falseLabel }
}
