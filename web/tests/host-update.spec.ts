/**
 * A host's card: the Update button, and the command that stays beneath it.
 *
 * It runs on the Updates fixture, with the desktop and the phone projects, for
 * the reason that fixture exists: the binary under test is a `dev` build, which
 * no release comparison accepts, and this controller reports 1.3.0. A host that
 * joins here on 1.2.0 is therefore behind a release, and its agent offering
 * `self-update` is what makes the button live.
 *
 * Every host is a real one, joined through the Add a host page's own token and
 * kept alive by posting heartbeats from the spec, as the fake agent does. What
 * the controller answers is the controller's: the states the fixture cannot make
 * by itself (a refusal, an administrator who is not the platform role) are served
 * to the page as the documents the controller would send.
 */
import { expect, test, type Locator, type Page } from '@playwright/test';
import { shapeDiffers } from '../src/lib/state/fleet-shape';
import {
  browserOverride,
  documentWidth,
  expectNoReload,
  goto,
  plantMarker,
  reload,
} from './support/fixtures';
import { enrolAgent, type Credentials } from './support/fake-agent';

test.use(browserOverride);

const TARGET = 'v1.3.0';
const BEHIND = '1.2.0';
const SELF_UPDATE = ['self-update'];

/** A host that joined on an earlier release, and has said what its agent offers. */
async function join(
  page: Page,
  name: string,
  features: string[] = SELF_UPDATE,
): Promise<Credentials> {
  const credentials = await enrolAgent(page, name, BEHIND);
  await beat(page, credentials, { features });
  return credentials;
}

interface Beat {
  version?: string;
  features?: string[];
  update_unsupported?: string;
  update?: { id: string; ok: boolean; tag: string; error?: string; finished_at: string };
}

/** One heartbeat, which carries the version and the features the agent has, as a real one does. */
async function beat(page: Page, credentials: Credentials, over: Beat = {}): Promise<void> {
  const response = await page.request.post('/api/v1/agent/heartbeat', {
    headers: { Authorization: `Bearer ${credentials.agent_token}` },
    data: {
      protocol_version: 1,
      version: over.version ?? BEHIND,
      features: over.features ?? SELF_UPDATE,
      ...(over.update ? { update: over.update } : {}),
      ...(over.update_unsupported ? { update_unsupported: over.update_unsupported } : {}),
    },
  });
  expect(response.ok()).toBeTruthy();
}

type HostDocument = {
  version?: string;
  version_skew?: string;
  update?: { state: string; reason: string; can_update: boolean; attempt_id: string };
};

async function hostOnServer(page: Page, credentials: Credentials): Promise<HostDocument> {
  const response = await page.request.get(`/api/v1/hosts/${credentials.host_id}`);
  expect(response.ok()).toBeTruthy();
  return (await response.json()) as HostDocument;
}

/** Run a spec against a joined host and remove it afterwards, whatever the spec found. */
async function withHost(
  page: Page,
  features: string[],
  body: (host: { credentials: Credentials; name: string }) => Promise<void>,
): Promise<void> {
  const name = `upd-host-${Date.now() % 1e8}-${Math.floor(Math.random() * 1e4)}`;
  const credentials = await join(page, name, features);
  try {
    await body({ credentials, name });
  } finally {
    await page.request.delete(`/api/v1/hosts/${credentials.host_id}?force=true`);
  }
}

const card = (page: Page, name: string) => page.getByRole('article', { name, exact: true });
const row = (page: Page, name: string) =>
  page.getByRole('region', { name: `Agent update for ${name}`, exact: true });
const updateButton = (page: Page, name: string) =>
  page.getByRole('button', { name: `Update ${name} to ${TARGET}`, exact: true });
const tryAgainButton = (page: Page, name: string) =>
  page.getByRole('button', { name: `Try again to update ${name} to ${TARGET}`, exact: true });
