/**
 * The Jobs grid.
 *
 * The page answers two operator questions, and this protects both. "Why is
 * this slow?" needs the queue wait and the run duration on every row. "Why has
 * this not started?" needs the unmatched filter to find the job no pool claims
 * and, more importantly, to say in full what that means -- an unmatched job is
 * not slow, nothing in this fleet is going to start it, and that is the one
 * thing the row itself cannot tell you.
 *
 * The same warning must stay off every job that already ran. A repository still
 * on a hosted-runner vendor produces jobs with labels no pool here claims, and
 * they run perfectly well; flagging them turned the page into a wall of red
 * during exactly the migration this fleet exists to make.
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
  // One of them ran on a hosted-runner vendor, and the default view leaves it
  // out.
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.managedJobs} jobs`);
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
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.managedJobs} jobs`);

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
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.managedJobs} jobs`);
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
  await expect(note).toContainText('1 queued job here has no pool to run it');
  await expect(note).toContainText('No enabled pool here answers');
  await expect(note).toContainText('it sits queued until the run is cancelled');
  await expect(note).toContainText('typo in');
  await expect(note.getByRole('link', { name: 'Check the pools and their labels' })).toBeVisible();
});

test('a job that already ran is never called unmatched, whatever its labels say', async ({
  page,
}) => {
  // The seed has one finished job on a hosted-runner vendor's label. It is
  // exactly as unclaimed as the queued one, and it plainly did run -- so it
  // takes the "other runners" view to see it at all.
  await goto(page, '/jobs?label=blacksmith-4vcpu-ubuntu-2404&all=true', 'Jobs');

  const vendor = dataRows(jobs(page)).first();
  await expect(vendor).toBeVisible();
  await expect(vendor).toContainText('Unclaimed');
  await expect(vendor).not.toContainText('Unmatched');

  // No banner either: it speaks for queued work, and there is none here.
  await expect(page.getByRole('note')).toHaveCount(0);

  // And on the unfiltered page the banner counts the one job that is actually
  // waiting, not every job whose labels this fleet does not answer.
  await goto(page, '/jobs', 'Jobs');
  await expect(page.getByRole('note')).toContainText('1 queued job here has no pool to run it');
});

/*
 * The default view is this fleet's own work. GitHub tells the controller about
 * every job in an installed repository, and a page that mixes a migration's
 * leftover hosted-runner jobs into the fleet's own history answers "how is my
 * fleet doing?" with somebody else's numbers. Seeing them is a deliberate act,
 * and it survives a copied link.
 */
test('other runners are hidden by default and one switch brings them back', async ({ page }) => {
  await goto(page, '/jobs', 'Jobs');
  await expect(dataRows(jobs(page)).first()).toBeVisible();
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.managedJobs} jobs`);
  await expect(jobs(page).getByText('blacksmith-4vcpu-ubuntu-2404')).toHaveCount(0);

  await page.getByRole('switch', { name: 'Include other runners' }).click();

  await expect(rowCount(page)).toContainText(`of ${FIXTURE.totalJobs} jobs`);
  await expect(page).toHaveURL(/[?&]all=true/);
  await expect(page.getByRole('group', { name: 'Filters in effect' })).toContainText(
    'jobs from every runner',
  );

  // A queued job nothing claims stays in the default view either way: nothing
  // ran it, so it is this fleet's problem to see.
  await goto(page, '/jobs', 'Jobs');
  await expect(page.getByRole('note')).toContainText('1 queued job here has no pool to run it');
});
