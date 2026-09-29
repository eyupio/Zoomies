/**
 * Pools, and the wizard that makes one.
 *
 * A pool is the only object in Zoomies that can quietly hand a workflow job
 * root on somebody's build host, so this protects the two things that stop
 * that being an accident: the list says which pools carry that risk and names
 * it, and the wizard spells out the dangerous choice and refuses to take it
 * without a deliberate confirmation. It also protects the wizard as a wizard --
 * seven steps, a preview of the runs-on line the labels produce, the server's
 * own verdict before anything is created, and a Back button that does not
 * throw away what was typed.
 */
import { expect, test, type Page } from '@playwright/test';
import { browserOverride, dataRows, FIXTURE, goto, grid, pageHeading } from './support/fixtures';

test.use(browserOverride);

const next = (page: Page) => page.getByRole('button', { name: 'Next' });
const back = (page: Page) => page.getByRole('button', { name: 'Back' });
const nameField = (page: Page) => page.getByRole('textbox', { name: 'Pool name' });

/**
 * One radio in one of the wizard's radio groups.
 *
 * By input name and value rather than by accessible name: each option's name
 * is its label plus the whole consequence sentence, and two of them start with
 * the same word ("Docker" and "Docker in Docker"), so a name match is either
 * ambiguous or a copy of the product's prose.
 */
const radio = (page: Page, group: 'backend' | 'docker-mode' | 'placement', value: string) =>
  page.locator(`input[name="pool-${group}"][value="${value}"]`);
const labelField = (page: Page) => page.getByRole('textbox', { name: 'Labels' });

/**
 * Move a slider to the value it announces, the way a keyboard does.
 *
 * The control moves by notch rather than by number -- its `value` is an index
 * into the notches, so filling it with "4" would land on the fifth notch
 * rather than on four cores. Arrowing towards the words the slider speaks is
 * both what an operator does and the only spelling that stays true when a
 * notch is added.
 */
async function setSlider(page: Page, name: string, valuetext: string): Promise<void> {
  const slider = page.getByRole('slider', { name });
  await slider.focus();
  for (let step = 0; step < 40; step++) {
    const current = await slider.getAttribute('aria-valuetext');
    if (current === valuetext) return;
    const before = await slider.inputValue();
    await page.keyboard.press('ArrowRight');
    if ((await slider.inputValue()) === before) break;
  }
  // Past it, or the wrong way: come back down until it matches.
  for (let step = 0; step < 40; step++) {
    if ((await slider.getAttribute('aria-valuetext')) === valuetext) return;
    const before = await slider.inputValue();
    await page.keyboard.press('ArrowLeft');
    if ((await slider.inputValue()) === before) break;
  }
  await expect(slider).toHaveAttribute('aria-valuetext', valuetext);
}

/**
 * Start the wizard on the advanced path.
 *
 * The first step is the fork -- automatic or advanced -- and most of these
 * specs are about a control that only the advanced path shows. Choosing it
 * here keeps each of them about its own subject rather than about the fork.
 */
async function toAdvanced(page: Page): Promise<void> {
  await page.getByRole('radio', { name: 'Advanced' }).check();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Target' })).toBeVisible();
}

/**
 * Press Next until the review step, whichever step we are on.
 *
 * Counting clicks made every one of these specs depend on how many steps the
 * advanced path has, so inserting one broke a dozen tests that were not about
 * the step list at all. The one spec that *is* about the step list walks it
 * explicitly and counts for itself.
 */
async function toReview(page: Page): Promise<void> {
  const review = page.getByRole('heading', { level: 2, name: 'Review' });
  for (let step = 0; step < 10; step++) {
    if (await review.isVisible()) return;
    await next(page).click();
  }
  await expect(review).toBeVisible();
}

/** Press Next until the named step, for a spec that is not about the step list. */
async function toStep(page: Page, title: string): Promise<void> {
  const heading = page.getByRole('heading', { level: 2, name: title });
  for (let step = 0; step < 10; step++) {
    if (await heading.isVisible()) return;
    await next(page).click();
  }
  await expect(heading).toBeVisible();
}

/** Add a label the way an operator does: type it, press Enter, see the chip. */
async function addLabel(page: Page, label: string): Promise<void> {
  await labelField(page).fill(label);
  await page.keyboard.press('Enter');
  await expect(page.getByRole('button', { name: `Remove the label ${label}` })).toBeVisible();
}

test('the list shows both pools and names the risk the arm64 one carries', async ({ page }) => {
  await goto(page, '/pools', 'Pools');
  const rows = dataRows(grid(page, 'Pools'));
  await expect(rows).toHaveCount(2);

  const linux = rows.filter({ hasText: FIXTURE.linuxPool });
  const arm = rows.filter({ hasText: FIXTURE.armPool });
  await expect(linux).toHaveCount(1);
  await expect(arm).toHaveCount(1);

  // The badge counts the risks; colour is never the only carrier. Two, and it
  // stays two: the count is of dangerous *settings*, not of everything the
  // controller currently has to say about the pool. This pool also has a job
  // it cannot place, and that condition arrives some minutes into a run --
  // which is how a badge that counted it made this assertion depend on how
  // long the suite had been going.
  await expect(arm.getByText('2 risks')).toBeVisible();
  // And the specific risks are in the row's text at all times -- in the
  // tooltip for a mouse, and in the always-present description for everyone
  // else -- rather than only appearing on hover.
  await expect(arm).toContainText('docker-in-docker');
  await expect(arm).toContainText('persistent runners');
  await expect(arm).toContainText('Docker in Docker');
  await expect(arm).toContainText('Reused');

  // The safe pool says so by having nothing to say.
  await expect(linux).not.toContainText('risk');
  await expect(linux).toContainText('One job');
});

