/**
 * The Usage page.
 *
 * Runner-hours belong to pools and installations: a runner's idle time is no
 * repository's and no workflow's. This protects that the page says so when
 * asked to group by repository, rather than printing a column of zeros that
 * read as "this repository used nothing".
 */
import { expect, test } from '@playwright/test';
import { browserOverride, FIXTURE, goto } from './support/fixtures';

test.use(browserOverride);

test('runner-hours are given by pool and declared unattributable by repository', async ({
  page,
}) => {
  await goto(page, '/usage', 'Usage');

  // The default grouping is by pool, and the seeded fleet has run runners, so
  // the first row carries a figure in hours.
  const rows = page.getByRole('table').getByRole('row');
  await expect(rows.nth(1)).toBeVisible();
  await expect(rows.nth(1).getByRole('cell').nth(1)).toHaveText(/^\d+\.\d\d$/);
  await expect(page.getByText('belongs to no single')).toBeHidden();

  await page.getByLabel('Group by').selectOption('repository');
  await page.getByRole('button', { name: 'Apply' }).click();

  // A string rather than a regular expression: Playwright normalises whitespace
  // for strings, and the template wraps this sentence across lines.
  await expect(page.getByText('belongs to no single repository')).toBeVisible();
  const api = page.getByRole('table').getByRole('row', { name: new RegExp(FIXTURE.repos[0]) });
  await expect(api).toBeVisible();
  await expect(api.getByRole('cell').nth(1)).toHaveText('Not attributable');
  await expect(api.getByRole('cell').nth(5)).toHaveText('Not attributable');
});
