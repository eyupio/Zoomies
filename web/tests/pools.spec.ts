/**
 * Pools, and the wizard that makes one.
 *
 * A pool is the only object in Zoomies that can quietly hand a workflow job
 * root on somebody's build host, so this protects the two things that stop
 * that being an accident: the list says which pools carry that risk and names
 * it, and the wizard spells out the dangerous choice and refuses to take it
 * without a deliberate confirmation. It also protects the wizard as a wizard --
 * five steps, a preview of the runs-on line the labels produce, the server's
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
const radio = (page: Page, group: 'backend' | 'docker-mode', value: string) =>
  page.locator(`input[name="pool-${group}"][value="${value}"]`);
const labelField = (page: Page) => page.getByRole('textbox', { name: 'Labels' });

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

  // The badge counts the risks; colour is never the only carrier.
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

test('the wizard walks target, labels, backend, scaling and review', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');

  // The step list by class: nothing in the accessibility tree tells it apart
  // from the breadcrumb list above it, which is also an ordered list in main.
  const steps = page.locator('ol.steps');
  for (const step of ['Target', 'Labels', 'Backend', 'Scaling', 'Review']) {
    await expect(steps).toContainText(step);
  }

  await expect(page.getByRole('heading', { level: 2, name: 'Target' })).toBeVisible();
  await expect(page.getByText('Step 1 of 5')).toBeVisible();
  await nameField(page).fill('e2e-pool');
  // One installation exists, so the wizard has chosen it already.
  await expect(page.getByLabel('GitHub installation')).toHaveValue(/ins_/);

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Labels' })).toBeVisible();
  await expect(page.getByText('Step 2 of 5')).toBeVisible();
  // The name has already produced a label; a second one is added on top.
  await expect(
    page.getByRole('button', { name: 'Remove the label zoomies-e2e-pool' }),
  ).toBeVisible();
  await addLabel(page, 'gpu');

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Backend' })).toBeVisible();
  await expect(radio(page, 'backend', 'docker')).toBeChecked();

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Scaling' })).toBeVisible();
  await expect(page.getByRole('spinbutton', { name: 'Maximum runners' })).toBeVisible();

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();
  await expect(page.getByText('Step 5 of 5')).toBeVisible();
  // The last step offers to create rather than to continue.
  await expect(page.getByRole('button', { name: 'Create pool' })).toBeVisible();
});

test('the labels step previews the runs-on line those labels produce', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');
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
  await nameField(page).fill('e2e-pool');
  await next(page).click();
  await addLabel(page, 'gpu');
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
  await nameField(page).fill('e2e-pool');
  await next(page).click();
  await addLabel(page, 'gpu');
  await next(page).click();
  await next(page).click();
  await next(page).click();
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
    /(\d+ connected hosts? can run this pool|No connected host can run this pool)/,
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
  await nameField(page).fill('e2e-podman');
  await next(page).click();
  await addLabel(page, 'gpu');
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
  await nameField(page).fill('e2e-remembered');
  await next(page).click();
  await addLabel(page, 'gpu');
  await addLabel(page, 'cuda12');
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Backend' })).toBeVisible();
  await radio(page, 'backend', 'podman').check();

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
  await nameField(page).fill('e2e-pool');
  await next(page).click();
  await addLabel(page, 'gpu');
  await next(page).click();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Scaling' })).toBeVisible();

  const max = page.getByRole('spinbutton', { name: 'Maximum runners' });
  await max.fill('6');
  await expect(max).toHaveValue('6');

  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();
  await expect(page.getByRole('region', { name: /What will be created/ })).toContainText(
    '6 maximum',
  );
});

test('a new pool arrives named after a spaniel and after what it will run on', async ({ page }) => {
  // A blank name field is answered with "test", and that name is then in every
  // runner name and every runs-on for the life of the pool. So the wizard
  // fills one in: the brand, a name from the kennel, and the infrastructure
  // the runners actually land on -- the demo fleet is all Linux Docker hosts
  // of two architectures, so it can say linux and honestly cannot say x64.
  await goto(page, '/pools/new', 'Create a pool');
  const name = nameField(page);
  await expect(name).toHaveValue(/^zoomies-[a-z]+-docker-linux$/);
  const first = await name.inputValue();

  // The label follows the name, so a pool is reachable without typing at all.
  await next(page).click();
  await expect(page.getByRole('button', { name: `Remove the label ${first}` })).toBeVisible();

  await back(page).click();
  await page.getByRole('button', { name: 'Spin a new name' }).click();
  await expect(name).not.toHaveValue(first);
  await expect(name).toHaveValue(/^zoomies-[a-z]+-docker-linux$/);
  const second = await name.inputValue();

  // And the label follows the roll, rather than leaving the pool answering to
  // a name it no longer has.
  await next(page).click();
  await expect(page.getByRole('button', { name: `Remove the label ${second}` })).toBeVisible();
  await expect(page.getByRole('button', { name: `Remove the label ${first}` })).toHaveCount(0);
});

test('the generated name follows the backend until somebody types their own', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');
  const name = nameField(page);
  await expect(name).toHaveValue(/-docker-linux$/);

  // The infrastructure half is a claim about where the runners land, so it
  // stops being true the moment the backend changes. Podman is the one no
  // demo host offers, which is beside the point here: the wizard renames the
  // pool on the way to finding that out.
  await next(page).click();
  await next(page).click();
  await radio(page, 'backend', 'podman').check();
  await back(page).click();
  await back(page).click();
  await expect(name).toHaveValue(/^zoomies-[a-z]+-podman-linux$/);

  // Once a name is typed it belongs to the operator, and choosing another
  // backend must not rewrite it under their cursor. The brand is the one part
  // that is not theirs to drop, so it is put back on the typed name and left
  // at that.
  await name.fill('e2e-mine');
  await next(page).click();
  await next(page).click();
  await radio(page, 'backend', 'docker').check();
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
