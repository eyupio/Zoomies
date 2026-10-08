/**
 * Renting machines, from the pages that do it.
 *
 * A provider is the only thing in Zoomies that spends money, and every page
 * here is shaped by that. The card leads with what is being held back rather
 * than with what the thing is; the wizard asks the controller for a verdict
 * before anything is created; the pause switch stops the spending without
 * stranding a VM; and the one destructive act on the orphan review is guarded
 * by the name of the row it forgets.
 *
 * All of it runs against the real binary and the seeded demo provider, so what
 * is proved is the controller's own sentences reaching the page -- a refusal
 * the browser invented would pass a weaker test and still be the bug.
 */
import { expect, test, type Page } from '@playwright/test';
import {
  browserOverride,
  expectNoReload,
  FIXTURE,
  goto,
  grid,
  pageHeading,
  plantMarker,
  reload,
  waitForRows,
} from './support/fixtures';

test.use(browserOverride);

/** The seeded provider's card, by the name its heading gives it. */
function providerCard(page: Page) {
  return page.getByRole('article', { name: FIXTURE.provider, exact: true });
}

/** The wizard's forward button, which is "Add provider" on the last step. */
const next = (page: Page) => page.getByRole('button', { name: 'Next' });

/**
 * Fill in the first step with answers this test is not about.
 *
 * The kind is already chosen: this build ships one driver, and a form that
 * asks a question with one answer is a form with an extra click in it.
 */
async function connectStep(page: Page, name: string): Promise<void> {
  await expect(page.getByLabel('Kind')).toHaveValue('proxmox');
  const manual = page.getByRole('button', { name: 'Configure connection manually' });
  if (await manual.isVisible()) await manual.click();
  await page.getByLabel('Name').fill(name);
  // By role: the connection choice below it describes itself with the word
  // too, and a radio is not where a URL goes.
  await page.getByRole('textbox', { name: /^Address/ }).fill('https://pve.e2e.example:8006');
  await page.getByLabel('Credential').fill('zoomies@pve!e2e=not-a-token');
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Placement' })).toBeVisible();
}

test('the provider card says what it rents, how much of its ceiling is spent and when it was last checked', async ({
  page,
}) => {
  await goto(page, '/providers', 'Providers');
  const card = providerCard(page);

  // What it is: the driver, in a neutral badge rather than a status hue,
  // beside the address this controller reaches it on.
  await expect(card).toContainText('proxmox');
  await expect(card).toContainText('https://pve.acme.example:8006');
  // And what one machine bought here is, which is the figure a ceiling of
  // four has to be read against.
  await expect(card).toContainText('8 vCPU · 16 GB · 4 runner slots');

  // Two of four, and the two states those machines are in.
  await expect(card).toContainText('2');
  await expect(card).toContainText('of 4 machines');
  await expect(card).toContainText('ready 1');
  await expect(card).toContainText('bootstrapping 1');

  // Nothing is holding it back, and the preflight has run: "Never checked"
  // is the sentence a provider nothing has looked at shows instead.
  await expect(card).toContainText('Last checked');
  await expect(card).not.toContainText('Never checked');
  await expect(card.getByText('Paused', { exact: true })).toHaveCount(0);

  // The fleet's totals agree with the one card that makes them up.
  await expect(page.getByText('Machines owned').locator('..')).toContainText('2');
  await expect(page.getByText('Needing review').locator('..')).toContainText('0');

  // And both machines are listed under it, each one a link to its own page.
  await expect(page.getByRole('link', { name: new RegExp(FIXTURE.readyMachine) })).toBeVisible();
  await expect(page.getByRole('link', { name: new RegExp(FIXTURE.buildingMachine) })).toBeVisible();
});

/**
 * A preflight that found something is the card's most important state and the
 * one the demo fleet cannot reach: its provider is an address that answers
 * nothing, and dialling it from a test would make this suite wait on a DNS
 * timeout. So the failure is delivered in the response instead. What is under
 * test is that the controller's own sentence reaches the card intact -- the
 * page must not summarise a preflight into "something went wrong".
 */