const confirmation = (page: Page, name: string) =>
  page.getByRole('dialog', { name: `Update the agent on ${name}`, exact: true });

/** Sign in as a role below the platform one: the server here has authentication off, the page is told otherwise. */
async function signedInAs(page: Page, role: 'viewer' | 'operator' | 'admin'): Promise<void> {
  await page.route('**/api/v1/meta', async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as Record<string, unknown>;
    return route.fulfill({
      response,
      json: { ...body, auth_disabled: false, bootstrap_required: false },
    });
  });
  await page.route('**/api/v1/auth/session', (route) =>
    route.fulfill({ json: { kind: 'token', id: 'tok_pretend', name: 'a token', role } }),
  );
}

/** Count the requests the page sends to update a host. */
function countStarts(page: Page): () => number {
  let posted = 0;
  page.on('request', (request) => {
    if (request.method() === 'POST' && /\/api\/v1\/hosts\/[^/]+\/update$/.test(request.url())) {
      posted += 1;
    }
  });
  return () => posted;
}

async function expectNoSidewaysScroll(page: Page, where: string): Promise<void> {
  const { scrollWidth, clientWidth } = await documentWidth(page);
  expect(
    scrollWidth,
    `${where} is ${scrollWidth}px wide in a ${clientWidth}px window, so the page scrolls sideways`,
  ).toBeLessThanOrEqual(clientWidth);
}

test('a host behind the controller is offered Update, and the command stays beneath it', async ({
  page,
}) => {
  await withHost(page, SELF_UPDATE, async ({ name }) => {
    await goto(page, '/hosts', 'Hosts');
    const mine = card(page, name);
    await expect(mine).toBeVisible();

    // A word for the state, the controller's sentence as text, and a button that
    // names the host and the release.
    await expect(row(page, name)).toBeVisible();
    await expect(row(page, name)).toContainText('Can be updated');
    await expect(row(page, name)).toContainText(
      `This host runs ${BEHIND} and can be updated to ${TARGET}.`,
    );
    await expect(updateButton(page, name)).toBeEnabled();
    await expect(updateButton(page, name)).toHaveText('Update');
    // Behind is a neutral fact, in the card's header.
    await expect(mine.getByText('Behind', { exact: true })).toBeVisible();

    // The copyable command is still there, folded, for everyone who had it.
    const summary = mine.locator('details.upgrade > summary');
    await expect(summary).toHaveText(/Update this agent to 1\.3\.0/);
    await summary.click();
    await expect(mine.locator('details.upgrade pre code')).toContainText(
      'sudo zoomies upgrade --mode agent --version',
    );
    await expect(mine.getByRole('button', { name: 'Copy the upgrade command' })).toBeVisible();
  });
});

test('the Update button is outside the fold and outside its summary', async ({ page }) => {
  await withHost(page, SELF_UPDATE, async ({ name }) => {
    await goto(page, '/hosts', 'Hosts');
    const button = updateButton(page, name);
    await expect(button).toBeVisible();
    // A control inside a summary is also a disclosure toggle.
    expect(await button.evaluate((el) => el.closest('summary, details'))).toBeNull();
    await expect(card(page, name).locator('summary button, summary a')).toHaveCount(0);
    // Folded or open, the button is where it was.
    await card(page, name).locator('details.upgrade > summary').click();
    await expect(button).toBeVisible();
  });
});

test('an operator is given the command and no button, and a viewer neither', async ({ page }) => {
  await withHost(page, SELF_UPDATE, async ({ name }) => {
    const starts = countStarts(page);
    await signedInAs(page, 'operator');
    await goto(page, '/hosts', 'Hosts');
    await expect(card(page, name)).toBeVisible();
    await expect(card(page, name).locator('details.upgrade > summary')).toContainText(
      'Update this agent to 1.3.0',
    );
    await expect(row(page, name)).toHaveCount(0);
    await expect(page.getByRole('button', { name: /^(Update|Try again)/ })).toHaveCount(0);

    await page.unroute('**/api/v1/auth/session');
    await signedInAs(page, 'viewer');
    await reload(page, 'Hosts');
    await expect(card(page, name)).toBeVisible();
    await expect(card(page, name).locator('details.upgrade')).toHaveCount(0);
    await expect(row(page, name)).toHaveCount(0);
    await expect(page.getByRole('button', { name: /^(Update|Try again)/ })).toHaveCount(0);
    expect(starts()).toBe(0);
  });
});

