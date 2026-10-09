/**
 * Settings: the backups.
 *
 * The one place a copy of the whole database leaves the machine and the one
 * place a copy comes back to replace the fleet, so what these tests protect is
 * the sequence: a backup taken from the page appears with what a restore needs
 * to know, it verifies, its download is the archive it says it is, a restore
 * is staged by name and can be cancelled, and deleting demands the name too.
 *
 * The one thing not pressed for real is "Restart and restore": it stops the controller
 * the whole suite shares. Staging is where the checks live; the restart is a
 * process exit the Go tests cover.
 */
import { expect, test, type Page } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);

const dialog = (page: Page, name: string | RegExp) => page.getByRole('dialog', { name });

async function openBackups(page: Page): Promise<void> {
  await goto(page, '/settings/backups', 'Backups');
}

/**
 * Press the button, and return the row the new backup appears in.
 *
 * It waits for the newest id to change rather than for the list to grow: a
 * fleet already holding `backup.keep` copies loses its oldest to retention as
 * it gains the new one, so the count is the one thing about a new backup that
 * does not move. This suite shares one controller across its projects and
 * reaches that ceiling part-way through.
 */
async function takeOne(page: Page) {
  const rows = page.locator('tbody').getByRole('row');
  const newest = async () => {
    if ((await rows.count()) === 0) return null;
    return (await rows.first().locator('.mono').first().textContent())?.trim() ?? null;
  };
  const before = await newest();
  await page.getByRole('button', { name: 'Back up now' }).first().click();
  await expect(page.getByText('Backup taken')).toBeVisible();
  await expect.poll(newest, { message: 'the new backup at the top of the list' }).not.toBe(before);
  // Newest first: the first body row is the one just taken.
  return rows.first();
}

/** The id printed in a row, which the confirmations ask to have typed back. */
async function idOf(row: ReturnType<Page['locator']>): Promise<string> {
  const id = await row.locator('.mono').first().textContent();
  expect(id, 'the row names the backup').toMatch(/^zoomies-\d{8}-\d{6}/);
  return id!.trim();
}

test('a backup taken from the page is listed with what a restore needs to know', async ({
  page,
}) => {
  await openBackups(page);
  const row = await takeOne(page);
  const id = await idOf(row);

  // The facts a restore will ask about, in the row rather than in a manifest
  // nobody opens: who took it, the schema it had reached, and whether this
  // host's key is the one that sealed it.
  await expect(row).toContainText('Taken here');
  await expect(row).toContainText('This key');
  await expect(row.getByRole('toolbar', { name: new RegExp(id) })).toBeVisible();

  // And the panel's own facts moved with it.
  await expect(page.getByText('Last backup')).toBeVisible();
  await expect(page.getByText('None yet')).toHaveCount(0);

  // Verify re-reads the file and says so, check by check.
  await row.getByRole('button', { name: `Verify: ${id}` }).click();
  const verify = dialog(page, new RegExp(`Verify ${id}`));
  await expect(verify.getByText('Sound')).toBeVisible();
  await expect(verify.getByText('Unchanged')).toBeVisible();
  await expect(verify.getByText('This backup can be restored here.')).toBeVisible();
  await verify.getByRole('button', { name: 'Done' }).click();
  await expect(verify).toBeHidden();

  // The download is the archive it says it is: a gzip, named for the backup.
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    row.getByRole('button', { name: `Download: ${id}` }).click(),
  ]);
  expect(download.suggestedFilename()).toBe(`${id}.tar.gz`);
});

test('an encrypted download insists on a passphrase worth having', async ({ page }) => {
  await openBackups(page);
  const row = await takeOne(page);
  const id = await idOf(row);

  await row.getByRole('button', { name: `Download encrypted: ${id}` }).click();
  const seal = dialog(page, 'Download encrypted');
  const go = seal.getByRole('button', { name: 'Download' });
  await expect(go, 'dead until a passphrase is typed twice').toBeDisabled();
  await seal.getByLabel('Passphrase', { exact: true }).fill('short');
  await expect(seal.getByText('Use at least eight characters.')).toBeVisible();
  await seal.getByLabel('Passphrase', { exact: true }).fill('correct horse battery staple');
  await seal.getByLabel('Passphrase again').fill('correct horse battery stapl');
  await expect(seal.getByText('The two do not match.')).toBeVisible();
  await expect(go).toBeDisabled();
  await seal.getByLabel('Passphrase again').fill('correct horse battery staple');
  await expect(go).toBeEnabled();
  const [download] = await Promise.all([page.waitForEvent('download'), go.click()]);
  expect(download.suggestedFilename()).toBe(`${id}.tar.gz.enc`);
  await expect(seal).toBeHidden();
});

