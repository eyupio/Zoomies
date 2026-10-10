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
  await page.route('**/api/v1/assistant/personal/chat', async (route) => {
    asked.push(route.request().postDataJSON());
    await route.continue();
  });
  await goto(page, '/settings/assistant', 'Assistant');
  await expect(page.getByText(/Answers from .*Demo model \(built in\)/)).toBeVisible();

  const message = page.getByRole('textbox', { name: 'Message' });
  await message.fill('What is a runner?');
  await page.getByRole('button', { name: 'Send' }).click();
  const answers = page.getByRole('article', { name: 'Eli' });
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

/** A streamed answer, cut into small pieces the way a model sends it. */
function answerStream(markdown: string): string {
  const pieces = markdown.match(/[\s\S]{1,24}/g) ?? [];
  return [
    ...pieces.map((text) => `event: delta\ndata: ${JSON.stringify({ text })}\n\n`),
    `event: usage\ndata: ${JSON.stringify({ input_tokens: 5, output_tokens: 9 })}\n\n`,
    `event: done\ndata: ${JSON.stringify({ provider: 'Demo', model: 'demo' })}\n\n`,
  ].join('');
}

test('Eli says hello, offers things to ask, and a click asks one', async ({ page }) => {
  const asked: Array<{ messages: Array<{ role: string; content: string }> }> = [];
  await page.route('**/api/v1/assistant/personal/chat', async (route) => {
    asked.push(route.request().postDataJSON());
    await route.continue();
  });
  await goto(page, '/settings/assistant', 'Assistant');
  await expect(page.getByText("Hi, I'm Eli")).toBeVisible();
  const starters = page.getByRole('list', { name: 'Things to ask' }).getByRole('button');
  await expect(starters).toHaveCount(4);
  const first = (await starters.first().textContent()) ?? '';
  await starters.first().click();
  await expect(page.getByRole('article', { name: 'Eli' }).first()).toContainText(
    `The built-in model heard: ${first}`,
  );
  expect(asked[0]!.messages).toEqual([{ role: 'user', content: first }]);
  // Once there is a conversation the greeting makes way for it.
  await expect(page.getByText("Hi, I'm Eli")).toHaveCount(0);
});

test('an answer is drawn from its Markdown, and what is not Markdown is never run', async ({
  page,
}) => {
  await page.route('**/api/v1/assistant/personal/chat', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: answerStream(
        [
          '## Where to look',
          '- **The dashboard** shows runners',
          '- `GET /api/runners` gives the list',
          '',
          '| Pool | Idle |',
          '|:--|--:|',
          '| ci | 2 |',
          '',
          '```yaml',
          'runs-on: [self-hosted]',
          '```',
          '',
          'See [the docs](https://docs.zoomies.sh/labels) and [bad](javascript:window.__pwned=1).',
          '<script>window.__pwned = 1</script> <img src=x onerror="window.__pwned=1">',
        ].join('\n'),
      ),
    }),
  );
  await goto(page, '/settings/assistant', 'Assistant');
  await page.getByRole('textbox', { name: 'Message' }).fill('hello');
  await page.getByRole('button', { name: 'Send' }).click();

  const answer = page.getByRole('article', { name: 'Eli' });
  await expect(answer.getByRole('listitem')).toHaveCount(2);
  await expect(answer.locator('strong', { hasText: 'The dashboard' })).toBeVisible();
  await expect(answer.getByRole('table')).toBeVisible();
  await expect(answer.getByRole('cell', { name: '2' })).toBeVisible();
  await expect(answer.getByText('runs-on: [self-hosted]')).toBeVisible();
  await expect(answer.getByRole('button', { name: 'Copy code' })).toBeVisible();

  const link = answer.getByRole('link', { name: 'the docs' });
  await expect(link).toHaveAttribute('href', 'https://docs.zoomies.sh/labels');
  await expect(link).toHaveAttribute('target', '_blank');
  await expect(link).toHaveAttribute('rel', /noopener/);
  // A scheme that runs code is only words, and markup is only characters.
  await expect(answer.getByRole('link')).toHaveCount(1);
  await expect(answer).toContainText('[bad](javascript:window.__pwned=1)');
  await expect(answer).toContainText('<script>window.__pwned = 1</script>');
  expect(await page.evaluate(() => (window as unknown as { __pwned?: number }).__pwned)).toBe(
    undefined,
  );
  expect(await answer.locator('script, img').count()).toBe(0);
});

