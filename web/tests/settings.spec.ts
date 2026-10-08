/**
 * Settings: the accounts and the API tokens, and the section they live in.
 *
 * These are the two places in the product where a mistake is expensive and
 * irreversible, so the protections are the point: deleting an account demands
 * its name typed out, a minted token is shown once and says so, and a revoked
 * token stops working immediately. None of it had a test.
 *
 * The seeded fixture has neither users nor tokens -- the demo seed refuses to
 * run at all on an instance that already has an account -- so each test makes
 * what it needs and takes it away again.
 */
import { randomBytes } from 'node:crypto';
import { expect, test, type Page } from '@playwright/test';
import { browserOverride, goto, openAccountMenu, pageHeading } from './support/fixtures';

test.use(browserOverride);

const dialog = (page: Page, name: string | RegExp) => page.getByRole('dialog', { name });

/**
 * A name nothing else in the suite will collide with. Random bytes rather than
 * Math.random(): these names become account usernames, and a scanner cannot
 * tell a test fixture from a credential.
 */
function unique(prefix: string): string {
  return `${prefix}-${randomBytes(4).toString('hex')}`;
}

/**
 * The page's own create button.
 *
 * An empty page carries two of them -- one in its header, one inside the
 * empty state, which is the guidelines' rule that an empty state names the
 * next step with the action inline. The first in document order is the
 * header's, and it is there whether the page is empty or not.
 */
const create = (page: Page, label: string) => page.getByRole('button', { name: label }).first();

/**
 * The section's own navigation: a rail beside the page on a desktop, a strip
 * above it on a tablet, and on a phone the list `/settings` itself shows.
 * Either way, every page is a link in a landmark named for the section.
 */
const settingsNav = (page: Page) => page.getByRole('navigation', { name: 'Settings' });

test('an account can be created, given a different role, and deleted by name', async ({ page }) => {
  const username = unique('spec-user');
  await goto(page, '/settings/users', 'Users');

  await create(page, 'Add an account').click();
  const form = dialog(page, 'Add an account');
  await form.getByRole('textbox', { name: 'Username' }).fill(username);
  await form.getByRole('textbox', { name: 'Display name' }).fill('A Spec Account');
  await form.getByRole('radio', { name: /Viewer/ }).check();
  await form.getByRole('textbox', { name: 'Password' }).fill('a-long-enough-password');
  await form.getByRole('button', { name: 'Add account' }).click();
  await expect(form).toBeHidden();

  const row = page.getByRole('row', { name: new RegExp(username) });
  await expect(row).toBeVisible();
  await expect(row).toContainText('Viewer');

  // Edited in place, and the table shows the label rather than the raw id.
  await row.getByRole('button', { name: new RegExp(`Actions for ${username}`) }).click();
  await page.getByRole('menuitem', { name: 'Edit role and details' }).click();
  const edit = dialog(page, new RegExp(`Edit ${username}`));
  await edit.getByRole('radio', { name: /Operator/ }).check();
  await edit.getByRole('button', { name: 'Save changes' }).click();
  await expect(edit).toBeHidden();
  await expect(row).toContainText('Operator');

  // Deleting demands the name typed out: this is the irreversible one.
  await row.getByRole('button', { name: new RegExp(`Actions for ${username}`) }).click();
  await page.getByRole('menuitem', { name: 'Delete this account' }).click();
  const confirm = dialog(page, 'Delete account');
  const go = confirm.getByRole('button', { name: 'Delete account' });
  await expect(go, 'the button is dead until the name is typed').toBeDisabled();
  await confirm.getByRole('textbox', { name: `Type ${username} to confirm` }).fill('not-the-name');
  await expect(go).toBeDisabled();
  await confirm.getByRole('textbox', { name: `Type ${username} to confirm` }).fill(username);
  await expect(go).toBeEnabled();
  await go.click();

  await expect(confirm).toBeHidden();
  await expect(row).toHaveCount(0);
});