test('a restore is staged by name, shown as waiting, and can be cancelled', async ({ page }) => {
  await openBackups(page);
  const row = await takeOne(page);
  const id = await idOf(row);

  await row.getByRole('button', { name: `Restore: ${id}` }).click();
  const confirm = dialog(page, 'Restore backup');
  const stage = confirm.getByRole('button', { name: 'Stage the restore' });
  await expect(stage, 'the button is dead until the name is typed').toBeDisabled();
  await expect(confirm).toContainText('Everyone is signed out');
  await confirm.getByRole('textbox', { name: `Type ${id} to confirm` }).fill(id);
  await expect(stage).toBeEnabled();
  await stage.click();
  await expect(confirm).toBeHidden();

  // Nothing has changed: the page says a restore is waiting, names it, and
  // offers the two ways out. The row it names cannot be deleted meanwhile.
  const banner = page.getByRole('region', { name: /waiting for a restart/i });
  await expect(banner).toBeVisible();
  await expect(banner).toContainText(id);
  await expect(page.getByRole('button', { name: 'Restart and restore' })).toBeVisible();
  const remove = row.getByRole('button', { name: `Delete: ${id}` });
  await expect(remove).toHaveAttribute('aria-disabled', 'true');

  await page.getByRole('button', { name: 'Cancel the restore', exact: true }).click();
  await expect(banner).toBeHidden();
  await expect(remove).not.toHaveAttribute('aria-disabled', 'true');
});

// The restart state is shared with the controller's own update, which hands it
// other words and another way of knowing the controller is back. A restore must
// still be told in its own: the sign-in page, the fence, the health probe.
test('applying a restore watches the health probe through both halves and says what a restore does', async ({
  page,
}) => {
  await openBackups(page);
  const row = await takeOne(page);
  const id = await idOf(row);
  await row.getByRole('button', { name: `Restore: ${id}` }).click();
  const confirm = dialog(page, 'Restore backup');
  await confirm.getByRole('textbox', { name: `Type ${id} to confirm` }).fill(id);
  await confirm.getByRole('button', { name: 'Stage the restore' }).click();
  await expect(confirm).toBeHidden();

  // The restart itself is not pressed on the shared controller: the request is
  // answered here, and the health probe goes down and comes back as a restart
  // would make it.
  let probe: 'up' | 'down' = 'up';
  await page.route('**/api/v1/backups/restore/apply', (route) => route.fulfill({ json: {} }));
  await page.route('**/healthz', (route) =>
    probe === 'up' ? route.fulfill({ status: 200, body: 'ok' }) : route.abort('connectionrefused'),
  );
  await page.getByRole('button', { name: 'Restart and restore' }).click();
  await dialog(page, /restart/i)
    .getByRole('button', { name: 'Restart and restore' })
    .click();

  const wait = page.locator('section.wait');
  await expect(wait.getByRole('heading', { name: 'Stopping the controller' })).toBeVisible();
  await expect(wait).toContainText(`restoring ${id}`);
  await expect(wait).toContainText('The restore is applied by the next controller to start');

  probe = 'down';
  await expect(wait.getByRole('heading', { name: 'Waiting for it to start again' })).toBeVisible({
    timeout: 10_000,
  });
  await expect(wait).toContainText('Everyone is signed out by the restore');

  // It answers again, and the page starts over on the page it came from.
  probe = 'up';
  await expect(wait.getByRole('heading', { name: 'It is back' })).toBeVisible({ timeout: 10_000 });
  await expect(page).toHaveURL(/\/settings\/backups$/);
  await page.unroute('**/healthz');
  await page.unroute('**/api/v1/backups/restore/apply');
  await expect(wait).toBeHidden({ timeout: 10_000 });

  // Put it back as it was: staged, so the next test starts from a clean page.
  await page.getByRole('button', { name: 'Cancel the restore', exact: true }).click();
  await expect(page.getByRole('region', { name: /waiting for a restart/i })).toBeHidden();
});

test('deleting a backup demands its name and then it is gone', async ({ page }) => {
  await openBackups(page);
  const row = await takeOne(page);
  const id = await idOf(row);

  await row.getByRole('button', { name: `Delete: ${id}` }).click();
  const confirm = dialog(page, 'Delete backup');
  const go = confirm.getByRole('button', { name: 'Delete backup' });
  await expect(go).toBeDisabled();
  await confirm.getByRole('textbox', { name: `Type ${id} to confirm` }).fill(id);
  await go.click();
  await expect(confirm).toBeHidden();
  await expect(page.getByText(`${id} deleted`)).toBeVisible();
  await expect(page.locator('tbody').getByRole('row').filter({ hasText: id })).toHaveCount(0);
});