test('a filter that matches nothing settles on the empty state, not the skeleton', async ({
  page,
}) => {
  await goto(page, '/pools', 'Pools');
  const rows = dataRows(grid(page, 'Pools'));
  await expect(rows).toHaveCount(2);

  await page.getByRole('searchbox', { name: 'Search pools' }).fill('nothing-is-called-this');
  const empty = page.getByText('No pools match those filters');
  await expect(empty).toBeVisible();

  // The grid used to refetch itself on every result and flip back to its
  // loading skeleton each time, so the empty state never stayed. Give it
  // a moment and check it is still the empty state on screen.
  await page.waitForTimeout(800);
  await expect(empty).toBeVisible();
  await expect(page.locator('tr.skeleton-row')).toHaveCount(0);

  // The empty state offers the way out.
  await page.getByRole('button', { name: 'Clear filters' }).click();
  await expect(rows).toHaveCount(2);
});

test('runner limits are adjustable from a pool row without opening the wizard', async ({
  page,
}) => {
  await goto(page, '/pools', 'Pools');
  const row = dataRows(grid(page, 'Pools')).filter({ hasText: FIXTURE.linuxPool });
  let patched: Record<string, unknown> | null = null;
  await page.route('**/api/v1/pools/*', async (route) => {
    if (route.request().method() !== 'PATCH') {
      await route.continue();
      return;
    }
    patched = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
  });

  await row.getByRole('button', { name: `Actions for ${FIXTURE.linuxPool}` }).click();
  await page.getByRole('menuitem', { name: 'Adjust runner limits' }).click();
  const dialog = page.getByRole('dialog', { name: 'Runner limits' });
  await dialog.getByRole('spinbutton', { name: 'Minimum runners' }).fill('2');
  await dialog.getByRole('spinbutton', { name: 'Maximum runners' }).fill('9');
  await dialog.getByRole('button', { name: 'Save limits' }).click();

  await expect(dialog).toBeHidden();
  await expect.poll(() => patched).toEqual({ min_runners: 2, max_runners: 9 });
  await expect(pageHeading(page, 'Pools')).toBeVisible();
});

test('the wizard forks into an automatic path and an advanced one', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');

  // The first question is how much of the pool to decide, because the two
  // answers lead to genuinely different amounts of work.
  await expect(page.getByRole('heading', { level: 2, name: 'Setup' })).toBeVisible();
  await expect(page.getByText('Step 1 of 5')).toBeVisible();
  await expect(page.getByRole('radio', { name: 'Automatic' })).toBeChecked();

  // The step list by class: nothing in the accessibility tree tells it apart
  // from the breadcrumb list above it, which is also an ordered list in main.
  const steps = page.locator('ol.steps');
  for (const step of ['Setup', 'Target', 'Labels', 'Docker', 'Review']) {
    await expect(steps).toContainText(step);
  }
  // Nothing the automatic path does not ask.
  await expect(steps).not.toContainText('Size');
  await expect(steps).not.toContainText('Runners');

  // Three questions and a review is the whole of it.
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Target' })).toBeVisible();
  await nameField(page).fill('e2e-pool');
  await expect(page.getByLabel('GitHub installation')).toHaveValue(/ins_/);

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Labels' })).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Remove the label zoomies-e2e-pool' }),
  ).toBeVisible();

  // The one question a fleet cannot answer for this pool: whether its jobs
  // build container images. Off unless asked for, because the daemon it turns
  // on runs in a privileged container.
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Docker' })).toBeVisible();
  await expect(page.getByRole('radio', { name: 'No' })).toBeChecked();

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();
  await expect(page.getByText('Step 5 of 5')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Create pool' })).toBeVisible();
});

// Turning Docker on used to mean finding it on the advanced path, three steps
// past anything the operator came for. A pool that builds images is an ordinary
// pool, so the automatic path asks, and says what the answer costs.
test('the automatic path can give a pool its own Docker daemon', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');

  await next(page).click();
  await nameField(page).fill('e2e-builders');
  await next(page).click();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Docker' })).toBeVisible();

  await page.getByRole('radio', { name: 'Yes' }).check();
  // What it costs is on the screen that asks, not on a page found later.
  await expect(page.getByText('privileged container')).toBeVisible();
  await expect(page.getByText('share one slot')).toBeVisible();

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();
  // The review step reads the server's own answer, which is where the image
  // with a Docker client in it appears without anybody pinning one.
  await expect(page.getByText('zoomies-runner-docker')).toBeVisible();
});

