/**
 * Pools, and the page that makes and edits one.
 *
 * A pool is the only object in Zoomies that can quietly hand a workflow job
 * root on somebody's build host, so this protects the two things that stop
 * that being an accident: the list says which pools carry that risk and names
 * it, and the editor spells out the dangerous choice and refuses to take it
 * without a deliberate confirmation. It also protects the editor as a page --
 * one section a new pool has to read and a row that says the answer for every
 * other, a way to any of them in one press, a preview of the runs-on line the
 * labels produce, the server's own verdict before anything is created, and a
 * form that does not lose what was typed when a section is shut.
 */
import { expect, test, type Locator, type Page } from '@playwright/test';
import { browserOverride, dataRows, FIXTURE, goto, grid, pageHeading } from './support/fixtures';

test.use(browserOverride);

type SectionId = 'basics' | 'hosts' | 'runner' | 'size' | 'scaling' | 'speed';
const SECTIONS: readonly SectionId[] = ['basics', 'hosts', 'runner', 'size', 'scaling', 'speed'];

const nameField = (page: Page) => page.getByRole('textbox', { name: 'Pool name' });
const createButton = (page: Page) => page.getByRole('button', { name: 'Create pool' });
const saveButton = (page: Page) => page.getByRole('button', { name: 'Save changes' });

/**
 * One radio in one of the editor's radio groups.
 *
 * By input name and value rather than by accessible name: each option's name
 * is its label plus the whole consequence sentence, and two of them start with
 * the same word ("Docker" and "Docker in Docker"), so a name match is either
 * ambiguous or a copy of the product's prose.
 */
const radio = (page: Page, group: 'backend' | 'docker-mode' | 'placement', value: string) =>
  page.locator(`input[name="pool-${group}"][value="${value}"]`);
const labelField = (page: Page) => page.getByRole('textbox', { name: 'Labels' });
/** The first section's yes or no: does this pool's jobs build container images. */
const wantsDocker = (page: Page, value: 'none' | 'dind') =>
  page.locator(`input[name="pool-wants-docker"][value="${value}"]`);

/**
 * A section, by the id it is linked to (`#size`).
 *
 * By id because nothing in the accessibility tree says which of six boxes of
 * settings is which until it is open, and the id is what the rail and a link to
 * a section use -- so a test that finds it this way is finding it the way a
 * person who was sent there would.
 */
const section = (page: Page, id: SectionId): Locator => page.locator(`#pool-${id}`);

/** The button in a section's heading: the whole row, as a person presses it. */
const toggle = (page: Page, id: SectionId): Locator =>
  section(page, id).getByRole('heading', { level: 2 }).getByRole('button');

/**
 * Open a section the way a person does: press its row.
 *
 * Pressing an open one would shut it, so this asks first; a spec about a
 * control in a section is then about the control, not about whether the page
 * happened to have the section open already.
 */
async function openSection(page: Page, id: SectionId): Promise<void> {
  const button = toggle(page, id);
  if ((await button.getAttribute('aria-expanded')) !== 'true') await button.click();
  await expect(button).toHaveAttribute('aria-expanded', 'true');
}

/** Open the disclosure inside a section that holds the settings most pools never touch. */
async function openMore(page: Page, title: string): Promise<void> {
  const row = page.locator('details', { has: page.locator('summary', { hasText: title }) });
  if ((await row.getAttribute('open')) === null) await row.locator('summary').first().click();
  await expect(row).toHaveAttribute('open', '');
}

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

test('a refused runner limit is withdrawn as soon as the figure is changed', async ({ page }) => {
  // The refusal was folded into the same value that disables Save, and only
  // cleared by the next save -- which the disabled button then prevented.
  await goto(page, '/pools', 'Pools');
  const row = dataRows(grid(page, 'Pools')).filter({ hasText: FIXTURE.linuxPool });
  await page.route('**/api/v1/pools/*', async (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback();
    await route.fulfill({
      status: 422,
      contentType: 'application/json',
      body: JSON.stringify({
        error: { code: 'validation_failed', message: 'that maximum is refused' },
        errors: [{ field: 'max_runners', message: 'This fleet has no room for that many.' }],
      }),
    });
  });

  await row.getByRole('button', { name: `Actions for ${FIXTURE.linuxPool}` }).click();
  await page.getByRole('menuitem', { name: 'Adjust runner limits' }).click();
  const dialog = page.getByRole('dialog', { name: 'Runner limits' });
  const save = dialog.getByRole('button', { name: 'Save limits' });
  await dialog.getByRole('spinbutton', { name: 'Minimum runners' }).fill('1');
  await dialog.getByRole('spinbutton', { name: 'Maximum runners' }).fill('9');
  await save.click();
  await expect(dialog).toContainText('This fleet has no room for that many.');
  await expect(save).toBeDisabled();

  await dialog.getByRole('spinbutton', { name: 'Maximum runners' }).fill('8');
  await expect(dialog).not.toContainText('This fleet has no room for that many.');
  await expect(save).toBeEnabled();
});

test('a new pool opens on the one section it needs, and every other says its answer', async ({
  page,
}) => {
  await goto(page, '/pools/new', 'Create a pool');

  // It is one page, not a procedure: nothing is a step, and nothing is pressed
  // to reach the next thing.
  await expect(page.getByRole('button', { name: 'Next' })).toHaveCount(0);
  await expect(page.getByText(/Step \d+ of \d+/)).toHaveCount(0);

  // What a first pool needs is on screen at once: a name, the label workflows
  // ask for and whether its jobs build images. The GitHub account is chosen.
  await expect(toggle(page, 'basics')).toHaveAttribute('aria-expanded', 'true');
  await expect(nameField(page)).toHaveValue(/^zoomies-[a-z]+$/);
  await expect(labelField(page)).toBeVisible();
  await expect(wantsDocker(page, 'none')).toBeChecked();
  await expect(page.getByText(/Registers runners with/)).toBeVisible();

  // Every other section is a row that says what it will do, without being
  // opened -- which is how a first-time reader sees that the defaults are
  // chosen, and an experienced one reads the whole pool in six lines.
  const answers: [SectionId, RegExp][] = [
    ['hosts', /Any host that can run it/],
    ['runner', /Docker · default image/],
    ['size', /One share of each host/],
    ['scaling', /\d+ to \d+ runners · idle 5m/],
    ['speed', /Scratch space on disk · no cache/],
  ];
  for (const [id, answer] of answers) {
    await expect(toggle(page, id)).toHaveAttribute('aria-expanded', 'false');
    await expect(section(page, id)).toContainText(answer);
  }

  // And it can be made now. Nothing was asked that has no answer.
  await expect(createButton(page)).toBeEnabled();
});

