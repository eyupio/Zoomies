import { expect, test } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);

test('runner queue context uses fleet totals and links to provisioning controls', async ({
  page,
}, testInfo) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await goto(page, '/runners', 'Runners');
  const stats = await (await page.request.get('/api/v1/stats')).json();
  const context = page.getByRole('region', { name: 'Runner operational context' });
  await expect(
    context.getByRole('link', {
      name: new RegExp(`Job queue depth: ${stats.fleet.queued_jobs}\\.`),
    }),
  ).toBeVisible();
  await expect(context.getByRole('link', { name: 'Manage queue ↗' })).toHaveAttribute(
    'href',
    '/queue',
  );
  await page.getByText('Explore fleet activity over the last 24 hours', { exact: true }).click();
  await expect(page.getByRole('slider', { name: 'Inspect a moment' })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('fleet-history.png'), fullPage: true });
  expect(errors).toEqual([]);
});

/*
 * A card's title is one size wherever the card is drawn. The Overview's Activity
 * matrix is a Panel and the Usage page's is a chart panel; they used to give
 * the same words two sizes, and the Usage page put a 16px card above two 13px
 * ones. docs/ui-guidelines.md gives card titles one size, and chart panels are
 * cards.
 */
test('a chart panel titles itself at the size every other card does', async ({ page }) => {
  const title = (name: string) => page.getByRole('heading', { level: 2, name, exact: true });
  const measure = (name: string) =>
    title(name)
      .first()
      .evaluate((h) => {
        const style = getComputedStyle(h);
        return { size: style.fontSize, line: style.lineHeight, colour: style.color };
      });

  await goto(page, '/', 'Overview');
  await expect(title('Activity matrix').first()).toBeVisible();
  const panel = await measure('Activity matrix');

  await goto(page, '/usage', 'Usage');
  await expect(title('Activity matrix').first()).toBeVisible();
  expect(await measure('Activity matrix')).toEqual(panel);
});

/*
 * Fleet activity draws every figure at once. It used to draw one of them,
 * chosen from a dropdown, which made the question the panel exists to answer
 * -- was anything waiting, and was there anything free to take it -- two
 * separate looks at two separate charts.
 */
test('fleet activity draws the chosen figures together and reads a moment across them', async ({
  page,
}) => {
  await goto(page, '/runners', 'Runners');
  await page.getByText('Explore fleet activity over the last 24 hours', { exact: true }).click();
  const trend = page.getByRole('region', { name: 'Fleet activity', exact: true });
  const figures = trend.getByRole('group', { name: 'Figures shown' });
  const rows = trend.getByRole('group', { name: 'Figures read at this moment' });

  // Demand and both halves of the answer to it are on the chart to begin
  // with; the other two figures are a chip away.
  await expect(figures.getByRole('button', { name: 'Queued jobs' })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
  await expect(figures.getByRole('button', { name: 'Live runners' })).toHaveAttribute(
    'aria-pressed',
    'false',
  );
  await expect(rows.getByRole('button')).toHaveText([
    'Queued jobs',
    'Running jobs',
    'Idle runners',
  ]);

  // The day is the window it opens on, and a wider window folds the minutes
  // into intervals that carry their peak -- each figure its own -- and says
  // so, so a day of samples is ninety-six points rather than a smear.
  await expect(
    trend.getByRole('img', {
      name: /Fleet activity: queued jobs, running jobs, idle runners; \d+ observed 15-minute intervals/,
    }),
  ).toBeVisible();
  await trend.getByRole('button', { name: 'The last 6 hours, in 5-minute peaks' }).click();
  await expect(trend.getByText(/5-minute intervals observed/)).toBeVisible();
  await trend.getByRole('button', { name: 'The last hour, minute by minute' }).click();
  await expect(trend.getByText(/minutes observed · gaps mean no sample/)).toBeVisible();

  // A chip adds its line and its row; switching one off takes both away.
  await figures.getByRole('button', { name: 'Live runners' }).click();
  await expect(rows.getByRole('button')).toHaveCount(4);
  await expect(
    trend.getByRole('img', { name: /Fleet activity:.*, live runners; \d+ observed minutes/ }),
  ).toBeVisible();
  await figures.getByRole('button', { name: 'Live runners' }).click();
  await expect(rows.getByRole('button')).toHaveCount(3);

  // The rows are the reading as well as the legend: the figures they show
  // are the ones at the chosen moment, so the exact numbers are never only
  // in a card that follows the pointer.
  const now = await rows.locator('.row').first().innerText();
  const slider = trend.getByRole('slider', { name: 'Inspect a moment' });
  await slider.fill('0');
  await expect(trend.getByRole('button', { name: 'Back to now' })).toBeVisible();
  await expect(slider).toHaveAttribute(
    'aria-valuetext',
    /Queued jobs \d+, Running jobs \d+, Idle runners \d+/,
  );
  // The whole hour is seeded, so the oldest minute is a reading and not a gap.
  await expect(rows.locator('.row').first()).not.toHaveText(now);
  await trend.getByRole('button', { name: 'Back to now' }).click();
  await expect(trend.getByRole('button', { name: 'Back to now' })).toBeHidden();
  await expect(rows.locator('.row').first()).toHaveText(now);
});

/*
 * The headline says what an operator would otherwise have to hunt for: the
 * peak in view, and how much of the window the fleet spent with work waiting
 * and nothing idle to take it. The seeded hour has a burst in the middle
 * where exactly that happens, so both halves have something to say.
 */
test('fleet activity names the peak in view and the spells with nothing free', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  await page.getByText('Explore fleet activity over the last 24 hours', { exact: true }).click();
  const trend = page.getByRole('region', { name: 'Fleet activity', exact: true });
  await trend.getByRole('button', { name: 'The last hour, minute by minute' }).click();

  // The headline is one sentence in two halves and has no role of its own, so
  // the class is how it is reached.
  const headline = trend.locator('p.headline');
  await expect(headline).toContainText(/Peak in view: \d+ queued jobs, at \d+:\d+/);
  const said = Number(
    /(\d+) of \d+ minutes with jobs queued and nothing idle/.exec(await headline.innerText())?.[1],
  );
  expect(said, 'the seeded burst runs the fleet out of idle runners').toBeGreaterThan(0);

  // And the same spells are marked on the coverage strip under the chart, so
  // the picture and the sentence cannot disagree.
  await expect(trend.locator('.coverage .starved')).toHaveCount(said);

  // The choice of window and figures is remembered, so an operator who came
  // back to a page does not set it up again.
  await page.reload();
  await page.getByText('Explore fleet activity over the last 24 hours', { exact: true }).click();
  await expect(
    trend.getByRole('button', { name: 'The last hour, minute by minute' }),
  ).toHaveAttribute('aria-pressed', 'true');
});

