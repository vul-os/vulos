import { test, expect } from '@playwright/test'
import { bootShell, launchApp } from './contrast-scan'

/**
 * Three structural accessibility properties, across the shell and the builtin
 * apps.
 *
 * These are the objectively checkable ones — a heading level either skips or it
 * does not, a control either has an accessible name or it does not. They are not
 * a substitute for judgement (is this the right heading? is this label useful?)
 * and nothing here should be read as "the OS is accessible". It is the floor,
 * held, and the marketing site has had the same floor for a while; the product
 * did not.
 *
 * The sweep that prompted this found the shell already clean on headings and alt
 * text, and four icon-only menu-bar controls with no accessible name: network,
 * battery, the inline theme toggle and the system menu, all sharing one
 * StatusButton component that had no way to pass a name.
 */

const AUDIT = () => {
  const visible = (el: Element) =>
    (el as HTMLElement).offsetParent !== null && el.getBoundingClientRect().width > 2

  const levels = [...document.querySelectorAll('h1,h2,h3,h4,h5,h6')]
    .filter(visible)
    .map((h) => ({ n: Number(h.tagName[1]), t: (h.textContent || '').trim().slice(0, 40) }))
  const skips: string[] = []
  for (let i = 1; i < levels.length; i++) {
    if (levels[i].n - levels[i - 1].n > 1) {
      skips.push(`h${levels[i - 1].n} → h${levels[i].n} at "${levels[i].t}"`)
    }
  }

  // A MISSING alt is the defect. alt="" is not — it is the correct way to mark
  // an image decorative, and demanding text there makes screen readers announce
  // noise.
  const noAlt = [...document.querySelectorAll('img')]
    .filter(visible)
    .filter((i) => i.getAttribute('alt') === null)
    .map((i) => (i.getAttribute('src') || '').slice(-50))

  const unnamed = [...document.querySelectorAll('button, a[href]')]
    .filter(visible)
    .filter((el) => {
      if ((el.textContent || '').trim()) return false
      if (el.getAttribute('aria-label') || el.getAttribute('aria-labelledby')) return false
      if (el.getAttribute('title')) return false
      const img = el.querySelector('img[alt]')
      if (img && img.getAttribute('alt')) return false
      // An icon can name its own control with <title> or aria-label on the svg.
      if (el.querySelector('svg[aria-label], svg > title')) return false
      return true
    })
    .map((el) => `${el.tagName.toLowerCase()}.${String((el as HTMLElement).className).split(' ')[0].slice(0, 30)}`)

  return {
    headings: levels.length,
    controls: [...document.querySelectorAll('button, a[href]')].filter(visible).length,
    skips,
    noAlt: [...new Set(noAlt)],
    unnamed: [...new Set(unnamed)],
  }
}

const SURFACES = [null, 'Settings', 'Files', 'Contacts', 'Calendar'] as const

for (const app of SURFACES) {
  const name = app ?? 'the shell'
  test(`${name} keeps its headings, alt text and control names`, async ({ page }) => {
    test.setTimeout(120_000)
    await bootShell(page, 'dark')
    if (app) await launchApp(page, app)

    const r = await page.evaluate(AUDIT)

    // A surface that failed to render would satisfy every assertion below.
    expect(r.controls, `${name} rendered almost no controls`).toBeGreaterThan(3)

    expect(
      r.skips,
      `a skipped heading level tells a screen-reader user a section is missing:\n${r.skips.join('\n')}`,
    ).toEqual([])
    expect(
      r.noAlt,
      `these images have no alt attribute (alt="" is fine and means decorative):\n${r.noAlt.join('\n')}`,
    ).toEqual([])
    expect(
      r.unnamed,
      `these are announced as a bare "button" or "link":\n${r.unnamed.join('\n')}`,
    ).toEqual([])
  })
}

/**
 * Prove the three detectors fire, separately from whether today's UI trips them.
 *
 * The shell and every app pass, so nothing exercises the logic — and three
 * attempts to mutate the source into failing were all INERT: changing the first
 * heading (going UP a level is not a skip), and twice editing a heading that
 * turned out not to be the one rendered. Each inert attempt looks exactly like a
 * working gate, which is the whole reason this test exists.
 *
 * Injecting is unambiguous: it puts one of each defect into the live page,
 * asserts each is reported, removes them, and asserts the reports stop.
 */
test('the heading, alt and control-name detectors fire', async ({ page }) => {
  test.setTimeout(120_000)
  await bootShell(page, 'dark')

  const clean = await page.evaluate(AUDIT)
  expect(clean.skips, 'the shell must start clean for this to mean anything').toEqual([])
  expect(clean.noAlt).toEqual([])
  expect(clean.unnamed).toEqual([])

  await page.evaluate(() => {
    const host = document.createElement('div')
    host.id = 'a11y-probe'
    host.setAttribute('style', 'position:fixed;top:0;left:0;width:200px;height:120px;z-index:9999')
    // h2 → h5 is a three-level skip; both must be VISIBLE to count.
    host.innerHTML =
      '<h2>PROBE SECTION</h2>' +
      '<h5>PROBE SUBSECTION</h5>' +
      '<img src="data:image/gif;base64,R0lGODlhAQABAAAAACw=" width="20" height="20">' +
      '<button style="width:30px;height:30px"><svg width="10" height="10"></svg></button>'
    document.body.appendChild(host)
  })
  await page.waitForTimeout(200)

  const found = await page.evaluate(AUDIT)
  expect(found.skips.join('\n'), 'an h2 → h5 jump was not reported').toContain('h2 → h5')
  expect(found.noAlt.length, 'an <img> with no alt attribute was not reported').toBeGreaterThan(0)
  expect(found.unnamed.length, 'an icon-only button with no name was not reported').toBeGreaterThan(0)

  await page.evaluate(() => document.getElementById('a11y-probe')?.remove())
  const after = await page.evaluate(AUDIT)
  expect(after.skips, 'the detector kept reporting after the probe was removed').toEqual([])
  expect(after.noAlt).toEqual([])
  expect(after.unnamed).toEqual([])
})

