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
