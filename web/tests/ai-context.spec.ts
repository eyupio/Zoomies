import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);

test('setup saves resumable drafts, retries only failures and never enables source access', async ({
  page,
  request,
}) => {
  const fake = JSON.parse(readFileSync('test-results/fakegithub.json', 'utf8'));
  const installations = await (await request.get('/api/v1/installations')).json();
  if (!installations.items.length) {
    const created = await request.post('/api/v1/installations', {
      data: {
        app_id: Number(fake.appId),
        installation_id: Number(fake.installationId),
        target: 'acme',
        target_type: 'org',
        api_base_url: fake.url,
        private_key: fake.privateKey,
      },
    });
    expect(created.status(), await created.text()).toBe(201);
  }
  await goto(page, '/ai-context', 'AI Context');
  await page.getByRole('link', { name: 'Enable repositories' }).click();
  await page.getByLabel('GitHub installation').selectOption({ label: 'acme' });
  await page.getByRole('checkbox', { name: 'acme/site', exact: true }).check();
  await page.getByRole('checkbox', { name: 'acme/widgets', exact: true }).check();
  const next = page.getByRole('button', { name: 'Next', exact: true });
  await next.click();
  await next.click();
  await expect(page.getByRole('radio', { name: /^Zoomies only/ })).toBeDisabled();
  await next.click();
  await page.getByLabel('Snapshots to retain').fill('7');
  await page.setViewportSize({ width: 375, height: 812 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: 'test-results/ai-context-mobile.png', fullPage: true });
  await next.click();
  await next.click();
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.screenshot({ path: 'test-results/ai-context-review.png', fullPage: true });
  let patches = 0;
  await page.route('**/api/v1/ai-context/repositories/*/config', async (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback();
    patches++;
    if (patches === 2)
      return route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({ error: { code: 'unavailable', message: 'Try again' } }),
      });
    return route.fallback();
  });
  await page.getByRole('button', { name: 'Save setup drafts' }).click();
  await expect(page.getByText('Draft saved', { exact: true })).toHaveCount(1);
  await page.getByRole('button', { name: 'Retry failed drafts' }).click();
  await expect(page.getByText('Draft saved', { exact: true })).toHaveCount(2);
  expect(patches).toBe(3);
  const drafts = await (await request.get('/api/v1/ai-context/repositories')).json();
  expect(drafts.items).toHaveLength(2);
  for (const draft of drafts.items) {
    expect(draft.available).toBe(false);
    expect(draft.config.keep_snapshots).toBe(7);
  }
  await page.getByRole('link', { name: 'Resume draft' }).first().click();
  await expect(page.getByLabel('GitHub installation')).toBeDisabled();
  await next.click();
  await next.click();
  await next.click();
  await expect(page.getByLabel('Snapshots to retain')).toHaveValue('7');
});
