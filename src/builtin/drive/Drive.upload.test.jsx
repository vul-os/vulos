// Drive.upload.test.jsx — WAVE-26 polish: unit tests for uploadRowView, the pure
// derivation behind Drive's per-file upload progress bars. Covers determinate
// byte-progress, clamping, done/error terminal states, and the indeterminate
// (unknown-size) fallback.
//
import { describe, it, expect } from 'vitest'

import { uploadRowView } from './uploadRowView'

describe('uploadRowView — upload progress derivation', () => {
  it('reports determinate percent while uploading', () => {
    const v = uploadRowView({ pct: 0.42, state: 'uploading' })
    expect(v.widthPct).toBe(42)
    expect(v.label).toBe('42%')
    expect(v.done).toBe(false)
    expect(v.failed).toBe(false)
    expect(v.indeterminate).toBe(false)
  })

  it('clamps fractions outside 0..1', () => {
    expect(uploadRowView({ pct: -0.5, state: 'uploading' }).widthPct).toBe(0)
    expect(uploadRowView({ pct: 2, state: 'uploading' }).widthPct).toBe(100)
  })

  it('rounds to whole percent', () => {
    expect(uploadRowView({ pct: 0.126, state: 'uploading' }).widthPct).toBe(13)
  })

  it('done state fills the bar and shows Done', () => {
    const v = uploadRowView({ pct: 1, state: 'done' })
    expect(v.done).toBe(true)
    expect(v.widthPct).toBe(100)
    expect(v.label).toBe('Done')
  })

  it('error state fills the bar and shows Failed', () => {
    const v = uploadRowView({ pct: 0.3, state: 'error' })
    expect(v.failed).toBe(true)
    expect(v.widthPct).toBe(100)
    expect(v.label).toBe('Failed')
  })

  it('indeterminate when pct is null and still uploading', () => {
    const v = uploadRowView({ pct: null, state: 'uploading' })
    expect(v.indeterminate).toBe(true)
    expect(v.label).toBe('…')
    expect(v.widthPct).toBe(40)
  })

  it('treats missing pct as zero rather than NaN', () => {
    const v = uploadRowView({ pct: undefined, state: 'uploading' })
    expect(v.widthPct).toBe(0)
  })
})