test('a duplicate username is refused inside the dialog, not as an administrator warning', async ({
  page,
}) => {
  // The refusal used to surface on the page behind the modal, in words about
  // administrators, after the operator had closed the dialog that raised it.
  const username = unique('spec-dupe');
  await goto(page, '/settings/users', 'Users');

  const add = async () => {
    await create(page, 'Add an account').click();
    const form = dialog(page, 'Add an account');
    await form.getByRole('textbox', { name: 'Username' }).fill(username);
    await form.getByRole('textbox', { name: 'Password' }).fill('a-long-enough-password');
    await form.getByRole('button', { name: 'Add account' }).click();
    return form;
  };

  const first = await add();
  await expect(first).toBeHidden();

  const second = await add();
  await expect(second).toBeVisible();
  await expect(second.getByRole('alert')).toContainText(/already exists/);
  await expect(page.getByText('Zoomies keeps at least one enabled administrator')).toHaveCount(0);

  await second.getByRole('button', { name: 'Cancel' }).click();
  const row = page.getByRole('row', { name: new RegExp(username) });
  await row.getByRole('button', { name: new RegExp(`Actions for ${username}`) }).click();
  await page.getByRole('menuitem', { name: 'Delete this account' }).click();
  const confirm = dialog(page, 'Delete account');
  await confirm.getByRole('textbox', { name: `Type ${username} to confirm` }).fill(username);
  await confirm.getByRole('button', { name: 'Delete account' }).click();
  await expect(row).toHaveCount(0);
});

test('a refused account edit says why inside the open dialog and keeps what was typed', async ({
  page,
}) => {
  const username = unique('spec-edit');
  await goto(page, '/settings/users', 'Users');
  await create(page, 'Add an account').click();
  const form = dialog(page, 'Add an account');
  await form.getByRole('textbox', { name: 'Username' }).fill(username);
  await form.getByRole('textbox', { name: 'Password' }).fill('a-long-enough-password');
  await form.getByRole('button', { name: 'Add account' }).click();
  await expect(form).toBeHidden();

  const refusal =
    'this is the last enabled administrator; give another account the admin role before changing this one';
  await page.route('**/api/v1/users/*', async (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback();
    await route.fulfill({
      status: 409,
      contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'conflict', message: refusal } }),
    });
  });

  const row = page.getByRole('row', { name: new RegExp(username) });
  await row.getByRole('button', { name: new RegExp(`Actions for ${username}`) }).click();
  await page.getByRole('menuitem', { name: 'Edit role and details' }).click();
  const edit = dialog(page, new RegExp(`Edit ${username}`));
  await edit.getByRole('textbox', { name: 'Display name' }).fill('Typed and kept');
  await edit.getByRole('button', { name: 'Save changes' }).click();

  await expect(edit.getByRole('alert')).toContainText('last enabled administrator');
  await expect(edit.getByRole('textbox', { name: 'Display name' })).toHaveValue('Typed and kept');

  await page.unroute('**/api/v1/users/*');
  await edit.getByRole('button', { name: 'Cancel' }).click();
  await row.getByRole('button', { name: new RegExp(`Actions for ${username}`) }).click();
  await page.getByRole('menuitem', { name: 'Delete this account' }).click();
  const confirm = dialog(page, 'Delete account');
  await confirm.getByRole('textbox', { name: `Type ${username} to confirm` }).fill(username);
  await confirm.getByRole('button', { name: 'Delete account' }).click();
  await expect(row).toHaveCount(0);
});

