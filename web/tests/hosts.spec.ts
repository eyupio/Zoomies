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
  reload,
} from './support/fixtures';

test.use(browserOverride);

/**
 * What one runner in this fleet asks for, by the rule the Adjust dialog uses:
 * the largest ask across the enabled pools, and twice over for a pool that
 * gives its jobs a daemon of their own, because the backend gives the sidecar
 * the same limits as the runner.
 *
 * Read from the fleet rather than written down here, so a change to the demo
 * data -- or another spec editing a pool on this shared server -- moves the
 * expectation with it instead of breaking it.
 */
async function runnerAsk(page: Page): Promise<{ cpus: number; memoryMb: number }> {
  const pools = (await page.request.get('/api/v1/pools').then((r) => r.json())) as {
    items?: {
      enabled?: boolean;
      docker_mode?: string;
      resources?: { cpus?: number; memory_mb?: number };
    }[];
  };
  let cpus = 0;
  let memoryMb = 0;
  for (const pool of pools.items ?? []) {
    if (pool.enabled !== true) continue;
    const containers = pool.docker_mode === 'dind' ? 2 : 1;
    cpus = Math.max(cpus, (pool.resources?.cpus ?? 0) * containers);
    memoryMb = Math.max(memoryMb, (pool.resources?.memory_mb ?? 0) * containers);
  }
  return { cpus: cpus > 0 ? cpus : 2, memoryMb: memoryMb > 0 ? memoryMb : 4096 };
}

/**
 * The install command. A <pre> carries no role of its own, so it is reached
 * inside the named hand-off panel by the installer it invokes. The credential
 * is shell-quoted, and should stay that way even when this test reads it back.
 */
function installCommand(page: Page) {
  return page
    .getByRole('region', { name: 'Run this on the new host' })
    .locator('pre', { hasText: 'zoomies.sh/install.sh' });
}

