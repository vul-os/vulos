#!/usr/bin/env node
/**
 * Vulos screenshot capture script.
 *
 * Uses Playwright (Chromium) to capture defined routes/views at 1440x900.
 * Screenshots are saved to docs/screenshots/<name>.png.
 *
 * Usage:
 *   npm run screenshots
 *   BASE_URL=http://localhost:8080 npm run screenshots
 *
 * Prerequisites:
 *   npm install
 *   npx playwright install chromium
 */

import { chromium } from 'playwright';
import { mkdir } from 'fs/promises';
import { fileURLToPath } from 'url';
import path from 'path';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, '..');
const OUT_DIR = path.join(REPO_ROOT, 'docs', 'screenshots');

const BASE_URL = process.env.BASE_URL || 'https://localhost:8080';
const VIEWPORT = { width: 1440, height: 900 };

// Credentials for demo/dev account used in screenshots.
// Matches the seeded dev account created by `--env=local` first-boot.
const DEV_EMAIL = process.env.SCREENSHOT_EMAIL || 'admin@localhost';
const DEV_PASSWORD = process.env.SCREENSHOT_PASSWORD || 'password';

/**
 * Routes to capture.
 * Each entry: { name, description, capture(page) }
 *
 * capture() receives a Playwright Page already navigated to BASE_URL and
 * authenticated (where applicable). It should navigate/interact to reach the
 * desired state, then resolve — the caller takes the screenshot.
 */
