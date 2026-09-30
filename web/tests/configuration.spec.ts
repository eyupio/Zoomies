/**
 * Settings: the configuration.
 *
 * This is the page that can change what the controller is, so the things worth
 * testing are the ones that stop it doing that by accident: that a change is
 * kept rather than lost at the next restart, that a value nothing can apply
 * says so instead of pretending, that a setting an environment variable is
 * holding is out of the way and explained rather than editable and futile, and
 * that a bad value is refused with a sentence somebody can act on.
 *
 * Every test puts back what it changed. The fixture's controller is shared with
 * the rest of the suite, and a retention window or a scheduler interval left
 * altered is a change every later spec runs against.
 */
import { expect, test, type Page } from '@playwright/test';
import { browserOverride, goto, openSection, reload } from './support/fixtures';

test.use(browserOverride);

/** One setting's row, found by the name a person reads rather than its key. */
const row = (page: Page, label: string) =>
  page.locator('.row').filter({ has: page.getByText(label, { exact: true }) });

async function openConfiguration(page: Page): Promise<void> {
  await goto(page, '/settings/configuration', 'Configuration');
}

test('a controller with no agent lists only the switch that would bring one back', async ({
  page,
}) => {
  // The fixture's controller runs no embedded agent, which is what a
  // controller-only instance is. Its agent settings describe nothing on this
  // process -- the joined hosts set their own -- so they are not offered; the
  // switch that would start one is. The fixture sets it from the environment,
  // so it is listed among the settings the environment holds.
  await openConfiguration(page);
  const held = page.getByRole('group', { name: /Held by the environment/i });
  await expect(held).toContainText('ZOOMIES_AGENT_EMBEDDED');
  await expect(row(page, 'Runner backend')).toHaveCount(0);
  await expect(row(page, 'Runners per host')).toHaveCount(0);
});

/** Change one setting through its own row, and wait for the row to settle. */
async function change(page: Page, label: string, value: string): Promise<void> {
  const target = row(page, label);
  await target.getByRole('button', { name: 'Change' }).click();
  const field = target.getByRole('textbox');
  await field.fill(value);
  await target.getByRole('button', { name: /^Save / }).click();
  await expect(target.getByRole('button', { name: 'Change' })).toBeVisible();
}

test('a change to a live setting is applied and kept', async ({ page }) => {
  await openConfiguration(page);

  // A retention window is safe to move: nothing else in the suite asserts it,
  // and the pruning it drives is not on any spec's path.
  await change(page, 'Keep webhook deliveries for', '96h');
  const target = row(page, 'Keep webhook deliveries for');
  // Shown in the largest whole unit, the way a person would say it.
  await expect(target).toContainText('4d');

  // Kept, not merely applied: the value is in the database, so the row now
  // says where it came from.
  await expect(target.getByText('Saved here')).toBeVisible();

  // A reload reads it back from the server rather than from anything the page
  // is still holding.
  await page.reload();
  await expect(row(page, 'Keep webhook deliveries for')).toContainText('4d');

  // Put it back, so the next spec sees the fixture it expects.
  await row(page, 'Keep webhook deliveries for').getByRole('button', { name: 'Reset' }).click();
  await expect(row(page, 'Keep webhook deliveries for').getByText('Saved here')).toBeHidden();
});

test('a setting the controller cannot apply to itself is stored and says it is waiting', async ({
  page,
}) => {
  await openConfiguration(page);

  // Rebinding a listener under live connections is not something a process can
  // do to itself. Refusing the edit, which is what this page used to do, never
  // stopped anybody wanting the change -- it only moved the work to a text
  // editor and left no record that anyone had asked.
  await change(page, 'Session lifetime', '48h');

  const target = row(page, 'Session lifetime');
  await expect(target.getByText('Waiting for a restart')).toBeVisible();
  await expect(page.getByRole('heading', { name: /waiting for a restart/i })).toBeVisible();

  await target.getByRole('button', { name: 'Reset' }).click();
  await expect(target.getByText('Waiting for a restart')).toBeHidden();
});

