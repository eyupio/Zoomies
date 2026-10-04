import { expect, test, type Page, type TestInfo } from '@playwright/test';
import { browserOverride, goto, grid, waitForRows } from './support/fixtures';

test.use(browserOverride);

/*
 * The words each style draws the same two moments with. Off is what a browser
 * that has never chosen shows; Cute is here because its 44px avatar is the
 * harder thing to fit in a narrow column, and the no-clipping check below is
 * worth most against it.
 */
const STYLES = [
  {
    style: 'off',
    boosted: 'Zoomies',
    lifecycle: 'Busy',
    boost: 'Maximum boost',
    throttled: 'Throttled',
  },
  {
    style: 'cute',
    boosted: 'Squirrel spotted',
    lifecycle: 'Walking!',
    boost: 'Maximum zoomies',
    throttled: 'Leash tightened',
  },
] as const;

for (const words of STYLES)
  test(`one live status keeps details accessible without clipping in rows or cards (${words.style})`, async ({
    page,
    isMobile,
  }, testInfo) => {
    await page.addInitScript((style) => {
      if (!localStorage.getItem('zoomies.prefs'))
        localStorage.setItem('zoomies.prefs', JSON.stringify({ statusStyle: style }));
    }, words.style);
    await runStatusChecks(page, isMobile, testInfo, words);
  });

async function runStatusChecks(
  page: Page,
  isMobile: boolean,
  testInfo: TestInfo,
  words: (typeof STYLES)[number],
): Promise<void> {
  let cpuState = 'maximum_zoomies';
  let runner: Record<string, unknown> | undefined;
  const withCPU = (row: Record<string, unknown>) => ({
    ...row,
    cpu_resource: {
      state: cpuState,
      guaranteed_cpus: 2,
      current_cpus: cpuState === 'throttled' ? 1 : 3.8,
      ceiling_cpus: 4,
    },
  });
  await page.route('**/api/v1/runners?*', async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    data.items = data.items.map((row: Record<string, unknown>) => {
      if (row.state !== 'busy') return row;
      const updated = withCPU(row);
      runner ??= updated;
      return updated;
    });
    await route.fulfill({ response, json: data });
  });
  await page.route('**/api/v1/events*', (route) =>
    route.fulfill({
      contentType: 'text/event-stream',
      body: runner
        ? `event: runner.updated\ndata: ${JSON.stringify(withCPU(runner))}\n\n`
        : ': connected\n\n',
    }),
  );
  await goto(page, '/runners', 'Runners');
  const table = grid(page, 'Runners');
  await waitForRows(table);
  if (isMobile) await page.getByRole('button', { name: /^Rows\b/ }).tap();
  await expect(table.getByRole('columnheader', { name: /Status/ })).toBeVisible();
  await expect(table.getByRole('columnheader', { name: /Zoomies/ })).toHaveCount(0);
  const status = table
    .getByRole('button', { name: `${words.boosted}: show status details` })
    .first();
  await status.scrollIntoViewIfNeeded();
  if (isMobile) await status.tap();
  else await status.click();
  const tip = page.locator('.runner-status-tip .bubble:popover-open');
  await expect(tip).toBeVisible();
  await expect(tip).toContainText(words.lifecycle);
  await expect(tip).toContainText(words.boost);
  await expect(tip).toContainText('3.8');
  const bounds = (await tip.boundingBox())!;
  expect(bounds.x).toBeGreaterThanOrEqual(0);
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(page.viewportSize()!.width);
  await page.screenshot({
    path: testInfo.outputPath(`unified-status-${words.style}.png`),
    fullPage: true,
  });
  await page.keyboard.press('Escape');
  await expect(tip).toHaveCount(0);

  // A CPU-only SSE update must replace the label without a page navigation.
  cpuState = 'throttled';
  await page.evaluate(() => {
    window.dispatchEvent(new Event('offline'));
    window.dispatchEvent(new Event('online'));
  });
  const leash = table
    .getByRole('button', { name: `${words.throttled}: show status details` })
    .first();
  await expect(leash).toBeVisible();
  // Existing saved/narrow widths must wrap, never ellipsize the status.
  await table.locator('th[data-table-column="state"]').evaluate((node) => {
    (node as HTMLElement).style.width = '112px';
  });
  const clipped = await leash.evaluate((button) => {
    const label = button.querySelector('.label')!;
    const avatar = button.querySelector('svg')!.getBoundingClientRect();
    const cell = button.closest('td')!.getBoundingClientRect();
    const rect = label.getBoundingClientRect();
    const overlaps =
      avatar.left < rect.right &&
      avatar.right > rect.left &&
      avatar.top < rect.bottom &&
      avatar.bottom > rect.top;
    return (
      label.scrollWidth > label.clientWidth + 1 ||
      rect.right > cell.right + 1 ||
      avatar.left < cell.left - 1 ||
      avatar.right > cell.right + 1 ||
      overlaps
    );
  });
  expect(clipped).toBe(false);
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await expect
    .poll(() => leash.locator('svg').evaluate((svg) => svg.getAnimations({ subtree: true }).length))
    .toBe(0);
  if (isMobile) {
    await page.getByRole('button', { name: /^Cards\b/ }).tap();
    await expect(leash).toBeVisible();
    await leash.tap();
    await expect(tip).toContainText('Host pressure protection');
    await page.screenshot({
      path: testInfo.outputPath(`status-card-${words.style}.png`),
      fullPage: true,
    });
  }
}
