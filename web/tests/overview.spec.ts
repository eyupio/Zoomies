/**
 * The Overview.
 *
 * This is the page an operator leaves open on a second monitor, so what it
 * protects is the promise that looking at it is enough: four numbers with an
 * hour of shape behind them, everything that needs a person listed with the
 * fix beside it, which pools cannot grow, and the scheduler's own words for
 * why runners appeared. The refresh button is here too, but what is protected
 * about it is that it is never load-bearing: the numbers arrive on their own,
 * and pressing it changes nothing about what the page says.
 */
import { expect, test, type Page } from '@playwright/test';
import { browserOverride, FIXTURE, goto } from './support/fixtures';

test.use(browserOverride);

test.beforeEach(async ({ page }) => {
  await goto(page, '/', 'Overview');
});

/**
 * Widen the activity matrix to the year.
 *
 * The matrix opens on today, by the hour, like every other range control; the
 * tests below are about the calendar of days, so they ask for it.
 */
async function yearOfDays(page: Page) {
  const matrix = page.getByRole('region', { name: 'Activity matrix', exact: true });
  await matrix
    .getByRole('group', { name: 'Range' })
    .getByRole('button', { name: 'The last year' })
    .click();
  await expect(matrix.getByRole('grid')).toHaveAccessibleName(/one square per day/);
}

/** A count. The tile's value paragraph is the only text that is only digits. */
const COUNT = /^\d[\d,]*$/;
/** What formatDuration produces: "820ms", "9.4s", "35m 00s", "2h 05m", or "--". */
const DURATION = /^(--|[\d.]+(ms|s)|\d+[mhd] \d{2}[smh])$/;

// A square that has jobs, however they went: "38 jobs finished, 2 failed" or
// "6 jobs queued, none finished". Both halves matter -- for the twenty minutes
// after midnight the newest day is all queue and no completions, because the
// demo fleet's most recent finished job is twenty minutes old and lands on
// yesterday, and a square that says "none finished" is still a square with
// jobs on it.
const WITH_JOBS = /\d[\d,]* jobs? (finished|queued)/;

test('the four metric tiles carry the numbers the fleet is judged on', async ({ page }) => {
  for (const label of ['Queued jobs', 'Running jobs', 'Live runners']) {
    const tile = page.getByRole('link', { name: new RegExp(`^${label}`) });
    await expect(tile, `${label} is on the Overview`).toBeVisible();
    await expect(tile.getByText(COUNT), `${label} shows a number`).toBeVisible();
  }

  const wait = page.getByRole('link', { name: /^Median queue wait/ });
  await expect(wait).toBeVisible();
  await expect(wait.getByText(DURATION)).toBeVisible();
  // The p95 is the reason the median is worth showing: one of them moving
  // without the other is the whole story of a fleet that is nearly coping.
  await expect(wait).toContainText(/p95/);
});

/**
 * The tiles are the first numbers anybody reads, and until now they counted
 * every job GitHub reported.
 *
 * On an organisation that also uses hosted runners -- which the fixture is --
 * that made "Queued jobs" and "Running jobs" partly somebody else's, so the
 * headline answer to "why is my fleet slow?" was a number nobody here could
 * act on. The tiles now default to this fleet's own work, like the panels
 * below them, and the same switch widens all of it.
 */
test("the metric tiles are this fleet's work, and the switch widens them", async ({ page }) => {
  const running = page.getByRole('link', { name: /^Running jobs/ });
  // The page header's own switch, not one of the panels': it governs the whole
  // page, and the tiles are the part of it nothing else controls.
  const header = page.locator('header').filter({ has: page.getByRole('heading', { level: 1 }) });
  const toggle = header.getByRole('switch', { name: 'Other runners' });

  await expect(toggle, 'the Overview carries the toggle its panels have').toBeVisible();
  await expect(toggle).not.toBeChecked();

  const ours = Number((await running.getByText(COUNT).innerText()).replace(/,/g, ''));

  // The fixture has one job running on a hosted-runner vendor. With the switch
  // off it is not in the tile; with it on it is.
  await toggle.click();
  await expect(toggle).toBeChecked();
  await expect
    .poll(async () => Number((await running.getByText(COUNT).innerText()).replace(/,/g, '')), {
      message: "the tile did not widen to include somebody else's running job",
    })
    .toBeGreaterThan(ours);

  // The tile links to the same jobs it just counted, or the number and the
  // list it leads to disagree.
  await expect(running).toHaveAttribute('href', /all=true/);

  // One preference, so the panels below moved with it.
  await expect(
    page
      .getByRole('region', { name: 'Active jobs', exact: true })
      .getByRole('switch', { name: 'Other runners' }),
  ).toBeChecked();

  await toggle.click();
  await expect(toggle).not.toBeChecked();
  await expect(running).toHaveAttribute('href', /^\/jobs\?state=in_progress$/);
  await expect
    .poll(async () => Number((await running.getByText(COUNT).innerText()).replace(/,/g, '')))
    .toBe(ours);
});

/**
 * The queued tile leads to the Queue, not to the Jobs page, and that is
 * deliberate.
 *
 * It counts this fleet's queued work less whatever an operator has removed
 * from the queue, which is exactly the Queue's default view. The Jobs page is
 * history: it has no provisioning filter, so a job removed from the queue sits
 * in it looking like any other queued one, and the tile and the list behind it
 * would disagree the moment somebody cleared a queue. With other runners on
 * the tile is counting queues this fleet does not own, which the Queue has
 * nothing to say about, so that view goes to the Jobs page as the others do.
 */
