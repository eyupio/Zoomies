/**
 * How big a runner is on one host, from the dialog that sets it.
 *
 * One host is enrolled the way an agent does it, with a machine of its own, and
 * every test works on that host. What is proved is the whole path rather than
 * a mock of either half: the figures typed into the dialog reach the
 * controller, the controller works out how many runners the host takes from
 * them, and the card says so in the controller's own number. Joins are rate
 * limited per address and both projects share one controller, so the host is
 * enrolled once and the tests run in order on it.
 */
import { expect, test, type Page } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);
test.describe.configure({ mode: 'serial' });

const NAME = `profile-host-${Date.now()}`;
let hostId = '';
let agentToken = '';

/** A host with 12 CPUs and 32 GB and room for eight runners, as its agent reports it. */
async function enrol(page: Page): Promise<{ id: string; token: string }> {
  const minted = await page.request.post('/api/v1/join-tokens', {
    data: { ttl: '15m', capacity: 8 },
  });
  expect(minted.ok(), 'a join token was minted').toBeTruthy();
  const { token } = (await minted.json()) as { token: string };
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
  return { id: joined.host_id, token: joined.agent_token };
}

/**
 * One heartbeat, so the host is one the scheduler would place on. A joined host
 * is healthy until its agent has been quiet for a while, and a spec that reads
 * the pool's room is asking about the hosts a runner could go to right now.
 */
async function heartbeat(page: Page): Promise<void> {
  const beat = await page.request.post('/api/v1/agent/heartbeat', {
    headers: { Authorization: `Bearer ${agentToken}` },
    data: { protocol_version: 1, usage: { cpu_percent: 5, memory_available_mb: 24000 } },
  });
  expect(beat.ok(), 'the heartbeat was accepted').toBeTruthy();
}

type HostView = {
  slots: number;
  slots_limited_by?: string;
  runner_profile?: {
    minimum?: { cpus?: number; memory_mb?: number };
    standard?: { cpus?: number; memory_mb?: number; burst_max_cpus?: number };
    tmpfs?: { disabled?: boolean; max_mb?: number };
  };
};

const readHost = async (page: Page): Promise<HostView> =>
  (await page.request.get(`/api/v1/hosts/${hostId}`).then((r) => r.json())) as HostView;

const card = (page: Page) => page.getByRole('article', { name: NAME, exact: true });

async function openDialog(page: Page) {
  await card(page)
    .getByRole('button', { name: /Actions for/ })
    .click();
  await page.getByRole('menuitem', { name: 'Set runner sizes' }).click();
  const dialog = page.getByRole('dialog', { name: `Runner sizes on ${NAME}` });
  await expect(dialog).toBeVisible();
  return dialog;
}

/** Type a size the way people write it, and commit it with Enter. */
async function type(page: Page, name: string, text: string): Promise<void> {
  const field = page.getByRole('textbox', { name, exact: true });
  await field.fill(text);
  await field.press('Enter');
}

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage();
  try {
    ({ id: hostId, token: agentToken } = await enrol(page));
  } finally {
    await page.close();
  }
});

test.afterAll(async ({ browser }) => {
  if (!hostId) return;
  const page = await browser.newPage();
  try {
    await page.request.delete(`/api/v1/hosts/${hostId}?force=true`);
  } finally {
    await page.close();
  }
});

test('a host with no size takes its capacity, and says so before anything is typed', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  await expect(card(page)).toContainText('0 of 8 slots in use');
  // An unprofiled host reads exactly as it always has: no block of sizes the
  // fleet never chose, repeated on every card.
  await expect(card(page).getByTestId('host-runner-sizes')).toHaveCount(0);

  const dialog = await openDialog(page);
  await expect(dialog.getByTestId('runner-sizes-summary')).toContainText(
    '8 runners at once, as its capacity says',
  );
  // Nothing to save until something is typed.
  await expect(dialog.getByRole('button', { name: 'Save sizes' })).toBeDisabled();
});

