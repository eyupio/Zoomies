/**
 * The pages keep themselves current.
 *
 * Refreshing is a thing an operator may do, never a thing they must, and this
 * protects the second half: a change made somewhere else -- another operator's
 * tab, the CLI, an automation -- appears on the page that is already open,
 * without anyone pressing anything. Each test makes the change over the API,
 * the way any other client would, and watches the page it is looking at.
 *
 * Each test also plants a value on `window` before the change and checks it
 * is still there afterwards, because a page that got its new numbers by
 * reloading itself would pass a weaker test and still be the bug.
 */
import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test';
import {
  browserOverride,
  dataRows,
  expectNoReload,
  FIXTURE,
  goto,
  grid,
  plantMarker,
} from './support/fixtures';

test.use(browserOverride);

interface Named {
  id: string;
  name: string;
}

/** A fixture, found by name the way the tests find it on screen. */
async function findByName(request: APIRequestContext, path: string, name: string): Promise<Named> {
  const body = (await request.get(path).then((r) => r.json())) as { items: Named[] };
  const found = body.items.find((item) => item.name === name);
  expect(found, `${name} is listed by ${path}`).toBeTruthy();
  return found as Named;
}

/** The top bar's problems bell, whose label carries the count. */
function bell(page: Page): Locator {
  return page.getByRole('button', { name: /^Problems\./ });
}

/** "1 error and 2 warnings need your attention." -> 3 */
function countIn(label: string | null): number {
  let total = 0;
  for (const match of (label ?? '').matchAll(/(\d+) (error|warning|note)s?/g)) {
    total += Number(match[1]);
  }
  return total;
}

test('a host cordoned from elsewhere says so on the Hosts page', async ({ page }) => {
  const host = await findByName(page.request, '/api/v1/hosts', 'demo-builder-2');
  await goto(page, '/hosts', 'Hosts');
  const card = page.getByRole('article', { name: host.name });
  await expect(card).toBeVisible();
  await expect(card).not.toContainText('Cordoned.');
  await plantMarker(page);

  try {
    const response = await page.request.post(`/api/v1/hosts/${host.id}/cordon`, {
      data: { cordoned: true },
    });
    expect(response.ok()).toBeTruthy();
    await expect(card).toContainText('Cordoned.');
    await expectNoReload(page);
  } finally {
    await page.request.post(`/api/v1/hosts/${host.id}/cordon`, { data: { cordoned: false } });
  }
  // And back again, the same way.
  await expect(card).not.toContainText('Cordoned.');
});

/*
 * A live refresh keeps what is on screen, so it must not be announced as a page
 * that is loading. A screen reader user on a busy fleet would otherwise hear
 * "Loading", then the count, again and again, over whatever they were reading:
 * live regions announce an outcome once, not every heartbeat.
 */
test('a live refresh of a grid does not announce that it is loading', async ({ page }) => {
  const host = await findByName(page.request, '/api/v1/hosts', 'demo-builder-2');
  await goto(page, '/runners', 'Runners');
  await expect(dataRows(grid(page, 'Runners')).first()).toBeVisible();

  // The grid's own polite region, the one that carries its count.
  const region = page.locator('p.sr-only[aria-live="polite"]', { hasText: /runners$/ });
  await expect(region).toHaveText(/^\d+ runners$/);

  await region.evaluate((node) => {
    const seen: string[] = [];
    (window as unknown as { __announced: string[] }).__announced = seen;
    new MutationObserver(() => seen.push(node.textContent ?? '')).observe(node, {
      childList: true,
      characterData: true,
      subtree: true,
    });
  });

  // A change from elsewhere the grid answers with a refetch of its own query.
  const refetched = page.waitForResponse(
    (response) => response.url().includes('/api/v1/runners?') && response.ok(),
  );
  try {
    const response = await page.request.post(`/api/v1/hosts/${host.id}/cordon`, {
      data: { cordoned: true },
    });
    expect(response.ok()).toBeTruthy();
    await refetched;
    // Let the answer land and the region settle before reading what it said.
    await expect(region).toHaveText(/^\d+ runners$/);
  } finally {
    await page.request.post(`/api/v1/hosts/${host.id}/cordon`, { data: { cordoned: false } });
  }

  const announced = await page.evaluate(
    () => (window as unknown as { __announced: string[] }).__announced,
  );
  expect(announced, 'nothing said "Loading" for a refresh that kept the rows').not.toContain(
    'Loading',
  );
});

test('a pool edited from elsewhere changes in the Pools grid', async ({ page }) => {
  const pool = await findByName(page.request, '/api/v1/pools', FIXTURE.linuxPool);
  await goto(page, '/pools', 'Pools');
  const row = dataRows(grid(page, 'Pools')).filter({ hasText: FIXTURE.linuxPool });
  await expect(row).toContainText('5m 00s');
  await plantMarker(page);

  try {
    const response = await page.request.patch(`/api/v1/pools/${pool.id}`, {
      data: { idle_timeout: '6m' },
    });
    expect(response.ok()).toBeTruthy();
    await expect(row).toContainText('6m 00s');
    await expectNoReload(page);
  } finally {
    await page.request.patch(`/api/v1/pools/${pool.id}`, { data: { idle_timeout: '5m' } });
  }
  await expect(row).toContainText('5m 00s');
});

