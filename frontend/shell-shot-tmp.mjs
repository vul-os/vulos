// Shell screenshot harness. Boots the real dev-server app against the E2E
// mock backend, at desktop / tablet / phone widths, both themes.
import { chromium } from 'playwright'
import { installBackend } from '/Users/pc/code/vulos/vulos/frontend/e2e/mock-backend.js'
import fs from 'node:fs'

const OUT = '/private/tmp/claude-501/-Users-pc-code-vulos/efcdfd46-8cf5-4517-b23e-88863f6d926b/scratchpad/shots'
fs.mkdirSync(OUT, { recursive: true })

const tag = process.argv[2] || 'shot'
const only = process.argv[3] // optional viewport filter
const BASE = 'http://localhost:5299'

const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  tablet: { width: 900, height: 1000 },
  phone: { width: 390, height: 844 },
}

// A page.route-compatible shim: installBackend takes a Playwright `page`.
const browser = await chromium.launch()

const errs = []

async function boot(vp, theme) {
  const ctx = await browser.newContext({ viewport: VIEWPORTS[vp], deviceScaleFactor: 2 })
  const page = await ctx.newPage()
  page.on('console', (m) => { if (m.type() === 'error') errs.push(`[${vp}/${theme}] ${m.text()}`) })
  page.on('pageerror', (e) => errs.push(`[${vp}/${theme}] PAGEERROR ${String(e)}`))
  await page.addInitScript((t) => {
    try {
      localStorage.setItem('vulos-theme', t)
      localStorage.setItem('vulos-ai-firstrun-done', '1')
    } catch { /* noop */ }
  }, theme)
  await installBackend(page)
  await page.goto(BASE, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(2500)
  return { ctx, page }
}

async function shot(page, name) {
  await page.screenshot({ path: `${OUT}/${tag}-${name}.png` })
  console.log('wrote', `${tag}-${name}.png`)
}

const scenes = {
  // empty desktop
  async idle() {},
  // launcher open
  async launcher(page) {
    await page.keyboard.press('Meta+Space')
    await page.waitForTimeout(700)
    await page.keyboard.type('ter')
    await page.waitForTimeout(600)
  },
  async launchpad(page) {
    await page.keyboard.press('Meta+Space')
    await page.waitForTimeout(900)
  },
  // a couple of windows open, floating
  async windows(page) {
    for (const app of ['Terminal', 'Files', 'Activity']) {
      await page.keyboard.press('Meta+k')
      await page.waitForTimeout(450)
      await page.keyboard.type(app)
      await page.waitForTimeout(550)
      await page.keyboard.press('Enter')
      await page.waitForTimeout(900)
    }
  },
  async chat(page) {
    await page.keyboard.press('Meta+Space')
    await page.waitForTimeout(400)
    await page.keyboard.press('Escape')
    const btn = page.getByRole('button', { name: /assistant|chat/i }).first()
    if (await btn.isVisible().catch(() => false)) { await btn.click(); await page.waitForTimeout(800) }
  },
}

const wantScenes = (process.env.SCENES || 'idle,launcher,windows').split(',')
const wantVps = only ? [only] : Object.keys(VIEWPORTS)
const wantThemes = (process.env.THEMES || 'dark,light').split(',')

for (const vp of wantVps) {
  for (const theme of wantThemes) {
    for (const scene of wantScenes) {
      const { ctx, page } = await boot(vp, theme)
      try {
        await scenes[scene](page)
        await page.waitForTimeout(400)
        await shot(page, `${vp}-${theme}-${scene}`)
      } catch (e) {
        console.log('scene failed', vp, theme, scene, String(e).slice(0, 200))
      }
      await ctx.close()
    }
  }
}

await browser.close()
if (errs.length) console.log('CONSOLE ERRORS:\n' + [...new Set(errs)].slice(0, 30).join('\n'))
else console.log('no console errors')
