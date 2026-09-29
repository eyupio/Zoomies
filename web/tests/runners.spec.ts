/**
 * The Runners grid.
 *
 * This is where an operator spends an incident, so it protects the things
 * that make a grid trustworthy: the rows say what state each runner is in, a
 * narrowed view is a URL somebody can paste to a colleague and reload, the
 * columns an operator chose stay chosen, the keyboard alone can drive it, and
 * anything destructive names the runner it is about and does nothing at all
 * when you change your mind.
 */
import { expect, test, type Locator, type Page } from '@playwright/test';
import {
  browserOverride,
  cellUnder,
  dataRows,
  FIXTURE,
  goto,
  grid,
  pageHeading,
  reload,
  rowCount,
  waitForRows,
} from './support/fixtures';

test.use(browserOverride);

/**
 * Every state badge the grid can draw, from web/src/lib/status.ts, in the plain
 * words a browser that has not chosen the kennel's vocabulary is shown.
 */
const STATES = ['Provisioning', 'Registering', 'Idle', 'Busy', 'Draining', 'Failed', 'Removed'];

const runners = (page: Page) => grid(page, 'Runners');

/** Wait for the first page of rows rather than for the network to go quiet. */
async function runnerRows(page: Page): Promise<Locator> {
  return waitForRows(runners(page));
}

/** The name in a row, read from the row rather than assumed: rows are sorted. */
async function nameOf(row: Locator): Promise<string> {
  return (await row.getByRole('link').first().innerText()).trim();
}

test('the grid lists the seeded runners with their states', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  const rows = await runnerRows(page);

  // The seed writes twelve runners; the terminal ones are hidden by default,
  // and the scheduler works on the rest as it catches up -- draining the idle
  // ones, giving up on the ones no agent ever collects -- so the count is a
  // floor rather than an exact number. Three busy, three draining and one idle
  // survive indefinitely, because nothing is there to finish their jobs.
  expect(await rows.count()).toBeGreaterThanOrEqual(6);
  await expect(rowCount(page)).toContainText(/of \d+\s+runners/);

  const gridLocator = runners(page);
  await expect(await cellUnder(gridLocator, rows.first(), 'Status')).toBeVisible();

  const states = await gridLocator.locator('[data-runner-status] .label').allTextContents();
  expect(states.length).toBe(await rows.count());
  for (const label of states) {
    expect(STATES, `"${label.trim()}" is one of the states the product defines`).toContain(
      label.trim(),
    );
  }

  const busy = rows.filter({ hasText: FIXTURE.busyRunner });
  await expect(busy).toHaveCount(1);
  await expect(busy).toContainText('Busy');
  await expect(busy).toContainText(FIXTURE.linuxPool);
});

test('filtering by state narrows the rows and puts the filter in the URL', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  await runnerRows(page);
  const before = await dataRows(runners(page)).count();

  await page.getByRole('button', { name: 'Any state' }).click();
  await page
    .getByRole('group', { name: 'Filter by runner state' })
    .getByRole('checkbox', { name: 'Busy' })
    .check();
  await page.keyboard.press('Escape');

  await expect(page).toHaveURL(/[?&]state=busy/);
  await expect(page.getByRole('group', { name: 'Filters in effect' })).toContainText('Busy');

  const filtered = dataRows(runners(page));
  await expect(filtered.first()).toBeVisible();
  await expect(filtered).not.toHaveCount(before);
  for (const label of await runners(page)
    .locator('[data-runner-status] .label')
    .allTextContents()) {
    expect(label.trim(), 'every remaining row matches the filter').toBe('Busy');
  }
  const narrowed = await filtered.count();

  // The whole point of keeping filters in the URL: paste it, get the same view.
  await reload(page, 'Runners');
  await expect(page).toHaveURL(/[?&]state=busy/);
  await expect(dataRows(runners(page))).toHaveCount(narrowed);
  await expect(page.getByRole('button', { name: 'Busy', exact: true })).toBeVisible();
});

test('a hidden column stays hidden after a reload', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  await runnerRows(page);
  await expect(runners(page).getByRole('columnheader', { name: 'Host' })).toBeVisible();

  await page.getByRole('button', { name: 'Columns' }).click();
  await page
    .getByRole('group', { name: 'Column layout' })
    .getByRole('checkbox', { name: 'Host' })
    .uncheck();
  await expect(runners(page).getByRole('columnheader', { name: 'Host' })).toHaveCount(0);

  await reload(page, 'Runners');
  await runnerRows(page);
  await expect(runners(page).getByRole('columnheader', { name: 'Host' })).toHaveCount(0);
  // Only that column went: hiding one must not quietly hide others.
  await expect(runners(page).getByRole('columnheader', { name: 'Status' })).toBeVisible();
  await expect(runners(page).getByRole('columnheader', { name: 'Name' })).toBeVisible();
});

