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
 * The seed also stands in for the update helper: the folder it was given holds
 * the marker an installed helper leaves, so the Update button is offered, and a
 * press writes its request there. Nothing answers it. The spec plays the helper
 * by writing a result.json into that folder, which is how an attempt ends here,
 * and it reads the folder to see what the controller asked for.
 *
 * What the page must not do is as much of the contract as what it says, so some
 * of these are about the controls that are absent, and the states the seed
 * cannot make (auto inside its soak, a list nobody has read, an address that is
 * not https, a controller that restarted onto a new build) are served to the
 * page as the documents the controller would send.
 */
import { existsSync, readFileSync, renameSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, test, type Page } from '@playwright/test';
import type { UpdatesStatus } from '../src/lib/api/types';
import {
  browserOverride,
  documentWidth,
  expectNoReload,
  goto,
  pageHeading,
  plantMarker,
  reload,
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
      upgrade_command: '',
    },
    controller: null,
    rollout: null,
  };
}

test('the page says what the seeded release list would take, for a build on 1.3.0', async ({
  page,
}) => {
  await goto(page, PAGE, 'Updates');

  await expect(controller(page).getByText('1.3.0', { exact: true })).toBeVisible();
  await expect(take(page)).toContainText('Available');
  await expect(take(page)).toContainText('Manual offers v1.3.2 and waits for a person to take it.');
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
    'Zoomies offers the newest release that can be installed on this system, with an Update button for this controller and for each host whose update helper is installed, and one on the Hosts page that updates every host behind this controller, one at a time.',
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

// The Update button is the platform role's, and it is the only control that acts.
// What the page must still never offer is a way to install, apply or restart by
// any other route, or to choose a release it was not shown.
test("the buttons are Refresh and the platform role's Update, and nothing installs or applies", async ({
  page,
}) => {
  await goto(page, PAGE, 'Updates');
  await expect(take(page)).toBeVisible();

  const main = page.getByRole('main');
  await expect(main.getByRole('button', { name: 'Refresh', exact: true })).toBeVisible();
  await expect(main.getByRole('button', { name: 'Update to v1.3.2', exact: true })).toBeVisible();
  await expect(main.getByRole('button', { name: /install|apply|upgrade|restart/i })).toHaveCount(0);
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
  // The page reads the status again when the event stream first reaches live. Let
  // that read take its failing answer before the route heals, or it lands after
  // the click and replaces the page it shows.
  await expect(page.locator('.connection')).toHaveAttribute('data-state', 'live');
  failing = false;
  await alert.getByRole('button', { name: 'Try again' }).click();
  await expect(take(page)).toContainText('Manual offers v1.3.2');
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
  await expect(take(page)).toContainText('Manual offers v1.3.2');

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

/* -- updating the controller ----------------------------------------------- */

const update = (page: Page) =>
  page.getByRole('region', { name: 'Update this controller', exact: true });
const updateButton = (page: Page) =>
  update(page).getByRole('button', { name: 'Update to v1.3.2', exact: true });
const confirmation = (page: Page) =>
  page.getByRole('dialog', { name: 'Update the controller', exact: true });
/** The attempt's own block, named by its title: "Updating to v1.3.2", "Updated to …". */
const attemptBlock = (page: Page, title: RegExp | string) =>
  update(page).getByRole('region', { name: title });

/**
 * The folder the controller was told to update through. The fixture makes it at
 * a path that carries the port, so the spec finds it from the address it is
 * driving and nothing is passed between the two.
 */
function updateFolder(baseURL: string | undefined): string {
  return join(tmpdir(), `zoomies-e2e-update-folder-${new URL(baseURL ?? '').port}`);
}

/**
 * Play the helper: answer the open attempt. Written under another name and
 * renamed, as the helper does, so the controller never reads half of it.
 */
function helperSays(
  baseURL: string | undefined,
  id: string,
  result: { ok: boolean; error: string },
): void {
  const folder = updateFolder(baseURL);
  const now = new Date().toISOString();
  const tmp = join(folder, '.result.tmp');
  writeFileSync(
    tmp,
    JSON.stringify({
      v: 1,
      id,
      ok: result.ok,
      tag: 'v1.3.2',
      from: '1.3.0',
      to: '',
      error: result.error,
      log_tail: '',
      started_at: now,
      finished_at: now,
    }),
  );
  renameSync(tmp, join(folder, 'result.json'));
}

/** The controller's own attempt, as its API says it is. */
async function latestAttempt(page: Page): Promise<NonNullable<UpdatesStatus['controller']> | null> {
  const status = (await (await page.request.get('/api/v1/updates')).json()) as UpdatesStatus;
  return status.controller;
}

// The seeded controller is shared by every test here, and an attempt left open
// would refuse the next press, so a test that pressed the button ends by playing
// a helper that failed. The controller notices within its ten second pass.
test.afterEach(async ({ page, baseURL }) => {
  const attempt = await latestAttempt(page);
  if (attempt?.state !== 'requested') return;
  helperSays(baseURL, attempt.id, { ok: false, error: 'Closed by the test that opened it.' });
  await expect
    .poll(async () => (await latestAttempt(page))?.state, { timeout: 25_000 })
    .not.toBe('requested');
});

/** The status of a controller that can be updated: manual, a ready helper and v1.3.2 on offer. */
function updatable(controller: UpdatesStatus['controller'] = null): UpdatesStatus {
  return {
    ...waiting(),
    mode: 'manual',
    target: { tag: 'v1.3.2', newer: true, due_at: null },
    reason:
      'Available: v1.3.2 is newer than the v1.3.0 running now; manual mode waits for someone to update.',
    helper: {
      state: 'ready',
      reason:
        "The update helper is installed on this controller's host, so the controller can update itself.",
      install_command: '',
      upgrade_command: '',
    },
    controller,
  };
}

/** An attempt from 1.3.0 to v1.3.2, as the controller sends one. */
function attemptIn(
  state: 'requested' | 'succeeded' | 'failed' | 'timed_out' | 'cancelled',
  error = '',
): NonNullable<UpdatesStatus['controller']> {
  return {
    id: 'upd_e2eattempt01',
    state,
    from: '1.3.0',
    to: 'v1.3.2',
    trigger: 'manual',
    requested_at: new Date(Date.now() - 4 * MINUTE).toISOString(),
    finished_at: state === 'requested' ? null : new Date(Date.now() - MINUTE).toISOString(),
    error,
  };
}

/** Sign in as a role below the platform one: the server here has authentication off, the page is told otherwise. */
async function signedInAs(page: Page, role: 'viewer' | 'operator' | 'admin'): Promise<void> {
  await page.route('**/api/v1/meta', async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as Record<string, unknown>;
    return route.fulfill({
      response,
      json: { ...body, auth_disabled: false, bootstrap_required: false },
    });
  });
  await page.route('**/api/v1/auth/session', (route) =>
    route.fulfill({ json: { kind: 'token', id: 'tok_pretend', name: 'a token', role } }),
  );
}

/** Count what the page sends to start an update. */
function countStarts(page: Page): () => number {
  let posted = 0;
  page.on('request', (request) => {
    if (
      request.method() === 'POST' &&
      new URL(request.url()).pathname === '/api/v1/updates/controller'
    ) {
      posted += 1;
    }
  });
  return () => posted;
}

test('the platform role is offered the button, and the confirmation says what the update does', async ({
  page,
}) => {
  const starts = countStarts(page);
  await goto(page, PAGE, 'Updates');
  await expect(updateButton(page)).toBeVisible();

  await updateButton(page).click();
  const dialog = confirmation(page);
  await expect(dialog).toBeVisible();
  // It names the release it is about to install and the build it replaces.
  await expect(dialog).toContainText('Update this controller from 1.3.0 to v1.3.2?');
  await expect(dialog).toContainText('The controller restarts.');
  await expect(dialog).toContainText('Jobs that are running keep running.');
  await expect(dialog).toContainText(
    'If the release changes the database, a copy of it is kept first beside the database',
  );
  await expect(dialog).toContainText('A release that changes nothing there takes none.');
  await expect(dialog.getByRole('button', { name: 'Update to v1.3.2', exact: true })).toBeVisible();

  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
  expect(starts(), 'cancelling asks for nothing').toBe(0);
  await expect(updateButton(page)).toBeFocused();
});

test('the confirmation is reached and left by keyboard, and focus returns to the button', async ({
  page,
}) => {
  const starts = countStarts(page);
  await goto(page, PAGE, 'Updates');
  await updateButton(page).focus();
  await page.keyboard.press('Enter');

  const dialog = confirmation(page);
  await expect(dialog).toBeVisible();
  const inside = () => dialog.evaluate((el) => el.contains(document.activeElement));
  expect(await inside(), 'focus moves into the dialog').toBe(true);
  for (let i = 0; i < 4; i += 1) {
    await page.keyboard.press('Tab');
    expect(await inside(), `focus stays inside after ${i + 1} Tab presses`).toBe(true);
  }

  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(updateButton(page)).toBeFocused();
  expect(starts()).toBe(0);
});

test('an administrator sees no Update button, and is told the platform role is needed', async ({
  page,
}) => {
  const starts = countStarts(page);
  await signedInAs(page, 'admin');
  await goto(page, PAGE, 'Updates');
  await expect(take(page)).toContainText('Available');

  await expect(update(page)).toContainText('Updating the controller needs the platform role.');
  await expect(page.getByRole('button', { name: /^Update to/ })).toHaveCount(0);
  await expect(update(page).getByRole('button')).toHaveCount(0);
  expect(starts()).toBe(0);
});

test("an administrator is told how a failed update ended in the controller's own words for them", async ({
  page,
}) => {
  const sentence = 'The update did not succeed. Whoever holds the platform role can read why.';
  await signedInAs(page, 'admin');
  await serve(page, () => updatable(attemptIn('failed', sentence)));
  await goto(page, PAGE, 'Updates');

  const block = attemptBlock(page, 'The update to v1.3.2 did not succeed');
  await expect(block).toContainText(sentence);
  await expect(block).toContainText('This controller still runs 1.3.0.');
  await expect(update(page).getByRole('button')).toHaveCount(0);
});

test('pressing Update asks the helper, shows the update in flight and says nothing final until the controller does', async ({
  page,
  baseURL,
}) => {
  test.setTimeout(90_000);
  await goto(page, PAGE, 'Updates');
  const connection = page.locator('.connection');
  await expect(connection).toHaveAttribute('data-state', 'live');

  await updateButton(page).click();
  const posted = page.waitForRequest(
    (request) =>
      request.method() === 'POST' &&
      new URL(request.url()).pathname === '/api/v1/updates/controller',
  );
  await confirmation(page).getByRole('button', { name: 'Update to v1.3.2', exact: true }).click();
  const request = await posted;
  expect(request.postDataJSON(), 'it asks for the release the dialog named').toEqual({
    tag: 'v1.3.2',
  });

  // In flight: the button is replaced by the state, and focus has moved to it.
  const flight = attemptBlock(page, 'Updating to v1.3.2');
  await expect(flight).toBeVisible();
  await expect(flight).toContainText('In progress');
  await expect(flight).toContainText('from 1.3.0');
  await expect(update(page).getByRole('button')).toHaveCount(0);
  await expect(confirmation(page)).toBeHidden();
  await expect(flight).toBeFocused();
  await expect(page.getByRole('status').filter({ hasText: 'Updating to v1.3.2' })).toHaveCount(1);

  // What the controller asked the helper to do is in the folder it was given.
  const attempt = await latestAttempt(page);
  expect(attempt?.state).toBe('requested');
  const written = JSON.parse(readFileSync(join(updateFolder(baseURL), 'request.json'), 'utf8'));
  expect(written).toMatchObject({ v: 1, id: attempt?.id, tag: 'v1.3.2' });

  // The state is the controller's, not the page's: a reload shows the same.
  await reload(page, 'Updates');
  await expect(attemptBlock(page, 'Updating to v1.3.2')).toBeVisible();
  await expect(update(page).getByRole('button')).toHaveCount(0);
  await expect(update(page)).not.toContainText('Updated to');

  // The helper refuses before it starts. The stream never dropped, so this
  // arrives as a frame and the page does not reload to show it.
  await expect(connection).toHaveAttribute('data-state', 'live');
  await plantMarker(page);
  helperSays(baseURL, attempt?.id ?? '', {
    ok: false,
    error:
      'The release could not be verified: the checksum of zoomies_linux_amd64 does not match checksums.txt.',
  });
  const failed = attemptBlock(page, 'The update to v1.3.2 did not succeed');
  await expect(failed).toBeVisible({ timeout: 25_000 });
  await expect(failed).toContainText(
    'The release could not be verified: the checksum of zoomies_linux_amd64 does not match checksums.txt.',
  );
  await expect(failed).toContainText('This controller still runs 1.3.0.');
  await expect(failed).toContainText('zoomies updates helper status');
  await expectNoReload(page);
  await expect(connection).toHaveAttribute('data-state', 'live');
  // It can be tried again, and the request the helper never took is taken back.
  await expect(updateButton(page)).toBeVisible();
  await expect.poll(() => existsSync(join(updateFolder(baseURL), 'request.json'))).toBe(false);
});

test('a status that says an update is open shows it in flight and never as done', async ({
  page,
}) => {
  await serve(page, () => updatable(attemptIn('requested')));
  await goto(page, PAGE, 'Updates');

  const flight = attemptBlock(page, 'Updating to v1.3.2');
  await expect(flight).toBeVisible();
  await expect(flight).toContainText('says how it ended when the controller does, and not before');
  await expect(update(page).getByRole('button')).toHaveCount(0);
  // However long the page waits, an old build running is not a finished update.
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await page.waitForTimeout(1_000);
  await expect(update(page)).not.toContainText(/Updated to|succeeded/i);
});

// The new process records the ending a moment after it starts. In that moment
// the status already carries the answer in the build it reports.
test('the build changing to the release asked for is shown as the update done', async ({
  page,
}) => {
  let running = '1.3.0';
  await serve(page, () => ({
    ...updatable(attemptIn('requested')),
    running: { version: running, release: true },
  }));
  await goto(page, PAGE, 'Updates');
  await expect(attemptBlock(page, 'Updating to v1.3.2')).toBeVisible();

  running = '1.3.2';
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  const done = attemptBlock(page, 'Updated to v1.3.2');
  await expect(done).toBeVisible();
  await expect(done).toContainText('This controller was on 1.3.0 and runs 1.3.2 now.');
});

test('an attempt the controller closed as succeeded is shown as done', async ({ page }) => {
  await serve(page, () => ({
    ...updatable(attemptIn('succeeded')),
    running: { version: '1.3.2', release: true },
    target: { tag: 'v1.3.2', newer: false, due_at: null },
  }));
  await goto(page, PAGE, 'Updates');

  const done = attemptBlock(page, 'Updated to v1.3.2');
  await expect(done).toContainText('Updated');
  await expect(done).toContainText('runs 1.3.2 now');
  await expect(update(page).getByRole('button')).toHaveCount(0);
  await expect(update(page)).toContainText('No newer release is on offer');
});

test('an update that timed out says so and where to look', async ({ page }) => {
  await serve(page, () =>
    updatable(
      attemptIn(
        'timed_out',
        "No answer came from the update helper within 90 minutes, and this controller still runs 1.3.0. Look at journalctl -u zoomies-update and zoomies updates helper status on the controller's host.",
      ),
    ),
  );
  await goto(page, PAGE, 'Updates');

  const block = attemptBlock(page, 'The update to v1.3.2 timed out');
  await expect(block).toContainText('No answer came from the update helper within 90 minutes');
  await expect(block).toContainText('zoomies updates helper status');
  await expect(block).toContainText('journalctl -u zoomies-update');
  await expect(updateButton(page)).toBeVisible();
});

// The sentence is whatever the helper wrote, and the helper's output is text
// that came off a download.
test("the helper's sentence is shown as text and never as markup", async ({ page }) => {
  const hostile =
    '<img src=x onerror="window.__pwned = true"><b>bold</b> **not bold** [link](https://example.invalid)';
  await serve(page, () => updatable(attemptIn('failed', hostile)));
  await goto(page, PAGE, 'Updates');

  const block = attemptBlock(page, 'The update to v1.3.2 did not succeed');
  await expect(block).toContainText(hostile);
  await expect(block.locator('img, b, a')).toHaveCount(0);
  expect(
    await page.evaluate(() => (window as unknown as { __pwned?: boolean }).__pwned),
  ).toBeUndefined();
});

test('with no helper there is no button, and the page says how to install one', async ({
  page,
}) => {
  await serve(page, () => ({ ...updatable(), helper: waiting().helper }));
  await goto(page, PAGE, 'Updates');

  await expect(update(page)).toContainText(
    "No update helper is installed on this controller's host.",
  );
  await expect(update(page).getByText('sudo zoomies updates helper install')).toBeVisible();
  await expect(
    update(page).getByRole('button', { name: 'Copy the install command' }),
  ).toBeVisible();
  await expect(page.getByRole('button', { name: /^Update to/ })).toHaveCount(0);
});

test('where the helper can never be installed the page says why, with the upgrade command and nothing to install', async ({
  page,
}) => {
  const reason =
    'The update helper cannot be installed here: this controller runs in a container that runs no runners, so the container does not mount the shared folder the update helper would read a request from. Update this controller on its host with the command below.';
  await serve(page, () => ({
    ...updatable(),
    helper: {
      state: 'unsupported',
      reason,
      install_command: '',
      upgrade_command: 'sudo zoomies upgrade',
    },
  }));
  await goto(page, PAGE, 'Updates');

  await expect(update(page)).toContainText(reason);
  await expect(update(page).getByText('sudo zoomies upgrade', { exact: true })).toBeVisible();
  await expect(
    update(page).getByRole('button', { name: 'Copy the upgrade command' }),
  ).toBeVisible();
  await expect(update(page).getByText('sudo zoomies updates helper install')).toHaveCount(0);
  await expect(update(page).getByRole('button', { name: 'Copy the install command' })).toHaveCount(
    0,
  );
  await expect(page.getByRole('button', { name: /^Update to/ })).toHaveCount(0);
});

test("a refusal to start is shown where the button was, in the controller's words", async ({
  page,
}) => {
  await serve(page, () => updatable());
  await page.route('**/api/v1/updates/controller', (route) =>
    route.fulfill({
      status: 409,
      json: {
        error: {
          code: 'update.in_progress',
          message: 'an update of this controller is already in progress',
        },
      },
    }),
  );
  await goto(page, PAGE, 'Updates');
  await updateButton(page).click();
  await confirmation(page).getByRole('button', { name: 'Update to v1.3.2', exact: true }).click();

  await expect(confirmation(page)).toBeHidden();
  await expect(page.getByRole('alert')).toContainText(
    'An update of this controller is already in progress.',
  );
  await expect(updateButton(page)).toBeVisible();
  await expect(updateButton(page)).toBeFocused();
});

// The restart is the stream going and coming back. The page asks for a restart
// that is quicker than any probe could catch by listening to the connection
// itself, and when it hears the controller again it reads how the update ended.
test('an update in flight survives the stream dropping and comes back to the same state', async ({
  page,
}) => {
  await serve(page, () => updatable(attemptIn('requested')));
  let cut = false;
  await page.route('**/api/v1/events*', (route) =>
    cut ? route.abort('connectionfailed') : route.fallback(),
  );
  const connection = page.locator('.connection');
  await goto(page, PAGE, 'Updates');
  await expect(connection).toHaveAttribute('data-state', 'live');
  await expect(attemptBlock(page, 'Updating to v1.3.2')).toBeVisible();
  await plantMarker(page);

  cut = true;
  await page.evaluate(() => window.dispatchEvent(new Event('offline')));
  await expect(
    update(page).getByRole('heading', { name: 'Waiting for the controller to answer' }),
  ).toBeVisible();
  await expect(update(page)).toContainText('This page has lost its connection to the controller');
  await expect(update(page)).toContainText('Updating from 1.3.0 to v1.3.2');
  await expect(update(page)).not.toContainText(/Updated to|succeeded/i);

  // The controller answers again. The page reloads onto what the controller
  // says, which is still an update in flight: nothing was claimed in between.
  cut = false;
  const reloaded = page.waitForEvent('load', { timeout: 20_000 });
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await reloaded;
  await expect(pageHeading(page, 'Updates')).toBeVisible();
  await expect(attemptBlock(page, 'Updating to v1.3.2')).toBeVisible();
  await expect(update(page).getByRole('button')).toHaveCount(0);
  expect(
    await page.evaluate(
      () => (window as unknown as { __zoomiesStayedPut?: boolean }).__zoomiesStayedPut,
    ),
    'the page started over',
  ).toBeUndefined();
});

test('the page does not mistake loading for a restart', async ({ page }) => {
  await serve(page, () => updatable(attemptIn('requested')));
  await goto(page, PAGE, 'Updates');
  await expect(attemptBlock(page, 'Updating to v1.3.2')).toBeVisible();
  await expect(page.locator('.connection')).toHaveAttribute('data-state', 'live');
  await expect(
    update(page).getByRole('heading', { name: /Waiting for the controller/ }),
  ).toHaveCount(0);
});

/** What the accessibility pass holds the Updates page to, in whatever state it is. */
async function expectAccessibleStructure(page: Page): Promise<void> {
  await expect(page.getByRole('heading', { level: 1 })).toHaveCount(1);
  await expect(page.getByRole('main')).toHaveCount(1);
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
  expect(
    levels.filter((level, i) => i > 0 && level > (levels[i - 1] ?? 0) + 1),
    `heading levels in order: ${levels.join(', ')}`,
  ).toEqual([]);
  // The state is announced from a region that is always there.
  await expect(update(page).getByRole('status')).toHaveCount(1);
}

test('the update controls and states have names, a heading order and a live region', async ({
  page,
}) => {
  await goto(page, PAGE, 'Updates');
  await expect(updateButton(page)).toBeVisible();
  await expectAccessibleStructure(page);

  await updateButton(page).click();
  await expect(confirmation(page)).toBeVisible();
  await expect(confirmation(page)).toHaveAttribute('aria-modal', 'true');
  await expect(confirmation(page).getByRole('button', { name: 'Cancel' })).toBeVisible();
  await page.keyboard.press('Escape');

  for (const attempt of [
    attemptIn('requested'),
    attemptIn('failed', 'The checksum did not match.'),
    attemptIn('timed_out', 'No answer came.'),
    attemptIn('cancelled'),
  ]) {
    await page.unroute('**/api/v1/updates');
    await serve(page, () => updatable(attempt));
    await reload(page, 'Updates');
    await expect(attemptBlock(page, /^(Updating|The update)/)).toBeVisible();
    await expectAccessibleStructure(page);
  }
});

test('nothing scrolls sideways at 360px while an update is open, failed or being asked for', async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 780 });
  await serve(page, () => updatable(attemptIn('requested')));
  await goto(page, PAGE, 'Updates');
  await expect(attemptBlock(page, 'Updating to v1.3.2')).toBeVisible();
  await expectNoSidewaysScroll(page, 'the Updates page with an update in flight');

  await page.unroute('**/api/v1/updates');
  await serve(page, () =>
    updatable(
      attemptIn(
        'failed',
        'The release could not be verified: the checksum of zoomies_linux_amd64_with_a_very_long_name_that_has_no_break does not match.',
      ),
    ),
  );
  await reload(page, 'Updates');
  const block = attemptBlock(page, 'The update to v1.3.2 did not succeed');
  await expect(block).toBeVisible();
  const box = await block.boundingBox();
  expect(
    box && box.x >= 16 && box.x + box.width <= 360 - 16 + 1,
    'the block keeps its gutters',
  ).toBe(true);
  await expectNoSidewaysScroll(page, 'the Updates page after a failed update');

  await updateButton(page).click();
  const dialog = confirmation(page);
  await expect(dialog).toBeVisible();
  const dialogBox = await dialog.boundingBox();
  expect(
    dialogBox && dialogBox.x >= 0 && dialogBox.x + dialogBox.width <= 360,
    'the dialog fits',
  ).toBe(true);
  await expectNoSidewaysScroll(page, 'the Updates page with the confirmation open');
});