test('a standard size gives the host as many slots as its machine holds, and the card says why', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  const dialog = await openDialog(page);
  const summary = dialog.getByTestId('runner-sizes-summary');

  // 12 CPUs less the reserve is 11.4: three runners of 3 CPU. The memory,
  // 32 GB less its reserve, holds three of 8 GB too -- the cores are named
  // because they are the tighter by the controller's own tie-break.
  await type(page, 'Standard CPU', '3');
  await type(page, 'Standard memory', '8g');
  await expect(summary).toContainText('3 runners at once');
  await expect(summary).toContainText('At 3 cores and 8 GB each');
  await expect(summary).toContainText('Its cores run out first');

  await dialog.getByRole('button', { name: 'Save sizes' }).click();
  await expect(dialog).toBeHidden();

  // The card counts against the controller's number, not the operator's
  // ceiling: "0 of 8" with three free would be a card promising slots the
  // next pass refuses.
  await expect(card(page)).toContainText('0 of 3 slots in use');
  await expect(card(page).getByTestId('host-slots-note')).toContainText('its cores limit it');
  const sizes = card(page).getByTestId('host-runner-sizes');
  await expect(sizes).toContainText('Standard');
  await expect(sizes).toContainText('3 cores and 8 GB');
  // Whose figure it is: the standard was set on this host.
  await expect(sizes).toContainText('set on this host');

  const saved = await readHost(page);
  expect(saved.slots).toBe(3);
  expect(saved.slots_limited_by).toBe('cpu');
  expect(saved.runner_profile?.standard).toEqual({ cpus: 3, memory_mb: 8192 });
});

test('a capacity below what the machine holds is the limit, and the dialog says which of the two it is', async ({
  page,
}) => {
  // Capacity is the operator's ceiling and the size is what the machine can
  // carry; the smaller is the count, and a host held below what it could take
  // says so rather than quietly running fewer runners than its size allows.
  const capped = await page.request.patch(`/api/v1/hosts/${hostId}`, { data: { capacity: 2 } });
  expect(capped.ok()).toBeTruthy();
  try {
    await goto(page, '/hosts', 'Hosts');
    await expect(card(page)).toContainText('0 of 2 slots in use');
    await expect(card(page).getByTestId('host-slots-note')).toContainText(
      'capped by its capacity of 2',
    );
    const dialog = await openDialog(page);
    await expect(dialog.getByTestId('runner-sizes-summary')).toContainText(
      'Its capacity of 2 is below the 3 the machine holds',
    );
    expect((await readHost(page)).slots_limited_by).toBe('capacity');
  } finally {
    await page.request.patch(`/api/v1/hosts/${hostId}`, { data: { capacity: 8 } });
  }
});

test('a figure that cannot be right is refused where it is typed, not after Save', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  const dialog = await openDialog(page);
  const save = dialog.getByRole('button', { name: 'Save sizes' });

  // A minimum above the standard says something that cannot be true.
  await type(page, 'Minimum CPU', '4');
  await expect(dialog).toContainText('The minimum is above the standard, 3 cores.');
  await expect(save).toBeDisabled();
  await type(page, 'Minimum CPU', '2');
  await expect(dialog).not.toContainText('The minimum is above the standard');

  // A size above the whole machine could never run one runner here.
  await type(page, 'Standard CPU', '20');
  await expect(dialog).toContainText('could never run a runner here');
  await expect(save).toBeDisabled();

  // A boost ceiling is the most a runner may use, its own share included.
  await type(page, 'Standard CPU', '3');
  await type(page, 'Boost ceiling', '2');
  await expect(dialog).toContainText('at least the standard, 3 cores');
  await expect(save).toBeDisabled();
  await type(page, 'Boost ceiling', '4');
  await expect(save).toBeEnabled();
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
});

test('a minimum and a ceiling are kept with the standard, and each says whose it is', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  const dialog = await openDialog(page);
  await type(page, 'Minimum CPU', '2');
  await type(page, 'Boost ceiling', '4');
  await dialog.getByRole('button', { name: 'Save sizes' }).click();
  await expect(dialog).toBeHidden();

  const sizes = card(page).getByTestId('host-runner-sizes');
  await expect(sizes).toContainText('Minimum');
  await expect(sizes).toContainText('2 cores');
  await expect(sizes).toContainText('Boost ceiling');
  await expect(sizes).toContainText('4 cores');
  // The minimum's memory was left empty, so only its CPU is the host's; the
  // fleet's own minimum, if it sets one, stands in for the rest and says so.
  const saved = await readHost(page);
  expect(saved.runner_profile).toEqual({
    minimum: { cpus: 2 },
    standard: { cpus: 3, memory_mb: 8192, burst_max_cpus: 4 },
  });
});

/** The editor's size section, open, for a pool of the given name. */
async function toSizeSection(page: Page, name: string): Promise<void> {
  await goto(page, '/pools/new', 'Create a pool');
  await page.getByRole('textbox', { name: 'Pool name' }).fill(name);
  await page.getByRole('textbox', { name: 'Labels' }).fill(name);
  await page.keyboard.press('Enter');
  await page.locator('#pool-size').getByRole('heading', { level: 2 }).getByRole('button').click();
  await expect(
    page.locator('#pool-size').getByRole('radio', { name: 'One share of each host' }),
  ).toBeVisible();
}

