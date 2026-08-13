import { describe, it, expect, afterEach, vi } from 'vitest'
import {
  viewportPx,
  readViewportSize,
  readDevicePixelRatio,
  toCanonical,
  fromCanonical,
  toCanonicalPoint,
  fromCanonicalPoint,
  toCanonicalSize,
  fromCanonicalSize,
  isPlausibleCanonicalUnit,
  type ViewportSize,
} from '../screenScale'

// The scenario this module exists for: a 4K screen beside a 1080p one, one
// browser instance per output, sharing the same session (roadmap/SCREENS.md
// "Per-output scale"). Every test below uses TWO DIFFERENT viewports on
// purpose — a suite where every viewport has the same extent cannot tell a
// correct conversion apart from one that just passes px through unchanged.
const FOUR_K: ViewportSize = { widthPx: viewportPx(3840), heightPx: viewportPx(2160) }
const FULL_HD: ViewportSize = { widthPx: viewportPx(1920), heightPx: viewportPx(1080) }
// Deliberately non-square-ratio-preserving relative to the pair above, so a
// mutation that swaps width/height inside toCanonicalPoint/Size is caught by
// SOME case even if it happens to survive another.
const PORTRAIT_1080: ViewportSize = { widthPx: viewportPx(1080), heightPx: viewportPx(1920) }

describe('toCanonical / fromCanonical', () => {
  it('is the identity fraction at 0 and at the far edge', () => {
    expect(toCanonical(viewportPx(0), viewportPx(1920))).toBe(0)
    expect(toCanonical(viewportPx(1920), viewportPx(1920))).toBe(1)
  })

  it('round-trips through the SAME viewport', () => {
    const back = fromCanonical(toCanonical(viewportPx(1800), viewportPx(1920)), viewportPx(1920))
    expect(back).toBe(1800)
  })

  // The actual regression this module exists to prevent: a value written on
  // one viewport, read back through a DIFFERENT one.
  it('rescales a value written on a 4K viewport when read on a 1080p one', () => {
    // Dead center of the 4K writer.
    const canonical = toCanonical(viewportPx(1920), FOUR_K.widthPx)
    expect(canonical).toBeCloseTo(0.5)
    // Read back on the 1080p follower: still dead center of THAT screen.
    const px = fromCanonical(canonical, FULL_HD.widthPx)
    expect(px).toBe(960)
  })

  it('does not divide by zero on a degenerate extent', () => {
    expect(toCanonical(viewportPx(500), viewportPx(0))).toBe(0)
  })
})

describe('toCanonicalPoint / fromCanonicalPoint — the 4K-beside-1080p case', () => {
  it('keeps a centered window centered across mismatched viewports', () => {
    const centerOn4K = { x: viewportPx(1920), y: viewportPx(1080) }
    const canonical = toCanonicalPoint(centerOn4K, FOUR_K)
    expect(canonical).toEqual({ nx: 0.5, ny: 0.5 })

    const onFullHD = fromCanonicalPoint(canonical, FULL_HD)
    expect(onFullHD).toEqual({ x: 960, y: 540 })
  })

  // This is the test that would fail if the conversion were skipped and raw
  // px were broadcast as-is (today's shape, per ShellProvider.tsx's
  // serializableShellState / useShellSession.publish): x=1920 unconverted
  // lands at the FAR RIGHT EDGE of a 1920-wide follower instead of its
  // center, and this assertion tells the two apart.
  it('a raw-px passthrough would have placed the window at the wrong spot — this proves the conversion actually runs', () => {
    const centerOn4K = { x: viewportPx(1920), y: viewportPx(1080) }
    const converted = fromCanonicalPoint(toCanonicalPoint(centerOn4K, FOUR_K), FULL_HD)
    const naivePassthrough = centerOn4K // what shared state carries today, unconverted
    expect(converted).not.toEqual(naivePassthrough)
    expect(converted).toEqual({ x: 960, y: 540 })
  })

  it('a near-edge window on the 4K writer stays near the same edge on the follower, not off-screen', () => {
    // 90% across, 5% down on the 4K writer.
    const nearEdge = { x: viewportPx(3456), y: viewportPx(108) }
    const canonical = toCanonicalPoint(nearEdge, FOUR_K)
    const onFullHD = fromCanonicalPoint(canonical, FULL_HD)
    expect(onFullHD.x).toBeCloseTo(1728) // 90% of 1920
    expect(onFullHD.y).toBeCloseTo(54) // 5% of 1080
    // Still on screen, unlike the raw px value (3456) would be.
    expect(onFullHD.x).toBeLessThan(FULL_HD.widthPx)
  })

  it('a value shared with a portrait follower converts axis-correctly, catching a width/height swap', () => {
    // Quarter across, three-quarters down on the 4K writer.
    const p = { x: viewportPx(960), y: viewportPx(1620) }
    const canonical = toCanonicalPoint(p, FOUR_K)
    expect(canonical).toEqual({ nx: 0.25, ny: 0.75 })
    const onPortrait = fromCanonicalPoint(canonical, PORTRAIT_1080)
    // Quarter of the portrait viewport's WIDTH (1080), three-quarters of its
    // HEIGHT (1920) — a width/height swap in the implementation would produce
    // {x: 810, y: 480} instead.
    expect(onPortrait).toEqual({ x: 270, y: 1440 })
  })

  it('round-trips through the SAME viewport unchanged', () => {
    const p = { x: viewportPx(200), y: viewportPx(700) }
    const back = fromCanonicalPoint(toCanonicalPoint(p, FULL_HD), FULL_HD)
    expect(back).toEqual(p)
  })
})

