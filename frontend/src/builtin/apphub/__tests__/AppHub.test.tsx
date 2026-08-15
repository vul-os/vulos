// AppHub.test.tsx — the App Hub browse + search + install UI.
//
// The layout itself is guarded by e2e/apphub-responsive.e2e.ts, which measures
// rendered geometry in a real browser; jsdom has no layout engine and no
// ResizeObserver, so an assertion about columns or widths made here would be
// about nothing. What this file guards is the BEHAVIOUR underneath: filtering,
// the four dead-end states, and the states a failing backend puts the UI in.

import { render, screen, fireEvent, cleanup, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { App } from '../../../core/AppRegistry'

vi.mock('../../../core/AppRegistry', () => ({ refreshInstalled: vi.fn<() => Promise<App[]>>() }))

import AppHub from '../AppHub'
import { hubModeFor } from '../hubMode'

type Route = (url: string) => Response

function mockBackend(routes: Record<string, Route>) {
  // LONGEST fragment first. '/api/store/registry' is a prefix of
  // '/api/store/registry/install', so insertion order alone routed the install
  // POST to the catalogue handler — which answers 200, so a test asserting an
  // install FAILURE watched a success render and blamed the component.
  const table = Object.entries(routes).sort((a, b) => b[0].length - a[0].length)
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const u = String(input)
    for (const [frag, fn] of table) if (u.includes(frag)) return fn(u)
    return new Response(JSON.stringify({}), { status: 200 })
  }))
}

function ok(body: unknown, status = 200) {
  return () => new Response(JSON.stringify(body), { status })
}

function mockRegistry(apps: unknown, installed: unknown = []) {
  mockBackend({
    '/api/store/registry': ok(apps),
    '/api/store/installed': ok(installed),
    '/api/packages/cache': ok({ ready: true, arch: 'amd64' }),
  })
}

afterEach(() => cleanup())
beforeEach(() => vi.clearAllMocks())

const searchBox = () => screen.getByPlaceholderText('Search apps')

/** The card an app's name sits in. Scoping matters: the filter rail carries the
 *  same words ("Web", "Streamed", "Service") as the card badges, so an unscoped
 *  query passes no matter what a card renders — the first version of the badge
 *  tests below did exactly that and survived a mutation removing the badge. */
const cardFor = (name: string) => screen.getByText(name).closest('article') as HTMLElement

// The GET /api/store/registry wire shape (services/appnet/registry.go's
// RegistryListEntry) — a store-only shape AppHub.tsx's local StoreApp narrows
// from, not the shell-launcher `App` type.
interface RegistryFixtureApp {
  id: string
  name: string
  type: string
  arch: string[]
  description: string
  category: string
  keywords: string[]
  versions: string[]
  latest: string
  installed: boolean
}

const APPS: RegistryFixtureApp[] = [
  {
    id: 'conduit',
    name: 'Conduit',
    type: 'service',
    arch: ['amd64'],
    description: 'Lightweight Matrix homeserver written in Rust',
    category: 'network',
    keywords: ['matrix', 'homeserver', 'federation', 'self-hosted'],
    versions: ['0.5.9'],
    latest: '0.5.9',
    installed: false,
  },
  {
    id: 'darktable',
    name: 'Darktable',
    type: 'desktop',
    arch: ['amd64'],
    description: 'Photography workflow and RAW developer',
    category: 'media',
    keywords: ['photography', 'raw', 'editing'],
    versions: ['latest'],
    latest: 'latest',
    installed: false,
  },
]