test('the queued tile leads to the queue it just counted', async ({ page }) => {
  const queued = page.getByRole('link', { name: /^Queued jobs/ });
  await expect(queued).toHaveAttribute('href', '/queue');

  const header = page.locator('header').filter({ has: page.getByRole('heading', { level: 1 }) });
  const toggle = header.getByRole('switch', { name: 'Other runners' });
  await toggle.click();
  await expect(toggle).toBeChecked();
  await expect(queued).toHaveAttribute('href', '/jobs?state=queued&all=true');

  await toggle.click();
  await expect(toggle).not.toBeChecked();
  await expect(queued).toHaveAttribute('href', '/queue');
});

test('each trend tile carries a described sparkline', async ({ page }) => {
  // The wait tile is deliberately excluded: its series is built from live
  // `stats` frames rather than from GET /samples, so on a freshly started
  // controller it has one point and correctly draws no line.
  for (const label of ['Queued jobs', 'Running jobs', 'Live runners']) {
    const tile = page.getByRole('link', { name: new RegExp(`^${label}`) });
    const sparkline = tile.getByRole('img');
    await expect(sparkline, `${label} has a sparkline`).toBeVisible();
    // The description is the whole point: a line nobody can read is not data.
    await expect(sparkline).toHaveAttribute(
      'aria-label',
      new RegExp(`^${label}: .+ now, .+ at the start of the window, peaking at .+`),
    );
  }
});

/** Open the problems drawer the way an operator does: from the top bar. */
async function openProblems(page: import('@playwright/test').Page) {
  await page.getByRole('button', { name: /^Problems\./ }).click();
  const drawer = page.getByRole('dialog', { name: 'Problems' });
  await expect(drawer).toBeVisible();
  return drawer;
}

test('the Overview says how much needs a person without spending the page on it', async ({
  page,
}) => {
  // The whole point of the summary: the fleet's own panels are what the page
  // is for, and a settled configuration warning must not push them under the
  // fold. So what is on the page is a sentence and a way in -- never the list.
  await expect(page.getByText(/needs? your attention\.$/).first()).toBeVisible();
  await expect(page.getByRole('button', { name: 'Review' })).toBeVisible();

  // Nothing in the page's own content lists a problem's fix; that lives in the
  // drawer, which is not open yet.
  await expect(page.getByRole('main')).not.toContainText('no webhook has ever arrived');

  // And on a desktop it is still on the first screen, under the matrix and
  // the numbers, which is exactly what the full-height list used to cost:
  // the line that says what needs a person must never be the thing a
  // settled configuration warning pushes out of sight. A phone stacks
  // everything and cannot promise this, so it is not asserted there.
  const viewport = page.viewportSize();
  if ((viewport?.width ?? 0) >= 1180) {
    const box = await page.getByRole('button', { name: 'Review' }).boundingBox();
    expect(box, 'the problems line is laid out').not.toBeNull();
    expect(box?.y ?? Infinity, 'the problems line is within the first screen').toBeLessThan(
      viewport?.height ?? 0,
    );
  }
});

test('the top bar carries the count on every page and opens the list', async ({ page }) => {
  const bell = page.getByRole('button', { name: /^Problems\./ });
  // The label spells the counts out, because colour and a badge alone are not
  // information anyone can read aloud.
  await expect(bell).toHaveAccessibleName(/(error|warning|note)s? .*need/);

  await page.goto('/runners');
  await expect(bell, 'the count follows the operator off the Overview').toBeVisible();
  await openProblems(page);
});

test('the problems drawer names the seeded faults, with a fix for each', async ({ page }) => {
  const problems = await openProblems(page);

  // Four the seeded fleet always has: a controller that cannot be told about
  // queued work, a pool that weakens isolation two different ways, and a job
  // no pool will ever claim. Host heartbeats are deliberately not asserted on:
  // the fixture hosts have no agent behind them, so they fall silent 90s after
  // the controller starts and that entry appears part-way through a run.
  await expect(problems).toContainText('no webhook has ever arrived');
  await expect(problems).toContainText('authentication is disabled');
  await expect(problems).toContainText(
    `pool ${FIXTURE.armPool}: docker-in-docker sidecar: runners get a privileged container`,
  );
  await expect(problems).toContainText(
    `pool ${FIXTURE.armPool}: persistent runners: job state and credentials leak between workflow runs`,
  );
  await expect(problems).toContainText(/no enabled pool here claims \d+ queued jobs?/);

  // Errors are listed before warnings, so the worst thing is the first thing.
  await expect(problems.getByRole('heading', { level: 3 }).first()).toContainText(/error/);

  // Every entry says what is true, why it matters and what to change. Those
  // three lines have no roles that tell them apart, so they are read by class.
  const entries = problems.getByRole('listitem');
  await expect(entries.first()).toBeVisible();
  const lines = await entries.evaluateAll((items) =>
    items.map((item) => ({
      title: (item.querySelector('.title') as HTMLElement | null)?.innerText.trim() ?? '',
      detail: (item.querySelector('.detail') as HTMLElement | null)?.innerText.trim() ?? '',
      fix: (item.querySelector('.fix') as HTMLElement | null)?.innerText.trim() ?? '',
    })),
  );
  expect(lines.length).toBeGreaterThanOrEqual(5);
  for (const line of lines) {
    expect(line.title, 'every problem says what is wrong').not.toBe('');
    expect(line.detail, `"${line.title}" says why it matters`).not.toBe('');
    expect(line.fix, `"${line.title}" says what to change`).not.toBe('');
  }
});