describe('toCanonicalSize / fromCanonicalSize', () => {
  it('rescales a window sized on 4K to the equivalent fraction of a 1080p follower', () => {
    const sizeOn4K = { width: viewportPx(1920), height: viewportPx(1080) } // half-width, half-height on 4K
    const canonical = toCanonicalSize(sizeOn4K, FOUR_K)
    expect(canonical).toEqual({ nw: 0.5, nh: 0.5 })
    const onFullHD = fromCanonicalSize(canonical, FULL_HD)
    // Half of 1920x1080, NOT the raw 1920x1080 passed through (which would be
    // the whole 1080p screen, not half of it).
    expect(onFullHD).toEqual({ width: 960, height: 540 })
  })

  it('does not silently grow a window past its origin viewport’s relative footprint on a smaller follower', () => {
    const bigOn4K = { width: viewportPx(3840), height: viewportPx(2160) } // fills the 4K screen
    const onFullHD = fromCanonicalSize(toCanonicalSize(bigOn4K, FOUR_K), FULL_HD)
    expect(onFullHD).toEqual({ width: 1920, height: 1080 }) // fills the 1080p screen too, not 3840x2160
  })
})

describe('isPlausibleCanonicalUnit — the runtime backstop for branding erasure', () => {
  it('accepts real fractions, including small overhang off an edge', () => {
    expect(isPlausibleCanonicalUnit(0)).toBe(true)
    expect(isPlausibleCanonicalUnit(0.5)).toBe(true)
    expect(isPlausibleCanonicalUnit(-0.1)).toBe(true)
    expect(isPlausibleCanonicalUnit(1.2)).toBe(true)
  })

  it('rejects a raw viewport-px value mistakenly passed where a fraction was expected', () => {
    // This is exactly the shape of bug the branded types exist to catch at
    // compile time; this test is the runtime net for when branding erases.
    expect(isPlausibleCanonicalUnit(1920)).toBe(false)
    expect(isPlausibleCanonicalUnit(3840)).toBe(false)
  })

  it('rejects non-finite input', () => {
    expect(isPlausibleCanonicalUnit(NaN)).toBe(false)
    expect(isPlausibleCanonicalUnit(Infinity)).toBe(false)
  })
})

describe('readViewportSize', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('reads window.innerWidth/innerHeight when present (jsdom)', () => {
    const size = readViewportSize()
    expect(size).not.toBeNull()
    expect(size!.widthPx).toBeGreaterThan(0)
    expect(size!.heightPx).toBeGreaterThan(0)
  })

  it('returns null outside a browser — no window object', () => {
    const realWindow = globalThis.window
    // @ts-expect-error -- simulating an environment without `window`, e.g. SSR
    delete globalThis.window
    vi.stubGlobal('innerWidth', undefined)
    vi.stubGlobal('innerHeight', undefined)
    try {
      expect(readViewportSize()).toBeNull()
    } finally {
      globalThis.window = realWindow
    }
  })

  it('refuses a zero-extent viewport rather than dividing by it later', () => {
    vi.stubGlobal('innerWidth', 0)
    vi.stubGlobal('innerHeight', 1080)
    expect(readViewportSize()).toBeNull()
  })
})

describe('readDevicePixelRatio', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('reads window.devicePixelRatio when present', () => {
    vi.stubGlobal('devicePixelRatio', 2)
    expect(readDevicePixelRatio()).toBe(2)
  })

  it('returns null when absent', () => {
    vi.stubGlobal('devicePixelRatio', undefined)
    expect(readDevicePixelRatio()).toBeNull()
  })

  // The point of the module comment: DPR alone does not predict whether
  // conversion is needed. Two viewports can share a DPR and still need
  // rescaling (different physical resolutions at the same compositor scale).
  it('is NOT what toCanonical/fromCanonical key off of — same DPR, different extents, still needs conversion', () => {
    vi.stubGlobal('devicePixelRatio', 1)
    const dprA = readDevicePixelRatio()
    vi.stubGlobal('devicePixelRatio', 1)
    const dprB = readDevicePixelRatio()
    expect(dprA).toBe(dprB) // same DPR...
    // ...but FOUR_K and FULL_HD still convert non-trivially, proving extent
    // (not DPR) is the load-bearing quantity.
    const converted = fromCanonicalPoint(
      toCanonicalPoint({ x: viewportPx(1920), y: viewportPx(1080) }, FOUR_K),
      FULL_HD,
    )
    expect(converted).toEqual({ x: 960, y: 540 })
  })
})
