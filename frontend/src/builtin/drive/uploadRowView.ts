// uploadRowView — pure derivation of an upload row's presentation from its
// state, powering Drive's per-file upload progress bars (WAVE-26). `pct` is a
// 0..1 byte fraction; `null` (unknown size) yields an indeterminate bar. state
// is 'uploading' | 'resuming' | 'done' | 'error'. 'resuming' is a transient
// state on the resumable path: the box already held committed bytes, so the bar
// is pre-seeded to the resumed fraction and labelled "Resuming".
export type UploadRowState = 'uploading' | 'resuming' | 'done' | 'error'

export interface UploadRowInput {
  pct: number | null | undefined
  state: UploadRowState
}

export interface UploadRowView {
  failed: boolean
  done: boolean
  resuming: boolean
  indeterminate: boolean
  widthPct: number
  label: string
}

export function uploadRowView({ pct, state }: UploadRowInput): UploadRowView {
  const failed = state === 'error'
  const done = state === 'done'
  const resuming = state === 'resuming'
  const indeterminate = pct === null && !done && !failed
  const widthPct = done || failed ? 100 : indeterminate ? 40 : Math.round(Math.max(0, Math.min(1, pct || 0)) * 100)
  const label = failed ? 'Failed' : done ? 'Done' : resuming ? 'Resuming' : indeterminate ? '…' : `${widthPct}%`
  return { failed, done, resuming, indeterminate, widthPct, label }
}