test('the configuration can be exported and an import is previewed before it is applied', async ({
  page,
}) => {
  await goto(page, '/settings/configuration', 'Configuration');
  await expect(page.getByRole('heading', { name: 'Configuration', exact: true })).toBeVisible();

  // The export is a download in the shape the file takes.
  await page.getByRole('button', { name: 'Export the settings' }).click();
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('menuitem', { name: 'As zoomies.yaml' }).click(),
  ]);
  expect(download.suggestedFilename()).toMatch(/^zoomies-settings-\d{8}-\d{6}\.yaml$/);

  // The import says what it would do, key by key, and refuses the key it
  // cannot take -- and nothing is written until Apply.
  await page.getByRole('button', { name: 'Import' }).click();
  const importing = dialog(page, 'Import settings');
  await importing
    .getByRole('textbox', { name: 'Document' })
    .fill('retention:\n  webhooks: 72h\nscheduler:\n  interval: soon\n');
  await importing.getByRole('button', { name: 'Check the document' }).click();
  const webhooks = importing.getByRole('row').filter({ hasText: 'retention.webhooks' });
  await expect(webhooks).toContainText('Will change');
  const interval = importing.getByRole('row').filter({ hasText: 'scheduler.interval' });
  await expect(interval).toContainText('Refused');
  await expect(interval).toContainText('is not a duration');
  await expect(importing.getByRole('button', { name: 'Apply 1 change' })).toBeEnabled();
  await importing.getByRole('button', { name: 'Apply 1 change' }).click();
  await expect(importing).toBeHidden();
  await expect(page.getByText('1 setting imported')).toBeVisible();

  // Applied, and shown on the page as a value stored here -- then put back,
  // so the next spec sees the fixture it expects.
  const row = page.locator('.row').filter({ has: page.getByText('Keep webhook deliveries for') });
  await expect(row).toContainText('3d');
  await row.getByRole('button', { name: 'Reset' }).click();
  await expect(row.getByText('Saved here')).toBeHidden();
});

/*
 * Retention runs when a backup is taken, which is the wrong moment for an
 * operator who has just lowered the number of copies the fleet keeps: nothing
 * happens until the next backup, and with the schedule off, nothing happens
 * ever. The button says what it is about to delete -- both ceilings, because
 * the local one and each bucket's are separate numbers -- and then says what
 * it did.
 */
test('retention can be applied without waiting for the next backup', async ({ page }) => {
  await openBackups(page);
  await takeOne(page);

  await page.getByRole('button', { name: 'Prune now' }).click();
  const confirming = dialog(page, 'Apply retention now');
  await expect(confirming).toContainText('This host keeps');
  await expect(confirming).toContainText('never counted and never removed');
  await confirming.getByRole('button', { name: 'Apply retention' }).click();
  await expect(confirming).toBeHidden();

  // The fixture keeps more copies than this spec has taken, so the honest
  // answer is that there was nothing over the ceiling -- which is the answer
  // being tested: the pass ran and reported, rather than silently doing
  // nothing.
  await expect(page.getByText(/Nothing to remove|Retention applied/)).toBeVisible();
});

/*
 * A partial failure is the one outcome of a retention pass that somebody has to
 * act on, and it arrives as a warning rather than an error because most of the
 * work did happen. That must not put it in the queue that waits for a pause:
 * the two live regions exist so that "one remote refused" interrupts and "every
 * copy is one we meant to keep" does not.
 */
test('a retention pass that only partly worked interrupts rather than waits', async ({ page }) => {
  await page.route('**/api/v1/backups/prune', async (route) => {
    await route.fulfill({
      json: {
        removed: ['zoomies-20260101-000000'],
        remotes: [{ name: 'offsite', removed: [], error: 'the bucket refused the delete' }],
      },
    });
  });

  await openBackups(page);
  // Retention has nothing to offer with no copy to keep, so the button is dead
  // until this fleet has taken one.
  await takeOne(page);
  await page.getByRole('button', { name: 'Prune now' }).click();
  const confirming = dialog(page, 'Apply retention now');
  await confirming.getByRole('button', { name: 'Apply retention' }).click();
  await expect(confirming).toBeHidden();

  const warned = page.getByText('Retention was partly applied');
  await expect(warned).toBeVisible();
  await expect(warned.locator('xpath=ancestor::*[@aria-live][1]')).toHaveAttribute(
    'aria-live',
    'assertive',
  );
  await expect(page.getByText('the bucket refused the delete')).toBeVisible();
});

/*
 * A fleet keeping ninety daily copies is ninety rows, and on a phone each is a
 * card six lines tall: the restore somebody came for is a minute of scrolling
 * away. The list pages, newest first, so the first page is the one that
 * matters.
 */
test('a long list of backups is paged rather than scrolled', async ({ page }) => {
  await page.route('**/api/v1/backups', async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    const one = body.items?.[0];
    test.skip(!one, 'the fixture has no backup to make a long list out of');
    body.items = Array.from({ length: 24 }, (_, i) => ({
      ...one,
      id: `zoomies-20260916-${String(100000 + i).slice(-6)}`,
    }));
    await route.fulfill({ response, json: body });
  });

  await openBackups(page);
  const rows = page.locator('tbody').getByRole('row');
  await expect(rows).toHaveCount(10);
  const pager = page.getByRole('navigation', { name: 'Pagination' });
  await expect(pager).toContainText('1–10 of 24 backups');

  await pager.getByRole('button', { name: 'Next page' }).click();
  await expect(pager).toContainText('11–20 of 24 backups');
  await expect(rows.first()).toContainText('zoomies-20260916-100010');

  // The last page is short, and the size is the operator's to change.
  await pager.getByRole('button', { name: 'Last page' }).click();
  await expect(rows).toHaveCount(4);
  await pager.getByLabel('Rows').selectOption('25');
  await expect(rows).toHaveCount(24);
});
