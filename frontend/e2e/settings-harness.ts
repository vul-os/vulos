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

/** Boot the shell with a mocked box and open the Settings app. */
export async function openSettings(page: Page, theme: 'dark' | 'light' = 'dark'): Promise<void> {
  await page.addInitScript((t) => {
    try { localStorage.setItem('vulos-theme', t) } catch { /* private mode */ }
  }, theme)
  await installBackend(page)
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
    const opener = page.getByRole('button', { name: /Sections|Menu|Settings sections/i }).first()
    if (await opener.count() && await opener.isVisible()) {
      await opener.click()
      await page.waitForTimeout(250)
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
 * Elements painting past the right edge of the viewport.
 *
 * Measured against the VIEWPORT rather than a scroll container because that is
 * what the user experiences as "the page slides sideways". Elements that are
 * legitimately scrollable in their own right (overflow-x:auto) are excluded —
 * a wide table inside its own scroller is a design, not a defect.
 */
export async function overflowsAt(page: Page): Promise<Overflow[]> {
  return page.evaluate(() => {
    const vw = document.documentElement.clientWidth
    const out: Overflow[] = []
    for (const el of Array.from(document.querySelectorAll('*'))) {
      const r = el.getBoundingClientRect()
      if (r.width === 0 || r.height === 0) continue
      // Skip anything inside a deliberate horizontal scroller.
      let p: Element | null = el
      let scrollable = false
      while (p && p !== document.body) {
        const ov = getComputedStyle(p).overflowX
        if (ov === 'auto' || ov === 'scroll') { scrollable = true; break }
        p = p.parentElement
      }
      if (scrollable) continue
      if (r.right > vw + 1) {
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
      const r = el.getBoundingClientRect()
      if (r.width === 0 || r.height === 0) continue
      const cs = getComputedStyle(el)
      if (cs.visibility === 'hidden' || cs.display === 'none') continue
      if (r.width < 24 || r.height < 24) {
        out.push({
          tag: el.tagName.toLowerCase(),
          name: (el.getAttribute('aria-label') || el.textContent || '').trim().slice(0, 40),
          w: Math.round(r.width),
          h: Math.round(r.height),
        })
      }
    }
    return out
  })
}