/*
 * The column chooser is a panel of checkboxes, not a menu, and closing it with
 * Escape from inside it must not strand the keyboard: the checkbox focus was on
 * is unmounted with the panel, and focus that falls to the document sends the
 * next Tab back to the top of the page. It returns to the button that opened it.
 */
test('escape closes the column chooser and returns focus to its button', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  await runnerRows(page);

  const columns = page.getByRole('button', { name: 'Columns' });
  // A panel of checkboxes, so it is announced as a dialog rather than as a
  // menu whose arrow keys do nothing.
  await expect(columns).toHaveAttribute('aria-haspopup', 'dialog');

  await columns.click();
  const checkbox = page
    .getByRole('group', { name: 'Column layout' })
    .getByRole('checkbox', { name: 'Host' });
  await checkbox.focus();
  await expect(checkbox).toBeFocused();

  await page.keyboard.press('Escape');
  await expect(page.getByRole('group', { name: 'Column layout' })).toHaveCount(0);
  await expect(columns).toBeFocused();
});

test('selecting rows reveals the bulk bar with the number selected', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  const rows = await runnerRows(page);
  const total = await rows.count();

  const bulk = page.getByRole('group', { name: /Actions for the selected/ });
  await expect(bulk).toHaveCount(0);

  await page.getByRole('checkbox', { name: /^Select every/ }).check();
  await expect(bulk).toBeVisible();
  await expect(bulk).toContainText(`${total} selected`);
  await expect(bulk.getByRole('button', { name: 'Drain' })).toBeVisible();
  await expect(bulk.getByRole('button', { name: 'Delete' })).toBeVisible();

  await bulk.getByRole('button', { name: 'Clear selection' }).click();
  await expect(bulk).toHaveCount(0);

  // One row, chosen from the keyboard, is counted the same way.
  await rows.first().focus();
  await page.keyboard.press(' ');
  await expect(bulk).toContainText('1 selected');
  await expect(page).toHaveURL(/\/runners$/);
});

test('clicking a row checkbox selects that row instead of opening the runner', async ({ page }) => {
  // This caught a bug that made mouse selection impossible: every cell holding
  // a control stopped the row's click from reaching the row -- the name cell
  // and the actions cell both did -- except the selection checkbox, so ticking
  // a row navigated to it and selected nothing. The keyboard path (Space on a
  // focused row) was unaffected, which is why the test above passed throughout.

  await goto(page, '/runners', 'Runners');
  const rows = await runnerRows(page);

  await rows.first().getByRole('checkbox').click();

  await expect(page, 'ticking a row must not navigate').toHaveURL(/\/runners$/);
  await expect(page.getByRole('group', { name: /Actions for the selected/ })).toContainText(
    '1 selected',
  );
});

test('draining a runner asks first, names it, and cancelling changes nothing', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  const rows = await runnerRows(page);
  const row = rows.filter({ hasText: FIXTURE.busyRunner }).first();
  const name = await nameOf(row);
  expect(name).toBe(FIXTURE.busyRunner);

  const trigger = page.getByRole('button', { name: `Actions for ${name}` });
  await trigger.click();
  await page.getByRole('menuitem', { name: 'Drain', exact: true }).click();

  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  // Named, and counted: a destructive action must say what it is about.
  await expect(dialog.getByRole('heading', { name: 'Drain runner' })).toBeVisible();
  await expect(dialog).toContainText(name);
  // This expectation moved deliberately. The dialog used to say draining never
  // interrupts work in progress, which was not true: the stop task carries a
  // five-minute timeout and the runner is killed after it. The dialog now says
  // what it costs, and this asserts the honest sentence rather than the
  // comfortable one.
  await expect(dialog).toContainText(
    'The job it is running now has five minutes to finish. If it takes longer, the runner is stopped and GitHub marks that job failed.',
  );

  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();

  // Nothing was asked of the controller, so the runner is exactly as it was.
  await expect(row).toContainText('Busy');
  await expect(page).toHaveURL(/\/runners$/);
  await reload(page, 'Runners');
  await expect(dataRows(runners(page)).filter({ hasText: FIXTURE.busyRunner })).toContainText(
    'Busy',
  );
});

test('opening a runner shows its detail page and its state timeline', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  const rows = await runnerRows(page);
  const row = rows.filter({ hasText: FIXTURE.busyRunner }).first();

  await row.getByRole('link', { name: FIXTURE.busyRunner }).click();

  await expect(pageHeading(page, FIXTURE.busyRunner)).toBeVisible();
  await expect(page).toHaveURL(/\/runners\/run_demo\d+$/);

  const timeline = page.getByRole('region', { name: 'Timeline' });
  await expect(timeline).toBeVisible();
  await expect(timeline.getByRole('listitem').first()).toBeVisible();
  // The states this runner passed through on the way to being busy.
  await expect(timeline).toContainText('Provisioning');
  await expect(timeline).toContainText('Registering');
  await expect(timeline).toContainText('Busy');
  // And the way back to the list is a breadcrumb, not the browser's button.
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('Runners');
});

