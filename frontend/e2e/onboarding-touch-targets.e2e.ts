import { test, expect, type Page } from '@playwright/test'
import { bootWizard, walkWizard, advance, settle, STEP_IDS, type StepId } from './onboarding-walk.js'
import { mkdirSync } from 'node:fs'

/**
 * onboarding-touch-targets.e2e.ts — the 44px floor inside the first-boot wizard,
 * at phone width, and the go-back path that the progress rail used to carry.
 *
 * # Why this file exists
 *
 * The wizard had never been measured at phone width by anything.
 * onboarding-contrast.e2e.ts walks all fifteen steps but runs at 1280;
 * mobile-touch-targets.e2e.ts holds a 44px floor at 390 but is scoped to the
 * SHELL chrome — the status bar, dock, home screen and switcher — and the shell
 * is not on screen during setup at all. First boot is the founder's stated
 * acceptance path and the first thing any new user sees, and a phone is a
 * plausible first client because the box has no screen of its own.
 *
 * Measured on the shipping build at 390×844 before this suite existed, the
 * wizard carried nine kinds of sub-floor control, the worst of them a 0.39 × 4
 * px <button> labelled "Go back to step 9 of 15" — fifteen of those, one per
 * step. src/auth/setup.css's TOUCH FLOOR block lists all nine with their
 * measurements, and WizardProgress in src/auth/Setup.tsx explains what replaced
 * the segments.
 *
 * # Why it measures the way it does
 *
 * A touch-floor gate that cannot fail is worse than no gate, and this repository
 * has shipped one: it asked whether a control cleared the floor and never
 * whether its CONTAINER could hold it, so a half-done fix passed 24/24. Three
 * things are asserted about every control rather than one:
 *
 *   1. its own border box is at least 44 × 44;
 *   2. the box that SURVIVES its clipping ancestors is still at least 44 × 44 —
 *      a 44px control in a 32px `overflow: hidden` row is 44px in the DOM and
 *      32px under a finger;
 *   3. document.elementFromPoint at its centre lands on it — a control that is
 *      big enough, unclipped, and covered by something else is not a target.
 *
 * Each control is scrolled to the centre of the wizard's scroll container first,
 * because that is the state a user meets it in, and because a rect read while
 * the control is below the fold answers a question nobody asked.
 *
 * The floors are asserted at 390×844 with a coarse pointer. The last describe
 * asserts the desktop is UNCHANGED, because the sizing is deliberately
 * `@media (pointer: coarse)`-scoped and "did not leak to the mouse" is a claim,
 * not an assumption.
 */

const SHOTS = 'test-results/onboarding-touch'
mkdirSync(SHOTS, { recursive: true })

/** 44px is the floor the shell already enforces (src/index.css `--touch-min`). */
const FLOOR = 44

/**
 * The ONE exemption, by class name, applied identically here and in setup.css.
 *
 * `.wz-linkish` is an inline action inside a sentence — the "Continue to the
 * desktop anyway" escape hatch in the finish-failure banner. WCAG 2.5.8 exempts
 * a target that is "in a sentence or otherwise constrained by the line-height of
 * non-target text" by name, and a 44px box would not enlarge it anyway: its
 * height is the line box it sits in.
 *
 * It is an allowlist of CLASSES, not a computed-style test for `display:
 * inline`, on purpose. "Exempt anything that is inline" is a hole a control can
 * be walked through — make it inline and it stops being measured. A control that
 * wants out of this floor has to be named here, in a commit.
 */
const EXEMPT_CLASSES = ['wz-linkish']

/**
 * The floor is meaningless on a step that rendered nothing, so each step
 * declares how many controls it must have measured. MEASURED, then set one
 * below the real count — the same discipline as onboarding-contrast.e2e.ts's
 * MIN_TEXT_NODES, and for the same reason: a wizard that crashed on step nine
 * would otherwise report eight clean steps.
 */
const MIN_CONTROLS: Record<StepId, number> = {
  welcome: 3,
  chooser: 4,
  device: 6,
  language: 20,
  timezone: 4,
  network: 4,
  account: 7,
  pin: 4,
  apps: 4,
  appearance: 8,
  identity: 4,
  storage: 5,
  ssh: 3,
  recoverykit: 4,
  ready: 3,
}