test('a provider whose last check failed says on the card what the check found', async ({
  page,
}) => {
  const complaint =
    'the token cannot see node pve-3: grant it VM.Allocate on /nodes/pve-3, or drop that node from the settings.';
  await page.route(/\/api\/v1\/providers(?:\?.*)?$/, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.items = body.items.map((provider: { name: string }) =>
      provider.name === FIXTURE.provider ? { ...provider, last_check_error: complaint } : provider,
    );
    await route.fulfill({ json: body });
  });

  await goto(page, '/providers', 'Providers');
  await expect(providerCard(page)).toContainText(complaint);
});

test("the wizard is rendered from the driver's schema, with the driver's own defaults filled in", async ({
  page,
}) => {
  await goto(page, '/providers/new', 'Add a provider');
  // The step list by class, for the reason the pool wizard's test gives: it is
  // an ordered list in main and so is the breadcrumb above it.
  await page.getByRole('button', { name: 'Configure connection manually' }).click();
  const steps = page.locator('ol.steps');
  for (const step of ['Connect', 'Placement', 'Machine', 'Limits', 'Review']) {
    await expect(steps).toContainText(step);
  }

  await connectStep(page, 'e2e-provider');

  // Every required setting the driver publishes is on the step. None of these
  // words is written in the form: they are the driver's, served by
  // GET /providers/kinds, which is what stops a setting a driver gained from
  // being missing from the page that configures it.
  for (const label of ['Nodes', 'Template VMID', 'Storage', 'Network bridge']) {
    await expect(page.getByLabel(label)).toBeVisible();
  }
  // Including what choosing one costs, in the driver's own sentence.
  await expect(
    page.getByText('Machines are spread across these, fewest guests first.'),
  ).toBeVisible();

  // The answers the driver has a default for come filled in, so the form is a
  // wizard rather than an interrogation.
  await expect(page.getByLabel('Network bridge')).toHaveValue('vmbr0');
  await expect(page.getByLabel('Lowest VMID')).toHaveValue('9000');
  await expect(page.getByLabel('Highest VMID')).toHaveValue('9099');

  // The seven a homelab never touches are folded away, counted so an operator
  // knows what is behind the disclosure before opening it.
  // Reached by its own summary: a <details> carries no role for a test to ask
  // for, and the words on it are the whole of what an operator sees closed.
  const advanced = page.getByText('Advanced settings (7)');
  await expect(advanced).toBeVisible();
  await expect(page.getByLabel('Resource pool')).toBeHidden();
  await advanced.click();
  await expect(page.getByLabel('Resource pool')).toBeVisible();
  await expect(page.getByLabel('Full clone')).toBeVisible();
});

/**
 * The two answers that are wrong for reasons no browser could know.
 *
 * A name already taken is a fact about the fleet's other rows, and a VMID range
 * that runs backwards is the driver's own arithmetic. Both are refused by the
 * controller, on the review step, in the words it would use in a log line --
 * and the same refusal comes back from the create itself, which is what proves
 * the verdict is guidance rather than the gate.
 */
test('an answer only the controller can judge is refused by the controller, not invented in the browser', async ({
  page,
}) => {
  await goto(page, '/providers/new', 'Add a provider');
  await connectStep(page, FIXTURE.provider);

  await page.getByLabel('Nodes').fill('pve-1');
  await page.getByLabel('Template VMID').fill('8000');
  await page.getByLabel('Storage').fill('local-zfs');
  await page.getByLabel('Lowest VMID').fill('9999');

  // The browser lets both through: neither is empty and both are numbers,
  // which is the whole of what a form can honestly decide by itself.
  await expect(next(page)).toBeEnabled();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Machine' })).toBeVisible();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Limits' })).toBeVisible();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();
  await expect(page.getByText('Step 5 of 5')).toBeVisible();

  const verdict = page.getByRole('region', { name: 'What the controller makes of it' });
  await expect(verdict).toContainText('2 answers still need fixing');
  await expect(verdict).toContainText(`a provider called ${FIXTURE.provider} already exists`);
  await expect(verdict).toContainText('the VMID range 9999-9099 runs backwards');
  // Named in the words the form uses for the box, not as `name`.
  await expect(verdict.getByRole('listitem').first()).toContainText('Name');

  // And pressing on anyway is refused by the same rules, which puts the
  // operator back on the step carrying the offending answer rather than
  // leaving them on a review page with a toast.
  await page.getByRole('button', { name: 'Add provider' }).click();
  await expect(page.getByText('Step 1 of 5')).toBeVisible();
  await expect(
    page.getByText(`a provider called ${FIXTURE.provider} already exists`),
  ).toBeVisible();

  // Nothing was created: the fleet still has the one provider it was seeded
  // with, under the name this draft tried to take.
  const listed = await page.request.get('/api/v1/providers');
  const body = (await listed.json()) as { items: { name: string }[] };
  expect(body.items.filter((p) => p.name === FIXTURE.provider)).toHaveLength(1);
});

