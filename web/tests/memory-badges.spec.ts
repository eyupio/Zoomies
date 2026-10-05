/**
 * The small badges on the Runners page: what the memory valve has done for a
 * runner, and which of its folders are kept in memory.
 *
 * They are read here from runners the test hands the page, not from the demo
 * fleet's own, so each state is exactly the one a test is about. What the page
 * does with them is what is protected: a pill says one thing in a few words, a
 * card says all of it with figures, hover and focus and a tap all reach the card,
 * the card stays on the screen, a pill is for reading and never opens the
 * runner, and a runner with nothing to say is not given a pill to say it with.
 */
import { expect, test, type Locator, type Page } from '@playwright/test';
import { browserOverride, goto, grid, waitForRows } from './support/fixtures';

test.use(browserOverride);

const lent = {
  state: 'lent',
  mode: 'automatic',
  guaranteed_mb: 5120,
  current_mb: 6656,
  ceiling_mb: 7680,
  lent_mb: 1536,
  raises: 2,
  reason: 'raised the limit from 6144 to 6656 MB: it was using 5400 MB',
};
const swapped = {
  state: 'spilled',
  mode: 'automatic',
  guaranteed_mb: 4096,
  current_mb: 6144,
  ceiling_mb: 6144,
  lent_mb: 2048,
  spill_mb: 1024,
  spill_allowed_mb: 1024,
  blocked: 'at_ceiling',
  raises: 3,
  near_limit: true,
};
const capped = {
  state: 'watching',
  mode: 'automatic',
  guaranteed_mb: 8192,
  current_mb: 8192,
  ceiling_mb: 12288,
  lent_mb: 0,
  blocked: 'pool_empty',
  reason:
    'it is using 7900 MB of its 8192 MB limit and wants more, but the host has no spare memory left to lend',
};
const observing = {
  state: 'observing',
  mode: 'observe',
  guaranteed_mb: 4096,
  current_mb: 4096,
  ceiling_mb: 6144,
  lent_mb: 0,
  would_lend_mb: 1024,
  near_limit: true,
};
const folders = {
  in_memory: 2,
  folders: [
    {
      kind: 'work',
      label: 'Work folder',
      path: '/home/runner/_work',
      in_memory: true,
      size_mb: 2048,
      asked_mb: 4096,
      auto: true,
    },
    {
      kind: 'tmp',
      label: 'Temporary folder',
      path: '/tmp',
      in_memory: true,
      size_mb: 1024,
      asked_mb: 1024,
    },
    {
      kind: 'daemon',
      label: 'Docker image store',
      path: '/var/lib/docker',
      in_memory: false,
      asked_mb: 4096,
      auto: true,
      why: 'auto_too_small',
      note: 'Kept on disk: this runner’s memory limit left the docker image store less than the 4.0 GB it is worth having in memory.',
    },
  ],
};

type Row = Record<string, unknown>;

/**
 * The page's own event stream says nothing for the length of a test. The demo
 * fleet tells its runners' stories live, and a frame about a runner replaces what
 * the page was handed for it, so a test that hands the page a story has to be the
 * only one telling it.
 */
async function quietEvents(page: Page): Promise<void> {
  await page.route('**/api/v1/events*', (route) =>
    route.fulfill({ contentType: 'text/event-stream', body: ': connected\n\n' }),
  );
}

/**
 * Give the first rows of the grid the states a test is about, in order, and leave
 * every other row exactly as the controller sent it. Returns the rows it changed.
 */
async function giveRunners(page: Page, extras: Row[]): Promise<Row[]> {
  const changed: Row[] = [];
  await quietEvents(page);
  await page.route('**/api/v1/runners?*', async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    changed.length = 0;
    data.items = data.items.map((row: Row, index: number) => {
      // Whatever the demo fleet says of memory is replaced: the test hands the
      // page every runner's memory story, so none is left over from the seed.
      const { memory_resource: _m, scratch: _s, ...plain } = row;
      const extra = extras[index];
      if (!extra) return plain;
      const next = { ...plain, ...extra };
      changed.push(next);
      return next;
    });
    await route.fulfill({ response, json: data });
  });
  return changed;
}

const pill = (page: Page, name: RegExp | string): Locator =>
  grid(page, 'Runners').getByRole('button', { name });

/** The card a pill opens: the one bubble that is open. */
const card = (page: Page): Locator => page.locator('.memory-badge-tip .bubble:popover-open');

async function open(page: Page, trigger: Locator, isMobile: boolean): Promise<void> {
  await trigger.scrollIntoViewIfNeeded();
  if (isMobile) await trigger.tap();
  else await trigger.hover();
  await expect(card(page)).toBeVisible();
}

async function showMemoryColumn(page: Page): Promise<void> {
  // The column is a wide one, which a window under 1180px does without and a
  // phone gets back in full, as a line of its own on each runner's card.
  await expect(page.locator('td[data-label="Memory"]').first()).toBeVisible();
}

