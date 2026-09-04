/**
 * The command palette.
 *
 * It is the fastest path to everything, and the one an operator reaches for
 * when something is on fire: Ctrl+K, type the name of the runner in the
 * alert, land on its page. This protects that it opens and closes without
 * losing the keyboard, that it searches pages and the live fleet rather than a
 * hard-coded list, and that the highlighted option is exposed to assistive
 * technology rather than being a colour.
 */
import { expect, test } from '@playwright/test';
import {
  browserOverride,
  FIXTURE,
  goto,
  isFocused,
  pageHeading,
  paletteOpener,
} from './support/fixtures';

test.use(browserOverride);

const palette = (page: import('@playwright/test').Page) =>
  page.getByRole('dialog', { name: 'Command palette' });
const search = (page: import('@playwright/test').Page) =>
  palette(page).getByRole('combobox', { name: /Search pages, pools, runners and hosts/ });

test.beforeEach(async ({ page }) => {
  await goto(page, '/', 'Overview');
});

test('Ctrl+K opens it, Escape closes it and gives focus back', async ({ page }) => {
  // Start from a known place so "focus went back where it was" means something.
  const opener = paletteOpener(page);
  await opener.focus();
  expect(await isFocused(opener)).toBe(true);

  await page.keyboard.press('Control+k');
  await expect(palette(page)).toBeVisible();
  await expect(search(page)).toBeFocused();

  await page.keyboard.press('Escape');
  await expect(palette(page)).toBeHidden();
  expect(await isFocused(opener), 'focus returns to whatever opened the palette').toBe(true);
});

test('typing a page name and pressing Enter goes there', async ({ page }) => {
  await page.keyboard.press('Control+k');
  await search(page).fill('jobs');

  const options = palette(page).getByRole('option');
  await expect(options.first()).toContainText('Jobs');

  await page.keyboard.press('Enter');
  await expect(palette(page)).toBeHidden();
  await expect(pageHeading(page, 'Jobs')).toBeVisible();
  await expect(page).toHaveURL(/\/jobs$/);
});

test('typing a runner name finds that runner and opens it', async ({ page }) => {
  await page.keyboard.press('Control+k');
  // The palette searches the live fleet cache, so this only passes because the
  // seeded runner is really there.
  await search(page).fill(FIXTURE.busyRunner);

  const first = palette(page).getByRole('option').first();
  await expect(first).toContainText(FIXTURE.busyRunner);
  await expect(first).toContainText('Runner');

  await page.keyboard.press('Enter');
  await expect(palette(page)).toBeHidden();
  await expect(pageHeading(page, FIXTURE.busyRunner)).toBeVisible();
  await expect(page).toHaveURL(/\/runners\/run_demo\d+$/);
});

test('the arrow keys move the selection and say so in the accessibility tree', async ({ page }) => {
  await page.keyboard.press('Control+k');
  await search(page).fill('demo');

  const options = palette(page).getByRole('option');
  await expect(options.first()).toBeVisible();
  const total = await options.count();
  expect(total, 'the fixture gives the palette something to move through').toBeGreaterThan(2);

  await expect(options.nth(0)).toHaveAttribute('aria-selected', 'true');
  await expect(search(page)).toHaveAttribute('aria-activedescendant', 'palette-option-0');

  await page.keyboard.press('ArrowDown');
  await expect(options.nth(0)).toHaveAttribute('aria-selected', 'false');
  await expect(options.nth(1)).toHaveAttribute('aria-selected', 'true');
  await expect(search(page)).toHaveAttribute('aria-activedescendant', 'palette-option-1');

  await page.keyboard.press('ArrowUp');
  await expect(options.nth(0)).toHaveAttribute('aria-selected', 'true');

  // Exactly one option is ever selected, whatever the arrows have been doing.
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('ArrowDown');
  await expect(palette(page).locator('[aria-selected="true"]')).toHaveCount(1);
});
