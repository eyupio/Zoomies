/**
 * The status page, as the reader it is for sees it: signed out, on a desktop
 * and on a phone, and through the same accessibility checks as the app.
 *
 * The page's promise is that it names nothing -- no pool, host, repository,
 * runner or job -- and the proof is a search: every name the fleet has,
 * looked for in the rendered page. The names are read from the diagnostics
 * fixture, which seeds the same deterministic fleet with authentication off,
 * because this fixture has authentication on and the test is signed out.
 */
import { devices, expect, test, type Page } from '@playwright/test';
import { browserOverride } from './support/fixtures';

test.use(browserOverride);

/** The diagnostics fixture's address: STUCK_PORT in playwright.config.ts. */
const SAME_FLEET = 'http://127.0.0.1:8097';

type Items = { items: Array<Record<string, unknown>> };

/** Every name the fleet has, from the fixture that can be read without signing in. */
async function fleetNames(page: Page): Promise<string[]> {
  const read = async (path: string, fields: string[]): Promise<string[]> => {
    const res = await page.request.get(`${SAME_FLEET}${path}`);
    expect(res.ok(), `GET ${path} on the diagnostics fixture`).toBe(true);
    const body = (await res.json()) as Items;
    return body.items.flatMap((item) =>
      fields.map((f) => item[f]).filter((v): v is string => typeof v === 'string'),
    );
  };
  const names = [
    ...(await read('/api/v1/pools', ['name'])),
    ...(await read('/api/v1/hosts', ['name'])),
    ...(await read('/api/v1/installations', ['target'])),
    ...(await read('/api/v1/runners?limit=500', ['name'])),
    ...(await read('/api/v1/jobs?limit=500', ['repo', 'workflow', 'job_name'])),
  ];
  return [...new Set(names.filter((n) => n.length > 0))];
}

async function openStatus(page: Page): Promise<void> {
  await page.goto('/status', { waitUntil: 'domcontentloaded' });
  await expect(page.getByRole('heading', { level: 1, name: 'Fleet status' })).toBeVisible();
  // The state word is the first thing the poll fills in.
  await expect(page.locator('#state-word')).toHaveText(/^(healthy|degraded|blocked)$/);
}

test.describe('signed out', () => {
  test('the page shows the fleet in trouble and names nothing in it', async ({ page }) => {
    // Nothing is signed in: the fixture has authentication on and this
    // context has no cookie, so this is what somebody with no account sees.
    expect(await page.context().cookies()).toHaveLength(0);
    await openStatus(page);

    // The diagnostics fleet has a pool nothing can place, so it is blocked,
    // and the page says why in words rather than only in a colour.
    await expect(page.locator('#state-word')).toHaveText('blocked');
    await expect(page.getByRole('heading', { name: 'What is happening' })).toBeVisible();
    await expect(page.getByText('Blocking').first()).toBeVisible();

    const names = await fleetNames(page);
    expect(names.length, 'the fixture has names to search for').toBeGreaterThan(8);
    const text = (await page.locator('body').innerText()) + (await page.content());
    const found = names.filter((n) => text.includes(n));
    expect(found, 'names from the fleet that reached the status page').toEqual([]);
  });

  test('the page never opens the event stream', async ({ page }) => {
    const requests: string[] = [];
    page.on('request', (r) => requests.push(new URL(r.url()).pathname));
    await openStatus(page);
    expect(requests).toContain('/api/v1/status');
    expect(requests.filter((p) => p.startsWith('/api/v1/events'))).toEqual([]);
  });

  test('the badge is the same state as an image', async ({ page }) => {
    const res = await page.request.get('/status.svg');
    expect(res.ok()).toBe(true);
    expect(res.headers()['content-type']).toBe('image/svg+xml');
    expect(await res.text()).toContain('fleet: blocked');
  });

  test('the app itself still asks for a sign-in', async ({ page }) => {
    const res = await page.request.get('/api/v1/pools');
    expect(res.status()).toBe(401);
  });
});

test.describe('accessibility', () => {
  test('the page has one h1, a main landmark and named links', async ({ page }) => {
    await openStatus(page);
    await expect(page.getByRole('heading', { level: 1 })).toHaveCount(1);
    await expect(page.getByRole('main')).toHaveCount(1);
    await expect(page.getByRole('link', { name: /^$/ })).toHaveCount(0);
    await expect(page.getByRole('button', { name: /^$/ })).toHaveCount(0);
    expect(await page.locator('[tabindex]:not([tabindex="0"]):not([tabindex="-1"])').count()).toBe(
      0,
    );
    // The state is announced when it changes, not only drawn.
    await expect(page.locator('[aria-live="polite"]')).toHaveCount(1);
  });
});

const { defaultBrowserType: _phoneBrowser, ...phone } = devices['Pixel 7'];

test.describe('on a phone', () => {
  test.use(phone);

  test('the page fits the screen without scrolling sideways', async ({ page }) => {
    await openStatus(page);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
    await expect(page.getByRole('heading', { name: 'The queue' })).toBeVisible();
  });
});
