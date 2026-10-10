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

/** Open Eli from the corner of whatever page this is. */
async function openEli(page: Page): Promise<void> {
  await page.getByRole('button', { name: 'Ask Eli' }).click();
  await expect(page.getByRole('dialog', { name: 'Eli assistant' })).toBeVisible();
}

/** Let Eli read the fleet through the demo model, or not. */
async function fleetAccess(page: Page, on: boolean): Promise<void> {
  const list = await (await page.request.get('/api/v1/assistant/providers')).json();
  const demo = list.items.find((p: { kind: string }) => p.kind === 'fake');
  const res = await page.request.patch(`/api/v1/assistant/providers/${demo.id}`, {
    data: { fleet_access: on },
  });
  expect(res.ok()).toBeTruthy();
}

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
  await openEli(page);
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
  await openEli(page);
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
  await openEli(page);
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
  await openEli(page);
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
  await openEli(page);
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

test('Ollama Cloud, OpenCode Zen and OpenCode Go are in the provider list and fill in their addresses', async ({
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

  await provider.selectOption('opencode-zen');
  await expect(address).toHaveValue('https://opencode.ai/zen/v1');
  await expect(name).toHaveValue('OpenCode Zen');
  await expect(form.getByText(/opencode\.ai\/auth/)).toBeVisible();

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

test('Eli is in the corner of every page, opens and closes from the keyboard, and keeps the conversation', async ({
  page,
}) => {
  await goto(page, '/pools', 'Pools');
  const launcher = page.getByRole('button', { name: 'Ask Eli' });
  await expect(launcher).toBeVisible();
  await expect(page.getByRole('dialog', { name: 'Eli assistant' })).toHaveCount(0);

  // Escape puts it away and gives the keyboard back to the button.
  await openEli(page);
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog', { name: 'Eli assistant' })).toHaveCount(0);
  await expect(launcher).toBeFocused();

  // E opens it from anywhere that is not a box to type in, with the box ready.
  await page.keyboard.press('e');
  await expect(page.getByRole('dialog', { name: 'Eli assistant' })).toBeVisible();
  const message = page.getByRole('textbox', { name: 'Message' });
  await expect(message).toBeFocused();
  await message.fill('Does this survive a page change?');
  await message.press('Enter');
  await expect(page.getByRole('article', { name: 'Eli' })).toContainText('Does this survive');

  // Going somewhere else does not end the conversation.
  await page.keyboard.press('Escape');
  await page.getByRole('link', { name: 'Runners', exact: true }).first().click();
  await expect(page).toHaveURL(/\/runners/);
  await page.getByRole('button', { name: 'Ask Eli' }).click();
  await expect(page.getByRole('article', { name: 'You' })).toContainText('Does this survive');
  await page.getByRole('button', { name: 'New conversation' }).click();
  await expect(page.getByRole('article', { name: 'You' })).toHaveCount(0);
});

test('Eli says it cannot see the fleet until an administrator allows it, and then looks', async ({
  page,
}) => {
  await fleetAccess(page, false);
  await goto(page, '/pools', 'Pools');
  await openEli(page);
  await expect(page.getByText(/Eli cannot see this fleet through Demo model/)).toBeVisible();
  await expect(page.getByRole('link', { name: 'Settings, Assistant' })).toBeVisible();

  try {
    await fleetAccess(page, true);
    await page.reload();
    await openEli(page);
    await expect(page.getByText(/Eli cannot see this fleet through/)).toHaveCount(0);
    // With the fleet to look at, the first things offered are about the fleet.
    await expect(
      page
        .getByRole('list', { name: 'Things to ask' })
        .getByText('How is the fleet doing right now?'),
    ).toBeVisible();
    const message = page.getByRole('textbox', { name: 'Message' });
    await message.fill('How is the fleet?');
    await message.press('Enter');
    const answer = page.getByRole('article', { name: 'Eli' });
    await expect(answer.getByRole('list', { name: 'What Eli looked at' })).toContainText(
      'Looked at the fleet at a glance',
    );
    // What the tool read is the controller's own figures, not something invented.
    await expect(answer).toContainText('queued_jobs');
  } finally {
    await fleetAccess(page, false);
  }
});

test('a provider says on its card that Eli may read the fleet through it, and the switch is on its form', async ({
  page,
}) => {
  await fleetAccess(page, false);
  await goto(page, '/settings/assistant', 'Assistant');
  const card = page.getByRole('article', { name: 'Demo model (built in)' });
  await expect(card.getByText('Eli can read the fleet')).toHaveCount(0);

  let modelListRequests = 0;
  page.on('request', (r) => {
    if (r.url().endsWith('/assistant/personal/providers/models')) modelListRequests++;
  });
  try {
    await card.getByRole('button', { name: 'Edit' }).click();
    const form = dialog(page, 'Edit Demo model (built in)');
    // What is typed into an Edit dialog stays, and the provider is asked for its
    // models a time or two, not on every change: the form once reset itself, and
    // asked again, whenever a field moved.
    await form.getByRole('textbox', { name: 'Name' }).fill('Demo model (renamed)');
    await page.waitForTimeout(800);
    await expect(form.getByRole('textbox', { name: 'Name' })).toHaveValue('Demo model (renamed)');
    expect(modelListRequests).toBeLessThanOrEqual(2);
    await form.getByRole('textbox', { name: 'Name' }).fill('Demo model (built in)');
    const switchEl = form.getByRole('switch', {
      name: 'Let Eli read this fleet through this provider',
    });
    await expect(switchEl).toHaveAttribute('aria-checked', 'false');
    await expect(form.getByText(/leaves it for a hosted one/)).toBeVisible();
    await switchEl.click();
    await form.getByRole('button', { name: 'Save' }).click();
    await expect(form).toBeHidden();
    await expect(card.getByText('Eli can read the fleet')).toBeVisible();
    // The Settings page and the corner agree without a reload.
    await openEli(page);
    await expect(page.getByText(/Eli cannot see this fleet through/)).toHaveCount(0);
  } finally {
    await fleetAccess(page, false);
  }
});

test('a provider card keeps its long kind label inside the card on a narrow screen', async ({
  page,
}) => {
  // The OpenAI-compatible label is the longest a card carries, and a badge is
  // one line, so it once ran out of the card on the right.
  // The list is read up front, the way the other specs read the API, and served
  // back with every provider claiming that kind: the page then draws the label
  // without a real OpenAI-compatible provider, which needs a key to be added.
  const list = await (await page.request.get('/api/v1/assistant/providers')).json();
  const items = list.items.map((p: Record<string, unknown>) => ({
    ...p,
    kind: 'openai_compatible',
    fleet_access: true,
  }));
  await page.route('**/api/v1/assistant/providers', (route) =>
    route.fulfill({ json: { ...list, items } }),
  );
  await page.setViewportSize({ width: 390, height: 800 });
  await goto(page, '/settings/assistant', 'Assistant');
  const card = page.getByRole('article').first();
  const badge = card.locator('.badge', { hasText: 'OpenAI-compatible server' });
  await expect(badge).toBeVisible();
  const [cardBox, badgeBox] = await Promise.all([card.boundingBox(), badge.boundingBox()]);
  expect(badgeBox!.x + badgeBox!.width).toBeLessThanOrEqual(cardBox!.x + cardBox!.width);
});

test('the card on the Assistant page opens Eli', async ({ page }) => {
  await goto(page, '/settings/assistant', 'Assistant');
  await expect(page.getByRole('heading', { name: 'Eli', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Open Eli' }).click();
  await expect(page.getByRole('dialog', { name: 'Eli assistant' })).toBeVisible();
});

test('a Claude subscription is added with no address and no key, tested, and Eli answers through it', async ({
  page,
}) => {
  await goto(page, '/settings/assistant', 'Assistant');
  await page.getByRole('button', { name: 'Add a provider' }).first().click();
  const form = dialog(page, 'Add a provider');
  await form.getByLabel('Provider', { exact: true }).selectOption('claude-code');

  // It has nothing to type but a name and a model: no address, no key, and nothing
  // to lend the fleet to. What it is, and whose it will be, is said before it is added.
  await expect(form.getByRole('textbox', { name: 'Base URL' })).toHaveCount(0);
  await expect(form.getByLabel('API key')).toHaveCount(0);
  await expect(form.getByRole('switch', { name: /read this fleet/ })).toHaveCount(0);
  await expect(form.getByText(/It is yours alone/)).toBeVisible();
  await expect(form.getByRole('textbox', { name: 'Name' })).toHaveValue('Claude (my subscription)');
  await expect(form.getByRole('combobox', { name: 'Model' })).toHaveValue('sonnet');

  try {
    await form.getByRole('button', { name: 'Test' }).click();
    await expect(form.getByText(/Answered as Claude Code 2\.1\.300/)).toBeVisible();
    await form.getByRole('button', { name: 'Add provider' }).click();
    await expect(form).toBeHidden();

    const card = page.getByRole('article', { name: 'Claude (my subscription)' });
    await expect(card.getByText('Your subscription')).toBeVisible();
    await expect(card.getByText('Key set, never shown')).toHaveCount(0);
    await expect(card.getByText('No key')).toHaveCount(0);
    await card.getByRole('button', { name: 'Set as default' }).click();
    await expect(card.getByRole('button', { name: 'Is the default' })).toBeVisible();

    await openEli(page);
    await expect(page.getByText(/Answers from .*Claude \(my subscription\)/)).toBeVisible();
    const message = page.getByRole('textbox', { name: 'Message' });
    await message.fill('hello from the spec');
    // The page names the provider it said would answer, so the controller's choice
    // cannot differ from what the panel promised.
    const asked = page.waitForRequest((r) => r.url().endsWith('/assistant/personal/chat'));
    await message.press('Enter');
    const providers = await (await page.request.get('/api/v1/assistant/providers')).json();
    const mine = providers.items.find((p: { kind: string }) => p.kind === 'claude_code');
    expect((await asked).postDataJSON().provider_id).toBe(mine.id);
    await expect(page.getByRole('article', { name: 'Eli' })).toContainText(
      'Claude Code heard: hello from the spec',
    );
    // It cannot be lent the fleet, so Eli says it cannot see it.
    await expect(page.getByText(/Eli cannot see this fleet through Claude/)).toBeVisible();
  } finally {
    // Put the demo model back as the answer, and take the subscription away, so the
    // specs after this one meet the fleet they were written for.
    const list = await (await page.request.get('/api/v1/assistant/providers')).json();
    const demo = list.items.find((p: { kind: string }) => p.kind === 'fake');
    await page.request.post(`/api/v1/assistant/providers/${demo.id}/default`, { data: {} });
    const claude = list.items.find((p: { kind: string }) => p.kind === 'claude_code');
    if (claude)
      await page.request.delete(`/api/v1/assistant/providers/${claude.id}`, {
        data: { name: claude.name },
      });
  }
});

test('somebody else’s Claude subscription is on the list, marked, and can be removed but not used', async ({
  page,
}) => {
  await page.route('**/api/v1/assistant/personal/providers', async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const res = await route.fetch();
    const body = await res.json();
    body.items.push({
      id: 'prov_alices',
      name: 'Alice’s Claude',
      kind: 'claude_code',
      base_url: '',
      model: 'sonnet',
      key_configured: false,
      enabled: true,
      is_default: false,
      local: false,
      fleet_access: false,
      subscription: true,
      owner: 'alice',
      owned_by_you: false,
      usable: false,
      created_at: '2026-10-09T10:00:00Z',
      updated_at: '2026-10-09T10:00:00Z',
    });
    await route.fulfill({ json: body });
  });
  await goto(page, '/settings/assistant', 'Assistant');

  const card = page.getByRole('article', { name: 'Alice’s Claude' });
  await expect(card.getByText('alice’s subscription')).toBeVisible();
  await expect(card.getByText(/Only alice can use, test or change this/)).toBeVisible();
  // Nothing that would spend her plan or change it is offered; taking the row away is.
  await expect(card.getByRole('button', { name: 'Test' })).toBeDisabled();
  await expect(card.getByRole('button', { name: 'Set as default' })).toBeDisabled();
  await expect(card.getByRole('button', { name: 'Edit' })).toBeDisabled();
  await expect(card.getByRole('button', { name: 'Remove' })).toBeEnabled();
});

for (const tool of [
  { preset: 'codex', name: 'ChatGPT (my plan)', heard: 'Codex heard: ', bin: 'Codex' },
  { preset: 'copilot', name: 'GitHub Copilot (my plan)', heard: 'Copilot heard: ', bin: 'Copilot' },
]) {
  test(`${tool.bin} is added with no address, no key and no list of models, and Eli answers through it`, async ({
    page,
  }) => {
    await goto(page, '/settings/assistant', 'Assistant');
    await page.getByRole('button', { name: 'Add a provider' }).first().click();
    const form = dialog(page, 'Add a provider');
    await form.getByLabel('Provider', { exact: true }).selectOption(tool.preset);

    // A name and, if wanted, a model: the tool chooses one when none is named.
    await expect(form.getByRole('textbox', { name: 'Base URL' })).toHaveCount(0);
    await expect(form.getByLabel('API key')).toHaveCount(0);
    await expect(form.getByRole('switch', { name: /read this fleet/ })).toHaveCount(0);
    await expect(form.getByRole('button', { name: /list of models/ })).toHaveCount(0);
    await expect(form.getByText(/It is yours alone/)).toBeVisible();
    await expect(form.getByRole('textbox', { name: 'Name' })).toHaveValue(tool.name);
    await expect(form.getByRole('textbox', { name: 'Model' })).toHaveValue('');
    await expect(form.getByText(/Leave this empty and/)).toBeVisible();

    try {
      await form.getByRole('button', { name: 'Test' }).click();
      await expect(form.getByText(/Answered as /)).toBeVisible();
      await form.getByRole('button', { name: 'Add provider' }).click();
      await expect(form).toBeHidden();

      const card = page.getByRole('article', { name: tool.name });
      await expect(card.getByText('Your subscription')).toBeVisible();
      await card.getByRole('button', { name: 'Set as default' }).click();
      await expect(card.getByRole('button', { name: 'Is the default' })).toBeVisible();

      await openEli(page);
      const message = page.getByRole('textbox', { name: 'Message' });
      await message.fill('hello from the spec');
      await message.press('Enter');
      const eli = page.getByRole('article', { name: 'Eli' });
      await expect(eli).toContainText(`${tool.heard}hello from the spec`);
      // What the agent's own tools did is not part of the answer.
      await expect(eli).not.toContainText('NOT-FOR-ELI');
      await expect(eli).toContainText('its default model');
    } finally {
      const list = await (await page.request.get('/api/v1/assistant/providers')).json();
      const demo = list.items.find((p: { kind: string }) => p.kind === 'fake');
      await page.request.post(`/api/v1/assistant/providers/${demo.id}/default`, { data: {} });
      const mine = list.items.find((p: { kind: string }) => p.kind === tool.preset);
      if (mine)
        await page.request.delete(`/api/v1/assistant/providers/${mine.id}`, {
          data: { name: mine.name },
        });
    }
  });
}

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

test('changing the personal default updates an already open Eli panel', async ({
  page,
  isMobile,
}) => {
  test.skip(isMobile, 'The mobile panel deliberately keeps the settings behind it inert.');
  await goto(page, '/settings/assistant', 'Assistant');
  const res = await page.request.post('/api/v1/assistant/personal/providers', {
    data: { name: 'Another model', kind: 'fake', model: 'demo' },
  });
  expect(res.ok()).toBeTruthy();
  const added = await res.json();
  try {
    await page.reload();
    await openEli(page);
    await page
      .getByRole('article', { name: 'Another model', exact: true })
      .getByRole('button', { name: 'Set as default' })
      .click();
    const panel = page.getByRole('dialog', { name: 'Eli assistant' });
    await expect(panel.getByText(/Answers from Another model/)).toBeVisible();
    const asked = page.waitForRequest((r) => r.url().endsWith('/assistant/personal/chat'));
    await panel.getByRole('textbox', { name: 'Message' }).fill('Use my new default');
    await panel.getByRole('button', { name: 'Send', exact: true }).click();
    expect((await asked).postDataJSON().provider_id).toBe(added.id);
  } finally {
    const providers = await (await page.request.get('/api/v1/assistant/providers')).json();
    const demo = providers.items.find((p: { name: string }) => p.name === 'Demo model (built in)');
    await page.request.post(`/api/v1/assistant/providers/${demo.id}/default`, { data: {} });
    await page.request.delete(`/api/v1/assistant/personal/providers/${added.id}`, {
      data: { name: added.name },
    });
  }
});