/**
 * A provider's name is the operator's own string, and it is rendered back to
 * them on the review step and in the toast. Svelte escapes interpolated text,
 * so the point of this is not to discover that it does -- it is to fail if it
 * ever stops.
 */
test('a provider named with markup is summarised as text, not run as it', async ({ page }) => {
  const dialogs: string[] = [];
  page.on('dialog', (dialog) => {
    dialogs.push(dialog.message());
    void dialog.dismiss();
  });

  const name = 'zoomies-<img src=x onerror=alert(1)><script>alert(2)</script>';
  await goto(page, '/providers/new', 'Add a provider');
  await connectStep(page, name);
  await page.getByLabel('Nodes').fill('pve-1');
  await page.getByLabel('Template VMID').fill('8000');
  await page.getByLabel('Storage').fill('local-zfs');
  await next(page).click();
  await next(page).click();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();

  // The payload really reached the page. An assertion about what did not
  // happen proves nothing if the string never arrived.
  await expect(page.getByText(name, { exact: true })).toBeVisible();
  await expect(page.locator('img[src="x"]')).toHaveCount(0);
  await expect(page.locator('script:has-text("alert(2)")')).toHaveCount(0);
  expect(dialogs, 'a dialog opened, so something in the name ran').toHaveLength(0);
});

/**
 * The kill switch: one press, and it is still pressed after a reload.
 *
 * It is a column on the provider row rather than a flag in memory, which is the
 * difference between a switch an operator can walk away from and one a restart
 * undoes. It is also deliberately narrow, and the page has to say so: an
 * operator reaching for this during an incident is asking "does this strand the
 * machines I already have?", and the answer is no.
 */
test('the pause switch survives a reload, and says what being paused does not stop', async ({
  page,
}) => {
  await goto(page, '/providers', 'Providers');
  const pause = page.getByRole('switch', { name: 'Pause new machines' });
  await expect(pause).toHaveAttribute('aria-checked', 'false');

  await plantMarker(page);
  await pause.click();
  await expect(pause).toHaveAttribute('aria-checked', 'true');

  try {
    // What it stopped, and what it did not. Both halves, because either one
    // alone is the sentence that gets somebody to press it at the wrong time.
    await expect(page.getByText('Nothing new is being bought.')).toBeVisible();
    await expect(
      page.getByText('Drains, deletes and machines already on their way carry on.').first(),
    ).toBeVisible();

    // The card agrees, in the controller's own words rather than the page's:
    // `held` answers for the fence, the configuration and the row at once.
    const card = providerCard(page);
    await expect(card.getByText('Paused', { exact: true })).toBeVisible();
    await expect(card).toContainText(`${FIXTURE.provider} is paused`);
    await expect(page.getByText('Paused providers').locator('..')).toContainText('1');
    await expectNoReload(page);

    // And it is on the row, not in this tab.
    await reload(page, 'Providers');
    await expect(page.getByRole('switch', { name: 'Pause new machines' })).toHaveAttribute(
      'aria-checked',
      'true',
    );
  } finally {
    // Left as it was found: every other spec in this suite reads the same
    // fleet, and a provider left paused is a fixture that quietly differs.
    const restore = page.getByRole('switch', { name: 'Pause new machines' });
    await restore.click();
    await expect(restore).toHaveAttribute('aria-checked', 'false');
  }
});

test('the orphan review is three tables, and says so when a fleet has nothing out of place', async ({
  page,
}) => {
  await goto(page, `/providers/${FIXTURE.providerId}?tab=orphans`, FIXTURE.provider);

  // The three ways a row and a resource can disagree, each with its own
  // heading: a resource nobody owns, a row that owns nothing, and a machine
  // nothing has confirmed is ours.
  for (const heading of [
    'Resources with no row',
    'Machines holding nothing',
    'Ownership unverified',
  ]) {
    await expect(page.getByRole('heading', { name: heading })).toBeVisible();
  }

  // The seeded fleet's two machines both hold a resource and were both
  // confirmed ours, so all three are empty -- and each says which question it
  // answered rather than drawing an empty table.
  await expect(page.getByText('Nothing untracked')).toBeVisible();
  await expect(page.getByText('Every machine here holds something.')).toBeVisible();
  await expect(page.getByText('Nothing in doubt')).toBeVisible();
  await expect(page.getByText('Last swept')).toBeVisible();
});

