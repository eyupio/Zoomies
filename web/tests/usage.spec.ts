/**
 * The Usage page.
 *
 * Runner-hours belong to pools and installations: a runner idles on behalf of
 * a pool, never on behalf of a repository or a workflow. This protects that
 * the page drops those columns and says why when asked to group by repository,
 * rather than printing a column of zeros that read as "this repository used
 * nothing", and that the three job counts are there in its place.
 *
 * It also protects the thing that makes a report worth having: the range and
 * the grouping are in the address bar, so the page an operator is looking at is
 * the page they can send to somebody else.
 */
import { expect, test, type Page } from '@playwright/test';
import { browserOverride, FIXTURE, goto } from './support/fixtures';

test.use(browserOverride);

// The report itself, named by its caption. The page has a second table, "By
// release", and a bare role would match both.
const table = (page: Page) => page.getByRole('table', { name: /^Usage by / });
// Located by element rather than by role: on a phone the heading row is hidden
// and each figure carries its own heading instead, so a columnheader role does
// not resolve there. Whether the column exists at all is the question, and that
// is the same question at both widths.
const header = (page: Page, name: string) =>
  table(page).locator('thead th').filter({ hasText: name });

test('runner-hours are given by pool and dropped, with the reason, by repository', async ({
  page,
}) => {
  await goto(page, '/usage', 'Usage');

  // The default grouping is by pool, and the seeded fleet has had runners, so
  // the attributed columns are there.
  await expect(header(page, 'Runner-hours')).toHaveCount(1);
  await expect(header(page, 'Estimated cost')).toHaveCount(1);
  // Strings rather than regular expressions throughout: Playwright normalises
  // whitespace for strings, and the template wraps these sentences.
  await expect(page.getByText('cannot be attributed at this grouping')).toBeHidden();

  // Runner-hours is the eighth column: the key, then the three job counts,
  // peak, wait and executing time before it.
  const first = table(page).getByRole('row').nth(1);
  await expect(first.getByRole('cell').nth(6)).toHaveText(/^\d+\.\d\d$/);

  await page.getByLabel('Group by').selectOption('repository');

  await expect(page.getByText('never on behalf of a repository')).toBeVisible();
  await expect(header(page, 'Runner-hours')).toHaveCount(0);
  await expect(header(page, 'Estimated cost')).toHaveCount(0);

  // What is left is honest: a row per seeded repository, with the queued,
  // started and completed counts as whole numbers.
  const api = table(page).getByRole('row', { name: new RegExp(FIXTURE.repos[0]) });
  await expect(api).toBeVisible();
  for (const column of [0, 1, 2]) {
    await expect(api.getByRole('cell').nth(column)).toHaveText(/^\d+$/);
  }
});

test('the report is a link: the range and the grouping live in the address bar', async ({
  page,
}) => {
  await goto(page, '/usage?since=2026-08-01&until=2026-08-31&group_by=workflow', 'Usage');

  await expect(page.getByLabel('Group by')).toHaveValue('workflow');
  // Exact: the range's second leg is labelled "to", which is a substring of
  // half the accessible names on the page.
  // A link from before the fields took a time still means whole days.
  await expect(page.getByLabel('From', { exact: true })).toHaveValue('2026-08-01T00:00');
  await expect(page.getByLabel('to', { exact: true })).toHaveValue('2026-08-31T23:59');
  // A range typed by hand is none of the quick ones.
  const quick = page.getByRole('group', { name: 'Quick ranges' });
  await expect(quick.locator('[aria-pressed="true"]')).toHaveCount(0);

  // Changing the grouping rewrites the address rather than needing an Apply
  // button, so a reload lands on the same report.
  await page.getByLabel('Group by').selectOption('pool');
  await expect(page).toHaveURL(/since=2026-08-01/);
  await expect(page).toHaveURL(/until=2026-08-31/);
  // Pool is the default, so it drops out rather than being spelled out.
  await expect(page).not.toHaveURL(/group_by=/);

  // And the CSV carries the same range, so the file is the report on screen.
  const csv = page.getByRole('link', { name: 'Export CSV' });
  await expect(csv).toHaveAttribute('href', /from=2026-08-01T/);
  await expect(csv).toHaveAttribute('href', /to=2026-08-31T/);
});