test('a host whose agent does not offer to update itself says why, and the button cannot be pressed', async ({
  page,
}) => {
  await withHost(page, [], async ({ name }) => {
    await goto(page, '/hosts', 'Hosts');
    await expect(row(page, name)).toContainText('Update by command');
    // The reason is text on the card, not a tooltip: a phone has no hover.
    await expect(row(page, name)).toContainText('does not offer to update itself');
    await expect(row(page, name)).toContainText('sudo zoomies updates helper install');
    await expect(updateButton(page, name)).toBeDisabled();
    // And the command beneath it is still the way.
    await expect(card(page, name).locator('details.upgrade > summary')).toBeVisible();
  });
});

test('a host the update helper can never be installed on says why, with no button and no install command', async ({
  page,
}) => {
  await withHost(page, [], async ({ credentials, name }) => {
    await beat(page, credentials, { features: [], update_unsupported: 'no-systemd' });
    await goto(page, '/hosts', 'Hosts');
    await expect(row(page, name)).toContainText('Update by command');
    await expect(row(page, name)).toContainText('a host that systemd does not run');
    await expect(row(page, name)).not.toContainText('helper install');
    await expect(row(page, name).getByRole('button')).toHaveCount(0);
    // The command beneath the card is the way.
    await expect(card(page, name).locator('details.upgrade > summary')).toBeVisible();
    expect((await hostOnServer(page, credentials)).update?.state).toBe('unsupported');
  });
});

test('the confirmation names the host and the release, and cancelling asks for nothing', async ({
  page,
}) => {
  await withHost(page, SELF_UPDATE, async ({ credentials, name }) => {
    const starts = countStarts(page);
    await goto(page, '/hosts', 'Hosts');
    await updateButton(page, name).focus();
    await page.keyboard.press('Enter');

    const dialog = confirmation(page, name);
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText(`Update the agent on ${name} from ${BEHIND} to ${TARGET}?`);
    await expect(dialog).toContainText(`The agent on ${name} restarts.`);
    await expect(dialog).toContainText('Jobs that are running keep running');
    await expect(dialog).toContainText('does not wait for them to finish');
    await expect(dialog).toContainText(`reports back when it runs ${TARGET}`);
    await expect(dialog).toContainText('and not before');
    await expect(dialog.getByRole('button', { name: `Update to ${TARGET}` })).toBeVisible();

    // Reached and left by keyboard: focus moves in, stays in, and comes back.
    const inside = () => dialog.evaluate((el) => el.contains(document.activeElement));
    expect(await inside(), 'focus moves into the dialog').toBe(true);
    for (let i = 0; i < 4; i += 1) {
      await page.keyboard.press('Tab');
      expect(await inside(), `focus stays inside after ${i + 1} Tab presses`).toBe(true);
    }
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(updateButton(page, name)).toBeFocused();

    await updateButton(page, name).click();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
    await expect(updateButton(page, name)).toBeFocused();
    expect(starts(), 'cancelling asks for nothing').toBe(0);
    expect((await hostOnServer(page, credentials)).update?.state, 'no attempt was opened').toBe(
      'none',
    );
  });
});

