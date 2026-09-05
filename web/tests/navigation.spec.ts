/**
 * Getting around.
 *
 * The navigation is the one part of the product an operator uses on every
 * visit, so this protects the things that make it feel like software rather
 * than a set of pages: every section reachable and marked as current, the
 * browser's own back button working, the two preferences that survive a
 * reload (a collapsed nav and a chosen theme), and a wrong address landing on
 * something that explains itself instead of a blank screen.
 */
import { expect, test } from '@playwright/test';
import {
  browserOverride,
  chooseTheme,
  clearStoredPreferences,
  goto,
  nav,
  navEntry,
  pageHeading,
  readTheme,
  reload,
  SECTIONS,
  sectionHeading,
} from './support/fixtures';

test.use(browserOverride);

// These run in both projects: the phone's navigation is a bar at the bottom
// rather than a column at the side, but every one of these is a question about
// routing, history or the theme, and the answer has to be the same at both
// widths. The one exception is marked where it is.

test('every navigation entry routes to its page and is marked as current', async ({ page }) => {
  await goto(page, '/', 'Overview');

  for (const section of SECTIONS) {
    const entry = navEntry(page, section.path);
    await entry.click();

    await expect(page).toHaveURL(new RegExp(`${section.path.replace(/\//g, '\\/')}$`));
    await expect(pageHeading(page, sectionHeading(section))).toBeVisible();
    await expect(entry, `${section.label} is marked as the current page`).toHaveAttribute(
      'aria-current',
      'page',
    );
    // Exactly one entry claims to be current, or the mark means nothing.
    await expect(nav(page).locator('[aria-current="page"]')).toHaveCount(1);
  }
});

test('every page ends in a footer that says which product this is and who makes it', async ({
  page,
}) => {
  // A signed-in screenshot is usually the first anyone outside the team sees
  // of Zoomies, and the footer is the one line on it that says what it is,
  // where the docs are and who builds it. docs/ui-guidelines.md promises all
  // three, and promises that the credit is the one link in the shell that
  // leaves the product -- so it opens a new tab and hands over no referrer.
  await goto(page, '/runners', 'Runners');

  const footer = page.getByRole('contentinfo');
  await expect(footer.getByText('Zoomies', { exact: true })).toBeVisible();
  await expect(footer.getByRole('link', { name: 'Docs' })).toHaveAttribute(
    'href',
    'https://zoomies.sh/quickstart/',
  );

  const credit = footer.getByRole('link', { name: 'EyUp.io' });
  await expect(credit).toBeVisible();
  await expect(footer.getByText(/Developed by/)).toBeVisible();
  await expect(credit).toHaveAttribute('href', 'https://eyup.io');
  await expect(credit).toHaveAttribute('target', '_blank');
  await expect(credit).toHaveAttribute('rel', /noopener/);
});

test('the browser back button returns to the previous page', async ({ page }) => {
  await goto(page, '/', 'Overview');
  await navEntry(page, '/runners').click();
  await expect(pageHeading(page, 'Runners')).toBeVisible();

  await navEntry(page, '/jobs').click();
  await expect(pageHeading(page, 'Jobs')).toBeVisible();

  await page.goBack();
  await expect(pageHeading(page, 'Runners')).toBeVisible();
  await expect(page).toHaveURL(/\/runners$/);

  await page.goBack();
  await expect(pageHeading(page, 'Overview')).toBeVisible();
  await expect(page).toHaveURL(/127\.0\.0\.1:\d+\/$/);
});

test('a collapsed navigation is still collapsed after a reload', async ({ page }) => {
  // The exception: a phone's navigation is a bar at the bottom with nothing to
  // collapse, and mobile.spec.ts asserts that the control is absent there.
  test.skip(!!test.info().project.use.isMobile, 'there is no collapse control on a phone');

  await goto(page, '/', 'Overview');
  await expect(page.locator('html')).not.toHaveAttribute('data-nav', 'collapsed');

  await page.getByRole('button', { name: 'Collapse the navigation' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-nav', 'collapsed');
  await expect(page.getByRole('button', { name: 'Expand the navigation' })).toBeVisible();

  await reload(page, 'Overview');
  // The attribute is applied by the inline script in index.html before first
  // paint, so a collapsed nav must never flash open on the way back.
  await expect(page.locator('html')).toHaveAttribute('data-nav', 'collapsed');
  await expect(page.getByRole('button', { name: 'Expand the navigation' })).toBeVisible();
  // Collapsed or not, every section is still reachable and still named.
  for (const section of SECTIONS) {
    await expect(navEntry(page, section.path)).toHaveCount(1);
  }
});

test('the theme toggle switches to dark and the choice survives a reload', async ({ page }) => {
  await goto(page, '/', 'Overview');
  expect((await readTheme(page)).attribute, 'system is the default and writes nothing').toBeNull();

  await chooseTheme(page, 'dark');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  expect((await readTheme(page)).stored).toBe('dark');

  await reload(page, 'Overview');
  const after = await readTheme(page);
  expect(after.attribute).toBe('dark');
  expect(after.stored).toBe('dark');

  // And with nothing stored it is the operating system's choice again, which
  // is what "system is the default" has to mean.
  await clearStoredPreferences(page);
  await reload(page, 'Overview');
  expect((await readTheme(page)).attribute).toBeNull();
});

test('an unknown address renders the not-found page, not a blank screen', async ({ page }) => {
  await goto(page, '/nowhere/at/all', 'Page not found');

  await expect(page.getByText('That address does not exist')).toBeVisible();
  // Exact: the brand mark is "Zoomies, go to the overview".
  await expect(page.getByRole('link', { name: 'Go to the overview', exact: true })).toBeVisible();
  await expect(page).toHaveTitle(/Page not found/);
  // The shell is still there, so the operator is not stranded.
  await expect(nav(page)).toBeVisible();

  await page.getByRole('link', { name: 'Go to the overview', exact: true }).click();
  await expect(pageHeading(page, 'Overview')).toBeVisible();
});
