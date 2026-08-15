import { test, expect, type Page } from '@playwright/test'
import { belowAA, textNodeCount } from './contrast-scan'
import { bootHub, hubBackend, settle, HUB } from './apphub-harness'
import { APPS } from './apphub-fixture'

/**
 * Every state of the App Hub meets WCAG AA, on BOTH themes.
 *
 * app-contrast.e2e.ts covers four builtins and the hub is not one of them, so
 * until now nothing measured it. That mattered here more than most: the hub's
 * badges were `text-sky-400` / `text-amber-400` / `text-emerald-400` over a
 * 10%-alpha fill of the same hue, which on this theme's light surfaces measures
 * between 1.6:1 and 2.9:1 — and there are two of them on every one of the 30
 * cards, so it was the most repeated failure on the screen.
 *
 * ── Why STATES and not just "the App Hub" ────────────────────────────────────
 *
 * A single scan of the default grid measures the cards and nothing else. The
 * hub's error text, its empty states, its "Installed" affordance, its progress
 * notice and its detail panel are all painted in DIFFERENT colours from
 * different tokens, and none of them are on screen when the catalogue loads
 * normally. A gate that only ever sees the happy path is how the placeholder
 * defect in this repo survived for months: clean, and unmeasurable.
 *
 * Each case therefore drives the hub into one state and asserts two things —
 * that the state actually rendered (a vacuity floor, since belowAA() on a blank
 * screen returns [] and reads exactly like a pass) and that nothing in it is
 * below its AA threshold on composited pixels.
 *
 * The recorded trap for this repo, which is why the shared scanner is used
 * rather than anything that reads tokens: a contrast gate that reads CSS values
 * is BLIND to `opacity`, and `aria-hidden` is not an exemption — a decorative
 * label is still text a sighted reader is trying to read.
 */

interface HubState {
  name: string
  /** Backend override; omitted means the standard 30-app catalogue. */
  backend?: Record<string, unknown>
  /** Drive the hub into the state after it has loaded. */
  drive?: (page: Page) => Promise<void>
  /** A string that proves the state is actually on screen. */
  proof: RegExp | string
  /**
   * Vacuity floor: text nodes the whole page must carry. Measured, not guessed
   * — each sits below the real count for that state so a hub that rendered
   * nothing cannot pass, while a state that legitimately carries little text
   * (the empty catalogue) is not held to the grid's number.
   */
  minText: number
}

const STATES: HubState[] = [
  {
    name: 'the browse grid',
    proof: 'Conduit',
    minText: 60,
  },
  {
    name: 'an open detail panel',
    drive: async (page) => {
      await page.getByRole('button', { name: 'Conduit', exact: true }).click()
      await expect(page.getByRole('heading', { name: /Conduit/ })).toBeVisible()
    },
    proof: 'Apache-2.0',
    minText: 60,
  },
  {
    name: 'the Installed tab, with apps in it',
    drive: async (page) => {
      await page.getByRole('tab', { name: /Installed/ }).click()
      await settle(page)
    },
    // Two fixture apps carry installed:true, so this renders the "Installed"
    // card affordance — a state the browse grid never shows.
    proof: 'Already Here',
    minText: 20,
  },
  {
    name: 'a catalogue that failed to load',
    backend: {
      'GET /api/store/registry': { status: 503, contentType: 'application/json', body: '{"error":"registry unavailable"}' },
      'GET /api/store/installed': { status: 200, contentType: 'application/json', body: '[]' },
      'GET /api/packages/cache': { status: 200, contentType: 'application/json', body: '{"ready":true,"arch":"amd64"}' },
    },
    proof: /could not be loaded/i,
    minText: 12,
  },
  {
    name: 'an empty catalogue',
    backend: hubBackend([]),
    proof: /catalogue is empty|Nothing/i,
    minText: 12,
  },
  {
    name: 'a search that matches nothing',
    drive: async (page) => {
      await page.getByPlaceholder('Search apps').fill('zzznomatch')
      await settle(page)
    },
    proof: /No apps match/,
    minText: 12,
  },
  {
    name: 'the package-index notice',
    backend: hubBackend(APPS, { cache: { ready: false, arch: 'amd64' } }),
    proof: 'Package index required',
    minText: 60,
  },
  {
    name: 'an app this box cannot run',
    drive: async (page) => {
      await page.getByPlaceholder('Search apps').fill('PowerOnly')
      await page.getByRole('button', { name: 'PowerOnly', exact: true }).click()
      await expect(page.getByText(/Not available for this machine/)).toBeVisible()
    },
    proof: /Not available for this machine/,
    minText: 12,
  },
]

