import { expect, test } from '@playwright/test';

const exe = process.env.PLAYWRIGHT_CHROMIUM;
test.use(exe ? { launchOptions: { executablePath: exe } } : {});

test('number field error', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push('console: ' + m.text());
  });
  await page.goto('/pools/new', { waitUntil: 'domcontentloaded' });
  const next = page.getByRole('button', { name: 'Next' });
  await page.getByRole('textbox', { name: 'Pool name' }).fill('e2e-wizard-pool');
  await next.click();
  await page.getByRole('textbox', { name: 'Labels' }).fill('gpu');
  await page.keyboard.press('Enter');
  await next.click();
  await next.click();
  await expect(page.getByRole('heading', { level: 2, name: 'Scaling' })).toBeVisible();
  console.log('errors before:', JSON.stringify(errors));
  const min = page.getByRole('spinbutton', { name: 'Minimum runners' });
  await min.pressSequentially('2');
  await page.waitForTimeout(500);
  console.log('errors after typing into Minimum runners:', JSON.stringify(errors, null, 1));
  console.log('progress:', await page.getByText(/Step \d of \d/).innerText());
  await next.click();
  await page.waitForTimeout(400);
  console.log('progress after next:', await page.getByText(/Step \d of \d/).innerText());
});
