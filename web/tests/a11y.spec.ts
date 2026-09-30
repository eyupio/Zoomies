/**
 * The accessibility checklist from docs/ui-guidelines.md section 6, as tests.
 *
 * These are the parts of that list a machine can check honestly: the landmark
 * structure a screen-reader user navigates by, an accessible name on every
 * control, alternative text or an explicit exemption on every image, a skip
 * link that really is the first thing in the tab order, a dialog that keeps
 * focus and gives it back, and no positive tabindex anywhere -- the one
 * mistake that quietly reorders the whole page for keyboard users.
 *
 * They run in both projects. A phone is not a different accessibility
 * standard.
 */
import { expect, test, type Page } from '@playwright/test';
import {
  browserOverride,
  FIXTURE,
  focusIsInMain,
  goto,
  isFocused,
  shiftTabTo,
} from './support/fixtures';

test.use(browserOverride);

// Every page in the navigation. It used to be four of the ten, which meant the
// audit of "every button has an accessible name" was an audit of the four
// pages least likely to be missing one.
const PAGES = [
  { path: '/', heading: 'Overview' },
  { path: '/pools', heading: 'Pools' },
  { path: '/runners', heading: 'Runners' },
  { path: '/workflows', heading: 'Workflows' },
  // Not in the navigation any more, and audited for that reason: it is the
  // page every link that names a job lands on.
  { path: '/jobs', heading: 'Jobs' },
  // The Queue puts four buttons on every row rather than a menu, which is four
  // times as many controls to get the name of right.
  { path: '/queue', heading: 'Queue' },
  { path: '/usage', heading: 'Usage' },
  { path: '/hosts', heading: 'Hosts' },
  { path: '/providers', heading: 'Providers' },
  // The three journey pages. They are not in the navigation, which is exactly
  // why they were missed: an operator setting a fleet up for the first time
  // spends more time on these than on anything in the sidebar, and every
  // control on them is one they have never seen before.
  { path: '/hosts/new', heading: 'Add a host' },
  { path: '/pools/new', heading: 'Create a pool' },
  { path: '/providers/new', heading: 'Add a provider' },
  // The two pages an operator reaches from a card rather than from the
  // navigation, and the pair a machine that is taking too long is diagnosed
  // from. Both are named after a fixture row, because a detail page with
  // nothing behind it is an error state rather than the page.
  { path: `/providers/${FIXTURE.providerId}`, heading: FIXTURE.provider },
  { path: `/machines/${FIXTURE.readyMachineId}`, heading: FIXTURE.readyMachine },
  { path: '/installations', heading: 'Installations' },
  { path: '/migrate', heading: 'Migrate repositories' },
  { path: '/audit', heading: 'Audit' },
  // Three of the settings pages: the one with a table and a row menu, the one
  // with the most controls on it, and the one that is nothing but switches --
  // each of which has to name itself without the label beside it.
  { path: '/settings/users', heading: 'Users' },
  { path: '/settings/configuration', heading: 'Configuration' },
  { path: '/settings/events', heading: 'Events' },
] as const;

/** The pages whose main content is a grid of rows to wait for. */
const GRID_PAGES = new Set(['/pools', '/runners', '/workflows', '/jobs', '/queue', '/audit']);

/**
 * Let the page finish rendering before auditing it.
 *
 * A grid page paints a skeleton first, so counting controls too early counts
 * the wrong page. Waiting for a real row is the honest signal.
 */
async function settle(page: Page, path: string): Promise<void> {
  await expect(page.getByRole('main')).toBeVisible();
  if (path === '/') {
    await expect(page.getByRole('region', { name: 'Recent events' })).toBeVisible();
    return;
  }
  if (GRID_PAGES.has(path)) {
    await expect(page.locator('tbody tr[data-row]').first()).toBeVisible();
    return;
  }
  // The rest settle on their own heading: a page with nothing seeded behind it
  // shows an empty state, which is a finished page and worth auditing too.
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
}

for (const { path, heading } of PAGES) {
  test(`${heading} has one h1 and the landmarks to navigate by`, async ({ page }) => {
    await goto(page, path, heading);
    await settle(page, path);

    await expect(page.getByRole('heading', { level: 1 })).toHaveCount(1);
    await expect(page.getByRole('main')).toHaveCount(1);
    // At least one navigation landmark, and the sections one is named so it
    // can be told apart from the grid's pagination.
    expect(await page.getByRole('navigation').count()).toBeGreaterThanOrEqual(1);
    await expect(page.getByRole('navigation', { name: 'Sections' })).toHaveCount(1);
  });
}