/**
 * Forgetting a row is the only destructive act on the review, and it is the
 * one that destroys nothing: the machine it forgets holds no resource. It still
 * asks for the name to be typed, because a fleet that forgets the wrong row is
 * a fleet paying for a VM nothing will ever delete.
 *
 * The three disagreements are delivered in the response. The demo fleet is
 * deliberately a fleet with nothing wrong with it, and the sweep that would
 * find a real orphan is a call to a hypervisor that is not there.
 */
test('forgetting a machine needs its name typed, and nothing is forgotten without it', async ({
  page,
}) => {
  const ghost = 'zoomies-mach-ghost02';
  await page.route(/\/api\/v1\/providers\/[^/]+\/orphans$/, async (route) => {
    await route.fulfill({
      json: {
        provider_id: FIXTURE.providerId,
        provider_name: FIXTURE.provider,
        last_sweep_at: new Date().toISOString(),
        untracked: [
          {
            name: 'zoomies-mach-ghost01',
            note: 'this resource wears the naming Zoomies gives its machines but no row accounts for it.',
          },
        ],
        no_resource: [
          {
            id: 'mach_ghost02',
            provider_id: FIXTURE.providerId,
            name: ghost,
            state: 'failed',
            message: 'the clone was refused, so this row never got as far as a resource',
          },
        ],
        unverified: [
          {
            id: 'mach_ghost03',
            provider_id: FIXTURE.providerId,
            name: 'zoomies-mach-ghost03',
            state: 'quarantined',
            resource_zone: 'pve-2',
            resource_id: '9042',
            ownership_error: 'the guest at 9042 carries another controller’s mark',
          },
        ],
      },
    });
  });

  const released: string[] = [];
  await page.route(/\/api\/v1\/machines\/[^/]+\/release$/, async (route) => {
    released.push(route.request().url());
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
  });

  await goto(page, `/providers/${FIXTURE.providerId}?tab=orphans`, FIXTURE.provider);

  await waitForRows(grid(page, 'Untracked resources'));
  await waitForRows(grid(page, 'Machines with no resource'));
  await waitForRows(grid(page, 'Machines whose ownership is unverified'));
  await expect(grid(page, 'Untracked resources')).toContainText('zoomies-mach-ghost01');
  await expect(grid(page, 'Machines whose ownership is unverified')).toContainText(
    'another controller',
  );

  // One button on the page, and it belongs to the list where forgetting a row
  // is guaranteed to destroy nothing.
  await expect(page.getByRole('button', { name: 'Forget' })).toHaveCount(1);
  // Forgetting is permanent, so the button says which machine it is about
  // before the dialog does: a list of them is otherwise a row of "Forget".
  await expect(page.getByRole('button', { name: `Forget ${ghost}`, exact: true })).toHaveCount(1);
  await grid(page, 'Machines with no resource').getByRole('button', { name: 'Forget' }).click();

  const dialog = page.getByRole('dialog', { name: 'Forget this machine' });
  await expect(dialog).toContainText('Nothing at the provider is touched.');
  const confirm = dialog.getByRole('button', { name: 'Forget it' });
  await expect(confirm).toBeDisabled();

  // A near miss is still a miss: the whole point of typing the name is that a
  // hand already on the button cannot complete this.
  const typed = dialog.getByRole('textbox', { name: `Type ${ghost} to confirm` });
  await typed.fill('zoomies-mach-ghost');
  await expect(confirm).toBeDisabled();
  await confirm.click({ force: true });
  expect(released, 'nothing was forgotten without the name').toHaveLength(0);
  await expect(dialog).toBeVisible();

  await typed.fill(ghost);
  await expect(confirm).toBeEnabled();
  await confirm.click();
  await expect.poll(() => released).toHaveLength(1);
  expect(released[0]).toContain('/machines/mach_ghost02/release');
});

