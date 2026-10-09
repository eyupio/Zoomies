/**
 * Settings → Updates: what the update mode would take, read from the controller.
 *
 * It has a controller of its own, on the desktop project and on the phone one.
 * The binary every other project drives is a `dev` build, which no release
 * comparison accepts, so on the shared fixture this page can only ever say that
 * the build is left alone. This controller is told to report 1.3.0 and to read a
 * release list that holds v1.3.1 and v1.3.2 (ZOOMIES_SEED_UPDATES), in manual
 * mode, which is the mode whose sentences do not move with the clock.
 *
 * The page reads and installs nothing. What it must not do is as much of the
 * contract as what it says, so some of these are about the controls that are
 * absent, and the states the seed cannot make -- auto inside its soak, a list
 * nobody has read, an address that is not https -- are served to the page as the
 * documents the controller would send.
 */
import { expect, test, type Page } from '@playwright/test';
import type { UpdatesStatus } from '../src/lib/api/types';
import {
  browserOverride,
  documentWidth,
  expectNoReload,
  goto,
  pageHeading,
  plantMarker,
} from './support/fixtures';

test.use(browserOverride);

const PAGE = '/settings/updates';
const HOUR = 3_600_000;
const MINUTE = 60_000;

/** The controller's own sentence for a manual target that is ahead of the build. */
const SEEDED_REASON =
  'Available: v1.3.2 is newer than the v1.3.0 running now; manual mode waits for someone to update.';

const take = (page: Page) =>
  page.getByRole('region', { name: 'What an update would take', exact: true });
const mode = (page: Page) => page.getByRole('region', { name: 'Update mode', exact: true });
const controller = (page: Page) =>
  page.getByRole('region', { name: 'This controller', exact: true });

/** How far the page can be scrolled sideways, which must be nowhere. */
async function expectNoSidewaysScroll(page: Page, where: string): Promise<void> {
  const { scrollWidth, clientWidth } = await documentWidth(page);
  expect(
    scrollWidth,
    `${where} is ${scrollWidth}px wide in a ${clientWidth}px window, so the page scrolls sideways`,
  ).toBeLessThanOrEqual(clientWidth);
}

/**
 * Answer the page's request for the status with a document of the test's own.
 *
 * The status is read by one request and repainted by one event, so serving the
 * request is enough to show the page any state the controller could be in.
 */
async function serve(page: Page, document: () => UpdatesStatus): Promise<void> {
  await page.route('**/api/v1/updates', (route) => route.fulfill({ json: document() }));
}

/** Auto, with v1.3.2 six hours into a day's soak and v1.3.1 passed over for it. */
function waiting(): UpdatesStatus {
  const now = Date.now();
  return {
    mode: 'auto',
    soak: '24h',
    running: { version: '1.3.0', release: true },
    latest: {
      tag: 'v1.3.2',
      url: 'https://example.invalid/releases/v1.3.2',
      published_at: new Date(now - 6 * HOUR).toISOString(),
    },
    target: {
      tag: 'v1.3.2',
      newer: true,
      // Five minutes past the hour boundary, so the sentence is not one tick
      // from reading seventeen.
      due_at: new Date(now + 18 * HOUR + 5 * MINUTE).toISOString(),
    },
    reason:
      'Waiting: v1.3.2 has been public for 6 hours and auto waits for 24; it can be taken in 18 hours. ' +
      'It replaced v1.3.1, which is skipped, and a newer release would start the wait again.',
    checked_at: new Date(now - 2 * MINUTE).toISOString(),
    helper: {
      state: 'missing',
      reason: "No update helper is installed on this controller's host.",
      install_command: 'sudo zoomies updates helper install',
    },
    controller: null,
  };
}

