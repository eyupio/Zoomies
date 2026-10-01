import { expect, test } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);

/*
 * The summary tiles used to sit inside the loading skeleton, so an operator saw
 * four zeros for as long as the request took and then no tiles at all -- and the
 * quota tile, which is fed by readings that arrive after the list, could never
 * show one. They belong with the content, where they are computed from what
 * loaded.
 */
test('the summary tiles show the loaded connections and stay once the page has loaded', async ({
  page,
}) => {
  // An App nearly out of quota, which is the reading the fourth tile exists for.
  await page.route(/\/api\/v1\/installations\/[^/]+\/rate-limit$/, (route) =>
    route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ limit: 5000, remaining: 100, reset_at: '2030-01-01T00:00:00Z' }),
    }),
  );
  await goto(page, '/installations', 'Installations');

  // A tile has no role of its own: it is a term and its definition.
  const tile = (label: string) => page.locator('.metric').filter({ hasText: label });
  await expect(tile('GitHub connections').locator('dd').first()).toHaveText('1');
  await expect(tile('Low API quota').locator('dd').first()).toHaveText('1');
  await expect(tile('Low API quota')).toContainText('1 quota readings');
});