test('a machine says where it is and how long each phase of getting there took', async ({
  page,
}) => {
  await goto(page, `/machines/${FIXTURE.readyMachineId}`, FIXTURE.readyMachine);

  const where = page.getByRole('region', { name: 'Where it is' });
  await expect(where).toContainText(FIXTURE.provider);
  // Zone and identifier together: on Proxmox that is the node and the VMID,
  // which is what somebody types into the cluster's own search.
  await expect(where).toContainText('pve-1 / 9000');
  await expect(where.getByRole('link', { name: FIXTURE.machineHost })).toBeVisible();
  await expect(where.getByRole('link', { name: FIXTURE.linuxPool })).toBeVisible();
  await expect(where).toContainText('Confirmed ours');

  // One row per phase it actually reached, in order, each in the vocabulary an
  // operator reads rather than the column name behind it.
  const timeline = page.getByRole('region', { name: 'How it got here' });
  const phases = timeline.locator('.phase');
  await expect(phases).toHaveText([
    'Planned',
    'Creating the machine',
    'Machine created',
    'Powering on',
    'Agent installed',
    'Enrolled with the controller',
    'Ready for work',
  ]);

  // Each row's figure is the gap to the next mark, which is the only way to
  // see which half of a slow build was slow: this clone took thirty-six
  // seconds and the agent install took thirty.
  await expect(timeline.locator('li', { hasText: 'Creating the machine' })).toContainText('36s');
  await expect(timeline.locator('li', { hasText: 'Powering on' })).toContainText('30s');
  // The last row is the one being lived through, so it counts rather than
  // reporting a duration that has not finished.
  await expect(timeline.locator('li').last()).toContainText('so far');
  // And a phase this machine never reached is absent rather than drawn as zero.
  await expect(timeline).not.toContainText('Draining');
});

/**
 * The handle an operator pastes into the provider's own task log.
 *
 * Every step this controller takes leaves a mark at the provider, and an
 * operator who cannot join the two has to guess which of the two systems is
 * slow. The seeded machines are not mid-operation -- nothing is calling a
 * hypervisor that is not there -- so the operation is delivered in the
 * response, which is the shape the machine loop writes while a clone runs.
 */
test("a machine in flight hands over the id that finds it in the provider's task log", async ({
  page,
}) => {
  const upid = 'UPID:pve-2:0000A1B2:04C3D2E1:6899FFFF:qmclone:9001:zoomies@pve!demo:';
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: async (value: string) => sessionStorage.setItem('copied-operation', value),
      },
    });
  });
  await page.route(new RegExp(`/api/v1/machines/${FIXTURE.buildingMachineId}$`), async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({
      json: {
        ...body,
        operation: 'create',
        operation_id: 'op_e2ecreate01',
        operation_handle: upid,
        operation_since: new Date(Date.now() - 90_000).toISOString(),
      },
    });
  });

  await goto(page, `/machines/${FIXTURE.buildingMachineId}`, FIXTURE.buildingMachine);
  const panel = page.getByRole('region', { name: 'What it is doing' });
  await expect(panel).toContainText('create');
  // How long it has been going, which is the number that decides whether this
  // is slow or stuck.
  await expect(panel).toContainText('1m 30s');
  await expect(panel).toContainText('op_e2ecreate01');
  await expect(panel).toContainText(upid);

  await panel.getByRole('button', { name: "Copy the provider's own handle" }).click();
  await expect
    .poll(() => page.evaluate(() => sessionStorage.getItem('copied-operation')))
    .toBe(upid);
});

test('the Hosts page shows the machines on their way, and marks the host a provider built', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');

  // The band answers the question an operator has on this page when there are
  // too few hosts: is anything on its way?
  const band = page.getByRole('region', { name: 'Machines' });
  await expect(band).toContainText('2 machines rented from 1 provider');
  await expect(band.getByRole('link', { name: 'Ready machines' })).toContainText('1');
  await expect(band.getByRole('link', { name: 'Bootstrapping machines' })).toContainText('1');
  await expect(band.getByRole('link', { name: 'Planned machines' })).toContainText('0');
  // The lifecycle is a flow, in the order the store enforces, and each step is
  // a link into the Providers page filtered to it.
  await expect(band.getByRole('link', { name: 'Ready machines' })).toHaveAttribute(
    'href',
    '/providers?state=ready',
  );

  // The host that machine became says which provider built it and which
  // resource it is, so an operator never deletes the host and leaves the VM
  // running on the bill.
  const rented = page.getByRole('article', { name: FIXTURE.machineHost, exact: true });
  await expect(rented).toContainText('proxmox · pve-1 · 9000');
  // And a host nobody rented carries no such badge.
  await expect(
    page.getByRole('article', { name: 'demo-builder-1', exact: true }),
  ).not.toContainText('proxmox');
});

