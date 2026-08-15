// builtinApps.system.test.tsx — conformance matrix for the system builtins:
// Drivers, Software (packages), Disk Usage, Dashboard.
//
// Each app is asked the same four questions (see harness.tsx):
//   mounts / renders its OWN surface / deliberate empty state / survives a dead
//   backend.
//
// "Its own surface" is asserted on vocabulary unique to that app — a device
// name, a module's byte count, a mount point. Asserting that the container
// merely rendered *something* is how a lazy chunk that mounts to an empty shell
// passes a green suite, which is the failure this file exists to prevent.
//
// A note on the shape most of these regressions share. None of these apps
// checked `res.ok`, and their JSON narrowers answer "empty list" for any shape
// they do not recognise — so an HTTP 500 carrying a JSON error body was parsed
// as a successful, empty result. The visible consequence was not an error but a
// confident lie: "No devices detected", "No volumes found", "No packages
// installed". Several of the assertions below exist specifically to pin the
// difference between "the box says there is nothing" and "the box did not say".
//
// Activity Monitor is deliberately NOT covered here: src/builtin/activity/ is
// owned by another agent in this same repo. See the report for the telemetry
// finding handed over to that owner.

import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import {
  installFetch, installFailingFetch, installBrowserStubs,
  settle, expectNotBlank, expectOwnSurface, type FetchRig,
} from './harness'

import Drivers from '../drivers/Drivers'
import Packages from '../packages/Packages'
import DiskUsage from '../disks/DiskUsage'
import DashboardApp from '../dashboard/DashboardApp'

let fetchRig: FetchRig | null = null
let restoreStubs: (() => void) | null = null

beforeEach(() => { restoreStubs = installBrowserStubs() })
afterEach(() => {
  cleanup()
  fetchRig?.restore(); fetchRig = null
  restoreStubs?.(); restoreStubs = null
})

// ---------------------------------------------------------------------------
// Drivers
// ---------------------------------------------------------------------------

const DRIVERS_OK = {
  kernel: '6.9.3-vulos',
  devices: [
    { id: '00:02.0', name: 'UHD Graphics 620', vendor: 'Intel', bus: 'pci', class: 'display', driver: 'i915', driver_state: 'active' },
    { id: '00:14.3', name: 'Wireless-AC 9560', vendor: 'Intel', bus: 'pci', class: 'network', driver_state: 'available', module: 'iwlwifi' },
  ],
  modules: [{ name: 'iwlwifi', size: 401408, used_by: 'iwlmvm' }],
}