test('a runner lent memory wears a pill with the figure, and its card says everything', async ({
  page,
  isMobile,
}) => {
  await giveRunners(page, [{ memory_resource: lent }]);
  await goto(page, '/runners', 'Runners');
  await waitForRows(grid(page, 'Runners'));
  await showMemoryColumn(page);

  const lentPill = pill(page, /Lent 1\.5 GB of memory: show details/);
  await expect(lentPill).toBeVisible();
  await expect(lentPill).toContainText('+1.5 GB');

  await open(page, lentPill, isMobile);
  const tip = card(page);
  await expect(tip).toContainText('Memory valve · Automatic');
  await expect(tip).toContainText('Lent 1.5 GB of memory');
  await expect(tip).toContainText('raised twice');
  await expect(tip).toContainText('never taken back');
  for (const figure of ['5.0 GB', '6.5 GB', '7.5 GB']) await expect(tip).toContainText(figure);
  for (const word of ['Guaranteed', 'Current', 'Ceiling']) await expect(tip).toContainText(word);
  // The agent's own sentence, with its numbers.
  await expect(tip).toContainText('Raised the limit from 6144 to 6656 MB');

  // Wholly on the screen, the way a card that is the only thing about a figure has to be.
  const bounds = (await tip.boundingBox())!;
  const view = page.viewportSize()!;
  expect(bounds.x).toBeGreaterThanOrEqual(0);
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(view.width);
  expect(bounds.y).toBeGreaterThanOrEqual(0);
  expect(bounds.y + bounds.height).toBeLessThanOrEqual(view.height);

  await page.keyboard.press('Escape');
  await expect(card(page)).toHaveCount(0);
});

test('a pill is for reading: opening it does not open the runner', async ({ page, isMobile }) => {
  await giveRunners(page, [{ memory_resource: lent }]);
  await goto(page, '/runners', 'Runners');
  await waitForRows(grid(page, 'Runners'));
  const lentPill = pill(page, /^Lent 1\.5 GB of memory/);
  await lentPill.scrollIntoViewIfNeeded();
  if (isMobile) await lentPill.tap();
  else await lentPill.click();
  await expect(card(page)).toBeVisible();
  await expect(page).toHaveURL(/\/runners(\?|$)/);
});

test('the keyboard reaches the same card, and Escape puts it away', async ({ page, isMobile }) => {
  test.skip(isMobile, 'a touch screen has no keyboard to reach it with');
  await giveRunners(page, [{ memory_resource: lent }]);
  await goto(page, '/runners', 'Runners');
  await waitForRows(grid(page, 'Runners'));
  const lentPill = pill(page, /^Lent 1\.5 GB of memory/);
  await lentPill.focus();
  await expect(card(page)).toBeVisible();
  await expect(card(page)).toContainText('Lent 1.5 GB of memory');
  // What a screen reader is given is the whole card as one sentence.
  const described = await lentPill.getAttribute('aria-describedby');
  await expect(page.locator(`[id="${described}"]`)).toContainText('never taken back');
  await page.keyboard.press('Escape');
  await expect(card(page)).toHaveCount(0);
  await expect(lentPill).toBeFocused();
});

test('swap, a runner that wants more, and an observing valve are each told apart', async ({
  page,
  isMobile,
}) => {
  await giveRunners(page, [
    { memory_resource: swapped },
    { memory_resource: capped },
    { memory_resource: observing },
  ]);
  await goto(page, '/runners', 'Runners');
  await waitForRows(grid(page, 'Runners'));

  // Swap is its own pill beside the loan it follows, and the loan's pill carries
  // a mark because the runner wants more than it may have.
  const loan = pill(page, /Lent 2\.0 GB of memory, and it wants more: show details/);
  await expect(loan).toBeVisible();
  await expect(loan).toHaveAttribute('data-wanting', 'true');
  const swap = pill(page, /^May use 1\.0 GB of swap/);
  await expect(swap).toHaveText('Swap');
  await expect(swap).toHaveAttribute('data-tone', 'pending');
  await open(page, swap, isMobile);
  await expect(card(page)).toContainText('slows down instead of being killed');
  await expect(card(page)).toContainText('up to 1.0 GB of swap');
  await page.keyboard.press('Escape');

  // A runner the host had no memory for, which has no loan to be told apart by.
  const hostFull = pill(page, /^Its host had no memory to lend/);
  await expect(hostFull).toHaveText('Host full');
  await open(page, hostFull, isMobile);
  await expect(card(page)).toContainText('wants more');
  await expect(card(page)).toContainText('the host has no spare memory left to lend');
  await page.keyboard.press('Escape');

  // An observing pool's pill is dashed: it says what would have happened.
  const watching = pill(page, /^Would have lent 1\.0 GB/);
  await expect(watching).toHaveText('~1.0 GB');
  await expect(watching).toHaveClass(/dashed/);
  await open(page, watching, isMobile);
  await expect(card(page)).toContainText('changes nothing');
  await expect(card(page)).toContainText('Would hold');
});

