/**
 * Connecting an MCP client, as a person sees it.
 *
 * It runs on the first-run controller, which has authentication on and MCP
 * sign-in forced on for it. The client is played by the test: it registers
 * the way Claude does, the browser is sent to /oauth/authorize, and the
 * redirect back to claude.ai is caught before it leaves the machine, so what
 * is asserted is the page in between -- the consent screen -- and the
 * connections list that approving it fills.
 */
import { createHash, randomBytes } from 'node:crypto';
import { test, expect, type Page } from '@playwright/test';
import { browserOverride, setupToken } from './support/fixtures';

const ADMIN = { username: 'ada', password: 'correct horse battery staple' };
const CALLBACK = 'https://claude.ai/api/mcp/auth_callback';

test.use(browserOverride);
test.describe.configure({ mode: 'serial' });

async function signIn(page: Page): Promise<void> {
  const metaResponse = await page.request.get('/api/v1/meta');
  const meta = await metaResponse.json();
  if (meta.bootstrap_required) {
    const made = await page.request.post('/api/v1/auth/bootstrap', {
      data: { ...ADMIN, setup_token: setupToken() },
    });
    expect(made.ok()).toBeTruthy();
  }
  const res = await page.request.post('/api/v1/auth/login', {
    data: ADMIN,
    headers: { Origin: new URL(metaResponse.url()).origin },
  });
  expect(res.status(), 'the admin signs in with a password alone here').toBe(200);
}

/** Register a client and start a sign-in; returns what the token call needs. */
async function startSignIn(page: Page): Promise<{ clientId: string; verifier: string }> {
  const reg = await page.request.post('/oauth/register', {
    data: { client_name: 'Claude', redirect_uris: [CALLBACK], token_endpoint_auth_method: 'none' },
  });
  expect(reg.status()).toBe(201);
  const { client_id: clientId } = await reg.json();
  const verifier = randomBytes(48).toString('base64url');
  const challenge = createHash('sha256').update(verifier).digest('base64url');
  const q = new URLSearchParams({
    client_id: clientId,
    redirect_uri: CALLBACK,
    response_type: 'code',
    code_challenge: challenge,
    code_challenge_method: 'S256',
    state: 'from-the-test',
    scope: 'mcp:read mcp:operate',
  });
  await page.goto(`/oauth/authorize?${q}`);
  return { clientId, verifier };
}

test('the consent screen names the client, where it returns you, and the role', async ({
  page,
}) => {
  await signIn(page);
  const { clientId, verifier } = await startSignIn(page);

  await expect(page).toHaveURL(/\/oauth\/consent\?request=oar_/);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(/Connect Claude to Zoomies/);
  await expect(page.getByText('claude.ai', { exact: true })).toBeVisible();
  const operator = page.getByRole('radio', { name: /Operator/ });
  await expect(operator).toBeChecked();
  await page.getByRole('radio', { name: /Viewer/ }).check();

  // The redirect back to Claude is caught here rather than followed.
  let returned = '';
  await page.route('https://claude.ai/**', async (route) => {
    returned = route.request().url();
    await route.fulfill({ status: 200, contentType: 'text/html', body: '<p>back in Claude</p>' });
  });
  await page.getByRole('button', { name: 'Allow' }).click();
  await expect.poll(() => returned).toContain('code=');
  const back = new URL(returned);
  expect(back.searchParams.get('state')).toBe('from-the-test');
  expect(back.searchParams.get('iss')).toBeTruthy();

  const token = await page.request.post('/oauth/token', {
    form: {
      grant_type: 'authorization_code',
      code: back.searchParams.get('code') ?? '',
      redirect_uri: CALLBACK,
      code_verifier: verifier,
      client_id: clientId,
    },
  });
  expect(token.status()).toBe(200);
  expect((await token.json()).scope).toBe('mcp:read');
});

