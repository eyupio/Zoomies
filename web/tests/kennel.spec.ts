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
  states: { attention: number; best_in_show: number; partial: number; pending: number };
  counts: { error: number; warning: number; waived: number };
}

async function overview(page: Page): Promise<Overview> {
  return (await page.request.get('/api/v1/kennel').then((r) => r.json())) as Overview;
}

/**
 * One of the cards on the Overview, found by its label. A card that opens a list
 * has an arrow after the label, which is part of the label's text.
 */
function tile(page: Page, label: string) {
  return page
    .locator('dl.metrics > div')
    .filter({ has: page.getByRole('term').filter({ hasText: new RegExp(`^${label}↗?$`) }) });
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
    // it on, from the page that explains what it would be turning on.
    await expect(page.getByRole('button', { name: 'Turn on Kennel Club' })).toBeVisible();

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
    await expect(page.getByRole('button', { name: 'Turn on Kennel Club' })).toBeVisible();
    await auditThePage(page, 'the off list');
  });

  test('AI Context is one press away, and its old addresses open the same pages', async ({
    page,
  }) => {
    await goto(page, '/kennel', 'Kennel Club');
    await page
      .getByRole('navigation', { name: 'Kennel Club' })
      .getByRole('link', { name: 'AI Context', exact: true })
      .click();
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

    await expect(tile(page, 'Repositories')).toContainText('3');
    // The number of repositories that need attention grows with the controller's
    // age (see quietRepository), so the page is held to what the API says now, and
    // to there being at least the one with the errors.
    const now = await overview(page);
    expect(now.states.attention).toBeGreaterThanOrEqual(1);
    await expect(tile(page, 'Need attention')).toContainText(String(now.states.attention));
    await expect(tile(page, 'Errors')).toContainText(String(now.counts.error));
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
    await expect(tile(page, 'Need attention')).toContainText('At least');
    const note = page.getByRole('note').filter({ hasText: 'GitHub asked Zoomies to wait' });
    await expect(note).toContainText('acme');
    await expect(note).toContainText('Held for a rate limit');
  });

  test('a refresh that fails keeps what the page had, says so, and recovers', async ({ page }) => {
    await goto(page, '/kennel', 'Kennel Club');
    const repositories = tile(page, 'Repositories');
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
    // A link about findings opens the section that lists them.
    await expect(page).toHaveURL(/\/kennel\/repositories\/kcr_[^/]+\/ci$/);
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
    await goto(page, `/kennel/repositories/${quiet.id}/ci`, quiet.name);
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
    await goto(page, `/kennel/repositories/${row.id}/ci`, 'acme/api');
    await page.getByRole('button', { name: 'Recheck' }).click();
    await expect(page.locator('.toast[data-tone="success"]')).toContainText('Recheck requested');
    await expect(page.getByText('Due to be read now.')).toBeVisible();

    await page.getByRole('button', { name: 'Recheck' }).click();
    await expect(page.locator('.toast[data-tone="error"]')).toContainText('asked to be read again');
  });

  test('a repository the controller changes is repainted in place', async ({ page }) => {
    const row = await repository(page, PUBLIC_REPO);
    await goto(page, `/kennel/repositories/${row.id}/ci`, PUBLIC_REPO);
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
    await goto(page, `/kennel/repositories/${row.id}/ci`, PUBLIC_REPO);
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

  test('the problems list carries one entry for Kennel Club, and it opens the list of repositories with errors', async ({
    page,
  }) => {
    // Kennel Club has every repository and its findings, so the drawer says that
    // some are exposed and not which: a drawer with a line per repository would
    // say it twice, and grow with the fleet.
    const listed = (await page.request
      .get('/api/v1/kennel/repositories?severity=error&limit=1')
      .then((r) => r.json())) as { total: number };
    expect(listed.total, 'the demo fleet has repositories with an error open').toBeGreaterThan(0);
    const title =
      listed.total === 1
        ? 'Kennel Club: 1 repository has an exposure error open'
        : `Kennel Club: ${listed.total} repositories have an exposure error open`;

    await goto(page, '/');
    await page.getByRole('button', { name: /^Problems\./ }).click();
    const drawer = page.getByRole('dialog', { name: 'Problems' });
    await expect(drawer).toBeVisible();
    const entries = drawer.getByRole('listitem').filter({ hasText: 'kennel.exposure' });
    await expect(entries).toHaveCount(1);
    await expect(entries).toContainText(title);
    // Not one of them is named.
    for (const name of FIXTURE.repos) await expect(entries).not.toContainText(name);

    await entries.getByRole('link', { name: 'Open Kennel Club' }).click();
    await expect(page).toHaveURL(/\/kennel\/repositories\?severity=error$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Repositories' })).toBeVisible();
    await expect(page.getByRole('link', { name: PUBLIC_REPO })).toBeVisible();
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
    // of evidence, in the repository's CI section: the places a name reaches this
    // page.
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
      [`/kennel/repositories/${row.id}/ci`, payload],
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
        [`/kennel/repositories/${row.id}/ci`, PUBLIC_REPO],
      ] as const) {
        await goto(page, path, heading);
        await expect(page.getByRole('main')).toBeVisible();
        await auditThePage(page, `${path} at ${width}px`);
      }
    }
  });

  /* -- sections and what the fleet knows --------------------------------------- */

  test.describe('a repository’s sections', () => {
    const DAY = 24 * 60 * 60 * 1000;

    interface Group {
      keys: Record<string, string>;
      count: number;
      succeeded: number;
      failed: number;
      cancelled: number;
      fleet_failed: number;
      queue_wait: { p50_ms: number | null };
      duration: { p50_ms: number | null };
    }

    /** What the jobs API counts for a repository over the Overview's first window, a week. */
    async function stats(page: Page, name: string, groupBy?: 'pool' | 'host'): Promise<Group[]> {
      const query = new URLSearchParams({
        repo: name,
        hosted: 'false',
        since: new Date(Date.now() - 7 * DAY).toISOString(),
      });
      if (groupBy) query.set('group_by', groupBy);
      const res = await page.request.get(`/api/v1/jobs/stats?${query}`);
      expect(res.ok(), `job stats for ${name}`).toBeTruthy();
      return ((await res.json()) as { groups: Group[] }).groups;
    }

    /** `formatDuration` for the tens of seconds and the minutes the demo's jobs take. */
    function spoken(ms: number | null): string {
      if (ms === null) return '--';
      const seconds = Math.round(ms / 1000);
      return seconds < 60
        ? `${seconds}s`
        : `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`;
    }

    const figure = (page: Page, label: string) =>
      page.locator('.metric').filter({ hasText: label }).locator('.value');

    /** A group as the API words it, with only what a test says. */
    function stub(over: Partial<Group>): Group {
      return {
        keys: {},
        count: 0,
        succeeded: 0,
        failed: 0,
        cancelled: 0,
        fleet_failed: 0,
        queue_wait: { p50_ms: null },
        duration: { p50_ms: null },
        ...over,
      };
    }

    test('it opens on the Overview, and the figures are what the jobs API counted', async ({
      page,
    }) => {
      for (const name of FIXTURE.repos) {
        const row = await repository(page, name);
        await goto(page, `/kennel/repositories/${row.id}`, name);
        await expect(page.getByRole('tab', { name: 'Overview' }), name).toHaveAttribute(
          'aria-selected',
          'true',
        );
        // Findings are the CI section's: the Overview says where they are and
        // lists none.
        await expect(page.getByRole('article'), name).toHaveCount(0);

        const [all] = await stats(page, name);
        expect(all, `${name} ran jobs this week`).toBeTruthy();
        const decided = all!.succeeded + all!.failed;
        await expect(figure(page, 'Jobs finished'), name).toHaveText(String(all!.count));
        await expect(figure(page, 'Success rate'), name).toHaveText(
          decided === 0 ? '--' : `${Math.round((all!.succeeded / decided) * 100)}%`,
        );
        await expect(figure(page, 'Time waiting for a runner'), name).toHaveText(
          spoken(all!.queue_wait.p50_ms),
        );
        await expect(figure(page, 'Time running'), name).toHaveText(spoken(all!.duration.p50_ms));
        // A time is never claimed for a job that has none.
        await expect(
          page.locator('.metric').filter({ hasText: 'Time running' }),
          name,
        ).toContainText('leaves out cancelled and skipped jobs');
      }
    });

    test('a repository nothing succeeded for says 0%, and one with no verdict says there is none', async ({
      page,
    }) => {
      const row = await repository(page, PUBLIC_REPO);
      let answer = stub({ count: 3, failed: 3, fleet_failed: 1 });
      await page.route('**/api/v1/jobs/stats?*', async (route) => {
        const real = await route.fetch();
        const body = (await real.json()) as { groups: Group[] };
        if (!new URL(route.request().url()).searchParams.has('group_by')) body.groups = [answer];
        return route.fulfill({ response: real, json: body });
      });

      await goto(page, `/kennel/repositories/${row.id}`, PUBLIC_REPO);
      // Zero is a rate. A dash would say nobody had asked.
      await expect(figure(page, 'Success rate')).toHaveText('0%');
      await expect(page.locator('.metric').filter({ hasText: 'Jobs finished' })).toContainText(
        '3 failed, 1 of them lost to this fleet',
      );

      // Cancelled and skipped jobs have no verdict, so there is nothing to rate.
      answer = stub({ count: 2, cancelled: 2 });
      await page.reload({ waitUntil: 'domcontentloaded' });
      await expect(figure(page, 'Success rate')).toHaveText('--');
      await expect(page.locator('.metric').filter({ hasText: 'Jobs finished' })).toContainText(
        '2 cancelled or skipped',
      );
      await expect(page.locator('.metric').filter({ hasText: 'Time running' })).toContainText(
        'Nothing was measured',
      );
    });

    test('the window is widened to 30 days, and says that is only what the fleet still holds', async ({
      page,
    }) => {
      const row = await repository(page, 'acme/api');
      await goto(page, `/kennel/repositories/${row.id}`, 'acme/api');
      await expect(page.getByRole('button', { name: '7 days', exact: true })).toHaveAttribute(
        'aria-pressed',
        'true',
      );
      await expect(page.getByText(/by default that is the last 30 days/)).toHaveCount(0);

      const asked = page.waitForRequest((request) => {
        const url = new URL(request.url());
        return (
          url.pathname.endsWith('/jobs/stats') &&
          !url.searchParams.has('group_by') &&
          Date.now() - Date.parse(url.searchParams.get('since') ?? '') > 29 * DAY
        );
      });
      await page.getByRole('button', { name: '30 days', exact: true }).click();
      const since = Date.parse(new URL((await asked).url()).searchParams.get('since') ?? '');
      expect(Date.now() - since, 'the window starts thirty days back').toBeLessThan(30.1 * DAY);
      await expect(page.getByRole('button', { name: '30 days', exact: true })).toHaveAttribute(
        'aria-pressed',
        'true',
      );
      await expect(page.getByText(/by default that is the last 30 days/)).toBeVisible();
    });

    test('it asks only for this repository’s own jobs, and Refresh asks again', async ({
      page,
    }) => {
      const row = await repository(page, 'acme/api');
      const counted: URL[] = [];
      const queued: URL[] = [];
      page.on('request', (request) => {
        const url = new URL(request.url());
        if (url.pathname.endsWith('/jobs/stats')) counted.push(url);
        else if (url.pathname.endsWith('/jobs') && url.searchParams.has('unmatched'))
          queued.push(url);
      });

      await goto(page, `/kennel/repositories/${row.id}`, 'acme/api');
      await expect(figure(page, 'Jobs finished')).toBeVisible();
      // The totals, then the same jobs by pool and by host.
      expect(counted.map((url) => url.searchParams.get('group_by') ?? '').sort()).toEqual([
        '',
        'host',
        'pool',
      ]);
      for (const url of counted) {
        // Another repository's jobs, or jobs on GitHub's own runners, would be
        // figures the fleet did not earn.
        expect(url.searchParams.getAll('repo'), url.search).toEqual(['acme/api']);
        expect(url.searchParams.get('hosted'), url.search).toBe('false');
      }
      expect(queued.length).toBe(1);
      expect(queued[0]!.searchParams.getAll('repo')).toEqual(['acme/api']);
      expect(queued[0]!.searchParams.get('unmatched')).toBe('true');

      // The page's one Refresh button refreshes everything on it, and asks once:
      // the repository coming back replaces what the Overview was given, which
      // must not read as a reason to count the jobs a second time.
      const reread = page.waitForResponse(
        (response) =>
          response.url().endsWith(`/api/v1/kennel/repositories/${row.id}`) &&
          response.request().method() === 'GET',
      );
      await page.getByRole('button', { name: 'Refresh', exact: true }).click();
      await reread;
      await expect.poll(() => counted.length).toBeGreaterThanOrEqual(6);
      await page.waitForTimeout(500);
      expect(counted.length, 'each of the three counts was asked for once more').toBe(6);
      expect(queued.length, 'and so was the queue').toBe(2);
    });

    test('switching the window while the first read is still out shows it loading, never blank', async ({
      page,
    }) => {
      const row = await repository(page, 'acme/api');
      await page.route('**/api/v1/jobs/stats?*', async (route) => {
        await new Promise((resolve) => setTimeout(resolve, 1_500));
        // The read that was given up on is gone by now, and says so.
        await route.continue().catch(() => undefined);
      });

      await goto(page, `/kennel/repositories/${row.id}`, 'acme/api');
      await page.getByRole('button', { name: '30 days', exact: true }).click();
      await page.waitForTimeout(400);
      await expect(page.locator('.overview .stack')).toBeVisible();
      await expect(page.locator('.metric')).toHaveCount(0);
      await expect(page.getByText('could not be read')).toHaveCount(0);
      await expect(figure(page, 'Jobs finished')).toBeVisible({ timeout: 10_000 });
    });

    test('a page that lost the stream asks the jobs API again when it comes back', async ({
      page,
    }) => {
      const row = await repository(page, 'acme/api');
      let counted = 0;
      page.on('request', (request) => {
        if (new URL(request.url()).pathname.endsWith('/jobs/stats')) counted += 1;
      });
      await goto(page, `/kennel/repositories/${row.id}`, 'acme/api');
      await expect(figure(page, 'Jobs finished')).toBeVisible();
      await expect(page.locator('.connection')).toHaveAttribute('data-state', 'live');
      const before = counted;

      let cut = true;
      await page.route('**/api/v1/events*', async (route) => {
        if (cut) return route.abort('connectionfailed');
        // Back, and saying nothing: only a page that asks for itself catches up.
        return route.fulfill({
          status: 200,
          headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
          body: ': back\n\n',
        });
      });
      await page.evaluate(() => window.dispatchEvent(new Event('offline')));
      await expect(page.locator('.connection')).not.toHaveAttribute('data-state', 'live');
      cut = false;
      await page.evaluate(() => window.dispatchEvent(new Event('online')));
      await expect.poll(() => counted, { timeout: 20_000 }).toBeGreaterThan(before);
    });

    test('a week with no jobs says so and offers the month, and a read that fails offers another go', async ({
      page,
    }) => {
      const row = await repository(page, 'acme/api');
      let failing = false;
      await page.route('**/api/v1/jobs/stats?*', async (route) => {
        if (failing) {
          return route.fulfill({
            status: 500,
            contentType: 'application/json',
            body: JSON.stringify({
              error: { code: 'internal', message: 'the jobs could not be counted' },
            }),
          });
        }
        const real = await route.fetch();
        const body = (await real.json()) as { groups: Group[] };
        const since = Date.parse(new URL(route.request().url()).searchParams.get('since') ?? '');
        if (Date.now() - since < 8 * DAY) body.groups = [];
        return route.fulfill({ response: real, json: body });
      });

      await goto(page, `/kennel/repositories/${row.id}`, 'acme/api');
      await expect(page.getByText('No jobs finished in the last 7 days')).toBeVisible();
      await expect(page.locator('.metric')).toHaveCount(0);
      await page.getByRole('button', { name: 'Look at 30 days' }).click();
      await expect(page.getByText('No jobs finished in the last 7 days')).toHaveCount(0);
      await expect(figure(page, 'Jobs finished')).toBeVisible();

      // What the fleet knows failing to load must not hide what Kennel Club says.
      failing = true;
      await page.getByRole('button', { name: '7 days', exact: true }).click();
      await expect(page.getByText('What the fleet knows could not be read')).toBeVisible();
      await expect(page.getByText('What Kennel Club says')).toBeVisible();
      failing = false;
      await page.getByRole('button', { name: 'Try again' }).click();
      await expect(page.getByText('What the fleet knows could not be read')).toHaveCount(0);
      await expect(page.getByText('No jobs finished in the last 7 days')).toBeVisible();
    });

    test('where it ran names the pools and hosts, links those the fleet has and owns up to the rest', async ({
      page,
    }) => {
      const names = async (resource: 'pools' | 'hosts') => {
        const res = await page.request.get(`/api/v1/${resource}`);
        const body = (await res.json()) as { items?: Array<{ id: string; name: string }> };
        return new Map((body.items ?? []).map((item) => [item.id, item.name]));
      };
      const known = { pool: await names('pools'), host: await names('hosts') };
      let unrecorded = 0;

      for (const name of FIXTURE.repos) {
        const row = await repository(page, name);
        await goto(page, `/kennel/repositories/${row.id}`, name);
        for (const [key, region, path] of [
          ['pool', 'Pools', 'pools'],
          ['host', 'Hosts', 'hosts'],
        ] as const) {
          const groups = await stats(page, name, key);
          const total = groups.reduce((sum, g) => sum + g.count, 0);
          const section = page.getByRole('region', { name: region, exact: true });
          await expect(section.getByRole('listitem'), `${name} ${region}`).toHaveCount(
            groups.length,
          );
          for (const g of groups) {
            const id = g.keys[key] ?? '';
            const label = id === 'unknown' ? 'Not recorded' : (known[key].get(id) ?? id);
            const item = section.getByRole('listitem').filter({ hasText: label });
            const jobs = g.count === 1 ? '1 job' : `${g.count} jobs`;
            await expect(item, `${name} ${label}`).toContainText(
              `${jobs}, ${Math.round((g.count / total) * 100)}%`,
            );
            if (id === 'unknown') {
              unrecorded += 1;
              // There is no page for a host nobody recorded.
              await expect(item.getByRole('link')).toHaveCount(0);
            } else {
              await expect(item.getByRole('link', { name: label, exact: true })).toHaveAttribute(
                'href',
                `/${path}/${id}`,
              );
            }
          }
        }
      }
      expect(unrecorded, 'the demo fleet has jobs whose host was never recorded').toBeGreaterThan(
        0,
      );
    });

    test('jobs waiting for a label nobody serves are counted, and the link opens them in the queue', async ({
      page,
    }) => {
      let waiting: { name: string; id: string; total: number } | undefined;
      let idle: { name: string; id: string } | undefined;
      for (const name of FIXTURE.repos) {
        const res = await page.request.get(
          `/api/v1/jobs?repo=${encodeURIComponent(name)}&unmatched=true&limit=1`,
        );
        const { total } = (await res.json()) as { total: number };
        const found = { name, id: (await repository(page, name)).id };
        if (total > 0) waiting ??= { ...found, total };
        else idle ??= found;
      }
      expect(waiting, 'the demo fleet queues a job no pool claims').toBeTruthy();
      expect(idle, 'and a repository with none').toBeTruthy();

      await goto(page, `/kennel/repositories/${idle!.id}`, idle!.name);
      await expect(page.getByText('Nothing is waiting for a label no pool serves.')).toBeVisible();
      await expect(page.getByRole('link', { name: 'See them in the queue' })).toHaveCount(0);

      await goto(page, `/kennel/repositories/${waiting!.id}`, waiting!.name);
      const sentence =
        waiting!.total === 1 ? '1 job is waiting' : `${waiting!.total} jobs are waiting`;
      await expect(page.getByText(`${sentence} for a label no pool serves.`)).toBeVisible();
      await page.getByRole('link', { name: 'See them in the queue' }).click();
      await expect(page).toHaveURL(
        new RegExp(`/queue\\?repo=${encodeURIComponent(waiting!.name)}&unmatched=true$`),
      );
      await expect(page.getByRole('heading', { level: 1, name: 'Queue' })).toBeVisible();
    });

    test('each section has an address, back returns to the one before, and an unknown one opens the Overview', async ({
      page,
    }) => {
      const row = await repository(page, PUBLIC_REPO);
      const base = `/kennel/repositories/${row.id}`;
      const selected = (label: string) =>
        expect(page.getByRole('tab', { name: label, exact: true })).toHaveAttribute(
          'aria-selected',
          'true',
        );

      await goto(page, base, PUBLIC_REPO);
      await plantMarker(page);
      // What Kennel Club says is one link away from what the fleet knows.
      await page.getByRole('link', { name: 'Open the CI tab' }).click();
      await expect(page).toHaveURL(new RegExp(`${base}/ci$`));
      await selected('CI');
      await page.goBack();
      await expect(page).toHaveURL(new RegExp(`${base}$`));
      await page.getByRole('tab', { name: 'CI', exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`${base}/ci$`));
      await selected('CI');
      await expect(page.getByRole('article')).toHaveCount(2);

      await page.goBack();
      await expect(page).toHaveURL(new RegExp(`${base}$`));
      await selected('Overview');
      await expect(page.getByRole('article')).toHaveCount(0);
      // Moving between sections is the page's own business, never a reload.
      await expectNoReload(page);

      // An address somebody was sent opens on the section it names.
      await goto(page, `${base}/ci`, PUBLIC_REPO);
      await selected('CI');

      // One that names no section is replaced, not left to show nothing.
      await goto(page, `${base}/nonsense`, PUBLIC_REPO);
      await expect(page).toHaveURL(new RegExp(`${base}$`));
      await selected('Overview');
    });
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

    const full = (f: Finding) => ({ ...f, detail: 'Detail.', fix: 'Fix.', evidence: [] });

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
        await goto(page, `/kennel/repositories/${id}/ci`, PUBLIC_REPO);
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
        await goto(page, `/kennel/repositories/${id}/ci`, PUBLIC_REPO);
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
      await goto(page, `/kennel/repositories/${id}/ci`, PUBLIC_REPO);
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
      await goto(page, `/kennel/repositories/${id}/ci`, PUBLIC_REPO);
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
      await goto(page, `/kennel/repositories/${id}/ci`, PUBLIC_REPO);
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

    test('the request names the finding exactly, and the page shows what the controller answered', async ({
      page,
    }) => {
      const { id } = await target(page);
      const real = await detail(page, id);
      const finding: Finding = {
        code: 'capacity.job_hit_default_limit',
        severity: 'warning',
        subject: 'a1b2c3d4',
        title: 'A job ran until GitHub stopped it at six hours',
      };
      const waiverId = 'kcw_answered1';
      const answer = (waived: boolean, reason = '') => ({
        ...real,
        findings: [...real.findings, ...(waived ? [] : [full(finding)])],
        waived: waived
          ? [
              {
                finding: full(finding),
                waiver: {
                  id: waiverId,
                  code: finding.code,
                  subject: finding.subject,
                  severity: finding.severity,
                  reason,
                  by: 'the controller',
                  at: new Date().toISOString(),
                  expires_at: new Date(Date.now() + 90 * 86_400_000).toISOString(),
                },
              },
            ]
          : [],
      });
      // Nothing here reaches the controller, so no frame of the stream can say
      // what changed: the page has the controller's answer to the request, or it
      // has nothing.
      let current = answer(false);
      await page.route(`**/api/v1/kennel/repositories/${id}`, (route) =>
        route.fulfill({ json: current }),
      );
      let sent: Record<string, string> | undefined;
      await page.route(`**/api/v1/kennel/repositories/${id}/waivers`, (route) => {
        sent = route.request().postDataJSON() as Record<string, string>;
        current = answer(true, sent.reason);
        return route.fulfill({ json: current });
      });
      let ended = '';
      await page.route(`**/api/v1/kennel/repositories/${id}/waivers/*`, (route) => {
        ended = route.request().url();
        current = answer(false);
        return route.fulfill({ json: current });
      });

      await goto(page, `/kennel/repositories/${id}/ci`, PUBLIC_REPO);
      await page.getByRole('button', { name: `Waive: ${finding.title}` }).click();
      const dialog = page.getByRole('dialog', { name: 'Waive this finding' });
      await dialog
        .getByRole('textbox', { name: /Why this is acceptable here/ })
        .fill('   Padded, and long enough to be a reason.   ');
      const asked = Date.now();
      await dialog.getByRole('button', { name: 'Waive', exact: true }).click();

      await expect(page.locator('details.waived')).toContainText(
        'Padded, and long enough to be a reason.',
      );
      await expect(article(page, finding)).toHaveCount(0);
      expect(sent, 'the request was made').toBeTruthy();
      expect(sent!.code).toBe(finding.code);
      // The subject is what tells two findings of one code apart, and what the
      // waiver is about: a waiver sent without it would cover the wrong one.
      expect(sent!.subject).toBe(finding.subject);
      expect(sent!.reason, 'the ends of the reason are trimmed').toBe(
        'Padded, and long enough to be a reason.',
      );
      // Left alone, the length is a quarter.
      const days = (Date.parse(sent!.expires_at!) - asked) / 86_400_000;
      expect(days).toBeGreaterThan(89.9);
      expect(days).toBeLessThan(90.1);

      await page.getByRole('button', { name: `End the waiver: ${finding.title}` }).click();
      await page
        .getByRole('dialog', { name: 'End waiver' })
        .getByRole('button', { name: 'End waiver', exact: true })
        .click();
      await expect(article(page, finding)).toBeVisible();
      await expect(page.locator('details.waived')).toHaveCount(0);
      expect(ended, 'the waiver is named by its own ID').toMatch(
        new RegExp(`/waivers/${waiverId}$`),
      );
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
        await goto(page, `/kennel/repositories/${id}/ci`, PUBLIC_REPO);
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
      await goto(page, `/kennel/repositories/${id}/ci`, PUBLIC_REPO);
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

        await goto(page, `/kennel/repositories/${id}/ci`, PUBLIC_REPO);
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

/* -- the side menu and the switch ---------------------------------------------- */

// Kennel Club's own navigation, and the one place its on/off setting is changed
// from. Every test here starts with it off and puts it back off: the suite shares
// one controller, and a problem Kennel Club raises moves counts other specs read.
test.describe('the side menu and the switch', () => {
  const rail = (page: Page) => page.getByRole('navigation', { name: 'Kennel Club' });
  const kennelSwitch = (page: Page) =>
    page.getByRole('switch', { name: 'Check repository standards' });
  const toast = (page: Page, tone: 'success' | 'error', text: string) =>
    page.locator(`.toast[data-tone="${tone}"]`).filter({ hasText: text });
  const on = { 'kennel.enabled': true };
  const off = { 'kennel.enabled': false };

  // The fixture controller has authentication off, so everybody there is an
  // administrator. The page is told who it is talking to, and asks no different
  // questions of the controller.
  async function pretendToBe(page: Page, role: 'viewer' | 'operator'): Promise<void> {
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

  test.beforeEach(async ({ page }) => patchSettings(page, off));
  test.afterEach(async ({ page }) => patchSettings(page, off));

  test('every page of the section has the rail, with the page on screen marked', async ({
    page,
  }) => {
    await patchSettings(page, on);
    await untilRead(page);
    const row = await repository(page, PUBLIC_REPO);
    for (const [path, heading, here] of [
      ['/kennel', 'Kennel Club', 'Overview'],
      ['/kennel/repositories', 'Repositories', 'Repositories'],
      [`/kennel/repositories/${row.id}`, PUBLIC_REPO, 'Repositories'],
      [`/kennel/repositories/${row.id}/ci`, PUBLIC_REPO, 'Repositories'],
      ['/kennel/ai-context', 'AI Context', 'AI Context'],
      // The old address renders the same page, rail included.
      ['/ai-context', 'AI Context', 'AI Context'],
    ] as const) {
      await goto(page, path, heading);
      await expect(rail(page).getByRole('link'), path).toHaveText([
        'Overview',
        'Repositories',
        'AI Context',
      ]);
      await expect(rail(page).locator('[aria-current="page"]'), path).toHaveCount(1);
      await expect(rail(page).getByRole('link', { name: here, exact: true }), path).toHaveAttribute(
        'aria-current',
        'page',
      );
    }
  });

  test('the switch heads the group it governs, and AI Context is apart from it', async ({
    page,
  }) => {
    await goto(page, '/kennel', 'Kennel Club');
    const standards = rail(page).getByRole('list', { name: 'Standards' });
    const assistants = rail(page).getByRole('list', { name: 'For assistants' });
    await expect(standards.getByRole('link')).toHaveText(['Overview', 'Repositories']);
    await expect(assistants.getByRole('link')).toHaveText(['AI Context']);
    await expect(kennelSwitch(page)).toBeVisible();
    // The switch is above what it governs; AI Context is not under it, because it
    // keeps working with Kennel Club off.
    const [toggleBox, standardsBox, assistantsBox] = await Promise.all([
      kennelSwitch(page).boundingBox(),
      standards.boundingBox(),
      assistants.boundingBox(),
    ]);
    expect(toggleBox!.y).toBeLessThan(standardsBox!.y);
    expect(standardsBox!.y).toBeLessThanOrEqual(assistantsBox!.y);
  });

  test('the rail takes you between the pages', async ({ page }) => {
    await patchSettings(page, on);
    await untilRead(page);
    await goto(page, '/kennel', 'Kennel Club');
    await rail(page).getByRole('link', { name: 'Repositories', exact: true }).click();
    await expect(page).toHaveURL(/\/kennel\/repositories$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Repositories' })).toBeVisible();
    await rail(page).getByRole('link', { name: 'AI Context', exact: true }).click();
    await expect(page).toHaveURL(/\/kennel\/ai-context$/);
    await expect(page.getByRole('heading', { level: 1, name: 'AI Context' })).toBeVisible();
    await rail(page).getByRole('link', { name: 'Overview', exact: true }).click();
    await expect(page).toHaveURL(/\/kennel$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Kennel Club' })).toBeVisible();
  });

  test('AI Context is in the rail, and works, while Kennel Club is off', async ({ page }) => {
    await goto(page, '/kennel', 'Kennel Club');
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'false');
    await rail(page).getByRole('link', { name: 'AI Context', exact: true }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'AI Context' })).toBeVisible();
    // Its page is not a page about being off, and the rail still says what the switch is.
    await expect(page.getByText('Kennel Club is off', { exact: true })).toHaveCount(0);
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'false');
  });

  test('the setup wizard has no rail: it is a task with a way back and not a place', async ({
    page,
  }) => {
    await goto(page, '/kennel/ai-context/setup', 'Enable repositories');
    await expect(rail(page)).toHaveCount(0);
  });

  test('an administrator turns it on and off from the rail, and off is asked about first', async ({
    page,
  }) => {
    const changes: unknown[] = [];
    page.on('request', (request) => {
      if (request.method() === 'PATCH' && request.url().endsWith('/api/v1/settings'))
        changes.push(request.postDataJSON());
    });
    await goto(page, '/kennel', 'Kennel Club');
    const toggle = kennelSwitch(page);
    await expect(toggle).toHaveAttribute('aria-checked', 'false');
    await expect(rail(page).getByText('Kennel Club is off and reads nothing.')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Kennel Club is off' })).toBeVisible();

    await toggle.click();
    await expect(toast(page, 'success', 'Kennel Club is on')).toBeVisible();
    await expect(toggle).toHaveAttribute('aria-checked', 'true');
    await expect(rail(page).getByText('Kennel Club is on and reading from GitHub.')).toBeVisible();
    expect((await overview(page)).enabled).toBe(true);
    expect(changes).toEqual([on]);
    await expect(page.getByRole('heading', { name: 'Kennel Club is off' })).toHaveCount(0);

    // Off takes the checking away from everybody, so it says what that does first.
    // Cancelling leaves it on and sends nothing.
    await toggle.click();
    const confirm = page.getByRole('dialog', { name: 'Turn off Kennel Club' });
    await expect(confirm).toBeVisible();
    await expect(confirm).toContainText('It stops reading from GitHub.');
    await expect(confirm).toContainText('What it found is kept, and shown again');
    await expect(confirm).toContainText('AI Context is not affected.');
    // Still on while it is being asked about.
    await expect(toggle).toHaveAttribute('aria-checked', 'true');
    await confirm.getByRole('button', { name: 'Cancel' }).click();
    await expect(confirm).toHaveCount(0);
    expect(changes).toHaveLength(1);
    expect((await overview(page)).enabled).toBe(true);

    await toggle.click();
    await confirm.getByRole('button', { name: 'Turn off', exact: true }).click();
    await expect(toast(page, 'success', 'Kennel Club is off')).toBeVisible();
    await expect(toggle).toHaveAttribute('aria-checked', 'false');
    expect(changes).toEqual([on, off]);
    expect((await overview(page)).enabled).toBe(false);
    await expect(page.getByRole('heading', { name: 'Kennel Club is off' })).toBeVisible();
  });

  test('the switch works from the keyboard', async ({ page, isMobile }) => {
    test.skip(isMobile, 'a phone has no keyboard');
    await goto(page, '/kennel', 'Kennel Club');
    await kennelSwitch(page).focus();
    await page.keyboard.press('Space');
    await expect(toast(page, 'success', 'Kennel Club is on')).toBeVisible();
    expect((await overview(page)).enabled).toBe(true);
  });

  test('the page that says it is off turns it on, and the list fills in', async ({ page }) => {
    await goto(page, '/kennel/repositories', 'Repositories');
    await expect(page.getByText('Kennel Club is off', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Turn on Kennel Club' }).click();
    await expect(toast(page, 'success', 'Kennel Club is on')).toBeVisible();
    await untilRead(page);
    await expect(page.getByRole('link', { name: PUBLIC_REPO })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText('Kennel Club is off', { exact: true })).toHaveCount(0);
  });

  // The controller sends the summary when it differs from the one it last sent,
  // and sends nothing while nobody is connected. So the page is opened first, and
  // each change is waited for in turn: a flip and a flip back between two passes
  // would, correctly, be no change at all to a controller that had sent nothing.
  test('a change made elsewhere is followed without a reload', async ({ page }) => {
    await goto(page, '/kennel/repositories', 'Repositories');
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'false');
    await expect(page.getByText('Kennel Club is off', { exact: true })).toBeVisible();
    await plantMarker(page);

    await patchSettings(page, on);
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'true', { timeout: 20_000 });
    await untilRead(page);
    await expect(page.getByRole('link', { name: PUBLIC_REPO })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText('Kennel Club is off', { exact: true })).toHaveCount(0);

    await patchSettings(page, off);
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'false', { timeout: 20_000 });
    await expect(page.getByText('Kennel Club is off', { exact: true })).toBeVisible();
    await expectNoReload(page);
  });

  // The stream opens after the page has loaded, so a change can land between the
  // page's first read and the stream coming up, and no frame is ever replayed for
  // it. The switch asks again when the stream arrives, as the pages beside it do.
  test('a change that landed before the stream was up is caught when it arrives', async ({
    page,
  }) => {
    await patchSettings(page, on);
    await untilRead(page);
    let cut = true;
    await page.route('**/api/v1/events*', (route) =>
      cut
        ? route.abort('connectionfailed')
        : route.fulfill({
            status: 200,
            headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
            body: ': up\n\n',
          }),
    );
    await goto(page, '/kennel/repositories', 'Repositories');
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'true');

    // Nothing is listening, so nothing says so.
    await patchSettings(page, off);
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'true');

    cut = false;
    await page.evaluate(() => window.dispatchEvent(new Event('online')));
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'false', { timeout: 20_000 });
    await expect(page.getByText('Kennel Club is off', { exact: true })).toBeVisible();
  });

  for (const role of ['viewer', 'operator'] as const) {
    test(`a ${role} sees the state and who can change it, and cannot press it`, async ({
      page,
    }) => {
      await pretendToBe(page, role);
      let changed = 0;
      page.on('request', (request) => {
        if (request.method() === 'PATCH' && request.url().endsWith('/api/v1/settings'))
          changed += 1;
      });

      await goto(page, '/kennel', 'Kennel Club');
      await expect(kennelSwitch(page)).toBeDisabled();
      await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'false');
      await expect(
        rail(page).getByText(
          'Kennel Club is off and reads nothing. An administrator can change that.',
        ),
      ).toBeVisible();
      // No button to press, and the page says who can instead.
      await expect(page.getByRole('button', { name: 'Turn on Kennel Club' })).toHaveCount(0);
      await expect(
        page.getByText('An administrator can turn it on, with the switch beside this page.'),
      ).toBeVisible();
      // The list says the same, and offers nothing either.
      await goto(page, '/kennel/repositories', 'Repositories');
      await expect(page.getByRole('button', { name: 'Turn on Kennel Club' })).toHaveCount(0);
      await expect(
        page.getByText('An administrator can turn it on, with the switch beside this page.'),
      ).toBeVisible();

      await patchSettings(page, on);
      await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'true', { timeout: 20_000 });
      await expect(
        rail(page).getByText(
          'Kennel Club is on and reading from GitHub. An administrator can change that.',
        ),
      ).toBeVisible();
      await expect(kennelSwitch(page)).toBeDisabled();
      expect(changed, 'nothing was sent from the page').toBe(0);
    });
  }

  test('a change the controller refuses is said beside the switch, and the switch goes back', async ({
    page,
  }) => {
    await page.route('**/api/v1/settings', (route) =>
      route.request().method() === 'PATCH'
        ? route.fulfill({
            status: 403,
            contentType: 'application/json',
            body: JSON.stringify({
              error: { code: 'forbidden', message: 'That setting needs the platform role.' },
            }),
          })
        : route.fallback(),
    );
    await goto(page, '/kennel', 'Kennel Club');
    await kennelSwitch(page).click();
    await expect(rail(page).getByRole('alert')).toContainText(
      'That setting needs the platform role.',
    );
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'false');
    await expect(toast(page, 'success', 'Kennel Club is on')).toHaveCount(0);
    expect((await overview(page)).enabled).toBe(false);
  });

  test('a change the environment overrules is not shown as made, and says why', async ({
    page,
  }) => {
    // The database accepts it and the environment has the last word: the answer
    // carries the setting as it stands, which is still off.
    await page.route('**/api/v1/settings', (route) =>
      route.request().method() === 'PATCH'
        ? route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({
              settings: [
                {
                  key: 'kennel.enabled',
                  value: false,
                  source: 'environment',
                  env: 'ZOOMIES_KENNEL_ENABLED',
                  pending: false,
                },
              ],
            }),
          })
        : route.fallback(),
    );
    await goto(page, '/kennel', 'Kennel Club');
    await kennelSwitch(page).click();
    await expect(rail(page).getByRole('alert')).toContainText('ZOOMIES_KENNEL_ENABLED is set');
    await expect(rail(page).getByRole('alert')).toContainText('stays off');
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'false');
    await expect(toast(page, 'success', 'Kennel Club is on')).toHaveCount(0);
  });

  // The stream is cut for the whole of these. A change made from the rail is
  // answered by the request that made it, and every page of the section follows
  // that answer, so none of them waits for a frame that a dropped stream cannot
  // send. The pages that did wait would pass the tests above, which have a
  // stream, and be wrong exactly when someone's connection is the problem.
  test.describe('with no stream to say so', () => {
    const pages = [
      {
        name: 'the overview',
        path: () => '/kennel',
        heading: 'Kennel Club',
        offHeading: undefined,
        saysOff: (page: Page) => page.getByRole('heading', { name: 'Kennel Club is off' }),
      },
      {
        name: 'the list of repositories',
        path: () => '/kennel/repositories',
        heading: 'Repositories',
        offHeading: undefined,
        saysOff: (page: Page) => page.locator('#main p.title', { hasText: 'Kennel Club is off' }),
      },
      {
        name: 'a repository',
        path: (id: string) => `/kennel/repositories/${id}`,
        heading: PUBLIC_REPO,
        // Arriving while it is off, the page does not know the name yet.
        offHeading: 'Repository',
        saysOff: (page: Page) => page.getByText('That repository could not be read'),
      },
    ];

    for (const where of pages) {
      test(`${where.name} follows the switch`, async ({ page }) => {
        await patchSettings(page, on);
        await untilRead(page);
        const row = await repository(page, PUBLIC_REPO);
        await patchSettings(page, off);
        await page.route('**/api/v1/events*', (route) => route.abort('connectionfailed'));

        await goto(page, where.path(row.id), where.offHeading ?? where.heading);
        await expect(where.saysOff(page)).toBeVisible();
        await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'false');

        await kennelSwitch(page).click();
        await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'true');
        await expect(where.saysOff(page)).toHaveCount(0);
        await expect(page.getByRole('heading', { level: 1, name: where.heading })).toBeVisible();

        await kennelSwitch(page).click();
        await page
          .getByRole('dialog', { name: 'Turn off Kennel Club' })
          .getByRole('button', { name: 'Turn off', exact: true })
          .click();
        await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'false');
        await expect(where.saysOff(page)).toBeVisible();
      });
    }
  });

  // A resync is the controller saying it could not replay what this tab missed. It
  // arrives on a stream that never dropped, so the switch has no reconnect to
  // notice, and what it holds may be wrong: it asks again. The frame is delivered
  // by hand to the open stream, and the answer the controller gives is changed
  // underneath, which is the only way to tell asking again from not asking.
  test('a stream that lost its place makes the switch ask again', async ({ page }) => {
    await patchSettings(page, on);
    await untilRead(page);
    await page.addInitScript(() => {
      const Real = window.EventSource;
      const streams: EventSource[] = [];
      (window as unknown as { __streams: EventSource[] }).__streams = streams;
      window.EventSource = class extends Real {
        constructor(url: string | URL, init?: EventSourceInit) {
          super(url, init);
          streams.push(this);
        }
      };
    });
    await goto(page, '/kennel/repositories', 'Repositories');
    await expect(page.locator('.connection')).toHaveAttribute('data-state', 'live');
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'true');

    await page.route('**/api/v1/kennel', async (route) => {
      const response = await route.fetch();
      const body = (await response.json()) as Record<string, unknown>;
      return route.fulfill({ response, json: { ...body, enabled: false } });
    });
    await page.evaluate(() => {
      for (const stream of (window as unknown as { __streams: EventSource[] }).__streams)
        stream.dispatchEvent(new MessageEvent('resync', { data: '{"reason":"test"}' }));
    });
    await expect(kennelSwitch(page)).toHaveAttribute('aria-checked', 'false', { timeout: 10_000 });
  });

  // The longest thing the switch says is what it says to somebody who cannot press
  // it. At the narrowest phone it is wider than the screen unless the switch has
  // the whole row to wrap in, which is why the switch is given a line of its own.
  test('on a phone the longest sentence under the switch wraps and is all in view', async ({
    page,
    isMobile,
  }) => {
    test.skip(!isMobile, 'the phone project checks the narrow widths');
    await pretendToBe(page, 'viewer');
    for (const width of [320, 360, 412]) {
      await page.setViewportSize({ width, height: 780 });
      await goto(page, '/kennel', 'Kennel Club');
      await expect(
        rail(page).getByText(
          'Kennel Club is off and reads nothing. An administrator can change that.',
        ),
        `the sentence at ${width}px`,
      ).toBeInViewport({ ratio: 1 });
      await expect(kennelSwitch(page)).toBeInViewport({ ratio: 1 });
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
        `the page does not scroll sideways at ${width}px`,
      ).toBe(true);
    }
  });

  test('on a phone every page of the section is in view, AI Context included, at a size a finger can use', async ({
    page,
    isMobile,
  }) => {
    test.skip(!isMobile, 'the phone project checks the narrow widths');
    for (const width of [360, 412]) {
      await page.setViewportSize({ width, height: 780 });
      for (const [path, heading] of [
        ['/kennel', 'Kennel Club'],
        ['/kennel/ai-context', 'AI Context'],
      ] as const) {
        await goto(page, path, heading);
        for (const name of ['Overview', 'Repositories', 'AI Context']) {
          const link = rail(page).getByRole('link', { name, exact: true });
          const where = `${name} on ${path} at ${width}px`;
          // Fully on screen: a strip that scrolls sideways hides the last one.
          await expect(link, where).toBeInViewport({ ratio: 1 });
          expect(
            (await link.boundingBox())!.height,
            `${where} is a finger-sized target`,
          ).toBeGreaterThanOrEqual(44);
        }
        await expect(kennelSwitch(page)).toBeInViewport({ ratio: 1 });
        await auditThePage(page, `${path} at ${width}px`);
      }
    }
  });
});