test('Enter submits a dialog with text fields, and a typed-name confirmation only once the name matches', async ({
  page,
}) => {
  // Every one of these was a bare <div> with a click handler on its footer
  // button, so Enter in a field did nothing and a password manager saw no form.
  const username = unique('spec-enter');
  await goto(page, '/settings/users', 'Users');

  await create(page, 'Add an account').click();
  const add = dialog(page, 'Add an account');
  await add.getByRole('textbox', { name: 'Username' }).fill(username);
  await add.getByRole('textbox', { name: 'Password' }).fill('a-long-enough-password');
  await add.getByRole('textbox', { name: 'Password' }).press('Enter');
  await expect(add).toBeHidden();

  const row = page.getByRole('row', { name: new RegExp(username) });
  await expect(row).toBeVisible();
  const menu = () =>
    row.getByRole('button', { name: new RegExp(`Actions for ${username}`) }).click();

  await menu();
  await page.getByRole('menuitem', { name: 'Edit role and details' }).click();
  const edit = dialog(page, new RegExp(`Edit ${username}`));
  await edit.getByRole('textbox', { name: 'Display name' }).fill('Saved with Enter');
  await edit.getByRole('textbox', { name: 'Display name' }).press('Enter');
  await expect(edit).toBeHidden();
  await expect(row).toContainText('Saved with Enter');

  await menu();
  await page.getByRole('menuitem', { name: 'Reset password' }).click();
  const reset = dialog(page, new RegExp(`Reset password for ${username}`));
  await reset.getByLabel('New password').fill('another-long-enough-password');
  await reset.getByLabel('New password').press('Enter');
  await expect(reset).toBeHidden();

  // The irreversible one. Enter with the wrong name is nothing at all; with
  // the right one it is the same as pressing the button that is now enabled.
  await menu();
  await page.getByRole('menuitem', { name: 'Delete this account' }).click();
  const confirm = dialog(page, 'Delete account');
  const typed = confirm.getByRole('textbox', { name: `Type ${username} to confirm` });
  await typed.fill('not-the-name');
  await typed.press('Enter');
  await expect(confirm).toBeVisible();
  await expect(row).toBeVisible();
  await typed.fill(username);
  await typed.press('Enter');
  await expect(confirm).toBeHidden();
  await expect(row).toHaveCount(0);

  const name = unique('spec-enter-token');
  await goto(page, '/settings/tokens', 'API tokens');
  await create(page, 'Create a token').click();
  const mint = dialog(page, 'Create an API token');
  await mint.getByRole('textbox', { name: 'Name' }).fill(name);
  await mint.getByRole('textbox', { name: 'Name' }).press('Enter');
  await expect(mint).toContainText('This is the only time it exists in plain text');
  await mint.getByRole('button', { name: 'Done' }).click();
});

test('a token is shown once, in plain text, and says so', async ({ page }) => {
  const name = unique('spec-token');
  await goto(page, '/settings/tokens', 'API tokens');

  await create(page, 'Create a token').click();
  const form = dialog(page, 'Create an API token');
  await form.getByRole('textbox', { name: 'Name' }).fill(name);
  await form.getByRole('radio', { name: /Operator/ }).check();
  await form.getByRole('button', { name: 'Create token' }).click();

  // The one moment it exists in plain text, and the dialog is explicit that
  // this is the only one.
  await expect(form).toContainText('This is the only time it exists in plain text');
  await expect(form.getByRole('button', { name: 'Copy the token' })).toBeVisible();

  // ...and a stray click must not be what ends it: the scrim and the corner
  // icon are gone, so only Done (or Escape, which is deliberate) closes it.
  await expect(form.getByRole('button', { name: 'Close' })).toHaveCount(0);
  await page.mouse.click(5, 5);
  await expect(form).toBeVisible();
  await expect(form.getByRole('button', { name: 'Copy the token' })).toBeVisible();
  await form.getByRole('button', { name: 'Done' }).click();
  await expect(form).toBeHidden();

  const row = page.getByRole('row', { name: new RegExp(name) });
  await expect(row).toBeVisible();
  await expect(row).toContainText('Operator');

  // Revoking needs a confirmation, but not the name: it is undoable by
  // minting another, unlike deleting the account that owns it.
  await row.getByRole('button', { name: 'Revoke' }).click();
  const confirm = dialog(page, 'Revoke token');
  await expect(confirm).toContainText('stops working immediately');
  await confirm.getByRole('button', { name: 'Revoke' }).click();
  await expect(confirm).toBeHidden();
  await expect(row.getByRole('button', { name: 'Revoke' })).toHaveCount(0);
});