test('a dismissed problem stops asking, and can be brought back', async ({ page }) => {
  const problems = await openProblems(page);
  const before = await problems.getByRole('listitem').count();

  const entry = problems.getByRole('listitem').filter({ hasText: 'authentication is disabled' });
  await entry.getByRole('button', { name: /^Dismiss or snooze:/ }).click();
  await page.getByRole('menuitem', { name: 'Dismiss until resolved' }).click();

  // Gone from the list, and off the count in the top bar.
  await expect(problems.getByRole('listitem')).toHaveCount(before - 1);
  await expect(problems).not.toContainText('authentication is disabled');

  // Never deleted, though: a dismissal is a decision, and a decision can be
  // undone. It is dated where it is listed.
  await problems.getByRole('button', { name: /Show 1 dismissed problem/ }).click();
  const dismissed = problems
    .getByRole('listitem')
    .filter({ hasText: 'authentication is disabled' });
  await expect(dismissed).toContainText('dismissed');
  await dismissed.getByRole('button', { name: /^Restore:/ }).click();
  await expect(problems.getByRole('listitem')).toHaveCount(before);

  // The controller is untouched by any of this: what an operator has read is a
  // browser preference, never fleet state, so an alerting rule still sees it.
  const api = await page.request.get('/api/v1/problems').then((r) => r.json());
  expect(
    (api.items ?? []).some((p: { code?: string }) => p.code === 'auth.disabled'),
    'the API still reports everything',
  ).toBe(true);
});

test('a problem can be snoozed for a fixed while instead of dismissed outright', async ({
  page,
}) => {
  const problems = await openProblems(page);
  const before = await problems.getByRole('listitem').count();

  const entry = problems.getByRole('listitem').filter({ hasText: 'authentication is disabled' });
  await entry.getByRole('button', { name: /^Dismiss or snooze:/ }).click();
  // Several durations are offered, not just one: an operator who knows this
  // will resolve itself in an hour should not have to pick "until resolved".
  await expect(page.getByRole('menuitem', { name: 'Snooze for 15 minutes' })).toBeVisible();
  await expect(page.getByRole('menuitem', { name: 'Snooze for 1 hour' })).toBeVisible();
  await expect(page.getByRole('menuitem', { name: 'Snooze for 4 hours' })).toBeVisible();
  await page.getByRole('menuitem', { name: 'Snooze for 24 hours' }).click();

  // Gone from the active list, same as an outright dismissal.
  await expect(problems.getByRole('listitem')).toHaveCount(before - 1);
  await expect(problems).not.toContainText('authentication is disabled');

  // Filed under dismissed, but it says when it comes back rather than just
  // when it was put away -- a snooze is a dated decision with a deadline.
  await problems.getByRole('button', { name: /Show 1 dismissed problem/ }).click();
  const snoozed = problems.getByRole('listitem').filter({ hasText: 'authentication is disabled' });
  await expect(snoozed).toContainText('dismissed');
  await expect(snoozed).toContainText(/back in/i);

  await snoozed.getByRole('button', { name: /^Restore:/ }).click();
  await expect(problems.getByRole('listitem')).toHaveCount(before);
});

test('a snooze can cover every problem of the same kind, not just the one', async ({ page }) => {
  const problems = await openProblems(page);
  const before = await problems.getByRole('listitem').count();

  const entry = problems.getByRole('listitem').filter({ hasText: 'authentication is disabled' });
  await entry.getByRole('button', { name: /^Dismiss or snooze:/ }).click();
  // Both scopes, for every duration: a planned outage that makes every pool
  // short of capacity should be put away once, not once per pool.
  await expect(page.getByRole('menuitem', { name: 'Snooze for 1 hour' })).toBeVisible();
  await page.getByRole('menuitem', { name: 'Snooze all like this for 1 hour' }).click();

  await expect(problems.getByRole('listitem')).toHaveCount(before - 1);
  await problems.getByRole('button', { name: /Show 1 dismissed problem/ }).click();
  const snoozed = problems.getByRole('listitem').filter({ hasText: 'authentication is disabled' });
  await expect(snoozed).toContainText('snoozed with all like it');
  await expect(snoozed).toContainText(/back in/i);

  await snoozed.getByRole('button', { name: /^Restore:/ }).click();
  await expect(problems.getByRole('listitem')).toHaveCount(before);
});

test('dismissing the last problem closes the drawer', async ({ page }) => {
  const problems = await openProblems(page);

  // "Dismiss all" clears every active entry in one action, so there is
  // nothing left the drawer needs to keep the operator looking at.
  await problems.getByRole('button', { name: 'Dismiss all' }).click();
  await expect(page.getByRole('dialog', { name: 'Problems' })).not.toBeVisible();
});

test('per-pool utilisation shows both pools and marks the one at its ceiling', async ({ page }) => {
  const pools = page.getByRole('region', { name: 'Pools' });
  await expect(pools).toBeVisible();
  await expect(pools.getByRole('link', { name: FIXTURE.linuxPool })).toBeVisible();
  await expect(pools.getByRole('link', { name: FIXTURE.armPool })).toBeVisible();

  const row = pools.getByRole('listitem').filter({ hasText: FIXTURE.linuxPool });
  // The floor and the ceiling are always on the row, so a full pool can be
  // told from a pool with room.
  await expect(row).toContainText('1–8 runners');

  // zoomies-demo-linux-x64 is seeded with eight live runners against a maximum of
  // eight and a job still queued for it: it cannot grow, and saying so is the
  // one thing this section exists for. It is asserted against the controller's
  // own numbers rather than against the fixture, because after about five
  // minutes the controller gives up on the two runners no agent ever collected
  // and the pool is then genuinely no longer at its ceiling. Either way the
  // page must agree with the server.
  const stats = await page.request.get('/api/v1/stats').then((response) => response.json());
  const pool = (stats.pools ?? []).find(
    (entry: { pool_name?: string }) => entry.pool_name === FIXTURE.linuxPool,
  );
  expect(pool, 'the controller knows this pool').toBeTruthy();
  const atCeiling = pool.max > 0 && pool.live >= pool.max && pool.queued > 0;

  if (atCeiling) {
    await expect(row).toContainText('At its ceiling');
    // Said again in the panel's own header, so it survives a glance.
    await expect(pools).toContainText('at the ceiling');
  } else {
    await expect(row).not.toContainText('At its ceiling');
  }
});

