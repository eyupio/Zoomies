/**
 * Zoomies on a phone.
 *
 * docs/ui-guidelines.md makes read-only monitoring on a phone a requirement,
 * and the phone breakpoint has one rule that matters above the rest: nothing
 * scrolls sideways. An operator woken at 3am reading a dashboard one-handed
 * cannot chase a column that is off the right edge of the screen.
 *
 * These run only in the Pixel 7 project; the desktop project skips them.
 */
import { expect, test, type Locator, type Page } from '@playwright/test';
import {
  browserOverride,
  dataRows,
  documentWidth,
  FIXTURE,
  goto,
  grid,
  nav,
  menuEntry,
  navEntry,
  navMenu,
  openNavMenu,
  pageHeading,
  PRIMARY_SECTIONS,
  SECTIONS,
  sectionHeading,
} from './support/fixtures';

test.use(browserOverride);

test.skip(({ isMobile }) => !isMobile, 'the phone layout, in the Pixel 7 project');

/** How far the page can be scrolled sideways, which must be nowhere. */
async function expectNoSidewaysScroll(page: Page, where: string): Promise<void> {
  const { scrollWidth, clientWidth } = await documentWidth(page);
  expect(
    scrollWidth,
    `${where} is ${scrollWidth}px wide in a ${clientWidth}px window, so the page scrolls sideways`,
  ).toBeLessThanOrEqual(clientWidth);
}

test('the Overview is readable without scrolling sideways', async ({ page }) => {
  // This caught two real bugs: `.app` kept `align-items: flex-start` where the
  // row becomes a column, so the content column was sized to its content
  // rather than to the window, and the top bar's account trigger
  // ("Authentication disabled" on this instance) was long enough to push the
  // document past the edge on its own.

  await goto(page, '/', 'Overview');
  await expect(page.getByRole('region', { name: 'Recent events' })).toBeVisible();

  // The tiles stack rather than shrinking into unreadable columns.
  const tiles = page.getByRole('link', { name: /^(Queued jobs|Running jobs|Live runners)/ });
  await expect(tiles).toHaveCount(3);
  for (let i = 0; i < 3; i++) {
    await expect(tiles.nth(i)).toBeVisible();
  }
  // The problems summary is a line, not a panel, and the count that opens the
  // full list is in the top bar where it is on every other page too.
  await expect(page.getByText(/needs? your attention\.$/).first()).toBeVisible();
  await expect(page.getByRole('button', { name: /^Problems\./ })).toBeVisible();

  await expectNoSidewaysScroll(page, 'the Overview');
});

test('migration labels and job exceptions stay readable', async ({ page }) => {
  await goto(page, '/migrate', 'Migrate repositories');

  // The demo has one installation and preselects repositories that can move.
  // Step through Installation and Repositories to the first wide decision.
  await page.getByRole('button', { name: 'Next' }).click();
  await page.getByRole('button', { name: 'Next' }).click();

  const mapping = page.getByLabel('What replaces ubuntu-latest');
  await expect(mapping).toBeVisible();
  expect((await mapping.boundingBox())?.width).toBeGreaterThan(180);

  await page.getByRole('button', { name: 'Next' }).click();
  const build = page.getByLabel('Where build in .github/workflows/ci.yml runs, in acme/widgets');
  await expect(build).toBeVisible();
  expect((await build.boundingBox())?.width).toBeGreaterThan(180);

  // A blocked row used to be squeezed into a 30px reason column, producing
  // the one-word-per-line stack from the reported phone capture.
  const reason = page.getByText('${{ }} expression').first();
  await expect(reason).toBeVisible();
  expect((await reason.boundingBox())?.width).toBeGreaterThan(180);
  await expectNoSidewaysScroll(page, 'the migration wizard');
});

