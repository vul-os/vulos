import { chromium } from 'playwright';

const OUT = '/private/tmp/claude-501/-Users-pc-code-vulos/294aba1f-b14c-4e0c-b9d2-1ad368389ed9/scratchpad/shots';
const browser = await chromium.launch();

for (const theme of ['dark', 'light']) {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1200 } });
  const errors = [];
  page.on('pageerror', e => errors.push(String(e)));
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  await page.goto('http://localhost:5377/icon-lab.html', { waitUntil: 'networkidle', timeout: 60000 });
  if (theme === 'light') {
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
  } else {
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
  }
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${OUT}/page-${theme}.png`, fullPage: true });

  // Per-section shots for close reading.
  const sections = await page.$$eval('section[data-shot]', els => els.map(e => e.getAttribute('data-shot')));
  for (const name of sections) {
    const el = await page.$(`section[data-shot="${name}"]`);
    if (el) await el.screenshot({ path: `${OUT}/${name}-${theme}.png` });
  }

  // Zoomed crops of specific cells in the full-catalog section for close review.
  const targets = ['home', 'lilmail', 'llmux', 'gitstate', 'kerf', 'diwan', 'envoir', 'browser', 'browser-stream', 'chrome', 'phone', 'vulos-phone', 'mail', 'lilmail', 'calculator'];
  const full = await page.$('section[data-shot="full"]');
  if (full) {
    for (const id of targets) {
      const label = await full.$(`span:text-is("${id}")`);
      if (!label) continue;
      const cell = await label.evaluateHandle(el => el.parentElement);
      await cell.asElement().screenshot({ path: `${OUT}/zoom-${id}-${theme}.png` });
    }
  }

  console.log(theme, 'ERRORS:', JSON.stringify(errors));
  await page.close();
}
await browser.close();