test('the advanced path walks target, labels, hosts, backend, size, scaling, runners and review', async ({
  page,
}) => {
  await goto(page, '/pools/new', 'Create a pool');
  await page.getByRole('radio', { name: 'Advanced' }).check();

  const steps = page.locator('ol.steps');
  for (const step of [
    'Setup',
    'Target',
    'Labels',
    'Hosts',
    'Backend',
    'Size',
    'Scaling',
    'Runners',
    'Review',
  ]) {
    await expect(steps).toContainText(step);
  }

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Target' })).toBeVisible();
  await expect(page.getByText('Step 2 of 9')).toBeVisible();
  await nameField(page).fill('e2e-pool');
  await expect(page.getByLabel('GitHub installation')).toHaveValue(/ins_/);

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Labels' })).toBeVisible();
  // The name has already produced a label; a second one is added on top.
  await expect(
    page.getByRole('button', { name: 'Remove the label zoomies-e2e-pool' }),
  ).toBeVisible();
  await addLabel(page, 'gpu');

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Hosts' })).toBeVisible();
  // A new pool reaches the whole fleet until an operator says otherwise.
  await expect(radio(page, 'placement', 'any')).toBeChecked();

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Backend' })).toBeVisible();
  await expect(radio(page, 'backend', 'docker')).toBeChecked();

  // Size before count: how much machine one runner gets is asked before how
  // many there may be, because the second means nothing without the first.
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Size' })).toBeVisible();
  // The size opens on the host's own share rather than on a figure somebody
  // has to accept, and the sliders appear once a fixed size is chosen.
  await expect(page.getByRole('radio', { name: 'One share of each host' })).toBeChecked();
  await expect(page.getByRole('slider', { name: 'CPU per runner' })).toHaveCount(0);
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();
  const cpu = page.getByRole('slider', { name: 'CPU per runner' });
  await expect(cpu).toBeVisible();
  // Opened on the fleet's own figures rather than left empty.
  await expect(cpu).toHaveAttribute('aria-valuetext', '2 cores');
  await expect(page.getByRole('slider', { name: 'Memory per runner' })).toHaveAttribute(
    'aria-valuetext',
    '4 GB',
  );

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Scaling' })).toBeVisible();
  await expect(page.getByRole('spinbutton', { name: 'Maximum runners' })).toBeVisible();
  // GitHub adds the default labels to every ephemeral runner, so leaving them
  // out is offered only once the pool reuses its runners.
  const noDefaults = page.getByRole('checkbox', { name: 'Leave out the default labels' });
  await expect(noDefaults).toHaveCount(0);
  await page.getByRole('checkbox', { name: 'Destroy each runner after one job' }).uncheck();
  await expect(noDefaults).toBeVisible();
  await expect(noDefaults).not.toBeChecked();
  await page.getByRole('checkbox', { name: 'Destroy each runner after one job' }).check();

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Runners' })).toBeVisible();
  // Every override is empty, because empty is the answer that means "follow
  // the fleet" -- and the fleet's own figure is the placeholder beside it.
  const provision = page.getByRole('textbox', { name: 'Provision timeout' });
  await expect(provision).toHaveValue('');
  await expect(provision).toHaveAttribute('placeholder', /this fleet's/);
  await expect(page.getByText('This pool follows the fleet on every runner timing.')).toBeVisible();

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();
  await expect(page.getByText('Step 9 of 9')).toBeVisible();
  // The last step offers to create rather than to continue.
  await expect(page.getByRole('button', { name: 'Create pool' })).toBeVisible();
});

test('a pool timing override is kept, and clearing it hands the setting back to the fleet', async ({
  page,
}) => {
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-slow');
  await toStep(page, 'Runners');

  const provision = page.getByRole('textbox', { name: 'Provision timeout' });
  await provision.fill('45m');
  await expect(
    page.getByText('This pool overrides 1 of 5 runner timings; the rest follow the fleet.'),
  ).toBeVisible();

  // A timeout inside the time a runner of this pool takes to start is said
  // while the number is being chosen, not afterwards.
  await provision.fill('5m');
  await expect(page.getByText('Shorter than a runner of this pool takes to start')).toBeVisible();

  // Cleared, the pool follows the fleet again and the caution goes with it.
  await provision.fill('');
  await expect(page.getByText('This pool follows the fleet on every runner timing.')).toBeVisible();
  await expect(page.getByText('Shorter than a runner of this pool takes to start')).toHaveCount(0);
});

test('the backend step names the image the chosen operating system will boot', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-pool');
  await next(page).click();
  await addLabel(page, 'gpu');
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Hosts' })).toBeVisible();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Backend' })).toBeVisible();

  // Nothing chosen: the pool follows the controller's default image and any
  // host will do.
  const os = page.getByLabel('Operating system');
  await expect(os).toHaveValue('');
  await expect(page.getByText("Runners boot the controller's default image.")).toBeVisible();

  // The options come from the server's catalogue, so choosing one must name a
  // real published image rather than a string the UI made up.
  await os.selectOption('debian-12');
  await expect(page.getByText('ghcr.io/eyupio/zoomies-runner:debian-12')).toBeVisible();

  // The demo fleet has one Debian host, so the step can say so before the
  // operator reaches the review.
  await expect(page.getByText(/1 connected host match/)).toBeVisible();

  // And an operating system nothing in the fleet runs is said to match nothing.
  await os.selectOption('fedora-42');
  await expect(page.getByText('No connected host matches')).toBeVisible();
});

