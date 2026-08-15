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
    expect([...new Set(failures)].sort()).toEqual([])
  })
}