/*
 * The runner lifecycle is the store's own state machine, drawn: the five
 * steps in the order the controller allows, each with its count and a way in,
 * and the segments of the bar under it are ways in too.
 */
test('the runner lifecycle draws the state machine with live counts and links', async ({
  page,
}) => {
  await goto(page, '/runners', 'Runners');
  const lifecycle = page.getByRole('region', { name: 'Runner lifecycle', exact: true });
  const steps = lifecycle.getByRole('list', { name: 'Runner lifecycle, in order' });
  await expect(steps.getByRole('listitem')).toHaveCount(5);
  await expect(steps.getByRole('link')).toHaveText([
    /^\s*Provisioning\s*\d+$/,
    /^\s*Registering\s*\d+$/,
    /^\s*Idle\s*\d+$/,
    /^\s*Busy\s*\d+$/,
    /^\s*Draining\s*\d+$/,
  ]);
  await expect(steps.getByRole('link', { name: 'Busy runners' })).toHaveAttribute(
    'href',
    '/runners?state=busy',
  );

  // The counts are the controller's, not a partial page of runners.
  const stats = await page.request.get('/api/v1/stats').then((r) => r.json());
  await expect(steps.getByRole('link', { name: 'Busy runners' })).toContainText(
    String(stats.runners.busy),
  );

  // Every segment of the bar is a link with a name, and hovering one says
  // the share out loud for sighted readers.
  const bar = lifecycle.locator('.bar');
  const busy = bar.getByRole('link', { name: 'Busy live runners' });
  await expect(busy).toBeVisible();
  await busy.hover();
  await expect(page.locator('.bubble:popover-open')).toContainText(/Busy.*of \d+ · \d+%/);
  await expect(lifecycle).toContainText(/\d+ live runners? in the flow/);
});

for (const route of [
  { path: '/runners', heading: 'Runners', panel: 'Runner lifecycle' },
  { path: '/pools', heading: 'Pools', panel: 'Pool demand and headroom' },
  { path: '/jobs', heading: 'Jobs', panel: 'Job outcomes' },
  { path: '/hosts', heading: 'Hosts', panel: 'Host capacity map' },
  { path: '/queue', heading: 'Queue', panel: 'Provisioning demand' },
])
  for (const theme of ['light', 'dark'] as const) {
    test(`${route.path} operational visuals fit the ${theme} layout`, async ({
      page,
    }, testInfo) => {
      const errors: string[] = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.addInitScript((value) => localStorage.setItem('zoomies.theme', value), theme);
      await goto(page, route.path, route.heading);
      await expect(page.getByRole('region', { name: route.panel, exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );
      await page.screenshot({
        path: testInfo.outputPath(`${route.path.slice(1)}-${theme}.png`),
        fullPage: true,
      });
      expect(errors).toEqual([]);
    });
  }
