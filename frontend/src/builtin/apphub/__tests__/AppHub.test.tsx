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

  // An empty catalogue used to fall through to the FILTER dead end: with no
  // search term and no filter set, the hub said "Nothing in this filter — try a
  // different word, or widen the category and type filters" and offered a
  // "Clear filters" button that could not change anything, because there was
  // nothing set to clear. Every instruction on screen was for a state the user
  // was not in.
  it('says the catalogue is empty rather than blaming filters that are not set', async () => {
    mockRegistry([])
    render(<AppHub />)

    expect(await screen.findByText('The catalogue is empty')).toBeInTheDocument()
    expect(screen.queryByText(/Nothing in this filter/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Clear filters' })).toBeNull()
  })

  it('offers a refetch from the empty catalogue, and shows apps when they arrive', async () => {
    // The registry answering empty is usually transient — it may still be
    // syncing — so the action has to actually re-ask, not just re-render.
    mockRegistry([])
    render(<AppHub />)
    await screen.findByText('The catalogue is empty')

    mockRegistry(APPS)
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    expect(await screen.findByText('Conduit')).toBeInTheDocument()
  })

  // The empty-catalogue branch must not swallow the filter branch: an empty
  // RESULT inside a non-empty catalogue is still the user's filter.
  it('still blames the search when the catalogue has apps but the query matches none', async () => {
    mockRegistry(APPS)
    render(<AppHub />)
    await screen.findByText('Conduit')

    fireEvent.change(searchBox(), { target: { value: 'zzznomatch' } })
    expect(await screen.findByText(/No apps match/)).toBeInTheDocument()
    expect(screen.queryByText('The catalogue is empty')).toBeNull()
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
  /** A box of a given architecture, serving one app. */
  function boxWith(boxArch: string, app: Partial<RegistryFixtureApp> & { arch: string[] }) {
    mockBackend({
      '/api/store/registry': ok([{ ...APPS[0], ...app }]),
      '/api/store/installed': ok([]),
      // The apt-cache endpoint is the FALLBACK source of the box's
      // architecture; /api/system/arch is preferred and does not exist yet, so
      // it 404s through mockBackend's catch-all and this is what is read.
      '/api/packages/cache': ok({ ready: true, arch: boxArch }),
    })
  }

  it('marks an app this box cannot run instead of offering to install it', async () => {
    boxWith('amd64', { arch: ['ppc64el'] })
    render(<AppHub />)
    await screen.findByText('Conduit')

    expect(within(cardFor('Conduit')).getByText(/Needs ppc64el/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install Conduit' })).toBeNull()
  })

  // ── The spelling trap ─────────────────────────────────────────────────────
  //
  // Debian says amd64/arm64; Flatpak, `uname -m` and Flathub metadata say
  // x86_64/aarch64. The comparison this replaced was a raw
  // `app.arch.includes(systemArch)`, so an x86_64-only Flathub app read as
  // INCOMPATIBLE on an amd64 box — silently, with no error, across most of the
  // desktop catalogue. These two tests are the founder's requirement stated as
  // assertions: an arm64 box must not offer an x86_64-only app, and an amd64
  // box must.

  it('offers an x86_64-only app on an amd64 box, despite the different spelling', async () => {
    boxWith('amd64', { id: 'lutris', name: 'Lutris', arch: ['x86_64'] })
    render(<AppHub />)
    await screen.findByText('Lutris')

    expect(screen.getByRole('button', { name: 'Install Lutris' })).toBeInTheDocument()
    expect(within(cardFor('Lutris')).queryByText(/Needs/)).toBeNull()
  })

  it('refuses an x86_64-only app on an arm64 box, and names what it needs', async () => {
    boxWith('arm64', { id: 'lutris', name: 'Lutris', arch: ['x86_64'] })
    render(<AppHub />)
    await screen.findByText('Lutris')

    // Shown, not hidden — "why can't I find Lutris?" is answered on the card.
    expect(within(cardFor('Lutris')).getByText('Needs amd64')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install Lutris' })).toBeNull()
  })

  it('reads the box architecture in uname spelling too', async () => {
    // A backend answering `uname -m` rather than `dpkg --print-architecture`
    // must not silently mark the whole catalogue incompatible.
    boxWith('aarch64', { id: 'lutris', name: 'Lutris', arch: ['arm64'] })
    render(<AppHub />)
    await screen.findByText('Lutris')

    expect(screen.getByRole('button', { name: 'Install Lutris' })).toBeInTheDocument()
  })

  it('shows the box architecture, so the user can see what decides this', async () => {
    boxWith('arm64', { arch: ['arm64'] })
    render(<AppHub />)
    await screen.findByText('Conduit')
    expect(screen.getByText('arm64')).toBeInTheDocument()
  })

  it('offers a filter to hide what this box cannot run, and it is off by default', async () => {
    mockBackend({
      '/api/store/registry': ok([
        { ...APPS[0], id: 'lutris', name: 'Lutris', arch: ['x86_64'] },
        { ...APPS[1], arch: ['arm64'] },
      ]),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true, arch: 'arm64' }),
    })
    render(<AppHub />)
    await screen.findByText('Darktable')

    // Default: BOTH are on screen. An app that vanishes teaches nothing.
    expect(screen.getByText('Lutris')).toBeInTheDocument()

    fireEvent.click(screen.getAllByRole('button', { name: /Runs on/ })[0])
    await waitFor(() => expect(screen.queryByText('Lutris')).toBeNull())
    expect(screen.getByText('Darktable')).toBeInTheDocument()
  })

  it('never hides a searched-for app behind the compatibility filter', async () => {
    // Someone typing "lutris" is asking about Lutris specifically. Answering
    // "no results" — on a box that simply cannot run it — is the exact failure
    // that showing-with-a-reason exists to avoid.
    mockBackend({
      '/api/store/registry': ok([
        { ...APPS[0], id: 'lutris', name: 'Lutris', arch: ['x86_64'] },
        { ...APPS[1], arch: ['arm64'] },
      ]),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true, arch: 'arm64' }),
    })
    render(<AppHub />)
    await screen.findByText('Lutris')

    fireEvent.click(screen.getAllByRole('button', { name: /Runs on/ })[0])
    await waitFor(() => expect(screen.queryByText('Lutris')).toBeNull())

    fireEvent.change(searchBox(), { target: { value: 'lutris' } })
    expect(await screen.findByText('Lutris')).toBeInTheDocument()
    expect(within(cardFor('Lutris')).getByText('Needs amd64')).toBeInTheDocument()
  })

  it('sorts what this box cannot run below what it can', async () => {
    mockBackend({
      '/api/store/registry': ok([
        // Alphabetically first, but incompatible.
        { ...APPS[0], id: 'aaa-lutris', name: 'AAA Lutris', arch: ['x86_64'] },
        { ...APPS[1], id: 'zzz-ok', name: 'ZZZ Runs Here', arch: ['arm64'] },
      ]),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true, arch: 'arm64' }),
    })
    render(<AppHub />)
    await screen.findByText('AAA Lutris')

    // Read the GRID's own order. A role query by name also matches each card's
    // "Install <name>" button, which is a different control and says nothing
    // about card order.
    const order = [...document.querySelectorAll('article.hub-card')]
      .map(c => c.getAttribute('data-app-id'))
    expect(order).toEqual(['zzz-ok', 'aaa-lutris'])
  })

  it('makes no compatibility claim when the box has not reported an architecture', async () => {
    // Claiming "yes" offers an install that fails in apt; claiming "no" marks
    // the whole catalogue unavailable on every backend that does not report
    // architecture — which is every backend today.
    mockBackend({
      '/api/store/registry': ok([{ ...APPS[0], arch: ['amd64'] }]),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true, arch: null }),
    })
    render(<AppHub />)
    await screen.findByText('Conduit')

    expect(within(cardFor('Conduit')).queryByText(/Needs/)).toBeNull()
    expect(screen.getByRole('button', { name: 'Install Conduit' })).toBeInTheDocument()
  })
})

describe('the detail panel', () => {
  // jsdom has no ResizeObserver, so useHubMode holds its `wide` fallback and
  // this exercises the DOCKED panel. The modal sheet's counterpart — that it is
  // announced as a dialog and carries the back affordance instead — is asserted
  // in e2e/apphub-responsive.e2e.ts, where a real browser can produce the width.
  it('offers exactly one way to dismiss the docked panel', async () => {
    // It had two, calling the identical handler: a "Back to the app list"
    // chevron and a "Close details" cross, side by side in a 40px bar. Two
    // controls for one action is not a convenience — a reader has to work out
    // what the difference is, and there is none.
    mockRegistry(APPS)
    render(<AppHub />)
    await screen.findByText('Conduit')

    fireEvent.click(screen.getByRole('button', { name: 'Conduit' }))
    const panel = await screen.findByRole('complementary')

    expect(within(panel).queryByRole('button', { name: 'Back to the app list' })).toBeNull()
    const close = within(panel).getByRole('button', { name: 'Close details' })

    fireEvent.click(close)
    await waitFor(() => expect(screen.queryByRole('complementary')).toBeNull())
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
