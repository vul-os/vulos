// Refuse to run the OS E2E suite against a different application.
//
// playwright.config.js sets `reuseExistingServer: !CI`, which is the right
// default for iteration speed — but it means ANY server already listening on
// the E2E port is adopted silently. Port 4173 is vite preview's default, so a
// preview of a sibling repo (vulos-cloud's marketing site, most easily) claims
// it first and the whole suite then runs against that site's pages.
//
// This is not hypothetical. It happened, and the damage was worse than a red
// suite: auth-contrast.e2e.ts reported 2 passed / 2 failed against the
// marketing site. The two failures were the create-account screen, which that
// site has no equivalent of. The two PASSES were the sign-in screen — they
// scanned a marketing page for text below WCAG AA, found none, and reported
// success. A contrast gate that passes while looking at the wrong application
// is exactly the failure this repository keeps finding in its own guards, and
// the port default makes it a one-command mistake for anyone.
//
// The check compares the SERVED <title> against the one in this repo's own
// index.html rather than a hardcoded string, so renaming the app updates both
// sides at once and this never becomes a stale literal to be deleted.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const titleOf = (html) => (html.match(/<title>([^<]*)<\/title>/i) || [])[1]?.trim()

export default async function assertCorrectApp() {
  const port = Number(process.env.E2E_PORT || 4173)
  const url = `http://localhost:${port}/`

  const here = dirname(fileURLToPath(import.meta.url))
  const expected = titleOf(readFileSync(join(here, '..', 'index.html'), 'utf8'))
  if (!expected) {
    throw new Error('e2e/assert-correct-app.js: no <title> in frontend/index.html, so this guard has no subject and would pass against anything')
  }

  let html
  try {
    html = await (await fetch(url)).text()
  } catch (err) {
    throw new Error(`e2e/assert-correct-app.js: could not fetch ${url} — ${err.message}`)
  }

  const served = titleOf(html)
  if (served !== expected) {
    throw new Error(
      `The E2E port is serving a DIFFERENT application.\n` +
      `  ${url}\n` +
      `    served:   ${JSON.stringify(served)}\n` +
      `    expected: ${JSON.stringify(expected)}  (from frontend/index.html)\n\n` +
      `playwright.config.js reuses an existing server on this port. Something else\n` +
      `is listening — most likely a "vite preview" of another repo, since 4173 is\n` +
      `vite's default port. Stop it, or set E2E_PORT to a free port.\n\n` +
      `Without this check the suite runs against that application and reports\n` +
      `passes for gates that never saw their subject.`,
    )
  }
}