test('the hosts step keeps a pool to an architecture and says which machines that is', async ({
  page,
}) => {
  // The demo fleet is two amd64 builders and one arm64 box, and the arm64 one
  // is cordoned. That last part is the case worth protecting: a selector can
  // match a host that is not taking work, and the step has to say so rather
  // than promise a runner the review step then refuses.
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-arm');
  await next(page).click();
  await addLabel(page, 'gpu');
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Hosts' })).toBeVisible();

  // A new pool takes the whole fleet, and says so in those words.
  await expect(radio(page, 'placement', 'any')).toBeChecked();
  const readout = page.getByText(/Every connected host matches/);
  await expect(readout).toBeVisible();

  await radio(page, 'placement', 'matching').check();
  await page.getByLabel('Architecture', { exact: true }).selectOption('arm64');

  // One host, named, and honest about the fact that it would take nothing.
  await expect(page.getByText('1 of 3 connected hosts match')).toBeVisible();
  // The host also appears in the fleet preview elsewhere in the wizard. The
  // matching-host badges have no distinct role, so scope the name to their
  // readout rather than relying on page-wide text uniqueness.
  await expect(page.locator('.match-hosts').getByText('demo-arm-1', { exact: true })).toBeVisible();
  await expect(page.getByText(/cordoned or not heartbeating/)).toBeVisible();

  // The other architecture is the two builders, and they are taking work.
  await page.getByLabel('Architecture', { exact: true }).selectOption('amd64');
  await expect(page.getByText('2 of 3 connected hosts match')).toBeVisible();
  await expect(page.getByText(/cordoned or not heartbeating/)).toHaveCount(0);

  // And the choice reaches the pool that gets created.
  await toReview(page);
  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();
  await expect(page.getByRole('region', { name: /What will be created/ })).toContainText(
    'arch=amd64',
  );
});

test('the labels step previews the runs-on line those labels produce', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-pool');
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Labels' })).toBeVisible();

  const preview = page.locator('figure');
  // The wizard filled the label in from the name, so the preview is already a
  // line that reaches this pool rather than one that reaches the whole fleet.
  await expect(preview).toContainText('runs-on: zoomies-e2e-pool');

  // Take that away and the pool answers only to the brand, which the preview
  // says reaches everything rather than letting it look finished.
  await page.getByRole('button', { name: 'Remove the label zoomies-e2e-pool' }).click();
  await expect(preview).toContainText('runs-on: zoomies');
  await expect(preview).toContainText('answers every job that asks for this fleet');

  await addLabel(page, 'zoomies-gpu');
  await addLabel(page, 'cuda12');
  // Two labels of its own need the list form; the brand is implied by both.
  await expect(preview).toContainText('runs-on: [cuda12, zoomies-gpu]');
  await expect(preview).toContainText('jobs:');

  // With one label left, the shortest correct line is that label alone.
  await page.getByRole('button', { name: 'Remove the label cuda12' }).click();
  await expect(preview).toContainText('runs-on: zoomies-gpu');
});

test('choosing the host socket warns about root and demands a confirmation', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-pool');
  await next(page).click();
  await addLabel(page, 'gpu');
  await next(page).click();
  // Past Hosts, which a pool that takes the whole fleet leaves as it is.
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Backend' })).toBeVisible();

  await radio(page, 'docker-mode', 'host-socket').check();

  // Said in the largest words on the step, not in a footnote.
  const warning = page.getByText('Any job on this pool can become root on the host', {
    exact: true,
  });
  await expect(warning).toBeVisible();
  await expect(page.getByText(/A pull request from a fork is enough to do it/)).toBeVisible();

  const consent = page.getByRole('checkbox', {
    name: /I understand that this gives every job on this pool root on the host/,
  });
  await expect(consent).toBeVisible();
  await expect(consent).not.toBeChecked();

  // Until it is ticked the wizard will not go on, and it says why.
  await expect(next(page)).toBeDisabled();
  // Said twice on purpose -- beside the checkbox and in the "before the next
  // step" list -- so the first of the two is enough to assert on.
  await expect(
    page
      .getByText('Confirm that you understand what mounting the host socket gives every job')
      .first(),
  ).toBeVisible();

  await consent.check();
  await expect(next(page)).toBeEnabled();

  // Changing the answer and coming back asks again: consent is per decision.
  await radio(page, 'docker-mode', 'none').check();
  await expect(warning).toBeHidden();
  await radio(page, 'docker-mode', 'host-socket').check();
  await expect(
    page.getByRole('checkbox', { name: /I understand that this gives every job/ }),
  ).not.toBeChecked();
  await expect(next(page)).toBeDisabled();
});

test('the review step shows the server verdict and how many hosts could run it', async ({
  page,
}) => {
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-pool');
  await next(page).click();
  await addLabel(page, 'gpu');
  await toReview(page);
  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();

  // What will be created, in the words the pool pages use everywhere else.
  const summary = page.getByRole('region', { name: /What will be created/ });
  await expect(summary).toContainText('e2e-pool');
  await expect(summary).toContainText('gpu');
  await expect(summary).toContainText('Docker');

  // And the server's own dry run, before anything exists. The count of hosts
  // that could run it is the point of asking: either two of the seeded hosts
  // can (the third is cordoned), or -- once the fixture hosts have stopped
  // heartbeating, which they do 90s after the controller starts because no
  // agent is behind them -- none can, and the wizard says that even louder.
  const verdict = page.getByRole('region', { name: "The controller's check" });
  await expect(verdict).toContainText(
    /(\d+ connected hosts? can run this pool|\d+ of the \d+ hosts this pool reaches can run it|No connected host can run this pool)/,
    { timeout: 15_000 },
  );
});