/* -- the cards on the Overview ------------------------------------------------- */

// A card opens the repositories it counts. The number on it and the rows behind it
// have to agree, or the click shows the card to be wrong; and a card that counts
// nothing has no link, since it would open an empty list.
test.describe('the cards on the Overview', () => {
  const card = (page: Page, label: string) =>
    page.getByRole('link', { name: new RegExp(`^(?:${label}): `) });
  const off = { 'kennel.enabled': false };

  test.beforeEach(async ({ page }) => {
    await patchSettings(page, { 'kennel.enabled': true });
    await untilRead(page);
  });
  test.afterEach(async ({ page }) => patchSettings(page, off));

  test('a card opens the repositories it counts, and the rows are as many as the number', async ({
    page,
  }) => {
    const counted = await overview(page);
    expect(counted.states.attention, 'some repository needs attention').toBeGreaterThan(0);
    expect(counted.states.best_in_show, 'some repository has nothing open').toBeGreaterThan(0);
    expect(counted.counts.error, 'some repository has an error open').toBeGreaterThan(0);

    const rows = dataRows(grid(page, 'Repositories'));
    for (const [label, href, expected] of [
      ['Repositories', '/kennel/repositories', counted.repositories],
      // Named for the standing, which the preference for playful wording changes.
      [
        'No open findings|Best in show',
        '/kennel/repositories?state=best_in_show',
        counted.states.best_in_show,
      ],
      ['Need attention', '/kennel/repositories?state=attention', counted.states.attention],
    ] as const) {
      await goto(page, '/kennel', 'Kennel Club');
      await expect(card(page, label), label).toHaveAttribute('href', href);
      await card(page, label).click();
      await expect(page, label).toHaveURL(new RegExp(`${href.replace('?', '\\?')}$`));
      await expect(page.getByRole('heading', { level: 1, name: 'Repositories' })).toBeVisible();
      await expect(rows, `${label} opens as many rows as its number`).toHaveCount(expected);
    }

    // Errors counts findings and the list shows the repositories that have them, so
    // the rows are the ones with an error open and not as many as the number.
    await goto(page, '/kennel', 'Kennel Club');
    await card(page, 'Errors').click();
    await expect(page).toHaveURL(/\/kennel\/repositories\?severity=error$/);
    await expect(rows.first()).toContainText(PUBLIC_REPO);
    await expect(page.getByRole('button', { name: /Remove.*Severity/i })).toBeVisible();

    // The panel under the cards opens the same list as its card.
    await goto(page, '/kennel', 'Kennel Club');
    await page.getByRole('link', { name: 'See all', exact: true }).click();
    await expect(page).toHaveURL(/\/kennel\/repositories\?state=attention$/);
    await expect(rows, 'See all opens as many rows as Need attention counts').toHaveCount(
      counted.states.attention,
    );
  });

  test('a card is judged by its own count, and two cards have no link to give', async ({
    page,
  }) => {
    // The stream is cut so that only this document is ever on the page: a frame
    // of the real summary would replace it, and a "has no link" check made after
    // that would be about the real fleet and not about the card.
    await page.route('**/api/v1/events*', (route) => route.abort('connectionfailed'));

    // One count at a time is something, and every other is nothing, so a card
    // judged by another card's count, or by none, is the one that shows.
    const counted = [
      ['No open findings|Best in show', '/kennel/repositories?state=best_in_show'],
      ['Need attention', '/kennel/repositories?state=attention'],
      ['Errors', '/kennel/repositories?severity=error'],
      ['Warnings', '/kennel/repositories?severity=warning'],
    ] as const;
    let alone = 0;
    await page.route('**/api/v1/kennel', async (route) => {
      const response = await route.fetch();
      const body = (await response.json()) as Overview;
      const some = (n: number) => (alone === n ? 3 : 0);
      return route.fulfill({
        response,
        json: {
          ...body,
          // "Partly checked" and "Waived" always have something to count.
          states: {
            ...body.states,
            best_in_show: some(0),
            attention: some(1),
            partial: 1,
            pending: 1,
          },
          counts: { ...body.counts, error: some(2), warning: some(3), waived: 1 },
        },
      });
    });

    for (const [only, [alonelabel]] of counted.entries()) {
      alone = only;
      await goto(page, '/kennel', 'Kennel Club');
      // The card that always links, which is also how the page is known to be read.
      await expect(card(page, 'Repositories')).toHaveAttribute('href', '/kennel/repositories');
      for (const [n, [label, href]] of counted.entries()) {
        const where = `${label}, when only ${alonelabel} counts something`;
        if (n === only) await expect(card(page, label), where).toHaveAttribute('href', href);
        else await expect(card(page, label), `${where}: no link`).toHaveCount(0);
      }
      // These two have no list to open whatever they count. "Partly checked" adds
      // two standings and the list filters by one; the list has no waiver filter.
      for (const [label, value] of [
        ['Partly checked', '2'],
        ['Waived', '1'],
      ] as const) {
        await expect(tile(page, label), `${label} is on the page`).toBeVisible();
        await expect(tile(page, label).locator('a'), `${label} is not a link`).toHaveCount(0);
        await expect(tile(page, label), `${label} says ${value}`).toContainText(value);
      }
    }
  });

  // The whole card is the target, not the small label in it, and it is tall enough
  // for a finger: a phone is where the cards are tapped.
  test('the whole card is the link, and it is a size a finger can use', async ({ page }) => {
    await goto(page, '/kennel', 'Kennel Club');
    const link = card(page, 'Need attention');
    const box = page.locator('.metric').filter({ has: link });
    const size = (await box.boundingBox())!;
    expect(size.height, 'the card is a finger-sized target').toBeGreaterThanOrEqual(44);
    await expect(box).toBeInViewport({ ratio: 1 });
    // Its far corner is nowhere near the label, and still opens the list.
    await box.click({ position: { x: size.width - 6, y: size.height - 6 } });
    await expect(page).toHaveURL(/\/kennel\/repositories\?state=attention$/);
    await auditThePage(page, 'the list from a card');
  });
});