test('every button and link has an accessible name', async ({ page }) => {
  // This audit deliberately visits every route. On a loaded CI runner that is
  // valid work for more than the ordinary single-page 30 second budget.
  test.slow();

  // This one caught a real bug on the phone: the bottom bar hid each entry's
  // label with `display: none`, which took its accessible name with it, and a
  // screen reader announced eight links called "link". The bar shows its four
  // labels outright now, and the collapsed desktop sidebar hides its own off
  // screen rather than removing them -- so both projects assert the same
  // thing.

  // `getByRole` uses the browser's own name computation and skips anything
  // hidden from the accessibility tree, so this is the real question rather
  // than a guess at aria-label attributes.
  for (const { path, heading } of PAGES) {
    await goto(page, path, heading);
    await settle(page, path);

    // An element with no accessible name has the empty one, so this asks for
    // exactly the offenders rather than comparing two counts taken at two
    // different moments.
    await expect(
      page.getByRole('button', { name: /^$/ }),
      `${path}: every button has an accessible name`,
    ).toHaveCount(0);
    await expect(
      page.getByRole('link', { name: /^$/ }),
      `${path}: every link has an accessible name`,
    ).toHaveCount(0);
  }
});

/*
 * A segmented option is named for the label it shows, and described after it.
 * Both halves have been got wrong in turn: a name that was the description
 * alone left the button reading "1m" with nothing by that name to ask for, and
 * a description that opened with the label again had a screen reader saying
 * "Rows -- Rows: the table". Voice control asks for what it can see, so the
 * label leads and is said once.
 */
test('a segmented option is asked for by the label it shows', async ({ page }) => {
  test.slow();

  let audited = 0;
  for (const { path, heading } of PAGES) {
    await goto(page, path, heading);
    await settle(page, path);

    // Nothing in the accessibility tree marks a choice as segmented, so the
    // group is reached structurally; the names it is judged on are the
    // rendered ones.
    const seen = await page.locator('.segmented button').evaluateAll((buttons) =>
      buttons.map((button) => ({
        label: (button.textContent ?? '').trim(),
        name: (button.getAttribute('aria-label') ?? button.textContent ?? '').trim(),
      })),
    );
    audited += seen.length;

    const offenders = seen.filter(({ label, name }) => {
      if (label === '') return false;
      if (!name.startsWith(label)) return true;
      // What follows the label is the description, and it is a description of
      // the option rather than the option's name a second time. The separator
      // between the two is the component's to choose, so it is stepped over
      // rather than spelled out here.
      return name
        .slice(label.length)
        .replace(/^[\s\p{Pd}:,.-]+/u, '')
        .startsWith(label);
    });

    expect(
      offenders,
      `${path}: a segmented option's accessible name says its label once, first`,
    ).toEqual([]);
  }

  // An audit that found no buttons to audit passes for the wrong reason.
  expect(audited, 'no segmented choice was found on any page').toBeGreaterThan(0);
});

test('every image has alternative text or is hidden from assistive technology', async ({
  page,
}) => {
  test.slow();

  for (const { path, heading } of PAGES) {
    await goto(page, path, heading);
    await settle(page, path);

    const offenders = await page.evaluate(() =>
      Array.from(document.querySelectorAll('img'))
        .filter(
          (image) =>
            image.getAttribute('alt') === null &&
            image.getAttribute('aria-hidden') !== 'true' &&
            image.closest('[aria-hidden="true"]') === null,
        )
        .map((image) => image.outerHTML.slice(0, 120)),
    );
    expect(offenders, `${path}: images carry alt text or are marked decorative`).toEqual([]);

    // An <svg role="img"> is an image too, and every one that is exposed must
    // say what it shows. A status shape beside its own printed label is
    // deliberately hidden instead, so it is not read out twice.
    const unlabelled = await page.evaluate(() =>
      Array.from(document.querySelectorAll('svg[role="img"]'))
        .filter(
          (svg) =>
            (svg.getAttribute('aria-label') ?? '').trim() === '' &&
            svg.getAttribute('aria-hidden') !== 'true' &&
            svg.closest('[aria-hidden="true"]') === null,
        )
        .map((svg) => svg.outerHTML.slice(0, 120)),
    );
    expect(unlabelled, `${path}: every svg image is described`).toEqual([]);
  }
});

