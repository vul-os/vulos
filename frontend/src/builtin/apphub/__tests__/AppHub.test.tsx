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
//
// `availability` is the BOX's verdict, computed by EvaluateArch. `arch` is still
// on the wire and is deliberately still in these fixtures: the hub must ignore
// it, and a fixture that dropped it could not prove that it does.
interface FixtureAvailability {
  state: string
  installable: boolean
  requires_emulation: boolean
  badge: string
  card_badge: string
  detail: string
  box_arch: string
  undeclared: boolean
  needs: string[]
  /**
   * The publisher-signature verdict: 'signed' | 'unsigned' | 'untrusted' |
   * 'uncheckable'. 55 of the 74 shipped entries carry no signature and the box
   * refuses to install every one of them, so a hub that offered them showed 55
   * Install buttons that could only fail.
   *
   * It rides this verdict rather than a mechanism of its own, and `state` stays
   * one of the four architecture rungs, so the hub's existing refusal path
   * renders it with no new branch and composes no sentence about signatures.
   */
  signature: string
}

interface RegistryFixtureApp {
  id: string
  name: string
  type: string
  arch: string[]
  availability: FixtureAvailability
  description: string
  category: string
  keywords: string[]
  versions: string[]
  latest: string
  installed: boolean
}

/**
 * The box's answer, in the shape and the wording services/appnet/arch.go emits.
 *
 * The SENTENCES are the box's to write and its tests own them
 * (TestEvaluateArch_NoUnmeasuredClaimReachesTheUser sweeps every rung's copy).
 * What this file asserts is that the hub RENDERS what it is given and composes
 * nothing of its own — so these strings are deliberately distinctive, and
 * several tests below check the exact text reaches the screen.
 */
function availability(over: Partial<FixtureAvailability> = {}): FixtureAvailability {
  return {
    state: 'native',
    installable: true,
    requires_emulation: false,
    badge: '',
    card_badge: '',
    detail: '',
    box_arch: 'amd64',
    undeclared: false,
    needs: [],
    signature: 'signed',
    ...over,
  }
}

/** Rung 5: the box will not install it, and says why. */
function refusedFor(name: string, needs: string[], boxArch: string): FixtureAvailability {
  return availability({
    state: 'unavailable',
    installable: false,
    badge: 'Not available on this box',
    card_badge: `Needs ${needs.join('/')}`,
    detail: `${name} ships for ${needs.join(' or ')} only, and this box is ${boxArch}. ` +
      `No build is published for this box's architecture. ` +
      `It stays available on any ${needs.join(' or ')} instance you run.`,
    box_arch: boxArch,
    needs,
  })
}

/**
 * The signature hold: the box will not install it because the entry has not been
 * signed yet, which is the state 55 of the 74 shipped entries are in.
 *
 * Transcribed from services/appnet/arch.go's heldForSignature — the box owns the
 * words and its own tests sweep them. What these specs assert is that the hub
 * shows what it is given and offers no install.
 */
function heldForSignature(name: string, needs: string[], boxArch: string): FixtureAvailability {
  return availability({
    state: 'unavailable',
    installable: false,
    signature: 'unsigned',
    badge: 'Awaiting publisher signature',
    card_badge: 'Awaiting signature',
    detail: `${name} is in the catalogue, but its entry carries no publisher signature yet, ` +
      `so this box refuses the install before anything is downloaded. Signing happens away ` +
      `from this box, at the publisher's key ceremony, so it is not something to put right ` +
      `here — and until a release carries that signature, no box will install this entry.`,
    box_arch: boxArch,
    needs,
  })
}

