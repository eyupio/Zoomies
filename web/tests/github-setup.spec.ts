/**
 * The address GitHub sends an operator back to.
 *
 * Every GitHub App this controller builds names `/settings/github/setup` as its
 * redirect and setup URL, so the address outlives the release that created the
 * App: it is not a page anybody navigates to, it is a promise made to GitHub.
 * When the router did not know it, the operator's reward for confirming the App
 * on GitHub was "Page not found" with the single-use code in the address bar.
 */
import { expect, test } from '@playwright/test';
import { browserOverride, goto, pageHeading } from './support/fixtures';

test.use(browserOverride);

test('the App creation callback lands on Installations with the code in hand', async ({ page }) => {
  await goto(page, '/settings/github/setup?code=abc123&state=xyz789', 'Installations');

  // Forwarded, not merely rendered: the flow lives on the Installations page.
  await expect(page).toHaveURL(/\/installations\?/);
  await expect(page).toHaveURL(/code=abc123/);
  await expect(page).toHaveURL(/state=xyz789/);

  // The connect flow is open at the step that spends the code, with the code
  // already in it -- nobody should have to copy it out of the address bar.
  const dialog = page.getByRole('dialog', { name: 'Connect GitHub' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByLabel('Code from GitHub')).toHaveValue('abc123');

  // Going back must not land on the callback again and re-spend the code.
  await page.goBack();
  await expect(pageHeading(page, 'Page not found')).toBeHidden();
});

test('the callback with nothing on it is still a page, not a dead end', async ({ page }) => {
  await goto(page, '/settings/github/setup', 'Installations');
  await expect(page.getByRole('dialog', { name: 'Connect GitHub' })).toBeHidden();
});
