/**
 * Size classes, tags and automatic pools, from the pages that show them.
 *
 * Two kinds of test live here, and which is which is a choice made each time.
 *
 * What a host says about itself is proved against the real controller: a host
 * is enrolled the way an agent does it, the controller works out its class and
 * records why, and the card and the dialog say it in the controller's own
 * words. That is the whole path, and the part that has to be true for an
 * operator to believe the rest.
 *
 * A pool the controller keeps needs a GitHub App installation that a spec has
 * no way to stand up -- the suite's fake GitHub is not reachable from this
 * controller -- so the pages that show one are proved against the controller's
 * real responses with the automatic fields laid over them, and the requests
 * the UI sends are captured rather than answered. The Go tests prove the
 * controller makes those pools; what this proves is that the page renders
 * what it is told and asks for what it means.
 *
 * Both switches are off by default and the suite shares one controller, so
 * every test that turns one on puts it back.
 */
import { expect, test, type Page, type Route } from '@playwright/test';
import { browserOverride, dataRows, documentWidth, goto, grid } from './support/fixtures';

test.use(browserOverride);
test.describe.configure({ mode: 'serial' });

type Switch = 'scheduler.size_routing' | 'scheduler.auto_pools';

async function setSwitch(page: Page, key: Switch, value: 'off' | 'shadow' | 'on'): Promise<void> {
  const res = await page.request.patch('/api/v1/settings', { data: { [key]: value } });
  expect(res.ok(), `${key} was set to ${value}`).toBeTruthy();
}

/**
 * Every button, link and field on the page has a name a screen reader can say,
 * and the page does not scroll sideways. The suite's accessibility and phone
 * passes visit each page in its ordinary state, which is not where these
 * controls are, so each state this file builds is audited here.
 */
async function auditThePage(page: Page, where: string): Promise<void> {
  // And the page fits its window: a panel of sentences is the kind of thing
  // that pushes a phone sideways without anybody noticing on a desktop.
  const { scrollWidth, clientWidth } = await documentWidth(page);
  expect(scrollWidth, `${where} scrolls sideways`).toBeLessThanOrEqual(clientWidth);
  await expect(page.getByRole('button', { name: /^$/ }), `${where}: buttons are named`).toHaveCount(
    0,
  );
  await expect(page.getByRole('link', { name: /^$/ }), `${where}: links are named`).toHaveCount(0);
  await expect(
    page.getByRole('textbox', { name: /^$/ }),
    `${where}: text fields are named`,
  ).toHaveCount(0);
}

/* -- a real host: what it says about itself ------------------------------------ */

