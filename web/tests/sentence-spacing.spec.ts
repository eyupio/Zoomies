/**
 * A sentence the page builds across an `{#if}` has to keep the space between its
 * halves. Svelte drops the whitespace at the start of a block, so
 * `says.{#if x}\n  The next{/if}` renders "says.The next", and nothing in the
 * markup looks wrong. These read what the page says, in every state that joins
 * two sentences this way, with the gap as part of the match: `toContainText('The
 * next')` passes either way, and a pattern that needs whitespace between the words
 * fails when there is none.
 */
import { expect, test, type Page } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);

/**
 * A sentence as a pattern. The markup wraps long sentences across lines, so the
 * words are joined by any whitespace; but there is always some, which is the whole
 * point: "lands.Twice" does not match "lands. Twice".
 */
function said(text: string): RegExp {
  const escape = (word: string) => word.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return new RegExp(text.trim().split(/\s+/).map(escape).join('\\s+'));
}

test('the hosts summary keeps the space before the private hosts it counts', async ({ page }) => {
  await goto(page, '/hosts', 'Hosts');
  await expect(page.locator('p.summary')).toContainText(
    /runner\s+slots\s+in\s+use\s+·\s+\d+\s+via\s+Tailcat/,
  );
});

/**
 * Open a pool's page with some of its fields laid over what the controller says.
 * The page reads the pool from its own address and from the list of pools, so both
 * answers carry the overlay, or the page shows whichever it heard last.
 */
async function openPool(page: Page, overlay: Record<string, unknown>): Promise<void> {
  const list = (await page.request.get('/api/v1/pools').then((r) => r.json())) as {
    items: { id: string; name: string }[];
  };
  const first = list.items[0];
  if (!first) throw new Error('the fixture has no pool to open');
  const { id, name } = first;
  const lay = <T extends { id: string }>(pool: T): T =>
    pool.id === id ? { ...pool, ...overlay } : pool;
  await page.route(/\/api\/v1\/pools(\?.*)?$/, async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const response = await route.fetch();
    const body = (await response.json()) as { items: { id: string }[] };
    await route.fulfill({ response, json: { ...body, items: body.items.map(lay) } });
  });
  await page.route(new RegExp(`/api/v1/pools/${id}$`), async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const response = await route.fetch();
    await route.fulfill({ response, json: lay((await response.json()) as { id: string }) });
  });
  await goto(page, `/pools/${id}`, name);
}

const dind = { docker_mode: 'dind' };

test.describe('the runner-size sentences on a pool page', () => {
  test('a pool of a fixed size says what a Docker daemon costs, after the sentence before it', async ({
    page,
  }) => {
    await openPool(page, { ...dind, sizing: 'fixed', resources: { cpus: 2, memory_mb: 4096 } });
    await expect(page.locator('main')).toContainText(
      said('wherever it lands. Twice over: the build runs in a Docker daemon'),
    );
  });

  test('a pool sized to the slot says the daemon and the runner share it, and then how', async ({
    page,
  }) => {
    await openPool(page, { ...dind, sizing: 'automatic', resources: {} });
    await expect(page.locator('main')).toContainText(
      said(
        `follows one that is resized. Its runner and its Docker daemon share that slot,
         so a slot here is one runner like anywhere else. They split it evenly.`,
      ),
    );
  });

  test('a daemon given the same share of CPU and memory says so once', async ({ page }) => {
    await openPool(page, { ...dind, sizing: 'automatic', resources: { daemon_share_percent: 30 } });
    await expect(page.locator('main')).toContainText(
      said('like anywhere else. The daemon is given 30% of it and the runner 70%.'),
    );
  });

  test('a daemon given different shares says each', async ({ page }) => {
    await openPool(page, {
      ...dind,
      sizing: 'automatic',
      resources: { daemon_cpu_share_percent: 30, daemon_memory_share_percent: 40 },
    });
    await expect(page.locator('main')).toContainText(
      said(
        `like anywhere else. The daemon is given 30% of its CPU and 40% of its memory;
         the runner keeps the rest.`,
      ),
    );
  });

  test('a pool sized by its host says what a host with no size gives, and then the daemon', async ({
    page,
  }) => {
    await openPool(page, {
      ...dind,
      sizing: 'profile',
      size_from_profile: true,
      fleet_standard: { cpus: 4, memory_mb: 8192 },
    });
    const main = page.locator('main');
    await expect(main).toContainText(
      said("runner sizes say. A host that sets none gives the fleet's default, 4 CPU and"),
    );
    // The figure before the full stop is the fleet's memory, which this does not care about.
    await expect(main).toContainText(
      /\w\.\s+Its\s+runner\s+and\s+its\s+Docker\s+daemon\s+share\s+that\s+size/,
    );
  });

  test('a pool sized by its host, with no fleet default, goes straight to the daemon', async ({
    page,
  }) => {
    await openPool(page, { ...dind, sizing: 'profile', size_from_profile: true });
    await expect(page.locator('main')).toContainText(
      said('runner sizes say. Its runner and its Docker daemon share that size'),
    );
  });
});
