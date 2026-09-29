/**
 * Every default table fits the window it is shown in.
 *
 * A table that scrolls sideways hides the column somebody is looking for behind
 * a gesture they have no reason to try, and takes the row's name off the left
 * edge the moment they do -- so the value they scrolled to belongs to a row they
 * can no longer name. The grids therefore divide whatever width there is and
 * truncate, and on a phone each row becomes a card and reads down instead.
 *
 * This is the file that holds that to be true, at the widths the product claims
 * to support: the reference desktop, the two documented breakpoints and either
 * side of them, and the two phone widths the mobile project uses.
 *
 * Two layouts are exempt, and say so where they are measured: a phone whose grids
 * are left in the row layout they start in scrolls its table inside the grid's
 * own frame, because ten columns do not divide 360 pixels. That is the trade
 * the layout exists to make and the reason Cards is one press away; what is
 * never exempt is the page, which is measured here in both layouts. The other
 * is a desktop layout the operator deliberately widened: that table scrolls in
 * its frame rather than silently shrinking some other column behind the
 * resize handle.
 */
import { expect, test, type Page } from '@playwright/test';
import {
  browserOverride,
  dataRows,
  goto,
  grid,
  SECTIONS,
  sectionHeading,
  waitForRows,
} from './support/fixtures';

test.use(browserOverride);

/** Every page whose main content is a grid, with the name its table carries. */
const GRIDS = [
  { path: '/runners', heading: 'Runners', label: 'Runners' },
  { path: '/workflows?state=completed', heading: 'Workflows', label: 'Workflow runs' },
  { path: '/jobs?state=completed', heading: 'Jobs', label: 'Jobs' },
  { path: '/pools', heading: 'Pools', label: 'Pools' },
  { path: '/queue', heading: 'Queue', label: 'Provisioning queue' },
  { path: '/audit', heading: 'Audit', label: 'Audit log' },
] as const;

/**
 * The widths that matter: the reference desktop the screenshots are taken at,
 * `--z-bp-lg` and just below it, `--z-bp-md`, and the two phone widths.
 */
const WIDTHS = [1440, 1180, 1179, 768, 412, 360] as const;

/** `--z-bp-md`, as a `max-width`: at this width and below, a row can be a card. */
const PHONE_BREAKPOINT = 768;

/** The two of those the mobile project runs at. */
const PHONES = [412, 360] as const;

/**
 * Put a phone-width grid into the card layout, the way an operator does.
 *
 * The grids start as the table at every width, so the promise this file holds
 * -- a table that fits its frame -- is the card layout's below the phone
 * threshold. Pressed rather than written into storage, because a preference
 * set behind the UI's back is a preference this suite would keep passing on
 * after the control that sets it broke.
 */
async function chooseCards(page: Page): Promise<void> {
  const cards = page.getByRole('button', { name: /^Cards\b/ });
  await cards.click();
  await expect(cards).toHaveAttribute('aria-pressed', 'true');
}

/**
 * How far the table overflows the frame around it, and how far the document
 * overflows the window.
 *
 * Both, because they fail separately: a table can stay inside a frame that is
 * itself too wide for the page, and a page can hold together while the table in
 * it is quietly cut off.
 */
async function overflow(page: Page, label: string): Promise<{ table: number; document: number }> {
  return grid(page, label).evaluate((table) => {
    const frame = table.parentElement as HTMLElement;
    return {
      table: table.scrollWidth - frame.clientWidth,
      document: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    };
  });
}

/** Every table on the page, against the box each one is drawn in. */
async function expectTablesFit(page: Page, where: string): Promise<void> {
  const measured = await page.evaluate(() =>
    [...document.querySelectorAll('table')].map((table) => ({
      caption: (table.caption?.textContent ?? table.getAttribute('aria-label') ?? '?').trim(),
      over: table.scrollWidth - (table.parentElement as HTMLElement).clientWidth,
    })),
  );
  expect(measured.length, `${where} has a table to measure`).toBeGreaterThan(0);
  for (const { caption, over } of measured) {
    expect(over, `${caption} is wider than its frame, in ${where}`).toBeLessThanOrEqual(1);
  }
}