test('confirming asks the controller, shows the update in flight across a reload, and says nothing final before the host does', async ({
  page,
}) => {
  await withHost(page, SELF_UPDATE, async ({ credentials, name }) => {
    await goto(page, '/hosts', 'Hosts');
    await updateButton(page, name).click();
    const posted = page.waitForRequest(
      (request) =>
        request.method() === 'POST' &&
        new URL(request.url()).pathname === `/api/v1/hosts/${credentials.host_id}/update`,
    );
    await confirmation(page, name)
      .getByRole('button', { name: `Update to ${TARGET}` })
      .click();
    expect((await posted).postData(), 'it sends no body for a field to hide in').toBeNull();

    // In flight: the button is replaced by the state, and focus has moved to it.
    await expect(row(page, name)).toContainText('Updating');
    await expect(row(page, name)).toContainText(`The update to ${TARGET} has been asked for.`);
    await expect(updateButton(page, name)).toHaveCount(0);
    await expect(confirmation(page, name)).toBeHidden();
    await expect(row(page, name)).toBeFocused();
    // Said once, politely, and without claiming it worked.
    const live = row(page, name).getByRole('status');
    await expect(live).toContainText(`${name}: updating.`);
    await expect(row(page, name)).not.toContainText('Updated');

    const server = await hostOnServer(page, credentials);
    expect(server.update?.state).toBe('requested');

    // The state is the controller's, not the page's: a reload shows the same,
    // and heartbeats that still report the old release change nothing.
    await beat(page, credentials);
    await reload(page, 'Hosts');
    await expect(row(page, name)).toContainText('Updating');
    await expect(updateButton(page, name)).toHaveCount(0);
    await page.waitForTimeout(1_000);
    await expect(row(page, name)).not.toContainText('Updated');
    await expect(row(page, name)).not.toContainText(`runs ${TARGET.slice(1)} now`);
  });
});

test('the host reporting the release is the update done, shown without a reload', async ({
  page,
}) => {
  await withHost(page, SELF_UPDATE, async ({ credentials, name }) => {
    await goto(page, '/hosts', 'Hosts');
    await updateButton(page, name).click();
    await confirmation(page, name)
      .getByRole('button', { name: `Update to ${TARGET}` })
      .click();
    await expect(row(page, name)).toContainText('Updating');
    await plantMarker(page);

    await beat(page, credentials, { version: '1.3.0' });
    await expect(row(page, name)).toContainText('Updated');
    await expect(row(page, name)).toContainText(`${name} runs 1.3.0 now.`);
    await expect(row(page, name).getByRole('status')).toContainText(`${name}: updated.`);
    await expect(updateButton(page, name)).toHaveCount(0);
    // It matches the controller now, so the card stops saying it is behind.
    await expect(card(page, name).getByText('Behind', { exact: true })).toHaveCount(0);
    await expectNoReload(page);

    // And it is the controller's word too: the attempt is closed as succeeded.
    await expect
      .poll(async () => (await hostOnServer(page, credentials)).update?.state)
      .toBe('succeeded');
  });
});

test('a failed update shows the helper’s words as text and can be tried again', async ({
  page,
}) => {
  await withHost(page, SELF_UPDATE, async ({ credentials, name }) => {
    await goto(page, '/hosts', 'Hosts');
    await updateButton(page, name).click();
    await confirmation(page, name)
      .getByRole('button', { name: `Update to ${TARGET}` })
      .click();
    await expect(row(page, name)).toContainText('Updating');

    const attempt = (await hostOnServer(page, credentials)).update?.attempt_id ?? '';
    expect(attempt).toMatch(/^upd/);
    const said =
      'The checksum of the release did not match. <b>Look at</b> journalctl -u zoomies-update.';
    await beat(page, credentials, {
      update: {
        id: attempt,
        ok: false,
        tag: TARGET,
        error: said,
        finished_at: new Date().toISOString(),
      },
    });

    await expect(row(page, name)).toContainText('Failed');
    await expect(row(page, name)).toContainText(said);
    // Text and never markup.
    await expect(row(page, name).locator('b')).toHaveCount(0);
    await expect(row(page, name).getByRole('status')).toContainText(`${name}: the update failed.`);
    // The host is still behind, so it can be asked again.
    await expect(tryAgainButton(page, name)).toBeEnabled();
    await expect(tryAgainButton(page, name)).toHaveText('Try again');
  });
});