test('the navigation is a bar at the bottom, aligned and reaching every page', async ({ page }) => {
  // Ten sections, each reached from a fresh Overview and most through the sheet
  // twice, is a few dozen navigations in one test. Every step passes on a
  // loaded two-core CI runner, but together they overran the default 30s and
  // failed whichever click happened to be under way when the budget ran out.
  test.slow();
  // The reported clipping was on a portrait tablet at the inclusive edge of
  // the compact-shell breakpoint, not on the narrower phone profile.
  await page.setViewportSize({ width: 768, height: 1024 });
  await goto(page, '/', 'Overview');

  const bar = nav(page);
  await expect(bar).toBeVisible();
  const box = await bar.boundingBox();
  const viewport = page.viewportSize();
  expect(box, 'the navigation is on screen').not.toBeNull();
  expect(
    (box?.y ?? 0) + (box?.height ?? 0) / 2,
    'the navigation sits at the bottom, under the thumb',
  ).toBeGreaterThan((viewport?.height ?? 0) / 2);
  // The desktop collapse control is gone, because there is nothing to collapse.
  await expect(page.getByRole('button', { name: /the navigation/ })).toHaveCount(0);

  // Four sections and the menu button, each an equal share of the width and
  // each under its own word.
  //
  // This is the regression the whole test exists for. `.nav.collapsed` is two
  // classes and the phone rules were one, a media query adds no specificity of
  // its own, and the collapse pref defaulted to on at any width under 1180px --
  // so on a phone the bar was 56px wide with all ten entries piled into the
  // corner and the masthead sitting on top of them.
  const entries = bar.getByRole('listitem');
  await expect(entries).toHaveCount(5);
  const widths: number[] = [];
  for (let i = 0; i < 5; i++) {
    const entry = await entries.nth(i).boundingBox();
    expect(entry, 'every entry has a box').not.toBeNull();
    expect(entry?.x ?? -1, 'no entry starts off the left edge').toBeGreaterThanOrEqual(0);
    expect(
      (entry?.x ?? 0) + (entry?.width ?? 0),
      'no entry runs off the right edge',
    ).toBeLessThanOrEqual((viewport?.width ?? 0) + 1);
    widths.push(entry?.width ?? 0);
  }
  expect(
    Math.max(...widths) - Math.min(...widths),
    'the entries share the width evenly rather than bunching up',
  ).toBeLessThanOrEqual(1);
  expect(
    Math.min(...widths),
    'each entry is wide enough to be a thumb target',
  ).toBeGreaterThanOrEqual(44);
  for (const label of ['Overview', 'Pools', 'Runners', 'Workflows', 'More']) {
    await expect(bar.getByText(label, { exact: true })).toBeVisible();
  }
  // Android Chrome can overlay its gesture pill while reporting a zero CSS
  // safe-area inset. Visibility alone did not catch the resulting clipped
  // words, so the bar must retain its explicit minimum protected edge.
  const bottomPadding = await bar.evaluate((element) =>
    Number.parseFloat(getComputedStyle(element).paddingBottom),
  );
  expect(
    bottomPadding,
    'the navigation protects labels from the system gesture area',
  ).toBeGreaterThanOrEqual(16);

  // Every section is one press away from the Overview: the four in the bar
  // directly, the other six through the menu the fifth entry opens.
  //
  // Each hop starts from the Overview on purpose: a press from a page that has
  // been scrolled tells you less than a press from a known one.
  for (const section of SECTIONS) {
    await goto(page, '/', 'Overview');
    if (PRIMARY_SECTIONS.includes(section.path)) {
      await navEntry(page, section.path).click();
      await expect(pageHeading(page, sectionHeading(section))).toBeVisible();
      await expect(navEntry(page, section.path)).toHaveAttribute('aria-current', 'page');
      continue;
    }
    await openNavMenu(page);
    await menuEntry(page, section.path).click();
    await expect(pageHeading(page, sectionHeading(section))).toBeVisible();
    // Choosing a section closes the menu: nobody wants to dismiss a menu they
    // have already used.
    await expect(navMenu(page)).toHaveCount(0);
    // And the menu says where you are when it is opened again.
    await openNavMenu(page);
    await expect(menuEntry(page, section.path)).toHaveAttribute('aria-current', 'page');
    await page.keyboard.press('Escape');
    await expect(navMenu(page)).toHaveCount(0);
  }
});

test('the sheet closes on Escape and on the scrim, and lists every section', async ({ page }) => {
  await goto(page, '/', 'Overview');

  const menu = await openNavMenu(page);
  for (const section of SECTIONS) {
    await expect(menuEntry(page, section.path)).toBeVisible();
  }
  // The masthead a phone otherwise never sees, since the bottom bar has no room
  // for one -- and the account and the theme, which the desktop keeps in the
  // top bar's menu and a phone has no other room for.
  await expect(menu.getByText('Zoomies', { exact: true })).toBeVisible();
  await expect(menu.getByRole('group', { name: 'Theme' })).toBeVisible();
  await expect(menu.getByRole('button', { name: 'Sign out' })).toBeVisible();

  await page.keyboard.press('Escape');
  await expect(navMenu(page)).toHaveCount(0);

  await openNavMenu(page);
  // The scrim is the part of the overlay outside the sheet, which rises from
  // the bottom edge; pressing near the top of the screen is pressing it.
  const size = page.viewportSize();
  await page.mouse.click((size?.width ?? 400) / 2, 24);
  await expect(navMenu(page)).toHaveCount(0);
});

test('the grids stay inside the screen instead of overflowing it', async ({ page }) => {
  // This one caught the subtler half of the same problem. Even once the column
  // stretched, a visually-hidden `.sr-only` span in a cell 500px along a
  // 1200px table was positioned against the page rather than against the
  // grid's own scroll frame, escaped its clipping, and made the document that
  // wide -- so the page scrolled sideways, and Chrome zoomed out to fit until
  // the toolbar and the row actions could not be pressed.

  for (const [path, heading, label] of [
    ['/runners', 'Runners', 'Runners'],
    ['/workflows', 'Workflows', 'Workflow runs'],
    ['/jobs', 'Jobs', 'Jobs'],
    ['/pools', 'Pools', 'Pools'],
  ] as const) {
    await goto(page, path, heading);
    await expect(dataRows(grid(page, label)).first()).toBeVisible();

    // The page must not scroll sideways, and nor may the grid: at this width
    // its rows are cards. `tables.spec.ts` is where that is held to be true at
    // every width; what this one guards is the page around it.
    await expectNoSidewaysScroll(page, `the ${heading} grid`);
  }
});

/*
 * The grids above were the list pages. A detail page is the other half of the
 * phone story and had no test at all, which is how this shipped: a pool page
 * on a 412px phone was 500-odd pixels wide, so the name was cut off mid-word,
 * the runner cards were clipped on the left, and Delete sat off the right edge
 * where it could not be pressed.
 *
 * The cause was one row shared by every detail page. PageHeader's actions row
 * is a flex row that never wrapped, and the buttons in it do not wrap their
 * own labels, so five of them -- refresh, prewarm, disable, edit, delete --
 * demanded their full width and pushed the document out. Giving it `width:
 * 100%` on a phone made the row wide without letting it wrap, which is why the
 * breakpoint that was already there did not save it.
 *
 * Every detail page with an actions row is walked, not just the pool one that
 * was reported, because the row is the same row on all of them.
 */