test('a new risk on a pool reaches the problems bell on whatever page is open', async ({
  page,
}) => {
  const pool = await findByName(page.request, '/api/v1/pools', FIXTURE.armPool);
  // The Jobs page has nothing to do with pools, which is the point: the bell
  // follows the operator everywhere.
  await goto(page, '/jobs', 'Jobs');
  await expect(bell(page)).toBeVisible();
  const before = countIn(await bell(page).getAttribute('aria-label'));
  await plantMarker(page);

  try {
    const response = await page.request.patch(`/api/v1/pools/${pool.id}`, {
      data: { run_as_root: true },
    });
    expect(response.ok()).toBeTruthy();
    await expect
      .poll(async () => countIn(await bell(page).getAttribute('aria-label')), {
        message: 'the bell counts the new warning',
      })
      .toBe(before + 1);
    await expectNoReload(page);
  } finally {
    await page.request.patch(`/api/v1/pools/${pool.id}`, { data: { run_as_root: false } });
  }
  await expect
    .poll(async () => countIn(await bell(page).getAttribute('aria-label')), {
      message: 'the bell forgets the warning once the setting is undone',
    })
    .toBe(before);
});

test('a dropped stream says so, and the page catches up by itself when it returns', async ({
  page,
}) => {
  // A page which cannot hear the controller has to say so rather than quietly
  // showing stale numbers, and has to reconcile once it can hear again. The
  // refresh button is not the answer to either: nobody watching a dashboard is
  // there to press it. Nothing tested either half.
  await goto(page, '/runners', 'Runners');
  const rows = dataRows(grid(page, 'Runners'));
  await expect(rows.first()).toBeVisible();

  const connection = page.locator('.connection');
  await expect(connection).toHaveAttribute('data-state', 'live');

  // Cut the stream. Aborting is what a proxy, a laptop lid or a controller
  // restart looks like from here.
  let cut = true;
  await page.route('**/api/v1/events*', async (route) => {
    if (cut) return route.abort('connectionfailed');
    return route.fallback();
  });
  await page.evaluate(() => window.dispatchEvent(new Event('offline')));

  await expect(connection, 'the top bar stops claiming to be live').not.toHaveAttribute(
    'data-state',
    'live',
  );
  await expect(page.locator('.connection-text')).toHaveText(/Reconnecting|Offline|Connecting/);

  // Let it back through. The stream reopens on its own and the page returns to
  // live without anybody pressing anything.
  cut = false;
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect(connection, 'and comes back by itself').toHaveAttribute('data-state', 'live', {
    timeout: 20_000,
  });
  // And the rows are still the rows: catching up is a reconcile, not a reload.
  await expect(rows.first()).toBeVisible();
});

test('an optimistic change that the controller refuses is put back', async ({ page }) => {
  // Optimism is what makes the grid feel immediate, and a refusal that left the
  // wrong answer on screen would be worse than no optimism at all: the operator
  // would believe the pool was disabled and walk away.
  await goto(page, '/pools', 'Pools');
  const rows = dataRows(grid(page, 'Pools'));
  const linux = rows.filter({ hasText: FIXTURE.linuxPool });
  await expect(linux).toContainText('Enabled');

  await page.route('**/api/v1/pools/*/disable', (route) =>
    route.fulfill({
      status: 409,
      contentType: 'application/json',
      body: JSON.stringify({
        error: { code: 'conflict', message: 'That pool changed underneath you.' },
      }),
    }),
  );

  await linux.getByRole('button', { name: new RegExp(`Actions for ${FIXTURE.linuxPool}`) }).click();
  await page.getByRole('menuitem', { name: 'Disable' }).click();

  // The refusal is reported, and the row is what the controller says it is
  // rather than what the click hoped for.
  await expect(page.locator('.toast[data-tone="error"]')).toBeVisible();
  await expect(linux, 'the row rolled back').toContainText('Enabled');
});

test('a scaling decision delivered twice appears once', async ({ page }) => {
  // A replay overlaps: after a resync, or a reconnect whose buffer reaches back
  // further than the tab needed, the stream carries decisions the tab already
  // has. Prepending both used to leave two identical rows -- a scheduler
  // apparently deciding the same thing twice, which is what a flapping pool
  // looks like -- and, because the feed is keyed by the decision's id, the
  // second copy takes the whole panel down rather than merely misleading.
  await goto(page, '/', 'Overview');
  const feed = page.getByRole('region', { name: 'Recent events', exact: true });
  await expect(feed).toBeVisible();

  const decision = {
    id: 'scl_replayed',
    pool_id: 'pool_replayed',
    pool_name: 'zoomies-demo-linux-x64',
    from: 2,
    to: 3,
    reason: 'a decision the stream sends twice',
    created_at: new Date().toISOString(),
  };
  const frame = `id: 999998\nevent: scaling\ndata: ${JSON.stringify(decision)}\n\n`;
  await page.route('**/api/v1/events*', (route) =>
    route.fulfill({
      status: 200,
      headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
      body: frame + frame.replace('999998', '999999'),
    }),
  );
  await plantMarker(page);
  await page.evaluate(() => {
    window.dispatchEvent(new Event('offline'));
    window.dispatchEvent(new Event('online'));
  });

  const row = feed.getByRole('listitem').filter({ hasText: decision.reason });
  await expect(row).toHaveCount(1, { timeout: 10_000 });
  await expectNoReload(page);
});

