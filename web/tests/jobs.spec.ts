/**
 * The Jobs grid.
 *
 * The page answers two operator questions, and this protects both. "Why is
 * this slow?" needs the queue wait and the run duration on every row. "Why has
 * this not started?" needs the unmatched filter to find the job no pool claims
 * and, more importantly, to say in full what that means -- an unmatched job is
 * not slow, it is never going to run, and that is the one thing the row itself
 * cannot tell you.
 */
import { expect, test, type Page } from '@playwright/test';
import {
  browserOverride,
  cellUnder,
  dataRows,
  facetTrigger,
  FIXTURE,
  goto,
  grid,
  rowCount,
} from './support/fixtures';

test.use(browserOverride);

/**
 * What formatDuration produces: "820ms", "9.4s", "35m 00s", "2h 05m". Not
 * anchored, because toContainText compares raw text and the cell markup puts
 * whitespace around the value.
 */
const DURATION = /(\d+(\.\d+)?(ms|s)\b|\d+[mhd] \d{2}[smh])/;

const jobs = (page: Page) => grid(page, 'Jobs');

test('the grid lists jobs with their queue wait and duration', async ({ page }) => {
  await goto(page, '/jobs', 'Jobs');
  const rows = dataRows(jobs(page));
  await expect(rows.first()).toBeVisible();

  // The seed writes fifty jobs and nothing adds more: no webhook ever arrives.
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.totalJobs} jobs`);
  await expect(jobs(page).getByRole('columnheader', { name: 'Queue wait' })).toBeVisible();
  await expect(jobs(page).getByRole('columnheader', { name: 'Duration' })).toBeVisible();

  // A finished job has both numbers, which is what makes the two columns worth
  // sorting by.
  const finished = rows.filter({ hasText: 'Success' }).first();
  await expect(finished).toBeVisible();
  await expect(await cellUnder(jobs(page), finished, 'Queue wait')).toContainText(DURATION);
  await expect(await cellUnder(jobs(page), finished, 'Duration')).toContainText(DURATION);
  await expect(await cellUnder(jobs(page), finished, 'Repository')).toContainText(/acme\//);

  // A job still waiting counts its wait up rather than pretending to know it.
  const queued = rows.filter({ hasText: 'Queued' }).first();
  await expect(await cellUnder(jobs(page), queued, 'Queue wait')).toContainText('so far');
});

test('a repository facet narrows the rows and is shareable', async ({ page }) => {
  await goto(page, '/jobs', 'Jobs');
  await expect(dataRows(jobs(page)).first()).toBeVisible();
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.totalJobs} jobs`);

  await facetTrigger(page, 'Repository').click();
  const menu = page.getByRole('group', { name: 'Filter by repository' });
  // The menu is built from GET /jobs/facets, so it lists what the database
  // holds rather than what happens to be on this page.
  for (const repo of FIXTURE.repos) {
    await expect(menu.getByRole('checkbox', { name: repo })).toBeVisible();
  }
  await menu.getByRole('checkbox', { name: 'acme/api' }).check();
  await page.keyboard.press('Escape');

  await expect(rowCount(page)).toContainText(`of ${FIXTURE.apiJobs} jobs`);
  await expect(page).toHaveURL(/[?&]repo=acme%2Fapi/);
  await expect(page.getByRole('group', { name: 'Filters in effect' })).toContainText('acme/api');
  for (const repo of await dataRows(jobs(page)).evaluateAll((rows) =>
    rows.map((row) => (row.querySelectorAll('td')[1] as HTMLElement | undefined)?.innerText ?? ''),
  )) {
    expect(repo.trim()).toBe('acme/api');
  }

  // Removing the chip puts every job back.
  await page.getByRole('button', { name: 'Remove the Repository filter acme/api' }).click();
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.totalJobs} jobs`);
});

test('the unmatched filter finds the job no pool claims and explains it', async ({ page }) => {
  await goto(page, '/jobs', 'Jobs');
  await expect(dataRows(jobs(page)).first()).toBeVisible();

  await page.getByRole('switch', { name: 'Unmatched only' }).click();

  await expect(page).toHaveURL(/[?&]unmatched=true/);
  await expect(rowCount(page)).toContainText('of 1 job');

  const row = dataRows(jobs(page)).first();
  await expect(row).toContainText('Unmatched');
  // The seeded job asks for labels no pool answers, so nothing claims it.
  await expect(row).toContainText('Unclaimed');
  await expect(row).toContainText('cuda12');

  // And the explanation, which is the part an operator cannot infer.
  const note = page.getByRole('note');
  await expect(note).toBeVisible();
  await expect(note).toContainText('1 job here will never run');
  await expect(note).toContainText('No enabled pool answers');
  await expect(note).toContainText('it will sit queued until it is cancelled');
  await expect(note).toContainText('typo in');
  await expect(note.getByRole('link', { name: 'Check the pools and their labels' })).toBeVisible();
});
