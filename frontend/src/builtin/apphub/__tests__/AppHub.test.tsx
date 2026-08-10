// AppHub.test.tsx — the App Store / App Hub browse+search UI.
//
// Guard: the search box must match on registry `keywords` in addition to
// name/description/id. Before this fix, RegistryListEntry (the shape
// GET /api/store/registry actually returns) had no Keywords field at all, so
// even if the frontend matched on app.keywords it would always be undefined
// — this test exercises the real fetch → filter path end to end with a
// server payload shaped like the real API response.

import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { App } from '../../../core/AppRegistry'

vi.mock('../../../core/AppRegistry', () => ({ refreshInstalled: vi.fn<() => Promise<App[]>>() }))

import AppHub from '../AppHub'

function mockRegistry(apps: unknown) {
  vi.stubGlobal('fetch', vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (url) => {
    const u = String(url)
    if (u.includes('/api/store/registry')) {
      return new Response(JSON.stringify(apps), { status: 200 })
    }
    if (u.includes('/api/store/installed')) {
      return new Response(JSON.stringify([]), { status: 200 })
    }
    if (u.includes('/api/packages/cache')) {
      return new Response(JSON.stringify({ ready: true, arch: 'amd64' }), { status: 200 })
    }
    return new Response(JSON.stringify({}), { status: 200 })
  }))
}

afterEach(() => cleanup())
beforeEach(() => vi.clearAllMocks())

// The GET /api/store/registry wire shape (services/appnet/registry.go's
// RegistryListEntry) — a store-only shape AppHub.tsx's local StoreApp
// narrows from, not the shell-launcher `App` type.
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
  const search = screen.getByPlaceholderText('Search...')
  fireEvent.change(search, { target: { value: 'federation' } })

  expect(await screen.findByText('Conduit')).toBeInTheDocument()
  expect(screen.queryByText('Darktable')).not.toBeInTheDocument()
})

it('still matches on name and description as before', async () => {
  mockRegistry(APPS)
  render(<AppHub />)
  await screen.findByText('Conduit')

  const search = screen.getByPlaceholderText('Search...')
  fireEvent.change(search, { target: { value: 'raw developer' } })

  expect(await screen.findByText('Darktable')).toBeInTheDocument()
  expect(screen.queryByText('Conduit')).not.toBeInTheDocument()
})

it('is case-insensitive and tolerant of apps with no keywords at all', async () => {
  mockRegistry([
    { ...APPS[1], keywords: undefined },
  ])
  render(<AppHub />)
  await screen.findByText('Darktable')

  // keywords is undefined here — matching must not throw when it falls
  // through to the (app.keywords || []).some(...) keyword check, and the
  // uppercase query must still match case-insensitively via the description.
  const search = screen.getByPlaceholderText('Search...')
  fireEvent.change(search, { target: { value: 'PHOTOGRAPHY' } })
  expect(await screen.findByText('Darktable')).toBeInTheDocument()

  // A term present in neither name, description, nor (absent) keywords
  // correctly yields no results, without throwing on the missing keywords.
  fireEvent.change(search, { target: { value: 'zzznomatch' } })
  expect(screen.queryByText('Darktable')).not.toBeInTheDocument()
})

// A web app used to render TWO badges reading the same word — "WEB WEB" — one
// from SourceBadge (where it comes from) and one from AppTypeBadge (how it
// runs). Both axes resolve to "Web" for a web app, so the card repeated the
// same fact twice; on the 63-app catalogue grid it was the most repeated text
// on screen. Caught by opening docs/screenshots/apphub.png, not by any test.
it('shows the WEB badge once, not twice, for a web app', async () => {
  mockRegistry([
    { ...APPS[0], id: 'gitea', name: 'Gitea', type: 'web', description: 'Self-hosted Git forge' },
  ])
  render(<AppHub />)
  await screen.findByText('Gitea')

  // Scope to the card: the sidebar has its own "Web" app-type FILTER row, which
  // is a different control and must keep its label.
  const card = screen.getByText('Gitea').closest('div')?.parentElement as HTMLElement
  const webBadges = Array.from(card.querySelectorAll('span'))
    .filter(el => el.textContent?.trim().toLowerCase() === 'web')
  expect(webBadges).toHaveLength(1)
})

// The suppression must be narrow: when the two badges carry DIFFERENT facts
// they both still render, because "Apt · Streamed" genuinely says two things.
it('still shows both badges when they say different things', async () => {
  mockRegistry([APPS[1]])  // darktable: type 'desktop' → Apt + Streamed
  render(<AppHub />)
  await screen.findByText('Darktable')

  // MUST be scoped to the CARD. The sidebar carries its own "Web"/"Streamed"/
  // "Service" app-type FILTER rows with the same words, so an unscoped
  // getAllByText matches the sidebar and passes no matter what the card
  // renders — the first version of this test did exactly that and survived a
  // mutation that removed the type badge from every card.
  const card = screen.getByText('Darktable').closest('div')?.parentElement as HTMLElement
  const labels = Array.from(card.querySelectorAll('span')).map(el => el.textContent?.trim().toLowerCase())
  expect(labels).toContain('apt')
  expect(labels).toContain('streamed')
})