test('a value that will not parse is refused under the field that was typed in', async ({
  page,
}) => {
  await openConfiguration(page);

  const target = row(page, 'Scheduler interval');
  await target.getByRole('button', { name: 'Change' }).click();
  await target.getByRole('textbox').fill('soon');
  await target.getByRole('button', { name: /^Save / }).click();

  // Under the field, not in a toast that floats away while somebody is still
  // reading it -- and saying what shape was wanted, not only that this one was
  // wrong.
  const failure = target.getByRole('alert');
  await expect(failure).toContainText('Write a length of time');
  await expect(failure).toContainText('30s, 5m, 1h30m or 7d');

  await target.getByRole('button', { name: /^Cancel editing / }).click();
});

test('settings held by the environment are gathered at the end and name their variable', async ({
  page,
}) => {
  await openConfiguration(page);

  // The fixture's controller runs with ZOOMIES_DISABLE_AUTH set, which is
  // exactly the state this group exists for: the page shows one value, the
  // file may say another, and nothing an administrator does here changes it.
  const held = page.getByRole('group', { name: /Held by the environment/i });
  await expect(held).toBeVisible();
  await expect(held).toContainText('ZOOMIES_DISABLE_AUTH');
  await expect(held).toContainText('amend the environment file');

  // And it is not among the settings somebody can change, because it cannot be.
  await expect(row(page, 'Disable authentication')).toHaveCount(0);
});

test('the page can be searched and filtered down to what has been changed', async ({ page }) => {
  await openConfiguration(page);

  // Eighty-eight settings is too many to scroll, so the search matches the key,
  // the summary and the environment variable alike.
  await page.getByRole('textbox', { name: 'Search settings' }).fill('ZOOMIES_PROVISION_TIMEOUT');
  await expect(row(page, 'Provision timeout')).toBeVisible();
  await expect(row(page, 'Log level')).toHaveCount(0);

  await page.getByRole('textbox', { name: 'Search settings' }).fill('');
  await page.getByText(/^Changed/).click();
  await expect(page.getByRole('heading', { name: 'Configuration', exact: true })).toBeVisible();

  // Whatever the fixture has changed, nothing still at its default is listed.
  await expect(row(page, 'Provision timeout')).toHaveCount(0);
});

test('a link to one setting lands on it, whatever the page was filtered to', async ({ page }) => {
  // A problem that names a key used to land somebody on a list of eighty-eight
  // and leave them to find it. The filters are stood down for the link, because
  // one arriving while "Changed" is selected would otherwise open a page the
  // setting is not on.
  await openConfiguration(page);
  await page.getByText(/^Changed/).click();
  await expect(row(page, 'Provision timeout')).toHaveCount(0);

  await page.goto('/settings/configuration?setting=scheduler.provision_timeout');
  const sought = row(page, 'Provision timeout');
  await expect(sought).toBeVisible();
  // Marked, not merely scrolled to: a page that jumps and then looks exactly as
  // it did leaves somebody hunting for what moved.
  await expect(sought).toHaveClass(/sought/);
});

test('saving a setting after arriving from a link leaves the search and the scroll alone', async ({
  page,
}) => {
  // The link's effect used to read the settings it had just been handed, so
  // every save, refresh or import re-armed it: the search box emptied and the
  // page jumped back to the linked row, away from the one being edited.
  await goto(page, '/settings/configuration?setting=scheduler.provision_timeout', 'Configuration');
  await expect(row(page, 'Provision timeout')).toHaveClass(/sought/);

  const search = page.getByRole('textbox', { name: 'Search settings' });
  await search.fill('retention.webhooks');
  await change(page, 'Keep webhook deliveries for', '96h');
  await expect(search).toHaveValue('retention.webhooks');
  await expect(row(page, 'Provision timeout')).toHaveCount(0);

  // Put it back, so the next spec sees the fixture it expects.
  await row(page, 'Keep webhook deliveries for').getByRole('button', { name: 'Reset' }).click();
  await expect(row(page, 'Keep webhook deliveries for').getByText('Saved here')).toBeHidden();
  await expect(search).toHaveValue('retention.webhooks');
});