test('a pool can take its size from each host, and says what a runner is on every one', async ({
  page,
}) => {
  await heartbeat(page);
  await toSizeSection(page, 'e2e-from-host');

  const validated: Record<string, unknown>[] = [];
  await page.route('**/api/v1/pools/validate*', async (route) => {
    validated.push(route.request().postDataJSON() as Record<string, unknown>);
    await route.continue();
  });

  await page.getByRole('radio', { name: 'The size each host sets' }).check();
  // No sliders: the figures are the hosts', not the pool's.
  await expect(page.getByRole('slider', { name: 'CPU per runner' })).toHaveCount(0);
  await expect(page.getByTestId('profile-sizing')).toContainText(
    /\d+ of \d+ hosts?\s+ha(s|ve) set a standard size/,
  );
  // A minimum and elastic CPU are offered as they are under a share: a pool
  // that takes its size from its host can still be floored and lend CPU. The
  // floor is behind a row of its own, and elastic CPU is a choice of three.
  await page.locator('summary', { hasText: 'Smallest runner this pool will accept' }).click();
  await expect(page.getByRole('textbox', { name: 'Minimum CPU' })).toBeVisible();
  await expect(page.getByRole('radio', { name: 'Automatic boost' })).toBeVisible();

  // The request names the choice and states no size: the two are refused
  // together, because the figure the pool stated would be the whole answer.
  await expect.poll(() => validated.at(-1)).toMatchObject({ size_from_profile: true });
  expect(validated.at(-1)?.resources).toEqual({});

  // The room says what a runner is on the host that has a size, whose figure
  // each is, and the floor and ceiling its profile puts on the pool.
  const row = page.getByTestId('room-host-sizing').filter({ hasText: '3 cores and 8 GB' });
  await expect(row).toContainText('from the host');
  await expect(row).toContainText('never less than 2 cores, from the host');
  await expect(row).toContainText('up to 4 cores, from the host');

  // Back to a share: the choice is sent as false, because an absent key would
  // be read as "leave it alone" and the pool would keep taking its host's.
  await page.getByRole('radio', { name: 'One share of each host' }).check();
  await expect.poll(() => validated.at(-1)).toMatchObject({ size_from_profile: false });
});

test("a pool that takes its size from its hosts shows each host's size on its own page", async ({
  page,
}) => {
  await heartbeat(page);
  const installations = (await page.request.get('/api/v1/installations').then((r) => r.json())) as {
    items: { id: string }[];
  };
  const made = await page.request.post('/api/v1/pools', {
    data: {
      name: 'e2e-profile-pool',
      installation_id: installations.items[0]?.id,
      labels: ['e2e-profile-pool'],
      backend: 'docker',
      min_runners: 0,
      max_runners: 2,
      size_from_profile: true,
    },
  });
  expect(made.ok(), 'the pool was created').toBeTruthy();
  const pool = (await made.json()) as { id: string; sizing: string };
  try {
    expect(pool.sizing).toBe('profile');
    await goto(page, `/pools/${pool.id}`, 'zoomies-e2e-profile-pool');
    await expect(page.getByText('The size each host sets')).toBeVisible();
    const panel = page.getByRole('region', { name: 'Size on each host' });
    // The host that was given a size says it, and whose it is...
    await expect(
      panel.locator('li').filter({ hasText: NAME }).getByTestId('room-host-sizing'),
    ).toContainText('A runner is 3 cores and 8 GB, from the host');
    // ...and a host that was not says the fleet's default stands in, so a pool
    // that takes its size from its hosts is never a row of unexplained sizes.
    await expect(
      panel.locator('li').filter({ hasNotText: NAME }).getByTestId('room-host-sizing').first(),
    ).toContainText("from the fleet's default");
  } finally {
    await page.request.delete(`/api/v1/pools/${pool.id}?force=true`);
  }
});

test('a profile that would leave a pool nowhere to run is answered in the dialog, and can be saved anyway', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  const requests: string[] = [];
  await page.route(`**/api/v1/hosts/${hostId}*`, async (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback();
    requests.push(route.request().url());
    if (route.request().url().includes('confirm=true')) {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
      return;
    }
    await route.fulfill({
      status: 409,
      contentType: 'application/json',
      body: JSON.stringify({
        error: {
          code: 'conflict',
          message:
            'saving this host as described would leave pool zoomies-small with nowhere to run: its minimum runner size is 3 CPU, above the 2 CPU the pool states',
        },
      }),
    });
  });

  const dialog = await openDialog(page);
  await type(page, 'Minimum CPU', '3');
  await dialog.getByRole('button', { name: 'Save sizes' }).click();
  // A question, not a failure: it is answered here, with the pool named, and
  // the plain Save is replaced by the one that means it.
  const refusal = dialog.getByRole('alert');
  await expect(refusal).toContainText('a pool would have nowhere to run');
  await expect(refusal).toContainText('zoomies-small');
  await expect(dialog.getByRole('button', { name: 'Save sizes' })).toHaveCount(0);

  // Moving a figure withdraws a question about figures that are no longer there.
  await type(page, 'Minimum CPU', '2');
  await expect(refusal).toHaveCount(0);
  await type(page, 'Minimum CPU', '3');
  await dialog.getByRole('button', { name: 'Save sizes' }).click();
  await expect(refusal).toBeVisible();

  await dialog.getByRole('button', { name: 'Save anyway' }).click();
  await expect(dialog).toBeHidden();
  expect(requests.at(-1)).toContain('confirm=true');
});

