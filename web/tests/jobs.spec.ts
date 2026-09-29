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
  columnTexts,
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

/** The row of status buttons above the grid. */
const statusFilter = (page: Page) => page.getByRole('group', { name: 'Filter jobs by status' });

/**
 * Every State cell, once the grid has answered the question just asked of it.
 *
 * The grid refetches on a short debounce, so reading the column the instant the
 * URL changes reads the page before it: polled rather than sampled once.
 */
async function statesSettle(page: Page, matches: RegExp): Promise<void> {
  await expect
    .poll(async () => {
      const states = (await columnTexts(jobs(page), 'State')).map((state) => state.trim());
      return states.length > 0 && states.every((state) => matches.test(state));
    })
    .toBe(true);
}

/**
 * Press "All" in the status row.
 *
 * The page opens on what is running, which is the question an operator opens it
 * with and is not the question most of these tests ask: they are about the
 * grid, the facets and the drawer, and those want the whole history in front of
 * them.
 */
async function everyStatus(page: Page): Promise<void> {
  await statusFilter(page).getByRole('button', { name: 'All', exact: true }).click();
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.managedJobs} jobs`);
}

test('the grid lists jobs with their queue wait and duration', async ({ page }) => {
  await goto(page, '/jobs', 'Jobs');
  const rows = dataRows(jobs(page));
  await expect(rows.first()).toBeVisible();

  // The seed writes a fixed set of jobs and nothing adds more: no webhook ever
  // arrives. Three of them ran on a hosted-runner vendor, and the default view
  // leaves those out.
  await everyStatus(page);
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
  await everyStatus(page);

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
  for (const repo of await columnTexts(jobs(page), 'Repository')) {
    expect(repo.trim()).toBe('acme/api');
  }

  // Removing the chip puts every job back. The status row is untouched by a
  // facet, so the view is still every status.
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
  await expect(note).toContainText('1 queued job has no pool here');
  await expect(note).toContainText('No enabled pool here answers');
  // Both readings are given: the fleet's own fault, or another provider's job.
  await expect(note).toContainText('typo in');
  await expect(note).toContainText('another runner provider');
  await expect(note.getByRole('link', { name: 'Check the pools and their labels' })).toBeVisible();
});

/*
 * Status is the filter this page is opened for, and it used to be three clicks
 * down a menu. It is a row of buttons now, and a page nobody has asked anything
 * of answers the question it is opened with -- what is running -- rather than
 * listing six months of finished work first.
 *
 * The default is for a bare visit only. Every link into this page from the
 * Overview, the insights and the problems panel already says what it wants, and
 * a default that overrode those would answer a different question than the one
 * the sender asked; that half matters more than the default itself, so it is
 * checked here rather than left to the tests that happen to use those links.
 */
test('the page opens on what is running, and the status row moves between views', async ({
  page,
}) => {
  await goto(page, '/jobs', 'Jobs');
  const running = statusFilter(page).getByRole('button', { name: 'Running', exact: true });
  await expect(running).toHaveAttribute('aria-pressed', 'true');
  await expect(rowCount(page)).toContainText(`of ${FIXTURE.runningJobs} jobs`);
  await statesSettle(page, /^Running$/);

  // The status is already on the row, so it is not repeated as a chip -- and a
  // chip for the default could not be removed, since removing it is what puts
  // the default back.
  await expect(page.getByRole('group', { name: 'Filters in effect' })).toHaveCount(0);

  // Another view is one press, and it is a link somebody else can open.
  await statusFilter(page).getByRole('button', { name: 'Queued', exact: true }).click();
  await expect(page).toHaveURL(/[?&]state=queued/);
  await expect(running).toHaveAttribute('aria-pressed', 'false');
  await statesSettle(page, /Queued/);

  // "Failed" is not a state at all -- it is the server's own reckoning of a job
  // that went wrong, runner faults included -- so pressing it has to clear the
  // state it replaces rather than stack on top of it and match nothing.
  await statusFilter(page).getByRole('button', { name: 'Failed', exact: true }).click();
  await expect(page).toHaveURL(/[?&]failed=true/);
  await expect(page).not.toHaveURL(/[?&]state=/);
  await statesSettle(page, /Failure|Timed out|Runner lost/);

  // And "All" is written out rather than left absent, because an absent status
  // is what the default reads as "running".
  await everyStatus(page);
  await expect(page).toHaveURL(/[?&]state=in_progress/);
  await expect(page).not.toHaveURL(/[?&]failed=true/);
});

/*
 * A narrower filter must not quietly widen the status. Typing in the search box
 * puts something in the address bar, and the default only holds while nothing is
 * there -- so without carrying the status along, one keystroke would take an
 * operator from "what is running" to every job the fleet has ever run.
 */
test('narrowing something else keeps the status the page is showing', async ({ page }) => {
  await goto(page, '/jobs', 'Jobs');
  await expect(dataRows(jobs(page)).first()).toBeVisible();

  await facetTrigger(page, 'Repository').click();
  await page
    .getByRole('group', { name: 'Filter by repository' })
    .getByRole('checkbox', { name: FIXTURE.repos[0] })
    .check();
  await page.keyboard.press('Escape');

  await expect(page).toHaveURL(/[?&]state=in_progress/);
  await expect(
    statusFilter(page).getByRole('button', { name: 'Running', exact: true }),
  ).toHaveAttribute('aria-pressed', 'true');
});

/*
 * An idle fleet is the ordinary state of a small one, and the page it opens on
 * is then empty. What it must not do is say something false about it: "no jobs
 * have run on this fleet" is the sentence the empty state exists to avoid, and
 * the way out is the same page without this one filter.
 */
test('an empty running view says the fleet is idle and offers the way out', async ({ page }) => {
  // A window nothing ran in, so the view is honestly empty whatever the fleet
  // is doing while the suite runs.
  await goto(page, '/jobs?state=in_progress&since=2020-01-01&until=2020-01-02', 'Jobs');

  await expect(page.getByText('Nothing is running right now')).toBeVisible();
  await page.getByRole('button', { name: 'Show every status', exact: true }).click();

  // The dates the operator chose are still theirs; only the status widened.
  await expect(page).toHaveURL(/since=2020-01-01/);
  await expect(
    statusFilter(page).getByRole('button', { name: 'All', exact: true }),
  ).toHaveAttribute('aria-pressed', 'true');
});

/*
 * A search that matches nothing is not an idle fleet. The page used to answer
 * it with "Nothing is running right now" -- false while other jobs run -- or
 * with "No jobs have run on this fleet", and offered a switch that cannot fix a
 * search.
 */
test('a search that matches nothing says so and clears back to the default view', async ({
  page,
}) => {
  await goto(page, '/jobs?q=nothing-is-called-this', 'Jobs');

  await expect(page.getByText('No jobs match these filters')).toBeVisible();
  await expect(page.getByText('Nothing is running right now')).toHaveCount(0);
  await expect(page.getByText('No jobs have run on this fleet')).toHaveCount(0);

  await page.getByRole('button', { name: 'Clear filters', exact: true }).last().click();
  await expect(page).not.toHaveURL(/[?&]q=/);
  await expect(dataRows(jobs(page)).first()).toBeVisible();
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
});

/*
 * An organisation that also rents runners elsewhere queues jobs this fleet has
 * no pool for all day long. They are listed by default because nothing here
 * ran them, but a warning above the grid about somebody else's work is a red
 * banner an operator can never clear -- and after a while stops reading, which
 * costs them the one that is theirs. The explanation is kept for the operator
 * who went looking: the unmatched filter, or the problems panel's link into it.
 */
test('the unmatched explanation waits to be asked for', async ({ page }) => {
  await goto(page, '/jobs', 'Jobs');
  await expect(dataRows(jobs(page)).first()).toBeVisible();
  await everyStatus(page);

  // The job is listed, and its row says what it is.
  await expect(jobs(page)).toContainText('Unmatched');
  await expect(page.getByRole('note')).toHaveCount(0);

  await page.getByRole('switch', { name: 'Unmatched only' }).click();
  await expect(page).toHaveURL(/[?&]unmatched=true/);
  await expect(page.getByRole('note')).toContainText('1 queued job has no pool here');

  // One job, and it is the fleet's own. The vendor's queued job is unclaimed
  // for the same reason and is not one of these: "nothing will run this" is
  // false about it, and it is the sentence this view exists to say.
  await expect(rowCount(page)).toContainText('of 1 job');
  await expect(jobs(page).getByText('blacksmith-4vcpu-ubuntu-2404')).toHaveCount(0);

  // The problems panel links straight here with the filter already on, which
  // is how an operator who has not gone looking still finds it. A link that
  // carries a filter keeps it: the page's own default is for a bare visit.
  await goto(page, '/jobs?unmatched=true', 'Jobs');
  await expect(page.getByRole('note')).toContainText('1 queued job has no pool here');
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
  await everyStatus(page);
  await expect(jobs(page).getByText('blacksmith-4vcpu-ubuntu-2404')).toHaveCount(0);

  await page.getByRole('switch', { name: 'Include other runners' }).click();

  await expect(rowCount(page)).toContainText(`of ${FIXTURE.totalJobs} jobs`);
  await expect(page).toHaveURL(/[?&]all=true/);
  await expect(page.getByRole('group', { name: 'Filters in effect' })).toContainText(
    'jobs from every runner',
  );

  // Including the one still queued, which is what the switch being off used to
  // fail to hide: GitHub reports a vendor's job the moment it is queued, and
  // "nothing here has run it" is true of it for a few seconds.
  const vendorQueued = dataRows(jobs(page)).filter({ hasText: 'blacksmith-4vcpu-ubuntu-2404' });
  await expect(vendorQueued.filter({ hasText: 'Queued' })).toHaveCount(1);
  await expect(vendorQueued.filter({ hasText: 'Queued' })).toContainText('Hosted elsewhere');

  // A queued job nothing claims is listed whichever runners are shown: nothing
  // ran it, so it is this fleet's problem to see. The row is how it is seen --
  // the explanation above the grid waits for the filter.
  await goto(page, '/jobs?state=queued', 'Jobs');
  await expect(jobs(page).getByText('cuda12')).toBeVisible();
  await expect(jobs(page)).toContainText('Unmatched');
});

/*
 * The drawer is where "what happened to my job?" gets answered without leaving
 * for GitHub: which step it failed at, and the story of how it got there, in
 * sentences. A job whose runner died under it says so first, because GitHub
 * records that as an ordinary failure and the workflow's owner would otherwise
 * go looking for a bug that is not there.
 */
test('opening a failed job names the step and tells the story', async ({ page }) => {
  await goto(page, '/jobs?failed=true', 'Jobs');
  const rows = dataRows(jobs(page));
  await expect(rows.first()).toBeVisible();

  // Every row in the failed view says where it went wrong, on the row itself.
  const stepFailure = rows
    .filter({ hasText: 'Failure' })
    .filter({ hasNotText: 'Runner lost' })
    .first();
  await expect(stepFailure).toBeVisible();
  await expect(await cellUnder(jobs(page), stepFailure, 'Failed at')).not.toContainText('--');

  await stepFailure.click();
  const drawer = page.getByRole('dialog');
  await expect(drawer).toBeVisible();

  // Why, first.
  const why = drawer.getByRole('note', { name: 'Why this job went wrong' });
  await expect(why).toContainText(/Failure at step \d+, /);
  await expect(why.getByRole('link', { name: /Open the failed step's log/ })).toBeVisible();

  // The steps, with the failed one among them.
  const steps = drawer.getByRole('list', { name: 'Steps' });
  await expect(steps.getByRole('listitem')).toHaveCount(6);
  await expect(steps).toContainText('Set up job');

  // And the timeline, oldest first, ending with how it ended.
  const timeline = drawer.getByRole('list', { name: 'Timeline' });
  const entries = timeline.getByRole('listitem');
  await expect(entries.first()).toContainText('GitHub queued');
  await expect(entries.first()).toContainText('via webhook');
  await expect(entries.last()).toContainText(/failed at step \d+/);
  await expect(timeline).toContainText('started on runner');
});

test("a job whose runner died under it is called the fleet's failure", async ({ page }) => {
  await goto(page, '/jobs?failed=true', 'Jobs');
  const lost = dataRows(jobs(page)).filter({ hasText: 'Runner lost' }).first();
  await expect(lost).toBeVisible();
  // The category rather than "Runner lost" repeated down the column: a list
  // where every row says the same two words says only that the fleet is
  // unwell, and one that says "Out of memory" says what to do on Monday.
  await expect(await cellUnder(jobs(page), lost, 'Failed at')).toContainText('Out of memory');

  await lost.click();
  const drawer = page.getByRole('dialog');
  const why = drawer.getByRole('note', { name: 'Why this job went wrong' });
  await expect(why).toContainText('Out of memory');
  await expect(why).toContainText('the workflow did nothing wrong');
  // And the remedy, which is the half somebody opened the page for. It comes
  // from the server, so the UI, the CLI and the problems drawer cannot offer
  // three different answers.
  await expect(why).toContainText("raise the pool's memory limit");
  await expect(why.getByRole('link', { name: 'Open the runner' })).toBeVisible();

  const timeline = drawer.getByRole('list', { name: 'Timeline' });
  await expect(timeline).toContainText('Runner lost');
  await expect(timeline).toContainText('via agent');

  // The problems drawer says the same thing, and sends the operator here.
  await page.keyboard.press('Escape');
  await expect(drawer).toBeHidden();
  await page.getByRole('button', { name: /^Problems\./ }).click();
  const problems = page.getByRole('dialog', { name: 'Problems' });
  await expect(problems).toContainText('lost the runner it was running on');
  await expect(problems.getByRole('link', { name: 'Open failed jobs' })).toBeVisible();
});

test("the fleet's own failures are one click from everything that failed", async ({ page }) => {
  // The question an operator arrives with is not "what failed" but "is this
  // us". GitHub records both halves as "failure", so this view is the only
  // place the difference can be asked for.
  await goto(page, '/jobs?failed=true', 'Jobs');
  // Counted after the first row arrives: a count taken while the grid is still
  // loading is zero, and every comparison against it passes for the wrong
  // reason.
  await expect(dataRows(jobs(page)).first()).toBeVisible();
  const everything = await dataRows(jobs(page)).count();

  await page.getByRole('button', { name: 'Our failures' }).click();
  await expect(page).toHaveURL(/faulted=true/);
  // Polled rather than counted once: the rows already on the page are the
  // previous view's until the refetch lands, and a count taken before it does
  // passes by comparing the old list with itself.
  const ours = dataRows(jobs(page));
  await expect.poll(() => ours.count()).toBeLessThan(everything);
  // Every row in this view is the fleet's, so every one of them carries the
  // badge that says so.
  for (const row of await ours.all()) {
    await expect(row).toContainText('Runner lost');
  }

  // A copied link reproduces it, which is how this gets sent to somebody.
  await page.reload();
  await expect(page.getByRole('button', { name: 'Our failures' })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
});

test('the failed filter is one switch and survives a copied link', async ({ page }) => {
  await goto(page, '/jobs', 'Jobs');
  await expect(dataRows(jobs(page)).first()).toBeVisible();
  await everyStatus(page);

  await page.getByRole('switch', { name: 'Failed only' }).click();
  await expect(page).toHaveURL(/[?&]failed=true/);
  // The status row is what says which view is in force, so the switch presses
  // the Failed button rather than adding a chip that repeats it.
  await expect(
    statusFilter(page).getByRole('button', { name: 'Failed', exact: true }),
  ).toHaveAttribute('aria-pressed', 'true');
  // Fewer than everything, and every one of them a failure of some kind. The
  // grid keeps the old count until the narrowed page lands, so wait for it.
  await expect(rowCount(page)).not.toContainText(`of ${FIXTURE.managedJobs} jobs`);
  const total = await rowCount(page).innerText();
  const shown = Number(/of (\d+) jobs?/.exec(total)?.[1] ?? '0');
  expect(shown).toBeGreaterThan(0);
  expect(shown).toBeLessThan(FIXTURE.managedJobs);
  for (const state of await columnTexts(jobs(page), 'State')) {
    expect(state).toMatch(/Failure|Timed out|Runner lost/);
  }
});

test('a queued job says what the fleet is doing about it', async ({ page }) => {
  await goto(page, '/jobs?state=queued', 'Jobs');
  // The queued job a pool claims, not the unmatched one: that one has its own note.
  const waiting = dataRows(jobs(page)).filter({ hasNotText: 'Unmatched' }).first();
  await expect(waiting).toBeVisible();
  await waiting.click();

  const drawer = page.getByRole('dialog');
  const status = drawer.getByRole('status', { name: 'What is happening to this job' });
  // Either lead word answers the question this test asks. Which one appears is
  // not about the page: the drawer says "Blocked" once the job's pool carries a
  // pool.no_capacity problem, and the demo fleet's pool genuinely cannot place
  // -- one host cordoned, one the wrong OS, one short of memory -- so the word
  // turns over the moment the scheduler has counted the seeded job as queued
  // and matched. Asserting one of them raced a background pass and lost on any
  // slow run: it failed twice in one morning, on two pull requests, one of
  // which changed nothing but Go files under test/drill.
  //
  // The substance is below -- the pool it is waiting on, and the live counts
  // that give the wait a reason -- and that holds either way.
  await expect(status).toContainText(/Waiting|Blocked/);
  await expect(status).toContainText(/zoomies-demo-linux-(x64|arm64)/);
  // The pool's live counts, so the wait has a reason next to it.
  await expect(status).toContainText('Starting');
  await expect(status).toContainText('Idle');
});

/**
 * A job an operator has taken out of the queue is not queued work, and the
 * page has to say so in both directions.
 *
 * GitHub goes on calling it `queued`, because Zoomies cannot unqueue a job, so
 * the Queued view used to list it beside work the fleet was about to pick up --
 * and beside its own "Queued now" figure, which stopped counting it. The view
 * now narrows to what is actually waiting, says so in a chip, and the job is
 * still in the history under All, badged for what it is.
 *
 * Rows are counted by the badge rather than found by name: the fixture runs
 * the same job names on several workflows, and a filter on one of them matches
 * work that has nothing to do with this.
 */
test('removing a job from the queue takes it out of Queued and badges it under All', async ({
  page,
  request,
}) => {
  const queue = await (
    await request.get('/api/v1/provisioning?limit=1&sort=queued_at&order=asc')
  ).json();
  const job = queue.items[0];
  expect(job, 'the fixture has queued work to remove').toBeTruthy();
  const removed = (p: Page) => dataRows(jobs(p)).filter({ hasText: 'Removed' });

  try {
    await request.post('/api/v1/provisioning/bulk', {
      data: { ids: [job.id], action: 'delete' },
    });

    await goto(page, '/jobs?state=queued', 'Jobs');
    // The filter the view applies on its own behalf is visible, so a shorter
    // list never looks like rows that went missing.
    await expect(page.getByText('Queue status')).toBeVisible();
    await expect(removed(page)).toHaveCount(0);

    // Still in the history, and called what it is.
    await everyStatus(page);
    await expect(removed(page)).toHaveCount(1);

    // And reachable on its own, which is what the chip's filter names.
    await goto(page, '/jobs?state=queued&provisioning=deleted', 'Jobs');
    await expect(dataRows(jobs(page))).toHaveCount(1);
    await expect(removed(page)).toHaveCount(1);
  } finally {
    await request.post('/api/v1/provisioning/bulk', {
      data: { ids: [job.id], action: 'resume' },
    });
  }
});
