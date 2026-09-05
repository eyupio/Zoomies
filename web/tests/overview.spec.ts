/**
 * The Overview.
 *
 * This is the page an operator leaves open on a second monitor, so what it
 * protects is the promise that looking at it is enough: four numbers with an
 * hour of shape behind them, everything that needs a person listed with the
 * fix beside it, which pools cannot grow, and the scheduler's own words for
 * why runners appeared. It also protects the absence of a refresh button --
 * "you never have to press anything" is a product promise, and a refresh
 * button anywhere on this page would break it.
 */
import { expect, test } from '@playwright/test';
import { browserOverride, FIXTURE, goto } from './support/fixtures';

test.use(browserOverride);

test.beforeEach(async ({ page }) => {
  await goto(page, '/', 'Overview');
});

/** A count. The tile's value paragraph is the only text that is only digits. */
const COUNT = /^\d[\d,]*$/;
/** What formatDuration produces: "820ms", "9.4s", "35m 00s", "2h 05m", or "--". */
const DURATION = /^(--|[\d.]+(ms|s)|\d+[mhd] \d{2}[smh])$/;

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

  // And on a desktop the panels an operator actually watches are above the fold
  // with it, which is exactly what the full-height list used to cost. A phone
  // stacks everything and cannot promise this, so it is not asserted there.
  const viewport = page.viewportSize();
  if ((viewport?.width ?? 0) >= 1180) {
    const box = await page.getByRole('region', { name: 'Recent scaling' }).boundingBox();
    expect(box, 'the scaling feed is laid out').not.toBeNull();
    expect(box?.y ?? Infinity, 'the scaling feed starts within the first screen').toBeLessThan(
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
  await expect(problems).toContainText('queued job(s) match no enabled pool');

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
  await entry.getByRole('button', { name: /^Dismiss:/ }).click();

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

test('the scaling feed quotes the scheduler verbatim', async ({ page }) => {
  const feed = page.getByRole('region', { name: 'Recent scaling' });
  await expect(feed).toBeVisible();
  // Paraphrasing the one sentence that explains why a runner exists is how a
  // dashboard stops being trustworthy, so the reason string is matched as the
  // scheduler writes it.
  await expect(feed).toContainText(/scaled zoomies-demo-linux-x64 \d+ -> \d+: /);
  await expect(feed).toContainText('scaled zoomies-demo-linux-x64 1 -> 4: 3 jobs queued > 30s');
  await expect(feed).toContainText('scaled zoomies-demo-linux-arm64 0 -> 1: 1 job queued > 30s');
  await expect(feed.getByRole('listitem').first()).toBeVisible();
});

test('the scaling feed sits beside the pools and the running jobs, never under them', async ({
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
  const feed = page.getByRole('region', { name: 'Recent scaling', exact: true });
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

test('there is no refresh button anywhere on the Overview', async ({ page }) => {
  // The page is fed by one SSE connection; nothing on it can be refreshed by
  // hand, and offering the gesture would suggest the numbers are stale.
  const refreshy = /refresh|reload|update now|check again|fetch/i;
  await expect(page.getByRole('button', { name: refreshy })).toHaveCount(0);
  await expect(page.getByRole('link', { name: refreshy })).toHaveCount(0);
  await expect(page.locator('[title*="efresh" i]')).toHaveCount(0);
});

/*
 * The recent past is where "is CI broken?" gets answered, and the panel has to
 * do it without a click: the outcome, where it went wrong, and whether the
 * fleet or the workflow is to blame.
 */
test('recent outcomes name the failures and blame the right party', async ({ page }) => {
  const outcomes = page.getByRole('region', { name: 'Recent outcomes' });
  await expect(outcomes).toBeVisible();
  const rows = outcomes.getByRole('listitem');
  await expect(rows.first()).toBeVisible();

  // A step failure says the step; a lost runner says so, and is badged.
  await expect(outcomes.getByText(/^at /).first()).toBeVisible();
  await expect(outcomes).toContainText('Success');
  await expect(outcomes.getByRole('link', { name: 'Every failed job' })).toHaveAttribute(
    'href',
    '/jobs?failed=true',
  );

  // Running jobs stay in their own panel beside it.
  await expect(page.getByRole('region', { name: 'Active jobs' })).toBeVisible();
});
