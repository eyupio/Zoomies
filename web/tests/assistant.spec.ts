/**
 * Settings: Assistant. The demo ships with a built-in model, and this adds a
 * local one on top of it, so the three things the page promises get a test:
 * a private address is refused until its switch is on, a key is never shown
 * again once saved, and removing a provider takes its name typed out.
 */
import { expect, test, type Page } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);

const dialog = (page: Page, name: string | RegExp) => page.getByRole('dialog', { name });

async function allowPrivate(page: Page, on: boolean | null): Promise<void> {
  const res = await page.request.patch('/api/v1/settings', {
    data: { 'assistant.allow_private_provider': on },
  });
  expect(res.ok()).toBeTruthy();
}

test('the demo model is the default and its check reads on the card', async ({ page }) => {
  await goto(page, '/settings/assistant', 'Assistant');
  const card = page.getByRole('article', { name: 'Demo model (built in)' });
  await expect(card).toBeVisible();
  await expect(card.getByText('Default', { exact: true })).toBeVisible();
  await expect(card.getByText(/Answered as demo/)).toBeVisible();
});

test('a private address is refused until the switch is on, the key is never shown, and removal takes the name', async ({
  page,
}) => {
  await allowPrivate(page, false);
  await goto(page, '/settings/assistant', 'Assistant');
  await page.getByRole('button', { name: 'Add a provider' }).first().click();
  const form = dialog(page, 'Add a provider');
  await form.getByRole('textbox', { name: 'Name' }).fill('Local Ollama');
  await form.getByLabel('Kind').selectOption('openai_compatible');
  await form.getByRole('textbox', { name: 'Base URL' }).fill('http://127.0.0.1:11434/v1');
  await form.getByRole('textbox', { name: 'Model' }).fill('llama3');
  await form.getByLabel('API key').fill('sk-spec-not-a-real-key');
  await form.getByRole('button', { name: 'Add provider' }).click();
  await expect(form.getByText(/assistant\.allow_private_provider/)).toBeVisible();

  await allowPrivate(page, true);
  await form.getByRole('button', { name: 'Add provider' }).click();
  await expect(form).toBeHidden();

  const card = page.getByRole('article', { name: 'Local Ollama' });
  await expect(card).toBeVisible();
  await expect(card.getByText('Key set, never shown')).toBeVisible();
  await expect(card.getByText('Never tested')).toBeVisible();
  await expect(page.getByText('sk-spec-not-a-real-key')).toHaveCount(0);

  await card.getByRole('button', { name: 'Remove' }).click();
  const confirm = dialog(page, 'Remove provider');
  const typed = confirm.getByRole('textbox');
  await typed.fill('Local Olama');
  await expect(confirm.getByRole('button', { name: 'Remove' })).toBeDisabled();
  await typed.fill('Local Ollama');
  await confirm.getByRole('button', { name: 'Remove' }).click();
  await expect(confirm).toBeHidden();
  await expect(card).toHaveCount(0);

  await allowPrivate(page, null);
});