test('a revoked token is deleted from the list, and the bulk action takes only spent ones', async ({
  page,
}) => {
  const gone = unique('spec-gone');
  const kept = unique('spec-kept');
  await goto(page, '/settings/tokens', 'API tokens');

  for (const name of [gone, kept]) {
    await create(page, 'Create a token').click();
    const form = dialog(page, 'Create an API token');
    await form.getByRole('textbox', { name: 'Name' }).fill(name);
    await form.getByRole('button', { name: 'Create token' }).click();
    await form.getByRole('button', { name: 'Done' }).click();
  }

  // A live token offers Revoke and never Delete: a credential that vanished
  // from the list while it still worked would be one nobody knows to revoke.
  const row = page.getByRole('row', { name: new RegExp(gone) });
  await expect(row.getByRole('button', { name: `Delete ${gone}` })).toHaveCount(0);
  await row.getByRole('button', { name: `Revoke ${gone}` }).click();
  await dialog(page, 'Revoke token').getByRole('button', { name: 'Revoke' }).click();
  await expect(dialog(page, 'Revoke token')).toBeHidden();

  await row.getByRole('button', { name: `Delete ${gone}` }).click();
  const confirm = dialog(page, 'Delete token');
  await expect(confirm).toContainText(gone);
  await confirm.getByRole('button', { name: 'Delete' }).click();
  await expect(confirm).toBeHidden();
  await expect(page.getByRole('row', { name: new RegExp(gone) })).toHaveCount(0);

  // Revoke the other and purge: it goes, and nothing that still works does.
  const other = page.getByRole('row', { name: new RegExp(kept) });
  await other.getByRole('button', { name: `Revoke ${kept}` }).click();
  await dialog(page, 'Revoke token').getByRole('button', { name: 'Revoke' }).click();
  await expect(dialog(page, 'Revoke token')).toBeHidden();
  const live = await page.getByRole('button', { name: /^Revoke / }).count();

  await page.getByRole('button', { name: 'Delete revoked and expired' }).click();
  const bulk = dialog(page, 'Delete revoked and expired tokens');
  await expect(bulk).toContainText('Tokens that still work are left alone');
  await bulk.getByRole('button', { name: 'Delete them' }).click();
  await expect(bulk).toBeHidden();
  await expect(page.getByRole('row', { name: new RegExp(kept) })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /^Revoke / })).toHaveCount(live);
  await expect(page.getByRole('button', { name: 'Delete revoked and expired' })).toHaveCount(0);
});

test('cancelling a destructive confirmation changes nothing', async ({ page }) => {
  const name = unique('spec-keep');
  await goto(page, '/settings/tokens', 'API tokens');

  await create(page, 'Create a token').click();
  const form = dialog(page, 'Create an API token');
  await form.getByRole('textbox', { name: 'Name' }).fill(name);
  await form.getByRole('button', { name: 'Create token' }).click();
  await form.getByRole('button', { name: 'Done' }).click();

  const row = page.getByRole('row', { name: new RegExp(name) });
  await row.getByRole('button', { name: 'Revoke' }).click();
  await dialog(page, 'Revoke token').getByRole('button', { name: 'Cancel' }).click();

  await expect(dialog(page, 'Revoke token')).toBeHidden();
  await expect(row.getByRole('button', { name: 'Revoke' }), 'still revocable').toBeVisible();

  // Tidy up so the next spec sees the page it expects.
  await row.getByRole('button', { name: 'Revoke' }).click();
  await dialog(page, 'Revoke token').getByRole('button', { name: 'Revoke' }).click();
  await expect(dialog(page, 'Revoke token')).toBeHidden();
});

/*
 * Settings is a section of pages rather than one page of tabs: each has an
 * address, the section's own navigation lists them all, and the one being
 * read is marked. On a desktop `/settings` alone goes straight to the first
 * page; on a phone it is the list of them.
 */