test('a detail page stays inside the screen, actions and all', async ({ page }) => {
  for (const [path, heading] of [
    [`/pools`, 'Pools'],
    [`/runners/${FIXTURE.busyRunnerId}`, FIXTURE.busyRunner],
  ] as const) {
    await goto(page, path, heading);
    await expectNoSidewaysScroll(page, `the ${heading} page`);
  }

  // The pool page is reached the way an operator reaches it, through the row,
  // since its id is not fixed by the seed the way the busy runner's is.
  await goto(page, '/pools', 'Pools');
  await page.getByRole('link', { name: FIXTURE.linuxPool }).first().click();
  await expect(pageHeading(page, FIXTURE.linuxPool)).toBeVisible();
  await expectNoSidewaysScroll(page, `the ${FIXTURE.linuxPool} page`);

  // The document fitting is necessary but not sufficient: a button can still
  // sit past the right edge of a page that does not scroll, and one that does
  // is one an operator on a phone cannot press. It was Delete that ended up
  // there -- last in the row, and the one whose being half off the screen is
  // worst in both directions. Every visible control is checked rather than
  // those four by name, since the row is built from whatever the page passes.
  const width = (await documentWidth(page)).clientWidth;
  const offscreen = await page.evaluate((limit) => {
    const out: string[] = [];
    for (const el of document.querySelectorAll('button, a[href]')) {
      const box = el.getBoundingClientRect();
      if (box.width === 0 && box.height === 0) continue;
      if (box.right > limit + 0.5 || box.left < -0.5) {
        out.push(
          `${(el.textContent ?? '').trim() || el.getAttribute('aria-label')} at ${Math.round(box.left)}..${Math.round(box.right)}`,
        );
      }
    }
    return out;
  }, width);
  expect(offscreen, `these controls are outside a ${width}px window`).toEqual([]);
});

/*
 * The usage report is ten columns wide, which is not a table on a 412px phone
 * however it scrolls: the heading is cut off mid-word, every pool reads
 * "zoomies-demo-lin..." and is indistinguishable from the next, and the figure
 * you scrolled to belongs to a row you can no longer name. On a phone each row
 * is a card instead, so this holds the three things that makes it readable --
 * the page does not scroll sideways, no row name is truncated, and every
 * figure carries its own heading.
 */
test('the usage report reads as cards on a phone, with nothing cut off', async ({ page }) => {
  for (const grouping of ['pool', 'repository', 'workflow', 'installation'] as const) {
    await goto(page, `/usage?group_by=${grouping}`, 'Usage');
    await expect(page.getByRole('table', { name: /^Usage by / })).toBeVisible();
    await expectNoSidewaysScroll(page, `the usage report grouped by ${grouping}`);
  }

  await goto(page, '/usage', 'Usage');
  // The table's rows and headers, not the activity matrix's: that is a grid
  // of its own above the report, with rows of squares rather than of figures.
  const report = page.getByRole('table', { name: /^Usage by / });
  const names = report.getByRole('rowheader');
  await expect(names.first()).toBeVisible();

  // Every row's name is rendered in full. Truncation here is what made two
  // different pools read as the same row.
  //
  // A name longer than the card wraps instead of being cut. Asserted on the
  // rule rather than on the rendering, because every name the demo fleet has
  // fits either way -- a measured check here would pass whatever the CSS said,
  // and the pools that read alike on a real fleet have longer names than these.
  const nowrap = await names.evaluateAll((els) =>
    els
      .flatMap((el) => [el, ...Array.from(el.querySelectorAll<HTMLElement>('*'))])
      .filter((el) => {
        const style = getComputedStyle(el);
        return style.whiteSpace === 'nowrap' && style.overflow === 'hidden';
      })
      .map((el) => el.textContent ?? ''),
  );
  expect(nowrap, 'a row name is still held to one clipped line').toEqual([]);

  // And every figure says what it is, because the column headings are gone.
  // The rendered pseudo-element is what is checked, not the attribute it is
  // drawn from: a rule that stopped drawing it would leave the attribute in
  // place and a column of unexplained numbers on screen.
  const unlabelled = await page.getByRole('cell').evaluateAll((els) =>
    els
      .filter((el) => {
        const label = getComputedStyle(el, '::before').content;
        return !label || label === 'none' || label === '""';
      })
      .map((el) => el.textContent ?? ''),
  );
  expect(unlabelled, 'a figure has no heading of its own on a phone').toEqual([]);

  // And the figures stack rather than running across, which is the whole of
  // what makes ten columns fit: two cells of one row share a left edge and sit
  // at different heights. Laid out as a table row they would do the opposite.
  const first = report.getByRole('row').nth(1).getByRole('cell');
  const a = await first.nth(0).boundingBox();
  const b = await first.nth(1).boundingBox();
  expect(a && b, 'the first row has no figures to lay out').toBeTruthy();
  expect(b!.y, 'the figures are laid out across the row, not down the card').toBeGreaterThan(a!.y);
  expect(Math.abs(b!.x - a!.x), 'the figures do not share a left edge').toBeLessThan(2);

  // The card holds its contents off its own edge by more than the hairline of
  // its border -- a row whose text starts on its own boundary reads as a wall
  // of figures rather than as a card.
  const card = await report.getByRole('row').nth(1).boundingBox();
  expect(a!.x - card!.x, 'the card gives its contents no room off its own edge').toBeGreaterThan(4);
});

test('a grid is still usable at this width', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  const rows = dataRows(grid(page, 'Runners'));
  await expect(rows.first()).toBeVisible();

  // Whatever the layout does, the rows are there and one can be opened. Six
  // of the seeded runners survive indefinitely; the rest depend on how long
  // the controller has been up. See runners.spec.ts.
  expect(await rows.count()).toBeGreaterThanOrEqual(6);
  const name = (await rows.first().getByRole('link').first().innerText()).trim();
  await rows.first().getByRole('link', { name }).click();
  await expect(pageHeading(page, name)).toBeVisible();
  await expect(page.getByRole('region', { name: 'Timeline' })).toBeVisible();
});

