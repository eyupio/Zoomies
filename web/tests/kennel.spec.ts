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
    await expect(tile('Need attention')).toContainText('1');
    await expect(tile('Errors')).toContainText('2');

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
    await expect(rows).toHaveCount(2);
    await expect(rows.filter({ hasText: PUBLIC_REPO })).toHaveCount(0);
    // With plain status words, which is what a browser that has not chosen sees,
    // best in show says what it means.
    await expect(rows.filter({ hasText: 'acme/api' })).toContainText('No open findings');

    await goto(page, '/kennel/repositories?code=exposure.fork_code_ran', 'Repositories');
    await expect(page.getByText('No repositories match those filters')).toBeVisible();
    await page.getByRole('button', { name: 'Clear filters' }).click();
    await expect(rows).toHaveCount(3);
    await auditThePage(page, 'the list');
  });

  test('a repository that was not looked at yet is not an all clear, and one that was read in full is', async ({
    page,
  }) => {
    const quiet = await repository(page, 'acme/api');
    await goto(page, `/kennel/repositories/${quiet.id}`, 'acme/api');
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

  test('a repository the stream changes is repainted in place', async ({ page }) => {
    const row = await repository(page, PUBLIC_REPO);
    await goto(page, `/kennel/repositories/${row.id}`, PUBLIC_REPO);
    await expect(page.getByRole('article')).toHaveCount(2);

    // The frame is the repository's own GET shape, with one finding gone, which
    // is what the stream sends when a pool is fixed.
    const current = (await page.request
      .get(`/api/v1/kennel/repositories/${row.id}`)
      .then((r) => r.json())) as { findings: unknown[]; counts: Record<string, number> };
    const next = {
      ...current,
      findings: current.findings.slice(0, 1),
      counts: { ...current.counts, error: 1 },
    };
    const frame = `id: 999990\nevent: kennel.updated\ndata: ${JSON.stringify(next)}\n\n`;
    await page.route('**/api/v1/events*', (route) =>
      route.fulfill({
        status: 200,
        headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
        body: frame,
      }),
    );
    await plantMarker(page);
    await page.evaluate(() => {
      window.dispatchEvent(new Event('offline'));
      window.dispatchEvent(new Event('online'));
    });
    await expect(page.getByRole('article')).toHaveCount(1, { timeout: 10_000 });
    await expect(page.getByText('1 error', { exact: true })).toBeVisible();
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
});