/**
 * The quick ranges go below a day, as the Hosts page's windows do, and the
 * one in force is pressed -- the default included, so a page opened cold says
 * which range it is showing rather than leaving the operator to read it off
 * the fields.
 */
test('the quick ranges reach below a day, and the one in force is pressed', async ({ page }) => {
  await goto(page, '/usage', 'Usage');
  const quick = page.getByRole('group', { name: 'Quick ranges' });
  await expect(quick.getByRole('button', { name: /^1d/, pressed: true })).toBeVisible();
  await expect(quick.locator('[aria-pressed="true"]')).toHaveCount(1);

  const asked = page.waitForRequest(/\/api\/v1\/usage\?/);
  await quick.getByRole('button', { name: /^6h/ }).click();
  const url = new URL((await asked).url());
  await expect(page).toHaveURL(/range=6h/);
  await expect(quick.getByRole('button', { name: /^6h/, pressed: true })).toBeVisible();
  await expect(quick.getByRole('button', { name: /^1d/, pressed: true })).toHaveCount(0);
  const span =
    new Date(url.searchParams.get('to')!).getTime() -
    new Date(url.searchParams.get('from')!).getTime();
  expect(span, 'six hours, up to now').toBe(6 * 3_600_000);

  // The fields show the preset's bounds to the minute, and a time typed into
  // one lets go of the preset.
  const from = page.getByLabel('From', { exact: true });
  await expect(from).toHaveValue(/T\d\d:\d\d$/);
  await from.fill('2026-08-01T09:30');
  await from.blur();
  await expect(page).toHaveURL(/since=2026-08-01T09%3A30|since=2026-08-01T09:30/);
  await expect(page).not.toHaveURL(/range=/);
  await expect(quick.locator('[aria-pressed="true"]')).toHaveCount(0);
});

test('a backwards range switches the CSV link off for the keyboard too', async ({ page }) => {
  await goto(page, '/usage?since=2026-08-31&until=2026-08-01', 'Usage');

  // No link role at all, which is what an <a> without an href is. The styling
  // says it is off; this is what makes it true for somebody tabbing to it, who
  // a `pointer-events: none` rule does not reach.
  await expect(page.getByRole('link', { name: 'Export CSV' })).toHaveCount(0);
  const off = page.getByText('Export CSV');
  await expect(off).toBeVisible();
  await expect(off.locator('xpath=ancestor::a[1]')).toHaveAttribute('aria-disabled', 'true');
  await expect(off.locator('xpath=ancestor::a[1]')).not.toHaveAttribute('href', /./);

  // And it comes back the moment the range makes sense again.
  await page.getByLabel('From', { exact: true }).fill('2026-08-01T00:00');
  await page.getByLabel('From', { exact: true }).blur();
  await expect(page.getByRole('link', { name: 'Export CSV' })).toHaveCount(1);
});

test('a backwards range says so as an alert, not only through the inputs it describes', async ({
  page,
}) => {
  // A describedby link is read only when a field is revisited, so somebody who
  // has just typed an end before the start would otherwise be told nothing.
  await goto(page, '/usage?since=2026-08-01T00:00&until=2026-08-31T00:00', 'Usage');
  const warning = page.getByRole('alert').filter({ hasText: 'The end is before the start' });
  await expect(warning).toHaveCount(0);

  await page.getByLabel('From', { exact: true }).fill('2026-09-15T00:00');
  await page.getByLabel('From', { exact: true }).blur();
  await expect(warning).toBeVisible();
});