test.describe('a host and its class', () => {
  const NAME = `class-host-${Date.now()}`;
  let hostId = '';
  let agentToken = '';

  type HostView = {
    size_class?: { class: string; source: string; measured?: string; reason: string };
    tags?: { key: string; value: string; source: string; overrides?: string }[];
  };

  const readHost = async (page: Page): Promise<HostView> =>
    (await page.request.get(`/api/v1/hosts/${hostId}`).then((r) => r.json())) as HostView;

  const card = (page: Page) => page.getByRole('article', { name: NAME, exact: true });

  async function openTags(page: Page) {
    await card(page)
      .getByRole('button', { name: /Actions for/ })
      .click();
    await page.getByRole('menuitem', { name: 'Edit tags' }).click();
    const dialog = page.getByRole('dialog', { name: `Tags on ${NAME}` });
    await expect(dialog).toBeVisible();
    return dialog;
  }

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    try {
      await setSwitch(page, 'scheduler.size_routing', 'shadow');
      const minted = await page.request.post('/api/v1/join-tokens', {
        data: { ttl: '15m', capacity: 8 },
      });
      expect(minted.ok(), 'a join token was minted').toBeTruthy();
      const { token } = (await minted.json()) as { token: string };
      // 12 CPUs and 32 GB: 11.4 and about 30 once the reserve is held back, which
      // is the medium class on the default limits.
      const join = await page.request.post('/api/v1/agent/join', {
        data: {
          protocol_version: 1,
          join_token: token,
          name: NAME,
          capacity: 8,
          os: 'linux',
          arch: 'amd64',
          cpus: 12,
          memory_mb: 32768,
          version: 'dev',
          backends: [{ kind: 'docker', available: true }],
        },
      });
      expect(join.ok(), 'the agent join succeeded').toBeTruthy();
      const joined = (await join.json()) as { host_id: string; agent_token: string };
      hostId = joined.host_id;
      agentToken = joined.agent_token;
      const beat = await page.request.post('/api/v1/agent/heartbeat', {
        headers: { Authorization: `Bearer ${agentToken}` },
        data: { protocol_version: 1, usage: { cpu_percent: 5, memory_available_mb: 24000 } },
      });
      expect(beat.ok(), 'the heartbeat was accepted').toBeTruthy();
      // The class is worked out by the controller's own pass, which a join wakes.
      await expect
        .poll(async () => (await readHost(page)).size_class?.class, { timeout: 20_000 })
        .toBe('medium');
    } finally {
      await page.close();
    }
  });

  test.afterAll(async ({ browser }) => {
    const page = await browser.newPage();
    try {
      if (hostId) await page.request.delete(`/api/v1/hosts/${hostId}?force=true`);
      await setSwitch(page, 'scheduler.size_routing', 'off');
    } finally {
      await page.close();
    }
  });

  test('a host says which class it is in and which of its tags the controller works out', async ({
    page,
  }) => {
    await goto(page, '/hosts', 'Hosts');
    const host = card(page);
    await expect(host.getByText('Medium class', { exact: true })).toBeVisible();
    await expect(host.getByTestId('host-size-class')).toContainText('Medium');
    // The controller's own sentence, in the open and not only in a tooltip:
    // there is no hover on a phone.
    await expect(host.getByTestId('host-size-class')).toContainText('allocatable');

    // Said once, as a caption, and the three tags the machine gives it.
    await expect(host.getByText('Worked out by the controller, not stored')).toBeVisible();
    const derived = host.getByTestId('host-derived-tags');
    await expect(derived).toContainText('arch=amd64');
    await expect(derived).toContainText('os=linux');
    await expect(derived).toContainText('size=medium');
    // None of its own, and the card says what that means for a pool's selector.
    await expect(host).toContainText('None of your own');
    // Only the pool switch has anything to say about pools.
    await expect(host.getByTestId('host-auto-pool')).toHaveCount(0);
    await auditThePage(page, 'the Hosts page with a class on it');
  });

  test('the tags dialog lists what the controller works out and keeps a tag of your own', async ({
    page,
  }) => {
    await goto(page, '/hosts', 'Hosts');
    const dialog = await openTags(page);
    const worked = dialog.getByTestId('derived-tags');
    await expect(worked).toContainText('size=medium');
    await expect(worked).toContainText('cannot be edited here');

    await dialog.getByRole('button', { name: 'Add a tag' }).click();
    await dialog.getByRole('textbox', { name: 'Tag 1 key' }).fill('rack');
    await dialog.getByRole('textbox', { name: 'Tag 1 value' }).fill('b4');
    // A name and no value is a flag, and a flag is true -- what
    // `zoomies hosts edit --tag gpu` writes -- so a selector asks for it the same
    // way whichever surface set it.
    await dialog.getByRole('button', { name: 'Add a tag' }).click();
    await dialog.getByRole('textbox', { name: 'Tag 2 key' }).fill('gpu');
    await dialog.getByRole('button', { name: 'Save changes' }).click();
    await expect(dialog).toBeHidden();

    const own = card(page).getByRole('list', { name: `Tags set on ${NAME}` });
    await expect(own).toContainText('rack=b4');
    await expect(own).toContainText('gpu=true');
    // The API says the same, and says whose it is.
    const view = await readHost(page);
    expect(view.tags).toContainEqual(
      expect.objectContaining({ key: 'rack', value: 'b4', source: 'operator' }),
    );
    expect(view.tags).toContainEqual(
      expect.objectContaining({ key: 'gpu', value: 'true', source: 'operator' }),
    );
  });

  test('a size tag has to be a class, and one that is replaces what the machine measures', async ({
    page,
  }) => {
    await goto(page, '/hosts', 'Hosts');
    const dialog = await openTags(page);
    await dialog.getByRole('button', { name: 'Add a tag' }).click();
    // The rack and the flag from the last test are rows 1 and 2.
    await dialog.getByRole('textbox', { name: 'Tag 3 key' }).fill('size');
    const value = dialog.getByRole('textbox', { name: 'Tag 3 value' });
    await value.fill('huge');

    // Said beside the row, before anything is sent, and Save waits.
    await expect(dialog.getByRole('alert')).toContainText(
      'the size tag has to be small, medium or large',
    );
    await expect(dialog.getByRole('button', { name: 'Save changes' })).toBeDisabled();
    await auditThePage(page, 'the tags dialog with a refused row');

    // The server refuses the same thing for a caller that never saw the form.
    const refused = await page.request.patch(`/api/v1/hosts/${hostId}`, {
      data: { labels: { size: 'huge' } },
    });
    expect(refused.status()).toBe(422);
    expect(await refused.text()).toContain('small, medium or large');

    await value.fill('large');
    await expect(dialog.getByRole('alert')).toHaveCount(0);
    await dialog.getByRole('button', { name: 'Save changes' }).click();
    await expect(dialog).toBeHidden();

    // The card follows the tag, and says what the machine would have said.
    const host = card(page);
    await expect(host.getByTestId('host-size-class')).toContainText('Large', { timeout: 15_000 });
    await expect(host.getByTestId('host-size-override')).toHaveText(
      "Set by this host's size tag. Its machine measures as medium.",
    );
    await expect(host.getByRole('list', { name: `Tags set on ${NAME}` })).toContainText(
      'size=large',
    );
    // It is the operator's now, so the controller no longer lists its own.
    await expect(host.getByTestId('host-derived-tags')).not.toContainText('size=');
  });

  test('removing the size tag gives the host back to what its machine measures', async ({
    page,
  }) => {
    await goto(page, '/hosts', 'Hosts');
    const dialog = await openTags(page);
    await dialog.getByRole('button', { name: 'Remove the tag size' }).click();
    await dialog.getByRole('button', { name: 'Save changes' }).click();
    await expect(dialog).toBeHidden();

    const host = card(page);
    await expect(host.getByTestId('host-size-class')).toContainText('Medium', { timeout: 15_000 });
    await expect(host.getByTestId('host-size-override')).toHaveCount(0);
    await expect(host.getByTestId('host-derived-tags')).toContainText('size=medium');
    await expect.poll(async () => (await readHost(page)).size_class?.source).toBe('measured');
  });

  test('the Overview feed carries the controller’s reason for putting the host where it did', async ({
    page,
  }) => {
    await goto(page, '/', 'Overview');
    const feed = page.getByRole('region', { name: 'Recent events', exact: true });
    // The controller's own sentence, under a title that says what kind of change
    // it was, and named for the host rather than for its id.
    const entry = feed.getByRole('listitem').filter({ hasText: NAME }).first();
    await expect(entry).toContainText('Host size class changed');
    await expect(entry).toContainText(`${NAME}: 11.4 CPUs and 30.4 GB of memory allocatable`);
    // And it is a kind of its own, which an operator can switch off without
    // losing who changed what.
    await goto(page, '/settings/events', 'Events');
    await expect(page.getByRole('switch', { name: 'Automatic changes' })).toHaveAttribute(
      'aria-checked',
      'true',
    );
  });

  test('the audit log says why the controller put the host where it did', async ({ page }) => {
    const audit = (await page.request
      .get(`/api/v1/audit?action=host.size_class&target_id=${hostId}&sort=created_at&order=asc`)
      .then((r) => r.json())) as {
      items: { target_id: string; actor_kind: string; after: string }[];
    };
    const mine = audit.items;
    expect(mine.length, 'the first class the host was given was recorded').toBeGreaterThan(0);
    for (const entry of mine) {
      expect(entry.actor_kind).toBe('system');
      const after = JSON.parse(entry.after) as { class: string; cause: string };
      expect(after.cause, 'a change by the controller carries the reason for it').not.toBe('');
    }
    expect((JSON.parse(mine[0]!.after) as { cause: string }).cause).toContain('allocatable');

    // And the page shows the row, under the controller's own name.
    await goto(page, '/audit?action=host.size_class', 'Audit');
    await expect(
      dataRows(grid(page, 'Audit log'))
        .filter({ hasText: 'host.size_class' })
        .filter({ hasText: 'system' })
        .first(),
    ).toBeVisible();
  });
});

