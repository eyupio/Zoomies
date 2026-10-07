/**
 * Kennel Club, end to end, against the real binary.
 *
 * It is off by default, so most people meet it off, and the first block is the
 * page they meet: what it does, what it reads and what it never does, with the
 * AI Context page it replaced in the navigation still one press away at its old
 * address and its new one. The second block turns it on -- the demo fleet
 * answers Kennel Club from its fixture, and names one repository public -- and
 * walks every state that has something to show. Every test that turns it on
 * puts it back: the suite shares one controller, and a problem Kennel Club
 * raises would move the counts the other specs assert on.
 *
 * Both the desktop and the phone project run this file, one after the other, so
 * nothing here may depend on a controller that has not been used before.
 */
import { expect, test, type Page } from '@playwright/test';
import {
  browserOverride,
  dataRows,
  documentWidth,
  expectCurrentSection,
  FIXTURE,
  goto,
  grid,
  expectNoReload,
  menuEntry,
  navEntry,
  openNavMenu,
  plantMarker,
} from './support/fixtures';

test.use(browserOverride);
test.describe.configure({ mode: 'serial' });

const PUBLIC_REPO = 'acme/site';

async function patchSettings(page: Page, settings: Record<string, unknown>): Promise<void> {
  const res = await page.request.patch('/api/v1/settings', { data: settings });
  expect(res.ok(), `settings ${JSON.stringify(settings)} were saved`).toBeTruthy();
}

interface Overview {
  enabled: boolean;
  repositories: number;
  states: { attention: number; best_in_show: number };
  counts: { error: number };
}

async function overview(page: Page): Promise<Overview> {
  return (await page.request.get('/api/v1/kennel').then((r) => r.json())) as Overview;
}

/** The demo's repositories are read within moments of the setting being turned on. */
async function untilRead(page: Page): Promise<void> {
  await expect
    .poll(async () => (await overview(page)).repositories, {
      message: 'Kennel Club reads the fixture fleet',
      timeout: 30_000,
    })
    .toBe(FIXTURE.repos.length);
}

interface Row {
  id: string;
  name: string;
  findings: Array<{ code: string; evidence: Array<{ kind: string; ref: string; label?: string }> }>;
}

/**
 * A repository with nothing open on it, whichever it is.
 *
 * Which ones those are changes while a controller runs: the demo fleet seeds one
 * job that nothing claims, and ten minutes after it was queued a repository has a
 * capacity warning that it did not have when the controller started. A suite
 * that shares one controller for half an hour cannot name the quiet repository
 * in advance, so it asks.
 */
async function quietRepository(page: Page): Promise<Row> {
  const listed = (await page.request
    .get('/api/v1/kennel/repositories?state=best_in_show')
    .then((r) => r.json())) as { items: Row[] };
  expect(listed.items.length, 'at least one repository has nothing open').toBeGreaterThan(0);
  return listed.items[0] as Row;
}

async function repository(page: Page, name: string): Promise<Row> {
  const listed = (await page.request
    .get(`/api/v1/kennel/repositories?q=${encodeURIComponent(name)}`)
    .then((r) => r.json())) as { items: Row[] };
  const row = listed.items.find((r) => r.name === name);
  expect(row, `Kennel Club has a row for ${name}`).toBeTruthy();
  return row as Row;
}

/**
 * Every control on the page has a name a screen reader can say, and the page
 * does not scroll sideways. The suite's accessibility and phone passes visit a
 * page in the state it is normally in, which for this one is off.
 */
async function auditThePage(page: Page, where: string): Promise<void> {
  const { scrollWidth, clientWidth } = await documentWidth(page);
  expect(scrollWidth, `${where} scrolls sideways`).toBeLessThanOrEqual(clientWidth);
  await expect(page.getByRole('button', { name: /^$/ }), `${where}: buttons are named`).toHaveCount(
    0,
  );
  await expect(page.getByRole('link', { name: /^$/ }), `${where}: links are named`).toHaveCount(0);
}

/* -- off ----------------------------------------------------------------------- */