const ROUTES = [
  {
    name: 'login',
    description: 'Login screen (unauthenticated)',
    authenticated: false,
    async capture(page) {
      await page.goto(BASE_URL, { waitUntil: 'networkidle' });
      // Wait for the login form to appear
      await page.waitForSelector('input[type="email"], input[type="text"], form', {
        timeout: 10_000,
      }).catch(() => {}); // best-effort
    },
  },
  {
    name: 'hero',
    description: 'Desktop shell (authenticated, all windows closed)',
    authenticated: true,
    async capture(page) {
      // Suppress + dismiss the AI first-run onboarding overlay before anything else.
      // It appears 800ms after DesktopCanvas mounts, so we set localStorage to
      // suppress it and click it away if it has already appeared.
      await page.evaluate(() => {
        try { localStorage.setItem('vulos-ai-firstrun-done', '1'); } catch { /* noop */ }
      }).catch(() => {});
      // Wait for the dialog's 800ms timer
      await page.waitForTimeout(1_000);
      const aiDialog = page.locator('[aria-label="Introducing the AI assistant"]').first();
      if (await aiDialog.isVisible().catch(() => false)) {
        const box = await aiDialog.boundingBox().catch(() => null);
        if (box) {
          await page.mouse.click(box.x + 10, box.y + 10);
        } else {
          await aiDialog.click({ force: true }).catch(() => {});
        }
        await page.waitForTimeout(500);
      }
      // Wait for the dock or shell root to appear
      await page.waitForSelector(
        '[data-testid="dock"], [class*="Dock"], [class*="dock"], [class*="shell"], [class*="Desktop"]',
        { timeout: 8_000 }
      ).catch(() => {});
      await page.waitForTimeout(400);
    },
  },
  {
    name: 'launchpad',
    description: 'Launchpad — full-screen app grid',
    authenticated: true,
    async capture(page) {
      // Vulos launchpad button sits in the menu bar with title="Applications"
      // Try that title first, then fall back to generic aria-label / F4.
      const launchpadBtn = page.locator(
        'button[title="Applications"], [data-testid="launchpad-btn"], ' +
        '[aria-label*="Launchpad"], [aria-label*="launchpad"], button[title*="Launchpad"]'
      ).first();
      const visible = await launchpadBtn.isVisible().catch(() => false);
      if (visible) {
        await launchpadBtn.click();
      } else {
        await page.keyboard.press('F4');
      }
      await page.waitForTimeout(900);
    },
  },
  {
    name: 'settings',
    description: 'Settings panel (opened via Launchpad → Settings app)',
    authenticated: true,
    async capture(page) {
      // Close anything open
      await page.keyboard.press('Escape');
      await page.waitForTimeout(400);
      // Open launchpad
      const launchpadBtn = page.locator('button[title="Applications"], [data-testid="launchpad-btn"]').first();
      const lpVisible = await launchpadBtn.isVisible().catch(() => false);
      if (lpVisible) {
        await launchpadBtn.click();
      } else {
        await page.keyboard.press('F4');
      }
      await page.waitForTimeout(600);
      // Click Settings (app id: "persona", displayed name: "Settings")
      const settingsApp = page.locator(
        '[aria-label="Open Settings"], button:has-text("Settings"), [data-appid="persona"]'
      ).first();
      const settingsVisible = await settingsApp.isVisible().catch(() => false);
      if (settingsVisible) {
        await settingsApp.click();
      } else {
        // fallback: search for "Settings" in launchpad search
        const searchBox = page.locator('input[placeholder*="Search"], input[type="search"]').first();
        if (await searchBox.isVisible().catch(() => false)) {
          await searchBox.fill('Settings');
          await page.waitForTimeout(400);
          await page.locator('button:has-text("Settings")').first().click().catch(() => {});
        }
      }
      await page.waitForTimeout(900);
    },
  },
  {
    name: 'settings-storage',
    description: 'Settings — Storage panel',
    authenticated: true,
    async capture(page) {
      // Settings window should already be open from previous route; click Storage in sidebar
      // If not, re-open it.
      await page.keyboard.press('Escape');
      await page.waitForTimeout(300);
      const launchpadBtn = page.locator('button[title="Applications"], [data-testid="launchpad-btn"]').first();
      const lpVisible = await launchpadBtn.isVisible().catch(() => false);
      if (lpVisible) {
        await launchpadBtn.click();
        await page.waitForTimeout(500);
        await page.locator('[aria-label="Open Settings"], button:has-text("Settings")').first().click().catch(() => {});
        await page.waitForTimeout(700);
      }
      // Click "Storage" in the Settings sidebar
      await page.locator('button:has-text("Storage")').first().click().catch(() => {});
      await page.waitForTimeout(700);
    },
  },
  {
    name: 'terminal',
    description: 'Terminal app open in a window',
    authenticated: true,
    async capture(page) {
      // Close any open windows by clicking window close buttons
      const closeButtons = page.locator('[title="Close"], button[aria-label="Close window"]');
      const count = await closeButtons.count().catch(() => 0);
      for (let i = 0; i < count; i++) {
        await closeButtons.first().click().catch(() => {});
        await page.waitForTimeout(200);
      }
      await page.keyboard.press('Escape');
      await page.waitForTimeout(300);
      // Open launchpad, then click Terminal
      const launchpadBtn = page.locator('button[title="Applications"], [data-testid="launchpad-btn"]').first();
      if (await launchpadBtn.isVisible().catch(() => false)) {
        await launchpadBtn.click();
      } else {
        await page.keyboard.press('F4');
      }
      await page.waitForTimeout(500);
      const termBtn = page.locator('[aria-label="Open Terminal"]').first();
      if (await termBtn.isVisible().catch(() => false)) {
        await termBtn.click();
      }
      await page.waitForTimeout(1_500);
    },
  },
  {
    name: 'files',
    description: 'File Manager app',
    authenticated: true,
    async capture(page) {
      // Close any open windows
      const closeButtons = page.locator('[title="Close"], button[aria-label="Close window"]');
      const count = await closeButtons.count().catch(() => 0);
      for (let i = 0; i < count; i++) {
        await closeButtons.first().click().catch(() => {});
        await page.waitForTimeout(200);
      }
      await page.keyboard.press('Escape');
      await page.waitForTimeout(300);
      const launchpadBtn = page.locator('button[title="Applications"], [data-testid="launchpad-btn"]').first();
      if (await launchpadBtn.isVisible().catch(() => false)) {
        await launchpadBtn.click();
      } else {
        await page.keyboard.press('F4');
      }
      await page.waitForTimeout(500);
      const filesBtn = page.locator('[aria-label="Open File Explorer"]').first();
      if (await filesBtn.isVisible().catch(() => false)) {
        await filesBtn.click();
      }
      await page.waitForTimeout(1_500);
    },
  },
  {
    name: 'apphub',
    description: 'App Hub (App Store)',
    authenticated: true,
    async capture(page) {
      // Close any open windows
      const closeButtons = page.locator('[title="Close"], button[aria-label="Close window"]');
      const count = await closeButtons.count().catch(() => 0);
      for (let i = 0; i < count; i++) {
        await closeButtons.first().click().catch(() => {});
        await page.waitForTimeout(200);
      }
      await page.keyboard.press('Escape');
      await page.waitForTimeout(300);
      const launchpadBtn = page.locator('button[title="Applications"], [data-testid="launchpad-btn"]').first();
      if (await launchpadBtn.isVisible().catch(() => false)) {
        await launchpadBtn.click();
      } else {
        await page.keyboard.press('F4');
      }
      await page.waitForTimeout(500);
      const appHubBtn = page.locator('[aria-label="Open App Hub"]').first();
      if (await appHubBtn.isVisible().catch(() => false)) {
        await appHubBtn.click();
      }
      await page.waitForTimeout(1_500);
    },
  },
  {
    name: 'activity',
    description: 'Activity Monitor',
    authenticated: true,
    async capture(page) {
      // Close any open windows
      const closeButtons = page.locator('[title="Close"], button[aria-label="Close window"]');
      const count = await closeButtons.count().catch(() => 0);
      for (let i = 0; i < count; i++) {
        await closeButtons.first().click().catch(() => {});
        await page.waitForTimeout(200);
      }
      await page.keyboard.press('Escape');
      await page.waitForTimeout(300);
      const launchpadBtn = page.locator('button[title="Applications"], [data-testid="launchpad-btn"]').first();
      if (await launchpadBtn.isVisible().catch(() => false)) {
        await launchpadBtn.click();
      } else {
        await page.keyboard.press('F4');
      }
      await page.waitForTimeout(500);
      const actBtn = page.locator('[aria-label="Open Activity Monitor"]').first();
      if (await actBtn.isVisible().catch(() => false)) {
        await actBtn.click();
      }
      await page.waitForTimeout(1_500);
    },
  },
];