/* -- pools the controller keeps ------------------------------------------------- */

const CLASSES = [
  {
    class: 'small',
    label: 'zoomies-small',
    host_max_cpus: 4,
    host_max_memory_mb: 16384,
    runner_cpus: 1,
    runner_memory_mb: 2048,
  },
  {
    class: 'medium',
    label: 'zoomies-medium',
    host_max_cpus: 12,
    host_max_memory_mb: 49152,
    runner_cpus: 2,
    runner_memory_mb: 4096,
  },
  { class: 'large', label: 'zoomies-large', runner_cpus: 4, runner_memory_mb: 8192 },
];

const status = (poolId: string, over: Record<string, unknown> = {}) => ({
  size_routing: 'shadow',
  auto_pools: 'shadow',
  installation: 'acme',
  at: new Date().toISOString(),
  pools: [
    {
      key: 'amd64/medium',
      name: 'zoomies-medium',
      pool_id: poolId,
      hosts: ['demo-builder-1', 'demo-builder-2'],
      slots: 10,
    },
  ],
  findings: [],
  skipped: [],
  pending: [],
  classes: CLASSES,
  default_class: 'medium',
  hold: '10m0s',
  fallback_wait: '2m0s',
  host_grace: '10m0s',
  ...over,
});

const auto = (over: Record<string, unknown> = {}) => ({
  key: 'amd64/medium',
  arch: 'amd64',
  class: 'medium',
  warm: 0,
  cap: 0,
  paused: false,
  kept: true,
  hosts: ['demo-builder-1', 'demo-builder-2'],
  slots: 10,
  summary:
    'Kept by the controller for medium x64 hosts: 2 hosts (demo-builder-1, demo-builder-2) hold 10 runners between them.',
  ...over,
});

type PoolRow = { id: string; name: string } & Record<string, unknown>;

/** One of the fleet's real pools, which the tests then make read as one the controller keeps. */
async function realPool(page: Page): Promise<PoolRow> {
  const list = (await page.request.get('/api/v1/pools').then((r) => r.json())) as {
    items: PoolRow[];
  };
  expect(list.items.length, 'the seeded fleet has a pool').toBeGreaterThan(0);
  return list.items[0]!;
}