const APPS: RegistryFixtureApp[] = [
  {
    id: 'conduit',
    name: 'Conduit',
    type: 'service',
    arch: ['amd64'],
    availability: availability(),
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
    availability: availability(),
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
  /**
   * ── What changed, and why these tests read differently ────────────────────
   *
   * The hub used to DECIDE this. First with `app.arch.includes(systemArch)` — a
   * raw string match in which `["x86_64"]` never matched `"amd64"`, so most of
   * the Flathub catalogue read as incompatible on every box — and then with a
   * spelling fold in arch.ts, which was correct and was still a second
   * implementation of the box's own policy.
   *
   * The decision now lives in services/appnet/arch.go (EvaluateArch) and arrives
   * per entry as `availability`. So the assertions here changed subject: they no
   * longer ask "does the hub compute the right answer", they ask "does the hub
   * render the box's answer, and ONLY the box's answer". The strongest of them
   * hand the hub an `arch` array that CONTRADICTS the verdict, which no fixture
   * could have done while the browser was deriving one from the other.
   */

  /** One app, with a verdict, served to the hub. */
  function serving(app: Partial<RegistryFixtureApp> & { availability: FixtureAvailability }) {
    mockBackend({
      '/api/store/registry': ok([{ ...APPS[0], ...app }]),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true }),
    })
  }

  it('marks an app the box refused instead of offering to install it', async () => {
    serving({ arch: ['ppc64el'], availability: refusedFor('Conduit', ['ppc64el'], 'amd64') })
    render(<AppHub />)
    await screen.findByText('Conduit')

    expect(within(cardFor('Conduit')).getByText(/Needs ppc64el/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install Conduit' })).toBeNull()
  })

  // ── The hub does not second-guess the box ────────────────────────────────
  //
  // These two are the deletion, stated as assertions. Each hands the component
  // an `arch` array pointing the OPPOSITE way to the verdict. A hub that still
  // compared strings would get both of them wrong, and a hub that folded
  // spellings would get the first one wrong — which is precisely the pair of
  // defects this file used to have to test around.

  it('offers an app whose declared arch it cannot match, because the box said yes', async () => {
    // The box resolved this through binfmt, an emulator, a flatpak that
    // supports the arch, or simply a spelling the browser has never heard of.
    // None of those are visible here, and the hub must not overrule them.
    serving({ id: 'lutris', name: 'Lutris', arch: ['x86_64'], availability: availability() })
    render(<AppHub />)
    await screen.findByText('Lutris')

    expect(screen.getByRole('button', { name: 'Install Lutris' })).toBeInTheDocument()
    expect(within(cardFor('Lutris')).queryByText(/Needs/)).toBeNull()
  })

  it('refuses an app whose declared arch LOOKS like a match, because the box said no', async () => {
    // arch says amd64, box_arch says amd64, and the box still refused — which is
    // exactly what happens when the recipe's delivery cannot serve this machine
    // or the entry is quarantined for a reason no arch list carries.
    serving({
      id: 'lutris', name: 'Lutris', arch: ['amd64'],
      availability: refusedFor('Lutris', ['amd64'], 'amd64'),
    })
    render(<AppHub />)
    await screen.findByText('Lutris')

    // Shown, not hidden — "why can't I find Lutris?" is answered on the card.
    expect(within(cardFor('Lutris')).getByText('Needs amd64')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install Lutris' })).toBeNull()
  })

  it('shows the box architecture the verdicts were computed against', async () => {
    // Read from the catalogue's own answer, not from a second endpoint: the
    // label and the badges must describe one machine.
    serving({ availability: availability({ box_arch: 'arm64' }) })
    render(<AppHub />)
    await screen.findByText('Conduit')
    expect(screen.getByText('arm64')).toBeInTheDocument()
  })

  it('offers a filter to hide what this box cannot run, and it is off by default', async () => {
    mockBackend({
      '/api/store/registry': ok([
        { ...APPS[0], id: 'lutris', name: 'Lutris', availability: refusedFor('Lutris', ['amd64'], 'arm64') },
        { ...APPS[1], availability: availability({ box_arch: 'arm64' }) },
      ]),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true }),
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
        { ...APPS[0], id: 'lutris', name: 'Lutris', availability: refusedFor('Lutris', ['amd64'], 'arm64') },
        { ...APPS[1], availability: availability({ box_arch: 'arm64' }) },
      ]),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true }),
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
        // Alphabetically first, but refused.
        {
          ...APPS[0], id: 'aaa-lutris', name: 'AAA Lutris',
          availability: refusedFor('AAA Lutris', ['amd64'], 'arm64'),
        },
        { ...APPS[1], id: 'zzz-ok', name: 'ZZZ Runs Here', availability: availability({ box_arch: 'arm64' }) },
      ]),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true }),
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

  it('makes no compatibility claim when the box sent no verdict', async () => {
    // Every backend from before this field existed. Claiming "yes" offers an
    // install that fails; claiming "no" empties the catalogue on all of them.
    // The app is offered, unbadged, with nothing asserted about it.
    mockBackend({
      '/api/store/registry': ok([{ ...APPS[0], arch: ['ppc64el'], availability: undefined }]),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true }),
    })
    render(<AppHub />)
    await screen.findByText('Conduit')

    expect(within(cardFor('Conduit')).queryByText(/Needs/)).toBeNull()
    expect(screen.getByRole('button', { name: 'Install Conduit' })).toBeInTheDocument()
    expect(cardFor('Conduit').getAttribute('data-arch-state')).toBe('unknown')
  })

  it('makes no claim for a rung this build has never heard of', async () => {
    // A future state must not be rendered as one of the four this build knows,
    // least of all as the one that takes the install button away.
    serving({ availability: { ...refusedFor('Conduit', ['amd64'], 'arm64'), state: 'distro-sourced' } })
    render(<AppHub />)
    await screen.findByText('Conduit')

    expect(within(cardFor('Conduit')).queryByText(/Needs/)).toBeNull()
    expect(screen.getByRole('button', { name: 'Install Conduit' })).toBeInTheDocument()
  })
})