test('clearing every figure hands the host back to the fleet', async ({ page }) => {
  await goto(page, '/hosts', 'Hosts');
  const dialog = await openDialog(page);
  await dialog.getByRole('button', { name: 'Follow the fleet in everything' }).click();
  await expect(dialog.getByTestId('runner-sizes-summary')).toContainText(
    '8 runners at once, as its capacity says',
  );
  await dialog.getByRole('button', { name: 'Save sizes' }).click();
  await expect(dialog).toBeHidden();

  await expect(card(page)).toContainText('0 of 8 slots in use');
  await expect(card(page).getByTestId('host-runner-sizes')).toHaveCount(0);
  const saved = await readHost(page);
  expect(saved.slots).toBe(8);
  expect(saved.runner_profile).toBeUndefined();
});

test("a host can cap pools' in-memory folders or keep them off, and the card says which", async ({
  page,
}) => {
  // Some machines have memory to spare for a pool's in-memory folders and some
  // do not, and the pool is one setting for every host it lands on, so the host
  // has the last word. This is the whole path: the dialog, the controller and the
  // card, with no pool involved -- the policy is a fact about the machine.
  await goto(page, '/hosts', 'Hosts');
  const dialog = await openDialog(page);
  const off = dialog.getByRole('checkbox', {
    name: 'Fall back to disk on this machine (temporary)',
  });
  const ceiling = dialog.getByRole('textbox', { name: 'Largest folder (MB)' });
  await expect(off).not.toBeChecked();

  // A ceiling below the floor is refused where it is typed, and Save waits.
  await ceiling.fill('32');
  await expect(dialog.getByText(/below 64 MB/)).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Save sizes' })).toBeDisabled();

  await ceiling.fill('2048');
  await dialog.getByRole('button', { name: 'Save sizes' }).click();
  await expect(dialog).toBeHidden();
  await expect(card(page).getByTestId('host-runner-sizes')).toContainText('Up to 2 GB each');
  expect((await readHost(page)).runner_profile?.tmpfs).toEqual({ max_mb: 2048 });

  // The sizes a folder is asked for here, in place of the defaults: held to the
  // floor where typed, saved with the ceiling, and said on the card.
  const sizes = await openDialog(page);
  const work = sizes.getByRole('textbox', { name: 'Work folder size (MB)' });
  await work.fill('10');
  await expect(sizes.getByText(/below 64 MB/).first()).toBeVisible();
  await expect(sizes.getByRole('button', { name: 'Save sizes' })).toBeDisabled();
  await work.fill('16384');
  await sizes.getByRole('textbox', { name: 'Docker image store size (MB)' }).fill('32768');
  await sizes.getByRole('button', { name: 'Save sizes' }).click();
  await expect(sizes).toBeHidden();
  await expect(card(page).getByTestId('host-runner-sizes')).toContainText('Work 16 GB');
  expect((await readHost(page)).runner_profile?.tmpfs).toEqual({
    max_mb: 2048,
    work_mb: 16384,
    daemon_mb: 32768,
  });

  // Keeping the folders off supersedes a ceiling: there is nothing to cap, so the
  // field goes away rather than leaving a number that means nothing.
  const again = await openDialog(page);
  await again
    .getByRole('checkbox', { name: 'Fall back to disk on this machine (temporary)' })
    .check();
  await expect(again.getByRole('textbox', { name: 'Largest folder (MB)' })).toHaveCount(0);
  await again.getByRole('button', { name: 'Save sizes' }).click();
  await expect(again).toBeHidden();
  await expect(card(page).getByTestId('host-runner-sizes')).toContainText(
    'Falling back to disk (temporary)',
  );
  // The sizes are not sent for a host that falls back to disk: there is no folder
  // to size, and the API refuses the pair.
  expect((await readHost(page)).runner_profile?.tmpfs).toEqual({ disabled: true });

  // And it can be handed back, which leaves a host that says nothing.
  const last = await openDialog(page);
  await last.getByRole('button', { name: 'Follow the fleet in everything' }).click();
  await last.getByRole('button', { name: 'Save sizes' }).click();
  await expect(last).toBeHidden();
  expect((await readHost(page)).runner_profile).toBeUndefined();
});
