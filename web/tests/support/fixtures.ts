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
import { readFileSync } from 'node:fs';
import { expect, type Locator, type Page } from '@playwright/test';

/**
 * A browser override for sandboxes where Playwright's own Chromium download is
 * absent and a compatible build sits somewhere else.
 *
 * CI installs the browser normally (`npx playwright install --with-deps
 * chromium`), leaves `PLAYWRIGHT_CHROMIUM` unset, and therefore gets `{}` --
 * no override at all. Every spec applies it with `test.use(browserOverride)`.
 */
/**
 * Where the first-run fixture leaves the setup token it saw the controller
 * print.
 *
 * The bootstrap route asks for that token: an empty database is not proof that
 * whoever is filling the first-run form deployed this controller, and being
 * able to read its log is. A test is in the same position as the operator, so
 * it fetches the token the same way -- out of the output. The path is defined
 * here and handed to the fixture on its command line, so the two cannot drift.
 */
export const SETUP_TOKEN_FILE = 'test-results/firstrun-setup-token';

/** The token the running first-run controller printed. */
export function setupToken(): string {
  return readFileSync(SETUP_TOKEN_FILE, 'utf8').trim();
}

export const browserOverride = process.env.PLAYWRIGHT_CHROMIUM
  ? { launchOptions: { executablePath: process.env.PLAYWRIGHT_CHROMIUM } }
  : {};

/**
 * The persistent navigation, in the fixed order docs/ui-guidelines.md gives.
 *
 * `heading` is the page's own <h1> where it says more than the nav entry does:
 * a nav label has to stay short enough to sit beside an icon, and a heading
 * does not. `lands` is where the address ends up when it is not the entry's
 * own path: Settings is a section of pages, so on a desktop `/settings` goes
 * straight to the first of them, while a phone shows the list of pages under
 * the section's own name.
 */
export const SECTIONS = [
  { path: '/', label: 'Overview' },
  { path: '/pools', label: 'Pools' },
  { path: '/runners', label: 'Runners' },
  { path: '/workflows', label: 'Workflows' },
  { path: '/usage', label: 'Usage' },
  { path: '/hosts', label: 'Hosts' },
  { path: '/providers', label: 'Providers' },
  { path: '/installations', label: 'Installations' },
  { path: '/migrate', label: 'Migrate', heading: 'Migrate repositories' },
  { path: '/kennel', label: 'Kennel Club' },
  { path: '/audit', label: 'Audit' },
  {
    path: '/settings',
    label: 'Settings',
    heading: /^(Account|Settings)$/,
    lands: /\/settings(\/account)?$/,
  },
] as const;

/** The <h1> a section's page shows. */
export function sectionHeading(section: (typeof SECTIONS)[number]): string | RegExp {
  return 'heading' in section ? section.heading : section.label;
}

/** A path as a literal inside a pattern: every metacharacter escaped, not only the slashes. */
function escapeForPattern(path: string): string {
  return path.replace(/[.*+?^${}()|[\]\\/]/g, '\\$&');
}

/** The address a section's entry ends up at, as a pattern the URL must match. */
export function sectionLanding(section: (typeof SECTIONS)[number]): RegExp {
  return 'lands' in section ? section.lands : new RegExp(`${escapeForPattern(section.path)}$`);
}