/* -- the hosts' rollout ------------------------------------------------------ */

const hostsRollout = (page: Page) =>
  page.getByRole('region', { name: 'Updating hosts', exact: true });

/** The planner's sentence for a rollout auto has started, as the controller words it. */
const AUTO_STARTED =
  'Starting a rollout to v1.3.0, the release the controller runs: 2 hosts are behind it and are updated one at a time.';

/**
 * No stream: a frame from the real controller, which has no such rollout, would
 * put its own status over the one served.
 */
async function quiet(page: Page): Promise<void> {
  await page.route('**/api/v1/events*', (route) =>
    route.fulfill({
      status: 200,
      headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
      body: '',
    }),
  );
}

function rolledOut(state: 'running' | 'halted' | 'done' | 'cancelled'): UpdatesStatus {
  return {
    ...waiting(),
    reason: AUTO_STARTED,
    rollout: {
      id: 'rol_e2erollout01',
      target: 'v1.3.0',
      state,
      halted_reason:
        state === 'halted'
          ? "The update of build-02 to v1.3.0 did not succeed, so the rollout is halted. Read why on the host's card, then resume the rollout or cancel it."
          : '',
      done: state === 'done' ? 2 : 1,
      total: 2,
      current: state === 'running' ? 'build-02' : '',
    },
  };
}