test('a runner lent spare CPU says so in the feed, in the dog park’s own words', async ({
  page,
}) => {
  // The demo fleet lends no CPU -- both its pools have elastic CPU off, so
  // every runner in it sits at its share -- so the two frames a controller
  // running a bursting pool would send are sent here. Two, not one: the first
  // is the state the runner is already in when the tab opens, which is not
  // something that just happened, and that rule is as much under test as the
  // line the second produces.
  await goto(page, '/', 'Overview');
  const feed = page.getByRole('region', { name: 'Recent events', exact: true });
  await expect(feed.getByRole('listitem').first()).toBeVisible();

  const listed = (await page.request.get('/api/v1/runners?limit=1').then((r) => r.json())) as {
    items: Array<Record<string, unknown>>;
  };
  const runner = listed.items[0];
  expect(runner, 'the fixture has a runner to lend CPU to').toBeTruthy();
  const held = runner?.cpu_resource as Record<string, unknown> | undefined;
  expect(held?.state, 'the fixture runner sits at its share').toBe('sit_and_stay');

  const withCPU = (cpu: Record<string, unknown>) => ({ ...runner, cpu_resource: cpu });
  const frame = (id: number, cpu: Record<string, unknown>) =>
    `id: ${id}\nevent: runner.updated\ndata: ${JSON.stringify(withCPU(cpu))}\n\n`;
  const body =
    frame(999_001, held ?? {}) +
    frame(999_002, {
      state: 'maximum_zoomies',
      label: 'Squirrel spotted, maximum zoomies',
      reason: 'spare_cpu_lent',
      guaranteed_cpus: 2,
      current_cpus: 3.8,
      ceiling_cpus: 4,
      factor: 1.9,
    });
  await page.route('**/api/v1/events*', (route) =>
    route.fulfill({
      status: 200,
      headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
      body,
    }),
  );
  await plantMarker(page);
  await page.evaluate(() => {
    window.dispatchEvent(new Event('offline'));
    window.dispatchEvent(new Event('online'));
  });

  // The state's own name, as the runner's page says it: translating it into
  // "CPU allocation factor 1.9" would describe a different system from the one
  // on the runner's own page. Plain, because this browser has not chosen the
  // kennel's words -- the controller's label carries those, and is only shown
  // to a browser that has.
  const row = feed.getByRole('listitem').filter({ hasText: 'Maximum boost' });
  await expect(row).toHaveCount(1, { timeout: 10_000 });
  await expect(row, 'and what it actually got').toContainText(
    '3.8 CPUs now against a guarantee of 2',
  );
  // The state it was already in when the tab opened is not a line of its own.
  await expect(feed.getByRole('listitem').filter({ hasText: 'Held at its share' })).toHaveCount(0);
  await expectNoReload(page);
});

test('a runner that comes up ready is a line of its own, and the states on the way are not', async ({
  page,
}) => {
  // The successful half of a runner's life. The demo controller hosts no
  // runners of its own, so the frames a controller with a host would send are
  // sent here: provisioning, registering, then idle. Only the last is a line
  // -- one per state would be a log rather than a feed.
  await goto(page, '/', 'Overview');
  const feed = page.getByRole('region', { name: 'Recent events', exact: true });
  await expect(feed.getByRole('listitem').first()).toBeVisible();

  const runner = {
    id: 'run_livespec',
    name: 'zoomies-livespec-runner',
    pool_id: 'pool_demolinux',
    pool_name: 'zoomies-demo-linux-x64',
    host_name: 'demo-builder-1',
    ephemeral: true,
    jobs_handled: 0,
    created_at: new Date().toISOString(),
  };
  const frame = (id: number, state: string) =>
    `id: ${id}\nevent: runner.updated\ndata: ${JSON.stringify({ ...runner, state })}\n\n`;
  await page.route('**/api/v1/events*', (route) =>
    route.fulfill({
      status: 200,
      headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
      body: frame(999_101, 'provisioning') + frame(999_102, 'registering') + frame(999_103, 'idle'),
    }),
  );
  await plantMarker(page);
  await page.evaluate(() => {
    window.dispatchEvent(new Event('offline'));
    window.dispatchEvent(new Event('online'));
  });

  const ready = feed.getByRole('listitem').filter({ hasText: 'A runner is ready for work' });
  await expect(ready).toHaveCount(1, { timeout: 10_000 });
  await expect(ready).toContainText(runner.name);
  await expect(feed.getByRole('listitem').filter({ hasText: 'Provisioning' })).toHaveCount(0);
  await expectNoReload(page);
});