test('the Settings tables stay inside the screen once they have rows', async ({ page }) => {
  // The join-tokens table caught this one: a table wider than a phone scrolls
  // inside its own box, yet mobile Chrome still counted the clipped part
  // towards the page's width, grew the layout viewport to fit, and the fixed
  // bottom navigation grew with it -- so the whole page scrolled sideways. The
  // accounts and API-token tables are the same shape, and the test server
  // starts with neither, so each is given a row here. Unique names, so a retry
  // does not trip over a leftover from a failed run.
  const stamp = Date.now();
  let userId = '';
  let tokenId = '';
  try {
    const user = await page.request.post('/api/v1/users', {
      data: {
        username: `e2e-user-${stamp}`,
        password: 'correct horse battery staple',
        display_name: 'Someone With A Long Display Name',
        email: `e2e-user-${stamp}@example.com`,
        role: 'viewer',
      },
    });
    expect(user.ok(), 'the account was created').toBeTruthy();
    userId = ((await user.json()) as { id: string }).id;
    const token = await page.request.post('/api/v1/tokens', {
      data: {
        name: `e2e-token-${stamp}`,
        role: 'operator',
        scopes: ['pools:read', 'runners:read', 'jobs:read'],
        expires_in: '1h',
      },
    });
    expect(token.ok(), 'the token was created').toBeTruthy();
    tokenId = ((await token.json()) as { id: string }).id;

    await goto(page, '/settings/users', 'Users');
    const accounts = page.getByRole('table', { name: 'Accounts' });
    await expect(accounts.getByRole('row').filter({ hasText: `e2e-user-${stamp}` })).toBeVisible();
    await expectNoSidewaysScroll(page, 'the Settings page listing an account');

    await goto(page, '/settings/tokens', 'API tokens');
    const tokens = page.getByRole('table', { name: 'API tokens' });
    await expect(tokens.getByRole('row').filter({ hasText: `e2e-token-${stamp}` })).toBeVisible();
    await expectNoSidewaysScroll(page, 'the Settings page listing an API token');
  } finally {
    // A token cannot be deleted, only revoked: its row stays, marked revoked,
    // so the audit trail keeps pointing at something.
    if (tokenId) await page.request.delete(`/api/v1/tokens/${tokenId}`);
    if (userId) await page.request.delete(`/api/v1/users/${userId}`);
  }
});

test('every text control is at least 16px, so tapping one does not zoom the page', async ({
  page,
}) => {
  // The rule the guidelines give in section 1.2, checked where it applies.
  // Mobile Safari zooms the whole viewport whenever a focused control's text
  // is under 16px, and the viewport meta deliberately sets no maximum-scale --
  // so a field a pixel under the line jumps a 360px page to roughly 410px of
  // effective width and runs the card off both edges, once per tap. Three
  // controls were written outside the primitives and missed it entirely: the
  // page-size select, the date range, and the pool wizard's label field.
  const PAGES = [
    { path: '/runners', heading: 'Runners' },
    { path: '/workflows', heading: 'Workflows' },
    { path: '/jobs', heading: 'Jobs' },
    { path: '/usage', heading: 'Usage' },
    { path: '/audit', heading: 'Audit' },
    { path: '/pools/new', heading: 'Create a pool' },
  ] as const;

  for (const { path, heading } of PAGES) {
    await goto(page, path, heading);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();

    const small = await page.evaluate(() =>
      Array.from(document.querySelectorAll('input, select, textarea'))
        .filter((el) => {
          const type = el.getAttribute('type');
          // Checkboxes, radios and range sliders have no editable text to zoom to.
          if (type === 'checkbox' || type === 'radio' || type === 'range' || type === 'hidden')
            return false;
          if (!(el as HTMLElement).offsetParent && el.getClientRects().length === 0) return false;
          return Number.parseFloat(getComputedStyle(el).fontSize) < 16;
        })
        .map((el) => `${el.tagName.toLowerCase()}[${el.getAttribute('aria-label') ?? el.id}]`),
    );
    expect(small, `${path}: every text control is 16px or more on a phone`).toEqual([]);
  }
});

/*
 * The reported bug, and the class it belongs to.
 *
 * A mobile browser answers a page that overflows sideways by laying the whole
 * thing out in a wider block and showing it smaller, and every `position:
 * fixed` overlay is laid out against that block rather than against the
 * screen. One row a few pixels past the edge is therefore not one row: the
 * problems drawer sized itself against an 800px block, anchored to the
 * right-hand end of it, and an operator on a 412px phone was left looking at a
 * sliver of a panel whose close button was off the side of the screen. The
 * side menu, the command palette, the dialogs and the bottom navigation all
 * sit in the same block and all go with it.
 *
 * The page is widened here deliberately rather than by finding a page that
 * does it, because the fix is not "no page is ever too wide" -- the rest of
 * this file is what protects that. It is that an overlay covers the window
 * whatever the page underneath it has done. Each one is opened first and the
 * page widened under it, which is both the order an operator meets this in and
 * the only order a test can use: once the page is laid out at twice the size,
 * the bottom bar is past the foot of the window as far as a click is
 * concerned.
 */