test('the page says what the seeded release list would take, for a build on 1.3.0', async ({
  page,
}) => {
  await goto(page, PAGE, 'Updates');

  await expect(controller(page).getByText('1.3.0', { exact: true })).toBeVisible();
  await expect(take(page)).toContainText('Available');
  await expect(take(page)).toContainText(
    'Manual would offer v1.3.2 and wait for a person to take it.',
  );
  // The controller's sentence is shown as it was given, not as this page would
  // have put it.
  await expect(take(page).getByText(SEEDED_REASON)).toBeVisible();
  await expect(take(page)).toContainText(/Published\s*\d+h ago/);
  await expect(take(page)).toContainText('Last checked');
});

test('the mode is read as text, with what it would do beside it, and is not a control', async ({
  page,
}) => {
  await goto(page, PAGE, 'Updates');

  await expect(mode(page)).toContainText('Manual');
  await expect(mode(page)).toContainText(
    'Zoomies would offer the newest release that can be installed on this system and wait for a person to take it.',
  );
  await expect(mode(page)).toContainText('24 hours');
  // Where the controls are is a link, for the roles that can open that page.
  await expect(mode(page).getByRole('link', { name: 'the Configuration page' })).toHaveAttribute(
    'href',
    '/settings/configuration?setting=updates.mode',
  );

  // Nothing on the page chooses a mode or types a soak.
  for (const role of [
    'radiogroup',
    'radio',
    'combobox',
    'listbox',
    'switch',
    'checkbox',
    'textbox',
    'spinbutton',
    'slider',
  ] as const) {
    await expect(page.getByRole('main').getByRole(role), `a ${role} on the page`).toHaveCount(0);
  }
});

test('nothing on the page updates anything: the one button reads the page again', async ({
  page,
}) => {
  await goto(page, PAGE, 'Updates');
  await expect(take(page)).toBeVisible();

  const main = page.getByRole('main');
  await expect(main.getByRole('button', { name: 'Refresh', exact: true })).toBeVisible();
  await expect(
    main.getByRole('button', { name: /update|install|apply|upgrade|restart/i }),
  ).toHaveCount(0);
  await expect(
    main.getByRole('link', { name: /update now|install|apply|upgrade|restart/i }),
  ).toHaveCount(0);
});

test('the release links to its page, and that link leaves in a new tab and says so', async ({
  page,
}) => {
  await goto(page, PAGE, 'Updates');

  const link = take(page).getByRole('link', { name: /^v1\.3\.2/ });
  await expect(link).toHaveAttribute('href', 'https://example.invalid/releases/v1.3.2');
  await expect(link).toHaveAttribute('target', '_blank');
  await expect(link).toHaveAttribute('rel', /noopener/);
  await expect(link).toHaveAccessibleName(/\(opens in a new tab\)$/);
});

test('the Updates page is one h1 with named controls and headings that skip no level', async ({
  page,
}) => {
  await goto(page, PAGE, 'Updates');
  await expect(take(page)).toBeVisible();

  await expect(page.getByRole('heading', { level: 1 })).toHaveCount(1);
  await expect(page.getByRole('main')).toHaveCount(1);
  await expect(page.getByRole('navigation', { name: 'Sections' })).toHaveCount(1);
  await expect(page.getByRole('button', { name: /^$/ }), 'every button has a name').toHaveCount(0);
  await expect(page.getByRole('link', { name: /^$/ }), 'every link has a name').toHaveCount(0);
  await expect(
    page.locator('[tabindex]:not([tabindex="0"]):not([tabindex="-1"])'),
    'the tab order is the document order',
  ).toHaveCount(0);

  const levels = await page
    .getByRole('main')
    .getByRole('heading')
    .evaluateAll((els) => els.map((el) => Number(el.tagName.slice(1))));
  expect(levels[0], 'the outline starts at the page heading').toBe(1);
  expect(
    levels.filter((level, i) => i > 0 && level > (levels[i - 1] ?? 0) + 1),
    `heading levels in order: ${levels.join(', ')}`,
  ).toEqual([]);

  // A list of facts holds terms and their descriptions and nothing else.
  const strays = await page.evaluate(() =>
    Array.from(document.querySelectorAll('dl > *'))
      .filter((child) => !['DT', 'DD'].includes(child.tagName))
      .map((child) => child.tagName),
  );
  expect(strays, 'a definition list holds only dt and dd').toEqual([]);
});

