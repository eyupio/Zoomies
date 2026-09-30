import { expect, test } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);

test('private enrolment explains unavailable configuration in an auth-disabled demo', async ({
  page,
}) => {
  await goto(page, '/hosts/new', 'Add a host');
  await expect(page.getByRole('radio', { name: /Private connection/ })).toBeDisabled();
  await expect(page.getByText(/Private connections need authentication/)).toBeVisible();
  await expect(page.getByRole('radio', { name: /Direct connection/ })).toBeChecked();
});

// These responses isolate the hand-off UI from relay availability. The Go
// integration test runs the same protocol over a real local Tailcat relay.
test('private enrolment hides address configuration and sends the selected connection', async ({
  page,
}) => {
  await page.route('**/api/v1/meta', async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      json: { ...(await response.json()), tailcat_available: true },
    });
  });
  let requested: Record<string, unknown> | undefined;
  await page.route('**/api/v1/join-tokens', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    requested = route.request().postDataJSON();
    await route.fulfill({
      status: 201,
      json: {
        id: 'join_private_fixture',
        token: 'zoojoin_fixture_only',
        usable: true,
        expires_at: new Date(Date.now() + 900000).toISOString(),
        command:
          "curl -fsSL https://zoomies.sh/install.sh | sh -s -- --mode agent --controller 'tailcat://tc_fixture_only' --join-token 'zoojoin_fixture_only'",
        join_command:
          "zoomies agent join 'tailcat://tc_fixture_only' --token 'zoojoin_fixture_only'",
      },
    });
  });
  await page.route('**/api/v1/join-tokens/join_private_fixture', (route) =>
    route.fulfill({
      json: {
        id: 'join_private_fixture',
        usable: true,
        expires_at: new Date(Date.now() + 900000).toISOString(),
      },
    }),
  );
  await goto(page, '/hosts/new', 'Add a host');
  await page.getByRole('textbox', { name: 'Controller address' }).fill('not-a-reachable-address');
  await page.getByRole('radio', { name: /Private connection/ }).check();
  await expect(page.getByRole('textbox', { name: 'Controller address' })).toHaveCount(0);
  await page.getByRole('button', { name: 'Get the command' }).click();
  await expect(page.getByRole('heading', { name: 'Run this on the new host' })).toBeFocused();
  expect(requested?.connection).toBe('tailcat');
  expect(requested?.controller_url).toBeUndefined();
  await expect(
    page.getByText('Waiting for your private host to join.', { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(/This command contains private connection credentials/),
  ).toBeVisible();
  await expect(page.locator('pre').filter({ hasText: 'zoomies.sh/install.sh' })).toContainText(
    'tailcat://tc_fixture_only',
  );
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('private hosts retain a distinct badge alongside normal health and capacity', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  await expect(page.getByText('Tailcat host', { exact: true }).first()).toBeVisible();
  await expect(page.getByText(/via Tailcat/).first()).toBeVisible();
});

/**
 * The connection chooser is the one radio group that was styled by hand. Its
 * legend was a raw 600 weight at body size where every other group's is the
 * small, medium, muted label, and its card drew a focus ring on a mouse click,
 * which the ring's own rule (keyboard focus only) says nothing should.
 */
test('the connection chooser labels itself like every other group and rings a card only for the keyboard', async ({
  page,
}) => {
  await goto(page, '/hosts/new', 'Add a host');
  const legend = page.locator('legend', { hasText: 'How should this host connect?' });
  const weight = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--z-weight-medium').trim(),
  );
  // Soft, so the ring below is judged on the same run.
  await expect.soft(legend).toHaveCSS('font-weight', weight);
  // --z-text-xs, which computes to pixels.
  await expect.soft(legend).toHaveCSS('font-size', '12px');

  const card = page.locator('label.connection-choice', { hasText: 'Direct connection' });
  const radio = card.getByRole('radio');
  await radio.click();
  await expect(radio).toBeFocused();
  await expect(card).toHaveCSS('outline-style', 'none');

  // Away and back by keyboard: now the ring is earned.
  await page.keyboard.press('Tab');
  await page.keyboard.press('Shift+Tab');
  await expect(radio).toBeFocused();
  await expect(card).toHaveCSS('outline-style', 'solid');
});