test('an overlay covers the window, not a page that has outgrown it', async ({ page }) => {
  await goto(page, '/', 'Overview');
  const window = (await documentWidth(page)).clientWidth;

  /** Puts a row past the right edge, the way a wide table or a long name does. */
  async function widenThePage(): Promise<void> {
    await page.evaluate((width) => {
      if (document.getElementById('too-wide')) return;
      const filler = document.createElement('div');
      filler.id = 'too-wide';
      filler.style.cssText = `width:${width * 2}px;height:8px`;
      document.querySelector('main')?.append(filler);
    }, window);
    const { scrollWidth, clientWidth } = await documentWidth(page);
    expect(scrollWidth, 'the page really is wider than the window now').toBeGreaterThan(
      clientWidth,
    );
  }

  /**
   * And put it back, so the next overlay is opened from a normal page.
   *
   * Necessary rather than tidy: while the page is wide the browser lays
   * everything out at twice the size, which puts the bottom bar past the foot
   * of the window and out of reach of a press.
   */
  async function narrowThePage(): Promise<void> {
    await page.evaluate(() => document.getElementById('too-wide')?.remove());
    await expectNoSidewaysScroll(page, 'the page with the filler taken out again');
  }

  /** The overlay is no wider than the screen, and starts at its edge. */
  async function expectWithinTheWindow(what: Locator, name: string): Promise<void> {
    // Measured once it has finished sliding in: these panels arrive from off
    // the edge, and mid-flight is the animation rather than the rule.
    await what.evaluate(async (node) => {
      await Promise.all(node.getAnimations().map((animation) => animation.finished));
    });
    const box = await what.boundingBox();
    expect(box, `${name} is on screen`).not.toBeNull();
    expect(box!.x, `${name} starts off the left edge`).toBeGreaterThanOrEqual(-0.5);
    expect(box!.width, `${name} is wider than the screen`).toBeLessThanOrEqual(window + 0.5);
  }

  await page.getByRole('button', { name: /^Problems\./ }).click();
  const drawer = page.getByRole('dialog', { name: 'Problems' });
  await expect(drawer).toBeVisible();
  await widenThePage();
  await expectWithinTheWindow(drawer, 'the problems drawer');
  // And it is the whole window rather than a column down one side of it: a
  // phone has no room for a panel beside the page it came from.
  expect((await drawer.boundingBox())!.width, 'the drawer fills the phone').toBeCloseTo(window, 0);
  // The bar along the bottom is laid out in the same block and was the other
  // half of the reported capture: five entries spread across a page twice the
  // width of the screen, with three of them off the right of it.
  await expectWithinTheWindow(nav(page), 'the bottom navigation');
  await narrowThePage();
  await page.keyboard.press('Escape');
  await expect(drawer).toHaveCount(0);

  const menu = await openNavMenu(page);
  await widenThePage();
  await expectWithinTheWindow(menu, 'the side menu');
  await narrowThePage();
  await page.keyboard.press('Escape');
  await expect(navMenu(page)).toHaveCount(0);

  await page.keyboard.press('Control+k');
  const palette = page.getByRole('dialog', { name: 'Command palette' });
  await expect(palette).toBeVisible();
  await widenThePage();
  await expectWithinTheWindow(palette, 'the command palette');
  await narrowThePage();
  await page.keyboard.press('Escape');
});

/*
 * The same rule as the tests above, at the width most Android phones actually
 * are. The Pixel 7 these run on is 412px, which turned out to be the forgiving
 * end of the range: a settings row gives its key 15rem and puts the value
 * beside it, which at 412px leaves Change just enough room and at 360px leaves
 * it hanging over the edge -- taking the document, and every fixed overlay
 * laid out against it, with it.
 */
test('every section fits a 360px phone, not just the one these tests emulate', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 780 });

  for (const section of SECTIONS) {
    await goto(page, section.path, sectionHeading(section));
    await expectNoSidewaysScroll(page, `${section.label} at 360px`);
  }

  // Settings is a section of pages, and the one that holds the rows is not
  // the one the list opens on.
  for (const [path, heading] of [
    ['/settings/account', 'Account'],
    ['/settings/appearance', 'Appearance'],
    ['/settings/events', 'Events'],
    ['/settings/users', 'Users'],
    ['/settings/tokens', 'API tokens'],
    ['/settings/configuration', 'Configuration'],
    ['/settings/backups', 'Backups'],
    ['/settings/about', 'About'],
  ] as const) {
    await goto(page, path, heading);
    await expectNoSidewaysScroll(page, `the Settings ${heading} page at 360px`);
  }
});

/*
 * A settings row is a sentence with a switch beside it, and on a phone the
 * switch goes under the sentence. It went a screenful under it: the text
 * column carries `flex: 1 1 20rem` so that it keeps a readable width next to
 * the control, and where the row becomes a column that basis stops being a
 * width and becomes a height -- 320px of empty box under every description.
 * Ten categories of that is ten screens of scrolling for ten switches.
 */
test('a settings switch sits under the sentence it belongs to, not a screen below it', async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 780 });

  for (const [path, heading] of [
    ['/settings/events', 'Events'],
    ['/settings/appearance', 'Appearance'],
  ] as const) {
    await goto(page, path, heading);
    const rows = page.locator('.setting');
    const count = await rows.count();
    expect(count, `the ${heading} page has settings rows`).toBeGreaterThan(0);

    for (let i = 0; i < count; i++) {
      // Measured against the text block's own last line rather than against
      // the control, because the empty space is *inside* the text block: the
      // box keeps its 20rem and the sentence sits at the top of it.
      const slack = await rows
        .nth(i)
        .locator('.text')
        .evaluate((text) => {
          const last = text.lastElementChild ?? text;
          return Math.round(
            text.getBoundingClientRect().bottom - last.getBoundingClientRect().bottom,
          );
        });
      expect(
        slack,
        `${heading} row ${i} holds ${slack}px of nothing under its description`,
      ).toBeLessThanOrEqual(8);
    }
  }
});

