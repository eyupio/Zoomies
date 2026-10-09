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
  rowCount,
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
  not_tracked: number;
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
    // The page says what is checked from the registry the evaluator runs, and this
    // route serves that same registry while Kennel Club is off. So the table is held
    // to the route, one row and one code for each entry, and not to a number that
    // every check added to the registry would have to remember to change: a count
    // written here is what failed the first time the registry grew.
    const registry = (await page.request.get('/api/v1/kennel/checks').then((r) => r.json())) as {
      items: { code: string }[];
    };
    // The body's rows: on a phone the header row is hidden and each row is a card.
    await expect(checks.locator('tbody tr')).toHaveCount(registry.items.length);
    for (const { code } of registry.items) {
      await expect(checks.getByText(code, { exact: true }), code).toBeVisible();
    }
    // The route cannot make this pass by serving less. These are the checks the
    // documentation and the screenshots name, and they stay in the registry.
    const codes = registry.items.map((entry) => entry.code);
    for (const code of [
      'exposure.public_repo_on_fleet',
      'exposure.public_repo_weak_pool',
      'exposure.fork_code_ran',
      'exposure.target_event_ran',
      'capacity.unserved_label',
      'capacity.job_hit_default_limit',
    ]) {
      expect(codes, code).toContain(code);
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

  test('a finding offers a prompt for a coding agent, and copying it copies what the API carries', async ({
    page,
  }) => {
    // A headless browser will not let a test read the clipboard, so the page's
    // one write to it is recorded instead.
    await page.addInitScript(() => {
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: {
          writeText: async (value: string) => sessionStorage.setItem('copied', value),
        },
      });
    });
    const row = await repository(page, PUBLIC_REPO);
    const doc = (await page.request
      .get(`/api/v1/kennel/repositories/${row.id}`)
      .then((r) => r.json())) as { findings: { code: string; subject: string; prompt: string }[] };
    await goto(page, `/kennel/repositories/${row.id}/ci`, PUBLIC_REPO);
    expect(doc.findings.length).toBeGreaterThan(0);
    const first = page.getByRole('article').first();
    await first.getByRole('button', { name: 'Copy prompt for your coding agent' }).click();
    const copied = await page.evaluate(() => sessionStorage.getItem('copied'));
    const match = doc.findings.find((f) => f.prompt === copied);
    expect(match, 'the clipboard holds one of the prompts the API sent, unchanged').toBeTruthy();
    expect(copied).toContain('Fix the Kennel Club finding `');
    // A waived finding is not something anyone is asked to fix: no button.
    const firstFinding = doc.findings[0]!;
    const made = await page.request.put(`/api/v1/kennel/repositories/${row.id}/waivers`, {
      data: {
        code: firstFinding.code,
        subject: firstFinding.subject,
        reason: 'Waived under the test, to show a waived finding offers no prompt.',
        expires_at: new Date(Date.now() + 86_400_000).toISOString(),
      },
    });
    expect(made.ok(), 'the waiver was made').toBeTruthy();
    try {
      await page.reload();
      await expect(page.locator('details.waived summary')).toContainText('Waived');
      const buttons = page.getByRole('button', { name: 'Copy prompt for your coding agent' });
      await expect(buttons).toHaveCount(doc.findings.length - 1);
      const waived = page.locator('details.waived');
      await expect(waived.getByRole('button', { name: /Copy prompt/ })).toHaveCount(0);
    } finally {
      const waiver = (
        (await (await page.request.get(`/api/v1/kennel/repositories/${row.id}`)).json()) as {
          waived: { waiver: { id: string } }[];
        }
      ).waived[0];
      if (waiver) {
        await page.request.delete(
          `/api/v1/kennel/repositories/${row.id}/waivers/${waiver.waiver.id}`,
        );
      }
    }
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
    // The list shows the repositories being served unless told otherwise, and says
    // that is what it looked among; asked for every repository, it says that.
    await expect(page.getByText('No active repositories match those filters')).toBeVisible();
    await goto(page, '/kennel/repositories?code=exposure.fork_code_ran&active=all', 'Repositories');
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

  test('a repository Kennel Club lets go says so instead of showing what it had', async ({
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
    await expect(page.getByText('Kennel Club has let this repository go')).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('article')).toHaveCount(0);
  });

  test('a page that lost the stream asks again when it comes back', async ({ page }) => {
    await goto(page, '/kennel', 'Kennel Club');
    await expect(page.locator('.connection')).toHaveAttribute('data-state', 'live');
    // The setup and workflow checks are opt-in, so some rows say "Turned off" before
    // anything is changed here. The test is about one row, the one it turns off.
    const unserved = page
      .getByRole('table', { name: 'Checks and the repositories that have each open' })
      .getByRole('row')
      .filter({ hasText: 'capacity.unserved_label' });
    await expect(unserved).toBeVisible();
    await expect(unserved).not.toContainText('Turned off');

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
      await expect(unserved).toContainText('Turned off', { timeout: 20_000 });
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
    // It counts every repository, so the list it opens shows every one.
    await expect(page).toHaveURL(/\/kennel\/repositories\?severity=error&active=all$/);
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

  test('a workflow finding names its file and line, and an unusual path is said to be one', async ({
    page,
  }) => {
    const row = await repository(page, PUBLIC_REPO);
    const sha = 'a'.repeat(40);
    const odd = 'b'.repeat(40);
    await page.route(`**/api/v1/kennel/repositories/${row.id}`, async (route) => {
      const real = await route.fetch();
      const body = (await real.json()) as Row & { files: Array<{ sha: string; path: string }> };
      body.findings.unshift({
        code: 'ci.no_timeout',
        severity: 'warning',
        subject: sha.slice(0, 12),
        title: 'Jobs have no explicit timeout',
        detail: 'Two executable jobs in this workflow have no timeout-minutes.',
        fix: 'Set timeout-minutes on each.',
        evidence: [
          { kind: 'file', ref: sha, job_index: 0, line: 4 },
          { kind: 'file', ref: odd, job_index: -1, line: 2 },
        ],
      } as unknown as Row['findings'][number]);
      body.files = [
        { sha, path: '.github/workflows/ci.yml' },
        { sha: odd, path: '' },
      ];
      return route.fulfill({ response: real, json: body });
    });
    await goto(page, `/kennel/repositories/${row.id}/ci`, PUBLIC_REPO);
    await expect(
      page.getByText('.github/workflows/ci.yml:4 (job 1)', { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText('a workflow with an unusual name, line 2', { exact: true }),
    ).toBeVisible();
    // A file is text, never a link: there is nothing here it could link to.
    await expect(page.locator('a', { hasText: '.github/workflows/ci.yml' })).toHaveCount(0);
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

    test('each figure opens the jobs it counted, and the list agrees with the figure', async ({
      page,
    }) => {
      const tile = (label: string) => page.locator('.metric').filter({ hasText: label });
      // The demo fleet's pools and hosts all have IDs, so each count is a link.
      const place = async (name: string, by: 'pool' | 'host') => {
        const groups = (await stats(page, name, by)).filter((g) => g.keys[by] !== 'unknown');
        expect(groups.length, `${name} ran jobs on a ${by}`).toBeGreaterThan(0);
        return groups;
      };

      // A link that is not there is not checked, so say how many were.
      const opened = { failed: 0, faulted: 0 };

      for (const name of FIXTURE.repos) {
        const row = await repository(page, name);
        const overview = `/kennel/repositories/${row.id}`;
        const [all] = await stats(page, name);
        expect(all, `${name} ran jobs this week`).toBeTruthy();

        // The link starts the list at the very instant the figure was counted from,
        // not at one worked out again: the demo's jobs are not near enough the edge
        // of the window to tell, so the instant itself is what is compared.
        const counted = page.waitForRequest(
          (r) => r.url().includes('/api/v1/jobs/stats') && !r.url().includes('group_by'),
        );
        await goto(page, overview, name);
        const from = new Date(new URL((await counted).url()).searchParams.get('since')!);
        const pad = (n: number) => String(n).padStart(2, '0');
        const minute = `${from.getFullYear()}-${pad(from.getMonth() + 1)}-${pad(from.getDate())}T${pad(from.getHours())}:${pad(from.getMinutes())}`;
        const finished = tile('Jobs finished').getByRole('link', { name: /^Jobs finished:/ });
        expect(
          new URL((await finished.getAttribute('href'))!, 'http://zoomies.test').searchParams.get(
            'since',
          ),
          name,
        ).toBe(minute);

        // Every finished job the figure counted, and no more.
        await finished.click();
        await expect(rowCount(page), name).toContainText(`of ${all!.count} jobs`);

        // The failures inside that count, and the ones the fleet itself caused.
        for (const [narrow, count, text] of [
          ['failed', all!.failed, `See the ${all!.failed} that failed`],
          ['faulted', all!.fleet_failed, `See the ${all!.fleet_failed} lost to this fleet`],
        ] as const) {
          if (count === 0) continue;
          opened[narrow] += 1;
          await goto(page, overview, name);
          await page.getByRole('link', { name: text }).click();
          await expect(page, `${name} ${narrow}`).toHaveURL(new RegExp(`[?&]${narrow}=true`));
          await expect(rowCount(page), `${name} ${narrow}`).toContainText(`of ${count} jobs`);
        }

        // Each pool's and each host's count opens the jobs that ran there.
        for (const [by, key] of [
          ['pool', 'pool_id'],
          ['host', 'host_id'],
        ] as const) {
          for (const group of await place(name, by)) {
            await goto(page, overview, name);
            await page
              .locator(
                `section[aria-labelledby="${by}s-heading"] a[href*="${key}=${group.keys[by]}"]`,
              )
              .click();
            await expect(rowCount(page), `${name} on ${group.keys[by]}`).toContainText(
              `of ${group.count} jobs`,
            );
          }
        }

        // The timings open the same jobs, slowest first.
        for (const [label, column] of [
          ['Time waiting for a runner', 'Queue wait'],
          ['Time running', 'Duration'],
        ] as const) {
          await goto(page, overview, name);
          await tile(label)
            .getByRole('link', { name: new RegExp(`^${label}:`) })
            .click();
          await expect(rowCount(page), `${name} ${label}`).toContainText(`of ${all!.count} jobs`);
          await expect(
            page.getByRole('columnheader', { name: column }),
            `${name} ${label}`,
          ).toHaveAttribute('aria-sort', 'descending');
        }
      }
      expect(opened.failed, 'the demo has failures to open').toBeGreaterThan(0);
      expect(opened.faulted, 'the demo has failures this fleet caused to open').toBeGreaterThan(0);
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
      // The page reads everything again when the stream first goes live, which
      // closes the gap between its first read and its subscription. On a page that
      // has just loaded that can land a moment after the first read and count the
      // jobs twice, so the stream is refused here: this test is about the page's own
      // requests, and the Refresh button is the only thing that should add any.
      await page.route('**/api/v1/events*', (route) => route.abort());
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

    // The settings checks are an opt-in, off in the fixture controller, so what the
    // tab says there is the sentence that explains it. The states that need a
    // GitHub which answers are put on the page by the API's own shapes.
    test('the Protection tab says the settings checks are off when nothing reads settings', async ({
      page,
    }) => {
      const row = await repository(page, PUBLIC_REPO);
      await goto(page, `/kennel/repositories/${row.id}/protection`, PUBLIC_REPO);
      await expect(page.getByRole('tab', { name: 'Protection', exact: true })).toHaveAttribute(
        'aria-selected',
        'true',
      );
      await expect(page.getByText('Repository settings checks are off.')).toBeVisible();
      await expect(page.getByRole('link', { name: 'Open Settings' })).toBeVisible();
      await expect(page.getByRole('article')).toHaveCount(0);
    });

    test('the Protection tab shows what was read and links each finding to its setting on GitHub', async ({
      page,
    }) => {
      const row = await repository(page, PUBLIC_REPO);
      const current = (await page.request
        .get(`/api/v1/kennel/repositories/${row.id}`)
        .then((r) => r.json())) as { installation_id: string };
      const finding = (code: string, title: string) => ({
        code,
        severity: 'warning',
        subject: '',
        title,
        detail: `${title}.`,
        fix: 'Change the setting.',
        evidence: [],
      });
      await page.route(`**/api/v1/kennel/repositories/${row.id}`, async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as Record<string, unknown> & {
          findings: unknown[];
          coverage: unknown[];
          disabled: string[];
        };
        body.disabled = body.disabled.filter(
          (code) =>
            ![
              'token.default_write',
              'exposure.fork_approval_weak',
              'exposure.private_fork_secrets',
              'protection.required_check_never_reports',
            ].includes(code),
        );
        body.findings = [
          ...body.findings,
          finding('token.default_write', 'The default workflow token can write'),
          finding(
            'protection.required_check_never_reports',
            'Required checks that no job produces',
          ),
        ];
        body.coverage = [
          ...body.coverage,
          {
            source: 'settings',
            label: 'Repository settings',
            state: 'ok',
            reason: 'The settings were read.',
            permission: 'Repository permissions: Administration: Read-only',
          },
          {
            source: 'protection',
            label: 'Required status checks',
            state: 'denied',
            reason: 'GitHub refused the read.',
            permission: 'Repository permissions: Administration: Read-only',
          },
        ];
        return route.fulfill({ response, json: body });
      });
      // Where GitHub's pages are served from is the installation's to say, and an
      // Enterprise host is not github.com.
      await page.route('**/api/v1/installations', (route) =>
        route.fulfill({
          json: { items: [{ id: current.installation_id, web_url: 'https://ghe.example.test/' }] },
        }),
      );
      await goto(page, `/kennel/repositories/${row.id}/protection`, PUBLIC_REPO);

      const sources = page.getByRole('list', { name: 'What was read' });
      await expect(sources).toContainText('Repository settings');
      await expect(sources).toContainText('Required status checks');
      await expect(sources).toContainText('Not granted');
      await expect(sources).toContainText('Administration: Read-only');

      const token = page.getByRole('article', { name: 'The default workflow token can write' });
      await expect(token.getByRole('link', { name: 'Actions settings on GitHub' })).toHaveAttribute(
        'href',
        `https://ghe.example.test/${PUBLIC_REPO}/settings/actions`,
      );
      const required = page.getByRole('article', { name: 'Required checks that no job produces' });
      await expect(
        required.getByRole('link', { name: 'Branch protection on GitHub' }),
      ).toHaveAttribute('href', `https://ghe.example.test/${PUBLIC_REPO}/settings/branches`);
      await expect(required.getByRole('link', { name: 'Rulesets on GitHub' })).toHaveAttribute(
        'href',
        `https://ghe.example.test/${PUBLIC_REPO}/settings/rules`,
      );

      // The same findings are on the CI tab, where they are waived.
      await page.getByRole('tab', { name: 'CI', exact: true }).click();
      await expect(
        page.getByRole('article', { name: 'The default workflow token can write' }),
      ).toBeVisible();
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

  test.describe('telling Kennel Club to stop looking at a repository', () => {
    const REASON = 'A sandbox nobody keeps up, and its jobs are not ours to judge.';
    const trackSwitch = (page: Page) => page.getByRole('switch', { name: 'Track this repository' });
    const stopDialog = (page: Page) =>
      page.getByRole('dialog', { name: 'Stop tracking this repository' });
    const toast = (page: Page, tone: 'success' | 'error', text: string) =>
      page.locator(`.toast[data-tone="${tone}"]`, { hasText: text });

    interface Tracking {
      tracking: { tracked: boolean; reason: string; by: string; since: string | null };
      state: string;
    }
    async function tracking(page: Page, id: string): Promise<Tracking> {
      return (await page.request
        .get(`/api/v1/kennel/repositories/${id}`)
        .then((r) => r.json())) as Tracking;
    }
    async function setTracking(page: Page, id: string, data: Record<string, unknown>) {
      const res = await page.request.put(`/api/v1/kennel/repositories/${id}/tracking`, { data });
      expect(res.ok(), `tracking was set to ${JSON.stringify(data)}`).toBeTruthy();
    }

    // Stopping puts a repository back to before anything evaluated it, so what this
    // block did is undone, and then waited out: the next block asks for findings.
    test.afterEach(async ({ page }) => {
      const stopped = (await page.request
        .get('/api/v1/kennel/repositories?tracked=false')
        .then((r) => r.json())) as { items: Row[] };
      for (const row of stopped.items) await setTracking(page, row.id, { tracked: true });
      await expect
        .poll(async () => (await overview(page)).states.pending, {
          message: 'the fleet is read again after tracking was put back',
          timeout: 30_000,
        })
        .toBe(0);
    });

    // The fixture controller has authentication off, so everybody there is an
    // administrator. The page is told who it is talking to.
    async function actAs(page: Page, role: 'viewer' | 'operator'): Promise<void> {
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

    test('an administrator stops it with a reason, the page says who and why, and starts it again', async ({
      page,
    }) => {
      const row = await quietRepository(page);
      await goto(page, `/kennel/repositories/${row.id}`, row.name);

      await expect(trackSwitch(page)).toBeChecked();
      await expect(page.locator('#track-state')).toHaveText(
        'Kennel Club reads this repository from GitHub.',
      );
      await expect(page.locator('p.meta')).not.toContainText('Not tracked');
      const standing = page.locator('p.standing');
      await expect(standing).toContainText('No open findings');
      await expect(standing.getByRole('link', { name: 'Open the CI tab' })).toBeVisible();

      await trackSwitch(page).click();
      const dialog = stopDialog(page);
      await expect(dialog).toContainText(
        'Its waivers are kept, and do nothing until it is tracked again.',
      );
      const stop = dialog.getByRole('button', { name: 'Stop tracking', exact: true });
      const reason = dialog.getByRole('textbox', { name: /Why Kennel Club should not look at it/ });

      // The rule is said before it is broken, in this form's own words, and the
      // switch has not moved: nothing is stopped until the form is sent.
      await expect(stop).toBeDisabled();
      await reason.fill('too short');
      await expect(dialog).toContainText(
        'At least 10 characters: say why Kennel Club should not look',
      );
      await expect(stop).toBeDisabled();
      await expect(trackSwitch(page)).toBeChecked();

      await auditThePage(page, 'the stop tracking dialog');
      const box = await dialog.boundingBox();
      const viewport = page.viewportSize();
      expect(box, 'the dialog is drawn').toBeTruthy();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(viewport!.width);

      await reason.fill(`  ${REASON}  `);
      await expect(stop).toBeEnabled();
      // The controller trims what it is sent as well, so the stored reason cannot
      // tell whether the page did; what the page sends is the page's to answer for.
      const sent = page.waitForRequest(
        (request) => request.method() === 'PUT' && request.url().endsWith('/tracking'),
      );
      await stop.click();
      expect((await sent).postDataJSON(), 'the words around the reason are not sent').toEqual({
        tracked: false,
        reason: REASON,
      });

      await expect(toast(page, 'success', 'Repository no longer tracked')).toBeVisible();
      await expect(dialog).toBeHidden();

      // It says so in the header and on the page, with who, and why, and what that means.
      await expect(trackSwitch(page)).not.toBeChecked();
      await expect(page.locator('p.meta')).toContainText('Not tracked');
      await expect(page.locator('p.meta')).toContainText('Since');
      const notice = page.getByTestId('not-tracked');
      await expect(notice).toContainText('Kennel Club is not looking at this repository.');
      // Svelte drops the whitespace at the start of an `{#if}` block, so a sentence
      // built across one reads "stopped iton 8 Oct". Match the words and the gap.
      await expect(notice).toContainText(/stopped it on \w/);
      await expect(notice).toContainText(REASON);
      await expect(notice).toContainText('Its waivers are kept');
      await expect(page.getByRole('button', { name: 'Recheck' })).toHaveCount(0);
      // The Overview's own summary of what Kennel Club says is made from the row that
      // stopping reset, and it does not read it: "Pending" and "No open findings" under
      // a notice that nothing is evaluated would be Kennel Club contradicting itself.
      await expect(standing).toContainText('Not tracked');
      await expect(standing).toContainText('that is not an all clear');
      await expect(
        page.getByText('Somebody told it not to look at this repository.'),
      ).toBeVisible();
      await expect(standing).not.toContainText('Pending');
      await expect(standing).not.toContainText('No open findings');
      await expect(standing.getByRole('link', { name: 'Open the CI tab' })).toHaveCount(0);

      const now = await tracking(page, row.id);
      expect(now.tracking.tracked).toBe(false);
      expect(now.tracking.reason, 'the words around the reason are not kept').toBe(REASON);
      expect(now.state, 'nothing is evaluated for it').toBe('pending');
      expect(now.tracking.by, 'the controller says who stopped it').toBeTruthy();
      await expect(notice).toContainText(now.tracking.by);
      const counts = await overview(page);
      expect(counts.repositories, 'it is out of the totals').toBe(FIXTURE.repos.length - 1);
      expect(counts.not_tracked).toBe(1);

      // The CI tab has nothing to show, and says that is not an all clear. The AI
      // Context tab is not Kennel Club's to stop and carries on.
      await page.getByRole('tab', { name: 'CI' }).click();
      await expect(
        page.getByText('Nothing to show for a repository that is not tracked'),
      ).toBeVisible();
      await page.getByRole('tab', { name: 'AI Context' }).click();
      await expect(notice).toHaveCount(0);
      await page.getByRole('tab', { name: 'Overview' }).click();
      await expect(notice).toBeVisible();

      // Starting again asks for nothing.
      await trackSwitch(page).click();
      await expect(toast(page, 'success', 'Repository tracked again')).toBeVisible();
      await expect(trackSwitch(page)).toBeChecked();
      await expect(notice).toHaveCount(0);
      await expect(page.getByRole('button', { name: 'Recheck' })).toBeVisible();
      await expect(standing).not.toContainText('Not tracked');
      expect((await tracking(page, row.id)).tracking.tracked).toBe(true);
    });

    test('what the controller says is wrong sits beside the field it is about', async ({
      page,
    }) => {
      const row = await quietRepository(page);
      // The form will not send a reason that is too short, so the controller's
      // field answer is handed back by the test, in the shape its 422 has.
      await page.route(`**/api/v1/kennel/repositories/${row.id}/tracking`, (route) =>
        route.fulfill({
          status: 422,
          contentType: 'application/json',
          body: JSON.stringify({
            error: { code: 'invalid', message: 'the request was refused' },
            errors: [
              {
                field: 'reason',
                message: 'may not contain control or direction-changing characters',
              },
            ],
          }),
        }),
      );
      await goto(page, `/kennel/repositories/${row.id}`, row.name);
      await trackSwitch(page).click();
      const dialog = stopDialog(page);
      await dialog
        .getByRole('textbox', { name: /Why Kennel Club should not look at it/ })
        .fill(REASON);
      await dialog.getByRole('button', { name: 'Stop tracking', exact: true }).click();
      await expect(dialog).toContainText(
        'may not contain control or direction-changing characters',
      );
      await expect(dialog).toBeVisible();
      await expect(trackSwitch(page)).toBeChecked();
      expect((await tracking(page, row.id)).tracking.tracked, 'nothing was stopped').toBe(true);
    });

    // The page learns of a change from the stream, and from the answer to the request
    // that made it. With the stream down the answer has to be enough.
    test('with no stream to say so, the answer to the press repaints the page', async ({
      page,
    }) => {
      const row = await quietRepository(page);
      await page.route('**/api/v1/events*', (route) => route.abort());
      await goto(page, `/kennel/repositories/${row.id}`, row.name);
      await trackSwitch(page).click();
      const dialog = stopDialog(page);
      await dialog
        .getByRole('textbox', { name: /Why Kennel Club should not look at it/ })
        .fill(REASON);
      await dialog.getByRole('button', { name: 'Stop tracking', exact: true }).click();
      await expect(page.getByTestId('not-tracked')).toContainText(REASON);
      await expect(trackSwitch(page)).not.toBeChecked();

      await trackSwitch(page).click();
      await expect(page.getByTestId('not-tracked')).toHaveCount(0);
      await expect(trackSwitch(page)).toBeChecked();
    });

    test('an operator is told that stopping is an administrator’s and may start it again', async ({
      page,
    }) => {
      const row = await quietRepository(page);
      await actAs(page, 'operator');
      await goto(page, `/kennel/repositories/${row.id}`, row.name);
      // A switch that would answer 403 is not offered: the sentence says who can.
      await expect(trackSwitch(page)).toBeDisabled();
      await expect(page.locator('#track-state')).toContainText('An administrator can stop that.');
      await expect(page.getByRole('button', { name: 'Recheck' })).toBeVisible();

      await setTracking(page, row.id, { tracked: false, reason: REASON });
      await expect(page.getByTestId('not-tracked')).toBeVisible();
      await expect(trackSwitch(page)).toBeEnabled();
      await expect(page.locator('#track-state')).not.toContainText(
        'An operator can start it again',
      );
      await expect(page.getByRole('button', { name: 'Recheck' })).toHaveCount(0);

      await trackSwitch(page).click();
      await expect(toast(page, 'success', 'Repository tracked again')).toBeVisible();
      await expect(page.getByTestId('not-tracked')).toHaveCount(0);
    });

    test('a viewer reads the state and is told who can change it', async ({ page }) => {
      const row = await quietRepository(page);
      await setTracking(page, row.id, { tracked: false, reason: REASON });
      await actAs(page, 'viewer');
      await goto(page, `/kennel/repositories/${row.id}`, row.name);
      await expect(trackSwitch(page)).toBeDisabled();
      await expect(page.locator('#track-state')).toContainText('An operator can start it again.');
      await expect(page.getByTestId('not-tracked')).toContainText(REASON);
      await auditThePage(page, 'a repository that is not tracked, as a viewer');
    });

    test('the list leaves out what is not tracked, a filter brings it back, and a card counts it apart', async ({
      page,
    }) => {
      const row = await quietRepository(page);
      const rows = dataRows(grid(page, 'Repositories'));

      // A fleet that has stopped looking at nothing has no card for it: a zero for a
      // feature most fleets never use is a number to learn to ignore.
      await goto(page, '/kennel', 'Kennel Club');
      await expect(tile(page, 'Repositories')).toContainText(String(FIXTURE.repos.length));
      await expect(tile(page, 'Not tracked')).toHaveCount(0);
      await setTracking(page, row.id, { tracked: false, reason: REASON });

      // The Overview counts the repositories Kennel Club is looking at, and the
      // others on a card of their own, so a number is never a repository nothing
      // has looked at yet.
      await goto(page, '/kennel', 'Kennel Club');
      await expect(tile(page, 'Repositories')).toContainText(String(FIXTURE.repos.length - 1));
      await expect(tile(page, 'Not tracked')).toContainText('1');

      // The card opens exactly those, and says so as a filter that can be taken off.
      await page.getByRole('link', { name: /^Not tracked: / }).click();
      await expect(page).toHaveURL(/tracked=false/);
      await expect(page).toHaveURL(/active=all/);
      await expect(page.getByRole('combobox', { name: 'Filter by tracking' })).toHaveValue('false');
      await expect(rows).toHaveCount(1);
      await expect(rows.first()).toContainText(row.name);
      await expect(rows.first()).toContainText('Not tracked');
      await expect(rows.first()).toContainText('Not evaluated');
      await expect(rows.first()).toContainText('Stopped');
      await expect(
        page.getByRole('button', { name: 'Remove the Tracking filter Not tracked' }),
      ).toBeVisible();

      // Without the filter the list is of the ones being looked at, as many as the card said.
      await page.getByRole('button', { name: 'Remove the Tracking filter Not tracked' }).click();
      await expect(rows).toHaveCount(FIXTURE.repos.length - 1);
      await expect(grid(page, 'Repositories')).not.toContainText(row.name);

      // Clearing every filter is the same way back to the default.
      await page
        .getByRole('combobox', { name: 'Filter by tracking' })
        .selectOption({ label: 'Not tracked' });
      await expect(rows).toHaveCount(1);
      await page.getByRole('button', { name: 'Clear all' }).click();
      await expect(rows).toHaveCount(FIXTURE.repos.length - 1);
      await expect(page).not.toHaveURL(/tracked=/);

      // And a person can ask for both.
      await page
        .getByRole('combobox', { name: 'Filter by tracking' })
        .selectOption({ label: 'Tracked and not tracked' });
      await expect(rows).toHaveCount(FIXTURE.repos.length);
      await expect(grid(page, 'Repositories')).toContainText(row.name);
      await auditThePage(page, 'the list with a repository that is not tracked');
    });

    test('with nothing tracked the Overview says so, and where the repositories are', async ({
      page,
    }) => {
      const listed = (await page.request
        .get('/api/v1/kennel/repositories')
        .then((r) => r.json())) as { items: Row[] };
      for (const row of listed.items)
        await setTracking(page, row.id, { tracked: false, reason: REASON });

      await goto(page, '/kennel', 'Kennel Club');
      // The title of an empty state is a paragraph and not a heading.
      const empty = page.getByText('Kennel Club is not looking at any repository', { exact: true });
      await expect(empty).toBeVisible();
      // A choice is not an absence, and the sentence does not say there is nothing.
      await expect(page.getByText('Nothing to look at yet')).toHaveCount(0);
      await expect(page.locator('#main')).toContainText(
        `All ${FIXTURE.repos.length} repositories it has been told not to look at.`,
      );
      await page.getByRole('link', { name: 'See them' }).click();
      await expect(page).toHaveURL(/tracked=false/);
      // They are not all active ones, and the card counted them all.
      await expect(page).toHaveURL(/active=all/);
      await expect(dataRows(grid(page, 'Repositories'))).toHaveCount(FIXTURE.repos.length);
    });

    test('a page that is open follows a change made somewhere else', async ({ page }) => {
      const row = await quietRepository(page);
      await goto(page, `/kennel/repositories/${row.id}`, row.name);
      await expect(page.getByTestId('not-tracked')).toHaveCount(0);

      await setTracking(page, row.id, { tracked: false, reason: REASON });
      await expect(page.getByTestId('not-tracked')).toContainText(REASON);
      await expect(trackSwitch(page)).not.toBeChecked();

      await setTracking(page, row.id, { tracked: true });
      await expect(page.getByTestId('not-tracked')).toHaveCount(0);
      await expect(trackSwitch(page)).toBeChecked();
    });

    // A frame on the stream is the repository as the page reads it, and replaces
    // what the page holds. A read the page asked for earlier can still be on its
    // way when the frame lands, and its answer is older than the frame: if it is
    // allowed to replace what the frame set, the page shows the repository as it was
    // and nothing says otherwise. That is what made the test above fail about once
    // in a hundred runs, whenever the read the page makes as its stream goes live was
    // still out when the change was made. Here the read is held on purpose.
    test('a late answer to an older read does not put back what a newer frame changed', async ({
      page,
    }) => {
      const row = await quietRepository(page);
      let hold = false;
      let answered: () => void = () => {};
      const controllerAnswered = new Promise<void>((resolve) => (answered = resolve));
      let release: () => void = () => {};
      const released = new Promise<void>((resolve) => (release = resolve));
      await page.route(`**/api/v1/kennel/repositories/${row.id}`, async (route) => {
        if (!hold || route.request().method() !== 'GET') return route.continue();
        hold = false;
        const answer = await route.fetch();
        answered();
        await released;
        return route.fulfill({ response: answer });
      });
      await goto(page, `/kennel/repositories/${row.id}`, row.name);
      // A change made before the stream is open sends no frame to anybody, so the
      // stream has to be live before the page is asked to read again.
      await expect(page.locator('p.connection[data-state="live"]')).toBeAttached();
      const notice = page.getByTestId('not-tracked');
      await expect(notice).toHaveCount(0);

      hold = true;
      await page.getByRole('button', { name: 'Refresh', exact: true }).click();
      // The controller answers the read while the repository is still tracked, and
      // the answer is kept back. Then the change is made, and its frame arrives.
      await controllerAnswered;
      await setTracking(page, row.id, { tracked: false, reason: REASON });
      await expect(notice).toContainText(REASON);

      const arrived = page.waitForResponse(
        (response) =>
          response.url().endsWith(`/api/v1/kennel/repositories/${row.id}`) &&
          response.request().method() === 'GET',
      );
      release();
      await arrived;
      // Two frames of the page's own drawing, so the answer has been acted on.
      await page.evaluate(
        () => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))),
      );
      await expect(notice).toContainText(REASON);
      await expect(trackSwitch(page)).not.toBeChecked();
      expect((await tracking(page, row.id)).tracking.tracked).toBe(false);
    });

    /* -- several at once, from the list ---------------------------------------- */

    // Stopping is the same decision as on a repository's own page, asked of a
    // selection: one reason, one request and one audit entry for each.
    const API = 'acme/api';
    const WIDGETS = 'acme/widgets';
    const bulkBar = (page: Page) => page.getByRole('group', { name: /Actions for the selected/ });
    const bulkDialog = (page: Page, title: string) => page.getByRole('dialog', { name: title });
    const listRows = (page: Page) => dataRows(grid(page, 'Repositories'));
    const tick = async (page: Page, name: string) =>
      listRows(page).filter({ hasText: name }).getByRole('checkbox').check();
    const putsToTracking = (page: Page): string[] => {
      const sent: string[] = [];
      page.on('request', (request) => {
        if (request.method() === 'PUT' && request.url().endsWith('/tracking'))
          sent.push(request.postData() ?? '');
      });
      return sent;
    };

    test('an administrator ticks several and stops them with one reason, each by its own request', async ({
      page,
    }) => {
      const [api, widgets] = [await repository(page, API), await repository(page, WIDGETS)];
      const before = await overview(page);
      await goto(page, '/kennel/repositories', 'Repositories');
      const sent = putsToTracking(page);

      await expect(bulkBar(page)).toHaveCount(0);
      await tick(page, API);
      await tick(page, WIDGETS);
      await expect(bulkBar(page)).toContainText('2 selected');
      await bulkBar(page).getByRole('button', { name: 'Stop tracking' }).click();

      const dialog = bulkDialog(page, 'Stop tracking 2 repositories');
      await expect(dialog).toBeVisible();
      // What is about to be stopped is named, so a wrong tick is seen before it costs
      // anything.
      const named = dialog.getByRole('list', { name: 'Repositories to stop tracking' });
      await expect(named).toContainText(API);
      await expect(named).toContainText(WIDGETS);
      await expect(dialog).toContainText('Their waivers are kept');

      const reason = dialog.getByRole('textbox', {
        name: /Why Kennel Club should not look at them/,
      });
      const stop = dialog.getByRole('button', { name: 'Stop tracking', exact: true });
      await expect(stop).toBeDisabled();
      await reason.fill('too short');
      await expect(dialog).toContainText(
        'At least 10 characters: say why Kennel Club should not look at these repositories',
      );
      await expect(stop).toBeDisabled();
      await auditThePage(page, 'the stop tracking dialog, for several repositories');

      await reason.fill(`  ${REASON}  `);
      await stop.click();

      await expect(toast(page, 'success', '2 repositories no longer tracked')).toBeVisible();
      await expect(dialog).toBeHidden();
      // One request for each, in the order they were ticked, saying the same thing,
      // without the words around the reason.
      expect(sent.map((body) => JSON.parse(body) as unknown)).toEqual([
        { tracked: false, reason: REASON },
        { tracked: false, reason: REASON },
      ]);

      // Both have left the list of what is being looked at, and the selection with them.
      await expect(listRows(page).filter({ hasText: API })).toHaveCount(0);
      await expect(listRows(page).filter({ hasText: WIDGETS })).toHaveCount(0);
      await expect(bulkBar(page)).toHaveCount(0);
      for (const row of [api, widgets]) {
        const now = await tracking(page, row.id);
        expect(now.tracking.tracked, `${row.name} is stopped`).toBe(false);
        expect(now.tracking.reason).toBe(REASON);
        expect(now.tracking.by, 'the controller says who').toBeTruthy();
      }
      expect((await overview(page)).not_tracked).toBe(before.not_tracked + 2);
    });

    test('cancelling stops nothing and keeps the selection', async ({ page }) => {
      const row = await repository(page, API);
      await goto(page, '/kennel/repositories', 'Repositories');
      const sent = putsToTracking(page);
      await tick(page, API);
      await bulkBar(page).getByRole('button', { name: 'Stop tracking' }).click();

      const dialog = bulkDialog(page, 'Stop tracking this repository');
      await dialog
        .getByRole('textbox', { name: /Why Kennel Club should not look at/ })
        .fill(REASON);
      await dialog.getByRole('button', { name: 'Cancel' }).click();
      await expect(dialog).toBeHidden();

      // Nobody who backed out has chosen anything, so what they ticked is still ticked
      // to be changed or tried again.
      await expect(bulkBar(page)).toContainText('1 selected');
      expect(sent, 'nothing was sent').toEqual([]);
      expect((await tracking(page, row.id)).tracking.tracked).toBe(true);

      // And the next time the dialog opens it is empty, not the last person's reason.
      await bulkBar(page).getByRole('button', { name: 'Stop tracking' }).click();
      await expect(
        dialog.getByRole('textbox', { name: /Why Kennel Club should not look at/ }),
      ).toHaveValue('');
    });

    test('what is already not tracked is left as it is, and said so', async ({ page }) => {
      const [api, widgets] = [await repository(page, API), await repository(page, WIDGETS)];
      await setTracking(page, widgets.id, { tracked: false, reason: REASON });
      await goto(page, '/kennel/repositories?tracked=all', 'Repositories');
      const sent = putsToTracking(page);

      await tick(page, API);
      await tick(page, WIDGETS);
      await expect(bulkBar(page)).toContainText('2 selected');
      await bulkBar(page).getByRole('button', { name: 'Stop tracking' }).click();

      // Two ticked, one to stop: the dialog is about one, and says what it leaves.
      const dialog = bulkDialog(page, 'Stop tracking this repository');
      await expect(dialog).toContainText(
        '1 repository that is already not tracked is left as it is.',
      );
      const named = dialog.getByRole('list', { name: 'Repositories to stop tracking' });
      await expect(named).toContainText(API);
      await expect(named).not.toContainText(WIDGETS);
      await dialog
        .getByRole('textbox', { name: /Why Kennel Club should not look at/ })
        .fill('A different reason, for the one that is still tracked.');
      await dialog.getByRole('button', { name: 'Stop tracking', exact: true }).click();

      await expect(toast(page, 'success', '1 repository no longer tracked')).toBeVisible();
      expect(sent, 'only the one that was tracked is asked about').toHaveLength(1);
      // The one that was already stopped keeps the reason it was stopped with, and
      // whoever stopped it: this is not a way to rewrite what a colleague wrote.
      expect((await tracking(page, widgets.id)).tracking.reason).toBe(REASON);
      expect((await tracking(page, api.id)).tracking.reason).toBe(
        'A different reason, for the one that is still tracked.',
      );
    });

    test('only what is already stopped is ticked: nothing to ask, so it says so', async ({
      page,
    }) => {
      const widgets = await repository(page, WIDGETS);
      await setTracking(page, widgets.id, { tracked: false, reason: REASON });
      await goto(page, '/kennel/repositories?tracked=all', 'Repositories');
      const sent = putsToTracking(page);

      await tick(page, WIDGETS);
      await bulkBar(page).getByRole('button', { name: 'Stop tracking' }).click();
      await expect(
        page.locator('.toast[data-tone="info"]', { hasText: 'Nothing to stop' }),
      ).toBeVisible();
      await expect(page.getByRole('dialog')).toHaveCount(0);
      // A dialog that opened on nothing would ask for a reason to do nothing.
      await expect(bulkBar(page)).toContainText('1 selected');
      expect(sent).toEqual([]);
    });

    test('one refused is named, the rest are stopped, and the refused stays ticked', async ({
      page,
    }) => {
      const [api, widgets] = [await repository(page, API), await repository(page, WIDGETS)];
      await page.route(`**/api/v1/kennel/repositories/${widgets.id}/tracking`, (route) =>
        route.fulfill({
          status: 409,
          contentType: 'application/json',
          body: JSON.stringify({
            error: { code: 'conflict', message: 'another change got there first' },
          }),
        }),
      );
      await goto(page, '/kennel/repositories', 'Repositories');
      await tick(page, API);
      await tick(page, WIDGETS);
      await bulkBar(page).getByRole('button', { name: 'Stop tracking' }).click();
      const dialog = bulkDialog(page, 'Stop tracking 2 repositories');
      await dialog
        .getByRole('textbox', { name: /Why Kennel Club should not look at them/ })
        .fill(REASON);
      await dialog.getByRole('button', { name: 'Stop tracking', exact: true }).click();

      // A count would leave the person to find out which; the name does not.
      const failed = toast(page, 'error', '1 of 2 could not be stopped');
      await expect(failed).toBeVisible();
      await expect(failed).toContainText('The other 1 was stopped.');
      await expect(failed).toContainText(`${WIDGETS}: another change got there first`);

      expect((await tracking(page, api.id)).tracking.tracked, 'the first went through').toBe(false);
      expect((await tracking(page, widgets.id)).tracking.tracked, 'the second did not').toBe(true);
      // What stopped has left the list; what did not is still there and still ticked,
      // to be tried again without ticking it again.
      await expect(listRows(page).filter({ hasText: API })).toHaveCount(0);
      await expect(listRows(page).filter({ hasText: WIDGETS })).toHaveCount(1);
      await expect(bulkBar(page)).toContainText('2 selected');
    });

    test('a reason the controller refuses is said beside the field, and nothing is stopped', async ({
      page,
    }) => {
      const [api, widgets] = [await repository(page, API), await repository(page, WIDGETS)];
      await page.route('**/api/v1/kennel/repositories/*/tracking', (route) =>
        route.fulfill({
          status: 422,
          contentType: 'application/json',
          body: JSON.stringify({
            error: { code: 'invalid', message: 'the request was refused' },
            errors: [
              {
                field: 'reason',
                message: 'may not contain control or direction-changing characters',
              },
            ],
          }),
        }),
      );
      await goto(page, '/kennel/repositories', 'Repositories');
      const sent = putsToTracking(page);
      await tick(page, API);
      await tick(page, WIDGETS);
      await bulkBar(page).getByRole('button', { name: 'Stop tracking' }).click();
      const dialog = bulkDialog(page, 'Stop tracking 2 repositories');
      await dialog
        .getByRole('textbox', { name: /Why Kennel Club should not look at them/ })
        .fill(REASON);
      await dialog.getByRole('button', { name: 'Stop tracking', exact: true }).click();

      await expect(dialog).toContainText(
        'may not contain control or direction-changing characters',
      );
      await expect(dialog).toBeVisible();
      // The same reason would be refused for the second, so it is not sent.
      expect(sent, 'the second is not tried with a reason that was refused').toHaveLength(1);
      for (const row of [api, widgets])
        expect((await tracking(page, row.id)).tracking.tracked).toBe(true);
      await expect(bulkBar(page)).toContainText('2 selected');
    });

    // The list learns of a stop from the stream, and from the page asking again
    // once its own requests are done. With the stream down the second has to be
    // enough, or the rows a person just stopped would stay in the list they are
    // looking at until they thought to reload it.
    test('with no stream to say so, the list is asked again once the stops are done', async ({
      page,
    }) => {
      const row = await repository(page, API);
      await page.route('**/api/v1/events*', (route) => route.abort());
      await goto(page, '/kennel/repositories', 'Repositories');
      await expect(listRows(page).filter({ hasText: API })).toHaveCount(1);
      await tick(page, API);
      await bulkBar(page).getByRole('button', { name: 'Stop tracking' }).click();
      const dialog = bulkDialog(page, 'Stop tracking this repository');
      await dialog
        .getByRole('textbox', { name: /Why Kennel Club should not look at/ })
        .fill(REASON);
      await dialog.getByRole('button', { name: 'Stop tracking', exact: true }).click();

      await expect(toast(page, 'success', '1 repository no longer tracked')).toBeVisible();
      await expect(listRows(page).filter({ hasText: API })).toHaveCount(0);
      await expect(bulkBar(page)).toHaveCount(0);
      expect((await tracking(page, row.id)).tracking.tracked).toBe(false);
    });

    test('more than eight are named by eight and a count of the rest', async ({ page }) => {
      // The fixture has three repositories, so the list is handed more of them.
      const first = await repository(page, API);
      await page.route('**/api/v1/kennel/repositories?*', async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as { items: Row[]; total: number };
        const items = [...body.items];
        for (let i = 0; items.length < 11; i++)
          items.push({ ...first, id: `${first.id}-copy-${i}`, name: `acme/copy-${i}` });
        return route.fulfill({ response, json: { ...body, items, total: items.length } });
      });
      await goto(page, '/kennel/repositories', 'Repositories');
      const sent = putsToTracking(page);
      // The header's box is on the page before any row is, and ticking it then
      // selects nothing, so the rows are waited for first. Without this the test
      // failed whenever the list was a moment slower than the click.
      await expect(listRows(page)).toHaveCount(11);
      await page.getByRole('checkbox', { name: /^Select every/ }).check();
      await bulkBar(page).getByRole('button', { name: 'Stop tracking' }).click();
      const dialog = bulkDialog(page, 'Stop tracking 11 repositories');
      const named = dialog.getByRole('list', { name: 'Repositories to stop tracking' });
      await expect(named.getByRole('listitem')).toHaveCount(9);
      await expect(named).toContainText('and 3 more');
      await dialog.getByRole('button', { name: 'Cancel' }).click();
      expect(sent).toEqual([]);
    });

    test('an operator is not offered the tick boxes: stopping is an administrator’s', async ({
      page,
    }) => {
      await actAs(page, 'operator');
      await goto(page, '/kennel/repositories', 'Repositories');
      await expect(listRows(page).first()).toBeVisible();
      await expect(listRows(page).first().getByRole('checkbox')).toHaveCount(0);
      await expect(page.getByRole('checkbox', { name: /^Select every/ })).toHaveCount(0);
      await expect(bulkBar(page)).toHaveCount(0);
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
      ['Repositories', '/kennel/repositories?active=all', counted.repositories],
      // Named for the standing, which the preference for playful wording changes.
      [
        'No open findings|Best in show',
        '/kennel/repositories?state=best_in_show&active=all',
        counted.states.best_in_show,
      ],
      [
        'Need attention',
        '/kennel/repositories?state=attention&active=all',
        counted.states.attention,
      ],
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
    await expect(page).toHaveURL(/\/kennel\/repositories\?severity=error&active=all$/);
    await expect(rows.first()).toContainText(PUBLIC_REPO);
    await expect(page.getByRole('button', { name: /Remove.*Severity/i })).toBeVisible();

    // The panel under the cards opens the same list as its card.
    await goto(page, '/kennel', 'Kennel Club');
    await page.getByRole('link', { name: 'See all', exact: true }).click();
    await expect(page).toHaveURL(/\/kennel\/repositories\?state=attention&active=all$/);
    await expect(rows, 'See all opens as many rows as Need attention counts').toHaveCount(
      counted.states.attention,
    );
  });

  test('a card is judged by its own count', async ({ page }) => {
    // The stream is cut so that only this document is ever on the page: a frame
    // of the real summary would replace it, and a "has no link" check made after
    // that would be about the real fleet and not about the card.
    await page.route('**/api/v1/events*', (route) => route.abort('connectionfailed'));

    // One count at a time is something, and every other is nothing, so a card
    // judged by another card's count, or by none, is the one that shows.
    const counted = [
      ['No open findings|Best in show', '/kennel/repositories?state=best_in_show&active=all'],
      ['Need attention', '/kennel/repositories?state=attention&active=all'],
      ['Errors', '/kennel/repositories?severity=error&active=all'],
      ['Warnings', '/kennel/repositories?severity=warning&active=all'],
    ] as const;
    let alone = 0;
    // "Partly checked" and "Waived" are judged by their own counts as well, so they
    // have something to count except when this says they do not.
    let nothing = false;
    await page.route('**/api/v1/kennel', async (route) => {
      const response = await route.fetch();
      const body = (await response.json()) as Overview;
      const some = (n: number) => (alone === n ? 3 : 0);
      return route.fulfill({
        response,
        json: {
          ...body,
          states: {
            ...body.states,
            best_in_show: some(0),
            attention: some(1),
            partial: nothing ? 0 : 1,
            pending: nothing ? 0 : 1,
          },
          counts: { ...body.counts, error: some(2), warning: some(3), waived: nothing ? 0 : 1 },
        },
      });
    });

    for (const [only, [alonelabel]] of counted.entries()) {
      alone = only;
      await goto(page, '/kennel', 'Kennel Club');
      // The card that always links, which is also how the page is known to be read.
      await expect(card(page, 'Repositories')).toHaveAttribute(
        'href',
        '/kennel/repositories?active=all',
      );
      for (const [n, [label, href]] of counted.entries()) {
        const where = `${label}, when only ${alonelabel} counts something`;
        if (n === only) await expect(card(page, label), where).toHaveAttribute('href', href);
        else await expect(card(page, label), `${where}: no link`).toHaveCount(0);
      }
      // "Partly checked" adds two standings, which the list narrows by as one, and
      // "Waived" counts findings and opens the repositories that hold them. Each has
      // something to count here, so each is a link.
      for (const [label, value, href] of [
        ['Partly checked', '2', '/kennel/repositories?incomplete=true&active=all'],
        ['Waived', '1', '/kennel/repositories?waived=true&active=all'],
      ] as const) {
        await expect(tile(page, label), `${label} is on the page`).toBeVisible();
        await expect(
          card(page, label),
          `${label} opens the repositories it counts`,
        ).toHaveAttribute('href', href);
        await expect(tile(page, label), `${label} says ${value}`).toContainText(value);
      }
    }

    // And when they count nothing there is nothing to open, as for every other card.
    nothing = true;
    await goto(page, '/kennel', 'Kennel Club');
    for (const label of ['Partly checked', 'Waived']) {
      await expect(tile(page, label), `${label} is on the page`).toBeVisible();
      await expect(tile(page, label).locator('a'), `${label} counts nothing: no link`).toHaveCount(
        0,
      );
    }
  });

  // A repository Kennel Club has been told not to look at is put back to "pending",
  // which is one of the two standings "Partly checked" adds up. The card counts only
  // what is being tracked, so the list it opens has to as well, or the click finds
  // one row more than the number it came from. Nothing in the fixture is partly
  // checked, so the stopped repository is the only pending row there is.
  test('the list Partly checked opens is as long as its number, and leaves out what is not tracked', async ({
    page,
  }) => {
    const before = await overview(page);
    const partly = before.states.partial + before.states.pending;

    const quiet = await (
      await page.request.get('/api/v1/kennel/repositories?state=best_in_show&tracked=true')
    ).json();
    const sandbox = (quiet.items as Array<{ id: string; name: string }>)[0]!;
    const stopped = await page.request.put(`/api/v1/kennel/repositories/${sandbox.id}/tracking`, {
      data: { tracked: false, reason: 'a sandbox nobody keeps, stopped for this test' },
    });
    expect(stopped.ok(), await stopped.text()).toBeTruthy();
    try {
      const after = await overview(page);
      expect(after.states.partial + after.states.pending, 'the card does not count it').toBe(
        partly,
      );

      // The rows are asked of the list's own answer, which cannot be satisfied before
      // it has arrived: a count of nothing is also what a page that has not loaded has.
      const answered = page.waitForResponse(
        (response) =>
          response.url().includes('/api/v1/kennel/repositories?') &&
          response.url().includes('incomplete=true'),
      );
      await goto(page, '/kennel/repositories?incomplete=true&active=all', 'Repositories');
      const list = (await (await answered).json()) as {
        total: number;
        items: Array<{ name: string }>;
      };
      expect(list.total, 'as many as the number on the card').toBe(partly);
      expect(
        list.items.map((item) => item.name),
        'the stopped one is not among them',
      ).not.toContain(sandbox.name);
    } finally {
      await page.request.put(`/api/v1/kennel/repositories/${sandbox.id}/tracking`, {
        data: { tracked: true },
      });
    }
  });

  // A summary on the stream replaces the document the page holds. A read the page
  // asked for earlier can still be on its way when it lands, and its answer is older:
  // if it is allowed to replace the summary, the cards show the fleet as it was and
  // nothing says otherwise. Here the read is held on purpose.
  test('a late answer to an older read does not put back what a newer summary changed', async ({
    page,
  }) => {
    const sandbox = await quietRepository(page);
    let hold = false;
    let answered: () => void = () => {};
    const controllerAnswered = new Promise<void>((resolve) => (answered = resolve));
    let release: () => void = () => {};
    const released = new Promise<void>((resolve) => (release = resolve));
    await page.route('**/api/v1/kennel', async (route) => {
      if (!hold || route.request().method() !== 'GET') return route.continue();
      hold = false;
      const answer = await route.fetch();
      answered();
      await released;
      return route.fulfill({ response: answer });
    });
    try {
      await goto(page, '/kennel', 'Kennel Club');
      // A change made before the stream is open sends no frame to anybody.
      await expect(page.locator('p.connection[data-state="live"]')).toBeAttached();
      const notTracked = page.getByRole('link', { name: /^Not tracked: / });
      await expect(notTracked).toHaveCount(0);

      hold = true;
      await page.getByRole('button', { name: 'Refresh', exact: true }).click();
      // The controller answers while nothing is stopped, and the answer is kept
      // back. Then a repository is stopped, and the summary that says so arrives.
      await controllerAnswered;
      const res = await page.request.put(`/api/v1/kennel/repositories/${sandbox.id}/tracking`, {
        data: { tracked: false, reason: 'Held back on purpose.' },
      });
      expect(res.ok()).toBeTruthy();
      await expect(notTracked).toHaveCount(1);

      const arrived = page.waitForResponse(
        (response) =>
          response.url().endsWith('/api/v1/kennel') && response.request().method() === 'GET',
      );
      release();
      await arrived;
      // Two frames of the page's own drawing, so the answer has been acted on.
      await page.evaluate(
        () => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))),
      );
      await expect(notTracked).toHaveCount(1);
    } finally {
      await page.request.put(`/api/v1/kennel/repositories/${sandbox.id}/tracking`, {
        data: { tracked: true },
      });
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
    await expect(page).toHaveURL(/\/kennel\/repositories\?state=attention&active=all$/);
    await auditThePage(page, 'the list from a card');
  });
});

/* -- the AI Context row ---------------------------------------------------------- */

// The row says something only when an AI Context run has failed: that is the one
// state of it somebody has to act on, and it is already on every page as a
// problem. A healthy fleet has no badge, so there is nothing on the row to learn
// to ignore.
test.describe('the AI Context row in the side menu', () => {
  const rail = (page: Page) => page.getByRole('navigation', { name: 'Kennel Club' });
  const row = (page: Page) => rail(page).getByRole('link', { name: 'AI Context', exact: true });
  const badge = (page: Page) => row(page).locator('.badge');

  const problem = (target: string, severity: 'error' | 'warning', kind = 'ai_context') => ({
    code: kind === 'ai_context' ? 'ai_context.run_failed' : 'kennel.exposure',
    severity,
    audience: 'fleet',
    title: `A problem for ${target}`,
    detail: 'It did not work.',
    fix: 'Make it work.',
    target_kind: kind,
    target_id: target,
  });

  /** What the controller says is wrong, with the stream cut so nothing else can say otherwise. */
  async function sayProblemsAre(page: Page, items: ReturnType<typeof problem>[]): Promise<void> {
    await page.route('**/api/v1/events*', (route) => route.abort('connectionfailed'));
    await page.route('**/api/v1/problems', async (route) => {
      const response = await route.fetch();
      const body = (await response.json()) as { items?: unknown[] };
      return route.fulfill({ response, json: { ...body, items } });
    });
  }

  test('a fleet with no failed run has no badge', async ({ page }) => {
    await goto(page, '/kennel/ai-context', 'AI Context');
    await expect(row(page)).toBeVisible();
    await expect(badge(page)).toHaveCount(0);
    await expect(row(page)).not.toHaveAttribute('aria-describedby', /./);
  });

  for (const [says, items, count, tone, text] of [
    ['one run failed', [problem('a', 'error')], '1', 'danger', '1 repository needs attention'],
    [
      'a run failed and another only warned',
      [problem('a', 'warning'), problem('b', 'error')],
      '2',
      'danger',
      '2 repositories need attention',
    ],
    [
      'runs only warned',
      [problem('a', 'warning'), problem('b', 'warning'), problem('c', 'warning')],
      '3',
      'pending',
      '3 repositories need attention',
    ],
  ] as const) {
    test(`the row says how bad and how many when ${says}`, async ({ page }) => {
      // Neither a pool's problem nor Kennel Club's is a failed AI Context run.
      await sayProblemsAre(page, [
        ...items,
        problem('pool_1', 'error', 'pool'),
        problem('acme/site', 'error', 'kennel'),
      ]);
      await goto(page, '/kennel/ai-context', 'AI Context');
      await expect(badge(page)).toHaveText(count);
      await expect(badge(page)).toHaveAttribute('data-tone', tone);
      // The link is still named for the page, and what is wrong is its description.
      await expect(row(page)).toHaveAccessibleDescription(text);
      await expect(badge(page).locator('..')).toHaveAttribute('aria-hidden', 'true');
    });
  }

  test('it is on every page of the section, since the rail is', async ({ page }) => {
    await sayProblemsAre(page, [problem('a', 'error')]);
    for (const [path, heading] of [
      ['/kennel', 'Kennel Club'],
      ['/kennel/repositories', 'Repositories'],
      ['/kennel/ai-context', 'AI Context'],
    ] as const) {
      await goto(page, path, heading);
      await expect(badge(page), path).toHaveText('1');
    }
    // The other two rows have nothing to say.
    await expect(
      rail(page).getByRole('link', { name: 'Overview', exact: true }).locator('.badge'),
    ).toHaveCount(0);
    await expect(
      rail(page).getByRole('link', { name: 'Repositories', exact: true }).locator('.badge'),
    ).toHaveCount(0);
  });

  // The frames are delivered by hand to the open stream, as for a resync above.
  test('it comes and goes as runs fail and pass, without a reload', async ({ page }) => {
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
    await goto(page, '/kennel/ai-context', 'AI Context');
    await expect(page.locator('.connection')).toHaveAttribute('data-state', 'live');
    await expect(badge(page)).toHaveCount(0);
    await plantMarker(page);

    const send = (items: unknown[]) =>
      page.evaluate(
        (payload) => {
          for (const stream of (window as unknown as { __streams: EventSource[] }).__streams)
            stream.dispatchEvent(new MessageEvent('problems.updated', { data: payload }));
        },
        JSON.stringify({ ok: true, items }),
      );
    await send([problem('a', 'error')]);
    await expect(badge(page)).toHaveText('1');
    await send([problem('a', 'error'), problem('b', 'warning')]);
    await expect(badge(page)).toHaveText('2');
    await send([]);
    await expect(badge(page)).toHaveCount(0);
    await expectNoReload(page);
  });

  test('on a phone the row, its badge and the others are all in view at a size a finger can use', async ({
    page,
    isMobile,
  }) => {
    test.skip(!isMobile, 'the phone project checks the narrow widths');
    await sayProblemsAre(page, [problem('a', 'error'), problem('b', 'error')]);
    for (const width of [320, 360, 412]) {
      await page.setViewportSize({ width, height: 780 });
      await goto(page, '/kennel/ai-context', 'AI Context');
      await expect(badge(page), `the badge at ${width}px`).toBeInViewport({ ratio: 1 });
      for (const name of ['Overview', 'Repositories', 'AI Context']) {
        const link = rail(page).getByRole('link', { name, exact: true });
        await expect(link, `${name} at ${width}px`).toBeInViewport({ ratio: 1 });
        expect(
          (await link.boundingBox())!.height,
          `${name} at ${width}px is a finger-sized target`,
        ).toBeGreaterThanOrEqual(44);
      }
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
        `the page does not scroll sideways at ${width}px`,
      ).toBe(true);
    }
    await auditThePage(page, 'AI Context with a failed run');
  });
});

/* -- the AI Context tab on a repository ------------------------------------------ */

// A repository's own page shows the card the AI Context page lists, found by the
// installation and GitHub's ID for the repository. The API answers 404 to somebody
// who may not configure the installation whether or not the repository has AI
// Context, so the tab must not read that 404 as "not set up" for them.
test.describe('the AI Context tab on a repository', () => {
  const off = { 'kennel.enabled': false };
  let target: { id: string; name: string; installation_id: string; repository_id: number };

  const notFound = { status: 404, json: { error: { code: 'not_found', message: 'Not found' } } };

  const record = (over: Record<string, unknown> = {}) => ({
    id: 'ctx_demo1',
    repository: {
      github_host: 'github.com',
      installation_id: target.installation_id,
      repository_id: target.repository_id,
    },
    full_name: target.name,
    instructions: 'Use the pack on the zoomies-ai-context branch.',
    badge_markdown: '![AI Context](https://example.test/badge.svg)',
    config: {
      source_branch: 'main',
      destination: 'repository',
      exclude: [],
      keep_snapshots: 5,
    },
    revision: 1,
    workflow_outdated: false,
    available: false,
    freshness: {
      state: 'awaiting_merge',
      desired_commit: 'abcdef1234567890',
      published_commit: '',
      snapshot_id: '',
      checked_at: new Date().toISOString(),
    },
    setup_state: 'awaiting_merge',
    setup_pr_url: 'https://github.com/acme/site/pull/12',
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    ...over,
  });

  /** What the API says about AI Context, and what it was asked, for the pair it is asked about. */
  async function apiSays(
    page: Page,
    says: {
      draft: (params: URLSearchParams) => { status?: number; json: unknown };
      installations?: Array<{ id: string; target: string }>;
    },
  ): Promise<URLSearchParams[]> {
    const asked: URLSearchParams[] = [];
    await page.route('**/api/v1/ai-context/draft*', (route) => {
      const params = new URL(route.request().url()).searchParams;
      asked.push(params);
      const answer = says.draft(params);
      return route.fulfill({ status: answer.status ?? 200, json: answer.json });
    });
    await page.route('**/api/v1/ai-context/installations', (route) =>
      route.fulfill({ json: { items: says.installations ?? [] } }),
    );
    return asked;
  }

  const tab = (page: Page) => page.getByRole('tabpanel');
  const toast = (page: Page, tone: 'success' | 'error', text: string) =>
    page.locator(`.toast[data-tone="${tone}"]`).filter({ hasText: text });
  const here = (path = '') => `/kennel/repositories/${target.id}/ai-context${path}`;

  test.beforeEach(async ({ page }) => {
    await patchSettings(page, { 'kennel.enabled': true });
    await untilRead(page);
    const row = await repository(page, PUBLIC_REPO);
    const detail = (await page.request
      .get(`/api/v1/kennel/repositories/${row.id}`)
      .then((r) => r.json())) as { installation_id: string; repository_id: number };
    target = { id: row.id, name: row.name, ...detail };
  });
  test.afterEach(async ({ page }) => patchSettings(page, off));

  test('the tab has an address, and is one of the repository’s sections', async ({ page }) => {
    await apiSays(page, { draft: () => notFound });
    await goto(page, `/kennel/repositories/${target.id}`, PUBLIC_REPO);
    await plantMarker(page);
    await page.getByRole('tab', { name: 'AI Context', exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`${here()}$`));
    await expect(page.getByRole('tab', { name: 'AI Context', exact: true })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`/kennel/repositories/${target.id}$`));
    await expectNoReload(page);
  });

  test('it shows the card for the repository, found by the installation and GitHub’s ID', async ({
    page,
  }) => {
    const asked = await apiSays(page, {
      draft: () => ({ json: record() }),
      installations: [{ id: target.installation_id, target: 'acme-org' }],
    });
    await goto(page, here(), PUBLIC_REPO);
    await expect(tab(page).getByText('Awaiting merge')).toBeVisible();
    // The installation is named as the AI Context page names it, not shown as an ID.
    await expect(tab(page).getByText('acme-org', { exact: true })).toBeVisible();
    await expect(tab(page).getByText('Source branch')).toBeVisible();
    await expect(tab(page).getByRole('link', { name: 'Open setup PR' })).toBeVisible();
    // The page is already about this repository, so the card does not name it again.
    await expect(tab(page).getByRole('heading', { level: 2, name: PUBLIC_REPO })).toHaveCount(0);
    expect(asked, 'it asked once').toHaveLength(1);
    expect(asked[0]!.get('installation_id')).toBe(target.installation_id);
    expect(asked[0]!.get('repository_id')).toBe(String(target.repository_id));
    await auditThePage(page, 'the AI Context tab');
  });

  test('somebody who may configure it is offered setup when there is none', async ({ page }) => {
    await apiSays(page, {
      draft: () => notFound,
      installations: [
        { id: 'some-other', target: 'other' },
        { id: target.installation_id, target: 'acme' },
      ],
    });
    await goto(page, here(), PUBLIC_REPO);
    await expect(tab(page).getByText('AI Context is not set up for this repository')).toBeVisible();
    // Setup opens on this repository's installation, with the repository selected.
    await expect(tab(page).getByRole('link', { name: 'Set up AI Context' })).toHaveAttribute(
      'href',
      `/kennel/ai-context/setup?installation_id=${encodeURIComponent(target.installation_id)}&repository_id=${target.repository_id}`,
    );
  });

  // The API answers the same to somebody who may not configure the installation
  // whether or not there is a record, so a 404 from it says nothing about whether
  // there is one, and neither may the tab.
  for (const status of [404, 403]) {
    test(`it does not say AI Context is not set up when a ${status} could mean it may not be told`, async ({
      page,
    }) => {
      await apiSays(page, {
        draft: () => ({
          status,
          json: { error: { code: status === 404 ? 'not_found' : 'forbidden', message: 'No' } },
        }),
        installations: [{ id: 'some-other', target: 'other' }],
      });
      await goto(page, here(), PUBLIC_REPO);
      await expect(
        tab(page).getByText('Zoomies cannot say whether AI Context is set up here'),
      ).toBeVisible();
      await expect(tab(page).getByText('is not set up')).toHaveCount(0);
      // Nothing they could not do.
      await expect(tab(page).getByRole('link', { name: 'Set up AI Context' })).toHaveCount(0);
      await expect(tab(page).getByRole('link', { name: 'Open AI Context' })).toHaveAttribute(
        'href',
        '/kennel/ai-context',
      );
    });
  }

  test('a failure to read it is an error that can be tried again, not a claim about the repository', async ({
    page,
  }) => {
    let failing = true;
    await apiSays(page, {
      draft: () =>
        failing
          ? { status: 500, json: { error: { code: 'internal', message: 'The database is busy' } } }
          : { json: record() },
    });
    await goto(page, here(), PUBLIC_REPO);
    await expect(tab(page).getByText('AI Context could not be read')).toBeVisible();
    await expect(tab(page).getByText('is not set up')).toHaveCount(0);
    failing = false;
    await tab(page)
      .getByRole('button', { name: /try again|retry/i })
      .click();
    await expect(tab(page).getByText('Awaiting merge')).toBeVisible();
    await expect(tab(page).getByText('AI Context could not be read')).toHaveCount(0);
  });

  test('a recheck updates the card where it is, on the tab as on the page', async ({ page }) => {
    await apiSays(page, { draft: () => ({ json: record() }) });
    let rechecks = 0;
    await page.route('**/api/v1/ai-context/repositories/ctx_demo1/recheck', (route) => {
      rechecks += 1;
      return route.fulfill({
        json: record({
          available: true,
          setup_state: undefined,
          setup_pr_url: undefined,
          freshness: {
            state: 'ready',
            desired_commit: 'abcdef1234567890',
            published_commit: 'abcdef1234567890',
            snapshot_id: 'snap_1',
            checked_at: new Date().toISOString(),
          },
        }),
      });
    });
    await goto(page, here(), PUBLIC_REPO);
    await tab(page).getByRole('button', { name: 'Recheck context' }).click();
    await expect(tab(page).getByText('Context verified')).toBeVisible();
    await expect(tab(page).getByText('Last verified commit')).toBeVisible();
    await expect(tab(page).getByRole('button', { name: 'Recheck context' })).toHaveCount(0);
    expect(rechecks).toBe(1);
  });

  // Moved by the address, as back, forward and a link do, and not by loading the page
  // afresh. The repository page empties itself when the repository changes, so the tab
  // is built again and not kept; what is checked is that the new one is asked about by
  // its own ID and the page was not reloaded.
  // Regenerate asks GitHub to run the workflow and answers with the record as it now
  // stands; the card has to show that answer, and neither action may be started while
  // the other is running, since each replaces the record the other is about.
  test('an action on the card holds the other until it is done, and shows its answer', async ({
    page,
  }) => {
    const stale = (over: Record<string, unknown> = {}) =>
      record({
        freshness: {
          state: 'stale',
          desired_commit: 'abcdef1234567890',
          published_commit: '1234567890abcdef',
          snapshot_id: 's',
          checked_at: new Date().toISOString(),
          ...over,
        },
      });
    await apiSays(page, { draft: () => ({ json: stale() }) });
    let release!: () => void;
    const gate = new Promise<void>((resolve) => (release = resolve));
    await page.route('**/api/v1/ai-context/repositories/ctx_demo1/regenerate', async (route) => {
      await gate;
      return route.fulfill({ json: stale({ failure: 'The new run has not finished yet' }) });
    });
    await page.route('**/api/v1/ai-context/repositories/ctx_demo1/recheck', async (route) => {
      await gate;
      return route.fulfill({ json: stale({ failure: 'Checked again, still behind' }) });
    });
    await goto(page, here(), PUBLIC_REPO);
    const regenerate = tab(page).getByRole('button', { name: 'Regenerate' });
    const recheck = tab(page).getByRole('button', { name: 'Recheck context' });
    await expect(regenerate).toBeEnabled();
    await expect(recheck).toBeEnabled();

    await regenerate.click();
    await expect(recheck, 'Recheck waits while Regenerate runs').toBeDisabled();
    // The running button gives its label to a spinner, so it is found by being busy.
    await expect(tab(page).locator('button[aria-busy="true"]')).toHaveAttribute(
      'aria-disabled',
      'true',
    );
    release();
    await expect(toast(page, 'success', 'Workflow started')).toBeVisible();
    await expect(tab(page).getByText('The new run has not finished yet')).toBeVisible();
    await expect(recheck).toBeEnabled();
    await expect(regenerate).toBeEnabled();
  });

  test('a recheck in flight holds Regenerate', async ({ page }) => {
    const stale = record({
      freshness: {
        state: 'stale',
        desired_commit: 'abcdef1234567890',
        published_commit: '1234567890abcdef',
        snapshot_id: 's',
        checked_at: new Date().toISOString(),
      },
    });
    await apiSays(page, { draft: () => ({ json: stale }) });
    let release!: () => void;
    const gate = new Promise<void>((resolve) => (release = resolve));
    await page.route('**/api/v1/ai-context/repositories/ctx_demo1/recheck', async (route) => {
      await gate;
      return route.fulfill({ json: stale });
    });
    await goto(page, here(), PUBLIC_REPO);
    await tab(page).getByRole('button', { name: 'Recheck context' }).click();
    await expect(
      tab(page).getByRole('button', { name: 'Regenerate' }),
      'Regenerate waits while Recheck runs',
    ).toBeDisabled();
    release();
    await expect(tab(page).getByRole('button', { name: 'Regenerate' })).toBeEnabled();
  });

  // What Zoomies will do about a failed run is said in a sentence, and "will" is only
  // said when it is going to: an automatic retry that has a time, not one that merely
  // has a time.
  for (const [says, retry, sentence] of [
    [
      'will start it again',
      { automatic: true, next_at: '2099-01-02T03:04:00Z', attempts_left: 2 },
      /Zoomies will start the workflow again from .* \(2 attempts left for this commit\)\./,
    ],
    [
      'will not, though a time is known',
      { automatic: false, next_at: '2099-01-02T03:04:00Z', attempts_left: 0 },
      /Zoomies will not start the workflow again by itself\. Use Regenerate once this is fixed\./,
    ],
  ] as const) {
    test(`a failed run says whether Zoomies ${says}`, async ({ page }) => {
      await apiSays(page, {
        draft: () => ({
          json: record({
            diagnosis: {
              title: 'GitHub refused to store the file',
              detail: 'The artifact quota is full.',
              fix: 'Free some storage.',
              action: 'repair',
              cause: 'artifact_quota',
              retry,
            },
          }),
        }),
      });
      await goto(page, here(), PUBLIC_REPO);
      await expect(
        tab(page).getByRole('group', { name: 'Why the last workflow run failed' }),
      ).toContainText(sentence);
    });
  }

  // The same card, listed: here it has to name its repository, because the list is
  // of them, and the tab above leaves the name off because its page is about one.
  test('the AI Context page names each repository on its card', async ({ page }) => {
    await page.route('**/api/v1/ai-context/repositories?*', (route) =>
      route.fulfill({
        json: { items: [record()], total: 1, limit: 50, offset: 0 },
      }),
    );
    await page.route('**/api/v1/ai-context/installations', (route) =>
      route.fulfill({ json: { items: [] } }),
    );
    await goto(page, '/kennel/ai-context', 'AI Context');
    await expect(page.getByRole('heading', { level: 2, name: PUBLIC_REPO })).toBeVisible();
    await expect(
      page.getByRole('region', { name: PUBLIC_REPO }).getByText('Source branch'),
    ).toBeVisible();
  });

  test('another repository is another question', async ({ page }) => {
    const listed = (await page.request
      .get('/api/v1/kennel/repositories?limit=100')
      .then((r) => r.json())) as { items: Array<{ id: string; name: string }> };
    const second = listed.items.find((r) => r.id !== target.id)!;
    const asked = await apiSays(page, {
      draft: (params) => ({
        json: record({
          full_name: `seen-for-${params.get('repository_id')}`,
          repository: {
            github_host: 'github.com',
            installation_id: params.get('installation_id'),
            repository_id: Number(params.get('repository_id')),
          },
        }),
      }),
    });
    await goto(page, here(), PUBLIC_REPO);
    await expect(tab(page).getByText('Source branch')).toBeVisible();
    await plantMarker(page);
    const before = asked.length;

    await page.evaluate((path) => {
      history.pushState({}, '', path);
      window.dispatchEvent(new PopStateEvent('popstate'));
    }, `/kennel/repositories/${second.id}/ai-context`);
    await expect(page.getByRole('heading', { level: 1, name: second.name })).toBeVisible();
    await expect.poll(() => asked.length, { message: 'it asked again' }).toBeGreaterThan(before);
    const ids = asked.map((p) => p.get('repository_id'));
    expect(new Set(ids).size, 'each repository was asked about by its own ID').toBe(2);
    await expectNoReload(page);
  });
});

/* -- the list of repositories, and which of them it shows ------------------------- */

// Kennel Club keeps a row for a repository for a quarter after its last job, and
// under the installation scope for every one the App can see, so the list shows
// only the repositories the fleet is serving unless a person says otherwise. The
// choice is theirs and stays in their browser. The fixture has no repository that
// is not being served, so the totals the page is told are the ones a fleet with
// quiet repositories would give, and what is checked is what the page asks and says.
test.describe('the list of repositories shows the ones being served', () => {
  const off = { 'kennel.enabled': false };
  const everyone = (page: Page) => page.getByRole('switch', { name: 'Active on Zoomies' });
  const scope = (page: Page) => page.locator('#main p.scope');

  /** What the page asked of the list, in order. */
  function listened(page: Page): URLSearchParams[] {
    const asked: URLSearchParams[] = [];
    page.on('request', (request) => {
      const url = new URL(request.url());
      if (url.pathname === '/api/v1/kennel/repositories') asked.push(url.searchParams);
    });
    return asked;
  }

  /**
   * The totals of a fleet with quiet repositories: the served ones are all but
   * `quiet` of the real rows, and `quiet` is how many have no recent job.
   */
  async function fleetWithQuietOnes(page: Page, quiet: number): Promise<void> {
    await page.route('**/api/v1/kennel/repositories?*', async (route) => {
      const params = new URL(route.request().url()).searchParams;
      const response = await route.fetch();
      const body = (await response.json()) as { items?: unknown[]; total: number };
      // Anything that is not a list -- an answer that Kennel Club is off, once a test
      // has finished with it -- is passed on as it came.
      if (!Array.isArray(body.items)) return route.fulfill({ response, json: body });
      if (params.get('active') === 'false')
        return route.fulfill({ response, json: { ...body, items: [], total: quiet } });
      if (params.get('active') === 'true') {
        const served = body.items.slice(0, Math.max(0, body.items.length - 1));
        return route.fulfill({ response, json: { ...body, items: served, total: served.length } });
      }
      return route.fulfill({ response });
    });
  }

  test.beforeEach(async ({ page }) => {
    await patchSettings(page, { 'kennel.enabled': true });
    await untilRead(page);
  });
  test.afterEach(async ({ page }) => patchSettings(page, off));

  test('it lists the repositories being served by default, and says what it leaves out', async ({
    page,
  }) => {
    await fleetWithQuietOnes(page, 4);
    const asked = listened(page);
    await goto(page, '/kennel/repositories', 'Repositories');
    await expect(everyone(page)).toHaveAttribute('aria-checked', 'true');
    await expect(scope(page)).toContainText('Showing repositories with a job in the last 30 days');
    await expect(scope(page)).toContainText('4 more repositories have no recent job.');
    await expect
      .poll(() => asked.some((p) => p.get('active') === 'true'), {
        message: 'the rows were asked for as active',
      })
      .toBe(true);
    // The count of what is left out is the same question, asked of the rest.
    const left = asked.find((p) => p.get('active') === 'false');
    expect(left, 'the hidden ones were counted').toBeTruthy();
    expect(left!.get('limit')).toBe('1');
    await auditThePage(page, 'the list, scoped');
  });

  test('a person who wants every repository says so, and it is remembered', async ({ page }) => {
    await fleetWithQuietOnes(page, 4);
    await goto(page, '/kennel/repositories', 'Repositories');
    // Settled: the served rows are in and the page has counted what it leaves out.
    const rows = dataRows(grid(page, 'Repositories'));
    await expect(rows).toHaveCount(2);
    await expect(scope(page)).toContainText('4 more repositories have no recent job.');
    const asked = listened(page);

    await everyone(page).click();
    await expect(everyone(page)).toHaveAttribute('aria-checked', 'false');
    await expect(scope(page)).toContainText('Showing every repository Kennel Club has a row for');
    await expect(scope(page)).not.toContainText('no recent job');
    // The rows are the other one as well, which is the point.
    await expect(rows).toHaveCount(3);
    // From the request that asked for every repository on, nothing asks for a scope.
    const unscoped = asked.findIndex((p) => p.get('active') === null && p.has('sort'));
    expect(unscoped, 'the list was asked again with no scope').toBeGreaterThanOrEqual(0);
    expect(
      asked.slice(unscoped).every((p) => p.get('active') === null),
      'nothing counts what is left out when nothing is',
    ).toBe(true);

    // Their browser remembers it.
    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'Repositories' })).toBeVisible();
    await expect(everyone(page)).toHaveAttribute('aria-checked', 'false');
    await everyone(page).click();
    await expect(everyone(page)).toHaveAttribute('aria-checked', 'true');
    await page.reload();
    await expect(everyone(page)).toHaveAttribute('aria-checked', 'true');
  });

  test('"Show them" looks at every repository once, and does not change what is remembered', async ({
    page,
  }) => {
    await fleetWithQuietOnes(page, 2);
    await goto(page, '/kennel/repositories', 'Repositories');
    await page.getByRole('button', { name: 'Show them' }).click();
    await expect(page).toHaveURL(/\/kennel\/repositories\?active=all$/);
    await expect(everyone(page)).toHaveAttribute('aria-checked', 'false');
    await expect(scope(page)).toContainText('Showing every repository');
    // The next visit, with no address to say otherwise, is the person's own choice.
    await goto(page, '/kennel/repositories', 'Repositories');
    await expect(everyone(page)).toHaveAttribute('aria-checked', 'true');
  });

  test('an address that says every repository wins, and turning the switch on lets go of it', async ({
    page,
  }) => {
    await fleetWithQuietOnes(page, 2);
    const asked = listened(page);
    await goto(page, '/kennel/repositories?active=all', 'Repositories');
    await expect(everyone(page)).toHaveAttribute('aria-checked', 'false');
    expect(asked.every((p) => p.get('active') === null)).toBe(true);
    await everyone(page).click();
    await expect(everyone(page)).toHaveAttribute('aria-checked', 'true');
    await expect(page).toHaveURL(/\/kennel\/repositories$/);
    await expect.poll(() => asked.some((p) => p.get('active') === 'true')).toBe(true);
  });

  test('when nothing is being served it says so, and where the quiet ones are', async ({
    page,
  }) => {
    await page.route('**/api/v1/kennel/repositories?*', async (route) => {
      const params = new URL(route.request().url()).searchParams;
      const response = await route.fetch();
      const body = (await response.json()) as { items?: unknown[]; total: number };
      if (!Array.isArray(body.items)) return route.fulfill({ response, json: body });
      return route.fulfill({
        response,
        json:
          params.get('active') === 'true'
            ? { ...body, items: [], total: 0 }
            : params.get('active') === 'false'
              ? { ...body, items: [], total: 3 }
              : body,
      });
    });
    await goto(page, '/kennel/repositories', 'Repositories');
    await expect(page.getByText('No repository has had a job lately')).toBeVisible();
    await expect(
      page.getByText('3 without a job in the last 30 days are not shown.'),
    ).toBeVisible();
    await page.getByRole('button', { name: 'Show them' }).last().click();
    await expect(page).toHaveURL(/active=all$/);
    await expect(page.getByRole('link', { name: PUBLIC_REPO })).toBeVisible();

    // A filter that finds no served repository says the others are not shown, too.
    await goto(page, '/kennel/repositories?severity=error', 'Repositories');
    await expect(page.getByText('No active repositories match those filters')).toBeVisible();
  });

  test('a fleet with every repository being served loses nothing and has nothing to say', async ({
    page,
  }) => {
    // The real API, no mock: every repository the fixture has was served a moment ago.
    const total = (await overview(page)).repositories;
    const served = (await page.request
      .get('/api/v1/kennel/repositories?active=true&limit=1')
      .then((r) => r.json())) as { total: number };
    const idle = (await page.request
      .get('/api/v1/kennel/repositories?active=false&limit=1')
      .then((r) => r.json())) as { total: number };
    expect(served.total, 'every repository is being served').toBe(total);
    expect(idle.total, 'none is not').toBe(0);

    await goto(page, '/kennel/repositories', 'Repositories');
    await expect(dataRows(grid(page, 'Repositories'))).toHaveCount(total);
    await expect(scope(page)).toContainText('Showing repositories with a job in the last 30 days');
    await expect(scope(page)).not.toContainText('no recent job');
  });

  // The Overview's counts and the problem in the drawer cover every repository, so
  // the list they open must too, or it would open on fewer rows than the number it
  // came from. The fixture cannot show fewer rows, so what is checked is the request.
  test('a link that counts every repository asks the list for every repository', async ({
    page,
  }) => {
    const asked = listened(page);
    await goto(page, '/kennel', 'Kennel Club');
    await page.getByRole('link', { name: /^Need attention: / }).click();
    await expect(page).toHaveURL(/\/kennel\/repositories\?state=attention&active=all$/);
    await expect(everyone(page)).toHaveAttribute('aria-checked', 'false');
    await expect.poll(() => asked.length).toBeGreaterThan(0);
    expect(
      asked.every((p) => p.get('active') === null),
      'no scope was asked for',
    ).toBe(true);
  });

  test('on a phone the switch is in view beside the filters, and nothing scrolls sideways', async ({
    page,
    isMobile,
  }) => {
    test.skip(!isMobile, 'the phone project checks the narrow widths');
    await fleetWithQuietOnes(page, 4);
    for (const width of [320, 360, 412]) {
      await page.setViewportSize({ width, height: 780 });
      await goto(page, '/kennel/repositories', 'Repositories');
      await expect(everyone(page), `the switch at ${width}px`).toBeInViewport({ ratio: 1 });
      await expect(scope(page), `the sentence at ${width}px`).toBeInViewport({ ratio: 1 });
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
        `the page does not scroll sideways at ${width}px`,
      ).toBe(true);
    }
  });
});