for (const width of WIDTHS) {
  test(`no grid scrolls sideways at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });

    // `--z-bp-md` is a `max-width`, so 768 is itself a phone: the card layout
    // is offered at every width in this list up to and including it.
    const phone = width <= PHONE_BREAKPOINT;

    for (const { path, heading, label } of GRIDS) {
      await goto(page, path, heading);
      await waitForRows(grid(page, label));

      // The page is measured in whatever layout the grid starts in: a grid that
      // scrolls inside its own frame must not take the document with it.
      const started = await overflow(page, label);
      expect(started.document, `the ${heading} page scrolls sideways`).toBeLessThanOrEqual(1);

      // On a phone the table that fits is the card layout's, so it is chosen
      // before the table is measured. Above the threshold there is nothing to
      // choose -- every grid is already the table.
      if (phone) await chooseCards(page);

      // A pixel of slack: the columns are shared out by arithmetic, and a
      // fractional width rounded up is not a table anybody can scroll.
      const measured = await overflow(page, label);
      expect(measured.table, `the ${heading} table is wider than its frame`).toBeLessThanOrEqual(1);
      expect(measured.document, `the ${heading} page scrolls sideways`).toBeLessThanOrEqual(1);
    }
  });
}

/*
 * The tables are the hard half of fitting a phone, but they are not the whole
 * claim: a page that holds its tables inside the window and then puts a chart,
 * a wizard or a row of buttons through the side of it is still a page nobody
 * can read one-handed. So every section is walked at both phone widths, and
 * the document itself is measured.
 *
 * The whole document rather than a chosen element, because whatever is too wide
 * is by definition the thing nobody thought to measure -- and the failure names
 * what stuck out, so the next person does not have to go looking for it.
 */
for (const width of PHONES) {
  test(`no page scrolls sideways at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });

    for (const section of SECTIONS) {
      await goto(page, section.path, sectionHeading(section));

      // Polled, because a page settles: a grid's columns are shared out once
      // it has measured its frame, and a skeleton one frame wide is not a
      // mobile view anybody sees.
      const culprits = async (): Promise<string[]> =>
        page.evaluate(() => {
          const root = document.documentElement;
          // A pixel of slack, for the same reason the tables get one.
          if (root.scrollWidth - root.clientWidth <= 1) return [];
          return [...document.querySelectorAll<HTMLElement>('main *')]
            .filter((el) => el.getBoundingClientRect().right > root.clientWidth + 1)
            .map((el) => `${el.tagName.toLowerCase()}.${el.className?.toString().trim()}`)
            .slice(0, 5);
        });
      await expect
        .poll(culprits, { message: `${sectionHeading(section)} scrolls sideways at ${width}px` })
        .toEqual([]);
    }
  });
}

/*
 * The phone does not merely fit: it keeps everything. A grid that fitted by
 * dropping columns would answer the wrong question -- an operator woken at 3am
 * is looking at a phone, and the column they need is the one that was dropped.
 */
test('every column is still there when the rows become cards', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 780 });
  await goto(page, '/runners', 'Runners');
  const runners = grid(page, 'Runners');
  await waitForRows(runners);
  await chooseCards(page);

  // The heading row is out of the way, so each cell says what it is instead.
  const labels = await dataRows(runners)
    .first()
    .locator('td')
    .evaluateAll((cells) => cells.map((cell) => cell.getAttribute('data-label')));
  for (const heading of ['Status', 'Name', 'Pool', 'Host', 'Current job', 'CPU', 'Memory']) {
    expect(labels, `a card names its ${heading}`).toContain(heading);
  }

  // And the two things a heading row does that a card cannot are kept: the
  // sort, and the tick that takes every row on the page.
  await expect(runners.getByRole('checkbox', { name: /^Select every/ })).toBeVisible();
  await runners
    .getByRole('columnheader', { name: 'Age' })
    .getByRole('button', { name: 'Age', exact: true })
    .click();
  await expect(page).toHaveURL(/sort=created_at/);
});

test('column width and order survive reload', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await goto(page, '/runners', 'Runners');
  const runners = grid(page, 'Runners');
  await waitForRows(runners);

  const name = runners.getByRole('columnheader', { name: /Name/ });
  const before = await name.evaluate((heading) => heading.getBoundingClientRect().width);
  await name.getByRole('button', { name: 'Reposition Name column' }).press('ArrowLeft');
  await name.getByRole('button', { name: 'Resize Name column' }).press('ArrowRight');

  const headings = async () =>
    runners
      .getByRole('columnheader')
      .evaluateAll((items) => items.map((item) => item.textContent?.replace(/⋮/g, '').trim()));
  await expect.poll(headings).toEqual(expect.arrayContaining(['Name', 'Status']));
  expect((await headings()).indexOf('Name')).toBeLessThan((await headings()).indexOf('Status'));
  await expect
    .poll(() => name.evaluate((heading) => heading.getBoundingClientRect().width))
    .toBeGreaterThan(before);

  await page.reload();
  await waitForRows(grid(page, 'Runners'));
  const restored = await headings();
  expect(restored.indexOf('Name')).toBeLessThan(restored.indexOf('Status'));
  const restoredWidth = await grid(page, 'Runners')
    .getByRole('columnheader', { name: /Name/ })
    .evaluate((heading) => heading.getBoundingClientRect().width);
  expect(restoredWidth).toBeGreaterThan(before);
});