test('every settings page has an address of its own, and the section lists them', async ({
  page,
}) => {
  await goto(page, '/settings', /^(Account|Settings)$/);
  const phone = !!test.info().project.use.isMobile;
  if (phone) {
    await expect(page).toHaveURL(/\/settings$/);
  } else {
    await expect(page).toHaveURL(/\/settings\/account$/);
  }

  const pages = [
    ['account', 'Account'],
    ['appearance', 'Appearance'],
    ['users', 'Users'],
    ['tokens', 'API tokens'],
    ['configuration', 'Configuration'],
    ['backups', 'Backups'],
    ['updates', 'Updates'],
    ['about', 'About'],
  ] as const;

  for (const [id, label] of pages) {
    // On a phone the section's list is the page `/settings` shows, and each
    // page carries a way back to it; on a desktop the rail is beside the page.
    // A row in the list is named by its label and its description, a rail
    // entry by its label alone.
    if (phone) await goto(page, '/settings', 'Settings');
    await page
      .getByRole('link', phone ? { name: new RegExp(`^${label} `) } : { name: label, exact: true })
      .first()
      .click();
    await expect(page).toHaveURL(new RegExp(`/settings/${id}$`));
    await expect(pageHeading(page, label)).toBeVisible();
    if (!phone) {
      const current = settingsNav(page).locator('[aria-current="page"]');
      await expect(current).toHaveCount(1);
      await expect(current).toHaveText(label);
    }
  }

  // A page survives a reload, which is what an address is for.
  await goto(page, '/settings/appearance', 'Appearance');
  await page.reload();
  await expect(pageHeading(page, 'Appearance')).toBeVisible();
  await expect(page).toHaveURL(/\/settings\/appearance$/);
});

test('the old tab addresses still land on their page', async ({ page }) => {
  // Bookmarks and the links in problem entries were written as `?tab=`, and a
  // setting a link named is still the row it lands on.
  await page.goto('/settings?tab=configuration&setting=scheduler.provision_timeout');
  await expect(pageHeading(page, 'Configuration')).toBeVisible();
  await expect(page).toHaveURL(/\/settings\/configuration\?setting=scheduler\.provision_timeout$/);
  await expect(page.locator('.row.sought')).toBeVisible();

  await page.goto('/settings?tab=about');
  await expect(pageHeading(page, 'About')).toBeVisible();
  await expect(page).toHaveURL(/\/settings\/about$/);
});

test('the account menu leads to the pages that are about the person', async ({ page }) => {
  await goto(page, '/', 'Overview');

  const menu = await openAccountMenu(page);
  // The identity first, then the theme as a choice with the one in force
  // marked, then the pages, then the way out.
  await expect(menu.getByRole('menuitemradio', { name: 'System' })).toHaveAttribute(
    'aria-checked',
    'true',
  );
  await expect(menu.getByRole('menuitem', { name: 'Sign out' })).toBeVisible();

  await menu.getByRole('menuitem', { name: 'Your account' }).click();
  await expect(pageHeading(page, 'Account')).toBeVisible();
  await expect(page).toHaveURL(/\/settings\/account$/);
  await expect(page.getByText(/^Signed in as /)).toBeVisible();
});

/**
 * The About page is the product's own identity card, and the two things it
 * says about the product itself are easy to get wrong in opposite directions.
 *
 * The mark: the brand guide ranks the original circular dog above every other
 * standalone mark, and the paw/swish stops at 64px, so anything else here is
 * the wrong artwork. 128px is the guide's minimum for the circular mark and
 * the reason this is the slot that carries it.
 *
 * The description: most people meet a controller somebody else installed, and
 * the page header says what the page is rather than what Zoomies is.
 */
test('the About page carries the primary mark and says what Zoomies is', async ({ page }) => {
  await goto(page, '/settings/about', 'About');

  // The mark is decorative, so nothing in the accessibility tree names it and
  // the served file is the only observable that distinguishes one from another.
  const mark = page.locator('.identity img');
  await expect(mark).toHaveAttribute('src', '/brand/mark-white.png');
  await expect(mark).toHaveJSProperty('naturalWidth', 128);

  await expect(
    page.getByText(/lightweight fleet controller for GitHub Actions runners/),
  ).toBeVisible();
});

/**
 * The Events page is the one setting that changes another page, so what it
 * protects is that the switch means what it says: the Overview's feed carries
 * exactly the kinds left on, it says how many are off rather than quietly
 * omitting them, and the choice survives leaving the page.
 */
