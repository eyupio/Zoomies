/**
 * The screens an operator meets before there is anything to monitor.
 *
 * These run against their own server -- authentication on, an empty database --
 * because the main harness disables auth and seeds a fleet, which makes both of
 * these screens unreachable. That gap is why a dead focus jump, a swallowed
 * caps-lock warning and an Overview whose only button led into a refusal all
 * survived in an otherwise well-tested UI.
 *
 * The database is per-run and the bootstrap route closes the moment an account
 * exists, so the order here is load-bearing: the bootstrap tests come first and
 * the last of them creates the administrator the rest sign in with.
 */
import { test, expect } from '@playwright/test';

const ADMIN = { username: 'ada', password: 'correct horse battery staple' };

test.describe.configure({ mode: 'serial' });

/** Each test gets its own context, so anything after the bootstrap signs in. */
async function signIn(page: import('@playwright/test').Page): Promise<void> {
  await page.goto('/login');
  await page.fill('input[name="username"]', ADMIN.username);
  await page.fill('input[name="password"]', ADMIN.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();
}

test('the first screen says what it is, where it sits, and what follows', async ({ page }) => {
  await page.goto('/');

  const heading = page.getByRole('heading', { level: 1 });
  await expect(heading).toHaveText('Create the first administrator');
  // An operator arriving from `docker compose up` has no way to know whether
  // this account finishes setup or begins it.
  await expect(page.getByText('Step 1 of 4')).toBeVisible();
  await expect(page.getByText(/connect a GitHub App|connect GitHub/i)).toBeVisible();
  // The cursor starts where there is something to type.
  await expect(page.locator('input[name="username"]')).toBeFocused();
});

test('submitting an empty form moves focus to the field that is missing', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('input[name="username"]')).toBeFocused();

  // Pressing Enter on an empty form used to do nothing visible at all: the
  // focus jump queried the DOM for aria-invalid before Svelte had rendered it,
  // so it matched nothing and focus stayed on the button.
  await page.keyboard.press('Enter');

  await expect(page.getByText('Choose a username for the administrator.')).toBeVisible();
  await expect(page.locator('input[name="username"]')).toBeFocused();
});

test('an error replaces the hint rather than pushing the button out from under the pointer', async ({
  page,
}) => {
  await page.goto('/');
  const submit = page.getByRole('button', { name: 'Create the administrator' });
  const before = await submit.boundingBox();

  // Blurring an empty required field shows its error. When that error was
  // rendered *alongside* the hint it added a row, the button moved 26px down
  // between mousedown and mouseup, and the click was delivered to whatever
  // took its place -- a submit button that visibly did nothing.
  await page.locator('input[name="username"]').focus();
  await page.locator('input[name="username"]').blur();
  await expect(page.getByText('Choose a username for the administrator.')).toBeVisible();

  expect((await submit.boundingBox())?.y).toBe(before?.y);
});

test('a short password is refused with the counter still visible', async ({ page }) => {
  await page.goto('/');
  await page.fill('input[name="username"]', ADMIN.username);
  await page.fill('input[name="password"]', 'short');
  await page.getByRole('button', { name: 'Create the administrator' }).click();

  // The error is the counter: it says how many characters are still needed,
  // which is more use than the strength hint it replaces.
  await expect(page.getByText('A few more characters: 7 to go.')).toBeVisible();
  await expect(page.locator('input[name="password"]')).toBeFocused();
});

test('creating the administrator lands somewhere that names the next step', async ({ page }) => {
  await page.goto('/');
  await page.fill('input[name="username"]', ADMIN.username);
  await page.fill('input[name="password"]', ADMIN.password);
  await page.fill('input[name="confirm-password"]', ADMIN.password);
  await page.getByRole('button', { name: 'Create the administrator' }).click();

  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();
  // The whole point of the checklist: the first action offered is the one that
  // can actually be completed, and it is not "Create a pool".
  const checklist = page.getByRole('region', { name: 'Finish setting up' });
  await expect(checklist).toBeVisible();
  await expect(checklist.getByRole('link', { name: /Connect GitHub/ })).toBeVisible();
  // A pool is impossible without an installation, so it is not offered as one.
  await expect(checklist.getByText('After GitHub is connected.')).toBeVisible();
});

test('signing out and back in reports the three kinds of failure differently', async ({ page }) => {
  await page.goto('/');
  await page.context().clearCookies();
  await page.goto('/login');

  await expect(page.getByRole('heading', { name: 'Sign in', level: 1 })).toBeVisible();
  await page.fill('input[name="username"]', ADMIN.username);
  await page.fill('input[name="password"]', 'not the password');
  await page.getByRole('button', { name: 'Sign in' }).click();

  // The server's own words, read as a sentence: it sends them lowercase and
  // unpunctuated, because they are also read in a log line.
  const alert = page.getByRole('alert');
  await expect(alert).toContainText('Incorrect username or password.');
  // The password is cleared and the cursor put back in it: retyping is the
  // next thing to do whichever failure this was.
  await expect(page.locator('input[name="password"]')).toBeFocused();
  await expect(page.locator('input[name="password"]')).toHaveValue('');

  await page.fill('input[name="password"]', ADMIN.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();
});

test('the connect dialog refuses before the form when GitHub cannot reach here', async ({
  page,
}) => {
  await signIn(page);
  await page.goto('/installations');
  await page.getByRole('button', { name: 'Connect GitHub' }).first().click();

  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  // This fixture has no external URL, which is the state a fresh compose
  // deployment is in. The refusal used to come after the whole form had been
  // filled in, attached to the "Organisation" field.
  await expect(dialog.getByText('This controller has no external URL yet')).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Continue to GitHub' })).toBeDisabled();
});

test('the page behind the connect dialog is inert while it is open', async ({ page }) => {
  await signIn(page);
  await page.goto('/installations');
  await page.getByRole('button', { name: 'Connect GitHub' }).first().click();

  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();

  // The page behind a modal is inert, so a screen reader's virtual cursor
  // cannot read out of the dialog into the navigation underneath -- a focus
  // trap stops Tab and says nothing to the virtual cursor.
  const navInert = () =>
    page.evaluate(
      () => document.querySelector<HTMLElement>('nav[aria-label="Sections"]')?.inert === true,
    );
  expect(await navInert()).toBe(true);
  // The toaster is exempt: a toast raised while a dialog is open is usually
  // about the dialog, and a live region nobody can hear is not one.
  expect(
    await page.evaluate(() => document.querySelector<HTMLElement>('.toaster')?.inert === true),
  ).toBe(false);

  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  expect(await navInert()).toBe(false);
});