test('a backend no host offers stops the wizard and offers one that does', async ({ page }) => {
  // The seeded hosts run Docker and probe Podman as absent, so a Podman pool is
  // the shape an operator actually gets stuck in: everything connected, nothing
  // able to run the pool. It would be enabled, its labels would match, and it
  // would never make a runner -- so the wizard refuses to create it while the
  // fleet has something else to offer.
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-podman');
  await next(page).click();
  await addLabel(page, 'gpu');
  await next(page).click();
  // Past Hosts, which a pool that takes the whole fleet leaves as it is.
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Backend' })).toBeVisible();
  await expect(next(page)).toBeEnabled();

  await radio(page, 'backend', 'podman').check();
  const stuck = page.getByRole('group', { name: 'No connected host can run a Podman pool' });
  await expect(stuck).toBeVisible();
  await expect(stuck).toContainText('No connected host offers Podman');
  // Named, with the count, rather than left as an exercise.
  await expect(stuck).toContainText(/Choose Docker \(\d+ hosts?\)/);
  await expect(next(page)).toBeDisabled();

  // The agent's own sentence comes through with its command as something to
  // copy rather than retype.
  await expect(stuck.getByText('systemctl --user enable --now podman.socket')).toBeVisible();
  await expect(
    stuck.getByRole('button', { name: /Copy the command systemctl --user enable/ }),
  ).toBeVisible();

  // And changed from here, without hunting back through the radio group.
  await stuck.getByRole('button', { name: /^Use Docker/ }).click();
  await expect(radio(page, 'backend', 'docker')).toBeChecked();
  await expect(stuck).toBeHidden();
  await expect(next(page)).toBeEnabled();
});