test('the events feed quotes the scheduler verbatim', async ({ page }) => {
  const feed = page.getByRole('region', { name: 'Recent events' });
  await expect(feed).toBeVisible();
  // Paraphrasing the one sentence that explains why a runner exists is how a
  // dashboard stops being trustworthy, so the reason string is matched as the
  // scheduler writes it.
  //
  // The newest three rather than the panel's whole length: the feed carries
  // every kind of news now, so a decision and a runner that failed compete for
  // the same dozen lines, and the seeded decisions older than an hour are
  // legitimately below the fold.
  const response = await page.request.get('/api/v1/scaling-events?limit=3');
  expect(response.ok()).toBe(true);
  const events = (await response.json()).items as Array<{ reason?: string }>;
  expect(events.length).toBeGreaterThan(0);
  for (const event of events) {
    expect(event.reason, "every scaling event carries the scheduler's sentence").toBeTruthy();
    await expect(feed).toContainText(event.reason!);
  }
  await expect(feed.getByRole('listitem').first()).toBeVisible();
});

test('the events feed carries the fleet’s other news, not only the scheduler’s', async ({
  page,
}) => {
  // The panel used to be the scheduler's decisions and nothing else, with how
  // the last jobs ended in a second list across the bottom of the page. Both
  // are this one list now: a job ending is news like anything else.
  //
  // Asserted with the decisions switched off, because a fleet that is scaling
  // writes a line every few minutes and the newest fifteen lines are the
  // panel's whole length: what is being tested is that the other kinds are
  // there at all, not where an hour-old job sits in a list that keeps moving.
  // That the switch works is the Events page's own test.
  await goto(page, '/settings/events', 'Events');
  await page.getByRole('switch', { name: 'Scaling decisions' }).click();
  await goto(page, '/', 'Overview');

  const feed = page.getByRole('region', { name: 'Recent events', exact: true });
  await expect(feed.getByRole('listitem').first()).toBeVisible();
  await expect(feed).not.toContainText('scaled zoomies-demo-');

  // The recent past is where "is CI broken?" gets answered, and it has to be
  // answered without a click: how the job ended, where it went wrong, and
  // whether the fleet or the workflow is to blame.
  await expect(feed, 'the jobs that went well are here too').toContainText('A job succeeded');
  await expect(feed).toContainText('A job failed');
  await expect(feed.getByText(/^at .+ · /).first(), 'a step failure names its step').toBeVisible();
  // And the line the outcomes panel carried: where it came from, and how long
  // it took. The shape rather than a particular repository and branch: the
  // seed hands those out by run, and which runs are among the newest fifteen
  // lines is the seed's business rather than this test's.
  await expect(feed.getByText(/ · acme\/[a-z]+ · [\w./-]+ · \d/).first()).toBeVisible();
  // The fleet owning up to its own failure: the runner died under the job, so
  // the failure is not the workflow's, and GitHub records both the same way.
  await expect(feed).toContainText(/A job.s runner stopped under it/);
});

test('an empty feed says when it is the operator’s own choices that emptied it', async ({
  page,
}) => {
  // The other half of the first-run spec's "blames no filter": here the fleet
  // has plenty to say and every kind is switched off, so the panel is empty
  // for exactly the reason it gives, and says how many lines it is holding
  // back so that "empty" and "silenced" cannot be mistaken for each other.
  await goto(page, '/settings/events', 'Events');
  const switches = page.getByRole('switch');
  const count = await switches.count();
  expect(count, 'the Events page lists the kinds').toBeGreaterThan(0);
  for (let i = 0; i < count; i++) {
    const kind = switches.nth(i);
    if (await kind.isChecked()) await kind.click();
  }
  await goto(page, '/', 'Overview');

  const feed = page.getByRole('region', { name: 'Recent events', exact: true });
  await expect(feed).toContainText('Nothing in the kinds you are watching');
  await expect(feed).toContainText(
    /\d+ events? (is|are) not shown, because 12 kinds of event are switched off for this browser/,
  );
  await expect(feed).not.toContainText('Nothing has happened yet');
});

