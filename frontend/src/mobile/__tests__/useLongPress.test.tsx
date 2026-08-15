import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { LONG_PRESS_MS, LONG_PRESS_SLOP, isTouchPointer, useLongPress, usePointerKind } from '../useLongPress'

/**
 * The long-press gesture, driven through a real React element with fake timers.
 *
 * jsdom has no touch hardware, but it does dispatch pointer events with a
 * `pointerType`, which is the only input this hook reads — so everything that
 * matters here (does a mouse fire it, does a scroll cancel it, does the tap that
 * follows get swallowed) is observable without a device. What is NOT observable
 * here is whether the browser's own long-press callout fights ours; that is what
 * e2e/mobile-files.e2e.ts is for.
 */

interface Fired { x: number; y: number }

function Probe({ onFire, disabled }: { onFire: (p: Fired) => void; disabled?: boolean }) {
  const press = useLongPress({ onLongPress: onFire, disabled })
  const pointer = usePointerKind()
  return (
    <button
      data-testid="target"
      onPointerDown={(e) => { pointer.track(e); press.onPointerDown(e) }}
      onPointerMove={press.onPointerMove}
      onPointerUp={press.onPointerUp}
      onPointerCancel={press.onPointerCancel}
      onClick={() => { if (!press.consumed()) onFire({ x: -1, y: -1 }) }}
      onContextMenu={(e) => {
        e.preventDefault()
        if (press.suppressContextMenu()) return
        onFire({ x: -2, y: -2 })
      }}
    >
      row
    </button>
  )
}

/** jsdom's PointerEvent does not carry pointerType, so it is set explicitly. */
function pointer(type: string, init: PointerEventInit & { pointerType?: string }) {
  const e = new MouseEvent(type, { bubbles: true, ...init }) as MouseEvent & { pointerType?: string }
  Object.defineProperty(e, 'pointerType', { value: init.pointerType ?? 'touch' })
  return e
}