/** Read the one argument after --join-token, quoted or not. */
async function joinToken(page: Page): Promise<string | undefined> {
  const command = await installCommand(page).innerText();
  return /--join-token\s+['"]?(zoojoin_[^'"\s]+)/.exec(command)?.[1];
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
  await expect(command).toContainText(`--controller '${origin}'`);
  await expect(command).toContainText('--version dev');
  await expect(page.getByText('Host install channel').locator('..')).toContainText(':dev');
  // What the agent will own on that machine is one click from the command
  // that hands it over, before anybody runs it.
  await expect(
    page
      .getByRole('region', { name: 'Run this on the new host' })
      .getByRole('link', { name: 'written down in the security guide' }),
  ).toHaveAttribute('href', 'https://zoomies.sh/security/#what-the-agent-owns-on-a-host');
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

  const token = await joinToken(page);
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

/**
 * A host that reports no memory left is held and, since the same measurement
 * is what overwhelms it, throttled on the same heartbeat. The card shows the
 * throttle's sentence rather than the hold's, because it says everything the
 * hold's does and what happens next. The memory figure recovers on the next
 * beat; the throttle stays a rung until the host has been calm for five
 * minutes, which is the fleet's promise rather than this test's.
 *
 * The same host's runtime then fails. That used to be a log line on the host
 * and nothing else; one beat puts it on the card, with when the recovery
 * attempt is due and how old the report is, and in the drawer, and the next
 * beat without it clears both. It rides on this host rather than joining one
 * of its own because joins are rate limited per address, and both projects
 * share one controller.
 */
test('host usage distinguishes a connected agent from held new starts and recovers', async ({
  page,
}, testInfo) => {
  await goto(page, '/hosts/new', 'Add a host');
  await page.getByRole('button', { name: 'Get the command' }).click();
  const token = await joinToken(page);
  const name = `usage-host-${Date.now()}`;
  let hostId = '';
  try {
    const join = await page.request.post('/api/v1/agent/join', {
      data: {
        protocol_version: 1,
        join_token: token,
        name,
        capacity: 2,
        os: 'linux',
        arch: 'amd64',
        cpus: 8,
        memory_mb: 16384,
        version: 'dev',
        backends: [{ kind: 'docker', available: true }],
      },
    });
    expect(join.ok()).toBeTruthy();
    const credentials = (await join.json()) as { host_id: string; agent_token: string };
    hostId = credentials.host_id;
    const heartbeat = async (memory: number, runtime?: object) => {
      const response = await page.request.post('/api/v1/agent/heartbeat', {
        headers: { Authorization: `Bearer ${credentials.agent_token}` },
        data: {
          protocol_version: 1,
          usage: { cpu_percent: 20, memory_available_mb: memory },
          ...(runtime ? { runtime } : {}),
        },
      });
      expect(response.ok()).toBeTruthy();
    };
    await heartbeat(0);
    await goto(page, '/hosts', 'Hosts');
    const card = page.getByRole('article', { name, exact: true });
    await expect(card).toContainText('Agent connected');
    await expect(card).toContainText('CPU usage 20%');
    await expect(card).toContainText('0 B memory available');
    // Two slots stepped to one: the controller's sentence names the rung,
    // the measurement and what the running work is doing.
    await expect(card).toContainText(
      "throttled to 1 of 2 slots (step 1 of 3) after sustained pressure: available memory is at or below the host's reserve",
    );
    await expect(card).toContainText('running jobs continue');
    await expect(card).toContainText(/0 of 1 slots in use\s*· throttled from 2/);
    await testInfo.attach('host-pressure-hold', {
      body: await card.screenshot(),
      contentType: 'image/png',
    });
    await heartbeat(8192);
    await expect(card).toContainText('8.0 GB memory available');
    // Still on its rung: calm has to last before a step is given back.
    await expect(card).toContainText('throttled from 2');

    // retry_in is a Go duration on the wire: nanoseconds.
    await heartbeat(8192, {
      failures: 3,
      kind: 'unavailable',
      error: 'backend: not available on this host: no socket at /var/run/docker.sock',
      retry_in: 40_000_000_000,
    });
    const recovering = card.getByTestId('host-runtime-recovering');
    await expect(recovering).toContainText('Runtime recovering: third failure in a row');
    await expect(recovering).toContainText(/Retrying in \d+s/);
    await expect(recovering).toContainText(/Reported (just now|\d+s ago)/);
    await testInfo.attach('host-runtime-recovering', {
      body: await card.screenshot(),
      contentType: 'image/png',
    });
    const problems = (await page.request.get('/api/v1/problems').then((r) => r.json())) as {
      items?: { code?: string; target_id?: string; fix?: string; since?: string }[];
    };
    const entry = (problems.items ?? []).find(
      (p) => p.code === 'host.runtime_recovering' && p.target_id === hostId,
    );
    expect(entry, 'the drawer carries the runtime cooldown').toBeTruthy();
    expect(entry?.fix).toContain('systemctl start docker');
    expect(entry?.since).toBeTruthy();

    // A beat with no report is a runtime that works again.
    await heartbeat(8192);
    await expect(recovering).toHaveCount(0);
  } finally {
    if (hostId) await page.request.delete(`/api/v1/hosts/${hostId}?force=true`);
  }
});

/**
 * A throttle is the fleet's answer to a host that has stopped keeping up, and
 * the card has to carry all of it: that it happened, how far, why, what it
 * costs the running jobs, and how it ends -- plus the one thing an operator
 * can do about it early, once the cause is known and gone.
 */
test('a host under sustained pressure is throttled, says why, and an operator can lift it', async ({
  page,
}, testInfo) => {
  await goto(page, '/hosts/new', 'Add a host');
  await page.getByRole('button', { name: 'Get the command' }).click();
  const token = await joinToken(page);
  const name = `throttle-host-${Date.now()}`;
  let hostId = '';
  try {
    const join = await page.request.post('/api/v1/agent/join', {
      data: {
        protocol_version: 1,
        join_token: token,
        name,
        capacity: 4,
        os: 'linux',
        arch: 'amd64',
        cpus: 8,
        memory_mb: 16384,
        version: 'dev',
        backends: [{ kind: 'docker', available: true }],
      },
    });
    expect(join.ok()).toBeTruthy();
    const credentials = (await join.json()) as { host_id: string; agent_token: string };
    hostId = credentials.host_id;
    // A load average of twenty on eight CPUs is a runnable queue the machine
    // is not draining, and it steps the throttle up on the first beat: memory
    // is left healthy so the sentence names the load and nothing else.
    const beat = await page.request.post('/api/v1/agent/heartbeat', {
      headers: { Authorization: `Bearer ${credentials.agent_token}` },
      data: {
        protocol_version: 1,
        usage: { cpu_percent: 60, memory_available_mb: 8192, load_average_1m: 20 },
      },
    });
    expect(beat.ok()).toBeTruthy();

    await goto(page, '/hosts', 'Hosts');
    const card = page.getByRole('article', { name, exact: true });
    // The badge carries the step in its title, so hovering answers "how bad".
    await expect(card.getByText('Throttled', { exact: true })).toHaveAttribute(
      'title',
      /^Throttled, step 1 of 3/,
    );
    await expect(card).toContainText(
      "throttled to 3 of 4 slots (step 1 of 3) after sustained pressure: the 1-minute load average is 20.0, at least twice the host's 8 CPUs",
    );
    await expect(card).toContainText('the throttle lifts one step after 5m of calm');
    await expect(card).toContainText(/0 of 3 slots in use\s*· throttled from 4/);
    await expect(card).toContainText('load 20');
    await testInfo.attach('host-throttled', {
      body: await card.screenshot(),
      contentType: 'image/png',
    });

    // Lifting it is a menu action, and the card follows without a reload.
    await plantMarker(page);
    await card.getByRole('button', { name: /Actions for/ }).click();
    await page.getByRole('menuitem', { name: 'Lift the throttle' }).click();
    await expect(card).toContainText(/0 of 4 slots in use/);
    await expect(card).not.toContainText('throttled from');
    await expect(card.getByTitle(/^Throttled, step/)).toHaveCount(0);
    await expect(page.getByText(`Throttle lifted on ${name}`)).toBeVisible();
    await expectNoReload(page);

    // And the controller agrees: the row is clear, not just the card.
    const read = await page.request.get(`/api/v1/hosts/${hostId}`);
    const host = (await read.json()) as { throttle?: unknown; effective_capacity: number };
    expect(host.throttle).toBeUndefined();
    expect(host.effective_capacity).toBe(4);
  } finally {
    if (hostId) await page.request.delete(`/api/v1/hosts/${hostId}?force=true`);
  }
});

test('runner capacity is adjustable from the host card without opening the full editor', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  const card = page.getByRole('article', { name: 'demo-builder-1', exact: true });
  let patched: Record<string, unknown> | null = null;
  await page.route('**/api/v1/hosts/*', async (route) => {
    if (route.request().method() !== 'PATCH') {
      await route.continue();
      return;
    }
    patched = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
  });

  await card.getByRole('button', { name: 'Adjust', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Adjust demo-builder-1' });
  await dialog.getByRole('spinbutton', { name: 'Maximum runners on this host' }).fill('7');
  // The slider is the same setting: it followed the typed figure.
  await expect(dialog.getByRole('slider', { name: 'Runner slots' })).toHaveAttribute(
    'aria-valuetext',
    '7 runners',
  );
  await dialog.getByRole('button', { name: 'Save changes' }).click();

  await expect(dialog).toBeHidden();
  // The reserve travels with the capacity, because the host reports both
  // figures and the dialog offers both. Its value is whatever the host had
  // -- another spec on this shared server may have set it -- so only the
  // figure this test typed is pinned.
  await expect.poll(() => patched).toMatchObject({ capacity: 7 });
  expect(Object.keys(patched ?? {}).sort()).toEqual([
    'capacity',
    'reserve_cpus',
    'reserve_disk_mb',
    'reserve_memory_mb',
  ]);
});

test('a refused capacity is withdrawn when the figure is changed, and only that figure', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  const card = page.getByRole('article', { name: 'demo-builder-1', exact: true });
  await page.route('**/api/v1/hosts/*', async (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback();
    await route.fulfill({
      status: 422,
      contentType: 'application/json',
      body: JSON.stringify({
        error: { code: 'validation_failed', message: 'that capacity is refused' },
        errors: [
          { field: 'capacity', message: 'This host cannot hold that many.' },
          { field: 'reserve_cpus', message: 'That reserve leaves nothing for runners.' },
        ],
      }),
    });
  });

  await card.getByRole('button', { name: 'Adjust', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Adjust demo-builder-1' });
  const capacity = dialog.getByRole('spinbutton', { name: 'Maximum runners on this host' });
  const save = dialog.getByRole('button', { name: 'Save changes' });
  await capacity.fill('6');
  await save.click();
  await expect(dialog).toContainText('This host cannot hold that many.');
  await expect(save).toBeDisabled();

  await capacity.fill('5');
  await expect(dialog).not.toContainText('This host cannot hold that many.');
  // Only the answer about the capacity is withdrawn; the one about the reserve
  // is about something the operator has not touched.
  await expect(dialog).toContainText('That reserve leaves nothing for runners.');
  await expect(save).toBeEnabled();
});

/*
 * The adjust dialog says what fits and marks it on each slider. Past that
 * mark it warns rather than refuses -- the operator who knows the jobs are
 * light is right to go past it -- and one press puts everything back on the
 * recommendation.
 */
test('the adjust dialog recommends, warns past the recommendation and can set itself to it', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  // demo-builder-1: 16 cores, 32 GB. What fits follows from two things the
  // fixture owns rather than this test: whatever reserve the host has --
  // another spec on this shared server may have set one -- and what a runner
  // in this fleet asks for, which is the dialog's own rule and is read here
  // the same way rather than copied as a number that goes stale.
  const builder = await page.request
    .get('/api/v1/hosts')
    .then((r) => r.json() as Promise<{ items: Record<string, number | string>[] }>)
    .then((page) => page.items.find((h) => h.name === 'demo-builder-1'));
  if (!builder) throw new Error('demo-builder-1 is not in the fixture');
  const ask = await runnerAsk(page);
  const reservedCores = Number(builder.reserve_cpus ?? 0);
  const reservedMb = Number(builder.reserve_memory_mb ?? 0);
  const fits = Math.max(
    1,
    Math.min(
      Math.floor((16 - reservedCores) / ask.cpus),
      Math.floor((32768 - reservedMb) / ask.memoryMb),
    ),
  );
  // With the recommended core and 3 GB held back, which is where the button
  // below puts them.
  const recommended = Math.max(
    1,
    Math.min(Math.floor((16 - 1) / ask.cpus), Math.floor((32768 - 3072) / ask.memoryMb)),
  );
  const card = page.getByRole('article', { name: 'demo-builder-1', exact: true });
  await card.getByRole('button', { name: 'Adjust', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Adjust demo-builder-1' });
  await expect(dialog).toContainText(`room for ${fits} runner`);

  const slots = dialog.getByRole('slider', { name: 'Runner slots' });
  const cores = dialog.getByRole('slider', { name: 'Cores held back' });
  const memory = dialog.getByRole('slider', { name: 'Memory held back' });
  await expect(slots).toHaveAttribute('aria-valuetext', `${builder.capacity} runners`);

  // Past the recommendation on slots: the callout says by how much.
  await dialog.getByRole('spinbutton', { name: 'Maximum runners on this host' }).fill('12');
  const warning = dialog.getByRole('status').filter({ hasText: 'Above the recommendation' });
  await expect(warning).toContainText(`12 runners would ask for ${12 * ask.cpus} cores`);

  // Too much held back: a different sentence, on the reserve. End is the
  // last notch, all but one core.
  await cores.focus();
  await page.keyboard.press('End');
  await expect(cores).toHaveAttribute('aria-valuetext', '15 cores');
  await expect(
    dialog.getByRole('status').filter({ hasText: 'More cores held back than recommended' }),
  ).toBeVisible();

  // One press: everything back on the mark, and the warnings gone. The
  // reserve takes a core, so the capacity that fits can be one fewer.
  await dialog.getByRole('button', { name: 'Set to recommendations' }).click();
  await expect(dialog).toContainText(`room for ${recommended} runner`);
  await expect(slots).toHaveAttribute(
    'aria-valuetext',
    recommended === 1 ? '1 runner' : `${recommended} runners`,
  );
  await expect(cores).toHaveAttribute('aria-valuetext', '1 core');
  await expect(memory).toHaveAttribute('aria-valuetext', '3 GB');
  await expect(dialog.getByRole('status')).toHaveCount(0);
  await expect(dialog.getByRole('button', { name: 'Set to recommendations' })).toBeDisabled();
  // A step past it re-enables the button: the recommendation is a place to
  // come back to, not a lock.
  await memory.focus();
  await page.keyboard.press('ArrowRight');
  await expect(memory).toHaveAttribute('aria-valuetext', '4 GB');
  await expect(dialog.getByRole('button', { name: 'Set to recommendations' })).toBeEnabled();
});

/*
 * The capacity map is one chart of every host, and the operator chooses
 * which hosts and which figures are on it. The choice survives a reload:
 * somebody watching two machines out of forty wants them there tomorrow.
 */
test('the host capacity map toggles hosts and measurements, and remembers the choice', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  const map = page.getByRole('region', { name: 'Host capacity map', exact: true });
  const chart = map.getByRole('img').first();
  // Two measurements on by default, across every host the fixture has: the
  // demo fleet and whatever the diagnostics fixture adds beside it.
  const hostList = await page.request
    .get('/api/v1/hosts')
    .then((r) => r.json() as Promise<{ items: { id: string }[] }>);
  const n = hostList.items.length;
  expect(n).toBeGreaterThanOrEqual(3);
  await expect(chart).toHaveAttribute(
    'aria-label',
    new RegExp(`^${2 * n} lines across ${n} of ${n} hosts`),
  );

  // History came from the samples route: a series for every host, and the
  // demo hosts' measured CPU on it.
  const samples = await page.request
    .get('/api/v1/hosts/samples?window=1h')
    .then((r) => r.json() as Promise<{ items: { host_id: string; cpu_percent?: number }[] }>);
  // Every host now in the fleet has a series; a host another spec deleted
  // keeps its rows until the prune, and is not a host on this chart.
  const sampled = new Set(samples.items.map((s) => s.host_id));
  for (const host of hostList.items) expect(sampled.has(host.id)).toBe(true);
  expect(samples.items.some((s) => typeof s.cpu_percent === 'number')).toBe(true);

  const measurements = map.getByRole('group', { name: 'Measurements shown' });
  await measurements.getByRole('button', { name: 'Memory used' }).click();
  await expect(chart).toHaveAttribute(
    'aria-label',
    new RegExp(`^${3 * n} lines across ${n} of ${n} hosts`),
  );
  await measurements.getByRole('button', { name: 'CPU used' }).click();
  await expect(chart).toHaveAttribute(
    'aria-label',
    new RegExp(`^${2 * n} lines across ${n} of ${n} hosts`),
  );

  const hosts = map.getByRole('group', { name: 'Hosts shown' });
  const arm = hosts.getByRole('button', { name: /^demo-arm-1/ });
  await arm.click();
  await expect(arm).toHaveAttribute('aria-pressed', 'false');
  await expect(chart).toHaveAttribute(
    'aria-label',
    new RegExp(`^${2 * (n - 1)} lines across ${n - 1} of ${n} hosts`),
  );
  // "Only" narrows the chart to one host.
  await hosts.getByRole('button', { name: 'Only' }).first().click();
  await expect(chart).toHaveAttribute('aria-label', new RegExp(`across 1 of ${n} hosts`));

  // The window is a control of the panel too.
  await map.getByRole('button', { name: 'The last hour, minute by minute' }).click();
  await expect(chart).toHaveAttribute('aria-label', /the last hour, minute by minute/);

  await page.reload();
  await page.getByRole('heading', { level: 1, name: 'Hosts' }).waitFor();
  await expect(map.getByRole('img').first()).toHaveAttribute(
    'aria-label',
    new RegExp(`^2 lines across 1 of ${n} hosts, the last hour, minute by minute`),
  );
  // The legend row says which host is on, in words as well as in colour:
  // the first row is demo-arm-1, the hosts being listed by name.
  await expect(hosts.getByRole('button', { name: /^demo-arm-1/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
  await expect(hosts.getByRole('button', { name: /^demo-builder-1/ })).toHaveAttribute(
    'aria-pressed',
    'false',
  );
});

/**
 * A host's disk is the resource that runs out first and says nothing when it
 * does.
 *
 * Every slot on the machine still reads as free, so the fleet keeps placing
 * work there, and the job fails part-way through a checkout or a cache
 * restore -- which reads as a flaky build rather than as a full disk. The
 * agent has measured it since ZF-103; until now it was stored and never shown.
 */
test('a host says how much disk its runners have, and marks the one that is nearly out', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');

  const roomy = page.getByRole('article', { name: 'demo-builder-1', exact: true });
  await expect(roomy).toContainText('GB free of');
  // 717 GB of 1024: the figure is the work directory's filesystem, in the
  // same units the rest of the card uses.
  await expect(roomy).toContainText(/717 GB free of 1,024/);

  // The one at six percent is marked, because "31 GB free" beside "512" is
  // not something anybody reads as urgent on its own.
  const tight = page.getByRole('article', { name: 'demo-builder-2', exact: true });
  const low = tight.locator('.low');
  await expect(low).toHaveText(/31 GB free of 512/);

  // And the roomy one is not marked, or the mark says nothing.
  await expect(roomy.locator('.low')).toHaveCount(0);
});

/**
 * Slots say whether the fleet will place another runner here; the committed
 * figures say whether the machine can carry it.
 *
 * They are different questions, and only the second one explains a host with
 * free slots taking nothing. The reservation has decided placement since
 * ZF-103b and appeared nowhere: an operator could see what a machine was and
 * what it had left, but not what the fleet had already promised away on it.
 */
test('a host shows what the fleet has committed on it, and lets an operator hold some back', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');

  const card = page.getByRole('article', { name: 'demo-builder-1', exact: true });
  const committed = card.getByRole('region', { name: /Resources committed/ });
  await expect(committed).toBeVisible();
  // 16 CPUs and 32 GB, less the floors, against what the runners on it hold:
  // 14.2 CPUs rather than 16. The scheduler keeps a twentieth of the machine
  // back for the daemon and the agent, and this is the demo's embedded host --
  // the controller runs on it -- so it keeps a core back for the controller too.
  await expect(committed).toContainText('CPU');
  await expect(committed).toContainText('Memory');
  await expect(committed).toContainText(/of 14\.2/);

  // And the reserve is settable from the same card, in Adjust -- the one place
  // that owns this host's resources -- against the figures it has reported.
  await plantMarker(page);
  await card.getByRole('button', { name: 'Adjust', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Adjust demo-builder-1' });
  await expect(dialog).toBeVisible();
  const memory = dialog.getByRole('slider', { name: 'Memory held back' });
  await expect(memory).toBeVisible();
  // 8 GB is a notch on a 32 GB machine; the slider is driven by the keyboard
  // because that is what an operator without a mouse has.
  await memory.focus();
  await page.keyboard.press('Home');
  for (let i = 0; i < 20 && (await memory.getAttribute('aria-valuetext')) !== '8 GB'; i++) {
    await page.keyboard.press('ArrowRight');
  }
  await expect(memory).toHaveAttribute('aria-valuetext', '8 GB');
  await dialog.getByRole('button', { name: 'Save changes' }).click();
  await expect(dialog).not.toBeVisible();

  // 32 GB less the 8 GB held back: the bar is drawn against what may actually
  // be placed on, not against the machine.
  await expect(committed).toContainText(/of 24 GB/);
  await expectNoReload(page);
});

/**
 * A reserve larger than the machine leaves nothing placeable, and is what
 * typing megabytes where you meant gigabytes looks like.
 *
 * The sliders cannot reach it -- their last notch leaves room to place on --
 * so this is pinned where a hand-written request still arrives: the API
 * refuses it, and the refusal says what is wrong rather than clamping.
 */
test('a reserve that would leave nothing to place on is refused', async ({ page }) => {
  const hosts = await page.request
    .get('/api/v1/hosts')
    .then((r) => r.json() as Promise<{ items: Record<string, unknown>[] }>);
  const host = hosts.items.find((h) => h.name === 'demo-builder-2');
  expect(host, 'demo-builder-2 is in the fixture').toBeTruthy();
  const refused = await page.request.patch(`/api/v1/hosts/${String(host?.id)}`, {
    data: { reserve_memory_mb: Number(host?.memory_mb ?? 0) },
  });
  expect(refused.status()).toBe(422);
  expect(await refused.text()).toContain('nothing to place on');
});

/**
 * The two settings a host has, each reached from the thing it describes: the
 * resources from Adjust beside the slot bar, and the tags from the block
 * that lists them. Capacity was on both once, with two different ideas of a
 * good number, and only one of them knew what a runner in this fleet asks for.
 * A tag is a label stored on the host, so what is sent is still `labels`.
 */
test('the tags dialog edits tags and nothing else', async ({ page }) => {
  await goto(page, '/hosts', 'Hosts');
  const card = page.getByRole('article', { name: 'demo-builder-1', exact: true });
  let patched: Record<string, unknown> | null = null;
  await page.route('**/api/v1/hosts/*', async (route) => {
    if (route.request().method() !== 'PATCH') {
      await route.continue();
      return;
    }
    patched = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
  });

  await card.getByRole('button', { name: /Actions for/ }).click();
  await page.getByRole('menuitem', { name: 'Edit tags' }).click();
  const dialog = page.getByRole('dialog', { name: 'Tags on demo-builder-1' });
  await expect(dialog).toBeVisible();
  // No resource setting in here at all: that is Adjust's, and the dialog says so.
  await expect(dialog).toContainText('Capacity and the reserve are under Adjust');
  await expect(dialog.getByRole('slider')).toHaveCount(0);
  await expect(dialog.getByRole('spinbutton')).toHaveCount(0);

  await dialog.getByRole('button', { name: 'Save changes' }).click();
  await expect(dialog).toBeHidden();
  expect(Object.keys(patched ?? {})).toEqual(['labels']);
});

/**
 * The three ways a join is refused, at the route an agent calls.
 *
 * A token is single-use and lasts an hour by default, so "already used" is an
 * ordinary failure rather than an edge case -- and the page that mints the
 * token is where an operator comes back to when it happens. Each refusal is a
 * sentence rather than a status code.
 *
 * Expiry is pinned at the API tier instead: making a token old enough means
 * forging the row, since `CreateJoinToken` reads a non-positive TTL as "use the
 * default", and a browser has no way to do that.
 */
test('a token that is spent or nonsense is refused with a reason', async ({ page }) => {
  await goto(page, '/hosts/new', 'Add a host');
  await page.getByRole('button', { name: 'Get the command' }).click();
  const token = await joinToken(page);
  expect(token, 'the command carries the token').toBeTruthy();

  const join = (joinToken: string, name: string) =>
    page.request.post('/api/v1/agent/join', {
      data: {
        protocol_version: 1,
        join_token: joinToken,
        name,
        capacity: 1,
        os: 'linux',
        arch: 'amd64',
        version: 'e2e',
        backends: [{ kind: 'docker', available: true }],
      },
    });

  const name = `e2e-refusal-${Date.now()}`;
  let hostId = '';
  try {
    // Nonsense first, so nothing has been spent: a truncated paste, or the
    // token id from the Hosts page rather than the secret shown once beside it.
    const garbage = await join('zoojoin_notatoken', `${name}-garbage`);
    expect(garbage.status(), 'a token that never existed').toBe(422);
    expect(JSON.stringify(await garbage.json())).toMatch(/join token/i);

    // Then the real one, which works exactly once.
    const first = await join(token as string, name);
    expect(first.ok(), 'the first join succeeded').toBeTruthy();
    hostId = ((await first.json()) as { host_id: string }).host_id;

    const second = await join(token as string, `${name}-second`);
    expect(second.status(), 'the same token twice').toBe(422);
    expect(JSON.stringify(await second.json())).toMatch(/join token/i);
  } finally {
    if (hostId) await page.request.delete(`/api/v1/hosts/${hostId}?force=true`);
  }
});

test('an older remote agent offers a copyable upgrade command without a join token', async ({
  page,
}) => {
  const command =
    "curl -fsSL https://zoomies.sh/install.sh | sh -s -- --upgrade --mode agent --version 'v1.2.3'";
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: async (value: string) => sessionStorage.setItem('copied-upgrade', value),
      },
    });
  });
  await page.route(/\/api\/v1\/hosts(?:\?.*)?$/, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.items = body.items.map((host: { name: string }) =>
      host.name === 'demo-builder-1'
        ? {
            ...host,
            embedded: false,
            version: '1.0',
            version_skew: 'behind',
            upgrade_version: '1.2.3',
            upgrade_command: command,
            upgrade_note: 'Run this on the host. Its existing credentials are kept.',
          }
        : host,
    );
    await route.fulfill({ json: body });
  });
  // And no stream: the host this test invents is a rewritten list response,
  // and the demo fleet's own heartbeat publishes the real one every thirty
  // seconds. A `host.updated` frame landing between the page and the click
  // takes the upgrade notice off the card for good, which is a test that
  // passes or fails on when in the half-minute it happened to run.
  await page.route('**/api/v1/events*', (route) =>
    route.fulfill({
      status: 200,
      headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
      body: '',
    }),
  );
  await goto(page, '/hosts', 'Hosts');
  const card = page.getByRole('article', { name: 'demo-builder-1', exact: true });
  // Folded away until it is asked for, so that a fleet due an upgrade is still
  // a grid of comparable cards rather than a page of repeated instructions.
  const reveal = card.getByText('Update this agent to 1.2.3');
  await expect(card.getByText(command)).toBeHidden();
  await reveal.click();
  await expect(card.getByText(command)).toBeVisible();
  await card.getByRole('button', { name: 'Copy the upgrade command' }).click();
  await expect
    .poll(() => page.evaluate(() => sessionStorage.getItem('copied-upgrade')))
    .toBe(command);
  await expect(card).not.toContainText('--join-token');
});

/*
 * The map's shortest windows are drawn finer than the controller's minute
 * samples, ten seconds to a point, so an operator watching a job land can
 * see each heartbeat rather than the last of every two. The history behind
 * them is still the samples route, asked for the same span.
 */
test('the host capacity map has windows down to the last minute, drawn ten seconds to a point', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  const map = page.getByRole('region', { name: 'Host capacity map', exact: true });
  const chart = map.getByRole('img').first();
  const windows = map.getByRole('group', { name: 'Window' });
  await expect(windows.getByRole('button')).toHaveText([
    '1m',
    '5m',
    '10m',
    '1h',
    '6h',
    '24h',
    '7d',
  ]);

  const fetched = page.waitForRequest((r) => r.url().includes('/api/v1/hosts/samples?window=5m'));
  await windows.getByRole('button', { name: 'The last 5 minutes, in 10-second points' }).click();
  await fetched;
  await expect(chart).toHaveAttribute('aria-label', /the last 5 minutes, in 10-second points/);
  // Thirty points across five minutes, and the timeline control steps through them.
  await expect(map.getByRole('slider', { name: 'Inspect a moment' })).toHaveAttribute('max', '29');
  // A moment in a sub-minute window is named to the second.
  await expect(map.locator('output')).toHaveText(/^\d{1,2}:\d{2}:\d{2}/);

  await windows.getByRole('button', { name: 'The last minute, in 10-second points' }).click();
  await expect(map.getByRole('slider', { name: 'Inspect a moment' })).toHaveAttribute('max', '5');
  await expect(chart).toHaveAttribute('aria-label', /the last minute, in 10-second points/);
});