test('folders in memory are one pill with an icon for each, and the card says what stayed on disk', async ({
  page,
  isMobile,
}) => {
  await giveRunners(page, [{ scratch: folders }]);
  await goto(page, '/runners', 'Runners');
  await waitForRows(grid(page, 'Runners'));

  const memory = pill(page, /2 of 3 folders in memory: show details/);
  await expect(memory).toBeVisible();
  await expect(memory).toContainText('3.0 GB');
  // An icon for each folder that is in memory, and none for the one that is not.
  await expect(memory.locator('svg')).toHaveCount(2);

  await open(page, memory, isMobile);
  const tip = card(page);
  await expect(tip).toContainText('In-memory folders');
  await expect(tip).toContainText('Work folder');
  await expect(tip).toContainText('/home/runner/_work');
  await expect(tip).toContainText('2.0 GB');
  await expect(tip).toContainText('Auto');
  await expect(tip).toContainText('fitted to its limit from 4.0 GB');
  await expect(tip).toContainText('Temporary folder');
  await expect(tip).toContainText('/tmp');
  await expect(tip).toContainText('Docker image store');
  await expect(tip).toContainText('On disk');
  await expect(tip).toContainText(
    'Kept on disk: this runner’s memory limit left the docker image store',
  );
});

test('a pool that asked for memory and gave this runner none says so, and a plain runner has no pill', async ({
  page,
}) => {
  await giveRunners(page, [
    {
      scratch: {
        in_memory: 0,
        folders: [
          {
            ...folders.folders[0],
            in_memory: false,
            size_mb: undefined,
            why: 'host_off',
            note: 'Kept on disk: this host’s owner has turned in-memory folders off.',
          },
        ],
      },
    },
  ]);
  await goto(page, '/runners', 'Runners');
  const rows = await waitForRows(grid(page, 'Runners'));
  const onDisk = pill(page, /^Kept on disk, though its pool asked for memory/);
  await expect(onDisk).toHaveText('On disk');
  await expect(onDisk).toHaveClass(/dashed/);
  // Every other row was handed nothing, and is drawn with nothing.
  const badged = await rows.filter({ has: page.getByTestId('memory-badges') }).count();
  expect(badged).toBe(1);
});

test("the runner's page repeats it in a panel, and counts a loan in the memory it is allowed", async ({
  page,
}) => {
  await quietEvents(page);
  const runners = await page.request.get('/api/v1/runners?limit=1').then((r) => r.json());
  const target = (runners.items as Row[])[0]!;
  await page.route(`**/api/v1/runners/${target.id}`, async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    await route.fulfill({
      response,
      json: {
        ...data,
        allocated_memory_mb: 5120,
        memory_bytes: 5400 * 1024 * 1024,
        memory_resource: lent,
        scratch: folders,
      },
    });
  });
  await goto(page, `/runners/${target.id}`, String(target.name));

  // The pills are in the header beside the status, with the valve's first.
  const header = page.getByTestId('memory-badges').first();
  await expect(header.getByRole('button', { name: /Lent 1\.5 GB of memory/ })).toBeVisible();
  await expect(header.getByRole('button', { name: /2 of 3 folders in memory/ })).toBeVisible();

  // And the panel is the same cards, drawn open.
  const panel = page.getByTestId('memory-cards');
  await expect(panel).toContainText('Lent 1.5 GB of memory');
  await expect(panel).toContainText('Guaranteed');
  await expect(panel).toContainText('2 of 3 folders in memory');
  await expect(panel).toContainText('Kept on disk');

  // The usage bar is measured against what the runner may hold now, not what it
  // was created with: 5.4 GB used against a 5 GB limit would read as past it.
  await expect(page.getByText('of 6.5 GB allowed, 1.5 GB of it lent')).toBeVisible();
});

test("a frame that says the runner holds no loan takes the loan off the runner's page", async ({
  page,
}) => {
  // A frame is the runner's whole view, and a view leaves out what a runner has
  // none of. The page merges frames into the detail it fetched, and a merge that
  // only added would keep the loan, and the card for it, after the pool switched
  // its valve off -- an operator would be told, on the page they opened to find
  // out, about memory the runner no longer has.
  await quietEvents(page);
  const runners = await page.request.get('/api/v1/runners?limit=1').then((r) => r.json());
  const target = (runners.items as Row[])[0]!;
  await page.route(`**/api/v1/runners/${target.id}`, async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    await route.fulfill({ response, json: { ...data, memory_resource: lent, scratch: folders } });
  });
  await goto(page, `/runners/${target.id}`, String(target.name));
  const panel = page.getByTestId('memory-cards');
  await expect(panel).toContainText('Lent 1.5 GB of memory');

  const { memory_resource: _m, scratch: _s, ...bare } = target;
  await page.route('**/api/v1/events*', (route) =>
    route.fulfill({
      status: 200,
      headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
      body: `id: 999201\nevent: runner.updated\ndata: ${JSON.stringify(bare)}\n\n`,
    }),
  );
  await page.evaluate(() => {
    window.dispatchEvent(new Event('offline'));
    window.dispatchEvent(new Event('online'));
  });

  await expect(panel).toHaveCount(0, { timeout: 10_000 });
  await expect(page.getByTestId('memory-badges')).toHaveCount(0);
});
