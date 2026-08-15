// widgets-shoot.mjs — drive the widget rail at real widths and write PNGs.
//
// A screenshot script, not a test: the assertions live in widgets-rail.e2e.ts.
// This exists so the rail can be LOOKED AT, in both themes and at phone, tablet
// and desktop widths, because this project has a documented history of visual
// defects surviving every green check.
//
//   node e2e/widgets-shoot.mjs [outdir]
//
// Requires a preview server on :4173 (npm run build && npx vite preview).
import { chromium } from 'playwright'
import { mkdirSync } from 'node:fs'
// The SAME mock backend the e2e suite uses, so these shots show the shell in the
// state the tests assert about rather than a hand-rolled approximation (it also
// dismisses the AI first-run dialog, which otherwise covers the whole desktop).
import { installBackend, json } from './mock-backend.js'

const OUT = process.argv[2] || 'widget-shots'
const BASE = process.env.E2E_BASE || 'http://localhost:4173'

const VIEWPORTS = [
  { name: 'phone', width: 390, height: 844 },
  { name: 'tablet-768', width: 768, height: 1024 },
  { name: 'tablet-834', width: 834, height: 1194 },
  { name: 'desktop-1280', width: 1280, height: 800 },
  { name: 'desktop-1680', width: 1680, height: 1050 },
]

mkdirSync(OUT, { recursive: true })

const log = (...a) => console.log(...a)

async function boot(page, theme, seed) {
  await page.addInitScript(([t, s]) => {
    try {
      localStorage.setItem('vulos-theme', t)
      localStorage.removeItem('vulos.widgets.layout.v1')
      if (s) localStorage.setItem('vulos.widgets.layout.v1', s)
    } catch { /* ignore */ }
  }, [theme, seed || ''])
  await installBackend(page, {
    'GET /api/pim/calendar/events': json({
      events: [
        { uid: 'e1', title: 'Launch review', start: new Date(Date.now() + 3.6e6).toISOString(), location: 'War room' },
        { uid: 'e2', title: 'Team sync', start: new Date(Date.now() + 9e7).toISOString() },
      ],
    }),
  })
  await page.goto(BASE, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(2500)
  await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme)
  await page.waitForTimeout(600)
}

async function openWindow(page) {
  try {
    await page.keyboard.press('Meta+k')
    const input = page.getByPlaceholder(/Search apps/)
    await input.waitFor({ timeout: 3000 })
    await input.fill('Calculator')
    await page.waitForTimeout(400)
    await page.keyboard.press('Enter')
    await page.waitForTimeout(1200)
  } catch { log('  (no window opened — palette unavailable)') }
}

const browser = await chromium.launch()
for (const vp of VIEWPORTS) {
  for (const theme of ['dark', 'light']) {
    const ctx = await browser.newContext({ viewport: { width: vp.width, height: vp.height }, deviceScaleFactor: 2 })
    const page = await ctx.newPage()
    const errors = []
    page.on('pageerror', (e) => errors.push(e.message))
    await boot(page, theme)
    await openWindow(page)

    const railBox = await page.evaluate(() => {
      const el = document.querySelector('[data-desktop-widgets]')
      if (!el) return null
      const r = el.getBoundingClientRect()
      const tiles = [...document.querySelectorAll('[data-widget-id]')].map((t) => ({
        id: t.getAttribute('data-widget-id'),
        size: t.getAttribute('data-size'),
        h: Math.round(t.getBoundingClientRect().height),
        w: Math.round(t.getBoundingClientRect().width),
      }))
      return {
        rail: { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) },
        viewport: { w: window.innerWidth, h: window.innerHeight },
        overflowsBottom: r.bottom > window.innerHeight + 1,
        tiles,
      }
    })

    const file = `${OUT}/${vp.name}-${theme}.png`
    await page.screenshot({ path: file })
    log(`${file}  rail=${railBox ? JSON.stringify(railBox.rail) : 'ABSENT'} overflowsBottom=${railBox?.overflowsBottom} tiles=${railBox?.tiles.length ?? 0}`)
    if (railBox?.tiles.length) log('   ' + railBox.tiles.map((t) => `${t.id}[${t.size}] ${t.w}x${t.h}`).join('  '))
    if (errors.length) log('   PAGE ERRORS: ' + errors.join(' | '))
    await ctx.close()
  }
}

// A second pass on the desktop: edit mode, the gallery, and the sandboxed widget.
{
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 2 })
  const page = await ctx.newPage()
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message))
  await boot(page, 'dark')
  await openWindow(page)
  await page.getByRole('button', { name: 'Edit widgets' }).click()
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${OUT}/edit-mode-dark.png` })
  log(`${OUT}/edit-mode-dark.png`)

  await page.getByRole('button', { name: 'Add widget' }).click()
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${OUT}/gallery-dark.png` })
  log(`${OUT}/gallery-dark.png`)

  await page.getByRole('button', { name: 'Add Moon phase' }).click()
  await page.waitForTimeout(1500)
  // A newly added widget lands at the END of the rail, which on a short desktop
  // is below the fold — scroll to it so the shot shows the thing being added.
  await page.evaluate(() => {
    const port = document.querySelector('.vwidget-scrollport')
    if (port) port.scrollTop = port.scrollHeight
  })
  await page.waitForTimeout(900)
  await page.screenshot({ path: `${OUT}/sandboxed-dark.png` })
  log(`${OUT}/sandboxed-dark.png`)

  await page.getByRole('button', { name: 'Configure World clock' }).click()
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${OUT}/config-dark.png` })
  log(`${OUT}/config-dark.png`)
  if (errors.length) log('   PAGE ERRORS: ' + errors.join(' | '))
  await ctx.close()
}

await browser.close()
log('done')
