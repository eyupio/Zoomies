/**
 * The pages an operator opens on their worst day.
 *
 * Every other spec runs against a fleet with nothing wrong with it, and that is
 * the right fixture for the grids and the wrong one for everything that
 * explains a fault. The demo deliberately keeps its two starting runners young
 * so an instance left open never reports a problem it does not have -- which
 * left the problems drawer, both stuck-runner shapes, a pool nothing can place
 * and a job GitHub is holding with nothing to render, and so untested. This
 * project runs against the same fleet with ZOOMIES_SEED_STUCK on top.
 *
 * What it protects is the diagnosis rather than the styling. A runner stuck in
 * `registering` has two unrelated causes and the page has to say which one this
 * is; a pool nothing can place is a misconfiguration rather than a busy fleet,
 * and the advice differs; a held job is not this fleet's fault at all.
 */
import { expect, test } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);

/**
 * The fixture's two stuck runners, by the ids the seed fixes so a test can go
 * straight to the page. demo06's container came up and never reached GitHub;
 * demo07 has no container at all.
 */
const STUCK = {
  neverRegistered: 'run_demo06',
  noContainer: 'run_demo07',
  blockedPool: 'pool_demostuckblocked',
  /**
   * The fixture's throttled host -- eight CPUs, four slots stepped to two --
   * and the busy demo runner the seed moves onto it with a defaulted
   * allocation. See seedThrottledHost in internal/controller/seed_stuck.go.
   */
  throttledHost: 'demo-throttled-1',
  throttledRunner: 'run_demo00',
};

/**
 * These two live here rather than in hosts.spec.ts and runners.spec.ts
 * because the fixture they read is seeded for this project alone: the demo
 * fleet the other projects run against never throttles, and a runner's
 * allocation is written by the controller at create time, so neither shape
 * can be made through the API from a browser.
 */
test('the throttled host says its step, its reason and what it was stepped down from', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  const card = page.getByRole('article', { name: STUCK.throttledHost, exact: true });
  await expect(card.getByText('Throttled', { exact: true })).toHaveAttribute(
    'title',
    /^Throttled, step 2 of 3/,
  );
  await expect(card).toContainText(
    "throttled to 2 of 4 slots (step 2 of 3) after sustained pressure: the 1-minute load average is 30.0, at least twice the host's 8 CPUs",
  );
  // One busy runner with a CPU limit is on it, running at half its quota.
  await expect(card).toContainText('the 1 runner with a CPU limit at 50% of it');
  await expect(card).toContainText(/1 of 2 slots in use\s*· throttled from 4/);
  await expect(card).toContainText('load 30');
});

test('a runner shows the allocation it was given and that its host is throttling it', async ({
  page,
}) => {
  await goto(page, `/runners/${STUCK.throttledRunner}`);
  const usage = page.getByRole('region', { name: 'Resource usage' });
  // (8 - 0.5) / 4 CPUs floored to the hundredth and (16384 - 512) / 4 MB: the
  // share a four-slot host of this size hands out, and it says so rather than
  // pointing at a pool field nobody set.
  await expect(usage).toContainText("1.87 CPU · 3.9 GB, the host's default share");
  await expect(usage).toContainText('of 1.87 allowed');
  // What it may use is what it holds now: the 3.9 GB it was created with and the
  // 1.5 GB the memory valve has lent it since, which the line says in so many words.
  await expect(usage).toContainText('of 5.4 GB allowed, 1.5 GB of it lent');
  // The sentence is decided from the runner's own recorded allocation and its
  // pool's backend, never from the pool's current limits: this one was created
  // with a share on a Docker pool, so the agent is lowering its quota and the
  // page may say so.
  const throttle = page.getByTestId('runner-throttle');
  await expect(throttle).toContainText('step 2 of 3');
  await expect(throttle).toContainText('CPU allocation is throttled to 50%');
});

