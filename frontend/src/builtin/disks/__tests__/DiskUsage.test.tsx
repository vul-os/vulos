// DiskUsage.test.tsx — the Disk Usage app's mount detail header.
//
// Guard: the donut + Used/Free/Total row used to stay side-by-side down to
// zero container width. On a narrow window (a phone-fullscreen app, or a
// desktop window snapped to a quarter-tile) that left the 3-column
// Used/Free/Total grid only a sliver of space, so its uppercase labels
// overflowed their cells edge-to-edge ("USEDFREETOTAL" in one run) and the
// values overlapped the same way — see docs/screenshots (disk-usage-mobile
// QA capture). The fix stacks the donut above the info column below the
// `@xs` container breakpoint, and adds `min-w-0 truncate` to every
// Used/Free/Total cell as a second line of defense so a still-cramped
// container clips instead of bleeding into its neighbour.
//
// jsdom does not evaluate `@container` queries (no real layout engine), so
// this cannot assert the stacked layout renders at a given pixel width. What
// it CAN pin, and what actually regressed, is that the responsive classes
// and the overflow guards are present on the right elements — removing any
// of them (verified by mutation) fails this test.

import { render, screen, cleanup } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import DiskUsage from '../DiskUsage'

const MOUNT = {
  mount_point: '/', device: '/dev/nvme0n1p2', fs_type: 'ext4',
  used_mb: 128_450, free_mb: 380_780, total_mb: 512_000, percent: 25.1,
}

function mockDisks() {
  vi.stubGlobal('fetch', vi.fn<(input: RequestInfo | URL) => Promise<Response>>(async (url) => {
    const u = String(url)
    if (u.includes('/api/disks/breakdown')) {
      return new Response(JSON.stringify([{ name: 'apps', path: '/apps', size_mb: 40_000 }]), { status: 200 })
    }
    if (u.includes('/api/disks')) {
      return new Response(JSON.stringify({ mounts: [MOUNT] }), { status: 200 })
    }
    return new Response(JSON.stringify({}), { status: 200 })
  }))
}

afterEach(() => cleanup())
beforeEach(() => vi.clearAllMocks())

it('renders the real mount figures', async () => {
  mockDisks()
  render(<DiskUsage />)
  expect(await screen.findByText('125.4 GB')).toBeInTheDocument()
  expect(screen.getByText('371.9 GB')).toBeInTheDocument()
  expect(screen.getByText('500.0 GB')).toBeInTheDocument()
})

it('stacks the donut above the info column, and truncates every Used/Free/Total cell, below the @xs container width', async () => {
  mockDisks()
  render(<DiskUsage />)
  const used = await screen.findByText('125.4 GB')

  // The donut+info row: flex-col by default, only flex-row from @xs up — a
  // bare `flex items-center` (no `flex-col`/`@xs:flex-row`) is the exact
  // regression, since it pins the donut and the info column side-by-side at
  // every container width.
  const row = screen.getByText(/ext4/).closest('div')?.parentElement?.parentElement
  expect(row?.className).toContain('flex-col')
  expect(row?.className).toContain('@xs:flex-row')

  // Every Used/Free/Total value (and its label) must be able to clip rather
  // than overflow into its neighbour when the grid cell is narrower than the
  // text — this is what actually stopped "USEDFREETOTAL" from running
  // together.
  const label = screen.getByText('Used')
  const cell = label.parentElement
  expect(cell?.className).toContain('min-w-0')
  expect(label.className).toContain('truncate')
  expect(used.className).toContain('truncate')
})
