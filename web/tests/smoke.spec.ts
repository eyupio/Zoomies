import { expect, test } from '@playwright/test';

const exe = process.env.PLAYWRIGHT_CHROMIUM;
test.use(exe ? { launchOptions: { executablePath: exe } } : {});

test('boots', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
});