test('the search box takes the slash key, the way a list an operator reads does', async ({
  page,
}) => {
  await openConfiguration(page);
  const search = page.getByRole('textbox', { name: 'Search settings' });
  await expect(search).not.toBeFocused();
  await page.keyboard.press('/');
  await expect(search).toBeFocused();

  // And it is not swallowed while somebody is typing a value into a field,
  // which would put a stray slash in the middle of what they were editing.
  await search.fill('provision');
  await page.keyboard.press('/');
  await expect(search).toHaveValue('provision/');
});

/*
 * Which layout the host capacity map opens in is a fleet setting, and one
 * per page: the Overview is glanced at and the Hosts page is looked into, so
 * a fleet may want every host on one chart on the first and a chart per
 * host on the second. The setting is live -- the page an operator moves to
 * next opens the new way without a reload -- and it is a default rather than
 * a rule: an operator who picks the other layout keeps their pick in that
 * browser, for that page only.
 */
test('the capacity map opens in the layout the fleet chose for each page, until an operator picks', async ({
  page,
}) => {
  const label = 'Hosts capacity map layout';
  const layout = page
    .getByRole('region', { name: 'Host capacity map', exact: true })
    .getByRole('group', { name: 'Layout' });

  await openConfiguration(page);
  const target = row(page, label);
  await target.getByRole('button', { name: 'Change' }).click();
  await target.getByRole('combobox').selectOption('split');
  await target.getByRole('button', { name: /^Save / }).click();
  await expect(target.getByRole('button', { name: 'Change' })).toBeVisible();
  await expect(target.getByText('Saved here')).toBeVisible();

  try {
    // Moving to the Hosts page within the app, no reload, finds the map
    // already opening a chart per host: the change is live.
    await openSection(page, '/hosts');
    await expect(page.getByRole('heading', { level: 1, name: 'Hosts' })).toBeVisible();
    await expect(layout.getByRole('button', { name: /^Per host/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    // The Overview has its own setting, which was not touched.
    await goto(page, '/', 'Overview');
    await expect(layout.getByRole('button', { name: /^Overlay/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    );

    // An operator's own pick outlives the fleet's default in this browser,
    // and belongs to the page it was made on.
    await goto(page, '/hosts', 'Hosts');
    await layout.getByRole('button', { name: /^Overlay/ }).click();
    await reload(page, 'Hosts');
    await expect(layout.getByRole('button', { name: /^Overlay/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
  } finally {
    // Put it back, so the next spec sees the fixture it expects.
    await openConfiguration(page);
    await row(page, label).getByRole('button', { name: 'Reset' }).click();
    await expect(row(page, label).getByText('Saved here')).toBeHidden();
  }
});

/*
 * Saving with the keyboard must not throw the keyboard out of the row. A button
 * that is natively disabled while its request runs is blurred by the browser, so
 * focus falls to the document and, when the save is refused and the editor stays
 * open, the next Tab starts from the top of the page. Busy is announced with
 * aria-busy and refused in the click handler instead, as Button does.
 */
test('the save button keeps focus while a change is being saved', async ({ page }) => {
  await openConfiguration(page);

  let release: () => void = () => {};
  const held = new Promise<void>((resolve) => (release = resolve));
  await page.route('**/api/v1/settings', async (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback();
    await held;
    return route.fulfill({
      status: 422,
      contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'invalid_input', message: 'That value is refused.' } }),
    });
  });

  const target = row(page, 'Keep webhook deliveries for');
  await target.getByRole('button', { name: 'Change' }).click();
  await target.getByRole('textbox').fill('97h');

  const save = target.getByRole('button', { name: /^Save / });
  await save.focus();
  await page.keyboard.press('Enter');

  await expect(save).toHaveAttribute('aria-busy', 'true');
  await expect(save, 'focus stays on the button that was pressed').toBeFocused();

  release();
  await expect(target.getByText('That value is refused.')).toBeVisible();
  await expect(save, 'and is still there when the save is refused').toBeFocused();
  await expect(save).not.toHaveAttribute('aria-busy', 'true');
});
