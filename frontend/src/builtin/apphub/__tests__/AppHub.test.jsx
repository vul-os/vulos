// AppHub.test.jsx — the App Store / App Hub browse+search UI.
//
// Guard: the search box must match on registry `keywords` in addition to
// name/description/id. Before this fix, RegistryListEntry (the shape
// GET /api/store/registry actually returns) had no Keywords field at all, so
// even if the frontend matched on app.keywords it would always be undefined
// — this test exercises the real fetch → filter path end to end with a
// server payload shaped like the real API response.

import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

vi.mock('../../../core/AppRegistry', () => ({ refreshInstalled: vi.fn() }))

import AppHub from '../AppHub'

function mockRegistry(apps) {
  global.fetch = vi.fn((url) => {
    const u = String(url)
    if (u.includes('/api/store/registry')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(apps) })
    }
    if (u.includes('/api/store/installed')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    }
    if (u.includes('/api/packages/cache')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ready: true, arch: 'amd64' }) })
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) })
  })
}

afterEach(() => cleanup())
beforeEach(() => vi.clearAllMocks())

const APPS = [
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