/*
 * Names Zoomies did not choose: a host enrolled as
 * `ip-10-0-31-44.eu-west-1.compute.internal`, a workflow job called whatever
 * its author called it. Both are wider than a phone with nowhere to break, and
 * both used to be rendered in something that refuses to wrap -- a badge, or a
 * drawer's heading. The badge took the page sideways; the heading kept its
 * width and pushed the drawer's Close off the panel, which on a phone is the
 * only way out of it an operator can see.
 *
 * The names are put into the responses rather than into the fleet, for the
 * reason hostile-input.spec.ts gives: what is under test is what the page does
 * with a name it is given.
 */
test('a name nobody chose wraps rather than taking the page or Close off the screen', async ({
  page,
}) => {
  // At the width of the phone above it, rather than the one these tests
  // emulate: the badge fits a fifty-character hostname at 412px and hangs over
  // the edge at 360px, which is the width most of them are.
  await page.setViewportSize({ width: 360, height: 780 });
  const hostile = 'ip-10-0-31-44.eu-west-1.compute.internal.example.invalid';

  await page.route('**/api/v1/hosts**', async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    if (Array.isArray(body.items)) for (const host of body.items) host.name = hostile;
    await route.fulfill({ response, json: body });
  });
  await page.route('**/api/v1/jobs**', async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    if (Array.isArray(body.items)) for (const job of body.items) job.job_name = hostile;
    await route.fulfill({ response, json: body });
  });

  // The pool wizard names every host that would match, in a badge each. The
  // hosts step is the advanced path's, so the fork is answered on the way.
  await goto(page, '/pools/new', 'Create a pool');
  await page.getByRole('radio', { name: 'Advanced' }).check();
  for (let step = 0; step < 3; step++) await page.getByRole('button', { name: 'Next' }).click();
  await expect(page.getByText(hostile).first()).toBeVisible();
  await expectNoSidewaysScroll(page, 'the pool wizard naming a long host');

  // The job drawer is titled with the job's name.
  await goto(page, '/jobs', 'Jobs');
  const row = dataRows(grid(page, 'Jobs')).first();
  await expect(row).toContainText(hostile);
  await row.click();
  const drawer = page.getByRole('dialog', { name: hostile });
  await expect(drawer).toBeVisible();
  const close = drawer.getByRole('button', { name: 'Close' });
  const box = await close.boundingBox();
  const panel = await drawer.boundingBox();
  expect(box, 'the drawer has a close button').not.toBeNull();
  expect(
    box!.x + box!.width,
    'Close was pushed off the side of the panel by the title',
  ).toBeLessThanOrEqual(panel!.x + panel!.width + 0.5);
  await expectNoSidewaysScroll(page, 'the job drawer titled with a long name');
});

/*
 * And the addresses nobody chose either.
 *
 * A backup destination is an S3 endpoint, a bucket and a prefix, and a
 * Cloudflare R2 endpoint is a thirty-two character account id in front of a
 * hostname: fifty-six characters with nowhere to break. The destination sat in
 * a grid whose items keep their min-content width by default, so the card was
 * held open at four hundred pixels and took the settings page sideways with
 * it -- the page an operator reaches for on a phone precisely when the fleet
 * is in trouble.
 */
test('a backup destination with a long address stays inside the screen', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 780 });
  const endpoint = 'https://0c3c36183dc3e0714e8de12c040ba3d5.r2.cloudflarestorage.com';

  await page.route('**/api/v1/backups', async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.remotes = [
      {
        name: 'zoomies',
        where: 'zoomies-backups-eu-west-production/controllers/zoomies-primary',
        endpoint,
        encrypted: false,
        keep: 30,
        disabled: false,
        source: 'database',
        shadowed: false,
        has_secret_key: true,
        uploading: false,
        copies: 30,
        bytes: 11_534_336,
        path_style: null,
        listed_at: new Date().toISOString(),
        last_upload_at: new Date().toISOString(),
        last_upload_id: 'zoomies-20260916-201754',
      },
    ];
    await route.fulfill({ response, json: body });
  });

  await goto(page, '/settings/backups', 'Backups');
  await expect(page.getByText(endpoint)).toBeVisible();
  await expectNoSidewaysScroll(page, 'the Backups page with a long destination address');
});

/*
 * The reading under the capacity map is a table: the host in the first column
 * and a figure per measurement after it. The name column was a flex container
 * with a 14rem name in it, which takes the cell out of the table's own layout
 * -- so a host called zoomies-8vcpu-31gb-ubuntu-2404-ollama1 held the column
 * open at its full width and pushed CPU and MEMORY off the right of the card.
 * The figures are what the reading is for, so the part that went was the
 * whole point of it.
 */
test('the capacity reading keeps its figures on the card whatever the hosts are called', async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 780 });
  const names = ['zoomies-12vcpu-31gb-debian-12-zoomies', 'zoomies-8vcpu-31gb-ubuntu-2404-ollama1'];

  await page.route('**/api/v1/hosts**', async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    if (Array.isArray(body.items)) {
      body.items.forEach((host: { name: string }, i: number) => (host.name = names[i % 2]!));
    }
    await route.fulfill({ response, json: body });
  });

  await goto(page, '/hosts', 'Hosts');
  const map = page.getByRole('region', { name: 'Host capacity map', exact: true });
  const chart = map.getByRole('img').first();
  await chart.scrollIntoViewIfNeeded();
  await chart.tap();

  const card = map.locator('.card');
  await expect(card).toBeVisible();
  const table = card.locator('table');
  const tableBox = await table.boundingBox();
  const cardBox = await card.boundingBox();
  expect(
    tableBox!.x + tableBox!.width,
    'the measurements are off the right of the reading, which is what the reading is for',
  ).toBeLessThanOrEqual(cardBox!.x + cardBox!.width + 0.5);
  await expectNoSidewaysScroll(page, 'the Hosts page reading long host names');
});