/**
 * Split belowAA()'s findings into the hub's and everything else's, by DOM.
 *
 * belowAA() scans the whole document, which is right — a builtin is only really
 * accessible if the screen it is on is — but it means a defect somewhere else on
 * that screen lands in this gate's lap. There is exactly one such defect today
 * and it is worth naming, because a suppression nobody can explain is how gates
 * rot: opening ANY window on the phone makes the mobile dock show a window-count
 * badge, and that badge paints var(--accent-contrast) on .accent-bg — white on
 * rgb(59,130,246) — at 3.68:1, in both themes. It lives in the mobile layout
 * (src/layouts/MobileStack.tsx), it appears for Settings and Files just as
 * readily as for the hub, and it is reported for its owner to fix.
 *
 * The split is made by ASKING THE DOM, not by listing strings. For each finding,
 * the elements carrying that exact text are located; the finding is foreign only
 * if at least one element carries it and NONE of them is inside the hub. Both
 * halves of that condition matter:
 *
 *  - "none inside the hub" means the moment the hub paints the same failing
 *    string, it stops being excused and this gate fails.
 *  - "at least one element carries it" means a finding this lookup cannot
 *    resolve — a ::before, a placeholder, an input value — is never quietly
 *    dropped. It counts as the hub's and fails loudly, which is the safe
 *    direction for a check whose whole job is to not miss things.
 */
async function attribute(page: Page, findings: string[]): Promise<{ hub: string[]; foreign: string[] }> {
  const hub: string[] = []
  const foreign: string[] = []
  for (const f of findings) {
    // belowAA's format: `<ratio> (need <n>) <color> on <bg> <size>px "<text>"`.
    const m = /^[\d.]+ \(need [\d.]+\) (.+?) on rgb\([^)]*\) ([\d.]+)px "(.*)"$/.exec(f)
    if (!m) { hub.push(f); continue }
    const [, color, size, label] = m

    /**
     * Match on the COLOUR as well as the text.
     *
     * Text alone is not an identifier and the first version of this proved it:
     * the mobile dock's window-count badge reads "1", and so does the hub's own
     * "Internet 1" category count. Looking up by text found both, one of them
     * inside the hub, so the dock's failure was attributed to the hub and every
     * phone case failed on a defect it does not own.
     *
     * The pair (text, computed colour, font-size) is what belowAA actually
     * measured, so matching on it identifies the element that produced the
     * finding rather than anything that merely says the same word.
     */
    const where = await page.evaluate(([lbl, col, px, sel]) => {
      const ph = /^placeholder: (.*)$/.exec(lbl as string)
      const hits: Element[] = []
      if (ph) {
        for (const el of document.querySelectorAll('input[placeholder], textarea[placeholder]')) {
          if ((el as HTMLInputElement).placeholder !== ph[1]) continue
          if (getComputedStyle(el, '::placeholder').color !== col) continue
          hits.push(el)
        }
      } else {
        for (const el of document.querySelectorAll('*')) {
          if (el.children.length) continue
          if ((el.textContent || '').trim() !== lbl) continue
          const cs = getComputedStyle(el)
          if (cs.color !== col) continue
          if (Math.abs(parseFloat(cs.fontSize) - Number(px)) > 0.6) continue
          hits.push(el)
        }
      }
      return { found: hits.length, inHub: hits.some((e) => !!e.closest(sel as string)) }
    }, [label, color, size, HUB] as const)

    // Unresolvable findings (::before/::after content, input values) count as
    // the hub's and fail loudly — the safe direction for a check whose job is
    // to not miss things.
    if (where.found > 0 && !where.inHub) foreign.push(f)
    else hub.push(f)
  }
  return { hub, foreign }
}

for (const theme of ['dark', 'light'] as const) {
  for (const state of STATES) {
    // Both a desktop width and a phone width: the hub swaps whole structures
    // between them (rail becomes chips, panel becomes a modal sheet), so the
    // narrow layout paints text the wide one never shows.
    for (const { label, width, height } of [
      { label: 'wide', width: 1440, height: 900 },
      { label: 'phone', width: 390, height: 844 },
    ]) {
      test(`${state.name} meets WCAG AA on ${theme}, ${label}`, async ({ page }) => {
        test.setTimeout(150_000)
        await page.emulateMedia({ reducedMotion: 'reduce' })

        await bootHub(page, { theme, width, height, backend: state.backend })
        if (state.drive) await state.drive(page)
        await settle(page)

        await expect(page.locator(HUB)).toBeVisible()
        await expect(page.locator(HUB).getByText(state.proof).first()).toBeVisible()
        // The theme has to survive hydration, or this measures one theme twice
        // and reports an agreement it never checked.
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme)

        const measured = await textNodeCount(page)
        expect(
          measured,
          `${state.name} rendered ${measured} text nodes — too few to have painted ` +
            'the state under test, so belowAA() would pass vacuously',
        ).toBeGreaterThan(state.minText)

        const { hub, foreign } = await attribute(page, await belowAA(page))

        expect(
          hub,
          `${state.name} has text below WCAG AA on ${theme} at ${width}px, measured ` +
            'on composited pixels. ' +
            (foreign.length
              ? `(${foreign.length} further finding(s) belong to elements outside the ` +
                `hub and are reported separately: ${foreign.join(' | ')})`
              : ''),
        ).toEqual([])
      })
    }
  }
}