/**
 * Lay the automatic fields over one pool, in every response that carries it.
 * Everything else about it -- its counts, its jobs, its history -- is the
 * controller's real answer.
 */
async function keepPool(
  page: Page,
  pool: PoolRow,
  fields: () => Record<string, unknown>,
  onPatch?: (body: Record<string, unknown>, route: Route) => Promise<void>,
): Promise<void> {
  const lay = (row: PoolRow): PoolRow => (row.id === pool.id ? { ...row, auto: fields() } : row);

  await page.route(/\/api\/v1\/pools(\?.*)?$/, async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const response = await route.fetch();
    const body = (await response.json()) as { items: PoolRow[] };
    await route.fulfill({ response, json: { ...body, items: body.items.map(lay) } });
  });
  await page.route(new RegExp(`/api/v1/pools/${pool.id}$`), async (route) => {
    const method = route.request().method();
    if (method === 'PATCH' && onPatch) {
      return onPatch(route.request().postDataJSON() as Record<string, unknown>, route);
    }
    if (method !== 'GET') return route.continue();
    const response = await route.fetch();
    await route.fulfill({ response, json: lay((await response.json()) as PoolRow) });
  });
}

test.describe('pools the controller keeps', () => {
  test('with both switches off the page says so in a sentence and shows no panel', async ({
    page,
  }) => {
    await goto(page, '/pools', 'Pools');
    await expect(page.getByTestId('auto-pools-off')).toContainText('Automatic pools are off');
    await expect(page.getByTestId('auto-pools-off')).toContainText('scheduler.auto_pools');
    await expect(page.getByTestId('auto-pools-panel')).toHaveCount(0);
    // Nothing on the grid says "Automatic" for a pool an operator made.
    await expect(dataRows(grid(page, 'Pools')).first()).toBeVisible();
    await expect(grid(page, 'Pools').getByText('Automatic', { exact: true })).toHaveCount(0);
  });

  test('the Pools page marks the pool and says what the controller would do and could not', async ({
    page,
  }) => {
    const pool = await realPool(page);
    await keepPool(page, pool, () => auto());
    await page.route('**/api/v1/auto-pools', (route) =>
      route.fulfill({
        json: status(pool.id, {
          findings: [
            {
              code: 'auto_pool.name_taken',
              subject: 'zoomies-large',
              message:
                'A pool called zoomies-large already exists and is not one the controller keeps, so it was left alone.',
              fix: 'Rename it or delete it, and the controller makes its own.',
            },
          ],
          skipped: [
            {
              host: 'demo-arm-1',
              host_id: 'host-c',
              reason: 'cordoned',
              message:
                'It is cordoned, so its slots do not count towards an automatic pool until it is uncordoned.',
            },
          ],
          pending: [
            {
              kind: 'create',
              key: 'amd64/large',
              pool: 'zoomies-large',
              cause: 'demo-builder-3 joined and is large, and no pool is kept for that class yet.',
            },
          ],
        }),
      }),
    );
    await goto(page, '/pools', 'Pools');

    // Opened for the operator, because something there is waiting on them.
    const panel = page.getByTestId('auto-pools-panel');
    await expect(panel).toHaveAttribute('open', '');
    await expect(panel).toContainText('Pools: Report only');
    await expect(panel).toContainText('Routing: Report only');
    await expect(panel).toContainText('1 pool · 2 hosts · 10 runner slots');
    await expect(panel.getByText('changes nothing')).toBeVisible();

    await expect(panel.getByTestId('auto-pools-pending')).toContainText('What it would do');
    await expect(panel.getByTestId('auto-pools-pending')).toContainText('Would make zoomies-large');
    await expect(panel.getByTestId('auto-pools-pending')).toContainText('demo-builder-3 joined');
    await expect(panel.getByTestId('auto-pools-findings')).toContainText('left alone');
    await expect(panel.getByTestId('auto-pools-findings')).toContainText('Rename it or delete it');
    await expect(panel.getByTestId('auto-pools-skipped')).toContainText('demo-arm-1');
    await expect(panel.getByTestId('auto-pools-skipped').getByRole('link')).toHaveAttribute(
      'href',
      '/hosts/host-c',
    );
    await expect(panel.getByTestId('auto-pools-list').getByRole('link')).toHaveAttribute(
      'href',
      `/pools/${pool.id}`,
    );

    // Where each class begins, in the numbers the controller works with.
    const classes = panel.getByTestId('auto-pools-classes');
    await expect(classes).toContainText('hosts up to 4 CPUs and 16 GB');
    await expect(classes).toContainText('runner 1 CPU and 2 GB');
    await expect(classes).toContainText('hosts up to 12 CPUs and 48 GB');
    await expect(classes).toContainText('hosts anything larger');
    await expect(classes).toContainText('default for a job nothing is known about');
    await expect(classes).toContainText('holds its class for 10 minutes');
    await expect(classes).toContainText('cannot be deleted while');

    // The pool itself, marked, with the controller's sentence for it.
    const row = dataRows(grid(page, 'Pools')).filter({ hasText: pool.name });
    await expect(row.getByText('Automatic', { exact: true })).toBeVisible();
    await auditThePage(page, 'the Pools page with the panel open');
  });

  test('with both switches on and nothing to report the panel is a line that opens on request', async ({
    page,
  }) => {
    const pool = await realPool(page);
    await keepPool(page, pool, () => auto());
    await page.route('**/api/v1/auto-pools', (route) =>
      route.fulfill({ json: status(pool.id, { auto_pools: 'on', size_routing: 'on' }) }),
    );
    await goto(page, '/pools', 'Pools');

    const panel = page.getByTestId('auto-pools-panel');
    await expect(panel).toContainText('Pools: On');
    await expect(panel).toContainText('Routing: On');
    await expect(panel).toContainText('1 pool · 2 hosts · 10 runner slots');
    // Nothing needs an operator, so it does not take the room: closed, and the
    // sections that would have said something are not there at all.
    await expect(panel).not.toHaveAttribute('open', '');
    await expect(panel.getByTestId('auto-pools-pending')).toHaveCount(0);
    await expect(panel.getByTestId('auto-pools-findings')).toHaveCount(0);
    await expect(panel.getByTestId('auto-pools-list')).toBeHidden();

    await panel.locator('summary').click();
    await expect(panel.getByTestId('auto-pools-list')).toContainText('zoomies-medium');
    await expect(panel.getByTestId('auto-pools-list')).toContainText('2 hosts (demo-builder-1');
    // And while it is on, a pool it keeps cannot be deleted.
    await dataRows(grid(page, 'Pools'))
      .filter({ hasText: pool.name })
      .getByRole('button', { name: /Actions for/ })
      .click();
    await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeDisabled();
  });

  test('a pool the controller keeps is paused or set up, never edited into something else', async ({
    page,
  }) => {
    const pool = await realPool(page);
    let paused = false;
    await keepPool(page, pool, () => auto({ paused }));
    await page.route('**/api/v1/auto-pools', (route) => route.fulfill({ json: status(pool.id) }));
    // Pausing is a request like any other pool's disable; what is proved is
    // that it is sent, and that the real pool is not disabled by it.
    const sent: string[] = [];
    await page.route(new RegExp(`/api/v1/pools/${pool.id}/(disable|enable)$`), async (route) => {
      sent.push(`${route.request().method()} ${new URL(route.request().url()).pathname}`);
      paused = route.request().url().endsWith('/disable');
      await route.fulfill({ json: {} });
    });
    await goto(page, '/pools', 'Pools');

    const row = dataRows(grid(page, 'Pools')).filter({ hasText: pool.name });
    await row.getByRole('button', { name: /Actions for/ }).click();
    // Its limits follow its hosts, so there is nothing to adjust, and the
    // wizard has nothing it could change; the two it does have are here.
    await expect(page.getByRole('menuitem', { name: 'Adjust runner limits' })).toHaveCount(0);
    await expect(page.getByRole('menuitem', { name: 'Edit', exact: true })).toHaveCount(0);
    await expect(page.getByRole('menuitem', { name: 'Settings' })).toBeVisible();
    // Not while it would be made again.
    await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeDisabled();

    await page.getByRole('menuitem', { name: 'Pause' }).click();
    await expect.poll(() => sent).toEqual([`POST /api/v1/pools/${pool.id}/disable`]);
    await row.getByRole('button', { name: /Actions for/ }).click();
    await expect(page.getByRole('menuitem', { name: 'Resume' })).toBeVisible();
  });

  test('a pool the controller is not keeping is a leftover that can be deleted', async ({
    page,
  }) => {
    const pool = await realPool(page);
    // Reporting only: nothing makes the pool again, so nothing is undone by deleting it.
    const notKept = () =>
      auto({
        kept: false,
        summary:
          'Made by the controller for medium x64 hosts, and not being kept now: automatic pools are only reporting what they would do, so the controller changes no pool. It holds the limits it had.',
      });
    await keepPool(page, pool, notKept);
    await page.route('**/api/v1/auto-pools', (route) => route.fulfill({ json: status(pool.id) }));
    await goto(page, '/pools', 'Pools');

    const row = dataRows(grid(page, 'Pools')).filter({ hasText: pool.name });
    await row.getByRole('button', { name: /Actions for/ }).click();
    await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeEnabled();
    await page.keyboard.press('Escape');

    await goto(page, `/pools/${pool.id}`, pool.name);
    await expect(page.getByTestId('pool-auto')).toContainText('not being kept now');
    await expect(page.getByRole('button', { name: 'Delete' })).toBeEnabled();
  });

  test('a pool page explains where its numbers come from, and its settings are the three it leaves to you', async ({
    page,
  }) => {
    const pool = await realPool(page);
    let patched: Record<string, unknown> | null = null;
    let current = auto();
    await keepPool(
      page,
      pool,
      () => current,
      async (body, route) => {
        patched = body;
        current = auto({ warm: 2, cap: 6 });
        await route.fulfill({ json: { ...pool, auto: current } });
      },
    );
    await page.route('**/api/v1/auto-pools', (route) => route.fulfill({ json: status(pool.id) }));
    await goto(page, `/pools/${pool.id}`, pool.name);

    await expect(page.getByText('Automatic', { exact: true }).first()).toBeVisible();
    const panel = page.getByTestId('pool-auto');
    await expect(panel).toContainText('Kept by the controller for medium x64 hosts');
    await expect(panel).toContainText('x64 hosts in the medium class');
    await expect(panel.getByTestId('pool-auto-hosts')).toContainText('demo-builder-1');
    await expect(panel).toContainText('from its hosts');
    await expect(panel).toContainText('Its maximum follows its hosts, so it is not edited');

    // The header offers what can be done to it, and no more.
    await expect(page.getByRole('button', { name: 'Runner limits' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Pause' })).toBeVisible();
    // The controller is keeping it, so deleting it would only be undone.
    await expect(page.getByRole('button', { name: 'Delete' })).toBeDisabled();

    await page.getByRole('button', { name: 'Settings' }).first().click();
    const dialog = page.getByRole('dialog', { name: `Settings for ${pool.name}` });
    await expect(dialog).toBeVisible();
    const warm = dialog.getByRole('spinbutton', { name: 'Runners to keep ready' });
    const cap = dialog.getByRole('spinbutton', { name: 'Cap' });
    // A number field takes no letters, so the figure that is wrong is a negative one.
    await warm.fill('-1');
    await expect(dialog).toContainText('Use a whole number of zero or more.');
    await expect(dialog.getByRole('button', { name: 'Save settings' })).toBeDisabled();
    await warm.fill('2');
    await cap.fill('6');
    await dialog.getByRole('textbox', { name: 'Idle timeout', exact: true }).fill('15m');
    await dialog.getByRole('button', { name: 'Save settings' }).click();
    await expect(dialog).toBeHidden();

    // Only what was changed is sent, and the valve was not.
    expect(patched).toEqual({ idle_timeout: '15m', auto: { warm: 2, cap: 6 } });
  });

  test('the memory valve is the operator’s to change on a pool the controller keeps', async ({
    page,
  }) => {
    // The pools most fleets run on are the ones the controller keeps, so without
    // this the valve could never be turned on for them.
    const pool = await realPool(page);
    let patched: Record<string, unknown> | null = null;
    const current = auto();
    await keepPool(
      page,
      pool,
      () => current,
      async (body, route) => {
        patched = body;
        await route.fulfill({
          json: {
            ...pool,
            auto: current,
            memory_burst: body.memory_burst ?? { mode: 'off' },
          },
        });
      },
    );
    await page.route('**/api/v1/auto-pools', (route) => route.fulfill({ json: status(pool.id) }));
    await goto(page, `/pools/${pool.id}`, pool.name);

    await page.getByRole('button', { name: 'Settings' }).first().click();
    const dialog = page.getByRole('dialog', { name: `Settings for ${pool.name}` });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('the four things it leaves to you');
    // The valve is a choice of three, and the page shows the one the pool is on:
    // the demo fleet's first pool is set to watch.
    await expect(dialog.getByRole('radio', { name: /^Observe only/ })).toBeChecked();
    await dialog.getByRole('radio', { name: /^Lend memory/ }).check();
    // Swap past a terabyte is a typo, and is said where it is typed.
    const swap = dialog.getByRole('textbox', { name: 'Swap as the last resort', exact: true });
    await swap.fill('2000000');
    await swap.blur();
    await expect(dialog).toContainText('more than a terabyte');
    await expect(dialog.getByRole('button', { name: 'Save settings' })).toBeDisabled();
    await swap.fill('2048');
    await swap.blur();
    await dialog.getByRole('textbox', { name: 'Memory ceiling', exact: true }).fill('12g');
    await dialog.getByRole('textbox', { name: 'Memory ceiling', exact: true }).press('Enter');
    await dialog.getByRole('button', { name: 'Save settings' }).click();
    await expect(dialog).toBeHidden();

    expect(patched).toMatchObject({
      memory_burst: { mode: 'automatic', max_memory_mb: 12288, spill_mb: 2048 },
    });
  });

  test('a link that says "edit this pool" opens the settings of a pool the controller keeps', async ({
    page,
  }) => {
    const pool = await realPool(page);
    await keepPool(page, pool, () => auto());
    await page.route('**/api/v1/auto-pools', (route) => route.fulfill({ json: status(pool.id) }));
    await goto(page, `/pools/${pool.id}?edit=1`, pool.name);
    // Not the wizard: it would offer labels and sizes the controller would put back.
    await expect(page.getByRole('dialog', { name: `Settings for ${pool.name}` })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Cancel' })).toBeVisible();
    await expect(page).not.toHaveURL(/edit=1/);
  });
});

/* -- jobs: what class, why, and where it ran ---------------------------------------- */

const jobs = (page: Page) => grid(page, 'Jobs');

/** Lay size facts over the jobs a list returns, and leave everything else about them alone. */
async function classJobs(page: Page, size: Record<string, unknown>): Promise<void> {
  await page.route(/\/api\/v1\/jobs\?/, async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as { items: Record<string, unknown>[] };
    await route.fulfill({
      response,
      json: { ...body, items: body.items.map((job) => ({ ...job, ...size })) },
    });
  });
}

test.describe('job size', () => {
  test('the drawer says the class, the authority, the pool it was sent to and where it ran', async ({
    page,
  }) => {
    await classJobs(page, {
      size_class: 'medium',
      size_basis: 'history',
      size_reason:
        'its last 14 runs used 6.1 GB at the 90th percentile, which a large runner holds',
      routed_class: 'large',
      routed_note: 'No medium host has room, so it was offered a larger one after two minutes.',
      ran_class: 'large',
      throttled_share: 0.43,
    });
    await goto(page, '/jobs?state=completed&sort=queued_at&order=asc', 'Jobs');
    const first = dataRows(jobs(page)).first();
    await expect(first).toBeVisible();
    await first.click();
    const drawer = page.getByRole('dialog');

    const cls = drawer.getByTestId('job-size-class');
    await expect(cls).toContainText('Medium');
    await expect(cls).toContainText('From its earlier runs');
    await expect(cls).toContainText('Classed medium because its last 14 runs used 6.1 GB');
    await expect(drawer.getByTestId('job-size-fallback')).toContainText('Fallback');
    await expect(drawer.getByTestId('job-size-fallback')).toContainText('the large pool');
    await expect(drawer.getByTestId('job-size-fallback')).toContainText('No medium host has room');
    // It ran where it was sent, and that is said as the fallback working.
    await expect(drawer.getByTestId('job-size-ran')).toHaveText(
      'Ran on a large host, the class it was sent to instead of medium.',
    );
    await expect(drawer.getByTestId('job-throttled')).toContainText('43%');
  });

  test('a job that landed on another class is explained rather than blamed', async ({ page }) => {
    await classJobs(page, {
      size_class: 'medium',
      size_basis: 'default',
      size_reason: 'nothing is known about the job yet, so it gets the default class',
      ran_class: 'large',
    });
    await goto(page, '/jobs?state=completed&sort=queued_at&order=asc', 'Jobs');
    const first = dataRows(jobs(page)).first();
    await first.click();
    const drawer = page.getByRole('dialog');
    const ran = drawer.getByTestId('job-size-ran');
    await expect(ran).toContainText('Ran on a large host, not the medium one it was put in.');
    await expect(ran).toContainText('matched to a runner by GitHub');
    await expect(ran).toContainText('Write the class label in its runs-on');
    await expect(drawer.getByTestId('job-size-fallback')).toHaveCount(0);
    await expect(drawer.getByTestId('job-throttled')).toHaveCount(0);
  });

  test('a job nobody classed shows no size rather than a blank one', async ({ page }) => {
    await goto(page, '/jobs?state=completed&sort=queued_at&order=asc', 'Jobs');
    const first = dataRows(jobs(page)).first();
    await expect(first).toBeVisible();
    await first.click();
    const drawer = page.getByRole('dialog');
    await expect(drawer.getByTestId('job-controller-version')).toBeVisible();
    await expect(drawer.getByTestId('job-size-class')).toHaveCount(0);
  });

  test('the report of jobs that would benefit from a size label, and the pins beside it', async ({
    page,
  }) => {
    const advice = [
      {
        repo: 'acme/api',
        workflow: 'ci.yml',
        job_name: 'build',
        kind: 'unguaranteed',
        class: 'large',
        runs: 12,
        labels: ['self-hosted', 'zoomies'],
        message:
          'its runs-on asks only for zoomies, so it is sent to large while there is room, which is best effort and not a promise.',
        fix: 'add zoomies-large to runs-on, beside zoomies, to make it a guarantee.',
      },
      {
        repo: 'acme/web',
        workflow: 'e2e.yml',
        job_name: 'smoke',
        kind: 'too_large',
        asked: 'large',
        class: 'small',
        runs: 31,
        labels: ['self-hosted', 'zoomies', 'zoomies-large'],
        message: 'its runs-on asks for zoomies-large, and its runs would fit a small runner.',
        fix: 'write zoomies-small in runs-on in place of zoomies-large.',
      },
    ];
    let pins: Record<string, unknown>[] = [];
    let put: Record<string, unknown> | null = null;
    let deleted: string | null = null;

    await page.route('**/api/v1/auto-pools', (route) =>
      route.fulfill({ json: status('pol_x', { auto_pools: 'off' }) }),
    );
    await page.route(/\/api\/v1\/label-advice/, (route) =>
      route.fulfill({
        json: {
          items: advice,
          total: 2,
          limit: 25,
          offset: 0,
          counts: { unguaranteed: 1, too_large: 1 },
        },
      }),
    );
    await page.route(/\/api\/v1\/size-pins/, async (route) => {
      const method = route.request().method();
      if (method === 'PUT') {
        put = route.request().postDataJSON() as Record<string, unknown>;
        pins = [{ ...put, created_by: 'e2e', created_at: new Date().toISOString() }];
        return route.fulfill({ json: { pin: pins[0], reclassified: 2 } });
      }
      if (method === 'DELETE') {
        deleted = new URL(route.request().url()).search;
        pins = [];
        return route.fulfill({ json: { reclassified: 0 } });
      }
      return route.fulfill({ json: { items: pins } });
    });

    await goto(page, '/jobs', 'Jobs');
    const panel = page.getByTestId('size-advice');
    await expect(panel).toContainText('Size labels and pins');
    // The count is on the closed summary, so the report is not found by chance.
    await expect(panel.locator('summary')).toContainText('2');
    await panel.locator('summary').click();

    await expect(panel.getByTestId('advice-counts')).toContainText(
      '1 names no class · 1 names a class that is larger than it uses',
    );
    const rows = panel.getByTestId('advice-row');
    await expect(rows).toHaveCount(2);
    await expect(rows.first()).toContainText('acme/api · ci.yml · build');
    await expect(rows.first()).toContainText('12 runs measured');
    await expect(rows.first()).toContainText(
      'Add zoomies-large to runs-on, beside zoomies, to make it a guarantee.',
    );
    await expect(panel.getByTestId('pins-none')).toBeVisible();
    await auditThePage(page, 'the Jobs page with the size report open');

    // Pinning from a row sends the class its runs call for, for that one job.
    await rows.first().getByRole('button', { name: 'Pin to Large' }).click();
    await expect
      .poll(() => put)
      .toEqual({
        repo: 'acme/api',
        workflow: 'ci.yml',
        job_name: 'build',
        class: 'large',
      });
    await expect(page.getByText('2 jobs already waiting moved to the large class.')).toBeVisible();
    const pin = panel.getByTestId('pin-row');
    await expect(pin).toContainText('acme/api · ci.yml · build');
    await expect(pin).toContainText('Large');
    // Already pinned, so the row does not offer it twice.
    await expect(rows.first().getByRole('button', { name: 'Pin to Large' })).toBeDisabled();

    await pin.getByRole('button', { name: /Remove/ }).click();
    await expect.poll(() => deleted).toBe('?repo=acme%2Fapi&workflow=ci.yml&job_name=build');
    await expect(panel.getByTestId('pins-none')).toBeVisible();
  });

  test('a whole repository is pinned from the same panel, and a half-named job is refused where typed', async ({
    page,
  }) => {
    let put: Record<string, unknown> | null = null;
    await page.route('**/api/v1/auto-pools', (route) =>
      route.fulfill({ json: status('pol_x', { size_routing: 'on' }) }),
    );
    await page.route(/\/api\/v1\/label-advice/, (route) =>
      route.fulfill({ json: { items: [], total: 0, limit: 25, offset: 0, counts: {} } }),
    );
    await page.route(/\/api\/v1\/size-pins/, async (route) => {
      if (route.request().method() === 'PUT') {
        put = route.request().postDataJSON() as Record<string, unknown>;
        return route.fulfill({ json: { pin: put, reclassified: 0 } });
      }
      return route.fulfill({ json: { items: [] } });
    });
    await goto(page, '/jobs', 'Jobs');
    const panel = page.getByTestId('size-advice');
    await panel.locator('summary').click();
    await expect(panel.getByTestId('advice-none')).toContainText('Nothing to suggest');

    const form = panel.getByRole('form', { name: 'Pin a repository or a job' });
    await form.getByRole('textbox', { name: 'Repository' }).fill('acme/api');
    await form.getByRole('combobox', { name: 'Class' }).selectOption('small');
    await form.getByRole('textbox', { name: 'Workflow' }).fill('ci.yml');
    // One half of a job's name pins something nobody asked for.
    await expect(form).toContainText('Name both the workflow and the job, or neither');
    await expect(form.getByRole('button', { name: 'Pin' })).toBeDisabled();
    await form.getByRole('textbox', { name: 'Workflow' }).fill('');
    await form.getByRole('button', { name: 'Pin' }).click();
    await expect.poll(() => put).toEqual({ repo: 'acme/api', class: 'small' });
  });

  test('with size routing off and nothing pinned the Jobs page has no size panel', async ({
    page,
  }) => {
    await goto(page, '/jobs', 'Jobs');
    await expect(
      dataRows(jobs(page))
        .first()
        .or(page.getByText(/Nothing is running/)),
    ).toBeVisible();
    await expect(page.getByTestId('size-advice')).toHaveCount(0);
  });
});
