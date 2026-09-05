/**
 * The pages keep themselves current.
 *
 * There is no refresh button anywhere in Zoomies, and this is what protects
 * the promise behind that: a change made somewhere else -- another operator's
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