test('no element carries a positive tabindex', async ({ page }) => {
  test.slow();

  for (const { path, heading } of PAGES) {
    await goto(page, path, heading);
    await settle(page, path);

    const positive = await page.evaluate(() =>
      Array.from(document.querySelectorAll('[tabindex]'))
        .filter((element) => Number(element.getAttribute('tabindex')) > 0)
        .map((element) => element.outerHTML.slice(0, 120)),
    );
    expect(positive, `${path}: the tab order is the document order`).toEqual([]);
  }
});

test('the skip link is first in the tab order and lands in main', async ({ page }) => {
  await goto(page, '/', 'Overview');
  const skip = page.getByRole('link', { name: 'Skip to the main content' });

  // The shell moves focus to the page heading after a navigation, so "from the
  // top" means walking backwards until there is nothing before us.
  const presses = await shiftTabTo(page, skip);
  expect(presses, 'the skip link is reachable from the keyboard').toBeGreaterThan(0);

  // It is off-screen until it has focus, and slides on-screen once it does;
  // polling rather than measuring once, because that slide is a transition.
  await expect
    .poll(async () => (await skip.boundingBox())?.y ?? -1, {
      message: 'a focused skip link comes on screen',
    })
    .toBeGreaterThanOrEqual(0);

  // Nothing at all comes before it.
  await page.keyboard.press('Shift+Tab');
  expect(await isFocused(skip)).toBe(false);
  expect(
    await page.evaluate(() => document.activeElement === document.body),
    'the skip link is the first stop in the page',
  ).toBe(true);

  await page.keyboard.press('Tab');
  expect(await isFocused(skip)).toBe(true);
  await page.keyboard.press('Enter');
  expect(await focusIsInMain(page), 'activating it moves focus into the main landmark').toBe(true);
});

test('a dialog keeps focus, closes on Escape and hands focus back', async ({ page }) => {
  // Opened from the runner's own page rather than from the grid's row menu, so
  // the same assertions hold at both widths.
  await goto(page, `/runners/${FIXTURE.busyRunnerId}`, FIXTURE.busyRunner);

  const trigger = page.getByRole('button', { name: 'Drain', exact: true });
  await trigger.click();

  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog).toHaveAttribute('aria-modal', 'true');
  // Focus moved in by itself.
  expect(
    await page.evaluate(() =>
      document.querySelector('[role="dialog"]')?.contains(document.activeElement),
    ),
  ).toBe(true);

  // And it stays in: tabbing round the dialog never escapes it.
  for (let press = 0; press < 6; press++) {
    await page.keyboard.press('Tab');
    expect(
      await page.evaluate(() =>
        document.querySelector('[role="dialog"]')?.contains(document.activeElement),
      ),
      'focus stays inside the dialog',
    ).toBe(true);
  }

  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  // Focus comes back a frame after the dialog has gone, so wait for it rather
  // than sampling once.
  await expect(trigger, 'focus returns to what opened the dialog').toBeFocused();
});

// A file input is visually hidden behind a button that opens its picker, so if
// it takes focus itself nothing is drawn: the ring is clipped away with the
// input, and a keyboard user loses the focus for a Tab. The button is the
// control; the input must not be a stop of its own.
const FILE_PICKERS = [
  { path: '/pools', heading: 'Pools', open: 'Import', dialog: 'Import pools' },
  {
    path: '/settings/configuration',
    heading: 'Configuration',
    open: 'Import',
    dialog: 'Import settings',
  },
  {
    path: '/settings/backups',
    heading: 'Backups',
    open: 'Upload a backup',
    dialog: 'Upload a backup',
  },
];

for (const { path, heading, open, dialog } of FILE_PICKERS) {
  test(`the ${dialog} dialog's file picker is reached through its button, not a hidden input`, async ({
    page,
  }) => {
    await goto(page, path, heading);
    await page.getByRole('button', { name: open, exact: true }).click();
    await expect(page.getByRole('dialog', { name: dialog })).toBeVisible();

    const chooser = page.getByRole('dialog', { name: dialog }).getByRole('button', {
      name: 'Choose a file',
    });
    let reached = false;
    for (let press = 0; press < 10; press++) {
      await page.keyboard.press('Tab');
      expect(
        await page.evaluate(() => (document.activeElement as HTMLInputElement | null)?.type),
        'focus never rests on an input nobody can see',
      ).not.toBe('file');
      reached ||= await chooser.evaluate((el) => el === document.activeElement);
    }
    expect(reached, 'the visible button is a tab stop').toBe(true);
  });
}

