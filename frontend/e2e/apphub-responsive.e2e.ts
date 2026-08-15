import { test, expect } from '@playwright/test'
import {
  bootHub, bootHubDraggedTo, hubWidth, overflowing, childrenEscaping,
  fontSizes, smallTargets, settle, HUB, hubBackend,
} from './apphub-harness'
import { hubModeFor } from '../src/builtin/apphub/hubMode'
import { manyApps, APPS } from './apphub-fixture'

/**
 * An arm64 box browsing a catalogue that contains x86_64-only Flathub apps.
 *
 * Lutris rather than Steam, deliberately. Steam is the obvious example and it
 * is NOT in this catalogue: roadmap/APP-CATALOG.md policy 1a excludes
 * proprietary apps for now, which takes Steam, Chrome, Spotify, Discord, Slack
 * and Zoom with it. Writing a fixture around one of those would model a
 * catalogue this product does not ship and quietly teach the next reader that
 * it does.
 *
 * Lutris is the honest stand-in: open source, kept by policy 1a's own list of
 * what remains in gaming, and genuinely x86_64-only on Flathub. Policy 1a notes
 * that removing proprietary apps shrinks the x86_64-only set — it does not empty
 * it, which is exactly why this comparison still has to work.
 */
const ARM_BOX_CATALOGUE = [
  ...APPS,
  {
    ...APPS[0],
    id: 'lutris', name: 'Lutris', type: 'desktop',
    flatpak_id: 'net.lutris.Lutris', arch: ['x86_64'],
    description: 'Open gaming platform — install and manage games from many sources',
    category: 'games',
  },
]

/**
 * The App Hub lays out correctly at EVERY width, measured in a real browser.
 *
 * ── Why a sweep and not three breakpoints ───────────────────────────────────
 *
 * The hub is a window. A user drags its frame, so its width is not one of three
 * design widths — it is every integer between the window manager's minimum and
 * the screen. A spec that checks 1440, 768 and 390 checks three points on a
 * continuum and says nothing about the 1437 between them. The widths below are
 * chosen to land ON and AROUND each structural boundary (1080, 700, 440) as
 * well as in the middle of each range, because a boundary is where a layout
 * breaks and a midpoint is where it is claimed to work.
 *
 * ── Why the hub's width and not the viewport's ──────────────────────────────
 *
 * Everything here is phrased against `hubWidth(page)`, never the viewport. The
 * defect this whole wave exists to fix was `xl:grid-cols-3` — a VIEWPORT media
 * query — laying out three columns inside a 442px window on a 1440px screen,
 * with the third column rendered outside the frame. A gate driven by
 * setViewportSize alone cannot produce that state and therefore could never
 * have caught it, which is why the dragged cases below exist.
 */

/** On/around every boundary, plus the middle of every range. */
const MAXIMISED_WIDTHS = [1600, 1440, 1200, 1081, 1080, 1079, 1024, 900, 820, 768]
const PHONE_WIDTHS = [480, 441, 440, 439, 430, 414, 390, 375, 360, 320]
/** Small windows on a 1440px screen — no viewport query can see these. */
const DRAGGED_WIDTHS = [900, 700, 620, 520, 440, 400, 360]

/**
 * The floor every rendered string in the hub must clear.
 *
 * 12px is this suite's minimum legible size. The hub's own stylesheet asserted
 * that floor in a comment and then wrote `font-size: 11px` six times underneath
 * it, so the rule existed only as prose. Measuring the RENDERED size is what
 * makes it a rule: the type scale is clamp()ed against container units, so a
 * value that is legal at 1440 can compute to something smaller at 360 and no
 * amount of reading the stylesheet will tell you where.
 */
const MIN_FONT_PX = 12

async function assertLaysOut(page: import('@playwright/test').Page, label: string) {
  const w = await hubWidth(page)

  // The layout CSS paints must be the layout JS believes it chose. If these can
  // disagree there is a width at which the panel is a modal dialog to a screen
  // reader and a docked column on screen.
  await expect(page.locator(HUB)).toHaveAttribute('data-hub-mode', hubModeFor(w))

  expect(await overflowing(page), `${label}: content wider than its box at hub width ${w}`).toEqual([])
  expect(await childrenEscaping(page), `${label}: element painted outside the hub at width ${w}`).toEqual([])

  const fonts = await fontSizes(page)
  expect(fonts.length, `${label}: no text measured at ${w}px — the hub did not render`).toBeGreaterThan(3)
  expect(
    fonts.filter((f) => f < MIN_FONT_PX),
    `${label}: text below ${MIN_FONT_PX}px at hub width ${w} (all sizes: ${fonts.join(', ')})`,
  ).toEqual([])

  // The document itself must not gain a horizontal scrollbar. A hub that fits
  // its own box while pushing the page wider has still broken the screen.
  const docSpill = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  )
  expect(docSpill, `${label}: the page scrolls horizontally at hub width ${w}`).toBeLessThanOrEqual(1)
}