interface Bad {
  step: string
  label: string
  cls: string
  /** the control's own border box */
  w: number
  h: number
  /** what survives its clipping ancestors */
  vw: number
  vh: number
  /** whether elementFromPoint at the centre lands on it */
  hit: boolean
  why: string
}

interface Measured { bad: Bad[]; count: number }

/**
 * Measure every control in the wizard, at the position a user meets it.
 *
 * Runs entirely in the page so the DOM is read once per step rather than once
 * per control over the wire.
 */
async function measureStep(page: Page, step: string, exempt: string[]): Promise<Measured> {
  return page.evaluate(
    ({ step, exempt, FLOOR }) => {
      const root = document.querySelector('.wz-root')
      if (!root) return { bad: [{ step, label: '(no .wz-root)', cls: '', w: 0, h: 0, vw: 0, vh: 0, hit: false, why: 'the wizard is not on screen' }], count: 0 }

      const SEL = 'button, a[href], [role="button"], [role="menuitem"], input, select, textarea'

      /**
       * The thing a finger has to hit.
       *
       * For a checkbox or radio wrapped in a <label>, that is the LABEL: the
       * native box stays checkbox-sized and the whole sentence is tappable,
       * which is what setup.css's .wz-checkbox has always claimed and what its
       * TOUCH FLOOR block now states as a rule. The substitution is only made
       * when the label genuinely CONTAINS the input — an implicit association,
       * so a press anywhere in the label toggles it — and the label's box is
       * then required to contain the input's box, so a label that has drifted
       * away from its control cannot lend it a target it does not cover.
       */
      const targetFor = (el: Element): { el: Element; note: string } | null => {
        const isBox = el instanceof HTMLInputElement && (el.type === 'checkbox' || el.type === 'radio')
        if (!isBox) return { el, note: '' }
        const label = el.closest('label')
        if (!label) return { el, note: '' }
        const a = el.getBoundingClientRect()
        const b = label.getBoundingClientRect()
        const covered = a.left >= b.left - 1 && a.right <= b.right + 1 && a.top >= b.top - 1 && a.bottom <= b.bottom + 1
        if (!covered) return { el, note: 'its <label> does not cover it' }
        return { el: label, note: '' }
      }

      /** The box that survives every clipping ancestor. */
      const visibleRect = (el: Element) => {
        let r = el.getBoundingClientRect()
        let p = el.parentElement
        while (p && p !== document.documentElement) {
          const cs = getComputedStyle(p)
          const clipsX = cs.overflowX !== 'visible'
          const clipsY = cs.overflowY !== 'visible'
          if (clipsX || clipsY) {
            const pr = p.getBoundingClientRect()
            const left = clipsX ? Math.max(r.left, pr.left) : r.left
            const right = clipsX ? Math.min(r.right, pr.right) : r.right
            const top = clipsY ? Math.max(r.top, pr.top) : r.top
            const bottom = clipsY ? Math.min(r.bottom, pr.bottom) : r.bottom
            r = new DOMRect(left, top, Math.max(0, right - left), Math.max(0, bottom - top))
          }
          p = p.parentElement
        }
        return r
      }

      const bad: { step: string; label: string; cls: string; w: number; h: number; vw: number; vh: number; hit: boolean; why: string }[] = []
      const seen = new Set<Element>()
      let count = 0

      for (const el of root.querySelectorAll(SEL)) {
        const t = targetFor(el)
        if (!t) continue
        const target = t.el
        if (seen.has(target)) continue
        const cls = (target.getAttribute('class') || '').toString()
        if (exempt.some((c) => cls.split(/\s+/).includes(c))) continue

        // A zero box is a hidden control, not a small one (sr-only inputs, the
        // steps not currently rendered).
        const raw = target.getBoundingClientRect()
        if (raw.width === 0 || raw.height === 0) continue
        seen.add(target)
        count++

        // Measure where a user would: scrolled into view. A rect read while the
        // control is below the fold is not the rect anyone taps.
        target.scrollIntoView({ block: 'center', inline: 'center' })
        const box = target.getBoundingClientRect()
        const vis = visibleRect(target)
        const cx = vis.left + vis.width / 2
        const cy = vis.top + vis.height / 2
        const at = document.elementFromPoint(cx, cy)
        const hit = !!at && (at === target || target.contains(at) || at.contains(target))

        const why: string[] = []
        if (t.note) why.push(t.note)
        if (box.width < FLOOR || box.height < FLOOR) why.push(`box ${Math.round(box.width * 100) / 100}×${Math.round(box.height * 100) / 100} is under ${FLOOR}`)
        else if (vis.width < FLOOR || vis.height < FLOOR) why.push(`clipped by an ancestor to ${Math.round(vis.width * 100) / 100}×${Math.round(vis.height * 100) / 100}`)
        if (!hit) why.push(`covered at its centre by <${at ? at.tagName.toLowerCase() : 'nothing'}>`)

        if (why.length) {
          bad.push({
            step,
            label: (target.getAttribute('aria-label') || target.textContent || '?').toString().trim().slice(0, 48),
            cls: cls.slice(0, 48),
            w: Math.round(box.width * 100) / 100,
            h: Math.round(box.height * 100) / 100,
            vw: Math.round(vis.width * 100) / 100,
            vh: Math.round(vis.height * 100) / 100,
            hit,
            why: why.join('; '),
          })
        }
      }
      return { bad, count }
    },
    { step, exempt, FLOOR },
  )
}