/*
 * The tables that are not grids: the usage report, and the two lists in
 * Settings. They are hand-written markup rather than `DataGrid`, so they get
 * the same treatment by hand -- and the same guarantee here, since a rule that
 * holds only where it was easy to apply is not a rule.
 *
 * Settings starts with neither an account beyond the signed-in one nor a token,
 * so each is given a row. Unique names, so a retry does not trip over a
 * leftover from a failed run.
 */
test('the tables that are not grids fit the window too', async ({ page, request }) => {
  const stamp = `${Date.now()}`;
  let userId = '';
  let tokenId = '';
  try {
    const user = await request.post('/api/v1/users', {
      data: {
        username: `e2e-tables-${stamp}`,
        password: 'correct horse battery staple',
        display_name: 'Someone With A Long Display Name',
        email: `e2e-tables-${stamp}@example.com`,
        role: 'viewer',
      },
    });
    expect(user.ok(), 'the account was created').toBeTruthy();
    userId = ((await user.json()) as { id: string }).id;
    const token = await request.post('/api/v1/tokens', {
      data: {
        name: `e2e-tables-${stamp}`,
        role: 'operator',
        scopes: ['pools:read', 'runners:read', 'jobs:read'],
        expires_in: '1h',
      },
    });
    expect(token.ok(), 'the token was created').toBeTruthy();
    tokenId = ((await token.json()) as { id: string }).id;

    for (const width of WIDTHS) {
      await page.setViewportSize({ width, height: 900 });

      await goto(page, '/usage', 'Usage');
      await expect(page.getByRole('table', { name: /^Usage by / })).toBeVisible();
      await expectTablesFit(page, `the usage report at ${width}px`);

      await goto(page, '/settings/users', 'Users');
      await expect(page.getByRole('table', { name: 'Accounts' })).toBeVisible();
      await expectTablesFit(page, `the accounts list at ${width}px`);

      await goto(page, '/settings/tokens', 'API tokens');
      await expect(page.getByRole('table', { name: 'API tokens' })).toBeVisible();
      await expectTablesFit(page, `the API tokens list at ${width}px`);
    }
  } finally {
    // A token cannot be deleted, only revoked: its row stays, marked revoked,
    // so the audit trail keeps pointing at something.
    if (tokenId) await request.delete(`/api/v1/tokens/${tokenId}`);
    if (userId) await request.delete(`/api/v1/users/${userId}`);
  }
});

/*
 * A row's menu is not cut off by the table it was opened from.
 *
 * The grid's frame scrolls, so it clips what overflows it, and the menu was
 * positioned inside that frame: on the last row of a full page an operator saw
 * the first item and a straight edge where the rest should have been. The menu
 * opens in the browser's top layer now, which is the same answer the tooltips
 * reached, so what is asserted here is the thing that was actually broken --
 * that a pointer aimed at the last item lands on it.
 */
test('a row menu opens over the table rather than inside it', async ({ page, isMobile }) => {
  test.skip(isMobile, 'the row layout and its menus; the phone gets cards');
  await goto(page, '/runners', 'Runners');
  const rows = await waitForRows(grid(page, 'Runners'));
  const last = rows.last();
  await last.scrollIntoViewIfNeeded();
  await last.getByRole('button', { name: /^Actions for/ }).click();

  const menu = page.getByRole('menu', { name: /^Actions for/ });
  await expect(menu).toBeVisible();
  const items = menu.getByRole('menuitem');
  const count = await items.count();
  expect(count, 'a runner has actions to offer').toBeGreaterThan(1);

  // A bounding box is reported whether or not an ancestor clips the paint, so
  // the question is asked of the browser the way a pointer asks it.
  for (let i = 0; i < count; i++) {
    const item = items.nth(i);
    const label = (await item.innerText()).trim();
    const box = await item.boundingBox();
    expect(box, `${label} is laid out`).not.toBeNull();
    const hit = await page.evaluate(
      ([x, y]) =>
        document
          .elementFromPoint(x as number, y as number)
          ?.closest('[role="menuitem"]')
          ?.textContent?.trim() ?? null,
      [box!.x + box!.width / 2, box!.y + box!.height / 2],
    );
    expect(hit, `${label} can be clicked where it is drawn`).toBe(label);
  }

  // And it lets go once the trigger has scrolled out of the frame it belongs
  // to: a menu left floating over rows it no longer points at is worse than
  // one that closed. The grid's scrolling frame has no role of its own -- it
  // is the div the table sits in -- so the class is how it is reached.
  await page
    .locator('.scroll')
    .first()
    .evaluate((el) => el.scrollTo({ top: 0 }));
  await expect(menu).toBeHidden();
});
