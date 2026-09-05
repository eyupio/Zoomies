/**
 * Adding a host, from the page that does it.
 *
 * The page's promise is that an operator never has to leave it: it comes
 * filled in, hands over one command, and says when the machine has joined.
 * The join itself is made the way an agent makes it -- a POST to the agent
 * route carrying the token the page showed -- so what is proved is the real
 * hand-off, against the real binary, rather than a mock of either half.
 */
import { expect, test, type Page } from '@playwright/test';
import {
  browserOverride,
  expectNoReload,
  FIXTURE,
  goto,
  pageHeading,
  plantMarker,
} from './support/fixtures';

test.use(browserOverride);

/**
 * The install command. A <pre> carries no role of its own, so it is reached
 * by the one thing only it contains: the token the page has just minted.
 */
function installCommand(page: Page) {
  return page.locator('pre', { hasText: '--join-token zoojoin_' });
}

/** The chips offering the labels the seeded pools select hosts by. */
function labelOffers(page: Page) {
  return page.getByRole('group', { name: 'Labels pools here select hosts by' });
}

test('the page comes filled in and hands over a command carrying the token', async ({ page }) => {
  await goto(page, '/hosts/new', 'Add a host');
  const origin = new URL(page.url()).origin;

  // The address this browser reached the controller on, since the test
  // server has no external URL of its own.
  await expect(page.getByRole('textbox', { name: 'Controller address' })).toHaveValue(origin);
  // Capacity is the agent's to decide unless told otherwise.
  await expect(page.getByRole('textbox', { name: 'Capacity' })).toHaveValue('');
  await expect(page.getByRole('textbox', { name: 'Capacity' })).toHaveAttribute(
    'placeholder',
    'Automatic',
  );

  // The seeded pools select hosts by architecture, so both values are on
  // offer; taking one puts it in the editor and takes it off the offer.
  const offers = labelOffers(page);
  await expect(offers.getByRole('button', { name: 'arch=amd64' })).toBeVisible();
  await offers.getByRole('button', { name: 'arch=arm64' }).click();
  await expect(page.getByRole('textbox', { name: 'Label 1 key' })).toHaveValue('arch');
  await expect(page.getByRole('textbox', { name: 'Label 1 value' })).toHaveValue('arm64');
  await expect(offers.getByRole('button', { name: 'arch=arm64' })).toHaveCount(0);
  // A host carries one value per key, so the other offer replaces rather than adds.
  await offers.getByRole('button', { name: 'arch=amd64' }).click();
  await expect(page.getByRole('textbox', { name: 'Label 1 value' })).toHaveValue('amd64');
  await expect(page.getByRole('textbox', { name: 'Label 2 key' })).toHaveCount(0);

  await page.getByRole('button', { name: 'Get the command' }).click();
  await expect(page.getByRole('heading', { name: 'Run this on the new host' })).toBeVisible();
  const command = installCommand(page);
  await expect(command).toContainText('--mode agent');
  await expect(command).toContainText(`--controller ${origin}`);
  // The page has other live regions -- the connection, the problems count --
  // so the waiting view is reached inside the panel that holds it.
  await expect(
    page.getByRole('region', { name: 'Run this on the new host' }).getByRole('status'),
  ).toContainText('Waiting for the host to join');

  // Going back keeps what was typed and revokes the token, so the settings
  // cost one click to fix and no credential is left lying about.
  await page.getByRole('button', { name: 'Discard this token and change the settings' }).click();
  await expect(page.getByRole('heading', { name: 'Describe the host' })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Label 1 value' })).toHaveValue('amd64');
});

test('the page says so the moment the host joins, without a reload', async ({ page }) => {
  await goto(page, '/hosts/new', 'Add a host');
  await labelOffers(page).getByRole('button', { name: 'arch=arm64' }).click();
  await page.getByRole('button', { name: 'Get the command' }).click();

  const token = /--join-token (zoojoin_\S+)/.exec(await installCommand(page).innerText())?.[1];
  expect(token, 'the command carries the token').toBeTruthy();
  await plantMarker(page);

  // Unique, because a host that joins under an existing name takes over its
  // row, and a retry must not inherit a leftover from a failed run.
  const name = `e2e-host-${Date.now()}`;
  let hostId = '';
  try {
    const join = await page.request.post('/api/v1/agent/join', {
      data: {
        protocol_version: 1,
        join_token: token,
        name,
        capacity: 3,
        os: 'linux',
        arch: 'arm64',
        version: 'e2e',
        backends: [{ kind: 'docker', available: true }],
      },
    });
    expect(join.ok(), 'the agent join succeeded').toBeTruthy();
    hostId = ((await join.json()) as { host_id: string }).host_id;

    await expect(page.getByRole('heading', { name: `${name} joined` })).toBeVisible();
    await expect(page.getByText('linux/arm64')).toBeVisible();
    // The token's labels reached the host, and the agent's own capacity stood
    // because the page left it blank.
    await expect(page.locator('dd', { hasText: 'arch=arm64' })).toBeVisible();
    await expect(page.locator('dd', { hasText: '3 runners at once' })).toBeVisible();
    // The arm pool selects arch=arm64 and runs on Docker, which this host offers.
    await expect(page.getByText(`can place runners here: ${FIXTURE.armPool}`)).toBeVisible();
    await expectNoReload(page);
  } finally {
    if (hostId) await page.request.delete(`/api/v1/hosts/${hostId}?force=true`);
  }
});

test('the Hosts page leads here', async ({ page }) => {
  await goto(page, '/hosts', 'Hosts');
  await page.getByRole('link', { name: 'Add a host' }).first().click();
  await expect(pageHeading(page, 'Add a host')).toBeVisible();
});
