import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

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
    await page.getByLabel('Snapshots to retain').fill('7');
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
    await expect(page.getByText('Draft saved', { exact: true })).toHaveCount(1);
    await page.getByRole('button', { name: 'Retry failed drafts' }).click();
    await expect(page.getByText('Draft saved', { exact: true })).toHaveCount(2);
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
    await page.screenshot({
      path: 'test-results/ai-context-managed-preview-mobile.png',
      fullPage: true,
    });
    await page.getByRole('button', { name: 'Create setup PRs', exact: true }).click();
    await expect(page.getByRole('link', { name: 'Open setup PR' })).toHaveCount(1);
    await page.getByRole('button', { name: 'Create setup PRs', exact: true }).click();
    await expect(page.getByRole('link', { name: 'Open setup PR' })).toHaveCount(2);
    expect(setupPosts).toBe(3);
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.screenshot({
      path: 'test-results/ai-context-managed-results-desktop.png',
      fullPage: true,
    });
    await page.unroute('**/api/v1/ai-context/repositories/*/setup');
    await page.getByRole('link', { name: 'Resume draft' }).first().click();
    await expect(page.getByLabel('GitHub installation')).toBeDisabled();
    await next.click();
    await next.click();
    await next.click();
    await expect(page.getByLabel('Snapshots to retain')).toHaveValue('7');
  } finally {
    // The connect journey uses this same controller and expects no existing
    // App. Remove only the installation this test created, even on failure.
    if (createdInstallation) {
      const removed = await request.delete(`/api/v1/installations/${createdInstallation}`);
      expect(removed.ok(), await removed.text()).toBe(true);
    }
  }
});
