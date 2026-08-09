import { describe, it, expect } from 'vitest'
import { nextGraphSize } from '../builtin/activity/ActivityMonitor'

// activityGraphSize.test.ts — the fix for the Activity Monitor render loop.
//
// AreaGraph observes its own container with a ResizeObserver and stores the
// measured size in state. It used to do:
//
//     setSize({ w: width, h: height })
//
// A fresh object is never Object.is-equal to the previous one, so React could
// not bail out and re-rendered on EVERY observation. The re-render resized the
// observed SVG, which fed the observer again — a self-sustaining loop, one per
// graph, four graphs on screen.
//
// The damage was not confined to Activity Monitor. It starved the main thread
// badly enough that OTHER windows' React.lazy chunks never finished committing,
// leaving them on their "Loading..." Suspense fallback indefinitely. That is the
// long-standing blank-window bug: it presented as a race, but it never resolved
// with time and it followed whichever window was open alongside Activity
// Monitor. Two windows shipped empty in a generated screenshot because of it.
//
// The property that stops the loop is IDENTITY, not equality: an unchanged size
// must return the previous object itself so React's bail-out can fire. Asserting
// values only would pass against the broken version.

describe('nextGraphSize', () => {
  it('returns the SAME OBJECT when the size is unchanged', () => {
    const prev = { w: 200, h: 80 }
    const next = nextGraphSize(prev, 200, 80)

    // toBe, not toEqual: a structurally-equal but distinct object is exactly
    // the bug — React re-renders, the observer refires, and the loop runs.
    expect(next).toBe(prev)
  })

  it('returns a new object when either dimension changes', () => {
    const prev = { w: 200, h: 80 }

    const wider = nextGraphSize(prev, 240, 80)
    expect(wider).not.toBe(prev)
    expect(wider).toEqual({ w: 240, h: 80 })

    const taller = nextGraphSize(prev, 200, 96)
    expect(taller).not.toBe(prev)
    expect(taller).toEqual({ w: 200, h: 96 })
  })

  it('settles: re-observing a size it just adopted stops producing new objects', () => {
    // The loop's shape was observe → new object → render → observe. Feeding the
    // result straight back in must reach a fixed point immediately.
    let size = { w: 200, h: 80 }
    const grown = nextGraphSize(size, 512, 240)
    expect(grown).not.toBe(size)

    size = grown
    for (let i = 0; i < 5; i++) {
      const again = nextGraphSize(size, 512, 240)
      expect(again).toBe(size)
      size = again
    }
  })

  it('treats fractional sizes exactly, so sub-pixel jitter is not a new size', () => {
    // contentRect values are fractional. If a comparison rounded, a container
    // oscillating between 200.4 and 200.6 would look "unchanged" and the graph
    // would stop tracking; if it compared loosely the other way, jitter would
    // restart the loop. Exact comparison is the intended behaviour.
    const prev = { w: 200.5, h: 80.25 }
    expect(nextGraphSize(prev, 200.5, 80.25)).toBe(prev)
    expect(nextGraphSize(prev, 200.6, 80.25)).not.toBe(prev)
  })
})