test('a failed update raises a problem that opens the host, where the update is', async ({
  page,
}) => {
  await withHost(page, SELF_UPDATE, async ({ credentials, name }) => {
    await goto(page, '/hosts', 'Hosts');
    await updateButton(page, name).click();
    await confirmation(page, name)
      .getByRole('button', { name: `Update to ${TARGET}` })
      .click();
    await expect(row(page, name)).toContainText('Updating');
    const attempt = (await hostOnServer(page, credentials)).update?.attempt_id ?? '';
    await beat(page, credentials, {
      update: {
        id: attempt,
        ok: false,
        tag: TARGET,
        error: 'The update helper could not verify the release.',
        finished_at: new Date().toISOString(),
      },
    });
    await expect(row(page, name)).toContainText('Failed');

    await page.getByRole('button', { name: /^Problems\./ }).click();
    const drawer = page.getByRole('dialog', { name: 'Problems' });
    const problem = drawer
      .getByRole('listitem')
      .filter({ hasText: 'host.update_failed' })
      .filter({ hasText: name });
    await expect(problem).toBeVisible({ timeout: 20_000 });
    await problem.getByRole('link', { name: 'Open the host' }).click();

    await expect(page).toHaveURL(new RegExp(`/hosts/${credentials.host_id}$`));
    // The host's own page holds the same row, and the same way to ask again.
    await expect(row(page, name)).toContainText('Failed');
    await expect(tryAgainButton(page, name)).toBeVisible();
  });
});

test('a refusal is a message in the controller’s words and the card is as it was', async ({
  page,
}) => {
  await withHost(page, SELF_UPDATE, async ({ credentials, name }) => {
    // A controller fenced for recovery refuses while the card still offers the
    // update, because the card is not told when the controller may not act.
    await page.route(`**/api/v1/hosts/${credentials.host_id}/update`, (route) =>
      route.fulfill({
        status: 409,
        json: {
          error: {
            code: 'conflict',
            message: 'this controller may not act right now, so it starts no update',
          },
        },
      }),
    );
    await goto(page, '/hosts', 'Hosts');
    await updateButton(page, name).click();
    await confirmation(page, name)
      .getByRole('button', { name: `Update to ${TARGET}` })
      .click();

    await expect(confirmation(page, name)).toBeHidden();
    const toast = page.locator('.toast').filter({ hasText: `${name} was not asked to update` });
    await expect(toast).toContainText('This controller may not act right now');
    // Nothing was claimed: the card still offers the update, and holds focus on it.
    await expect(row(page, name)).toContainText('Can be updated');
    await expect(row(page, name)).not.toContainText('Updating');
    await expect(updateButton(page, name)).toBeEnabled();
    await expect(updateButton(page, name)).toBeFocused();
  });
});

/** The card's sentence for a host the mode alone keeps from being updated, as the controller words it. */
const MODE_OFF =
  'Updating is off, so hosts are not updated from here. Somebody with the platform role can turn it on by setting updates.mode to manual or auto on the Configuration page.';

/** Serve one host as a controller with updating off renders it: the fixture pins its mode to manual. */
async function withUpdatingOff(page: Page, hostID: string): Promise<void> {
  const off = (host: Record<string, unknown>) =>
    host.id === hostID
      ? { ...host, update: { state: 'none', reason: MODE_OFF, can_update: false, attempt_id: '' } }
      : host;
  await page.route(/\/api\/v1\/hosts(\?.*)?$/, async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as { items?: Record<string, unknown>[] };
    return route.fulfill({ response, json: { ...body, items: (body.items ?? []).map(off) } });
  });
  // And no stream, or a host frame from the real controller replaces the rewrite.
  await page.route('**/api/v1/events*', (route) =>
    route.fulfill({
      status: 200,
      headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
      body: '',
    }),
  );
  await page.route(`**/api/v1/hosts/${hostID}`, async (route) => {
    const response = await route.fetch();
    return route.fulfill({
      response,
      json: off((await response.json()) as Record<string, unknown>),
    });
  });
}

