import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
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
    await page.getByRole('link', { name: 'Enable repositories' }).click();
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
    await expect(page.getByRole('heading', { name: 'Connect your assistant' })).toHaveCount(2);
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