test('nothing scrolls sideways at 360px', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 780 });
  await goto(page, PAGE, 'Updates');
  await expect(take(page)).toBeVisible();
  await expectNoSidewaysScroll(page, 'the Updates page');
});

test('auto inside its soak says when it would take the release, and what a newer one costs', async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 780 });
  await serve(page, waiting);
  await goto(page, PAGE, 'Updates');

  await expect(take(page)).toContainText('Waiting');
  await expect(take(page)).toContainText('Auto would take v1.3.2 in 18 hours.');
  // Both sentences of the reason, wrapped inside the window rather than sized
  // for one line.
  const reason = take(page).getByText(/^Waiting: v1\.3\.2 has been public for 6 hours/);
  await expect(reason).toContainText('It replaced v1.3.1, which is skipped');
  const box = await reason.boundingBox();
  expect(box && box.x >= 0 && box.x + box.width <= 360, 'the reason sits inside the window').toBe(
    true,
  );

  await expect(mode(page)).toContainText('Auto');
  await expect(mode(page)).toContainText('A newer release restarts the wait');
  await expect(mode(page)).toContainText('24 hours');
  await expectNoSidewaysScroll(page, 'the Updates page waiting out a soak');
});

test('an address that is not https is shown as the tag it names and never linked', async ({
  page,
}) => {
  let url = '';
  await serve(page, () => {
    const status = waiting();
    return { ...status, latest: status.latest && { ...status.latest, url } };
  });

  for (url of ['javascript:alert(1)', 'http://example.invalid/releases/v1.3.2', '']) {
    await goto(page, PAGE, 'Updates');
    await expect(take(page).getByText('v1.3.2', { exact: true })).toBeVisible();
    await expect(
      take(page).getByRole('link'),
      `a link was drawn for the address ${JSON.stringify(url)}`,
    ).toHaveCount(0);
  }
});

test('a list nobody has read is said to be unread, with no release named', async ({ page }) => {
  await serve(page, () => ({
    ...waiting(),
    mode: 'manual',
    latest: null,
    target: null,
    checked_at: null,
    reason:
      'Zoomies has not read the release list yet. It reads it every 24h, so the next read is due within that time.',
  }));
  await goto(page, PAGE, 'Updates');

  await expect(take(page)).toContainText('Not read yet');
  await expect(take(page)).toContainText('Zoomies has not read the release list yet.');
  await expect(take(page)).not.toContainText('Newest release');
  await expect(take(page).getByRole('link')).toHaveCount(0);
});

test('with updating off the page says so and offers nothing', async ({ page }) => {
  await serve(page, () => ({
    ...waiting(),
    mode: 'off',
    latest: null,
    target: null,
    checked_at: null,
    reason: 'Updates are off. Set updates.mode to manual or auto to be offered new releases.',
  }));
  await goto(page, PAGE, 'Updates');

  await expect(take(page)).toContainText('Off');
  await expect(take(page)).toContainText(
    'Updates are off. Set updates.mode to manual or auto to be offered new releases.',
  );
  await expect(mode(page)).toContainText('Zoomies tells you when a newer release exists');
  await expect(take(page)).not.toContainText(/(Auto|Manual) would/);
});

test('a status that cannot be read says so, and asking again shows the page', async ({ page }) => {
  let failing = true;
  await page.route('**/api/v1/updates', (route) =>
    failing
      ? route.fulfill({
          status: 500,
          json: {
            error: { code: 'internal', message: 'the update status could not be worked out' },
          },
        })
      : route.fallback(),
  );
  await goto(page, PAGE, 'Updates');

  const alert = page.getByRole('alert');
  await expect(alert).toContainText('The update status could not be worked out.');
  failing = false;
  await alert.getByRole('button', { name: 'Try again' }).click();
  await expect(take(page)).toContainText('Manual would offer v1.3.2');
});