test('the keyboard alone moves through the rows and opens one', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  const rows = await runnerRows(page);

  await rows.first().focus();
  await expect(rows.first()).toBeFocused();

  await page.keyboard.press('ArrowDown');
  await expect(rows.nth(1)).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await expect(rows.nth(2)).toBeFocused();
  await page.keyboard.press('ArrowUp');
  await expect(rows.nth(1)).toBeFocused();

  const expected = await nameOf(rows.nth(1));
  const href = await rows.nth(1).getByRole('link').first().getAttribute('href');
  expect(href, 'the focused runner has a detail link').toMatch(/^\/runners\/.+/);
  await page.keyboard.press('Enter');
  await expect(pageHeading(page, expected)).toBeVisible();
  await expect(page).toHaveURL(new URL(href!, page.url()).href);
});

// A confirmation dialog owns the keyboard while it is up. `g` then `o` used to
// be answered by the shell underneath it, so an operator half-way through
// confirming a drain could be moved to the Overview with the question still
// open behind them.
test('a shortcut typed into an open dialog stays in the dialog', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  const rows = await runnerRows(page);
  const row = rows.filter({ hasText: FIXTURE.busyRunner }).first();
  const name = await nameOf(row);

  await page.getByRole('button', { name: `Actions for ${name}` }).click();
  await page.getByRole('menuitem', { name: 'Drain', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();

  // Typed at the dialog, not at a field: the chord is the shell's, and the
  // shell must decline it.
  await dialog.getByRole('heading', { name: 'Drain runner' }).click();
  await page.keyboard.press('g');
  await page.keyboard.press('o');
  await expect(dialog).toBeVisible();
  await expect(page).toHaveURL(/\/runners$/);

  // Escape closes it, and only then does the same chord go somewhere.
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await page.keyboard.press('g');
  await page.keyboard.press('o');
  await expect(page).toHaveURL(/\/$/);
});

// Deleting takes the runner out from under the detail page, so there is
// nothing left there to look at. It should return the operator to the grid
// they opened it from -- not a page loaded fresh, which has lost their place
// in the fleet.
test('deleting a runner from its detail page returns to the grid it was opened from', async ({
  page,
}) => {
  await goto(page, '/runners', 'Runners');
  const rows = await runnerRows(page);
  const row = rows.filter({ hasText: FIXTURE.busyRunner }).first();
  const name = await nameOf(row);

  // A click, not a direct load, so the router has somewhere to send the
  // operator back to.
  await row.getByRole('link').first().click();
  await expect(pageHeading(page, name)).toBeVisible();

  // The delete itself is exercised elsewhere against the real controller;
  // this is about what the page does once it is told the request went
  // through, which does not depend on the runner actually being gone.
  await page.route(`**/api/v1/runners/${FIXTURE.busyRunnerId}*`, (route) =>
    route.request().method() === 'DELETE'
      ? route.fulfill({ status: 202, contentType: 'application/json', body: '{}' })
      : route.continue(),
  );

  await page.getByRole('button', { name: 'Delete', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: 'Delete runner' }).click();

  await expect(page).toHaveURL(/\/runners$/);
  await expect(pageHeading(page, 'Runners')).toBeVisible();
});

test('clipboard refusal is visible and does not blame HTTPS on a secure page', async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: () => Promise.reject(new DOMException('Denied', 'NotAllowedError')) },
    });
  });
  await goto(page, `/runners/${FIXTURE.busyRunnerId}`, FIXTURE.busyRunner);
  await page.getByRole('button', { name: 'Copy the runner ID', exact: true }).click();
  await expect(page.getByText('Could not copy to the clipboard', { exact: true })).toBeVisible();
  await expect(
    page.getByText(
      'Your browser did not allow clipboard access. Select the text and copy it manually.',
      { exact: true },
    ),
  ).toBeVisible();
});

/*
 * The heading is read first and believed. "Runner not found" over a panel that
 * says the controller failed sent an operator looking for a runner that was
 * never gone, so only a 404 gets to say it.
 */
test('a runner that could not be loaded is only called not found when the controller says so', async ({
  page,
}) => {
  await page.route(`**/api/v1/runners/${FIXTURE.busyRunnerId}`, (route) =>
    route.fulfill({
      status: 500,
      contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'internal', message: 'The controller broke.' } }),
    }),
  );
  await page.goto(`/runners/${FIXTURE.busyRunnerId}`, { waitUntil: 'domcontentloaded' });
  await expect(page.getByText('That runner could not be loaded')).toBeVisible();
  await expect(pageHeading(page, 'Runner')).toBeVisible();

  await page.goto('/runners/run_never_existed', { waitUntil: 'domcontentloaded' });
  await expect(pageHeading(page, 'Runner not found')).toBeVisible();
});