test('with updating off the card says so, and its button cannot be pressed', async ({ page }) => {
  await withHost(page, SELF_UPDATE, async ({ credentials, name }) => {
    const starts = countStarts(page);
    await withUpdatingOff(page, credentials.host_id);
    await goto(page, '/hosts', 'Hosts');
    // The reason is text on the card, not a tooltip: a phone has no hover.
    await expect(row(page, name)).toContainText('Updating is off');
    await expect(row(page, name)).toContainText('updates.mode');
    await expect(updateButton(page, name)).toBeDisabled();
    await updateButton(page, name).click({ force: true });
    await expect(confirmation(page, name)).toHaveCount(0);
    // The command beneath the card is still the way.
    await expect(card(page, name).locator('details.upgrade > summary')).toBeVisible();

    await goto(page, `/hosts/${credentials.host_id}`, name);
    await expect(row(page, name)).toContainText('Updating is off');
    await expect(updateButton(page, name)).toBeDisabled();
    expect(starts(), 'nothing was asked of the controller').toBe(0);
  });
});

test('a heartbeat that changes nothing about the update leaves the host’s shape alone', async ({
  page,
}) => {
  // The fleet's shape moves a page's grids to fetch again, so a block that
  // changed on every beat would cost a round trip per host per heartbeat.
  // Compared on the documents the real controller sent, before and after.
  await withHost(page, SELF_UPDATE, async ({ credentials, name }) => {
    await goto(page, '/hosts', 'Hosts');
    await updateButton(page, name).click();
    await confirmation(page, name)
      .getByRole('button', { name: `Update to ${TARGET}` })
      .click();
    await expect(row(page, name)).toContainText('Updating');

    await beat(page, credentials);
    const before = (await hostOnServer(page, credentials)) as Record<string, unknown>;
    for (let i = 0; i < 3; i += 1) await beat(page, credentials);
    const after = (await hostOnServer(page, credentials)) as Record<string, unknown>;
    expect(after.update, 'the block is the same one').toEqual(before.update);
    expect(shapeDiffers(before, after), 'three heartbeats are the same fleet').toBe(false);

    // And the attempt closing is news, which a grid is right to fetch for.
    await beat(page, credentials, { version: '1.3.0' });
    const closed = (await hostOnServer(page, credentials)) as Record<string, unknown>;
    expect(shapeDiffers(after, closed)).toBe(true);
  });
});

test('the host’s own page offers the same update', async ({ page }) => {
  await withHost(page, SELF_UPDATE, async ({ credentials, name }) => {
    await goto(page, `/hosts/${credentials.host_id}`, name);
    await expect(page.getByRole('region', { name: 'Agent update', exact: true })).toBeVisible();
    await expect(row(page, name)).toContainText('Can be updated');
    await updateButton(page, name).click();
    await expect(confirmation(page, name)).toBeVisible();
    await confirmation(page, name)
      .getByRole('button', { name: `Update to ${TARGET}` })
      .click();
    await expect(row(page, name)).toContainText('Updating');
    await expect(row(page, name)).toBeFocused();

    // An operator has no panel for it.
    await page.unroute('**/api/v1/auth/session').catch(() => undefined);
    await signedInAs(page, 'operator');
    await reload(page, name);
    await expect(page.getByRole('region', { name: 'Agent update', exact: true })).toHaveCount(0);
  });
});