/** Names from internal/controller/seed.go, so the fixture and the tests agree. */
export const FIXTURE = {
  linuxPool: 'zoomies-demo-linux-x64',
  armPool: 'zoomies-demo-linux-arm64',
  /** The installation both pools register with, for a spec that makes a third. */
  installationId: 'ins_demoacme',
  /**
   * A runner that is busy in the fixture, so it is never reaped mid-run.
   *
   * The shape is its pool's, the word is from the kennel and `demo00` is the
   * index rather than a random token, because a fixture has to be the same
   * every time. See demoRunnerName in internal/controller/seed.go.
   */
  busyRunner: 'zoomies-2vcpu-4gb-ubuntu-2404-banjo-demo00',
  /** Its id, which the seed fixes so a test can go straight to its page. */
  busyRunnerId: 'run_demo00',
  repos: ['acme/api', 'acme/site', 'acme/widgets'],
  /** Has no workflows, so the migration wizard hides it by default. */
  quietRepo: 'acme/docs',
  /** Archived on GitHub, so no pull request could ever be opened against it. */
  archivedRepo: 'acme/legacy-api',
  /** Already runs on this fleet, so there is nothing left to move. */
  migratedRepo: 'acme/infra',
  /** Has two workflow files, so the wizard can be made to choose between them. */
  multiWorkflowRepo: 'acme/widgets',
  /** The one place the demo fleet rents machines from, and its id. */
  provider: 'demo-pve',
  providerId: 'prv_demoproxmox',
  /**
   * The two machines it has rented, deliberately unalike: one that finished and
   * became a host, one still on its way. The first is what the provider badge
   * and the machine-to-host link are drawn from, the second what the lifecycle
   * band and the timeline's still-running last row are.
   */
  readyMachine: 'zoomies-mach-demo01',
  readyMachineId: 'mach_demo01',
  buildingMachine: 'zoomies-mach-demo02',
  buildingMachineId: 'mach_demo02',
  /** The host the ready machine became. */
  machineHost: 'demo-builder-2',
  /** Every job the seed writes; nothing adds more, since no webhook arrives. */
  totalJobs: 55,
  /**
   * Every job this fleet has a hand in: everything except the three jobs the
   * seed runs on a hosted-runner vendor -- one finished, one running and one
   * still queued -- which this fleet had no hand in.
   */
  managedJobs: 52,
  /**
   * What the Jobs page shows before it is asked anything: the jobs running on
   * this fleet's own runners, one on each busy runner the seed leaves behind.
   * The vendor's running job is not one of them.
   */
  runningJobs: 3,
  /**
   * Jobs in acme/api: the seed cycles three repositories over the twenty-five
   * runs behind its fifty jobs -- two jobs to a run, and a run keeps to one
   * repository -- and again over the backlog that explains its starting
   * runners. The hosted-runner job the default view hides belongs to
   * acme/widgets, so this count is the same in either view.
   */
  apiJobs: 17,
  /**
   * Every workflow run the seed's jobs belong to: the twenty-five behind the
   * fifty, the vendor's two, and one per backlog job.
   */
  totalRuns: 30,
  /** The runs this fleet has a hand in: everything but the vendor's two. */
  managedRuns: 28,
  /**
   * What the Workflows page shows before it is asked anything: the runs with
   * a job on one of this fleet's busy runners. Three such jobs, two runs.
   */
  runningRuns: 2,
  /** The run whose runner died under a job -- the one fleet failure the seed writes. */
  faultedRun: 321,
  /** A run whose job was re-run: its row says which attempt it is on. */
  rerunRun: 303,
} as const;

/**
 * Plant something a reload would lose, then check it is still there.
 *
 * A page that got its new numbers by reloading itself would pass a weaker
 * test and still be the bug: every page is expected to update in place, with
 * nobody pressing anything.
 */
export async function plantMarker(page: Page): Promise<void> {
  await page.evaluate(() => {
    (window as unknown as { __zoomiesStayedPut: boolean }).__zoomiesStayedPut = true;
  });
}

export async function expectNoReload(page: Page): Promise<void> {
  const stayed = await page.evaluate(
    () => (window as unknown as { __zoomiesStayedPut?: boolean }).__zoomiesStayedPut === true,
  );
  expect(stayed, 'the page updated in place rather than reloading').toBe(true);
}

/** The page's `<h1>`, optionally by name -- exactly, or by a pattern. */
export function pageHeading(page: Page, name?: string | RegExp): Locator {
  if (name === undefined) return page.getByRole('heading', { level: 1 });
  if (name instanceof RegExp) return page.getByRole('heading', { level: 1, name });
  return page.getByRole('heading', { level: 1, name, exact: true });
}

/**
 * Go to a path and wait for the page itself, never for the network.
 *
 * @param heading the `<h1>` that proves the route rendered.
 */
export async function goto(page: Page, path: string, heading?: string | RegExp): Promise<void> {
  await page.goto(path, { waitUntil: 'domcontentloaded' });
  await expect(pageHeading(page, heading)).toBeVisible();
}

/** Reload and wait for the same proof `goto` waits for. */
export async function reload(page: Page, heading?: string | RegExp): Promise<void> {
  await page.reload({ waitUntil: 'domcontentloaded' });
  await expect(pageHeading(page, heading)).toBeVisible();
}

/** The persistent navigation: the left sidebar, or the phone's bottom bar. */
export function nav(page: Page): Locator {
  return page.getByRole('navigation', { name: 'Sections' });
}

/**
 * One navigation entry, by the path it points at.
 *
 * By href rather than by accessible name, because the sidebar empties the name
 * of its own labels when it is collapsed (see the note in a11y.spec.ts) and
 * this helper has to work in both projects. Scoped to the list so the brand
 * mark, which also points at "/", is not one of the entries.
 *
 * On a phone the bar carries only the four primary sections; the rest are in
 * the sheet More opens, so reach those with `menuEntry`.
 */