test('a failed answer can be asked again, and the second one replaces it', async ({ page }) => {
  let calls = 0;
  await page.route('**/api/v1/assistant/personal/chat', (route) => {
    calls++;
    if (calls === 1)
      return route.fulfill({
        status: 502,
        contentType: 'application/json',
        body: JSON.stringify({
          error: { code: 'assistant.provider_failed', message: 'the provider is down' },
        }),
      });
    return route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: answerStream('Back again.'),
    });
  });
  await goto(page, '/settings/assistant', 'Assistant');
  await page.getByRole('textbox', { name: 'Message' }).fill('hello');
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.getByRole('alert').filter({ hasText: 'the provider is down' })).toBeVisible();
  await page.getByRole('button', { name: 'Try again' }).click();
  await expect(page.getByRole('article', { name: 'Eli' })).toContainText('Back again.');
  await expect(page.getByRole('alert').filter({ hasText: 'the provider is down' })).toHaveCount(0);
  await expect(page.getByRole('article', { name: 'You' })).toHaveCount(1);
});

test('a refused question says why where the answer would have been', async ({ page }) => {
  await page.route('**/api/v1/assistant/personal/chat', (route) =>
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
  await page.route('**/api/v1/assistant/personal/providers/models', async (route) => {
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

test('Eli keeps drafts and history through layout changes, minimisation and navigation', async ({
  page,
  isMobile,
}) => {
  await goto(page, '/runners', 'Runners');
  await page.getByRole('button', { name: 'Ask Eli', exact: true }).click();
  const panel = page.getByRole('dialog', { name: 'Eli assistant' });
  await expect(panel).toBeVisible();
  const message = panel.getByRole('textbox', { name: 'Message' });
  await expect(message).toBeEnabled();
  await message.fill('Help me diagnose a runner failure');
  if (!isMobile) {
    await panel.getByRole('button', { name: 'Expanded', exact: true }).click();
    await panel.getByRole('button', { name: 'Place Eli on the left' }).click();
    await expect(message).toHaveValue('Help me diagnose a runner failure');
    await panel.getByRole('button', { name: 'Full screen' }).click();
    await expect(message).toHaveValue('Help me diagnose a runner failure');
    await panel.getByRole('button', { name: 'Restore panel' }).click();
  }
  await panel.getByRole('button', { name: 'Minimise Eli' }).click();
  await page.getByRole('button', { name: 'Ask Eli', exact: true }).click();
  await expect(message).toHaveValue('Help me diagnose a runner failure');
  await message.press('Enter');
  await expect(panel.getByRole('article', { name: 'Eli' })).toContainText(
    'The built-in model heard:',
  );
  await panel.getByRole('button', { name: 'Trace the failure' }).click();
  await expect(panel.getByRole('article', { name: 'You' })).toHaveCount(2);
  await expect(panel.getByRole('button', { name: 'Trace the failure' })).toHaveCount(0);
  await panel.getByRole('button', { name: 'Minimise Eli' }).click();
  await page.getByRole('link', { name: 'Pools', exact: true }).first().click();
  await page.getByRole('button', { name: 'Ask Eli', exact: true }).click();
  await expect(panel.getByRole('article', { name: 'You' })).toHaveCount(2);
  await panel.getByRole('button', { name: 'Minimise Eli' }).click();
  await expect(page.getByRole('button', { name: 'Ask Eli', exact: true })).toBeFocused();
});

test('a runner contextual action asks Eli with its displayed snapshot', async ({ page }) => {
  const asked: Array<{ messages: Array<{ role: string; content: string }> }> = [];
  await page.route('**/api/v1/assistant/personal/chat', async (route) => {
    asked.push(route.request().postDataJSON());
    await route.continue();
  });
  await goto(page, '/runners', 'Runners');
  await page.locator('main a[href^="/runners/"]').first().click();
  await page.getByTitle('Share these displayed details with Eli and ask for guidance').click();
  const panel = page.getByRole('dialog', { name: 'Eli assistant' });
  await expect(panel.getByRole('article', { name: 'Eli' })).toContainText(
    'The built-in model heard:',
  );
  expect(asked).toHaveLength(1);
  expect(asked[0]!.messages[0]!.content).toContain('Context shared from the runner UI:');
  expect(asked[0]!.messages[0]!.content).toContain('State:');
  await expect(panel.getByText(/^Shared runner:/)).toBeVisible();
});