test('the connection is listed on the account and can be ended there', async ({ page }) => {
  await signIn(page);
  await page.goto('/settings/connections');
  const row = page.getByRole('row', { name: /Claude/ });
  await expect(row).toBeVisible();
  await expect(row.getByRole('cell', { name: 'Viewer' })).toBeVisible();

  // The real connection has no source grants. Opening its source controls
  // must show an honest empty state, never infer access from the Viewer role.
  await row.getByRole('button', { name: 'Source access' }).click();
  const sources = page.getByRole('dialog', { name: 'Source access' });
  await expect(sources).toBeVisible();
  await expect(sources.getByText(/No source repositories are available to you yet/)).toBeVisible();
  await page.screenshot({ path: 'test-results/source-access-desktop.png' });
  await page.setViewportSize({ width: 375, height: 812 });
  await expect(sources.getByRole('button', { name: 'Save source access' })).toBeVisible();
  expect(await sources.evaluate((el) => el.getBoundingClientRect().right)).toBeLessThanOrEqual(375);
  await page.screenshot({ path: 'test-results/source-access-mobile.png' });
  await sources.getByRole('button', { name: 'Cancel' }).click();
  await expect(row.getByRole('button', { name: 'Source access' })).toBeFocused();
  await page.setViewportSize({ width: 1280, height: 720 });

  // The API tests exercise live source membership; this paged fixture checks
  // that editing page one never revokes an existing choice from page two.
  const eligible = Array.from({ length: 61 }, (_, i) => ({
    id: `context-${i}`,
    full_name: `acme/repository-${String(i + 1).padStart(3, '0')}`,
  }));
  let saved: string[] | null = null;
  const sourcePattern = '**/api/v1/auth/mcp-connections/*/repositories*';
  await page.route(sourcePattern, async (route) => {
    if (route.request().method() === 'PUT') {
      saved = route.request().postDataJSON().repository_ids;
      await route.fulfill({ status: 204 });
      return;
    }
    const offset = Number(new URL(route.request().url()).searchParams.get('offset') ?? 0);
    await route.fulfill({
      json: {
        items: eligible.slice(offset, offset + 50),
        total: eligible.length,
        limit: 50,
        offset,
        selected_repository_ids: ['context-60'],
      },
    });
  });
  await row.getByRole('button', { name: 'Source access' }).click();
  await sources.getByRole('checkbox', { name: 'acme/repository-001' }).check();
  await sources.getByRole('button', { name: 'Next' }).click();
  await expect(sources.getByRole('checkbox', { name: 'acme/repository-061' })).toBeChecked();
  await sources.getByRole('checkbox', { name: 'acme/repository-051' }).check();
  await sources.getByRole('button', { name: 'Save source access' }).click();
  await expect(sources).not.toBeVisible();
  expect(saved).toEqual(['context-60', 'context-0', 'context-50']);
  await page.unroute(sourcePattern);

  await page.goto('/settings/mcp-clients');
  await expect(page.getByRole('heading', { name: 'Clients', exact: true })).toBeVisible();
  await expect(page.getByRole('cell', { name: 'Registered itself' }).first()).toBeVisible();

  await page.goto('/settings/connections');
  await page
    .getByRole('row', { name: /Claude/ })
    .getByRole('button', { name: 'Disconnect' })
    .click();
  await page.getByRole('dialog').getByRole('button', { name: 'Disconnect' }).click();
  await expect(page.getByText('No MCP connections')).toBeVisible();
});

test('a redirect the client never registered is refused on the controller, not followed', async ({
  page,
}) => {
  await signIn(page);
  const reg = await page.request.post('/oauth/register', {
    data: { client_name: 'Claude', redirect_uris: [CALLBACK] },
  });
  const { client_id: clientId } = await reg.json();
  const q = new URLSearchParams({
    client_id: clientId,
    redirect_uri: 'https://evil.example/steal',
    response_type: 'code',
    code_challenge: 'x'.repeat(43),
    code_challenge_method: 'S256',
  });
  await page.goto(`/oauth/authorize?${q}`);
  await expect(page).toHaveURL(/\/oauth\/consent\?error=invalid_request/);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(
    'This connection cannot go ahead',
  );
  await expect(page.getByRole('alert')).toContainText('never registered');
});

// The consent page is on the controller's own domain, so a sentence taken
// from its address would be a sentence anybody could put there.
test('the refusal page writes its own text, never what the address says', async ({ page }) => {
  const q = new URLSearchParams({
    error: 'invalid_request',
    error_description: 'Your account is locked. Call 555-0100 to unlock it.',
  });
  await page.goto(`/oauth/consent?${q}`);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(
    'This connection cannot go ahead',
  );
  await expect(page.getByRole('alert')).toContainText('never registered');
  await expect(page.getByText('555-0100')).toHaveCount(0);
});

// Rotating a secret breaks whatever still holds the old one at its next
// refresh, so a stray click on the row must not be enough to do it.
test('rotating a client secret asks first, then shows the new secret once', async ({ page }) => {
  await signIn(page);
  await page.goto('/settings/mcp-clients');
  await page.getByRole('button', { name: 'Create a client' }).click();
  const form = page.getByRole('dialog', { name: 'Create an MCP client' });
  await form.getByLabel('Name').fill('Rotation test');
  await form.getByRole('button', { name: 'Create client' }).click();
  const made = page.getByRole('dialog', { name: 'Rotation test' });
  const first =
    (
      await made
        .getByText(/^zoocs_/)
        .first()
        .textContent()
    )?.trim() ?? '';
  expect(first, 'the new client shows its secret once').toMatch(/^zoocs_/);
  // A stray click on the scrim would lose the only copy of it.
  await expect(made.getByRole('button', { name: 'Close' })).toHaveCount(0);
  await page.mouse.click(5, 5);
  await expect(made).toBeVisible();
  await made.getByRole('button', { name: 'Done' }).click();

  const row = page.getByRole('row', { name: /Rotation test/ });
  await row.getByRole('button', { name: 'Rotate secret' }).click();
  const confirm = page.getByRole('dialog', { name: 'Rotate secret' });
  await expect(confirm).toContainText('The old one stops working now');
  await confirm.getByRole('button', { name: 'Cancel' }).click();
  await expect(confirm).toBeHidden();

  await row.getByRole('button', { name: 'Rotate secret' }).click();
  await page
    .getByRole('dialog', { name: 'Rotate secret' })
    .getByRole('button', { name: 'Rotate' })
    .click();
  const shown = page.getByRole('dialog', { name: 'Rotation test' });
  await expect(shown).toBeVisible();
  await expect(shown).toContainText('zoocs_');
  await expect(shown).not.toContainText(first);
});