export function navEntry(page: Page, path: string): Locator {
  return nav(page).getByRole('listitem').locator(`a[href="${path}"]`);
}

/** The paths the phone's bottom bar carries itself, from `lib/shell/sections.ts`. */
export const PRIMARY_SECTIONS: readonly string[] = ['/', '/pools', '/runners', '/workflows'];

/** The phone's menu -- the sheet More opens -- once it is open. */
export function navMenu(page: Page): Locator {
  return page.getByRole('dialog', { name: 'All sections' });
}

/** Press "More" in the bottom bar and wait for the sheet it opens. */
export async function openNavMenu(page: Page): Promise<Locator> {
  await nav(page).getByRole('button', { name: 'More' }).click();
  const menu = navMenu(page);
  await expect(menu).toBeVisible();
  return menu;
}

/** One entry of the phone's menu, by the path it points at. */
export function menuEntry(page: Page, path: string): Locator {
  return navMenu(page).getByRole('listitem').locator(`a[href="${path}"]`);
}

/**
 * Go to a section the way an operator would at this width.
 *
 * The sidebar lists all twelve; the phone's bar lists four and keeps the rest
 * in the sheet. Which of those a test is looking at is not the thing under
 * test in most specs, so they ask for the section and get there.
 */
export async function openSection(page: Page, path: string): Promise<void> {
  if ((await navEntry(page, path).count()) > 0) {
    await navEntry(page, path).click();
    return;
  }
  await openNavMenu(page);
  await menuEntry(page, path).click();
  await expect(navMenu(page)).toHaveCount(0);
}

/**
 * That the navigation marks this section as the one being looked at -- and
 * marks exactly one, since a mark two entries carry means nothing.
 */
export async function expectCurrentSection(page: Page, path: string): Promise<void> {
  if ((await navEntry(page, path).count()) > 0) {
    await expect(navEntry(page, path)).toHaveAttribute('aria-current', 'page');
    await expect(nav(page).locator('[aria-current="page"]')).toHaveCount(1);
    return;
  }
  const menu = await openNavMenu(page);
  await expect(menuEntry(page, path)).toHaveAttribute('aria-current', 'page');
  await expect(menu.locator('[aria-current="page"]')).toHaveCount(1);
  await page.keyboard.press('Escape');
  await expect(navMenu(page)).toHaveCount(0);
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

/**
 * The index of a column, so a cell can be read by what its header says.
 *
 * Read from the elements rather than from the accessibility tree: on a phone
 * every row is a card and the heading row is hidden, so `columnheader` does not
 * resolve there -- but the column is still the same column, in the same place,
 * and its cell still carries the heading as its own label.
 */
export async function columnIndex(gridLocator: Locator, header: string): Promise<number> {
  const headers = await gridLocator
    .locator('thead th')
    .evaluateAll((cells) => cells.map((cell) => (cell.textContent ?? '').trim()));
  const index = headers.findIndex((text) => text.toLowerCase().startsWith(header.toLowerCase()));
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

/** The top bar's command-palette hint. */
export function paletteOpener(page: Page): Locator {
  // By its own label, not by the key cap inside it. The cap is hidden on a
  // phone -- it names a key that is not there -- and the words beside it are
  // hidden below the sidebar breakpoint, so the button carries the name itself
  // and it is the same at every width.
  return page.getByRole('button', { name: 'Search or jump to' });
}

/** The top bar's account menu, opened. */
export async function openAccountMenu(page: Page): Promise<Locator> {
  await page.getByRole('button', { name: 'Account menu' }).click();
  const menu = page.getByRole('menu', { name: 'Account menu' });
  await expect(menu).toBeVisible();
  return menu;
}

/**
 * Choose a theme the way an operator does: from the account menu, where the
 * three options are visible and the one in force is marked.
 */
export async function chooseTheme(page: Page, choice: 'light' | 'dark'): Promise<void> {
  const menu = await openAccountMenu(page);
  await menu.getByRole('menuitemradio', { name: choice === 'dark' ? 'Dark' : 'Light' }).click();
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

/**
 * Open the Hosts page's capacity map, which is folded away until somebody asks,
 * and return its region.
 *
 * Idempotent, because the choice is remembered: a test that opened it and then
 * reloads, or that visits the page twice, finds it open and must not close it.
 */
export async function openHostCapacityMap(page: Page): Promise<Locator> {
  const toggle = page.getByRole('button', { name: 'Host capacity map', exact: true });
  if ((await toggle.getAttribute('aria-expanded')) !== 'true') await toggle.click();
  const map = page.getByRole('region', { name: 'Host capacity map', exact: true });
  await expect(map).toBeVisible();
  return map;
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
