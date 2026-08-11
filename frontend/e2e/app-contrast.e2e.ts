import { test, expect } from '@playwright/test'
import { bootShell, launchApp, belowAA, textNodeCount } from './contrast-scan'

/**
 * Text inside the builtin app windows meets WCAG AA, in both themes.
 *
 * The shell is a small part of the UI. Everything a person actually works in is
 * an app window, and until now nothing guarded them — the contrast fixes to
 * Files, Calendar and Settings were each made with a throwaway probe and left
 * unprotected.
 *
 * What those probes found, for the record, since these tests are what keep it
 * from coming back:
 *
 *  - Files: eleven sub-AA strings, including "Empty directory" at 1.42:1, which
 *    is close enough to invisible that an empty folder read as a broken render.
 *    Its labels used text-neutral-600/700, which is low-contrast in BOTH themes —
 *    not, as it first appeared, a consequence of this theme inverting the neutral
 *    palette. That inversion is contrast-preserving: 2.31 light against 2.29
 *    dark.
 *  - Calendar: the Month/Agenda pills, "+ New event" and the today marker used
 *    the raw accent as a FILL under white text, at 3.68:1.
 *  - Settings: a section heading in raw accent at 3.46:1, and "Save changes" at
 *    3.68:1 white-on-accent.
 *
 * BUILTIN apps only. Calculator and the other `/app/<id>/` web apps are iframes
 * pointing at URLs this harness does not serve, so their window opens EMPTY —
 * a white rectangle with a title bar. Measuring one reports zero failures, which
 * is how an unmeasurable app reads exactly like a clean one. It was in the first
 * version of this list and its "pass" was worth nothing.
 *
 * Four apps rather than all of them: enough to cover the shapes (a form-dense
 * settings pane, a file list, a month grid, a sparse data list) without turning a
 * gate into a ten-minute suite.
 */

const APPS = ['Settings', 'Files', 'Calendar', 'Contacts']

for (const theme of ['dark', 'light'] as const) {
  for (const app of APPS) {
    test(`${app} meets WCAG AA on the ${theme} theme`, async ({ page }) => {
      test.setTimeout(120_000)
      await bootShell(page, theme)
      const before = await textNodeCount(page)
      await launchApp(page, app)

      // An app that failed to open reports no failures and passes, so the floor
      // is what makes this test mean anything. It is a DELTA against the shell,
      // not an absolute count: Contacts opens onto an empty list and adds only
      // nine text nodes, so an absolute floor either fails a working app or is
      // too low to catch a broken one. A window that opened but rendered nothing
      // still adds its title bar, which is why the threshold is above that.
      const after = await textNodeCount(page)
      expect(
        after - before,
        `${app} added only ${after - before} text nodes over the shell's ${before} — ` +
          'its window opened but does not appear to have rendered',
      ).toBeGreaterThan(5)

      expect(
        await belowAA(page),
        `${app} has text below WCAG AA on ${theme}, measured on composited pixels`,
      ).toEqual([])
    })
  }
}