/**
 * Attempt to log in using the Vulos local auth form (username + password).
 *
 * The local login form uses type="text" with placeholder "Username" — NOT
 * type="email".  After a user exists the passkey-first layout shows first;
 * we click "Use password instead" to get to the password form.
 *
 * Returns true on success (or if already authenticated), false on failure.
 */
async function login(page) {
  await page.goto(BASE_URL, { waitUntil: 'networkidle' });
  await page.waitForTimeout(800);

  // ── detect whether we're already authenticated ────────────────────────────
  // The shell root / dock is present when authenticated.
  const shellPresent = await page.locator(
    '[data-testid="dock"], [class*="Dock"], [class*="shell"], [class*="Desktop"]'
  ).first().isVisible().catch(() => false);
  if (shellPresent) return true;

  // ── handle passkey-first layout: click "Use password instead" ─────────────
  const usePasswordBtn = page.locator('button:has-text("Use password instead")').first();
  if (await usePasswordBtn.isVisible().catch(() => false)) {
    await usePasswordBtn.click();
    await page.waitForTimeout(400);
  }

  // ── fill in username (type="text", placeholder="Username", autocomplete="username") ─
  const usernameInput = page.locator(
    'input[autocomplete="username"], input[placeholder*="Username" i], input[placeholder*="username" i]'
  ).first();
  const usernameVisible = await usernameInput.isVisible().catch(() => false);

  if (!usernameVisible) {
    // Fall back: maybe cloud/email form — try email input as before
    const emailInput = page.locator('input[type="email"], input[name="email"], input[placeholder*="email" i]').first();
    const emailVisible = await emailInput.isVisible().catch(() => false);
    if (!emailVisible) {
      console.warn('    WARN: no login form detected — page may already be authenticated or form changed');
      return true;
    }
    await emailInput.fill(DEV_EMAIL);
  } else {
    await usernameInput.fill(DEV_EMAIL);
  }

  // ── fill in password ───────────────────────────────────────────────────────
  const passwordInput = page.locator('input[type="password"]').first();
  const passVisible = await passwordInput.isVisible().catch(() => false);
  if (passVisible) {
    await passwordInput.fill(DEV_PASSWORD);
  }

  // ── submit ─────────────────────────────────────────────────────────────────
  const submitBtn = page.locator(
    'button[type="submit"], button:has-text("Sign In"), button:has-text("Sign in"), ' +
    'button:has-text("Log in"), button:has-text("Login"), button:has-text("Create Account")'
  ).first();
  const submitVisible = await submitBtn.isVisible().catch(() => false);
  if (submitVisible) {
    await submitBtn.click();
  } else {
    await page.keyboard.press('Enter');
  }

  // ── wait for shell to appear ───────────────────────────────────────────────
  await page.waitForSelector(
    '[data-testid="dock"], [class*="Dock"], [class*="shell"], [class*="Desktop"]',
    { timeout: 10_000 }
  ).catch(() => {});
  await page.waitForTimeout(1_200);

  // ── suppress the AI first-run onboarding overlay ──────────────────────────
  // It appears 800ms after the desktop renders on first visit. Setting the
  // localStorage flag before it fires prevents it from showing at all, and
  // clicking it dismisses it if we race.
  await page.evaluate(() => {
    try { localStorage.setItem('vulos-ai-firstrun-done', '1'); } catch { /* noop */ }
  }).catch(() => {});
  // Dismiss the dialog if it already appeared
  const aiDialog = page.locator('[aria-label="Introducing the AI assistant"]').first();
  if (await aiDialog.isVisible().catch(() => false)) {
    await page.keyboard.press('Escape');
    await aiDialog.click().catch(() => {});
    await page.waitForTimeout(400);
  }

  return true;
}