test('the Events page decides what the Overview’s feed carries', async ({ page }) => {
  await goto(page, '/settings/events', 'Events');
  // Counted rather than written down, so the assertion below is about the
  // switch rather than about how many kinds this release happens to ship.
  const kinds = await page.getByRole('switch').count();
  const on = await page.getByRole('switch', { checked: true }).count();
  const scaling = page.getByRole('switch', { name: 'Scaling decisions' });
  await expect(scaling, 'the scheduler’s decisions are on by default').toHaveAttribute(
    'aria-checked',
    'true',
  );
  await scaling.click();
  await expect(scaling).toHaveAttribute('aria-checked', 'false');

  await goto(page, '/', 'Overview');
  const feed = page.getByRole('region', { name: 'Recent events', exact: true });
  await expect(feed.getByRole('listitem').first()).toBeVisible();
  await expect(feed, 'the decisions are gone').not.toContainText('scaled zoomies-demo-');
  // Counted out loud rather than omitted silently: a panel that quietly left
  // things out would be worse than one that shows too much.
  await expect(feed).toContainText(`${on - 1} of ${kinds} kinds`);
  // What was not switched off is still there -- and it is not a scaling
  // decision, which is the whole point of the feed being a feed.
  await expect(feed).toContainText(/A job/);

  // And the choice is this browser's, remembered across the navigation back.
  await goto(page, '/settings/events', 'Events');
  const again = page.getByRole('switch', { name: 'Scaling decisions' });
  await expect(again).toHaveAttribute('aria-checked', 'false');
  await again.click();

  await goto(page, '/', 'Overview');
  await expect(feed).toContainText(/scaled zoomies-demo-/);
  await expect(feed).toContainText(`${on} of ${kinds} kinds`);
});

test('a size setting reads 8g and writes it back as 8 GB', async ({ page }) => {
  // A runner's default memory is a megabyte count in the file, and the row used
  // to be a number box that took 8192 and nothing else. It is edited and then
  // cancelled rather than saved: the database is shared by every spec running
  // at once, and nothing here is about storing a value. Not an agent.* size:
  // this controller runs no agent, so the page does not list those.
  const key = 'runners.default_memory_mb';
  await goto(page, `/settings/configuration?setting=${key}`, 'Configuration');
  const row = page.locator(`[id="setting-${key}"]`);
  await row.getByRole('button', { name: 'Change' }).click();

  const field = row.getByRole('textbox', { name: `New value for ${key}` });
  await field.fill('8g');
  await field.press('Enter');
  await expect(field).toHaveValue('8 GB');
  await expect(row.getByRole('slider', { name: `New value for ${key}` })).toHaveAttribute(
    'aria-valuetext',
    '8 GB',
  );

  await field.fill('8192 mb');
  await field.press('Enter');
  await expect(field).toHaveValue('8 GB');

  await row.getByRole('button', { name: `Cancel editing ${key}` }).click();
  await expect(field).toBeHidden();
});

test('a duration setting reads 2 weeks and writes it back as 14d', async ({ page }) => {
  // Go writes a retention period in hours, so the row used to say 720h and
  // take 336h. It is edited and cancelled rather than saved, for the same
  // shared-database reason as the size above.
  const key = 'retention.jobs';
  await goto(page, `/settings/configuration?setting=${key}`, 'Configuration');
  const row = page.locator(`[id="setting-${key}"]`);
  await expect(row.locator('.shown')).not.toHaveText(/\d+h$/);
  await row.getByRole('button', { name: 'Change' }).click();

  const field = row.getByRole('textbox', { name: `New value for ${key}` });
  await field.fill('2 weeks');
  await field.press('Enter');
  await expect(field).toHaveValue('14d');
  await expect(row.getByRole('slider', { name: `New value for ${key}` })).toHaveAttribute(
    'aria-valuetext',
    '14d',
  );
  await field.fill('36h');
  await field.press('Enter');
  await expect(field).toHaveValue('1d 12h');

  await row.getByRole('button', { name: `Cancel editing ${key}` }).click();
});
