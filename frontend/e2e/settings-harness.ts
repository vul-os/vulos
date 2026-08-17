/**
 * settings-harness.ts — shared driving + measurement for the Settings audit.
 *
 * Settings is the largest surface in the OS (30 sections, ~3.6k lines) and had
 * never been walked at a phone width. The measurements here are the ones that
 * repeatedly found defects a green unit suite could not: horizontal overflow at
 * 320px, sub-24px touch targets, and content rendered outside its own window.
 */
import { expect, type Page } from '@playwright/test'
import { installBackend } from './mock-backend.js'

/**
 * Every section id/label pair in the nav, owner-visible. Kept as data so a
 * sweep walks the WHOLE surface rather than the two or three panels a
 * hand-written test would remember to visit.
 */
export const SECTIONS: { id: string; label: string }[] = [
  { id: 'ai', label: 'AI Assistant' },
  { id: 'models', label: 'AI Models' },
  { id: 'aiapps', label: 'AI Apps' },
  { id: 'appearance', label: 'Appearance' },
  { id: 'notifications', label: 'Notifications' },
  { id: 'wifi', label: 'WiFi' },
  { id: 'bluetooth', label: 'Bluetooth' },
  { id: 'audio', label: 'Sound' },
  { id: 'display', label: 'Display' },
  { id: 'energy', label: 'Battery & Energy' },
  { id: 'location', label: 'Location' },
  { id: 'vault', label: 'Backup & Sync' },
  { id: 'recall', label: 'Search & Index' },
  { id: 'storage', label: 'Storage' },
  { id: 'storagemode', label: 'Storage Mode' },
  { id: 'connmode', label: 'Connection Mode' },
  { id: 'network', label: 'Remote Access' },
  { id: 'lanpairing', label: 'Native Pairing' },
  // Browser Trust shipped alongside Native Pairing and was left out of this
  // list, so the responsive sweep and the contrast walk both reported the whole
  // surface clean while never once rendering it. A sweep is only as wide as its
  // section table.
  { id: 'browsertrust', label: 'Browser Trust' },
  { id: 'domain', label: 'Custom Domain' },
  { id: 'relay', label: 'Relay & Reachability' },
  { id: 'cdn', label: 'CDN' },
  { id: 'turnSettings', label: 'TURN / WebRTC' },
  { id: 'webhooks', label: 'Webhooks' },
  { id: 'developer', label: 'Developer' },
  { id: 'users', label: 'Users & Profiles' },
  { id: 'pin', label: 'Device PIN' },
  { id: 'fingerprint', label: 'Fingerprint' },
  { id: 'account', label: 'Account' },
  { id: 'offlinedata', label: 'Offline Data' },
  { id: 'dataexport', label: 'Export My Data' },
  { id: 'security', label: 'Sign-in security' },
  { id: 'osupdate', label: 'OS Update' },
  { id: 'boxhealth', label: 'Box Health' },
  { id: 'about', label: 'About' },
]

/**
 * Boot the shell with a mocked box and open the Settings app.
 *
 * `overrides` is passed straight through to installBackend, keyed by
 * "METHOD /path". It exists so a spec can drive what the BOX SAYS — a 500, a
 * body missing a field, a refused write — which is the only way to test what
 * Settings claims when it has not been told. Optional and trailing, so the
 * existing callers are unaffected.
 */
export async function openSettings(
  page: Page,
  theme: 'dark' | 'light' = 'dark',
  overrides: Record<string, unknown> = {},
): Promise<void> {
  await page.addInitScript((t) => {
    try { localStorage.setItem('vulos-theme', t) } catch { /* private mode */ }
  }, theme)
  await installBackend(page, overrides)
  await page.goto('/')
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 20_000 })
  await input.fill('settings')
  await expect(page.getByText('Settings').first()).toBeVisible()
  await page.keyboard.press('Enter')
  await page.evaluate((t) => { document.documentElement.setAttribute('data-theme', t) }, theme)
  await page.waitForTimeout(1200)
}

/**
 * Navigate to a section. Below the `sm` breakpoint the rail is replaced by a
 * drawer, so the nav button is only reachable after opening it — a sweep that
 * skipped that step would silently measure the SAME panel at every mobile
 * width and report the whole surface clean.
 */
export async function gotoSection(page: Page, label: string): Promise<boolean> {
  const width = page.viewportSize()?.width ?? 1440
  if (width < 640) {
    // Exact label, and only when the drawer is actually shut. A loose
    // /sections/i regex also matched "CLOSE settings sections" and the drawer's
    // own nav landmark, so the sweep clicked the wrong thing and then fought
    // the open overlay for the rest of the run.
    const opener = page.getByRole('button', { name: 'Open settings sections' })
    if (await opener.count() && (await opener.getAttribute('aria-expanded')) !== 'true') {
      await opener.click()
      await page.waitForTimeout(300)
    }
  }
  const nav = page.getByRole('navigation', { name: 'Settings sections' })
  const btn = nav.getByRole('button', { name: label, exact: true }).first()
  if (!(await btn.count())) return false
  await btn.click({ timeout: 5000 }).catch(() => {})
  await page.waitForTimeout(500)
  return true
}