test('in auto the planner’s sentence is shown as it was given, and the mode says what auto does by itself', async ({
  page,
}) => {
  await serve(page, () => rolledOut('running'));
  await quiet(page);
  await goto(page, PAGE, 'Updates');

  await expect(take(page).getByText(AUTO_STARTED, { exact: true })).toBeVisible();
  await expect(mode(page)).toContainText('Auto');
  await expect(mode(page)).toContainText(
    'it updates this controller through its update helper, then every host behind the controller, one at a time.',
  );
  await expect(mode(page)).toContainText(
    'A host whose update fails halts the rollout until an administrator resumes or cancels it.',
  );
  await expect(mode(page)).not.toContainText('takes no release by itself yet');
});

test('the rollout is shown with its progress, a halted one in the draining colour, and an administrator is sent to Hosts to act on it', async ({
  page,
}) => {
  let state: Parameters<typeof rolledOut>[0] = 'running';
  await serve(page, () => rolledOut(state));
  await quiet(page);
  await goto(page, PAGE, 'Updates');
  await expect(hostsRollout(page)).toContainText('Rolling out');
  await expect(hostsRollout(page)).toContainText('1 of 2 hosts updated.');
  await expect(hostsRollout(page)).toContainText('build-02 is being updated now.');

  state = 'halted';
  await reload(page, 'Updates');
  await expect(hostsRollout(page)).toContainText('The rollout to v1.3.0 is halted');
  await expect(hostsRollout(page)).toContainText(
    'The update of build-02 to v1.3.0 did not succeed',
  );
  await expect(hostsRollout(page).locator('[data-tone="draining"]')).toHaveText('Halted');
  const link = hostsRollout(page).getByRole('link', {
    name: 'Resume or cancel it on the Hosts page',
  });
  await expect(link).toHaveAttribute('href', '/hosts');
  // Nothing here acts on it: that is the Hosts page's, beside the hosts it moves.
  await expect(hostsRollout(page).getByRole('button')).toHaveCount(0);

  state = 'done';
  await reload(page, 'Updates');
  await expect(hostsRollout(page)).toContainText('The rollout to v1.3.0 is done');
  await expect(hostsRollout(page).locator('[data-tone="idle"]')).toHaveText('Done');
  await expect(hostsRollout(page).getByRole('link')).toHaveCount(0);

  // An operator reads it and is sent nowhere.
  state = 'halted';
  await signedInAs(page, 'operator');
  await reload(page, 'Updates');
  await expect(hostsRollout(page)).toContainText('Halted');
  // Counted in the document and not the accessibility tree, which can leave a
  // link out for reasons of its own and so pass for the wrong one.
  await expect(hostsRollout(page).locator('a')).toHaveCount(0);
  await expect(hostsRollout(page)).not.toContainText('Hosts page');
  await expectAccessibleStructure(page);
});

test('nothing scrolls sideways at 360px with a halted rollout', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 780 });
  await serve(page, () => rolledOut('halted'));
  await quiet(page);
  await goto(page, PAGE, 'Updates');
  await expect(hostsRollout(page)).toContainText('Halted');
  await expectNoSidewaysScroll(page, 'the Updates page with a halted rollout');
});