test('every section opens from its own row, and Expand all opens them together', async ({
  page,
}) => {
  await goto(page, '/pools/new', 'Create a pool');

  const share = page.getByRole('radio', { name: 'One share of each host' });
  await toggle(page, 'size').click();
  await expect(toggle(page, 'size')).toHaveAttribute('aria-expanded', 'true');
  await expect(share).toBeChecked();
  // The same press shuts it again, and what was in it is not on the page.
  await toggle(page, 'size').click();
  await expect(toggle(page, 'size')).toHaveAttribute('aria-expanded', 'false');
  await expect(share).toHaveCount(0);

  await page.getByRole('button', { name: 'Expand all' }).click();
  for (const id of SECTIONS)
    await expect(toggle(page, id)).toHaveAttribute('aria-expanded', 'true');
  await page.getByRole('button', { name: 'Collapse all' }).click();
  for (const id of SECTIONS) {
    await expect(toggle(page, id)).toHaveAttribute('aria-expanded', 'false');
  }
});

test('on a wide screen a rail jumps to a section, and a link to one opens it', async ({
  page,
  isMobile,
}) => {
  test.skip(!!isMobile, 'the rail is drawn only where there is room for it; a phone has the rows');
  await goto(page, '/pools/new', 'Create a pool');

  const rail = page.getByRole('navigation', { name: 'On this page' });
  await expect(rail.getByRole('link')).toHaveCount(SECTIONS.length);
  await rail.getByRole('link', { name: 'Scaling' }).click();

  // Opened, brought into view, and the cursor is on it -- so a keyboard user
  // arrives somewhere rather than at the top of the page they left.
  await expect(toggle(page, 'scaling')).toHaveAttribute('aria-expanded', 'true');
  await expect(toggle(page, 'scaling')).toBeFocused();
  await expect(section(page, 'scaling')).toBeInViewport();
  // And the address says where it is, as a replacement: it is a place on this
  // page, and the back button should leave the page rather than the place.
  await expect(page).toHaveURL(/\/pools\/new#scaling$/);
});

test('a link to a section opens it', async ({ page }) => {
  await goto(page, '/pools/new#size', 'Create a pool');
  await expect(toggle(page, 'size')).toHaveAttribute('aria-expanded', 'true');
  await expect(page.getByRole('radio', { name: 'One share of each host' })).toBeChecked();
  // Only that one, besides the first a new pool opens on.
  await expect(toggle(page, 'scaling')).toHaveAttribute('aria-expanded', 'false');
});

test('a pool can be made from the first section alone', async ({ page }) => {
  const name = `e2e-first-${Date.now()}`;
  let poolId = '';
  try {
    await goto(page, '/pools/new', 'Create a pool');
    await nameField(page).fill(name);
    await nameField(page).blur();
    await createButton(page).click();

    // It lands on the pool it made, which is where the runs-on line is.
    await expect(pageHeading(page, `zoomies-${name}`)).toBeVisible();
    poolId = new URL(page.url()).pathname.split('/')[2] ?? '';
    expect(poolId, 'the address names the new pool').not.toBe('');

    // Made as the defaults would make it: reached by its own label, no Docker,
    // no figure of its own for the size.
    const made = (await page.request.get(`/api/v1/pools/${poolId}`).then((r) => r.json())) as {
      labels?: string[];
      docker_mode?: string;
      resources?: { cpus?: number; memory_mb?: number };
    };
    expect(made.labels).toContain(`zoomies-${name}`);
    expect(made.docker_mode ?? 'none').toBe('none');
    expect(made.resources?.cpus ?? 0).toBe(0);
    expect(made.resources?.memory_mb ?? 0).toBe(0);
  } finally {
    if (poolId) await page.request.delete(`/api/v1/pools/${poolId}?force=true`);
  }
});

// Turning Docker on used to mean finding it on the advanced path, three steps
// past anything the operator came for. A pool that builds images is an ordinary
// pool, so the first section asks, and says what the answer costs.
test('the first section can give a pool its own Docker daemon', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');

  await page.getByRole('radio', { name: /Yes, give each runner a Docker daemon/ }).check();
  // What it costs is on the screen that asks, not on a page found later.
  await expect(page.getByText('privileged container')).toBeVisible();
  await expect(page.getByText('share one slot')).toBeVisible();

  // And it is the same answer the runner section holds, which is where the
  // third, dangerous, one is chosen.
  await openSection(page, 'runner');
  await expect(radio(page, 'docker-mode', 'dind')).toBeChecked();

  // The page reads the server's own answer, which is where the image with a
  // Docker client in it appears without anybody pinning one.
  await page.getByText('Every setting, as it will be created').click();
  await expect(page.getByText('zoomies-runner-docker')).toBeVisible();
});

test('pressing Create while something is wrong says what, and goes there', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');

  // Take the only label away, so no workflow could ask for this pool.
  await page.getByRole('button', { name: /^Remove the label zoomies-/ }).click();
  await expect(page.getByText('1 thing to fix before this can be created.')).toBeVisible();

  // The button is not disabled: a disabled button cannot explain itself. It
  // shows the problem beside the field it is about, and nothing is created.
  await createButton(page).click();
  await expect(
    page.getByText('Add at least one label, or no workflow can ask for this pool.'),
  ).toBeVisible();
  await expect(page).toHaveURL(/\/pools\/new/);

  // Putting one back clears it, and the bar says so.
  await addLabel(page, 'gpu');
  await expect(page.getByText(/thing to fix/)).toHaveCount(0);
});

