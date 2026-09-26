/**
 * The refresh control.
 *
 * Zoomies keeps itself current over one SSE stream, so this button is never
 * how a page keeps up. It is how an operator settles the question -- and the
 * only thing that makes it worth having is that it is the same control, in the
 * same place, with the same name, wherever they happen to be. So that is what
 * these protect: it is on every page that has something to fetch, it is not on
 * the pages that have not, it never sits ahead of the action that changes the
 * fleet, and the keyboard reaches it too.
 */
import { expect, test, type Page } from '@playwright/test';
import { browserOverride, goto, pageHeading, SECTIONS, sectionHeading } from './support/fixtures';

test.use(browserOverride);

/** The one control. Named, not guessed at: every page must call it this. */
function refreshButton(page: Page) {
  return page.getByRole('button', { name: 'Refresh', exact: true });
}

/**
 * Sections whose pages fetch something, and therefore have the button. Migrate
 * is the exception: it holds a flow rather than a view of the fleet. Settings
 * is a section of pages, and on a phone its own address is the list of them,
 * which fetches nothing -- so it is checked on a page that does.
 */
const REFRESHABLE = [
  ...SECTIONS.filter((section) => section.path !== '/migrate' && section.path !== '/settings').map(
    (section) => ({ path: section.path, label: section.label, heading: sectionHeading(section) }),
  ),
  { path: '/settings/users', label: 'Settings', heading: 'Users' },
];

for (const section of REFRESHABLE) {
  test(`${section.label} offers refresh, and pressing it leaves the page standing`, async ({
    page,
  }) => {
    await goto(page, section.path, section.heading);

    const refresh = refreshButton(page);
    await expect(refresh).toHaveCount(1);
    await expect(refresh).toBeVisible();
    await refresh.click();

    // The heading is the proof the page did not go anywhere: a refresh that
    // remounts the route would take the operator's scroll position, their
    // selection and their place in a form with it.
    await expect(pageHeading(page, section.heading)).toBeVisible();
    await expect(refresh).not.toHaveAttribute('aria-busy', 'true');
    await expect(refresh).toBeEnabled();
  });
}

test('a page with nothing to fetch offers nothing to press', async ({ page }) => {
  // The migration wizard holds a flow, not a view of the fleet. Refreshing it
  // would either do nothing or throw away what somebody has half-filled in,
  // and a control that succeeds at neither is worse than no control.
  await goto(page, '/migrate', 'Migrate repositories');
  await expect(refreshButton(page)).toHaveCount(0);
});

test('the refresh sits before the action that changes the fleet, not after it', async ({
  page,
}) => {
  // Reading order is the whole affordance. If refresh drifts past "Add a host"
  // on one page and not on another, an operator stops reaching for it without
  // looking -- which is the only reason it is uniform. Document order rather
  // than coordinates, because the row wraps on a phone and the claim is the
  // same either way.
  await goto(page, '/hosts', 'Hosts');
  const refresh = refreshButton(page);
  const add = page.getByRole('link', { name: 'Add a host' });
  await expect(refresh).toBeVisible();
  await expect(add).toBeVisible();

  const refreshFirst = await refresh.evaluate(
    (el, other) => Boolean(el.compareDocumentPosition(other) & Node.DOCUMENT_POSITION_FOLLOWING),
    await add.elementHandle(),
  );
  expect(refreshFirst, "refresh comes before the page's own action").toBe(true);
});

test('R refreshes the page, without taking the letter from anything else', async ({ page }) => {
  await goto(page, '/hosts', 'Hosts');
  const refresh = refreshButton(page);

  // The button reports the work whichever way it was started, which is what
  // makes the key and the button one gesture rather than two. The busy window
  // is the 420ms minimum spin, which a loaded runner can let pass between two
  // of Playwright's polls -- so the page records the attribute itself, from
  // before the key goes down, and the test asks whether it was ever set.
  await refresh.evaluate((el) => {
    const seen = { busy: false };
    (window as unknown as { __refreshSeen: typeof seen }).__refreshSeen = seen;
    new MutationObserver(() => {
      if (el.getAttribute('aria-busy') === 'true') seen.busy = true;
    }).observe(el, { attributes: true, attributeFilter: ['aria-busy'] });
  });
  await page.keyboard.press('r');
  await expect(refresh).toHaveAttribute('title', /^Refreshed /);
  await expect(refresh).not.toHaveAttribute('aria-busy', 'true');
  const seen = await page.evaluate(
    () => (window as unknown as { __refreshSeen: { busy: boolean } }).__refreshSeen.busy,
  );
  expect(seen, 'the button was busy while R refreshed').toBe(true);

  // `g r` is still Runners: the chord gets the key first, and a shortcut that
  // quietly ate half the navigation would be a bad trade for a refresh.
  await page.keyboard.press('g');
  await page.keyboard.press('r');
  await expect(pageHeading(page, 'Runners')).toBeVisible();

  // And in a search field an `r` is a letter, as it is everywhere else.
  await goto(page, '/pools', 'Pools');
  const search = page.getByRole('searchbox', { name: 'Search pools' });
  await search.click();
  await page.keyboard.press('r');
  await expect(search).toHaveValue('r');
});

test('the refresh says when it last landed', async ({ page }) => {
  // Somebody reaching for this button is usually asking "is this current?",
  // and the honest answer is a time, not a spinner.
  await goto(page, '/hosts', 'Hosts');
  const refresh = refreshButton(page);
  await expect(refresh).toHaveAttribute('title', /Fetch this page again/);

  await refresh.click();
  await expect(refresh).not.toHaveAttribute('aria-busy', 'true');
  await expect(refresh).toHaveAttribute('title', /^Refreshed .+\. Fetch it again\./);
});