test('changing grouping cancels a pending manual refresh', async ({ page }) => {
  await goto(page, '/usage', 'Usage');
  await expect(header(page, 'Runner-hours')).toHaveCount(1);
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route('**/api/v1/usage?**', async (route) => {
    if (new URL(route.request().url()).searchParams.get('group_by') !== 'pool') {
      await route.continue();
      return;
    }
    await gate;
    await route.abort().catch(() => {});
  });
  const started = page.waitForRequest((request) => request.url().includes('/api/v1/usage?'));
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  const old = await started;
  const cancelled = page.waitForEvent('requestfailed', { predicate: (request) => request === old });
  try {
    await page.getByLabel('Group by').selectOption('repository');
    await cancelled;
    await expect(
      table(page).getByRole('row', { name: new RegExp(FIXTURE.repos[0]) }),
    ).toBeVisible();
    await expect(header(page, 'Runner-hours')).toHaveCount(0);
  } finally {
    release();
  }
});

for (const theme of ['light', 'dark'] as const) {
  test(`date controls follow the explicit ${theme} theme instead of the system preference`, async ({
    page,
  }) => {
    await page.emulateMedia({ colorScheme: theme === 'light' ? 'dark' : 'light' });
    await page.addInitScript((choice) => localStorage.setItem('zoomies.theme', choice), theme);
    await goto(page, '/usage', 'Usage');
    await expect(page.getByLabel('From', { exact: true })).toHaveCSS('color-scheme', theme);
    await expect(page.getByLabel('to', { exact: true })).toHaveCSS('color-scheme', theme);
  });
}