test('a runner whose container came up but never registered says exactly that', async ({
  page,
}) => {
  await goto(page, `/runners/${STUCK.neverRegistered}`);

  // Both halves of coming up are separate rows, and it is the second one being
  // absent that names the cause. A single "Registered" row -- which is what
  // this page used to show, from started_at -- said the opposite.
  await expect(page.getByText('Container started', { exact: true }).first()).toBeVisible();
  await expect(page.getByText('Registered with GitHub', { exact: true })).toHaveCount(0);

  // "Is the agent even alive?" is the next question, and answering it used to
  // mean leaving for the Hosts page.
  await expect(page.getByText('Host last seen', { exact: true })).toBeVisible();
});

test('a runner with no container yet is the other shape, and looks different', async ({ page }) => {
  await goto(page, `/runners/${STUCK.noContainer}`);

  // Nothing started, so neither stamp is there. The two pages have to be
  // distinguishable or the operator cannot tell which fault they have.
  await expect(page.getByText('Container started', { exact: true })).toHaveCount(0);
  await expect(page.getByText('Registered with GitHub', { exact: true })).toHaveCount(0);
});

test('the problems drawer names the stuck runners and what to do about them', async ({ page }) => {
  await goto(page, '/');

  // The Overview never lists a problem itself -- it says how much needs a
  // person and offers the way in -- so this is the operator's own route.
  await page.getByRole('button', { name: /^Problems\./ }).click();
  const drawer = page.getByRole('dialog', { name: 'Problems' });
  await expect(drawer).toBeVisible();

  await expect(drawer).toContainText(/stuck starting up for over/i);
  // The detail counts both shapes separately, and the fix answers the one the
  // oldest runner has. One answer for both shapes would send half the readers
  // to the wrong place.
  await expect(drawer).toContainText(/waiting for a container to start/i);
  await expect(drawer).toContainText(/has not registered/i);
  await expect(drawer).toContainText(/check the agent log on the host/i);

  // And the pool nothing can place is in the same list, with its own answer:
  // the fixture queues one job in it, so the problem is an error with a job
  // waiting rather than a warning about a pool that merely wants runners.
  await expect(drawer).toContainText(/has 1 job waiting and nowhere to run them/i);
});

test('a pool nothing can place says so rather than reading as a busy fleet', async ({ page }) => {
  await goto(page, `/pools/${STUCK.blockedPool}`);

  // "at capacity" clears itself and "not matching the pool's host selector"
  // never will. The reason strings keep that distinction and the page shows
  // the scheduler's own words.
  await expect(page.getByText(/not matching the pool's host selector/i).first()).toBeVisible();
  await expect(page.getByText(/relax the pool's host selector/i).first()).toBeVisible();
});

test('a job GitHub is holding says it is held, and is not charged a queue wait', async ({
  page,
}) => {
  // A held job is waiting, not running, so the view is asked for: the page
  // opens on what is running.
  await goto(page, '/jobs?state=waiting');

  await page.getByText('deploy-production').first().click();

  // The sentence now comes from the controller rather than from the browser,
  // and it is the same one `zoomies jobs get` prints -- which is the point of
  // moving it: two renderings of one answer instead of two answers.
  await expect(page.getByText(/holding this job for a deployment review/i)).toBeVisible();
  // The fix is also the first next step, offered as the link to GitHub.
  await expect(page.getByRole('link', { name: /approve the deployment on GitHub/i })).toBeVisible();
  // The time a held job spends is GitHub's, not the queue's: a number here
  // would charge this fleet for a review it cannot influence.
  await expect(page.getByText('Not queued yet')).toBeVisible();
});

test('a blocked pool says blocked in the drawer, not merely waiting', async ({ page }) => {
  await goto(page, '/jobs?state=waiting');

  // The distinction the explanation exists for: a fleet that is merely busy
  // clears itself, and a pool whose selector matches nothing never will. The
  // panel used to say "waiting" to both, because counting a pool's runners
  // cannot tell them apart.
  await page.getByText('deploy-production').first().click();
  await expect(page.getByRole('status', { name: /What is happening to this job/i })).toBeVisible();
});