test('a refresh that does not get through keeps what the page had and says so', async ({
  page,
}) => {
  await goto(page, PAGE, 'Updates');
  await expect(take(page)).toContainText('Available');

  await page.route('**/api/v1/updates', (route) =>
    route.fulfill({
      status: 500,
      json: { error: { code: 'internal', message: 'the update status could not be worked out' } },
    }),
  );
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  const notice = page
    .getByRole('status')
    .filter({ hasText: 'The last refresh did not get through' });
  await expect(notice).toBeVisible();
  await expect(take(page)).toContainText('Manual would offer v1.3.2');

  await page.unroute('**/api/v1/updates');
  await notice.getByRole('button', { name: 'Try again' }).click();
  await expect(notice).toBeHidden();
});

/** Move the soak, the one thing about the seeded controller a spec may change: the environment pins its mode. */
const setSoak = (page: Page, soak: string) =>
  page.request.patch('/api/v1/settings', { data: { 'updates.soak': soak } });

// The status is computed, not stored: no row is written when a soak changes, so
// the event is the only thing that tells an open page. A read of it is answered
// from here on with the document the page already holds, so the only way to the
// new figure is the frame.
test('the page repaints from the event stream without a reload', async ({ page }) => {
  await goto(page, PAGE, 'Updates');
  // A frame sent before the stream is open is a frame nobody hears.
  await expect(page.locator('.connection')).toHaveAttribute('data-state', 'live');
  await expect(mode(page)).toContainText('24 hours');
  await plantMarker(page);

  const held = (await (await page.request.get('/api/v1/updates')).json()) as UpdatesStatus;
  await page.route('**/api/v1/updates', (route) => route.fulfill({ json: held }));
  try {
    expect((await setSoak(page, '12h')).ok(), 'the soak was changed').toBeTruthy();
    // A change to a setting runs a pass of the controller's loop at once, and
    // the status is worked out after every pass; ten seconds is the longest
    // wait for the next one.
    await expect(mode(page)).toContainText('12 hours', { timeout: 20_000 });
    await expectNoReload(page);
  } finally {
    await page.unroute('**/api/v1/updates');
    await setSoak(page, '24h');
  }
});

// Nothing is replayed to a stream that has only just opened, so a page that
// could not hear the controller when a change was made learns of it by asking
// again as soon as it can. A second page hears the change and so spares the
// first the frame the controller would otherwise send it on reconnecting: the
// controller sends a status once, to whoever is listening when it changes.
test('a page that could not hear the controller asks again when the stream returns', async ({
  page,
  context,
}) => {
  let cut = true;
  await page.route('**/api/v1/events*', (route) =>
    cut ? route.abort('connectionfailed') : route.fallback(),
  );
  await goto(page, PAGE, 'Updates');
  await expect(mode(page)).toContainText('24 hours');
  await expect(page.locator('.connection')).not.toHaveAttribute('data-state', 'live');
  await plantMarker(page);

  const listener = await context.newPage();
  try {
    await goto(listener, PAGE, 'Updates');
    await expect(listener.locator('.connection')).toHaveAttribute('data-state', 'live');
    expect((await setSoak(page, '12h')).ok(), 'the soak was changed').toBeTruthy();
    await expect(mode(listener)).toContainText('12 hours', { timeout: 20_000 });
    await expect(mode(page), 'the page that could not hear is as it was').toContainText('24 hours');

    cut = false;
    await page.evaluate(() => window.dispatchEvent(new Event('online')));
    await expect(page.locator('.connection')).toHaveAttribute('data-state', 'live', {
      timeout: 20_000,
    });
    await expect(mode(page)).toContainText('12 hours');
    await expectNoReload(page);
  } finally {
    await listener.close();
    await page.unroute('**/api/v1/events*');
    await setSoak(page, '24h');
  }
});