test('the Providers page leads to the wizard, and a machine leads back to its provider', async ({
  page,
}) => {
  await goto(page, '/providers', 'Providers');
  await page.getByRole('link', { name: 'Add a provider' }).first().click();
  await expect(pageHeading(page, 'Add a provider')).toBeVisible();

  await goto(page, `/machines/${FIXTURE.readyMachineId}`, FIXTURE.readyMachine);
  await page.getByRole('link', { name: FIXTURE.provider }).first().click();
  await expect(pageHeading(page, FIXTURE.provider)).toBeVisible();
});

test('a private connection is offered only where the controller can make one, and asks for the gateway address', async ({
  page,
}) => {
  // The e2e binary runs with authentication off, where private connections
  // are unavailable: the choice is there, disabled, with the reason beside it.
  await goto(page, '/providers/new', 'Add a provider');
  await page.getByRole('button', { name: 'Configure connection manually' }).click();
  await expect(page.getByRole('radio', { name: /Private connection/ })).toBeDisabled();
  await expect(page.getByRole('radio', { name: 'Direct' })).toBeChecked();
  await expect(page.getByText(/Private connections need authentication/)).toBeVisible();

  await page.route('**/api/v1/meta', async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      json: { ...(await response.json()), tailcat_available: true },
    });
  });
  await goto(page, '/providers/new', 'Add a provider');
  await page.getByRole('button', { name: 'Configure connection manually' }).click();
  await page.getByRole('textbox', { name: /^Address/ }).fill('https://pve.e2e.example:8006');
  await expect(page.getByLabel('Private connection address')).toHaveCount(0);
  await page.getByRole('radio', { name: /Private connection/ }).check();
  const address = page.getByLabel('Private connection address');
  await expect(address).toBeVisible();
  await expect(address).toHaveAttribute('type', 'password');
  await address.fill('not an address');
  await address.blur();
  await expect(page.getByText(/Copy the whole address the gateway printed/)).toBeVisible();
  // The endpoint is still what the certificate is checked against, so it
  // stays required and untouched by the choice.
  await expect(page.getByRole('textbox', { name: /^Address/ })).toHaveValue(
    'https://pve.e2e.example:8006',
  );
  // Nothing about the choice is a matter for the browser alone: what leaves
  // the page names the connection, and the address travels only with it.
  await page.getByRole('radio', { name: 'Direct' }).check();
  await expect(page.getByLabel('Private connection address')).toHaveCount(0);
});

/**
 * A ceiling of one is the cautious first value for anything that costs money,
 * so "1 machines" is on the first-run path of every paid provider. The count
 * is the operator's own number: only the noun follows it.
 */
test('a ceiling of one machine is written in the singular on the card and the provider page', async ({
  page,
}) => {
  const one = { max_machines: 1 };
  await page.route(/\/api\/v1\/providers(?:\?.*)?$/, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.items = body.items.map((provider: object) => ({ ...provider, ...one }));
    await route.fulfill({ json: body });
  });
  await page.route(/\/api\/v1\/providers\/[^/?]+$/, async (route) => {
    const response = await route.fetch();
    await route.fulfill({ json: { ...(await response.json()), ...one } });
  });

  await goto(page, '/providers', 'Providers');
  await expect(providerCard(page)).toContainText('of 1 machine');
  await expect(providerCard(page)).not.toContainText('1 machines');

  await goto(page, `/providers/${FIXTURE.providerId}?tab=settings`, FIXTURE.provider);
  await expect(page.getByText(/built at once/)).toContainText('1 machine,');
  await expect(page.getByText(/built at once/)).not.toContainText('1 machines');
});

test('the review step writes a ceiling of one machine in the singular', async ({ page }) => {
  await goto(page, '/providers/new', 'Add a provider');
  await connectStep(page, 'e2e-one-machine');
  await page.getByLabel('Nodes').fill('pve-1');
  await page.getByLabel('Template VMID').fill('8000');
  await page.getByLabel('Storage').fill('local-zfs');
  await next(page).click();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Limits' })).toBeVisible();
  await page.getByLabel('Maximum machines').fill('1');
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();
  const ceiling = page.locator('div', { has: page.locator('dt', { hasText: 'Ceiling' }) }).last();
  await expect(ceiling).toContainText('1 machine');
  await expect(ceiling).not.toContainText('1 machines');
});