test.describe('maximised on a desktop screen', () => {
  for (const width of MAXIMISED_WIDTHS) {
    test(`lays out at ${width}px`, async ({ page }) => {
      test.setTimeout(150_000)
      await bootHub(page, { width, height: 900 })
      await assertLaysOut(page, `desktop ${width}`)
    })
  }
})

test.describe('a phone or small tablet', () => {
  for (const width of PHONE_WIDTHS) {
    test(`lays out at ${width}px`, async ({ page }) => {
      test.setTimeout(150_000)
      await bootHub(page, { width, height: 844 })
      await assertLaysOut(page, `phone ${width}`)

      // Touch ergonomics, at the widths where the pointer is a finger.
      // 30px is the hub's own control height; the exemption for a control whose
      // hit area is stretched by a pseudo-element is a MEASUREMENT (see
      // smallTargets), not a blanket pass.
      expect(
        await smallTargets(page, 30),
        `phone ${width}: controls too small to hit with a finger`,
      ).toEqual([])
    })
  }
})

test.describe('a window dragged narrow on a 1440px screen', () => {
  // The case that motivated all of this: every desktop media query still
  // matches, and the hub must ignore all of them and read its own box.
  for (const width of DRAGGED_WIDTHS) {
    test(`lays out when dragged to ${width}px`, async ({ page }) => {
      test.setTimeout(150_000)
      const got = await bootHubDraggedTo(page, width)
      await assertLaysOut(page, `dragged ${width} (measured ${got})`)
    })
  }
})

test('the grid never renders a column narrower than its own cards', async ({ page }) => {
  test.setTimeout(150_000)
  // A bare `minmax(264px, 1fr)` track cannot shrink below 264px, so in a
  // narrower frame the single column is wider than its container and the grid
  // overflows. This asserts the consequence — every card inside the frame —
  // rather than the CSS that achieves it.
  await bootHubDraggedTo(page, 360)
  const spill = await page.locator(HUB).evaluate((el) => {
    const box = el.getBoundingClientRect()
    let worst = 0
    for (const c of el.querySelectorAll('.hub-card')) {
      const r = c.getBoundingClientRect()
      worst = Math.max(worst, r.right - box.right, box.left - r.left)
    }
    return Math.round(worst)
  })
  expect(spill, 'a card is painted outside the hub frame').toBeLessThanOrEqual(1)
})

test('the rail keeps its row height when it has to scroll', async ({ page }) => {
  test.setTimeout(150_000)
  // .hub-rail is a column flex container with overflow-y:auto. A flex item's
  // default flex-shrink:1 acts on the main axis, which in a column is HEIGHT,
  // so without `flex: none` every row compresses instead of the rail
  // scrolling. Measured before the fix, in a 708x467 window: rows rendered
  // 22px, not the 30px they are styled at. Invisible at a tall viewport, which
  // is why a maximised screenshot never showed it.
  await bootHubDraggedTo(page, 900, { height: 620 })
  const heights = await page.locator(HUB).evaluate((el) =>
    [...el.querySelectorAll('.hub-rail-item')].map((n) => Math.round(n.getBoundingClientRect().height)),
  )
  expect(heights.length, 'no rail rows were measured').toBeGreaterThan(6)
  expect(
    heights.filter((h) => h < 29),
    `rail rows compressed below their styled height: ${heights.join(', ')}`,
  ).toEqual([])
})

test('the filter rail does not scroll itself away from its first chip', async ({ page }) => {
  test.setTimeout(150_000)
  // Scroll anchoring, which is on by default: the "Runs on this box" chip is
  // inserted at the HEAD of the rail once the catalogue loads and something in
  // it turns out to be incompatible, and the browser compensates by scrolling
  // right by exactly that chip's width. Measured before the fix on a 390px
  // phone against an arm64 box: scrollLeft settled at 157px, parking the
  // architecture filter off the left edge of its own scroller — on the one
  // width where the chip rail is the ONLY way to reach that filter.
  await bootHub(page, {
    width: 390, height: 844,
    backend: hubBackend(ARM_BOX_CATALOGUE, { cache: { ready: true, arch: 'arm64' } }),
  })
  await settle(page)

  const rail = page.locator(`${HUB} .hub-chips`)
  const first = rail.locator('.hub-chip').first()
  await expect(first).toContainText('Runs on this box')

  expect(
    await rail.evaluate((el) => Math.round(el.scrollLeft)),
    'the chip rail scrolled itself on load, hiding its own first control',
  ).toBe(0)

  // The chip is not merely present in the DOM — it is inside the visible box.
  const visible = await rail.evaluate((el) => {
    const c = el.querySelector('.hub-chip') as HTMLElement
    const r = c.getBoundingClientRect()
    const b = el.getBoundingClientRect()
    return r.left >= b.left - 1 && r.right <= b.right + 1
  })
  expect(visible, 'the first chip is in the DOM but outside the visible rail').toBe(true)
})