test('a drawer keeps focus the way a dialog does, and gives it back', async ({ page }) => {
  // A drawer is a modal too. It was untested, and it is the one an operator
  // opens most: every row of the Jobs grid opens one.
  await goto(page, '/jobs', 'Jobs');
  const row = page.locator('tbody tr[data-row]').first();
  await expect(row).toBeVisible();
  await row.click();

  const drawer = page.getByRole('dialog');
  await expect(drawer).toBeVisible();
  await expect(drawer).toHaveAttribute('aria-modal', 'true');
  expect(
    await page.evaluate(() =>
      document.querySelector('[role="dialog"]')?.contains(document.activeElement),
    ),
    'focus moves into the drawer by itself',
  ).toBe(true);

  for (let press = 0; press < 8; press++) {
    await page.keyboard.press('Tab');
    expect(
      await page.evaluate(() =>
        document.querySelector('[role="dialog"]')?.contains(document.activeElement),
      ),
      'focus stays inside the drawer',
    ).toBe(true);
  }

  // And the page behind it is not merely covered: it is inert, so a screen
  // reader's virtual cursor cannot read on into it either.
  // `inert` is set on every sibling of the overlay on the way up to <body>,
  // and it is not inherited as a property, so the answer is a walk. The grid
  // is the thing that matters: it is what an operator could otherwise read on
  // into, or click a row of, from behind an open drawer.
  expect(
    await page.evaluate(() => {
      let node: HTMLElement | null = document.querySelector('table[role="grid"]');
      while (node) {
        if (node.inert) return true;
        node = node.parentElement;
      }
      return false;
    }),
    'the grid behind the drawer is inert',
  ).toBe(true);

  await page.keyboard.press('Escape');
  await expect(drawer).toBeHidden();
  // Waited for rather than sampled once, for the reason the dialog above gives:
  // the overlay hands focus back a frame after it has gone, because clearing
  // the `inert` it put on the page has to happen first. Which frame that lands
  // on is the browser's business -- on a grid page it is a frame later again
  // when the columns have just been remeasured -- and it is not what this test
  // is about, which is that focus comes back at all rather than being left on
  // <body> at the top of the document.
  await expect
    .poll(() => focusIsInMain(page), { message: 'focus comes back to the page' })
    .toBe(true);
});

test('asking for less motion gets none', async ({ page }) => {
  // The tokens collapse to 1ms rather than the components each remembering to
  // check, so this is the honest place to assert it: if the tokens are right,
  // every transition and animation in the product is.
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await goto(page, '/', 'Overview');

  const motion = await page.evaluate(() => {
    const style = getComputedStyle(document.documentElement);
    return ['--z-motion-fast', '--z-motion-base', '--z-motion-slow'].map((token) =>
      style.getPropertyValue(token).trim(),
    );
  });
  expect(motion, 'every motion token collapses').toEqual(['1ms', '1ms', '1ms']);

  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await page.reload();
  const restored = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--z-motion-base').trim(),
  );
  expect(restored, 'and comes back for anyone who did not ask').not.toBe('1ms');
});

test('the page has the two live regions everything announces through', async ({ page }) => {
  await goto(page, '/', 'Overview');

  // Polite for what can wait, assertive for what interrupted a task. Both
  // exist before anything is announced, because a region added at the moment
  // of the announcement is not read out.
  await expect(page.locator('[aria-live="polite"]').first()).toBeAttached();
  await expect(page.locator('[aria-live="assertive"]').first()).toBeAttached();
});

test('the page holds together at 200% zoom without scrolling sideways', async ({ page }) => {
  // Reflow, in the terms WCAG puts it: at 320px of effective width the content
  // is still one column. Emulated by halving the viewport rather than by a
  // zoom setting, which is the same arithmetic and is scriptable.
  await page.setViewportSize({ width: 640, height: 480 });
  await goto(page, '/', 'Overview');
  await expect(page.getByRole('region', { name: 'Recent events' })).toBeVisible();

  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow, 'the document does not scroll sideways').toBeLessThanOrEqual(1);
});
