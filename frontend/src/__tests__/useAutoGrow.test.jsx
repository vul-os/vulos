/**
 * useAutoGrow.test.jsx — the shared composer textarea auto-resize hook.
 *
 * jsdom doesn't lay out text, so we drive the branch logic by stubbing
 * scrollHeight: the hook must (a) clamp the applied height to maxHeight and
 * (b) switch overflowY to 'auto' only once content exceeds maxHeight.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup, act } from '@testing-library/react'
import { useState } from 'react'
import { useAutoGrow } from '../core/useAutoGrow'

afterEach(cleanup)

// A test harness that forces a given scrollHeight on the textarea element so the
// hook's clamp/overflow logic is observable in jsdom.
function Harness({ scrollHeight, maxHeight = 100, initial = '' }) {
  const [v, setV] = useState(initial)
  const ref = useAutoGrow(v, { maxHeight })
  const attach = (el) => {
    ref.current = el
    if (el) Object.defineProperty(el, 'scrollHeight', { value: scrollHeight, configurable: true })
  }
  return <textarea ref={attach} value={v} onChange={(e) => setV(e.target.value)} data-testid="ta" />
}

describe('useAutoGrow', () => {
  it('clamps the applied height to maxHeight and enables scrolling past it', () => {
    const { getByTestId } = render(<Harness scrollHeight={240} maxHeight={100} />)
    const ta = getByTestId('ta')
    // Trigger the layout effect via a value change.
    act(() => { ta.dispatchEvent(new Event('input', { bubbles: true })) })
    expect(ta.style.height).toBe('100px')
    expect(ta.style.overflowY).toBe('auto')
  })

  it('grows to fit content below maxHeight without scrolling', () => {
    const { getByTestId } = render(<Harness scrollHeight={48} maxHeight={100} />)
    const ta = getByTestId('ta')
    act(() => { ta.dispatchEvent(new Event('input', { bubbles: true })) })
    expect(ta.style.height).toBe('48px')
    expect(ta.style.overflowY).toBe('hidden')
  })
})
