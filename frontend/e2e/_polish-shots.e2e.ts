// TEMP screenshot harness for the app-interior polish pass. NOT part of the
// suite — never commit this file. Captures named apps at desktop (1440x900)
// and phone (390x844), both themes, into OUT_DIR.
import { test, type Page } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'
import fs from 'node:fs'

const OUT_DIR = process.env.SHOT_OUT || '/tmp/polish-shots'
fs.mkdirSync(OUT_DIR, { recursive: true })

const VIEWPORTS: Record<string, { width: number; height: number }> = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
}
const THEMES = ['dark', 'light']

async function boot(page: Page, theme: string, overrides: Record<string, unknown> = {}) {
  await page.addInitScript((t) => {
    try {
      localStorage.setItem('vulos-theme', t)
      localStorage.setItem('vulos-ai-firstrun-done', '1')
    } catch { /* noop */ }
  }, theme)
  await installBackend(page, overrides)
  await page.goto('/')
}

async function launch(page: Page, name: string, isPhone: boolean) {
  if (isPhone) {
    await page.locator('[data-shell="mobile"]').first().waitFor({ timeout: 20_000 })
  } else {
    await page.getByTitle('Applications').first().waitFor({ timeout: 20_000 })
  }
  const input = page.getByPlaceholder(/Search apps/)
  await (async () => {
    for (let i = 0; i < 8; i++) {
      await page.keyboard.press('Meta+k')
      if (await input.isVisible().catch(() => false)) return
      await page.waitForTimeout(200)
    }
  })()
  await input.fill(name)
  await page.getByText(name, { exact: true }).first().waitFor({ timeout: 6_000 }).catch(() => {})
  await page.keyboard.press('Enter')
  await page.waitForTimeout(900)
  if (!isPhone) {
    const maxBtn = page.getByRole('button', { name: 'Maximize window' }).first()
    if (await maxBtn.isVisible().catch(() => false)) {
      await maxBtn.click().catch(() => {})
      await page.waitForTimeout(400)
    }
  }
}

interface Shot {
  file: string
  app: string
  overrides?: Record<string, unknown>
  extra?: (page: Page, isPhone: boolean) => Promise<void>
  viewports?: string[] // default both
}

const cards = json({
  contacts: [
    { uid: 'u1', name: 'Ada Lovelace', org: 'Vulos', title: 'Founding Engineer', emails: ['ada@vulos.example', 'ada.personal@gmail.com'], phones: ['+1 555 0182'], note: 'First programmer. Prefers async updates.' },
    { uid: 'u2', name: 'Grace Hopper', org: 'US Navy', title: 'Rear Admiral', emails: ['grace@navy.mil'], phones: ['+1 555 0134'] },
    { uid: 'u3', name: 'Priya Menon', org: 'Acme, Inc.', title: 'Head of Product', emails: ['priya@acme.io'], phones: ['+1 415 555 0134'] },
    { uid: 'u4', name: 'Sam Okafor', org: 'Acme, Inc.', title: 'Engineering Lead', emails: ['sam@acme.io'], phones: ['+1 415 555 0198'] },
    { uid: 'u5', name: 'Marta Costa', org: 'Lisbon Design Collective', title: 'Creative Director', emails: ['marta@lisbondesign.pt'], phones: ['+351 21 555 0198'] },
    { uid: 'u6', name: 'Nadia Rahman', org: 'Northwind Studio', title: 'Studio Manager', emails: ['nadia@northwind.studio'], phones: ['+44 20 7946 0102'] },
    { uid: 'u7', name: 'Events Team', emails: ['events@acme.io'] },
  ],
})
const unified = json({
  contacts: [
    { id: 'u1', name: 'Ada Lovelace', org: 'Vulos', emails: ['ada@vulos.example'], phones: ['+1 555 0182'], sources: ['vulos', 'phone'] },
    { id: 'u4', name: 'Sam Okafor', org: 'Acme, Inc.', emails: ['sam@acme.io'], phones: ['+1 415 555 0198'], sources: ['vulos', 'phone', 'box-sim'] },
    { id: 'x1', name: 'Thabo Mokoena', phones: ['+27 82 555 0147'], sources: ['phone'] },
  ],
})

const ASSISTANT_STATUS = json({
  tier: 'local', label: 'On your device',
  sovereignty: { tier: 'local', provider: 'ollama', model: 'llama3', allowed: true },
  tier_options: [
    { tier: 'local', label: 'On your device' },
    { tier: 'sovereign', label: 'Operator-declared endpoint' },
    { tier: 'brokered', label: 'Brokered · no-train' },
  ],
  mail_source: 'lilmail',
})
const ASSISTANT_ANSWER =
  "You're mostly clear this morning. Priya is waiting on your ack on the Q3 roadmap timeline, " +
  "and the studio invoice is due Friday — auto-pay is on, so no action needed unless you want to review it. " +
  "Design review moved to 2pm. Nothing else is on fire."

const SHOTS: Shot[] = [
  { file: 'assistant-empty', app: 'Assistant', overrides: { 'GET /api/assistant/status': ASSISTANT_STATUS } },
  {
    file: 'assistant-convo', app: 'Assistant',
    overrides: {
      'GET /api/assistant/status': ASSISTANT_STATUS,
      'POST /api/assistant/attention': json({ answer: ASSISTANT_ANSWER }),
    },
    extra: async (page) => {
      await page.getByRole('button', { name: /What needs my attention/ }).first().click().catch(() => {})
      await page.waitForTimeout(500)
    },
  },
  { file: 'contacts-list', app: 'Contacts', overrides: { 'GET /api/pim/contacts/cards': cards, 'GET /api/contacts/unified': unified } },
  {
    file: 'contacts-detail', app: 'Contacts',
    overrides: { 'GET /api/pim/contacts/cards': cards, 'GET /api/contacts/unified': unified },
    extra: async (page) => {
      await page.getByText('Ada Lovelace', { exact: true }).first().click().catch(() => {})
      await page.waitForTimeout(300)
    },
  },
  {
    file: 'contacts-editor', app: 'Contacts',
    overrides: { 'GET /api/pim/contacts/cards': cards, 'GET /api/contacts/unified': unified },
    extra: async (page) => {
      await page.getByLabel('New contact').click().catch(() => {})
      await page.waitForTimeout(300)
    },
  },
]

for (const shot of SHOTS) {
  for (const vpName of shot.viewports || Object.keys(VIEWPORTS)) {
    for (const theme of THEMES) {
      test(`${shot.file} ${vpName} ${theme}`, async ({ page }) => {
        await page.setViewportSize(VIEWPORTS[vpName])
        await boot(page, theme, shot.overrides || {})
        const isPhone = vpName === 'phone'
        await launch(page, shot.app, isPhone)
        if (shot.extra) await shot.extra(page, isPhone)
        await page.waitForTimeout(200)
        await page.screenshot({ path: `${OUT_DIR}/${shot.file}-${vpName}-${theme}.png` })
      })
    }
  }
}

// ── SECOND PASS: the rest of the built-in apps, desktop only ────────────────
const MORE = ['Activity Monitor', 'Disk Usage', 'Packages', 'Drivers', 'Drive', 'Messages']
for (const app of MORE) {
  test(`more-${app.replace(/\s+/g, '-')} desktop dark`, async ({ page }) => {
    await page.setViewportSize(VIEWPORTS.desktop)
    await boot(page, 'dark', {})
    await launch(page, app, false)
    await page.waitForTimeout(900)
    await page.screenshot({ path: `${OUT_DIR}/more-${app.replace(/\s+/g, '-')}-desktop-dark.png` })
  })
}