test('going back a step does not lose what was typed', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-remembered');
  await next(page).click();
  await addLabel(page, 'gpu');
  await addLabel(page, 'cuda12');
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Hosts' })).toBeVisible();
  await radio(page, 'placement', 'matching').check();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Backend' })).toBeVisible();
  await radio(page, 'backend', 'podman').check();

  await back(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Hosts' })).toBeVisible();
  // The placement choice survives the round trip like everything else.
  await expect(radio(page, 'placement', 'matching')).toBeChecked();

  await back(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Labels' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Remove the label gpu' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Remove the label cuda12' })).toBeVisible();

  await back(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Target' })).toBeVisible();
  // Branded on the way out of the field, and that is what comes back.
  await expect(nameField(page)).toHaveValue('zoomies-e2e-remembered');

  // Forward again, and the later steps are as they were left too.
  await next(page).click();
  await next(page).click();
  await next(page).click();
  await expect(radio(page, 'backend', 'podman')).toBeChecked();
});

test('editing the maximum runners still lets the wizard reach review', async ({ page }) => {
  // This caught a bug that made the wizard unusable: Input.svelte takes `type`
  // as a prop, so `bind:value` coerced a number input's value to a number
  // behind the caller's back. The draft holds strings, toNumber() called
  // `.trim()` on one, the derived validation threw, and the wizard was stuck
  // on Scaling for good -- touch the numbers at all and the pool could never
  // be created. Every number field on the step did it; the text fields beside
  // them were fine, which is why it took a test that types into one.

  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-pool');
  await next(page).click();
  await addLabel(page, 'gpu');
  await toStep(page, 'Scaling');

  const max = page.getByRole('spinbutton', { name: 'Maximum runners' });
  await max.fill('6');
  await expect(max).toHaveValue('6');

  await toReview(page);
  await expect(page.getByRole('region', { name: /What will be created/ })).toContainText(
    '6 maximum',
  );
});

test('a new pool with nothing to say for itself is named after a spaniel', async ({ page }) => {
  // A blank name field is answered with "test", and that name is then in every
  // runner name and every runs-on for the life of the pool. So the wizard fills
  // one in.
  //
  // A pool is named for its shape, and this one has none yet: the demo fleet is
  // Ubuntu and Debian on two architectures, so there is nothing every host
  // agrees on, and nothing has been asked for. A name that picked one of those
  // answers would be a name that lies about the rest of the fleet -- so the
  // spaniel carries it until the operator says more.
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  const name = nameField(page);
  await expect(name).toHaveValue(/^zoomies-[a-z]+$/);
  const first = await name.inputValue();

  // The label follows the name, so a pool is reachable without typing at all.
  await next(page).click();
  await expect(page.getByRole('button', { name: `Remove the label ${first}` })).toBeVisible();

  await back(page).click();
  await page.getByRole('button', { name: 'Spin a new name' }).click();
  await expect(name).not.toHaveValue(first);
  await expect(name).toHaveValue(/^zoomies-[a-z]+$/);
  const second = await name.inputValue();

  // And the label follows the roll, rather than leaving the pool answering to
  // a name it no longer has.
  await next(page).click();
  await expect(page.getByRole('button', { name: `Remove the label ${second}` })).toBeVisible();
  await expect(page.getByRole('button', { name: `Remove the label ${first}` })).toHaveCount(0);
});

test('the generated name follows the shape until somebody types their own', async ({ page }) => {
  // The name is a claim about what a job gets, so it follows the answers as
  // they are given: this is the same grammar `zoomies init` prints, and an
  // operator who meets both should meet one convention.
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  const name = nameField(page);

  await next(page).click();
  await next(page).click();
  await next(page).click();
  await page.getByLabel('Operating system').selectOption('debian-12');
  await back(page).click();
  await back(page).click();
  await back(page).click();
  // The size says nothing yet: it is the fleet's default, which every pool
  // here gets, so the name is the platform alone.
  await expect(name).toHaveValue('zoomies-debian-12');

  // A size the pool has of its own is part of the shape too, and it is the
  // part a workflow author is choosing between, so it leads. A pool that
  // leaves the size to its host has none to name, which is why the fixed
  // choice has to be made before the figure means anything.
  await toStep(page, 'Size');
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();
  await setSlider(page, 'CPU per runner', '4 cores');
  await back(page).click();
  await back(page).click();
  await back(page).click();
  await back(page).click();
  await expect(name).toHaveValue('zoomies-4vcpu-debian-12');

  // And switching back to the host's share drops it again, rather than
  // advertising a size this pool no longer asks for anywhere.
  await toStep(page, 'Size');
  await page.getByRole('radio', { name: 'One share of each host' }).check();
  await back(page).click();
  await back(page).click();
  await back(page).click();
  await back(page).click();
  await expect(name).toHaveValue('zoomies-debian-12');

  // Once a name is typed it belongs to the operator, and answering another
  // question must not rewrite it under their cursor. The brand is the one part
  // that is not theirs to drop, so it is put back on the typed name and left
  // at that.
  await name.fill('e2e-mine');
  await next(page).click();
  await next(page).click();
  await next(page).click();
  await page.getByLabel('Operating system').selectOption('ubuntu-24.04');
  await back(page).click();
  await back(page).click();
  await back(page).click();
  await expect(name).toHaveValue('zoomies-e2e-mine');
});

test('a name typed without the brand gains it', async ({ page }) => {
  // In GitHub's runner settings the prefix is the only thing telling our
  // runners from anyone else's, so it is not something an operator can type
  // their way out of. The field shows what will be saved rather than letting
  // the name change on its way to the server.
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  const name = nameField(page);

  await name.fill('gpu');
  await name.blur();
  await expect(name).toHaveValue('zoomies-gpu');

  // A name that already carries the brand keeps exactly one.
  await name.fill('zoomies-gpu');
  await name.blur();
  await expect(name).toHaveValue('zoomies-gpu');
});

test('a label the operator has changed is never filled in again', async ({ page }) => {
  // Removing the suggested chip has to stick. Refilling it on the next
  // keystroke would make the field impossible to empty, and would quietly put
  // back a label somebody deliberately took off.
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await next(page).click();
  const suggested = page.getByRole('button', { name: /^Remove the label zoomies-/ });
  await expect(suggested).toBeVisible();
  await suggested.click();
  await addLabel(page, 'gpu');

  await back(page).click();
  await page.getByRole('button', { name: 'Spin a new name' }).click();
  await next(page).click();
  await expect(page.getByRole('button', { name: 'Remove the label gpu' })).toBeVisible();
  await expect(page.getByRole('button', { name: /^Remove the label zoomies-/ })).toHaveCount(0);
});

test('the pools page offers the wizard and the wizard can be abandoned', async ({ page }) => {
  await goto(page, '/pools', 'Pools');
  await page.getByRole('link', { name: 'Create a pool' }).first().click();
  await expect(pageHeading(page, 'Create a pool')).toBeVisible();

  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(pageHeading(page, 'Pools')).toBeVisible();
  // Nothing was created on the way out.
  await expect(dataRows(grid(page, 'Pools'))).toHaveCount(2);
});

test('editing a pool is not refused because its own name is taken', async ({ page }) => {
  await goto(page, `/pools`, 'Pools');
  const rows = dataRows(grid(page, 'Pools'));
  await rows.filter({ hasText: FIXTURE.linuxPool }).getByRole('link').first().click();
  await expect(pageHeading(page, FIXTURE.linuxPool)).toBeVisible();

  await page.getByRole('button', { name: 'Edit' }).first().click();
  await expect(nameField(page)).toHaveValue(FIXTURE.linuxPool);

  // Straight through to the review step without touching the name. The dry run
  // used to compare the pool against every pool including itself, so this said
  // "a pool called zoomies-demo-linux-x64 already exists" -- about itself -- and
  // the only way to save any edit was to rename the pool as well.
  await toReview(page);
  await expect(page.getByText('already exists')).toBeHidden();
  await expect(page.getByRole('button', { name: /Save|Update/ })).toBeEnabled();
});

test('editing an automatic pool offers the advanced path, and elastic CPU with it', async ({
  page,
}) => {
  // An edit skips the fork, and a pool with nothing the simple path cannot
  // show opens on that path: target, labels, docker, review. None of those is
  // the size step, so a plain automatic pool -- the very pool elastic CPU is
  // for -- had no screen to turn it on from, and no way to the one that has
  // it. Both demo pools are tuned and open on the advanced path already, so
  // this makes the plain pool the wizard's own automatic path would have made.
  // Branded up front, because the server brands it anyway and the heading
  // this waits for is the name as saved.
  const name = `zoomies-e2e-plain-${Date.now()}`;
  let poolId = '';
  try {
    const created = await page.request.post('/api/v1/pools', {
      data: { name, installation_id: FIXTURE.installationId, labels: [name] },
    });
    expect(created.ok(), 'the plain pool was created').toBeTruthy();
    poolId = ((await created.json()) as { id: string }).id;

    await goto(page, `/pools/${poolId}?edit=1`, name);
    await expect(nameField(page)).toHaveValue(name);
    // The short path, as it should be for a pool with nothing to show on the
    // long one -- and the way onto the long one beside it.
    await expect(page.getByText('Step 1 of 4')).toBeVisible();
    await page.getByRole('button', { name: 'Show every setting' }).click();

    // It lands on the first step the short path skipped, with the rest ahead,
    // and the offer is gone because there is nothing left to show.
    await expect(page.getByRole('heading', { level: 2, name: 'Hosts' })).toBeVisible();
    await expect(page.getByText('Step 3 of 8')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Show every setting' })).toBeHidden();
    // Nothing typed on the short path was lost on the way.
    await back(page).click();
    await expect(page.getByRole('button', { name: `Remove the label ${name}` })).toBeVisible();

    await toStep(page, 'Size');
    await page.getByRole('combobox', { name: 'Elastic CPU' }).selectOption('automatic');
    // And the step says whether the hosts will honour it. The demo agents are
    // this build's, so every one of them can.
    await expect(
      page.getByText('Every host this pool can land on runs an agent that can lend CPU.'),
    ).toBeVisible();
    await toReview(page);
    await expect(page.getByRole('region', { name: /What will be saved/ })).toContainText(
      'Automatic, up to the host ceiling',
    );
    await page.getByRole('button', { name: 'Save changes' }).click();
    await expect(page.getByRole('button', { name: 'Save changes' })).toBeHidden();

    // Saved as the controller sees it: an elastic pool, not merely a form
    // that showed the word.
    const saved = (await page.request.get(`/api/v1/pools/${poolId}`).then((r) => r.json())) as {
      sizing?: string;
      cpu_burst?: { mode?: string };
    };
    expect(saved.cpu_burst?.mode).toBe('automatic');
    expect(saved.sizing).toBe('elastic');
  } finally {
    if (poolId) await page.request.delete(`/api/v1/pools/${poolId}?force=true`);
  }
});

test('a ticked pool can be edited from the same bar that enables and disables it', async ({
  page,
}) => {
  await goto(page, '/pools', 'Pools');
  const rows = dataRows(grid(page, 'Pools'));
  const bar = page.getByRole('group', { name: /Actions for the selected/ });

  await rows.filter({ hasText: FIXTURE.linuxPool }).getByRole('checkbox').check();
  await expect(bar.getByRole('button', { name: 'Edit' })).toBeEnabled();

  // Two ticked, and there is nothing sensible to edit: the button stays on
  // screen and says why rather than disappearing.
  await rows.filter({ hasText: FIXTURE.armPool }).getByRole('checkbox').check();
  await expect(bar.getByRole('button', { name: 'Edit' })).toBeDisabled();

  await rows.filter({ hasText: FIXTURE.armPool }).getByRole('checkbox').uncheck();
  await bar.getByRole('button', { name: 'Edit' }).click();
  await expect(nameField(page)).toHaveValue(FIXTURE.linuxPool);
});

test('the size step says which hosts a CPU limit has just cost the pool', async ({ page }) => {
  // The bug this covers: the hosts step counted every machine the selector
  // reached, the review step counted fewer, and nothing between them said that
  // a resource limit was what had happened. The demo fleet is a 16-CPU builder,
  // an 8-CPU builder and a cordoned arm64 box, so a 12-CPU runner is a request
  // only one of them can take.
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-big');
  await next(page).click();
  await addLabel(page, 'gpu');
  await next(page).click();
  await next(page).click();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Size' })).toBeVisible();

  // A fixed size, because that is the request only one machine can take. The
  // host's own share is by construction something every host can give.
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();
  await setSlider(page, 'CPU per runner', '12 cores');

  // Named host, and the two numbers an operator cannot compare for themselves:
  // what the machine has, and what one runner of this pool is charged. The
  // fixture hosts stop heartbeating 90s after the controller starts, and then
  // the honest reason for the same host is a different one.
  const fit = page.getByText(/can run (this pool|it)/).locator('..');
  await expect(fit).toContainText('demo-builder-2');
  await expect(fit).toContainText(/charged 12|not heartbeating/);

  // A limit the fleet can cover puts the host back, leaving only the cordoned
  // box -- which is the fleet's state rather than this pool's doing, so the
  // block stops blaming the limits an operator has already corrected.
  await setSlider(page, 'CPU per runner', '4 cores');
  await expect(fit).not.toContainText('demo-builder-2');
  await expect(fit).not.toContainText(/Matching the host selector is not the whole of it/);
});

test('a refused pool deletion keeps the typed confirmation available for retry', async ({
  page,
}) => {
  await goto(page, '/pools', 'Pools');
  const row = dataRows(grid(page, 'Pools')).filter({ hasText: FIXTURE.linuxPool });
  await row.getByRole('button', { name: `Actions for ${FIXTURE.linuxPool}` }).click();
  await page.getByRole('menuitem', { name: 'Delete', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Delete pool', exact: true });
  const typed = dialog.getByRole('textbox', { name: `Type ${FIXTURE.linuxPool} to confirm` });
  await typed.fill(FIXTURE.linuxPool);
  let attempts = 0;
  await page.route('**/api/v1/pools/*', async (route) => {
    if (route.request().method() !== 'DELETE') {
      await route.continue();
      return;
    }
    attempts++;
    await route.fulfill({
      status: 409,
      contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'conflict', message: 'The pool is still in use.' } }),
    });
  });
  await dialog.getByRole('button', { name: 'Delete pool', exact: true }).click();
  await expect(page.getByText('The pool is still in use.', { exact: true })).toBeVisible();
  await expect(dialog).toBeVisible();
  await expect(typed).toHaveValue(FIXTURE.linuxPool);
  await dialog.getByRole('button', { name: 'Delete pool', exact: true }).click();
  await expect.poll(() => attempts).toBe(2);
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(dialog).toBeHidden();
});

/*
 * "Destroy its runners immediately" interrupts work in progress, so the dialog
 * opens on the drain every time. It used to keep whatever the last open had
 * left, which put the destructive option one careless confirmation away.
 */
test("a pool page's delete dialog opens unticked after being cancelled ticked", async ({
  page,
}) => {
  const pools = (await page.request.get('/api/v1/pools').then((r) => r.json())) as {
    items: { id: string; name: string }[];
  };
  const pool = pools.items.find((p) => p.name === FIXTURE.linuxPool)!;
  await goto(page, `/pools/${pool.id}`, FIXTURE.linuxPool);

  await page.getByRole('button', { name: 'Delete', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Delete pool', exact: true });
  const force = dialog.getByRole('checkbox', { name: /Destroy its runners immediately/ });
  await expect(force).not.toBeChecked();
  await force.check();
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(dialog).toBeHidden();

  await page.getByRole('button', { name: 'Delete', exact: true }).click();
  await expect(force).not.toBeChecked();
});

test('a runner size can be typed the way people write it, and is written back in the largest unit', async ({
  page,
}) => {
  // "4g" is how Docker spells four gigabytes and "1.5" is how a ticket asks for
  // a core and a half. A slider alone could not take either, and a number box
  // labelled in megabytes made the operator do the arithmetic.
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-typed-size');
  await next(page).click();
  await addLabel(page, 'typed');
  await next(page).click();
  await next(page).click();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Size' })).toBeVisible();
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();

  const memory = page.getByRole('textbox', { name: 'Memory per runner' });
  for (const typed of ['4096mb', '4g', '4 GB']) {
    await memory.fill(typed);
    await memory.press('Enter');
    await expect(memory).toHaveValue('4 GB');
    await expect(page.getByRole('slider', { name: 'Memory per runner' })).toHaveAttribute(
      'aria-valuetext',
      '4 GB',
    );
  }

  // A figure off the notches is kept as typed rather than snapped to one.
  await memory.fill('1.5g');
  await memory.press('Enter');
  await expect(memory).toHaveValue('1.5 GB');

  const cpu = page.getByRole('textbox', { name: 'CPU per runner' });
  await cpu.fill('1.5');
  await cpu.press('Enter');
  await expect(cpu).toHaveValue('1.5 cores');
  await expect(page.getByRole('slider', { name: 'CPU per runner' })).toHaveAttribute(
    'aria-valuetext',
    '1.5 cores',
  );

  // Moving the slider moves the field with it.
  await setSlider(page, 'CPU per runner', '4 cores');
  await expect(cpu).toHaveValue('4 cores');

  // What cannot be read is said beside the field and changes nothing.
  await memory.fill('lots');
  await memory.press('Enter');
  await expect(page.getByRole('alert').filter({ hasText: '4 GB, 4096 MB or 4g' })).toBeVisible();
  await expect(page.getByRole('slider', { name: 'Memory per runner' })).toHaveAttribute(
    'aria-valuetext',
    '1.5 GB',
  );
});

test('an automatic size can carry a minimum for hosts with less than a share left', async ({
  page,
}) => {
  // An automatic pool waited for a whole slot's share of a host, however much
  // of one was idle. A minimum is what lets it start on what is left, and the
  // step says so without naming a standard the pool does not set.
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-auto-minimum');
  await next(page).click();
  await addLabel(page, 'auto-minimum');
  await next(page).click();
  await next(page).click();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Size' })).toBeVisible();
  await page.getByRole('radio', { name: 'One share of each host' }).check();

  const minimum = page.getByRole('textbox', { name: 'Minimum memory' });
  await minimum.fill('2g');
  await minimum.press('Enter');
  await expect(minimum).toHaveValue('2 GB');
  await expect(page.getByText(/whole slot's share/)).toContainText('never less than');
  await expect(page.getByText(/whole slot's share/)).toContainText('2 GB');
});

test('a container pool can keep its tool cache with its cache', async ({ page }) => {
  // The tool cache is a setting of the pool, offered beside the cache it is
  // shared with, rather than an environment variable to remember.
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-tool-cache');
  await next(page).click();
  await addLabel(page, 'tool-cache');
  await next(page).click();
  await next(page).click();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Size' })).toBeVisible();

  const tools = page.getByRole('checkbox', { name: 'Keep a tool cache as well' });
  await expect(tools).toHaveCount(0);
  await page.getByRole('checkbox', { name: 'Keep a cache between runners' }).check();
  await expect(tools).toBeVisible();
  await tools.check();
  await expect(tools).toBeChecked();
});

test('a fixed size can carry a minimum for hosts a little short of it', async ({ page }) => {
  // A standard a host cannot quite meet used to leave the job queued. The
  // minimum is the size the pool will still accept, and the step says what
  // happens with it in the pool's own figures.
  await goto(page, '/pools/new', 'Create a pool');
  await toAdvanced(page);
  await nameField(page).fill('e2e-minimum');
  await next(page).click();
  await addLabel(page, 'minimum');
  await next(page).click();
  await next(page).click();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Size' })).toBeVisible();
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();

  const memory = page.getByRole('textbox', { name: 'Memory per runner', exact: true });
  await memory.fill('8g');
  await memory.press('Enter');
  const minimum = page.getByRole('textbox', { name: 'Minimum memory' });
  await minimum.fill('6g');
  await minimum.press('Enter');
  await expect(minimum).toHaveValue('6 GB');
  await expect(page.getByText(/never less than/)).toContainText('6 GB');

  // A minimum above the standard is refused where it is typed.
  await minimum.fill('12g');
  await minimum.press('Enter');
  await expect(
    page.getByText('The minimum has to be at or below the standard memory.'),
  ).toBeVisible();
});