// The page follows the stream for as long as it is open. One that kept listening
// after the operator had gone elsewhere would read the status again every time
// the stream came back, for a page nobody can see, for as long as the tab lived.
// The control is the same cycle on this page, which must ask: without it, a page
// that never asked again would pass for one that had stopped.
test('the page stops listening to the stream when the operator leaves it', async ({ page }) => {
  let reads = 0;
  page.on('request', (request) => {
    if (request.method() === 'GET' && new URL(request.url()).pathname === '/api/v1/updates') {
      reads += 1;
    }
  });
  let cut = false;
  await page.route('**/api/v1/events*', (route) =>
    cut ? route.abort('connectionfailed') : route.fallback(),
  );
  const connection = page.locator('.connection');
  /** The stream drops and comes back, which is when a page that follows it asks again. */
  const dropAndRestore = async () => {
    cut = true;
    await page.evaluate(() => window.dispatchEvent(new Event('offline')));
    await expect(connection).not.toHaveAttribute('data-state', 'live');
    cut = false;
    await page.evaluate(() => window.dispatchEvent(new Event('online')));
    await expect(connection).toHaveAttribute('data-state', 'live', { timeout: 20_000 });
  };

  try {
    await goto(page, PAGE, 'Updates');
    await expect(connection).toHaveAttribute('data-state', 'live');
    await expect(mode(page)).toContainText('24 hours');
    await plantMarker(page);

    reads = 0;
    await dropAndRestore();
    await expect.poll(() => reads, { message: 'the open page asks again' }).toBeGreaterThan(0);

    // Leave through the router, as a link does, so nothing reloads and only the
    // page's own teardown can stop it.
    await page.evaluate(() => {
      history.pushState({}, '', '/settings/about');
      window.dispatchEvent(new PopStateEvent('popstate'));
    });
    await expect(pageHeading(page, 'About')).toBeVisible();
    await expect(pageHeading(page, 'Updates')).toBeHidden();
    await expectNoReload(page);

    reads = 0;
    await dropAndRestore();
    // Nothing is awaited that could show a read that did not happen, so give one
    // that would a moment to be sent.
    await page.waitForTimeout(1_000);
    expect(reads, 'reads of the status after the page was left').toBe(0);
  } finally {
    await page.unroute('**/api/v1/events*');
  }
});

// A read and a frame can cross: the read was sent first and answers last, with
// a document the frame has already replaced. The frame is the newer of the two.
test('a read that was in flight when a frame landed does not put its older answer over it', async ({
  page,
}) => {
  await goto(page, PAGE, 'Updates');
  await expect(page.locator('.connection')).toHaveAttribute('data-state', 'live');
  await expect(mode(page)).toContainText('24 hours');

  const older = (await (await page.request.get('/api/v1/updates')).json()) as UpdatesStatus;
  let answer!: () => void;
  const held = new Promise<void>((resolve) => (answer = resolve));
  await page.route('**/api/v1/updates', async (route) => {
    await held;
    await route.fulfill({ json: older });
  });
  const refresh = page.getByRole('button', { name: 'Refresh', exact: true });
  try {
    await refresh.click();
    expect((await setSoak(page, '12h')).ok(), 'the soak was changed').toBeTruthy();
    await expect(mode(page)).toContainText('12 hours', { timeout: 20_000 });

    // The read answers now, with the document from before the change, and the
    // refresh ends: that is when it would have been put over the frame.
    answer();
    await expect(refresh).toHaveAttribute('title', /^Refreshed /);
    await expect(mode(page)).toContainText('12 hours');
  } finally {
    answer();
    await page.unroute('**/api/v1/updates');
    await setSoak(page, '24h');
  }
});

test('the notice that a newer release exists opens this page from the problems drawer', async ({
  page,
}) => {
  await goto(page, '/');
  await page.getByRole('button', { name: /^Problems\./ }).click();
  const drawer = page.getByRole('dialog', { name: 'Problems' });
  await expect(drawer).toBeVisible();

  const notice = drawer.getByRole('listitem').filter({ hasText: 'controller.update_available' });
  await expect(notice).toContainText('this controller is running 1.3.0');
  await notice.getByRole('link', { name: 'Open Updates' }).click();

  await expect(page).toHaveURL(/\/settings\/updates$/);
  await expect(pageHeading(page, 'Updates')).toBeVisible();
});