export interface Overflow { tag: string; cls: string; text: string; right: number; vw: number }

/**
 * Settings content painting past the right edge of its OWN container.
 *
 * Deliberately measured against the Settings root rather than the viewport.
 * Measuring the viewport conflated two unrelated defects: content escaping its
 * panel (a Settings bug) and the shell opening a window wider than the screen
 * (a window-manager bug). The second is real — at 768px the Settings window
 * runs to x=920 and slices the "Scan Networks" button in half — but it is not
 * Settings' to fix, and it is not even Settings-specific: the shell's default
 * window is 720 wide at x=60, so every windowed app overruns an iPad portrait.
 * See ShellProvider's OPEN_WINDOW, which clamps neither size nor position.
 *
 * Scoping here keeps this gate honest about the surface it owns; the window
 * defect is reported separately rather than silently absorbed.
 *
 * Elements inside a deliberate horizontal scroller are excluded — a wide table
 * in its own overflow-x:auto container is a design, not a defect.
 */
export async function overflowsAt(page: Page): Promise<Overflow[]> {
  return page.evaluate(() => {
    // The Settings root: the @container/win element the whole app lives in.
    const root = document.querySelector('[class*="@container/win"]')
      ?? document.querySelector('nav[aria-label="Settings sections"]')?.parentElement
    if (!root) return []
    const rootRight = root.getBoundingClientRect().right
    const vw = Math.round(rootRight)
    const out: Overflow[] = []
    for (const el of Array.from(root.querySelectorAll('*'))) {
      const r = el.getBoundingClientRect()
      if (r.width === 0 || r.height === 0) continue
      let p: Element | null = el
      let scrollable = false
      while (p && p !== root) {
        const ov = getComputedStyle(p).overflowX
        if (ov === 'auto' || ov === 'scroll') { scrollable = true; break }
        p = p.parentElement
      }
      if (scrollable) continue
      if (r.right > rootRight + 1) {
        out.push({
          tag: el.tagName.toLowerCase(),
          cls: (el.getAttribute('class') || '').slice(0, 80),
          text: (el.textContent || '').trim().slice(0, 40),
          right: Math.round(r.right),
          vw,
        })
      }
    }
    return out
  })
}

export interface SmallTarget { tag: string; name: string; w: number; h: number }

/**
 * Interactive elements smaller than WCAG 2.2 AA's 24x24 CSS-px minimum.
 *
 * Counts the element's own box. A control can also be made big enough by
 * padding on a wrapper, so this reports candidates rather than certainties —
 * but a 12x12 window control has no such excuse, and that is exactly what a
 * parallel agent found on tablets tonight.
 */
export async function smallTargets(page: Page): Promise<SmallTarget[]> {
  return page.evaluate(() => {
    const sel = 'button, a[href], input:not([type=hidden]), select, textarea, [role=button], [role=switch]'
    const out: SmallTarget[] = []
    for (const el of Array.from(document.querySelectorAll(sel))) {
      const cs = getComputedStyle(el)
      if (cs.visibility === 'hidden' || cs.display === 'none') continue

      // Measure the EFFECTIVE activation area, not the control's own box. A
      // native radio paints at ~13x13 in every browser, but Settings wraps each
      // one in a <label htmlFor> with px-4 py-3 and cursor-pointer, so the
      // whole row activates it and the real target is comfortably large.
      // Measuring the input alone reported the Connection Mode and Storage
      // radios as 13x13 failures when nothing was wrong with them; WCAG 2.2
      // sizes the activation area, which for a labelled control is the label.
      // Only a WRAPPING label counts. A detached `label[for=…]` sitting above a
      // field (Settings' <Field> renders one) is not the control's hit area,
      // and resolving to it measured those 16px caption boxes instead of the
      // inputs — swapping one wrong answer for another.
      const target: Element = el.closest('label') ?? el

      const r = target.getBoundingClientRect()
      if (r.width === 0 || r.height === 0) continue
      if (r.width < 24 || r.height < 24) {
        out.push({
          tag: el.tagName.toLowerCase(),
          name: (el.getAttribute('aria-label') || target.textContent || el.textContent || '').trim().slice(0, 40),
          w: Math.round(r.width),
          h: Math.round(r.height),
        })
      }
    }
    return out
  })
}