/** What the accessibility pass holds the Hosts page to while an update is in any state. */
async function expectAccessibleStructure(page: Page, region: Locator): Promise<void> {
  await expect(page.getByRole('heading', { level: 1 })).toHaveCount(1);
  await expect(page.getByRole('main')).toHaveCount(1);
  await expect(page.getByRole('button', { name: /^$/ }), 'every button has a name').toHaveCount(0);
  await expect(
    page.locator('[tabindex]:not([tabindex="0"]):not([tabindex="-1"])'),
    'the tab order is the document order',
  ).toHaveCount(0);
  // The state is announced from a region that is always there.
  await expect(region.getByRole('status')).toHaveCount(1);
  // The region has a name, and the button inside it names the host.
  await expect(region).toHaveAccessibleName(/^Agent update for /);
  const duplicates = await page.evaluate(() => {
    const ids = [...document.querySelectorAll('[id]')].map((el) => el.id);
    return ids.filter((id, i) => ids.indexOf(id) !== i);
  });
  expect(duplicates, 'no id is used twice').toEqual([]);
}

test('the update row and its confirmation have names, a live region and no stray tab stops', async ({
  page,
}) => {
  await withHost(page, SELF_UPDATE, async ({ credentials, name }) => {
    await goto(page, '/hosts', 'Hosts');
    await expect(updateButton(page, name)).toBeVisible();
    await expectAccessibleStructure(page, row(page, name));

    await updateButton(page, name).click();
    await expect(confirmation(page, name)).toHaveAttribute('aria-modal', 'true');
    await expect(confirmation(page, name).getByRole('button', { name: 'Cancel' })).toBeVisible();
    await page.keyboard.press('Escape');

    await updateButton(page, name).click();
    await confirmation(page, name)
      .getByRole('button', { name: `Update to ${TARGET}` })
      .click();
    await expect(row(page, name)).toContainText('Updating');
    await expectAccessibleStructure(page, row(page, name));

    const attempt = (await hostOnServer(page, credentials)).update?.attempt_id ?? '';
    await beat(page, credentials, {
      update: {
        id: attempt,
        ok: false,
        tag: TARGET,
        error: 'It failed.',
        finished_at: new Date().toISOString(),
      },
    });
    await expect(row(page, name)).toContainText('Failed');
    await expectAccessibleStructure(page, row(page, name));
  });
});

test('nothing scrolls sideways at 360px with the update offered, in flight, failed or being confirmed', async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 780 });
  await withHost(page, SELF_UPDATE, async ({ credentials, name }) => {
    await goto(page, '/hosts', 'Hosts');
    await expect(updateButton(page, name)).toBeVisible();
    await expectNoSidewaysScroll(page, 'the Hosts page with the update offered');
    const box = await row(page, name).boundingBox();
    expect(
      box && box.x >= 16 - 1 && box.x + box.width <= 360 - 16 + 1,
      `the row keeps its gutters (${JSON.stringify(box)})`,
    ).toBe(true);

    await updateButton(page, name).click();
    const dialogBox = await confirmation(page, name).boundingBox();
    expect(
      dialogBox && dialogBox.x >= 0 && dialogBox.x + dialogBox.width <= 360,
      'the dialog fits',
    ).toBe(true);
    await expectNoSidewaysScroll(page, 'the Hosts page with the confirmation open');
    await confirmation(page, name)
      .getByRole('button', { name: `Update to ${TARGET}` })
      .click();
    await expect(row(page, name)).toContainText('Updating');
    await expectNoSidewaysScroll(page, 'the Hosts page with the update in flight');

    const attempt = (await hostOnServer(page, credentials)).update?.attempt_id ?? '';
    await beat(page, credentials, {
      update: {
        id: attempt,
        ok: false,
        tag: TARGET,
        error:
          'The release could not be verified: the checksum of zoomies_linux_amd64_with_a_very_long_name_that_has_no_break_at_all does not match.',
        finished_at: new Date().toISOString(),
      },
    });
    await expect(row(page, name)).toContainText('Failed');
    await expectNoSidewaysScroll(page, 'the Hosts page after a failed update');
  });
});