/*
 * The runner lifecycle wrapped as a row of flex items, so each card was as
 * wide as its own label: on a phone "Kitting up" and "Checking the
 * collar" sat side by side at different sizes, the row under them lined up
 * with neither, and the arrow that should point at the next step pointed off
 * the end of the row at nothing. It is a grid now, so the steps stay in step
 * at every width.
 */
test('the runner lifecycle keeps its steps in columns when it wraps', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 780 });
  await goto(page, '/runners', 'Runners');

  const steps = page.getByRole('list', { name: 'Runner lifecycle, in order' });
  const cards = steps.getByRole('link');
  await expect(cards).toHaveCount(5);

  const boxes = await cards.evaluateAll((els) =>
    els.map((el) => {
      const box = el.getBoundingClientRect();
      const head = el.querySelector('.head') as HTMLElement;
      return { x: box.x, width: box.width, clipped: head.scrollWidth > head.clientWidth + 1 };
    }),
  );

  // Two columns, so every card is one width and sits at one of two offsets.
  // The first two cards are the columns, and the count above has already
  // proved there are five of them.
  const [left, right] = boxes;
  expect(left, 'the lifecycle rendered no cards to measure').toBeDefined();
  expect(right, 'the lifecycle rendered a single column').toBeDefined();
  const width = left!.width;
  for (const [i, box] of boxes.entries()) {
    expect(Math.abs(box.width - width), `step ${i} is a different width`).toBeLessThanOrEqual(1);
    const column = i % 2 === 0 ? left!.x : right!.x;
    expect(Math.abs(box.x - column), `step ${i} starts out of column`).toBeLessThanOrEqual(1);
  }

  // A card that is one width is no use if the label inside it is cut in half.
  expect(boxes.filter((b) => b.clipped)).toEqual([]);

  // The arrow at the end of a row, and the one past the last step, point at
  // nothing, so they are held in the layout rather than drawn.
  const arrows = await steps
    .locator('.arrow')
    .evaluateAll((els) => els.map((el) => getComputedStyle(el).visibility));
  expect(arrows).toEqual(['visible', 'hidden', 'visible', 'hidden', 'hidden']);
});

/*
 * The two phone layouts, and who decides between them.
 *
 * Rows are the default at every width: a fleet is scanned rather than read,
 * one line per runner is what that wants, and a grid that keeps its shape on a
 * phone is a page an operator already knows. Cards are the other half -- ten
 * columns in 412 pixels say nothing each, and a card gives every value a line
 * of its own -- so both are on offer: a toggle above each grid for the page,
 * and a default in Settings for the rest. The trade in the row layout is the
 * sideways scroll, and it is the grid's own frame that takes it: the page
 * around it never does.
 */

/** How far the table overflows its frame, and the document its window. */
async function gridOverflow(
  page: Page,
  label: string,
): Promise<{ table: number; document: number }> {
  return grid(page, label).evaluate((table) => {
    const frame = table.parentElement as HTMLElement;
    return {
      table: table.scrollWidth - frame.clientWidth,
      document: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    };
  });
}

/**
 * The layout toggle above a grid, as the operator presses it. A segmented
 * button is named for the label it shows and then described, so the label is
 * what to ask for -- matching the description too would tie this to wording
 * that is free to change.
 */
function layout(page: Page, choice: 'Cards' | 'Rows'): Locator {
  return page.getByRole('button', { name: new RegExp(`^${choice}\\b`) });
}

test('a grid can be read as cards instead of rows, and remembers which', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  const rows = dataRows(grid(page, 'Runners'));
  await expect(rows.first()).toBeVisible();

  // The table to begin with: one line per runner, and the columns at the widths
  // they declare -- wider than the phone, so the frame has something to scroll.
  const line = (await rows.first().boundingBox())!;
  const overflow = await gridOverflow(page, 'Runners');
  expect(overflow.table, 'the table fits, so there is nothing to scroll to').toBeGreaterThan(0);
  expect(
    overflow.document,
    'the grid took the page sideways with it instead of scrolling inside its own frame',
  ).toBeLessThanOrEqual(0);
  await expectNoSidewaysScroll(page, 'the Runners grid in the row layout');

  await layout(page, 'Cards').click();

  // Every value has a line of its own now, so a row is many lines tall and the
  // table is exactly as wide as the screen.
  const card = (await rows.first().boundingBox())!;
  expect(card.height, 'a card is one line tall, so it is not a card').toBeGreaterThan(100);
  expect(line.height, 'the row layout was already stacking values').toBeLessThan(card.height / 2);
  expect((await gridOverflow(page, 'Runners')).table).toBeLessThanOrEqual(1);

  // The choice is the operator's and outlives the visit.
  await page.reload();
  await expect(pageHeading(page, 'Runners')).toBeVisible();
  await expect(layout(page, 'Cards')).toHaveAttribute('aria-pressed', 'true');
  expect((await dataRows(grid(page, 'Runners')).first().boundingBox())!.height).toBeGreaterThan(
    100,
  );

  // It is this grid's choice, not every grid's: Jobs has made none, so it is
  // still the table.
  await goto(page, '/jobs', 'Jobs');
  await expect(layout(page, 'Rows')).toHaveAttribute('aria-pressed', 'true');

  // And back again: a layout that can be chosen and not unchosen is a trap.
  await goto(page, '/runners', 'Runners');
  await layout(page, 'Rows').click();
  await expect(layout(page, 'Rows')).toHaveAttribute('aria-pressed', 'true');
});

