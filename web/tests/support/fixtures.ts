/**
 * Shared helpers for the Zoomies end-to-end suite.
 *
 * Two rules shape everything in here:
 *
 *  * Never wait for `networkidle`. The app holds a Server-Sent Events
 *    connection open for its whole life, so the network is never idle and any
 *    such wait burns the timeout instead of proving anything. `goto()` below
 *    settles on `domcontentloaded` and then waits for a real thing on the page.
 *  * Prefer roles and accessible names over CSS. Where a CSS selector is used
 *    it is because nothing in the accessibility tree distinguishes what is
 *    being reached for -- the three lines of a problem entry, say, or a
 *    grid's body rows -- and each of those is commented at the point of use.
 *    The tests cannot add `data-testid`: the Svelte source is not theirs.
 */
import { expect, type Locator, type Page } from '@playwright/test';

/**
 * A browser override for sandboxes where Playwright's own Chromium download is
 * absent and a compatible build sits somewhere else.
 *
 * CI installs the browser normally (`npx playwright install --with-deps
 * chromium`), leaves `PLAYWRIGHT_CHROMIUM` unset, and therefore gets `{}` --
 * no override at all. Every spec applies it with `test.use(browserOverride)`.
 */
export const browserOverride = process.env.PLAYWRIGHT_CHROMIUM
  ? { launchOptions: { executablePath: process.env.PLAYWRIGHT_CHROMIUM } }
  : {};

/**
 * The persistent navigation, in the fixed order docs/ui-guidelines.md gives.
 *
 * `heading` is the page's own <h1> where it says more than the nav entry does:
 * a nav label has to stay short enough to sit beside an icon, and a heading
 * does not.
 */
export const SECTIONS = [
  { path: '/', label: 'Overview' },
  { path: '/pools', label: 'Pools' },
  { path: '/runners', label: 'Runners' },
  { path: '/jobs', label: 'Jobs' },
  { path: '/hosts', label: 'Hosts' },
  { path: '/installations', label: 'Installations' },
  { path: '/migrate', label: 'Migrate', heading: 'Migrate repositories' },
  { path: '/audit', label: 'Audit' },
  { path: '/settings', label: 'Settings' },
] as const;

/** The <h1> a section's page shows. */
export function sectionHeading(section: (typeof SECTIONS)[number]): string {
  return 'heading' in section ? section.heading : section.label;
}

/** Names from internal/controller/seed.go, so the fixture and the tests agree. */
export const FIXTURE = {
  linuxPool: 'zoomies-demo-linux-x64',
  armPool: 'zoomies-demo-linux-arm64',
  /** A runner that is busy in the fixture, so it is never reaped mid-run. */
  busyRunner: 'zoomies-demo0000',
  /** Its id, which the seed fixes so a test can go straight to its page. */
  busyRunnerId: 'run_demo00',
  repos: ['acme/api', 'acme/site', 'acme/widgets'],
  /** Every job the seed writes; nothing adds more, since no webhook arrives. */
  totalJobs: 50,
  /**
   * What the Jobs page shows by default: everything except the one job the seed
   * runs on a hosted-runner vendor, which this fleet had no hand in.
   */
  managedJobs: 49,
  /**
   * Jobs in acme/api: the seed cycles three repositories over fifty jobs. The
   * hosted-runner job the default view hides belongs to acme/widgets, so this
   * count is the same in either view.
   */
  apiJobs: 17,
} as const;

/** The page's `<h1>`, optionally by name. */
export function pageHeading(page: Page, name?: string): Locator {
  return name === undefined
    ? page.getByRole('heading', { level: 1 })
    : page.getByRole('heading', { level: 1, name, exact: true });
}

/**
 * Go to a path and wait for the page itself, never for the network.
 *
 * @param heading the `<h1>` that proves the route rendered.
 */
export async function goto(page: Page, path: string, heading?: string): Promise<void> {
  await page.goto(path, { waitUntil: 'domcontentloaded' });
  await expect(pageHeading(page, heading)).toBeVisible();
}

/** Reload and wait for the same proof `goto` waits for. */
export async function reload(page: Page, heading?: string): Promise<void> {
  await page.reload({ waitUntil: 'domcontentloaded' });
  await expect(pageHeading(page, heading)).toBeVisible();
}

/** The left navigation. */
export function nav(page: Page): Locator {
  return page.getByRole('navigation', { name: 'Sections' });
}

/**
 * One navigation entry, by the path it points at.
 *
 * Deliberately not by accessible name: at the phone breakpoint the label span
 * is `display: none`, which empties the name (see the note in a11y.spec.ts),
 * and this helper has to work in both projects. Scoped to the list so the
 * brand mark, which also points at "/", is not one of the entries.
 */
export function navEntry(page: Page, path: string): Locator {
  return nav(page).getByRole('listitem').locator(`a[href="${path}"]`);
}

/** A DataGrid by its accessible name: "Runners", "Pools", "Jobs". */
export function grid(page: Page, label: string): Locator {
  return page.getByRole('grid', { name: label });
}

/**
 * The grid's real data rows.
 *
 * Structural rather than by role for two reasons: nothing in ARIA separates a
 * body row from the header row, and the loading skeleton draws rows that are
 * just as visible as real ones. Only a real row carries `data-row`.
 */
