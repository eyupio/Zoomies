/**
 * The last step of the setup checklist is a job to run, not a migration.
 *
 * Every step before it happens inside Zoomies, and the last one happens in the
 * operator's own repository -- where the only help used to be a `runs-on` line
 * and a button into the migration wizard, which ends in pull requests opened on
 * their repositories. For somebody evaluating Zoomies with no workflow worth
 * editing, that was the wrong tool and a far bigger ask than hello-world.
 *
 * The demo fleet has a pool and a long history of jobs, so the checklist is
 * never shown on it. This puts the page in the state the last step exists for:
 * a fleet with a pool that has never run anything. The counts are zero, and the
 * stream is shut so it cannot put them back.
 */
import { expect, test, type Page } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);

// What a fleet that has never run a job reports: no jobs in any state, and no
// waits or start-up times to take a percentile of.
const NOTHING = [
  'queued_jobs',
  'running_jobs',
  'completed',
  'failed',
  'fleet_failed',
  'median_wait_ms',
  'p95_wait_ms',
  'p50_startup_ms',
  'p95_startup_ms',
  'p50_registration_ms',
  'p95_registration_ms',
];

async function aFleetThatHasNeverRunAJob(page: Page): Promise<void> {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: async (value: string) => sessionStorage.setItem('copied', value) },
    });
  });
  await page.route('**/api/v1/events*', (route) => route.abort());
  await page.route('**/api/v1/stats*', async (route) => {
    const response = await route.fetch();
    const stats = (await response.json()) as Record<string, unknown> & {
      fleet?: Record<string, unknown>;
    };
    for (const key of NOTHING) {
      stats[key] = 0;
      if (stats.fleet) stats.fleet[key] = 0;
    }
    await route.fulfill({ response, json: stats });
  });
  await goto(page, '/', 'Overview');
}

test('the last step is a job to run, with a workflow that needs nothing else', async ({ page }) => {
  await aFleetThatHasNeverRunAJob(page);

  const checklist = page.getByRole('region', { name: 'Finish setting up' });
  await expect(checklist).toBeVisible();
  await expect(checklist.getByText('Run a job on it')).toBeVisible();

  // A complete workflow, not a fragment: it is saved as it stands in a
  // repository that has nothing else in it. It goes to the pool the checklist
  // just named, so the job cannot land somewhere else.
  const sample = checklist.getByRole('group', { name: 'A test workflow' });
  await expect(sample).toContainText('on: workflow_dispatch');
  await expect(sample).toContainText(/runs-on: zoomies-demo/);
  await expect(sample).not.toContainText(/checkout|make /);
  await expect(checklist.getByText(/\.github\/workflows\/zoomies-test\.yml/)).toBeVisible();

  // The migration wizard is still there for somebody who has workflows to move,
  // but it is no longer the thing the step offers.
  await expect(checklist.getByRole('link', { name: 'Use the migration wizard.' })).toHaveAttribute(
    'href',
    '/migrate',
  );
  await expect(checklist.getByRole('link', { name: 'Rewrite workflows' })).toHaveCount(0);

  // It copies whole, and it is the text on screen that is copied.
  const shown = (await sample.innerText()).trim();
  await checklist.getByRole('button', { name: 'Copy the test workflow' }).click();
  const copied = await page.evaluate(() => sessionStorage.getItem('copied'));
  expect((copied ?? '').trim()).toBe(shown);
});

// A phone's card is narrower than the longest line of the workflow, so the code
// scrolls inside its box. A region a finger can scroll and a keyboard cannot is
// not a region everybody can read.
test('the workflow can be scrolled and read by keyboard, and the page does not move', async ({
  page,
}) => {
  await aFleetThatHasNeverRunAJob(page);
  const sample = page
    .getByRole('region', { name: 'Finish setting up' })
    .getByRole('group', { name: 'A test workflow' });
  await expect(sample).toBeVisible();

  await sample.focus();
  await expect(sample).toBeFocused();

  const { scrollWidth, clientWidth } = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(scrollWidth, 'the page scrolls sideways').toBeLessThanOrEqual(clientWidth);
});

// The pool exists, so the dashboard under the checklist has something real to
// show. The first-run composition (the checklist and nothing else) is only for
// a fleet with nothing in it, and an established fleet seen from a browser that
// never dismissed the checklist must not lose its panels for a quiet hour.
test('a fleet with a pool keeps its dashboard under the checklist', async ({ page }) => {
  await aFleetThatHasNeverRunAJob(page);
  await expect(page.getByRole('region', { name: 'Finish setting up' })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Pools', exact: true })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Recent events', exact: true })).toBeVisible();

  // And what has never been measured is not reported as zero.
  const wait = page.getByRole('link', { name: /^Median queue wait/ });
  await expect(wait).toContainText('--');
  await expect(wait).not.toContainText('0ms');
});