test.describe('while Kennel Club is off', () => {
  test.beforeAll(async ({ request }) => {
    const res = await request.patch('/api/v1/settings', { data: { 'kennel.enabled': false } });
    expect(res.ok()).toBeTruthy();
  });

  test('it is in the navigation and AI Context is not', async ({ page, isMobile }) => {
    await goto(page, '/', 'Overview');
    if (isMobile) {
      await openNavMenu(page);
      await expect(menuEntry(page, '/kennel')).toBeVisible();
      await expect(menuEntry(page, '/ai-context')).toHaveCount(0);
      return;
    }
    await expect(navEntry(page, '/kennel')).toBeVisible();
    await expect(navEntry(page, '/ai-context')).toHaveCount(0);
  });

  test('the g k chord opens it', async ({ page, isMobile }) => {
    test.skip(isMobile, 'a phone has no keyboard chord');
    await goto(page, '/', 'Overview');
    await page.keyboard.press('g');
    await page.keyboard.press('k');
    await expect(page).toHaveURL(/\/kennel$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Kennel Club' })).toBeVisible();
  });

  test('it explains itself, and what it checks is the registry the evaluator runs', async ({
    page,
  }) => {
    await goto(page, '/kennel', 'Kennel Club');

    await expect(page.getByRole('heading', { name: 'Kennel Club is off' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'What it reads' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'What it never does' })).toBeVisible();
    // The platform account an auth-less controller signs everybody in as may turn
    // it on, and is told where.
    await expect(page.getByRole('link', { name: 'Turn on in Settings' })).toHaveAttribute(
      'href',
      '/settings/configuration?setting=kennel.enabled',
    );

    const checks = page.getByRole('table', { name: 'What Kennel Club checks' });
    // The body's rows: on a phone the header row is hidden and each row is a card.
    await expect(checks.locator('tbody tr')).toHaveCount(6);
    for (const code of [
      'exposure.public_repo_on_fleet',
      'exposure.public_repo_weak_pool',
      'exposure.fork_code_ran',
      'exposure.target_event_ran',
      'capacity.unserved_label',
      'capacity.job_hit_default_limit',
    ]) {
      await expect(checks.getByText(code, { exact: true }), code).toBeVisible();
    }
    await auditThePage(page, 'the off page');
  });

  test('the list says why there is nothing in it', async ({ page }) => {
    await goto(page, '/kennel/repositories', 'Repositories');
    await expect(page.getByText('Kennel Club is off', { exact: true })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Turn on in Settings' })).toBeVisible();
    await auditThePage(page, 'the off list');
  });

  test('AI Context is one press away, and its old addresses open the same pages', async ({
    page,
  }) => {
    await goto(page, '/kennel', 'Kennel Club');
    await page.getByRole('link', { name: 'AI Context', exact: true }).click();
    await expect(page).toHaveURL(/\/kennel\/ai-context$/);
    await expect(page.getByRole('heading', { level: 1, name: 'AI Context' })).toBeVisible();
    await expectCurrentSection(page, '/kennel');

    // The old address renders the page without redirecting, so a bookmark, a
    // link in the docs and a problem raised before the move all still work, and
    // the sidebar still says which section it belongs to.
    await goto(page, '/ai-context', 'AI Context');
    await expect(page).toHaveURL(/\/ai-context$/);
    await expectCurrentSection(page, '/kennel');
  });
});

/* -- on ------------------------------------------------------------------------ */

test.describe('with Kennel Club on', () => {
  test.beforeAll(async ({ request }) => {
    const res = await request.patch('/api/v1/settings', {
      data: { 'kennel.enabled': true, 'kennel.disabled_checks': [] },
    });
    expect(res.ok()).toBeTruthy();
  });

  test.afterAll(async ({ request }) => {
    const res = await request.patch('/api/v1/settings', {
      data: { 'kennel.enabled': false, 'kennel.disabled_checks': [] },
    });
    expect(res.ok(), 'Kennel Club was turned off again').toBeTruthy();
  });

  test('the Overview counts what was read, and says which repository to open first', async ({
    page,
  }) => {
    await untilRead(page);
    await goto(page, '/kennel', 'Kennel Club');

    const tile = (label: string) =>
      page
        .locator('dl.metrics > div')
        .filter({ has: page.getByRole('term').filter({ hasText: new RegExp(`^${label}$`) }) });
    await expect(tile('Repositories')).toContainText('3');
    // The number of repositories that need attention grows with the controller's
    // age (see quietRepository), so the page is held to what the API says now, and
    // to there being at least the one with the errors.
    const now = await overview(page);
    expect(now.states.attention).toBeGreaterThanOrEqual(1);
    await expect(tile('Need attention')).toContainText(String(now.states.attention));
    await expect(tile('Errors')).toContainText(String(now.counts.error));
    expect(now.counts.error, 'only the public repository has errors').toBe(2);

    const attention = page.getByRole('region', { name: 'Needs attention' });
    await expect(attention.getByRole('link', { name: new RegExp(PUBLIC_REPO) })).toBeVisible();
    await expect(attention).toContainText('2 errors');

    // By check: which repositories have each check open.
    const byCheck = page.getByRole('table', {
      name: 'Checks and the repositories that have each open',
    });
    const weak = byCheck.getByRole('row').filter({ hasText: 'exposure.public_repo_weak_pool' });
    await expect(weak.getByRole('link')).toContainText('1 repository has');
    await weak.getByRole('link').click();
    await expect(page).toHaveURL(/\/kennel\/repositories\?code=exposure\.public_repo_weak_pool/);

    await auditThePage(page, 'the Overview');
  });

  test('the Overview says what Kennel Club could see', async ({ page }) => {
    await goto(page, '/kennel', 'Kennel Club');
    const seen = page.getByRole('region', { name: 'What Kennel Club can see' });
    await expect(seen).toContainText('What this fleet observed');
    await expect(seen).toContainText('Repository details');
    await expect(seen).toContainText('Workflow runs');
    await expect(seen.getByText('Read', { exact: true }).first()).toBeVisible();
  });

  test('counts that are a minimum say so, and an installation that cannot be read is named', async ({
    page,
  }) => {
    // What a held read looks like: one repository only partly read, and an
    // installation whose reads are not getting through. The numbers above must
    // not read as totals, and the page says why.
    await page.route('**/api/v1/kennel', async (route) => {
      const real = await route.fetch();
      const body = (await real.json()) as {
        states: Record<string, number>;
        unavailable: unknown[];
      };
      body.states = { ...body.states, partial: 1 };
      body.unavailable = [
        {
          installation_id: FIXTURE.installationId,
          target: 'acme',
          state: 'held',
          reason: 'GitHub asked Zoomies to wait, so nothing was read.',
          since: new Date(Date.now() - 3600_000).toISOString(),
        },
      ];
      return route.fulfill({ response: real, json: body });
    });
    await goto(page, '/kennel', 'Kennel Club');

    await expect(
      page.getByText(
        'These counts are a minimum: 1 repository is only partly checked and reads from acme are not getting through',
      ),
    ).toBeVisible();
    const attention = page
      .locator('dl.metrics > div')
      .filter({ has: page.getByRole('term').filter({ hasText: /^Need attention$/ }) });
    await expect(attention).toContainText('At least');
    const note = page.getByRole('note').filter({ hasText: 'GitHub asked Zoomies to wait' });
    await expect(note).toContainText('acme');
    await expect(note).toContainText('Held for a rate limit');
  });

  test('a refresh that fails keeps what the page had, says so, and recovers', async ({ page }) => {
    await goto(page, '/kennel', 'Kennel Club');
    const repositories = page
      .locator('dl.metrics > div')
      .filter({ has: page.getByRole('term').filter({ hasText: /^Repositories$/ }) });
    await expect(repositories).toContainText('3');

    let down = true;
    await page.route('**/api/v1/kennel', (route) =>
      down ? route.abort('connectionfailed') : route.fallback(),
    );
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await expect(page.getByText('The last refresh did not get through')).toBeVisible();
    // Last-known data stays: a failed refresh is not a reason to blank the page.
    await expect(repositories).toContainText('3');

    down = false;
    await page.getByRole('button', { name: 'Try again' }).click();
    await expect(page.getByText('The last refresh did not get through')).toHaveCount(0);
  });

  test('a repository shows each finding, what to change and where it was seen', async ({
    page,
  }) => {
    await goto(page, '/kennel', 'Kennel Club');
    await page
      .getByRole('region', { name: 'Needs attention' })
      .getByRole('link', { name: new RegExp(PUBLIC_REPO) })
      .click();
    await expect(page).toHaveURL(/\/kennel\/repositories\/kcr_/);
    await expect(page.getByRole('heading', { level: 1, name: PUBLIC_REPO })).toBeVisible();

    const findings = page.getByRole('article');
    await expect(findings).toHaveCount(2);
    await expect(findings.first()).toContainText('What to change');
    const weak = page.getByRole('article', { name: /weak isolation/ });
    await expect(weak).toContainText(FIXTURE.armPool);
    await expect(weak).toContainText('exposure.public_repo_weak_pool');

    // Never an all clear on a read that was not whole: here every source was
    // read, so the page says so, and says when the next read is.
    const seen = page.getByRole('region', { name: 'What Kennel Club could see' });
    await expect(seen.getByText('Read', { exact: true })).toHaveCount(3);
    await expect(page.getByText(/Next read from GitHub/)).toBeVisible();
    await auditThePage(page, 'a repository');
  });

  test('the list narrows by the API’s own filters, and says so when nothing matches', async ({
    page,
  }) => {
    await goto(page, '/kennel/repositories?severity=error', 'Repositories');
    const rows = dataRows(grid(page, 'Repositories'));
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText(PUBLIC_REPO);
    await expect(rows.first()).toContainText('Needs attention');

    // A filter is a chip that can be taken off, and taking it off widens the list.
    await page.getByRole('button', { name: /Remove.*Severity/i }).click();
    await expect(rows).toHaveCount(3);

    await goto(page, '/kennel/repositories?state=best_in_show', 'Repositories');
    const quiet = (await overview(page)).states.best_in_show;
    expect(quiet, 'some repository has nothing open').toBeGreaterThan(0);
    await expect(rows).toHaveCount(quiet);
    await expect(rows.filter({ hasText: PUBLIC_REPO })).toHaveCount(0);

    await goto(page, '/kennel/repositories?code=exposure.fork_code_ran', 'Repositories');
    await expect(page.getByText('No repositories match those filters')).toBeVisible();
    await page.getByRole('button', { name: 'Clear filters' }).click();
    await expect(rows).toHaveCount(3);
    await auditThePage(page, 'the list');
  });

  test('a repository that was not looked at yet is not an all clear, and one that was read in full is', async ({
    page,
  }) => {
    const quiet = await quietRepository(page);
    await goto(page, `/kennel/repositories/${quiet.id}`, quiet.name);
    // Nothing is open and everything was read: the one place "best in show" is
    // earned, and the page says what earned it.
    await expect(
      page.getByText('Every check that is turned on ran against everything it needs'),
    ).toBeVisible();
    await expect(page.getByRole('article')).toHaveCount(0);

    // The same page for a read that was not whole must not say so. A held read is
    // what an installation under a rate limit looks like, which Kennel Club
    // reports as a state and never as a fault.
    await page.route(`**/api/v1/kennel/repositories/${quiet.id}`, async (route) => {
      const real = await route.fetch();
      const body = (await real.json()) as Record<string, unknown>;
      body.state = 'partial';
      body.complete = false;
      body.coverage = [
        {
          source: 'runs',
          label: 'Workflow runs',
          state: 'held',
          reason: 'GitHub has asked Zoomies to wait, so nothing was read.',
          permission: 'Repository permissions: Actions: Read-only',
        },
      ];
      return route.fulfill({ response: real, json: body });
    });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await expect(page.getByText('This is not an all clear.')).toBeVisible();
    await expect(page.getByText('Nothing open, and not an all clear')).toBeVisible();
    await expect(page.getByText('Held for a rate limit')).toBeVisible();
    await expect(page.getByText('Every check that is turned on ran')).toHaveCount(0);
  });

  // Best in show is the kennel word, so it follows the vocabulary switch like the
  // runner states do. The list's "Open" column also says "No open findings", so
  // these look at the standing itself, on the page that is about one repository.
  test('best in show says what it means in plain words', async ({ page }) => {
    await page.addInitScript(() =>
      localStorage.setItem('zoomies.prefs', JSON.stringify({ statusStyle: 'off' })),
    );
    const quiet = await quietRepository(page);
    await goto(page, `/kennel/repositories/${quiet.id}`, quiet.name);
    await expect(page.getByText('No open findings', { exact: true }).first()).toBeVisible();
    await expect(page.getByText('Best in show', { exact: true })).toHaveCount(0);
  });

  test('best in show is the kennel word when the kennel words are on', async ({ page }) => {
    await page.addInitScript(() =>
      localStorage.setItem('zoomies.prefs', JSON.stringify({ statusStyle: 'cute' })),
    );
    const quiet = await quietRepository(page);
    await goto(page, `/kennel/repositories/${quiet.id}`, quiet.name);
    await expect(page.getByText('Best in show', { exact: true }).first()).toBeVisible();
  });

  test('a recheck is asked for, and a second ask is told to wait', async ({ page }) => {
    const row = await repository(page, 'acme/api');
    // The real route allows one a repository in five minutes, and this file runs
    // twice against one controller, so the answers are the controller's own
    // sentences, handed back by the test.
    let asked = 0;
    await page.route(`**/api/v1/kennel/repositories/${row.id}/recheck`, async (route) => {
      asked += 1;
      if (asked === 1) {
        const real = await page.request.get(`/api/v1/kennel/repositories/${row.id}`);
        return route.fulfill({
          status: 202,
          contentType: 'application/json',
          body: JSON.stringify({ ...(await real.json()), next_due_at: null }),
        });
      }
      return route.fulfill({
        status: 429,
        headers: { 'retry-after': '290' },
        contentType: 'application/json',
        body: JSON.stringify({
          error: {
            code: 'rate_limited',
            message:
              'this repository was asked to be read again a moment ago; it can be asked again at 12:00:00 UTC',
          },
        }),
      });
    });
    await goto(page, `/kennel/repositories/${row.id}`, 'acme/api');
    await page.getByRole('button', { name: 'Recheck' }).click();
    await expect(page.locator('.toast[data-tone="success"]')).toContainText('Recheck requested');
    await expect(page.getByText('Due to be read now.')).toBeVisible();

    await page.getByRole('button', { name: 'Recheck' }).click();
    await expect(page.locator('.toast[data-tone="error"]')).toContainText('asked to be read again');
  });

  test('a repository the controller changes is repainted in place', async ({ page }) => {
    const row = await repository(page, PUBLIC_REPO);
    await goto(page, `/kennel/repositories/${row.id}`, PUBLIC_REPO);
    await expect(page.getByRole('article')).toHaveCount(2);
    await plantMarker(page);

    try {
      // Turning one check off is a decision the controller applies at once and
      // announces on the stream, so this is the real path end to end: nothing is
      // injected, the connection is never cut, and so nothing makes the page ask
      // again. Only the frame can repaint it.
      await patchSettings(page, { 'kennel.disabled_checks': ['exposure.public_repo_weak_pool'] });
      await expect(page.getByRole('article')).toHaveCount(1, { timeout: 20_000 });
      // With the weak pool no longer a finding, the public-repository finding is
      // back to its usual severity, which is a warning.
      await expect(page.getByText('1 warning', { exact: true })).toBeVisible();
      await expectNoReload(page);
    } finally {
      await patchSettings(page, { 'kennel.disabled_checks': [] });
    }
    await expect(page.getByRole('article')).toHaveCount(2, { timeout: 20_000 });
  });

  test('a repository that stops being tracked says so instead of showing what it had', async ({
    page,
  }) => {
    const row = await repository(page, PUBLIC_REPO);
    await goto(page, `/kennel/repositories/${row.id}`, PUBLIC_REPO);
    await expect(page.getByRole('article')).toHaveCount(2);

    const frame = `id: 999991\nevent: kennel.deleted\ndata: ${JSON.stringify({ id: row.id })}\n\n`;
    await page.route('**/api/v1/events*', (route) =>
      route.fulfill({
        status: 200,
        headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
        body: frame,
      }),
    );
    await page.evaluate(() => {
      window.dispatchEvent(new Event('offline'));
      window.dispatchEvent(new Event('online'));
    });
    await expect(page.getByText('Kennel Club no longer tracks this repository')).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('article')).toHaveCount(0);
  });

  test('a page that lost the stream asks again when it comes back', async ({ page }) => {
    await goto(page, '/kennel', 'Kennel Club');
    await expect(page.locator('.connection')).toHaveAttribute('data-state', 'live');
    await expect(page.getByText('Turned off')).toHaveCount(0);

    let cut = true;
    await page.route('**/api/v1/events*', async (route) => {
      if (cut) return route.abort('connectionfailed');
      // The stream is back and says nothing: no replay of what was missed and no
      // resync. A page that caught up only because the controller told it what it
      // missed would pass the test without ever asking; this one has to ask.
      return route.fulfill({
        status: 200,
        headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
        body: ': back\n\n',
      });
    });
    await page.evaluate(() => window.dispatchEvent(new Event('offline')));
    await expect(page.locator('.connection')).not.toHaveAttribute('data-state', 'live');

    // Something changes while nobody is listening, so no frame will ever say so.
    await patchSettings(page, { 'kennel.disabled_checks': ['capacity'] });
    try {
      cut = false;
      await page.evaluate(() => window.dispatchEvent(new Event('online')));
      await expect(page.getByText('Turned off').first()).toBeVisible({ timeout: 20_000 });
    } finally {
      await patchSettings(page, { 'kennel.disabled_checks': [] });
    }
  });

  test('a problem about a repository opens the repository', async ({ page }) => {
    await goto(page, '/');
    await page.getByRole('button', { name: /^Problems\./ }).click();
    const drawer = page.getByRole('dialog', { name: 'Problems' });
    await expect(drawer).toBeVisible();
    const entry = drawer.getByRole('listitem').filter({ hasText: 'kennel.exposure' });
    await expect(entry).toContainText(PUBLIC_REPO);
    await entry.getByRole('link', { name: 'Open the repository' }).click();
    await expect(page).toHaveURL(/\/kennel\/repositories\/kcr_/);
    await expect(page.getByRole('heading', { level: 1, name: PUBLIC_REPO })).toBeVisible();
  });

  test('names a stranger chose are shown as text and never run', async ({ page }) => {
    const payload = '<img src=x onerror=alert(1)>';
    const dialogs: string[] = [];
    page.on('dialog', (dialog) => {
      dialogs.push(dialog.message());
      void dialog.dismiss();
    });
    const row = await repository(page, PUBLIC_REPO);

    // A repository's name, in the Overview and the list, and the label on a piece
    // of evidence, in the repository: the three places a name reaches this page.
    await page.route('**/api/v1/kennel', async (route) => {
      const real = await route.fetch();
      const body = (await real.json()) as { attention: Array<{ name: string }> };
      for (const a of body.attention) a.name = payload;
      return route.fulfill({ response: real, json: body });
    });
    await page.route('**/api/v1/kennel/repositories?*', async (route) => {
      const real = await route.fetch();
      const body = (await real.json()) as { items: Array<{ name: string }> };
      for (const item of body.items) item.name = payload;
      return route.fulfill({ response: real, json: body });
    });
    await page.route(`**/api/v1/kennel/repositories/${row.id}`, async (route) => {
      const real = await route.fetch();
      const body = (await real.json()) as Row;
      body.name = payload;
      for (const finding of body.findings)
        for (const item of finding.evidence) {
          item.label = payload;
          // A reference is only ever linked when it has the shape of an
          // identifier the controller made; this one is a path to somewhere else.
          item.ref = '../settings/users';
        }
      return route.fulfill({ response: real, json: body });
    });

    for (const [path, heading] of [
      ['/kennel', 'Kennel Club'],
      ['/kennel/repositories', 'Repositories'],
      [`/kennel/repositories/${row.id}`, payload],
    ] as const) {
      await goto(page, path, heading);
      await expect(page.getByText(payload, { exact: true }).first(), path).toBeVisible();
      await expect(page.locator('img[src="x"]'), `${path} made an image`).toHaveCount(0);
    }
    await expect(
      page.locator('a[href*="settings/users"]'),
      'a reference that is not an identifier was linked',
    ).toHaveCount(0);
    expect(dialogs, 'nothing a stranger named ran').toEqual([]);
  });

  test('it fits a phone', async ({ page, isMobile }) => {
    test.skip(!isMobile, 'the phone project checks the narrow widths');
    const row = await repository(page, PUBLIC_REPO);
    for (const width of [360, 412]) {
      await page.setViewportSize({ width, height: 780 });
      for (const [path, heading] of [
        ['/kennel', 'Kennel Club'],
        ['/kennel/repositories', 'Repositories'],
        [`/kennel/repositories/${row.id}`, PUBLIC_REPO],
      ] as const) {
        await goto(page, path, heading);
        await expect(page.getByRole('main')).toBeVisible();
        await auditThePage(page, `${path} at ${width}px`);
      }
    }
  });

  /* -- waiving ----------------------------------------------------------------- */

  test.describe('waiving a finding', () => {
    interface Finding {
      code: string;
      severity: string;
      subject: string;
      title: string;
    }
    interface Waived {
      finding: Finding;
      waiver: { id: string; reason: string; by: string; expires_at: string };
    }
    interface Detail {
      id: string;
      counts: { error: number; warning: number };
      findings: Finding[];
      waived: Waived[];
    }

    const WEAK_POOL = 'exposure.public_repo_weak_pool';

    async function detail(page: Page, id: string): Promise<Detail> {
      return (await page.request
        .get(`/api/v1/kennel/repositories/${id}`)
        .then((r) => r.json())) as Detail;
    }

    /** What this block made is the shared controller's to be rid of, passed or failed. */
    async function endWaivers(page: Page, id: string): Promise<void> {
      for (const entry of (await detail(page, id)).waived) {
        await page.request.delete(`/api/v1/kennel/repositories/${id}/waivers/${entry.waiver.id}`);
      }
    }

    async function target(page: Page): Promise<{ id: string; finding: Finding }> {
      const row = await repository(page, PUBLIC_REPO);
      const finding = (await detail(page, row.id)).findings.find((f) => f.code === WEAK_POOL);
      expect(finding, `${PUBLIC_REPO} has the finding this block waives`).toBeTruthy();
      return { id: row.id, finding: finding as Finding };
    }

    // A toast from the step before can still be on screen, so each assertion
    // names its own.
    const toast = (page: Page, tone: 'success' | 'error', text: string) =>
      page.locator(`.toast[data-tone="${tone}"]`, { hasText: text });

    // A finding is a section of the page and not a row, so it is found by what it
    // is about. The class and not the role: while a dialog is open the page
    // behind it is hidden from the accessibility tree, and a query by role would
    // find nothing whether or not the finding was still there.
    const article = (page: Page, finding: Finding) =>
      page.locator('article.finding', { hasText: finding.title });

    test('a finding is waived with a reason, listed under Waived, and ended again', async ({
      page,
    }) => {
      const { id, finding } = await target(page);
      try {
        const before = await detail(page, id);
        await goto(page, `/kennel/repositories/${id}`, PUBLIC_REPO);
        await page.getByRole('button', { name: `Waive: ${finding.title}` }).click();

        const dialog = page.getByRole('dialog', { name: 'Waive this finding' });
        await expect(dialog).toContainText(finding.title);
        const waive = dialog.getByRole('button', { name: 'Waive', exact: true });
        const reason = dialog.getByRole('textbox', { name: /Why this is acceptable here/ });

        // The rule is said before it is broken, and ten spaces are not ten
        // characters of reason.
        await expect(waive).toBeDisabled();
        await reason.fill('too short');
        await expect(dialog).toContainText('At least 10 characters');
        await expect(waive).toBeDisabled();
        await reason.fill(' '.repeat(12));
        await expect(waive).toBeDisabled();

        // Whatever the screen, the form fits in it.
        await auditThePage(page, 'the waive dialog');
        const box = await dialog.boundingBox();
        const viewport = page.viewportSize();
        expect(box, 'the dialog is drawn').toBeTruthy();
        expect(box!.x).toBeGreaterThanOrEqual(0);
        expect(box!.x + box!.width).toBeLessThanOrEqual(viewport!.width);

        const why = 'The pool is rebuilt from a clean image every night, and this is a docs site.';
        await reason.fill(why);
        await expect(dialog).toContainText('of 500 characters');
        await dialog.getByLabel('Waive for').selectOption({ label: '30 days' });
        await expect(waive).toBeEnabled();
        const asked = Date.now();
        await waive.click();

        await expect(toast(page, 'success', 'Finding waived')).toBeVisible();
        await expect(dialog).toBeHidden();

        // It is no longer open, and it is not gone: it is listed with who, why and
        // until when.
        await expect(article(page, finding)).toHaveCount(0);
        const waived = page.locator('details.waived');
        await expect(waived).toContainText(finding.title);
        await expect(waived).toContainText(why);
        await expect(waived).toContainText('Waived by');

        const after = await detail(page, id);
        expect(after.counts.error, 'one fewer error is open').toBe(before.counts.error - 1);
        expect(after.waived).toHaveLength(1);
        const days = (Date.parse(after.waived[0]!.waiver.expires_at) - asked) / 86_400_000;
        expect(days, 'the waiver runs for the 30 days that were chosen').toBeGreaterThan(29.9);
        expect(days).toBeLessThan(30.1);

        // Ending it asks first, says what happens, and puts the finding back.
        await page.getByRole('button', { name: `End the waiver: ${finding.title}` }).click();
        const confirm = page.getByRole('dialog', { name: 'End waiver' });
        await expect(confirm).toContainText('open again straight away');
        await confirm.getByRole('button', { name: 'End waiver', exact: true }).click();
        await expect(toast(page, 'success', 'Waiver ended')).toBeVisible();
        await expect(article(page, finding)).toBeVisible();
        expect((await detail(page, id)).waived).toHaveLength(0);
      } finally {
        await endWaivers(page, id);
      }
    });

    test('a finding that stops being open while the form is open is said so', async ({ page }) => {
      const { id, finding } = await target(page);
      try {
        await goto(page, `/kennel/repositories/${id}`, PUBLIC_REPO);
        await page.getByRole('button', { name: `Waive: ${finding.title}` }).click();
        const dialog = page.getByRole('dialog', { name: 'Waive this finding' });
        await dialog
          .getByRole('textbox', { name: /Why this is acceptable here/ })
          .fill('Decided before the check was turned off, and typed slowly.');

        // The check is turned off in Settings while the form is open, so the
        // finding the form is about is not there any more.
        await patchSettings(page, { 'kennel.disabled_checks': [WEAK_POOL] });
        await expect(article(page, finding)).toHaveCount(0);

        await dialog.getByRole('button', { name: 'Waive', exact: true }).click();
        // The controller's own sentence, and the form stays for the person to read it.
        await expect(dialog.getByRole('alert')).toContainText('matches no open finding');
        await expect(dialog).toBeVisible();
        expect((await detail(page, id)).waived, 'nothing was waived').toHaveLength(0);
      } finally {
        await patchSettings(page, { 'kennel.disabled_checks': [] });
        await endWaivers(page, id);
      }
    });

    test('what the controller says is wrong sits beside the field it is about', async ({
      page,
    }) => {
      const { id, finding } = await target(page);
      // The form will not send a reason that is too short, so the controller's
      // field answers are handed back by the test, in the shape its 422 has.
      await page.route(`**/api/v1/kennel/repositories/${id}/waivers`, (route) =>
        route.fulfill({
          status: 422,
          contentType: 'application/json',
          body: JSON.stringify({
            error: { code: 'invalid', message: 'the waiver was refused' },
            errors: [
              {
                field: 'reason',
                message: 'may not contain control or direction-changing characters',
              },
              { field: 'expires_at', message: 'must be in the future' },
            ],
          }),
        }),
      );
      await goto(page, `/kennel/repositories/${id}`, PUBLIC_REPO);
      await page.getByRole('button', { name: `Waive: ${finding.title}` }).click();
      const dialog = page.getByRole('dialog', { name: 'Waive this finding' });
      await dialog
        .getByRole('textbox', { name: /Why this is acceptable here/ })
        .fill('A reason that is long enough to be sent.');
      await dialog.getByRole('button', { name: 'Waive', exact: true }).click();

      await expect(
        dialog.getByRole('textbox', { name: /Why this is acceptable here/ }),
      ).toHaveAccessibleDescription(/may not contain control or direction-changing characters/);
      await expect(dialog.getByLabel('Waive for')).toHaveAccessibleDescription(
        /must be in the future/,
      );
      await expect(dialog).toBeVisible();
      // A refusal that is about the fields is not also a toast.
      await expect(page.locator('.toast')).toHaveCount(0);
    });

    test('a refusal about the person and not a field is said as a sentence', async ({ page }) => {
      const { id, finding } = await target(page);
      await page.route(`**/api/v1/kennel/repositories/${id}/waivers`, (route) =>
        route.fulfill({
          status: 403,
          contentType: 'application/json',
          body: JSON.stringify({
            error: {
              code: 'forbidden',
              message: 'waiving an error needs the admin role, and you are an operator',
            },
          }),
        }),
      );
      await goto(page, `/kennel/repositories/${id}`, PUBLIC_REPO);
      await page.getByRole('button', { name: `Waive: ${finding.title}` }).click();
      const dialog = page.getByRole('dialog', { name: 'Waive this finding' });
      await dialog
        .getByRole('textbox', { name: /Why this is acceptable here/ })
        .fill('A reason that is long enough to be sent.');
      await dialog.getByRole('button', { name: 'Waive', exact: true }).click();

      await expect(toast(page, 'error', 'waiving an error needs the admin role')).toBeVisible();
      await expect(dialog).toBeVisible();
    });

    test('an operator may waive a warning and is told an error is an administrator’s', async ({
      page,
    }) => {
      const { id } = await target(page);
      const real = await detail(page, id);
      const error = real.findings.find((f) => f.severity === 'error') as Finding;
      const warning: Finding = {
        code: 'capacity.unserved_label',
        severity: 'warning',
        subject: '',
        title: 'Jobs waited for a label no pool serves',
      };
      const done: Finding = {
        code: 'capacity.job_hit_default_limit',
        severity: 'warning',
        subject: '',
        title: 'A job ran until GitHub stopped it at six hours',
      };
      const full = (f: Finding) => ({ ...f, detail: 'Detail.', fix: 'Fix.', evidence: [] });
      await page.route(`**/api/v1/kennel/repositories/${id}`, async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as Record<string, unknown> & { findings: unknown[] };
        body.findings = [...body.findings, full(warning)];
        body.waived = [
          {
            finding: full(done),
            waiver: {
              id: 'kcw_pretend01',
              code: done.code,
              subject: '',
              severity: 'warning',
              reason: 'A nightly export that is meant to take hours.',
              by: 'somebody else',
              at: new Date().toISOString(),
              expires_at: new Date(Date.now() + 30 * 86_400_000).toISOString(),
            },
          },
        ];
        return route.fulfill({ response, json: body });
      });
      // The fixture controller has authentication off, so everybody there is an
      // administrator. The page is told who it is talking to, and asks no
      // different questions of the controller.
      const actAs = async (role: 'viewer' | 'operator') => {
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
      };

      await actAs('operator');
      await goto(page, `/kennel/repositories/${id}`, PUBLIC_REPO);
      await expect(page.getByRole('button', { name: `Waive: ${warning.title}` })).toBeVisible();
      await expect(page.getByRole('button', { name: `Waive: ${error.title}` })).toHaveCount(0);
      await expect(article(page, error)).toContainText('Only an administrator can waive an error');
      await expect(article(page, warning)).not.toContainText('Only an administrator');
      // Any operator may end any waiver: ending one only makes Kennel Club stricter.
      await expect(
        page.getByRole('button', { name: `End the waiver: ${done.title}` }),
      ).toBeVisible();

      await page.unroute('**/api/v1/auth/session');
      await page.route('**/api/v1/auth/session', (route) =>
        route.fulfill({
          json: { kind: 'token', id: 'tok_pretend', name: 'a token', role: 'viewer' },
        }),
      );
      await page.reload();
      await expect(page.getByRole('heading', { level: 1, name: PUBLIC_REPO })).toBeVisible();
      await expect(article(page, warning)).toBeVisible();
      // A viewer is offered nothing, and is not told about a rule that is not theirs.
      await expect(page.getByRole('button', { name: /^Waive/ })).toHaveCount(0);
      await expect(page.getByRole('button', { name: /^End the waiver/ })).toHaveCount(0);
      await expect(page.getByText('Only an administrator')).toHaveCount(0);
    });

    test('a reason somebody typed is shown as text and never run', async ({ page }) => {
      const { id, finding } = await target(page);
      const reason =
        '<img src=x onerror=alert(1)> <b>bold</b> because the docs site has no secrets';
      const dialogs: string[] = [];
      page.on('dialog', (dialog) => {
        dialogs.push(dialog.message());
        void dialog.dismiss();
      });
      try {
        await goto(page, `/kennel/repositories/${id}`, PUBLIC_REPO);
        await page.getByRole('button', { name: `Waive: ${finding.title}` }).click();
        const form = page.getByRole('dialog', { name: 'Waive this finding' });
        await form.getByRole('textbox', { name: /Why this is acceptable here/ }).fill(reason);
        await form.getByRole('button', { name: 'Waive', exact: true }).click();
        await expect(toast(page, 'success', 'Finding waived')).toBeVisible();

        const waived = page.locator('details.waived');
        await expect(waived.getByText(reason, { exact: true })).toBeVisible();
        await expect(page.locator('img[src="x"]'), 'a reason made an image').toHaveCount(0);
        await expect(waived.locator('b'), 'a reason made markup').toHaveCount(0);
        expect(dialogs, 'nothing a person typed ran').toEqual([]);
      } finally {
        await endWaivers(page, id);
      }
    });

    test('cancelling leaves nothing waived, and the form opens empty again', async ({ page }) => {
      const { id, finding } = await target(page);
      await goto(page, `/kennel/repositories/${id}`, PUBLIC_REPO);
      const open = () => page.getByRole('button', { name: `Waive: ${finding.title}` }).click();
      const dialog = page.getByRole('dialog', { name: 'Waive this finding' });
      const reason = dialog.getByRole('textbox', { name: /Why this is acceptable here/ });

      await open();
      await reason.fill('Half of a decision, abandoned.');
      await page.keyboard.press('Escape');
      await expect(dialog).toBeHidden();
      await open();
      await expect(reason, 'the last form’s reason was not kept').toHaveValue('');
      await dialog.getByRole('button', { name: 'Cancel' }).click();
      await expect(dialog).toBeHidden();
      expect((await detail(page, id)).waived).toHaveLength(0);
      await expect(article(page, finding)).toBeVisible();
    });

    test('a waiver somebody else already ended is said so', async ({ page }) => {
      const { id, finding } = await target(page);
      try {
        const made = await page.request.put(`/api/v1/kennel/repositories/${id}/waivers`, {
          data: {
            code: finding.code,
            subject: finding.subject,
            reason: 'Made through the API, to be ended under the page.',
            expires_at: new Date(Date.now() + 30 * 86_400_000).toISOString(),
          },
        });
        expect(made.ok(), 'the waiver was made').toBeTruthy();
        const waiver = (await detail(page, id)).waived[0]!.waiver;

        await goto(page, `/kennel/repositories/${id}`, PUBLIC_REPO);
        await page.getByRole('button', { name: `End the waiver: ${finding.title}` }).click();
        const confirm = page.getByRole('dialog', { name: 'End waiver' });
        // A colleague ends it first.
        const ended = await page.request.delete(
          `/api/v1/kennel/repositories/${id}/waivers/${waiver.id}`,
        );
        expect(ended.ok()).toBeTruthy();

        await confirm.getByRole('button', { name: 'End waiver', exact: true }).click();
        await expect(toast(page, 'error', 'That waiver was not ended')).toBeVisible();
        // The confirmation stays, so the person reads why instead of wondering.
        await expect(confirm).toBeVisible();
      } finally {
        await endWaivers(page, id);
      }
    });
  });
});
