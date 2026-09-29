/**
 * The Workflows page: one row per workflow run, opening to the jobs inside.
 *
 * What GitHub's Actions tab lists, in this fleet's terms. The rows are runs
 * rather than jobs, each summing up the jobs GitHub reported under it, and
 * a run opens in place to those jobs and how each is getting on -- so an
 * operator who arrives with a run number from a pull request finds it, and
 * then finds what this fleet did with it, without leaving the page.
 */
import { expect, test, type Page } from '@playwright/test';
import {
  browserOverride,
  dataRows,
  FIXTURE,
  goto,
  grid,
  pageHeading,
  rowCount,
} from './support/fixtures';

test.use(browserOverride);

const runs = (page: Page) => grid(page, 'Workflow runs');

/** The row of status buttons above the grid, by the view it names. */
function view(page: Page, name: string) {
  return page.getByRole('group', { name: 'Filter jobs by status' }).getByRole('button', { name });
}

test('the page lists one row per run and opens on what is running', async ({ page }) => {
  await goto(page, '/workflows', 'Workflows');
  await expect(dataRows(runs(page)).first()).toBeVisible();

  // Three jobs are running on this fleet's runners, in two runs: the page
  // counts runs, not jobs.
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.runningRuns} runs`);
  await expect(view(page, 'Running')).toHaveAttribute('aria-pressed', 'true');

  // A row is a run: it says how many jobs it holds and how they are getting on.
  const first = dataRows(runs(page)).first();
  await expect(first).toContainText('Running');
  await expect(first).toContainText(/\d+ running/);

  // Every run this fleet has a hand in, then everything GitHub reported.
  await view(page, 'All').click();
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.managedRuns} runs`);
  await page.getByRole('switch', { name: 'Include other runners' }).click();
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.totalRuns} runs`);
  await expect(page).toHaveURL(/[?&]all=true/);
});

test('a run opens in place to the jobs inside it, and closes again from the keyboard', async ({
  page,
}) => {
  // The one run with a failure this fleet caused: its row says so, and so
  // does the job under it.
  await goto(page, '/workflows?faulted=true', 'Workflows');
  const row = dataRows(runs(page)).first();
  await expect(row).toBeVisible();
  await expect(dataRows(runs(page))).toHaveCount(1);
  await expect(row).toContainText(`#${FIXTURE.faultedRun}`);
  await expect(row).toContainText('Runner lost');
  await expect(row).toContainText('1 succeeded · 1 failed');

  await row.getByRole('button', { name: 'Show jobs' }).click();
  const jobs = page.getByRole('table', { name: `Jobs in run #${FIXTURE.faultedRun}` });
  await expect(jobs).toBeVisible();
  await expect(jobs.locator('tbody tr')).toHaveCount(2);
  // Each job with its state and what went wrong, in the same words the Jobs
  // page uses: the fleet's own category rather than "failure" twice over.
  await expect(jobs).toContainText('Success');
  await expect(jobs).toContainText('Failure');
  await expect(jobs).toContainText('Out of memory');
  await expect(jobs).toContainText(FIXTURE.linuxPool);

  // A job opens the same drawer the Jobs page opens.
  const job = jobs.getByRole('button', { name: /^(build|test|lint|package)$/ }).first();
  await job.click();
  const drawer = page.getByRole('dialog');
  await expect(drawer).toBeVisible();
  await expect(drawer.getByRole('list', { name: 'Timeline' })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(drawer).toBeHidden();
  // Closing it hands focus back to the job, a frame after the drawer has
  // gone -- the page has to lift its inert first. Waited for, because a row
  // focused inside that frame has focus taken back from it, and the arrow
  // below then lands on the job's button, which the grid rightly leaves
  // alone -- which failed a full-suite run, the table still open.
  await expect(job).toBeFocused();

  // The arrows a tree uses close and open the run again.
  await row.focus();
  await page.keyboard.press('ArrowLeft');
  await expect(jobs).toBeHidden();
  await expect(row).toHaveAttribute('aria-expanded', 'false');
  await page.keyboard.press('ArrowRight');
  await expect(jobs).toBeVisible();
  await expect(row).toHaveAttribute('aria-expanded', 'true');
});

// A search that matches nothing is not an idle fleet, whatever the status view says.
test('a search that matches nothing says so and clears back to the default view', async ({
  page,
}) => {
  await goto(page, '/workflows?q=nothing-is-called-this', 'Workflows');

  await expect(page.getByText('No runs match these filters')).toBeVisible();
  await expect(page.getByText('Nothing is running right now')).toHaveCount(0);

  await page.getByRole('button', { name: 'Clear filters', exact: true }).last().click();
  await expect(page).not.toHaveURL(/[?&]q=/);
  await expect(dataRows(runs(page)).first()).toBeVisible();
});