/*
 * The map has two layouts. Every host on one plot is for asking which
 * machine is the busy one; a plot per host is for asking what one machine
 * has been doing, and the plots share one x axis and one crosshair so a
 * moment is still read across the fleet. The choice is remembered.
 */
test('the host capacity map can draw a chart per host, sharing one crosshair', async ({ page }) => {
  await goto(page, '/hosts', 'Hosts');
  const map = page.getByRole('region', { name: 'Host capacity map', exact: true });
  const n = await page.request
    .get('/api/v1/hosts')
    .then((r) => r.json() as Promise<{ items: { id: string }[] }>)
    .then((h) => h.items.length);
  const layout = map.getByRole('group', { name: 'Layout' });
  await expect(layout.getByRole('button', { name: /^Overlay/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  );

  await layout.getByRole('button', { name: /^Per host/ }).click();
  // One chart for each host, named for it, and the overlay's chart is gone.
  const charts = map.getByRole('img', { name: /lines, the last 24 hours/ });
  await expect(charts).toHaveCount(n);
  await expect(map.getByRole('img', { name: /^demo-arm-1: 2 lines/ })).toBeVisible();
  await expect(map.getByRole('img', { name: /lines across/ })).toHaveCount(0);

  // Hovering one chart draws the crosshair on all of them, at the same moment.
  const first = charts.first();
  const box = await first.boundingBox();
  await first.hover({ position: { x: box!.width * 0.4, y: box!.height * 0.5 } });
  await expect(map.locator('.cursor')).toHaveCount(n);
  const xs = await map
    .locator('.cursor')
    .evaluateAll((els) => els.map((e) => e.getAttribute('x1')));
  expect(new Set(xs).size).toBe(1);
  await expect(map.locator('output')).toContainText(/\d{1,2}:\d{2}/);
  await page.mouse.move(0, 0);

  // The rows still head their charts, and switching a host off takes its chart.
  const hosts = map.getByRole('group', { name: 'Hosts shown' });
  await hosts.getByRole('button', { name: /^demo-arm-1/ }).click();
  await expect(charts).toHaveCount(n - 1);
  await hosts.getByRole('button', { name: /^demo-arm-1/ }).click();
  await expect(charts).toHaveCount(n);

  await page.reload();
  await page.getByRole('heading', { level: 1, name: 'Hosts' }).waitFor();
  await expect(layout.getByRole('button', { name: /^Per host/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
  await expect(charts).toHaveCount(n);
  await layout.getByRole('button', { name: /^Overlay/ }).click();
  await expect(map.getByRole('img', { name: /lines across/ })).toHaveCount(1);
});

/*
 * Twenty lines on one chart are only readable if one can be singled out.
 * Pointing at a host's row brings its lines forward and steps the rest
 * back; pointing at a measurement's chip does the same for that
 * measurement across every host; and the newest value of the lead
 * measurement is written at the end of every line, so the chart can be read
 * from its right-hand edge alone.
 */
test('the host capacity map singles out a host or a measurement under the pointer, and labels every line end', async ({
  page,
}) => {
  await goto(page, '/hosts', 'Hosts');
  const map = page.getByRole('region', { name: 'Host capacity map', exact: true });
  const n = await page.request
    .get('/api/v1/hosts')
    .then((r) => r.json() as Promise<{ items: { id: string }[] }>)
    .then((h) => h.items.length);
  const hosts = map.getByRole('group', { name: 'Hosts shown' });
  const dim = map.locator('.series.dim');
  const lit = map.locator('.series:not(.dim)');
  await expect(lit).toHaveCount(2 * n);
  await expect(dim).toHaveCount(0);

  await hosts.getByRole('button', { name: /^demo-builder-1/ }).hover();
  await expect(lit).toHaveCount(2);
  await expect(dim).toHaveCount(2 * (n - 1));

  const chips = map.getByRole('group', { name: 'Measurements shown' });
  await chips.getByRole('button', { name: 'Runner slots' }).hover();
  await expect(lit).toHaveCount(n);
  // A chip that is not on the chart singles out nothing.
  await chips.getByRole('button', { name: 'Disk used' }).hover();
  await expect(dim).toHaveCount(0);
  await page.mouse.move(0, 0);
  await expect(dim).toHaveCount(0);

  // The end labels say what each host's lead figure is right now, and they
  // agree with the rows beneath, which read the same moment.
  const ends = map.locator('svg text.end');
  await expect(ends).toHaveCount(n);
  // SVG text has no innerText, so the labels are read as content.
  const labels = await ends.evaluateAll((els) => els.map((e) => e.textContent?.trim()));
  const meters = await hosts
    .locator('.meter strong')
    .evaluateAll((els) => els.map((e) => e.textContent?.trim()));
  for (let k = 0; k < n; k++) {
    expect(labels[k]).toMatch(/^\d+%$/);
    // Two meters a row -- CPU then slots -- so the CPU one is every other.
    expect(labels[k]).toBe(meters[2 * k]);
  }

  // Reading a moment from the timeline moves the rows and the headline to it.
  await expect(map.getByText(/hosts? past 85% now/)).toBeVisible();
  await map.getByRole('slider', { name: 'Inspect a moment' }).fill('0');
  await expect(map.getByText(/hosts? past 85% at \d{1,2}:\d{2}/)).toBeVisible();
  await map.getByRole('button', { name: 'Back to now' }).click();
  await expect(map.getByText(/hosts? past 85% now/)).toBeVisible();
});

test('a host whose agent cannot lend CPU says so, until its agent can', async ({ page }) => {
  // Elastic CPU moves a live runner's quota through the agent on its host,
  // and an agent too old to advertise that it can leaves every runner of an
  // elastic pool at its share -- silently, unless the host says so. This
  // joins as such an agent does, claiming nothing, then heartbeats as an
  // upgraded one would.
  await goto(page, '/hosts/new', 'Add a host');
  await page.getByRole('button', { name: 'Get the command' }).click();
  const token = await joinToken(page);
  const name = `old-agent-${Date.now()}`;
  let hostId = '';
  try {
    const join = await page.request.post('/api/v1/agent/join', {
      data: {
        protocol_version: 1,
        join_token: token,
        name,
        capacity: 2,
        os: 'linux',
        arch: 'amd64',
        cpus: 8,
        memory_mb: 16384,
        version: 'dev',
        backends: [{ kind: 'docker', available: true }],
      },
    });
    expect(join.ok()).toBeTruthy();
    const credentials = (await join.json()) as { host_id: string; agent_token: string };
    hostId = credentials.host_id;

    await goto(page, '/hosts', 'Hosts');
    const card = page.getByRole('article', { name, exact: true });
    await expect(card.getByText('Cannot lend CPU')).toBeVisible();

    const beat = await page.request.post('/api/v1/agent/heartbeat', {
      headers: { Authorization: `Bearer ${credentials.agent_token}` },
      data: { protocol_version: 1, features: ['elastic-cpu'] },
    });
    expect(beat.ok()).toBeTruthy();
    await reload(page, 'Hosts');
    await expect(card).toBeVisible();
    await expect(card.getByText('Cannot lend CPU')).toBeHidden();
  } finally {
    if (hostId) await page.request.delete(`/api/v1/hosts/${hostId}?force=true`);
  }
});