describe('useLongPress', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  function setup(disabled = false) {
    const fired: Fired[] = []
    // Scoped to THIS render's container: a test that renders the probe twice
    // (the pointerup/pointercancel pair below) would otherwise ask the whole
    // document for one testid and get two.
    const { container } = render(<Probe onFire={(p) => fired.push(p)} disabled={disabled} />)
    const el = container.querySelector('[data-testid="target"]')
    if (!el) throw new Error('probe did not render')
    return { el, fired }
  }

  it('fires after the hold, at the point the finger rested', () => {
    const { el, fired } = setup()
    act(() => { el.dispatchEvent(pointer('pointerdown', { clientX: 120, clientY: 300 })) })
    expect(fired).toEqual([])
    act(() => { vi.advanceTimersByTime(LONG_PRESS_MS) })
    expect(fired).toEqual([{ x: 120, y: 300 }])
  })

  it('does not fire before the hold is complete', () => {
    const { el, fired } = setup()
    act(() => { el.dispatchEvent(pointer('pointerdown', { clientX: 10, clientY: 10 })) })
    act(() => { vi.advanceTimersByTime(LONG_PRESS_MS - 1) })
    expect(fired).toEqual([])
  })

  it('never fires for a mouse', () => {
    // A mouse user pressing and holding is not asking for anything, and a long
    // press that fired on a mouse would race the real contextmenu.
    const { el, fired } = setup()
    act(() => { el.dispatchEvent(pointer('pointerdown', { clientX: 5, clientY: 5, pointerType: 'mouse' })) })
    act(() => { vi.advanceTimersByTime(LONG_PRESS_MS * 3) })
    expect(fired).toEqual([])
  })

  it('cancels when the finger travels — a scroll is not a press', () => {
    // Without this every flick down a list fires a long press on whatever row
    // it started on.
    const { el, fired } = setup()
    act(() => { el.dispatchEvent(pointer('pointerdown', { clientX: 100, clientY: 400 })) })
    act(() => { el.dispatchEvent(pointer('pointermove', { clientX: 100, clientY: 400 + LONG_PRESS_SLOP + 1 })) })
    act(() => { vi.advanceTimersByTime(LONG_PRESS_MS * 2) })
    expect(fired).toEqual([])
  })

  it('tolerates a tremor inside the slop', () => {
    const { el, fired } = setup()
    act(() => { el.dispatchEvent(pointer('pointerdown', { clientX: 100, clientY: 400 })) })
    act(() => { el.dispatchEvent(pointer('pointermove', { clientX: 100, clientY: 400 + LONG_PRESS_SLOP - 1 })) })
    act(() => { vi.advanceTimersByTime(LONG_PRESS_MS) })
    expect(fired).toHaveLength(1)
  })

  it('cancels on pointerup and on pointercancel', () => {
    for (const ending of ['pointerup', 'pointercancel']) {
      const { el, fired } = setup()
      act(() => { el.dispatchEvent(pointer('pointerdown', { clientX: 1, clientY: 1 })) })
      act(() => { el.dispatchEvent(pointer(ending, {})) })
      act(() => { vi.advanceTimersByTime(LONG_PRESS_MS * 2) })
      expect(fired, ending).toEqual([])
    }
  })

  it('swallows the click a touch synthesises after the press', () => {
    // Otherwise opening the menu would also select or navigate underneath it.
    const { el, fired } = setup()
    act(() => { el.dispatchEvent(pointer('pointerdown', { clientX: 9, clientY: 9 })) })
    act(() => { vi.advanceTimersByTime(LONG_PRESS_MS) })
    act(() => { el.dispatchEvent(new MouseEvent('click', { bubbles: true })) })
    expect(fired).toEqual([{ x: 9, y: 9 }])
  })

  it('lets a plain tap through', () => {
    const { el, fired } = setup()
    act(() => { el.dispatchEvent(pointer('pointerdown', { clientX: 9, clientY: 9 })) })
    act(() => { el.dispatchEvent(pointer('pointerup', {})) })
    act(() => { el.dispatchEvent(new MouseEvent('click', { bubbles: true })) })
    expect(fired).toEqual([{ x: -1, y: -1 }])
  })

  it('swallows the platform contextmenu that echoes our own press', () => {
    // Chrome on Android fires one; iOS Safari does not. Without the suppression
    // Android would open two menus stacked on each other.
    const { el, fired } = setup()
    act(() => { el.dispatchEvent(pointer('pointerdown', { clientX: 7, clientY: 7 })) })
    act(() => { vi.advanceTimersByTime(LONG_PRESS_MS) })
    act(() => { el.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true })) })
    expect(fired).toEqual([{ x: 7, y: 7 }])
  })

  it('still lets a real right-click through', () => {
    const { el, fired } = setup()
    act(() => { el.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true })) })
    expect(fired).toEqual([{ x: -2, y: -2 }])
  })

  it('does nothing at all when disabled', () => {
    const { el, fired } = setup(true)
    act(() => { el.dispatchEvent(pointer('pointerdown', { clientX: 3, clientY: 3 })) })
    act(() => { vi.advanceTimersByTime(LONG_PRESS_MS * 2) })
    expect(fired).toEqual([])
  })
})

describe('usePointerKind', () => {
  it('reports the pointer that produced the click, not the device', () => {
    // A touchscreen laptop reports (pointer: coarse) while the user drives a
    // mouse, which is why this reads the event rather than a media query.
    const seen: boolean[] = []
    function P() {
      const p = usePointerKind()
      return (
        <button
          data-testid="p"
          onPointerDown={p.track}
          onClick={() => seen.push(p.wasTouch())}
        >x</button>
      )
    }
    render(<P />)
    const el = screen.getByTestId('p')
    el.dispatchEvent(pointer('pointerdown', { pointerType: 'mouse' }))
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    el.dispatchEvent(pointer('pointerdown', { pointerType: 'touch' }))
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(seen).toEqual([false, true])
  })
})

describe('isTouchPointer', () => {
  it('counts a stylus as touch and a mouse as not', () => {
    expect(isTouchPointer({ pointerType: 'touch' })).toBe(true)
    expect(isTouchPointer({ pointerType: 'pen' })).toBe(true)
    expect(isTouchPointer({ pointerType: 'mouse' })).toBe(false)
    expect(isTouchPointer({})).toBe(false)
  })
})
