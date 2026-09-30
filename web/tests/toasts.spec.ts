/**
 * The notification queue, and the one promise it makes.
 *
 * "Successes dismiss themselves, errors do not" is the whole contract: an
 * operator who looked away should still find out that something was refused.
 * The queue is capped, though, so the interesting case is a refusal followed by
 * a run of successes -- which is exactly what a bad afternoon looks like.
 */
import { expect, test, type APIRequestContext, type Page, type Route } from '@playwright/test';
import { browserOverride, dataRows, FIXTURE, goto, grid } from './support/fixtures';

test.use(browserOverride);

// Five round trips through the grid, and the queue's own dismissal timers in
// between. Comfortable on a laptop and not on a shared CI runner, where the
// first version of this spent its whole budget on the body and timed out in
// its own cleanup -- leaving the fleet disabled for every spec that followed.
test.slow();

const errorToasts = (page: Page) => page.locator('.toast[data-tone="error"]');
const successToasts = (page: Page) => page.locator('.toast[data-tone="success"]');

/** Tick one pool and press one of the bulk buttons. */
async function bulkAction(page: Page, label: 'Enable' | 'Disable'): Promise<void> {
  const row = dataRows(grid(page, 'Pools')).filter({ hasText: FIXTURE.linuxPool });
  const checkbox = row.getByRole('checkbox', { name: 'Select this row' });
  await checkbox.check();
  await page
    .getByRole('group', { name: /Actions for the selected/ })
    .getByRole('button', { name: label })
    .click();
  await expect(checkbox).not.toBeChecked();
}

/**
 * Put the fleet back.
 *
 * In a hook rather than a `finally`, so that restoring it is not competing for
 * what is left of the test's own timeout: the suite shares one controller, and
 * a pool left disabled here is a pool that cannot place a runner in somebody
 * else's spec -- a failure a long way from its cause.
 */
async function enableEveryPool(request: APIRequestContext): Promise<void> {
  const pools = await request.get('/api/v1/pools');
  for (const pool of ((await pools.json()).items ?? []) as { id?: string }[]) {
    if (pool.id) await request.post(`/api/v1/pools/${pool.id}/enable`);
  }
}

test.afterEach(async ({ request }) => {
  await enableEveryPool(request);
});

test('a refusal is not pushed off the screen by the successes that follow it', async ({ page }) => {
  await goto(page, '/pools', 'Pools');
  await expect(dataRows(grid(page, 'Pools')).first()).toBeVisible();

  // One refusal, from the controller's point of view: the request is made and
  // comes back 500, which is the path an operator meets when a pool's host has
  // gone away underneath it.
  await page.route('**/api/v1/pools/*/disable', (route) =>
    route.fulfill({
      status: 500,
      contentType: 'application/json',
      body: JSON.stringify({ code: 'internal', message: 'The pool could not be reached.' }),
    }),
  );
  await bulkAction(page, 'Disable');
  await expect(errorToasts(page)).toHaveCount(1);
  await page.unroute('**/api/v1/pools/*/disable');

  // Four things that went fine. The queue shows four at a time, so under the
  // old rule -- evict the oldest, whatever it is -- the fourth success pushed
  // the refusal off the screen before anybody had read it.
  for (const label of ['Disable', 'Enable', 'Disable', 'Enable'] as const) {
    await bulkAction(page, label);
  }
  await expect(successToasts(page).first()).toBeVisible();
  await expect(errorToasts(page)).toHaveCount(1);
  await expect(errorToasts(page)).toContainText('could not be disabled');

  // And it goes when it is dismissed, not before.
  await errorToasts(page).getByRole('button', { name: 'Dismiss' }).click();
  await expect(errorToasts(page)).toHaveCount(0);
});

/**
 * The server writes its refusals for a log line: lowercase, no full stop. Under
 * a sentence-case title they read as raw output, so the toast and the error
 * panel capitalise and stop them -- and change nothing else, because a 403's
 * words are the ones that name the role required.
 */
const REFUSAL = 'your account is not allowed to do that';
const forbidden = (route: Route) =>
  route.fulfill({
    status: 403,
    contentType: 'application/json',
    body: JSON.stringify({ error: { code: 'forbidden', message: REFUSAL } }),
  });

test('a refused action explains itself in a sentence, in the server words', async ({ page }) => {
  const pools = (await page.request.get('/api/v1/pools').then((r) => r.json())) as {
    items: { id: string; name: string }[];
  };
  const pool = pools.items.find((p) => p.name === FIXTURE.linuxPool);
  expect(pool).toBeDefined();
  await page.route(`**/api/v1/pools/${pool!.id}/prewarm`, forbidden);
  await goto(page, `/pools/${pool!.id}`, FIXTURE.linuxPool);
  await page.getByRole('button', { name: 'Prewarm image' }).click();
  await expect(errorToasts(page)).toHaveCount(1);
  await expect(errorToasts(page)).toContainText('Your account is not allowed to do that.');
  await expect(errorToasts(page)).not.toContainText(REFUSAL);
});

test('a report that failed to load shows the refusal as a sentence', async ({ page }) => {
  await page.route('**/api/v1/usage?**', forbidden);
  await page.goto('/usage', { waitUntil: 'domcontentloaded' });
  await expect(page.getByRole('alert').filter({ hasText: 'not allowed' }).first()).toContainText(
    'Your account is not allowed to do that.',
  );
});