test('a section holding the problem opens by itself, and the row says so', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');

  // Choose the host socket, which needs a confirmation, and shut the section
  // again so that what is wrong is out of sight.
  await openSection(page, 'runner');
  await radio(page, 'docker-mode', 'host-socket').check();
  await toggle(page, 'runner').click();
  await expect(toggle(page, 'runner')).toHaveAttribute('aria-expanded', 'false');

  // Create was pressed with the confirmation outstanding: the section the
  // problem is in is opened, and its row counts what is wrong.
  await createButton(page).click();
  await expect(toggle(page, 'runner')).toHaveAttribute('aria-expanded', 'true');
  await expect(section(page, 'runner')).toContainText('1 to fix');
  await expect(
    page
      .getByText('Confirm that you understand what mounting the host socket gives every job')
      .first(),
  ).toBeVisible();
});

test('shutting a section does not lose what was typed in it', async ({ page }) => {
  // This is what going back a step had to be true of, and it is truer here:
  // the draft is one object, and a section is only a way of looking at it.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-remembered');
  await addLabel(page, 'gpu');
  await addLabel(page, 'cuda12');
  await openSection(page, 'hosts');
  await radio(page, 'placement', 'matching').check();
  await openSection(page, 'runner');
  await radio(page, 'backend', 'podman').check();

  for (const id of ['basics', 'hosts', 'runner'] as const) await toggle(page, id).click();
  for (const id of ['basics', 'hosts', 'runner'] as const) {
    await expect(toggle(page, id)).toHaveAttribute('aria-expanded', 'false');
  }
  // The rows still say it, shut.
  await expect(section(page, 'runner')).toContainText('Podman');
  await expect(section(page, 'basics')).toContainText('gpu, cuda12');

  for (const id of ['basics', 'hosts', 'runner'] as const) await openSection(page, id);
  // Branded on the way out of the field, and that is what comes back.
  await expect(nameField(page)).toHaveValue('zoomies-e2e-remembered');
  await expect(page.getByRole('button', { name: 'Remove the label gpu' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Remove the label cuda12' })).toBeVisible();
  await expect(radio(page, 'placement', 'matching')).toBeChecked();
  await expect(radio(page, 'backend', 'podman')).toBeChecked();
});

test('every other section opens on the answer a new pool would have chosen', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');

  await openSection(page, 'hosts');
  // A new pool reaches the whole fleet until an operator says otherwise.
  await expect(radio(page, 'placement', 'any')).toBeChecked();

  await openSection(page, 'runner');
  await expect(radio(page, 'backend', 'docker')).toBeChecked();

  await openSection(page, 'size');
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

  await openSection(page, 'scaling');
  await expect(page.getByRole('spinbutton', { name: 'Maximum runners' })).toBeVisible();
  // GitHub adds the default labels to every ephemeral runner, so leaving them
  // out is offered only once the pool reuses its runners.
  const noDefaults = page.getByRole('checkbox', { name: 'Leave out the default labels' });
  await expect(noDefaults).toHaveCount(0);
  await page.getByRole('checkbox', { name: 'Destroy each runner after one job' }).uncheck();
  await expect(noDefaults).toBeVisible();
  await expect(noDefaults).not.toBeChecked();
  await page.getByRole('checkbox', { name: 'Destroy each runner after one job' }).check();

  // The timings are behind a row of their own, every one empty: empty is the
  // answer that means "follow the fleet" -- and the fleet's own figure is the
  // placeholder beside it.
  await openMore(page, 'Priority and runner timings');
  const provision = page.getByRole('textbox', { name: 'Provision timeout' });
  await expect(provision).toHaveValue('');
  await expect(provision).toHaveAttribute('placeholder', /this fleet's/);
  await expect(page.getByText('This pool follows the fleet on every runner timing.')).toBeVisible();

  await openSection(page, 'speed');
  await expect(
    page.getByRole('checkbox', { name: 'Keep the work folder in memory' }),
  ).not.toBeChecked();
  await expect(
    page.getByRole('checkbox', { name: 'Keep a cache between runners' }),
  ).not.toBeChecked();
});

test('a pool timing override is kept, and clearing it hands the setting back to the fleet', async ({
  page,
}) => {
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-slow');
  await openSection(page, 'scaling');
  await openMore(page, 'Priority and runner timings');

  const provision = page.getByRole('textbox', { name: 'Provision timeout' });
  await provision.fill('45m');
  await expect(
    page.getByText('This pool overrides 1 of 5 runner timings; the rest follow the fleet.'),
  ).toBeVisible();
  // The row says it shut, so the override is never hidden behind a closed one.
  await expect(section(page, 'scaling')).toContainText('1 timing override');

  // A timeout inside the time a runner of this pool takes to start is said
  // while the number is being chosen, not afterwards.
  await provision.fill('5m');
  await expect(page.getByText('Shorter than a runner of this pool takes to start')).toBeVisible();

  // Cleared, the pool follows the fleet again and the caution goes with it.
  await provision.fill('');
  await expect(page.getByText('This pool follows the fleet on every runner timing.')).toBeVisible();
  await expect(page.getByText('Shorter than a runner of this pool takes to start')).toHaveCount(0);
});

test('the runner section names the image the chosen operating system will boot', async ({
  page,
}) => {
  await goto(page, '/pools/new', 'Create a pool');
  await openSection(page, 'runner');

  // Nothing chosen: the pool follows the controller's default image and any
  // host will do.
  const os = page.getByLabel('Operating system');
  await expect(os).toHaveValue('');
  await expect(page.getByText("Runners boot the controller's default image.")).toBeVisible();

  // The options come from the server's catalogue, so choosing one must name a
  // real published image rather than a string the UI made up.
  await os.selectOption('debian-12');
  await expect(page.getByText('ghcr.io/eyupio/zoomies-runner:debian-12')).toBeVisible();

  // The demo fleet has one Debian host, so the section can say so before the
  // operator has pressed anything.
  await expect(page.getByText(/1 connected host match/)).toBeVisible();

  // And an operating system nothing in the fleet runs is said to match nothing.
  await os.selectOption('fedora-42');
  await expect(page.getByText('No connected host matches')).toBeVisible();
});

test('the hosts section keeps a pool to an architecture and says which machines that is', async ({
  page,
}) => {
  // The demo fleet is two amd64 builders and one arm64 box, and the arm64 one
  // is cordoned. That last part is the case worth protecting: a selector can
  // match a host that is not taking work, and the section has to say so rather
  // than promise a runner the controller then refuses.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-arm');
  await openSection(page, 'hosts');

  // A new pool takes the whole fleet, and says so in those words.
  await expect(radio(page, 'placement', 'any')).toBeChecked();
  const readout = page.getByText(/Every connected host matches/);
  await expect(readout).toBeVisible();

  await radio(page, 'placement', 'matching').check();
  await page.getByLabel('Architecture', { exact: true }).selectOption('arm64');

  // One host, named, and honest about the fact that it would take nothing.
  await expect(page.getByText('1 of 3 connected hosts match')).toBeVisible();
  // The host also appears in the controller's check further down. The
  // matching-host badges have no distinct role, so scope the name to their
  // readout rather than relying on page-wide text uniqueness.
  await expect(page.locator('.match-hosts').getByText('demo-arm-1', { exact: true })).toBeVisible();
  await expect(page.getByText(/cordoned or not heartbeating/)).toBeVisible();
  // And the row says the same shut.
  await expect(section(page, 'hosts')).toContainText('Only hosts where arch=arm64');

  // The other architecture is the two builders, and they are taking work.
  await page.getByLabel('Architecture', { exact: true }).selectOption('amd64');
  await expect(page.getByText('2 of 3 connected hosts match')).toBeVisible();
  await expect(page.getByText(/cordoned or not heartbeating/)).toHaveCount(0);

  // And the choice reaches the pool that gets created.
  await page.getByText('Every setting, as it will be created').click();
  await expect(page.getByRole('region', { name: /What will be created/ })).toContainText(
    'arch=amd64',
  );
});

test('the first section previews the runs-on line those labels produce', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-pool');

  const preview = page.locator('figure');
  // The page filled the label in from the name, so the preview is already a
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
  await openSection(page, 'runner');
  await radio(page, 'docker-mode', 'host-socket').check();

  // Said in the largest words on the section, not in a footnote.
  const warning = page.getByText('Any job on this pool can become root on the host', {
    exact: true,
  });
  await expect(warning).toBeVisible();
  await expect(page.getByText(/A pull request from a fork is enough to do it/)).toBeVisible();

  // The first section stops offering a yes or no for it: the answer is not one
  // of those two, and says where it is chosen.
  await expect(section(page, 'basics')).toContainText("Docker for jobs: the host's socket");

  const consent = page.getByRole('checkbox', {
    name: /I understand that this gives every job on this pool root on the host/,
  });
  await expect(consent).toBeVisible();
  await expect(consent).not.toBeChecked();

  // Until it is ticked nothing can be created, and the bar says why.
  await expect(page.getByText('1 thing to fix before this can be created.')).toBeVisible();
  await createButton(page).click();
  // Said twice on purpose -- beside the checkbox and on the row -- so the first
  // of the two is enough to assert on.
  await expect(
    page
      .getByText('Confirm that you understand what mounting the host socket gives every job')
      .first(),
  ).toBeVisible();
  await expect(page).toHaveURL(/\/pools\/new/);

  await consent.check();
  await expect(page.getByText(/thing to fix/)).toHaveCount(0);

  // Changing the answer and coming back asks again: consent is per decision.
  await radio(page, 'docker-mode', 'none').check();
  await expect(warning).toBeHidden();
  await radio(page, 'docker-mode', 'host-socket').check();
  await expect(
    page.getByRole('checkbox', { name: /I understand that this gives every job/ }),
  ).not.toBeChecked();
  await expect(page.getByText('1 thing to fix before this can be created.')).toBeVisible();
});

test('the page shows the server verdict and how many hosts could run it', async ({ page }) => {
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-pool');
  await addLabel(page, 'gpu');

  // What will be created, in the words the pool pages use everywhere else.
  await page.getByText('Every setting, as it will be created').click();
  const summary = page.getByRole('region', { name: /What will be created/ });
  await expect(summary).toContainText('e2e-pool');
  await expect(summary).toContainText('gpu');
  await expect(summary).toContainText('Docker');

  // And the server's own dry run, before anything exists. The count of hosts
  // that could run it is the point of asking: either two of the seeded hosts
  // can (the third is cordoned), or -- once the fixture hosts have stopped
  // heartbeating, which they do 90s after the controller starts because no
  // agent is behind them -- none can, and the page says that even louder.
  const verdict = page.getByRole('region', { name: "The controller's check" });
  await expect(verdict).toContainText(
    /(\d+ connected hosts? can run this pool|\d+ of the \d+ hosts this pool reaches can run it|No connected host can run this pool)/,
    { timeout: 15_000 },
  );
});

test('the bar beside Create says how much room the pool has, before it is made', async ({
  page,
  isMobile,
}) => {
  test.skip(!!isMobile, 'a phone keeps the bar to what needs acting on, and shows this below');
  await goto(page, '/pools/new', 'Create a pool');
  // The same answer as the controller's panel, in a line that is always on
  // screen: either room for runners on the hosts that can take them, or -- once
  // the fixture hosts have stopped heartbeating -- that none can.
  await expect(
    page.getByText(
      /(Room for \d+ runners? on \d+ hosts?\.|\d+ connected hosts? can run this pool\.|No connected host can run this pool yet)/,
    ),
  ).toBeVisible({ timeout: 15_000 });
});

test('a backend no host offers stops the pool being made and offers one that does', async ({
  page,
}) => {
  // The seeded hosts run Docker and probe Podman as absent, so a Podman pool is
  // the shape an operator actually gets stuck in: everything connected, nothing
  // able to run the pool. It would be enabled, its labels would match, and it
  // would never make a runner -- so the editor refuses to create it while the
  // fleet has something else to offer.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-podman');
  await addLabel(page, 'gpu');
  await openSection(page, 'runner');
  await expect(page.getByText(/thing to fix/)).toHaveCount(0);

  await radio(page, 'backend', 'podman').check();
  const stuck = page.getByRole('group', { name: 'No connected host can run a Podman pool' });
  await expect(stuck).toBeVisible();
  await expect(stuck).toContainText('No connected host offers Podman');
  // Named, with the count, rather than left as an exercise.
  await expect(stuck).toContainText(/Choose Docker \(\d+ hosts?\)/);
  await expect(page.getByText('1 thing to fix before this can be created.')).toBeVisible();
  await expect(section(page, 'runner')).toContainText('1 to fix');

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
  await expect(page.getByText(/thing to fix/)).toHaveCount(0);
});

test('editing the maximum runners still lets the pool be created', async ({ page }) => {
  // This caught a bug that made the editor unusable: Input.svelte takes `type`
  // as a prop, so `bind:value` coerced a number input's value to a number
  // behind the caller's back. The draft holds strings, toNumber() called
  // `.trim()` on one, the derived validation threw, and the form was stuck for
  // good -- touch the numbers at all and the pool could never be created.
  // Every number field in the section did it; the text fields beside them were
  // fine, which is why it took a test that types into one.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-pool');
  await addLabel(page, 'gpu');
  await openSection(page, 'scaling');

  const max = page.getByRole('spinbutton', { name: 'Maximum runners' });
  await max.fill('6');
  await expect(max).toHaveValue('6');
  // The row says it, and so does the pool the page is about to make.
  await expect(section(page, 'scaling')).toContainText('0 to 6 runners');
  await expect(createButton(page)).toBeEnabled();

  await page.getByText('Every setting, as it will be created').click();
  await expect(page.getByRole('region', { name: /What will be created/ })).toContainText(
    '6 maximum',
  );
});

test('a new pool with nothing to say for itself is named after a spaniel', async ({ page }) => {
  // A blank name field is answered with "test", and that name is then in every
  // runner name and every runs-on for the life of the pool. So the editor fills
  // one in.
  //
  // A pool is named for its shape, and this one has none yet: the demo fleet is
  // Ubuntu and Debian on two architectures, so there is nothing every host
  // agrees on, and nothing has been asked for. A name that picked one of those
  // answers would be a name that lies about the rest of the fleet -- so the
  // spaniel carries it until the operator says more.
  await goto(page, '/pools/new', 'Create a pool');
  const name = nameField(page);
  await expect(name).toHaveValue(/^zoomies-[a-z]+$/);
  const first = await name.inputValue();

  // The label follows the name, so a pool is reachable without typing at all.
  await expect(page.getByRole('button', { name: `Remove the label ${first}` })).toBeVisible();

  await page.getByRole('button', { name: 'Spin a new name' }).click();
  await expect(name).not.toHaveValue(first);
  await expect(name).toHaveValue(/^zoomies-[a-z]+$/);
  const second = await name.inputValue();

  // And the label follows the roll, rather than leaving the pool answering to
  // a name it no longer has.
  await expect(page.getByRole('button', { name: `Remove the label ${second}` })).toBeVisible();
  await expect(page.getByRole('button', { name: `Remove the label ${first}` })).toHaveCount(0);
});

test('the generated name follows the shape until somebody types their own', async ({ page }) => {
  // The name is a claim about what a job gets, so it follows the answers as
  // they are given: this is the same grammar `zoomies init` prints, and an
  // operator who meets both should meet one convention.
  await goto(page, '/pools/new', 'Create a pool');
  const name = nameField(page);

  await openSection(page, 'runner');
  await page.getByLabel('Operating system').selectOption('debian-12');
  // The size says nothing yet: it is the fleet's default, which every pool
  // here gets, so the name is the platform alone.
  await expect(name).toHaveValue('zoomies-debian-12');

  // A size the pool has of its own is part of the shape too, and it is the
  // part a workflow author is choosing between, so it leads. A pool that
  // leaves the size to its host has none to name, which is why the fixed
  // choice has to be made before the figure means anything.
  await openSection(page, 'size');
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();
  await setSlider(page, 'CPU per runner', '4 cores');
  await expect(name).toHaveValue('zoomies-4vcpu-debian-12');

  // And switching back to the host's share drops it again, rather than
  // advertising a size this pool no longer asks for anywhere.
  await page.getByRole('radio', { name: 'One share of each host' }).check();
  await expect(name).toHaveValue('zoomies-debian-12');

  // Once a name is typed it belongs to the operator, and answering another
  // question must not rewrite it under their cursor. The brand is the one part
  // that is not theirs to drop, so it is put back on the typed name and left
  // at that.
  await name.fill('e2e-mine');
  await name.blur();
  await page.getByLabel('Operating system').selectOption('ubuntu-24.04');
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
  const suggested = page.getByRole('button', { name: /^Remove the label zoomies-/ });
  await expect(suggested).toBeVisible();
  await suggested.click();
  await addLabel(page, 'gpu');

  await page.getByRole('button', { name: 'Spin a new name' }).click();
  await expect(page.getByRole('button', { name: 'Remove the label gpu' })).toBeVisible();
  await expect(page.getByRole('button', { name: /^Remove the label zoomies-/ })).toHaveCount(0);
});

test('the pools page offers the editor and it can be abandoned', async ({ page }) => {
  await goto(page, '/pools', 'Pools');
  await page.getByRole('link', { name: 'Create a pool' }).first().click();
  await expect(pageHeading(page, 'Create a pool')).toBeVisible();

  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(pageHeading(page, 'Pools')).toBeVisible();
  // Nothing was created on the way out.
  await expect(dataRows(grid(page, 'Pools'))).toHaveCount(2);
});

test('an existing pool opens on none of its sections, and Save waits for a change', async ({
  page,
  isMobile,
}) => {
  await goto(page, '/pools', 'Pools');
  const rows = dataRows(grid(page, 'Pools'));
  await rows.filter({ hasText: FIXTURE.linuxPool }).getByRole('link').first().click();
  await expect(pageHeading(page, FIXTURE.linuxPool)).toBeVisible();
  await page.getByRole('button', { name: 'Edit' }).first().click();

  // An operator who came to change one setting is better served by six lines
  // they can read than by a form they have to scroll, and the rows are the way
  // to the one they came for.
  for (const id of SECTIONS)
    await expect(toggle(page, id)).toHaveAttribute('aria-expanded', 'false');
  await expect(section(page, 'basics')).toContainText(FIXTURE.linuxPool);

  // Nothing has changed, so there is nothing to save and the bar says so. A
  // phone keeps the sentence for a screen reader and gives the line of screen
  // to the buttons, so there it is in the page but not drawn.
  await expect(saveButton(page)).toBeDisabled();
  const noChanges = page.getByText('No changes yet.');
  await expect(noChanges).toBeAttached();
  const box = await noChanges.boundingBox();
  if (isMobile) expect(box?.width ?? 0).toBeLessThanOrEqual(1);
  else expect(box?.width ?? 0).toBeGreaterThan(40);

  // Change one thing: only the section it is in is marked, and Save is on.
  await openSection(page, 'scaling');
  const max = page.getByRole('spinbutton', { name: 'Maximum runners' });
  const before = await max.inputValue();
  await max.fill(String(Number(before) + 1));
  await expect(section(page, 'scaling')).toContainText('Edited');
  await expect(section(page, 'size')).not.toContainText('Edited');
  await expect(saveButton(page)).toBeEnabled();

  // Typing the old figure back is not an edit.
  await max.fill(before);
  await expect(section(page, 'scaling')).not.toContainText('Edited');
  await expect(saveButton(page)).toBeDisabled();
});

test('editing a pool is not refused because its own name is taken', async ({ page }) => {
  await goto(page, `/pools`, 'Pools');
  const rows = dataRows(grid(page, 'Pools'));
  await rows.filter({ hasText: FIXTURE.linuxPool }).getByRole('link').first().click();
  await expect(pageHeading(page, FIXTURE.linuxPool)).toBeVisible();

  await page.getByRole('button', { name: 'Edit' }).first().click();
  await openSection(page, 'basics');
  await expect(nameField(page)).toHaveValue(FIXTURE.linuxPool);

  // Change something without touching the name. The dry run used to compare the
  // pool against every pool including itself, so this said "a pool called
  // zoomies-demo-linux-x64 already exists" -- about itself -- and the only way
  // to save any edit was to rename the pool as well.
  await openSection(page, 'scaling');
  const max = page.getByRole('spinbutton', { name: 'Maximum runners' });
  await max.fill(String(Number(await max.inputValue()) + 1));
  await expect(page.getByText('already exists')).toBeHidden();
  await expect(saveButton(page)).toBeEnabled();
});

test('editing an automatic pool offers elastic CPU in the size section', async ({ page }) => {
  // The plain automatic pool is exactly the pool elastic CPU is for. The
  // wizard's short path never showed the step that has it, so a pool made the
  // way most pools are had no screen to turn it on from. Both demo pools are
  // tuned already, so this makes the plain pool a first-time operator would
  // have made.
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

    // A link to the section, as one sent to a colleague would be.
    await goto(page, `/pools/${poolId}?edit=1#size`, name);
    await expect(toggle(page, 'size')).toHaveAttribute('aria-expanded', 'true');

    await page.getByRole('radio', { name: 'Automatic boost' }).check();
    // And the section says whether the hosts will honour it. The demo agents
    // are this build's, so every one of them can.
    await expect(
      page.getByText('Every host this pool can land on runs an agent that can lend CPU.'),
    ).toBeVisible();
    await expect(section(page, 'size')).toContainText('Edited');
    await expect(section(page, 'size')).toContainText('elastic CPU boosting');
    await page.getByText('Every setting, as it will be saved').click();
    await expect(page.getByRole('region', { name: /What will be saved/ })).toContainText(
      'Automatic, up to the host ceiling',
    );
    await saveButton(page).click();
    await expect(saveButton(page)).toBeHidden();

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
  await openSection(page, 'basics');
  await expect(nameField(page)).toHaveValue(FIXTURE.linuxPool);
});

test('the size section says which hosts a CPU limit has just cost the pool', async ({ page }) => {
  // The bug this covers: the hosts section counted every machine the selector
  // reached, the controller counted fewer, and nothing between them said that
  // a resource limit was what had happened. The demo fleet is a 16-CPU builder,
  // an 8-CPU builder and a cordoned arm64 box, so a 12-CPU runner is a request
  // only one of them can take.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-big');
  await addLabel(page, 'gpu');
  await openSection(page, 'size');

  // A fixed size, because that is the request only one machine can take. The
  // host's own share is by construction something every host can give.
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();
  await setSlider(page, 'CPU per runner', '12 cores');

  // Named host, and the two numbers an operator cannot compare for themselves:
  // what the machine has, and what one runner of this pool is charged. The
  // fixture hosts stop heartbeating 90s after the controller starts, and then
  // the honest reason for the same host is a different one. Scoped to this
  // section: the controller's check says the same thing further down.
  const fit = section(page, 'size')
    .getByText(/can run (this pool|it)/)
    .locator('..');
  await expect(fit).toContainText('demo-builder-2');
  await expect(fit).toContainText(/charged 12|not heartbeating/);

  // A limit the fleet can cover puts the host back, leaving only the cordoned
  // box -- which is the fleet's state rather than this pool's doing, so the
  // block stops blaming the limits an operator has already corrected.
  await setSlider(page, 'CPU per runner', '4 cores');
  await expect(fit).not.toContainText('demo-builder-2');
  await expect(fit).not.toContainText(/Matching the host selector is not the whole of it/);
});

test('a runner size can be typed the way people write it, and is written back in the largest unit', async ({
  page,
}) => {
  // "4g" is how Docker spells four gigabytes and "1.5" is how a ticket asks for
  // a core and a half. A slider alone could not take either, and a number box
  // labelled in megabytes made the operator do the arithmetic.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-typed-size');
  await addLabel(page, 'typed');
  await openSection(page, 'size');
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
  // section says so without naming a standard the pool does not set. It is the
  // exception, so it is behind a row of its own.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-auto-minimum');
  await addLabel(page, 'auto-minimum');
  await openSection(page, 'size');
  await page.getByRole('radio', { name: 'One share of each host' }).check();
  await openMore(page, 'Smallest runner this pool will accept');

  const minimum = page.getByRole('textbox', { name: 'Minimum memory' });
  await minimum.fill('2g');
  await minimum.press('Enter');
  await expect(minimum).toHaveValue('2 GB');
  await expect(page.getByText(/whole slot's share/)).toContainText('never less than');
  await expect(page.getByText(/whole slot's share/)).toContainText('2 GB');

  // With both minimums the sentence joins them with "and". The word sat alone
  // between two conditional blocks, which trim the whitespace at either end, so
  // it read "1 coreand2 GB"; the check is on the whole phrase because every
  // part of it was present all along.
  const minimumCpu = page.getByRole('textbox', { name: 'Minimum CPU' });
  await minimumCpu.fill('1');
  await minimumCpu.press('Enter');
  await expect(page.getByText(/whole slot's share/)).toContainText(
    'never less than 1 core and 2 GB.',
  );
  // And the row says it shut.
  await expect(section(page, 'size')).toContainText('never below 1 core and 2 GB');
});

test('a container pool can keep its tool cache with its cache', async ({ page }) => {
  // The tool cache is a setting of the pool, offered beside the cache it is
  // shared with, rather than an environment variable to remember.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-tool-cache');
  await addLabel(page, 'tool-cache');
  await openSection(page, 'speed');

  const tools = page.getByRole('checkbox', { name: 'Keep a tool cache as well' });
  await expect(tools).toHaveCount(0);
  await page.getByRole('checkbox', { name: 'Keep a cache between runners' }).check();
  await expect(tools).toBeVisible();
  await tools.check();
  await expect(tools).toBeChecked();
  await expect(section(page, 'speed')).toContainText('shared cache with tools');
});

test('a container pool can keep its work folder in memory, and is told what that costs', async ({
  page,
}) => {
  // Scratch space in memory is opt-in because a tmpfs is charged to the runner's
  // memory limit. The editor says so where the choice is made, with the limit
  // that leaves the job the room it has now, and offers to set it.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-tmpfs');
  await addLabel(page, 'tmpfs');
  await openSection(page, 'size');
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();
  const memory = page.getByRole('textbox', { name: 'Memory per runner', exact: true });
  await memory.fill('6g');
  await memory.press('Enter');

  await openSection(page, 'speed');
  const work = page.getByRole('checkbox', { name: 'Keep the work folder in memory' });
  const tmp = page.getByRole('checkbox', { name: 'Keep /tmp in memory as well' });
  // Off by default, both of them: nothing changes for a pool until somebody asks.
  await expect(work).not.toBeChecked();
  await expect(tmp).not.toBeChecked();
  await expect(page.getByText('Raise the memory limit to')).toHaveCount(0);
  // And there is nothing to place until one is on.
  await expect(page.getByRole('radio', { name: 'Always in memory' })).toHaveCount(0);

  // A pool with no Docker-in-Docker sidecar has no image store to keep in memory.
  await expect(
    page.getByRole('checkbox', { name: 'Keep the Docker image store in memory' }),
  ).toHaveCount(0);

  // The work folder is the one the editor offers; /tmp is its own choice.
  await work.check();
  await expect(tmp).not.toBeChecked();
  await expect(page.getByRole('textbox', { name: 'Work folder size (MB)' })).toBeVisible();
  await expect(page.getByRole('textbox', { name: '/tmp size (MB)' })).toHaveCount(0);

  // Auto is the default and keeps a folder on disk where a runner is too small, so
  // there is nothing to propose; the proposal is for a pool that insists.
  await expect(page.getByText('Raise the memory limit to')).toHaveCount(0);
  await page.getByRole('radio', { name: 'Always in memory' }).check();

  // 6 GB for the job and 4 GB for the folder: the folder takes most of it, so
  // the editor proposes 10 GB and the button applies it. Accepting ends the
  // proposal -- it must not follow the limit upward. The limit is in the size
  // section, which is still open above, so the figure is seen to change.
  await expect(page.getByText('Raise the memory limit to 10 GB')).toBeVisible();
  await page.getByRole('button', { name: 'Set the limit to 10 GB' }).click();
  await expect(memory).toHaveValue('10 GB');
  await expect(page.getByText('Raise the memory limit to')).toHaveCount(0);

  // A size below the floor is refused where it is typed.
  const size = page.getByRole('textbox', { name: 'Work folder size (MB)' });
  await size.fill('8');
  await size.blur();
  await expect(page.getByRole('alert').filter({ hasText: /at least 64/ })).toBeVisible();
});

test('a Docker-in-Docker pool can keep the sidecar image store in memory, and only such a pool is offered it', async ({
  page,
}) => {
  // The image store is the sidecar's, a second container with a memory limit of
  // its own, so a pool with no sidecar is not offered it; and it is a choice of
  // its own because an image bigger than the store does not pull.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-tmpfs-dind');
  await addLabel(page, 'tmpfs-dind');
  await page.getByRole('radio', { name: /Yes, give each runner a Docker daemon/ }).check();
  await openSection(page, 'size');
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();
  const memory = page.getByRole('textbox', { name: 'Memory per runner', exact: true });
  await memory.fill('6g');
  await memory.press('Enter');

  await openSection(page, 'speed');
  const store = page.getByRole('checkbox', { name: 'Keep the Docker image store in memory' });
  await expect(store).not.toBeChecked();
  await expect(page.getByRole('textbox', { name: 'Image store size (MB)' })).toHaveCount(0);
  await store.check();
  await expect(page.getByRole('textbox', { name: 'Image store size (MB)' })).toBeVisible();
  await page.getByRole('radio', { name: 'Always in memory' }).check();

  // A 6 GB limit and the 8 GB default store: the proposal is at least twice the
  // store, 16 GB, and taking it ends the proposal rather than moving it.
  await expect(page.getByText('Raise the memory limit to 16 GB')).toBeVisible();
  await page.getByRole('button', { name: 'Set the limit to 16 GB' }).click();
  await expect(memory).toHaveValue('16 GB');
  await expect(page.getByText('Raise the memory limit to')).toHaveCount(0);

  // A size below the floor is refused where it is typed.
  const size = page.getByRole('textbox', { name: 'Image store size (MB)' });
  await size.fill('8');
  await size.blur();
  await expect(page.getByRole('alert').filter({ hasText: /at least 64/ })).toBeVisible();
});

test('in-memory folders are Auto by default, and Auto says it keeps a folder on disk where a runner is too small', async ({
  page,
}) => {
  // Auto is the recommended answer: it is what a pool that turns a folder on
  // starts with, and it removes the proposal to raise the limit, because a folder
  // that does not fit is simply not in memory.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-tmpfs-auto');
  await addLabel(page, 'tmpfs-auto');
  await openSection(page, 'size');
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();
  const memory = page.getByRole('textbox', { name: 'Memory per runner', exact: true });
  await memory.fill('6g');
  await memory.press('Enter');

  await openSection(page, 'speed');
  await page.getByRole('checkbox', { name: 'Keep the work folder in memory' }).check();
  await page.getByRole('checkbox', { name: 'Keep /tmp in memory as well' }).check();
  await expect(page.getByRole('radio', { name: /^Auto/ })).toBeChecked();
  // And Auto says what it comes to on the fleet's hosts, per host, so the answer
  // to "where did my folders go" is on the page that asked for them.
  const plan = page.getByTestId('tmpfs-plan');
  await expect(plan).toBeVisible();
  await expect(plan).toContainText(/Auto puts/);
  // No "raise the limit" callout under Auto, where the same folders under Always
  // would raise one.
  await expect(page.getByText('Raise the memory limit to')).toHaveCount(0);
  await page.getByRole('radio', { name: 'Always in memory' }).check();
  await expect(page.getByText('Raise the memory limit to')).toBeVisible();
  await expect(section(page, 'speed')).toContainText('work folder, /tmp in memory');
});

test('a Docker-in-Docker pool sized by its host can give the sidecar a larger share of the slot', async ({
  page,
}) => {
  // The build runs in the sidecar, so an even split can starve the container
  // doing the work. The share is offered only where it means something: a host
  // share to divide, and a daemon to give it to.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-daemon-share');
  await addLabel(page, 'daemon-share');
  await page.getByRole('radio', { name: /Yes, give each runner a Docker daemon/ }).check();
  await openSection(page, 'size');

  const share = page.getByRole('textbox', { name: "Docker sidecar's share (%)" });
  await expect(share).toBeVisible();
  await share.fill('95');
  await share.blur();
  await expect(page.getByRole('alert').filter({ hasText: /between 10 and 90/ })).toBeVisible();
  // The row that holds it counts what is wrong, shut or open.
  await expect(section(page, 'size')).toContainText('1 to fix');
  await share.fill('70');
  await share.blur();
  await expect(page.getByRole('alert').filter({ hasText: /between 10 and 90/ })).toHaveCount(0);

  // A fixed size gives both containers the whole figure, so there is no share.
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();
  await expect(share).toHaveCount(0);
});

test('a fixed size can carry a minimum for hosts a little short of it', async ({ page }) => {
  // A standard a host cannot quite meet used to leave the job queued. The
  // minimum is the size the pool will still accept, and the section says what
  // happens with it in the pool's own figures.
  await goto(page, '/pools/new', 'Create a pool');
  await nameField(page).fill('e2e-minimum');
  await addLabel(page, 'minimum');
  await openSection(page, 'size');
  await page.getByRole('radio', { name: 'A fixed size on every host' }).check();

  const memory = page.getByRole('textbox', { name: 'Memory per runner', exact: true });
  await memory.fill('8g');
  await memory.press('Enter');
  await openMore(page, 'Smallest runner this pool will accept');
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
 * Ten identical "Open this run on GitHub" links tell somebody tabbing through
 * the list nothing about which job each one opens, or that it leaves the page.
 */
test("a pool page's recent jobs each link to GitHub under their own name", async ({ page }) => {
  const pools = (await page.request.get('/api/v1/pools').then((r) => r.json())) as {
    items: { id: string; name: string }[];
  };
  const pool = pools.items.find((p) => p.name === FIXTURE.linuxPool)!;
  await goto(page, `/pools/${pool.id}`, FIXTURE.linuxPool);

  const links = page.getByRole('link', { name: /^Open .+ on GitHub, in a new tab/ });
  await expect(links.first()).toBeVisible();
  const names = await links.evaluateAll((els) => els.map((el) => el.getAttribute('aria-label')));
  expect(names.length).toBeGreaterThan(1);
  expect(new Set(names).size, 'each link names its own job').toBeGreaterThan(1);
  await expect(page.getByRole('link', { name: 'Open this run on GitHub' })).toHaveCount(0);
});

/*
 * The last mile ends in a job. A pool's page used to end in one line of YAML,
 * which is not a file anybody can run: it has no trigger, so GitHub refuses it,
 * and it assumes a repository with something worth building in it. The page now
 * also hands over a whole workflow that does nothing to anyone's code, with the
 * label of the pool the page is about already in it.
 */
test("a pool's page hands over a whole test workflow that asks for that pool", async ({ page }) => {
  const pools = (await page.request.get('/api/v1/pools').then((r) => r.json())) as {
    items: { id: string; name: string }[];
  };
  const pool = pools.items.find((p) => p.name === FIXTURE.linuxPool)!;
  await goto(page, `/pools/${pool.id}`, FIXTURE.linuxPool);

  // A pool's page is visited for other reasons, so the card is shut until asked.
  const workflow = page.getByRole('group', { name: 'The test workflow' });
  await expect(workflow).toBeHidden();
  await page.getByText('Or run a test job first').click();

  await expect(workflow).toContainText('name: Zoomies test job');
  await expect(workflow).toContainText('on: workflow_dispatch');
  // The same value the snippet above it prints, because both come from the one rule.
  const preview = page.getByRole('figure').filter({ hasText: 'What a workflow writes' });
  const line = (await preview.textContent())?.match(/runs-on: \S+/)?.[0];
  expect(line, 'the pool page prints a runs-on line').toBeTruthy();
  await expect(workflow).toContainText(line!);
  await expect(page.getByRole('button', { name: 'Copy the test workflow' })).toBeVisible();
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

/**
 * The prewarm toast states how many hosts matched, and a fleet with a single
 * host is the common case: "1 matching host(s)" is a placeholder that reached
 * the screen.
 */
test('prewarming an image says how many hosts matched in words, singular or plural', async ({
  page,
}) => {
  const pools = (await page.request.get('/api/v1/pools').then((r) => r.json())) as {
    items: { id: string; name: string }[];
  };
  const pool = pools.items.find((p) => p.name === FIXTURE.linuxPool);
  expect(pool).toBeDefined();
  const prewarm = `**/api/v1/pools/${pool!.id}/prewarm`;
  for (const [queued, sentence] of [
    [1, '1 matching host.'],
    [3, '3 matching hosts.'],
  ] as const) {
    await page.route(prewarm, (route) => route.fulfill({ json: { queued } }));
    await goto(page, `/pools/${pool!.id}`, FIXTURE.linuxPool);
    await page.getByRole('button', { name: 'Prewarm image' }).click();
    const toast = page.locator('.toast[data-tone="success"]', { hasText: 'Image prewarm queued' });
    await expect(toast).toContainText(sentence);
    await expect(toast).not.toContainText('(s)');
    await page.unroute(prewarm);
  }
});
