/**
 * Two-step verification, from turning it on to signing in with it.
 *
 * It runs on the first-run controller -- authentication on -- after
 * first-run.spec.ts has created the account it signs in with, because under
 * the shared harness authentication is off and there is no sign-in to add a
 * step to. The steps are serial: each one leaves the account in the state the
 * next one starts from, and the last turns two-step off again.
 *
 * The authenticator app is RFC 6238 written out below with node:crypto, so
 * the test types the code a phone would show rather than one the server told
 * it.
 */
import { createHmac } from 'node:crypto';
import { test, expect, type Page } from '@playwright/test';
import { browserOverride, setupToken } from './support/fixtures';

const ADMIN = { username: 'ada', password: 'correct horse battery staple' };

test.use(browserOverride);
test.describe.configure({ mode: 'serial' });

/** The six digits an authenticator app shows for this key at this moment. */
function totp(secret: string, at = Date.now()): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = '';
  for (const ch of secret.replace(/\s+/g, '')) {
    bits += alphabet.indexOf(ch).toString(2).padStart(5, '0');
  }
  const key = Buffer.alloc(Math.floor(bits.length / 8));
  for (let i = 0; i < key.length; i++) key[i] = parseInt(bits.slice(i * 8, i * 8 + 8), 2);
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 1000 / 30)));
  const mac = createHmac('sha1', key).update(counter).digest();
  const offset = mac.readUInt8(mac.length - 1) & 0x0f;
  const value = (mac.readUInt32BE(offset) & 0x7fffffff) % 1_000_000;
  return String(value).padStart(6, '0');
}

let secret = '';
let recovery: string[] = [];

/** One of the recovery codes the first test was shown. */
function spare(i: number): string {
  const code = recovery[i];
  if (!code) throw new Error(`there is no recovery code ${i}; did the first test run?`);
  return code;
}

/**
 * The account first-run.spec.ts makes. Created here when this file runs on
 * its own, so it does not depend on being run after the other one.
 */
async function ensureAccount(page: Page): Promise<void> {
  const meta = await (await page.request.get('/api/v1/meta')).json();
  if (!meta.bootstrap_required) return;
  const res = await page.request.post('/api/v1/auth/bootstrap', {
    data: { ...ADMIN, setup_token: setupToken() },
  });
  expect(res.ok()).toBeTruthy();
}

async function password(page: Page): Promise<void> {
  await page.context().clearCookies();
  await page.goto('/login');
  await page.fill('input[name="username"]', ADMIN.username);
  await page.fill('input[name="password"]', ADMIN.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
}

test('turning it on shows a QR code, the key, and ten recovery codes once', async ({ page }) => {
  await ensureAccount(page);
  await password(page);
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();

  await page.goto('/settings/account');
  await page.getByRole('button', { name: 'Turn on two-step verification' }).click();
  const dialog = page.getByRole('dialog', { name: 'Turn on two-step verification' });
  await expect(dialog.getByRole('img', { name: /QR code/ })).toBeVisible();

  await dialog.getByRole('button', { name: 'Enter the key instead' }).click();
  // The key is the paragraph labelled "Your key": nothing in the accessibility
  // tree tells it apart from the hint beside it except that label.
  secret = ((await dialog.getByLabel('Your key').textContent()) ?? '').replace(/\s+/g, '');
  expect(secret).toMatch(/^[A-Z2-7]{32}$/);

  // A wrong first code turns nothing on.
  await dialog.getByLabel('Six-digit code').fill('000000');
  await dialog.getByRole('button', { name: 'Turn on' }).click();
  await expect(dialog.getByText(/not right, or has already been used/)).toBeVisible();

  await dialog.getByLabel('Six-digit code').fill(totp(secret));
  await dialog.getByRole('button', { name: 'Turn on' }).click();

  const done = page.getByRole('dialog', { name: 'Two-step verification is on' });
  const codes = done.getByRole('list', { name: 'Recovery codes' }).getByRole('listitem');
  await expect(codes).toHaveCount(10);
  recovery = (await codes.allTextContents()).map((c) => c.trim());
  expect(spare(0)).toMatch(/^[a-z2-9]{5}-[a-z2-9]{5}$/);
  await done.getByRole('button', { name: 'I have saved them' }).click();

  await expect(page.getByText('10 of 10 recovery codes left.')).toBeVisible();
});

test('the password is not enough: the code step comes next and a wrong code stays on it', async ({
  page,
}) => {
  await password(page);
  await expect(page.getByRole('heading', { name: 'Enter your code', level: 1 })).toBeVisible();
  const field = page.locator('input[name="code"]');
  await expect(field).toBeFocused();

  // Nothing behind the sign-in answers yet.
  expect((await page.request.get('/api/v1/auth/session')).status()).toBe(401);

  await field.fill('000000');
  await page.getByRole('button', { name: 'Verify and sign in' }).click();
  await expect(page.getByText(/not right, or has already been used/)).toBeVisible();
  await expect(field).toBeFocused();
  await expect(field).toHaveValue('');

  // The code that turned it on used this step; the phone's next one is the
  // one a person would be typing now.
  await field.fill(totp(secret, Date.now() + 30_000));
  await page.getByRole('button', { name: 'Verify and sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();
});

test('the code step fits a phone in both themes', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme });
    await password(page);
    await expect(page.getByRole('heading', { name: 'Enter your code', level: 1 })).toBeVisible();
    const { scrollWidth, clientWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(
      scrollWidth,
      `the code step scrolls sideways in the ${colorScheme} theme`,
    ).toBeLessThanOrEqual(clientWidth);
    await expect(page.getByRole('button', { name: 'Verify and sign in' })).toBeInViewport();
  }
});

test('a recovery code signs in once and says how many are left', async ({ page }) => {
  await password(page);
  await page.locator('input[name="code"]').fill(spare(0).toUpperCase());
  await page.getByRole('button', { name: 'Verify and sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();
  await expect(page.getByText(/9 are left/)).toBeVisible();

  await password(page);
  await page.locator('input[name="code"]').fill(spare(0));
  await page.getByRole('button', { name: 'Verify and sign in' }).click();
  await expect(page.getByText(/not right, or has already been used/)).toBeVisible();
});

test('turning it off takes the password and a code', async ({ page }) => {
  await password(page);
  await page.locator('input[name="code"]').fill(spare(1));
  await page.getByRole('button', { name: 'Verify and sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();

  // An administrator can see who has it on, and has the reset to hand.
  await page.goto('/settings/users');
  await expect(page.getByText('Two-step on')).toBeVisible();
  await page.getByRole('button', { name: `Actions for ${ADMIN.username}` }).click();
  await expect(page.getByRole('menuitem', { name: 'Reset two-step verification' })).toBeEnabled();
  await page.keyboard.press('Escape');

  await page.goto('/settings/account');
  await page.getByRole('button', { name: 'Turn off' }).click();
  const dialog = page.getByRole('dialog', { name: 'Turn off two-step verification' });
  await dialog.getByLabel('Password').fill('not the password');
  await dialog.getByLabel('Code from your app').fill(spare(2));
  await dialog.getByRole('button', { name: 'Turn off' }).click();
  await expect(dialog.getByText('the current password is not correct')).toBeVisible();

  await dialog.getByLabel('Password').fill(ADMIN.password);
  await dialog.getByLabel('Code from your app').fill(spare(3));
  await dialog.getByRole('button', { name: 'Turn off' }).click();
  await expect(page.getByRole('button', { name: 'Turn on two-step verification' })).toBeVisible();

  await password(page);
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();
});
