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
  await form.getByLabel('Provider', { exact: true }).selectOption('openai-compatible');
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

/**
 * Asking the model. The demo's built-in model answers "The built-in model heard:"
 * and what it was asked, so a conversation can be followed end to end with no
 * account and no key.
 */
test('the demo model answers a question typed on the page, and the next one carries the first', async ({
  page,
}) => {
  const asked: Array<{ messages: Array<{ role: string; content: string }> }> = [];
  await page.route('**/api/v1/assistant/chat', async (route) => {
    asked.push(route.request().postDataJSON());
    await route.continue();
  });
  await goto(page, '/settings/assistant', 'Assistant');
  await expect(page.getByText(/Answers from .*Demo model \(built in\)/)).toBeVisible();

  const message = page.getByRole('textbox', { name: 'Message' });
  await message.fill('What is a runner?');
  await page.getByRole('button', { name: 'Send' }).click();
  const answers = page.getByRole('article', { name: 'Assistant' });
  await expect(answers.first()).toContainText('The built-in model heard: What is a runner?');
  await expect(answers.first()).toContainText('Demo model (built in)');
  await expect(page.getByRole('article', { name: 'You' }).first()).toContainText(
    'What is a runner?',
  );
  expect(asked[0]!.messages).toEqual([{ role: 'user', content: 'What is a runner?' }]);

  // Enter sends, and what was said before travels with the question.
  await message.fill('And an ephemeral one?');
  await message.press('Enter');
  await expect(answers.nth(1)).toContainText('The built-in model heard: And an ephemeral one?');
  expect(asked[1]!.messages.map((m) => m.role)).toEqual(['user', 'assistant', 'user']);
  expect(asked[1]!.messages[1]!.content).toBe('The built-in model heard: What is a runner?');

  await page.getByRole('button', { name: 'New conversation' }).click();
  await expect(page.getByRole('article', { name: 'You' })).toHaveCount(0);
});

test('a refused question says why where the answer would have been', async ({ page }) => {
  await page.route('**/api/v1/assistant/chat', (route) =>
    route.fulfill({
      status: 502,
      contentType: 'application/json',
      body: JSON.stringify({
        error: { code: 'assistant.provider_failed', message: 'the provider refused the key' },
      }),
    }),
  );
  await goto(page, '/settings/assistant', 'Assistant');
  await page.getByRole('textbox', { name: 'Message' }).fill('hello');
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(
    page.getByRole('alert').filter({ hasText: 'the provider refused the key' }),
  ).toBeVisible();
  // A turn that failed is not repeated to the model, and the box is ready again.
  await expect(page.getByRole('button', { name: 'Send' })).toBeDisabled();
  await page.getByRole('textbox', { name: 'Message' }).fill('again');
  await expect(page.getByRole('button', { name: 'Send' })).toBeEnabled();
});

test('Ollama Cloud and OpenCode Go are in the provider list and fill in their addresses', async ({
  page,
}) => {
  await goto(page, '/settings/assistant', 'Assistant');
  await page.getByRole('button', { name: 'Add a provider' }).first().click();
  const form = dialog(page, 'Add a provider');
  const provider = form.getByLabel('Provider', { exact: true });
  const address = form.getByRole('textbox', { name: 'Base URL' });
  const name = form.getByRole('textbox', { name: 'Name' });

  // Ollama Cloud is where a new provider starts.
  await expect(provider).toHaveValue('ollama-cloud');
  await expect(address).toHaveValue('https://ollama.com/v1');
  await expect(name).toHaveValue('Ollama Cloud');

  await provider.selectOption('opencode-go');
  await expect(address).toHaveValue('https://opencode.ai/zen/go/v1');
  await expect(name).toHaveValue('OpenCode Go');
  await expect(form.getByText(/opencode\.ai\/auth/)).toBeVisible();

  // What the person typed is theirs: choosing another provider does not replace it.
  await name.fill('My gateway');
  await address.fill('https://gateway.example.test/v1');
  await provider.selectOption('ollama-cloud');
  await expect(name).toHaveValue('My gateway');
  await expect(address).toHaveValue('https://gateway.example.test/v1');
});

test('the model is chosen from the provider’s own list once it has been loaded', async ({
  page,
}) => {
  const asked: Array<Record<string, unknown>> = [];
  await page.route('**/api/v1/assistant/providers/models', async (route) => {
    asked.push(route.request().postDataJSON());
    await route.fulfill({ json: { items: ['model-a', 'model-b'] } });
  });
  await goto(page, '/settings/assistant', 'Assistant');
  await page.getByRole('button', { name: 'Add a provider' }).first().click();
  const form = dialog(page, 'Add a provider');

  // Before it is loaded the model is typed.
  await expect(form.getByRole('textbox', { name: 'Model' })).toBeVisible();
  await form.getByLabel('API key').fill('sk-spec-not-a-real-key');
  await form.getByRole('button', { name: 'Load the list of models' }).click();
  const choice = form.getByLabel('Model');
  await expect(choice.locator('option')).toHaveText(['Choose a model', 'model-a', 'model-b']);
  await choice.selectOption('model-b');
  await expect(choice).toHaveValue('model-b');
  expect(asked[0]).toMatchObject({ kind: 'openai_compatible', base_url: 'https://ollama.com/v1' });

  // Changing provider forgets a list that was another provider's.
  await form.getByLabel('Provider', { exact: true }).selectOption('opencode-go');
  await expect(form.getByRole('textbox', { name: 'Model' })).toBeVisible();
});