describe('Drivers', () => {
  it('mounts and lists the real detected hardware', async () => {
    fetchRig = installFetch({ '/api/drivers': { body: DRIVERS_OK } })
    const { container } = render(<Drivers />)
    await settle()

    expectNotBlank(container, 'Drivers')
    expect(screen.getByText('Additional Drivers')).toBeInTheDocument()
    // Its own surface: real device rows grouped under real class headings.
    await waitFor(() => expect(screen.getByText('UHD Graphics 620')).toBeInTheDocument())
    expect(screen.getByText('Display')).toBeInTheDocument()
    expect(screen.getByText('Network')).toBeInTheDocument()
    expectOwnSurface(container, /6\.9\.3-vulos/, 'Drivers')
  })

  it('shows a deliberate empty state when no hardware is detected', async () => {
    fetchRig = installFetch({ '/api/drivers': { body: { devices: [], modules: [] } } })
    render(<Drivers />)
    await settle()
    await waitFor(() => expect(screen.getByText('No devices detected')).toBeInTheDocument())
  })

  // REGRESSION (defect BUILTIN-2): a failing /api/drivers left `status` null with
  // `loading` false, so every content branch was false and the body rendered as
  // an empty rectangle under the header — no message, no reason.
  it('explains itself when /api/drivers fails instead of rendering an empty body', async () => {
    fetchRig = installFailingFetch()
    const { container } = render(<Drivers />)
    await settle(4)

    expectNotBlank(container, 'Drivers (backend down)')
    expect(screen.getByText('Additional Drivers')).toBeInTheDocument()
    expect(screen.getByText(/could not|couldn't|failed|unavailable/i)).toBeInTheDocument()
    // And a way out. Two Rescan buttons exist on this surface — the header's
    // and the error panel's — so assert on the set, not on uniqueness.
    const rescans = screen.getAllByRole('button', { name: /rescan/i })
    expect(rescans.length).toBeGreaterThan(0)
    for (const b of rescans) expect(b).toBeEnabled()
  })

  // REGRESSION (defect BUILTIN-2, second half). This is the assertion that
  // matters most in this file. installFailingFetch answers 500 with a *valid
  // JSON body*, which is what a Go service returns when it errors. res.ok was
  // never consulted, so that body reached toStatus(), which maps anything it
  // does not recognise to { devices: [], modules: [] } — and the app then
  // rendered its designed empty state. A user whose driver service had fallen
  // over was told, in a considered piece of UI, that their machine contains no
  // hardware.
  it('does not claim "No devices detected" when the box merely failed to answer', async () => {
    fetchRig = installFailingFetch()
    render(<Drivers />)
    await settle(4)

    expect(
      screen.queryByText('No devices detected'),
      'an HTTP failure must never be reported as a successful scan that found nothing',
    ).not.toBeInTheDocument()
  })

  // The loading state and the failure state must not be the same picture.
  it('stops showing the scanning spinner once the scan has failed', async () => {
    fetchRig = installFailingFetch()
    render(<Drivers />)
    await settle(4)
    expect(screen.queryByText('Detecting hardware...')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Software (packages)
// ---------------------------------------------------------------------------

const PKG_STATUS = { installed_count: 2, repos: [{ url: 'https://deb.vulos.org/stable', enabled: true }] }
const PKG_INSTALLED = [
  { name: 'openssh-server', version: '9.7p1', description: 'Secure shell server' },
  { name: 'ripgrep', version: '14.1.0', description: 'Fast recursive grep' },
]

describe('Software (packages)', () => {
  it('mounts and lists the real installed packages', async () => {
    fetchRig = installFetch({
      '/api/packages/status': { body: PKG_STATUS },
      '/api/packages/installed': { body: PKG_INSTALLED },
    })
    const { container } = render(<Packages />)
    await settle()

    expectNotBlank(container, 'Software')
    await waitFor(() => expect(screen.getByText('openssh-server')).toBeInTheDocument())
    expect(screen.getByText('ripgrep')).toBeInTheDocument()
    expectOwnSurface(container, /Installed Packages/, 'Software')
  })

  // REGRESSION (defect BUILTIN-3): with an empty installed list and no filter
  // typed, none of the three content branches matched — the pane rendered as a
  // blank rectangle with no explanation.
  it('says the box has no packages rather than rendering a blank pane', async () => {
    fetchRig = installFetch({
      '/api/packages/status': { body: PKG_STATUS },
      '/api/packages/installed': { body: [] },
    })
    const { container } = render(<Packages />)
    await settle()
    expect(screen.getByText(/No packages installed/i)).toBeInTheDocument()
    expectNotBlank(container, 'Software (empty)')
  })

  // REGRESSION (defect BUILTIN-4): a failing /api/packages/installed left the
  // list state null forever, so the app sat on "Loading packages..." for the
  // life of the window — a spinner that can never resolve.
  it('stops claiming to load when the package backend is down', async () => {
    fetchRig = installFailingFetch()
    const { container } = render(<Packages />)
    await settle(4)

    expectNotBlank(container, 'Software (backend down)')
    expect(screen.getAllByText('Software').length).toBeGreaterThan(0)
    expect(screen.queryByText('Loading packages...'), 'a dead backend must not spin forever')
      .not.toBeInTheDocument()
    expect(screen.getByText(/could not|couldn't|failed|unavailable/i)).toBeInTheDocument()
  })

  // Same false-empty shape as Drivers: the 500 body parsed, toPkgList() gave [],
  // and the user was told their box has nothing installed.
  it('does not claim "No packages installed" when the package service is down', async () => {
    fetchRig = installFailingFetch()
    render(<Packages />)
    await settle(4)
    // Exact-string, not regex: the error copy deliberately contains the words
    // "having no packages installed" to draw the very distinction being
    // asserted, and a loose regex would match the fix as if it were the bug.
    expect(
      screen.queryByText('No packages installed'),
      'an outage must not be reported as an empty package list',
    ).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Disk Usage
// ---------------------------------------------------------------------------

const DISKS_OK = {
  mounts: [
    { mount_point: '/', device: '/dev/nvme0n1p2', fs_type: 'ext4', used_mb: 40000, free_mb: 60000, total_mb: 100000, percent: 40 },
    { mount_point: '/boot', device: '/dev/nvme0n1p1', fs_type: 'vfat', used_mb: 120, free_mb: 380, total_mb: 500, percent: 24 },
  ],
}

describe('Disk Usage', () => {
  it('mounts and renders the real volume list plus the selected mount', async () => {
    fetchRig = installFetch({
      '/api/disks': { body: DISKS_OK },
      '/api/disks/breakdown': { body: { entries: [{ name: 'var', path: '/var', size_mb: 8000 }] } },
    })
    const { container } = render(<DiskUsage />)
    await settle()

    expectNotBlank(container, 'Disk Usage')
    expect(screen.getByText('Volumes')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('/dev/nvme0n1p2')).toBeInTheDocument())
    expectOwnSurface(container, /\/dev\/nvme0n1p1/, 'Disk Usage')
  })

  it('says "No volumes found" rather than rendering an empty rail', async () => {
    fetchRig = installFetch({ '/api/disks': { body: { mounts: [] } } })
    render(<DiskUsage />)
    await settle()
    await waitFor(() => expect(screen.getByText('No volumes found')).toBeInTheDocument())
    expect(screen.getByText('No filesystem selected')).toBeInTheDocument()
  })

  // REGRESSION (defect BUILTIN-5): the rail rendered as literally nothing when
  // /api/disks rejected — `mounts` stayed null and `mounts?.length === 0` is
  // false for null, so neither the spinner, the list, nor the empty message
  // matched.
  it('survives /api/disks failing and says why', async () => {
    fetchRig = installFailingFetch()
    const { container } = render(<DiskUsage />)
    await settle(4)
    expectNotBlank(container, 'Disk Usage (backend down)')
    expect(screen.getByText('Volumes')).toBeInTheDocument()
    // Not stuck on the loading spinner forever.
    expect(screen.queryByText('Scanning...')).not.toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent(/Could not read volumes/i)
  })

  it('does not claim "No volumes found" when the box failed to answer', async () => {
    fetchRig = installFailingFetch()
    render(<DiskUsage />)
    await settle(4)
    expect(
      screen.queryByText('No volumes found'),
      'a box that cannot be asked is not a box with no filesystems',
    ).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

describe('Dashboard', () => {
  it('mounts, resolves its lazy tab chunk and renders past the spinner', async () => {
    fetchRig = installFetch({
      '/api/apps/visibility': { body: { apps: [] } },
      '/api/routing/apps': { body: { apps: [] } },
      '/api/store/installed': { body: [] },
      '/api/instances': { body: { instances: [] } },
    })
    const { container } = render(<DashboardApp />)
    await settle(8)

    expectNotBlank(container, 'Dashboard')
    expect(screen.getByRole('button', { name: 'Web' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Instances' })).toBeInTheDocument()
    // The lazy child must actually have resolved — the shared Suspense fallback
    // is the literal string "Loading...".
    await waitFor(() => expect(container.textContent).not.toMatch(/Loading\.\.\./))
  })

  it('survives the dashboard backend being down', async () => {
    fetchRig = installFailingFetch()
    const { container } = render(<DashboardApp />)
    await settle(8)
    expectNotBlank(container, 'Dashboard (backend down)')
    expect(screen.getByRole('button', { name: 'Web' })).toBeInTheDocument()
  })
})
