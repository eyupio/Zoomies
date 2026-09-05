/**
 * The Usage page.
 *
 * Runner-hours belong to pools and installations: a runner idles on behalf of
 * a pool, never on behalf of a repository or a workflow. This protects that
 * the page drops the column and says why when asked to group by repository,
 * rather than printing a column of zeros that read as "this repository used
 * nothing", and that the three job counts are there in its place.
 */
import { expect, test } from '@playwright/test';
import { browserOverride, FIXTURE, goto } from './support/fixtures';

test.use(browserOverride);

const table = (page: import('@playwright/test').Page) => page.getByRole('table');

test('runner-hours are given by pool and dropped, with the reason, by repository', async ({
  page,
}) => {
  await goto(page, '/usage', 'Usage');

  // The default grouping is by pool, and the seeded fleet has had runners, so
  // the column is there and the first row carries a figure in hours.
  await expect(table(page).getByRole('columnheader', { name: 'Runner-hours' })).toBeVisible();
  await expect(table(page).getByRole('columnheader', { name: 'Estimated cost' })).toBeVisible();
  const first = table(page).getByRole('row').nth(1);
  await expect(first.getByRole('cell').nth(1)).toHaveText(/^\d+\.\d\d$/);
  // Strings rather than regular expressions throughout: Playwright normalises
  // whitespace for strings, and the template wraps these sentences.
  await expect(page.getByText('cannot be attributed at this grouping')).toBeHidden();

  await page.getByLabel('Group by').selectOption('repository');
  await page.getByRole('button', { name: 'Apply' }).click();

  await expect(page.getByText('never on behalf of a repository')).toBeVisible();
  await expect(table(page).getByRole('columnheader', { name: 'Runner-hours' })).toBeHidden();
  await expect(table(page).getByRole('columnheader', { name: 'Estimated cost' })).toBeHidden();

  // What is left is honest: a row per seeded repository, with the queued,
  // started and completed counts as whole numbers.
  const api = table(page).getByRole('row', { name: new RegExp(FIXTURE.repos[0]) });
  await expect(api).toBeVisible();
  for (const column of [1, 2, 3]) {
    await expect(api.getByRole('cell').nth(column)).toHaveText(/^\d+$/);
  }
});
