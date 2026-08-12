import { test, expect } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'
import { belowAA, textNodeCount } from './contrast-scan.js'

/**
 * The screens shown BEFORE sign-in must meet WCAG AA, on both themes.
 *
 * Nothing covered them. shell-contrast.e2e.ts boots an authenticated shell and
 * app-contrast.e2e.ts opens app windows — both start after the user is already
 * in. The sign-in and create-account screens are the first thing anyone ever
 * sees, and they were scanned by no gate at all.
 *
 * What that cost, measured on a real boot frame captured from the installed
 * image rather than from a mock: the "Vulos OS" wordmark at the foot of the
 * create-account screen rendered #e5e5e5 on #fefeff — 1.26:1, where AA asks for
 * 4.5:1. The class on it said text-neutral-800 (#262626) and the pixels said
 * neutral-200, so the class was not applying and the product's own name was
 * invisible. The h1 two hundred pixels above it measured 17.96:1, which is why
 * the screen looked fine at a glance.
 *
 * Both AuthGate branches are covered, because they render different content and
 * a defect in one says nothing about the other.
 */

const BRANCHES = [
  {
    name: 'create-account (first boot)',
    backend: {
      'GET /api/auth/me': json({}, 401),
      'GET /api/auth/status': json({ has_users: false }),
      'GET /api/setup/status': json({ setup_complete: true }),
    },
    expect: 'Create your account',
  },
  {
    name: 'sign-in (user exists)',
    backend: {
      'GET /api/auth/me': json({}, 401),
      'GET /api/auth/status': json({ has_users: true }),
    },
    expect: 'Sign in',
  },
] as const

for (const theme of ['dark', 'light'] as const) {
  for (const branch of BRANCHES) {
    test(`${branch.name} meets WCAG AA on the ${theme} theme`, async ({ page }) => {
      test.setTimeout(90_000)

      await page.addInitScript((t) => {
        try {
          // 'vulos-theme', the key ThemeProvider actually reads (STORAGE_KEYS.theme).
          // An invented key leaves the app on its default and the run measures
          // one theme twice — which the data-theme assertion below caught.
          localStorage.setItem('vulos-theme', t)
        } catch {
          /* storage blocked — the attribute below is what actually decides */
        }
        document.documentElement.setAttribute('data-theme', t)
      }, theme)

      // Freeze the entrance animation. Both auth screens fade in over ~0.5s, and
      // a scan that lands mid-fade measures partly-transparent text against a
      // partly-drawn background — a number that belongs to no state the user
      // ever sees, and that changes run to run. The first screenshot from this
      // spec caught it: the whole screen was washed out.
      await page.emulateMedia({ reducedMotion: 'reduce' })

      await installBackend(page, branch.backend as Record<string, unknown>)
      await page.goto('/')
      await expect(page.getByText(branch.expect).first()).toBeVisible()
      // The theme attribute has to survive hydration, or this measures the
      // default theme twice and reports agreement it never checked.
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme)

      // Wait for every animation to finish before measuring, belt-and-braces
      // with reducedMotion above. A scan that lands mid-fade reads
      // partly-transparent text over a partly-drawn background: a number
      // belonging to no state the user ever sees. Left to timing alone this
      // spec flipped between runs — two green, then a different two red — and a
      // contrast gate that changes its mind is worse than none.
      await page.waitForFunction(
        () => document.getAnimations().every((a) => a.playState === 'finished'),
        null,
        { timeout: 10_000 },
      )

      // The sign-in branch is deliberately spare: heading, two field labels, the
      // button, and the wordmark — five nodes, counted rather than guessed after
      // a floor of >5 failed on the real screen. This is a vacuity guard, so it
      // sits just under the real count and not at a round number.
      const measured = await textNodeCount(page)
      expect(
        measured,
        'the auth screen rendered almost no text, so this would pass vacuously',
      ).toBeGreaterThan(4)

      expect(
        await belowAA(page),
        `${branch.name} text below WCAG AA on ${theme}, measured on composited pixels`,
      ).toEqual([])
    })
  }
}