test('a re-run counts its latest attempt and still lists the earlier one', async ({ page }) => {
  await goto(page, '/workflows?state=completed&repo=acme%2Fwidgets', 'Workflows');
  const row = dataRows(runs(page))
    .filter({ hasText: `#${FIXTURE.rerunRun}` })
    .first();
  await expect(row).toBeVisible();
  await expect(row).toContainText('attempt 2');

  await row.getByRole('button', { name: 'Show jobs' }).click();
  const jobs = page.getByRole('table', { name: `Jobs in run #${FIXTURE.rerunRun}` });
  await expect(jobs).toContainText('attempt 2');
  await expect(jobs).toContainText('attempt 1');
});

test('a run links to the same run on GitHub, and the filters travel to the Jobs page', async ({
  page,
}) => {
  await goto(page, '/workflows?state=completed&repo=acme%2Fapi', 'Workflows');
  const row = dataRows(runs(page)).first();
  await expect(row).toBeVisible();
  await expect(row.getByRole('link', { name: /^Open run \d+ of/ })).toHaveAttribute(
    'href',
    /github\.com\/acme\/api\/actions\/runs\/\d+$/,
  );

  // The same history one step down, narrowed the same way.
  await page.getByRole('link', { name: 'Every job' }).click();
  await expect(pageHeading(page, 'Jobs')).toBeVisible();
  await expect(page).toHaveURL(/\/jobs\?/);
  await expect(page).toHaveURL(/state=completed/);
  await expect(page).toHaveURL(/repo=acme%2Fapi/);
  await expect(rowCount(page)).toContainText('jobs');

  // And back up.
  await page.getByRole('link', { name: 'Workflow runs' }).click();
  await expect(pageHeading(page, 'Workflows')).toBeVisible();
  await expect(page).toHaveURL(/\/workflows\?/);
  await expect(page).toHaveURL(/repo=acme%2Fapi/);
});

/** A run's name is not a pattern: it carries slashes and dots of its own. */
function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/*
 * The Queue page's controls, on a run and on the jobs inside it. An operator
 * who knows the run from a pull request should be able to hold or hurry all
 * of it from its row, and one job of it from the row under that, without
 * going to the Queue page and finding the same jobs there by name.
 */
test('a run and the jobs inside it take the Queue page’s controls', async ({ page, request }) => {
  // A queued run this fleet has a hand in, and its queued jobs.
  const response = await request.get(
    '/api/v1/workflow-runs?state=queued&managed=true&cancelling=false&limit=1&sort=queued_at&order=asc',
  );
  const { items } = await response.json();
  expect(items.length).toBeGreaterThan(0);
  const run = items[0];
  const number = run.run_number || run.github_run_id;
  const subject = `${run.repo} · ${run.workflow || 'workflow'} #${number}`;
  const jobsResponse = await request.get(
    `/api/v1/jobs?repo=${encodeURIComponent(run.repo)}&run_id=${run.github_run_id}&state=queued`,
  );
  const queuedJobs: { id: string; job_name: string; repo: string }[] = (await jobsResponse.json())
    .items;
  expect(queuedJobs.length).toBeGreaterThan(0);
  const ids = queuedJobs.map((job) => job.id);
  try {
    await goto(page, `/workflows?state=queued&repo=${encodeURIComponent(run.repo)}`, 'Workflows');
    const row = dataRows(runs(page))
      .filter({ hasText: `#${number}` })
      .first();
    await expect(row).toBeVisible();

    // Pause on the run's row is one press and a confirmation, and every
    // queued job of the run is paused by it. The row says so itself, and
    // pressing Pause again is refused with the reason.
    await row.getByRole('button', { name: `Pause: ${subject}`, exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Pause', exact: true }).click();
    await expect(row).toContainText(/paused/i);
    for (const id of ids) {
      const job = await (await request.get(`/api/v1/jobs/${id}`)).json();
      expect(job.state).toBe('queued');
      expect(job.provisioning).toBe('paused');
    }
    const pause = row.getByRole('button', {
      name: new RegExp(`^Pause: ${escapeRegExp(subject)}\\.`),
    });
    await expect(pause).toHaveAttribute('aria-disabled', 'true');
    await expect(pause).toHaveAccessibleName(/already paused/);

    // Inside the run, each job wears the hold, and Run now on one of them
    // lifts that job alone: the endpoint is the Queue page's own, and so is
    // the state it leaves behind.
    await row.getByRole('button', { name: 'Show jobs' }).click();
    const jobs = page.getByRole('table', { name: `Jobs in run #${number}` });
    await expect(jobs).toBeVisible();
    const first = queuedJobs[0];
    if (!first) throw new Error('expected at least one queued job');
    const jobRow = jobs.locator('tbody tr').filter({ hasText: first.job_name }).first();
    await expect(jobRow).toContainText('Paused');
    await jobRow
      .getByRole('button', { name: `Run now: ${first.job_name} in ${first.repo}`, exact: true })
      .click();
    await page.getByRole('dialog').getByRole('button', { name: 'Run now', exact: true }).click();
    await expect(jobRow).toContainText('Run now');
    const updated = await (await request.get(`/api/v1/jobs/${first.id}`)).json();
    expect(updated.state).toBe('queued');
    expect(updated.provisioning).toBe('');
    expect(updated.provision_now).toBe(true);
  } finally {
    await request.post('/api/v1/provisioning/bulk', { data: { ids, action: 'resume' } });
  }
});