test.describe('first-boot wizard, phone 390×844 (touch)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 3 })

  test('nothing in the wizard is under 44px, on any of the fifteen steps', async ({ page }) => {
    test.setTimeout(400_000)
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(e.message))
    await bootWizard(page, 'dark')

    const bad: Bad[] = []
    const counts: Partial<Record<StepId, number>> = {}
    const visited: StepId[] = []

    await walkWizard(page, async (step) => {
      await settle(page)
      visited.push(step)
      const m = await measureStep(page, step, EXEMPT_CLASSES)
      bad.push(...m.bad)
      counts[step] = m.count

      // No step may push the page sideways. Widening every control to 44px is
      // exactly the change that does it.
      const o = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, inner: window.innerWidth }))
      expect(o.doc, `step "${step}" overflows horizontally (${o.doc} > ${o.inner})`).toBeLessThanOrEqual(o.inner + 1)
    })

    await page.screenshot({ path: `${SHOTS}/phone-390-ready.png` })

    // Vacuity guards FIRST: a floor over an empty page passes trivially.
    expect(visited, 'the walk did not reach all fifteen steps').toEqual([...STEP_IDS])
    for (const step of STEP_IDS) {
      expect(
        counts[step] ?? 0,
        `step "${step}" offered ${counts[step] ?? 0} controls to measure, expected at least ${MIN_CONTROLS[step]} — the step is hollow, so its clean floor means nothing`,
      ).toBeGreaterThanOrEqual(MIN_CONTROLS[step])
    }

    expect(bad, `sub-${FLOOR}px or unhittable controls in the first-boot wizard:\n${JSON.stringify(bad, null, 2)}`).toEqual([])
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('the recovery-phrase reveal is measured too, before and after it is revealed', async ({ page }) => {
    test.setTimeout(400_000)
    // The walk above measures each step as it FIRST renders, which is the state
    // a user meets — but the account step has a second face. After
    // POST /api/auth/register it swaps in MasterKeyReveal, the screen where the
    // only credential that can recover this account is shown once and confirmed.
    // Measuring only the account FORM would leave the wizard's most consequential
    // surface outside the floor, so it gets measured explicitly, in both of its
    // states: blurred behind "Tap to reveal", and revealed.
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(e.message))
    await bootWizard(page, 'dark')
    for (const step of ['welcome', 'chooser', 'device', 'language', 'timezone', 'network'] as StepId[]) {
      await settle(page)
      await advance(page, step)
    }
    await settle(page)

    // The account form, filled the way onboarding-walk fills it. Not reused from
    // `advance('account')` because that helper walks straight THROUGH the reveal,
    // which is the surface under test here.
    await page.locator('#wz-name').fill('Ada Lovelace')
    await page.locator('#wz-username').fill('ada')
    await page.locator('#wz-password').fill('correct-horse-battery')
    await page.locator('#wz-password2').fill('correct-horse-battery')
    await settle(page)
    await page.getByRole('button', { name: /create account/i }).first().click()
    await settle(page)
    await expect(page.getByText(/save your recovery phrase/i)).toBeVisible({ timeout: 15_000 })

    const blurred = await measureStep(page, 'account/reveal (blurred)', EXEMPT_CLASSES)
    expect(blurred.count, 'the blurred reveal offered nothing to measure').toBeGreaterThanOrEqual(4)

    await page.getByRole('button', { name: /tap to reveal/i }).click()
    await settle(page)
    const revealed = await measureStep(page, 'account/reveal (revealed)', EXEMPT_CLASSES)
    expect(revealed.count, 'the revealed phrase screen offered nothing to measure').toBeGreaterThanOrEqual(5)
    await page.screenshot({ path: `${SHOTS}/phone-390-recovery-reveal.png` })

    const bad = [...blurred.bad, ...revealed.bad]
    expect(bad, `sub-${FLOOR}px or unhittable controls on the recovery-phrase screen:\n${JSON.stringify(bad, null, 2)}`).toEqual([])
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('the progress rail is decoration — it holds no controls at all', async ({ page }) => {
    // The floor above would already catch fifteen 0.39×4 px buttons. This says
    // the stronger thing: they are not there to catch. Fifteen segments are
    // fifteen tab stops and fifteen screen-reader announcements even at a size
    // that passes, and the rail is 70px wide on this viewport, so no arrangement
    // of fifteen 44px targets fits in it.
    await bootWizard(page, 'dark')
    await advance(page, 'welcome')
    await settle(page)

    const rail = await page.evaluate(() => ({
      segs: document.querySelectorAll('.wz-seg').length,
      focusable: document.querySelectorAll('.wz-track button, .wz-track a[href], .wz-track [role="button"], .wz-track [tabindex]').length,
      announced: document.querySelectorAll('.wz-track *:not([aria-hidden="true"])').length,
      trackWidth: Math.round((document.querySelector('.wz-track')?.getBoundingClientRect().width ?? 0) * 100) / 100,
    }))
    expect(rail.segs, 'the segmented rail is gone entirely — this asserts it is decorative, not absent').toBe(15)
    expect(rail.focusable, 'the progress rail has focusable children again').toBe(0)
    expect(rail.announced, 'the progress segments are announced individually again').toBe(0)
    // One progressbar, one announcement.
    await expect(page.getByRole('progressbar')).toHaveAttribute('aria-valuemax', '15')
  })

  test('an earlier step is still reachable, and the jump lands where it says', async ({ page }) => {
    test.setTimeout(400_000)
    // THE regression this pairs with. The segments were the only way to reach a
    // step other than the previous one; replacing them may not drop that.
    await bootWizard(page, 'dark')

    // Walk to 'network', the sixth step (index 5).
    for (const step of ['welcome', 'chooser', 'device', 'language', 'timezone'] as StepId[]) {
      await settle(page)
      await advance(page, step)
    }
    await settle(page)
    await expect(page.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '6')

    // The counter is the control now, and it is a real one.
    const counter = page.getByRole('button', { name: /Step 6 of 15\. Go back to an earlier step\./ })
    await expect(counter, 'there is no go-back control in the wizard header').toBeVisible()
    const cbox = await counter.boundingBox()
    if (!cbox) throw new Error('the go-back control has no box')
    expect(cbox.width, 'the go-back control is under the floor').toBeGreaterThanOrEqual(FLOOR)
    expect(cbox.height, 'the go-back control is under the floor').toBeGreaterThanOrEqual(FLOOR)

    await counter.click()
    const menu = page.getByRole('menu', { name: 'Go back to an earlier step' })
    await expect(menu).toBeVisible()
    await page.screenshot({ path: `${SHOTS}/phone-390-jump-menu.png` })

    // Exactly the steps BEHIND you — the same rule the old segments used. A menu
    // offering the steps ahead would be a new capability, not a preserved one.
    const items = menu.getByRole('menuitem')
    await expect(items).toHaveCount(5)
    await expect(items.nth(0)).toContainText('Welcome')
    await expect(items.nth(3)).toContainText('Language')

    // Every row in the menu is a real target too.
    for (let i = 0; i < 5; i++) {
      const b = await items.nth(i).boundingBox()
      if (!b) throw new Error(`menu row ${i} has no box`)
      expect(b.height, `menu row ${i} is ${b.height}px tall`).toBeGreaterThanOrEqual(FLOOR)
      expect(b.width, `menu row ${i} is ${b.width}px wide`).toBeGreaterThanOrEqual(FLOOR)
    }

    // The jump LANDS. Not "the menu closed" — the wizard is on the step the row
    // named, and its content is on screen.
    await items.nth(3).click()
    await settle(page)
    await expect(page.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '4')
    await expect(
      page.getByRole('button', { name: /English/i }).first(),
      'the wizard reports step 4 but the language step is not on screen',
    ).toBeVisible()

    // And the menu shrinks with the position, so it can never point forward.
    await page.getByRole('button', { name: /Step 4 of 15\. Go back to an earlier step\./ }).click()
    await expect(menu.getByRole('menuitem')).toHaveCount(3)
  })

  test('the footer Back button still moves the wizard back', async ({ page }) => {
    // The other half of "do not silently drop the ability to go back": the menu
    // is an addition to the one-step path, not a replacement for it.
    await bootWizard(page, 'dark')
    await settle(page)
    await advance(page, 'welcome')
    await settle(page)
    await advance(page, 'chooser')
    await settle(page)
    await expect(page.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '3')
    // Scoped to the action bar: the header's go-back control also has "back" in
    // its name, and this test is about the OTHER path.
    await page.locator('.wz-nav').getByRole('button', { name: /back/i }).first().click()
    await settle(page)
    await expect(page.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '2')
  })
})

