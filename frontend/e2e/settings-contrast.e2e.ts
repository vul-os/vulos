/**
 * settings-contrast.e2e.ts — every rendered string in Settings must meet AA, in
 * BOTH themes.
 *
 * Measured on composited pixels via contrast-scan.ts, not on tokens: the
 * failure is never a value in a file, it is a pair, and `opacity` is half of
 * it. A gate that reads tokens is blind to the `opacity: 0.85` sitting beside a
 * perfectly good `color`. Tonight a wizard shipped text at 1.48:1 and an
 * Activity Monitor button at 1.18:1 with every token nominally correct.
 *
 * Both themes are pinned because a single-theme sweep has already been wrong in
 * both directions on this project: the shell suite booted light and let
 * --text-faint ship at 2.55:1 in dark, and the marketing site's dark-only sweep
 * passed while light held 576 sub-AA strings.
 */
import { test, expect } from '@playwright/test'
import { belowAA, textNodeCount } from './contrast-scan.js'
import { openSettings, gotoSection, SECTIONS } from './settings-harness.js'

/**
 * Light-theme status COLOURS that are sub-AA as text, declared as debt because
 * their cause is a shared token, not a Settings choice.
 *
 * src/index.css's light palette sets --status-danger #dc2626, --status-warning
 * #d97706 and --status-success #16a34a — Tailwind's 600-weight red/amber/green,
 * all of which land near 3:1 as normal-weight text on these surfaces. They are
 * used as text across the whole OS ("Ready", "Set", "Pairing info
 * unavailable"), so repairing them per-usage inside Settings would be both
 * wrong and useless: it would leave every other app failing. index.css belongs
 * to the customization pass; this list is the report, in executable form.
 *
 * Two rules keep it from becoming a place to hide a real regression, the same
 * two that guard apiContract.test.ts's OPTIONAL_CAPABILITIES:
 *
 *   1. Only these exact colours are forgiven. A sub-AA string in any OTHER
 *      colour still fails, which is what catches a new Settings defect.
 *   2. Each declared colour must STILL BE FAILING. The moment the token is
 *      darkened, its entry is stale and this suite fails until the entry is
 *      deleted — so the list cannot outlive the defect it documents.
 */
const LIGHT_TOKEN_DEBT: Record<string, string> = {
  'rgb(22, 163, 74)': '--status-success #16a34a (light) — green-600 as text, ~3.1:1',
  'rgb(217, 119, 6)': '--status-warning #d97706 (light) — amber-600 as text, ~3.2:1',
  'rgb(220, 38, 38)': '--status-danger #dc2626 (light) — red-600 as text, ~3.6:1',
}

for (const theme of ['dark', 'light'] as const) {
  test(`no sub-AA text anywhere in Settings (${theme})`, async ({ page }) => {
    test.setTimeout(180_000)
    await page.setViewportSize({ width: 1440, height: 900 })
    await openSettings(page, theme)

    const failures: string[] = []
    let visited = 0
    let measured = 0
    for (const { label } of SECTIONS) {
      if (!(await gotoSection(page, label))) continue
      visited++
      measured += await textNodeCount(page)
      for (const f of await belowAA(page)) failures.push(`${label} · ${f}`)
    }

    // Two counting guards. A sweep that navigated nowhere, or one that measured
    // a blank window, would find zero sub-AA strings and read as a clean pass —
    // which is this repo's single most common defect shape.
    expect(visited, 'walked too few sections to have measured anything').toBeGreaterThan(8)
    expect(measured, 'measured too little text: the panels did not render').toBeGreaterThan(200)

    const debt = theme === 'light' ? Object.keys(LIGHT_TOKEN_DEBT) : []
    const unexpected = [...new Set(failures)].filter(f => !debt.some(c => f.includes(c)))
    expect(unexpected.sort()).toEqual([])

    // Rule 2: a declared colour that no longer fails is stale. Without this the
    // list would quietly outlive the token fix and start forgiving a colour
    // that had become fine — which is how an exception list turns into a hole.
    const stale = debt.filter(c => !failures.some(f => f.includes(c)))
    expect(
      stale.map(c => `${c} — ${LIGHT_TOKEN_DEBT[c]}`).sort(),
      'these light-theme tokens now pass AA: delete their LIGHT_TOKEN_DEBT entries',
    ).toEqual([])
  })
}
