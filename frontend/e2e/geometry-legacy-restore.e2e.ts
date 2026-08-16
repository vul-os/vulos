// geometry-legacy-restore.e2e.ts — a window saved by Vulos v0.1.0 must come
// back somewhere the user can actually reach it, on whatever screen the box is
// opened on next.
//
// WHY THIS EXISTS
//
// Window geometry crosses localStorage and the cross-tab channel as a canonical
// fraction of the writer's own viewport, tagged `geomUnit: 'canonical-v1'`
// (providers/screenScale.ts, ShellProvider.tsx). Untagged payloads are never
// rescaled — nothing in them records the extent they were measured against, so
// there is nothing honest to rescale by, and their magnitudes are
// indistinguishable from canonical fractions.
//
// v0.1.0 (2026-08-06, the first release the project published) wrote exactly
// such an untagged blob, under the same `vulos-shell-state` key this build
// reads. Left as raw px, a window that sat at x=1180 on the 1920-wide screen
// that saved it restores 1132px past the right edge of a 768-wide one — title
// bar and bottom-right resize grip both gone. The unit suite cannot see that:
// hydratePersistedState is a pure function, and 1900 > 768 is only a defect
// once you know how wide the screen is. This spec measures the real rendered
// rect in a real browser instead.
//
// The blob below is byte-for-byte the shape `git show
// v0.1.0:frontend/src/providers/ShellProvider.tsx`'s saveShellState produced —
// no geomUnit, no viewport, nothing else to go on.

import { test, expect, type Page } from '@playwright/test'
import { installBackend } from './mock-backend.js'

const STORAGE_KEY = 'vulos-shell-state'

// The dock floats over the bottom DOCK_H px and the menu bar owns the top
// MENU_BAR_H (shell/windowTiling.ts). A window that is on-screen by the raw
// viewport rect but underneath the dock is still unreachable, so the usable
// band is what gets asserted.
const MENU_BAR_H = 32
const DOCK_H = 68
const WINDOW_EDGE_MARGIN = 8

// Saved on a 1920x1080 screen, near its right edge but comfortably inside it:
// 1180 + 720 = 1900, clear of that viewport's own 1912px limit. So this
// geometry is not "broken" — it is correct for the screen that produced it,
// which is exactly what makes reading it on a smaller one the interesting case.
const LEGACY_POSITION = { x: 1180, y: 200 }
const LEGACY_SIZE = { width: 720, height: 500 }

function legacyBlob() {
  return {
    desktops: {
      'desktop-1': {
        id: 'desktop-1',
        label: 'Desktop 1',
        activeWindow: 1,
        windows: [{
          id: 1, appId: 'terminal', title: 'Terminal', icon: '',
          position: LEGACY_POSITION, size: LEGACY_SIZE,
          minimized: false, _tile: null, _maximized: false, _builtin: true,
        }],
      },
    },
    activeDesktop: 'desktop-1',
  }
}

async function bootWithLegacyState(page: Page) {
  // Reduced motion, for the reason windows-open-geometry.e2e.ts documents: the
  // open choreography scales the window from a transform for ~1 frame, so a
  // boundingBox() taken in flight reports a smaller box than the geometry the
  // shell actually chose.
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await installBackend(page)
  // addInitScript, not an evaluate after goto: the restore runs in
  // ShellProvider's mount effect, so the blob has to be in place before the
  // first script of the page runs, not after.
  await page.addInitScript(([key, blob]) => {
    window.localStorage.setItem(key as string, JSON.stringify(blob))
  }, [STORAGE_KEY, legacyBlob()] as const)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 15_000 })
}

async function restoredRect(page: Page) {
  const win = page.locator('[data-window-id]')
  // Asserted, not assumed: if the legacy blob failed to restore at all this
  // spec would otherwise "pass" by measuring zero windows — the hollow-gate
  // shape this repo keeps finding.
  await expect(win, 'the legacy blob should have restored exactly one window').toHaveCount(1, { timeout: 10_000 })
  const box = await win.first().boundingBox()
  if (!box) throw new Error('restored window had no bounding box')
  return box
}

test('a v0.1.0 window saved on a 1920 screen restores fully reachable on a 768 one', async ({ page }) => {
  await page.setViewportSize({ width: 768, height: 1024 })
  await bootWithLegacyState(page)
  const box = await restoredRect(page)
  await page.screenshot({ path: 'test-results/geometry-legacy-768.png' })
  console.log(`[768x1024] restored at (${Math.round(box.x)},${Math.round(box.y)}) ${Math.round(box.width)}x${Math.round(box.height)}`)

  // Unfitted, this rendered at x=1180 — 1132px of it past the right edge.
  expect(box.x, 'left edge').toBeGreaterThanOrEqual(WINDOW_EDGE_MARGIN)
  expect(box.x + box.width, 'right edge').toBeLessThanOrEqual(768 - WINDOW_EDGE_MARGIN)
  expect(box.y, 'top edge clears the menu bar').toBeGreaterThanOrEqual(MENU_BAR_H)
  expect(box.y + box.height, 'bottom edge clears the dock').toBeLessThanOrEqual(1024 - DOCK_H)

  // Fitted, not rescaled: rescaling against a guessed 1920 writer would have
  // put it at 1180 * 768/1920 = 472 and shrunk it to 288x231. The size is
  // untouched because it fits, and the window is pushed only as far left as
  // it takes to be reachable.
  expect(box.width).toBeCloseTo(LEGACY_SIZE.width, 0)
  expect(box.height).toBeCloseTo(LEGACY_SIZE.height, 0)
  expect(box.x).toBeCloseTo(768 - WINDOW_EDGE_MARGIN - LEGACY_SIZE.width, 0) // 40
  expect(box.y, 'y was already reachable, so it is left exactly alone').toBeCloseTo(LEGACY_POSITION.y, 0)
})

test('the same v0.1.0 window on the 1920 screen that wrote it does not move at all', async ({ page }) => {
  // The control. Without it, a fit that simply flung every legacy window into
  // the top-left corner would pass the test above. A legacy blob read back on
  // a screen it already fits must be byte-identical — the upgrade path nearly
  // every v0.1.0 user is actually on.
  await page.setViewportSize({ width: 1920, height: 1080 })
  await bootWithLegacyState(page)
  const box = await restoredRect(page)
  console.log(`[1920x1080] restored at (${Math.round(box.x)},${Math.round(box.y)}) ${Math.round(box.width)}x${Math.round(box.height)}`)
  expect(box.x).toBeCloseTo(LEGACY_POSITION.x, 0)
  expect(box.y).toBeCloseTo(LEGACY_POSITION.y, 0)
  expect(box.width).toBeCloseTo(LEGACY_SIZE.width, 0)
  expect(box.height).toBeCloseTo(LEGACY_SIZE.height, 0)
})