export function dataRows(gridLocator: Locator): Locator {
  return gridLocator.locator('tbody tr[data-row]');
}

/** Wait for a grid to have rows -- never for the network, which never idles. */
export async function waitForRows(gridLocator: Locator): Promise<Locator> {
  const rows = dataRows(gridLocator);
  await expect(rows.first()).toBeVisible();
  return rows;
}

/**
 * The pagination summary -- "1–9 of 9 runners" -- which is how a filter proves
 * it narrowed the set rather than merely repainting it. The line carries no
 * role of its own, so it is reached inside the landmark that does.
 */
export function rowCount(page: Page): Locator {
  return page.getByRole('navigation', { name: 'Pagination' }).locator('p');
}

/** The index of a column, so a cell can be read by what its header says. */
export async function columnIndex(gridLocator: Locator, header: string): Promise<number> {
  const headers = await gridLocator.getByRole('columnheader').allInnerTexts();
  const index = headers.findIndex((text) =>
    text.trim().toLowerCase().startsWith(header.toLowerCase()),
  );
  expect(index, `the grid has a "${header}" column`).toBeGreaterThanOrEqual(0);
  return index;
}

/**
 * One cell of one row, named by its column header rather than its position.
 *
 * The table carries `role="grid"`, so its cells are `gridcell`s.
 */
export async function cellUnder(
  gridLocator: Locator,
  row: Locator,
  header: string,
): Promise<Locator> {
  return row.getByRole('gridcell').nth(await columnIndex(gridLocator, header));
}

/** Every value in one column of the grid, in the order the rows are shown. */
export async function columnTexts(gridLocator: Locator, header: string): Promise<string[]> {
  const index = await columnIndex(gridLocator, header);
  return dataRows(gridLocator).evaluateAll(
    (rows, at) =>
      rows.map(
        (row) => (row.querySelectorAll('td')[at] as HTMLElement | undefined)?.innerText ?? '',
      ),
    index,
  );
}

/**
 * A filter menu trigger on the Jobs page.
 *
 * The column header that sorts by the same field is also a button with the
 * same words, so the trigger is picked out by the `aria-expanded` that only a
 * popup trigger carries.
 */
export function facetTrigger(page: Page, label: string, open = false): Locator {
  return page.getByRole('button', { name: label, expanded: open });
}

/** What the theme currently is: the attribute on `<html>` and what was stored. */
export async function readTheme(
  page: Page,
): Promise<{ attribute: string | null; stored: string | null }> {
  return page.evaluate(() => ({
    attribute: document.documentElement.getAttribute('data-theme'),
    stored: (() => {
      try {
        return localStorage.getItem('zoomies.theme');
      } catch {
        return null;
      }
    })(),
  }));
}

/**
 * The top bar's command-palette hint.
 *
 * Below 1180px the words are hidden and only the shortcut is left, so the
 * accessible name shrinks from "Search or jump to Ctrl K" to "Ctrl K".
 */
export function paletteOpener(page: Page): Locator {
  return page.getByRole('button', { name: /(Ctrl|⌘)\s*K/ });
}

/** The top bar's theme control. Its label says where a press will take you. */
export function themeToggle(page: Page): Locator {
  return page.getByRole('button', { name: /^Theme:/ });
}

/**
 * Press the theme toggle until the chosen theme is on screen.
 *
 * The control cycles light → dark → system, so "switch to dark" is one press
 * or two depending on where it started.
 */
export async function chooseTheme(page: Page, choice: 'light' | 'dark'): Promise<void> {
  for (let press = 0; press < 3; press++) {
    if ((await readTheme(page)).attribute === choice) return;
    await themeToggle(page).click();
  }
  await expect(page.locator('html')).toHaveAttribute('data-theme', choice);
}

/**
 * Forget every stored preference for this origin.
 *
 * Playwright gives each test its own context, so storage starts empty anyway;
 * this is for the tests that write a preference and then want to prove what
 * happens without one.
 */
export async function clearStoredPreferences(page: Page): Promise<void> {
  await page.evaluate(() => {
    try {
      localStorage.clear();
    } catch {
      /* private mode: there was nothing stored to begin with */
    }
  });
}

/** How wide the document is against how wide the window is. */
export async function documentWidth(
  page: Page,
): Promise<{ scrollWidth: number; clientWidth: number }> {
  return page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
}

/** True when the element currently holds focus. */
export function isFocused(locator: Locator): Promise<boolean> {
  return locator.evaluate((element) => element === document.activeElement);
}

/** Whether focus is somewhere inside the main landmark. */
export function focusIsInMain(page: Page): Promise<boolean> {
  return page.evaluate(
    () => document.querySelector('main')?.contains(document.activeElement) ?? false,
  );
}

/**
 * Walk backwards to the top of the tab order.
 *
 * The shell moves focus to the page heading after every navigation, so a plain
 * `Tab` starts from the middle of the document. Shift+Tab until the given
 * element has focus; the number of presses is returned so a caller can assert
 * that nothing else came first.
 */
export async function shiftTabTo(page: Page, target: Locator, limit = 40): Promise<number> {
  for (let presses = 1; presses <= limit; presses++) {
    await page.keyboard.press('Shift+Tab');
    if (await isFocused(target)) return presses;
  }
  return -1;
}
