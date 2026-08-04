import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// launchApp is the single shared app-launch path. These tests pin the GAME-07
// contract: a GPURoute launch goes through the auto-detecting
// POST /api/stream/launch-app so gaming mode engages ONLY for real games, and
// the resolved `gaming` flag is passed to StreamViewer (so pointer-lock + split
// input channels activate for games only). A plain desktop CPUStream launch must
// NOT hit launch-app and must NOT engage gaming. FPS is no longer hardcoded to 30.

// Keep the builtin/native branches out of the way so every test app reaches the
// stream lane.
vi.mock('../shell/builtinApps', () => ({
  builtinComponent: () => null,
  isBuiltinComponent: () => false,
  BUILTIN_SINGLETONS: new Set(),
}))
vi.mock('../core/useNativeMode', () => ({ getNativeMode: () => 'web' }))
// StreamViewer is lazy-imported; stub it so no real WebRTC module loads. We only
// inspect the element's props, never render it.
vi.mock('../builtin/stream/StreamViewer', () => ({ default: () => null }))

import { launchApp } from '../shell/launchApp'

// Extract the StreamViewer element (child of the Suspense wrapper) from a
// captured openWindow() call, so we can read its props (sessionId, gaming).
function streamViewerProps(win) {
  const suspense = win.component
  return suspense?.props?.children?.props || null
}

describe('launchApp — GAME-07 gaming-mode engagement', () => {
  let calls
  let laneByApp
  let launchAppGaming

  beforeEach(() => {
    calls = []
    laneByApp = {}
    launchAppGaming = false
    global.fetch = vi.fn(async (url, opts) => {
      calls.push({ url, body: opts?.body ? JSON.parse(opts.body) : null })
      if (url.startsWith('/api/router/classify')) {
        const app = new URL(url, 'http://x').searchParams.get('app')
        return { ok: true, json: async () => ({ lane: laneByApp[app] || 'CPUStream' }) }
      }
      if (url === '/api/stream/launch-app') {
        return { ok: true, json: async () => ({ id: 'sess', gaming: launchAppGaming }) }
      }
      if (url === '/api/stream/launch') {
        return { ok: true, json: async () => ({ id: 'sess' }) }
      }
      return { ok: true, json: async () => ({}) }
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('routes a GPURoute game through launch-app and engages gaming when detected', async () => {
    laneByApp['steam'] = 'GPURoute'
    launchAppGaming = true // backend auto-detected a real game
    let win = null
    await launchApp({ id: 'steam', name: 'Steam', type: 'desktop', command: 'steam' }, { openWindow: (w) => { win = w } })

    const urls = calls.map((c) => c.url)
    expect(urls).toContain('/api/stream/launch-app')
    expect(urls).not.toContain('/api/stream/launch')
    // gaming flag from the response is passed to the viewer
    expect(streamViewerProps(win).gaming).toBe(true)
  })

  it('routes a GPURoute non-game (GPU app) through launch-app but does NOT engage gaming', async () => {
    laneByApp['blender'] = 'GPURoute'
    launchAppGaming = false // backend did NOT detect a game
    let win = null
    await launchApp({ id: 'blender', name: 'Blender', type: 'desktop', command: 'blender' }, { openWindow: (w) => { win = w } })

    expect(calls.map((c) => c.url)).toContain('/api/stream/launch-app')
    expect(streamViewerProps(win).gaming).toBe(false)
  })

  it('routes a plain desktop CPUStream app through /api/stream/launch and never engages gaming', async () => {
    laneByApp['kicad'] = 'CPUStream'
    let win = null
    await launchApp({ id: 'kicad', name: 'KiCad', type: 'desktop', command: 'kicad' }, { openWindow: (w) => { win = w } })

    const urls = calls.map((c) => c.url)
    expect(urls).toContain('/api/stream/launch')
    expect(urls).not.toContain('/api/stream/launch-app')
    expect(streamViewerProps(win).gaming).toBe(false)
  })

  it('no longer hardcodes fps to 30 for either lane', async () => {
    laneByApp['kicad'] = 'CPUStream'
    await launchApp({ id: 'kicad', name: 'KiCad', type: 'desktop', command: 'kicad' }, { openWindow: () => {} })
    const launch = calls.find((c) => c.url === '/api/stream/launch')
    expect(launch.body.fps).not.toBe(30)
    expect(launch.body.fps).toBe(60)

    calls = []
    laneByApp['steam'] = 'GPURoute'
    await launchApp({ id: 'steam', name: 'Steam', type: 'desktop', command: 'steam' }, { openWindow: () => {} })
    const launchApp2 = calls.find((c) => c.url === '/api/stream/launch-app')
    expect(launchApp2.body.fps).not.toBe(30)
    expect(launchApp2.body.fps).toBe(60)
  })
})