/** The Appearance page's choice of how a grid lays a row out on a phone. */
const tablesDefault = (page: Page, name: 'Rows' | 'Cards') =>
  page.getByRole('group', { name: 'Tables on a phone' }).getByRole('button', { name });

test('the Settings default decides for every grid that has not chosen', async ({ page }) => {
  await goto(page, '/settings/appearance', 'Appearance');
  await tablesDefault(page, 'Cards').click();
  await expect(tablesDefault(page, 'Cards')).toHaveAttribute('aria-pressed', 'true');

  // Neither Jobs nor Pools has a choice of its own, so both follow the default
  // -- and each says so on its own toggle, which is where an operator would go
  // to change that one back.
  for (const [path, heading, label] of [
    ['/jobs', 'Jobs', 'Jobs'],
    ['/pools', 'Pools', 'Pools'],
  ] as const) {
    await goto(page, path, heading);
    await expect(dataRows(grid(page, label)).first()).toBeVisible();
    await expect(layout(page, 'Cards')).toHaveAttribute('aria-pressed', 'true');
    expect((await gridOverflow(page, label)).table).toBeLessThanOrEqual(1);
    await expectNoSidewaysScroll(page, `the ${heading} grid following the card default`);
  }

  // The default goes back too, and takes the grids following it with it.
  await goto(page, '/settings/appearance', 'Appearance');
  await tablesDefault(page, 'Rows').click();
  await expect(tablesDefault(page, 'Rows')).toHaveAttribute('aria-pressed', 'true');
  await goto(page, '/jobs', 'Jobs');
  await expect(layout(page, 'Rows')).toHaveAttribute('aria-pressed', 'true');
});

/*
 * The capacity map read the chart by hover, and a finger cannot hover: the
 * reading appeared under the fingertip and was gone the moment it lifted.
 * On a phone a tap chooses the moment, the reading sits under the chart
 * rather than under the finger, and every control on the map is tall
 * enough to hit.
 */
test('the host capacity map is read by touch', async ({ page }) => {
  await goto(page, '/hosts', 'Hosts');
  const map = page.getByRole('region', { name: 'Host capacity map', exact: true });
  const chart = map.getByRole('img').first();
  await expect(chart).toBeVisible();

  // A tap on the chart chooses a moment, and the reading stays put after the
  // finger has gone, below the chart where it can be read.
  await chart.tap();
  const card = map.locator('.card');
  await expect(card).toBeVisible();
  const chartBox = await chart.boundingBox();
  const cardBox = await card.boundingBox();
  expect(cardBox!.y, 'the reading sits under the chart, not on it').toBeGreaterThanOrEqual(
    chartBox!.y + chartBox!.height - 1,
  );
  expect(cardBox!.x + cardBox!.width).toBeLessThanOrEqual(chartBox!.x + chartBox!.width + 1);
  await map.getByRole('button', { name: 'Back to now' }).tap();
  await expect(card).toBeHidden();

  // Every target is finger-sized: the window choice, the measurement chips,
  // the hosts' switches and the per-host actions.
  const targets = [
    map.getByRole('group', { name: 'Window' }).getByRole('button', { name: '1m' }),
    map.getByRole('group', { name: 'Measurements shown' }).getByRole('button').first(),
    map.getByRole('group', { name: 'Hosts shown' }).getByRole('button').first(),
    map.getByRole('button', { name: 'Only' }).first(),
    map.getByRole('link', { name: /^History/ }).first(),
  ];
  for (const target of targets) {
    const box = await target.boundingBox();
    expect(box, 'the target is on the page').not.toBeNull();
    expect(box!.height, `${await target.innerText()} is too short to tap`).toBeGreaterThanOrEqual(
      44,
    );
  }
  await expectNoSidewaysScroll(page, 'the Hosts page with the capacity map');
});

/*
 * And the fleet trend is the same story: it read the chart by hover, and the
 * only way to a figure on a phone was the slider. A tap chooses the moment
 * here too, the reading sits under the chart rather than under the finger,
 * and the chips that put a figure on the chart are finger-sized.
 */
test('fleet activity is read by touch', async ({ page }) => {
  await goto(page, '/runners', 'Runners');
  await page.getByText('Explore fleet activity over the last 24 hours', { exact: true }).tap();
  const trend = page.getByRole('region', { name: 'Fleet activity', exact: true });
  const chart = trend.getByRole('img').first();
  await expect(chart).toBeVisible();

  await chart.tap();
  // The card has no role of its own -- it is the same reading the rows below
  // carry, put beside the crosshair -- so the class is how it is reached.
  const card = trend.locator('.card');
  await expect(card).toBeVisible();
  const chartBox = await chart.boundingBox();
  const cardBox = await card.boundingBox();
  expect(cardBox!.y, 'the reading sits under the chart, not on it').toBeGreaterThanOrEqual(
    chartBox!.y + chartBox!.height - 1,
  );
  expect(cardBox!.x + cardBox!.width).toBeLessThanOrEqual(chartBox!.x + chartBox!.width + 1);
  await trend.getByRole('button', { name: 'Back to now' }).tap();
  await expect(card).toBeHidden();

  const targets = [
    trend.getByRole('group', { name: 'Window' }).getByRole('button', { name: '1h' }),
    trend.getByRole('group', { name: 'Figures shown' }).getByRole('button').first(),
    trend.getByRole('group', { name: 'Figures read at this moment' }).getByRole('button').first(),
  ];
  for (const target of targets) {
    const box = await target.boundingBox();
    expect(box, 'the target is on the page').not.toBeNull();
    expect(box!.height, `${await target.innerText()} is too short to tap`).toBeGreaterThanOrEqual(
      44,
    );
  }
  await expectNoSidewaysScroll(page, 'the Runners page with fleet activity open');
});