/**
 * Every enabled control shows something when it takes focus.
 *
 * WCAG 2.4.7. A keyboard user navigating by Tab has no pointer to tell them
 * where they are; if focus changes nothing on screen, the control is
 * unreachable in practice even though it is reachable in the DOM.
 *
 * This asserts the WEAK property — that focusing changes the rendered style at
 * all — not that the indicator is a good one. Contrast and thickness of the
 * indicator (WCAG 2.4.11) are a further question this does not answer, and
 * saying so matters more than the number of passing controls.
 *
 * Programmatic .focus() is enough here because this codebase styles
 * `.focus-primary:focus` AND `:focus-visible` together; a codebase that styled
 * only :focus-visible would need real keyboard input to test the same thing.
 *
 * ## What it will NOT notice
 *
 * Deleting the shell-wide ring from index.css does not fail this — measured, not
 * assumed. With the custom ring gone the browser's own default takes over, and
 * the browser default IS an indicator, so the property this asserts still holds.
 * The gate catches "no indicator at all"; it does not catch "a worse indicator".
 * Judging an indicator's quality is WCAG 2.4.11 and a different check.
 */
const FOCUS_CHECK = () => {
  const visible = (el: Element) =>
    (el as HTMLElement).offsetParent !== null && el.getBoundingClientRect().width > 2
  const snap = (el: Element) => {
    const cs = getComputedStyle(el)
    return [cs.outlineStyle, cs.outlineWidth, cs.outlineColor, cs.boxShadow,
            cs.borderColor, cs.backgroundColor, cs.color].join('|')
  }
  const controls = [...document.querySelectorAll('button, a[href], input, select, textarea, [tabindex]')]
    .filter(visible)
    .filter((el) => !(el as HTMLButtonElement).disabled)
  const without: string[] = []
  const active = document.activeElement as HTMLElement | null
  // Every control, not a sample. An arbitrary cap made this partial in a way
  // that hid itself: the self-test below appends its probe at the END of the
  // document, and with a slice(0, 40) the probe was never reached — the detector
  // looked broken when it was merely short-sighted, and every "clean" result
  // before that only described the first forty controls on the page.
  for (const el of controls) {
    const before = snap(el)
    ;(el as HTMLElement).focus()
    const after = snap(el)
    ;(el as HTMLElement).blur()
    if (before === after) {
      without.push(
        `${el.tagName.toLowerCase()}.${String((el as HTMLElement).className).split(' ')[0].slice(0, 30)}` +
          ` "${(el.textContent || '').trim().slice(0, 20)}"`,
      )
    }
  }
  active?.focus()
  return { checked: controls.length, without: [...new Set(without)] }
}

for (const app of [null, 'Settings', 'Contacts'] as const) {
  const name = app ?? 'the shell'
  test(`${name} shows a focus indicator on every enabled control`, async ({ page }) => {
    test.setTimeout(120_000)
    await bootShell(page, 'dark')
    if (app) await launchApp(page, app)

    const r = await page.evaluate(FOCUS_CHECK)
    expect(r.checked, `${name} exposed almost no controls to check`).toBeGreaterThan(5)
    expect(
      r.without,
      `these controls look identical focused and unfocused, so a keyboard user ` +
        `cannot see where they are:\n${r.without.join('\n')}`,
    ).toEqual([])
  })
}

test('the focus-indicator detector fires', async ({ page }) => {
  test.setTimeout(120_000)
  await bootShell(page, 'dark')

  const clean = await page.evaluate(FOCUS_CHECK)
  expect(clean.without, 'the shell must start clean for this to mean anything').toEqual([])

  await page.evaluate(() => {
    const host = document.createElement('div')
    host.id = 'focus-probe'
    host.setAttribute('style', 'position:fixed;top:0;left:0;z-index:9999')
    // outline:none with nothing put back is the classic way to lose the
    // indicator, and it is invisible in review.
    // `!important` is needed, and that is worth understanding rather than
    // working around: index.css carries a shell-wide :focus-visible ring over
    // :is(button, a[href], input, …, [tabindex]:not([tabindex="-1"])) with
    // specificity chosen SO THAT a lone `outline-none` cannot suppress it. A
    // realistic probe — a button with outline:none — therefore still gets a
    // ring, and the first version of this test failed because the product
    // defended itself.
    //
    // So the probe defeats the rule deliberately. The gate still earns its place:
    // it catches that rule being weakened, and any custom widget that ends up
    // outside the selector list.
    host.innerHTML =
      '<button style="outline:none !important;box-shadow:none !important;' +
      'width:40px;height:24px;border:0;background:#333;color:#fff">P</button>'
    document.body.appendChild(host)
  })
  await page.waitForTimeout(150)

  const found = await page.evaluate(FOCUS_CHECK)
  expect(
    found.without.length,
    'a button with outline:none and no replacement was not reported',
  ).toBeGreaterThan(0)

  await page.evaluate(() => document.getElementById('focus-probe')?.remove())
  expect(
    (await page.evaluate(FOCUS_CHECK)).without,
    'the detector kept reporting after the probe was removed',
  ).toEqual([])
})