test.describe('first-boot wizard, desktop 1280×800 (mouse) — density unchanged', () => {
  test.use({ viewport: { width: 1280, height: 800 }, hasTouch: false, isMobile: false })

  test('the touch sizing did not leak onto a pointer', async ({ page }) => {
    // The floor is a property of the INPUT DEVICE, not of the app: setup.css
    // scopes all of it to `@media (pointer: coarse)`. If that scoping is ever
    // dropped the wizard inflates for every desktop user, and nothing else in
    // the suite would notice.
    await bootWizard(page, 'dark')
    for (const step of ['welcome', 'chooser', 'device', 'language', 'timezone', 'network'] as StepId[]) {
      await settle(page)
      await advance(page, step)
    }
    await settle(page)

    const dense = await page.evaluate(() => {
      const themeBtn = [...document.querySelectorAll('.wz-header-inner button')]
        .find((b) => (b.getAttribute('aria-label') || '').startsWith('Theme mode'))
      const t = themeBtn?.getBoundingClientRect()
      return { themeW: Math.round(t?.width ?? 0), themeH: Math.round(t?.height ?? 0) }
    })
    expect(dense.themeH, 'the desktop theme toggle grew — the coarse-pointer scoping leaked').toBeLessThan(FLOOR)
    expect(dense.themeW, 'the desktop theme toggle grew — the coarse-pointer scoping leaked').toBeLessThan(FLOOR)

    // The capability is not touch-only either: the go-back control is the same
    // control with a mouse.
    await page.getByRole('button', { name: /Go back to an earlier step\./ }).click()
    await expect(page.getByRole('menu', { name: 'Go back to an earlier step' })).toBeVisible()
  })

  test('the wizard switch keeps its desktop size', async ({ page }) => {
    await bootWizard(page, 'dark')
    for (const step of ['welcome', 'chooser', 'device', 'language', 'timezone', 'network', 'account', 'pin', 'apps'] as StepId[]) {
      await settle(page)
      await advance(page, step)
    }
    await settle(page)
    const sw = await page.locator('.wz-switch').first().boundingBox()
    if (!sw) throw new Error('the appearance step has no switch')
    expect(Math.round(sw.width), 'the switch grew on a mouse-driven desktop').toBe(44)
    expect(Math.round(sw.height), 'the switch grew on a mouse-driven desktop').toBe(24)
  })
})
