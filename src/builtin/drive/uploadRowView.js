// uploadRowView — pure derivation of an upload row's presentation from its
// state, powering Drive's per-file upload progress bars (WAVE-26). `pct` is a
// 0..1 byte fraction; `null` (unknown size) yields an indeterminate bar. state
// is 'uploading' | 'done' | 'error'.
export function uploadRowView({ pct, state }) {
  const failed = state === 'error'
  const done = state === 'done'
  const indeterminate = pct === null && !done && !failed
  const widthPct = done || failed ? 100 : indeterminate ? 40 : Math.round(Math.max(0, Math.min(1, pct || 0)) * 100)
  const label = failed ? 'Failed' : done ? 'Done' : indeterminate ? '…' : `${widthPct}%`
  return { failed, done, indeterminate, widthPct, label }
}
