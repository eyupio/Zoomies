import { readFileSync } from 'node:fs';
import { expect, test, type APIRequestContext } from '@playwright/test';
import { browserOverride, chooseTheme, goto } from './support/fixtures';

test.use(browserOverride);

test('setup saves resumable drafts, retries only failures and never enables source access', async ({
  page,
  request,
}) => {
  let createdInstallation = '';
  try {
    const fake = JSON.parse(readFileSync('test-results/fakegithub.json', 'utf8'));
    const installations = await (await request.get('/api/v1/installations')).json();
    if (!installations.items.length) {
      const created = await request.post('/api/v1/installations', {
        data: {
          app_id: Number(fake.appId),
          installation_id: Number(fake.installationId),
          target: 'acme',
          target_type: 'org',
          api_base_url: fake.url,
          private_key: fake.privateKey,
        },
      });
      expect(created.status(), await created.text()).toBe(201);
      createdInstallation = (await created.json()).id;
    }
    await goto(page, '/ai-context', 'AI Context');
    await page.getByRole('link', { name: 'Enable repositories' }).first().click();
    await page.getByLabel('GitHub installation').selectOption({ label: 'acme' });
    await page.getByRole('checkbox', { name: 'acme/site', exact: true }).check();
    await page.getByRole('checkbox', { name: 'acme/widgets', exact: true }).check();
    const next = page.getByRole('button', { name: 'Next', exact: true });
    await next.click();
    await next.click();
    await expect(page.getByRole('radio', { name: /^Zoomies only/ })).toBeDisabled();
    await next.click();
    await page.getByLabel('Snapshots to retain').fill('0');
    await expect(page.getByText('Use a whole number from 1 to 100.')).toBeVisible();
    await expect(next).toBeDisabled();
    await page.getByLabel('Snapshots to retain').fill('7');
    const exclusions = page.getByRole('textbox', { name: 'Exclusions', exact: true });
    const originalExclusions = await exclusions.inputValue();
    await exclusions.fill(Array.from({ length: 101 }, (_, i) => `excluded-${i}`).join('\n'));
    await expect(exclusions).toHaveAttribute('aria-invalid', 'true');
    await expect(page.getByText('Use at most 100 exclusion patterns.')).toBeVisible();
    await expect(page.getByText('Use a whole number from 1 to 100.')).toHaveCount(0);
    await exclusions.fill(originalExclusions);
    await page.setViewportSize({ width: 375, height: 812 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
    await page.screenshot({ path: 'test-results/ai-context-mobile.png', fullPage: true });
    await next.click();
    await next.click();
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.screenshot({ path: 'test-results/ai-context-review.png', fullPage: true });
    let patches = 0;
    await page.route('**/api/v1/ai-context/repositories/*/config', async (route) => {
      if (route.request().method() !== 'PATCH') return route.fallback();
      patches++;
      if (patches === 2)
        return route.fulfill({
          status: 503,
          contentType: 'application/json',
          body: JSON.stringify({ error: { code: 'unavailable', message: 'Try again' } }),
        });
      return route.fallback();
    });
    let setupPosts = 0;
    await page.route('**/api/v1/ai-context/repositories/*/setup', async (route) => {
      const draftId = route.request().url().split('/repositories/')[1]?.split('/')[0] ?? '';
      const preview = {
        revision: 2,
        base_commit: 'a'.repeat(40),
        branch: `zoomies-ai-context-setup-${draftId}`,
        plan_hash: 'b'.repeat(64),
        files: [
          {
            path: '.github/workflows/zoomies-ai-context.yml',
            mode: '100644',
            previous_sha: '',
            content: 'name: Zoomies AI Context\n# <script>window.__contextInjected=true</script>\n',
          },
        ],
      };
      if (route.request().method() === 'POST') {
        setupPosts++;
        if (setupPosts === 2)
          return route.fulfill({
            status: 503,
            json: { error: { code: 'unavailable', message: 'Temporary publication failure' } },
          });
        return route.fulfill({
          json: {
            ...preview,
            setup: {
              repository_id: draftId,
              revision: 2,
              plan_hash: preview.plan_hash,
              state: 'awaiting_merge',
              pr_number: setupPosts,
              pr_url: `https://github.com/acme/site/pull/${setupPosts}`,
            },
          },
        });
      }
      return route.fulfill({ json: preview });
    });
    await page.getByRole('button', { name: 'Review setup changes' }).click();
    await expect(page.getByRole('heading', { name: 'Review repository changes' })).toBeFocused();
    await expect(page.getByText('Ready to review', { exact: true })).toHaveCount(1);
    await page.getByRole('button', { name: 'Retry failed drafts' }).click();
    await expect(page.getByText('Ready to review', { exact: true })).toHaveCount(2);
    expect(patches).toBe(3);
    const drafts = await (await request.get('/api/v1/ai-context/repositories')).json();
    expect(drafts.items).toHaveLength(2);
    for (const draft of drafts.items) {
      expect(draft.available).toBe(false);
      expect(draft.config.keep_snapshots).toBe(7);
    }
    expect(setupPosts).toBe(0);
    await page.setViewportSize({ width: 375, height: 812 });
    await page.locator('summary').first().click();
    await expect(
      page
        .getByRole('textbox', { name: 'Proposed .github/workflows/zoomies-ai-context.yml' })
        .first(),
    ).toHaveValue(/Zoomies AI Context/);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
    expect(await page.evaluate(() => '__contextInjected' in window)).toBe(false);
    await expect(page.getByRole('button', { name: 'Copy contents' }).first()).toBeVisible();
    expect(await page.locator('.results').evaluate((el) => getComputedStyle(el).overflowY)).toBe(
      'visible',
    );
    await page.screenshot({
      path: 'test-results/ai-context-managed-preview-mobile.png',
      fullPage: true,
    });
    await chooseTheme(page, 'dark');
    await page.screenshot({
      path: 'test-results/ai-context-review-mobile-dark.png',
      fullPage: true,
    });
    await chooseTheme(page, 'light');
    await page.getByRole('button', { name: 'Create setup PRs', exact: true }).click();
    await expect(page.getByRole('link', { name: /Open setup PR/ })).toHaveCount(1);
    await page.getByRole('button', { name: 'Create setup PRs', exact: true }).click();
    await expect(page.getByRole('link', { name: /Open setup PR/ })).toHaveCount(2);
    await expect(
      page.getByRole('heading', { name: 'Use AI Context with your assistant' }),
    ).toHaveCount(2);
    await expect(page.getByRole('button', { name: 'Copy AI instructions' })).toHaveCount(2);
    await expect(
      page.getByRole('link', { name: 'Manage MCP connections' }).first(),
    ).toHaveAttribute('href', '/settings/connections');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
    await expect(page.getByRole('link', { name: /Open setup PR/ }).first()).toHaveAttribute(
      'target',
      '_blank',
    );
    expect(setupPosts).toBe(3);
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.screenshot({
      path: 'test-results/ai-context-managed-results-desktop.png',
      fullPage: true,
    });
    await chooseTheme(page, 'dark');
    await page.screenshot({
      path: 'test-results/ai-context-results-desktop-dark.png',
      fullPage: true,
    });
    await chooseTheme(page, 'light');
    await page.unroute('**/api/v1/ai-context/repositories/*/setup');
    await page.getByRole('link', { name: 'View setup' }).first().click();
    await expect(page.getByLabel('GitHub installation')).toBeDisabled();
    await next.click();
    await next.click();
    await next.click();
    await expect(page.getByLabel('Snapshots to retain')).toHaveValue('7');
    await goto(page, '/ai-context', 'AI Context');
    await page.getByText('AI instructions and README badge', { exact: true }).first().click();
    await expect(page.getByRole('button', { name: 'Copy AI instructions' }).first()).toBeVisible();
    await expect(page.getByRole('button', { name: 'Copy badge Markdown' }).first()).toBeVisible();
    await page.setViewportSize({ width: 375, height: 812 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
    await page.screenshot({
      path: 'test-results/ai-context-instructions-mobile.png',
      fullPage: true,
    });
  } finally {
    // The connect journey uses this same controller and expects no existing
    // App. Remove only the installation this test created, even on failure.
    if (createdInstallation) {
      const removed = await request.delete(`/api/v1/installations/${createdInstallation}`);
      expect(removed.ok(), await removed.text()).toBe(true);
    }
  }
});

test('verification shows retained source separately from failure and rechecks one repository', async ({
  page,
}) => {
  const repository = {
    id: 'aic_review',
    full_name: 'acme/context',
    repository: { github_host: 'github.com', installation_id: 'installation', repository_id: 42 },
    config: { source_branch: 'main', destination: 'both', exclude: [], keep_snapshots: 3 },
    revision: 1,
    available: false,
    setup_state: 'awaiting_merge',
    setup_pr_url: 'https://github.com/acme/context/pull/1',
    created_at: '2026-10-03T10:00:00Z',
    updated_at: '2026-10-03T10:00:00Z',
    freshness: {
      state: 'stale',
      desired_commit: 'b'.repeat(40),
      published_commit: 'a'.repeat(40),
      snapshot_id: 'c'.repeat(64),
      checked_at: '2026-10-03T10:00:00Z',
      failure: 'Current generation could not be verified. The previous snapshot is retained.',
    },
  };
  await page.route('**/api/v1/ai-context/repositories?*', (route) =>
    route.fulfill({ json: { items: [repository], total: 1, limit: 50, offset: 0 } }),
  );
  await page.route('**/api/v1/ai-context/repositories/aic_review/recheck', (route) =>
    route.fulfill({
      json: {
        ...repository,
        available: true,
        freshness: {
          ...repository.freshness,
          state: 'ready',
          published_commit: 'b'.repeat(40),
          failure: '',
        },
      },
    }),
  );
  await goto(page, '/ai-context', 'AI Context');
  await chooseTheme(page, 'light');
  await expect(page.getByText('Generation out of date', { exact: true })).toBeVisible();
  await expect(page.getByText(repository.freshness.failure)).toBeVisible();
  await expect(page.getByText('aaaaaaaaaaaa', { exact: true })).toBeVisible();
  await expect(page.getByText('Last checked', { exact: true })).toBeVisible();
  await page.setViewportSize({ width: 375, height: 812 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: 'test-results/ai-context-freshness-mobile.png', fullPage: true });
  await page.getByRole('button', { name: 'Recheck context' }).click();
  await expect(page.getByText('Context verified', { exact: true })).toBeVisible();
  await expect(page.getByText(repository.freshness.failure)).toHaveCount(0);
  await expect(page.getByText('bbbbbbbbbbbb', { exact: true })).toBeVisible();
  await chooseTheme(page, 'dark');
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.screenshot({
    path: 'test-results/ai-context-freshness-desktop-dark.png',
    fullPage: true,
  });
});

test('a failed workflow run is explained on the card, with what Zoomies will do about it', async ({
  page,
}) => {
  const repository = {
    id: 'aic_failed',
    full_name: 'acme/private-repo',
    repository: { github_host: 'github.com', installation_id: 'installation', repository_id: 43 },
    config: { source_branch: 'main', destination: 'both', exclude: [], keep_snapshots: 3 },
    revision: 1,
    available: false,
    workflow_outdated: false,
    setup_state: 'awaiting_merge',
    setup_pr_url: 'https://github.com/acme/private-repo/pull/1',
    created_at: '2026-10-03T10:00:00Z',
    updated_at: '2026-10-03T10:00:00Z',
    freshness: {
      state: 'stale',
      desired_commit: 'b'.repeat(40),
      published_commit: 'a'.repeat(40),
      snapshot_id: 'c'.repeat(64),
      checked_at: '2026-10-06T07:00:00Z',
      failure: "GitHub's artifact storage is full. Delete old artifacts.",
    },
    diagnosis: {
      cause: 'artifact_quota',
      title: "GitHub's artifact storage is full",
      detail:
        'GitHub refused to store the file that carries the context from one job to the next. In this repository, 608 unexpired artifacts hold 3.5 GiB; 118 of them, named rea-graph-studio-windows-amd64-alpha, hold 3.5 GiB.',
      fix: 'Delete old artifacts, or give the workflows that upload the most a shorter retention-days.',
      commit: 'b'.repeat(40),
      run_url: 'https://github.com/acme/private-repo/actions/runs/1',
      failed_at: '2026-10-06T06:47:50Z',
      observed_at: '2026-10-06T07:00:00Z',
      retry: { automatic: true, next_at: '2026-10-06T12:47:50Z', attempts_left: 3 },
    },
  };
  const serve = (current: object) =>
    page.route('**/api/v1/ai-context/repositories?*', (route) =>
      route.fulfill({ json: { items: [current], total: 1, limit: 50, offset: 0 } }),
    );
  await serve(repository);
  await goto(page, '/ai-context', 'AI Context');
  await chooseTheme(page, 'light');

  const card = page.getByRole('group', { name: 'Why the last workflow run failed' });
  await expect(
    card.getByRole('heading', { name: "GitHub's artifact storage is full" }),
  ).toBeVisible();
  await expect(card).toContainText('118 of them, named rea-graph-studio-windows-amd64-alpha');
  await expect(card).toContainText('Delete old artifacts');
  await expect(card).toContainText('Zoomies will start the workflow again from');
  await expect(card).toContainText('3 attempts left for this commit');
  // The diagnosis replaces the generic failure rather than repeating it beside it.
  await expect(
    page.getByText("GitHub's artifact storage is full. Delete old artifacts."),
  ).toHaveCount(0);
  const run = card.getByRole('link', { name: 'Open the failed run' });
  await expect(run).toHaveAttribute('href', 'https://github.com/acme/private-repo/actions/runs/1');
  await expect(run).toHaveAttribute('target', '_blank');
  // Nothing a person can fix by repairing the workflow, so no button is promoted.
  await expect(page.getByRole('link', { name: 'Reinstall / repair' })).not.toHaveClass(/primary/);

  await page.setViewportSize({ width: 375, height: 812 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: 'test-results/ai-context-diagnosis-mobile.png', fullPage: true });

  // A failure only a new workflow can fix says so, and the control that does it
  // is the one that stands out. A run address that is not https is not linked.
  await page.unroute('**/api/v1/ai-context/repositories?*');
  await serve({
    ...repository,
    diagnosis: {
      ...repository.diagnosis,
      cause: 'oversized_file',
      title: 'The workflow refuses files over 1 MiB',
      detail: 'This workflow was written by an earlier Zoomies release.',
      fix: 'Use Reinstall / repair to move the workflow to the current generator.',
      action: 'repair',
      run_url: 'javascript:alert(1)',
      retry: { automatic: false, attempts_left: 0 },
    },
  });
  await goto(page, '/ai-context', 'AI Context');
  await expect(card).toContainText('Zoomies will not start the workflow again by itself');
  await expect(card.getByRole('link', { name: 'Open the failed run' })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Reinstall / repair' })).toHaveClass(/primary/);
  await chooseTheme(page, 'dark');
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.screenshot({
    path: 'test-results/ai-context-diagnosis-desktop-dark.png',
    fullPage: true,
  });
});

test('maintenance reviews amended settings, recovers publication and shows removal effects', async ({
  page,
}) => {
  const repository = {
    id: 'aic_maintenance',
    full_name: 'acme/context',
    repository: { github_host: 'github.com', installation_id: 'installation', repository_id: 42 },
    config: {
      source_branch: 'main',
      destination: 'both',
      exclude: ['private/**'],
      keep_snapshots: 3,
    },
    revision: 2,
    available: false,
    setup_state: 'awaiting_merge',
    setup_pr_url: 'https://github.com/acme/context/pull/1',
    created_at: '2026-10-03T10:00:00Z',
    updated_at: '2026-10-03T10:00:00Z',
  };
  await page.route('**/api/v1/ai-context/repositories?*', (route) =>
    route.fulfill({ json: { items: [repository], total: 1, limit: 50, offset: 0 } }),
  );
  await page.route('**/api/v1/ai-context/repositories/aic_maintenance', (route) =>
    route.fulfill({ json: repository }),
  );
  await page.route('**/api/v1/ai-context/repositories/aic_maintenance/setup', (route) =>
    route.fulfill({
      json: {
        revision: 2,
        base_commit: 'a'.repeat(40),
        branch: 'original',
        plan_hash: 'b'.repeat(64),
        files: [],
        setup: { state: 'awaiting_merge', pr_number: 1, pr_url: repository.setup_pr_url },
      },
    }),
  );
  let reviewBody: { mode?: string; config?: { keep_snapshots: number; exclude: string[] } } = {};
  let publications = 0;
  const preview = {
    revision: 2,
    base_commit: 'a'.repeat(40),
    branch: 'maintenance',
    plan_hash: 'c'.repeat(64),
    files: [
      {
        path: 'AGENTS.md',
        previous_sha: 'b'.repeat(40),
        mode: '100644',
        content: '<script>window.__contextInjected=true</script>',
      },
    ],
  };
  await page.route('**/api/v1/ai-context/repositories/aic_maintenance/maintenance', (route) => {
    reviewBody = route.request().postDataJSON();
    return route.fulfill({ json: preview });
  });
  await page.route(
    '**/api/v1/ai-context/repositories/aic_maintenance/maintenance/apply',
    (route) => {
      publications++;
      if (publications === 1)
        return route.fulfill({
          status: 503,
          json: {
            error: { code: 'unavailable', message: 'Lost GitHub response; retry this operation.' },
          },
        });
      expect(route.request().postDataJSON().plan_hash).toBe(preview.plan_hash);
      return route.fulfill({
        json: {
          ...preview,
          setup: {
            state: 'awaiting_merge',
            pr_number: 2,
            pr_url: 'https://github.com/acme/context/pull/2',
          },
        },
      });
    },
  );
  await goto(page, '/ai-context', 'AI Context');
  await expect(page.getByRole('link', { name: 'Reinstall / repair', exact: true })).toBeVisible();
  await page.getByRole('link', { name: 'Amend', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Amend AI Context', exact: true })).toBeVisible();
  await page.getByLabel('Snapshots to retain').fill('6');
  await page.getByLabel('Exclusions, one pattern per line').fill('private/**\nsecrets/**');
  await page.getByRole('button', { name: 'Review changes', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Review repository changes' })).toBeVisible();
  expect(reviewBody.mode).toBe('amend');
  expect(reviewBody.config?.keep_snapshots).toBe(6);
  expect(reviewBody.config?.exclude).toEqual(['private/**', 'secrets/**']);
  await page.getByText('Update AGENTS.md', { exact: true }).click();
  expect(await page.evaluate(() => '__contextInjected' in window)).toBe(false);
  await page.setViewportSize({ width: 375, height: 812 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: 'test-results/ai-context-maintenance-mobile.png', fullPage: true });
  await page.getByRole('button', { name: 'Create reviewed maintenance PR' }).click();
  await expect(
    page.getByText('Lost GitHub response; retry this operation.', { exact: true }),
  ).toBeVisible();
  await page.getByRole('button', { name: 'Create reviewed maintenance PR' }).click();
  await expect(page.getByRole('link', { name: 'Open maintenance PR' })).toHaveAttribute(
    'href',
    'https://github.com/acme/context/pull/2',
  );
  await goto(page, '/ai-context', 'AI Context');
  await page.getByRole('link', { name: 'Remove', exact: true }).click();
  await expect(page.getByText(/immediately revokes Zoomies source access/)).toBeVisible();
  await expect(page.getByText(/historical generated context branch/)).toBeVisible();
  await page.getByRole('button', { name: 'Review changes', exact: true }).click();
  await expect(
    page.getByRole('button', { name: 'Revoke access and create removal PR' }),
  ).toBeVisible();
  expect(reviewBody.mode).toBe('remove');
});

test('an administrator makes someone an installation owner without granting source access', async ({
  page,
  request,
}) => {
  let createdInstallation = '';
  let installation = '';
  let userId = '';
  try {
    const fake = JSON.parse(readFileSync('test-results/fakegithub.json', 'utf8'));
    const installations = await (await request.get('/api/v1/installations')).json();
    if (installations.items.length) {
      installation = installations.items[0].id;
    } else {
      const created = await request.post('/api/v1/installations', {
        data: {
          app_id: Number(fake.appId),
          installation_id: Number(fake.installationId),
          target: 'acme',
          target_type: 'org',
          api_base_url: fake.url,
          private_key: fake.privateKey,
        },
      });
      expect(created.status(), await created.text()).toBe(201);
      installation = createdInstallation = (await created.json()).id;
    }
    const user = await request.post('/api/v1/users', {
      data: { username: 'context-owner', password: 'correct horse battery staple', role: 'viewer' },
    });
    expect(user.status(), await user.text()).toBe(201);
    userId = (await user.json()).id;

    await goto(page, '/ai-context', 'AI Context');
    await page.getByText('Installation owners', { exact: true }).click();
    await page
      .getByRole('button', { name: /^Owners of / })
      .first()
      .click();
    const dialog = page.getByRole('dialog', { name: 'Installation owners' });
    await expect(dialog.getByText('0 owners selected')).toBeVisible();
    await expect(dialog.getByText(/does not grant source access/)).toBeVisible();
    await dialog.getByRole('checkbox', { name: /context-owner/ }).check();
    await expect(dialog.getByText('1 owner selected')).toBeVisible();
    await dialog.getByRole('button', { name: 'Save owners' }).click();
    await expect(dialog).toBeHidden();

    const owners = await (
      await request.get(`/api/v1/ai-context/installations/${installation}/owners`)
    ).json();
    expect(owners.user_ids).toEqual([userId]);
  } finally {
    if (installation) {
      await request.put(`/api/v1/ai-context/installations/${installation}/owners`, {
        data: { user_ids: [] },
      });
    }
    if (userId) await request.delete(`/api/v1/users/${userId}`);
    if (createdInstallation) await request.delete(`/api/v1/installations/${createdInstallation}`);
  }
});

test('a reader sees assistant notes marked AI-written and never rendered as HTML', async ({
  page,
}) => {
  const hostile =
    '# Plan\n\n<img src=x onerror="window.__noted=1"><script>window.__noted=2</script>';
  // An administrator who is also one of the repository's readers: the
  // configured card offers notes once the context is verified.
  await page.route('**/api/v1/ai-context/repositories?*', (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: 'aic_reader',
            full_name: 'acme/context',
            repository: {
              github_host: 'github.com',
              installation_id: 'installation',
              repository_id: 42,
            },
            config: { source_branch: 'main', destination: 'both', exclude: [], keep_snapshots: 3 },
            revision: 1,
            available: true,
            setup_state: 'complete',
            created_at: '2026-10-03T10:00:00Z',
            updated_at: '2026-10-03T10:00:00Z',
          },
        ],
        total: 1,
        limit: 50,
        offset: 0,
      },
    }),
  );
  const note = {
    id: 'aia_1',
    repository_id: 'aic_reader',
    slug: 'upgrade-plan',
    version: 2,
    kind: 'plan',
    title: 'Upgrade plan',
    source_commit: 'a'.repeat(40),
    author_name: 'Ada',
    via_kind: 'connection',
    via_name: 'Claude',
    created_at: '2026-10-03T10:00:00Z',
  };
  await page.route('**/api/v1/ai-context/source/aic_reader/notes?*', (route) =>
    route.fulfill({ json: { items: [{ ...note, versions: 2 }], total: 1, limit: 100, offset: 0 } }),
  );
  await page.route('**/api/v1/ai-context/source/aic_reader/notes/upgrade-plan', (route) =>
    route.fulfill({ json: { ...note, body: hostile } }),
  );
  await goto(page, '/ai-context', 'AI Context');
  await page.getByText('Assistant notes', { exact: true }).click();
  await expect(page.getByText('AI-written', { exact: true })).toBeVisible();
  await expect(page.getByText(/Version 2 by Ada\s+via Claude/)).toBeVisible();
  await page.getByRole('button', { name: 'Read' }).click();
  const body = page.getByLabel('Upgrade plan, as written');
  await expect(body).toHaveText(hostile);
  expect(await body.locator('img, script').count()).toBe(0);
  expect(await page.evaluate(() => (window as unknown as { __noted?: number }).__noted)).toBe(
    undefined,
  );
  await page.setViewportSize({ width: 375, height: 812 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: 'test-results/ai-context-notes-mobile.png', fullPage: true });
});

test('a slow note never appears under the title of the one read after it', async ({ page }) => {
  await page.route('**/api/v1/ai-context/repositories?*', (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: 'aic_reader',
            full_name: 'acme/context',
            repository: {
              github_host: 'github.com',
              installation_id: 'installation',
              repository_id: 42,
            },
            config: { source_branch: 'main', destination: 'both', exclude: [], keep_snapshots: 3 },
            revision: 1,
            available: true,
            setup_state: 'complete',
            created_at: '2026-10-03T10:00:00Z',
            updated_at: '2026-10-03T10:00:00Z',
          },
        ],
        total: 1,
        limit: 50,
        offset: 0,
      },
    }),
  );
  const note = (slug: string, title: string) => ({
    id: `aia_${slug}`,
    repository_id: 'aic_reader',
    slug,
    version: 1,
    kind: 'report',
    title,
    source_commit: 'a'.repeat(40),
    author_name: 'Ada',
    via_kind: 'user',
    created_at: '2026-10-03T10:00:00Z',
  });
  await page.route('**/api/v1/ai-context/source/aic_reader/notes?*', (route) =>
    route.fulfill({
      json: {
        items: [note('slow', 'Slow review'), note('fast', 'Fast plan')],
        total: 2,
        limit: 100,
        offset: 0,
      },
    }),
  );
  let releaseSlow = () => {};
  const slowHeld = new Promise<void>((resolve) => (releaseSlow = resolve));
  await page.route('**/api/v1/ai-context/source/aic_reader/notes/slow', async (route) => {
    await slowHeld;
    await route
      .fulfill({ json: { ...note('slow', 'Slow review'), body: 'SLOW BODY' } })
      .catch(() => {});
  });
  await page.route('**/api/v1/ai-context/source/aic_reader/notes/fast', (route) =>
    route.fulfill({ json: { ...note('fast', 'Fast plan'), body: 'FAST BODY' } }),
  );
  await goto(page, '/ai-context', 'AI Context');
  await page.getByText('Assistant notes', { exact: true }).click();
  const read = page.getByRole('button', { name: 'Read' });
  await read.first().click();
  await read.last().click();
  await expect(page.getByLabel('Fast plan, as written')).toHaveText('FAST BODY');
  releaseSlow();
  // Give the held reply every chance to land before checking it did not.
  await page.waitForTimeout(500);
  await expect(page.getByText('SLOW BODY')).toHaveCount(0);
  await expect(page.getByLabel('Fast plan, as written')).toHaveText('FAST BODY');
});

test('a workflow from an earlier release is badged and offers the repair first', async ({
  page,
}) => {
  const base = {
    repository: { github_host: 'github.com', installation_id: 'installation', repository_id: 42 },
    config: { source_branch: 'main', destination: 'both', exclude: [], keep_snapshots: 3 },
    revision: 1,
    available: true,
    setup_state: 'awaiting_merge',
    setup_pr_url: 'https://github.com/acme/context/pull/1',
    created_at: '2026-10-03T10:00:00Z',
    updated_at: '2026-10-03T10:00:00Z',
  };
  const items = [
    { ...base, id: 'aic_old', full_name: 'acme/old', workflow_outdated: true },
    { ...base, id: 'aic_new', full_name: 'acme/new', workflow_outdated: false },
  ];
  await page.route('**/api/v1/ai-context/repositories?*', (route) =>
    route.fulfill({ json: { items, total: 2, limit: 50, offset: 0 } }),
  );
  await goto(page, '/ai-context', 'AI Context');
  await expect(page.getByText('Workflow out of date', { exact: true })).toHaveCount(1);
  const old = page.getByRole('region', { name: 'acme/old' });
  await expect(old.getByText('Workflow out of date', { exact: true })).toBeVisible();
  await expect(old.getByRole('link', { name: 'Reinstall / repair' })).toHaveClass(/primary/);
  const current = page.getByRole('region', { name: 'acme/new' });
  await expect(current.getByRole('link', { name: 'Reinstall / repair' })).not.toHaveClass(
    /primary/,
  );
});

// -- setup opened from a repository's page -------------------------------------------
//
// A repository's AI Context tab links to setup with the repository's GitHub ID, and
// the wizard ticks it once the installation's list arrives. The address is anybody's
// to write, so the ID is only ever looked for in the list the server returned.

/**
 * Runs `use` with an installation to set up against: the one the controller has, or
 * one made here and removed after, because the connect journey uses this same
 * controller and expects no existing App.
 */
async function withInstallation<T>(
  request: APIRequestContext,
  use: (installation: string) => Promise<T>,
): Promise<T> {
  let created = '';
  try {
    const existing = await (await request.get('/api/v1/installations')).json();
    let installation: string = existing.items[0]?.id ?? '';
    if (!installation) {
      const fake = JSON.parse(readFileSync('test-results/fakegithub.json', 'utf8'));
      const response = await request.post('/api/v1/installations', {
        data: {
          app_id: Number(fake.appId),
          installation_id: Number(fake.installationId),
          target: 'acme',
          target_type: 'org',
          api_base_url: fake.url,
          private_key: fake.privateKey,
        },
      });
      expect(response.status(), await response.text()).toBe(201);
      installation = created = (await response.json()).id;
    }
    return await use(installation);
  } finally {
    if (created) await request.delete(`/api/v1/installations/${created}`);
  }
}

interface Discovered {
  id: number;
  full_name: string;
  archived: boolean;
}

/** The repositories the server lists for an installation, so no ID is written down here. */
async function discovered(request: APIRequestContext, installation: string): Promise<Discovered[]> {
  const response = await request.get(
    `/api/v1/ai-context/discovery?installation_id=${encodeURIComponent(installation)}`,
  );
  expect(response.ok(), await response.text()).toBe(true);
  return (await response.json()).repositories;
}

const setupAddress = (installation: string, repositoryId?: number | string) =>
  `/kennel/ai-context/setup?installation_id=${encodeURIComponent(installation)}` +
  (repositoryId === undefined ? '' : `&repository_id=${repositoryId}`);

test.describe("setup opened from a repository's page", () => {
  test('arrives with that repository chosen, says why, and leaves the choice to the person', async ({
    page,
    request,
  }) => {
    await withInstallation(request, async (installation) => {
      const widgets = (await discovered(request, installation)).find(
        (r) => r.full_name === 'acme/widgets',
      )!;
      await goto(page, setupAddress(installation, widgets.id), 'Enable repositories');

      const chosen = page.getByRole('checkbox', { name: 'acme/widgets', exact: true });
      await expect(chosen).toBeChecked();
      await expect(
        page.getByRole('checkbox', { name: 'acme/site', exact: true }),
      ).not.toBeChecked();
      await expect(
        page.getByText('acme/widgets is selected because you came from its page.'),
      ).toBeVisible();
      await expect(page.getByText('No repository is preselected.')).toHaveCount(0);
      await expect(page.getByText('1 of 20 repositories selected')).toBeVisible();
      const next = page.getByRole('button', { name: 'Next', exact: true });
      await expect(next).toBeEnabled();

      // It was ticked for them, once. Unticking it holds, and setup waits for a choice.
      await chosen.uncheck();
      await expect(page.getByText('0 of 20 repositories selected')).toBeVisible();
      await expect(next).toBeDisabled();
      await expect(chosen).not.toBeChecked();
    });
  });

  test('a link without a repository, or with one that is not an ID, selects nothing', async ({
    page,
    request,
  }) => {
    await withInstallation(request, async (installation) => {
      for (const address of [setupAddress(installation), setupAddress(installation, 'abc')]) {
        await goto(page, address, 'Enable repositories');
        await expect(page.getByText('No repository is preselected.')).toBeVisible();
        await expect(page.getByText('0 of 20 repositories selected')).toBeVisible();
        await expect(page.getByRole('checkbox', { checked: true })).toHaveCount(0);
        // A malformed one is ignored, not complained about: it names nothing to be missing.
        await expect(page.getByText('none is selected')).toHaveCount(0);
      }
    });
  });

  test('a repository the installation does not list selects nothing, and says so', async ({
    page,
    request,
  }) => {
    await withInstallation(request, async (installation) => {
      await goto(page, setupAddress(installation, 2147483647), 'Enable repositories');
      await expect(
        page.getByText(/not among this installation's repositories, so none is selected/),
      ).toBeVisible();
      await expect(page.getByRole('checkbox', { checked: true })).toHaveCount(0);
      await expect(page.getByText('0 of 20 repositories selected')).toBeVisible();
      await expect(page.getByText('No repository is preselected.')).toBeVisible();
    });
  });

  test('an archived repository cannot be set up, so it is not chosen, and the page says why', async ({
    page,
    request,
  }) => {
    await withInstallation(request, async (installation) => {
      const widgets = (await discovered(request, installation)).find(
        (r) => r.full_name === 'acme/widgets',
      )!;
      await page.route('**/api/v1/ai-context/discovery*', async (route) => {
        const response = await route.fetch();
        const body = await response.json();
        body.repositories = body.repositories.map((r: Discovered) =>
          r.id === widgets.id ? { ...r, archived: true } : r,
        );
        await route.fulfill({ response, json: body });
      });
      await goto(page, setupAddress(installation, widgets.id), 'Enable repositories');
      const archived = page.getByRole('checkbox', { name: 'acme/widgets', exact: true });
      await expect(archived).toBeDisabled();
      await expect(archived).not.toBeChecked();
      await expect(
        page.getByText('acme/widgets is archived and cannot be set up, so none is selected.'),
      ).toBeVisible();
      await expect(page.getByText('0 of 20 repositories selected')).toBeVisible();
    });
  });

  // Setup can load again while the person is part-way through it: a failed recheck
  // shows "Try again", and that reads the installation's list again. The repository
  // they came from was ticked for them once, and is not ticked over what they chose.
  test('loading again does not tick the repository over what the person chose', async ({
    page,
    request,
  }) => {
    await withInstallation(request, async (installation) => {
      const repositories = await discovered(request, installation);
      const widgets = repositories.find((r) => r.full_name === 'acme/widgets')!;
      await goto(page, setupAddress(installation, widgets.id), 'Enable repositories');
      await expect(page.getByRole('checkbox', { name: 'acme/widgets', exact: true })).toBeChecked();

      await page.getByRole('checkbox', { name: 'acme/widgets', exact: true }).uncheck();
      await page.getByRole('checkbox', { name: 'acme/site', exact: true }).check();
      await page.getByRole('button', { name: 'Next', exact: true }).click();

      let failing = true;
      await page.route('**/api/v1/ai-context/discovery*', (route) =>
        failing
          ? route.fulfill({
              status: 503,
              json: { error: { code: 'unavailable', message: 'Try again' } },
            })
          : route.fallback(),
      );
      await page.getByRole('button', { name: 'Recheck permissions' }).click();
      await expect(page.getByText('Setup could not be loaded')).toBeVisible();
      failing = false;
      await page.getByRole('button', { name: 'Try again' }).click();
      await expect(page.getByText('Setup could not be loaded')).toHaveCount(0);

      await page.getByRole('button', { name: 'Back', exact: true }).click();
      await expect(page.getByRole('checkbox', { name: 'acme/site', exact: true })).toBeChecked();
      await expect(
        page.getByRole('checkbox', { name: 'acme/widgets', exact: true }),
      ).not.toBeChecked();
      await expect(page.getByText('1 of 20 repositories selected')).toBeVisible();
    });
  });

  // A draft being resumed already says which repository it is for, and an address
  // that names a different one is not allowed to change that.
  test("resuming a draft keeps the draft's repository, whatever the address names", async ({
    page,
    request,
  }) => {
    await withInstallation(request, async (installation) => {
      const repositories = await discovered(request, installation);
      const widgets = repositories.find((r) => r.full_name === 'acme/widgets')!;
      const site = repositories.find((r) => r.full_name === 'acme/site')!;
      const draft = {
        id: 'ctx_resume',
        repository: {
          github_host: 'github.com',
          installation_id: installation,
          repository_id: widgets.id,
        },
        full_name: 'acme/widgets',
        instructions: '',
        badge_markdown: '',
        config: {
          source_branch: 'main',
          destination: 'repository',
          exclude: [],
          keep_snapshots: 5,
        },
        revision: 1,
        workflow_outdated: false,
        available: false,
        freshness: { state: 'not_configured' },
        setup_state: 'draft',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      };
      await page.route('**/api/v1/ai-context/repositories/ctx_resume**', (route) => {
        const path = new URL(route.request().url()).pathname;
        if (path.endsWith('/members')) return route.fulfill({ json: { user_ids: [] } });
        if (path.endsWith('/setup'))
          return route.fulfill({
            status: 404,
            json: { error: { code: 'not_found', message: 'Not found' } },
          });
        return route.fulfill({ json: draft });
      });
      await goto(
        page,
        `${setupAddress(installation, site.id)}&draft_id=ctx_resume`,
        'Enable repositories',
      );
      await expect(page.getByRole('checkbox', { name: 'acme/widgets', exact: true })).toBeChecked();
      await expect(
        page.getByRole('checkbox', { name: 'acme/site', exact: true }),
      ).not.toBeChecked();
      await expect(page.getByText('1 of 20 repositories selected')).toBeVisible();
      await expect(page.getByText('because you came from its page')).toHaveCount(0);
    });
  });
});
