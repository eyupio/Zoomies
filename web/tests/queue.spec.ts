import { expect, test } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);

/** A job's name is not a pattern: it carries slashes and dots of its own. */
function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

test('queue controls persist, filter and restore demand without changing GitHub state', async ({
  page,
  request,
}) => {
  const response = await request.get('/api/v1/provisioning?limit=1&sort=queued_at&order=asc');
  const { items } = await response.json();
  expect(items.length).toBeGreaterThan(0);
  const job = items[0];
  try {
    await goto(page, '/queue', 'Queue');
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await expect(
      page.getByRole('button', { name: 'Select all matching', exact: true }),
    ).toBeEnabled();
    // The row's own actions are buttons, not a menu, so pausing one item is a
    // single press. Each carries what it acts on in its name, which is what
    // keeps it apart from the identically-worded button in the selection bar.
    await page
      .getByRole('button', { name: `Pause: ${job.repo} / ${job.job_name}`, exact: true })
      .first()
      .click();
    await page.getByRole('dialog').getByRole('button', { name: 'Pause', exact: true }).click();
    await expect(page.getByRole('status').filter({ hasText: '1 updated' })).toBeVisible();
    await page.reload();
    await page.getByRole('button', { name: /Paused.*Demand on hold/ }).click();
    await expect(page.getByRole('grid', { name: 'Provisioning queue' })).toContainText(
      job.job_name,
    );
    await page.getByRole('button', { name: 'Select all matching', exact: true }).click();
    await expect(page.getByText('Snapshot across all pages')).toBeVisible();
    await page.getByRole('button', { name: 'Run now', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Run now', exact: true }).click();
    const updated = await (await request.get(`/api/v1/jobs/${job.id}`)).json();
    expect(updated.state).toBe('queued');
    expect(updated.provisioning).toBe('');
    expect(updated.provision_now).toBe(true);
    expect(errors).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
  } finally {
    await request.post('/api/v1/provisioning/bulk', { data: { ids: [job.id], action: 'resume' } });
  }
});

test('a start with a time of day narrows the queue to that minute', async ({ page, request }) => {
  // The queue the page lists by default, from the API, so the expected count is
  // worked out here and not by the helper under test.
  const { items } = (await request
    .get(
      '/api/v1/provisioning?limit=500&sort=queued_at&order=asc&provisioning=ready&provisioning=expedited&provisioning=paused',
    )
    .then((r) => r.json())) as { items: { queued_at: string }[] };

  // The minute of the newest job, so the cut falls inside the queue's span and
  // not at a day boundary: a page that rounded to the day would keep every job.
  const cut = new Date(items[items.length - 1]!.queued_at);
  cut.setSeconds(0, 0);
  const kept = items.filter((job) => Date.parse(job.queued_at) >= cut.getTime()).length;
  expect(kept, 'the cut has to fall inside the queue').toBeGreaterThan(0);
  expect(kept, 'the cut has to leave some out').toBeLessThan(items.length);

  const pad = (n: number) => String(n).padStart(2, '0');
  const local = `${cut.getFullYear()}-${pad(cut.getMonth() + 1)}-${pad(cut.getDate())}T${pad(cut.getHours())}:${pad(cut.getMinutes())}`;
  await goto(page, `/queue?since=${encodeURIComponent(local)}`, 'Queue');
  await expect(page.getByText('matching items')).toContainText(`${kept} matching items`);
  await expect(page.getByRole('group', { name: 'Queued between' }).getByLabel('From')).toHaveValue(
    local,
  );
});

test('queue views preserve combined filters and cancel leaves demand unchanged', async ({
  page,
}) => {
  await goto(page, '/queue?repo=acme%2Fapi&provisioning=ready', 'Queue');
  await page.getByRole('button', { name: 'Save view', exact: true }).click();
  await page.getByRole('textbox', { name: 'View name' }).fill('API ready');
  await page.getByRole('dialog').getByRole('button', { name: 'Save view', exact: true }).click();
  await page.getByRole('button', { name: 'Reset filters', exact: true }).first().click();
  await page
    .getByRole('combobox', { name: 'Saved queue views' })
    .selectOption({ label: 'API ready' });
  await expect(page).toHaveURL(/repo=acme%2Fapi/);
  await expect(page).toHaveURL(/provisioning=ready/);
  await expect(
    page.getByRole('button', { name: 'Select all matching', exact: true }),
  ).toBeEnabled();
  await page.getByRole('button', { name: 'Select all matching', exact: true }).click();
  await page.getByRole('button', { name: 'Delete from queue', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page.getByText('Snapshot across all pages')).toBeVisible();
});

/*
 * Every other form dialog is a labelled field with Cancel and the primary
 * action in its footer, so an operator who has used one knows how to leave and
 * how to submit this one. Enter still saves, because the field is a form's.
 */
test('the save-view dialog has a Cancel and still saves on Enter', async ({ page }) => {
  await goto(page, '/queue?repo=acme%2Fapi', 'Queue');
  await page.getByRole('button', { name: 'Save view', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Save queue view' });
  await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Save view', exact: true })).toBeDisabled();
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(dialog).toBeHidden();

  await page.getByRole('button', { name: 'Save view', exact: true }).click();
  await page.getByRole('textbox', { name: 'View name' }).fill('Enter saves');
  await page.getByRole('textbox', { name: 'View name' }).press('Enter');
  await expect(dialog).toBeHidden();
  await expect(
    page.getByRole('combobox', { name: 'Saved queue views' }).locator('option', {
      hasText: 'Enter saves',
    }),
  ).toHaveCount(1);
});

/*
 * The actions are on the row rather than behind a menu, which is four buttons
 * per row instead of one trigger. That is a hundred tab stops on a full page if
 * each is a stop of its own, so the four are one toolbar: Tab reaches it once
 * and the arrow keys move along it. The arrows skip what cannot be pressed,
 * because focus that lands on a disabled button is focus an operator has to get
 * out of by guessing.
 */
test("a row's actions are one stop on the keyboard, and the arrows move along them", async ({
  page,
}) => {
  const response = await page.request.get('/api/v1/provisioning?limit=1&sort=queued_at&order=asc');
  const { items } = await response.json();
  const job = items[0];
  const subject = `${job.repo} / ${job.job_name}`;

  await goto(page, '/queue', 'Queue');
  const runNow = page.getByRole('button', { name: `Run now: ${subject}`, exact: true }).first();
  await expect(runNow).toBeVisible();

  // Reached the way the keyboard reaches it, rather than by being clicked: a
  // click would say nothing about the tab order.
  await runNow.focus();
  await expect(runNow).toBeFocused();

  await page.keyboard.press('ArrowRight');
  await expect(
    page.getByRole('button', { name: `Pause: ${subject}`, exact: true }).first(),
  ).toBeFocused();

  /*
   * Resume is already in force on a ready item, and the arrow lands on it all
   * the same. An action that cannot be taken is refused, not removed: it stays
   * focusable so that the reason it gives is reachable without a pointer, and
   * so that confirming an action which makes its own button unavailable does
   * not drop focus to the top of the document.
   */
  await page.keyboard.press('ArrowRight');
  const resume = page
    .getByRole('button', { name: new RegExp(`^Resume: ${escapeRegExp(subject)}\\.`) })
    .first();
  await expect(resume).toBeFocused();
  await expect(resume).toHaveAttribute('aria-disabled', 'true');
  // The reason is part of what the control is called, so it is announced with it.
  await expect(resume).toHaveAccessibleName(/Already provisioning normally/);

  // Pressing it does nothing rather than opening a confirmation for a no-op.
  await resume.press('Enter');
  await expect(page.getByRole('dialog')).toHaveCount(0);

  await page.keyboard.press('ArrowRight');
  await expect(
    page.getByRole('button', { name: `Delete from queue: ${subject}`, exact: true }).first(),
  ).toBeFocused();
});

/*
 * The top bar is sticky too, so a bar pinned at the top of the page slid up
 * behind it: the bulk actions were there, and could be neither seen nor pressed
 * until the operator scrolled back to the top of a long queue.
 */
test('the bulk-action bar stays below the top bar while a long queue scrolls', async ({ page }) => {
  // A short window, so the page overflows however few items the fleet holds.
  await page.setViewportSize({ width: 1280, height: 420 });
  await goto(page, '/queue', 'Queue');
  const select = page.getByRole('button', { name: 'Select all matching', exact: true });
  await expect(select).toBeEnabled();

  await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
  await expect
    .poll(() => page.evaluate(() => window.scrollY), { message: 'the page did not scroll' })
    .toBeGreaterThan(0);

  // The bar has no role or name of its own, so it is found by its class.
  const topBar = await page.getByRole('banner').boundingBox();
  const bar = await page.locator('.selection-bar').boundingBox();
  expect(topBar, 'the top bar has a box').not.toBeNull();
  expect(bar, 'the selection bar has a box').not.toBeNull();
  expect(bar!.y).toBeGreaterThanOrEqual(topBar!.y + topBar!.height);

  // Where it is drawn is not enough: the press has to reach the button.
  const button = await select.boundingBox();
  const reached = await page.evaluate(
    ({ x, y }) => document.elementFromPoint(x, y)?.closest('button')?.textContent?.trim(),
    { x: button!.x + button!.width / 2, y: button!.y + button!.height / 2 },
  );
  expect(reached).toBe('Select all matching');
});