describe('browsing and search', () => {
  it('shows every app with no search query', async () => {
    mockRegistry(APPS)
    render(<AppHub />)
    expect(await screen.findByText('Conduit')).toBeInTheDocument()
    expect(screen.getByText('Darktable')).toBeInTheDocument()
  })

  it('matches a search term against keywords even when it does not appear in the name or description', async () => {
    mockRegistry(APPS)
    render(<AppHub />)
    await screen.findByText('Conduit')

    // "federation" appears only in conduit's keywords, not its name/description.
    fireEvent.change(searchBox(), { target: { value: 'federation' } })

    expect(await screen.findByText('Conduit')).toBeInTheDocument()
    expect(screen.queryByText('Darktable')).not.toBeInTheDocument()
  })

  it('still matches on name and description as before', async () => {
    mockRegistry(APPS)
    render(<AppHub />)
    await screen.findByText('Conduit')

    fireEvent.change(searchBox(), { target: { value: 'raw developer' } })

    expect(await screen.findByText('Darktable')).toBeInTheDocument()
    expect(screen.queryByText('Conduit')).not.toBeInTheDocument()
  })

  it('is case-insensitive and tolerant of apps with no keywords at all', async () => {
    mockRegistry([{ ...APPS[1], keywords: undefined }])
    render(<AppHub />)
    await screen.findByText('Darktable')

    // keywords is undefined here — matching must not throw when it falls
    // through to the (app.keywords || []).some(...) keyword check, and the
    // uppercase query must still match case-insensitively via the description.
    fireEvent.change(searchBox(), { target: { value: 'PHOTOGRAPHY' } })
    expect(await screen.findByText('Darktable')).toBeInTheDocument()

    fireEvent.change(searchBox(), { target: { value: 'zzznomatch' } })
    expect(screen.queryByText('Darktable')).not.toBeInTheDocument()
  })

  it('matches on author, which is shown in the panel and was not searchable', async () => {
    mockRegistry([{ ...APPS[0], author: 'Timo Kösters' }])
    render(<AppHub />)
    await screen.findByText('Conduit')

    fireEvent.change(searchBox(), { target: { value: 'kösters' } })
    expect(await screen.findByText('Conduit')).toBeInTheDocument()
  })

  it('reports how many apps the current view is showing', async () => {
    mockRegistry(APPS)
    render(<AppHub />)
    await screen.findByText('Conduit')
    expect(screen.getByText('2 apps')).toBeInTheDocument()

    fireEvent.change(searchBox(), { target: { value: 'federation' } })
    expect(await screen.findByText('1 app')).toBeInTheDocument()
  })
})

describe('badges', () => {
  // A web app used to render TWO badges reading the same word — "WEB WEB" — one
  // from SourceBadge (where it comes from) and one from AppTypeBadge (how it
  // runs). Both axes resolve to "Web" for a web app, so the card repeated the
  // same fact twice; on the 63-app catalogue grid it was the most repeated text
  // on screen. Caught by opening docs/screenshots/apphub.png, not by any test.
  it('shows the Web badge once, not twice, for a web app', async () => {
    mockRegistry([{ ...APPS[0], id: 'gitea', name: 'Gitea', type: 'web', description: 'Self-hosted Git forge' }])
    render(<AppHub />)
    await screen.findByText('Gitea')

    const webBadges = within(cardFor('Gitea'))
      .getAllByText(/^Web$/i)
    expect(webBadges).toHaveLength(1)
  })

  // The suppression must be narrow: when the two badges carry DIFFERENT facts
  // they both still render, because "Apt · Streamed" genuinely says two things.
  it('still shows both badges when they say different things', async () => {
    mockRegistry([APPS[1]]) // darktable: type 'desktop' → Apt + Streamed
    render(<AppHub />)
    await screen.findByText('Darktable')

    const card = within(cardFor('Darktable'))
    expect(card.getByText('Apt')).toBeInTheDocument()
    expect(card.getByText('Streamed')).toBeInTheDocument()
  })
})

describe('category labels', () => {
  // The shipped registry contains `games`, `storage` and `development`, none of
  // which are in CATEGORY_LABELS. The rail rendered them as bare lowercase
  // slugs with no icon between "Productivity" and "System", so three of eight
  // rows looked like a rendering fault.
  it('title-cases a category the label table has never heard of', async () => {
    mockRegistry([{ ...APPS[0], category: 'storage' }])
    render(<AppHub />)
    await screen.findByText('Conduit')

    expect(screen.getAllByText('Storage').length).toBeGreaterThan(0)
    expect(screen.queryByText('storage')).not.toBeInTheDocument()
  })

  it('counts the apps in each category', async () => {
    mockRegistry([APPS[0], { ...APPS[1], category: 'network' }])
    render(<AppHub />)
    await screen.findByText('Conduit')

    const network = screen.getAllByRole('button', { name: /Network/ })[0]
    expect(within(network).getByText('2')).toBeInTheDocument()
  })
})