async function main() {
  await mkdir(OUT_DIR, { recursive: true });

  console.log(`Capturing screenshots from ${BASE_URL}`);
  console.log(`Output directory: ${OUT_DIR}`);
  console.log('');

  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: VIEWPORT,
    deviceScaleFactor: 1,
    // Accept self-signed certs for local dev
    ignoreHTTPSErrors: true,
  });

  // Authenticated session reused for auth routes
  let authenticatedPage = null;

  const results = [];

  /**
   * Wake the screen / dismiss screensaver and any blocking modals before a capture.
   * - Jogs mouse (prevents energy service dimming)
   * - Dismisses the AI first-run onboarding overlay (uses localStorage flag)
   * - Clicks away screensaver clock overlay if present
   * - Calls /api/energy/wake
   */
  async function wakeScreen(page) {
    // Jog the mouse — prevents the energy service from dimming
    await page.mouse.move(700, 450);
    await page.waitForTimeout(100);
    await page.mouse.move(710, 440);

    // Suppress AI first-run overlay via localStorage + dismiss if already shown
    await page.evaluate(() => {
      try { localStorage.setItem('vulos-ai-firstrun-done', '1'); } catch { /* noop */ }
    }).catch(() => {});

    // Wait briefly to let React re-render after localStorage change
    await page.waitForTimeout(300);

    // Click away the AI first-run dialog if it's visible.
    // The outer backdrop has onClick={handleDismiss}; click its center to dismiss.
    const aiDialog = page.locator('[aria-label="Introducing the AI assistant"]').first();
    if (await aiDialog.isVisible().catch(() => false)) {
      // Click the top-left corner of the backdrop (outside the inner card) to trigger dismiss
      const box = await aiDialog.boundingBox().catch(() => null);
      if (box) {
        await page.mouse.click(box.x + 10, box.y + 10);
      } else {
        await aiDialog.click({ force: true }).catch(() => {});
      }
      await page.waitForTimeout(500);
      // Verify it's gone; if not, try keyboard
      if (await aiDialog.isVisible().catch(() => false)) {
        await page.keyboard.press('Escape');
        await page.waitForTimeout(300);
      }
    }

    // If the screensaver clock is visible, click to dismiss
    const hasClock = await page.locator('text=/\\d{1,2}:\\d{2}\\s*(AM|PM)/').first().isVisible().catch(() => false);
    if (hasClock) {
      await page.mouse.click(700, 450);
      await page.waitForTimeout(600);
    }

    // Hit the wake endpoint (best-effort; may 401 if not auth-wired)
    await page.evaluate(async (url) => {
      try { await fetch(url + '/api/energy/wake', { method: 'POST', credentials: 'include' }); } catch { /* noop */ }
    }, BASE_URL).catch(() => {});
    await page.waitForTimeout(200);
  }

  for (const route of ROUTES) {
    const { name, description, authenticated, capture } = route;
    console.log(`  [${name}] ${description}`);

    try {
      let page;
      if (authenticated) {
        if (!authenticatedPage) {
          authenticatedPage = await context.newPage();
          const ok = await login(authenticatedPage);
          if (!ok) {
            console.warn(`    WARN: login failed — capturing ${name} unauthenticated`);
          }
        }
        page = authenticatedPage;
      } else {
        page = await context.newPage();
      }

      if (authenticated) {
        await wakeScreen(page);
      }
      await capture(page);
      const outPath = path.join(OUT_DIR, `${name}.png`);
      await page.screenshot({ path: outPath, fullPage: false });
      console.log(`    ✓ saved ${path.relative(REPO_ROOT, outPath)}`);
      results.push({ name, status: 'ok', path: outPath });

      // Close unauthenticated pages; keep the authenticated one
      if (!authenticated) {
        await page.close();
      }
    } catch (err) {
      console.error(`    ✗ failed: ${err.message}`);
      results.push({ name, status: 'failed', error: err.message });
    }
  }

  if (authenticatedPage) await authenticatedPage.close();
  await context.close();
  await browser.close();

  console.log('');
  console.log('Summary:');
  const ok = results.filter((r) => r.status === 'ok');
  const failed = results.filter((r) => r.status === 'failed');
  console.log(`  ${ok.length} captured, ${failed.length} failed`);
  if (failed.length > 0) {
    console.log('  Failed:');
    for (const r of failed) {
      console.log(`    - ${r.name}: ${r.error}`);
    }
    console.log('');
    console.log('  See docs/screenshots/README.md for which screenshots need a live instance.');
  }

  process.exit(failed.length > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error('Fatal:', err);
  process.exit(1);
});
