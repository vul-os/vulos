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
      // Close any open modals / splash screens
      await page.keyboard.press('Escape');
      await page.waitForTimeout(600);
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
      // Try clicking the launchpad button in the dock, or press F4
      const launchpadBtn = page.locator(
        '[data-testid="launchpad-btn"], [aria-label*="Launchpad"], [aria-label*="launchpad"], button[title*="Launchpad"]'
      ).first();
      const visible = await launchpadBtn.isVisible().catch(() => false);
      if (visible) {
        await launchpadBtn.click();
      } else {
        await page.keyboard.press('F4');
      }
      await page.waitForTimeout(700);
    },
  },
  {
    name: 'settings',
    description: 'Settings panel',
    authenticated: true,
    async capture(page) {
      // Dismiss launchpad if open
      await page.keyboard.press('Escape');
      await page.waitForTimeout(400);
      // Try clicking the settings icon in dock
      const settingsBtn = page.locator(
        '[data-testid="settings-btn"], [aria-label*="Settings"], [aria-label*="settings"], button[title*="Settings"]'
      ).first();
      const visible = await settingsBtn.isVisible().catch(() => false);
      if (visible) {
        await settingsBtn.click();
      } else {
        // Try navigating directly
        await page.goto(`${BASE_URL}#settings`, { waitUntil: 'networkidle' });
      }
      await page.waitForTimeout(700);
    },
  },
  {
    name: 'terminal',
    description: 'Terminal app open in a window',
    authenticated: true,
    async capture(page) {
      // Dismiss previous windows/panels
      await page.keyboard.press('Escape');
      await page.waitForTimeout(300);
      // Try clicking Terminal in launchpad or dock
      const termBtn = page.locator(
        '[data-testid="terminal-btn"], [aria-label*="Terminal"], [aria-label*="terminal"], button[title*="Terminal"]'
      ).first();
      const visible = await termBtn.isVisible().catch(() => false);
      if (visible) {
        await termBtn.click();
      } else {
        // Open launchpad and click Terminal
        await page.keyboard.press('F4');
        await page.waitForTimeout(500);
        const termInLaunchpad = page.locator('text=Terminal').first();
        await termInLaunchpad.click().catch(() => {});
      }
      await page.waitForTimeout(900);
    },
  },
  {
    name: 'files',
    description: 'File Manager app',
    authenticated: true,
    async capture(page) {
      await page.keyboard.press('Escape');
      await page.waitForTimeout(300);
      const filesBtn = page.locator(
        '[data-testid="files-btn"], [aria-label*="Files"], [aria-label*="File Manager"], button[title*="Files"]'
      ).first();
      const visible = await filesBtn.isVisible().catch(() => false);
      if (visible) {
        await filesBtn.click();
      } else {
        await page.keyboard.press('F4');
        await page.waitForTimeout(500);
        await page.locator('text=Files').first().click().catch(() => {});
      }
      await page.waitForTimeout(900);
    },
  },
  {
    name: 'apphub',
    description: 'App Hub (App Store)',
    authenticated: true,
    async capture(page) {
      await page.keyboard.press('Escape');
      await page.waitForTimeout(300);
      const appHubBtn = page.locator(
        '[data-testid="apphub-btn"], [aria-label*="App Hub"], [aria-label*="App Store"], button[title*="App"]'
      ).first();
      const visible = await appHubBtn.isVisible().catch(() => false);
      if (visible) {
        await appHubBtn.click();
      } else {
        await page.keyboard.press('F4');
        await page.waitForTimeout(500);
        await page.locator('text=/App Hub|App Store/i').first().click().catch(() => {});
      }
      await page.waitForTimeout(900);
    },
  },
  {
    name: 'activity',
    description: 'Activity Monitor',
    authenticated: true,
    async capture(page) {
      await page.keyboard.press('Escape');
      await page.waitForTimeout(300);
      await page.keyboard.press('F4');
      await page.waitForTimeout(500);
      await page.locator('text=/Activity|Monitor/i').first().click().catch(() => {});
      await page.waitForTimeout(900);
    },
  },
];

/**
 * Attempt to log in using email/password.
 * Returns true on success, false if the login form is not found or creds fail.
 */
async function login(page) {
  await page.goto(BASE_URL, { waitUntil: 'networkidle' });

  const emailInput = page.locator('input[type="email"], input[name="email"], input[placeholder*="email" i]').first();
  const emailVisible = await emailInput.isVisible().catch(() => false);
  if (!emailVisible) {
    // Already authenticated, or no login form found
    return true;
  }

  await emailInput.fill(DEV_EMAIL);

  const passwordInput = page.locator('input[type="password"]').first();
  const passVisible = await passwordInput.isVisible().catch(() => false);
  if (passVisible) {
    await passwordInput.fill(DEV_PASSWORD);
  }

  // Submit
  const submitBtn = page.locator(
    'button[type="submit"], button:has-text("Sign in"), button:has-text("Log in"), button:has-text("Login")'
  ).first();
  const submitVisible = await submitBtn.isVisible().catch(() => false);
  if (submitVisible) {
    await submitBtn.click();
  } else {
    await page.keyboard.press('Enter');
  }

  // Wait for navigation away from login
  await page.waitForNavigation({ timeout: 8_000 }).catch(() => {});
  await page.waitForTimeout(1_000);

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
