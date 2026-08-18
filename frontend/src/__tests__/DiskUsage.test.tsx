import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent, act } from '@testing-library/react'

import DiskUsage from '../builtin/disks/DiskUsage'

// GET /api/disks answers once and is never re-fetched, which is the fact behind
// the sidebar defect below: every render hands the same Mount objects back to
// the click handlers.
const MOUNTS = {
  mounts: [
    { mount_point: '/home', device: '/dev/sda2', fs_type: 'ext4', used_mb: 400_000, free_mb: 100_000, total_mb: 500_000, percent: 80 },
    { mount_point: '/boot', device: '/dev/sda1', fs_type: 'vfat', used_mb: 200, free_mb: 800, total_mb: 1000, percent: 20 },
  ],
}

function entry(name: string, path: string, size_mb: number) {
  return { name, path, size_mb }
}

const SCANS: Record<string, ReturnType<typeof entry>[]> = {
  '/home': [entry('user', '/home/user', 300_000)],
  '/home/user': [entry('Downloads', '/home/user/Downloads', 250_000)],
  '/home/user/Downloads': [entry('iso-hoard', '/home/user/Downloads/iso-hoard', 240_000)],
  '/boot': [entry('efi', '/boot/efi', 100)],
}

// `deferred` lets a specific path's `du` hang, which is how a slow scan and a
// fast one get to race in a deterministic order.
function mockDisks(opts: { deferred?: Record<string, boolean> } = {}) {
  const releases: Record<string, () => void> = {}
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    const u = String(url)
    if (u === '/api/disks') {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(MOUNTS) })
    }
    const m = /\/api\/disks\/breakdown\?path=(.+)$/.exec(u)
    if (m) {
      const path = decodeURIComponent(m[1])
      const body = SCANS[path] ?? []
      const answer = { ok: true, status: 200, json: () => Promise.resolve(body) }
      if (opts.deferred?.[path]) {
        return new Promise(resolve => { releases[path] = () => resolve(answer) })
      }
      return Promise.resolve(answer)
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  }))
  return releases
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('DiskUsage — the volume list must stay live after drilling in', () => {
  // The sidebar button used to only setSelectedMount(m). `mounts` is fetched
  // once and never replaced, so that hands back the SAME object every time:
  // React bails out of the identical write, and the effect keyed on
  // `selectedMount` never re-runs. Clicking the volume you are already on —
  // the obvious "take me back to the top" gesture — did nothing at all.
  it('rescans the volume when its already-selected sidebar entry is clicked again', async () => {
    mockDisks()
    render(<DiskUsage />)

    // Auto-selected /home. Drill down two levels through the breakdown rows.
    expect(await screen.findByText('user')).toBeInTheDocument()
    fireEvent.click(screen.getByText('user'))
    expect(await screen.findByText('Downloads')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Downloads'))
    expect(await screen.findByText('iso-hoard')).toBeInTheDocument()

    // Now click /home in the sidebar — the volume that is still marked active.
    const homeButton = screen.getAllByText('/home').find(el => el.closest('button'))!.closest('button')!
    expect(homeButton).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(homeButton)

    expect(await screen.findByText('user')).toBeInTheDocument()
    expect(screen.queryByText('iso-hoard')).toBeNull()
  })

  it('still switches to a different volume', async () => {
    mockDisks()
    render(<DiskUsage />)
    await screen.findByText('user')

    fireEvent.click(screen.getAllByText('/boot').find(el => el.closest('button'))!.closest('button')!)
    expect(await screen.findByText('efi')).toBeInTheDocument()
  })

  // `du` on a big tree is slow. The breadcrumb is written synchronously at the
  // start of a scan, so a late answer landing last leaves the path label and the
  // list under it describing two different directories — and clicking a row then
  // scans a path unrelated to anything on screen.
  it('does not let a slow scan overwrite the directory the user moved on to', async () => {
    const releases = mockDisks({ deferred: { '/home/user': true } })
    render(<DiskUsage />)
    await screen.findByText('user')

    // Ask for /home/user (slow), then immediately go back to /home (fast).
    fireEvent.click(screen.getByText('user'))
    await waitFor(() => expect(releases['/home/user']).toBeTypeOf('function'))
    const homeButton = screen.getAllByText('/home').find(el => el.closest('button'))!.closest('button')!
    fireEvent.click(homeButton)
    expect(await screen.findByText('user')).toBeInTheDocument()

    // The abandoned scan finally answers.
    await act(async () => { releases['/home/user']() })
    await waitFor(() => expect(screen.getByText('user')).toBeInTheDocument())
    expect(screen.queryByText('Downloads')).toBeNull()
  })
})