// ── The four rungs, as the user sees them ───────────────────────────────────
//
// roadmap/DISTRO-SOURCED-APPS.md §1: native > emulated-and-told > available on a
// sibling > declared unavailable with the reason. Rung 5 still ships a row —
// E1–E3 are exceptions to uniform AVAILABILITY, never to uniform VISIBILITY.

describe('the rungs', () => {
  function serve(app: Partial<RegistryFixtureApp> & { availability: FixtureAvailability }) {
    mockBackend({
      '/api/store/registry': ok([{ ...APPS[0], ...app }]),
      '/api/store/installed': ok([]),
      '/api/packages/cache': ok({ ready: true }),
    })
  }

  it('rung 1 — a native app carries no badge at all', async () => {
    serve({ availability: availability() })
    render(<AppHub />)
    await screen.findByText('Conduit')

    const card = cardFor('Conduit')
    expect(within(card).queryByText(/Needs|emulat|On your/i)).toBeNull()
    expect(within(card).getByRole('button', { name: 'Install Conduit' })).toBeInTheDocument()
    expect(card.getAttribute('data-arch-state')).toBe('native')
  })

  it('rung 3 — an emulated app is OFFERED and the cost is stated before the button', async () => {
    // The labelling is the whole difference between rung 3 and rung 2. An app
    // that is present and crawls reads as a Vulos defect rather than as a
    // hardware limit, so the sentence has to be on screen next to the install.
    const detail = 'Conduit ships for amd64 only and will run on this arm64 box through emulation ' +
      '— noticeably slower, though it uses this box’s own graphics driver rather than an emulated one.'
    serve({
      availability: availability({
        state: 'emulated', installable: true, requires_emulation: true,
        badge: 'Runs emulated', card_badge: 'Runs emulated', detail,
        box_arch: 'arm64', needs: ['amd64'],
      }),
    })
    render(<AppHub />)
    await screen.findByText('Conduit')

    const card = cardFor('Conduit')
    expect(card.getAttribute('data-arch-state')).toBe('emulated')
    expect(within(card).getByText('Runs emulated')).toBeInTheDocument()
    // Offered, not hidden: rung 3 is above rung 5.
    expect(within(card).getByRole('button', { name: 'Install Conduit' })).toBeInTheDocument()

    fireEvent.click(within(card).getByRole('button', { name: 'Conduit' }))
    expect(await screen.findByText(detail)).toBeInTheDocument()
    // Scoped to the PANEL. An unscoped /^Install/ also matches the card's own
    // "Install Conduit" button, which is a different control and would let this
    // pass with no install affordance in the panel at all.
    const panel = document.querySelector('.hub-detail') as HTMLElement
    expect(within(panel).getByRole('button', { name: /^Install/ })).toBeInTheDocument()
  })

  it('rung 4 — a sibling instance is NAMED, and no install is offered here', async () => {
    const detail = 'Conduit ships for amd64 only, and this box is arm64. ' +
      'It is installed on studio-box and stays available there.'
    serve({
      availability: availability({
        state: 'other-instance', installable: false,
        badge: 'On your other instance', card_badge: 'On studio-box', detail,
        box_arch: 'arm64', needs: ['amd64'],
      }),
    })
    render(<AppHub />)
    await screen.findByText('Conduit')

    const card = cardFor('Conduit')
    expect(card.getAttribute('data-arch-state')).toBe('other-instance')
    expect(within(card).getByText('On studio-box')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install Conduit' })).toBeNull()

    fireEvent.click(within(card).getByRole('button', { name: 'Conduit' }))
    expect(await screen.findByText('On your other instance')).toBeInTheDocument()
    expect(screen.getByText(detail)).toBeInTheDocument()
  })

  it('rung 5 — refused, shown, and the box’s reason is the reason displayed', async () => {
    const av = refusedFor('Conduit', ['amd64'], 'arm64')
    serve({ availability: av })
    render(<AppHub />)
    await screen.findByText('Conduit')

    fireEvent.click(within(cardFor('Conduit')).getByRole('button', { name: 'Conduit' }))
    expect(await screen.findByText(av.badge)).toBeInTheDocument()
    expect(screen.getByText(av.detail)).toBeInTheDocument()
  })

  /**
   * THE HONESTY GATE.
   *
   * Two claims must never reach a user, and neither is a wording preference:
   *
   *  - that the app FAILS TO RUN. §6 Q3 of roadmap/DISTRO-SOURCED-APPS.md got
   *    `bwrap: Creating new namespace failed: Operation not permitted`, which is
   *    a CONTAINER PRIVILEGE limit that would have stopped a native aarch64 app
   *    in the same container. It is recorded `untestable-on-arm64-mac`. Nobody
   *    has measured it. What the hub used to say — "Installing it here would
   *    fail" — is additionally measurably wrong for Flatpak, where
   *    `flatpak install --arch=x86_64` deploys the app and its 1.4 GB platform.
   *  - that emulation gets HARDWARE acceleration. §5.3 proved which GL stack
   *    box64 bound, on a container with NO GPU where that stack is llvmpipe.
   *
   * The Go side owns this for the copy it writes
   * (TestEvaluateArch_NoUnmeasuredClaimReachesTheUser). This is the other half:
   * the hub must not add a sentence of its own on top. It is checked by feeding
   * a verdict whose own strings are innocuous and scanning the WHOLE rendered
   * hub — so any sentence the component composes is caught, wherever it is.
   */
  const UNMEASURED = [
    'cannot run', 'will not run', 'would fail', 'installing it here',
    'hardware acceleration', 'gpu-accelerated', 'full graphics acceleration',
    'in settings',
  ]

  it('adds no architecture sentence of its own to any rung', async () => {
    const rungs: FixtureAvailability[] = [
      availability(),
      availability({
        state: 'emulated', installable: true, requires_emulation: true,
        badge: 'Runs emulated', card_badge: 'Runs emulated',
        detail: 'Conduit runs here through emulation, and is slower for it.',
        box_arch: 'arm64', needs: ['amd64'],
      }),
      availability({
        state: 'other-instance', installable: false,
        badge: 'On your other instance', card_badge: 'On studio-box',
        detail: 'Conduit is installed on studio-box and stays available there.',
        box_arch: 'arm64', needs: ['amd64'],
      }),
      refusedFor('Conduit', ['amd64'], 'arm64'),
      heldForSignature('Conduit', ['amd64'], 'arm64'),
    ]

    for (const av of rungs) {
      cleanup()
      serve({ availability: av })
      render(<AppHub />)
      await screen.findByText('Conduit')
      // Open the panel too — that is where the composed sentence used to live.
      fireEvent.click(within(cardFor('Conduit')).getByRole('button', { name: 'Conduit' }))
      await screen.findAllByText('Conduit')

      const rendered = (document.body.textContent || '').toLowerCase()
      for (const phrase of UNMEASURED) {
        expect(rendered.includes(phrase), `${av.state}: the hub rendered "${phrase}", which the ` +
          'box did not send and nobody measured').toBe(false)
      }
    }
    // COVERAGE: without this the loop passes by iterating over nothing.
    expect(rungs.length).toBe(5)
  })

  /**
   * THE SIGNATURE HOLD.
   *
   * 55 of the 74 shipped entries carry no publisher signature: staged by a
   * catalogue wave, inert until the founder's offline ceremony, and refused by
   * InstallFromRegistry before anything is downloaded. The hub listed all 55
   * with a live Install button, so the box advertised 55 apps whose install
   * could only fail.
   *
   * These specs are the hub's half of the fix. They do NOT re-check the box's
   * policy — services/appnet owns that — they check that a hold arriving on the
   * verdict removes the button and shows the box's own words.
   */
  it('a held entry is not offered for install, however installable its architecture is', async () => {
    // Native architecture, no `needs`, nothing about this box refuses it. The
    // ONLY thing standing between this card and an Install button is the
    // signature — so a wrong-arch fixture would pass this test without the gate.
    const av = heldForSignature('Conduit', [], 'amd64')
    serve({ availability: av })
    render(<AppHub />)
    await screen.findByText('Conduit')

    const card = cardFor('Conduit')
    expect(screen.queryByRole('button', { name: 'Install Conduit' })).toBeNull()
    expect(card.getAttribute('data-signature')).toBe('unsigned')
    expect(within(card).getByText('Awaiting signature')).toBeInTheDocument()

    fireEvent.click(within(card).getByRole('button', { name: 'Conduit' }))
    // The box's badge and the box's sentence, verbatim.
    expect(await screen.findByText(av.badge)).toBeInTheDocument()
    expect(screen.getByText(av.detail)).toBeInTheDocument()
    const panel = document.querySelector('.hub-detail') as HTMLElement
    expect(within(panel).queryByRole('button', { name: /^Install/ })).toBeNull()

    // THE CONTROL. The same app, the same architecture, signature verified: the
    // button comes back. Without it this passes on a hub that offers nothing.
    cleanup()
    serve({ availability: availability() })
    render(<AppHub />)
    await screen.findByText('Conduit')
    expect(screen.getByRole('button', { name: 'Install Conduit' })).toBeInTheDocument()
  })

  it('frames a pending signature as a hold and a FAILED one as a refusal', async () => {
    // The words come from the box; the frame does not, and it is the one
    // channel the sentence cannot reach. An entry waiting on a ceremony is not
    // broken, and 55 red-blocked cards would say it is.
    const held = heldForSignature('Conduit', [], 'amd64')
    serve({ availability: held })
    render(<AppHub />)
    await screen.findByText('Conduit')
    fireEvent.click(within(cardFor('Conduit')).getByRole('button', { name: 'Conduit' }))
    await screen.findByText(held.badge)
    expect(document.querySelector('.hub-detail .hub-notice')?.getAttribute('data-tone'))
      .toBe('accent')

    // The control, and the reason the check is not just `signature !== 'signed'`:
    // a signature that FAILED to verify means the entry was altered, re-keyed or
    // signed by a stranger. Describing that as a pending state would be a false
    // reassurance about the one case that warrants alarm.
    cleanup()
    const failed = availability({
      state: 'unavailable', installable: false, signature: 'untrusted',
      badge: 'Publisher signature did not verify',
      card_badge: 'Signature rejected',
      detail: 'Conduit carries a publisher signature that does not verify.',
    })
    serve({ availability: failed })
    render(<AppHub />)
    await screen.findByText('Conduit')
    fireEvent.click(within(cardFor('Conduit')).getByRole('button', { name: 'Conduit' }))
    await screen.findByText(failed.badge)
    expect(document.querySelector('.hub-detail .hub-notice')?.getAttribute('data-tone'))
      .toBe('danger')
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