test('the search field offers exactly one clear button', async ({ page }) => {
  test.setTimeout(150_000)
  /**
   * input[type=search] makes Chromium draw its OWN × as well as the hub's, so a
   * field with a query in it showed two clear glyphs side by side.
   *
   * Measured by BEHAVIOUR, and that is the whole point of this comment. The
   * native button lives in the input's shadow DOM: no selector finds it,
   * `elementFromPoint` resolves through it to the input, and
   * `getComputedStyle(el, '::-webkit-search-cancel-button')` silently falls
   * back to the input's own style — it reports `appearance: auto` and the
   * INPUT's 194x40 box whether the rule suppressing it is present or not. The
   * first version of this test used exactly that hit-test and PASSED with the
   * fix reverted, which is the defect class this repository has the most of.
   *
   * What does discriminate is what the pixel does. Measured with the fix
   * reverted, on a 390px phone, against a build served from a PRIVATE output
   * directory (the shared dist/ is rebuilt by other agents mid-run and is not
   * evidence of anything):
   *
   *   input box [12, 206], padding-right 34px, so content box ends at 171
   *   hub's own button        [176, 200]   -> clicks here clear, by design
   *   native cancel button    ~[160, 171]  -> clicks here clear ONLY if present
   *   inert padding           [200, 206]   -> clicks here never clear
   *
   * The native button sits INSIDE the content box, to the LEFT of the hub's
   * own. The first version of this test clicked 3px from the outer right edge —
   * inert padding — and therefore passed whether or not the native button
   * existed. Aiming at the content-box edge instead is what makes it a test.
   *
   * With the fix in place that point is ordinary text area: clicking it places
   * a caret and leaves the value alone.
   */
  await bootHub(page, { width: 390, height: 844 })
  const input = page.getByPlaceholder('Search apps')
  await input.fill('matrix')
  await settle(page)

  // The native button hugs the inner (content-box) right edge, so derive the
  // probe point from that rather than from a hard-coded offset.
  const probe = await input.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const cs = getComputedStyle(el)
    const contentRight = r.right - parseFloat(cs.paddingRight) - parseFloat(cs.borderRightWidth)
    return { x: contentRight - 6, y: r.top + r.height / 2 }
  })

  // Guard the guard: if the probe point were on the hub's OWN clear button, the
  // click below would clear the field for a legitimate reason and this test
  // would fail forever instead of measuring anything.
  const onOwnButton = await page.evaluate(
    ([x, y]) => !!(document.elementFromPoint(x, y) as Element | null)?.closest('.hub-search-clear'),
    [probe.x, probe.y] as const,
  )
  expect(onOwnButton, "the probe point landed on the hub's own clear button — it proves nothing there").toBe(false)

  await page.mouse.click(probe.x, probe.y)
  await settle(page)

  await expect(
    input,
    'clicking the inner right edge of the field cleared it, so Chromium is ' +
      "drawing its own search-cancel button there alongside the hub's",
  ).toHaveValue('matrix')
})

test('a hundred apps still lay out on a phone', async ({ page }) => {
  test.setTimeout(150_000)
  await bootHub(page, { width: 390, height: 844, backend: hubBackend(manyApps(100)) })
  await settle(page)
  await expect(page.locator(`${HUB} .hub-card`)).toHaveCount(100)
  await assertLaysOut(page, 'phone 390 with 100 apps')
})

test('an app with no natural break point in its name does not push the grid open', async ({ page }) => {
  test.setTimeout(150_000)
  // 'Hyperconvergent-Infrastructure-Orchestrator' — overflow-wrap cannot break
  // a hyphenless run, and a column narrower than the word is what defeats a
  // truncation rule that assumes spaces.
  await bootHub(page, { width: 320, height: 844 })
  await page.getByPlaceholder('Search apps').fill('Hyperconvergent')
  await settle(page)
  await expect(page.locator(`${HUB} .hub-card`)).toHaveCount(1)
  await assertLaysOut(page, 'phone 320, unbreakable name')
})

test('the detail panel is a docked column when wide and a modal sheet when not', async ({ page }) => {
  test.setTimeout(150_000)
  // The ARIA has to follow the real layout: a sheet covering the list IS modal,
  // and announcing a docked column as a dialog is just as wrong the other way.
  await bootHub(page, { width: 1440, height: 900 })
  await page.getByRole('button', { name: 'Conduit', exact: true }).click()
  await settle(page)
  await expect(page.locator('.hub-detail')).toHaveAttribute('role', 'complementary')
  await assertLaysOut(page, 'wide with panel open')

  await bootHubDraggedTo(page, 520)
  await page.getByRole('button', { name: 'Conduit', exact: true }).click()
  await settle(page)
  await expect(page.locator('.hub-detail')).toHaveAttribute('role', 'dialog')
  await expect(page.locator('.hub-detail')).toHaveAttribute('aria-modal', 'true')
  await assertLaysOut(page, 'compact with sheet open')
})
