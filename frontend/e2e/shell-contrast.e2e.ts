import { test, expect } from '@playwright/test'
import { bootShell, belowAA, textNodeCount } from './contrast-scan'

/**
 * Text in the desktop shell meets WCAG AA against what is actually behind it,
 * in BOTH themes.
 *
 * Both are asserted because pinning was the bug: this suite used to boot
 * whatever theme resolved by default, which was light, and the dark theme went
 * unchecked while carrying --text-faint at 2.55:1.
 *
 * No exceptions are tolerated here. Two used to be:
 *
 *  - the "alpha" watermark was `var(--accent)` at opacity 0.85 and measured
 *    2.80:1. The accent is the USER's choice, so no palette value could fix it;
 *    it now uses --accent-text, derived at runtime from whatever they picked
 *    (core/accentContrast.ts).
 *  - the menu-bar date was Tailwind's `text-neutral-500` at 4.39:1 and is now
 *    neutral-400.
 *
 * If a shortfall ever has to be tolerated again, list it by its visible text so
 * a NEW failure cannot hide behind it — and say why it is a decision rather than
 * a defect.
 *
 * Scope: the shell as it boots. App windows are covered by app-contrast.e2e.ts.
 */

for (const theme of ['dark', 'light'] as const) {
  test(`desktop shell text meets WCAG AA on the ${theme} theme`, async ({ page }) => {
    test.setTimeout(120_000)
    await bootShell(page, theme)

    const measured = await textNodeCount(page)
    expect(measured, 'the shell rendered almost no text, so this would pass vacuously').toBeGreaterThan(20)

    expect(
      await belowAA(page),
      `shell text below WCAG AA on ${theme}, measured on composited pixels`,
    ).toEqual([])
  })
}