test('the events feed sits beside the pools and the running jobs, never under them', async ({
  page,
}) => {
  // A fleet with one pool and ten decisions used to show the pools, then a
  // screen of blank space, then the running jobs: the feed set the height of
  // the row it shared with the pools. Now the jobs share the pools' column
  // and the feed is cut to that column's height, scrolling inside itself for
  // the rest. A phone stacks the panels, so there is nothing to assert there.
  const viewport = page.viewportSize();
  test.skip((viewport?.width ?? 0) < 1180, 'the panels stack on a narrow screen');

  const pools = page.getByRole('region', { name: 'Pools', exact: true });
  const jobs = page.getByRole('region', { name: 'Active jobs', exact: true });
  const feed = page.getByRole('region', { name: 'Recent events', exact: true });
  // Both lists load after the page does; measure them full, not as skeletons.
  await expect(jobs.getByRole('listitem').first()).toBeVisible();
  await expect(feed.getByRole('listitem').first()).toBeVisible();

  const poolsBox = await pools.boundingBox();
  const jobsBox = await jobs.boundingBox();
  const feedBox = await feed.boundingBox();
  expect(poolsBox).not.toBeNull();
  expect(jobsBox).not.toBeNull();
  expect(feedBox).not.toBeNull();
  if (!poolsBox || !jobsBox || !feedBox) return;

  // The jobs are under the pools and beside the feed.
  expect(jobsBox.y, 'the jobs come after the pools').toBeGreaterThan(poolsBox.y + poolsBox.height);
  expect(jobsBox.x + jobsBox.width, "the jobs share the pools' column").toBeLessThanOrEqual(
    feedBox.x,
  );
  // The feed ends where that column ends. A pixel of slack for rounding.
  expect(feedBox.y + feedBox.height, 'the feed is no taller than its column').toBeLessThanOrEqual(
    jobsBox.y + jobsBox.height + 1,
  );
  // And the fixture's ten decisions do not all fit in that height, so the
  // ones off the end are reachable by scrolling the panel, not gone.
  const list = feed.getByRole('list');
  const hidden = await list.evaluate(
    (el) => (el.parentElement?.scrollHeight ?? 0) - (el.parentElement?.clientHeight ?? 0),
  );
  expect(hidden, 'the decisions that do not fit are a scroll away').toBeGreaterThan(0);
  await feed.getByRole('listitem').last().scrollIntoViewIfNeeded();
  const scrolled = await list.evaluate((el) => el.parentElement?.scrollTop ?? 0);
  expect(scrolled, 'the panel itself scrolls to reach the oldest').toBeGreaterThan(0);
});

test('refreshing the Overview leaves it saying exactly what it said', async ({ page }) => {
  // The gesture exists for the operator who wants to be sure, so the thing
  // worth protecting is that it is not how the page keeps up: a press must not
  // blank the tiles, re-run their skeletons, or change a single number that
  // the stream had already put there.
  const live = page.getByRole('link', { name: /^Live runners/ });
  await expect(live).toBeVisible();
  const before = await live.textContent();

  const refresh = page.getByRole('button', { name: 'Refresh' });
  await expect(refresh).toBeVisible();
  await refresh.click();

  // Nothing goes behind a skeleton on the way through: the last known truth
  // stays on screen while the reconcile is in flight.
  await expect(live).toBeVisible();
  await expect(refresh).not.toHaveAttribute('aria-busy', 'true');
  await expect(live).toHaveText(before ?? '');
});

/*
 * Whose jobs these are.
 *
 * GitHub reports every job in the repositories an installation covers, so an
 * organisation part-way through a migration -- or one that keeps a hosted or
 * vendor runner for a couple of workflows -- has jobs on this page that no
 * runner here has ever seen. The panels used to list them under "what the
 * fleet is running", which is a claim about somebody else's machine.
 */
test("the job panels are this fleet's work, and say so when they are not", async ({ page }) => {
  const active = page.getByRole('region', { name: 'Active jobs', exact: true });
  const feed = page.getByRole('region', { name: 'Recent events', exact: true });
  await expect(active.getByRole('listitem').first()).toBeVisible();

  // By default both panels are the fleet's own work: the fixture's two vendor
  // jobs, one running and one finished, are not on the page at all.
  await expect(active).toContainText('What this fleet is running at this moment.');
  await expect(active).not.toContainText('blacksmith');
  await expect(
    feed,
    'the feed carries the finished ones, and holds to the same rule',
  ).not.toContainText('blacksmith');
  await expect(active.getByText('Elsewhere')).toHaveCount(0);

  // One switch, shared: flipping it on either panel widens both.
  await active.getByRole('switch', { name: 'Other runners' }).click();

  await expect(active).toContainText('wherever it is running');
  const vendor = active.getByRole('listitem').filter({ hasText: 'blacksmith' });
  await expect(vendor).toHaveCount(1);
  // And it is marked, because the row above it is this fleet's and looks the
  // same otherwise.
  await expect(vendor.getByText('Elsewhere')).toBeVisible();

  // The choice is the operator's, and it survives a reload.
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
  await expect(
    page
      .getByRole('region', { name: 'Active jobs', exact: true })
      .getByRole('switch', { name: 'Other runners' }),
  ).toBeChecked();

  // Off again, and the page is the fleet's own once more.
  await page
    .getByRole('region', { name: 'Active jobs', exact: true })
    .getByRole('switch', { name: 'Other runners' })
    .click();
  await expect(page.getByRole('region', { name: 'Active jobs', exact: true })).not.toContainText(
    'blacksmith',
  );
});

test('the subtitle names the window the figures actually cover', async ({ page }) => {
  // The page used to promise that "trends and waits cover the last hour". The
  // trends do; the waits and the outcomes are computed over the controller's
  // stats window, a day by default, so the sentence described a figure the page
  // was not showing. It comes from the payload now, which is the only way it
  // stays true when the window is not the default.
  const stats = (await page.request.get('/api/v1/stats').then((r) => r.json())) as Record<
    string,
    unknown
  >;
  await page.route('**/api/v1/stats*', (route) =>
    route.fulfill({
      status: 200,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ ...stats, window: '6h0m0s' }),
    }),
  );
  // And no stream, so the controller's own window cannot arrive and overwrite
  // the one under test a second later.
  await page.route('**/api/v1/events*', (route) =>
    route.fulfill({
      status: 200,
      headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
      body: '',
    }),
  );

  await goto(page, '/', 'Overview');
  const subtitle = page.locator('.subtitle').first();
  await expect(subtitle).toContainText('the last 6 hours');
  await expect(subtitle, 'the sparklines are still an hour, and say so separately').toContainText(
    'Trends cover the last hour',
  );
});