test('Proxmox onboarding waits for one command and fills the connection without manual credentials', async ({
  page,
}) => {
  let hostConnected = false;
  let discoveryBody: Record<string, unknown> | undefined;
  await page.route('**/api/v1/meta', async (route) => {
    const response = await route.fetch();
    const meta = await response.json();
    await route.fulfill({ response, json: { ...meta, tailcat_available: true } });
  });
  await page.route('**/api/v1/provider-setups', (route) =>
    route.fulfill({
      status: 201,
      json: {
        id: 'pvs_test',
        expires_at: new Date(Date.now() + 3600000).toISOString(),
        ready: false,
        command: 'sudo zoomies providers connect-proxmox --token test-capability',
      },
    }),
  );
  await page.route('**/api/v1/provider-setups/pvs_test', (route) =>
    route.fulfill({
      json: {
        id: 'pvs_test',
        expires_at: new Date(Date.now() + 3600000).toISOString(),
        ready: hostConnected,
        ...(hostConnected ? { name: 'proxmox-pve-1', endpoint: 'https://pve.example:8006' } : {}),
      },
    }),
  );
  await page.route('**/api/v1/providers/discover', (route) => {
    discoveryBody = route.request().postDataJSON() as Record<string, unknown>;
    return route.fulfill({
      json: {
        nodes: [{ value: 'pve-1', label: 'pve-1' }],
        storages: [{ value: 'local-lvm', label: 'local-lvm' }],
        bridges: [{ value: 'vmbr0', label: 'vmbr0' }],
        templates: [{ value: '9000', label: 'Runner template' }],
      },
    });
  });
  await goto(page, '/providers/new', 'Add a provider');
  await expect(page.getByLabel('Kind')).toHaveValue('proxmox');
  for (const field of [
    'Name',
    'Credential',
    'Certificate authority',
    'Private connection address',
  ]) {
    await expect(page.getByLabel(field, { exact: true })).toHaveCount(0);
  }
  await expect(next(page)).toBeDisabled();
  await page.getByRole('button', { name: 'Generate setup command' }).click();
  await expect(page.getByText(/Waiting for your Proxmox host/)).toBeVisible();
  hostConnected = true;
  await expect(page.getByText(/Connected to proxmox-pve-1/)).toBeVisible();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Placement' })).toBeVisible();
  await expect.poll(() => discoveryBody?.setup_id).toBe('pvs_test');
  expect(discoveryBody).not.toHaveProperty('credential');
  expect(discoveryBody).not.toHaveProperty('tailcat_address');
  await expect(next(page)).toBeEnabled();
  await next(page).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Capacity' })).toBeVisible();
  await expect(page.getByText('Advanced machine settings', { exact: true })).toBeVisible();
});

test('Proxmox offers an existing Tailcat gateway without hiding it behind manual setup', async ({
  page,
}) => {
  await page.route('**/api/v1/meta', async (route) => {
    const response = await route.fetch();
    const meta = await response.json();
    await route.fulfill({ response, json: { ...meta, tailcat_available: true } });
  });
  await goto(page, '/providers/new', 'Add a provider');
  await expect(page.getByRole('radio', { name: /^Automatic setup · Tailcat/ })).toBeChecked();
  await page.getByRole('radio', { name: /^Use an existing Tailcat gateway/ }).check();
  await expect(page.getByRole('radio', { name: /^Private connection · Tailcat/ })).toBeChecked();
  await expect(page.getByLabel('Private connection address')).toBeVisible();
  await expect(
    page.getByText('zoomies gateway --target <hypervisor-ip>:8006', { exact: true }),
  ).toBeVisible();
  await page.getByRole('radio', { name: /^Configure a direct connection/ }).check();
  await expect(page.getByRole('radio', { name: /^Direct/ })).toBeChecked();
  await expect(page.getByLabel('Private connection address')).toHaveCount(0);
  await page.getByRole('radio', { name: /^Automatic setup · Tailcat/ }).check();
  await expect(page.getByLabel('Credential', { exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Generate setup command' })).toBeVisible();
});
