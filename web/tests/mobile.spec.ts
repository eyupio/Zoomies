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
import { expect, test, type Page } from '@playwright/test';
import {
  browserOverride,
  dataRows,
  documentWidth,
  goto,
  grid,
  nav,
  navEntry,
  pageHeading,
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
  await expect(page.getByRole('region', { name: 'Recent scaling' })).toBeVisible();

  // The tiles stack rather than shrinking into unreadable columns.
  const tiles = page.getByRole('link', { name: /^(Queued jobs|Running jobs|Live runners)/ });
  await expect(tiles).toHaveCount(3);
  for (let i = 0; i < 3; i++) {
    await expect(tiles.nth(i)).toBeVisible();
  }
  await expect(page.getByRole('region', { name: 'Problems' })).toBeVisible();

  await expectNoSidewaysScroll(page, 'the Overview');
});

test('the navigation becomes a bar at the bottom and every page is reachable', async ({ page }) => {
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

  // Every section is one press away from the Overview. Located by href rather
  // than by name, because at this width the label is visually hidden and the
  // href is the thing that has to be right.
  //
  // Each hop starts from the Overview on purpose: a press from a page that has
  // been scrolled tells you less than a press from a known one.
  for (const section of SECTIONS) {
    await goto(page, '/', 'Overview');
    await navEntry(page, section.path).click();
    await expect(pageHeading(page, sectionHeading(section))).toBeVisible();
    await expect(navEntry(page, section.path)).toHaveAttribute('aria-current', 'page');
  }
});

test('the grids stay inside the screen instead of overflowing it', async ({ page }) => {
  // This one caught the subtler half of the same problem. Even once the column
  // stretched, a visually-hidden `.sr-only` span in a cell 500px along a
  // 1200px table was positioned against the page rather than against the
  // grid's own scroll frame, escaped its clipping, and made the document that
  // wide -- so the page scrolled sideways instead of the grid scrolling inside
  // its frame, and Chrome zoomed out to fit until the toolbar and the row
  // actions could not be pressed.

  for (const [path, heading, label] of [
    ['/runners', 'Runners', 'Runners'],
    ['/jobs', 'Jobs', 'Jobs'],
    ['/pools', 'Pools', 'Pools'],
  ] as const) {
    await goto(page, path, heading);
    await expect(dataRows(grid(page, label)).first()).toBeVisible();

    // A wide table is fine -- it may scroll inside its own frame -- but the
    // page around it must not.
    await expectNoSidewaysScroll(page, `the ${heading} grid`);
  }
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