/*
 * The activity matrix: a year of the fleet's days as one band of squares at
 * the top of the page. What it protects is that the band is data and not
 * decoration -- every square says its figures in words, the keyboard can
 * walk it, and selecting a day leads to that day's jobs -- and that it sits
 * above the numbers, where the operator asked for it.
 */
test('the activity matrix heads the page, and every square says what it holds', async ({
  page,
}) => {
  await yearOfDays(page);
  const matrix = page.getByRole('region', { name: 'Activity matrix', exact: true });
  await expect(matrix).toBeVisible();
  const grid = matrix.getByRole('grid');
  await expect(grid).toHaveAccessibleName(/weeks of this fleet's jobs, one square per day/);

  // Above the metric tiles, on every width.
  const tile = page.getByRole('link', { name: /^Queued jobs/ });
  const matrixBox = await matrix.boundingBox();
  const tileBox = await tile.boundingBox();
  expect(matrixBox).not.toBeNull();
  expect(tileBox).not.toBeNull();
  expect(matrixBox!.y, 'the matrix comes before the tiles').toBeLessThan(tileBox!.y);

  // The seed's morning of jobs is in the newest squares. A square's
  // accessible name is the whole sentence the tooltip shows, so nothing
  // lives only in the tooltip.
  const squares = grid.getByRole('gridcell');
  expect(await squares.count()).toBeGreaterThan(7);
  const busy = grid.locator(
    '[role="gridcell"][data-kind="failing"], [role="gridcell"][data-kind="healthy"]',
  );
  await expect(busy.first()).toBeVisible();
  await expect(busy.last()).toHaveAttribute('aria-label', /\d+ jobs? finished.*succeeded.*failed/);

  // The header sums the squares on screen, and the seed has failures.
  await expect(matrix).toContainText(/\d+ jobs? finished/);
  await expect(matrix.locator('.badge')).toHaveText(/^\s*\d+ failed$/);

  // The band never widens the page: it is cut to the width it has.
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('the matrix is one tab stop, walked with the arrow keys, with a tooltip on every square', async ({
  page,
}) => {
  await yearOfDays(page);
  const matrix = page.getByRole('region', { name: 'Activity matrix', exact: true });
  const grid = matrix.getByRole('grid');
  await expect(grid).toBeVisible();

  // Exactly one square is in the tab order, and it is the newest with jobs.
  const stop = grid.locator('[role="gridcell"][tabindex="0"]');
  await expect(stop).toHaveCount(1);
  await expect(stop).toHaveAttribute('aria-label', WITH_JOBS);

  await stop.focus();
  const tip = page.locator('.tip:popover-open');
  await expect(tip, 'focus opens the tooltip').toBeVisible();
  await expect(tip).toContainText(WITH_JOBS);
  await expect(tip).toContainText('Queued');
  await expect(tip).toContainText('Executing');

  // Left is the same weekday a week earlier; the tooltip follows the focus.
  const before = await stop.getAttribute('aria-label');
  await page.keyboard.press('ArrowLeft');
  const focused = grid.locator('[role="gridcell"]:focus');
  await expect(focused).toHaveCount(1);
  expect(await focused.getAttribute('aria-label')).not.toBe(before);
  await expect(page.locator('.tip:popover-open')).toContainText(
    (await focused.getAttribute('aria-label'))!.split('.')[0]!,
  );
  // And back, so the rest of the test is about today.
  await page.keyboard.press('ArrowRight');
  await expect(grid.locator('[role="gridcell"]:focus')).toHaveAttribute('aria-label', before!);

  // Escape lets go of the tooltip and nothing else: the page is still here.
  await page.keyboard.press('Escape');
  await expect(page.locator('.tip:popover-open')).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
});

test('selecting a day opens its hours and leads to its jobs', async ({ page }) => {
  await yearOfDays(page);
  const matrix = page.getByRole('region', { name: 'Activity matrix', exact: true });
  const grid = matrix.getByRole('grid');
  // The newest day that finished something and failed some of it, which is
  // what every assertion below needs: the outcome split, the hour-by-hour
  // summary and the "Failed jobs" link are all rendered from completions. It
  // is the tab stop itself for all but the twenty minutes after midnight --
  // in those the newest day is all queue, the demo fleet's most recent
  // finished job having landed on yesterday -- so a day picked by the clock
  // rather than by its contents would make this test a nightly flake.
  const failing = grid.locator('[role="gridcell"][data-kind="failing"]');
  await expect(failing.first()).toBeAttached();
  const newest = await failing.evaluateAll((cells) =>
    Math.max(...cells.map((c) => Number(c.getAttribute('data-index')))),
  );
  const day = grid.locator(`[role="gridcell"][data-index="${newest}"]`);
  await expect(day).toHaveAttribute('aria-label', WITH_JOBS);

  await day.click();
  await expect(day).toHaveAttribute('aria-selected', 'true');
  const detail = matrix.getByRole('region', { name: 'Selected day' });
  await expect(detail).toBeVisible();
  await expect(detail).toContainText(WITH_JOBS);
  await expect(detail).toContainText('Succeeded');
  await expect(detail).toContainText('Failed');

  // The hour-by-hour breakdown is fetched on demand and described in words.
  const hours = detail.getByRole('img', { name: /^Hour by hour:/ });
  await expect(hours).toBeVisible();
  // The clock is the browser's own, so it may carry a meridiem.
  await expect(hours).toHaveAccessibleName(/busiest at \d\d:\d\d( [AP]M)? with \d+ finished/);

  // The links are cut to the day, and the Jobs page honours them.
  const jobs = detail.getByRole('link', { name: 'Jobs that day' });
  await expect(jobs).toHaveAttribute(
    'href',
    /^\/jobs\?since=\d{4}-\d\d-\d\d&until=\d{4}-\d\d-\d\d$/,
  );
  await expect(detail.getByRole('link', { name: 'Failed jobs' })).toHaveAttribute(
    'href',
    /^\/jobs\?failed=true&since=/,
  );
  await expect(detail.getByRole('link', { name: 'Usage report for the day' })).toHaveAttribute(
    'href',
    /^\/usage\?since=\d{4}-\d\d-\d\d&until=\d{4}-\d\d-\d\d$/,
  );

  // Selecting the same square again lets go of it.
  await day.click();
  await expect(day).toHaveAttribute('aria-selected', 'false');
  await expect(matrix.getByRole('region', { name: 'Selected day' })).toHaveCount(0);

  await day.click();
  await jobs.click();
  await expect(page.getByRole('heading', { name: 'Jobs' })).toBeVisible();
  await expect(page.locator('tbody tr[data-row]').first()).toBeVisible();
  await expect(page).toHaveURL(/since=/);
});

test('the matrix can be coloured by a different figure', async ({ page }) => {
  const matrix = page.getByRole('region', { name: 'Activity matrix', exact: true });
  // The demo fleet's jobs are written once, when the controller starts, over
  // the six hours before it. A suite that starts before midnight and gets here
  // after it finds today's squares empty of anything queued, so this asks for
  // a window that holds those hours whatever the clock says. The test is about
  // colouring, not about today.
  const week = matrix
    .getByRole('group', { name: 'Range' })
    .getByRole('button', { name: 'The last 7 days, by the hour' });
  await week.click();
  await expect(week).toHaveAttribute('aria-pressed', 'true');
  await expect(matrix.getByRole('grid').getByRole('gridcell')).toHaveCount(7 * 24);
  await expect(matrix).toContainText('Failures, few');

  const colour = matrix.getByLabel('Colour the matrix by');
  await colour.selectOption('queue');
  await expect(matrix.locator('.matrix')).toHaveAttribute('data-mode', 'queue');
  await expect(matrix).toContainText('Fewer queued');
  await expect(matrix).not.toContainText('Failures, few');
  // The squares are the same squares, now saying how many were queued.
  await expect(
    matrix.locator('[role="gridcell"][data-tone="pending"][data-kind="active"]').first(),
  ).toBeVisible();

  await colour.selectOption('runtime');
  await expect(matrix).toContainText('runner time');
  await expect(
    matrix.locator('[role="gridcell"][data-tone="busy"][data-kind="active"]').first(),
  ).toBeVisible();
});

test('the matrix has quick ranges, and remembers the one chosen', async ({ page }) => {
  const matrix = page.getByRole('region', { name: 'Activity matrix', exact: true });
  const ranges = matrix.getByRole('group', { name: 'Range' });
  // Today alone, until an operator asks for more: one row of twenty-four.
  await expect(ranges.getByRole('button', { name: 'Today, by the hour' })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
  const grid = matrix.getByRole('grid');
  await expect(grid.getByRole('gridcell')).toHaveCount(24);

  // A week by the hour: seven rows of twenty-four squares, the newest one
  // with jobs being the tab stop, and a square is an hour.
  await ranges.getByRole('button', { name: 'The last 7 days, by the hour' }).click();
  await expect(grid).toHaveAccessibleName(/^7 days of this fleet's jobs, one square per hour$/);
  await expect(grid.getByRole('gridcell')).toHaveCount(7 * 24);
  // The newest hour with anything in it may be one where jobs are still
  // queued and nothing has finished yet; either way it is an hour.
  await expect(grid.locator('[role="gridcell"][tabindex="0"]')).toHaveAttribute(
    'aria-label',
    /, \d\d?:\d\d( [AP]M)? to \d\d?:\d\d( [AP]M)?\. \d+ jobs? (finished|queued)/,
  );

  // The choice is the operator's, and survives a reload.
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
  const again = page.getByRole('region', { name: 'Activity matrix', exact: true });
  await expect(again.getByRole('button', { name: 'The last 7 days, by the hour' })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
  await expect(again.getByRole('grid').getByRole('gridcell')).toHaveCount(7 * 24);
});

/**
 * The band spends its width.
 *
 * What this protects is the thing an operator sees before they read anything:
 * a band with a stub of squares at the left, a field of white, and the key
 * pushed against the right edge. The grid takes the width it can use and the
 * figures take the rest, so there is no gap between them to explain -- and on
 * a phone that holds too, which is the half of it that was once missing: a
 * week of hours squeezed into ten-pixel squares filled half the screen and
 * left the other half white, whatever the phone was.
 */
test('the matrix spends the width on squares and figures, with the key beneath', async ({
  page,
}) => {
  const matrix = page.getByRole('region', { name: 'Activity matrix', exact: true });
  const band = matrix.locator('.band');
  // The grid itself, not the frame around it: the frame filled the band before
  // any of this, and the squares inside it were the stub.
  const grid = matrix.getByRole('grid');
  const aside = matrix.locator('.aside');
  const key = matrix.locator('.legend');

  // The figures are what the squares on screen come to, so an operator reads
  // the window without hovering every square in it. Read off the week rather
  // than today: the demo fleet's most recent finished job landed twenty
  // minutes before it was seeded, so a suite started in the twenty minutes
  // after midnight has a today with nothing finished in it, and the aside
  // says so instead -- correctly, and for the rest of the run, because no
  // demo job ever completes. The week always has the morning's work in it.
  await matrix
    .getByRole('group', { name: 'Range' })
    .getByRole('button', { name: 'The last 7 days, by the hour' })
    .click();
  await expect(grid).toBeVisible();
  await expect(aside).toContainText('Failure rate');
  await expect(aside).toContainText('Busiest hour');
  await expect(aside).toContainText(/\d[\d,]* jobs? finished/);

  const viewport = page.viewportSize();
  if ((viewport?.width ?? 0) < 900) {
    /*
      A phone is too narrow for both, so one of the two gives way -- and which
      one is the grid's to decide. A week of hours would use every pixel of the
      band, so it takes it and the figures go underneath, and so does a month,
      now that its days are six squares each rather than five week columns of
      one. A few weeks of whole days -- the Usage page's shape -- would stop
      short, and there the figures stay beside it rather than leaving the
      band's own width white. Either way the page never widens.
    */
    for (const [range, spends] of [
      ['The last 7 days, by the hour', true],
      ['The last 30 days', true],
    ] as const) {
      await matrix
        .getByRole('group', { name: 'Range' })
        .getByRole('button', { name: range })
        .click();
      await expect(grid).toBeVisible();
      const [bandBox, gridBox, asideBox] = await Promise.all([
        band.boundingBox(),
        grid.boundingBox(),
        aside.boundingBox(),
      ]);
      if (spends) {
        expect(gridBox!.width, `${range}: the squares take the band`).toBeGreaterThan(
          bandBox!.width - 16,
        );
        expect(asideBox!.y, `${range}: the figures are under them`).toBeGreaterThan(gridBox!.y);
      } else {
        expect(asideBox!.x, `${range}: the figures are beside the squares`).toBeGreaterThan(
          gridBox!.x + gridBox!.width - 1,
        );
        expect(
          gridBox!.width + asideBox!.width,
          `${range}: and the two of them are the band`,
        ).toBeGreaterThan(bandBox!.width - 32);
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );
    }
    return;
  }

  // A day of hours has more columns than rows, so its squares grow into the
  // room, and so does a month of days cut into squares. Nothing is left over:
  // the two of them, and the one gap between, are the band.
  for (const range of ['Today, by the hour', 'The last 30 days', 'The last 90 days']) {
    await matrix.getByRole('group', { name: 'Range' }).getByRole('button', { name: range }).click();
    await expect(grid).toBeVisible();
    const [bandBox, gridBox, asideBox, keyBox] = await Promise.all([
      band.boundingBox(),
      grid.boundingBox(),
      aside.boundingBox(),
      key.boundingBox(),
    ]);
    const spent = gridBox!.width + asideBox!.width;
    expect(spent, `${range}: the squares and the figures fill the band`).toBeGreaterThan(
      bandBox!.width - 48,
    );
    // The key explains the squares, so it runs under them rather than sitting
    // in a column of its own with the white space in front of it.
    expect(keyBox!.y, `${range}: the key is beneath`).toBeGreaterThanOrEqual(
      gridBox!.y + gridBox!.height - 1,
    );
    expect(keyBox!.x, `${range}: the key starts under the grid`).toBeLessThan(gridBox!.x + 8);
  }
});

/**
 * The longer ranges fill the band with squares.
 *
 * A month of whole days was five week columns in the corner of a band with
 * the rest of it white, and a year was cut to fit a phone -- six months of it,
 * under a button that says a year. A month and a quarter now cut every day
 * into squares, and every range is drawn whole with its square sized to the
 * width, so what the button names is what is on screen.
 */
test('a month and a quarter cut their days into squares, and a year is twelve months', async ({
  page,
}) => {
  const matrix = page.getByRole('region', { name: 'Activity matrix', exact: true });
  const ranges = matrix.getByRole('group', { name: 'Range' });
  const grid = matrix.getByRole('grid');
  const frame = matrix.locator('.frame');

  for (const [range, name, squares] of [
    [
      'The last 30 days, in four-hour squares',
      /^\d weeks of this fleet's jobs, one square per 4 hours$/,
      30 * 6,
    ],
    [
      'The last 90 days, in eight-hour squares',
      /^\d+ weeks of this fleet's jobs, one square per 8 hours$/,
      90 * 3,
    ],
    ['The last year', /^52 weeks of this fleet's jobs, one square per day$/, null],
  ] as const) {
    await ranges.getByRole('button', { name: range }).click();
    await expect(grid).toHaveAccessibleName(name);
    if (squares !== null) await expect(grid.getByRole('gridcell')).toHaveCount(squares);
    // Every square of it on screen, on every width: the frame does not
    // scroll to reach the newest, and the page does not widen.
    const fits = await frame.evaluate((el) => el.scrollWidth <= el.clientWidth + 1);
    expect(fits, `${range}: the whole window fits the band`).toBe(true);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
  }

  // Selecting a slice of a day opens that day's hours, and says what it is.
  await ranges.getByRole('button', { name: 'The last 30 days, in four-hour squares' }).click();
  const stop = grid.locator('[role="gridcell"][tabindex="0"]');
  await expect(stop).toHaveAttribute(
    'aria-label',
    /^\w+,? .+, \d\d?:\d\d( [AP]M)? to \d\d?:\d\d( [AP]M)?\. \d+ jobs? (finished|queued)/,
  );
  await stop.click();
  const detail = matrix.getByRole('region', { name: 'Selected hours' });
  await expect(detail).toBeVisible();
  await expect(detail.getByRole('img', { name: /^Hour by hour:/ })).toBeVisible();
  await expect(detail.getByRole('link', { name: 'Jobs that day' })).toBeVisible();
});