describe('states nobody designed', () => {
  it('distinguishes a failed catalogue fetch from an empty catalogue', async () => {
    // Before this, the catch set apps to [] and the grid said "No apps match
    // your search" — a box with no network told the user its store was empty.
    mockBackend({
      '/api/store/registry': ok({ error: 'boom' }, 503),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true, arch: 'amd64' }),
    })
    render(<AppHub />)

    expect(await screen.findByText(/could not be loaded/i)).toBeInTheDocument()
    expect(screen.queryByText(/No apps match/i)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })

  it('offers a way out of an empty search rather than a dead end', async () => {
    mockRegistry(APPS)
    render(<AppHub />)
    await screen.findByText('Conduit')

    fireEvent.change(searchBox(), { target: { value: 'zzznomatch' } })
    expect(await screen.findByText(/No apps match/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }))
    expect(await screen.findByText('Conduit')).toBeInTheDocument()
  })

  it('sends someone with nothing installed to the catalogue', async () => {
    mockRegistry(APPS)
    render(<AppHub />)
    await screen.findByText('Conduit')

    fireEvent.click(screen.getByRole('tab', { name: /Installed/ }))
    expect(await screen.findByText('Nothing installed yet')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Browse the catalogue' }))
    expect(await screen.findByText('Conduit')).toBeInTheDocument()
  })

  it('renders an app with no description without leaving an empty line', async () => {
    mockRegistry([{ ...APPS[0], description: '' }])
    render(<AppHub />)
    await screen.findByText('Conduit')
    expect(cardFor('Conduit').querySelector('.hub-card-desc')).toBeNull()
  })
})

describe('install failures are visible', () => {
  // The failure this closes: `error` was rendered ONLY inside the detail panel,
  // so pressing Get on a card with no panel open and having the install fail
  // showed the user nothing whatsoever.
  it('surfaces an install error raised from a card, with no panel open', async () => {
    mockBackend({
      '/api/store/registry': ok(APPS),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true, arch: 'amd64' }),
      '/api/store/registry/install': ok({ error: 'no such package', detail: 'apt-get exited 100' }, 500),
    })
    render(<AppHub />)
    await screen.findByText('Conduit')

    expect(screen.queryByRole('dialog')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Install Conduit' }))

    await waitFor(() => expect(screen.getByText(/no such package/)).toBeInTheDocument())
    expect(screen.getByText(/apt-get exited 100/)).toBeInTheDocument()
  })

  it('confirms a successful install where the user is looking', async () => {
    mockBackend({
      '/api/store/registry': ok(APPS),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true, arch: 'amd64' }),
      '/api/store/registry/install': ok({ ok: true }),
    })
    render(<AppHub />)
    await screen.findByText('Conduit')

    fireEvent.click(screen.getByRole('button', { name: 'Install Conduit' }))
    await waitFor(() => expect(screen.getByText('Conduit installed')).toBeInTheDocument())
  })
})

describe('architecture', () => {
  it('marks an app this box cannot run instead of offering to install it', async () => {
    mockBackend({
      '/api/store/registry': ok([{ ...APPS[0], arch: ['ppc64el'] }]),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true, arch: 'amd64' }),
    })
    render(<AppHub />)
    await screen.findByText('Conduit')

    expect(within(cardFor('Conduit')).getByText('Unavailable')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install Conduit' })).toBeNull()
  })
})

describe('hubModeFor', () => {
  // The layout is chosen from the HUB's width, not the viewport's. These are the
  // exact thresholds e2e/apphub-responsive.e2e.ts drives real pixels through, so
  // a change to one has to be a change to both.
  it('picks a layout from the hub width, including the widths a dragged window produces', () => {
    expect(hubModeFor(1440)).toBe('wide')
    expect(hubModeFor(1080)).toBe('wide')
    expect(hubModeFor(1079)).toBe('mid')
    expect(hubModeFor(700)).toBe('mid')
    expect(hubModeFor(699)).toBe('compact')
    expect(hubModeFor(440)).toBe('compact')
    expect(hubModeFor(439)).toBe('narrow')
    expect(hubModeFor(320)).toBe('narrow')
    // A 442px window on a 1440px screen — the case the old viewport media
    // queries answered with a three-column grid.
    expect(hubModeFor(442)).toBe('compact')
  })
})
