import { expect, test } from '@playwright/test';

const exe = process.env.PLAYWRIGHT_CHROMIUM;
test.use(exe ? { launchOptions: { executablePath: exe } } : {});

test('overview dump', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
  await expect(page.getByText('Recent scaling')).toBeVisible();
  await page.waitForTimeout(1500);
  const svgs = await page.locator('svg[role="img"]').evaluateAll((els) =>
    els.map((e) => e.getAttribute('aria-label')),
  );
  console.log('SPARKLINES:', JSON.stringify(svgs, null, 1));
  const tiles = await page.locator('.tile').evaluateAll((els) => els.map((e) => (e as HTMLElement).innerText));
  console.log('TILES:', JSON.stringify(tiles, null, 1));
  const main = await page.locator('main').innerText();
  console.log('MAIN TEXT:\n', main);
});
