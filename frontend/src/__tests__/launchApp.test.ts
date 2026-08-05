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

import { launchApp, type LaunchAppDeps } from '../shell/launchApp'
import type { AppEntry } from '../shell/appTypes'

// The window shape captured from an openWindow() call.
type CapturedWindow = Parameters<LaunchAppDeps['openWindow']>[0]

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// Extract the StreamViewer element (child of the Suspense wrapper) from a
// captured openWindow() call, so we can read its props (sessionId, gaming).
function streamViewerProps(win: CapturedWindow | null): Record<string, unknown> | null {
  const suspense = win?.component
  if (!suspense) return null
  const suspenseProps = suspense.props
  if (!isRecord(suspenseProps)) return null
  const children = suspenseProps.children
  if (!isRecord(children)) return null
  const childProps = children.props
  return isRecord(childProps) ? childProps : null
}

interface FetchCall {
  url: string
  body: { fps?: number, [key: string]: unknown } | null
}

// The gaming-mode test apps only exercise launchApp's routing logic — the
// AppEntry fields below that logic doesn't read (icon/description/keywords/
// category) are filled with inert placeholders purely to satisfy the type.
function testApp(overrides: Partial<AppEntry> & Pick<AppEntry, 'id' | 'name'>): AppEntry {
  return { icon: '', description: '', keywords: [], category: 'test', ...overrides }
}

function expectFetchBody(call: FetchCall | undefined): { fps?: number, [key: string]: unknown } {
  if (!call) throw new Error('expected a matching fetch call')
  if (!call.body) throw new Error('expected the matched fetch call to have a JSON body')
  return call.body
}

describe('launchApp — GAME-07 gaming-mode engagement', () => {
  let calls: FetchCall[]
  let laneByApp: Record<string, string>
  let launchAppGaming: boolean

  beforeEach(() => {
    calls = []
    laneByApp = {}
    launchAppGaming = false
    vi.stubGlobal('fetch', vi.fn(async (url: string, opts?: RequestInit) => {
      calls.push({ url, body: typeof opts?.body === 'string' ? JSON.parse(opts.body) : null })
      if (url.startsWith('/api/router/classify')) {
        const app = new URL(url, 'http://x').searchParams.get('app') || ''
        return { ok: true, json: async () => ({ lane: laneByApp[app] || 'CPUStream' }) }
      }
      if (url === '/api/stream/launch-app') {
        return { ok: true, json: async () => ({ id: 'sess', gaming: launchAppGaming }) }
      }
      if (url === '/api/stream/launch') {
        return { ok: true, json: async () => ({ id: 'sess' }) }
      }
      return { ok: true, json: async () => ({}) }
    }))
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('routes a GPURoute game through launch-app and engages gaming when detected', async () => {
    laneByApp['steam'] = 'GPURoute'
    launchAppGaming = true // backend auto-detected a real game
    let win: CapturedWindow | null = null
    await launchApp(testApp({ id: 'steam', name: 'Steam', type: 'desktop', command: 'steam' }), { openWindow: (w) => { win = w } })

    const urls = calls.map((c) => c.url)
    expect(urls).toContain('/api/stream/launch-app')
    expect(urls).not.toContain('/api/stream/launch')
    // gaming flag from the response is passed to the viewer
    expect(streamViewerProps(win)?.gaming).toBe(true)
  })

  it('routes a GPURoute non-game (GPU app) through launch-app but does NOT engage gaming', async () => {
    laneByApp['blender'] = 'GPURoute'
    launchAppGaming = false // backend did NOT detect a game
    let win: CapturedWindow | null = null
    await launchApp(testApp({ id: 'blender', name: 'Blender', type: 'desktop', command: 'blender' }), { openWindow: (w) => { win = w } })

    expect(calls.map((c) => c.url)).toContain('/api/stream/launch-app')
    expect(streamViewerProps(win)?.gaming).toBe(false)
  })

  it('routes a plain desktop CPUStream app through /api/stream/launch and never engages gaming', async () => {
    laneByApp['kicad'] = 'CPUStream'
    let win: CapturedWindow | null = null
    await launchApp(testApp({ id: 'kicad', name: 'KiCad', type: 'desktop', command: 'kicad' }), { openWindow: (w) => { win = w } })

    const urls = calls.map((c) => c.url)
    expect(urls).toContain('/api/stream/launch')
    expect(urls).not.toContain('/api/stream/launch-app')
    expect(streamViewerProps(win)?.gaming).toBe(false)
  })

  it('no longer hardcodes fps to 30 for either lane', async () => {
    laneByApp['kicad'] = 'CPUStream'
    await launchApp(testApp({ id: 'kicad', name: 'KiCad', type: 'desktop', command: 'kicad' }), { openWindow: () => {} })
    const launch = expectFetchBody(calls.find((c) => c.url === '/api/stream/launch'))
    expect(launch.fps).not.toBe(30)
    expect(launch.fps).toBe(60)

    calls = []
    laneByApp['steam'] = 'GPURoute'
    await launchApp(testApp({ id: 'steam', name: 'Steam', type: 'desktop', command: 'steam' }), { openWindow: () => {} })
    const launchApp2 = expectFetchBody(calls.find((c) => c.url === '/api/stream/launch-app'))
    expect(launchApp2.fps).not.toBe(30)
    expect(launchApp2.fps).toBe(60)
  })
})