test('analytics supports keyboard inspection, group focus and matching CSV exports', async ({
  page,
}) => {
  await goto(page, '/usage', 'Usage');
  const chart = page.getByRole('region', { name: 'Usage over time', exact: true });
  await expect(chart).toBeVisible();

  // Every figure is a chip and every chip is a line, so the two measures are
  // not two fixed pictures: the chart draws whichever figures are pressed,
  // and says which those are in its accessible name.
  const measures = chart.getByRole('group', { name: 'Measure' });
  await measures.getByRole('button', { name: 'Runner time' }).click();
  await expect(chart.getByRole('img', { name: /executing, allocated/ })).toBeVisible();
  const figures = chart.getByRole('group', { name: 'Figures shown' });
  await figures.getByRole('button', { name: 'Executing' }).click();
  await expect(chart.getByRole('img', { name: /Usage over time: allocated/ })).toBeVisible();
  // The rows below are the same switchboard, and the reading: a figure taken
  // off the chart leaves them too.
  const rows = chart.getByRole('group', { name: 'Figures read at this interval' });
  await expect(rows.getByRole('button', { name: /Executing/ })).toHaveCount(0);
  await expect(rows.getByRole('button', { name: /Allocated/ })).toHaveCount(1);
  await measures.getByRole('button', { name: 'Jobs' }).click();

  const matrix = page.getByRole('region', { name: 'Activity matrix', exact: true });
  // The grid is one tab stop: the square with tabindex 0 is the newest one
  // with anything in it. Focusing it opens the tooltip; Enter selects it and
  // opens the detail, which carries the same figures as text.
  const square = matrix.locator('[role="gridcell"][tabindex="0"]');
  await square.focus();
  await expect(square).toBeFocused();
  await expect(page.locator('.tip:popover-open')).toContainText('Queued');
  await square.press('Enter');
  // The report opens on a day, so a square is an hour and the detail is one.
  await expect(matrix.getByRole('region', { name: 'Selected hour' })).toContainText('Queued');
  // Choosing a square there brings its interval under the chart's crosshair,
  // which is the whole point of the two panels sharing a moment. Letting it
  // go lets go of both.
  const clear = chart.getByRole('button', { name: 'Back to the latest' });
  await expect(clear).toBeVisible();
  await clear.click();
  await expect(matrix.getByRole('region', { name: 'Selected hour' })).toHaveCount(0);
  const ranking = page.getByRole('region', { name: 'Execution by group', exact: true });
  await ranking.getByRole('button').first().click();
  await expect(page).toHaveURL(/entity=/);
  const key = new URL(page.url()).searchParams.get('entity');
  const href = await page.getByRole('link', { name: 'Export CSV' }).getAttribute('href');
  expect(new URL(href!, page.url()).searchParams.get('key')).toBe(key);
  await expect(table(page).locator('tbody tr')).toHaveCount(1);
  await page.getByLabel('Group by').selectOption('host');
  await expect(page).not.toHaveURL(/entity=/);
  await expect(header(page, 'Runner-hours')).toHaveCount(1);
  await expect(
    matrix.getByText('Capacity observations belong to pools.', { exact: false }),
  ).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

/**
 * The Usage page's matrix is drawn the way the Overview draws a window as
 * long, not in the report's own days: a month of days was five columns in
 * the corner of the panel. Its squares are then narrower than the chart's, so
 * choosing one has to find the chart's interval it falls in -- the day, not
 * the square's index -- or the crosshair would land a month out.
 */
test('a month is drawn in four-hour squares, and a square moves the crosshair to its day', async ({
  page,
}) => {
  const day = (d: Date) =>
    `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
  const since = day(new Date(Date.now() - 29 * 86_400_000));
  await goto(page, `/usage?since=${since}`, 'Usage');
  const matrix = page.getByRole('region', { name: 'Activity matrix', exact: true });
  const grid = matrix.getByRole('grid');
  await expect(grid).toHaveAccessibleName(/^\d weeks of every pool, one square per 4 hours$/);
  await expect(grid.getByRole('gridcell')).toHaveCount(30 * 6);
  const fits = await matrix
    .locator('.frame')
    .evaluate((el) => el.scrollWidth <= el.clientWidth + 1);
  expect(fits, 'the whole month fits the panel').toBe(true);

  // A square three days back, in the afternoon: the chart is a day to a
  // point, so its crosshair goes to the 27th of the month's 30 days.
  const chart = page.getByRole('region', { name: 'Usage over time', exact: true });
  const square = grid.locator(`[role="gridcell"][data-index="${27 * 6 + 3}"]`);
  await square.click();
  await expect(matrix.getByRole('region', { name: 'Selected hours' })).toBeVisible();
  await expect(chart.getByRole('slider', { name: 'Inspect an interval' })).toHaveValue('27');
  await chart.getByRole('button', { name: 'Back to the latest' }).click();
  await expect(matrix.getByRole('region', { name: 'Selected hours' })).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('one installation opens its report, with every figure defined in the docs', async ({
  page,
}) => {
  await goto(page, '/usage?group_by=installation', 'Usage');
  // Grouped by installation with none chosen, the page says where the report is.
  await expect(page.getByTestId('installation-report-hint')).toBeVisible();

  await goto(page, `/usage?group_by=installation&entity=${FIXTURE.installationId}`, 'Usage');
  const report = page.getByRole('region', { name: 'Installation report', exact: true });
  await expect(report).toBeVisible();
  for (const figure of ['Observed', 'Eligible', 'Created for', 'Ran here', 'Fleet fault']) {
    await expect(report.getByText(figure, { exact: true })).toBeVisible();
  }
  await expect(
    report.getByRole('rowheader', { name: 'Eligible to first create task' }),
  ).toBeVisible();
  await expect(report.getByRole('link', { name: 'How each figure is defined' })).toHaveAttribute(
    'href',
    /metrics\/#per-installation-report$/,
  );
  // It is per installation, so no repository appears in it.
  for (const repo of FIXTURE.repos) {
    await expect(report.getByText(repo)).toHaveCount(0);
  }

  // Its window is its own, and changing it asks again.
  const asked = page.waitForRequest((r) => r.url().includes('/report?window=168h'));
  await report.getByLabel('Report window').selectOption('168h');
  await asked;
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

/**
 * The "By release" table answers what the report above it cannot: did builds
 * get faster, and did fewer break, after an upgrade. The demo fleet's jobs
 * were run by two releases and its oldest by none that was recorded, so the
 * table has both releases and the row that says "not recorded" -- which must be
 * there rather than dropped, or the newest releases look like the whole history.
 */
test('the usage page compares releases, and keeps the jobs nobody stamped', async ({ page }) => {
  await goto(page, '/usage?range=90d', 'Usage');

  const releases = page.getByRole('region', { name: 'By release' });
  await expect(releases).toBeVisible();
  const byRelease = releases.getByRole('table');
  await expect(byRelease).toBeVisible();

  const newest = byRelease.locator('tr[data-release="v1.3.3"]');
  await expect(newest).toBeVisible();
  await expect(byRelease.locator('tr[data-release="v1.3.2"]')).toBeVisible();

  // The jobs from before the stamp are their own row, said in words.
  const unknown = byRelease.locator('tr[data-release="unknown"]');
  await expect(unknown).toBeVisible();
  await expect(unknown).toContainText('not recorded');

  // Figures, not blanks: a release that ran jobs has a duration and a wait.
  for (const label of ['Jobs', 'Duration p50', 'Duration p95', 'Queue wait p50']) {
    await expect(newest.getByRole('cell').filter({ hasText: /\d/ }).first()).toBeVisible();
    await expect(byRelease.locator('thead th').filter({ hasText: label })).toHaveCount(1);
  }
  await expect(releases).toContainText('cancelled and skipped');
});

test('a range with no jobs says so in the release table rather than showing an empty one', async ({
  page,
}) => {
  await goto(page, '/usage?since=2019-01-01&until=2019-01-02', 'Usage');
  const releases = page.getByRole('region', { name: 'By release' });
  await expect(releases.getByTestId('usage-by-release-empty')).toBeVisible();
  await expect(releases.getByRole('table')).toHaveCount(0);
});

test('a range longer than the server allows names the setting that limits it', async ({ page }) => {
  await goto(page, '/usage?since=2020-01-01&until=2020-12-31', 'Usage');
  await expect(page.getByRole('region', { name: 'By release' }).getByRole('alert')).toContainText(
    'limits.job_stats_window',
  );
});

/**
 * One measure, one spelling. The tile once printed "125.0s" and the table
 * under it "2.1 min" for the same mean, and a twenty-minute wait read "1200.0s"
 * in the tile: two figures a screen apart that made an operator ask whether
 * they were two measurements. Every other duration in the app is written by
 * formatDuration, so both are.
 */
test('the average queue wait is written the same way in the tile and in the table', async ({
  page,
}) => {
  await page.route('**/api/v1/usage?**', async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.items = (body.items ?? []).map((row: object) => ({
      ...row,
      jobs_started: 4,
      average_queue_wait_seconds: 125,
    }));
    await route.fulfill({ json: body });
  });
  await goto(page, '/usage', 'Usage');

  const tile = page.locator('.metric').filter({ hasText: 'Average queue wait' });
  await expect(tile.locator('dd').first()).toHaveText('2m 05s');
  await expect(
    table(page).getByRole('row').nth(1).getByRole('cell').filter({ hasText: '2m 05s' }),
  ).toHaveCount(1);
});

/**
 * "Nothing to show" has one spelling. The tiles above the table used an em dash
 * where the table's own cells (and every formatter) write two hyphens, so a
 * range with no completed jobs disagreed with itself about what empty looks like.
 */
test('a page with no completed jobs marks its missing figures the way the rest of the app does', async ({
  page,
}) => {
  await page.route('**/api/v1/usage?**', async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.items = (body.items ?? []).map((row: object) => ({
      ...row,
      jobs_completed: 0,
      jobs_started: 0,
      average_queue_wait_seconds: null,
    }));
    await route.fulfill({ json: body });
  });
  await goto(page, '/usage', 'Usage');
  const tile = page.locator('.metric').filter({ hasText: 'Success rate' });
  await expect(tile.locator('dd').first()).toHaveText('--');
  await expect(page.locator('.metric').filter({ hasText: 'Average queue wait' })).toContainText(
    '--',
  );
});
