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

test('the problems panel names the seeded faults, with a fix for each', async ({ page }) => {
  const problems = page.getByRole('region', { name: 'Problems' });
  await expect(problems).toBeVisible();

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

test('per-pool utilisation shows both pools and marks the one at its ceiling', async ({ page }) => {
  const pools = page.getByRole('region', { name: 'Pools' });
  await expect(pools).toBeVisible();
  await expect(pools.getByRole('link', { name: FIXTURE.linuxPool })).toBeVisible();
  await expect(pools.getByRole('link', { name: FIXTURE.armPool })).toBeVisible();

  const row = pools.getByRole('listitem').filter({ hasText: FIXTURE.linuxPool });
  // The floor and the ceiling are always on the row, so a full pool can be
  // told from a pool with room.
  await expect(row).toContainText('1–8 runners');

  // demo-linux-x64 is seeded with eight live runners against a maximum of
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
  await expect(feed).toContainText(/scaled demo-linux-x64 \d+ -> \d+: /);
  await expect(feed).toContainText('scaled demo-linux-x64 1 -> 4: 3 jobs queued > 30s');
  await expect(feed).toContainText('scaled demo-linux-arm64 0 -> 1: 1 job queued > 30s');
  await expect(feed.getByRole('listitem').first()).toBeVisible();
});

test('there is no refresh button anywhere on the Overview', async ({ page }) => {
  // The page is fed by one SSE connection; nothing on it can be refreshed by
  // hand, and offering the gesture would suggest the numbers are stale.
  const refreshy = /refresh|reload|update now|check again|fetch/i;
  await expect(page.getByRole('button', { name: refreshy })).toHaveCount(0);
  await expect(page.getByRole('link', { name: refreshy })).toHaveCount(0);
  await expect(page.locator('[title*="efresh" i]')).toHaveCount(0);
});
